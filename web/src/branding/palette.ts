/**
 * The brand palette: one customer hex in, two AA-guaranteed themes out.
 *
 * # Why this exists
 *
 * tokens.css used to derive every accent shade from `--brand-primary` with
 * `color-mix`, and nothing checked the result. A customer who configured a
 * pale mint got mint links on a white page at 1.3:1 and an app they could not
 * read — and `onPrimary` was trusted as sent, so a wrong value produced a
 * submit button with an invisible label. Contrast that is not asserted is
 * contrast that regresses, which is the same lesson `labelPalette.ts` already
 * applies to label chips; this module applies it to the brand itself.
 *
 * # The contract, pinned clause by clause in palette.test.ts
 *
 *   - The accent, used as TEXT (links, the active rail row), clears WCAG 2.2 AA
 *     for normal text (4.5:1) against BOTH surfaces of its theme: the default
 *     surface and the canvas.
 *   - `onAccent` clears 4.5:1 on the accent AND on its hover and active steps,
 *     so a button label never disappears mid-press.
 *   - The tints are alpha washes of the accent (the `color-mix(... transparent)`
 *     semantics tokens.css had, expressed as rgba so CSS needs no arithmetic),
 *     and body text over the strong tint on the default surface stays AA.
 *   - When the customer's primary cannot satisfy the constraints as it is, it
 *     is ADJUSTED — lightness moved in OKLCH toward the constraint, hue kept,
 *     chroma kept as far as the sRGB gamut allows — and the adjustment is
 *     DECLARED in {@link BrandPalette.adjusted} with a reason a person can read.
 *     The customer's `onPrimary` is a hint: it is used when it clears the
 *     constraint against the final accent, and replaced (and declared) when
 *     it does not.
 *   - A primary that already passes is returned EXACTLY, so Moov's own
 *     `#5b5bd6` never shifts.
 *   - The TONAL family — `accentContainer` / `onAccentContainer` /
 *     `selectedRow` / `activePill`, Gmail's three related tones of one accent
 *     — is derived from the ORIGINAL primary's hue and chroma at fixed
 *     lightness targets, never from the adjusted accent. That is the fix for
 *     the defect that motivated it: a pastel `#b8faff` is adjusted to a deep
 *     teal for TEXT, and containers derived from the teal erased the brand's
 *     own hue from the chrome. Text on each of these clears 4.5:1, and the
 *     chroma is reduced toward neutral until it does.
 *   - Deterministic: same input, same output, no randomness, no environment.
 *
 * # Why OKLCH and not "mix with black"
 *
 * Mixing toward black or white moves hue and saturation along with lightness:
 * a saturated yellow mixed 40% toward black is an olive nobody chose. OKLCH
 * lets lightness move alone, so an adjusted brand still reads as the
 * customer's colour, only deep enough (or bright enough) to be legible. The
 * conversion is ~40 lines (Björn Ottosson's published matrices) and pulling a
 * colour library in for it would be a dependency for forty lines.
 *
 * # PURE
 *
 * No DOM, no imports beyond the contrast helpers this repo already has. It runs
 * identically in the provider, in tests, and in the tokens.css pin.
 */

import { AA_NORMAL_TEXT, contrastRatio, parseHex } from "../mail/labelPalette";

/** The seven values a theme needs from the brand. */
export interface ThemePalette {
  /** The accent, as it is used for text and for solid surfaces. */
  readonly accent: string;
  readonly accentHover: string;
  readonly accentActive: string;
  /** Text and icons ON the accent (and on its hover/active steps). */
  readonly onAccent: string;
  /** A light alpha wash of the accent, as an `rgba()` string. */
  readonly accentTint: string;
  /** A stronger alpha wash of the accent, as an `rgba()` string. */
  readonly accentTintStrong: string;
  /** The keyboard focus ring. Follows the accent. */
  readonly focusRing: string;

  /*
   * The TONAL family (Gmail's arrangement).
   *
   * The values above are the accent as INK: lightness-adjusted until they
   * clear 4.5:1 as TEXT, which is why a pastel brand becomes a deep teal. The
   * four below are the accent as CONTAINER, and a container carries no such
   * constraint — what sits on it does. So they are derived from the ORIGINAL
   * primary's hue and chroma at fixed lightness targets, which is what keeps a
   * light-cyan brand looking like light cyan in the chrome while its links
   * stay legible. They are OPAQUE, so their contrast is the contrast they
   * appear to have rather than one that depends on what scrolls behind.
   */

