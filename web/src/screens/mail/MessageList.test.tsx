import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import { groupByThread } from "../../mail/threading";
import { KEYWORD_SEEN, type Email } from "../../mail/types";
import { MessageList } from "./MessageList";

/**
 * The list's E2 hover actions (item 5).
 *
 * The three properties worth pinning are the ones that break silently:
 *
 *   - each action targets ITS OWN row, never the selection — the pointer has
 *     already named the target, and routing through the selection is the bug
 *     that makes hover actions feel dangerous;
 *   - clicking one does NOT also open the message (the row's own click
 *     handler is one bubble away);
 *   - they are reachable by keyboard and do not corrupt the `role="grid"`
 *     semantics the virtualizer depends on.
 *
 * Note what is NOT asserted: that they are visually hidden until hover. That
 * is a CSS `:hover` rule, and jsdom has no hover — asserting on it here would
 * only test the stylesheet loader. It is verified in a real browser.
 */

function email(id: string, overrides: Partial<Email> = {}): Email {
  return {
    id,
    threadId: `t-${id}`,
    mailboxIds: { inbox: true },
    keywords: { [KEYWORD_SEEN]: true },
    subject: `Subject ${id}`,
    from: [{ name: `Sender ${id}`, email: `${id}@example.com` }],
    receivedAt: "2026-08-20T10:00:00Z",
    ...overrides,
  };
}

function renderList(overrides: Record<string, unknown> = {}) {
  const emails = [email("a"), email("b", { keywords: {} })];
  const handlers = {
    onRowArchive: vi.fn(),
    onRowDelete: vi.fn(),
    onRowToggleRead: vi.fn(),
    onOpen: vi.fn(),
    onSelect: vi.fn(),
  };
  render(
    <I18nProvider locale="es">
      <MessageList
        listKey="mailbox:inbox"
        groups={groupByThread(emails)}
        selectedId="t-a"
        isLoading={false}
        empty={<p>empty</p>}
        {...handlers}
        {...overrides}
      />
    </I18nProvider>,
  );
  return handlers;
}

/** The row whose accessible content contains the given subject. */
function rowFor(subject: string): HTMLElement {
  const row = screen
    .getAllByRole("row")
    .find((candidate) => within(candidate).queryByText(subject) !== null);
  if (row === undefined) throw new Error(`no row for ${subject}`);
  return row;
}

describe("hover actions (E2 item 5)", () => {
  it("puts three actions — and only three — in every row", () => {
    renderList();
    const row = rowFor("Subject a");
    /*
     * Gmail ships FOUR, including snooze. Snooze is epic E4's (it needs the
     * Snoozed mailbox and the engine's return-to-inbox job), and a dead
     * fourth button would be worse than three that work. If E4 lands and this
     * number does not move, the button was forgotten.
     */
    expect(within(row).getAllByRole("button")).toHaveLength(3);
    expect(within(row).getByRole("button", { name: /archivar/i })).toBeInTheDocument();
    expect(within(row).getByRole("button", { name: /mover a la papelera/i })).toBeInTheDocument();
  });

  it("acts on ITS OWN row, not on whatever is selected elsewhere", async () => {
    const user = userEvent.setup();
    // "a" is the selected row; the click lands on "b".
    const handlers = renderList();
    await user.click(within(rowFor("Subject b")).getByRole("button", { name: /archivar/i }));
    expect(handlers.onRowArchive).toHaveBeenCalledTimes(1);
    expect(handlers.onRowArchive.mock.calls[0]?.[0]).toMatchObject({ id: "t-b" });
  });

  /*
   * Without stopPropagation the row's own click handler also fires and the
   * message opens — so "archive" would archive AND open, which is the single
   * most confusing thing a hover action can do.
   */
  it("does not also open the message", async () => {
    const user = userEvent.setup();
    const handlers = renderList();
    await user.click(within(rowFor("Subject a")).getByRole("button", { name: /archivar/i }));
    expect(handlers.onOpen).not.toHaveBeenCalled();
  });

  it("names the read toggle by what the click will DO to that row", () => {
    renderList();
    // "a" is read → the action marks it unread; "b" is unread → marks it read.
    expect(
      within(rowFor("Subject a")).getByRole("button", { name: /marcar como no leído/i }),
    ).toBeInTheDocument();
    expect(
      within(rowFor("Subject b")).getByRole("button", { name: /marcar como leído/i }),
    ).toBeInTheDocument();
  });

  it("is reachable by keyboard rather than being mouse-only", async () => {
    const user = userEvent.setup();
    const handlers = renderList();
    const archive = within(rowFor("Subject a")).getByRole("button", { name: /archivar/i });
    // Focusable within the row: the roving tabindex governs the ROWS, not the
    // controls inside the one the user is on.
    expect(archive).toHaveAttribute("tabindex", "0");
    archive.focus();
    await user.keyboard("{Enter}");
    expect(handlers.onRowArchive).toHaveBeenCalledTimes(1);
    // And Enter must not ALSO reach the row's open handler.
    expect(handlers.onOpen).not.toHaveBeenCalled();
  });

  it("keeps the grid semantics the virtualizer depends on", () => {
    renderList();
    const grid = screen.getByRole("grid");
    expect(grid).toHaveAttribute("aria-rowcount", "2");
    const row = rowFor("Subject a");
    expect(row).toHaveAttribute("aria-rowindex", "1");
    // The actions live in a gridcell of their own, so the row is still a row
    // of cells rather than a row with a loose button in it.
    expect(within(row).getAllByRole("gridcell").length).toBeGreaterThanOrEqual(4);
  });

  it("renders no action cell at all when the handlers are absent", () => {
    renderList({ onRowArchive: undefined, onRowDelete: undefined, onRowToggleRead: undefined });
    // Only the selection checkbox remains, and it is not a button.
    expect(within(rowFor("Subject a")).queryAllByRole("button")).toHaveLength(0);
  });
});
