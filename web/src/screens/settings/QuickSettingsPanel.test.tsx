import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";

import { I18nProvider } from "../../i18n/I18nProvider";
import { en } from "../../i18n/strings";
import { PrefsProvider } from "../../mail/PrefsProvider";
import { DEFAULT_PREFS, type Prefs } from "../../mail/prefs";
import { QuickSettingsPanel } from "./QuickSettingsPanel";

/**
 * The quick-settings dock (E12/B2).
 *
 * What these cover is the part a screenshot cannot: that the panel is NOT a
 * dialog (it must stay out of modal semantics, because the page behind it is
 * genuinely interactive), that Escape and focus behave, and — the one that
 * matters most — that each control writes the SAME preference key the full
 * settings surface writes. The panel is a second surface over one source of
 * truth, and the failure mode of getting that wrong is a setting that appears
 * to change in one place and not the other.
 *
 * What is NOT tested here: that the thumbnails look like what they describe.
 * That is a screenshot's job, and the director's side-by-side gate is where it
 * belongs.
 */

/** The panel as the shell mounts it: behind a gear that owns `isOpen`. */
function Harness({
  initialPrefs = DEFAULT_PREFS,
  onOpenFullSettings = () => undefined,
}: {
  readonly initialPrefs?: Prefs;
  readonly onOpenFullSettings?: () => void;
} = {}): React.JSX.Element {
  const [open, setOpen] = useState(false);
  return (
    <I18nProvider locale="en">
      <PrefsProvider
        client={undefined}
        session={undefined}
        accountId=""
        initialPrefs={initialPrefs}
      >
        <button type="button" onClick={() => { setOpen(true); }}>
          {en["settings.open"]}
        </button>
        <QuickSettingsPanel
          isOpen={open}
          onClose={() => { setOpen(false); }}
          onOpenFullSettings={onOpenFullSettings}
        />
      </PrefsProvider>
    </I18nProvider>
  );
}

function gear(): HTMLElement {
  return screen.getByRole("button", { name: en["settings.open"] });
}

describe("the dock's shape", () => {
  it("renders nothing at all until it is opened", () => {
    render(<Harness />);
    expect(screen.queryByRole("complementary")).not.toBeInTheDocument();
  });

  it("is a labelled complementary landmark, never a dialog", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(gear());

    /*
     * The load-bearing assertion of this file. `role="dialog"` on a panel that
     * leaves the page interactive makes a screen reader announce a modal
     * context that does not exist, and some will refuse to let the user tab
     * out of it — into the mail the panel is there to help them look at.
     */
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(
      screen.getByRole("complementary", { name: en["quickSettings.title"] }),
    ).toBeInTheDocument();
  });

  it("puts 'See all settings' at the top, above the option groups", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(gear());

    const panel = screen.getByRole("complementary");
    const seeAll = screen.getByRole("button", { name: en["quickSettings.seeAll"] });
    const firstGroup = within(panel).getAllByRole("group")[0];
    expect(firstGroup).toBeDefined();
    // Gmail's own order: the door to the other twenty settings is the first
    // thing you see, not something below the previews.
    expect(
      seeAll.compareDocumentPosition(firstGroup as Node) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  /*
   * A real finding, pinned rather than papered over.
   *
   * Two groups in this panel both offer an option called "Default"/"Normal" —
   * the density and the inbox type. On screen they are unambiguous, because
   * each sits under its own heading beside its own picture. In the
   * ACCESSIBILITY tree they are only distinguishable through the fieldset that
   * contains them, which is exactly what the `<fieldset>`/`<legend>` pairing
   * provides and what a `<div>` with a styled heading would not have.
   *
   * This test is the guard on that: if the legends were ever dropped for
   * "simpler" markup, two identically-named radios would become genuinely
   * indistinguishable to a screen-reader user, and nothing else in the suite
   * would notice.
   */
  it("disambiguates the two 'Default' options by their group, not by their label", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(gear());

    expect(screen.getAllByRole("radio", { name: en["settings.density.default"] })).toHaveLength(2);

    const densityGroup = screen.getByRole("group", {
      name: en["settings.density.label"],
    });
    const inboxGroup = screen.getByRole("group", {
      name: en["settings.inboxType.label"],
    });
    expect(
      within(densityGroup).getByRole("radio", { name: en["settings.density.default"] }),
    ).not.toBe(
      within(inboxGroup).getByRole("radio", { name: en["settings.inboxType.default"] }),
    );
  });

  it("offers all four groups, each named by its own legend", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(gear());

    for (const name of [
      en["settings.density.label"],
      en["theme.label"],
      en["settings.inboxType.label"],
      en["settings.readingPane.label"],
    ]) {
      expect(screen.getByRole("group", { name })).toBeInTheDocument();
    }
  });
});

