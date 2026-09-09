import { describe, expect, it } from "vitest";

import {
  computeWindow,
  offsetForIndex,
  ROW_HEIGHT,
  scrollOffsetToReveal,
  totalHeight,
} from "./windowing";

describe("computeWindow", () => {
  it("renders only a slice of a large list", () => {
    const range = computeWindow({
      scrollTop: 0,
      viewportHeight: 720,
      itemCount: 10_000,
      rowHeight: 72,
      overscan: 6,
    });
    // 10 visible rows + 6 overscan below; nothing above at the top.
    expect(range.start).toBe(0);
    expect(range.end).toBe(16);
    expect(range.end - range.start).toBeLessThan(30);
  });

  /*
   * THE INVARIANT. The two spacers plus the rendered rows must equal the total
   * content height exactly, at every scroll position — this is what keeps the
   * scrollbar's length and position honest. A per-row rounding error would let
   * a long list drift away from its scrollbar.
   */
  it("keeps paddingTop + rendered + paddingBottom === total height", () => {
    const itemCount = 626; // the pilot's real seeded INBOX
    const rowHeight = 72;
    const total = totalHeight(itemCount, rowHeight);

    for (const scrollTop of [0, 1, 37, 720, 5_000, 20_000, total, total + 500]) {
      const range = computeWindow({ scrollTop, viewportHeight: 800, itemCount, rowHeight });
      const rendered = (range.end - range.start) * rowHeight;
      expect(range.paddingTop + rendered + range.paddingBottom).toBe(total);
    }
  });

  it("never returns indices outside the list", () => {
    const range = computeWindow({
      scrollTop: 1_000_000,
      viewportHeight: 800,
      itemCount: 50,
      rowHeight: 72,
    });
    expect(range.start).toBeGreaterThanOrEqual(0);
    expect(range.end).toBeLessThanOrEqual(50);
    expect(range.start).toBeLessThanOrEqual(range.end);
    // The real symptom of an inverted range: a negative spacer.
    expect(range.paddingBottom).toBeGreaterThanOrEqual(0);
  });

  it("stays consistent when the list shrinks under a stale scroll position", () => {
    // Switching from a 626-row folder to a 6-row one before the scroll
    // container resets: scrollTop is valid for the old list, not the new one.
    const range = computeWindow({
      scrollTop: 40_000,
      viewportHeight: 800,
      itemCount: 6,
      rowHeight: 72,
    });
    const rendered = (range.end - range.start) * 72;
    expect(range.paddingTop + rendered + range.paddingBottom).toBe(6 * 72);
    expect(range.paddingBottom).toBeGreaterThanOrEqual(0);
  });

  it("handles a rubber-band scroll past the top (negative scrollTop)", () => {
    // Real on iOS and macOS; a naive floor() would produce negative indices.
    const range = computeWindow({
      scrollTop: -220,
      viewportHeight: 800,
      itemCount: 100,
      rowHeight: 72,
    });
    expect(range.start).toBe(0);
    expect(range.paddingTop).toBe(0);
  });

  it("returns an empty range for an empty list", () => {
    const range = computeWindow({ scrollTop: 0, viewportHeight: 800, itemCount: 0 });
    expect(range).toEqual({ start: 0, end: 0, paddingTop: 0, paddingBottom: 0 });
  });

  it("covers the viewport when scrolled to a fraction of a row", () => {
    // The ceil-on-the-sum case: a partial first row means one more row is
    // visible than ceil(height / rowHeight) predicts.
    const rowHeight = 72;
    const range = computeWindow({
      scrollTop: 36,
      viewportHeight: 720,
      itemCount: 1000,
      rowHeight,
      overscan: 0,
    });
    expect(range.start * rowHeight).toBeLessThanOrEqual(36);
    expect(range.end * rowHeight).toBeGreaterThanOrEqual(36 + 720);
  });

  it("scales to 100k rows without enumerating them", () => {
    const range = computeWindow({
      scrollTop: 3_600_000,
      viewportHeight: 900,
      itemCount: 100_000,
      rowHeight: 72,
    });
    expect(range.end - range.start).toBeLessThan(40);
  });
});

describe("scrollOffsetToReveal", () => {
  it("does not scroll when the row and its margin are already visible", () => {
    // The property that makes j/k feel right: no movement unless needed.
    expect(scrollOffsetToReveal(3, 0, 720, 72)).toBeUndefined();
  });

  it("scrolls up far enough to show the row BEFORE the cursor (B-13)", () => {
    // Row 2 spans 144-216. Without the margin this stopped at 144, putting the
    // cursor flush against the top edge with nothing above it to move onto.
    expect(scrollOffsetToReveal(2, 500, 720, 72)).toBe(72);
  });

  it("scrolls down far enough to show the row AFTER the cursor (B-13)", () => {
    /*
     * Row 20 spans 1440-1512; a 720px viewport bottom-aligned at 792 used to
     * leave the cursor flush against the bottom with nothing under it — the
     * artefact the side-by-side review caught. One row of margin puts it at
     * 864, which is 792 + one 72px row.
     */
    expect(scrollOffsetToReveal(20, 0, 720, 72, 100)).toBe(864);
  });

  it("starts scrolling ONE ROW EARLIER than it used to", () => {
    /*
     * Row 10 spans 720-792 and the viewport is 0-720, so the row is entirely
     * out of view by one pixel of its top edge — but the interesting case is
     * row 9 (648-720), which is fully visible and yet sits ON the bottom edge.
     * The margin is what makes that a scroll: it is the whole point.
     */
    expect(scrollOffsetToReveal(9, 0, 720, 72, 100)).toBe(72);
  });

  it("does not demand a margin the list does not have, at the END", () => {
    /*
     * The last row of a 10-row list: 648-720 against a 720px viewport whose
     * maximum scroll is 10*72 - 720 = 0. Asking for a row of margin under the
     * final row would scroll to 72 and leave a blank strip under the list, so
     * the offset is clamped to the last scrollable position — which here means
     * no scroll at all.
     */
    expect(scrollOffsetToReveal(9, 0, 720, 72, 10)).toBeUndefined();
  });

  it("clamps the bottom margin against the real end of a longer list", () => {
    // 30 rows = 2160px of content in a 720px viewport → max offset 1440. Row 29
    // (2088-2160) would want 2160 + 72 - 720 = 1512, which is past the end.
    expect(scrollOffsetToReveal(29, 0, 720, 72, 30)).toBe(1440);
  });

  it("never returns a negative offset, and needs no margin at the TOP", () => {
    // Row 0 has nothing above it, so the margin is clamped away rather than
    // producing an offset the container cannot take.
    expect(scrollOffsetToReveal(0, 100, 50, 72)).toBe(0);
    expect(scrollOffsetToReveal(0, 0, 720, 72, 100)).toBeUndefined();
  });

  it("leaves the bottom unclamped when the caller cannot say how long the list is", () => {
    // The documented degradation: without `itemCount` the margin still applies,
    // it simply is not clamped against an end this function was not told about.
    expect(scrollOffsetToReveal(20, 0, 720, 72)).toBe(864);
  });
});

describe("offsetForIndex and totalHeight", () => {
  it("computes a row's offset", () => {
    expect(offsetForIndex(10, 72)).toBe(720);
  });

  it("clamps a negative index", () => {
    expect(offsetForIndex(-5, 72)).toBe(0);
  });

  it("computes the total height", () => {
    expect(totalHeight(626, ROW_HEIGHT)).toBe(626 * ROW_HEIGHT);
  });
});
