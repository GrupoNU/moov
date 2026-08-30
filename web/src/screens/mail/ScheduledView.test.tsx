import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import type { ScheduledSend } from "../../mail/scheduled";
import { ScheduledView } from "./ScheduledView";

/**
 * The Scheduled view (L3 E4, canon §2.3).
 *
 * The three properties worth pinning: each row names its own submission (a
 * cancel that hit the wrong one would un-schedule a message the user never
 * touched), the timestamp is machine-readable, and the wording tells the truth
 * about what cancelling does — the message reverts to a DRAFT, which is the
 * canon's own promise and the reason the server suppresses the implicit
 * Email/set for a scheduled submission.
 */

const ITEMS: readonly ScheduledSend[] = [
  {
    id: "s1",
    emailId: "e1",
    sendAt: "2026-09-04T08:00:00Z",
    subject: "Propuesta",
    recipients: ["Ana <ana@x.test>"],
  },
  {
    id: "s2",
    emailId: "e2",
    sendAt: "2026-09-07T13:00:00Z",
    subject: "",
    recipients: [],
  },
];

function renderView(overrides: Partial<Parameters<typeof ScheduledView>[0]> = {}) {
  const onCancel = vi.fn();
  const onSendNow = vi.fn();
  render(
    <I18nProvider locale="es">
      <ScheduledView
        items={ITEMS}
        onCancel={onCancel}
        onSendNow={onSendNow}
        busyId={undefined}
        locale="es"
        {...overrides}
      />
    </I18nProvider>,
  );
  return { onCancel, onSendNow };
}

function rowFor(text: string): HTMLElement {
  const item = screen
    .getAllByRole("listitem")
    .find((candidate) => within(candidate).queryByText(text) !== null);
  if (item === undefined) throw new Error(`no row for ${text}`);
  return item;
}

describe("ScheduledView", () => {
  it("says the messages are still drafts, which is what cancelling relies on", () => {
    renderView();
    // Canon §2.3: "cancel reverts to draft". A view that did not say so would
    // leave a user who cancelled hunting for a message they think is gone.
    expect(screen.getByText(/siguen siendo borradores/i)).toBeInTheDocument();
  });

  it("renders the send time as a machine-readable instant", () => {
    renderView();
    const when = within(rowFor("Propuesta")).getByText(/sept/i);
    expect(when.tagName).toBe("TIME");
    expect(when).toHaveAttribute("datetime", "2026-09-04T08:00:00Z");
  });

  it("names a message with no subject rather than rendering a blank row", () => {
    renderView();
    expect(screen.getByText(/sin asunto/i)).toBeInTheDocument();
    expect(screen.getByText(/sin destinatarios/i)).toBeInTheDocument();
  });

  it("cancels the submission the row is FOR, not the first one", async () => {
    const user = userEvent.setup();
    const { onCancel } = renderView();
    await user.click(
      within(rowFor("Propuesta")).getByRole("button", { name: /cancelar envío/i }),
    );
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onCancel.mock.calls[0]?.[0]).toMatchObject({ id: "s1" });
  });

  it("sends the row's own message now", async () => {
    const user = userEvent.setup();
    const { onSendNow } = renderView();
    await user.click(
      within(rowFor("Propuesta")).getByRole("button", { name: /enviar ahora/i }),
    );
    expect(onSendNow.mock.calls[0]?.[0]).toMatchObject({ id: "s1", emailId: "e1" });
  });

  it("disables ONLY the row with an operation in flight", () => {
    renderView({ busyId: "s1" });
    const busy = rowFor("Propuesta");
    expect(within(busy).getByRole("button", { name: /cancelar envío/i })).toBeDisabled();
    // A second scheduled send is unrelated and must stay actionable.
    const other = rowFor("(sin destinatarios)");
    expect(within(other).getByRole("button", { name: /cancelar envío/i })).toBeEnabled();
  });

  it("has its own empty state rather than an empty list", () => {
    renderView({ items: [] });
    expect(screen.getByText(/no hay nada programado/i)).toBeInTheDocument();
    expect(screen.queryByRole("listitem")).toBeNull();
  });

  it("falls back to the raw value for an unparseable timestamp, never 'Invalid Date'", () => {
    renderView({
      items: [{ id: "s9", emailId: "e9", sendAt: "later", subject: "Raro", recipients: [] }],
    });
    expect(within(rowFor("Raro")).getByText("later")).toBeInTheDocument();
  });
});
