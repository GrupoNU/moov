/**
 * The composer's remembered body mode (L3 E7; canon §2.3, UNSOURCED row).
 *
 * # Honesty about the citation
 *
 * Plain-text compose mode is in the canon's UNSOURCED register (§5): it is
 * behaviourally real in Gmail — the composer's ⋯ menu carries "Plain text mode"
 * and it persists between messages — but no fetchable Google page documents it
 * as a settings row. Per P1 the unsourced founds no decision, so the DECISION
 * here rests on the observable behaviour we are matching, and the fact that it
 * is unsourced is recorded rather than dressed up as a citation.
 *
 * # Why the choice persists at all
 *
 * Because it is a property of how a person writes, not of one message. Someone
 * who works in plain text works in plain text; making them re-pick it on every
 * composer is the kind of small friction that makes a client feel like it is
 * not paying attention. Gmail persists it, and it is the right call regardless.
 *
 * The persistence deliberately does NOT apply to a resumed draft or a reply
 * whose original was HTML — see `Composer`. A stored preference must not
 * silently flatten a message that already has formatting in it; the mode is a
 * default for NEW compositions, and the composer's own state wins once a body
 * exists.
 *
 * # The same named gap as the opt-out next door
 *
 * `localStorage`, per browser, because prefs v1 has no key for it and the
 * server refuses unknown keys. It does not roam. That is a smaller consequence
 * here than for the address opt-out — a body-mode default is a convenience, not
 * a privacy control — so it is noted here rather than in the UI.
 *
 * @todo Prefs v2: a `composeBodyMode: "rich" | "plain"` key, read through
 *   `Prefs/get` with this as the migration source.
 */

const STORAGE_KEY = "moov.composeBodyMode.v1";

/** The two surfaces `BodyEditor` already implements. */
export type BodyMode = "rich" | "plain";

/**
 * The default for a new message.
 *
 * Rich, matching Gmail and matching what `newDraft(true)` already passed before
 * this preference existed — so a user who never opens the menu sees exactly the
 * composer they saw yesterday.
 */
export const DEFAULT_BODY_MODE: BodyMode = "rich";

/** Reads the remembered mode. Never throws; an unreadable store means default. */
export function loadBodyMode(storage?: Storage): BodyMode {
  try {
    const store = storage ?? globalThis.localStorage;
    const raw = store?.getItem(STORAGE_KEY);
    // Only the exact stored value counts. Anything else — absent, corrupted, a
    // value written by a future build — falls back rather than being coerced.
    return raw === "plain" ? "plain" : DEFAULT_BODY_MODE;
  } catch {
    return DEFAULT_BODY_MODE;
  }
}

/** Writes the remembered mode. A blocked storage costs the memory, not an error. */
export function saveBodyMode(mode: BodyMode, storage?: Storage): void {
  try {
    const store = storage ?? globalThis.localStorage;
    store?.setItem(STORAGE_KEY, mode);
  } catch {
    // The composer still switches modes; the choice just does not outlive the
    // session.
  }
}
