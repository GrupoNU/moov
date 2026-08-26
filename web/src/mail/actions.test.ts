import { describe, expect, it } from "vitest";

import {
  applyOverlay,
  applyPatch,
  deleteIsPermanent,
  EMPTY_OVERLAY,
  inverseFor,
  patchFor,
  planAction,
  resolveToggle,
  withPatch,
  withPatches,
  withoutIds,
  type MessageAction,
} from "./actions";
import { KEYWORD_FLAGGED, KEYWORD_SEEN, type Email } from "./types";

function email(id: string, overrides: Partial<Email> = {}): Email {
  return {
    id,
    mailboxIds: { inbox: true },
    keywords: {},
    subject: id,
    receivedAt: "2026-08-20T10:00:00Z",
    ...overrides,
  };
}

describe("patchFor", () => {
  it("marks read and unread", () => {
    expect(patchFor({ kind: "markRead", ids: ["e1"] }, email("e1"), "inbox")).toEqual({
      seen: true,
    });
    expect(patchFor({ kind: "markUnread", ids: ["e1"] }, email("e1"), "inbox")).toEqual({
      seen: false,
    });
  });

  it("flags and unflags", () => {
    expect(patchFor({ kind: "flag", ids: ["e1"] }, email("e1"), "inbox")).toEqual({
      flagged: true,
    });
    expect(patchFor({ kind: "unflag", ids: ["e1"] }, email("e1"), "inbox")).toEqual({
      flagged: false,
    });
  });

  it("removes the row when a move leaves the folder being viewed", () => {
    const action: MessageAction = { kind: "archive", ids: ["e1"], mailboxId: "archive" };
    expect(patchFor(action, email("e1"), "inbox")).toEqual({
      mailboxId: "archive",
      removed: true,
    });
  });

  /*
   * Moving a message INTO the folder you are looking at must not make the row
   * disappear — the message is still right there.
   */
  it("does not remove the row when the destination is the current folder", () => {
    const action: MessageAction = { kind: "move", ids: ["e1"], mailboxId: "inbox" };
    expect(patchFor(action, email("e1"), "inbox")).toEqual({ mailboxId: "inbox" });
  });

  it("produces no patch for a move with no destination", () => {
    expect(patchFor({ kind: "move", ids: ["e1"] }, email("e1"), "inbox")).toEqual({});
  });

  /*
   * REGRESSION. A delete carries no destination on purpose — the server owns
   * the W-A2 semantics — and an earlier version let it fall through to the
   * move branch, which returns {} for a missing mailboxId. The consequence was
   * not a cosmetic one: `planAction` skips empty patches, so `delete` painted
   * nothing AND never issued the request. The key pressed, and nothing at all
   * happened.
   */
  it("removes the row for a delete, which names no destination", () => {
    expect(patchFor({ kind: "delete", ids: ["e1"] }, email("e1"), "inbox")).toEqual({
      removed: true,
    });
  });

  it("plans a delete into a real patch, so the action is actually issued", () => {
    const { patches, inverses } = planAction(
      { kind: "delete", ids: ["e1"] },
      [email("e1")],
      "inbox",
    );
    expect(patches.size).toBe(1);
    // And its rollback puts the row back: a failed delete must not leave the
    // message hidden, because the message is still there.
    expect(inverses.get("e1")).toEqual({ removed: false });
  });

  it("restores a deleted row on rollback even in Trash, where nothing moves", () => {
    const inTrash = email("e1", { mailboxIds: { trash: true } });
    const { patches, inverses } = planAction({ kind: "delete", ids: ["e1"] }, [inTrash], "trash");
    const hidden = applyOverlay([inTrash], new Map([["e1", patches.get("e1") ?? {}]]));
    expect(hidden).toEqual([]);
    const restored = applyOverlay([inTrash], new Map([["e1", inverses.get("e1") ?? {}]]));
    expect(restored.map((message) => message.id)).toEqual(["e1"]);
  });
});

