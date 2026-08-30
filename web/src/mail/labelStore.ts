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
 * # The named gap: this is localStorage, and it does not roam
 *
 * The server's `Prefs` singleton (`internal/jmap/mail/prefs.go`) validates
 * STRICTLY: an unknown key is refused with `invalidProperties`, which is the
 * correct behaviour and is why this module does not invent one. There is no
 * `labels` key in the schema, and adding one is a server change that belongs to
 * the server, not to a client that decides unilaterally what the wire looks
 * like.
 *
 * So label metadata is kept in `localStorage`, per browser, and the honest
 * consequence — stated here and in the settings UI rather than discovered by a
 * user — is:
 *
 *   **A label's colour and sidebar visibility do not roam between devices.**
 *   Create "Clientes" in blue on the laptop and it is grey on the phone. The
 *   label itself, its name and every message it is on are fully shared; only
 *   the presentation is local.
 *
 * @todo Prefs schema v2 (`internal/jmap/mail/prefs.go` + `store.Prefs`) is the
 *   durable home: a `labels` key holding
 *   `{ [keyword: string]: { color: string; visibility: LabelVisibility } }`,
 *   served by the existing `Prefs/get` / `Prefs/set` under `CAP_PREFS`. When it
 *   lands, {@link loadLabelMetadata} reads it from prefs with the localStorage
 *   copy as a one-time migration source, and this comment gets deleted rather
 *   than amended. Until then, the gap above is real and named.
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
