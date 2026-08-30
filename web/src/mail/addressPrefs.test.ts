import { describe, expect, it } from "vitest";

import {
  DEFAULT_ADDRESS_AUTOCOMPLETE,
  loadAddressAutocomplete,
  saveAddressAutocomplete,
} from "./addressPrefs";
import { DEFAULT_BODY_MODE, loadBodyMode, saveBodyMode } from "./composePrefs";

/**
 * The two localStorage-backed composer preferences (E7).
 *
 * Both follow E8's precedent (`labelStore.ts`) and both carry the same named
 * gap: prefs v1 has no key for them, so they do not roam. What the tests pin is
 * the behaviour that gap makes load-bearing — that a broken or hostile storage
 * degrades to the working default instead of breaking the composer.
 */

/** A Storage that behaves, backed by a Map. */
function memoryStorage(initial: Record<string, string> = {}): Storage {
  const map = new Map(Object.entries(initial));
  return {
    getItem: (key) => map.get(key) ?? null,
    setItem: (key, value) => {
      map.set(key, value);
    },
    removeItem: (key) => {
      map.delete(key);
    },
    clear: () => {
      map.clear();
    },
    key: (index) => [...map.keys()][index] ?? null,
    get length() {
      return map.size;
    },
  };
}

/** A Storage that throws on everything — private mode, a full quota. */
function hostileStorage(): Storage {
  const boom = (): never => {
    throw new Error("storage is blocked");
  };
  return {
    getItem: boom,
    setItem: boom,
    removeItem: boom,
    clear: boom,
    key: boom,
    get length(): number {
      return boom();
    },
  };
}

describe("address autocomplete preference", () => {
  it("defaults ON, as Gmail's does", () => {
    expect(DEFAULT_ADDRESS_AUTOCOMPLETE).toBe(true);
    expect(loadAddressAutocomplete(memoryStorage())).toBe(true);
  });

  it("round-trips the opt-out", () => {
    const storage = memoryStorage();
    saveAddressAutocomplete(false, storage);
    expect(loadAddressAutocomplete(storage)).toBe(false);

    saveAddressAutocomplete(true, storage);
    expect(loadAddressAutocomplete(storage)).toBe(true);
  });

  it("treats a corrupted value as ON, not as OFF", () => {
    // Failing toward the working feature: a user whose storage got scrambled
    // should see autocomplete keep working, not a composer that silently
    // stopped completing.
    expect(loadAddressAutocomplete(memoryStorage({ "moov.addressAutocomplete.v1": "?" }))).toBe(
      true,
    );
  });

  it("survives a storage that throws", () => {
    expect(loadAddressAutocomplete(hostileStorage())).toBe(true);
    expect(() => {
      saveAddressAutocomplete(false, hostileStorage());
    }).not.toThrow();
  });
});

describe("compose body mode preference", () => {
  it("defaults to rich", () => {
    expect(DEFAULT_BODY_MODE).toBe("rich");
    expect(loadBodyMode(memoryStorage())).toBe("rich");
  });

  it("remembers the last choice", () => {
    const storage = memoryStorage();
    saveBodyMode("plain", storage);
    expect(loadBodyMode(storage)).toBe("plain");

    saveBodyMode("rich", storage);
    expect(loadBodyMode(storage)).toBe("rich");
  });

  it("falls back for any value it did not write", () => {
    expect(loadBodyMode(memoryStorage({ "moov.composeBodyMode.v1": "markdown" }))).toBe("rich");
    expect(loadBodyMode(memoryStorage({ "moov.composeBodyMode.v1": "" }))).toBe("rich");
  });

  it("survives a storage that throws", () => {
    expect(loadBodyMode(hostileStorage())).toBe("rich");
    expect(() => {
      saveBodyMode("plain", hostileStorage());
    }).not.toThrow();
  });
});
