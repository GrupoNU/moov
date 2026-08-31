/**
 * The localStorage mirrors of prefs v2, and the one-time migrations that seeded
 * them (L3 E5/E7/E8/E9b — the wiring the gate's finding 2 named).
 *
 * # What a mirror is here, and what it is not
 *
 * Three v2 preferences are needed by code that runs BEFORE the session's
 * `Prefs/get` resolves, or in a boot where it never will:
 *
 *   - **the offline depth** — an offline cold boot has no server at all, and a
 *     cache running at the default in exactly the situation the user configured
 *     it for is a setting that does nothing;
 *   - **the autocomplete choice** — the index's write-through fires on the
 *     first render, and defaulting to "collect" for that window would gather
 *     addresses a user opted out of, which is the one failure the setting
 *     exists to prevent;
 *   - **label presentation** — the sidebar draws chips on the first paint, and
 *     a flash of grey-then-coloured labels is the same defect the theme's
 *     pre-paint script exists to avoid.
 *
 * In all three the mirror is a CACHE and prefs are the truth. This module owns
 * the one direction that keeps that honest: prefs → mirror, written through on
 * every change. Nothing here ever reads a mirror to decide what to save.
 *
 * The theme established the pattern and states its own reasoning
 * (`store.Prefs.Theme`: "a pre-paint cache of this field, not a second source
 * of truth"); it keeps its own module because it is applied to the DOM rather
 * than consumed by a React tree.
 *
 * # The migrations, and why they run exactly once
 *
 * Each of these settings previously lived ONLY in localStorage, so an existing
 * browser holds a real user choice the account has never seen. The migration
 * pushes it up on first load. It must be idempotent and it must converge: a
 * migration that re-fired every load would fight another device's change on
 * every reconnect, and `PutPrefs` advances the state cursor even for a no-op
 * write, so a repeated push would also refresh every other open tab forever.
 *
 * Convergence comes from the migration functions themselves rather than from a
 * "migrated" flag: each returns `undefined` when the account already carries
 * the local value, so once the push lands the next call is a no-op. A flag
 * would have been a fourth piece of local state to get wrong, and — worse — it
 * would make the migration unrepeatable on a browser where the first attempt
 * failed.
 */

import {
  addressAutocompleteMigration,
  saveAddressAutocomplete,
  addressAutocompleteEnabled,
} from "./addressPrefs";
import {
  labelMetadataMigration,
  loadLabelState,
  metadataFromPrefs,
  saveLabelState,
} from "./labelStore";
import { saveCacheDepth } from "../offline/cache";
import type { Prefs } from "./prefs";

/**
 * Writes every mirror from a loaded preference object.
 *
 * Called only when prefs are AVAILABLE — a server without the capability must
 * not have its defaults overwrite a choice the mirror is legitimately holding,
 * which is the same gate `App.tsx` puts on the theme reconciliation and for the
 * same reason.
 */
export function writePrefsMirrors(prefs: Prefs, storage?: Storage): void {
  saveCacheDepth(prefs.offlineDepth, storage);
  saveAddressAutocomplete(addressAutocompleteEnabled(prefs.addressAutocomplete), storage);
  /*
   * The label mirror keeps its `known` set untouched: that half is local by
   * design (a label this browser created and has not applied to anything yet is
   * not a fact about the account), and only `metadata` roams.
   */
  const local = loadLabelState(storage);
  saveLabelState({ known: local.known, metadata: metadataFromPrefs(prefs.labels) }, storage);
}

/**
 * The preference patch that carries this browser's pre-prefs choices up to the
 * account, or `undefined` when there is nothing to migrate.
 *
 * All three are computed together and returned as ONE patch so the whole
 * migration is a single `Prefs/set`. Three separate saves would be three state
 * advances, three chances to half-succeed, and three rollbacks to reason about
 * for what is conceptually one event: "this browser had settings, the account
 * now has them".
 */
export function prefsMigrationPatch(
  prefs: Prefs,
  storage?: Storage,
): Partial<Prefs> | undefined {
  const patch: { -readonly [K in keyof Prefs]?: Prefs[K] } = {};
  let any = false;

  const labels = labelMetadataMigration(loadLabelState(storage).metadata, prefs.labels);
  if (labels !== undefined) {
    patch.labels = labels;
    any = true;
  }

  const autocomplete = addressAutocompleteMigration(prefs.addressAutocomplete, storage);
  if (autocomplete !== undefined) {
    patch.addressAutocomplete = autocomplete;
    any = true;
  }

  /*
   * The offline depth has NO migration, and its absence is deliberate: it never
   * had a localStorage life before this change. `HEADER_CAP`/`BODY_CAP` were
   * hard-coded constants, not a stored user choice, so there is nothing a
   * browser could be holding that the account does not already have. The mirror
   * for it is created by the write-through above, on the first load.
   */

  return any ? patch : undefined;
}
