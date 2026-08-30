/**
 * Optimistic message actions and their rollback (P3 deliverable 1).
 *
 * # The design, and why it is a pure reducer
 *
 * ADR §6 asks for actions under 100 ms *perceived*. The server is fast — W1
 * measured flags at 19-44 ms and archive at 166 ms on the LAN — but the pilot
 * is served across the Atlantic, where the round trip alone is ~530 ms
 * (P2's `Core/echo` baseline). No server can win that; only the client can, by
 * painting the result before the round trip and reconciling afterwards.
 *
 * That makes rollback the load-bearing part, and rollback is where optimistic
 * UIs fail. The two classic bugs:
 *
 *   - **Rollback to a stale snapshot.** Capture "the list before", restore it
 *     on failure, and you also undo every OTHER change that landed in between
 *     — including a message that arrived by SSE. So this module never
 *     snapshots the list. Each action records the *inverse patch* for the
 *     specific messages it touched, and rollback replays only that.
 *
 *   - **Silent revert.** A message flips back and the user is left believing
 *     they mis-clicked. Every failure here carries the server's own message to
 *     the surface, which the brief makes a product requirement: a precise
 *     backend error turned into "an error occurred" is a defect.
 *
 * # Why the state is a patch map, not a copy of the list
 *
 * `MailScreen` owns `emails` from the server. Overlaying a `Map<id, patch>` on
 * top means the server's list stays the single source of truth: a refetch
 * simply replaces it, and any patch whose effect has already landed becomes a
 * no-op instead of fighting the fresh data. Applying the overlay is
 * `applyOverlay`, which is pure and therefore testable against every ordering.
 */

import {
  KEYWORD_FLAGGED,
  KEYWORD_SEEN,
  type Email,
} from "./types";

/** What a user can ask of one or more messages. */
export type MessageActionKind =
  | "markRead"
  | "markUnread"
  | "flag"
  | "unflag"
  | "archive"
  | "delete"
  | "move"
  /*
   * E2: spam and not-spam. They are MOVES — to the mailbox with role `junk`
   * and back to `inbox` respectively — and they are separate kinds rather
   * than plain moves because the UI must be able to say "Report spam" instead
   * of "Move to Junk", and because the undo stack keys its wording on the
   * kind. The transport is identical (`moveMessages`), which is deliberate:
   * the Rspamd learning that Mailcow's imapsieve hangs off the MOVE is
   * triggered by the IMAP operation, not by anything we could invent here.
   */
  | "spam"
  | "notSpam";

/** One action, already resolved against concrete messages. */
export interface MessageAction {
  readonly kind: MessageActionKind;
  /** The message ids the action applies to. */
  readonly ids: readonly string[];
  /** For `move`: the destination mailbox id. Absent otherwise. */
  readonly mailboxId?: string;
}

/**
 * The optimistic effect on ONE message: what to show before the server answers.
 *
 * `undefined` members mean "this action does not touch that property", which
 * is what keeps two concurrent actions on the same message (flag + archive)
 * from clobbering each other.
 */
export interface MessagePatch {
  readonly seen?: boolean;
  readonly flagged?: boolean;
  /** The mailbox the message now appears to be in. */
  readonly mailboxId?: string;
  /** True when the message should vanish from the current list. */
  readonly removed?: boolean;
}

/** The overlay: patches keyed by message id. */
export type Overlay = ReadonlyMap<string, MessagePatch>;

export const EMPTY_OVERLAY: Overlay = new Map();

/**
 * Computes the optimistic patch for an action on one message.
 *
 * `currentMailboxId` is the list the user is looking at: an archive or a move
 * removes the row from THAT list, but a move into the very folder being viewed
 * must not (moving a message to Inbox while in Inbox is a no-op visually).
 */
