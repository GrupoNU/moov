import type { Branding } from "../branding/branding";
import styles from "./BrandMark.module.css";

/**
 * The brand's visual mark: a customer's logo when they have one, Moov's own
 * wordmark when they do not.
 *
 * # Why the fallback is drawn rather than shipped as an image
 *
 * Most installations never configure a brand (L2-pwa §2, P3), so this fallback
 * is what MOST people see — it has to be good, not a placeholder. Drawing it as
 * inline SVG rather than shipping a PNG means it is crisp at every density,
 * costs no extra request on the critical path of the login screen, and — the
 * part that matters for W-A2 — it inherits `currentColor`, so it recolours with
 * the brand instead of being a fixed-colour bitmap that clashes with it.
 *
 * The mark itself: a rounded square with an "M" cut through it as a continuous
 * stroke, suggesting a path/route (the product is "Moov"). It reads at 24px.
 *
 * # A logo REPLACES the name; it does not accompany it
 *
 * When a customer has a logo, it IS their wordmark — it already says the name,
 * graphically. Rendering the text name beside it printed the brand twice, and
 * on the login panel (where the content column is capped at 44ch) the duplicate
 * then ellipsised, so a real pilot brand's panel read "[LOGO] Área …". Gmail
 * shows its mark alone for the same reason.
 *
 * So the name is rendered as TEXT only beside the drawn glyph, where it is
 * doing real work (the glyph is an abstract mark that names nothing). With a
 * logo the name lives in the image's `alt`, which is where a screen reader
 * wants it anyway.
 *
 * # Why a customer's logo is sized by HEIGHT alone
 *
 * The drawn fallback is square; an uploaded logo is usually not. Sizing a
 * customer's image to a square box distorts every wordmark that goes through
 * it, which is the single most visible way a white-label product can look
 * broken. So the logo is given a fixed height, a free width, `object-fit:
 * contain`, and a per-size `max-width` ceiling — the brand may be any shape it
 * likes, within a width the layout has already budgeted for.
 *
 * # Dark contexts, without asking JavaScript what the theme is
 *
 * A wordmark is usually one flat colour, and the common upload is a dark one.
 * A pilot brand's is pure black: invisible on the login panel's dark gradient,
 * and invisible in the dark theme's top bar. Two cases, handled without a
 * single theme read in JS:
 *
 *   - The customer supplied `logoDarkUrl`. BOTH images are rendered and CSS
 *     picks one, using the same three-state pattern tokens.css uses
 *     (`[data-theme="dark"]`, plus a `prefers-color-scheme` block guarded with
 *     `:not([data-theme="light"])`). A JS theme check would be a second source
 *     of truth for something CSS already knows, and would flash the wrong logo
 *     on first paint.
 *   - They did not. The light logo is drawn on a small light PLATE — a
 *     surface-coloured rounded rectangle behind it — so a black wordmark stays
 *     legible. Recolouring the logo instead (a CSS filter, a blend mode) is the
 *     obvious-looking option and it is wrong: it mangles any logo that is not a
 *     flat silhouette, which is most of them.
 */

export interface BrandMarkProps {
  readonly branding: Branding;
  readonly size?: "sm" | "md" | "lg";
  /**
   * Renders the mark alone, without the product name beside it. Only affects
   * the FALLBACK glyph: a customer's logo is always alone, because the logo
   * already is the name.
   */
  readonly iconOnly?: boolean;
  /**
   * Inverts the colours for use on the brand panel's dark gradient, where the
   * normal accent-on-surface pairing would have no contrast. Also selects the
   * dark logo variant, because that gradient is dark in every theme.
   */
  readonly onDark?: boolean;
}

/**
 * The rendered height of the logo at each size, mirroring BrandMark.module.css.
 *
 * Duplicated in TS because the `height` ATTRIBUTE is what reserves the box
 * before any stylesheet or image has loaded, and an attribute cannot read a
 * class. A test pins these against the stylesheet so the two cannot drift.
 */
const SIZE_HEIGHTS: Record<NonNullable<BrandMarkProps["size"]>, number> = {
  sm: 28,
  md: 32,
  lg: 44,
};

