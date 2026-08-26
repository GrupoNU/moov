/**
 * The JMAP client (arbitration W-A3: ours, typed, hand-written).
 *
 * # Why we wrote this instead of using a library
 *
 * W-A3's reasoning: our server is finished and we know its surface exactly, so
 * a generic client would buy us nothing and cost us the types. It would also
 * tie us to its assumptions about auth, batching and error shape, when the
 * whole point of the error taxonomy (errors.ts) is that OUR distinctions
 * survive to the UI.
 *
 * # What P1 needs and what is here for P2
 *
 * P1 needs exactly one thing from JMAP: proof that a credential works, which
 * is a GET of the Session object. Everything below that — the request builder,
 * back-references, batching — is written now because the shape of the client
 * is a design decision, not because P1 calls it. P2 adds methods, not
 * plumbing.
 */

import { ApiError, apiErrorFromResponse, apiErrorFromThrown } from "./errors";

/** RFC 8620 §2 capability URNs. */
export const CAP_CORE = "urn:ietf:params:jmap:core";
export const CAP_MAIL = "urn:ietf:params:jmap:mail";
export const CAP_SUBMISSION = "urn:ietf:params:jmap:submission";

/** Where the Session object lives (never a redirect — see routes.go). */
export const SESSION_ENDPOINT = "/.well-known/jmap";

/** An account as the Session object describes it (RFC 8620 §2). */
export interface JmapAccount {
  readonly name: string;
  readonly isPersonal: boolean;
  readonly isReadOnly: boolean;
  readonly accountCapabilities: Readonly<Record<string, unknown>>;
}

/** The Session object (RFC 8620 §2). Only the fields the app reads are typed. */
export interface JmapSession {
  readonly capabilities: Readonly<Record<string, unknown>>;
  readonly accounts: Readonly<Record<string, JmapAccount>>;
  readonly primaryAccounts: Readonly<Record<string, string>>;
  readonly username: string;
  readonly apiUrl: string;
  readonly downloadUrl: string;
  readonly uploadUrl: string;
  readonly eventSourceUrl: string;
  readonly state: string;
}

/**
 * One method call in a request: [name, arguments, client id] (RFC 8620 §3.2).
 *
 * The client id is what back-references point at, which is why it is required
 * rather than generated: a caller building a batch needs to name its own steps.
 */
export type JmapInvocation = readonly [
  method: string,
  args: Readonly<Record<string, unknown>>,
  clientId: string,
];

/** A JMAP Response (RFC 8620 §3.4). */
export interface JmapResponse {
  readonly methodResponses: readonly JmapInvocation[];
  readonly sessionState: string;
  readonly createdIds?: Readonly<Record<string, string>>;
}

/**
 * A back-reference (RFC 8620 §3.7): "use the result of an earlier call in this
 * same request as this argument".
 *
 * Modelled as a helper rather than left to callers to spell, because the `#`
 * prefix on the ARGUMENT NAME (not the value) is the part everyone gets wrong
 * the first time, and a typo produces a confusing server error rather than a
 * type error. Used from P2 onward — the Email/query → Email/get pair is the
 * canonical case.
 */
export interface BackReference {
  readonly resultOf: string;
  readonly name: string;
  readonly path: string;
}

/**
 * Builds the `#name` argument entry for a back-reference.
 *
 * @example
 *   ["Email/get", { accountId, ...backRef("ids", { resultOf: "q", name: "Email/query", path: "/ids" }) }, "g"]
 */
export function backRef(
  argumentName: string,
  reference: BackReference,
): Record<string, BackReference> {
  return { [`#${argumentName}`]: reference };
}

/**
 * Credentials for HTTP Basic (arbitration J-A1).
 *
 * Basic is what the server implements today. It is held in memory only — see
 * session storage in ../auth/session.ts for what is and is not persisted, and
 * why.
 */
export interface BasicCredentials {
  readonly username: string;
  readonly password: string;
}

/**
 * Encodes credentials for the Authorization header.
 *
 * `btoa` handles only Latin-1, and both an email local part and a password may
 * legitimately be UTF-8 — the server announces `charset="UTF-8"` in its
 * challenge (RFC 7617 §2.1), so it expects UTF-8 bytes. TextEncoder produces
 * them; without this step a password with an accent authenticates locally and
 * fails in production, which is precisely the bug that is hardest to reproduce.
 */
export function encodeBasicCredentials({ username, password }: BasicCredentials): string {
  const bytes = new TextEncoder().encode(`${username}:${password}`);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return `Basic ${btoa(binary)}`;
}

