/**
 * The optimistic-action controller (P3 deliverable 1).
 *
 * # What this hook owns, and why it is a hook rather than more MailScreen
 *
 * The whole optimistic cycle — paint, call, confirm or roll back, report —
 * is one coherent mechanism with real invariants, and it was not going to
 * survive being spread across another 200 lines of `MailScreen`. The pure
 * maths lives in `mail/actions.ts` and is unit-tested there; this hook is the
 * effectful shell around it: it holds the overlay, issues the JMAP call, and
 * decides what to do with each answer.
 *
 * # The three rules it enforces
 *
 * 1. **Paint before the round trip.** ADR §6 asks for <100 ms perceived, and
 *    the pilot's transatlantic RTT alone is ~530 ms. Only the client can win
 *    that.
 *
 * 2. **Roll back the inverse, never a snapshot.** A snapshot of the list would
 *    also undo everything that landed in between — a message that arrived by
 *    SSE, another action that succeeded. `planAction` captures the inverse
 *    patch per message at dispatch time; failure replays exactly that.
 *
 * 3. **Never a silent revert.** Every failure surfaces the SERVER's own
 *    sentence. RFC 8620 §5.3 gives per-record errors, so a batch can half
 *    succeed — and the half that failed is named and restored while the half
 *    that worked is left alone.
 */

import { useCallback, useRef, useState } from "react";

import type { JmapClient } from "../../api/jmap";
import {
  applyOverlay,
  planAction,
  withPatches,
  withoutIds,
  EMPTY_OVERLAY,
  type MessageAction,
  type Overlay,
} from "../../mail/actions";
import type { Email } from "../../mail/types";
import {
  destroyMessages,
  firstFailureMessage,
  moveMessages,
  setKeyword,
  type SetOutcome,
} from "../../mail/write";
import { KEYWORD_FLAGGED, KEYWORD_SEEN } from "../../mail/types";

/** What the caller has to tell the hook to make a call. */
export interface ActionContext {
  readonly client: JmapClient | undefined;
  readonly accountId: string;
  /** The mailbox being viewed, so a move knows whether the row leaves. */
  readonly currentMailboxId: string | undefined;
}

/** How an action ended, for the caller's toast. */
export interface ActionResult {
  readonly action: MessageAction;
  readonly succeeded: readonly string[];
  readonly failed: readonly string[];
  /** The server's own words for the first failure, when there was one. */
  readonly failureMessage: string | undefined;
}

export interface MessageActionsApi {
  /** Applies the overlay to a server list. */
  readonly project: (emails: readonly Email[]) => readonly Email[];
  /** Dispatches an action optimistically. */
  readonly run: (action: MessageAction, emails: readonly Email[]) => Promise<ActionResult>;
  /** True while at least one action is in flight. */
  readonly isBusy: boolean;
  /** Clears every pending patch — used when the list is refetched wholesale. */
  readonly reset: () => void;
}

export function useMessageActions(context: ActionContext): MessageActionsApi {
  const [overlay, setOverlay] = useState<Overlay>(EMPTY_OVERLAY);
  const [inFlight, setInFlight] = useState(0);
  const { client, accountId, currentMailboxId } = context;

  /*
   * The overlay is mirrored into a ref so a callback can read the CURRENT
   * value without being re-created on every change — which would otherwise
   * make `run` a new function on every keystroke and defeat every memo above
   * it.
   */
  const overlayRef = useRef(overlay);
  overlayRef.current = overlay;

  const project = useCallback(
    (emails: readonly Email[]) => applyOverlay(emails, overlay),
    [overlay],
  );

  const reset = useCallback((): void => {
    setOverlay(EMPTY_OVERLAY);
  }, []);

  const run = useCallback(
    async (action: MessageAction, emails: readonly Email[]): Promise<ActionResult> => {
      if (client === undefined || accountId === "" || action.ids.length === 0) {
        return { action, succeeded: [], failed: [], failureMessage: undefined };
      }

      // Both halves from the SAME snapshot: computing the inverse after the
      // optimistic patch has been applied would invert the optimistic state.
      const { patches, inverses } = planAction(action, emails, currentMailboxId);
      if (patches.size === 0) {
        return { action, succeeded: [], failed: [], failureMessage: undefined };
      }

      setOverlay((current) => withPatches(current, patches));
      setInFlight((count) => count + 1);

      let outcome: SetOutcome;
      try {
        outcome = await callFor(client, accountId, action);
      } catch (error) {
        // A transport-level failure means NOTHING was applied: roll everything
        // back and report the reason.
        setOverlay((current) => withPatches(current, inverses));
        setInFlight((count) => count - 1);
        return {
          action,
          succeeded: [],
          failed: [...patches.keys()],
          failureMessage: error instanceof Error ? error.message : String(error),
        };
      }

      const failedIds = Object.keys(outcome.failed);
      const failedSet = new Set(failedIds);
      const succeeded = [...patches.keys()].filter((id) => !failedSet.has(id));

      setOverlay((current) => {
        // Roll back ONLY what failed…
        const rolledBack = withPatches(
          current,
          new Map(failedIds.flatMap((id) => {
            const inverse = inverses.get(id);
            return inverse === undefined ? [] : [[id, inverse] as const];
          })),
        );
        /*
         * …and DROP the patches for what succeeded, rather than keeping them.
         * The server's data now says what the patch was pretending, so holding
         * the patch would mask a later legitimate change arriving by SSE.
         *
         * A removal is the exception: the row must stay hidden until the list
         * is refetched, or an archived message would reappear for the seconds
         * between the answer and the refresh.
         */
        const confirmable = succeeded.filter((id) => patches.get(id)?.removed !== true);
        return withoutIds(rolledBack, confirmable);
      });
      setInFlight((count) => count - 1);

      return {
        action,
        succeeded,
        failed: failedIds,
        failureMessage: firstFailureMessage(outcome),
      };
    },
    [client, accountId, currentMailboxId],
  );

  return { project, run, isBusy: inFlight > 0, reset };
}

/** Maps an action onto the JMAP call that performs it. */
function callFor(
  client: JmapClient,
  accountId: string,
  action: MessageAction,
): Promise<SetOutcome> {
  switch (action.kind) {
    case "markRead":
      return setKeyword(client, accountId, action.ids, KEYWORD_SEEN, true);
    case "markUnread":
      return setKeyword(client, accountId, action.ids, KEYWORD_SEEN, false);
    case "flag":
      return setKeyword(client, accountId, action.ids, KEYWORD_FLAGGED, true);
    case "unflag":
      return setKeyword(client, accountId, action.ids, KEYWORD_FLAGGED, false);
    case "delete":
      /*
       * `destroy`, NOT a move to Trash computed here. The server owns the
       * W-A2 semantics — move to Trash unless already there, expunge only from
       * Trash — and re-implementing that rule client-side would drift from it.
       * The UI's job is to SAY which of the two is about to happen
       * (`deleteIsPermanent`), not to decide it.
       */
      return destroyMessages(client, accountId, action.ids);
    case "archive":
    case "move": {
      const mailboxId = action.mailboxId;
      if (mailboxId === undefined) {
        return Promise.resolve({
          updated: [],
          destroyed: [],
          created: {},
          failed: {},
          newState: undefined,
        });
      }
      return moveMessages(client, accountId, action.ids, mailboxId);
    }
  }
}
