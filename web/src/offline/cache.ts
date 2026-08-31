/**
 * The offline mail cache (L3 E9, decision D-2; canon §2.10).
 *
 * # What Gmail's own offline mode is, and what we copy
 *
 * Gmail offline (/mail/answer/1306849) lets you "read, search, and reply", with
 * a sync-depth setting in days, a separate "download attachments" toggle, and
 * one honest limitation: **attachments are not previewable offline**. Its
 * outgoing mail waits in an "Outbox" folder. That shape is what this cache
 * exists to support — and the ONE thing we diverge on is Google's, not
 * email's: Gmail offline is Chrome-only and requires a bookmark rather than an
 * install, which is ecosystem strategy. D-2, signed, replaces it with standard
 * SW + IndexedDB that works in any browser with the APIs.
 *
 * # What is cached, and what is deliberately not
 *
 *   - **the mailbox list**, so the sidebar is real offline rather than a
 *     spinner;
 *   - **message headers**, {@link HEADER_CAP} most-recent per mailbox — enough
 *     for the list to render and for offline search to have something to match;
 *   - **bodies of messages the user actually OPENED**, capped at
 *     {@link BODY_CAP} by least-recently-read. Not "the last 200 messages'
 *     bodies": pre-fetching bodies would multiply the download of every sync by
 *     the average message size for mail the user may never open, which is a
 *     cost paid on a phone's data plan for a guess.
 *   - **NOT attachments, and NOT the sanitized HTML's external assets.** The
 *     body's own HTML text is cached (it arrives inside `Email/get`, so it is
 *     free); every `blobId` download and every proxied remote image is not.
 *     This is the same limitation Gmail declares, and the UI declares it too —
 *     see `offline.attachments.unavailable` in the string table, shown on the
 *     attachment list when offline.
 *
 * # Scoping by account
 *
 * Every row carries `accountId`. One browser profile can sign into two
 * mailboxes, and a cache that let the second read the first's mail would be the
 * worst bug in this file. Reads filter on it; {@link MailCache.clearOtherAccounts}
 * drops everything belonging to anyone else on sign-in.
 *
 * # Every function here resolves, never rejects
 *
 * See `idb.ts`. A broken or absent store is a cache miss, and a cache miss is a
 * supported state of the entire app.
 */

import type { Email, Mailbox } from "../mail/types";
import {
  eachCursor,
  INDEX_BODIES_BY_READ,
  INDEX_HEADERS_BY_MAILBOX,
  request,
  STORE_BODIES,
  STORE_HEADERS,
  STORE_MAILBOXES,
  withTransaction,
} from "./idb";

/**
 * The DEFAULT number of headers per mailbox — the depth used when no preference
 * is known.
 *
 * 200 matches `SEARCH_WINDOW` in `mail/api.ts` — the size of the window the app
 * fetches anyway — so the common case is "cache exactly what was just
 * displayed" with no extra request. It is also roughly a screen-ful times
 * twenty, which is more scrolling than anyone does offline, and at ~1 kB of
 * JSON per header it costs ~200 kB per mailbox: real, bounded, and far under
 * any browser's quota for a dozen folders.
 *
 * It is no longer the only answer: `Prefs.offlineDepth.headersPerMailbox`
 * (E9b, `store.Prefs.OfflineDepth`) makes the depth a user's choice, because
 * the honest number depends on the device — a phone on a metered connection and
 * a desktop on a fast link want different answers, and the user is the only one
 * who knows which they are on. This constant remains the value the server
 * itself defaults to, so an account that never touched the setting caches
 * exactly what it always did.
 */
export const HEADER_CAP = 200;

/**
 * The DEFAULT number of message bodies kept, evicted least-recently-read first.
 *
 * Bodies are the expensive rows: a message with a long HTML part is tens of
 * kilobytes where a header is one. 100 is chosen to be generous for the actual
 * offline use case (the mail you were reading when the train entered the
 * tunnel, plus a day's worth of what you opened) while keeping the worst case
 * — 100 large HTML messages — in the low tens of megabytes rather than
 * competing with the browser's whole origin quota.
 */
export const BODY_CAP = 100;

/**
 * The depths this cache is currently running at.
 *
 * A value rather than two constants because the cache must answer "how deep am
 * I" identically at write time (trimming) and at read time (the default
 * `limit`), and threading two numbers through both paths is how one of them
 * ends up trimming to a depth the other never reads.
 */
