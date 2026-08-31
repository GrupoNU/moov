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

import type { JmapClient, JmapSession } from "../api/jmap";
import {
  DEFAULT_PREFS,
  fetchPrefs,
  savePrefs,
  sessionHasPrefs,
  type PrefKey,
  type Prefs,
} from "./prefs";

/**
 * The preference provider (L3 E5).
 *
 * # Why the same optimistic shape as message actions
 *
 * `useMessageActions` established the rule this follows: paint first, call
 * second, roll back the INVERSE on failure, and never revert silently. A
 * settings toggle has exactly the same physics as an archive — the round trip
 * to the pilot is ~530 ms and ADR §6 asks for <100 ms perceived — so it gets
 * the same treatment rather than a spinner on every switch.
 *
 * The rollback here restores the PREVIOUS VALUE OF THE CHANGED KEY, not a
 * snapshot of the whole object. Two settings changed in quick succession are
 * two independent saves, and a snapshot rollback of the second would undo the
 * first as well — the same reasoning `useMessageActions` gives for inverse
 * patches over snapshots.
 *
 * # Feature detection, not optimism
 *
 * Nothing is requested until the session is known to advertise the capability.
 * A server without it leaves `isAvailable` false, the app runs on
 * {@link DEFAULT_PREFS}, and the settings screen says the preferences cannot
 * be saved — which is honest (P4) and is also what makes this PWA safe to
 * point at an older moovd during a deploy.
 *
 * # The status the UI actually needs
 *
 * `status` distinguishes "still loading" from "loaded" from "unavailable",
 * because rendering the defaults during a load is correct but SAYING they are
 * the user's settings while a request is in flight is not.
 */

export type PrefsStatus = "loading" | "ready" | "unavailable";

export interface PrefsContextValue {
  readonly prefs: Prefs;
  readonly status: PrefsStatus;
  /** True when the server advertises the capability and a load succeeded. */
  readonly isAvailable: boolean;
  /** The last save failure, in the server's own words. Cleared by a success. */
  readonly error: string | undefined;
  /** True while at least one save is in flight. */
  readonly isSaving: boolean;
  /**
   * Sets one preference, optimistically.
   *
   * Resolves true when the server accepted it and false when the value was
   * rolled back — so a caller with a side effect to run (requesting
   * notification permission, say) can wait for the truth rather than acting on
   * the optimistic paint.
   */
  readonly setPref: <K extends PrefKey>(key: K, value: Prefs[K]) => Promise<boolean>;
  /**
   * Sets SEVERAL preferences as one save, with the same optimistic shape.
   *
   * It exists for the one case a sequence of {@link setPref} calls gets wrong:
   * a change that is conceptually ONE event across several keys — the v2
   * migration carrying a browser's local settings up to the account, or a
   * signature editor that creates an item and selects it in the same gesture.
   * Sent separately those are N state advances, N chances to half-succeed, and
   * a window in which `signatures.forNew` names an item the account does not
   * have yet, which the server correctly refuses as a dangling reference.
   *
   * Rollback restores the previous value of exactly the keys in the patch.
   */
  readonly setPrefs: (patch: Partial<Prefs>) => Promise<boolean>;
}

const PrefsContext = createContext<PrefsContextValue | undefined>(undefined);

export interface PrefsProviderProps {
  readonly children: ReactNode;
  readonly client: JmapClient | undefined;
  readonly session: JmapSession | undefined;
  readonly accountId: string;
  /**
   * Test seam: skips the load and starts from these values.
   *
   * Production passes nothing. It exists so a component test can render a
   * screen at a known density without standing up a JMAP fake for a question
   * that is not about JMAP.
   */
  readonly initialPrefs?: Prefs;
}

