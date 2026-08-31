/**
 * Label METADATA — colour, sidebar visibility, and where they live (L3 E8).
 *
 * # The split this file exists to manage
 *
 * A label is two things stored in two places, and only one of them is durable:
 *
 *   - **The assignment** — "this message carries `$label:work`" — is an IMAP
 *     keyword on the message. That lives in Dovecot, is the source of truth
 *     (ADR-001), survives a rebuild of our store, and is visible to every other
 *     client. Nothing in this file touches it.
 *   - **The presentation** — what colour the chip is, whether the label shows
 *     in the sidebar always / only-when-unread / never — has no home on the
 *     server today.
 *
 * # Where the presentation lives now: prefs v2, and it ROAMS
 *
 * The gap this file used to name is closed. `Prefs.labels`
 * (`internal/jmap/mail/prefs.go`, `store.Prefs.Labels`) is the durable home —
 * a `{ [keyword]: { color, visibility } }` map served by the existing
 * `Prefs/get`/`Prefs/set` under `CAP_PREFS` — so a label created blue on the
 * laptop is blue on the phone.
 *
 * `localStorage` did not go away; it changed job. It is now a MIRROR of prefs,
 * exactly as the theme's is (`theme.ts`, and the reasoning on `store.Prefs`'
 * Theme field): the sidebar has to draw chips on the first paint after a reload,
 * and the session fetch has not resolved yet. The mirror is written through on
 * every prefs change and read only before prefs arrive, so it is a cache and
 * never a second source of truth.
 *
 * The `known` set stays LOCAL and is not mirrored into prefs, which is a
 * deliberate asymmetry rather than an oversight. It answers "did this browser
 * just create a label that has no messages yet", and a label that never got
 * applied anywhere is not a fact about the account — pushing it to the server
 * would resurrect a discarded label on every other device. `metadata` is the
 * half that describes a real, shared label, and it is the half that roams.
 *
 * # The one-time migration
 *
 * A browser that used the old scheme holds metadata prefs has never seen.
 * {@link labelMetadataMigration} computes the entries to push — the local ones
 * that prefs does not already carry — and the caller writes them through once,
 * on first load. Prefs WINS every conflict: a key present on both sides is the
 * server's, because the server's value may have come from another device that
 * set it more recently, and there is no local timestamp that could argue
 * otherwise.
 *
 * # Why the metadata is keyed by KEYWORD, not by a generated id
 *
 * The keyword IS the label's identity — it is what is on the messages and what
 * Bulwark, Sieve and any other client see. A separate id would need a mapping
 * that only this browser holds, so a label created elsewhere would arrive with
 * no metadata AND no way to acquire it. Keyed by keyword, a label discovered
 * from a message's flags simply gets the defaults.
 */

import { DEFAULT_LABEL_COLOR_ID, isLabelColorId } from "./labelPalette";
import { decodeLabelName, isLabelKeyword } from "./labels";
import { MAX_LABEL_PREFS, type LabelPrefs, type Prefs } from "./prefs";

/**
 * Gmail's `labelListVisibility`, adopted verbatim (canon §2.6, API-confirmed
 * and director-verified live).
 *
 * `showIfUnread` is the one worth having and the one most clients skip: a label
 * you only care about when something is waiting in it should not cost a
 * permanent row in a sidebar that already lists 24 folders.
 */
export const LABEL_VISIBILITIES = ["show", "showIfUnread", "hide"] as const;
export type LabelVisibility = (typeof LABEL_VISIBILITIES)[number];

export const DEFAULT_LABEL_VISIBILITY: LabelVisibility = "show";

/** The presentation of one label. */
export interface LabelMetadata {
  readonly colorId: string;
  readonly visibility: LabelVisibility;
}

export const DEFAULT_LABEL_METADATA: LabelMetadata = {
  colorId: DEFAULT_LABEL_COLOR_ID,
  visibility: DEFAULT_LABEL_VISIBILITY,
};

/** A label as the UI handles it: its keyword, its name, and its presentation. */
export interface Label {
  /** The IMAP keyword — `$label:<name>`. The identity. */
  readonly keyword: string;
  /** The display name, decoded from the keyword. */
  readonly name: string;
  readonly colorId: string;
  readonly visibility: LabelVisibility;
}

