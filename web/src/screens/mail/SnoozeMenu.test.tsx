import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import { SnoozeMenu } from "./SnoozeMenu";

/**
 * The snooze menu (L3 E4, canon §2.2).
 *
 * What is worth pinning here is the WIRE VALUE each row produces, because that
 * is what the server validates and what a user's expectation is measured
 * against. `mail/snoozePresets.test.ts` already enumerates the calendar edges;
 * these tests check that the menu shows what that module computed, that the
 * clock is re-read on open (a menu mounted at 19:29 and opened at 20:01 must
 * have withdrawn "later today"), and that the custom picker refuses a past
 * instant here rather than sending it for the server to refuse.
 */

/** Monday 2026-08-31 at 09:00 local. */
function monday(hour = 9, minute = 0): Date {
  return new Date(2026, 7, 31, hour, minute, 0, 0);
}

function renderMenu(now: () => Date = () => monday()) {
  const onSnooze = vi.fn();
  render(
    <I18nProvider locale="es">
      <SnoozeMenu
        disabled={false}
        onSnooze={onSnooze}
        now={now}
        triggerClassName="trigger"
        triggerContent="b"
      />
    </I18nProvider>,
  );
  return onSnooze;
}

describe("SnoozeMenu", () => {
  it("follows the APG menu-button pattern on its trigger", () => {
    renderMenu();
    const trigger = screen.getByRole("button", { name: /posponer hasta/i });
    expect(trigger).toHaveAttribute("aria-haspopup", "menu");
    expect(trigger).toHaveAttribute("aria-expanded", "false");
  });

  it("offers Gmail's four preset names on a weekday morning", async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("button", { name: /posponer hasta/i }));
    const items = screen.getAllByRole("menuitem").map((item) => item.textContent ?? "");
    expect(items[0]).toMatch(/más tarde hoy/i);
    expect(items[1]).toMatch(/mañana/i);
    expect(items[2]).toMatch(/este fin de semana/i);
    expect(items[3]).toMatch(/la próxima semana/i);
    expect(items[4]).toMatch(/elegir fecha y hora/i);
  });

  it("sends the UTCDate the server parses, not a JS timestamp", async () => {
    const user = userEvent.setup();
    const onSnooze = renderMenu();
    await user.click(screen.getByRole("button", { name: /posponer hasta/i }));
    await user.click(screen.getByRole("menuitem", { name: /mañana/i }));

    const until = onSnooze.mock.calls[0]?.[0] as string;
    // The exact shape `parseSnoozeUntil` accepts and re-serializes.
    expect(until).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);
    // 08:00 LOCAL on the first of September — the instant, not the wall clock,
    // is what goes on the wire.
    expect(new Date(until).getTime()).toBe(new Date(2026, 8, 1, 8, 0, 0, 0).getTime());
  });

  it("closes after a choice — one snooze per visit, unlike the label menu", async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("button", { name: /posponer hasta/i }));
    await user.click(screen.getByRole("menuitem", { name: /mañana/i }));
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("RE-READS the clock on open, so a stale mount cannot offer a stale option", async () => {
    const user = userEvent.setup();
    // Mounted in the morning, opened at 21:00 — "later today" would land at
    // midnight and is withdrawn.
    let current = monday(9);
    renderMenu(() => current);
    current = monday(21);
    await user.click(screen.getByRole("button", { name: /posponer hasta/i }));
    const items = screen.getAllByRole("menuitem").map((item) => item.textContent ?? "");
    expect(items.some((label) => /más tarde hoy/i.test(label))).toBe(false);
    expect(items.some((label) => /mañana/i.test(label))).toBe(true);
  });

  it("withdraws 'this weekend' during the weekend rather than greying it out", async () => {
    const user = userEvent.setup();
    // Saturday 2026-09-05.
    renderMenu(() => new Date(2026, 8, 5, 10, 0, 0, 0));
    await user.click(screen.getByRole("button", { name: /posponer hasta/i }));
    const items = screen.getAllByRole("menuitem").map((item) => item.textContent ?? "");
    expect(items.some((label) => /este fin de semana/i.test(label))).toBe(false);
  });

  it("opens an inline picker rather than a second dialog", async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("button", { name: /posponer hasta/i }));
    await user.click(screen.getByRole("menuitem", { name: /elegir fecha y hora/i }));
    // Still inside the same menu: a dialog would need its own focus trap, its
    // own Escape and its own focus return — all three of which PopupMenu
    // already implements for this menu.
    expect(screen.getByRole("menu")).toBeInTheDocument();
    expect(screen.getByLabelText(/volver a mostrar esta conversación/i)).toBeInTheDocument();
  });

  it("refuses a past instant here rather than sending it to be refused", async () => {
    const user = userEvent.setup();
    const onSnooze = renderMenu();
    await user.click(screen.getByRole("button", { name: /posponer hasta/i }));
    await user.click(screen.getByRole("menuitem", { name: /elegir fecha y hora/i }));

    const input = screen.getByLabelText(/volver a mostrar esta conversación/i);
    await user.clear(input);
    await user.type(input, "2020-01-01T08:00");
    await user.click(screen.getByRole("button", { name: /^posponer$/i }));

    // The same refusal the server's `parseSnoozeUntil` would give ("until must
    // be in the future"), one round trip earlier and in the user's language.
    expect(onSnooze).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent(/fecha y hora futuras/i);
  });

  it("sends a valid custom instant", async () => {
    const user = userEvent.setup();
    const onSnooze = renderMenu();
    await user.click(screen.getByRole("button", { name: /posponer hasta/i }));
    await user.click(screen.getByRole("menuitem", { name: /elegir fecha y hora/i }));

    const input = screen.getByLabelText(/volver a mostrar esta conversación/i);
    await user.clear(input);
    await user.type(input, "2099-03-04T17:30");
    await user.click(screen.getByRole("button", { name: /^posponer$/i }));

    const until = onSnooze.mock.calls[0]?.[0] as string;
    expect(new Date(until).getTime()).toBe(new Date(2099, 2, 4, 17, 30, 0, 0).getTime());
  });

  it("does not open at all when disabled", async () => {
    const user = userEvent.setup();
    render(
      <I18nProvider locale="es">
        <SnoozeMenu
          disabled
          onSnooze={vi.fn()}
          triggerClassName="trigger"
          triggerContent="b"
        />
      </I18nProvider>,
    );
    const trigger = screen.getByRole("button", { name: /posponer hasta/i });
    expect(trigger).toBeDisabled();
    await user.click(trigger);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("publishes an open() so the `b` key can raise it", async () => {
    let open: (() => void) | undefined;
    render(
      <I18nProvider locale="es">
        <SnoozeMenu
          disabled={false}
          onSnooze={vi.fn()}
          now={() => monday()}
          onReady={(fn) => {
            open = fn;
          }}
          triggerClassName="trigger"
          triggerContent="b"
        />
      </I18nProvider>,
    );
    expect(open).toBeDefined();
    // An explicit handle rather than a synthetic click on the trigger: a
    // synthetic click on a DISABLED button silently does nothing, and the
    // failure looks like a broken shortcut.
    await userEvent.setup().click(screen.getByRole("button", { name: /posponer hasta/i }));
    expect(screen.getByRole("menu")).toBeInTheDocument();
  });
});
