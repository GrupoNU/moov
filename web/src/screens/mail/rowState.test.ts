import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

/**
 * Pins the message row's STATE LAYER to the stylesheet (B-03, B-04, B-07).
 *
 * # Why static text rather than a render
 *
 * The same reason `paneGrid.test.ts` gives: jsdom performs no layout and no
 * cascade resolution worth trusting. `getComputedStyle` on a rendered row
 * returns the declared value of whichever rule the test happened to trigger,
 * not the one the browser would paint — so a rendered assertion passed all
 * along, for as long as each of these three defects shipped. The stylesheet is
 * the artefact that has to be right, so the stylesheet is what is asserted.
 *
 * # The three defects (side-by-side review, 2026-09-08)
 *
 *   - **B-03** the hovered row had no elevation: a `transition` on `box-shadow`
 *     was declared with no rule that ever set one, so the lift Gmail draws was
 *     an animation of nothing.
 *   - **B-04** the hover strip masked the date with a 1.5rem gradient instead of
 *     hiding it (leaving a stray character beside the icons) and anchored its
 *     right edge to a constant unrelated to the row's padding, which clipped
 *     the fourth icon at the denser row heights.
 *   - **B-07** every row was white, so read and unread differed only in font
 *     weight — Gmail tints the resting row and punches the unread one through
 *     it in white.
 */
describe("the message row's state layer", () => {
  const css = readFileSync(
    resolve(process.cwd(), "src/screens/mail/MessageList.module.css"),
    "utf8",
  );

  /** The body of the first rule whose selector is exactly `selector`. */
  const rule = (selector: string): string => {
    const escaped = selector.replace(/[.:]/g, "\\$&");
    const match = new RegExp(`(?:^|\\n)${escaped}\\s*\\{([^{}]*)\\}`).exec(css);
    expect(match, `no rule for ${selector}`).not.toBeNull();
    return match?.[1] ?? "";
  };

  it("tints the resting row and leaves unread on the default surface (B-07)", () => {
    expect(rule(".row")).toMatch(/background:\s*var\(--surface-sunken\)/);
    expect(rule(".unread")).toMatch(/background:\s*var\(--surface-default\)/);
  });

  it("keeps the unread contrast carried by weight as well as by surface (B-07)", () => {
    // Surface alone would be colour-only information. The weight rules predate
    // this change and must survive it.
    expect(css).toMatch(/\.unread \.senderText,\s*\n\.unread \.subject \{[^{}]*font-weight/);
  });

  it("lifts the hovered row with a shadow, a raised surface and a stacking context (B-03)", () => {
    const hover = rule(".row:hover");
    expect(hover).toMatch(/box-shadow:\s*var\(--shadow-sm\)/);
    // A DISTINCT surface from the resting tint, or hovering a read row would
    // change nothing at all — the collision B-07 created.
    expect(hover).toMatch(/background:\s*var\(--surface-raised\)/);
    // Without the stacking context the absolutely-positioned neighbours paint
    // over the blur and the shadow is invisible on three of its four sides.
    expect(hover).toMatch(/z-index:\s*[1-9]/);
  });

  it("declares the transition the lift animates (B-03)", () => {
    const row = rule(".row");
    expect(row).toContain("box-shadow var(--duration-fast)");
    expect(row).toContain("background var(--duration-fast)");
  });

  it("hides the date under hover instead of masking it (B-04)", () => {
    expect(rule(".row:hover .date")).toMatch(/visibility:\s*hidden/);
    // `display: none` would collapse the meta column and reflow the row under
    // the pointer, and would take the timestamp out of the accessibility tree.
    expect(rule(".row:hover .date")).not.toMatch(/display:\s*none/);
  });

  it("leaves no gradient mask behind the hover strip (B-04)", () => {
    // The mask is what produced the stray character: it can only ever be as
    // wide as it was authored, and dates are wider.
    expect(rule(".hoverActions")).not.toContain("linear-gradient");
    expect(css).not.toMatch(/\.(selected|checked) \.hoverActions \{[^{}]*linear-gradient/);
  });

  it("bounds the preview's measure rather than letting it fill the row (B-08)", () => {
    /*
     * `flex: 1` let the preview take every spare pixel, which on a 2560px
     * display put text within a few pixels of the date — roughly 500px past
     * where Gmail stops. The bound is in `ch` because it is about how many
     * characters the eye scans, which is what moves when density changes the
     * font; a px bound would hold only at the width it was measured at.
     */
    const preview = rule(".preview");
    expect(preview).toMatch(/max-width:\s*\d+ch/);
    expect(preview).not.toMatch(/flex:\s*1\s*;/);
  });

  it("aligns the hover strip to the row's own padding (B-04)", () => {
    // A constant inset clipped the fourth icon wherever `--row-padding-x` was
    // smaller than it — which is every density below "default".
    expect(rule(".hoverActions")).toMatch(
      /right:\s*var\(--row-padding-x,\s*var\(--row-padding-x-fallback\)\)/,
    );
  });
});
