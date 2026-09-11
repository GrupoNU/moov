import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

/**
 * Pins the reader's pinned footer: the reply verbs stay on screen, the message
 * scrolls behind them.
 *
 * # What this exists to prevent
 *
 * The owner's Gmail screenshot (reading pane right, 2026-09-10) caught it: in
 * Gmail the "Responder / Reenviar" row is a fixed strip at the FOOT of the
 * reading pane, with a hairline above it and the body scrolling behind. Ours
 * sat at the end of the content — in a twenty-message thread, the way to reply
 * was to scroll past all twenty — and in the single-message reader they had
 * drifted to the TOP, under the subject, which is the one place Gmail never
 * puts them.
 *
 * Three things had to be true at once and none of them is visible to a render
 * test: the pane must stop being the scrollport, the BODY must become one, and
 * the verbs must be pinned outside it.
 *
 * # Why static text and not a render
 *
 * jsdom performs no layout and resolves no custom properties, so a rendered
 * assertion cannot tell a pinned footer from a block at the end of the
 * content — `getComputedStyle` would hand back the same nothing for both. The
 * stylesheet is the artefact that has to be right. Same reasoning, and the
 * same `block` helper, as `canvasSurfaces.test.ts`, `paneGrid.test.ts` and
 * `rowHeight.test.ts` — three fixes this project shipped that were invisible
 * to jsdom and visible immediately in a browser.
 */
const read = (file: string): string =>
  readFileSync(resolve(process.cwd(), "src/screens/mail", file), "utf8");

/** The declaration block of the rule whose selector is exactly `selector`. */
const block = (css: string, selector: string): string => {
  for (let at = css.indexOf(selector); at !== -1; at = css.indexOf(selector, at + 1)) {
    const before = css.slice(0, at);
    const lineStart = before.lastIndexOf("\n") + 1;
    // The selector must own its line, so `.actions` never matches
    // `.actionsRow`'s line or a descendant selector that merely mentions it.
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

describe("the reader scrolls its BODY, not the whole pane", () => {
  const pane = read("ReadingPane.module.css");

  it("stops the pane from being the scrollport", () => {
    /*
     * This is the half that is easy to lose. Leaving `overflow-y: auto` here
     * as well would give the pane a second scrollport, and the footer would
     * scroll away inside it exactly as it used to — with everything else in
     * this file still passing.
     */
    expect(block(pane, ".pane")).not.toContain("overflow-y: auto");
  });

  it("makes the body region the scrollport instead", () => {
    expect(block(pane, ".bodyRegion")).toContain("overflow-y: auto;");
  });

  it("keeps the body able to shrink, or it would never scroll at all", () => {
    // A flex child with the default `min-height: auto` grows to its content
    // and the overflow above never engages.
    expect(block(pane, ".bodyRegion")).toContain("min-height: 0;");
  });
});


describe("the pinned reply strip, ONE row for both readers", () => {
  const row = block(read("ReplyRow.module.css"), ".row");

  /*
   * The fix the owner's SECOND screenshot forced. The conversation's row was
   * `position: sticky; bottom: 0` inside the scrolling column, and it could
   * not reach the bottom: sticky pins against the scrollport's PADDING edge,
   * and that scrollport has a bottom padding, so the strip stopped short and
   * the message scrolled through the gap underneath it.
   *
   * As a real last flex child there is no `bottom` to get wrong.
   */
  it("does not grow or shrink — it IS the last item of the pane's column", () => {
    expect(row).toContain("flex: none;");
  });

  it("is not positioned at all — no sticky, no fixed, no coordinates", () => {
    // The whole point. A `bottom` anywhere in this rule means someone put the
    // coordinate back, and a coordinate is what missed the edge.
    expect(row).not.toContain("position:");
    expect(row).not.toContain("bottom:");
  });

  it("draws Gmail's hairline between the message and the verbs", () => {
    expect(row).toContain("border-top: 1px solid var(--border-subtle);");
  });

  it("is opaque, so neither the mail nor the canvas shows through it", () => {
    expect(row).toContain("background: var(--surface-default);");
  });

  it("carries no negative margin — the trick that did not work", () => {
    /*
     * The first attempt pulled the sticky row out over the scrollport's
     * padding with negative margins. It moved the box and not the edge sticky
     * pins to, so the gap stayed. Its fingerprint must not come back.
     */
    expect(row).not.toMatch(/margin[^;]*calc\(-1/);
  });

  it("hides itself from print, where the verbs are not part of the message", () => {
    const css = read("ReplyRow.module.css");
    const print = css.slice(css.indexOf("@media print"));
    expect(print).toContain(".row");
    expect(print).toContain("display: none");
  });
});

describe("the old arrangements are gone, not merely unused", () => {
  it("leaves no sticky row in the conversation's stylesheet", () => {
    /*
     * The defect was `position: sticky` on a row inside the scroller. What is
     * checked is a DECLARATION — a line that ends in a semicolon — because the
     * tombstone comment explaining the removal names the property too, and a
     * substring search would match the explanation and never the mistake.
     */
    const declarations = read("ConversationView.module.css")
      .split("\n")
      .filter((line) => !line.trimStart().startsWith("*"))
      .join("\n");
    expect(declarations).not.toMatch(/position:\s*sticky\s*;/);
  });

  it("leaves no `.actions` rule in the reader's stylesheet", () => {
    /*
     * The single-message reader's own row, which was a header row under the
     * subject before it was a footer. Both readers share `ReplyRow` now, and a
     * second rule for the same strip is how two strips end up disagreeing.
     */
    const pane = read("ReadingPane.module.css");
    expect(pane).not.toMatch(/^\.actions\s*\{/m);
  });
});
