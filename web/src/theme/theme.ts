/**
 * Theme selection.
 *
 * Three states, matching what tokens.css implements: an explicit "light" or
 * "dark" stamps `data-theme` on <html>, and "system" removes the attribute so
 * `prefers-color-scheme` decides. Removing the attribute — rather than
 * computing the OS preference and stamping the result — is what makes the app
 * FOLLOW a theme change while it is open, without a listener.
 */

export type ThemePreference = "light" | "dark" | "system";

const STORAGE_KEY = "moov.theme.v1";

/** The default. "system" is the only honest default: the user already told the
 * OS what they want, and overriding that would be presumptuous. */
export const DEFAULT_THEME: ThemePreference = "system";

export function isThemePreference(value: unknown): value is ThemePreference {
  return value === "light" || value === "dark" || value === "system";
}

/**
 * Reads the stored preference.
 *
 * localStorage rather than sessionStorage — the opposite of the credential
 * decision, and for the same reasoning applied to different data: a theme is
 * not a secret, and a user who picked dark mode wants it next week too.
 */
export function loadThemePreference(storage?: Storage): ThemePreference {
  try {
    const store = storage ?? globalThis.localStorage;
    const raw = store?.getItem(STORAGE_KEY);
    return isThemePreference(raw) ? raw : DEFAULT_THEME;
  } catch {
    return DEFAULT_THEME;
  }
}

export function saveThemePreference(theme: ThemePreference, storage?: Storage): void {
  try {
    const store = storage ?? globalThis.localStorage;
    store?.setItem(STORAGE_KEY, theme);
  } catch {
    // A blocked storage costs the preference across reloads and nothing else.
  }
}

/**
 * Applies a preference to the document root.
 *
 * "system" REMOVES the attribute rather than setting it to "system": the CSS
 * is written against the presence of the attribute, and an unrecognised value
 * would leave both the media query and the attribute selector inactive, which
 * is how a theme toggle ends up with a state that styles nothing.
 */
export function applyTheme(theme: ThemePreference, root: HTMLElement): void {
  if (theme === "system") {
    root.removeAttribute("data-theme");
    return;
  }
  root.setAttribute("data-theme", theme);
}
