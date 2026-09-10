/**
 * The brand administration client (L2-brand-admin §4).
 *
 * # Why this file re-validates a server we own
 *
 * Exactly the discipline `mergeBranding` already applies to `GET /branding`,
 * and for the same reason one level up: the values in this document become
 * `style.setProperty` calls on a preview container, `<img src>` attributes and
 * `href`s. A field that is not what its type says would either be ignored
 * (a half-applied preview, which reads as "the app lost my colour") or, with a
 * crafted value, would be CSS injection on the settings page. So every field is
 * checked on the way in, independently, and a bad one falls back rather than
 * poisoning the document around it.
 *
 * The difference from `mergeBranding` is what a bad field falls back TO. The
 * public document merges onto Moov's defaults, because the login screen must
 * render something. This one is an EDITOR: it falls back to the empty value,
 * because showing the administrator Moov's `#5b5bd6` in a field their server
 * did not send would invite them to "keep" a colour that is not theirs.
 *
 * # Errors are a closed set, not strings
 *
 * Five kinds, because the UI reacts differently to each: `notAdmin` removes the
 * whole tab, `invalidField` points at one control, `tooLarge` and
 * `unsupportedType` are upload messages beside the drop zone, and `network` is
 * the only one that means "try again". A `catch` on a message string would have
 * made the tab's own existence depend on prose.
 */

/** The four colour seeds an administrator controls. */
export interface BrandAdminColors {
  readonly primary: string;
  readonly onPrimary: string;
  readonly splashFrom: string;
  readonly splashTo: string;
}

/** The four colour fields, as the API names them in patches and in the doc. */
export const BRAND_COLOR_FIELDS = ["primary", "onPrimary", "splashFrom", "splashTo"] as const;
export type BrandColorField = (typeof BRAND_COLOR_FIELDS)[number];

/** One stored image, as the server describes it. */
export interface BrandAsset {
  /** Already carries the server's `?v=` cache-buster. Rendered verbatim. */
  readonly url: string;
  readonly bytes: number;
  readonly width: number;
  readonly height: number;
}

/** The four asset slots. The keys are the API's own path segments. */
export const ASSET_KINDS = ["logo", "logoDark", "icon", "splash"] as const;
export type AssetKind = (typeof ASSET_KINDS)[number];

/** Where the generated icons came from — `iconIssue` explains a non-"icon". */
export type IconSource = "icon" | "logo" | "default";

/**
 * The admin view of a host's brand — everything the public document has, plus
 * the bytes, the generated icons, and what the server thinks is wrong with it.
 */
export interface BrandAdminDoc {
  readonly host: string;
  /** True while the host is still on Moov's own brand. */
  readonly isDefault: boolean;
  readonly name: string;
  readonly shortName: string;
  readonly tagline: string;
  readonly supportUrl: string;
  readonly privacyUrl: string;
  readonly termsUrl: string;
  readonly colors: BrandAdminColors;
  /**
   * Which colour fields the operator actually SET, as opposed to the ones the
   * server derived or defaulted.
   *
   * `colors` above is the EFFECTIVE palette — a picker needs a colour, so the
   * server resolves every field before sending it — which makes a derived
   * gradient stop indistinguishable from a typed one. The panel needs the
   * difference to show a derived value as a PLACEHOLDER (it is automatic, and
   * clearing the field returns it to automatic) rather than as text the
   * operator appears to have entered.
   */
  readonly colorsConfigured: ReadonlySet<BrandColorField>;
  readonly assets: Readonly<Record<AssetKind, BrandAsset | null>>;
  readonly iconSource: IconSource;
  /** Why the icons are not from the uploaded square icon, or "". */
  readonly iconIssue: string;
  readonly brandAdmins: readonly string[];
  /** Non-fatal complaints about what was just accepted (non-square icon, …). */
  readonly warnings: readonly string[];
  readonly publicUrl: string;
  readonly manifestUrl: string;
  /** The generated icons by name, e.g. `icon-192`, `apple-touch-icon`. */
  readonly iconUrls: Readonly<Record<string, string>>;
  readonly version: number;
}

