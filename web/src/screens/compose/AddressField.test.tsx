import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import type { IndexedAddress } from "../../mail/addressIndex";
import type { AddressChip } from "../../mail/addresses";
import { AddressField } from "./AddressField";

/**
 * The recipient field's combobox (E7, canon §2.3).
 *
 * Two things are being proven here at once, and the second matters as much as
 * the first: that the suggestion layer works, and that it did NOT disturb the
 * chip behaviour it was laid over. Paste-splitting, blur-commit and
 * Backspace-removal are the field's oldest and most load-bearing habits, so
 * they are re-asserted here rather than assumed.
 */

afterEach(cleanup);

const index: readonly IndexedAddress[] = [
  {
    email: "ana@example.com",
    displayName: "Ana Gómez",
    lastSeenAt: 200,
    timesSeen: 8,
    source: "sent",
  },
  {
    email: "andres@example.com",
    displayName: "Andrés Paz",
    lastSeenAt: 100,
    timesSeen: 3,
    source: "browsed",
  },
  {
    email: "beatriz@example.com",
    displayName: undefined,
    lastSeenAt: 900,
    timesSeen: 1,
    source: "browsed",
  },
];

/** Renders the field as a controlled component, exposing what it committed. */
function renderField(
  props: Partial<React.ComponentProps<typeof AddressField>> = {},
): { chips: () => readonly AddressChip[]; onChange: ReturnType<typeof vi.fn> } {
  let current: readonly AddressChip[] = props.chips ?? [];
  const onChange = vi.fn((next: readonly AddressChip[]) => {
    current = next;
    rerender();
  });

  const element = (): React.JSX.Element => (
    <I18nProvider locale="en">
      <AddressField
        label="To"
        {...props}
        chips={current}
        onChange={(next) => {
          onChange(next);
        }}
      />
    </I18nProvider>
  );

  const { rerender: doRerender } = render(element());
  function rerender(): void {
    doRerender(element());
  }

  return { chips: () => current, onChange };
}

function listbox(): HTMLElement | null {
  return screen.queryByRole("listbox", { hidden: false });
}

describe("AddressField without an index", () => {
  it("is not announced as a combobox — an empty one would be a lie", () => {
    renderField();
    const input = screen.getByLabelText("To");
    expect(input).not.toHaveAttribute("role", "combobox");
    // And the browser's own autofill is left in place, since it is better than
    // nothing when we have nothing.
    expect(input).toHaveAttribute("autocomplete", "email");
  });

  it("still commits on Enter", async () => {
    const user = userEvent.setup();
    const { chips } = renderField();
    await user.type(screen.getByLabelText("To"), "libre@x.com{Enter}");
    expect(chips().map((chip) => chip.email)).toEqual(["libre@x.com"]);
  });
});

