import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Email } from "../../mail/types";
import { OfflineProvider, useOffline } from "../../offline/OfflineProvider";
import { FakeIndexedDB, installKeyRange } from "../../test/fakeIndexedDB";
import { useAddressIndex, SCAN_CAP, SCAN_PAGE } from "./useAddressIndex";

/**
 * The address index as the screen uses it (E7, canon §2.3).
 *
 * The pure ranking is pinned in `mail/addressIndex.test.ts` and the persistence
 * in `offline/addressStore.test.ts`. What is left — and what only a mounted
 * hook can prove — is the LIFECYCLE: that the opt-out really stops the feeding
 * rather than only hiding the popup, that clearing empties the store, and that
 * the first-run scan is bounded and runs once.
 */

let restoreKeyRange: () => void;
/** The shim, installed as the global `indexedDB` the provider opens. */
let factory: FakeIndexedDB;

beforeEach(() => {
  restoreKeyRange = installKeyRange();
  factory = new FakeIndexedDB();
  vi.stubGlobal("indexedDB", factory);
  window.localStorage.clear();
});

afterEach(() => {
  restoreKeyRange();
  vi.unstubAllGlobals();
});

function email(id: string, from: string, to: readonly string[] = []): Email {
  return {
    id,
    blobId: `b-${id}`,
    threadId: `t-${id}`,
    mailboxIds: { mb1: true },
    keywords: {},
    from: [{ name: null, email: from }],
    to: to.map((address) => ({ name: null, email: address })),
    subject: "asunto",
    receivedAt: "2026-08-30T10:00:00Z",
    size: 100,
  };
}

/**
 * A probe that renders the index's observable state and exposes its verbs as
 * buttons, so the test drives the hook the way the screen does.
 */
function Probe({
  emails,
  fetchSentPage,
}: {
  readonly emails?: readonly Email[];
  readonly fetchSentPage?: (
    mailboxId: string,
    position: number,
    limit: number,
  ) => Promise<readonly Email[]>;
}): React.JSX.Element {
  const index = useAddressIndex({
    ownAddress: "yo@example.com",
    sentMailboxId: fetchSentPage === undefined ? undefined : "mbSent",
    fetchSentPage,
  });
  const { addresses: store } = useOffline();

  return (
    <div>
      <span data-testid="count">{index.count}</span>
      <span data-testid="enabled">{String(index.enabled)}</span>
      {/* The precondition `ready()` waits on — see its comment. */}
      <span data-testid="storeReady">{String(store !== undefined)}</span>
      <span data-testid="suggestions">
        {index.suggestions.map((entry) => entry.email).join(",")}
      </span>
      <button
        type="button"
        onClick={() => {
          index.record(emails ?? []);
        }}
      >
        record
      </button>
      <button
        type="button"
        onClick={() => {
          index.setEnabled(!index.enabled);
        }}
      >
        toggle
      </button>
      <button
        type="button"
        onClick={() => {
          void index.clear();
        }}
      >
        clear
      </button>
    </div>
  );
}

function renderProbe(props: React.ComponentProps<typeof Probe> = {}): void {
  render(
    <OfflineProvider accountId="a1">
      <Probe {...props} />
    </OfflineProvider>,
  );
}

/**
 * Waits for the provider's async database open to settle.
 *
 * It watches a flag the PROVIDER owns rather than `enabled`, and the distinction
 * is what this helper got wrong before prefs v2. `enabled` reads true on the
 * very first render — it always could, and now certainly does, since the
 * localStorage mirror answers before prefs resolve — so waiting on it proved
 * nothing about the store. The tests that record would then click through a
 * still-undefined store, `record` would return early exactly as it is designed
 * to, and the count would stay at zero perhaps one run in six.
 *
 * The probe therefore reports whether the address STORE exists, which is the
 * condition every recording case actually depends on — `record` returns early
 * without one, by design.
 */
async function ready(): Promise<void> {
  await waitFor(() => {
    expect(screen.getByTestId("storeReady")).toHaveTextContent("true");
  });
}