/** The probe's answer: who may edit this host. */
export interface BrandAdminProbe {
  readonly host: string;
  readonly canEdit: boolean;
}

/** The partial write body. Absent = unchanged; "" = clear. */
export interface BrandPatch {
  readonly name?: string;
  readonly shortName?: string;
  readonly tagline?: string;
  readonly supportUrl?: string;
  readonly privacyUrl?: string;
  readonly termsUrl?: string;
  readonly colors?: {
    readonly primary?: string;
    readonly onPrimary?: string;
    readonly splashFrom?: string;
    readonly splashTo?: string;
  };
}

/** What went wrong, as a value the UI can switch on. */
export type BrandAdminErrorKind =
  /** 404 — not an administrator of this host, or the feature is off. */
  | "notAdmin"
  /** 400 — one named field was refused, with the server's reason. */
  | "invalidField"
  /** 413 — the image is over the 2 MiB ceiling. */
  | "tooLarge"
  /** 415 — the bytes are not one of the accepted image types. */
  | "unsupportedType"
  /** Anything else: transport, 5xx, a body that is not a document. */
  | "network";

export class BrandAdminError extends Error {
  readonly kind: BrandAdminErrorKind;
  /** For `invalidField`: which field the server named. */
  readonly field: string | undefined;
  readonly status: number;

  constructor(kind: BrandAdminErrorKind, message: string, status = 0, field?: string) {
    super(message);
    this.name = "BrandAdminError";
    this.kind = kind;
    this.status = status;
    this.field = field;
  }
}

/** The API root. Same-origin by construction, like every other route. */
export const BRAND_ADMIN_ENDPOINT = "/branding/admin";

/** The upload ceiling the server enforces, mirrored so the client can pre-check. */
export const MAX_ASSET_BYTES = 2 * 1024 * 1024;

/**
 * The image types the server accepts.
 *
 * SVG is deliberately absent, and it is the one refusal that needs a sentence
 * on screen: an SVG is a document that can carry script and external
 * references, and these images are rendered on the LOGIN page, before anyone
 * has authenticated. Rasterising one server-side would be a second parser on
 * hostile input for a convenience.
 */
export const ACCEPTED_IMAGE_TYPES = [
  "image/png",
  "image/jpeg",
  "image/webp",
  "image/gif",
] as const;

/** The extensions that go with them, for the picker's `accept` and a pre-check. */
export const ACCEPTED_IMAGE_EXTENSIONS = [
  ".png",
  ".jpg",
  ".jpeg",
  ".webp",
  ".gif",
] as const;

/**
 * A client-side pre-check of one file.
 *
 * The SERVER is authoritative — it sniffs the bytes, and a renamed `.png` is
 * refused there whatever this says. This exists so the common mistakes (a PDF,
 * a 6 MB photograph, an SVG) are answered instantly and beside the control the
 * user just used, instead of after an upload that was never going to work.
 */
export function checkImageFile(file: File): BrandAdminErrorKind | undefined {
  if (file.size > MAX_ASSET_BYTES) return "tooLarge";
  const type = file.type.toLowerCase();
  const name = file.name.toLowerCase();
  const typeOk = (ACCEPTED_IMAGE_TYPES as readonly string[]).includes(type);
  const extensionOk = ACCEPTED_IMAGE_EXTENSIONS.some((extension) => name.endsWith(extension));
  /*
   * Either signal is enough. A file dragged out of some applications arrives
   * with an empty `type`, and a file whose name has no extension is normal on
   * macOS — refusing on one missing signal would block real uploads the server
   * would have happily taken.
   */
  if (type !== "" && !typeOk) return "unsupportedType";
  if (type === "" && !extensionOk) return "unsupportedType";
  return undefined;
}

// ---------------------------------------------------------------------------
// parsing — nothing from the wire is trusted
// ---------------------------------------------------------------------------

