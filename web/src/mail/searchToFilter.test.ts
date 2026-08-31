import { describe, expect, it } from "vitest";

import { searchToFilterDraft, type SearchCriteria } from "./searchToFilter";

/**
 * "Crear filtro" (E12/B7).
 *
 * The mapping itself is dull; what these tests are for is the two ways it can
 * be QUIETLY wrong, both of which produce a filter that files far more mail
 * than the search the user built it from — discovered weeks later as archived
 * mail they wanted.
 *
 *   1. A criterion the algebra cannot express being dropped SILENTLY.
 *   2. An unticked "has attachment" box mapping to `false` (which means "must
 *      NOT have one") rather than `null` ("do not care").
 */

function criteria(over: Partial<SearchCriteria> = {}): SearchCriteria {
  return {
    from: "",
    to: "",
    subject: "",
    words: "",
    sizeMode: "larger",
    sizeValue: "",
    sizeUnit: "M",
    within: "",
    hasAttachment: false,
    scope: "",
    ...over,
  };
}

describe("what maps", () => {
  it("carries the three header criteria", () => {
    const { draft } = searchToFilterDraft(
      criteria({ from: "boletin@example.com", to: "me@example.com", subject: "Factura" }),
    );
    expect(draft.from).toEqual(["boletin@example.com"]);
    expect(draft.to).toEqual(["me@example.com"]);
    expect(draft.subject).toEqual(["Factura"]);
  });

  it("trims, and drops a field that is only whitespace", () => {
    /*
     * The difference between "no from condition" and "a from condition that
     * always matches" is the difference between a filter that files a
     * newsletter and one that files everything.
     */
    const { draft } = searchToFilterDraft(criteria({ from: "  a@b.com  ", subject: "   " }));
    expect(draft.from).toEqual(["a@b.com"]);
    expect(draft.subject).toEqual([]);
  });

  it("converts the size to BYTES, into the half the mode names", () => {
    const larger = searchToFilterDraft(
      criteria({ sizeMode: "larger", sizeValue: "5", sizeUnit: "M" }),
    );
    expect(larger.draft.sizeOver).toBe(5 * 1024 * 1024);
    expect(larger.draft.sizeUnder).toBe(0);

    const smaller = searchToFilterDraft(
      criteria({ sizeMode: "smaller", sizeValue: "200", sizeUnit: "K" }),
    );
    expect(smaller.draft.sizeUnder).toBe(200 * 1024);
    expect(smaller.draft.sizeOver).toBe(0);
  });

  it("treats an unparseable or non-positive size as UNSET, which is 0", () => {
    expect(searchToFilterDraft(criteria({ sizeValue: "" })).draft.sizeOver).toBe(0);
    expect(searchToFilterDraft(criteria({ sizeValue: "abc" })).draft.sizeOver).toBe(0);
    expect(searchToFilterDraft(criteria({ sizeValue: "0" })).draft.sizeOver).toBe(0);
    expect(searchToFilterDraft(criteria({ sizeValue: "-3" })).draft.sizeOver).toBe(0);
  });

  it("leaves the NAME empty rather than deriving one", () => {
    // The name's whole job is to be what the user recognises this rule by six
    // months from now; a machine-generated string is the one thing it must not
    // be.
    expect(searchToFilterDraft(criteria({ from: "a@b.com" })).draft.name).toBe("");
  });
});

describe("hasAttachment is three-valued, and the middle value is the trap", () => {
  it("maps a TICKED box to true", () => {
    expect(searchToFilterDraft(criteria({ hasAttachment: true })).draft.hasAttachment).toBe(
      true,
    );
  });

  it("maps an UNTICKED box to null — 'do not care' — and never to false", () => {
    /*
     * The wire type is Boolean|null and the three values mean three different
     * things: true is "must have one", false is "must NOT have one", null is
     * "do not care". An unticked box means the user did not ask about
     * attachments; mapping it to `false` would build a filter that files
     * exactly the messages WITHOUT attachments, which is nobody's intent and
     * is invisible until it happens.
     */
    const { draft } = searchToFilterDraft(criteria({ from: "a@b.com", hasAttachment: false }));
    expect(draft.hasAttachment).toBeNull();
    expect(draft.hasAttachment).not.toBe(false);
  });
});

describe("what cannot map is NAMED, never dropped silently", () => {
  it("reports free text, which the rule algebra has no condition for", () => {
    const { dropped } = searchToFilterDraft(criteria({ from: "a@b.com", words: "factura" }));
    expect(dropped).toContain("words");
  });

  it("reports the date range, which a delivery-time filter cannot express", () => {
    const { dropped } = searchToFilterDraft(criteria({ from: "a@b.com", within: "7" }));
    expect(dropped).toContain("dateRange");
  });

  it("reports the scope, because a filter acts on mail arriving in one place", () => {
    const { dropped } = searchToFilterDraft(criteria({ from: "a@b.com", scope: "mb1" }));
    expect(dropped).toContain("scope");
  });

  it("reports NOTHING when the filter matches exactly what the search did", () => {
    const { dropped } = searchToFilterDraft(criteria({ from: "a@b.com", hasAttachment: true }));
    expect(dropped).toEqual([]);
  });
});

describe("usability — the guard against a rule that matches everything", () => {
  it("is unusable when no criterion mapped", () => {
    /*
     * A rule with no conditions matches EVERY message. Opening the builder
     * pre-filled from a search that expressed only free text is how a user
     * archives their whole inbox with one click, so the caller is told not to.
     */
    const { usable } = searchToFilterDraft(criteria({ words: "factura", within: "7" }));
    expect(usable).toBe(false);
  });

  it("is usable on ANY single mapped criterion", () => {
    expect(searchToFilterDraft(criteria({ from: "a@b.com" })).usable).toBe(true);
    expect(searchToFilterDraft(criteria({ to: "a@b.com" })).usable).toBe(true);
    expect(searchToFilterDraft(criteria({ subject: "x" })).usable).toBe(true);
    expect(searchToFilterDraft(criteria({ sizeValue: "5" })).usable).toBe(true);
    expect(searchToFilterDraft(criteria({ hasAttachment: true })).usable).toBe(true);
  });

  it("is unusable on an empty panel", () => {
    expect(searchToFilterDraft(criteria()).usable).toBe(false);
  });
});
