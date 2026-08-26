import { useTranslation } from "../../i18n/I18nProvider";
import styles from "./LoginScreen.module.css";

/**
 * The error banner.
 *
 * # The shape is the point
 *
 * A title that names what happened, and a body that says what to do. That
 * two-part shape is what stops a message from degenerating into "an error
 * occurred": a title alone tends to be a status, and a body alone tends to be
 * an apology. Together they answer the user's two questions in order.
 *
 * `role="alert"` rather than a polite region: unlike the toggle status, a
 * failed sign-in IS the thing the user is waiting on, and interrupting to say
 * so is correct. It is rendered only when there is an error, which is the case
 * where role="alert" is announced reliably.
 */

export interface ErrorNoticeProps {
  readonly id: string;
  readonly title: string;
  readonly body: string;
  /** Rendered as a link when the brand configured one. */
  readonly supportUrl?: string;
  /**
   * Whether to show the "contact your administrator" hint. True only for the
   * not-provisioned case — see errorMessages.ts on why it is not shown for
   * everything.
   */
  readonly showAdministratorHint: boolean;
}

export function ErrorNotice({
  id,
  title,
  body,
  supportUrl,
  showAdministratorHint,
}: ErrorNoticeProps): React.JSX.Element {
  const { t } = useTranslation();

  return (
    <div className={styles.errorNotice} id={id} role="alert">
      <WarningIcon />
      <div className={styles.errorText}>
        <p className={styles.errorTitle}>{title}</p>
        <p className={styles.errorBody}>{body}</p>
        {showAdministratorHint && (
          <p className={styles.errorAction}>
            {supportUrl !== undefined ? (
              <a
                className={styles.errorLink}
                href={supportUrl}
                rel="noopener noreferrer"
                target="_blank"
              >
                {t("login.contactAdministrator")}
              </a>
            ) : (
              /* With no configured support URL the hint is still shown as
               * text: "contact your administrator" is actionable advice even
               * without a link, and dropping it would lose the remedy. */
              t("login.contactAdministrator")
            )}
          </p>
        )}
      </div>
    </div>
  );
}

function WarningIcon(): React.JSX.Element {
  return (
    <svg
      viewBox="0 0 24 24"
      aria-hidden="true"
      focusable="false"
      className={styles.errorIcon}
    >
      <circle cx="12" cy="12" r="9.2" fill="none" stroke="currentColor" strokeWidth="1.7" />
      <path
        d="M12 7.6v5.2"
        stroke="currentColor"
        strokeWidth="1.9"
        strokeLinecap="round"
      />
      <circle cx="12" cy="16.4" r="1.05" fill="currentColor" />
    </svg>
  );
}
