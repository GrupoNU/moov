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
import { usePrefs } from "../../mail/PrefsProvider";
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
  /**
   * The message the reader fetched — the one whose body arrived with the
   * route. It seeds the members map; it is NOT, by itself, a reason to expand
   * anything (C-05).
   */
  readonly openEmail: Email;
  /**
   * C-05: the message asked for BY NAME (permalink, search hit,
   * notification), expanded whatever its age. Undefined when the route's id
   * is only a thread row's representative — then Gmail's rule alone decides:
   * the newest and the unread.
   */
  readonly targetMessageId?: string | undefined;
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
   * The inline compose box, when a reply or a forward is open on this
   * conversation (canon 07 §7).
   *
   * It takes the pills' place rather than sitting beside them, which is
   * Gmail's behaviour and the honest one: the pills say "start writing", and
   * once you are writing there is nothing left for them to start. They come
   * back the moment the box is sent or discarded.
   *
   * A node rather than a flag because this component knows nothing about
   * drafts, identities or the send path, and should not begin to — the reader
   * is a reader. The host builds the composer and hands it down.
   */
  readonly inlineCompose?: React.ReactNode;
  /**
   * Publishes the conversation's controls so the pane can drive them from the
   * keyboard (`;`, `:`, `p`, `n`). A ref-shaped callback rather than props
   * flowing down, because the keyboard lives at the top of the screen and the
   * state lives here — and lifting the state would put a fetch-owning reducer
   * into MailScreen, which is exactly what this component exists to avoid.
   */
  readonly onControls?: (controls: ConversationControls | undefined) => void;
  /** C-14: the reader's own addresses, for each message's "para mí". */
  readonly ownAddresses?: readonly string[] | undefined;
}

/**
 * What the keyboard — and, since C-06, the pane's header — can ask of an open
 * conversation, plus the one fact the header's control needs to draw itself.
 */
export interface ConversationControls {
  readonly expandAll: () => void;
  readonly collapseAll: () => void;
  /** `p` / `n`: move to the previous/next message INSIDE the conversation. */
  readonly goToMessage: (direction: "next" | "previous") => void;
  /**
   * C-06: whether every message is expanded — what Gmail's double chevron in
   * the header reflects (`aria-expanded`) and flips. Published here rather
   * than lifted, for the reason the controls themselves are: the state lives
   * with the fetch, and the header lives two components up.
   */
  readonly allExpanded: boolean;
  /** How many messages the conversation holds, for the same header. */
  readonly messageCount: number;
}

/** How many bodies to request in one batch — Bulwark's `batched()` lesson. */
const BODY_BATCH = 5;

