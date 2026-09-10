import { describe, expect, it } from "vitest";

import {
  displaySubject,
  groupByThread,
  isFromSelf,
  recipientLabels,
  recipientRowLabel,
  senderLabel,
} from "./threading";
import type { Email } from "./types";

function email(
  id: string,
  options: {
    threadId?: string;
    seen?: boolean;
    flagged?: boolean;
    attachment?: boolean;
    from?: { name: string | null; email: string };
    subject?: string;
  } = {},
): Email {
  const keywords: Record<string, boolean> = {};
  if (options.seen ?? false) keywords.$seen = true;
  if (options.flagged ?? false) keywords.$flagged = true;
  return {
    id,
    ...(options.threadId !== undefined ? { threadId: options.threadId } : {}),
    keywords,
    from: [options.from ?? { name: "Ana Gómez", email: "ana@example.com" }],
    subject: options.subject ?? "Asunto",
    hasAttachment: options.attachment ?? false,
  };
}

describe("groupByThread", () => {
  it("collapses messages sharing a threadId into one row", () => {
    const groups = groupByThread([
      email("e3", { threadId: "t1" }),
      email("e2", { threadId: "t1" }),
      email("e1", { threadId: "t1" }),
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0]?.size).toBe(3);
  });

  /*
   * THE ORDER RULE. The server has already sorted the list; a group takes the
   * position of its NEWEST message. Re-sorting on the client is how a list
   * silently disagrees with the server about order.
   */
  it("preserves server order, placing each group at its newest message", () => {
    const groups = groupByThread([
      email("e9", { threadId: "tB" }),
      email("e8", { threadId: "tA" }),
      email("e7", { threadId: "tB" }),
      email("e6", { threadId: "tC" }),
    ]);
    expect(groups.map((g) => g.id)).toEqual(["tB", "tA", "tC"]);
    expect(groups[0]?.latest.id).toBe("e9");
  });

  it("treats a message with no threadId as its own conversation", () => {
    const groups = groupByThread([email("e1"), email("e2")]);
    expect(groups).toHaveLength(2);
    expect(groups.every((g) => g.size === 1)).toBe(true);
  });

  it("marks a group unread when ANY message in it is unread", () => {
    const groups = groupByThread([
      email("e2", { threadId: "t1", seen: true }),
      email("e1", { threadId: "t1", seen: false }),
    ]);
    expect(groups[0]?.hasUnread).toBe(true);
  });

  it("marks a group read only when every message is read", () => {
    const groups = groupByThread([
      email("e2", { threadId: "t1", seen: true }),
      email("e1", { threadId: "t1", seen: true }),
    ]);
    expect(groups[0]?.hasUnread).toBe(false);
  });

  it("aggregates flags and attachments across the group", () => {
    const groups = groupByThread([
      email("e2", { threadId: "t1", flagged: true }),
      email("e1", { threadId: "t1", attachment: true }),
    ]);
    expect(groups[0]?.hasFlagged).toBe(true);
    expect(groups[0]?.hasAttachment).toBe(true);
  });

  it("lists distinct participants, newest first, without repeats", () => {
    const groups = groupByThread([
      email("e3", { threadId: "t1", from: { name: "Carlos", email: "c@x.com" } }),
      email("e2", { threadId: "t1", from: { name: "Ana", email: "a@x.com" } }),
      email("e1", { threadId: "t1", from: { name: "Carlos", email: "c@x.com" } }),
    ]);
    expect(groups[0]?.participants).toEqual(["Carlos", "Ana"]);
  });

  it("handles an empty list", () => {
    expect(groupByThread([])).toEqual([]);
  });

  it("scales to a 24-message thread, the largest in the pilot's account", () => {
    const messages = Array.from({ length: 24 }, (_, i) =>
      email(`e${i}`, { threadId: "big" }),
    );
    const groups = groupByThread(messages);
    expect(groups).toHaveLength(1);
    expect(groups[0]?.size).toBe(24);
  });
});

describe("senderLabel", () => {
  it("prefers the display name", () => {
    expect(senderLabel(email("e1", { from: { name: "Ana Gómez", email: "a@x.com" } }))).toBe(
      "Ana Gómez",
    );
  });

  it("falls back to the FULL address, never the local part", () => {
    // "info" alone would be ambiguous across domains — a list that shows two
    // different people as the same name is a list that misleads.
    expect(senderLabel(email("e1", { from: { name: null, email: "info@areacorp.com" } }))).toBe(
      "info@areacorp.com",
    );
  });

  it("ignores a whitespace-only name", () => {
    expect(senderLabel(email("e1", { from: { name: "   ", email: "a@x.com" } }))).toBe("a@x.com");
  });

  it("returns undefined when there is no From at all", () => {
    expect(senderLabel({ id: "e1", from: null })).toBeUndefined();
  });
});

