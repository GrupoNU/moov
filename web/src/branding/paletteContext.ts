import { createContext, useContext } from "react";

import { MOOV_DEFAULT_BRANDING } from "./branding";
import { derivePalette, type BrandPalette } from "./palette";

/**
 * The derived palette of the current brand, for the rare component that needs
 * a colour VALUE rather than a token — a canvas, an inline SVG data URL, a
 * `theme-color` meta. Everything that can read a CSS custom property should;
 * this exists for what cannot.
 *
 * Lives apart from BrandingProvider.tsx so that file keeps exporting only its
 * component and the one hook Fast Refresh already allows; a context and a
 * hook in a plain .ts file are outside that rule's remit.
 */
export const DEFAULT_PALETTE: BrandPalette = derivePalette(
  MOOV_DEFAULT_BRANDING.colors.primary,
  MOOV_DEFAULT_BRANDING.colors.onPrimary,
);

export const PaletteContext = createContext<BrandPalette>(DEFAULT_PALETTE);

/** The current brand's palette. Always defined — the defaults are the floor. */
export function usePalette(): BrandPalette {
  return useContext(PaletteContext);
}
