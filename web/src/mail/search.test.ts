import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  createDebouncer,
  isSearchable,
  MIN_QUERY_LENGTH,
  normalizeQuery,
  refusalFor,
  SEARCH_DEBOUNCE_MS,
} from "./search";

describe("normalizeQuery", () => {
  it("trims and collapses whitespace so equal searches are equal strings", () => {
    // websearch_to_tsquery ANDs the words either way; treating these as
    // different queries would fire a second request for the same result.
    expect(normalizeQuery("  arquitectura   del  sync  ")).toBe("arquitectura del sync");
  });

  it("leaves a normal query untouched", () => {
    expect(normalizeQuery("arquitectura")).toBe("arquitectura");
  });

  it("normalises whitespace-only input to the empty string", () => {
    expect(normalizeQuery("   ")).toBe("");
  });
});

describe("isSearchable", () => {
  it("rejects queries below the minimum length", () => {
    expect(isSearchable("")).toBe(false);
    expect(isSearchable("a")).toBe(false);
  });

  it("accepts a query at the minimum length", () => {
    expect(isSearchable("a".repeat(MIN_QUERY_LENGTH))).toBe(true);
  });
});

describe("createDebouncer", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("fires once after the delay, not once per keystroke", () => {
    // The server enforces maxConcurrentRequests: 8 with a 429. Typing
    // "arquitectura" un-debounced would be 12 requests.
    const spy = vi.fn();
    const debounced = createDebouncer(spy, SEARCH_DEBOUNCE_MS);

    for (const q of ["a", "ar", "arq", "arqu", "arqui"]) debounced.run(q);
    expect(spy).not.toHaveBeenCalled();

    vi.advanceTimersByTime(SEARCH_DEBOUNCE_MS);
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("uses the LAST value, so the final keystroke is the one that counts", () => {
    const spy = vi.fn();
    const debounced = createDebouncer(spy, SEARCH_DEBOUNCE_MS);
    debounced.run("arq");
    debounced.run("arquitectura");
    vi.advanceTimersByTime(SEARCH_DEBOUNCE_MS);
    expect(spy).toHaveBeenCalledWith("arquitectura");
  });

  it("restarts the clock on each call rather than firing mid-word", () => {
    const spy = vi.fn();
    const debounced = createDebouncer(spy, 200);
    debounced.run("a");
    vi.advanceTimersByTime(150);
    debounced.run("ab");
    vi.advanceTimersByTime(150);
    expect(spy).not.toHaveBeenCalled();
    vi.advanceTimersByTime(50);
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("cancels a pending call", () => {
    const spy = vi.fn();
    const debounced = createDebouncer(spy, 200);
    debounced.run("a");
    debounced.cancel();
    vi.advanceTimersByTime(1000);
    expect(spy).not.toHaveBeenCalled();
  });

  it("flushes a pending call immediately (Enter should not wait)", () => {
    const spy = vi.fn();
    const debounced = createDebouncer(spy, 200);
    debounced.run("arquitectura");
    debounced.flush();
    expect(spy).toHaveBeenCalledWith("arquitectura");
    // And does not fire again when the timer would have elapsed.
    vi.advanceTimersByTime(1000);
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("does nothing on flush when nothing is pending", () => {
    const spy = vi.fn();
    createDebouncer(spy, 200).flush();
    expect(spy).not.toHaveBeenCalled();
  });
});

describe("refusalFor", () => {
  /*
   * unsupportedFilter is a NORMAL answer from this server's bounded
   * repertoire, not a failure. Mapping it to a refusal — rather than to an
   * empty list — is the difference between "no results" (a lie) and "this
   * server cannot answer that search" (the truth).
   */
  it("classifies unsupportedFilter as a refusal, carrying the server's reason", () => {
    expect(refusalFor("unsupportedFilter", 'filter condition "body" is not supported')).toEqual({
      kind: "unsupported",
      description: 'filter condition "body" is not supported',
    });
  });

  it("classifies unsupportedSort as a refusal too", () => {
    expect(refusalFor("unsupportedSort")).toEqual({ kind: "unsupported" });
  });

  it("does NOT classify a real failure as a refusal", () => {
    // serverFail belongs in the error taxonomy, not in a "can't search that"
    // message.
    expect(refusalFor("serverFail")).toBeUndefined();
    expect(refusalFor("invalidArguments")).toBeUndefined();
  });
});
