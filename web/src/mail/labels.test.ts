import { describe, expect, it } from "vitest";

import {
  consumesKeywordSlot,
  decodeLabelName,
  decodePointerToken,
  encodeLabelKeyword,
  escapePointerToken,
  isLabelKeyword,
  isReservedKeyword,
  keywordFromPatchKey,
  keywordPatchKey,
  labelBudget,
  labelKeywordsOf,
  labelStateFor,
  LABEL_PREFIX,
  MAX_DURABLE_KEYWORDS,
  MAX_LABEL_NAME_LENGTH,
  toggleTargetFor,
  validateLabelName,
} from "./labels";

describe("the wire convention", () => {
  it("encodes a display name as $label:<name>", () => {
    expect(encodeLabelKeyword("work")).toBe("$label:work");
    expect(LABEL_PREFIX).toBe("$label:");
  });

  it("round-trips every name shape, including the ones that break naive code", () => {
    const names = [
      "work",
      // The nested convention — the case that produced the silent-loss bug.
      "work/clients",
      // A tilde, the OTHER JSON-Pointer metacharacter.
      "back~up",
      // Both, in the order that catches a wrong escape order.
      "a~1b/c",
      // Unicode: the pilot is Spanish and this must survive verbatim.
      "Facturación",
      "日本語",
      "emoji 🎯",
      // Spaces and punctuation.
      "To do (urgent)",
    ];
    for (const name of names) {
      expect(decodeLabelName(encodeLabelKeyword(name))).toBe(name);
    }
  });

  it("trims the name so two labels cannot look identical", () => {
    expect(encodeLabelKeyword("  work  ")).toBe("$label:work");
  });

  it("does not read a non-label keyword as a label", () => {
    for (const keyword of ["$seen", "$flagged", "NonJunk", "$Forwarded", "label:work", ""]) {
      expect(decodeLabelName(keyword)).toBeUndefined();
      expect(isLabelKeyword(keyword)).toBe(false);
    }
  });

  it("treats a bare prefix with no name as not a label", () => {
    expect(decodeLabelName("$label:")).toBeUndefined();
  });
});

describe("RFC 6901 escaping", () => {
  it("escapes ~ before /, which is the order the RFC mandates", () => {
    expect(escapePointerToken("a~b")).toBe("a~0b");
    expect(escapePointerToken("a/b")).toBe("a~1b");
    // The order test: escaping / first would produce "a~01b" here.
    expect(escapePointerToken("a~1b")).toBe("a~01b");
  });

  it("decodes in the mirror order", () => {
    expect(decodePointerToken("a~01b")).toBe("a~1b");
    expect(decodePointerToken("a~1b")).toBe("a/b");
    expect(decodePointerToken("a~0b")).toBe("a~b");
  });

  it("round-trips arbitrary tokens", () => {
    for (const token of ["", "plain", "a/b", "a~b", "~", "/", "~1", "~0", "a~1/b~0/c"]) {
      expect(decodePointerToken(escapePointerToken(token))).toBe(token);
    }
  });
});

describe("keywordPatchKey — the single point where a patch key is built", () => {
  it("leaves a system flag alone (no metacharacters to escape)", () => {
    expect(keywordPatchKey("$seen")).toBe("keywords/$seen");
    expect(keywordPatchKey("$flagged")).toBe("keywords/$flagged");
  });

  it("escapes the slash of a nested label — the exact silent-loss shape", () => {
    /*
     * research 05 §5.0: an unescaped `keywords/$label:work/clients` addresses
     * the `clients` MEMBER of `$label:work`. The server accepts it, nothing
     * errors, and the label never lands. The escaped form addresses the
     * keyword itself.
     */
    const naive = `keywords/${encodeLabelKeyword("work/clients")}`;
    expect(naive).toBe("keywords/$label:work/clients");

    const key = keywordPatchKey(encodeLabelKeyword("work/clients"));
    expect(key).toBe("keywords/$label:work~1clients");
    expect(key).not.toBe(naive);
    // The escaped key has exactly ONE separator: the one after "keywords".
    expect(key.split("/")).toHaveLength(2);
  });

  it("escapes a tilde", () => {
    expect(keywordPatchKey("$label:back~up")).toBe("keywords/$label:back~0up");
  });

  it("round-trips through keywordFromPatchKey for every name shape", () => {
    for (const name of ["work", "work/clients", "back~up", "a~1b/c", "Facturación"]) {
      const keyword = encodeLabelKeyword(name);
      expect(keywordFromPatchKey(keywordPatchKey(keyword))).toBe(keyword);
    }
  });

  it("returns undefined for a key that is not a keyword patch", () => {
    expect(keywordFromPatchKey("mailboxIds")).toBeUndefined();
    expect(keywordFromPatchKey("keywords/")).toBeUndefined();
  });
});

