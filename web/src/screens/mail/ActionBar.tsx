import { useCallback, useEffect, useRef, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { buildMailboxTree } from "../../mail/mailboxes";
import { mailboxLabelKey } from "./mailboxLabels";
import type { Mailbox } from "../../mail/types";
import styles from "./ActionBar.module.css";

/**
 * The action bar above the message list: bulk actions on the current
 * selection, and the folder menu.
 *
 * # Why it is always present, not conditional on a selection
 *
 * A toolbar that appears when you select something and vanishes when you do
 * not makes the list jump by its own height every time — and moves whatever
 * you were about to click. It stays, and its buttons disable instead, which is
 * also what lets a screen reader user discover the available actions before
 * committing to a selection.
 *
 * Disabled buttons keep their accessible names; `aria-disabled` is NOT used in
 * place of `disabled`, because a genuinely inert control should also be
 * unfocusable — the selection is the thing to fix, and it is one key away.
 */

export interface ActionBarProps {
  readonly selectedCount: number;
  readonly totalCount: number;
  readonly allSelected: boolean;
  readonly onSelectAll: (selected: boolean) => void;
  readonly onMarkRead: () => void;
  readonly onMarkUnread: () => void;
  readonly onFlag: () => void;
  readonly onArchive: () => void;
  readonly onDelete: () => void;
  readonly onMove: (mailboxId: string) => void;
  readonly mailboxes: readonly Mailbox[];
  /** The folder currently shown — excluded from the move menu. */
  readonly currentMailboxId: string | undefined;
  /** True when `delete` will erase rather than move to Trash (W-A2). */
  readonly deleteIsPermanent: boolean;
  readonly onCompose: () => void;
  readonly isBusy: boolean;
}

export function ActionBar({
  selectedCount,
  totalCount,
  allSelected,
  onSelectAll,
  onMarkRead,
  onMarkUnread,
  onFlag,
  onArchive,
  onDelete,
  onMove,
  mailboxes,
  currentMailboxId,
  deleteIsPermanent,
  onCompose,
  isBusy,
}: ActionBarProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const hasSelection = selectedCount > 0;
  const disabled = !hasSelection || isBusy;

  return (
    <div className={styles.bar} role="toolbar" aria-label={t("action.more")}>
      <button type="button" className={styles.compose} onClick={onCompose}>
        <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
          <path d="M13.6 3.6l2.8 2.8L7.6 15.2 4 16l.8-3.6z" />
        </svg>
        {t("compose.new")}
      </button>

      <span className={styles.divider} aria-hidden="true" />

      <label className={styles.selectAll}>
        <input
          type="checkbox"
          checked={allSelected && totalCount > 0}
          // The indeterminate box is the honest state for "some but not all";
          // a plain unchecked box would claim nothing is selected.
          ref={(node) => {
            if (node !== null) {
              node.indeterminate = hasSelection && !allSelected;
            }
          }}
          disabled={totalCount === 0}
          onChange={(event) => {
            onSelectAll(event.target.checked);
          }}
        />
        <span className="visually-hidden">{t("action.selectAll")}</span>
      </label>

      <ActionButton
        label={t("action.markRead")}
        disabled={disabled}
        onClick={onMarkRead}
        icon={
          <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
            <path d="M2.8 6.2l7.2 5 7.2-5" />
            <rect x="2.8" y="4.5" width="14.4" height="11" rx="1.6" />
          </svg>
        }
      />
      <ActionButton
        label={t("action.markUnread")}
        disabled={disabled}
        onClick={onMarkUnread}
        icon={
          <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
            <rect x="2.8" y="4.5" width="14.4" height="11" rx="1.6" />
            <circle cx="15.4" cy="5.6" r="2.6" fill="currentColor" stroke="none" />
          </svg>
        }
      />
      <ActionButton
        label={t("action.flag")}
        disabled={disabled}
        onClick={onFlag}
        icon={
          <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" aria-hidden="true" focusable="false">
            <path d="M10 2.6l2.3 4.7 5.2.8-3.8 3.7.9 5.2-4.6-2.4-4.6 2.4.9-5.2L2.5 8.1l5.2-.8z" />
          </svg>
        }
      />

      <span className={styles.divider} aria-hidden="true" />

      <ActionButton
        label={t("action.archive")}
        disabled={disabled}
        onClick={onArchive}
        icon={
          <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
            <rect x="2.6" y="3.6" width="14.8" height="3.6" rx="1" />
            <path d="M4 7.2v8a1.4 1.4 0 0 0 1.4 1.4h9.2a1.4 1.4 0 0 0 1.4-1.4v-8M8 10.4h4" />
          </svg>
        }
      />
      {/*
        The delete button's LABEL changes with the semantics (arbitration
        W-A2): "Move to Trash" outside Trash, "Delete permanently" inside it.
        One word for both promises would be a lie in one of the two cases.
      */}
      <ActionButton
        label={deleteIsPermanent ? t("action.deleteForever") : t("action.delete")}
        disabled={disabled}
        onClick={onDelete}
        destructive={deleteIsPermanent}
        icon={
          <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
            <path d="M3.6 5.6h12.8M8 5.6V4.2a1 1 0 0 1 1-1h2a1 1 0 0 1 1 1v1.4M5.4 5.6l.7 10a1.4 1.4 0 0 0 1.4 1.3h5a1.4 1.4 0 0 0 1.4-1.3l.7-10" />
          </svg>
        }
      />

      <MoveMenu
        mailboxes={mailboxes}
        currentMailboxId={currentMailboxId}
        disabled={disabled}
        onMove={onMove}
      />

      <span className={styles.spacer} />

      {/* Always in the DOM so the count change is announced, not inserted. */}
      <span className={styles.count} role="status" aria-live="polite">
        {hasSelection ? format("action.selected", selectedCount) : ""}
      </span>
    </div>
  );
}

function ActionButton({
  label,
  icon,
  disabled,
  onClick,
  destructive = false,
}: {
  readonly label: string;
  readonly icon: React.ReactNode;
  readonly disabled: boolean;
  readonly onClick: () => void;
  readonly destructive?: boolean;
}): React.JSX.Element {
  return (
    <button
      type="button"
      className={[styles.action, destructive ? styles.destructive : ""].filter(Boolean).join(" ")}
      disabled={disabled}
      onClick={onClick}
      aria-label={label}
      title={label}
    >
      {icon}
    </button>
  );
}

/**
 * The move-to-folder menu.
 *
 * Implemented as a `menu`/`menuitem` pattern with roving focus and Escape to
 * close — the WAI-ARIA APG menu-button behaviour — rather than a `<select>`,
 * because a select cannot show the folder hierarchy with indentation and
 * cannot be dismissed without choosing something.
 */
function MoveMenu({
  mailboxes,
  currentMailboxId,
  disabled,
  onMove,
}: {
  readonly mailboxes: readonly Mailbox[];
  readonly currentMailboxId: string | undefined;
  readonly disabled: boolean;
  readonly onMove: (mailboxId: string) => void;
}): React.JSX.Element {
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
        className={styles.action}
        disabled={disabled}
        aria-haspopup="menu"
        aria-expanded={isOpen}
        aria-label={t("action.moveTo")}
        title={t("action.moveTo")}
        onClick={() => {
          setOpen((open) => !open);
        }}
      >
        <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
          <path d="M2.8 5.4a1.4 1.4 0 0 1 1.4-1.4h3l1.6 2h6a1.4 1.4 0 0 1 1.4 1.4v7.2a1.4 1.4 0 0 1-1.4 1.4H4.2a1.4 1.4 0 0 1-1.4-1.4z" />
        </svg>
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
