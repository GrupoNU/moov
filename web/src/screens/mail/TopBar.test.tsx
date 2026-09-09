import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

/**
 * A-07/E-10 — the search box's width, asserted against the STYLESHEET.
 *
 * # Why a CSS test and not a render test
 *
 * jsdom applies no user-agent stylesheet, resolves no cascade and computes no
 * `clamp()`: `getComputedStyle` on the rendered bar returns the literal string
 * we wrote, or nothing at all. A render assertion here would pass with the
 * rule deleted, which makes it worse than no test — it would report a green
 * check over the exact regression it was written to catch.
 *
 * So the invariant is asserted where it is true: the three numbers Gmail's
 * geometry is built from must be in the file. The review measured Moov's box
 * at x215-640 (~425 px, flush against the wordmark) against Gmail's x250-810
 * (~560 px, a gap after the brand), and the fix is a ratio with a floor and a
 * ceiling rather than a fixed width.
 *
 * # What each number is pinned for
 *
 * - `40vw`  — the ratio. Delete it and the box stops growing with the screen.
 * - `720px` — the ceiling. Raise it and the pill crowds the right cluster.
 * - `420px` — the floor, and the reason the box shrinks LAST: the grid's brand
 *             track gives up its space before this minimum is breached.
 *
 * The ORDER inside `clamp(min, preferred, max)` is load-bearing and is checked
 * as one expression rather than three loose matches: `clamp(720px, 40vw,
 * 420px)` contains all three numbers and is nonsense.
 */

const css = readFileSync(
  resolve(process.cwd(), "src/screens/mail/TopBar.module.css"),
  "utf8",
);

/** The body of a rule, by its exact selector. `undefined` when absent. */
function ruleBody(selector: string): string | undefined {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`${escaped}\\s*\\{([\\s\\S]*?)\\}`).exec(css)?.[1];
}

describe("A-07/E-10: the search track's width", () => {
  it("clamps the box to Gmail's band — 40vw, floor 420px, ceiling 720px", () => {
    const rule = ruleBody(".search > *");
    expect(rule).toBeDefined();
    // One expression, so a transposed clamp cannot pass.
    expect(rule).toMatch(/width:\s*clamp\(\s*420px\s*,\s*40vw\s*,\s*720px\s*\)/);
  });

  it("lets the box shrink below its floor rather than overflow a narrow bar", () => {
    // Without this the 420px floor would push the pill under the right cluster
    // on a phone: `clamp` has no idea how much room the track actually has.
    expect(ruleBody(".search > *")).toMatch(/max-width:\s*100%/);
  });

  it("sets the box off from the wordmark instead of butting against it", () => {
    // The review's "búsqueda pegada al wordmark": the grid gap alone was not
    // read as a gap, because the brand does not fill its 244px track.
    expect(ruleBody(".search")).toMatch(/margin-left:\s*var\(--space-2\)/);
  });

  it("keeps the right cluster on an `auto` track, so it never yields first", () => {
    // Responsive order is the AC: the search box shrinks before help, gear and
    // avatar do. A cluster on `1fr` would compress instead.
    expect(ruleBody(".bar")).toMatch(/grid-template-columns:\s*244px\s+minmax\(0,\s*1fr\)\s+auto/);
  });

  it("does not let the pill re-impose a cap of its own", () => {
    // The 34rem (544px) cap in SearchBar.module.css is where the measured
    // ~425px came from: the bar could offer the track any width and the pill
    // would refuse it. Width is the surface's decision now.
    const searchBarCss = readFileSync(
      resolve(process.cwd(), "src/screens/mail/SearchBar.module.css"),
      "utf8",
    );
    const wrapper = /\.wrapper\s*\{([\s\S]*?)\}/.exec(searchBarCss)?.[1] ?? "";
    expect(wrapper).not.toMatch(/max-width/);
  });
});
