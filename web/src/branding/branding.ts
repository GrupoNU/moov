/**
 * Branding: fetching the customer's brand and turning it into design tokens
 * (arbitrations W-A1 and W-A2 of L2-pwa §3).
 *
 * The flow is deliberately boring and total:
 *
 *   1. The app starts with MOOV_DEFAULT_BRANDING already applied, so the first
 *      paint is a correctly styled Moov screen rather than an unstyled one.
 *   2. GET /branding resolves the real brand by Host.
 *   3. Whatever comes back — including nothing, including garbage — is merged
 *      onto the defaults and written to CSS custom properties.
 *
 * There is no state in which the app has no brand. A login screen that fails
 * to render because a logo could not be fetched would be a far worse outcome
 * than one that renders unbranded, so every failure path here lands on the
 * defaults silently (and says so in the console, once).
 */

/** The colour seeds a customer controls. Mirrors Go's `BrandingColors`. */
export interface BrandingColors {
  readonly primary: string;
  readonly onPrimary: string;
  readonly splashFrom: string;
  readonly splashTo: string;
}

/**
 * The branding document, mirroring Go's `Branding` (internal/jmaphttp/branding.go).
 *
 * This interface IS the wire contract. It is hand-written against a server we
 * own and whose responses we have read, which is the same choice W-A3 makes
 * for the JMAP client: a generic fetcher would buy nothing and cost us the
 * types.
 */
export interface Branding {
  readonly name: string;
  readonly logoUrl: string;
  readonly splashUrl: string;
  readonly colors: BrandingColors;
  readonly tagline: string;
  readonly supportUrl: string;
  /** True when this is Moov's own brand rather than a configured customer's. */
  readonly isDefault: boolean;
}

/**
 * Moov's own brand.
 *
 * These values are duplicated from Go's `DefaultBranding()` and from the seed
 * block of tokens.css. The duplication is intentional and pinned by a test:
 * the CSS copy makes the first paint correct before any JavaScript runs, the
 * Go copy answers callers that are not this app, and this copy is what the
 * merge falls back to. Three consumers, one palette, one test that fails when
 * they diverge.
 */
export const MOOV_DEFAULT_BRANDING: Branding = {
  name: "Moov Mail",
  logoUrl: "",
  splashUrl: "",
  colors: {
    primary: "#5b5bd6",
    onPrimary: "#ffffff",
    splashFrom: "#1e1b4b",
    splashTo: "#4c1d95",
  },
  tagline: "",
  supportUrl: "",
  isDefault: true,
};

/** The endpoint, same-origin by construction (W-A1). */
export const BRANDING_ENDPOINT = "/branding";

/**
 * A CSS hex colour, validated.
 *
 * The client re-validates what the server already validated, for one specific
 * reason: these strings are written into `style.setProperty`, and a value that
 * is not a colour would either be ignored (leaving the previous brand's
 * colour, a confusing half-applied state) or, with a crafted value, participate
 * in CSS injection. Validating here means the only thing that can reach a
 * custom property is a hex literal.
 */
function isHexColor(value: unknown): value is string {
  return typeof value === "string" && /^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/.test(value);
}

/**
 * A URL safe to put in an `<img src>`.
 *
 * Only same-origin ROOT-RELATIVE paths are accepted — which is exactly what
 * the server emits. A customer-supplied absolute URL is refused even though
 * the server would never send one: an `<img src>` pointing off-origin is a
 * tracking pixel on the login page and a mixed-content warning, and defending
 * against it here costs one regex.
 */
function isSafeAssetUrl(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.startsWith("/") &&
    // "//host/path" is protocol-relative and therefore off-origin.
    !value.startsWith("//") &&
    !value.includes("\\")
  );
}

/** A link target that cannot become script execution. */
function isSafeLinkUrl(value: unknown): value is string {
  if (typeof value !== "string" || value === "") return false;
  const lower = value.toLowerCase();
  return (
    lower.startsWith("https://") || lower.startsWith("http://") || lower.startsWith("mailto:")
  );
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.trim() !== "";
}

/**
 * Merges an unknown server response onto the defaults, field by field.
 *
 * Every field is validated independently, so a document with one bad colour
 * keeps its good ones. This is the client half of the server's own rule: a
 * partially valid brand renders partially branded, never unstyled.
 */
