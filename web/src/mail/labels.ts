/**
 * Labels — the keyword convention, the 26-keyword budget, and the JSON-Pointer
 * escaping every keyword patch has to go through (L3 epic E8, GC-5).
 *
 * # The model, and why it is not Gmail's
 *
 * Gmail can say "everything is a label" because it has ten thousand of them.
 * We have **26 durable IMAP keywords per Maildir folder** — measured, not
 * assumed: validation V1 put 500 keywords on one message and Dovecot accepted,
 * persisted and returned every one, but after a force-resync only 26 survived,
 * because `dovecot-keywords` encodes each keyword as one letter a-z in the
 * message filename and stops at index 25. See the constant's own comment at
 * `internal/imap/metadata.go:52` (`MaxDurableKeywordsPerMailbox = 26`), which
 * this module MIRRORS rather than imports — a TypeScript build cannot read Go,
 * and a number copied without its citation is a number that drifts.
 *
 * The consequence GC-5 draws from that, and this module encodes: **folders
 * carry the organizational load** (they are unlimited), and keywords are
 * reserved for the few cross-cutting flags. So the label UI's job is not to
 * hide the ceiling behind a spinner — it is to state it. Hence
 * {@link labelBudget}, which is a pure function precisely so the honest number
 * on screen is the same number the create button obeys.
 *
 * # The wire convention: `$label:<slug>`
 *
 * A user label is an IMAP keyword named `$label:<slug>`. This is not invented
 * here: it is arbitration A6's hybrid (keywords + METADATA), and it is the same
 * string Bulwark uses for its tags — `docs/research/05-gmail-class-surface.md`
 * §1.3 records `add_label` compiling to Sieve `addflag "$label:<id>"` and notes
 * "same convention as our A6 keywords", and §5.0 documents the tag id being the
 * keyword suffix. Adopting it verbatim means a mailbox labelled in Bulwark
 * reads correctly in Moov and vice versa, which is worth far more than any
 * naming we could prefer.
 *
 * # The bug this file exists to prevent
 *
 * `Email/set` patches keywords by JSON Pointer: the patch key is
 * `keywords/$label:work`. RFC 6901 makes `/` a SEPARATOR inside a pointer, so a
 * label named `work/clients` produces `keywords/$label:work/clients`, which
 * addresses **the `clients` member of `$label:work`** — a different location
 * that the server accepts and that never carries the label. The tag silently
 * never lands (research 05 §5.0: "The tag silently never lands", 29 lines of
 * `patch-pointer.ts` written after that production bug).
 *
 * {@link keywordPatchKey} is the single place a `keywords/…` key is built, and
 * it escapes `~` → `~0` then `/` → `~1`, in that order. The order is not a
 * detail: escaping `/` first would turn a literal `~1` in a name into a slash
 * on decode. {@link decodePointerToken} reverses it in the mirror order.
 */

import { KEYWORD_ANSWERED, KEYWORD_DRAFT, KEYWORD_FLAGGED, KEYWORD_SEEN } from "./types";

// ---------------------------------------------------------------------------
// the ceiling
// ---------------------------------------------------------------------------

/**
 * The number of distinct IMAP keywords a Maildir folder holds DURABLY: 26.
 *
 * Mirrored from `internal/imap/metadata.go:52`
 * (`MaxDurableKeywordsPerMailbox`). It is duplicated rather than fetched
 * because it is a property of the Maildir format, not of a running server —
 * there is no endpoint that could report it, and V1 proved asking Dovecot gives
 * the WRONG answer (it accepts 500 and loses 474 later, silently).
 *
 * The budget is shared with the standard keywords other clients set —
 * `$Forwarded`, `$MDNSent`, `NonJunk` — which consume from the same 26. That is
 * why {@link labelBudget} takes the folder's OBSERVED keywords rather than
 * counting only our own labels.
 */
export const MAX_DURABLE_KEYWORDS = 26;

// ---------------------------------------------------------------------------
// the convention
// ---------------------------------------------------------------------------

/** The prefix that marks a keyword as one of our user labels (A6 / Bulwark). */
export const LABEL_PREFIX = "$label:";

