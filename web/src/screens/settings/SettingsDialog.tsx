import { useEffect, useRef } from "react";

import { ThemeToggle } from "../../components/ThemeToggle";
import { useTranslation } from "../../i18n/I18nProvider";
import type { Strings } from "../../i18n/strings";
import styles from "./SettingsDialog.module.css";

/**
 * The keys these rows may use: the plain-string ones.
 *
 * `t` already accepts only plain keys, but the row components take a key as a
 * PROP and hand it to `t` later, so without this the prop would be typed
 * `StringKey` and a formatting key would be a cast at the `t` call rather than
 * an error at the call site that supplied it.
 */
type PlainStringKey = {
  [K in keyof Strings]: Strings[K] extends string ? K : never;
}[keyof Strings];

/**
 * The application settings sheet.
 *
 * # Why a real <dialog>, again
 *
 * The same three properties ShortcutsDialog banks on: `showModal()` makes the
 * rest of the page inert (not merely covered), traps focus for as long as it
 * is open, and closes on Escape. Re-implementing any of those is how a settings
 * panel ends up letting Tab wander behind it. Focus RESTORATION is done
 * explicitly, because browsers do not agree about it and a settings sheet that
 * dumps you at the top of the document punishes you for opening it.
 *
 * # The shape, and why it is a list of rows
 *
 * Theme is the first setting, not the only one. Every setting is a
 * {@link SettingRow}: a label, an optional one-line description, and a control
 * on the right. Adding the next one — a signature editor, a notifications
 * switch, a list-density choice — is appending a `<SettingRow>` to a section
 * here and its strings to the table; it is not a redesign, and it is
 * deliberately not a new dialog per setting.
 *
 * Sections exist for the same reason: `<SettingsSection>` groups rows under a
 * heading, so the second and third settings have an obvious home before the
 * sheet is long enough to need tabs. When it IS long enough, the sections
 * become the tab list without the rows changing at all.
 */

export interface SettingsDialogProps {
  readonly isOpen: boolean;
  readonly onClose: () => void;
}

export function SettingsDialog({ isOpen, onClose }: SettingsDialogProps): React.JSX.Element {
  const { t } = useTranslation();
  const dialogRef = useRef<HTMLDialogElement | null>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return;

    if (isOpen && !dialog.open) {
      // Remember where focus was so it can be restored on close.
      returnFocusRef.current =
        document.activeElement instanceof HTMLElement ? document.activeElement : null;
      dialog.showModal();
    } else if (!isOpen && dialog.open) {
      dialog.close();
      returnFocusRef.current?.focus();
    }
  }, [isOpen]);

  // The dialog can close by means we did not initiate (Escape, the backdrop),
  // so the parent's state is synchronised from the element's own event rather
  // than assumed.
  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return undefined;
    const handleClose = (): void => {
      returnFocusRef.current?.focus();
      onClose();
    };
    dialog.addEventListener("close", handleClose);
    return () => {
      dialog.removeEventListener("close", handleClose);
    };
  }, [onClose]);

  /*
   * Backdrop dismissal, attached natively rather than as a React onClick: a
   * <dialog> is not an interactive element, so an onClick on it is both a
   * jsx-a11y error and a genuine keyboard trap. It is a pure ENHANCEMENT for
   * pointer users — Escape and the close button both dismiss, and both work
   * from the keyboard.
   */
  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return undefined;
    const onBackdropClick = (event: MouseEvent): void => {
      if (event.target === dialog) onClose();
    };
    dialog.addEventListener("click", onBackdropClick);
    return () => {
      dialog.removeEventListener("click", onBackdropClick);
    };
  }, [onClose]);

  return (
    <dialog ref={dialogRef} className={styles.dialog} aria-labelledby="settings-title">
      <div className={styles.content}>
        <div className={styles.header}>
          <h2 className={styles.title} id="settings-title">
            {t("settings.title")}
          </h2>
          <button
            type="button"
            className={styles.close}
            onClick={onClose}
            aria-label={t("settings.close")}
          >
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true" focusable="false">
              <path d="M5.5 5.5l9 9M14.5 5.5l-9 9" />
            </svg>
          </button>
        </div>

        <SettingsSection titleKey="settings.section.appearance">
          <SettingRow labelKey="theme.label" descriptionKey="settings.theme.description">
            <ThemeToggle />
          </SettingRow>
        </SettingsSection>

        {/*
          THE NEXT SETTING GOES HERE — another <SettingsSection> ("Writing" for
          a signature, "Notifications" for desktop alerts), or another
          <SettingRow> inside an existing one. Nothing above needs to change.
        */}
      </div>
    </dialog>
  );
}

/** A titled group of settings rows. */
function SettingsSection({
  titleKey,
  children,
}: {
  readonly titleKey: PlainStringKey;
  readonly children: React.ReactNode;
}): React.JSX.Element {
  const { t } = useTranslation();
  return (
    /*
     * A <section> named by its own heading, so a screen reader user can jump
     * between groups instead of walking every row of a long sheet.
     */
    <section className={styles.section} aria-labelledby={`settings-${titleKey}`}>
      <h3 className={styles.sectionTitle} id={`settings-${titleKey}`}>
        {t(titleKey)}
      </h3>
      <div className={styles.rows}>{children}</div>
    </section>
  );
}

/**
 * One setting: what it is on the left, the control on the right.
 *
 * The label is NOT a <label> element and does not point at the control. Some
 * controls here are single inputs (a switch) and some are groups (the theme
 * radios, which carry their own fieldset/legend); a <label> can only name the
 * first kind, and pointing one at a fieldset produces a name that screen
 * readers announce inconsistently. So each control stays responsible for its
 * own accessible name — ThemeToggle's legend says "Theme" — and this text is
 * the VISIBLE heading of the row.
 */
function SettingRow({
  labelKey,
  descriptionKey,
  children,
}: {
  readonly labelKey: PlainStringKey;
  readonly descriptionKey?: PlainStringKey;
  readonly children: React.ReactNode;
}): React.JSX.Element {
  const { t } = useTranslation();
  return (
    <div className={styles.row}>
      <div className={styles.rowText}>
        <span className={styles.rowLabel}>{t(labelKey)}</span>
        {descriptionKey !== undefined && (
          <span className={styles.rowDescription}>{t(descriptionKey)}</span>
        )}
      </div>
      <div className={styles.rowControl}>{children}</div>
    </div>
  );
}
