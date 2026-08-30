import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import { SearchChips } from "./SearchChips";

/**
 * The chips row (L3 epic E3).
 *
 * The property under test is the one that makes the component correct: a chip
 * holds NO state. Every assertion here is about the query STRING the chip
 * hands up, because that string is the only state there is.
 */

function renderChips(query: string) {
  const onChange = vi.fn();
  render(
    <I18nProvider locale="es">
      <SearchChips query={query} onChange={onChange} />
    </I18nProvider>,
  );
  return { onChange };
}

describe("SearchChips", () => {
  it("renders the toggles Gmail's chip row has", () => {
    renderChips("informe");
    expect(screen.getByRole("button", { name: /adjunto/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /sin leer/i })).toBeInTheDocument();
  });

  it("adds an operator to the query when a chip is pressed", async () => {
    const user = userEvent.setup();
    const { onChange } = renderChips("informe");
    await user.click(screen.getByRole("button", { name: /adjunto/i }));
    expect(onChange).toHaveBeenCalledWith("has:attachment informe");
  });

  it("reads its ON state out of the query, holding none of its own", () => {
    renderChips("has:attachment informe");
    expect(screen.getByRole("button", { name: /adjunto/i })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("REMOVES the operator when an active chip is pressed again", async () => {
    const user = userEvent.setup();
    const { onChange } = renderChips("has:attachment informe");
    await user.click(screen.getByRole("button", { name: /adjunto/i }));
    // Back to exactly the string it started from — the round-trip invariant.
    expect(onChange).toHaveBeenCalledWith("informe");
  });

  it("toggles is:unread through the query string too", async () => {
    const user = userEvent.setup();
    const { onChange } = renderChips("informe");
    await user.click(screen.getByRole("button", { name: /sin leer/i }));
    expect(onChange).toHaveBeenCalledWith("is:unread informe");
  });

  it("shows a From chip only when the query carries one", () => {
    renderChips("informe");
    expect(screen.queryByText(/^De: |^From: /)).not.toBeInTheDocument();

    renderChips("from:ana informe");
    expect(screen.getByText(/ana/)).toBeInTheDocument();
  });

  it("removes the From term when its chip is pressed", async () => {
    const user = userEvent.setup();
    const { onChange } = renderChips("from:ana informe");
    await user.click(screen.getByRole("button", { name: /quitar/i }));
    expect(onChange).toHaveBeenCalledWith("informe");
  });

  it("writes an absolute instant for a time preset, as the parser would", async () => {
    const user = userEvent.setup();
    const { onChange } = renderChips("informe");
    await user.click(screen.getByRole("button", { name: /cualquier fecha/i }));
    await user.click(screen.getByRole("menuitem", { name: /7 d/i }));

    const next = onChange.mock.calls[0]?.[0] as string;
    /*
     * The chip must produce the SAME shape a typed `newer_than:7d` resolves to
     * — an `after:` with a concrete date — or the two ways of asking for the
     * last week would disagree.
     */
    expect(next).toMatch(/^after:\d{4}\/\d{2}\/\d{2} informe$/);
  });
});
