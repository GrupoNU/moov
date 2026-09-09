import { useTranslation } from "../../i18n/I18nProvider";
import { PopupMenu } from "../mail/PopupMenu";
import styles from "./EmojiPicker.module.css";

/**
 * The composer's emoji picker (D-03; canon 07 §7's footer row).
 *
 * # Why this is thirty characters and not a library
 *
 * Gmail's picker is a searchable, categorised, skin-tone-aware grid of the full
 * Unicode emoji set. Reproducing it means either shipping an emoji database —
 * the smallest credible ones are hundreds of kilobytes — or a dependency, and
 * this repo takes neither for a control whose realistic use is "put a 🙂 in a
 * reply". The rule the review is applying is Gmail's SHAPE, not Gmail's
 * inventory: the gap a user feels is that there is no way to insert an emoji at
 * all, not that the grid stops at thirty.
 *
 * So this is a small grid of the ones people actually send, and it is honest
 * about being that: no search box promising a set that is not here, no
 * categories with one row each. When a real picker is worth its weight it
 * replaces this file and nothing else changes — the insertion seam is one
 * callback.
 *
 * # Insertion, and why the caller does it
 *
 * The picker does not touch the document. It hands a character up and
 * `Composer` inserts it at the caret through the same `insertPlainText` the
 * paste handler uses — which is the one path that knows about the rich
 * surface's selection, the textarea's selection, and the sanitizer round trip
 * that has to follow. A picker that wrote into the DOM itself would be a second
 * such path, and the second one is always the one that gets the sanitizer
 * wrong.
 */

/**
 * The set on offer.
 *
 * Chosen as the ones that carry meaning in ordinary correspondence — approval,
 * thanks, apology, attention — rather than the most-used list, which is
 * dominated by faces nobody puts in a work email. Grouped loosely so the grid
 * reads left to right: faces, gestures, marks, objects.
 */
const COMMON_EMOJI: readonly string[] = [
  "🙂", "😀", "😅", "😉", "😊", "🤔", "😐", "🙁",
  "👍", "👎", "🙏", "👏", "🤝", "💪", "👋", "🫡",
  "✅", "❌", "❗", "❓", "⚠️", "⭐", "🔥", "💡",
  "📎", "📅", "📌", "📈", "🎉", "☕", "❤️", "🚀",
];

export function EmojiPicker({
  onPick,
  triggerClassName,
}: {
  readonly onPick: (emoji: string) => void;
  readonly triggerClassName: string | undefined;
}): React.JSX.Element {
  const { t } = useTranslation();

  return (
    <PopupMenu
      label={t("compose.emoji")}
      disabled={false}
      triggerClassName={triggerClassName}
      triggerContent={
        <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true" focusable="false">
          <circle cx="10" cy="10" r="7" />
          <circle cx="7.4" cy="8.2" r="0.9" fill="currentColor" stroke="none" />
          <circle cx="12.6" cy="8.2" r="0.9" fill="currentColor" stroke="none" />
          <path d="M6.8 12.2a4 4 0 0 0 6.4 0" strokeLinecap="round" />
        </svg>
      }
    >
      {(close) => (
        <li role="none" className={styles.gridCell}>
          {/*
            One grid inside one menu item rather than thirty-two `menuitem`s.

            A menu whose every row is a single character is a menu the arrow
            keys walk through one emoji at a time — thirty-two presses to reach
            the last one. The grid is a `group` of buttons instead, which Tab
            and the arrow keys traverse the way a keyboard user expects of a
            palette, and each button carries the character as its accessible
            name because that is the only name it has.
          */}
          <div className={styles.grid} role="group" aria-label={t("compose.emoji")}>
            {COMMON_EMOJI.map((emoji) => (
              <button
                key={emoji}
                type="button"
                className={styles.emoji}
                aria-label={emoji}
                /* mousedown+preventDefault, as the formatting buttons do: a
                   click lands after the editable has lost focus and collapsed
                   its selection, so the insertion would go nowhere. */
                onMouseDown={(event) => {
                  event.preventDefault();
                }}
                onClick={() => {
                  onPick(emoji);
                  close();
                }}
              >
                {emoji}
              </button>
            ))}
          </div>
        </li>
      )}
    </PopupMenu>
  );
}
