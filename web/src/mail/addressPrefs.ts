/**
 * The autocomplete opt-out (L3 E7; canon §2.3 — "I'll add contacts myself").
 *
 * # What Gmail offers and what this mirrors
 *
 * Gmail auto-saves the addresses you mail to and completes from them, with one
 * user-facing escape hatch worded exactly "I'll add contacts myself"
 * (/contacts/answer/1069522). Turning it off stops the AUTO-SAVING; it does not
 * merely hide the results. This module is that switch.
 *
 * Our version does one thing Gmail's row does not, because our index is local
 * and therefore erasable by us: opting out also OFFERS to delete what was
 * already collected. An opt-out that leaves the collected data in place is a
 * setting about display, not about privacy, and the row would be making a
 * promise it does not keep. The deletion is offered rather than forced — a user
 * may want to stop collecting while keeping the addresses they have.
 *
 * # Where the choice lives now: prefs v2, and it ROAMS
 *
 * The gap this file used to name is closed. `Prefs.addressAutocomplete`
 * (`"auto"` | `"manual"`, `internal/jmap/mail/prefs.go`) is the durable home,
 * so switching the collection off on the laptop switches it off on the phone.
 *
 * `localStorage` did not go away; it changed job, exactly as the theme's copy
 * did. The composer's suggestion popup and the index's write-through both run
 * on the FIRST render after a reload, before the session's `Prefs/get` has
 * resolved — and defaulting to "on" for that window would collect addresses a
 * user has opted out of, which is the one failure this setting exists to
 * prevent. So the mirror is read as the pre-load answer and written through on
 * every prefs change: a cache, never a second source of truth.
 *
 * What stays local and always will: the INDEX itself
 * (`offline/addressStore.ts`). Only the choice roams. The addresses are
 * browser-local by design, they are never uploaded, and the settings row says
 * so — that sentence in the description is about the data, not about the
 * setting, and it survives.
 */

import type { AddressAutocompleteMode } from "./prefs";

const STORAGE_KEY = "moov.addressAutocomplete.v1";

/**
 * The default: ON, as Gmail's is.
 *
 * A mail client whose recipient field does not complete is one people notice
 * within a minute, and Gmail — whose privacy posture is the strictest thing in
 * this canon — ships it on. The opt-out is the divergence a user chooses, not
 * the state they start in.
 */
export const DEFAULT_ADDRESS_AUTOCOMPLETE = true;

/**
 * Reads the preference. Never throws — a blocked storage keeps the default.
 *
 * The parse is strict about the OFF value only: anything that is not the exact
 * stored `"off"` means on. A corrupted or half-written value therefore fails
 * toward the working feature rather than toward a composer that silently stops
 * completing, which is the failure a user would report as a bug rather than as
 * a setting.
 */
export function loadAddressAutocomplete(storage?: Storage): boolean {
  try {
    const store = storage ?? globalThis.localStorage;
    const raw = store?.getItem(STORAGE_KEY);
    if (raw === null || raw === undefined) return DEFAULT_ADDRESS_AUTOCOMPLETE;
    return raw !== "off";
  } catch {
    return DEFAULT_ADDRESS_AUTOCOMPLETE;
  }
}

/** Writes the preference. A blocked storage costs the setting, never an error. */
export function saveAddressAutocomplete(enabled: boolean, storage?: Storage): void {
  try {
    const store = storage ?? globalThis.localStorage;
    store?.setItem(STORAGE_KEY, enabled ? "on" : "off");
  } catch {
    // Private mode, a full quota, a locked-down browser. The composer still
    // works; the choice just does not survive a reload. Never a thrown error
    // on a preference.
  }
}

// ---------------------------------------------------------------------------
// prefs v2 — the durable home
// ---------------------------------------------------------------------------

/**
 * Whether this browser ever wrote the local mirror.
 *
 * It is what separates "the user opted out here" from "this browser has never
 * had an opinion", which the boolean alone cannot express — `false` from
 * {@link loadAddressAutocomplete} could be either an opt-out or a default. The
 * migration needs the distinction: a browser with no stored value has nothing
 * to migrate, and pushing its default would overwrite a real choice made on
 * another device with a value nobody chose.
 */
export function hasStoredAddressAutocomplete(storage?: Storage): boolean {
  try {
    const store = storage ?? globalThis.localStorage;
    const raw = store?.getItem(STORAGE_KEY);
    return raw !== null && raw !== undefined;
  } catch {
    return false;
  }
}

/** The prefs value, as the boolean this module and its consumers speak. */
export function addressAutocompleteEnabled(mode: AddressAutocompleteMode): boolean {
  return mode === "auto";
}

/** The boolean, as the prefs value. */
export function addressAutocompleteMode(enabled: boolean): AddressAutocompleteMode {
  return enabled ? "auto" : "manual";
}

/**
 * The one-time migration: the local choice, when prefs are still at their
 * default and this browser holds a real opt-out.
 *
 * Returns `undefined` when there is nothing to push, which is the common case
 * and covers three situations that are all "leave the server alone":
 *
 *   - this browser never stored a choice (nothing to migrate);
 *   - it stored the same value prefs already carry (a write would be a no-op
 *     that still advances the state cursor in every other tab);
 *   - prefs already say `"manual"` (the opt-out has been expressed on the
 *     account, and a browser that never turned it off must not turn it back on
 *     — the asymmetry is deliberate, since re-enabling collection against a
 *     user's stated wish is the harmful direction of this particular setting).
 *
 * The last rule is why this is not the symmetric "local wins if it differs". An
 * ON local mirror meeting a `"manual"` account is exactly a second device that
 * predates the opt-out, and honouring it would silently resume collecting.
 */
export function addressAutocompleteMigration(
  prefsMode: AddressAutocompleteMode,
  storage?: Storage,
): AddressAutocompleteMode | undefined {
  if (!hasStoredAddressAutocomplete(storage)) return undefined;
  if (prefsMode === "manual") return undefined;
  const local = addressAutocompleteMode(loadAddressAutocomplete(storage));
  return local === prefsMode ? undefined : local;
}
