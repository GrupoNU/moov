/**
 * Virtual list windowing math (P2 deliverable 3).
 *
 * Pure arithmetic, deliberately separated from the component that renders it.
 * Windowing bugs — a row that flickers at a scroll boundary, a list that jumps
 * when data arrives — are off-by-one errors, and an off-by-one error is worth
 * a unit test rather than a scroll session.
 *
 * # Why fixed-height rows
 *
 * Every row in this list is the same height. That is a design decision, not a
 * simplification: variable heights require measuring each row after render,
 * which means the scrollbar length changes as you scroll (the "jumpy
 * scrollbar" every measured-list implementation fights), and it makes
 * `scrollTop → index` a search instead of a division. Gmail, Fastmail and
 * Superhuman all use fixed-height rows for exactly this reason. Multi-line
 * previews are traded away for a list that cannot jump.
 */

/** The pixel height of one message row. Must match `--row-height` in CSS. */
export const ROW_HEIGHT = 72;

/**
 * How many rows to render beyond the viewport on each side.
 *
 * Overscan trades memory for the chance that a fast scroll outruns React's
 * next paint and shows blank space. Six rows ≈ 432 px of buffer above and
 * below, which covers a flick on a 60 Hz display without rendering a
 * meaningful fraction of a 200-row window.
 */
export const OVERSCAN = 6;

/** The slice of rows a virtualized list should render. */
export interface WindowRange {
  /** First index to render, inclusive. */
  readonly start: number;
  /** Last index to render, exclusive. */
  readonly end: number;
  /** Pixels of spacer above the rendered slice. */
  readonly paddingTop: number;
  /** Pixels of spacer below the rendered slice. */
  readonly paddingBottom: number;
}

export interface WindowInput {
  readonly scrollTop: number;
  readonly viewportHeight: number;
  readonly itemCount: number;
  readonly rowHeight?: number;
  readonly overscan?: number;
}

/**
 * Computes which rows to render for a given scroll position.
 *
 * Total content height is `itemCount * rowHeight`, and the two paddings sum
 * with the rendered rows to exactly that — which is the invariant that keeps
 * the scrollbar honest and is asserted by a test. Getting it wrong by a pixel
 * per row is how a long list slowly drifts away from its scrollbar.
 */
export function computeWindow({
  scrollTop,
  viewportHeight,
  itemCount,
  rowHeight = ROW_HEIGHT,
  overscan = OVERSCAN,
}: WindowInput): WindowRange {
  if (itemCount <= 0 || rowHeight <= 0) {
    return { start: 0, end: 0, paddingTop: 0, paddingBottom: 0 };
  }

  // A negative scrollTop is real: iOS and macOS rubber-band past the top.
  const safeScrollTop = Math.max(0, scrollTop);
  const safeViewport = Math.max(0, viewportHeight);

  const firstVisible = Math.floor(safeScrollTop / rowHeight);
  // `ceil` on the sum rather than on the height alone: a viewport that is not a
  // whole number of rows and is scrolled to a fraction of a row shows one more
  // row than `ceil(height / rowHeight)` predicts.
  const lastVisible = Math.ceil((safeScrollTop + safeViewport) / rowHeight);

  const end = Math.min(itemCount, lastVisible + overscan);
  /*
   * `start` is clamped against `end`, not only against 0.
   *
   * Without the upper clamp, a scrollTop far past the content (which happens
   * when the list SHRINKS under a stable scroll position — switching from a
   * 626-row folder to a 6-row one before the scroll container has been reset)
   * yields start > end: an inverted range that slices to nothing and makes
   * paddingBottom negative. The list renders empty at a valid scroll offset,
   * which looks exactly like a data-loading failure.
   */
  const start = Math.min(Math.max(0, firstVisible - overscan), end);

  return {
    start,
    end,
    paddingTop: start * rowHeight,
    paddingBottom: Math.max(0, (itemCount - end) * rowHeight),
  };
}