/**
 * Keywords that are NOT user labels and that consume from the same 26.
 *
 * Two groups, and the distinction matters for the budget:
 *
 *   - the four RFC 8621 §4.1.1 system flags (`$seen`, `$flagged`, `$answered`,
 *     `$draft`), which Dovecot stores in the Maildir filename's own flag field
 *     rather than in the keyword registry — so they cost NOTHING from the 26
 *     (this is why `imapNameForKeyword` in the server translates them to bare
 *     flags);
 *   - everything else here — `$forwarded`, `$mdnsent`, `nonjunk`, `junk`,
 *     `$phishing` — which really is a registered keyword and really does take a
 *     letter.
 *
 * {@link SYSTEM_FLAG_KEYWORDS} is the first group; this set is the union. Names
 * are compared case-insensitively because IMAP keyword matching is
 * case-insensitive and Dovecot allocates ONE letter per case-folded name.
 */
export const SYSTEM_FLAG_KEYWORDS: readonly string[] = [
  KEYWORD_SEEN,
  KEYWORD_FLAGGED,
  KEYWORD_ANSWERED,
  KEYWORD_DRAFT,
];

/**
 * Keywords the ecosystem sets that are reserved and never shown as labels.
 *
 * `$Forwarded` and `$MDNSent` are RFC 5788 IMAP keywords; `NonJunk`/`Junk` are
 * the de-facto spam-training pair Thunderbird and friends write; `$Phishing`
 * comes from the same registry. A user typing "Junk" as a label name would
 * collide with a keyword another client already owns, so the name is refused
 * rather than allowed to alias.
 */
export const RESERVED_KEYWORDS: readonly string[] = [
  ...SYSTEM_FLAG_KEYWORDS,
  "$forwarded",
  "$mdnsent",
  "$phishing",
  "$junk",
  "$notjunk",
  "nonjunk",
  "junk",
];

const RESERVED_SET: ReadonlySet<string> = new Set(
  RESERVED_KEYWORDS.map((keyword) => keyword.toLowerCase()),
);

const SYSTEM_FLAG_SET: ReadonlySet<string> = new Set(
  SYSTEM_FLAG_KEYWORDS.map((keyword) => keyword.toLowerCase()),
);

/** True when a keyword is reserved — a system flag or an ecosystem keyword. */
export function isReservedKeyword(keyword: string): boolean {
  return RESERVED_SET.has(keyword.trim().toLowerCase());
}

/**
 * True when a keyword occupies one of the 26 Maildir keyword slots.
 *
 * The four system flags do NOT: they live in the Maildir filename's flag field.
 * Everything else does, including keywords set by other clients — which is the
 * whole reason the budget cannot be computed from our own label list alone.
 */
export function consumesKeywordSlot(keyword: string): boolean {
  return !SYSTEM_FLAG_SET.has(keyword.trim().toLowerCase());
}

// ---------------------------------------------------------------------------
// encode / decode
// ---------------------------------------------------------------------------

/** The longest display name a label may carry. */
export const MAX_LABEL_NAME_LENGTH = 64;

/**
 * The keyword for a display name.
 *
 * The name is carried VERBATIM after the prefix — not slugified, not
 * lowercased, not stripped of `/`. Three reasons, in order of weight:
 *
 *   1. **Round-trip.** `decodeLabelName(encodeLabelKeyword(n)) === n` for every
 *      accepted name, which is what makes the keyword the single source of
 *      truth for the label's name. A slug would need a separate name→slug map
 *      that some other client (Bulwark, a Sieve rule) has never seen.
 *   2. **`/` is meaningful.** Nested labels ("work/clients") are Bulwark's
 *      `nestedTags` convention and are the only reason the JSON-Pointer
 *      escaping below is load-bearing. Slugifying the slash away would hide the
 *      bug rather than fix it.
 *   3. **Unicode.** "Facturación" must be a label. IMAP keywords are ASCII-ish
 *      in practice but Dovecot stores them as opaque bytes and both our server
 *      and Bulwark round-trip UTF-8 keywords; downgrading to ASCII would make
 *      the Spanish pilot's labels unreadable.
 *
 * Whitespace is trimmed because a leading space is invisible and would produce
 * two labels that look identical.
 */
export function encodeLabelKeyword(name: string): string {
  return `${LABEL_PREFIX}${name.trim()}`;
}

