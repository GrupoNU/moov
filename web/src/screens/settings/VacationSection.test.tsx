import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import { EMPTY_VACATION, type VacationResponse } from "../../mail/filters";
import { localDayEnd, localDayStart } from "../../mail/vacationWindow";
import { VacationSection } from "./VacationSection";

/**
 * The vacation responder form (E6, canon §2.8).
 *
 * The two things worth pinning: the day boundaries the form PRODUCES (Gmail's
 * 12:00 AM / 11:59 PM, computed from the user's own midnight, because the
 * server explicitly delegated them), and that `htmlBody` is never in the patch —
 * a responder whose HTML was configured elsewhere must survive an edit to its
 * text.
 */

function renderSection(
  overrides: Partial<React.ComponentProps<typeof VacationSection>> = {},
) {
  const props = {
    vacation: EMPTY_VACATION,
    onSave: vi.fn().mockResolvedValue(true),
    ...overrides,
  };
  render(
    <I18nProvider locale="es">
      <VacationSection {...props} />
    </I18nProvider>,
  );
  return props;
}

function savedPatch(props: { onSave: unknown }): Record<string, unknown> {
  const mock = props.onSave as ReturnType<typeof vi.fn>;
  return (mock.mock.calls[0]?.[0] ?? {}) as Record<string, unknown>;
}

describe("the dates carry Gmail's day boundaries, in the user's own zone", () => {
  it("sends the first day as LOCAL midnight and the last as local 23:59:59", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.click(screen.getByRole("switch", { name: "Enviar una respuesta automática" }));
    await user.type(screen.getByLabelText("Primer día"), "2026-09-01");
    await user.type(screen.getByLabelText("Último día"), "2026-09-14");
    await user.type(screen.getByLabelText("Asunto"), "Fuera");
    await user.click(screen.getByRole("button", { name: "Guardar" }));

    const patch = savedPatch(props);
    // Compared against the mapper rather than against a literal UTC string, so
    // the assertion holds in whatever zone the suite runs in — which is the
    // very property the mapping exists to provide.
    expect(patch.fromDate).toBe(localDayStart("2026-09-01"));
    expect(patch.toDate).toBe(localDayEnd("2026-09-14"));
  });

  it("states the timezone rule on screen — it is the user's own midnight", () => {
    renderSection();
    expect(screen.getByText(/en tu propia zona horaria/i)).toBeInTheDocument();
  });

  it("sends null for an empty bound, which is the wire's 'no bound'", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.click(screen.getByRole("switch", { name: "Enviar una respuesta automática" }));
    await user.type(screen.getByLabelText("Asunto"), "Fuera");
    await user.click(screen.getByRole("button", { name: "Guardar" }));
    const patch = savedPatch(props);
    expect(patch.fromDate).toBeNull();
    expect(patch.toDate).toBeNull();
  });

  it("shows a stored range back in the form as calendar days", () => {
    renderSection({
      vacation: {
        ...EMPTY_VACATION,
        isEnabled: true,
        fromDate: localDayStart("2026-09-01")!,
        toDate: localDayEnd("2026-09-14")!,
      },
    });
    expect(screen.getByLabelText("Primer día")).toHaveValue("2026-09-01");
    expect(screen.getByLabelText("Último día")).toHaveValue("2026-09-14");
  });
});

describe("validation, before the round trip", () => {
  it("refuses an end before the start", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.type(screen.getByLabelText("Primer día"), "2026-09-10");
    await user.type(screen.getByLabelText("Último día"), "2026-09-01");
    await user.type(screen.getByLabelText("Asunto"), "Fuera");
    await user.click(screen.getByRole("button", { name: "Guardar" }));
    expect(screen.getByRole("alert")).toHaveTextContent("anterior al primero");
    expect(props.onSave).not.toHaveBeenCalled();
  });

  it("refuses an ENABLED responder with neither subject nor body (§8)", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.click(screen.getByRole("switch", { name: "Enviar una respuesta automática" }));
    await user.click(screen.getByRole("button", { name: "Guardar" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Escribí un asunto o un mensaje.");
    expect(props.onSave).not.toHaveBeenCalled();
  });

  it("lets a DISABLED responder be saved empty — nothing is being sent", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.click(screen.getByRole("button", { name: "Guardar" }));
    expect(props.onSave).toHaveBeenCalled();
  });
});

describe("the patch", () => {
  it("NEVER carries htmlBody — a responder's HTML survives a text edit", async () => {
    const user = userEvent.setup();
    const withHtml: VacationResponse = {
      ...EMPTY_VACATION,
      isEnabled: true,
      subject: "Fuera",
      textBody: "Vuelvo el lunes.",
      htmlBody: "<p>Vuelvo el lunes.</p>",
    };
    const props = renderSection({ vacation: withHtml });
    await user.clear(screen.getByLabelText("Mensaje"));
    await user.type(screen.getByLabelText("Mensaje"), "Vuelvo el martes.");
    await user.click(screen.getByRole("button", { name: "Guardar" }));
    expect(savedPatch(props)).not.toHaveProperty("htmlBody");
  });

  it("says so on screen when an HTML body exists, so the edit is not a mystery", () => {
    renderSection({ vacation: { ...EMPTY_VACATION, htmlBody: "<p>hola</p>" } });
    expect(screen.getByText(/deja el HTML intacto/i)).toBeInTheDocument();
  });

  it("clears an emptied field with null, which is how the wire spells 'unset'", async () => {
    const user = userEvent.setup();
    const props = renderSection({
      vacation: { ...EMPTY_VACATION, subject: "Fuera", textBody: "Vuelvo." },
    });
    await user.clear(screen.getByLabelText("Asunto"));
    await user.click(screen.getByRole("button", { name: "Guardar" }));
    expect(savedPatch(props).subject).toBeNull();
  });

  it("reports the outcome rather than assuming it worked", async () => {
    const user = userEvent.setup();
    renderSection({ onSave: vi.fn().mockResolvedValue(false) });
    await user.click(screen.getByRole("button", { name: "Guardar" }));
    expect(
      await screen.findByText("La respuesta automática no se pudo guardar"),
    ).toBeInTheDocument();
  });

  it("saves EXPLICITLY, not on every keystroke", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.type(screen.getByLabelText("Mensaje"), "Vuelvo el lunes.");
    // A half-typed sentence must never be the reply going out on the user's
    // behalf — the same reason the signature row has its own Save button.
    expect(props.onSave).not.toHaveBeenCalled();
  });
});
