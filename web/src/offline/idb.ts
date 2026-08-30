/**
 * A hand-rolled promise layer over IndexedDB (L3 E9, decision D-2).
 *
 * # Why not `idb`
 *
 * The brief forbids new npm dependencies, and that constraint happens to be
 * right here: `idb` is a general-purpose wrapper for a general-purpose API, and
 * this app uses four object stores with `get`/`put`/`delete`/`getAll` and one
 * index. That is the ~150 lines below. What a dependency would buy is the parts
 * we do not use, plus a supply-chain surface on the one module that holds a
 * copy of the user's mail.
 *
 * # The rule every function here obeys: failure is silent and total
 *
 * IndexedDB is unavailable or broken in more situations than any other storage
 * API: Safari private browsing has historically thrown on `open`, Firefox with
 * cookies blocked throws a `SecurityError`, a full quota aborts transactions
 * mid-write, and a browser can evict the whole database between two calls. None
 * of that may ever break the app — Moov works online without a single byte of
 * this, so the cache is an optimisation and an optimisation that throws is a
 * bug.
 *
 * Therefore: every entry point resolves rather than rejects, and a failed read
 * is indistinguishable from a cache miss. The one thing that is NOT silent is
 * the user-visible consequence, which the UI states (the offline banner says
 * what is and is not available) rather than pretending the cache is complete.
 *
 * # Versioning
 *
 * {@link SCHEMA_VERSION} is bumped whenever a store or index changes, and
 * {@link upgradeDatabase} is written as a fall-through switch on the OLD
 * version so upgrading from any earlier version applies every step in order.
 * `onupgradeneeded` gives us `oldVersion`, which is what makes that possible;
 * the alternative — "create everything if missing" — silently skips migrations
 * that need to touch existing rows.
 */

/** The database name. One per origin; the account id scopes the rows inside it. */
export const DB_NAME = "moov-offline";

/**
 * The schema version.
 *
 * v1 (E9b): mailboxes, headers, bodies, outbox.
 */
export const SCHEMA_VERSION = 1;

/** The object stores. Named constants because a typo'd string is a silent miss. */
export const STORE_MAILBOXES = "mailboxes";
export const STORE_HEADERS = "headers";
export const STORE_BODIES = "bodies";
export const STORE_OUTBOX = "outbox";

/** The index that answers "the headers of mailbox X, newest first". */
export const INDEX_HEADERS_BY_MAILBOX = "byMailbox";
/** The index that answers "the least recently read bodies" for the LRU sweep. */
export const INDEX_BODIES_BY_READ = "byLastReadAt";

/**
 * Applies the schema for a version transition.
 *
 * Exported and pure-ish (it touches only the database handed to it) so the
 * upgrade path is testable against the in-memory shim without opening a real
 * database — the alternative is discovering a broken migration in production,
 * where it presents as an app that boots with an empty cache and no error.
 */
export function upgradeDatabase(db: IDBDatabase, oldVersion: number): void {
  /*
   * A `switch` on the OLD version, written to FALL THROUGH: upgrading from v0
   * must apply v1, then v2, then v3 in order, and a `case` that returns would
   * silently skip every later step. There is only one case today, so nothing
   * falls through yet — the shape is here so that adding v2 is one `case` and
   * not a restructuring.
   */
  switch (oldVersion) {
    case 0: {
      // Keyed by mailbox id; one row per mailbox, holding the JMAP object.
      db.createObjectStore(STORE_MAILBOXES, { keyPath: "id" });

      /*
       * Headers are keyed by MESSAGE id rather than by [mailbox, message]:
       * a message has exactly one mailbox on this server (types.ts: "exactly
       * one key"), and a compound key would make "update this message's flags"
       * require knowing where it lives, which the SSE refresh path does not
       * always have to hand.
       */
      const headers = db.createObjectStore(STORE_HEADERS, { keyPath: "id" });
      // `[mailboxId, receivedAt]` so a range query over one mailbox comes back
      // in date order without sorting the whole store in memory.
      headers.createIndex(INDEX_HEADERS_BY_MAILBOX, ["mailboxId", "receivedAt"]);

      const bodies = db.createObjectStore(STORE_BODIES, { keyPath: "id" });
      // The LRU's cursor: oldest `lastReadAt` first.
      bodies.createIndex(INDEX_BODIES_BY_READ, "lastReadAt");

      // The outbox is keyed by our own generated id, not by anything the
      // server assigns — the whole point is that it exists before the server
      // has ever seen it.
      db.createObjectStore(STORE_OUTBOX, { keyPath: "id" });
    }
  }
}

/** The `indexedDB` factory, injectable so tests can supply the shim. */
export type IdbFactory = Pick<IDBFactory, "open">;

/** Resolves the factory this environment offers, or undefined where there is none. */
export function defaultFactory(): IdbFactory | undefined {
  try {
    // jsdom has no indexedDB, and neither does a locked-down browser. Reading
    // the property can itself throw in some privacy modes, hence the try.
    return typeof indexedDB === "undefined" ? undefined : indexedDB;
  } catch {
    return undefined;
  }
}

