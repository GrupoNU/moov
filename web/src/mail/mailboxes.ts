/**
 * Mailbox ordering, nesting and lookup (P2 deliverable 2).
 *
 * Pure functions over a flat `Mailbox[]`, because the sidebar's hard parts —
 * "which folder is Inbox", "what order do 24 folders go in", "how deep is
 * this one" — are all data questions, and answering them in a component would
 * make them untestable and re-answer them on every render.
 */

import type { Mailbox, MailboxRole } from "./types";

/**
 * The order roles appear in the sidebar.
 *
 * This is the deliverable's specified order (Inbox, Drafts, Sent, Archive,
 * Junk, Trash) and it is NOT the server's `sortOrder`. The server orders
 * Archive(40) before Junk(70) and Trash(80) but puts Drafts(20) before
 * Sent(30) — close, but it also interleaves `flagged`(50) and `all`(60),
 * which are Gmail-style virtual folders we surface differently. Owning the
 * order here means the sidebar reads the same against any server.
 */
const ROLE_ORDER: readonly MailboxRole[] = [
  "inbox",
  "drafts",
  "sent",
  "archive",
  "junk",
  "trash",
  "all",
  "flagged",
];

/** Rank for a role; unroled folders sort after every roled one. */
function roleRank(role: MailboxRole | null): number {
  if (role === null) return ROLE_ORDER.length;
  const index = ROLE_ORDER.indexOf(role);
  return index === -1 ? ROLE_ORDER.length : index;
}

/** A mailbox positioned in the tree. */
export interface MailboxNode {
  readonly mailbox: Mailbox;
  /** 0 for a top-level folder. */
  readonly depth: number;
  /** True when this node has children (so the sidebar can render a twisty). */
  readonly hasChildren: boolean;
}

/**
 * Flattens the mailbox tree into display order: roled folders first in the
 * canonical order, then custom folders alphabetically, with each folder's
 * descendants immediately after it.
 *
 * A FLAT list rather than a nested one because the sidebar renders a flat
 * `<ul>` with `aria-level` — which is what a screen reader needs for a tree —
 * and because a nested render makes "the next folder down" (the `j` key)
 * require a traversal instead of an index.
 *
 * Cycles and orphans are handled rather than assumed away: a `parentId`
 * pointing at a folder that is not in the list (possible when a parent is
 * unsubscribed) would otherwise make its children invisible, so they are
 * promoted to the top level.
 */
export function buildMailboxTree(mailboxes: readonly Mailbox[]): readonly MailboxNode[] {
  const byId = new Map<string, Mailbox>();
  for (const mailbox of mailboxes) byId.set(mailbox.id, mailbox);

  // Children indexed by parent, with orphans re-rooted.
  const childrenOf = new Map<string | null, Mailbox[]>();
  for (const mailbox of mailboxes) {
    const parent =
      mailbox.parentId !== null && byId.has(mailbox.parentId) ? mailbox.parentId : null;
    const siblings = childrenOf.get(parent);
    if (siblings === undefined) childrenOf.set(parent, [mailbox]);
    else siblings.push(mailbox);
  }

  const compare = (a: Mailbox, b: Mailbox): number => {
    const rankDelta = roleRank(a.role) - roleRank(b.role);
    if (rankDelta !== 0) return rankDelta;
    // Locale-aware, case-insensitive, and numeric so "Folder 10" follows
    // "Folder 9" rather than preceding it.
    return a.name.localeCompare(b.name, undefined, {
      sensitivity: "base",
      numeric: true,
    });
  };

  const out: MailboxNode[] = [];
  const visited = new Set<string>();

  const walk = (parentId: string | null, depth: number): void => {
    const children = childrenOf.get(parentId);
    if (children === undefined) return;
    for (const mailbox of [...children].sort(compare)) {
      // A parentId cycle would otherwise recurse until the stack dies.
      if (visited.has(mailbox.id)) continue;
      visited.add(mailbox.id);
      const grandchildren = childrenOf.get(mailbox.id);
      out.push({
        mailbox,
        depth,
        hasChildren: grandchildren !== undefined && grandchildren.length > 0,
      });
      walk(mailbox.id, depth + 1);
    }
  };

  walk(null, 0);
  return out;
}

/** Finds the mailbox holding a role, if the account has one. */
export function findByRole(
  mailboxes: readonly Mailbox[],
  role: MailboxRole,
): Mailbox | undefined {
  return mailboxes.find((mailbox) => mailbox.role === role);
}

/**
 * Resolves what a URL segment names: a role alias ("inbox") or a mailbox id
 * ("mc").
 *
 * Roles are tried FIRST so that a custom folder literally named "inbox" cannot
 * shadow the real one — and the id path is the fallback rather than the other
 * way round, because ids are opaque and a role alias is what links contain.
 */
export function resolveMailbox(
  mailboxes: readonly Mailbox[],
  segment: string,
): Mailbox | undefined {
  const asRole = mailboxes.find((mailbox) => mailbox.role === segment);
  if (asRole !== undefined) return asRole;
  return mailboxes.find((mailbox) => mailbox.id === segment);
}

/**
 * The canonical URL segment for a mailbox: its role when it has one, its id
 * otherwise.
 *
 * This is what keeps `/mail/inbox` in the address bar instead of `/mail/mc` —
 * a link that survives being sent to a colleague on another account.
 */
export function mailboxSegment(mailbox: Mailbox): string {
  return mailbox.role ?? mailbox.id;
}

/**
 * Whether a mailbox shows unread counts.
 *
 * Drafts, Sent, Archive and Trash are counted but not BADGED: a bold "3" next
 * to Sent is noise, because mail you sent being "unread" is an artefact of how
 * it was appended, not information (the pilot's own Sent folder shows 1 unread
 * of 1 message for exactly that reason). Gmail badges Inbox, Drafts and Junk;
 * so do we.
 */
export function showsUnreadBadge(mailbox: Mailbox): boolean {
  switch (mailbox.role) {
    case "sent":
    case "archive":
    case "trash":
    case "all":
      return false;
    default:
      return true;
  }
}

/**
 * Whether the mailbox's own count is the total rather than the unread count.
 *
 * Drafts is the case: "3 drafts" is what a user wants to know, and none of
 * them are meaningfully "unread".
 */
export function countsTotalNotUnread(mailbox: Mailbox): boolean {
  return mailbox.role === "drafts";
}

/** The number the sidebar shows beside a mailbox, or undefined for none. */
export function badgeCount(mailbox: Mailbox): number | undefined {
  if (countsTotalNotUnread(mailbox)) {
    return mailbox.totalEmails > 0 ? mailbox.totalEmails : undefined;
  }
  if (!showsUnreadBadge(mailbox)) return undefined;
  return mailbox.unreadEmails > 0 ? mailbox.unreadEmails : undefined;
}
