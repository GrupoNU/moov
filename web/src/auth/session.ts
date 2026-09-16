/**
 * Session persistence.
 *
 * # What is stored, and what is deliberately not
 *
 * The server authenticates two ways. HTTP Basic (arbitration J-A1) is the
 * primary scheme: the password is validated by a real IMAP LOGIN against
 * Dovecot, and every request carries it. Delegated sign-in (epic M2, contract
 * §3) is the second: an external portal signs a short-lived JWT, the PWA
 * exchanges it for an opaque Moov session token, and every request carries
 * THAT as `Authorization: Bearer`. To stay signed in across a reload, the app
 * therefore has to keep one or the other somewhere.
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
 * with an explicit sign-out that erases it.
 *
 * # The shape is a discriminated union, and the honest reason
 *
 * The original version of this file anticipated bearer tokens and said "THIS
 * is the file that changes and nothing else". That was half right, and the
 * half that was wrong is worth recording rather than quietly deleting.
 *
 * It IS the only file that decides what persistence holds. It is NOT the only
 * file that changes, because the stored value is not the only thing that
 * differs between the two schemes: a Basic session renews nothing and signs
 * out locally, while a bearer session has an expiry, a renewal deadline, an
 * absolute ceiling and a server-side logout — and a 401 means something
 * categorically different under each (with Basic, "your password is wrong,
 * here is the form"; with a bearer token, "this link is dead, go back to the
 * portal", because there is no password to type).
 *
 * So the union carries the FACTS of each scheme rather than flattening them
 * into an optional-password bag, and the places that must branch on the
 * difference — the HTTP layer's Authorization header, the 401 handler, the
 * renewal timer — read the discriminant instead of guessing from which field
 * happens to be populated.
 *
 * A stored bearer session deliberately keeps its `expiresAt` and
 * `absoluteExpiresAt`: they are the server's own words, and holding them lets
 * a reload discard a session that is already dead without a round trip that
 * would only be answered with a 401.
 *
 * The stored value is not encrypted, and pretending otherwise would be
 * security theatre: any key the app could use to decrypt it would sit beside
 * it in the same storage.
 */

import type { BasicCredentials } from "../api/jmap";

/** The key the credential is stored under. */
const STORAGE_KEY = "moov.session.v1";

/**
 * A stored HTTP Basic credential — the classic sign-in.
 *
 * `kind` is present on both members so a value read back from storage can be
 * narrowed without inspecting its fields. A value written by an older build
 * has no `kind` at all, and {@link loadSession} treats that absence as
 * "basic", which is what it was.
 */
export interface StoredBasicSession {
  readonly kind: "basic";
  readonly username: string;
  readonly password: string;
}

/**
 * A stored delegated session token (contract §3.4).
 *
 * `username` is the mailbox address the session grants, kept for the same
 * reason the Basic member keeps it: the shell shows it, and it is what makes
 * "which account is this" answerable without waiting for the JMAP Session.
 *
 * The three timestamps are ISO-8601 strings exactly as the server sent them,
 * not `Date`s: they round-trip through `JSON.stringify` unchanged, and
 * parsing at the point of comparison keeps a corrupt value from poisoning the
 * whole record.
 */
export interface StoredBearerSession {
  readonly kind: "bearer";
  readonly username: string;
  /** The opaque `mds1_…` session token. */
  readonly token: string;
  /** When the sliding expiry lapses (RFC 3339). */
  readonly expiresAt: string;
  /** When the PWA should renew (RFC 3339). */
  readonly renewAfter: string;
  /** The renewal ceiling; past it the portal must be used again (RFC 3339). */
  readonly absoluteExpiresAt: string;
  /** The account's retention phase, mirrored from the Session response. */
  readonly readOnly: boolean;
  /** The account's display name, when the server supplied one. */
  readonly displayName?: string;
}

/** What persistence holds: one scheme or the other, never a blend. */
export type PersistedSession = StoredBasicSession | StoredBearerSession;

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

/** Reads the persisted session, or undefined when there is none. */
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

    if (record.kind === "bearer") return readBearer(record);
    /*
     * Anything else is a Basic record. `kind` is checked positively rather
     * than by exclusion so a FUTURE scheme written by a newer build (a tab
     * left open across a deploy) is discarded rather than misread as a Basic
     * credential with missing fields — the read below would reject it anyway,
     * but the intent is worth being explicit about.
     */
    if (record.kind !== undefined && record.kind !== "basic") return undefined;
    return readBasic(record);
  } catch {
    // A corrupted entry is discarded rather than repaired.
    return undefined;
  }
}

