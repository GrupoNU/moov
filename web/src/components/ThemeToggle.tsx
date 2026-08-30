import { useEffect, useId, useState } from "react";

import { useTranslation } from "../i18n/I18nProvider";
import {
  applyTheme,
  loadThemePreference,
  saveThemePreference,
  type ThemePreference,
} from "../theme/theme";
import styles from "./ThemeToggle.module.css";

/**
 * The theme control: light / dark / system.
 *
 * # Why a radio group and not a two-state switch
 *
 * A switch can only express two states, which forces the app to drop "system"
 * — and "system" is the state most users actually want, because they already
 * told their OS. A three-way control keeps it available and makes the current
 * choice visible at a glance rather than inferable from an icon.
 *
 * It is built from real radio inputs with a shared name, so the keyboard
 * behaviour (arrow keys move between options, the group is one tab stop) comes
 * from the browser rather than from JavaScript that would have to reimplement
 * it — and would get it subtly wrong.
 */
export interface ThemeToggleProps {
  /**
   * The current theme, when a caller owns it.
   *
   * L3 E5 makes preferences ACCOUNT-level: the server's `Prefs.theme` is the
   * source of truth and localStorage becomes a pre-paint cache mirroring it
   * (that is what the theme module's own header calls it). So the settings
   * screen passes the value down from the prefs provider.
   *
   * Omitted, the control keeps its P1 behaviour and reads localStorage itself
   * — which is what the login screen and any pre-session surface still need,
   * since there is no account to have a preference yet.
   */
  readonly value?: ThemePreference;
  /** Called with the chosen theme. Required to make {@link value} meaningful. */
  readonly onChange?: (theme: ThemePreference) => void;
}

export function ThemeToggle({ value, onChange }: ThemeToggleProps = {}): React.JSX.Element {
  const { t } = useTranslation();
  const groupName = useId();
  const [uncontrolled, setUncontrolled] = useState<ThemePreference>(() =>
    loadThemePreference(),
  );

  const isControlled = value !== undefined;
  const theme = value ?? uncontrolled;

  const setTheme = (next: ThemePreference): void => {
    if (!isControlled) setUncontrolled(next);
    onChange?.(next);
  };

  /*
   * The DOM write happens here in BOTH modes, and that is deliberate: the
   * attribute and the localStorage cache must move the instant the radio does,
   * whether the value came from local state or from the account. Deferring the
   * paint to the owner's round trip would make the theme the one setting that
   * visibly lags — and the cache is what the pre-paint script in index.html
   * reads on the next load, so it has to stay in step with the choice even
   * while the save is in flight.
   */
  useEffect(() => {
    if (typeof document === "undefined") return;
    applyTheme(theme, document.documentElement);
    saveThemePreference(theme);
  }, [theme]);

  const options: readonly { value: ThemePreference; label: string }[] = [
    { value: "light", label: t("theme.light") },
    { value: "dark", label: t("theme.dark") },
    { value: "system", label: t("theme.system") },
  ];

  return (
    /*
     * A fieldset with a legend is the accessible grouping for a set of radios;
     * the legend is visually hidden because the three labels already say what
     * the group is about, but a screen reader announces "Theme" when entering
     * it.
     */
    <fieldset className={styles.group}>
      <legend className="visually-hidden">{t("theme.label")}</legend>
      {options.map((option) => (
        <label key={option.value} className={styles.option}>
          <input
            className={styles.radio}
            type="radio"
            name={groupName}
            value={option.value}
            checked={theme === option.value}
            onChange={() => { setTheme(option.value); }}
          />
          <span className={styles.optionLabel}>{option.label}</span>
        </label>
      ))}
    </fieldset>
  );
}
