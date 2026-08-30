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

import type { Label } from "./labelStore";

/** Where a suggestion came from — the UI groups by this. */
export type SuggestionKind = "recent" | "label" | "operator";

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
 */
export function buildSuggestions({ input, recent, labels }: SuggestionInput): readonly Suggestion[] {
  const trimmed = input.trim();
  const lower = trimmed.toLowerCase();
  const token = activeToken(input).toLowerCase();

  const suggestions: Suggestion[] = [];

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
      if (!label.name.toLowerCase().includes(token) && token !== "") continue;
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
      if (token === "" || !operator.startsWith(token)) continue;
      // An exact match is not a suggestion — the user has already typed it.
      if (operator === token) continue;
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
