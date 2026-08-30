import { describe, expect, it } from "vitest";

import {
  blockAdvice,
  blockDraft,
  blockedAddresses,
  blockedRules,
  isBlockableAddress,
  isBlocked,
} from "./blockedSenders";
import { parseFilterRule, type FilterRule } from "./filters";
import type { Email } from "./types";

/**
 * Blocked senders — the `type: "blocked"` slice (canon §2.2).
 *
 * The point these tests protect: "Bloqueados" is a VIEW of the one rule list,
 * not a second store. If the type tag stops being how the slice is taken, the
 * blocked list quietly starts showing filters — or, worse, the block button
 * starts writing rules the blocked section cannot see.
 */

function rule(overrides: Partial<FilterRule>): FilterRule {
  return {
    ...parseFilterRule({
      id: "r1",
      name: "",
      type: "filter",
      enabled: true,
      from: [],
      to: [],
      subject: [],
      sizeOver: 0,
      sizeUnder: 0,
      hasAttachment: null,
      moveTo: "",
      labels: [],
      markRead: false,
      star: false,
      forward: "",
      delete: false,
      stop: false,
    })!,
    ...overrides,
  };
}

const RULES: readonly FilterRule[] = [
  rule({ id: "r1", type: "filter", name: "facturas", subject: ["factura"] }),
  rule({ id: "r2", type: "blocked", name: "spam@bad.example", from: ["spam@bad.example"] }),
  rule({ id: "r3", type: "neverSpam", name: "banco", from: ["banco@ok.example"] }),
  rule({ id: "r4", type: "blocked", name: "otro@bad.example", from: ["Otro@Bad.Example"] }),
];

describe("the blocked slice is taken by the type tag", () => {
  it("returns only the blocked rules, in script order", () => {
    expect(blockedRules(RULES).map((r) => r.id)).toEqual(["r2", "r4"]);
  });

  it("does NOT include neverSpam — it is the opposite remedy", () => {
    expect(blockedRules(RULES).map((r) => r.type)).toEqual(["blocked", "blocked"]);
  });

  it("collects the addresses, lowercased for comparison", () => {
    expect([...blockedAddresses(RULES)].sort()).toEqual([
      "otro@bad.example",
      "spam@bad.example",
    ]);
  });

  it("answers is-blocked case-insensitively — a header's casing is not identity", () => {
    expect(isBlocked(RULES, "SPAM@BAD.EXAMPLE")).toBe(true);
    expect(isBlocked(RULES, "  otro@bad.example  ")).toBe(true);
    expect(isBlocked(RULES, "banco@ok.example")).toBe(false);
  });
});

describe("the address check mirrors the server's looksLikeAddress exactly", () => {
  it("accepts an ordinary address", () => {
    expect(isBlockableAddress("a@b.co")).toBe(true);
  });

  it("refuses what the model refuses", () => {
    expect(isBlockableAddress("")).toBe(false);
    expect(isBlockableAddress("no-at-sign")).toBe(false);
    expect(isBlockableAddress("@b.co")).toBe(false);
    expect(isBlockableAddress("a@")).toBe(false);
    expect(isBlockableAddress("a@b@c")).toBe(false);
    expect(isBlockableAddress("a b@c.d")).toBe(false);
    expect(isBlockableAddress("a\tb@c.d")).toBe(false);
  });

  it("refuses control characters — they would break the generated script", () => {
    expect(isBlockableAddress("a\n@b.co")).toBe(false);
    expect(isBlockableAddress("a\u007f@b.co")).toBe(false);
    expect(isBlockableAddress("a\r@b.co")).toBe(false);
  });
});

describe("the block draft", () => {
  it("normalizes the address and tags the rule blocked", () => {
    const draft = blockDraft("  SPAM@Bad.Example  ");
    expect(draft.type).toBe("blocked");
    expect(draft.from).toEqual(["spam@bad.example"]);
    expect(draft.name).toBe("spam@bad.example");
  });

  it("does NOT set stop — blocking one sender must not skip the other rules", () => {
    expect(blockDraft("a@b.co").stop).toBe(false);
  });

  it("sets no folder action — the server compiles the Junk filing from the type", () => {
    const draft = blockDraft("a@b.co");
    expect(draft.moveTo).toBe("");
    expect(draft.delete).toBe(false);
  });
});

describe("block is not unsubscribe (canon §2.2, stated on the same row)", () => {
  const withUnsubscribe: Email = {
    id: "m1",
    headers: [{ name: "List-Unsubscribe", value: "<https://list.example/u?k=1>" }],
  };
  const plain: Email = { id: "m2" };

  it("flags the unsubscribe route when the sender offers one", () => {
    expect(blockAdvice(withUnsubscribe, "News@List.Example")).toEqual({
      address: "news@list.example",
      hasUnsubscribe: true,
    });
  });

  it("does not invent one for an ordinary message", () => {
    expect(blockAdvice(plain, "a@b.co").hasUnsubscribe).toBe(false);
  });
});
