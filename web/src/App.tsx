import { useEffect } from "react";

import { AuthProvider, useAuth } from "./auth/AuthProvider";
import { BrandingProvider } from "./branding/BrandingProvider";
import { I18nProvider, useTranslation } from "./i18n/I18nProvider";
import { LoginScreen } from "./screens/login/LoginScreen";
import { AppShell } from "./screens/shell/AppShell";
import { applyTheme, loadThemePreference } from "./theme/theme";
import styles from "./App.module.css";

/**
 * The application root.
 *
 * # The "router"
 *
 * W-A3 calls for a light router. P1 does not have routes yet — it has two
 * mutually exclusive screens selected by authentication state — so the router
 * here is that switch, and nothing more. Introducing a URL router before there
 * is a second destination would be scaffolding without a building.
 *
 * What P2 adds (a mailbox in the path, a message id) plugs in below `AppShell`,
 * which is why the authenticated branch is a single component rather than a
 * tree spread through this file.
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
      return <AppShell />;
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
