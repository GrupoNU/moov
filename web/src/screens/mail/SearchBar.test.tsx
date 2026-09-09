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

/**
 * E-06, E-07, E-08 — what the popup holds besides suggestions.
 *
 * Gmail's dropdown answers three questions at once: what could I type (the
 * suggestions), is my message already here (five matching conversations), and
 * where is everything (the Enter row). Ours answered only the first, which is
 * why the review called the combobox well built and badly fed.
 */
describe("E-06/E-07/E-08 — messages, chips and the way out", () => {
  const PREVIEWS = [
    { id: "e1", sender: "Ana Gómez", subject: "Presupuesto marzo", date: "12 mar" },
    { id: "e2", sender: "Bruno", subject: "Re: presupuesto", date: "9 mar" },
  ];

  function renderBar(
    props: Partial<React.ComponentProps<typeof SearchBar>> = {},
  ): { onSearch: ReturnType<typeof vi.fn>; onOpenPreview: ReturnType<typeof vi.fn> } {
    const onSearch = vi.fn();
    const onOpenPreview = vi.fn();
    function Host(): React.JSX.Element {
      const [value, setValue] = useState("");
      return (
        <I18nProvider locale="es">
          <SearchBar
            value={value}
            onChange={setValue}
            onSearch={onSearch}
            isSearching={false}
            onOpenPreview={onOpenPreview}
            {...props}
          />
        </I18nProvider>
      );
    }
    render(<Host />);
    return { onSearch, onOpenPreview };
  }

  it("shows matching messages, without searching or navigating", async () => {
    const user = userEvent.setup();
    const onPreviewSearch = vi.fn(() => Promise.resolve(PREVIEWS));
    const { onSearch } = renderBar({ onPreviewSearch });

    await user.type(screen.getByRole("combobox"), "presupuesto");

    expect(await screen.findByText("Presupuesto marzo")).toBeInTheDocument();
    expect(screen.getByText("Ana Gómez")).toBeInTheDocument();
    expect(screen.getByText("12 mar")).toBeInTheDocument();
    // The inbox behind is untouched: this is the debouncer's proper job now,
    // and it never calls the thing that changes the route.
    expect(onSearch).not.toHaveBeenCalled();
  });

  it("collapses a burst of keystrokes into ONE request", async () => {
    const user = userEvent.setup();
    const onPreviewSearch = vi.fn(() => Promise.resolve(PREVIEWS));
    renderBar({ onPreviewSearch });

    await user.type(screen.getByRole("combobox"), "presupuesto");
    await screen.findByText("Presupuesto marzo");

    // The reason the debouncer survived P0-3: many keystrokes, one request.
    expect(onPreviewSearch).toHaveBeenCalledTimes(1);
    expect(onPreviewSearch.mock.calls[0]?.[0]).toBe("presupuesto");
  });

  it("opens a previewed message and leaves the list alone", async () => {
    const user = userEvent.setup();
    const onPreviewSearch = vi.fn(() => Promise.resolve(PREVIEWS));
    const { onSearch, onOpenPreview } = renderBar({ onPreviewSearch });

    await user.type(screen.getByRole("combobox"), "presupuesto");
    await user.click(await screen.findByText("Presupuesto marzo"));

    expect(onOpenPreview).toHaveBeenCalledWith(expect.objectContaining({ id: "e1" }));
    // Not a search: the user found the message they wanted, and replacing
    // their inbox with a result list they never asked for would be the screen
    // doing something they did not.
    expect(onSearch).not.toHaveBeenCalled();
  });

  it("offers the E-08 row, which runs the whole search", async () => {
    const user = userEvent.setup();
    const { onSearch } = renderBar();

    await user.type(screen.getByRole("combobox"), "presupuesto");
    await user.click(screen.getByText(/todos los resultados para/i));

    expect(onSearch).toHaveBeenCalledWith("presupuesto");
  });

  it("keeps the E-08 row and the Enter key doing the same thing", async () => {
    const user = userEvent.setup();
    const onPreviewSearch = vi.fn(() => Promise.resolve(PREVIEWS));
    const { onSearch } = renderBar({ onPreviewSearch });

    const input = screen.getByRole("combobox");
    await user.type(input, "presupuesto");
    await screen.findByText("Presupuesto marzo");
    await user.keyboard("{Enter}");

    // A row labelled "Enter" that did something Enter does not would be worse
    // than no row at all.
    expect(onSearch).toHaveBeenCalledWith("presupuesto");
  });

  it("walks ONE cursor across suggestions, messages and the Enter row", async () => {
    const user = userEvent.setup();
    const onPreviewSearch = vi.fn(() => Promise.resolve(PREVIEWS));
    renderBar({ onPreviewSearch, recentSearches: ["presupuesto marzo"] });

    const input = screen.getByRole("combobox");
    await user.type(input, "presupuesto");
    await screen.findByText("Presupuesto marzo");

    const options = screen.getAllByRole("option");
    // Recent + two messages + the Enter row: one listbox, one virtual cursor.
    // Two lists would mean two cursors and a hand-written hand-off, which is
    // where a combobox stops matching the APG pattern.
    expect(options.length).toBe(4);
    await user.keyboard("{ArrowDown}");
    expect(options[0]).toHaveAttribute("aria-selected", "true");
  });

  it("shows the quick chips only while the box is focused and EMPTY (E-07)", async () => {
    const user = userEvent.setup();
    renderBar();

    const input = screen.getByRole("combobox");
    await user.click(input);
    expect(screen.getByRole("group", { name: /búsquedas rápidas/i })).toBeInTheDocument();

    await user.type(input, "x");
    // From here on the suggestions answer the same question better.
    expect(screen.queryByRole("group", { name: /búsquedas rápidas/i })).toBeNull();
  });

  it("emits ordinary grammar from a chip", async () => {
    const user = userEvent.setup();
    const { onSearch } = renderBar();

    await user.click(screen.getByRole("combobox"));
    await user.click(screen.getByRole("button", { name: /con adjunto/i }));

    // A chip and a typed query are the same thing to everything downstream.
    expect(onSearch).toHaveBeenCalledWith("has:attachment");
  });

  it("shows no message rows at all without a preview source", async () => {
    const user = userEvent.setup();
    renderBar();
    await user.type(screen.getByRole("combobox"), "presupuesto");
    // Offline, or a screen with no client: the operator and recent suggestions
    // still work, because they never need a request.
    expect(screen.queryByText("Presupuesto marzo")).toBeNull();
  });
});
