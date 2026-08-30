import { describe, expect, it } from "vitest";

import { parseSearchQuery } from "./searchQuery";
import { SCOPE_ANYWHERE, planFilter, resolveScope } from "./searchFilter";
import { KEYWORD_FLAGGED, KEYWORD_SEEN, type Mailbox } from "./types";

/**
 * The WIRE SHAPE of a searched filter (L3 epic E3).
 *
 * These assert the exact JSON handed to `Email/query`, following the precedent
 * `api.test.ts` set for E1/E8: the contract with the server is the thing that
 * drifts silently, and a filter that is one key wrong comes back as an
 * `unsupportedFilter` the user cannot act on — or, worse, as a result list
 * that quietly ignored a condition.
 *
 * Every expectation here is traceable to a rule in
 * `internal/jmap/mail/query.go`; the module under test cites them line by line.
 */

const NOW = new Date("2026-08-30T12:00:00.000Z");

function mailbox(id: string, name: string, role: Mailbox["role"] = null): Mailbox {
  return {
    id,
    name,
    parentId: null,
    role,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
    isSubscribed: true,
    myRights: {
      mayReadItems: true,
      mayAddItems: true,
      mayRemoveItems: true,
      maySetSeen: true,
      maySetKeywords: true,
      mayCreateChild: true,
      mayRename: true,
      mayDelete: true,
      maySubmit: true,
    },
  };
}

const MAILBOXES: readonly Mailbox[] = [
  mailbox("mb1", "Bandeja de entrada", "inbox"),
  mailbox("mb2", "Enviados", "sent"),
  mailbox("mb3", "Spam", "junk"),
  mailbox("mb4", "Papelera", "trash"),
  mailbox("mb5", "Proyectos"),
];

function plan(input: string) {
  return planFilter(parseSearchQuery(input, NOW), MAILBOXES);
}

describe("free text", () => {
  it("sends a bare text condition", () => {
    expect(plan("arquitectura").filter).toEqual({ text: "arquitectura" });
  });

  it("sends NO inMailboxOtherThan of its own — the server's default exclusion", () => {
    /*
     * The trap this pins: `applyDefaultExclusion` says "an explicit
     * inMailboxOtherThan ... suppresses the server's default one". A client
     * that helpfully excludes Spam and Trash by hand would therefore turn OFF
     * Gmail's default exclusion and put Spam back into every search.
     */
    const filter = plan("arquitectura").filter!;
    expect(filter).not.toHaveProperty("inMailboxOtherThan");
    expect(JSON.stringify(filter)).not.toContain("inMailboxOtherThan");
  });
});

describe("rule 1 — from/to/subject fold into the ONE text field", () => {
  /*
   * `translateCondition` maps text/from/to/subject onto a single `f.text`, and
   * `mergeFilters` refuses "two different text conditions in one filter". Two
   * conditions would be an outright refusal, not a narrower search.
   */

  it("sends from: as a single text condition", () => {
    expect(plan("from:ana").filter).toEqual({ text: "ana" });
  });

  it("MERGES from: and subject: into one text rather than sending two", () => {
    expect(plan("from:ana subject:informe").filter).toEqual({ text: "ana informe" });
  });

  it("merges the header fields with the free text", () => {
    expect(plan("from:ana trimestral").filter).toEqual({ text: "ana trimestral" });
  });

  it("quotes a multi-word field value so it stays a phrase", () => {
    expect(plan('subject:"informe trimestral"').filter).toEqual({
      text: '"informe trimestral"',
    });
  });

  it("declares the fold as an approximation the UI can state", () => {
    const result = plan("from:ana subject:informe");
    expect(result.approximations).toEqual([
      { code: "fieldsFoldedIntoText", fields: ["from", "subject"] },
    ]);
  });

  it("does NOT cry approximation for a lone from: — the server over-matches it anyway", () => {
    expect(plan("from:ana").approximations).toEqual([]);
  });
});

