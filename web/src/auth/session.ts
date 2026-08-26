/**
 * Session persistence.
 *
 * # What is stored, and what is deliberately not
 *
 * The server authenticates with HTTP Basic (arbitration J-A1), which means
 * every request carries the password. To stay signed in across a reload, the
 * app therefore has to keep the credential somewhere.
 *
 * The choice made here is `sessionStorage`, and the reasoning is worth stating
 * because the alternatives are all worse in a specific way:
 *
 *   - localStorage would survive the browser closing. On a shared or public
 *     machine that leaves a working mail password on disk indefinitely. A user
 *     who closes the browser reasonably believes they have left.
 *   - A cookie cannot be HttpOnly here (script must read it to build the
 *     Authorization header), so it gains nothing over storage and adds CSRF
 *     surface.
 *   - In-memory only would be the most secure, and is what a bearer-token
 *     design would allow — but with Basic it means re-typing the password on
 *     every reload, which is not a mail client.
 *
 * So: sessionStorage, scoped to the tab and cleared when the browser closes,
 * with an explicit sign-out that erases it. When the server grows bearer
 * tokens (noted as phase 2 in L2-jmap-server §2.2), THIS is the file that
 * changes and nothing else — which is why the stored shape is behind a type
 * rather than spread across the app.
 *
 * The stored value is not encrypted, and pretending otherwise would be
 * security theatre: any key the app could use to decrypt it would sit beside
 * it in the same storage.
 */

import type { BasicCredentials } from "../api/jmap";

/** The key the credential is stored under. */
const STORAGE_KEY = "moov.session.v1";

/**
 * What persistence holds. Only the credential and the identity it proved —
 * never the Session object, which is refetched on every start because its
 * `state` string may have changed while the tab was closed.
 */
export interface PersistedSession {
  readonly username: string;
  readonly password: string;
}

/** The storage the app uses; injectable so tests do not touch the real one. */
export interface SessionStorageLike {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

/**
 * Returns the browser's sessionStorage, or a no-op when it is unavailable.
 *
 * Unavailable is a real state, not a hypothetical: Safari in private mode has
 * historically thrown on setItem, and a storage exception must never take down
 * a login screen. The fallback simply means the session does not survive a
 * reload.
 */
export function defaultSessionStorage(): SessionStorageLike {
  try {
    const storage = globalThis.sessionStorage;
    if (storage !== undefined && storage !== null) {
      // Probe it: existence does not imply usability under a strict policy.
      const probe = "__moov_probe__";
      storage.setItem(probe, "1");
      storage.removeItem(probe);
      return storage;
    }
  } catch {
    // Fall through to the no-op.
  }
  return {
    getItem: () => null,
    setItem: () => undefined,
    removeItem: () => undefined,
  };
}

/** Reads the persisted credential, or undefined when there is none. */
export function loadSession(
  storage: SessionStorageLike = defaultSessionStorage(),
): PersistedSession | undefined {
  let raw: string | null;
  try {
    raw = storage.getItem(STORAGE_KEY);
  } catch {
    return undefined;
  }
  if (raw === null || raw === "") return undefined;

  try {
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null) return undefined;
    const record = parsed as Record<string, unknown>;
    const username = record.username;
    const password = record.password;
    if (typeof username !== "string" || typeof password !== "string") return undefined;
    if (username === "" || password === "") return undefined;
    return { username, password };
  } catch {
    // A corrupted entry is discarded rather than repaired.
    return undefined;
  }
}

/** Persists a credential. */
export function saveSession(
  credentials: BasicCredentials,
  storage: SessionStorageLike = defaultSessionStorage(),
): void {
  try {
    storage.setItem(
      STORAGE_KEY,
      JSON.stringify({ username: credentials.username, password: credentials.password }),
    );
  } catch {
    // Storage full or blocked: the user stays signed in for this page view
    // only, which is strictly better than failing the sign-in.
  }
}

/** Erases the persisted credential. This is what sign-out does. */
export function clearSession(storage: SessionStorageLike = defaultSessionStorage()): void {
  try {
    storage.removeItem(STORAGE_KEY);
  } catch {
    // Nothing useful to do; the value is tab-scoped and dies with the tab.
  }
}
