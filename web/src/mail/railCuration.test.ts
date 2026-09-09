import { describe, expect, it } from "vitest";

import { buildMailboxTree, type MailboxNode } from "./mailboxes";
import {
  curateRail,
  disambiguateName,
  effectiveVisibility,
  isHiddenByPolicy,
  isVisibleInRail,
  normalizeFolderName,
  orderMore,
  PRIMARY_ROLES,
} from "./railCuration";
import type { FolderVisibility } from "./prefs";
import type { Mailbox, MailboxRole } from "./types";

/**
 * The rail's curation policy (P0-5).
 *
 * The failure it prevents is not subtle: the owner's real account put about
 * twenty-five folders in the sidebar, including Calendario, Diario, Fuentes RSS
 * and "Problemas de sincronización" whose Conflictos child carried a badge of
 * 26 — a number demanding attention about a folder that holds no mail.
 *
 * Every assertion here is about a DEFAULT, never a rule: nothing is deleted,
 * and a stored choice always wins over the policy. The tests that matter most
 * are the ones proving the policy stays out of the way — a role-bearing folder
 * is never hidden by its name, and a user's "show" survives.
 */

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
    unread?: number;
  } = {},
): Mailbox {
  return {
    id,
    name,
    parentId: options.parentId ?? null,
    role: options.role ?? null,
    sortOrder: 100,
    totalEmails: 0,
    unreadEmails: options.unread ?? 0,
    totalThreads: 0,
    unreadThreads: options.unread ?? 0,
    myRights: RIGHTS,
    isSubscribed: true,
  };
}

const NO_CHOICES: Readonly<Record<string, FolderVisibility>> = {};

describe("normalizeFolderName", () => {
  it("folds accents, case and whitespace so both locales' spellings meet", () => {
    expect(normalizeFolderName("Problemas de sincronización")).toBe(
      "problemas de sincronizacion",
    );
    expect(normalizeFolderName("  RSS   Feeds ")).toBe("rss feeds");
  });
});

describe("the default hide policy", () => {
  it("hides the Outlook and Exchange folders that hold no mail", () => {
    const boxes = [
      mailbox("c", "Calendario"),
      mailbox("j", "Diario"),
      mailbox("r", "RSS Feeds"),
      mailbox("s", "Sync Issues"),
      mailbox("h", "Historial de conversaciones"),
      mailbox("d", "Elementos detectados"),
    ];
    for (const box of boxes) expect(isHiddenByPolicy(box, boxes)).toBe(true);
  });

  it("hides a CHILD of a hidden folder — Conflictos (26) was the whole complaint", () => {
    const boxes = [
      mailbox("s", "Problemas de sincronización"),
      mailbox("c", "Conflictos", { parentId: "s", unread: 26 }),
    ];
    expect(isHiddenByPolicy(boxes[1]!, boxes)).toBe(true);
  });

  it("NEVER hides a folder that carries a role, whatever it is called", () => {
    // The worst outcome a name heuristic could have: the account's real Junk
    // folder disappearing because Outlook also localises it "Correo no
    // deseado". The role is the stronger statement.
    const junk = mailbox("j", "Correo no deseado", { role: "junk" });
    expect(isHiddenByPolicy(junk, [junk])).toBe(false);
  });

  it("leaves an ordinary folder alone", () => {
    const box = mailbox("w", "Trabajo");
    expect(isHiddenByPolicy(box, [box])).toBe(false);
  });
});

describe("the user's stored choice", () => {
  it("beats the policy in both directions", () => {
    const boxes = [mailbox("c", "Calendario"), mailbox("w", "Trabajo")];
    const stored: Record<string, FolderVisibility> = { Calendario: "show", Trabajo: "hide" };
    expect(effectiveVisibility(boxes[0]!, boxes, stored)).toBe("show");
    expect(effectiveVisibility(boxes[1]!, boxes, stored)).toBe("hide");
  });

  it("resolves showIfUnread against the count, not in the component", () => {
    const quiet = mailbox("a", "Avisos");
    const noisy = mailbox("b", "Alertas", { unread: 3 });
    const stored: Record<string, FolderVisibility> = {
      Avisos: "showIfUnread",
      Alertas: "showIfUnread",
    };
    expect(isVisibleInRail(quiet, [quiet, noisy], stored)).toBe(false);
    expect(isVisibleInRail(noisy, [quiet, noisy], stored)).toBe(true);
  });
});

