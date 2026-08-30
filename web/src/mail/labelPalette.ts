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
}

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
const SLATE: LabelColor = {
  id: "slate",
  light: "#e2e8f0",
  lightText: "#1e293b",
  dark: "#334155",
  darkText: "#e2e8f0",
};

export const LABEL_COLORS: readonly LabelColor[] = [
  SLATE,
  { id: "red", light: "#fee2e2", lightText: "#7f1d1d", dark: "#7f1d1d", darkText: "#fee2e2" },
  { id: "orange", light: "#ffedd5", lightText: "#7c2d12", dark: "#7c2d12", darkText: "#ffedd5" },
  { id: "amber", light: "#fef3c7", lightText: "#78350f", dark: "#78350f", darkText: "#fef3c7" },
  { id: "lime", light: "#ecfccb", lightText: "#365314", dark: "#365314", darkText: "#ecfccb" },
  { id: "green", light: "#dcfce7", lightText: "#14532d", dark: "#14532d", darkText: "#dcfce7" },
  { id: "teal", light: "#ccfbf1", lightText: "#134e4a", dark: "#134e4a", darkText: "#ccfbf1" },
  { id: "cyan", light: "#cffafe", lightText: "#164e63", dark: "#164e63", darkText: "#cffafe" },
  { id: "blue", light: "#dbeafe", lightText: "#1e3a8a", dark: "#1e3a8a", darkText: "#dbeafe" },
  { id: "indigo", light: "#e0e7ff", lightText: "#312e81", dark: "#312e81", darkText: "#e0e7ff" },
  { id: "purple", light: "#f3e8ff", lightText: "#581c87", dark: "#581c87", darkText: "#f3e8ff" },
  { id: "pink", light: "#fce7f3", lightText: "#831843", dark: "#831843", darkText: "#fce7f3" },
];

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
