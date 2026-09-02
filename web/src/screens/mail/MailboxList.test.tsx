import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import type { Mailbox } from "../../mail/types";
import { MailboxList } from "./MailboxList";

/** The sidebar's E2 "Empty trash now" affordance (item 7). */

function mailbox(
  id: string,
  role: Mailbox["role"],
  name: string,
  totalEmails = 4,
): Mailbox {
  return {
    id,
    name,
    parentId: null,
    role,
    sortOrder: role === "inbox" ? 1 : 2,
    totalEmails,
    unreadEmails: 0,
    totalThreads: totalEmails,
    unreadThreads: 0,
    isSubscribed: true,
    myRights: {
      mayReadItems: true,
      mayAddItems: true,
      mayRemoveItems: true,
      maySetSeen: true,
      maySetKeywords: true,
      mayCreateChild: true,
      mayRename: true,
      mayDelete: true,
      maySubmit: true,
    },
  };
}

const MAILBOXES = [
  mailbox("inbox", "inbox", "Inbox"),
  mailbox("trash", "trash", "Trash"),
];

function renderSidebar(overrides: Record<string, unknown> = {}) {
  const onEmptyTrash = vi.fn();
  render(
    <I18nProvider locale="es">
      <MailboxList
        mailboxes={MAILBOXES}
        selectedId="trash"
        onSelect={vi.fn()}
        onEmptyTrash={onEmptyTrash}
        {...overrides}
      />
    </I18nProvider>,
  );
  return { onEmptyTrash };
}

describe("empty trash (E2 item 7)", () => {
  it("puts the affordance on the Trash row and nowhere else", () => {
    renderSidebar();
    const buttons = screen.getAllByRole("button", { name: /vaciar la papelera/i });
    expect(buttons).toHaveLength(1);
  });

  /*
   * The caller passes the handler only while Trash is on screen. A
   * permanently visible irreversible bulk destroy in a sidebar is a mis-click
   * waiting to happen, so its ABSENCE has to be as testable as its presence.
   */
  it("renders nothing when the caller does not offer it", () => {
    renderSidebar({ onEmptyTrash: undefined });
    expect(screen.queryByRole("button", { name: /vaciar la papelera/i })).not.toBeInTheDocument();
  });

  it("hands the Trash mailbox itself to the caller, which owns the confirmation", async () => {
    const user = userEvent.setup();
    const { onEmptyTrash } = renderSidebar();
    await user.click(screen.getByRole("button", { name: /vaciar la papelera/i }));
    expect(onEmptyTrash).toHaveBeenCalledWith(expect.objectContaining({ id: "trash" }));
  });

  it("is disabled on an already-empty Trash", () => {
    render(
      <I18nProvider locale="es">
        <MailboxList
          mailboxes={[mailbox("inbox", "inbox", "Inbox"), mailbox("trash", "trash", "Trash", 0)]}
          selectedId="trash"
          onSelect={vi.fn()}
          onEmptyTrash={vi.fn()}
        />
      </I18nProvider>,
    );
    expect(screen.getByRole("button", { name: /vaciar la papelera/i })).toBeDisabled();
  });

  it("says what it is doing, and refuses a second press, while it runs", () => {
    renderSidebar({ isEmptyingTrash: true });
    const button = screen.getByRole("button", { name: /vaciando la papelera/i });
    expect(button).toBeDisabled();
  });

  /*
   * The button is a SIBLING of the folder link, not a child of it: a button
   * inside an anchor is invalid HTML, and its click would also navigate.
   */
  it("does not nest the button inside the folder link", () => {
    renderSidebar();
    const button = screen.getByRole("button", { name: /vaciar la papelera/i });
    expect(button.closest("a")).toBeNull();
  });

  it("keeps the tree semantics intact", () => {
    renderSidebar();
    expect(screen.getByRole("tree")).toBeInTheDocument();
    expect(screen.getAllByRole("treeitem")).toHaveLength(2);
  });
});

/**
 * E4 — the three outgoing/deferred destinations, and why they stay three
 * (canon §2.2 and §2.3).
 */