describe("inverseFor", () => {
  /*
   * The load-bearing property: the inverse comes from the message's ACTUAL
   * prior state, not from flipping the patch. Marking an already-read message
   * as read and then failing must leave it read.
   */
  it("restores the true prior read state, not the negation of the patch", () => {
    const alreadyRead = email("e1", { keywords: { [KEYWORD_SEEN]: true } });
    expect(inverseFor({ seen: true }, alreadyRead, "inbox")).toEqual({ seen: true });

    const unread = email("e2");
    expect(inverseFor({ seen: true }, unread, "inbox")).toEqual({ seen: false });
  });

  it("restores the original mailbox and un-removes the row", () => {
    const message = email("e1", { mailboxIds: { inbox: true } });
    expect(inverseFor({ mailboxId: "archive", removed: true }, message, "inbox")).toEqual({
      mailboxId: "inbox",
      removed: false,
    });
  });

  it("touches nothing the patch did not touch", () => {
    const message = email("e1", { keywords: { [KEYWORD_FLAGGED]: true } });
    expect(inverseFor({ seen: true }, message, "inbox")).toEqual({ seen: false });
  });
});

describe("applyPatch", () => {
  it("adds and removes keywords without clobbering the others", () => {
    const message = email("e1", { keywords: { [KEYWORD_FLAGGED]: true, $answered: true } });
    const patched = applyPatch(message, { seen: true });
    expect(patched.keywords).toEqual({
      [KEYWORD_FLAGGED]: true,
      $answered: true,
      [KEYWORD_SEEN]: true,
    });

    const unflagged = applyPatch(patched, { flagged: false });
    expect(unflagged.keywords).toEqual({ $answered: true, [KEYWORD_SEEN]: true });
  });

  it("replaces the mailbox set — the server holds one mailbox per message", () => {
    expect(applyPatch(email("e1"), { mailboxId: "archive" }).mailboxIds).toEqual({
      archive: true,
    });
  });

  it("does not mutate the input", () => {
    const message = email("e1");
    applyPatch(message, { seen: true, mailboxId: "archive" });
    expect(message.keywords).toEqual({});
    expect(message.mailboxIds).toEqual({ inbox: true });
  });
});

describe("applyOverlay", () => {
  const list = [email("e1"), email("e2"), email("e3")];

  it("returns the SAME array when the overlay is empty", () => {
    expect(applyOverlay(list, EMPTY_OVERLAY)).toBe(list);
  });

  it("filters out removed messages", () => {
    const overlay = withPatch(EMPTY_OVERLAY, "e2", { removed: true });
    expect(applyOverlay(list, overlay).map((message) => message.id)).toEqual(["e1", "e3"]);
  });

  it("merges keyword patches", () => {
    const overlay = withPatch(EMPTY_OVERLAY, "e1", { seen: true });
    expect(applyOverlay(list, overlay)[0]?.keywords?.[KEYWORD_SEEN]).toBe(true);
  });

  /*
   * The whole reason the overlay is a patch map rather than a snapshot: a
   * refetch replaces `emails` wholesale, and a patch for an id no longer in
   * the list must simply not apply — not crash, not resurrect the row.
   */
  it("ignores patches for ids not in the list", () => {
    const overlay = withPatch(EMPTY_OVERLAY, "gone", { seen: true });
    expect(applyOverlay(list, overlay)).toBe(list);
  });

  it("preserves the server's order", () => {
    const overlay = withPatches(
      EMPTY_OVERLAY,
      new Map([
        ["e1", { seen: true }],
        ["e3", { flagged: true }],
      ]),
    );
    expect(applyOverlay(list, overlay).map((message) => message.id)).toEqual([
      "e1",
      "e2",
      "e3",
    ]);
  });
});

describe("withPatch / withoutIds", () => {
  it("merges rather than replaces, so two actions on one message coexist", () => {
    let overlay = withPatch(EMPTY_OVERLAY, "e1", { seen: true });
    overlay = withPatch(overlay, "e1", { flagged: true });
    expect(overlay.get("e1")).toEqual({ seen: true, flagged: true });
  });

  it("never mutates the input overlay", () => {
    const overlay = withPatch(EMPTY_OVERLAY, "e1", { seen: true });
    withPatch(overlay, "e2", { seen: true });
    expect(overlay.size).toBe(1);
  });

  it("drops confirmed ids so later server truth is not masked", () => {
    const overlay = withPatches(
      EMPTY_OVERLAY,
      new Map([
        ["e1", { seen: true }],
        ["e2", { seen: true }],
      ]),
    );
    const after = withoutIds(overlay, ["e1"]);
    expect([...after.keys()]).toEqual(["e2"]);
  });

  it("returns the same overlay when nothing was dropped", () => {
    const overlay = withPatch(EMPTY_OVERLAY, "e1", { seen: true });
    expect(withoutIds(overlay, ["other"])).toBe(overlay);
    expect(withoutIds(overlay, [])).toBe(overlay);
  });
});