describe("useAddressIndex", () => {
  it("records the addresses of messages the app loaded", async () => {
    const user = userEvent.setup();
    renderProbe({ emails: [email("m1", "ana@x.com", ["bea@x.com"])] });
    await ready();

    await user.click(screen.getByRole("button", { name: "record" }));

    await waitFor(() => {
      expect(screen.getByTestId("count")).toHaveTextContent("2");
    });
    expect(screen.getByTestId("suggestions").textContent).toContain("ana@x.com");
    expect(screen.getByTestId("suggestions").textContent).toContain("bea@x.com");
  });

  it("never records the account's own address", async () => {
    const user = userEvent.setup();
    renderProbe({ emails: [email("m1", "yo@example.com", ["otra@x.com"])] });
    await ready();

    await user.click(screen.getByRole("button", { name: "record" }));

    await waitFor(() => {
      expect(screen.getByTestId("count")).toHaveTextContent("1");
    });
    expect(screen.getByTestId("suggestions")).toHaveTextContent("otra@x.com");
  });

  it("ranks by frequency, so the repeat correspondent leads", async () => {
    const user = userEvent.setup();
    renderProbe({ emails: [email("m1", "frecuente@x.com")] });
    await ready();

    // Three sightings of one address, then one of another.
    await user.click(screen.getByRole("button", { name: "record" }));
    await user.click(screen.getByRole("button", { name: "record" }));
    await user.click(screen.getByRole("button", { name: "record" }));

    await waitFor(() => {
      expect(screen.getByTestId("count")).toHaveTextContent("1");
    });
    expect(screen.getByTestId("suggestions")).toHaveTextContent("frecuente@x.com");
  });

  /*
   * The opt-out has to STOP THE FEEDING, not merely hide the popup. A setting
   * described as being about saving addresses that keeps saving them is a
   * false description.
   */
  it("stops recording once switched off", async () => {
    const user = userEvent.setup();
    renderProbe({ emails: [email("m1", "ana@x.com")] });
    await ready();

    await user.click(screen.getByRole("button", { name: "toggle" }));
    expect(screen.getByTestId("enabled")).toHaveTextContent("false");

    await user.click(screen.getByRole("button", { name: "record" }));

    // Nothing was stored, and nothing is suggested.
    await waitFor(() => {
      expect(screen.getByTestId("suggestions")).toHaveTextContent("");
    });
    expect(screen.getByTestId("count")).toHaveTextContent("0");
  });

  it("hides what it already collected while switched off, and offers it again after", async () => {
    const user = userEvent.setup();
    renderProbe({ emails: [email("m1", "ana@x.com")] });
    await ready();

    await user.click(screen.getByRole("button", { name: "record" }));
    await waitFor(() => {
      expect(screen.getByTestId("suggestions")).toHaveTextContent("ana@x.com");
    });

    await user.click(screen.getByRole("button", { name: "toggle" }));
    expect(screen.getByTestId("suggestions")).toHaveTextContent("");

    // Switching back on does NOT lose what was collected — the opt-out is a
    // pause, and erasing is the separate, explicit "delete" affordance.
    await user.click(screen.getByRole("button", { name: "toggle" }));
    expect(screen.getByTestId("suggestions")).toHaveTextContent("ana@x.com");
  });

  it("remembers the opt-out across mounts", async () => {
    const user = userEvent.setup();
    renderProbe();
    await ready();
    await user.click(screen.getByRole("button", { name: "toggle" }));

    expect(window.localStorage.getItem("moov.addressAutocomplete.v1")).toBe("off");
  });

  it("clear() really empties the store", async () => {
    const user = userEvent.setup();
    renderProbe({ emails: [email("m1", "ana@x.com", ["bea@x.com"])] });
    await ready();

    await user.click(screen.getByRole("button", { name: "record" }));
    await waitFor(() => {
      expect(screen.getByTestId("count")).toHaveTextContent("2");
    });

    await user.click(screen.getByRole("button", { name: "clear" }));

    await waitFor(() => {
      expect(screen.getByTestId("count")).toHaveTextContent("0");
    });
    expect(screen.getByTestId("suggestions")).toHaveTextContent("");
  });
});

describe("the first-run Sent scan", () => {
  it("walks Sent once and stores what it finds", async () => {
    const fetchSentPage = vi.fn(() =>
      Promise.resolve([email("s1", "cliente@x.com", ["yo@example.com"])]),
    );
    renderProbe({ fetchSentPage });
    await ready();

    await waitFor(() => {
      expect(screen.getByTestId("count")).toHaveTextContent("1");
    });
    expect(screen.getByTestId("suggestions")).toHaveTextContent("cliente@x.com");
    // One short page ends the walk: the mailbox is exhausted.
    expect(fetchSentPage).toHaveBeenCalledTimes(1);
  });

  it("does not run twice in the same browser", async () => {
    const fetchSentPage = vi.fn(() => Promise.resolve([email("s1", "cliente@x.com")]));
    renderProbe({ fetchSentPage });
    await waitFor(() => {
      expect(window.localStorage.getItem("moov.addressScan.v1")).toBe("1");
    });

    const callsAfterFirst = fetchSentPage.mock.calls.length;
    // A second mount — a reload, a route change — must not re-walk Sent.
    renderProbe({ fetchSentPage });
    await act(async () => {
      await Promise.resolve();
    });
    expect(fetchSentPage.mock.calls.length).toBe(callsAfterFirst);
  });

  /*
   * The bounded-loop discipline. A server that keeps returning full pages —
   * because it ignores `position`, or because the mailbox is enormous — must
   * not make this walk forever on the first screen after sign-in.
   */
  it("stops at the cap even when the server always returns a full page", async () => {
    const fullPage = Array.from({ length: SCAN_PAGE }, (_, n) =>
      email(`s${String(n)}`, `remitente${String(n)}@x.com`),
    );
    const fetchSentPage = vi.fn(() => Promise.resolve(fullPage));

    renderProbe({ fetchSentPage });
    await waitFor(() => {
      expect(window.localStorage.getItem("moov.addressScan.v1")).toBe("1");
    });

    expect(fetchSentPage).toHaveBeenCalledTimes(SCAN_CAP / SCAN_PAGE);
  });

  it("gives up quietly when the scan fails", async () => {
    const fetchSentPage = vi.fn(() => Promise.reject(new Error("500")));
    renderProbe({ fetchSentPage });

    await waitFor(() => {
      expect(fetchSentPage).toHaveBeenCalled();
    });
    await act(async () => {
      await Promise.resolve();
    });

    /*
     * No throw, and no retry loop hammering a server having a bad day: ONE
     * attempt, then it stops. The write-through feed still works, so
     * autocomplete simply has less to go on.
     */
    expect(fetchSentPage).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("count")).toHaveTextContent("0");
    // The scan is NOT marked done, so a later session may try again — a failed
    // walk is not a completed one.
    expect(window.localStorage.getItem("moov.addressScan.v1")).toBeNull();
  });

  it("does not scan while switched off", async () => {
    window.localStorage.setItem("moov.addressAutocomplete.v1", "off");
    const fetchSentPage = vi.fn(() => Promise.resolve([email("s1", "cliente@x.com")]));
    renderProbe({ fetchSentPage });

    await act(async () => {
      await Promise.resolve();
    });
    expect(fetchSentPage).not.toHaveBeenCalled();
  });
});
