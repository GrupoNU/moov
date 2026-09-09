/**
 * Preferences — the client mirror of the server's `Prefs` singleton (L3 E5).
 *
 * # The contract this file consumes
 *
 * The server (epic E0, `internal/jmap/mail/prefs.go`) serves ONE object per
 * account under the vendor capability {@link CAP_PREFS}. Its id is the constant
 * `"singleton"`, which is not an invention: RFC 8621 §8 gives `VacationResponse`
 * exactly that shape, so every client's `/get` and `/set` machinery works
 * unchanged. Three methods exist — `Prefs/get`, `Prefs/set`, `Prefs/changes` —
 * and this module uses the first two.
 *
 * # Why the defaults live here AND on the server
 *
 * The server fills every field on read, so a materialized object always arrives
 * complete and {@link DEFAULT_PREFS} is never consulted for a live account. It
 * exists for the two moments where there is no server answer yet: the first
 * render before the load resolves, and a session that does not advertise the
 * capability at all. Rendering a settings screen with `undefined` controls
 * while a request is in flight is how a settings panel flickers every value on
 * open.
 *
 * A test pins these defaults against the server's own (`store.Prefs`), so the
 * duplication cannot drift silently — which is risk 6 of the L3 plan
 * ("preference schema drift"), answered with a test rather than a promise.
 *
 * # Feature detection is mandatory, never assumed
 *
 * A vendor capability is by definition something a server may not have. The
 * session is inspected for {@link CAP_PREFS} before any request is made
 * ({@link sessionHasPrefs}); without it the app runs on defaults and the
 * settings screen says so, rather than issuing a call the server will reject
 * with `unknownCapability` and painting the failure as an error the user caused.
 */

import { CAP_CORE, type JmapClient, type JmapSession } from "../api/jmap";

/** The vendor capability the preference methods live under (jmap.CapPrefs). */
export const CAP_PREFS = "https://moov.email/ns/prefs";

/** The wire id of the singleton (RFC 8621 §8's shape). */
export const PREFS_ID = "singleton";

// ---------------------------------------------------------------------------
// the value domains — each mirrors a closed set the server validates against
// ---------------------------------------------------------------------------

/**
 * The undo-send windows, in seconds.
 *
 * Gmail's exact offered set (canon §2.3, /mail/answer/2819488). The server
 * additionally clamps to [5, 30] on the send path, so a value arriving by any
 * other route still produces an honorable window.
 */
export const UNDO_SEND_SECONDS = [5, 10, 20, 30] as const;
export type UndoSendSeconds = (typeof UNDO_SEND_SECONDS)[number];

/** D-4: "always" means "always THROUGH the HMAC proxy" — never unproxied. */
export const IMAGES_POLICIES = ["always", "ask"] as const;
export type ImagesPolicy = (typeof IMAGES_POLICIES)[number];

/** Canon §2.2: Gmail offers "older messages, newer messages, or the list". */
export const AUTO_ADVANCE = ["list", "newer", "older"] as const;
export type AutoAdvance = (typeof AUTO_ADVANCE)[number];

/** Canon §2.4. The three names are Gmail's; the pixel values are ours. */
export const DENSITIES = ["default", "comfortable", "compact"] as const;
export type Density = (typeof DENSITIES)[number];

/** Canon §2.4 (/9499937): "No split" / "Right of inbox" / "Below inbox". */
export const READING_PANES = ["none", "right", "bottom"] as const;
export type ReadingPane = (typeof READING_PANES)[number];

/**
 * The DETERMINISTIC subset of Gmail's six inbox types (canon §2.4).
 *
 * "Important first" and "Priority Inbox" need the importance classifier and
 * are deferred to the AI phase by the same rule that cut notifications to two
 * modes (GC-2): a control whose classifier does not exist is a control that
 * does nothing.
 */
export const INBOX_TYPES = ["default", "unread_first", "starred_first"] as const;
export type InboxType = (typeof INBOX_TYPES)[number];

/** GC-2: two modes pre-AI. The third ("important only") is classifier-gated. */
export const NOTIFICATION_MODES = ["new", "off"] as const;
export type NotificationMode = (typeof NOTIFICATION_MODES)[number];

// --- v2: the roaming keys of epics E5, E7, E8 and E9b ---------------------
//
// These six mirror `store.Prefs`' v2 block. Each replaces a localStorage
// "named gap" its epic declared: label presentation, the offline depth, the
// autocomplete opt-out, the Send & Archive button, the reply default and the
// named signatures. The server validates every one of them
// (`internal/jmap/mail/prefs.go`), so the domains below are not advisory —
// a value outside them is refused with `invalidProperties` and rolled back.

/**
 * Gmail's three label-list visibilities (canon §2.6), governing the SIDEBAR
 * only: a hidden label still applies to its messages and still renders on the
 * message itself.
 *
 * Mirrors the server's `labelVisibilityChoices`. `labelStore.ts` carries the
 * same triple as `LABEL_VISIBILITIES` for the local shape; a test pins that the
 * two agree, because they are the same domain written twice.
 */
export const LABEL_PREF_VISIBILITIES = ["show", "showIfUnread", "hide"] as const;
export type LabelPrefVisibility = (typeof LABEL_PREF_VISIBILITIES)[number];

