/**
 * A minimal in-memory IndexedDB, written here rather than installed.
 *
 * # Why this exists at all
 *
 * The offline cache is the one part of the PWA whose bugs are invisible until a
 * user is offline — exactly the moment they cannot report anything useful. It
 * therefore needs real tests, and jsdom ships no IndexedDB. The obvious answer
 * (`fake-indexeddb`) is a new npm dependency, which this epic is not allowed to
 * add, and would be a general-purpose implementation for a cache that uses
 * `get`, `put`, `delete`, `getAll`, `count`, `clear` and one cursor.
 *
 * # What it does and does not model
 *
 * MODELLED, because the cache depends on it:
 *
 *   - object stores with a `keyPath`, including compound key paths for the
 *     header index;
 *   - indexes, including compound (`["mailboxId", "receivedAt"]`) ones, with
 *     `openCursor` in both directions and `IDBKeyRange.bound`;
 *   - transactions that COMPLETE asynchronously, so `withTransaction`'s
 *     completion-not-request contract is genuinely exercised;
 *   - `onupgradeneeded` with a real `oldVersion`, so the migration chain runs;
 *   - injectable failure (`failNextOpen`, `failNextTransaction`), because the
 *     whole design of the cache layer is "a broken store degrades to
 *     online-only" and a suite that never breaks the store never checks that.
 *
 * NOT modelled, deliberately: key ordering across mixed types, `IDBKeyRange`
 * variants beyond `bound`/`only`, versionchange events, `getAllKeys`, blob
 * values (the cache stores none — see the attachment note in `cache.ts`), and
 * structured-clone semantics. Values are deep-copied through a JSON round trip,
 * which is exactly the fidelity the cache needs (everything it stores is JSON)
 * and which usefully catches a caller that tries to store something else.
 *
 * A shim that pretended to be complete would be worse than this one: the point
 * is that its limits are written down, so a test that needs something outside
 * them fails loudly rather than passing against a fiction.
 */

type Key = string | number | readonly (string | number)[];

/** Deep copy with the same JSON-only fidelity the real store gives our data. */
function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

/** Compares two keys, scalar or compound, the way IndexedDB orders them. */
function compareKeys(a: Key, b: Key): number {
  const left = Array.isArray(a) ? a : [a];
  const right = Array.isArray(b) ? b : [b];
  const length = Math.max(left.length, right.length);
  for (let index = 0; index < length; index += 1) {
    const l = left[index];
    const r = right[index];
    if (l === undefined) return -1;
    if (r === undefined) return 1;
    if (l < r) return -1;
    if (l > r) return 1;
  }
  return 0;
}

function keyToString(key: Key): string {
  return JSON.stringify(Array.isArray(key) ? key : [key]);
}

/** Reads a (possibly compound) key path out of a record. */
function extractKey(value: unknown, keyPath: string | readonly string[]): Key | undefined {
  const read = (path: string): string | number | undefined => {
    const found = (value as Record<string, unknown>)[path];
    return typeof found === "string" || typeof found === "number" ? found : undefined;
  };
  if (typeof keyPath === "string") return read(keyPath);
  const parts = keyPath.map(read);
  return parts.some((part) => part === undefined)
    ? undefined
    : (parts as (string | number)[]);
}

/*
 * Mirrors `IDBRequest<T>`, whose type parameter likewise appears only on
 * `result` — that IS the shape being modelled, and the callers below (`get`,
 * `getAll`, `count`, the cursor) each depend on it to type what they resolve
 * to. The rule's usual advice, "replace it with the concrete type", would mean
 * four near-identical request classes.
 */
// eslint-disable-next-line @typescript-eslint/no-unnecessary-type-parameters
class FakeRequest<T> {
  public onsuccess: (() => void) | null = null;
  public onerror: (() => void) | null = null;
  public result!: T;
  public error: Error | null = null;
}

