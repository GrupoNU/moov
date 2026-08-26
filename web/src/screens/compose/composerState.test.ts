import { describe, expect, it } from "vitest";

import {
  draftTo,
  forwardDraft,
  newDraft,
  replyDraft,
  resumeDraft,
  type QuotingStrings,
} from "./composerState";
import type { Email, EmailBodyPart } from "../../mail/types";

const strings: QuotingStrings = {
  attributionLine: (date, sender) => `On ${date}, ${sender} wrote:`,
  forwardedHeader: "---------- Forwarded message ----------",
  from: "From",
  date: "Date",
  subject: "Subject",
  to: "To",
  formatDate: (isoDate) => isoDate ?? "",
};

function part(partId: string, type: string): EmailBodyPart {
  return {
    partId,
    blobId: null,
    size: 10,
    name: null,
    type,
    charset: null,
    disposition: null,
    cid: null,
    language: null,
    location: null,
  };
}

function message(overrides: Partial<Email> = {}): Email {
  return {
    id: "e1",
    from: [{ name: "Ana", email: "ana@x.com" }],
    to: [{ name: null, email: "me@moov.test" }],
    subject: "Presupuesto",
    sentAt: "2026-08-20T10:00:00Z",
    receivedAt: "2026-08-20T10:00:05Z",
    messageId: ["<orig@x.com>"],
    textBody: [part("0", "text/plain")],
    bodyValues: { "0": { value: "El presupuesto adjunto.", isEncodingProblem: false, isTruncated: false } },
    ...overrides,
  };
}

describe("newDraft", () => {
  it("is blank and focuses the recipient field", () => {
    const draft = newDraft(true);
    expect(draft.to).toEqual([]);
    expect(draft.subject).toBe("");
    expect(draft.focusField).toBe("to");
    expect(draft.existingDraftId).toBeUndefined();
  });

  it("starts in plain text when rich is not preferred", () => {
    expect(newDraft(false).html).toBeUndefined();
    expect(newDraft(true).html).toBe("");
  });

  it("gives each composition a distinct seed, so the editor re-seeds", () => {
    expect(newDraft(true).seedKey).not.toBe(newDraft(true).seedKey);
  });
});

describe("replyDraft", () => {
  it("addresses the sender, prefixes Re:, and threads", () => {
    const draft = replyDraft(message(), "me@moov.test", false, strings);
    expect(draft.to.map((chip) => chip.email)).toEqual(["ana@x.com"]);
    expect(draft.subject).toBe("Re: Presupuesto");
    expect(draft.inReplyTo).toEqual(["<orig@x.com>"]);
    expect(draft.references).toEqual(["<orig@x.com>"]);
  });

  /*
   * Top-posting: the caret must land ABOVE the quote, where the user writes.
   * A body that starts with the quoted text puts the reply under a wall of
   * other people's words.
   */
  it("opens with blank space above the quote", () => {
    const draft = replyDraft(message(), "me@moov.test", false, strings);
    expect(draft.text.startsWith("\n\n")).toBe(true);
    expect(draft.html?.startsWith("<p><br></p>")).toBe(true);
  });

  it("quotes the original with an attribution line", () => {
    const draft = replyDraft(message(), "me@moov.test", false, strings);
    expect(draft.text).toContain("On 2026-08-20T10:00:00Z, Ana <ana@x.com> wrote:");
    expect(draft.text).toContain("> El presupuesto adjunto.");
  });

  it("focuses the body — the recipients and subject are already right", () => {
    expect(replyDraft(message(), "me@moov.test", false, strings).focusField).toBe("body");
  });

  it("reply-all cc's the others and never the account itself", () => {
    const original = message({
      to: [
        { name: null, email: "me@moov.test" },
        { name: "Bea", email: "bea@y.com" },
      ],
      cc: [{ name: null, email: "carlos@z.com" }],
    });
    const draft = replyDraft(original, "me@moov.test", true, strings);
    expect(draft.to.map((chip) => chip.email)).toEqual(["ana@x.com"]);
    expect(draft.cc.map((chip) => chip.email)).toEqual(["bea@y.com", "carlos@z.com"]);
  });

  it("quotes the HTML part verbatim when the original had one", () => {
    const original = message({
      htmlBody: [part("1", "text/html")],
      bodyValues: {
        "0": { value: "texto", isEncodingProblem: false, isTruncated: false },
        "1": { value: "<p>con <b>formato</b></p>", isEncodingProblem: false, isTruncated: false },
      },
    });
    const draft = replyDraft(original, "me@moov.test", false, strings);
    expect(draft.html).toContain("<p>con <b>formato</b></p>");
    expect(draft.html).toContain('type="cite"');
  });

  it("converts a text-only original into HTML for the rich quote", () => {
    const draft = replyDraft(message(), "me@moov.test", false, strings);
    expect(draft.html).toContain("<p>El presupuesto adjunto.</p>");
  });

  /*
   * The attribution line is built from a message's own display name, which is
   * attacker-controlled. It is escaped before it becomes markup — the
   * sanitizer would catch it downstream, but authoring hostile markup and
   * relying on the next layer is building a defect on purpose.
   */
  it("escapes a hostile display name in the HTML attribution", () => {
    const original = message({ from: [{ name: '<img src=x onerror=alert(1)>', email: "a@x.com" }] });
    const draft = replyDraft(original, "me@moov.test", false, strings);
    expect(draft.html).not.toContain("<img src=x");
    expect(draft.html).toContain("&lt;img src=x");
  });
});

