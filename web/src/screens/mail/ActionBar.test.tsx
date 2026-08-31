import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import type { Mailbox } from "../../mail/types";
import { ActionBar, type ActionBarProps } from "./ActionBar";

/** The E2 spam control in the bulk toolbar. */

function mailbox(id: string, role: Mailbox["role"], name: string): Mailbox {
  return {
    id,
    name,
    parentId: null,
    role,
    sortOrder: 0,
    totalEmails: 1,
    unreadEmails: 0,
    totalThreads: 1,
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

function renderBar(overrides: Partial<ActionBarProps> = {}) {
  const props: ActionBarProps = {
    selectedCount: 1,
    onMarkRead: vi.fn(),
    onMarkUnread: vi.fn(),
    onFlag: vi.fn(),
    onArchive: vi.fn(),
    onDelete: vi.fn(),
    onMove: vi.fn(),
    mailboxes: [mailbox("inbox", "inbox", "Inbox"), mailbox("junk", "junk", "Junk")],
    currentMailboxId: "inbox",
    deleteIsPermanent: false,
    onCompose: vi.fn(),
    isBusy: false,
    onToggleSpam: vi.fn(),
    inJunk: false,
    // E8: the bar hosts the "Label as" menu. Empty by default — a bar with no
    // labels must still render every other control.
    labels: [],
    labelSelection: [],
    onToggleLabel: vi.fn(),
    onManageLabels: vi.fn(),
    ...overrides,
  };
  render(
    <I18nProvider locale="es">
      <ActionBar {...props} />
    </I18nProvider>,
  );
  return props;
}

describe("the spam control (E2 item 1)", () => {
  it("reads 'Marcar como spam' outside Junk", async () => {
    const user = userEvent.setup();
    const props = renderBar();
    await user.click(screen.getByRole("button", { name: /^marcar como spam$/i }));
    expect(props.onToggleSpam).toHaveBeenCalledTimes(1);
  });

  /*
   * ONE control that flips, not two of which one is always dead. Inside Junk
   * "Report spam" would be meaningless, and a permanently disabled twin is
   * clutter that teaches the user to ignore the toolbar.
   */
  it("flips to 'No es spam' inside Junk", () => {
    renderBar({ inJunk: true, currentMailboxId: "junk" });
    expect(screen.getByRole("button", { name: /^no es spam$/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^marcar como spam$/i })).not.toBeInTheDocument();
  });

  it("is disabled with nothing selected, like every other bulk action", () => {
    renderBar({ selectedCount: 0 });
    expect(screen.getByRole("button", { name: /^marcar como spam$/i })).toBeDisabled();
  });

  it("is disabled while an action is in flight", () => {
    renderBar({ isBusy: true });
    expect(screen.getByRole("button", { name: /^marcar como spam$/i })).toBeDisabled();
  });
});

describe("the shared move menu still works after the extraction", () => {
  it("opens and offers every folder except the one on screen", async () => {
    const user = userEvent.setup();
    renderBar();
    await user.click(screen.getByRole("button", { name: /mover a una carpeta/i }));
    const items = screen.getAllByRole("menuitem");
    expect(items).toHaveLength(1);
    expect(items[0]).toHaveTextContent(/spam|junk/i);
  });

  it("reports the destination to the caller and closes", async () => {
    const user = userEvent.setup();
    const props = renderBar();
    await user.click(screen.getByRole("button", { name: /mover a una carpeta/i }));
    const [first] = screen.getAllByRole("menuitem");
    if (first === undefined) throw new Error("the move menu offered nothing");
    await user.click(first);
    expect(props.onMove).toHaveBeenCalledWith("junk");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });
});

/**
 * E4 — snooze and mute in the bar (canon §2.2).
 *
 * The property under test is FEATURE DETECTION: a vendor capability the server
 * does not advertise is a feature that does not exist here, and a permanently
 * greyed-out button invites the user to hunt for the selection that would
 * enable it. So absence removes the control, and only a missing SELECTION
 * disables one.
 */
describe("E4: snooze and mute", () => {
  it("shows neither control when the server has no triage capability", () => {
    renderBar();
    expect(screen.queryByRole("button", { name: /posponer/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /silenciar/i })).toBeNull();
  });

  it("shows both once the capability is present", () => {
    renderBar({ onSnooze: vi.fn(), onToggleMute: vi.fn() });
    expect(screen.getByRole("button", { name: /posponer hasta/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^silenciar$/i })).toBeInTheDocument();
  });

  it("names the mute control by what the click will DO, like the spam one", () => {
    // Every conversation already muted → the button offers to unmute.
    renderBar({ onToggleMute: vi.fn(), allMuted: true });
    expect(screen.getByRole("button", { name: /dejar de silenciar/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^silenciar$/i })).toBeNull();
  });

  it("disables both with nothing selected, and enables them with a selection", () => {
    renderBar({ selectedCount: 0, onSnooze: vi.fn(), onToggleMute: vi.fn() });
    expect(screen.getByRole("button", { name: /posponer hasta/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: /^silenciar$/i })).toBeDisabled();

    renderBar({ selectedCount: 2, onSnooze: vi.fn(), onToggleMute: vi.fn() });
    expect(screen.getAllByRole("button", { name: /posponer hasta/i })[1]).toBeEnabled();
  });

  it("offers unsnooze ONLY where there is something snoozed to bring back", () => {
    renderBar({ onSnooze: vi.fn() });
    expect(screen.queryByRole("button", { name: /traer ahora/i })).toBeNull();

    renderBar({ onSnooze: vi.fn(), onUnsnooze: vi.fn() });
    expect(screen.getByRole("button", { name: /traer ahora/i })).toBeInTheDocument();
  });

  it("mutes the selection on click", async () => {
    const user = userEvent.setup();
    const onToggleMute = vi.fn();
    renderBar({ onToggleMute });
    await user.click(screen.getByRole("button", { name: /^silenciar$/i }));
    expect(onToggleMute).toHaveBeenCalledTimes(1);
  });
});
