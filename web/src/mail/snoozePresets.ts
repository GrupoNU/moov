/**
 * The snooze preset times (L3 E4, canon §2.2 — "Snooze").
 *
 * # These times are OURS, and the canon says so
 *
 * `docs/research/06-gmail-canon.md` §5 lists "snooze preset times" in the
 * UNSOURCED register: Gmail ships presets named "Later today", "Tomorrow",
 * "This weekend", "Next week", "Pick date & time", but Google publishes no
 * article stating the HOURS behind those names, and the canon's discipline is
 * that an unsourced number is not copied — it is decided, and the decision is
 * recorded where a reader will find it.
 *
 * So the labels are Gmail's (they are on the fetchable page) and the clock
 * times below are Moov's, chosen once, here, rather than scattered through a
 * menu component:
 *
 *   - **Later today** — now + 3 hours, rounded UP to the next half hour, and
 *     REFUSED when that lands at or after 23:00. A "later today" that wakes a
 *     message at 01:40 is not later today; Gmail hides the option late in the
 *     evening for the same reason, and hiding it is more honest than silently
 *     converting it into tomorrow.
 *   - **Tomorrow** — 08:00 the next calendar day. The morning hour is the same
 *     one every other preset uses, so "I'll see it in the morning" means one
 *     time of day, not three.
 *   - **This weekend** — Saturday 08:00. On a Saturday or a Sunday the weekend
 *     is already here, so the option is refused rather than silently meaning
 *     "next Saturday" — the label would be a lie for six days.
 *   - **Next week** — Monday 08:00, the NEXT Monday. On a Monday that is seven
 *     days out, never today: "next week" that fires in twenty minutes is the
 *     single worst possible reading of the phrase.
 *
 * # Why the whole thing is a pure function of `now`
 *
 * Every branch here is a calendar edge — Friday evening, Saturday, Sunday,
 * 22:59, midnight, a month boundary — and a calendar edge computed inside a
 * component is a calendar edge nobody tests. `now` is a parameter, so the
 * table in `snoozePresets.test.ts` enumerates them.
 *
 * # Local time, deliberately
 *
 * The wake times are built in the BROWSER's local timezone and converted to
 * UTC only at the wire (`toUntilString`). "Tomorrow at 8" means eight in the
 * morning where the user is, and computing it in UTC would wake a message at
 * 05:00 for a reader in Buenos Aires. The server accepts any instant
 * (`parseSnoozeUntil` in `internal/jmap/mail/triage.go` — "the presets are the
 * UI's concern"), which is exactly this division of labour.
 */

/** The preset identities. `custom` is the picker, not a computed time. */
export type SnoozePresetId =
  | "laterToday"
  | "tomorrow"
  | "thisWeekend"
  | "nextWeek";

/**
 * The i18n keys the presets carry, as a literal union.
 *
 * Typed rather than left as `string` so a renderer can pass one straight to
 * `t()` without a cast — and so renaming a key here is a compile error at the
 * string table rather than a blank menu row at runtime.
 */
export type SnoozePresetLabelKey =
  | "snooze.laterToday"
  | "snooze.tomorrow"
  | "snooze.thisWeekend"
  | "snooze.nextWeek";

/** One offered preset: what to call it and when it wakes. */
export interface SnoozePreset {
  readonly id: SnoozePresetId;
  /** The i18n key for the row's label. */
  readonly labelKey: SnoozePresetLabelKey;
  /** The wake instant, in local time. */
  readonly at: Date;
}

/** The morning hour every "next day" preset lands on. */
export const MORNING_HOUR = 8;

/** Later-today's offset, before rounding. */
const LATER_TODAY_HOURS = 3;

/**
 * The hour past which "later today" stops being offered.
 *
 * At 20:00 the +3h lands at 23:00, which is the first time that reads as
 * "tonight" rather than "later today" — so 23:00 is the exclusive ceiling and
 * a computed wake at or after it withdraws the option.
 */
export const LATER_TODAY_CEILING_HOUR = 23;

/** Rounds a Date UP to the next half hour, leaving an exact :00/:30 alone. */
function roundUpToHalfHour(date: Date): Date {
  const out = new Date(date.getTime());
  out.setSeconds(0, 0);
  const minutes = out.getMinutes();
  if (minutes === 0 || minutes === 30) return out;
  out.setMinutes(minutes < 30 ? 30 : 60);
  return out;
}

/** A Date at `hour`:00:00.000 local, `days` after `from`'s calendar date. */
function atHourDaysAhead(from: Date, days: number, hour: number): Date {
  const out = new Date(from.getFullYear(), from.getMonth(), from.getDate() + days, hour, 0, 0, 0);
  return out;
}

