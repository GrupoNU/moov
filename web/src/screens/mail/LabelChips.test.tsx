import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import { DEFAULT_LABEL_COLOR_ID } from "../../mail/labelPalette";
import type { Label } from "../../mail/labelStore";
import { LabelChips, MAX_ROW_CHIPS } from "./LabelChips";

/**
 * The chips (E8) — the row-level display of labels.
 *
 * The rule under test is canon §4.2's, and it is a rule about what must NOT
 * happen: chips carry the colour, the ROW never does. A tinted row would put
 * one label in the same channel the row already uses for unread, selected and
 * focused, and none of the four would be readable.
 */

function label(name: string, colorId = DEFAULT_LABEL_COLOR_ID): Label {
  return { keyword: `$label:${name}`, name, colorId, visibility: "show" };
}

function renderChips(labels: readonly Label[], props: Partial<React.ComponentProps<typeof LabelChips>> = {}) {
  return render(
    <I18nProvider locale="es">
      <LabelChips labels={labels} {...props} />
    </I18nProvider>,
  );
}

describe("rendering", () => {
  it("renders nothing at all for a message with no labels", () => {
    const { container } = renderChips([]);
    // Not an empty span: an empty flex container still occupies its gap in the
    // row, which would shift every unlabelled row by a few pixels.
    expect(container.firstChild).toBeNull();
  });

  it("renders one chip per label", () => {
    renderChips([label("work"), label("clients")]);
    expect(screen.getByText("work")).toBeInTheDocument();
    expect(screen.getByText("clients")).toBeInTheDocument();
  });

  it("carries the colour as custom properties, both themes at once", () => {
    renderChips([label("work", "blue")]);
    const chip = screen.getByText("work");
    // Both pairs travel inline so the stylesheet can swap them under the
    // dark-theme selector — no JavaScript reads the theme.
    expect(chip.style.getPropertyValue("--label-bg")).not.toBe("");
    expect(chip.style.getPropertyValue("--label-fg")).not.toBe("");
    expect(chip.style.getPropertyValue("--label-bg-dark")).not.toBe("");
    expect(chip.style.getPropertyValue("--label-fg-dark")).not.toBe("");
  });
});

describe("overflow", () => {
  const many = ["a", "b", "c", "d", "e"].map((name) => label(name));

  it("shows at most three chips, then a +N marker", () => {
    renderChips(many);
    for (const name of ["a", "b", "c"]) {
      expect(screen.getByText(name)).toBeInTheDocument();
    }
    expect(screen.queryByText("d")).not.toBeInTheDocument();
    // The count is what is HIDDEN, not the total: 5 labels, 3 shown, "+2".
    expect(screen.getByText("+2")).toBeInTheDocument();
  });

  it("uses MAX_ROW_CHIPS as the default so the constant is the single source", () => {
    expect(MAX_ROW_CHIPS).toBe(3);
  });

  it("shows no marker when everything fits", () => {
    renderChips([label("a"), label("b")]);
    expect(screen.queryByText(/^\+/)).not.toBeInTheDocument();
  });

  it("names the hidden labels in the marker's title, so nothing is unreachable", () => {
    renderChips(many);
    expect(screen.getByText("+2")).toHaveAttribute("title", "d, e");
  });

  it("shows every chip when the caller lifts the cap — the reader's case", () => {
    renderChips(many, { max: Number.POSITIVE_INFINITY });
    for (const name of ["a", "b", "c", "d", "e"]) {
      expect(screen.getByText(name)).toBeInTheDocument();
    }
    expect(screen.queryByText(/^\+/)).not.toBeInTheDocument();
  });
});

describe("interaction", () => {
  it("is inert text when no handler is given", () => {
    renderChips([label("work")]);
    expect(screen.queryByRole("button", { name: /work/i })).not.toBeInTheDocument();
  });

  it("is a button that navigates when a handler is given", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    renderChips([label("work")], { onSelect });
    await user.click(screen.getByRole("button", { name: /work/i }));
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ name: "work" }));
  });

  it("stops the click from also opening the row's conversation", async () => {
    const user = userEvent.setup();
    const onRowClick = vi.fn();
    const onSelect = vi.fn();
    render(
      <I18nProvider locale="es">
        {/* eslint-disable-next-line jsx-a11y/no-static-element-interactions, jsx-a11y/click-events-have-key-events */}
        <div onClick={onRowClick}>
          <LabelChips labels={[label("work")]} onSelect={onSelect} />
        </div>
      </I18nProvider>,
    );
    await user.click(screen.getByRole("button", { name: /work/i }));
    expect(onSelect).toHaveBeenCalledTimes(1);
    // Two destinations from one click is the bug this prevents.
    expect(onRowClick).not.toHaveBeenCalled();
  });
});

describe("accessibility", () => {
  it("gives a screen reader the whole list as one phrase, not N fragments", () => {
    renderChips(["a", "b", "c", "d"].map((name) => label(name)));
    // Including the ones the +N hid: the visual truncation is a layout
    // constraint, not an information one.
    expect(screen.getByText(/Etiquetas: a, b, c, d/)).toBeInTheDocument();
  });
});
