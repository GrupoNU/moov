import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";

import {
  applyBranding,
  applyBrandingToDocument,
  fetchBranding,
  MOOV_DEFAULT_BRANDING,
  type Branding,
} from "./branding";
import { derivePalette } from "./palette";
import { PaletteContext } from "./paletteContext";
import { BrandingRefreshContext, type BrandingRefresh } from "./refreshContext";

/**
 * Supplies the brand to the tree and writes its seeds into CSS.
 *
 * The provider NEVER blocks rendering. It starts with Moov's defaults — which
 * tokens.css has already applied to the first paint — and swaps in the real
 * brand when it arrives. The alternative, holding the app behind a spinner
 * until /branding answers, would trade a correct-but-generic first paint for a
 * blank one, on the single screen where perceived speed matters most.
 */
const BrandingContext = createContext<Branding>(MOOV_DEFAULT_BRANDING);

export interface BrandingProviderProps {
  readonly children: ReactNode;
  /** Supplied by tests and by a server-rendered future; skips the fetch. */
  readonly branding?: Branding;
  readonly fetchImpl?: typeof fetch;
}

export function BrandingProvider({
  children,
  branding,
  fetchImpl,
}: BrandingProviderProps): React.JSX.Element {
  const [resolved, setResolved] = useState<Branding>(branding ?? MOOV_DEFAULT_BRANDING);

  /*
   * Guards a `setState` after unmount for the REFRESH path only. The boot
   * fetch has its own `cancelled` flag scoped to its effect; a refresh is
   * triggered by a user gesture and can outlive the screen that asked for it,
   * so it needs a flag that lives as long as the provider.
   */
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  useEffect(() => {
    if (branding !== undefined) {
      setResolved(branding);
      return undefined;
    }
    let cancelled = false;
    void fetchBranding(fetchImpl !== undefined ? { fetchImpl } : {}).then((document) => {
      if (!cancelled) setResolved(document);
    });
    return () => {
      cancelled = true;
    };
  }, [branding, fetchImpl]);

  /*
   * Re-reads the document after the brand-admin panel saved something
   * (L2-brand-admin §5): the app repaints with the new brand without a reload.
   *
   * A provider given an EXPLICIT `branding` prop refuses to refresh, and that
   * is the correct reading of the prop rather than a limitation: a caller that
   * passes the brand OWNS it, and letting a nested screen swap it out from
   * under them would quietly redefine the prop as "the initial brand".
   */
  const refresh = useCallback<BrandingRefresh>(async () => {
    if (branding !== undefined) return;
    const document = await fetchBranding(fetchImpl !== undefined ? { fetchImpl } : {});
    if (alive.current) setResolved(document);
  }, [branding, fetchImpl]);

  // The palette is derived ONCE per resolved brand. derivePalette bisects in
  // OKLCH and is cheap, but it is pure, and a pure function of the brand has
  // no business running on every render.
  const palette = useMemo(
    () => derivePalette(resolved.colors.primary, resolved.colors.onPrimary),
    [resolved.colors.primary, resolved.colors.onPrimary],
  );

  // Writing the tokens is a DOM side effect, so it belongs in an effect rather
  // than in render — and it runs on every brand change, including the initial
  // defaults, so the document is never in a half-applied state.
  useEffect(() => {
    if (typeof document === "undefined") return;
    applyBranding(resolved, document.documentElement, palette);
    applyBrandingToDocument(resolved, document);
  }, [resolved, palette]);

  // "Adjust it and DECLARE it": when a customer's colours could not be used
  // as sent, the console says so ONCE per brand, naming the theme and the
  // reason, so an operator who wonders why their button is a shade deeper
  // than the hex they typed finds the answer where they will look first.
  //
  // Moov's own brand is exempt: its dark lift is the product's design, pinned
  // by the tokens.css test, and a warning about a constant is noise on every
  // boot of every unbranded installation.
  useEffect(() => {
    if (resolved.isDefault) return;
    const themes = (["light", "dark"] as const).filter(
      (theme) => palette.adjusted[theme] !== undefined,
    );
    if (themes.length === 0) return;
    const detail = themes.map((theme) => `${theme}: ${palette.adjusted[theme] ?? ""}`).join(" | ");
    console.warn(`[branding] ${resolved.name}: colours adjusted for WCAG AA — ${detail}`);
  }, [resolved, palette]);

  return (
    <BrandingContext.Provider value={resolved}>
      <PaletteContext.Provider value={palette}>
        <BrandingRefreshContext.Provider value={refresh}>
          {children}
        </BrandingRefreshContext.Provider>
      </PaletteContext.Provider>
    </BrandingContext.Provider>
  );
}

/** The current brand. Always defined — the defaults are the floor. */
export function useBranding(): Branding {
  return useContext(BrandingContext);
}
