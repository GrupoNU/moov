import { describe, expect, it } from "vitest";

import { ATTACK_CORPUS } from "./corpus";
import { normalizeCid, sanitizeEmailHtml } from "./sanitize";

const BLOCK = { allowRemoteImages: false } as const;

/**
 * Parses a sanitized string in an INERT document (DOMParser does not execute
 * or fetch) and returns the tree, so assertions can inspect the live DOM the
 * way the iframe's parser will — the check the "output string" test cannot
 * make, because an entity that looks safe as text may decode to markup.
 */
function parse(html: string): Document {
  return new DOMParser().parseFromString(html, "text/html");
}

describe("the adversarial corpus", () => {
  for (const c of ATTACK_CORPUS) {
    it(`${c.id}: ${c.attack}`, () => {
      const { html } = sanitizeEmailHtml(c.html, BLOCK);

      for (const needle of c.mustNotContain ?? []) {
        expect(html.toLowerCase(), `output contains ${needle}`).not.toContain(
          needle.toLowerCase(),
        );
      }
      for (const re of c.mustNotMatch ?? []) {
        expect(html, `output matches ${re}`).not.toMatch(re);
      }

      // The structural invariants EVERY case must satisfy, hostile or benign:
      // the parsed tree carries no executable or fetching surface.
      const doc = parse(html);
      assertInert(doc);
    });
  }
});

