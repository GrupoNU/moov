/**
 * The JMAP Mail wire types, hand-written against the server we own.
 *
 * Every shape here was verified against the LIVE pilot rather than inferred
 * from the RFC, because the RFC permits shapes our server does not produce and
 * the difference is exactly where a client breaks. The ones that matter:
 *
 *   - address lists are `null` when the header was absent, never `[]`;
 *   - `subject` is `null` for an empty subject, never `""`;
 *   - `bodyValues` is keyed by `partId`, which is a DECIMAL INDEX AS A STRING
 *     ("0", "2"), not a content id;
 *   - every body part's `blobId` is `null` in phase 1 — the only downloadable
 *     blob is the whole message (see attachments in the reading pane).
 */

/** RFC 8621 §4.1.4 — the roles our server emits, lowercase, exactly. */
export type MailboxRole =
  | "inbox"
  | "archive"
  | "drafts"
  | "sent"
  | "junk"
  | "trash"
  | "all"
  | "flagged";

/** RFC 8621 §4.1.2 — what the account may do to a mailbox. */
export interface MailboxRights {
  readonly mayReadItems: boolean;
  readonly mayAddItems: boolean;
  readonly mayRemoveItems: boolean;
  readonly maySetSeen: boolean;
  readonly maySetKeywords: boolean;
  readonly mayCreateChild: boolean;
  readonly mayRename: boolean;
  readonly mayDelete: boolean;
  readonly maySubmit: boolean;
}

/** RFC 8621 §4.1 — a Mailbox. */
export interface Mailbox {
  readonly id: string;
  readonly name: string;
  readonly parentId: string | null;
  readonly role: MailboxRole | null;
  readonly sortOrder: number;
  readonly totalEmails: number;
  readonly unreadEmails: number;
  readonly totalThreads: number;
  readonly unreadThreads: number;
  readonly myRights: MailboxRights;
  readonly isSubscribed: boolean;
}

/** RFC 8621 §4.1.2.3 — one address in a header. `name` is null when absent. */
export interface EmailAddress {
  readonly name: string | null;
  readonly email: string;
}

/**
 * RFC 8621 §4.1.4 — one part of a message body.
 *
 * `blobId` is typed as `string | null` and is ALWAYS null on this server:
 * phase 1 stores one blob per message, not per part. The type keeps the null
 * so a future per-part blob does not become a breaking change, and every read
 * site has to acknowledge it.
 */
export interface EmailBodyPart {
  readonly partId: string | null;
  readonly blobId: string | null;
  readonly size: number;
  readonly name: string | null;
  readonly type: string;
  readonly charset: string | null;
  readonly disposition: string | null;
  readonly cid: string | null;
  readonly language: readonly string[] | null;
  readonly location: string | null;
  readonly subParts?: readonly EmailBodyPart[] | null;
}

/** RFC 8621 §4.1.4 — the decoded text of one body part. */
export interface EmailBodyValue {
  readonly value: string;
  readonly isEncodingProblem: boolean;
  readonly isTruncated: boolean;
}

/** RFC 8621 §4.1.1 — one header, as it appeared. */
export interface EmailHeader {
  readonly name: string;
  readonly value: string;
}

/**
 * RFC 8621 §4 — an Email.
 *
 * Nearly everything is optional because `Email/get` returns exactly the
 * properties asked for: a list row requests eight of these, a reading pane
 * requests fifteen more. Modelling that as one type with optional members —
 * rather than two types — keeps a single cache keyed by id, into which a row
 * fetch and a body fetch both merge.
 */