/**
 * Reduces a server-advertised URL to a same-origin path.
 *
 * # Why the Session's own URLs cannot be used verbatim
 *
 * RFC 8620 §2 lets the Session object advertise absolute URLs, and ours does:
 * `https://moov.atmosfera.cloud/jmap/api`. Using that string directly works in
 * production — where the app is served from that very origin — and breaks
 * everywhere else, because the browser then makes a CROSS-origin request that
 * needs CORS. In development the app runs on `localhost:5173` behind a proxy
 * whose entire purpose (see vite.config.ts) is to reproduce production's
 * same-origin topology; an absolute URL steps around the proxy and is refused
 * by the preflight, which is exactly the class of bug that configuration was
 * written to design out.
 *
 * So the PATH the server advertises is honoured — it is the server's own
 * routing decision and may change — while the ORIGIN is always ours. A URL on
 * a different origin than the page is deliberately reduced to its path rather
 * than followed, because a JMAP client that follows an origin handed to it by
 * a response is one redirect away from sending Basic credentials somewhere
 * else.
 */
function sameOrigin(advertised: string | undefined, fallback: string): string {
  if (advertised === undefined || advertised === "") return fallback;
  if (advertised.startsWith("/")) return advertised;
  try {
    const parsed = new URL(advertised);
    /*
     * `pathname` percent-encodes the braces of a URI Template — `{accountId}`
     * comes back as `%7BaccountId%7D` — so a later `.replace("{accountId}", …)`
     * silently matches nothing and the placeholder ships to the server
     * unexpanded. Decoding restores the template. It is safe here because the
     * only thing being decoded is a path we are about to substitute into, and
     * the VALUES are encoded individually at substitution time.
     */
    return decodeURIComponent(`${parsed.pathname}${parsed.search}`);
  } catch {
    return fallback;
  }
}

/** Options for constructing a {@link JmapClient}. */
export interface JmapClientOptions {
  /**
   * The API origin. Empty string — the default — means same-origin, which is
   * how the app is served in production (Caddy fronts both) and in development
   * (the Vite proxy).
   */
  readonly baseUrl?: string;
  readonly fetchImpl?: typeof fetch;
}

/**
 * A typed JMAP client for one authenticated user.
 *
 * One client, one credential: rebuilding it is how the app changes user, which
 * makes "which credential is this request using" answerable by construction.
 */
export class JmapClient {
  private readonly authorization: string;
  private readonly baseUrl: string;
  private readonly fetchImpl: typeof fetch;
  private session: JmapSession | undefined;

  constructor(credentials: BasicCredentials, options: JmapClientOptions = {}) {
    this.authorization = encodeBasicCredentials(credentials);
    this.baseUrl = (options.baseUrl ?? "").replace(/\/+$/, "");
    this.fetchImpl = options.fetchImpl ?? globalThis.fetch.bind(globalThis);
  }

  /**
   * Fetches the Session object — and, incidentally, IS the login check.
   *
   * There is no separate login endpoint: the server authenticates every
   * request with Basic, so "are these credentials good" is answered by any
   * authenticated request, and the Session is the cheapest one that also
   * returns everything the app needs to start (RFC 8620 §2). A 401 here is a
   * wrong password; a 403 is the unprovisioned case.
   */
  async fetchSession(signal?: AbortSignal): Promise<JmapSession> {
    const response = await this.request(SESSION_ENDPOINT, { method: "GET" }, signal);
    const session = (await response.json()) as JmapSession;
    this.session = session;
    return session;
  }

  /** The last fetched session, if any. */
  get currentSession(): JmapSession | undefined {
    return this.session;
  }

