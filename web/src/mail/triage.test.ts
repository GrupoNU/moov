import { describe, expect, it, vi } from "vitest";

import { CAP_CORE, JmapClient, type JmapSession } from "../api/jmap";
import {
  CAP_TRIAGE,
  DEFAULT_SCHEDULE_LIMITS,
  fetchMutedThreadIds,
  fetchSnoozes,
  scheduleLimits,
  sessionHasTriage,
  setThreadsMuted,
  snoozeMailboxName,
  snoozeMessages,
  unsnoozeMessages,
} from "./triage";

/**
 * The triage WIRE SHAPES (L3 E4).
 *
 * These pin what is SENT and how the answer is read, because that is the half
 * that drifts silently: a `Snooze/set` sending `until` in the wrong format, a
 * `Mute/set` sending an update the server has no path for, or a call that
 * forgets to name the vendor capability in `using` — which RFC 8620 §1.8 makes
 * the difference between a working method and an `unknownMethod`.
 */

const ACCOUNT = "a";

function stub(responses: Record<string, unknown>) {
  const sent: [string, Record<string, unknown>, string][] = [];
  const usingLog: string[][] = [];
  const client = new JmapClient({ username: "u", password: "p" });
  vi.spyOn(client, "call").mockImplementation((invocations, using) => {
    const calls = invocations as [string, Record<string, unknown>, string][];
    sent.push(...calls);
    usingLog.push([...(using ?? [])]);
    return Promise.resolve({
      methodResponses: calls.map(([name, , id]) => [name, responses[id] ?? {}, id]),
    } as never);
  });
  return { client, sent, usingLog };
}

function session(overrides: Partial<JmapSession> = {}): JmapSession {
  return {
    capabilities: { [CAP_TRIAGE]: { snoozeMailboxName: "Snoozed" } },
    accounts: {
      [ACCOUNT]: {
        name: "u",
        isPersonal: true,
        isReadOnly: false,
        accountCapabilities: {
          [CAP_TRIAGE]: { maxScheduledSends: 100, maxDelayedSendSeconds: 2_592_000 },
        },
      },
    },
    primaryAccounts: {},
    username: "u",
    apiUrl: "/jmap/api",
    downloadUrl: "",
    uploadUrl: "",
    eventSourceUrl: "",
    state: "s",
    ...overrides,
  };
}

describe("feature detection", () => {
  it("is false without a session — no capability, no controls", () => {
    expect(sessionHasTriage(undefined, ACCOUNT)).toBe(false);
    expect(snoozeMailboxName(undefined)).toBeUndefined();
  });

  it("is false when the server does not advertise the vendor URI", () => {
    const bare = session({ capabilities: {} });
    const withoutAccount: JmapSession = {
      ...bare,
      accounts: {
        [ACCOUNT]: { name: "u", isPersonal: true, isReadOnly: false, accountCapabilities: {} },
      },
    };
    expect(sessionHasTriage(withoutAccount, ACCOUNT)).toBe(false);
  });

  it("accepts the capability advertised on the account alone", () => {
    const accountOnly = session({ capabilities: {} });
    expect(sessionHasTriage(accountOnly, ACCOUNT)).toBe(true);
  });

  it("reads the Snoozed folder's NAME from the session, never a constant", () => {
    // There is no RFC 6154 role for snoozed mail, so the name IS the contract
    // (internal/jmaphttp/session.go's triageCapability).
    const renamed = session({
      capabilities: { [CAP_TRIAGE]: { snoozeMailboxName: "Zzz" } },
    });
    expect(snoozeMailboxName(renamed)).toBe("Zzz");
  });

  it("falls back to Snoozed only when the capability carries no name", () => {
    const nameless = session({ capabilities: { [CAP_TRIAGE]: {} } });
    expect(snoozeMailboxName(nameless)).toBe("Snoozed");
  });
});

describe("scheduleLimits", () => {
  it("reads both numbers out of the TRIAGE account capability", () => {
    expect(scheduleLimits(session(), ACCOUNT)).toEqual({
      maxScheduledSends: 100,
      maxDelayedSendSeconds: 2_592_000,
    });
  });

  it("falls back to the canon's numbers when the session carries none", () => {
    expect(scheduleLimits(undefined, ACCOUNT)).toEqual(DEFAULT_SCHEDULE_LIMITS);
    expect(DEFAULT_SCHEDULE_LIMITS.maxScheduledSends).toBe(100);
    // 30 days — the server's MaxDelayedSend.
    expect(DEFAULT_SCHEDULE_LIMITS.maxDelayedSendSeconds).toBe(30 * 24 * 60 * 60);
  });
});

