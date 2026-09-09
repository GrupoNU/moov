import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import {
  EMPTY_RULE,
  exportFilters,
  parseFilterRule,
  type FilterRule,
  type FilterRuleDraft,
  type ForwardingAddress,
} from "../../mail/filters";
import type { Label } from "../../mail/labelStore";
import type { Mailbox } from "../../mail/types";
import { FiltersSection } from "./FiltersSection";

/**
 * The filter manager (E6, GC-4).
 *
 * The tests that carry weight here are the ones about the epic's product
 * requirements rather than about CRUD: the builder produces the vendor wire
 * shape exactly (a round trip through the form must not change a rule), the
 * `scriptActive: false` banner appears and says the right thing, the forward
 * picker offers ONLY verified addresses, and the conditions GC-4 removed are
 * genuinely absent rather than disabled.
 */

function rule(overrides: Partial<FilterRule> = {}): FilterRule {
  return { ...parseFilterRule({ ...EMPTY_RULE, id: "r1" })!, ...overrides };
}

function mailbox(id: string, name: string): Mailbox {
  return {
    id,
    name,
    parentId: null,
    role: null,
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

function address(email: string, state: ForwardingAddress["state"]): ForwardingAddress {
  return { id: `f-${email}`, email, state, verifiedAt: state === "accepted" ? "2026-08-01T00:00:00Z" : null };
}

const LABELS: readonly Label[] = [
  { keyword: "$label:work", name: "work", colorId: "slate", visibility: "show" },
];

function renderSection(
  overrides: Partial<React.ComponentProps<typeof FiltersSection>> = {},
) {
  const props = {
    rules: [rule({ id: "r1", name: "facturas", subject: ["factura"], star: true })],
    scriptActive: true,
    forwardingAddresses: [] as readonly ForwardingAddress[],
    mailboxes: [mailbox("mb1", "Facturas")],
    labels: LABELS,
    onCreate: vi.fn(),
    onUpdate: vi.fn(),
    onDelete: vi.fn(),
    onMove: vi.fn(),
    onActivate: vi.fn(),
    onImport: vi.fn() as ((rules: readonly FilterRuleDraft[]) => void) | undefined,
    ...overrides,
  };
  render(
    <I18nProvider locale="es">
      <FiltersSection {...props} />
    </I18nProvider>,
  );
  return props;
}

describe("the scriptActive banner — the honesty bit made visible", () => {
  it("shows nothing when Moov's script IS the active one", () => {
    renderSection({ scriptActive: true });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("says the rules are NOT running when a foreign script is active", () => {
    renderSection({ scriptActive: false });
    const banner = screen.getByRole("alert");
    expect(within(banner).getByText(/no se están ejecutando/i)).toBeInTheDocument();
    expect(within(banner).getByText(/otro script Sieve activo en el servidor/i)).toBeInTheDocument();
  });

  it("states the preservation guarantee — the fear the button has to answer", () => {
    renderSection({ scriptActive: false });
    // "What happens to my other script" is the first question, and the answer
    // is that the server never destroys foreign content.
    expect(screen.getByText(/nunca borra un script que no escribió/i)).toBeInTheDocument();
  });

  it("offers the activation, and calls it", async () => {
    const user = userEvent.setup();
    const props = renderSection({ scriptActive: false });
    await user.click(screen.getByRole("button", { name: "Activar las reglas de Moov" }));
    expect(props.onActivate).toHaveBeenCalledTimes(1);
  });

  it("keeps the EXPLANATION but drops the button when the server has no Sieve capability", () => {
    // The situation is still true; only the remedy is unavailable. Hiding the
    // banner would leave a user with silently dead rules.
    renderSection({ scriptActive: false, onActivate: undefined });
    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Activar las reglas de Moov" }),
    ).not.toBeInTheDocument();
  });
});

describe("the list states ORDER, because Sieve is sequential", () => {
  it("numbers each rule and says how many there are", () => {
    renderSection({
      rules: [
        rule({ id: "r1", name: "uno", subject: ["a"], star: true }),
        rule({ id: "r2", name: "dos", subject: ["b"], star: true }),
      ],
    });
    expect(screen.getByText("Regla 1 de 2")).toBeInTheDocument();
    expect(screen.getByText("Regla 2 de 2")).toBeInTheDocument();
  });

  it("disables 'up' on the first rule and 'down' on the last", () => {
    renderSection({
      rules: [
        rule({ id: "r1", name: "uno", subject: ["a"], star: true }),
        rule({ id: "r2", name: "dos", subject: ["b"], star: true }),
      ],
    });
    expect(screen.getByRole("button", { name: "Subir: uno" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Bajar: dos" })).toBeDisabled();
  });

  it("moves a rule", async () => {
    const user = userEvent.setup();
    const props = renderSection({
      rules: [
        rule({ id: "r1", name: "uno", subject: ["a"], star: true }),
        rule({ id: "r2", name: "dos", subject: ["b"], star: true }),
      ],
    });
    await user.click(screen.getByRole("button", { name: "Subir: dos" }));
    expect(props.onMove).toHaveBeenCalledWith("r2", "up");
  });

  it("summarises the conditions and the actions on the row", () => {
    renderSection({
      rules: [
        rule({ id: "r1", name: "facturas", from: ["a@b.co"], moveTo: "Facturas", star: true }),
      ],
    });
    expect(screen.getByText(/De: a@b\.co/)).toBeInTheDocument();
    expect(screen.getByText(/Mover a la carpeta: Facturas/)).toBeInTheDocument();
  });

  it("leaves BLOCKED rules to their own section — they have no visible action", () => {
    renderSection({
      rules: [
        rule({ id: "r1", name: "facturas", subject: ["a"], star: true }),
        rule({ id: "r2", type: "blocked", name: "spam@bad.example", from: ["spam@bad.example"] }),
      ],
    });
    expect(screen.getByText("facturas")).toBeInTheDocument();
    expect(screen.queryByText("spam@bad.example")).not.toBeInTheDocument();
    expect(screen.getByText("Regla 1 de 1")).toBeInTheDocument();
  });

  it("says so when there are no filters", () => {
    renderSection({ rules: [] });
    expect(screen.getByText("Todavía no hay filtros.")).toBeInTheDocument();
  });
});

describe("the builder round-trips the vendor wire shape", () => {
  it("builds a rule from the form, in the exact field names the server reads", async () => {
    const user = userEvent.setup();
    const props = renderSection({ rules: [] });
    await user.click(screen.getByRole("button", { name: "Crear un filtro" }));

    await user.type(screen.getByLabelText("Nombre"), "facturas");
    await user.type(screen.getByLabelText("De"), "contabilidad@proveedor.com");
    await user.type(screen.getByLabelText("Asunto"), "factura");
    await user.selectOptions(screen.getByLabelText("Adjunto"), "yes");
    await user.selectOptions(screen.getByLabelText("Mover a la carpeta"), "Facturas");
    await user.click(screen.getByRole("checkbox", { name: "Destacarlo" }));
    await user.click(screen.getByRole("button", { name: "Guardar" }));

    expect(props.onCreate).toHaveBeenCalledWith({
      name: "facturas",
      type: "filter",
      enabled: true,
      from: ["contabilidad@proveedor.com"],
      to: [],
      subject: ["factura"],
      sizeOver: 0,
      sizeUnder: 0,
      hasAttachment: true,
      moveTo: "Facturas",
      labels: [],
      markRead: false,
      star: true,
      forward: "",
      delete: false,
      stop: false,
    });
  });

  it("loads an existing rule into the form and returns it unchanged", async () => {
    const user = userEvent.setup();
    const existing = rule({
      id: "r1",
      name: "facturas",
      from: ["a@b.co"],
      subject: ["factura"],
      sizeOver: 2048,
      hasAttachment: false,
      moveTo: "Facturas",
      labels: ["work"],
      markRead: true,
      star: true,
      stop: true,
    });
    const props = renderSection({ rules: [existing] });
    await user.click(screen.getByRole("button", { name: "Editar" }));
    await user.click(screen.getByRole("button", { name: "Guardar" }));

    // A round trip through the form must be the identity on the wire shape:
    // anything else silently rewrites a rule the user only opened to look at.
    const { id: _id, ...draft } = existing;
    expect(props.onUpdate).toHaveBeenCalledWith("r1", draft);
  });

  it("splits a multi-line field into the wire's array, dropping blanks", async () => {
    const user = userEvent.setup();
    const props = renderSection({ rules: [] });
    await user.click(screen.getByRole("button", { name: "Crear un filtro" }));
    await user.type(screen.getByLabelText("De"), "a@b.co{enter}{enter}c@d.co");
    await user.click(screen.getByRole("checkbox", { name: "Destacarlo" }));
    await user.click(screen.getByRole("button", { name: "Guardar" }));

    const draft = (props.onCreate as ReturnType<typeof vi.fn>).mock.calls[0]?.[0] as {
      from: string[];
    };
    expect(draft.from).toEqual(["a@b.co", "c@d.co"]);
  });

  it("converts a size in the chosen unit into bytes", async () => {
    const user = userEvent.setup();
    const props = renderSection({ rules: [] });
    await user.click(screen.getByRole("button", { name: "Crear un filtro" }));
    await user.type(screen.getByLabelText("Más grande que"), "5");
    await user.selectOptions(
      screen.getByLabelText("Más grande que / Más chico que"),
      "MB",
    );
    await user.click(screen.getByRole("checkbox", { name: "Destacarlo" }));
    await user.click(screen.getByRole("button", { name: "Guardar" }));

    const draft = (props.onCreate as ReturnType<typeof vi.fn>).mock.calls[0]?.[0] as {
      sizeOver: number;
    };
    expect(draft.sizeOver).toBe(5 * 1024 * 1024);
  });

  it("sends type neverSpam when the never-spam action is ticked", async () => {
    const user = userEvent.setup();
    const props = renderSection({ rules: [] });
    await user.click(screen.getByRole("button", { name: "Crear un filtro" }));
    await user.type(screen.getByLabelText("De"), "banco@ok.example");
    await user.click(screen.getByRole("checkbox", { name: "Nunca marcarlo como spam" }));
    await user.click(screen.getByRole("button", { name: "Guardar" }));

    const draft = (props.onCreate as ReturnType<typeof vi.fn>).mock.calls[0]?.[0] as {
      type: string;
    };
    expect(draft.type).toBe("neverSpam");
  });
});

describe("GC-4's restrictions are ABSENT, not disabled", () => {
  it("offers no date condition", async () => {
    const user = userEvent.setup();
    renderSection({ rules: [] });
    await user.click(screen.getByRole("button", { name: "Crear un filtro" }));
    // Not "present but disabled": Gmail has no date criterion either, and a
    // greyed-out field is the dead control P4 forbids.
    expect(screen.queryByLabelText(/fecha/i)).not.toBeInTheDocument();
  });

  it("names the restriction instead of hiding it", async () => {
    const user = userEvent.setup();
    renderSection({ rules: [] });
    await user.click(screen.getByRole("button", { name: "Crear un filtro" }));
    expect(
      screen.getByText(/No hay condición por fecha ni condición de búsqueda libre/i),
    ).toBeInTheDocument();
  });
});

describe("the forward picker offers only VERIFIED addresses", () => {
  it("replaces the picker with the hint when nothing is verified", async () => {
    const user = userEvent.setup();
    renderSection({ rules: [], forwardingAddresses: [address("p@dest.com", "pending")] });
    await user.click(screen.getByRole("button", { name: "Crear un filtro" }));
    expect(screen.queryByLabelText("Reenviar a")).not.toBeInTheDocument();
    expect(screen.getByText(/Solo las direcciones verificadas/i)).toBeInTheDocument();
  });

  it("lists the accepted ones and NOT the pending ones", async () => {
    const user = userEvent.setup();
    renderSection({
      rules: [],
      forwardingAddresses: [address("ok@dest.com", "accepted"), address("p@dest.com", "pending")],
    });
    await user.click(screen.getByRole("button", { name: "Crear un filtro" }));
    const picker = screen.getByLabelText("Reenviar a");
    expect(within(picker).getByRole("option", { name: "ok@dest.com" })).toBeInTheDocument();
    expect(within(picker).queryByRole("option", { name: "p@dest.com" })).not.toBeInTheDocument();
  });
});

describe("the builder refuses before the round trip", () => {
  it("refuses a filter with no condition", async () => {
    const user = userEvent.setup();
    const props = renderSection({ rules: [] });
    await user.click(screen.getByRole("button", { name: "Crear un filtro" }));
    await user.click(screen.getByRole("checkbox", { name: "Destacarlo" }));
    await user.click(screen.getByRole("button", { name: "Guardar" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Agregá al menos una condición.");
    expect(props.onCreate).not.toHaveBeenCalled();
  });

  it("refuses a filter with no action", async () => {
    const user = userEvent.setup();
    const props = renderSection({ rules: [] });
    await user.click(screen.getByRole("button", { name: "Crear un filtro" }));
    await user.type(screen.getByLabelText("Asunto"), "factura");
    await user.click(screen.getByRole("button", { name: "Guardar" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Agregá al menos una acción.");
    expect(props.onCreate).not.toHaveBeenCalled();
  });

  it("refuses moving AND trashing the same message", async () => {
    const user = userEvent.setup();
    renderSection({ rules: [] });
    await user.click(screen.getByRole("button", { name: "Crear un filtro" }));
    await user.type(screen.getByLabelText("Asunto"), "factura");
    await user.selectOptions(screen.getByLabelText("Mover a la carpeta"), "Facturas");
    await user.click(screen.getByRole("checkbox", { name: "Mover a la papelera" }));
    await user.click(screen.getByRole("button", { name: "Guardar" }));
    expect(screen.getByRole("alert")).toHaveTextContent(/no las dos cosas/);
  });
});

describe("deleting a filter asks first", () => {
  it("confirms with our own dialog, and says what survives", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.click(screen.getByRole("button", { name: "Eliminar" }));
    expect(
      screen.getByText(/El correo ya archivado queda donde está/i),
    ).toBeInTheDocument();
    expect(props.onDelete).not.toHaveBeenCalled();
  });

  it("deletes when confirmed", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.click(screen.getByRole("button", { name: "Eliminar" }));
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Eliminar" }));
    expect(props.onDelete).toHaveBeenCalledTimes(1);
  });
});

/**
 * Import and export (F-42).
 *
 * The download itself is a Blob and an `<a>.click()` that jsdom cannot follow,
 * so what these cover is everything on THIS side of it: that both affordances
 * are there, that the format note is on screen at the moment the user decides
 * what to do with the file, that a good file reaches the caller as parsed
 * drafts, and — the one that matters — that a bad file produces a named reason
 * rather than silence.
 */
describe("importing and exporting (F-42)", () => {
  /** A file the browser's `File.text()` will read back. */
  function file(name: string, text: string): File {
    return new File([text], name, { type: "application/json" });
  }

  it("offers both, and says whose format the file is", () => {
    renderSection();

    expect(screen.getByRole("button", { name: /exportar filtros/i })).toBeEnabled();
    expect(screen.getByRole("button", { name: /importar filtros/i })).toBeEnabled();
    // Naming it after Gmail's XML would send the user to Gmail with something
    // Gmail cannot read.
    expect(screen.getByText(/JSON propio de Moov, no el XML de Gmail/i)).toBeInTheDocument();
  });

  it("cannot export nothing", () => {
    renderSection({ rules: [] });
    expect(screen.getByRole("button", { name: /exportar filtros/i })).toBeDisabled();
  });

  it("hides the import when the caller has no way to write", () => {
    renderSection({ onImport: undefined });
    expect(screen.queryByRole("button", { name: /importar filtros/i })).not.toBeInTheDocument();
  });

  it("hands the caller the parsed drafts of a good file", async () => {
    const user = userEvent.setup();
    const onImport = vi.fn();
    renderSection({ onImport });

    const doc = exportFilters([rule({ id: "rX", name: "boletines", from: ["news@x.test"] })]);
    await user.upload(
      screen.getByLabelText(/importar filtros/i),
      file("moov-filtros.json", JSON.stringify(doc)),
    );

    await screen.findByText(/se importó 1 filtro/i);
    expect(onImport).toHaveBeenCalledTimes(1);
    // The drafts, not the file: no ids, exactly what `FilterRule/set` accepts.
    expect(onImport.mock.calls[0]?.[0]).toEqual([
      expect.objectContaining({ name: "boletines", from: ["news@x.test"] }),
    ]);
  });

  it("names the reason a malformed file was refused, and writes nothing", async () => {
    const user = userEvent.setup();
    const onImport = vi.fn();
    renderSection({ onImport });

    await user.upload(
      screen.getByLabelText(/importar filtros/i),
      file("broken.json", "{ this is not json"),
    );

    // "No se pudo importar" over a file the user chose is a dead end: they
    // cannot tell a wrong file from a corrupt one.
    expect(await screen.findByRole("alert")).toHaveTextContent(/no es JSON/i);
    expect(onImport).not.toHaveBeenCalled();
  });

  it("says so when the file is valid JSON but not ours", async () => {
    const user = userEvent.setup();
    const onImport = vi.fn();
    renderSection({ onImport });

    await user.upload(
      screen.getByLabelText(/importar filtros/i),
      file("gmail.json", '{"feed":{"entry":[]}}'),
    );

    expect(await screen.findByRole("alert")).toHaveTextContent(/El XML de Gmail no está soportado/i);
    expect(onImport).not.toHaveBeenCalled();
  });
});