export interface Email {
  readonly id: string;
  readonly blobId?: string;
  readonly threadId?: string;
  /** An object-as-set: `{ "mc": true }`. Exactly one key on this server. */
  readonly mailboxIds?: Readonly<Record<string, boolean>>;
  /** An object-as-set: `{ "$seen": true }`. */
  readonly keywords?: Readonly<Record<string, boolean>>;
  readonly size?: number;
  /** UTC, no fractional seconds: "2026-08-20T14:03:11Z". */
  readonly receivedAt?: string;
  /** RFC 3339 with the sender's offset preserved, or null. */
  readonly sentAt?: string | null;
  readonly messageId?: readonly string[] | null;
  readonly inReplyTo?: readonly string[] | null;
  readonly references?: readonly string[] | null;
  readonly sender?: readonly EmailAddress[] | null;
  readonly from?: readonly EmailAddress[] | null;
  readonly to?: readonly EmailAddress[] | null;
  readonly cc?: readonly EmailAddress[] | null;
  readonly bcc?: readonly EmailAddress[] | null;
  readonly replyTo?: readonly EmailAddress[] | null;
  readonly subject?: string | null;
  readonly preview?: string;
  readonly hasAttachment?: boolean;
  readonly headers?: readonly EmailHeader[];
  readonly bodyStructure?: EmailBodyPart;
  readonly bodyValues?: Readonly<Record<string, EmailBodyValue>>;
  readonly textBody?: readonly EmailBodyPart[];
  readonly htmlBody?: readonly EmailBodyPart[];
  readonly attachments?: readonly EmailBodyPart[];
  /**
   * E10 (canon §4.1.15): Moov's vendor property — true when the delivery
   * scanner (Rspamd on Mailcow) declared the message spam, wherever it ended
   * up filed. Computed SERVER-side in one documented place
   * (`internal/jmap/mail/suspicious.go`); this client never parses verdict
   * headers itself. Only present when requested by name (DETAIL_PROPERTIES).
   */
  readonly "moov:suspicious"?: boolean;
}

/**
 * True when the scanner flagged this message as spam (E10, canon §4.1.15).
 *
 * The consequences mirror Junk exactly — warning banner, remote images
 * unloadable — WITHOUT hiding the mail or its other affordances. Absent
 * property (a list row, an old cache entry) reads as not-suspicious: the
 * degraded treatment needs a positive verdict, never the benefit of a doubt
 * in the hostile direction.
 */
export function isSuspicious(email: Email): boolean {
  return email["moov:suspicious"] === true;
}

/** RFC 8621 §3 — a Thread. `emailIds` is oldest-first. */
export interface Thread {
  readonly id: string;
  readonly emailIds: readonly string[];
}

/** The JMAP keywords this client knows by name (RFC 8621 §4.1.1). */
export const KEYWORD_SEEN = "$seen";
export const KEYWORD_FLAGGED = "$flagged";
export const KEYWORD_ANSWERED = "$answered";
export const KEYWORD_DRAFT = "$draft";

/** True when the message has been read. */
export function isSeen(email: Email): boolean {
  return email.keywords?.[KEYWORD_SEEN] === true;
}

/** True when the message is starred/flagged. */
export function isFlagged(email: Email): boolean {
  return email.keywords?.[KEYWORD_FLAGGED] === true;
}

/** True when the message has been replied to. */
export function isAnswered(email: Email): boolean {
  return email.keywords?.[KEYWORD_ANSWERED] === true;
}

/** The properties a list row needs — and no more.
 *
 * `headers` and `bodyValues` are deliberately absent: requesting either makes
 * the server open and re-parse the raw blob for every row, which is the
 * difference between a list that paints instantly and one that does not.
 */
export const LIST_PROPERTIES: readonly string[] = [
  "id",
  "threadId",
  "mailboxIds",
  "keywords",
  "from",
  "to",
  "subject",
  "receivedAt",
  "preview",
  "hasAttachment",
  "size",
];

/** The properties the reading pane adds on top of a row. */
export const DETAIL_PROPERTIES: readonly string[] = [
  ...LIST_PROPERTIES,
  "blobId",
  "sentAt",
  "cc",
  "bcc",
  "replyTo",
  "sender",
  "messageId",
  "inReplyTo",
  "references",
  "textBody",
  "htmlBody",
  "attachments",
  "bodyValues",
  /*
   * E2: `headers` is requested for the OPEN message only — never for a list
   * row, where it would make the server re-parse every message's raw blob to
   * paint a list (see LIST_PROPERTIES above). The reading pane needs it for
   * `List-Unsubscribe` and `List-ID`, which exist nowhere else in the JMAP
   * object model.
   */
  "headers",
  /*
   * E10: the scanner's spam verdict, served from the same re-parse `headers`
   * rides on — so it costs nothing extra HERE and would cost a blob read per
   * row in LIST_PROPERTIES. It drives the suspicious-mail banner and the
   * remote-image suppression in the reader (canon §4.1.15).
   */
  "moov:suspicious",
];
