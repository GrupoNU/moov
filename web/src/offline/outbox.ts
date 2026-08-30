/**
 * The Outbox — mail composed offline, and the queue that drains it (L3 E9).
 *
 * # Gmail's shape, adopted
 *
 * "Offline sends queue in an **Outbox** folder" (canon §2.10). That is the
 * whole design and it is the right one for a reason worth stating: a message
 * the user pressed Send on has left their hands, and any UI that leaves it
 * looking like a draft — or worse, loses it on tab close — breaks the one
 * promise a mail client makes. So the queued item is a first-class object in
 * durable storage, it appears in the sidebar as a folder (only when non-empty,
 * exactly as Gmail's does), and every state it can be in is visible.
 *
 * # This file is pure
 *
 * The state machine and the drain decision are here; the IndexedDB rows are in
 * {@link OutboxStore} below, and the actual send is a transport function
 * injected into {@link drainOutbox}. That split is what lets the interesting
 * part — "what happens when the third of five queued messages gets a permanent
 * 5xx" — be a unit test rather than an offline browser session.
 *
 * # The undo window is NOT re-implemented here
 *
 * A queued message that reaches the server goes through the ordinary send path,
 * which means the server's `EmailSubmission` gets its `sendAt` and the undo
 * window is the server's clock, exactly as for an online send (W3). This queue
 * ends at "handed to the send path"; it does not hold mail back for five extra
 * seconds of its own, which would make the effective delay the sum of two
 * windows and make `cancelSubmission` race a local timer.
 */

import type { DraftSpec } from "../mail/write";
import { request, STORE_OUTBOX, withTransaction } from "./idb";

/**
 * Where a queued message is in its life.
 *
 *   - **queued** — waiting for a connection. The resting state.
 *   - **sending** — a drain is in flight for it. Exists so a second drain
 *     (the `online` event and the SSE recovery can both fire) cannot pick up
 *     the same item and send it twice.
 *   - **failed** — the server refused it, or it ran out of attempts. Terminal
 *     until the user retries; NEVER dropped.
 *
 * There is no "sent" state, because a sent item is removed: keeping it would
 * make the Outbox a permanent second Sent folder, and the real Sent folder is
 * where a sent message belongs.
 */
export type OutboxState = "queued" | "sending" | "failed";

/** How many automatic attempts an item gets before it needs a human. */
export const MAX_ATTEMPTS = 3;

/** One message waiting to go out. */
export interface OutboxItem {
  /** Ours, generated locally — the server has never seen this message. */
  readonly id: string;
  readonly accountId: string;
  readonly state: OutboxState;
  /** Everything the send path needs, exactly as the composer built it. */
  readonly spec: DraftSpec;
  /** The identity to send as, resolved when the user pressed Send. */
  readonly identityId: string;
  readonly sentMailboxId: string | undefined;
  readonly queuedAt: number;
  readonly attempts: number;
  /** The server's or the network's own words, when something went wrong. */
  readonly lastError: string | undefined;
  /** For the list row: who it is to and what it is about. */
  readonly subject: string;
  readonly recipients: readonly string[];
}

/**
 * Generates an id that cannot collide with a server id or another queue item.
 *
 * The `ob-` prefix keeps it distinguishable from a JMAP id at a glance in any
 * log or devtools row. The rest is a timestamp plus randomness, so two messages
 * queued in the same millisecond differ.
 *
 * It is deliberately NOT relied on for ordering: base-36 of a growing number
 * changes digit count, so ids do not sort lexicographically in time order. The
 * queue's order comes from `queuedAt`, which {@link drainable} and
 * {@link OutboxStore.list} both sort on numerically.
 */
export function newOutboxId(now: number = Date.now(), entropy: number = Math.random()): string {
  return `ob-${now.toString(36)}-${entropy.toString(36).slice(2, 10)}`;
}

// ---------------------------------------------------------------------------
// the state machine
// ---------------------------------------------------------------------------

/** Marks an item as being sent right now. */
export function markSending(item: OutboxItem): OutboxItem {
  return { ...item, state: "sending", attempts: item.attempts + 1 };
}

/**
 * Records a failure and decides whether it is terminal.
 *
 * `permanent` is the caller's judgement — a 5xx from the submission, or a
 * refusal the server named — and it short-circuits the attempt count: retrying
 * a message the server has already rejected on its merits only produces the
 * same rejection three times, and each one is a chance to duplicate.
 *
 * A TRANSIENT failure below the attempt cap returns to `queued`, which is the
 * honest state: the mail has not gone and we will try again. Only at the cap
 * does it become `failed` and wait for a human.
 */
export function markFailure(
  item: OutboxItem,
  error: string,
  permanent: boolean,
): OutboxItem {
  const terminal = permanent || item.attempts >= MAX_ATTEMPTS;
  return { ...item, state: terminal ? "failed" : "queued", lastError: error };
}

/**
 * The user's explicit retry of a failed item.
 *
 * Resets the attempt counter, because the human pressing the button is new
 * information: they may have fixed the address, or simply know the server is
 * back. Leaving the count at the cap would make the retry button send once and
 * fail permanently again, which reads as the button not working.
 */
export function retryItem(item: OutboxItem): OutboxItem {
  return { ...item, state: "queued", attempts: 0, lastError: undefined };
}

/**
 * The items a drain should pick up, in the order they were queued.
 *
 * `sending` items are skipped — that is the whole point of the state — and
 * `failed` ones are not retried automatically. FIFO by `queuedAt` because a
 * mail thread sent offline must arrive in the order it was written; sorting by
 * anything else would deliver a reply before the message it replies to.
 */
export function drainable(items: readonly OutboxItem[]): readonly OutboxItem[] {
  return items
    .filter((item) => item.state === "queued")
    .slice()
    .sort((a, b) => a.queuedAt - b.queuedAt);
}

