import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import { SearchBar } from "./SearchBar";

/**
 * The search field: typing must not search (P0-3, 2026-09-08).
 *
 * The debounce shipped as a plausible convenience and made the field hostile —
 * every pause mid-word navigated, so a half-typed `from:` came back "no
 * matches" while the user's finger was on the next key.
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