/** Gmail's "create contacts for autocomplete" (canon §2.3). */
export const ADDRESS_AUTOCOMPLETE_MODES = ["auto", "manual"] as const;
export type AddressAutocompleteMode = (typeof ADDRESS_AUTOCOMPLETE_MODES)[number];

/**
 * Which reply the reader offers FIRST (canon §2.3).
 *
 * Gmail's default is "reply", and the reason to adopt it is not deference: the
 * failure modes are asymmetric. Defaulting to reply-all means a user eventually
 * answers a mailing list in a message they meant for one person, which cannot
 * be taken back; defaulting to reply costs a click.
 */
export const REPLY_BEHAVIORS = ["reply", "replyAll"] as const;
export type ReplyBehavior = (typeof REPLY_BEHAVIORS)[number];

/**
 * The inclusive bounds the server enforces on the offline depths
 * (`minOfflineHeaders`/`maxOfflineHeaders`/`minOfflineBodies`/`maxOfflineBodies`).
 *
 * They are mirrored rather than fetched from the capability so a settings
 * control can constrain the user BEFORE a save is refused. The floors are not
 * decoration: a header depth below a screenful makes the offline list visibly
 * truncated at the first scroll, which reads as data loss rather than as a
 * setting.
 */
export const OFFLINE_DEPTH_BOUNDS = {
  headersPerMailbox: { min: 50, max: 1000 },
  bodies: { min: 20, max: 500 },
} as const;

/** The caps the server enforces on the v2 collections. */
export const MAX_LABEL_PREFS = 26;
export const MAX_SIGNATURE_ITEMS = 10;

// --- v3: the folder rail's visibility map (P0-5) --------------------------

/**
 * Which folders the rail draws, keyed by mailbox DISPLAY NAME.
 *
 * The same three values Gmail gives its label list, and deliberately the same
 * three {@link LabelPrefVisibility} already carries: a user who has learned
 * "mostrar / ocultar / mostrar si hay sin leer" in one table meets it again in
 * the other. Mirrors the server's `folderVisibilityChoices`.
 *
 * Keyed by NAME rather than id because that is what the settings table shows
 * and what survives a folder being recreated — ids are per-account and opaque.
 */
export const FOLDER_VISIBILITIES = ["show", "hide", "showIfUnread"] as const;
export type FolderVisibility = (typeof FOLDER_VISIBILITIES)[number];

/** The cap the server enforces on the folder-rail map. */
export const MAX_FOLDER_VISIBILITY = 200;

/** One label's presentation metadata, as prefs carries it. */
export interface LabelPrefs {
  /** A palette id — a NAME such as "amber", never a hex value. */
  readonly color: string;
  readonly visibility: LabelPrefVisibility;
}

/** How much mail the PWA keeps for offline reading (E9b). */
export interface OfflineDepth {
  readonly headersPerMailbox: number;
  readonly bodies: number;
}

/** One named signature (E7). `htmlBody` is sanitized by the server on the way in. */
export interface SignatureItem {
  readonly name: string;
  readonly textBody: string;
  readonly htmlBody: string;
}

/**
 * The named-signature model (E7), and the precedence rule against the
 * per-identity signature — MIRRORED here from `store.Prefs.Signatures`, which is
 * where it is stated authoritatively:
 *
 * ```
 * MOOV's own PWA, composing new mail : if signatures.forNew names an existing
 *                                      item, use that item's body; otherwise
 *                                      fall back to the Identity's signature.
 * MOOV's own PWA, composing a reply  : the same, with forReply.
 * Any other JMAP client              : the Identity's signature, always.
 * ```
 *
 * This is a PRESENTATION-layer preference, not a protocol divergence: the
 * composer inserts the signature into the body before submission, exactly as
 * RFC 8621 §6 says a client SHOULD do with the Identity's own. The server
 * assembles no signature into any message, so two clients can only ever
 * disagree about what the composer PRE-FILLED.
 *
 * {@link resolveSignature} is the one implementation of the rule, and
 * `prefs.test.ts` pins it against the quoted table above.
 */
export interface SignaturePrefs {
  readonly items: Readonly<Record<string, SignatureItem>>;
  /** An item id, or null for "fall back to the Identity's own signature". */
  readonly forNew: string | null;
  readonly forReply: string | null;
}

/** Account-level, like Gmail's. localStorage keeps a pre-paint COPY. */
export const THEMES = ["light", "dark", "system"] as const;
export type Theme = (typeof THEMES)[number];

/**
 * The languages the settings screen offers.
 *
 * `null` is "follow the browser" and is what the wire carries for it — the
 * server stores `""` internally and renders JSON null, because `""` is not a
 * BCP 47 tag and a client would have to guess what it meant.
 */
export const LANGUAGES = ["es", "en"] as const;
export type Language = (typeof LANGUAGES)[number];

// ---------------------------------------------------------------------------
// the object
// ---------------------------------------------------------------------------

