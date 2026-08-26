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
    const apiUrl = this.session?.apiUrl ?? `${this.baseUrl}/jmap/api`;
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
