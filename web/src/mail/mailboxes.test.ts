import { describe, expect, it } from "vitest";

import {
  badgeCount,
  buildMailboxTree,
  findByRole,
  mailboxSegment,
  resolveMailbox,
  showsUnreadBadge,
} from "./mailboxes";
import type { Mailbox, MailboxRole } from "./types";

const RIGHTS = {
  mayReadItems: true,
  mayAddItems: true,
  mayRemoveItems: true,
  maySetSeen: true,
  maySetKeywords: true,
  mayCreateChild: true,
  mayRename: true,
  mayDelete: true,
  maySubmit: true,
};

function mailbox(
  id: string,
  name: string,
  options: {
    role?: MailboxRole | null;
    parentId?: string | null;
    total?: number;
    unread?: number;
  } = {},
): Mailbox {
  return {
    id,
    name,
    parentId: options.parentId ?? null,
    role: options.role ?? null,
    sortOrder: 100,
    totalEmails: options.total ?? 0,
    unreadEmails: options.unread ?? 0,
    totalThreads: options.total ?? 0,
    unreadThreads: options.unread ?? 0,
    myRights: RIGHTS,
    isSubscribed: true,
  };
}

describe("buildMailboxTree", () => {
  it("orders roles Inbox, Sent, Drafts, Archive, Junk, Trash before custom folders", () => {
    // P0-5 (review A-03): Enviados before Borradores — canon 07 §2's order,
    // not P2's. The rail is navigated from memory, so the order is the API.
    // Deliberately supplied in a scrambled order.
    const tree = buildMailboxTree([
      mailbox("m1", "Zeta"),
      mailbox("m2", "Trash", { role: "trash" }),
      mailbox("m3", "INBOX", { role: "inbox" }),
      mailbox("m4", "Junk", { role: "junk" }),
      mailbox("m5", "Alpha"),
      mailbox("m6", "Sent", { role: "sent" }),
      mailbox("m7", "Drafts", { role: "drafts" }),
      mailbox("m8", "Archive", { role: "archive" }),
    ]);

    expect(tree.map((node) => node.mailbox.name)).toEqual([
      "INBOX",
      "Sent",
      "Drafts",
      "Archive",
      "Junk",
      "Trash",
      "Alpha",
      "Zeta",
    ]);
  });

  it("nests children under their parent with increasing depth", () => {
    // The shape the pilot's own moov-test account has: S2 with five children.
    const tree = buildMailboxTree([
      mailbox("m1", "S2"),
      mailbox("m2", "folder2", { parentId: "m1" }),
      mailbox("m3", "folder1", { parentId: "m1" }),
      mailbox("m4", "deep", { parentId: "m3" }),
    ]);

    expect(tree.map((n) => [n.mailbox.name, n.depth])).toEqual([
      ["S2", 0],
      ["folder1", 1],
      ["deep", 2],
      ["folder2", 1],
    ]);
  });

  it("marks which nodes have children so the sidebar can show a twisty", () => {
    const tree = buildMailboxTree([
      mailbox("m1", "Parent"),
      mailbox("m2", "Child", { parentId: "m1" }),
    ]);
    expect(tree.find((n) => n.mailbox.id === "m1")?.hasChildren).toBe(true);
    expect(tree.find((n) => n.mailbox.id === "m2")?.hasChildren).toBe(false);
  });

  it("promotes orphans to the top level rather than hiding them", () => {
    // A parent that is not in the list (unsubscribed, or filtered out) must
    // not make its children invisible.
    const tree = buildMailboxTree([mailbox("m2", "Orphan", { parentId: "missing" })]);
    expect(tree).toHaveLength(1);
    expect(tree[0]?.depth).toBe(0);
  });

  it("survives a parentId cycle without recursing forever", () => {
    const tree = buildMailboxTree([
      { ...mailbox("m1", "A"), parentId: "m2" },
      { ...mailbox("m2", "B"), parentId: "m1" },
    ]);
    // Neither is reachable from the root, so nothing is rendered — but the
    // call returns rather than blowing the stack, which is the point.
    expect(Array.isArray(tree)).toBe(true);
  });

  it("sorts custom folders numerically so 'Folder 10' follows 'Folder 9'", () => {
    const tree = buildMailboxTree([
      mailbox("m1", "Folder 10"),
      mailbox("m2", "Folder 9"),
      mailbox("m3", "Folder 1"),
    ]);
    expect(tree.map((n) => n.mailbox.name)).toEqual(["Folder 1", "Folder 9", "Folder 10"]);
  });

  it("handles an empty account", () => {
    expect(buildMailboxTree([])).toEqual([]);
  });
});

describe("resolveMailbox", () => {
  const boxes = [
    mailbox("mc", "INBOX", { role: "inbox" }),
    mailbox("m1", "inbox", { role: null }),
  ];

  it("prefers the role over a custom folder with the same name", () => {
    // A folder literally named "inbox" must not shadow the real Inbox.
    expect(resolveMailbox(boxes, "inbox")?.id).toBe("mc");
  });

  it("falls back to an id for folders with no role", () => {
    expect(resolveMailbox(boxes, "m1")?.id).toBe("m1");
  });

  it("returns undefined for an unknown segment", () => {
    expect(resolveMailbox(boxes, "nope")).toBeUndefined();
  });
});

describe("mailboxSegment", () => {
  it("uses the role so links survive being shared across accounts", () => {
    expect(mailboxSegment(mailbox("mc", "INBOX", { role: "inbox" }))).toBe("inbox");
  });

  it("falls back to the id for a custom folder", () => {
    expect(mailboxSegment(mailbox("m5", "Proyectos"))).toBe("m5");
  });
});

describe("badges", () => {
  it("does not badge Sent, whose unread count is an artefact of the append", () => {
    // The pilot's real Sent folder reports 1 unread of 1 message.
    expect(showsUnreadBadge(mailbox("m7", "Sent", { role: "sent" }))).toBe(false);
    expect(badgeCount(mailbox("m7", "Sent", { role: "sent", total: 1, unread: 1 }))).toBeUndefined();
  });

  it("badges Inbox with its unread count", () => {
    expect(badgeCount(mailbox("mc", "INBOX", { role: "inbox", total: 626, unread: 623 }))).toBe(623);
  });

  it("badges Drafts with its TOTAL, because a draft is not 'unread'", () => {
    expect(badgeCount(mailbox("ma", "Drafts", { role: "drafts", total: 3, unread: 3 }))).toBe(3);
  });

  it("shows no badge when there is nothing to report", () => {
    expect(badgeCount(mailbox("mc", "INBOX", { role: "inbox", total: 10, unread: 0 }))).toBeUndefined();
  });
});

describe("findByRole", () => {
  it("finds the inbox", () => {
    const boxes = [mailbox("m1", "X"), mailbox("mc", "INBOX", { role: "inbox" })];
    expect(findByRole(boxes, "inbox")?.id).toBe("mc");
  });

  it("returns undefined when the account has no such role", () => {
    expect(findByRole([mailbox("m1", "X")], "archive")).toBeUndefined();
  });
});
