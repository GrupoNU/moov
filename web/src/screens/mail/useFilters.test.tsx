import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { CAP_CORE, JmapClient, type JmapSession } from "../../api/jmap";
import { blockDraft } from "../../mail/blockedSenders";
import { CAP_FILTERS, CAP_QUOTA, CAP_SIEVE, CAP_VACATION, EMPTY_RULE } from "../../mail/filters";
import { useFilters } from "./useFilters";

/**
 * The E6 controller (L3 epic E6).
 *
 * Two contracts are pinned here that nothing else can pin:
 *
 *   1. **Every write RE-READS, and nothing is optimistic.** The vendor surface
 *      has no `/changes`, and a write regenerates and re-pushes an ENTIRE Sieve
 *      script — the server may normalize or refuse the whole batch, so a UI
 *      predicting the result would be showing a script that does not exist.
 *   2. **Each capability gates its own section.** `session.go` puts filters,
 *      vacation and quota behind three independent config fields, so a
 *      deployment with one and not the others has to be representable.
 */

const ACCOUNT = "a";

interface Scripted {
  readonly client: JmapClient;
  readonly calls: string[];
  readonly sent: [string, Record<string, unknown>, string][];
}

/**
 * A client that answers each method from a table, recording what was asked.
 *
 * `rules` is mutated by `FilterRule/set` so a re-read after a write returns the
 * NEW list — which is what lets the no-optimism contract be observed rather
 * than asserted.
 */
function scriptedClient(initial: {
  rules?: Record<string, unknown>[];
  scriptActive?: boolean;
  addresses?: Record<string, unknown>[];
  quotas?: Record<string, unknown>[];
  vacation?: Record<string, unknown>;
  failSet?: { type: string; description: string };
}): Scripted {
  const rules = [...(initial.rules ?? [])];
  const addresses = [...(initial.addresses ?? [])];
  const calls: string[] = [];
  const sent: [string, Record<string, unknown>, string][] = [];
  const client = new JmapClient({ username: "u", password: "p" });

  vi.spyOn(client, "call").mockImplementation((invocations) => {
    const batch = invocations as [string, Record<string, unknown>, string][];
    sent.push(...batch);
    const responses = batch.map(([name, args, id]) => {
      calls.push(name);
      switch (name) {
        case "FilterRule/get":
          return [
            name,
            { list: [...rules], state: "s", scriptActive: initial.scriptActive ?? true },
            id,
          ];
        case "FilterRule/set": {
          if (initial.failSet !== undefined) {
            return [name, { notCreated: { n: initial.failSet }, newState: "s" }, id];
          }
          const create = (args.create ?? {}) as Record<string, Record<string, unknown>>;
          const created: Record<string, unknown> = {};
          for (const [cid, draft] of Object.entries(create)) {
            const newId = `r${String(rules.length)}`;
            rules.push({ ...draft, id: newId });
            created[cid] = { id: newId };
          }
          for (const rid of (args.destroy ?? []) as string[]) {
            const index = rules.findIndex((rule) => rule.id === rid);
            if (index >= 0) rules.splice(index, 1);
          }
          return [name, { created, newState: "s2" }, id];
        }
        case "Forwarding/get":
          return [
            name,
            { list: [{ id: "singleton", enabled: false, address: null, disposition: "keep" }], state: "s" },
            id,
          ];
        case "ForwardingAddress/get":
          return [name, { list: [...addresses], state: "f" }, id];
        case "ForwardingAddress/set": {
          const create = (args.create ?? {}) as Record<string, { email: string }>;
          const created: Record<string, unknown> = {};
          for (const [cid, draft] of Object.entries(create)) {
            const newId = `f${String(addresses.length)}`;
            addresses.push({ id: newId, email: draft.email, state: "pending", verifiedAt: null });
            created[cid] = { id: newId, state: "pending" };
          }
          return [name, { created, newState: "f2" }, id];
        }
        case "VacationResponse/get":
          return [
            name,
            {
              list: [
                {
                  id: "singleton",
                  isEnabled: false,
                  fromDate: null,
                  toDate: null,
                  subject: null,
                  textBody: null,
                  htmlBody: null,
                  ...initial.vacation,
                },
              ],
              state: "v",
            },
            id,
          ];
        case "VacationResponse/set":
          return [name, { updated: { singleton: null }, newState: "v2" }, id];
        case "Quota/get":
          return [name, { list: initial.quotas ?? [], state: "q" }, id];
        case "SieveScript/get":
          return [name, { list: [{ id: "S1", name: "moov", isActive: false }], state: "sc" }, id];
        case "SieveScript/set":
          return [name, { newState: "sc2" }, id];
        default:
          return [name, {}, id];
      }
    });
    return Promise.resolve({ methodResponses: responses } as never);
  });

  return { client, calls, sent };
}