describe("reserved keywords", () => {
  it("reserves the system flags and the ecosystem keywords", () => {
    for (const keyword of ["$seen", "$flagged", "$answered", "$draft", "$Forwarded", "$MDNSent", "NonJunk", "Junk"]) {
      expect(isReservedKeyword(keyword)).toBe(true);
    }
  });

  it("matches case-insensitively, because IMAP keyword matching is", () => {
    expect(isReservedKeyword("$SEEN")).toBe(true);
    expect(isReservedKeyword("nonjunk")).toBe(true);
    expect(isReservedKeyword("NONJUNK")).toBe(true);
  });

  it("does not reserve an ordinary name", () => {
    expect(isReservedKeyword("work")).toBe(false);
    expect(isReservedKeyword("$label:work")).toBe(false);
  });

  it("discounts ONLY the four system flags from the Maildir slots", () => {
    // These live in the Maildir filename's flag field, not dovecot-keywords.
    for (const flag of ["$seen", "$flagged", "$answered", "$draft"]) {
      expect(consumesKeywordSlot(flag)).toBe(false);
    }
    // These are real registered keywords and really do take a letter.
    for (const keyword of ["$Forwarded", "$MDNSent", "NonJunk", "$label:work"]) {
      expect(consumesKeywordSlot(keyword)).toBe(true);
    }
  });
});

describe("validateLabelName", () => {
  it("accepts an ordinary name", () => {
    expect(validateLabelName("Clientes")).toBeUndefined();
  });

  it("refuses an empty or whitespace-only name", () => {
    expect(validateLabelName("")).toBe("empty");
    expect(validateLabelName("   ")).toBe("empty");
  });

  it("refuses a name past the length cap", () => {
    expect(validateLabelName("a".repeat(MAX_LABEL_NAME_LENGTH))).toBeUndefined();
    expect(validateLabelName("a".repeat(MAX_LABEL_NAME_LENGTH + 1))).toBe("tooLong");
  });

  it("refuses a reserved keyword name, so a label cannot alias another client's flag", () => {
    expect(validateLabelName("NonJunk")).toBe("reserved");
    expect(validateLabelName("$seen")).toBe("reserved");
  });

  it("refuses a duplicate case-insensitively — one keyword, not two rows", () => {
    expect(validateLabelName("Work", ["work"])).toBe("duplicate");
    expect(validateLabelName("work", ["Work"])).toBe("duplicate");
    expect(validateLabelName("otro", ["work"])).toBeUndefined();
  });

  it("refuses control characters, which Dovecot would reject mid-command", () => {
    expect(validateLabelName("a\u0000b")).toBe("control");
    expect(validateLabelName("a\nb")).toBe("control");
    expect(validateLabelName("a\u007fb")).toBe("control");
  });

  it("allows the metacharacters the escaping exists for", () => {
    expect(validateLabelName("work/clients")).toBeUndefined();
    expect(validateLabelName("back~up")).toBeUndefined();
  });
});