describe("planAction", () => {
  it("computes patches and inverses from the SAME snapshot", () => {
    const emails = [
      email("e1"),
      email("e2", { keywords: { [KEYWORD_SEEN]: true } }),
    ];
    const { patches, inverses } = planAction(
      { kind: "markRead", ids: ["e1", "e2"] },
      emails,
      "inbox",
    );
    expect(patches.get("e1")).toEqual({ seen: true });
    expect(patches.get("e2")).toEqual({ seen: true });
    // e1 was unread, e2 was already read — the inverses differ, which a
    // patch-flipping implementation would get wrong for e2.
    expect(inverses.get("e1")).toEqual({ seen: false });
    expect(inverses.get("e2")).toEqual({ seen: true });
  });

  it("skips ids that are not in the list", () => {
    const { patches } = planAction({ kind: "flag", ids: ["e1", "ghost"] }, [email("e1")], "inbox");
    expect([...patches.keys()]).toEqual(["e1"]);
  });

  it("round-trips: applying the patch then its inverse restores the message", () => {
    const original = email("e1", { keywords: { [KEYWORD_FLAGGED]: true } });
    const { patches, inverses } = planAction(
      { kind: "archive", ids: ["e1"], mailboxId: "archive" },
      [original],
      "inbox",
    );
    const forward = applyPatch(original, patches.get("e1") ?? {});
    const back = applyPatch(forward, inverses.get("e1") ?? {});
    expect(back.mailboxIds).toEqual(original.mailboxIds);
    expect(back.keywords).toEqual(original.keywords);
  });
});

describe("resolveToggle", () => {
  /*
   * Gmail's rule, copied: a mixed selection resolves to "make them all read".
   * Toggling each message independently leaves the selection still mixed,
   * which is never what anyone wanted.
   */
  it("marks all read when any is unread", () => {
    const emails = [email("e1", { keywords: { [KEYWORD_SEEN]: true } }), email("e2")];
    expect(resolveToggle(emails, KEYWORD_SEEN)).toEqual({ value: true });
  });

  it("marks all unread only when every one is read", () => {
    const emails = [
      email("e1", { keywords: { [KEYWORD_SEEN]: true } }),
      email("e2", { keywords: { [KEYWORD_SEEN]: true } }),
    ];
    expect(resolveToggle(emails, KEYWORD_SEEN)).toEqual({ value: false });
  });

  it("applies the same rule to flags", () => {
    expect(resolveToggle([email("e1")], KEYWORD_FLAGGED)).toEqual({ value: true });
    expect(
      resolveToggle([email("e1", { keywords: { [KEYWORD_FLAGGED]: true } })], KEYWORD_FLAGGED),
    ).toEqual({ value: false });
  });

  it("is false for an empty selection — nothing to make true", () => {
    expect(resolveToggle([], KEYWORD_SEEN)).toEqual({ value: false });
  });
});

describe("deleteIsPermanent", () => {
  /*
   * Server arbitration W-A2: destroy MOVES to Trash unless the message is
   * already there. The UI must say which of the two is about to happen —
   * one word for both promises is a lie in one of the cases.
   */
  it("is false for a message outside Trash", () => {
    expect(deleteIsPermanent(email("e1"), "trash")).toBe(false);
  });

  it("is true for a message already in Trash", () => {
    expect(deleteIsPermanent(email("e1", { mailboxIds: { trash: true } }), "trash")).toBe(true);
  });

  it("is false when the account has no Trash — the server refuses anyway", () => {
    expect(deleteIsPermanent(email("e1"), undefined)).toBe(false);
  });
});
