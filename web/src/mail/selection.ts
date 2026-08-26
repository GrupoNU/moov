/**
 * Multi-select with shift and ctrl ranges (P3 deliverable 1).
 *
 * # Why this is a pure module
 *
 * Range selection has an anchor, and the anchor is where every implementation
 * gets it wrong. The rules people actually expect — from Finder, Explorer,
 * Gmail and every file manager since 1984 — are:
 *
 *   - a plain click REPLACES the selection and moves the anchor;
 *   - ctrl/cmd-click TOGGLES one item and moves the anchor to it;
 *   - shift-click selects the range from the ANCHOR to the clicked item,
 *     replacing what a previous shift-click selected but NOT moving the
 *     anchor — so shift-clicking around grows and shrinks one range from a
 *     fixed origin rather than leapfrogging;
 *   - ctrl+shift-click ADDS the range to the existing selection.
 *
 * That last pair is the part that is invisible until it is wrong, and it is
 * exactly what a pure reducer can be tested against.
 */

/** The selection and the anchor a shift-range is measured from. */
export interface SelectionState {
  readonly selected: ReadonlySet<string>;
  /** The id a shift-range starts at, or undefined when there is none. */
  readonly anchor: string | undefined;
}

export const EMPTY_SELECTION: SelectionState = {
  selected: new Set<string>(),
  anchor: undefined,
};

/** The modifier keys of the click that caused the change. */
export interface SelectionModifiers {
  /** Ctrl on Windows/Linux, Meta on macOS — the caller normalises. */
  readonly toggle: boolean;
  readonly range: boolean;
}

/**
 * Applies a click on `id` within `orderedIds`.
 *
 * `orderedIds` is the list AS DISPLAYED, so a range follows what the user
 * sees rather than some underlying order — the distinction matters the moment
 * the list is sorted or filtered.
 */
export function selectionAfterClick(
  state: SelectionState,
  id: string,
  orderedIds: readonly string[],
  modifiers: SelectionModifiers,
): SelectionState {
  const { toggle, range } = modifiers;

  if (range && state.anchor !== undefined) {
    const anchorIndex = orderedIds.indexOf(state.anchor);
    const clickedIndex = orderedIds.indexOf(id);
    if (anchorIndex !== -1 && clickedIndex !== -1) {
      const from = Math.min(anchorIndex, clickedIndex);
      const to = Math.max(anchorIndex, clickedIndex);
      const inRange = orderedIds.slice(from, to + 1);
      // Ctrl+Shift ADDS the range; plain Shift replaces the selection with it.
      const next = toggle ? new Set(state.selected) : new Set<string>();
      for (const rangeId of inRange) next.add(rangeId);
      // The anchor deliberately does NOT move: shift-clicking around must grow
      // and shrink one range from a fixed origin, not leapfrog.
      return { selected: next, anchor: state.anchor };
    }
  }

  if (toggle) {
    const next = new Set(state.selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    return { selected: next, anchor: id };
  }

  // A plain click replaces the selection — unless it lands on the only
  // selected item, which is how you deselect without reaching for a modifier.
  if (state.selected.size === 1 && state.selected.has(id)) {
    return { selected: new Set<string>(), anchor: undefined };
  }
  return { selected: new Set([id]), anchor: id };
}

/** Selects everything, or clears the selection. */
export function selectionAfterSelectAll(
  orderedIds: readonly string[],
  selectAll: boolean,
): SelectionState {
  return selectAll
    ? { selected: new Set(orderedIds), anchor: orderedIds[0] }
    : EMPTY_SELECTION;
}

/**
 * Drops ids that no longer exist.
 *
 * Called after a refresh: a selection holding ids the server no longer returns
 * would make a bulk action target messages that are gone — and would keep the
 * "3 selected" badge lying about a list with two rows.
 */
export function pruneSelection(
  state: SelectionState,
  orderedIds: readonly string[],
): SelectionState {
  if (state.selected.size === 0 && state.anchor === undefined) return state;
  const present = new Set(orderedIds);

  const next = new Set<string>();
  for (const id of state.selected) {
    if (present.has(id)) next.add(id);
  }

  /*
   * The anchor is checked INDEPENDENTLY of the selection, and a test caught
   * this: an early return on "the selected set did not shrink" left a dangling
   * anchor pointing at a message that is gone. The next shift-click would then
   * measure its range from a row that is not in the list, and
   * `selectionAfterClick` would silently fall back to a plain click — losing
   * the selection the user was building, for no visible reason.
   */
  const anchor =
    state.anchor !== undefined && present.has(state.anchor) ? state.anchor : undefined;

  if (next.size === state.selected.size && anchor === state.anchor) return state;
  return { selected: next, anchor };
}

/** True when every displayed id is selected (and there is at least one). */
export function isAllSelected(
  state: SelectionState,
  orderedIds: readonly string[],
): boolean {
  if (orderedIds.length === 0) return false;
  return orderedIds.every((id) => state.selected.has(id));
}

/**
 * The ids an action applies to.
 *
 * The rule Gmail uses and this copies: when there IS a selection, an action
 * applies to it; when there is none, it applies to the focused row. That is
 * what makes `e` archive "the message I am looking at" without any selection
 * ceremony, while a selection makes the same key a bulk action.
 */
export function actionTargets(
  state: SelectionState,
  focusedId: string | undefined,
): readonly string[] {
  if (state.selected.size > 0) return [...state.selected];
  return focusedId === undefined ? [] : [focusedId];
}
