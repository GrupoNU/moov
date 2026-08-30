/**
 * Gmail's search language, parsed (L3 epic E3, canon §2.5).
 *
 * # What this module is, and what it deliberately is not
 *
 * It turns the string a user types into a structured {@link ParsedQuery}, and
 * back again. That is all. It does NOT decide what the server can answer — the
 * mapping to a JMAP filter lives in `mail/api.ts`, and the refusals it has to
 * make are listed there against `internal/jmap/mail/query.go`.
 *
 * The split matters because this module is the SINGLE SOURCE OF TRUTH for the
 * query. The options panel, the chips row and the suggestion dropdown all
 * write their state back into the query STRING and re-parse it, rather than
 * keeping a second copy of "is unread checked". A second copy is how a chip
 * ends up saying one thing while the box says another, and the user has no way
 * to tell which one the server was given. Round-tripping through text means
 * the box is always the truth, because the box is the only state there is.
 *
 * {@link formatQuery} is therefore not a convenience: it is the other half of
 * the invariant, and `searchQuery.test.ts` asserts parse→format→parse is a
 * fixed point over the whole grammar.
 *
 * # The operators, and where each one comes from
 *
 * Canon §2.5 lists Gmail's language, citing support.google.com/mail/answer/7190
 * retrieved 2026-08-30. The set implemented here is the plan's E3 subset:
 *
 *   from: to: cc: bcc: subject:   — address and header terms, bare or quoted
 *   has:attachment                 — hasAttachment
 *   is:unread is:read is:starred   — the system flags
 *   in:anywhere in:spam in:trash   — scope, plus in:<folder> by name
 *   label:<name>                   — E8's `$label:` keyword convention
 *   before:/after: YYYY/MM/DD      — absolute date bounds
 *   older_than:/newer_than: 7d     — relative, resolved to instants at parse
 *   larger:/smaller: 5M            — size bounds with K/M/G suffixes
 *   "exact phrase"                 — kept verbatim in the free text
 *   -<operator>                    — negation, ONLY where the server has an
 *                                    inverse (see {@link NEGATABLE_FIELDS})
 *   OR                             — disjunction between groups
 *
 * The long tail Gmail also has (`deliveredto: AROUND +word {} list: filename:
 * rfc822msgid: header:` and the 12 superstars) is deferred BY NAME in the L3
 * plan §6, not forgotten. A term using one of those parses as a
 * {@link UnsupportedTerm} carrying the operator's name, so the UI can say
 * which one it could not use — never silently swallow it, and never quietly
 * demote it to free text where it would match message bodies mentioning the
 * word.
 *
 * # Negation: only where an inverse exists
 *
 * The server refuses the NOT operator on principle — `query.go`'s
 * `translateOperator` explains that a complement is a set no index can
 * produce, so serving it means visiting every row of the account. What it DOES
 * serve are the cheap negations carried by conditions: `notKeyword` for the
 * four IMAP system flags, and `inMailboxOtherThan` to exclude whole folders.
 *
 * So `-is:starred` is real (it becomes `notKeyword:$flagged`) and `-from:ana`
 * is not. The parser accepts the syntax for both — a user typing a minus is
 * not making a syntax error — and marks the unanswerable one as
 * {@link UnsupportedTerm} so the UI renders an honest chip instead of a result
 * list that quietly ignored the exclusion. Ignoring it would show messages the
 * user explicitly asked to hide, which `query.go` calls "a privacy failure,
 * not a missing feature".
 */

// ---------------------------------------------------------------------------
// the grammar's vocabulary
// ---------------------------------------------------------------------------

/** The text-valued operators that name a header field. */
export const TEXT_FIELDS = ["from", "to", "cc", "bcc", "subject"] as const;
export type TextField = (typeof TEXT_FIELDS)[number];

/**
 * The `is:` values this grammar understands.
 *
 * Gmail's list is longer (`is:muted is:important`), and those two arrive with
 * epic E4 and the IA phase respectively. Until then they are unsupported terms
 * by name rather than silently-ignored words.
 */
export const IS_VALUES = ["unread", "read", "starred"] as const;
export type IsValue = (typeof IS_VALUES)[number];

