import { describe, expect, it } from "vitest";

import {
  MAX_MIGRATE_ROUNDS,
  MIGRATE_BATCH_SIZE,
  shouldContinueMigration,
  summarizeMigration,
  type MigrateRound,
} from "./migrateKeyword";

const round = (attempted: number, migrated: number): MigrateRound => ({ attempted, migrated });

describe("shouldContinueMigration — the four stopping conditions", () => {
  it("continues while a round is making progress", () => {
    expect(shouldContinueMigration(round(100, 100), 1, false)).toBe(true);
  });

  it("stops when the query returns nothing: the keyword is gone", () => {
    expect(shouldContinueMigration(round(0, 0), 1, false)).toBe(false);
  });

  it("stops when a round changed nothing — the server is refusing this set", () => {
    /*
     * The runaway case: without this, the loop re-queries the same 100 and
     * re-fails forever, hammering the server from a browser tab.
     */
    expect(shouldContinueMigration(round(100, 0), 1, false)).toBe(false);
  });

  it("stops at the round ceiling even while making progress", () => {
    expect(shouldContinueMigration(round(100, 100), MAX_MIGRATE_ROUNDS - 1, false)).toBe(true);
    expect(shouldContinueMigration(round(100, 100), MAX_MIGRATE_ROUNDS, false)).toBe(false);
  });

  it("stops immediately when aborted, whatever the round says", () => {
    expect(shouldContinueMigration(round(100, 100), 1, true)).toBe(false);
  });

  it("has a batch size that fits inside the server's 200-row window", () => {
    expect(MIGRATE_BATCH_SIZE).toBeLessThanOrEqual(200);
  });
});

describe("summarizeMigration", () => {
  it("sums what was migrated", () => {
    const result = summarizeMigration([round(100, 100), round(40, 40), round(0, 0)]);
    expect(result.migrated).toBe(140);
    expect(result.rounds).toBe(3);
  });

  it("is complete only when a final round found nothing to attempt", () => {
    // The only proof of completion: a round that matched zero messages. A round
    // that migrated everything it FOUND might still have left a 101st behind
    // the window.
    expect(summarizeMigration([round(100, 100), round(0, 0)]).incomplete).toBe(false);
    expect(summarizeMigration([round(100, 100)]).incomplete).toBe(true);
  });

  it("is incomplete when it stopped at the round ceiling", () => {
    const rounds = Array.from({ length: MAX_MIGRATE_ROUNDS }, () => round(100, 100));
    expect(summarizeMigration(rounds).incomplete).toBe(true);
  });

  it("is incomplete and flagged when aborted", () => {
    const result = summarizeMigration([round(100, 100)], { aborted: true });
    expect(result.aborted).toBe(true);
    expect(result.incomplete).toBe(true);
    // The count is still honest: 100 messages really were migrated.
    expect(result.migrated).toBe(100);
  });

  it("carries the server's own words for the first failure", () => {
    const result = summarizeMigration([round(100, 40)], {
      failureMessage: "over quota",
    });
    expect(result.failureMessage).toBe("over quota");
  });

  it("reports nothing done for a migration that never ran", () => {
    const result = summarizeMigration([]);
    expect(result.migrated).toBe(0);
    expect(result.incomplete).toBe(false);
  });
});
