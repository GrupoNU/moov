import { describe, expect, it } from "vitest";

import {
  MAX_OR_BRANCHES,
  firstGroup,
  formatDateValue,
  formatQuery,
  formatSizeValue,
  hasAnyTerm,
  isEmptyGroup,
  parseAgeValue,
  parseDateValue,
  windowAround,
  parseSearchQuery,
  parseSizeValue,
  withGroupPatch,
} from "./searchQuery";

/**
 * The operator grammar (L3 epic E3, canon §2.5).
 *
 * These tests are the specification: every operator the plan names has a case
 * here, and every REFUSAL has one too — because "the chip says it could not
 * use this term" is a behaviour a user sees, not an internal detail.
 *
 * `NOW` is a fixed instant so the relative operators assert exact values.
 */
const NOW = new Date("2026-08-30T12:00:00.000Z");

describe("tokenizing", () => {
  it("splits on whitespace and keeps quoted values whole", () => {
    const q = parseSearchQuery('subject:"quarterly report" from:ana', NOW);
    expect(q.groups[0]?.fields).toEqual({
      subject: "quarterly report",
      from: "ana",
    });
  });

  it("keeps a quoted phrase in the free text, quotes included", () => {
    const q = parseSearchQuery('"exact phrase" loose', NOW);
    expect(q.groups[0]?.text).toBe('"exact phrase" loose');
  });

  it("survives an unterminated quote — a user mid-typing has one", () => {
    const q = parseSearchQuery('subject:"half typed', NOW);
    expect(q.groups[0]?.fields.subject).toBe("half typed");
    expect(q.unsupported).toEqual([]);
  });

  it("treats a colon inside ordinary text as text, not an operator", () => {
    const q = parseSearchQuery("https://example.test/x meeting", NOW);
    expect(q.groups[0]?.text).toBe("https://example.test/x meeting");
    expect(q.unsupported).toEqual([]);
  });

  it("treats a clock time as text — an operator name must start with a letter", () => {
    // The counterpart to the digits fix below: `rfc822msgid:` must be
    // recognised as an operator (digits in the NAME), while `12:30` must not.
    const q = parseSearchQuery("meeting 12:30", NOW);
    expect(q.groups[0]?.text).toBe("meeting 12:30");
    expect(q.unsupported).toEqual([]);
  });

  it("recognises an operator whose name contains digits (rfc822msgid)", () => {
    // A letters-only name pattern made this fall through to free text, which
    // would search message BODIES for the literal id — a silent mis-answer.
    const q = parseSearchQuery("rfc822msgid:<abc@x>", NOW);
    expect(q.groups[0]?.text).toBe("");
    expect(q.unsupported[0]).toMatchObject({ operator: "rfc822msgid" });
  });

  it("ignores a bare AND — it is the default conjunction", () => {
    const q = parseSearchQuery("from:ana AND is:unread", NOW);
    expect(q.groups).toHaveLength(1);
    expect(q.groups[0]?.fields.from).toBe("ana");
    expect(q.groups[0]?.unread).toBe(true);
  });

  it("parses an empty string to one empty group", () => {
    const q = parseSearchQuery("", NOW);
    expect(q.groups).toHaveLength(1);
    expect(isEmptyGroup(q.groups[0]!)).toBe(true);
    expect(hasAnyTerm(q)).toBe(false);
  });
});

