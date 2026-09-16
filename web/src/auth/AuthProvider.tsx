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

import { ApiError, apiErrorFromThrown } from "../api/errors";
import {
  authenticate,
  type AuthCredential,
  type BasicCredentials,
  type JmapSession,
} from "../api/jmap";
import {
  DELEGATED_ROUTE,
  exchangeDelegatedToken,
  logoutDelegatedSession,
  renewDelegatedSession,
  takeDelegatedToken,
  type DelegatedOutcome,
  type DelegatedSessionResponse,
} from "./delegated";
import {
  clearSession,
  defaultSessionStorage,
  isBeyondAbsoluteLifetime,
  isDueForRenewal,
  loadSession,
  saveBearerSession,
  saveSession,
  type PersistedSession,
  type SessionStorageLike,
  type StoredBearerSession,
} from "./session";

/**
 * Authentication state and the transitions that change it.
 *
 * The state is a discriminated union rather than a bag of booleans, because
 * the combinations a bag allows ("loading AND authenticated AND has an error")
 * are exactly the ones that produce a screen showing two things at once. With
 * a union, the app renders one of a few things and the compiler enforces that.
 *
 * # The state delegated sign-in adds, and why it is not just an error
 *
 * `link-dead` is a FIFTH state rather than an `anonymous` with an error, and
 * that distinction is the point of the whole M2 client work (contract §3.7).
 * `anonymous` renders the login form. A user who arrived through a portal has
 * no password — the mailbox was created for them and the password discarded
 * at provisioning — so showing them a form is inviting them to fail at
 * something impossible. `link-dead` renders "open the mail again from the
 * portal", which is the only action that works.
 */
export type AuthState =
  /** Deciding whether a stored credential still works. Renders a splash. */
  | { readonly status: "restoring" }
  /** No credential, or one that was rejected. Renders the login screen. */
  | { readonly status: "anonymous"; readonly error?: ApiError }
  /** A sign-in is in flight. */
  | { readonly status: "authenticating" }
  /** Signed in. */
  | {
      readonly status: "authenticated";
      readonly session: JmapSession;
      readonly username: string;
      /** How the caller authenticated; `readOnly` rides with the bearer case. */
      readonly delegated?: StoredBearerSession;
    }
  /**
   * A delegated link that cannot be used: expired, invalid, already spent, or
   * for an account that cannot sign in. NEVER the login form.
   */
  | { readonly status: "link-dead"; readonly reason: DelegatedFailure };

/** What went wrong with a delegated link, in terms the screen renders. */
export type DelegatedFailure =
  /** The single 401: one message for every token-level refusal (§3.4). */
  | { readonly kind: "invalid" }
  /** The mailbox is not set up in Moov. Reuses the existing screen's copy. */
  | { readonly kind: "not-provisioned" }
  /** The account exists but cannot be used (suspended, disabled). */
  | { readonly kind: "unusable"; readonly code: string }
  /** Delegated sign-in is not configured for this host. */
  | { readonly kind: "not-configured" }
  /** Nothing is wrong with the link; the server could not answer. */
  | { readonly kind: "unavailable"; readonly retryAfterSeconds?: number };