/**
 * Opens the database, applying any pending upgrade.
 *
 * Resolves to `undefined` — never rejects — when IndexedDB is missing, blocked,
 * or the open fails for any reason. Every caller treats that as "no cache",
 * which is a supported mode of the whole app.
 *
 * `onblocked` resolves undefined rather than hanging: it fires when ANOTHER tab
 * holds the database open at an older version, and a promise that never settles
 * would freeze whatever awaited it. Degrading that tab to online-only until it
 * reloads is the correct trade.
 */
export function openDatabase(
  factory: IdbFactory | undefined = defaultFactory(),
  name: string = DB_NAME,
  version: number = SCHEMA_VERSION,
): Promise<IDBDatabase | undefined> {
  if (factory === undefined) return Promise.resolve(undefined);

  return new Promise((resolve) => {
    let request: IDBOpenDBRequest;
    try {
      request = factory.open(name, version);
    } catch {
      resolve(undefined);
      return;
    }

    request.onupgradeneeded = (event) => {
      try {
        upgradeDatabase(request.result, event.oldVersion);
      } catch {
        // A failed migration must not leave a half-built schema in use. The
        // transaction aborts, `onerror` fires, and we run without a cache.
        request.transaction?.abort();
      }
    };
    request.onsuccess = () => {
      resolve(request.result);
    };
    request.onerror = () => {
      resolve(undefined);
    };
    request.onblocked = () => {
      resolve(undefined);
    };
  });
}

/**
 * Runs `work` inside one transaction and resolves when the transaction
 * COMPLETES — not when the last request succeeds.
 *
 * That distinction is the reason this helper exists. A write whose request
 * fired successfully can still be lost if the transaction later aborts (a quota
 * error, a browser eviction), and code that awaits the request rather than the
 * transaction reports a save that did not happen. The outbox in particular
 * cannot afford that: it is the only copy of a message the user believes they
 * sent.
 *
 * Resolves `undefined` on any failure, per the module's rule.
 */
export function withTransaction<T>(
  db: IDBDatabase,
  stores: readonly string[],
  mode: IDBTransactionMode,
  work: (tx: IDBTransaction) => Promise<T> | T,
): Promise<T | undefined> {
  return new Promise((resolve) => {
    let tx: IDBTransaction;
    try {
      tx = db.transaction([...stores], mode);
    } catch {
      resolve(undefined);
      return;
    }

    let value: T | undefined;
    let failed = false;

    tx.oncomplete = () => {
      resolve(failed ? undefined : value);
    };
    tx.onerror = () => {
      resolve(undefined);
    };
    tx.onabort = () => {
      resolve(undefined);
    };

    void (async () => {
      try {
        value = await work(tx);
      } catch {
        failed = true;
        try {
          tx.abort();
        } catch {
          // Already finished; `oncomplete`/`onabort` will settle the promise.
        }
      }
    })();
  });
}

/** Promisifies one IDBRequest. Rejects, so `withTransaction` can abort. */
export function request<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => {
      resolve(req.result);
    };
    req.onerror = () => {
      reject(req.error ?? new Error("IndexedDB request failed"));
    };
  });
}

/**
 * Walks a cursor, calling `visit` for each value until it returns false.
 *
 * Written as a cursor rather than `getAll` for the two callers that need it:
 * the LRU sweep, which must stop after N rows rather than materialise every
 * cached body, and the per-mailbox header read, which takes the newest N out of
 * a store that may hold thousands.
 */
export function eachCursor(
  source: IDBObjectStore | IDBIndex,
  query: IDBKeyRange | null,
  direction: IDBCursorDirection,
  /*
   * The row is `unknown` rather than a generic parameter: a generic used once
   * is just an assertion with extra syntax, and it would let a caller claim a
   * row type IndexedDB never verified. Callers narrow it themselves, which puts
   * the (unavoidable) cast where the row's shape is actually known.
   */
  visit: (value: unknown) => boolean,
): Promise<void> {
  return new Promise((resolve, reject) => {
    const req = source.openCursor(query, direction);
    req.onsuccess = () => {
      const cursor = req.result;
      if (cursor === null) {
        resolve();
        return;
      }
      let keepGoing: boolean;
      try {
        keepGoing = visit(cursor.value);
      } catch (error) {
        reject(error instanceof Error ? error : new Error(String(error)));
        return;
      }
      if (!keepGoing) {
        resolve();
        return;
      }
      cursor.continue();
    };
    req.onerror = () => {
      reject(req.error ?? new Error("IndexedDB cursor failed"));
    };
  });
}

/** Deletes the whole database — the sign-out path. Never throws. */
export function deleteDatabase(
  factory: (IdbFactory & Partial<Pick<IDBFactory, "deleteDatabase">>) | undefined =
    defaultFactory(),
  name: string = DB_NAME,
): Promise<void> {
  return new Promise((resolve) => {
    if (factory?.deleteDatabase === undefined) {
      resolve();
      return;
    }
    let req: IDBOpenDBRequest;
    try {
      req = factory.deleteDatabase(name);
    } catch {
      resolve();
      return;
    }
    req.onsuccess = () => {
      resolve();
    };
    req.onerror = () => {
      resolve();
    };
    // Another tab is holding it open. Nothing to do but carry on: the rows are
    // scoped by account id, so a stale database cannot show one user another's
    // mail.
    req.onblocked = () => {
      resolve();
    };
  });
}
