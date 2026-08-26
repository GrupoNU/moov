import { useEffect } from "react";

import { AuthProvider, useAuth } from "./auth/AuthProvider";
import { BrandingProvider } from "./branding/BrandingProvider";
import { I18nProvider, useTranslation } from "./i18n/I18nProvider";
import { RouterProvider } from "./router/RouterProvider";
import { LoginScreen } from "./screens/login/LoginScreen";
import { MailScreen } from "./screens/mail/MailScreen";
import { applyTheme, loadThemePreference } from "./theme/theme";
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
        <RouterProvider>
          <MailScreen />
        </RouterProvider>
      );
  }
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
