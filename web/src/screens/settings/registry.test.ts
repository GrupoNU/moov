import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { en, es, type Strings } from "../../i18n/strings";
import { DEFAULT_PREFS, type PrefKey } from "../../mail/prefs";
import { foldForSearch } from "../../mail/settingsSearch";
import {
  ROW_PREF_KEYS,
  SECTION_IDS,
  SECTION_TITLES,
  SETTINGS_ROWS,
} from "./registry";

/**
 * The registry's own guarantees.
 *
 * The screen renders its rows as JSX and registers them here as data, and the
 * duplication is deliberate (see `registry.ts` on why generation would be a
 * worse abstraction). What the duplication costs is DRIFT, and this file is
 * the mechanism that makes drift a failing test rather than a row nobody can
 * find with the search.
 */

const dialogSource = readFileSync(
  resolve(process.cwd(), "src/screens/settings/SettingsDialog.tsx"),
  "utf8",
);

describe("coverage — every preference reaches a control", () => {
  it("gives EVERY server preference a row that writes it", () => {
    /*
     * The failure this exists to prevent: a preference that lives in the
     * schema, is validated by the server, and has no way for a user to change
     * it. That is invisible to the type checker, invisible to lint, and
     * invisible in a browser unless you already knew to look for it.
     */
    const written = new Set(Object.values(ROW_PREF_KEYS));
    const missing = (Object.keys(DEFAULT_PREFS) as PrefKey[]).filter(
      (key) => !written.has(key),
    );
    expect(
      missing,
      missing.length === 0
        ? ""
        : `These preferences have no settings row:\n  ${missing.join("\n  ")}`,
    ).toEqual([]);
  });

  it("maps every pref-writing row id to a row that actually exists", () => {
    const ids = new Set(SETTINGS_ROWS.map((row) => row.id));
    for (const id of Object.keys(ROW_PREF_KEYS)) {
      expect(ids.has(id), `ROW_PREF_KEYS names "${id}", which is not a row`).toBe(true);
    }
  });

  it("never maps two rows to the same preference", () => {
    // Two controls writing one key would race each other and disagree on
    // screen about what the value is.
    const keys = Object.values(ROW_PREF_KEYS);
    expect(new Set(keys).size).toBe(keys.length);
  });
});

describe("no drift between the registry and the rendered sheet", () => {
  it("renders every registered row's label key", () => {
    /*
     * A text scan of the component source, which is crude in the SAFE
     * direction: it can only produce a false NEGATIVE (a key mentioned in a
     * comment counts as rendered), never a false accusation that fails CI for
     * no reason. The defect it catches is the real one — a row registered for
     * search that the sheet never draws, so the search finds it and clicking
     * through shows nothing.
     */
    const unrendered = SETTINGS_ROWS.filter(
      (row) => !dialogSource.includes(`"${row.labelKey}"`),
    ).map((row) => row.id);
    expect(
      unrendered,
      unrendered.length === 0
        ? ""
        : `Registered but never rendered:\n  ${unrendered.join("\n  ")}`,
    ).toEqual([]);
  });

  it("registers every section the rail lists", () => {
    for (const id of SECTION_IDS) {
      expect(SECTION_TITLES[id], `section "${id}" has no title key`).toBeTruthy();
    }
  });

  it("puts every row in a section the rail can reach", () => {
    const sections = new Set<string>(SECTION_IDS);
    for (const row of SETTINGS_ROWS) {
      expect(
        sections.has(row.sectionId),
        `row "${row.id}" is in section "${row.sectionId}", which the rail does not list`,
      ).toBe(true);
    }
  });

  it("leaves no section empty", () => {
    // An empty section is a rail entry that opens onto nothing.
    const used = new Set(SETTINGS_ROWS.map((row) => row.sectionId));
    for (const id of SECTION_IDS) {
      expect(used.has(id), `section "${id}" has no rows`).toBe(true);
    }
  });

  it("gives every row a unique id", () => {
    const ids = SETTINGS_ROWS.map((row) => row.id);
    expect(new Set(ids).size).toBe(ids.length);
  });
});