export interface CacheDepth {
  readonly headersPerMailbox: number;
  readonly bodies: number;
}

export const DEFAULT_CACHE_DEPTH: CacheDepth = {
  headersPerMailbox: HEADER_CAP,
  bodies: BODY_CAP,
};

/**
 * The localStorage mirror of the depth.
 *
 * # Why a mirror is REQUIRED here and not merely convenient
 *
 * This is the theme's problem in a harsher form. The theme mirror exists
 * because the value is needed before first paint; this one exists because the
 * value is needed when there may be NO SERVER AT ALL. An offline cold boot —
 * the PWA opened on a train, which is the entire scenario this cache serves —
 * has no session, no `Prefs/get`, and no way to ever learn the user's depth.
 * Without a mirror the cache would silently run at the default in exactly the
 * situation the user configured it for, and a setting that applies only when
 * you do not need it is a setting that does nothing.
 *
 * Prefs remain the source of truth. This is written through whenever prefs
 * load or change, and read only when they are unavailable.
 */
const DEPTH_KEY = "moov.offlineDepth.v1";

/** Reads the mirrored depth. Never throws — an absent or broken one is the default. */
export function loadCacheDepth(storage?: Storage): CacheDepth {
  try {
    const store = storage ?? globalThis.localStorage;
    const raw = store?.getItem(DEPTH_KEY);
    if (raw === null || raw === undefined) return DEFAULT_CACHE_DEPTH;
    const parsed = JSON.parse(raw) as { headersPerMailbox?: unknown; bodies?: unknown };
    return {
      headersPerMailbox:
        typeof parsed.headersPerMailbox === "number" && Number.isInteger(parsed.headersPerMailbox)
          ? parsed.headersPerMailbox
          : HEADER_CAP,
      bodies:
        typeof parsed.bodies === "number" && Number.isInteger(parsed.bodies)
          ? parsed.bodies
          : BODY_CAP,
    };
  } catch {
    return DEFAULT_CACHE_DEPTH;
  }
}

/** Writes the mirrored depth. A blocked storage costs the depth, never an error. */
export function saveCacheDepth(depth: CacheDepth, storage?: Storage): void {
  try {
    const store = storage ?? globalThis.localStorage;
    store?.setItem(DEPTH_KEY, JSON.stringify(depth));
  } catch {
    // Private mode, a full quota, a locked-down browser: the cache runs at the
    // default depth next boot. Never a thrown error on a preference.
  }
}

/** A cached mailbox row. */
interface MailboxRow {
  readonly id: string;
  readonly accountId: string;
  readonly mailbox: Mailbox;
  readonly cachedAt: number;
}

/** A cached header row. `receivedAt` is denormalised for the index. */
interface HeaderRow {
  readonly id: string;
  readonly accountId: string;
  readonly mailboxId: string;
  /** ISO 8601, which sorts lexicographically in the same order it sorts in time. */
  readonly receivedAt: string;
  readonly email: Email;
  readonly cachedAt: number;
}

/** A cached body row. */
interface BodyRow {
  readonly id: string;
  readonly accountId: string;
  readonly email: Email;
  /** Drives the LRU eviction. */
  readonly lastReadAt: number;
}

/** The empty-string sentinel for a missing `receivedAt`, so the index is total. */
const NO_DATE = "";

/** Above every character a date string contains — the upper bound of a range. */
const MAX_KEY = "￿";

/**
 * The cache, bound to one database handle and one account.
 *
 * A class rather than free functions because every method needs the same two
 * values and threading them through fifteen call sites is how one of them ends
 * up reading another account's rows.
 */
export class MailCache {
  /**
   * The depth is a CONSTRUCTOR argument with a default, not a mutable field.
   *
   * A cache handed a new depth is a new `MailCache`, which means the trimming a
   * write does and the limit a read applies can never disagree within one
   * instance. The caller that owns the prefs subscription rebuilds the object
   * when the depth changes; that is one line there, versus a mutable field
   * every method would have to re-read at exactly the right moment.
   */
  constructor(
    private readonly db: IDBDatabase,
    private readonly accountId: string,
    private readonly depth: CacheDepth = DEFAULT_CACHE_DEPTH,
  ) {}

  /** The depths this instance trims and reads at. */
  get cacheDepth(): CacheDepth {
    return this.depth;
  }