  /** The compose button's fill: a light tonal container of the brand hue. */
  readonly accentContainer: string;
  /** Text and icons ON {@link accentContainer}, at 4.5:1 or better. */
  readonly onAccentContainer: string;
  /** The selected message row: the faintest of the three tones. */
  readonly selectedRow: string;
  /** The active folder pill in the rail: between the other two. */
  readonly activePill: string;
}

/** Which theme a value belongs to. */
export type ThemeName = "light" | "dark";

/** The full palette: both themes plus what was changed, and why. */
export interface BrandPalette {
  readonly light: ThemePalette;
  readonly dark: ThemePalette;
  /**
   * Per theme, a human-readable reason when the customer's colours could not
   * be used as sent. Empty when the primary and onPrimary passed as they were.
   */
  readonly adjusted: { readonly light?: string; readonly dark?: string };
}

/**
 * The surfaces the accent must read against, per theme, DUPLICATED from
 * tokens.css on purpose: this module is pure and cannot read a stylesheet.
 * `palette.test.ts` reads tokens.css as text and fails when the two drift.
 */
export interface ThemeSurfaces {
  readonly surfaceDefault: string;
  readonly surfaceCanvas: string;
  /** `--text-default` of the theme: the body text that sits on a tinted row. */
  readonly textDefault: string;
  /** `--text-strong` of the theme: the weight the active pill's label uses. */
  readonly textStrong: string;
  /** OKLCH lightness targets for the three opaque tonal containers. */
  readonly containerL: number;
  readonly onContainerL: number;
  readonly selectedRowL: number;
  readonly activePillL: number;
  /** Alpha of the light tint and of the strong tint. */
  readonly tintAlpha: number;
  readonly tintStrongAlpha: number;
  /** Which way lightness moves when the primary needs adjusting. */
  readonly direction: -1 | 1;
}

export const THEME_SURFACES: Readonly<Record<ThemeName, ThemeSurfaces>> = {
  light: {
    surfaceDefault: "#ffffff",
    surfaceCanvas: "#f6f7fb",
    textDefault: "#2b2f3d",
    textStrong: "#12141d",
    tintAlpha: 0.1,
    tintStrongAlpha: 0.18,
    containerL: 0.9,
    onContainerL: 0.25,
    selectedRowL: 0.96,
    activePillL: 0.92,
    direction: -1,
  },
  dark: {
    surfaceDefault: "#151824",
    surfaceCanvas: "#0d0f17",
    textDefault: "#dfe3ee",
    textStrong: "#f4f6fb",
    tintAlpha: 0.22,
    tintStrongAlpha: 0.32,
    containerL: 0.32,
    onContainerL: 0.92,
    selectedRowL: 0.22,
    activePillL: 0.28,
    direction: 1,
  },
};

/**
 * The two candidates for text on the accent when the customer's hint fails.
 * Near-black is the dark theme's canvas so an accent button on a dark page
 * reads as a cut-out of it, which is what the previous fixed value was too.
 */
export const ON_ACCENT_DARK = "#0d0f17";
export const ON_ACCENT_LIGHT = "#ffffff";

/** How far hover and active step away from the accent, in OKLCH lightness. */
const HOVER_STEP = 0.07;
const ACTIVE_STEP = 0.14;
/** Below this OKLCH lightness the light theme's hover/active go lighter. */
const REVERSE_BELOW = 0.35;
/** Above this OKLCH lightness the dark theme's hover/active go darker. */
const REVERSE_ABOVE = 0.86;
/** Where an upward step starts when the accent is deeper than this. */
const STEP_UP_FLOOR = 0.25;

// ---------------------------------------------------------------------------
// sRGB <-> OKLCH
// ---------------------------------------------------------------------------

interface Oklch {
  readonly l: number;
  readonly c: number;
  readonly h: number;
}

function srgbToLinear(channel: number): number {
  const s = channel / 255;
  return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
}

