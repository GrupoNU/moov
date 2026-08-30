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
}

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
  };
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
