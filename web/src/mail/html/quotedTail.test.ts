import { describe, expect, it } from "vitest";

import {
  findQuotedTailOffset,
  MIN_VISIBLE_CHARS,
  splitQuotedTail,
} from "./quotedTail";

/**
 * The quoted-tail corpus.
 *
 * Every case is a real client's shape, not an invented one: Gmail's
 * attribution + blockquote, Outlook's header block, Apple Mail's "On … wrote:"
 * with a nested blockquote, the localized attribution lines the pilot's own
 * Spanish mail carries, and the bottom-posted reply that must NOT be trimmed.
 *
 * The invariant test at the bottom is the important one: this module may only
 * ever SPLIT the sanitized string, never alter it. That is what lets it run
 * downstream of DOMPurify without becoming a second, unreviewed sanitizer.
 */

const LONG_REPLY =
  "<p>Thanks Ana, that works for me. I have updated the deployment notes " +
  "and pushed the branch.</p>";

describe("finding the quoted tail", () => {
  it("cuts at a Gmail-style attribution line above the blockquote", () => {
    const html =
      LONG_REPLY +
      `<div><div dir="ltr">On Mon, Aug 4, 2026 at 10:11 AM Ana &lt;ana@example.com&gt; wrote:</div>` +
      `<blockquote><p>Can you review the plan?</p></blockquote></div>`;

    const { visible, quoted } = splitQuotedTail(html);
    expect(visible).toContain("Thanks Ana");
    expect(visible).not.toContain("wrote:");
    // The attribution belongs WITH the quote — it is what the quote is
    // attributed to, and leaving it visible above a collapsed block reads as
    // a dangling sentence.
    expect(quoted).toContain("wrote:");
    expect(quoted).toContain("Can you review the plan?");
  });

  it("cuts at a Spanish attribution line", () => {
    const html =
      `<p>Perfecto, lo reviso hoy mismo y te confirmo por la tarde.</p>` +
      `<div>El lun, 4 ago 2026 a las 10:11, Ana escribió:</div>` +
      `<blockquote><p>¿Podés revisar el plan?</p></blockquote>`;

    const { visible, quoted } = splitQuotedTail(html);
    expect(visible).toContain("Perfecto");
    expect(quoted).toContain("escribió:");
    expect(quoted).toContain("¿Podés revisar el plan?");
  });

  it.each([
    ["French", "Le lun. 4 août 2026 à 10:11, Ana a écrit :"],
    ["German", "Am 04.08.2026 um 10:11 schrieb Ana:"],
    ["Italian", "Il giorno lun 4 ago 2026 Ana ha scritto:"],
    ["Portuguese", "Em seg., 4 de ago. de 2026, Ana escreveu:"],
  ])("cuts at a %s attribution line", (_language, attribution) => {
    const html = `${LONG_REPLY}<div>${attribution}</div><blockquote><p>q</p></blockquote>`;
    const { visible, quoted } = splitQuotedTail(html);
    expect(visible).toContain("Thanks Ana");
    expect(quoted).toContain(attribution.slice(-12));
  });

  it("cuts at an Outlook From/Sent header block", () => {
    const html =
      `<p>Approved — go ahead and ship it this afternoon.</p>` +
      `<hr>` +
      `<div><b>From:</b> Ana &lt;ana@example.com&gt;<br>` +
      `<b>Sent:</b> Monday, 4 August 2026 10:11<br>` +
      `<b>To:</b> Diego<br>` +
      `<b>Subject:</b> Plan</div>` +
      `<p>Please review.</p>`;

    const { visible, quoted } = splitQuotedTail(html);
    expect(visible).toContain("Approved");
    expect(quoted).toContain("From:");
    expect(quoted).toContain("Please review.");
  });

  it("cuts at a Spanish Outlook De/Enviado header block", () => {
    const html =
      `<p>De acuerdo, seguimos con ese plan la semana que viene.</p>` +
      `<div>De: Ana &lt;ana@example.com&gt;<br>Enviado: lunes, 4 de agosto de 2026<br>Para: Diego</div>` +
      `<p>Revisá el plan.</p>`;

    const { quoted } = splitQuotedTail(html);
    expect(quoted).toContain("De:");
    expect(quoted).toContain("Revisá el plan.");
  });

  it("cuts at an explicit Original Message separator", () => {
    const html =
      `<p>Forwarding this along for your records, nothing needed from you.</p>` +
      `<p>-----Original Message-----</p><p>the original text</p>`;

    const { visible, quoted } = splitQuotedTail(html);
    expect(visible).toContain("Forwarding this along");
    expect(quoted).toContain("Original Message");
  });

  it("cuts at a trailing blockquote with no attribution line", () => {
    const html = `${LONG_REPLY}<blockquote><p>the older message</p></blockquote>`;
    const { visible, quoted } = splitQuotedTail(html);
    expect(visible).toBe(LONG_REPLY);
    expect(quoted).toBe("<blockquote><p>the older message</p></blockquote>");
  });

  it("handles a nested blockquote chain as ONE tail", () => {
    // The compounding shape: each round trip adds a level, and all of it is
    // one quote as far as the reader is concerned.
    const html =
      LONG_REPLY +
      `<blockquote><p>second</p><blockquote><p>first</p></blockquote></blockquote>`;
    const { visible, quoted } = splitQuotedTail(html);
    expect(visible).toBe(LONG_REPLY);
    expect(quoted).toContain("second");
    expect(quoted).toContain("first");
    // ONE cut, not two: the outer blockquote is the boundary.
    expect(quoted.startsWith("<blockquote>")).toBe(true);
  });

  it("ignores trailing empty padding after the quote", () => {
    const html = `${LONG_REPLY}<blockquote><p>old</p></blockquote><div>&nbsp;</div><br>`;
    const { quoted } = splitQuotedTail(html);
    expect(quoted).toContain("old");
  });
});

