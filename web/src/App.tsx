import { useEffect, useMemo } from "react";

import { JmapClient, type BasicCredentials } from "./api/jmap";
import { AuthProvider, useAuth } from "./auth/AuthProvider";
import { loadSession } from "./auth/session";
import { BrandingProvider } from "./branding/BrandingProvider";
import { I18nProvider, useTranslation } from "./i18n/I18nProvider";
import { PrefsProvider, usePrefs } from "./mail/PrefsProvider";
import { OfflineProvider } from "./offline/OfflineProvider";
import { RouterProvider } from "./router/RouterProvider";
import { LoginScreen } from "./screens/login/LoginScreen";
import { MailScreen } from "./screens/mail/MailScreen";
import { applyTheme, loadThemePreference, saveThemePreference } from "./theme/theme";
import styles from "./App.module.css";

/**
 * The application root.
 *
 * # The router, in two layers
 *
 * P1 left this as an auth-state switch and named it the seam a URL router
 * would plug into. P2 plugs it in, and the two layers stay separate on
 * purpose:
 *
 *   - THIS switch chooses between "signed out" and "signed in". It is not a
 *     URL question: no path should render the mail UI without a session, and
 *     no path should hide it with one.
 *   - The URL router (`RouterProvider`) lives INSIDE the authenticated branch,
 *     because every route it knows about — a mailbox, a message, a search — is
 *     meaningless without an account to resolve it against.
 *
 * Nesting it this way means an unauthenticated visit to `/mail/inbox/e42`
 * shows the login screen and, once signed in, lands exactly there: the URL was
 * never discarded, only deferred.
 */

function Router(): React.JSX.Element {
  const { state } = useAuth();
  const { t } = useTranslation();

  switch (state.status) {
    case "restoring":
      /*
       * The one screen shown while a stored credential is revalidated. It is
       * intentionally almost empty: it exists for the ~200 ms of a Session
       * fetch, and anything more elaborate would flash.
       */
      return (
        <div className={styles.splash} role="status" aria-live="polite">
          <span className="visually-hidden">{t("app.loading")}</span>
          <span className={styles.spinner} aria-hidden="true" />
        </div>
      );

    case "anonymous":
    case "authenticating":
      // The login screen renders in both: `authenticating` is the same screen
      // with its button in a pending state, not a different one. Swapping
      // screens mid-submit would lose what the user typed.
      return <LoginScreen />;

    case "authenticated":
      return (
        <SignedIn>
          <RouterProvider>
            <MailScreen />
          </RouterProvider>
        </SignedIn>
      );
  }
}

/**
 * The authenticated shell's providers (L3 E5).
 *
 * # Why the preferences load HERE and not inside MailScreen
 *
 * Two of them govern things above the mail screen. The LANGUAGE re-scopes
 * `I18nProvider`, which wraps everything; the THEME is account-level and has to
 * be reconciled against the pre-paint localStorage cache as soon as there is an
 * account. Loading them inside MailScreen would put both of those below the
 * providers they need to change.
 *
 * The client is rebuilt from the same stored credential MailScreen uses rather
 * than shared through a context. That looks like duplication and is deliberate:
 * `JmapClient` is a thin, stateless-per-request wrapper around `fetch` with an
 * Authorization header, so a second instance costs nothing, and threading one
 * through a context would make "which credential is this request using" a
 * question with a non-local answer — the exact property MailScreen's own
 * comment says the construction rule exists to preserve.
 */
function SignedIn({ children }: { readonly children: React.ReactNode }): React.JSX.Element {
  const { state } = useAuth();
  const session = state.status === "authenticated" ? state.session : undefined;
  const accountId = session?.primaryAccounts["urn:ietf:params:jmap:mail"] ?? "";

  const client = useMemo<JmapClient | undefined>(() => {
    if (state.status !== "authenticated") return undefined;
    const stored: BasicCredentials | undefined = loadSession();
    return stored === undefined ? undefined : new JmapClient(stored);
  }, [state.status]);

  /*
   * E9: the offline layer wraps the preferences rather than the other way
   * round, and the order is deliberate. The cache is keyed by ACCOUNT and
   * nothing else — it must not be torn down and rebuilt when a preference
   * changes, which is what nesting it inside `PrefsProvider` would risk the
   * moment someone adds a prefs-derived key to it. Outside, its lifetime is
   * exactly the account's.
   */
  return (
    <OfflineProvider accountId={accountId}>
      <PrefsProvider client={client} session={session} accountId={accountId}>
        <LocalizedFromPrefs>{children}</LocalizedFromPrefs>
      </PrefsProvider>
    </OfflineProvider>
  );
}

/**
 * Re-scopes the string table to the account's language preference.
 *
 * A SECOND `I18nProvider` nested inside the outer one, rather than lifting the
 * locale into `MoovApp`'s. The outer provider has to render before the session
 * exists — the login screen and the restoring splash are both localized — and
 * it cannot depend on a preference that requires an account to fetch. Nesting
 * makes the account's choice override the browser's detection for exactly the
 * subtree that has an account, which is the correct scope, and `null` (follow
 * the browser) falls through to the outer detection with no special case.
 *
 * `I18nProvider`'s `locale` prop was reserved for this: its own comment says
 * "used by tests and by a future user preference".
 */
function LocalizedFromPrefs({
  children,
}: {
  readonly children: React.ReactNode;
}): React.JSX.Element {
  const { prefs, isAvailable } = usePrefs();
  const { locale } = useTranslation();

  /*
   * Theme reconciliation, once the account's preference is known.
   *
   * The pre-paint script in index.html reads localStorage — it must, because
   * there is no session yet at first paint and a flash of the wrong theme is
   * the thing it exists to prevent. E5 makes the ACCOUNT the source of truth,
   * so this adopts the server's value and rewrites the cache to match, which
   * is what makes the theme follow the user to a new browser.
   *
   * Gated on `isAvailable`: a server without the capability must not have its
   * default silently overwrite a choice the user made locally and that the
   * cache is legitimately holding.
   */
  useEffect(() => {
    if (!isAvailable || typeof document === "undefined") return;
    applyTheme(prefs.theme, document.documentElement);
    saveThemePreference(prefs.theme);
  }, [isAvailable, prefs.theme]);

  // `?? locale` keeps the browser-detected value when the preference says
  // "follow the browser", instead of re-running detection with a different
  // language list.
  return <I18nProvider locale={prefs.language ?? locale}>{children}</I18nProvider>;
}

export interface AppProps {
  /** Test seams; production passes nothing. */
  readonly children?: never;
}

export function App(_props: AppProps = {}): React.JSX.Element {
  const { t } = useTranslation();

  /*
   * The theme is applied before first paint by a script in index.html (to
   * avoid a flash of the wrong theme). This effect keeps it correct for the
   * rest of the session — it is the source of truth once React is running.
   */
  useEffect(() => {
    applyTheme(loadThemePreference(), document.documentElement);
  }, []);

  return (
    <>
      {/* The first tab stop on every screen. */}
      <a className="skip-link" href="#main">
        {t("app.skipToContent")}
      </a>
      <Router />
    </>
  );
}

/**
 * The composed application, with every provider in the order they depend on
 * each other: i18n has no dependencies, branding needs none but supplies the
 * tokens, auth needs neither but is consumed by the screens.
 */
export function MoovApp(): React.JSX.Element {
  return (
    <I18nProvider>
      <BrandingProvider>
        <AuthProvider>
          <App />
        </AuthProvider>
      </BrandingProvider>
    </I18nProvider>
  );
}
