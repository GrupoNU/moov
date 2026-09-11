import { useTranslation } from "../../i18n/I18nProvider";
import { usePrefs } from "../../mail/PrefsProvider";
import styles from "./ReplyRow.module.css";

/**
 * The reader's reply verbs, as the strip pinned to the bottom of the pane
 * (canon 07 §6; owner's screenshots, 2026-09-10 and 2026-09-11).
 *
 * # Why this is its own component
 *
 * There were two rows: the single-message reader's Responder / Responder a
 * todos / Reenviar, and the conversation's outlined pills. They are the same
 * three verbs in the same place doing the same thing, and they had drifted
 * apart — one was a header row under the subject, the other a block at the end
 * of the content. Gmail has one strip, pinned.
 *
 * # Why it lives OUTSIDE the scrollport
 *
 * The conversation's row was pinned with `position: sticky; bottom: 0` while
 * it still rendered inside the scrolling column. Sticky pins against the
 * scrollport's PADDING edge, and that scrollport has a bottom padding — so the
 * strip stopped short of the pane's bottom and the message scrolled through
 * the gap underneath it. The owner saw it in the live pilot. A negative margin
 * does not fix it: it moves the box, not the edge sticky pins to.
 *
 * So the row is a real `flex: none` last child of the pane's column, after the
 * scrollport rather than within it. "The bottom of the column" is then a fact
 * of the layout with no coordinate to miss, and there is no gap for anything
 * to show through because there is nothing below it. The scroll content keeps
 * its own padding, inside the scrollport where it belongs.
 *
 * `ConversationView` publishes which message to act on (`onReplyTarget`)
 * rather than rendering the row itself — see that prop for why a value and not
 * a node.
 */

export interface ReplyRowProps {
  /** Plain reply. */
  readonly onReply: () => void;
  /** Reply to everyone. */
  readonly onReplyAll: () => void;
  readonly onForward: () => void;
  /**
   * Whether reply-all would reach anyone a plain reply would not.
   *
   * False DROPS the reply-all button: a control that would produce the
   * identical draft is a choice with no difference, and Gmail omits it too.
   *
   * The single-message reader passes `true` unconditionally, which preserves
   * exactly what it did before this row was shared — it always offered both,
   * and narrowing that silently would be a behaviour change smuggled in under
   * a refactor.
   */
  readonly severalRecipients?: boolean;
}

export function ReplyRow({
  onReply,
  onReplyAll,
  onForward,
  severalRecipients = true,
}: ReplyRowProps): React.JSX.Element {
  const { t } = useTranslation();
  /*
   * Which reply comes FIRST (E5 `defaultReplyBehavior`, canon §2.3).
   *
   * Read from the provider rather than threaded as a prop: one enum reaching
   * one row, and `usePrefs` falls back to Gmail's own default outside a
   * provider so every existing test keeps working unchanged. The `r` key
   * follows the same preference in `MailScreen`, so the button the eye lands
   * on and the key the hand reaches for agree.
   *
   * It is an ORDER swap, never a conditional render: the preference moves the
   * emphasis and the default, it does not remove a control (P4).
   */
  const { prefs } = usePrefs();

  const verbs = severalRecipients
    ? prefs.defaultReplyBehavior === "replyAll"
      ? (["replyAll", "reply"] as const)
      : (["reply", "replyAll"] as const)
    : (["reply"] as const);

  return (
    <div className={styles.row} role="group" aria-label={t("action.reply")}>
      {verbs.map((verb) => (
        <button
          key={verb}
          type="button"
          className={styles.pill}
          onClick={verb === "replyAll" ? onReplyAll : onReply}
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
      <button type="button" className={styles.pill} onClick={onForward}>
        <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
          <path d="M12 5.5l4.5 4-4.5 4" />
          <path d="M16.2 9.5H9.8a5.3 5.3 0 0 0-5.3 5.3v.7" />
        </svg>
        {t("action.forward")}
      </button>
    </div>
  );
}
