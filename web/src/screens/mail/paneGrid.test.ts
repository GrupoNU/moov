import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

/**
 * Pins the reading-pane grid's placement rules to the stylesheet.
 *
 * # The bug this exists to prevent (P0-1, 2026-09-08)
 *
 * `.dividerCell { grid-area: divider }` was declared unscoped. The "below the
 * inbox" layout defines that area; the "right of the list" layout defines no
 * areas at all — and an unresolvable area name does not get ignored, it makes
 * the browser mint implicit tracks for the orphan line names. The reader slid
 * into the divider's `auto` track and the list, still mounted, collapsed to
 * `1fr` of nothing: the live grid computed `260 | 0 | 1771 | 520 | 0 | 9`,
 * six tracks instead of four, and opening a message covered the list.
 *
 * # Why this test is static text and not a render
 *
 * jsdom performs no layout: `getComputedStyle` returns the declared value, not
 * a resolved grid, so a rendered assertion would have passed all along — it
 * did, for as long as the bug shipped. The stylesheet is the artefact that has
 * to be right, so the stylesheet is what is asserted, the same way
 * `rowHeight.test.ts` asserts the list's CSS consumes its density variables.
 */
describe("the reading-pane grid", () => {
  const css = readFileSync(
    resolve(process.cwd(), "src/screens/mail/MailScreen.module.css"),
    "utf8",
  );

  it("scopes the divider's named area to the layout that declares it", () => {
    expect(css).toContain(".readingBottom .dividerCell {");
    // The orphan: `grid-area: divider` must never be reachable from a selector
    // that does not also constrain the layout to `.readingBottom`.
    const areaRules = [...css.matchAll(/([^{}]*)\{[^{}]*grid-area:\s*divider[^{}]*\}/g)];
    expect(areaRules.length).toBeGreaterThan(0);
    for (const [, selector] of areaRules) {
      expect(selector).toContain(".readingBottom");
    }
  });

  it("places all three cells of the right-hand split by explicit column", () => {
    // Sibling order then cannot decide where a pane lands, which is the class
    // of bug the orphan area belonged to — not just that one instance.
    const column = (selector: string): string | undefined => {
      const match = new RegExp(
        `\\${selector}\\s*\\{[^{}]*grid-column:\\s*([^;]+);`,
      ).exec(css);
      return match?.[1]?.trim();
    };
    expect(column(".reading .listColumn")).toBe("2");
    expect(column(".reading .dividerCell")).toBe("3");
    expect(column(".reading .readerColumn")).toBe("4");
  });

  it("re-places the reader at every breakpoint that drops tracks", () => {
    /*
     * Below 1100px the split is two tracks and below 860px it is one, so a
     * reader still pinned to column 4 would mint the same implicit tracks the
     * explicit placement exists to prevent. Both narrow blocks must therefore
     * say where the reader goes.
     */
    // Each breakpoint appears more than once (the quick-settings dock has its
    // own block), so every block at that width is collected and the rule need
    // only be in one of them.
    const blocks = (width: number): string[] =>
      [...css.matchAll(new RegExp(`@media \\(max-width: ${width}px\\) \\{([\\s\\S]*?)\\n\\}`, "g"))].map(
        (match) => match[1] ?? "",
      );
    const places = (width: number, column: number): boolean =>
      blocks(width).some((block) =>
        new RegExp(`\\.reading \\.readerColumn \\{[^{}]*grid-column:\\s*${column}`).test(block),
      );

    expect(places(1100, 2)).toBe(true);
    expect(places(860, 1)).toBe(true);
  });
});
