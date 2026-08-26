/**
 * Grouping a message list into conversations (P2 deliverable 4).
 *
 * The server does the hard half: it computes real JWZ-simplified `threadId`s
 * over References/In-Reply-To (L2-sync-engine §2.3), and the pilot's own
 * account has 1,168 multi-message threads, the largest with 24 messages. So
 * this module does NOT re-derive threading — it groups an already-ordered list
 * by the id the server assigned.
 *
 * # Why grouping happens on the client
 *
 * `Email/query` has no `collapseThreads` on this server: it answers
 * `unsupportedFilter` (verified against the live pilot). The list therefore
 * arrives as individual messages and is collapsed here. That is a real
 * limitation with one real consequence, stated honestly in the UI rather than
 * hidden: a thread's size reflects the messages IN THIS RESULT WINDOW, not the
 * thread's total size, because the other messages may be in another folder or
 * beyond the window. `Thread/get` gives the true total, and the reading pane
 * uses it — the list does not, because doing so would cost one request per
 * visible row.
 */

import type { Email } from "./types";
import { isFlagged, isSeen } from "./types";

/** One row in the list: a single message, or a collapsed conversation. */
export interface ThreadGroup {
  /** The thread id, or the message id when the message has no thread. */
  readonly id: string;
  /**
   * The message the row represents: the NEWEST in the group, because that is
   * the one whose date the row shows and whose subject is current.
   */
  readonly latest: Email;
  /** Every message of this thread present in the window, newest first. */
  readonly messages: readonly Email[];
  /** How many messages are in the window. 1 means "not a conversation". */
  readonly size: number;
  /** True when ANY message in the group is unread — the row renders unread. */
  readonly hasUnread: boolean;
  /** True when ANY message is flagged. */
  readonly hasFlagged: boolean;
  /** True when ANY message carries an attachment. */
  readonly hasAttachment: boolean;
  /** Every distinct sender in the group, in first-seen order (newest first). */
  readonly participants: readonly string[];
}

/**
 * Groups messages into conversations, preserving the order the server returned.
 *
 * The ORDER RULE: a group takes the position of its newest message. The server
 * already sorted the list (receivedAt descending, or the hasKeyword pair), so
 * walking it once and appending each thread at its first sighting preserves
 * that sort exactly — no re-sorting, and therefore no way for the client's
 * idea of order to drift from the server's.
 *
 * @param emails messages in server order (newest first for the default sort)
 */
export function groupByThread(emails: readonly Email[]): readonly ThreadGroup[] {
  /*
   * `order` holds the buckets themselves rather than their keys.
   *
   * The obvious shape — an array of keys plus a Map to look them up in — needs
   * a non-null assertion on every lookup to convince the compiler that a key
   * taken from `order` is present in the map. Holding the bucket directly
   * makes that invariant structural instead of asserted: there is no lookup
   * that can fail, so there is nothing to assert.
   */
  interface Bucket {
    readonly key: string;
    readonly messages: Email[];
    readonly latest: Email;
  }

  const order: Bucket[] = [];
  const byKey = new Map<string, Bucket>();

  for (const email of emails) {
    // A message with no threadId is its own conversation of one. Falling back
    // to the message id (rather than dropping it, or bucketing every such
    // message together under "") keeps the row rendering.
    const key = email.threadId ?? email.id;
    const existing = byKey.get(key);
    if (existing === undefined) {
      // The first message seen for a thread is, by the order rule, its latest.
      const bucket: Bucket = { key, messages: [email], latest: email };
      byKey.set(key, bucket);
      order.push(bucket);
    } else {
      existing.messages.push(email);
    }
  }

  return order.map(({ key, messages, latest }) => {
    const participants: string[] = [];
    for (const message of messages) {
      const sender = senderLabel(message);
      if (sender !== undefined && !participants.includes(sender)) {
        participants.push(sender);
      }
    }
    return {
      id: key,
      latest,
      messages,
      size: messages.length,
      hasUnread: messages.some((m) => !isSeen(m)),
      hasFlagged: messages.some(isFlagged),
      hasAttachment: messages.some((m) => m.hasAttachment === true),
      participants,
    };
  });
}

/**
 * The name to show for a message's sender: the display name, or the address
 * when there is no name.
 *
 * The local part alone is NOT used as a fallback. "diego" is ambiguous across
 * domains, and a list where two different people both read as "info" is a list
 * that misleads — the full address is longer but true, and the row truncates
 * it with an ellipsis rather than lying.
 */
export function senderLabel(email: Email): string | undefined {
  const first = email.from?.[0];
  if (first === undefined) return undefined;
  const name = first.name?.trim();
  return name !== undefined && name !== "" ? name : first.email;
}

/**
 * The subject to show for a group, with reply/forward prefixes stripped.
 *
 * A conversation is one subject; repeating "Re: Re: Fwd:" on the row wastes
 * the width that the actual subject needs. The prefixes are matched in several
 * languages because the pilot is Spanish and its mail is not — Spanish clients
 * emit "RE:" and "RV:", French "TR:", German "AW:"/"WG:".
 *
 * The match is anchored and repeated, so "Re: Fwd: Re: x" collapses to "x",
 * and it is deliberately conservative: a subject that merely CONTAINS "re:"
 * mid-sentence is untouched.
 */
const REPLY_PREFIX =
  /^\s*(?:(?:re|rv|fwd?|tr|aw|wg|antw|sv|vs|enc)\s*(?:\[\d+\])?\s*:\s*)+/i;

export function displaySubject(subject: string | null | undefined): string | undefined {
  if (subject === undefined || subject === null) return undefined;
  const stripped = subject.replace(REPLY_PREFIX, "").trim();
  // A subject that was ONLY a prefix ("Re:") keeps its original text rather
  // than becoming empty, which would render a blank row.
  return stripped === "" ? subject.trim() : stripped;
}
