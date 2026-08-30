import { describe, expect, it } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";

import { OfflineProvider, useOffline } from "./OfflineProvider";

/**
 * The offline provider's degradation contract.
 *
 * This suite runs in jsdom, which has NO IndexedDB — so it exercises the exact
 * branch that the pilot's least-capable browser takes, and the one that must
 * never break anything: no cache, no outbox, and an app that behaves as it did
 * before this epic existed.
 *
 * The happy path (a real database, real rows) is covered against the in-memory
 * shim in `cache.test.ts` and `idb.test.ts`. What is proved HERE is the
 * absence case, which those cannot reach.
 */

/** Captured so a test can call the API the component received. */
let captured: ReturnType<typeof useOffline> | undefined;

function Probe(): React.JSX.Element {
  const offline = useOffline();
  captured = offline;
  return (
    <div>
      <span data-testid="ready">{String(offline.isReady)}</span>
      <span data-testid="online">{String(offline.isOnline)}</span>
      <span data-testid="cache">{offline.cache === undefined ? "none" : "present"}</span>
      <span data-testid="outbox">{offline.outbox === undefined ? "none" : "present"}</span>
      <span data-testid="items">{String(offline.outboxItems.length)}</span>
    </div>
  );
}

describe("OfflineProvider without IndexedDB", () => {
  it("settles as ready with no cache rather than hanging", async () => {
    /*
     * The failure this prevents: a provider that never resolves would leave
     * every consumer waiting on `isReady` forever, which presents as a mail
     * screen that never finishes loading — on a browser where everything
     * except the cache works perfectly.
     */
    render(
      <OfflineProvider accountId="acc">
        <Probe />
      </OfflineProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("ready")).toHaveTextContent("true");
    });
    expect(screen.getByTestId("cache")).toHaveTextContent("none");
    expect(screen.getByTestId("outbox")).toHaveTextContent("none");
    expect(screen.getByTestId("items")).toHaveTextContent("0");
  });

  it("does not even try to open a database before there is an account", () => {
    // The cache is scoped by account; opening one for "" would create rows
    // nothing could ever read back.
    render(
      <OfflineProvider accountId="">
        <Probe />
      </OfflineProvider>,
    );

    expect(screen.getByTestId("ready")).toHaveTextContent("false");
    expect(screen.getByTestId("cache")).toHaveTextContent("none");
  });

  it("reports the browser as online by default", async () => {
    render(
      <OfflineProvider accountId="acc">
        <Probe />
      </OfflineProvider>,
    );
    // Settle the async open before asserting, so its `setReady` does not land
    // after the test finishes.
    await waitFor(() => {
      expect(screen.getByTestId("ready")).toHaveTextContent("true");
    });
    expect(screen.getByTestId("online")).toHaveTextContent("true");
  });

  it("follows the browser's offline and online events", async () => {
    render(
      <OfflineProvider accountId="acc">
        <Probe />
      </OfflineProvider>,
    );
    await waitFor(() => {
      expect(screen.getByTestId("ready")).toHaveTextContent("true");
    });

    // `act` because the listener runs outside React's own event system: the
    // dispatch is what triggers the state update, so it is the thing to wrap.
    act(() => {
      window.dispatchEvent(new Event("offline"));
    });
    expect(screen.getByTestId("online")).toHaveTextContent("false");

    act(() => {
      window.dispatchEvent(new Event("online"));
    });
    expect(screen.getByTestId("online")).toHaveTextContent("true");
  });
});

describe("useOffline outside a provider", () => {
  it("returns an inert API rather than throwing", async () => {
    /*
     * Every consumer already handles "no storage", so an absent provider is the
     * same case. A hook that threw would let the offline layer take down a
     * screen that works perfectly well without it — including in any test that
     * renders a component in isolation.
     */
    render(<Probe />);

    expect(screen.getByTestId("ready")).toHaveTextContent("true");
    expect(screen.getByTestId("cache")).toHaveTextContent("none");
    expect(screen.getByTestId("items")).toHaveTextContent("0");

    // Every member is callable and none throws — the write-through helpers are
    // what the mail screen calls on EVERY fetch, so a throwing no-op here would
    // break the list rather than just the cache.
    expect(captured).toBeDefined();
    expect(() => {
      captured?.cacheMailboxes([]);
      captured?.cacheHeaders("inbox", []);
      captured?.cacheBody({ id: "m1" });
    }).not.toThrow();
    await expect(captured?.reloadOutbox()).resolves.toBeUndefined();
  });
});
