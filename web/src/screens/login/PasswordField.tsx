import { forwardRef, useId, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import styles from "./LoginScreen.module.css";

/**
 * The password field, with a visibility toggle.
 *
 * # Why the toggle is a real button and not an icon with a click handler
 *
 * Toggling password visibility is the control most often built inaccessibly:
 * a <span> with onClick is invisible to the keyboard, and an <img> with no
 * label announces nothing. This is a <button type="button"> — the type matters,
 * since a bare <button> inside a <form> defaults to submit and would sign the
 * user in when they tried to look at their password.
 *
 * Its state is conveyed three ways because each serves a different user:
 *   - aria-pressed, for a screen reader user, says the toggle's state;
 *   - the accessible name changes ("Show"/"Hide"), so the name always says
 *     what the NEXT press will do;
 *   - a live region announces the change, because aria-pressed alone is not
 *     reliably announced on activation across screen readers.
 */

export interface PasswordFieldProps {
  readonly id: string;
  readonly value: string;
  readonly onChange: (value: string) => void;
  readonly disabled?: boolean;
  readonly invalid?: boolean;
  readonly errorMessage?: string;
  /** An extra element id to reference, for the form-level error notice. */
  readonly describedBy?: string;
}

export const PasswordField = forwardRef<HTMLInputElement, PasswordFieldProps>(
  function PasswordField(
    { id, value, onChange, disabled = false, invalid = false, errorMessage, describedBy },
    ref,
  ) {
    const { t } = useTranslation();
    const [visible, setVisible] = useState(false);
    const statusId = useId();
    const errorId = `${id}-error`;

    const describedByIds = [
      errorMessage !== undefined ? errorId : undefined,
      describedBy,
    ]
      .filter((entry): entry is string => entry !== undefined)
      .join(" ");

    return (
      <div className={styles.field}>
        <label className={styles.label} htmlFor={id}>
          {t("login.passwordLabel")}
        </label>

        <div className={styles.passwordWrapper}>
          <input
            ref={ref}
            id={id}
            className={`${styles.input} ${styles.passwordInput}`}
            type={visible ? "text" : "password"}
            name="password"
            value={value}
            onChange={(event) => { onChange(event.target.value); }}
            /*
             * "current-password" tells a password manager this is a sign-in,
             * not a registration — the difference between it offering to fill
             * and it offering to generate a new password.
             */
            autoComplete="current-password"
            autoCapitalize="none"
            autoCorrect="off"
            spellCheck={false}
            required
            aria-required="true"
            aria-invalid={invalid ? true : undefined}
            aria-describedby={describedByIds !== "" ? describedByIds : undefined}
            disabled={disabled}
          />

          <button
            /* NOT a submit button — see the note above. */
            type="button"
            className={styles.passwordToggle}
            onClick={() => { setVisible((current) => !current); }}
            /* The name states the action this press performs. */
            aria-label={visible ? t("login.hidePassword") : t("login.showPassword")}
            aria-pressed={visible}
            aria-controls={id}
            disabled={disabled}
            /* Never a tab trap: it sits between the field and the submit
             * button in the natural order, which is where a user reaches for
             * it. */
          >
            {visible ? <EyeOffIcon /> : <EyeIcon />}
          </button>
        </div>

        {/* Announces the toggle for screen readers that do not report
            aria-pressed on activation. */}
        <span className="visually-hidden" role="status" aria-live="polite" id={statusId}>
          {visible ? t("login.passwordShown") : t("login.passwordHidden")}
        </span>

        {errorMessage !== undefined && (
          <p className={styles.fieldError} id={errorId}>
            {errorMessage}
          </p>
        )}
      </div>
    );
  },
);

function EyeIcon(): React.JSX.Element {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" className={styles.icon}>
      <path
        d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12Z"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      <circle cx="12" cy="12" r="3.1" fill="none" stroke="currentColor" strokeWidth="1.7" />
    </svg>
  );
}

function EyeOffIcon(): React.JSX.Element {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" className={styles.icon}>
      <path
        d="M4 4l16 16M9.9 5.9A9.6 9.6 0 0 1 12 5.5c6 0 9.5 6.5 9.5 6.5a17 17 0 0 1-3.3 4.1M6.6 7.9A17 17 0 0 0 2.5 12S6 18.5 12 18.5c.8 0 1.5-.1 2.2-.3"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      <path
        d="M9.9 10.1a3.1 3.1 0 0 0 4.2 4.2"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinecap="round"
      />
    </svg>
  );
}
