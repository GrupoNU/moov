import { describe, expect, it } from "vitest";

import {
  connectionState,
  showsConnectionPill,
  shouldRecycleStream,
  STALE_STREAM_MS,
} from "./connection";

describe("connectionState", () => {
  it("is online when the network is up and the stream is alive", () => {
    expect(connectionState({ online: true, streamDead: false })).toBe("online");
  });

  it("is reconnecting when only the stream is down", () => {
    // A server restart or a revoked token: the network is fine and this genuinely
    // heals itself, so the wording promises exactly that.
    expect(connectionState({ online: true, streamDead: true })).toBe("reconnecting");
  });

  it("reports offline even when the stream has not noticed yet", () => {
    // The browser's flag WINS: with no network nothing works, not just push,
    // and "reconnecting" would understate it.
    expect(connectionState({ online: false, streamDead: false })).toBe("offline");
    expect(connectionState({ online: false, streamDead: true })).toBe("offline");
  });
});

describe("showsConnectionPill", () => {
  it("shows nothing while everything works", () => {
    expect(showsConnectionPill("online")).toBe(false);
  });

  it("shows for both failure states", () => {
    expect(showsConnectionPill("offline")).toBe(true);
    expect(showsConnectionPill("reconnecting")).toBe(true);
  });
});

describe("shouldRecycleStream", () => {
  const now = 1_800_000_000_000;
  const base = {
    visibility: "visible" as const,
    streamDead: false,
    lastEventAt: now - 1_000,
    now,
    online: true,
  };

  it("does nothing when the tab is going hidden", () => {
    // Work for a tab nobody is looking at, which the OS is about to freeze.
    expect(shouldRecycleStream({ ...base, visibility: "hidden", streamDead: true })).toBe(
      false,
    );
  });

  it("does nothing while offline", () => {
    // A reconnect with no network burns a token mint and fails; the `online`
    // event is the right trigger for that case.
    expect(shouldRecycleStream({ ...base, online: false, streamDead: true })).toBe(false);
  });

  it("recycles a stream known to be dead", () => {
    // The frozen-timer case: onDead fired while backgrounded and nothing ran.
    expect(shouldRecycleStream({ ...base, streamDead: true })).toBe(true);
  });

  it("does not recycle a stream that spoke recently", () => {
    expect(shouldRecycleStream(base)).toBe(false);
  });

  it("recycles a stream silent past the threshold", () => {
    /*
     * The case with no error at all: iOS tore the socket down while the PWA was
     * suspended, and the EventSource still reports itself open.
     */
    expect(
      shouldRecycleStream({ ...base, lastEventAt: now - STALE_STREAM_MS - 1 }),
    ).toBe(true);
  });

  it("treats exactly the threshold as still fresh", () => {
    expect(shouldRecycleStream({ ...base, lastEventAt: now - STALE_STREAM_MS })).toBe(
      false,
    );
  });

  it("distrusts a stream that never delivered anything", () => {
    // No evidence it works, and the tab has been away.
    expect(shouldRecycleStream({ ...base, lastEventAt: 0 })).toBe(true);
  });
});
