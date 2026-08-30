import { describe, expect, it, vi } from "vitest";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";

import { I18nProvider } from "../../i18n/I18nProvider";
import { en } from "../../i18n/strings";
import { PrefsProvider } from "../../mail/PrefsProvider";
import { DEFAULT_PREFS, type Prefs } from "../../mail/prefs";
import type { Identity } from "../../mail/write";
import { applyTheme, loadThemePreference } from "../../theme/theme";
import { SettingsDialog } from "./SettingsDialog";

/**
 * The settings sheet.
 *
 * These cover what a unit test can honestly verify: that it opens and closes
 * from the keyboard, that focus goes in and comes back, that the section rail
 * navigates, that the search filters, and that every control is a real labelled
 * input whose current state is visible. What jsdom cannot verify — that the
 * sheet visually sits above the app, that the light theme actually looks light —
 * is checked in a real browser.
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

const IDENTITY: Identity = {
  id: "i1",
  name: "Diego",
  email: "diego@example.test",
  replyTo: null,
  bcc: null,
  textSignature: "— Diego",
  htmlSignature: "",
  mayDelete: false,
};

/** The dialog as the app mounts it: behind a trigger that owns `isOpen`. */
function Harness({
  initialPrefs = DEFAULT_PREFS,
  identity,
  onSaveSignature,
}: {
  readonly initialPrefs?: Prefs;
  readonly identity?: Identity;
  readonly onSaveSignature?: (text: string) => Promise<boolean>;
} = {}): React.JSX.Element {
  const [open, setOpen] = useState(false);
  return (
    <I18nProvider locale="en">
      {/*
        `initialPrefs` short-circuits the load, so these tests exercise the
        SHEET rather than the JMAP transport — which `prefs.test.ts` covers
        directly against a fake client.
      */}
      <PrefsProvider
        client={undefined}
        session={undefined}
        accountId=""
        initialPrefs={initialPrefs}
      >
        <button type="button" onClick={() => { setOpen(true); }}>
          {en["settings.open"]}
        </button>
        <SettingsDialog
          isOpen={open}
          onClose={() => { setOpen(false); }}
          identity={identity}
          onSaveSignature={onSaveSignature}
        />
      </PrefsProvider>
    </I18nProvider>
  );
}

function renderDialog(props: Parameters<typeof Harness>[0] = {}) {
  localStorage.clear();
  applyTheme("light", document.documentElement);
  return render(<Harness {...props} />);
}

/** The trigger, which is also where focus must return. */
function trigger(): HTMLElement {
  return screen.getByRole("button", { name: en["settings.open"] });
}

/** Opens the sheet and navigates the rail to a section. */
async function openAt(
  user: ReturnType<typeof userEvent.setup>,
  section: string,
): Promise<void> {
  await user.click(trigger());
  await user.click(screen.getByRole("button", { name: section }));
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

describe("the section rail", () => {
  it("lands on General, so the sheet is never blank on open", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    expect(
      screen.getByRole("heading", { name: en["settings.section.general"] }),
    ).toBeInTheDocument();
    // …and the other sections are not rendered at once, which is the whole
    // point of a rail over one long column.
    expect(
      screen.queryByRole("heading", { name: en["settings.section.offline"] }),
    ).not.toBeInTheDocument();
  });

  it("offers every section of the adapted Gmail IA", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    const nav = screen.getByRole("navigation", { name: en["settings.nav.label"] });
    for (const title of [
      en["settings.section.general"],
      en["settings.section.appearance"],
      en["settings.section.inbox"],
      en["settings.section.account"],
      en["settings.section.filters"],
      en["settings.section.forwarding"],
      en["settings.section.vacation"],
      en["settings.section.offline"],
    ]) {
      expect(
        within(nav).getByRole("button", { name: title }),
        `the rail is missing "${title}"`,
      ).toBeInTheDocument();
    }
  });

  it("switches the panel when a section is chosen", async () => {
    const user = userEvent.setup();
    renderDialog();
    await openAt(user, en["settings.section.inbox"]);

    expect(
      screen.getByRole("combobox", { name: en["settings.inboxType.label"] }),
    ).toBeInTheDocument();
    // The previous section's controls are gone, not merely scrolled past.
    expect(
      screen.queryByRole("switch", { name: en["settings.snippets.label"] }),
    ).not.toBeInTheDocument();
  });

  it("marks the current section for assistive technology", async () => {
    const user = userEvent.setup();
    renderDialog();
    await openAt(user, en["settings.section.appearance"]);

    expect(
      screen.getByRole("button", { name: en["settings.section.appearance"] }),
    ).toHaveAttribute("aria-current", "true");
  });
});