/** One account's preferences, materialized — every field always present. */
export interface Prefs {
  readonly undoSendSeconds: UndoSendSeconds;
  readonly imagesPolicy: ImagesPolicy;
  readonly conversationView: boolean;
  readonly hoverActions: boolean;
  readonly autoAdvance: AutoAdvance;
  readonly density: Density;
  readonly showSnippets: boolean;
  readonly keyboardShortcuts: boolean;
  /** A BCP 47 tag, or null for "follow the browser". */
  readonly language: Language | null;
  readonly readingPane: ReadingPane;
  readonly inboxType: InboxType;
  readonly notifications: NotificationMode;
  readonly theme: Theme;

  // --- v2 ---

  /** Label presentation, keyed by the IMAP keyword (`$label:work`). */
  readonly labels: Readonly<Record<string, LabelPrefs>>;
  readonly offlineDepth: OfflineDepth;
  readonly addressAutocomplete: AddressAutocompleteMode;
  readonly sendAndArchive: boolean;
  readonly defaultReplyBehavior: ReplyBehavior;
  readonly signatures: SignaturePrefs;

  // --- v3 ---

  /**
   * Rail visibility per folder, keyed by display NAME (P0-5).
   *
   * ABSENT entries are the common case and mean "the policy decides" — see
   * `railCuration.ts`, which owns the default. An empty map is therefore not
   * "everything hidden"; it is "the user has said nothing", which is where
   * every account starts.
   */
  readonly folderVisibility: Readonly<Record<string, FolderVisibility>>;
}

/**
 * The v2 keys, as data.
 *
 * Used by {@link prefsSchemaVersion} to decide whether a response came from a
 * server that serves them, and by the tests that pin the wire shape. Listing
 * them once means a seventh key added to the interface without being added here
 * is caught by the `satisfies` below rather than by a user.
 */
export const PREFS_V2_KEYS = [
  "labels",
  "offlineDepth",
  "addressAutocomplete",
  "sendAndArchive",
  "defaultReplyBehavior",
  "signatures",
] as const satisfies readonly (keyof Prefs)[];

export type PrefsV2Key = (typeof PREFS_V2_KEYS)[number];

/**
 * The v3 keys, as data — one, so far.
 *
 * Separate from {@link PREFS_V2_KEYS} rather than appended to it, because
 * feature detection is per-SCHEMA: a server may serve v2 and not v3 for the
 * length of a deploy, and a settings table gated on "serves v2" would offer a
 * control whose save comes back `unknownProperty`.
 */
export const PREFS_V3_KEYS = ["folderVisibility"] as const satisfies readonly (keyof Prefs)[];

export type PrefsV3Key = (typeof PREFS_V3_KEYS)[number];

/**
 * The product defaults.
 *
 * These are the SERVER's defaults, restated. `keyboardShortcuts` is true —
 * decision D-3, a signed divergence from Gmail's off-by-default, taken because
 * our audience is the power-user end of the market (regla 2) and Gmail's own
 * rationale for the opposite is unsourced (canon §5).
 *
 * `hoverActions` is true because Gmail's are ON by default with a single
 * "Disable hover actions" setting (canon §2.2), and `autoAdvance` is "list"
 * because Gmail's auto-advance is OFF by default — "back to the conversation
 * list" IS that off state expressed as one of three values.
 */
export const DEFAULT_PREFS: Prefs = {
  undoSendSeconds: 10,
  imagesPolicy: "always",
  conversationView: true,
  hoverActions: true,
  autoAdvance: "list",
  density: "default",
  showSnippets: true,
  keyboardShortcuts: true,
  language: null,
  readingPane: "right",
  inboxType: "default",
  notifications: "off",
  theme: "light",

  // v2. The two collections start EMPTY rather than absent, which is the one
  // place this mirror deliberately differs from the stored form: the server
  // omits an empty map (`omitempty`) because a missing key and an empty one
  // carry the same information, but a consumer reading `undefined` would have to
  // decide whether it meant "no labels" or "unknown", and every call site would
  // carry that branch. An always-present object is the shape a component can
  // read and spread without a special case — the same argument the server makes
  // for rendering `{}` rather than null on the wire (`prefsLabelsValue`).
  labels: {},
  offlineDepth: { headersPerMailbox: 200, bodies: 100 },
  addressAutocomplete: "auto",
  // A REGISTERED DIVERGENCE from Gmail (whose setting ships off), taken after
  // the fact: the button already shipped visible in Moov's composer, so a
  // default of false would REMOVE a control users already have.
  sendAndArchive: true,
  defaultReplyBehavior: "reply",
  signatures: { items: {}, forNew: null, forReply: null },

  // v3. Empty for the same reason `labels` is: the server omits an empty map,
  // and a consumer reading `undefined` would have to branch on "no choices"
  // versus "unknown". Empty here means the POLICY decides every folder, which
  // is where every account starts.
  folderVisibility: {},
};

/** The keys a caller may set, one at a time. */
export type PrefKey = keyof Prefs;

// ---------------------------------------------------------------------------
// parsing — the server is trusted for shape, never for values
// ---------------------------------------------------------------------------

function oneOf<T extends string>(
  value: unknown,
  choices: readonly T[],
  fallback: T,
): T {
  return typeof value === "string" && (choices as readonly string[]).includes(value)
    ? (value as T)
    : fallback;
}

function boolOr(value: unknown, fallback: boolean): boolean {
  return typeof value === "boolean" ? value : fallback;
}

