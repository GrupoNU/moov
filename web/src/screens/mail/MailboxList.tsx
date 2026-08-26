import { useMemo } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import {
  badgeCount,
  buildMailboxTree,
  mailboxSegment,
  type MailboxNode,
} from "../../mail/mailboxes";
import type { Mailbox, MailboxRole } from "../../mail/types";
import { mailboxLabel } from "./mailboxLabels";
import styles from "./MailboxList.module.css";

/**
 * The folder sidebar (P2 deliverable 2).
 *
 * # Why a `tree` and not a `list`
 *
 * Mailboxes nest — the pilot's own test account has a folder with five
 * children, and real accounts reach 24 folders. A flat `<ul>` of links would
 * render the nesting as indentation only, which a screen reader cannot see. So
 * the sidebar is an ARIA `tree`: every row carries `aria-level` and
 * `aria-selected`, and a parent carries `aria-expanded`. That is the standard
 * pattern for exactly this shape, and it costs three attributes.
 *
 * The DOM stays FLAT (a single list with indentation from `--depth`) even
 * though the semantics are a tree. Nested `<ul>`s would make keyboard
 * navigation a traversal and make the indent a function of markup depth rather
 * than of data — with a deep tree, that is how a sidebar ends up 300px wide.
 */

export interface MailboxListProps {
  readonly mailboxes: readonly Mailbox[];
  /** The mailbox currently being viewed, if any. */
  readonly selectedId: string | undefined;
  readonly onSelect: (mailbox: Mailbox) => void;
  readonly isLoading?: boolean;
}

/**
 * Localised names for the folders whose names Dovecot supplies in English.
 *
 * The mapping itself lives in `mailboxLabels.ts` because P3's move menu needs
 * the same one, and two copies would drift the first time a role is added.
 */
function useRoleName(): (mailbox: Mailbox) => string {
  const { t } = useTranslation();
  return (mailbox: Mailbox): string => mailboxLabel(mailbox, t);
}

/** An inline icon per role, so folders are recognisable before they are read. */
function MailboxIcon({ role }: { readonly role: MailboxRole | null }): React.JSX.Element {
  // One 20x20 grid, stroked with currentColor, so every icon shares a weight
  // and inherits the row's colour (including the selected state).
  const path = ICON_PATHS[role ?? "folder"] ?? ICON_PATHS.folder;
  return (
    <svg
      className={styles.icon}
      viewBox="0 0 20 20"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {path}
    </svg>
  );
}

const ICON_PATHS: Readonly<Record<string, React.JSX.Element>> = {
  inbox: (
    <>
      <path d="M2.5 11.5h4l1.2 2h4.6l1.2-2h4" />
      <path d="M4.3 4.2h11.4l1.8 7.3v4a1 1 0 0 1-1 1H3.5a1 1 0 0 1-1-1v-4z" />
    </>
  ),
  drafts: (
    <>
      <path d="M13.2 2.9l3.9 3.9-9 9-4.6.7.7-4.6z" />
      <path d="M11.6 4.5l3.9 3.9" />
    </>
  ),
  sent: <path d="M17.5 2.5L9 11m8.5-8.5l-5.6 15-2.9-6.5L2.5 8.1z" />,
  archive: (
    <>
      <rect x="2.5" y="3.5" width="15" height="4" rx="1" />
      <path d="M4 7.5v8a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1v-8M8 10.5h4" />
    </>
  ),
  junk: (
    <>
      <path d="M10 2.5l7.5 4v4c0 4-3.2 6.8-7.5 7.5C5.7 17.3 2.5 14.5 2.5 10.5v-4z" />
      <path d="M10 7v3.5M10 13.2v.1" />
    </>
  ),
  trash: (
    <>
      <path d="M3.5 5.5h13M8 5.5V3.8a1 1 0 0 1 1-1h2a1 1 0 0 1 1 1v1.7" />
      <path d="M5 5.5l.8 10.2a1 1 0 0 0 1 .9h6.4a1 1 0 0 0 1-.9L15 5.5" />
    </>
  ),
  all: (
    <>
      <rect x="2.5" y="4.5" width="15" height="11" rx="1.5" />
      <path d="M2.5 6.5L10 11l7.5-4.5" />
    </>
  ),
  flagged: (
    <path d="M10 2.6l2.3 4.7 5.2.8-3.8 3.7.9 5.2-4.6-2.4-4.6 2.4.9-5.2L2.5 8.1l5.2-.8z" />
  ),
  folder: <path d="M2.5 5.4a1 1 0 0 1 1-1h3.4l1.8 2h7.8a1 1 0 0 1 1 1v7.2a1 1 0 0 1-1 1h-13a1 1 0 0 1-1-1z" />,
};