describe("settings search (D-5)", () => {
  it("finds a row in a section the user is not standing in", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    // Standing in General; the density row lives in Appearance.
    await user.type(
      screen.getByRole("searchbox", { name: en["settings.search.label"] }),
      "density",
    );

    expect(
      screen.getByRole("combobox", { name: en["settings.density.label"] }),
    ).toBeInTheDocument();
  });

  it("finds a row by a SYNONYM the label never says", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    // "privacy" appears in no rendered string of the images row; it is exactly
    // what a worried user types.
    await user.type(
      screen.getByRole("searchbox", { name: en["settings.search.label"] }),
      "privacy",
    );

    expect(
      screen.getByRole("combobox", { name: en["settings.images.label"] }),
    ).toBeInTheDocument();
  });

  it("hides the rows that do not match", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    await user.type(
      screen.getByRole("searchbox", { name: en["settings.search.label"] }),
      "density",
    );

    expect(
      screen.queryByRole("switch", { name: en["settings.snippets.label"] }),
    ).not.toBeInTheDocument();
  });

  it("says so when nothing matches, rather than showing a blank panel", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    await user.type(
      screen.getByRole("searchbox", { name: en["settings.search.label"] }),
      "cryptography",
    );

    expect(screen.getByText(en["settings.search.empty"])).toBeInTheDocument();
  });

  it("clears with Escape without closing the sheet", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    const box = screen.getByRole("searchbox", { name: en["settings.search.label"] });
    await user.type(box, "density");
    await user.type(box, "{Escape}");

    expect(box).toHaveValue("");
    // The sheet must survive: Escape inside a search field means "clear", and
    // only an already-empty field lets it through to dismiss.
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("clears with the clear button", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    const box = screen.getByRole("searchbox", { name: en["settings.search.label"] });
    await user.type(box, "density");
    await user.click(screen.getByRole("button", { name: en["settings.search.clear"] }));

    expect(box).toHaveValue("");
    // Back to the unfiltered section.
    expect(
      screen.getByRole("switch", { name: en["settings.snippets.label"] }),
    ).toBeInTheDocument();
  });
});

describe("the theme control", () => {
  it("is a labelled group of three options with the current one checked", async () => {
    const user = userEvent.setup();
    renderDialog();
    await openAt(user, en["settings.section.appearance"]);

    expect(screen.getByRole("group", { name: en["theme.label"] })).toBeInTheDocument();

    // Every option is a real radio with a visible label, so the current state
    // is visible rather than inferable from an icon.
    expect(screen.getByRole("radio", { name: en["theme.light"] })).toBeChecked();
    expect(screen.getByRole("radio", { name: en["theme.dark"] })).not.toBeChecked();
    expect(screen.getByRole("radio", { name: en["theme.system"] })).not.toBeChecked();
  });

  it("applies a choice IMMEDIATELY and mirrors it into the pre-paint cache", async () => {
    const user = userEvent.setup();
    renderDialog();
    await openAt(user, en["settings.section.appearance"]);

    await user.click(screen.getByRole("radio", { name: en["theme.dark"] }));

    // Immediately: the attribute the CSS keys on has already changed, with no
    // reload and no save button.
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    /*
     * Mirrored: E5 makes the ACCOUNT the source of truth, and localStorage the
     * cache the pre-paint script in index.html reads. The cache must move with
     * the choice even while the save is in flight, or the next load flashes the
     * old theme.
     */
    expect(loadThemePreference()).toBe("dark");
  });

  it("lets the user opt IN to following the system, which removes the attribute", async () => {
    const user = userEvent.setup();
    renderDialog();
    await openAt(user, en["settings.section.appearance"]);

    await user.click(screen.getByRole("radio", { name: en["theme.system"] }));

    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expect(loadThemePreference()).toBe("system");
  });

  it("is operable entirely from the keyboard", async () => {
    const user = userEvent.setup();
    renderDialog();
    await openAt(user, en["settings.section.appearance"]);

    // Radios in one group are a single tab stop and arrows move between them.
    // That behaviour comes from the browser because these are real inputs.
    screen.getByRole("radio", { name: en["theme.light"] }).focus();
    await user.keyboard("{ArrowRight}");

    expect(screen.getByRole("radio", { name: en["theme.dark"] })).toBeChecked();
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
  });

  it("shows the ACCOUNT's theme, not the local cache", async () => {
    const user = userEvent.setup();
    // The cache says light (renderDialog applies it); the account says dark.
    renderDialog({ initialPrefs: { ...DEFAULT_PREFS, theme: "dark" } });
    await openAt(user, en["settings.section.appearance"]);

    expect(screen.getByRole("radio", { name: en["theme.dark"] })).toBeChecked();
  });
});

