import { readFileSync } from "node:fs";
import { resolve } from "node:path";

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

/**
 * A-10 — the pill's SCALE and COLOUR, asserted against the stylesheet.
 *
 * The header above says the styling is not what these tests pin, and that was
 * true while the styling was arbitrary. It is not arbitrary any more: the
 * review measured ~100x32 in solid brand purple against Gmail's ~135x48 light
 * accent with a soft lift, and each of those numbers now answers a specific
 * defect. A regression on any of them is invisible in jsdom — which computes
 * no cascade and no `color-mix` — so the file is read directly, the same
 * technique `MailboxList.test.tsx` uses for the row reset.
 */
describe("A-10: the pill is at Gmail's scale", () => {
  const css = readFileSync(
    resolve(process.cwd(), "src/screens/mail/ComposeButton.module.css"),
    "utf8",
  );
  const compose = /\.compose \{([\s\S]*?)\}/.exec(css)?.[1] ?? "";
  const collapsed = /\.composeCollapsed \{([\s\S]*?)\}/.exec(css)?.[1] ?? "";

  it("reaches Gmail's 48x135 as MINIMUMS, not fixed dimensions", () => {
    // Minimums rather than `height`/`width`: a hard 48px clips the label the
    // moment a locale runs long or a user raises their font size. The pill has
    // to reach Gmail's proportions at the default and GROW past them.
    expect(compose).toMatch(/min-height:\s*48px/);
    expect(compose).toMatch(/min-width:\s*135px/);
    // The lookbehind is not decoration: `\bheight` matches inside `min-height`
    // (the hyphen is a word boundary), so without it the two assertions above
    // would contradict these two and the test could never pass.
    expect(compose).not.toMatch(/(?<!min-|max-)height:\s*\d/);
    expect(compose).not.toMatch(/(?<!min-|max-)width:\s*\d/);
  });

  it("carries the subtle elevation instead of sitting flat in the rail", () => {
    expect(compose).toMatch(/box-shadow:\s*var\(--shadow-sm\)/);
  });

  it("uses a light wash of the accent with dark text, not a solid fill", () => {
    /*
     * The rail already spends its accent on the SELECTED folder. A solid
     * purple button directly above a solid purple row made two different
     * things shout in one colour, and the eye could not tell which said
     * "where you are". Gmail resolves it the same way: light pill, filled row.
     */
    expect(compose).toMatch(/background:\s*color-mix\([^)]*var\(--color-accent\)[^)]*\)/);
    expect(compose).toMatch(/color:\s*var\(--text-strong\)/);
    expect(compose).not.toMatch(/background:\s*var\(--color-accent\)\s*;/);
  });

  it("mixes the fill against a SURFACE, so it is opaque", () => {
    // `--color-accent-tint` is 10% alpha over *transparent*. Using it here
    // would let the folder list scroll visibly under a control that must read
    // as solid, and its contrast would depend on whatever was behind it.
    expect(compose).toMatch(/color-mix\(in srgb,\s*var\(--color-accent\)\s*18%,\s*var\(--surface-default\)\)/);
  });

  it("releases the min-width when collapsed, or the FAB blows the rail open", () => {
    // `aspect-ratio: 1` resolving against a surviving 135px minimum would make
    // the collapsed "circle" a 135px disc in a 60px rail.
    expect(collapsed).toMatch(/min-width:\s*0/);
  });
});
