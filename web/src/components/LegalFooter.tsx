import { useBranding } from "../branding/BrandingProvider";
import { useTranslation } from "../i18n/I18nProvider";
import { MOOV_LICENSE_URL, MOOV_REPO_URL, sourceUrlForCommit } from "./legalLinks";
import styles from "./LegalFooter.module.css";

export interface LegalFooterProps {
  /**
   * A layout hint. `"login"` sits under the sign-in form, `"list"` is the
   * muted strip at the foot of the message list; the only difference is the
   * spacing around the line.
   */
  readonly placement: "login" | "list";
}

/**
 * The legal footer: attribution, the source offer, the licence, and the
 * operator's own policy links.
 *
 * # Why the source link cannot be branded away
 *
 * Moov is AGPL-3.0, and §13 is the clause that makes that meaningful for a
 * hosted product: an operator who modifies Moov and lets users interact with
 * it over a network must offer those users the corresponding source. A footer
 * link is how every other AGPL web product discharges that, and a
 * customer-configurable one would discharge nothing — so `branding` cannot
 * remove or replace the first three links. It can only ADD its own Privacy
 * and Terms, which are the operator's obligations rather than ours.
 *
 * # Why it is one line of muted text
 *
 * Gmail's foot line ("Términos · Privacidad · Políticas") is the canon here:
 * legal text is present, findable, and takes exactly one row of grey. Anything
 * taller in the mail list would cost a message row, which is the resource the
 * screen is actually for.
 */
export function LegalFooter({ placement }: LegalFooterProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const branding = useBranding();
  const commit = __MOOV_COMMIT__;

  return (
    <footer
      className={`${styles.footer} ${placement === "login" ? styles.login : styles.list}`}
      aria-label={t("legal.label")}
    >
      {/*
        Every link here is off-origin, so every one of them carries the pair
        that keeps the opened page from reaching back through `window.opener`
        — the same treatment the login screen's support link gets.
      */}
      <a className={styles.link} href={MOOV_REPO_URL} rel="noopener noreferrer" target="_blank">
        {t("legal.poweredBy")}
      </a>
      <span className={styles.separator} aria-hidden="true">
        ·
      </span>
      <a
        className={styles.link}
        href={sourceUrlForCommit(commit)}
        /*
         * The commit rides in the TITLE rather than in the text: it is the
         * evidence that this link points at the running program, and it is
         * worth nothing to the reader who is not checking. Putting it on
         * screen would double the width of the line for a hex string.
         */
        title={format("legal.sourceCommit", commit)}
        rel="noopener noreferrer"
        target="_blank"
      >
        {t("legal.sourceCode")}
      </a>
      <span className={styles.separator} aria-hidden="true">
        ·
      </span>
      <a className={styles.link} href={MOOV_LICENSE_URL} rel="noopener noreferrer" target="_blank">
        {t("legal.license")}
      </a>

      {branding.privacyUrl !== "" && (
        <>
          <span className={styles.separator} aria-hidden="true">
            ·
          </span>
          <a
            className={styles.link}
            href={branding.privacyUrl}
            rel="noopener noreferrer"
            target="_blank"
          >
            {t("legal.privacy")}
          </a>
        </>
      )}

      {branding.termsUrl !== "" && (
        <>
          <span className={styles.separator} aria-hidden="true">
            ·
          </span>
          <a
            className={styles.link}
            href={branding.termsUrl}
            rel="noopener noreferrer"
            target="_blank"
          >
            {t("legal.terms")}
          </a>
        </>
      )}
    </footer>
  );
}