/** The whole local metadata table, keyed by keyword. */
export type LabelMetadataMap = Readonly<Record<string, LabelMetadata>>;

/**
 * Labels this browser knows about even though no fetched message carries them.
 *
 * Stored alongside the metadata because a freshly created label has no messages
 * yet: without a remembered list it would vanish from the sidebar the moment
 * the list refetched, and the user would create it again — and burn a second
 * keyword slot out of 26.
 */
export interface LabelState {
  readonly known: readonly string[];
  readonly metadata: LabelMetadataMap;
}

export const EMPTY_LABEL_STATE: LabelState = { known: [], metadata: {} };

const STORAGE_KEY = "moov.labels.v1";

function isVisibility(value: unknown): value is LabelVisibility {
  return (
    typeof value === "string" && (LABEL_VISIBILITIES as readonly string[]).includes(value)
  );
}

/**
 * Parses stored JSON into a state, discarding anything malformed.
 *
 * Every field is defaulted individually rather than the whole record being
 * rejected: a metadata entry naming a colour a newer build added should degrade
 * to grey, not take the other nineteen labels' colours down with it.
 */
export function parseLabelState(raw: unknown): LabelState {
  if (typeof raw !== "object" || raw === null) return EMPTY_LABEL_STATE;
  const record = raw as { known?: unknown; metadata?: unknown };

  const known: string[] = [];
  if (Array.isArray(record.known)) {
    for (const entry of record.known) {
      if (typeof entry === "string" && isLabelKeyword(entry) && !known.includes(entry)) {
        known.push(entry);
      }
    }
  }

  const metadata: Record<string, LabelMetadata> = {};
  if (typeof record.metadata === "object" && record.metadata !== null) {
    for (const [keyword, value] of Object.entries(record.metadata as Record<string, unknown>)) {
      if (!isLabelKeyword(keyword)) continue;
      const entry = value as { colorId?: unknown; visibility?: unknown };
      metadata[keyword] = {
        colorId:
          typeof entry.colorId === "string" && isLabelColorId(entry.colorId)
            ? entry.colorId
            : DEFAULT_LABEL_COLOR_ID,
        visibility: isVisibility(entry.visibility)
          ? entry.visibility
          : DEFAULT_LABEL_VISIBILITY,
      };
    }
  }

  return { known, metadata };
}

/** Reads the local label state. Never throws — a blocked storage is empty. */
export function loadLabelState(storage?: Storage): LabelState {
  try {
    const store = storage ?? globalThis.localStorage;
    const raw = store?.getItem(STORAGE_KEY);
    if (raw === null || raw === undefined) return EMPTY_LABEL_STATE;
    return parseLabelState(JSON.parse(raw));
  } catch {
    return EMPTY_LABEL_STATE;
  }
}

/** Writes the local label state. A blocked storage costs presentation only. */
export function saveLabelState(state: LabelState, storage?: Storage): void {
  try {
    const store = storage ?? globalThis.localStorage;
    store?.setItem(STORAGE_KEY, JSON.stringify(state));
  } catch {
    // Private mode, a full quota, a locked-down browser: the labels still work,
    // they are just grey and always visible. Never a thrown error on a colour.
  }
}

// ---------------------------------------------------------------------------
// prefs v2 — the durable home
// ---------------------------------------------------------------------------

/**
 * The prefs `labels` map, read as this module's metadata shape.
 *
 * The two differ in one field NAME only — prefs calls it `color`, this module
 * calls it `colorId` — and the rename is worth keeping rather than papering
 * over: `colorId` says out loud that the value is a palette id and not a hex
 * string, which is the palette's load-bearing decision (a contrast fix to the
 * amber swatch must reach every existing label). The wire uses the shorter name
 * because that is what the server validates.
 *
 * A colour the palette does not know degrades to the default, per-entry, the
 * same way {@link parseLabelState} degrades a stored one: a newer build's
 * swatch should make one chip grey, not take the other nineteen down with it.
 */