interface Range {
  readonly lower?: Key;
  readonly upper?: Key;
  readonly lowerOpen: boolean;
  readonly upperOpen: boolean;
}

function inRange(key: Key, range: Range | null): boolean {
  if (range === null) return true;
  if (range.lower !== undefined) {
    const cmp = compareKeys(key, range.lower);
    if (cmp < 0 || (cmp === 0 && range.lowerOpen)) return false;
  }
  if (range.upper !== undefined) {
    const cmp = compareKeys(key, range.upper);
    if (cmp > 0 || (cmp === 0 && range.upperOpen)) return false;
  }
  return true;
}

/** The `IDBKeyRange` constructors the cache uses. */
export const FakeKeyRange = {
  bound(lower: Key, upper: Key, lowerOpen = false, upperOpen = false): Range {
    return { lower, upper, lowerOpen, upperOpen };
  },
  only(key: Key): Range {
    return { lower: key, upper: key, lowerOpen: false, upperOpen: false };
  },
};

interface StoreDefinition {
  readonly keyPath: string;
  readonly indexes: Map<string, string | readonly string[]>;
  readonly rows: Map<string, unknown>;
}

class FakeCursor {
  constructor(
    public readonly value: unknown,
    private readonly advance: () => void,
  ) {}
  continue(): void {
    this.advance();
  }
}

class FakeIndex {
  constructor(
    private readonly store: StoreDefinition,
    private readonly keyPath: string | readonly string[],
    private readonly tx: FakeTransaction,
  ) {}

  openCursor(
    query: Range | null,
    direction: IDBCursorDirection = "next",
  ): FakeRequest<FakeCursor | null> {
    const rows = [...this.store.rows.values()]
      .map((value) => ({ value, key: extractKey(value, this.keyPath) }))
      .filter(
        (entry): entry is { value: unknown; key: Key } =>
          entry.key !== undefined && inRange(entry.key, query),
      )
      .sort((a, b) => compareKeys(a.key, b.key));
    if (direction === "prev") rows.reverse();
    return this.tx.cursorRequest(rows.map((entry) => entry.value));
  }
}

class FakeObjectStore {
  constructor(
    private readonly definition: StoreDefinition,
    private readonly tx: FakeTransaction,
  ) {}

  index(name: string): FakeIndex {
    const path = this.definition.indexes.get(name);
    if (path === undefined) throw new Error(`no such index: ${name}`);
    return new FakeIndex(this.definition, path, this.tx);
  }

  get(key: Key): FakeRequest<unknown> {
    return this.tx.settle(() => {
      const found = this.definition.rows.get(keyToString(key));
      return found === undefined ? undefined : clone(found);
    });
  }

  getAll(): FakeRequest<unknown[]> {
    return this.tx.settle(() => [...this.definition.rows.values()].map(clone));
  }

  put(value: unknown): FakeRequest<Key> {
    return this.tx.settle(() => {
      this.tx.assertWritable();
      const key = extractKey(value, this.definition.keyPath);
      if (key === undefined) throw new Error("value has no key at its keyPath");
      this.definition.rows.set(keyToString(key), clone(value));
      return key;
    });
  }

  delete(key: Key): FakeRequest<undefined> {
    return this.tx.settle(() => {
      this.tx.assertWritable();
      this.definition.rows.delete(keyToString(key));
      return undefined;
    });
  }

  clear(): FakeRequest<undefined> {
    return this.tx.settle(() => {
      this.tx.assertWritable();
      this.definition.rows.clear();
      return undefined;
    });
  }

  count(): FakeRequest<number> {
    return this.tx.settle(() => this.definition.rows.size);
  }

  openCursor(
    query: Range | null,
    direction: IDBCursorDirection = "next",
  ): FakeRequest<FakeCursor | null> {
    const rows = [...this.definition.rows.entries()]
      .filter(([key]) => inRange(JSON.parse(key) as Key, query))
      .sort(([a], [b]) => compareKeys(JSON.parse(a) as Key, JSON.parse(b) as Key))
      .map(([, value]) => value);
    if (direction === "prev") rows.reverse();
    return this.tx.cursorRequest(rows);
  }