describe("rule 2 — cc and bcc are exact and stay their own conditions", () => {
  /*
   * Migration 0008 gave each its own trigram index: "so `cc:ana@x.test`
   * matches the Cc header and only the Cc header". Folding them into the
   * tsvector would make them WORSE.
   */

  it("sends cc: as its own condition", () => {
    expect(plan("cc:ana informe").filter).toEqual({
      operator: "AND",
      conditions: [{ text: "informe" }, { cc: "ana" }],
    });
  });

  it("sends bcc: as its own condition", () => {
    expect(plan("bcc:ana informe").filter).toEqual({
      operator: "AND",
      conditions: [{ text: "informe" }, { bcc: "ana" }],
    });
  });

  it("never folds cc into the text", () => {
    const filter = JSON.stringify(plan("cc:ana informe").filter);
    expect(filter).not.toContain('"text":"informe ana"');
  });
});

describe("the flags — the exact predicates the server has", () => {
  it("is:unread is notKeyword:$seen", () => {
    expect(plan("is:unread informe").filter).toEqual({
      operator: "AND",
      conditions: [{ text: "informe" }, { notKeyword: KEYWORD_SEEN }],
    });
  });

  it("is:read is hasKeyword:$seen", () => {
    expect(plan("is:read informe").filter).toEqual({
      operator: "AND",
      conditions: [{ text: "informe" }, { hasKeyword: KEYWORD_SEEN }],
    });
  });

  it("is:starred is hasKeyword:$flagged", () => {
    expect(plan("is:starred informe").filter).toEqual({
      operator: "AND",
      conditions: [{ text: "informe" }, { hasKeyword: KEYWORD_FLAGGED }],
    });
  });

  it("-is:starred is notKeyword:$flagged — the one real negation", () => {
    expect(plan("-is:starred informe").filter).toEqual({
      operator: "AND",
      conditions: [{ text: "informe" }, { notKeyword: KEYWORD_FLAGGED }],
    });
  });

  it("has:attachment is the boolean condition", () => {
    expect(plan("has:attachment informe").filter).toEqual({
      operator: "AND",
      conditions: [{ text: "informe" }, { hasAttachment: true }],
    });
  });

  it("-has:attachment sends false, not a NOT operator", () => {
    expect(plan("-has:attachment informe").filter).toEqual({
      operator: "AND",
      conditions: [{ text: "informe" }, { hasAttachment: false }],
    });
  });

  it("never emits a NOT operator, which the server refuses on principle", () => {
    for (const query of ["-is:starred x", "-has:attachment x", "is:read x"]) {
      expect(JSON.stringify(plan(query).filter)).not.toContain('"NOT"');
    }
  });
});

describe("dates and sizes", () => {
  it("sends after/before as RFC 3339 instants", () => {
    expect(plan("after:2026/01/15 before:2026/03/01 informe").filter).toEqual({
      operator: "AND",
      conditions: [
        { text: "informe" },
        { after: "2026-01-15T00:00:00.000Z" },
        { before: "2026-03-01T00:00:00.000Z" },
      ],
    });
  });

  it("resolves newer_than: to an absolute after, never a relative expression", () => {
    expect(plan("newer_than:7d informe").filter).toEqual({
      operator: "AND",
      conditions: [{ text: "informe" }, { after: "2026-08-23T12:00:00.000Z" }],
    });
  });

  it("sends larger:/smaller: as minSize/maxSize in octets", () => {
    expect(plan("larger:5M smaller:1G informe").filter).toEqual({
      operator: "AND",
      conditions: [
        { text: "informe" },
        { minSize: 5 * 1024 * 1024 },
        { maxSize: 1024 * 1024 * 1024 },
      ],
    });
  });
});