describe("labelBudget — the ceiling, stated honestly", () => {
  it("mirrors the server's measured constant", () => {
    // internal/imap/metadata.go:52 — MaxDurableKeywordsPerMailbox = 26.
    expect(MAX_DURABLE_KEYWORDS).toBe(26);
  });

  it("reports the whole ceiling available on an untouched folder", () => {
    const budget = labelBudget([], []);
    expect(budget.available).toBe(26);
    expect(budget.used).toBe(0);
    expect(budget.isFull).toBe(false);
  });

  it("does not charge the four system flags", () => {
    const budget = labelBudget(["$seen", "$flagged", "$answered", "$draft"], []);
    expect(budget.systemUsed).toBe(0);
    expect(budget.available).toBe(26);
  });

  it("charges the semi-system keywords other clients set — they share the 26", () => {
    const budget = labelBudget(["$seen", "$Forwarded", "$MDNSent", "NonJunk"], []);
    expect(budget.systemUsed).toBe(3);
    expect(budget.available).toBe(23);
  });

  it("charges each user label once", () => {
    const labels = ["$label:a", "$label:b", "$label:c"];
    const budget = labelBudget([...labels, "$seen"], labels);
    expect(budget.labelsUsed).toBe(3);
    expect(budget.systemUsed).toBe(0);
    expect(budget.available).toBe(23);
  });

  it("counts a label that exists but is on no message yet", () => {
    // The 27th label must be refused BEFORE it is applied, or it is created,
    // used, and lost weeks later when the Maildir index is rebuilt.
    const budget = labelBudget([], ["$label:brand-new"]);
    expect(budget.labelsUsed).toBe(1);
    expect(budget.available).toBe(25);
  });

  it("does not double-count a label that is both known and observed", () => {
    const budget = labelBudget(["$label:work", "$label:work"], ["$label:work"]);
    expect(budget.used).toBe(1);
    expect(budget.available).toBe(25);
  });

  it("folds case, because Dovecot allocates one letter per case-folded name", () => {
    const budget = labelBudget(["$LABEL:Work", "$label:work"], ["$label:work"]);
    expect(budget.used).toBe(1);
  });

  it("ignores empty and whitespace entries", () => {
    const budget = labelBudget(["", "   ", "$label:a"], ["$label:a"]);
    expect(budget.used).toBe(1);
  });

  it("reaches exactly zero at the ceiling and reports full", () => {
    const labels = Array.from({ length: 26 }, (_, index) => `$label:l${index}`);
    const budget = labelBudget(labels, labels);
    expect(budget.available).toBe(0);
    expect(budget.isFull).toBe(true);
    expect(budget.used).toBe(26);
  });

  it("never reports a negative budget when the folder is already over", () => {
    // Reachable in the wild: another client can write past the ceiling and
    // Dovecot accepts it in memory. The UI must say "0 available", never "-4".
    const labels = Array.from({ length: 30 }, (_, index) => `$label:l${index}`);
    const budget = labelBudget(labels, labels);
    expect(budget.available).toBe(0);
    expect(budget.used).toBe(26);
    expect(budget.isFull).toBe(true);
  });
});

describe("reading labels off a message", () => {
  it("lists only the label keywords that are set to true", () => {
    expect(
      labelKeywordsOf({
        $seen: true,
        "$label:work": true,
        "$label:old": false,
        NonJunk: true,
      }),
    ).toEqual(["$label:work"]);
  });

  it("is empty for a message with no keywords at all", () => {
    expect(labelKeywordsOf(undefined)).toEqual([]);
  });
});

describe("labelStateFor — the mixed state a menu has to render", () => {
  const withLabel = { "$label:work": true };
  const without = { $seen: true };

  it("is 'all' when every message carries it", () => {
    expect(labelStateFor("$label:work", [withLabel, withLabel])).toBe("all");
  });

  it("is 'none' when none does", () => {
    expect(labelStateFor("$label:work", [without, without])).toBe("none");
  });

  it("is 'some' when the selection is split", () => {
    expect(labelStateFor("$label:work", [withLabel, without])).toBe("some");
  });

  it("is 'none' for an empty selection", () => {
    expect(labelStateFor("$label:work", [])).toBe("none");
  });

  it("treats an absent keywords map as not carrying the label", () => {
    expect(labelStateFor("$label:work", [undefined, withLabel])).toBe("some");
  });
});

describe("toggleTargetFor — Gmail's rule", () => {
  it("applies to all when the selection is mixed", () => {
    expect(toggleTargetFor("some")).toBe(true);
  });

  it("applies when none has it", () => {
    expect(toggleTargetFor("none")).toBe(true);
  });

  it("removes only when every message already has it", () => {
    expect(toggleTargetFor("all")).toBe(false);
  });
});
