import { describe, expect, it, vi } from "vitest";

import { JmapClient } from "../api/jmap";
import { fetchConversationMessages, fetchThreadRows, queryEmails, toJmapFilter } from "./api";
import { encodeLabelKeyword } from "./labels";
import { KEYWORD_ANSWERED, KEYWORD_DRAFT, KEYWORD_FLAGGED, KEYWORD_SEEN } from "./types";
import { INBOX_TYPES, sortForInboxType } from "./prefs";
import type { Email, Thread } from "./types";

/**
 * The list query's WIRE SHAPE (L3 epic E1).
 *
 * These assert on what is SENT, not on what renders, because the contract with
 * the server is the thing that can silently drift: `collapseThreads` omitted
 * where it was meant, a `Thread/get` fired on an uncollapsed list (one request
 * per message, to answer a question the list does not ask), or a back-reference
 * pointing at the wrong call.
 */

const ACCOUNT = "a";

function stub(responses: Record<string, unknown>) {
  const sent: [string, Record<string, unknown>, string][] = [];
  const client = new JmapClient({ username: "u", password: "p" });
  vi.spyOn(client, "call").mockImplementation((invocations) => {
    const calls = invocations as [string, Record<string, unknown>, string][];
    sent.push(...calls);
    // A resolved promise rather than an `async` body: there is nothing to
    // await here, and the lint rule that says so is right.
    return Promise.resolve({
      methodResponses: calls.map(([name, , id]) => [name, responses[id] ?? {}, id]),
    } as never);
  });
  return { client, sent };
}

const ROW: Email = { id: "e1", threadId: "t1", subject: "hi" };
const THREAD: Thread = { id: "t1", emailIds: ["e0", "e1"] };