describe("scope", () => {
  it("in:anywhere sends the EMPTY inMailboxOtherThan — the documented escape hatch", () => {
    expect(plan("in:anywhere informe").filter).toEqual({
      operator: "AND",
      conditions: [{ text: "informe" }, { inMailboxOtherThan: [] }],
    });
    expect(plan("in:anywhere informe").includesEverything).toBe(true);
  });

  it("resolves in:inbox by ROLE, not by the server's display name", () => {
    // The pilot's inbox is called "Bandeja de entrada".
    expect(plan("in:inbox informe").filter).toEqual({
      operator: "AND",
      conditions: [{ text: "informe" }, { inMailbox: "mb1" }],
    });
  });

  it("maps in:spam and in:trash onto the junk and trash roles", () => {
    expect(plan("in:spam informe").filter).toMatchObject({
      conditions: [{ text: "informe" }, { inMailbox: "mb3" }],
    });
    expect(plan("in:trash informe").filter).toMatchObject({
      conditions: [{ text: "informe" }, { inMailbox: "mb4" }],
    });
  });

  it("resolves a role-less folder by name", () => {
    expect(plan("in:Proyectos informe").filter).toMatchObject({
      conditions: [{ text: "informe" }, { inMailbox: "mb5" }],
    });
  });

  it("names an unknown folder instead of searching everything", () => {
    const result = plan("in:Inexistente informe");
    expect(result.filter).toBeUndefined();
    expect(result.problems).toEqual([
      { code: "unknownMailbox", detail: "inexistente" },
    ]);
  });

  it("an explicit in:spam beats the default exclusion — that is the server's rule", () => {
    // Sending `inMailbox` is enough; the server steps back on its own.
    const filter = JSON.stringify(plan("in:spam informe").filter);
    expect(filter).not.toContain("inMailboxOtherThan");
  });

  it("resolveScope is exported for the panel's folder select", () => {
    expect(resolveScope("inbox", MAILBOXES)?.id).toBe("mb1");
    expect(resolveScope(SCOPE_ANYWHERE, MAILBOXES)).toBeUndefined();
  });
});

describe("labels", () => {
  it("a bare label: is the whole-account hasKeyword E8 already ships", () => {
    expect(plan("label:Clientes").filter).toEqual({ hasKeyword: "$label:Clientes" });
  });

  it("a label with text is the AND the server serves on the text path", () => {
    expect(plan("label:Clientes informe").filter).toEqual({
      operator: "AND",
      conditions: [{ text: "informe" }, { hasKeyword: "$label:Clientes" }],
    });
  });

  it("REFUSES a label scoped to a folder — the folder view has no keyword predicate", () => {
    /*
     * `answerable()`: "filter condition hasKeyword is not supported without a
     * text condition: the folder view has no keyword predicate". Caught here,
     * before the round trip, naming what to remove.
     */
    const result = plan("label:Clientes in:inbox");
    expect(result.filter).toBeUndefined();
    expect(result.problems).toEqual([{ code: "labelNeedsText", detail: "Clientes" }]);
  });
});

describe("rule 3 — a filter needs a text or a folder", () => {
  it.each(["is:unread", "has:attachment", "larger:5M", "after:2026/01/15", "is:starred"])(
    "refuses %s on its own, naming what to add",
    (query) => {
      const result = plan(query);
      expect(result.filter).toBeUndefined();
      expect(result.problems).toEqual([{ code: "needsTextOrFolder" }]);
    },
  );

  it("accepts the same term once a folder is named", () => {
    expect(plan("is:unread in:inbox").filter).toEqual({
      operator: "AND",
      conditions: [{ notKeyword: KEYWORD_SEEN }, { inMailbox: "mb1" }],
    });
  });

  it("accepts the same term once text is present", () => {
    expect(plan("is:unread informe").filter).toBeDefined();
  });
});

