/**
 * The conversation reader's state machine (L3 epic E1, canon §2.1).
 *
 * Gmail's defining behavior: opening a message opens the WHOLE conversation.
 * The older messages render collapsed, the last one (and anything unread)
 * renders open, `;` and `:` expand and collapse everything, and `p`/`n` move
 * between messages INSIDE the conversation while `j`/`k` keep moving between
 * conversations.
 *
 * All of that is decisions about a list and a set, which is why it lives here
 * as pure functions rather than inside the component: the rules are exactly
 * the kind that look obvious and are wrong in a corner — "which message is
 * open when the thread has one unread in the middle", "what does `n` do on the
 * last message", "does opening a conversation mark all of it read" — and every
 * one of those is a test below rather than a bug in a thread.
 *
 * # The three rules that matter
 *
 * 1. **Newest at the BOTTOM.** Canon §2.1 quotes it verbatim: replies are
 *    grouped "with the latest email at the bottom of a conversation thread".
 *    That is the OPPOSITE of the message list's newest-first order, and the
 *    inversion happens exactly once — in {@link conversationOrder} — so no
 *    other code has to remember which way round it is.
 *
 * 2. **Bodies are fetched lazily, per expanded message.** The pilot has a real
 *    24-message thread; requesting `bodyValues` for all of it to open one
 *    message would fetch two dozen bodies to render one. {@link bodiesToFetch}
 *    names only what is expanded and not already held.
 *
 * 3. **Only EXPANDED messages are marked read** (canon §2.1's Gmail behavior,
 *    and the honest one): opening a conversation with twelve unread replies
 *    must not silently mark twelve messages read when the user saw one.
 *    {@link messagesToMarkRead} is that rule, and nothing else may mark.
 */

import { isSeen, type Email } from "./types";

/** Which messages of the open conversation are expanded. */
export interface ConversationState {
  /** The expanded message ids. Everything else in the thread is collapsed. */
  readonly expanded: ReadonlySet<string>;
  /**
   * Messages whose read-marking has already been issued this session.
   *
   * Kept so that collapsing and re-expanding a message does not re-issue the
   * write, and so the effect that performs the marking is idempotent — it runs
   * on every render pass, and without this it would loop against its own
   * optimistic update.
   */
  readonly marked: ReadonlySet<string>;
}

export const EMPTY_CONVERSATION: ConversationState = {
  expanded: new Set(),
  marked: new Set(),
};

/**
 * Orders a thread's messages the way the reader shows them: OLDEST FIRST, so
 * the newest sits at the bottom (canon §2.1).
 *
 * Sorted by `receivedAt`, with the message id as the tiebreaker — two messages
 * can share a timestamp to the second (a mailing list fan-out delivers them in
 * the same second), and an unstable sort would let them swap places between
 * renders, which looks like the thread reshuffling itself.
 *
 * Messages missing `receivedAt` sort FIRST rather than being dropped: the date
 * is unknown, not the message, and a thread that silently loses a member is
 * far worse than one with an oddly placed row.
 */
export function conversationOrder(messages: readonly Email[]): readonly Email[] {
  return [...messages].sort((a, b) => {
    const at = a.receivedAt ?? "";
    const bt = b.receivedAt ?? "";
    if (at !== bt) return at < bt ? -1 : 1;
    return a.id < b.id ? -1 : 1;
  });
}

/**
 * The messages that start expanded when a conversation opens.
 *
 * Gmail's rule, from canon §2.1: everything collapsed EXCEPT the last one and
 * anything unread. The reasoning is the reader's intent — the newest message
 * is what they came for, and an unread message anywhere in the thread is
 * something they have not seen yet, so hiding it behind a click would bury
 * exactly the thing that made the thread interesting.
 *
 * `openId` is the message the user actually clicked, when the route named one.
 * It is always expanded even if it is old and read: arriving from a search
 * result or a permalink and finding that message collapsed would be an outright
 * bug — the app would have navigated to something it then hid.
 */
export function initialExpanded(
  messages: readonly Email[],
  openId?: string,
): ReadonlySet<string> {
  const ordered = conversationOrder(messages);
  const expanded = new Set<string>();

  const last = ordered[ordered.length - 1];
  if (last !== undefined) expanded.add(last.id);

  for (const message of ordered) {
    if (!isSeen(message)) expanded.add(message.id);
  }

  if (openId !== undefined && ordered.some((message) => message.id === openId)) {
    expanded.add(openId);
  }

  return expanded;
}

/** Expands or collapses one message. */
export function toggleExpanded(
  state: ConversationState,
  id: string,
): ConversationState {
  const expanded = new Set(state.expanded);
  if (expanded.has(id)) expanded.delete(id);
  else expanded.add(id);
  return { ...state, expanded };
}

/** `;` — expand every message in the conversation (canon §2.1). */
export function expandAll(
  state: ConversationState,
  messages: readonly Email[],
): ConversationState {
  return { ...state, expanded: new Set(messages.map((message) => message.id)) };
}

