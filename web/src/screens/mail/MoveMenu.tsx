import { useCallback, useEffect, useRef, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { buildMailboxTree } from "../../mail/mailboxes";
import type { Mailbox } from "../../mail/types";
import { mailboxLabelKey } from "./mailboxLabels";
import styles from "./MoveMenu.module.css";

/**
 * The move-to-folder menu.
 *
 * Extracted from `ActionBar` in E2, where the reading pane grew its own
 * move control (item 3). Two copies of an APG menu-button — with its outside
 * click, its Escape, its roving focus — is two copies of the hardest 60 lines
 * in this screen, and they would have drifted the first time one was fixed.
 *
 * Implemented as a `menu`/`menuitem` pattern rather than a `<select>`, because
 * a select cannot show the folder hierarchy with indentation and cannot be
 * dismissed without choosing something.
 *
 * The TRIGGER's appearance is the caller's: the action bar wants an icon
 * button that matches its neighbours, the reader wants a text button that
 * matches its own row. Only the trigger is themed; the popup is identical in
 * both, which is the point of sharing it.
 */

export interface MoveMenuProps {
  readonly mailboxes: readonly Mailbox[];
  /** The folder currently shown — excluded from the menu. */
  readonly currentMailboxId: string | undefined;
  readonly disabled: boolean;
  readonly onMove: (mailboxId: string) => void;
  /** The trigger's class, so each caller keeps its own button styling. */
  readonly triggerClassName: string | undefined;
  /** Rendered inside the trigger: an icon in the bar, a word in the reader. */
  readonly triggerContent: React.ReactNode;
}

export function MoveMenu({
  mailboxes,
  currentMailboxId,
  disabled,
  onMove,
  triggerClassName,
  triggerContent,
}: MoveMenuProps): React.JSX.Element {
  const { t } = useTranslation();
  const [isOpen, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const buttonRef = useRef<HTMLButtonElement | null>(null);

  const close = useCallback((): void => {
    setOpen(false);
    buttonRef.current?.focus();
  }, []);

  // Click outside and Escape both dismiss. Both are required: a menu that only
  // closes on Escape traps a mouse user, and one that only closes on an
  // outside click traps a keyboard user.
  useEffect(() => {
    if (!isOpen) return undefined;
    const onPointerDown = (event: PointerEvent): void => {
      if (containerRef.current?.contains(event.target as Node) !== true) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === "Escape") {
        event.stopPropagation();
        close();
      }
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [isOpen, close]);

  const targets = buildMailboxTree(mailboxes).filter(
    (node) => node.mailbox.id !== currentMailboxId && node.mailbox.myRights.mayAddItems,
  );

  return (
    <div className={styles.menuWrap} ref={containerRef}>
      <button
        ref={buttonRef}
        type="button"
        className={triggerClassName}
        disabled={disabled}
        aria-haspopup="menu"
        aria-expanded={isOpen}
        aria-label={t("action.moveTo")}
        title={t("action.moveTo")}
        onClick={() => {
          setOpen((open) => !open);
        }}
      >
        {triggerContent}
      </button>

      {isOpen && (
        <ul className={styles.menu} role="menu" aria-label={t("action.moveTo")}>
          {targets.map((node, index) => (
            <li key={node.mailbox.id} role="none">
              <button
                type="button"
                role="menuitem"
                className={styles.menuItem}
                style={{ paddingLeft: `calc(var(--space-3) + ${node.depth} * var(--space-4))` }}
                /* The APG menu-button pattern requires focus to move into the
                   menu when it opens; without it the menu is unusable by
                   keyboard. */
                // eslint-disable-next-line jsx-a11y/no-autofocus
                autoFocus={index === 0}
                onClick={() => {
                  onMove(node.mailbox.id);
                  close();
                }}
              >
                <MailboxName mailbox={node.mailbox} />
              </button>
            </li>
          ))}
          {targets.length === 0 && (
            <li role="none">
              <span className={styles.menuEmpty}>{t("list.empty")}</span>
            </li>
          )}
        </ul>
      )}
    </div>
  );
}

/** A mailbox's display name: the translated role name, or the server's own. */
function MailboxName({ mailbox }: { readonly mailbox: Mailbox }): React.JSX.Element {
  const { t } = useTranslation();
  const key = mailboxLabelKey(mailbox.role);
  return <>{key === undefined ? mailbox.name : t(key)}</>;
}