/**
 * The operators a leading `-` may negate.
 *
 * Read directly out of the server's `applyNotKeyword`: "only the IMAP system
 * flags are negatable". Of the flags, only `$flagged` (starred) and `$seen`
 * (read/unread) have a spelling in Gmail's language, and `is:unread` is
 * ALREADY the negation of `$seen` — so the negatable set is exactly the three
 * `is:` values plus `has:attachment`.
 *
 * `has:attachment` is negatable for a different reason: `hasAttachment` is a
 * BOOLEAN condition, so its negation is `hasAttachment:false`, an ordinary
 * value rather than a complement. `query.go` serves it from
 * `messages.has_attachments` either way.
 *
 * `in:` and `label:` are NOT here even though `inMailboxOtherThan` exists. The
 * exclusion is a real server capability, but wiring `-in:spam` to it would
 * fight the server's own default exclusion of Spam and Trash (which the client
 * must not send `inMailboxOtherThan` for — see `api.ts`), and the combination
 * was never measured. Deferred honestly rather than shipped untested.
 */
export const NEGATABLE_FIELDS = ["is", "has"] as const;

/** Units accepted by `older_than:` / `newer_than:` — Gmail's d / m / y. */
const AGE_UNITS: Readonly<Record<string, number>> = {
  d: 1,
  // Gmail's `m` is MONTHS here, not minutes, and `y` is years. The day counts
  // are the calendar approximations Gmail's own operator page implies; the
  // parser computes an instant so the server only ever sees a date.
  m: 30,
  y: 365,
};

/** Size suffixes, binary — the convention `larger:5M` follows in Gmail. */
const SIZE_UNITS: Readonly<Record<string, number>> = {
  k: 1024,
  m: 1024 * 1024,
  g: 1024 * 1024 * 1024,
};

// ---------------------------------------------------------------------------
// the parsed shape
// ---------------------------------------------------------------------------

/** A term the grammar recognised but this server cannot answer. */
export interface UnsupportedTerm {
  /** The operator as the user typed it, e.g. "filename" or "-from". */
  readonly operator: string;
  /** The whole term, so the UI can quote it back and offer to remove it. */
  readonly raw: string;
  /** Why it cannot be sent. */
  readonly reason: UnsupportedReason;
}

export type UnsupportedReason =
  /** The operator is real in Gmail but deferred by name (L3 plan §6). */
  | "deferredOperator"
  /** A `-` on a term whose complement no index can produce (query.go NOT). */
  | "negationUnanswerable"
  /** The operator's value did not parse (a date that is not a date). */
  | "badValue";

/** One conjunctive group: everything ANDed together. */
export interface QueryGroup {
  /** Free text, phrases included, in the order typed. */
  readonly text: string;
  /** Header-field terms. A field may be named once; a repeat is unsupported. */
  readonly fields: Readonly<Partial<Record<TextField, string>>>;
  /** `has:attachment`, or its negation. */
  readonly hasAttachment?: boolean;
  /** `is:unread` / `-is:read` collapse here: true means UNREAD. */
  readonly unread?: boolean;
  /** `is:starred` / `-is:starred`. */
  readonly starred?: boolean;
  /** `in:<name>`, lowercased. `anywhere` is the canon's own scope keyword. */
  readonly inMailbox?: string;
  /** `label:<name>`, verbatim — `encodeLabelKeyword` is api.ts's job. */
  readonly label?: string;
  /** `after:` and `before:`, as ISO-8601 instants (UTC). */
  readonly after?: string;
  readonly before?: string;
  /** `larger:` / `smaller:`, in octets. */
  readonly larger?: number;
  readonly smaller?: number;
}

/** The whole query: one or more OR-ed groups, plus what could not be used. */
export interface ParsedQuery {
  /**
   * The disjunctive branches. A query with no `OR` has exactly one, which is
   * the overwhelmingly common case and the one the mapper keeps flat.
   */
  readonly groups: readonly QueryGroup[];
  /** Terms named but excluded from the filter, each with its reason. */
  readonly unsupported: readonly UnsupportedTerm[];
}

/** The server's `maxOrBranches` (query.go), mirrored so the UI can say so. */
export const MAX_OR_BRANCHES = 4;