describe("the strings exist in BOTH locales", () => {
  const keysOf = (row: (typeof SETTINGS_ROWS)[number]): readonly string[] =>
    row.descriptionKey === undefined ? [row.labelKey] : [row.labelKey, row.descriptionKey];

  it.each([
    ["en", en],
    ["es", es],
  ])("has a non-empty %s string for every row", (_name, table) => {
    const strings = table as unknown as Record<string, unknown>;
    for (const row of SETTINGS_ROWS) {
      for (const key of keysOf(row)) {
        const value = strings[key];
        expect(typeof value, `${key} is missing`).toBe("string");
        expect(String(value).trim(), `${key} is empty`).not.toBe("");
      }
    }
  });

  it("has a section title in both locales", () => {
    for (const id of SECTION_IDS) {
      const key = SECTION_TITLES[id] as keyof Strings;
      expect(typeof en[key]).toBe("string");
      expect(typeof es[key]).toBe("string");
    }
  });

  it("translates the sections rather than shipping the English twice", () => {
    /*
     * A copy-pasted Spanish block is the most likely way this table goes
     * wrong, and it type-checks perfectly. "General" and "Offline"→"Sin
     * conexión" are the honest exceptions: one is the same word in both
     * languages, the other is checked positively below.
     */
    expect(es["settings.section.inbox"]).not.toBe(en["settings.section.inbox"]);
    expect(es["settings.section.account"]).not.toBe(en["settings.section.account"]);
    expect(es["settings.section.offline"]).not.toBe(en["settings.section.offline"]);
  });
});

describe("the search synonyms", () => {
  it("gives every row at least one keyword in each language", () => {
    // A row with only English synonyms is unfindable by a Spanish-speaking
    // user who cannot see it, which is precisely the person the search is for.
    for (const row of SETTINGS_ROWS) {
      expect(row.keywords.length, `row "${row.id}" has no keywords`).toBeGreaterThanOrEqual(4);
    }
  });

  it("stores keywords already folded, so the matcher has nothing to undo", () => {
    /*
     * The matcher folds both sides anyway, so an accented keyword still works
     * — but an unaccented one is one less thing to get wrong when someone adds
     * a row, and it makes the table greppable with a plain ASCII search.
     */
    for (const row of SETTINGS_ROWS) {
      for (const keyword of row.keywords) {
        expect(keyword, `"${keyword}" on row "${row.id}" is not folded`).toBe(
          foldForSearch(keyword),
        );
      }
    }
  });

  it("does not repeat a keyword within one row", () => {
    for (const row of SETTINGS_ROWS) {
      expect(new Set(row.keywords).size, `row "${row.id}" repeats a keyword`).toBe(
        row.keywords.length,
      );
    }
  });
});

describe("what the sheet deliberately does NOT have", () => {
  it("has no IMAP or POP section — GC-9", () => {
    /*
     * Dovecot IS the IMAP server. Gmail's IMAP/POP settings exist to paper
     * over its own web-store-vs-IMAP impedance mismatch; porting them would
     * import Google's architectural debt to solve a problem we do not have.
     * This is a category error the plan names explicitly, so it is pinned
     * rather than left to reviewer memory.
     */
    for (const row of SETTINGS_ROWS) {
      expect(row.id.toLowerCase()).not.toContain("imap");
      expect(row.id.toLowerCase()).not.toContain("pop");
    }
    for (const id of SECTION_IDS) {
      expect(id).not.toContain("imap");
      expect(id).not.toContain("pop");
    }
  });

  it("has no messages-per-page setting — decision D-8", () => {
    // The list is virtualized, which makes a page size meaningless: it is a
    // paginated-legacy artifact, and D-8 signed NO.
    const labels = SETTINGS_ROWS.map((row) => en[row.labelKey]).join(" ").toLowerCase();
    expect(labels).not.toContain("per page");
    expect(labels).not.toContain("page size");
  });
});
