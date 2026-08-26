import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

import {
  applyBranding,
  applyBrandingToDocument,
  fetchBranding,
  MOOV_DEFAULT_BRANDING,
  type Branding,
} from "./branding";

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

  // Writing the tokens is a DOM side effect, so it belongs in an effect rather
  // than in render — and it runs on every brand change, including the initial
  // defaults, so the document is never in a half-applied state.
  useEffect(() => {
    if (typeof document === "undefined") return;
    applyBranding(resolved, document.documentElement);
    applyBrandingToDocument(resolved, document);
  }, [resolved]);

  return <BrandingContext.Provider value={resolved}>{children}</BrandingContext.Provider>;
}

/** The current brand. Always defined — the defaults are the floor. */
export function useBranding(): Branding {
  return useContext(BrandingContext);
}
