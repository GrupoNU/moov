import { beforeEach, describe, expect, it } from "vitest";

import { DEFAULT_LABEL_COLOR_ID } from "./labelPalette";
import {
  DEFAULT_LABEL_METADATA,
  DEFAULT_LABEL_VISIBILITY,
  EMPTY_LABEL_STATE,
  deriveLabels,
  loadLabelState,
  parseLabelState,
  renamedLabel,
  saveLabelState,
  visibleLabels,
  withLabel,
  withoutLabel,
  type Label,
  type LabelState,
} from "./labelStore";

/** A minimal in-memory Storage, so the tests never touch a real localStorage. */
function memoryStorage(): Storage {
  const map = new Map<string, string>();
  return {
    get length() {
      return map.size;
    },
    clear: () => {
      map.clear();
    },
    getItem: (key: string) => map.get(key) ?? null,
    key: (index: number) => [...map.keys()][index] ?? null,
    removeItem: (key: string) => {
      map.delete(key);
    },
    setItem: (key: string, value: string) => {
      map.set(key, value);
    },
  };
}

let storage: Storage;

beforeEach(() => {
  storage = memoryStorage();
});

describe("persistence", () => {
  it("round-trips a state through storage", () => {
    const state: LabelState = {
      known: ["$label:work"],
      metadata: { "$label:work": { colorId: "blue", visibility: "showIfUnread" } },
    };
    saveLabelState(state, storage);
    expect(loadLabelState(storage)).toEqual(state);
  });

  it("reads an empty state when nothing was stored", () => {
    expect(loadLabelState(storage)).toEqual(EMPTY_LABEL_STATE);
  });

  it("survives malformed JSON rather than throwing on boot", () => {
    storage.setItem("moov.labels.v1", "{not json");
    expect(loadLabelState(storage)).toEqual(EMPTY_LABEL_STATE);
  });

  it("survives a storage that throws — private mode, full quota", () => {
    const hostile = {
      getItem: () => {
        throw new Error("denied");
      },
      setItem: () => {
        throw new Error("denied");
      },
    } as unknown as Storage;
    expect(loadLabelState(hostile)).toEqual(EMPTY_LABEL_STATE);
    // A blocked write costs the colour and nothing else: it must not throw.
    expect(() => {
      saveLabelState(EMPTY_LABEL_STATE, hostile);
    }).not.toThrow();
  });
});

describe("parseLabelState defaults per field, never wholesale", () => {
  it("defaults an unknown colour without discarding the other labels", () => {
    const parsed = parseLabelState({
      known: ["$label:a", "$label:b"],
      metadata: {
        "$label:a": { colorId: "chartreuse", visibility: "hide" },
        "$label:b": { colorId: "blue", visibility: "show" },
      },
    });
    expect(parsed.metadata["$label:a"]).toEqual({
      colorId: DEFAULT_LABEL_COLOR_ID,
      visibility: "hide",
    });
    expect(parsed.metadata["$label:b"]).toEqual({ colorId: "blue", visibility: "show" });
  });

  it("defaults an unknown visibility", () => {
    const parsed = parseLabelState({
      known: [],
      metadata: { "$label:a": { colorId: "blue", visibility: "sometimes" } },
    });
    expect(parsed.metadata["$label:a"]?.visibility).toBe(DEFAULT_LABEL_VISIBILITY);
  });

  it("drops entries whose key is not a label keyword", () => {
    const parsed = parseLabelState({
      known: ["$seen", "$label:ok", 42],
      metadata: { $seen: { colorId: "blue", visibility: "show" } },
    });
    expect(parsed.known).toEqual(["$label:ok"]);
    expect(parsed.metadata).toEqual({});
  });

  it("rejects a non-object", () => {
    expect(parseLabelState(null)).toEqual(EMPTY_LABEL_STATE);
    expect(parseLabelState("labels")).toEqual(EMPTY_LABEL_STATE);
  });

  it("de-duplicates the known list", () => {
    const parsed = parseLabelState({ known: ["$label:a", "$label:a"], metadata: {} });
    expect(parsed.known).toEqual(["$label:a"]);
  });
});