describe("queryEmails", () => {
  it("sends no collapseThreads by default — false is the RFC default", () => {
    // Sending it would spend a parse on a question the server already assumed.
    const { client, sent } = stub({ q: { ids: ["e1"] }, g: { list: [ROW] } });
    void queryEmails(client, ACCOUNT, { kind: "mailbox", mailboxId: "m1" });
    const query = sent.find(([name]) => name === "Email/query");
    expect(query?.[1]).not.toHaveProperty("collapseThreads");
  });

  it("does NOT fetch threads for an uncollapsed list", async () => {
    // One Thread/get per message, to answer a question an uncollapsed list
    // does not ask, is exactly the request this omission prevents.
    const { client, sent } = stub({ q: { ids: ["e1"] }, g: { list: [ROW] } });
    const page = await queryEmails(client, ACCOUNT, { kind: "mailbox", mailboxId: "m1" });
    expect(sent.map(([name]) => name)).toEqual(["Email/query", "Email/get"]);
    expect(page.threads).toEqual([]);
  });

  it("asks for collapseThreads and the threads in ONE batch", async () => {
    const { client, sent } = stub({
      q: { ids: ["e1"] },
      g: { list: [ROW] },
      th: { list: [THREAD] },
    });
    const page = await queryEmails(client, ACCOUNT, { kind: "mailbox", mailboxId: "m1" }, {
      collapseThreads: true,
    });

    expect(sent.map(([name]) => name)).toEqual(["Email/query", "Email/get", "Thread/get"]);
    expect(sent[0]?.[1].collapseThreads).toBe(true);
    // The Thread/get names its ids by back-reference into the Email/get, so the
    // server resolves all three without a round trip in between.
    expect(sent[2]?.[1]["#ids"]).toEqual({
      resultOf: "g",
      name: "Email/get",
      path: "/list/*/threadId",
    });
    expect(page.threads).toEqual([THREAD]);
  });

  it("degrades to an empty thread list when Thread/get fails", async () => {
    // The sizes are a nicety: a list that renders without them shows the window
    // count instead of the total. A list that does not render at all is worse.
    const { client } = stub({
      q: { ids: ["e1"] },
      g: { list: [ROW] },
      // No "th" entry: `responseFor` throws for the missing call.
    });
    const page = await queryEmails(client, ACCOUNT, { kind: "mailbox", mailboxId: "m1" }, {
      collapseThreads: true,
    });
    expect(page.emails).toEqual([ROW]);
    expect(page.threads).toEqual([]);
  });

  it("collapses together with the [hasKeyword, receivedAt] sort", async () => {
    /*
     * The pair a real client opens every folder with, and the one combination
     * E1's UI half depends on: the server's own
     * TestQueryCollapseWithTheKeywordSort pins the other side of this.
     */
    const { client, sent } = stub({
      q: { ids: ["e1"] },
      g: { list: [ROW] },
      th: { list: [THREAD] },
    });
    await queryEmails(client, ACCOUNT, { kind: "mailbox", mailboxId: "m1" }, {
      collapseThreads: true,
      sort: [
        { property: "hasKeyword", keyword: "$seen", isAscending: true },
        { property: "receivedAt", isAscending: false },
      ],
    });
    expect(sent[0]?.[1].collapseThreads).toBe(true);
    expect(sent[0]?.[1].sort).toHaveLength(2);
  });

  it("never sends the one sort the server refuses to collapse", () => {
    /*
     * The server declines `collapseThreads` with the `relevance` sort, because
     * that sort ranks a bounded recent window rather than an index order and so
     * has no cursor to page a collapsed result with.
     *
     * This client cannot express that combination: `sortForInboxType` is the
     * ONLY thing that builds a sort, and it emits nothing but hasKeyword and
     * receivedAt. Pinned here so a future sort option cannot quietly introduce
     * the refusal — the failure would be a list that silently falls back to
     * uncollapsed, which nobody would notice.
     */
    for (const inboxType of INBOX_TYPES) {
      const sort = sortForInboxType(inboxType) ?? [];
      for (const comparator of sort) {
        expect(comparator.property).not.toBe("relevance");
      }
    }
  });

  it("restores the query's order rather than trusting Email/get's", async () => {
    // RFC 8620 §5.1 lets a server return records in any order; trusting `list`
    // would produce a subtly shuffled inbox that looks like a server bug.
    const a: Email = { id: "a" };
    const b: Email = { id: "b" };
    const { client } = stub({ q: { ids: ["a", "b"] }, g: { list: [b, a] } });
    const page = await queryEmails(client, ACCOUNT, { kind: "all" });
    expect(page.emails.map((email) => email.id)).toEqual(["a", "b"]);
  });
});

describe("the conversation reader's two fetch stages", () => {
  it("stage 1 asks for rows WITHOUT body values", async () => {
    // The rule that keeps a 24-message thread from costing 24 bodies.
    const { client, sent } = stub({ g: { list: [ROW] } });
    await fetchThreadRows(client, ACCOUNT, ["e0", "e1"]);
    expect(sent[0]?.[1].ids).toEqual(["e0", "e1"]);
    expect(sent[0]?.[1]).not.toHaveProperty("fetchTextBodyValues");
    expect(sent[0]?.[1]).not.toHaveProperty("fetchHTMLBodyValues");
  });

  it("stage 2 asks for full bodies, and for no thread", async () => {
    const { client, sent } = stub({ g: { list: [ROW] } });
    await fetchConversationMessages(client, ACCOUNT, ["e1"]);
    expect(sent).toHaveLength(1);
    expect(sent[0]?.[0]).toBe("Email/get");
    expect(sent[0]?.[1].fetchTextBodyValues).toBe(true);
    expect(sent[0]?.[1].fetchHTMLBodyValues).toBe(true);
    // The caller already holds the thread — that is how it knew to ask.
    expect(sent.some(([name]) => name === "Thread/get")).toBe(false);
  });

  it("sends no request at all for an empty id list", async () => {
    const { client, sent } = stub({});
    expect(await fetchConversationMessages(client, ACCOUNT, [])).toEqual([]);
    expect(await fetchThreadRows(client, ACCOUNT, [])).toEqual([]);
    expect(sent).toHaveLength(0);
  });
});

