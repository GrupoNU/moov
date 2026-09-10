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

import { derivePalette, type BrandPalette } from "./palette";

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
  /**
   * A short form of the name for tight spots (the collapsed rail, the PWA
   * install prompt, a tab title next to an unread count). Optional on the
   * TYPE because callers outside this module build Branding literals; the
   * merge always fills it, and {@link brandShortName} is the total accessor.
   */
  readonly shortName?: string;
  readonly logoUrl: string;
  /**
   * A variant of the logo for dark contexts, or "" when the customer supplied
   * none.
   *
   * A wordmark is usually one fixed colour, and the common upload is a dark
   * one: Areacorp's is pure black, which is invisible on the login brand
   * panel's dark gradient and in the dark theme's top bar. A second asset is
   * the only honest fix — recolouring somebody's logo in CSS (a filter, a
   * blend mode) mangles any logo that is not a flat silhouette.
   *
   * When it is "", {@link BrandMark} falls back to drawing the light logo on a
   * light plate, which keeps a dark wordmark legible without touching its
   * pixels.
   */
  readonly logoDarkUrl: string;
  readonly splashUrl: string;
  readonly colors: BrandingColors;
  readonly tagline: string;
  readonly supportUrl: string;
  /**
   * The operator's privacy policy, or "" when they configured none.
   *
   * Distinct from the licence and source links in the legal footer, which are
   * OURS and non-removable (AGPL-3.0 §13): this one is the operator's own
   * obligation to their users, and only they can say where it lives. Empty
   * renders nothing rather than a dead link.
   */
  readonly privacyUrl: string;
  /** The operator's terms of service, or "" when they configured none. */
  readonly termsUrl: string;
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
  shortName: "Moov Mail",
  logoUrl: "",
  logoDarkUrl: "",
  splashUrl: "",
  colors: {
    primary: "#5b5bd6",
    onPrimary: "#ffffff",
    splashFrom: "#1e1b4b",
    splashTo: "#4c1d95",
  },
  tagline: "",
  supportUrl: "",
  privacyUrl: "",
  termsUrl: "",
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

/** The longest a short name may be, in user-perceived characters. */
export const SHORT_NAME_MAX_RUNES = 12;

/**
 * Derives a short name from a full one, the way the server does when the
 * customer did not configure one: the name itself when it fits, else its
 * first word cut to {@link SHORT_NAME_MAX_RUNES}. Counted in code points, not
 * UTF-16 units, so a name with an emoji or an accented letter is not cut in
 * the middle of a character.
 */
export function deriveShortName(name: string): string {
  const trimmed = name.trim();
  if (Array.from(trimmed).length <= SHORT_NAME_MAX_RUNES) return trimmed;
  const firstWord = trimmed.split(/\s+/)[0] ?? trimmed;
  return Array.from(firstWord).slice(0, SHORT_NAME_MAX_RUNES).join("");
}

/** The brand's short name, always defined. */
export function brandShortName(brand: Branding): string {
  return brand.shortName ?? deriveShortName(brand.name);
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

  const name = isNonEmptyString(doc.name) ? doc.name.trim() : defaults.name;

  return {
    name,
    // A configured short name is taken as sent (trimmed, and cut to the same
    // ceiling the derivation respects, so no caller has to defend against a
    // long one); otherwise it is derived from whichever name won above.
    shortName: isNonEmptyString(doc.shortName)
      ? Array.from(doc.shortName.trim()).slice(0, SHORT_NAME_MAX_RUNES).join("")
      : deriveShortName(name),
    logoUrl: isSafeAssetUrl(doc.logoUrl) ? doc.logoUrl : defaults.logoUrl,
    logoDarkUrl: isSafeAssetUrl(doc.logoDarkUrl) ? doc.logoDarkUrl : defaults.logoDarkUrl,
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
    // The same scheme allow-list as supportUrl, for the same reason: these
    // three are the only strings in the document that become an `href`, and a
    // javascript: URL in one of them would be script execution on the login
    // screen. A rejected value falls back to "" — no link at all — rather than
    // to some other host's policy page.
    privacyUrl: isSafeLinkUrl(doc.privacyUrl) ? doc.privacyUrl : defaults.privacyUrl,
    termsUrl: isSafeLinkUrl(doc.termsUrl) ? doc.termsUrl : defaults.termsUrl,
    // The server's `default` flag is authoritative when present; anything else
    // is treated as "a brand was configured", which is the safe reading (it
    // only affects whether the UI may show Moov's own wordmark).
    isDefault: doc.default === true,
  };
}

/**
 * The custom properties the brand controls, with their values.
 *
 * The four customer seeds plus the per-theme accent family. tokens.css picks
 * `-light` in its light block and `-dark` in both dark blocks; JavaScript
 * NEVER writes `--color-accent` itself, because an inline value on :root wins
 * over every theme block and would freeze the accent in one theme.
 *
 * Exposed as data so the test that pins tokens.css's defaults iterates the
 * same names this function writes.
 */
export function brandSeeds(
  brand: Branding,
  palette: BrandPalette,
): Readonly<Record<string, string>> {
  return {
    "--brand-primary": brand.colors.primary,
    "--brand-on-primary": brand.colors.onPrimary,
    "--brand-splash-from": brand.colors.splashFrom,
    "--brand-splash-to": brand.colors.splashTo,

    "--brand-accent-light": palette.light.accent,
    "--brand-accent-hover-light": palette.light.accentHover,
    "--brand-accent-active-light": palette.light.accentActive,
    "--brand-on-accent-light": palette.light.onAccent,
    "--brand-accent-tint-light": palette.light.accentTint,
    "--brand-accent-tint-strong-light": palette.light.accentTintStrong,
    "--brand-accent-container-light": palette.light.accentContainer,
    "--brand-on-accent-container-light": palette.light.onAccentContainer,
    "--brand-selected-row-light": palette.light.selectedRow,
    "--brand-active-pill-light": palette.light.activePill,

    "--brand-accent-dark": palette.dark.accent,
    "--brand-accent-hover-dark": palette.dark.accentHover,
    "--brand-accent-active-dark": palette.dark.accentActive,
    "--brand-on-accent-dark": palette.dark.onAccent,
    "--brand-accent-tint-dark": palette.dark.accentTint,
    "--brand-accent-tint-strong-dark": palette.dark.accentTintStrong,
    "--brand-accent-container-dark": palette.dark.accentContainer,
    "--brand-on-accent-container-dark": palette.dark.onAccentContainer,
    "--brand-selected-row-dark": palette.dark.selectedRow,
    "--brand-active-pill-dark": palette.dark.activePill,
  };
}

/**
 * Writes the brand seeds onto the document root.
 *
 * ONLY seed properties are written — the customer's four plus the accent
 * family {@link derivePalette} guarantees at WCAG AA. Everything else in the
 * app is derived from them in CSS (tokens.css layer 2), which keeps
 * JavaScript out of the semantic-token business and keeps theme switching in
 * the stylesheet where it belongs.
 *
 * The palette is a parameter so the provider can compute it once per brand;
 * it defaults to deriving from the brand for callers with no reason to cache.
 */
export function applyBranding(
  brand: Branding,
  root: HTMLElement,
  palette: BrandPalette = derivePalette(brand.colors.primary, brand.colors.onPrimary),
): void {
  const style = root.style;
  for (const [name, value] of Object.entries(brandSeeds(brand, palette))) {
    style.setProperty(name, value);
  }
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
