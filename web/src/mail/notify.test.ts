import { describe, expect, it } from "vitest";

import {
  EMPTY_ARRIVAL_STATE,
  mailboxNotifiable,
  newArrivals,
  NOTIFICATION_ICON,
  notificationContent,
  receivedAtMillis,
  shouldNotify,
  type ArrivalState,
  type NotifyContext,
} from "./notify";
import type { Email } from "./types";

/**
 * The two decisions behind a desktop notification.
 *
 * Both are pure, and both have a failure mode that is loud in exactly the wrong
 * way: a detector that is too eager announces the whole inbox on sign-in, and a
 * predicate that is too permissive toasts a user who is staring at the message
 * already. So the tests below are written mostly as "does NOT fire" cases.
 */

function mail(id: string, receivedAt: string, extra: Partial<Email> = {}): Email {
  return { id, receivedAt, ...extra };
}

const T0 = "2026-08-30T10:00:00Z";
const T1 = "2026-08-30T10:05:00Z";
const T2 = "2026-08-30T10:10:00Z";

describe("newArrivals", () => {
  it("announces nothing on the first observation, and seeds", () => {
    // Sign-in: the whole inbox is "unseen" and none of it is news.
    const result = newArrivals([mail("a", T1), mail("b", T0)], EMPTY_ARRIVAL_STATE);

    expect(result.arrivals).toEqual([]);
    expect(result.state.seeded).toBe(true);
    expect(result.state.ids).toEqual(new Set(["a", "b"]));
    expect(result.state.newestReceivedAt).toBe(Date.parse(T1));
  });

  it("announces a message that is both unseen and newer", () => {
    const seeded = newArrivals([mail("a", T0)], EMPTY_ARRIVAL_STATE).state;

    const result = newArrivals([mail("b", T1), mail("a", T0)], seeded);

    expect(result.arrivals.map((email) => email.id)).toEqual(["b"]);
  });

  it("announces nothing when the same window comes back", () => {
    // The overwhelmingly common refresh: a flag changed somewhere, so the
    // array is new and the mail is not.
    const seeded = newArrivals([mail("a", T1), mail("b", T0)], EMPTY_ARRIVAL_STATE).state;

    const result = newArrivals([mail("a", T1), mail("b", T0)], seeded);

    expect(result.arrivals).toEqual([]);
  });

  it("does NOT announce older mail that merely entered the window", () => {
    /*
     * The case an id-set alone gets wrong, and the reason the timestamp
     * condition exists: paginating (or a window that grows) surfaces messages
     * this client never fetched. They are new to us and old to the mailbox.
     */
    const seeded = newArrivals([mail("a", T2)], EMPTY_ARRIVAL_STATE).state;

    const result = newArrivals([mail("a", T2), mail("old", T0)], seeded);

    expect(result.arrivals).toEqual([]);
  });

  it("does not announce a message sharing the previous high-water mark", () => {
    // Errs toward silence: equal timestamps are treated as already-seen.
    const seeded = newArrivals([mail("a", T1)], EMPTY_ARRIVAL_STATE).state;

    const result = newArrivals([mail("b", T1), mail("a", T1)], seeded);

    expect(result.arrivals).toEqual([]);
  });

  it("returns several arrivals newest first", () => {
    const seeded = newArrivals([mail("a", T0)], EMPTY_ARRIVAL_STATE).state;

    const result = newArrivals([mail("c", T1), mail("d", T2), mail("a", T0)], seeded);

    expect(result.arrivals.map((email) => email.id)).toEqual(["d", "c"]);
  });

  it("advances the high-water mark so the same mail is announced once", () => {
    const first = newArrivals([mail("a", T0)], EMPTY_ARRIVAL_STATE).state;
    const second = newArrivals([mail("b", T1), mail("a", T0)], first);
    expect(second.arrivals.map((email) => email.id)).toEqual(["b"]);

    // A second refresh with the same content must be silent.
    const third = newArrivals([mail("b", T1), mail("a", T0)], second.state);
    expect(third.arrivals).toEqual([]);
  });

  it("treats a missing or unparseable receivedAt as the epoch", () => {
    expect(receivedAtMillis({ id: "x" })).toBe(0);
    expect(receivedAtMillis({ id: "x", receivedAt: "not a date" })).toBe(0);

    const seeded = newArrivals([mail("a", T1)], EMPTY_ARRIVAL_STATE).state;
    // 0 is not > the high-water mark, so it cannot announce.
    expect(newArrivals([{ id: "b" }, mail("a", T1)], seeded).arrivals).toEqual([]);
  });

  it("survives an empty window without losing what it knew", () => {
    const seeded = newArrivals([mail("a", T1)], EMPTY_ARRIVAL_STATE).state;
    const result = newArrivals([], seeded);

    expect(result.arrivals).toEqual([]);
    // The high-water mark must NOT reset, or the next refresh re-announces `a`.
    expect(result.state.newestReceivedAt).toBe(Date.parse(T1));
  });
});

