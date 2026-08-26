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
export function ThemeToggle(): React.JSX.Element {
  const { t } = useTranslation();
  const groupName = useId();
  const [theme, setTheme] = useState<ThemePreference>(() => loadThemePreference());

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