/**
 * E1: the collapsed path, where the server returns one message per
 * conversation and a `Thread/get` carries the real sizes.
 *
 * The distinction these pin is the one the UI shows the user: a count that IS
 * the conversation's size versus a count that is only what fits in the window.
 */
describe("groupByThread with server thread sizes", () => {
  it("reports the server's size and marks it exact", () => {
    // The collapsed shape: ONE message back, but the thread has 24.
    const groups = groupByThread(
      [email("e24", { threadId: "t1" })],
      [{ id: "t1", emailIds: Array.from({ length: 24 }, (_, i) => `e${i + 1}`) }],
    );
    expect(groups[0]?.size).toBe(24);
    expect(groups[0]?.sizeIsExact).toBe(true);
  });

  it("falls back to the window count, NOT exact, when no thread came back", () => {
    const groups = groupByThread([
      email("e2", { threadId: "t1" }),
      email("e1", { threadId: "t1" }),
    ]);
    expect(groups[0]?.size).toBe(2);
    expect(groups[0]?.sizeIsExact).toBe(false);
  });

  it("ignores a thread whose reported size is smaller than the window holds", () => {
    /*
     * The two disagreeing about membership should never happen — a window is a
     * subset of a thread — but if it does, the LARGER number is at least true
     * and the claim of exactness is dropped rather than reporting a count
     * below the rows actually on screen.
     */
    const groups = groupByThread(
      [email("e2", { threadId: "t1" }), email("e1", { threadId: "t1" })],
      [{ id: "t1", emailIds: ["e1"] }],
    );
    expect(groups[0]?.size).toBe(2);
    expect(groups[0]?.sizeIsExact).toBe(false);
  });

  it("is an identity pass over an already-collapsed list", () => {
    // One message per thread in, one row per thread out, order preserved.
    const groups = groupByThread(
      [
        email("b1", { threadId: "tB" }),
        email("a1", { threadId: "tA" }),
        email("c1", { threadId: "tC" }),
      ],
      [
        { id: "tA", emailIds: ["a1", "a0"] },
        { id: "tB", emailIds: ["b1"] },
        { id: "tC", emailIds: ["c1", "c0", "c-1"] },
      ],
    );
    expect(groups.map((g) => g.id)).toEqual(["tB", "tA", "tC"]);
    expect(groups.map((g) => g.size)).toEqual([1, 2, 3]);
    expect(groups.every((g) => g.sizeIsExact)).toBe(true);
  });

  it("leaves unread and flagged aggregation to the messages in hand", () => {
    /*
     * A collapsed row can only know the state of the message the server
     * returned — the newest. That is a real limit of the collapsed path and it
     * is left visible rather than papered over: `hasUnread` means "the message
     * on this row is unread", which is what the row displays.
     */
    const groups = groupByThread(
      [email("e2", { threadId: "t1", seen: true })],
      [{ id: "t1", emailIds: ["e1", "e2"] }],
    );
    expect(groups[0]?.hasUnread).toBe(false);
    expect(groups[0]?.size).toBe(2);
  });
});

describe("displaySubject", () => {
  it.each([
    ["Re: Hola", "Hola"],
    ["RE: Hola", "Hola"],
    ["Fwd: Hola", "Hola"],
    ["Fw: Hola", "Hola"],
    ["Re: Fwd: Re: Hola", "Hola"],
    ["RV: Hola", "Hola"], // Spanish forward
    ["AW: Hallo", "Hallo"], // German reply
    ["TR: Bonjour", "Bonjour"], // French forward
    ["Re[2]: Hola", "Hola"],
  ])("strips the prefix from %o", (input, expected) => {
    expect(displaySubject(input)).toBe(expected);
  });

  it("leaves a subject that merely contains 're:' mid-sentence alone", () => {
    expect(displaySubject("Sobre el core: arquitectura")).toBe("Sobre el core: arquitectura");
  });

  it("keeps the original when the subject is only a prefix", () => {
    // Stripping to "" would render a blank row.
    expect(displaySubject("Re:")).toBe("Re:");
  });

  it("returns undefined for an absent subject", () => {
    expect(displaySubject(null)).toBeUndefined();
    expect(displaySubject(undefined)).toBeUndefined();
  });
});

