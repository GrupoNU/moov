import { describe, expect, it } from "vitest";

import {
  classifyUrl,
  canonicalRemoteUrl,
  sanitizeStyleValue,
  EMAIL_ALLOWED_CSS_PROPERTIES,
  EMAIL_ALLOWED_TAGS,
  EMAIL_ALLOWED_ATTRIBUTES,
} from "./policy";

/*
 * The policy's PURE half: URL classification and the inline-CSS grammar.
 * Each refusal here names its attack; the sanitizer tests then prove the
 * same refusals through the full pipeline.
 */

describe("classifyUrl", () => {
  it("accepts the four link schemes and classifies them", () => {
    expect(classifyUrl("https://example.com/a")).toBe("web");
    expect(classifyUrl("http://example.com/a")).toBe("web");
    expect(classifyUrl("mailto:a@example.com")).toBe("mailto");
    expect(classifyUrl("tel:+541100000000")).toBe("tel");
  });

  it("recognizes raster data: images and cid: references", () => {
    expect(classifyUrl("data:image/png;base64,iVBORw0KGgo=")).toBe("data-image");
    expect(classifyUrl("data:image/jpeg;base64,AAAA")).toBe("data-image");
    expect(classifyUrl("data:image/webp;base64,AAAA")).toBe("data-image");
    expect(classifyUrl("cid:part1@example.com")).toBe("cid");
  });

  it("refuses script-bearing schemes outright", () => {
    // Attack: direct script execution via URL.
    expect(classifyUrl("javascript:alert(1)")).toBe("refused");
    expect(classifyUrl("vbscript:msgbox(1)")).toBe("refused");
  });

  it("refuses the lexical scheme-smuggling family", () => {
    // Attack: hide the scheme from a string comparison; the browser's URL
    // parser strips tab/CR/LF and case-folds, so the check must too.
    expect(classifyUrl("JaVaScRiPt:alert(1)")).toBe("refused");
    expect(classifyUrl("jav\tascript:alert(1)")).toBe("refused");
    expect(classifyUrl("jav\nascript:alert(1)")).toBe("refused");
    expect(classifyUrl("jav\rascript:alert(1)")).toBe("refused");
    expect(classifyUrl("  javascript:alert(1)")).toBe("refused");
    expect(classifyUrl("java\u0000script:alert(1)")).toBe("refused");
    expect(classifyUrl("\u0001javascript:alert(1)")).toBe("refused");
  });

  it("refuses data: that is not a raster image", () => {
    // Attack: data:text/html is a same-document script vector; data SVG is
    // a script container by format.
    expect(classifyUrl("data:text/html,<script>alert(1)</script>")).toBe("refused");
    expect(classifyUrl("data:image/svg+xml,<svg onload=alert(1)/>")).toBe("refused");
    expect(classifyUrl("data:application/octet-stream;base64,AAAA")).toBe("refused");
    // Whitespace tricks inside the data head fail closed.
    expect(classifyUrl("data:image/png\t;base64,AAAA")).toBe("refused");
  });

  it("refuses relative URLs — there is deliberately no base", () => {
    // Attack: inside the srcdoc frame a relative URL resolves against the
    // APP's origin (confused deputy).
    expect(classifyUrl("/jmap/api")).toBe("refused");
    expect(classifyUrl("logo.png")).toBe("refused");
    expect(classifyUrl("../../../etc/passwd")).toBe("refused");
    expect(classifyUrl("#anchor")).toBe("refused");
    expect(classifyUrl("//evil.example/pixel.png")).toBe("refused");
    expect(classifyUrl("")).toBe("refused");
  });

  it("refuses local and context-dependent schemes", () => {
    expect(classifyUrl("file:///etc/passwd")).toBe("refused");
    expect(classifyUrl("blob:https://example.com/x")).toBe("refused");
    expect(classifyUrl("about:blank")).toBe("refused");
    expect(classifyUrl("chrome://settings")).toBe("refused");
    expect(classifyUrl("ftp://example.com/a")).toBe("refused");
  });
});

describe("canonicalRemoteUrl", () => {
  it("canonicalizes host case and default ports", () => {
    expect(canonicalRemoteUrl("HTTPS://Example.COM:443/a")).toBe("https://example.com/a");
  });
  it("returns undefined for non-web URLs", () => {
    expect(canonicalRemoteUrl("javascript:alert(1)")).toBeUndefined();
    expect(canonicalRemoteUrl("not a url")).toBeUndefined();
  });
});

