/**
 * The list's pager (E12/B4, canon 07 §3).
 *
 * Gmail puts "1–50 de 15.224 ‹ ›" at the right of the toolbar row above the
 * list, and it is one of the few pieces of its chrome that carries genuine
 * INFORMATION rather than only navigation: it is the only place in the product
 * that tells you how much mail a folder holds.
 *
 * # What this can honestly say, and what it cannot
 *
 * Two server facts govern every sentence this module produces, and both are
 * quoted here because getting either wrong produces a number that looks
 * authoritative and is false.
 *
 * **1. The server pages, up to a ceiling.** `internal/jmap/mail/search.go`:
 *
 * ```
 * const MaxQueryReach = 100000
 * ```
 *
 * `Email/query` serves `position` by walking the index, and a regression test
 * (`query_paging_test.go`, 2026-08-26) pins that positions 200, 400 and 600
 * return the RIGHT ids over a 626-message corpus — the defect where every
 * message past the 200th was unreachable is fixed. So a real pager is
 * expressible. Past 100,000 it is not, and this module says so rather than
 * offering a "next" that returns nothing.
 *
 * **2. The total is EXACT or ABSENT — never an estimate.** `query.go`'s
 * `queryTotal`, on why a capped count is not put in `total`:
 *
 * ```
 * Given "report a wrong number" versus "omit the property", this omits it.
 * ```
 *
 * The server answers `total` only when the result was not truncated by its
 * window; otherwise the property is absent. So {@link pageLabel} has two
 * shapes, and the second is not a degraded version of the first — it is a
 * different, true sentence ("1–50" with no total) where the first would have
 * been a guess. That is the `sizeIsExact` honesty the row counts already carry,
 * applied to the pager.
 *
 * # Why 50 and why it is not a preference
 *
 * 50 is Gmail's own default page size. It is a CONSTANT here rather than a
 * setting because the list beneath it is virtualized: the rows a user scrolls
 * past are rendered on demand either way, so a preference would change nothing
 * they can see. `registry.ts` records that omission with its reason
 * (`PAGE_SIZE_ROW_OMITTED`).
 */

/** The rows one page holds. Gmail's default; see the header on why it is fixed. */
export const PAGE_SIZE = 50;

/**
 * The deepest position the server will serve (`mail.MaxQueryReach`).
 *
 * Mirrored rather than discovered, for the same reason the offline depth bounds
 * are mirrored in `prefs.ts`: a pager must be able to disable "next" BEFORE
 * sending a request that would come back empty. A greyed arrow at the ceiling
 * is honest; an arrow that pages into nothing is the dead control P4 forbids.
 *
 * A test pins this against the Go constant's documented value, so the two
 * cannot drift silently across the seam no compiler spans.
 */
export const MAX_REACH = 100000;

/** Everything the pager needs to describe and navigate one page. */
export interface PageState {
  /** The 0-based index of the first row on this page. */
  readonly position: number;
  /** How many rows this page actually returned. */
  readonly shown: number;
  /**
   * The server's exact total, or undefined when it declined to count.
   *
   * Undefined is NOT "unknown because we did not ask": it is the server saying
   * the result filled its window, so any number it could give would be a floor
   * rather than a total. See the header.
   */
  readonly total: number | undefined;
}

/**
 * What the pager can honestly assert about how much mail is behind it.
 *
 * The three cases are not a value plus two fallbacks — they are three
 * different true statements, and which one applies is decidable from data the
 * client already has.
 */
export type PageBound =
  /** The server counted: `de 15.224`. */
  | { readonly kind: "exact"; readonly count: number }
  /**
   * The server declined to count AND there is another page, so the folder holds
   * strictly more than what has been served: `de más de 50`.
   */
  | { readonly kind: "atLeast"; readonly count: number }
  /**
   * The server declined to count and this is the LAST page, which makes the
   * count exact anyway — the result is exhausted, so `position + shown` is not
   * a floor, it is the answer. See {@link pageBound}.
   */
  | { readonly kind: "none" };

