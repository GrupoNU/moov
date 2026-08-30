import { describe, expect, it } from "vitest";

import {
  addressesFromMessage,
  foldForMatch,
  matchesQuery,
  mergeSighting,
  rankAddresses,
  suggestAddresses,
  type IndexedAddress,
} from "./addressIndex";

/**
 * The address index's pure half (E7, canon §2.3).
 *
 * The ranking and the matching rules are the whole product here: an
 * autocomplete that suggests the wrong person first is one people stop reading,
 * and one that cannot find "Gómez" from "gomez" is one they stop using.
 */

function address(overrides: Partial<IndexedAddress> = {}): IndexedAddress {
  return {
    email: "ana@example.com",
    displayName: "Ana Gómez",
    lastSeenAt: 1_000,
    timesSeen: 1,
    source: "browsed",
    ...overrides,
  };
}

describe("mergeSighting", () => {
  it("creates a row from the first sighting", () => {
    const merged = mergeSighting(undefined, {
      email: "Ana@Example.COM",
      displayName: "Ana Gómez",
      seenAt: 500,
      source: "sent",
    });
    // Lowercased on the way in: the address IS the identity.
    expect(merged).toEqual({
      email: "ana@example.com",
      displayName: "Ana Gómez",
      lastSeenAt: 500,
      timesSeen: 1,
      source: "sent",
    });
  });

  it("counts repeat sightings", () => {
    const first = mergeSighting(undefined, {
      email: "a@x.com",
      seenAt: 100,
      source: "browsed",
    });
    const second = mergeSighting(first, { email: "a@x.com", seenAt: 200, source: "browsed" });
    expect(second.timesSeen).toBe(2);
    expect(second.lastSeenAt).toBe(200);
  });

  it("adopts a newer display name", () => {
    const existing = address({ displayName: "Ana G." });
    const merged = mergeSighting(existing, {
      email: "ana@example.com",
      displayName: "Ana Gómez Ruiz",
      seenAt: 2_000,
      source: "browsed",
    });
    expect(merged.displayName).toBe("Ana Gómez Ruiz");
  });

  it("never lets an empty name erase a known one", () => {
    const existing = address({ displayName: "Ana Gómez" });
    for (const displayName of [undefined, "", "   "]) {
      const merged = mergeSighting(existing, {
        email: "ana@example.com",
        displayName,
        seenAt: 2_000,
        source: "browsed",
      });
      expect(merged.displayName).toBe("Ana Gómez");
    }
  });

  it("keeps the newest timestamp when an older message arrives later", () => {
    // The Sent scan walks backwards through old mail; it must not make an
    // address look staler than the message the user just opened.
    const existing = address({ lastSeenAt: 5_000 });
    const merged = mergeSighting(existing, {
      email: "ana@example.com",
      seenAt: 1_000,
      source: "sent",
    });
    expect(merged.lastSeenAt).toBe(5_000);
  });

  it("keeps the original source", () => {
    const existing = address({ source: "sent" });
    const merged = mergeSighting(existing, {
      email: "ana@example.com",
      seenAt: 2_000,
      source: "browsed",
    });
    expect(merged.source).toBe("sent");
  });
});

describe("addressesFromMessage", () => {
  it("takes from, to and cc", () => {
    const found = addressesFromMessage({
      from: [{ name: "Ana", email: "ana@x.com" }],
      to: [{ name: null, email: "bea@x.com" }],
      cc: [{ name: "Caro", email: "caro@x.com" }],
    });
    expect(found.map((entry) => entry.email)).toEqual([
      "ana@x.com",
      "bea@x.com",
      "caro@x.com",
    ]);
    // A null header name becomes undefined, never the string "null".
    expect(found[1]?.displayName).toBeUndefined();
  });

  it("never indexes bcc — the deliberate divergence", () => {
    /*
     * `bcc` is not part of the parameter type at all, which is the strongest
     * form this guarantee can take: indexing it would not compile. The cast
     * proves the RUNTIME behaviour too — a message object carrying the field
     * (as one straight off the wire does) contributes nothing from it.
     */
    const withBcc = {
      to: [{ name: null, email: "bea@x.com" }],
      bcc: [{ name: null, email: "secreto@x.com" }],
    };
    const found = addressesFromMessage(withBcc);
    expect(found.map((entry) => entry.email)).toEqual(["bea@x.com"]);
  });

  it("drops the account's own address", () => {
    const found = addressesFromMessage(
      {
        from: [{ name: "Yo", email: "Me@Example.com" }],
        to: [{ name: null, email: "otra@x.com" }],
      },
      "me@example.com",
    );
    expect(found.map((entry) => entry.email)).toEqual(["otra@x.com"]);
  });

  it("drops invalid addresses rather than indexing an unusable chip", () => {
    const found = addressesFromMessage({
      to: [
        { name: null, email: "no-arroba" },
        { name: null, email: "sin@dominio" },
        { name: null, email: "buena@x.com" },
      ],
    });
    expect(found.map((entry) => entry.email)).toEqual(["buena@x.com"]);
  });

  it("counts an address appearing twice in one message once", () => {
    const found = addressesFromMessage({
      to: [{ name: "Ana", email: "ana@x.com" }],
      cc: [{ name: "Ana", email: "ANA@x.com" }],
    });
    expect(found).toHaveLength(1);
  });

  it("tolerates absent and null header lists", () => {
    expect(addressesFromMessage({})).toEqual([]);
    expect(addressesFromMessage({ from: null, to: null, cc: null })).toEqual([]);
  });
});

