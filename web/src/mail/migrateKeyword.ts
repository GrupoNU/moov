/**
 * Renaming and deleting a label: the bounded keyword migration (L3 E8).
 *
 * # Why renaming a label is a data migration
 *
 * A label has no record of its own. It IS the keyword on every message that
 * carries it, so renaming "work" to "trabajo" means rewriting `$label:work` to
 * `$label:trabajo` on every one of them, and deleting a label means removing
 * the keyword from every one of them. There is no server-side rename; Bulwark
 * reached the same conclusion and calls its version `migrateKeyword()`
 * (research 05 §1.3).
 *
 * # Why it is a bounded loop, exactly like emptyTrash
 *
 * `Email/query` answers within a hard 200-row window (see `api.ts`'s header),
 * so a label on 4,000 messages is not knowable in one request. The shape is the
 * same one `emptyTrash.ts` established and for the same reasons, restated
 * because they are the reasons the loop is safe:
 *
 *   - **Re-query, never page.** Each round asks for "the first N messages that
 *     STILL have the old keyword". Since every successful round removes the
 *     keyword from what it touched, the set shrinks and the same query is a
 *     stable cursor. An offset would skip messages as the set moved underneath
 *     it.
 *   - **Stop when a round changes nothing.** A server refusing one particular
 *     message would otherwise make this re-query and re-fail the same 200
 *     forever, from a browser tab.
 *   - **A hard round ceiling** on top of that, so even a server that keeps
 *     reporting success without applying anything cannot spin.
 *   - **Report what is left.** "1,800 migrated, some remain" is honest; a
 *     spinner that stops is not.
 *
 * # Why it is abortable
 *
 * Forty rounds against a transatlantic pilot is a minute of wall clock. A user
 * who started a rename on the wrong label must be able to stop it, and the
 * cancellation has to be checked BETWEEN rounds so the in-flight write is never
 * torn — a half-applied `Email/set` would leave messages with both keywords,
 * which is the one outcome worse than not renaming at all.
 */

/** How many query-then-write rounds are attempted before stopping. */
export const MAX_MIGRATE_ROUNDS = 40;

/** How many messages one round asks for and writes. */
export const MIGRATE_BATCH_SIZE = 100;

/** What one round did. */
export interface MigrateRound {
  /** How many messages the query returned. */
  readonly attempted: number;
  /** How many the server confirmed. */
  readonly migrated: number;
}

/** How the whole migration ended. */
export interface MigrateResult {
  readonly migrated: number;
  readonly rounds: number;
  /** True when the loop stopped before the keyword was gone from every message. */
  readonly incomplete: boolean;
  /** True when the caller aborted it. */
  readonly aborted: boolean;
  /** The server's own words for the first failure, when there was one. */
  readonly failureMessage: string | undefined;
}

/**
 * Decides whether the loop should run another round.
 *
 * Pure, so the four stopping conditions can be enumerated by a test — which is
 * where a runaway browser loop is otherwise discovered in production.
 */
export function shouldContinueMigration(
  round: MigrateRound,
  roundsSoFar: number,
  isAborted: boolean,
): boolean {
  if (isAborted) return false;
  if (roundsSoFar >= MAX_MIGRATE_ROUNDS) return false;
  // Nothing matched: the keyword is gone from every message.
  if (round.attempted === 0) return false;
  // Tried and changed nothing: asking again would fail identically.
  if (round.migrated === 0) return false;
  return true;
}

/** Folds the rounds into the result the toast reports. */
export function summarizeMigration(
  rounds: readonly MigrateRound[],
  options: { readonly aborted?: boolean; readonly failureMessage?: string | undefined } = {},
): MigrateResult {
  const migrated = rounds.reduce((total, round) => total + round.migrated, 0);
  const last = rounds[rounds.length - 1];
  const aborted = options.aborted === true;
  /*
   * "Incomplete" means the keyword was NOT observed gone. The only proof is a
   * final round that found nothing to attempt; a round that migrated everything
   * it found might still have left a 101st message behind the window.
   */
  const incomplete =
    aborted ||
    (last === undefined ? false : last.attempted > 0 || rounds.length >= MAX_MIGRATE_ROUNDS);
  return {
    migrated,
    rounds: rounds.length,
    incomplete,
    aborted,
    failureMessage: options.failureMessage,
  };
}

/**
 * Progress, for the UI to render while the loop runs.
 *
 * `total` is deliberately optional and deliberately NOT a promise of the final
 * count: `Email/query` reports `total` only when it can, and inside a bounded
 * window that number can be a floor rather than a total. A progress bar that
 * reaches 100% and keeps going is worse than a counter that just counts, so the
 * UI shows the count and shows the total only when the server gave one.
 */
export interface MigrateProgress {
  readonly migrated: number;
  readonly total: number | undefined;
  readonly round: number;
}
