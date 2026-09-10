import { readFileSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import { AA_NORMAL_TEXT, contrastRatio, parseHex } from "../mail/labelPalette";
import { MOOV_DEFAULT_BRANDING } from "./branding";
import {
  compositeOver,
  deltaE,
  derivePalette,
  deriveSplashColors,
  hexToOklch,
  MIN_DELTA_CONTAINER_PILL,
  MIN_DELTA_PILL_ROW,
  normalizeHex,
  oklchToHex,
  ON_ACCENT_DARK,
  ON_ACCENT_LIGHT,
  SPLASH_FROM_MIX_TOWARD_BLACK,
  SPLASH_TO_MIX_TOWARD_BLACK,
  THEME_SURFACES,
  type BrandPalette,
  type ThemeName,
} from "./palette";

/**
 * The contrast guarantee is a TEST, not a claim (the labelPalette precedent).
 *
 * Every constraint the module's header states is asserted here over a sweep
 * wide enough that a customer cannot find a hex that slips through: every hue
 * at 15° steps, at several lightness and chroma levels, plus the corners of
 * the sRGB cube, greys, and the Moov default.
 */

const THEMES: readonly ThemeName[] = ["light", "dark"];

/** The sweep: 24 hues × 5 lightness × 3 chroma, plus the named edge cases. */
function sweep(): readonly string[] {
  const inputs = new Set<string>([
    "#000000",
    "#ffffff",
    "#ffff00",
    "#00ffff",
    "#ff0000",
    "#00ff00",
    "#0000ff",
    "#ff00ff",
    "#808080",
    "#5b5bd6",
    "#c0ffee",
    // The pastel that motivated the tonal family: adjusted to a deep teal for
    // TEXT, it made every derived tint teal too and the brand's light cyan
    // disappeared from the chrome.
    "#b8faff",
    "#1e1b4b",
    "#4c1d95",
    "#010101",
    "#fefefe",
    "#fff",
    "#000",
  ]);
  for (let h = 0; h < 360; h += 15) {
    for (const l of [0.15, 0.35, 0.55, 0.75, 0.95]) {
      for (const c of [0.04, 0.12, 0.3]) {
        // oklchToHex gamut-maps, so an out-of-gamut request becomes the most
        // vivid in-gamut colour at that lightness and hue — a real input.
        inputs.add(oklchToHex({ l, c, h }));
      }
    }
  }
  return [...inputs];
}

const INPUTS = sweep();

/** The three grounds `onAccent` must read on. */
function grounds(palette: BrandPalette, theme: ThemeName): readonly string[] {
  const t = palette[theme];
  return [t.accent, t.accentHover, t.accentActive];
}

function alphaOf(rgba: string): number {
  const match = /rgba\((\d+), (\d+), (\d+), ([\d.]+)\)/.exec(rgba);
  if (match === null) throw new Error(`not an rgba(): ${rgba}`);
  return Number(match[4]);
}

function rgbOf(rgba: string): string {
  const match = /rgba\((\d+), (\d+), (\d+), ([\d.]+)\)/.exec(rgba);
  if (match === null) throw new Error(`not an rgba(): ${rgba}`);
  const hex = (n: string): string => Number(n).toString(16).padStart(2, "0");
  return `#${hex(match[1] ?? "0")}${hex(match[2] ?? "0")}${hex(match[3] ?? "0")}`;
}

describe("the sweep covers what it claims", () => {
  it("has every hue at 15° steps, several lightness and chroma levels, and the edge cases", () => {
    // 24 hues × 5 L × 3 C = 360 generated, minus the collisions where gamut
    // mapping at L=0.15 or 0.95 collapses several requests onto one hex, plus
    // the named cases: 282 distinct inputs at the time of writing. The point
    // of the number is that a regression that shrinks the sweep is visible.
    expect(INPUTS.length).toBeGreaterThanOrEqual(270);
    for (const edge of ["#000000", "#ffffff", "#ffff00", "#00ffff", "#ff0000", "#808080"]) {
      expect(INPUTS).toContain(edge);
    }
    expect(INPUTS).toContain(MOOV_DEFAULT_BRANDING.colors.primary);
  });
});

describe("derivePalette — the AA constraints, over the whole sweep", () => {
  it("makes the accent read as text on BOTH surfaces of each theme", () => {
    for (const input of INPUTS) {
      const palette = derivePalette(input);
      for (const theme of THEMES) {
        const { surfaceDefault, surfaceCanvas } = THEME_SURFACES[theme];
        const accent = palette[theme].accent;
        expect(
          contrastRatio(accent, surfaceDefault),
          `${input} → ${theme} accent ${accent} on surface-default`,
        ).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
        expect(
          contrastRatio(accent, surfaceCanvas),
          `${input} → ${theme} accent ${accent} on surface-canvas`,
        ).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
      }
    }
  });

  it("keeps onAccent legible on the accent AND on hover AND on active", () => {
    for (const input of INPUTS) {
      // With the hint white, black, and absent: the hint must never buy a
      // failing pair.
      for (const hint of [undefined, "#ffffff", "#000000", "#ffff00"]) {
        const palette = derivePalette(input, hint);
        for (const theme of THEMES) {
          const on = palette[theme].onAccent;
          for (const ground of grounds(palette, theme)) {
            expect(
              contrastRatio(on, ground),
              `${input} (hint ${String(hint)}) → ${theme} ${on} on ${ground}`,
            ).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
          }
        }
      }
    }
  });

  it("uses the customer's onPrimary when it clears every ground, and only then", () => {
    for (const input of INPUTS) {
      for (const hint of ["#ffffff", "#000000", "#0d0f17", "#ffff00"]) {
        const palette = derivePalette(input, hint);
        for (const theme of THEMES) {
          const worst = Math.min(
            ...grounds(palette, theme).map((ground) => contrastRatio(hint, ground)),
          );
          if (worst >= AA_NORMAL_TEXT) {
            expect(palette[theme].onAccent, `${input} ${theme} keeps hint ${hint}`).toBe(hint);
          } else {
            expect([ON_ACCENT_DARK, ON_ACCENT_LIGHT]).toContain(palette[theme].onAccent);
            expect(palette.adjusted[theme]).toContain(`onPrimary ${hint}`);
          }
        }
      }
    }
  });

  it("keeps body text legible over the strong tint on the default surface", () => {
    for (const input of INPUTS) {
      const palette = derivePalette(input);
      for (const theme of THEMES) {
        const surfaces = THEME_SURFACES[theme];
        const tint = palette[theme].accentTintStrong;
        const ground = compositeOver(rgbOf(tint), alphaOf(tint), surfaces.surfaceDefault);
        expect(
          contrastRatio(surfaces.textDefault, ground),
          `${input} → ${theme} text over tint-strong (${ground})`,
        ).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
      }
    }
  });

  it("builds the tints as alpha washes OF THE ACCENT, with the tokens.css alphas", () => {
    for (const input of INPUTS) {
      const palette = derivePalette(input);
      for (const theme of THEMES) {
        const t = palette[theme];
        const surfaces = THEME_SURFACES[theme];
        expect(rgbOf(t.accentTint)).toBe(t.accent);
        expect(rgbOf(t.accentTintStrong)).toBe(t.accent);
        expect(alphaOf(t.accentTint)).toBe(surfaces.tintAlpha);
        expect(alphaOf(t.accentTintStrong)).toBe(surfaces.tintStrongAlpha);
        expect(t.focusRing).toBe(t.accent);
      }
    }
  });

  it("gives hover and active a visible step away from the accent, in the right order", () => {
    for (const input of INPUTS) {
      const palette = derivePalette(input);
      for (const theme of THEMES) {
        const t = palette[theme];
        const l0 = hexToOklch(t.accent).l;
        const l1 = hexToOklch(t.accentHover).l;
        const l2 = hexToOklch(t.accentActive).l;
        expect(t.accentHover, `${input} ${theme} hover differs`).not.toBe(t.accent);
        expect(t.accentActive, `${input} ${theme} active differs`).not.toBe(t.accentHover);
        // Both steps go the SAME way from the accent, active further than hover.
        expect(Math.sign(l1 - l0), `${input} ${theme} monotone`).toBe(Math.sign(l2 - l0));
        expect(Math.abs(l2 - l0)).toBeGreaterThan(Math.abs(l1 - l0));
        // And the step is perceptible: at least 4% OKLCH lightness.
        expect(Math.abs(l1 - l0), `${input} ${theme} hover step`).toBeGreaterThan(0.04);
      }
    }
  });

  it("emits every colour as a lowercase six-digit hex or an rgba()", () => {
    for (const input of INPUTS) {
      const palette = derivePalette(input, "#FFF");
      for (const theme of THEMES) {
        const t = palette[theme];
        for (const hex of [t.accent, t.accentHover, t.accentActive, t.onAccent, t.focusRing]) {
          expect(hex).toMatch(/^#[0-9a-f]{6}$/);
        }
        expect(t.accentTint).toMatch(/^rgba\(\d+, \d+, \d+, 0\.\d+\)$/);
        expect(t.accentTintStrong).toMatch(/^rgba\(\d+, \d+, \d+, 0\.\d+\)$/);
      }
    }
  });
});

/**
 * The tonal family: Gmail's three related tones of ONE accent.
 *
 * These are the tokens the chrome is painted with — the compose button, the
 * selected message row, the rail's active folder pill — and the defect they
 * fix is a hue one, not a contrast one: they used to derive from the ADJUSTED
 * accent, so a pastel brand whose accent had to become a deep teal to be
 * readable as text lost its own hue everywhere. They carry text, so AA is
 * pinned here as well; but the hue assertion is the reason they exist.
 */
describe("derivePalette — the tonal containers", () => {
  it("keeps body text AA on the selected row and on the active pill", () => {
    for (const input of INPUTS) {
      const palette = derivePalette(input);
      for (const theme of THEMES) {
        const t = palette[theme];
        const surfaces = THEME_SURFACES[theme];
        expect(
          contrastRatio(surfaces.textDefault, t.selectedRow),
          `${input} ${theme} text on selectedRow ${t.selectedRow}`,
        ).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
        expect(
          contrastRatio(surfaces.textDefault, t.activePill),
          `${input} ${theme} text on activePill ${t.activePill}`,
        ).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
        // The pill's label is `--text-strong` (it is bold and it is the "you
        // are here" mark), so that one is pinned too.
        expect(
          contrastRatio(surfaces.textStrong, t.activePill),
          `${input} ${theme} strong text on activePill ${t.activePill}`,
        ).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
      }
    }
  });

  it("keeps onAccentContainer AA on the container it is painted on", () => {
    for (const input of INPUTS) {
      const palette = derivePalette(input);
      for (const theme of THEMES) {
        const t = palette[theme];
        expect(
          contrastRatio(t.onAccentContainer, t.accentContainer),
          `${input} ${theme} ${t.onAccentContainer} on ${t.accentContainer}`,
        ).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
      }
    }
  });

  it("puts the three tones at their targets, in the designed order", () => {
    for (const input of INPUTS) {
      const palette = derivePalette(input);
      for (const theme of THEMES) {
        const t = palette[theme];
        const surfaces = THEME_SURFACES[theme];
        // Each tone lands on its target lightness (within hex resolution).
        const near = (hex: string, target: number, name: string): void => {
          expect(hexToOklch(hex).l, `${input} ${theme} ${name} L`).toBeCloseTo(target, 1);
        };
        near(t.selectedRow, surfaces.selectedRow.l, "selectedRow");
        near(t.activePill, surfaces.activePill.l, "activePill");
        /*
         * The container is the one tone allowed to LEAVE its target: when the
         * separation floor is not met it is pushed away in 0.01 steps. So it
         * is pinned at its target OR beyond it, in the theme's own direction,
         * rather than at the target exactly.
         */
        const containerL = hexToOklch(t.accentContainer).l;
        if (surfaces.direction === -1) {
          expect(containerL, `${input} ${theme} container L`).toBeLessThanOrEqual(
            surfaces.container.l + 0.02,
          );
        } else {
          expect(containerL, `${input} ${theme} container L`).toBeGreaterThanOrEqual(
            surfaces.container.l - 0.02,
          );
        }
        /*
         * And the order Gmail uses: the row is the faintest, the pill sits
         * between it and the button. In the light theme "fainter" means closer
         * to white (higher L); in the dark theme it means closer to black.
         * Stated as distance from the theme's own surface, the rule is the
         * same sentence in both.
         */
        const depth = (hex: string): number =>
          Math.abs(hexToOklch(hex).l - hexToOklch(surfaces.surfaceDefault).l);
        expect(depth(t.selectedRow), `${input} ${theme} row < pill`).toBeLessThan(
          depth(t.activePill),
        );
        expect(depth(t.activePill), `${input} ${theme} pill < container`).toBeLessThan(
          depth(t.accentContainer),
        );
      }
    }
  });

  it("keeps neighbouring tones perceptibly apart, over the whole sweep", () => {
    /*
     * The defect this pins, measured live on mail.gruponu.com: with primary
     * #b8faff the trio came out #afebef / #b6f1f6 / #cffcff and the owner saw
     * ONE colour. He was right — a pastel's chroma is already under a shared
     * ceiling, so the only axis left was lightness, and 0.90/0.92/0.96 is not
     * a visible step. Gmail separates on BOTH axes (its compose container
     * #c2e7ff is clearly more saturated than its active folder #d3e3fd, and
     * its selected row is near-white).
     *
     * "Perceptibly" is not a matter of opinion here: OKLab was fitted so that
     * Euclidean distance tracks perceived difference, so the assertion is a
     * plain ΔE with no weighting — and it runs over every hue, because the
     * shortfall appeared for ONE family of inputs and passed inspection for
     * the rest.
     */
    for (const input of INPUTS) {
      const palette = derivePalette(input);
      for (const theme of THEMES) {
        const t = palette[theme];
        expect(
          deltaE(t.accentContainer, t.activePill),
          `${input} ${theme} container ${t.accentContainer} vs pill ${t.activePill}`,
        ).toBeGreaterThanOrEqual(MIN_DELTA_CONTAINER_PILL);
        expect(
          deltaE(t.activePill, t.selectedRow),
          `${input} ${theme} pill ${t.activePill} vs row ${t.selectedRow}`,
        ).toBeGreaterThanOrEqual(MIN_DELTA_PILL_ROW);
      }
    }
  });

  it("separates by CHROMA as well as by lightness, for a brand that has chroma", () => {
    /*
     * The floor above is a distance and could in principle be met by lightness
     * alone; this is the assertion that the second axis is really in play,
     * which is the half the first cut was missing. The container is the most
     * saturated of the three and the row the least — Gmail's own ordering.
     */
    for (const input of INPUTS) {
      const base = hexToOklch(input);
      // A near-grey brand has no chroma to distribute; its tones are greys.
      if (base.c < 0.12) continue;
      const palette = derivePalette(input);
      for (const theme of THEMES) {
        const t = palette[theme];
        const c = (hex: string): number => hexToOklch(hex).c;
        expect(
          c(t.accentContainer),
          `${input} ${theme} container more saturated than pill`,
        ).toBeGreaterThan(c(t.activePill));
        expect(
          c(t.activePill),
          `${input} ${theme} pill more saturated than row`,
        ).toBeGreaterThan(c(t.selectedRow));
      }
    }
  });

  it("never exhausts the separation budget — the caps do nearly all the work", () => {
    /*
     * The push is a SAFETY NET, not the mechanism. If a hue needed many steps
     * the targets would be wrong, and the container would be drifting far from
     * where it was designed to sit; a bound on the drift is how that shows up
     * as a failing test rather than as a quietly odd-looking brand.
     */
    for (const input of INPUTS) {
      const palette = derivePalette(input);
      for (const theme of THEMES) {
        const surfaces = THEME_SURFACES[theme];
        const drift = Math.abs(
          hexToOklch(palette[theme].accentContainer).l - surfaces.container.l,
        );
        expect(drift, `${input} ${theme} container drift`).toBeLessThanOrEqual(0.05);
      }
    }
  });

  it("derives the trio from the ORIGINAL primary's hue, not from the adjusted accent", () => {
    for (const input of INPUTS) {
      const base = hexToOklch(input);
      // Only meaningful for a colour that HAS a hue: below this chroma the
      // angle is numerical noise and the tones are greys by design.
      if (base.c < 0.05) continue;
      const palette = derivePalette(input);
      const t = palette.light;
      for (const [name, hex] of [
        ["accentContainer", t.accentContainer],
        ["selectedRow", t.selectedRow],
        ["activePill", t.activePill],
      ] as const) {
        const drift = Math.abs(((hexToOklch(hex).h - base.h + 540) % 360) - 180);
        expect(drift, `${input} → ${name} ${hex} hue drift`).toBeLessThanOrEqual(8);
      }
    }
  });

  it("keeps a pastel brand's own hue in the chrome — the defect this fixes", () => {
    /*
     * #b8faff is light cyan. As TEXT it reads at 1.08:1 on white, so the
     * accent is dropped to a dark teal — and before this family existed every
     * tint came from that teal, so the customer's pastel was nowhere on the
     * screen. Now the ink is teal and the containers are cyan.
     */
    const p = derivePalette("#b8faff", "#ffffff");
    expect(p.light.accent).toBe("#3b7b80");
    expect(p.light.accentContainer).toBe("#9ddee3");
    expect(p.light.onAccentContainer).toBe("#00272a");
    expect(p.light.selectedRow).toBe("#e3fbfc");
    expect(p.light.activePill).toBe("#c2f2f6");
    expect(p.dark.accentContainer).toBe("#004145");
    expect(p.dark.onAccentContainer).toBe("#b1f2f7");
    expect(p.dark.selectedRow).toBe("#051c1e");
    expect(p.dark.activePill).toBe("#002d30");
    // The containers are LIGHTER than the accent in the light theme — i.e.
    // they came from the pastel, not from the teal the accent became.
    for (const tone of [p.light.accentContainer, p.light.selectedRow, p.light.activePill]) {
      expect(hexToOklch(tone).l).toBeGreaterThan(hexToOklch(p.light.accent).l + 0.2);
    }
  });

  it("documents the trio for the Moov default", () => {
    // The values tokens.css carries by hand; branding.test.ts pins that copy.
    const p = derivePalette("#5b5bd6", "#ffffff");
    expect(p.light.accentContainer).toBe("#c6cdff");
    expect(p.light.onAccentContainer).toBe("#1b1c42");
    expect(p.light.selectedRow).toBe("#f3f4ff");
    expect(p.light.activePill).toBe("#e2e6ff");
    expect(p.dark.accentContainer).toBe("#2f3260");
    expect(p.dark.onAccentContainer).toBe("#dee3ff");
    expect(p.dark.selectedRow).toBe("#151726");
    expect(p.dark.activePill).toBe("#21243e");
  });

  it("emits the four as lowercase six-digit hex, and is deterministic", () => {
    for (const input of INPUTS) {
      const a = derivePalette(input, "#FFF");
      const b = derivePalette(input, "#FFF");
      for (const theme of THEMES) {
        for (const hex of [
          a[theme].accentContainer,
          a[theme].onAccentContainer,
          a[theme].selectedRow,
          a[theme].activePill,
        ]) {
          expect(hex).toMatch(/^#[0-9a-f]{6}$/);
        }
        expect(a[theme]).toEqual(b[theme]);
      }
    }
  });

  it("degrades the corners of the cube to neutral tones rather than to nothing", () => {
    // Black and white have no hue to keep, so the tones are greys AT THE
    // TARGET LIGHTNESS — still three distinguishable steps, still AA.
    for (const input of ["#000000", "#ffffff"]) {
      const p = derivePalette(input);
      for (const theme of THEMES) {
        const t = p[theme];
        expect(new Set([t.selectedRow, t.activePill, t.accentContainer]).size).toBe(3);
        expect(
          contrastRatio(THEME_SURFACES[theme].textDefault, t.selectedRow),
        ).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
      }
    }
  });
});

describe("derivePalette — adjusting, and saying so", () => {
  it("returns a passing primary EXACTLY, and declares nothing for that theme", () => {
    for (const input of INPUTS) {
      const palette = derivePalette(input);
      for (const theme of THEMES) {
        const { surfaceDefault, surfaceCanvas } = THEME_SURFACES[theme];
        const passes =
          contrastRatio(input, surfaceDefault) >= AA_NORMAL_TEXT &&
          contrastRatio(input, surfaceCanvas) >= AA_NORMAL_TEXT;
        if (passes) {
          expect(palette[theme].accent, `${input} ${theme} untouched`).toBe(normalizeHex(input));
          // No hint was given, so nothing about onPrimary can be declared
          // either: the theme is clean.
          expect(palette.adjusted[theme]).toBeUndefined();
        } else {
          expect(palette[theme].accent).not.toBe(normalizeHex(input));
          expect(palette.adjusted[theme]).toMatch(/reads at \d+\.\d\d:1 .*WCAG AA/);
          expect(palette.adjusted[theme]).toContain(palette[theme].accent);
        }
      }
    }
  });

  it("moves lightness only: the hue survives the adjustment", () => {
    for (const input of INPUTS) {
      const original = hexToOklch(input);
      // A grey has no hue to preserve, and a colour at the sRGB corners can
      // rotate slightly when chroma is gamut-mapped at the new lightness.
      if (original.c < 0.05) continue;
      const palette = derivePalette(input);
      for (const theme of THEMES) {
        const adjusted = hexToOklch(palette[theme].accent);
        if (adjusted.c < 0.03) continue;
        const delta = Math.abs(((adjusted.h - original.h + 540) % 360) - 180);
        expect(delta, `${input} → ${theme} ${palette[theme].accent} hue drift`).toBeLessThan(8);
      }
    }
  });

  it("is deterministic", () => {
    for (const input of INPUTS) {
      expect(derivePalette(input, "#ffffff")).toEqual(derivePalette(input, "#ffffff"));
      expect(derivePalette(input)).toEqual(derivePalette(input));
    }
    // And case/length-insensitive on the input form.
    expect(derivePalette("#C0FFEE")).toEqual(derivePalette("#c0ffee"));
    expect(derivePalette("#fff")).toEqual(derivePalette("#ffffff"));
  });

  it("does not shift the Moov brand: #5b5bd6 stays #5b5bd6 with white on it, in light", () => {
    const { primary, onPrimary } = MOOV_DEFAULT_BRANDING.colors;
    const palette = derivePalette(primary, onPrimary);
    expect(palette.light.accent).toBe("#5b5bd6");
    expect(palette.light.onAccent).toBe("#ffffff");
    expect(palette.adjusted.light).toBeUndefined();
    // The dark theme lifts it, as tokens.css always did, and says so.
    expect(palette.dark.accent).not.toBe("#5b5bd6");
    expect(palette.adjusted.dark).toContain("raised");
  });

  it("handles the named edge cases the way the design says", () => {
    // Pure white cannot be text on white: it becomes a mid grey, hue-less.
    const white = derivePalette("#ffffff", "#000000");
    expect(hexToOklch(white.light.accent).c).toBeLessThan(0.01);
    expect(white.adjusted.light).toContain("lowered");
    // ...and on a dark page it is fine as it is.
    expect(white.dark.accent).toBe("#ffffff");

    // Pure black is fine on white and is lifted to a grey on dark.
    const black = derivePalette("#000000", "#ffffff");
    expect(black.light.accent).toBe("#000000");
    expect(black.light.onAccent).toBe("#ffffff");
    expect(black.dark.accent).not.toBe("#000000");
    expect(black.dark.onAccent).toBe(ON_ACCENT_DARK);

    // Yellow: unusable as text on white, deepened to an olive-gold that still
    // carries the hue; untouched on dark, where black text goes on it.
    const yellow = derivePalette("#ffff00", "#ffffff");
    expect(yellow.adjusted.light).toBeDefined();
    expect(yellow.dark.accent).toBe("#ffff00");
    expect(yellow.dark.onAccent).toBe(ON_ACCENT_DARK);
    // The white hint fails on yellow and is replaced — declared.
    expect(yellow.adjusted.dark).toContain("onPrimary #ffffff");

    // Red at 3.73:1 on white: nudged, not replaced.
    const red = derivePalette("#ff0000");
    expect(red.light.accent).not.toBe("#ff0000");
    expect(hexToOklch(red.light.accent).h).toBeCloseTo(hexToOklch("#ff0000").h, 0);

    // Mid grey misses both themes narrowly and is nudged both ways.
    const grey = derivePalette("#808080");
    expect(grey.adjusted.light).toBeDefined();
    expect(grey.adjusted.dark).toBeDefined();
  });

  it("does not throw on a malformed input; it degrades to a palette built from black", () => {
    expect(() => derivePalette("rebeccapurple")).not.toThrow();
    expect(derivePalette("rebeccapurple")).toEqual(derivePalette("#000000"));
  });
});

describe("the colour maths", () => {
  it("round-trips sRGB through OKLCH", () => {
    for (const input of INPUTS) {
      expect(oklchToHex(hexToOklch(input))).toBe(normalizeHex(input));
    }
  });

  it("puts the OKLCH reference points where the spec does", () => {
    // White is L=1 with no chroma; black is L=0.
    expect(hexToOklch("#ffffff").l).toBeCloseTo(1, 3);
    expect(hexToOklch("#ffffff").c).toBeLessThan(1e-3);
    expect(hexToOklch("#000000").l).toBeCloseTo(0, 6);
    // sRGB red: L≈0.628, C≈0.258, h≈29.2° (Ottosson's published values).
    const red = hexToOklch("#ff0000");
    expect(red.l).toBeCloseTo(0.628, 2);
    expect(red.c).toBeCloseTo(0.258, 2);
    expect(red.h).toBeCloseTo(29.2, 0);
  });

  it("composites alpha over a ground the way the browser paints it", () => {
    expect(compositeOver("#000000", 0.5, "#ffffff")).toBe("#808080");
    expect(compositeOver("#ff0000", 1, "#ffffff")).toBe("#ff0000");
    expect(compositeOver("#ff0000", 0, "#ffffff")).toBe("#ffffff");
  });

  it("normalises hex forms", () => {
    expect(normalizeHex("#FFF")).toBe("#ffffff");
    expect(normalizeHex("#C0ffEE")).toBe("#c0ffee");
    expect(parseHex(normalizeHex("#123"))).toEqual([17, 34, 51]);
  });
});

describe("the surfaces this module assumes match tokens.css", () => {
  /**
   * THEME_SURFACES is a copy of tokens.css because the module is pure. This is
   * the pin: change a surface in the stylesheet and this fails, so the contrast
   * guarantee can never be quietly computed against the wrong ground.
   */
  const css = readFileSync(join(process.cwd(), "src/styles/tokens.css"), "utf8");
  const lightBlock = css.slice(0, css.indexOf("@media (prefers-color-scheme: dark)"));
  const darkBlock = css.slice(css.indexOf(':root[data-theme="dark"]'));

  const token = (block: string, name: string): string | undefined =>
    new RegExp(`${name}:\\s*(#[0-9a-fA-F]{6});`).exec(block)?.[1]?.toLowerCase();

  it("light", () => {
    expect(token(lightBlock, "--surface-default")).toBe(THEME_SURFACES.light.surfaceDefault);
    expect(token(lightBlock, "--surface-canvas")).toBe(THEME_SURFACES.light.surfaceCanvas);
    expect(token(lightBlock, "--text-default")).toBe(THEME_SURFACES.light.textDefault);
    expect(token(lightBlock, "--text-strong")).toBe(THEME_SURFACES.light.textStrong);
  });

  it("dark", () => {
    expect(token(darkBlock, "--surface-default")).toBe(THEME_SURFACES.dark.surfaceDefault);
    expect(token(darkBlock, "--surface-canvas")).toBe(THEME_SURFACES.dark.surfaceCanvas);
    expect(token(darkBlock, "--text-default")).toBe(THEME_SURFACES.dark.textDefault);
    expect(token(darkBlock, "--text-strong")).toBe(THEME_SURFACES.dark.textStrong);
  });
});


describe("the login gradient derived from the primary", () => {
  /**
   * The owner's finding: they set a pale cyan primary, left the two gradient
   * stops alone, and their login panel came out VIOLET — the stops still fell
   * back to Moov's. A brand that configures a primary has already said what its
   * gradient should be.
   *
   * The SERVER is the authority (`branding.DeriveSplashColors` in Go); this
   * copy exists so the brand panel can show an administrator the result before
   * they save, as the placeholder in a field they have not filled in. The two
   * implementations must agree exactly, so the table below is the SAME table
   * Go's `TestDeriveSplashColorsTable` asserts, computed by hand: each channel
   * scaled by (1 - amount), rounded half-up.
   */
  const TABLE: readonly (readonly [string, string, string])[] = [
    // The pastel cyan that found the bug. 0xb8=184, 0xfa=250, 0xff=255.
    // 30% -> 55.2/75/76.5 -> #374b4d; 65% -> 119.6/162.5/165.75 -> #78a3a6.
    ["#b8faff", "#374b4d", "#78a3a6"],
    // Moov's own indigo: 0x5b=91, 0xd6=214.
    ["#5b5bd6", "#1b1b40", "#3b3b8b"],
    ["#000000", "#000000", "#000000"],
    ["#ffffff", "#4d4d4d", "#a6a6a6"],
    // The three-digit form expands by DOUBLING the digit, not zero-padding.
    ["#f00", "#4d0000", "#a60000"],
  ];

  it("matches the server's table, byte for byte", () => {
    for (const [primary, from, to] of TABLE) {
      expect(deriveSplashColors(primary), primary).toEqual({ from, to });
    }
  });

  it("uses the same two constants the server does", () => {
    expect(SPLASH_FROM_MIX_TOWARD_BLACK).toBe(0.7);
    expect(SPLASH_TO_MIX_TOWARD_BLACK).toBe(0.35);
  });

  it("derives a gradient that runs deep to mid, never the other way", () => {
    // The panel paints `from` at the top-left corner; a `from` lighter than
    // `to` would read as an inverted gradient on every brand at once.
    for (const primary of ["#b8faff", "#5b5bd6", "#0f766e", "#ffcc00", "#123"]) {
      const { from, to } = deriveSplashColors(primary);
      const sum = (hex: string): number => (parseHex(hex) ?? [0, 0, 0]).reduce((a, b) => a + b, 0);
      expect(sum(from), primary).toBeLessThanOrEqual(sum(to));
      expect(from).toMatch(/^#[0-9a-f]{6}$/);
      expect(to).toMatch(/^#[0-9a-f]{6}$/);
    }
  });

  it("derives nothing from a value that is not a colour", () => {
    // The caller then keeps its own fallback rather than writing a custom
    // property built from garbage.
    for (const bad of ["", "rebeccapurple", "#12345", "#gggggg", "5b5bd6"]) {
      expect(deriveSplashColors(bad), bad).toEqual({ from: "", to: "" });
    }
  });

  it("is not the accent derivation, and must not become it", () => {
    /*
     * Two different jobs with two different colour spaces. `derivePalette`
     * moves lightness in OKLCH because its output must clear a contrast
     * threshold while staying recognizably the customer's colour; these two are
     * a decorative backdrop with no contrast constraint of their own (the
     * panel's scrim owns legibility), and a straight sRGB mix is the operation
     * an operator can check with a calculator — which matters when the value is
     * offered to them as a placeholder.
     */
    const primary = "#b8faff";
    expect(deriveSplashColors(primary).from).not.toBe(derivePalette(primary).light.accent);
  });
});
