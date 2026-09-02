import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import { ComposeButton } from "./ComposeButton";

/**
 * The rail's "Redactar" pill (owner's finding 1, canon 07 §2).
 *
 * Two things are worth pinning, and neither is the styling.
 *
 * The WORD: the owner's finding was that the button said "Escribir" where
 * Gmail says "Redactar". A label that is a reasonable synonym is exactly the
 * kind of drift no type checker catches, so the string is asserted directly in
 * both locales rather than through a key.
 *
 * The COLLAPSED form: the rail's hamburger changes what the control is, and
 * the failure mode is silent — a round button whose accessible name went away
 * with its visible text is a control a screen-reader user can no longer
 * identify.
 */

function renderButton(props: Record<string, unknown> = {}, locale?: "es" | "en") {
  const onCompose = vi.fn();
  render(
    <I18nProvider {...(locale === undefined ? {} : { locale })}>
      <ComposeButton onCompose={onCompose} {...props} />
    </I18nProvider>,
  );
  return onCompose;
}

describe("the compose pill", () => {
  it("says Gmail's word, in Spanish", () => {
    renderButton({}, "es");
    expect(screen.getByRole("button", { name: "Redactar" })).toBeInTheDocument();
    // The word the button used to show. Naming it here is the point: this is a
    // regression test for a label, and a label regression is invisible.
    expect(screen.queryByText("Escribir")).not.toBeInTheDocument();
  });

  it("says Gmail's word, in English", () => {
    renderButton({}, "en");
    expect(screen.getByRole("button", { name: "Compose" })).toBeInTheDocument();
    expect(screen.queryByText("Write")).not.toBeInTheDocument();
  });

  it("composes when clicked", async () => {
    const user = userEvent.setup();
    const onCompose = renderButton({}, "es");
    await user.click(screen.getByRole("button", { name: "Redactar" }));
    expect(onCompose).toHaveBeenCalledTimes(1);
  });

  it("keeps its accessible name when the rail collapses, and drops the visible word", () => {
    renderButton({ collapsed: true }, "es");

    /*
     * Still findable BY NAME — that is the whole assertion. Gmail's collapsed
     * rail keeps a round pencil button, and a user who cannot name it has lost
     * the control even though it is still painted.
     */
    const button = screen.getByRole("button", { name: "Redactar" });
    expect(button).toBeInTheDocument();
    /*
     * …but the word is not TEXT any more. It lives only in `aria-label`, so a
     * screen reader announces it exactly once. Rendering a visually-hidden
     * span next to the label would announce it twice.
     */
    expect(button).not.toHaveTextContent("Redactar");
  });

  it("shows the word as real text when the rail is expanded", () => {
    renderButton({ collapsed: false }, "es");
    expect(screen.getByRole("button", { name: "Redactar" })).toHaveTextContent("Redactar");
  });
});
