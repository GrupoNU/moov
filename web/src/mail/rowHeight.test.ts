import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

import { ROW_HEIGHT } from "./windowing";

/**
 * Pins the windowing maths to the stylesheet.
 *
 * The virtualizer computes every offset from `ROW_HEIGHT`, while the browser
 * draws each row at the CSS `--row-height`. If the two ever disagree, nothing
 * throws and no test fails on its own — the list simply drifts away from its
 * scrollbar, a little more with every row, which is the kind of bug that gets
 * reported as "scrolling feels wrong" and takes a day to find.
 *
 * So the two are asserted equal here, in the one test that would fail the
 * moment someone tunes the row height in only one of the two places.
 */
describe("ROW_HEIGHT", () => {
  it("matches --row-height in MessageList.module.css", () => {
    // Resolved from the project root (Vitest's cwd) rather than from
    // `import.meta.url`, which is not a file: URL under the dev server's
    // module graph.
    const cssPath = resolve(
      process.cwd(),
      "src/screens/mail/MessageList.module.css",
    );
    const css = readFileSync(cssPath, "utf8");
    const match = /--row-height:\s*(\d+)px/.exec(css);

    expect(match, "--row-height is not declared in MessageList.module.css").not.toBeNull();
    expect(Number(match?.[1])).toBe(ROW_HEIGHT);
  });
});
