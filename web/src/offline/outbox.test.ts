import { describe, expect, it, vi } from "vitest";

import {
  drainOutbox,
  drainable,
  hasFailures,
  markFailure,
  markSending,
  MAX_ATTEMPTS,
  newOutboxId,
  pendingCount,
  retryItem,
  showsOutbox,
  type OutboxItem,
  type SendAttempt,
} from "./outbox";
import type { DraftSpec } from "../mail/write";

/**
 * The Outbox queue.
 *
 * The rule every test here defends: **a message the user pressed Send on is
 * never silently lost.** Failures are visible, retries are possible, and a
 * drain that hits a bad connection does not burn every item's attempt budget on
 * the way past.
 */

const spec: DraftSpec = {
  mailboxId: "drafts",
  from: [{ name: null, email: "me@example.com" }],
  to: [{ name: null, email: "you@example.com" }],
  cc: [],
  bcc: [],
  subject: "Hola",
  text: "…",
  attachments: [],
};

function item(id: string, overrides: Partial<OutboxItem> = {}): OutboxItem {
  return {
    id,
    accountId: "acc",
    state: "queued",
    spec,
    identityId: "primary",
    sentMailboxId: "sent",
    queuedAt: Number(id.replace(/\D/g, "")) || 1,
    attempts: 0,
    lastError: undefined,
    subject: "Hola",
    recipients: ["you@example.com"],
    ...overrides,
  };
}

describe("newOutboxId", () => {
  it("is unique for two messages queued in the same millisecond", () => {
    expect(newOutboxId(1000, 0.1)).not.toBe(newOutboxId(1000, 0.9));
  });

  it("is recognisable as ours rather than a server id", () => {
    // Ordering deliberately does NOT come from the id — base-36 changes digit
    // count as the clock grows, so ids do not sort in time order. `queuedAt`
    // does that job, which `drainable` sorts on numerically.
    expect(newOutboxId(1000, 0.5).startsWith("ob-")).toBe(true);
  });
});

describe("the state machine", () => {
  it("markSending claims the item and counts the attempt", () => {
    const sending = markSending(item("1"));
    expect(sending.state).toBe("sending");
    expect(sending.attempts).toBe(1);
  });

  it("markFailure returns a transient failure to queued, below the cap", () => {
    const failed = markFailure(item("1", { attempts: 1 }), "network down", false);
    // The honest state: the mail has not gone and we will try again.
    expect(failed.state).toBe("queued");
    expect(failed.lastError).toBe("network down");
  });

  it("markFailure is terminal at the attempt cap", () => {
    const failed = markFailure(item("1", { attempts: MAX_ATTEMPTS }), "still down", false);
    expect(failed.state).toBe("failed");
  });

  it("markFailure is terminal immediately for a permanent refusal", () => {
    /*
     * Retrying a message the server rejected on its merits only produces the
     * same rejection three times — and every attempt is a chance to duplicate.
     */
    const failed = markFailure(item("1", { attempts: 1 }), "550 no such user", true);
    expect(failed.state).toBe("failed");
  });

  it("retryItem resets the budget, because the human is new information", () => {
    const retried = retryItem(item("1", { state: "failed", attempts: 9, lastError: "x" }));
    expect(retried).toMatchObject({ state: "queued", attempts: 0, lastError: undefined });
  });
});

describe("drainable", () => {
  it("takes queued items in FIFO order", () => {
    // A reply must not overtake the message it answers.
    const items = [item("3"), item("1"), item("2")];
    expect(drainable(items).map((entry) => entry.id)).toEqual(["1", "2", "3"]);
  });

  it("skips items already being sent", () => {
    // The state exists precisely so two drains cannot send one message twice.
    expect(drainable([item("1", { state: "sending" })])).toEqual([]);
  });

  it("does not automatically retry a failed item", () => {
    expect(drainable([item("1", { state: "failed" })])).toEqual([]);
  });
});