/**
 * The bound, from what the client knows (B-01).
 *
 * # Why this is not a protocol change
 *
 * The server keeps omitting the exact count, and deliberately: `query.go`'s
 * `queryTotal` measured an exact count at 452 ms p95 over the reference corpus,
 * past the product's own bar, and chose "omit the property" over "report a
 * wrong number". Nothing here asks it to reconsider. What this adds is the
 * sentence Gmail writes when it is in the same position — "de más de 1.000" —
 * assembled from two facts the client already holds: how many rows it has been
 * served, and whether {@link hasNextPage} says there are more.
 *
 * # The case that surprises people
 *
 * When the server gave no total and there is NO next page, the count is exact.
 * `hasNextPage` returns false for a short page, which means the result was
 * exhausted inside the window we asked for — so `position + shown` is the
 * whole result, not a floor. Saying "more than 31" over a folder holding
 * exactly 31 would be the same kind of small lie the omission exists to avoid,
 * in the opposite direction. It returns `none` rather than `exact` so the
 * caller renders the plain range: on a single short page "1–31" already says
 * everything "1–31 de 31" would, and Gmail writes it the short way too.
 */
export function pageBound(state: PageState): PageBound {
  if (state.total !== undefined) return { kind: "exact", count: state.total };
  if (!hasNextPage(state)) return { kind: "none" };
  return { kind: "atLeast", count: state.position + state.shown };
}

/**
 * The human-readable range, as Gmail writes it.
 *
 * Three shapes, and none is a degraded version of another — see
 * {@link pageBound} for which fact each one states:
 *
 *   - counted:        `1–50 de 15.224`
 *   - bounded below:  `1–50 de más de 50`
 *   - exhausted:      `1–31`
 *
 * Every number is formatted by the CALLER's locale formatter, which is why this
 * takes them rather than reaching for `toLocaleString` — the thousands
 * separator is "." in es-419 and "," in en, and a pager that gets that wrong
 * looks like a different product in one of the two locales.
 *
 * An EMPTY page returns undefined rather than "0–0 de 0": there is nothing to
 * page through, and the list already renders its own empty state, which says
 * something more useful than a range of nothing.
 */
export function pageLabel(
  state: PageState,
  format: (first: number, last: number, total: number) => string,
  formatWithoutTotal: (first: number, last: number) => string,
  /**
   * `1–50 de más de 50`. Optional so a caller with no such string still gets
   * the old two shapes rather than a crash — the pager renders in more than one
   * place and a missing translation must degrade to a true sentence.
   */
  formatAtLeast?: (first: number, last: number, atLeast: number) => string,
): string | undefined {
  if (state.shown <= 0) return undefined;
  const first = state.position + 1;
  const last = state.position + state.shown;
  const bound = pageBound(state);
  if (bound.kind === "exact") return format(first, last, bound.count);
  if (bound.kind === "atLeast" && formatAtLeast !== undefined) {
    return formatAtLeast(first, last, bound.count);
  }
  return formatWithoutTotal(first, last);
}

/**
 * Whether there is a previous page.
 *
 * Purely a function of position: page 1 has nothing before it, and every other
 * page does. Unlike "next", this can never be wrong — the rows behind us were
 * served, so they exist.
 */
export function hasPreviousPage(state: PageState): boolean {
  return state.position > 0;
}

/**
 * Whether there is a next page — the one answer that must not over-promise.
 *
 * Three ways it is FALSE, and each is a different fact:
 *
 *   1. **A short page.** Fewer rows came back than were asked for, so the
 *      result is exhausted. This is the ordinary end of a folder and is the
 *      only signal available when the server declined to count.
 *   2. **The total says so.** With an exact total, `position + shown >= total`
 *      is definitive.
 *   3. **The reach ceiling.** The next page would start at or past
 *      `MAX_REACH`, which the server cannot serve. Offering the arrow anyway
 *      would page a user into a permanently empty list with no explanation.
 *
 * The default when none of the three fires is TRUE, and that is the right
 * default: a full page with no total means the server had more matches than its
 * window, which is precisely "there is more". Defaulting to false would strand
 * a user at row 50 of a folder holding thousands.
 */
export function hasNextPage(state: PageState): boolean {
  if (state.shown < PAGE_SIZE) return false;
  if (state.total !== undefined && state.position + state.shown >= state.total) return false;
  if (state.position + PAGE_SIZE >= MAX_REACH) return false;
  return true;
}

/**
 * The position one page forward, clamped at the reach ceiling.
 *
 * Clamped rather than refused: a caller that ignores {@link hasNextPage} gets a
 * position the server can still answer instead of one it will serve as an empty
 * list. Belt and braces on a boundary whose failure is silent.
 */
export function nextPosition(state: PageState): number {
  return Math.min(state.position + PAGE_SIZE, Math.max(0, MAX_REACH - PAGE_SIZE));
}

/** The position one page back, never below zero. */
export function previousPosition(state: PageState): number {
  return Math.max(0, state.position - PAGE_SIZE);
}
