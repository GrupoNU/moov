import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import type { Email } from "../../mail/types";
import { RecipientSummary } from "./RecipientSummary";

/**
 * "para mí ▾" (C-14): the phrase, and the headers behind the caret.
 */

const EMAIL: Email = {
  id: "m1",
  subject: "Pago factura mayo",
  from: [{ name: "Juan López", email: "juan@claro.example" }],
  to: [{ name: null, email: "diego@gruponu.com" }],
  cc: [{ name: "Ana", email: "ana@x" }],
  replyTo: [{ name: null, email: "cobranzas@claro.example" }],
  receivedAt: "2026-06-29T13:23:00Z",
};

function renderSummary(email: Email = EMAIL, own: readonly string[] = ["diego@gruponu.com"]) {
  render(
    <I18nProvider locale="es">
      <RecipientSummary email={email} ownAddresses={own} />
    </I18nProvider>,
  );
}

describe("the phrase", () => {
  it("says 'para mí, Ana' and nothing else until asked", () => {
    renderSummary();
    const toggle = screen.getByRole("button", { name: /^para mí, ana$/i });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("juan@claro.example", { exact: false })).not.toBeInTheDocument();
  });

  it("names the others when the reader is not a recipient", () => {
    renderSummary(EMAIL, []);
    expect(screen.getByRole("button", { name: /^para diego@gruponu\.com, ana$/i })).toBeInTheDocument();
  });

  it("renders nothing for a message with no recipients", () => {
    renderSummary({ id: "m2", to: null, cc: null });
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});

describe("the caret", () => {
  it("reveals the full headers, and hides them again", async () => {
    const user = userEvent.setup();
    renderSummary();
    await user.click(screen.getByRole("button", { name: /^para mí/i }));

    const toggle = screen.getByRole("button", { name: /^para mí/i });
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Juan López <juan@claro.example>")).toBeInTheDocument();
    expect(screen.getByText("diego@gruponu.com")).toBeInTheDocument();
    expect(screen.getByText("Ana <ana@x>")).toBeInTheDocument();
    expect(screen.getByText("cobranzas@claro.example")).toBeInTheDocument();
    expect(screen.getByText("Pago factura mayo")).toBeInTheDocument();
    // The labels are the reader's own words, as a description list.
    expect(screen.getByText("De")).toBeInTheDocument();
    expect(screen.getByText("Responder a")).toBeInTheDocument();
    expect(screen.getByText("Fecha")).toBeInTheDocument();

    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Juan López <juan@claro.example>")).not.toBeInTheDocument();
  });

  it("omits header rows the message does not carry", async () => {
    const user = userEvent.setup();
    renderSummary({ id: "m3", to: [{ name: "Ana", email: "ana@x" }] }, []);
    await user.click(screen.getByRole("button", { name: /^para ana$/i }));
    expect(screen.queryByText("Cc")).not.toBeInTheDocument();
    expect(screen.queryByText("Cco")).not.toBeInTheDocument();
    expect(screen.queryByText("Responder a")).not.toBeInTheDocument();
  });
});
