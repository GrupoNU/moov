import { describe, expect, it } from "vitest";

import { MAX_EMPTY_ROUNDS, shouldContinue, summarize } from "./emptyTrash";

describe("when the empty-trash loop keeps going", () => {
  it("continues while a round is still destroying messages", () => {
    expect(shouldContinue({ attempted: 200, destroyed: 200 }, 1)).toBe(true);
  });

  it("stops when the query came back empty — the folder is done", () => {
    expect(shouldContinue({ attempted: 0, destroyed: 0 }, 1)).toBe(false);
  });

  /*
   * The runaway guard. A round that finds messages and destroys none means the
   * server is refusing that exact set; querying again returns the same set and
   * refuses identically, so continuing would hammer the server from a browser
   * tab forever.
   */
  it("stops when a round destroyed nothing despite finding messages", () => {
    expect(shouldContinue({ attempted: 200, destroyed: 0 }, 1)).toBe(false);
  });

  it("stops at the round ceiling even while making progress", () => {
    expect(shouldContinue({ attempted: 200, destroyed: 200 }, MAX_EMPTY_ROUNDS)).toBe(false);
    expect(shouldContinue({ attempted: 200, destroyed: 200 }, MAX_EMPTY_ROUNDS - 1)).toBe(true);
  });
});

describe("what the toast reports", () => {
  it("adds up every round", () => {
    const result = summarize(
      [
        { attempted: 200, destroyed: 200 },
        { attempted: 200, destroyed: 200 },
        { attempted: 12, destroyed: 12 },
        { attempted: 0, destroyed: 0 },
      ],
      undefined,
    );
    expect(result.destroyed).toBe(412);
    expect(result.incomplete).toBe(false);
  });

  /*
   * Emptiness is only PROVEN by a final round that found nothing. A round that
   * destroyed everything it found might still have left a 201st message behind
   * the 200-row window, so claiming "empty" there would be a lie.
   */
  it("calls it incomplete when the last round still found messages", () => {
    const result = summarize([{ attempted: 200, destroyed: 200 }], undefined);
    expect(result.destroyed).toBe(200);
    expect(result.incomplete).toBe(true);
  });

  it("calls it incomplete when the ceiling was reached", () => {
    const rounds = Array.from({ length: MAX_EMPTY_ROUNDS }, () => ({
      attempted: 200,
      destroyed: 200,
    }));
    expect(summarize(rounds, undefined).incomplete).toBe(true);
  });

  it("carries the server's own failure text through", () => {
    const result = summarize([{ attempted: 5, destroyed: 0 }], "mailbox is read-only");
    expect(result.failureMessage).toBe("mailbox is read-only");
  });

  it("reports nothing done for a Trash that was already empty", () => {
    const result = summarize([{ attempted: 0, destroyed: 0 }], undefined);
    expect(result.destroyed).toBe(0);
    expect(result.incomplete).toBe(false);
  });

  it("handles no rounds at all without claiming incompleteness", () => {
    expect(summarize([], undefined)).toEqual({
      destroyed: 0,
      incomplete: false,
      failureMessage: undefined,
    });
  });
});
