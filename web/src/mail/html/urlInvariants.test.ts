import { describe, expect, it } from "vitest";

import { sanitizeEmailHtml } from "./sanitize";

/**
 * GC-7, pinned (L3 E10; canon §4.1.3): the sanitize pipeline NEVER rewrites
 * or wraps an http(s) URL. Hover-to-verify — the one phishing defence every
 * user actually has — depends on the link the browser shows being the link
 * the sender wrote, byte for byte. Gmail refuses link wrapping for exactly
 * this reason, and so does Moov.
 *
 * The single sanctioned exception is remote IMAGES, whose src is rewritten
 * to the HMAC proxy path — and the companion suite (imageBypass.test.ts)
 * pins that this is the ONLY rewrite class. Here the pins are:
 *
 *   - every href that survives sanitization is BYTE-IDENTICAL to the input,
 *     across the URL shapes that tempt a normalizer: tracking-parameter
 *     soup, IDN hosts, punycode, percent-encodings, uppercase schemes;
 *   - the rewrite of an img src carries the ORIGINAL canonical URL through
 *     the signing interface intact — the proxy wraps, it never replaces.
 *
 * Assertions read `getAttribute("href")` off a DOMParser tree rather than
 * grepping the serialized string, because serialization entity-encodes `&`
 * (`&amp;`) without changing the attribute VALUE — the value is what the
 * browser navigates with and what the status bar shows.
 */

function hrefAfterSanitize(url: string): string | null {
  const { html } = sanitizeEmailHtml(`<a href="${url}">link</a>`, {
    allowRemoteImages: false,
  });
  const doc = new DOMParser().parseFromString(html, "text/html");
  return doc.querySelector("a")?.getAttribute("href") ?? null;
}

/** URL shapes a rewriting pipeline would mangle first. */
const SURVIVOR_URLS: readonly string[] = [
  // Tracking-parameter soup: exactly the URLs a "cleaner" would clean.
  "https://shop.example.com/product?utm_source=news&utm_medium=email&utm_campaign=q3_launch&gclid=Cj0KCQjw&fbclid=IwAR2x&mc_eid=abc123",
  // Multi-value and empty parameters, plus a fragment.
  "https://example.com/path?a=1&a=2&empty=&flag#section-2",
  // IDN host, as the sender wrote it (unicode form).
  "https://münchen.example/straße?q=grüße",
  // The same host in punycode — a rewriter that round-trips through URL
  // parsing would flip one form into the other; we must preserve BOTH.
  "https://xn--mnchen-3ya.example/stra%C3%9Fe",
  // Percent-encoding that must not be decoded or re-encoded.
  "https://example.com/p%20ath?q=%2Fadmin%3F",
  // Port, userinfo-free, trailing slash — all preserved as written.
  "https://example.com:8443/deep/path/",
  // Plain http survives as http: no silent https upgrade.
  "http://legacy.example.com/track?id=9",
];

describe("GC-7: surviving http(s) hrefs are byte-identical to input", () => {
  for (const url of SURVIVOR_URLS) {
    it(`preserves ${url}`, () => {
      expect(hrefAfterSanitize(url)).toBe(url);
    });
  }

  it("adds only link hygiene (target/rel), never touching the destination", () => {
    const url = "https://example.com/?utm_source=x&next=%2F";
    const { html } = sanitizeEmailHtml(`<a href="${url}">x</a>`, {
      allowRemoteImages: false,
    });
    const a = new DOMParser().parseFromString(html, "text/html").querySelector("a");
    expect(a?.getAttribute("href")).toBe(url);
    expect(a?.getAttribute("target")).toBe("_blank");
    expect(a?.getAttribute("rel")).toBe("noopener noreferrer");
  });

  it("never wraps a link in a redirector — the app's own origin cannot appear in an href it did not start in", () => {
    const url = "https://newsletter.example.com/offer?id=42";
    const href = hrefAfterSanitize(url);
    expect(href).toBe(url);
    // The explicit negative: no /jmap/, no proxy path, no wrapping of any
    // kind in front of the destination.
    expect(href).not.toMatch(/jmap|imgproxy|redirect/i);
  });
});

describe("GC-7: the image rewrite carries the original URL, intact, through the signer", () => {
  const original =
    "https://cdn.example.com/img/pixel.gif?user=U123&campaign=aug&utm_source=mail";

  it("hands the signer the canonical ORIGINAL URL — that is what gets signed", () => {
    const seen: string[] = [];
    sanitizeEmailHtml(`<img src="${original}">`, {
      allowRemoteImages: true,
      proxiedUrlFor: (url) => {
        seen.push(url);
        return undefined;
      },
    });
    expect(seen).toEqual([original]);
  });

  it("reports the original URL in remoteImageUrls for the blocked pass too", () => {
    const { remoteImageUrls } = sanitizeEmailHtml(`<img src="${original}">`, {
      allowRemoteImages: false,
    });
    expect(remoteImageUrls).toEqual([original]);
  });

  it("rewrites the src to the signed path the server minted and nothing else", () => {
    const proxied = `/jmap/imgproxy?url=${encodeURIComponent(original)}&exp=1&sig=abc`;
    const { html } = sanitizeEmailHtml(`<img src="${original}" alt="a">`, {
      allowRemoteImages: true,
      proxiedUrlFor: () => proxied,
    });
    const img = new DOMParser().parseFromString(html, "text/html").querySelector("img");
    // The proxy URL is used verbatim — and the original rides inside it,
    // recoverable by decoding the `url` parameter.
    expect(img?.getAttribute("src")).toBe(proxied);
    const q = new URLSearchParams((img?.getAttribute("src") ?? "").split("?")[1]);
    expect(q.get("url")).toBe(original);
  });

  it("a proxied pass still leaves the hrefs in the same document untouched", () => {
    const link = "https://example.com/read-more?utm_source=mail&id=7";
    const proxied = `/jmap/imgproxy?url=${encodeURIComponent(original)}&sig=s`;
    const { html } = sanitizeEmailHtml(
      `<a href="${link}">more</a><img src="${original}">`,
      { allowRemoteImages: true, proxiedUrlFor: () => proxied },
    );
    const doc = new DOMParser().parseFromString(html, "text/html");
    expect(doc.querySelector("a")?.getAttribute("href")).toBe(link);
    expect(doc.querySelector("img")?.getAttribute("src")).toBe(proxied);
  });
});