export function metadataFromPrefs(labels: Prefs["labels"]): LabelMetadataMap {
  const out: Record<string, LabelMetadata> = {};
  for (const [keyword, entry] of Object.entries(labels)) {
    if (!isLabelKeyword(keyword)) continue;
    out[keyword] = {
      colorId: isLabelColorId(entry.color) ? entry.color : DEFAULT_LABEL_COLOR_ID,
      visibility: entry.visibility,
    };
  }
  return out;
}

/** This module's metadata shape, rendered as the prefs `labels` map. */
export function metadataToPrefs(metadata: LabelMetadataMap): Record<string, LabelPrefs> {
  const out: Record<string, LabelPrefs> = {};
  for (const [keyword, meta] of Object.entries(metadata)) {
    out[keyword] = { color: meta.colorId, visibility: meta.visibility };
  }
  return out;
}

/**
 * The effective metadata: prefs when they are available, the local mirror
 * before they arrive.
 *
 * Not a merge. Once prefs have loaded they are the whole truth — a keyword the
 * mirror holds and prefs does not is a label whose metadata was DELETED on
 * another device, and merging would resurrect it on every load here. The
 * migration below is the one path that ever pushes local entries up, and it
 * runs once.
 */
export function effectiveMetadata(
  prefsLabels: Prefs["labels"],
  mirror: LabelMetadataMap,
  prefsAvailable: boolean,
): LabelMetadataMap {
  return prefsAvailable ? metadataFromPrefs(prefsLabels) : mirror;
}

/**
 * The one-time migration: the local entries prefs does not already carry.
 *
 * Returns `undefined` when there is nothing to do, so the caller can skip the
 * save entirely rather than write an identical object and burn a state advance
 * in every other tab (`PutPrefs` moves `updated_at` even for a no-op, which is
 * documented as deliberate and is exactly why we should not trigger it for
 * nothing).
 *
 * PREFS WIN every conflict. A keyword present on both sides keeps the server's
 * value, because the server's may have been set from another device more
 * recently and no local timestamp could argue otherwise. Only keywords prefs
 * has never heard of are pushed — which is precisely "what this browser knows
 * that the account does not".
 *
 * The result is capped at {@link MAX_LABEL_PREFS}, the durable-keyword ceiling
 * the server also enforces. A browser that accumulated more than 26 metadata
 * entries across renames would otherwise produce a migration the server refuses
 * WHOLE, losing all of it rather than the excess — and a refused migration
 * would retry on every load, since nothing would have been written to mark it
 * done.
 */
export function labelMetadataMigration(
  local: LabelMetadataMap,
  prefsLabels: Prefs["labels"],
): Record<string, LabelPrefs> | undefined {
  const merged: Record<string, LabelPrefs> = { ...metadataToPrefs(metadataFromPrefs(prefsLabels)) };
  let added = 0;
  for (const [keyword, meta] of Object.entries(local)) {
    if (keyword in merged) continue;
    if (Object.keys(merged).length >= MAX_LABEL_PREFS) break;
    merged[keyword] = { color: meta.colorId, visibility: meta.visibility };
    added += 1;
  }
  return added === 0 ? undefined : merged;
}

// ---------------------------------------------------------------------------
// deriving the label list
// ---------------------------------------------------------------------------

/**
 * The labels to show, from the keywords SEEN on messages plus the ones this
 * browser remembers creating.
 *
 * Discovery from messages is what makes a label created in Bulwark — or by a
 * Sieve rule — appear here at all. The remembered set is what keeps a
 * just-created, not-yet-applied label from disappearing on the next refetch.
 * Both are needed; either alone is a bug someone would file.
 *
 * Sorted by display name with `localeCompare`, so "Ámbito" sorts next to
 * "Ambos" for a Spanish reader rather than after "Zulu".
 */
