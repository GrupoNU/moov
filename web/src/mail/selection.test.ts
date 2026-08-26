import { describe, expect, it } from "vitest";

import {
  actionTargets,
  EMPTY_SELECTION,
  isAllSelected,
  pruneSelection,
  selectionAfterClick,
  selectionAfterSelectAll,
  type SelectionState,
} from "./selection";

const ids = ["a", "b", "c", "d", "e"];

const plain = { toggle: false, range: false };
const ctrl = { toggle: true, range: false };
const shift = { toggle: false, range: true };
const ctrlShift = { toggle: true, range: true };

/** Sorted for comparison — a Set has no order to assert on. */
function selected(state: SelectionState): string[] {
  return [...state.selected].sort();
}

describe("selectionAfterClick", () => {
  it("a plain click replaces the selection and moves the anchor", () => {
    let state = selectionAfterClick(EMPTY_SELECTION, "b", ids, plain);
    expect(selected(state)).toEqual(["b"]);
    expect(state.anchor).toBe("b");

    state = selectionAfterClick(state, "d", ids, plain);
    expect(selected(state)).toEqual(["d"]);
    expect(state.anchor).toBe("d");
  });

  it("a plain click on the only selected row deselects it", () => {
    const state = selectionAfterClick(EMPTY_SELECTION, "b", ids, plain);
    expect(selected(selectionAfterClick(state, "b", ids, plain))).toEqual([]);
  });

  it("ctrl-click toggles one row and moves the anchor to it", () => {
    let state = selectionAfterClick(EMPTY_SELECTION, "b", ids, plain);
    state = selectionAfterClick(state, "d", ids, ctrl);
    expect(selected(state)).toEqual(["b", "d"]);
    expect(state.anchor).toBe("d");

    state = selectionAfterClick(state, "b", ids, ctrl);
    expect(selected(state)).toEqual(["d"]);
  });

  it("shift-click selects the range from the anchor, inclusive", () => {
    let state = selectionAfterClick(EMPTY_SELECTION, "b", ids, plain);
    state = selectionAfterClick(state, "d", ids, shift);
    expect(selected(state)).toEqual(["b", "c", "d"]);
  });

  it("shift-click works backwards too", () => {
    let state = selectionAfterClick(EMPTY_SELECTION, "d", ids, plain);
    state = selectionAfterClick(state, "b", ids, shift);
    expect(selected(state)).toEqual(["b", "c", "d"]);
  });

  /*
   * The rule that is invisible until it is wrong: the anchor must NOT move on
   * a shift-click, so shift-clicking around grows and shrinks one range from a
   * fixed origin instead of leapfrogging.
   */
  it("shift-click does not move the anchor, so the range can be resized", () => {
    let state = selectionAfterClick(EMPTY_SELECTION, "b", ids, plain);
    state = selectionAfterClick(state, "e", ids, shift);
    expect(selected(state)).toEqual(["b", "c", "d", "e"]);
    expect(state.anchor).toBe("b");

    // Shrink it back — an anchor that had moved to "e" would select ["c","d","e"].
    state = selectionAfterClick(state, "c", ids, shift);
    expect(selected(state)).toEqual(["b", "c"]);
    expect(state.anchor).toBe("b");
  });

  it("shift-click REPLACES a previous shift range rather than accumulating", () => {
    let state = selectionAfterClick(EMPTY_SELECTION, "a", ids, plain);
    state = selectionAfterClick(state, "b", ids, shift);
    state = selectionAfterClick(state, "c", ids, shift);
    expect(selected(state)).toEqual(["a", "b", "c"]);
  });

  it("ctrl+shift ADDS the range to the existing selection", () => {
    let state = selectionAfterClick(EMPTY_SELECTION, "e", ids, plain);
    state = selectionAfterClick(state, "a", ids, ctrl);
    state = selectionAfterClick(state, "c", ids, ctrlShift);
    expect(selected(state)).toEqual(["a", "b", "c", "e"]);
  });

  it("shift with no anchor behaves like a plain click", () => {
    const state = selectionAfterClick(EMPTY_SELECTION, "c", ids, shift);
    expect(selected(state)).toEqual(["c"]);
    expect(state.anchor).toBe("c");
  });

  it("shift with an anchor no longer in the list falls back to a plain click", () => {
    const stale: SelectionState = { selected: new Set(["gone"]), anchor: "gone" };
    const state = selectionAfterClick(stale, "c", ids, shift);
    expect(selected(state)).toEqual(["c"]);
  });

  it("never mutates the input state", () => {
    const start = selectionAfterClick(EMPTY_SELECTION, "b", ids, plain);
    selectionAfterClick(start, "d", ids, ctrl);
    expect(selected(start)).toEqual(["b"]);
  });
});

describe("selectionAfterSelectAll", () => {
  it("selects everything displayed, anchored at the first row", () => {
    const state = selectionAfterSelectAll(ids, true);
    expect(selected(state)).toEqual(["a", "b", "c", "d", "e"]);
    expect(state.anchor).toBe("a");
  });

  it("clears everything, anchor included", () => {
    expect(selectionAfterSelectAll(ids, false)).toEqual(EMPTY_SELECTION);
  });
});

describe("pruneSelection", () => {
  /*
   * After a refresh a stale selection would make a bulk action target messages
   * that are gone, and would keep the "3 selected" badge lying about a list
   * with two rows.
   */
  it("drops ids the server no longer returns", () => {
    const state: SelectionState = { selected: new Set(["a", "gone", "c"]), anchor: "a" };
    expect(selected(pruneSelection(state, ids))).toEqual(["a", "c"]);
  });

  it("clears an anchor that vanished", () => {
    const state: SelectionState = { selected: new Set(["a"]), anchor: "gone" };
    expect(pruneSelection(state, ids).anchor).toBeUndefined();
  });

  it("returns the SAME object when nothing was pruned, so React skips work", () => {
    const state: SelectionState = { selected: new Set(["a", "c"]), anchor: "a" };
    expect(pruneSelection(state, ids)).toBe(state);
  });

  it("is a no-op on an empty selection", () => {
    expect(pruneSelection(EMPTY_SELECTION, ids)).toBe(EMPTY_SELECTION);
  });
});

describe("isAllSelected", () => {
  it("is true only when every displayed row is selected", () => {
    expect(isAllSelected({ selected: new Set(ids), anchor: "a" }, ids)).toBe(true);
    expect(isAllSelected({ selected: new Set(["a"]), anchor: "a" }, ids)).toBe(false);
  });

  it("is false for an empty list, so the header box is not checked", () => {
    expect(isAllSelected(EMPTY_SELECTION, [])).toBe(false);
  });
});

describe("actionTargets", () => {
  /*
   * Gmail's rule, copied: with a selection an action is a bulk action; with
   * none it applies to the focused row — which is what makes `e` archive "the
   * message I am looking at" without any selection ceremony.
   */
  it("uses the selection when there is one", () => {
    const state: SelectionState = { selected: new Set(["a", "b"]), anchor: "a" };
    expect([...actionTargets(state, "e")].sort()).toEqual(["a", "b"]);
  });

  it("falls back to the focused row", () => {
    expect(actionTargets(EMPTY_SELECTION, "c")).toEqual(["c"]);
  });

  it("is empty when there is neither", () => {
    expect(actionTargets(EMPTY_SELECTION, undefined)).toEqual([]);
  });
});
