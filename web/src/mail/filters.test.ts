import { describe, expect, it, vi } from "vitest";

import { CAP_CORE, JmapClient, type JmapSession } from "../api/jmap";
import {
  activateManagedScript,
  CAP_FILTERS,
  CAP_QUOTA,
  CAP_SIEVE,
  CAP_VACATION,
  createFilterRule,
  createFilterRules,
  createForwardingAddress,
  destroyFilterRules,
  destroyForwardingAddress,
  e6Capabilities,
  exportFilters,
  EMPTY_RULE,
  fetchFilters,
  fetchForwardingAddresses,
  fetchQuota,
  fetchSieveScripts,
  fetchVacation,
  filtersExportFilename,
  forwardingVerifyUrl,
  parseFilterRule,
  parseFiltersExport,
  parseForwardingAddress,
  parseQuota,
  parseVacation,
  persistRuleOrder,
  reorderRules,
  ruleDraft,
  saveForwardAll,
  saveVacation,
  sessionHasCapability,
  storageQuota,
  updateFilterRule,
  verifyForwardingToken,
  type FilterRule,
} from "./filters";

/**
 * The E6 WIRE SHAPES (filters, forwarding, vacation, quota).
 *
 * The half that drifts silently is what is SENT: a `/set` that names the wrong
 * capability in `using` is an `unknownMethod` (RFC 8620 §1.8), a create that
 * spells a property differently is an `invalidProperties`, and neither shows up
 * in a rendering test. So each call here is pinned against the Go handler that
 * reads it — the property names below were taken from `filterRuleProperties`,
 * `forwardingProperties`, `forwardingAddressProperties`, `vacationProperties`
 * and `quotaProperties`.
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

function session(capabilities: Record<string, unknown>): JmapSession {
  return {
    capabilities,
    accounts: {
      [ACCOUNT]: {
        name: "u",
        isPersonal: true,
        isReadOnly: false,
        accountCapabilities: {},
      },
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

/** A rule exactly as `filterRuleObject` renders one — every property, in full. */
const WIRE_RULE = {
  id: "r0123456789ab",
  name: "facturas",
  type: "filter",
  enabled: true,
  from: ["contabilidad@proveedor.com"],
  to: [],
  subject: ["factura"],
  sizeOver: 0,
  sizeUnder: 0,
  hasAttachment: true,
  moveTo: "Facturas",
  labels: ["$label:work"],
  markRead: false,
  star: true,
  forward: "",
  delete: false,
  stop: true,
};

describe("feature detection — four capabilities, probed separately", () => {
  it("is false without a session", () => {
    expect(sessionHasCapability(undefined, ACCOUNT, CAP_FILTERS)).toBe(false);
  });

  it("accepts a capability advertised at the session level", () => {
    expect(sessionHasCapability(session({ [CAP_FILTERS]: {} }), ACCOUNT, CAP_FILTERS)).toBe(
      true,
    );
  });

  it("accepts one advertised only on the account (RFC 8620 §2's other map)", () => {
    const s = session({});
    const withAccount: JmapSession = {
      ...s,
      accounts: {
        [ACCOUNT]: { ...s.accounts[ACCOUNT]!, accountCapabilities: { [CAP_QUOTA]: {} } },
      },
    };
    expect(sessionHasCapability(withAccount, ACCOUNT, CAP_QUOTA)).toBe(true);
  });

  it("survives a session whose account map is missing entirely", () => {
    const s = session({});
    const broken: JmapSession = { ...s, accounts: {} };
    expect(() => sessionHasCapability(broken, ACCOUNT, CAP_FILTERS)).not.toThrow();
    expect(sessionHasCapability(broken, ACCOUNT, CAP_FILTERS)).toBe(false);
  });

  it("reports each E6 section independently — one capability does not imply another", () => {
    const caps = e6Capabilities(
      session({ [CAP_FILTERS]: {}, [CAP_QUOTA]: {} }),
      ACCOUNT,
    );
    expect(caps).toEqual({ filters: true, vacation: false, quota: true, sieve: false });
  });

  it("names the four URIs the server registers them under", () => {
    expect(CAP_FILTERS).toBe("https://moov.email/ns/filters");
    expect(CAP_VACATION).toBe("urn:ietf:params:jmap:vacationresponse");
    expect(CAP_QUOTA).toBe("urn:ietf:params:jmap:quota");
    expect(CAP_SIEVE).toBe("urn:ietf:params:jmap:sieve");
  });
});

