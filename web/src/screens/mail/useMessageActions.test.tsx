import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { JmapClient } from "../../api/jmap";
import { KEYWORD_SEEN, type Email } from "../../mail/types";
import { useMessageActions } from "./useMessageActions";

/**
 * The optimistic cycle end to end: paint, call, confirm or roll back.
 *
 * The maths is tested in `mail/actions.ts`; what is proved here is the SHELL —
 * that the paint happens before the round trip resolves, that a failure
 * restores exactly what it changed and nothing else, and that a half-failed
 * batch keeps the half that worked.
 */

function email(id: string, overrides: Partial<Email> = {}): Email {
  return {
    id,
    mailboxIds: { inbox: true },
    keywords: {},
    subject: id,
    receivedAt: "2026-08-20T10:00:00Z",
    ...overrides,
  };
}

/** A client whose response is resolved by the test, so timing is controllable. */
function deferredClient() {
  let release: (body: Record<string, unknown>) => void = () => undefined;
  const gate = new Promise<Record<string, unknown>>((resolve) => {
    release = resolve;
  });

  const fetchImpl = vi.fn(async () => {
    const body = await gate;
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }) as unknown as typeof fetch;

  return {
    client: new JmapClient({ username: "u", password: "p" }, { fetchImpl }),
    release: (methodResponses: [string, Record<string, unknown>, string][]) => {
      release({ methodResponses, sessionState: "s" });
    },
  };
}

