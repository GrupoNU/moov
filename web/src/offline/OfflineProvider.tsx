import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import type { Email, Mailbox } from "../mail/types";
import { AddressStore } from "./addressStore";
import { loadCacheDepth, MailCache } from "./cache";
import { openDatabase } from "./idb";
import { OutboxStore, type OutboxItem } from "./outbox";

/**
 * The offline context: one database, one cache, one outbox (L3 E9, D-2).
 *
 * # Why a provider rather than a module-level singleton
 *
 * The cache is scoped to an ACCOUNT, and the account is known only after
 * sign-in. A module-level singleton would either have to be re-keyed on every
 * read (which is how one of them ends up reading another account's rows) or be
 * torn down and rebuilt by imperative code that nothing supervises. A provider
 * makes the lifetime a React lifetime: the database opens when the account is
 * known, the rows of every OTHER account are dropped once, and it all goes away
 * on sign-out.
 *
 * # The whole thing is optional, and that is load-bearing
 *
 * `cache` and `outbox` are `undefined` whenever IndexedDB is missing, blocked,
 * or broken — which includes the entire unit suite, where jsdom has no
 * IndexedDB at all. Every consumer therefore has to handle absence, and the
 * app's behaviour in that case is the behaviour it had before this epic:
 * online-only, with no offline banner claiming anything it cannot back up. That
 * is not a degraded mode to apologise for; it is the mode the app shipped in.
 */

export interface OfflineApi {
  /** The mail cache, when this browser has usable storage. */
  readonly cache: MailCache | undefined;
  /** The durable send queue, when this browser has usable storage. */
  readonly outbox: OutboxStore | undefined;
  /**
   * E7: the address index behind recipient autocomplete.
   *
   * Lives here because it shares everything that matters with the cache — the
   * same database handle, the same account scoping, the same "undefined when
   * there is no storage" contract — and a second provider opening a second
   * connection to the same database would be two lifetimes to keep in step for
   * no benefit.
   */
  readonly addresses: AddressStore | undefined;
  /** True once the open attempt has settled, either way. */
  readonly isReady: boolean;
  /** `navigator.onLine`, kept live by the online/offline events. */
  readonly isOnline: boolean;
  /** The queue as the UI renders it, refreshed by {@link OfflineApi.reloadOutbox}. */
  readonly outboxItems: readonly OutboxItem[];
  reloadOutbox: () => Promise<void>;
  /** Write-through helpers, no-ops without a cache. */
  cacheMailboxes: (mailboxes: readonly Mailbox[]) => void;
  cacheHeaders: (mailboxId: string, emails: readonly Email[]) => void;
  cacheBody: (email: Email) => void;
}

const OfflineContext = createContext<OfflineApi | undefined>(undefined);

/** Reads `navigator.onLine`, defaulting to online where it does not exist. */
function readOnline(): boolean {
  return typeof navigator === "undefined" || navigator.onLine;
}