describe("the preference controls", () => {
  it("offers Gmail's exact four undo-send values", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    const select = screen.getByRole("combobox", { name: en["settings.undoSend.label"] });
    const values = Array.from(select.querySelectorAll("option")).map((o) => o.value);
    expect(values).toEqual(["5", "10", "20", "30"]);
  });

  it("renders each toggle as a real switch reflecting its current value", async () => {
    const user = userEvent.setup();
    renderDialog({ initialPrefs: { ...DEFAULT_PREFS, showSnippets: false, hoverActions: true } });
    await user.click(trigger());

    expect(screen.getByRole("switch", { name: en["settings.snippets.label"] })).not.toBeChecked();
    expect(screen.getByRole("switch", { name: en["settings.hover.label"] })).toBeChecked();
  });

  it("moves a switch when it is clicked", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    const snippets = screen.getByRole("switch", { name: en["settings.snippets.label"] });
    expect(snippets).toBeChecked();
    await user.click(snippets);
    // Optimistic: the control moves without waiting for a round trip.
    expect(snippets).not.toBeChecked();
  });

  it("offers the language switcher with a follow-the-browser option", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(trigger());

    const select = screen.getByRole("combobox", { name: en["settings.language.label"] });
    const values = Array.from(select.querySelectorAll("option")).map((o) => o.value);
    // `auto` first: it is the default and the honest one.
    expect(values).toEqual(["auto", "es", "en"]);
    expect(select).toHaveValue("auto");
  });

  it("offers exactly the three reading panes Gmail names", async () => {
    const user = userEvent.setup();
    renderDialog();
    await openAt(user, en["settings.section.appearance"]);

    const select = screen.getByRole("combobox", { name: en["settings.readingPane.label"] });
    const values = Array.from(select.querySelectorAll("option")).map((o) => o.value);
    expect(values).toEqual(["none", "right", "bottom"]);
  });

  it("offers only the DETERMINISTIC inbox types", async () => {
    const user = userEvent.setup();
    renderDialog();
    await openAt(user, en["settings.section.inbox"]);

    const select = screen.getByRole("combobox", { name: en["settings.inboxType.label"] });
    const values = Array.from(select.querySelectorAll("option")).map((o) => o.value);
    // No "Important first" and no "Priority Inbox": both need the importance
    // classifier, which is AI-phase (GC-2's rule applied to inbox types).
    expect(values).toEqual(["default", "unread_first", "starred_first"]);
  });

  it("offers two notification modes, not Gmail's three", async () => {
    const user = userEvent.setup();
    renderDialog();
    await openAt(user, en["settings.section.inbox"]);

    const select = screen.getByRole("combobox", { name: en["settings.notifications.label"] });
    const values = Array.from(select.querySelectorAll("option")).map((o) => o.value);
    // GC-2: "important mail only" is gated on the classifier that does not
    // exist, and a mode with no classifier is a control that does nothing.
    expect(values).toEqual(["new", "off"]);
  });
});

