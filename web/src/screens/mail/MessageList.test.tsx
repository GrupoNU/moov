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
  it("puts three actions in a row when the server has no snooze", () => {
    renderList();
    const row = rowFor("Subject a");
    /*
     * Gmail ships FOUR. The fourth arrived with E4 and is a RENDER PROP, so a
     * server without the vendor triage capability still gets exactly these
     * three rather than a dead button — which was the whole reason E2 shipped
     * three and named the epic that would supply the fourth.
     */
    expect(within(row).getAllByRole("button")).toHaveLength(3);
    expect(within(row).getByRole("button", { name: /archivar/i })).toBeInTheDocument();
    expect(within(row).getByRole("button", { name: /mover a la papelera/i })).toBeInTheDocument();
  });

  it("puts FOUR — Gmail's exact set — once E4 supplies the snooze trigger", () => {
    renderList({
      renderRowSnooze: (_group: unknown, className: string) => (
        <button type="button" className={className} aria-label="Posponer" />
      ),
    });
    const row = rowFor("Subject a");
    expect(within(row).getAllByRole("button")).toHaveLength(4);
    expect(within(row).getByRole("button", { name: /posponer/i })).toBeInTheDocument();
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

/**
 * E4 — what a row says about mute and snooze (canon §2.2).
 *
 * Both are facts the row has to carry visually AND to assistive technology,
 * and both come from data the row does not own: a set of muted thread ids and
 * a map of wake times. The tests below pin the three things that would break
 * silently — the badge appearing for the wrong thread, the wake time not
 * replacing the received date, and "unsnooze" also opening the message.
 */
describe("E4: the muted badge and the Snoozed view's rows", () => {
  it("badges only the conversation whose thread is in the muted set", () => {
    renderList({ mutedThreadIds: new Set(["t-a"]) });
    // The icon is decorative; the visually-hidden state is what a screen
    // reader hears, and it is what is asserted.
    expect(within(rowFor("Subject a")).getByText(/silenciada/i)).toBeInTheDocument();
    expect(within(rowFor("Subject b")).queryByText(/silenciada/i)).toBeNull();
  });

  it("says nothing about mute when nothing is muted", () => {
    renderList();
    expect(screen.queryByText(/silenciada/i)).toBeNull();
  });

  it("replaces the received date with the wake time in the Snoozed view", () => {
    renderList({ snoozeUntilById: new Map([["a", "2026-09-04T08:00:00Z"]]) });
    const row = rowFor("Subject a");
    // The machine-readable value is the contract; the rendered text is locale
    // and timezone dependent, so it is the `datetime` that is asserted.
    const when = within(row).getByText(/vuelve/i);
    expect(when.getAttribute("datetime")).toBe("2026-09-04T08:00:00Z");
  });

  it("leaves a row with no pending snooze showing its ordinary date", () => {
    renderList({ snoozeUntilById: new Map([["a", "2026-09-04T08:00:00Z"]]) });
    expect(within(rowFor("Subject b")).queryByText(/vuelve/i)).toBeNull();
  });

  it("offers unsnooze per row, and it does NOT also open the message", async () => {
    const user = userEvent.setup();
    const onRowUnsnooze = vi.fn();
    const handlers = renderList({
      snoozeUntilById: new Map([["a", "2026-09-04T08:00:00Z"]]),
      onRowUnsnooze,
    });
    await user.click(
      within(rowFor("Subject a")).getByRole("button", { name: /traer ahora/i }),
    );
    expect(onRowUnsnooze).toHaveBeenCalledTimes(1);
    // The bug this prevents: bringing a message back and immediately opening
    // it, because the row's own click handler is one bubble away.
    expect(handlers.onOpen).not.toHaveBeenCalled();
  });

  it("shows no unsnooze affordance without a handler for it", () => {
    renderList({ snoozeUntilById: new Map([["a", "2026-09-04T08:00:00Z"]]) });
    expect(within(rowFor("Subject a")).queryByRole("button", { name: /traer ahora/i })).toBeNull();
  });
});

/**
 * The row's clickable star (E12/B4, canon 07 §3).
 *
 * It was a read-only icon in the meta strip until E12, which meant the one
 * gesture every Gmail user makes without looking — click the star — silently
 * did nothing. These pin the three properties that would break it quietly.
 */
describe("the star", () => {
  it("is not rendered as a control when no flag action is wired", () => {
    /*
     * A caller with no `onRowToggleFlag` gets INFORMATION, not a dead control:
     * the read-only icon still shows which rows are starred. That is P4 applied
     * to the one row element that has both a stateful and a stateless form.
     */
    renderList();
    expect(
      within(rowFor("Subject a")).queryByRole("button", { name: /destacar/i }),
    ).not.toBeInTheDocument();
  });

  it("is a toggle BUTTON, not a checkbox, and says which state it is in", () => {
    const onRowToggleFlag = vi.fn();
    renderList({ onRowToggleFlag });

    const star = within(rowFor("Subject a")).getByRole("button", { name: "Destacar" });
    /*
     * `aria-pressed`, never `aria-checked`. A screen reader announces
     * "pressed" for the first and "checked" for the second — and the row
     * already has a real checkbox two cells to the left whose meaning is
     * entirely different (select, not star). Two "checked" controls in one row
     * meaning different things is the confusion this avoids.
     */
    expect(star).toHaveAttribute("aria-pressed", "false");
  });

  it("names what the CLICK will do, so the icon and the label cannot disagree", () => {
    const flagged = [
      email("a", { keywords: { $flagged: true } }),
      email("b", { keywords: {} }),
    ];
    renderList({ onRowToggleFlag: vi.fn(), groups: groupByThread(flagged) });

    const star = within(rowFor("Subject a")).getByRole("button", {
      name: "Quitar el destacado",
    });
    expect(star).toHaveAttribute("aria-pressed", "true");
  });

  it("acts on ITS OWN row and does NOT also open the message", () => {
    const onRowToggleFlag = vi.fn();
    const handlers = renderList({ onRowToggleFlag });

    const star = within(rowFor("Subject b")).getByRole("button", { name: "Destacar" });
    star.click();

    /*
     * Both halves matter. The first: the pointer already named its target, so
     * the star must not route through the selection. The second: the row's own
     * click handler is one bubble away, and without `stopPropagation` starring
     * a message would also open it — which is what makes a star feel dangerous
     * rather than incidental.
     */
    expect(onRowToggleFlag).toHaveBeenCalledTimes(1);
    expect(onRowToggleFlag.mock.calls[0]?.[0]).toMatchObject({ id: "t-b" });
    expect(handlers.onOpen).not.toHaveBeenCalled();
  });

  it("draws ONE star per row, never the button and the read-only icon together", () => {
    const flagged = [email("a", { keywords: { $flagged: true } })];
    renderList({ onRowToggleFlag: vi.fn(), groups: groupByThread(flagged) });

    // Two stars in one row would read as two different facts about it.
    const row = rowFor("Subject a");
    expect(within(row).getAllByRole("button", { name: /destacado/i })).toHaveLength(1);
  });
});

/**
 * B-09 (canon 07 §3): the conversation's size, beside the sender.
 *
 * Gmail writes "Google 2" and Moov's rows wrote "Google", losing the one signal
 * that says a row is a conversation rather than a message. The mechanism was
 * already here — `groupByThread` computes the size and distinguishes an exact
 * count from a windowed one — so what this pins is that the number REACHES the
 * row, in the right place, saying the right thing about where it came from.
 */
describe("the conversation's size in the row (B-09)", () => {
  /** Two messages of one thread, which is the smallest conversation. */
  const conversation = [
    email("a", { threadId: "t-conv" }),
    email("a2", { threadId: "t-conv", subject: "Subject a" }),
  ];

  it("shows the count beside the sender, after it", () => {
    renderList({ groups: groupByThread(conversation), selectedId: "t-conv" });
    const row = rowFor("Subject a");
    expect(within(row).getByText("2")).toBeInTheDocument();
  });

  it("shows NO count on a single message — a 1 would be noise", () => {
    renderList();
    const row = rowFor("Subject a");
    expect(within(row).queryByText("1")).toBeNull();
  });

  it("says the count is WINDOWED when it was grouped client-side", () => {
    /*
     * The honesty that makes the number trustworthy. With no `Thread` records
     * the size counts only the messages in the fetched window, so a "3" may
     * really mean "3 of maybe 24" — and the tooltip says so rather than
     * claiming a total the client cannot know. A silent lie here would make the
     * whole list untrustworthy for the sake of one digit.
     */
    renderList({ groups: groupByThread(conversation), selectedId: "t-conv" });
    const row = rowFor("Subject a");
    expect(within(row).getByText("2")).toHaveAttribute(
      "title",
      "2 mensajes de esta conversación en estos resultados",
    );
  });

  it("states the count as FACT when the server supplied the thread", () => {
    // With a `Thread` record the size is the thread's real total, so the
    // wording drops the hedge.
    const groups = groupByThread(conversation, [
      { id: "t-conv", emailIds: ["a", "a2", "a3", "a4"] },
    ]);
    renderList({ groups, selectedId: "t-conv" });
    const row = rowFor("Subject a");
    expect(within(row).getByText("4")).toHaveAttribute(
      "title",
      "4 mensajes en esta conversación",
    );
  });

  it("spells the count out for assistive technology as well as drawing it", () => {
    // The visible number is a bare digit next to a name; a screen reader needs
    // the sentence, which the row's visually-hidden state carries.
    renderList({ groups: groupByThread(conversation), selectedId: "t-conv" });
    const row = rowFor("Subject a");
    // The windowed wording, because this group was assembled client-side — the
    // hidden text carries the same hedge the tooltip does rather than a
    // confident sentence beside a cautious one.
    expect(row.textContent).toMatch(/2 mensajes de esta conversación en estos resultados/);
  });
});

/**
 * Review §4.3 — the blue unread dot is gone (owner's decision).
 *
 * It was an addition over Gmail, and B-07 made it redundant: an unread row is
 * already the bold one and the white one punching through the list's tint. The
 * dot said a third time what two signals were already saying, in the same
 * 200 px of row as the star and the label chips.
 *
 * Two assertions, because the risk is on both sides. One: nothing round and
 * accented renders in the sender cell any more — the regression to guard is
 * somebody restoring it as "just a small marker". Two: the state is STILL
 * announced. The dot was `aria-hidden` and never carried the information, but a
 * removal that quietly took the announcement with it would be a real
 * accessibility loss dressed up as a design cleanup.
 */
describe("§4.3: the unread dot is removed", () => {
  it("draws no dot element on an unread row", () => {
    // Row "b" is the unread one — `keywords: {}` means no `$seen`.
    renderList();
    const row = rowFor("Subject b");
    // Asserted through the CSS-module class, which is what the element had:
    // the dot was a bare decorative span with no role, no text and no name, so
    // there is nothing else about it to query by.
    expect(row.querySelector('[class*="unreadDot"]')).toBeNull();
  });

  it("still announces the unread state to a screen reader", () => {
    renderList();
    expect(rowFor("Subject b").textContent).toMatch(/Sin leer/);
    // And says nothing of the sort on the read row, so the assertion above is
    // not passing on a string the list prints unconditionally.
    expect(rowFor("Subject a").textContent).not.toMatch(/Sin leer/);
  });
});