function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** A hex literal, or "" — the same gate `mergeBranding` puts on a colour. */
function hex(value: unknown): string {
  return typeof value === "string" &&
    /^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/.test(value)
    ? value.toLowerCase()
    : "";
}

/**
 * A same-origin root-relative path, or "".
 *
 * The same rule `mergeBranding` applies, for the same reason: these strings
 * become `<img src>`, and an off-origin one on the settings page is a tracking
 * pixel that reports when an administrator opened their brand panel.
 */
function assetUrl(value: unknown): string {
  return typeof value === "string" &&
    value.startsWith("/") &&
    !value.startsWith("//") &&
    !value.includes("\\")
    ? value
    : "";
}

function stringList(value: unknown): readonly string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

function parseAsset(value: unknown): BrandAsset | null {
  if (!isRecord(value)) return null;
  const url = assetUrl(value.url);
  // An asset whose URL did not survive validation is not half an asset: it is
  // an empty slot, because there is nothing to draw and nothing to remove.
  if (url === "") return null;
  return {
    url,
    bytes: typeof value.bytes === "number" && value.bytes >= 0 ? value.bytes : 0,
    width: typeof value.width === "number" && value.width > 0 ? value.width : 0,
    height: typeof value.height === "number" && value.height > 0 ? value.height : 0,
  };
}

function parseIconUrls(value: unknown): Readonly<Record<string, string>> {
  if (!isRecord(value)) return {};
  const out: Record<string, string> = {};
  for (const [name, url] of Object.entries(value)) {
    const safe = assetUrl(url);
    if (safe !== "") out[name] = safe;
  }
  return out;
}

/**
 * The configured-colour set, admitting only the four names the API defines.
 *
 * An unknown string is DROPPED rather than kept: the set only ever gates a
 * placeholder, so an unrecognised member could do nothing useful and a
 * server that grew a fifth colour would otherwise reach a panel that cannot
 * render it. An absent field yields the empty set — "nothing was configured" —
 * which is also what an older server sends, and which degrades to showing the
 * effective values as placeholders everywhere. That is the safe direction: it
 * never puts a value in a field the operator did not type.
 */
function parseColorsConfigured(raw: unknown): ReadonlySet<BrandColorField> {
  const out = new Set<BrandColorField>();
  if (!Array.isArray(raw)) return out;
  for (const entry of raw) {
    const known = BRAND_COLOR_FIELDS.find((field) => field === entry);
    if (known !== undefined) out.add(known);
  }
  return out;
}

/**
 * Turns an unknown body into a document, field by field.
 *
 * Throws only when the body is not an object at all — at that point there is
 * no document to show and pretending otherwise would put an empty editor in
 * front of a brand that exists.
 */
export function parseBrandAdminDoc(raw: unknown): BrandAdminDoc {
  if (!isRecord(raw)) {
    throw new BrandAdminError("network", "the brand document was not an object");
  }
  const colors = isRecord(raw.colors) ? raw.colors : {};
  const assets = isRecord(raw.assets) ? raw.assets : {};
  const iconSource = raw.iconSource;

  return {
    host: str(raw.host),
    isDefault: raw.default === true,
    name: str(raw.name),
    shortName: str(raw.shortName),
    tagline: str(raw.tagline),
    supportUrl: str(raw.supportUrl),
    privacyUrl: str(raw.privacyUrl),
    termsUrl: str(raw.termsUrl),
    colors: {
      primary: hex(colors.primary),
      onPrimary: hex(colors.onPrimary),
      splashFrom: hex(colors.splashFrom),
      splashTo: hex(colors.splashTo),
    },
    colorsConfigured: parseColorsConfigured(raw.colorsConfigured),
    assets: {
      logo: parseAsset(assets.logo),
      logoDark: parseAsset(assets.logoDark),
      icon: parseAsset(assets.icon),
      splash: parseAsset(assets.splash),
    },
    iconSource:
      iconSource === "icon" || iconSource === "logo" || iconSource === "default"
        ? iconSource
        : "default",
    iconIssue: str(raw.iconIssue),
    brandAdmins: stringList(raw.brandAdmins),
    warnings: stringList(raw.warnings),
    publicUrl: str(raw.publicUrl),
    manifestUrl: str(raw.manifestUrl),
    iconUrls: parseIconUrls(raw.iconUrls),
    version: typeof raw.version === "number" ? raw.version : 0,
  };
}

