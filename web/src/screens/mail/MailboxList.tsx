import { Fragment, useMemo, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import {
  badgeCount,
  buildMailboxTree,
  mailboxSegment,
  type MailboxNode,
} from "../../mail/mailboxes";
import {
  curateRail,
  disambiguateName,
  normalizeFolderName,
  orderMore,
} from "../../mail/railCuration";
import type { FolderVisibility } from "../../mail/prefs";
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
  /**
   * E2 item 7: "Empty trash now", rendered under the Trash row.
   *
   * Absent means no affordance at all — the caller passes it only while Trash
   * is the folder on screen, because a permanently visible irreversible bulk
   * destroy in a sidebar is a mis-click waiting to happen.
   */
  readonly onEmptyTrash?: ((trash: Mailbox) => void) | undefined;
  readonly isEmptyingTrash?: boolean;
  /**
   * E9: the Outbox, as a virtual folder.
   *
   * Absent means the queue is empty and the row is not drawn at all — Gmail's
   * shape (canon §2.10), and the right one: an always-visible Outbox that is
   * always empty is a control that means nothing almost all of the time.
   *
   * It is NOT a `Mailbox`, and the type says so. A queued message has no JMAP
   * id, no unread count and no rights, so passing a fabricated Mailbox through
   * the tree would make every consumer's `mailboxes.find(...)` able to return a
   * folder the server has never heard of.
   */
  readonly outbox?: {
    readonly count: number;
    readonly hasFailures: boolean;
    readonly isSelected: boolean;
    readonly onSelect: () => void;
  };
  /**
   * "Destacados" — the starred view (canon 07 §2, owner's finding 3).
   *
   * ALWAYS drawn, unlike the Outbox and Scheduled rows below. Gmail lists it
   * second in the rail whether or not anything is starred, and that is the
   * point: the entry is how a user learns the star leads somewhere. A row that
   * appeared only once you had already starred something could never teach it.
   *
   * No count. Gmail shows none here either, and the honest reason is that we
   * have nothing cheap to show: JMAP publishes an unread count per MAILBOX,
   * and there is no per-keyword counter — producing one would mean an extra
   * query on every load to badge a row nobody asked to have badged.
   */
  readonly starred?: {
    readonly isSelected: boolean;
    readonly onSelect: () => void;
  };
  /**
   * E4: the Scheduled view (canon §2.3 — Gmail's own left-nav "Scheduled").
   *
   * Like the Outbox, absent means the row is not drawn: nothing is scheduled,
   * so a permanent entry would mean nothing almost all of the time. Unlike the
   * Outbox, the count is of SERVER-side submissions, which is why the three
   * outgoing destinations stay separate rather than being merged into one
   * "pending" folder — they fail differently, they are cancelled differently,
   * and only one of them is local to this browser.
   */
  readonly scheduled?: {
    readonly count: number;
    readonly isSelected: boolean;
    readonly onSelect: () => void;
  };
  /**
   * E4: the NAME of the Snoozed folder, so the sidebar can label and badge it
   * as a first-class destination rather than as one more custom folder.
   *
   * The name rather than an id, because that is how the folder is identified
   * everywhere (there is no RFC 6154 role for snoozed mail); the row is still
   * a real `Mailbox` from the tree, which is what keeps `g b`, deep links and
   * the message list working on it with no special case.
   */
  readonly snoozedMailboxName?: string | undefined;
  /**
   * "Pospuestos" when the Snoozed FOLDER does not exist yet (owner's finding 3).
   *
   * GC-10 makes snoozing a real IMAP move, and `internal/sync/snooze.go`
   * creates the folder on demand — so until the user snoozes for the first
   * time there is no mailbox for the tree to draw, and the entry was simply
   * absent. Gmail shows Pospuestos always.
   *
   * This prop draws the entry in that gap. It navigates to an EMPTY STATE and
   * creates nothing: a client that made a folder just to have a row to point
   * at would be writing to Dovecot — the source of truth — to satisfy its own
   * layout, which is exactly backwards. The folder still appears the moment a
   * real snooze creates it, at which point the tree draws it and this row is
   * not passed.
   */
  readonly snoozedPlaceholder?: {
    readonly isSelected: boolean;
    readonly onSelect: () => void;
  };
  /**
   * E12: the rail is collapsed to icons (the hamburger, canon 07 §1-2).
   *
   * ONE class on the `<ul>`, not a different tree. The DOM, the `role="tree"`
   * semantics, every `aria-level` and the visually-hidden state text are all
   * unchanged: a collapsed rail is a VISUAL narrowing, and a screen reader must
   * still hear "Inbox, 12 unread" from a row whose label the sighted user has
   * folded away. Rendering a second, icon-only tree would have meant two places
   * for a folder row to be wrong.
   *
   * The name survives as the row's `title`, so a pointer user can recover it by
   * hovering rather than by expanding.
   */
  readonly collapsed?: boolean;
  /**
   * P0-5: the user's per-folder rail visibility, keyed by mailbox NAME.
   *
   * From the server's `folderVisibility` preference (schema v3). Omitted — the
   * case on a v2 server, and the case for every account that has never touched
   * the settings table — means the policy in `railCuration.ts` decides alone,
   * which is the correct fallback rather than a degraded one: the policy is
   * the DEFAULT, and an empty map is exactly "no choices have been made".
   */
  readonly folderVisibility?: Readonly<Record<string, FolderVisibility>>;
  /**
   * P0-5: whether "Más" is open, and how to remember the answer.
   *
   * Lifted rather than kept here so the state survives this component
   * remounting (a route change does that) and so the localStorage read happens
   * once in the shell, beside the rail's own collapse. Omitted, the section
   * still works — it just forgets between mounts, which is what a test wants.
   */
  readonly moreOpen?: boolean;
  readonly onToggleMore?: (() => void) | undefined;
}

