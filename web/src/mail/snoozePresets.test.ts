import { describe, expect, it } from "vitest";

import {
  LATER_TODAY_CEILING_HOUR,
  MORNING_HOUR,
  datetimeLocalValue,
  parseDatetimeLocal,
  snoozePresets,
  toUntilString,
  type SnoozePresetId,
} from "./snoozePresets";

/**
 * The presets are OURS (canon §5 lists Gmail's times as unsourced), so these
 * tests are the specification of the choice rather than a comparison against
 * something published. Every case below is a calendar edge — the reason the
 * computation is a pure function of `now` in the first place.
 *
 * All dates are built with the local-time constructor, because the module
 * works in local time deliberately (a "tomorrow at 8" computed in UTC wakes a
 * reader in Buenos Aires at five in the morning).
 */

/** 2026-08-31 is a Monday; the whole week below is anchored on it. */
const MONDAY = 31;

function at(day: number, hour: number, minute = 0): Date {
  return new Date(2026, 7, day, hour, minute, 0, 0);
}

function ids(now: Date): readonly SnoozePresetId[] {
  return snoozePresets(now).map((preset) => preset.id);
}

function presetAt(now: Date, id: SnoozePresetId): Date {
  const found = snoozePresets(now).find((preset) => preset.id === id);
  if (found === undefined) throw new Error(`preset ${id} was not offered`);
  return found.at;
}

describe("snoozePresets — later today", () => {
  it("offers now + 3h, rounded up to the next half hour", () => {
    const later = presetAt(at(MONDAY, 9, 12), "laterToday");
    expect(later.getHours()).toBe(12);
    expect(later.getMinutes()).toBe(30);
    expect(later.getSeconds()).toBe(0);
  });

  it("leaves an exact half hour alone rather than pushing it forward", () => {
    const later = presetAt(at(MONDAY, 9, 30), "laterToday");
    expect(later.getHours()).toBe(12);
    expect(later.getMinutes()).toBe(30);
  });

  it("rounds :31 up to the next hour, not to :30 of the same one", () => {
    const later = presetAt(at(MONDAY, 9, 31), "laterToday");
    expect(later.getHours()).toBe(13);
    expect(later.getMinutes()).toBe(0);
  });

  it("is withdrawn once the ROUNDED +3h reaches the 23:00 ceiling", () => {
    // 20:00 + 3h = 23:00 exactly — the first time that reads as "tonight".
    expect(ids(at(MONDAY, 20, 0))).not.toContain("laterToday");
    // 19:31 + 3h = 22:31, which the half-hour rounding pushes to 23:00 — so it
    // is withdrawn too. The ceiling is applied AFTER rounding, deliberately:
    // the rounded time is the one the user will be shown and the one the
    // message actually wakes at, so it is the one the label has to be true of.
    expect(ids(at(MONDAY, 19, 31))).not.toContain("laterToday");
    // 19:30 rounds to itself, so +3h lands at 22:30 and the option survives.
    expect(ids(at(MONDAY, 19, 30))).toContain("laterToday");
  });

  it("is withdrawn after midnight rollover, which the hour test alone misses", () => {
    // 23:00 + 3h = 02:00 TOMORROW: the hour (2) passes the ceiling test, so
    // only the same-day check catches it. This is the bug the two conditions
    // exist for.
    const late = at(MONDAY, 23, 0);
    expect(late.getHours() + 3).toBeGreaterThan(LATER_TODAY_CEILING_HOUR);
    expect(ids(late)).not.toContain("laterToday");
  });
});

describe("snoozePresets — tomorrow", () => {
  it("is the next calendar day at the morning hour", () => {
    // 2026-08-31 is the last day of August, so "tomorrow" is 2026-09-01 —
    // which is also why every date assertion here names the real calendar date
    // rather than doing arithmetic on the day number.
    const tomorrow = presetAt(at(MONDAY, 9, 12), "tomorrow");
    expect(tomorrow.getMonth()).toBe(8);
    expect(tomorrow.getDate()).toBe(1);
    expect(tomorrow.getHours()).toBe(MORNING_HOUR);
    expect(tomorrow.getMinutes()).toBe(0);
  });

  it("crosses a month boundary correctly", () => {
    // 2026-08-31 23:30 → 2026-09-01 08:00.
    const tomorrow = presetAt(at(MONDAY, 23, 30), "tomorrow");
    expect(tomorrow.getMonth()).toBe(8); // September
    expect(tomorrow.getDate()).toBe(1);
    expect(tomorrow.getHours()).toBe(MORNING_HOUR);
  });

  it("is always offered, at every hour of the day", () => {
    for (let hour = 0; hour < 24; hour += 1) {
      expect(ids(at(MONDAY, hour))).toContain("tomorrow");
    }
  });
});