  /**
   * Issues a JMAP Request (RFC 8620 §3.3) with the given method calls.
   *
   * `using` defaults to core+mail, the two capabilities every mail call needs;
   * a caller doing submission passes its own list. The server rejects a
   * capability it does not implement, which is the J1 truthfulness rule seen
   * from the client side.
   */
  async call(
    methodCalls: readonly JmapInvocation[],
    using: readonly string[] = [CAP_CORE, CAP_MAIL],
    signal?: AbortSignal,
  ): Promise<JmapResponse> {
    const apiUrl = sameOrigin(this.session?.apiUrl, `${this.baseUrl}/jmap/api`);
    const response = await this.request(
      apiUrl,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ using, methodCalls }),
      },
      signal,
    );
    return (await response.json()) as JmapResponse;
  }

  /**
   * Downloads a blob's bytes.
   *
   * # Why this exists rather than an `<a download href>`
   *
   * The download route authenticates with HTTP Basic, and a browser navigation
   * — an anchor click, an `<img src>`, a `window.open` — sends no Authorization
   * header. Against the live pilot that produces a 401 with
   * `WWW-Authenticate: Basic`, which a browser answers by showing its own
   * credential prompt: the user is asked to log in again, into a native dialog,
   * to download their own attachment.
   *
   * So the bytes come through `fetch` with the header attached, and the caller
   * turns the Blob into an object URL. The cost is that the whole blob is
   * buffered in memory; with `maxSizeUpload` at 50 MB that is bounded and
   * acceptable, and it is the only correct option until the server grows
   * pre-signed download tokens.
   */
  async downloadBlob(
    accountId: string,
    blobId: string,
    name: string,
    type: string,
    signal?: AbortSignal,
  ): Promise<Blob> {
    const url = this.downloadUrlFor(accountId, blobId, name, type);
    const response = await this.request(url, { method: "GET" }, signal);
    return await response.blob();
  }

  /**
   * Signs remote-image URLs for the image proxy (W-A4, ADR §5).
   *
   * An auxiliary REST endpoint like /branding, not a JMAP method — the RFC
   * has no vocabulary for "mint me a capability URL". The response maps each
   * ACCEPTED original URL to a relative `/jmap/imgproxy?...` path carrying
   * an expiry and an HMAC; URLs the server refused (bad scheme, private
   * address, oversized) are simply absent, and the client leaves those
   * images blocked. The path is relative and requested same-origin by the
   * message iframe's <img>, which can carry no credentials — the HMAC is
   * the authorization.
   */
  async signImageProxyUrls(
    urls: readonly string[],
    signal?: AbortSignal,
  ): Promise<Record<string, string>> {
    const response = await this.request(
      "/jmap/imgproxy/sign",
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ urls }),
      },
      signal,
    );
    const body = (await response.json()) as { urls?: Record<string, string> };
    return body.urls ?? {};
  }

  /**
   * Expands the Session's `downloadUrl` template (RFC 8620 §2).
   *
   * NOTE the deliberate correction: the server advertises the template with
   * `?accept={type}`, but its handler reads the `type` query parameter. Passing
   * `accept=` therefore yields `application/octet-stream` for everything. We
   * emit `type=` so an allowlisted content type is honoured, and the mismatch
   * is recorded as a server gap rather than silently worked around forever.
   */
  downloadUrlFor(accountId: string, blobId: string, name: string, type: string): string {
    // Same-origin for the same reason as apiUrl: the advertised URL is
    // absolute, and following it would be a cross-origin request carrying
    // Basic credentials.
    const base = sameOrigin(
      this.session?.downloadUrl,
      `${this.baseUrl}/jmap/download/{accountId}/{blobId}/{name}`,
    );
    const expanded = base
      .replace("{accountId}", encodeURIComponent(accountId))
      .replace("{blobId}", encodeURIComponent(blobId))
      .replace("{name}", encodeURIComponent(name));
    // Drop whatever query the template carried and set the one the server reads.
    const withoutQuery = expanded.split("?")[0] ?? expanded;
    return `${withoutQuery}?type=${encodeURIComponent(type)}`;
  }

  /**
   * The single path every request takes: attaches auth, normalises failures
   * into ApiError, and never lets a raw fetch rejection escape.
   */
  private async request(
    url: string,
    init: RequestInit,
    signal?: AbortSignal,
  ): Promise<Response> {
    const absolute = url.startsWith("http") ? url : `${this.baseUrl}${url}`;
    let response: Response;
    try {
      response = await this.fetchImpl(absolute, {
        ...init,
        signal: signal ?? null,
        headers: {
          ...(init.headers as Record<string, string> | undefined),
          Authorization: this.authorization,
          Accept: "application/json",
        },
        // The Authorization header is sent explicitly, so the browser must not
        // also attach ambient credentials — that combination is what the CORS
        // wildcard rule in cors.go exists to keep unrepresentable.
        credentials: "omit",
      });
    } catch (error) {
      throw apiErrorFromThrown(error);
    }

    if (!response.ok) {
      throw await apiErrorFromResponse(response);
    }
    return response;
  }
}

/**
 * Verifies a credential pair and returns the session.
 *
 * This is the whole of P1's JMAP surface. It exists as a function rather than
 * as a method so the login screen depends on one narrow thing it can stub.
 */
export async function authenticate(
  credentials: BasicCredentials,
  options: JmapClientOptions = {},
  signal?: AbortSignal,
): Promise<{ client: JmapClient; session: JmapSession }> {
  const client = new JmapClient(credentials, options);
  const session = await client.fetchSession(signal);
  return { client, session };
}

/** Re-exported so callers need one import for the client and its errors. */
export { ApiError };
