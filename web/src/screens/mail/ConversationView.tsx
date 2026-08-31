import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import type { JmapClient } from "../../api/jmap";
import { fetchConversationMessages, fetchThreadRows } from "../../mail/api";
import {
  adjacentMessage,
  bodiesToFetch,
  collapseAll,
  conversationOrder,
  EMPTY_CONVERSATION,
  expandAll,
  initialExpanded,
  isAllExpanded,
  messagesToMarkRead,
  toggleExpanded,
  withMarked,
  type ConversationState,
} from "../../mail/conversation";
import { isSuspicious, type Email, type Thread } from "../../mail/types";
import { ConversationMessage } from "./ConversationMessage";
import type { SignImageUrls } from "./SecureHtmlBody";
import styles from "./ConversationView.module.css";

/**
 * The conversation, rendered (L3 epic E1, canon §2.1 — Gmail's defining
 * behavior).
 *
 * Opening a message opens the WHOLE conversation: every message of the thread,
 * oldest at the top, all collapsed except the newest and anything unread.
 *
 * # What this component owns, and what it deliberately does not
 *
 * It owns the FETCHING and the expansion state. It does not own the toolbar,
 * the star, the move menu or any conversation-wide verb — those stay in
 * {@link ReadingPane}, which already expands a thread to all its message ids
 * for actions. That split is canon §2.1's own: "Reply/reply-all/forward are
 * per-message; toolbar archive/delete/label act on the conversation."
 *
 * # The fetch, in two stages, and why
 *
 * Stage 1 asks for the thread's ROWS — the same narrow property set a list row
 * uses, for every message id in the thread. That is one request and it is what
 * makes the collapsed rows renderable: sender, preview, date, keywords.
 *
 * Stage 2 asks for BODIES, and only for messages that are expanded and do not
 * have one yet ({@link bodiesToFetch}). The pilot's largest real thread has 24
 * messages; pulling 24 bodies to render the one the user opened would be the
 * most expensive way to be wrong about this feature, and `maxBodyValueBytes`
 * makes each of them potentially half a megabyte.
 *
 * The two stages are separate requests rather than one because their property
 * sets differ by an order of magnitude in cost — that is the same reason
 * LIST_PROPERTIES and DETAIL_PROPERTIES are separate constants.
 *
 * # Read-marking
 *
 * Only messages that actually EXPAND are marked read (canon §2.1). The effect
 * that issues it is guarded by the `marked` set in the state machine, so it is
 * idempotent against its own optimistic update — without that it would loop.
 */

export interface ConversationViewProps {
  /** The message the route named — always expanded, whatever its age. */
  readonly openEmail: Email;
  /** The thread, from the same `Email/get` + `Thread/get` batch. */
  readonly thread: Thread | undefined;
  readonly client: JmapClient;
  readonly accountId: string;
  /** The `blob`-scoped token each message's attachment downloads need. */
  readonly blobToken?: string | undefined;
  readonly signImageUrls: SignImageUrls;
  readonly allowRemoteImages: boolean;
  readonly autoLoadImages: boolean;
  /** Per-message composition — the caller supplies the message to act on. */
  readonly onReply: (email: Email, all: boolean) => void;
  readonly onForward: (email: Email) => void;
  /** Marks the given messages read (canon §2.1: only what was expanded). */
  readonly onMarkRead: (ids: readonly string[]) => void;
  /**
   * Publishes the conversation's controls so the pane can drive them from the
   * keyboard (`;`, `:`, `p`, `n`). A ref-shaped callback rather than props
   * flowing down, because the keyboard lives at the top of the screen and the
   * state lives here — and lifting the state would put a fetch-owning reducer
   * into MailScreen, which is exactly what this component exists to avoid.
   */
  readonly onControls?: (controls: ConversationControls | undefined) => void;
}

/** What the keyboard can ask of an open conversation. */
export interface ConversationControls {
  readonly expandAll: () => void;
  readonly collapseAll: () => void;
  /** `p` / `n`: move to the previous/next message INSIDE the conversation. */
  readonly goToMessage: (direction: "next" | "previous") => void;
}

/** How many bodies to request in one batch — Bulwark's `batched()` lesson. */
const BODY_BATCH = 5;

