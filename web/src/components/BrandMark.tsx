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
 * # Why a customer's logo is sized by HEIGHT alone
 *
 * The drawn fallback is square; an uploaded logo is usually not. Sizing a
 * customer's image to a square box distorts every wordmark that goes through
 * it, which is the single most visible way a white-label product can look
 * broken. So the logo is given a fixed height, a free width, `object-fit:
 * contain`, and a per-size `max-width` ceiling — the brand may be any shape it
 * likes, within a width the layout has already budgeted for.
 */

export interface BrandMarkProps {
  readonly branding: Branding;
  readonly size?: "sm" | "md" | "lg";
  /**
   * Renders the mark alone, without the product name beside it. Used where the
   * name is already present as a heading.
   */
  readonly iconOnly?: boolean;
  /**
   * Inverts the colours for use on the brand panel's dark gradient, where the
   * normal accent-on-surface pairing would have no contrast.
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
  const classes = [styles.mark, styles[size], onDark ? styles.onDark : ""]
    .filter(Boolean)
    .join(" ");

  return (
    <span className={classes}>
      {branding.logoUrl !== "" ? (
        <img
          className={styles.logo}
          src={branding.logoUrl}
          /*
           * The alt text is the product name, because the logo IS the product
           * name rendered graphically — a screen reader user must hear the
           * brand, not "logo". When the name is also rendered as text beside
           * it, the image becomes decorative and alt="" avoids a stutter.
           */
          alt={iconOnly ? branding.name : ""}
          /*
           * HEIGHT only, and it is deliberate.
           *
           * A logo is not square. Customers upload wordmarks — "ACME MAIL" at
           * 4:1 is the common case — and a `width` attribute alongside the
           * height is an aspect ratio the browser will honour, so it squashed
           * every one of them into a 32x32 box. The CSS gives the element a
           * fixed height, `width: auto` and `object-fit: contain`, so the
           * intrinsic ratio is what decides the width.
           *
           * The attribute stays for the CLS reserve it was added for: with a
           * height attribute and `width: auto` the browser still reserves a
           * line box of the right HEIGHT before the bytes arrive, and the CSS
           * `min-width` reserves a square's worth of horizontal space so a
           * slow logo grows sideways into room already held rather than
           * shoving what follows it. The value is per-size in CSS; this
           * attribute only has to be non-absurd for the pre-load reserve.
           */
          height={SIZE_HEIGHTS[size]}
          decoding="async"
        />
      ) : (
        <MoovGlyph />
      )}
      {!iconOnly && <span className={styles.name}>{branding.name}</span>}
    </span>
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
