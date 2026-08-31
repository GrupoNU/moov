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

function renderPanel(query = "", onCreateFilter?: (draft: unknown) => void) {
  const onSubmit = vi.fn();
  const onClose = vi.fn();
  render(
    <I18nProvider locale="es">
      <SearchOptions
        query={query}
        mailboxes={MAILBOXES}
        onSubmit={onSubmit}
        onClose={onClose}
        onCreateFilter={onCreateFilter as never}
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

  it("has NO 'Crear filtro' button when the server offers no Sieve", () => {
    /*
     * E12/B7 landed the button, but only where it can DO something. Without
     * the capability the caller passes no handler, and the button is absent
     * rather than disabled — a control that opens nothing is the dead
     * affordance P4 forbids, and a greyed one still advertises a feature that
     * does not exist here.
     */
    renderPanel();
    expect(screen.queryByRole("button", { name: /crear filtro/i })).toBeNull();
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

/**
 * "Crear filtro" (E12/B7, canon 07 §8).
 *
 * The mapping itself is tested in `mail/searchToFilter.test.ts`. What these
 * cover is the panel's side: that the button only appears where it can act,
 * that it refuses to build a rule with no conditions, and that what a filter
 * cannot carry over is said BEFORE the click rather than discovered after it.
 */
describe("Crear filtro (E12/B7)", () => {
  it("appears when a builder is wired", () => {
    renderPanel("from:boletin@example.com", vi.fn());
    expect(screen.getByRole("button", { name: /crear filtro/i })).toBeInTheDocument();
  });

  it("hands the builder the criteria the panel is showing", async () => {
    const user = userEvent.setup();
    const onCreateFilter = vi.fn();
    renderPanel("from:boletin@example.com subject:Factura", onCreateFilter);

    await user.click(screen.getByRole("button", { name: /crear filtro/i }));

    expect(onCreateFilter).toHaveBeenCalledTimes(1);
    const result = onCreateFilter.mock.calls[0]?.[0] as {
      draft: { from: string[]; subject: string[] };
      usable: boolean;
    };
    expect(result.draft.from).toEqual(["boletin@example.com"]);
    expect(result.draft.subject).toEqual(["Factura"]);
    expect(result.usable).toBe(true);
  });

  it("is DISABLED when nothing would become a rule condition", () => {
    /*
     * A rule with no conditions matches EVERY message. A search of pure free
     * text maps to nothing the filter algebra can express, so offering to
     * build a filter from it would offer to file the whole inbox.
     */
    renderPanel("factura", vi.fn());
    expect(screen.getByRole("button", { name: /crear filtro/i })).toBeDisabled();
  });

  it("says what a filter will NOT carry over, before the click", async () => {
    const user = userEvent.setup();
    renderPanel("from:a@b.com factura", vi.fn());

    /*
     * The free text is dropped — the algebra has no full-text condition — and
     * saying so here is what keeps the user from getting a filter that matches
     * far more mail than the search they built it from, and discovering it
     * weeks later as archived mail they wanted.
     */
    expect(screen.getByRole("status")).toHaveTextContent(/no puede trasladar/i);
    // The button still works: the rule that CAN be built is a real one.
    await user.click(screen.getByRole("button", { name: /crear filtro/i }));
  });

  it("says nothing when the filter matches exactly what the search did", () => {
    renderPanel("from:a@b.com has:attachment", vi.fn());
    expect(screen.queryByText(/no puede trasladar/i)).not.toBeInTheDocument();
  });
});