describe("header-field operators", () => {
  it.each(["from", "to", "cc", "bcc", "subject"] as const)("parses %s:", (field) => {
    const q = parseSearchQuery(`${field}:ana@example.test`, NOW);
    expect(q.groups[0]?.fields[field]).toBe("ana@example.test");
  });

  it("is case-insensitive on the operator name but not on the value", () => {
    const q = parseSearchQuery("FROM:Ana", NOW);
    expect(q.groups[0]?.fields.from).toBe("Ana");
  });

  it("names a second, different value for the same field instead of overwriting", () => {
    // The server refuses "two different cc conditions"; the parser must not
    // hand it one, and must not silently pick a winner either.
    const q = parseSearchQuery("cc:ana cc:juan", NOW);
    expect(q.groups[0]?.fields.cc).toBe("ana");
    expect(q.unsupported).toEqual([
      { operator: "cc", raw: "cc:juan", reason: "badValue" },
    ]);
  });

  it("accepts a repeated IDENTICAL value silently — it is the same condition", () => {
    const q = parseSearchQuery("cc:ana cc:ana", NOW);
    expect(q.unsupported).toEqual([]);
  });

  it("calls an empty value INCOMPLETE, not bad (E-02)", () => {
    /*
     * `from:` is the state every operator query passes through. Reported as a
     * `badValue` it produced a refusal card over "no matches" while the user
     * was still typing the name — the review's hostile intermediate state. The
     * term is still excluded from the filter; it is just not complained about.
     */
    const q = parseSearchQuery("from:", NOW);
    expect(q.unsupported[0]).toMatchObject({ operator: "from", reason: "incomplete" });
    expect(q.groups[0]?.fields.from).toBeUndefined();
  });

  it("calls every valueless operator incomplete, not just the text ones", () => {
    // The date and size parsers would otherwise report `""` as a value that
    // failed to parse — true, and useless to a user mid-word.
    for (const raw of ["before:", "after:", "larger:", "newer_than:", "label:", "in:", "is:"]) {
      expect(parseSearchQuery(raw, NOW).unsupported[0]?.reason).toBe("incomplete");
    }
  });

  it("still refuses a value that is present and wrong", () => {
    // The distinction only pays if the genuine mistake still speaks up.
    expect(parseSearchQuery("before:tuesday", NOW).unsupported[0]?.reason).toBe("badValue");
    expect(parseSearchQuery("larger:huge", NOW).unsupported[0]?.reason).toBe("badValue");
  });
});

describe("is: and has:", () => {
  it("maps is:unread and is:read to the one unread flag", () => {
    expect(parseSearchQuery("is:unread", NOW).groups[0]?.unread).toBe(true);
    expect(parseSearchQuery("is:read", NOW).groups[0]?.unread).toBe(false);
  });

  it("maps is:starred", () => {
    expect(parseSearchQuery("is:starred", NOW).groups[0]?.starred).toBe(true);
  });

  it("maps has:attachment", () => {
    expect(parseSearchQuery("has:attachment", NOW).groups[0]?.hasAttachment).toBe(true);
  });

  /*
   * E4 changed half of this test, and the change is the point.
   *
   * `is:muted` used to be deferred by name, because nothing could answer it.
   * E4's `Mute/get` can — just not through the filter: the server refused a
   * vendor `inMutedThread` condition on measured grounds ("a predicate whose
   * whole result set is [...] a few dozen ids a client can cache"). So the term
   * is now ACCEPTED and honoured client-side, and only `is:important` — which
   * needs a classifier that does not exist yet — is still deferred.
   */
  it("accepts is:muted, and still defers is:important by NAME", () => {
    const q = parseSearchQuery("is:muted is:important", NOW);
    expect(q.unsupported).toEqual([
      { operator: "is:important", raw: "is:important", reason: "deferredOperator" },
    ]);
    expect(q.groups[0]?.muted).toBe(true);
    // And critically: neither leaks into the free text, where they would match
    // message bodies containing the word "muted".
    expect(q.groups[0]?.text).toBe("");
  });

  it("reads -is:muted as 'not muted' rather than refusing the minus", () => {
    expect(parseSearchQuery("-is:muted hola", NOW).groups[0]?.muted).toBe(false);
  });

  it("defers the superstar has: values by name", () => {
    const q = parseSearchQuery("has:yellow-star", NOW);
    expect(q.unsupported[0]).toMatchObject({
      operator: "has:yellow-star",
      reason: "deferredOperator",
    });
  });
});

