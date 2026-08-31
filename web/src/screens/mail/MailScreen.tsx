import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { JmapClient, withAccessToken, type BasicCredentials } from "../../api/jmap";
import { TokenManager } from "../../api/tokens";
import { connectPush } from "../../mail/push";
import { useAuth } from "../../auth/AuthProvider";
import { loadSession } from "../../auth/session";
import { useBranding } from "../../branding/BrandingProvider";
import { BrandMark } from "../../components/BrandMark";
import { useConfirm } from "../../components/useConfirm";
import { useTranslation } from "../../i18n/I18nProvider";
import {
  INITIAL_KEYBOARD_STATE,
  CHORD_TIMEOUT_MS,
  hasPendingChord,
  resolveShortcut,
  type KeyboardState,
  type SelectionScope,
  type ShortcutAction,
} from "../../keyboard/shortcuts";
import { usePrefs } from "../../mail/PrefsProvider";
import { densityVariables, paneLayout, sortForInboxType } from "../../mail/prefs";
import { loadSidebarCollapsed, saveSidebarCollapsed } from "../../mail/viewChrome";
import {
  nextPosition,
  PAGE_SIZE,
  previousPosition,
  type PageState,
} from "../../mail/paging";
import { connectionState, shouldRecycleStream } from "../../mail/connection";
import {
  EMPTY_ARRIVAL_STATE,
  mailboxNotifiable,
  newArrivals,
  notificationContent,
  shouldNotify,
  type ArrivalState,
} from "../../mail/notify";
import { useOffline } from "../../offline/OfflineProvider";
import { bootMode, type BootMode } from "../../offline/boot";
import { searchOffline } from "../../offline/search";
import {
  drainOutbox,
  newOutboxId,
  pendingCount,
  retryItem,
  showsOutbox,
  type OutboxItem,
  type SendAttempt,
} from "../../offline/outbox";
import {
  fetchMailboxes,
  fetchMessageDetail,
  queryEmails,
  MailApiError,
  type MailFilter,
} from "../../mail/api";
import { deleteIsPermanent, resolveToggle, type MessageAction } from "../../mail/actions";
import { encodeLabelKeyword } from "../../mail/labels";
import type { Label } from "../../mail/labelStore";
import { visibleLabels } from "../../mail/labelStore";
import { mailboxSegment, resolveMailbox } from "../../mail/mailboxes";
import { isSearchable, normalizeQuery, refusalFor } from "../../mail/search";
import { parseSearchQuery, type UnsupportedTerm } from "../../mail/searchQuery";
import { planFilter, type FilterPlan, type FilterProblem } from "../../mail/searchFilter";
import {
  loadRecentSearches,
  saveRecentSearches,
  withRecentSearch,
} from "../../mail/searchSuggestions";
import { fetchSnippets, snippetIndex, type SearchSnippet } from "../../mail/snippet";
import { SearchChips } from "./SearchChips";
import {
  actionTargets,
  EMPTY_SELECTION,
  idsFromHere,
  isAllSelected,
  pruneSelection,
  selectionAfterClick,
  selectionByScope,
  type SelectionState,
} from "../../mail/selection";
import {
  isUndoable,
  makeUndoEntry,
  UNDO_WINDOW_MS,
  type UndoEntry,
} from "../../mail/undo";
import { MAX_EMPTY_ROUNDS, shouldContinue, summarize, type EmptyRound } from "../../mail/emptyTrash";
import {
  cancelSubmission,
  destroyMessages,
  firstFailureMessage,
  hasFailures,
  sendDraft,
  sendScheduledNow,
  setIdentitySignature,
  type DraftSpec,
} from "../../mail/write";
import { makeChip } from "../../mail/addresses";
import { groupByThread, type ThreadGroup } from "../../mail/threading";
import { KEYWORD_FLAGGED, KEYWORD_SEEN, type Email, type Mailbox, type Thread } from "../../mail/types";
import { fetchIdentities, type Identity } from "../../mail/write";
import { encodeBasicCredentials } from "../../api/jmap";
import { useRouter } from "../../router/RouterProvider";
import {
  DEFAULT_ROUTE,
  DEFAULT_SETTINGS_TAB,
  openMessageId as routeMessageId,
  withMessage,
  type Route,
  type SettingsTab,
} from "../../router/routes";
import { parseComposeRequest, urlWithoutCompose } from "../../pwa/mailto";
import { useAddressIndex } from "./useAddressIndex";
import { useForwardAsAttachment } from "./useForwardAsAttachment";
import { Composer } from "../compose/Composer";
import type { ComposerAttachment } from "../compose/AttachmentList";
import {
  draftTo,
  forwardDraft,
  newDraft,
  replyDraft,
  resumeDraft,
  type ComposerDraft,
  type QuotingStrings,
} from "../compose/composerState";
import {
  fetchMutedThreadIds,
  fetchSnoozes,
  scheduleLimits,
  sessionHasTriage,
  setThreadsMuted,
  snoozeMailboxName,
  snoozeMessages,
  unsnoozeMessages,
} from "../../mail/triage";
import { fetchScheduled, type ScheduledSend } from "../../mail/scheduled";
import { ActionBar } from "./ActionBar";
import { ConnectionPill } from "./ConnectionPill";
import { OutboxView } from "./OutboxView";
import { ScheduledView } from "./ScheduledView";
import { SnoozeMenu } from "./SnoozeMenu";
import type { ConversationControls } from "./ConversationView";
import { LabelList } from "./LabelList";
import { useLabels } from "./useLabels";
import { useFilters } from "./useFilters";
import { VacationBanner } from "./VacationBanner";
import { blockAdvice, blockDraft } from "../../mail/blockedSenders";
import { MailboxList } from "./MailboxList";
import { mailboxLabel } from "./mailboxLabels";
import { ListToolbar } from "./ListToolbar";
import { MessageList } from "./MessageList";
import { ReadingPane } from "./ReadingPane";
import { SearchBar } from "./SearchBar";
import { TopBar } from "./TopBar";
import { QuickSettingsPanel } from "../settings/QuickSettingsPanel";
import { SettingsPage } from "../settings/SettingsPage";
import type { LabelsSectionProps } from "../settings/LabelsSection";
import type { FiltersSectionProps } from "../settings/FiltersSection";
import type { BlockedSectionProps } from "../settings/BlockedSection";
import type { ForwardingSectionProps } from "../settings/ForwardingSection";
import type { VacationSectionProps } from "../settings/VacationSection";
import type { QuotaRowProps } from "../settings/QuotaRow";
import type { MigrateResult } from "../../mail/migrateKeyword";
import { ShortcutsDialog } from "./ShortcutsDialog";
import { useMessageActions } from "./useMessageActions";
import { formatFullDate } from "../../mail/format";
import styles from "./MailScreen.module.css";

/**
 * The mail screen: the three-column shell and everything that coordinates it.
 *
 * This is the only stateful component in P2. The pure logic it orchestrates —
 * routing, mailbox ordering, thread grouping, the keyboard map, the windowing
 * maths, the debounce — all lives in tested modules, which is what keeps this
 * file about WIRING rather than about behaviour.
 */

/**
 * E3: how many rows one `SearchSnippet/get` asks for.
 *
 * Fifty rather than the page's full 200: a snippet is a `ts_headline` over a
 * message body, which is real work per row, and a person scanning a result list
 * reads the first screenful. The rest of the page renders its ordinary preview
 * — degraded in a way nobody notices, versus a request four times larger to
 * highlight rows most searches never scroll to.
 */
const SNIPPET_BATCH = 50;

