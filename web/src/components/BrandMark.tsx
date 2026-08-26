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
          /* Reserve the box so a slow logo cannot shift the layout (CLS). */
          width={32}
          height={32}
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