describe("negation — only where the server has an inverse", () => {
  /*
   * The authority is `internal/jmap/mail/query.go`: the NOT operator is
   * refused on principle ("its result is the complement of a match set, which
   * no index in this store can produce"), and the cheap negations that ARE
   * served are notKeyword on the four IMAP system flags plus whole-mailbox
   * exclusion.
   */

  it("-is:starred is real — notKeyword:$flagged", () => {
    expect(parseSearchQuery("-is:starred", NOW).groups[0]?.starred).toBe(false);
  });

  it("-is:unread is real — it is is:read", () => {
    expect(parseSearchQuery("-is:unread", NOW).groups[0]?.unread).toBe(false);
  });

  it("-is:read is real — it is is:unread", () => {
    expect(parseSearchQuery("-is:read", NOW).groups[0]?.unread).toBe(true);
  });

  it("-has:attachment is real — hasAttachment is a boolean, not a complement", () => {
    expect(parseSearchQuery("-has:attachment", NOW).groups[0]?.hasAttachment).toBe(false);
  });

  it("-from: is NOT — named honestly and excluded from the filter", () => {
    const q = parseSearchQuery("-from:ana", NOW);
    expect(q.groups[0]?.fields.from).toBeUndefined();
    expect(q.unsupported).toEqual([
      { operator: "-from", raw: "-from:ana", reason: "negationUnanswerable" },
    ]);
  });

  it.each(["to", "cc", "bcc", "subject", "in", "label", "before", "after", "larger"])(
    "-%s: is unanswerable and says so",
    (op) => {
      const q = parseSearchQuery(`-${op}:x`, NOW);
      expect(q.unsupported[0]).toMatchObject({ reason: "negationUnanswerable" });
    },
  );

  it("never demotes a refused negation to free text", () => {
    // The failure this guards: `-from:ana` becoming the text "from:ana" would
    // return the messages the user asked to EXCLUDE — the exact inversion.
    const q = parseSearchQuery("-from:ana report", NOW);
    expect(q.groups[0]?.text).toBe("report");
  });

  it("a bare minus in the text is not a negation", () => {
    const q = parseSearchQuery("cost-benefit", NOW);
    expect(q.groups[0]?.text).toBe("cost-benefit");
    expect(q.unsupported).toEqual([]);
  });
});

describe("scope operators", () => {
  it("parses in:<name>, lowercased", () => {
    expect(parseSearchQuery("in:Archive", NOW).groups[0]?.inMailbox).toBe("archive");
  });

  it.each(["anywhere", "spam", "trash", "inbox"])("parses in:%s", (scope) => {
    expect(parseSearchQuery(`in:${scope}`, NOW).groups[0]?.inMailbox).toBe(scope);
  });

  it("parses label: verbatim — encoding is the mapper's job", () => {
    expect(parseSearchQuery('label:"Work Clients"', NOW).groups[0]?.label).toBe(
      "Work Clients",
    );
  });
});

