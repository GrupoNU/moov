/**
 * The undo stack (E2 item 2 — Gmail's `z`).
 *
 * # Why an "undo" needs TWO halves, and why that is the whole design
 *
 * `actions.ts` already computes an inverse PATCH per message, and it is easy to
 * mistake that for undo. It is not. An inverse patch repaints the client; it
 * says nothing to the server. Rolling one back after a SUCCESSFUL archive would
 * show the message back in the inbox while Dovecot still has it in Archive —
 * a lie that survives exactly until the next refresh, which is the worst
 * possible failure mode because the user believes the undo worked.
 *
 * So an undoable entry carries both:
 *
 *   - the inverse patches, for the instant repaint (ADR §6's <100 ms), and
 *   - the inverse ACTION, which is a real {@link MessageAction} re-issued
 *     against the server.
 *
 * The inverse action is what makes `z` true rather than cosmetic.
 *
 * # Single entry, like Gmail
 *
 * Gmail's `z` is "undo last action", not a history. A stack of one is not a
 * simplification here — it is the correct model: a second undo in Gmail redoes
 * nothing, and the toast that offers the undo is itself the affordance's
 * lifetime. Keeping a deeper history would mean offering to undo an action
 * whose toast is long gone and whose messages may have moved twice since.
 *
 * # Expiry is data, not a timer
 *
 * The entry carries `expiresAt` rather than being deleted by a `setTimeout`.
 * A timer is invisible to a test and races with re-renders; a timestamp lets
 * {@link isUndoable} be a pure function of (entry, now), which a test can
 * enumerate — including the boundary, which is where off-by-one lives.
 */

import type { MessageAction, MessagePatch } from "./actions";

/** How long the undo affordance stays live. Gmail's toast is about this long. */
export const UNDO_WINDOW_MS = 8_000;

/** The action kinds `z` can take back. */
const UNDOABLE_KINDS: ReadonlySet<string> = new Set([
  "archive",
  "delete",
  "move",
  "spam",
  "notSpam",
]);

/**
 * True when an action is worth offering an undo for.
 *
 * Flags and read/unread are deliberately OUT, and that is Gmail's line too:
 * they are one keystroke to reverse directly, visible in place, and offering a
 * toast for each would mean a toast on nearly every keypress. The undoable set
 * is the set of actions that make a message DISAPPEAR from where the user was
 * looking — which is exactly when "wait, no" happens.
 */
export function isUndoableAction(action: MessageAction): boolean {
  return UNDOABLE_KINDS.has(action.kind) && action.ids.length > 0;
}

/** One undoable step: how to repaint it and how to un-do it on the server. */
export interface UndoEntry {
  /** Monotonic, so a stale toast cannot undo a newer action. */
  readonly id: number;
  /** The action that was performed — for the toast's wording. */
  readonly action: MessageAction;
  /**
   * The server call that reverses it. `undefined` when the action cannot be
   * reversed (a permanent delete), in which case no undo is offered at all.
   */
  readonly inverseAction: MessageAction | undefined;
  /** The client-side repaint, keyed by message id. */
  readonly inverses: ReadonlyMap<string, MessagePatch>;
  /** Epoch millis after which the offer is gone. */
  readonly expiresAt: number;
  /**
   * E4: the message ids to UN-SNOOZE, when the action being undone was a
   * snooze.
   *
   * A snooze cannot be undone by {@link inverseAction}, and the reason is not
   * an omission — it is that the inverse is not a `MessageAction` at all. A
   * `move` back into the inbox would put the message where it was WITHOUT
   * clearing the wake time the server recorded, so the mail would return and
   * then vanish again at the appointed hour. The only honest reverse is
   * `Snooze/set destroy`, which is a different method under a different
   * capability, so it travels as its own field.
   *
   * The asymmetry that makes this safe: un-snoozing BEFORE the wake is a plain
   * move back and the message keeps its id, while AFTER the wake
   * `internal/sync/snooze.go` re-APPENDs it with a fresh INTERNALDATE (so it
   * "returns to the top of your inbox") and the id changes. The undo window is
   * eight seconds and the nearest preset is hours away, so only the first case
   * can ever be offered.
   */
  readonly unsnoozeIds?: readonly string[];
}

/**
 * Builds the inverse ACTION for an action that has just succeeded.
 *
 * `originMailboxId` is where the messages came FROM — the mailbox the user was
 * looking at. Undoing an archive means moving them back there, which is why
 * this cannot be derived from the action alone: `{kind:"archive", mailboxId:
 * archive}` knows its destination but not its origin.
 *
 * Returns `undefined` when there is no honest reverse:
 *
 *   - a `delete` with no origin to return to, or one that was permanent
 *     (the message is gone from the server; pretending otherwise would be the
 *     exact lie this module exists to prevent);
 *   - any action whose origin is unknown.
 */
export function inverseActionFor(
  action: MessageAction,
  originMailboxId: string | undefined,
  options: { readonly wasPermanent?: boolean } = {},
): MessageAction | undefined {
  if (!isUndoableAction(action)) return undefined;
  if (options.wasPermanent === true) return undefined;
  if (originMailboxId === undefined) return undefined;
  // Moving a message back into the folder it was already in is a no-op that
  // would still cost a round trip and still show a toast: refuse it.
  if (action.mailboxId !== undefined && action.mailboxId === originMailboxId) {
    return undefined;
  }
  return { kind: "move", ids: action.ids, mailboxId: originMailboxId };
}

/** Assembles an entry, or `undefined` when the action cannot be undone. */
export function makeUndoEntry(input: {
  readonly id: number;
  readonly action: MessageAction;
  readonly inverses: ReadonlyMap<string, MessagePatch>;
  readonly originMailboxId: string | undefined;
  readonly now: number;
  readonly wasPermanent?: boolean;
}): UndoEntry | undefined {
  const inverseAction = inverseActionFor(input.action, input.originMailboxId, {
    ...(input.wasPermanent !== undefined ? { wasPermanent: input.wasPermanent } : {}),
  });
  if (inverseAction === undefined) return undefined;
  return {
    id: input.id,
    action: input.action,
    inverseAction,
    inverses: input.inverses,
    expiresAt: input.now + UNDO_WINDOW_MS,
  };
}

/** True while the entry's offer is still live. */
export function isUndoable(entry: UndoEntry | undefined, now: number): boolean {
  if (entry === undefined) return false;
  /*
   * An entry needs SOMETHING to re-issue. Ordinarily that is the inverse
   * action; E4's snooze entry has no inverse action by construction (see
   * `unsnoozeIds`) and carries its own reverse instead, so either one keeps
   * the offer live and neither one alone is required.
   */
  if (entry.inverseAction === undefined && (entry.unsnoozeIds ?? []).length === 0) {
    return false;
  }
  // Strictly less-than: at exactly `expiresAt` the window has closed. Being
  // explicit here is the point — "<=" would keep an expired offer clickable
  // for one tick, which is precisely the kind of boundary a test must pin.
  return now < entry.expiresAt;
}

/** The i18n key describing what an undoable action did, for the toast. */
export function undoDescriptionKey(action: MessageAction): string {
  switch (action.kind) {
    case "archive":
      return "action.doneArchived";
    case "delete":
      return "action.doneDeleted";
    case "spam":
      return "action.doneSpam";
    case "notSpam":
      return "action.doneNotSpam";
    default:
      return "action.doneMoved";
  }
}