export function patchFor(
  action: MessageAction,
  _email: Email,
  currentMailboxId: string | undefined,
): MessagePatch {
  switch (action.kind) {
    case "markRead":
      return { seen: true };
    case "markUnread":
      return { seen: false };
    case "flag":
      return { flagged: true };
    case "unflag":
      return { flagged: false };
    case "delete":
      /*
       * A delete carries NO destination, and that is not an omission: the
       * server owns the W-A2 semantics (move to Trash, or expunge when already
       * there) and the client must not duplicate that rule. Either way the row
       * leaves the folder being viewed, which is the only thing the optimistic
       * paint needs to know.
       *
       * An earlier version fell through to the move branch and returned `{}`
       * for a missing mailboxId — which made the whole action a no-op that
       * never even issued the request. A test caught it.
       */
      return { removed: true };

    case "archive":
    case "spam":
    case "notSpam":
    case "move": {
      const destination = action.mailboxId;
      if (destination === undefined) return {};
      const leaves = currentMailboxId !== undefined && destination !== currentMailboxId;
      return {
        mailboxId: destination,
        ...(leaves ? { removed: true } : {}),
      };
    }
  }
  // The switch is exhaustive over MessageActionKind; this is unreachable and
  // exists only so a future kind added without a case is a compile error at
  // the switch rather than a silent {} here.
  return patchNever(action.kind);
}

function patchNever(kind: never): MessagePatch {
  throw new Error(`unhandled action kind: ${String(kind)}`);
}

/**
 * The INVERSE of a patch, computed from the message's state before it applied.
 *
 * This is what rollback replays. Note that it is derived from the *email*, not
 * from the patch — restoring "seen: false" is only correct if the message was
 * genuinely unread before, and a patch alone does not know that.
 */
export function inverseFor(
  patch: MessagePatch,
  email: Email,
  currentMailboxId: string | undefined,
): MessagePatch {
  const inverse: {
    seen?: boolean;
    flagged?: boolean;
    mailboxId?: string;
    removed?: boolean;
  } = {};
  if (patch.seen !== undefined) inverse.seen = email.keywords?.[KEYWORD_SEEN] === true;
  if (patch.flagged !== undefined) inverse.flagged = email.keywords?.[KEYWORD_FLAGGED] === true;
  if (patch.mailboxId !== undefined) {
    const original = Object.keys(email.mailboxIds ?? {})[0] ?? currentMailboxId;
    if (original !== undefined) inverse.mailboxId = original;
  }
  /*
   * Un-removing is keyed on `removed`, NOT on `mailboxId`. A delete removes
   * the row without naming a destination (the server owns W-A2), so keying the
   * restore on the mailbox would leave a failed delete's row hidden forever —
   * the message is still there, and the list would be silently lying.
   */
  if (patch.removed !== undefined) inverse.removed = false;
  return inverse;
}

/** Merges a patch into an overlay, returning a new overlay. */
export function withPatch(
  overlay: Overlay,
  id: string,
  patch: MessagePatch,
): Overlay {
  const next = new Map(overlay);
  const existing = next.get(id);
  next.set(id, existing === undefined ? patch : { ...existing, ...patch });
  return next;
}

/** Merges patches for many ids at once. */
export function withPatches(
  overlay: Overlay,
  patches: ReadonlyMap<string, MessagePatch>,
): Overlay {
  let next = overlay;
  for (const [id, patch] of patches) next = withPatch(next, id, patch);
  return next;
}

/**
 * Drops the given ids from the overlay entirely.
 *
 * Called when the server CONFIRMS an action: the real data now says what the
 * patch was pretending, so keeping the patch would make a later legitimate
 * change (a message marked unread on a phone, arriving by SSE) invisible.
 */
export function withoutIds(overlay: Overlay, ids: readonly string[]): Overlay {
  if (ids.length === 0) return overlay;
  const next = new Map(overlay);
  let changed = false;
  for (const id of ids) {
    if (next.delete(id)) changed = true;
  }
  return changed ? next : overlay;
}

/**
 * Applies the overlay to a server list.
 *
 * Removed messages are filtered out; keyword and mailbox patches are merged
 * into the email objects. The result is a NEW array only when something
 * changed, so React's identity checks still skip work on the common
 * empty-overlay path.
 */
export function applyOverlay(
  emails: readonly Email[],
  overlay: Overlay,
): readonly Email[] {
  if (overlay.size === 0) return emails;

  const out: Email[] = [];
  let changed = false;
  for (const email of emails) {
    const patch = overlay.get(email.id);
    if (patch === undefined) {
      out.push(email);
      continue;
    }
    if (patch.removed === true) {
      changed = true;
      continue;
    }
    out.push(applyPatch(email, patch));
    changed = true;
  }
  return changed ? out : emails;
}

