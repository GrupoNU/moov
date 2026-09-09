/**
 * What the folder rail SHOWS, and in what order (P0-5, canon 07 §2).
 *
 * # The problem this module exists for
 *
 * A real account's IMAP folder list is not a rail. The owner's own account
 * dumps about twenty-five folders into the sidebar — Calendario, Diario,
 * Fuentes RSS, Problemas de sincronización with a badge of 26 on its
 * Conflictos child — none of which hold mail a person reads. Those folders are
 * created by Outlook and Exchange as a side effect of syncing something else,
 * and IMAP has no way to say so: to the protocol they are folders like any
 * other. Collapsed to icons, the rail became twenty identical rectangles.
 *
 * Gmail's rail is five entries and a "Más". So this module answers three
 * questions as data, testably, away from any component:
 *
 *   1. Which folders are almost certainly not mail (the POLICY below).
 *   2. Which of the rest are canonical — always visible, in Gmail's order.
 *   3. What goes behind "Más".
 *
 * # Hidden, never deleted — and never decided in the client alone
 *
 * Nothing here removes a folder from the account, and nothing here is final: a
 * hidden folder is listed in Configuración → Etiquetas → Carpetas, where the
 * user flips it back. That table writes `folderVisibility` in the server-side
 * preferences (schema v3), so the choice roams to every device instead of
 * living in one browser's localStorage. The policy below is only what happens
 * BEFORE the user has said anything — a default, in the strict sense.
 *
 * # Why match on the NAME, and why that is safe here
 *
 * These folders have no RFC 6154 role, no attribute, nothing structural to key
 * on. The name is all there is. Matching on it is a heuristic and could in
 * principle hide a real folder someone called "Notas" — which is exactly why
 * the visibility table exists, why the policy is a default rather than a rule,
 * and why nothing is ever deleted.
 *
 * Names are compared after Unicode normalization and accent-stripping, in both
 * locales, because the same Exchange folder arrives as "Sync Issues" or
 * "Problemas de sincronización" depending on the mailbox's language, and a
 * user's client may have written it either way.
 */

import type { Mailbox, MailboxRole } from "./types";
import type { FolderVisibility } from "./prefs";

// ---------------------------------------------------------------------------
// name normalization
// ---------------------------------------------------------------------------

/**
 * Folds a folder name to its comparison form: lower case, no accents, no
 * repeated whitespace.
 *
 * NFD then stripping the combining marks rather than a hand-written accent
 * table: it covers every language the server might hand us, and it is the same
 * approach the search side already takes with `unaccent`.
 */
export function normalizeFolderName(name: string): string {
  return name
    .normalize("NFD")
    .replace(/\p{Diacritic}/gu, "")
    .toLowerCase()
    .replace(/\s+/g, " ")
    .trim();
}

// ---------------------------------------------------------------------------
// the policy
// ---------------------------------------------------------------------------

/**
 * The folders hidden by default, in both locales.
 *
 * Every entry is a folder an Outlook/Exchange client creates to sync something
 * that is not mail, or a leftover of one. Each is listed by BOTH spellings
 * rather than by a pattern, because a pattern broad enough to catch
 * "Problemas de sincronización" would also catch a user's own folder about a
 * problem.
 */
const HIDDEN_BY_DEFAULT: readonly string[] = [
  // Non-mail Outlook stores.
  "calendar",
  "calendario",
  "contacts",
  "contactos",
  "journal",
  "diario",
  "notes",
  "notas",
  "tasks",
  "tareas",
  "rss feeds",
  "fuentes rss",
  "conversation history",
  "historial de conversaciones",
  // Exchange's sync bookkeeping. The children are named too: they arrive as
  // their own top-level folders on some servers, and as children on others.
  "sync issues",
  "problemas de sincronizacion",
  "conflicts",
  "conflictos",
  "local failures",
  "errores locales",
  "server failures",
  "errores del servidor",
  // Duplicate junk folders Outlook makes beside the role-bearing one.
  "junk e-mail",
  "junk email",
  "correo no deseado",
  // Outlook's "detected items", and the leftover an early Moov snooze made.
  "elementos detectados",
  "pospuesto",
].map(normalizeFolderName);

const HIDDEN_SET = new Set(HIDDEN_BY_DEFAULT);

/**
 * Whether the POLICY hides this folder when the user has expressed no choice.
 *
 * A folder carrying an RFC 6154 role is never hidden whatever it is called:
 * the role is a stronger statement than the name, and hiding the account's
 * actual Junk folder because Outlook also localised it "Correo no deseado"
 * would be the worst possible outcome of a name heuristic.
 *
 * A CHILD of a hidden folder is hidden too. "Problemas de sincronización" with
 * its three children is one thing to the user, and hiding the parent while
 * leaving "Conflictos (26)" behind in the rail would be worse than hiding
 * nothing.
 */
