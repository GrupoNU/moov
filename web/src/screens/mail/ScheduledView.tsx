import { useTranslation } from "../../i18n/I18nProvider";
import type { ScheduledSend } from "../../mail/scheduled";
import styles from "./ScheduledView.module.css";

/**
 * The Scheduled view (L3 E4, canon §2.3 — Gmail's own left-nav "Scheduled").
 *
 * # Why it is a view of its own and not a folder
 *
 * The server refused a Scheduled FOLDER, and instructively:
 * "a scheduled message must stay a DRAFT (canon: 'cancel reverts to draft'),
 * and a draft lives in Drafts. Moving it to a second folder would make every
 * other IMAP client show it outside Drafts, where their own compose flows
 * cannot reach it — the mirror image of why snooze DOES move."
 *
 * So these rows are not messages in a mailbox; they are `EmailSubmission`
 * records joined to the drafts they will send. They have no keywords, no thread
 * and no place in the list's selection or keyboard model, which is the same
 * reason `OutboxView` is its own view — and the reason this reuses that shape
 * rather than the message list's.
 *
 * # What each action really does, said out loud
 *
 *   - **Cancel send** removes the schedule. The draft does NOT disappear: the
 *     server never filed it into Sent (`holdsItsDraft`) and never retires it
 *     for a cancellation, so the message is still in Drafts and still editable.
 *     The toast says so, because a user who reads "cancelled" and cannot find
 *     the message has lost work as far as they can tell.
 *   - **Send now** is cancel-then-resubmit (`sendScheduledNow` documents why it
 *     cannot be an update). It gets the ordinary undo window, like any send.
 */
export function ScheduledView({
  items,
  onCancel,
  onSendNow,
  busyId,
  locale,
}: {
  readonly items: readonly ScheduledSend[];
  readonly onCancel: (item: ScheduledSend) => void;
  readonly onSendNow: (item: ScheduledSend) => void;
  /** The row with an operation in flight, so its buttons cannot be double-fired. */
  readonly busyId: string | undefined;
  readonly locale: string;
}): React.JSX.Element {
  const { t } = useTranslation();

  if (items.length === 0) {
    return (
      <div className={styles.empty}>
        <p className={styles.emptyTitle}>{t("schedule.empty")}</p>
      </div>
    );
  }

  return (
    <div className={styles.wrap}>
      <p className={styles.explain}>{t("schedule.explain")}</p>
      <ul className={styles.list}>
        {items.map((item) => (
          <li key={item.id} className={styles.item}>
            <div className={styles.main}>
              <span className={styles.recipients}>
                {item.recipients.length > 0
                  ? item.recipients.join(", ")
                  : t("schedule.noRecipients")}
              </span>
              <span className={styles.subject}>
                {item.subject === "" ? t("notification.noSubject") : item.subject}
              </span>
            </div>

            <div className={styles.side}>
              {/* A machine-readable timestamp beside the human one: the row is
                  ABOUT a moment in time, so the moment belongs in the markup. */}
              <time className={styles.when} dateTime={item.sendAt}>
                {formatSendAt(item.sendAt, locale)}
              </time>
              <span className={styles.actions}>
                <button
                  type="button"
                  className={styles.sendNow}
                  disabled={busyId === item.id}
                  onClick={() => {
                    onSendNow(item);
                  }}
                >
                  {t("schedule.sendNow")}
                </button>
                <button
                  type="button"
                  className={styles.cancel}
                  disabled={busyId === item.id}
                  onClick={() => {
                    onCancel(item);
                  }}
                >
                  {t("schedule.cancel")}
                </button>
              </span>
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * The scheduled instant, in full.
 *
 * Never relative ("in 3 days"): a scheduled send is a commitment to a wall
 * clock, and the one question this row exists to answer is exactly WHEN. An
 * unparseable value falls back to the raw string rather than to "Invalid Date".
 */
function formatSendAt(sendAt: string, locale: string): string {
  const at = new Date(sendAt);
  if (Number.isNaN(at.getTime())) return sendAt;
  return at.toLocaleString(locale, {
    weekday: "short",
    day: "numeric",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
  });
}
