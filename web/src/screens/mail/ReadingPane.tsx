import { useCallback, useEffect, useRef, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import type { JmapClient } from "../../api/jmap";
import { signImageProxyUrls } from "../../mail/api";
import { formatFullDate, initialsFor, machineDate } from "../../mail/format";
import { headerSection, unfoldHeaders } from "../../mail/rawMessage";
import { displaySubject, senderLabel } from "../../mail/threading";
import {
  isFlagged,
  type Email,
  type EmailAddress,
  type Mailbox,
  type Thread,
} from "../../mail/types";
import { listIdLabel, unsubscribeInfo, type UnsubscribeInfo } from "../../mail/unsubscribe";
import { useOffline } from "../../offline/OfflineProvider";
import { ConversationView, type ConversationControls } from "./ConversationView";
import { AttachmentList, DownloadOriginalButton } from "./MessageAttachments";
import { MessageBody } from "./MessageBody";
import { LabelChips } from "./LabelChips";
import { LabelMenu } from "./LabelMenu";
import { labelsFor, type Label } from "../../mail/labelStore";
import { MoveMenu } from "./MoveMenu";
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
  onArchive,
  onDelete,
  deleteIsPermanent,
  blobToken,
  onToggleFlag,
  onMove,
  onMarkUnread,
  onToggleSpam,
  onUnsubscribeByMail,
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
  conversationView,
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
   * Read from the OfflineProvider rather than taken as a prop: this is the only
   * thing in this component that cares about connectivity, and threading a
   * boolean through the reader's already-long prop list to reach one paragraph
   * would cost more than it explains.
   */
  const isOffline = !isOnline;
  const threadSize = thread?.emailIds.length ?? 1;
  const isoDate = machineDate(email.receivedAt);
  const unsubscribe = unsubscribeInfo(email);

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
              E8: the open message's labels, in full — the reader has the room
              the list row does not, and an open conversation is exactly where
              the complete set belongs (`max: Infinity`, no "+N").
            */}
            <LabelChips
              labels={labelsFor(email.keywords, labels ?? [])}
              max={Number.POSITIVE_INFINITY}
              onSelect={onSelectLabel}
            />
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
          The action row. Reply is the primary action of a mail client and is
          styled as such; the rest are equal-weight secondary actions. Each is
          a real <button> with a text label, not an icon alone — this pane has
          the room, and an icon-only toolbar is a guessing game the first time
          someone uses it.
        */}
        <div className={styles.actions} role="group" aria-label={t("action.more")}>
          <button type="button" className={styles.primaryAction} onClick={onReply}>
            {t("action.reply")}
          </button>
          <button type="button" className={styles.secondaryAction} onClick={onReplyAll}>
            {t("action.replyAll")}
          </button>
          <button type="button" className={styles.secondaryAction} onClick={onForward}>
            {t("action.forward")}
          </button>
          <span className={styles.actionSpacer} />

          {/*
            E2: the star. `aria-pressed` rather than a changing label, because
            it IS a toggle in one state and announcing it as a toggle is what
            tells a screen-reader user whether the message is starred right
            now — a label that flips only says what the next press will do.
          */}
          <button
            type="button"
            className={[styles.secondaryAction, isFlagged(email) ? styles.activeAction : ""]
              .filter(Boolean)
              .join(" ")}
            onClick={onToggleFlag}
            aria-pressed={isFlagged(email)}
          >
            {isFlagged(email) ? t("action.unflag") : t("action.flag")}
          </button>

          <button type="button" className={styles.secondaryAction} onClick={onArchive}>
            {t("action.archive")}
          </button>
          {/* The LABEL states which of the two semantics applies (W-A2). */}
          <button
            type="button"
            className={[styles.secondaryAction, deleteIsPermanent ? styles.dangerAction : ""]
              .filter(Boolean)
              .join(" ")}
            onClick={onDelete}
          >
            {deleteIsPermanent ? t("action.deleteForever") : t("action.delete")}
          </button>

          <button type="button" className={styles.secondaryAction} onClick={onToggleSpam}>
            {inJunk ? t("action.notSpam") : t("action.spam")}
          </button>

          {/*
            E4: snooze and mute, in the reader's own text-label idiom rather
            than the action bar's icons. Both act on the WHOLE conversation,
            which is the same thing every other button in this row does —
            `targetMessageIds()` expands the focused thread — and is what canon
            §2.1 asks for.
          */}
          {onSnooze !== undefined && (
            <SnoozeMenu
              disabled={false}
              onSnooze={onSnooze}
              triggerClassName={styles.secondaryAction}
              triggerContent={t("snooze.action")}
            />
          )}

          {onUnsnooze !== undefined && (
            <button type="button" className={styles.secondaryAction} onClick={onUnsnooze}>
              {t("snooze.unsnooze")}
            </button>
          )}

          {onToggleMute !== undefined && (
            <button type="button" className={styles.secondaryAction} onClick={onToggleMute}>
              {/* The word says what the click will DO — the badge above says
                  what the state IS, and the two read from the same flag. */}
              {isMuted ? t("mute.unmute") : t("mute.action")}
            </button>
          )}

          <MoveMenu
            mailboxes={mailboxes}
            currentMailboxId={currentMailboxId}
            disabled={false}
            onMove={onMove}
            triggerClassName={styles.secondaryAction}
            triggerContent={t("action.move")}
          />

          {/*
            E8: "Label as", beside "Move to" — the pair the reader offers for
            the same reason the action bar does (canon §2.7 binds `v` and `l`
            adjacently). Rendered only when the host wired the handlers, so an
            embedding without label plumbing shows no dead control.
          */}
          {onToggleLabel !== undefined && onManageLabels !== undefined && (
            <LabelMenu
              labels={labels ?? []}
              selection={[email.keywords]}
              disabled={false}
              onToggle={onToggleLabel}
              onManage={onManageLabels}
              triggerClassName={styles.secondaryAction}
              triggerContent={t("label.labelAs")}
            />
          )}

          {/*
            Mark-unread CLOSES the reader, and that is not a shortcut: leaving
            the message open would have the reading pane immediately re-mark it
            read, so the button would appear to do nothing. Gmail returns to
            the list for exactly this reason.
          */}
          <button type="button" className={styles.secondaryAction} onClick={onMarkUnread}>
            {t("action.markUnread")}
          </button>

          <button
            type="button"
            className={styles.secondaryAction}
            onClick={() => {
              window.print();
            }}
          >
            {t("action.print")}
          </button>

          <button
            type="button"
            className={styles.secondaryAction}
            onClick={() => {
              setOriginalOpen(true);
            }}
            aria-haspopup="dialog"
          >
            {t("action.viewOriginal")}
          </button>
        </div>

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
            email={email}
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
            thread={thread}
            client={client}
            accountId={accountId}
            blobToken={blobToken}
            signImageUrls={signImages}
            allowRemoteImages={!inJunk}
            autoLoadImages={autoLoadImages}
            onReply={onReplyToMessage}
            onForward={onForwardMessage}
            onMarkRead={onMarkMessagesRead}
            onControls={onConversationControls}
          />
        ) : (
          /* Keyed by message id so per-message state — the remote-images
             opt-in above all — can never leak from one message to the next. */
          <MessageBody
            key={email.id}
            email={email}
            signImageUrls={signImages}
            allowRemoteImages={!inJunk}
            autoLoadImages={autoLoadImages}
          />
        )}
        {inJunk && (
          <p className={styles.spamImagesNote} role="note">
            {t("reader.spamImagesBlocked")}
          </p>
        )}
      </div>

      <footer className={styles.footer}>
        <DownloadOriginalButton email={email} client={client} accountId={accountId} />
      </footer>

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