function readBasic(record: Record<string, unknown>): StoredBasicSession | undefined {
  const { username, password } = record;
  if (typeof username !== "string" || typeof password !== "string") return undefined;
  if (username === "" || password === "") return undefined;
  return { kind: "basic", username, password };
}

function readBearer(record: Record<string, unknown>): StoredBearerSession | undefined {
  const { username, token, expiresAt, renewAfter, absoluteExpiresAt } = record;
  if (typeof username !== "string" || username === "") return undefined;
  if (typeof token !== "string" || token === "") return undefined;
  if (
    typeof expiresAt !== "string" ||
    typeof renewAfter !== "string" ||
    typeof absoluteExpiresAt !== "string"
  ) {
    return undefined;
  }
  const displayName = record.displayName;
  return {
    kind: "bearer",
    username,
    token,
    expiresAt,
    renewAfter,
    absoluteExpiresAt,
    // A missing flag is `false`, never `undefined`: read-only is a RESTRICTION,
    // and a restriction that arrives as undefined must not be read as "allowed"
    // by one call site and "forbidden" by another.
    readOnly: record.readOnly === true,
    ...(typeof displayName === "string" && displayName !== "" ? { displayName } : {}),
  };
}

/** Persists a Basic credential. */
export function saveSession(
  credentials: BasicCredentials,
  storage: SessionStorageLike = defaultSessionStorage(),
): void {
  writeSession(
    { kind: "basic", username: credentials.username, password: credentials.password },
    storage,
  );
}

/** Persists a delegated session (contract §3.4). */
export function saveBearerSession(
  session: StoredBearerSession,
  storage: SessionStorageLike = defaultSessionStorage(),
): void {
  writeSession(session, storage);
}

function writeSession(session: PersistedSession, storage: SessionStorageLike): void {
  try {
    storage.setItem(STORAGE_KEY, JSON.stringify(session));
  } catch {
    // Storage full or blocked: the user stays signed in for this page view
    // only, which is strictly better than failing the sign-in.
  }
}

/** Erases the persisted session. This is what sign-out does. */
export function clearSession(storage: SessionStorageLike = defaultSessionStorage()): void {
  try {
    storage.removeItem(STORAGE_KEY);
  } catch {
    // Nothing useful to do; the value is tab-scoped and dies with the tab.
  }
}

/**
 * True when a stored bearer session is past its renewal ceiling and cannot be
 * revived by any number of renewals (contract §3.4).
 *
 * An unparseable timestamp counts as expired. That is the safe direction: the
 * cost of being wrong is one trip back through the portal, while the cost of
 * the opposite mistake is an app that keeps retrying a dead credential.
 */
export function isBeyondAbsoluteLifetime(
  session: StoredBearerSession,
  now: number = Date.now(),
): boolean {
  const at = Date.parse(session.absoluteExpiresAt);
  return Number.isNaN(at) || at <= now;
}

/** True when a stored bearer session has passed its `renewAfter` deadline. */
export function isDueForRenewal(
  session: StoredBearerSession,
  now: number = Date.now(),
): boolean {
  const at = Date.parse(session.renewAfter);
  // An unparseable deadline renews NOW rather than never: a renewal that was
  // not needed costs one request, while a renewal that never fires costs the
  // session.
  return Number.isNaN(at) || at <= now;
}

/**
 * Whether the account this session grants is in its read-only retention phase
 * (contract §2.4, §3.4).
 *
 * # The seam, named
 *
 * This is the ONE place the PWA learns the fact before the JMAP Session
 * arrives, and it reads it from the exchange response the server already
 * sends. `false` is the default in every direction: a Basic session has no
 * such phase, a stored record without the flag is not read-only, and a server
 * that never sets it (M1 has not landed, so nothing does yet) behaves exactly
 * as today.
 *
 * When M1 wires its `AccountStatusSource`, the flag starts arriving with no
 * change here — and the consumers that must hide compose read it through this
 * function rather than reaching into the union.
 */
export function isReadOnlySession(session: PersistedSession | undefined): boolean {
  return session?.kind === "bearer" && session.readOnly;
}