describe("refusing to trim", () => {
  it("leaves a message with no quote untouched", () => {
    const html = "<p>A plain message with no quoted material at all.</p>";
    expect(splitQuotedTail(html)).toEqual({ visible: html, quoted: "" });
    expect(findQuotedTailOffset(html)).toBeUndefined();
  });

  it("does NOT trim a quote the author replied BELOW (bottom-posting)", () => {
    // The classic destructive false positive: the reply is under the quote,
    // so hiding the quote's tail would hide the actual answer.
    const html =
      `<blockquote><p>Can you review the plan?</p></blockquote>` +
      `<p>Yes — I reviewed it this morning and left two comments on the spec.</p>`;
    expect(findQuotedTailOffset(html)).toBeUndefined();
  });

  it("does NOT trim an inline blockquote the author answered around", () => {
    const html =
      `<p>Answers inline below, shout if anything is unclear.</p>` +
      `<blockquote><p>When?</p></blockquote>` +
      `<p>Thursday.</p>` +
      `<blockquote><p>Where?</p></blockquote>` +
      `<p>The usual room.</p>`;
    expect(findQuotedTailOffset(html)).toBeUndefined();
  });

  it("does NOT trim when almost nothing would remain visible", () => {
    // A forward, or a one-word ack: the quote IS the content.
    const html = `<p>ok</p><blockquote><p>a very long original message body</p></blockquote>`;
    expect(findQuotedTailOffset(html)).toBeUndefined();
  });

  it("does not treat the word 'wrote' in prose as an attribution", () => {
    const html =
      "<p>I wrote the report yesterday and it is ready for your review now.</p>";
    expect(findQuotedTailOffset(html)).toBeUndefined();
  });

  it("does not treat a lone From: in prose as an Outlook divider", () => {
    const html =
      "<p>The label should read From: the archive, not From the archive.</p>";
    expect(findQuotedTailOffset(html)).toBeUndefined();
  });

  it("refuses unbalanced blockquote markup rather than guessing", () => {
    const html = `${LONG_REPLY}</blockquote><p>stray</p>`;
    expect(findQuotedTailOffset(html)).toBeUndefined();
  });

  it("respects MIN_VISIBLE_CHARS exactly at the boundary", () => {
    const short = "x".repeat(MIN_VISIBLE_CHARS - 1);
    const long = "x".repeat(MIN_VISIBLE_CHARS);
    const quote = "<blockquote><p>older</p></blockquote>";
    expect(findQuotedTailOffset(`<p>${short}</p>${quote}`)).toBeUndefined();
    expect(findQuotedTailOffset(`<p>${long}</p>${quote}`)).toBeDefined();
  });
});

describe("the split invariant", () => {
  const corpus = [
    "<p>plain</p>",
    `${LONG_REPLY}<blockquote><p>old</p></blockquote>`,
    `${LONG_REPLY}<div>On Mon, Ana wrote:</div><blockquote><p>old</p></blockquote>`,
    `<p>Approved and ready to go out today.</p><hr><div>From: a<br>Sent: b</div>`,
    "<blockquote><p>q</p></blockquote><p>bottom posted reply text here</p>",
    "",
    "<p>&nbsp;</p>",
  ];

  it("never alters a single byte — the halves concatenate to the input", () => {
    /*
     * THE property that makes this module safe downstream of the sanitizer.
     * If it ever fails, this module has become a markup rewriter and has to be
     * re-reviewed as part of the sanitization pipeline rather than as
     * presentation.
     */
    for (const html of corpus) {
      const { visible, quoted } = splitQuotedTail(html);
      expect(visible + quoted).toBe(html);
    }
  });

  it("never returns a quoted half without a visible half", () => {
    for (const html of corpus) {
      const { visible, quoted } = splitQuotedTail(html);
      if (quoted !== "") expect(visible).not.toBe("");
    }
  });

  it("is stable: splitting the visible half again finds nothing new", () => {
    // Guards against a rule that would keep eating the message one call at a
    // time if a caller ever looped.
    const html = `${LONG_REPLY}<div>On Mon, Ana wrote:</div><blockquote><p>old</p></blockquote>`;
    const once = splitQuotedTail(html);
    const twice = splitQuotedTail(once.visible);
    expect(twice.quoted).toBe("");
  });

  it("terminates promptly on pathological input", () => {
    /*
     * E4's fuzzing found a superlinear-blowup DoS in a parser; the bounded
     * `[^]{0,400}` windows in the attribution patterns exist so this module
     * cannot repeat it. A deeply nested, heavily punctuated body must not
     * take measurable time.
     */
    const nested = "<blockquote>".repeat(500) + "On x wrote:".repeat(200) + "</blockquote>".repeat(500);
    const started = Date.now();
    splitQuotedTail(nested);
    expect(Date.now() - started).toBeLessThan(1000);
  });
});
