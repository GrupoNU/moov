/**
 * The error taxonomy.
 *
 * # Why this file exists
 *
 * The pilot's hard lesson, recorded in L2-pwa §4 (epic P4) and in the project's
 * history: our server answers a login from an unprovisioned mailbox with a
 * precise, actionable 403 —
 *
 *   "this mailbox authenticated correctly but is not provisioned in Moov;
 *    an administrator must add it with `moovctl account add` first"
 *
 * — and Bulwark rendered that as "an error occurred". The information was
 * there, on the wire, and the client threw it away. A user in that state
 * cannot possibly guess what to do.
 *
 * So the client's job is to preserve the DISTINCTION the server made. This
 * module turns a transport outcome into a closed set of kinds, and the UI maps
 * each kind to a message that tells the user what actually happened and what to
 * do next. The set is closed (a discriminated union) so that adding a kind
 * without translating it is a TYPE ERROR, not a generic fallback rendered to
 * someone's screen.
 */

/**
 * What went wrong, in terms the UI can act on.
 *
 * The names describe the USER'S situation, not the HTTP status, because that
 * is what the message has to explain.
 */
export type ApiErrorKind =
  /** The credentials were rejected by Dovecot. 401. */
  | "invalid-credentials"
  /**
   * The credentials were CORRECT, but the mailbox is not provisioned in Moov
   * (or is disabled). 403. This is the one the pilot lost, and the one whose
   * message must name the remedy: an administrator has to add the account.
   */
  | "not-provisioned"
  /** Too many attempts; the lockout is active. 429, with Retry-After. */
  | "rate-limited"
  /** The server is reachable but broken, or the auth backend is down. 5xx. */
  | "server-error"
  /** The server could not be reached at all: offline, DNS, TLS, CORS. */
  | "network"
  /** The request was aborted (navigation, timeout). */
  | "aborted"
  /** A JMAP-level error the transport succeeded in delivering. */
  | "jmap"
  /** Anything genuinely unclassifiable — kept so the union is total. */
  | "unknown";

/**
 * An error with a kind the UI can switch on.
 *
 * `detail` carries the SERVER'S OWN message when it sent one (our server sends
 * RFC 7807 problem documents with a human-readable `detail`). The UI prefers
 * its own translated copy for the known kinds — a translated sentence beats an
 * English one from a daemon — but keeps the server's text for diagnostics and
 * for the kinds it cannot phrase better itself.
 */
export class ApiError extends Error {
  readonly kind: ApiErrorKind;
  /** The HTTP status, when there was a response. */
  readonly status: number | undefined;
  /** The server's own explanation, when it sent one. */
  readonly detail: string | undefined;
  /** Seconds to wait, parsed from Retry-After on a 429. */
  readonly retryAfterSeconds: number | undefined;

  constructor(
    kind: ApiErrorKind,
    message: string,
    options: {
      status?: number;
      detail?: string;
      retryAfterSeconds?: number;
      cause?: unknown;
    } = {},
  ) {
    super(message, options.cause !== undefined ? { cause: options.cause } : undefined);
    this.name = "ApiError";
    this.kind = kind;
    this.status = options.status;
    this.detail = options.detail;
    this.retryAfterSeconds = options.retryAfterSeconds;
  }
}

/**
 * Classifies an HTTP response into a kind.
 *
 * The mapping is the inverse of what internal/jmaphttp/auth.go writes, and the
 * comments name the server function responsible for each status so the two can
 * be checked against each other by a reader.
 */
export function kindForStatus(status: number): ApiErrorKind {
  // 401 — Authenticator.challenge: Dovecot rejected the password, or no
  // credentials were sent.
  if (status === 401) return "invalid-credentials";
  // 403 — Authenticator.requireProvisioned: the LOGIN succeeded and the
  // account is absent from the store or disabled. THE distinction the pilot
  // lost.
  if (status === 403) return "not-provisioned";
  // 429 — Authenticator.tooMany, or the maxConcurrentRequests gate.
  if (status === 429) return "rate-limited";
  // 503 is the auth backend being unreachable (Dovecot down); every other 5xx
  // is the server itself. Both are "not your fault, try later".
  if (status >= 500) return "server-error";
  return "unknown";
}

/**
 * Reads the `detail` of an RFC 7807 problem document, if the response carries
 * one.
 *
 * Never throws: a body that is not a problem document simply yields undefined,
 * and the caller falls back to its own copy.
 */
export async function readProblemDetail(response: Response): Promise<string | undefined> {
  const contentType = response.headers.get("Content-Type") ?? "";
  if (!contentType.includes("json")) return undefined;
  try {
    const body: unknown = await response.json();
    if (typeof body === "object" && body !== null) {
      const detail = (body as Record<string, unknown>).detail;
      if (typeof detail === "string" && detail.trim() !== "") {
        return detail.trim();
      }
    }
  } catch {
    // A truncated or non-JSON body is not itself an error worth reporting.
  }
  return undefined;
}

/** Parses Retry-After (delta-seconds form, which is what our server sends). */
export function parseRetryAfter(response: Response): number | undefined {
  const raw = response.headers.get("Retry-After");
  if (raw === null) return undefined;
  const seconds = Number.parseInt(raw.trim(), 10);
  return Number.isFinite(seconds) && seconds >= 0 ? seconds : undefined;
}

/**
 * Builds an ApiError from a failed HTTP response, reading the problem body.
 */
export async function apiErrorFromResponse(response: Response): Promise<ApiError> {
  const kind = kindForStatus(response.status);
  const detail = await readProblemDetail(response);
  const retryAfterSeconds = parseRetryAfter(response);
  return new ApiError(kind, detail ?? `HTTP ${response.status}`, {
    status: response.status,
    ...(detail !== undefined ? { detail } : {}),
    ...(retryAfterSeconds !== undefined ? { retryAfterSeconds } : {}),
  });
}

/**
 * Classifies a thrown value from `fetch`.
 *
 * `fetch` rejects with a TypeError for every transport failure — offline, DNS,
 * TLS, and a CORS refusal — which are indistinguishable to script by design.
 * They collapse to "network", and the UI's message for it says "could not
 * reach the server", which is true for all of them.
 */
export function apiErrorFromThrown(error: unknown): ApiError {
  if (error instanceof ApiError) return error;
  if (error instanceof DOMException && error.name === "AbortError") {
    return new ApiError("aborted", "The request was cancelled.", { cause: error });
  }
  if (error instanceof TypeError) {
    return new ApiError("network", "The server could not be reached.", { cause: error });
  }
  return new ApiError("unknown", error instanceof Error ? error.message : String(error), {
    cause: error,
  });
}
