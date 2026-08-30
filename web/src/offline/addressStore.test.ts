import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { FakeIndexedDB, installKeyRange } from "../test/fakeIndexedDB";
import { AddressStore, ADDRESS_CAP } from "./addressStore";
import { openDatabase, SCHEMA_VERSION } from "./idb";

/**
 * The address index's persistence (E7).
 *
 * The pure merge/rank rules are pinned in `mail/addressIndex.test.ts`. What
 * only a store can prove is here: that two accounts in one browser never see
 * each other's correspondents, that the cap actually evicts, and that the
 * opt-out's "delete saved addresses" really deletes.
 */

let restoreKeyRange: () => void;

beforeEach(() => {
  restoreKeyRange = installKeyRange();
});

afterEach(() => {
  restoreKeyRange();
});

async function openStore(
  factory: FakeIndexedDB,
  accountId = "a1",
  name = "addr-db",
): Promise<AddressStore> {
  const db = await openDatabase(factory as unknown as IDBFactory, name, SCHEMA_VERSION);
  expect(db).toBeDefined();
  return new AddressStore(db!, accountId);
}

describe("AddressStore", () => {
  it("records a sighting and reads it back", async () => {
    const store = await openStore(new FakeIndexedDB());
    await store.record([{ email: "ana@x.com", displayName: "Ana Gómez" }], "browsed", 1_000);

    const all = await store.all();
    expect(all).toHaveLength(1);
    expect(all[0]).toMatchObject({
      email: "ana@x.com",
      displayName: "Ana Gómez",
      timesSeen: 1,
      source: "browsed",
    });
  });

  it("merges repeat sightings rather than duplicating rows", async () => {
    const store = await openStore(new FakeIndexedDB());
    await store.record([{ email: "ana@x.com", displayName: undefined }], "browsed", 1_000);
    await store.record([{ email: "ANA@X.com", displayName: "Ana Gómez" }], "sent", 2_000);

    const all = await store.all();
    expect(all).toHaveLength(1);
    expect(all[0]).toMatchObject({
      email: "ana@x.com",
      displayName: "Ana Gómez",
      timesSeen: 2,
      lastSeenAt: 2_000,
      // The first sighting's source is the origin, and it sticks.
      source: "browsed",
    });
  });

  it("returns addresses ranked, not in insertion order", async () => {
    const store = await openStore(new FakeIndexedDB());
    await store.record([{ email: "poco@x.com", displayName: undefined }], "browsed", 9_000);
    for (let n = 0; n < 4; n += 1) {
      await store.record([{ email: "mucho@x.com", displayName: undefined }], "browsed", 1_000);
    }

    const all = await store.all();
    expect(all[0]?.email).toBe("mucho@x.com");
  });

  it("never lets one account read another's addresses", async () => {
    // The single most important property in this file: the addresses someone
    // corresponds with are among the more sensitive things stored here.
    const factory = new FakeIndexedDB();
    const first = await openStore(factory, "a1");
    const second = await openStore(factory, "a2");

    await first.record([{ email: "cliente@x.com", displayName: "Cliente" }], "sent");

    expect(await second.all()).toEqual([]);
    expect(await first.all()).toHaveLength(1);
  });

  it("keys by account, so the same address can exist for two accounts", async () => {
    const factory = new FakeIndexedDB();
    const first = await openStore(factory, "a1");
    const second = await openStore(factory, "a2");

    await first.record([{ email: "shared@x.com", displayName: "Desde A1" }], "browsed", 1_000);
    await second.record([{ email: "shared@x.com", displayName: "Desde A2" }], "browsed", 2_000);

    expect((await first.all())[0]?.displayName).toBe("Desde A1");
    expect((await second.all())[0]?.displayName).toBe("Desde A2");
  });

  it("clear() erases this account's addresses and only this account's", async () => {
    const factory = new FakeIndexedDB();
    const first = await openStore(factory, "a1");
    const second = await openStore(factory, "a2");

    await first.record([{ email: "uno@x.com", displayName: undefined }], "browsed");
    await second.record([{ email: "dos@x.com", displayName: undefined }], "browsed");

    await first.clear();

    // The opt-out must really delete…
    expect(await first.all()).toEqual([]);
    // …and must not reach across accounts while doing it.
    expect(await second.all()).toHaveLength(1);
  });

  it("counts what it holds", async () => {
    const store = await openStore(new FakeIndexedDB());
    expect(await store.count()).toBe(0);
    await store.record(
      [
        { email: "a@x.com", displayName: undefined },
        { email: "b@x.com", displayName: undefined },
      ],
      "browsed",
    );
    expect(await store.count()).toBe(2);
  });

  it("evicts the least useful rows past the cap", async () => {
    const store = await openStore(new FakeIndexedDB());

    // One address worth keeping: seen many times.
    for (let n = 0; n < 5; n += 1) {
      await store.record([{ email: "importante@x.com", displayName: undefined }], "browsed", 1_000);
    }
    // And a flood of one-off newsletter senders, past the cap.
    const flood = Array.from({ length: ADDRESS_CAP + 20 }, (_, n) => ({
      email: `bulk${String(n)}@x.com`,
      displayName: undefined,
    }));
    await store.record(flood, "browsed", 500);

    const all = await store.all();
    expect(all).toHaveLength(ADDRESS_CAP);
    // The frequent correspondent survives the flood — that is what "least
    // useful first" has to mean for the cap to be worth having.
    expect(all.some((entry) => entry.email === "importante@x.com")).toBe(true);
  });

  it("does nothing, and does not throw, on an empty batch", async () => {
    const store = await openStore(new FakeIndexedDB());
    await expect(store.record([], "browsed")).resolves.toBeUndefined();
    expect(await store.all()).toEqual([]);
  });

  it("resolves rather than rejecting when the transaction cannot open", async () => {
    // The module's inherited rule: a broken store is an empty one, never a
    // thrown error on a composer keystroke.
    const factory = new FakeIndexedDB();
    const store = await openStore(factory);
    factory.failNextTransaction = true;
    await expect(
      store.record([{ email: "a@x.com", displayName: undefined }], "browsed"),
    ).resolves.toBeUndefined();

    factory.failNextTransaction = true;
    await expect(store.all()).resolves.toEqual([]);
  });
});
