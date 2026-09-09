import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import { es } from "../../i18n/strings";
import { labelBudget, MAX_DURABLE_KEYWORDS } from "../../mail/labels";
import { DEFAULT_LABEL_COLOR_ID, LABEL_COLORS } from "../../mail/labelPalette";
import type { Label } from "../../mail/labelStore";
import type { Mailbox } from "../../mail/types";
import { LabelsSection } from "./LabelsSection";

/**
 * The label manager (E8) — and specifically its HONESTY about 26.
 *
 * The tests that matter here are not the CRUD ones. They are the ones that pin
 * the epic's product requirement: the ceiling is stated before the attempt, the
 * create control is disabled AT zero with the explanation and the unlimited
 * alternative beside it, and the number on screen is the number the button
 * obeys. Anything less and the 27th label is created, applied, read back for
 * weeks, and then vanishes from every message at once (validation V1).
 */

/**
 * The name a swatch announces (F-31): "Ámbar · Suave".
 *
 * Built here from the SAME string table the component reads, so this is a
 * check that every palette entry has a name rather than a second copy of the
 * names — a hardcoded list would pass while the component rendered the raw id
 * for a hue nobody added to the table.
 */
function colorName(color: (typeof LABEL_COLORS)[number]): string {
  const hue = es[`label.hue.${color.hue}` as keyof typeof es];
  const step = es[`label.step.${color.step}` as keyof typeof es];
  return `${String(hue)} · ${String(step)}`;
}

function label(name: string, colorId = DEFAULT_LABEL_COLOR_ID): Label {
  return { keyword: `$label:${name}`, name, colorId, visibility: "show" };
}

function renderSection(
  overrides: Partial<React.ComponentProps<typeof LabelsSection>> = {},
) {
  const props = {
    labels: [label("work")],
    budget: labelBudget(["$label:work"], ["$label:work"]),
    onCreate: vi.fn(),
    onSetColor: vi.fn(),
    onSetVisibility: vi.fn(),
    onRename: vi.fn(),
    onDelete: vi.fn(),
    ...overrides,
  };
  render(
    <I18nProvider locale="es">
      <LabelsSection {...props} />
    </I18nProvider>,
  );
  return props;
}

/** A budget with exactly `available` slots left. */
function budgetWith(available: number) {
  const used = MAX_DURABLE_KEYWORDS - available;
  const keywords = Array.from({ length: used }, (_, index) => `$label:l${index}`);
  return { budget: labelBudget(keywords, keywords), labels: keywords.map((_, i) => label(`l${i}`)) };
}

describe("the ceiling is stated, not discovered", () => {
  it("shows how many of the 26 remain", () => {
    const { budget, labels } = budgetWith(17);
    renderSection({ budget, labels });
    expect(screen.getByText("17 de 26 disponibles")).toBeInTheDocument();
  });

  it("explains WHY the number is 26, and names the unlimited alternative", () => {
    renderSection();
    // The explanation is permanent, not an error shown after a failure: a user
    // who understands the constraint plans around it.
    expect(screen.getByText(/26 keywords IMAP duraderas/i)).toBeInTheDocument();
    expect(screen.getByText(/carpetas no tienen ese límite/i)).toBeInTheDocument();
  });

  it("announces the budget as a status, so a screen reader hears it change", () => {
    renderSection();
    expect(screen.getByRole("status")).toHaveTextContent(/disponibles/i);
  });
});

describe("at zero, creation is disabled WITH the reason", () => {
  it("says there is no room instead of showing '0 de 26'", () => {
    const { budget, labels } = budgetWith(0);
    renderSection({ budget, labels });
    expect(screen.getByText(/no queda lugar/i)).toBeInTheDocument();
  });

  it("disables the name field and the create button", () => {
    const { budget, labels } = budgetWith(0);
    renderSection({ budget, labels });
    expect(screen.getByRole("button", { name: /etiqueta nueva/i })).toBeDisabled();
    expect(screen.getByLabelText(/nombre de la etiqueta/i)).toBeDisabled();
  });

  it("offers the folder alternative when the host can create one", async () => {
    const user = userEvent.setup();
    const { budget, labels } = budgetWith(0);
    const onCreateFolder = vi.fn();
    renderSection({ budget, labels, onCreateFolder });
    await user.click(screen.getByRole("button", { name: /crear una carpeta/i }));
    expect(onCreateFolder).toHaveBeenCalled();
  });

  it("still creates when there IS room — the disabled state is not permanent", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.type(screen.getByLabelText(/nombre de la etiqueta/i), "Clientes");
    await user.click(screen.getByRole("button", { name: /etiqueta nueva/i }));
    expect(props.onCreate).toHaveBeenCalledWith("Clientes", expect.any(String));
  });
});