describe("OR", () => {
  it("sends the server's OR operator with complete branches", () => {
    expect(plan("from:ana OR from:juan").filter).toEqual({
      operator: "OR",
      conditions: [{ text: "ana" }, { text: "juan" }],
    });
  });

  it("keeps each branch a complete AND", () => {
    expect(plan("from:ana is:unread OR in:Proyectos").filter).toEqual({
      operator: "OR",
      conditions: [
        { operator: "AND", conditions: [{ text: "ana" }, { notKeyword: KEYWORD_SEEN }] },
        { inMailbox: "mb5" },
      ],
    });
  });

  it("refuses a branch that would not stand on its own — the server's OR rule", () => {
    /*
     * `translateOr`: "A BRANCH OF AN OR MUST BE A FILTER THIS SERVER WOULD
     * SERVE ON ITS OWN ... a disjunction never narrows, it only widens".
     */
    const result = plan("from:ana OR is:starred");
    expect(result.filter).toBeUndefined();
    expect(result.problems).toEqual([{ code: "branchNotAnswerable", detail: "2" }]);
  });

  it("refuses more than the server's four branches, before the round trip", () => {
    const result = plan("in:inbox OR in:Enviados OR in:Spam OR in:Papelera OR in:Proyectos");
    expect(result.filter).toBeUndefined();
    expect(result.problems).toEqual([{ code: "tooManyBranches", detail: "5" }]);
  });

  it("never nests an OR inside an AND, which mergeFilters refuses", () => {
    /*
     * `mergeFilters`: "an OR nested inside an AND is not supported; distribute
     * it into one OR of complete conditions". Our OR is always the ROOT node,
     * and each branch is a complete AND — never the other way round.
     */
    for (const query of [
      "from:ana OR from:juan",
      "from:ana is:unread OR in:Proyectos",
      "in:inbox informe OR in:Proyectos informe",
    ]) {
      const filter = plan(query).filter!;
      expect(filter.operator).toBe("OR");
      for (const branch of filter.conditions as Record<string, unknown>[]) {
        // A branch is a condition or an AND — never another operator node.
        if ("operator" in branch) expect(branch.operator).toBe("AND");
      }
    }
  });
});

describe("the system flags are not answerable on their own", () => {
  /*
   * The bug a test caught here: `hasKeyword:$flagged` looks like the bare
   * `hasKeyword` that E8's label view sends, but the server treats the two
   * completely differently. `applyHasKeyword` routes a SYSTEM flag into the
   * `flagsAll` bitmask and never sets `f.keyword`, so `answerable()` sees an
   * empty text AND an empty keyword and refuses the filter.
   *
   * Counting every hasKeyword as answerable therefore shipped `is:starred` as
   * a standalone query the server would reject on arrival.
   */

  it("is:starred alone is refused client-side, not round-tripped into an error", () => {
    const result = plan("is:starred");
    expect(result.filter).toBeUndefined();
    expect(result.problems).toEqual([{ code: "needsTextOrFolder" }]);
  });

  it("is:read alone is refused for the same reason", () => {
    expect(plan("is:read").problems).toEqual([{ code: "needsTextOrFolder" }]);
  });

  it("but a bare USER label IS answerable — that is E8's label view", () => {
    expect(plan("label:Clientes").filter).toEqual({ hasKeyword: "$label:Clientes" });
  });

  it("an OR branch carrying only a system flag is refused too", () => {
    expect(plan("from:ana OR is:starred").problems).toEqual([
      { code: "branchNotAnswerable", detail: "2" },
    ]);
  });
});

describe("unsupported terms travel with the plan", () => {
  it("carries the parser's refusals through so the UI can name them", () => {
    const result = plan("-from:ana informe");
    expect(result.filter).toEqual({ text: "informe" });
    expect(result.unsupported).toEqual([
      { operator: "-from", raw: "-from:ana", reason: "negationUnanswerable" },
    ]);
  });

  it("an empty query sends nothing at all", () => {
    const result = plan("");
    expect(result.filter).toBeUndefined();
    expect(result.problems).toEqual([]);
  });

  it("undefined means 'do not search' and is never confused with null", () => {
    // `null` is the server's account-wide enumeration; `undefined` is "no".
    expect(plan("").filter).toBeUndefined();
    expect(plan("").filter).not.toBeNull();
  });
});