export function isHiddenByPolicy(
  mailbox: Mailbox,
  mailboxes: readonly Mailbox[],
): boolean {
  if (mailbox.role !== null) return false;
  if (HIDDEN_SET.has(normalizeFolderName(mailbox.name))) return true;

  // Walk up. Bounded by the number of mailboxes, so a parentId cycle cannot
  // spin here even though `buildMailboxTree` already re-roots the ones it sees.
  const byId = new Map(mailboxes.map((box) => [box.id, box]));
  let parentId = mailbox.parentId;
  for (let hops = 0; parentId !== null && hops < mailboxes.length; hops += 1) {
    const parent = byId.get(parentId);
    if (parent === undefined) return false;
    if (parent.role === null && HIDDEN_SET.has(normalizeFolderName(parent.name))) return true;
    parentId = parent.parentId;
  }
  return false;
}

/**
 * The visibility in force for one folder: the user's stored choice when there
 * is one, the policy's default otherwise.
 *
 * Keyed by NAME rather than by id, matching the server's `folderVisibility`
 * map. Ids are per-account and change when a folder is recreated; the name is
 * what the user recognises and what the settings table shows them.
 */
export function effectiveVisibility(
  mailbox: Mailbox,
  mailboxes: readonly Mailbox[],
  stored: Readonly<Record<string, FolderVisibility>>,
): FolderVisibility {
  const choice = stored[mailbox.name];
  if (choice !== undefined) return choice;
  return isHiddenByPolicy(mailbox, mailboxes) ? "hide" : "show";
}

/**
 * Whether a folder is drawn in the rail right now.
 *
 * `showIfUnread` is Gmail's third state and it is resolved HERE rather than in
 * the component, because it depends on data — the unread count — and a
 * component that decided it would re-decide on every render.
 */
export function isVisibleInRail(
  mailbox: Mailbox,
  mailboxes: readonly Mailbox[],
  stored: Readonly<Record<string, FolderVisibility>>,
): boolean {
  switch (effectiveVisibility(mailbox, mailboxes, stored)) {
    case "show":
      return true;
    case "hide":
      return false;
    case "showIfUnread":
      return mailbox.unreadEmails > 0;
  }
}

// ---------------------------------------------------------------------------
// the canonical rows, and what falls behind "Más"
// ---------------------------------------------------------------------------

/**
 * The roles that stay above "Más", in Gmail's order (canon 07 §2).
 *
 * Recibidos · [Destacados · Pospuestos — virtual, injected by the component]
 * · Enviados · Borradores. Everything else — Archivo, Spam, Papelera and every
 * custom folder — lives behind the collapse.
 *
 * Note the ORDER Enviados before Borradores: Gmail's, and the opposite of what
 * the rail shipped with (review A-03).
 */
export const PRIMARY_ROLES: readonly MailboxRole[] = ["inbox", "sent", "drafts"];

/** The roles "Más" holds first, before the custom folders. */
const MORE_ROLE_ORDER: readonly MailboxRole[] = ["archive", "junk", "trash", "all", "flagged"];

/**
 * The rail split in two: what is always drawn, and what "Más" reveals.
 *
 * Both halves keep the tree's own order — this partitions, it does not re-sort,
 * so a folder's children still follow it and the depth a row renders at is
 * still the tree's.
 */
export interface CuratedRail<T> {
  readonly primary: readonly T[];
  readonly more: readonly T[];
}

/**
 * Splits an ordered list of rail entries into the primary rows and the ones
 * behind "Más".
 *
 * Generic over the entry so the caller can pass `MailboxNode`s without this
 * module depending on the tree's shape; `mailboxOf` is how it reads the folder
 * out. That keeps this file about the POLICY and leaves the tree to
 * `mailboxes.ts`.
 *
 * A CHILD follows its parent into whichever half the parent went to, even when
 * the child's own role would have put it elsewhere: a subtree split across a
 * collapse would render children with no visible parent, at an indent that
 * means nothing.
 */