describe("validation says which rule was broken", () => {
  it("refuses a duplicate by name", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.type(screen.getByLabelText(/nombre de la etiqueta/i), "work");
    await user.click(screen.getByRole("button", { name: /etiqueta nueva/i }));
    expect(screen.getByRole("alert")).toHaveTextContent(/ya existe/i);
    expect(props.onCreate).not.toHaveBeenCalled();
  });

  it("refuses a reserved keyword name", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.type(screen.getByLabelText(/nombre de la etiqueta/i), "NonJunk");
    await user.click(screen.getByRole("button", { name: /etiqueta nueva/i }));
    expect(screen.getByRole("alert")).toHaveTextContent(/reservado/i);
    expect(props.onCreate).not.toHaveBeenCalled();
  });

  it("refuses an empty name", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.click(screen.getByRole("button", { name: /etiqueta nueva/i }));
    expect(screen.getByRole("alert")).toHaveTextContent(/poné un nombre/i);
    expect(props.onCreate).not.toHaveBeenCalled();
  });

  it("ACCEPTS a nested name — the whole reason the pointer escaping exists", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.type(screen.getByLabelText(/nombre de la etiqueta/i), "work/clients");
    await user.click(screen.getByRole("button", { name: /etiqueta nueva/i }));
    expect(props.onCreate).toHaveBeenCalledWith("work/clients", expect.any(String));
  });
});

describe("the palette is closed and reachable", () => {
  it("offers exactly the palette's colours as a radio group", () => {
    renderSection();
    expect(screen.getAllByRole("radiogroup").length).toBeGreaterThan(0);
    // A radio group, not a free picker: the contrast guarantee only holds for
    // the pairs someone checked (canon §2.6). Every palette entry is offered,
    // and nothing outside it is.
    for (const color of LABEL_COLORS) {
      expect(
        screen.getAllByRole("radio", { name: colorName(color) }).length,
        `no swatch for ${color.id}`,
      ).toBeGreaterThan(0);
    }
    // The create picker offers exactly the palette, no more — twenty-four
    // since F-31 gave every hue a bold step.
    const [createGroup] = screen.getAllByRole("radiogroup");
    expect(createGroup).toBeDefined();
    expect(within(createGroup!).getAllByRole("radio")).toHaveLength(LABEL_COLORS.length);
  });

  /*
   * F-31. The swatches used to announce their raw ids ("slate", "amber") and
   * show no tooltip at all: a sighted user hovering a pastel learned nothing,
   * and a screen-reader user heard a word out of our source code.
   */
  it("names each swatch with words a person would use, not with its id", () => {
    renderSection();

    expect(screen.getAllByRole("radio", { name: "Ámbar · Suave" }).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("radio", { name: "Rojo · Intenso" }).length).toBeGreaterThan(0);
    // The id is gone from the announcement entirely.
    expect(screen.queryByRole("radio", { name: "amber" })).not.toBeInTheDocument();
  });

  it("puts the same name in the tooltip, so hover and screen reader agree", () => {
    renderSection();

    const swatch = screen.getAllByRole("radio", { name: "Azul · Intenso" })[0];
    expect(swatch?.closest("label")).toHaveAttribute("title", "Azul · Intenso");
  });

  it("offers every hue at both strengths (F-31)", () => {
    renderSection();

    // Twelve pales beside each other were "casi indistinguibles"; a user who
    // wants one label to be loud now has a way to say so.
    const [createGroup] = screen.getAllByRole("radiogroup");
    // The name lives in the wrapping <label>, which is also the tooltip's home.
    const names = within(createGroup!)
      .getAllByRole("radio")
      .map((radio) => radio.closest("label")?.getAttribute("title") ?? "");
    expect(names.filter((name) => name.includes("Suave"))).toHaveLength(12);
    expect(names.filter((name) => name.includes("Intenso"))).toHaveLength(12);
  });
});

