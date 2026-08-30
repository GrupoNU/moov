import { describe, expect, it, vi } from "vitest";

import { JmapClient } from "../api/jmap";
import {
  fetchSnippets,
  hasHighlight,
  parseSnippet,
  snippetIndex,
} from "./snippet";

/**
 * Snippet parsing (L3 epic E3, RFC 8621 §5).
 *
 * The security block is the point of this file. The server pins a contract —
 * "the only markup is <mark>" — and the client does not trust it, so these
 * tests feed it exactly what a broken or compromised server would send and
 * assert the output is inert TEXT.
 */

describe("parseSnippet", () => {
  it("returns one plain segment when nothing is marked", () => {
    expect(parseSnippet("no matches here")).toEqual([
      { text: "no matches here", isMatch: false },
    ]);
  });

  it("splits around a mark", () => {
    expect(parseSnippet("the <mark>arquitectura</mark> doc")).toEqual([
      { text: "the ", isMatch: false },
      { text: "arquitectura", isMatch: true },
      { text: " doc", isMatch: false },
    ]);
  });

  it("handles several marks", () => {
    expect(parseSnippet("<mark>a</mark> y <mark>b</mark>")).toEqual([
      { text: "a", isMatch: true },
      { text: " y ", isMatch: false },
      { text: "b", isMatch: true },
    ]);
  });

  it("is empty for an absent snippet — §5.1's null", () => {
    expect(parseSnippet(undefined)).toEqual([]);
    expect(parseSnippet(null)).toEqual([]);
    expect(parseSnippet("")).toEqual([]);
  });

  it("degrades an unbalanced mark instead of losing text", () => {
    // The server promises balance; if it ever broke that promise, the text
    // must still all arrive. Highlighting too much is a cosmetic bug; dropping
    // the tail of a preview is a data bug.
    expect(parseSnippet("start <mark>never closed")).toEqual([
      { text: "start ", isMatch: false },
      { text: "never closed", isMatch: true },
    ]);
  });

  it("decodes the five entities Go's html.EscapeString produces", () => {
    expect(parseSnippet("R&amp;D &lt;ok&gt; &quot;x&quot; &#39;y&#39;")).toEqual([
      { text: `R&D <ok> "x" 'y'`, isMatch: false },
    ]);
  });

  it("decodes &amp; LAST, so a double escape cannot become a tag", () => {
    // "&amp;lt;script&amp;gt;" must decode to the literal "&lt;script&gt;",
    // NOT to "<script>". Decoding the ampersand first is the classic bug.
    expect(parseSnippet("&amp;lt;script&amp;gt;")[0]?.text).toBe("&lt;script&gt;");
  });

  it("hasHighlight reports whether anything actually matched", () => {
    expect(hasHighlight(parseSnippet("plain"))).toBe(false);
    expect(hasHighlight(parseSnippet("<mark>hit</mark>"))).toBe(true);
  });
});

describe("SECURITY: a hostile snippet stays text", () => {
  /*
   * The threat model, stated plainly: the store proved (internal/store/
   * snippets.go) that PostgreSQL's ts_headline passes `<script>` through
   * untouched, so a naive `StartSel=<mark>` implementation is a stored XSS.
   * The server closes that by escaping BEFORE re-introducing its own marks.
   *
   * These tests assert the CLIENT is safe even if that had been done wrong —
   * because the client never treats a snippet as markup at all.
   */

  it("a <script> tag arrives as literal text in a single segment", () => {
    const hostile = "<script>alert(1)</script>";
    const segments = parseSnippet(hostile);
    expect(segments).toEqual([{ text: "<script>alert(1)</script>", isMatch: false }]);
    // The critical property: there is exactly ONE segment and it is not a
    // match, so nothing about the string was interpreted as structure.
    expect(segments).toHaveLength(1);
  });

  it("an <img onerror> arrives as literal text", () => {
    const hostile = '<img src=x onerror="alert(1)">';
    expect(parseSnippet(hostile)).toEqual([
      { text: '<img src=x onerror="alert(1)">', isMatch: false },
    ]);
  });

  it("a mark with attributes is NOT a mark — only the exact token splits", () => {
    /*
     * `<mark onmouseover=…>` is not the literal token `<mark>`, so the scanner
     * never enters mark state — and therefore never looks for a closing token
     * either. The ENTIRE string, closing tag included, comes back as one
     * unmarked text segment. A parser matching `<mark` loosely would instead
     * have created an element and then had to decide what to do with an
     * attacker's attributes.
     */
    const hostile = '<mark onmouseover="alert(1)">x</mark>';
    const segments = parseSnippet(hostile);
    expect(segments).toEqual([{ text: hostile, isMatch: false }]);
  });

  it("nested marks cannot produce anything but text segments", () => {
    const segments = parseSnippet("<mark>a<mark>b</mark>c</mark>");
    // Whatever the isMatch flags end up being, every entry is a plain string.
    for (const segment of segments) {
      expect(typeof segment.text).toBe("string");
      expect(typeof segment.isMatch).toBe("boolean");
    }
    /*
     * The exact trace, because "nothing is lost" is the property that matters
     * and it should be asserted as a fact rather than approximated:
     *
     *   <mark>   opens a run
     *   a        marked text
     *   <mark>   the scanner is looking for a CLOSE, so this is text
     *   b        still marked
     *   </mark>  closes the run
     *   c        unmarked
     *   </mark>  no run is open, so this is text too
     *
     * Both stray tokens survive as characters, which is what "the output is
     * always a list of strings" means in practice: a malformed snippet can
     * mis-highlight, and can never drop a user's text or create an element.
     */
    expect(segments).toEqual([
      { text: "a<mark>b", isMatch: true },
      { text: "c</mark>", isMatch: false },
    ]);
  });

  it("an escaped script (what the server really sends) decodes to visible text", () => {
    // This is the REAL shape: the server escapes the angle brackets, so the
    // user sees the tag as text — which is the correct rendering of a message
    // that genuinely contains that string.
    const escaped = "&lt;script&gt;alert(1)&lt;/script&gt;";
    expect(parseSnippet(escaped)).toEqual([
      { text: "<script>alert(1)</script>", isMatch: false },
    ]);
  });
});

