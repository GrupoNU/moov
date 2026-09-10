import { useCallback, useState } from "react";

import type { Branding } from "../branding/branding";
import {
  SIZE_HEIGHTS,
  logoShapeOf,
  type BrandMarkSize,
  type LogoShape,
} from "./brandMarkShape";
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
 * # A WIDE logo replaces the name; a SQUARE one cannot
 *
 * When a customer's logo is a wordmark, it IS their name — it already says it,
 * graphically. Rendering the text name beside a wordmark printed the brand
 * twice, and on the login panel (where the content column is capped) the
 * duplicate then ellipsised, so a real pilot brand's panel read
 * "[LOGO] Área …".
 *
 * A SQUARE mark is the opposite case, and it is the one the first owner of the
 * brand panel actually uploaded. A square glyph names nothing: shown alone at
 * a wordmark's height it is a postage stamp with no text anywhere on the
 * screen saying whose product this is. Gmail's own bar is the reference — an
 * "M" glyph WITH the word "Gmail" beside it — and that pairing is exactly what
 * a square mark needs.
 *
 * So the name comes back as text for a square mark, and stays out of the way
 * for a wide one. Where the line is drawn, and how it is measured, lives in
 * ./brandMarkShape.
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
  readonly size?: BrandMarkSize;
  /**
   * Renders the mark alone, without the product name beside it. Only affects
   * the FALLBACK glyph and a SQUARE logo — a wordmark is always alone, because
   * the wordmark already is the name.
   */
  readonly iconOnly?: boolean;
  /**
   * Inverts the colours for use on the brand panel's dark gradient, where the
   * normal accent-on-surface pairing would have no contrast. Also selects the
   * dark logo variant, because that gradient is dark in every theme.
   */
  readonly onDark?: boolean;
  /**
   * Whether the no-dark-variant PLATE may be drawn. Defaults to true.
   *
   * The plate is a light rectangle behind a dark logo so it stays legible on a
   * dark ground, and it is right wherever WE chose that ground. The login
   * panel with a customer's SPLASH PHOTOGRAPH is the case where we did not:
   * the operator picked both the picture and the logo and can see whether the
   * pair works, so a plate they did not ask for is chrome on top of their
   * composition. That caller passes false.
   */
  readonly plated?: boolean;
}

export function BrandMark({
  branding,
  size = "md",
  iconOnly = false,
  onDark = false,
  plated = true,
}: BrandMarkProps): React.JSX.Element {
  const hasLogo = branding.logoUrl !== "";

  /*
   * The shape is MEASURED, not configured.
   *
   * The alternative was a `logoShape` field on the branding document, and it
   * is worse in both directions: an operator would have to describe a file
   * they can see, and a wrong answer would be a layout bug nobody could
   * explain. The browser already knows the intrinsic size the moment the image
   * decodes; reading `naturalWidth/naturalHeight` on `load` is one line and
   * cannot disagree with the picture.
   *
   * BEFORE the load the assumption is WIDE, and that direction is deliberate:
   * a wordmark (the common upload) then renders at its final size immediately
   * and never moves, and a square mark grows once, downward-compatibly, into
   * space the flex row absorbs. Assuming square would make every wordmark
   * shrink on load — a shift on the most-visited screen in the product.
   */
  const [shape, setShape] = useState<LogoShape | undefined>(undefined);
  const measure = useCallback((image: HTMLImageElement | null) => {
    if (image === null) return;
    /*
     * A cached image can be `complete` before React ever attaches `onLoad`, so
     * the ref measures whatever is already there and the handler catches the
     * rest. Measuring in both places is what makes a reload behave like a
     * first visit.
     */
    if (image.complete) {
      const measured = logoShapeOf(image.naturalWidth, image.naturalHeight);
      if (measured !== undefined) setShape(measured);
    }
  }, []);
  const onLoad = useCallback((event: React.SyntheticEvent<HTMLImageElement>) => {
    const measured = logoShapeOf(event.currentTarget.naturalWidth, event.currentTarget.naturalHeight);
    if (measured !== undefined) setShape(measured);
  }, []);

  const effectiveShape: LogoShape = shape ?? "wide";

  /*
   * The plate is the no-dark-variant fallback, and it is only needed where the
   * background is actually dark. `onDark` is one such place unconditionally
   * (the brand panel's gradient is dark in every theme); the other is the dark
   * THEME, which this component cannot see — so the class is emitted and the
   * stylesheet decides whether it paints, exactly like the image swap.
   */
  const needsPlate = plated && hasLogo && branding.logoDarkUrl === "";

  /*
   * The name accompanies a SQUARE logo and the drawn glyph, and never a
   * wordmark. `iconOnly` suppresses it everywhere it could appear.
   */
  const showName = !iconOnly && (!hasLogo || effectiveShape === "square");

  const classes = [
    styles.mark,
    styles[size],
    styles[effectiveShape],
    onDark ? styles.onDark : "",
    needsPlate ? styles.plated : "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <span className={classes}>
      {hasLogo ? (
        <>
          <LogoImages
            branding={branding}
            size={size}
            shape={effectiveShape}
            onDark={onDark}
            onLoad={onLoad}
            measure={measure}
          />
          {showName && <span className={styles.name}>{branding.name}</span>}
        </>
      ) : (
        <>
          <MoovGlyph />
          {showName && <span className={styles.name}>{branding.name}</span>}
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
  shape,
  onDark,
  onLoad,
  measure,
}: {
  readonly branding: Branding;
  readonly size: BrandMarkSize;
  readonly shape: LogoShape;
  readonly onDark: boolean;
  readonly onLoad: (event: React.SyntheticEvent<HTMLImageElement>) => void;
  readonly measure: (image: HTMLImageElement | null) => void;
}): React.JSX.Element {
  const hasDarkLogo = branding.logoDarkUrl !== "";

  /*
   * The alt text is the product name when the logo stands alone, because then
   * the logo IS the product name rendered graphically — a screen reader user
   * must hear the brand, not "logo". When the NAME is rendered as text beside
   * it (a square mark), the image is decorative by definition: announcing it
   * would read the brand out twice, which is the exact fault the wordmark rule
   * was written to avoid, in the other direction.
   */
  const named = shape !== "square";
  /*
   * `alt` is written OUT on every <img> below rather than spread in with the
   * rest, because a lint rule that cannot see through a spread is a lint rule
   * that cannot protect the one attribute here that a screen reader depends
   * on. The value is computed once; only its presence is repeated.
   */
  const alt = named ? branding.name : "";
  const hidden = named ? undefined : ("true" as const);
  const shared = {
    height: SIZE_HEIGHTS[size][shape],
    decoding: "async",
    onLoad,
    ref: measure,
  } as const;

  if (!hasDarkLogo) {
    /*
     * On the dark panel this is a dark wordmark on a light plate, which is a
     * deliberate look rather than a fault; `onDark` does not change the source
     * because there is no other source to choose.
     */
    return (
      <img
        className={styles.logo}
        src={branding.logoUrl}
        alt={alt}
        aria-hidden={hidden}
        {...shared}
      />
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
        alt={alt}
        aria-hidden={hidden}
        {...shared}
      />
    );
  }

  return (
    <>
      <img
        className={[styles.logo, styles.logoLight].join(" ")}
        src={branding.logoUrl}
        alt={alt}
        aria-hidden={hidden}
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