describe("FilterRule/get — the rules and the honesty bit", () => {
  it("sends one request with both gets, naming the vendor capability", async () => {
    const { client, sent, usingLog } = stub({
      r: { list: [WIRE_RULE], state: "s1", scriptActive: true },
      f: { list: [{ id: "singleton", enabled: false, address: null, disposition: "keep" }] },
    });
    await fetchFilters(client, ACCOUNT);
    expect(sent.map(([name]) => name)).toEqual(["FilterRule/get", "Forwarding/get"]);
    expect(sent[0]?.[1]).toEqual({ accountId: ACCOUNT, ids: null });
    expect(usingLog[0]).toEqual([CAP_CORE, CAP_FILTERS]);
  });

  it("round-trips every property of the vendor rule object", async () => {
    const { client } = stub({
      r: { list: [WIRE_RULE], state: "s1", scriptActive: true },
      f: { list: [] },
    });
    const config = await fetchFilters(client, ACCOUNT);
    expect(config.rules).toEqual([
      {
        id: "r0123456789ab",
        name: "facturas",
        type: "filter",
        enabled: true,
        from: ["contabilidad@proveedor.com"],
        to: [],
        subject: ["factura"],
        sizeOver: 0,
        sizeUnder: 0,
        hasAttachment: true,
        moveTo: "Facturas",
        labels: ["$label:work"],
        markRead: false,
        star: true,
        forward: "",
        delete: false,
        stop: true,
      } satisfies FilterRule,
    ]);
    expect(config.state).toBe("s1");
  });

  it("reads scriptActive:false — the case the banner exists for", async () => {
    const { client } = stub({
      r: { list: [], state: "s", scriptActive: false },
      f: { list: [] },
    });
    expect((await fetchFilters(client, ACCOUNT)).scriptActive).toBe(false);
  });

  it("defaults a MISSING scriptActive to true, so an older server raises no false alarm", async () => {
    const { client } = stub({ r: { list: [], state: "s" }, f: { list: [] } });
    expect((await fetchFilters(client, ACCOUNT)).scriptActive).toBe(true);
  });

  it("reads hasAttachment:null as unset rather than as false", () => {
    const rule = parseFilterRule({ ...WIRE_RULE, hasAttachment: null });
    expect(rule?.hasAttachment).toBeNull();
  });

  it("refuses an object with no id — an unaddressable rule is not a rule", () => {
    expect(parseFilterRule({ name: "x" })).toBeUndefined();
    expect(parseFilterRule(null)).toBeUndefined();
  });

  it("reads the forward-all singleton, defaulting an empty disposition to keep", async () => {
    const { client } = stub({
      r: { list: [], state: "s", scriptActive: true },
      f: { list: [{ id: "singleton", enabled: true, address: "x@y.z", disposition: "" }] },
    });
    const config = await fetchFilters(client, ACCOUNT);
    expect(config.forwardAll).toEqual({
      enabled: true,
      address: "x@y.z",
      disposition: "keep",
    });
  });
});