describe("fetchSnippets", () => {
  const ACCOUNT = "a";

  function stub(response: unknown) {
    const sent: [string, Record<string, unknown>, string][] = [];
    const client = new JmapClient({ username: "u", password: "p" });
    vi.spyOn(client, "call").mockImplementation((invocations) => {
      const calls = invocations as [string, Record<string, unknown>, string][];
      sent.push(...calls);
      return Promise.resolve({
        methodResponses: calls.map(([name, , id]) => [name, response, id]),
      } as never);
    });
    return { client, sent };
  }

  it("sends the SAME filter the query used — §5.1 requires it", async () => {
    const { client, sent } = stub({ list: [] });
    const filter = { operator: "AND", conditions: [{ text: "x" }] };
    await fetchSnippets(client, ACCOUNT, filter, ["e1", "e2"]);
    expect(sent).toEqual([
      ["SearchSnippet/get", { accountId: ACCOUNT, filter, emailIds: ["e1", "e2"] }, "s"],
    ]);
  });

  it("sends nothing at all for an empty id list", async () => {
    const { client, sent } = stub({ list: [] });
    expect(await fetchSnippets(client, ACCOUNT, null, [])).toEqual([]);
    expect(sent).toEqual([]);
  });

  it("reads §5's nullable subject and preview as undefined", async () => {
    const { client } = stub({
      list: [
        { emailId: "e1", subject: "<mark>x</mark>", preview: null },
        { emailId: "e2", subject: null, preview: "hit" },
      ],
    });
    expect(await fetchSnippets(client, ACCOUNT, null, ["e1", "e2"])).toEqual([
      { emailId: "e1", subject: "<mark>x</mark>", preview: undefined },
      { emailId: "e2", subject: undefined, preview: "hit" },
    ]);
  });

  it("degrades to no highlighting when the server refuses the method", async () => {
    /*
     * An older server answers `unknownMethod`. A result list without
     * highlighting is perfectly usable, so this must never fail the search.
     */
    const sent: unknown[] = [];
    const client = new JmapClient({ username: "u", password: "p" });
    vi.spyOn(client, "call").mockImplementation((invocations) => {
      sent.push(invocations);
      return Promise.resolve({
        methodResponses: [["error", { type: "unknownMethod" }, "s"]],
      } as never);
    });
    expect(await fetchSnippets(client, ACCOUNT, null, ["e1"])).toEqual([]);
  });

  it("discards malformed rows rather than trusting the shape", async () => {
    const { client } = stub({ list: [null, 7, { subject: "no id" }, { emailId: "e1" }] });
    expect(await fetchSnippets(client, ACCOUNT, null, ["e1"])).toEqual([
      { emailId: "e1", subject: undefined, preview: undefined },
    ]);
  });

  it("survives a list that is not a list", async () => {
    const { client } = stub({ list: "nope" });
    expect(await fetchSnippets(client, ACCOUNT, null, ["e1"])).toEqual([]);
  });
});

describe("snippetIndex", () => {
  it("indexes by email id so a row looks its own up", () => {
    const index = snippetIndex([
      { emailId: "e1", subject: "a", preview: undefined },
      { emailId: "e2", subject: undefined, preview: "b" },
    ]);
    expect(index.get("e1")?.subject).toBe("a");
    expect(index.get("e3")).toBeUndefined();
  });
});
