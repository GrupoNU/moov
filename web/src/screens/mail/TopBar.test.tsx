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

/**
 * The settings gear's geometry (owner: the old one "looks deformed",
 * 2026-09-11).
 *
 * # What this exists to prevent
 *
 * The gear was drawn by hand as one long relative path and its teeth were
 * genuinely uneven — different widths, different reaches, and a hub that was
 * not concentric with the ring. At 18px that reads as a smudge. The
 * replacement is Material Symbols "settings" geometry, generated rather than
 * drawn.
 *
 * # Why the numbers and not a snapshot
 *
 * A snapshot of the `d` string would fail on any edit, including a correct one,
 * and would say nothing about WHY it failed. What has to be true is a
 * geometric fact — eight identical teeth around one centre — so that is what
 * is measured: every vertex is parsed out of the path and its distance from
 * the centre is computed. A gear whose teeth drift apart again fails here with
 * the drift named, whatever the string looks like.
 */
describe("the settings gear", () => {
  const source = readFileSync(resolve(process.cwd(), "src/screens/mail/TopBar.tsx"), "utf8");

  /** The gear's tooth path — the one inside the `styles.gear` svg. */
  const gearPath = ((): string => {
    const svg = source.slice(source.indexOf("className={styles.gear}"));
    const match = /<path d="([^"]+)"/.exec(svg.slice(0, svg.indexOf("</svg>")));
    if (match?.[1] === undefined) expect.fail("no path inside the gear svg");
    return match[1];
  })();

  /** Every absolute vertex of the path, as [x, y]. */
  const vertices = [...gearPath.matchAll(/[ML](-?[\d.]+) (-?[\d.]+)/g)].map(
    (m) => [Number(m[1]), Number(m[2])] as const,
  );

  const CENTRE = 10;
  const radius = ([x, y]: readonly [number, number]): number =>
    Math.round(Math.hypot(x - CENTRE, y - CENTRE) * 100) / 100;

  it("has 16 vertices — eight teeth, each a tip and a root", () => {
    expect(vertices).toHaveLength(16);
  });

  it("puts every TIP at exactly the same distance from the centre", () => {
    // The defect, stated as a measurement: uneven teeth are tips at unequal
    // radii, and no amount of looking at the string reveals that.
    const tips = vertices.filter((_, k) => k % 2 === 0).map(radius);
    expect(new Set(tips).size).toBe(1);
  });

  it("puts every ROOT at exactly the same distance too", () => {
    const roots = vertices.filter((_, k) => k % 2 === 1).map(radius);
    expect(new Set(roots).size).toBe(1);
  });

  it("makes the teeth actually stick out", () => {
    const tip = radius(vertices[0]!);
    const root = radius(vertices[1]!);
    expect(tip).toBeGreaterThan(root);
  });

  it("spaces the vertices evenly — one every 22.5°", () => {
    /*
     * Equal radii alone would still allow eight teeth bunched to one side.
     * The angles are what make them a gear rather than a crown.
     */
    const angles = vertices.map(([x, y]) =>
      Math.round(((Math.atan2(y - CENTRE, x - CENTRE) * 180) / Math.PI + 360) % 360),
    );
    const sorted = [...angles].sort((a, b) => a - b);
    const gaps = sorted.map((angle, k) =>
      k === 0 ? angle + 360 - sorted[sorted.length - 1]! : angle - sorted[k - 1]!,
    );
    // 22.5° rounds to 22 or 23 depending on where the vertex falls.
    for (const gap of gaps) expect(gap).toBeGreaterThanOrEqual(22);
    for (const gap of gaps) expect(gap).toBeLessThanOrEqual(23);
  });

  it("closes the path, so the ring is a ring", () => {
    expect(gearPath.trimEnd().endsWith("Z")).toBe(true);
  });

  it("centres the hub on the same point the teeth are arranged around", () => {
    // The old gear's hub and ring were not concentric. `cx`/`cy` must be the
    // centre the radii above were measured from.
    const svg = source.slice(source.indexOf("className={styles.gear}"));
    const circle = /<circle cx="([\d.]+)" cy="([\d.]+)" r="([\d.]+)"/.exec(
      svg.slice(0, svg.indexOf("</svg>")),
    );
    if (circle === null) expect.fail("the gear has no hub");
    expect(Number(circle[1])).toBe(CENTRE);
    expect(Number(circle[2])).toBe(CENTRE);
    // And the hub must sit inside the root circle, or it would cut the teeth.
    expect(Number(circle[3])).toBeLessThan(radius(vertices[1]!));
  });

  it("carries the same stroke weight as the help icon beside it", () => {
    /*
     * The gear was 1.5 while its neighbour was 1.6, which made it read a shade
     * lighter than the icon next to it — the kind of difference nobody reports
     * and everybody sees.
     */
    const weights = [...source.matchAll(/strokeWidth="([\d.]+)"/g)].map((m) => m[1]);
    const gearSvg = source.slice(source.indexOf("className={styles.gear}"));
    const gearWeight = /strokeWidth="([\d.]+)"/.exec(gearSvg)?.[1];
    expect(gearWeight).toBe("1.6");
    expect(weights).toContain("1.6");
  });
});
