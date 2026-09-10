import { useEffect, useId, useRef, useState, type FormEvent } from "react";

import { useAuth } from "../../auth/AuthProvider";
import { useBranding } from "../../branding/BrandingProvider";
import { messageForError } from "../../api/errorMessages";
import { useTranslation } from "../../i18n/I18nProvider";
import { BrandPanel } from "./BrandPanel";
import { PasswordField } from "./PasswordField";
import { ErrorNotice } from "./ErrorNotice";
import { BrandMark } from "../../components/BrandMark";
import { LegalFooter } from "../../components/LegalFooter";
import styles from "./LoginScreen.module.css";

/**
 * The login screen (product decisions P1 and P2 of L2-pwa §2).
 *
 * # The layout: split screen (P2)
 *
 * Brand imagery on one half, the form on the other. The spec's reasoning is
 * that Moov serves ONE company per domain, which is the case where a split
 * screen is canonical — the image means something to everyone who sees it.
 * Google's single-column layout is a consequence of serving billions of
 * unrelated users; copying it here would discard the one contextual advantage
 * this product has.
 *
 * # One step (P1), built to become two
 *
 * Email and password together. Identity-first routing exists to pick between
 * SSO providers, passkeys and multiple accounts; we have none of those — auth
 * is an IMAP LOGIN and the domain already identifies the company — so a second
 * step would be pure friction. Fastmail and Superhuman, the actual benchmark,
 * are single-step.
 *
 * THE SEAM for the two-step future is deliberate and is described at
 * the form body below.
 */
