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
import { authenticate, type BasicCredentials, type JmapSession } from "../api/jmap";
import {
  clearSession,
  defaultSessionStorage,
  loadSession,
  saveSession,
  type SessionStorageLike,
} from "./session";

/**
 * Authentication state and the two transitions that change it.
 *
 * The state is a discriminated union rather than a bag of booleans, because
 * the combinations a bag allows ("loading AND authenticated AND has an error")
 * are exactly the ones that produce a screen showing two things at once. With
 * a union, the app renders one of four things and the compiler enforces that.
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
    };

interface AuthContextValue {
  readonly state: AuthState;
  /** Attempts a sign-in. Never throws: failures land in `state.error`. */
  readonly signIn: (credentials: BasicCredentials) => Promise<void>;
  readonly signOut: () => void;
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

export interface AuthProviderProps {
  readonly children: ReactNode;
  /** Injected by tests. */
  readonly storage?: SessionStorageLike;
  readonly authenticateImpl?: typeof authenticate;
  /**
   * Skips restoring a stored session. Tests that want a clean login screen set
   * this rather than having to stub storage.
   */
  readonly skipRestore?: boolean;
}

export function AuthProvider({
  children,
  storage,
  authenticateImpl = authenticate,
  skipRestore = false,
}: AuthProviderProps): React.JSX.Element {
  const storageRef = useRef<SessionStorageLike>(storage ?? defaultSessionStorage());
  const [state, setState] = useState<AuthState>(() =>
    skipRestore ? { status: "anonymous" } : { status: "restoring" },
  );

  /**
   * Restores a stored credential on mount by REVALIDATING it, never by
   * trusting it.
   *
   * The distinction matters: a password may have been changed, or the account
   * disabled, since the tab was opened. Trusting storage would land the user
   * in the app shell and then fail every subsequent call with no explanation.
   * One Session fetch answers the question properly.
   */
  useEffect(() => {
    if (skipRestore) return undefined;

    const stored = loadSession(storageRef.current);
    if (stored === undefined) {
      setState({ status: "anonymous" });
      return undefined;
    }

    const controller = new AbortController();
    let cancelled = false;

    void (async () => {
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
  }, [authenticateImpl, skipRestore]);

  const signIn = useCallback(
    async (credentials: BasicCredentials): Promise<void> => {
      setState({ status: "authenticating" });
      try {
        const { session } = await authenticateImpl(credentials);
        saveSession(credentials, storageRef.current);
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
    clearSession(storageRef.current);
    setState({ status: "anonymous" });
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({ state, signIn, signOut }),
    [state, signIn, signOut],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const context = useContext(AuthContext);
  if (context === undefined) {
    throw new Error("useAuth must be used inside an <AuthProvider>");
  }
  return context;
}
