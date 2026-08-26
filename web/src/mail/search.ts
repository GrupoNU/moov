/**
 * Search behaviour (P2 deliverable 5).
 *
 * # The <100 ms bar, and what "perceived" means
 *
 * ADR §6 asks for as-you-type search under 100 ms *perceived*. Two things
 * follow, and only one of them is about speed:
 *
 *   1. The request must be fast. Ours is: measured against the live pilot, a
 *      text query plus its chained Email/get round-trips well inside the bar
 *      (numbers in web/README.md).
 *   2. The UI must never go BLANK while it is in flight. A list that empties
 *      and refills reads as slower than one that keeps the previous results
 *      dimmed until the new ones land, even when the second is objectively
 *      slower. This is why the search state keeps `results` from the previous
 *      query while `isLoading` is true.
 *
 * # Why debounce at all if it is fast
 *
 * Not for our sake — for the server's. `maxConcurrentRequests` is 8, enforced
 * with a 429; a 40-character query typed at speed would fire 40 requests and
 * be rate-limited into failure. Debouncing collapses a burst of keystrokes into
 * one request, and the trailing edge is what makes the last keystroke the one
 * that counts.
 */

/**
 * How long to wait after the last keystroke.
 *
 * 180 ms is chosen against typing cadence rather than plucked round: a fluent
 * typist's inter-key interval is roughly 120-160 ms, so a shorter delay fires
 * mid-word and a longer one is perceptible as lag after you stop. It leaves
 * most of the 100 ms budget to the round trip because the debounce runs
 * DURING typing, not after it — the user is still moving when it elapses.
 */
export const SEARCH_DEBOUNCE_MS = 180;

/** The shortest query worth sending. */
export const MIN_QUERY_LENGTH = 2;

/**
 * Normalises a raw input value into the query the server should see.
 *
 * Collapsing internal whitespace matters because `websearch_to_tsquery` ANDs
 * the words it finds: "arquitectura   del" and "arquitectura del" are the same
 * search, and treating them as different would fire a second request and
 * discard a warm result for no reason.
 */
export function normalizeQuery(raw: string): string {
  return raw.trim().replace(/\s+/g, " ");
}

/** True when a normalised query is worth sending to the server. */
export function isSearchable(query: string): boolean {
  return query.length >= MIN_QUERY_LENGTH;
}

/**
 * A debouncer with a cancel, built as a factory rather than a hook so the
 * timing logic can be tested with fake timers and no React at all.
 */
export interface Debouncer<A extends readonly unknown[]> {
  readonly run: (...args: A) => void;
  readonly cancel: () => void;
  /** Runs the pending call immediately, if there is one. */
  readonly flush: () => void;
}

export function createDebouncer<A extends readonly unknown[]>(
  fn: (...args: A) => void,
  delayMs: number = SEARCH_DEBOUNCE_MS,
): Debouncer<A> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let pending: A | undefined;

  const cancel = (): void => {
    if (timer !== undefined) {
      clearTimeout(timer);
      timer = undefined;
    }
    pending = undefined;
  };

  return {
    run: (...args: A): void => {
      pending = args;
      if (timer !== undefined) clearTimeout(timer);
      timer = setTimeout(() => {
        timer = undefined;
        const call = pending;
        pending = undefined;
        if (call !== undefined) fn(...call);
      }, delayMs);
    },
    cancel,
    flush: (): void => {
      if (timer !== undefined) clearTimeout(timer);
      timer = undefined;
      const call = pending;
      pending = undefined;
      if (call !== undefined) fn(...call);
    },
  };
}

/**
 * Why a search could not be run, when the server refuses it.
 *
 * The server's repertoire is bounded by design (S3: unbounded work is what
 * sinks the instance under concurrency), so `unsupportedFilter` is a normal,
 * expected answer — not an error. Mapping it to a REASON, rather than to an
 * empty list, is the difference between "no results" (a lie: results may
 * exist) and "this server cannot answer that search" (the truth).
 */
export type SearchRefusal =
  /** The server cannot answer this filter shape. */
  | { readonly kind: "unsupported"; readonly description?: string }
  /** The result filled the server's window; more matches exist beyond it. */
  | { readonly kind: "truncated"; readonly shown: number };

/**
 * Classifies a JMAP method error into a refusal the UI can explain.
 *
 * Returns undefined for errors that are NOT refusals — those are real failures
 * and belong in the error taxonomy, not in a "we can't search that" message.
 */
export function refusalFor(type: string, description?: string): SearchRefusal | undefined {
  if (type === "unsupportedFilter" || type === "unsupportedSort") {
    return description !== undefined
      ? { kind: "unsupported", description }
      : { kind: "unsupported" };
  }
  return undefined;
}