/**
 * The display name of a label keyword, or `undefined` when it is not one.
 *
 * Returning `undefined` rather than the raw keyword is deliberate: the callers
 * are filters over a message's whole keyword set, which contains `$seen`,
 * `$forwarded` and anything another client wrote. "Not a label" has to be
 * expressible, or the reading pane grows a chip reading "$seen".
 */
export function decodeLabelName(keyword: string): string | undefined {
  if (!keyword.startsWith(LABEL_PREFIX)) return undefined;
  const name = keyword.slice(LABEL_PREFIX.length);
  return name === "" ? undefined : name;
}

/** True when a keyword follows the user-label convention. */
export function isLabelKeyword(keyword: string): boolean {
  return decodeLabelName(keyword) !== undefined;
}

/** Why a proposed label name was refused. */
export type LabelNameProblem = "empty" | "tooLong" | "reserved" | "duplicate" | "control";

/**
 * Validates a display name against the existing labels.
 *
 * Returns `undefined` when the name is acceptable. Comparison for duplicates is
 * case-insensitive because IMAP keyword matching is: "Work" and "work" would be
 * ONE keyword on the server and two rows in our list, which is exactly the
 * "labels that exist only in the DB, silently" failure L2 §2.3 forbids.
 */
export function validateLabelName(
  name: string,
  existing: readonly string[] = [],
): LabelNameProblem | undefined {
  const trimmed = name.trim();
  if (trimmed === "") return "empty";
  if (trimmed.length > MAX_LABEL_NAME_LENGTH) return "tooLong";
  /*
   * Control characters cannot appear in an IMAP flag (RFC 3501's `atom` excludes
   * them) and would be rejected by Dovecot mid-command, which surfaces as a
   * connection-level error rather than as a validation message. Refusing here
   * turns an ugly failure into a sentence.
   */
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(trimmed)) return "control";
  if (isReservedKeyword(trimmed) || isReservedKeyword(encodeLabelKeyword(trimmed))) {
    return "reserved";
  }
  const folded = trimmed.toLowerCase();
  if (existing.some((other) => other.trim().toLowerCase() === folded)) return "duplicate";
  return undefined;
}

// ---------------------------------------------------------------------------
// RFC 6901 — the escaping that keeps a nested label from vanishing
// ---------------------------------------------------------------------------

/**
 * Escapes one JSON-Pointer reference token (RFC 6901 §3).
 *
 * `~` → `~0` FIRST, then `/` → `~1`. The order is the whole trick: escaping `/`
 * first would emit `~1` for it, and the subsequent `~` pass would turn that
 * into `~01`, which decodes back to a literal `~1` instead of a slash.
 */
export function escapePointerToken(token: string): string {
  return token.replaceAll("~", "~0").replaceAll("/", "~1");
}

/**
 * Unescapes one JSON-Pointer reference token (RFC 6901 §4).
 *
 * The MIRROR order: `~1` → `/` first, then `~0` → `~`. Reversing it would make
 * an escaped `~01` decode to `/` — RFC 6901 §4 states the order explicitly for
 * exactly this reason.
 */
export function decodePointerToken(token: string): string {
  return token.replaceAll("~1", "/").replaceAll("~0", "~");
}

/**
 * The `Email/set` PatchObject key that addresses ONE keyword.
 *
 * **This is the single point where a `keywords/…` key is built.** Every keyword
 * write in the client goes through it — `write.ts`'s `setKeyword`, the label
 * apply/remove path, the rename migration — so the escaping cannot be forgotten
 * at one call site, which is precisely how the Bulwark bug happened.
 *
 * @example
 * keywordPatchKey("$label:work/clients") // "keywords/$label:work~1clients"
 */
export function keywordPatchKey(keyword: string): string {
  return `keywords/${escapePointerToken(keyword)}`;
}

/**
 * The keyword a patch key addressed, or `undefined` when the key is not a
 * keyword patch.
 *
 * The inverse of {@link keywordPatchKey}, which is what lets a test assert the
 * round trip rather than eyeball two string literals.
 */
export function keywordFromPatchKey(key: string): string | undefined {
  const prefix = "keywords/";
  if (!key.startsWith(prefix)) return undefined;
  const token = key.slice(prefix.length);
  return token === "" ? undefined : decodePointerToken(token);
}

// ---------------------------------------------------------------------------
// the budget — the heart of GC-5
// ---------------------------------------------------------------------------

