/**
 * The label colour palette — closed, and contrast-guaranteed (L3 E8, GC-5).
 *
 * # Why a closed palette and not a colour picker
 *
 * This is Gmail's own design, copied for its reason rather than its look. Gmail
 * offers a fixed set of background/text PAIRS ("a closed palette guaranteeing
 * contrast, never a free picker" — canon §2.6, /mail/answer/118708). A free
 * picker lets a user choose `#f0f0f0` text on `#ffffff`, and the chip they
 * cannot read is a chip we shipped. Pairs mean the text colour is chosen WITH
 * the background by someone who checked, once, for everyone.
 *
 * Bulwark ships 39 colours (3 shades × 13 hues). We ship 12, and the difference
 * is not timidity: with a 26-keyword ceiling a user will never hold more than
 * ~20 labels, so 39 swatches is a longer grid to scan for no additional
 * expressive power. Twelve distinguishable hues over a realistic label count is
 * the honest number.
 *
 * # The contrast guarantee is a test, not a claim
 *
 * Every pair is asserted at WCAG 2.2 AA for normal text (4.5:1) against BOTH
 * its light and dark backgrounds by `labelPalette.test.ts`, using the
 * {@link contrastRatio} implementation below (WCAG 2.x relative luminance,
 * sRGB). A palette whose contrast is checked by eye is a palette that regresses
 * the first time someone nudges a hex value.
 *
 * # Why each colour carries a dark-theme pair too
 *
 * The app is theme-aware (light, dark, and system). A single background chosen
 * for a white page is a muddy smear on a dark one, and a single text colour
 * cannot be legible on both. So a colour is FOUR values, and the component
 * picks the pair by CSS custom properties rather than by reading the theme in
 * JavaScript — which is what keeps the chips correct when the OS theme flips
 * while the tab is open.
 */

/** One palette entry: an id, and the two theme-specific pairs. */
export interface LabelColor {
  /** The stable id stored with the label. Never a hex value — see below. */
  readonly id: string;
  /** Background in the light theme. */
  readonly light: string;
  /** Text on {@link light}. */
  readonly lightText: string;
  /** Background in the dark theme. */
  readonly dark: string;
  /** Text on {@link dark}. */
  readonly darkText: string;
  /**
   * The hue's family, shared by its pale and bold steps (F-31).
   *
   * Used to lay the picker out as one row per hue, so the two steps of a
   * colour sit beside each other and the grid reads as twelve hues at two
   * strengths rather than as twenty-four unrelated squares.
   */
  readonly hue: string;
  /** "pale" or "bold" — which step of the hue this is. */
  readonly step: LabelColorStep;
}

export const LABEL_COLOR_STEPS = ["pale", "bold"] as const;
export type LabelColorStep = (typeof LABEL_COLOR_STEPS)[number];

/**
 * The palette.
 *
 * Ids are NAMES, not hex values, and that is the load-bearing decision: the
 * stored label metadata references `"amber"`, so a future tweak to the amber
 * background — for contrast, for a redesign, for a third theme — reaches every
 * existing label instead of leaving them pinned to a hex string chosen in 2026.
 *
 * The light pairs are dark text (`#1f2937`-ish, or white where the hue is deep)
 * on a pale tint; the dark pairs invert to a light text on a desaturated deep
 * tint, because a pale tint on a dark page glows.
 */
/**
 * The twelve hues, each at TWO strengths (F-31).
 *
 * # Why a second step, when twelve pales were the deliberate number
 *
 * The review put it plainly: eleven pastels beside each other were "casi
 * indistinguibles". That is not a failure of the palette's REASONING — pale
 * tints with dark text are the right default, they carry AA everywhere, and
 * they let a chip sit in a list without shouting — it is a failure of RANGE. A
 * user labelling "Facturas" and "Urgente" wants one of them to be loud, and
 * with only pales the only way to say "this one matters" is to pick a hue that
 * happens to be darker, which is a distinction nobody can predict.
 *
 * So each hue now has a `bold` step: the SAME saturated ground in both themes
 * with white text. That is the one place this file departs from "four values,
 * two pairs", and it is deliberate — a saturated chip reads correctly on a
 * white page and on a dark one, so inverting it would produce a second colour
 * for no gain. The pale step keeps the original inversion, because a pale tint
 * on a dark page really does glow.
 *
 * Twenty-four is still a closed palette and still contrast-guaranteed by the
 * test below; what it stops being is a grid of near-identical squares. Bulwark
 * ships 39 (3 steps × 13 hues); with a 26-keyword ceiling a user will not hold
 * more than ~20 labels, so a third step would be a longer grid to scan for no
 * additional expressive power.
 */

