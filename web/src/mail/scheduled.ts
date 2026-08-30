/**
 * Scheduled sends — the "Programados" view (L3 E4, canon §2.3).
 *
 * # The shape this consumes, read off the server
 *
 * `internal/jmap/mail/submission_query.go` is explicit about why there is no
 * Scheduled FOLDER: "a scheduled message must stay a DRAFT (canon: 'cancel
 * reverts to draft'), and a draft lives in Drafts. Moving it to a second folder
 * would make every other IMAP client show it outside Drafts, where their own
 * compose flows cannot reach it — the mirror image of why snooze DOES move."
 *
 * So the view is `EmailSubmission/query` filtered on `undoStatus`, and the
 * server answers exactly three FilterConditions — `undoStatus`, `before`,
 * `after` — refusing `identityIds`, `emailIds` and `threadIds` with
 * `unsupportedFilter`. This module sends only the three, so it never meets that
 * refusal. Its sort is `sentAt` only (also enforced server-side), and the server
 * defaults to DESCENDING when none is given; {@link fetchScheduled} sends the
 * comparator explicitly anyway, ASCENDING, because a "what goes out next" list
 * reads soonest-first while the server's own default was chosen for a
 * recent-sends list.
 *
 * # Why the rows need TWO calls and cannot use a back-reference
 *
 * A submission carries `emailId`, and a row needs the message's subject and
 * recipients. RFC 8620 §3.7's back-reference resolves a path INTO an earlier
 * call's response, so `EmailSubmission/query` → `EmailSubmission/get` chains
 * fine — but `Email/get` needs `/list/*&#47;emailId` out of the GET, which is a
 * third call. All three ride ONE request, joined by two back-references, so the
 * view still costs a single round trip.
 *
 * # The undo window is NOT a scheduled send
 *
 * Every ordinary send is `pending` for the length of the undo window, so a
 * naive `undoStatus:"pending"` query lists the message the user pressed Send on
 * four seconds ago next to one scheduled for Friday. {@link isScheduled}
 * separates them by the only fact that distinguishes them: how far out `sendAt`
 * is. The threshold is the undo window itself, which is a preference — so it is
 * a parameter here rather than a constant.
 */

import { CAP_CORE, CAP_MAIL, CAP_SUBMISSION, backRef, type JmapClient, type JmapInvocation } from "../api/jmap";
import { MailApiError, isMethodError } from "./api";
import type { EmailAddress } from "./types";

/** The `using` array the submission calls send. */
const SUBMISSION_CAPS = [CAP_CORE, CAP_MAIL, CAP_SUBMISSION];

/** One row of the Scheduled view. */
export interface ScheduledSend {
  /** The EmailSubmission id — what a cancel names. */
  readonly id: string;
  /** The draft's Email id, so "send now" can resubmit it. */
  readonly emailId: string;
  /** The instant the server will transmit (UTCDate). */
  readonly sendAt: string;
  readonly subject: string;
  readonly recipients: readonly string[];
}

/**
 * How far ahead a `sendAt` must be to count as SCHEDULED rather than as an
 * ordinary send sitting in its undo window.
 *
 * A margin is added to the window because the two clocks differ: `sendAt` is
 * the server's, `now` is the browser's, and the round trip is real. Without it
 * a send whose window has 200 ms left could momentarily be listed as scheduled
 * in a view the user did not ask for.
 */
const SCHEDULE_MARGIN_MS = 5_000;

/**
 * True when a pending submission is a SCHEDULED send rather than an undoable
 * one.
 *
 * `undoWindowSeconds` is the account's setting (E5's `undoSendSeconds`), which
 * the server uses to compute `sendAt` for an ordinary send.
 */
export function isScheduled(
  sendAt: string,
  now: number,
  undoWindowSeconds: number,
): boolean {
  const at = Date.parse(sendAt);
  if (Number.isNaN(at)) return false;
  return at - now > undoWindowSeconds * 1000 + SCHEDULE_MARGIN_MS;
}

function responseFor(
  responses: readonly JmapInvocation[],
  clientId: string,
): Record<string, unknown> {
  for (const [name, args, id] of responses) {
    if (id !== clientId) continue;
    if (name === "error") {
      const err = isMethodError(args) ? args : undefined;
      throw new MailApiError(`JMAP method error: ${err?.type ?? "unknown"}`, err);
    }
    return args;
  }
  throw new MailApiError(`no response for call "${clientId}"`);
}

/** Flattens an Email's To/Cc into display addresses. */
function recipientsOf(email: Record<string, unknown> | undefined): readonly string[] {
  if (email === undefined) return [];
  const out: string[] = [];
  for (const field of ["to", "cc"] as const) {
    const list = email[field];
    if (!Array.isArray(list)) continue;
    for (const entry of list as readonly EmailAddress[]) {
      const address = entry.email;
      if (typeof address === "string" && address !== "") {
        out.push(entry.name !== null && entry.name !== undefined && entry.name !== ""
          ? `${entry.name} <${address}>`
          : address);
      }
    }
  }
  return out;
}

/**
 * The account's scheduled sends, with their drafts' subjects and recipients.
 *
 * Returns them soonest-first. Ordinary sends inside their undo window are
 * FILTERED OUT here rather than by the server: the server cannot know the
 * account's undo window is the dividing line (it is a Moov preference, not a
 * JMAP concept), and `EmailSubmission/query`'s `after` filter would express the
 * wrong thing — a cutoff computed from the client's clock, sent to a server
 * whose clock is the authority on `sendAt`.
 */