export function BrandMark({
  branding,
  size = "md",
  iconOnly = false,
  onDark = false,
}: BrandMarkProps): React.JSX.Element {
  const hasLogo = branding.logoUrl !== "";

  /*
   * The plate is the no-dark-variant fallback, and it is only needed where the
   * background is actually dark. `onDark` is one such place unconditionally
   * (the brand panel's gradient is dark in every theme); the other is the dark
   * THEME, which this component cannot see — so the class is emitted and the
   * stylesheet decides whether it paints, exactly like the image swap.
   */
  const needsPlate = hasLogo && branding.logoDarkUrl === "";

  const classes = [
    styles.mark,
    styles[size],
    onDark ? styles.onDark : "",
    needsPlate ? styles.plated : "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <span className={classes}>
      {hasLogo ? (
        <LogoImages branding={branding} size={size} onDark={onDark} />
      ) : (
        <>
          <MoovGlyph />
          {!iconOnly && <span className={styles.name}>{branding.name}</span>}
        </>
      )}
    </span>
  );
}

/**
 * The customer's logo: one `<img>`, or two when a dark variant exists.
 *
 * Two elements rather than one with a swapped `src`, because the swap has to
 * happen in CSS (see the component's docs). `<picture>` with a
 * `prefers-color-scheme` media source would cover the media query but NOT the
 * explicit `data-theme` attribute, and that is a class of user this app has to
 * serve: someone who chose dark while their OS is light.
 */
function LogoImages({
  branding,
  size,
  onDark,
}: {
  readonly branding: Branding;
  readonly size: NonNullable<BrandMarkProps["size"]>;
  readonly onDark: boolean;
}): React.JSX.Element {
  const hasDarkLogo = branding.logoDarkUrl !== "";

  /*
   * The alt text is the product name, because the logo IS the product name
   * rendered graphically — a screen reader user must hear the brand, not
   * "logo". With two images only ONE may carry it: the other is the same
   * information in a different colour, so it is decorative by definition and
   * announcing it would make the brand be read out twice.
   */
  const shared = { height: SIZE_HEIGHTS[size], decoding: "async" } as const;

  if (!hasDarkLogo) {
    /*
     * On the dark panel this is a dark wordmark on a light plate, which is a
     * deliberate look rather than a fault; `onDark` does not change the source
     * because there is no other source to choose.
     */
    return (
      <img className={styles.logo} src={branding.logoUrl} alt={branding.name} {...shared} />
    );
  }

  /*
   * `onDark` is unconditional: the gradient is dark whatever the theme, so the
   * dark variant is the only correct one there and the light one must not be
   * rendered at all — a CSS-hidden sibling would still be fetched.
   */
  if (onDark) {
    return (
      <img
        className={styles.logo}
        src={branding.logoDarkUrl}
        alt={branding.name}
        {...shared}
      />
    );
  }

  return (
    <>
      <img
        className={[styles.logo, styles.logoLight].join(" ")}
        src={branding.logoUrl}
        alt={branding.name}
        {...shared}
      />
      <img
        className={[styles.logo, styles.logoDark].join(" ")}
        src={branding.logoDarkUrl}
        alt=""
        aria-hidden="true"
        {...shared}
      />
    </>
  );
}

/** Moov's own mark, inheriting currentColor. */
function MoovGlyph(): React.JSX.Element {
  return (
    <svg
      className={styles.glyph}
      viewBox="0 0 32 32"
      /* Decorative: the product name is rendered as text beside it, and when
       * it is not, the caller passes it as this element's accessible name. */
      aria-hidden="true"
      focusable="false"
    >
      <rect x="1" y="1" width="30" height="30" rx="8.5" className={styles.glyphPlate} />
      <path
        d="M9 22V11.4c0-.5.6-.7.9-.3l5.4 6.6c.35.43 1 .43 1.35 0l5.4-6.6c.32-.4.95-.17.95.33V22"
        fill="none"
        stroke="currentColor"
        strokeWidth="2.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
