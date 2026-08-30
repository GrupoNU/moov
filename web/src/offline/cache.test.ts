import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { BODY_CAP, HEADER_CAP, MailCache } from "./cache";
import { openDatabase, SCHEMA_VERSION } from "./idb";
import { FakeIndexedDB, installKeyRange } from "../test/fakeIndexedDB";
import type { Email, Mailbox } from "../mail/types";

/**
 * The offline cache.
 *
 * Three properties are load-bearing and each has a bug it prevents:
 *
 *   - **account scoping** — one browser profile, two mailboxes; a cache that
 *     let the second read the first's mail would be the worst defect in this
 *     epic;
 *   - **the caps** — an uncapped cache grows until the browser evicts the
 *     WHOLE origin, taking the outbox (a message the user believes they sent)
 *     with it;
 *   - **silent degradation** — a broken store must read as a cache miss, never
 *     as a thrown error on a mail screen.
 */

let restoreKeyRange: () => void;
let factory: FakeIndexedDB;
let db: IDBDatabase;

beforeEach(async () => {
  restoreKeyRange = installKeyRange();
  factory = new FakeIndexedDB();
  const opened = await openDatabase(
    factory as unknown as IDBFactory,
    `cache-${String(Math.random())}`,
    SCHEMA_VERSION,
  );
  db = opened!;
});

afterEach(() => {
  restoreKeyRange();
});

function mailbox(id: string, name = id): Mailbox {
  return {
    id,
    name,
    parentId: null,
    role: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
    isSubscribed: true,
    myRights: {
      mayReadItems: true,
      mayAddItems: true,
      mayRemoveItems: true,
      maySetSeen: true,
      maySetKeywords: true,
      mayCreateChild: true,
      mayRename: true,
      mayDelete: true,
      maySubmit: true,
    },
  };
}

/** A header whose `receivedAt` is derived from `minute`, so order is explicit. */
function header(id: string, minute: number, extra: Partial<Email> = {}): Email {
  const stamp = `2026-08-30T${String(Math.floor(minute / 60)).padStart(2, "0")}:${String(
    minute % 60,
  ).padStart(2, "0")}:00Z`;
  return { id, receivedAt: stamp, subject: id, ...extra };
}

describe("mailboxes", () => {
  it("round-trips the list", async () => {
    const cache = new MailCache(db, "acc");
    await cache.putMailboxes([mailbox("inbox"), mailbox("sent")]);

    expect((await cache.mailboxes()).map((box) => box.id)).toEqual(["inbox", "sent"]);
  });

  it("REPLACES the list, so a folder deleted elsewhere disappears here", async () => {
    const cache = new MailCache(db, "acc");
    await cache.putMailboxes([mailbox("inbox"), mailbox("old")]);
    await cache.putMailboxes([mailbox("inbox")]);

    expect((await cache.mailboxes()).map((box) => box.id)).toEqual(["inbox"]);
  });

  it("never shows one account another's folders", async () => {
    await new MailCache(db, "acc-a").putMailboxes([mailbox("a-inbox")]);
    await new MailCache(db, "acc-b").putMailboxes([mailbox("b-inbox")]);

    expect((await new MailCache(db, "acc-a").mailboxes()).map((box) => box.id)).toEqual([
      "a-inbox",
    ]);
    expect((await new MailCache(db, "acc-b").mailboxes()).map((box) => box.id)).toEqual([
      "b-inbox",
    ]);
  });

  it("returns an empty list rather than throwing when the store is broken", async () => {
    const cache = new MailCache(db, "acc");
    await cache.putMailboxes([mailbox("inbox")]);
    factory.failNextTransaction = true;

    // A cache miss, not an exception on a mail screen.
    await expect(cache.mailboxes()).resolves.toEqual([]);
  });

  it("does not throw when a WRITE fails", async () => {
    const cache = new MailCache(db, "acc");
    factory.failNextTransaction = true;
    await expect(cache.putMailboxes([mailbox("inbox")])).resolves.toBeUndefined();
  });
});