function linearToSrgb(linear: number): number {
  const v = linear <= 0.0031308 ? linear * 12.92 : 1.055 * linear ** (1 / 2.4) - 0.055;
  return v * 255;
}

/** sRGB (0-255) to OKLCH. Hue in degrees, 0 for an achromatic colour. */
export function hexToOklch(hex: string): Oklch {
  const rgb = parseHex(hex) ?? [0, 0, 0];
  const r = srgbToLinear(rgb[0]);
  const g = srgbToLinear(rgb[1]);
  const b = srgbToLinear(rgb[2]);

  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b);
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b);
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b);

  const L = 0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s;
  const a = 1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s;
  const bb = 0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s;

  const c = Math.hypot(a, bb);
  // Below this chroma the hue is numerical noise; call it grey.
  const h = c < 1e-4 ? 0 : ((Math.atan2(bb, a) * 180) / Math.PI + 360) % 360;
  return { l: L, c, h };
}

/** OKLCH to linear-light sRGB channels, UNCLAMPED (used for the gamut test). */
function oklchToLinearRgb({ l: L, c, h }: Oklch): readonly [number, number, number] {
  const rad = (h * Math.PI) / 180;
  const a = c * Math.cos(rad);
  const bb = c * Math.sin(rad);

  const l = (L + 0.3963377774 * a + 0.2158037573 * bb) ** 3;
  const m = (L - 0.1055613458 * a - 0.0638541728 * bb) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * bb) ** 3;

  return [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ];
}

function inGamut(color: Oklch): boolean {
  const eps = 1e-4;
  return oklchToLinearRgb(color).every((v) => v >= -eps && v <= 1 + eps);
}

/**
 * Brings a colour into the sRGB gamut by REDUCING CHROMA at fixed lightness
 * and hue — the perceptual equivalent of "the same colour, slightly less
 * vivid". Clipping channels instead would shift hue and lightness, and
 * lightness is the one axis the contrast constraint is steering.
 */
function toGamut(color: Oklch): Oklch {
  const clamped = { ...color, l: Math.min(1, Math.max(0, color.l)) };
  if (inGamut(clamped)) return clamped;
  let lo = 0;
  let hi = clamped.c;
  for (let i = 0; i < 32; i++) {
    const mid = (lo + hi) / 2;
    if (inGamut({ ...clamped, c: mid })) lo = mid;
    else hi = mid;
  }
  return { ...clamped, c: lo };
}

function channelToHex(value: number): string {
  const clamped = Math.min(255, Math.max(0, Math.round(value)));
  return clamped.toString(16).padStart(2, "0");
}

/** OKLCH to a lowercase `#rrggbb`, gamut-mapped first. */
export function oklchToHex(color: Oklch): string {
  const [r, g, b] = oklchToLinearRgb(toGamut(color));
  return `#${channelToHex(linearToSrgb(r))}${channelToHex(linearToSrgb(g))}${channelToHex(linearToSrgb(b))}`;
}

/** `#abc` or `#AABBCC` to the canonical lowercase six-digit form. */
export function normalizeHex(hex: string): string {
  const rgb = parseHex(hex) ?? [0, 0, 0];
  return `#${channelToHex(rgb[0])}${channelToHex(rgb[1])}${channelToHex(rgb[2])}`;
}

// ---------------------------------------------------------------------------
// the constraints
// ---------------------------------------------------------------------------

function readsOn(surfaces: ThemeSurfaces, hex: string): boolean {
  return (
    contrastRatio(hex, surfaces.surfaceDefault) >= AA_NORMAL_TEXT &&
    contrastRatio(hex, surfaces.surfaceCanvas) >= AA_NORMAL_TEXT
  );
}

/** The smallest contrast a text colour has across a set of grounds. */
function worstContrast(text: string, grounds: readonly string[]): number {
  return Math.min(...grounds.map((ground) => contrastRatio(text, ground)));
}

/**
 * Moves lightness from `from` toward `limit` (0 or 1) and returns the hex
 * CLOSEST to the original that satisfies `passes`. Bisection over 40 steps is
 * far below hex resolution; the predicate is evaluated on the rounded hex so
 * the value returned is the value that passed, not a real number near it.
 */