// ---------------------------------------------------------------------------
// the client
// ---------------------------------------------------------------------------

export interface BrandAdminClientOptions {
  /**
   * The `Authorization` header value — the SAME Basic string every other
   * authenticated route uses (`MailScreen`'s `authedFetch`, `uploadBlob`).
   *
   * Passed in rather than read from storage here, so this module has no
   * opinion about where a credential lives and a test needs no session.
   */
  readonly authorization: string;
  readonly fetchImpl?: typeof fetch;
  readonly baseUrl?: string;
}

/**
 * Turns a failed response into the right kind of error.
 *
 * The status IS the taxonomy — the server's contract assigns one meaning to
 * each — so nothing here reads a message to decide what happened. A 400's body
 * is read for its `field` and `reason`, which is the only case where the body
 * carries information the status does not.
 */
async function errorFor(response: Response): Promise<BrandAdminError> {
  if (response.status === 404) {
    return new BrandAdminError("notAdmin", "not a brand administrator", 404);
  }
  if (response.status === 413) {
    return new BrandAdminError("tooLarge", "the image is over the size limit", 413);
  }
  if (response.status === 415) {
    let reason = "the image type is not accepted";
    try {
      const body: unknown = await response.json();
      if (isRecord(body) && typeof body.reason === "string" && body.reason !== "") {
        reason = body.reason;
      }
    } catch {
      // A body that is not JSON changes nothing: the status already said it.
    }
    return new BrandAdminError("unsupportedType", reason, 415);
  }
  if (response.status === 400) {
    let field: string | undefined;
    let reason = "the value was refused";
    try {
      const body: unknown = await response.json();
      if (isRecord(body)) {
        if (typeof body.field === "string" && body.field !== "") field = body.field;
        if (typeof body.reason === "string" && body.reason !== "") reason = body.reason;
      }
    } catch {
      // Same as above.
    }
    return new BrandAdminError("invalidField", reason, 400, field);
  }
  return new BrandAdminError("network", `the server answered ${response.status}`, response.status);
}

/**
 * The typed client. One instance per credential, like {@link JmapClient}.
 *
 * Every method either returns a parsed document or throws a
 * {@link BrandAdminError} — there is no third outcome and no `null` to check,
 * which is what keeps the call sites in the section free of shape guards.
 */
export class BrandAdminClient {
  private readonly authorization: string;
  private readonly fetchImpl: typeof fetch;
  private readonly root: string;

  constructor(options: BrandAdminClientOptions) {
    this.authorization = options.authorization;
    this.fetchImpl = options.fetchImpl ?? globalThis.fetch.bind(globalThis);
    this.root = `${(options.baseUrl ?? "").replace(/\/+$/, "")}${BRAND_ADMIN_ENDPOINT}`;
  }

  /**
   * Asks whether this session may administer this host.
   *
   * Answers `undefined` rather than throwing on 404, because "not an
   * administrator" is the NORMAL answer for almost every user and is not an
   * error condition — it is the fact that decides whether a tab exists.
   */
  async probe(signal?: AbortSignal): Promise<BrandAdminProbe | undefined> {
    let response: Response;
    try {
      response = await this.send(this.root, { method: "GET" }, signal);
    } catch (error) {
      if (error instanceof BrandAdminError && error.kind === "notAdmin") return undefined;
      throw error;
    }
    const body: unknown = await response.json().catch(() => undefined);
    if (!isRecord(body) || body.canEdit !== true) return undefined;
    return { host: str(body.host), canEdit: true };
  }

