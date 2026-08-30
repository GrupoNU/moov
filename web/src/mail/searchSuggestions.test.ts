import { describe, expect, it } from "vitest";

import type { Label } from "./labelStore";
import {
  MAX_RECENT_SEARCHES,
  MAX_SUGGESTIONS,
  OPERATOR_HINTS,
  activeToken,
  buildSuggestions,
  loadRecentSearches,
  replaceActiveToken,
  saveRecentSearches,
  withRecentSearch,
} from "./searchSuggestions";

/** A Storage double, so the history's rules are tested without a browser. */
function fakeStorage(initial: Record<string, string> = {}): Storage {
  const data = new Map(Object.entries(initial));
  return {
    get length() {
      return data.size;
    },
    clear: () => {
      data.clear();
    },
    getItem: (key: string) => data.get(key) ?? null,
    key: (index: number) => Array.from(data.keys())[index] ?? null,
    removeItem: (key: string) => {
      data.delete(key);
    },
    setItem: (key: string, value: string) => {
      data.set(key, value);
    },
  };
}

const LABELS: readonly Label[] = [
  { keyword: "$label:Clientes", name: "Clientes", colorId: "slate", visibility: "show" },
  { keyword: "$label:Facturas", name: "Facturas", colorId: "slate", visibility: "show" },
  {
    keyword: "$label:Proyectos internos",
    name: "Proyectos internos",
    colorId: "slate",
    visibility: "show",
  },
];

describe("the recent-search history", () => {
  it("adds to the front", () => {
    expect(withRecentSearch(["b"], "a")).toEqual(["a", "b"]);
  });

  it("MOVES a repeat to the front instead of duplicating it", () => {
    expect(withRecentSearch(["a", "b", "c"], "c")).toEqual(["c", "a", "b"]);
  });

  it("caps at ten", () => {
    let history: readonly string[] = [];
    for (let i = 0; i < 15; i += 1) history = withRecentSearch(history, `q${i}`);
    expect(history).toHaveLength(MAX_RECENT_SEARCHES);
    expect(history[0]).toBe("q14");
  });

  it("ignores an empty query", () => {
    expect(withRecentSearch(["a"], "   ")).toEqual(["a"]);
  });

  it("round-trips through storage", () => {
    const storage = fakeStorage();
    saveRecentSearches(["from:ana", "informe"], storage);
    expect(loadRecentSearches(storage)).toEqual(["from:ana", "informe"]);
  });

  it("returns nothing for corrupt storage rather than throwing into a render", () => {
    expect(loadRecentSearches(fakeStorage({ "moov.search.recent": "{{{" }))).toEqual([]);
    expect(loadRecentSearches(fakeStorage({ "moov.search.recent": '"nope"' }))).toEqual([]);
    expect(
      loadRecentSearches(fakeStorage({ "moov.search.recent": '[1, null, "ok"]' })),
    ).toEqual(["ok"]);
  });

  it("survives a storage that throws — private mode", () => {
    /*
     * Built from scratch rather than spread over `fakeStorage()`: spreading an
     * object with a getter (`length`) evaluates it once and freezes the value,
     * and spreading a real class instance would drop its prototype. Neither
     * matters for these two methods, but the honest construction costs nothing
     * and does not teach the pattern.
     */
    const deny = (): never => {
      throw new Error("denied");
    };
    const hostile: Storage = {
      length: 0,
      clear: deny,
      getItem: deny,
      key: deny,
      removeItem: deny,
      setItem: deny,
    };
    expect(loadRecentSearches(hostile)).toEqual([]);
    expect(() => {
      saveRecentSearches(["x"], hostile);
    }).not.toThrow();
  });
});

describe("the active token", () => {
  it("is the text after the last space", () => {
    expect(activeToken("informe fr")).toBe("fr");
    expect(activeToken("informe")).toBe("informe");
    expect(activeToken("informe ")).toBe("");
  });

  it("replaces only the last token", () => {
    expect(replaceActiveToken("informe fr", "from:")).toBe("informe from:");
    expect(replaceActiveToken("fr", "from:")).toBe("from:");
    expect(replaceActiveToken("informe ", "from:")).toBe("informe from:");
  });
});

describe("buildSuggestions", () => {
  it("shows recent searches ONLY on an empty box", () => {
    const out = buildSuggestions({
      input: "",
      recent: ["from:ana", "informe"],
      labels: LABELS,
    });
    expect(out.map((s) => s.kind)).toEqual(["recent", "recent"]);
    // No wall of syntax for someone who just clicked into the field.
    expect(out.some((s) => s.kind === "operator")).toBe(false);
  });

  it("puts recent searches FIRST once typing starts — the plan's ranking", () => {
    const out = buildSuggestions({
      input: "fa",
      recent: ["facturas pendientes"],
      labels: LABELS,
    });
    expect(out[0]).toMatchObject({ kind: "recent", value: "facturas pendientes" });
    expect(out.some((s) => s.kind === "label")).toBe(true);
  });

  it("completes an operator from a prefix — typing 'fr' offers 'from:'", () => {
    const out = buildSuggestions({ input: "fr", recent: [], labels: [] });
    expect(out).toEqual([
      { kind: "operator", value: "from:", label: "from:", id: "operator:from:" },
    ]);
  });

  it("completes an operator on the LAST token, leaving earlier words alone", () => {
    const out = buildSuggestions({ input: "informe fr", recent: [], labels: [] });
    expect(out[0]?.value).toBe("informe from:");
  });

  it("does not suggest an operator the user has finished typing", () => {
    const out = buildSuggestions({ input: "from:", recent: [], labels: [] });
    expect(out.some((s) => s.label === "from:")).toBe(false);
  });

  it("offers a label as a complete label: term", () => {
    const out = buildSuggestions({ input: "clien", recent: [], labels: LABELS });
    expect(out[0]).toMatchObject({ kind: "label", value: "label:Clientes" });
  });

  it("quotes a multi-word label so it survives re-parsing", () => {
    const out = buildSuggestions({ input: "proye", recent: [], labels: LABELS });
    expect(out[0]?.value).toBe('label:"Proyectos internos"');
  });

  it("never suggests a deferred operator that would lead straight to a refusal", () => {
    // `filename:` is real Gmail and unsupported here. Suggesting it would be a
    // control that leads to an error — P4's dead control in disguise.
    expect(OPERATOR_HINTS).not.toContain("filename:");
    expect(OPERATOR_HINTS).not.toContain("header:");
    const out = buildSuggestions({ input: "fil", recent: [], labels: [] });
    expect(out).toEqual([]);
  });

  it("does not repeat a recent search identical to the input", () => {
    const out = buildSuggestions({ input: "informe", recent: ["informe"], labels: [] });
    expect(out.some((s) => s.kind === "recent")).toBe(false);
  });

  it("caps the whole list", () => {
    const recent = Array.from({ length: 20 }, (_, i) => `informe ${i}`);
    const out = buildSuggestions({ input: "informe", recent, labels: LABELS });
    expect(out.length).toBeLessThanOrEqual(MAX_SUGGESTIONS);
  });

  it("gives every suggestion a unique id for React", () => {
    const out = buildSuggestions({
      input: "a",
      recent: ["ana", "arquitectura"],
      labels: LABELS,
    });
    expect(new Set(out.map((s) => s.id)).size).toBe(out.length);
  });
});