/** True when the sidebar shows the Outbox folder at all (Gmail's shape). */
export function showsOutbox(items: readonly OutboxItem[]): boolean {
  return items.length > 0;
}

/** How many items the Outbox badge reports: everything not yet gone. */
export function pendingCount(items: readonly OutboxItem[]): number {
  return items.filter((item) => item.state !== "failed").length;
}

/** True when anything in the queue needs the user's attention. */
export function hasFailures(items: readonly OutboxItem[]): boolean {
  return items.some((item) => item.state === "failed");
}

// ---------------------------------------------------------------------------
// draining
// ---------------------------------------------------------------------------

/** What one send attempt reported back. */
export type SendAttempt =
  | { readonly kind: "sent" }
  | { readonly kind: "failed"; readonly error: string; readonly permanent: boolean };

/** The transport a drain uses, injected so the logic is testable without JMAP. */
export type OutboxTransport = (item: OutboxItem) => Promise<SendAttempt>;

/** What a drain did. */
export interface DrainResult {
  readonly sent: readonly string[];
  readonly failed: readonly string[];
  /** Items still queued: the drain stopped early, or they came back. */
  readonly remaining: readonly string[];
}

/**
 * Sends every drainable item, in order, stopping at the first transient
 * failure.
 *
 * # Why it stops
 *
 * A transient failure means the connection is not actually back — the `online`
 * event fires on a captive portal, and an SSE reconnect can succeed a moment
 * before the SMTP path is reachable. Marching through the rest of the queue in
 * that state burns every item's attempt budget in one go and turns a two-second
 * blip into five permanently failed messages. So the first transient failure
 * ends the drain; the next trigger tries again from the top.
 *
 * A PERMANENT failure does not stop it: that item is the problem, not the
 * connection, and the messages behind it should still go.
 *
 * # Sequential, never parallel
 *
 * `for … await` rather than `Promise.all`, for the ordering reason in
 * {@link drainable}: replies must not overtake the messages they answer.
 */
export async function drainOutbox(
  items: readonly OutboxItem[],
  transport: OutboxTransport,
  onChange: (item: OutboxItem) => Promise<void> | void,
): Promise<DrainResult> {
  const sent: string[] = [];
  const failed: string[] = [];
  const queue = drainable(items);

  for (let index = 0; index < queue.length; index += 1) {
    const item = queue[index];
    if (item === undefined) continue;

    const attempting = markSending(item);
    await onChange(attempting);

    let outcome: SendAttempt;
    try {
      outcome = await transport(attempting);
    } catch (error) {
      outcome = {
        kind: "failed",
        error: error instanceof Error ? error.message : String(error),
        permanent: false,
      };
    }

    if (outcome.kind === "sent") {
      sent.push(item.id);
      continue;
    }

    const afterFailure = markFailure(attempting, outcome.error, outcome.permanent);
    await onChange(afterFailure);
    if (afterFailure.state === "failed") {
      failed.push(item.id);
      continue;
    }
    // Transient and still queued: the connection is not really back. Stop, and
    // report everything from here on as remaining.
    return {
      sent,
      failed,
      remaining: queue.slice(index).map((entry) => entry.id),
    };
  }

  return { sent, failed, remaining: [] };
}

// ---------------------------------------------------------------------------
// persistence
// ---------------------------------------------------------------------------

/**
 * The durable queue.
 *
 * Separate from the state machine above so the interesting logic stays pure.
 * Every method resolves rather than rejecting, per `idb.ts` — but note the
 * asymmetry that matters: a failed READ is a cache miss and harmless, while a
 * failed WRITE means a message the user pressed Send on was not persisted.
 * {@link OutboxStore.put} therefore reports its success as a boolean, and the
 * composer refuses to close on false rather than pretending the mail is queued.
 */
export class OutboxStore {
  constructor(
    private readonly db: IDBDatabase,
    private readonly accountId: string,
  ) {}

  /** Persists one item. Returns false when the write did not commit. */
  async put(item: OutboxItem): Promise<boolean> {
    const done = await withTransaction(this.db, [STORE_OUTBOX], "readwrite", async (tx) => {
      await request(tx.objectStore(STORE_OUTBOX).put(item));
      return true;
    });
    return done === true;
  }

  /** Removes an item — a successful send, or the user discarding it. */
  async remove(id: string): Promise<void> {
    await withTransaction(this.db, [STORE_OUTBOX], "readwrite", async (tx) => {
      await request(tx.objectStore(STORE_OUTBOX).delete(id));
    });
  }

  /** Everything queued for this account, oldest first. */
  async list(): Promise<readonly OutboxItem[]> {
    const rows = await withTransaction(this.db, [STORE_OUTBOX], "readonly", async (tx) =>
      (await request(tx.objectStore(STORE_OUTBOX).getAll())) as readonly OutboxItem[],
    );
    if (rows === undefined) return [];
    return rows
      .filter((row) => row.accountId === this.accountId)
      .sort((a, b) => a.queuedAt - b.queuedAt);
  }

  /**
   * Returns `sending` items to `queued` at startup.
   *
   * A tab closed mid-drain leaves an item marked `sending` with nothing sending
   * it, and that item would be skipped by every future drain — a message stuck
   * forever in a state that looks like progress. Reclaiming them on boot is the
   * cheap fix, and it is safe: the send either happened (in which case the item
   * was removed) or it did not.
   */
  async reclaimStuck(): Promise<readonly OutboxItem[]> {
    const items = await this.list();
    const stuck = items.filter((item) => item.state === "sending");
    for (const item of stuck) await this.put({ ...item, state: "queued" });
    return stuck;
  }
}
