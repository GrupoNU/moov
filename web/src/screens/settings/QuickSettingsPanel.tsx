import { useEffect, useId, useRef } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { usePrefs } from "../../mail/PrefsProvider";
import {
  DENSITIES,
  INBOX_TYPES,
  READING_PANES,
  THEMES,
  type Density,
  type InboxType,
  type ReadingPane,
  type Theme,
} from "../../mail/prefs";
import { applyTheme, saveThemePreference } from "../../theme/theme";
import {
  DensityThumb,
  InboxTypeThumb,
  ReadingPaneThumb,
  ThemeThumb,
} from "./QuickThumbnails";
import { OptionGroup } from "./OptionGroup";
import {
  DENSITY_LABELS,
  INBOX_TYPE_LABELS,
  READING_PANE_LABELS,
  THEME_LABELS,
} from "./optionLabels";
import styles from "./QuickSettingsPanel.module.css";

/**
 * Quick settings — the gear's docked panel (E12/B2, canon 07 §4).
 *
 * # A dock, not a dialog, and the difference is the whole design
 *
 * Gmail's quick panel slides in from the right edge and the LIST SHRINKS to
 * make room. There is no overlay, no backdrop and no inertness: the mail behind
 * it stays live, and that is the point — you change the density and watch the
 * rows you are already looking at change, then change it again. A modal would
 * hide the very thing every option in the panel is about.
 *
 * That rules out `<dialog>.showModal()`, which the full settings sheet uses for
 * exactly the opposite reason. So the three properties `showModal()` would have
 * supplied are re-established by hand, and only the two that are CORRECT for a
 * non-modal surface:
 *
 *   - **Escape closes**, because every dismissible surface in this app does;
 *   - **focus moves in on open and returns to the gear on close**, because a
 *     panel that appears without focus is a panel a keyboard user has to hunt
 *     for with Tab, and one that drops focus on close leaves them at the top of
 *     the document.
 *
 * The third — a focus TRAP — is deliberately not implemented. Trapping focus in
 * a surface that leaves the page interactive is a lie about the page's state:
 * Tab must be able to leave, because the user can still click out there.
 *
 * # Semantics: `complementary`, not `dialog`
 *
 * `role="dialog"` on a non-modal panel makes screen readers announce a modal
 * context that does not exist, and some will not let the user tab out of it.
 * A labelled `complementary` landmark is what this actually is — supporting
 * content beside the main region — and it gives the user a landmark to jump to,
 * which a dialog does not.
 *
 * # It owns no state
 *
 * Every control here writes the SAME `PrefsProvider` key the full settings page
 * writes. This is a second surface over one source of truth, not a second copy
 * of the settings: changing density here and opening the page shows the new
 * value, because there is nothing to keep in step.
 */

export interface QuickSettingsPanelProps {
  readonly isOpen: boolean;
  readonly onClose: () => void;
  /** "See all settings" — routes to the settings page (B3). */
  readonly onOpenFullSettings: () => void;
}

