import { describe, expect, it } from "vitest";

import {
  actionsSummary,
  criteriaSummary,
  formatBytes,
  hasNoActions,
  hasNoCriteria,
  isSafeScriptString,
  parseSize,
  validateFilterRule,
  type SummaryWords,
} from "./filterSummary";
import { EMPTY_RULE, parseFilterRule, type FilterRule, type FilterRuleDraft } from "./filters";

/**
 * Rule summaries and the builder's pre-flight validation (GC-4).
 *
 * These pin the two things that would otherwise be discovered by a user: a
 * summary line that silently omits a criterion (so the list says a rule does
 * less than it does), and a validation gap that lets the builder send something
 * `internal/sieve/model.go` will refuse.
 */

function draft(overrides: Partial<FilterRuleDraft> = {}): FilterRuleDraft {
  return { ...EMPTY_RULE, ...overrides };
}

function rule(overrides: Partial<FilterRule> = {}): FilterRule {
  return { ...parseFilterRule({ ...EMPTY_RULE, id: "r1" })!, ...overrides };
}

const WORDS: SummaryWords = {
  from: "De",
  to: "Para",
  subject: "Asunto",
  sizeOver: (b) => `mayor que ${b}`,
  sizeUnder: (b) => `menor que ${b}`,
  hasAttachment: "con adjunto",
  noAttachment: "sin adjunto",
  moveTo: (f) => `mover a ${f}`,
  label: (n) => `etiquetar ${n}`,
  markRead: "marcar leído",
  star: "destacar",
  forward: (a) => `reenviar a ${a}`,
  deleteAction: "eliminar",
  neverSpam: "nunca spam",
  stop: "detener",
  separator: " · ",
  empty: "—",
  formatBytes,
};

const VERIFIED: ReadonlySet<string> = new Set(["ok@dest.com"]);

describe("emptiness — the two checks the model applies", () => {
  it("sees an untouched draft as having neither criteria nor actions", () => {
    expect(hasNoCriteria(draft())).toBe(true);
    expect(hasNoActions(draft())).toBe(true);
  });

  it("counts hasAttachment:false as a REAL criterion, not as unset", () => {
    // The distinction the wire preserves with Boolean|null: "no attachment" is
    // a filter, "unset" is not. Treating false as empty would silently drop it.
    expect(hasNoCriteria(draft({ hasAttachment: false }))).toBe(false);
  });

  it("counts a size bound and a stop as non-empty", () => {
    expect(hasNoCriteria(draft({ sizeOver: 1 }))).toBe(false);
    expect(hasNoActions(draft({ stop: true }))).toBe(false);
  });
});

describe("validation mirrors the server, so the builder refuses before the round trip", () => {
  it("accepts a well-formed filter", () => {
    expect(
      validateFilterRule(draft({ subject: ["factura"], star: true }), VERIFIED),
    ).toEqual([]);
  });

  it("refuses a filter with no criterion", () => {
    expect(validateFilterRule(draft({ star: true }), VERIFIED)).toContain("noCriteria");
  });

  it("refuses a filter with no action", () => {
    expect(validateFilterRule(draft({ subject: ["x"] }), VERIFIED)).toContain("noActions");
  });

  it("does NOT demand an action of a neverSpam rule — its type IS the action", () => {
    const problems = validateFilterRule(
      draft({ type: "neverSpam", from: ["banco@ok.example"] }),
      VERIFIED,
    );
    expect(problems).toEqual([]);
  });

  it("refuses moveTo together with delete", () => {
    expect(
      validateFilterRule(
        draft({ subject: ["x"], moveTo: "Facturas", delete: true }),
        VERIFIED,
      ),
    ).toContain("moveAndDelete");
  });

  it("refuses a forward to an UNVERIFIED address — GC-4's whole point", () => {
    expect(
      validateFilterRule(draft({ subject: ["x"], forward: "nope@dest.com" }), VERIFIED),
    ).toContain("forwardUnverified");
  });

  it("accepts a forward to a verified one, case-insensitively", () => {
    expect(
      validateFilterRule(draft({ subject: ["x"], forward: "OK@Dest.com" }), VERIFIED),
    ).toEqual([]);
  });

  it("refuses a forward target that is not an address at all", () => {
    expect(
      validateFilterRule(draft({ subject: ["x"], forward: "no-at-sign" }), VERIFIED),
    ).toContain("forwardNotAddress");
  });

  it("refuses a blocked rule with no address, and one with a bad address", () => {
    expect(validateFilterRule(draft({ type: "blocked" }), VERIFIED)).toContain(
      "blockedNeedsAddress",
    );
    expect(
      validateFilterRule(draft({ type: "blocked", from: ["nope"] }), VERIFIED),
    ).toContain("blockedNotAddress");
  });

  it("refuses control characters in any criterion or folder name", () => {
    expect(
      validateFilterRule(draft({ subject: ["a\nb"], star: true }), VERIFIED),
    ).toContain("controlCharacters");
    expect(
      validateFilterRule(draft({ subject: ["x"], moveTo: "a\tb" }), VERIFIED),
    ).toContain("controlCharacters");
  });

  it("refuses a negative size bound", () => {
    expect(
      validateFilterRule(draft({ sizeOver: -1, star: true }), VERIFIED),
    ).toContain("negativeSize");
  });

  it("reports every problem at once", () => {
    const problems = validateFilterRule(
      draft({ moveTo: "X", delete: true, forward: "bad" }),
      VERIFIED,
    );
    expect(problems).toContain("noCriteria");
    expect(problems).toContain("moveAndDelete");
    expect(problems).toContain("forwardNotAddress");
  });
});