export function MailboxList({
  mailboxes,
  selectedId,
  onSelect,
  isLoading = false,
}: MailboxListProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const roleName = useRoleName();

  // Sorting 24 folders on every keystroke elsewhere in the app would be
  // wasteful; the tree only changes when the mailboxes do.
  const tree = useMemo(() => buildMailboxTree(mailboxes), [mailboxes]);

  if (isLoading && mailboxes.length === 0) {
    return <SidebarSkeleton />;
  }

  return (
    <ul className={styles.tree} role="tree" aria-label={t("shell.mailboxes")}>
      {tree.map((node) => (
        <MailboxRow
          key={node.mailbox.id}
          node={node}
          isSelected={node.mailbox.id === selectedId}
          name={roleName(node.mailbox)}
          onSelect={onSelect}
          formatUnread={(count) => format("mailbox.unreadCount", count)}
        />
      ))}
    </ul>
  );
}

interface MailboxRowProps {
  readonly node: MailboxNode;
  readonly isSelected: boolean;
  readonly name: string;
  readonly onSelect: (mailbox: Mailbox) => void;
  readonly formatUnread: (count: number) => string;
}

function MailboxRow({
  node,
  isSelected,
  name,
  onSelect,
  formatUnread,
}: MailboxRowProps): React.JSX.Element {
  const { mailbox, depth } = node;
  const badge = badgeCount(mailbox);
  const hasUnread = mailbox.unreadEmails > 0 && mailbox.role !== "sent";

  return (
    <li
      role="treeitem"
      aria-level={depth + 1}
      aria-selected={isSelected}
      // A parent is always rendered expanded in P2 (there is no collapse yet),
      // and saying so is required for a valid tree — omitting it would tell a
      // screen reader the children are hidden.
      {...(node.hasChildren ? { "aria-expanded": true } : {})}
      className={styles.item}
    >
      <a
        className={[styles.row, isSelected ? styles.selected : "", hasUnread ? styles.unread : ""]
          .filter(Boolean)
          .join(" ")}
        // A real href, so the folder is a LINK: middle-click opens a tab,
        // Cmd-click opens a background tab, and the status bar shows where it
        // goes. A div with an onClick would silently take all of that away.
        href={`/mail/${encodeURIComponent(mailboxSegment(mailbox))}`}
        onClick={(event) => {
          // Let the browser handle any modified click — that is the whole
          // point of using an anchor.
          if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
          event.preventDefault();
          onSelect(mailbox);
        }}
        style={{ paddingLeft: `calc(var(--space-3) + ${depth} * var(--space-4))` }}
        {...(isSelected ? { "aria-current": "page" as const } : {})}
      >
        <MailboxIcon role={mailbox.role} />
        <span className={styles.name}>{name}</span>
        {badge !== undefined && (
          <span
            className={styles.badge}
            /* The visible number is decorative for assistive tech — the
             * accessible name below states what it counts, because "623"
             * announced alone is meaningless. */
            aria-hidden="true"
          >
            {badge > 999 ? "999+" : badge}
          </span>
        )}
        {badge !== undefined && <span className="visually-hidden">{formatUnread(badge)}</span>}
      </a>
    </li>
  );
}

/** The loading state: P1's honest skeleton, kept because it was right. */
function SidebarSkeleton(): React.JSX.Element {
  const { t } = useTranslation();
  return (
    <ul className={styles.skeletonList} aria-busy="true" aria-label={t("app.loading")}>
      {[68, 54, 74, 46, 62, 58].map((width, index) => (
        <li key={index} className={styles.skeletonRow} aria-hidden="true">
          <span className={styles.skeletonDot} />
          <span className={styles.skeletonBar} style={{ width: `${width}%` }} />
        </li>
      ))}
    </ul>
  );
}
