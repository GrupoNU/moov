import { useCallback, useEffect, useRef, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import type { JmapClient } from "../../api/jmap";
import { signImageProxyUrls } from "../../mail/api";
import { formatFullDate, initialsFor, machineDate } from "../../mail/format";
import { headerSection, unfoldHeaders } from "../../mail/rawMessage";
import { displaySubject, senderLabel } from "../../mail/threading";
import {
  isFlagged,
  isSuspicious,
  type Email,
  type EmailAddress,
  type Mailbox,
  type Thread,
} from "../../mail/types";
import { listIdLabel, unsubscribeInfo, type UnsubscribeInfo } from "../../mail/unsubscribe";
import { useOffline } from "../../offline/OfflineProvider";
import { usePrefs } from "../../mail/PrefsProvider";
import { ConversationView, type ConversationControls } from "./ConversationView";
import { AttachmentList, DownloadOriginalButton } from "./MessageAttachments";
import { MessageBody } from "./MessageBody";
import { LabelChips } from "./LabelChips";
import { LabelMenu } from "./LabelMenu";
import { labelsFor, type Label } from "../../mail/labelStore";
import { mailboxLabel } from "./mailboxLabels";
import { MoveMenu } from "./MoveMenu";
import { PopupMenu } from "./PopupMenu";
import { SnoozeMenu } from "./SnoozeMenu";
import styles from "./ReadingPane.module.css";

/**
 * The reading pane.
 *
 * Everything except the body is here: the subject, the conversation-wide
 * toolbar, the spam banner, and the keyboard path back to the list. What it
 * shows BELOW that depends on one preference:
 *
 *   - `conversationView` ON (the default, canon §2.1's defining behavior) —
 *     {@link ConversationView} renders the WHOLE thread, each message with its
 *     own sender line, body, attachments and per-message reply actions.
 *   - OFF — the single-message reader this pane shipped with: one sender
 *     block, one recipient list, one attachment list, one body.
 *
 * The toolbar at the top acts on the CONVERSATION in both cases (canon §2.1:
 * "toolbar archive/delete/label act on the conversation"), because MailScreen
 * already expands a selected row to every message id in its thread.
 */

export interface ReadingPaneProps {
  readonly email: Email | undefined;
  readonly thread: Thread | undefined;
  readonly isLoading: boolean;
  readonly error: string | undefined;
  /**
   * E9: this message has no cached body and there is no network to fetch one.
   *
   * Distinct from `error` on purpose: nothing failed. The message exists, it is
   * simply not on this device, which is a fact about the cache the user can act
   * on ("reconnect and it will be here") rather than a fault to report.
   */
  readonly offlineUnavailable?: boolean;
  readonly onClose: () => void;
  /** Used for attachment and raw-message downloads, which need auth headers. */
  readonly client: JmapClient;
  readonly accountId: string;
  /**
   * The current `blob`-scoped download token, when one is held. It turns each
   * attachment into a NATIVE `<a download>` — the browser streams the bytes
   * itself instead of the app buffering them through fetch+objectURL. Absent
   * (still minting, or the feature failed), the per-attachment links simply
   * do not render and the whole-message download below still works.
   */
  readonly blobToken?: string | undefined;
  // --- P3: acting on the open message --------------------------------------
  readonly onReply: () => void;
  readonly onReplyAll: () => void;
  readonly onForward: () => void;
  /**
   * E7: forwards THIS message as a `.eml` attachment (canon §2.3).
   *
   * Absent removes the button — P4, no dead controls.
   */
  readonly onForwardAsAttachment?: (() => void) | undefined;
  readonly onArchive: () => void;
  readonly onDelete: () => void;
  /** True when delete ERASES rather than moves to Trash (server rule W-A2). */
  readonly deleteIsPermanent: boolean;

  // --- E2: the completed reader ------------------------------------------
  /** Toggles the star on the open message. */
  readonly onToggleFlag: () => void;
  /** Moves the open message to a chosen folder. */
  readonly onMove: (mailboxId: string) => void;
  /** Marks the open message unread (and returns to the list, per Gmail). */
  readonly onMarkUnread: () => void;
  /** Reports spam, or — when already in Junk — takes it back out. */
  readonly onToggleSpam: () => void;
  /** Opens the composer prefilled from a `mailto:` unsubscribe URI. */
  readonly onUnsubscribeByMail: (to: string, subject: string | undefined, body: string | undefined) => void;

  /**
   * E6: blocks this message's sender (canon §2.2 — "all future emails go to
   * Spam"). Absent when the server has no filter capability, which removes the
   * control rather than disabling it.
   *
   * The address is resolved HERE, from the message the user is looking at, so
   * the host never has to guess which of `from`/`sender` the reader meant.
   */
  readonly onBlockSender?: ((address: string) => void) | undefined;

  // --- E8: labels ----------------------------------------------------------
  /** The known labels, for resolving this message's chips and their colours. */
  readonly labels?: readonly Label[];
  /** Applies or removes one label on the open message. */
  readonly onToggleLabel?: ((keyword: string, apply: boolean) => void) | undefined;
  /** Opens the label manager. */
  readonly onManageLabels?: (() => void) | undefined;
  /** Clicking a chip navigates to that label's view. */
  readonly onSelectLabel?: ((label: Label) => void) | undefined;

  readonly mailboxes: readonly Mailbox[];
  /** The folder being viewed, so the move menu can exclude it. */
  readonly currentMailboxId: string | undefined;
  /**
   * True when the open message is in the mailbox with role `junk`.
   *
   * Two consequences, both from canon §4.1.9: the reader shows the spam
   * banner, and remote images become UNLOADABLE rather than merely blocked —
   * the unblock control is not rendered at all.
   */
  readonly inJunk: boolean;

  // --- E4: snooze and mute (canon §2.2) ---
  /**
   * Snoozes the open conversation. Absent when the server has no triage
   * capability, which removes the control rather than disabling it.
   */
  readonly onSnooze?: ((until: string) => void) | undefined;
  /** Mutes or unmutes the open conversation. */
  readonly onToggleMute?: (() => void) | undefined;
  /**
   * True when the open conversation is muted.
   *
   * Drives BOTH the badge in the header and the wording of the control, so the
   * two can never disagree about the same fact.
   */
  readonly isMuted?: boolean;
  /** E4: brings the open conversation back now, in the Snoozed view. */
  readonly onUnsnooze?: (() => void) | undefined;
  /**
   * E5 / D-4: load remote images without asking (the `imagesPolicy: "always"`
   * pole). Junk still overrides it — see {@link MessageBody}.
   */
  readonly autoLoadImages?: boolean;
  /** Goes to the next/previous message in the list; absent when there is none. */
  readonly onNextMessage: (() => void) | undefined;
  readonly onPreviousMessage: (() => void) | undefined;
  /**
   * C-06: where the open conversation sits in the list — Gmail's "8 de 15.287"
   * beside ‹ ›. `index` is 1-based and absolute (the page's offset is already
   * added); `total` is the server's count when it gave one, and absent shows
   * the index alone rather than a made-up denominator.
   */
  readonly listPosition?: { readonly index: number; readonly total?: number | undefined } | undefined;

  // --- E1: conversation view (canon §2.1) ----------------------------------
  /**
   * Whether the reading pane shows the WHOLE conversation.
   *
   * The `conversationView` preference (E5), which gates the entire feature.
   * False restores exactly the single-message reader that shipped before E1 —
   * not a degraded version of the new one, the same code path.
   */
  readonly conversationView: boolean;
  /**
   * C-05: the message the user asked for BY NAME — a permalink, a search hit,
   * a notification — which the conversation expands whatever its age. Absent
   * when the route's id is merely a thread row's representative, so opening
   * a thread from Sent shows Gmail's set (newest + unread) and not the user's
   * own old reply on top of it.
   */
  readonly targetMessageId?: string | undefined;
  /**
   * Per-message composition inside a conversation (canon §2.1).
   *
   * A reply replies to ONE message — the one whose text it quotes — while the
   * toolbar above acts on the thread. These carry the message so the caller
   * quotes the right one; the propless `onReply`/`onForward` above stay for
   * the single-message reader and for the toolbar.
   */
  readonly onReplyToMessage: (email: Email, all: boolean) => void;
  readonly onForwardMessage: (email: Email) => void;
  /** Marks the messages that were EXPANDED read — never the whole thread. */
  readonly onMarkMessagesRead: (ids: readonly string[]) => void;
  /** Publishes the conversation's keyboard controls (`;`, `:`, `p`, `n`). */
  readonly onConversationControls: (controls: ConversationControls | undefined) => void;
}

export function ReadingPane({
  email,
  thread,
  isLoading,
  error,
  offlineUnavailable = false,
  onClose,
  client,
  accountId,
  onReply,
  onReplyAll,
  onForward,
  onForwardAsAttachment,
  onArchive,
  onDelete,
  deleteIsPermanent,
  blobToken,
  onToggleFlag,
  onMove,
  onMarkUnread,
  onToggleSpam,
  onUnsubscribeByMail,
  onBlockSender,
  labels,
  onToggleLabel,
  onManageLabels,
  onSelectLabel,
  mailboxes,
  currentMailboxId,
  inJunk,
  onSnooze,
  onToggleMute,
  isMuted = false,
  onUnsnooze,
  autoLoadImages = false,
  onNextMessage,
  onPreviousMessage,
  listPosition,
  conversationView,
  targetMessageId,
  onReplyToMessage,
  onForwardMessage,
  onMarkMessagesRead,
  onConversationControls,
}: ReadingPaneProps): React.JSX.Element {
  const { t, format, locale } = useTranslation();
  const [originalOpen, setOriginalOpen] = useState(false);
  /*
   * E9: read here, at the top, because every early return below it is a hook
   * boundary — calling `useOffline` next to the attachment list it serves would
   * be a conditional hook.
   */
  const { isOnline } = useOffline();
  /*
   * E5 v2 `defaultReplyBehavior`, read here for exactly the reason stated
   * above — every early return below is a hook boundary, and lint caught this
   * one when it was first placed beside the buttons it serves.
   *
   * From the provider rather than threaded as a prop, on the same reasoning
   * `isOnline` follows: the reply default reaches one pair of buttons in this
   * file, and a prop would grow an already-long list to carry one enum to one
   * place. `usePrefs` falls back to the defaults outside a provider, so every
   * existing test of this component keeps working unchanged, at Gmail's own
   * default.
   */
  const { prefs } = usePrefs();

  /*
   * C-06: the conversation's controls, kept HERE as well as forwarded to the
   * host. The host needs them for `;`/`:`/`p`/`n`; this pane needs
   * `allExpanded` to draw Gmail's double chevron in its own header, which sits
   * two components above the state that answers it. Same seam, one more
   * reader — no state is lifted.
   */
  const [conversation, setConversation] = useState<ConversationControls | undefined>(undefined);
  const publishControls = useCallback(
    (controls: ConversationControls | undefined): void => {
      setConversation(controls);
      onConversationControls(controls);
    },
    [onConversationControls],
  );

  // The remote-image signer the secure HTML renderer uses (W-A4): the ONLY
  // path by which a message's remote image can ever be fetched, and it goes
  // through our authenticated sign endpoint plus the HMAC proxy.
  const signImages = useCallback(
    (urls: readonly string[]) => signImageProxyUrls(client, urls),
    [client],
  );

  if (isLoading && email === undefined) {
    return (
      <div className={styles.pane}>
        <div className={styles.centered} role="status" aria-live="polite">
          <span className={styles.spinner} aria-hidden="true" />
          <p className={styles.mutedText}>{t("reader.loading")}</p>
        </div>
      </div>
    );
  }

  if (error !== undefined) {
    return (
      <div className={styles.pane}>
        <div className={styles.centered}>
          <p className={styles.errorTitle}>{t("reader.loadFailed")}</p>
          <p className={styles.mutedText}>{error}</p>
        </div>
      </div>
    );
  }

  /*
   * E9: offline, and this message's body was never stored.
   *
   * Checked BEFORE the `email === undefined` branch below, because that branch
   * says "failed to load" — which would be a lie here. Nothing failed: the mail
   * exists on the server and simply is not on this device, and saying exactly
   * that is what lets the user stop trying.
   */
  if (offlineUnavailable) {
    return (
      <div className={styles.pane}>
        <div className={styles.centered}>
          <p className={styles.errorTitle}>{t("offline.body.unavailable")}</p>
          <p className={styles.mutedText}>{t("offline.body.unavailableBody")}</p>
        </div>
      </div>
    );
  }

  if (email === undefined) {
    return (
      <div className={styles.pane}>
        <div className={styles.centered}>
          <p className={styles.errorTitle}>{t("reader.loadFailed")}</p>
        </div>
      </div>
    );
  }

  const subject = displaySubject(email.subject) ?? t("list.noSubject");
  const attachments = email.attachments ?? [];
  /*
   * E10 (canon §4.1.15): the scanner flagged this message but something —
   * a never-spam rule, the deployment's filing threshold — kept it out of
   * Junk. The treatment mirrors Junk exactly where it is a security stance
   * (remote images UNLOADABLE, not merely blocked) and diverges where Junk's
   * treatment is about the folder: the banner offers "report spam" instead
   * of "not spam", and nothing else about the message is degraded.
   *
   * Inside Junk the spam banner already says everything this one would, so
   * the two banners are mutually exclusive by construction.
   */
  const suspicious = isSuspicious(email);
  const remoteImagesAllowed = !inJunk && !suspicious;
  /*
   * Read from the OfflineProvider rather than taken as a prop: this is the only
   * thing in this component that cares about connectivity, and threading a
   * boolean through the reader's already-long prop list to reach one paragraph
   * would cost more than it explains.
   */
  const isOffline = !isOnline;
  const threadSize = thread?.emailIds.length ?? 1;
  const isoDate = machineDate(email.receivedAt);
  const unsubscribe = unsubscribeInfo(email);
  /*
   * C-07: the folder chip(s). One mailbox per message on this server, but the
   * shape is a set (RFC 8621 §4.1.1) and the loop costs nothing. Unknown ids
   * (a mailbox the list has not loaded) simply render no chip rather than a
   * chip with no name.
   */
  const folderChips = Object.keys(email.mailboxIds ?? {})
    .map((id) => mailboxes.find((mailbox) => mailbox.id === id))
    .filter((mailbox): mailbox is Mailbox => mailbox !== undefined)
    .map((mailbox) => ({
      id: mailbox.id,
      name: mailboxLabel(mailbox, t),
      isInbox: mailbox.role === "inbox",
    }));

  return (
    <article
      /*
       * `printRoot` is what the print stylesheet keys on: at print time every
       * other column of the app is hidden and this element becomes the page.
       * Marking it in the markup rather than selecting it by position means a
       * layout change cannot silently break printing.
       */
      className={[styles.pane, styles.printRoot].join(" ")}
      /* A labelled region, so a screen reader user can jump straight to the
       * message they just opened. */
      aria-label={subject}
    >
      <header className={styles.header}>
        <div className={styles.headerTop}>
          <h1 className={styles.subject}>
            {subject}
            {/*
              C-07 / canon 07 §6: the chips beside the subject, Gmail's own
              order — the FOLDER first ("Recibidos ×"), then the labels.

              The folder chip is the one the list row cannot show (the row is
              already in that folder); here it answers "where is this" for a
              conversation reached by search or permalink, and its × on the
              inbox chip is Gmail's exact affordance: remove from Inbox =
              archive, the same verb the toolbar's icon fires.

              The labels are the open message's keywords — the same set the
              list row shows for its representative, in full here
              (`max: Infinity`, no "+N") because the reader has the room.
            */}
            <span className={styles.chipRow}>
              {folderChips.map((chip) => (
                <span key={chip.id} className={styles.folderChip}>
                  <span className={styles.folderChipName}>{chip.name}</span>
                  {chip.isInbox && (
                    <button
                      type="button"
                      className={styles.folderChipRemove}
                      onClick={onArchive}
                      aria-label={format("reader.removeFromFolder", chip.name)}
                      title={format("reader.removeFromFolder", chip.name)}
                    >
                      <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true" focusable="false">
                        <path d="M6 6l8 8M14 6l-8 8" />
                      </svg>
                    </button>
                  )}
                </span>
              ))}
              <LabelChips
                labels={labelsFor(email.keywords, labels ?? [])}
                max={Number.POSITIVE_INFINITY}
                onSelect={onSelectLabel}
              />
            </span>
            {/*
              E4: the muted badge, in WORDS here rather than as the list row's
              icon (canon §2.2).

              The reader has the room the row does not, and mute is a state
              whose consequence a user will not infer from a small icon: it is
              not "this conversation is quiet", it is "its replies will skip
              your inbox entirely". The `title` carries that sentence, and the
              badge sits beside the labels because it belongs to the same
              family — persistent facts ABOUT the conversation, not actions on
              it.
            */}
            {isMuted && (
              <span className={styles.mutedBadge} title={t("mute.badgeExplain")}>
                {t("mute.badge")}
              </span>
            )}
          </h1>
          {/*
            Previous/next before close, in that reading order, because that is
            the order they are reached by Tab and the order they sit in every
            mail client's top-right corner. Each is DISABLED rather than hidden
            at the ends of the list: a control that vanishes moves the two
            beside it, and the close button must not jump under the pointer.
          */}
          <div className={styles.navGroup} role="group" aria-label={t("shortcuts.sectionNavigate")}>
            {/*
              C-06: Gmail's double chevron — expand all / collapse all — at the
              top right, where Gmail puts it. Only for a real conversation: a
              single message must not grow a control that does nothing. The
              state comes from the conversation itself (see `publishControls`),
              so `aria-expanded` and the tooltip can never disagree with what
              the thread shows; `;` and `:` drive the same two functions.
            */}
            {conversationView && conversation !== undefined && conversation.messageCount > 1 && (
              <button
                type="button"
                className={styles.close}
                onClick={() => {
                  if (conversation.allExpanded) conversation.collapseAll();
                  else conversation.expandAll();
                }}
                aria-expanded={conversation.allExpanded}
                aria-label={conversation.allExpanded ? t("reader.collapseAll") : t("reader.expandAll")}
                title={conversation.allExpanded ? `${t("reader.collapseAll")} (:)` : `${t("reader.expandAll")} (;)`}
              >
                <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
                  {conversation.allExpanded ? (
                    <path d="M5 7.5l5 4 5-4M5 13.5l5-4 5 4" transform="translate(0 -0.5)" />
                  ) : (
                    <path d="M5 3.5l5 4 5-4M5 12.5l5 4 5-4" />
                  )}
                </svg>
              </button>
            )}
            {/*
              C-06: "N de M" — the open conversation's place in the list,
              Gmail's shape exactly, between the chevron and the arrows it
              qualifies. The index alone when the server declined to count:
              a denominator we do not have is not a denominator.
            */}
            {listPosition !== undefined && (
              <span className={styles.position} aria-live="polite">
                {listPosition.total === undefined
                  ? listPosition.index.toLocaleString(locale)
                  : format("reader.positionOf", listPosition.index, listPosition.total)}
              </span>
            )}
            <button
              type="button"
              className={styles.close}
              onClick={onPreviousMessage}
              disabled={onPreviousMessage === undefined}
              aria-label={t("action.previous")}
              title={`${t("action.previous")} (k)`}
            >
              <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
                <path d="M13 15.5l-6-5.5 6-5.5" />
              </svg>
            </button>
            <button
              type="button"
              className={styles.close}
              onClick={onNextMessage}
              disabled={onNextMessage === undefined}
              aria-label={t("action.next")}
              title={`${t("action.next")} (j)`}
            >
              <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
                <path d="M7 4.5l6 5.5-6 5.5" />
              </svg>
            </button>
            <button
              type="button"
              className={styles.close}
              onClick={onClose}
              /* The accessible name says where it goes, not what it looks like. */
              aria-label={t("reader.close")}
              title={t("reader.close")}
            >
              <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round">
                <path d="M5.5 5.5l9 9M14.5 5.5l-9 9" />
              </svg>
            </button>
          </div>
        </div>

        {/*
          The Spam banner (canon §4.1.9).
          `role="note"` rather than `alert`: it is a standing property of the
          message, not an event, and an alert would re-interrupt a screen
          reader every time the pane re-renders.
        */}
        {inJunk && (
          <div className={styles.spamBanner} role="note">
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M10 2.6l6.6 3.5v4c0 3.7-2.8 6.4-6.6 7.3-3.8-.9-6.6-3.6-6.6-7.3v-4z" />
              <path d="M10 7v4M10 13.6v.1" />
            </svg>
            <div>
              <p className={styles.spamBannerTitle}>{t("reader.spamBanner")}</p>
              <p className={styles.spamBannerBody}>{t("reader.spamBannerBody")}</p>
            </div>
            <button type="button" className={styles.primaryAction} onClick={onToggleSpam}>
              {t("action.notSpam")}
            </button>
          </div>
        )}

        {/*
          E10 / canon §4.1.15: the suspicious-mail warning. Same `role="note"`
          reasoning as the spam banner — a standing property of the message,
          not an event. The action it offers is the one Gmail's own warnings
          offer: confirm the scanner ("Report spam"), which files the message
          into Junk and teaches the filter (imapsieve reports on the move).
        */}
        {!inJunk && suspicious && (
          <div className={[styles.spamBanner, styles.suspiciousBanner].join(" ")} role="note">
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M10 2.8L1.8 16.6h16.4z" />
              <path d="M10 7.8v4M10 14.4v.1" />
            </svg>
            <div>
              <p className={styles.spamBannerTitle}>{t("reader.suspiciousBanner")}</p>
              <p className={styles.spamBannerBody}>{t("reader.suspiciousBannerBody")}</p>
            </div>
            <button type="button" className={styles.secondaryAction} onClick={onToggleSpam}>
              {t("action.spam")}
            </button>
          </div>
        )}

        {/*
          P0-6: the toolbar, as ONE ROW OF ICONS (canon 07 §6).

          What was here was sixteen text buttons wrapping into two rows. That
          was a deliberate decision — "this pane has the room, and an icon-only
          toolbar is a guessing game the first time someone uses it" — and its
          premise died with P0-1: the reader is a 520px column beside the list,
          not a full-width pane. Sixteen labels in 520px wrap, and the wrap
          MOVES every button whenever a conditional one appears or disappears,
          which is worse for muscle memory than any icon.

          The guessing-game objection is answered rather than dismissed. Every
          icon carries `aria-label` and `title` with the same string, so the
          name is one hover away for a pointer and always present for a screen
          reader — Gmail's own answer, and the one `ActionBar` already uses.

          The order is the canon's: back · archivar · spam · eliminar · marcar
          no leído · posponer · mover · etiquetas · ⋮. Reply, reply-all and
          forward are NOT here: they are the reader's primary verbs and keep
          their words below, where Gmail puts them too.
        */}
        <div className={styles.iconBar} role="toolbar" aria-label={t("action.more")}>
          {/*
            No back arrow HERE, even though canon 07 §6 lists one first.

            Gmail's reader replaces the list, so its ← is the only way back.
            This pane also runs BESIDE the list (the "right" split), where the
            header already carries a close ✕ next to the prev/next arrows —
            and a second control with the same accessible name, three
            centimetres away, is not parity: it is two answers to "how do I get
            out of here", one of which will be the one a user does not press.

            So the canon's ← is the header's ✕, which was already there and
            already keyboard-reachable. The divider below still opens the row,
            because the verbs still start after the navigation cluster.
          */}
          <IconAction label={t("action.archive")} onClick={onArchive}>
            <rect x="2.6" y="3.6" width="14.8" height="3.6" rx="1" />
            <path d="M4 7.2v8a1.4 1.4 0 0 0 1.4 1.4h9.2a1.4 1.4 0 0 0 1.4-1.4v-8M8 10.4h4" />
          </IconAction>

          {/* The label FLIPS inside Junk, so one control covers both directions
              — Gmail's shape, and what `!` does. */}
          <IconAction
            label={inJunk ? t("action.notSpam") : t("action.spam")}
            onClick={onToggleSpam}
          >
            <path d="M10 2.6l6.6 3.5v4c0 3.7-2.8 6.4-6.6 7.3-3.8-.9-6.6-3.6-6.6-7.3v-4z" />
            {inJunk ? <path d="M7.2 9.9l2 2 3.6-3.8" /> : <path d="M10 7v4M10 13.6v.1" />}
          </IconAction>

          {/* The LABEL states which of the two semantics applies (W-A2): one
              word for both promises would be a lie in one of the cases. */}
          <IconAction
            label={deleteIsPermanent ? t("action.deleteForever") : t("action.delete")}
            onClick={onDelete}
            danger={deleteIsPermanent}
          >
            <path d="M3.6 5.6h12.8M8 5.6V4.2a1 1 0 0 1 1-1h2a1 1 0 0 1 1 1v1.4M5.4 5.6l.7 10a1.4 1.4 0 0 0 1.4 1.3h5a1.4 1.4 0 0 0 1.4-1.3l.7-10" />
          </IconAction>

          {/*
            Mark-unread CLOSES the reader, and that is not a shortcut: leaving
            the message open would have the reading pane immediately re-mark it
            read, so the button would appear to do nothing. Gmail returns to
            the list for exactly this reason.
          */}
          <IconAction label={t("action.markUnread")} onClick={onMarkUnread}>
            <rect x="2.8" y="4.5" width="14.4" height="11" rx="1.6" />
            <circle cx="15.4" cy="5.6" r="2.6" fill="currentColor" stroke="none" />
          </IconAction>

          {onSnooze !== undefined && (
            <SnoozeMenu
              disabled={false}
              onSnooze={onSnooze}
              triggerClassName={styles.iconAction}
              triggerContent={
                <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
                  <circle cx="10" cy="10.5" r="6.8" />
                  <path d="M10 6.8v3.9l2.6 1.6" />
                </svg>
              }
            />
          )}

          {/* Only in the Snoozed view, where it is the one thing a user does to
              a row; everywhere else there is nothing to bring back. */}
          {onUnsnooze !== undefined && (
            <IconAction label={t("snooze.unsnooze")} onClick={onUnsnooze}>
              <path d="M3.4 10.5a6.6 6.6 0 1 1 2 4.7" />
              <path d="M3 6.4v4.1h4.1" />
            </IconAction>
          )}

          <span className={styles.iconDivider} aria-hidden="true" />

          <MoveMenu
            mailboxes={mailboxes}
            currentMailboxId={currentMailboxId}
            disabled={false}
            onMove={onMove}
            triggerClassName={styles.iconAction}
            triggerContent={
              <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
                <path d="M2.8 5.4a1.4 1.4 0 0 1 1.4-1.4h3l1.6 2h6a1.4 1.4 0 0 1 1.4 1.4v7.2a1.4 1.4 0 0 1-1.4 1.4H4.2a1.4 1.4 0 0 1-1.4-1.4z" />
              </svg>
            }
          />

          {/* "Label as" beside "Move to" — the pair a user chooses between, and
              the reason canon §2.7 binds `v` and `l` to adjacent keys. */}
          {onToggleLabel !== undefined && onManageLabels !== undefined && (
            <LabelMenu
              labels={labels ?? []}
              selection={[email.keywords]}
              disabled={false}
              onToggle={onToggleLabel}
              onManage={onManageLabels}
              triggerClassName={styles.iconAction}
              triggerContent={
                <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
                  <path d="M3.4 8.6V4.4a1 1 0 0 1 1-1h4.2l7.6 7.6a1.2 1.2 0 0 1 0 1.7l-4.5 4.5a1.2 1.2 0 0 1-1.7 0L3.4 9.6z" />
                  <circle cx="6.9" cy="6.9" r="1.1" fill="currentColor" stroke="none" />
                </svg>
              }
            />
          )}

          {/*
            The overflow. Everything below is a real verb with no icon a person
            would recognise ("forward as attachment", "view original") or one
            used rarely enough that a permanent slot costs more than it earns.
            Text labels in a menu, which is where an unrecognisable glyph
            belongs — the same trade `ActionBar` already makes.
          */}
          <PopupMenu
            label={t("action.more")}
            disabled={false}
            triggerClassName={styles.iconAction}
            triggerContent={
              <svg viewBox="0 0 20 20" fill="currentColor" aria-hidden="true" focusable="false">
                <circle cx="10" cy="4.6" r="1.5" />
                <circle cx="10" cy="10" r="1.5" />
                <circle cx="10" cy="15.4" r="1.5" />
              </svg>
            }
          >
            {(close) => (
              <>
                {/*
                  E2: the star. `aria-pressed` rather than a flipping label,
                  because it IS a toggle and announcing it as one tells a
                  screen-reader user the current state — a label that flips
                  only says what the NEXT press will do.
                */}
                <MenuAction
                  label={isFlagged(email) ? t("action.unflag") : t("action.flag")}
                  pressed={isFlagged(email)}
                  onClick={() => {
                    onToggleFlag();
                    close();
                  }}
                />

                {onToggleMute !== undefined && (
                  <MenuAction
                    label={isMuted ? t("mute.unmute") : t("mute.action")}
                    onClick={() => {
                      onToggleMute();
                      close();
                    }}
                  />
                )}

                {/*
                  E6: block the sender (canon §2.2). Rendered only when the
                  address resolves AND the server offers filters — a block
                  writes a Sieve rule, so without the capability it is a
                  control that cannot do its job.
                */}
                {onBlockSender !== undefined && senderAddress(email) !== undefined && (
                  <MenuAction
                    label={t("blocked.action")}
                    onClick={() => {
                      const address = senderAddress(email);
                      if (address !== undefined) onBlockSender(address);
                      close();
                    }}
                  />
                )}

                {onForwardAsAttachment !== undefined && (
                  <MenuAction
                    label={t("action.forwardAsAttachment")}
                    onClick={() => {
                      onForwardAsAttachment();
                      close();
                    }}
                  />
                )}

                <MenuAction
                  label={t("action.print")}
                  onClick={() => {
                    window.print();
                    close();
                  }}
                />

                <MenuAction
                  label={t("action.viewOriginal")}
                  onClick={() => {
                    setOriginalOpen(true);
                    close();
                  }}
                />

                {/*
                  C-04: the whole-message download, ONCE, beside "Ver original"
                  — the same file seen a different way. It used to render under
                  every message body, which put a diagnostic six times between
                  the reader and the next message in a six-message thread.

                  The component itself renders the item, because it owns a
                  fetch state machine and a live region that a second
                  implementation would get wrong.
                */}
                <DownloadOriginalButton
                  email={email}
                  client={client}
                  accountId={accountId}
                  variant="menu"
                  onDone={close}
                />
              </>
            )}
          </PopupMenu>
        </div>

        {/*
          The reply verbs keep their WORDS, below the icon row.

          E5 `defaultReplyBehavior` (canon §2.3), Gmail's shape exactly: the
          preference chooses which reply is PRIMARY and the other stays on
          screen as a secondary. Both remain available and both keep their own
          label — the setting moves the emphasis and the default, it never
          removes a control (P4), which is why this is an order swap rather
          than a conditional render.

          The `r` key follows the same preference in `MailScreen`, so the
          button the eye lands on and the key the hand reaches for agree.
        */}
        {/*
          C-09: in conversation view the reply verbs live at the END of the
          thread, as Gmail's pills (ConversationView), and NOT here as well —
          two rows saying "Responder" in one pane are two answers to one
          question. The single-message reader keeps this row: it has no
          bottom, its body is the whole pane.
        */}
        {!conversationView && (
        <div className={styles.actions} role="group" aria-label={t("action.reply")}>
          {prefs.defaultReplyBehavior === "replyAll" ? (
            <>
              <button type="button" className={styles.primaryAction} onClick={onReplyAll}>
                {t("action.replyAll")}
              </button>
              <button type="button" className={styles.secondaryAction} onClick={onReply}>
                {t("action.reply")}
              </button>
            </>
          ) : (
            <>
              <button type="button" className={styles.primaryAction} onClick={onReply}>
                {t("action.reply")}
              </button>
              <button type="button" className={styles.secondaryAction} onClick={onReplyAll}>
                {t("action.replyAll")}
              </button>
            </>
          )}
          <button type="button" className={styles.secondaryAction} onClick={onForward}>
            {t("action.forward")}
          </button>
        </div>
        )}

        {/*
          E1: the sender block belongs to ONE message, and in conversation view
          every message renders its own (ConversationMessage). Repeating the
          opened message's sender above the thread would state it twice and,
          worse, would keep naming the message the route happened to open while
          the reader scrolled through six others.

          The count line goes too: the conversation has its own bar with the
          count AND the expand/collapse control.

          The unsubscribe button is the one thing that has to survive, because
          it is a statement about the SENDER of the mail that brought you here
          (canon §2.2) — so it moves up beside the subject when the per-message
          identity block is not rendered.
        */}
        {conversationView ? (
          unsubscribe !== undefined && (
            <div className={styles.conversationUnsubscribe}>
              <UnsubscribeButton
                info={unsubscribe}
                listName={listIdLabel(email)}
                onUnsubscribeByMail={onUnsubscribeByMail}
              />
            </div>
          )
        ) : (
          <>
            {threadSize > 1 && (
              <p className={styles.threadContext}>{format("reader.threadContext", threadSize)}</p>
            )}

            <div className={styles.identity}>
              <span className={styles.avatar} aria-hidden="true">
                {initialsFor(senderLabel(email))}
              </span>
              <div className={styles.identityText}>
                <p className={styles.fromLine}>
                  <span className={styles.fromName}>
                    {senderLabel(email) ?? t("list.unknownSender")}
                  </span>
                  {email.from?.[0]?.name !== null && email.from?.[0] !== undefined && (
                    <span className={styles.fromAddress}>{`<${email.from[0].email}>`}</span>
                  )}
                </p>
                {isoDate !== undefined && (
                  <time className={styles.date} dateTime={isoDate}>
                    {formatFullDate(email.receivedAt, locale)}
                  </time>
                )}
              </div>
              {/* Canon §2.2: the control sits NEXT TO THE SENDER, not in the
                  toolbar — it is a statement about who is writing, not an
                  action on this one message. */}
              {unsubscribe !== undefined && (
                <UnsubscribeButton
                  info={unsubscribe}
                  listName={listIdLabel(email)}
                  onUnsubscribeByMail={onUnsubscribeByMail}
                />
              )}
            </div>

            {/*
              Recipients as a description list: each label is programmatically
              tied to its addresses, which is what lets a screen reader say
              "To: Ana, Carlos" instead of reading five names with no idea
              which field they belong to.
            */}
            <dl className={styles.recipients}>
              <AddressRow label={t("reader.to")} addresses={email.to} />
              <AddressRow label={t("reader.cc")} addresses={email.cc} />
              <AddressRow label={t("reader.bcc")} addresses={email.bcc} />
            </dl>
          </>
        )}
      </header>

      {/*
        The attachment list belongs to ONE message, so in conversation view it
        is rendered by each expanded message (ConversationMessage) rather than
        hoisted here — hoisting the opened message's files above six others
        would attribute them to the wrong sender.
      */}
      {!conversationView && attachments.length > 0 && (
        <>
          <AttachmentList
            attachments={attachments}
            client={client}
            accountId={accountId}
            blobToken={blobToken}
          />
          {/*
            E9 / canon §2.10: attachments are NOT cached, and Gmail declares the
            same limitation ("attachments not previewable" offline). Declaring
            it here — on the list of files that will not open — rather than in a
            settings page nobody reads is the difference between a documented
            limit and a broken button.
          */}
          {isOffline && (
            <p className={styles.offlineNote}>{t("offline.attachments.unavailable")}</p>
          )}
        </>
      )}

      <div className={styles.bodyRegion}>
        {/*
          E1 / canon §2.1: the conversation, or the single message.

          The `conversationView` preference chooses between two REAL code
          paths, not between a feature and a crippled version of it: with it
          off, this is exactly the reader that shipped before E1.

          Canon §4.1.9 applies identically to both: in Spam the images are not
          merely blocked-with-an-offer, they are UNLOADABLE.
          `allowRemoteImages={false}` removes the unblock control entirely
          rather than disabling it, because a disabled "Show images" invites
          the click that the policy exists to prevent, and a remote fetch from
          a message in Spam is a delivery receipt to a spammer.
        */}
        {conversationView ? (
          <ConversationView
            /* Keyed by the THREAD so moving between conversations remounts —
               and moving inside one (p/n, a click on a collapsed row) does
               not, which is what preserves the expansion the user built. */
            key={thread?.id ?? email.id}
            openEmail={email}
            targetMessageId={targetMessageId}
            thread={thread}
            client={client}
            accountId={accountId}
            blobToken={blobToken}
            signImageUrls={signImages}
            /* E10: the thread-level conjunct is the FOLDER rule (Junk); the
               per-message suspicious verdict is applied inside
               ConversationView, message by message, because a clean reply and
               a flagged first message legitimately share a thread. */
            allowRemoteImages={!inJunk}
            autoLoadImages={autoLoadImages}
            onReply={onReplyToMessage}
            onForward={onForwardMessage}
            onMarkRead={onMarkMessagesRead}
            onControls={publishControls}
          />
        ) : (
          /* Keyed by message id so per-message state — the remote-images
             opt-in above all — can never leak from one message to the next. */
          <MessageBody
            key={email.id}
            email={email}
            signImageUrls={signImages}
            allowRemoteImages={remoteImagesAllowed}
            autoLoadImages={autoLoadImages}
          />
        )}
        {inJunk && (
          <p className={styles.spamImagesNote} role="note">
            {t("reader.spamImagesBlocked")}
          </p>
        )}
      </div>

      <OriginalDialog
        isOpen={originalOpen}
        onClose={() => {
          setOriginalOpen(false);
        }}
        email={email}
        client={client}
        accountId={accountId}
      />
    </article>
  );
}

/**
 * One icon button in the reader's toolbar (P0-6).
 *
 * The children are the SVG's paths, not a whole `<svg>`: every icon here
 * shares one 20×20 grid, one stroke weight and `currentColor`, so writing the
 * wrapper sixteen times would be sixteen chances for one icon to be a
 * different weight than its neighbours — the kind of drift nobody reports and
 * everybody sees.
 *
 * `aria-label` AND `title`, always the same string. That is what answers the
 * objection the old text-labelled toolbar was built on: the name is one hover
 * away for a pointer user and always present for a screen reader, so an
 * icon-only row is not a guessing game.
 */
function IconAction({
  label,
  onClick,
  danger = false,
  pressed,
  children,
}: {
  readonly label: string;
  readonly onClick: () => void;
  readonly danger?: boolean;
  /** Present makes the button a TOGGLE and announces its current state. */
  readonly pressed?: boolean;
  readonly children: React.ReactNode;
}): React.JSX.Element {
  return (
    <button
      type="button"
      className={[
        styles.iconAction,
        danger ? styles.iconActionDanger : "",
        pressed === true ? styles.iconActionActive : "",
      ]
        .filter(Boolean)
        .join(" ")}
      onClick={onClick}
      aria-label={label}
      title={label}
      {...(pressed !== undefined ? { "aria-pressed": pressed } : {})}
    >
      <svg
        viewBox="0 0 20 20"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
        focusable="false"
      >
        {children}
      </svg>
    </button>
  );
}

/**
 * One text item in the reader's overflow menu (P0-6).
 *
 * `role="menuitem"` inside the `li role="none"` wrapper `PopupMenu` expects —
 * the list itself carries `role="menu"`, and an `li` that kept its implicit
 * `listitem` role between the two would make the menu announce a list of items
 * that are not menu items.
 */
function MenuAction({
  label,
  onClick,
  pressed,
}: {
  readonly label: string;
  readonly onClick: () => void;
  readonly pressed?: boolean;
}): React.JSX.Element {
  return (
    <li role="none">
      <button
        type="button"
        role="menuitem"
        className={styles.menuItem}
        onClick={onClick}
        {...(pressed !== undefined ? { "aria-pressed": pressed } : {})}
      >
        {label}
      </button>
    </li>
  );
}

/**
 * The address a "block this sender" would block (E6).
 *
 * `from` before `sender`, which is the order that matters and the one worth
 * writing down: RFC 5322's `Sender` is who PUT the message in the mail system
 * and `From` is who wrote it. For a list posting they differ, and blocking the
 * list's own submission address would block every author on that list rather
 * than the one the user is looking at. `From` is the identity the reader shows,
 * so it is the identity the button acts on.
 *
 * Returns undefined when neither header carries an address, which removes the
 * control — a block with nothing to block is not an action.
 */
function senderAddress(email: Email): string | undefined {
  const address = email.from?.[0]?.email ?? email.sender?.[0]?.email;
  if (address === undefined || address.trim() === "") return undefined;
  return address.trim().toLowerCase();
}

/**
 * The Unsubscribe control (canon §2.2).
 *
 * The `mailto:` path opens OUR composer prefilled, so the user sees exactly
 * what is about to be sent from their own address and can cancel. The http(s)
 * path is a link with `rel="noopener noreferrer"` and `target="_blank"` — a
 * real anchor rather than a button calling `window.open`, so middle-click and
 * "copy link" work and the user can see where it goes before committing.
 */
function UnsubscribeButton({
  info,
  listName,
  onUnsubscribeByMail,
}: {
  readonly info: UnsubscribeInfo;
  readonly listName: string | undefined;
  readonly onUnsubscribeByMail: (
    to: string,
    subject: string | undefined,
    body: string | undefined,
  ) => void;
}): React.JSX.Element | null {
  const { t, format } = useTranslation();

  // The accessible name says WHAT is being unsubscribed from when the message
  // told us (List-ID), which is the difference between "Unsubscribe" (from
  // what?) and "Unsubscribe from Moov News".
  const label =
    listName === undefined ? t("action.unsubscribe") : format("reader.unsubscribeFrom", listName);

  if (info.mailto !== undefined) {
    const { to, subject, body } = info.mailto;
    return (
      <button
        type="button"
        className={styles.unsubscribe}
        onClick={() => {
          onUnsubscribeByMail(to, subject, body);
        }}
        aria-label={label}
        title={t("reader.unsubscribeLatency")}
      >
        {t("action.unsubscribe")}
      </button>
    );
  }

  if (info.url === undefined) return null;

  /*
   * TODO(E-server): RFC 8058 one-click. `info.oneClick` says the sender
   * accepts a bare POST to this URI, which would spare the user the round
   * trip to a landing page — but the POST cannot be made from the browser
   * (cross-origin, and it would leak the reader's IP to the sender). It
   * belongs on the server, in a later epic; until then the link is the
   * honest path and the flag is parsed and carried for it.
   */
  return (
    <a
      className={styles.unsubscribe}
      href={info.url}
      target="_blank"
      rel="noopener noreferrer"
      aria-label={label}
      title={t("reader.unsubscribeOpensTab")}
    >
      {t("action.unsubscribe")}
    </a>
  );
}

/**
 * "Show original" — the raw RFC 822 headers (canon §2.2).
 *
 * The bytes come through the SAME authenticated blob path the download button
 * uses (the download route needs HTTP Basic, which a navigation cannot carry —
 * see {@link DownloadOriginalButton}), and only the header section is rendered:
 * a 4 MB message with a base64 attachment must not become 4 MB of DOM.
 *
 * Rendered as TEXT in a `<pre>`, never as HTML. The content is attacker-
 * controlled by definition, and this is one of the few places in the app that
 * shows it verbatim.
 */
function OriginalDialog({
  isOpen,
  onClose,
  email,
  client,
  accountId,
}: {
  readonly isOpen: boolean;
  readonly onClose: () => void;
  readonly email: Email;
  readonly client: JmapClient;
  readonly accountId: string;
}): React.JSX.Element {
  const { t } = useTranslation();
  const dialogRef = useRef<HTMLDialogElement | null>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const [headers, setHeaders] = useState<string | undefined>(undefined);
  const [state, setState] = useState<"idle" | "loading" | "failed">("idle");
  const [copied, setCopied] = useState<"idle" | "done" | "failed">("idle");

  // The same <dialog> pattern as ShortcutsDialog: showModal() makes the rest
  // of the page inert, traps focus and handles Escape, none of which a
  // hand-rolled overlay gets right. Focus restoration is explicit because not
  // every browser does it.
  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return;
    if (isOpen && !dialog.open) {
      returnFocusRef.current =
        document.activeElement instanceof HTMLElement ? document.activeElement : null;
      dialog.showModal();
    } else if (!isOpen && dialog.open) {
      dialog.close();
      returnFocusRef.current?.focus();
    }
  }, [isOpen]);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return undefined;
    const handleClose = (): void => {
      returnFocusRef.current?.focus();
      onClose();
    };
    dialog.addEventListener("close", handleClose);
    return () => {
      dialog.removeEventListener("close", handleClose);
    };
  }, [onClose]);

  const blobId = email.blobId;

  useEffect(() => {
    if (!isOpen || blobId === undefined) return undefined;
    let cancelled = false;
    setState("loading");
    setCopied("idle");
    void (async () => {
      try {
        const blob = await client.downloadBlob(accountId, blobId, "message.eml", "message/rfc822");
        const raw = await blob.text();
        if (cancelled) return;
        setHeaders(unfoldHeaders(headerSection(raw)));
        setState("idle");
      } catch {
        if (!cancelled) setState("failed");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [isOpen, client, accountId, blobId]);

  const copy = useCallback((): void => {
    if (headers === undefined) return;
    // `navigator.clipboard` is absent in insecure contexts and in jsdom; the
    // failure is REPORTED rather than swallowed, because a Copy button that
    // silently does nothing is the most confusing control in any UI.
    const clipboard = navigator.clipboard as Clipboard | undefined;
    if (clipboard === undefined) {
      setCopied("failed");
      return;
    }
    void clipboard.writeText(headers).then(
      () => {
        setCopied("done");
      },
      () => {
        setCopied("failed");
      },
    );
  }, [headers]);

  return (
    <dialog ref={dialogRef} className={styles.originalDialog} aria-labelledby="original-title">
      <div className={styles.originalContent}>
        <div className={styles.originalHeader}>
          <h2 className={styles.originalTitle} id="original-title">
            {t("reader.originalTitle")}
          </h2>
          <button
            type="button"
            className={styles.close}
            onClick={onClose}
            aria-label={t("shortcuts.close")}
          >
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true" focusable="false">
              <path d="M5.5 5.5l9 9M14.5 5.5l-9 9" />
            </svg>
          </button>
        </div>

        <p className={styles.originalSubtitle}>{t("reader.originalHeaders")}</p>

        {state === "loading" && <p className={styles.mutedText}>{t("reader.originalLoading")}</p>}
        {state === "failed" && <p className={styles.errorTitle}>{t("reader.originalFailed")}</p>}
        {state === "idle" && headers !== undefined && (
          <pre className={styles.originalPre}>{headers}</pre>
        )}

        <div className={styles.originalActions}>
          <button
            type="button"
            className={styles.secondaryAction}
            onClick={copy}
            disabled={headers === undefined}
          >
            {t("reader.copy")}
          </button>
          {/* Always present so the outcome is ANNOUNCED when it appears,
              rather than a live region being inserted with its own text. */}
          <span role="status" aria-live="polite" className={styles.downloadStatus}>
            {copied === "done" ? t("reader.copied") : ""}
            {copied === "failed" ? t("reader.copyFailed") : ""}
          </span>
        </div>
      </div>
    </dialog>
  );
}

function AddressRow({
  label,
  addresses,
}: {
  readonly label: string;
  readonly addresses: readonly EmailAddress[] | null | undefined;
}): React.JSX.Element | null {
  // Absent headers are `null` on this server, never `[]` — either way there is
  // nothing to render, and an empty row would be noise.
  if (addresses === null || addresses === undefined || addresses.length === 0) return null;
  return (
    <div className={styles.recipientRow}>
      <dt className={styles.recipientLabel}>{label}</dt>
      <dd className={styles.recipientValue}>
        {addresses.map((address) => address.name ?? address.email).join(", ")}
      </dd>
    </div>
  );
}
