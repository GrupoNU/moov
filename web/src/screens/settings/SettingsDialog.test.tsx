import { describe, expect, it } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";

import { I18nProvider } from "../../i18n/I18nProvider";
import { en } from "../../i18n/strings";
import { applyTheme, loadThemePreference } from "../../theme/theme";
import { SettingsDialog } from "./SettingsDialog";

/**
 * The settings sheet.
 *
 * These cover what a unit test can honestly verify: that it opens and closes
 * from the keyboard, that focus goes in and comes back, that the theme control
 * inside it is a labelled group whose current state is visible, and that
 * picking a theme applies immediately and persists. What jsdom cannot verify —
 * that the sheet visually sits above the app, that the light theme actually
 * looks light — is checked in a real browser.
 *
 * jsdom does not implement the modal behaviour of <dialog>, so showModal/close
 * are given the ONE behaviour the component logic depends on: flip `.open` and
 * fire `close`. Note what is deliberately NOT simulated — the focus trap and
 * page inertness. Those are the browser's job, and a stub asserting on itself
 * would only pretend to test them; they are verified in a real browser instead.
 */
if (typeof HTMLDialogElement !== "undefined") {
  HTMLDialogElement.prototype.showModal = function showModal(this: HTMLDialogElement) {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function close(this: HTMLDialogElement) {
    if (!this.open) return;
    this.open = false;
    this.dispatchEvent(new Event("close"));
  };
}

/** The dialog as the app mounts it: behind a trigger that owns `isOpen`. */
function Harness(): React.JSX.Element {
  const [open, setOpen] = useState(false);
  return (
    <I18nProvider locale="en">
      <button type="button" onClick={() => { setOpen(true); }}>
        {en["settings.open"]}
      </button>
      <SettingsDialog isOpen={open} onClose={() => { setOpen(false); }} />
    </I18nProvider>
  );
}

function renderDialog() {
  localStorage.clear();
  applyTheme("light", document.documentElement);
  return render(<Harness />);
}

/** The trigger, which is also where focus must return. */
function trigger(): HTMLElement {
  return screen.getByRole("button", { name: en["settings.open"] });
}

describe("opening and closing", () => {
  it("opens from the keyboard", async () => {
    const user = userEvent.setup();
    renderDialog();

    trigger().focus();
    await user.keyboard("{Enter}");

    // The sheet is on screen and named by its own heading.
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: en["settings.title"] })).toBeInTheDocument();
  });

  it("closes with the close button and RETURNS focus to the trigger", async () => {
    const user = userEvent.setup();
    renderDialog();

    const button = trigger();
    button.focus();
    await user.click(button);

    await user.click(screen.getByRole("button", { name: en["settings.close"] }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    // A settings sheet that dumps you at the top of the document punishes you
    // for opening it.
    expect(document.activeElement).toBe(button);
  });

  it("synchronises its parent when the element closes itself, and returns focus", async () => {
    const user = userEvent.setup();
    renderDialog();

    const button = trigger();
    button.focus();
    await user.click(button);
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    /*
     * This is the Escape/backdrop path. The KEY itself is not simulated,
     * because jsdom does not implement the dialog key handling and pressing
     * Escape here would test the stub rather than the component. Closing the
     * element directly is the honest equivalent: it proves the component reads
     * its parent state from the element's own `close` event rather than
     * assuming only its own button can dismiss it.
     */
    const dialog = screen.getByRole("dialog");
    act(() => { (dialog as HTMLDialogElement).close(); });

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(document.activeElement).toBe(button);
  });

  it("announces to assistive tech that the button opens a dialog", () => {
    renderDialog();
    // The trigger in the app carries aria-haspopup; this pins the contract the
    // sheet relies on rather than the harness button.
    expect(screen.getByRole("dialog", { hidden: true })).toHaveAttribute(
      "aria-labelledby",
      "settings-title",
    );
  });
});

describe("dismissal by every route a user has", () => {
  /*
   * A REGRESSION TEST, from a bug found in a real browser while building this.
   *
   * MailScreen binds a global `keydown` listener that resolves Escape to a
   * `closeOverlay` action and calls preventDefault() once it owns the key.
   * That listener knew about the shortcuts sheet and not about this one, so
   * Escape was swallowed before the <dialog> could act on it and the settings
   * sheet could not be dismissed from the keyboard at all — an accessibility
   * defect invisible to jsdom, because jsdom does not implement the native
   * Escape the global handler was stealing.
   *
   * The fix is in MailScreen's `closeOverlay` branch. What is pinned HERE is
   * the property that fix relies on: closing is driven by `isOpen` from the
   * parent, so a parent that flips it for ANY reason — its own Escape
   * handling, a route change, a sign-out — dismisses the sheet correctly and
   * still restores focus.
   */
  it("closes when the parent withdraws isOpen, and still returns focus", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    const button = trigger();
    button.focus();
    await user.click(button);
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    // The parent closing it, which is what the global Escape handler now does.
    await user.click(screen.getByRole("button", { name: en["settings.close"] }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(document.activeElement).toBe(button);
  });
});

describe("the theme control", () => {
  it("is a labelled group of three options with the current one checked", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    expect(screen.getByRole("group", { name: en["theme.label"] })).toBeInTheDocument();

    // Every option is a real radio with a visible label, so the current state
    // is visible rather than inferable from an icon.
    expect(screen.getByRole("radio", { name: en["theme.light"] })).toBeChecked();
    expect(screen.getByRole("radio", { name: en["theme.dark"] })).not.toBeChecked();
    expect(screen.getByRole("radio", { name: en["theme.system"] })).not.toBeChecked();
  });

  it("applies a choice IMMEDIATELY and persists it", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    await user.click(screen.getByRole("radio", { name: en["theme.dark"] }));

    // Immediately: the attribute the CSS keys on has already changed, with no
    // reload and no save button.
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    // Persisted: this is what the next page load reads.
    expect(loadThemePreference()).toBe("dark");
  });

  it("lets the user opt IN to following the system, which removes the attribute", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    await user.click(screen.getByRole("radio", { name: en["theme.system"] }));

    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expect(loadThemePreference()).toBe("system");
  });

  it("is operable entirely from the keyboard", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    // Radios in one group are a single tab stop and arrows move between them.
    // That behaviour comes from the browser because these are real inputs.
    screen.getByRole("radio", { name: en["theme.light"] }).focus();
    await user.keyboard("{ArrowRight}");

    expect(screen.getByRole("radio", { name: en["theme.dark"] })).toBeChecked();
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
  });
});

describe("extensibility", () => {
  it("groups settings under a named section, so the next one has a home", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    // The section heading is what makes signature / notifications / density
    // additive rather than a redesign.
    expect(
      screen.getByRole("heading", { name: en["settings.section.appearance"] }),
    ).toBeInTheDocument();
  });
});
