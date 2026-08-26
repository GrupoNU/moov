import { describe, expect, it, vi } from "vitest";

import {
  AUTOSAVE_DEBOUNCE_MS,
  AUTOSAVE_MAX_WAIT_MS,
  createAutosaveScheduler,
  parseSendAt,
  secondsUntil,
  undoWindowSeconds,
} from "./drafts";

/** A controllable clock and timer queue, so no test waits on real time. */
function fakeTimers() {
  let now = 1_000_000;
  let nextHandle = 1;
  const queue = new Map<number, { at: number; fn: () => void }>();

  return {
    now: (): number => now,
    setTimer: (fn: () => void, ms: number): number => {
      const handle = nextHandle++;
      queue.set(handle, { at: now + ms, fn });
      return handle;
    },
    clearTimer: (handle: number): void => {
      queue.delete(handle);
    },
    /** Advances the clock, running every timer that comes due. */
    advance(ms: number): void {
      now += ms;
      for (const [handle, entry] of [...queue.entries()]) {
        if (entry.at <= now) {
          queue.delete(handle);
          entry.fn();
        }
      }
    },
    pending(): number {
      return queue.size;
    },
  };
}

describe("createAutosaveScheduler", () => {
  it("saves once, after the debounce, for a burst of typing", () => {
    const timers = fakeTimers();
    const save = vi.fn();
    const scheduler = createAutosaveScheduler(save, { ...timers, debounceMs: 2000 });

    scheduler.touch();
    timers.advance(500);
    scheduler.touch();
    timers.advance(500);
    scheduler.touch();
    expect(save).not.toHaveBeenCalled();

    timers.advance(2000);
    expect(save).toHaveBeenCalledTimes(1);
  });

  /*
   * The bug a pure debounce has: someone composing for four minutes without a
   * two-second pause has nothing saved when the tab crashes. The max-wait
   * turns the debounce into a "save at least every N seconds" guarantee.
   */
  it("saves anyway once the max wait has elapsed during continuous typing", () => {
    const timers = fakeTimers();
    const save = vi.fn();
    const scheduler = createAutosaveScheduler(save, {
      ...timers,
      debounceMs: 2000,
      maxWaitMs: 10_000,
    });

    // Type every 500 ms for 12 s: a pure debounce would never fire.
    for (let elapsed = 0; elapsed < 12_000; elapsed += 500) {
      scheduler.touch();
      timers.advance(500);
    }
    expect(save).toHaveBeenCalled();
  });

  it("flush runs a pending save immediately", () => {
    const timers = fakeTimers();
    const save = vi.fn();
    const scheduler = createAutosaveScheduler(save, timers);

    scheduler.touch();
    expect(scheduler.isPending()).toBe(true);
    scheduler.flush();
    expect(save).toHaveBeenCalledTimes(1);
    expect(scheduler.isPending()).toBe(false);
  });

  it("flush on a clean composer saves nothing", () => {
    const timers = fakeTimers();
    const save = vi.fn();
    createAutosaveScheduler(save, timers).flush();
    expect(save).not.toHaveBeenCalled();
  });

  /*
   * Discarding a draft must not race a scheduled save into recreating it.
   */
  it("cancel drops a pending save without running it", () => {
    const timers = fakeTimers();
    const save = vi.fn();
    const scheduler = createAutosaveScheduler(save, timers);

    scheduler.touch();
    scheduler.cancel();
    timers.advance(60_000);
    expect(save).not.toHaveBeenCalled();
    expect(timers.pending()).toBe(0);
  });

  it("starts a fresh window after a save, rather than firing immediately again", () => {
    const timers = fakeTimers();
    const save = vi.fn();
    const scheduler = createAutosaveScheduler(save, {
      ...timers,
      debounceMs: 2000,
      maxWaitMs: 10_000,
    });

    scheduler.touch();
    timers.advance(2000);
    expect(save).toHaveBeenCalledTimes(1);

    scheduler.touch();
    timers.advance(1000);
    expect(save).toHaveBeenCalledTimes(1);
    timers.advance(1000);
    expect(save).toHaveBeenCalledTimes(2);
  });

  it("ships defaults that are sane relative to each other", () => {
    expect(AUTOSAVE_DEBOUNCE_MS).toBeLessThan(AUTOSAVE_MAX_WAIT_MS);
  });
});

describe("secondsUntil", () => {
  /*
   * Ceil, not floor: a window with 4.2 s left must read "5". A countdown that
   * shows 0 while the button still works is a countdown nobody trusts.
   */
  it("rounds up so the last visible number is 1", () => {
    expect(secondsUntil(10_000, 5_800)).toBe(5);
    expect(secondsUntil(10_000, 9_100)).toBe(1);
    expect(secondsUntil(10_000, 9_999)).toBe(1);
  });

  it("floors at zero once the instant has passed", () => {
    expect(secondsUntil(10_000, 10_000)).toBe(0);
    expect(secondsUntil(10_000, 30_000)).toBe(0);
  });
});

describe("parseSendAt", () => {
  it("parses the server's UTC ISO form", () => {
    expect(parseSendAt("2026-08-26T12:00:10Z")).toBe(Date.parse("2026-08-26T12:00:10Z"));
  });

  /*
   * A malformed value must not become a window of NaN seconds, which renders
   * blank and never expires.
   */
  it("returns undefined rather than NaN for garbage", () => {
    expect(parseSendAt("not a date")).toBeUndefined();
    expect(parseSendAt("")).toBeUndefined();
    expect(parseSendAt(undefined)).toBeUndefined();
  });
});

describe("undoWindowSeconds", () => {
  /*
   * The server clamps its window to 5-30 s per account. A hardcoded 10 would
   * either offer undo after the mail left, or stop offering it while the
   * server still would.
   */
  it("derives the window from the server's own sendAt", () => {
    const now = Date.parse("2026-08-26T12:00:00Z");
    expect(undoWindowSeconds("2026-08-26T12:00:10Z", now)).toBe(10);
    expect(undoWindowSeconds("2026-08-26T12:00:30Z", now)).toBe(30);
    expect(undoWindowSeconds("2026-08-26T12:00:05Z", now)).toBe(5);
  });

  it("is zero for a send already due", () => {
    const now = Date.parse("2026-08-26T12:00:00Z");
    expect(undoWindowSeconds("2026-08-26T11:59:55Z", now)).toBe(0);
  });

  it("is undefined when the server sent nothing usable", () => {
    expect(undoWindowSeconds(undefined, Date.now())).toBeUndefined();
  });
});