describe("the sidebar's view of the queue", () => {
  it("shows the folder only when non-empty (Gmail's shape)", () => {
    expect(showsOutbox([])).toBe(false);
    expect(showsOutbox([item("1")])).toBe(true);
  });

  it("counts everything that has not gone yet", () => {
    const items = [item("1"), item("2", { state: "sending" }), item("3", { state: "failed" })];
    expect(pendingCount(items)).toBe(2);
    expect(hasFailures(items)).toBe(true);
  });
});

describe("drainOutbox", () => {
  const sent = (): SendAttempt => ({ kind: "sent" });

  it("sends every queued item in order and reports them", async () => {
    const order: string[] = [];
    const transport = vi.fn((entry: OutboxItem): Promise<SendAttempt> => {
      order.push(entry.id);
      return Promise.resolve(sent());
    });

    const result = await drainOutbox([item("2"), item("1")], transport, () => undefined);

    expect(order).toEqual(["1", "2"]);
    expect(result.sent).toEqual(["1", "2"]);
    expect(result.remaining).toEqual([]);
  });

  it("persists the sending state before the transport runs", async () => {
    const states: string[] = [];
    await drainOutbox([item("1")], () => Promise.resolve(sent()), (entry) => {
      states.push(entry.state);
    });
    // Crash-safety: an item mid-flight is recorded as such, so a reload can
    // reclaim it rather than sending it twice.
    expect(states).toEqual(["sending"]);
  });

  it("STOPS at the first transient failure", async () => {
    /*
     * The `online` event fires on a captive portal. Marching through the rest
     * of the queue in that state burns every item's budget and turns a blip
     * into five permanently failed messages.
     */
    const tried: string[] = [];
    const transport = (entry: OutboxItem): Promise<SendAttempt> => {
      tried.push(entry.id);
      return Promise.resolve({ kind: "failed", error: "network", permanent: false });
    };

    const result = await drainOutbox(
      [item("1"), item("2"), item("3")],
      transport,
      () => undefined,
    );

    expect(tried).toEqual(["1"]);
    expect(result.sent).toEqual([]);
    expect(result.failed).toEqual([]);
    expect(result.remaining).toEqual(["1", "2", "3"]);
  });

  it("does NOT stop at a permanent failure — that item is the problem", async () => {
    const transport = (entry: OutboxItem): Promise<SendAttempt> =>
      Promise.resolve(
        entry.id === "1"
          ? { kind: "failed", error: "550 no such user", permanent: true }
          : sent(),
      );

    const result = await drainOutbox([item("1"), item("2")], transport, () => undefined);

    expect(result.failed).toEqual(["1"]);
    expect(result.sent).toEqual(["2"]);
  });

  it("treats a thrown transport as a transient failure rather than crashing", async () => {
    const result = await drainOutbox(
      [item("1")],
      () => {
        throw new Error("fetch exploded");
      },
      () => undefined,
    );

    expect(result.sent).toEqual([]);
    expect(result.remaining).toEqual(["1"]);
  });

  it("marks an item failed once it exhausts its attempts, and keeps going", async () => {
    const recorded: OutboxItem[] = [];
    const result = await drainOutbox(
      [item("1", { attempts: MAX_ATTEMPTS }), item("2")],
      (entry) =>
        Promise.resolve(
          entry.id === "1"
            ? { kind: "failed", error: "timeout", permanent: false }
            : sent(),
        ),
      (entry) => {
        recorded.push(entry);
      },
    );

    // Terminal, so the drain continues rather than stopping on a dead item.
    expect(result.failed).toEqual(["1"]);
    expect(result.sent).toEqual(["2"]);
    // And it is never dropped: the last recorded state for it is `failed`.
    const last = recorded.filter((entry) => entry.id === "1").pop();
    expect(last?.state).toBe("failed");
    expect(last?.lastError).toBe("timeout");
  });

  it("does nothing at all with an empty queue", async () => {
    const transport = vi.fn();
    const result = await drainOutbox([], transport, () => undefined);
    expect(transport).not.toHaveBeenCalled();
    expect(result).toEqual({ sent: [], failed: [], remaining: [] });
  });
});