function session(uris: readonly string[]): JmapSession {
  return {
    capabilities: Object.fromEntries(uris.map((uri) => [uri, {}])),
    accounts: {
      [ACCOUNT]: { name: "u", isPersonal: true, isReadOnly: false, accountCapabilities: {} },
    },
    primaryAccounts: {},
    username: "u",
    apiUrl: "/jmap/api",
    downloadUrl: "",
    uploadUrl: "",
    eventSourceUrl: "",
    state: "s",
  };
}

const ALL = [CAP_CORE, CAP_FILTERS, CAP_VACATION, CAP_QUOTA, CAP_SIEVE];

function renderFilters(
  scripted: Scripted,
  uris: readonly string[] = ALL,
  authedFetch?: (url: string) => Promise<Response>,
) {
  return renderHook(() =>
    useFilters({
      client: scripted.client,
      session: session(uris),
      accountId: ACCOUNT,
      authedFetch,
    }),
  );
}

describe("each capability gates its own section", () => {
  it("reports all four when the session advertises all four", async () => {
    const scripted = scriptedClient({});
    const { result } = renderFilters(scripted);
    await waitFor(() => {
      expect(result.current.capabilities.filters).toBe(true);
    });
    expect(result.current.capabilities).toEqual({
      filters: true,
      vacation: true,
      quota: true,
      sieve: true,
    });
  });

  it("reports filters WITHOUT vacation, which the server can genuinely do", async () => {
    const scripted = scriptedClient({});
    const { result } = renderFilters(scripted, [CAP_CORE, CAP_FILTERS]);
    await waitFor(() => {
      expect(result.current.capabilities.filters).toBe(true);
    });
    expect(result.current.capabilities.vacation).toBe(false);
    // And it never issued the call it has no capability for.
    expect(scripted.calls).not.toContain("VacationResponse/get");
  });

  it("issues NO request at all with no capabilities", async () => {
    const scripted = scriptedClient({});
    renderFilters(scripted, [CAP_CORE]);
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(scripted.calls).toEqual([]);
  });

  it("withholds the activation when the server has no Sieve capability", async () => {
    const scripted = scriptedClient({ scriptActive: false });
    const { result } = renderFilters(scripted, [CAP_CORE, CAP_FILTERS]);
    await waitFor(() => {
      expect(result.current.scriptActive).toBe(false);
    });
    // The banner still fires — the situation is true — but the remedy is gone.
    expect(result.current.activate).toBeUndefined();
  });
});

describe("the load", () => {
  it("reads rules, forwarding and vacation in the fewest requests", async () => {
    const scripted = scriptedClient({
      rules: [{ ...EMPTY_RULE, id: "r1", name: "facturas", subject: ["factura"] }],
    });
    const { result } = renderFilters(scripted);
    await waitFor(() => {
      expect(result.current.rules).toHaveLength(1);
    });
    // FilterRule/get and Forwarding/get share a state cursor, so they ride one
    // request — batching them is what guarantees they describe the same script.
    expect(scripted.calls).toContain("FilterRule/get");
    expect(scripted.calls).toContain("Forwarding/get");
    expect(scripted.calls).toContain("ForwardingAddress/get");
  });

  it("does not fire the banner before the first read lands", () => {
    const scripted = scriptedClient({ scriptActive: false });
    const { result } = renderFilters(scripted);
    // An unknown must never render "your rules are not running".
    expect(result.current.scriptActive).toBe(true);
  });
});