describe("FilterRule/set", () => {
  it("creates under the creation id the response is read by", async () => {
    const { client, sent } = stub({ s: { created: { n: { id: "rabc" } }, newState: "s2" } });
    const outcome = await createFilterRule(client, ACCOUNT, {
      ...EMPTY_RULE,
      name: "n",
      subject: ["hola"],
      star: true,
    });
    expect(sent[0]?.[0]).toBe("FilterRule/set");
    const args = sent[0]?.[1] as { create: Record<string, unknown> };
    expect(args.create.n).toMatchObject({ name: "n", subject: ["hola"], star: true });
    expect(outcome.created.n).toEqual({ id: "rabc" });
  });

  it("never sends an id in a create — the server refuses one", async () => {
    const { client, sent } = stub({ s: {} });
    await createFilterRule(client, ACCOUNT, ruleDraft(parseFilterRule(WIRE_RULE)!));
    const args = sent[0]?.[1] as { create: Record<string, Record<string, unknown>> };
    expect(args.create.n).not.toHaveProperty("id");
  });

  it("updates by id with a partial patch", async () => {
    const { client, sent } = stub({ s: { updated: { rabc: null } } });
    await updateFilterRule(client, ACCOUNT, "rabc", { enabled: false });
    expect(sent[0]?.[1]).toEqual({ accountId: ACCOUNT, update: { rabc: { enabled: false } } });
  });

  it("destroys by id list", async () => {
    const { client, sent } = stub({ s: { destroyed: ["rabc"] } });
    const outcome = await destroyFilterRules(client, ACCOUNT, ["rabc"]);
    expect(sent[0]?.[1]).toEqual({ accountId: ACCOUNT, destroy: ["rabc"] });
    expect(outcome.destroyed).toEqual(["rabc"]);
  });

  it("surfaces a server refusal as a per-record failure, with its own sentence", async () => {
    const { client } = stub({
      s: {
        notCreated: {
          n: { type: "invalidProperties", description: "a filter needs at least one action" },
        },
      },
    });
    const outcome = await createFilterRule(client, ACCOUNT, EMPTY_RULE);
    expect(outcome.failed.n?.description).toBe("a filter needs at least one action");
  });
});

describe("rule order — Sieve is sequential, so order is configuration", () => {
  const rules: readonly FilterRule[] = [
    { ...parseFilterRule(WIRE_RULE)!, id: "r1", name: "one" },
    { ...parseFilterRule(WIRE_RULE)!, id: "r2", name: "two" },
    { ...parseFilterRule(WIRE_RULE)!, id: "r3", name: "three" },
  ];

  it("moves a rule up", () => {
    expect(reorderRules(rules, "r2", "up").map((r) => r.name)).toEqual([
      "two",
      "one",
      "three",
    ]);
  });

  it("moves a rule down", () => {
    expect(reorderRules(rules, "r2", "down").map((r) => r.name)).toEqual([
      "one",
      "three",
      "two",
    ]);
  });

  it("returns the SAME array at either end, so a caller can skip the request", () => {
    expect(reorderRules(rules, "r1", "up")).toBe(rules);
    expect(reorderRules(rules, "r3", "down")).toBe(rules);
    expect(reorderRules(rules, "nope", "up")).toBe(rules);
  });

  it("persists a swap as content updates on the two affected positions", async () => {
    const { client, sent } = stub({ s: { updated: { r1: null, r2: null } } });
    const after = reorderRules(rules, "r2", "up");
    await persistRuleOrder(client, ACCOUNT, rules, after);
    const args = sent[0]?.[1] as { update: Record<string, { name: string }> };
    // Position 0 now carries "two"'s content, position 1 carries "one"'s.
    expect(args.update.r1?.name).toBe("two");
    expect(args.update.r2?.name).toBe("one");
    // The unmoved third rule is not rewritten.
    expect(args.update.r3).toBeUndefined();
  });

  it("issues no request at all when nothing moved", async () => {
    const { client, sent } = stub({ s: {} });
    await persistRuleOrder(client, ACCOUNT, rules, rules);
    expect(sent).toEqual([]);
  });
});

describe("Forwarding/set — the forward-all singleton", () => {
  it("patches the singleton by its wire id", async () => {
    const { client, sent } = stub({ s: { updated: { singleton: null } } });
    await saveForwardAll(client, ACCOUNT, { enabled: true, address: "x@y.z" });
    expect(sent[0]?.[1]).toEqual({
      accountId: ACCOUNT,
      update: { singleton: { enabled: true, address: "x@y.z" } },
    });
  });

  it("carries the archive disposition the canon's second option needs", async () => {
    const { client, sent } = stub({ s: {} });
    await saveForwardAll(client, ACCOUNT, { disposition: "archive" });
    const args = sent[0]?.[1] as { update: Record<string, { disposition: string }> };
    expect(args.update.singleton?.disposition).toBe("archive");
  });
});