describe("AddressField as a combobox", () => {
  it("carries the APG combobox attributes", () => {
    renderField({ suggestions: index });
    const input = screen.getByLabelText("To");
    expect(input).toHaveAttribute("role", "combobox");
    expect(input).toHaveAttribute("aria-expanded", "false");
    expect(input).toHaveAttribute("aria-autocomplete", "list");
    // Turned off, so the browser's shared dropdown does not cover ours.
    expect(input).toHaveAttribute("autocomplete", "off");
  });

  it("suggests from one character, matching name or address", async () => {
    const user = userEvent.setup();
    renderField({ suggestions: index });
    await user.type(screen.getByLabelText("To"), "a");

    const options = within(listbox()!).getAllByRole("option");
    // Ranked: Ana (8 sightings) before Andrés (3).
    expect(options[0]).toHaveTextContent("ana@example.com");
    expect(options[1]).toHaveTextContent("andres@example.com");
    expect(screen.getByLabelText("To")).toHaveAttribute("aria-expanded", "true");
  });

  it("finds an accented name from unaccented typing", async () => {
    const user = userEvent.setup();
    renderField({ suggestions: index });
    await user.type(screen.getByLabelText("To"), "gomez");

    const options = within(listbox()!).getAllByRole("option");
    expect(options).toHaveLength(1);
    expect(options[0]).toHaveTextContent("Ana Gómez");
  });

  it("shows nothing before a character is typed", async () => {
    const user = userEvent.setup();
    renderField({ suggestions: index });
    await user.click(screen.getByLabelText("To"));
    expect(listbox()).toBeNull();
  });

  it("moves a virtual cursor with the arrows, keeping DOM focus in the input", async () => {
    const user = userEvent.setup();
    renderField({ suggestions: index });
    const input = screen.getByLabelText("To");
    await user.type(input, "a");
    await user.keyboard("{ArrowDown}");

    const options = within(listbox()!).getAllByRole("option");
    expect(options[0]).toHaveAttribute("aria-selected", "true");
    expect(input).toHaveAttribute("aria-activedescendant", options[0]!.id);
    // The whole point of the pattern: typing is never interrupted.
    expect(input).toHaveFocus();

    await user.keyboard("{ArrowDown}");
    expect(within(listbox()!).getAllByRole("option")[1]).toHaveAttribute("aria-selected", "true");

    await user.keyboard("{ArrowUp}");
    expect(within(listbox()!).getAllByRole("option")[0]).toHaveAttribute("aria-selected", "true");
  });

  it("wraps the cursor at both ends", async () => {
    const user = userEvent.setup();
    renderField({ suggestions: index });
    await user.type(screen.getByLabelText("To"), "a");

    // Up from "nothing active" lands on the LAST option.
    await user.keyboard("{ArrowUp}");
    const options = within(listbox()!).getAllByRole("option");
    expect(options[options.length - 1]).toHaveAttribute("aria-selected", "true");
  });

  it("commits the active suggestion on Enter, with its display name", async () => {
    const user = userEvent.setup();
    const { chips } = renderField({ suggestions: index });
    await user.type(screen.getByLabelText("To"), "a");
    await user.keyboard("{ArrowDown}{Enter}");

    expect(chips()).toHaveLength(1);
    expect(chips()[0]).toMatchObject({
      email: "ana@example.com",
      // The name rides along — that is why the index stores one.
      name: "Ana Gómez",
      isValid: true,
    });
    expect(listbox()).toBeNull();
  });

  it("commits the active suggestion on Tab", async () => {
    const user = userEvent.setup();
    const { chips } = renderField({ suggestions: index });
    await user.type(screen.getByLabelText("To"), "beat");
    await user.keyboard("{ArrowDown}{Tab}");

    expect(chips().map((chip) => chip.email)).toEqual(["beatriz@example.com"]);
  });

  it("commits a suggestion on click", async () => {
    const user = userEvent.setup();
    const { chips } = renderField({ suggestions: index });
    await user.type(screen.getByLabelText("To"), "andr");
    await user.click(within(listbox()!).getAllByRole("option")[0]!);

    expect(chips().map((chip) => chip.email)).toEqual(["andres@example.com"]);
  });

  it("commits the TYPED text when no suggestion is active", async () => {
    const user = userEvent.setup();
    const { chips } = renderField({ suggestions: index });
    // "a" matches suggestions, but the user never arrowed onto one — so their
    // own typing is what commits, not the first row of a list they ignored.
    await user.type(screen.getByLabelText("To"), "algo@nuevo.com{Enter}");
    expect(chips().map((chip) => chip.email)).toEqual(["algo@nuevo.com"]);
  });

  it("never offers an address the field already has", async () => {
    const user = userEvent.setup();
    const { chips } = renderField({ suggestions: index });
    await user.type(screen.getByLabelText("To"), "a");
    await user.keyboard("{ArrowDown}{Enter}");
    expect(chips().map((chip) => chip.email)).toEqual(["ana@example.com"]);

    await user.type(screen.getByLabelText("To"), "a");
    const options = within(listbox()!).getAllByRole("option");
    expect(options.every((option) => !option.textContent?.includes("ana@example.com"))).toBe(true);
  });

  it("closes the popup on Escape without clearing the typed text", async () => {
    const user = userEvent.setup();
    renderField({ suggestions: index });
    const input = screen.getByLabelText("To");
    await user.type(input, "an");
    expect(listbox()).not.toBeNull();

    await user.keyboard("{Escape}");
    expect(listbox()).toBeNull();
    expect(input).toHaveValue("an");
  });
});

describe("AddressField behaviours the combobox must not have broken", () => {
  it("still splits a pasted list into chips", async () => {
    const user = userEvent.setup();
    const { chips } = renderField({ suggestions: index });
    const input = screen.getByLabelText("To");
    await user.click(input);
    await user.paste('"Gómez, Ana" <ana@x.com>, bea@y.com');

    expect(chips().map((chip) => chip.email)).toEqual(["ana@x.com", "bea@y.com"]);
    // The quoted display name survived the split — the reason the parser is
    // hand-written in the first place.
    expect(chips()[0]?.name).toBe("Gómez, Ana");
  });

  it("still commits a typed address on blur", async () => {
    const user = userEvent.setup();
    const { chips } = renderField({ suggestions: index });
    await user.type(screen.getByLabelText("To"), "tarde@x.com");
    await user.tab();
    expect(chips().map((chip) => chip.email)).toEqual(["tarde@x.com"]);
  });

  it("still removes the last chip on Backspace in an empty input", async () => {
    const user = userEvent.setup();
    const { chips } = renderField({ suggestions: index });
    await user.type(screen.getByLabelText("To"), "uno@x.com{Enter}dos@x.com{Enter}");
    expect(chips()).toHaveLength(2);

    await user.keyboard("{Backspace}");
    expect(chips().map((chip) => chip.email)).toEqual(["uno@x.com"]);
  });

  it("still marks an invalid chip", async () => {
    const user = userEvent.setup();
    const { chips } = renderField({ suggestions: index });
    await user.type(screen.getByLabelText("To"), "no-es-una-direccion{Enter}");

    expect(chips()[0]?.isValid).toBe(false);
    expect(screen.getByRole("alert")).toHaveTextContent("no-es-una-direccion");
  });

  it("still removes a chip with its remove button", async () => {
    const user = userEvent.setup();
    const { chips } = renderField({ suggestions: index });
    await user.type(screen.getByLabelText("To"), "quitame@x.com{Enter}");
    await user.click(screen.getByRole("button", { name: "Remove quitame@x.com" }));
    expect(chips()).toHaveLength(0);
  });
});
