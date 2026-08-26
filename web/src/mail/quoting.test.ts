import { describe, expect, it } from "vitest";

import {
  attributionFor,
  bodyAsHtml,
  bodyAsText,
  escapeHtml,
  forwardSubject,
  htmlToText,
  quoteHtml,
  quoteText,
  replyHeaders,
  replyRecipients,
  replySubject,
  textToHtml,
} from "./quoting";
import type { Email } from "./types";

function email(overrides: Partial<Email> = {}): Email {
  return {
    id: "e1",
    from: [{ name: "Ana", email: "ana@x.com" }],
    to: [{ name: null, email: "me@moov.test" }],
    subject: "Presupuesto",
    receivedAt: "2026-08-20T10:00:00Z",
    ...overrides,
  };
}

describe("replySubject", () => {
  it("prefixes Re:", () => {
    expect(replySubject("Presupuesto")).toBe("Re: Presupuesto");
  });

  it("does not stack a second Re:", () => {
    expect(replySubject("Re: Presupuesto")).toBe("Re: Presupuesto");
    expect(replySubject("RE:  Presupuesto")).toBe("RE:  Presupuesto");
  });

  it("handles an absent subject without producing 'Re: undefined'", () => {
    expect(replySubject(null)).toBe("Re:");
    expect(replySubject(undefined)).toBe("Re:");
    expect(replySubject("   ")).toBe("Re:");
  });
});

describe("forwardSubject", () => {
  it("prefixes Fwd: and does not stack", () => {
    expect(forwardSubject("Nota")).toBe("Fwd: Nota");
    expect(forwardSubject("Fwd: Nota")).toBe("Fwd: Nota");
    expect(forwardSubject("FW: Nota")).toBe("FW: Nota");
    // Spanish and German clients emit these; stacking them looks broken.
    expect(forwardSubject("RV: Nota")).toBe("RV: Nota");
    expect(forwardSubject("WG: Nota")).toBe("WG: Nota");
  });
});

describe("replyRecipients", () => {
  it("replies to From when there is no Reply-To", () => {
    const { to, cc } = replyRecipients(email(), "me@moov.test", false);
    expect(to.map((chip) => chip.email)).toEqual(["ana@x.com"]);
    expect(cc).toEqual([]);
  });

  /*
   * RFC 5322 §3.6.2 exists so a sender can direct replies elsewhere. Ignoring
   * it sends the reply to a mailbox nobody reads — the classic bug with
   * ticketing addresses and mailing lists.
   */
  it("honours Reply-To over From", () => {
    const message = email({ replyTo: [{ name: null, email: "tickets@x.com" }] });
    const { to } = replyRecipients(message, "me@moov.test", false);
    expect(to.map((chip) => chip.email)).toEqual(["tickets@x.com"]);
  });

  it("reply-all adds To and Cc, excluding the account and the To list", () => {
    const message = email({
      to: [
        { name: null, email: "me@moov.test" },
        { name: "Bea", email: "bea@y.com" },
      ],
      cc: [{ name: null, email: "carlos@z.com" }],
    });
    const { to, cc } = replyRecipients(message, "me@moov.test", true);
    expect(to.map((chip) => chip.email)).toEqual(["ana@x.com"]);
    // Not me@moov.test (that is us), not ana@x.com (already in To).
    expect(cc.map((chip) => chip.email)).toEqual(["bea@y.com", "carlos@z.com"]);
  });

  it("reply-all excludes the account case-insensitively", () => {
    const message = email({ to: [{ name: null, email: "ME@MOOV.test" }] });
    const { cc } = replyRecipients(message, "me@moov.test", true);
    expect(cc).toEqual([]);
  });

  it("never puts the same address in both To and Cc", () => {
    const message = email({ cc: [{ name: null, email: "ana@x.com" }] });
    const { to, cc } = replyRecipients(message, "me@moov.test", true);
    const overlap = to
      .map((chip) => chip.email)
      .filter((address) => cc.some((chip) => chip.email === address));
    expect(overlap).toEqual([]);
  });
});

describe("replyHeaders", () => {
  it("builds In-Reply-To and References per RFC 5322 §3.6.4", () => {
    const parent = email({
      messageId: ["<parent@x.com>"],
      references: ["<root@x.com>", "<mid@x.com>"],
    });
    expect(replyHeaders(parent)).toEqual({
      inReplyTo: ["<parent@x.com>"],
      references: ["<root@x.com>", "<mid@x.com>", "<parent@x.com>"],
    });
  });

  it("starts the chain when the parent has no References", () => {
    const parent = email({ messageId: ["<only@x.com>"] });
    expect(replyHeaders(parent)).toEqual({
      inReplyTo: ["<only@x.com>"],
      references: ["<only@x.com>"],
    });
  });

  /*
   * A message with no Message-ID cannot be threaded onto. Inventing one would
   * thread the reply onto nothing; the honest outcome is a new conversation.
   */
  it("returns empty headers when the parent has no Message-ID", () => {
    expect(replyHeaders(email())).toEqual({ inReplyTo: [], references: [] });
  });

  it("caps the chain but keeps the thread ROOT, which threading anchors on", () => {
    const references = Array.from({ length: 40 }, (_, index) => `<r${index}@x.com>`);
    const parent = email({ messageId: ["<parent@x.com>"], references });
    const result = replyHeaders(parent, 10);
    expect(result.references).toHaveLength(10);
    expect(result.references[0]).toBe("<r0@x.com>");
    expect(result.references.at(-1)).toBe("<parent@x.com>");
  });
});