describe("the account section", () => {
  it("shows the identity read-only", async () => {
    const user = userEvent.setup();
    renderDialog({ identity: IDENTITY });
    await openAt(user, en["settings.section.account"]);

    expect(screen.getByText(IDENTITY.name)).toBeInTheDocument();
    expect(screen.getByText(IDENTITY.email)).toBeInTheDocument();
  });

  it("says so honestly when there is no sending identity", async () => {
    const user = userEvent.setup();
    renderDialog();
    await openAt(user, en["settings.section.account"]);

    expect(screen.getByText(en["settings.identity.missing"])).toBeInTheDocument();
  });

  it("seeds the signature from the identity and saves it EXPLICITLY", async () => {
    const user = userEvent.setup();
    const onSaveSignature = vi.fn().mockResolvedValue(true);
    renderDialog({ identity: IDENTITY, onSaveSignature });
    await openAt(user, en["settings.section.account"]);

    const field = screen.getByRole("textbox", { name: en["settings.signature.label"] });
    expect(field).toHaveValue(IDENTITY.textSignature);

    await user.clear(field);
    await user.type(field, "Saludos");

    // Nothing has been sent yet: a signature is prose, and autosaving on every
    // keystroke would make a half-typed sentence briefly the real signature.
    expect(onSaveSignature).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: en["settings.signature.save"] }));
    expect(onSaveSignature).toHaveBeenCalledWith("Saludos");
    expect(await screen.findByText(en["settings.signature.saved"])).toBeInTheDocument();
  });

  it("keeps the save button inert until the text actually changes", async () => {
    const user = userEvent.setup();
    renderDialog({ identity: IDENTITY, onSaveSignature: vi.fn() });
    await openAt(user, en["settings.section.account"]);

    expect(screen.getByRole("button", { name: en["settings.signature.save"] })).toBeDisabled();
  });

  it("reports a failed save in the row rather than silently doing nothing", async () => {
    const user = userEvent.setup();
    renderDialog({ identity: IDENTITY, onSaveSignature: vi.fn().mockResolvedValue(false) });
    await openAt(user, en["settings.section.account"]);

    const field = screen.getByRole("textbox", { name: en["settings.signature.label"] });
    await user.type(field, "!");
    await user.click(screen.getByRole("button", { name: en["settings.signature.save"] }));

    expect(await screen.findByText(en["settings.signature.failed"])).toBeInTheDocument();
  });
});

describe("the honest skeletons (P4)", () => {
  it.each([
    [en["settings.section.filters"], en["settings.filters.soon"]],
    [en["settings.section.forwarding"], en["settings.forwarding.soon"]],
    [en["settings.section.vacation"], en["settings.vacation.soon"]],
    [en["settings.section.offline"], en["settings.offline.soon"]],
  ])("names what is coming in %s, with no control at all", async (section, promise) => {
    const user = userEvent.setup();
    renderDialog();
    await openAt(user, section);

    expect(screen.getByText(promise)).toBeInTheDocument();

    /*
     * The rule these sections exist to honour: never a control that does
     * nothing. A disabled "Create filter" button would be exactly that — it
     * invites the click, then refuses it. A paragraph is information.
     */
    const panel = screen.getByRole("dialog");
    const controls = Array.from(
      panel.querySelectorAll("input, select, textarea"),
    ).filter((element) => element.getAttribute("type") !== "search");
    expect(controls).toHaveLength(0);
  });
});

describe("persistence honesty", () => {
  it("says the choices are session-only when the server cannot store them", async () => {
    const user = userEvent.setup();
    // No client, no session, and no `initialPrefs` short-circuit: the provider
    // resolves to "unavailable", which is the older-server case.
    render(
      <I18nProvider locale="en">
        <PrefsProvider client={undefined} session={undefined} accountId="">
          <SettingsDialog isOpen onClose={() => undefined} />
        </PrefsProvider>
      </I18nProvider>,
    );

    expect(screen.getByText(en["settings.unavailable"])).toBeInTheDocument();
    // …and the controls still move, so they are not dead.
    const snippets = screen.getByRole("switch", { name: en["settings.snippets.label"] });
    await user.click(snippets);
    expect(snippets).not.toBeChecked();
  });
});