describe("forwardDraft", () => {
  it("has no recipients and prefixes Fwd:", () => {
    const draft = forwardDraft(message(), strings);
    expect(draft.to).toEqual([]);
    expect(draft.subject).toBe("Fwd: Presupuesto");
    expect(draft.focusField).toBe("to");
  });

  it("carries the RFC-conventional forwarded header", () => {
    const draft = forwardDraft(message(), strings);
    expect(draft.text).toContain("---------- Forwarded message ----------");
    expect(draft.text).toContain("From: Ana <ana@x.com>");
    expect(draft.text).toContain("Subject: Presupuesto");
    expect(draft.text).toContain("To: me@moov.test");
  });

  /*
   * A forward does NOT thread onto the original: it is a new conversation with
   * a new audience, and threading it into the old one would put the forward in
   * the original participants' view of the thread.
   */
  it("does not thread onto the original", () => {
    const draft = forwardDraft(message(), strings);
    expect(draft.inReplyTo).toBeUndefined();
    expect(draft.references).toBeUndefined();
  });

  it("does not quote-indent the forwarded body — a forward is not a reply", () => {
    expect(forwardDraft(message(), strings).text).toContain("El presupuesto adjunto.");
    expect(forwardDraft(message(), strings).text).not.toContain("> El presupuesto");
  });
});

describe("resumeDraft", () => {
  /*
   * The server id must be carried, or the next save would create a SECOND
   * message instead of replacing this revision — Drafts would accumulate one
   * message per editing session (RFC 8621 §4.6 makes a message immutable).
   */
  it("carries the draft's server id so the next save replaces it", () => {
    const existing = message({ id: "draft-9" });
    expect(resumeDraft(existing).existingDraftId).toBe("draft-9");
  });

  it("restores every recipient field", () => {
    const existing = message({
      to: [{ name: null, email: "a@x.com" }],
      cc: [{ name: null, email: "b@x.com" }],
      bcc: [{ name: null, email: "c@x.com" }],
    });
    const draft = resumeDraft(existing);
    expect(draft.to.map((chip) => chip.email)).toEqual(["a@x.com"]);
    expect(draft.cc.map((chip) => chip.email)).toEqual(["b@x.com"]);
    expect(draft.bcc.map((chip) => chip.email)).toEqual(["c@x.com"]);
  });

  it("restores the body without quoting it", () => {
    expect(resumeDraft(message()).text).toBe("El presupuesto adjunto.");
  });

  it("keeps a reply-draft's threading headers", () => {
    const existing = message({ inReplyTo: ["<p@x.com>"], references: ["<r@x.com>"] });
    const draft = resumeDraft(existing);
    expect(draft.inReplyTo).toEqual(["<p@x.com>"]);
    expect(draft.references).toEqual(["<r@x.com>"]);
  });

  it("handles a null subject without rendering the word null", () => {
    expect(resumeDraft(message({ subject: null })).subject).toBe("");
  });
});

describe("draftTo", () => {
  it("prefills the recipient and moves focus past it", () => {
    const draft = draftTo("ana@x.com", true);
    expect(draft.to.map((chip) => chip.email)).toEqual(["ana@x.com"]);
    expect(draft.focusField).toBe("subject");
  });
});