function fitLightness(
  base: Oklch,
  limit: 0 | 1,
  passes: (hex: string) => boolean,
): string {
  let failing = base.l;
  let passing: number = limit;
  for (let i = 0; i < 40; i++) {
    const mid = (failing + passing) / 2;
    if (passes(oklchToHex({ ...base, l: mid }))) passing = mid;
    else failing = mid;
  }
  return oklchToHex({ ...base, l: passing });
}

/**
 * A hover or active step: lightness moved by `step` in the theme's direction
 * (deeper in light, brighter in dark) — or the other way when the accent sits
 * near the end of the scale. A black accent has no darker hover, and OKLCH
 * lightness is so compressed at the sRGB toe that a step down from L=0.2 is a
 * one-level change nobody can see; so below {@link REVERSE_BELOW} the light
 * theme steps UP, and above {@link REVERSE_ABOVE} the dark theme steps DOWN.
 * Both reversals keep `onAccent` legible (the test sweeps them).
 */
function stepped(accent: Oklch, step: number, direction: -1 | 1): string {
  const reversed = direction === -1 ? accent.l < REVERSE_BELOW : accent.l > REVERSE_ABOVE;
  const sign = reversed ? -direction : direction;
  // Stepping UP out of the toe starts from a floor: from pure black, L=0.07
  // is still #010101. From L=0.25 the same step is a visible grey.
  const from = sign === 1 ? Math.max(accent.l, STEP_UP_FLOOR) : accent.l;
  return oklchToHex({ ...accent, l: from + sign * step });
}

// ---------------------------------------------------------------------------
// the tonal containers
// ---------------------------------------------------------------------------

/**
 * The most chroma a tonal container may carry, per theme.
 *
 * A container is a large flat field behind text; at full brand chroma a light
 * one glows and a dark one turns into a colour block that fights the message
 * it holds. Gmail's own containers sit far below its accent's chroma. These
 * ceilings are what make the three tones read as tones of one colour rather
 * than three saturated fills.
 */
const CONTAINER_CHROMA_MAX = 0.06;
const ON_CONTAINER_CHROMA_MAX = 0.07;
/** How far a tone's chroma is pulled toward neutral on each retry. */
const CHROMA_DECAY = 0.75;

/**
 * A tonal container: the ORIGINAL primary's hue and chroma at a fixed OKLCH
 * lightness, with chroma capped and then reduced toward neutral until every
 * `passes` predicate holds.
 *
 * # Why the ORIGINAL primary and not the adjusted accent
 *
 * This is the whole point of the family. A pastel like `#b8faff` cannot be
 * used as text on white, so `accent` is lightness-dropped into a deep teal —
 * and if the containers derived from THAT, the customer's own hue would
 * vanish from the chrome, which is exactly the defect this fixes. Lightness is
 * the axis the AA constraint steers, and a container's lightness is already
 * pinned by its target; only its chroma is free, so chroma is what gives.
 *
 * Reducing chroma (rather than moving lightness) also keeps the three tones at
 * their designed distances from each other: the row stays fainter than the
 * pill, the pill fainter than the button, whatever the brand hue is.
 */
function tonal(
  base: Oklch,
  lightness: number,
  chromaMax: number,
  passes: (hex: string) => boolean,
): string {
  let chroma = Math.min(base.c, chromaMax);
  // 24 decays at 0.75 take any chroma below 1e-3, i.e. to a neutral grey of
  // the target lightness — which always passes, because the targets were
  // chosen against the theme's surfaces. So this loop terminates on a value
  // that satisfies the predicate rather than on an exhausted budget.
  for (let i = 0; i < 24; i++) {
    const hex = oklchToHex({ ...base, l: lightness, c: chroma });
    if (passes(hex)) return hex;
    chroma *= CHROMA_DECAY;
  }
  return oklchToHex({ ...base, l: lightness, c: 0 });
}

function rgba(hex: string, alpha: number): string {
  const [r, g, b] = parseHex(hex) ?? [0, 0, 0];
  return `rgba(${String(r)}, ${String(g)}, ${String(b)}, ${String(alpha)})`;
}

interface ThemeResult {
  readonly palette: ThemePalette;
  readonly reason: string | undefined;
}