/** An empty group, for building on. */
const EMPTY_GROUP: QueryGroup = { text: "", fields: {} };

// ---------------------------------------------------------------------------
// tokenizing
// ---------------------------------------------------------------------------

interface Token {
  /** The token's whole text as typed, for echoing back in a chip. */
  readonly raw: string;
  /** The operator name, lowercased, or undefined for free text. */
  readonly operator?: string;
  /** The value after the colon, unquoted. */
  readonly value: string;
  /** True when a `-` preceded the operator. */
  readonly negated: boolean;
  /** True when the value arrived inside double quotes. */
  readonly quoted: boolean;
}

/**
 * Splits a query into tokens, respecting quotes.
 *
 * Written as a character scanner rather than a regex because quoting interacts
 * with the operator colon: `subject:"quarterly report"` is ONE token whose
 * value contains a space, and no single regex expresses that without
 * backtracking traps on unbalanced quotes. An unterminated quote is treated as
 * running to the end of input, which is what a user mid-typing has.
 */
function tokenize(input: string): Token[] {
  const tokens: Token[] = [];
  let i = 0;

  while (i < input.length) {
    // Skip whitespace between tokens.
    if (/\s/.test(input[i] ?? "")) {
      i += 1;
      continue;
    }

    const start = i;
    let negated = false;
    if (input[i] === "-" && i + 1 < input.length && !/\s/.test(input[i + 1] ?? "")) {
      negated = true;
      i += 1;
    }

    /*
     * The operator name, if this token has one.
     *
     * Digits are part of the name set, not an afterthought: Gmail has
     * `rfc822msgid:`, and a letters-only pattern would fail to recognise it as
     * an operator at all — so it would fall through to free text and search
     * message BODIES for the literal string "rfc822msgid:<id>". A deferred
     * operator that silently becomes a text search is exactly the silent
     * mis-answer this module exists to prevent. The name must still START with
     * a letter, so a bare "12:30" stays the time a user typed.
     */
    let operator: string | undefined;
    const opMatch = /^([A-Za-z][A-Za-z0-9_]*):/.exec(input.slice(i));
    if (opMatch !== null) {
      operator = (opMatch[1] ?? "").toLowerCase();
      i += opMatch[0].length;
    }

    /*
     * The value: a quoted run, or everything up to the next space.
     *
     * The scan finds the END and the value is taken with one `slice`, rather
     * than appended character by character. Indexing a string past its end
     * yields `undefined`, and `value += input[i]` would happily stringify that
     * into a literal "undefined" inside a user's search term — a bug the
     * length guard makes unreachable here but which nothing in the expression
     * itself prevents.
     */
    let value = "";
    let quoted = false;
    if (input[i] === '"') {
      quoted = true;
      i += 1;
      const valueStart = i;
      while (i < input.length && input[i] !== '"') i += 1;
      value = input.slice(valueStart, i);
      // Step past the closing quote when there is one; an unterminated quote
      // simply ends at the input's end.
      if (input[i] === '"') i += 1;
    } else {
      const valueStart = i;
      while (i < input.length && !/\s/.test(input[i] ?? "")) i += 1;
      value = input.slice(valueStart, i);
    }

    const raw = input.slice(start, i);
    if (raw === "") {
      // Defensive: a lone `-` at the end of input consumes nothing else.
      i += 1;
      continue;
    }
    tokens.push({
      raw,
      ...(operator !== undefined ? { operator } : {}),
      value,
      negated,
      quoted,
    });
  }

  return tokens;
}

// ---------------------------------------------------------------------------
// value parsers
// ---------------------------------------------------------------------------

/**
 * Parses Gmail's `YYYY/MM/DD` (and the ISO `YYYY-MM-DD` a user will also type)
 * into a UTC instant at midnight.
 *
 * Returns undefined for anything that is not a real calendar date — including
 * `2026/02/31`, which `new Date` would silently roll into March. The roll is
 * checked rather than trusted because a date that quietly means a different
 * day is worse than one that is refused with its name on screen.
 */
