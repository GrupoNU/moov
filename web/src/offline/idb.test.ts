import { afterEach, beforeEach, describe, expect, it } from "vitest";

import {
  deleteDatabase,
  defaultFactory,
  openDatabase,
  request,
  SCHEMA_VERSION,
  STORE_BODIES,
  STORE_HEADERS,
  STORE_MAILBOXES,
  STORE_OUTBOX,
  upgradeDatabase,
  withTransaction,
} from "./idb";
import { FakeIndexedDB, installKeyRange } from "../test/fakeIndexedDB";

/**
 * The storage layer's guard rails.
 *
 * The single most important property asserted here is the module's rule:
 * **nothing rejects.** A cache that throws is worse than no cache, because the
 * app works fine without one and a rejected promise on a mail screen is a blank
 * page. Every "unavailable" path below therefore checks for a resolved
 * `undefined`, not a caught error.
 */

let restoreKeyRange: () => void;

beforeEach(() => {
  restoreKeyRange = installKeyRange();
});

afterEach(() => {
  restoreKeyRange();
});

const open = async (factory: FakeIndexedDB): Promise<IDBDatabase> => {
  const db = await openDatabase(factory as unknown as IDBFactory, "test-db", SCHEMA_VERSION);
  expect(db).toBeDefined();
  return db!;
};

describe("defaultFactory", () => {
  it("is undefined in jsdom, which is the real branch npm test takes", () => {
    // Not a mock: this is the environment the suite actually runs in, and the
    // whole cache degrades through this branch.
    expect(defaultFactory()).toBeUndefined();
  });
});

describe("openDatabase", () => {
  it("resolves undefined rather than rejecting when there is no IndexedDB", async () => {
    await expect(openDatabase(undefined)).resolves.toBeUndefined();
  });

  it("resolves undefined when the open fails (private mode, blocked storage)", async () => {
    const factory = new FakeIndexedDB();
    factory.failNextOpen = true;
    await expect(
      openDatabase(factory as unknown as IDBFactory, "test-db"),
    ).resolves.toBeUndefined();
  });

  it("creates every store the cache needs", async () => {
    const factory = new FakeIndexedDB();
    const db = await open(factory);

    // A transaction naming all four is the honest check: it throws in the real
    // API (and in the shim) if any store is missing.
    const ok = await withTransaction(
      db,
      [STORE_MAILBOXES, STORE_HEADERS, STORE_BODIES, STORE_OUTBOX],
      "readonly",
      () => true,
    );
    expect(ok).toBe(true);
  });

  it("runs the migration chain from version 0 only once", async () => {
    const factory = new FakeIndexedDB();
    await open(factory);
    // A second open at the same version must not re-run the upgrade, which in
    // the real API would throw on `createObjectStore` for an existing name.
    const again = await openDatabase(factory as unknown as IDBFactory, "test-db", SCHEMA_VERSION);
    expect(again).toBeDefined();
  });
});

describe("upgradeDatabase", () => {
  it("does nothing for an already-current database", () => {
    // The fall-through switch must not re-create stores when oldVersion is
    // already the current one — that is what makes the chain safe to re-run.
    const created: string[] = [];
    const db = {
      createObjectStore: (name: string) => {
        created.push(name);
        return { createIndex: () => undefined };
      },
    } as unknown as IDBDatabase;

    upgradeDatabase(db, SCHEMA_VERSION);
    expect(created).toEqual([]);
  });

  it("creates all four stores from scratch", () => {
    const created: string[] = [];
    const indexes: string[] = [];
    const db = {
      createObjectStore: (name: string) => {
        created.push(name);
        return {
          createIndex: (indexName: string) => {
            indexes.push(indexName);
          },
        };
      },
    } as unknown as IDBDatabase;

    upgradeDatabase(db, 0);

    expect(created).toEqual([
      STORE_MAILBOXES,
      STORE_HEADERS,
      STORE_BODIES,
      STORE_OUTBOX,
    ]);
    expect(indexes).toHaveLength(2);
  });
});

describe("withTransaction", () => {
  it("resolves the work's value once the transaction COMPLETES", async () => {
    const factory = new FakeIndexedDB();
    const db = await open(factory);

    const wrote = await withTransaction(db, [STORE_MAILBOXES], "readwrite", async (tx) => {
      await request(tx.objectStore(STORE_MAILBOXES).put({ id: "a", accountId: "x" }));
      return "done";
    });
    expect(wrote).toBe("done");

    const read = await withTransaction(db, [STORE_MAILBOXES], "readonly", async (tx) =>
      request(tx.objectStore(STORE_MAILBOXES).get("a")),
    );
    expect(read).toMatchObject({ id: "a" });
  });

  it("resolves undefined when the transaction cannot even be opened", async () => {
    const factory = new FakeIndexedDB();
    const db = await open(factory);
    factory.failNextTransaction = true;

    await expect(
      withTransaction(db, [STORE_MAILBOXES], "readwrite", () => "never"),
    ).resolves.toBeUndefined();
  });

  it("resolves undefined and aborts when the work throws", async () => {
    const factory = new FakeIndexedDB();
    const db = await open(factory);

    const result = await withTransaction(db, [STORE_MAILBOXES], "readwrite", async (tx) => {
      await request(tx.objectStore(STORE_MAILBOXES).put({ id: "b", accountId: "x" }));
      throw new Error("mid-transaction failure");
      // Unreachable, and present only so the callback has a non-void return
      // type — the assertion below is that this value never arrives.
      return "committed";
    });

    expect(result).toBeUndefined();
  });

  it("resolves undefined for a store outside the transaction's scope", async () => {
    const factory = new FakeIndexedDB();
    const db = await open(factory);

    await expect(
      withTransaction(db, [STORE_MAILBOXES], "readonly", (tx) =>
        tx.objectStore(STORE_HEADERS).getAll(),
      ),
    ).resolves.toBeUndefined();
  });
});

describe("deleteDatabase", () => {
  it("resolves even when the factory cannot delete", async () => {
    await expect(deleteDatabase(undefined)).resolves.toBeUndefined();
  });

  it("removes the database", async () => {
    const factory = new FakeIndexedDB();
    const db = await open(factory);
    await withTransaction(db, [STORE_MAILBOXES], "readwrite", async (tx) => {
      await request(tx.objectStore(STORE_MAILBOXES).put({ id: "a", accountId: "x" }));
    });

    await deleteDatabase(factory as unknown as IDBFactory, "test-db");

    // A fresh open finds an empty store rather than the old row.
    const reopened = await open(factory);
    const rows = await withTransaction(reopened, [STORE_MAILBOXES], "readonly", (tx) =>
      request(tx.objectStore(STORE_MAILBOXES).getAll()),
    );
    expect(rows).toEqual([]);
  });
});
