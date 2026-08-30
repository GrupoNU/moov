import { useTranslation } from "../../i18n/I18nProvider";
import type { Label } from "../../mail/labelStore";
import type { Mailbox } from "../../mail/types";
import { LabelMenu } from "./LabelMenu";
import { MoveMenu } from "./MoveMenu";
import { SnoozeMenu } from "./SnoozeMenu";
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
  /** E2: report spam, or — inside Junk — take it back out. */
  readonly onToggleSpam: () => void;
  /** True when the current folder IS Junk, which flips the spam control. */
  readonly inJunk: boolean;

  // --- E8: "Label as" ---
  readonly labels: readonly Label[];
  /** The keyword maps of the selected messages, for the menu's tri-state. */
  readonly labelSelection: readonly (Readonly<Record<string, boolean>> | undefined)[];
  readonly onToggleLabel: (keyword: string, apply: boolean) => void;
  readonly onManageLabels: () => void;
  /** Publishes the menu's `open()` so the `l` shortcut can raise it. */
  readonly onLabelMenuReady?: ((open: () => void) => void) | undefined;

  // --- E4: snooze and mute (canon §2.2) ---
  /**
   * Snoozes the selection until an instant, or absent when this server has no
   * triage capability.
   *
   * Absent removes the control entirely rather than disabling it: a vendor
   * capability the server does not advertise is a feature that does not exist
   * here, and a permanently greyed-out button invites the user to hunt for the
   * selection that would enable it.
   */
  readonly onSnooze?: ((until: string) => void) | undefined;
  /** Publishes the snooze menu's `open()` so `b` can raise it. */
  readonly onSnoozeMenuReady?: ((open: () => void) => void) | undefined;
  /** Mutes or unmutes the selection. Absent for the same reason as `onSnooze`. */
  readonly onToggleMute?: (() => void) | undefined;
  /**
   * True when every selected conversation is already muted, so the control can
   * say what the click will DO rather than what the state is.
   */
  readonly allMuted?: boolean;
  /** E4: in the Snoozed view, "bring these back now" replaces nothing else. */
  readonly onUnsnooze?: (() => void) | undefined;
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
  onToggleSpam,
  inJunk,
  labels,
  labelSelection,
  onToggleLabel,
  onManageLabels,
  onLabelMenuReady,
  onSnooze,
  onSnoozeMenuReady,
  onToggleMute,
  allMuted = false,
  onUnsnooze,
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

      {/*
        E2: report spam. The label FLIPS inside Junk — "Not spam" there — so
        one control covers both directions, which is Gmail's shape and what
        the `!` key does. Two separate buttons would leave one of them dead in
        every folder.
      */}
      <ActionButton
        label={inJunk ? t("action.notSpam") : t("action.spam")}
        disabled={disabled}
        onClick={onToggleSpam}
        icon={
          inJunk ? (
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M10 2.6l6.6 3.5v4c0 3.7-2.8 6.4-6.6 7.3-3.8-.9-6.6-3.6-6.6-7.3v-4z" />
              <path d="M7.2 9.9l2 2 3.6-3.8" />
            </svg>
          ) : (
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M10 2.6l6.6 3.5v4c0 3.7-2.8 6.4-6.6 7.3-3.8-.9-6.6-3.6-6.6-7.3v-4z" />
              <path d="M10 7v4M10 13.6v.1" />
            </svg>
          )
        }
      />

      {/*
        E4: snooze (`b`). It sits with archive, delete and spam because it
        belongs to the same family — the verbs that make a conversation LEAVE
        the list — rather than with move and label, which relocate or annotate
        it. Gmail's own hover strip groups it exactly this way.
      */}
      {onSnooze !== undefined && (
        <SnoozeMenu
          disabled={disabled}
          onSnooze={onSnooze}
          onReady={onSnoozeMenuReady}
          triggerClassName={styles.action}
          triggerContent={
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <circle cx="10" cy="10.5" r="6.8" />
              <path d="M10 6.8v3.9l2.6 1.6" />
            </svg>
          }
        />
      )}

      {/*
        E4: bring a snoozed conversation back now. Present only in the Snoozed
        view, where it is the one thing a user does to a row — everywhere else
        there is nothing snoozed to bring back.
      */}
      {onUnsnooze !== undefined && (
        <ActionButton
          label={t("snooze.unsnooze")}
          disabled={disabled}
          onClick={onUnsnooze}
          icon={
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M3.4 10.5a6.6 6.6 0 1 1 2 4.7" />
              <path d="M3 6.4v4.1h4.1" />
            </svg>
          }
        />
      )}

      {/*
        E4: mute (`m`). Its LABEL flips with the selection's state, the same
        rule the spam button follows: one control covering both directions,
        saying what the click will do rather than what the state is.
      */}
      {onToggleMute !== undefined && (
        <ActionButton
          label={allMuted ? t("mute.unmute") : t("mute.action")}
          disabled={disabled}
          onClick={onToggleMute}
          icon={
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M4 7.5h2.6L10 4.6v10.8L6.6 12.5H4a1 1 0 0 1-1-1v-3a1 1 0 0 1 1-1z" />
              {allMuted ? (
                <path d="M13.2 8.2a3.2 3.2 0 0 1 0 4.6" />
              ) : (
                <path d="M13.4 7.6l3.6 4.8m0-4.8l-3.6 4.8" />
              )}
            </svg>
          }
        />
      )}

      <MoveMenu
        mailboxes={mailboxes}
        currentMailboxId={currentMailboxId}
        disabled={disabled}
        onMove={onMove}
        triggerClassName={styles.action}
        triggerContent={
          <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
            <path d="M2.8 5.4a1.4 1.4 0 0 1 1.4-1.4h3l1.6 2h6a1.4 1.4 0 0 1 1.4 1.4v7.2a1.4 1.4 0 0 1-1.4 1.4H4.2a1.4 1.4 0 0 1-1.4-1.4z" />
          </svg>
        }
      />

      {/*
        E8: "Label as" (`l`). It sits next to "Move to" (`v`) because the two
        are the pair a user chooses between — file it in a folder, or tag it
        across folders — and canon §2.7 binds them to adjacent keys for the
        same reason.
      */}
      <LabelMenu
        labels={labels}
        selection={labelSelection}
        disabled={disabled}
        onToggle={onToggleLabel}
        onManage={onManageLabels}
        onReady={onLabelMenuReady}
        triggerClassName={styles.action}
        triggerContent={
          <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
            <path d="M3.4 8.6V4.4a1 1 0 0 1 1-1h4.2l7.6 7.6a1.2 1.2 0 0 1 0 1.7l-4.5 4.5a1.2 1.2 0 0 1-1.7 0L3.4 9.6z" />
            <circle cx="6.9" cy="6.9" r="1.1" fill="currentColor" stroke="none" />
          </svg>
        }
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
