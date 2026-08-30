import { describe, expect, it } from "vitest";

import {
  isLocalDate,
  isVacationActive,
  localDayEnd,
  localDayStart,
  toUtcDate,
  utcDateToLocalDate,
  validateVacation,
  type VacationDraft,
} from "./vacationWindow";

/**
 * The vacation form's decidable half (canon §2.8).
 *
 * The day-boundary tests are written so they hold in ANY timezone the suite
 * runs in — they assert the round trip and the local clock reading, never a
 * literal UTC string, because a literal would pass in UTC−3 and fail in CI.
 * That is exactly the class of bug the timezone decision was made to avoid, so
 * the tests must not depend on the very offset they exist to handle.
 */

function draft(overrides: Partial<VacationDraft> = {}): VacationDraft {
  return {
    isEnabled: true,
    fromDate: "",
    toDate: "",
    subject: "Fuera de la oficina",
    textBody: "Vuelvo el lunes.",
    ...overrides,
  };
}

describe("the local-day boundaries Gmail promises (12:00 AM / 11:59 PM)", () => {
  it("recognises a well-formed calendar date", () => {
    expect(isLocalDate("2026-09-01")).toBe(true);
    expect(isLocalDate("2026-02-30")).toBe(false);
    expect(isLocalDate("2026-13-01")).toBe(false);
    expect(isLocalDate("01/09/2026")).toBe(false);
    expect(isLocalDate("")).toBe(false);
  });

  it("maps a picked day to LOCAL midnight, not to UTC midnight", () => {
    const start = localDayStart("2026-09-01");
    expect(start).toBeDefined();
    const instant = new Date(start!);
    // Read back through the local clock: the whole promise is that the user's
    // own midnight is the boundary.
    expect(instant.getHours()).toBe(0);
    expect(instant.getMinutes()).toBe(0);
    expect(instant.getSeconds()).toBe(0);
    expect(instant.getDate()).toBe(1);
  });

  it("maps the end day to 23:59:59 local — the inclusive end of that day", () => {
    const end = localDayEnd("2026-09-14");
    const instant = new Date(end!);
    expect(instant.getHours()).toBe(23);
    expect(instant.getMinutes()).toBe(59);
    expect(instant.getSeconds()).toBe(59);
    expect(instant.getDate()).toBe(14);
  });

  it("emits the §8 UTCDate spelling the server parses", () => {
    expect(localDayStart("2026-09-01")).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);
    expect(toUtcDate(new Date(Date.UTC(2026, 8, 1, 3, 0, 0)))).toBe("2026-09-01T03:00:00Z");
  });

  it("round-trips a picked day back to the same day in the form", () => {
    expect(utcDateToLocalDate(localDayStart("2026-09-01")!)).toBe("2026-09-01");
    expect(utcDateToLocalDate(localDayEnd("2026-09-14")!)).toBe("2026-09-14");
  });

  it("shows an absent or unparseable bound as an empty field", () => {
    expect(utcDateToLocalDate(null)).toBe("");
    expect(utcDateToLocalDate("not a date")).toBe("");
  });

  it("refuses an impossible date rather than coercing it", () => {
    expect(localDayStart("2026-02-30")).toBeUndefined();
    expect(localDayEnd("")).toBeUndefined();
  });
});

describe("validation — every problem, not the first", () => {
  it("accepts a well-formed draft", () => {
    expect(validateVacation(draft())).toEqual([]);
  });

  it("refuses an end before the start", () => {
    expect(
      validateVacation(draft({ fromDate: "2026-09-10", toDate: "2026-09-01" })),
    ).toContain("endBeforeStart");
  });

  it("accepts a one-day window (start and end on the same day)", () => {
    // 00:00:00 to 23:59:59 on one day is a legal window, and the naive
    // comparison of two identical dates would reject it.
    expect(
      validateVacation(draft({ fromDate: "2026-09-01", toDate: "2026-09-01" })),
    ).toEqual([]);
  });

  it("refuses an ENABLED responder with neither subject nor body (§8)", () => {
    expect(validateVacation(draft({ subject: "  ", textBody: "" }))).toContain(
      "emptyMessage",
    );
  });

  it("allows a subject with no body, and a body with no subject", () => {
    expect(validateVacation(draft({ textBody: "" }))).toEqual([]);
    expect(validateVacation(draft({ subject: "" }))).toEqual([]);
  });

  it("lets a DISABLED responder be empty — nothing is being sent", () => {
    expect(
      validateVacation(draft({ isEnabled: false, subject: "", textBody: "" })),
    ).toEqual([]);
  });

  it("refuses a multi-line subject, as the server does", () => {
    expect(validateVacation(draft({ subject: "uno\ndos" }))).toContain("multilineSubject");
  });

  it("reports an unparseable date", () => {
    expect(validateVacation(draft({ fromDate: "mañana" }))).toContain("invalidDate");
  });

  it("reports BOTH problems at once when both are present", () => {
    const problems = validateVacation(
      draft({ subject: "", textBody: "", fromDate: "2026-09-10", toDate: "2026-09-01" }),
    );
    expect(problems).toContain("emptyMessage");
    expect(problems).toContain("endBeforeStart");
  });
});

describe("the inbox banner's window test", () => {
  const now = new Date("2026-09-05T12:00:00Z");

  it("is off when the responder is disabled", () => {
    expect(
      isVacationActive({ isEnabled: false, fromDate: null, toDate: null }, now),
    ).toBe(false);
  });

  it("is on when enabled with no bounds at all", () => {
    expect(isVacationActive({ isEnabled: true, fromDate: null, toDate: null }, now)).toBe(
      true,
    );
  });

  it("is on inside the window", () => {
    expect(
      isVacationActive(
        { isEnabled: true, fromDate: "2026-09-01T00:00:00Z", toDate: "2026-09-10T23:59:59Z" },
        now,
      ),
    ).toBe(true);
  });

  it("is OFF before the window — enabled is not the same as responding", () => {
    expect(
      isVacationActive(
        { isEnabled: true, fromDate: "2026-09-10T00:00:00Z", toDate: null },
        now,
      ),
    ).toBe(false);
  });

  it("is off after the window", () => {
    expect(
      isVacationActive(
        { isEnabled: true, fromDate: null, toDate: "2026-09-01T23:59:59Z" },
        now,
      ),
    ).toBe(false);
  });

  it("treats an open start bound as open", () => {
    expect(
      isVacationActive(
        { isEnabled: true, fromDate: null, toDate: "2026-09-10T23:59:59Z" },
        now,
      ),
    ).toBe(true);
  });

  it("treats an UNPARSEABLE bound as no bound — it still says you are responding", () => {
    expect(
      isVacationActive({ isEnabled: true, fromDate: "garbage", toDate: null }, now),
    ).toBe(true);
  });

  it("is on at the exact instants of both bounds (inclusive)", () => {
    const start = "2026-09-05T12:00:00Z";
    expect(isVacationActive({ isEnabled: true, fromDate: start, toDate: null }, now)).toBe(
      true,
    );
    expect(isVacationActive({ isEnabled: true, fromDate: null, toDate: start }, now)).toBe(
      true,
    );
  });
});
