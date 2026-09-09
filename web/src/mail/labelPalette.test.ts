import { describe, expect, it } from "vitest";

import {
  AA_NORMAL_TEXT,
  contrastRatio,
  DEFAULT_LABEL_COLOR_ID,
  isLabelColorId,
  labelColor,
  labelColorVariables,
  LABEL_COLOR_STEPS,
  LABEL_COLORS,
  meetsAA,
  parseHex,
  relativeLuminance,
} from "./labelPalette";

describe("the palette is closed and well-formed", () => {
  it("has twelve hues at two strengths, with unique ids (F-31)", () => {
    // Twelve pales beside each other were "casi indistinguibles" (F-31), and a
    // user who wanted one label to be loud had no way to say so.
    expect(LABEL_COLORS).toHaveLength(24);
    const ids = new Set(LABEL_COLORS.map((color) => color.id));
    expect(ids.size).toBe(LABEL_COLORS.length);
    expect(new Set(LABEL_COLORS.map((color) => color.hue)).size).toBe(12);
    for (const step of LABEL_COLOR_STEPS) {
      expect(LABEL_COLORS.filter((color) => color.step === step)).toHaveLength(12);
    }
  });

  it("names ids rather than hex values, so a colour can be re-tuned later", () => {
    for (const color of LABEL_COLORS) {
      expect(color.id).toMatch(/^[a-z]+(-bold)?$/);
    }
  });

  /*
   * The compatibility guarantee, asserted rather than assumed.
   *
   * The PALE step keeps the bare hue name because that is what every label
   * created before F-31 stored. Renaming them to "amber-pale" would have turned
   * every existing label into an unknown id and silently repainted the user's
   * whole set grey — a data migration disguised as a palette tweak.
   */
  it("leaves the pre-existing ids meaning exactly what they meant", () => {
    for (const color of LABEL_COLORS) {
      if (color.step !== "pale") continue;
      expect(color.id).toBe(color.hue);
    }
    // The original twelve, spot-checked against the values they shipped with.
    expect(labelColor("amber").light).toBe("#fef3c7");
    expect(labelColor("blue").lightText).toBe("#1e3a8a");
  });

  it("gives every hue a bold step that is one ground in both themes", () => {
    for (const color of LABEL_COLORS) {
      if (color.step !== "bold") continue;
      // A saturated chip reads correctly on a white page and on a dark one, so
      // inverting it would produce a second colour for no gain.
      expect(color.dark).toBe(color.light);
      expect(color.lightText).toBe("#ffffff");
      expect(color.darkText).toBe("#ffffff");
    }
  });

  it("gives every entry all four values as parseable hex", () => {
    for (const color of LABEL_COLORS) {
      for (const value of [color.light, color.lightText, color.dark, color.darkText]) {
        expect(value).toMatch(/^#[0-9a-f]{6}$/);
        expect(parseHex(value)).toBeDefined();
      }
    }
  });

  it("includes the default id", () => {
    expect(isLabelColorId(DEFAULT_LABEL_COLOR_ID)).toBe(true);
  });
});

describe("contrast — the guarantee the closed palette exists to make", () => {
  /*
   * The reason there is no colour picker (canon §2.6): Gmail ships background/
   * text PAIRS because a free picker lets someone choose an unreadable chip.
   * A pair whose contrast was checked by eye regresses the first time a hex
   * value is nudged, so it is checked here.
   */
  it("meets WCAG 2.2 AA for normal text on BOTH themes, for every entry", () => {
    for (const color of LABEL_COLORS) {
      const light = contrastRatio(color.light, color.lightText);
      const dark = contrastRatio(color.dark, color.darkText);
      expect(
        light,
        `${color.id} light pair is ${light.toFixed(2)}:1`,
      ).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
      expect(
        dark,
        `${color.id} dark pair is ${dark.toFixed(2)}:1`,
      ).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
    }
  });

  it("computes the reference ratios of the WCAG definition", () => {
    // Black on white is the maximum, 21:1; a colour against itself is 1:1.
    expect(contrastRatio("#000000", "#ffffff")).toBeCloseTo(21, 5);
    expect(contrastRatio("#777777", "#777777")).toBeCloseTo(1, 5);
    // Symmetric: the formula puts the lighter colour on top either way.
    expect(contrastRatio("#000000", "#ffffff")).toBeCloseTo(
      contrastRatio("#ffffff", "#000000"),
      10,
    );
  });

  it("computes relative luminance at the endpoints", () => {
    expect(relativeLuminance("#000000")).toBeCloseTo(0, 6);
    expect(relativeLuminance("#ffffff")).toBeCloseTo(1, 6);
  });

  it("meetsAA agrees with the threshold", () => {
    expect(meetsAA("#ffffff", "#000000")).toBe(true);
    expect(meetsAA("#ffffff", "#f0f0f0")).toBe(false);
  });
});

describe("parseHex", () => {
  it("accepts the short form and expands it", () => {
    expect(parseHex("#fff")).toEqual([255, 255, 255]);
    expect(parseHex("#0a0")).toEqual([0, 170, 0]);
  });

  it("accepts the long form with or without the hash", () => {
    expect(parseHex("#1e293b")).toEqual([30, 41, 59]);
    expect(parseHex("1e293b")).toEqual([30, 41, 59]);
  });

  it("rejects a malformed value rather than guessing", () => {
    expect(parseHex("")).toBeUndefined();
    expect(parseHex("#12345")).toBeUndefined();
    expect(parseHex("#zzzzzz")).toBeUndefined();
  });
});

describe("labelColor falls back rather than failing", () => {
  it("returns the named colour", () => {
    expect(labelColor("blue").id).toBe("blue");
  });

  it("returns the default for an unknown id — a label from a newer build", () => {
    expect(labelColor("chartreuse").id).toBe(DEFAULT_LABEL_COLOR_ID);
    expect(labelColor(undefined).id).toBe(DEFAULT_LABEL_COLOR_ID);
  });
});

describe("labelColorVariables", () => {
  it("carries BOTH theme pairs, so the stylesheet swaps without JavaScript", () => {
    const vars = labelColorVariables("blue");
    const blue = labelColor("blue");
    expect(vars).toEqual({
      "--label-bg": blue.light,
      "--label-fg": blue.lightText,
      "--label-bg-dark": blue.dark,
      "--label-fg-dark": blue.darkText,
    });
  });
});

/**
 * The default is spelt out rather than indexed out of the generated list, so
 * that it is total by construction. This is the check that keeps the two from
 * drifting: the fallback must still BE a member of the palette, with the same
 * values the generator produces for it.
 */
describe("the default entry", () => {
  it("is a real member of the palette, not a parallel definition", () => {
    const fallback = labelColor(undefined);
    const listed = LABEL_COLORS.find((color) => color.id === DEFAULT_LABEL_COLOR_ID);

    expect(listed).toBeDefined();
    expect(fallback).toEqual(listed);
  });
});
