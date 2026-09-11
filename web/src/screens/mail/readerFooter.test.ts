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

describe("the single-message reader's pinned footer", () => {
  const pane = read("ReadingPane.module.css");
  const actions = block(pane, ".actions");

  it("does not grow or shrink — it is the last item of the pane's column", () => {
    /*
     * `flex: none` rather than `position: fixed`: the row genuinely IS the
     * last child of a flex column that fills the pane, so it lands at the
     * bottom with no coordinates to keep in sync and no chance of overlapping
     * the message above it.
     */
    expect(actions).toContain("flex: none;");
  });

  it("draws Gmail's hairline between the message and the verbs", () => {
    expect(actions).toContain("border-top: 1px solid var(--border-subtle);");
  });

  it("is opaque, so the message cannot show through it", () => {
    expect(actions).toContain("background: var(--surface-default);");
  });

  it("is not hung off the top of the header any more", () => {
    // It used to be `margin-top`, under the subject. A margin-top on a pinned
    // footer would be the fingerprint of that arrangement coming back.
    expect(actions).not.toContain("margin-top:");
  });
});

describe("the conversation's pinned pill row", () => {
  const conversation = read("ConversationView.module.css");
  const replyRow = block(conversation, ".replyRow");

  /*
   * Sticky rather than the flex footer next door, and the difference is not a
   * style choice: these pills act on the NEWEST message and need the thread's
   * membership, both of which are `ConversationView`'s state — and that
   * component lives INSIDE the scroller. Lifting them to `ReadingPane` to
   * make the row a flex sibling would put a fetch-owning reducer into the
   * pane, which is exactly what `ConversationView` exists to avoid.
   */
  it("sticks to the bottom edge of the scrollport", () => {
    expect(replyRow).toContain("position: sticky;");
    expect(replyRow).toContain("bottom: 0;");
  });

  it("draws the same hairline above it as the single-message footer", () => {
    expect(replyRow).toContain("border-top: 1px solid var(--border-subtle);");
  });

  it("is opaque — a transparent sticky row shows the mail through itself", () => {
    // The load-bearing one. A sticky element with no background of its own
    // floats over the message with the text legible straight through it.
    expect(replyRow).toContain("background: var(--surface-default);");
  });

  it("stacks above the message it floats over", () => {
    expect(replyRow).toContain("z-index:");
  });

  it("is hidden for print, where there is nothing to stick to", () => {
    // `ReadingPane.module.css` already drops its own `.actions` for print;
    // this is the conversation's half, which was not needed while the row was
    // an ordinary block and is now that it is positioned.
    const print = conversation.slice(conversation.indexOf("@media print"));
    expect(print).toContain(".replyRow");
    expect(print).toContain("display: none");
  });
});
