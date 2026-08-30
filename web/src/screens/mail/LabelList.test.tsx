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
  it("renders nothing when there are no labels", () => {
    // An empty "Etiquetas" heading over nothing is a permanent reminder of a
    // feature the user is not using.
    const { container } = render(
      <I18nProvider locale="es">
        <LabelList labels={[]} selectedKeyword={undefined} onSelect={vi.fn()} />
      </I18nProvider>,
    );
    expect(container.firstChild).toBeNull();
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