describe("the list", () => {
  it("shows each label as a chip with its name", () => {
    renderSection({ labels: [label("work"), label("clients")] });
    expect(screen.getByText("work")).toBeInTheDocument();
    expect(screen.getByText("clients")).toBeInTheDocument();
  });

  it("offers the three visibility values, Gmail's labelListVisibility verbatim", () => {
    renderSection();
    const select = screen.getByLabelText(/en la barra lateral: work/i);
    expect(select).toHaveValue("show");
    expect(screen.getByRole("option", { name: /mostrar si hay sin leer/i })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /^ocultar$/i })).toBeInTheDocument();
  });

  it("reports a visibility change", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.selectOptions(screen.getByLabelText(/en la barra lateral: work/i), "hide");
    expect(props.onSetVisibility).toHaveBeenCalledWith(
      expect.objectContaining({ name: "work" }),
      "hide",
    );
  });

  it("renames through a form rather than a prompt, so it can be cancelled", async () => {
    const user = userEvent.setup();
    const props = renderSection();
    await user.click(screen.getByRole("button", { name: /^renombrar$/i }));
    const field = screen.getByLabelText(/renombrar «work»/i);
    await user.clear(field);
    await user.type(field, "trabajo");
    await user.click(screen.getByRole("button", { name: /^renombrar$/i }));
    expect(props.onRename).toHaveBeenCalledWith(
      expect.objectContaining({ name: "work" }),
      "trabajo",
    );
  });

  it("refuses a rename onto an existing name", async () => {
    const user = userEvent.setup();
    const props = renderSection({ labels: [label("work"), label("clients")] });
    const [firstRename] = screen.getAllByRole("button", { name: /^renombrar$/i });
    expect(firstRename).toBeDefined();
    await user.click(firstRename!);
    const field = screen.getByLabelText(/renombrar «work»/i);
    await user.clear(field);
    await user.type(field, "clients");
    /*
     * The OTHER rows keep their own "Renombrar" buttons while one row is being
     * edited, so the submit is found inside the open form rather than by name
     * across the whole list — which is also how a user reaches it.
     */
    const form = field.closest("form");
    expect(form).not.toBeNull();
    await user.click(within(form!).getByRole("button", { name: /^renombrar$/i }));
    expect(props.onRename).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent(/ya existe/i);
  });

  /*
   * F-30. "Todavía no hay etiquetas" was true and useless: it told a user who
   * had arrived looking for labels that they were in the right place, and
   * offered them nothing. The real block is not the button — it is "what would
   * I even call one".
   */
  it("invites the first label instead of stating its absence", () => {
    renderSection({ labels: [], budget: labelBudget([], []) });

    expect(screen.getByText(/creá tu primera etiqueta/i)).toBeInTheDocument();
    // The one sentence that makes a label worth wanting: it crosses folders.
    expect(screen.getByText(/cruza carpetas/i)).toBeInTheDocument();
  });

  it("prefills the name box from an example rather than creating anything", async () => {
    const user = userEvent.setup();
    const props = renderSection({ labels: [], budget: labelBudget([], []) });

    await user.click(screen.getByRole("button", { name: /usar «Facturas» como nombre/i }));

    // The example is a starting point the user can edit, not a decision made
    // on their behalf — nothing was created.
    expect(screen.getByLabelText(/nombre de la etiqueta/i)).toHaveValue("Facturas");
    expect(props.onCreate).not.toHaveBeenCalled();
  });

  it("creates from a prefilled example when the user then submits", async () => {
    const user = userEvent.setup();
    const props = renderSection({ labels: [], budget: labelBudget([], []) });

    await user.click(screen.getByRole("button", { name: /usar «Viajes» como nombre/i }));
    await user.click(screen.getByRole("button", { name: "Etiqueta nueva" }));

    // The chip also sets the colour it was drawn in, so what the user saw is
    // what they get.
    expect(props.onCreate).toHaveBeenCalledWith("Viajes", "teal-bold");
  });
});

describe("a running migration is visible and stoppable", () => {
  it("shows the progress the host reports", () => {
    renderSection({ migrationStatus: "1200 mensajes actualizados…" });
    expect(screen.getByText(/1200 mensajes actualizados/)).toBeInTheDocument();
  });

  it("offers a stop, because forty rounds is a minute of wall clock", async () => {
    const user = userEvent.setup();
    const onAbortMigration = vi.fn();
    renderSection({ migrationStatus: "…", onAbortMigration });
    await user.click(screen.getByRole("button", { name: /detener/i }));
    expect(onAbortMigration).toHaveBeenCalled();
  });
});

