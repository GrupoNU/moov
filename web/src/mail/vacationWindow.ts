/**
 * The vacation form's decidable half (E6, canon §2.8).
 *
 * Validation, the local-midnight ↔ UTCDate mapping, and the banner's window
 * test live here rather than in the section component for the reason this
 * codebase keeps repeating: the RULES are decidable from values, the WIRING is
 * not, and a rule inside a component can only be tested by standing up a
 * dialog, a provider and a JMAP client.
 *
 * # The timezone decision, and whose it is
 *
 * `internal/jmap/mail/vacation.go` records the server's half explicitly:
 *
 * > Timezone: fromDate/toDate are §8 UTCDates, honored to the second in UTC by
 * > the generated Sieve guard. Gmail's "starts 12:00 AM, ends 11:59 PM" day
 * > boundaries are produced by the CLIENT sending day-aligned instants in the
 * > user's zone — this server keeps no per-account timezone and does not guess
 * > one.
 *
 * So the day-boundary semantics are OURS to produce, and this module is where
 * they are produced. {@link localDayStart} and {@link localDayEnd} turn a date
 * the user picked in a `<input type="date">` into the instants Gmail promises:
 * 00:00:00 and 23:59:59 **local**, serialized as the UTCDate the wire carries.
 * The user in UTC−3 who says "hasta el 14" gets a responder that stops at
 * 23:59:59 on the 14th where they live, not at 21:00.
 *
 * The consequence, worth naming because it is visible: the UTCDate stored is
 * NOT midnight-shaped, and a client in another zone reading the same object
 * sees an odd-looking hour. That is correct — the instant is the truth and the
 * calendar day was only ever an input convention.
 */

/** A date as an `<input type="date">` carries it: "2026-09-01". */
export type LocalDate = string;

/** True for a well-formed, real calendar date in the input's own format. */
export function isLocalDate(value: string): boolean {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const [year, month, day] = value.split("-").map(Number);
  if (year === undefined || month === undefined || day === undefined) return false;
  if (month < 1 || month > 12 || day < 1 || day > 31) return false;
  const probe = new Date(year, month - 1, day);
  return (
    probe.getFullYear() === year && probe.getMonth() === month - 1 && probe.getDate() === day
  );
}

/**
 * 00:00:00 local on that calendar day, as a UTCDate string.
 *
 * `new Date(y, m, d)` is the local-time constructor — the one that does the
 * zone conversion for us — as opposed to `new Date("2026-09-01")`, which ES
 * parses as UTC midnight and would silently shift the boundary by the offset.
 * That difference is the entire bug this function exists to not have.
 */
export function localDayStart(date: LocalDate): string | undefined {
  if (!isLocalDate(date)) return undefined;
  const [year, month, day] = date.split("-").map(Number);
  if (year === undefined || month === undefined || day === undefined) return undefined;
  return toUtcDate(new Date(year, month - 1, day, 0, 0, 0, 0));
}

/** 23:59:59 local on that calendar day — Gmail's own end boundary. */
export function localDayEnd(date: LocalDate): string | undefined {
  if (!isLocalDate(date)) return undefined;
  const [year, month, day] = date.split("-").map(Number);
  if (year === undefined || month === undefined || day === undefined) return undefined;
  return toUtcDate(new Date(year, month - 1, day, 23, 59, 59, 0));
}

/** The §8 UTCDate spelling the server parses and re-emits. */
export function toUtcDate(instant: Date): string {
  return `${instant.toISOString().slice(0, 19)}Z`;
}

/**
 * The calendar day a stored UTCDate falls on, in the reader's own zone.
 *
 * The inverse of {@link localDayStart} for form display. It is not an exact
 * inverse and cannot be: an instant near a day boundary in another zone lands
 * on the neighbouring date here, which is the honest answer to "what day is
 * this instant, where I am".
 */