/** A finite whole number clamped into an inclusive range, or the fallback. */
function boundedInt(
  value: unknown,
  { min, max }: { readonly min: number; readonly max: number },
  fallback: number,
): number {
  if (typeof value !== "number" || !Number.isInteger(value)) return fallback;
  if (value < min || value > max) return fallback;
  return value;
}

/**
 * Reads the label-presentation map.
 *
 * An entry missing either half is DROPPED rather than defaulted, which is the
 * opposite of how the scalar preferences degrade — and deliberately so. A
 * scalar has one honest fallback (the product default); a half-written label
 * entry would render a chip in a colour the user never picked, and dropping it
 * returns the label to the default swatch, visible, which is exactly the state
 * "no metadata" already means. The label itself is never affected: it lives in
 * an IMAP keyword (A6) and nothing here can delete one.
 *
 * The cap is applied on the way in as well as by the server, so a map that grew
 * past the durable-keyword ceiling by any route cannot make the sidebar render
 * entries that can never survive an index rebuild. Extra entries are dropped in
 * key order, which is stable.
 */
function parseLabelPrefs(value: unknown): Readonly<Record<string, LabelPrefs>> {
  if (typeof value !== "object" || value === null) return {};
  const out: Record<string, LabelPrefs> = {};
  let kept = 0;
  for (const [keyword, raw] of Object.entries(value as Record<string, unknown>)) {
    if (kept >= MAX_LABEL_PREFS) break;
    if (typeof raw !== "object" || raw === null) continue;
    const entry = raw as { color?: unknown; visibility?: unknown };
    if (typeof entry.color !== "string") continue;
    if (
      typeof entry.visibility !== "string" ||
      !(LABEL_PREF_VISIBILITIES as readonly string[]).includes(entry.visibility)
    ) {
      continue;
    }
    out[keyword] = { color: entry.color, visibility: entry.visibility as LabelPrefVisibility };
    kept += 1;
  }
  return out;
}

/**
 * Reads the folder-rail visibility map (v3).
 *
 * An entry whose value is not one of the three is DROPPED, not defaulted, for
 * the same reason a malformed label entry is: defaulting it to "show" would
 * silently reveal a folder the user had hidden, which is the wrong way for a
 * parse failure to fall. Dropping it hands the folder back to the policy,
 * which is the state it was in before anyone chose.
 */
function parseFolderVisibility(value: unknown): Readonly<Record<string, FolderVisibility>> {
  if (typeof value !== "object" || value === null) return {};
  const out: Record<string, FolderVisibility> = {};
  let kept = 0;
  for (const [name, raw] of Object.entries(value as Record<string, unknown>)) {
    if (kept >= MAX_FOLDER_VISIBILITY) break;
    if (typeof raw !== "string") continue;
    if (!(FOLDER_VISIBILITIES as readonly string[]).includes(raw)) continue;
    out[name] = raw as FolderVisibility;
    kept += 1;
  }
  return out;
}

/** Reads the offline depth, clamping each half independently. */
function parseOfflineDepth(value: unknown): OfflineDepth {
  if (typeof value !== "object" || value === null) return DEFAULT_PREFS.offlineDepth;
  const o = value as { headersPerMailbox?: unknown; bodies?: unknown };
  return {
    headersPerMailbox: boundedInt(
      o.headersPerMailbox,
      OFFLINE_DEPTH_BOUNDS.headersPerMailbox,
      DEFAULT_PREFS.offlineDepth.headersPerMailbox,
    ),
    bodies: boundedInt(
      o.bodies,
      OFFLINE_DEPTH_BOUNDS.bodies,
      DEFAULT_PREFS.offlineDepth.bodies,
    ),
  };
}

/**
 * Reads the named-signature model.
 *
 * `forNew`/`forReply` are dropped when they name no surviving item, rather than
 * being carried as a dangling id. The server refuses a dangling reference on
 * write for a stated reason — the fallback it would silently produce is a
 * DIFFERENT signature going out under the user's name — and the read path
 * honours the same rule: a reference that resolves to nothing IS "none", and
 * {@link resolveSignature} then falls back to the Identity's signature, which is
 * the documented behaviour for "no named signature selected".
 */
function parseSignaturePrefs(value: unknown): SignaturePrefs {
  if (typeof value !== "object" || value === null) return DEFAULT_PREFS.signatures;
  const o = value as { items?: unknown; forNew?: unknown; forReply?: unknown };

  const items: Record<string, SignatureItem> = {};
  if (typeof o.items === "object" && o.items !== null) {
    let kept = 0;
    for (const [id, raw] of Object.entries(o.items as Record<string, unknown>)) {
      if (kept >= MAX_SIGNATURE_ITEMS) break;
      if (typeof raw !== "object" || raw === null) continue;
      const entry = raw as { name?: unknown; textBody?: unknown; htmlBody?: unknown };
      items[id] = {
        name: typeof entry.name === "string" ? entry.name : "",
        textBody: typeof entry.textBody === "string" ? entry.textBody : "",
        htmlBody: typeof entry.htmlBody === "string" ? entry.htmlBody : "",
      };
      kept += 1;
    }
  }

  const ref = (raw: unknown): string | null =>
    typeof raw === "string" && raw !== "" && raw in items ? raw : null;

  return { items, forNew: ref(o.forNew), forReply: ref(o.forReply) };
}