  // --- mailboxes -----------------------------------------------------------

  /** Write-through: called wherever the app fetches the mailbox list. */
  async putMailboxes(mailboxes: readonly Mailbox[]): Promise<void> {
    const now = Date.now();
    await withTransaction(this.db, [STORE_MAILBOXES], "readwrite", async (tx) => {
      const store = tx.objectStore(STORE_MAILBOXES);
      /*
       * The list is REPLACED rather than merged: a folder deleted in another
       * client must disappear from our sidebar too, and a merge would keep it
       * forever. Only this account's rows are cleared — see the loop, which
       * deletes by id rather than calling `clear()`.
       */
      const existing = (await request(store.getAll())) as readonly MailboxRow[];
      for (const row of existing) {
        if (row.accountId === this.accountId) await request(store.delete(row.id));
      }
      for (const mailbox of mailboxes) {
        const row: MailboxRow = {
          id: mailbox.id,
          accountId: this.accountId,
          mailbox,
          cachedAt: now,
        };
        await request(store.put(row));
      }
    });
  }

  /** The cached mailbox list, or an empty array when there is none. */
  async mailboxes(): Promise<readonly Mailbox[]> {
    const rows = await withTransaction(
      this.db,
      [STORE_MAILBOXES],
      "readonly",
      async (tx) =>
        (await request(tx.objectStore(STORE_MAILBOXES).getAll())) as readonly MailboxRow[],
    );
    if (rows === undefined) return [];
    return rows
      .filter((row) => row.accountId === this.accountId)
      .map((row) => row.mailbox);
  }

  // --- headers -------------------------------------------------------------

  /**
   * Write-through for a fetched window.
   *
   * Called with the list the screen just rendered, so browsing IS the sync —
   * which is what makes the cache warm without a background job, a schedule, or
   * a second code path that could disagree with what the user saw.
   *
   * After writing, the mailbox is trimmed to the configured header depth.
   */
  async putHeaders(mailboxId: string, emails: readonly Email[]): Promise<void> {
    const now = Date.now();
    await withTransaction(this.db, [STORE_HEADERS], "readwrite", async (tx) => {
      const store = tx.objectStore(STORE_HEADERS);
      for (const email of emails) {
        const row: HeaderRow = {
          id: email.id,
          accountId: this.accountId,
          mailboxId,
          receivedAt: email.receivedAt ?? NO_DATE,
          email,
          cachedAt: now,
        };
        await request(store.put(row));
      }
      await this.trimMailbox(store, mailboxId);
    });
  }

  /**
   * Drops everything past the configured header depth in one mailbox, oldest
   * first.
   *
   * Walks the `[mailboxId, receivedAt]` index BACKWARDS (newest first) and
   * deletes from the cap onward, which touches only the rows being evicted
   * rather than materialising the whole mailbox to sort it.
   */
  private async trimMailbox(store: IDBObjectStore, mailboxId: string): Promise<void> {
    const index = store.index(INDEX_HEADERS_BY_MAILBOX);
    // The full key range for one mailbox: from its earliest possible date to
    // the latest.
    const range = IDBKeyRange.bound([mailboxId, NO_DATE], [mailboxId, MAX_KEY]);

    let seen = 0;
    const doomed: string[] = [];
    await eachCursor(index, range, "prev", (value) => {
      const row = value as HeaderRow;
      seen += 1;
      if (seen > this.depth.headersPerMailbox && row.accountId === this.accountId) {
        doomed.push(row.id);
      }
      return true;
    });
    for (const id of doomed) await request(store.delete(id));
  }

  /**
   * The cached headers of one mailbox, newest first, at most the configured
   * header depth.
   *
   * The default is resolved in the BODY rather than in the parameter list,
   * because a default initializer cannot see `this` — and taking the depth from
   * the instance is the whole point: a read that defaulted to the module
   * constant would return 200 rows out of a cache the user configured to hold
   * 500.
   */
  async headers(mailboxId: string, limit?: number): Promise<readonly Email[]> {
    const cap = limit ?? this.depth.headersPerMailbox;
    const rows = await withTransaction(
      this.db,
      [STORE_HEADERS],
      "readonly",
      async (tx) => {
        const index = tx.objectStore(STORE_HEADERS).index(INDEX_HEADERS_BY_MAILBOX);
        const range = IDBKeyRange.bound([mailboxId, NO_DATE], [mailboxId, MAX_KEY]);
        const found: HeaderRow[] = [];
        await eachCursor(index, range, "prev", (value) => {
          const row = value as HeaderRow;
          if (row.accountId === this.accountId) found.push(row);
          return found.length < cap;
        });
        return found;
      },
    );
    return rows === undefined ? [] : rows.map((row) => row.email);
  }