  createIndex(name: string, keyPath: string | readonly string[]): void {
    this.definition.indexes.set(name, keyPath);
  }
}

class FakeTransaction {
  public oncomplete: (() => void) | null = null;
  public onerror: (() => void) | null = null;
  public onabort: (() => void) | null = null;

  private pending = 0;
  private finished = false;
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor(
    private readonly db: FakeDatabase,
    private readonly stores: readonly string[],
    public readonly mode: IDBTransactionMode,
  ) {}

  assertWritable(): void {
    if (this.mode === "readonly") throw new Error("read-only transaction");
  }

  objectStore(name: string): FakeObjectStore {
    if (!this.stores.includes(name)) {
      throw new Error(`store ${name} is not in this transaction's scope`);
    }
    const definition = this.db.stores.get(name);
    if (definition === undefined) throw new Error(`no such store: ${name}`);
    return new FakeObjectStore(definition, this);
  }

  /** Cancels a scheduled completion: a new request means the tx is still busy. */
  private busy(): void {
    if (this.timer === undefined) return;
    clearTimeout(this.timer);
    this.timer = undefined;
  }

  /** Queues one request's work, resolving it on a microtask. */
  settle<T>(work: () => T): FakeRequest<T> {
    const req = new FakeRequest<T>();
    this.pending += 1;
    this.busy();
    queueMicrotask(() => {
      if (this.finished) return;
      try {
        req.result = work();
        this.pending -= 1;
        req.onsuccess?.();
      } catch (error) {
        this.pending -= 1;
        req.error = error instanceof Error ? error : new Error(String(error));
        req.onerror?.();
      }
      this.maybeComplete();
    });
    return req;
  }

  cursorRequest(rows: readonly unknown[]): FakeRequest<FakeCursor | null> {
    const req = new FakeRequest<FakeCursor | null>();
    let index = 0;
    const step = (): void => {
      this.pending += 1;
      this.busy();
      queueMicrotask(() => {
        if (this.finished) return;
        this.pending -= 1;
        const row = rows[index];
        if (row === undefined) {
          req.result = null;
        } else {
          index += 1;
          req.result = new FakeCursor(clone(row), step);
        }
        req.onsuccess?.();
        this.maybeComplete();
      });
    };
    step();
    return req;
  }

  abort(): void {
    if (this.finished) return;
    this.finished = true;
    queueMicrotask(() => {
      this.onabort?.();
    });
  }

  /**
   * Completes once no request is outstanding and the caller has stopped issuing
   * them.
   *
   * # Why a macrotask rather than a microtask
   *
   * The real API keeps a transaction alive as long as its success callbacks
   * keep queueing new requests, and completes when the microtask queue drains
   * with nothing outstanding. A shim cannot observe that directly, and the
   * obvious approximation — complete on the next microtask with `pending === 0`
   * — races the CALLER: `withTransaction` runs an `async` function, so any
   * `await` before its first `put` lets the transaction complete underneath it.
   * That is a bug in the shim, not in the code under test, and it showed up in
   * development as a phantom "the write did not commit".
   *
   * A `setTimeout(0)` is strictly after every microtask an `await` chain can
   * queue, so a caller's asynchronous work always gets to issue its requests.
   * The timer is reset by each new request, which preserves the real property
   * that matters here: completion happens strictly after the last request, and
   * `withTransaction`'s "resolve on complete, not on request" contract is
   * genuinely exercised.
   */
  private maybeComplete(): void {
    if (this.finished || this.pending > 0) return;
    this.busy();
    this.timer = setTimeout(() => {
      this.timer = undefined;
      if (this.finished || this.pending > 0) return;
      this.finished = true;
      this.oncomplete?.();
    }, 0);
  }