export function curateRail<T>(
  entries: readonly T[],
  mailboxOf: (entry: T) => Mailbox,
  mailboxes: readonly Mailbox[],
  stored: Readonly<Record<string, FolderVisibility>>,
  /**
   * The Snoozed folder's NAME, when the account has one.
   *
   * It has to be named because it is the one canonical row with no role to
   * key on: RFC 6154 defines none for snoozed mail, and the sync engine
   * refused to invent one (E4). Gmail lists Pospuestos third, above Enviados,
   * so demoting it behind "Más" for want of a role would move a row people
   * navigate to from memory.
   */
  snoozedName?: string,
): CuratedRail<T> {
  const primary: T[] = [];
  const more: T[] = [];
  /** Ids whose subtree has been sent behind "Más". */
  const demoted = new Set<string>();

  for (const entry of entries) {
    const mailbox = mailboxOf(entry);
    if (!isVisibleInRail(mailbox, mailboxes, stored)) continue;

    const parentDemoted = mailbox.parentId !== null && demoted.has(mailbox.parentId);
    const isCanonical =
      (mailbox.role !== null && PRIMARY_ROLES.includes(mailbox.role)) ||
      (snoozedName !== undefined && mailbox.name === snoozedName);
    const isPrimary = !parentDemoted && isCanonical;

    if (isPrimary) primary.push(entry);
    else {
      more.push(entry);
      demoted.add(mailbox.id);
    }
  }

  /*
   * The canonical rows are put in Gmail's order EXPLICITLY rather than left in
   * the tree's, because the Snoozed folder has no role: the tree sorts unroled
   * folders after every roled one, which would land Pospuestos below
   * Borradores when Gmail puts it above Enviados. A stable sort on a rank, so
   * a subtree under one of these keeps following its parent.
   */
  const canonicalRank = (mailbox: Mailbox): number => {
    if (snoozedName !== undefined && mailbox.name === snoozedName) return 1;
    if (mailbox.role === null) return PRIMARY_ROLES.length + 1;
    const index = PRIMARY_ROLES.indexOf(mailbox.role);
    // Inbox is 0; Sent and Drafts shift up by one to leave room for Pospuestos.
    return index <= 0 ? index : index + 1;
  };

  return {
    primary: [...primary].sort(
      (a, b) => canonicalRank(mailboxOf(a)) - canonicalRank(mailboxOf(b)),
    ),
    more,
  };
}

/**
 * Orders the "Más" half: the remaining ROLES first in a fixed order, then
 * everything else as the tree already had it.
 *
 * Gmail puts Archivo, Spam and Papelera at the top of its own "Más" and the
 * user's labels below, and that is the useful order: the three a user reaches
 * for are found without reading, and their own folders keep the alphabetical
 * order they know.
 *
 * Subtrees are kept together — a child moves with its parent — so this is a
 * sort of ROOTS, with each root's descendants re-attached after it.
 */
export function orderMore<T>(
  entries: readonly T[],
  mailboxOf: (entry: T) => Mailbox,
): readonly T[] {
  const present = new Set(entries.map((entry) => mailboxOf(entry).id));

  // Group each entry under the topmost ancestor that is also in this half.
  const roots: T[] = [];
  const subtree = new Map<string, T[]>();
  const rootOf = new Map<string, string>();

  for (const entry of entries) {
    const mailbox = mailboxOf(entry);
    const parentId = mailbox.parentId;
    const root =
      parentId !== null && present.has(parentId) ? rootOf.get(parentId) : undefined;
    if (root === undefined) {
      roots.push(entry);
      rootOf.set(mailbox.id, mailbox.id);
      subtree.set(mailbox.id, []);
    } else {
      rootOf.set(mailbox.id, root);
      subtree.get(root)?.push(entry);
    }
  }

  const rank = (mailbox: Mailbox): number => {
    if (mailbox.role === null) return MORE_ROLE_ORDER.length;
    const index = MORE_ROLE_ORDER.indexOf(mailbox.role);
    return index === -1 ? MORE_ROLE_ORDER.length : index;
  };

  // A STABLE sort by rank alone, so the roles lead and everything else keeps
  // the tree's alphabetical order rather than being re-sorted by a second key.
  const ordered = [...roots].sort((a, b) => rank(mailboxOf(a)) - rank(mailboxOf(b)));

  return ordered.flatMap((root) => [root, ...(subtree.get(mailboxOf(root).id) ?? [])]);
}

// ---------------------------------------------------------------------------
// the duplicate-name case
// ---------------------------------------------------------------------------

/**
 * Disambiguates a custom folder whose display name collides with a role row's
 * label (P0-5d).
 *
 * The account that prompted this has an `Archive` role folder AND a custom
 * folder the user called "Archivo": the rail drew two identical rows, and no
 * amount of looking told you which was which. The ROLE keeps the plain label —
 * it is the one the rest of the UI, the move menu and the keyboard all mean —
 * and the custom one is qualified.
 *
 * Qualified by its PARENT PATH when it has one, because that is information
 * the user recognises ("Trabajo/Archivo"), and by a generic suffix only when
 * it is top-level and there is nothing better to say.
 *
 * Returns the name unchanged when there is no collision, which is the case for
 * almost every folder — the caller can use it unconditionally.
 */
export function disambiguateName(
  mailbox: Mailbox,
  displayName: string,
  takenByRole: ReadonlySet<string>,
  mailboxes: readonly Mailbox[],
  folderSuffix: string,
): string {
  if (mailbox.role !== null) return displayName;
  if (!takenByRole.has(normalizeFolderName(displayName))) return displayName;

  const parent =
    mailbox.parentId === null
      ? undefined
      : mailboxes.find((box) => box.id === mailbox.parentId);
  if (parent !== undefined) return `${parent.name}/${displayName}`;
  return `${displayName} (${folderSuffix})`;
}
