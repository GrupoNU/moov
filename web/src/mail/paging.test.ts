import { describe, expect, it } from "vitest";

import {
  hasNextPage,
  hasPreviousPage,
  MAX_REACH,
  nextPosition,
  PAGE_SIZE,
  pageBound,
  pageLabel,
  previousPosition,
  type PageState,
} from "./paging";

/**
 * The pager (E12/B4).
 *
 * The interesting assertions here are all about `hasNextPage`, because it is
 * the one answer that can OVER-PROMISE — and an arrow that pages into a
 * permanently empty list is worse than no arrow at all, since the user has no
 * way to tell it from a broken app.
 */

/** A page, with the defaults most cases want. */
function page(over: Partial<PageState> = {}): PageState {
  return { position: 0, shown: PAGE_SIZE, total: undefined, ...over };
}

// The two formatters the app supplies, in their English shapes.
const withTotal = (first: number, last: number, total: number): string =>
  `${String(first)}–${String(last)} of ${String(total)}`;
const withoutTotal = (first: number, last: number): string =>
  `${String(first)}–${String(last)}`;
/* B-01: the third shape — a floor, worded as one. */
const atLeast = (first: number, last: number, count: number): string =>
  `${String(first)}–${String(last)} of more than ${String(count)}`;

describe("pageLabel", () => {
  it("writes Gmail's range with the total when the server gave one", () => {
    expect(pageLabel(page({ total: 15224 }), withTotal, withoutTotal)).toBe(
      "1–50 of 15224",
    );
  });

  it("writes the range ALONE when the server declined to count and none is asked for", () => {
    /*
     * Not a degraded version of the first sentence — a different true one. The
     * server omits `total` when the result filled its window ("report a wrong
     * number versus omit the property, this omits it"), so any number here
     * would be a floor dressed as a total.
     *
     * A caller that supplies no `formatAtLeast` still gets this, which is what
     * makes the fourth argument safe to omit: it degrades to a true sentence
     * rather than to a crash or to a guess.
     */
    expect(pageLabel(page(), withTotal, withoutTotal)).toBe("1–50");
  });

  it("states the FLOOR when the server declined to count and there is more (B-01)", () => {
    // The client knows two things the server did not say: 50 rows arrived, and
    // `hasNextPage` is true. "More than 50" is the strongest sentence those two
    // support, and it is the one Gmail writes.
    expect(pageLabel(page(), withTotal, withoutTotal, atLeast)).toBe("1–50 of more than 50");
  });

  it("carries the floor forward with the offset", () => {
    expect(pageLabel(page({ position: 100 }), withTotal, withoutTotal, atLeast)).toBe(
      "101–150 of more than 150",
    );
  });

  it("does NOT claim 'more than' on a short page — that page IS the end", () => {
    /*
     * The opposite lie to the one the server's omission avoids. A short page
     * means the result was exhausted inside the window, so "more than 31" over
     * a folder of exactly 31 would be false. The plain range is written
     * instead: on a single short page "1–31" already says everything.
     */
    expect(pageLabel(page({ shown: 31 }), withTotal, withoutTotal, atLeast)).toBe("1–31");
  });

  it("prefers the server's exact count over any floor the client could compute", () => {
    expect(pageLabel(page({ total: 15224 }), withTotal, withoutTotal, atLeast)).toBe(
      "1–50 of 15224",
    );
  });

  it("counts from ONE, because a user is not an array index", () => {
    expect(pageLabel(page({ position: 50, total: 120 }), withTotal, withoutTotal)).toBe(
      "51–100 of 120",
    );
  });

  it("ends at what actually arrived, not at what was asked for", () => {
    // A last page of 20 rows says 101–120, never 101–150.
    expect(
      pageLabel(page({ position: 100, shown: 20, total: 120 }), withTotal, withoutTotal),
    ).toBe("101–120 of 120");
  });

  it("says nothing at all for an empty page", () => {
    // "0–0 of 0" is noise: the list renders its own empty state, which says
    // something more useful than a range over nothing.
    expect(pageLabel(page({ shown: 0, total: 0 }), withTotal, withoutTotal)).toBeUndefined();
  });
});