export function OfflineProvider({
  accountId,
  children,
}: {
  /** Empty until sign-in resolves; the database waits for it. */
  readonly accountId: string;
  readonly children: React.ReactNode;
}): React.JSX.Element {
  const [cache, setCache] = useState<MailCache | undefined>(undefined);
  const [outbox, setOutbox] = useState<OutboxStore | undefined>(undefined);
  const [addresses, setAddresses] = useState<AddressStore | undefined>(undefined);
  const [isReady, setReady] = useState(false);
  const [isOnline, setOnline] = useState(readOnline);
  const [outboxItems, setOutboxItems] = useState<readonly OutboxItem[]>([]);

  /*
   * The database handle, held in a ref so the cleanup can close it without
   * making it a dependency of anything.
   */
  const dbRef = useRef<IDBDatabase | undefined>(undefined);

  useEffect(() => {
    if (accountId === "") return undefined;
    let cancelled = false;

    void (async () => {
      const db = await openDatabase();
      if (cancelled) {
        db?.close();
        return;
      }
      if (db === undefined) {
        // No storage: online-only, which is a fully supported mode.
        setReady(true);
        return;
      }
      dbRef.current = db;
      /*
       * The depth comes from the MIRROR, not from the prefs context, and that
       * is forced rather than chosen: this provider sits above `PrefsProvider`
       * in the tree (it takes only an account id) and, more importantly, an
       * offline cold boot has no session to fetch prefs with at all. The mirror
       * is written through whenever prefs load — see `useCacheDepth` — so the
       * value read here is the user's, one boot late at worst.
       */
      const mailCache = new MailCache(db, accountId, loadCacheDepth());
      const queue = new OutboxStore(db, accountId);
      /*
       * Drop the other accounts' rows BEFORE exposing the cache, so no read can
       * observe them even for a frame. Own rows are kept — wiping everything on
       * sign-in would throw away exactly the cache the returning user wants.
       */
      await mailCache.clearOtherAccounts();
      /*
       * Reclaim anything a previous tab left mid-flight. Without this, a tab
       * closed during a drain leaves an item marked `sending` that no future
       * drain will ever pick up — a message stuck forever in a state that looks
       * like progress.
       */
      await queue.reclaimStuck();
      if (cancelled) {
        db.close();
        return;
      }
      setCache(mailCache);
      setOutbox(queue);
      setAddresses(new AddressStore(db, accountId));
      setOutboxItems(await queue.list());
      setReady(true);
    })();

    return () => {
      cancelled = true;
      dbRef.current?.close();
      dbRef.current = undefined;
      setCache(undefined);
      setOutbox(undefined);
      setAddresses(undefined);
      setOutboxItems([]);
      setReady(false);
    };
  }, [accountId]);

  // --- the browser's own connectivity signal -------------------------------

  useEffect(() => {
    if (typeof window === "undefined") return undefined;
    const goOnline = (): void => {
      setOnline(true);
    };
    const goOffline = (): void => {
      setOnline(false);
    };
    window.addEventListener("online", goOnline);
    window.addEventListener("offline", goOffline);
    // Re-read on mount: the flag can have changed between render and effect.
    setOnline(readOnline());
    return () => {
      window.removeEventListener("online", goOnline);
      window.removeEventListener("offline", goOffline);
    };
  }, []);

  const reloadOutbox = useCallback(async (): Promise<void> => {
    if (outbox === undefined) return;
    setOutboxItems(await outbox.list());
  }, [outbox]);

  /*
   * The write-through helpers are FIRE-AND-FORGET on purpose.
   *
   * They are called from the same effects that render mail, and making the
   * render path await a storage write would put an IndexedDB transaction on the
   * critical path of painting the inbox — for a cache whose entire value
   * proposition is that its absence changes nothing. A dropped write costs one
   * message's offline availability; a slow paint costs the Gmail-class bar.
   */
  const cacheMailboxes = useCallback(
    (mailboxes: readonly Mailbox[]): void => {
      void cache?.putMailboxes(mailboxes);
    },
    [cache],
  );

  const cacheHeaders = useCallback(
    (mailboxId: string, emails: readonly Email[]): void => {
      if (emails.length === 0) return;
      void cache?.putHeaders(mailboxId, emails);
    },
    [cache],
  );

  const cacheBody = useCallback(
    (email: Email): void => {
      void cache?.putBody(email);
    },
    [cache],
  );

  const value = useMemo<OfflineApi>(
    () => ({
      cache,
      outbox,
      addresses,
      isReady,
      isOnline,
      outboxItems,
      reloadOutbox,
      cacheMailboxes,
      cacheHeaders,
      cacheBody,
    }),
    [
      cache,
      outbox,
      addresses,
      isReady,
      isOnline,
      outboxItems,
      reloadOutbox,
      cacheMailboxes,
      cacheHeaders,
      cacheBody,
    ],
  );

  return <OfflineContext.Provider value={value}>{children}</OfflineContext.Provider>;
}

/**
 * The inert API, used outside a provider.
 *
 * A module-level constant rather than an object built per call, so the memo
 * dependencies of any consumer see a stable identity.
 */
const INERT: OfflineApi = {
  cache: undefined,
  outbox: undefined,
  addresses: undefined,
  isReady: true,
  isOnline: true,
  outboxItems: [],
  reloadOutbox: () => Promise.resolve(),
  cacheMailboxes: () => undefined,
  cacheHeaders: () => undefined,
  cacheBody: () => undefined,
};

/**
 * The offline API.
 *
 * Returns a fully-formed inert value outside a provider rather than throwing:
 * every consumer already has to handle "no storage", so an absent provider is
 * the same case, and a hook that throws would make the offline layer able to
 * take down a screen that works perfectly well without it.
 */
export function useOffline(): OfflineApi {
  const found = useContext(OfflineContext);
  return found ?? INERT;
}
