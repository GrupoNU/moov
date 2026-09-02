import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import { DEFAULT_LABEL_COLOR_ID } from "../../mail/labelPalette";
import type { Label } from "../../mail/labelStore";
import { LabelList } from "./LabelList";

/**
 * The sidebar's label group (E8).
 *
 * The decision under test is GC-5's line made visible: labels are a SEPARATE,
 * titled group, not more rows in the folder tree. Rendering them in the tree
 * would erase the distinction the whole epic is built on — folders organise
 * (unlimited), labels cut across (26).
 */

function label(name: string, colorId = DEFAULT_LABEL_COLOR_ID): Label {
  return { keyword: `$label:${name}`, name, colorId, visibility: "show" };
}

function renderList(labels: readonly Label[], selectedKeyword?: string) {
  const onSelect = vi.fn();
  render(
    <I18nProvider locale="es">
      <LabelList labels={labels} selectedKeyword={selectedKeyword} onSelect={onSelect} />
    </I18nProvider>,
  );
  return onSelect;
}

describe("rendering", () => {
  /**
   * Canon 07 §2 and the owner's finding 2.
   *
   * E8 rendered nothing until the first label existed. That made the section —
   * and with it the whole feature — undiscoverable from the rail on exactly the
   * accounts that had never used it. Gmail keeps the header and its `+`
   * regardless, because the header IS the affordance.
   */
  it("shows the heading and the + even with no labels at all", () => {
    render(
      <I18nProvider locale="es">
        <LabelList
          labels={[]}
          selectedKeyword={undefined}
          onSelect={vi.fn()}
          onCreate={vi.fn()}
        />
      </I18nProvider>,
    );
    expect(screen.getByRole("heading", { name: /etiquetas/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /etiqueta nueva/i })).toBeInTheDocument();
  });

  it("is JUST the header when empty — no placeholder row, no empty list", () => {
    render(
      <I18nProvider locale="es">
        <LabelList
          labels={[]}
          selectedKeyword={undefined}
          onSelect={vi.fn()}
          onCreate={vi.fn()}
        />
      </I18nProvider>,
    );
    /*
     * An empty `list` is announced as "list, 0 items" — a statement about a
     * structure the user never asked about. The header already says everything
     * the empty state has to say.
     */
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });

  it("renders nothing when the rail is collapsed AND there are no labels", () => {
    /*
     * The one case that still renders nothing, and for a structural reason: at
     * icon width the header is hidden (no room for a heading or a `+`), so what
     * would remain is an empty group with nothing to act on.
     */
    const { container } = render(
      <I18nProvider locale="es">
        <LabelList
          labels={[]}
          selectedKeyword={undefined}
          onSelect={vi.fn()}
          onCreate={vi.fn()}
          collapsed
        />
      </I18nProvider>,
    );
    expect(container.firstChild).toBeNull();
  });

  it("opens the label manager from the +", async () => {
    const user = userEvent.setup();
    const onCreate = vi.fn();
    render(
      <I18nProvider locale="es">
        <LabelList
          labels={[]}
          selectedKeyword={undefined}
          onSelect={vi.fn()}
          onCreate={onCreate}
        />
      </I18nProvider>,
    );
    await user.click(screen.getByRole("button", { name: /etiqueta nueva/i }));
    expect(onCreate).toHaveBeenCalledTimes(1);
  });

  it("titles the group, so the two sidebar sections are distinguishable", () => {
    renderList([label("work")]);
    expect(screen.getByRole("heading", { name: /etiquetas/i })).toBeInTheDocument();
  });

  it("is a plain list, not a tree — labels do not nest structurally", () => {
    renderList([label("work")]);
    expect(screen.getByRole("list", { name: /etiquetas/i })).toBeInTheDocument();
    expect(screen.queryByRole("tree")).not.toBeInTheDocument();
  });

  it("shows one row per label", () => {
    renderList([label("work"), label("clients")]);
    expect(screen.getByRole("button", { name: /work/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /clients/i })).toBeInTheDocument();
  });
});

describe("selection", () => {
  it("marks the current label as the page", () => {
    renderList([label("work"), label("clients")], "$label:work");
    expect(screen.getByRole("button", { name: /work/i })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(screen.getByRole("button", { name: /clients/i })).not.toHaveAttribute(
      "aria-current",
    );
  });

  it("reports a click with the whole label", async () => {
    const user = userEvent.setup();
    const onSelect = renderList([label("work")]);
    await user.click(screen.getByRole("button", { name: /work/i }));
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ keyword: "$label:work" }));
  });
});
