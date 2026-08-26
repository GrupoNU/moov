import { useAuth } from "../../auth/AuthProvider";
import { useBranding } from "../../branding/BrandingProvider";
import { BrandMark } from "../../components/BrandMark";
import { ThemeToggle } from "../../components/ThemeToggle";
import { useTranslation } from "../../i18n/I18nProvider";
import styles from "./AppShell.module.css";

/**
 * The authenticated landing (P1's final AC: "a real authenticated landing — an
 * app shell with the sidebar skeleton is enough for P1").
 *
 * # What is deliberately NOT here
 *
 * The mailbox list. P2 owns it, and rendering fake folders now would be a lie
 * that has to be deleted later. What IS here is the frame those folders will
 * land in — the branded header, the sidebar column, the content region — plus
 * a skeleton in the sidebar that is honest about being a placeholder.
 *
 * The value of building the frame now is that P2 adds a data source to an
 * existing layout rather than inventing the layout under time pressure.
 */
export function AppShell(): React.JSX.Element {
  const { t, format } = useTranslation();
  const branding = useBranding();
  const { state, signOut } = useAuth();

  const username = state.status === "authenticated" ? state.username : "";

  return (
    <div className={styles.shell}>
      <header className={styles.header}>
        <BrandMark branding={branding} size="sm" />

        <div className={styles.headerActions}>
          <ThemeToggle />
          <span className={styles.account} title={username}>
            {format("shell.signedInAs", username)}
          </span>
          <button className={styles.signOut} type="button" onClick={signOut}>
            {t("shell.signOut")}
          </button>
        </div>
      </header>

      <div className={styles.body}>
        {/*
          The sidebar. `aria-label` rather than a visible heading: the region
          needs a name for screen-reader navigation, and a visible "Mailboxes"
          title would be redundant once the folder list is there.
        */}
        <nav className={styles.sidebar} aria-label={t("shell.mailboxes")}>
          <p className={styles.sidebarHeading}>{t("shell.mailboxes")}</p>
          {/*
            An honest skeleton: it announces itself as busy rather than
            pretending to be content, so a screen reader says "loading" instead
            of reading six empty list items.
          */}
          <ul className={styles.skeletonList} aria-busy="true" aria-label={t("app.loading")}>
            {[0, 1, 2, 3, 4, 5].map((index) => (
              <li key={index} className={styles.skeletonRow} aria-hidden="true">
                <span className={styles.skeletonDot} />
                <span
                  className={styles.skeletonBar}
                  /* Varied widths so the skeleton reads as a list of names
                   * rather than as a broken table. */
                  style={{ width: `${[68, 54, 74, 46, 62, 58][index] ?? 60}%` }}
                />
              </li>
            ))}
          </ul>
        </nav>

        <main className={styles.content} id="main">
          <div className={styles.placeholder}>
            <BrandMark branding={branding} size="lg" iconOnly />
            <h1 className={styles.placeholderTitle}>{t("shell.comingSoon")}</h1>
            <p className={styles.placeholderBody}>{t("shell.comingSoonBody")}</p>
          </div>
        </main>
      </div>
    </div>
  );
}
