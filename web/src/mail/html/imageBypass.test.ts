import { describe, expect, it } from "vitest";

import { ATTACK_CORPUS } from "./corpus";
import { sanitizeEmailHtml } from "./sanitize";
import { buildSrcDoc } from "./srcdoc";

/**
 * D-4, audited end to end (L3 E10; canon §4.1.1): with `imagesPolicy:
 * "always"` every remote image loads THROUGH the HMAC proxy and only through
 * it — the direct remote URL must be unrepresentable in the final document.
 * Gmail's display-by-default is conditional on exactly this property ("
 * senders can't use image loading to get information about you"), so this
 * suite is the evidence that Moov's default-display is defensible.
 *
 * The pass under test is the PERMISSIVE one: `allowRemoteImages: true` with
 * a signer that happily signs every URL it is asked about — the worst case,
 * where a bypass would actually fetch. Each attack then goes through the
 * REAL assembly (`buildSrcDoc`) and the final document is searched for any
 * fetchable slot naming an external origin:
 *
 *   - src anywhere but <img>, or an <img> src that is neither data: nor the
 *     same-origin proxy path;
 *   - the secondary URL carriers (srcset, poster, background, ping,
 *     formaction, xlink:href, data=, dynsrc, lowsrc);
 *   - CSS url() in any surviving style attribute or <style> block other
 *     than the two stylesheets the srcdoc itself authors.
 *
 * `evil.example` may legitimately appear percent-ENCODED inside a
 * `/jmap/imgproxy?url=…` src (the original rides inside the proxy URL — the
 * GC-7 companion pins that) and as a link HREF (links are navigation, not
 * fetches; the sanitizer's corpus suite owns href hygiene). Everything else
 * is a bypass.
 */

/** A signer that signs everything: the most permissive (worst) case. */
function permissiveProxy(url: string): string {
  return `/jmap/imgproxy?url=${encodeURIComponent(url)}&exp=1&sig=test`;
}

function renderThroughPipeline(html: string): Document {
  const sanitized = sanitizeEmailHtml(html, {
    allowRemoteImages: true,
    proxiedUrlFor: permissiveProxy,
  });
  const srcdoc = buildSrcDoc(sanitized.html, "https://mail.example.org");
  return new DOMParser().parseFromString(srcdoc, "text/html");
}

/** Every attribute that can cause a fetch, beyond src itself. */
const FETCH_ATTRIBUTES = [
  "srcset", "poster", "background", "ping", "formaction", "action",
  "data", "dynsrc", "lowsrc", "xlink:href", "imagesrcset",
] as const;

