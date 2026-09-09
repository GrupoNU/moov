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

function renderPanel(
  query = "",
  onCreateFilter?: (draft: unknown) => void,
  currentMailbox?: Mailbox,
) {
  const onSubmit = vi.fn();
  const onClose = vi.fn();
  render(
    <I18nProvider locale="es">
      <SearchOptions
        query={query}
        mailboxes={MAILBOXES}
        currentMailbox={currentMailbox}
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

  it("E-20: SAYS why the exclude field is missing, rather than leaving a hole", () => {
    /*
     * The reasoning has always been in the component's doc comment. The
     * review's point is that a doc comment is not on screen: a user who knows
     * Gmail's panel counts the fields, finds one missing, and concludes either
     * that they mis-remembered or that this is unfinished. Neither is true.
     */
    renderPanel();
    expect(screen.getByRole("note")).toHaveTextContent(/no puede responder/i);
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

/**
 * E-15 — the default scope, named for what it actually is (owner's decision 2,
 * 2026-09-09).
 *
 * The WIRE never scoped a search to a folder: an empty `in:` sends no scope
 * condition and the server applies Gmail's own exclusion of Spam and Trash
 * (`applyDefaultExclusion`). What was wrong was the LABEL — the default option
 * read "En esta carpeta", so a user reading the panel believed every search was
 * folder-scoped when none of them were. A control that misdescribes what it
 * does is worse than a missing one, because the user acts on the description.
 */
describe("E-15 — the scope the panel offers", () => {
  it("names the default as the whole account, not 'this folder'", () => {
    renderPanel();
    const scope = screen.getByLabelText(/buscar en/i);
    // The empty value is the default, and it now says what it does.
    expect(scope).toHaveValue("");
    expect(
      screen.getByRole("option", { name: /todo el correo \(salvo spam/i }),
    ).not.toBeNull();
  });

  it("offers 'En esta carpeta' as a real option when there is a folder on screen", () => {
    renderPanel("", undefined, MAILBOXES[0]);
    expect(screen.getByRole("option", { name: "En esta carpeta" })).not.toBeNull();
  });

  it("has no 'En esta carpeta' when the user is not in a folder", () => {
    // A search route, a label view, the Outbox: there is no "this folder", and
    // offering the scope anyway would be a scope with nothing behind it.
    renderPanel();
    expect(screen.queryByRole("option", { name: "En esta carpeta" })).toBeNull();
  });

  it("SENDS no in: for the default — the account-wide wire shape", async () => {
    const user = userEvent.setup();
    const { onSubmit } = renderPanel("factura");
    await user.click(screen.getByRole("button", { name: /^buscar$/i }));
    // No `in:` at all. The server excludes Spam and Trash itself; a client
    // that sent its own inMailboxOtherThan here would SUPPRESS that default
    // and put Spam back into every search (searchFilter.ts, rule on scope).
    expect(onSubmit).toHaveBeenCalledWith("factura");
  });

  it("SENDS in:<folder> when the user picks this folder — the scoped wire shape", async () => {
    const user = userEvent.setup();
    const { onSubmit } = renderPanel("factura", undefined, MAILBOXES[0]);
    await user.selectOptions(screen.getByLabelText(/buscar en/i), "mb1");
    await user.click(screen.getByRole("button", { name: /^buscar$/i }));
    /*
     * `in:inbox`, not `in:"bandeja de entrada"`. The segment is the ROLE where
     * a folder has one, which is what keeps the query portable: `resolveScope`
     * reads it back by role, so the same string works on a server whose Inbox
     * is named in another language.
     */
    expect(onSubmit).toHaveBeenCalledWith("in:inbox factura");
  });
});

/**
 * E-21 — "within N days OF a date", the half the panel was missing.
 *
 * E3 shipped only the left side of Gmail's pair and said so in a comment: with
 * no anchor the window could only run backwards from NOW, as a `newer_than:`.
 * That made "find the mail from around the launch" — the commonest reason
 * anyone opens this panel — impossible from here. The review logged it as a
 * concrete functional gap, not a cosmetic one.
 */
describe("E-21 — the date anchor", () => {
  it("still emits newer_than: when no anchor is given", async () => {
    const user = userEvent.setup();
    const { onSubmit } = renderPanel();

    await user.type(screen.getByLabelText(/contiene las palabras/i), "informe");
    await user.selectOptions(screen.getByRole("combobox", { name: /fecha dentro de|date within/i }), "7");
    await user.click(screen.getByRole("button", { name: /^buscar$/i }));

    /*
     * "Within a week" with no date does mean "of today", so the old behaviour
     * is right and is kept rather than replaced.
     *
     * It arrives as a single `after:` and not as `newer_than:`, because the
     * panel normalises through the parser on its way out and the parser
     * resolves a relative age to an absolute instant — deliberately, so the
     * server never sees a relative expression it would have to interpret. The
     * assertion is on the SHAPE for that reason: the exact date moves daily.
     */
    const emitted = onSubmit.mock.calls[0]?.[0] as string;
    expect(emitted).toMatch(/^after:\d{4}\/\d{2}\/\d{2} informe$/);
    expect(emitted).not.toContain("before:");
  });

  it("emits a SYMMETRIC after/before pair around the anchor", async () => {
    const user = userEvent.setup();
    const { onSubmit } = renderPanel();

    await user.type(screen.getByLabelText(/contiene las palabras/i), "informe");
    await user.selectOptions(screen.getByRole("combobox", { name: /fecha dentro de|date within/i }), "1");
    await user.type(screen.getByLabelText(/fecha alrededor/i), "2026-03-12");
    await user.click(screen.getByRole("button", { name: /^buscar$/i }));

    /*
     * The 11th through the 13th. `before:` resolves to MIDNIGHT of the day it
     * names, so the upper bound is the 14th — anything else would silently drop
     * the 13th, which is half of what the user asked for.
     */
    expect(onSubmit).toHaveBeenCalledWith("after:2026/03/11 before:2026/03/14 informe");
  });

  it("keeps the anchor inert until a window is chosen", () => {
    renderPanel();
    // An anchor alone says nothing: "of the 12th" is not a date range.
    expect(screen.getByLabelText(/fecha alrededor/i)).toBeDisabled();
  });
});