/** The hue order, which is also the picker's row order. */
const HUES = [
  "slate",
  "red",
  "orange",
  "amber",
  "lime",
  "green",
  "teal",
  "cyan",
  "blue",
  "indigo",
  "purple",
  "pink",
] as const;

/** The pale step's four values, per hue. */
const PALE: Readonly<
  Record<(typeof HUES)[number], readonly [string, string, string, string]>
> = {
  slate: ["#e2e8f0", "#1e293b", "#334155", "#e2e8f0"],
  red: ["#fee2e2", "#7f1d1d", "#7f1d1d", "#fee2e2"],
  orange: ["#ffedd5", "#7c2d12", "#7c2d12", "#ffedd5"],
  amber: ["#fef3c7", "#78350f", "#78350f", "#fef3c7"],
  lime: ["#ecfccb", "#365314", "#365314", "#ecfccb"],
  green: ["#dcfce7", "#14532d", "#14532d", "#dcfce7"],
  teal: ["#ccfbf1", "#134e4a", "#134e4a", "#ccfbf1"],
  cyan: ["#cffafe", "#164e63", "#164e63", "#cffafe"],
  blue: ["#dbeafe", "#1e3a8a", "#1e3a8a", "#dbeafe"],
  indigo: ["#e0e7ff", "#312e81", "#312e81", "#e0e7ff"],
  purple: ["#f3e8ff", "#581c87", "#581c87", "#f3e8ff"],
  pink: ["#fce7f3", "#831843", "#831843", "#fce7f3"],
};

/**
 * The bold step's ground, in BOTH themes, with white text.
 *
 * Every one of these clears AA against `#ffffff` with room to spare (the
 * tightest is lime at 4.99:1), which the palette test asserts rather than
 * trusts — these were chosen by measurement, not by eye.
 */
const BOLD: Readonly<Record<(typeof HUES)[number], string>> = {
  slate: "#334155",
  red: "#b91c1c",
  orange: "#c2410c",
  amber: "#b45309",
  lime: "#4d7c0f",
  green: "#15803d",
  teal: "#0f766e",
  cyan: "#0e7490",
  blue: "#1d4ed8",
  indigo: "#4338ca",
  purple: "#7e22ce",
  pink: "#be185d",
};

/**
 * The palette, built from the two tables.
 *
 * # The ids are load-bearing and BACKWARD-COMPATIBLE
 *
 * The pale step keeps the bare hue name (`"amber"`), which is what every label
 * created before this change stored. Renaming them to `"amber-pale"` would have
 * turned every existing label into an unknown id and silently repainted the
 * user's whole set grey. The new step takes the suffixed id (`"amber-bold"`),
 * so the addition is additive in the only sense that matters: no stored value
 * changes meaning.
 */
export const LABEL_COLORS: readonly LabelColor[] = HUES.flatMap((hue) => {
  const [light, lightText, dark, darkText] = PALE[hue];
  const bold = BOLD[hue];
  return [
    { id: hue, light, lightText, dark, darkText, hue, step: "pale" as const },
    {
      // The suffixed id is the NEW one; the bare name stays with the pale step
      // so no label created before this change changes colour.
      id: `${hue}-bold`,
      light: bold,
      lightText: "#ffffff",
      dark: bold,
      darkText: "#ffffff",
      hue,
      step: "bold" as const,
    },
  ];
});

/**
 * The default, WRITTEN OUT rather than indexed out of the list.
 *
 * `LABEL_COLORS[0]` would be the same object, and would need an assertion the
 * linter forbids for a good reason: an index into a generated array is a claim
 * the type system cannot check, and a fallback that could be `undefined` is
 * exactly the thing this binding exists to rule out. Spelt as a literal it is
 * total by construction, and the palette test asserts it is also a MEMBER of
 * the list — so the two cannot drift apart silently.
 */