/**
 * Days from `from` forward to the next occurrence of `weekday`.
 *
 * Always at least 1: "the next Monday" asked on a Monday is seven days away,
 * never zero. That is the whole reason this is a named helper rather than a
 * modulo inline — the `|| 7` is the bug everybody writes.
 */
function daysUntilWeekday(from: Date, weekday: number): number {
  const delta = (weekday - from.getDay() + 7) % 7;
  return delta === 0 ? 7 : delta;
}

/**
 * The presets on offer at `now`, in menu order.
 *
 * A preset is OMITTED rather than disabled when its label would be untrue —
 * "later today" late at night, "this weekend" during the weekend. A greyed-out
 * row invites the user to wonder what would make it work; an absent row simply
 * is not offered, which is Gmail's own behaviour.
 */
export function snoozePresets(now: Date): readonly SnoozePreset[] {
  const out: SnoozePreset[] = [];

  const later = roundUpToHalfHour(
    new Date(now.getTime() + LATER_TODAY_HOURS * 60 * 60 * 1000),
  );
  /*
   * Two conditions, and both matter. The hour test withdraws the option late
   * in the evening; the DATE test catches the case the hour test cannot — a
   * +3h that has already rolled past midnight has an hour like 01:00, which
   * passes the ceiling but is emphatically not "later today".
   */
  const sameDay =
    later.getFullYear() === now.getFullYear() &&
    later.getMonth() === now.getMonth() &&
    later.getDate() === now.getDate();
  if (sameDay && later.getHours() < LATER_TODAY_CEILING_HOUR) {
    out.push({ id: "laterToday", labelKey: "snooze.laterToday", at: later });
  }

  out.push({
    id: "tomorrow",
    labelKey: "snooze.tomorrow",
    at: atHourDaysAhead(now, 1, MORNING_HOUR),
  });

  /*
   * Saturday is 6, Sunday is 0. The weekend option is offered Monday through
   * Friday only: on Saturday it would mean today (and possibly a time already
   * past), and on Sunday it would mean six days out under a label that says
   * "this".
   */
  const day = now.getDay();
  const inWeekend = day === 6 || day === 0;
  if (!inWeekend) {
    out.push({
      id: "thisWeekend",
      labelKey: "snooze.thisWeekend",
      at: atHourDaysAhead(now, daysUntilWeekday(now, 6), MORNING_HOUR),
    });
  }

  out.push({
    id: "nextWeek",
    labelKey: "snooze.nextWeek",
    at: atHourDaysAhead(now, daysUntilWeekday(now, 1), MORNING_HOUR),
  });

  return out;
}

/**
 * The wire value for a wake time: RFC 8620 §1.4's UTCDate.
 *
 * The server parses with `time.RFC3339` and re-serializes as
 * `2006-01-02T15:04:05Z`, so seconds-precision UTC with a literal `Z` is what
 * it round-trips. `toISOString` produces milliseconds, which parse fine but
 * come back stripped — sending the shape the server returns keeps a client-side
 * comparison of the two strings meaningful.
 */
export function toUntilString(at: Date): string {
  return `${at.toISOString().slice(0, 19)}Z`;
}

/**
 * The `min` attribute for the custom picker's `datetime-local` input.
 *
 * `datetime-local` reads and writes LOCAL time with no zone, so the bound has
 * to be built from the local field values rather than from `toISOString`, which
 * would offset it by the user's UTC offset — a bug that lets a user in UTC+2
 * pick a time two hours in the past and get the server's "until must be in the
 * future" refusal for a time their own screen showed as valid.
 */
export function datetimeLocalValue(at: Date): string {
  const pad = (n: number): string => String(n).padStart(2, "0");
  return (
    `${String(at.getFullYear()).padStart(4, "0")}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}` +
    `T${pad(at.getHours())}:${pad(at.getMinutes())}`
  );
}

/**
 * Reads a `datetime-local` value back into an instant, refusing the past.
 *
 * Returns `undefined` for anything unusable, which is what lets the caller keep
 * the menu open and say why instead of sending a value the server will refuse
 * with `invalidProperties` — the same refusal, one round trip earlier and in
 * the user's own language.
 */
export function parseDatetimeLocal(value: string, now: Date): Date | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(value.trim());
  if (match === null) return undefined;
  const [, year, month, day, hour, minute] = match;
  const at = new Date(
    Number(year),
    Number(month) - 1,
    Number(day),
    Number(hour),
    Number(minute),
    0,
    0,
  );
  if (Number.isNaN(at.getTime())) return undefined;
  // The server refuses a wake at or before `now`; refusing it here too means
  // the user learns immediately rather than after a round trip.
  if (at.getTime() <= now.getTime()) return undefined;
  return at;
}
