/**
 * Delegated sign-in, client side (epic M2; contract
 * `docs/specs/L2-accounts-api-contract.md` §3).
 *
 * # The flow, and the one rule that governs this file
 *
 * A portal's backend signs a short-lived JWT and sends the browser to
 * `https://<host>/auth/delegated#token=<jwt>`. The FRAGMENT is the whole
 * security design of the hand-off: it is never sent to a server, so the token
 * reaches no access log, no proxy log and no `Referer` header. Everything
 * here exists to keep that true for the fraction of a second the token spends
 * inside the browser:
 *
 *   1. read `location.hash`,
 *   2. erase it with `history.replaceState` BEFORE anything else runs — in
 *      particular before any network call, so the token cannot be in the
 *      address bar when a request is in flight and cannot end up in a history
 *      entry the user might share,
 *   3. only then POST it to the exchange, in a request BODY,
 *   4. never log it, never put it in a URL, never keep it after step 3.
 *
 * The ordering is not a style preference; it is `takeDelegatedToken` being a
 * separate function from `exchangeDelegatedToken`, so that the erase cannot
 * be accidentally moved after the fetch by a later edit. A test asserts
 * `location.hash === ""` after the handler runs and that no request URL
 * contains the token.
 *
 * # Why the errors are classified here rather than reused from api/errors
 *
 * The exchange's status codes mean different things from the JMAP API's. A
 * 401 on `/jmap/api` means "your password is wrong" and the answer is the
 * login form. A 401 HERE means "this link is dead" and the login form is the
 * WRONG answer — the user has no password to type, so offering the form
 * invites them to fail. §3.4 makes that split explicit and this module keeps
 * it, mapping the statuses onto an outcome the caller renders directly.
 */

/** Where the portal sends the browser. */
export const DELEGATED_ROUTE = "/auth/delegated";

const EXCHANGE_ENDPOINT = "/auth/delegated/exchange";
const RENEW_ENDPOINT = "/auth/delegated/renew";
const LOGOUT_ENDPOINT = "/auth/delegated/logout";

/**
 * The Session response (§3.4), as the server sends it.
 *
 * `jmap.sessionUrl` is deliberately NOT modelled: the app already knows where
 * the JMAP Session lives, and following a URL handed back by an auth response
 * is the shape of bug the same-origin reduction in `api/jmap.ts` exists to
 * prevent.
 */
export interface DelegatedSessionResponse {
  readonly tokenType: string;
  readonly sessionToken: string;
  readonly expiresAt: string;
  readonly renewAfter: string;
  readonly absoluteExpiresAt: string;
  readonly account: { readonly address: string; readonly name?: string };
  readonly readOnly?: boolean;
}

/**
 * What an exchange or a renewal produced.
 *
 * A discriminated union rather than a thrown error, because every one of
 * these is a SCREEN the caller has to render, not an exception: "invalid" is
 * the dead-link screen, "not-provisioned" is the existing one, "unavailable"
 * is a retry. Throwing would push the classification into a catch block where
 * the compiler stops helping.
 */
export type DelegatedOutcome =
  /** A session was issued. */
  | { readonly kind: "ok"; readonly session: DelegatedSessionResponse }
  /**
   * 401 — the single refusal §3.4 mandates. Bad signature, wrong audience,
   * expired, replayed, malformed: one message for all of them, by design. The
   * user sees "open the mail again from the portal", never a login form.
   */
  | { readonly kind: "invalid" }
  /** 403 `notProvisioned` — the mailbox is not set up in Moov. */
  | { readonly kind: "not-provisioned" }
  /** 403 `suspended` / `disabled` — the account cannot be used. */
  | { readonly kind: "unusable"; readonly code: string }
  /** 404 — delegated sign-in is not configured for this host. */
  | { readonly kind: "not-configured" }
  /** 429, 503 or a transport failure: nothing is wrong with the link. */
  | { readonly kind: "unavailable"; readonly retryAfterSeconds?: number };

/**
 * Takes the token out of the URL fragment and erases the fragment.
 *
 * Returns undefined when there is no token, which is the ordinary case for
 * every other route. The erase happens whether or not a token was found: a
 * fragment on this route has no other meaning, and leaving a malformed one in
 * the address bar only invites a reload that fails the same way.
 *
 * `history.replaceState` rather than assigning `location.hash = ""`: the
 * assignment leaves a bare `#` in the URL and pushes a history entry, so Back
 * would return to the page carrying the token. replaceState overwrites the
 * entry in place — the token is gone from history, not merely from view.
 */
export function takeDelegatedToken(win: Window = window): string | undefined {
  const hash = win.location.hash;
  let token: string | undefined;
  if (hash.startsWith("#")) {
    const params = new URLSearchParams(hash.slice(1));
    const raw = params.get("token");
    if (raw !== null && raw !== "") token = raw;
  }

  try {
    /*
     * The path and query are preserved and only the fragment dropped. Passing
     * `location.pathname + location.search` rather than a fixed string keeps
     * a deep link's query intact — and the URL must stay same-origin, which
     * a relative string guarantees without a parse.
     */
    win.history.replaceState(
      win.history.state,
      "",
      `${win.location.pathname}${win.location.search}`,
    );
  } catch {
    /*
     * Some embedded contexts refuse replaceState. The token must still be
     * used — refusing to sign the user in because the address bar could not
     * be tidied would be the worse failure — but the caller has to know the
     * URL is still dirty, and it does: the fragment is visibly unchanged.
     */
  }

  return token;
}

