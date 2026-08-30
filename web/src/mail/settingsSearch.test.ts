import { describe, expect, it } from "vitest";

import {
  foldForSearch,
  haystackFor,
  queryTerms,
  rowMatches,
  searchSettings,
  type SearchableRow,
} from "./settingsSearch";

const ROWS: readonly SearchableRow[] = [
  {
    id: "language",
    sectionId: "general",
    label: "Idioma",
    description: "El idioma en el que está escrita la interfaz de Moov.",
    keywords: ["language", "locale", "español", "english"],
  },
  {
    id: "images",
    sectionId: "general",
    label: "Imágenes remotas",
    description: "Las imágenes siempre pasan por el proxy de Moov.",
    keywords: ["images", "privacidad", "proxy", "tracking"],
  },
  {
    id: "density",
    sectionId: "appearance",
    label: "Densidad",
    description: "Cuánto espacio ocupa cada fila.",
    keywords: ["density", "compacta", "rows", "filas"],
  },
  {
    id: "theme",
    sectionId: "appearance",
    label: "Tema",
    description: "Elegí cómo se ve Moov.",
    keywords: ["theme", "dark", "oscuro", "claro"],
  },
];

describe("foldForSearch", () => {
  it("strips accents so a keyboard without them still finds the row", () => {
    // The single most likely near-miss in a Spanish UI.
    expect(foldForSearch("Imágenes")).toBe("imagenes");
    expect(foldForSearch("Contraseña")).toBe("contrasena");
  });

  it("lowercases and trims", () => {
    expect(foldForSearch("  TEMA  ")).toBe("tema");
  });

  it("leaves an already-folded string alone", () => {
    expect(foldForSearch("density")).toBe("density");
  });
});

describe("queryTerms", () => {
  it("returns no terms for an empty or blank query", () => {
    expect(queryTerms("")).toEqual([]);
    expect(queryTerms("   ")).toEqual([]);
  });

  it("splits on whitespace and folds each term", () => {
    expect(queryTerms("  Imágenes  Remotas ")).toEqual(["imagenes", "remotas"]);
  });
});

describe("rowMatches", () => {
  const images = ROWS[1]!;

  it("matches everything when the query is empty", () => {
    expect(rowMatches(images, [])).toBe(true);
  });

  it("matches on the label", () => {
    expect(rowMatches(images, queryTerms("imagenes"))).toBe(true);
  });

  it("matches on the description, not only the label", () => {
    expect(rowMatches(images, queryTerms("proxy de moov"))).toBe(true);
  });

  it("matches on a hardcoded synonym the label never says", () => {
    // The whole reason synonyms exist: "privacidad" appears nowhere in the
    // rendered row, and it is exactly what a worried user types.
    expect(haystackFor(images)).toContain("privacidad");
    expect(rowMatches(images, queryTerms("privacidad"))).toBe(true);
  });

  it("matches the English name from a Spanish UI", () => {
    // Very common in this market: the user knows the setting by its English
    // name even with the interface in Spanish.
    expect(rowMatches(images, queryTerms("images"))).toBe(true);
  });

  it("requires ALL terms, so a multi-word query narrows", () => {
    expect(rowMatches(images, queryTerms("imagenes proxy"))).toBe(true);
    expect(rowMatches(images, queryTerms("imagenes densidad"))).toBe(false);
  });

  it("does not match on a substring of nothing — no fuzziness", () => {
    // A settings list this small has no room for a false positive.
    expect(rowMatches(images, queryTerms("imgenes"))).toBe(false);
  });
});

describe("searchSettings", () => {
  it("shows everything, and every section, when nothing is typed", () => {
    const result = searchSettings(ROWS, "");
    expect(result.isFiltering).toBe(false);
    expect(result.isEmpty).toBe(false);
    expect(result.rowIds.size).toBe(ROWS.length);
    expect([...result.sectionIds].sort()).toEqual(["appearance", "general"]);
  });

  it("narrows to the matching rows and reveals only their sections", () => {
    const result = searchSettings(ROWS, "oscuro");
    expect(result.isFiltering).toBe(true);
    expect([...result.rowIds]).toEqual(["theme"]);
    // The General section must disappear entirely rather than render as an
    // empty heading.
    expect([...result.sectionIds]).toEqual(["appearance"]);
  });

  it("can match rows across more than one section", () => {
    // "moov" appears in the language description and the theme description.
    const result = searchSettings(ROWS, "moov");
    expect(result.sectionIds.size).toBe(2);
  });

  it("reports an honest empty state rather than an empty screen", () => {
    const result = searchSettings(ROWS, "cryptography");
    expect(result.isFiltering).toBe(true);
    expect(result.isEmpty).toBe(true);
    expect(result.rowIds.size).toBe(0);
    expect(result.sectionIds.size).toBe(0);
  });

  it("is never 'empty' when the user has typed nothing", () => {
    // The distinction the screen renders on: no query is not a failed search.
    expect(searchSettings([], "").isEmpty).toBe(false);
    expect(searchSettings([], "anything").isEmpty).toBe(true);
  });

  it("is accent-insensitive end to end", () => {
    expect([...searchSettings(ROWS, "imagenes").rowIds]).toEqual(["images"]);
    expect([...searchSettings(ROWS, "IMÁGENES").rowIds]).toEqual(["images"]);
  });
});
