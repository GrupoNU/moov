import { describe, expect, it } from "vitest";

import {
  chipsToWire,
  dedupeAddresses,
  formatAddress,
  hasValidRecipients,
  isCommitKey,
  isValidEmail,
  makeChip,
  parseAddress,
  parseAddressList,
  splitAddressList,
  wireToChips,
} from "./addresses";

describe("isValidEmail", () => {
  it("accepts the addresses people actually have", () => {
    for (const address of [
      "moov-test@atmosfera.cloud",
      "a@b.co",
      "first.last+tag@sub.example.com",
      "d.nannini@gruponu.com",
      "usuario@dominio.com.ar",
      "josé@ejemplo.es",
    ]) {
      expect(isValidEmail(address), address).toBe(true);
    }
  });

  it("refuses shapes that would corrupt or forge a header", () => {
    for (const address of [
      "",
      "no-at-sign",
      "@nolocal.com",
      "trailing@",
      "two@at@signs.com",
      "bare@hostname",
      "spaces in@example.com",
      "with<bracket@example.com",
      "comma,inside@example.com",
      // header injection: a CRLF would start a new header field
      "victim@example.com\r\nBcc: attacker@evil.com",
      ".leading@example.com",
      "trailing.@example.com",
      "double..dot@example.com",
      "user@.example.com",
      "user@example..com",
      "user@-example.com",
    ]) {
      expect(isValidEmail(address), JSON.stringify(address)).toBe(false);
    }
  });

  it("refuses an address longer than the RFC 5321 ceiling", () => {
    expect(isValidEmail(`${"a".repeat(250)}@example.com`)).toBe(false);
  });
});

describe("splitAddressList", () => {
  it("splits on commas and semicolons", () => {
    expect(splitAddressList("a@x.com, b@y.com; c@z.com")).toEqual([
      "a@x.com",
      "b@y.com",
      "c@z.com",
    ]);
  });

  /*
   * The bug this test exists for: Outlook writes display names "Last, First",
   * and a split(",") turns ONE address into two broken ones. This is the most
   * common paste in a corporate mail client.
   */
  it("does not split inside a quoted display name", () => {
    expect(splitAddressList('"Gómez, Ana" <ana@x.com>, bea@y.com')).toEqual([
      '"Gómez, Ana" <ana@x.com>',
      "bea@y.com",
    ]);
  });

  it("does not split inside angle brackets", () => {
    expect(splitAddressList("Ana <ana@x.com>,Bea <bea@y.com>")).toEqual([
      "Ana <ana@x.com>",
      "Bea <bea@y.com>",
    ]);
  });

  it("drops empty entries from trailing separators", () => {
    expect(splitAddressList("a@x.com,,  ;b@y.com,")).toEqual(["a@x.com", "b@y.com"]);
  });
});

describe("parseAddress", () => {
  it("reads a bare address", () => {
    expect(parseAddress("ana@x.com")).toEqual({ name: undefined, email: "ana@x.com" });
  });

  it("reads a named address", () => {
    expect(parseAddress("Ana Gómez <ana@x.com>")).toEqual({
      name: "Ana Gómez",
      email: "ana@x.com",
    });
  });

  it("unquotes a quoted display name and unescapes it", () => {
    expect(parseAddress('"Gómez, Ana" <ana@x.com>')).toEqual({
      name: "Gómez, Ana",
      email: "ana@x.com",
    });
    expect(parseAddress('"Say \\"hi\\"" <hi@x.com>')).toEqual({
      name: 'Say "hi"',
      email: "hi@x.com",
    });
  });

  it("keeps unparseable input as the address, so the user's typing survives", () => {
    expect(parseAddress("total garbage")).toEqual({ name: undefined, email: "total garbage" });
  });
});

describe("parseAddressList", () => {
  it("marks invalid entries rather than discarding them", () => {
    const chips = parseAddressList("good@example.com, nonsense");
    expect(chips).toHaveLength(2);
    expect(chips[0]?.isValid).toBe(true);
    expect(chips[1]?.isValid).toBe(false);
    expect(chips[1]?.email).toBe("nonsense");
  });

  it("gives every chip a distinct key even for the same address", () => {
    const chips = parseAddressList("a@x.com, a@x.com");
    expect(chips[0]?.key).not.toBe(chips[1]?.key);
  });
});

describe("formatAddress", () => {
  it("renders a bare address unchanged", () => {
    expect(formatAddress({ name: undefined, email: "a@x.com" })).toBe("a@x.com");
  });

  it("renders a plain name unquoted", () => {
    expect(formatAddress({ name: "Ana", email: "a@x.com" })).toBe("Ana <a@x.com>");
  });

  it("quotes a name that would otherwise re-parse wrongly", () => {
    expect(formatAddress({ name: "Gómez, Ana", email: "a@x.com" })).toBe(
      '"Gómez, Ana" <a@x.com>',
    );
  });

  it("round-trips a comma-bearing name through parse", () => {
    const rendered = formatAddress({ name: "Gómez, Ana", email: "a@x.com" });
    expect(parseAddress(rendered)).toEqual({ name: "Gómez, Ana", email: "a@x.com" });
  });
});

describe("chipsToWire", () => {
  it("drops invalid chips and nulls an absent name (the server's shape)", () => {
    const chips = [makeChip("a@x.com", "Ana"), makeChip("nope"), makeChip("b@y.com")];
    expect(chipsToWire(chips)).toEqual([
      { name: "Ana", email: "a@x.com" },
      { name: null, email: "b@y.com" },
    ]);
  });
});

describe("wireToChips", () => {
  it("handles the server's null for an absent header", () => {
    expect(wireToChips(null)).toEqual([]);
    expect(wireToChips(undefined)).toEqual([]);
  });

  it("carries names across", () => {
    const chips = wireToChips([{ name: "Ana", email: "a@x.com" }]);
    expect(chips[0]?.name).toBe("Ana");
    expect(chips[0]?.isValid).toBe(true);
  });
});

describe("dedupeAddresses", () => {
  it("removes duplicates case-insensitively", () => {
    const chips = [makeChip("A@X.com"), makeChip("a@x.com"), makeChip("b@y.com")];
    expect(dedupeAddresses(chips, []).map((chip) => chip.email)).toEqual([
      "A@X.com",
      "b@y.com",
    ]);
  });

  it("removes the excluded addresses — reply-all must not include yourself", () => {
    const chips = [makeChip("me@moov.test"), makeChip("other@x.com")];
    expect(dedupeAddresses(chips, ["ME@moov.test"]).map((chip) => chip.email)).toEqual([
      "other@x.com",
    ]);
  });
});

describe("hasValidRecipients", () => {
  it("is false with no recipients at all", () => {
    expect(hasValidRecipients([], [])).toBe(false);
  });

  it("is false when any chip is invalid", () => {
    expect(hasValidRecipients([makeChip("a@x.com"), makeChip("bad")])).toBe(false);
  });

  it("is true when at least one valid chip exists across the lists", () => {
    expect(hasValidRecipients([], [makeChip("a@x.com")])).toBe(true);
  });
});

describe("isCommitKey", () => {
  it("commits on the four keys every mail client uses", () => {
    for (const key of [",", ";", "Enter", "Tab"]) {
      expect(isCommitKey(key), key).toBe(true);
    }
    expect(isCommitKey("a")).toBe(false);
    expect(isCommitKey("Backspace")).toBe(false);
  });
});