export function MailScreen(): React.JSX.Element {
  const { state, signOut } = useAuth();
  const branding = useBranding();
  const { t, format, locale } = useTranslation();
  const { route, navigate, replace } = useRouter();
  const { prefs } = usePrefs();

  /*
   * E11: the app's own confirm, replacing `window.confirm` at every site on
   * this screen — permanent delete, empty trash, delete a label, discard a
   * queued message. The natives were unstyleable and inconsistent between
   * browsers, and are increasingly suppressed outright, which would turn a
   * destructive checkpoint into a silent yes.
   */
  const { confirm, dialog: confirmDialog } = useConfirm();

  const session = state.status === "authenticated" ? state.session : undefined;
  const accountId = session?.primaryAccounts["urn:ietf:params:jmap:mail"] ?? "";
  const username = state.status === "authenticated" ? state.username : "";

  /*
   * The client is rebuilt only when the credential changes, which is what makes
   * "which credential is this request using" answerable by construction (P1's
   * rule). The credential comes from the same storage AuthProvider validated at
   * sign-in — it is not re-prompted.
   */
  const client = useMemo<JmapClient | undefined>(() => {
    if (state.status !== "authenticated") return undefined;
    const stored: BasicCredentials | undefined = loadSession();
    if (stored === undefined) return undefined;
    const built = new JmapClient(stored);
    // Seed the session so apiUrl/downloadUrl come from the server's own
    // templates rather than from a guess.
    void built.fetchSession().catch(() => undefined);
    return built;
  }, [state.status]);

  /*
   * The Authorization header value, for the ONE request the JmapClient cannot
   * make on the caller's behalf: the attachment upload, which needs XHR for
   * its progress events (see `uploadBlob`). It is derived from the same stored
   * credential the client uses and never leaves this component tree.
   */
  const authorization = useMemo<string>(() => {
    if (state.status !== "authenticated") return "";
    const stored: BasicCredentials | undefined = loadSession();
    return stored === undefined ? "" : encodeBasicCredentials(stored);
  }, [state.status]);

  /**
   * An authenticated GET, for the ONE aux route that is neither a JMAP method
   * nor an upload: `GET /jmap/forwarding/verify?token=…` (E6).
   *
   * Built here from the same header value the upload uses rather than added to
   * `JmapClient`, because that class's narrowness is deliberate — one
   * credential, an enumerated set of endpoints — and widening it for a single
   * settings route would trade a property worth keeping for one call site.
   *
   * `credentials: "omit"` for the same reason the client does it: the header is
   * sent explicitly, so ambient cookies must not ride along.
   */
  const authedFetch = useMemo<((url: string) => Promise<Response>) | undefined>(() => {
    if (authorization === "") return undefined;
    return (url: string) =>
      fetch(url, {
        method: "GET",
        headers: { Authorization: authorization, Accept: "application/json" },
        credentials: "omit",
      });
  }, [authorization]);

  /*
   * E5: density, applied as CSS custom properties on the document ROOT.
   *
   * The root rather than a wrapper, because the reading pane, the composer and
   * every dialog are in the top layer or portaled out of this subtree — a
   * variable set on a div here would simply not reach them. The same three
   * values are what `MessageList` derives its virtualization divisor from
   * (`rowHeightFor`), so the maths and the paint cannot disagree.
   */
  useEffect(() => {
    if (typeof document === "undefined") return undefined;
    const root = document.documentElement;
    const variables = densityVariables(prefs.density);
    for (const [name, value] of Object.entries(variables)) {
      root.style.setProperty(name, value);
    }
    return () => {
      // Removed on unmount rather than left behind: the login screen after a
      // sign-out must not keep the previous account's density.
      for (const name of Object.keys(variables)) root.style.removeProperty(name);
    };
  }, [prefs.density]);

  /**
   * The mailbox list as the SERVER last gave it.
   *
   * E9 renamed this from `mailboxes` because the name now belongs to the
   * derived value below: everything downstream must read the list that is
   * actually on screen, which offline is the cached one. Splitting them here —
   * rather than at nineteen call sites — is what makes it impossible for one
   * consumer to read live data while the sidebar next to it draws the cache.
   */
  const [liveMailboxes, setMailboxes] = useState<readonly Mailbox[]>([]);
  const [mailboxError, setMailboxError] = useState<string | undefined>(undefined);
  const [isLoadingMailboxes, setLoadingMailboxes] = useState(true);

  /**
   * The message window as the SERVER last gave it.
   *
   * Renamed alongside `liveMailboxes` for the same reason: `emails` below is
   * the derived value every consumer reads, so that the list, the selection,
   * the keyboard and the actions cannot disagree about whether they are looking
   * at live mail or the cache.
   */
  const [liveEmails, setEmails] = useState<readonly Email[]>([]);
  /**
   * E1: the `Thread/get` results that rode the list's own batch.
   *
   * Non-empty only on the collapsed path, where they carry each row's TRUE
   * conversation size. On the uncollapsed path they stay empty and the rows
   * fall back to counting what is in the window — which `sizeIsExact` reports
   * honestly rather than hiding.
   */
  const [listThreads, setListThreads] = useState<readonly Thread[]>([]);
  const [isLoadingList, setLoadingList] = useState(true);
  const [listError, setListError] = useState<string | undefined>(undefined);
  const [truncated, setTruncated] = useState(false);
  const [refusal, setRefusal] = useState<string | undefined>(undefined);
  const [resultTotal, setResultTotal] = useState<number | undefined>(undefined);

  /**
   * B4: the page offset the list is showing (canon 07 §3).
   *
   * State rather than a route parameter, and the choice is deliberate. A page
   * number in the URL would make "/mail/inbox?p=3" a shareable link to a
   * position that means nothing to the recipient — their inbox's page 3 holds
   * different mail, and mine holds different mail an hour later. Gmail's own
   * pager is likewise not in its URL. What IS shareable stays shareable: the
   * folder, the search, and the open message.
   *
   * It resets whenever the LIST identity changes, which is what keeps a folder
   * switch from landing on page 3 of a folder with six messages — the same rule
   * and the same `listKey` the virtualizer resets its scroll on.
   */
  const [position, setPosition] = useState(0);

  const [selectedId, setSelectedId] = useState<string | undefined>(undefined);
  const [detail, setDetail] = useState<{ email?: Email; thread?: Thread }>({});
  const [isLoadingDetail, setLoadingDetail] = useState(false);
  const [detailError, setDetailError] = useState<string | undefined>(undefined);

  const [searchText, setSearchText] = useState(
    route.kind === "search" ? route.query : "",
  );
  const [helpOpen, setHelpOpen] = useState(false);
  const [toast, setToast] = useState<string | undefined>(undefined);

  /**
   * B3: settings is a ROUTE now, not a piece of local state (canon 07 §5).
   *
   * `settingsOpen` is gone with the `<dialog>` it governed. Everything that
   * used to set it navigates instead, which is what makes the surface
   * deep-linkable, bookmarkable and reachable with Back — the properties a
   * dialog could not have.
   *
   * `settingsTab` is `undefined` off the settings route, which is exactly the
   * condition the shell renders the mail list under. It is not a second copy of
   * the route: it is the route, read once.
   */
  const settingsTab = route.kind === "settings" ? route.tab : undefined;
  const inSettings = settingsTab !== undefined;

  /*
   * E12: the top bar's two pieces of shell state.
   *
   * The rail's collapse is seeded from localStorage in a LAZY initial state
   * rather than an effect, for the same reason the recent-search history is:
   * an effect would paint one frame of the expanded rail and then snap it
   * closed, which reads as a layout glitch rather than as a restored choice.
   * It is device-local by design — see `mail/viewChrome.ts` on why a pixel
   * width and a rail state must not roam between a laptop and a monitor.
   */
  const [sidebarCollapsed, setSidebarCollapsed] = useState<boolean>(() =>
    loadSidebarCollapsed(),
  );
  const toggleSidebar = useCallback((): void => {
    setSidebarCollapsed((collapsed) => {
      const next = !collapsed;
      saveSidebarCollapsed(next);
      return next;
    });
  }, []);

  /**
   * B2: the quick-settings dock.
   *
   * Session state, not persisted: it is a transient surface the user opens to
   * change one thing, and a panel that reopened itself on every load would
   * permanently narrow the list for someone who forgot to close it once.
   */
  const [quickSettingsOpen, setQuickSettingsOpen] = useState(false);

  /*
   * E3: the recent-search history and the result snippets.
   *
   * The history is seeded from localStorage ONCE (a lazy initial state, not an
   * effect) so the very first focus of the box already offers it — an effect
   * would render an empty dropdown for one frame, which reads as "there is no
   * history" to someone who has one.
   */
  const [recentSearches, setRecentSearches] = useState<readonly string[]>(() =>
    loadRecentSearches(),
  );
  const [snippets, setSnippets] = useState<readonly SearchSnippet[]>([]);

  // --- P3 state ------------------------------------------------------------
  const [selection, setSelection] = useState<SelectionState>(EMPTY_SELECTION);
  const [composerDraft, setComposerDraft] = useState<ComposerDraft | undefined>(undefined);
  /**
   * E7: attachments a composer should open carrying — the forwarded `.eml`s.
   *
   * Separate from the draft rather than a field on it because a `ComposerDraft`
   * is built by pure functions in `composerState.ts` that know nothing about
   * blobs or uploads, and threading an already-uploaded attachment through them
   * would put an async concern inside a synchronous constructor.
   */
  const [pendingAttachments, setPendingAttachments] = useState<
    readonly ComposerAttachment[] | undefined
  >(undefined);
  const [identity, setIdentity] = useState<Identity | undefined>(undefined);
  /** A refetch trigger: bumped after a write so the list re-reads the truth. */
  const [refreshToken, setRefreshToken] = useState(0);

  /*
   * E2: the single undo slot (Gmail's `z`).
   *
   * ONE entry, not a stack — Gmail's `z` is "undo last action", and offering to
   * undo an action whose toast is long gone (and whose messages may have moved
   * twice since) is worse than not offering it. The entry carries both halves:
   * inverse patches for the instant repaint, and an inverse ACTION that is
   * genuinely re-issued to the server. See `mail/undo.ts` on why the patch
   * alone would be a lie.
   */
  const [undoEntry, setUndoEntry] = useState<UndoEntry | undefined>(undefined);
  const undoCounter = useRef(0);

  /**
   * E4: the muted conversations, cached as the server's own header says to.
   *
   * Declared HERE, well above the rest of the E4 block, because `is:muted`
   * narrows the row list — and the row list is computed near the top, before
   * the selection, the keyboard and every action that reads it. The fetch that
   * fills it lives with the rest of E4; only the state has to be this early.
   */
  const [mutedThreadIds, setMutedThreadIds] = useState<ReadonlySet<string>>(
    () => new Set<string>(),
  );
  const [isEmptyingTrash, setEmptyingTrash] = useState(false);

  const searchInputRef = useRef<HTMLInputElement | null>(null);

  // --- E9: offline, connection honesty, notifications ----------------------

  const offline = useOffline();
  /*
   * The three write-through helpers, destructured.
   *
   * Each is a `useCallback` in the provider, so it is stable while the cache
   * is — but `offline.cacheHeaders` as a dependency is a MEMBER expression, and
   * the exhaustive-deps rule cannot see through one: it asks for the whole
   * `offline` object instead, which changes whenever the outbox does. Depending
   * on that would refetch the mailbox list and the message window every time a
   * queued message changed state. Destructuring names exactly what these
   * effects use.
   */
  const { cacheMailboxes, cacheHeaders, cacheBody } = offline;

  /**
   * E7: one page of Sent, for the address index's first-run scan.
   *
   * Defined here rather than in the hook because this is where the client and
   * the request repertoire live. `collapseThreads` is deliberately NOT set: the
   * scan wants every message's recipients, and one row per conversation would
   * hide the addresses of every reply inside a thread — which is most of them.
   */
  const fetchSentPage = useCallback(
    async (
      mailboxId: string,
      position: number,
      limit: number,
    ): Promise<readonly Email[]> => {
      if (client === undefined) return [];
      const page = await queryEmails(
        client,
        accountId,
        { kind: "mailbox", mailboxId },
        { limit, position },
      );
      return page.emails;
    },
    [client, accountId],
  );

  /**
   * E7: the address index's write-through, published through a ref.
   *
   * The two effects that feed it — the message window and the reader — are
   * declared ABOVE the index itself, which cannot move up because it needs the
   * Sent mailbox and therefore `roleMailboxId`. A ref reassigned on every
   * render states "read the latest" honestly, and keeps this out of those
   * effects' dependency arrays: `record` changes identity whenever the store or
   * the opt-out does, and naming it would refetch the whole message window on a
   * settings toggle.
   *
   * This is the same device `advanceTargetRef` uses a few hundred lines below,
   * for the same declaration-order reason.
   */
  const recordAddressesRef = useRef<(emails: readonly Email[]) => void>(() => undefined);
  const recordAddresses = useCallback((emails: readonly Email[]): void => {
    recordAddressesRef.current(emails);
  }, []);

  /**
   * True once a real request has failed with a network error.
   *
   * This is the signal that promotes the app to cached rendering when
   * `navigator.onLine` lies (a captive portal reports online). It is CLEARED by
   * any successful fetch, so one blip does not pin the app in offline mode.
   */
  const [requestFailed, setRequestFailed] = useState(false);

  /** True between the stream dying and its next confirmed state event. */
  const [streamDead, setStreamDead] = useState(false);
  /** When the stream last proved it was alive — the staleness watchdog's input. */
  const lastStreamEventRef = useRef(0);

  /**
   * The arrival detector's memory, in a ref rather than state.
   *
   * It must not trigger a render: it changes on every refresh, and rendering
   * because we noticed nothing new would be a render for nothing. It is also
   * read inside the same effect that writes it, which state would make stale.
   */
  const arrivalRef = useRef<ArrivalState>(EMPTY_ARRIVAL_STATE);

  /** Cached mail rendered when the network is gone (mode "cached"). */
  const [cachedEmails, setCachedEmails] = useState<readonly Email[]>([]);
  const [cachedMailboxes, setCachedMailboxes] = useState<readonly Mailbox[]>([]);
  const [cachedDetail, setCachedDetail] = useState<Email | undefined>(undefined);
  /** True when the reader is showing a message the cache does not have. */
  const [detailUncached, setDetailUncached] = useState(false);

  // --- scoped tokens + real-time push (closes P2 gap 4) ---------------------

  /*
   * The scoped tokens the header-less browser primitives need: `push` opens
   * the EventSource, `blob` signs attachment hrefs. TokenManager mints them
   * on session start, refreshes ahead of expiry, and — in this effect's
   * cleanup, which runs on sign-out while the client's credential is still
   * in memory — revokes them server-side. They are NOT credentials: the
   * server refuses them everywhere except the one route each scope names.
   */
  const [pushToken, setPushToken] = useState<string | undefined>(undefined);
  const [blobToken, setBlobToken] = useState<string | undefined>(undefined);
  const tokenManagerRef = useRef<TokenManager | undefined>(undefined);

  useEffect(() => {
    if (client === undefined) return undefined;
    const manager = new TokenManager(client, {
      onTokens: (tokens) => {
        setPushToken(tokens.push?.token);
        setBlobToken(tokens.blob?.token);
      },
    });
    tokenManagerRef.current = manager;
    void manager.start();
    return () => {
      tokenManagerRef.current = undefined;
      setPushToken(undefined);
      setBlobToken(undefined);
      void manager.stop();
    };
  }, [client]);

  /*
   * One push connection per token: when the manager refreshes the token this
   * effect re-runs, closing the old stream and opening a new one — so a
   * stream never outlives the token that authenticated it, and a server
   * restart (which invalidates every token at once) heals through the same
   * path. A burst of state events collapses into one refetch via a short
   * trailing debounce; the refetch itself is the ordinary refresh cycle, so
   * pushed changes and manual refreshes render through identical code.
   */
  useEffect(() => {
    if (client === undefined || pushToken === undefined) return undefined;
    if (typeof EventSource === "undefined") return undefined;
    let debounce: ReturnType<typeof setTimeout> | undefined;
    const handle = connectPush({
      url: withAccessToken(client.eventSourceUrlFor(), pushToken),
      onStateChange: () => {
        /*
         * E9: a state event is the only proof the stream is alive. It clears
         * the "dead" flag — so the pill goes away on its own, without a
         * "connected" signal the server does not send — and stamps the
         * watchdog's clock.
         */
        lastStreamEventRef.current = Date.now();
        setStreamDead(false);
        if (debounce !== undefined) clearTimeout(debounce);
        debounce = setTimeout(() => {
          setRefreshToken((token) => token + 1);
        }, 300);
      },
      onDead: () => {
        // The browser gave up on the stream — with this server that means
        // the token died (restart or revocation). A fresh mint reconnects.
        // E9: and the user is told, rather than the inbox silently freezing.
        setStreamDead(true);
        void tokenManagerRef.current?.refreshNow();
      },
    });
    /*
     * E9: the stream is alive from the moment it opens, as far as the watchdog
     * is concerned. Without this stamp a freshly opened connection looks "never
     * heard from", and the first `visibilitychange` would recycle a stream that
     * is perfectly healthy.
     */
    lastStreamEventRef.current = Date.now();
    setStreamDead(false);
    return () => {
      if (debounce !== undefined) clearTimeout(debounce);
      handle.close();
    };
  }, [client, pushToken]);

  /*
   * E9 / G3: `recycleStaleSSE` on `visibilitychange`.
   *
   * The case a timer cannot cover: an iOS home-screen PWA freezes its timers
   * when backgrounded, so a watchdog implemented as `setInterval` sleeps
   * exactly when the connection is being torn down. `visibilitychange` is
   * guaranteed to fire on the way back, which makes it the moment to ask
   * whether the stream is still worth trusting.
   *
   * The DECISION is `shouldRecycleStream` (pure, tested); this effect is the
   * wiring around it. Recycling means minting a fresh token — which re-runs the
   * effect above and therefore reopens the stream — and refetching, because
   * whatever arrived while the tab slept is not in the list.
   */
  useEffect(() => {
    if (typeof document === "undefined") return undefined;
    const onVisibility = (): void => {
      const recycle = shouldRecycleStream({
        visibility: document.visibilityState === "visible" ? "visible" : "hidden",
        streamDead,
        lastEventAt: lastStreamEventRef.current,
        now: Date.now(),
        online: typeof navigator === "undefined" || navigator.onLine,
      });
      if (!recycle) return;
      void tokenManagerRef.current?.refreshNow();
      setRefreshToken((token) => token + 1);
    };
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [streamDead]);

  /*
   * E9: coming back online forces a refresh.
   *
   * The stream's own retry may take a while to notice, and the list on screen
   * is by definition stale — it was rendered from cache, or frozen at the
   * moment the network went. Refetching immediately is what makes reconnection
   * feel instant rather than eventual.
   */
  useEffect(() => {
    if (!offline.isOnline) return;
    setRequestFailed(false);
    setRefreshToken((token) => token + 1);
  }, [offline.isOnline]);

  /*
   * E9: what the screen actually renders.
   *
   * ONE switch, here, rather than a conditional at every consumer. The rule is
   * simple and worth stating because getting it wrong is invisible: while the
   * live path works these are exactly the server's values, and the moment it
   * does not, EVERYTHING downstream — the sidebar, the list, the grouping, the
   * selection, the keyboard — moves to the cache together. A screen that drew
   * a cached sidebar next to a live list would be lying about one of them.
   *
   * `offlineMailboxes`/`offlineEmails` are the cached copies loaded further
   * down; the mode that chooses between them is computed there too, so this
   * pair is deliberately written in terms of the same two raw inputs
   * (`isOnline`, `requestFailed`) rather than reading a value declared later.
   */
  const useCache = !offline.isOnline || requestFailed;
  const mailboxes = useCache && cachedMailboxes.length > 0 ? cachedMailboxes : liveMailboxes;
  const emails = useCache && cachedMailboxes.length > 0 ? cachedEmails : liveEmails;

  // --- mailboxes -----------------------------------------------------------

  useEffect(() => {
    if (client === undefined || accountId === "") return undefined;
    const controller = new AbortController();
    setLoadingMailboxes(true);
    void (async () => {
      try {
        const list = await fetchMailboxes(client, accountId, controller.signal);
        if (!controller.signal.aborted) {
          setMailboxes(list);
          setMailboxError(undefined);
          // E9: browsing IS the sync. The sidebar the user just saw is the
          // sidebar they get offline, with no background job to disagree with.
          cacheMailboxes(list);
          setRequestFailed(false);
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          setMailboxError(error instanceof Error ? error.message : String(error));
          // E9: a failed fetch is what promotes us to cached rendering when
          // `navigator.onLine` is lying (a captive portal reports online).
          setRequestFailed(true);
        }
      } finally {
        if (!controller.signal.aborted) setLoadingMailboxes(false);
      }
    })();
    return () => {
      controller.abort();
    };
    /*
     * refreshToken is here on purpose: a pushed StateChange (or a completed
     * write) bumps it, and the sidebar's unread counts are exactly what push
     * exists to keep live.
     */
  }, [client, accountId, refreshToken, cacheMailboxes]);

  /** The mailbox the route names, once the list has loaded. */
  const activeMailbox = useMemo<Mailbox | undefined>(() => {
    if (route.kind !== "mailbox") return undefined;
    return resolveMailbox(mailboxes, route.mailboxId);
  }, [route, mailboxes]);

  /*
   * Canonicalise the URL once mailboxes are known: `/mail/mc` becomes
   * `/mail/inbox` when mc has the inbox role. `replace`, not `navigate` — the
   * user did not choose this, so it must not cost them a Back press.
   */
  useEffect(() => {
    if (route.kind !== "mailbox" || activeMailbox === undefined) return;
    const canonical = mailboxSegment(activeMailbox);
    if (canonical !== route.mailboxId) {
      replace({ ...route, mailboxId: canonical });
    }
  }, [route, activeMailbox, replace]);

  // --- the message list ----------------------------------------------------

  /**
   * E3: the search plan — the parsed query, its filter, and everything it
   * could NOT express.
   *
   * Computed once here rather than in each consumer, because the banner, the
   * chips and the request all have to be talking about the same parse. The
   * mailboxes are an input: `in:<name>` resolves against them, so the plan is
   * recomputed when they arrive.
   */
  const searchPlan = useMemo<FilterPlan | undefined>(() => {
    if (route.kind !== "search") return undefined;
    return planFilter(parseSearchQuery(route.query), mailboxes);
  }, [route, mailboxes]);

  /** What the current route asks the server for. */
  const filter = useMemo<MailFilter | undefined>(() => {
    if (route.kind === "search") {
      const query = normalizeQuery(route.query);
      if (!isSearchable(query)) return undefined;
      /*
       * E3: the operator grammar owns the filter now.
       *
       * `planFilter` returns `undefined` when the query cannot be answered as
       * typed — a refused negation, an unknown folder, too many OR branches —
       * and the banner below explains which term was at fault. Falling back to
       * a plain text search here would be the silent mis-answer the whole
       * epic exists to avoid: it would search for the words of an operator the
       * user meant as a filter.
       */
      if (searchPlan?.filter === undefined) return undefined;
      return { kind: "query", filter: searchPlan.filter };
    }
    /*
     * E8: a label view is a `hasKeyword` filter across the WHOLE account, not
     * scoped to a folder — a label is cross-cutting by definition, and scoping
     * it would hide exactly the messages the user filed away, which is the one
     * thing a label is for.
     */
    if (route.kind === "label") {
      return { kind: "label", keyword: encodeLabelKeyword(route.name) };
    }
    if (activeMailbox === undefined) return undefined;
    return { kind: "mailbox", mailboxId: activeMailbox.id };
  }, [route, activeMailbox, searchPlan]);

  /**
   * E5: the inbox-type sort.
   *
   * Applied ONLY to the INBOX, which is what the setting is named after and
   * what Gmail's own six inbox types govern (canon §2.4). Sorting Sent by
   * "unread first" would be meaningless, and sorting a SEARCH by it would
   * override the relevance the user asked for.
   *
   * The comparator pair is built by `sortForInboxType`, which documents the
   * polarity read out of the server's `translateKeywordSort`. "default"
   * yields undefined — no sort argument at all, so a plain inbox load never
   * pays for a partition the server would compute identically.
   */
  const sort = useMemo(() => {
    if (route.kind !== "mailbox" || activeMailbox?.role !== "inbox") return undefined;
    return sortForInboxType(prefs.inboxType) as
      | readonly Record<string, unknown>[]
      | undefined;
  }, [route.kind, activeMailbox?.role, prefs.inboxType]);

  /**
   * E1: whether the LIST asks the server to collapse threads (RFC 8621 §4.4.3).
   *
   * Tied to the same `conversationView` preference that gates the reader, which
   * is what makes the setting mean one thing: on, mail is organised by
   * conversation everywhere; off, by message everywhere.
   *
   * Server-side collapse is categorically better than the client-side grouping
   * it replaces, and for a reason worth naming: the client can only group what
   * is inside the window it fetched, so a row's count meant "in this window"
   * rather than "in this conversation". The server collapses in the database
   * over the whole folder, and `Thread/get` rides the same batch with the real
   * sizes. `groupByThread` still runs on the result — a collapsed list is
   * already one message per thread, so it becomes an identity pass that
   * attaches the reported sizes.
   */
  const collapseThreads = prefs.conversationView;

  /** Identifies the list, so the virtualizer resets scroll only on a real change. */
  const listKey =
    route.kind === "search"
      ? `search:${normalizeQuery(route.query)}:${collapseThreads ? "c" : "m"}`
      : route.kind === "label"
      ? `label:${route.name}:${collapseThreads ? "c" : "m"}`
      : // The inbox type is part of the list's identity: changing it reorders
        // every row, so the scroll position from the previous order is
        // meaningless and must reset rather than land the user mid-list.
        // So is the collapse mode: turning conversation view on or off changes
        // what a row IS, and an offset into the old list means nothing in the
        // new one.
        `mailbox:${activeMailbox?.id ?? ""}:${prefs.inboxType}:${collapseThreads ? "c" : "m"}`;

  /*
   * B4: a NEW list starts at page one.
   *
   * `listKey` is exactly the identity the virtualizer resets its scroll on, and
   * the pager has to follow it for the same reason: an offset into the previous
   * list means nothing in this one. Without this, switching from a 15,000-row
   * inbox at page 40 to a folder with six messages would land on an empty page
   * that looks like the folder is empty.
   *
   * A layout effect, not a plain one, so the reset happens before paint —
   * otherwise one frame renders the new folder at the old offset.
   */
  useLayoutEffect(() => {
    setPosition(0);
  }, [listKey]);

  useEffect(() => {
    if (client === undefined || accountId === "" || filter === undefined) {
      if (filter === undefined && route.kind === "search") {
        // A query too short to send is not an error: show nothing, quietly.
        setEmails([]);
        setLoadingList(false);
      }
      return undefined;
    }
    const controller = new AbortController();
    setLoadingList(true);
    setRefusal(undefined);
    setListError(undefined);

    void (async () => {
      try {
        let page;
        try {
          page = await queryEmails(client, accountId, filter, {
            signal: controller.signal,
            ...(sort !== undefined ? { sort } : {}),
            collapseThreads,
            // B4: one PAGE, not the servers whole 200-row window. See
            // mail/paging.ts on why 50 and why it is not a preference.
            limit: PAGE_SIZE,
            position,
          });
        } catch (error) {
          /*
           * E1: a server that will not collapse THIS query still has a list to
           * give, so ask again without the collapse rather than showing
           * nothing.
           *
           * The server documents exactly one refusable combination (collapse
           * with the `relevance` sort) and this client never sends it — so in
           * practice this path is for a server OLDER than the collapse
           * support, which answers `unsupportedFilter`/`unsupportedSort` for
           * an argument it does not know. That is the whole capability probe:
           * one retry, driven by the server's own answer, instead of a version
           * check that would have to be kept in step by hand.
           *
           * The fallback list is grouped client-side, so its counts become
           * window-bounded — `sizeIsExact` carries that difference rather than
           * the UI pretending the two paths are the same.
           */
          const refusedCollapse =
            collapseThreads &&
            error instanceof MailApiError &&
            error.methodError !== undefined &&
            refusalFor(error.methodError.type, error.methodError.description) !== undefined;
          if (!refusedCollapse || controller.signal.aborted) throw error;
          page = await queryEmails(client, accountId, filter, {
            signal: controller.signal,
            ...(sort !== undefined ? { sort } : {}),
            limit: PAGE_SIZE,
            position,
          });
        }
        if (controller.signal.aborted) return;
        setEmails(page.emails);
        setListThreads(page.threads);
        setTruncated(page.truncated);
        setResultTotal(page.total);
        setRequestFailed(false);
        /*
         * E9 write-through: the window the user is looking at becomes the
         * window they get offline. Only a MAILBOX view caches — a search result
         * is a computed set, not a folder's contents, and storing it under a
         * mailbox id would make the cached "inbox" whatever the user last
         * searched for.
         */
        if (filter.kind === "mailbox") {
          cacheHeaders(filter.mailboxId, page.emails);
        }
        /*
         * E7 write-through: every message the app loads feeds the address
         * index, exactly as the header cache above is fed. Unlike the cache
         * this runs for SEARCH results too — a message found by searching is
         * still a message whose correspondents are real, and the index is not
         * keyed by folder so there is nothing to mis-file.
         */
        recordAddresses(page.emails);
      } catch (error) {
        if (controller.signal.aborted) return;
        setRequestFailed(true);
        // An unsupportedFilter is a REFUSAL, not a failure: the server is
        // telling us its repertoire cannot answer this shape. Rendering it as
        // an empty list would be a lie, so it gets its own explanation.
        if (error instanceof MailApiError && error.methodError !== undefined) {
          const declined = refusalFor(
            error.methodError.type,
            error.methodError.description,
          );
          if (declined !== undefined) {
            setEmails([]);
            setListThreads([]);
            setRefusal(error.methodError.description ?? t("search.unsupportedBody"));
            return;
          }
        }
        setEmails([]);
        setListThreads([]);
        setListError(error instanceof Error ? error.message : String(error));
      } finally {
        if (!controller.signal.aborted) setLoadingList(false);
      }
    })();

    return () => {
      controller.abort();
    };
  }, [
    client,
    accountId,
    filter,
    route.kind,
    t,
    refreshToken,
    sort,
    collapseThreads,
    // B4: the pager's offset is an INPUT to the query, so moving it refetches
    // exactly as changing the filter does — one code path for both.
    position,
    cacheHeaders,
    recordAddresses,
  ]);

  // --- P3: identity (the signature and the sending address) ----------------

  useEffect(() => {
    if (client === undefined || accountId === "") return undefined;
    const controller = new AbortController();
    void (async () => {
      try {
        const identities = await fetchIdentities(client, accountId, controller.signal);
        if (!controller.signal.aborted) setIdentity(identities[0]);
      } catch {
        // A missing identity does not break reading; it disables SENDING, and
        // the composer says so rather than the whole screen failing.
        if (!controller.signal.aborted) setIdentity(undefined);
      }
    })();
    return () => {
      controller.abort();
    };
  }, [client, accountId]);

  /**
   * E5: saves the signature from the settings sheet.
   *
   * It lives here rather than in the sheet because this is where the JMAP
   * client is, and the sheet is deliberately client-free — everything else it
   * writes goes through the prefs context. On success the local identity is
   * updated from the value we sent rather than refetched: the composer reads
   * `identity.textSignature` and a stale one would append the previous
   * signature to the very next reply.
   */
  const saveSignature = useCallback(
    async (textSignature: string): Promise<boolean> => {
      if (client === undefined || accountId === "" || identity === undefined) return false;
      try {
        const outcome = await setIdentitySignature(
          client,
          accountId,
          identity.id,
          textSignature,
        );
        if (hasFailures(outcome)) {
          setToast(`${t("settings.signature.failed")}: ${firstFailureMessage(outcome) ?? ""}`.trim());
          return false;
        }
        setIdentity((current) =>
          current === undefined ? current : { ...current, textSignature },
        );
        return true;
      } catch (error) {
        setToast(
          `${t("settings.signature.failed")}: ${error instanceof Error ? error.message : String(error)}`,
        );
        return false;
      }
    },
    [client, accountId, identity, t],
  );

  // --- P3: optimistic actions ----------------------------------------------

  const actions = useMessageActions({
    client,
    accountId,
    currentMailboxId: activeMailbox?.id,
  });

  /*
   * The list the UI renders: the server's data with the optimistic overlay on
   * top. Every consumer below — the grouping, the selection, the keyboard —
   * reads THIS, so an optimistic change is visible everywhere at once and
   * there is no second source of truth to keep in step.
   */
  const projected = useMemo(() => actions.project(emails), [actions, emails]);

  /*
   * E1: the rows.
   *
   * `groupByThread` runs on BOTH paths and that is deliberate. On the collapsed
   * path the server already returned one message per conversation, so grouping
   * is an identity pass whose real job is attaching the `Thread/get` sizes; on
   * the uncollapsed path it does the grouping itself and the sizes are absent,
   * which it reports through `sizeIsExact`. One row shape, one code path
   * downstream — the selection, the keyboard and the actions never have to ask
   * which query produced the list.
   */
  const serverGroups = useMemo(
    () => groupByThread(projected, listThreads),
    [projected, listThreads],
  );

  /**
   * E4: `is:muted`, applied to the rows that came back.
   *
   * This is the one narrowing in the app that is NOT a filter condition, and
   * the reason is the server's, stated in `internal/jmap/mail/triage.go`: a
   * vendor `inMutedThread` "would make every mail search join against the mute
   * table for a predicate whose whole result set is, in practice, a few dozen
   * ids a client can cache". So the client caches the ids and narrows here.
   *
   * It replaces `groups` rather than sitting beside it, deliberately. Every
   * consumer downstream — the list, the selection, `j`/`k`, `x`, the action
   * targets — has to see the SAME rows, or the keyboard would walk over rows
   * that are not on screen and a bulk action would touch messages the user
   * cannot see. A second name would be a second answer to "what is in the
   * list", and one of them would eventually be wrong.
   *
   * The honest consequence, which `ListNotice` states rather than hiding: this
   * narrows the PAGE, not the search. A muted conversation outside the server's
   * 200-row window is not reached by `is:muted`.
   */
  const mutedFilter = searchPlan?.mutedOnly;
  const groups = useMemo((): readonly ThreadGroup[] => {
    if (mutedFilter === undefined) return serverGroups;
    return serverGroups.filter((group) => {
      const threadId = group.latest.threadId;
      const isMuted = threadId !== undefined && mutedThreadIds.has(threadId);
      return isMuted === mutedFilter;
    });
  }, [serverGroups, mutedFilter, mutedThreadIds]);

  /*
   * E3: the snippets for the rows on screen (RFC 8621 §5).
   *
   * # Why this asks for the whole fetched page, not the visible viewport
   *
   * The plan says "only for rows on screen", and the intent behind it is the
   * one that matters: never ask the server to headline the WHOLE result set to
   * paint a few rows. The page the list holds is already bounded by the
   * server's 200-row window, and the batch is capped below that again.
   *
   * Scoping to the scroll viewport instead would fire a fresh request on every
   * scroll — dozens of round trips through a list, each re-running the search
   * server-side to compute headlines — which costs far more than one bounded
   * batch and makes highlights flicker in as the user scrolls. One request per
   * result page is both cheaper and steadier.
   *
   * A failure here is invisible by design: `fetchSnippets` returns empty, and
   * the list renders its ordinary subject and preview.
   */
  useEffect(() => {
    if (
      client === undefined ||
      accountId === "" ||
      route.kind !== "search" ||
      searchPlan?.filter === undefined ||
      emails.length === 0
    ) {
      setSnippets([]);
      return undefined;
    }
    const controller = new AbortController();
    // The cap: a page is at most 200 rows, and headlining 200 message bodies to
    // paint a list is the same category of mistake as fetching their bodies.
    const ids = emails.slice(0, SNIPPET_BATCH).map((email) => email.id);
    void (async () => {
      const found = await fetchSnippets(
        client,
        accountId,
        searchPlan.filter ?? null,
        ids,
        controller.signal,
      ).catch(() => []);
      if (!controller.signal.aborted) setSnippets(found);
    })();
    return () => {
      controller.abort();
    };
  }, [client, accountId, route.kind, searchPlan, emails]);

  /** Snippets by id, so a row can find its own without a scan. */
  const snippetsById = useMemo(() => snippetIndex(snippets), [snippets]);

  /**
   * B4: what the pager describes.
   *
   * `shown` counts the ROWS on screen, not the messages fetched, and the two
   * differ under conversation view: a collapsed query returns one message per
   * thread, so they agree, but the client-grouped FALLBACK path (an older
   * server that refused `collapseThreads`) collapses several messages into one
   * row. Counting messages there would say "1–50" over a list showing 31 rows.
   *
   * `total` is the server's, passed through untouched — exact when it gave one
   * and undefined when it declined. `mail/paging.ts` documents why those are
   * two different true sentences rather than a value and a fallback.
   */
  const pageState = useMemo<PageState>(
    () => ({ position, shown: groups.length, total: resultTotal }),
    [position, groups.length, resultTotal],
  );

  /** Refetches the list from the server after a write. */
  const refresh = useCallback((): void => {
    actions.reset();
    setRefreshToken((token) => token + 1);
  }, [actions]);

  /*
   * E8: the labels.
   *
   * It reads `projected` rather than `emails` so a just-applied label is
   * discovered from the OPTIMISTIC state — otherwise creating and applying a
   * label in one gesture would leave the sidebar row missing until the refetch
   * landed, which reads as the label not having worked.
   */
  const labelsApi = useLabels({
    client,
    accountId,
    emails: projected,
    onChanged: refresh,
  });

  // Keep the selection valid as the list changes underneath it.
  useEffect(() => {
    if (groups.length === 0) {
      setSelectedId(undefined);
      return;
    }
    if (selectedId === undefined || !groups.some((group) => group.id === selectedId)) {
      setSelectedId(groups[0]?.id);
    }
  }, [groups, selectedId]);

  // --- the open message ----------------------------------------------------

  // E9: the Outbox route carries no message — its rows are queue entries, not
  // server objects. `routeMessageId` answers that uniformly for every route.
  const openMessageId = routeMessageId(route);

  useEffect(() => {
    if (client === undefined || accountId === "" || openMessageId === undefined) {
      setDetail({});
      setDetailError(undefined);
      return undefined;
    }
    const controller = new AbortController();
    setLoadingDetail(true);
    setDetailError(undefined);
    void (async () => {
      try {
        const result = await fetchMessageDetail(client, accountId, openMessageId, {
          signal: controller.signal,
        });
        if (!controller.signal.aborted) {
          setDetail({
            ...(result.email !== undefined ? { email: result.email } : {}),
            ...(result.thread !== undefined ? { thread: result.thread } : {}),
          });
          /*
           * E9: a body is cached because the user OPENED it, never
           * speculatively. Pre-fetching bodies would multiply every sync by the
           * average message size for mail nobody may ever read — a cost paid on
           * a phone's data plan for a guess.
           */
          if (result.email !== undefined) {
            cacheBody(result.email);
            // E7: an opened message is the richest sighting there is — it
            // carries the full recipient list, where a list row may not.
            recordAddresses([result.email]);
          }
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          setDetailError(error instanceof Error ? error.message : String(error));
        }
      } finally {
        if (!controller.signal.aborted) setLoadingDetail(false);
      }
    })();
    return () => {
      controller.abort();
    };
  }, [client, accountId, openMessageId, cacheBody, recordAddresses]);

  // --- E9: rendering from the cache -----------------------------------------

  /**
   * What the shell is showing right now: live data, the cache, or an honest
   * empty state.
   *
   * `hasCache` is answered by whether the cached mailbox list is non-empty
   * rather than by asking the store, because that is the value the sidebar
   * actually renders — a mode that claimed a cache the sidebar could not draw
   * would be a spinner with extra steps.
   */
  const mode: BootMode = bootMode({
    online: offline.isOnline,
    requestFailed,
    hasCache: cachedMailboxes.length > 0,
  });
  const isOfflineMode = mode !== "online";

  /*
   * E9 / GC-2: fire a desktop notification for genuinely new mail.
   *
   * Runs on the LIVE window only. Rendering from the cache must never notify:
   * the cached list is by definition mail that already arrived, and announcing
   * it on reconnection would toast the user about messages they read yesterday.
   *
   * Both decisions are pure and tested (`mail/notify.ts`); what is left here is
   * the construction and the click handler, which is all that genuinely needs a
   * browser.
   */
  useEffect(() => {
    if (typeof Notification === "undefined" || typeof document === "undefined") return;
    // Only the inbox-shaped views notify, and only from live data.
    if (useCache || route.kind !== "mailbox") return;

    const { arrivals, state } = newArrivals(liveEmails, arrivalRef.current);
    arrivalRef.current = state;
    if (arrivals.length === 0) return;

    const allowed = shouldNotify({
      mode: prefs.notifications,
      permission: Notification.permission,
      attention: {
        hasFocus: document.hasFocus(),
        isVisible: document.visibilityState === "visible",
      },
      mailboxNotifiable: mailboxNotifiable(activeMailbox?.role),
    });
    if (!allowed) return;

    for (const email of arrivals) {
      const content = notificationContent(
        email,
        t("notification.unknownSender"),
        t("notification.noSubject"),
      );
      try {
        const toast = new Notification(content.title, {
          body: content.body,
          tag: content.tag,
          icon: content.icon,
        });
        toast.onclick = () => {
          /*
           * Focus the window FIRST, then navigate. The other order leaves the
           * app on the right message in a window still behind the user's
           * editor, which reads as the click having done nothing.
           */
          window.focus();
          navigate(withMessage(route, email.id));
          toast.close();
        };
      } catch {
        // Some browsers throw rather than no-op when notifications are
        // unavailable despite a granted permission (an iOS PWA without the
        // right entitlement). Losing a toast must never break the refresh.
      }
    }
    /*
     * `route` and `navigate` are read by the click handler but deliberately NOT
     * dependencies: including them would re-run the detector whenever the user
     * opened a message, and the arrival ref would then be advanced by a render
     * that observed nothing new. `liveEmails` changing is the only event that
     * means "the window was refetched".
     */
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [liveEmails, useCache, prefs.notifications, activeMailbox?.role, t]);

  /*
   * Load the cache whenever the live path is not working.
   *
   * Runs on the ROUTE, so navigating between folders offline reads each
   * folder's cached window — the sidebar is not decoration in this mode, the
   * folders it lists genuinely open.
   */
  useEffect(() => {
    const cache = offline.cache;
    if (cache === undefined) return undefined;
    let cancelled = false;

    void (async () => {
      const boxes = await cache.mailboxes();
      if (cancelled) return;
      setCachedMailboxes(boxes);

      if (offline.isOnline && !requestFailed) return;

      if (route.kind === "search") {
        /*
         * Offline search: a plain text match over what is stored, labelled as
         * such in the UI. Deliberately NOT the operator language of canon §2.5
         * — see `offline/search.ts` on why imitating the server's search badly
         * is worse than not imitating it.
         */
        const [headers, bodies] = await Promise.all([cache.allHeaders(), cache.allBodies()]);
        if (cancelled) return;
        setCachedEmails(searchOffline(route.query, headers, bodies));
        return;
      }

      if (route.kind === "mailbox") {
        // The route may still carry a role alias ("inbox"); resolve it against
        // the CACHED list, which is the only list that exists in this mode.
        const resolved = resolveMailbox(boxes, route.mailboxId);
        const found = await cache.headers(resolved?.id ?? route.mailboxId);
        if (cancelled) return;
        setCachedEmails(found);
        return;
      }

      /*
       * A label view offline. The keywords are on the cached headers, so this
       * is answerable — unlike a server-side filter, which is not.
       */
      if (route.kind === "label") {
        const keyword = encodeLabelKeyword(route.name);
        const headers = await cache.allHeaders();
        if (cancelled) return;
        setCachedEmails(headers.filter((email) => email.keywords?.[keyword] === true));
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [offline.cache, offline.isOnline, requestFailed, route, refreshToken]);

  /** The cached body of the open message, and whether there is one at all. */
  useEffect(() => {
    const cache = offline.cache;
    if (cache === undefined || openMessageId === undefined || !isOfflineMode) {
      setCachedDetail(undefined);
      setDetailUncached(false);
      return undefined;
    }
    let cancelled = false;
    void (async () => {
      const found = await cache.body(openMessageId);
      if (cancelled) return;
      setCachedDetail(found);
      // The honest per-message state: the list renders happily from cache while
      // THIS message's body was never stored.
      setDetailUncached(found === undefined);
    })();
    return () => {
      cancelled = true;
    };
  }, [offline.cache, openMessageId, isOfflineMode]);

  // --- P3: multi-select ----------------------------------------------------

  const orderedIds = useMemo(() => groups.map((group) => group.id), [groups]);

  /*
   * A stale selection must not survive a refresh: it would make a bulk action
   * target messages that are gone and keep the "3 selected" badge lying.
   */
  useEffect(() => {
    setSelection((current) => pruneSelection(current, orderedIds));
  }, [orderedIds]);

  const toggleSelect = useCallback(
    (
      group: ThreadGroup,
      modifiers: { readonly toggle: boolean; readonly range: boolean },
    ): void => {
      setSelection((current) => selectionAfterClick(current, group.id, orderedIds, modifiers));
    },
    [orderedIds],
  );

  /*
   * B4: `selectAll` is gone, subsumed by `runSelectBy`.
   *
   * The toolbar's checkbox now calls `runSelectBy("all")` / `runSelectBy("none")`
   * — the same reducer its dropdown's other four scopes use, and the same one
   * the `* a` / `* n` chords already resolved to. Keeping a second path that
   * only knew two of the six states would have been a second place for "select
   * all" to mean something slightly different from what the keyboard means.
   *
   * `selectionAfterSelectAll` stays in `mail/selection.ts` with its own tests:
   * it is the reducer `selectionByScope("all")` is defined in terms of.
   */

  /**
   * The MESSAGE ids an action applies to.
   *
   * The list is grouped into threads, and a thread row stands for every
   * message in it — so archiving a conversation archives the conversation, not
   * just its newest message, which is what "archive" means in every mail
   * client and what a user who selects one row expects.
   *
   * E1 made this subtler and it is worth being explicit about, because getting
   * it wrong would be silent: on the COLLAPSED path the server returns exactly
   * one message per conversation, so `group.messages` holds one member and
   * `group.messages.map(id)` would archive the newest reply and leave the rest
   * of the thread in the inbox. The `Thread/get` that rides the list's batch is
   * what closes that — it carries every member id — and it is consulted first,
   * with the window's own messages as the fallback for the uncollapsed path.
   */
  const threadMembers = useMemo(() => {
    const byThread = new Map<string, readonly string[]>();
    for (const thread of listThreads) byThread.set(thread.id, thread.emailIds);
    return byThread;
  }, [listThreads]);

  /**
   * Every message id one row stands for.
   *
   * The single place that answers "what is this conversation, really", so the
   * toolbar, the hover actions and `_` cannot drift apart on it.
   */
  const idsOfGroup = useCallback(
    (group: ThreadGroup): readonly string[] =>
      threadMembers.get(group.id) ?? group.messages.map((message) => message.id),
    [threadMembers],
  );

  const targetMessageIds = useCallback(
    (): readonly string[] => {
      const groupIds = actionTargets(selection, selectedId);
      const wanted = new Set(groupIds);
      const out: string[] = [];
      for (const group of groups) {
        if (!wanted.has(group.id)) continue;
        for (const id of idsOfGroup(group)) out.push(id);
      }
      return out;
    },
    [selection, selectedId, groups, idsOfGroup],
  );

  // --- P3: running an action ------------------------------------------------

  const roleMailboxId = useCallback(
    (role: string): string | undefined =>
      mailboxes.find((mailbox) => mailbox.role === role)?.id,
    [mailboxes],
  );

  const trashMailboxId = roleMailboxId("trash");

  /**
   * E7: the address index (canon §2.3, Gmail's "Other contacts" model).
   *
   * Declared here because it needs the Sent mailbox, which needs
   * `roleMailboxId`. Its two feeds are wired below: the write-through rides the
   * list and reader effects, and the first-run scan is the hook's own.
   */
  const addressIndex = useAddressIndex({
    ownAddress: identity?.email,
    sentMailboxId: roleMailboxId("sent"),
    fetchSentPage,
  });
  // Publishes the current `record` to the ref the effects above read.
  recordAddressesRef.current = addressIndex.record;

  /** E7: `.eml` attachments for forward-as-attachment. */
  const forwardAttachments = useForwardAsAttachment({
    client,
    accountId,
    authorization,
    uploadUrlTemplate: session?.uploadUrl,
    sessionCapabilities: session?.capabilities,
  });

  /** True when `delete` on the current targets ERASES rather than moves (W-A2). */
  const willDeletePermanently = useMemo(() => {
    if (trashMailboxId === undefined) return false;
    const ids = new Set(targetMessageIds());
    const targets = projected.filter((email) => ids.has(email.id));
    // Only claim "permanent" when EVERY target is already in Trash; a mixed
    // selection gets the softer, and truthful, wording.
    return targets.length > 0 && targets.every((email) => deleteIsPermanent(email, trashMailboxId));
  }, [projected, targetMessageIds, trashMailboxId]);

  /**
   * Where auto-advance would land, read through a ref.
   *
   * `dispatchAction` needs the adjacent conversation, and `siblingGroup` is
   * defined below it (it depends on the route, which depends on things
   * declared later). A ref reassigned on every render states "read the latest"
   * honestly, where hoisting the whole navigation block above the action
   * dispatcher would tangle two unrelated concerns to satisfy a declaration
   * order.
   *
   * Gmail's own naming is the reason for the inversion at the call site: its
   * "newer message" is the one ABOVE in a newest-first list, which is what
   * this codebase calls the "previous" sibling.
   */
  const advanceTargetRef = useRef<(direction: "next" | "previous") => ThreadGroup | undefined>(
    () => undefined,
  );

  /**
   * Dispatches an action and reports its outcome.
   *
   * The failure path is the point: it names WHAT failed with the server's own
   * sentence, and the hook has already restored the prior state. A silent
   * revert — the thing this must never be — would leave the user believing
   * they mis-clicked.
   */
  const dispatchAction = useCallback(
    async (
      action: MessageAction,
      successMessage: string,
      options: {
        /**
         * E2: record an undo offer for this action. Carries the mailbox the
         * messages came FROM, which the inverse move needs and which the
         * action itself does not know.
         */
        readonly undoOrigin?: string | undefined;
        readonly wasPermanent?: boolean;
        /** E2 item 8: close the reader when the OPEN message was the target. */
        readonly autoAdvance?: boolean;
      } = {},
    ): Promise<void> => {
      if (action.ids.length === 0) return;
      const targetedOpenMessage =
        openMessageId !== undefined && action.ids.includes(openMessageId);

      const result = await actions.run(action, projected);

      if (result.failed.length > 0) {
        setToast(
          result.succeeded.length > 0
            ? `${format("action.partialFailure", result.succeeded.length, result.failed.length)} ${result.failureMessage ?? ""}`.trim()
            : `${t("action.failedTitle")}: ${result.failureMessage ?? t("action.failedRestored")}`,
        );
        // Even a total failure refetches: the server's truth is the only thing
        // that resolves a disagreement about what actually happened.
        refresh();
        return;
      }
      if (result.succeeded.length === 0) return;

      /*
       * The undo offer is recorded for the ids that actually SUCCEEDED. Using
       * the requested ids would offer to un-archive a message the server
       * refused to archive, which would move it somewhere it never left.
       */
      if (options.undoOrigin !== undefined) {
        undoCounter.current += 1;
        setUndoEntry(
          makeUndoEntry({
            id: undoCounter.current,
            action: { ...action, ids: result.succeeded },
            inverses: new Map(),
            originMailboxId: options.undoOrigin,
            now: Date.now(),
            ...(options.wasPermanent !== undefined ? { wasPermanent: options.wasPermanent } : {}),
          }),
        );
      }

      setToast(successMessage);
      setSelection(EMPTY_SELECTION);

      /*
       * Auto-advance (canon §2.2, E2's default now wired to E5's preference).
       *
       * Gmail's default is "back to the conversation list", which is what E2
       * shipped; the opt-in offers "older messages" or "newer messages"
       * instead. The destination is read through `advanceTargetRef` BEFORE
       * `refresh()` runs, from the list as it still stands: once the refetch
       * lands, the archived row has left and the same index points one row too
       * far. That is the off-by-one `runArchiveAndAdvance` documents, and it is
       * why the order of these two statements matters.
       *
       * The direction is INVERTED on the way in, and deliberately so: the list
       * is newest-first, so Gmail's "newer message" is the row ABOVE, which
       * this codebase calls the "previous" sibling.
       *
       * Falling back to the list when there is no adjacent message is
       * deliberate: leaving the reader open on a message that is no longer in
       * this folder is the failure the default exists to prevent, and it must
       * not come back through the opt-in.
       */
      if (options.autoAdvance === true && targetedOpenMessage) {
        const next =
          prefs.autoAdvance === "list"
            ? undefined
            : advanceTargetRef.current(prefs.autoAdvance === "newer" ? "previous" : "next");
        if (next === undefined) {
          navigate(withMessage(route, undefined));
        } else {
          setSelectedId(next.id);
          navigate(withMessage(route, next.latest.id));
        }
      }

      refresh();
    },
    [actions, projected, refresh, t, format, openMessageId, navigate, route, prefs.autoAdvance],
  );

  const runArchive = useCallback((): void => {
    const archiveId = roleMailboxId("archive");
    const ids = targetMessageIds();
    if (archiveId === undefined) {
      setToast(t("action.failedTitle"));
      return;
    }
    void dispatchAction(
      { kind: "archive", ids, mailboxId: archiveId },
      format("action.doneArchived", ids.length),
      { undoOrigin: activeMailbox?.id, autoAdvance: true },
    );
  }, [roleMailboxId, targetMessageIds, dispatchAction, format, t, activeMailbox?.id]);

  /**
   * E7: the archive half of Send & Archive, and its inverse (canon §2.3).
   *
   * # Why these do not go through `dispatchAction`
   *
   * `dispatchAction` is built for a user action on the current SELECTION: it
   * shows a toast, clears the selection, records an undo offer and may
   * auto-advance the reader. None of that is right here. The composer owns the
   * message the user is looking at, it is showing its own undo affordance
   * already, and a second undo offer in a toast — one that would undo the
   * archive but not the send — is precisely the confusing pair this avoids.
   *
   * So these call `actions.run` directly: the optimistic overlay still applies
   * (the conversation leaves the list at once, which is what makes the button
   * feel instant), and the composer reports the outcome in the words that fit
   * what it just did.
   *
   * # Why the inverse is a MOVE back to the origin, not "unarchive"
   *
   * There is no un-archive verb; archiving is a move, and its inverse is the
   * move back. The origin is captured at call time from the mailbox the user is
   * looking at, which is where the conversation was when they hit reply.
   */
  const archiveConversation = useCallback(
    async (ids: readonly string[]): Promise<boolean> => {
      const archiveId = roleMailboxId("archive");
      if (archiveId === undefined || ids.length === 0) return false;
      const result = await actions.run({ kind: "archive", ids, mailboxId: archiveId }, projected);
      // Partial success counts as failure for the composer's message: the
      // conversation is not archived if half of it moved.
      return result.failed.length === 0 && result.succeeded.length > 0;
    },
    [roleMailboxId, actions, projected],
  );

  /** The mailbox a Send & Archive would return the conversation TO. */
  const replyOriginMailboxId = activeMailbox?.id;

  /**
   * E7: the messages a Send & Archive would archive.
   *
   * The whole CONVERSATION, not the one message being replied to — that is
   * what "archive" means everywhere else in this screen (see
   * `targetMessageIds`) and what canon §2.1 says a toolbar action does. Read
   * from the open message's thread, falling back to the message itself when
   * the thread has not loaded.
   *
   * Empty whenever there is nothing on screen to archive, which is what removes
   * the button rather than leaving one that would act on nothing.
   */
  const archiveTargetIds = useMemo<readonly string[]>(() => {
    if (openMessageId === undefined) return [];
    const thread = detail.thread;
    if (thread !== undefined && thread.emailIds.length > 0) return thread.emailIds;
    return [openMessageId];
  }, [openMessageId, detail.thread]);

  const restoreConversation = useCallback(
    async (ids: readonly string[]): Promise<void> => {
      if (replyOriginMailboxId === undefined || ids.length === 0) return;
      await actions.run({ kind: "move", ids, mailboxId: replyOriginMailboxId }, projected);
      refresh();
    },
    [replyOriginMailboxId, actions, projected, refresh],
  );

  const runDelete = useCallback(async (): Promise<void> => {
    const ids = targetMessageIds();
    if (ids.length === 0) return;
    /*
     * Confirmation is asked for ONLY when the delete is irreversible (W-A2:
     * already in Trash). A confirm on every delete trains people to dismiss
     * it, which is how the one that mattered gets dismissed too.
     *
     * E11: our own dialog. `window.confirm` is suppressed outright in some
     * embedding contexts, which would have turned this checkpoint into a
     * silent permanent delete — a correctness bug, not a styling one.
     */
    if (
      willDeletePermanently &&
      !(await confirm({
        message: format("action.confirmDeleteForever", ids.length),
        confirmLabel: t("action.confirm"),
        destructive: true,
      }))
    ) {
      return;
    }
    void dispatchAction(
      { kind: "delete", ids },
      willDeletePermanently
        ? format("action.doneDeletedForever", ids.length)
        : format("action.doneDeleted", ids.length),
      {
        undoOrigin: activeMailbox?.id,
        // A permanent delete has no reverse; `makeUndoEntry` refuses it and no
        // undo is offered, rather than one that would quietly fail.
        wasPermanent: willDeletePermanently,
        autoAdvance: true,
      },
    );
  }, [targetMessageIds, willDeletePermanently, dispatchAction, format, activeMailbox?.id, confirm, t]);

  const runMove = useCallback(
    (mailboxId: string): void => {
      const ids = targetMessageIds();
      const target = mailboxes.find((mailbox) => mailbox.id === mailboxId);
      void dispatchAction(
        { kind: "move", ids, mailboxId },
        format(
          "action.doneMoved",
          target === undefined ? "" : mailboxLabel(target, t),
        ),
        { undoOrigin: activeMailbox?.id, autoAdvance: true },
      );
    },
    [targetMessageIds, mailboxes, dispatchAction, format, t, activeMailbox?.id],
  );

  // --- E2: spam and not-spam ------------------------------------------------

  const junkMailboxId = roleMailboxId("junk");
  const inboxMailboxId = roleMailboxId("inbox");
  /** True when the folder on screen IS Junk, which flips every spam control. */
  const inJunk = activeMailbox?.role === "junk";
  /** True when the folder on screen is Trash, which reveals "Empty trash now". */
  const inTrash = activeMailbox?.role === "trash";

  /**
   * Reports spam, or — inside Junk — takes the message back out.
   *
   * One function for both directions because the user's gesture is one
   * gesture: `!` and the button both mean "this classification is wrong".
   * Which way it goes is a property of where they are standing.
   */
  const runToggleSpam = useCallback((): void => {
    const ids = targetMessageIds();
    if (ids.length === 0) return;
    const destination = inJunk ? inboxMailboxId : junkMailboxId;
    if (destination === undefined) {
      // No Junk folder (or no Inbox): say so rather than silently doing
      // nothing, which would read as a broken button.
      setToast(t("action.failedTitle"));
      return;
    }
    void dispatchAction(
      { kind: inJunk ? "notSpam" : "spam", ids, mailboxId: destination },
      inJunk ? format("action.doneNotSpam", ids.length) : format("action.doneSpam", ids.length),
      { undoOrigin: activeMailbox?.id, autoAdvance: true },
    );
  }, [
    targetMessageIds,
    inJunk,
    inboxMailboxId,
    junkMailboxId,
    dispatchAction,
    format,
    t,
    activeMailbox?.id,
  ]);

  const runToggleRead = useCallback(
    (force?: boolean): void => {
      const ids = targetMessageIds();
      const idSet = new Set(ids);
      const targets = projected.filter((email) => idSet.has(email.id));
      const value = force ?? resolveToggle(targets, KEYWORD_SEEN).value;
      void dispatchAction(
        { kind: value ? "markRead" : "markUnread", ids },
        value ? t("action.markRead") : t("action.markUnread"),
      );
    },
    [targetMessageIds, projected, dispatchAction, t],
  );

  const runToggleFlag = useCallback((): void => {
    const ids = targetMessageIds();
    const idSet = new Set(ids);
    const targets = projected.filter((email) => idSet.has(email.id));
    const value = resolveToggle(targets, KEYWORD_FLAGGED).value;
    void dispatchAction(
      { kind: value ? "flag" : "unflag", ids },
      value ? t("action.flag") : t("action.unflag"),
    );
  }, [targetMessageIds, projected, dispatchAction, t]);

  // --- E8: applying and removing labels -------------------------------------

  /**
   * Applies or removes ONE label across the current target set.
   *
   * It goes through the same optimistic machinery as every other action, so a
   * label paints instantly and rolls back with the server's own words on
   * failure. The server enforces the 26-keyword ceiling
   * (`checkKeywordCeiling`), and its refusal arrives as a per-record `SetError`
   * that `dispatchAction` surfaces verbatim — which is the last line of defence
   * behind the budget the UI already showed.
   */
  const runToggleLabel = useCallback(
    (keyword: string, apply: boolean): void => {
      const ids = targetMessageIds();
      if (ids.length === 0) return;
      void dispatchAction(
        { kind: apply ? "label" : "unlabel", ids, keyword },
        apply ? t("label.applied") : t("label.removed"),
      );
    },
    [targetMessageIds, dispatchAction, t],
  );

  /** The keyword maps the "Label as" menu reads its tri-state from. */
  const labelSelection = useMemo<readonly (Readonly<Record<string, boolean>> | undefined)[]>(() => {
    const ids = new Set(targetMessageIds());
    return projected.filter((email) => ids.has(email.id)).map((email) => email.keywords);
  }, [targetMessageIds, projected]);

  /** Navigates to a label's view. */
  const goToLabel = useCallback(
    (label: Label): void => {
      navigate({ kind: "label", name: label.name });
    },
    [navigate],
  );

  /**
   * The `l` key's target: the action bar's "Label as" menu, published by the
   * menu itself when it mounts.
   *
   * A ref rather than state, so registering it does not re-render the screen —
   * and so `runAction`'s dependency list does not change on every mount of the
   * bar, which would recreate the global key handler on every list refresh.
   */
  /**
   * B3: the mail route to come BACK to when settings closes.
   *
   * A ref, updated on every render where the route is NOT settings, rather than
   * `navigate(-1)` or a history pop. Two reasons, both about honesty: the user
   * may have arrived at `/settings/general` from a bookmark with no history
   * behind it, where a back-step would leave the app (this returns them to the
   * inbox instead); and they may have changed tabs three times while inside,
   * where a single back-step lands on another settings tab rather than on mail.
   *
   * A ref and not state, because nothing renders from it — writing it during
   * render would be a re-render for a value only a click handler reads.
   */
  const mailRouteRef = useRef<Route>(DEFAULT_ROUTE);
  if (route.kind !== "settings") mailRouteRef.current = route;

  /** Opens the settings page at a tab (the gear's panel, the label manager). */
  const goToSettings = useCallback(
    (tab: SettingsTab = DEFAULT_SETTINGS_TAB): void => {
      navigate({ kind: "settings", tab });
    },
    [navigate],
  );

  /** Leaves settings for the mail the user was looking at. */
  const leaveSettings = useCallback((): void => {
    navigate(mailRouteRef.current);
  }, [navigate]);

  const openLabelMenu = useRef<(() => void) | undefined>(undefined);
  const registerLabelMenu = useCallback((open: () => void): void => {
    openLabelMenu.current = open;
  }, []);

  /**
   * Opens Settings on the LABELS tab — the menu's "Manage labels…", and the
   * sidebar's `+`.
   *
   * B3 makes this land on the right tab rather than on whatever tab the page
   * happened to be showing. That is the deep-linking the route was added for,
   * used first by the app itself: before, "Manage labels…" opened the sheet on
   * General and left the user to find the section.
   */
  const openLabelSettings = useCallback((): void => {
    goToSettings("labels");
  }, [goToSettings]);

  /**
   * Reports how a rename or delete ended, in the migration's own terms.
   *
   * The three outcomes are three different sentences on purpose. "Stopped after
   * 1,800" and "1,800 updated — some still carry the old label" mean different
   * things to a user deciding whether to run it again, and collapsing them into
   * "done" would make a half-finished migration look finished. That is the
   * failure `emptyTrash` was written to avoid, applied here.
   */
  const reportMigration = useCallback(
    (result: MigrateResult): void => {
      if (result.failureMessage !== undefined && result.migrated === 0) {
        setToast(`${t("label.migrateFailed")}: ${result.failureMessage}`);
        return;
      }
      if (result.aborted) {
        setToast(format("label.migrateAborted", result.migrated));
        return;
      }
      setToast(
        result.incomplete
          ? format("label.migrateIncomplete", result.migrated)
          : format("label.migrateDone", result.migrated),
      );
    },
    [t, format],
  );

  /** Everything the settings sheet's label manager needs, in one object. */
  /**
   * E6: filters, blocked senders, forwarding, vacation and quota.
   *
   * One controller for five surfaces because four of them ARE one Sieve script
   * on the server (see `useFilters`), and the fifth is read on the same screen.
   */
  const filtersApi = useFilters({ client, session, accountId, authedFetch });

  const labelSettings = useMemo<LabelsSectionProps>(
    () => ({
      labels: labelsApi.labels,
      budget: labelsApi.budget,
      onCreate: labelsApi.create,
      onSetColor: labelsApi.setColor,
      onSetVisibility: labelsApi.setVisibility,
      onRename: (label, newName) => {
        void labelsApi.rename(label, newName).then(reportMigration);
      },
      onDelete: (label) => {
        // The confirmation says what is and is NOT deleted: removing a label
        // from 4,000 messages is alarming precisely because it sounds like
        // deleting 4,000 messages.
        void (async () => {
          if (
            !(await confirm({
              message: format("label.deleteConfirm", label.name),
              destructive: true,
            }))
          ) {
            return;
          }
          void labelsApi.remove(label).then(reportMigration);
        })();
      },
      migrationStatus: labelsApi.isMigrating
        ? format("label.migrating", labelsApi.migratedCount)
        : undefined,
      onAbortMigration: labelsApi.isMigrating ? labelsApi.abort : undefined,
      onCreateFolder: undefined,
    }),
    [labelsApi, reportMigration, format, confirm],
  );

  /**
   * The four E6 settings sections, each present only when its capability is.
   *
   * `undefined` is what makes the settings sheet render its honest skeleton, so
   * the ternaries below are not defensive coding — they are the mechanism by
   * which a deployment without Sieve says so instead of showing controls that
   * cannot work. The three capabilities are checked SEPARATELY because
   * `session.go` gates them on three independent config fields.
   */
  const filterSettings = useMemo<FiltersSectionProps | undefined>(
    () =>
      filtersApi.capabilities.filters
        ? {
            rules: filtersApi.rules,
            scriptActive: filtersApi.scriptActive,
            forwardingAddresses: filtersApi.forwardingAddresses,
            mailboxes,
            labels: labelsApi.labels,
            onCreate: filtersApi.createRule,
            onUpdate: filtersApi.updateRule,
            onDelete: filtersApi.deleteRule,
            onMove: filtersApi.moveRule,
            onActivate: filtersApi.activate,
            isActivating: filtersApi.isActivating,
            isBusy: filtersApi.isBusy,
            error: filtersApi.error,
          }
        : undefined,
    [filtersApi, mailboxes, labelsApi.labels],
  );

  const blockedSettings = useMemo<BlockedSectionProps | undefined>(
    () =>
      filtersApi.capabilities.filters
        ? {
            rules: filtersApi.rules,
            onBlock: filtersApi.createRule,
            onUnblock: filtersApi.deleteRule,
            isBusy: filtersApi.isBusy,
            error: filtersApi.error,
          }
        : undefined,
    [filtersApi],
  );

  const forwardingSettings = useMemo<ForwardingSectionProps | undefined>(
    () =>
      filtersApi.capabilities.filters
        ? {
            addresses: filtersApi.forwardingAddresses,
            forwardAll: filtersApi.forwardAll,
            onAdd: filtersApi.addForwardingAddress,
            onVerify: filtersApi.verifyForwarding,
            onRemove: filtersApi.removeForwardingAddress,
            onSaveForwardAll: filtersApi.saveForwarding,
            isBusy: filtersApi.isBusy,
            error: filtersApi.error,
          }
        : undefined,
    [filtersApi],
  );

  const vacationSettings = useMemo<VacationSectionProps | undefined>(
    () =>
      filtersApi.capabilities.vacation
        ? {
            vacation: filtersApi.vacation,
            onSave: filtersApi.saveVacationResponse,
            error: filtersApi.error,
          }
        : undefined,
    [filtersApi],
  );

  const quotaSettings = useMemo<QuotaRowProps | undefined>(
    () =>
      filtersApi.capabilities.quota
        ? {
            quotas: filtersApi.quotas,
            error: filtersApi.quotaError,
            onRefresh: filtersApi.refreshQuota,
          }
        : undefined,
    [filtersApi],
  );

  /**
   * E6: blocking the open message's sender (canon §2.2).
   *
   * The dialog is here rather than in the reader because the decision needs
   * facts the reader does not hold — whether the address is already blocked —
   * and because "block" is a settings write that must survive the reading pane
   * closing under it.
   *
   * The unsubscribe sentence is appended when the message offers one, which is
   * the canon's own pairing: block sends future mail to Spam and does NOT
   * unsubscribe, so a newsletter is better handled by the other button. Saying
   * so at the moment of the decision is the only place it helps.
   */
  const blockSender = useCallback(
    (address: string): void => {
      const opened = detail.email;
      const advice =
        opened === undefined ? undefined : blockAdvice(opened, address);
      void (async () => {
        const body =
          advice?.hasUnsubscribe === true
            ? `${t("blocked.dialogBody")}\n\n${t("blocked.dialogUnsubscribe")}`
            : t("blocked.dialogBody");
        if (
          !(await confirm({
            title: format("blocked.dialogTitle", address),
            message: body,
            confirmLabel: t("blocked.confirm"),
            destructive: true,
          }))
        ) {
          return;
        }
        filtersApi.createRule(blockDraft(address));
      })();
    },
    [detail.email, confirm, t, format, filtersApi],
  );

  /**
   * The labels the sidebar shows, after `labelListVisibility`.
   *
   * `showIfUnread` needs to know whether a label has unread mail. The honest
   * answer available client-side is "is any UNREAD message in the loaded window
   * carrying it" — there is no per-keyword unread count in JMAP the way there
   * is per mailbox, and inventing one would mean a query per label on every
   * load. So the predicate is bounded by the window and errs toward a shorter
   * sidebar, which is the failure direction that does not add rows the user did
   * not ask for.
   */
  const sidebarLabels = useMemo(() => {
    const unread = new Set<string>();
    for (const email of projected) {
      if (email.keywords?.[KEYWORD_SEEN] === true) continue;
      for (const [keyword, value] of Object.entries(email.keywords ?? {})) {
        if (value) unread.add(keyword);
      }
    }
    return visibleLabels(labelsApi.labels, (label) => unread.has(label.keyword));
  }, [labelsApi.labels, projected]);

  // --- E1: the conversation reader ------------------------------------------

  /**
   * Marks the messages that were EXPANDED read (canon §2.1).
   *
   * It goes through `actions.run` rather than `dispatchAction` for two
   * reasons, both about NOISE: this fires as a side effect of reading rather
   * than of a gesture, so it must not raise a toast ("Marked as read" on every
   * message you open would be unbearable), and it must not clear the selection
   * or trigger auto-advance — the user did not act, they read.
   *
   * A failure is deliberately silent HERE and only here: the messages stay
   * unread, the next open retries, and there is no user intent to report back
   * on. Every other write in this screen reports its failures.
   */
  const markMessagesRead = useCallback(
    (ids: readonly string[]): void => {
      if (ids.length === 0) return;
      void (async () => {
        const result = await actions.run({ kind: "markRead", ids }, projected);
        // Refresh only when something actually changed, so the sidebar's unread
        // counts follow — but never on a no-op, which would refetch the list
        // every time a collapsed message was expanded and found already read.
        if (result.succeeded.length > 0) setRefreshToken((token) => token + 1);
      })();
    },
    [actions, projected],
  );

  /**
   * The open conversation's keyboard controls, published by ConversationView.
   *
   * A ref rather than state: the controls object is rebuilt on every render of
   * the conversation (it closes over the message list), and holding it in
   * state would re-render this whole screen each time. Nothing here reads it
   * during render — only the keyboard handler does, and that runs on an event.
   */
  const conversationControls = useRef<ConversationControls | undefined>(undefined);
  const setConversationControls = useCallback(
    (controls: ConversationControls | undefined): void => {
      conversationControls.current = controls;
    },
    [],
  );


  // --- E2: undo (`z`) -------------------------------------------------------

  /**
   * E4's un-snooze, read through a ref by `runUndo`.
   *
   * Same shape and same reason as `advanceTargetRef`: the real callback lives
   * in the E4 block below, which depends on the resolved mailbox list, and a
   * ref reassigned on every render says "read the latest" honestly.
   */
  const unsnoozeRef = useRef<(ids: readonly string[]) => void>(() => undefined);

  /**
   * Takes back the last undoable action.
   *
   * This re-issues the INVERSE MUTATION to the server. Repainting the client
   * from the inverse patches alone would put the row back in the list while
   * Dovecot still had the message where the action left it — a lie that
   * survives exactly until the next refresh, which is the worst kind because
   * the user believes the undo worked.
   */
  const runUndo = useCallback((): void => {
    const entry = undoEntry;
    if (entry === undefined || !isUndoable(entry, Date.now())) {
      setToast(t("action.undoExpired"));
      return;
    }

    /*
     * E4: a snooze's reverse is not a `MessageAction`.
     *
     * A `move` back into the inbox would restore the row while leaving the
     * server's wake time in place, so the message would return and then vanish
     * again at the appointed hour. `Snooze/set destroy` is the only honest
     * reverse, so the entry carries the ids and this branch calls it.
     */
    if ((entry.unsnoozeIds ?? []).length > 0) {
      setUndoEntry(undefined);
      // Through a ref for the same reason `advanceTargetRef` exists: the E4
      // block is declared below this one (it depends on the mailbox list and
      // the route), and hoisting it here to satisfy a declaration order would
      // tangle two unrelated concerns.
      unsnoozeRef.current(entry.unsnoozeIds ?? []);
      return;
    }

    if (entry.inverseAction === undefined) {
      setToast(t("action.undoExpired"));
      return;
    }
    // Consumed immediately: a second `z` must not re-issue the same move, and
    // the toast's button must not stay live while the request is in flight.
    setUndoEntry(undefined);
    const inverse = entry.inverseAction;
    void (async () => {
      const result = await actions.run(inverse, projected);
      if (result.failed.length > 0 || result.succeeded.length === 0) {
        setToast(`${t("action.undoFailed")}: ${result.failureMessage ?? ""}`.trim());
      } else {
        setToast(t("action.undoDone"));
      }
      refresh();
    })();
  }, [undoEntry, actions, projected, refresh, t]);

  // The offer expires on its own, so a toast that has scrolled out of the
  // user's attention cannot be triggered by a stray `z` minutes later.
  useEffect(() => {
    if (undoEntry === undefined) return undefined;
    const remaining = Math.max(0, undoEntry.expiresAt - Date.now());
    const timer = setTimeout(() => {
      setUndoEntry((current) => (current?.id === undoEntry.id ? undefined : current));
    }, remaining);
    return () => {
      clearTimeout(timer);
    };
  }, [undoEntry]);

  // --- E2: emptying the Trash ----------------------------------------------

  /**
   * Destroys everything in Trash, in windows.
   *
   * It cannot be one call: `Email/query` answers within a 200-row window, so a
   * Trash of 4,000 messages would delete 200 and report success. The loop
   * re-queries rather than paging, because every destroy shifts the list under
   * any offset — "the first 200 still there" is the only stable cursor over a
   * shrinking set. `mail/emptyTrash.ts` owns the stopping rules.
   */
  const runEmptyTrash = useCallback(
    (trashId: string): void => {
      if (client === undefined || accountId === "" || isEmptyingTrash) return;
      void (async () => {
        setEmptyingTrash(true);
        setToast(t("action.emptyTrashWorking"));
        const rounds: EmptyRound[] = [];
        let failureMessage: string | undefined;
        try {
          for (let round = 0; round < MAX_EMPTY_ROUNDS; round += 1) {
            const page = await queryEmails(client, accountId, {
              kind: "mailbox",
              mailboxId: trashId,
            });
            const ids = page.ids;
            if (ids.length === 0) {
              rounds.push({ attempted: 0, destroyed: 0 });
              break;
            }
            const outcome = await destroyMessages(client, accountId, ids);
            failureMessage ??= firstFailureMessage(outcome);
            const done: EmptyRound = {
              attempted: ids.length,
              destroyed: outcome.destroyed.length,
            };
            rounds.push(done);
            if (!shouldContinue(done, rounds.length)) break;
          }
          const result = summarize(rounds, failureMessage);
          setToast(
            result.destroyed === 0
              ? (result.failureMessage ?? t("action.emptyTrashEmpty"))
              : format("action.emptyTrashDone", result.destroyed),
          );
        } catch (error) {
          setToast(
            `${t("action.failedTitle")}: ${error instanceof Error ? error.message : String(error)}`,
          );
        } finally {
          setEmptyingTrash(false);
          refresh();
        }
      })();
    },
    [client, accountId, isEmptyingTrash, t, format, refresh],
  );

  /**
   * The sidebar's "Empty trash now", with its confirmation.
   *
   * The count in the prompt comes from the mailbox's own `totalEmails` — the
   * user must be told HOW MANY messages they are about to erase, because
   * "empty the trash" reads very differently at 3 messages and at 4,000.
   */
  const confirmEmptyTrash = useCallback(
    async (trash: Mailbox): Promise<void> => {
      if (trash.totalEmails === 0) {
        setToast(t("action.emptyTrashEmpty"));
        return;
      }
      if (
        !(await confirm({
          message: format("action.emptyTrashConfirm", trash.totalEmails),
          destructive: true,
        }))
      ) {
        return;
      }
      runEmptyTrash(trash.id);
    },
    [format, t, runEmptyTrash, confirm],
  );

  // --- navigation ----------------------------------------------------------

  const openGroup = useCallback(
    (group: ThreadGroup): void => {
      navigate(withMessage(route, group.latest.id));
    },
    [navigate, route],
  );

  const closeMessage = useCallback((): void => {
    navigate(withMessage(route, undefined));
  }, [navigate, route]);

  /*
   * E2 item 3: moving between messages WITH the reader open.
   *
   * `j`/`k` keep their list-only meaning when nothing is open (they move the
   * focused row without opening it — the Gmail behaviour P2 shipped). Once a
   * message is open they navigate the route, which is also Gmail's: the
   * reading pane is the cursor at that point, and moving the row behind an
   * open message would leave the two disagreeing about "current".
   *
   * The index is taken from the GROUP whose message is open rather than from
   * `selectedId`, so a message reached by URL — or by a `[`/`]` that already
   * moved the selection — still advances from where the user actually is.
   */
  const openGroupIndex = useMemo((): number => {
    if (openMessageId === undefined) return -1;
    return groups.findIndex((group) =>
      group.messages.some((message) => message.id === openMessageId),
    );
  }, [groups, openMessageId]);

  const siblingGroup = useCallback(
    (direction: "next" | "previous"): ThreadGroup | undefined => {
      if (openGroupIndex < 0) return undefined;
      return groups[openGroupIndex + (direction === "next" ? 1 : -1)];
    },
    [groups, openGroupIndex],
  );

  // Kept current for `dispatchAction`'s auto-advance, which runs before this
  // is declared. See `advanceTargetRef`.
  advanceTargetRef.current = siblingGroup;

  const goToSibling = useCallback(
    (direction: "next" | "previous"): void => {
      const target = siblingGroup(direction);
      if (target === undefined) return;
      setSelectedId(target.id);
      navigate(withMessage(route, target.latest.id));
    },
    [siblingGroup, navigate, route],
  );

  const goToMailbox = useCallback(
    (mailbox: Mailbox): void => {
      navigate({ kind: "mailbox", mailboxId: mailboxSegment(mailbox) });
      setSearchText("");
    },
    [navigate],
  );

  const runSearch = useCallback(
    (text: string): void => {
      const query = normalizeQuery(text);
      const next: Route =
        query === ""
          ? { kind: "mailbox", mailboxId: "inbox" }
          : { kind: "search", query };
      // Typing replaces rather than pushes: one Back press should leave the
      // search, not walk back through every keystroke that built it.
      replace(next);

      /*
       * E3: remember it, but only if it is a real search.
       *
       * The debounce means this fires once the user has STOPPED typing, so the
       * history collects finished queries rather than every prefix of one. A
       * query too short to send is not remembered either — offering "ar" back
       * as a suggestion would be noise where the useful entry is the whole
       * thing the user eventually typed.
       */
      if (query !== "" && isSearchable(query)) {
        setRecentSearches((current) => {
          const updated = withRecentSearch(current, query);
          saveRecentSearches(updated);
          return updated;
        });
      }
    },
    [replace],
  );

  /** E3: forgets the stored searches, from the dropdown's own affordance. */
  const clearRecentSearches = useCallback((): void => {
    setRecentSearches([]);
    saveRecentSearches([]);
  }, []);

  // --- E9: the Outbox -------------------------------------------------------

  const goToOutbox = useCallback((): void => {
    navigate({ kind: "outbox" });
    setSearchText("");
  }, [navigate]);

  /**
   * Sends everything the queue is holding.
   *
   * The transport is the ORDINARY send path (`sendDraft`), which is what keeps
   * the undo window the server's business: a drained message gets its
   * `EmailSubmission` and its `sendAt` exactly like a message sent online, so
   * there is no second local delay to race `cancelSubmission`.
   *
   * A missing client or identity is a TRANSIENT failure, not a permanent one:
   * it means this tab is not ready yet, which the next trigger may fix. Marking
   * such an item permanently failed would strand a perfectly good message.
   */
  const drainQueue = useCallback(async (): Promise<void> => {
    const store = offline.outbox;
    if (store === undefined || client === undefined || accountId === "") return;

    const items = await store.list();
    if (items.length === 0) return;

    const result = await drainOutbox(
      items,
      async (item: OutboxItem): Promise<SendAttempt> => {
        if (identity === undefined) {
          return { kind: "failed", error: t("send.failedTitle"), permanent: false };
        }
        try {
          const sent = await sendDraft(client, accountId, item.spec, {
            identityId: item.identityId,
            sentMailboxId: item.sentMailboxId,
          });
          if (sent.submission === undefined) {
            /*
             * The server accepted the request and refused the message. That is
             * a judgement about THIS message — a bad address, a policy refusal
             * — so retrying it would only reproduce the refusal.
             */
            return {
              kind: "failed",
              error: firstFailureMessage(sent.outcome) ?? t("send.failedTitle"),
              permanent: true,
            };
          }
          return { kind: "sent" };
        } catch (error) {
          // A thrown error is the network, not the message.
          return {
            kind: "failed",
            error: error instanceof Error ? error.message : String(error),
            permanent: false,
          };
        }
      },
      async (item) => {
        await store.put(item);
      },
    );

    for (const id of result.sent) await store.remove(id);
    await offline.reloadOutbox();
    if (result.sent.length > 0) {
      setToast(format("outbox.sentToast", result.sent.length));
      refresh();
    }
  }, [
    offline,
    client,
    accountId,
    identity,
    t,
    format,
    refresh,
  ]);

  /*
   * Drain when the connection comes back.
   *
   * Both triggers the spec names land here: the `online` event (through
   * `offline.isOnline`) and SSE recovery (through `streamDead` clearing). The
   * `sending` state in the queue is what makes a double trigger safe — the
   * second drain finds nothing drainable.
   */
  useEffect(() => {
    if (!offline.isOnline || streamDead) return;
    void drainQueue();
  }, [offline.isOnline, streamDead, drainQueue]);

  const retryOutboxItem = useCallback(
    (item: OutboxItem): void => {
      void (async () => {
        await offline.outbox?.put(retryItem(item));
        await offline.reloadOutbox();
        await drainQueue();
      })();
    },
    [offline, drainQueue],
  );

  const discardOutboxItem = useCallback(
    (item: OutboxItem): void => {
      void (async () => {
        // The one place a queued message is allowed to disappear: the user asked.
        if (!(await confirm({ message: t("draft.discardConfirm"), destructive: true }))) return;
        await offline.outbox?.remove(item.id);
        await offline.reloadOutbox();
      })();
    },
    [offline, t, confirm],
  );

  /**
   * Queues a message the composer could not send because there is no network.
   *
   * Returns false when the write did NOT commit, which the composer treats as
   * a refusal to close: a message that is neither sent nor stored must not
   * vanish behind a dialog that closed as if it had worked.
   */
  const queueForLater = useCallback(
    async (spec: DraftSpec): Promise<boolean> => {
      const store = offline.outbox;
      if (store === undefined || identity === undefined) return false;
      const stored = await store.put({
        id: newOutboxId(),
        accountId,
        state: "queued",
        spec,
        identityId: identity.id,
        sentMailboxId: roleMailboxId("sent"),
        queuedAt: Date.now(),
        attempts: 0,
        lastError: undefined,
        subject: spec.subject,
        recipients: spec.to.map((address) => address.email),
      });
      if (!stored) return false;
      await offline.reloadOutbox();
      setToast(t("outbox.queuedToast"));
      return true;
    },
    [offline, identity, accountId, roleMailboxId, t],
  );

  // --- E4: snooze, mute and scheduled sends ---------------------------------

  /**
   * Whether this server has the triage verbs at all.
   *
   * A vendor capability is by definition something a server may not have, and
   * RFC 8620 §1.8 makes the opt-in per capability. So every control below is
   * gated on this rather than rendered and failing: a snooze button that
   * answers `unknownMethod` is worse than no snooze button, because the user
   * cannot tell it apart from a bug.
   */
  const hasTriage = sessionHasTriage(session, accountId);
  /**
   * The Snoozed folder, resolved by the NAME the session publishes.
   *
   * Not by role: RFC 6154 defines no SPECIAL-USE attribute for snoozed mail and
   * `internal/sync/snooze.go` refused to invent one, so the name IS the
   * contract — and it comes from the server so a rename does not need a client
   * release.
   */
  const snoozedFolderName = snoozeMailboxName(session);
  const snoozedMailbox = useMemo<Mailbox | undefined>(() => {
    if (!hasTriage || snoozedFolderName === undefined) return undefined;
    return mailboxes.find((mailbox) => mailbox.name === snoozedFolderName);
  }, [hasTriage, snoozedFolderName, mailboxes]);
  const inSnoozed =
    snoozedMailbox !== undefined && activeMailbox?.id === snoozedMailbox.id;

  /** E4: pending snoozes, by message id — only ever read in the Snoozed view. */
  const [snoozeUntilById, setSnoozeUntilById] = useState<ReadonlyMap<string, string>>(
    () => new Map<string, string>(),
  );
  const [scheduledSends, setScheduledSends] = useState<readonly ScheduledSend[]>([]);
  const [scheduleBusyId, setScheduleBusyId] = useState<string | undefined>(undefined);

  /*
   * The mute set, refetched on every list refresh.
   *
   * `Mute/get` returns the WHOLE set in one indexed read — the server's own
   * header calls it "a few dozen ids a client can cache" and declines to
   * register a /changes for exactly that reason — so re-reading it alongside
   * the list is cheaper than tracking deltas would be.
   */
  useEffect(() => {
    if (!hasTriage || client === undefined || accountId === "") return undefined;
    const controller = new AbortController();
    void (async () => {
      try {
        const muted = await fetchMutedThreadIds(client, accountId, controller.signal);
        if (!controller.signal.aborted) setMutedThreadIds(muted);
      } catch {
        /*
         * Swallowed deliberately: mute state is a BADGE. A list that renders
         * without it is missing an icon, where a list that fails to render
         * because a decoration could not be fetched is broken. The same rule
         * `Thread/get` follows on the list path.
         */
      }
    })();
    return () => {
      controller.abort();
    };
  }, [hasTriage, client, accountId, refreshToken]);

  /* The wake times, fetched only where they are shown. */
  useEffect(() => {
    if (!hasTriage || !inSnoozed || client === undefined || accountId === "") {
      return undefined;
    }
    const controller = new AbortController();
    void (async () => {
      try {
        const records = await fetchSnoozes(client, accountId, controller.signal);
        if (controller.signal.aborted) return;
        setSnoozeUntilById(new Map(records.map((record) => [record.id, record.until])));
      } catch {
        // Same reasoning as the mute set: the rows still render, without times.
      }
    })();
    return () => {
      controller.abort();
    };
  }, [hasTriage, inSnoozed, client, accountId, refreshToken]);

  /** E4: the account's schedule limits, read from the session (declared == applied). */
  const limits = useMemo(() => scheduleLimits(session, accountId), [session, accountId]);

  const inScheduled = route.kind === "scheduled";

  /*
   * The scheduled sends.
   *
   * Fetched whenever the app is running, not only inside the view, because the
   * SIDEBAR entry appears only when the list is non-empty (E9b's Outbox shape)
   * — and a sidebar that can only discover its own entry by being visited is
   * not an entry at all.
   */
  useEffect(() => {
    if (client === undefined || accountId === "") return undefined;
    const controller = new AbortController();
    void (async () => {
      try {
        const rows = await fetchScheduled(client, accountId, {
          now: Date.now(),
          undoWindowSeconds: prefs.undoSendSeconds,
          signal: controller.signal,
        });
        if (!controller.signal.aborted) setScheduledSends(rows);
      } catch {
        // A view that cannot list is empty, not broken.
      }
    })();
    return () => {
      controller.abort();
    };
  }, [client, accountId, refreshToken, prefs.undoSendSeconds]);

  const goToScheduled = useCallback((): void => {
    navigate({ kind: "scheduled" });
    setSearchText("");
  }, [navigate]);

  const goToSnoozed = useCallback((): void => {
    if (snoozedMailbox === undefined) {
      // The chord is bound whether or not the folder exists; saying so is
      // better than a keypress that silently does nothing.
      setToast(t("snooze.unavailable"));
      return;
    }
    goToMailbox(snoozedMailbox);
  }, [snoozedMailbox, goToMailbox, t]);

  /**
   * Snoozes a set of messages until an instant.
   *
   * The WHOLE CONVERSATION goes, which is Gmail's unit: a thread with three of
   * its messages asleep and one awake is a conversation nobody can reason
   * about. The caller expands the thread (`idsOfGroup` / `targetMessageIds`).
   *
   * The row leaves the list optimistically because the server MOVEs it — that
   * is GC-10's whole point, snoozing is IMAP-visible — so a `removed` patch is
   * the truth rather than a guess, and the refetch that follows confirms it.
   *
   * The undo offer is real: un-snoozing before the wake is a plain move back
   * (`Snooze/set destroy`), and the ids the client is holding are still the
   * server's. That stops being true AFTER the wake — `internal/sync/snooze.go`
   * re-APPENDs with a fresh INTERNALDATE so the mail "returns to the top of
   * your inbox", which mints a new UID — but the undo window is eight seconds
   * and the nearest wake is hours away, so the case cannot arise.
   */
  const runSnooze = useCallback(
    (ids: readonly string[], until: string): void => {
      if (client === undefined || accountId === "" || ids.length === 0) return;
      void (async () => {
        try {
          const outcome = await snoozeMessages(client, accountId, ids, until);
          if (hasFailures(outcome)) {
            setToast(
              `${t("action.failedTitle")}: ${firstFailureMessage(outcome) ?? ""}`.trim(),
            );
            refresh();
            return;
          }
          setToast(format("snooze.done", 1));
          // The inverse is a real server call, not a repaint — the rule
          // `mail/undo.ts` exists to enforce.
          undoCounter.current += 1;
          setUndoEntry({
            id: undoCounter.current,
            action: { kind: "move", ids, mailboxId: snoozedMailbox?.id ?? "" },
            inverseAction: undefined,
            inverses: new Map(),
            expiresAt: Date.now() + UNDO_WINDOW_MS,
            unsnoozeIds: ids,
          });
          refresh();
        } catch (error) {
          setToast(error instanceof Error ? error.message : String(error));
        }
      })();
    },
    [client, accountId, snoozedMailbox, t, format, refresh],
  );

  /** E4: brings snoozed messages back now — the inverse of a snooze. */
  const runUnsnooze = useCallback(
    (ids: readonly string[]): void => {
      if (client === undefined || accountId === "" || ids.length === 0) return;
      void (async () => {
        try {
          const outcome = await unsnoozeMessages(client, accountId, ids);
          setToast(
            hasFailures(outcome)
              ? `${t("action.failedTitle")}: ${firstFailureMessage(outcome) ?? ""}`.trim()
              : t("snooze.undone"),
          );
          refresh();
        } catch (error) {
          setToast(error instanceof Error ? error.message : String(error));
        }
      })();
    },
    [client, accountId, t, refresh],
  );

  // Kept current for `runUndo`, which is declared above this block.
  unsnoozeRef.current = runUnsnooze;

  /**
   * Mutes or unmutes the conversations under the cursor.
   *
   * Gmail's `m` toggles, and the direction is decided by the SELECTION as a
   * whole — the same rule `resolveToggle` applies to read and starred: if any
   * target is unmuted, mute them all; only when every one is already muted does
   * the key unmute. Toggling each independently leaves a mixed selection after
   * an explicit keystroke, which is never what anyone meant.
   *
   * The cached set is updated optimistically so the badge appears at once, and
   * the refetch above reconciles it.
   */
  const runToggleMute = useCallback((): void => {
    if (client === undefined || accountId === "" || !hasTriage) return;
    const ids = actionTargets(selection, selectedId);
    const threadIds = groups
      .filter((group) => ids.includes(group.id))
      .map((group) => group.latest.threadId)
      .filter((id): id is string => id !== undefined && id !== "");
    if (threadIds.length === 0) return;

    const muting = threadIds.some((id) => !mutedThreadIds.has(id));
    setMutedThreadIds((current) => {
      const next = new Set(current);
      for (const id of threadIds) {
        if (muting) next.add(id);
        else next.delete(id);
      }
      return next;
    });

    void (async () => {
      try {
        const outcome = await setThreadsMuted(client, accountId, threadIds, muting);
        if (hasFailures(outcome)) {
          setToast(`${t("action.failedTitle")}: ${firstFailureMessage(outcome) ?? ""}`.trim());
        } else {
          setToast(muting ? t("mute.done") : t("mute.undone"));
        }
      } catch (error) {
        setToast(error instanceof Error ? error.message : String(error));
      } finally {
        // Whatever happened, the server's answer is the one that stands.
        refresh();
      }
    })();
  }, [
    client,
    accountId,
    hasTriage,
    selection,
    selectedId,
    groups,
    mutedThreadIds,
    t,
    refresh,
  ]);

  /** E4: cancels a scheduled send. The DRAFT survives, and the toast says so. */
  const cancelScheduled = useCallback(
    (item: ScheduledSend): void => {
      if (client === undefined || accountId === "") return;
      setScheduleBusyId(item.id);
      void (async () => {
        try {
          const outcome = await cancelSubmission(client, accountId, item.id);
          setToast(
            hasFailures(outcome)
              ? // `cannotUnsend` is a TRUE statement: the mail is going out.
                (firstFailureMessage(outcome) ?? t("send.cannotUnsend"))
              : t("schedule.canceled"),
          );
        } catch (error) {
          setToast(error instanceof Error ? error.message : String(error));
        } finally {
          setScheduleBusyId(undefined);
          refresh();
        }
      })();
    },
    [client, accountId, t, refresh],
  );

  /** E4: releases a scheduled send immediately (cancel, then resubmit). */
  const sendScheduledImmediately = useCallback(
    (item: ScheduledSend): void => {
      if (client === undefined || accountId === "" || identity === undefined) return;
      if (item.emailId === "") {
        // Without the draft's id there is nothing to resubmit, and cancelling
        // alone would silently turn "send now" into "cancel".
        setToast(t("action.failedTitle"));
        return;
      }
      setScheduleBusyId(item.id);
      void (async () => {
        try {
          const result = await sendScheduledNow(client, accountId, item.id, item.emailId, {
            identityId: identity.id,
            sentMailboxId: roleMailboxId("sent"),
          });
          if (result.resubmitted === undefined) {
            setToast(firstFailureMessage(result.canceled) ?? t("send.cannotUnsend"));
          } else if (hasFailures(result.resubmitted)) {
            setToast(
              `${t("action.failedTitle")}: ${firstFailureMessage(result.resubmitted) ?? ""}`.trim(),
            );
          } else {
            setToast(t("schedule.sentNow"));
          }
        } catch (error) {
          setToast(error instanceof Error ? error.message : String(error));
        } finally {
          setScheduleBusyId(undefined);
          refresh();
        }
      })();
    },
    [client, accountId, identity, roleMailboxId, t, refresh],
  );

  /**
   * True when EVERY conversation under the cursor is already muted.
   *
   * Drives the label on the mute control, so the button says what the click
   * will do. `every` rather than `some` for the same reason `resolveToggle`
   * uses `some` in the opposite direction: the toggle mutes unless there is
   * nothing left to mute, so "Unmute" is only honest when all of them are.
   */
  const allTargetsMuted = useMemo((): boolean => {
    const ids = new Set(actionTargets(selection, selectedId));
    const targets = groups.filter((group) => ids.has(group.id));
    if (targets.length === 0) return false;
    return targets.every((group) => {
      const threadId = group.latest.threadId;
      return threadId !== undefined && mutedThreadIds.has(threadId);
    });
  }, [selection, selectedId, groups, mutedThreadIds]);

  /**
   * E4: whether the conversation OPEN IN THE READER is muted.
   *
   * Read off the open message rather than off `allTargetsMuted`, which follows
   * the selection: the reader shows one conversation, and a badge that changed
   * because a checkbox moved somewhere else in the list would be describing a
   * different thread than the one on screen.
   */
  const openThreadIsMuted = useMemo((): boolean => {
    const threadId = detail.email?.threadId;
    return threadId !== undefined && mutedThreadIds.has(threadId);
  }, [detail.email, mutedThreadIds]);

  /** Opens the snooze menu from the `b` key. Published by the menu itself. */
  const snoozeMenuRef = useRef<(() => void) | undefined>(undefined);
  const registerSnoozeMenu = useCallback((open: () => void): void => {
    snoozeMenuRef.current = open;
  }, []);

  /*
   * E11 — the two application keys of canon §2.7 that reach the toolbar.
   *
   * Same ref-not-state discipline as the menus above, and for the same reason:
   * registering a handle must not re-render the screen or rebuild the global
   * key handler on every list refresh.
   */
  const focusToolbarRef = useRef<(() => void) | undefined>(undefined);
  const registerToolbar = useCallback((focus: () => void): void => {
    focusToolbarRef.current = focus;
  }, []);
  const moreMenuRef = useRef<(() => void) | undefined>(undefined);
  const registerMoreMenu = useCallback((open: () => void): void => {
    moreMenuRef.current = open;
  }, []);

  // --- P3: opening the composer --------------------------------------------

  /** The wording the quoting module needs, resolved from the string table. */
  const quotingStrings = useMemo<QuotingStrings>(
    () => ({
      attributionLine: (date, sender) => format("compose.attributionLine", date, sender),
      forwardedHeader: t("compose.forwardedHeader"),
      from: t("compose.forwardedFrom"),
      date: t("compose.forwardedDate"),
      subject: t("compose.forwardedSubject"),
      to: t("compose.forwardedTo"),
      formatDate: (isoDate) => formatFullDate(isoDate, locale),
    }),
    [t, format, locale],
  );

  /**
   * The message a reply/forward is about.
   *
   * The OPEN message when the reading pane has one, because that is what the
   * user is looking at; otherwise the newest message of the focused row. A
   * reply that quotes a different message from the one on screen is a bug
   * nobody reports and everybody notices.
   */
  const composeSubject = useCallback((): Email | undefined => {
    if (detail.email !== undefined) return detail.email;
    const group = groups.find((candidate) => candidate.id === selectedId);
    return group?.latest;
  }, [detail.email, groups, selectedId]);

  const openCompose = useCallback((): void => {
    setComposerDraft(newDraft(true));
  }, []);

  /*
   * E9: a `mailto:` the OS handed us through the manifest's protocol handler.
   *
   * The browser does not deliver the URI directly — it percent-encodes the
   * whole thing into the registered template's `%s` and navigates. So this is
   * an ordinary URL to read, and `parseComposeRequest` is a pure function over
   * it (src/pwa/mailto.ts explains the shape and why only `mailto:` is
   * accepted).
   *
   * The parameter is stripped from the URL as soon as the composer opens. A
   * reload must not resurrect a composer the user dismissed, and a
   * correspondent's address does not belong in an address bar, a history
   * entry, or a screenshot of either.
   *
   * `window.location.search` is read directly rather than through the route:
   * `compose` is not part of the route model and should not become part of it
   * — it is a one-shot instruction, not a destination.
   */
  useEffect(() => {
    if (typeof window === "undefined") return;
    // One composer at a time; an arriving mailto must not replace whatever the
    // user is already in the middle of writing.
    if (composerDraft !== undefined) return;

    const current = `${window.location.pathname}${window.location.search}`;
    const request = parseComposeRequest(current);
    if (request === undefined) return;

    const base = draftTo(request.to, true);
    setComposerDraft({
      ...base,
      subject: request.subject ?? base.subject,
      text: request.body ?? base.text,
      // A prefilled body arrives as plain text; keeping the rich seed as well
      // would let the editor seed from an empty HTML string and drop it.
      html: request.body === undefined ? base.html : undefined,
      // Straight to the subject when the sender named a recipient but no
      // subject, and to the body when both came prefilled.
      focusField: request.subject === undefined ? "subject" : "body",
    });

    window.history.replaceState(null, "", urlWithoutCompose(current));
  }, [composerDraft]);

  /**
   * E2 item 6: the `mailto:` unsubscribe.
   *
   * It opens OUR composer prefilled rather than handing the URI to the OS.
   * The user sees exactly what is about to leave their address, from the
   * account they are signed into, and can cancel — where a `mailto:` handoff
   * would either open an unrelated desktop client or do nothing visible at
   * all, depending on a browser setting they have never seen.
   *
   * The caret opens in the BODY: the recipient and subject came from the
   * sender and there is nothing to fix about them.
   */
  const openUnsubscribeMail = useCallback(
    (to: string, subject: string | undefined, body: string | undefined): void => {
      const base = newDraft(false);
      setComposerDraft({
        ...base,
        to: [makeChip(to)],
        subject: subject ?? t("action.unsubscribe"),
        text: body ?? "",
        focusField: "body",
      });
    },
    [t],
  );

  const openReply = useCallback(
    (all: boolean): void => {
      const original = composeSubject();
      if (original === undefined) return;
      /*
       * A reply needs the message BODY, and a list row does not carry one
       * (LIST_PROPERTIES omits bodyValues deliberately — asking for it would
       * make the server re-parse every message to paint a list). When the
       * reading pane is open the detail is already loaded; otherwise the
       * message is opened first, and the user replies from there. Quoting an
       * empty body would silently produce a reply with no quote.
       */
      if (original.bodyValues === undefined) {
        navigate(withMessage(route, original.id));
        return;
      }
      setComposerDraft(replyDraft(original, username, all, quotingStrings));
    },
    [composeSubject, username, quotingStrings, navigate, route],
  );

  /**
   * E1: reply/forward to ONE message of a conversation (canon §2.1).
   *
   * `openReply` above acts on "the message the reader is showing", which in a
   * conversation is ambiguous — there are twelve. These take the message
   * explicitly, so a reply quotes the message whose Reply button was pressed
   * rather than whichever one the route happens to name.
   *
   * They do NOT navigate when the body is missing, which is the one behavioural
   * difference from `openReply`: in a conversation only an EXPANDED message
   * shows these buttons and expanding is what fetches the body, so a missing
   * body means a fetch still in flight. Navigating away mid-fetch would move
   * the user somewhere they did not ask to go; doing nothing lets them press
   * again a moment later.
   */
  const replyToMessage = useCallback(
    (original: Email, all: boolean): void => {
      if (original.bodyValues === undefined) return;
      setComposerDraft(replyDraft(original, username, all, quotingStrings));
    },
    [username, quotingStrings],
  );

  const forwardMessage = useCallback(
    (original: Email): void => {
      if (original.bodyValues === undefined) return;
      setComposerDraft(forwardDraft(original, quotingStrings));
    },
    [quotingStrings],
  );

  /**
   * E7: forward one or more messages AS ATTACHMENTS (canon §2.3).
   *
   * Unlike the quoted forward next door, this needs no body — the whole message
   * is downloaded as a blob, so a row from the list works as well as an opened
   * message. That is what lets it serve multi-select from the action bar.
   *
   * The composer opens with the attachments already on it, and with a subject
   * naming how many messages are inside. What could NOT be attached is named in
   * a toast: silently forwarding three of four selected messages is exactly the
   * kind of quiet partial success this codebase refuses elsewhere.
   */
  const forwardAsAttachment = useCallback(
    (targets: readonly Email[]): void => {
      if (targets.length === 0) return;
      setToast(t("forwardAttachment.preparing"));
      void (async () => {
        const { attachments, refused } = await forwardAttachments.prepare(targets);
        if (attachments.length === 0) {
          setToast(t("forwardAttachment.failed"));
          return;
        }
        setComposerDraft({
          // Matches the default "Compose" takes; the composer then applies
          // the remembered plain/rich preference on top (E7).
          ...newDraft(true),
          subject: format("forwardAttachment.subject", attachments.length),
        });
        setPendingAttachments(attachments);
        setToast(
          refused.length === 0
            ? undefined
            : `${t("forwardAttachment.failed")}: ${refused.join(", ")}`,
        );
      })();
    },
    [forwardAttachments, t, format],
  );

  const openForward = useCallback((): void => {
    const original = composeSubject();
    if (original === undefined) return;
    if (original.bodyValues === undefined) {
      navigate(withMessage(route, original.id));
      return;
    }
    setComposerDraft(forwardDraft(original, quotingStrings));
  }, [composeSubject, quotingStrings, navigate, route]);

  /**
   * Opening a message in Drafts RESUMES it rather than reading it.
   *
   * A draft is unfinished writing, not mail; showing it in a reading pane with
   * a Reply button would be nonsense. The composer carries the draft's server
   * id so the next save destroys this revision instead of accumulating one
   * message per edit (RFC 8621 §4.6's immutability, handled in `saveDraft`).
   */
  /*
   * `navigate` and the current `route` are read through a ref rather than
   * named as dependencies. Listing them would re-run this effect on every
   * navigation — harmless, because the guards below make it a no-op, but it
   * would make the effect's real trigger (the detail arriving for a message in
   * Drafts) impossible to see. A ref states "read the latest, do not re-run"
   * honestly, where suppressing the lint rule would only hide the question.
   */
  const closeReadingPane = useRef<() => void>(() => undefined);
  closeReadingPane.current = () => {
    navigate(withMessage(route, undefined));
  };

  useEffect(() => {
    if (activeMailbox?.role !== "drafts") return;
    const open = detail.email;
    if (open?.bodyValues === undefined) return;
    if (composerDraft !== undefined) return;
    setComposerDraft(resumeDraft(open));
    // The reading pane must not stay open behind the composer.
    closeReadingPane.current();
  }, [activeMailbox?.role, detail.email, composerDraft]);

  // --- E2: the remaining triage verbs --------------------------------------

  /**
   * `]` / `[`: archive, then land on the next/previous conversation.
   *
   * The move is decided BEFORE the archive, from the list as it stands. Doing
   * it afterwards would read an index into a list the archived row has already
   * left, which lands one row too far — the classic off-by-one that makes this
   * key feel unreliable in clients that get it wrong.
   */
  const runArchiveAndAdvance = useCallback(
    (direction: "next" | "previous"): void => {
      const index = groups.findIndex((group) => group.id === selectedId);
      const target = index < 0 ? undefined : groups[index + (direction === "next" ? 1 : -1)];
      const wasReading = openMessageId !== undefined;
      runArchive();
      if (target === undefined) return;
      setSelectedId(target.id);
      // Only follow into the reader when the user was already reading; from
      // the list, `]` advances the cursor without opening anything.
      if (wasReading) navigate(withMessage(route, target.latest.id));
    },
    [groups, selectedId, runArchive, openMessageId, navigate, route],
  );

  /**
   * Gmail's `_`: mark unread from the focused row DOWNWARD.
   *
   * Deliberately ignores the checkbox selection — `_` is a positional verb
   * ("I'll deal with the rest later"), not a bulk one, and applying it to a
   * selection somewhere else in the list would be a different action wearing
   * the same key.
   */
  const runMarkUnreadFromHere = useCallback((): void => {
    const groupIds = idsFromHere(orderedIds, selectedId);
    if (groupIds.length === 0) return;
    const wanted = new Set(groupIds);
    const ids = groups.filter((group) => wanted.has(group.id)).flatMap(idsOfGroup);
    void dispatchAction({ kind: "markUnread", ids }, t("action.markUnread"));
  }, [orderedIds, selectedId, groups, idsOfGroup, dispatchAction, t]);

  /** The `* a`/`* n`/`* r`/`* u`/`* s`/`* t` chords. */
  const runSelectBy = useCallback(
    (scope: SelectionScope): void => {
      /*
       * A thread row counts as READ only when every message in it is read, and
       * as STARRED when any message is — the same asymmetry the row's own dot
       * and star use, so `* u` selects exactly the rows that look unread.
       */
      const rows = groups.map((group) => ({
        id: group.id,
        isRead: !group.hasUnread,
        isStarred: group.hasFlagged,
      }));
      setSelection(selectionByScope(rows, scope));
    },
    [groups],
  );

  // --- the keyboard --------------------------------------------------------

  const keyboardRef = useRef<KeyboardState>(INITIAL_KEYBOARD_STATE);
  const chordTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const runAction = useCallback(
    (action: ShortcutAction): void => {
      const index = groups.findIndex((group) => group.id === selectedId);
      switch (action.kind) {
        /*
         * E2: with a message OPEN, `j`/`k` navigate the reader (Gmail's own
         * semantics); with nothing open they move the list cursor exactly as
         * P2 shipped. One key, two contexts — because "current message" means
         * the open one when there is one.
         */
        case "next": {
          if (openMessageId !== undefined) {
            goToSibling("next");
            break;
          }
          const next = groups[Math.min(index + 1, groups.length - 1)];
          if (next !== undefined) setSelectedId(next.id);
          break;
        }
        case "previous": {
          if (openMessageId !== undefined) {
            goToSibling("previous");
            break;
          }
          const previous = groups[Math.max(index - 1, 0)];
          if (previous !== undefined) setSelectedId(previous.id);
          break;
        }
        case "open": {
          const current = groups[index];
          if (current !== undefined) openGroup(current);
          break;
        }
        case "back":
          /*
           * B3: `u` means "back to the list", and from the settings page the
           * list is the mail the user came from. Falling through to
           * `closeMessage()` there would be a no-op — settings carries no open
           * message — so the one key the canon gives for "go back" would do
           * nothing on the one view it is most needed on.
           */
          if (inSettings) leaveSettings();
          else closeMessage();
          break;
        case "focusSearch":
          searchInputRef.current?.focus();
          searchInputRef.current?.select();
          break;
        case "goToMailbox": {
          const target = mailboxes.find((mailbox) => mailbox.role === action.role);
          if (target !== undefined) goToMailbox(target);
          break;
        }
        case "help":
          setHelpOpen(true);
          break;
        case "closeOverlay":
          /*
           * The overlays are closed in STACKING order, topmost first.
           *
           * This branch exists because the global handler calls
           * preventDefault() once it owns a key, which means a <dialog>'s own
           * native Escape never fires while this listener is bound. Any modal
           * added to this screen must therefore be listed here or it becomes
           * un-dismissable by keyboard — a real defect.
           *
           * B3: the settings SHEET is gone from this list, and deliberately.
           * Settings is a route now, and Escape does not close pages — Escape
           * dismisses overlays. Leaving settings is `u`/Back/the explicit
           * button, exactly as leaving any other view is. The quick-settings
           * dock is absent for the opposite reason: it handles its own Escape
           * in the capture phase, before this listener runs.
           */
          if (helpOpen) setHelpOpen(false);
          // The composer owns its own Escape (it must flush the draft first),
          // so the global handler must not close it out from under that.
          else if (composerDraft !== undefined) break;
          else if (selection.selected.size > 0) setSelection(EMPTY_SELECTION);
          else if (openMessageId !== undefined) closeMessage();
          break;

        // P3: the keys P2 left bound but inert are now real.
        case "archive":
          runArchive();
          break;
        case "delete":
          void runDelete();
          break;
        /*
         * E11 — `Shift+I` / `Shift+U`, the DIRECTIONAL read pair (canon §2.7).
         *
         * `runToggleRead` already took a `force`, which is what makes this a
         * two-line change: the toggle was never the underlying operation, only
         * the binding. With a mixed selection the direction is now the user's,
         * not a coin-flip decided by whichever row happened to be first.
         */
        case "markRead":
          runToggleRead(action.read);
          break;
        case "toggleFlag":
          runToggleFlag();
          break;
        case "compose":
          openCompose();
          break;
        case "reply":
          /*
           * E5 `defaultReplyBehavior` (canon §2.3): `r` is "the default reply",
           * not "reply to sender". Gmail's setting moves exactly this key —
           * with `replyAll` chosen, `r` opens a reply-all — while `Shift+A`
           * stays unconditionally reply-all, so the explicit verb never becomes
           * ambiguous.
           *
           * There is deliberately NO inverse binding for "reply to sender only"
           * when the default is reply-all. Gmail has none either, and inventing
           * one would add a key to the map that exists in no other mail client's
           * muscle memory.
           */
          openReply(prefs.defaultReplyBehavior === "replyAll");
          break;
        case "replyAll":
          openReply(true);
          break;
        case "forward":
          openForward();
          break;
        case "selectRow": {
          const current = groups[index];
          if (current !== undefined) toggleSelect(current, { toggle: true, range: false });
          break;
        }

        // --- E4 (canon §2.2) ---
        /*
         * `b` OPENS the menu rather than snoozing: a single key cannot name one
         * of five wake times. Exactly what `l` does for labels, and the
         * imperative handle is the same one — a synthetic click on a disabled
         * trigger would silently do nothing and look like a broken shortcut.
         */
        case "snooze":
          snoozeMenuRef.current?.();
          break;
        case "toggleMute":
          runToggleMute();
          break;
        case "goToSnoozed":
          goToSnoozed();
          break;

        // --- E2 ---
        case "toggleSpam":
          runToggleSpam();
          break;
        case "undo":
          runUndo();
          break;
        case "archiveAndAdvance":
          runArchiveAndAdvance(action.direction);
          break;
        case "markUnreadFromHere":
          runMarkUnreadFromHere();
          break;
        case "selectBy":
          runSelectBy(action.scope);
          break;

        /*
         * E1: the conversation keys (canon §2.1).
         *
         * They act ONLY on an open conversation, and they no-op silently
         * otherwise — `;` pressed in the list has nothing to expand, and
         * inventing a meaning for it there (expand the focused row?) would be
         * a key that does two different things depending on where you stand.
         *
         * `conversationControls.current` is undefined when the reader is
         * closed OR when conversation view is off, so the preference gates
         * these keys without this switch having to know about it.
         */
        case "expandConversation":
          if (action.expand) conversationControls.current?.expandAll();
          else conversationControls.current?.collapseAll();
          break;
        case "conversationMessage":
          conversationControls.current?.goToMessage(action.direction);
          break;
        /*
         * E8 — `l` opens the "Label as" menu rather than applying anything: a
         * single key cannot name one of up to 26 labels, and Gmail's `l` opens
         * the picker too. With nothing selected the menu's trigger is disabled
         * and `open()` returns without doing anything, which is the same
         * outcome as clicking the greyed-out button.
         */
        case "labelAs":
          openLabelMenu.current?.();
          break;

        /*
         * E11 — `,` and `.` (canon §2.7).
         *
         * Both are NAVIGATION into controls that already existed and were
         * mouse-only. `.` opens the overflow menu the same way `l` and `b`
         * open theirs; when the bar does not render one (no forward-as-
         * attachment on this server) the handle is undefined and the key
         * no-ops, which is the honest outcome — there is no menu to open.
         */
        case "focusToolbar":
          focusToolbarRef.current?.();
          break;
        case "moreActions":
          moreMenuRef.current?.();
          break;
      }
    },
    [
      groups,
      selectedId,
      openGroup,
      closeMessage,
      mailboxes,
      goToMailbox,
      helpOpen,
      inSettings,
      leaveSettings,
      openMessageId,
      composerDraft,
      selection,
      runArchive,
      runDelete,
      runToggleRead,
      runToggleFlag,
      openCompose,
      openReply,
      openForward,
      toggleSelect,
      goToSibling,
      runToggleSpam,
      runUndo,
      runArchiveAndAdvance,
      runMarkUnreadFromHere,
      runSelectBy,
      // E4
      runToggleMute,
      goToSnoozed,
      // E5 v2: `r` follows the reply default.
      prefs.defaultReplyBehavior,
    ],
  );

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      const { action, nextState } = resolveShortcut(
        {
          key: event.key,
          code: event.code,
          ctrlKey: event.ctrlKey,
          metaKey: event.metaKey,
          altKey: event.altKey,
          shiftKey: event.shiftKey,
          target: event.target,
        },
        keyboardRef.current,
        /*
         * E5: the `keyboardShortcuts` preference (D-3 keeps it ON by default,
         * diverging from Gmail). "Off" still resolves Escape and `/` — see
         * `isAlwaysOnKey` — because a modal that cannot be dismissed and a
         * search that cannot be reached are accessibility defects, not
         * preferences.
         */
        { enabled: prefs.keyboardShortcuts },
      );

      /*
       * While the composer is open the list behind it is not the user's
       * context: `e` must not archive a message they cannot see. The dialog's
       * own Escape handling still runs, because the dialog element gets the
       * event first.
       */
      if (composerDraft !== undefined) return;

      keyboardRef.current = nextState;

      // A `g` or `*` prefix expires, so a stray press cannot swallow the next
      // real keystroke indefinitely.
      if (chordTimer.current !== undefined) clearTimeout(chordTimer.current);
      if (hasPendingChord(nextState)) {
        chordTimer.current = setTimeout(() => {
          keyboardRef.current = INITIAL_KEYBOARD_STATE;
        }, CHORD_TIMEOUT_MS);
      }

      if (action === undefined) return;
      // Only now is the event ours — preventDefault after deciding, never
      // before, so unbound keys reach the browser untouched.
      event.preventDefault();
      runAction(action);
    };

    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      if (chordTimer.current !== undefined) clearTimeout(chordTimer.current);
    };
  }, [runAction, composerDraft, prefs.keyboardShortcuts]);

  /*
   * Toasts clear themselves — but a toast carrying an UNDO offer must outlive
   * the offer, not the other way round. A bubble that vanished at 2.6 s while
   * `z` still worked for another 5.4 s would make the undo window a secret
   * only the keyboard knew about.
   */
  useEffect(() => {
    if (toast === undefined) return undefined;
    const timer = setTimeout(
      () => {
        setToast(undefined);
      },
      undoEntry === undefined ? 2600 : UNDO_WINDOW_MS,
    );
    return () => {
      clearTimeout(timer);
    };
  }, [toast, undoEntry]);

  // --- render --------------------------------------------------------------

  const isReading = openMessageId !== undefined;

  /*
   * E5: the reading pane (canon §2.4, /9499937 — "No split" / "Right of
   * inbox" / "Below inbox").
   *
   * The three are LAYOUT variants of the same components, chosen by a class on
   * the grid container, not three code paths:
   *
   *   - "right"  — the split P2 shipped: list beside reader, two columns.
   *   - "bottom" — the same two panes stacked, list above and reader below.
   *   - "none"   — no split at all: opening a message REPLACES the list, and
   *                `u` (or the reader's close button) brings it back.
   *
   * The decision itself is `paneLayout` in `mail/prefs.ts` — a pure function
   * with its own tests, because the "none" rule (unmount the list, never hide
   * it) is a real invariant and this component cannot be rendered in a unit
   * test without auth, a router, a JMAP client and an EventSource.
   */
  const layout = paneLayout(prefs.readingPane, isReading);
  const { listHidden } = layout;

  /** E9: what the pill says, if anything. */
  const connection = connectionState({ online: offline.isOnline, streamDead });
  /** E9: the Outbox appears only when it holds something (Gmail's shape). */
  const outboxVisible = showsOutbox(offline.outboxItems);
  const inOutbox = route.kind === "outbox";

  return (
    <div className={styles.shell}>
      <TopBar
        branding={branding}
        username={username}
        onSignOut={signOut}
        sidebarCollapsed={sidebarCollapsed}
        onToggleSidebar={toggleSidebar}
        onOpenHelp={() => {
          setHelpOpen(true);
        }}
        /*
         * The gear opens the QUICK panel, and the panel's "See all settings"
         * opens the full surface — Gmail's two-step shape (canon 07 §1, §4).
         * It TOGGLES rather than only opening, because a gear that does
         * nothing when the panel it opened is already showing reads as broken.
         */
        onOpenQuickSettings={() => {
          setQuickSettingsOpen((open) => !open);
        }}
        quickSettingsOpen={quickSettingsOpen}
      >
        <SearchBar
          ref={searchInputRef}
          value={searchText}
          onChange={setSearchText}
          onSearch={runSearch}
          isSearching={isLoadingList && route.kind === "search"}
          /* E3: the suggestion sources and the options panel's folder list. */
          recentSearches={recentSearches}
          labels={sidebarLabels}
          mailboxes={mailboxes}
          onClearRecent={clearRecentSearches}
        />
      </TopBar>

      {/*
        E6: the vacation banner (canon §2.8) — between the header and the panes,
        spanning the whole screen, which is Gmail's own placement.

        Above the body rather than inside the list column on purpose: the fact
        it reports is about the ACCOUNT, not about the folder being viewed, and
        it has to be equally visible with the reader open. It renders nothing
        unless the responder is enabled AND today is inside its window.
      */}
      {vacationSettings !== undefined && (
        <VacationBanner
          vacation={filtersApi.vacation}
          onEndNow={() => filtersApi.saveVacationResponse({ isEnabled: false })}
        />
      )}

      <div
        className={[
          styles.body,
          layout.mode === "right" ? styles.reading : "",
          layout.mode === "bottom" ? styles.readingBottom : "",
          layout.mode === "full" ? styles.readingFull : "",
          /*
           * E12: the hamburger's effect is ONE class on the grid, not a
           * conditional render of a different sidebar. The rail stays mounted
           * and keeps its scroll position, its selection and its ARIA tree —
           * unmounting it would drop a screen-reader user's place in the
           * folder list every time the layout changed width.
           */
          sidebarCollapsed ? styles.railCollapsed : "",
          /*
           * B2: the dock's own track. The panel renders `null` when closed, so
           * a permanent track would leave a `0fr` column and a stray border;
           * appending the column only while it is open keeps every other
           * layout byte-identical to what it was.
           */
          quickSettingsOpen ? styles.quickOpen : "",
        ]
          .filter(Boolean)
          .join(" ")}
      >
        <nav className={styles.sidebar} aria-label={t("shell.mailboxes")}>
          {mailboxError !== undefined ? (
            <div className={styles.sidebarError}>
              <p>{t("mailbox.loadFailed")}</p>
            </div>
          ) : (
            <MailboxList
              mailboxes={mailboxes}
              selectedId={activeMailbox?.id}
              onSelect={goToMailbox}
              isLoading={isLoadingMailboxes}
              /*
               * E2 item 7. The affordance appears on the Trash row and ONLY
               * while Trash is the folder on screen: it is an irreversible
               * bulk destroy, and a permanently visible button for it in a
               * sidebar is a mis-click waiting to happen.
               *
               * 30-day retention is Dovecot's/Mailcow's expunge policy, not a
               * timer of ours (spec E2) — this is the manual "now".
               */
              onEmptyTrash={inTrash ? (trash) => { void confirmEmptyTrash(trash); } : undefined}
              isEmptyingTrash={isEmptyingTrash}
              /*
               * E9: the Outbox, as a VIRTUAL folder the list draws only when
               * it holds something — which is Gmail's own shape (canon §2.10)
               * and the right one: a permanently visible Outbox that is always
               * empty is a control that means nothing 99% of the time.
               */
              {...(outboxVisible
                ? {
                    outbox: {
                      count: pendingCount(offline.outboxItems),
                      hasFailures: offline.outboxItems.some(
                        (item) => item.state === "failed",
                      ),
                      isSelected: inOutbox,
                      onSelect: goToOutbox,
                    },
                  }
                : {})}
              /*
               * E4: Scheduled, on the same "only when it holds something" rule
               * as the Outbox. Snoozed is NOT here and does not need to be: it
               * is a real folder in the tree above, and canon §2.2's `g b`
               * navigation means Gmail shows it always — which a real folder
               * does for free.
               */
              {...(scheduledSends.length > 0
                ? {
                    scheduled: {
                      count: scheduledSends.length,
                      isSelected: inScheduled,
                      onSelect: goToScheduled,
                    },
                  }
                : {})}
              snoozedMailboxName={snoozedFolderName}
              collapsed={sidebarCollapsed}
            />
          )}

          {/*
            E8: the labels, as their OWN group below the folders. GC-5's line
            made visible — folders organise, labels cut across — rather than
            fifteen more rows in a tree the user would expect to file mail into.
          */}
          <LabelList
            labels={sidebarLabels}
            selectedKeyword={
              route.kind === "label" ? encodeLabelKeyword(route.name) : undefined
            }
            onSelect={goToLabel}
            /* E12 (canon 07 §2): the `+` beside the heading. It routes to the
               label manager rather than prompting inline — see LabelListProps. */
            onCreate={openLabelSettings}
            collapsed={sidebarCollapsed}
          />

          {/*
            E12: the bottom-left settings button is GONE.

            It had a stated rationale (Slack/Linear/VS Code put app-level
            controls bottom-left) and it was defensible in isolation. Canon 07
            §1 settles it against the benchmark that governs this epic: Gmail's
            gear is top-right, and "most people can use Gmail blind" is a claim
            about where their hand goes. Two entries would have been worse than
            either — a user who found one would never learn the other — so this
            one is deleted rather than kept as a second door.
          */}
        </nav>

        {/*
          B3: the settings page REPLACES the list area (canon 07 §5).

          The top bar and the rail stay exactly where they are, which is what
          makes settings a destination inside the app rather than a screen the
          app disappears behind — and is the layout Gmail uses. It carries
          `#main` for the same reason the list and the reader trade it: the skip
          link must land on whatever is actually showing.
        */}
        {inSettings && settingsTab !== undefined ? (
          <main className={styles.settingsColumn} id="main">
            <SettingsPage
              tab={settingsTab}
              onSelectTab={goToSettings}
              onClose={leaveSettings}
              onOpenQuickSettings={() => {
                setQuickSettingsOpen(true);
              }}
              identity={identity}
              onSaveSignature={saveSignature}
              labels={labelSettings}
              /*
               * E7: the autocomplete row. Passed only when this browser
               * actually has an index to govern — `offline.addresses` is
               * undefined without usable storage, and a switch over nothing is
               * a dead control.
               */
              {...(offline.addresses !== undefined
                ? {
                    addresses: {
                      enabled: addressIndex.enabled,
                      setEnabled: addressIndex.setEnabled,
                      count: addressIndex.count,
                      clear: addressIndex.clear,
                    },
                  }
                : {})}
              /*
               * E6. Each is undefined when the server does not advertise its
               * capability, which is what makes the page render the honest
               * skeleton rather than a control that cannot work.
               */
              filters={filterSettings}
              blocked={blockedSettings}
              forwarding={forwardingSettings}
              vacation={vacationSettings}
              quota={quotaSettings}
            />
          </main>
        ) : (
          <>
        {/*
          In "No split" the list is UNMOUNTED while a message is open, not
          hidden: a virtualized list in a zero-height container measures a
          viewport of 0 and renders a window of nothing, so returning to it
          would land on an empty list at the wrong scroll offset. `#main` moves
          onto whichever pane is actually showing, so the skip link never
          points at nothing.
        */}
        {!listHidden && (
        <main className={styles.listColumn} id="main">
          {/*
            E9: the connection pill floats over the column (absolutely
            positioned) so it never reflows the virtualized list beneath it.
          */}
          <ConnectionPill state={connection} />

          {/*
            E9: the Outbox is a view, not a filtered list — its rows are queue
            entries with no server identity, so none of the list's machinery
            (selection, keyboard, actions) applies to them. Rendering it here
            rather than as a route of its own keeps the shell, the sidebar and
            the reader exactly where they were.
          */}
          {inOutbox ? (
            <OutboxView
              items={offline.outboxItems}
              onRetry={retryOutboxItem}
              onDiscard={discardOutboxItem}
            />
          ) : inScheduled ? (
            /*
             * E4: the Scheduled view, on the same footing as the Outbox and for
             * the same reason — its rows are `EmailSubmission` records, not
             * `Email`s, so none of the list's machinery (selection, keyboard,
             * actions) applies to them.
             */
            <ScheduledView
              items={scheduledSends}
              onCancel={cancelScheduled}
              onSendNow={sendScheduledImmediately}
              busyId={scheduleBusyId}
              locale={locale}
            />
          ) : (
          <>
          {/*
            E9: the staleness banner. It states WHY the list may be out of date,
            which the pill alone does not — the pill says "offline", this says
            "so what you are looking at is saved mail".
          */}
          {isOfflineMode && mode === "cached" && (
            <div className={styles.noticeWarn} role="status">
              <span>{t("offline.banner.stale")}</span>
            </div>
          )}
          {/*
            E9: offline search results are labelled as covering only the cache.
            Silence here would let a user conclude their mail does not contain
            something it does contain.
          */}
          {isOfflineMode && route.kind === "search" && (
            <div className={styles.noticeInfo} role="status">
              {format("offline.search.label", groups.length)}
            </div>
          )}
          {/*
            E3: the chips row, under the box while a search is active (canon
            §2.5). It holds no state of its own — each chip reads and rewrites
            the query STRING, so it can never disagree with what was searched.
          */}
          {route.kind === "search" && !isOfflineMode && (
            <SearchChips query={route.query} onChange={runSearch} />
          )}
          {/*
            B4: the list's CHROME strip (canon 07 §3), above the verbs.

            Two strips, because Gmail has two and they do different jobs: this
            one acts on the LIST (select by scope, refresh, page), the ActionBar
            below acts on the SELECTION. Merging them would produce one strip
            where half the controls grey out and half do not.

            Offline it renders WITHOUT a pager: the cached window is whatever
            was stored, not a page of a server-side result, and a pager over it
            would offer to fetch a page nothing can fetch.
          */}
          <ListToolbar
            onSelectBy={runSelectBy}
            allSelected={isAllSelected(selection, orderedIds)}
            someSelected={selection.selected.size > 0}
            totalCount={groups.length}
            onRefresh={refresh}
            isRefreshing={isLoadingList}
            {...(isOfflineMode
              ? {}
              : {
                  page: pageState,
                  onNewerPage: () => {
                    setPosition(previousPosition(pageState));
                  },
                  onOlderPage: () => {
                    setPosition(nextPosition(pageState));
                  },
                })}
          />
          <ActionBar
            selectedCount={selection.selected.size}
            onMarkRead={() => {
              runToggleRead(true);
            }}
            onMarkUnread={() => {
              runToggleRead(false);
            }}
            onFlag={runToggleFlag}
            onArchive={runArchive}
            onDelete={() => { void runDelete(); }}
            onMove={runMove}
            mailboxes={mailboxes}
            currentMailboxId={activeMailbox?.id}
            deleteIsPermanent={willDeletePermanently}
            onCompose={openCompose}
            isBusy={actions.isBusy}
            onToggleSpam={runToggleSpam}
            inJunk={inJunk}
            labels={labelsApi.labels}
            labelSelection={labelSelection}
            onToggleLabel={runToggleLabel}
            onManageLabels={openLabelSettings}
            onLabelMenuReady={registerLabelMenu}
            /* E11: the handles behind `,` and `.` (canon §2.7). */
            onToolbarReady={registerToolbar}
            onMoreMenuReady={registerMoreMenu}
            /*
             * E7: forward the selection as `.eml` attachments. Works on the
             * list rows directly — the whole message is downloaded as a blob,
             * so unlike a quoted forward it needs no body to have been fetched.
             */
            onForwardAsAttachment={() => {
              const wanted = new Set(targetMessageIds());
              forwardAsAttachment(projected.filter((email) => wanted.has(email.id)));
            }}
            /*
             * E4: the triage controls, present only when the server advertises
             * the vendor capability. Passing `undefined` removes them entirely
             * rather than greying them out — a feature this server does not
             * have is not a feature waiting for a selection.
             */
            {...(hasTriage
              ? {
                  onSnooze: (until: string) => {
                    runSnooze(targetMessageIds(), until);
                  },
                  onSnoozeMenuReady: registerSnoozeMenu,
                  onToggleMute: runToggleMute,
                  allMuted: allTargetsMuted,
                }
              : {})}
            {...(hasTriage && inSnoozed
              ? {
                  onUnsnooze: () => {
                    runUnsnooze(targetMessageIds());
                  },
                }
              : {})}
          />
          <MessageList
            labels={labelsApi.labels}
            onSelectLabel={goToLabel}
            /* E3: the result highlighting, empty outside a search. */
            snippets={snippetsById}
            listKey={listKey}
            groups={groups}
            selectedId={selectedId}
            selectedIds={selection.selected}
            onToggleSelect={toggleSelect}
            onSelect={(group) => {
              setSelectedId(group.id);
            }}
            onOpen={openGroup}
            isLoading={isLoadingList}
            /*
             * E2 item 5: the per-row hover actions.
             *
             * Each acts on THAT row regardless of the current selection or
             * focus — the pointer already named its target, and routing them
             * through `targetMessageIds()` would archive whatever happened to
             * be selected elsewhere in the list, which is the bug that makes
             * hover actions feel dangerous.
             */
            onRowArchive={(group) => {
              const archiveId = roleMailboxId("archive");
              if (archiveId === undefined) {
                setToast(t("action.failedTitle"));
                return;
              }
              const ids = idsOfGroup(group);
              void dispatchAction(
                { kind: "archive", ids, mailboxId: archiveId },
                format("action.doneArchived", ids.length),
                { undoOrigin: activeMailbox?.id, autoAdvance: true },
              );
            }}
            onRowDelete={(group) => {
              const ids = idsOfGroup(group);
              const permanent =
                trashMailboxId !== undefined &&
                group.messages.every((message) => deleteIsPermanent(message, trashMailboxId));
              void (async () => {
              if (
                permanent &&
                !(await confirm({
                  message: format("action.confirmDeleteForever", ids.length),
                  confirmLabel: t("action.confirm"),
                  destructive: true,
                }))
              ) {
                return;
              }
              void dispatchAction(
                { kind: "delete", ids },
                permanent
                  ? format("action.doneDeletedForever", ids.length)
                  : format("action.doneDeleted", ids.length),
                {
                  undoOrigin: activeMailbox?.id,
                  wasPermanent: permanent,
                  autoAdvance: true,
                },
              );
              })();
            }}
            onRowToggleRead={(group) => {
              const ids = idsOfGroup(group);
              // The row's own state decides the direction, so the icon and
              // what the click does can never disagree.
              const value = group.hasUnread;
              void dispatchAction(
                { kind: value ? "markRead" : "markUnread", ids },
                value ? t("action.markRead") : t("action.markUnread"),
              );
            }}
            /*
             * B4 (canon 07 §3): the row's clickable star.
             *
             * It goes through the SAME `dispatchAction` every other write does,
             * so it paints optimistically and rolls back with the server's own
             * words on failure — the star was already drawn here as a read-only
             * icon, which meant the one gesture every Gmail user makes without
             * looking silently did nothing.
             *
             * Like the hover actions, it acts on THAT row regardless of the
             * selection: the pointer has already named its target. And like
             * them it reads the row's own state for the direction, so the
             * filled/outline icon and what the click does cannot disagree.
             */
            onRowToggleFlag={(group) => {
              const ids = idsOfGroup(group);
              const starred = group.hasFlagged;
              void dispatchAction(
                { kind: starred ? "unflag" : "flag", ids },
                starred ? t("action.unflag") : t("action.flag"),
              );
            }}
            /*
             * E4: the FOURTH hover action, which completes Gmail's set of four
             * (canon §2.2). It is a render prop rather than a callback because
             * it opens a menu, and the menu needs the clock, the i18n and the
             * dispatcher this component holds — the row supplies only the
             * trigger's styling so it looks like its three siblings.
             *
             * It acts on THAT row's conversation regardless of the selection,
             * the same rule the other three follow: the pointer already named
             * its target.
             */
            {...(hasTriage
              ? {
                  renderRowSnooze: (group, triggerClassName) => (
                    <SnoozeMenu
                      disabled={false}
                      triggerClassName={triggerClassName}
                      onSnooze={(until) => {
                        runSnooze(idsOfGroup(group), until);
                      }}
                      triggerContent={
                        <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
                          <circle cx="10" cy="10.5" r="6.8" />
                          <path d="M10 6.8v3.9l2.6 1.6" />
                        </svg>
                      }
                    />
                  ),
                }
              : {})}
            mutedThreadIds={mutedThreadIds}
            {...(inSnoozed
              ? {
                  snoozeUntilById,
                  onRowUnsnooze: (group: ThreadGroup) => {
                    runUnsnooze(idsOfGroup(group));
                  },
                }
              : {})}
            notice={
              <ListNotice
                refusal={refusal}
                truncated={truncated}
                shown={groups.length}
                error={listError}
                total={resultTotal}
                isSearch={route.kind === "search"}
                /* E3: the parse, so the banner can name the term at fault. */
                plan={searchPlan}
              />
            }
            empty={
              <EmptyState
                isSearch={route.kind === "search"}
                labelName={route.kind === "label" ? route.name : undefined}
                query={route.kind === "search" ? route.query : ""}
                hasRefusal={refusal !== undefined}
                /*
                 * E9: offline with nothing stored is its OWN empty state. "No
                 * messages" would be a claim about the mailbox; the truth is a
                 * claim about this device.
                 */
                offlineEmpty={mode === "empty"}
              />
            }
          />
          </>
          )}
        </main>
        )}

        {isReading && client !== undefined && (
          <aside
            className={styles.readerColumn}
            aria-label={t("list.selectMessage")}
            id={listHidden ? "main" : undefined}
          >
            <ReadingPane
              /*
               * E9: offline, the reader falls back to the cached body — which
               * exists only for messages the user actually opened while online.
               */
              email={isOfflineMode ? (cachedDetail ?? detail.email) : detail.email}
              thread={detail.thread}
              /*
               * Never a spinner offline: there is no request in flight to wait
               * for, and a spinner that can never resolve is the worst of the
               * three possible states.
               */
              isLoading={isOfflineMode ? false : isLoadingDetail}
              error={detailError}
              /*
               * The honest per-message state. The list may be rendering happily
               * from cache while THIS message's body was never stored, and that
               * deserves its own explanation rather than an empty pane.
               */
              offlineUnavailable={isOfflineMode && detailUncached}
              onClose={closeMessage}
              client={client}
              accountId={accountId}
              onReply={() => {
                openReply(false);
              }}
              onReplyAll={() => {
                openReply(true);
              }}
              onForward={openForward}
              /*
               * E7: the whole CONVERSATION as attachments, matching what every
               * other toolbar verb in this pane acts on (canon §2.1) — a reply
               * chain forwarded to a lawyer is worth nothing if it carries only
               * the last message.
               */
              onForwardAsAttachment={() => {
                const wanted = new Set(archiveTargetIds);
                const targets = projected.filter((email) => wanted.has(email.id));
                /*
                 * The projected list is the fallback's source, not the only
                 * one: a conversation opened from a search may have members the
                 * current window does not hold, and the message the reader is
                 * showing is always available. It carries `blobId`, which is
                 * all the download needs.
                 */
                const opened = detail.email;
                forwardAsAttachment(
                  targets.length > 0 ? targets : opened === undefined ? [] : [opened],
                );
              }}
              onArchive={runArchive}
              onDelete={() => { void runDelete(); }}
              deleteIsPermanent={willDeletePermanently}
              blobToken={blobToken}
              onToggleFlag={runToggleFlag}
              onMove={runMove}
              onMarkUnread={() => {
                runToggleRead(false);
                // Back to the list: leaving the message open would have the
                // reader immediately re-mark it read, so the button would
                // appear to do nothing at all.
                closeMessage();
              }}
              onToggleSpam={runToggleSpam}
              onUnsubscribeByMail={openUnsubscribeMail}
              /*
               * E6: block the sender. Passed only when the server offers
               * filters — a block writes a Sieve rule, so without the
               * capability the button would have nothing to write.
               */
              {...(filtersApi.capabilities.filters ? { onBlockSender: blockSender } : {})}
              labels={labelsApi.labels}
              onToggleLabel={runToggleLabel}
              onManageLabels={openLabelSettings}
              onSelectLabel={goToLabel}
              mailboxes={mailboxes}
              currentMailboxId={activeMailbox?.id}
              inJunk={inJunk}
              /*
               * E4: the triage verbs in the reader, acting on the whole
               * conversation exactly as archive and delete do —
               * `targetMessageIds()` expands the open thread, which is what
               * canon §2.1 asks of every toolbar action.
               */
              {...(hasTriage
                ? {
                    onSnooze: (until: string) => {
                      runSnooze(targetMessageIds(), until);
                    },
                    onToggleMute: runToggleMute,
                    isMuted: openThreadIsMuted,
                  }
                : {})}
              {...(hasTriage && inSnoozed
                ? {
                    onUnsnooze: () => {
                      runUnsnooze(targetMessageIds());
                    },
                  }
                : {})}
              /*
               * E5 / D-4: the images policy.
               *
               * "always" auto-loads remote images THROUGH the HMAC proxy — the
               * proxy is the precondition that makes Gmail's own default
               * defensible (canon §7.1: the sender learns nothing about the
               * reader). "ask" is the per-message opt-in P2 shipped.
               *
               * Junk is excluded unconditionally and that exclusion is NOT
               * expressed here: `inJunk` already drives `allowRemoteImages` in
               * the reader, and E2's rule that Spam images are unloadable wins
               * over any preference. Passing the policy through the same prop
               * would have made a setting able to override a security stance.
               */
              autoLoadImages={prefs.imagesPolicy === "always"}
              onNextMessage={siblingGroup("next") === undefined ? undefined : () => {
                goToSibling("next");
              }}
              onPreviousMessage={siblingGroup("previous") === undefined ? undefined : () => {
                goToSibling("previous");
              }}
              /*
               * E1 / canon §2.1 — the conversation reader, gated by the E5
               * preference. With it off the pane is exactly the single-message
               * reader that shipped before, on the same code path.
               *
               * The toolbar props above (archive, delete, spam, move, star)
               * are UNCHANGED and act on the whole conversation either way:
               * `targetMessageIds()` already expands the focused thread row to
               * every message id in it, which is what canon §2.1 asks for
               * ("toolbar archive/delete/label act on the conversation").
               */
              conversationView={prefs.conversationView}
              onReplyToMessage={replyToMessage}
              onForwardMessage={forwardMessage}
              onMarkMessagesRead={markMessagesRead}
              onConversationControls={setConversationControls}
            />
          </aside>
        )}
          </>
        )}

        {/*
          B2: quick settings, as the LAST GRID COLUMN.

          Inside the grid, not floating above it, which is the whole design
          (canon 07 §4): the list shrinks to make room and the page stays
          interactive, so you change the density and watch the rows you are
          already looking at change. A modal would hide the very thing every
          option in the panel is about.
        */}
        {quickSettingsOpen && (
          /*
           * The wrapper exists for ONE reason: the "below the list" layout
           * places its panes by named grid AREAS, and a grid area can only be
           * assigned by a rule in the grid's own stylesheet. The panel's class
           * comes from its own CSS module, which this file cannot name. One
           * div owned here is a smaller price than either exporting a class
           * across modules or giving the panel a `gridArea` prop it would
           * otherwise have no business knowing about.
           */
          <div className={styles.quickPanel}>
            <QuickSettingsPanel
              isOpen={quickSettingsOpen}
              onClose={() => {
                setQuickSettingsOpen(false);
              }}
              onOpenFullSettings={() => {
                // The panel closes as the full surface opens: leaving a
                // shrunken list behind the settings page the user is about to
                // read is a layout they never asked for and would have to undo.
                setQuickSettingsOpen(false);
                goToSettings();
              }}
            />
          </div>
        )}
      </div>

      <ShortcutsDialog
        isOpen={helpOpen}
        onClose={() => {
          setHelpOpen(false);
        }}
      />

      {/* E11: the confirm this screen's destructive actions await. */}
      {confirmDialog}

      {composerDraft !== undefined && client !== undefined && (
        <Composer
          /* Keyed by the draft's seed so switching from a reply to a forward
             mounts a FRESH composer rather than reusing one whose local state
             belongs to the previous message. */
          key={composerDraft.seedKey}
          draft={composerDraft}
          client={client}
          accountId={accountId}
          identity={identity}
          draftsMailboxId={roleMailboxId("drafts")}
          sentMailboxId={roleMailboxId("sent")}
          sessionCapabilities={session?.capabilities}
          uploadUrlTemplate={session?.uploadUrl}
          authorization={authorization}
          onClose={() => {
            setComposerDraft(undefined);
            // E7: the forwarded `.eml`s belong to the composer that was
            // carrying them, not to the next one.
            setPendingAttachments(undefined);
          }}
          onNotify={setToast}
          onChanged={refresh}
          {...(pendingAttachments !== undefined
            ? { initialAttachments: pendingAttachments }
            : {})}
          /*
           * E7: recipient autocomplete (canon §2.3).
           *
           * Empty when the user opted out or nothing is indexed yet, which the
           * field reads as "no combobox at all" rather than "an empty popup".
           */
          addressSuggestions={addressIndex.suggestions}
          onRecordAddresses={addressIndex.recordSent}
          /*
           * E7: Send & Archive, offered only for a REPLY that has a
           * conversation to archive and only when there is an Archive folder to
           * archive into. Absent removes the button, per P4 — a control that
           * cannot act must not be on screen.
           *
           * E5 v2 adds the fourth condition: `prefs.sendAndArchive`, which is
           * Gmail's own "Show 'Send & Archive' button in reply" setting. It is
           * the FIRST test rather than the last only for readability; all four
           * are necessary. Note the three structural conditions still apply
           * with the preference on — a preference that says "show it" cannot
           * conjure an Archive folder, and the honest answer when there is
           * nothing to archive into stays "no button".
           */
          {...(prefs.sendAndArchive &&
          isReplyIntent(composerDraft.intent) &&
          archiveTargetIds.length > 0 &&
          roleMailboxId("archive") !== undefined
            ? {
                onSendAndArchive: () => archiveConversation(archiveTargetIds),
                onUndoArchive: () => restoreConversation(archiveTargetIds),
              }
            : {})}
          /*
           * E9: where a send goes when there is no network.
           *
           * Passed only when the queue actually exists — a browser without
           * usable storage gets `undefined`, and the composer then reports the
           * send failure honestly rather than promising an Outbox that cannot
           * hold anything.
           */
          {...(offline.outbox !== undefined ? { onQueueOffline: queueForLater } : {})}
          isOnline={offline.isOnline}
          /*
           * E4: schedule send, offered only when the server advertises the
           * triage capability that carries its horizon. `maxDelayedSend` was 0
           * before E4 and is 30 days now, so a client that assumed a number
           * would have been wrong in both directions — it is read, never
           * guessed.
           */
          {...(hasTriage
            ? { maxDelayedSendSeconds: limits.maxDelayedSendSeconds }
            : {})}
        />
      )}

      {/* A single always-present live region: messages announced when they
          appear, rather than a region inserted together with its own text.

          E2: the Undo button lives INSIDE the same bubble rather than in a
          second toast. Two stacked toasts would fight for the same corner, and
          the undo offer is not a separate message — it is what this message
          lets you do about what just happened. */}
      <div className={styles.toast} role="status" aria-live="polite">
        {toast !== undefined && (
          <span className={styles.toastBubble}>
            {toast}
            {undoEntry !== undefined && (
              <button type="button" className={styles.toastUndo} onClick={runUndo}>
                {`${t("action.undo")} (z)`}
              </button>
            )}
          </span>
        )}
      </div>
    </div>
  );
}

/**
 * True for the two intents Send & Archive applies to (E7, canon §2.3).
 *
 * Gmail's setting is worded "Show 'Send & Archive' button in reply", and a
 * forward is not a reply: it starts a new exchange with someone who was not in
 * the original, and archiving the thread you just forwarded onward is not what
 * pressing send there means. A new message has no conversation at all.
 */
function isReplyIntent(intent: ComposerDraft["intent"]): boolean {
  return intent === "reply" || intent === "replyAll";
}

/** The banner above the list: a refusal, a truncation warning, or a count. */
/**
 * E3: the sentence for one term the parser had to exclude.
 *
 * A `switch` in a named function rather than a ternary chain inside the JSX.
 * That is not only readability: a long conditional-expression chain returning
 * differently-typed calls is one of the shapes that makes the TypeScript
 * checker allocate heavily, and this component sits in a 3,000-line file that
 * was already close to the limit on a modest machine. Two small functions cost
 * nothing at runtime and keep the incremental build inside its budget.
 */
function refusedTermMessage(
  term: UnsupportedTerm,
  format: ReturnType<typeof useTranslation>["format"],
): string {
  switch (term.reason) {
    case "negationUnanswerable":
      return format("search.refused.negation", term.operator);
    case "deferredOperator":
      return format("search.refused.deferred", term.operator);
    case "badValue":
      return format("search.refused.badValue", term.operator);
  }
}

/** E3: the sentence for one reason the planner refused the whole query. */
function problemMessage(
  problem: FilterProblem,
  t: ReturnType<typeof useTranslation>["t"],
  format: ReturnType<typeof useTranslation>["format"],
): string {
  switch (problem.code) {
    case "needsTextOrFolder":
      return t("search.problem.needsTextOrFolder");
    case "labelNeedsText":
      return t("search.problem.labelNeedsText");
    case "unknownMailbox":
      return format("search.problem.unknownMailbox", problem.detail ?? "");
    case "tooManyBranches":
      return format("search.problem.tooManyBranches", Number(problem.detail ?? 0));
    case "branchNotAnswerable":
      return format("search.problem.branchNotAnswerable", Number(problem.detail ?? 0));
  }
}

function ListNotice({
  refusal,
  truncated,
  shown,
  error,
  total,
  isSearch,
  plan,
}: {
  readonly refusal: string | undefined;
  readonly truncated: boolean;
  readonly shown: number;
  readonly error: string | undefined;
  readonly total: number | undefined;
  readonly isSearch: boolean;
  /** E3: the parsed plan, so a refusal can name the term at fault. */
  readonly plan?: FilterPlan | undefined;
}): React.JSX.Element | null {
  const { t, format } = useTranslation();

  if (error !== undefined) {
    return (
      <div className={styles.noticeError} role="alert">
        <strong>{t("list.loadFailed")}</strong>
        <span>{error}</span>
      </div>
    );
  }

  /*
   * E3: the refused terms, EACH BY NAME.
   *
   * The banner used to say "this server cannot answer that search" and stop
   * there, which leaves a user editing their query at random. Now every term
   * the parser excluded and every reason the planner refused gets its own
   * line, so the fix is visible: remove that operator, add a word, name a
   * folder.
   *
   * This runs whether or not the SERVER refused, because most refusals are now
   * caught before the request — the round trip is spent only on shapes we
   * believed were answerable.
   */
  const problems = plan?.problems ?? [];
  const unsupported = plan?.unsupported ?? [];
  if (refusal !== undefined || problems.length > 0 || unsupported.length > 0) {
    return (
      <div className={styles.noticeWarn} role="status">
        <strong>
          {problems.length > 0 || unsupported.length > 0
            ? t("search.refused.title")
            : t("search.unsupported")}
        </strong>
        {unsupported.map((term) => (
          <span key={`${term.reason}:${term.raw}`}>
            {refusedTermMessage(term, format)}
          </span>
        ))}
        {problems.map((problem) => (
          <span key={`${problem.code}:${problem.detail ?? ""}`}>
            {problemMessage(problem, t, format)}
          </span>
        ))}
        {/* The server's own words, when it was the one to refuse. */}
        {refusal !== undefined && problems.length === 0 && unsupported.length === 0 && (
          <span>{t("search.unsupportedBody")}</span>
        )}
      </div>
    );
  }

  /*
   * E3: the widening, stated rather than hidden.
   *
   * `from:` and `subject:` share one tsvector on this server, so combining
   * them searches the whole message. That is a real difference from what the
   * user asked for, and the row that says so is what keeps an over-matching
   * result from reading as a bug.
   */
  const folded = plan?.approximations.find((item) => item.code === "fieldsFoldedIntoText");

  /*
   * E4: `is:muted` narrowed the PAGE, not the search — and says so.
   *
   * This is the only predicate in the app that the server does not answer, and
   * the difference is user-visible: a muted conversation past the 200-row
   * window is not found. Stating it here is the same discipline the truncation
   * banner and the folded-fields row already apply — the alternative is a
   * result count the user has no way to reconcile with what they asked for.
   *
   * It comes BEFORE the truncation branch precisely because the two together
   * are the worst case, and the mute caveat is the one the user cannot guess.
   */
  if (plan?.mutedOnly !== undefined) {
    return (
      <div className={styles.noticeInfo} role="status">
        <span>{t("mute.clientSideNotice")}</span>
        {truncated && (
          <span>
            {isSearch ? format("list.truncatedSearch", shown) : format("list.truncated", shown)}
          </span>
        )}
      </div>
    );
  }

  if (truncated) {
    return (
      <div className={styles.noticeInfo} role="status">
        {isSearch ? format("list.truncatedSearch", shown) : format("list.truncated", shown)}
      </div>
    );
  }

  if (isSearch && (total !== undefined || folded !== undefined)) {
    return (
      <div className={styles.noticeCount}>
        {total !== undefined && total > 0 && (
          <span>{format("search.resultCount", total)}</span>
        )}
        {plan?.includesEverything === true && <span>{t("search.scopeEverything")}</span>}
        {folded !== undefined && (
          <span>{format("search.approximate.folded", folded.fields.join(", "))}</span>
        )}
      </div>
    );
  }

  return null;
}

function EmptyState({
  isSearch,
  query,
  hasRefusal,
  labelName,
  offlineEmpty = false,
}: {
  readonly isSearch: boolean;
  readonly query: string;
  readonly hasRefusal: boolean;
  /** E8: the label being viewed, when the route is a label view. */
  readonly labelName?: string | undefined;
  /** E9: offline with nothing cached — a fact about the device, not the mailbox. */
  readonly offlineEmpty?: boolean;
}): React.JSX.Element | null {
  const { t, format } = useTranslation();
  const branding = useBranding();

  // A refusal has its own banner; a second "nothing found" beneath it would
  // contradict it.
  if (hasRefusal) return null;

  /*
   * E9: offline with an empty cache takes priority over every other wording.
   * Saying "this folder has no messages" here would be a lie about the server,
   * when the truth is that this device has not stored anything yet.
   */
  if (offlineEmpty) {
    return (
      <div className={styles.empty}>
        <BrandMark branding={branding} size="lg" iconOnly />
        <p className={styles.emptyTitle}>{t("offline.empty.title")}</p>
        <p className={styles.emptyBody}>{t("offline.empty.body")}</p>
      </div>
    );
  }

  /*
   * E8: a label view's empty state names the LABEL. "This folder has no
   * messages" would be wrong twice over — a label is not a folder, and the
   * account is not empty; nothing carries this label yet, which is a different
   * and actionable fact.
   */
  const title = labelName !== undefined
    ? t("list.emptyLabel")
    : isSearch
      ? t("list.emptySearch")
      : t("list.empty");
  const body = labelName !== undefined
    ? format("list.emptyLabelBody", labelName)
    : isSearch
      ? format("list.emptySearchBody", query)
      : t("list.emptyBody");

  return (
    <div className={styles.empty}>
      <BrandMark branding={branding} size="lg" iconOnly />
      <p className={styles.emptyTitle}>{title}</p>
      <p className={styles.emptyBody}>{body}</p>
    </div>
  );
}