describe("date operators", () => {
  it("parses Gmail's YYYY/MM/DD", () => {
    expect(parseSearchQuery("after:2026/01/15", NOW).groups[0]?.after).toBe(
      "2026-01-15T00:00:00.000Z",
    );
  });

  it("parses the ISO spelling a user will also type", () => {
    expect(parseSearchQuery("before:2026-03-01", NOW).groups[0]?.before).toBe(
      "2026-03-01T00:00:00.000Z",
    );
  });

  it("refuses a date that is not a real calendar day, rather than rolling it", () => {
    // `new Date(2026, 1, 31)` silently becomes 3 March. A search for February
    // that quietly means March is worse than one that is named.
    expect(parseDateValue("2026/02/31")).toBeUndefined();
    const q = parseSearchQuery("after:2026/02/31", NOW);
    expect(q.groups[0]?.after).toBeUndefined();
    expect(q.unsupported[0]).toMatchObject({ operator: "after", reason: "badValue" });
  });

  it("refuses nonsense values", () => {
    expect(parseDateValue("yesterday")).toBeUndefined();
    expect(parseDateValue("2026/13/01")).toBeUndefined();
  });

  it("resolves newer_than: to an absolute instant", () => {
    // 7 days before the fixed NOW.
    expect(parseSearchQuery("newer_than:7d", NOW).groups[0]?.after).toBe(
      "2026-08-23T12:00:00.000Z",
    );
  });

  it("resolves older_than: to the mirror bound", () => {
    expect(parseSearchQuery("older_than:1y", NOW).groups[0]?.before).toBe(
      "2025-08-30T12:00:00.000Z",
    );
  });

  it("reads m as MONTHS, which is what Gmail's operator page means", () => {
    expect(parseAgeValue("2m", NOW)).toBe(
      new Date(NOW.getTime() - 60 * 86_400_000).toISOString(),
    );
  });

  it("refuses a zero or unit-less age", () => {
    expect(parseAgeValue("0d", NOW)).toBeUndefined();
    expect(parseAgeValue("7", NOW)).toBeUndefined();
    expect(parseAgeValue("7w", NOW)).toBeUndefined();
  });
});

describe("size operators", () => {
  it.each([
    ["larger:5M", 5 * 1024 * 1024],
    ["larger:500k", 500 * 1024],
    ["larger:1G", 1024 * 1024 * 1024],
    ["larger:2048", 2048],
  ])("parses %s", (input, expected) => {
    expect(parseSearchQuery(input, NOW).groups[0]?.larger).toBe(expected);
  });

  it("parses smaller: to the upper bound", () => {
    expect(parseSearchQuery("smaller:1M", NOW).groups[0]?.smaller).toBe(1024 * 1024);
  });

  it("reads a bare size: as larger-than, which is Gmail's meaning", () => {
    expect(parseSearchQuery("size:1M", NOW).groups[0]?.larger).toBe(1024 * 1024);
  });

  it("refuses a value that is not a size", () => {
    expect(parseSizeValue("big")).toBeUndefined();
    expect(parseSearchQuery("larger:big", NOW).unsupported[0]).toMatchObject({
      operator: "larger",
      reason: "badValue",
    });
  });
});

describe("OR", () => {
  it("splits into branches", () => {
    const q = parseSearchQuery("from:ana OR from:juan", NOW);
    expect(q.groups).toHaveLength(2);
    expect(q.groups[0]?.fields.from).toBe("ana");
    expect(q.groups[1]?.fields.from).toBe("juan");
  });

  it("is case-insensitive and does not catch the word 'or' in a phrase", () => {
    const q = parseSearchQuery('"this or that"', NOW);
    expect(q.groups).toHaveLength(1);
    expect(q.groups[0]?.text).toBe('"this or that"');
  });

  it("mirrors the server's branch bound so the UI can enforce it", () => {
    expect(MAX_OR_BRANCHES).toBe(4);
    const q = parseSearchQuery("in:a OR in:b OR in:c OR in:d OR in:e", NOW);
    expect(q.groups).toHaveLength(5); // the parser reports; the mapper refuses
  });
});

describe("deferred operators (L3 plan §6)", () => {
  it.each(["deliveredto", "list", "filename", "rfc822msgid", "header", "category"])(
    "%s: is named, not silently dropped",
    (op) => {
      const q = parseSearchQuery(`${op}:x`, NOW);
      expect(q.unsupported).toEqual([
        { operator: op, raw: `${op}:x`, reason: "deferredOperator" },
      ]);
      expect(q.groups[0]?.text).toBe("");
    },
  );
});