describe("safeScriptString", () => {
  it("accepts ordinary text including accents", () => {
    expect(isSafeScriptString("facturación — 2026")).toBe(true);
  });

  it("refuses C0 controls and DEL", () => {
    expect(isSafeScriptString("a\nb")).toBe(false);
    expect(isSafeScriptString("a\u0000b")).toBe(false);
    expect(isSafeScriptString("a\u007fb")).toBe(false);
  });
});

describe("summaries — the one line a rule row shows", () => {
  it("joins every criterion that is set", () => {
    const text = criteriaSummary(
      rule({
        from: ["a@b.co"],
        subject: ["factura"],
        sizeOver: 1024 * 1024,
        hasAttachment: true,
      }),
      WORDS,
    );
    expect(text).toBe("De: a@b.co · Asunto: factura · mayor que 1.0 MB · con adjunto");
  });

  it("renders hasAttachment:false as its own criterion", () => {
    expect(criteriaSummary(rule({ hasAttachment: false }), WORDS)).toBe("sin adjunto");
  });

  it("says so when a rule has no criteria rather than rendering an empty line", () => {
    expect(criteriaSummary(rule(), WORDS)).toBe("—");
    expect(actionsSummary(rule(), WORDS)).toBe("—");
  });

  it("joins every action that is set", () => {
    const text = actionsSummary(
      rule({
        moveTo: "Facturas",
        labels: ["trabajo"],
        markRead: true,
        star: true,
        forward: "ok@dest.com",
        stop: true,
      }),
      WORDS,
    );
    expect(text).toBe(
      "mover a Facturas · etiquetar trabajo · marcar leído · destacar · reenviar a ok@dest.com · detener",
    );
  });

  it("renders a neverSpam rule's TYPE as its action — it has no action fields", () => {
    expect(actionsSummary(rule({ type: "neverSpam" }), WORDS)).toBe("nunca spam");
  });
});

describe("byte formatting and parsing", () => {
  it("formats below a kilobyte in plain bytes", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(999)).toBe("999 B");
  });

  it("steps up through the units with one decimal", () => {
    expect(formatBytes(1024)).toBe("1.0 KB");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(1024 * 1024 * 5)).toBe("5.0 MB");
  });

  it("refuses to render a negative size", () => {
    expect(formatBytes(-1)).toBe("0 B");
  });

  it("parses a size in the chosen unit", () => {
    expect(parseSize("5", "MB")).toBe(5 * 1024 * 1024);
    expect(parseSize("2", "KB")).toBe(2048);
    expect(parseSize("100", "B")).toBe(100);
  });

  it("reads an empty field as 'unset' (0, the server's own spelling)", () => {
    expect(parseSize("", "MB")).toBe(0);
    expect(parseSize("   ", "MB")).toBe(0);
  });

  it("returns undefined for a typo rather than silently storing 0", () => {
    // 0 means "no bound" on the wire, so coercing "cinco" to 0 would drop the
    // criterion the user was adding without telling them.
    expect(parseSize("cinco", "MB")).toBeUndefined();
    expect(parseSize("-3", "MB")).toBeUndefined();
  });
});