describe("ForwardingAddress — the verification state machine", () => {
  it("lists addresses with their state and verification instant", async () => {
    const { client } = stub({
      a: {
        list: [
          { id: "f1", email: "a@b.c", state: "accepted", verifiedAt: "2026-08-30T10:00:00Z" },
          { id: "f2", email: "d@e.f", state: "pending", verifiedAt: null },
        ],
      },
    });
    const addresses = await fetchForwardingAddresses(client, ACCOUNT);
    expect(addresses).toEqual([
      { id: "f1", email: "a@b.c", state: "accepted", verifiedAt: "2026-08-30T10:00:00Z" },
      { id: "f2", email: "d@e.f", state: "pending", verifiedAt: null },
    ]);
  });

  it("treats an UNKNOWN state as pending — the redirect gate must fail closed", () => {
    const address = parseForwardingAddress({ id: "f", email: "a@b.c", state: "weird" });
    expect(address?.state).toBe("pending");
  });

  it("creates with just an email — the token and the mail are the server's job", async () => {
    const { client, sent } = stub({
      s: { created: { n: { id: "f3", state: "pending" } } },
    });
    const outcome = await createForwardingAddress(client, ACCOUNT, "new@dest.com");
    expect(sent[0]?.[1]).toEqual({
      accountId: ACCOUNT,
      create: { n: { email: "new@dest.com" } },
    });
    expect(outcome.created.n).toEqual({ id: "f3", state: "pending" });
  });

  it("surfaces the in-use refusal verbatim — it names the fix", async () => {
    const { client } = stub({
      s: {
        notDestroyed: {
          f1: {
            type: "forbidden",
            description:
              "the address is still used by a filter or the forwarding setting; remove that first",
          },
        },
      },
    });
    const outcome = await destroyForwardingAddress(client, ACCOUNT, "f1");
    expect(outcome.failed.f1?.type).toBe("forbidden");
    expect(outcome.failed.f1?.description).toContain("remove that first");
  });

  it("builds the verify URL the aux route reads (?token=…, encoded)", () => {
    expect(forwardingVerifyUrl("a b/c")).toBe("/jmap/forwarding/verify?token=a%20b%2Fc");
  });

  it("consumes a token and returns the address the server confirmed", async () => {
    const seen: string[] = [];
    const result = await verifyForwardingToken((url) => {
      seen.push(url);
      return Promise.resolve(
        new Response(JSON.stringify({ verified: "new@dest.com" }), { status: 200 }),
      );
    }, "tok");
    expect(seen).toEqual(["/jmap/forwarding/verify?token=tok"]);
    expect(result.verified).toBe("new@dest.com");
  });

  it("throws on the single 403 refusal — bad, expired and foreign are one answer", async () => {
    await expect(
      verifyForwardingToken(
        () => Promise.resolve(new Response("{}", { status: 403 })),
        "tok",
      ),
    ).rejects.toThrow(/403/);
  });
});