describe("deriveLabels", () => {
  it("discovers labels from the keywords seen on messages", () => {
    // This is what makes a label created in Bulwark, or by a Sieve rule,
    // appear at all.
    const labels = deriveLabels(["$seen", "$label:work", "NonJunk"], EMPTY_LABEL_STATE);
    expect(labels.map((label) => label.name)).toEqual(["work"]);
    expect(labels[0]?.colorId).toBe(DEFAULT_LABEL_COLOR_ID);
  });

  it("keeps a just-created label that no message carries yet", () => {
    // Without this, a new label vanishes on the next refetch and the user
    // creates it again — burning a second slot out of 26.
    const state = withLabel(EMPTY_LABEL_STATE, "$label:nuevo", DEFAULT_LABEL_METADATA);
    expect(deriveLabels([], state).map((label) => label.name)).toEqual(["nuevo"]);
  });

  it("does not duplicate a label that is both known and observed", () => {
    const state = withLabel(EMPTY_LABEL_STATE, "$label:work", DEFAULT_LABEL_METADATA);
    expect(deriveLabels(["$label:work"], state)).toHaveLength(1);
  });

  it("applies the stored metadata", () => {
    const state = withLabel(EMPTY_LABEL_STATE, "$label:work", {
      colorId: "blue",
      visibility: "hide",
    });
    const [label] = deriveLabels(["$label:work"], state);
    expect(label?.colorId).toBe("blue");
    expect(label?.visibility).toBe("hide");
  });

  it("sorts by display name with locale rules", () => {
    const names = deriveLabels(
      ["$label:zulu", "$label:Ámbito", "$label:ambos"],
      EMPTY_LABEL_STATE,
      "es",
    ).map((label) => label.name);
    // "Ámbito" sorts with the As, not after Z.
    expect(names[names.length - 1]).toBe("zulu");
    expect(names).toContain("Ámbito");
  });

  it("keeps a nested name whole", () => {
    const labels = deriveLabels(["$label:work/clients"], EMPTY_LABEL_STATE);
    expect(labels[0]?.name).toBe("work/clients");
    expect(labels[0]?.keyword).toBe("$label:work/clients");
  });
});

describe("state transitions", () => {
  it("adds a label with its metadata", () => {
    const state = withLabel(EMPTY_LABEL_STATE, "$label:a", {
      colorId: "red",
      visibility: "hide",
    });
    expect(state.known).toEqual(["$label:a"]);
    expect(state.metadata["$label:a"]?.colorId).toBe("red");
  });

  it("does not add the same keyword twice", () => {
    let state = withLabel(EMPTY_LABEL_STATE, "$label:a", DEFAULT_LABEL_METADATA);
    state = withLabel(state, "$label:a", { colorId: "blue", visibility: "show" });
    expect(state.known).toEqual(["$label:a"]);
    expect(state.metadata["$label:a"]?.colorId).toBe("blue");
  });

  it("removes a label and forgets its metadata", () => {
    const state = withoutLabel(
      withLabel(EMPTY_LABEL_STATE, "$label:a", DEFAULT_LABEL_METADATA),
      "$label:a",
    );
    expect(state.known).toEqual([]);
    expect(state.metadata).toEqual({});
  });

  it("carries the metadata across a rename", () => {
    const before = withLabel(EMPTY_LABEL_STATE, "$label:work", {
      colorId: "teal",
      visibility: "showIfUnread",
    });
    const after = renamedLabel(before, "$label:work", "$label:trabajo");
    expect(after.known).toEqual(["$label:trabajo"]);
    expect(after.metadata["$label:trabajo"]).toEqual({
      colorId: "teal",
      visibility: "showIfUnread",
    });
    expect(after.metadata["$label:work"]).toBeUndefined();
  });

  it("gives a renamed label the defaults when it had no metadata", () => {
    const after = renamedLabel(EMPTY_LABEL_STATE, "$label:a", "$label:b");
    expect(after.metadata["$label:b"]).toEqual(DEFAULT_LABEL_METADATA);
  });
});

describe("visibleLabels — Gmail's labelListVisibility", () => {
  const make = (name: string, visibility: Label["visibility"]): Label => ({
    keyword: `$label:${name}`,
    name,
    colorId: DEFAULT_LABEL_COLOR_ID,
    visibility,
  });

  const labels = [make("always", "show"), make("unread", "showIfUnread"), make("never", "hide")];

  it("always shows 'show' and never shows 'hide'", () => {
    const shown = visibleLabels(labels, () => false).map((label) => label.name);
    expect(shown).toEqual(["always"]);
  });

  it("shows 'showIfUnread' only when the label has unread mail", () => {
    const shown = visibleLabels(labels, (label) => label.name === "unread").map(
      (label) => label.name,
    );
    expect(shown).toEqual(["always", "unread"]);
  });

  it("keeps 'hide' hidden even when it has unread mail", () => {
    const shown = visibleLabels(labels, () => true).map((label) => label.name);
    expect(shown).not.toContain("never");
  });
});
