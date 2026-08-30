import { describe, expect, it } from "vitest";

import { buildSrcDoc, MESSAGE_SANDBOX } from "./srcdoc";

/*
 * Layer 3's pinned properties. The sandbox grant set and the CSP are the
 * whole security value of this file, so they are pinned by name: a change to
 * either that weakens isolation must fail a test rather than pass review.
 */

describe("the message sandbox", () => {
  it("grants neither allow-scripts nor allow-same-origin", () => {
    // The pair together voids the sandbox; each alone is still refused.
    expect(MESSAGE_SANDBOX).not.toContain("allow-scripts");
    expect(MESSAGE_SANDBOX).not.toContain("allow-same-origin");
  });

  it("grants nothing beyond escaping popups", () => {
    const grants = MESSAGE_SANDBOX.split(/\s+/).filter(Boolean).sort();
    expect(grants).toEqual([
      "allow-popups",
      "allow-popups-to-escape-sandbox",
    ]);
    // Explicitly not present: forms, modals, top navigation, downloads,
    // pointer lock, presentation — each a capability mail content must lack.
    for (const forbidden of [
      "allow-forms", "allow-modals", "allow-top-navigation",
      "allow-top-navigation-by-user-activation", "allow-downloads",
      "allow-pointer-lock", "allow-presentation", "allow-orientation-lock",
    ]) {
      expect(MESSAGE_SANDBOX).not.toContain(forbidden);
    }
  });
});

describe("the CSP", () => {
  const cspOf = (doc: string): string => {
    const match = /content="([^"]*Content-Security[^"]*)?"/i.exec(doc);
    const meta = /http-equiv="Content-Security-Policy" content="([^"]*)"/i.exec(doc);
    void match;
    return meta?.[1] ?? "";
  };

  it("starts from default-src 'none'", () => {
    const csp = cspOf(buildSrcDoc("<p>x</p>", "https://mail.example.com"));
    expect(csp).toContain("default-src 'none'");
  });

  it("permits images only from data: and our own image proxy", () => {
    const csp = cspOf(buildSrcDoc("<p>x</p>", "https://mail.example.com"));
    expect(csp).toContain("img-src data: https://mail.example.com/jmap/imgproxy");
    // No wildcard: the ONLY https host named is our own origin's proxy path,
    // never a scheme-wide `https:` grant that would allow any host.
    expect(csp).not.toContain("img-src *");
    expect(csp).not.toMatch(/img-src[^;]*\shttps:(?:\s|;|$)/);
  });

  it("forbids form submission and rebasing", () => {
    const csp = cspOf(buildSrcDoc("<p>x</p>", "https://mail.example.com"));
    expect(csp).toContain("form-action 'none'");
    expect(csp).toContain("base-uri 'none'");
  });

  it("never grants script-src", () => {
    const csp = cspOf(buildSrcDoc("<p>x</p>", "https://mail.example.com"));
    expect(csp).not.toContain("script-src");
    expect(csp).not.toContain("'unsafe-eval'");
  });

  it("falls back to a network-free img-src for an unusable origin", () => {
    // Attack surface reduction: a garbage origin must not widen img-src.
    const csp = cspOf(buildSrcDoc("<p>x</p>", ""));
    expect(csp).toContain("img-src data:");
    expect(csp).not.toContain("/jmap/imgproxy");
  });
});

describe("the document shell", () => {
  it("declares utf-8 and its head before any body content", () => {
    const doc = buildSrcDoc("<p>MARKER</p>", "https://mail.example.com");
    expect(doc.indexOf("charset")).toBeLessThan(doc.indexOf("MARKER"));
    expect(doc.indexOf("Content-Security-Policy")).toBeLessThan(doc.indexOf("MARKER"));
    expect(doc).toContain("color-scheme: light");
  });

  it("embeds the sanitized markup verbatim (it does not re-sanitize)", () => {
    // buildSrcDoc adds isolation, not sanitization; its input is already clean.
    const doc = buildSrcDoc("<p>hello</p>", "https://mail.example.com");
    expect(doc).toContain("<p>hello</p>");
  });
});

/**
 * The quoted tail (L3 epic E1). The mechanism has to add ZERO capability to
 * the frame — no script, no same-origin — so the only thing it can do is
 * build a different document. These pin that it does exactly that.
 */
describe("the quoted tail", () => {
  const origin = "https://mail.example.com";
  const visible = "<p>the reply</p>";
  const quoted = "<blockquote><p>the quoted original</p></blockquote>";

  it("omits the tail entirely when it is not shown", () => {
    const doc = buildSrcDoc(visible, origin, { quotedHtml: quoted, showQuoted: false });
    expect(doc).toContain("the reply");
    // NOT merely hidden with CSS: the bytes are absent, so a select-all inside
    // the frame cannot copy text the reader was told was trimmed away.
    expect(doc).not.toContain("the quoted original");
    expect(doc).not.toContain("moov-quoted");
  });

  it("emits the tail in its own wrapper when shown", () => {
    const doc = buildSrcDoc(visible, origin, { quotedHtml: quoted, showQuoted: true });
    expect(doc).toContain("the reply");
    expect(doc).toContain('<div class="moov-quoted">');
    expect(doc).toContain("the quoted original");
    // The order is the message's own order: reply first, quote after.
    expect(doc.indexOf("the reply")).toBeLessThan(doc.indexOf("the quoted original"));
  });

  it("shown or not, it never changes the isolation properties", () => {
    for (const showQuoted of [true, false]) {
      const doc = buildSrcDoc(visible, origin, { quotedHtml: quoted, showQuoted });
      expect(doc).toContain("default-src 'none'");
      expect(doc).not.toContain("script-src");
      expect(doc).toContain("form-action 'none'");
    }
  });

  it("adds no quote styling to a message that has no tail", () => {
    const doc = buildSrcDoc(visible, origin, { quotedHtml: "", showQuoted: true });
    expect(doc).not.toContain("moov-quoted");
  });

  it("is identical to the two-argument form when no tail is given", () => {
    // Backwards compatibility as a test, not a promise: every pre-E1 call site
    // keeps producing exactly the document it produced before.
    expect(buildSrcDoc(visible, origin, {})).toBe(buildSrcDoc(visible, origin));
  });

  it("reproduces the sanitizer's output exactly when the tail is shown", () => {
    /*
     * The whole-pipeline invariant: split + reassemble is the identity, so a
     * shown quote renders precisely the markup the sanitizer approved —
     * nothing added between the halves, nothing dropped.
     */
    const doc = buildSrcDoc(visible, origin, { quotedHtml: quoted, showQuoted: true });
    const body = doc.slice(doc.indexOf("<body>") + "<body>".length, doc.indexOf("</body>"));
    expect(body.replace('<div class="moov-quoted">', "").replace(/<\/div>$/, "")).toBe(
      visible + quoted,
    );
  });
});
