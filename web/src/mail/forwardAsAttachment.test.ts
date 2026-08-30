import { describe, expect, it } from "vitest";

import { emlFilename, partitionBySize, RFC822_TYPE } from "./forwardAsAttachment";

/**
 * Forward as attachment (E7, canon §2.3).
 *
 * The filename is the interesting half: it crosses into a MIME header, a
 * `Content-Disposition` parameter and then the recipient's filesystem, so a
 * subject that is allowed to pass through unfiltered is a header-injection
 * surface wearing a friendly name.
 */

describe("emlFilename", () => {
  it("uses the subject", () => {
    expect(emlFilename("Informe trimestral")).toBe("Informe trimestral.eml");
  });

  it("keeps accents and non-Latin scripts", () => {
    // The allowlist is `\p{L}`, not A-Z: stripping accents would mangle the
    // names of most of this pilot's mail.
    expect(emlFilename("Reunión mañana")).toBe("Reunión mañana.eml");
    expect(emlFilename("会議の資料")).toBe("会議の資料.eml");
  });

  it("strips the characters that would break a header or a path", () => {
    // The leading "re:" is consumed by `displaySubject`, which is the point of
    // sharing it — see the prefix test below.
    expect(emlFilename('re: "urgent"; rm -rf /')).toBe("urgent rm -rf.eml");
    expect(emlFilename("../../etc/passwd")).toBe("etcpasswd.eml");
  });

  it("removes control characters, including CRLF", () => {
    // A newline in a filename is header injection in a Content-Disposition.
    const name = emlFilename("uno\r\nBcc: victima@x.com");
    expect(name).not.toContain("\n");
    expect(name).not.toContain("\r");
  });

  it("falls back to a usable name when nothing survives", () => {
    expect(emlFilename("")).toBe("message.eml");
    expect(emlFilename(null)).toBe("message.eml");
    expect(emlFilename(undefined)).toBe("message.eml");
    // A subject that is entirely emoji still has to produce a saveable file.
    expect(emlFilename("🎉🎉🎉")).toBe("message.eml");
  });

  it("never produces a hidden or trailing-dot file", () => {
    expect(emlFilename(".oculto")).toBe("oculto.eml");
    expect(emlFilename("nombre...")).toBe("nombre.eml");
  });

  it("caps the length so an attachment row stays readable", () => {
    const long = emlFilename("a".repeat(200));
    expect(long).toBe(`${"a".repeat(60)}.eml`);
  });

  it("collapses the whitespace that stripping leaves behind", () => {
    expect(emlFilename("uno @@@ dos")).toBe("uno dos.eml");
  });

  it("strips the Re:/Fwd: prefixes the thread display already normalises", () => {
    // Shared with `displaySubject`, so a forwarded thread does not produce
    // "Re Re Fwd algo.eml".
    expect(emlFilename("Re: Fwd: algo")).toBe("algo.eml");
  });

  it("names the RFC's own type", () => {
    expect(RFC822_TYPE).toBe("message/rfc822");
  });
});

describe("partitionBySize", () => {
  const messages = [
    { id: "a", size: 100 },
    { id: "b", size: 200 },
    { id: "c", size: 300 },
  ];

  it("accepts everything when nothing is advertised", () => {
    const { accepted, refused } = partitionBySize(messages, {});
    expect(accepted).toHaveLength(3);
    expect(refused).toHaveLength(0);
  });

  it("refuses one file over the per-file cap, keeping the rest", () => {
    // The multi-select case: the user asked for three, and the answer is two
    // attached plus an honest word about the third — not a failed operation.
    const { accepted, refused } = partitionBySize(messages, { perFile: 250 });
    expect(accepted.map((entry) => entry.id)).toEqual(["a", "b"]);
    expect(refused.map((entry) => entry.id)).toEqual(["c"]);
  });

  it("stops at the per-message total", () => {
    const { accepted, refused } = partitionBySize(messages, { total: 350 });
    expect(accepted.map((entry) => entry.id)).toEqual(["a", "b"]);
    expect(refused.map((entry) => entry.id)).toEqual(["c"]);
  });

  it("counts what the composer already carries against the total", () => {
    // 300 already attached leaves 150 of a 450 budget: "a" (100) fits, and
    // both of the larger ones do not.
    const { accepted, refused } = partitionBySize(messages, {
      total: 450,
      alreadyAttached: 300,
    });
    expect(accepted.map((entry) => entry.id)).toEqual(["a"]);
    expect(refused.map((entry) => entry.id)).toEqual(["b", "c"]);
  });

  it("keeps evaluating after a refusal, so a small message after a big one fits", () => {
    const { accepted } = partitionBySize(
      [
        { id: "grande", size: 900 },
        { id: "chico", size: 10 },
      ],
      { total: 500 },
    );
    expect(accepted.map((entry) => entry.id)).toEqual(["chico"]);
  });
});