describe("pageBound — what the client may assert (B-01)", () => {
  it("passes the server's count through when there is one", () => {
    expect(pageBound(page({ total: 15224 }))).toEqual({ kind: "exact", count: 15224 });
  });

  it("floors at what has been served when the server declined and there is more", () => {
    expect(pageBound(page({ position: 50 }))).toEqual({ kind: "atLeast", count: 100 });
  });

  it("asserts NOTHING on an exhausted result rather than a floor equal to the truth", () => {
    /*
     * The subtle one. A short page with no total means the count IS
     * `position + shown` — but "more than 31" over exactly 31 is false, and
     * "31 of 31" is noise the range already carries. So: no bound.
     */
    expect(pageBound(page({ shown: 31 }))).toEqual({ kind: "none" });
  });

  it("asserts nothing at the reach ceiling either", () => {
    // `hasNextPage` is false there, so there is no evidence of more mail — only
    // evidence that the server will not look further, which is a different
    // thing and not a claim about how much exists.
    expect(pageBound(page({ position: MAX_REACH - PAGE_SIZE }))).toEqual({ kind: "none" });
  });
});

describe("hasPreviousPage", () => {
  it("is false on the first page and true on every other", () => {
    expect(hasPreviousPage(page())).toBe(false);
    expect(hasPreviousPage(page({ position: PAGE_SIZE }))).toBe(true);
  });
});

describe("hasNextPage", () => {
  it("is true for a full page with no total — the server had more than its window", () => {
    /*
     * The default that matters. Defaulting to false here would strand a user at
     * row 50 of a folder holding thousands, which is exactly the defect the
     * server's own paging fix (query_paging_test.go) was written for.
     */
    expect(hasNextPage(page())).toBe(true);
  });

  it("is false for a SHORT page: fewer rows than asked for means exhausted", () => {
    expect(hasNextPage(page({ shown: 12 }))).toBe(false);
  });

  it("is false when an exact total says this page reached the end", () => {
    expect(hasNextPage(page({ position: 100, shown: PAGE_SIZE, total: 150 }))).toBe(false);
  });

  it("is true when an exact total says there is more", () => {
    expect(hasNextPage(page({ position: 100, shown: PAGE_SIZE, total: 151 }))).toBe(true);
  });

  it("stops at the server's reach ceiling rather than paging into nothing", () => {
    /*
     * `mail.MaxQueryReach` is a real wall: past it `Email/query` answers the
     * empty list. A "next" arrow there would look like the app losing the
     * user's mail, so the arrow goes away instead — a boundary the client can
     * only respect by mirroring the constant.
     */
    const atCeiling = page({ position: MAX_REACH - PAGE_SIZE, total: MAX_REACH * 2 });
    expect(hasNextPage(atCeiling)).toBe(false);
  });

  it("still pages right up TO the ceiling", () => {
    const oneBefore = page({ position: MAX_REACH - 2 * PAGE_SIZE, total: MAX_REACH * 2 });
    expect(hasNextPage(oneBefore)).toBe(true);
  });
});

describe("navigation", () => {
  it("steps by exactly one page in each direction", () => {
    expect(nextPosition(page({ position: 100 }))).toBe(150);
    expect(previousPosition(page({ position: 100 }))).toBe(50);
  });

  it("never steps below zero", () => {
    expect(previousPosition(page({ position: 0 }))).toBe(0);
    // A position that is not a whole page (from a stale URL, say) still lands
    // on something the server can answer rather than on a negative offset.
    expect(previousPosition(page({ position: 20 }))).toBe(0);
  });

  it("clamps forward at the ceiling even if the caller ignored hasNextPage", () => {
    // Belt and braces on a boundary whose failure is a silently empty list.
    const beyond = nextPosition(page({ position: MAX_REACH }));
    expect(beyond).toBeLessThanOrEqual(MAX_REACH - PAGE_SIZE);
  });
});

describe("the constants are the server's, not ours", () => {
  it("mirrors mail.MaxQueryReach", () => {
    /*
     * The seam no compiler spans. `internal/jmap/mail/search.go` pins this
     * value with its own test (`TestMaxQueryReachIsTheSignedD7Ceiling`, ADR §6)
     * because it is a DECISION with measurements behind it; this is the client
     * half of the same pin.
     */
    expect(MAX_REACH).toBe(100000);
  });

  it("uses Gmail's page size", () => {
    expect(PAGE_SIZE).toBe(50);
  });
});