describe("sanitizeStyleValue", () => {
  it("keeps the visual language of email", () => {
    expect(sanitizeStyleValue("color: #333; font-size: 14px")).toBe(
      "color: #333; font-size: 14px",
    );
    expect(sanitizeStyleValue("background-color: rgb(240, 240, 240)")).toBe(
      "background-color: rgb(240, 240, 240)",
    );
    expect(sanitizeStyleValue("width: calc(100% - 20px)")).toBe(
      "width: calc(100% - 20px)",
    );
    expect(sanitizeStyleValue("border: 1px solid #ccc; text-align: center")).toBe(
      "border: 1px solid #ccc; text-align: center",
    );
  });

  it("is a fixed point on everything it accepts (idempotence)", () => {
    const inputs = [
      "color: #333; font-size: 14px",
      "background: rgba(0,0,0,0.5); margin: 0 auto",
      "font-family: Arial, sans-serif",
    ];
    for (const input of inputs) {
      const once = sanitizeStyleValue(input);
      expect(sanitizeStyleValue(once)).toBe(once);
    }
  });

  it("drops url() and every other fetching notation — the Spy Sheets class", () => {
    // Attack: CSS-only exfiltration/tracking via conditional network fetch.
    expect(sanitizeStyleValue("background: url(https://evil.example/p.gif)")).toBe("");
    expect(sanitizeStyleValue("background: url('https://evil.example')")).toBe("");
    expect(sanitizeStyleValue("background: image-set(url(x) 1x)")).toBe("");
    expect(sanitizeStyleValue("color: env(--x)")).toBe("");
    expect(sanitizeStyleValue("width: attr(data-x px)")).toBe("");
    expect(sanitizeStyleValue("color: var(--indirect)")).toBe("");
  });

  it("drops legacy execution notations", () => {
    // Attack: expression() executed script in old IE; behavior/-moz-binding
    // bound code to elements.
    expect(sanitizeStyleValue("width: expression(alert(1))")).toBe("");
    expect(sanitizeStyleValue("behavior: url(#default#time2)")).toBe("");
    expect(sanitizeStyleValue("-moz-binding: url(x)")).toBe("");
  });

  it("drops escape and comment smuggling", () => {
    // Attack: \75rl( is url( after CSS unescaping; comments split keywords.
    expect(sanitizeStyleValue("background: \\75rl(https://evil.example)")).toBe("");
    expect(sanitizeStyleValue("background: u\\rl(x)")).toBe("");
    expect(sanitizeStyleValue("background: u/**/rl(x)")).toBe("");
    expect(sanitizeStyleValue("color: red/* url( */")).toBe("");
  });

  it("drops overlay and scroll-jack properties", () => {
    // Attack: content floating over the frame, spoofing what a message says.
    expect(sanitizeStyleValue("position: fixed; top: 0")).toBe("");
    expect(sanitizeStyleValue("position: absolute")).toBe("");
    expect(sanitizeStyleValue("z-index: 99999")).toBe("");
    expect(sanitizeStyleValue("transform: translateY(-100vh)")).toBe("");
    expect(EMAIL_ALLOWED_CSS_PROPERTIES.has("position")).toBe(false);
    expect(EMAIL_ALLOWED_CSS_PROPERTIES.has("z-index")).toBe(false);
  });

  it("drops values with quotes, brackets, @ or control characters", () => {
    expect(sanitizeStyleValue('font-family: "</style><script>"')).toBe("");
    expect(sanitizeStyleValue("color: red<b>")).toBe("");
    expect(sanitizeStyleValue("color: @import")).toBe("");
    expect(sanitizeStyleValue("color: re\u0000d")).toBe("");
  });

  it("drops unbalanced or unattributed parentheses", () => {
    expect(sanitizeStyleValue("width: calc(100% - (20px)")).toBe("");
    expect(sanitizeStyleValue("color: (red)")).toBe("");
    expect(sanitizeStyleValue("width: rgb (1,2,3)")).toBe("");
  });

  it("refuses oversized attributes whole", () => {
    expect(sanitizeStyleValue(`color: red; ${"a".repeat(5000)}`)).toBe("");
  });

  it("keeps surviving declarations when siblings die", () => {
    expect(
      sanitizeStyleValue("color: #333; background: url(evil); font-size: 12px"),
    ).toBe("color: #333; font-size: 12px");
  });
});

describe("the allowlists themselves", () => {
  it("never allow script, style, foreign content, forms or frames", () => {
    for (const forbidden of [
      "script", "style", "link", "meta", "base", "title", "iframe", "frame",
      "frameset", "object", "embed", "applet", "form", "input", "button",
      "select", "textarea", "option", "svg", "math", "annotation-xml",
      "mglyph", "mtext", "foreignobject", "audio", "video", "source",
      "template", "noscript", "picture", "canvas", "dialog", "slot",
    ]) {
      expect(EMAIL_ALLOWED_TAGS, `tag ${forbidden}`).not.toContain(forbidden);
    }
  });

  it("never allow event handlers, identity, or secondary URL carriers", () => {
    for (const forbidden of [
      "onerror", "onload", "onclick", "onmouseover", "onfocus",
      "id", "class", "name", "srcset", "ping", "background", "poster",
      "action", "formaction", "usemap", "target", "rel", "tabindex",
      "autofocus", "contenteditable", "is", "slot", "xmlns",
    ]) {
      expect(EMAIL_ALLOWED_ATTRIBUTES, `attr ${forbidden}`).not.toContain(forbidden);
    }
    for (const attr of EMAIL_ALLOWED_ATTRIBUTES) {
      expect(attr.startsWith("on"), `attr ${attr} looks like a handler`).toBe(false);
    }
  });
});