function deriveTheme(
  theme: ThemeName,
  primary: string,
  onPrimary: string | undefined,
): ThemeResult {
  const surfaces = THEME_SURFACES[theme];
  const reasons: string[] = [];

  // 1. The accent: the primary itself when it reads, else the nearest
  //    lightness along the theme's direction that does.
  let accent = primary;
  if (!readsOn(surfaces, primary)) {
    const base = hexToOklch(primary);
    accent = fitLightness(base, surfaces.direction === -1 ? 0 : 1, (hex) =>
      readsOn(surfaces, hex),
    );
    const worst = Math.min(
      contrastRatio(primary, surfaces.surfaceDefault),
      contrastRatio(primary, surfaces.surfaceCanvas),
    );
    reasons.push(
      `${primary} reads at ${worst.toFixed(2)}:1 on the ${theme} surfaces; ` +
        `lightness ${surfaces.direction === -1 ? "lowered" : "raised"} to ${accent} ` +
        `to reach ${String(AA_NORMAL_TEXT)}:1 (WCAG AA), hue kept.`,
    );
  }

  // 2. Hover and active, as lightness steps from the FINAL accent.
  const accentLch = hexToOklch(accent);
  const accentHover = stepped(accentLch, HOVER_STEP, surfaces.direction);
  const accentActive = stepped(accentLch, ACTIVE_STEP, surfaces.direction);
  const grounds = [accent, accentHover, accentActive];

  // 3. Text on the accent: the hint when it clears every ground, otherwise
  //    whichever of near-black / near-white has the better worst case.
  let onAccent: string;
  if (onPrimary !== undefined && worstContrast(onPrimary, grounds) >= AA_NORMAL_TEXT) {
    onAccent = onPrimary;
  } else {
    onAccent =
      worstContrast(ON_ACCENT_DARK, grounds) >= worstContrast(ON_ACCENT_LIGHT, grounds)
        ? ON_ACCENT_DARK
        : ON_ACCENT_LIGHT;
    if (onPrimary !== undefined) {
      reasons.push(
        `onPrimary ${onPrimary} reads at ${worstContrast(onPrimary, grounds).toFixed(2)}:1 ` +
          `on the ${theme} accent; replaced with ${onAccent}.`,
      );
    }
  }

  // 4. The tonal containers, from the ORIGINAL primary's hue and chroma.
  //
  //    `selectedRow` and `activePill` must hold BODY text (a message row's
  //    sender and subject, a folder's name) and the pill also holds
  //    `--text-strong`; both are pinned at AA here rather than hoped for.
  //    `accentContainer` carries `onAccentContainer`, which is derived from
  //    the same hue and falls back to the theme's strong text when the hue
  //    cannot reach 4.5:1 on it — a brand never buys an unreadable button.
  const primaryLch = hexToOklch(primary);
  const selectedRow = tonal(primaryLch, surfaces.selectedRowL, CONTAINER_CHROMA_MAX, (hex) =>
    contrastRatio(surfaces.textDefault, hex) >= AA_NORMAL_TEXT,
  );
  const activePill = tonal(
    primaryLch,
    surfaces.activePillL,
    CONTAINER_CHROMA_MAX,
    (hex) =>
      contrastRatio(surfaces.textDefault, hex) >= AA_NORMAL_TEXT &&
      contrastRatio(surfaces.textStrong, hex) >= AA_NORMAL_TEXT,
  );
  const accentContainer = tonal(
    primaryLch,
    surfaces.containerL,
    CONTAINER_CHROMA_MAX,
    // The container itself only has to be able to CARRY text: the fallback ink
    // below is the theme's strong text, so the ceiling this checks is the one
    // that guarantees the button is never unreadable.
    (hex) => contrastRatio(surfaces.textStrong, hex) >= AA_NORMAL_TEXT,
  );
  const tintedInk = tonal(
    primaryLch,
    surfaces.onContainerL,
    ON_CONTAINER_CHROMA_MAX,
    (hex) => contrastRatio(hex, accentContainer) >= AA_NORMAL_TEXT,
  );
  const onAccentContainer =
    contrastRatio(tintedInk, accentContainer) >= AA_NORMAL_TEXT
      ? tintedInk
      : surfaces.textStrong;

  return {
    palette: {
      accent,
      accentHover,
      accentActive,
      onAccent,
      accentTint: rgba(accent, surfaces.tintAlpha),
      accentTintStrong: rgba(accent, surfaces.tintStrongAlpha),
      focusRing: accent,
      accentContainer,
      onAccentContainer,
      selectedRow,
      activePill,
    },
    reason: reasons.length > 0 ? reasons.join(" ") : undefined,
  };
}