/**
 * Reads a `Prefs` object off the wire, defaulting every field it cannot
 * recognise.
 *
 * The server validates strictly and fills every key, so in practice nothing
 * here fires. It is written defensively anyway for the one case that WILL
 * happen: a client newer than the server it is talking to, mid-deploy, where a
 * key this build knows about is simply absent from the response. Falling back
 * per FIELD means such a client degrades one row at a time instead of throwing
 * away the twelve preferences the old server did send.
 */
export function parsePrefs(raw: unknown): Prefs {
  if (typeof raw !== "object" || raw === null) return DEFAULT_PREFS;
  const o = raw as Record<string, unknown>;

  const seconds = o.undoSendSeconds;
  const undoSendSeconds =
    typeof seconds === "number" &&
    (UNDO_SEND_SECONDS as readonly number[]).includes(seconds)
      ? (seconds as UndoSendSeconds)
      : DEFAULT_PREFS.undoSendSeconds;

  // `language` is String|null on the wire: null means "follow the browser",
  // and an unknown tag is treated the same way rather than pinning the UI to
  // a locale this build has no strings for.
  const language =
    typeof o.language === "string" && (LANGUAGES as readonly string[]).includes(o.language)
      ? (o.language as Language)
      : null;

  return {
    undoSendSeconds,
    imagesPolicy: oneOf(o.imagesPolicy, IMAGES_POLICIES, DEFAULT_PREFS.imagesPolicy),
    conversationView: boolOr(o.conversationView, DEFAULT_PREFS.conversationView),
    hoverActions: boolOr(o.hoverActions, DEFAULT_PREFS.hoverActions),
    autoAdvance: oneOf(o.autoAdvance, AUTO_ADVANCE, DEFAULT_PREFS.autoAdvance),
    density: oneOf(o.density, DENSITIES, DEFAULT_PREFS.density),
    showSnippets: boolOr(o.showSnippets, DEFAULT_PREFS.showSnippets),
    keyboardShortcuts: boolOr(o.keyboardShortcuts, DEFAULT_PREFS.keyboardShortcuts),
    language,
    readingPane: oneOf(o.readingPane, READING_PANES, DEFAULT_PREFS.readingPane),
    inboxType: oneOf(o.inboxType, INBOX_TYPES, DEFAULT_PREFS.inboxType),
    notifications: oneOf(o.notifications, NOTIFICATION_MODES, DEFAULT_PREFS.notifications),
    theme: oneOf(o.theme, THEMES, DEFAULT_PREFS.theme),

    // v2. Every one of these tolerates ABSENCE, which is not a hypothetical:
    // a v1 server (or a v2 one mid-deploy) sends none of them, and this build
    // must render the settings screen from its own defaults rather than throw
    // away the fourteen keys the old server did send.
    labels: parseLabelPrefs(o.labels),
    offlineDepth: parseOfflineDepth(o.offlineDepth),
    addressAutocomplete: oneOf(
      o.addressAutocomplete,
      ADDRESS_AUTOCOMPLETE_MODES,
      DEFAULT_PREFS.addressAutocomplete,
    ),
    sendAndArchive: boolOr(o.sendAndArchive, DEFAULT_PREFS.sendAndArchive),
    defaultReplyBehavior: oneOf(
      o.defaultReplyBehavior,
      REPLY_BEHAVIORS,
      DEFAULT_PREFS.defaultReplyBehavior,
    ),
    signatures: parseSignaturePrefs(o.signatures),

    // v3. Absent on a v2 server, and absence is not a problem: an empty map
    // means the rail's own policy decides, which is what a v2 server's rail
    // did anyway.
    folderVisibility: parseFolderVisibility(o.folderVisibility),
  };
}

/**
 * Whether a served preference object came from a server that knows the v2 keys.
 *
 * # Why this is feature-detected from the OBJECT and not from a version number
 *
 * The server publishes no schema version on the wire, and that is by design:
 * `store.PrefsSchemaVersion` is metadata ABOUT the stored document, spliced in
 * at storage time and deliberately kept off the JMAP object because RFC 8621 has
 * no place for it (`encodePrefs` states exactly this). What the session DOES
 * advertise is the capability, and what the capability advertises is the
 * DOMAINS — `labelColorValues`, the offline bounds, the signature caps — which
 * exist only in the v2 server.
 *
 * So the honest detection is structural: a v2 server always renders all six
 * keys (`prefsObject` writes them unconditionally, and the two maps render as
 * `{}` rather than null precisely so a client never has to distinguish "empty"
 * from "missing"). Their presence is therefore exactly "this server serves v2",
 * and their absence is exactly "it does not".
 *
 * What it is FOR: a settings screen must not offer a control whose save the
 * server will refuse with `unknownProperty`. A row gated on this renders as
 * unavailable instead — the same honesty `sessionHasPrefs` buys for the whole
 * screen, at key granularity, for the deploy window in which a new PWA is
 * talking to an old moovd.
 */
export function servesPrefsV2(raw: unknown): boolean {
  if (typeof raw !== "object" || raw === null) return false;
  const o = raw as Record<string, unknown>;
  return PREFS_V2_KEYS.every((key) => o[key] !== undefined);
}

