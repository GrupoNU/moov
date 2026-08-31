import { beforeEach, describe, expect, it } from "vitest";

import { loadAddressAutocomplete, saveAddressAutocomplete } from "./addressPrefs";
import { loadLabelState, saveLabelState } from "./labelStore";
import { DEFAULT_PREFS, type Prefs } from "./prefs";
import { prefsMigrationPatch, writePrefsMirrors } from "./prefsMirrors";
import { loadCacheDepth } from "../offline/cache";

/**
 * The mirrors and the one-time migration (prefs v2).
 *
 * These are the two halves of the wiring the gate's finding 2 asked for, and
 * the half that can go wrong quietly is the migration: it must carry a
 * browser's existing choices up to the account exactly once, converge, and
 * never resurrect data another device deleted.
 */

beforeEach(() => {
  window.localStorage.clear();
});

/** Prefs with a v2 key overridden, everything else at its default. */
function withPrefs(patch: Partial<Prefs>): Prefs {
  return { ...DEFAULT_PREFS, ...patch };
}

describe("writePrefsMirrors", () => {
  it("writes every mirror from the loaded preferences", () => {
    writePrefsMirrors(
      withPrefs({
        offlineDepth: { headersPerMailbox: 500, bodies: 250 },
        addressAutocomplete: "manual",
        labels: { "$label:work": { color: "teal", visibility: "hide" } },
      }),
    );

    expect(loadCacheDepth()).toEqual({ headersPerMailbox: 500, bodies: 250 });
    expect(loadAddressAutocomplete()).toBe(false);
    expect(loadLabelState().metadata).toEqual({
      "$label:work": { colorId: "teal", visibility: "hide" },
    });
  });

  it("keeps the local `known` set, which does not roam", () => {
    /*
     * The asymmetry stated in `labelStore.ts`: `known` answers "did THIS
     * browser create a label with no messages yet", which is not a fact about
     * the account. Overwriting it from prefs would make a just-created label
     * vanish from the sidebar on the first prefs load.
     */
    saveLabelState({ known: ["$label:fresh"], metadata: {} });
    writePrefsMirrors(withPrefs({ labels: {} }));
    expect(loadLabelState().known).toEqual(["$label:fresh"]);
  });

  it("drops mirrored metadata for a label deleted on another device", () => {
    // Prefs are the whole truth once loaded — a merge would resurrect it here
    // on every load.
    saveLabelState({
      known: [],
      metadata: { "$label:gone": { colorId: "red", visibility: "show" } },
    });
    writePrefsMirrors(withPrefs({ labels: {} }));
    expect(loadLabelState().metadata).toEqual({});
  });
});

describe("prefsMigrationPatch", () => {
  it("does nothing for a browser with no local choices", () => {
    expect(prefsMigrationPatch(DEFAULT_PREFS)).toBeUndefined();
  });

  it("carries this browser's label colours up to an account that has none", () => {
    saveLabelState({
      known: [],
      metadata: { "$label:work": { colorId: "blue", visibility: "hide" } },
    });

    expect(prefsMigrationPatch(DEFAULT_PREFS)).toEqual({
      labels: { "$label:work": { color: "blue", visibility: "hide" } },
    });
  });

  it("lets PREFS win a conflict — the account may have been set more recently", () => {
    /*
     * There is no local timestamp that could argue otherwise, so the only safe
     * rule is "push what the account has never heard of, keep what it has".
     */
    saveLabelState({
      known: [],
      metadata: {
        "$label:work": { colorId: "blue", visibility: "hide" },
        "$label:new": { colorId: "lime", visibility: "show" },
      },
    });

    const patch = prefsMigrationPatch(
      withPrefs({ labels: { "$label:work": { color: "red", visibility: "show" } } }),
    );

    expect(patch?.labels).toEqual({
      // The server's value survives…
      "$label:work": { color: "red", visibility: "show" },
      // …and only the genuinely-unknown one is added.
      "$label:new": { color: "lime", visibility: "show" },
    });
  });

  it("CONVERGES: once the push has landed, the next call is a no-op", () => {
    /*
     * The property that makes a flag unnecessary — and that matters because
     * `PutPrefs` advances the state cursor even for an identical write, so a
     * migration that re-fired every load would refresh every other tab forever.
     */
    saveLabelState({
      known: [],
      metadata: { "$label:work": { colorId: "blue", visibility: "hide" } },
    });
    saveAddressAutocomplete(false);

    const first = prefsMigrationPatch(DEFAULT_PREFS);
    expect(first).toBeDefined();

    // Apply it, as the provider would, and ask again.
    const applied = withPrefs(first ?? {});
    expect(prefsMigrationPatch(applied)).toBeUndefined();
  });

  it("carries an opt-out this browser stored", () => {
    saveAddressAutocomplete(false);
    expect(prefsMigrationPatch(DEFAULT_PREFS)).toEqual({ addressAutocomplete: "manual" });
  });

  it("never RE-ENABLES collection against an account that already opted out", () => {
    /*
     * The deliberate asymmetry. A browser whose mirror still says "on" is a
     * second device that predates the opt-out; honouring it would silently
     * resume collecting addresses, which is the harmful direction of this
     * particular setting.
     */
    saveAddressAutocomplete(true);
    expect(prefsMigrationPatch(withPrefs({ addressAutocomplete: "manual" }))).toBeUndefined();
  });

  it("ignores a browser that never expressed an opinion", () => {
    // `loadAddressAutocomplete` cannot tell a default from a choice, so the
    // migration checks for a STORED value: pushing a default would overwrite a
    // real choice made elsewhere with a value nobody chose.
    expect(loadAddressAutocomplete()).toBe(true);
    expect(prefsMigrationPatch(DEFAULT_PREFS)).toBeUndefined();
  });

  it("sends the whole migration as ONE patch", () => {
    // One conceptual event — "this browser had settings, the account now has
    // them" — so one save, one state advance, one rollback to reason about.
    saveLabelState({
      known: [],
      metadata: { "$label:work": { colorId: "blue", visibility: "hide" } },
    });
    saveAddressAutocomplete(false);

    const patch = prefsMigrationPatch(DEFAULT_PREFS);
    expect(Object.keys(patch ?? {}).sort()).toEqual(["addressAutocomplete", "labels"]);
  });

  it("caps the label migration at the durable-keyword ceiling", () => {
    /*
     * A browser that accumulated more than 26 entries across renames would
     * otherwise produce a patch the server refuses WHOLE — losing all of it
     * rather than the excess, and retrying on every load since nothing would
     * have been written to mark it done.
     */
    const metadata: Record<string, { colorId: string; visibility: "show" }> = {};
    for (let index = 0; index < 40; index += 1) {
      metadata[`$label:l${String(index)}`] = { colorId: "teal", visibility: "show" };
    }
    saveLabelState({ known: [], metadata });

    const patch = prefsMigrationPatch(DEFAULT_PREFS);
    expect(Object.keys(patch?.labels ?? {}).length).toBe(26);
  });
});