/** POSTs the token to the exchange (§3.4). */
export async function exchangeDelegatedToken(
  token: string,
  fetchImpl: typeof fetch = globalThis.fetch.bind(globalThis),
  signal?: AbortSignal,
): Promise<DelegatedOutcome> {
  return postDelegated(
    EXCHANGE_ENDPOINT,
    { headers: {}, body: JSON.stringify({ token }) },
    fetchImpl,
    signal,
  );
}

/**
 * Renews a session (§3.5): a new token with the same shape, authenticated
 * with the CURRENT one. The old token stays valid for 60 s server-side, so a
 * request already in flight does not fail.
 */
export async function renewDelegatedSession(
  sessionToken: string,
  fetchImpl: typeof fetch = globalThis.fetch.bind(globalThis),
  signal?: AbortSignal,
): Promise<DelegatedOutcome> {
  return postDelegated(
    RENEW_ENDPOINT,
    { headers: { Authorization: `Bearer ${sessionToken}` } },
    fetchImpl,
    signal,
  );
}

/**
 * Ends a session server-side (§3.5). Never throws and never reports failure:
 * the route answers 204 even for an already-dead session, and a sign-out that
 * could fail on the user would be a worse bug than one that occasionally
 * leaves a session to expire on its own — the local credential is erased
 * either way by the caller.
 */
export async function logoutDelegatedSession(
  sessionToken: string,
  fetchImpl: typeof fetch = globalThis.fetch.bind(globalThis),
): Promise<void> {
  try {
    await fetchImpl(LOGOUT_ENDPOINT, {
      method: "POST",
      headers: { Authorization: `Bearer ${sessionToken}`, Accept: "application/json" },
      credentials: "omit",
    });
  } catch {
    // Offline, or the server is down. The session expires on its own.
  }
}

interface PostShape {
  readonly headers: Record<string, string>;
  readonly body?: string;
}

async function postDelegated(
  endpoint: string,
  shape: PostShape,
  fetchImpl: typeof fetch,
  signal?: AbortSignal,
): Promise<DelegatedOutcome> {
  let response: Response;
  try {
    response = await fetchImpl(endpoint, {
      method: "POST",
      headers: {
        ...shape.headers,
        Accept: "application/json",
        // The exchange carries a JSON body; the renewal carries none, and
        // sending a Content-Type without one would be a lie the server is
        // entitled to refuse (its readTokenBody insists on application/json
        // exactly when it reads a body).
        ...(shape.body === undefined ? {} : { "Content-Type": "application/json" }),
      },
      ...(shape.body === undefined ? {} : { body: shape.body }),
      // The Authorization header is explicit, so ambient cookies must not
      // ride along — the same rule JmapClient follows.
      credentials: "omit",
      signal: signal ?? null,
    });
  } catch {
    return { kind: "unavailable" };
  }

  if (response.ok) {
    try {
      const session = (await response.json()) as DelegatedSessionResponse;
      if (typeof session.sessionToken !== "string" || session.sessionToken === "") {
        // A 200 without a token is a broken server, not a bad link.
        return { kind: "unavailable" };
      }
      return { kind: "ok", session };
    } catch {
      return { kind: "unavailable" };
    }
  }

  return classifyRefusal(response, await readCode(response));
}

/**
 * Reads the RFC 7807 extension member `code` the 403s carry (§3.4).
 *
 * Never throws: a body that is not a problem document simply yields undefined
 * and the caller falls back to the status alone.
 */
async function readCode(response: Response): Promise<string | undefined> {
  const contentType = response.headers.get("Content-Type") ?? "";
  if (!contentType.includes("json")) return undefined;
  try {
    const body: unknown = await response.json();
    if (typeof body === "object" && body !== null) {
      const code = (body as Record<string, unknown>).code;
      if (typeof code === "string" && code !== "") return code;
    }
  } catch {
    // Truncated or non-JSON; the status is enough.
  }
  return undefined;
}

function classifyRefusal(response: Response, code: string | undefined): DelegatedOutcome {
  switch (response.status) {
    case 401:
      // THE single refusal. Nothing about WHY reaches here, by design.
      return { kind: "invalid" };
    case 403:
      /*
       * `notProvisioned` is the one the user can do nothing about except ask
       * an administrator, and the app already has a screen for it. The other
       * codes (`suspended`, `disabled`) are distinct facts with distinct
       * copy, so they are kept rather than folded into one — an unknown code
       * lands in the same member and renders the generic explanation, which
       * keeps a future server code from becoming a blank screen.
       */
      return code === "notProvisioned"
        ? { kind: "not-provisioned" }
        : { kind: "unusable", code: code ?? "unknown" };
    case 404:
      return { kind: "not-configured" };
    case 400:
      /*
       * A malformed body is our bug, not the user's, but the only honest
       * screen is still the dead-link one: there is nothing they can do
       * differently, and the token they were handed did not work.
       */
      return { kind: "invalid" };
    default:
      break;
  }
  if (response.status === 429 || response.status >= 500) {
    const retry = parseRetryAfter(response);
    return retry === undefined
      ? { kind: "unavailable" }
      : { kind: "unavailable", retryAfterSeconds: retry };
  }
  return { kind: "unavailable" };
}

function parseRetryAfter(response: Response): number | undefined {
  const raw = response.headers.get("Retry-After");
  if (raw === null) return undefined;
  const seconds = Number(raw);
  return Number.isFinite(seconds) && seconds >= 0 ? seconds : undefined;
}
