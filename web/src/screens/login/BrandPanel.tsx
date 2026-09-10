import { useCallback, useState } from "react";

import type { Branding } from "../../branding/branding";
import { BrandMark } from "../../components/BrandMark";
import {
  inkForLuminance,
  sampleContentLuminance,
  type SplashInk,
} from "./splashInk";
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
  /**
   * Measures the region behind the content block. Injected ONLY for tests:
   * jsdom has no 2D context, so the real sampler always answers undefined
   * there and the rule would be untestable through this component.
   */
  readonly sampleLuminance?: (image: HTMLImageElement) => number | undefined;
}

export function BrandPanel({
  branding,
  sampleLuminance = sampleContentLuminance,
}: BrandPanelProps): React.JSX.Element {
  const hasImage = branding.splashUrl !== "";
  /*
   * The panel draws its name, tagline and logo over the image unless the
   * operator turned that off — which they do when the artwork already carries
   * the brand, as the pilot owner's does. Without an image the text is the
   * only thing on the panel, so the switch does not apply: a gradient with
   * nothing on it is not a brand panel, it is a coloured rectangle.
   */
  const showText = !hasImage || branding.splashText;

  /*
   * The ink is MEASURED from the photograph, because there is no longer an
   * overlay to guarantee contrast — see splashInk.ts. It starts as "light",
   * which is what every panel wore before this and what a gradient still
   * wears, so nothing flashes on a dark image and the fallback for a
   * measurement that never arrives is the previous behaviour.
   */
  const [ink, setInk] = useState<SplashInk>("light");
  const measure = useCallback(
    (image: HTMLImageElement | null) => {
      if (image?.complete !== true) return;
      const luminance = sampleLuminance(image);
      if (luminance !== undefined) setInk(inkForLuminance(luminance));
    },
    [sampleLuminance],
  );
  const onImageLoad = useCallback(
    (event: React.SyntheticEvent<HTMLImageElement>) => {
      const luminance = sampleLuminance(event.currentTarget);
      if (luminance !== undefined) setInk(inkForLuminance(luminance));
    },
    [sampleLuminance],
  );

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
      /*
       * The ink is an ATTRIBUTE rather than a class so the stylesheet owns both
       * treatments and the component owns only the measurement. It is written
       * unconditionally — a gradient panel is "light" by construction — so a
       * rule never has to ask whether the attribute exists.
       */
      data-ink={ink}
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
          /* A cached image can be `complete` before React attaches onLoad, so
             the ref measures what is already there and the handler catches the
             rest — the same pairing BrandMark uses to size a logo. */
          ref={measure}
          onLoad={onImageLoad}
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

      {/*
        The whole content block goes when the artwork already carries the brand
        — the LOGO included. Keeping the mark and dropping only the words would
        still be a second logo on top of the one in the picture, which is the
        complaint.
      */}
      {showText && (
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
      )}
    </aside>
  );
}
