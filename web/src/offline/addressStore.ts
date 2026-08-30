/**
 * Persistence for the address index (L3 E7; schema v2).
 *
 * # Why IndexedDB and not localStorage
 *
 * The label metadata next door lives in `localStorage` and that is right for
 * it: a handful of colours, read once at boot. This is a different shape — a
 * few thousand rows updated on every message the app loads — and localStorage
 * is synchronous, string-only and capped around 5 MB per origin. Writing an
 * index through it means serialising the WHOLE table on every sighting, on the
 * main thread, in the middle of rendering a message list. IndexedDB is already
 * open for the offline cache; this store rides along.
 *
 * # The rule inherited from `idb.ts`: failure is silent and total
 *
 * Every function here resolves and never rejects. An address index that throws
 * would break the composer, and the composer works perfectly without it —
 * autocomplete is an affordance, not a dependency. A broken store is
 * indistinguishable from an empty one.
 *
 * # Account scoping is in the KEY, not in a filter
 *
 * Rows are keyed `<accountId>\n<email>`. See the schema comment in `idb.ts`:
 * the addresses a person corresponds with are among the more sensitive things
 * this app holds, and scoping by filter-after-read leaves the leak one forgotten
 * `.filter()` away. A newline is the separator because it cannot occur in
 * either half — an email address containing one would have been refused by
 * `isValidEmail` long before it reached here.
 */

import {
  mergeSighting,
  rankAddresses,
  type AddressSource,
  type IndexedAddress,
} from "../mail/addressIndex";
import { request, STORE_ADDRESSES, withTransaction } from "./idb";

/** A stored row: the indexed address plus its scoping key. */
interface AddressRow extends IndexedAddress {
  /** `<accountId>\n<email>` — see the file header. */
  readonly key: string;
  readonly accountId: string;
}

/**
 * A hard cap on how many addresses are kept per account.
 *
 * Unbounded growth is the failure mode of every auto-fed index: a mailbox that
 * receives newsletters accumulates thousands of no-reply addresses nobody will
 * ever type, and the store grows forever while the suggestions get worse. At
 * the cap the least useful rows go first — lowest `timesSeen`, then oldest —
 * which is exactly the ordering `rankAddresses` already defines, read from the
 * other end.
 *
 * 2,000 is generous: it is more distinct correspondents than a person has, and
 * at roughly 100 bytes a row it is 200 kB — trivial next to the message bodies
 * in the same database.
 */
export const ADDRESS_CAP = 2000;

/** Builds the scoping key. One place, so reads and writes cannot disagree. */
function keyFor(accountId: string, email: string): string {
  return `${accountId}\n${email.trim().toLowerCase()}`;
}

/**
 * The address index for one account, bound to one database handle.
 *
 * A class for the same reason `MailCache` is one: every method needs the handle
 * and the account id, and threading them through each call site is how one of
 * them ends up reading another account's rows.
 */
export class AddressStore {
  constructor(
    private readonly db: IDBDatabase,
    private readonly accountId: string,
  ) {}

  /**
   * Records sightings, merging each into whatever is already stored.
   *
   * Takes a BATCH rather than one address because a message carries several and
   * one transaction per recipient would be several round-trips through the
   * IndexedDB queue for a single user action. The whole batch is one
   * transaction, so it either all lands or none of it does — and "none of it"
   * is fine, per the module's rule.
   */
  async record(
    sightings: readonly {
      readonly email: string;
      readonly displayName: string | undefined;
    }[],
    source: AddressSource,
    seenAt: number = Date.now(),
  ): Promise<void> {
    if (sightings.length === 0) return;
    await withTransaction(this.db, [STORE_ADDRESSES], "readwrite", async (tx) => {
      const store = tx.objectStore(STORE_ADDRESSES);
      for (const sighting of sightings) {
        const key = keyFor(this.accountId, sighting.email);
        const existing = (await request(store.get(key))) as AddressRow | undefined;
        const merged = mergeSighting(existing, {
          email: sighting.email,
          displayName: sighting.displayName,
          seenAt,
          source,
        });
        const row: AddressRow = { ...merged, key, accountId: this.accountId };
        await request(store.put(row));
      }
      await this.trim(store);
    });
  }

  /**
   * Drops the least useful rows once the account is over {@link ADDRESS_CAP}.
   *
   * Runs inside the caller's transaction so the cap cannot be exceeded between
   * a write and its cleanup. Reads the whole store, which is the honest thing
   * to do at this size: with no index on `timesSeen` there is no cursor that
   * walks in rank order, and building one would cost a write on every sighting
   * to save a read that happens only when the cap is actually hit.
   */
  private async trim(store: IDBObjectStore): Promise<void> {
    const rows = (await request(store.getAll())) as readonly AddressRow[];
    const mine = rows.filter((row) => row.accountId === this.accountId);
    if (mine.length <= ADDRESS_CAP) return;
    // Ranked best-first, so everything past the cap is the tail to evict.
    const ranked = rankAddresses(mine) as readonly AddressRow[];
    for (const row of ranked.slice(ADDRESS_CAP)) await request(store.delete(row.key));
  }

  /** Every address known for this account, ranked. Empty when there is none. */
  async all(): Promise<readonly IndexedAddress[]> {
    const rows = await withTransaction(
      this.db,
      [STORE_ADDRESSES],
      "readonly",
      async (tx) =>
        (await request(tx.objectStore(STORE_ADDRESSES).getAll())) as readonly AddressRow[],
    );
    if (rows === undefined) return [];
    return rankAddresses(rows.filter((row) => row.accountId === this.accountId));
  }

  /**
   * Erases every address stored for this account — the opt-out's "delete saved
   * addresses" (canon §2.3's escape hatch, made real).
   *
   * Only THIS account's rows: a second mailbox in the same browser has its own
   * index and its own consent, and clearing one must not clear the other.
   */
  async clear(): Promise<void> {
    await withTransaction(this.db, [STORE_ADDRESSES], "readwrite", async (tx) => {
      const store = tx.objectStore(STORE_ADDRESSES);
      const rows = (await request(store.getAll())) as readonly AddressRow[];
      for (const row of rows) {
        if (row.accountId === this.accountId) await request(store.delete(row.key));
      }
    });
  }

  /** How many addresses are stored — what the settings row reports. */
  async count(): Promise<number> {
    return (await this.all()).length;
  }
}