export function deriveLabels(
  observedKeywords: readonly string[],
  state: LabelState,
  locale?: string,
): readonly Label[] {
  const keywords = new Set<string>();
  for (const keyword of observedKeywords) {
    if (isLabelKeyword(keyword)) keywords.add(keyword);
  }
  for (const keyword of state.known) keywords.add(keyword);

  const labels: Label[] = [];
  for (const keyword of keywords) {
    const name = decodeLabelName(keyword);
    if (name === undefined) continue;
    const meta = state.metadata[keyword] ?? DEFAULT_LABEL_METADATA;
    labels.push({ keyword, name, colorId: meta.colorId, visibility: meta.visibility });
  }
  return labels.sort((a, b) => a.name.localeCompare(b.name, locale));
}

/**
 * The labels carried by ONE message (or thread), resolved against the known
 * set so each chip gets its colour.
 *
 * A keyword with no entry in `byKeyword` still produces a chip, with the
 * defaults: it is a label another client created, and dropping it would make
 * the row lie about what the message carries. The label list and the chips
 * therefore agree by construction — both derive from the same keywords.
 *
 * Order follows `byKeyword`'s order (the sorted label list), not the message's
 * key order, so two rows carrying the same two labels show them in the same
 * order. `Object.keys` order is insertion order, which is the server's, which
 * varies per message.
 */
export function labelsFor(
  keywords: Readonly<Record<string, boolean>> | undefined,
  known: readonly Label[],
): readonly Label[] {
  if (keywords === undefined) return [];
  const present = new Set(
    Object.entries(keywords)
      .filter(([, value]) => value)
      .map(([keyword]) => keyword),
  );
  if (present.size === 0) return [];

  const out: Label[] = [];
  const claimed = new Set<string>();
  for (const label of known) {
    if (present.has(label.keyword)) {
      out.push(label);
      claimed.add(label.keyword);
    }
  }
  // Anything labelled but unknown: keep it, with the defaults.
  for (const keyword of present) {
    if (claimed.has(keyword)) continue;
    const name = decodeLabelName(keyword);
    if (name === undefined) continue;
    out.push({ keyword, name, ...DEFAULT_LABEL_METADATA });
  }
  return out;
}

/** Adds a label to the remembered set with its metadata. */
export function withLabel(
  state: LabelState,
  keyword: string,
  metadata: LabelMetadata,
): LabelState {
  const known = state.known.includes(keyword) ? state.known : [...state.known, keyword];
  return { known, metadata: { ...state.metadata, [keyword]: metadata } };
}

/** Removes a label from the remembered set and drops its metadata. */
export function withoutLabel(state: LabelState, keyword: string): LabelState {
  // Rebuilt by filtering rather than by copy-then-delete: the same discipline
  // `applyPatch` uses on keywords, and it avoids a dynamic `delete` on a map
  // whose keys are user-supplied strings.
  const metadata = Object.fromEntries(
    Object.entries(state.metadata).filter(([entry]) => entry !== keyword),
  );
  return { known: state.known.filter((entry) => entry !== keyword), metadata };
}

/**
 * Moves a label's metadata from one keyword to another — the local half of a
 * rename.
 *
 * The remote half (rewriting the keyword on every message) is
 * `mail/migrateKeyword.ts`. They are separate because they fail differently: a
 * migration can be partial and must report that, while this is a local map
 * update that cannot fail. Doing the local move only AFTER the migration
 * reports success is the caller's job, and is why this returns a new state
 * rather than writing one.
 */
export function renamedLabel(
  state: LabelState,
  from: string,
  to: string,
): LabelState {
  const metadata = state.metadata[from] ?? DEFAULT_LABEL_METADATA;
  return withLabel(withoutLabel(state, from), to, metadata);
}

/**
 * The labels the SIDEBAR shows, applying `labelListVisibility` (canon §2.6).
 *
 * `showIfUnread` needs to know whether the label currently has unread mail,
 * which only the caller can answer — so it is passed in as a predicate rather
 * than looked up here. A label whose unread state is unknown is treated as
 * having none, which errs toward a shorter sidebar rather than toward showing
 * every hidden label on a slow load.
 */
export function visibleLabels(
  labels: readonly Label[],
  hasUnread: (label: Label) => boolean,
): readonly Label[] {
  return labels.filter((label) => {
    switch (label.visibility) {
      case "show":
        return true;
      case "hide":
        return false;
      case "showIfUnread":
        return hasUnread(label);
    }
  });
}