/** A message with recipients, for the Sent/Drafts rules. */
function addressed(
  id: string,
  to: readonly { name: string | null; email: string }[],
  options: {
    cc?: readonly { name: string | null; email: string }[];
    threadId?: string;
    from?: { name: string | null; email: string };
  } = {},
): Email {
  return {
    ...email(id, {
      ...(options.threadId !== undefined ? { threadId: options.threadId } : {}),
      ...(options.from !== undefined ? { from: options.from } : {}),
    }),
    to: [...to],
    ...(options.cc !== undefined ? { cc: [...options.cc] } : {}),
  };
}

describe("recipientLabels", () => {
  it("prefers the name and falls back to the address", () => {
    const message = addressed("m", [
      { name: "Hernán", email: "hernan@example.com" },
      { name: null, email: "pablo@example.com" },
    ]);
    expect(recipientLabels(message)).toEqual(["Hernán", "pablo@example.com"]);
  });

  it("reads To then Cc, deduplicated by ADDRESS", () => {
    const message = addressed("m", [{ name: "Hernán", email: "hernan@example.com" }], {
      cc: [
        { name: "Hernán H", email: "HERNAN@example.com" },
        { name: "Ana", email: "ana@example.com" },
      ],
    });
    // The Cc repeat of the same mailbox collapses, keeping the To spelling.
    expect(recipientLabels(message)).toEqual(["Hernán", "Ana"]);
  });

  it("never reads Bcc — a blind copy must not be printed in a list", () => {
    const message: Email = {
      ...addressed("m", [{ name: "Hernán", email: "hernan@example.com" }]),
      bcc: [{ name: "Secreto", email: "secreto@example.com" }],
    };
    expect(recipientLabels(message)).toEqual(["Hernán"]);
  });

  it("is empty for a message addressed to nobody", () => {
    expect(recipientLabels(email("m"))).toEqual([]);
  });
});

describe("recipientRowLabel", () => {
  it("prefixes the recipients, Gmail's shape", () => {
    const [group] = groupByThread([
      addressed("m", [
        { name: "Hernán", email: "hernan@example.com" },
        { name: "Pablo", email: "pablo@example.com" },
      ]),
    ]);
    expect(recipientRowLabel(group!, "Para: ")).toBe("Para: Hernán, Pablo");
  });

  it("unions a thread's recipients in first-seen (newest-first) order", () => {
    const groups = groupByThread([
      addressed("m2", [{ name: "Pablo", email: "pablo@example.com" }], { threadId: "t" }),
      addressed("m1", [{ name: "Hernán", email: "hernan@example.com" }], { threadId: "t" }),
    ]);
    expect(recipientRowLabel(groups[0]!, "To: ")).toBe("To: Pablo, Hernán");
  });

  it("collapses a recipient repeated across the thread", () => {
    const groups = groupByThread([
      addressed("m2", [{ name: "Hernán", email: "hernan@example.com" }], { threadId: "t" }),
      addressed("m1", [{ name: "Hernán", email: "hernan@example.com" }], { threadId: "t" }),
    ]);
    expect(recipientRowLabel(groups[0]!, "To: ")).toBe("To: Hernán");
  });

  it("is undefined with no recipients, so the caller can fall back", () => {
    const [group] = groupByThread([email("m")]);
    // A bare "Para: " pointing at nothing is worse than the sender.
    expect(recipientRowLabel(group!, "Para: ")).toBeUndefined();
  });
});

describe("isFromSelf", () => {
  it("matches the account's own address, case-insensitively", () => {
    const mine = email("m", { from: { name: "Yo", email: "Diego@Example.com" } });
    expect(isFromSelf(mine, ["diego@example.com"])).toBe(true);
  });

  it("is false for anybody else, and for a message with no From", () => {
    expect(isFromSelf(email("m"), ["diego@example.com"])).toBe(false);
    expect(isFromSelf({ id: "m", keywords: {} }, ["diego@example.com"])).toBe(false);
  });

  it("is false when the account has no known address", () => {
    const mine = email("m", { from: { name: "Yo", email: "diego@example.com" } });
    expect(isFromSelf(mine, [])).toBe(false);
  });
});