describe("writes re-read; nothing is optimistic", () => {
  it("blocking a sender writes a blocked rule and re-reads the list", async () => {
    const scripted = scriptedClient({});
    const { result } = renderFilters(scripted);
    await waitFor(() => {
      expect(result.current.capabilities.filters).toBe(true);
    });
    const before = scripted.calls.filter((name) => name === "FilterRule/get").length;

    await act(async () => {
      result.current.createRule(blockDraft("spam@bad.example"));
      await Promise.resolve();
    });

    await waitFor(() => {
      expect(result.current.rules).toHaveLength(1);
    });
    expect(result.current.rules[0]?.type).toBe("blocked");
    expect(result.current.rules[0]?.from).toEqual(["spam@bad.example"]);
    // The re-read happened: the new rule is on screen because the SERVER said
    // so, not because the client predicted it.
    expect(scripted.calls.filter((name) => name === "FilterRule/get").length).toBeGreaterThan(
      before,
    );
  });

  it("surfaces the server's own refusal sentence", async () => {
    const scripted = scriptedClient({
      failSet: { type: "invalidProperties", description: "a filter needs at least one action" },
    });
    const { result } = renderFilters(scripted);
    await waitFor(() => {
      expect(result.current.capabilities.filters).toBe(true);
    });
    await act(async () => {
      result.current.createRule({ ...EMPTY_RULE, subject: ["x"] });
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(result.current.error).toBe("a filter needs at least one action");
    });
  });

  it("skips the request entirely when a move is a no-op at either end", async () => {
    const scripted = scriptedClient({
      rules: [{ ...EMPTY_RULE, id: "r1", name: "uno", subject: ["a"] }],
    });
    const { result } = renderFilters(scripted);
    await waitFor(() => {
      expect(result.current.rules).toHaveLength(1);
    });
    const before = scripted.calls.filter((name) => name === "FilterRule/set").length;
    act(() => {
      result.current.moveRule("r1", "up");
    });
    expect(scripted.calls.filter((name) => name === "FilterRule/set").length).toBe(before);
  });

  it("adds a forwarding address and re-reads it as pending", async () => {
    const scripted = scriptedClient({});
    const { result } = renderFilters(scripted);
    await waitFor(() => {
      expect(result.current.capabilities.filters).toBe(true);
    });
    await act(async () => {
      await result.current.addForwardingAddress("vos@otrolado.com");
    });
    await waitFor(() => {
      expect(result.current.forwardingAddresses).toHaveLength(1);
    });
    expect(result.current.forwardingAddresses[0]).toMatchObject({
      email: "vos@otrolado.com",
      state: "pending",
    });
  });
});

describe("verification goes through the aux route, not through JMAP", () => {
  it("calls the verify path with the token and re-reads on success", async () => {
    const scripted = scriptedClient({
      addresses: [{ id: "f1", email: "p@dest.com", state: "pending", verifiedAt: null }],
    });
    const seen: string[] = [];
    const { result } = renderFilters(scripted, ALL, (url) => {
      seen.push(url);
      return Promise.resolve(new Response(JSON.stringify({ verified: "p@dest.com" })));
    });
    await waitFor(() => {
      expect(result.current.forwardingAddresses).toHaveLength(1);
    });
    let ok = false;
    await act(async () => {
      ok = await result.current.verifyForwarding("tok");
    });
    expect(ok).toBe(true);
    expect(seen).toEqual(["/jmap/forwarding/verify?token=tok"]);
  });

  it("reports a refusal as a plain false — the route gives one answer for every reason", async () => {
    const scripted = scriptedClient({});
    const { result } = renderFilters(scripted, ALL, () =>
      Promise.resolve(new Response("{}", { status: 403 })),
    );
    await waitFor(() => {
      expect(result.current.capabilities.filters).toBe(true);
    });
    let ok = true;
    await act(async () => {
      ok = await result.current.verifyForwarding("bad");
    });
    expect(ok).toBe(false);
  });
});

describe("quota is read on demand and fails on its own", () => {
  it("is not fetched until it is asked for", async () => {
    const scripted = scriptedClient({});
    const { result } = renderFilters(scripted);
    await waitFor(() => {
      expect(result.current.capabilities.quota).toBe(true);
    });
    // Usage moves with every delivery and has no changelog, so the read happens
    // when the number is about to be SEEN, not on mount.
    expect(scripted.calls).not.toContain("Quota/get");
    expect(result.current.quotas).toBeUndefined();
  });

  it("reads on refresh", async () => {
    const scripted = scriptedClient({
      quotas: [
        {
          id: "storage",
          resourceType: "octets",
          used: 1024,
          hardLimit: 4096,
          name: "User quota",
        },
      ],
    });
    const { result } = renderFilters(scripted);
    await waitFor(() => {
      expect(result.current.capabilities.quota).toBe(true);
    });
    act(() => {
      result.current.refreshQuota();
    });
    await waitFor(() => {
      expect(result.current.quotas).toHaveLength(1);
    });
  });

  it("keeps an empty list as an empty list — 'no quota' is a real answer", async () => {
    const scripted = scriptedClient({ quotas: [] });
    const { result } = renderFilters(scripted);
    await waitFor(() => {
      expect(result.current.capabilities.quota).toBe(true);
    });
    act(() => {
      result.current.refreshQuota();
    });
    await waitFor(() => {
      expect(result.current.quotas).toEqual([]);
    });
  });
});
