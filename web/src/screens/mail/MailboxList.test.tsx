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
