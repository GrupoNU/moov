import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { JmapClient, withAccessToken, type BasicCredentials } from "../../api/jmap";
import { TokenManager } from "../../api/tokens";
import { connectPush } from "../../mail/push";
import { useAuth } from "../../auth/AuthProvider";
import { loadSession } from "../../auth/session";
import { useBranding } from "../../branding/BrandingProvider";
import { BrandMark } from "../../components/BrandMark";
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
import {
  fetchMailboxes,
  fetchMessageDetail,
  queryEmails,
  MailApiError,
  type MailFilter,
} from "../../mail/api";
import { deleteIsPermanent, resolveToggle, type MessageAction } from "../../mail/actions";
import { mailboxSegment, resolveMailbox } from "../../mail/mailboxes";
import { isSearchable, normalizeQuery, refusalFor } from "../../mail/search";
import {
  actionTargets,
  EMPTY_SELECTION,
  idsFromHere,
  isAllSelected,
  pruneSelection,
  selectionAfterClick,
  selectionAfterSelectAll,
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
import { destroyMessages, firstFailureMessage } from "../../mail/write";
import { makeChip } from "../../mail/addresses";
import { groupByThread, type ThreadGroup } from "../../mail/threading";
import { KEYWORD_FLAGGED, KEYWORD_SEEN, type Email, type Mailbox, type Thread } from "../../mail/types";
import { fetchIdentities, type Identity } from "../../mail/write";
import { encodeBasicCredentials } from "../../api/jmap";
import { useRouter } from "../../router/RouterProvider";
import { withMessage, type Route } from "../../router/routes";
import { Composer } from "../compose/Composer";
import {
  forwardDraft,
  newDraft,
  replyDraft,
  resumeDraft,
  type ComposerDraft,
  type QuotingStrings,
} from "../compose/composerState";
import { ActionBar } from "./ActionBar";
import { MailboxList } from "./MailboxList";
import { mailboxLabel } from "./mailboxLabels";
import { MessageList } from "./MessageList";
import { ReadingPane } from "./ReadingPane";
import { SearchBar } from "./SearchBar";
import { SettingsDialog } from "../settings/SettingsDialog";
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

export function MailScreen(): React.JSX.Element {
  const { state, signOut } = useAuth();
  const branding = useBranding();
  const { t, format, locale } = useTranslation();
  const { route, navigate, replace } = useRouter();

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

  const [mailboxes, setMailboxes] = useState<readonly Mailbox[]>([]);
  const [mailboxError, setMailboxError] = useState<string | undefined>(undefined);
  const [isLoadingMailboxes, setLoadingMailboxes] = useState(true);

  const [emails, setEmails] = useState<readonly Email[]>([]);
  const [isLoadingList, setLoadingList] = useState(true);
  const [listError, setListError] = useState<string | undefined>(undefined);
  const [truncated, setTruncated] = useState(false);
  const [refusal, setRefusal] = useState<string | undefined>(undefined);
  const [resultTotal, setResultTotal] = useState<number | undefined>(undefined);

  const [selectedId, setSelectedId] = useState<string | undefined>(undefined);
  const [detail, setDetail] = useState<{ email?: Email; thread?: Thread }>({});
  const [isLoadingDetail, setLoadingDetail] = useState(false);
  const [detailError, setDetailError] = useState<string | undefined>(undefined);

  const [searchText, setSearchText] = useState(
    route.kind === "search" ? route.query : "",
  );
  const [helpOpen, setHelpOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [toast, setToast] = useState<string | undefined>(undefined);

  // --- P3 state ------------------------------------------------------------
  const [selection, setSelection] = useState<SelectionState>(EMPTY_SELECTION);
  const [composerDraft, setComposerDraft] = useState<ComposerDraft | undefined>(undefined);
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
  const [isEmptyingTrash, setEmptyingTrash] = useState(false);

  const searchInputRef = useRef<HTMLInputElement | null>(null);

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
        if (debounce !== undefined) clearTimeout(debounce);
        debounce = setTimeout(() => {
          setRefreshToken((token) => token + 1);
        }, 300);
      },
      onDead: () => {
        // The browser gave up on the stream — with this server that means
        // the token died (restart or revocation). A fresh mint reconnects.
        void tokenManagerRef.current?.refreshNow();
      },
    });
    return () => {
      if (debounce !== undefined) clearTimeout(debounce);
      handle.close();
    };
  }, [client, pushToken]);

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
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          setMailboxError(error instanceof Error ? error.message : String(error));
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
  }, [client, accountId, refreshToken]);

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

  /** What the current route asks the server for. */
  const filter = useMemo<MailFilter | undefined>(() => {
    if (route.kind === "search") {
      const query = normalizeQuery(route.query);
      if (!isSearchable(query)) return undefined;
      return { kind: "search", text: query };
    }
    if (activeMailbox === undefined) return undefined;
    return { kind: "mailbox", mailboxId: activeMailbox.id };
  }, [route, activeMailbox]);

  /** Identifies the list, so the virtualizer resets scroll only on a real change. */
  const listKey =
    route.kind === "search" ? `search:${normalizeQuery(route.query)}` : `mailbox:${activeMailbox?.id ?? ""}`;

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
        const page = await queryEmails(client, accountId, filter, {
          signal: controller.signal,
        });
        if (controller.signal.aborted) return;
        setEmails(page.emails);
        setTruncated(page.truncated);
        setResultTotal(page.total);
      } catch (error) {
        if (controller.signal.aborted) return;
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
            setRefusal(error.methodError.description ?? t("search.unsupportedBody"));
            return;
          }
        }
        setEmails([]);
        setListError(error instanceof Error ? error.message : String(error));
      } finally {
        if (!controller.signal.aborted) setLoadingList(false);
      }
    })();

    return () => {
      controller.abort();
    };
  }, [client, accountId, filter, route.kind, t, refreshToken]);

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

  const groups = useMemo(() => groupByThread(projected), [projected]);

  /** Refetches the list from the server after a write. */
  const refresh = useCallback((): void => {
    actions.reset();
    setRefreshToken((token) => token + 1);
  }, [actions]);

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

  const openMessageId = route.messageId;

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
  }, [client, accountId, openMessageId]);

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

  const selectAll = useCallback(
    (all: boolean): void => {
      setSelection(selectionAfterSelectAll(orderedIds, all));
    },
    [orderedIds],
  );

  /**
   * The MESSAGE ids an action applies to.
   *
   * The list is grouped into threads, and a thread row stands for every
   * message in it — so archiving a conversation archives the conversation, not
   * just its newest message, which is what "archive" means in every mail
   * client and what a user who selects one row expects.
   */
  const targetMessageIds = useCallback(
    (): readonly string[] => {
      const groupIds = actionTargets(selection, selectedId);
      const wanted = new Set(groupIds);
      const out: string[] = [];
      for (const group of groups) {
        if (!wanted.has(group.id)) continue;
        for (const message of group.messages) out.push(message.id);
      }
      return out;
    },
    [selection, selectedId, groups],
  );

  // --- P3: running an action ------------------------------------------------

  const roleMailboxId = useCallback(
    (role: string): string | undefined =>
      mailboxes.find((mailbox) => mailbox.role === role)?.id,
    [mailboxes],
  );

  const trashMailboxId = roleMailboxId("trash");

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
       * E2 item 8 — auto-advance's DEFAULT, which in Gmail is "back to the
       * conversation list". Archiving or deleting the message you are reading
       * must not leave its reading pane open showing a message that is no
       * longer in this folder. The opt-in that picks the next/previous message
       * instead arrives with the settings epic; this is only the default.
       */
      if (options.autoAdvance === true && targetedOpenMessage) {
        navigate(withMessage(route, undefined));
      }

      refresh();
    },
    [actions, projected, refresh, t, format, openMessageId, navigate, route],
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

  const runDelete = useCallback((): void => {
    const ids = targetMessageIds();
    if (ids.length === 0) return;
    /*
     * Confirmation is asked for ONLY when the delete is irreversible (W-A2:
     * already in Trash). A confirm on every delete trains people to dismiss
     * it, which is how the one that mattered gets dismissed too.
     */
    if (willDeletePermanently && !window.confirm(format("action.confirmDeleteForever", ids.length))) {
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
  }, [targetMessageIds, willDeletePermanently, dispatchAction, format, activeMailbox?.id]);

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

  // --- E2: undo (`z`) -------------------------------------------------------

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
    if (!isUndoable(entry, Date.now()) || entry?.inverseAction === undefined) {
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
    (trash: Mailbox): void => {
      if (trash.totalEmails === 0) {
        setToast(t("action.emptyTrashEmpty"));
        return;
      }
      if (!window.confirm(format("action.emptyTrashConfirm", trash.totalEmails))) return;
      runEmptyTrash(trash.id);
    },
    [format, t, runEmptyTrash],
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
    },
    [replace],
  );

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
    const ids = groups
      .filter((group) => wanted.has(group.id))
      .flatMap((group) => group.messages.map((message) => message.id));
    void dispatchAction({ kind: "markUnread", ids }, t("action.markUnread"));
  }, [orderedIds, selectedId, groups, dispatchAction, t]);

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
          closeMessage();
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
           * un-dismissable by keyboard — a real defect, and the reason the
           * settings sheet is named explicitly rather than assumed to handle
           * its own Escape the way an unmounted dialog would.
           */
          if (settingsOpen) setSettingsOpen(false);
          else if (helpOpen) setHelpOpen(false);
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
          runDelete();
          break;
        case "toggleRead":
          runToggleRead();
          break;
        case "toggleFlag":
          runToggleFlag();
          break;
        case "compose":
          openCompose();
          break;
        case "reply":
          openReply(false);
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
      settingsOpen,
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
    ],
  );

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      const { action, nextState } = resolveShortcut(
        {
          key: event.key,
          ctrlKey: event.ctrlKey,
          metaKey: event.metaKey,
          altKey: event.altKey,
          shiftKey: event.shiftKey,
          target: event.target,
        },
        keyboardRef.current,
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
  }, [runAction, composerDraft]);

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

  return (
    <div className={styles.shell}>
      <header className={styles.header}>
        <div className={styles.headerBrand}>
          <BrandMark branding={branding} size="sm" />
        </div>

        <SearchBar
          ref={searchInputRef}
          value={searchText}
          onChange={setSearchText}
          onSearch={runSearch}
          isSearching={isLoadingList && route.kind === "search"}
        />

        <div className={styles.headerActions}>
          <button
            type="button"
            className={styles.iconButton}
            onClick={() => {
              setHelpOpen(true);
            }}
            aria-label={t("shortcuts.title")}
            title={`${t("shortcuts.title")} (?)`}
          >
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden="true" focusable="false">
              <circle cx="10" cy="10" r="7.5" />
              <path d="M7.8 7.7a2.2 2.2 0 1 1 2.9 2.1c-.5.2-.8.6-.8 1.1v.4M10 14.2v.1" />
            </svg>
          </button>
          <span className={styles.account} title={username}>
            {format("shell.signedInAs", username)}
          </span>
          <button className={styles.signOut} type="button" onClick={signOut}>
            {t("shell.signOut")}
          </button>
        </div>
      </header>

      <div className={[styles.body, isReading ? styles.reading : ""].filter(Boolean).join(" ")}>
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
              onEmptyTrash={inTrash ? confirmEmptyTrash : undefined}
              isEmptyingTrash={isEmptyingTrash}
            />
          )}

          {/*
            The settings entry point.

            BOTTOM-LEFT, inside the sidebar but after the folder tree and
            visually separated from it — which is the convention (Slack,
            Linear, VS Code, Gmail's own bottom-left rail) for "this acts on
            the APPLICATION, not on the thing the column above lists". Putting
            it in the header instead would have made it a peer of search and
            sign-out; putting it in the tree would have made it look like a
            folder you can open mail in.

            `margin-top: auto` in the stylesheet is what pins it to the bottom
            of the column no matter how few folders the account has, without a
            second scroll container.
          */}
          <div className={styles.sidebarFooter}>
            <button
              type="button"
              className={styles.settingsButton}
              onClick={() => {
                setSettingsOpen(true);
              }}
              aria-haspopup="dialog"
              aria-expanded={settingsOpen}
            >
              <GearIcon />
              <span>{t("settings.open")}</span>
            </button>
          </div>
        </nav>

        <main className={styles.listColumn} id="main">
          <ActionBar
            selectedCount={selection.selected.size}
            totalCount={groups.length}
            allSelected={isAllSelected(selection, orderedIds)}
            onSelectAll={selectAll}
            onMarkRead={() => {
              runToggleRead(true);
            }}
            onMarkUnread={() => {
              runToggleRead(false);
            }}
            onFlag={runToggleFlag}
            onArchive={runArchive}
            onDelete={runDelete}
            onMove={runMove}
            mailboxes={mailboxes}
            currentMailboxId={activeMailbox?.id}
            deleteIsPermanent={willDeletePermanently}
            onCompose={openCompose}
            isBusy={actions.isBusy}
            onToggleSpam={runToggleSpam}
            inJunk={inJunk}
          />
          <MessageList
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
              const ids = group.messages.map((message) => message.id);
              void dispatchAction(
                { kind: "archive", ids, mailboxId: archiveId },
                format("action.doneArchived", ids.length),
                { undoOrigin: activeMailbox?.id, autoAdvance: true },
              );
            }}
            onRowDelete={(group) => {
              const ids = group.messages.map((message) => message.id);
              const permanent =
                trashMailboxId !== undefined &&
                group.messages.every((message) => deleteIsPermanent(message, trashMailboxId));
              if (permanent && !window.confirm(format("action.confirmDeleteForever", ids.length))) {
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
            }}
            onRowToggleRead={(group) => {
              const ids = group.messages.map((message) => message.id);
              // The row's own state decides the direction, so the icon and
              // what the click does can never disagree.
              const value = group.hasUnread;
              void dispatchAction(
                { kind: value ? "markRead" : "markUnread", ids },
                value ? t("action.markRead") : t("action.markUnread"),
              );
            }}
            notice={
              <ListNotice
                refusal={refusal}
                truncated={truncated}
                shown={groups.length}
                error={listError}
                total={resultTotal}
                isSearch={route.kind === "search"}
              />
            }
            empty={
              <EmptyState
                isSearch={route.kind === "search"}
                query={route.kind === "search" ? route.query : ""}
                hasRefusal={refusal !== undefined}
              />
            }
          />
        </main>

        {isReading && client !== undefined && (
          <aside className={styles.readerColumn} aria-label={t("list.selectMessage")}>
            <ReadingPane
              email={detail.email}
              thread={detail.thread}
              isLoading={isLoadingDetail}
              error={detailError}
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
              onArchive={runArchive}
              onDelete={runDelete}
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
              mailboxes={mailboxes}
              currentMailboxId={activeMailbox?.id}
              inJunk={inJunk}
              onNextMessage={siblingGroup("next") === undefined ? undefined : () => {
                goToSibling("next");
              }}
              onPreviousMessage={siblingGroup("previous") === undefined ? undefined : () => {
                goToSibling("previous");
              }}
            />
          </aside>
        )}
      </div>

      <SettingsDialog
        isOpen={settingsOpen}
        onClose={() => {
          setSettingsOpen(false);
        }}
      />

      <ShortcutsDialog
        isOpen={helpOpen}
        onClose={() => {
          setHelpOpen(false);
        }}
      />

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
          }}
          onNotify={setToast}
          onChanged={refresh}
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

