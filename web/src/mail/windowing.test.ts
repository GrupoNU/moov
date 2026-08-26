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
  it("does not scroll when the row is already fully visible", () => {
    // The property that makes j/k feel right: no movement unless needed.
    expect(scrollOffsetToReveal(3, 0, 720, 72)).toBeUndefined();
  });

  it("scrolls up to the row's top when it is above the viewport", () => {
    expect(scrollOffsetToReveal(2, 500, 720, 72)).toBe(144);
  });

  it("scrolls down by the minimum needed when the row is below", () => {
    // Row 20 spans 1440-1512; viewport is 0-720 → bottom-align at 792.
    expect(scrollOffsetToReveal(20, 0, 720, 72)).toBe(792);
  });

  it("never returns a negative offset", () => {
    expect(scrollOffsetToReveal(0, 100, 50, 72)).toBe(0);
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
