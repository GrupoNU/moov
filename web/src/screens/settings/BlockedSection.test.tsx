import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import { EMPTY_RULE, parseFilterRule, type FilterRule } from "../../mail/filters";
import { BlockedSection } from "./BlockedSection";

/**
 * Blocked senders (E6, canon §2.2).
 *
 * The section is one list and one field, so the tests worth having are about
 * the two things that are NOT obvious: that the list is a slice of the rule
 * surface by type (a filter must never appear here, and a block must never
 * appear among the filters), and that the sentence "blocking does not
 * unsubscribe" is on screen — because a user blocking a newsletter is choosing
 * the worse of two remedies.
 */

function rule(overrides: Partial<FilterRule> = {}): FilterRule {
  return { ...parseFilterRule({ ...EMPTY_RULE, id: "r1" })!, ...overrides };
}

function renderSection(
  overrides: Partial<React.ComponentProps<typeof BlockedSection>> = {},
) {
  const props = {
    rules: [] as readonly FilterRule[],
    onBlock: vi.fn(),
    onUnblock: vi.fn(),
    ...overrides,
  };
  render(
    <I18nProvider locale="es">
      <BlockedSection {...props} />
    </I18nProvider>,
  );
  return props;
}

describe("the list is a SLICE of the rule surface", () => {
  it("shows only blocked rules, never filters", () => {
    renderSection({
      rules: [
        rule({ id: "r1", type: "filter", name: "facturas", subject: ["factura"] }),
        rule({ id: "r2", type: "blocked", name: "spam@bad.example", from: ["spam@bad.example"] }),
      ],
    });
    expect(screen.getByText("spam@bad.example")).toBeInTheDocument();
    expect(screen.queryByText("facturas")).not.toBeInTheDocument();
  });

  it("shows the ADDRESS, which is the only thing a blocked rule carries", () => {
    renderSection({
      rules: [rule({ id: "r2", type: "blocked", name: "", from: ["spam@bad.example"] })],
    });
    expect(screen.getByText("spam@bad.example")).toBeInTheDocument();
  });

  it("says so when nobody is blocked", () => {
    renderSection();
    expect(screen.getByText("No bloqueaste a nadie.")).toBeInTheDocument();
  });
});

describe("blocking is not unsubscribing, and the section says so", () => {
  it("states it in the section's own description", () => {
    renderSection();
    expect(
      screen.getByText(/Bloquear no te da de baja de ninguna lista/i),
    ).toBeInTheDocument();
  });

  it("states what blocking DOES — mail goes to Spam", () => {
    renderSection();
    expect(screen.getByText(/va directo a Spam/i)).toBeInTheDocument();
  });
});

describe("adding a block", () => {
  it("sends the blocked-type draft with the normalized address", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.type(screen.getByLabelText("Bloquear una dirección"), "  SPAM@Bad.Example  ");
    await user.click(screen.getByRole("button", { name: "Bloquear una dirección" }));
    expect(props.onBlock).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "blocked",
        from: ["spam@bad.example"],
        name: "spam@bad.example",
      }),
    );
  });

  it("refuses something that is not an address", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.type(screen.getByLabelText("Bloquear una dirección"), "no-arroba");
    await user.click(screen.getByRole("button", { name: "Bloquear una dirección" }));
    expect(props.onBlock).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent("dirección de correo completa");
  });

  it("refuses a duplicate rather than writing a second identical rule", async () => {
    const user = userEvent.setup();
    const props = renderSection({
      rules: [rule({ id: "r2", type: "blocked", from: ["spam@bad.example"] })],
    });
    await user.type(screen.getByLabelText("Bloquear una dirección"), "SPAM@BAD.EXAMPLE");
    await user.click(screen.getByRole("button", { name: "Bloquear una dirección" }));
    expect(props.onBlock).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent("ya está bloqueada");
  });
});

describe("unblocking asks first", () => {
  it("names what happens — the mail comes back", async () => {
    const user = userEvent.setup();
    const props = renderSection({
      rules: [rule({ id: "r2", type: "blocked", from: ["spam@bad.example"] })],
    });
    await user.click(screen.getByRole("button", { name: "Desbloquear" }));
    expect(screen.getByText(/vuelve a tu bandeja de entrada/i)).toBeInTheDocument();
    expect(props.onUnblock).not.toHaveBeenCalled();
  });

  it("unblocks when confirmed", async () => {
    const user = userEvent.setup();
    const props = renderSection({
      rules: [rule({ id: "r2", type: "blocked", from: ["spam@bad.example"] })],
    });
    await user.click(screen.getByRole("button", { name: "Desbloquear" }));
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Desbloquear" }));
    expect(props.onUnblock).toHaveBeenCalledTimes(1);
  });
});