export function QuickSettingsPanel({
  isOpen,
  onClose,
  onOpenFullSettings,
}: QuickSettingsPanelProps): React.JSX.Element | null {
  const { t } = useTranslation();
  const { prefs, setPref } = usePrefs();
  const panelRef = useRef<HTMLElement | null>(null);
  const headingId = useId();
  /*
   * Where focus came FROM, captured on open.
   *
   * Read from `document.activeElement` rather than taking a ref to the gear:
   * the panel can also be opened by a route or a future shortcut, and a
   * hard-wired return target would then send focus to a button the user never
   * touched. Whatever had focus is the honest place to give it back.
   */
  const returnFocusRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    if (!isOpen) return undefined;
    returnFocusRef.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    /*
     * Focus lands on the PANEL, not on its first control. Landing on "See all
     * settings" would make Escape-then-Enter a way to navigate away by
     * accident, and it would read the button's label before the panel's own
     * name — so the user hears what they activated only after hearing where
     * they can go. The container is `tabIndex={-1}` for exactly this: focusable
     * by script, never by Tab.
     */
    panelRef.current?.focus();
    return undefined;
  }, [isOpen]);

  useEffect(() => {
    if (!isOpen) return undefined;
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key !== "Escape") return;
      /*
       * Stopped, so the shell's global Escape does not ALSO fire on this press
       * and close the reading pane behind the panel — dismissing two surfaces
       * with one key is the behaviour that makes people stop trusting Escape.
       */
      event.stopPropagation();
      onClose();
    };
    // Capture phase, so this runs before the shell's document-level handler.
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [isOpen, onClose]);

  /*
   * Focus returns on the way OUT, in a cleanup rather than in the close
   * handler, so it happens however the panel came to be closed — the X, Escape,
   * the gear toggling it off, or a route change that unmounted it.
   */
  useEffect(() => {
    if (!isOpen) return undefined;
    return () => {
      returnFocusRef.current?.focus();
    };
  }, [isOpen]);

  /**
   * The theme's immediate paint and its pre-paint cache.
   *
   * This is the ONE preference in the panel that needs more than a `setPref`,
   * and it is the same contract `ThemeToggle` carried before the control moved
   * here (B3). Two things must happen the instant the radio moves, neither of
   * which the round trip can wait for:
   *
   *   - the `data-theme` attribute the CSS keys on, or the theme becomes the
   *     one setting that visibly lags its own control;
   *   - `saveThemePreference`, because that cache is what the pre-paint script
   *     in `index.html` reads on the NEXT load. Left behind, the following load
   *     flashes the old colours before React catches up.
   *
   * `App.tsx` also applies the theme from `prefs.theme`, and the redundancy is
   * deliberate rather than accidental: that effect is what makes a theme
   * changed on another device apply here, and this one is what makes a theme
   * changed HERE apply before the server has answered. They converge on the
   * same value, so neither can win a race the user would notice.
   */
  const theme = prefs.theme;
  useEffect(() => {
    if (typeof document === "undefined") return;
    applyTheme(theme, document.documentElement);
    saveThemePreference(theme);
  }, [theme]);

  if (!isOpen) return null;

  return (
    /*
     * `<aside>` IS `role="complementary"` — the role is implicit and stating
     * it is redundant, which the linter is right to reject. What matters is
     * that this is not a `dialog`: see the header on why a non-modal surface
     * must not claim modal semantics.
     */
    <aside
      ref={panelRef}
      className={styles.panel}
      aria-labelledby={headingId}
      tabIndex={-1}
    >
      <div className={styles.header}>
        <h2 className={styles.title} id={headingId}>
          {t("quickSettings.title")}
        </h2>
        <button
          type="button"
          className={styles.close}
          onClick={onClose}
          aria-label={t("quickSettings.close")}
        >
          <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true" focusable="false">
            <path d="M5.5 5.5l9 9M14.5 5.5l-9 9" />
          </svg>
        </button>
      </div>

      {/*
        The full-width outlined button at the TOP, which is where Gmail puts
        it: the panel is a shortcut to four settings, and the door to the other
        twenty-odd has to be the first thing you see, not something you scroll
        past the previews to find.
      */}
      <button type="button" className={styles.seeAll} onClick={onOpenFullSettings}>
        {t("quickSettings.seeAll")}
      </button>

      <div className={styles.sections}>
        <OptionGroup<Density>
          legendKey="settings.density.label"
          value={prefs.density}
          options={DENSITIES}
          labelKey={(option) => DENSITY_LABELS[option]}
          onChange={(next) => {
            void setPref("density", next);
          }}
          renderThumb={(option) => <DensityThumb density={option} />}
        />

        <OptionGroup<Theme>
          legendKey="theme.label"
          value={prefs.theme}
          options={THEMES}
          labelKey={(option) => THEME_LABELS[option]}
          onChange={(next) => {
            void setPref("theme", next);
          }}
          renderThumb={(option) => <ThemeThumb theme={option} />}
        />

        <OptionGroup<InboxType>
          legendKey="settings.inboxType.label"
          value={prefs.inboxType}
          options={INBOX_TYPES}
          labelKey={(option) => INBOX_TYPE_LABELS[option]}
          onChange={(next) => {
            void setPref("inboxType", next);
          }}
          renderThumb={(option) => <InboxTypeThumb inboxType={option} />}
        />

        <OptionGroup<ReadingPane>
          legendKey="settings.readingPane.label"
          value={prefs.readingPane}
          options={READING_PANES}
          labelKey={(option) => READING_PANE_LABELS[option]}
          onChange={(next) => {
            void setPref("readingPane", next);
          }}
          renderThumb={(option) => <ReadingPaneThumb pane={option} />}
        />
      </div>
    </aside>
  );
}
