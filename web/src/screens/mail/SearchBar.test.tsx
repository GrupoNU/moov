import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import { SearchBar } from "./SearchBar";

/**
 * The search field's two P0 regressions (2026-09-08).
 *
 * Both are about what the box does when the user has NOT asked for anything:
 * typing must not search (P0-3) and Escape must give focus back (P0-4). Each
 * shipped as a plausible-looking convenience and each made the field hostile —
 * the first navigated on every half-typed operator, the second trapped the
 * caret so no global shortcut could fire again.
 */

/** A controlled host, because the real screen owns the text. */
function Harness({ onSearch }: { readonly onSearch: (value: string) => void }): React.JSX.Element {
  const [value, setValue] = useState("");
  return (
    <I18nProvider>
      <SearchBar value={value} onChange={setValue} onSearch={onSearch} isSearching={false} />
    </I18nProvider>
  );
}

describe("the search field", () => {
  it("does not search while the user is typing", async () => {
    const user = userEvent.setup();
    const onSearch = vi.fn();
    render(<Harness onSearch={onSearch} />);

    const input = screen.getByRole("combobox");
    // `from:` is the exact shape the review caught: an operator with no value,
    // which the old debounce sent as a query and the list answered "no
    // matches" to, under a warning card, mid-word.
    await user.type(input, "from:");

    expect(onSearch).not.toHaveBeenCalled();
    expect(input).toHaveValue("from:");
  });

  it("searches on Enter, with what is in the box", async () => {
    const user = userEvent.setup();
    const onSearch = vi.fn();
    render(<Harness onSearch={onSearch} />);

    const input = screen.getByRole("combobox");
    await user.type(input, "from:ana{Enter}");

    expect(onSearch).toHaveBeenCalledWith("from:ana");
  });

  it("gives focus back on Escape, and keeps the query", async () => {
    const user = userEvent.setup();
    const onSearch = vi.fn();
    render(<Harness onSearch={onSearch} />);

    const input = screen.getByRole("combobox");
    await user.click(input);
    await user.type(input, "presupuesto");
    expect(document.activeElement).toBe(input);

    // Focusing opens the suggestion popup, so the first Escape may spend
    // itself closing it — the field's documented innermost-first order.
    await user.keyboard("{Escape}");
    if (document.activeElement === input) await user.keyboard("{Escape}");

    expect(document.activeElement).not.toBe(input);
    // E-28: Escape leaves; it does not clear. The X clears.
    expect(input).toHaveValue("presupuesto");
  });

  it("clears from the X, which is the affordance that says so", async () => {
    const user = userEvent.setup();
    const onSearch = vi.fn();
    render(<Harness onSearch={onSearch} />);

    const input = screen.getByRole("combobox");
    await user.type(input, "presupuesto");
    await user.click(screen.getByRole("button", { name: /borrar|clear/i }));

    expect(input).toHaveValue("");
    expect(onSearch).toHaveBeenCalledWith("");
  });
});

/**
 * E-11 — the panel trigger, and where recent searches come from.
 *
 * Two halves of one finding. The trigger drew a three-line taper, which reads
 * as a FILTER FUNNEL — the icon for "narrow these results", a thing this button
 * does not do — and the review asked for Gmail's sliders. And it asked that
 * recent searches appear on FOCUS rather than behind that button, because
 * "what did I search for last time" is the first question a person has when
 * they click into an empty box, not something to go looking for.
 */
describe("E-11 — the options trigger and the recents on focus", () => {
  function withRecents(recent: readonly string[]) {
    const onSearch = vi.fn();
    render(
      <I18nProvider locale="es">
        <SearchBar
          value=""
          onChange={vi.fn()}
          onSearch={onSearch}
          isSearching={false}
          recentSearches={recent}
        />
      </I18nProvider>,
    );
    return { onSearch };
  }

  it("shows recent searches on focus, with no click on the trigger", async () => {
    const user = userEvent.setup();
    withRecents(["factura marzo", "from:ana"]);

    await user.click(screen.getByRole("combobox"));

    expect(screen.getByRole("option", { name: /factura marzo/ })).toBeInTheDocument();
  });

  it("draws sliders, not a caret — the button opens settings, not a list", () => {
    withRecents([]);
    const trigger = screen.getByRole("button", { name: /opciones de búsqueda/i });
    // The knobs are the whole difference: a rail with a knob says "adjust
    // this", a chevron says "there is more of this list below".
    expect(trigger.querySelectorAll("circle").length).toBe(2);
  });
});
