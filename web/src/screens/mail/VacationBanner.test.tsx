import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import { EMPTY_VACATION, type VacationResponse } from "../../mail/filters";
import { VacationBanner } from "./VacationBanner";

/**
 * The vacation banner (E6, canon §2.8).
 *
 * The acceptance criterion is the WINDOW, not the switch: a responder enabled
 * with a range starting next Monday is not responding today, so a banner that
 * fired on `isEnabled` alone would be the UI disagreeing with what Dovecot is
 * actually doing. Everything else here is one button.
 */

const NOW = new Date("2026-09-05T12:00:00Z");

function renderBanner(
  vacation: Partial<VacationResponse>,
  onEndNow = vi.fn().mockResolvedValue(true),
) {
  render(
    <I18nProvider locale="es">
      <VacationBanner
        vacation={{ ...EMPTY_VACATION, ...vacation }}
        onEndNow={onEndNow}
        now={NOW}
      />
    </I18nProvider>,
  );
  return onEndNow;
}

describe("visibility follows the WINDOW, not just the switch", () => {
  it("renders nothing when the responder is off", () => {
    renderBanner({ isEnabled: false });
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("shows while enabled with no bounds", () => {
    renderBanner({ isEnabled: true, subject: "Fuera" });
    expect(screen.getByRole("status")).toHaveTextContent(
      "Tu respuesta automática está activa",
    );
  });

  it("shows inside the window", () => {
    renderBanner({
      isEnabled: true,
      fromDate: "2026-09-01T00:00:00Z",
      toDate: "2026-09-10T23:59:59Z",
    });
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("stays HIDDEN before the window — enabled is not the same as responding", () => {
    renderBanner({ isEnabled: true, fromDate: "2026-09-10T00:00:00Z" });
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("stays hidden after the window", () => {
    renderBanner({ isEnabled: true, toDate: "2026-09-01T23:59:59Z" });
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("names the end date when there is one — that is what makes it information", () => {
    renderBanner({ isEnabled: true, toDate: "2026-09-10T23:59:59Z" });
    expect(screen.getByRole("status")).toHaveTextContent(/activa hasta el/);
  });
});

describe('"Finalizar ahora" — Gmail\'s own control', () => {
  it("turns the responder off", async () => {
    const user = userEvent.setup();
    const onEndNow = renderBanner({ isEnabled: true, subject: "Fuera" });
    await user.click(screen.getByRole("button", { name: "Finalizar ahora" }));
    expect(onEndNow).toHaveBeenCalledTimes(1);
  });

  it("reports a failed turn-off rather than pretending it worked", async () => {
    const user = userEvent.setup();
    renderBanner({ isEnabled: true, subject: "Fuera" }, vi.fn().mockResolvedValue(false));
    await user.click(screen.getByRole("button", { name: "Finalizar ahora" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "No se pudo apagar la respuesta automática",
    );
  });

  it("is a status and not an alert — it is a standing condition, not an event", () => {
    // An alert would re-interrupt a screen reader on every re-render of the
    // mail list, which is constant.
    renderBanner({ isEnabled: true, subject: "Fuera" });
    expect(screen.getByRole("status")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