interface AuthContextValue {
  readonly state: AuthState;
  /** Attempts a sign-in. Never throws: failures land in `state.error`. */
  readonly signIn: (credentials: BasicCredentials) => Promise<void>;
  readonly signOut: () => void;
  /**
   * What the app must call when ANY authenticated request answers 401.
   *
   * With Basic this is the existing behaviour: drop the credential, back to
   * the login form. With a bearer session it is the whole point of §3.7 —
   * drop the credential and show the dead-link screen instead, because the
   * form would ask for a password that does not exist.
   */
  readonly onUnauthorized: () => void;
  /** The credential the HTTP layer should send, or undefined when signed out. */
  readonly credential: AuthCredential | undefined;
  /** True when the account is in its read-only retention phase (§2.4). */
  readonly readOnly: boolean;
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

export interface AuthProviderProps {
  readonly children: ReactNode;
  /** Injected by tests. */
  readonly storage?: SessionStorageLike;
  readonly authenticateImpl?: typeof authenticate;
  /** Injected by tests; production uses the module's own fetch. */
  readonly fetchImpl?: typeof fetch;
  /**
   * Skips restoring a stored session. Tests that want a clean login screen set
   * this rather than having to stub storage.
   */
  readonly skipRestore?: boolean;
}

/** The exchange response, as this module stores it. */
function toStoredBearer(response: DelegatedSessionResponse): StoredBearerSession {
  const name = response.account.name;
  return {
    kind: "bearer",
    username: response.account.address,
    token: response.sessionToken,
    expiresAt: response.expiresAt,
    renewAfter: response.renewAfter,
    absoluteExpiresAt: response.absoluteExpiresAt,
    readOnly: response.readOnly === true,
    ...(typeof name === "string" && name !== "" ? { displayName: name } : {}),
  };
}

/** Maps a non-ok outcome onto the failure the dead-link screen renders. */
function failureFor(outcome: Exclude<DelegatedOutcome, { kind: "ok" }>): DelegatedFailure {
  return outcome;
}

export function AuthProvider({
  children,
  storage,
  authenticateImpl = authenticate,
  fetchImpl,
  skipRestore = false,
}: AuthProviderProps): React.JSX.Element {
  const storageRef = useRef<SessionStorageLike>(storage ?? defaultSessionStorage());
  const [state, setState] = useState<AuthState>(() =>
    skipRestore ? { status: "anonymous" } : { status: "restoring" },
  );

  /*
   * The bearer session, held in a ref as well as in state.
   *
   * The ref is what the renewal timer and the visibility handler read. They
   * are long-lived callbacks registered once; closing over the state value
   * would pin them to whatever session existed when they were created, so the
   * first renewal would replace the token and every subsequent one would
   * present the stale one and fail.
   */
  const bearerRef = useRef<StoredBearerSession | undefined>(undefined);

  /**
   * Adopts a delegated session: persists it, remembers it, and fetches the
   * JMAP Session with it.
   *
   * The JMAP fetch is not ceremony. The exchange proves the TOKEN is good; it
   * does not prove the app can talk JMAP as this account, and landing in the
   * shell only to fail on the first method call is exactly the failure mode
   * the Basic path's revalidation exists to avoid.
   */
  const adoptBearer = useCallback(
    async (stored: StoredBearerSession, signal?: AbortSignal): Promise<boolean> => {
      try {
        const { session } = await authenticateImpl({ token: stored.token }, {}, signal);
        bearerRef.current = stored;
        saveBearerSession(stored, storageRef.current);
        setState({
          status: "authenticated",
          session,
          username: stored.username,
          delegated: stored,
        });
        return true;
      } catch (error) {
        const apiError = apiErrorFromThrown(error);
        if (apiError.kind === "aborted") return false;
        bearerRef.current = undefined;
        clearSession(storageRef.current);
        setState({
          status: "link-dead",
          reason:
            apiError.kind === "not-provisioned"
              ? { kind: "not-provisioned" }
              : { kind: "invalid" },
        });
        return false;
      }
    },
    [authenticateImpl],
  );

  /**
   * Startup: the delegated landing route first, then a stored credential.
   *
   * The ORDER matters and is not arbitrary. A token in the fragment is an
   * explicit instruction from the portal to open THIS mailbox, and it must
   * win over whatever the tab happened to be holding — otherwise a second
   * event link opened in the same tab would silently show the first
   * mailbox's mail.
   */
  useEffect(() => {
    if (skipRestore) return undefined;

    const controller = new AbortController();
    let cancelled = false;

    void (async () => {
      /*
       * The fragment is read and ERASED before anything else, including
       * before the decision about what to do with it. `takeDelegatedToken`
       * does both in one call precisely so a later edit cannot slip a network
       * request between the read and the erase.
       */
      const onDelegatedRoute =
        typeof window !== "undefined" && window.location.pathname === DELEGATED_ROUTE;
      const token = onDelegatedRoute ? takeDelegatedToken(window) : undefined;

      if (onDelegatedRoute && token === undefined) {
        // The route with no token: someone bookmarked the landing page.
        if (!cancelled) setState({ status: "link-dead", reason: { kind: "invalid" } });
        return;
      }

      if (token !== undefined) {
        const outcome = await exchangeDelegatedToken(token, fetchImpl, controller.signal);
        if (cancelled) return;
        if (outcome.kind !== "ok") {
          clearSession(storageRef.current);
          setState({ status: "link-dead", reason: failureFor(outcome) });
          return;
        }
        await adoptBearer(toStoredBearer(outcome.session), controller.signal);
        return;
      }

      const stored: PersistedSession | undefined = loadSession(storageRef.current);
      if (stored === undefined) {
        setState({ status: "anonymous" });
        return;
      }

      if (stored.kind === "bearer") {
        /*
         * A session past its absolute ceiling is dead and no renewal can
         * revive it (§3.4). Checking locally saves a round trip whose only
         * possible answer is a 401 — and produces the SAME screen, so the
         * user sees no difference.
         */
        if (isBeyondAbsoluteLifetime(stored)) {
          clearSession(storageRef.current);
          setState({ status: "link-dead", reason: { kind: "invalid" } });
          return;
        }
        /*
         * Due for renewal on restore: renew FIRST, then adopt. A session
         * whose window lapsed while the tab was closed would otherwise be
         * adopted, fail its first request, and land the user on the dead-link
         * screen for a session that was perfectly renewable.
         */
        let candidate = stored;
        if (isDueForRenewal(stored)) {
          const renewed = await renewDelegatedSession(
            stored.token,
            fetchImpl,
            controller.signal,
          );
          if (cancelled) return;
          if (renewed.kind === "ok") {
            candidate = toStoredBearer(renewed.session);
          } else if (renewed.kind !== "unavailable") {
            clearSession(storageRef.current);
            setState({ status: "link-dead", reason: failureFor(renewed) });
            return;
          }
          // `unavailable` falls through: the old token may still have life in
          // it, and a transient server hiccup must not sign anyone out.
        }
        await adoptBearer(candidate, controller.signal);
        return;
      }

      /*
       * The Basic path, unchanged: restore by REVALIDATING, never by
       * trusting. A password may have been changed, or the account disabled,
       * since the tab was opened.
       */
      try {
        const { session } = await authenticateImpl(stored, {}, controller.signal);
        if (!cancelled) {
          setState({ status: "authenticated", session, username: stored.username });
        }
      } catch (error) {
        if (cancelled) return;
        const apiError = apiErrorFromThrown(error);
        if (apiError.kind === "aborted") return;

        // A credential the server rejects is worthless: drop it so the next
        // reload does not repeat the round trip.
        if (apiError.kind === "invalid-credentials" || apiError.kind === "not-provisioned") {
          clearSession(storageRef.current);
        }
        // Restoration failures are NOT surfaced as login errors. The user did
        // not just type anything, so an error banner over an empty form would
        // be about an action they did not take; they simply get the login
        // screen. A genuine failure will be explained when they submit.
        setState({ status: "anonymous" });
      }
    })();

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [adoptBearer, authenticateImpl, fetchImpl, skipRestore]);

  /**
   * Renewal (§3.5): when `renewAfter` passes, and on wake-up.
   *
   * Both triggers, because neither alone is enough. A timer alone misses the
   * laptop that was asleep for six hours — browsers do not fire a timer that
   * was due during sleep at the moment it was due, so the tab would wake
   * holding an expired token and fail its next request. A visibility handler
   * alone misses the tab left open and visible all day.
   *
   * The check is the same in both cases (`isDueForRenewal` against the stored
   * deadline), so the two triggers cannot disagree about whether a renewal is
   * needed — they only differ in when they ask.
   */
  const renewIfDue = useCallback(async (): Promise<void> => {
    const current = bearerRef.current;
    if (current === undefined) return;
    if (isBeyondAbsoluteLifetime(current)) {
      bearerRef.current = undefined;
      clearSession(storageRef.current);
      setState({ status: "link-dead", reason: { kind: "invalid" } });
      return;
    }
    if (!isDueForRenewal(current)) return;

    const outcome = await renewDelegatedSession(current.token, fetchImpl);
    if (outcome.kind === "ok") {
      const next = toStoredBearer(outcome.session);
      bearerRef.current = next;
      saveBearerSession(next, storageRef.current);
      setState((previous) =>
        previous.status === "authenticated"
          ? { ...previous, username: next.username, delegated: next }
          : previous,
      );
      return;
    }
    if (outcome.kind === "unavailable") {
      // The current token is still valid for a while; try again next tick.
      return;
    }
    bearerRef.current = undefined;
    clearSession(storageRef.current);
    setState({ status: "link-dead", reason: failureFor(outcome) });
  }, [fetchImpl]);

  useEffect(() => {
    if (state.status !== "authenticated" || state.delegated === undefined) return undefined;
    if (typeof window === "undefined") return undefined;

    const tick = (): void => void renewIfDue();
    /*
     * A fixed cadence rather than a timer set to fire exactly at
     * `renewAfter`. The deadline moves on every renewal, a long setTimeout is
     * the one a sleeping machine fires late or not at all, and the check
     * itself is a comparison of two numbers — so polling once a minute costs
     * nothing and cannot get stuck holding a stale deadline.
     */
    const id = window.setInterval(tick, RENEW_POLL_MS);
    const onVisible = (): void => {
      if (document.visibilityState === "visible") tick();
    };
    document.addEventListener("visibilitychange", onVisible);
    window.addEventListener("online", tick);
    // One immediate check, for the wake-up that happened before this effect
    // was re-registered.
    tick();

    return () => {
      window.clearInterval(id);
      document.removeEventListener("visibilitychange", onVisible);
      window.removeEventListener("online", tick);
    };
  }, [renewIfDue, state]);

  const signIn = useCallback(
    async (credentials: BasicCredentials): Promise<void> => {
      setState({ status: "authenticating" });
      try {
        const { session } = await authenticateImpl(credentials);
        saveSession(credentials, storageRef.current);
        bearerRef.current = undefined;
        setState({
          status: "authenticated",
          session,
          username: credentials.username,
        });
      } catch (error) {
        const apiError = apiErrorFromThrown(error);
        // The credential never persists unless it worked.
        clearSession(storageRef.current);
        setState({ status: "anonymous", error: apiError });
      }
    },
    [authenticateImpl],
  );

  const signOut = useCallback((): void => {
    const current = bearerRef.current;
    bearerRef.current = undefined;
    clearSession(storageRef.current);
    if (current !== undefined) {
      /*
       * Fire and forget: §3.5 makes logout answer 204 even for a dead
       * session, and the local credential is already gone. Awaiting it would
       * make sign-out feel slow for no gain, and failing it would leave the
       * user staring at a mailbox they asked to leave.
       */
      void logoutDelegatedSession(current.token, fetchImpl);
      // A delegated user has no password, so the login form is the wrong
      // destination for a deliberate sign-out too (§3.7).
      setState({ status: "link-dead", reason: { kind: "invalid" } });
      return;
    }
    setState({ status: "anonymous" });
  }, [fetchImpl]);

  const onUnauthorized = useCallback((): void => {
    const current = bearerRef.current;
    bearerRef.current = undefined;
    clearSession(storageRef.current);
    setState(
      current === undefined
        ? { status: "anonymous" }
        : { status: "link-dead", reason: { kind: "invalid" } },
    );
  }, []);

  const credential = useMemo<AuthCredential | undefined>(() => {
    if (state.status !== "authenticated") return undefined;
    if (state.delegated !== undefined) return { token: state.delegated.token };
    const stored = loadSession(storageRef.current);
    return stored?.kind === "basic"
      ? { username: stored.username, password: stored.password }
      : undefined;
  }, [state]);

  const value = useMemo<AuthContextValue>(
    () => ({
      state,
      signIn,
      signOut,
      onUnauthorized,
      credential,
      readOnly: state.status === "authenticated" && state.delegated?.readOnly === true,
    }),
    [state, signIn, signOut, onUnauthorized, credential],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

/** How often the renewal check runs while a delegated session is open. */
const RENEW_POLL_MS = 60_000;

export function useAuth(): AuthContextValue {
  const context = useContext(AuthContext);
  if (context === undefined) {
    throw new Error("useAuth must be used inside an <AuthProvider>");
  }
  return context;
}