describe("Snooze/get", () => {
  it("asks for the whole set and names the vendor capability", async () => {
    const { client, sent, usingLog } = stub({
      s: { list: [{ id: "e1", emailId: "e1", until: "2026-09-01T08:00:00Z", originMailboxName: null }] },
    });
    const records = await fetchSnoozes(client, ACCOUNT);
    expect(sent[0]?.[0]).toBe("Snooze/get");
    expect(sent[0]?.[1]).toEqual({ accountId: ACCOUNT, ids: null });
    expect(usingLog[0]).toEqual([CAP_CORE, CAP_TRIAGE]);
    expect(records).toEqual([
      { id: "e1", until: "2026-09-01T08:00:00Z", originMailboxName: null },
    ]);
  });

  it("keeps a named origin folder and normalises anything else to null", async () => {
    const { client } = stub({
      s: {
        list: [
          { id: "e1", until: "2026-09-01T08:00:00Z", originMailboxName: "Trabajo" },
          { id: "e2", until: "2026-09-02T08:00:00Z", originMailboxName: null },
          { id: "e3", until: "2026-09-03T08:00:00Z" },
        ],
      },
    });
    const records = await fetchSnoozes(client, ACCOUNT);
    expect(records.map((r) => r.originMailboxName)).toEqual(["Trabajo", null, null]);
  });

  it("drops a record missing the fields the UI needs rather than rendering undefined", async () => {
    const { client } = stub({ s: { list: [{ id: "e1" }, { until: "2026-09-01T08:00:00Z" }] } });
    expect(await fetchSnoozes(client, ACCOUNT)).toEqual([]);
  });
});

describe("Snooze/set — create", () => {
  it("sends one create per message id, in ONE call", async () => {
    const { client, sent } = stub({ s: { created: { s0: { id: "e1" }, s1: { id: "e2" } } } });
    await snoozeMessages(client, ACCOUNT, ["e1", "e2"], "2026-09-01T08:00:00Z");
    expect(sent).toHaveLength(1);
    expect(sent[0]?.[0]).toBe("Snooze/set");
    expect(sent[0]?.[1]).toEqual({
      accountId: ACCOUNT,
      create: {
        s0: { emailId: "e1", until: "2026-09-01T08:00:00Z" },
        s1: { emailId: "e2", until: "2026-09-01T08:00:00Z" },
      },
    });
  });

  it("makes no request at all for an empty id list", async () => {
    const { client, sent } = stub({});
    const outcome = await snoozeMessages(client, ACCOUNT, [], "2026-09-01T08:00:00Z");
    expect(sent).toEqual([]);
    expect(outcome.failed).toEqual({});
  });

  it("surfaces a per-record refusal with the server's own words", async () => {
    const { client } = stub({
      s: {
        notCreated: {
          s0: { type: "forbidden", description: "mail: the Snoozed mailbox is unavailable" },
        },
      },
    });
    const outcome = await snoozeMessages(client, ACCOUNT, ["e1"], "2026-09-01T08:00:00Z");
    expect(outcome.failed.s0?.type).toBe("forbidden");
    expect(outcome.failed.s0?.description).toContain("Snoozed mailbox is unavailable");
  });
});

describe("Snooze/set — destroy (un-snooze)", () => {
  it("destroys by EMAIL id, which is the Snooze's own id", async () => {
    const { client, sent } = stub({ s: { destroyed: ["e1", "e2"] } });
    const outcome = await unsnoozeMessages(client, ACCOUNT, ["e1", "e2"]);
    expect(sent[0]?.[1]).toEqual({ accountId: ACCOUNT, destroy: ["e1", "e2"] });
    expect(outcome.destroyed).toEqual(["e1", "e2"]);
  });

  it("reports the server's notFound for a message that is not snoozed", async () => {
    const { client } = stub({
      s: { notDestroyed: { e1: { type: "notFound", description: "the message is not snoozed" } } },
    });
    const outcome = await unsnoozeMessages(client, ACCOUNT, ["e1"]);
    expect(outcome.failed.e1?.type).toBe("notFound");
  });
});

describe("Mute/get", () => {
  it("returns the muted THREAD ids as a set — the whole is:muted surface", async () => {
    const { client, sent } = stub({
      m: { list: [{ id: "t1", threadId: "t1" }, { id: "t7", threadId: "t7" }] },
    });
    const muted = await fetchMutedThreadIds(client, ACCOUNT);
    expect(sent[0]?.[0]).toBe("Mute/get");
    expect(sent[0]?.[1]).toEqual({ accountId: ACCOUNT, ids: null });
    expect([...muted].sort()).toEqual(["t1", "t7"]);
  });

  it("is an empty set when nothing is muted", async () => {
    const { client } = stub({ m: { list: [] } });
    expect((await fetchMutedThreadIds(client, ACCOUNT)).size).toBe(0);
  });
});

describe("Mute/set", () => {
  it("mutes with CREATE — the server has no update path for a binary fact", async () => {
    const { client, sent } = stub({ m: { created: { m0: { id: "t1" } } } });
    await setThreadsMuted(client, ACCOUNT, ["t1"], true);
    expect(sent[0]?.[1]).toEqual({ accountId: ACCOUNT, create: { m0: { threadId: "t1" } } });
    expect(sent[0]?.[1]).not.toHaveProperty("update");
  });

  it("unmutes with DESTROY", async () => {
    const { client, sent } = stub({ m: { destroyed: ["t1"] } });
    await setThreadsMuted(client, ACCOUNT, ["t1"], false);
    expect(sent[0]?.[1]).toEqual({ accountId: ACCOUNT, destroy: ["t1"] });
  });

  it("names the vendor capability on every triage call", async () => {
    const { client, usingLog } = stub({ m: {} });
    await setThreadsMuted(client, ACCOUNT, ["t1"], true);
    expect(usingLog[0]).toContain(CAP_TRIAGE);
  });

  it("makes no request for an empty list, in either direction", async () => {
    const { client, sent } = stub({});
    await setThreadsMuted(client, ACCOUNT, [], true);
    await setThreadsMuted(client, ACCOUNT, [], false);
    expect(sent).toEqual([]);
  });
});
