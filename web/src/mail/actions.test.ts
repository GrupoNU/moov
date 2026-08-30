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

/**
 * E8 — the label/unlabel action kinds.
 *
 * They exercise the one part of the overlay that was NOT there before: an
 * arbitrary keyword map, merged rather than replaced, and inverted from the
 * message rather than from the patch.
 */
describe("label and unlabel", () => {
  const WORK = "$label:work";
  const CLIENTS = "$label:clients";

  const labelled = (keywords: Record<string, boolean> = {}): Email => ({
    id: "e1",
    keywords,
    mailboxIds: { m1: true },
  });

  it("paints the keyword without removing the row", () => {
    // A label is not a move: the row stays where it is, in every view.
    const patch = patchFor({ kind: "label", ids: ["e1"], keyword: WORK }, labelled(), "m1");
    expect(patch).toEqual({ keywords: { [WORK]: true } });
    expect(patch.removed).toBeUndefined();
  });

  it("clears the keyword on unlabel", () => {
    const patch = patchFor({ kind: "unlabel", ids: ["e1"], keyword: WORK }, labelled(), "m1");
    expect(patch).toEqual({ keywords: { [WORK]: false } });
  });

  it("is a no-op when no keyword travels, rather than a patch that means nothing", () => {
    expect(patchFor({ kind: "label", ids: ["e1"] }, labelled(), "m1")).toEqual({});
  });

  it("inverts from the MESSAGE, so a failed apply on an already-labelled message restores it", () => {
    /*
     * The subtle one. Inverting the PATCH would restore "absent" and strip a
     * label the user never touched — the same class of bug the seen/flagged
     * inverse was written to avoid.
     */
    const already = labelled({ [WORK]: true });
    const patch = patchFor({ kind: "label", ids: ["e1"], keyword: WORK }, already, "m1");
    expect(inverseFor(patch, already, "m1")).toEqual({ keywords: { [WORK]: true } });

    const fresh = labelled();
    const patch2 = patchFor({ kind: "label", ids: ["e1"], keyword: WORK }, fresh, "m1");
    expect(inverseFor(patch2, fresh, "m1")).toEqual({ keywords: { [WORK]: false } });
  });

  it("applies the keyword to the email as a SET member, never as false", () => {
    // The keywords property is an object-as-set (RFC 8621 §4.1.1): a cleared
    // keyword must be ABSENT, or the chip renderer would list it.
    const withLabel = applyPatch(labelled({ $seen: true }), { keywords: { [WORK]: true } });
    expect(withLabel.keywords).toEqual({ $seen: true, [WORK]: true });

    const without = applyPatch(labelled({ $seen: true, [WORK]: true }), {
      keywords: { [WORK]: false },
    });
    expect(without.keywords).toEqual({ $seen: true });
    expect(Object.keys(without.keywords ?? {})).not.toContain(WORK);
  });

  it("MERGES two label patches on one message instead of dropping the first", () => {
    /*
     * The bug a shallow spread would produce: applying two labels in one visit
     * to the menu would paint only the second, and the first would flicker back
     * until the refetch. The menu stays open precisely so this happens.
     */
    let overlay = withPatch(EMPTY_OVERLAY, "e1", { keywords: { [WORK]: true } });
    overlay = withPatch(overlay, "e1", { keywords: { [CLIENTS]: true } });
    expect(overlay.get("e1")?.keywords).toEqual({ [WORK]: true, [CLIENTS]: true });
  });

  it("does not let a label patch clobber a concurrent archive on the same message", () => {
    let overlay = withPatch(EMPTY_OVERLAY, "e1", { mailboxId: "m2", removed: true });
    overlay = withPatch(overlay, "e1", { keywords: { [WORK]: true } });
    const patch = overlay.get("e1");
    expect(patch?.removed).toBe(true);
    expect(patch?.mailboxId).toBe("m2");
    expect(patch?.keywords).toEqual({ [WORK]: true });
  });

  it("survives the round trip through applyOverlay", () => {
    const overlay = withPatch(EMPTY_OVERLAY, "e1", { keywords: { [WORK]: true } });
    const [out] = applyOverlay([labelled({ $seen: true })], overlay);
    expect(out?.keywords).toEqual({ $seen: true, [WORK]: true });
  });

  it("plans both halves from the same snapshot", () => {
    const emails = [labelled({ [WORK]: true }), { ...labelled(), id: "e2" }];
    const { patches, inverses } = planAction(
      { kind: "label", ids: ["e1", "e2"], keyword: WORK },
      emails,
      "m1",
    );
    expect(patches.get("e1")?.keywords).toEqual({ [WORK]: true });
    // e1 already had it, e2 did not — the inverses differ accordingly.
    expect(inverses.get("e1")?.keywords).toEqual({ [WORK]: true });
    expect(inverses.get("e2")?.keywords).toEqual({ [WORK]: false });
  });
});
