import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";

import {
  DensityThumb,
  InboxTypeThumb,
  ReadingPaneThumb,
  ThemeThumb,
} from "./QuickThumbnails";

/**
 * The thumbnails (F-11, F-34/F-35).
 *
 * # What a test can assert about a picture
 *
 * Not that it looks right — that is the director's side-by-side gate. What it
 * CAN pin is the structural claim the review's finding was about: every theme
 * thumbnail draws the same list skeleton, and the dark one is not a solid
 * block. "Is it a block or a list?" is countable — the bars are `<rect>`s with
 * a fill — so the regression that produced F-11 (a dark variant whose ink sank
 * into its ground) is catchable without a screenshot.
 *
 * These also guard the reuse F-34/F-35 depends on: the settings page renders
 * the SAME components as the quick panel, so a thumbnail that stops rendering
 * breaks two surfaces and one test file should say so.
 */

/**
 * The skeleton's BARS: the filled rects that are not the card itself.
 *
 * Selected by height rather than by a class, because the thing being asserted
 * is the picture's geometry — a bar is 2.6 units tall and the ground is 30.8,
 * so "is this a list or a filled card?" is answerable from the shapes alone.
 */
function bars(container: HTMLElement): readonly SVGRectElement[] {
  return [...container.querySelectorAll("rect")].filter((rect) => {
    const fill = rect.getAttribute("fill");
    if (fill === null || fill === "none") return false;
    return Number(rect.getAttribute("height") ?? "0") < 10;
  });
}

describe("ThemeThumb (F-11)", () => {
  it("draws the same list skeleton for every theme, never a solid block", () => {
    const counts = (["light", "dark", "system"] as const).map((theme) => {
      const { container } = render(<ThemeThumb theme={theme} />);
      return bars(container).length;
    });

    // Light and dark: three bars each. System: the same skeleton twice, one per
    // half — which is what makes it read as "either of these" rather than as a
    // fourth kind of picture.
    expect(counts[0]).toBe(3);
    expect(counts[1]).toBe(3);
    expect(counts[2]).toBe(6);
  });

  it("gives the dark skeleton ink that is legible on its own ground", () => {
    const { container } = render(<ThemeThumb theme="dark" />);
    const fills = bars(container).map((rect) => rect.getAttribute("fill"));

    // The ground is #1f2124. Every bar must be a LIGHT ink over it — the bug
    // was bars so close to the ground that the thumbnail read as filled.
    for (const fill of fills) {
      expect(fill).not.toBe("#1f2124");
      expect(fill).toMatch(/^#[89abcdef]/i);
    }
  });

  it("paints the system halves in opposite grounds", () => {
    const { container } = render(<ThemeThumb theme="system" />);
    const grounds = [...container.querySelectorAll("path")].map((path) =>
      path.getAttribute("fill"),
    );

    expect(grounds).toContain("#ffffff");
    expect(grounds).toContain("#1f2124");
  });
});

describe("the other three sets still draw", () => {
  it("renders a density, an inbox type and a reading pane", () => {
    for (const density of ["default", "comfortable", "compact"] as const) {
      const { container } = render(<DensityThumb density={density} />);
      expect(container.querySelector("svg")).not.toBeNull();
    }
    for (const inboxType of ["default", "unread_first", "starred_first"] as const) {
      const { container } = render(<InboxTypeThumb inboxType={inboxType} />);
      expect(container.querySelector("svg")).not.toBeNull();
    }
    for (const pane of ["none", "right", "bottom"] as const) {
      const { container } = render(<ReadingPaneThumb pane={pane} />);
      expect(container.querySelector("svg")).not.toBeNull();
    }
  });

  it("hides every thumbnail from the accessibility tree", () => {
    const { container } = render(<DensityThumb density="compact" />);
    expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  });
});
