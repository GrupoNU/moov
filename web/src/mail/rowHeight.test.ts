import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

import { densityMetrics, DENSITIES } from "./prefs";
import { ROW_HEIGHT } from "./windowing";

/**
 * Pins the windowing maths to the stylesheet.
 *
 * The virtualizer computes every offset from a row height, while the browser
 * draws each row at the CSS `--row-height`. If the two ever disagree, nothing
 * throws and no test fails on its own — the list simply drifts away from its
 * scrollbar, a little more with every row, which is the kind of bug that gets
 * reported as "scrolling feels wrong" and takes a day to find.
 *
 * # What changed in E5, and what did not
 *
 * The height is now a function of the DENSITY preference, so there are three
 * of them and both sides read `densityMetrics()`. The stylesheet's literals
 * became `--row-height-fallback` and friends: the values the list draws with
 * for the moment BEFORE the preference has loaded. Those are the "default"
 * density's values, so the pre-load and post-load paints agree for the common
 * case — and that agreement is exactly what this file still pins.
 */
describe("the row geometry", () => {
  const css = readFileSync(
    // Resolved from the project root (Vitest's cwd) rather than from
    // `import.meta.url`, which is not a file: URL under the dev server's
    // module graph.
    resolve(process.cwd(), "src/screens/mail/MessageList.module.css"),
    "utf8",
  );

  const declared = (name: string): number | undefined => {
    const match = new RegExp(`${name}:\\s*(\\d+)px`).exec(css);
    return match === null ? undefined : Number(match[1]);
  };

  it("declares a CSS fallback equal to the default density's row height", () => {
    expect(declared("--row-height-fallback")).toBe(densityMetrics("default").rowHeight);
  });

  it("declares fallbacks for the padding and gap too", () => {
    expect(declared("--row-padding-x-fallback")).toBe(densityMetrics("default").rowPaddingX);
    expect(declared("--row-gap-fallback")).toBe(densityMetrics("default").rowGap);
  });

  it("declares fallbacks for the avatar and the action button (B-11)", () => {
    expect(declared("--row-avatar-fallback")).toBe(densityMetrics("default").avatarSize);
    expect(declared("--row-action-fallback")).toBe(densityMetrics("default").actionSize);
  });

  it("sizes the avatar and the actions from the density table, never a literal (B-11)", () => {
    /*
     * The regression this catches is the one B-11 had to fix: the avatar was a
     * fixed 34px, which was TALLER than the 32px compact row the Gmail-anchored
     * scale asks for. A literal here is a value that silently outgrows any
     * future tightening of the scale.
     */
    expect(css).toContain("var(--row-avatar, var(--row-avatar-fallback))");
    expect(css).toContain("var(--row-action, var(--row-action-fallback))");
    expect(css).not.toMatch(/\.avatar \{[^{}]*width:\s*\d+px/);
    expect(css).not.toMatch(/\.rowAction \{[^{}]*width:\s*[\d.]+rem/);
  });

  it("keeps every row taller than the tallest thing inside it (B-11)", () => {
    /*
     * The fit invariant. Compact is the binding case — a 32px row around a 24px
     * avatar — and this is what stops a future tightening from shipping a row
     * its own contents overflow, which is not a visual nit: the rows are
     * absolutely positioned at fixed offsets, so an overflowing avatar is
     * clipped by the next row rather than pushing it down.
     */
    for (const density of DENSITIES) {
      const { rowHeight, avatarSize, actionSize } = densityMetrics(density);
      expect(rowHeight).toBeGreaterThan(avatarSize);
      expect(rowHeight).toBeGreaterThan(actionSize);
      // And with room to breathe on both sides, not merely by a pixel.
      expect(rowHeight - Math.max(avatarSize, actionSize)).toBeGreaterThanOrEqual(8);
    }
  });

  it("anchors the scale on Gmail's row and keeps the steps visible (B-11)", () => {
    /*
     * The review measured Gmail's default row at ~40px and found ours at
     * COMPACT was 56 — the whole scale sat a step and a half above the product
     * it is benchmarked against, so a user arriving from Gmail saw a third less
     * mail per screen with no setting that would give it back.
     */
    expect(densityMetrics("default").rowHeight).toBe(40);
    // Monotonic, and each step big enough that the user can tell the control
    // did something: a 4px step is a setting that looks broken.
    const heights = ["compact", "default", "comfortable"].map(
      (d) => densityMetrics(d as (typeof DENSITIES)[number]).rowHeight,
    );
    for (let i = 1; i < heights.length; i += 1) {
      expect((heights[i] ?? 0) - (heights[i - 1] ?? 0)).toBeGreaterThanOrEqual(8);
    }
  });

  it("keeps the windowing module's own default in step with the default density", () => {
    // ROW_HEIGHT is still the default parameter of `computeWindow` and friends,
    // so a caller that passes no height must get the same geometry the default
    // density draws.
    expect(ROW_HEIGHT).toBe(densityMetrics("default").rowHeight);
  });

  it("reads every density's geometry as whole pixels", () => {
    // Fractional row heights make `scrollTop / rowHeight` land between rows and
    // reintroduce the drift this file exists to prevent.
    for (const density of DENSITIES) {
      const metrics = densityMetrics(density);
      expect(Number.isInteger(metrics.rowHeight)).toBe(true);
      expect(Number.isInteger(metrics.rowPaddingX)).toBe(true);
      expect(Number.isInteger(metrics.rowGap)).toBe(true);
    }
  });

  it("consumes the root variables with those fallbacks, never a bare literal", () => {
    /*
     * The regression this catches: someone tunes a row height back to a literal
     * in the CSS, and density silently stops moving the rows while the
     * virtualizer keeps dividing by the preference — the exact drift above.
     */
    expect(css).toContain("var(--row-height, var(--row-height-fallback))");
    expect(css).toContain("var(--row-padding-x, var(--row-padding-x-fallback))");
    expect(css).not.toMatch(/height:\s*var\(--row-height\)\s*;/);
  });
});