describe("E4: the Snoozed folder and the Scheduled entry", () => {
  it("labels the Snoozed folder and does NOT need a role to find it", () => {
    // Dovecot supplies the name in English and RFC 6154 has no SPECIAL-USE
    // attribute for snoozed mail, so the sidebar recognises it by the NAME the
    // session capability published — never by a role that does not exist.
    renderSidebar({
      mailboxes: [...MAILBOXES, mailbox("snz", null, "Snoozed")],
      snoozedMailboxName: "Snoozed",
    });
    expect(screen.getByRole("link", { name: /pospuestos/i })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /^Snoozed$/ })).toBeNull();
  });

  it("leaves a folder alone when the session names a different one", () => {
    // A server that renamed its folder must not have some OTHER folder called
    // "Snoozed" relabelled and re-iconed as if it were the real one.
    renderSidebar({
      mailboxes: [...MAILBOXES, mailbox("snz", null, "Snoozed")],
      snoozedMailboxName: "Zzz",
    });
    expect(screen.getByRole("link", { name: /^Snoozed$/ })).toBeInTheDocument();
  });

  it("draws the Scheduled entry only when something is scheduled", () => {
    renderSidebar();
    expect(screen.queryByRole("button", { name: /programados/i })).toBeNull();

    renderSidebar({ scheduled: { count: 2, isSelected: false, onSelect: vi.fn() } });
    expect(screen.getByRole("button", { name: /programados/i })).toBeInTheDocument();
  });

  it("keeps Outbox and Scheduled as TWO entries, never merged", () => {
    /*
     * Both list mail that has not gone out, and merging them would make "why is
     * this still here?" have two different answers: the Outbox is local to this
     * browser and drains when the network returns, while a scheduled send is a
     * server-side submission other devices can see and cancel.
     */
    renderSidebar({
      outbox: { count: 1, hasFailures: false, isSelected: false, onSelect: vi.fn() },
      scheduled: { count: 2, isSelected: false, onSelect: vi.fn() },
    });
    expect(screen.getByRole("button", { name: /bandeja de salida/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /programados/i })).toBeInTheDocument();
  });

  it("navigates to Scheduled on click", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    renderSidebar({ scheduled: { count: 2, isSelected: false, onSelect } });
    await user.click(screen.getByRole("button", { name: /programados/i }));
    expect(onSelect).toHaveBeenCalledTimes(1);
  });
});

/**
 * "Destacados" and "Pospuestos" — the two rail entries the owner found missing
 * (canon 07 §2, finding 3).
 *
 * What is worth pinning is not that they render, but WHEN and WHERE: both are
 * always-visible entries whose position under Recibidos is the muscle memory,
 * and Pospuestos must not double up once its real folder exists.
 */
describe("the always-visible virtual entries", () => {
  it("shows Destacados, and immediately after Recibidos", () => {
    renderSidebar({ starred: { isSelected: false, onSelect: vi.fn() } });

    const names = screen.getAllByRole("treeitem").map((item) => item.textContent ?? "");
    // Moov's own name for the inbox is "Bandeja de entrada"; Gmail says
    // "Recibidos". That difference is not this test's subject — the ORDER is.
    const inbox = names.findIndex((name) => /bandeja de entrada/i.test(name));
    const starred = names.findIndex((name) => /destacados/i.test(name));
    expect(inbox).toBeGreaterThanOrEqual(0);
    expect(starred).toBeGreaterThanOrEqual(0);
    /*
     * Adjacency, not mere presence. Gmail puts Destacados directly under the
     * inbox, and "the one under Recibidos" is how a migrating user finds it —
     * appending it at the end of the rail would render the same row somewhere
     * the hand does not go.
     */
    expect(starred).toBe(inbox + 1);
  });

  it("navigates to Destacados on click", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    renderSidebar({ starred: { isSelected: false, onSelect } });
    await user.click(screen.getByRole("button", { name: /destacados/i }));
    expect(onSelect).toHaveBeenCalledTimes(1);
  });

  it("shows Pospuestos even though no Snoozed folder exists yet", () => {
    /*
     * The gap the placeholder exists for: GC-10 creates the Snoozed folder on
     * the first real snooze, so before then the tree has nothing to draw and
     * the entry was simply absent — where Gmail shows it always.
     */
    renderSidebar({ snoozedPlaceholder: { isSelected: false, onSelect: vi.fn() } });
    expect(screen.getByRole("button", { name: /pospuestos/i })).toBeInTheDocument();
  });

  it("draws Pospuestos ONCE when the real folder exists", () => {
    /*
     * The caller stops passing the placeholder as soon as the folder is in the
     * tree. Two rows both labelled "Pospuestos" — one routing to a real folder
     * and one to an empty state — is the failure this guards.
     */
    renderSidebar({
      mailboxes: [...MAILBOXES, mailbox("snz", null, "Snoozed")],
      snoozedMailboxName: "Snoozed",
    });
    /*
     * Counted across BOTH roles on purpose. A real folder row is a `link` (it
     * has a URL a middle-click can open) and the placeholder is a `button`, so
     * querying either role alone would miss exactly the duplicate this guards.
     */
    const rows = [
      ...screen.queryAllByRole("link", { name: /pospuestos/i }),
      ...screen.queryAllByRole("button", { name: /pospuestos/i }),
    ];
    expect(rows).toHaveLength(1);
  });

  it("omits both entries when the caller does not pass them", () => {
    // They are the caller's decision, not the list's: nothing here invents a
    // destination the shell has not wired.
    renderSidebar();
    expect(screen.queryByRole("button", { name: /destacados/i })).not.toBeInTheDocument();
    // Neither role: the fixture has no Snoozed folder either, so nothing at all
    // should name Pospuestos.
    expect(screen.queryByRole("button", { name: /pospuestos/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /pospuestos/i })).not.toBeInTheDocument();
  });
});