export function ConversationView({
  openEmail,
  thread,
  client,
  accountId,
  blobToken,
  signImageUrls,
  allowRemoteImages,
  autoLoadImages,
  onReply,
  onForward,
  onMarkRead,
  onControls,
}: ConversationViewProps): React.JSX.Element {
  const { t, format } = useTranslation();

  /*
   * The thread's messages, keyed by id.
   *
   * A Map rather than an array because both stages MERGE into it: stage 1 puts
   * rows in, stage 2 replaces them with the same message plus its body. Merging
   * into a keyed store is what lets a body arrive without disturbing the order
   * or re-rendering the messages around it.
   */
  const [members, setMembers] = useState<ReadonlyMap<string, Email>>(
    () => new Map([[openEmail.id, openEmail]]),
  );
  const [state, setState] = useState<ConversationState>(EMPTY_CONVERSATION);
  const [rowsError, setRowsError] = useState<string | undefined>(undefined);
  /** Ids whose body request is in flight, so it is not issued twice. */
  const inFlight = useRef<Set<string>>(new Set());

  const threadId = thread?.id ?? openEmail.threadId;
  const memberIds = useMemo(
    () => thread?.emailIds ?? [openEmail.id],
    [thread?.emailIds, openEmail.id],
  );

  /*
   * A NEW conversation resets everything.
   *
   * Keyed on the thread id and the open message: navigating within the same
   * thread (`p`/`n`, or a click on a collapsed row) must NOT refetch or reset
   * the expansion the user has built up, while moving to another conversation
   * must not inherit it.
   */
  useEffect(() => {
    inFlight.current = new Set();
    setMembers(new Map([[openEmail.id, openEmail]]));
    setState(EMPTY_CONVERSATION);
    setRowsError(undefined);
    // Deliberately keyed on the THREAD, not the open message: see above.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [threadId]);

  // The open message is always merged in — it arrives with its body already,
  // from the reader's own `Email/get`, so it must not be overwritten by a
  // bodyless row from stage 1.
  useEffect(() => {
    setMembers((current) => {
      const next = new Map(current);
      next.set(openEmail.id, openEmail);
      return next;
    });
  }, [openEmail]);

  // --- stage 1: the thread's rows ------------------------------------------

  useEffect(() => {
    if (memberIds.length <= 1) return undefined;
    const controller = new AbortController();
    void (async () => {
      try {
        const rows = await fetchThreadRows(client, accountId, memberIds, controller.signal);
        if (controller.signal.aborted) return;
        setMembers((current) => {
          const next = new Map(current);
          for (const row of rows) {
            const existing = next.get(row.id);
            /*
             * A row NEVER clobbers a message that already has a body: the
             * merge keeps the richer record and takes only the fields the row
             * can refresh. Overwriting would blank the body of the message the
             * user is reading the moment the thread's rows land.
             */
            next.set(
              row.id,
              existing?.bodyValues === undefined ? row : { ...existing, ...row, bodyValues: existing.bodyValues },
            );
          }
          return next;
        });
        setRowsError(undefined);
      } catch (error) {
        if (controller.signal.aborted) return;
        // A failed thread fetch degrades to the single message, which is
        // exactly the pre-E1 behavior — never a broken pane.
        setRowsError(error instanceof Error ? error.message : String(error));
      }
    })();
    return () => {
      controller.abort();
    };
  }, [client, accountId, memberIds]);

  /** The thread's messages as an array, in reading order (oldest first). */
  const ordered = useMemo(
    () => conversationOrder([...members.values()]),
    [members],
  );

  // The initial expansion, computed once the rows are in. It runs when the set
  // of member ids changes, which is exactly when "what is unread in this
  // thread" becomes knowable.
  const seededFor = useRef<string | undefined>(undefined);
  useEffect(() => {
    const key = threadId;
    if (key === undefined) return;
    // Seed only when the full membership is present, or when there is only one
    // message — otherwise the seed would be computed from the single message
    // the reader opened with and would never see the thread's unread ones.
    if (members.size < memberIds.length) return;
    if (seededFor.current === key) return;
    seededFor.current = key;
    setState((current) => ({
      ...current,
      expanded: initialExpanded([...members.values()], openEmail.id),
    }));
  }, [threadId, members, memberIds.length, openEmail.id]);

  // --- stage 2: bodies for what is expanded --------------------------------

  useEffect(() => {
    const wanted = bodiesToFetch(state, ordered, inFlight.current).slice(0, BODY_BATCH);
    if (wanted.length === 0) return undefined;
    for (const id of wanted) inFlight.current.add(id);

    const controller = new AbortController();
    void (async () => {
      try {
        const full = await fetchConversationMessages(
          client,
          accountId,
          wanted,
          controller.signal,
        );
        if (controller.signal.aborted) return;
        setMembers((current) => {
          const next = new Map(current);
          for (const message of full) {
            next.set(message.id, { ...next.get(message.id), ...message });
          }
          return next;
        });
      } catch {
        /*
         * A body that failed to load is released from the in-flight set so a
         * later expand can retry it. It is NOT surfaced as a pane-level error:
         * the other messages of the thread are fine, and the failed one shows
         * its loading line — which is honest, since we will try again.
         */
        for (const id of wanted) inFlight.current.delete(id);
      }
    })();
    return () => {
      controller.abort();
    };
  }, [state, ordered, client, accountId]);

  // --- read-marking: only what expanded (canon §2.1) -----------------------

  useEffect(() => {
    const ids = messagesToMarkRead(state, ordered);
    if (ids.length === 0) return;
    setState((current) => withMarked(current, ids));
    onMarkRead(ids);
  }, [state, ordered, onMarkRead]);

  // --- the controls the keyboard drives ------------------------------------

  /** The message `p`/`n` moves from: the last one the user expanded. */
  const [currentId, setCurrentId] = useState<string | undefined>(openEmail.id);

  const containerRef = useRef<HTMLDivElement | null>(null);

  /**
   * Brings a message into view.
   *
   * `CSS.escape` is not decoration: a JMAP id is server-chosen and this
   * builds a selector out of it, which is an injection the moment an id
   * contains a quote or a bracket. jsdom implements it; the guard is for the
   * environments that do not.
   */
  const scrollTo = useCallback((id: string): void => {
    const container = containerRef.current;
    if (container === null) return;
    const escaped = typeof CSS !== "undefined" && typeof CSS.escape === "function"
      ? CSS.escape(id)
      : undefined;
    if (escaped === undefined) return;
    const element = container.querySelector(`[data-message-id="${escaped}"]`);
    if (!(element instanceof HTMLElement)) return;
    /*
     * Scrolling is a COURTESY, and it must never be able to abort the
     * navigation that asked for it. `scrollIntoView` is absent in jsdom and in
     * some embedded webviews, and an exception here would leave `p`/`n` having
     * expanded a message and then thrown on the way to showing it — the state
     * changed, the UI did not, and the error surfaces nowhere near the cause.
     */
    if (typeof element.scrollIntoView !== "function") return;
    try {
      element.scrollIntoView({ block: "nearest", behavior: "smooth" });
    } catch {
      // Older engines reject the options object; the position is not worth a
      // second attempt with different arguments.
    }
  }, []);

  const goToMessage = useCallback(
    (direction: "next" | "previous"): void => {
      const target = adjacentMessage(ordered, currentId, direction);
      // At the ends `adjacentMessage` returns nothing rather than wrapping, so
      // `n` on the newest message simply does nothing — which is what the
      // state machine's own test pins.
      if (target === undefined) return;
      setCurrentId(target.id);
      /*
       * Moving to a message EXPANDS it. That is what "go to the next message"
       * means inside a conversation: a `p`/`n` that only moved a highlight
       * between closed rows would be a cursor with nothing to read.
       */
      setState((current) =>
        current.expanded.has(target.id) ? current : toggleExpanded(current, target.id),
      );
      scrollTo(target.id);
    },
    [ordered, currentId, scrollTo],
  );

  const controls = useMemo<ConversationControls>(
    () => ({
      expandAll: () => {
        setState((current) => expandAll(current, ordered));
      },
      collapseAll: () => {
        setState((current) => collapseAll(current, ordered));
      },
      goToMessage,
    }),
    [ordered, goToMessage],
  );

  useEffect(() => {
    onControls?.(controls);
    return () => {
      onControls?.(undefined);
    };
  }, [controls, onControls]);

  // --- render ---------------------------------------------------------------

  const allExpanded = isAllExpanded(state, ordered);

  return (
    <div className={styles.conversation} ref={containerRef}>
      {/*
        The conversation's own header strip: how many messages, and the
        expand/collapse-all pair that `;` and `:` also drive. It renders only
        for a real conversation — a single message must not grow a control that
        says "1 message" and a button that does nothing.
      */}
      {ordered.length > 1 && (
        <div className={styles.conversationBar}>
          <span className={styles.count}>{format("reader.threadContext", ordered.length)}</span>
          <button
            type="button"
            className={styles.expandToggle}
            onClick={() => {
              if (allExpanded) controls.collapseAll();
              else controls.expandAll();
            }}
            aria-expanded={allExpanded}
          >
            {allExpanded ? t("reader.collapseAll") : t("reader.expandAll")}
          </button>
        </div>
      )}

      {/* A thread whose rows failed to load still shows the message the user
          opened; saying so beats a silently one-message "conversation". */}
      {rowsError !== undefined && ordered.length < memberIds.length && (
        <p className={styles.threadError} role="status">
          {t("reader.threadLoadFailed")}
        </p>
      )}

      {ordered.map((message) => (
        <ConversationMessage
          key={message.id}
          email={message}
          isExpanded={state.expanded.has(message.id)}
          isCurrent={ordered.length > 1 && message.id === currentId}
          onToggle={() => {
            setCurrentId(message.id);
            setState((current) => toggleExpanded(current, message.id));
          }}
          onReply={() => {
            onReply(message, false);
          }}
          onReplyAll={() => {
            onReply(message, true);
          }}
          onForward={() => {
            onForward(message);
          }}
          signImageUrls={signImageUrls}
          /*
           * E10 (canon §4.1.15): the folder-level rule (Junk, the prop) AND
           * this message's own scanner verdict. Per message, because a thread
           * is a mixed bag: the flagged solicitation and the clean reply that
           * quoted it must not share a fate — suppressing the whole thread
           * would punish the reply, and unlocking it would fetch for the spam.
           */
          allowRemoteImages={allowRemoteImages && !isSuspicious(message)}
          autoLoadImages={autoLoadImages}
          client={client}
          accountId={accountId}
          blobToken={blobToken}
        />
      ))}
    </div>
  );
}
