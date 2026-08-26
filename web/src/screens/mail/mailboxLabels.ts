/**
 * Localised folder names, shared by the sidebar and the move menu (P3).
 *
 * Extracted from `MailboxList` when P3's move menu needed the same mapping.
 * Two copies of this switch would drift the first time a role is added, and
 * a sidebar that says "Papelera" beside a move menu that says "Trash" is the
 * kind of inconsistency that reads as a different product in each column.
 *
 * The rule it encodes: a ROLED folder is shown under its localised name —
 * "Bandeja de entrada" rather than Dovecot's "INBOX" — because the role is a
 * stable, server-independent identity. A custom folder renders verbatim,
 * because translating it would be renaming the user's own folder.
 */

import type { Mailbox, MailboxRole } from "../../mail/types";

/** The i18n keys for the roled folder names. */
const ROLE_LABEL_KEYS = {
  inbox: "mailbox.inbox",
  drafts: "mailbox.drafts",
  sent: "mailbox.sent",
  archive: "mailbox.archive",
  junk: "mailbox.junk",
  trash: "mailbox.trash",
  all: "mailbox.all",
  flagged: "mailbox.flagged",
} as const satisfies Record<MailboxRole, string>;

/** One of the role label keys. */
export type MailboxLabelKey = (typeof ROLE_LABEL_KEYS)[MailboxRole];

/**
 * The i18n key for a role, or undefined for a custom folder.
 *
 * Returning the KEY rather than the translated string keeps this module free
 * of React, so it can be unit-tested and called from a non-component context.
 */
export function mailboxLabelKey(role: MailboxRole | null): MailboxLabelKey | undefined {
  if (role === null) return undefined;
  return ROLE_LABEL_KEYS[role];
}

/** Resolves a mailbox's display name given a translator. */
export function mailboxLabel(
  mailbox: Mailbox,
  t: (key: MailboxLabelKey) => string,
): string {
  const key = mailboxLabelKey(mailbox.role);
  return key === undefined ? mailbox.name : t(key);
}
