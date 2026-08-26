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
 * The scroll offset that brings a row fully into view, moving as little as
 * possible.
 *
 * "As little as possible" is what makes `j`/`k` feel right: a keyboard
 * selection that scrolls the row to the CENTRE on every press turns a list
 * into a slot machine. The row is only scrolled when it is actually outside
 * the viewport, and then only to its nearest edge.
 *
 * Returns undefined when no scrolling is needed, so callers can skip the
 * write entirely rather than setting `scrollTop` to its current value (which
 * cancels smooth scrolling in some browsers).
 */
export function scrollOffsetToReveal(
  index: number,
  scrollTop: number,
  viewportHeight: number,
  rowHeight: number = ROW_HEIGHT,
): number | undefined {
  const rowTop = index * rowHeight;
  const rowBottom = rowTop + rowHeight;

  if (rowTop < scrollTop) return rowTop;
  if (rowBottom > scrollTop + viewportHeight) {
    return Math.max(0, rowBottom - viewportHeight);
  }
  return undefined;
}

/** Total scrollable height for a list. */
export function totalHeight(itemCount: number, rowHeight: number = ROW_HEIGHT): number {
  return Math.max(0, itemCount * rowHeight);
}
