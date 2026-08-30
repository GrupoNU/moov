/**
 * Emptying the Trash (E2 item 7 — canon §2.2's "Empty Trash now").
 *
 * # Why this is a loop and not one call
 *
 * `Email/query` on this server answers within a hard 200-row window (see
 * `api.ts`'s header). A Trash with 4,000 messages is therefore not knowable in
 * one request, and a naive implementation would delete 200 and report success —
 * leaving a "now empty" Trash with 3,800 messages in it.
 *
 * So the operation is: query a window, destroy exactly what came back, query
 * again. The window is re-queried rather than paged because every destroy
 * shifts the list underneath any offset; asking for "the first 200 that are
 * still there" is the only stable cursor when the set is shrinking.
 *
 * # Why it must be bounded
 *
 * A loop driven by "is the folder empty yet" is a loop that never ends if the
 * server refuses one particular message — it would re-query the same 200 and
 * re-fail forever, hammering the server from a browser tab. {@link
 * emptyTrashPlan} therefore stops on the first round that destroys nothing,
 * and on a hard round ceiling, and REPORTS what is left rather than silently
 * giving up. An honest "3,800 deleted, some remain" beats a spinner.
 */

/** How many rounds of query-then-destroy are attempted before stopping. */
export const MAX_EMPTY_ROUNDS = 40;

/** What one round of the loop did. */
export interface EmptyRound {
  readonly attempted: number;
  readonly destroyed: number;
}

/** How the whole operation ended. */
export interface EmptyTrashResult {
  readonly destroyed: number;
  /** True when the loop stopped before the folder was empty. */
  readonly incomplete: boolean;
  /** The server's own words for the first failure, when there was one. */
  readonly failureMessage: string | undefined;
}

/**
 * Decides whether the loop should run another round.
 *
 * Pure so the three stopping conditions can be enumerated by a test, which is
 * where a runaway browser loop would otherwise be discovered — in production.
 */
export function shouldContinue(round: EmptyRound, roundsSoFar: number): boolean {
  if (roundsSoFar >= MAX_EMPTY_ROUNDS) return false;
  // Nothing left to try: the query came back empty.
  if (round.attempted === 0) return false;
  // Tried and destroyed nothing: the server is refusing this set, and asking
  // again would refuse identically. Stop and report rather than spin.
  if (round.destroyed === 0) return false;
  return true;
}

/** Folds the rounds into the result the toast reports. */
export function summarize(
  rounds: readonly EmptyRound[],
  failureMessage: string | undefined,
): EmptyTrashResult {
  const destroyed = rounds.reduce((total, round) => total + round.destroyed, 0);
  const last = rounds[rounds.length - 1];
  /*
   * "Incomplete" means the folder was NOT observed empty. The only proof of
   * emptiness this loop can have is a final round that found nothing to
   * attempt; a round that destroyed everything it found might still have left
   * a 201st message behind the window.
   */
  const incomplete =
    last === undefined ? false : last.attempted > 0 || rounds.length >= MAX_EMPTY_ROUNDS;
  return { destroyed, incomplete, failureMessage };
}