/**
 * Derives both themes from a customer's primary (and optional onPrimary hint).
 *
 * Inputs are `#rgb` or `#rrggbb` in any case; a malformed value is read as
 * black rather than thrown on, because this runs on the login screen and a
 * palette built from black is legible while an exception is a blank page.
 * (The merge upstream already refuses anything that is not a hex literal.)
 */
export function derivePalette(primary: string, onPrimary?: string): BrandPalette {
  const base = normalizeHex(primary);
  const hint = onPrimary === undefined ? undefined : normalizeHex(onPrimary);

  const light = deriveTheme("light", base, hint);
  const dark = deriveTheme("dark", base, hint);

  return {
    light: light.palette,
    dark: dark.palette,
    adjusted: {
      ...(light.reason !== undefined ? { light: light.reason } : {}),
      ...(dark.reason !== undefined ? { dark: dark.reason } : {}),
    },
  };
}

/**
 * Composites `top` (with `alpha`) over `ground` in sRGB, the way a browser
 * paints an rgba() background over an opaque one. Exported for the test that
 * pins body text over the tint.
 */
export function compositeOver(topHex: string, alpha: number, groundHex: string): string {
  const top = parseHex(topHex) ?? [0, 0, 0];
  const ground = parseHex(groundHex) ?? [255, 255, 255];
  const mix = (i: 0 | 1 | 2): string => channelToHex(top[i] * alpha + ground[i] * (1 - alpha));
  return `#${mix(0)}${mix(1)}${mix(2)}`;
}

/**
 * How far the derived login-panel gradient stops sit from the primary, as a
 * fraction of the way to black. Mirrors Go's `branding.SplashFromMixToward` /
 * `SplashToMixToward`; a test pins the two implementations to the same table.
 */
export const SPLASH_FROM_MIX_TOWARD_BLACK = 0.7;
export const SPLASH_TO_MIX_TOWARD_BLACK = 0.35;

/**
 * The two login-panel gradient stops a brand gets when it configured a primary
 * and no gradient of its own: the primary mixed toward black.
 *
 * # Why this exists in the client at all
 *
 * The SERVER is the authority — `GET /branding` already serves the derived
 * values, so the login screen and the app never compute this. This copy is for
 * the brand PANEL, which has to show an administrator what their gradient will
 * become BEFORE they save, and as the placeholder in the two fields they have
 * not filled in. Asking the server would mean a round trip per keystroke of a
 * colour picker.
 *
 * # Why a straight sRGB mix rather than {@link derivePalette}'s OKLCH
 *
 * Different job. The accent derivation moves lightness in OKLCH because its
 * output must stay recognizably the customer's colour while clearing a contrast
 * threshold. These two are a decorative backdrop with no contrast constraint of
 * their own (the panel's scrim owns legibility), and a channel mix is the
 * operation an operator can check with a calculator — which matters when the
 * value is offered to them as a placeholder they may override.
 */
export function deriveSplashColors(primary: string): {
  readonly from: string;
  readonly to: string;
} {
  /*
   * The leading `#` is required HERE even though `parseHex` treats it as
   * optional, because this function has to agree with Go's
   * `branding.DeriveSplashColors` exactly and that one refuses a bare `5b5bd6`
   * — the server's own `NormalizeHexColor` does. A client that accepted one
   * more input than the server would show a placeholder for a value the server
   * is about to reject.
   */
  const rgb = /^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/.test(primary.trim())
    ? parseHex(primary)
    : undefined;
  if (rgb === undefined) return { from: "", to: "" };
  const mix = (amount: number): string => {
    const keep = 1 - amount;
    return `#${channelToHex(rgb[0] * keep)}${channelToHex(rgb[1] * keep)}${channelToHex(rgb[2] * keep)}`;
  };
  return {
    from: mix(SPLASH_FROM_MIX_TOWARD_BLACK),
    to: mix(SPLASH_TO_MIX_TOWARD_BLACK),
  };
}
