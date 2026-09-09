import { describe, expect, it } from "vitest";

import { PREVIEW_MAX_CHARS, previewText } from "./previewText";

/**
 * The preview line's cleanup (B-08).
 *
 * The cases below are drawn from the rows in the side-by-side evidence: the
 * DonWeb renewal notices, whose preview is two thirds `utm_` parameters, and
 * the LinkedIn digests, which open with a `urn:li:activity` link. Those are not
 * unusual mail — they are what a real inbox is mostly made of, which is why the
 * defect was visible on the first screenshot.
 */
describe("previewText", () => {
  it("collapses a tracking URL to its domain, keeping the sentence", () => {
    const raw =
      "Revísalo para evitar interrupciones donweb https://donweb.com?utm_campaign=Aviso%2520de%2520vencimiento&utm_medium=email_action";
    expect(previewText(raw)).toBe(
      "Revísalo para evitar interrupciones donweb donweb.com",
    );
  });

  it("drops the URL entirely when asked to", () => {
    expect(
      previewText("Mira esto https://example.com/a/b?c=d y contame", { urls: "drop" }),
    ).toBe("Mira esto y contame");
  });

  it("keeps a meaningful subdomain and strips only the www", () => {
    // "mail.google.com" is not "google.com"; "www.donweb.com" is "donweb.com".
    expect(previewText("ver https://mail.google.com/x")).toBe("ver mail.google.com");
    expect(previewText("ver https://www.donweb.com/x")).toBe("ver donweb.com");
  });

  it("recognises a scheme-less www. link", () => {
    expect(previewText("escribí a www.ejemplo.com.ar/contacto ya")).toBe(
      "escribí a ejemplo.com.ar ya",
    );
  });

  it("clears the punctuation the URL was wrapped in", () => {
    /*
     * Composers wrap links as `( … )` or `< … >`. With the URL replaced by
     * nothing, the row would otherwise read "…interrupciones donweb ( )".
     */
    expect(previewText("interrupciones donweb ( https://x.com/a ) fin", { urls: "drop" })).toBe(
      "interrupciones donweb fin",
    );
    expect(previewText("ver <https://x.com/a> fin", { urls: "drop" })).toBe("ver fin");
  });

  it("does not eat the sentence's punctuation with the URL", () => {
    expect(previewText("Visitá https://x.com/a. Gracias.", { urls: "drop" })).toBe(
      "Visitá . Gracias.",
    );
  });

  it("leaves BARE domains alone — prose is not a link", () => {
    /*
     * The restraint that keeps this from mangling ordinary text. A matcher
     * greedy enough to catch "donweb.com" without a scheme also catches
     * "Escribinos a soporte.com o llamanos", and worse, "etc. Mañana".
     */
    const prose = "Escribinos a soporte.com o llamanos. Etc. Mañana seguimos.";
    expect(previewText(prose)).toBe(prose);
  });

  it("leaves a preview with no links completely untouched", () => {
    const plain = "Diego Adrián, ¡espero tu respuesta! Daniela está esperando tu respuesta";
    expect(previewText(plain)).toBe(plain);
  });

  it("collapses the whitespace its own removals create", () => {
    expect(previewText("a   https://x.com/1   https://x.com/2   b", { urls: "drop" })).toBe(
      "a b",
    );
  });

  it("bounds the length, cutting at a word boundary", () => {
    const long = "palabra ".repeat(200);
    const out = previewText(long);
    expect(out.length).toBeLessThanOrEqual(PREVIEW_MAX_CHARS);
    // Cut at a space, so the line does not end mid-word on a row wide enough to
    // show the end of it.
    expect(out.endsWith("palabra")).toBe(true);
  });

  it("appends no ellipsis of its own", () => {
    // The row clips with CSS `text-overflow`; a literal "…" would either double
    // up with the clip's or waste a character the clip was going to take.
    expect(previewText("x".repeat(500))).not.toContain("…");
  });

  it("cuts a single unbroken run where it falls", () => {
    // A 500-character "word" is not a word, so there is no boundary worth
    // preferring and the hard bound applies.
    expect(previewText("y".repeat(500))).toHaveLength(PREVIEW_MAX_CHARS);
  });

  it("does not walk a pathological preview end to end", () => {
    /*
     * The reason the bound is applied BEFORE the regex passes: a base64 blob
     * pasted into a body would otherwise be scanned in full, once per row, on
     * every render, to then be thrown away. This asserts the observable half —
     * a huge input still produces a bounded answer — and pins the ordering by
     * asserting it does so quickly.
     */
    const blob = `${"A".repeat(2_000_000)} https://x.com/y`;
    const started = Date.now();
    expect(previewText(blob)).toHaveLength(PREVIEW_MAX_CHARS);
    expect(Date.now() - started).toBeLessThan(200);
  });

  it("is total: empty in, empty out, and never throws on a malformed URL", () => {
    expect(previewText("")).toBe("");
    // A scheme with nothing usable after it: the domain cannot be parsed, so it
    // is dropped rather than left as a fragment or raised as an error.
    expect(previewText("mira https:// y listo")).toBe("mira y listo");
  });

  it("honours an explicit maxChars", () => {
    // Never LONGER than the bound; it may be shorter when the word-boundary cut
    // pulls back, which at these toy sizes it does.
    expect(previewText("abcdefghij", { maxChars: 4 }).length).toBeLessThanOrEqual(4);
    expect(previewText("uno dos tres cuatro", { maxChars: 8 })).toBe("uno dos");
  });
});
