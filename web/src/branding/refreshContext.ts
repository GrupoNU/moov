import { createContext, useContext } from "react";

/**
 * Re-reads `GET /branding` and re-applies the brand to the whole app.
 *
 * # Why a refresher exists at all
 *
 * Through the brand-admin panel (L2-brand-admin §5) the brand becomes
 * EDITABLE from inside the running app, which it never was before: until now
 * the document was fetched once at boot and could only change by an operator
 * running `moovctl` and the user reloading. An administrator who picks a new
 * primary and then keeps looking at the old accent has no way to tell whether
 * the save landed, and "reload the page to see it" is the sentence this whole
 * epic exists to avoid.
 *
 * # Why it is a context and not an exported function
 *
 * The applied brand is provider STATE. A module-level function could re-fetch
 * the document but could not tell React about it, so the seeds would repaint
 * and the components reading `useBranding()` would keep the old name — a
 * half-updated app, which is worse than one that did not update.
 *
 * `applyBranding` stays the single writer of the seed properties: this only
 * makes the provider re-resolve, and the provider's existing effect does the
 * writing exactly as it does on boot.
 *
 * # Why it lives beside the provider rather than in it
 *
 * The same Fast Refresh rule that put `usePalette` in `paletteContext.ts`:
 * a component file should export components, and `BrandingProvider.tsx`
 * already spends its one allowed hook on `useBranding`.
 */
export type BrandingRefresh = () => Promise<void>;

/**
 * The default is a no-op that resolves, so a caller outside the provider —
 * a test rendering a section on its own — neither crashes nor silently hangs
 * awaiting a promise that never settles.
 */
export const BrandingRefreshContext = createContext<BrandingRefresh>(async () => {
  /* no provider: nothing to refresh */
});

/** Re-reads the brand and repaints the app with it. */
export function useBrandingRefresh(): BrandingRefresh {
  return useContext(BrandingRefreshContext);
}