describe("snoozePresets — this weekend", () => {
  it("is the coming Saturday at the morning hour, from a Monday", () => {
    const weekend = presetAt(at(MONDAY, 9), "thisWeekend");
    expect(weekend.getDay()).toBe(6);
    // Monday 2026-08-31 + 5 days = Saturday 2026-09-05.
    expect(weekend.getMonth()).toBe(8);
    expect(weekend.getDate()).toBe(5);
    expect(weekend.getHours()).toBe(MORNING_HOUR);
  });

  it("is TOMORROW when asked on a Friday evening", () => {
    // Friday 2026-09-04 at 19:00 — the edge the brief names.
    const friday = new Date(2026, 8, 4, 19, 0, 0, 0);
    expect(friday.getDay()).toBe(5);
    const weekend = presetAt(friday, "thisWeekend");
    expect(weekend.getDay()).toBe(6);
    expect(weekend.getDate()).toBe(5);
    expect(weekend.getHours()).toBe(MORNING_HOUR);
  });

  it("is NOT offered during the weekend it names", () => {
    const saturday = new Date(2026, 8, 5, 10, 0, 0, 0);
    const sunday = new Date(2026, 8, 6, 10, 0, 0, 0);
    expect(saturday.getDay()).toBe(6);
    expect(sunday.getDay()).toBe(0);
    expect(ids(saturday)).not.toContain("thisWeekend");
    expect(ids(sunday)).not.toContain("thisWeekend");
  });
});

describe("snoozePresets — next week", () => {
  it("is the coming Monday from midweek", () => {
    // Wednesday 2026-09-02.
    const wednesday = new Date(2026, 8, 2, 14, 0, 0, 0);
    expect(wednesday.getDay()).toBe(3);
    const next = presetAt(wednesday, "nextWeek");
    expect(next.getDay()).toBe(1);
    expect(next.getDate()).toBe(7);
  });

  it("is SEVEN days out when asked on a Monday, never today", () => {
    const monday = at(MONDAY, 9);
    expect(monday.getDay()).toBe(1);
    const next = presetAt(monday, "nextWeek");
    expect(next.getDay()).toBe(1);
    expect(next.getMonth()).toBe(8);
    expect(next.getDate()).toBe(7);
    expect(next.getTime()).toBeGreaterThan(monday.getTime());
  });

  it("is Monday from a Sunday, one day out", () => {
    const sunday = new Date(2026, 8, 6, 10, 0, 0, 0);
    const next = presetAt(sunday, "nextWeek");
    expect(next.getDay()).toBe(1);
    expect(next.getDate()).toBe(7);
  });
});

describe("snoozePresets — the offered set", () => {
  it("keeps menu order stable regardless of which options are withdrawn", () => {
    expect(ids(at(MONDAY, 9))).toEqual([
      "laterToday",
      "tomorrow",
      "thisWeekend",
      "nextWeek",
    ]);
    // Saturday night: neither later-today nor this-weekend survive.
    const saturdayNight = new Date(2026, 8, 5, 22, 0, 0, 0);
    expect(ids(saturdayNight)).toEqual(["tomorrow", "nextWeek"]);
  });

  it("never offers a wake time in the past, at any hour of any weekday", () => {
    for (let day = 0; day < 7; day += 1) {
      for (let hour = 0; hour < 24; hour += 1) {
        const now = new Date(2026, 8, 1 + day, hour, 17, 0, 0);
        for (const preset of snoozePresets(now)) {
          expect(preset.at.getTime()).toBeGreaterThan(now.getTime());
        }
      }
    }
  });
});

describe("toUntilString", () => {
  it("emits the server's exact UTCDate shape, without milliseconds", () => {
    const wire = toUntilString(new Date(Date.UTC(2026, 8, 1, 8, 0, 0, 123)));
    expect(wire).toBe("2026-09-01T08:00:00Z");
    // The shape `internal/jmap/mail/triage.go` re-serializes.
    expect(wire).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);
  });
});

describe("datetime-local round trip", () => {
  it("formats a local instant without shifting it by the UTC offset", () => {
    const local = new Date(2026, 8, 1, 8, 5, 0, 0);
    expect(datetimeLocalValue(local)).toBe("2026-09-01T08:05");
  });

  it("round-trips through the picker's value", () => {
    const now = new Date(2026, 7, 31, 9, 0, 0, 0);
    const chosen = new Date(2026, 8, 1, 8, 5, 0, 0);
    const parsed = parseDatetimeLocal(datetimeLocalValue(chosen), now);
    expect(parsed?.getTime()).toBe(chosen.getTime());
  });

  it("refuses a malformed value", () => {
    const now = new Date(2026, 7, 31, 9, 0, 0, 0);
    expect(parseDatetimeLocal("", now)).toBeUndefined();
    expect(parseDatetimeLocal("tomorrow", now)).toBeUndefined();
    expect(parseDatetimeLocal("2026-09-01", now)).toBeUndefined();
  });

  it("refuses the past and the present, as the server does", () => {
    const now = new Date(2026, 7, 31, 9, 0, 0, 0);
    expect(parseDatetimeLocal("2026-08-31T08:00", now)).toBeUndefined();
    expect(parseDatetimeLocal("2026-08-31T09:00", now)).toBeUndefined();
    expect(parseDatetimeLocal("2026-08-31T09:01", now)).toBeDefined();
  });
});