export function parseDateValue(value: string): string | undefined {
  const match = /^(\d{4})[/-](\d{1,2})[/-](\d{1,2})$/.exec(value.trim());
  if (match === null) return undefined;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  if (month < 1 || month > 12 || day < 1 || day > 31) return undefined;
  const date = new Date(Date.UTC(year, month - 1, day));
  // The round-trip check: Date.UTC normalises out-of-range days silently.
  if (
    date.getUTCFullYear() !== year ||
    date.getUTCMonth() !== month - 1 ||
    date.getUTCDate() !== day
  ) {
    return undefined;
  }
  return date.toISOString();
}

/**
 * Resolves `7d` / `3m` / `1y` against a reference instant.
 *
 * `now` is a parameter rather than a call to `Date.now()` inside so the whole
 * grammar stays a pure function of its inputs — which is what lets the tests
 * assert exact instants instead of tolerating a window.
 */
export function parseAgeValue(value: string, now: Date): string | undefined {
  const match = /^(\d+)([dmy])$/i.exec(value.trim());
  if (match === null) return undefined;
  const amount = Number(match[1]);
  const days = AGE_UNITS[(match[2] ?? "d").toLowerCase()];
  if (days === undefined || amount <= 0) return undefined;
  return new Date(now.getTime() - amount * days * 86_400_000).toISOString();
}

/** Parses `5M` / `500k` / `1024` into octets. */
export function parseSizeValue(value: string): number | undefined {
  const match = /^(\d+(?:\.\d+)?)\s*([kmg])?b?$/i.exec(value.trim());
  if (match === null) return undefined;
  const amount = Number(match[1]);
  if (!Number.isFinite(amount) || amount < 0) return undefined;
  const unit = match[2]?.toLowerCase();
  const multiplier = unit === undefined ? 1 : (SIZE_UNITS[unit] ?? 1);
  return Math.round(amount * multiplier);
}

// ---------------------------------------------------------------------------
// parsing
// ---------------------------------------------------------------------------

/** Mutable twin of QueryGroup, used while a group accumulates. */
interface GroupDraft {
  textParts: string[];
  fields: Partial<Record<TextField, string>>;
  hasAttachment?: boolean;
  unread?: boolean;
  starred?: boolean;
  inMailbox?: string;
  label?: string;
  after?: string;
  before?: string;
  larger?: number;
  smaller?: number;
}

function newDraft(): GroupDraft {
  return { textParts: [], fields: {} };
}

function sealDraft(draft: GroupDraft): QueryGroup {
  return {
    text: draft.textParts.join(" "),
    fields: draft.fields,
    ...(draft.hasAttachment !== undefined ? { hasAttachment: draft.hasAttachment } : {}),
    ...(draft.unread !== undefined ? { unread: draft.unread } : {}),
    ...(draft.starred !== undefined ? { starred: draft.starred } : {}),
    ...(draft.inMailbox !== undefined ? { inMailbox: draft.inMailbox } : {}),
    ...(draft.label !== undefined ? { label: draft.label } : {}),
    ...(draft.after !== undefined ? { after: draft.after } : {}),
    ...(draft.before !== undefined ? { before: draft.before } : {}),
    ...(draft.larger !== undefined ? { larger: draft.larger } : {}),
    ...(draft.smaller !== undefined ? { smaller: draft.smaller } : {}),
  };
}

/** True when a group carries nothing at all. */
export function isEmptyGroup(group: QueryGroup): boolean {
  return (
    group.text === "" &&
    Object.keys(group.fields).length === 0 &&
    group.hasAttachment === undefined &&
    group.unread === undefined &&
    group.starred === undefined &&
    group.inMailbox === undefined &&
    group.label === undefined &&
    group.after === undefined &&
    group.before === undefined &&
    group.larger === undefined &&
    group.smaller === undefined
  );
}

/**
 * The operators Gmail has and this epic defers (L3 plan §6, "operadores de
 * búsqueda de cola").
 *
 * Listed EXPLICITLY rather than caught by a fallback, because the two cases
 * need different messages: a real Gmail operator we have not built yet is
 * "not supported yet", while `foo:bar` is not an operator at all and is
 * honestly just text — treating the second as an unsupported operator would
 * put a scary chip on a perfectly ordinary search for a URL or a time.
 */