/**
 * Whether a served preference object came from a server that knows v3.
 *
 * The same structural detection as {@link servesPrefsV2} and for the same
 * reason — no version rides the wire — read at v3's own granularity because a
 * deploy window can serve v2 and not v3.
 *
 * What it is FOR here: the folder-visibility TABLE in settings. On a v2 server
 * the rail still curates itself (the policy needs no preference), but a switch
 * the user flips would come back `unknownProperty` and silently revert. The
 * table renders as unavailable instead — the same honesty the v2 rows get.
 */
export function servesPrefsV3(raw: unknown): boolean {
  if (typeof raw !== "object" || raw === null) return false;
  const o = raw as Record<string, unknown>;
  return PREFS_V3_KEYS.every((key) => o[key] !== undefined);
}

/**
 * The signature body the composer pre-fills, per the precedence rule documented
 * on {@link SignaturePrefs} (and authoritatively on `store.Prefs.Signatures`).
 *
 * Both halves of a named signature are returned, because the composer picks by
 * body mode: rich composition takes `html`, plain takes `text`. A named
 * signature with an empty `htmlBody` — which is every signature this UI can
 * create, since HTML editing is not built — falls back to its own `textBody`
 * for the HTML case, so choosing a named signature never blanks the rich
 * composer's footer.
 *
 * Returning `undefined` means "no named signature applies": the caller uses the
 * Identity's own `textSignature`/`htmlSignature`, which is the RFC 8621 §6
 * behaviour every other JMAP client sees.
 */
export function resolveSignature(
  signatures: SignaturePrefs,
  intent: "new" | "reply",
): { readonly text: string; readonly html: string } | undefined {
  const id = intent === "new" ? signatures.forNew : signatures.forReply;
  if (id === null) return undefined;
  const item = signatures.items[id];
  if (item === undefined) return undefined;
  return { text: item.textBody, html: item.htmlBody === "" ? item.textBody : item.htmlBody };
}

// ---------------------------------------------------------------------------
// feature detection
// ---------------------------------------------------------------------------

/**
 * True when this session advertises the preference capability.
 *
 * BOTH places are checked. RFC 8620 §2 puts a capability in the session's
 * top-level `capabilities` (the server implements it) and in each account's
 * `accountCapabilities` (this account may use it) — and they can legitimately
 * differ, which is the whole point of the two maps. Accepting either is the
 * tolerant read: a server that advertises it anywhere is a server that will
 * answer, and refusing on a technicality would leave the user on defaults with
 * a working backend one field away.
 */
export function sessionHasPrefs(
  session: JmapSession | undefined,
  accountId?: string,
): boolean {
  if (session === undefined) return false;
  if (CAP_PREFS in session.capabilities) return true;
  if (accountId === undefined) return false;
  const account = session.accounts[accountId];
  return account !== undefined && CAP_PREFS in account.accountCapabilities;
}

// ---------------------------------------------------------------------------
// the two calls
// ---------------------------------------------------------------------------

/** What a `Prefs/get` or `Prefs/set` came back with. */
export interface PrefsResult {
  readonly prefs: Prefs;
  /** The server's state cursor, for a future `Prefs/changes`. */
  readonly state: string;
  /**
   * Whether this server serves the v3 keys (P0-5).
   *
   * Carried on the RESULT rather than recomputed by callers, because it can
   * only be read from the RAW response: `parsePrefs` fills every absent key
   * with a default, so by the time a `Prefs` object exists the evidence of
   * what the server actually sent is gone. Detected once, where the raw object
   * is still in hand.
   */
  readonly servesV3: boolean;
}

function firstResponse(
  responses: readonly (readonly [string, Record<string, unknown>, string])[],
  clientId: string,
): Record<string, unknown> {
  for (const [name, args, id] of responses) {
    if (id !== clientId) continue;
    if (name === "error") {
      const type = typeof args.type === "string" ? args.type : "unknown";
      const description =
        typeof args.description === "string" ? `: ${args.description}` : "";
      throw new Error(`Prefs method error: ${type}${description}`);
    }
    return args;
  }
  throw new Error(`no response for call "${clientId}"`);
}

/**
 * Loads the account's preferences.
 *
 * `ids: null` returns the one object, which is what RFC 8620 §5.1 prescribes
 * ("If null, then all records in the data set are returned") and what keeps
 * this a plain `/get` rather than a bespoke method shape.
 */
export async function fetchPrefs(
  client: JmapClient,
  accountId: string,
  signal?: AbortSignal,
): Promise<PrefsResult> {
  const response = await client.call(
    [["Prefs/get", { accountId, ids: null }, "p"]],
    [CAP_CORE, CAP_PREFS],
    signal,
  );
  const args = firstResponse(response.methodResponses, "p");
  const list = (args.list ?? []) as readonly unknown[];
  return {
    prefs: parsePrefs(list[0]),
    state: typeof args.state === "string" ? args.state : "",
    servesV3: servesPrefsV3(list[0]),
  };
}