describe("the controls write the real preferences", () => {
  it("shows the current value of every preference as the checked radio", async () => {
    const user = userEvent.setup();
    render(
      <Harness
        initialPrefs={{
          ...DEFAULT_PREFS,
          density: "compact",
          readingPane: "bottom",
          inboxType: "unread_first",
        }}
      />,
    );
    await user.click(gear());

    expect(
      screen.getByRole("radio", { name: en["settings.density.compact"] }),
    ).toBeChecked();
    expect(
      screen.getByRole("radio", { name: en["settings.readingPane.bottom"] }),
    ).toBeChecked();
    expect(
      screen.getByRole("radio", { name: en["settings.inboxType.unread_first"] }),
    ).toBeChecked();
  });

  it("changing density moves the checked radio — one source of truth", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(gear());

    await user.click(screen.getByRole("radio", { name: en["settings.density.compact"] }));

    /*
     * The assertion is on the PROVIDER's state reflected back into the panel,
     * not on a local `useState` the panel might have kept: the panel holds no
     * preference state of its own, so a radio that moved proves the write
     * reached `PrefsProvider` and came back.
     */
    expect(
      screen.getByRole("radio", { name: en["settings.density.compact"] }),
    ).toBeChecked();
    expect(
      within(screen.getByRole("group", { name: en["settings.density.label"] })).getByRole(
        "radio",
        { name: en["settings.density.default"] },
      ),
    ).not.toBeChecked();
  });

  it("the reading pane and the theme write independently", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(gear());

    await user.click(screen.getByRole("radio", { name: en["settings.readingPane.none"] }));
    await user.click(screen.getByRole("radio", { name: en["theme.dark"] }));

    expect(
      screen.getByRole("radio", { name: en["settings.readingPane.none"] }),
    ).toBeChecked();
    expect(screen.getByRole("radio", { name: en["theme.dark"] })).toBeChecked();
    // The density was never touched and must not have moved: four groups over
    // one prefs object is exactly where a patch that sent the whole object
    // would clobber a neighbour.
    expect(
      within(screen.getByRole("group", { name: en["settings.density.label"] })).getByRole(
        "radio",
        { name: en["settings.density.default"] },
      ),
    ).toBeChecked();
  });
});

describe("dismissal and focus", () => {
  it("closes on Escape and gives focus back to whatever opened it", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(gear());
    expect(screen.getByRole("complementary")).toBeInTheDocument();

    await user.keyboard("{Escape}");

    expect(screen.queryByRole("complementary")).not.toBeInTheDocument();
    expect(gear()).toHaveFocus();
  });

  it("closes from its own X", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(gear());

    await user.click(screen.getByRole("button", { name: en["quickSettings.close"] }));

    expect(screen.queryByRole("complementary")).not.toBeInTheDocument();
  });

  it("hands off to the full surface rather than opening it beside itself", async () => {
    const user = userEvent.setup();
    const onOpenFullSettings = vi.fn();
    render(<Harness onOpenFullSettings={onOpenFullSettings} />);
    await user.click(gear());

    await user.click(screen.getByRole("button", { name: en["quickSettings.seeAll"] }));

    expect(onOpenFullSettings).toHaveBeenCalledTimes(1);
  });
});