describe("quoteText", () => {
  it("prefixes every line", () => {
    expect(quoteText("uno\ndos")).toBe("> uno\n> dos");
  });

  it("deepens an existing quote without adding a space each level", () => {
    expect(quoteText("> uno")).toBe(">> uno");
  });

  it("handles CRLF, which is what arrives over the wire", () => {
    expect(quoteText("uno\r\ndos")).toBe("> uno\n> dos");
  });

  it("quotes a blank line rather than dropping it", () => {
    expect(quoteText("uno\n\ndos")).toBe("> uno\n> \n> dos");
  });
});

describe("escapeHtml", () => {
  it("neutralises markup in a display name", () => {
    expect(escapeHtml('<img src=x onerror="alert(1)">')).toBe(
      "&lt;img src=x onerror=&quot;alert(1)&quot;&gt;",
    );
  });

  it("escapes the ampersand FIRST so escapes are not double-escaped wrongly", () => {
    expect(escapeHtml("a & <b>")).toBe("a &amp; &lt;b&gt;");
  });
});

describe("quoteHtml", () => {
  it("escapes the attribution line but embeds the original verbatim", () => {
    const html = quoteHtml('On <date>, "Ana" wrote:', "<p>hola</p>");
    expect(html).toContain("&lt;date&gt;");
    expect(html).toContain("<p>hola</p>");
    expect(html).toContain('type="cite"');
  });
});

describe("textToHtml", () => {
  it("turns blank lines into paragraphs and single newlines into breaks", () => {
    expect(textToHtml("uno\ndos\n\ntres")).toBe("<p>uno<br>dos</p><p>tres</p>");
  });

  it("escapes the text it wraps", () => {
    expect(textToHtml("<script>")).toBe("<p>&lt;script&gt;</p>");
  });
});

describe("htmlToText", () => {
  it("decodes entities, which a tag-stripping regex cannot", () => {
    expect(htmlToText("<p>a &amp; b</p>")).toBe("a & b");
  });

  it("breaks blocks onto separate lines", () => {
    expect(htmlToText("<p>uno</p><p>dos</p>")).toBe("uno\n\ndos");
  });

  it("turns <br> into a newline", () => {
    expect(htmlToText("uno<br>dos")).toBe("uno\ndos");
  });

  it("drops script and style content entirely", () => {
    expect(htmlToText("<p>hola</p><script>alert(1)</script><style>p{}</style>")).toBe("hola");
  });

  it("does not execute anything — an onerror image contributes no text", () => {
    expect(htmlToText('<img src=x onerror="throw new Error(\'ran\')">')).toBe("");
  });
});

describe("bodyAsText / bodyAsHtml", () => {
  it("prefers the text part when present", () => {
    const message = email({
      textBody: [{ partId: "0", blobId: null, size: 3, name: null, type: "text/plain", charset: null, disposition: null, cid: null, language: null, location: null }],
      bodyValues: { "0": { value: "hola", isEncodingProblem: false, isTruncated: false } },
    });
    expect(bodyAsText(message)).toBe("hola");
  });

  it("falls back to converting the HTML when there is no text part", () => {
    const message = email({
      htmlBody: [{ partId: "1", blobId: null, size: 3, name: null, type: "text/html", charset: null, disposition: null, cid: null, language: null, location: null }],
      bodyValues: { "1": { value: "<p>hola</p>", isEncodingProblem: false, isTruncated: false } },
    });
    expect(bodyAsText(message)).toBe("hola");
    expect(bodyAsHtml(message)).toBe("<p>hola</p>");
  });

  it("returns empty for a parse-failed message rather than throwing", () => {
    expect(bodyAsText(email())).toBe("");
    expect(bodyAsHtml(email())).toBeUndefined();
  });
});

describe("attributionFor", () => {
  it("renders the sender the way a header would", () => {
    expect(attributionFor(email())).toEqual({
      sentAt: "2026-08-20T10:00:00Z",
      sender: "Ana <ana@x.com>",
    });
  });

  it("prefers sentAt over receivedAt — the original's own clock", () => {
    const message = email({ sentAt: "2026-08-19T08:00:00-03:00" });
    expect(attributionFor(message).sentAt).toBe("2026-08-19T08:00:00-03:00");
  });

  it("survives a message with no From", () => {
    expect(attributionFor(email({ from: null })).sender).toBe("");
  });
});