describe("rankAddresses", () => {
  it("puts the most-used first", () => {
    const ranked = rankAddresses([
      address({ email: "poco@x.com", timesSeen: 1, lastSeenAt: 9_000 }),
      address({ email: "mucho@x.com", timesSeen: 12, lastSeenAt: 1_000 }),
    ]);
    // Frequency beats recency: the weekly correspondent outranks this
    // morning's one-off.
    expect(ranked[0]?.email).toBe("mucho@x.com");
  });

  it("breaks a frequency tie by recency", () => {
    const ranked = rankAddresses([
      address({ email: "vieja@x.com", timesSeen: 3, lastSeenAt: 1_000 }),
      address({ email: "nueva@x.com", timesSeen: 3, lastSeenAt: 9_000 }),
    ]);
    expect(ranked[0]?.email).toBe("nueva@x.com");
  });

  it("is a total order, so the list never reshuffles between keystrokes", () => {
    const same = { timesSeen: 2, lastSeenAt: 500 };
    const ranked = rankAddresses([
      address({ email: "b@x.com", ...same }),
      address({ email: "a@x.com", ...same }),
    ]);
    expect(ranked.map((entry) => entry.email)).toEqual(["a@x.com", "b@x.com"]);
  });

  it("does not mutate its input", () => {
    const input = [
      address({ email: "b@x.com", timesSeen: 1 }),
      address({ email: "a@x.com", timesSeen: 9 }),
    ];
    rankAddresses(input);
    expect(input[0]?.email).toBe("b@x.com");
  });
});

describe("foldForMatch", () => {
  it("removes accents and lowercases", () => {
    expect(foldForMatch("Ana GÓMEZ")).toBe("ana gomez");
    expect(foldForMatch("Muñoz")).toBe("munoz");
  });
});

describe("matchesQuery", () => {
  const ana = address({ email: "a.gomez@example.com", displayName: "Ana Gómez" });

  it("matches anywhere inside the address", () => {
    expect(matchesQuery(ana, "gomez")).toBe(true);
    expect(matchesQuery(ana, "example")).toBe(true);
    expect(matchesQuery(ana, "a.go")).toBe(true);
  });

  it("matches the start of the display name", () => {
    expect(matchesQuery(ana, "ana")).toBe(true);
    expect(matchesQuery(ana, "An")).toBe(true);
  });

  it("matches the start of any WORD of the display name", () => {
    // A surname is not "in the middle" of a name to anybody typing one.
    expect(matchesQuery(ana, "gó")).toBe(true);
    expect(matchesQuery(ana, "go")).toBe(true);
  });

  it("finds an accented name from unaccented typing", () => {
    expect(matchesQuery(address({ displayName: "Íñigo Muñoz", email: "im@x.com" }), "inigo")).toBe(
      true,
    );
    expect(matchesQuery(address({ displayName: "Íñigo Muñoz", email: "im@x.com" }), "munoz")).toBe(
      true,
    );
  });

  it("does NOT match the middle of a name word", () => {
    // Substring-on-name is what makes a suggestion list look random.
    const fernanda = address({ displayName: "Fernanda Mansilla", email: "fm@x.com" });
    expect(matchesQuery(fernanda, "man")).toBe(true); // word start — fine
    expect(matchesQuery(fernanda, "ans")).toBe(false); // mid-word — refused
  });

  it("refuses an empty query", () => {
    expect(matchesQuery(ana, "")).toBe(false);
    expect(matchesQuery(ana, "   ")).toBe(false);
  });

  it("handles a row with no display name", () => {
    const bare = address({ displayName: undefined, email: "bare@x.com" });
    expect(matchesQuery(bare, "bare")).toBe(true);
    expect(matchesQuery(bare, "ana")).toBe(false);
  });
});

describe("suggestAddresses", () => {
  const index = [
    address({ email: "ana@x.com", displayName: "Ana Gómez", timesSeen: 5, lastSeenAt: 100 }),
    address({ email: "andres@x.com", displayName: "Andrés Paz", timesSeen: 9, lastSeenAt: 50 }),
    address({ email: "bea@x.com", displayName: "Bea Ruiz", timesSeen: 2, lastSeenAt: 900 }),
  ];

  it("returns nothing for an empty query", () => {
    expect(suggestAddresses(index, "")).toEqual([]);
    expect(suggestAddresses(index, "  ")).toEqual([]);
  });

  it("returns matches ranked, not in index order", () => {
    const found = suggestAddresses(index, "an");
    expect(found.map((entry) => entry.email)).toEqual(["andres@x.com", "ana@x.com"]);
  });

  it("excludes addresses already chipped in the field", () => {
    const found = suggestAddresses(index, "an", ["ANDRES@x.com"]);
    expect(found.map((entry) => entry.email)).toEqual(["ana@x.com"]);
  });

  it("caps the list", () => {
    const many = Array.from({ length: 30 }, (_, n) =>
      address({ email: `user${String(n)}@x.com`, displayName: undefined, timesSeen: n }),
    );
    expect(suggestAddresses(many, "user")).toHaveLength(6);
    expect(suggestAddresses(many, "user", [], 2)).toHaveLength(2);
  });
});