const NO_FOLDER_VISIBILITY: Readonly<Record<string, FolderVisibility>> = {};

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

/**
 * An inline icon per role, so folders are recognisable before they are read.
 *
 * `iconKey` overrides the role, and E4 is why it exists: the Snoozed folder has
 * no role to key on (RFC 6154 defines none and the sync engine refused to
 * invent one), so it is recognised by NAME upstream and told which icon to
 * wear here. A clock, which is what the folder is about.
 */
function MailboxIcon({
  role,
  iconKey,
}: {
  /*
   * Optional so a caller with no mailbox behind it — the Scheduled row — can
   * omit it and pass only `iconKey`. Writing `role={null}` there instead is
   * what a first version did, and jsx-a11y correctly reads a literal `role`
   * prop as an ARIA role and rejects `null` as one; the lint was right about
   * the shape even though this is not a DOM element.
   */
  readonly role?: MailboxRole | null | undefined;
  readonly iconKey?: string | undefined;
}): React.JSX.Element {
  // One 20x20 grid, stroked with currentColor, so every icon shares a weight
  // and inherits the row's colour (including the selected state).
  const path = ICON_PATHS[iconKey ?? role ?? "folder"] ?? ICON_PATHS.folder;
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
  // E4: a clock, for the Snoozed folder — the only thing snoozed mail is about.
  snoozed: (
    <>
      <circle cx="10" cy="10.5" r="6.8" />
      <path d="M10 6.8v3.9l2.6 1.6" />
    </>
  ),
  // E4: a clock over an outbound arrow, for Scheduled — the same clock as
  // Snoozed, because both are "later", with the direction that tells them apart.
  scheduled: (
    <>
      <circle cx="10" cy="11" r="6.3" />
      <path d="M10 7.6v3.6l2.4 1.4" />
      <path d="M16.2 4.4l-2.6 2.6m2.6-2.6h-2.4m2.4 0v2.4" />
    </>
  ),
};

