/**
 * The suggestions under the search box: recent searches, labels, operators
 * (L3 epic E3; canon §2.5 "Suggestions from contacts/labels/messages/past
 * searches").
 *
 * Pure functions plus a localStorage-backed history, deliberately separated
 * from the combobox that renders them — a suggestion list is ranking logic,
 * and ranking logic tested through a dropdown is ranking logic that is not
 * really tested.
 *
 * # What is here and what is NOT
 *
 * Gmail suggests from four sources. Three are implementable today:
 *
 *   - **past searches** — this module's history, capped and clearable;
 *   - **labels** — E8's `labelStore` already knows them;
 *   - **operators** — typing "fr" should offer `from:`, which is the cheapest
 *     way to teach the language to someone who does not know it exists.
 *
 * The fourth, **contacts**, is a DECLARED SOFT DEPENDENCY: the L3 plan puts the
 * address index in epic E7 ("las de direcciones cuando aterrice el índice de
 * E7 — dependencia blanda declarada"). Rather than invent a worse index here,
 * the source is absent — no empty section, no placeholder row. When E7's index
 * lands it becomes a fourth {@link SuggestionKind} and this comment is deleted.
 */

import { rankAddresses, suggestAddresses, type IndexedAddress } from "./addressIndex";
import type { Label } from "./labelStore";
import { mailboxSegment } from "./mailboxes";
import type { Mailbox } from "./types";

/** Where a suggestion came from — the UI groups by this. */
export type SuggestionKind = "recent" | "label" | "operator" | "value";

export interface Suggestion {
  readonly kind: SuggestionKind;
  /** The query text this suggestion would put in the box. */
  readonly value: string;
  /** What to show. For an operator this is the operator plus its hint. */
  readonly label: string;
  /** A stable key for React, unique across kinds. */
  readonly id: string;
}

// ---------------------------------------------------------------------------
// the history
// ---------------------------------------------------------------------------

/**
 * How many past searches to keep.
 *
 * Ten is the spec's cap and it is the right order of magnitude for a dropdown
 * a person scans rather than reads: a list long enough to need scrolling is a
 * list that costs more attention than retyping.
 */
export const MAX_RECENT_SEARCHES = 10;

const STORAGE_KEY = "moov.search.recent";

/** Reads the recent-search list, tolerating anything in storage. */
export function loadRecentSearches(storage?: Storage): readonly string[] {
  try {
    const store = storage ?? globalThis.localStorage;
    const raw = store?.getItem(STORAGE_KEY);
    if (raw === null || raw === undefined) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter((entry): entry is string => typeof entry === "string" && entry !== "")
      .slice(0, MAX_RECENT_SEARCHES);
  } catch {
    // Private mode, corrupt JSON, a locked-down browser: no history is a
    // complete feature, so this never throws into a render.
    return [];
  }
}

/** Writes the recent-search list. A blocked storage costs the history only. */
export function saveRecentSearches(queries: readonly string[], storage?: Storage): void {
  try {
    const store = storage ?? globalThis.localStorage;
    store?.setItem(STORAGE_KEY, JSON.stringify(queries.slice(0, MAX_RECENT_SEARCHES)));
  } catch {
    // Deliberately silent: search works without a history.
  }
}

/**
 * Adds a query to the front of the history, de-duplicated and capped.
 *
 * Pure, so the ordering rules are testable without a Storage. Re-searching an
 * old query MOVES it to the front rather than adding a duplicate, which is what
 * makes a short list stay useful.
 */
export function withRecentSearch(
  history: readonly string[],
  query: string,
): readonly string[] {
  const trimmed = query.trim();
  if (trimmed === "") return history;
  const rest = history.filter((entry) => entry !== trimmed);
  return [trimmed, ...rest].slice(0, MAX_RECENT_SEARCHES);
}

// ---------------------------------------------------------------------------
// operator hints
// ---------------------------------------------------------------------------

/**
 * The operators offered as completions, in the order Gmail's own help lists
 * them (canon §2.5).
 *
 * Only operators this client can actually SEND are here. Offering
 * `filename:` — which the parser names as deferred — would be a suggestion
 * that leads straight to a refusal chip, which is P4's dead control wearing a
 * different hat.
 */
export const OPERATOR_HINTS: readonly string[] = [
  "from:",
  "to:",
  "cc:",
  "bcc:",
  "subject:",
  "has:attachment",
  "is:unread",
  "is:read",
  "is:starred",
  "in:anywhere",
  "label:",
  "after:",
  "before:",
  "newer_than:",
  "older_than:",
  "larger:",
  "smaller:",
];

/**
 * The word currently being typed — everything after the last space.
 *
 * Operator completion applies to the LAST token only: a user typing
 * "informe fr" is starting a second term, and suggesting `from:` should
 * complete that token while leaving "informe" alone.
 */
export function activeToken(input: string): string {
  const match = /(\S*)$/.exec(input);
  return match?.[1] ?? "";
}

/** Replaces the last token, which is what accepting an operator hint does. */
export function replaceActiveToken(input: string, replacement: string): string {
  const head = input.slice(0, input.length - activeToken(input).length);
  return `${head}${replacement}`;
}