describe("headers", () => {
  it("returns a mailbox's headers newest first", async () => {
    const cache = new MailCache(db, "acc");
    await cache.putHeaders("inbox", [header("a", 10), header("c", 30), header("b", 20)]);

    expect((await cache.headers("inbox")).map((email) => email.id)).toEqual(["c", "b", "a"]);
  });

  it("keeps mailboxes separate", async () => {
    const cache = new MailCache(db, "acc");
    await cache.putHeaders("inbox", [header("a", 10)]);
    await cache.putHeaders("sent", [header("s", 20)]);

    expect((await cache.headers("inbox")).map((email) => email.id)).toEqual(["a"]);
    expect((await cache.headers("sent")).map((email) => email.id)).toEqual(["s"]);
  });

  it("caps a mailbox at HEADER_CAP, evicting the OLDEST", async () => {
    const cache = new MailCache(db, "acc");
    // One past the cap, in ascending date order so the oldest is `h0`.
    const many = Array.from({ length: HEADER_CAP + 5 }, (_, index) =>
      header(`h${String(index)}`, index),
    );
    await cache.putHeaders("inbox", many);

    const kept = await cache.headers("inbox", HEADER_CAP + 50);
    expect(kept).toHaveLength(HEADER_CAP);
    // The five oldest are gone; the newest survived.
    expect(kept.map((email) => email.id)).toContain(`h${String(HEADER_CAP + 4)}`);
    expect(kept.map((email) => email.id)).not.toContain("h0");
    expect(kept.map((email) => email.id)).not.toContain("h4");
  });

  it("updates a header in place rather than duplicating it", async () => {
    // The flags-changed refresh, which is most of what write-through sees.
    const cache = new MailCache(db, "acc");
    await cache.putHeaders("inbox", [header("a", 10, { keywords: {} })]);
    await cache.putHeaders("inbox", [header("a", 10, { keywords: { $seen: true } })]);

    const kept = await cache.headers("inbox");
    expect(kept).toHaveLength(1);
    expect(kept[0]?.keywords).toEqual({ $seen: true });
  });

  it("tolerates a message with no receivedAt at all", async () => {
    // The index must stay total: a header with no date is still cacheable.
    const cache = new MailCache(db, "acc");
    await cache.putHeaders("inbox", [{ id: "nodate", subject: "x" }]);

    expect((await cache.headers("inbox")).map((email) => email.id)).toEqual(["nodate"]);
  });

  it("scopes allHeaders to the account", async () => {
    await new MailCache(db, "acc-a").putHeaders("inbox", [header("a", 10)]);
    await new MailCache(db, "acc-b").putHeaders("inbox", [header("b", 20)]);

    expect((await new MailCache(db, "acc-a").allHeaders()).map((email) => email.id)).toEqual([
      "a",
    ]);
  });

  it("returns empty rather than throwing on a broken read", async () => {
    const cache = new MailCache(db, "acc");
    await cache.putHeaders("inbox", [header("a", 10)]);
    factory.failNextTransaction = true;
    await expect(cache.headers("inbox")).resolves.toEqual([]);
  });
});

describe("bodies", () => {
  it("round-trips a body the user opened", async () => {
    const cache = new MailCache(db, "acc");
    const email = header("a", 10, {
      bodyValues: { "0": { value: "hola", isEncodingProblem: false, isTruncated: false } },
    });
    await cache.putBody(email);

    expect((await cache.body("a"))?.bodyValues?.["0"]?.value).toBe("hola");
  });

  it("misses cleanly for a message that was never opened", async () => {
    await expect(new MailCache(db, "acc").body("never")).resolves.toBeUndefined();
  });

  it("never serves one account another's body", async () => {
    await new MailCache(db, "acc-a").putBody(header("shared-id", 10));
    // Same id, different account: must be a miss, not the other account's mail.
    await expect(new MailCache(db, "acc-b").body("shared-id")).resolves.toBeUndefined();
  });

  it("evicts the LEAST RECENTLY READ past BODY_CAP", async () => {
    const cache = new MailCache(db, "acc");
    for (let index = 0; index < BODY_CAP; index += 1) {
      await cache.putBody(header(`b${String(index)}`, index));
    }

    // Re-open the oldest one: that must be what saves it.
    await cache.putBody(header("b0", 0));
    // One more push past the cap.
    await cache.putBody(header("new", 999));

    // `b1` was the least recently read once `b0` was refreshed.
    await expect(cache.body("b1")).resolves.toBeUndefined();
    await expect(cache.body("b0")).resolves.toBeDefined();
    await expect(cache.body("new")).resolves.toBeDefined();
    expect((await cache.allBodies()).length).toBe(BODY_CAP);
  });

  it("does not refresh the LRU stamp on a plain read", async () => {
    /*
     * A probe ("is this cached?") must not keep a message alive; only an actual
     * open does, and the open path calls putBody.
     */
    const cache = new MailCache(db, "acc");
    await cache.putBody(header("first", 1));
    await cache.putBody(header("second", 2));
    await cache.body("first");

    // Fill to the cap so exactly one eviction happens.
    for (let index = 0; index < BODY_CAP - 1; index += 1) {
      await cache.putBody(header(`filler${String(index)}`, 100 + index));
    }

    // `first` was read but not re-put, so it is still the oldest write.
    await expect(cache.body("first")).resolves.toBeUndefined();
  });
});

describe("clearOtherAccounts", () => {
  it("drops every other account's rows and keeps its own", async () => {
    const other = new MailCache(db, "acc-b");
    await other.putMailboxes([mailbox("b-inbox")]);
    await other.putHeaders("inbox", [header("bh", 10)]);
    await other.putBody(header("bb", 10));

    const mine = new MailCache(db, "acc-a");
    await mine.putMailboxes([mailbox("a-inbox")]);
    await mine.putHeaders("inbox", [header("ah", 10)]);
    await mine.putBody(header("ab", 10));

    await mine.clearOtherAccounts();

    // Mine survives — the whole point of not wiping everything on sign-in.
    expect(await mine.stats()).toEqual({ headers: 1, bodies: 1 });
    expect((await mine.mailboxes()).map((box) => box.id)).toEqual(["a-inbox"]);
    // Theirs is gone.
    expect(await other.stats()).toEqual({ headers: 0, bodies: 0 });
    expect(await other.mailboxes()).toEqual([]);
  });
});