  /** The current document. */
  async get(signal?: AbortSignal): Promise<BrandAdminDoc> {
    const response = await this.send(`${this.root}/brand`, { method: "GET" }, signal);
    return parseBrandAdminDoc(await this.json(response));
  }

  /**
   * Writes only the fields present in `patch`.
   *
   * A partial body rather than the whole document, which is the server's own
   * contract and is also the only safe shape for a form that saves per field:
   * sending every field on every blur would make two administrators editing at
   * once overwrite each other's untouched values.
   */
  async update(patch: BrandPatch, signal?: AbortSignal): Promise<BrandAdminDoc> {
    const response = await this.send(
      `${this.root}/brand`,
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(patch),
      },
      signal,
    );
    return parseBrandAdminDoc(await this.json(response));
  }

  /** Replaces one asset with the file's raw bytes. */
  async putAsset(kind: AssetKind, file: File, signal?: AbortSignal): Promise<BrandAdminDoc> {
    const response = await this.send(
      `${this.root}/assets/${kind}`,
      {
        method: "PUT",
        headers: {
          "Content-Type": file.type === "" ? "application/octet-stream" : file.type,
        },
        body: file,
      },
      signal,
    );
    return parseBrandAdminDoc(await this.json(response));
  }

  /** Removes one asset. */
  async deleteAsset(kind: AssetKind, signal?: AbortSignal): Promise<BrandAdminDoc> {
    const response = await this.send(`${this.root}/assets/${kind}`, { method: "DELETE" }, signal);
    return parseBrandAdminDoc(await this.json(response));
  }

  /** Back to Moov's own brand. The administrator keeps their access. */
  async reset(signal?: AbortSignal): Promise<BrandAdminDoc> {
    const response = await this.send(`${this.root}/reset`, { method: "POST" }, signal);
    return parseBrandAdminDoc(await this.json(response));
  }

  private async json(response: Response): Promise<unknown> {
    try {
      return await response.json();
    } catch {
      throw new BrandAdminError("network", "the server's answer was not JSON", response.status);
    }
  }

  private async send(
    url: string,
    init: RequestInit,
    signal?: AbortSignal,
  ): Promise<Response> {
    let response: Response;
    try {
      response = await this.fetchImpl(url, {
        ...init,
        signal: signal ?? null,
        headers: {
          ...(init.headers as Record<string, string> | undefined),
          Authorization: this.authorization,
          Accept: "application/json",
        },
        /*
         * The header is sent explicitly, so ambient cookies must not ride
         * along — the same rule `JmapClient.request` states and for the same
         * reason (the CORS wildcard in cors.go stays representable).
         */
        credentials: "omit",
      });
    } catch (error) {
      if (error instanceof DOMException && error.name === "AbortError") throw error;
      throw new BrandAdminError("network", "the request could not reach the server");
    }
    if (!response.ok) throw await errorFor(response);
    return response;
  }
}

/**
 * Turns a client error into the sentence the panel shows.
 *
 * Lives here rather than beside the section for one reason: the SERVER's own
 * message is the right sentence for three of the five kinds. A 400 named the
 * field and why ("use 12 characters at most"), a 415 named the type it refused,
 * a 413 named the ceiling — no generic string this app could write would
 * improve on any of them, and replacing them with one would throw away the only
 * part of a refusal a user can act on.
 *
 * The two that DO need our own words are the ones with nothing to say: a
 * transport failure has no message worth showing, and a 404 is deliberately
 * indistinguishable from "no such route", so its sentence has to be written
 * here rather than read off the wire.
 */
export function brandErrorMessage(
  error: unknown,
  strings: { readonly network: string; readonly notAdmin: string },
): string {
  if (!(error instanceof BrandAdminError)) return strings.network;
  switch (error.kind) {
    case "notAdmin":
      return strings.notAdmin;
    case "invalidField":
    case "unsupportedType":
    case "tooLarge":
      return error.message;
    default:
      return strings.network;
  }
}
