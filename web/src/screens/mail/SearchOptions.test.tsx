import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import { SearchOptions } from "./SearchOptions";
import type { Mailbox } from "../../mail/types";

/**
 * The search options panel (L3 epic E3).
 *
 * Two of these tests assert an ABSENCE, and they are the most important ones
 * here: "Doesn't have the words" and "Create filter" are Gmail fields this
 * panel deliberately does not ship, each for a stated reason. A test is what
 * keeps a future contributor from "completing" the panel by adding a control
 * that can only produce an error.
 */

function mailbox(id: string, name: string, role: Mailbox["role"] = null): Mailbox {
  return {
    id,
    name,
    parentId: null,
    role,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
    isSubscribed: true,
    myRights: {
      mayReadItems: true,
      mayAddItems: true,
      mayRemoveItems: true,
      maySetSeen: true,
      maySetKeywords: true,
      mayCreateChild: true,
      mayRename: true,
      mayDelete: true,
      maySubmit: true,
    },
  };
}

const MAILBOXES: readonly Mailbox[] = [
  mailbox("mb1", "Bandeja de entrada", "inbox"),
  mailbox("mb5", "Proyectos"),
];

function renderPanel(query = "") {
  const onSubmit = vi.fn();
  const onClose = vi.fn();
  render(
    <I18nProvider locale="es">
      <SearchOptions
        query={query}
        mailboxes={MAILBOXES}
        onSubmit={onSubmit}
        onClose={onClose}
      />
    </I18nProvider>,
  );
  return { onSubmit, onClose };
}

describe("the deliberate absences (P4: no dead controls)", () => {
  it("has NO 'Doesn't have the words' field — the server refuses NOT", () => {
    /*
     * `query.go` translateOperator: NOT's result is the complement of a match
     * set, "which no index in this store can produce". A field for it could
     * only ever produce an error, so it is omitted entirely rather than
     * disabled — a greyed-out field still advertises a feature that does not
     * exist.
     */
    renderPanel();
    expect(screen.queryByText(/no contiene|doesn't have/i)).not.toBeInTheDocument();
  });

  it("has NO 'Create filter' button — that arrives with E6 (Sieve)", () => {
    renderPanel();
    expect(screen.queryByRole("button", { name: /crear filtro|create filter/i })).toBeNull();
    // And there is no disabled button standing in for it either.
    for (const button of screen.getAllByRole("button")) {
      expect(button).not.toBeDisabled();
    }
  });
});

describe("composing a query", () => {
  it("builds a query string from the fields, not a filter", async () => {
    const user = userEvent.setup();
    const { onSubmit } = renderPanel();

    await user.type(screen.getByLabelText(/^De$/i), "ana");
    await user.type(screen.getByLabelText(/asunto/i), "informe");
    await user.click(screen.getByRole("button", { name: /^buscar$/i }));

    // The panel emits GRAMMAR, which the box then parses like any typed query.
    expect(onSubmit).toHaveBeenCalledWith("from:ana subject:informe");
  });

  it("quotes a multi-word value so it survives re-parsing", async () => {
    const user = userEvent.setup();
    const { onSubmit } = renderPanel();

    await user.type(screen.getByLabelText(/asunto/i), "informe trimestral");
    await user.click(screen.getByRole("button", { name: /^buscar$/i }));

    expect(onSubmit).toHaveBeenCalledWith('subject:"informe trimestral"');
  });

  it("emits in:anywhere for the All mail scope — the server's escape hatch", async () => {
    const user = userEvent.setup();
    const { onSubmit } = renderPanel();

    await user.type(screen.getByLabelText(/contiene las palabras/i), "informe");
    await user.selectOptions(screen.getByLabelText(/buscar en/i), "anywhere");
    await user.click(screen.getByRole("button", { name: /^buscar$/i }));

    expect(onSubmit).toHaveBeenCalledWith("in:anywhere informe");
  });

  it("emits NO in: for the default scope, so the server applies its own exclusion", async () => {
    const user = userEvent.setup();
    const { onSubmit } = renderPanel();

    await user.type(screen.getByLabelText(/contiene las palabras/i), "informe");
    await user.click(screen.getByRole("button", { name: /^buscar$/i }));

    expect(onSubmit).toHaveBeenCalledWith("informe");
  });

  it("composes has:attachment from the checkbox", async () => {
    const user = userEvent.setup();
    const { onSubmit } = renderPanel();

    await user.type(screen.getByLabelText(/contiene las palabras/i), "informe");
    await user.click(screen.getByLabelText(/tiene adjunto/i));
    await user.click(screen.getByRole("button", { name: /^buscar$/i }));

    expect(onSubmit).toHaveBeenCalledWith("has:attachment informe");
  });

  it("seeds itself from the query already in the box, so it never lies", () => {
    renderPanel("from:ana subject:informe has:attachment");
    expect(screen.getByLabelText(/^De$/i)).toHaveValue("ana");
    expect(screen.getByLabelText(/asunto/i)).toHaveValue("informe");
    expect(screen.getByLabelText(/tiene adjunto/i)).toBeChecked();
  });

  it("closes on Escape without searching", async () => {
    const user = userEvent.setup();
    const { onSubmit, onClose } = renderPanel();
    await user.type(screen.getByLabelText(/^De$/i), "ana{Escape}");
    expect(onClose).toHaveBeenCalled();
    expect(onSubmit).not.toHaveBeenCalled();
  });
});
