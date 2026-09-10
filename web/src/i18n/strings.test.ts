import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { brandName, en, es, type BrandName, type StringKey } from "./strings";

/**
 * The product is installed under the HOST's name, so a user-visible string
 * that says "Moov" on mail.areacorp.com.ar is wrong — the reader has never
 * heard of Moov and is looking at Área Mail.
 *
 * This is the test that keeps it that way. It walks both locale tables and
 * fails on any string that names the product, so the next hardcoded "Moov"
 * dies at the gate rather than in front of a customer.
 */

/**
 * The keys that may legitimately say "Moov", each with its reason.
 *
 * Written as a record and not a set so the reason is impossible to omit: a
 * future exception has to be argued in the same commit that adds it.
 */
const ALLOWED: Readonly<Partial<Record<StringKey, string>>> = {
  // AGPL-3.0 §13 attribution. A credit that renamed itself per host would be
  // no credit at all, and the licence obligation is not the customer's to
  // rebrand away.
  "legal.poweredBy": "attribution required by the licence; deliberately not branded",
};

/**
 * Renders one table entry so the sweep can read the words in it.
 *
 * A plain string is itself. A function is called with the brand in EVERY
 * argument slot — its arity is whatever it is, and a format string of three
 * numbers must not blow up a scan that is only looking for a leftover "Moov".
 * Passing the brand everywhere also means a brand string is exercised no
 * matter which position the brand sits in.
 */
function render(value: unknown, brand: BrandName): string {
  if (typeof value !== "function") return String(value);
  const fn = value as (...args: unknown[]) => string;
  const args = Array.from({ length: Math.max(fn.length, 1) }, () => brand);
  try {
    return fn(...args);
  } catch {
    // A format string that does arithmetic on its argument is not a brand
    // string, and its literal text is still what we want to scan.
    return fn.toString();
  }
}

describe("no locale string names the product", () => {
  const tables: readonly (readonly [string, Record<string, unknown>])[] = [
    ["en", en],
    ["es", es],
  ];

  it.each(tables)("%s says the host's brand, never Moov", (_locale, table) => {
    // A name that could not be mistaken for anything already in the table,
    // so a leftover literal cannot hide behind a coincidence.
    const brand = brandName("Área Mail");
    const offenders: string[] = [];

    for (const [key, value] of Object.entries(table)) {
      if (key in ALLOWED) continue;
      if (render(value, brand).includes("Moov")) offenders.push(key);
    }

    expect(offenders, `these keys hardcode "Moov"`).toEqual([]);
  });

  it.each(tables)("%s substitutes the brand wherever the product is named", (_locale, table) => {
    const brand = brandName("Área Mail");
    const branded = Object.entries(table).filter(
      ([key, value]) => typeof value === "function" && !(key in ALLOWED) && takesOnlyBrand(value),
    );

    // A floor, not an exact count: the point is that the mechanism is wired
    // to real strings, and an exact number would break on every new string.
    expect(branded.length).toBeGreaterThanOrEqual(20);
    for (const [key, value] of branded) {
      expect(render(value, brand), `${key} ignores the brand`).toContain("Área Mail");
    }
  });

  it("keeps the allowlist honest — every allowed key exists and does say Moov", () => {
    for (const key of Object.keys(ALLOWED) as StringKey[]) {
      for (const [, table] of tables) {
        const value = table[key];
        expect(value, `${key} is not in the table`).toBeDefined();
        expect(render(value, brandName("Área Mail"))).toContain("Moov");
      }
    }
  });
});

/** True when a table value is a one-argument function — the brand shape. */
function takesOnlyBrand(value: unknown): boolean {
  return typeof value === "function" && value.length === 1 && isBrandShaped(value);
}

/**
 * Distinguishes a brand string from a one-argument format string at RUNTIME.
 *
 * The type system tells them apart nominally, which is invisible here, so the
 * test calls the function with the brand and asks whether the brand came out.
 * A format string like `label.renameTitle` also would — it interpolates
 * whatever it is given — so this is only used to pick candidates, never to
 * assert correctness on its own.
 */
function isBrandShaped(value: unknown): boolean {
  try {
    return (value as (b: BrandName) => string)(brandName("Área Mail")).includes("Área Mail");
  } catch {
    return false;
  }
}

describe("Spanish stays grammatical for an arbitrary brand", () => {
  /**
   * The failure this guards is subtle and permanent: a Spanish sentence that
   * writes "el ${brand}" or "una ${brand}" reads as broken for half the names
   * a customer could choose, because the name's gender and number are
   * unknowable. The safe constructions are a bare apposition ("${brand} no
   * responde") and "de ${brand}".
   *
   * It reads the SOURCE rather than the rendered strings, because the article
   * and the interpolation are adjacent in the source and nowhere else — once
   * rendered, "el Área Mail" is just three words and the test would have to
   * know every brand name to spot it.
   */
  const source = readFileSync(
    join(dirname(fileURLToPath(import.meta.url)), "strings.ts"),
    "utf8",
  );
  const spanish = source.slice(source.indexOf("export const es: Strings"));

  it.each([["el"], ["la"], ["los"], ["las"], ["un"], ["una"]])(
    "never writes %s immediately before the brand",
    (article) => {
      const pattern = new RegExp(String.raw`\b${article}\s+\$\{brand\}`, "i");
      expect(spanish).not.toMatch(pattern);
    },
  );
});