/** What the create UI needs to know, and to say out loud. */
export interface LabelBudget {
  /** The ceiling: {@link MAX_DURABLE_KEYWORDS}. */
  readonly ceiling: number;
  /** Slots taken by keywords that are NOT our user labels. */
  readonly systemUsed: number;
  /** Slots taken by user labels. */
  readonly labelsUsed: number;
  /** Total slots taken — `systemUsed + labelsUsed`, never above the ceiling. */
  readonly used: number;
  /** How many more labels can be created. Zero means creation is disabled. */
  readonly available: number;
  /** True when no further label can be created. */
  readonly isFull: boolean;
}

/**
 * Computes the label budget for a folder.
 *
 * Pure, and separated from every component, because this number is what the UI
 * PROMISES: "17 de 26 disponibles" must be the same arithmetic that decides
 * whether the create button is enabled, or the UI offers a creation that the
 * server refuses — which is exactly the silent failure GC-5 forbids.
 *
 * @param observedKeywords every DISTINCT keyword seen in the folder, in any
 *   case, including ones other clients set. The four system flags among them
 *   are discounted ({@link consumesKeywordSlot}) because Dovecot keeps them in
 *   the Maildir filename's flag field, not in `dovecot-keywords`.
 * @param labelKeywords the keywords WE consider user labels. Passed separately
 *   from `observedKeywords` because a label may be defined locally before any
 *   message carries it, and the budget must count it as spent the moment it
 *   exists — otherwise the 27th label is created, applied, and lost weeks later
 *   when the index is rebuilt.
 */
export function labelBudget(
  observedKeywords: readonly string[],
  labelKeywords: readonly string[] = [],
): LabelBudget {
  const labels = new Set<string>();
  for (const keyword of labelKeywords) {
    const folded = keyword.trim().toLowerCase();
    if (folded !== "") labels.add(folded);
  }

  const others = new Set<string>();
  for (const keyword of observedKeywords) {
    const folded = keyword.trim().toLowerCase();
    if (folded === "") continue;
    if (!consumesKeywordSlot(folded)) continue;
    if (labels.has(folded)) continue;
    others.add(folded);
  }

  const systemUsed = others.size;
  const labelsUsed = labels.size;
  const used = Math.min(systemUsed + labelsUsed, MAX_DURABLE_KEYWORDS);
  const available = Math.max(MAX_DURABLE_KEYWORDS - systemUsed - labelsUsed, 0);

  return {
    ceiling: MAX_DURABLE_KEYWORDS,
    systemUsed,
    labelsUsed,
    used,
    available,
    isFull: available === 0,
  };
}

// ---------------------------------------------------------------------------
// reading labels off messages
// ---------------------------------------------------------------------------

/** The label keywords carried by one message, in the order given. */
export function labelKeywordsOf(keywords: Readonly<Record<string, boolean>> | undefined): readonly string[] {
  if (keywords === undefined) return [];
  return Object.entries(keywords)
    .filter(([keyword, value]) => value && isLabelKeyword(keyword))
    .map(([keyword]) => keyword);
}

/**
 * How a label applies across a SELECTION: to all of it, some of it, or none.
 *
 * "Mixed" is a real state a menu has to render — Gmail shows a dash rather than
 * a tick — and collapsing it to "off" would make one click silently strip the
 * label from the messages that had it.
 */
export type LabelSelectionState = "all" | "some" | "none";

/** The state of one label over a set of messages' keyword maps. */
export function labelStateFor(
  keyword: string,
  messages: readonly (Readonly<Record<string, boolean>> | undefined)[],
): LabelSelectionState {
  if (messages.length === 0) return "none";
  let present = 0;
  for (const keywords of messages) {
    if (keywords?.[keyword] === true) present += 1;
  }
  if (present === 0) return "none";
  return present === messages.length ? "all" : "some";
}

/**
 * What a click on a mixed-state label should DO.
 *
 * Gmail's rule, and the same one `resolveToggle` applies to read/starred: if
 * any message lacks the label, apply it to all; only when every message already
 * has it does the click remove it. Toggling per message leaves a selection in a
 * mixed state after an explicit click, which is never what anyone meant.
 */
export function toggleTargetFor(state: LabelSelectionState): boolean {
  return state !== "all";
}
