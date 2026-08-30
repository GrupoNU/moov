import { useTranslation } from "../../i18n/I18nProvider";
import type { OutboxItem } from "../../offline/outbox";
import styles from "./OutboxView.module.css";

/**
 * The Outbox folder's contents (L3 E9; canon §2.10 — "offline sends queue in
 * an 'Outbox' folder").
 *
 * # Why it is its own view rather than a filtered message list
 *
 * Nothing in here is an `Email`. These messages have never reached the server,
 * so they have no id, no thread, no keywords and no mailbox — every column the
 * message list draws would be empty or invented. What they DO have is a state,
 * and the state is the point: a queued message needs to say whether it is
 * waiting, going out, or stuck, and only the last of those needs a button.
 *
 * # Failures are never silent, and never automatic
 *
 * A failed item keeps its row, keeps the server's own words for why, and offers
 * exactly two actions: try again, or discard. It is never retried on its own
 * (that would repeat a refusal the server already made on the message's merits)
 * and never removed on its own — a message the user pressed Send on that
 * vanished without a trace is the worst outcome this whole epic can produce.
 */
export function OutboxView({
  items,
  onRetry,
  onDiscard,
}: {
  readonly items: readonly OutboxItem[];
  readonly onRetry: (item: OutboxItem) => void;
  readonly onDiscard: (item: OutboxItem) => void;
}): React.JSX.Element {
  const { t } = useTranslation();

  if (items.length === 0) {
    return (
      <div className={styles.empty}>
        <p className={styles.emptyTitle}>{t("outbox.empty")}</p>
      </div>
    );
  }

  return (
    <div className={styles.wrap}>
      <p className={styles.explain}>{t("outbox.explain")}</p>
      <ul className={styles.list}>
        {items.map((item) => (
          <li key={item.id} className={styles.item}>
            <div className={styles.main}>
              <span className={styles.recipients}>{item.recipients.join(", ")}</span>
              <span className={styles.subject}>
                {item.subject === "" ? t("notification.noSubject") : item.subject}
              </span>
              {/*
                The server's own sentence, not a paraphrase. "Could not be sent"
                alone leaves the user with nothing to act on; "550 no such user"
                tells them the address is wrong.
              */}
              {item.lastError !== undefined && (
                <span className={styles.error}>{item.lastError}</span>
              )}
            </div>

            <div className={styles.side}>
              <span
                className={[
                  styles.state,
                  item.state === "failed" ? styles.stateFailed : "",
                  item.state === "sending" ? styles.stateSending : "",
                ]
                  .filter(Boolean)
                  .join(" ")}
              >
                {item.state === "failed"
                  ? t("outbox.failed")
                  : item.state === "sending"
                    ? t("outbox.sending")
                    : t("outbox.queued")}
              </span>

              {/*
                Actions appear only on a FAILED item. A queued message is going
                out on its own; offering "try again" for it would invite a
                second send of a message already in flight.
              */}
              {item.state === "failed" && (
                <span className={styles.actions}>
                  <button
                    type="button"
                    className={styles.retry}
                    onClick={() => {
                      onRetry(item);
                    }}
                  >
                    {t("outbox.retry")}
                  </button>
                  <button
                    type="button"
                    className={styles.discard}
                    onClick={() => {
                      onDiscard(item);
                    }}
                  >
                    {t("outbox.discard")}
                  </button>
                </span>
              )}
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}