/**
 * Saves a partial change to the singleton.
 *
 * The patch is sent as an `update` on the id `"singleton"`, per RFC 8620 §5.3.
 * Only the changed keys travel: the server reads, patches, validates and
 * writes the whole object, so sending all thirteen would make every save a
 * chance to overwrite a field another tab just changed.
 *
 * A per-record `notUpdated` entry is turned into a THROWN error rather than a
 * quiet return, because the caller's rollback path is what must run. §5.3's
 * `invalidProperties` names the offending keys and the server describes each —
 * that sentence is carried through so the UI can show the server's own words
 * instead of "save failed".
 *
 * # The v2 keys need no encoding step, and that is a property worth naming
 *
 * `Partial<Prefs>` is sent VERBATIM as the PatchObject. That works for the three
 * structured v2 properties only because this mirror was built to the server's
 * wire shape rather than to a convenient client shape: `labels` is
 * `{[keyword]: {color, visibility}}` on both sides, `offlineDepth` is
 * `{headersPerMailbox, bodies}`, and `signatures` is `{items, forNew, forReply}`
 * with `null` — not `""` — for an unset reference, which is exactly what
 * `parseSignatureRef` accepts and what `prefsOptionalID` emits.
 *
 * Had the client modelled any of them differently (a `Map`, a `colorId` field,
 * an `""` sentinel) this function would need a serializer, and a serializer is a
 * second place for the two schemas to drift. `prefs.test.ts` pins the exact JSON
 * of a set of each of the six keys, which is the check that keeps the shortcut
 * honest — it is the Go↔TS seam no compiler spans.
 *
 * v3's `folderVisibility` inherits the property without adding a case: it is a
 * flat `{[name]: "show"|"hide"|"showIfUnread"}` on both sides. Sending the
 * whole map replaces it, which is the server's documented whole-map patch; the
 * per-folder `folderVisibility/<name>` pointer form exists there too and is not
 * used here, because a folder name may contain a slash and would then need RFC
 * 6901 escaping on the way out — a second encoding, for no gain over sending
 * the map the settings table is already holding.
 */
export async function savePrefs(
  client: JmapClient,
  accountId: string,
  patch: Partial<Prefs>,
  signal?: AbortSignal,
): Promise<PrefsResult> {
  const response = await client.call(
    [
      [
        "Prefs/set",
        { accountId, update: { [PREFS_ID]: patch } },
        "p",
      ],
      // The updated object is read back in the SAME request, so the state the
      // caller stores is the state that includes this write. A second round
      // trip could interleave with another tab's save and store a cursor that
      // never described the data in hand.
      ["Prefs/get", { accountId, ids: [PREFS_ID] }, "g"],
    ],
    [CAP_CORE, CAP_PREFS],
    signal,
  );

  const setArgs = firstResponse(response.methodResponses, "p");
  const notUpdated = (setArgs.notUpdated ?? {}) as Record<string, unknown>;
  const refusal = notUpdated[PREFS_ID];
  if (refusal !== undefined) {
    const error = refusal as { type?: unknown; description?: unknown };
    const type = typeof error.type === "string" ? error.type : "invalidProperties";
    const description =
      typeof error.description === "string" ? `: ${error.description}` : "";
    throw new Error(`Prefs/set refused (${type})${description}`);
  }

  const getArgs = firstResponse(response.methodResponses, "g");
  const list = (getArgs.list ?? []) as readonly unknown[];
  return {
    prefs: parsePrefs(list[0]),
    state: typeof getArgs.state === "string" ? getArgs.state : "",
    servesV3: servesPrefsV3(list[0]),
  };
}

// ---------------------------------------------------------------------------
// density → the CSS custom properties the list actually draws with
// ---------------------------------------------------------------------------

/**
 * The pixel geometry of one density.
 *
 * `rowHeight` is the number the VIRTUALIZER divides by, so it is not a styling
 * detail: `computeWindow` derives every offset from it and the stylesheet must
 * draw rows at exactly that height or the list drifts away from its scrollbar
 * (the defect `rowHeight.test.ts` was written for). Density therefore changes
 * ONE number that both sides read, rather than a CSS value the maths never
 * hears about.
 *
 * The values: 72 px is what P2 shipped and stays "default". "comfortable" is
 * Gmail's roomier row; "compact" drops the preview line's breathing room to
 * fit roughly a third more mail on a laptop screen. Google publishes no pixel
 * values for its three names (canon §5 records the absence), so these are ours
 * and are chosen to be visibly distinct — a density setting whose steps are
 * 4 px apart is a control the user cannot tell is working.
 */
export interface DensityMetrics {
  /** The row height in pixels — the virtualizer's divisor. */
  readonly rowHeight: number;
  /** Horizontal padding inside a row. */
  readonly rowPaddingX: number;
  /** Gap between a row's cells. */
  readonly rowGap: number;
}

const DENSITY_METRICS: Readonly<Record<Density, DensityMetrics>> = {
  default: { rowHeight: 72, rowPaddingX: 16, rowGap: 12 },
  comfortable: { rowHeight: 88, rowPaddingX: 20, rowGap: 14 },
  compact: { rowHeight: 56, rowPaddingX: 12, rowGap: 8 },
};

/** The geometry for a density. */
export function densityMetrics(density: Density): DensityMetrics {
  return DENSITY_METRICS[density];
}

/** The row height a density draws at — the virtualizer's divisor. */
export function rowHeightFor(density: Density): number {
  return DENSITY_METRICS[density].rowHeight;
}