/** A client that answers immediately with a scripted response. */
function instantClient(args: Record<string, unknown>) {
  const fetchImpl = vi.fn(
    () =>
      Promise.resolve(
        new Response(JSON.stringify({ methodResponses: [["Email/set", args, "s"]], sessionState: "s" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
  ) as unknown as typeof fetch;
  return new JmapClient({ username: "u", password: "p" }, { fetchImpl });
}

/** A client whose transport fails outright. */
function brokenClient(message: string) {
  const fetchImpl = vi.fn(() => Promise.reject(new TypeError(message))) as unknown as typeof fetch;
  return new JmapClient({ username: "u", password: "p" }, { fetchImpl });
}

describe("the optimistic paint", () => {
  /*
   * The premise of the whole design (ADR §6): the change is visible BEFORE the
   * server answers. If this fails, every action costs the pilot's ~530 ms
   * transatlantic round trip.
   */
  it("shows the change before the server has answered", async () => {
    const { client, release } = deferredClient();
    const emails = [email("e1")];

    const { result } = renderHook(() =>
      useMessageActions({ client, accountId: "a1", currentMailboxId: "inbox" }),
    );

    let pending: Promise<unknown> = Promise.resolve();
    act(() => {
      pending = result.current.run({ kind: "markRead", ids: ["e1"] }, emails);
    });

    // The request is in flight and unresolved…
    await waitFor(() => {
      expect(result.current.isBusy).toBe(true);
    });
    // …and the message ALREADY reads as seen.
    expect(result.current.project(emails)[0]?.keywords?.[KEYWORD_SEEN]).toBe(true);

    act(() => {
      release([["Email/set", { updated: { e1: null } }, "s"]]);
    });
    await act(async () => {
      await pending;
    });
  });

  it("removes an archived row from the list it is leaving", async () => {
    const client = instantClient({ updated: { e1: null } });
    const emails = [email("e1"), email("e2")];

    const { result } = renderHook(() =>
      useMessageActions({ client, accountId: "a1", currentMailboxId: "inbox" }),
    );

    await act(async () => {
      await result.current.run(
        { kind: "archive", ids: ["e1"], mailboxId: "mbArchive" },
        emails,
      );
    });
    expect(result.current.project(emails).map((message) => message.id)).toEqual(["e2"]);
  });

  /*
   * Once the server confirms, the patch is DROPPED rather than kept. Keeping
   * it would mask a later legitimate change — a message marked unread on a
   * phone, arriving by SSE — behind a stale optimistic value.
   */
  it("drops a confirmed keyword patch so later server truth is visible", async () => {
    const client = instantClient({ updated: { e1: null } });
    const emails = [email("e1")];

    const { result } = renderHook(() =>
      useMessageActions({ client, accountId: "a1", currentMailboxId: "inbox" }),
    );
    await act(async () => {
      await result.current.run({ kind: "markRead", ids: ["e1"] }, emails);
    });

    // A fresh list in which the server says the message is unread again wins.
    const fresh = [email("e1", { keywords: {} })];
    expect(result.current.project(fresh)[0]?.keywords?.[KEYWORD_SEEN]).toBeUndefined();
  });
});

describe("rollback", () => {
  /*
   * The rule: restore the message's ACTUAL prior state, not the negation of
   * the patch. A flag that was already set must stay set after a failed
   * "flag" — flipping it would corrupt state the user never touched.
   */
  it("restores the true prior state on a per-record failure", async () => {
    const client = instantClient({
      notUpdated: { e1: { type: "notFound", description: "no such message" } },
    });
    const alreadyRead = email("e1", { keywords: { [KEYWORD_SEEN]: true } });

    const { result } = renderHook(() =>
      useMessageActions({ client, accountId: "a1", currentMailboxId: "inbox" }),
    );

    let outcome;
    await act(async () => {
      outcome = await result.current.run({ kind: "markRead", ids: ["e1"] }, [alreadyRead]);
    });

    expect(result.current.project([alreadyRead])[0]?.keywords?.[KEYWORD_SEEN]).toBe(true);
    expect(outcome).toMatchObject({ failed: ["e1"], failureMessage: "no such message" });
  });

  it("puts an archived row back when the move fails", async () => {
    const client = instantClient({
      notUpdated: { e1: { type: "serverFail", description: "applying mailboxIds failed" } },
    });
    const emails = [email("e1"), email("e2")];

    const { result } = renderHook(() =>
      useMessageActions({ client, accountId: "a1", currentMailboxId: "inbox" }),
    );
    await act(async () => {
      await result.current.run({ kind: "archive", ids: ["e1"], mailboxId: "mbArchive" }, emails);
    });

    // Back in the list, and back in its original mailbox.
    const projected = result.current.project(emails);
    expect(projected.map((message) => message.id)).toEqual(["e1", "e2"]);
    expect(projected[0]?.mailboxIds).toEqual({ inbox: true });
  });

  /*
   * §5.3 gives per-record errors precisely so a batch can half succeed.
   * Rolling the whole batch back would undo messages the server DID change.
   */
  it("keeps the half that succeeded and restores only the half that failed", async () => {
    const client = instantClient({
      updated: { e1: null },
      notUpdated: { e2: { type: "notFound" } },
    });
    const emails = [email("e1"), email("e2")];

    const { result } = renderHook(() =>
      useMessageActions({ client, accountId: "a1", currentMailboxId: "inbox" }),
    );

    let outcome;
    await act(async () => {
      outcome = await result.current.run({ kind: "flag", ids: ["e1", "e2"] }, emails);
    });

    expect(outcome).toMatchObject({ succeeded: ["e1"], failed: ["e2"] });
    const projected = result.current.project(emails);
    expect(projected[1]?.keywords?.$flagged).toBeUndefined();
  });

  it("rolls everything back and names the reason when the transport fails", async () => {
    const client = brokenClient("Failed to fetch");
    const emails = [email("e1"), email("e2")];

    const { result } = renderHook(() =>
      useMessageActions({ client, accountId: "a1", currentMailboxId: "inbox" }),
    );

    let outcome;
    await act(async () => {
      outcome = await result.current.run({ kind: "markRead", ids: ["e1", "e2"] }, emails);
    });

    expect(outcome).toMatchObject({ succeeded: [] });
    // Never a silent revert: the failure carries a real reason up.
    expect((outcome as unknown as { failureMessage?: string }).failureMessage).toBeTruthy();
    for (const message of result.current.project(emails)) {
      expect(message.keywords?.[KEYWORD_SEEN]).toBeUndefined();
    }
  });
});

describe("the calls it makes", () => {
  it("maps delete onto destroy, letting the SERVER own the W-A2 semantics", async () => {
    const requests: { methodCalls: [string, Record<string, unknown>, string][] }[] = [];
    const fetchImpl = vi.fn((_url: string, init?: RequestInit) => {
      requests.push(JSON.parse(typeof init?.body === "string" ? init.body : "{}"));
      return Promise.resolve(
        new Response(JSON.stringify({ methodResponses: [["Email/set", { destroyed: ["e1"] }, "s"]], sessionState: "s" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }) as unknown as typeof fetch;
    const client = new JmapClient({ username: "u", password: "p" }, { fetchImpl });

    const { result } = renderHook(() =>
      useMessageActions({ client, accountId: "a1", currentMailboxId: "inbox" }),
    );
    await act(async () => {
      await result.current.run({ kind: "delete", ids: ["e1"] }, [email("e1")]);
    });

    // The first captured request may be the client's own session fetch, so the
    // Email/set is looked up by name rather than by position.
    const call = requests
      .flatMap((request) => request.methodCalls)
      .find((invocation) => invocation[0] === "Email/set");
    expect(call?.[1].destroy).toEqual(["e1"]);
    // NOT a client-side move to Trash: the server decides which of the two
    // semantics applies, and duplicating that rule here would drift from it.
    expect(call?.[1]).not.toHaveProperty("update");
  });

  it("makes no request for an empty target list", async () => {
    const fetchImpl = vi.fn() as unknown as typeof fetch;
    const client = new JmapClient({ username: "u", password: "p" }, { fetchImpl });

    const { result } = renderHook(() =>
      useMessageActions({ client, accountId: "a1", currentMailboxId: "inbox" }),
    );
    await act(async () => {
      await result.current.run({ kind: "markRead", ids: [] }, []);
    });
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("reset clears every pending patch", async () => {
    const { client, release } = deferredClient();
    const emails = [email("e1")];

    const { result } = renderHook(() =>
      useMessageActions({ client, accountId: "a1", currentMailboxId: "inbox" }),
    );
    let pending: Promise<unknown> = Promise.resolve();
    act(() => {
      pending = result.current.run({ kind: "markRead", ids: ["e1"] }, emails);
    });
    await waitFor(() => {
      expect(result.current.project(emails)[0]?.keywords?.[KEYWORD_SEEN]).toBe(true);
    });

    act(() => {
      result.current.reset();
    });
    expect(result.current.project(emails)).toBe(emails);

    act(() => {
      release([["Email/set", { updated: { e1: null } }, "s"]]);
    });
    await act(async () => {
      await pending;
    });
  });
});
