import { describe, expect, it } from "vitest";

import { foldText, matchesQuery, queryTerms, searchableText, searchOffline } from "./search";
import type { Email } from "../mail/types";

/**
 * Offline search.
 *
 * The behaviour worth pinning is not "does substring matching work" — it is the
 * set of decisions around it: accent folding (Spanish), AND across terms, no
 * operator language, and the merge of header rows with body rows so a cached
 * body deepens a result rather than duplicating it.
 */

function mail(id: string, extra: Partial<Email> = {}): Email {
  return { id, receivedAt: "2026-08-30T10:00:00Z", ...extra };
}

describe("foldText", () => {
  it("folds case and Spanish accents", () => {
    // The one that matters: nobody types the accent into a search box.
    expect(foldText("Revisión")).toBe("revision");
    expect(foldText("MAÑANA")).toBe("manana");
    expect(foldText("Ámbito")).toBe("ambito");
  });

  it("leaves non-Latin text alone", () => {
    expect(foldText("Привет")).toBe("привет");
    expect(foldText("東京")).toBe("東京");
  });
});

describe("queryTerms", () => {
  it("splits on whitespace and drops empties", () => {
    expect(queryTerms("  hola   mundo ")).toEqual(["hola", "mundo"]);
    expect(queryTerms("   ")).toEqual([]);
  });
});

describe("searchableText", () => {
  it("covers subject, addresses (name AND address), and preview", () => {
    const text = searchableText(
      mail("m1", {
        subject: "Reunión",
        from: [{ name: "Ada Lovelace", email: "al@example.com" }],
        to: [{ name: null, email: "diego@gruponu.com" }],
        preview: "Nos vemos el martes",
      }),
    );

    expect(text).toContain("reunion");
    expect(text).toContain("ada lovelace");
    // The address too: searching "al@" must find a sender whose name is set.
    expect(text).toContain("al@example.com");
    expect(text).toContain("diego@gruponu.com");
    expect(text).toContain("nos vemos el martes");
  });

  it("includes body values when the body was cached", () => {
    const text = searchableText(
      mail("m1", {
        bodyValues: {
          "0": { value: "presupuesto adjunto", isEncodingProblem: false, isTruncated: false },
        },
      }),
    );
    expect(text).toContain("presupuesto adjunto");
  });
});

describe("matchesQuery", () => {
  const email = mail("m1", { subject: "Presupuesto de agosto", preview: "Adjunto el detalle" });

  it("requires EVERY term (AND, not OR)", () => {
    expect(matchesQuery(email, queryTerms("presupuesto agosto"))).toBe(true);
    expect(matchesQuery(email, queryTerms("presupuesto septiembre"))).toBe(false);
  });

  it("matches across fields", () => {
    // "presupuesto" is in the subject, "detalle" in the preview.
    expect(matchesQuery(email, queryTerms("presupuesto detalle"))).toBe(true);
  });

  it("matches nothing for an empty query", () => {
    expect(matchesQuery(email, [])).toBe(false);
  });

  it("treats an operator as literal text rather than pretending to parse it", () => {
    /*
     * The honest design: online search has the operator language of canon §2.5,
     * this does not, and a `from:` that silently matched nothing would be worse
     * than one searched for literally.
     */
    const withOperator = mail("m2", { subject: "re: from:ada" });
    expect(matchesQuery(withOperator, queryTerms("from:ada"))).toBe(true);
    expect(matchesQuery(email, queryTerms("from:ada"))).toBe(false);
  });
});

describe("searchOffline", () => {
  const headers = [
    mail("a", { subject: "Presupuesto", receivedAt: "2026-08-30T09:00:00Z" }),
    mail("b", { subject: "Reunión", receivedAt: "2026-08-30T11:00:00Z" }),
    mail("c", { subject: "Factura", receivedAt: "2026-08-30T10:00:00Z" }),
  ];

  it("returns matches newest first", () => {
    const found = searchOffline("u", headers);
    // "Presupuesto", "Reunión" and "Factura" all contain a folded "u".
    expect(found.map((email) => email.id)).toEqual(["b", "c", "a"]);
  });

  it("returns nothing for an empty query rather than everything", () => {
    expect(searchOffline("   ", headers)).toEqual([]);
  });

  it("merges a cached body into its header row instead of duplicating it", () => {
    const bodies = [
      mail("a", {
        subject: "Presupuesto",
        receivedAt: "2026-08-30T09:00:00Z",
        bodyValues: {
          "0": { value: "hormigón armado", isEncodingProblem: false, isTruncated: false },
        },
      }),
    ];

    const found = searchOffline("hormigon", headers, bodies);

    expect(found).toHaveLength(1);
    expect(found[0]?.id).toBe("a");
    // And the header fields survive the merge.
    expect(found[0]?.subject).toBe("Presupuesto");
  });

  it("finds a header-only message even though its body is not cached", () => {
    // The asymmetry the UI declares: no body means header-field matching only.
    const found = searchOffline("factura", headers, []);
    expect(found.map((email) => email.id)).toEqual(["c"]);
  });

  it("does not return one message twice when it appears in both lists", () => {
    const found = searchOffline("presupuesto", headers, [headers[0]!]);
    expect(found).toHaveLength(1);
  });
});