/** The properties that must hold for ANY sanitized output. */
function assertInert(doc: Document): void {
  const forbidden = [
    "script", "style", "link", "meta", "base", "iframe", "frame", "object",
    "embed", "applet", "form", "input", "button", "select", "textarea",
    "svg", "math", "audio", "video", "template", "noscript",
  ];
  for (const tag of forbidden) {
    expect(doc.querySelectorAll(tag).length, `element <${tag}> present`).toBe(0);
  }

  for (const el of doc.querySelectorAll("*")) {
    for (const attr of el.attributes) {
      // No event handlers survive anywhere.
      expect(attr.name.startsWith("on"), `handler ${attr.name}`).toBe(false);
      // No id/name (DOM clobbering).
      expect(["id", "name"], `clobber attr ${attr.name}`).not.toContain(attr.name);
    }
    // Every href/src that survived is a scheme a click or an <img> can
    // safely mean — never javascript:/vbscript:/data:text/html/relative.
    const href = el.getAttribute("href");
    if (href !== null) {
      expect(/^(https?:|mailto:|tel:)/i.test(href), `href ${href}`).toBe(true);
    }
    const src = el.getAttribute("src");
    if (src !== null) {
      expect(
        /^(data:image\/|\/jmap\/imgproxy)/i.test(src),
        `src ${src}`,
      ).toBe(true);
    }
    // No inline style names a fetching or overlay property.
    const style = el.getAttribute("style");
    if (style !== null) {
      expect(style, `style url() in ${style}`).not.toMatch(/url\s*\(/i);
      expect(style, `style position in ${style}`).not.toMatch(/position\s*:/i);
      expect(style, `style expression in ${style}`).not.toMatch(/expression/i);
    }
  }
}

describe("the benign control renders substantively", () => {
  it("keeps text, headings, links and layout", () => {
    const control = ATTACK_CORPUS.find((c) => c.id === "benign-newsletter");
    if (control === undefined) throw new Error("control case missing");
    const { html } = sanitizeEmailHtml(control.html, BLOCK);
    const doc = parse(html);
    expect(doc.querySelector("h1")?.textContent).toBe("Hello");
    expect(doc.querySelector("table")).not.toBeNull();
    const link = doc.querySelector("a");
    expect(link?.getAttribute("href")).toBe("https://example.com/post");
    // Forced link hygiene.
    expect(link?.getAttribute("target")).toBe("_blank");
    expect(link?.getAttribute("rel")).toBe("noopener noreferrer");
    // The color survives as a filtered inline style.
    expect(doc.querySelector("td")?.getAttribute("style")).toContain("color");
  });
});

describe("the remote-image pipeline", () => {
  const withRemote = `<p>hi</p><img src="https://cdn.example.com/a.png" alt="a">
    <img src="https://cdn.example.com/b.png"><img src="cid:inline@x">`;

  it("blocks remote images by default and collects their URLs", () => {
    const out = sanitizeEmailHtml(withRemote, { allowRemoteImages: false });
    // No <img> keeps a remote src while blocked.
    expect(out.html).not.toContain("cdn.example.com");
    expect(out.blockedImageCount).toBe(2);
    expect(out.remoteImageUrls).toEqual([
      "https://cdn.example.com/a.png",
      "https://cdn.example.com/b.png",
    ]);
    // The cid: image is counted as unavailable, not as blocked-remote — and
    // its id is reported so the parent can go and fetch the part (C-11).
    expect(out.droppedInlineImageCount).toBe(1);
    expect(out.inlineImageCids).toEqual(["inline@x"]);
  });

  /*
   * C-11: inline images resolve ONLY to raster data: URLs the parent built.
   * The resolver's word is not trusted — a value of any other shape is
   * refused and the image dropped, which is what keeps this path unable to
   * widen the frame's image surface by a single scheme.
   */
  it("resolves a cid: image to the parent's data: URL", () => {
    const out = sanitizeEmailHtml(withRemote, {
      allowRemoteImages: false,
      inlineImageFor: (cid) => (cid === "inline@x" ? "data:image/png;base64,iVBORw0KGgo=" : undefined),
    });
    expect(out.html).toContain('src="data:image/png;base64,iVBORw0KGgo="');
    expect(out.droppedInlineImageCount).toBe(0);
    expect(out.inlineImageCids).toEqual(["inline@x"]);
  });

  it("refuses a resolver value that is not a raster data: image", () => {
    for (const bad of [
      "https://evil.example/x.png",
      "/jmap/download/a/b/x.png?access_token=t",
      "data:text/html;base64,PHNjcmlwdD4=",
      "data:image/svg+xml;base64,PHN2Zz4=",
      "javascript:alert(1)",
    ]) {
      const out = sanitizeEmailHtml(withRemote, {
        allowRemoteImages: false,
        inlineImageFor: () => bad,
      });
      expect(out.html, bad).not.toContain(bad);
      expect(out.droppedInlineImageCount, bad).toBe(1);
    }
  });

  it("normalizes content-ids from either side of the match", () => {
    expect(normalizeCid("cid:part1@x")).toBe("part1@x");
    expect(normalizeCid("CID:<part1@x>")).toBe("part1@x");
    expect(normalizeCid(" <part1@x> ")).toBe("part1@x");
    expect(normalizeCid("cid:image001.png%40ABC")).toBe("image001.png@ABC");
    // Case preserved: the fallback fold is the parent's decision.
    expect(normalizeCid("cid:Part1@X")).toBe("Part1@X");
  });

  it("rewrites to the proxy ONLY through the signed mapping", () => {
    const mapping = new Map<string, string>([
      ["https://cdn.example.com/a.png", "/jmap/imgproxy?u=AAA&e=1&s=BBB"],
      // b.png is deliberately unmapped: the signer refused it.
    ]);
    const out = sanitizeEmailHtml(withRemote, {
      allowRemoteImages: true,
      proxiedUrlFor: (u) => mapping.get(u),
    });
    // a.png points at the proxy; b.png (unmapped) stays blocked.
    expect(out.html).toContain("/jmap/imgproxy?u=AAA");
    expect(out.html).not.toContain("cdn.example.com");
    expect(out.blockedImageCount).toBe(1);
  });

  it("NEVER emits a src that is not proxy or data:, even if the mapping lies", () => {
    // Attack: a compromised/incorrect signer returns an absolute attacker URL.
    const evilMapping = new Map<string, string>([
      ["https://cdn.example.com/a.png", "https://evil.example/track.png"],
      ["https://cdn.example.com/b.png", "javascript:alert(1)"],
    ]);
    const out = sanitizeEmailHtml(withRemote, {
      allowRemoteImages: true,
      proxiedUrlFor: (u) => evilMapping.get(u),
    });
    expect(out.html).not.toContain("evil.example");
    expect(out.html).not.toContain("javascript:");
    // Both fell back to blocked, because neither value was a /jmap/imgproxy path.
    expect(out.blockedImageCount).toBe(2);
  });

  it("keeps inline data: raster images without a network fetch", () => {
    const out = sanitizeEmailHtml(
      `<img src="data:image/png;base64,iVBORw0KGgo=" alt="x">`,
      { allowRemoteImages: false },
    );
    expect(out.html).toContain("data:image/png;base64");
    expect(out.blockedImageCount).toBe(0);
  });
});

describe("output stability", () => {
  it("is idempotent on the whole corpus (sanitize∘sanitize == sanitize)", () => {
    for (const c of ATTACK_CORPUS) {
      const once = sanitizeEmailHtml(c.html, BLOCK).html;
      const twice = sanitizeEmailHtml(once, BLOCK).html;
      expect(twice, `not idempotent on ${c.id}`).toBe(once);
    }
  });

  it("does not throw on adversarial or degenerate input", () => {
    for (const input of ["", "<", "<<<>>>", "\u0000", "&#x", "<p".repeat(5000)]) {
      expect(() => sanitizeEmailHtml(input, BLOCK)).not.toThrow();
    }
  });
});