describe("the round trip — parse → format → parse is a fixed point", () => {
  const CASES = [
    "from:ana",
    "from:ana is:unread",
    'subject:"quarterly report" has:attachment',
    "is:starred -has:attachment",
    "in:anywhere from:ana",
    "label:Clientes is:unread",
    "after:2026/01/15 before:2026/03/01",
    "larger:5M smaller:1G",
    "from:ana OR from:juan",
    "from:ana report",
    '"exact phrase" from:ana',
    "-is:starred",
    "is:read",
  ];

  it.each(CASES)("round-trips %s", (input) => {
    const once = formatQuery(parseSearchQuery(input, NOW));
    const twice = formatQuery(parseSearchQuery(once, NOW));
    expect(twice).toBe(once);
    // And the STRUCTURE survives, not only the string.
    expect(parseSearchQuery(twice, NOW).groups).toEqual(
      parseSearchQuery(once, NOW).groups,
    );
  });

  it("does not re-emit an unsupported term — the box must not promise it", () => {
    const formatted = formatQuery(parseSearchQuery("-from:ana report", NOW));
    expect(formatted).toBe("report");
  });

  it("formats dates and sizes back to their compact spellings", () => {
    expect(formatDateValue("2026-01-15T00:00:00.000Z")).toBe("2026/01/15");
    expect(formatSizeValue(5 * 1024 * 1024)).toBe("5M");
    expect(formatSizeValue(2048)).toBe("2K");
    expect(formatSizeValue(1500)).toBe("1500");
  });
});

describe("withGroupPatch — what the chips and the panel call", () => {
  it("adds an operator to an empty query", () => {
    expect(withGroupPatch("", { unread: true }, NOW)).toBe("is:unread");
  });

  it("removes an operator when patched with undefined", () => {
    expect(withGroupPatch("from:ana is:unread", { unread: undefined }, NOW)).toBe(
      "from:ana",
    );
  });

  it("toggling twice returns the box to its exact original string", () => {
    // The canonical output order is what makes this true, and it is what keeps
    // a chip from churning the box every time it is pressed.
    const start = "from:ana report";
    const on = withGroupPatch(start, { hasAttachment: true }, NOW);
    const off = withGroupPatch(on, { hasAttachment: undefined }, NOW);
    expect(off).toBe(start);
  });

  it("preserves the free text while editing operators", () => {
    expect(withGroupPatch("quarterly report", { starred: true }, NOW)).toBe(
      "is:starred quarterly report",
    );
  });

  it("edits only the FIRST branch of an OR", () => {
    const out = withGroupPatch("from:ana OR from:juan", { unread: true }, NOW);
    expect(out).toBe("from:ana is:unread OR from:juan");
  });

  it("replaces a field rather than duplicating it", () => {
    expect(withGroupPatch("from:ana", { fields: { from: "juan" } }, NOW)).toBe(
      "from:juan",
    );
  });

  it("firstGroup reads back what a chip renders from", () => {
    expect(firstGroup("is:unread from:ana", NOW)).toMatchObject({
      unread: true,
      fields: { from: "ana" },
    });
  });
});

describe("hasAnyTerm", () => {
  it("is false for whitespace", () => {
    expect(hasAnyTerm(parseSearchQuery("   ", NOW))).toBe(false);
  });

  it("is TRUE for a query of only unsupported terms", () => {
    // The user asked for something. An idle inbox would be a lie.
    expect(hasAnyTerm(parseSearchQuery("filename:x", NOW))).toBe(true);
  });
});

describe("windowAround — the arithmetic, without a form", () => {
  it("includes the whole last day of the window", () => {
    expect(windowAround("2026-03-12", 1)).toEqual({
      after: "2026/03/11",
      before: "2026/03/14",
    });
  });

  it("crosses a month boundary without rolling wrong", () => {
    expect(windowAround("2026-03-01", 3)).toEqual({
      after: "2026/02/26",
      before: "2026/03/05",
    });
  });

  it("refuses an unparseable anchor rather than guessing a date", () => {
    // The rule `parseDateValue` follows: a date that quietly means a different
    // day is worse than one that is refused.
    expect(windowAround("marzo", 1)).toBeUndefined();
    expect(windowAround("", 1)).toBeUndefined();
  });
});