/** Applies one patch to one email, producing a new object. */
export function applyPatch(email: Email, patch: MessagePatch): Email {
  let next = email;

  if (patch.seen !== undefined || patch.flagged !== undefined) {
    /*
     * Rebuilt by FILTERING rather than by copy-then-delete. A keyword that is
     * cleared must be absent from the object, not present as `false`: the JMAP
     * keywords property is an object-as-SET (RFC 8621 §4.1.1), so `isSeen`
     * reads presence, and a `{$seen: false}` entry would be read as an
     * unrecognised keyword rather than as "unread" by anything that iterates
     * the keys.
     */
    const cleared = new Set<string>();
    if (patch.seen === false) cleared.add(KEYWORD_SEEN);
    if (patch.flagged === false) cleared.add(KEYWORD_FLAGGED);

    const keywords: Record<string, boolean> = Object.fromEntries(
      Object.entries(email.keywords ?? {}).filter(([name]) => !cleared.has(name)),
    );
    if (patch.seen === true) keywords[KEYWORD_SEEN] = true;
    if (patch.flagged === true) keywords[KEYWORD_FLAGGED] = true;
    next = { ...next, keywords };
  }

  if (patch.mailboxId !== undefined) {
    next = { ...next, mailboxIds: { [patch.mailboxId]: true } };
  }
  return next;
}

/**
 * Decides which direction a toggle should go for a selection.
 *
 * Gmail's rule, copied: if ANY message in the selection is unread, "toggle
 * read" marks them all read; only when every one is already read does it mark
 * them unread. Toggling each message independently produces a selection in a
 * mixed state after the toggle, which is never what anyone wanted.
 */
export function resolveToggle(
  emails: readonly Email[],
  keyword: typeof KEYWORD_SEEN | typeof KEYWORD_FLAGGED,
): { readonly value: boolean } {
  if (keyword === KEYWORD_SEEN) {
    const anyUnread = emails.some((email) => email.keywords?.[KEYWORD_SEEN] !== true);
    return { value: anyUnread };
  }
  const anyUnflagged = emails.some((email) => email.keywords?.[KEYWORD_FLAGGED] !== true);
  return { value: anyUnflagged };
}

/**
 * A pending action, tracked so its failure can be reported and rolled back.
 *
 * The `inverses` map is captured at DISPATCH time from the emails as they then
 * were — see the file header on why a list snapshot would be wrong.
 */
export interface PendingAction {
  readonly id: number;
  readonly action: MessageAction;
  readonly inverses: ReadonlyMap<string, MessagePatch>;
}

/**
 * Builds the optimistic overlay and its inverse for a whole action.
 *
 * Returns both halves at once because they must be computed from the SAME
 * snapshot of the emails: computing the inverse later, after the optimistic
 * patch has already been applied to the rendered list, would invert the
 * optimistic state instead of the original.
 */
export function planAction(
  action: MessageAction,
  emails: readonly Email[],
  currentMailboxId: string | undefined,
): {
  readonly patches: ReadonlyMap<string, MessagePatch>;
  readonly inverses: ReadonlyMap<string, MessagePatch>;
} {
  const byId = new Map(emails.map((email) => [email.id, email]));
  const patches = new Map<string, MessagePatch>();
  const inverses = new Map<string, MessagePatch>();

  for (const id of action.ids) {
    const email = byId.get(id);
    if (email === undefined) continue;
    const patch = patchFor(action, email, currentMailboxId);
    if (Object.keys(patch).length === 0) continue;
    patches.set(id, patch);
    inverses.set(id, inverseFor(patch, email, currentMailboxId));
  }
  return { patches, inverses };
}

/**
 * True when `destroy` on this message will move it to Trash rather than erase
 * it (server arbitration W-A2).
 *
 * The UI must say which one is about to happen. "Delete" that means "move to
 * Trash" and "Delete" that means "gone forever" are different promises, and a
 * client that uses one word for both is lying to the user in one of the two
 * cases.
 */
export function deleteIsPermanent(
  email: Email,
  trashMailboxId: string | undefined,
): boolean {
  if (trashMailboxId === undefined) return false;
  return Object.keys(email.mailboxIds ?? {}).includes(trashMailboxId);
}