describe("VacationResponse (RFC 8621 §8)", () => {
  it("reads the singleton, mapping absent fields to null", async () => {
    const { client, usingLog } = stub({
      v: {
        list: [
          {
            id: "singleton",
            isEnabled: true,
            fromDate: "2026-09-01T03:00:00Z",
            toDate: null,
            subject: "Fuera",
            textBody: "Vuelvo el lunes",
            htmlBody: null,
          },
        ],
        state: "v1",
      },
    });
    const { vacation, state } = await fetchVacation(client, ACCOUNT);
    expect(vacation).toEqual({
      isEnabled: true,
      fromDate: "2026-09-01T03:00:00Z",
      toDate: null,
      subject: "Fuera",
      textBody: "Vuelvo el lunes",
      htmlBody: null,
    });
    expect(state).toBe("v1");
    expect(usingLog[0]).toEqual([CAP_CORE, CAP_VACATION]);
  });

  it("gives a never-configured account the all-null disabled object", () => {
    expect(parseVacation(undefined).isEnabled).toBe(false);
    expect(parseVacation(undefined).subject).toBeNull();
  });

  it("patches by the singleton id with only what changed", async () => {
    const { client, sent } = stub({ s: { updated: { singleton: null } } });
    await saveVacation(client, ACCOUNT, { isEnabled: false });
    expect(sent[0]?.[1]).toEqual({
      accountId: ACCOUNT,
      update: { singleton: { isEnabled: false } },
    });
  });

  it("sends dates as UTCDate strings, which is what the server parses", async () => {
    const { client, sent } = stub({ s: {} });
    await saveVacation(client, ACCOUNT, {
      fromDate: "2026-09-01T03:00:00Z",
      toDate: "2026-09-15T02:59:59Z",
    });
    const args = sent[0]?.[1] as { update: Record<string, Record<string, unknown>> };
    expect(args.update.singleton).toEqual({
      fromDate: "2026-09-01T03:00:00Z",
      toDate: "2026-09-15T02:59:59Z",
    });
  });
});

describe("Quota/get (RFC 9425)", () => {
  it("reads the storage object", async () => {
    const { client, sent, usingLog } = stub({
      q: {
        list: [
          {
            id: "storage",
            resourceType: "octets",
            used: 1_073_741_824,
            hardLimit: 5_368_709_120,
            scope: "account",
            name: "User quota",
            types: ["Email"],
            warnLimit: null,
            softLimit: null,
            description: null,
          },
        ],
        state: "q1",
      },
    });
    const quotas = await fetchQuota(client, ACCOUNT);
    expect(sent[0]?.[1]).toEqual({ accountId: ACCOUNT, ids: null });
    expect(usingLog[0]).toEqual([CAP_CORE, CAP_QUOTA]);
    expect(storageQuota(quotas)).toEqual({
      id: "storage",
      resourceType: "octets",
      used: 1_073_741_824,
      hardLimit: 5_368_709_120,
      name: "User quota",
    });
  });

  it("an EMPTY list is 'no limit', not a zero-sized quota", async () => {
    const { client } = stub({ q: { list: [], state: "q0" } });
    const quotas = await fetchQuota(client, ACCOUNT);
    expect(quotas).toEqual([]);
    expect(storageQuota(quotas)).toBeUndefined();
  });

  it("refuses an object missing the two required numbers", () => {
    expect(parseQuota({ id: "storage", resourceType: "octets" })).toBeUndefined();
  });
});

describe("SieveScript — activation, the fix the banner offers", () => {
  it("lists scripts with their active flag", async () => {
    const { client, sent } = stub({
      s: { list: [{ id: "S1", name: "moov", isActive: false }, { id: "S2", name: "roundcube", isActive: true }] },
    });
    const scripts = await fetchSieveScripts(client, ACCOUNT);
    expect(sent[0]?.[1]).toMatchObject({ ids: null, properties: ["name", "isActive"] });
    expect(scripts).toEqual([
      { id: "S1", name: "moov", isActive: false },
      { id: "S2", name: "roundcube", isActive: true },
    ]);
  });

  it("activates with NO creations, updates or destructions — §2.4's precondition", async () => {
    const { client, sent, usingLog } = stub({ s: { newState: "s9" } });
    await activateManagedScript(client, ACCOUNT, "S1");
    expect(sent[0]?.[0]).toBe("SieveScript/set");
    expect(sent[0]?.[1]).toEqual({ accountId: ACCOUNT, onSuccessActivateScript: "S1" });
    // Sending a create/update/destroy would make `allOK` conditional; the whole
    // point is that it is vacuously true so the activation applies.
    expect(sent[0]?.[1]).not.toHaveProperty("create");
    expect(sent[0]?.[1]).not.toHaveProperty("update");
    expect(sent[0]?.[1]).not.toHaveProperty("destroy");
    expect(usingLog[0]).toEqual([CAP_CORE, CAP_SIEVE]);
  });
});

