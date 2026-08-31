import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import type { ForwardAll, ForwardingAddress } from "../../mail/filters";
import { ForwardingSection } from "./ForwardingSection";

/**
 * Forwarding, and specifically its STATE MACHINE (E6, canon §2.11 / GC-4).
 *
 * pending → accepted → in use → removal-refused is the whole feature, and each
 * transition changes which affordance is on screen. The failure these tests
 * exist to prevent is the state being inferred rather than rendered: a code box
 * shown next to a verified address, a forward-all switch offered with nothing
 * verified (which the server would refuse), or a removal that destroys an
 * address a rule still points at without warning anyone first.
 */

function address(
  email: string,
  state: ForwardingAddress["state"],
): ForwardingAddress {
  return {
    id: `f-${email}`,
    email,
    state,
    verifiedAt: state === "accepted" ? "2026-08-01T10:00:00Z" : null,
  };
}

const NO_FORWARD_ALL: ForwardAll = { enabled: false, address: null, disposition: "keep" };

function renderSection(
  overrides: Partial<React.ComponentProps<typeof ForwardingSection>> = {},
) {
  const props = {
    addresses: [] as readonly ForwardingAddress[],
    forwardAll: NO_FORWARD_ALL,
    onAdd: vi.fn().mockResolvedValue(true),
    onVerify: vi.fn().mockResolvedValue(true),
    onRemove: vi.fn(),
    onSaveForwardAll: vi.fn(),
    ...overrides,
  };
  render(
    <I18nProvider locale="es">
      <ForwardingSection {...props} />
    </I18nProvider>,
  );
  return props;
}

describe("adding an address puts it in the PENDING state", () => {
  it("says nothing is registered yet", () => {
    renderSection();
    expect(screen.getByText("Todavía no hay direcciones de reenvío.")).toBeInTheDocument();
  });

  it("registers the address — the server mails the code", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.type(
      screen.getByLabelText("Agregar una dirección de reenvío"),
      "vos@otrolado.com",
    );
    await user.click(screen.getByRole("button", { name: "Agregar una dirección de reenvío" }));
    expect(props.onAdd).toHaveBeenCalledWith("vos@otrolado.com");
  });

  it("refuses something that is not an address, without a round trip", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.type(screen.getByLabelText("Agregar una dirección de reenvío"), "no-arroba");
    await user.click(screen.getByRole("button", { name: "Agregar una dirección de reenvío" }));
    expect(props.onAdd).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent("dirección de correo completa");
  });

  it("reports a failed create — the send is synchronous, so the failure is real", async () => {
    const user = userEvent.setup();
    renderSection({ onAdd: vi.fn().mockResolvedValue(false) });
    await user.type(screen.getByLabelText("Agregar una dirección de reenvío"), "a@b.co");
    await user.click(screen.getByRole("button", { name: "Agregar una dirección de reenvío" }));
    expect(await screen.findByText("No se pudo agregar la dirección")).toBeInTheDocument();
  });

  it("marks a pending address as waiting, and tells the user where the code went", () => {
    renderSection({ addresses: [address("vos@otrolado.com", "pending")] });
    expect(screen.getByText("Esperando el código")).toBeInTheDocument();
    expect(screen.getByText(/Te enviamos un código a vos@otrolado\.com/)).toBeInTheDocument();
  });
});