  /** Starts the completion clock for a transaction nobody issued a request on. */
  arm(): void {
    this.maybeComplete();
  }
}

class FakeDatabase {
  public readonly stores = new Map<string, StoreDefinition>();

  constructor(
    public readonly name: string,
    public version: number,
    private readonly owner: FakeIndexedDB,
  ) {}

  createObjectStore(name: string, options: { keyPath: string }): FakeObjectStore {
    const definition: StoreDefinition = {
      keyPath: options.keyPath,
      indexes: new Map(),
      rows: new Map(),
    };
    this.stores.set(name, definition);
    // The upgrade transaction is synthetic; a store created here needs a
    // transaction object to hand back so `createIndex` can be called on it.
    const tx = new FakeTransaction(this, [name], "versionchange");
    return new FakeObjectStore(definition, tx);
  }

  transaction(stores: readonly string[], mode: IDBTransactionMode): FakeTransaction {
    if (this.owner.failNextTransaction) {
      this.owner.failNextTransaction = false;
      throw new Error("simulated transaction failure");
    }
    const tx = new FakeTransaction(this, stores, mode);
    // Arm it so an empty transaction still completes, as the real API does.
    queueMicrotask(() => {
      tx.arm();
    });
    return tx;
  }

  close(): void {
    // Nothing to release in memory; present because callers may call it.
  }
}

/** The factory. Hand it to `openDatabase` in place of `globalThis.indexedDB`. */
export class FakeIndexedDB {
  private readonly databases = new Map<string, FakeDatabase>();

  /** Set to make the next `open` fail — the "IndexedDB is unavailable" case. */
  public failNextOpen = false;
  /** Set to make the next `transaction()` throw — the "quota died" case. */
  public failNextTransaction = false;

  open(name: string, version: number): {
    onupgradeneeded: ((event: { oldVersion: number }) => void) | null;
    onsuccess: (() => void) | null;
    onerror: (() => void) | null;
    onblocked: (() => void) | null;
    result: FakeDatabase;
    transaction: { abort: () => void } | null;
  } {
    const req = {
      onupgradeneeded: null as ((event: { oldVersion: number }) => void) | null,
      onsuccess: null as (() => void) | null,
      onerror: null as (() => void) | null,
      onblocked: null as (() => void) | null,
      result: undefined as unknown as FakeDatabase,
      transaction: { abort: (): void => undefined } as { abort: () => void } | null,
    };

    if (this.failNextOpen) {
      this.failNextOpen = false;
      queueMicrotask(() => {
        req.onerror?.();
      });
      return req;
    }

    queueMicrotask(() => {
      const existing = this.databases.get(name);
      const oldVersion = existing?.version ?? 0;
      const db = existing ?? new FakeDatabase(name, version, this);
      db.version = version;
      this.databases.set(name, db);
      req.result = db;
      if (oldVersion < version) {
        req.onupgradeneeded?.({ oldVersion });
      }
      req.onsuccess?.();
    });

    return req;
  }

  deleteDatabase(name: string): {
    onsuccess: (() => void) | null;
    onerror: (() => void) | null;
    onblocked: (() => void) | null;
  } {
    const req = {
      onsuccess: null as (() => void) | null,
      onerror: null as (() => void) | null,
      onblocked: null as (() => void) | null,
    };
    queueMicrotask(() => {
      this.databases.delete(name);
      req.onsuccess?.();
    });
    return req;
  }
}

/**
 * Installs the shim's `IDBKeyRange` on the global, which the cache uses by
 * name. Returns a function that removes it again.
 */
export function installKeyRange(): () => void {
  const previous = (globalThis as { IDBKeyRange?: unknown }).IDBKeyRange;
  (globalThis as { IDBKeyRange?: unknown }).IDBKeyRange = FakeKeyRange;
  return () => {
    (globalThis as { IDBKeyRange?: unknown }).IDBKeyRange = previous;
  };
}