/**
 * Import and export (review F-42).
 *
 * The review called the absence a contradiction of positioning: Sieve is behind
 * these rules, so exporting them is trivial, and a webmail whose selling point
 * is that your mail is yours should not be the one place your rules are
 * trapped.
 *
 * What these pin is the pair of properties that make the feature worth having:
 * a ROUND TRIP that survives (export then import gives back the same rules),
 * and a REFUSAL that names its reason for every file it will not read. A parser
 * that silently dropped a field would be worse than no import at all — the
 * user would have a filter that looks right and behaves differently.
 */
describe("filters import/export (F-42)", () => {
  const rule = parseFilterRule(WIRE_RULE)!;
  const second = parseFilterRule({
    ...WIRE_RULE,
    id: "rffffffffffff",
    name: "Second",
    subject: ["urgente"],
  })!;

  it("round-trips a rule set through the file, in order", () => {
    const doc = exportFilters([rule, second]);
    const result = parseFiltersExport(JSON.stringify(doc));

    expect(result.problem).toBeUndefined();
    // Exactly what `FilterRule/set` accepts: the wire object minus the
    // server-set id, which is why the import path needs no translation layer.
    expect(result.rules).toEqual([ruleDraft(rule), ruleDraft(second)]);
    // Order is configuration on this surface (Sieve runs top to bottom), so
    // the array's order is the meaningful part of the document.
    expect(result.rules?.[0]?.name).toBe(rule.name);
    expect(result.rules?.[1]?.name).toBe("Second");
  });

  it("strips the server-set ids, so the file is portable rather than account-bound", () => {
    const doc = exportFilters([rule]);
    expect(JSON.stringify(doc)).not.toContain(rule.id);
    expect(doc.kind).toBe("moov.filters");
  });

  it("names the file with the date, so two exports do not overwrite each other", () => {
    expect(filtersExportFilename(new Date("2026-09-09T12:00:00Z"))).toBe(
      "moov-filtros-2026-09-09.json",
    );
  });

  it("refuses a file that is not JSON, and says which failure it was", () => {
    expect(parseFiltersExport("<?xml version=\"1.0\"?><feed/>").problem).toBe("notJson");
  });

  it("refuses valid JSON that is not our document — Gmail's export included", () => {
    expect(parseFiltersExport('{"feed":{"entry":[]}}').problem).toBe("notOurFormat");
    expect(parseFiltersExport("[]").problem).toBe("notOurFormat");
    expect(parseFiltersExport("null").problem).toBe("notOurFormat");
  });

  it("refuses a NEWER version rather than reading it optimistically", () => {
    // A field this build does not know would import with part of its behaviour
    // silently missing, which is worse than not importing it.
    const doc = { ...exportFilters([rule]), version: 99 };
    expect(parseFiltersExport(JSON.stringify(doc)).problem).toBe("futureVersion");
  });

  it("refuses an empty export rather than reporting a successful no-op", () => {
    expect(parseFiltersExport(JSON.stringify(exportFilters([]))).problem).toBe("noRules");
  });

  it("refuses the WHOLE file when one rule is malformed", () => {
    const doc = { ...exportFilters([rule]), rules: [ruleDraft(rule), "not a rule"] };
    // All-or-nothing: a half-applied set is a filtering configuration nobody
    // designed, and order-dependence means the half that landed can behave
    // differently from the half that was meant to be there.
    expect(parseFiltersExport(JSON.stringify(doc)).problem).toBe("badRule");
  });

  it("sends every imported rule in ONE /set, so the import is atomic", async () => {
    const { client, sent, usingLog } = stub({ s: { created: {} } });
    await createFilterRules(client, ACCOUNT, [ruleDraft(rule), ruleDraft(second)]);

    expect(sent).toHaveLength(1);
    expect(sent[0]?.[0]).toBe("FilterRule/set");
    // Positional creation ids: the server appends creates in order, so the
    // array's order becomes the script's order.
    expect(Object.keys(sent[0]?.[1].create as object)).toEqual(["n0", "n1"]);
    expect(usingLog[0]).toEqual([CAP_CORE, CAP_FILTERS]);
  });
});