const DEFERRED_OPERATORS: readonly string[] = [
  "deliveredto",
  "list",
  "filename",
  "rfc822msgid",
  "header",
  "category",
  "around",
];

/**
 * Parses a Gmail-style query string.
 *
 * `now` defaults to the current instant and exists as a parameter for the
 * relative date operators; nothing else in the grammar is time-dependent.
 */
export function parseSearchQuery(input: string, now: Date = new Date()): ParsedQuery {
  const tokens = tokenize(input);
  const groups: QueryGroup[] = [];
  const unsupported: UnsupportedTerm[] = [];

  let draft = newDraft();

  const flush = (): void => {
    groups.push(sealDraft(draft));
    draft = newDraft();
  };

  const refuse = (token: Token, operator: string, reason: UnsupportedReason): void => {
    unsupported.push({ operator, raw: token.raw, reason });
  };

  for (const token of tokens) {
    // `OR` between groups. Bare `AND` is accepted and ignored: it is the
    // default conjunction, so a user who types it means what we already do.
    if (token.operator === undefined && !token.quoted) {
      const bare = token.value.toUpperCase();
      if (bare === "OR") {
        flush();
        continue;
      }
      if (bare === "AND") continue;
    }

    if (token.operator === undefined) {
      // Free text. A quoted phrase keeps its quotes so the round trip through
      // `formatQuery` preserves the user's intent, and so the server's
      // `websearch_to_tsquery` — which understands quoted phrases natively —
      // receives the phrase as a phrase.
      if (token.value === "") continue;
      draft.textParts.push(token.quoted ? `"${token.value}"` : token.value);
      continue;
    }

    const { operator, value, negated } = token;
    const signedName = negated ? `-${operator}` : operator;

    if (DEFERRED_OPERATORS.includes(operator)) {
      refuse(token, operator, "deferredOperator");
      continue;
    }

    // A negation on anything outside the negatable set is the server's refused
    // NOT. Named, never applied, never demoted to a positive term.
    if (negated && !(NEGATABLE_FIELDS as readonly string[]).includes(operator)) {
      refuse(token, signedName, "negationUnanswerable");
      continue;
    }

    if ((TEXT_FIELDS as readonly string[]).includes(operator)) {
      const field = operator as TextField;
      if (value === "") {
        refuse(token, operator, "badValue");
        continue;
      }
      if (draft.fields[field] !== undefined && draft.fields[field] !== value) {
        // A second, different value for the same field. The server refuses two
        // different conditions on one column ("two different cc conditions"),
        // so the LATER one is named rather than silently winning.
        refuse(token, operator, "badValue");
        continue;
      }
      draft.fields[field] = value;
      continue;
    }

    switch (operator) {
      case "has": {
        if (value.toLowerCase() === "attachment") {
          draft.hasAttachment = !negated;
        } else {
          // `has:yellow-star` and friends: real Gmail, deferred by name.
          refuse(token, `has:${value}`, "deferredOperator");
        }
        break;
      }

      case "is": {
        const v = value.toLowerCase();
        if (v === "unread") draft.unread = !negated;
        else if (v === "read") draft.unread = negated;
        else if (v === "starred") draft.starred = !negated;
        else refuse(token, `is:${value}`, "deferredOperator");
        break;
      }

      case "in": {
        if (value === "") {
          refuse(token, operator, "badValue");
          break;
        }
        draft.inMailbox = value.toLowerCase();
        break;
      }

      case "label": {
        if (value === "") {
          refuse(token, operator, "badValue");
          break;
        }
        draft.label = value;
        break;
      }

      case "after":
      case "before": {
        const iso = parseDateValue(value);
        if (iso === undefined) {
          refuse(token, operator, "badValue");
          break;
        }
        if (operator === "after") draft.after = iso;
        else draft.before = iso;
        break;
      }

      case "newer_than":
      case "older_than": {
        const iso = parseAgeValue(value, now);
        if (iso === undefined) {
          refuse(token, operator, "badValue");
          break;
        }
        // `newer_than:7d` means "received AFTER seven days ago"; `older_than`
        // is the mirror. The instants are absolute, so the server never sees a
        // relative expression it would have to interpret.
        if (operator === "newer_than") draft.after = iso;
        else draft.before = iso;
        break;
      }

      case "larger":
      case "smaller":
      case "size": {
        const octets = parseSizeValue(value);
        if (octets === undefined) {
          refuse(token, operator, "badValue");
          break;
        }
        // Gmail's bare `size:` means "larger than", per its operator page.
        if (operator === "smaller") draft.smaller = octets;
        else draft.larger = octets;
        break;
      }

      default: {
        /*
         * Not an operator this grammar knows and not one Gmail has either —
         * so it is ordinary text that happens to contain a colon (a URL, a
         * time, a Message-ID). Treating it as text is what a user means; the
         * whole token is kept, colon included.
         */
        draft.textParts.push(token.raw);
        break;
      }
    }
  }

  flush();

  return { groups, unsupported };
}

