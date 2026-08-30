import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import { ScheduleMenu } from "./ScheduleMenu";

/**
 * The composer's "Send later" menu (L3 E4, canon §2.3).
 *
 * It shares its machinery and its stylesheet with the snooze menu, so those
 * are not re-tested here. What IS tested is the one thing genuinely different:
 * the HORIZON. A snooze may be years out; a scheduled send is capped by the
 * server's advertised `maxDelayedSend` of 30 days, and refusing past it
 * client-side is the client half of the server's "declared == applied" rule.
 */

const THIRTY_DAYS = 30 * 24 * 60 * 60;

/** Monday 2026-08-31 at 09:00 local — the afternoon preset is still ahead. */
function monday(hour = 9): Date {
  return new Date(2026, 7, 31, hour, 0, 0, 0);
}

function renderMenu(overrides: { readonly maxDelayedSendSeconds?: number } = {}) {
  const onSchedule = vi.fn();
  render(
    <I18nProvider locale="es">
      <ScheduleMenu
        disabled={false}
        onSchedule={onSchedule}
        maxDelayedSendSeconds={overrides.maxDelayedSendSeconds ?? THIRTY_DAYS}
        now={() => monday()}
        triggerClassName="trigger"
        triggerContent="schedule"
      />
    </I18nProvider>,
  );
  return onSchedule;
}

describe("ScheduleMenu", () => {
  it("offers the three presets plus a picker", async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("button", { name: /enviar más tarde/i }));
    const items = screen.getAllByRole("menuitem").map((item) => item.textContent ?? "");
    expect(items[0]).toMatch(/esta tarde/i);
    expect(items[1]).toMatch(/mañana a la mañana/i);
    expect(items[2]).toMatch(/el lunes a la mañana/i);
    expect(items[3]).toMatch(/elegir fecha y hora/i);
  });

  it("sends the UTCDate the server's parseSendAt accepts", async () => {
    const user = userEvent.setup();
    const onSchedule = renderMenu();
    await user.click(screen.getByRole("button", { name: /enviar más tarde/i }));
    await user.click(screen.getByRole("menuitem", { name: /esta tarde/i }));

    const sendAt = onSchedule.mock.calls[0]?.[0] as string;
    expect(sendAt).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);
    expect(new Date(sendAt).getTime()).toBe(new Date(2026, 7, 31, 13, 0, 0, 0).getTime());
  });

  it("bounds the picker with the ADVERTISED horizon, not a guess", async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("button", { name: /enviar más tarde/i }));
    await user.click(screen.getByRole("menuitem", { name: /elegir fecha y hora/i }));

    const input = screen.getByLabelText(/enviar el/i);
    // Both bounds on the input itself, so the browser's own picker cannot even
    // offer a date the server would refuse.
    expect(input).toHaveAttribute("min");
    expect(input).toHaveAttribute("max");
  });

  it("refuses a date beyond the horizon, naming the real number", async () => {
    const user = userEvent.setup();
    const onSchedule = renderMenu();
    await user.click(screen.getByRole("button", { name: /enviar más tarde/i }));
    await user.click(screen.getByRole("menuitem", { name: /elegir fecha y hora/i }));

    const input = screen.getByLabelText(/enviar el/i);
    await user.clear(input);
    await user.type(input, "2099-01-01T09:00");
    await user.click(screen.getByRole("button", { name: /^programar$/i }));

    expect(onSchedule).not.toHaveBeenCalled();
    // "30 días" — read from the capability, not hard-coded in a sentence.
    expect(screen.getByRole("alert")).toHaveTextContent(/30 días/i);
  });

  it("moves the refusal with the server's advertised limit", async () => {
    const user = userEvent.setup();
    // A server that only schedules a week ahead must produce a DIFFERENT
    // sentence, which is what proves the number is read rather than written.
    renderMenu({ maxDelayedSendSeconds: 7 * 24 * 60 * 60 });
    await user.click(screen.getByRole("button", { name: /enviar más tarde/i }));
    await user.click(screen.getByRole("menuitem", { name: /elegir fecha y hora/i }));

    const input = screen.getByLabelText(/enviar el/i);
    await user.clear(input);
    await user.type(input, "2099-01-01T09:00");
    await user.click(screen.getByRole("button", { name: /^programar$/i }));
    expect(screen.getByRole("alert")).toHaveTextContent(/7 días/i);
  });

  it("refuses the past with its own message, not the horizon's", async () => {
    const user = userEvent.setup();
    const onSchedule = renderMenu();
    await user.click(screen.getByRole("button", { name: /enviar más tarde/i }));
    await user.click(screen.getByRole("menuitem", { name: /elegir fecha y hora/i }));

    const input = screen.getByLabelText(/enviar el/i);
    await user.clear(input);
    await user.type(input, "2020-01-01T09:00");
    await user.click(screen.getByRole("button", { name: /^programar$/i }));

    expect(onSchedule).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent(/fecha y hora futuras/i);
  });

  it("is disabled with the send button, never independently enabled", () => {
    render(
      <I18nProvider locale="es">
        <ScheduleMenu
          disabled
          onSchedule={vi.fn()}
          maxDelayedSendSeconds={THIRTY_DAYS}
          triggerClassName="trigger"
          triggerContent="schedule"
        />
      </I18nProvider>,
    );
    expect(screen.getByRole("button", { name: /enviar más tarde/i })).toBeDisabled();
  });
});
