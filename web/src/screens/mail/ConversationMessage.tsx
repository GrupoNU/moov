import { useTranslation } from "../../i18n/I18nProvider";
import type { JmapClient } from "../../api/jmap";
import { formatFullDate, formatListDate, initialsFor, machineDate } from "../../mail/format";
import { senderLabel } from "../../mail/threading";
import { isFlagged, type Email } from "../../mail/types";
import { AttachmentList } from "./MessageAttachments";
import { MessageBody } from "./MessageBody";
import { PopupMenu } from "./PopupMenu";
import { RecipientSummary } from "./RecipientSummary";
import type { SignImageUrls } from "./SecureHtmlBody";
import styles from "./ConversationMessage.module.css";

/**
 * One message inside a conversation (L3 epic E1, canon §2.1).
 *
 * Two renderings of the same message, chosen by `isExpanded`:
 *
 *   - **Collapsed** — a single clickable row: sender, snippet, date. That is
 *     Gmail's set exactly, and the omission matters as much as the inclusions:
 *     no subject, because every message in a conversation shares one and
 *     repeating it twenty-four times is how a thread becomes unreadable.
 *   - **Expanded** — sender identity, recipients, the body through the secure
 *     pipeline, and the per-message actions (reply / reply-all / forward).
 *
 * # Why the per-message actions are here and the conversation actions are not
 *
 * Canon §2.1: "Reply/reply-all/forward are per-message; toolbar archive/delete/
 * label act on the conversation." A reply replies to A message — the one whose
 * quoted text goes into the draft — while archiving archives the whole thread.
 * Putting the first pair here and leaving the second to the pane's toolbar is
 * that distinction expressed in the component boundary, so neither can quietly
 * acquire the other's scope.
 *
 * # The collapsed row is a button, not a div with a handler
 *
 * It is the primary affordance of a collapsed message and it must be reachable
 * by Tab, activatable by Space and Enter, and announced with its expanded
 * state. `aria-expanded` on a real `<button>` gets all of that for free; the
 * alternative is re-implementing three behaviours and getting one wrong.
 */

export interface ConversationMessageProps {
  readonly email: Email;
  readonly isExpanded: boolean;
  readonly onToggle: () => void;
  /** Per-message composition (canon §2.1). */
  readonly onReply: () => void;
  readonly onReplyAll: () => void;
  readonly onForward: () => void;
  readonly signImageUrls: SignImageUrls;
  /** Junk suppresses the remote-image opt-in entirely (canon §4.1.9). */
  readonly allowRemoteImages: boolean;
  readonly autoLoadImages: boolean;
  /**
   * True for the message the reader is "on" for `p`/`n` purposes. It gets the
   * scroll target and a focus ring, so keyboard navigation inside a long
   * thread is visible rather than invisible.
   */
  readonly isCurrent: boolean;
  /** Needed for this message's own attachment downloads. */
  readonly client: JmapClient;
  readonly accountId: string;
  /** The `blob`-scoped token that makes each attachment a native download. */
  readonly blobToken?: string | undefined;
  /** C-14: the reader's own addresses, so the recipient line can say "mí". */
  readonly ownAddresses?: readonly string[] | undefined;
}