describe("verification consumes the code", () => {
  it("shows the code box ONLY on a pending address", () => {
    renderSection({
      addresses: [address("ok@dest.com", "accepted"), address("p@dest.com", "pending")],
    });
    // One box, for the one address that needs it.
    expect(screen.getAllByLabelText("Código de verificación")).toHaveLength(1);
  });

  it("gives each pending address its OWN box, so a code cannot land under the wrong one", () => {
    renderSection({
      addresses: [address("a@dest.com", "pending"), address("b@dest.com", "pending")],
    });
    expect(screen.getByRole("form", { name: "Verificar: a@dest.com" })).toBeInTheDocument();
    expect(screen.getByRole("form", { name: "Verificar: b@dest.com" })).toBeInTheDocument();
  });

  it("sends the pasted code", async () => {
    const user = userEvent.setup();
    const props = renderSection({ addresses: [address("p@dest.com", "pending")] });
    await user.type(screen.getByLabelText("Código de verificación"), " abc123 ");
    await user.click(screen.getByRole("button", { name: "Verificar" }));
    // Trimmed: a code relayed by mail arrives with whitespace more often than not.
    expect(props.onVerify).toHaveBeenCalledWith("abc123");
  });

  it("reports the single refusal without pretending to know which reason it was", async () => {
    const user = userEvent.setup();
    renderSection({
      addresses: [address("p@dest.com", "pending")],
      onVerify: vi.fn().mockResolvedValue(false),
    });
    await user.type(screen.getByLabelText("Código de verificación"), "bad");
    await user.click(screen.getByRole("button", { name: "Verificar" }));
    expect(await screen.findByText(/Puede estar mal, vencido, o ser de otra dirección/)).toBeInTheDocument();
  });

  it("shows an accepted address as verified, with its date", () => {
    renderSection({ addresses: [address("ok@dest.com", "accepted")] });
    expect(screen.getByText("Verificada")).toBeInTheDocument();
    expect(screen.getByText(/Verificada el /)).toBeInTheDocument();
    expect(screen.queryByLabelText("Código de verificación")).not.toBeInTheDocument();
  });
});

describe("forward-all needs a verified destination first", () => {
  it("replaces the switch with the precondition when nothing is verified", () => {
    renderSection({ addresses: [address("p@dest.com", "pending")] });
    expect(
      screen.getByText("Agregá y verificá una dirección de destino primero."),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("switch", { name: /Reenviar una copia/ }),
    ).not.toBeInTheDocument();
  });

  it("offers the switch once one is verified, and supplies the address when turning it on", async () => {
    const user = userEvent.setup();
    const props = renderSection({ addresses: [address("ok@dest.com", "accepted")] });
    await user.click(screen.getByRole("switch", { name: "Reenviar una copia de cada mensaje" }));
    // Enabling with no address would be refused by the server, so the only
    // verified destination is supplied with the same write.
    expect(props.onSaveForwardAll).toHaveBeenCalledWith({
      enabled: true,
      address: "ok@dest.com",
    });
  });

  it("offers the canon's TWO dispositions and no others", () => {
    renderSection({
      addresses: [address("ok@dest.com", "accepted")],
      forwardAll: { enabled: true, address: "ok@dest.com", disposition: "keep" },
    });
    const picker = screen.getByLabelText("Conservar la copia de Moov");
    expect(within(picker).getAllByRole("option")).toHaveLength(2);
    expect(within(picker).getByRole("option", { name: "en Recibidos" })).toBeInTheDocument();
    expect(within(picker).getByRole("option", { name: "en Archivo" })).toBeInTheDocument();
  });

  it("saves the archive disposition", async () => {
    const user = userEvent.setup();
    const props = renderSection({
      addresses: [address("ok@dest.com", "accepted")],
      forwardAll: { enabled: true, address: "ok@dest.com", disposition: "keep" },
    });
    await user.selectOptions(screen.getByLabelText("Conservar la copia de Moov"), "archive");
    expect(props.onSaveForwardAll).toHaveBeenCalledWith({ disposition: "archive" });
  });
});

describe("removal warns before it is attempted", () => {
  it("names the consequence for rules that forward there", async () => {
    const user = userEvent.setup();
    const props = renderSection({ addresses: [address("ok@dest.com", "accepted")] });
    await user.click(screen.getByRole("button", { name: "Quitar" }));
    expect(
      screen.getByText(/Cualquier filtro que reenvíe ahí deja de funcionar/),
    ).toBeInTheDocument();
    expect(props.onRemove).not.toHaveBeenCalled();
  });

  it("removes when confirmed", async () => {
    const user = userEvent.setup();
    const props = renderSection({ addresses: [address("ok@dest.com", "accepted")] });
    await user.click(screen.getByRole("button", { name: "Quitar" }));
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Quitar" }));
    expect(props.onRemove).toHaveBeenCalledTimes(1);
  });

  it("surfaces the server's own refusal sentence, which names the fix", () => {
    // The in-use refusal is the server's to word: only it knows whether a
    // filter or the forward-all setting is the blocker.
    renderSection({
      addresses: [address("ok@dest.com", "accepted")],
      error: "the address is still used by a filter or the forwarding setting; remove that first",
    });
    expect(screen.getByRole("alert")).toHaveTextContent("remove that first");
  });
});
