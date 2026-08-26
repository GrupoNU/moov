import { describe, expect, it } from "vitest";

import { connectPush, type EventSourceLike, type StateChange } from "./push";

/**
 * connectPush wraps EventSource: parse state events, surface a dead
 * connection, close cleanly. A fake EventSource drives each claim.
 */

class FakeEventSource implements EventSourceLike {
  readonly url: string;
  readyState = 0;
  closed = false;
  private readonly listeners = new Map<string, ((event: MessageEvent) => void)[]>();

  constructor(url: string) {
    this.url = url;
  }

  addEventListener(type: string, listener: (event: MessageEvent) => void): void {
    const list = this.listeners.get(type) ?? [];
    list.push(listener);
    this.listeners.set(type, list);
  }

  close(): void {
    this.closed = true;
    this.readyState = 2;
  }

  emit(type: string, data?: string): void {
    for (const listener of this.listeners.get(type) ?? []) {
      listener({ data } as MessageEvent);
    }
  }
}

function harness(): {
  source: () => FakeEventSource;
  changes: StateChange[];
  deaths: number[];
  open: (url?: string) => ReturnType<typeof connectPush>;
} {
  let created: FakeEventSource | undefined;
  const changes: StateChange[] = [];
  const deaths: number[] = [];
  return {
    source: () => {
      if (created === undefined) throw new Error("no EventSource was created");
      return created;
    },
    changes,
    deaths,
    open: (url = "/jmap/eventsource?types=*&access_token=tok") =>
      connectPush({
        url,
        onStateChange: (change) => changes.push(change),
        onDead: () => deaths.push(1),
        eventSourceImpl: (u) => {
          created = new FakeEventSource(u);
          return created;
        },
      }),
  };
}

describe("connectPush", () => {
  it("opens the given URL (token included) and parses state events", () => {
    const h = harness();
    h.open("/jmap/eventsource?types=*&closeafter=no&ping=30&access_token=t0k");

    expect(h.source().url).toContain("access_token=t0k");

    h.source().emit(
      "state",
      JSON.stringify({
        "@type": "StateChange",
        changed: { a1: { Email: "em-2", Mailbox: "mb-3" } },
      }),
    );

    expect(h.changes).toHaveLength(1);
    expect(h.changes[0]?.changed.a1?.Email).toBe("em-2");
  });

  it("drops malformed frames without crashing or notifying", () => {
    const h = harness();
    h.open();

    h.source().emit("state", "{not json");
    h.source().emit("state", JSON.stringify({ noChanged: true }));
    h.source().emit("state", undefined);

    expect(h.changes).toHaveLength(0);
  });

  it("reports a dead connection only when the browser gave up", () => {
    const h = harness();
    h.open();

    // Transient: EventSource is reconnecting on its own (readyState 0/1).
    h.source().readyState = 0;
    h.source().emit("error");
    expect(h.deaths).toHaveLength(0);

    // Fatal: CLOSED — the token is dead; the token layer must heal it.
    h.source().readyState = 2;
    h.source().emit("error");
    expect(h.deaths).toHaveLength(1);
  });

  it("close() closes the source and silences later errors", () => {
    const h = harness();
    const handle = h.open();

    handle.close();
    expect(h.source().closed).toBe(true);

    // An error event after close must not call onDead: the caller closed on
    // purpose (token refresh), and a spurious onDead would mint in a loop.
    h.source().readyState = 2;
    h.source().emit("error");
    expect(h.deaths).toHaveLength(0);
  });
});
