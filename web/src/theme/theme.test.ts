import { beforeEach, describe, expect, it } from "vitest";

import {
  applyTheme,
  DEFAULT_THEME,
  isThemePreference,
  loadThemePreference,
  saveThemePreference,
  type ThemePreference,
} from "./theme";

/**
 * The theme model.
 *
 * The first describe block is the regression test for the defect this change
 * exists to fix: a first-time visitor on a dark OS was being shown a dark
 * webmail nobody chose. The product decision is that light is the default and
 * "system" is an explicit CHOICE, so the default is pinned here rather than
 * left as an implementation detail somebody can flip back without noticing.
 */

/** An in-memory Storage, so no test touches the real localStorage. */
function memoryStorage(seed?: Record<string, string>): Storage {
  const map = new Map<string, string>(Object.entries(seed ?? {}));
  return {
    getItem: (key: string) => map.get(key) ?? null,
    setItem: (key: string, value: string) => void map.set(key, value),
    removeItem: (key: string) => void map.delete(key),
    clear: () => { map.clear(); },
    key: () => null,
    get length() { return map.size; },
  };
}

/** A Storage that throws on every access, like a browser with storage blocked. */
function blockedStorage(): Storage {
  const boom = (): never => { throw new Error("storage is blocked"); };
  return {
    getItem: boom,
    setItem: boom,
    removeItem: boom,
    clear: boom,
    key: boom,
    length: 0,
  };
}

describe("the default theme", () => {
  it("is light, not system", () => {
    // The defect: with "system" as the default, a dark OS decided the theme
    // for a user who had never been asked.
    expect(DEFAULT_THEME).toBe("light");
  });

  it("is what a fresh profile with no stored preference gets", () => {
    expect(loadThemePreference(memoryStorage())).toBe("light");
  });

  it("is what a corrupted stored value falls back to", () => {
    // A value from a future version, or one a user typed into devtools, must
    // not leave the app in a state that styles nothing.
    expect(loadThemePreference(memoryStorage({ "moov.theme.v1": "midnight" }))).toBe("light");
  });

  it("is what a blocked storage falls back to", () => {
    expect(loadThemePreference(blockedStorage())).toBe("light");
  });

  it("stamps data-theme=light on a fresh document, so a dark OS media query cannot win", () => {
    // tokens.css guards its dark block with :not([data-theme="light"]). The
    // attribute being PRESENT is therefore the whole mechanism by which an
    // explicit light choice beats prefers-color-scheme: dark.
    const root = document.createElement("html");
    applyTheme(loadThemePreference(memoryStorage()), root);
    expect(root.getAttribute("data-theme")).toBe("light");
  });
});

describe("persistence", () => {
  it("round-trips every preference", () => {
    for (const theme of ["light", "dark", "system"] as const) {
      const storage = memoryStorage();
      saveThemePreference(theme, storage);
      expect(loadThemePreference(storage)).toBe(theme);
    }
  });

  it("survives a reload: a stored choice beats the default", () => {
    const storage = memoryStorage();
    saveThemePreference("dark", storage);
    // A second "page load" reading the same storage.
    expect(loadThemePreference(storage)).toBe("dark");
    expect(loadThemePreference(storage)).not.toBe(DEFAULT_THEME);
  });

  it("keeps an explicit 'system' rather than collapsing it to the default", () => {
    // The regression that would make "follow system" un-selectable: if
    // "system" were stored and then read back as the default, the radio would
    // visibly snap back to Light on every reload.
    const storage = memoryStorage();
    saveThemePreference("system", storage);
    expect(loadThemePreference(storage)).toBe("system");
  });

  it("does not throw when storage is blocked", () => {
    expect(() => { saveThemePreference("dark", blockedStorage()); }).not.toThrow();
  });
});

describe("applyTheme", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("html");
  });

  it("stamps the attribute for an explicit choice", () => {
    applyTheme("dark", root);
    expect(root.getAttribute("data-theme")).toBe("dark");
    applyTheme("light", root);
    expect(root.getAttribute("data-theme")).toBe("light");
  });

  it("REMOVES the attribute for 'system' rather than setting it to 'system'", () => {
    // An unrecognised attribute value would leave both the media query and the
    // attribute selector inactive, which is how a theme control ends up with a
    // state that styles nothing.
    applyTheme("dark", root);
    applyTheme("system", root);
    expect(root.hasAttribute("data-theme")).toBe(false);
  });
});

describe("isThemePreference", () => {
  it("accepts exactly the three states", () => {
    const valid: ThemePreference[] = ["light", "dark", "system"];
    for (const value of valid) expect(isThemePreference(value)).toBe(true);
    for (const value of [null, undefined, "", "System", "auto", 0, {}]) {
      expect(isThemePreference(value)).toBe(false);
    }
  });
});
