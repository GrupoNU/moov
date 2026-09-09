import { describe, expect, it } from "vitest";

import { formatAddress, recipientSummary } from "./recipientSummary";

const ME = ["diego@gruponu.com"];

describe("the 'para mí' phrase", () => {
  it("says 'mí' alone when the reader is the only recipient", () => {
    expect(
      recipientSummary({ to: [{ name: "Diego", email: "Diego@GrupoNU.com" }] }, ME, "mí"),
    ).toBe("mí");
  });

  it("puts 'mí' first, then the others by name", () => {
    expect(
      recipientSummary(
        {
          to: [{ name: "Ana", email: "ana@x" }, { name: null, email: "diego@gruponu.com" }],
          cc: [{ name: "Carlos", email: "carlos@x" }],
        },
        ME,
        "mí",
      ),
    ).toBe("mí, Ana, Carlos");
  });

  it("names the others when the reader is not among them", () => {
    expect(
      recipientSummary({ to: [{ name: "Ana", email: "ana@x" }, { name: null, email: "list@x" }] }, ME, "mí"),
    ).toBe("Ana, list@x");
  });

  it("collapses a person named in both To and Cc", () => {
    expect(
      recipientSummary(
        { to: [{ name: "Ana", email: "ana@x" }], cc: [{ name: "Ana B.", email: "ANA@x" }] },
        ME,
        "mí",
      ),
    ).toBe("Ana");
  });

  it("is undefined with no recipients at all", () => {
    expect(recipientSummary({ to: null, cc: null }, ME, "mí")).toBeUndefined();
    expect(recipientSummary({}, [], "mí")).toBeUndefined();
  });
});

describe("formatAddress", () => {
  it("renders name and address, or the address alone", () => {
    expect(formatAddress({ name: "Ana", email: "ana@x" })).toBe("Ana <ana@x>");
    expect(formatAddress({ name: null, email: "ana@x" })).toBe("ana@x");
    expect(formatAddress({ name: "  ", email: "ana@x" })).toBe("ana@x");
  });
});