export function LoginScreen(): React.JSX.Element {
  const { t } = useTranslation();
  const branding = useBranding();
  const { state, signIn } = useAuth();

  const formId = useId();
  const emailId = `${formId}-email`;
  const passwordId = `${formId}-password`;
  const errorId = `${formId}-error`;

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  /** Client-side validation, kept separate from server errors. */
  const [fieldError, setFieldError] = useState<{ field: "email" | "password"; message: string } | undefined>();

  const emailRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);

  const submitting = state.status === "authenticating";
  const serverError = state.status === "anonymous" ? state.error : undefined;

  /*
   * Focus the email field on arrival — but only on a CLEAN arrival.
   *
   * The `autoFocus` attribute is deliberately not used, for the reason
   * jsx-a11y flags it: it fires during initial render, before a screen reader
   * has finished announcing the page, so the user is dropped into a text field
   * having heard neither the product name nor the heading. An effect runs after
   * the document is complete.
   *
   * The guard matters just as much. When an error notice is on screen it is a
   * `role="alert"`, and moving focus at that moment would cut its announcement
   * short. In that case focus stays where it is and the user hears what went
   * wrong — which is the information they are waiting for.
   *
   * `mountedWithError` is read from a ref rather than from `serverError`
   * directly so this stays a mount-only effect: re-running it on every state
   * change would yank focus back to the email field while the user was typing
   * their password.
   */
  const mountedWithError = useRef(serverError !== undefined);
  useEffect(() => {
    if (!mountedWithError.current) {
      emailRef.current?.focus();
    }
  }, []);

  const translation = useTranslation();
  const errorMessage =
    serverError !== undefined && serverError.kind !== "aborted"
      ? messageForError(serverError, translation)
      : undefined;

  function handleSubmit(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault();
    if (submitting) return;

    // Validate in the order the fields appear, and move focus to the first
    // problem: a message with no focus change leaves a keyboard or screen
    // reader user hunting for the field it refers to.
    const trimmedEmail = email.trim();
    if (trimmedEmail === "") {
      setFieldError({ field: "email", message: t("login.error.emailRequired") });
      emailRef.current?.focus();
      return;
    }
    // A deliberately loose check: "something@something". Anything stricter
    // rejects addresses that are legal and that Dovecot would accept, and the
    // server is the real authority anyway. The only job here is to catch the
    // user who typed a bare username and would otherwise get "wrong password".
    if (!/^[^\s@]+@[^\s@]+$/.test(trimmedEmail)) {
      setFieldError({ field: "email", message: t("login.error.emailInvalid") });
      emailRef.current?.focus();
      return;
    }
    if (password === "") {
      setFieldError({ field: "password", message: t("login.error.passwordRequired") });
      passwordRef.current?.focus();
      return;
    }

    setFieldError(undefined);
    void signIn({ username: trimmedEmail, password });
  }

  const activeError = fieldError?.message ?? errorMessage?.title;

  return (
    <div className={styles.layout}>

      <main className={styles.formSide} id="main">
        <div className={styles.formCard}>
          {/*
            On narrow screens the brand panel collapses to a background, so the
            logo would be lost; this compact mark carries the identity there.
            It is hidden on wide screens where the panel already shows it.
          */}
          <div className={styles.compactBrand}>
            <BrandMark branding={branding} size="sm" />
          </div>

          <header className={styles.header}>
            <h1 className={styles.heading}>{t("login.heading")}</h1>
            <p className={styles.subheading}>{t("login.subheading")}</p>
          </header>

          {errorMessage !== undefined && (
            <ErrorNotice
              id={errorId}
              title={errorMessage.title}
              body={errorMessage.body}
              {...(errorMessage.suggestsAdministrator && branding.supportUrl !== ""
                ? { supportUrl: branding.supportUrl }
                : {})}
              showAdministratorHint={errorMessage.suggestsAdministrator}
            />
          )}

          <form className={styles.form} onSubmit={handleSubmit} noValidate>
            {/*
              THE TWO-STEP SEAM.

              Everything between here and </form> is the "credentials step".
              Introducing identity-first routing means:

                1. adding a `step` state ("identity" | "credentials"),
                2. rendering the email field alone when step === "identity",
                   with a Continue button that asks the server how this address
                   authenticates,
                3. rendering the password field plus the (now read-only) email
                   when step === "credentials".

              What makes that a change of ~30 lines rather than a rewrite: the
              two fields are already independent components with their own
              labels, validation and refs; the submit handler validates them in
              sequence rather than as one blob; and nothing in the layout,
              branding or error handling knows how many fields there are. The
              form element and its error plumbing are reused as-is.
            */}
            <div className={styles.field}>
              <label className={styles.label} htmlFor={emailId}>
                {t("login.emailLabel")}
              </label>
              <input
                ref={emailRef}
                id={emailId}
                className={styles.input}
                type="email"
                name="username"
                value={email}
                onChange={(event) => {
                  setEmail(event.target.value);
                  if (fieldError?.field === "email") setFieldError(undefined);
                }}
                placeholder={t("login.emailPlaceholder")}
                // The autocomplete tokens password managers actually key on.
                // "username" (not "email") is what the spec defines for a
                // sign-in identifier, and getting it wrong is why some managers
                // silently refuse to fill a form.
                autoComplete="username"
                inputMode="email"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                required
                aria-required="true"
                aria-invalid={fieldError?.field === "email" ? true : undefined}
                aria-describedby={
                  fieldError?.field === "email"
                    ? `${emailId}-error`
                    : errorMessage !== undefined
                      ? errorId
                      : undefined
                }
                disabled={submitting}
              />
              {fieldError?.field === "email" && (
                <p className={styles.fieldError} id={`${emailId}-error`}>
                  {fieldError.message}
                </p>
              )}
            </div>

            <PasswordField
              id={passwordId}
              ref={passwordRef}
              value={password}
              onChange={(next) => {
                setPassword(next);
                if (fieldError?.field === "password") setFieldError(undefined);
              }}
              disabled={submitting}
              invalid={fieldError?.field === "password"}
              {...(fieldError?.field === "password"
                ? { errorMessage: fieldError.message }
                : {})}
              {...(errorMessage !== undefined ? { describedBy: errorId } : {})}
            />

            <button className={styles.submit} type="submit" disabled={submitting}>
              {submitting ? t("login.submitting") : t("login.submit")}
            </button>
          </form>

          {branding.supportUrl !== "" && (
            <p className={styles.help}>
              <a
                className={styles.helpLink}
                href={branding.supportUrl}
                // The support URL may be off-origin; these two attributes stop
                // the target page from reaching back through window.opener.
                rel="noopener noreferrer"
                target="_blank"
              >
                {t("login.needHelp")}
              </a>
            </p>
          )}

          {/*
            The legal line, LAST inside the form column and outside the form
            itself — under the submit and under the support link, which is
            where a footnote belongs and where Gmail puts its own. It is on the
            form side rather than on the brand panel deliberately: the panel
            collapses on narrow screens, and the AGPL §13 source offer must be
            reachable on a phone too.
          */}
          <LegalFooter placement="login" />
        </div>

        {/*
          One polite live region for the whole screen.

          Polite rather than assertive: a sign-in failure is important but not
          an emergency, and assertive interrupts whatever the user is currently
          hearing mid-word. The region is always in the DOM — a live region
          that is inserted at the same moment its text appears is frequently
          not announced at all, which is the most common way this feature is
          got wrong.
        */}
        <div className="visually-hidden" role="status" aria-live="polite">
          {activeError ?? ""}
        </div>
      </main>
      {/*
        The brand half. `aria-hidden` is NOT set: the product name inside it is
        genuine content a screen reader user should hear. What IS hidden is the
        decorative image, inside BrandPanel.
      */}
      <BrandPanel branding={branding} />
    </div>
  );
}
