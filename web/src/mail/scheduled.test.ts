import { describe, expect, it, vi } from "vitest";

import { CAP_SUBMISSION, JmapClient } from "../api/jmap";
import {
  AFTERNOON_HOUR,
  SCHEDULE_MORNING_HOUR,
  fetchScheduled,
  isScheduled,
  schedulePresets,
  withinDelayHorizon,
  type SchedulePresetId,
} from "./scheduled";
import { sendDraft, sendScheduledNow, type DraftSpec } from "./write";

/**
 * The Scheduled view's contract (L3 E4, canon §2.3).
 *
 * Two things are pinned here and they are the two that can silently break:
 * the FILTER sent to `EmailSubmission/query` (the server answers exactly three
 * conditions and refuses the rest with `unsupportedFilter`), and the rule that
 * separates a scheduled send from an ordinary one sitting in its undo window —
 * without which the view lists the message the user pressed Send on four
 * seconds ago.
 */

const ACCOUNT = "a";

function stub(responses: Record<string, unknown>) {
  const sent: [string, Record<string, unknown>, string][] = [];
  const usingLog: string[][] = [];
  const client = new JmapClient({ username: "u", password: "p" });
  vi.spyOn(client, "call").mockImplementation((invocations, using) => {
    const calls = invocations as [string, Record<string, unknown>, string][];
    sent.push(...calls);
    usingLog.push([...(using ?? [])]);
    return Promise.resolve({
      methodResponses: calls.map(([name, , id]) => [name, responses[id] ?? {}, id]),
    } as never);
  });
  return { client, sent, usingLog };
}

const NOW = Date.UTC(2026, 8, 1, 10, 0, 0);
const UNDO_SECONDS = 10;

function iso(msFromNow: number): string {
  return `${new Date(NOW + msFromNow).toISOString().slice(0, 19)}Z`;
}

describe("isScheduled", () => {
  it("is false for an ordinary send inside its undo window", () => {
    expect(isScheduled(iso(8_000), NOW, UNDO_SECONDS)).toBe(false);
  });

  it("is false just past the window, inside the clock-skew margin", () => {
    // The two clocks are not the same one: sendAt is the server's, `now` is the
    // browser's. Without the margin a send whose window has 200 ms left would
    // flicker into a view the user did not ask for.
    expect(isScheduled(iso(12_000), NOW, UNDO_SECONDS)).toBe(false);
  });

  it("is true for a send hours out", () => {
    expect(isScheduled(iso(3 * 60 * 60 * 1000), NOW, UNDO_SECONDS)).toBe(true);
  });

  it("is false for an unparseable timestamp rather than throwing", () => {
    expect(isScheduled("soon", NOW, UNDO_SECONDS)).toBe(false);
  });

  it("moves with the account's undo window, which is a preference", () => {
    const at = iso(25_000);
    expect(isScheduled(at, NOW, 10)).toBe(true);
    expect(isScheduled(at, NOW, 30)).toBe(false);
  });
});

describe("fetchScheduled", () => {
  const RESPONSES = {
    q: { ids: ["s1", "s2"] },
    s: {
      list: [
        { id: "s1", emailId: "e1", sendAt: iso(4 * 60 * 60 * 1000), undoStatus: "pending" },
        { id: "s2", emailId: "e2", sendAt: iso(5_000), undoStatus: "pending" },
      ],
    },
    e: {
      list: [
        { id: "e1", subject: "Viernes", to: [{ name: "Ana", email: "ana@x.com" }] },
        { id: "e2", subject: "Ya sale", to: [{ name: null, email: "b@x.com" }] },
      ],
    },
  };

  it("filters on undoStatus ONLY — the server refuses the id conditions", async () => {
    const { client, sent } = stub(RESPONSES);
    await fetchScheduled(client, ACCOUNT, { now: NOW, undoWindowSeconds: UNDO_SECONDS });
    const query = sent.find(([name]) => name === "EmailSubmission/query");
    expect(query?.[1].filter).toEqual({ undoStatus: "pending" });
    // identityIds / emailIds / threadIds would come back unsupportedFilter.
    expect(query?.[1].filter).not.toHaveProperty("emailIds");
  });

  it("sorts by sentAt ascending — soonest first, not the server's default", async () => {
    const { client, sent } = stub(RESPONSES);
    await fetchScheduled(client, ACCOUNT, { now: NOW, undoWindowSeconds: UNDO_SECONDS });
    const query = sent.find(([name]) => name === "EmailSubmission/query");
    expect(query?.[1].sort).toEqual([{ property: "sentAt", isAscending: true }]);
  });

  it("joins query → get → Email/get in ONE request, by back-reference", async () => {
    const { client, sent, usingLog } = stub(RESPONSES);
    await fetchScheduled(client, ACCOUNT, { now: NOW, undoWindowSeconds: UNDO_SECONDS });
    expect(sent.map(([name]) => name)).toEqual([
      "EmailSubmission/query",
      "EmailSubmission/get",
      "Email/get",
    ]);
    expect(sent[2]?.[1]["#ids"]).toEqual({
      resultOf: "s",
      name: "EmailSubmission/get",
      path: "/list/*/emailId",
    });
    expect(usingLog[0]).toContain(CAP_SUBMISSION);
  });

  it("EXCLUDES an ordinary send still in its undo window", async () => {
    const { client } = stub(RESPONSES);
    const rows = await fetchScheduled(client, ACCOUNT, {
      now: NOW,
      undoWindowSeconds: UNDO_SECONDS,
    });
    expect(rows.map((row) => row.id)).toEqual(["s1"]);
  });

  it("carries subject and recipients from the joined draft", async () => {
    const { client } = stub(RESPONSES);
    const [row] = await fetchScheduled(client, ACCOUNT, {
      now: NOW,
      undoWindowSeconds: UNDO_SECONDS,
    });
    expect(row?.subject).toBe("Viernes");
    expect(row?.recipients).toEqual(["Ana <ana@x.com>"]);
    expect(row?.emailId).toBe("e1");
  });

  it("keeps the row when the draft could not be fetched — degraded, not broken", async () => {
    const { client } = stub({ ...RESPONSES, e: { list: [] } });
    const [row] = await fetchScheduled(client, ACCOUNT, {
      now: NOW,
      undoWindowSeconds: UNDO_SECONDS,
    });
    // It still says WHEN and can still be canceled, which is what matters.
    expect(row?.id).toBe("s1");
    expect(row?.subject).toBe("");
    expect(row?.recipients).toEqual([]);
  });
});