/**
 * `:` — collapse every message EXCEPT the newest (canon §2.1).
 *
 * Not "collapse literally everything": a reading pane showing a stack of
 * closed rows and no message at all is a dead end, and Gmail keeps the last
 * one open. Collapsing to nothing would also make `:` a way to lose your
 * place with no way back except a click.
 */
export function collapseAll(
  state: ConversationState,
  messages: readonly Email[],
): ConversationState {
  const ordered = conversationOrder(messages);
  const last = ordered[ordered.length - 1];
  return {
    ...state,
    expanded: new Set(last === undefined ? [] : [last.id]),
  };
}

/** True when every message of the conversation is expanded. */
export function isAllExpanded(
  state: ConversationState,
  messages: readonly Email[],
): boolean {
  return messages.length > 0 && messages.every((message) => state.expanded.has(message.id));
}

/**
 * The ids whose FULL BODY still has to be fetched.
 *
 * Rule 2 of this module, expressed: a message needs a body only when it is
 * expanded, and only when we do not already hold one. `bodyValues` present is
 * the marker — `Email/get` for a list row deliberately omits it
 * (LIST_PROPERTIES), so its presence means a detail fetch already landed.
 *
 * The result is capped by the caller's batch size rather than here: this
 * function answers "what is missing", and how many to ask for at once is a
 * transport decision.
 */
export function bodiesToFetch(
  state: ConversationState,
  messages: readonly Email[],
  held: ReadonlySet<string>,
): readonly string[] {
  const wanted: string[] = [];
  for (const message of conversationOrder(messages)) {
    if (!state.expanded.has(message.id)) continue;
    if (held.has(message.id)) continue;
    if (message.bodyValues !== undefined) continue;
    wanted.push(message.id);
  }
  return wanted;
}

/**
 * The ids that opening/expanding should mark READ — rule 3.
 *
 * Only expanded, only unread, only not already marked. A message the user
 * scrolled past while collapsed stays unread, which is the whole point: Gmail
 * does not mark a twelve-message thread read because you opened its newest
 * reply, and neither does this.
 */
export function messagesToMarkRead(
  state: ConversationState,
  messages: readonly Email[],
): readonly string[] {
  const ids: string[] = [];
  for (const message of messages) {
    if (!state.expanded.has(message.id)) continue;
    if (state.marked.has(message.id)) continue;
    if (isSeen(message)) continue;
    ids.push(message.id);
  }
  return ids;
}

/** Records that these ids have had their read-marking issued. */
export function withMarked(
  state: ConversationState,
  ids: readonly string[],
): ConversationState {
  if (ids.length === 0) return state;
  const marked = new Set(state.marked);
  for (const id of ids) marked.add(id);
  return { ...state, marked };
}

/**
 * `p` / `n` — the message BEFORE or AFTER the current one, inside the
 * conversation (canon §2.1).
 *
 * Distinct from `j`/`k`, which move between conversations. The distinction is
 * Gmail's and it is the reason both pairs exist: in a 24-message thread the
 * user needs to walk the thread without leaving it.
 *
 * Returns undefined at the ends rather than wrapping. Wrapping would take a
 * user who pressed `n` once too often from the newest message back to the
 * oldest, which reads as the thread having jumped somewhere else entirely.
 *
 * With no current message, `n` starts at the newest and `p` at the oldest —
 * the ends the two keys travel from.
 */
export function adjacentMessage(
  messages: readonly Email[],
  currentId: string | undefined,
  direction: "next" | "previous",
): Email | undefined {
  const ordered = conversationOrder(messages);
  if (ordered.length === 0) return undefined;

  if (currentId === undefined) {
    return direction === "next" ? ordered[ordered.length - 1] : ordered[0];
  }

  const index = ordered.findIndex((message) => message.id === currentId);
  if (index < 0) return undefined;

  // "next" walks DOWN the rendered order (older → newer), because the reader
  // shows oldest first; that is what "the next message" means on screen.
  const target = index + (direction === "next" ? 1 : -1);
  return ordered[target];
}

/**
 * A conversation's collapsed-row summary for one message.
 *
 * The collapsed row shows sender, snippet and date (canon §2.1) — no subject,
 * because every message in a conversation shares one and repeating it 24 times
 * is how a thread becomes unreadable.
 */
export interface CollapsedRow {
  readonly id: string;
  readonly isUnread: boolean;
  readonly isExpanded: boolean;
}

/** The render plan for a conversation: every message, in order, with its state. */
export function conversationRows(
  state: ConversationState,
  messages: readonly Email[],
): readonly CollapsedRow[] {
  return conversationOrder(messages).map((message) => ({
    id: message.id,
    isUnread: !isSeen(message),
    isExpanded: state.expanded.has(message.id),
  }));
}
