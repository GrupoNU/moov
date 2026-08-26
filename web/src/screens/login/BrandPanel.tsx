import type { Branding } from "../../branding/branding";
import { BrandMark } from "../../components/BrandMark";
import styles from "./BrandPanel.module.css";

/**
 * The imagery half of the split screen (product decision P2).
 *
 * # What it shows, in order of preference
 *
 *   1. The customer's splash image, over their gradient.
 *   2. Just the gradient, when no image is configured — which is the default
 *      case and must look deliberate rather than empty. The gradient is
 *      overlaid with a soft radial "aurora" and a fine grid, so an
 *      unconfigured install still gets a panel that looks designed.
 *
 * # The mobile collapse (P2 requires it; the choice is justified in web/README)
 *
 * The panel does NOT become a background image behind the form on narrow
 * screens. It collapses to a short brand BAND above the form: full-bleed
 * gradient, the mark, and nothing else.
 *
 * Reasoning: an image behind a form is the option that most often fails
 * accessibility — text contrast becomes a property of whatever photograph a
 * customer uploaded, and no overlay opacity is safe for every image. A band
 * keeps the brand visible, keeps the form on a plain surface where contrast is
 * a fixed, testable value, and guarantees the form never falls below the fold
 * (the band is `flex: none` at a fixed small height, so the form always owns
 * the remaining viewport).
 */

export interface BrandPanelProps {
  readonly branding: Branding;
}

export function BrandPanel({ branding }: BrandPanelProps): React.JSX.Element {
  const hasImage = branding.splashUrl !== "";

  return (
    <aside
      className={styles.panel}
      /*
       * The panel is presentation plus the product name. The name is genuine
       * content, so the panel is not aria-hidden; the decorative image and the
       * generated texture are hidden individually below.
       */
      data-has-image={hasImage ? "true" : "false"}
    >
      {/* The gradient is painted by CSS from the two brand seed colours. */}
      <div className={styles.gradient} aria-hidden="true" />

      {hasImage && (
        <img
          className={styles.image}
          src={branding.splashUrl}
          /*
           * Decorative by definition: it is a mood image chosen by the
           * customer, carrying no information the user needs. An invented
           * description would be noise in a screen reader.
           */
          alt=""
          aria-hidden="true"
          /* The largest element on the screen — it IS the LCP candidate, so it
           * is loaded eagerly with high priority rather than lazily. */
          loading="eager"
          fetchPriority="high"
          decoding="async"
        />
      )}

      {/* Texture, only when there is no photograph to sit on top of. */}
      {!hasImage && (
        <>
          <div className={styles.aurora} aria-hidden="true" />
          <div className={styles.grid} aria-hidden="true" />
        </>
      )}

      {/* A scrim under the content so text contrast is fixed regardless of
          which image a customer uploaded. */}
      <div className={styles.scrim} aria-hidden="true" />

      <div className={styles.content}>
        <BrandMark branding={branding} size="lg" onDark />
        {branding.tagline !== "" && <p className={styles.tagline}>{branding.tagline}</p>}
      </div>
    </aside>
  );
}