describe("sendDraft with a sendAt", () => {
  const SPEC: DraftSpec = {
    mailboxId: "drafts",
    from: [{ name: null, email: "me@x.com" }],
    to: [{ name: null, email: "you@x.com" }],
    cc: [],
    bcc: [],
    subject: "hola",
    text: "hola",
    attachments: [],
  };

  it("puts sendAt on the submission create", async () => {
    const { client, sent } = stub({
      c: { created: { draft: { id: "e1" } } },
      s: { created: { sendIt: { id: "s1", undoStatus: "pending", sendAt: "2026-09-04T08:00:00Z" } } },
    });
    await sendDraft(client, ACCOUNT, SPEC, {
      identityId: "primary",
      sentMailboxId: "sent",
      sendAt: "2026-09-04T08:00:00Z",
    });
    const submission = sent.find(([name]) => name === "EmailSubmission/set");
    const create = submission?.[1].create as Record<string, Record<string, unknown>>;
    expect(create.sendIt?.sendAt).toBe("2026-09-04T08:00:00Z");
  });

  it("OMITS onSuccessUpdateEmail for a scheduled send", async () => {
    // The server suppresses it anyway (holdsItsDraft): a message scheduled for
    // Friday must not sit in Sent from Tuesday, and must stay a draft the user
    // can still edit. Sending an instruction the server must ignore would state
    // an intent this client does not have.
    const { client, sent } = stub({
      c: { created: { draft: { id: "e1" } } },
      s: { created: { sendIt: { id: "s1", undoStatus: "pending", sendAt: "2026-09-04T08:00:00Z" } } },
    });
    await sendDraft(client, ACCOUNT, SPEC, {
      identityId: "primary",
      sentMailboxId: "sent",
      sendAt: "2026-09-04T08:00:00Z",
    });
    const submission = sent.find(([name]) => name === "EmailSubmission/set");
    expect(submission?.[1]).not.toHaveProperty("onSuccessUpdateEmail");
  });

  it("KEEPS onSuccessUpdateEmail for an ordinary send", async () => {
    const { client, sent } = stub({
      c: { created: { draft: { id: "e1" } } },
      s: { created: { sendIt: { id: "s1", undoStatus: "pending", sendAt: "" } } },
    });
    await sendDraft(client, ACCOUNT, SPEC, { identityId: "primary", sentMailboxId: "sent" });
    const submission = sent.find(([name]) => name === "EmailSubmission/set");
    expect(submission?.[1]).toHaveProperty("onSuccessUpdateEmail");
  });

  it("surfaces the cap-100 overQuota refusal as a per-record error", async () => {
    const { client } = stub({
      c: { created: { draft: { id: "e1" } } },
      s: {
        notCreated: {
          sendIt: {
            type: "overQuota",
            description:
              "this account already has 100 scheduled sends; the limit is 100 " +
              "(the same ceiling Gmail applies). Cancel one to schedule another.",
          },
        },
      },
    });
    const result = await sendDraft(client, ACCOUNT, SPEC, {
      identityId: "primary",
      sentMailboxId: "sent",
      sendAt: "2026-09-04T08:00:00Z",
    });
    expect(result.submission).toBeUndefined();
    expect(result.outcome.failed.sendIt?.type).toBe("overQuota");
    expect(result.outcome.failed.sendIt?.description).toContain("Cancel one");
  });
});