export function PrefsProvider({
  children,
  client,
  session,
  accountId,
  initialPrefs,
}: PrefsProviderProps): React.JSX.Element {
  const [prefs, setPrefs] = useState<Prefs>(initialPrefs ?? DEFAULT_PREFS);
  const [status, setStatus] = useState<PrefsStatus>(
    initialPrefs === undefined ? "loading" : "ready",
  );
  const [error, setError] = useState<string | undefined>(undefined);
  const [inFlight, setInFlight] = useState(0);

  const available = sessionHasPrefs(session, accountId);

  /*
   * The current value is mirrored into a ref so `setPref` can read it without
   * listing `prefs` as a dependency — which would make the setter a new
   * function on every change and re-render every control that holds it.
   */
  const prefsRef = useRef(prefs);
  prefsRef.current = prefs;

  useEffect(() => {
    if (initialPrefs !== undefined) return undefined;
    /*
     * Anything that makes a load impossible resolves to "unavailable" — it must
     * never leave the status at "loading".
     *
     * The three cases are genuinely one: no client, no account, or a server
     * that does not advertise the capability all mean "there is no stored
     * preference to fetch". Returning early on the first two while only the
     * third set the status was a real bug — it left the sheet claiming to be
     * loading forever, with no request in flight to ever finish it, and with
     * the persistence warning suppressed because "loading" is not
     * "unavailable".
     */
    if (client === undefined || accountId === "" || !available) {
      // Not an error: a server without the capability is a server we can still
      // read mail from. The app runs on defaults and says so.
      setStatus("unavailable");
      return undefined;
    }

    const controller = new AbortController();
    setStatus("loading");
    void (async () => {
      try {
        const result = await fetchPrefs(client, accountId, controller.signal);
        if (controller.signal.aborted) return;
        setPrefs(result.prefs);
        setStatus("ready");
        setError(undefined);
      } catch (cause) {
        if (controller.signal.aborted) return;
        /*
         * A failed LOAD degrades to defaults rather than blocking the app: the
         * user came here to read mail, and a settings request that 500s must
         * not be the reason the inbox does not render. The reason is kept so
         * the settings screen can show it instead of pretending the defaults
         * are the user's choices.
         */
        setStatus("unavailable");
        setError(cause instanceof Error ? cause.message : String(cause));
      }
    })();

    return () => {
      controller.abort();
    };
  }, [client, accountId, available, initialPrefs]);

  /*
   * The one implementation. `setPref` is a one-key call into this rather than a
   * parallel code path, because two optimistic-save routines are two places for
   * the rollback rule to drift — and the rollback rule is the subtle part.
   */
  const setPrefsPatch = useCallback(
    async (patch: Partial<Prefs>): Promise<boolean> => {
      const keys = Object.keys(patch) as PrefKey[];
      if (keys.length === 0) return true;

      /*
       * The previous values of exactly the patched keys, captured BEFORE the
       * optimistic paint. Rolling back a snapshot of the whole object would
       * undo a concurrent save of some other key — the reasoning
       * `useMessageActions` gives for inverse patches over snapshots.
       */
      const previous: Partial<Prefs> = {};
      for (const key of keys) {
        (previous as Record<string, unknown>)[key] = prefsRef.current[key];
      }

      // Paint first. Every consumer — the list's density, the keyboard gate,
      // the layout — reads the context, so one write moves the whole app.
      setPrefs((current) => ({ ...current, ...patch }));

      if (client === undefined || accountId === "" || !available) {
        /*
         * No server to save to. The change is kept for THIS session so the
         * controls are not dead (P4: never a control that does nothing), and
         * the screen already says the preferences are not being persisted.
         */
        return false;
      }

      setInFlight((count) => count + 1);
      try {
        const result = await savePrefs(client, accountId, patch);
        /*
         * The server's ANSWER replaces the optimistic guess, rather than the
         * guess being confirmed. They are normally identical; when they are
         * not — a value the server clamped, a field another tab changed
         * between our read and our write — the server is right, and adopting
         * its object is what keeps a second tab from being silently wrong.
         */
        setPrefs(result.prefs);
        setError(undefined);
        return true;
      } catch (cause) {
        // Roll back exactly the patched keys, not the whole object.
        setPrefs((current) => ({ ...current, ...previous }));
        setError(cause instanceof Error ? cause.message : String(cause));
        return false;
      } finally {
        setInFlight((count) => count - 1);
      }
    },
    [client, accountId, available],
  );

  const setPref = useCallback(
    async <K extends PrefKey>(key: K, value: Prefs[K]): Promise<boolean> => {
      /*
       * The identity short-circuit stays here rather than moving into the
       * patch path, because it is only sound for a SCALAR: `===` on the two v2
       * maps compares references, and a caller that rebuilt an equal object
       * would be told "saved" without a request. Every key `setPref` is used
       * for is a scalar; a structured one goes through `setPrefs`.
       */
      if (prefsRef.current[key] === value) return true;
      return setPrefsPatch({ [key]: value });
    },
    [setPrefsPatch],
  );

  const value = useMemo<PrefsContextValue>(
    () => ({
      prefs,
      status,
      isAvailable: available && status === "ready",
      error,
      isSaving: inFlight > 0,
      setPref,
      setPrefs: setPrefsPatch,
    }),
    [prefs, status, available, error, inFlight, setPref, setPrefsPatch],
  );

  return <PrefsContext.Provider value={value}>{children}</PrefsContext.Provider>;
}

/**
 * Access to the preferences.
 *
 * Unlike `useTranslation`, this does NOT throw outside its provider: it falls
 * back to a read-only view of the defaults. The difference is deliberate. A
 * missing i18n provider means text renders as keys — visibly broken, worth a
 * hard failure. A missing prefs provider means the app behaves as a fresh
 * account would, which is a correct app; making it crash would mean every
 * small component test that renders a row has to mount a provider to ask a
 * question that is not about preferences.
 */
export function usePrefs(): PrefsContextValue {
  const context = useContext(PrefsContext);
  return context ?? FALLBACK;
}

const FALLBACK: PrefsContextValue = {
  prefs: DEFAULT_PREFS,
  status: "unavailable",
  isAvailable: false,
  error: undefined,
  isSaving: false,
  setPref: () => Promise.resolve(false),
  setPrefs: () => Promise.resolve(false),
};
