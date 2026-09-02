import { useTranslation } from "../../i18n/I18nProvider";
import styles from "./ComposeButton.module.css";

/**
 * "Redactar" — the top of the left rail (canon 07 §2).
 *
 * # Why it lives here and not in the toolbar
 *
 * E12 shipped it inside `ActionBar`, in the chrome strip above the list. That
 * put the single most-used control in the product somewhere Gmail never puts
 * it. Canon 07 §2 is explicit: the pill is the FIRST thing in the rail, between
 * the logo row and "Recibidos", and it is "the most prominent control on the
 * page". For a user who can drive Gmail blind, that position is muscle memory —
 * the hand goes to the top-left corner before the eye arrives.
 *
 * It is also the reason this is its own component rather than more JSX in
 * `MailScreen`: the rail's collapsed state changes what the control IS (a pill
 * with a label, or a round icon button), and that branch deserves a name and a
 * test rather than being a ternary buried in a 4,000-line file.
 *
 * # The collapsed form
 *
 * With the rail collapsed to icons, Gmail keeps the button and drops the word:
 * it becomes a round pencil button of the same visual weight. The label does
 * not vanish — it moves to `aria-label` and `title`, so the control keeps its
 * accessible name for a screen reader and gets a tooltip for a pointer. A
 * collapsed rail must not silently cost a user the ability to know what a
 * button does.
 */

export interface ComposeButtonProps {
  readonly onCompose: () => void;
  /** The rail is collapsed to icons; see `MailboxListProps.collapsed`. */
  readonly collapsed?: boolean;
}

export function ComposeButton({
  onCompose,
  collapsed = false,
}: ComposeButtonProps): React.JSX.Element {
  const { t } = useTranslation();
  const label = t("compose.new");

  return (
    <button
      type="button"
      className={[styles.compose, collapsed ? styles.composeCollapsed : ""]
        .filter(Boolean)
        .join(" ")}
      onClick={onCompose}
      /*
       * The accessible name is ALWAYS the word, whether or not it is painted.
       * When the pill shows its label the attribute agrees with the visible
       * text; when collapsed it is the only name the control has.
       */
      aria-label={label}
      title={label}
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
        <path d="M13.6 3.6l2.8 2.8L7.6 15.2 4 16l.8-3.6z" />
      </svg>
      {/*
        The label is removed from the TREE when collapsed rather than hidden
        with CSS. A visually-hidden span would still be read by a screen reader,
        which — next to the `aria-label` above — would announce the word twice.
      */}
      {!collapsed && <span className={styles.label}>{label}</span>}
    </button>
  );
}