  /** Every cached header for this account — what offline search runs over. */
  async allHeaders(): Promise<readonly Email[]> {
    const rows = await withTransaction(
      this.db,
      [STORE_HEADERS],
      "readonly",
      async (tx) =>
        (await request(tx.objectStore(STORE_HEADERS).getAll())) as readonly HeaderRow[],
    );
    if (rows === undefined) return [];
    return rows
      .filter((row) => row.accountId === this.accountId)
      .map((row) => row.email);
  }

  // --- bodies --------------------------------------------------------------

  /**
   * Caches a message the user OPENED, and refreshes its LRU stamp.
   *
   * The stamp is written on every open, not only the first, which is what makes
   * the eviction least-recently-READ rather than least-recently-fetched: the
   * message you keep coming back to survives, which is the one the cache is for.
   */
  async putBody(email: Email): Promise<void> {
    await withTransaction(this.db, [STORE_BODIES], "readwrite", async (tx) => {
      const store = tx.objectStore(STORE_BODIES);
      const row: BodyRow = {
        id: email.id,
        accountId: this.accountId,
        email,
        lastReadAt: Date.now(),
      };
      await request(store.put(row));
      await this.evictBodies(store);
    });
  }

  /** Drops the least-recently-read bodies past the configured body depth. */
  private async evictBodies(store: IDBObjectStore): Promise<void> {
    const total = await request(store.count());
    if (total <= this.depth.bodies) return;

    const excess = total - this.depth.bodies;
    const doomed: string[] = [];
    // Forward over `lastReadAt` is oldest-read first, which is the eviction
    // order by definition.
    await eachCursor(store.index(INDEX_BODIES_BY_READ), null, "next", (value) => {
      doomed.push((value as BodyRow).id);
      return doomed.length < excess;
    });
    for (const id of doomed) await request(store.delete(id));
  }

  /**
   * A cached body, if there is one. Does NOT update the LRU stamp — a read that
   * is only a cache probe should not keep a message alive, and the open path
   * calls {@link MailCache.putBody} anyway.
   */
  async body(id: string): Promise<Email | undefined> {
    const row = await withTransaction(this.db, [STORE_BODIES], "readonly", async (tx) =>
      (await request(tx.objectStore(STORE_BODIES).get(id))) as BodyRow | undefined,
    );
    if (row?.accountId !== this.accountId) return undefined;
    return row.email;
  }

  /** Every cached body for this account — offline search's full-text corpus. */
  async allBodies(): Promise<readonly Email[]> {
    const rows = await withTransaction(this.db, [STORE_BODIES], "readonly", async (tx) =>
      (await request(tx.objectStore(STORE_BODIES).getAll())) as readonly BodyRow[],
    );
    if (rows === undefined) return [];
    return rows.filter((row) => row.accountId === this.accountId).map((row) => row.email);
  }

  // --- housekeeping --------------------------------------------------------

  /**
   * Removes every row belonging to a DIFFERENT account.
   *
   * Called once when a session starts. The alternative — wiping everything on
   * sign-in — would throw away the cache of the account signing back in, which
   * is precisely the one they want warm.
   */
  async clearOtherAccounts(): Promise<void> {
    await withTransaction(
      this.db,
      [STORE_MAILBOXES, STORE_HEADERS, STORE_BODIES],
      "readwrite",
      async (tx) => {
        for (const name of [STORE_MAILBOXES, STORE_HEADERS, STORE_BODIES]) {
          const store = tx.objectStore(name);
          const rows = (await request(store.getAll())) as readonly {
            id: string;
            accountId: string;
          }[];
          for (const row of rows) {
            if (row.accountId !== this.accountId) await request(store.delete(row.id));
          }
        }
      },
    );
  }

  /** How much is cached, for the settings screen's honest disclosure. */
  async stats(): Promise<{ readonly headers: number; readonly bodies: number }> {
    const headers = await this.allHeaders();
    const bodies = await this.allBodies();
    return { headers: headers.length, bodies: bodies.length };
  }
}
