import { useTranslation } from "../../i18n/I18nProvider";
import type { JmapClient } from "../../api/jmap";
import { formatFullDate, formatListDate, initialsFor, machineDate } from "../../mail/format";
import { senderLabel } from "../../mail/threading";
import { isFlagged, type Email, type EmailAddress } from "../../mail/types";
import { AttachmentList } from "./MessageAttachments";
import { MessageBody } from "./MessageBody";
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
            <RecipientLine email={email} />
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
          Per-message actions (canon §2.1). Reply is primary here as it is in
          the single-message reader; the conversation-wide verbs (archive,
          delete, spam, move, star) stay in the pane's toolbar above, because
          they act on the whole thread.
        */}
        <div className={styles.messageActions} role="group" aria-label={t("action.more")}>
          <button type="button" className={styles.messageAction} onClick={onReply}>
            {t("action.reply")}
          </button>
          <button type="button" className={styles.messageAction} onClick={onReplyAll}>
            {t("action.replyAll")}
          </button>
          <button type="button" className={styles.messageAction} onClick={onForward}>
            {t("action.forward")}
          </button>
        </div>
      </header>

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
 * The "to Ana, Carlos" line under the sender.
 *
 * Gmail shows recipients on an expanded message because in a thread the
 * question "was this to me or to the list" is asked constantly. It is
 * truncated by CSS rather than by slicing the array, so the full list is still
 * selectable and readable by a screen reader.
 */
function RecipientLine({ email }: { readonly email: Email }): React.JSX.Element | null {
  const { t } = useTranslation();
  const recipients: readonly EmailAddress[] = [...(email.to ?? []), ...(email.cc ?? [])];
  if (recipients.length === 0) return null;
  const names = recipients.map((address) => address.name ?? address.email).join(", ");
  return (
    <span className={styles.recipientLine}>
      {`${t("reader.to")} ${names}`}
    </span>
  );
}