function assertNoExternalFetchSurface(doc: Document): void {
  for (const el of doc.body.querySelectorAll("*")) {
    for (const attr of FETCH_ATTRIBUTES) {
      expect(el.hasAttribute(attr), `<${el.tagName}> carries ${attr}`).toBe(false);
    }
    const src = el.getAttribute("src");
    if (src !== null) {
      expect(el.tagName, "src survives only on <img>").toBe("IMG");
      expect(
        /^(data:image\/|\/jmap\/imgproxy\?)/i.test(src),
        `img src is not data:/proxy: ${src}`,
      ).toBe(true);
    }
    const style = el.getAttribute("style");
    if (style !== null) {
      expect(style, `style can fetch: ${style}`).not.toMatch(/url\s*\(/i);
      expect(style, `style can fetch: ${style}`).not.toMatch(/image-set|element\(/i);
    }
  }
  // The only <style> is the srcdoc's own head stylesheet; no message content
  // may contribute one, and the authored ones never contain url().
  for (const styleEl of doc.querySelectorAll("style")) {
    expect(styleEl.textContent ?? "").not.toMatch(/url\s*\(/i);
  }
  // No element that fetches by its nature survives at all.
  for (const tag of ["link", "source", "video", "audio", "object", "embed", "iframe", "svg", "image", "picture", "track"]) {
    expect(doc.body.querySelectorAll(tag).length, `<${tag}> in body`).toBe(0);
  }
}

/** The D-4 bypass attempts, in the attack-corpus idiom. */
const BYPASS_ATTEMPTS: readonly { id: string; html: string }[] = [
  { id: "srcset-on-img", html: `<img src="https://evil.example/a.png" srcset="https://evil.example/b.png 1x, https://evil.example/c.png 2x">` },
  { id: "picture-source", html: `<picture><source srcset="https://evil.example/p.png"><img src="https://evil.example/f.png"></picture>` },
  { id: "video-poster", html: `<video poster="https://evil.example/poster.jpg" src="https://evil.example/v.mp4"></video>` },
  { id: "audio-src", html: `<audio src="https://evil.example/a.mp3" autoplay></audio>` },
  { id: "object-data", html: `<object data="https://evil.example/o.svg"></object>` },
  { id: "embed-src", html: `<embed src="https://evil.example/e.swf">` },
  { id: "svg-image-xlink", html: `<svg><image xlink:href="https://evil.example/x.png" href="https://evil.example/y.png"/></svg>` },
  { id: "table-background", html: `<table background="https://evil.example/bg.png"><tr><td background="https://evil.example/td.png">x</td></tr></table>` },
  { id: "body-background-attr", html: `<div background="https://evil.example/bg2.png">x</div>` },
  { id: "css-background-url", html: `<div style="background: url(https://evil.example/c.png)">x</div>` },
  { id: "css-background-image", html: `<p style="background-image:url('https://evil.example/i.png')">x</p>` },
  { id: "css-escaped-url", html: `<p style="background:\\75rl(https://evil.example/esc.png)">x</p>` },
  { id: "css-image-set", html: `<p style="background: image-set('https://evil.example/is.png' 1x)">x</p>` },
  { id: "css-content-url", html: `<p style="content: url(https://evil.example/ct.png)">x</p>` },
  { id: "css-list-style", html: `<ul style="list-style-image: url(https://evil.example/li.png)"><li>x</li></ul>` },
  { id: "style-element", html: `<style>p{background:url(https://evil.example/s.png)}</style><p>x</p>` },
  { id: "link-stylesheet", html: `<link rel="stylesheet" href="https://evil.example/l.css"><p>x</p>` },
  { id: "input-image", html: `<input type="image" src="https://evil.example/in.png">` },
  { id: "img-ping-shape", html: `<img src="https://evil.example/p.png" ping="https://evil.example/ping">` },
  { id: "src-on-div", html: `<div src="https://evil.example/d.png">x</div>` },
  { id: "protocol-relative", html: `<img src="//evil.example/pr.png">` },
  { id: "img-inside-noscript", html: `<noscript><img src="https://evil.example/ns.png"></noscript><p>x</p>` },
  { id: "meta-refresh", html: `<meta http-equiv="refresh" content="0;url=https://evil.example/r"><p>x</p>` },
  { id: "base-rebase", html: `<base href="https://evil.example/"><img src="pr.png">` },
  { id: "img-lowsrc-dynsrc", html: `<img src="https://evil.example/l.png" lowsrc="https://evil.example/low.png" dynsrc="https://evil.example/dyn.png">` },
];

describe("D-4: no bypass reaches an external origin through the full pipeline", () => {
  for (const attempt of BYPASS_ATTEMPTS) {
    it(attempt.id, () => {
      const doc = renderThroughPipeline(attempt.html);
      assertNoExternalFetchSurface(doc);
      // Belt and braces: the RAW host string may survive only inside a
      // same-origin proxy src (percent-encoded by construction there) — so a
      // bare "https://evil.example" in a fetchable attribute cannot exist.
      // (No anchors in this corpus, so any literal occurrence outside an
      // img proxy src is a leak.)
      for (const el of doc.body.querySelectorAll("*")) {
        for (const attr of el.attributes) {
          if (attr.name === "src" && attr.value.startsWith("/jmap/imgproxy?")) continue;
          expect(attr.value, `${el.tagName}@${attr.name} leaks the origin`).not.toContain(
            "evil.example",
          );
        }
      }
    });
  }

  it("the whole adversarial corpus also holds under the PERMISSIVE pass", () => {
    // The corpus suite runs blocked-first; D-4's question is whether any of
    // those attacks fare better when images are allowed and the signer is
    // maximally permissive. None may.
    for (const c of ATTACK_CORPUS) {
      const doc = renderThroughPipeline(c.html);
      assertNoExternalFetchSurface(doc);
    }
  });

  it("the legitimate case still works: a remote img comes out as the proxy path, whole", () => {
    const original = "https://cdn.example.com/logo.png?v=3";
    const doc = renderThroughPipeline(`<p>hi</p><img src="${original}" alt="logo">`);
    const img = doc.querySelector("img");
    expect(img?.getAttribute("src")).toBe(permissiveProxy(original));
    // The CSP in the assembled head states the same policy a second time.
    const csp = doc.querySelector('meta[http-equiv="Content-Security-Policy"]');
    expect(csp?.getAttribute("content")).toContain("img-src data: https://mail.example.org/jmap/imgproxy");
    expect(csp?.getAttribute("content")).toContain("default-src 'none'");
  });
});