export function ConversationMessage({
  email,
  isExpanded,
  onToggle,
  onReply,
  onReplyAll,
  onForward,
  signImageUrls,
  allowRemoteImages,
  autoLoadImages,
  isCurrent,
  client,
  accountId,
  blobToken,
  ownAddresses,
}: ConversationMessageProps): React.JSX.Element {
  const { t, locale } = useTranslation();

  const sender = senderLabel(email) ?? t("list.unknownSender");
  const isoDate = machineDate(email.receivedAt);

  const className = [
    styles.message,
    isExpanded ? styles.expanded : styles.collapsed,
    isCurrent ? styles.current : "",
  ]
    .filter(Boolean)
    .join(" ");

  if (!isExpanded) {
    return (
      <article className={className} data-message-id={email.id}>
        <button type="button" className={styles.collapsedRow} onClick={onToggle} aria-expanded={false}>
          <span className={styles.avatarSmall} aria-hidden="true">
            {initialsFor(senderLabel(email))}
          </span>
          <span className={styles.collapsedSender}>{sender}</span>
          {/*
            The snippet is the server's `preview`, which every list row already
            carries — so a collapsed row costs nothing extra to render. It is
            marked aria-hidden because the sender and date around it already
            name the row; a screen reader reading a truncated body fragment as
            part of the button's name would make the control's name change
            every time the message did.
          */}
          <span className={styles.collapsedPreview} aria-hidden="true">
            {email.preview ?? ""}
          </span>
          {/* A star on a collapsed row, because the row otherwise gives no
              sign that this particular message in the thread is starred. */}
          {isFlagged(email) && (
            <span className={styles.collapsedStar} aria-label={t("action.flag")}>
              ★
            </span>
          )}
          {isoDate !== undefined && (
            <time className={styles.collapsedDate} dateTime={isoDate}>
              {formatListDate(email.receivedAt, locale)}
            </time>
          )}
        </button>
      </article>
    );
  }

  return (
    <article className={className} data-message-id={email.id}>
      <header className={styles.messageHeader}>
        {/*
          The whole header is the collapse control: clicking the sender line of
          an open message closes it, which is what every mail client does and
          what a reader tries first. It stays a <button> for the same reasons
          the collapsed row is one.
        */}
        <button
          type="button"
          className={styles.expandedRow}
          onClick={onToggle}
          aria-expanded={true}
        >
          <span className={styles.avatar} aria-hidden="true">
            {initialsFor(senderLabel(email))}
          </span>
          <span className={styles.identityText}>
            <span className={styles.fromLine}>
              <span className={styles.fromName}>{sender}</span>
              {/* The address is shown only when the display NAME already took
                  the line above — otherwise `sender` is the address itself and
                  this would print it twice. */}
              {email.from?.[0]?.name != null && (
                <span className={styles.fromAddress}>{`<${email.from[0].email}>`}</span>
              )}
            </span>
          </span>
          {isFlagged(email) && (
            <span className={styles.collapsedStar} aria-label={t("action.flag")}>
              ★
            </span>
          )}
          {isoDate !== undefined && (
            <time className={styles.expandedDate} dateTime={isoDate}>
              {formatFullDate(email.receivedAt, locale)}
            </time>
          )}
        </button>

        {/*
          Per-message actions (canon §2.1), in Gmail's shape (C-08): ONE reply
          arrow and a ⋮ holding reply-all and forward. Three text buttons on
          every expanded message read as three pills per message down a long
          thread; Gmail keeps one glyph in the sender line and the rest a
          click away. Both are real buttons — Tab reaches them, Enter and
          Space press them — and the arrow carries `aria-label` AND `title`
          with the same word, the answer this app gives everywhere to "an icon
          is a guessing game". The conversation-wide verbs (archive, delete,
          spam, move, star) stay in the pane's toolbar, because they act on
          the whole thread.
        */}
        <div className={styles.messageActions} role="group" aria-label={t("action.more")}>
          <button
            type="button"
            className={styles.messageIconAction}
            onClick={onReply}
            aria-label={t("action.reply")}
            title={t("action.reply")}
          >
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M8 5.5L3.5 9.5 8 13.5" />
              <path d="M3.8 9.5h6.4a5.3 5.3 0 0 1 5.3 5.3v.7" />
            </svg>
          </button>
          <PopupMenu
            label={t("action.more")}
            disabled={false}
            triggerClassName={styles.messageIconAction}
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
                <MessageMenuItem
                  label={t("action.reply")}
                  onClick={() => {
                    onReply();
                    close();
                  }}
                />
                <MessageMenuItem
                  label={t("action.replyAll")}
                  onClick={() => {
                    onReplyAll();
                    close();
                  }}
                />
                <MessageMenuItem
                  label={t("action.forward")}
                  onClick={() => {
                    onForward();
                    close();
                  }}
                />
              </>
            )}
          </PopupMenu>
        </div>
      </header>

      {/*
        C-14: "para mí ▾", as a SIBLING of the collapse button above — a
        toggle inside a button would be invalid markup and would collapse the
        message on every press. Indented to the name column, where Gmail
        puts it.
      */}
      <div className={styles.recipientRow}>
        <RecipientSummary email={email} ownAddresses={ownAddresses} />
      </div>

      <div className={styles.messageBody}>
        {/*
          The body arrives lazily: a thread's list rows carry no `bodyValues`
          (LIST_PROPERTIES omits them deliberately), so expanding a message is
          what triggers its fetch. Until it lands the row shows a quiet
          placeholder rather than the empty-body notice, which would claim the
          message HAS no content when we simply have not asked for it yet.
        */}
        {email.bodyValues === undefined ? (
          <p className={styles.bodyLoading} role="status" aria-live="polite">
            {t("reader.loading")}
          </p>
        ) : (
          <>
            <MessageBody
              /* Keyed by id so the per-message remote-image opt-in can never
                 leak from one message of the thread to another. */
              key={email.id}
              email={email}
              signImageUrls={signImageUrls}
              allowRemoteImages={allowRemoteImages}
              autoLoadImages={autoLoadImages}
              client={client}
              accountId={accountId}
            />
            {/* THIS message's attachments — in a thread, files belong to the
                message that carried them, never to the conversation. */}
            {(email.attachments ?? []).length > 0 && (
              <AttachmentList
                attachments={email.attachments ?? []}
                client={client}
                accountId={accountId}
                blobToken={blobToken}
              />
            )}
          </>
        )}
      </div>
    </article>
  );
}

/**
 * One item of a message's ⋮ menu (C-08): the `li role="none"` wrapping a
 * `role="menuitem"` button that `PopupMenu` expects, in the same shape the
 * reader's overflow uses.
 */
function MessageMenuItem({
  label,
  onClick,
}: {
  readonly label: string;
  readonly onClick: () => void;
}): React.JSX.Element {
  return (
    <li role="none">
      <button type="button" role="menuitem" className={styles.menuItem} onClick={onClick}>
        {label}
      </button>
    </li>
  );
}