describe("sendScheduledNow", () => {
  it("cancels FIRST, then resubmits in a SECOND request", async () => {
    // Not one batch: §3.2 processes calls sequentially, so a failed cancel
    // followed by a create in the same request would send the message twice.
    const { client, sent } = stub({
      s: { updated: { s1: null }, created: { sendNow: { id: "s2" } } },
    });
    const result = await sendScheduledNow(client, ACCOUNT, "s1", "e1", {
      identityId: "primary",
      sentMailboxId: "sent",
    });
    expect(sent).toHaveLength(2);
    expect(sent[0]?.[1].update).toEqual({ s1: { undoStatus: "canceled" } });
    const create = sent[1]?.[1].create as Record<string, Record<string, unknown>>;
    expect(create.sendNow).toEqual({ identityId: "primary", emailId: "e1" });
    // The resubmission has NO sendAt: it is an ordinary immediate send.
    expect(create.sendNow).not.toHaveProperty("sendAt");
    expect(result.resubmitted).toBeDefined();
  });

  it("files the resubmission into Sent, unlike the scheduled create", async () => {
    const { client, sent } = stub({ s: { updated: { s1: null }, created: { sendNow: { id: "s2" } } } });
    await sendScheduledNow(client, ACCOUNT, "s1", "e1", {
      identityId: "primary",
      sentMailboxId: "sent",
    });
    expect(sent[1]?.[1].onSuccessUpdateEmail).toEqual({
      "#sendNow": { mailboxIds: { sent: true }, "keywords/$draft": null },
    });
  });

  it("does NOT resubmit when the cancel was refused — the mail is already going", async () => {
    const { client, sent } = stub({
      s: { notUpdated: { s1: { type: "cannotUnsend", description: "already transmitting" } } },
    });
    const result = await sendScheduledNow(client, ACCOUNT, "s1", "e1", {
      identityId: "primary",
      sentMailboxId: "sent",
    });
    expect(sent).toHaveLength(1);
    expect(result.resubmitted).toBeUndefined();
    expect(result.canceled.failed.s1?.type).toBe("cannotUnsend");
  });
});

describe("schedulePresets", () => {
  function ids(now: Date): readonly SchedulePresetId[] {
    return schedulePresets(now).map((preset) => preset.id);
  }

  it("offers this afternoon only while it is still ahead", () => {
    const morning = new Date(2026, 8, 1, 9, 0, 0, 0);
    const evening = new Date(2026, 8, 1, 17, 0, 0, 0);
    expect(ids(morning)).toContain("thisAfternoon");
    expect(ids(evening)).not.toContain("thisAfternoon");
  });

  it("uses the same clock times the snooze menu does", () => {
    const morning = new Date(2026, 8, 1, 9, 0, 0, 0);
    const presets = schedulePresets(morning);
    expect(presets.find((p) => p.id === "thisAfternoon")?.at.getHours()).toBe(AFTERNOON_HOUR);
    expect(presets.find((p) => p.id === "tomorrowMorning")?.at.getHours()).toBe(
      SCHEDULE_MORNING_HOUR,
    );
  });

  it("puts Monday seven days out when asked on a Monday", () => {
    const monday = new Date(2026, 7, 31, 9, 0, 0, 0);
    expect(monday.getDay()).toBe(1);
    const next = schedulePresets(monday).find((p) => p.id === "mondayMorning");
    expect(next?.at.getDay()).toBe(1);
    expect(next?.at.getDate()).toBe(7);
    expect(next?.at.getMonth()).toBe(8);
  });

  it("never offers an instant in the past", () => {
    for (let hour = 0; hour < 24; hour += 1) {
      const now = new Date(2026, 8, 1, hour, 30, 0, 0);
      for (const preset of schedulePresets(now)) {
        expect(preset.at.getTime()).toBeGreaterThan(now.getTime());
      }
    }
  });
});

describe("withinDelayHorizon", () => {
  const THIRTY_DAYS = 30 * 24 * 60 * 60;

  it("accepts a date inside the advertised maxDelayedSend", () => {
    expect(withinDelayHorizon(new Date(NOW + 29 * 24 * 3_600_000), NOW, THIRTY_DAYS)).toBe(true);
  });

  it("refuses one beyond it — the same refusal the server would give", () => {
    expect(withinDelayHorizon(new Date(NOW + 31 * 24 * 3_600_000), NOW, THIRTY_DAYS)).toBe(false);
  });

  it("refuses the past", () => {
    expect(withinDelayHorizon(new Date(NOW - 1000), NOW, THIRTY_DAYS)).toBe(false);
  });
});