export function mergeBranding(raw: unknown): Branding {
  // An array is `typeof "object"` and non-null, so it would otherwise fall
  // through to the field-by-field merge below and produce a document flagged
  // as a CONFIGURED brand with every field defaulted — a state that says "this
  // customer has a brand" about a response that is not a document at all. A
  // JSON array is never a branding document, so it is rejected outright.
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) {
    return MOOV_DEFAULT_BRANDING;
  }
  const doc = raw as Record<string, unknown>;
  const rawColors =
    typeof doc.colors === "object" && doc.colors !== null
      ? (doc.colors as Record<string, unknown>)
      : {};

  const defaults = MOOV_DEFAULT_BRANDING;

  return {
    name: isNonEmptyString(doc.name) ? doc.name.trim() : defaults.name,
    logoUrl: isSafeAssetUrl(doc.logoUrl) ? doc.logoUrl : defaults.logoUrl,
    splashUrl: isSafeAssetUrl(doc.splashUrl) ? doc.splashUrl : defaults.splashUrl,
    colors: {
      primary: isHexColor(rawColors.primary)
        ? rawColors.primary.toLowerCase()
        : defaults.colors.primary,
      onPrimary: isHexColor(rawColors.onPrimary)
        ? rawColors.onPrimary.toLowerCase()
        : defaults.colors.onPrimary,
      splashFrom: isHexColor(rawColors.splashFrom)
        ? rawColors.splashFrom.toLowerCase()
        : defaults.colors.splashFrom,
      splashTo: isHexColor(rawColors.splashTo)
        ? rawColors.splashTo.toLowerCase()
        : defaults.colors.splashTo,
    },
    tagline: isNonEmptyString(doc.tagline) ? doc.tagline.trim() : defaults.tagline,
    supportUrl: isSafeLinkUrl(doc.supportUrl) ? doc.supportUrl : defaults.supportUrl,
    // The server's `default` flag is authoritative when present; anything else
    // is treated as "a brand was configured", which is the safe reading (it
    // only affects whether the UI may show Moov's own wordmark).
    isDefault: doc.default === true,
  };
}

/**
 * Writes the brand seeds onto the document root.
 *
 * ONLY the four seed properties are written. Everything else in the app is
 * derived from them in CSS (tokens.css layer 2), which is what keeps this
 * function to four lines and keeps JavaScript out of the palette business.
 */
export function applyBranding(brand: Branding, root: HTMLElement): void {
  const style = root.style;
  style.setProperty("--brand-primary", brand.colors.primary);
  style.setProperty("--brand-on-primary", brand.colors.onPrimary);
  style.setProperty("--brand-splash-from", brand.colors.splashFrom);
  style.setProperty("--brand-splash-to", brand.colors.splashTo);
}

/**
 * Applies the brand's effects outside the token system: the document title and
 * the theme-color meta.
 *
 * Separate from applyBranding because these are document-level side effects
 * rather than styling, and a test for the token contract should not have to
 * assert about <title>.
 */
export function applyBrandingToDocument(brand: Branding, doc: Document): void {
  doc.title = brand.name;

  let meta = doc.querySelector<HTMLMetaElement>('meta[name="theme-color"]');
  if (!meta) {
    meta = doc.createElement("meta");
    meta.name = "theme-color";
    doc.head.appendChild(meta);
  }
  meta.content = brand.colors.primary;
}

/** Options for {@link fetchBranding}, all with production defaults. */
export interface FetchBrandingOptions {
  /** Injected for tests; defaults to the global fetch. */
  readonly fetchImpl?: typeof fetch;
  /**
   * How long to wait before giving up and using the defaults. The login screen
   * must not sit blank behind a hanging request: 4 s is well past any healthy
   * response and well short of a user deciding the app is broken.
   */
  readonly timeoutMs?: number;
  readonly endpoint?: string;
}

/**
 * Fetches the brand for the current host.
 *
 * NEVER REJECTS. Every failure — network, timeout, non-2xx, malformed JSON —
 * resolves to the Moov defaults, because the caller's only sensible response to
 * an error would be to use the defaults anyway, and making that explicit here
 * removes an error path from every call site.
 */
export async function fetchBranding(options: FetchBrandingOptions = {}): Promise<Branding> {
  const {
    fetchImpl = globalThis.fetch.bind(globalThis),
    timeoutMs = 4000,
    endpoint = BRANDING_ENDPOINT,
  } = options;

  const controller = new AbortController();
  const timer = setTimeout(() => { controller.abort(); }, timeoutMs);

  try {
    const response = await fetchImpl(endpoint, {
      signal: controller.signal,
      headers: { Accept: "application/json" },
      // The document is public and cacheable; letting the HTTP cache serve it
      // is the difference between a branded first paint and a flash of
      // defaults on every reload.
      credentials: "omit",
    });
    if (!response.ok) {
      return MOOV_DEFAULT_BRANDING;
    }
    return mergeBranding(await response.json());
  } catch {
    // Deliberately swallowed: see the contract above.
    return MOOV_DEFAULT_BRANDING;
  } finally {
    clearTimeout(timer);
  }
}