/**
 * The custom properties a density stamps on the document root.
 *
 * Returned as data rather than written here, so the applying component owns
 * the DOM write and a test can assert the mapping without a document.
 */
export function densityVariables(density: Density): Readonly<Record<string, string>> {
  const metrics = DENSITY_METRICS[density];
  return {
    "--row-height": `${metrics.rowHeight}px`,
    "--row-padding-x": `${metrics.rowPaddingX}px`,
    "--row-gap": `${metrics.rowGap}px`,
  };
}

// ---------------------------------------------------------------------------
// readingPane → what the shell actually renders
// ---------------------------------------------------------------------------

/**
 * What the mail shell shows, given the reading-pane setting and whether a
 * message is open.
 *
 * Extracted as a pure function because the alternative — three booleans
 * computed inline in `MailScreen` — is exactly the kind of logic that is
 * untestable without standing up auth, a router, a JMAP client and an
 * EventSource. The layout RULES are decidable from two values; the wiring is
 * not, and this file is where the codebase puts the decidable half.
 */
export interface PaneLayout {
  /** True when list and reader are on screen together. */
  readonly isSplit: boolean;
  /** True when the list must not be rendered at all. */
  readonly listHidden: boolean;
  /** True when the reader is on screen. */
  readonly showsReader: boolean;
  /** The layout the grid should use. */
  readonly mode: "list" | "right" | "bottom" | "full";
}

/**
 * Resolves the shell's layout (canon §2.4 — /9499937's three options).
 *
 * The rule that is easy to get wrong and is pinned by a test: in "none", the
 * list must be UNMOUNTED rather than hidden. A virtualized list in a
 * zero-height container measures a viewport of 0 and computes a window of
 * nothing, so returning to it would land on an empty list at a scroll offset
 * that no longer means anything.
 *
 * With no message open all three settings agree — there is only a list — which
 * is why "none" is not a permanently different shell but a different answer to
 * "what happens when you open something".
 */
export function paneLayout(readingPane: ReadingPane, isReading: boolean): PaneLayout {
  if (!isReading) {
    return { isSplit: false, listHidden: false, showsReader: false, mode: "list" };
  }
  if (readingPane === "none") {
    // The reader REPLACES the list; `u` and the close button bring it back.
    return { isSplit: false, listHidden: true, showsReader: true, mode: "full" };
  }
  return {
    isSplit: true,
    listHidden: false,
    showsReader: true,
    mode: readingPane === "bottom" ? "bottom" : "right",
  };
}

// ---------------------------------------------------------------------------
// inboxType → the server's sort
// ---------------------------------------------------------------------------

/** One JMAP `Comparator` (RFC 8620 §5.5). */
export interface SortComparator {
  readonly property: string;
  readonly keyword?: string;
  readonly isAscending?: boolean;
}

/**
 * The sort an inbox type asks the server for.
 *
 * # The polarity, read out of the server rather than guessed
 *
 * `internal/jmap/mail/query.go` accepts EXACTLY one shape beyond a single
 * comparator: the pair `[hasKeyword, receivedAt]` (`translateKeywordSort`).
 * Anything else is refused with `unsupportedSort`, so this function returns
 * either that pair or `undefined` — never a third shape the server would
 * reject and the UI would render as an empty inbox.
 *
 * Its polarity is the part that is easy to get backwards, so it is quoted
 * here from the source:
 *
 * ```go
 * // §4.4.2: the comparator sorts on "whether the Email has the keyword".
 * // isAscending:false therefore means "those that have it come first"
 * keywordFirst: !sort[0].ascending(),
 * ```
 *
 * and `ascending()` treats an ABSENT `isAscending` as true (RFC 8620 §5.5's
 * default). So:
 *
 *   - `isAscending: false` → messages that HAVE the keyword sort first;
 *   - `isAscending: true`  → messages that LACK the keyword sort first.
 *
 * Which inverts per inbox type, and this is the whole reason the two branches
 * below do not look symmetrical:
 *
 *   - **unread_first** wants messages WITHOUT `$seen` on top, so it asks for
 *     `isAscending: true` — the lacking side first.
 *   - **starred_first** wants messages WITH `$flagged` on top, so it asks for
 *     `isAscending: false` — the having side first.
 *
 * The second comparator is `receivedAt` descending in both, which is the
 * ordinary newest-first inbox applied WITHIN each of the two partitions.
 *
 * Returns `undefined` for "default", meaning "send no sort at all" — the
 * server's own default is already newest-first, and a redundant comparator
 * pair would put every plain inbox load through the keyword-partition path for
 * no visible difference.
 */
export function sortForInboxType(inboxType: InboxType): readonly SortComparator[] | undefined {
  switch (inboxType) {
    case "default":
      return undefined;
    case "unread_first":
      return [
        // $seen ASCENDING = "those WITHOUT $seen first" = unread on top.
        { property: "hasKeyword", keyword: "$seen", isAscending: true },
        { property: "receivedAt", isAscending: false },
      ];
    case "starred_first":
      return [
        // $flagged DESCENDING = "those WITH $flagged first" = starred on top.
        { property: "hasKeyword", keyword: "$flagged", isAscending: false },
        { property: "receivedAt", isAscending: false },
      ];
  }
}
