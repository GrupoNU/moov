import { useEffect, useRef } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { SECTION_TITLE_KEYS, SHORTCUT_HELP, SHORTCUT_SECTIONS } from "../../keyboard/shortcuts";
import type { StringKey } from "../../i18n/strings";
import { usePrefs } from "../../mail/PrefsProvider";
import styles from "./ShortcutsDialog.module.css";

/**
 * The `?` shortcuts sheet (P2 deliverable 7: "a discoverable shortcuts help").
 *
 * # Why a real <dialog>
 *
 * `showModal()` gives, for free and correctly, three things a hand-rolled
 * overlay gets wrong: the rest of the page becomes inert (not just visually
 * covered), focus is trapped inside for as long as it is open, and Escape
 * closes it. Re-implementing those is how modals end up letting Tab wander
 * behind them.
 *
 * The one thing it does NOT do is return focus where it came from in every
 * browser, so that is done explicitly below — a help sheet that dumps you at
 * the top of the document is a help sheet that punishes you for opening it.
 */

export interface ShortcutsDialogProps {
  readonly isOpen: boolean;
  readonly onClose: () => void;
}

export function ShortcutsDialog({ isOpen, onClose }: ShortcutsDialogProps): React.JSX.Element {
  const { t } = useTranslation();
  const { prefs } = usePrefs();
  const shortcutsEnabled = prefs.keyboardShortcuts;
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
   * Backdrop dismissal is wired natively rather than with a React onClick.
   *
   * A `<dialog>` is not an interactive element, so an onClick on it is both an
   * accessibility lint error and a genuine trap: the handler would be
   * unreachable by keyboard, which is why the rule exists. The behaviour is
   * still worth having for mouse users, so it is attached as a plain listener
   * — and it is purely an ENHANCEMENT, because Escape (handled by the dialog
   * itself) and the close button both already dismiss, and both work from the
   * keyboard.
   */
  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return undefined;
    const onBackdropClick = (event: MouseEvent): void => {
      // The dialog element only receives a click itself when the pointer was
      // outside its content box — i.e. on the backdrop.
      if (event.target === dialog) onClose();
    };
    dialog.addEventListener("click", onBackdropClick);
    return () => {
      dialog.removeEventListener("click", onBackdropClick);
    };
  }, [onClose]);

  return (
    <dialog ref={dialogRef} className={styles.dialog} aria-labelledby="shortcuts-title">
      <div className={styles.content}>
        <div className={styles.header}>
          <h2 className={styles.title} id="shortcuts-title">
            {t("shortcuts.title")}
          </h2>
          <button
            type="button"
            className={styles.close}
            onClick={onClose}
            aria-label={t("shortcuts.close")}
          >
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true" focusable="false">
              <path d="M5.5 5.5l9 9M14.5 5.5l-9 9" />
            </svg>
          </button>
        </div>

        {/*
          E5: the honest disclaimer when the map is turned off.

          The sheet is still reachable — the header's help button opens it, and
          `?` does not while shortcuts are off — so a user who lands here must
          be told why none of the keys below do anything, rather than being left
          to conclude the shortcuts are broken. It names the two that DO still
          work, because those are the ones they will need to get out of here and
          to search.
        */}
        {!shortcutsEnabled && (
          <p className={styles.disabledNote} role="status">
            {t("shortcuts.disabled")}
          </p>
        )}

        {/*
          E11: grouped like Gmail's own cheat sheet.

          Forty-odd rows in press order is a wall, not a reference. The
          sections come from the map itself (`entry.section`), so a new binding
          lands in a group by declaring one rather than by being inserted in
          the right place in a flat list — which is the kind of ordering nobody
          maintains.

          Each section is its own <dl>: a single definition list broken up by
          headings would put the <h3>s between <dt>/<dd> pairs, which is
          invalid and makes a screen reader announce one long list.
        */}
        {SHORTCUT_SECTIONS.map((section) => {
          const entries = SHORTCUT_HELP.filter((entry) => entry.section === section);
          if (entries.length === 0) return null;
          return (
            <section className={styles.section} key={section}>
              <h3 className={styles.sectionTitle}>
                {t(SECTION_TITLE_KEYS[section] as StringKey & PlainDescriptionKey)}
              </h3>
              <dl className={styles.list}>
                {entries.map((entry) => (
                  <div className={styles.entry} key={entry.descriptionKey}>
                    <dt className={styles.keys}>
                      {entry.keys.map((key, index) => (
                        <span key={key}>
                          <kbd className={styles.key}>{key}</kbd>
                          {/* "then" between the two halves of a chord, so `g i`
                              does not read as "press g and i together". */}
                          {index < entry.keys.length - 1 && (
                            <span className={styles.thenText} aria-hidden="true">
                              {" "}
                            </span>
                          )}
                        </span>
                      ))}
                    </dt>
                    <dd className={styles.description}>
                      {t(entry.descriptionKey as StringKey & PlainDescriptionKey)}
                    </dd>
                  </div>
                ))}
              </dl>
            </section>
          );
        })}
      </div>
    </dialog>
  );
}

/**
 * The help entries' description keys are all plain strings; this alias makes
 * that promise to `t` without widening its parameter type for every caller.
 */
type PlainDescriptionKey = `shortcuts.${string}`;