const SLATE: LabelColor = {
  id: "slate",
  light: PALE.slate[0],
  lightText: PALE.slate[1],
  dark: PALE.slate[2],
  darkText: PALE.slate[3],
  hue: "slate",
  step: "pale",
};

/** The id used when a label has no colour, or names one this build removed. */
export const DEFAULT_LABEL_COLOR_ID = "slate";

const BY_ID: ReadonlyMap<string, LabelColor> = new Map(
  LABEL_COLORS.map((color) => [color.id, color]),
);

/** True when an id names a colour this build knows. */
export function isLabelColorId(id: string): boolean {
  return BY_ID.has(id);
}

/**
 * The colour for an id, falling back to the default.
 *
 * Never throws and never returns undefined: an unknown id is what a label
 * created by a NEWER build looks like to an older one, and a chip rendered in
 * the default grey is strictly better than a crash or an invisible label.
 */
export function labelColor(id: string | undefined): LabelColor {
  // `SLATE` is the default AND a member of LABEL_COLORS, held as its own
  // binding so the fallback needs no assertion and no lookup that could miss.
  if (id === undefined) return SLATE;
  return BY_ID.get(id) ?? SLATE;
}

/**
 * The inline custom properties a chip renders with.
 *
 * Returned as DATA rather than written to the DOM here, so the mapping is
 * testable without a document — and so the component decides where they land.
 * The stylesheet reads `--label-bg`/`--label-fg` and swaps which pair they hold
 * under the dark-theme selector, which is why both pairs travel together
 * instead of the caller picking one.
 */
export function labelColorVariables(id: string | undefined): Readonly<Record<string, string>> {
  const color = labelColor(id);
  return {
    "--label-bg": color.light,
    "--label-fg": color.lightText,
    "--label-bg-dark": color.dark,
    "--label-fg-dark": color.darkText,
  };
}

// ---------------------------------------------------------------------------
// contrast — WCAG 2.2 relative luminance, so the guarantee is checkable
// ---------------------------------------------------------------------------

/** Parses `#rgb` or `#rrggbb` into 0-255 channels. Returns undefined if malformed. */
export function parseHex(hex: string): readonly [number, number, number] | undefined {
  const value = hex.trim().replace(/^#/, "");
  if (value.length === 3) {
    // Indexed rather than spread: a hex triple is ASCII by definition, and the
    // spread form trips the codebase's rule against decomposing strings.
    const r = Number.parseInt(value.slice(0, 1).repeat(2), 16);
    const g = Number.parseInt(value.slice(1, 2).repeat(2), 16);
    const b = Number.parseInt(value.slice(2, 3).repeat(2), 16);
    return [r, g, b].every((n) => Number.isFinite(n)) ? [r, g, b] : undefined;
  }
  if (value.length !== 6) return undefined;
  const r = Number.parseInt(value.slice(0, 2), 16);
  const g = Number.parseInt(value.slice(2, 4), 16);
  const b = Number.parseInt(value.slice(4, 6), 16);
  return [r, g, b].every((n) => Number.isFinite(n)) ? [r, g, b] : undefined;
}

/** WCAG 2.x relative luminance of an sRGB colour. */
export function relativeLuminance(hex: string): number {
  const rgb = parseHex(hex);
  if (rgb === undefined) return 0;
  const linear = (channel: number): number => {
    const s = channel / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * linear(rgb[0]) + 0.7152 * linear(rgb[1]) + 0.0722 * linear(rgb[2]);
}

/**
 * The WCAG contrast ratio between two colours, from 1 to 21.
 *
 * `(L1 + 0.05) / (L2 + 0.05)` with the lighter colour on top — the formula from
 * WCAG 2.2's definition of contrast ratio, implemented rather than imported
 * because it is nine lines and a dependency for nine lines is a dependency for
 * nine lines.
 */
export function contrastRatio(a: string, b: string): number {
  const la = relativeLuminance(a);
  const lb = relativeLuminance(b);
  const lighter = Math.max(la, lb);
  const darker = Math.min(la, lb);
  return (lighter + 0.05) / (darker + 0.05);
}

/** WCAG 2.2 AA for normal text. */
export const AA_NORMAL_TEXT = 4.5;

/** True when a pair meets AA for normal text. */
export function meetsAA(background: string, text: string): boolean {
  return contrastRatio(background, text) >= AA_NORMAL_TEXT;
}
