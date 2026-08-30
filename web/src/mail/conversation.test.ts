import { describe, expect, it } from "vitest";

import {
  adjacentMessage,
  bodiesToFetch,
  collapseAll,
  conversationOrder,
  conversationRows,
  EMPTY_CONVERSATION,
  expandAll,
  initialExpanded,
  isAllExpanded,
  messagesToMarkRead,
  toggleExpanded,
  withMarked,
  type ConversationState,
} from "./conversation";
import { KEYWORD_SEEN, type Email } from "./types";

/**
 * The conversation state machine's rules, enumerated.
 *
 * The three that carry real risk are the ones with the most cases here:
 * newest-at-the-bottom (an inversion against the list's order), lazy bodies
 * (the 24-message thread must not fetch 24 bodies), and read-marking (only
 * what was EXPANDED, which is the difference between Gmail's behavior and
 * silently marking a whole thread read).
 */

function email(id: string, receivedAt: string, seen = true, extra: Partial<Email> = {}): Email {
  return {
    id,
    threadId: "t1",
    receivedAt,
    keywords: seen ? { [KEYWORD_SEEN]: true } : {},
    ...extra,
  };
}

/** A four-message thread, deliberately supplied NEWEST-first (list order). */
const THREAD: readonly Email[] = [
  email("m4", "2026-08-04T10:00:00Z"),
  email("m3", "2026-08-03T10:00:00Z"),
  email("m2", "2026-08-02T10:00:00Z"),
  email("m1", "2026-08-01T10:00:00Z"),
];

const state = (expanded: string[], marked: string[] = []): ConversationState => ({
  expanded: new Set(expanded),
  marked: new Set(marked),
});

describe("conversation order", () => {
  it("puts the newest at the BOTTOM (canon §2.1)", () => {
    expect(conversationOrder(THREAD).map((m) => m.id)).toEqual(["m1", "m2", "m3", "m4"]);
  });

  it("is stable for messages sharing a timestamp", () => {
    const same = [
      email("b", "2026-08-01T10:00:00Z"),
      email("a", "2026-08-01T10:00:00Z"),
    ];
    // A list fan-out delivers in the same second; without a tiebreaker the two
    // would swap between renders and the thread would look like it reshuffled.
    expect(conversationOrder(same).map((m) => m.id)).toEqual(["a", "b"]);
    expect(conversationOrder([...same].reverse()).map((m) => m.id)).toEqual(["a", "b"]);
  });

  it("keeps a message with no date rather than dropping it", () => {
    const withUndated = [...THREAD, { id: "m0", threadId: "t1" } as Email];
    expect(conversationOrder(withUndated)).toHaveLength(5);
    expect(conversationOrder(withUndated)[0]?.id).toBe("m0");
  });

  it("does not mutate its input", () => {
    const input = [...THREAD];
    conversationOrder(input);
    expect(input.map((m) => m.id)).toEqual(["m4", "m3", "m2", "m1"]);
  });
});

describe("what starts expanded", () => {
  it("opens only the newest when everything is read", () => {
    expect([...initialExpanded(THREAD)]).toEqual(["m4"]);
  });

  it("also opens every unread message, wherever it sits", () => {
    const withUnread = [
      THREAD[0]!,
      email("m3", "2026-08-03T10:00:00Z", false),
      THREAD[2]!,
      THREAD[3]!,
    ];
    const expanded = initialExpanded(withUnread);
    expect(expanded.has("m4")).toBe(true);
    expect(expanded.has("m3")).toBe(true);
    expect(expanded.has("m2")).toBe(false);
  });

  it("always opens the message the route named, even if old and read", () => {
    // Arriving from a search result and finding that very message collapsed
    // would be the app hiding what it just navigated to.
    const expanded = initialExpanded(THREAD, "m1");
    expect(expanded.has("m1")).toBe(true);
    expect(expanded.has("m4")).toBe(true);
  });

  it("ignores an openId that is not in this thread", () => {
    expect([...initialExpanded(THREAD, "elsewhere")]).toEqual(["m4"]);
  });

  it("handles an empty thread", () => {
    expect([...initialExpanded([])]).toEqual([]);
  });
});

describe("expanding and collapsing", () => {
  it("toggles one message both ways", () => {
    const opened = toggleExpanded(EMPTY_CONVERSATION, "m2");
    expect(opened.expanded.has("m2")).toBe(true);
    expect(toggleExpanded(opened, "m2").expanded.has("m2")).toBe(false);
  });

  it("`;` expands every message", () => {
    const all = expandAll(EMPTY_CONVERSATION, THREAD);
    expect(all.expanded.size).toBe(4);
    expect(isAllExpanded(all, THREAD)).toBe(true);
  });

  it("`:` collapses everything EXCEPT the newest", () => {
    // Never to nothing: a pane of closed rows with no message is a dead end.
    const collapsed = collapseAll(expandAll(EMPTY_CONVERSATION, THREAD), THREAD);
    expect([...collapsed.expanded]).toEqual(["m4"]);
  });

  it("keeps the marked set across expand and collapse", () => {
    const marked = withMarked(EMPTY_CONVERSATION, ["m1"]);
    expect(expandAll(marked, THREAD).marked.has("m1")).toBe(true);
    expect(collapseAll(marked, THREAD).marked.has("m1")).toBe(true);
  });

  it("isAllExpanded is false for an empty thread", () => {
    expect(isAllExpanded(expandAll(EMPTY_CONVERSATION, []), [])).toBe(false);
  });
});