// ---------------------------------------------------------------------------
// building the list
// ---------------------------------------------------------------------------

/** How many suggestions to show at once, across all kinds. */
export const MAX_SUGGESTIONS = 8;

export interface SuggestionInput {
  /** The current contents of the box. */
  readonly input: string;
  readonly recent: readonly string[];
  readonly labels: readonly Label[];
  /**
   * E-04: the account's folders, for `in:` completion. Empty means no folder
   * values are offered — which is what a caller without them should get, not a
   * guess.
   */
  readonly mailboxes?: readonly Mailbox[];
  /**
   * E-04: E7's address index, for `from:` / `to:` / `cc:` / `bcc:` completion.
   *
   * The soft dependency this module declared in E3 ("contacts ... when E7's
   * index lands it becomes a fourth SuggestionKind") — landed. Absent when the
   * user opted out of the index or nothing has been seen yet, which produces no
   * address rows rather than an empty section.
   */
  readonly addresses?: readonly IndexedAddress[];
}

/**
 * The operators whose VALUE set is closed, with the values (E-04).
 *
 * These are the ones where the vocabulary is finite and ours: `is:` and `has:`
 * have exactly the words the parser accepts, so offering anything else would
 * lead straight to a refusal chip. The open-ended operators — addresses,
 * folders, labels — draw from the account's own data instead, below.
 *
 * `is:muted` is here because the parser accepts it and the UI honours it over
 * the returned window (see `QueryGroup.muted`); `is:important` is NOT, because
 * it needs the classifier the IA phase brings and would refuse.
 */
const CLOSED_VALUES: Readonly<Record<string, readonly string[]>> = {
  is: ["unread", "read", "starred", "muted"],
  has: ["attachment"],
};

/** The operators completed from the address index. */
const ADDRESS_OPERATORS: readonly string[] = ["from", "to", "cc", "bcc"];

/**
 * Splits a token that already carries a complete operator (E-04).
 *
 * `from:` → `{ operator: "from", value: "" }`, `from:an` → `{ "from", "an" }`,
 * and anything without a colon → undefined. The operator must be one this
 * grammar knows, so a URL typed into the box ("https://x") is not mistaken for
 * an operator with a value — which is the same distinction `parseSearchQuery`'s
 * default branch makes, for the same reason.
 */