describe("shouldNotify", () => {
  const base: NotifyContext = {
    mode: "new",
    permission: "granted",
    attention: { hasFocus: false, isVisible: false },
    mailboxNotifiable: true,
  };

  it("fires when the preference is on, permission granted, and the tab is away", () => {
    expect(shouldNotify(base)).toBe(true);
  });

  it("does not fire when the preference is off", () => {
    expect(shouldNotify({ ...base, mode: "off" })).toBe(false);
  });

  it("does not fire without a granted permission", () => {
    // `default` is not a maybe: firing without a grant does nothing anyway.
    expect(shouldNotify({ ...base, permission: "default" })).toBe(false);
    expect(shouldNotify({ ...base, permission: "denied" })).toBe(false);
    expect(shouldNotify({ ...base, permission: undefined })).toBe(false);
  });

  it("does not fire while the user is looking at the app", () => {
    // The mail appearing in the list IS the notification.
    expect(
      shouldNotify({ ...base, attention: { hasFocus: true, isVisible: true } }),
    ).toBe(false);
  });

  it("fires for a visible but UNFOCUSED window", () => {
    // A second monitor, or the app behind an editor: the list is on screen and
    // the user is not looking at it.
    expect(
      shouldNotify({ ...base, attention: { hasFocus: false, isVisible: true } }),
    ).toBe(true);
  });

  it("fires for a hidden tab", () => {
    expect(
      shouldNotify({ ...base, attention: { hasFocus: true, isVisible: false } }),
    ).toBe(true);
  });

  it("does not fire for a mailbox that must never notify", () => {
    expect(shouldNotify({ ...base, mailboxNotifiable: false })).toBe(false);
  });
});

describe("mailboxNotifiable", () => {
  it("excludes junk, trash, sent and drafts", () => {
    expect(mailboxNotifiable("junk")).toBe(false);
    expect(mailboxNotifiable("trash")).toBe(false);
    // The subtle one: our own SSE fires when a message we just sent lands in
    // Sent, which would notify the sender about their own mail.
    expect(mailboxNotifiable("sent")).toBe(false);
    // Autosave writes to Drafts constantly.
    expect(mailboxNotifiable("drafts")).toBe(false);
  });

  it("includes the inbox, archive and user folders", () => {
    expect(mailboxNotifiable("inbox")).toBe(true);
    expect(mailboxNotifiable("archive")).toBe(true);
    // A Sieve rule filing into "Clientes" is still mail arriving.
    expect(mailboxNotifiable(null)).toBe(true);
    expect(mailboxNotifiable(undefined)).toBe(true);
  });
});

describe("notificationContent", () => {
  it("puts the sender in the title and the subject in the body", () => {
    const content = notificationContent(
      mail("m1", T1, {
        from: [{ name: "Ada Lovelace", email: "ada@example.com" }],
        subject: "Analytical engine",
      }),
      "Unknown",
      "(no subject)",
    );

    expect(content.title).toBe("Ada Lovelace");
    expect(content.body).toBe("Analytical engine");
    expect(content.icon).toBe(NOTIFICATION_ICON);
  });

  it("carries the brand-resolved icon, not the static one", () => {
    /*
     * Pinned as a LITERAL, because the assertion above compares the constant
     * to itself and would survive any change to it.
     *
     * A toast is the app's most out-of-context surface: it appears over
     * somebody else's window with no other chrome to identify it. The path has
     * to be the one the server resolves per Host, or every customer's
     * notification carries Moov's mark. It must NOT be cached by the service
     * worker either, which `/branding` already guarantees by prefix.
     */
    expect(NOTIFICATION_ICON).toBe("/branding/icons/icon-192.png");
  });

  it("falls back to the address when the display name is absent or blank", () => {
    expect(
      notificationContent(
        mail("m1", T1, { from: [{ name: null, email: "ada@example.com" }] }),
        "Unknown",
        "(no subject)",
      ).title,
    ).toBe("ada@example.com");

    expect(
      notificationContent(
        mail("m1", T1, { from: [{ name: "   ", email: "ada@example.com" }] }),
        "Unknown",
        "(no subject)",
      ).title,
    ).toBe("ada@example.com");
  });

  it("falls back to the supplied string when there is no sender at all", () => {
    expect(
      notificationContent(mail("m1", T1), "Remitente desconocido", "(sin asunto)").title,
    ).toBe("Remitente desconocido");
  });

  it("uses the no-subject string rather than an empty line", () => {
    expect(
      notificationContent(mail("m1", T1, { subject: null }), "x", "(sin asunto)").body,
    ).toBe("(sin asunto)");
    expect(
      notificationContent(mail("m1", T1, { subject: "  " }), "x", "(sin asunto)").body,
    ).toBe("(sin asunto)");
  });

  it("appends the preview under the subject and truncates a long one", () => {
    const short = notificationContent(
      mail("m1", T1, { subject: "Hola", preview: "Cómo va todo" }),
      "x",
      "y",
    );
    expect(short.body).toBe("Hola\nCómo va todo");

    const long = notificationContent(
      mail("m1", T1, { subject: "Hola", preview: "a".repeat(500) }),
      "x",
      "y",
    );
    expect(long.body.endsWith("…")).toBe(true);
    expect(long.body.length).toBeLessThan(200);
  });

  it("tags per message id, which is what collapses duplicates", () => {
    // Two tabs, or a StateChange delivered twice: one toast, not two.
    const a = notificationContent(mail("m1", T1), "x", "y");
    const b = notificationContent(mail("m1", T1), "x", "y");
    expect(a.tag).toBe(b.tag);
    expect(notificationContent(mail("m2", T1), "x", "y").tag).not.toBe(a.tag);
  });
});

describe("EMPTY_ARRIVAL_STATE", () => {
  it("is unseeded, so the first observation is always silent", () => {
    const state: ArrivalState = EMPTY_ARRIVAL_STATE;
    expect(state.seeded).toBe(false);
    expect(state.ids.size).toBe(0);
  });
});