export function ConversationView({
  openEmail,
  targetMessageId,
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
  ownAddresses,
  inlineCompose,
}: ConversationViewProps): React.JSX.Element {
  const { t } = useTranslation();
  /*
   * C-09: which reply the bottom pills put FIRST. Read from the provider for
   * the same reason ReadingPane reads it there: one enum reaching one row.
   * Outside a provider it falls back to Gmail's default (plain reply).
   */
  const { prefs } = usePrefs();

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
  /** The newest message — what the bottom reply pills act on (C-09). */
  const newest = ordered[ordered.length - 1];

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
    /*
     * C-05: the TARGET, not the open message, is what forces an expansion.
     * `openEmail` is whichever member the route named — from a thread row
     * that is the row's representative (in Sent, the user's own reply), and
     * expanding it on top of the newest was the "2 of 3 open" defect.
     */
    setState((current) => ({
      ...current,
      expanded: initialExpanded([...members.values()], targetMessageId),
    }));
  }, [threadId, members, memberIds.length, targetMessageId]);

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

  /**
   * The message `p`/`n` moves from: the last one the user expanded.
   *
   * C-05: it starts on the TARGET when there is one and otherwise on the
   * newest — the message that is actually open — rather than on the route's
   * representative, which may be collapsed.
   */
  const [currentId, setCurrentId] = useState<string | undefined>(targetMessageId);
  /*
   * Derived rather than set by an effect: an effect would leave one render in
   * which the membership is complete but the cursor still undefined, and a
   * `p` pressed in that window would start from the oldest message instead
   * of the newest — the kind of race a test catches one run in five.
   */
  const effectiveCurrentId =
    currentId ?? (members.size >= memberIds.length ? newest?.id : undefined);

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
      const target = adjacentMessage(ordered, effectiveCurrentId, direction);
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
    [ordered, effectiveCurrentId, scrollTo],
  );

  const allExpanded = isAllExpanded(state, ordered);

  const controls = useMemo<ConversationControls>(
    () => ({
      expandAll: () => {
        setState((current) => expandAll(current, ordered));
      },
      collapseAll: () => {
        setState((current) => collapseAll(current, ordered));
      },
      goToMessage,
      allExpanded,
      messageCount: ordered.length,
    }),
    [ordered, goToMessage, allExpanded],
  );

  useEffect(() => {
    onControls?.(controls);
    return () => {
      onControls?.(undefined);
    };
  }, [controls, onControls]);

  // --- render ---------------------------------------------------------------

  /*
   * C-06: no header strip of its own any more. "Conversación con N mensajes /
   * Expandir todo" was a sentence and a text button where Gmail has a double
   * chevron at the top right of the subject; that control now lives in the
   * pane's header (ReadingPane), driven by the `allExpanded` this component
   * publishes, and the count became the "N de M" position beside ‹ ›.
   */
  return (
    <div className={styles.conversation} ref={containerRef}>
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
          isCurrent={ordered.length > 1 && message.id === effectiveCurrentId}
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
          ownAddresses={ownAddresses}
        />
      ))}

      {/*
        C-09: the reply pills at the END of the conversation, Gmail's shape —
        outlined, with the verb's glyph, after the last message, where the
        eye is when it finishes reading. They act on the NEWEST message,
        which is what "reply to this conversation" means (and what Gmail's
        bottom pills do); a reply to an older message is its own arrow in its
        own sender line (C-08). They render once the membership is known, so
        they cannot momentarily reply to the wrong message while rows load.

        Reply-all is offered only when the newest message HAD more than one
        party besides the reader — a pill that would produce the same draft as
        "Reply" is a choice with no difference, and Gmail omits it too. The
        E5 `defaultReplyBehavior` preference orders the two, exactly as the
        single-message reader's row does, so the key and the pill agree.
      */}
      {/*
        The inline compose box, at the foot of the thread and IN PLACE OF the
        pills (canon 07 §7).

        Gmail's reply opens exactly here, under the last message, with the
        conversation still readable above it. The pills are not merely hidden
        while it is open — they have nothing left to do, because what they
        start is already started — and they return unchanged when it closes.

        Note the asymmetry, which is Gmail's: the PILLS are pinned to the
        bottom of the pane (sticky — see the stylesheet), but the BOX is in
        the FLOW and scrolls with the content. A pinned compose box would eat
        half the reader and pin the thing you are writing over the thing you
        are answering, which is the opposite of why an inline reply exists.
      */}
      {inlineCompose}

      {inlineCompose === undefined &&
        newest !== undefined &&
        members.size >= memberIds.length && (
        <div className={styles.replyRow} role="group" aria-label={t("action.reply")}>
          {(prefs.defaultReplyBehavior === "replyAll" && hasSeveralRecipients(newest)
            ? (["replyAll", "reply"] as const)
            : hasSeveralRecipients(newest)
              ? (["reply", "replyAll"] as const)
              : (["reply"] as const)
          ).map((verb) => (
            <button
              key={verb}
              type="button"
              className={styles.replyPill}
              onClick={() => {
                onReply(newest, verb === "replyAll");
              }}
            >
              <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
                {verb === "replyAll" ? (
                  <>
                    <path d="M7 5.5L2.5 9.5 7 13.5" />
                    <path d="M11 5.5L6.5 9.5 11 13.5" />
                    <path d="M6.8 9.5h5.2a5.3 5.3 0 0 1 5.3 5.3v.7" />
                  </>
                ) : (
                  <>
                    <path d="M8 5.5L3.5 9.5 8 13.5" />
                    <path d="M3.8 9.5h6.4a5.3 5.3 0 0 1 5.3 5.3v.7" />
                  </>
                )}
              </svg>
              {verb === "replyAll" ? t("action.replyAll") : t("action.reply")}
            </button>
          ))}
          <button
            type="button"
            className={styles.replyPill}
            onClick={() => {
              onForward(newest);
            }}
          >
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M12 5.5l4.5 4-4.5 4" />
              <path d="M16.2 9.5H9.8a5.3 5.3 0 0 0-5.3 5.3v.7" />
            </svg>
            {t("action.forward")}
          </button>
        </div>
      )}
    </div>
  );
}

/**
 * True when replying to everyone would reach someone a plain reply would
 * not: any Cc, or more than one To. The reader's own address is not
 * subtracted here — the composer's reply-all already drops it — so the
 * question is only "is there a second party at all".
 */
function hasSeveralRecipients(message: Email): boolean {
  const to = message.to?.length ?? 0;
  const cc = message.cc?.length ?? 0;
  return to + cc > 1;
}