export function utcDateToLocalDate(value: string | null): LocalDate {
  if (value === null || value === "") return "";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "";
  const year = String(parsed.getFullYear()).padStart(4, "0");
  const month = String(parsed.getMonth() + 1).padStart(2, "0");
  const day = String(parsed.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

/**
 * What is wrong with a vacation form, if anything.
 *
 * Every problem is reported, not the first — the same contract the server's own
 * `applyVacationPatch` keeps ("§5.3's list-them-all contract"), so the form and
 * the server never disagree about how many things are wrong.
 */
export type VacationProblem =
  /** `toDate` before `fromDate` — the server refuses this too. */
  | "endBeforeStart"
  /** Enabled with neither subject nor body — §8's subject-or-body rule. */
  | "emptyMessage"
  /** A date field that is not a date. */
  | "invalidDate"
  /** The server refuses a multi-line subject (`applyVacationPatch`). */
  | "multilineSubject";

/** The form's editable state, in the shapes the inputs actually hold. */
export interface VacationDraft {
  readonly isEnabled: boolean;
  /** "" means "no bound", which the wire spells as null. */
  readonly fromDate: LocalDate;
  readonly toDate: LocalDate;
  readonly subject: string;
  readonly textBody: string;
}

/**
 * Every problem with a draft.
 *
 * Note what is NOT a problem: an empty subject with a non-empty body, or the
 * reverse. §8 requires only that they are not ALL empty, and Dovecot supplies
 * its own subject ("Auto: <original>") when none is given — so demanding one
 * would be stricter than both the RFC and the mail server.
 */
export function validateVacation(draft: VacationDraft): readonly VacationProblem[] {
  const problems: VacationProblem[] = [];

  const fromOk = draft.fromDate === "" || isLocalDate(draft.fromDate);
  const toOk = draft.toDate === "" || isLocalDate(draft.toDate);
  if (!fromOk || !toOk) problems.push("invalidDate");

  if (fromOk && toOk && draft.fromDate !== "" && draft.toDate !== "") {
    const start = localDayStart(draft.fromDate);
    const end = localDayEnd(draft.toDate);
    if (start !== undefined && end !== undefined && end < start) {
      problems.push("endBeforeStart");
    }
  }

  if (draft.isEnabled && draft.subject.trim() === "" && draft.textBody.trim() === "") {
    problems.push("emptyMessage");
  }

  if (/[\r\n]/.test(draft.subject)) problems.push("multilineSubject");

  return problems;
}

// ---------------------------------------------------------------------------
// the inbox banner's window test
// ---------------------------------------------------------------------------

/**
 * Whether the "estás en vacaciones" banner belongs on screen right now.
 *
 * Gmail shows a banner across the top of the inbox WHILE the responder is
 * active, with an "End now" button (canon §2.8). "While active" is two
 * conditions, and the second is the one a naive implementation drops:
 *
 *   1. `isEnabled` — the user turned it on;
 *   2. now is INSIDE the window — a responder enabled with a range starting
 *      next Monday is not responding today, and a banner claiming otherwise
 *      would be the UI lying about the mail server's behaviour. The generated
 *      Sieve guards on exactly these bounds, so the banner tracks what Dovecot
 *      will actually do.
 *
 * An absent bound is an OPEN bound, matching the generator (a zero time means
 * no guard is emitted for that side).
 */
export function isVacationActive(
  vacation: {
    readonly isEnabled: boolean;
    readonly fromDate: string | null;
    readonly toDate: string | null;
  },
  now: Date,
): boolean {
  if (!vacation.isEnabled) return false;
  const instant = now.getTime();

  if (vacation.fromDate !== null && vacation.fromDate !== "") {
    const start = new Date(vacation.fromDate).getTime();
    // An UNPARSEABLE bound is treated as no bound rather than as a reason to
    // hide the banner: the responder is enabled, so the honest default is to
    // say so. Hiding it would leave a user auto-replying with no indication.
    if (!Number.isNaN(start) && instant < start) return false;
  }
  if (vacation.toDate !== null && vacation.toDate !== "") {
    const end = new Date(vacation.toDate).getTime();
    if (!Number.isNaN(end) && instant > end) return false;
  }
  return true;
}