export function splitOperatorToken(
  token: string,
): { readonly operator: string; readonly value: string } | undefined {
  const match = /^-?([A-Za-z][A-Za-z0-9_]*):(.*)$/.exec(token);
  if (match === null) return undefined;
  const operator = (match[1] ?? "").toLowerCase();
  const known =
    operator in CLOSED_VALUES ||
    ADDRESS_OPERATORS.includes(operator) ||
    operator === "in" ||
    operator === "label";
  if (!known) return undefined;
  return { operator, value: (match[2] ?? "").replace(/^"/, "") };
}

/** Quotes a value so it survives re-tokenizing, as the panel's builder does. */
function quoteValue(value: string): string {
  return /\s/.test(value) ? `"${value}"` : value;
}

/**
 * Builds the suggestion list for the current input.
 *
 * # The ranking, and why it is this one
 *
 * Recent searches come FIRST — the plan says so explicitly ("búsquedas
 * recientes primero") and it is right: a search you have run before is a
 * search you are far more likely to want than a completion of a word you are
 * halfway through typing.
 *
 * Then labels, then operator hints. Operators sit last because they are the
 * TEACHING source: valuable to someone who does not know the language exists,
 * and noise to someone who does. Last place is where a thing can be
 * discovered without being in the way.
 *
 * An EMPTY input shows recent searches only. Offering the whole operator
 * vocabulary to someone who has just clicked into the box is a wall of syntax,
 * and Gmail does not do it either.
 *
 * # E-04: a COMPLETE operator opens its values
 *
 * The review found the dropdown empty for the one input that most needs it.
 * Typing `from:` produced nothing at all, for three compounding reasons: an
 * account with no labels contributed no label rows; the operator list refused
 * an exact match (`from:` === `from:`, "the user has already typed it"); and
 * the recent-search filter is a substring test that `from:` rarely passes. So
 * the combobox — which the review otherwise called APG of manual — was
 * correctly built and starved.
 *
 * The fix is not to relax those filters. It is that a complete operator is a
 * DIFFERENT question: the user has finished saying WHICH field and is now
 * asking WHAT to put in it, and the answer is the account's own data —
 * addresses from E7's index, the folders, the labels — or, for `is:`/`has:`,
 * the closed vocabulary the parser accepts. Suggesting an operator to someone
 * who has just typed one whole would be the least useful row on the list.
 */
export function buildSuggestions({
  input,
  recent,
  labels,
  mailboxes = [],
  addresses = [],
}: SuggestionInput): readonly Suggestion[] {
  const trimmed = input.trim();
  const lower = trimmed.toLowerCase();
  const token = activeToken(input);
  const tokenLower = token.toLowerCase();

  const suggestions: Suggestion[] = [];

  /*
   * E-04: the value branch, taken FIRST and taken alone.
   *
   * When the active token is a complete operator, the list is its values and
   * nothing else. Mixing recent searches in would put rows that ignore the
   * operator above rows that answer it, which is how a dropdown teaches people
   * to stop looking at it.
   */
  const split = splitOperatorToken(token);
  if (split !== undefined) {
    for (const suggestion of valueSuggestions(input, split, { labels, mailboxes, addresses })) {
      suggestions.push(suggestion);
    }
    return suggestions.slice(0, MAX_SUGGESTIONS);
  }

  for (const query of recent) {
    // On an empty box, every recent search qualifies; once typing starts, only
    // the ones that extend what is there.
    if (trimmed !== "" && !query.toLowerCase().includes(lower)) continue;
    // A recent search identical to what is already typed is not a suggestion.
    if (query.toLowerCase() === lower) continue;
    suggestions.push({ kind: "recent", value: query, label: query, id: `recent:${query}` });
  }

  if (trimmed !== "") {
    for (const label of labels) {
      if (!label.name.toLowerCase().includes(tokenLower) && tokenLower !== "") continue;
      // The value is a complete query fragment, so accepting it lands a term
      // the parser will read back as `label:`.
      const value = replaceActiveToken(
        input,
        /\s/.test(label.name) ? `label:"${label.name}"` : `label:${label.name}`,
      );
      suggestions.push({
        kind: "label",
        value,
        label: label.name,
        id: `label:${label.keyword}`,
      });
    }

    for (const operator of OPERATOR_HINTS) {
      if (tokenLower === "" || !operator.startsWith(tokenLower)) continue;
      // An exact match is not a suggestion — the user has already typed it.
      if (operator === tokenLower) continue;
      suggestions.push({
        kind: "operator",
        value: replaceActiveToken(input, operator),
        label: operator,
        id: `operator:${operator}`,
      });
    }
  }

  return suggestions.slice(0, MAX_SUGGESTIONS);
}

/**
 * The values for one complete operator (E-04).
 *
 * Each branch draws from the source that actually knows the answer, and the
 * ranking inside a branch is that source's own — `rankAddresses` for addresses,
 * the account's folder order for `in:` — rather than a second ordering invented
 * here. A partial value narrows by substring, which is what a person typing
 * three letters of a name expects.
 *
 * The emitted `value` is always the WHOLE query with the active token replaced,
 * so accepting a row lands a string the grammar reads back unchanged. That is
 * the same invariant the panel and the chips hold: one grammar, and every
 * surface writes through it.
 */
function valueSuggestions(
  input: string,
  { operator, value }: { readonly operator: string; readonly value: string },
  sources: {
    readonly labels: readonly Label[];
    readonly mailboxes: readonly Mailbox[];
    readonly addresses: readonly IndexedAddress[];
  },
): readonly Suggestion[] {
  const needle = value.trim().toLowerCase();
  const emit = (term: string, label: string, id: string): Suggestion => ({
    kind: "value",
    value: replaceActiveToken(input, term),
    label,
    id,
  });

  const closed = CLOSED_VALUES[operator];
  if (closed !== undefined) {
    return closed
      .filter((candidate) => candidate.startsWith(needle))
      .map((candidate) =>
        emit(`${operator}:${candidate}`, `${operator}:${candidate}`, `value:${operator}:${candidate}`),
      );
  }

  if (ADDRESS_OPERATORS.includes(operator)) {
    /*
     * E7's index, ranked by its own `rankAddresses` — recency and frequency of
     * real correspondence, which is the ordering the composer's recipient field
     * already uses. Reproducing a different one here would make the same person
     * appear in a different place depending on which field they were typing in.
     */
    /*
     * A bare `from:` shows the top of the index, which `suggestAddresses`
     * cannot answer — it returns nothing for an empty query, correctly, since
     * an empty recipient field must not drop a popup on someone who has not
     * typed. Here the operator IS the request, so the ranked head is what the
     * user asked for and `rankAddresses` supplies it.
     */
    const matched =
      needle === ""
        ? rankAddresses(sources.addresses).slice(0, MAX_SUGGESTIONS)
        : suggestAddresses(sources.addresses, needle, [], MAX_SUGGESTIONS);
    return matched.map((address) =>
      emit(
        `${operator}:${quoteValue(address.email)}`,
        address.displayName === undefined || address.displayName === ""
          ? address.email
          : `${address.displayName} — ${address.email}`,
        `value:${operator}:${address.email}`,
      ),
    );
  }

  if (operator === "in") {
    return sources.mailboxes
      .filter((box) => box.name.toLowerCase().includes(needle))
      .slice(0, MAX_SUGGESTIONS)
      .map((box) =>
        emit(`in:${quoteValue(mailboxSegment(box))}`, box.name, `value:in:${box.id}`),
      );
  }

  // `label:` — the only remaining known operator.
  return sources.labels
    .filter((label) => label.name.toLowerCase().includes(needle))
    .slice(0, MAX_SUGGESTIONS)
    .map((label) =>
      emit(`label:${quoteValue(label.name)}`, label.name, `value:label:${label.keyword}`),
    );
}
