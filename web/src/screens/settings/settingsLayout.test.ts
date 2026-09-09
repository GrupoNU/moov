import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

import { en, es } from "../../i18n/strings";

/**
 * The settings surfaces' GEOMETRY, pinned in CSS (review F-06, F-19/20/47/48).
 *
 * # Why a stylesheet test and not a rendered one
 *
 * jsdom applies no stylesheet: `getComputedStyle` on a class from a CSS module
 * returns nothing, so a rendered test cannot see a width, a padding or a
 * max-width at all. These numbers are exactly what the side-by-side review
 * measured and exactly what a careless refactor silently reverts, so they are
 * asserted against the source text — the same approach `styles/a11y.test.ts`
 * and `styles/overlayScrim.test.ts` already take for rules jsdom cannot see.
 *
 * What each number MEANS is in the stylesheet's own comments; what is here is
 * only "this is still the number the review asked for".
 */

const read = (path: string): string =>
  readFileSync(resolve(process.cwd(), "src", path), "utf8");

describe("the quick-settings dock (F-06)", () => {
  it("is 22rem wide, not the 19rem the review measured against Gmail's ~365px", () => {
    const css = read("screens/mail/MailScreen.module.css");
    expect(css).toMatch(/--quick-width:\s*22rem/);
    // The old value must be gone from the declaration, not merely overridden
    // somewhere later — two declarations of one track width is how a layout
    // ends up depending on rule order.
    expect(css).not.toMatch(/--quick-width:\s*19rem/);
  });
});

describe("the settings page rows (F-19, F-20, F-48)", () => {
  const css = read("screens/settings/SettingsPage.module.css");

  it("gives the row label the weight Gmail gives it", () => {
    const rule = /\.rowLabel\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
    expect(rule).toMatch(/font-weight:\s*var\(--weight-semibold\)/);
  });

  it("caps the rows container so they do not stretch across a wide monitor", () => {
    const rule = /\.rows\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
    expect(rule).toMatch(/max-width:\s*1150px/);
    // Left-aligned, like Gmail: capped AND centred would float the rows away
    // from the tab that selected them.
    expect(rule).toMatch(/margin-right:\s*auto/);
    expect(rule).not.toMatch(/margin-left:\s*auto/);
  });

  it("tightens the row's vertical padding so more than ten rows fit a screen", () => {
    const rule = /\.row\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
    // space-3, not space-4: the review counted 10 of our rows against Gmail's
    // 19 at the same viewport, with ~450px of dead white below them.
    expect(rule).toMatch(/padding:\s*var\(--space-3\)\s+0/);
  });

  it("holds the description to one line in the two-column layout", () => {
    const rule = /\.rowDescription\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
    expect(rule).toMatch(/white-space:\s*nowrap/);
    expect(rule).toMatch(/text-overflow:\s*ellipsis/);
    // …and lets it wrap again once the row stacks, where it has the width.
    expect(css).toMatch(/@container settings \(max-width: 48rem\)[\s\S]*?\.rowDescription/);
  });
});

/**
 * F-08: one word per concept, in both locales.
 *
 * The quick panel stacks Densidad above Tipo de bandeja, and their first
 * options meant the same thing in two words — "Normal" and "Predeterminada".
 * A user reading them one under the other has to wonder what the difference
 * is, and there is none.
 */
describe("the default option's name (F-08)", () => {
  it("says the same word for density and inbox type in each locale", () => {
    expect(es["settings.density.default"]).toBe(es["settings.inboxType.default"]);
    expect(en["settings.density.default"]).toBe(en["settings.inboxType.default"]);
  });

  it("is the Spanish word the rest of the panel uses", () => {
    expect(es["settings.density.default"]).toBe("Predeterminada");
  });
});
