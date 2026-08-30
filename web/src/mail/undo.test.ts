import { describe, expect, it } from "vitest";

import type { MessageAction, MessagePatch } from "./actions";
import {
  inverseActionFor,
  isUndoable,
  isUndoableAction,
  makeUndoEntry,
  undoDescriptionKey,
  UNDO_WINDOW_MS,
  type UndoEntry,
} from "./undo";

const NO_PATCHES: ReadonlyMap<string, MessagePatch> = new Map();

function action(kind: MessageAction["kind"], mailboxId?: string): MessageAction {
  return { kind, ids: ["m1"], ...(mailboxId !== undefined ? { mailboxId } : {}) };
}

describe("which actions offer an undo", () => {
  it.each([
    ["archive" as const],
    ["delete" as const],
    ["move" as const],
    ["spam" as const],
    ["notSpam" as const],
  ])("offers one for %s — the row disappeared", (kind) => {
    expect(isUndoableAction(action(kind, "box"))).toBe(true);
  });

  it.each([
    ["markRead" as const],
    ["markUnread" as const],
    ["flag" as const],
    ["unflag" as const],
  ])("does not offer one for %s — reversing it is one keypress in place", (kind) => {
    expect(isUndoableAction(action(kind))).toBe(false);
  });

  it("refuses an action with no targets, so an empty toast cannot appear", () => {
    expect(isUndoableAction({ kind: "archive", ids: [], mailboxId: "a" })).toBe(false);
  });
});

describe("the inverse action — the half that actually reaches the server", () => {
  it("moves the messages back to where they came from", () => {
    const inverse = inverseActionFor(action("archive", "archiveBox"), "inbox");
    expect(inverse).toEqual({ kind: "move", ids: ["m1"], mailboxId: "inbox" });
  });

  it("brings a spam report back out of Junk", () => {
    const inverse = inverseActionFor(action("spam", "junk"), "inbox");
    expect(inverse).toEqual({ kind: "move", ids: ["m1"], mailboxId: "inbox" });
  });

  /*
   * The load-bearing refusal. A permanent delete has no reverse on the server,
   * and offering one would repaint a row for a message that no longer exists —
   * the exact lie the module header calls out.
   */
  it("refuses to invert a permanent delete", () => {
    expect(
      inverseActionFor(action("delete"), "trash", { wasPermanent: true }),
    ).toBeUndefined();
  });

  it("refuses when the origin is unknown, rather than guessing a folder", () => {
    expect(inverseActionFor(action("archive", "archiveBox"), undefined)).toBeUndefined();
  });

  it("refuses a move whose destination is already the origin", () => {
    expect(inverseActionFor(action("move", "inbox"), "inbox")).toBeUndefined();
  });
});

describe("the entry and its window", () => {
  it("is undoable inside the window and not at its boundary", () => {
    const entry = makeUndoEntry({
      id: 1,
      action: action("archive", "archiveBox"),
      inverses: NO_PATCHES,
      originMailboxId: "inbox",
      now: 1_000,
    });
    expect(entry).toBeDefined();
    expect(isUndoable(entry, 1_000)).toBe(true);
    expect(isUndoable(entry, 1_000 + UNDO_WINDOW_MS - 1)).toBe(true);
    // Exactly at expiry the offer is gone — the boundary, pinned.
    expect(isUndoable(entry, 1_000 + UNDO_WINDOW_MS)).toBe(false);
    expect(isUndoable(entry, 1_000 + UNDO_WINDOW_MS + 1)).toBe(false);
  });

  it("produces no entry at all when the action cannot be reversed", () => {
    expect(
      makeUndoEntry({
        id: 1,
        action: action("delete"),
        inverses: NO_PATCHES,
        originMailboxId: "trash",
        now: 0,
        wasPermanent: true,
      }),
    ).toBeUndefined();
  });

  it("treats an absent entry as nothing to undo", () => {
    expect(isUndoable(undefined, 0)).toBe(false);
  });

  it("carries the inverse patches through for the instant repaint", () => {
    const inverses = new Map<string, MessagePatch>([["m1", { removed: false }]]);
    const entry = makeUndoEntry({
      id: 7,
      action: action("archive", "archiveBox"),
      inverses,
      originMailboxId: "inbox",
      now: 0,
    });
    expect(entry?.inverses.get("m1")).toEqual({ removed: false });
    expect(entry?.id).toBe(7);
  });
});

describe("the toast's wording", () => {
  it.each([
    ["archive" as const, "action.doneArchived"],
    ["delete" as const, "action.doneDeleted"],
    ["spam" as const, "action.doneSpam"],
    ["notSpam" as const, "action.doneNotSpam"],
    ["move" as const, "action.doneMoved"],
  ])("names %s with its own string", (kind, key) => {
    expect(undoDescriptionKey(action(kind, "box"))).toBe(key);
  });
});

/**
 * E4 — a snoozed conversation's undo (canon §2.2).
 *
 * A snooze is the one undoable action whose reverse is NOT a `MessageAction`,
 * and the tests below pin why: a `move` back into the inbox would restore the
 * row while leaving the server's wake time in place, so the message would
 * return and then vanish again at the appointed hour. The only honest reverse
 * is `Snooze/set destroy`, so the entry carries the ids for it instead.
 */
describe("the snooze entry (E4)", () => {
  const NOW = 1_000_000;

  function snoozeEntry(overrides: Partial<UndoEntry> = {}): UndoEntry {
    return {
      id: 1,
      action: { kind: "move", ids: ["e1", "e2"], mailboxId: "snoozed" },
      // No inverse ACTION by construction — see the block comment above.
      inverseAction: undefined,
      inverses: new Map(),
      expiresAt: NOW + UNDO_WINDOW_MS,
      unsnoozeIds: ["e1", "e2"],
      ...overrides,
    };
  }

  it("is undoable on its unsnoozeIds alone, with no inverse action", () => {
    expect(isUndoable(snoozeEntry(), NOW)).toBe(true);
  });

  it("expires on the same boundary as every other entry", () => {
    const entry = snoozeEntry();
    expect(isUndoable(entry, entry.expiresAt - 1)).toBe(true);
    // Strictly less-than: at exactly expiresAt the window has closed.
    expect(isUndoable(entry, entry.expiresAt)).toBe(false);
  });

  it("is NOT undoable when it carries neither an action nor ids", () => {
    // The guard that keeps an entry with nothing to re-issue from offering an
    // undo button that would do nothing.
    expect(isUndoable(snoozeEntry({ unsnoozeIds: [] }), NOW)).toBe(false);
    // The absent case, spelled by OMITTING the key: `exactOptionalPropertyTypes`
    // is on, so `undefined` is not a value an optional property may hold — and
    // the distinction is real, since the whole point is that the field may not
    // be there at all.
    const { unsnoozeIds: _omitted, ...withoutIds } = snoozeEntry();
    expect(isUndoable(withoutIds, NOW)).toBe(false);
  });

  it("leaves an ordinary entry's rules untouched", () => {
    // The E4 branch must widen the condition, never weaken the existing one: a
    // move with a real inverse is still undoable, and one with neither is not.
    const ordinary: UndoEntry = {
      id: 2,
      action: { kind: "archive", ids: ["e3"], mailboxId: "archive" },
      inverseAction: { kind: "move", ids: ["e3"], mailboxId: "inbox" },
      inverses: new Map(),
      expiresAt: NOW + UNDO_WINDOW_MS,
    };
    expect(isUndoable(ordinary, NOW)).toBe(true);
    expect(isUndoable({ ...ordinary, inverseAction: undefined }, NOW)).toBe(false);
  });
});