// ---------------------------------------------------------------------------
// formatting — the other half of the round trip
// ---------------------------------------------------------------------------

/** Quotes a value if it contains whitespace, so it survives re-tokenizing. */
function quoteIfNeeded(value: string): string {
  return /[\s"]/.test(value) ? `"${value.replace(/"/g, "")}"` : value;
}

/** Formats an ISO instant back to Gmail's `YYYY/MM/DD`. */
export function formatDateValue(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  const month = String(date.getUTCMonth() + 1).padStart(2, "0");
  const day = String(date.getUTCDate()).padStart(2, "0");
  return `${date.getUTCFullYear()}/${month}/${day}`;
}

/** Formats octets back to the most compact `K`/`M`/`G` spelling. */
export function formatSizeValue(octets: number): string {
  for (const [unit, factor] of [
    ["G", SIZE_UNITS.g],
    ["M", SIZE_UNITS.m],
    ["K", SIZE_UNITS.k],
  ] as const) {
    if (factor !== undefined && octets >= factor && octets % factor === 0) {
      return `${octets / factor}${unit}`;
    }
  }
  return String(octets);
}

/** Renders one group back to query syntax. */
function formatGroup(group: QueryGroup): string {
  const parts: string[] = [];

  for (const field of TEXT_FIELDS) {
    const value = group.fields[field];
    if (value !== undefined) parts.push(`${field}:${quoteIfNeeded(value)}`);
  }
  if (group.hasAttachment === true) parts.push("has:attachment");
  if (group.hasAttachment === false) parts.push("-has:attachment");
  if (group.unread === true) parts.push("is:unread");
  if (group.unread === false) parts.push("is:read");
  if (group.starred === true) parts.push("is:starred");
  if (group.starred === false) parts.push("-is:starred");
  if (group.inMailbox !== undefined) parts.push(`in:${quoteIfNeeded(group.inMailbox)}`);
  if (group.label !== undefined) parts.push(`label:${quoteIfNeeded(group.label)}`);
  if (group.after !== undefined) parts.push(`after:${formatDateValue(group.after)}`);
  if (group.before !== undefined) parts.push(`before:${formatDateValue(group.before)}`);
  if (group.larger !== undefined) parts.push(`larger:${formatSizeValue(group.larger)}`);
  if (group.smaller !== undefined) parts.push(`smaller:${formatSizeValue(group.smaller)}`);
  if (group.text !== "") parts.push(group.text);

  return parts.join(" ");
}

/**
 * Renders a parsed query back to a string.
 *
 * The order is CANONICAL (operators first, in a fixed order, then free text)
 * rather than the order the user typed. That is deliberate: the panel and the
 * chips both rewrite the query, and a stable order means toggling a chip twice
 * returns the box to exactly the string it started with. Preserving typing
 * order would make the box churn every time a chip moved.
 *
 * Unsupported terms are NOT re-emitted: they were excluded from the filter, so
 * putting them back in the box would make the box promise something the search
 * did not do. The UI shows them as chips beside the box instead.
 */
export function formatQuery(query: ParsedQuery): string {
  return query.groups
    .map(formatGroup)
    .filter((part) => part !== "")
    .join(" OR ");
}

// ---------------------------------------------------------------------------
// editing — what the panel and the chips call
// ---------------------------------------------------------------------------

/**
 * A chip's or the panel's edit to one group.
 *
 * Every key is explicitly `| undefined` rather than merely optional, because
 * under `exactOptionalPropertyTypes` those are different types and only the
 * first can carry the REMOVAL signal: `{ unread: undefined }` means "turn this
 * operator off", which is what a chip does on its second press. A plain
 * `Partial<QueryGroup>` cannot express it.
 */
export type GroupPatch = {
  readonly [K in keyof QueryGroup]?: QueryGroup[K] | undefined;
};

/**
 * Applies a change to the FIRST group of a query and returns the new string.
 *
 * The first group is the one the chips edit, and the reason is honesty about
 * what a chip can mean: "Is unread" applied to `from:a OR from:b` is
 * ambiguous — it could distribute over both branches or narrow one — and
 * Gmail's own chips only ever appear on a simple query. So a chip edits the
 * first branch and the box shows the result, rather than the UI guessing.
 */
export function withGroupPatch(
  input: string,
  patch: GroupPatch,
  now: Date = new Date(),
): string {
  const parsed = parseSearchQuery(input, now);
  const groups = parsed.groups.length > 0 ? [...parsed.groups] : [EMPTY_GROUP];
  const head = groups[0] ?? EMPTY_GROUP;

  /*
   * The merge, written out rather than spread, because the two halves of a
   * QueryGroup behave differently and a spread hides that:
   *
   *   - `text` and `fields` are REQUIRED, so a patch that omits them (or sets
   *     them to undefined, which a chip does when it clears an unrelated key)
   *     must fall back to the current value, never to undefined.
   *   - every other key is OPTIONAL, and an explicit `undefined` is the
   *     REMOVAL signal — that is how a chip toggles itself off.
   *
   * The rebuilt draft goes through `sealDraft`, so the "present but undefined"
   * keys a spread would leave behind never reach the output at all. The
   * previous version leaned on a double cast to silence exactly this, which is
   * how `text: undefined` could have reached a caller typed as `string`.
   */
  const has = (key: keyof QueryGroup): boolean =>
    Object.prototype.hasOwnProperty.call(patch, key);
  const pick = <K extends keyof QueryGroup>(key: K): QueryGroup[K] | undefined =>
    has(key) ? patch[key] : head[key];

  const draft: GroupDraft = {
    textParts: [],
    fields: { ...(pick("fields") ?? head.fields) },
  };
  const nextText = pick("text") ?? (has("text") ? "" : head.text);
  if (nextText !== "") draft.textParts.push(nextText);

  const hasAttachment = pick("hasAttachment");
  if (hasAttachment !== undefined) draft.hasAttachment = hasAttachment;
  const unread = pick("unread");
  if (unread !== undefined) draft.unread = unread;
  const starred = pick("starred");
  if (starred !== undefined) draft.starred = starred;
  const inMailbox = pick("inMailbox");
  if (inMailbox !== undefined) draft.inMailbox = inMailbox;
  const label = pick("label");
  if (label !== undefined) draft.label = label;
  const after = pick("after");
  if (after !== undefined) draft.after = after;
  const before = pick("before");
  if (before !== undefined) draft.before = before;
  const larger = pick("larger");
  if (larger !== undefined) draft.larger = larger;
  const smaller = pick("smaller");
  if (smaller !== undefined) draft.smaller = smaller;

  groups[0] = sealDraft(draft);
  return formatQuery({ groups, unsupported: [] });
}

/** Reads one operator's value off the first group — what a chip renders from. */
export function firstGroup(input: string, now: Date = new Date()): QueryGroup {
  const parsed = parseSearchQuery(input, now);
  return parsed.groups[0] ?? EMPTY_GROUP;
}

/**
 * True when a query says anything at all — the test for "a search is active".
 *
 * A query of only unsupported terms counts as active: the user asked for
 * something, and the UI has to explain why nothing came back rather than
 * showing an idle inbox as though nothing was typed.
 */
export function hasAnyTerm(query: ParsedQuery): boolean {
  return query.groups.some((group) => !isEmptyGroup(group)) || query.unsupported.length > 0;
}
