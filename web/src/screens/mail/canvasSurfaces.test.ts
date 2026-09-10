import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

/**
 * Pins Gmail's surface split: chrome on the canvas, panes as white cards.
 *
 * # What this exists to prevent
 *
 * The rail and the message list both painted `--surface-default`, so the only
 * thing between them was a 1px hairline and the rail read as the list's left
 * margin rather than as a separate surface. Gmail paints one light grey behind
 * the top bar, the rail and the gutter, and floats the list and the reader on
 * it as white cards with rounded top corners - that grey is what separates the
 * two areas and what lifts the rail's text off a ground of its own.
 *
 * # Why static text and not a render
 *
 * jsdom performs no layout and does not resolve custom properties:
 * `getComputedStyle` hands back `var(--surface-canvas)` as a literal string,
 * so a rendered assertion could not tell the two surfaces apart at all. The
 * stylesheet is the artefact that has to be right - the same reasoning as
 * `paneGrid.test.ts` and `rowHeight.test.ts`.
 */
const read = (file: string): string =>
  readFileSync(resolve(process.cwd(), "src/screens/mail", file), "utf8");

/**
 * The declaration block of the rule whose selector is exactly `selector`.
 *
 * Scanned rather than matched with a regex: every selector here starts with a
 * `.` or contains a `:`, and building a pattern out of them costs more escaping
 * than the scan costs code.
 */
const block = (css: string, selector: string): string => {
  for (let at = css.indexOf(selector); at !== -1; at = css.indexOf(selector, at + 1)) {
    const before = css.slice(0, at);
    const lineStart = before.lastIndexOf("\n") + 1;
    // The selector must own its line (so `.row` never matches `.rowActive`'s
    // line or a descendant selector that merely mentions it).
    if (before.slice(lineStart).trim() !== "") continue;
    const rest = css.slice(at + selector.length);
    const open = rest.indexOf("{");
    const close = rest.indexOf("}");
    if (open === -1 || (close !== -1 && close < open)) continue;
    if (rest.slice(0, open).trim() !== "") continue;
    return rest.slice(open + 1, rest.indexOf("}", open));
  }
  expect.fail(`no rule for \`${selector}\``);
};

describe("the application canvas", () => {
  const shell = read("MailScreen.module.css");
  const topBar = read("TopBar.module.css");

  it("paints the left rail on the canvas, not on the list's white", () => {
    expect(block(shell, ".sidebar")).toContain("background: var(--surface-canvas);");
  });

  it("paints the top bar on the same canvas as the rail", () => {
    expect(block(topBar, ".bar")).toContain("background: var(--surface-canvas);");
  });

  it("drops the rail's right border - the tone change is the separation", () => {
    // Gmail has none. A hairline on top of a tone change reads as a seam in a
    // surface that is meant to be continuous with the top bar.
    expect(block(shell, ".sidebar")).not.toContain("border-right:");
  });

  it("keeps the list and the reader white", () => {
    expect(block(read("MessageList.module.css"), ".container")).toContain(
      "background: var(--surface-default);",
    );
    expect(block(read("ReadingPane.module.css"), ".pane")).toContain(
      "background: var(--surface-default);",
    );
  });
});

describe("the panes as cards on the canvas", () => {
  const shell = read("MailScreen.module.css");

  it.each([".listColumn", ".readerColumn", ".settingsColumn"])(
    "rounds only %s's TOP corners",
    (selector) => {
      // Square at the bottom: the card runs to the bottom of the viewport, so
      // rounding there would open a sliver of grey under an edge that is never
      // drawn.
      expect(block(shell, selector)).toContain(
        "border-radius: var(--radius-lg) var(--radius-lg) 0 0;",
      );
    },
  );

  it("leaves a canvas gutter beside the rail in every layout", () => {
    // The gutter is a margin on the CELL, never a grid `column-gap`: the rail's
    // `--rail-width`, the resizable `--reader-width` and the divider are all
    // fixed numbers, and a gap would take its 8px out of them.
    expect(block(shell, ".listColumn")).toContain("margin-left: var(--space-2);");
    expect(block(shell, ".settingsColumn")).toContain("margin-left: var(--space-2);");
    // "No split" and "below" put the reader in the list's cell, so it inherits
    // the gutter there; in the right-hand split the divider already supplies it.
    expect(shell).toContain(".readingFull .readerColumn,");
    expect(shell).not.toContain("column-gap:");
  });

  it("withdraws the gutter and the corners with the rail on a phone", () => {
    // Below 860px the rail is `display: none`, so there is no canvas beside the
    // pane: an 8px margin would be a grey stripe down a phone and the corners
    // two grey wedges above a pane with nothing over it.
    const phone = shell.slice(shell.lastIndexOf("@media (max-width: 860px)"));
    expect(phone).toContain(".body .listColumn,");
    expect(phone).toContain("margin-left: 0;");
    expect(phone).toContain("border-radius: 0;");
  });
});

describe("chrome affordances on the canvas", () => {
  it.each([
    ["MailboxList.module.css", ".row"],
    ["LabelList.module.css", ".row"],
    ["LabelList.module.css", ".create"],
    ["TopBar.module.css", ".iconButton"],
  ])("mixes %s's %s:hover over the canvas, not over a second surface", (file, selector) => {
    /*
     * `--surface-sunken` is #eef0f6 against a #f6f7fb canvas - about three per
     * cent of lightness, where against the old white ground it was a clear step
     * down. Mixing a fixed amount of ink into whatever the canvas currently is
     * keeps the step the same size in both themes and under any brand colour,
     * which two independently-moving surface tokens cannot.
     */
    const css = read(file);
    expect(block(css, `${selector}:hover`)).toMatch(
      /background: color-mix[(]in srgb, #(000|fff) [0-9]+%, var[(]--surface-canvas[)][)];/,
    );
    // The dark theme flips to white ink - more black on a near-black canvas is
    // invisible - under BOTH dark selectors: the media query for the system
    // setting and the attribute for an explicit choice.
    const dark = css.slice(css.indexOf("prefers-color-scheme: dark"));
    expect(dark).toContain(`${selector}:hover`);
    expect(css.slice(css.indexOf('[data-theme="dark"]'))).toContain(`${selector}:hover`);
  });
});

describe("the search box as a well in the chrome", () => {
  const css = read("SearchBar.module.css");

  it("is a step DOWN from the canvas the bar now paints", () => {
    // It used to BE `--surface-canvas`, which on a canvas-coloured bar is the
    // same colour: only the hairline was left to say a control was there.
    expect(block(css, ".wrapper")).toMatch(
      /background: color-mix[(]in srgb, #000 [0-9]+%, var[(]--surface-canvas[)][)];/,
    );
    expect(block(css, ".wrapper")).not.toContain("background: var(--surface-canvas);");
  });

  it("still turns white on focus in the dark theme", () => {
    /*
     * A `:root[data-theme]` prefix outweighs the bare `.wrapper:focus-within`
     * rule, so the dark theme's resting well has to exclude the focused state
     * explicitly or it would win and the focused box would stay grey — the one
     * state where "this control is live" must be unmistakable.
     */
    expect(css).toContain(':root[data-theme="dark"] .wrapper:not(:focus-within)');
    expect(block(css, ".wrapper:focus-within")).toContain(
      "background: var(--surface-default);",
    );
  });
});
