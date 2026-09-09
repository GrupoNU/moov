import { readFileSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import { AA_NORMAL_TEXT, contrastRatio, parseHex } from "../mail/labelPalette";
import { MOOV_DEFAULT_BRANDING } from "./branding";
import {
  compositeOver,
  derivePalette,
  hexToOklch,
  normalizeHex,
  oklchToHex,
  ON_ACCENT_DARK,
  ON_ACCENT_LIGHT,
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
  });

  it("dark", () => {
    expect(token(darkBlock, "--surface-default")).toBe(THEME_SURFACES.dark.surfaceDefault);
    expect(token(darkBlock, "--surface-canvas")).toBe(THEME_SURFACES.dark.surfaceCanvas);
    expect(token(darkBlock, "--text-default")).toBe(THEME_SURFACES.dark.textDefault);
  });
});