export function MailboxList({
  mailboxes,
  selectedId,
  onSelect,
  isLoading = false,
  onEmptyTrash,
  isEmptyingTrash = false,
  outbox,
  scheduled,
  starred,
  snoozedMailboxName,
  snoozedPlaceholder,
  collapsed = false,
  folderVisibility = NO_FOLDER_VISIBILITY,
  moreOpen,
  onToggleMore,
}: MailboxListProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const roleName = useRoleName();

  /*
   * P0-5: "Más" keeps its own state when the shell does not lift it.
   *
   * Closed by default either way — Gmail's shape, and the point of the
   * collapse. A caller that passes `moreOpen` owns it (and persists it); one
   * that does not gets a working section that forgets on remount, which is
   * what the tests and any future embedding want.
   */
  const [localMoreOpen, setLocalMoreOpen] = useState(false);
  const isMoreOpen = moreOpen ?? localMoreOpen;
  const toggleMore = (): void => {
    if (onToggleMore !== undefined) onToggleMore();
    else setLocalMoreOpen((open) => !open);
  };

  // Sorting 24 folders on every keystroke elsewhere in the app would be
  // wasteful; the tree only changes when the mailboxes do.
  const tree = useMemo(() => buildMailboxTree(mailboxes), [mailboxes]);

  /*
   * P0-5: the rail, curated. Three steps, each a pure function tested on its
   * own in `railCuration.test.ts`:
   *
   *   1. drop what the policy (or the user) hides,
   *   2. split into the canonical rows and the rest,
   *   3. order the rest so Archivo/Spam/Papelera lead.
   *
   * Memoized together because they are one derivation of one input; splitting
   * them into three `useMemo`s would buy nothing and let them drift apart.
   */
  const { primary, more } = useMemo(() => {
    const split = curateRail(
      tree,
      (node) => node.mailbox,
      mailboxes,
      folderVisibility,
      snoozedMailboxName,
    );
    return {
      primary: split.primary,
      more: orderMore(split.more, (node) => node.mailbox),
    };
  }, [tree, mailboxes, folderVisibility, snoozedMailboxName]);

  /*
   * P0-5b: the section opens itself when what you are LOOKING AT is inside it.
   *
   * Without this, navigating to Papelera — from the keyboard, a deep link or
   * the reader's delete — leaves the rail with no row marked current and the
   * folder you are in nowhere on screen. That is disorienting in exactly the
   * way the collapse was meant to prevent, and it is also what breaks the
   * "Vaciar la papelera" affordance, which by design renders only on the Trash
   * ROW and only while Trash is open.
   *
   * Derived rather than an effect on selection: an effect would paint one
   * frame with the section shut and then open it, and would also fight a user
   * who deliberately closed it while standing in one of its folders.
   */
  const selectionInMore =
    selectedId !== undefined && more.some((node) => node.mailbox.id === selectedId);
  const showMore = (isMoreOpen || selectionInMore) && !collapsed;

  /*
   * P0-5d: the names a ROLE row already occupies, folded for comparison.
   *
   * Built from the roled mailboxes' DISPLAY labels rather than their raw
   * names, because that is what the user sees: the role folder called
   * "Archive" renders as "Archivo", and it is "Archivo" that a custom folder
   * can collide with.
   */
  const roleLabels = useMemo(() => {
    const taken = new Set<string>();
    for (const mailbox of mailboxes) {
      if (mailbox.role !== null) taken.add(normalizeFolderName(roleName(mailbox)));
    }
    return taken;
    // `roleName` closes over the translator, which is stable per locale.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mailboxes, t]);

  if (isLoading && mailboxes.length === 0) {
    return <SidebarSkeleton />;
  }

  const renderNode = (node: MailboxNode): React.JSX.Element => {
    /*
     * E4: the Snoozed folder is a REAL mailbox (GC-10 makes snoozing an IMAP
     * move), so it comes through the tree like any other and every existing
     * code path — routing, deep links, the message list, `g b` — works on it
     * unchanged. Only two things differ, and both are presentation: it gets
     * the clock icon and a localised name, because Dovecot supplies its name
     * in English exactly as it does for Sent and Drafts.
     */
    const isSnoozed =
      snoozedMailboxName !== undefined && node.mailbox.name === snoozedMailboxName;
    const label = isSnoozed ? t("snooze.mailboxName") : roleName(node.mailbox);
    const row = (
      <MailboxRow
        key={node.mailbox.id}
        node={node}
        isSelected={node.mailbox.id === selectedId}
        /*
         * P0-5d: a custom folder whose name collides with a role's label is
         * qualified, so the rail can never draw two rows reading "Archivo"
         * with no way to tell them apart. The ROLE keeps the plain label — it
         * is the one the move menu, the keyboard and the URL all mean.
         */
        name={disambiguateName(
          node.mailbox,
          label,
          roleLabels,
          mailboxes,
          t("mailbox.customSuffix"),
        )}
        onSelect={onSelect}
        formatUnread={(count) => format("mailbox.unreadCount", count)}
        {...(isSnoozed ? { iconKey: "snoozed" } : {})}
        {...(onEmptyTrash !== undefined && node.mailbox.role === "trash"
          ? { onEmptyTrash, isEmptyingTrash }
          : {})}
      />
    );
    /*
     * Gmail's rail order (canon 07 §2): Recibidos, Destacados, Pospuestos,
     * then Enviados and Borradores. The two virtual entries are emitted right
     * after the Inbox row rather than appended at the end, because their
     * POSITION is the muscle memory — Destacados is "the one under the inbox".
     *
     * Keyed off the inbox ROLE rather than an index, so a server that puts
     * Inbox somewhere else in `sortOrder` still gets them in the right place,
     * and an account with no inbox at all simply does not show them mid-tree.
     */
    if (node.mailbox.role !== "inbox") return row;
    return (
      <Fragment key={node.mailbox.id}>
        {row}
        {starred !== undefined && <StarredRow {...starred} />}
        {snoozedPlaceholder !== undefined && (
          <SnoozedPlaceholderRow {...snoozedPlaceholder} />
        )}
      </Fragment>
    );
  };

  return (
    <ul
      className={[styles.tree, collapsed ? styles.treeCollapsed : ""]
        .filter(Boolean)
        .join(" ")}
      role="tree"
      aria-label={t("shell.mailboxes")}
    >
      {primary.map(renderNode)}
      {/*
        P0-5b: the two outgoing rows stay ABOVE "Más".
        Both are drawn only when non-empty, so they are never noise, and when
        they ARE drawn they are urgent — mail that has not gone out. Burying
        that behind a collapse would be the one place the curation could cost
        a user something real.
      */}
      {outbox !== undefined && <OutboxRow {...outbox} />}
      {scheduled !== undefined && <ScheduledRow {...scheduled} />}

      {/*
        P0-5b/e: "Más", and what it hides.

        Rendered only when there is something behind it — a disclosure that
        reveals nothing is a control that lies. With the rail COLLAPSED the
        section's contents stay closed regardless: canon 07 §2 collapses the
        rail to a handful of DISTINCT icons, and a dozen identical generic
        folder glyphs is precisely the state the owner's screenshot showed.
      */}
      {more.length > 0 && (
        <>
          <MoreToggle open={showMore} onToggle={toggleMore} />
          {showMore && more.map(renderNode)}
        </>
      )}
    </ul>
  );
}

/**
 * The "Más" disclosure (P0-5b, canon 07 §2).
 *
 * A `treeitem` like every other row rather than a `group` with an
 * `aria-expanded` parent, because it is not a folder and has no children in
 * the tree's sense — the rows it reveals are siblings at level 1, exactly
 * where they were before the collapse existed. `aria-expanded` on the button
 * states what it does; the revealed rows are found by continuing down the
 * tree, which is where a screen-reader user is already looking.
 */
function MoreToggle({
  open,
  onToggle,
}: {
  readonly open: boolean;
  readonly onToggle: () => void;
}): React.JSX.Element {
  const { t } = useTranslation();
  const label = open ? t("mailbox.less") : t("mailbox.more");

  return (
    <li role="treeitem" aria-level={1} aria-selected={false} className={styles.item}>
      <button
        type="button"
        className={`${styles.row} ${styles.moreToggle}`}
        onClick={onToggle}
        aria-expanded={open}
        title={label}
      >
        <svg
          className={[styles.chevron, open ? styles.chevronOpen : ""]
            .filter(Boolean)
            .join(" ")}
          viewBox="0 0 20 20"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.6"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden="true"
          focusable="false"
        >
          <path d="M6 8l4 4 4-4" />
        </svg>
        <span className={styles.name}>{label}</span>
      </button>
    </li>
  );
}

/**
 * "Destacados" — the starred row (canon 07 §2).
 *
 * A virtual destination with no `Mailbox` behind it, so it is its own component
 * for the same reason `OutboxRow` is. It reuses the `flagged` star icon the
 * icon table already carries, which is deliberate: the rail entry and the star
 * on every row must be the same mark, or the connection between "I clicked the
 * star" and "they live here" has to be learned instead of seen.
 *
 * No badge. There is no per-keyword unread count in JMAP, and Gmail shows none
 * here either.
 */
function StarredRow({
  isSelected,
  onSelect,
}: {
  readonly isSelected: boolean;
  readonly onSelect: () => void;
}): React.JSX.Element {
  const { t } = useTranslation();

  return (
    <li role="treeitem" aria-level={1} aria-selected={isSelected} className={styles.item}>
      <button
        type="button"
        className={[styles.row, isSelected ? styles.selected : ""].filter(Boolean).join(" ")}
        onClick={onSelect}
        style={{ paddingLeft: "var(--space-3)" }}
        title={t("starred.viewName")}
        {...(isSelected ? { "aria-current": "page" as const } : {})}
      >
        <MailboxIcon iconKey="flagged" />
        <span className={styles.name}>{t("starred.viewName")}</span>
      </button>
    </li>
  );
}

/**
 * "Pospuestos" before the Snoozed folder exists (owner's finding 3).
 *
 * Visually identical to the real folder's row — same clock icon, same name — so
 * that the entry does not appear to CHANGE when the folder is finally created;
 * from the user's side it was always there, which is Gmail's behaviour.
 *
 * It carries no badge because there is nothing to count, and it creates
 * nothing when clicked: it routes to an empty state. Making a folder to justify
 * a row would mean the client writing to Dovecot — the source of truth — for
 * the sake of its own layout.
 */
function SnoozedPlaceholderRow({
  isSelected,
  onSelect,
}: {
  readonly isSelected: boolean;
  readonly onSelect: () => void;
}): React.JSX.Element {
  const { t } = useTranslation();

  return (
    <li role="treeitem" aria-level={1} aria-selected={isSelected} className={styles.item}>
      <button
        type="button"
        className={[styles.row, isSelected ? styles.selected : ""].filter(Boolean).join(" ")}
        onClick={onSelect}
        style={{ paddingLeft: "var(--space-3)" }}
        title={t("snooze.mailboxName")}
        {...(isSelected ? { "aria-current": "page" as const } : {})}
      >
        <MailboxIcon iconKey="snoozed" />
        <span className={styles.name}>{t("snooze.mailboxName")}</span>
      </button>
    </li>
  );
}

/**
 * The Scheduled row (E4).
 *
 * Its own component for the reason `OutboxRow` is: there is no `Mailbox` behind
 * it and its badge counts messages WAITING rather than messages unread. The two
 * are deliberately NOT merged into one "pending" row, even though both list
 * mail that has not gone out: the Outbox is local to this browser and drains
 * when the network returns, while these are server-side submissions with a
 * chosen hour that other devices can see and cancel. One row for both would
 * make "why is this still here?" have two different answers.
 */
function ScheduledRow({
  count,
  isSelected,
  onSelect,
}: {
  readonly count: number;
  readonly isSelected: boolean;
  readonly onSelect: () => void;
}): React.JSX.Element {
  const { t } = useTranslation();

  return (
    <li role="treeitem" aria-level={1} aria-selected={isSelected} className={styles.item}>
      <button
        type="button"
        className={[styles.row, isSelected ? styles.selected : ""].filter(Boolean).join(" ")}
        onClick={onSelect}
        style={{ paddingLeft: "var(--space-3)" }}
        {...(isSelected ? { "aria-current": "page" as const } : {})}
      >
        <MailboxIcon iconKey="scheduled" />
        <span className={styles.name}>{t("schedule.viewName")}</span>
        {count > 0 && (
          <span className={styles.badge} aria-hidden="true">
            {count}
          </span>
        )}
        {/* What the number counts, for the same reason the Outbox states it:
            a bare number after a folder name reads as an unread count. */}
        <span className="visually-hidden">{t("schedule.viewName")}</span>
      </button>
    </li>
  );
}

/**
 * The Outbox row (E9).
 *
 * Its own component rather than a branch inside `MailboxRow`, because almost
 * nothing it draws is the same: there is no `Mailbox`, no href a middle-click
 * could usefully open in a tab (the queue is local to this browser), and the
 * badge counts messages WAITING rather than messages unread. Squeezing it into
 * the folder row would mean five `?? undefined` branches inside a component
 * that is currently easy to read.
 *
 * It renders at `aria-level={1}`, as a sibling of the top-level folders, which
 * is where it belongs: it is a destination, not a child of the inbox.
 */
function OutboxRow({
  count,
  hasFailures,
  isSelected,
  onSelect,
}: {
  readonly count: number;
  readonly hasFailures: boolean;
  readonly isSelected: boolean;
  readonly onSelect: () => void;
}): React.JSX.Element {
  const { t } = useTranslation();

  return (
    <li role="treeitem" aria-level={1} aria-selected={isSelected} className={styles.item}>
      <button
        type="button"
        className={[styles.row, isSelected ? styles.selected : ""].filter(Boolean).join(" ")}
        onClick={onSelect}
        style={{ paddingLeft: "var(--space-3)" }}
        {...(isSelected ? { "aria-current": "page" as const } : {})}
      >
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
          {/* An outbound tray: the inbox icon's arrow, reversed. */}
          <path d="M2.5 11.5h4l1.2 2h4.6l1.2-2h4" />
          <path d="M4.3 4.2h11.4l1.8 7.3v4a1 1 0 0 1-1 1H3.5a1 1 0 0 1-1-1v-4z" />
          <path d="M10 9.5V3.8m0 0L8 5.9m2-2.1l2 2.1" />
        </svg>
        <span className={styles.name}>{t("outbox.name")}</span>
        {count > 0 && (
          <span className={styles.badge} aria-hidden="true">
            {count}
          </span>
        )}
        {/*
          The accessible name states what the number COUNTS. A bare "2" read
          aloud after a folder name is indistinguishable from an unread count,
          and these are messages that have not gone out — a different and more
          urgent fact. A failure is announced as such rather than as a number.
        */}
        <span className="visually-hidden">
          {hasFailures ? t("outbox.failed") : t("outbox.queued")}
        </span>
      </button>
    </li>
  );
}

interface MailboxRowProps {
  readonly node: MailboxNode;
  readonly isSelected: boolean;
  readonly name: string;
  readonly onSelect: (mailbox: Mailbox) => void;
  readonly formatUnread: (count: number) => string;
  /** E2: present only on the Trash row, and only while Trash is on screen. */
  readonly onEmptyTrash?: ((trash: Mailbox) => void) | undefined;
  readonly isEmptyingTrash?: boolean;
  /** E4: an icon override for a folder with no role to key on (Snoozed). */
  readonly iconKey?: string | undefined;
}

function MailboxRow({
  node,
  isSelected,
  name,
  onSelect,
  formatUnread,
  onEmptyTrash,
  isEmptyingTrash = false,
  iconKey,
}: MailboxRowProps): React.JSX.Element {
  const { t } = useTranslation();
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
        /* E12: with the rail collapsed the label is folded away visually, so
           the name has to survive somewhere a pointer can reach it. It is set
           unconditionally rather than only when collapsed — a title on a row
           whose label is already visible is harmless, and a conditional one is
           a second state to keep in step. */
        title={name}
      >
        <MailboxIcon role={mailbox.role} iconKey={iconKey} />
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
      {/*
        A sibling of the folder LINK, not a child of it: a button inside an
        anchor is invalid HTML and, worse, its click would also navigate.
      */}
      {onEmptyTrash !== undefined && (
        <button
          type="button"
          className={styles.emptyTrash}
          disabled={isEmptyingTrash || mailbox.totalEmails === 0}
          onClick={() => {
            onEmptyTrash(mailbox);
          }}
        >
          {isEmptyingTrash ? t("action.emptyTrashWorking") : t("action.emptyTrash")}
        </button>
      )}
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
