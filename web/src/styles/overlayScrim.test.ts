import { readFileSync, readdirSync } from "node:fs";
import { join, resolve } from "node:path";

import { describe, expect, it } from "vitest";

/**
 * `--surface-overlay` is the modal SCRIM, not a surface to draw on.
 *
 * # The bug this exists to prevent (P0-2, 2026-09-08)
 *
 * Three popup menus — move-to, the action bar's overflow, and the reader's
 * "show original" dialog — used the scrim token as their own background. It is
 * `rgba(…, 0.45)`, so the message rows underneath read straight through the
 * menu: unreadable, and it looked like a rendering fault rather than a wrong
 * token. The name is what invites the mistake ("overlay" sounds like the thing
 * on top), which is why the rule is pinned here instead of being left to
 * review.
 *
 * The one legitimate use is a `::backdrop` rule, where the scrim IS the point.
 */
describe("the overlay scrim token", () => {
  const root = resolve(process.cwd(), "src");

  const cssFiles = (dir: string): string[] =>
    readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
      const path = join(dir, entry.name);
      if (entry.isDirectory()) return cssFiles(path);
      return entry.name.endsWith(".module.css") ? [path] : [];
    });

  it("is used by no module stylesheet outside a ::backdrop rule", () => {
    const offenders: string[] = [];

    for (const file of cssFiles(root)) {
      const css = readFileSync(file, "utf8");
      // Each rule as `selector { body }`; a use of the token in a body whose
      // selector is not a ::backdrop is an offence.
      for (const match of css.matchAll(/([^{}]*)\{([^{}]*)\}/g)) {
        const selector = match[1] ?? "";
        const body = match[2] ?? "";
        if (!body.includes("--surface-overlay")) continue;
        if (selector.includes("::backdrop")) continue;
        offenders.push(`${file}: ${selector.trim()}`);
      }
    }

    expect(offenders).toEqual([]);
  });

  it("still declares the token, for the backdrops that do want it", () => {
    const tokens = readFileSync(join(root, "styles", "tokens.css"), "utf8");
    expect(tokens).toMatch(/--surface-overlay:\s*rgba\(/);
  });
});
