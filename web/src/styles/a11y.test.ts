import { readdirSync, readFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/** Every stylesheet under a directory, recursively. */
function cssFilesIn(dir: string): string[] {
  const found: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) found.push(...cssFilesIn(path));
    else if (entry.name.endsWith(".css")) found.push(path);
  }
  return found;
}

/**
 * The two named accessibility gaps of E11, pinned as invariants.
 *
 * These read the stylesheets as text rather than rendering them, because jsdom
 * does not implement the cascade, `prefers-reduced-motion`, or `:focus-visible`
 * — a rendering test would be asserting on the stub. What CAN be checked
 * mechanically is that the rules exist and that nobody has quietly opted a
 * component out of them, which is exactly how both gaps appear in practice.
 */

const STYLES = join(process.cwd(), "src/styles");

describe("prefers-reduced-motion", () => {
  it("kills animations outright rather than shortening them", async () => {
    const css = await readFile(join(STYLES, "base.css"), "utf8");
    const block = css.slice(css.indexOf("@media (prefers-reduced-motion: reduce)"));
    expect(block).not.toBe("");

    /*
     * The widely-copied recipe sets `animation-duration: 0.01ms` with
     * `iteration-count: 1`, which is WRONG for an infinite spinner: it leaves
     * the element parked at the animation's END state — a rotation nobody
     * asked for — and still schedules work. `animation: none` is what gives a
     * spinner its static fallback.
     */
    expect(block).toMatch(/animation:\s*none\s*!important/);
    expect(block).not.toMatch(/animation-duration:\s*0\.01ms/);
  });

  it("keeps transitions near-zero rather than none, so transitionend still fires", async () => {
    // A state change that waits for a transition to END must still complete,
    // or a reduced-motion user gets a dialog that never finishes opening.
    const css = await readFile(join(STYLES, "base.css"), "utf8");
    const block = css.slice(css.indexOf("@media (prefers-reduced-motion: reduce)"));
    expect(block).toMatch(/transition-duration:\s*0\.01ms\s*!important/);
    expect(block).not.toMatch(/transition:\s*none/);
  });
});

describe("the focus ring", () => {
  it("is defined once, as tokens", async () => {
    const tokens = await readFile(join(STYLES, "tokens.css"), "utf8");
    for (const token of ["--focus-ring-color", "--focus-ring-width", "--focus-ring-offset"]) {
      expect(tokens, token).toContain(token);
    }
  });

  it("has a global :focus-visible rule, so a new element is covered by default", async () => {
    const css = await readFile(join(STYLES, "base.css"), "utf8");
    expect(css).toMatch(/:focus-visible\s*\{[^}]*outline:\s*var\(--focus-ring-width\)/);
  });

  /*
   * The drift this catches: E11 found nine components painting their own ring
   * as a literal `2px solid var(--color-accent)` — and one referencing a
   * `--focus-ring` token that does not exist, so its ring fell back to plain
   * text colour. A ring that differs per component is not readable as "this is
   * where focus is".
   */
  it("is never hand-rolled: every focus outline goes through the token", () => {
    const offenders: string[] = [];
    for (const file of cssFilesIn(join(process.cwd(), "src"))) {
      const lines = readFileSync(file, "utf8").split("\n");
      /*
       * Inside `forced-colors: active` the whole point is to use SYSTEM
       * colours (Highlight, CanvasText): the OS palette replaces ours, and a
       * ring painted with our token would be one of the colours the user's
       * high-contrast mode is explicitly overriding. Those rules are exempt,
       * and the exemption is scoped to that at-rule rather than global.
       */
      let inForcedColors = false;
      let depth = 0;
      lines.forEach((line, index) => {
        if (line.includes("@media") && line.includes("forced-colors")) {
          inForcedColors = true;
          depth = 0;
        }
        if (inForcedColors) {
          depth += (line.match(/\{/g) ?? []).length - (line.match(/\}/g) ?? []).length;
          if (depth <= 0 && line.includes("}")) inForcedColors = false;
        }

        const match = /^\s*outline:\s*(.+);/.exec(line);
        if (match === null) return;
        const value = match[1] ?? "";
        // `outline: none` is legal when a container paints the ring instead —
        // both current uses delegate to a `:focus-within` on the wrapper.
        if (value === "none") return;
        if (value.includes("var(--focus-ring-width)")) return;
        if (inForcedColors) return;
        offenders.push(`${file}:${index + 1} → ${value}`);
      });
    }
    expect(offenders).toEqual([]);
  });
});
