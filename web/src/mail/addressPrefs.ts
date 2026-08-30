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
 * # The named gap: this does not roam
 *
 * The server's `Prefs` singleton (`internal/jmap/mail/prefs.go`) validates
 * strictly and refuses unknown keys with `invalidProperties` — correct
 * behaviour, and the reason this module does not invent a key. There is no
 * `addressAutocomplete` in prefs v1.
 *
 * So this follows the precedent E8 set for label metadata (`mail/labelStore.ts`):
 * `localStorage`, per browser, with the consequence stated ON SCREEN in the
 * settings row rather than left to be discovered —
 *
 *   **The opt-out applies to this browser only.** Switch it off on the laptop
 *   and the phone keeps collecting, because the phone has its own index. That
 *   is arguably the honest behaviour for a setting governing a browser-local
 *   store — but it is not what a user expects from a preference, so it is said
 *   out loud.
 *
 * @todo Prefs schema v2 (`internal/jmap/mail/prefs.go` + `store.Prefs`) is the
 *   durable home: an `addressAutocomplete: boolean` key served by the existing
 *   `Prefs/get`/`Prefs/set` under `CAP_PREFS`. When it lands,
 *   {@link loadAddressAutocomplete} reads prefs with localStorage as a one-time
 *   migration source, and this comment is deleted rather than amended.
 */

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