/**
 * E8 — the label filter's SHAPE, pinned to what the server accepts.
 *
 * `internal/jmap/mail/query.go` translates one `hasKeyword` per filter and
 * refuses the four system flags for reasons it states: `$seen` is only
 * expressible as its negation (`notKeyword`), and `$flagged`/`$answered`/
 * `$draft` live in a bitmask the repertoire has no predicate for. A client that
 * sent one would get `unsupportedFilter` and the user would see an empty list.
 *
 * These tests do not talk to the server; they pin the shape so a change here
 * that would produce a refusal fails in CI instead of in a browser.
 */
describe("the label filter (E8)", () => {
  it("is a bare hasKeyword — a label is cross-cutting, never folder-scoped", () => {
    expect(toJmapFilter({ kind: "label", keyword: "$label:work" })).toEqual({
      hasKeyword: "$label:work",
    });
  });

  it("ANDs with inMailbox when a folder is named — the shape searchFilter accepts", () => {
    expect(toJmapFilter({ kind: "label", keyword: "$label:work", mailboxId: "m1" })).toEqual({
      operator: "AND",
      conditions: [{ inMailbox: "m1" }, { hasKeyword: "$label:work" }],
    });
  });

  it("carries the keyword VERBATIM, including the slash the pointer escaping hides", () => {
    /*
     * The escaping is a JSON-POINTER concern and belongs only to patch keys. A
     * filter value is a plain string, so escaping it here would ask the server
     * for a keyword literally named "work~1clients" — which no message has.
     */
    const keyword = encodeLabelKeyword("work/clients");
    expect(toJmapFilter({ kind: "label", keyword })).toEqual({
      hasKeyword: "$label:work/clients",
    });
  });

  it("never produces a hasKeyword the server refuses", () => {
    /*
     * Every keyword this filter kind can carry comes from `encodeLabelKeyword`,
     * so it always starts with `$label:` and can never BE a system flag. This
     * asserts the property rather than the absence of a call site.
     */
    for (const flag of [KEYWORD_SEEN, KEYWORD_FLAGGED, KEYWORD_ANSWERED, KEYWORD_DRAFT]) {
      const keyword = encodeLabelKeyword(flag);
      expect(keyword).not.toBe(flag);
      expect(keyword.startsWith("$label:")).toBe(true);
    }
  });

  /**
   * "Destacados" — the starred view's wire shape (canon 07 §2).
   *
   * This is the assertion that matters most in this file, because the shape is
   * NOT free: it was verified against the real `handleEmailQuery`, and the
   * server's answers were
   *
   *   {"hasKeyword":"$flagged"}                       -> unsupportedFilter,
   *      "this filter needs an inMailbox or a text condition to be answerable"
   *   {AND:[{inMailbox:m1},{hasKeyword:"$flagged"}]}  -> accepted
   *
   * `$flagged` is answerable at all only because it is an IMAP SYSTEM flag:
   * `applyHasKeyword` routes it through `systemFlagBit` to a bitmask
   * predicate, which E3's `store.Narrowing` gave to every store shape. A user
   * label takes the keywords-array path instead — which is why the two are
   * separate filter kinds rather than one carrying a different string.
   *
   * If this shape ever drifts, the user gets an empty Destacados list with no
   * error, so it is pinned here rather than left to a browser to discover.
   */
  it("builds the starred view as the AND the server accepts, never a bare hasKeyword", () => {
    expect(toJmapFilter({ kind: "starred", mailboxId: "m1" })).toEqual({
      operator: "AND",
      conditions: [{ inMailbox: "m1" }, { hasKeyword: KEYWORD_FLAGGED }],
    });
  });

  it("sends the filter through queryEmails unchanged", () => {
    const { client, sent } = stub({ q: { ids: ["e1"], queryState: "1" }, g: { list: [ROW] } });
    void queryEmails(client, ACCOUNT, { kind: "label", keyword: "$label:work" });
    const query = sent.find(([name]) => name === "Email/query");
    expect(query?.[1].filter).toEqual({ hasKeyword: "$label:work" });
  });
});
