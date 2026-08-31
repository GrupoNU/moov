import { afterEach, describe, expect, it, vi } from "vitest";

import {
  clampPane,
  loadPaneSize,
  loadSidebarCollapsed,
  PANE_BOUNDS,
  savePaneSize,
  saveSidebarCollapsed,
} from "./viewChrome";

/**
 * View chrome (E12).
 *
 * The point of these tests is not the storage round trip — that is one line of
 * localStorage. It is the CLAMPING, because the stored values are untrusted
 * input that outlives the build that wrote them: a divider restored to -400px
 * renders a reading pane the user cannot see and cannot drag back, with no
 * error to explain it.
 */

afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("clampPane", () => {
  it("returns a value already inside the bound unchanged", () => {
    expect(clampPane(600, "width")).toBe(600);
    expect(clampPane(300, "height")).toBe(300);
  });

  it("clamps to the bound rather than rejecting", () => {
    expect(clampPane(-400, "width")).toBe(PANE_BOUNDS.width.min);
    expect(clampPane(99999, "width")).toBe(PANE_BOUNDS.width.max);
    expect(clampPane(0, "height")).toBe(PANE_BOUNDS.height.min);
  });

  it("falls back to the default for a value that is not a number", () => {
    // NaN is what `Number.parseFloat` returns for a corrupted stored string,
    // and it compares false against every bound — so a naive `<`/`>` clamp
    // would pass it straight through into a CSS length.
    expect(clampPane(Number.NaN, "width")).toBe(PANE_BOUNDS.width.default);
    expect(clampPane(Number.POSITIVE_INFINITY, "height")).toBe(
      PANE_BOUNDS.height.default,
    );
  });

  it("rounds, so a stored value and the rendered pixel agree", () => {
    expect(clampPane(520.6, "width")).toBe(521);
  });
});

describe("loadPaneSize", () => {
  it("is the default when nothing was ever stored", () => {
    expect(loadPaneSize("width")).toBe(PANE_BOUNDS.width.default);
  });

  it("round-trips a stored size", () => {
    savePaneSize("width", 640);
    expect(loadPaneSize("width")).toBe(640);
  });

  it("clamps a stored value that is out of range", () => {
    // The case a future build's different bounds would produce.
    localStorage.setItem("moov.chrome.readerWidth", "5000");
    expect(loadPaneSize("width")).toBe(PANE_BOUNDS.width.max);
  });

  it("falls back to the default for a stored value that is not a number", () => {
    localStorage.setItem("moov.chrome.readerWidth", "[object Object]");
    expect(loadPaneSize("width")).toBe(PANE_BOUNDS.width.default);
  });

  it("keeps the two axes independent", () => {
    savePaneSize("width", 700);
    savePaneSize("height", 300);
    expect(loadPaneSize("width")).toBe(700);
    expect(loadPaneSize("height")).toBe(300);
  });
});

describe("loadSidebarCollapsed", () => {
  it("defaults to expanded", () => {
    expect(loadSidebarCollapsed()).toBe(false);
  });

  it("round-trips both directions", () => {
    saveSidebarCollapsed(true);
    expect(loadSidebarCollapsed()).toBe(true);
    saveSidebarCollapsed(false);
    expect(loadSidebarCollapsed()).toBe(false);
  });

  it("treats anything but the exact flag as expanded", () => {
    // A permissive read (`raw !== null`) would make a leftover "false" from
    // some other build collapse the rail — a bug impossible to reproduce from
    // a user's description.
    localStorage.setItem("moov.chrome.sidebarCollapsed", "false");
    expect(loadSidebarCollapsed()).toBe(false);
    localStorage.setItem("moov.chrome.sidebarCollapsed", "yes");
    expect(loadSidebarCollapsed()).toBe(false);
  });
});

describe("storage that throws", () => {
  it("does not propagate — Safari private mode has a zero quota", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("SecurityError");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("QuotaExceededError");
    });

    expect(() => {
      saveSidebarCollapsed(true);
    }).not.toThrow();
    expect(loadSidebarCollapsed()).toBe(false);
    expect(loadPaneSize("width")).toBe(PANE_BOUNDS.width.default);
  });
});