/** The scroll offset that puts a row at the top of the viewport. */
export function offsetForIndex(index: number, rowHeight: number = ROW_HEIGHT): number {
  return Math.max(0, index * rowHeight);
}

/**
 * How much of the list stays visible past the cursor row, in ROWS (B-13).
 *
 * One row at each edge. `j` used to land the cursor flush against the bottom of
 * the viewport with nothing under it, which is the artefact the side-by-side
 * review caught: the keyboard user cannot see what they are about to move onto,
 * so every press is a step into the dark and the list feels like it ends at the
 * cursor. Gmail (and every editor with a `scrolloff`) keeps a row of context on
 * the far side.
 *
 * One rather than two or three: the margin is context, not centring. Larger
 * values start scrolling the list on presses that did not need to scroll it,
 * which is the slot-machine feel `scrollOffsetToReveal` was written to avoid.
 */
export const SCROLL_MARGIN_ROWS = 1;

/**
 * The scroll offset that brings a row into view WITH a row of margin beside it,
 * moving as little as possible.
 *
 * "As little as possible" is what makes `j`/`k` feel right: a keyboard
 * selection that scrolls the row to the CENTRE on every press turns a list into
 * a slot machine. The row is only scrolled when it is inside the margin at
 * either edge, and then only far enough to clear it.
 *
 * # The margin, and the two places it must not apply
 *
 * The target is not the row but the row PLUS {@link SCROLL_MARGIN_ROWS} of
 * neighbour on the side being approached. At the very ends of the list that
 * neighbour does not exist, and demanding it would scroll past the content:
 * both results are therefore clamped — the top at 0 (which is what stops `k` on
 * row 0 from asking for a negative offset) and the bottom at the last scrollable
 * position, so `j` on the final row does not leave a blank strip below it.
 *
 * Returns undefined when no scrolling is needed, so callers can skip the write
 * entirely rather than setting `scrollTop` to its current value (which cancels
 * smooth scrolling in some browsers).
 */
export function scrollOffsetToReveal(
  index: number,
  scrollTop: number,
  viewportHeight: number,
  rowHeight: number = ROW_HEIGHT,
  /**
   * The list's length, so the bottom margin can be clamped against the real end
   * of the content. Absent means "unknown", and the offset is then left
   * unclamped at the bottom — the old behaviour, which is safe because a
   * caller that cannot say how long its list is also cannot be over-scrolled by
   * this function beyond one row.
   */
  itemCount?: number,
): number | undefined {
  const margin = SCROLL_MARGIN_ROWS * rowHeight;
  const rowTop = index * rowHeight;
  const rowBottom = rowTop + rowHeight;

  // The furthest the container can scroll, when we know how long it is.
  const maxOffset =
    itemCount === undefined
      ? undefined
      : Math.max(0, itemCount * rowHeight - viewportHeight);

  if (rowTop - margin < scrollTop) {
    // Approaching from above: reveal the row and the one before it. Clamped at
    // 0 for the first rows, where there is no row before it to reveal.
    const wanted = Math.max(0, rowTop - margin);
    // Only scroll if that actually moves us UP; on the first rows the clamp can
    // land exactly where we already are.
    return wanted < scrollTop ? wanted : undefined;
  }

  if (rowBottom + margin > scrollTop + viewportHeight) {
    const wanted = Math.max(0, rowBottom + margin - viewportHeight);
    // At the end of the list the margin has nothing to reveal, so the offset is
    // clamped to the last scrollable position rather than scrolling past it and
    // leaving a blank strip under the final row.
    const clamped = maxOffset === undefined ? wanted : Math.min(wanted, maxOffset);
    return clamped > scrollTop ? clamped : undefined;
  }

  return undefined;
}

/** Total scrollable height for a list. */
export function totalHeight(itemCount: number, rowHeight: number = ROW_HEIGHT): number {
  return Math.max(0, itemCount * rowHeight);
}