/** The banner above the list: a refusal, a truncation warning, or a count. */
/**
 * The gear.
 *
 * Same 20x20 grid, 1.6 stroke and `currentColor` as every other icon in this
 * screen, so it inherits the row's colour and sits at the same optical weight
 * as the folder icons above it. `aria-hidden` because the button already has
 * a visible text label — announcing the icon too would say "settings settings".
 */
function GearIcon(): React.JSX.Element {
  return (
    <svg
      className={styles.settingsIcon}
      viewBox="0 0 20 20"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      <circle cx="10" cy="10" r="2.6" />
      <path d="M10 2.2l1.1 1.9a6.6 6.6 0 0 1 1.7.7l2.1-.5 1.4 2.4-1.5 1.6a6.6 6.6 0 0 1 0 1.4l1.5 1.6-1.4 2.4-2.1-.5a6.6 6.6 0 0 1-1.7.7L10 17.8l-1.1-1.9a6.6 6.6 0 0 1-1.7-.7l-2.1.5-1.4-2.4 1.5-1.6a6.6 6.6 0 0 1 0-1.4L3.7 8.7l1.4-2.4 2.1.5a6.6 6.6 0 0 1 1.7-.7z" />
    </svg>
  );
}

function ListNotice({
  refusal,
  truncated,
  shown,
  error,
  total,
  isSearch,
}: {
  readonly refusal: string | undefined;
  readonly truncated: boolean;
  readonly shown: number;
  readonly error: string | undefined;
  readonly total: number | undefined;
  readonly isSearch: boolean;
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

  if (refusal !== undefined) {
    // The honest degradation the brief demands: never a silent empty list.
    return (
      <div className={styles.noticeWarn} role="status">
        <strong>{t("search.unsupported")}</strong>
        <span>{t("search.unsupportedBody")}</span>
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

  if (isSearch && total !== undefined && total > 0) {
    return <div className={styles.noticeCount}>{format("search.resultCount", total)}</div>;
  }

  return null;
}

function EmptyState({
  isSearch,
  query,
  hasRefusal,
}: {
  readonly isSearch: boolean;
  readonly query: string;
  readonly hasRefusal: boolean;
}): React.JSX.Element | null {
  const { t, format } = useTranslation();
  const branding = useBranding();

  // A refusal has its own banner; a second "nothing found" beneath it would
  // contradict it.
  if (hasRefusal) return null;

  return (
    <div className={styles.empty}>
      <BrandMark branding={branding} size="lg" iconOnly />
      <p className={styles.emptyTitle}>{isSearch ? t("list.emptySearch") : t("list.empty")}</p>
      <p className={styles.emptyBody}>
        {isSearch ? format("list.emptySearchBody", query) : t("list.emptyBody")}
      </p>
    </div>
  );
}
