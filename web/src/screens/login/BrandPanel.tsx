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
      /* data-brand-panel lets the login layout target this element across the
       * CSS-Module boundary: LoginScreen.module.css needs to flip the panel
       * above the form on collapsed viewports, and hashed class names from
       * another module are not addressable from there. */
      data-brand-panel=""
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

      {/*
        The scrim belongs to the GRADIENT-ONLY case.

        Over a photograph it is still a colour effect — the owner read the
        bottom darkening as a tint on their own picture, which is what it is.
        With an image the panel paints nothing over it and legibility comes
        from a text-shadow on the content instead (BrandPanel.module.css).
        Without one there is no picture to protect, the two stops are the
        brand's own colours, and the scrim is what keeps the mark legible
        against a light gradient.
      */}
      {!hasImage && <div className={styles.scrim} aria-hidden="true" />}

      <div className={styles.content}>
        {/*
          `plated` is suppressed over a photograph. The plate is a light
          rectangle the product puts behind a dark logo so it stays legible on
          the gradient — useful when WE chose the background, wrong when the
          operator did: they picked this photograph AND this logo and can see
          whether the pair works, and a plate they did not ask for is chrome on
          top of their composition.
        */}
        <BrandMark branding={branding} size="lg" onDark plated={!hasImage} />
        {branding.tagline !== "" && <p className={styles.tagline}>{branding.tagline}</p>}
      </div>
    </aside>
  );
}
