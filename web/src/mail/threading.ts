/**
 * Grouping a message list into conversations (P2 deliverable 4, completed by
 * L3 epic E1).
 *
 * The server does the hard half: it computes real JWZ-simplified `threadId`s
 * over References/In-Reply-To (L2-sync-engine §2.3), and the pilot's own
 * account has 1,168 multi-message threads, the largest with 24 messages. So
 * this module does NOT re-derive threading — it groups an already-ordered list
 * by the id the server assigned.
 *
 * # Two paths, and why both still exist
 *
 * **Collapsed (E1, the normal path).** `Email/query` now honours
 * `collapseThreads` (RFC 8621 §4.4.3), collapsing IN THE DATABASE inside a
 * bounded window. The list then arrives with one message per conversation and
 * a `Thread/get` riding the same batch, so each row knows its conversation's
 * TRUE size. {@link groupByThread}'s `threadSizes` argument carries it.
 *
 * **Uncollapsed (the fallback).** When the server declines to collapse — or
 * when the conversation-view preference is off — the list arrives as
 * individual messages and is grouped here. That path has one honest
 * limitation, which is exactly why the collapsed one was built: a thread's
 * size then reflects the messages IN THIS RESULT WINDOW, not the thread's
 * total, because the rest may be in another folder or past the window.
 *
 * {@link ThreadGroup.sizeIsExact} distinguishes the two, so the UI can show a
 * count it can stand behind rather than one that quietly means something
 * different depending on which path served it.
 */

import type { Email, Thread } from "./types";
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
  /**
   * How many messages the conversation has. 1 means "not a conversation".
   *
   * Its MEANING depends on {@link sizeIsExact}: the thread's true total when
   * the server collapsed and reported it, otherwise only how many of its
   * messages are in this result window.
   */
  readonly size: number;
  /**
   * True when {@link size} is the conversation's real size, straight from
   * `Thread/get`.
   *
   * False on the client-grouped path, where the count is bounded by the fetch
   * window. The UI uses this to decide whether it may state a number as a
   * fact — a badge reading "3" that silently means "3 of maybe 24" is the kind
   * of small lie that makes a whole list untrustworthy.
   */
  readonly sizeIsExact: boolean;
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
 * @param threads the `Thread/get` results that rode the same batch, when the
 *   query collapsed. Their `emailIds.length` is the conversation's TRUE size;
 *   a thread absent from this list falls back to the window count, with
 *   `sizeIsExact` false to say so.
 */
export function groupByThread(
  emails: readonly Email[],
  threads: readonly Thread[] = [],
): readonly ThreadGroup[] {
  const sizeByThread = new Map<string, number>();
  for (const thread of threads) sizeByThread.set(thread.id, thread.emailIds.length);

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
    /*
     * The server's count wins when there is one, and it is never SMALLER than
     * what we hold: the window can only ever contain a subset of a thread. A
     * server count below the window count would mean the two disagree about
     * membership, so the larger is taken and the claim of exactness dropped —
     * a number that is at least true beats a smaller one that is not.
     */
    const reported = sizeByThread.get(key);
    const windowed = messages.length;
    const exact = reported !== undefined && reported >= windowed;

    return {
      id: key,
      latest,
      messages,
      size: exact ? (reported ?? windowed) : windowed,
      sizeIsExact: exact,
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