describe("lazy body fetching", () => {
  it("asks only for expanded messages", () => {
    // THE rule that keeps a 24-message thread from costing 24 bodies.
    expect(bodiesToFetch(state(["m4"]), THREAD, new Set())).toEqual(["m4"]);
  });

  it("asks for nothing when everything is collapsed", () => {
    expect(bodiesToFetch(EMPTY_CONVERSATION, THREAD, new Set())).toEqual([]);
  });

  it("does not re-ask for a body already held", () => {
    expect(bodiesToFetch(state(["m4", "m3"]), THREAD, new Set(["m4"]))).toEqual(["m3"]);
  });

  it("does not ask for a message that already carries bodyValues", () => {
    const loaded = [
      email("m4", "2026-08-04T10:00:00Z", true, {
        bodyValues: { "0": { value: "hi", isEncodingProblem: false, isTruncated: false } },
      }),
      ...THREAD.slice(1),
    ];
    expect(bodiesToFetch(state(["m4"]), loaded, new Set())).toEqual([]);
  });

  it("returns them in reading order, oldest first", () => {
    expect(bodiesToFetch(state(["m4", "m1", "m2"]), THREAD, new Set())).toEqual([
      "m1",
      "m2",
      "m4",
    ]);
  });
});

describe("read-marking (only what was expanded)", () => {
  const unreadThread: readonly Email[] = [
    email("m4", "2026-08-04T10:00:00Z", false),
    email("m3", "2026-08-03T10:00:00Z", false),
    email("m2", "2026-08-02T10:00:00Z", false),
    email("m1", "2026-08-01T10:00:00Z", false),
  ];

  it("marks ONLY the expanded messages, never the whole thread", () => {
    // Gmail's behavior and the honest one: opening a thread with four unread
    // replies to read the newest must not mark the other three read.
    expect(messagesToMarkRead(state(["m4"]), unreadThread)).toEqual(["m4"]);
  });

  it("marks each newly expanded message as it opens", () => {
    expect([...messagesToMarkRead(state(["m4", "m2"]), unreadThread)].sort()).toEqual([
      "m2",
      "m4",
    ]);
  });

  it("never re-marks a message it has already marked", () => {
    // Without this the effect would loop against its own optimistic update.
    expect(messagesToMarkRead(state(["m4"], ["m4"]), unreadThread)).toEqual([]);
  });

  it("never marks an already-read message", () => {
    expect(messagesToMarkRead(state(["m4"]), THREAD)).toEqual([]);
  });

  it("withMarked is a no-op for an empty list", () => {
    expect(withMarked(EMPTY_CONVERSATION, [])).toBe(EMPTY_CONVERSATION);
  });
});

describe("p / n inside the conversation", () => {
  it("`n` walks toward the newest, `p` toward the oldest", () => {
    expect(adjacentMessage(THREAD, "m2", "next")?.id).toBe("m3");
    expect(adjacentMessage(THREAD, "m2", "previous")?.id).toBe("m1");
  });

  it("stops at the ends rather than wrapping", () => {
    // Wrapping would take a user who pressed `n` once too often from the
    // newest message to the oldest, which reads as the thread jumping.
    expect(adjacentMessage(THREAD, "m4", "next")).toBeUndefined();
    expect(adjacentMessage(THREAD, "m1", "previous")).toBeUndefined();
  });

  it("starts from the end each key travels from when nothing is current", () => {
    expect(adjacentMessage(THREAD, undefined, "next")?.id).toBe("m4");
    expect(adjacentMessage(THREAD, undefined, "previous")?.id).toBe("m1");
  });

  it("returns nothing for an id outside the thread, or an empty thread", () => {
    expect(adjacentMessage(THREAD, "nope", "next")).toBeUndefined();
    expect(adjacentMessage([], undefined, "next")).toBeUndefined();
  });
});

describe("the render plan", () => {
  it("lists every message in reading order with its state", () => {
    const rows = conversationRows(state(["m4"]), [
      THREAD[0]!,
      email("m3", "2026-08-03T10:00:00Z", false),
      THREAD[2]!,
      THREAD[3]!,
    ]);
    expect(rows.map((row) => row.id)).toEqual(["m1", "m2", "m3", "m4"]);
    expect(rows.find((row) => row.id === "m3")?.isUnread).toBe(true);
    expect(rows.find((row) => row.id === "m4")?.isExpanded).toBe(true);
    expect(rows.find((row) => row.id === "m1")?.isExpanded).toBe(false);
  });
});