describe("curateRail", () => {
  const boxes = [
    mailbox("i", "INBOX", { role: "inbox" }),
    mailbox("s", "Sent", { role: "sent" }),
    mailbox("d", "Drafts", { role: "drafts" }),
    mailbox("a", "Archive", { role: "archive" }),
    mailbox("j", "Junk", { role: "junk" }),
    mailbox("t", "Trash", { role: "trash" }),
    mailbox("w", "Trabajo"),
    mailbox("c", "Calendario"),
  ];
  const nodes = buildMailboxTree(boxes);
  const of = (node: MailboxNode): Mailbox => node.mailbox;

  it("keeps only Gmail's canonical rows above the collapse", () => {
    const { primary } = curateRail(nodes, of, boxes, NO_CHOICES);
    expect(primary.map((node) => node.mailbox.role)).toEqual(["inbox", "sent", "drafts"]);
    // Canon 07 §2: Enviados BEFORE Borradores, which is the opposite of what
    // the rail shipped with (review A-03).
    expect(PRIMARY_ROLES).toEqual(["inbox", "sent", "drafts"]);
  });

  it("puts everything else behind Más, minus what the policy hides", () => {
    const { more } = curateRail(nodes, of, boxes, NO_CHOICES);
    const names = more.map((node) => node.mailbox.name);
    expect(names).toContain("Archive");
    expect(names).toContain("Trash");
    expect(names).toContain("Trabajo");
    expect(names).not.toContain("Calendario");
  });

  it("never leaves a child stranded on the other side of the collapse", () => {
    const nested = [
      mailbox("i", "INBOX", { role: "inbox" }),
      mailbox("w", "Trabajo"),
      mailbox("w2", "Clientes", { parentId: "w" }),
    ];
    const { primary, more } = curateRail(buildMailboxTree(nested), of, nested, NO_CHOICES);
    expect(primary.map((node) => node.mailbox.id)).toEqual(["i"]);
    expect(more.map((node) => node.mailbox.id)).toEqual(["w", "w2"]);
  });
});

describe("orderMore", () => {
  it("leads with Archivo, Spam and Papelera, then the user's own folders", () => {
    const boxes = [
      mailbox("w", "Trabajo"),
      mailbox("t", "Trash", { role: "trash" }),
      mailbox("a", "Archive", { role: "archive" }),
      mailbox("z", "Zeta"),
      mailbox("j", "Junk", { role: "junk" }),
    ];
    const ordered = orderMore(boxes, (box) => box);
    expect(ordered.map((box) => box.name)).toEqual([
      "Archive",
      "Junk",
      "Trash",
      "Trabajo",
      "Zeta",
    ]);
  });

  it("keeps a subtree together when it reorders the roots", () => {
    const boxes = [
      mailbox("w", "Trabajo"),
      mailbox("w2", "Clientes", { parentId: "w" }),
      mailbox("a", "Archive", { role: "archive" }),
    ];
    expect(orderMore(boxes, (box) => box).map((box) => box.id)).toEqual(["a", "w", "w2"]);
  });
});

describe("disambiguateName", () => {
  const taken = new Set(["archivo"]);

  it("qualifies a custom folder colliding with a role's label", () => {
    // The account that prompted this drew two rows both reading "Archivo" and
    // gave the user no way to tell them apart.
    const custom = mailbox("x", "Archivo");
    expect(disambiguateName(custom, "Archivo", taken, [custom], "carpeta")).toBe(
      "Archivo (carpeta)",
    );
  });

  it("prefers the parent path, which is information the user recognises", () => {
    const parent = mailbox("p", "Trabajo");
    const custom = mailbox("x", "Archivo", { parentId: "p" });
    expect(disambiguateName(custom, "Archivo", taken, [parent, custom], "carpeta")).toBe(
      "Trabajo/Archivo",
    );
  });

  it("leaves the ROLE row's own label alone — it is the one everything means", () => {
    const role = mailbox("a", "Archive", { role: "archive" });
    expect(disambiguateName(role, "Archivo", taken, [role], "carpeta")).toBe("Archivo");
  });

  it("touches nothing when there is no collision", () => {
    const custom = mailbox("x", "Trabajo");
    expect(disambiguateName(custom, "Trabajo", taken, [custom], "carpeta")).toBe("Trabajo");
  });
});
