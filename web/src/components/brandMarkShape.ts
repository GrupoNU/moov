/**
 * How a customer's logo is SIZED: by the shape of the file they uploaded.
 *
 * # Why the shape decides anything
 *
 * The first owner to configure a brand uploaded a SQUARE mark, and the top bar
 * rendered it at a wordmark's height — 28px, which for a square is a postage
 * stamp with a quarter of a wordmark's ink. Equal heights do not produce equal
 * presence when one mark is four times wider than the other, so the height is
 * per shape rather than per size alone.
 *
 * The same measurement decides whether the product NAME is rendered as text
 * beside the mark: a wordmark already says the name graphically, and a square
 * glyph names nothing at all. See {@link ASPECT_WIDE_MIN}.
 *
 * # Why this is a module of its own
 *
 * Only so `BrandMark.tsx` exports components and nothing else, which is what
 * keeps Fast Refresh working (and what the repo's lint config enforces at zero
 * warnings). Everything here is pure: no DOM, no React, no imports.
 */

/** What a logo's aspect ratio makes it, once it is known. */
export type LogoShape = "wide" | "square";

/** The sizes {@link LogoShape} heights are defined for. */
export type BrandMarkSize = "sm" | "md" | "lg";

/**
 * Where a logo stops being a wordmark and becomes a glyph.
 *
 * At 1.6:1 a mark is still visibly wider than it is tall but has no room for a
 * readable word — a lockup ("glyph above a short word") and a two-letter
 * monogram both land here, and both need the product name beside them for the
 * same reason a square one does. Above it the mark is a strip long enough to
 * carry text, which is a wordmark.
 *
 * The threshold is deliberately generous toward "square": the failure of
 * treating a square mark as wide (a stamp, with the product unnamed anywhere on
 * the screen) is far worse than the failure the other way (a slightly wide
 * wordmark with its own name repeated once, at a size where it fits).
 */
export const ASPECT_WIDE_MIN = 1.6;

/**
 * The rendered height of the logo per size AND shape, mirroring
 * BrandMark.module.css.
 *
 * Duplicated in TypeScript because the `height` ATTRIBUTE is what reserves the
 * box before any stylesheet or image has loaded, and an attribute cannot read a
 * class. A test pins these against the stylesheet so the two cannot drift.
 *
 * 32/40 in the top bar is Gmail's own relationship (canon 07 §1 measures its
 * bar mark at ~40px tall); 56/80 is the same ratio on the login panel, where
 * there is room for the mark to be the subject rather than a label.
 */
export const SIZE_HEIGHTS: Record<BrandMarkSize, Record<LogoShape, number>> = {
  sm: { wide: 32, square: 40 },
  md: { wide: 32, square: 40 },
  lg: { wide: 56, square: 80 },
};

/**
 * Classifies a loaded image by its intrinsic size.
 *
 * Returns undefined for an image whose size the browser does not know — a
 * decode failure, or jsdom, which reports 0x0 for everything it never fetched.
 * The caller keeps its pre-load assumption rather than acting on a measurement
 * that is not one.
 */
export function logoShapeOf(
  naturalWidth: number,
  naturalHeight: number,
): LogoShape | undefined {
  if (!(naturalWidth > 0) || !(naturalHeight > 0)) return undefined;
  return naturalWidth / naturalHeight > ASPECT_WIDE_MIN ? "wide" : "square";
}