export async function fetchScheduled(
  client: JmapClient,
  accountId: string,
  options: {
    readonly now: number;
    readonly undoWindowSeconds: number;
    readonly signal?: AbortSignal;
  },
): Promise<readonly ScheduledSend[]> {
  const { now, undoWindowSeconds, signal } = options;

  const response = await client.call(
    [
      [
        "EmailSubmission/query",
        {
          accountId,
          // The one FilterCondition this view needs, and one the server
          // answers exactly (§7.3).
          filter: { undoStatus: "pending" },
          // Soonest first: this is a "what goes out next" list.
          sort: [{ property: "sentAt", isAscending: true }],
          calculateTotal: true,
        },
        "q",
      ],
      [
        "EmailSubmission/get",
        {
          accountId,
          ...backRef("ids", { resultOf: "q", name: "EmailSubmission/query", path: "/ids" }),
        },
        "s",
      ],
      [
        "Email/get",
        {
          accountId,
          ...backRef("ids", {
            resultOf: "s",
            name: "EmailSubmission/get",
            path: "/list/*/emailId",
          }),
          properties: ["id", "subject", "to", "cc"],
        },
        "e",
      ],
    ],
    SUBMISSION_CAPS,
    signal,
  );

  const responses = response.methodResponses;
  const submissions = (responseFor(responses, "s").list ?? []) as readonly Record<string, unknown>[];

  /*
   * The drafts are a NICETY, exactly as `Thread/get` is on a list: a scheduled
   * row without its subject is degraded (it still says WHEN and can still be
   * canceled), where a whole view that fails because one draft was destroyed
   * out from under it is broken.
   */
  let emails: readonly Record<string, unknown>[] = [];
  try {
    emails = (responseFor(responses, "e").list ?? []) as readonly Record<string, unknown>[];
  } catch {
    emails = [];
  }
  const emailById = new Map<string, Record<string, unknown>>();
  for (const email of emails) {
    if (typeof email.id === "string") emailById.set(email.id, email);
  }

  const out: ScheduledSend[] = [];
  for (const submission of submissions) {
    const id = submission.id;
    const emailId = submission.emailId;
    const sendAt = submission.sendAt;
    if (typeof id !== "string" || typeof sendAt !== "string") continue;
    if (!isScheduled(sendAt, now, undoWindowSeconds)) continue;
    const email = typeof emailId === "string" ? emailById.get(emailId) : undefined;
    out.push({
      id,
      emailId: typeof emailId === "string" ? emailId : "",
      sendAt,
      subject: typeof email?.subject === "string" ? email.subject : "",
      recipients: recipientsOf(email),
    });
  }
  // The server sorts by sentAt, but the join above cannot re-order and a
  // defensive sort here costs nothing on a list the canon caps at 100.
  return out.sort((a, b) => a.sendAt.localeCompare(b.sendAt));
}

// ---------------------------------------------------------------------------
// the schedule-send presets
// ---------------------------------------------------------------------------

export type SchedulePresetId = "thisAfternoon" | "tomorrowMorning" | "mondayMorning";

export interface SchedulePreset {
  readonly id: SchedulePresetId;
  readonly labelKey: string;
  readonly at: Date;
}

/** The afternoon hour "this afternoon" means. */
export const AFTERNOON_HOUR = 13;
/** The morning hour every "next day" schedule preset lands on. */
export const SCHEDULE_MORNING_HOUR = 8;

/**
 * The schedule-send presets at `now`, in menu order.
 *
 * Gmail offers the same three shapes plus a picker, and — exactly as with the
 * snooze presets — the NAMES are Gmail's while the HOURS are ours: canon §5
 * records no published article giving Gmail's own times. 13:00 and 08:00 are
 * the same two clock times the snooze menu uses, so "the morning" means one
 * hour across the whole app rather than two.
 *
 * A preset is withdrawn once it is in the past, for the same reason the snooze
 * ones are: an option that would immediately fail the server's "sendAt must be
 * in the future" is worse than an option that is not offered.
 */
export function schedulePresets(now: Date): readonly SchedulePreset[] {
  const out: SchedulePreset[] = [];

  const afternoon = new Date(
    now.getFullYear(), now.getMonth(), now.getDate(), AFTERNOON_HOUR, 0, 0, 0,
  );
  if (afternoon.getTime() > now.getTime()) {
    out.push({ id: "thisAfternoon", labelKey: "schedule.thisAfternoon", at: afternoon });
  }

  out.push({
    id: "tomorrowMorning",
    labelKey: "schedule.tomorrowMorning",
    at: new Date(
      now.getFullYear(), now.getMonth(), now.getDate() + 1, SCHEDULE_MORNING_HOUR, 0, 0, 0,
    ),
  });

  const daysToMonday = ((1 - now.getDay() + 7) % 7) === 0 ? 7 : (1 - now.getDay() + 7) % 7;
  out.push({
    id: "mondayMorning",
    labelKey: "schedule.mondayMorning",
    at: new Date(
      now.getFullYear(), now.getMonth(), now.getDate() + daysToMonday,
      SCHEDULE_MORNING_HOUR, 0, 0, 0,
    ),
  });

  return out;
}

/**
 * True when an instant is inside the server's advertised `maxDelayedSend`.
 *
 * Checked client-side so the picker can refuse a date the server would refuse,
 * in the user's own language and without a round trip. The limit is READ from
 * the session (`scheduleLimits`), never assumed — "declared == applied" is the
 * server's J1 rule and this is its client half.
 */
export function withinDelayHorizon(
  at: Date,
  now: number,
  maxDelayedSendSeconds: number,
): boolean {
  const delta = at.getTime() - now;
  return delta > 0 && delta <= maxDelayedSendSeconds * 1000;
}