describe("the closed gap is on screen, not only in a changelog", () => {
  it("says colours and visibility DO roam, now that prefs v2 carries them", () => {
    renderSection();
    /*
     * This test used to assert the opposite sentence, and the inversion is the
     * deliverable: prefs v2's `labels` key made the old limitation false, so
     * the note became the positive fact. Stating it is not decoration — a user
     * who read the previous warning would otherwise go on believing their
     * colours are stuck on one browser.
     */
    expect(screen.getByText(/se guardan en tu cuenta/i)).toBeInTheDocument();
  });

  it("no longer claims the metadata is browser-local", () => {
    renderSection();
    // The direction that actually matters: a leftover copy of the old caveat
    // anywhere in this section would contradict the wiring.
    expect(screen.queryByText(/se guardan en este navegador/i)).toBeNull();
  });
});

/**
 * P0-5c — the folder-visibility table (review F-28).
 *
 * This is the OTHER HALF of the rail's curation, and the half that makes it
 * defensible. The rail hides Calendario, Diario and Problemas de
 * sincronización by a NAME heuristic, which can be wrong about someone's real
 * folder called "Notas". Nothing is deleted, and this table is where a user
 * sees every folder the account has, sees what the rail is doing with it, and
 * changes their mind. Without it the hiding would be an unexplained
 * disappearance — which is worse than the folder dump it replaced.
 */
describe("P0-5c: the Carpetas table", () => {
  function folder(id: string, name: string): Mailbox {
    return {
      id,
      name,
      parentId: null,
      role: null,
      sortOrder: 100,
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

  const FOLDERS = [folder("c", "Calendario"), folder("w", "Trabajo")];

  function renderWithFolders(overrides: Record<string, unknown> = {}) {
    const onSetVisibility = vi.fn();
    renderSection({
      folders: {
        folders: FOLDERS,
        visibilityOf: (box: Mailbox) => (box.name === "Calendario" ? "hide" : "show"),
        onSetVisibility,
        isAvailable: true,
        ...overrides,
      },
    });
    return { onSetVisibility };
  }

  it("lists every folder, hidden ones included — nothing is deleted", () => {
    renderWithFolders();
    expect(screen.getByText("Calendario")).toBeInTheDocument();
    expect(screen.getByText("Trabajo")).toBeInTheDocument();
  });

  it("shows what the rail is currently doing with each one", () => {
    renderWithFolders();
    expect(
      screen.getByRole("combobox", { name: /en el riel de carpetas: Calendario/i }),
    ).toHaveValue("hide");
    expect(
      screen.getByRole("combobox", { name: /en el riel de carpetas: Trabajo/i }),
    ).toHaveValue("show");
  });

  it("flips one back, by name", async () => {
    const user = userEvent.setup();
    const { onSetVisibility } = renderWithFolders();
    await user.selectOptions(
      screen.getByRole("combobox", { name: /en el riel de carpetas: Calendario/i }),
      "show",
    );
    expect(onSetVisibility).toHaveBeenCalledWith(
      expect.objectContaining({ name: "Calendario" }),
      "show",
    );
  });

  it("offers the SAME three words the label list uses", () => {
    renderWithFolders();
    const select = screen.getByRole("combobox", {
      name: /en el riel de carpetas: Trabajo/i,
    });
    const options = within(select).getAllByRole("option").map((o) => o.textContent);
    // One vocabulary across both tables: a user should not learn it twice.
    expect(options).toHaveLength(3);
  });

  it("goes read-only, and says why, on a server that cannot store the choice", () => {
    /*
     * A switch the UI accepts and `Prefs/set` refuses with `unknownProperty`
     * reverts silently on reload — the worst kind of failure. The rail still
     * curates itself there, which is why the table stays VISIBLE rather than
     * disappearing and leaving the hiding unexplained.
     */
    renderWithFolders({ isAvailable: false });
    expect(
      screen.getByRole("combobox", { name: /en el riel de carpetas: Trabajo/i }),
    ).toBeDisabled();
    expect(screen.getByText(/no puede guardar la elección/i)).toBeInTheDocument();
  });

  it("draws no table at all when the host has no folders to show", () => {
    // A heading over nothing is worse than silence.
    renderSection();
    expect(screen.queryByText(/^Carpetas$/)).not.toBeInTheDocument();
  });
});
