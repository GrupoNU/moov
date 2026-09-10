import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";

import { I18nProvider } from "../../i18n/I18nProvider";
import { en, es } from "../../i18n/strings";
import { PrefsProvider } from "../../mail/PrefsProvider";
import { DEFAULT_PREFS, type Prefs } from "../../mail/prefs";
import type { Identity } from "../../mail/write";
import { DEFAULT_SETTINGS_TAB, type SettingsTab } from "../../router/routes";
import { CAP_PREFS } from "../../mail/prefs";
import { JmapClient, type JmapSession } from "../../api/jmap";
import { applyTheme } from "../../theme/theme";
import type { BrandSectionProps } from "./BrandSection";
import type { BrandAdminDoc } from "../../branding/adminApi";
import { SettingsPage } from "./SettingsPage";

/**
 * The settings PAGE.
 *
 * These cover what a unit test can honestly verify: that the tab row navigates
 * and is a real APG tablist, that the search filters ACROSS tabs, that every
 * control is a labelled input whose current state is visible, and that a
 * missing capability renders a named absence rather than a dead control. What
 * jsdom cannot verify — that the page visually replaces the list while the rail
 * stays put — is checked in a real browser.
 *
 * # What E12 deleted from this file, and why the deletions are the point
 *
 * Three describes are gone: "opening and closing", "dismissal by every route a
 * user has", and the `<dialog>` stub that made them possible. They tested
 * `showModal()`, focus return to a trigger, and the parent's `isOpen` — the
 * mechanics of a modal, none of which a page has or should have. Keeping them
 * against a routed page would have meant asserting that a page behaves like a
 * dialog, which is the opposite of what B3 decided.
 *
 * What REPLACES them is the tab-row coverage below plus MailScreen's own canary
 * (which walks gear → quick panel → page) and `routes.test.ts` (which pins the
 * URL round trip). Between them, every property the deleted tests protected —
 * the surface opens, it can be left, it can be reached — is still asserted; it
 * is asserted about a destination rather than about an overlay.
 */

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

/**
 * The page as the shell mounts it: the TAB comes from the route, so the harness
 * owns it exactly as `MailScreen` owns the router's answer.
 */
function Harness({
  initialPrefs = DEFAULT_PREFS,
  identity,
  onSaveSignature,
  initialTab = DEFAULT_SETTINGS_TAB,
  onClose = () => undefined,
  onOpenQuickSettings,
  brand,
}: {
  readonly initialPrefs?: Prefs;
  readonly identity?: Identity;
  readonly onSaveSignature?: (text: string) => Promise<boolean>;
  readonly initialTab?: SettingsTab;
  readonly onClose?: () => void;
  readonly onOpenQuickSettings?: () => void;
  /** L2-brand-admin: present only for an administrator of this host. */
  readonly brand?: BrandSectionProps;
} = {}): React.JSX.Element {
  const [tab, setTab] = useState<SettingsTab>(initialTab);
  return (
    <I18nProvider locale="en">
      {/*
        `initialPrefs` short-circuits the load, so these tests exercise the
        PAGE rather than the JMAP transport — which `prefs.test.ts` covers
        directly against a fake client.
      */}
      <PrefsProvider
        client={undefined}
        session={undefined}
        accountId=""
        initialPrefs={initialPrefs}
      >
        <SettingsPage
          tab={tab}
          onSelectTab={setTab}
          onClose={onClose}
          onOpenQuickSettings={onOpenQuickSettings}
          identity={identity}
          onSaveSignature={onSaveSignature}
          brand={brand}
        />
      </PrefsProvider>
    </I18nProvider>
  );
}

function renderPage(props: Parameters<typeof Harness>[0] = {}) {
  localStorage.clear();
  applyTheme("light", document.documentElement);
  return render(<Harness {...props} />);
}

/**
 * Renders the page and navigates the tab row to a tab.
 *
 * It CLICKS the tab rather than passing `initialTab`, deliberately: most of
 * these tests want the control they are about to assert on to have arrived
 * through the navigation a user performs, not to have been mounted directly.
 */
async function openAt(
  user: ReturnType<typeof userEvent.setup>,
  tabName: string,
): Promise<void> {
  await user.click(screen.getByRole("tab", { name: tabName }));
}

describe("the tab row", () => {
  it("lands on the tab the route names, so the page is never blank", () => {
    renderPage();

    expect(
      screen.getByRole("heading", { name: en["settings.section.general"] }),
    ).toBeInTheDocument();
    // …and the other tabs' sections are not rendered at once, which is the
    // whole point of tabs over one long column.
    expect(
      screen.queryByRole("heading", { name: en["settings.section.offline"] }),
    ).not.toBeInTheDocument();
  });

  it("opens directly on a deep-linked tab — the reason settings became a route", () => {
    renderPage({ initialTab: "offline" });

    expect(
      screen.getByRole("heading", { name: en["settings.section.offline"] }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: en["settings.section.general"] }),
    ).not.toBeInTheDocument();
  });

  it("offers every tab of the adapted Gmail IA (canon 07 §5)", () => {
    renderPage();

    const tabs = screen.getByRole("tablist", { name: en["settings.tabs.label"] });
    for (const title of [
      en["settings.section.general"],
      en["settings.section.labels"],
      en["settings.section.inbox"],
      en["settings.section.account"],
      en["settings.tab.filters"],
      en["settings.section.forwarding"],
      en["settings.section.offline"],
    ]) {
      expect(
        within(tabs).getByRole("tab", { name: title }),
        `the tab row is missing "${title}"`,
      ).toBeInTheDocument();
    }
  });

  it("switches the panel when a tab is chosen", async () => {
    const user = userEvent.setup();
    renderPage();
    await openAt(user, en["settings.section.inbox"]);

    // F-34/F-35: a radio GROUP with previews, not a select. The fieldset's
    // legend is what names it — visually hidden, because the row's own left
    // column already prints the setting's name.
    expect(
      screen.getByRole("group", { name: en["settings.inboxType.label"] }),
    ).toBeInTheDocument();
    // The previous tab's controls are gone, not merely scrolled past.
    expect(
      screen.queryByRole("switch", { name: en["settings.snippets.label"] }),
    ).not.toBeInTheDocument();
  });

  it("folds blocked senders into the filters tab, as Gmail does", async () => {
    const user = userEvent.setup();
    renderPage();
    await openAt(user, en["settings.tab.filters"]);

    // ONE tab, TWO sections. The fold is honest here in a way it is only
    // conventional at Google: both are the same Sieve script on our server.
    expect(
      screen.getByRole("heading", { name: en["settings.section.filters"] }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: en["settings.section.blocked"] }),
    ).toBeInTheDocument();
  });

  /*
   * F-47. The review saw "General" as the tab and "GENERAL" in small caps 40px
   * below it — the same word twice, on six of the seven tabs. The heading is
   * hidden rather than deleted, so these assert BOTH halves: it is out of sight
   * where it duplicates the tab, and it is still in the accessibility tree
   * naming its section.
   */
  it("hides the section heading on a tab that holds only one section", () => {
    renderPage();

    const heading = screen.getByRole("heading", { name: en["settings.section.general"] });
    // Still named, still findable by a screen reader — and off the screen.
    expect(heading).toHaveClass("visually-hidden");
  });

  it("shows both headings on the one tab that holds two sections", async () => {
    const user = userEvent.setup();
    renderPage();
    await openAt(user, en["settings.tab.filters"]);

    // Here the small caps do real work: they are the only thing separating two
    // lists of different objects.
    expect(
      screen.getByRole("heading", { name: en["settings.section.filters"] }),
    ).not.toHaveClass("visually-hidden");
    expect(
      screen.getByRole("heading", { name: en["settings.section.blocked"] }),
    ).not.toHaveClass("visually-hidden");
  });

  it("shows headings under a search, where the tabs no longer say where a hit lives", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.type(
      screen.getByRole("searchbox", { name: en["settings.search.label"] }),
      "language",
    );

    expect(
      screen.getByRole("heading", { name: en["settings.section.general"] }),
    ).not.toHaveClass("visually-hidden");
  });

  it("is a real APG tablist: one tab stop, arrows move within it", async () => {
    const user = userEvent.setup();
    renderPage();

    const general = screen.getByRole("tab", { name: en["settings.section.general"] });
    const labels = screen.getByRole("tab", { name: en["settings.section.labels"] });
    // Roving tabindex: only the selected tab is reachable by Tab, which is what
    // makes a seven-tab row ONE stop rather than seven.
    expect(general).toHaveAttribute("tabindex", "0");
    expect(labels).toHaveAttribute("tabindex", "-1");

    general.focus();
    await user.keyboard("{ArrowRight}");

    expect(labels).toHaveAttribute("aria-selected", "true");
    expect(labels).toHaveFocus();
  });

  it("wraps at both ends rather than dead-ending", async () => {
    const user = userEvent.setup();
    renderPage();

    screen.getByRole("tab", { name: en["settings.section.general"] }).focus();
    await user.keyboard("{ArrowLeft}");

    // Left from the first tab lands on the LAST one.
    expect(
      screen.getByRole("tab", { name: en["settings.section.offline"] }),
    ).toHaveAttribute("aria-selected", "true");
  });

  it("names its panel after the selected tab", () => {
    renderPage({ initialTab: "account" });

    const panel = screen.getByRole("tabpanel");
    expect(panel).toHaveAttribute("aria-labelledby", "settings-tab-account");
  });
});

describe("leaving the page", () => {
  it("offers an explicit way back to mail, not only a keyboard path", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    renderPage({ onClose });

    await user.click(screen.getByRole("button", { name: en["settings.backToMail"] }));

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("is never a dialog — a page must be tabbable out of", () => {
    renderPage();
    /*
     * The inverse of what this file used to assert. The old sheet WAS a
     * `<dialog>` and three tests pinned its modal mechanics; B3 makes the
     * absence of those mechanics the property worth protecting, because a
     * settings surface that traps focus is a settings surface you cannot leave
     * by tabbing to the mail behind it.
     */
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});

describe("the quick-panel pointer (P0-7)", () => {
  /*
   * Theme and density have no CONTROL on this page — theirs lives in the quick
   * panel, where the change is visible as you make it. They used to render on
   * the Recibidos tab anyway, as rows that looked like settings and settled
   * nothing: the review called them dead rows, and Gmail has none.
   *
   * They now render ONLY as search results, which is the distinction that
   * matters: the registry keeps them so "densidad" finds something (D-5), and
   * what it finds is a way INTO the panel rather than an empty anchor.
   */
  it("puts no dead rows on the tab a user is reading", async () => {
    const user = userEvent.setup();
    renderPage({ onOpenQuickSettings: vi.fn() });
    await openAt(user, en["settings.section.inbox"]);

    expect(screen.queryByText(en["settings.inQuickPanel"])).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: en["settings.openQuickPanel"] }),
    ).not.toBeInTheDocument();
    // The live rows of the tab are untouched — this removed two rows, not four.
    expect(
      screen.getByRole("group", { name: en["settings.readingPane.label"] }),
    ).toBeInTheDocument();
  });

  it("never duplicates the control the panel owns", async () => {
    const user = userEvent.setup();
    renderPage();
    await openAt(user, en["settings.section.inbox"]);

    // Two live controls over one preference is the drift this codebase avoids
    // everywhere else; the theme radios belong to the panel.
    expect(screen.queryByRole("radio", { name: en["theme.dark"] })).not.toBeInTheDocument();
  });

  it("appears for a SEARCH, and opens the panel from there", async () => {
    const user = userEvent.setup();
    const onOpenQuickSettings = vi.fn();
    renderPage({ onOpenQuickSettings });

    await user.type(
      screen.getByRole("searchbox", { name: en["settings.search.label"] }),
      "density",
    );

    await user.click(screen.getByRole("button", { name: en["settings.openQuickPanel"] }));
    expect(onOpenQuickSettings).toHaveBeenCalledTimes(1);
  });

  it("degrades to plain text when no opener was wired", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.type(
      screen.getByRole("searchbox", { name: en["settings.search.label"] }),
      "density",
    );

    // A button that leads nowhere is the dead control P4 forbids; the sentence
    // still tells the user where to look.
    expect(
      screen.queryByRole("button", { name: en["settings.openQuickPanel"] }),
    ).not.toBeInTheDocument();
    expect(screen.getByText(en["settings.inQuickPanel"])).toBeInTheDocument();
  });
});

describe("settings search (D-5)", () => {
  it("finds a row in a TAB the user is not standing in", async () => {
    const user = userEvent.setup();
    renderPage();

    // Standing in General; the reading-pane row lives in the Inbox tab. Under a
    // search the tabs are suspended and every match renders wherever it lives,
    // because results hidden behind a tab are results that appear not to exist.
    await user.type(
      screen.getByRole("searchbox", { name: en["settings.search.label"] }),
      "reading pane",
    );

    expect(
      screen.getByRole("group", { name: en["settings.readingPane.label"] }),
    ).toBeInTheDocument();
  });

  it("still finds the rows whose CONTROL moved to the quick panel", async () => {
    const user = userEvent.setup();
    renderPage();

    /*
     * The reason `theme` and `density` keep their registry rows after B3 moved
     * their controls out. A user who types "density" here must be told where
     * the control is; finding nothing would look like the setting had been
     * removed, and the search silently returning empty for two real settings is
     * the exact failure D-5 exists to prevent.
     */
    await user.type(
      screen.getByRole("searchbox", { name: en["settings.search.label"] }),
      "density",
    );

    expect(screen.getByText(en["settings.inQuickPanel"])).toBeInTheDocument();
    expect(screen.queryByText(en["settings.search.empty"])).not.toBeInTheDocument();
  });

  it("finds a row by a SYNONYM the label never says", async () => {
    const user = userEvent.setup();
    renderPage();

    // "privacy" appears in no rendered string of the images row; it is exactly
    // what a worried user types.
    await user.type(
      screen.getByRole("searchbox", { name: en["settings.search.label"] }),
      "privacy",
    );

    expect(
      screen.getByRole("group", { name: en["settings.images.label"] }),
    ).toBeInTheDocument();
  });

  it("hides the rows that do not match", async () => {
    const user = userEvent.setup();
    renderPage();

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
    renderPage();

    await user.type(
      screen.getByRole("searchbox", { name: en["settings.search.label"] }),
      "cryptography",
    );

    expect(screen.getByText(en["settings.search.empty"])).toBeInTheDocument();
  });

  it("clears with Escape without disturbing the page", async () => {
    const user = userEvent.setup();
    renderPage();

    const box = screen.getByRole("searchbox", { name: en["settings.search.label"] });
    await user.type(box, "density");
    await user.type(box, "{Escape}");

    expect(box).toHaveValue("");
    /*
     * The page must survive, and the assertion changed shape with B3: there is
     * no dialog to still be open, so what is checked is that the tab row came
     * BACK — clearing the search un-suspends the tabs, which is the visible
     * proof the page is intact and standing where it was.
     */
    expect(
      screen.getByRole("tab", { name: en["settings.section.general"] }),
    ).toHaveAttribute("aria-selected", "true");
  });

  it("clears with the clear button", async () => {
    const user = userEvent.setup();
    renderPage();

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

describe("the preference controls", () => {
  it("offers Gmail's exact four undo-send values", () => {
    renderPage();

    const select = screen.getByRole("combobox", { name: en["settings.undoSend.label"] });
    const values = Array.from(select.querySelectorAll("option")).map((o) => o.value);
    expect(values).toEqual(["5", "10", "20", "30"]);
  });

  it("renders each toggle as a real switch reflecting its current value", () => {
    renderPage({ initialPrefs: { ...DEFAULT_PREFS, showSnippets: false, hoverActions: true } });

    expect(screen.getByRole("switch", { name: en["settings.snippets.label"] })).not.toBeChecked();
    expect(screen.getByRole("switch", { name: en["settings.hover.label"] })).toBeChecked();
  });

  it("moves a switch when it is clicked", async () => {
    const user = userEvent.setup();
    renderPage();

    const snippets = screen.getByRole("switch", { name: en["settings.snippets.label"] });
    expect(snippets).toBeChecked();
    await user.click(snippets);
    // Optimistic: the control moves without waiting for a round trip.
    expect(snippets).not.toBeChecked();
  });

  it("offers the language switcher with a follow-the-browser option", () => {
    renderPage();

    const select = screen.getByRole("combobox", { name: en["settings.language.label"] });
    const values = Array.from(select.querySelectorAll("option")).map((o) => o.value);
    // `auto` first: it is the default and the honest one.
    expect(values).toEqual(["auto", "es", "en"]);
    expect(select).toHaveValue("auto");
  });

  it("offers exactly the three reading panes Gmail names", async () => {
    const user = userEvent.setup();
    renderPage();
    await openAt(user, en["settings.section.inbox"]);

    const group = screen.getByRole("group", { name: en["settings.readingPane.label"] });
    const values = within(group)
      .getAllByRole("radio")
      .map((radio) => (radio as HTMLInputElement).value);
    expect(values).toEqual(["none", "right", "bottom"]);
  });

  it("offers only the DETERMINISTIC inbox types", async () => {
    const user = userEvent.setup();
    renderPage();
    await openAt(user, en["settings.section.inbox"]);

    const group = screen.getByRole("group", { name: en["settings.inboxType.label"] });
    const values = within(group)
      .getAllByRole("radio")
      .map((radio) => (radio as HTMLInputElement).value);
    // No "Important first" and no "Priority Inbox": both need the importance
    // classifier, which is AI-phase (GC-2's rule applied to inbox types).
    expect(values).toEqual(["default", "unread_first", "starred_first"]);
  });

  it("offers two notification modes, not Gmail's three", async () => {
    const user = userEvent.setup();
    renderPage();
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
    renderPage({ identity: IDENTITY });
    await openAt(user, en["settings.section.account"]);

    expect(screen.getByText(IDENTITY.name)).toBeInTheDocument();
    expect(screen.getByText(IDENTITY.email)).toBeInTheDocument();
  });

  it("says so honestly when there is no sending identity", async () => {
    const user = userEvent.setup();
    renderPage();
    await openAt(user, en["settings.section.account"]);

    expect(screen.getByText(en["settings.identity.missing"])).toBeInTheDocument();
  });

  it("seeds the signature from the identity and saves it EXPLICITLY", async () => {
    const user = userEvent.setup();
    const onSaveSignature = vi.fn().mockResolvedValue(true);
    renderPage({ identity: IDENTITY, onSaveSignature });
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
    renderPage({ identity: IDENTITY, onSaveSignature: vi.fn() });
    await openAt(user, en["settings.section.account"]);

    expect(screen.getByRole("button", { name: en["settings.signature.save"] })).toBeDisabled();
  });

  it("reports a failed save in the row rather than silently doing nothing", async () => {
    const user = userEvent.setup();
    renderPage({ identity: IDENTITY, onSaveSignature: vi.fn().mockResolvedValue(false) });
    await openAt(user, en["settings.section.account"]);

    const field = screen.getByRole("textbox", { name: en["settings.signature.label"] });
    await user.type(field, "!");
    await user.click(screen.getByRole("button", { name: en["settings.signature.save"] }));

    expect(await screen.findByText(en["settings.signature.failed"])).toBeInTheDocument();
  });
});

describe("the honest skeletons (P4)", () => {
  /*
   * The TAB names, not the section names — B3 folded blocked into filters and
   * vacation into forwarding, so two of these three are now reached through a
   * tab whose label differs from the heading the skeleton renders under.
   */
  it.each([
    [en["settings.tab.filters"], en["settings.filters.soon"]],
    [en["settings.section.forwarding"], en["settings.forwarding.soon"]],
    [en["settings.section.forwarding"], en["settings.vacation.soon"]],
    /*
     * Offline LEFT this list: prefs v2 gave it two real controls (the header
     * and body depths), so a skeleton there would now be the dishonest option.
     * Its own coverage is below.
     */
  ])("names what is coming under %s, with no control at all", async (tab, promise) => {
    const user = userEvent.setup();
    renderPage();
    await openAt(user, tab);

    /*
     * `getAllBy`, because the FOLD makes duplicates real: the filters tab holds
     * both the filter list and the blocked senders, and without the Sieve
     * capability both render the same "this server does not offer filters"
     * skeleton. That is correct — they are the same missing capability stated
     * where each is expected — so the assertion is that the sentence is
     * present, not that it is unique.
     */
    expect(screen.getAllByText(promise).length).toBeGreaterThan(0);

    /*
     * The rule these sections exist to honour: never a control that does
     * nothing. A disabled "Create filter" button would be exactly that — it
     * invites the click, then refuses it. A paragraph is information.
     *
     * The panel is found by its `tabpanel` role now rather than by `dialog`:
     * same assertion, new container. The search box is excluded because it
     * lives in the page HEADER and is not part of what the tab renders — but
     * the query is written to exclude it by type anyway, so a future move of
     * the box inside the panel cannot silently turn this green.
     */
    const panel = screen.getByRole("tabpanel");
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
          <SettingsPage
            tab={DEFAULT_SETTINGS_TAB}
            onSelectTab={() => undefined}
            onClose={() => undefined}
          />
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

/**
 * The prefs v2 rows (the gate's finding 2).
 *
 * Each of these six server keys was validated and roamed by the server while
 * having ZERO client consumers — a reverse dead control. What is pinned here is
 * that the control EXISTS and writes the key; the wire shape of each write is
 * pinned in `mail/prefs.test.ts`, and the behaviour each one drives in the
 * suite of the surface it drives.
 */
describe("the v2 rows exist and write their key", () => {
  it("offers the Send & Archive switch, defaulting on", async () => {
    const user = userEvent.setup();
    renderPage();
    await openAt(user, en["settings.section.general"]);

    const control = screen.getByRole("switch", { name: en["settings.sendAndArchive.label"] });
    // A registered divergence from Gmail, taken because the button already
    // shipped visible and defaulting to false would REMOVE a live control.
    expect(control).toBeChecked();
    await user.click(control);
    expect(control).not.toBeChecked();
  });

  /*
   * F-26: two options, so radios with Gmail's inline explanation — not a
   * select whose collapsed state shows one of the two.
   */
  it("offers the reply default as radios with both verbs, each explained", async () => {
    const user = userEvent.setup();
    renderPage();
    await openAt(user, en["settings.section.general"]);

    const group = screen.getByRole("group", { name: en["settings.replyBehavior.label"] });
    expect(
      within(group).getByRole("radio", { name: en["settings.replyBehavior.reply"] }),
    ).toBeChecked();
    // The explanation is what makes the radios worth more than the select.
    expect(within(group).getByText(en["settings.replyBehavior.replyNote"])).toBeInTheDocument();
    expect(
      within(group).getByText(en["settings.replyBehavior.replyAllNote"]),
    ).toBeInTheDocument();

    await user.click(
      within(group).getByRole("radio", { name: en["settings.replyBehavior.replyAll"] }),
    );
    expect(
      within(group).getByRole("radio", { name: en["settings.replyBehavior.replyAll"] }),
    ).toBeChecked();
  });

  it("gives the offline section two real depth controls instead of a promise", async () => {
    const user = userEvent.setup();
    renderPage();
    await openAt(user, en["settings.section.offline"]);

    expect(
      screen.getByRole("spinbutton", { name: en["settings.offlineHeaders.label"] }),
    ).toHaveValue(200);
    expect(
      screen.getByRole("spinbutton", { name: en["settings.offlineBodies.label"] }),
    ).toHaveValue(100);
    // The limitation Gmail declares too, said next to the number.
    expect(screen.getByText(en["settings.offlineDepth.attachments"])).toBeInTheDocument();
  });

  it("mirrors the server's bounds on the depth inputs, so a refused save is impossible", async () => {
    const user = userEvent.setup();
    renderPage();
    await openAt(user, en["settings.section.offline"]);

    const headers = screen.getByRole("spinbutton", {
      name: en["settings.offlineHeaders.label"],
    });
    expect(headers).toHaveAttribute("min", "50");
    expect(headers).toHaveAttribute("max", "1000");
  });

  it("REVERTS an out-of-range depth on commit rather than leaving a red box behind", async () => {
    /*
     * The server refuses it too (`prefsPatchBoundedInt`) — this is the copy of
     * the check that stops the user AT the boundary. Reverting rather than
     * holding the bad value is what keeps the sheet from closing over a change
     * the user thinks they made.
     */
    const user = userEvent.setup();
    renderPage();
    await openAt(user, en["settings.section.offline"]);

    const headers = screen.getByRole("spinbutton", {
      name: en["settings.offlineHeaders.label"],
    });
    await user.clear(headers);
    await user.type(headers, "5");
    await user.tab();

    expect(headers).toHaveValue(200);
    expect(screen.getByRole("alert")).toHaveTextContent("50");
  });

  it("offers named signatures: create, name, pick for new and for replies", async () => {
    const user = userEvent.setup();
    renderPage({ identity: IDENTITY });
    await openAt(user, en["settings.section.account"]);

    expect(screen.getByText(en["settings.signatures.empty"])).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: en["settings.signatures.add"] }));

    // The two pickers now offer the new item beside the "none" fallback.
    const forNew = screen.getByRole("combobox", { name: en["settings.signatures.forNew"] });
    const forReply = screen.getByRole("combobox", { name: en["settings.signatures.forReply"] });
    expect(forNew).toHaveValue("");
    expect(within(forNew).getAllByRole("option")).toHaveLength(2);
    expect(within(forReply).getAllByRole("option")).toHaveLength(2);
  });

  it("says out loud that signatures are edited as plain text here", async () => {
    // The honest limitation: htmlBody is carried through untouched but cannot
    // be edited, and a user with a rich signature needs to know why the box
    // shows plain text.
    const user = userEvent.setup();
    renderPage({ identity: IDENTITY });
    await openAt(user, en["settings.section.account"]);
    expect(screen.getByText(en["settings.signatures.textOnly"])).toBeInTheDocument();
  });

  it("no longer claims the autocomplete choice is browser-only", () => {
    /*
     * Asserted against the STRING TABLE rather than the rendered row, because
     * the row only renders when an `addresses` controller is supplied and this
     * sheet's harness has none — `useAddressIndex.test.tsx` drives that
     * controller. What matters here is the claim itself: the standalone
     * `localOnly` disclaimer is gone, and the description now separates the two
     * facts, since the CHOICE roams while the saved addresses do not.
     */
    expect(en).not.toHaveProperty("settings.addressAutocomplete.localOnly");
    expect(es).not.toHaveProperty("settings.addressAutocomplete.localOnly");

    for (const table of [en, es]) {
      const description = table["settings.addressAutocomplete.description"];
      expect(description).not.toMatch(/does not roam|no viaja/i);
      expect(description).toMatch(/every device|todos tus dispositivos/i);
      // The half that is still true is still said.
      expect(description).toMatch(/only in this browser|solo en este navegador/i);
    }
  });

  it("deletes the orphaned offline-depth promise, now that the control exists", () => {
    /*
     * `offline.depthPending` was defined in BOTH locales and rendered nowhere —
     * the gate found it as an honest-note pattern failing in the quietest way
     * possible. Two real controls replaced it, so the string is gone; this
     * pins that it stays gone rather than drifting back in unrendered.
     */
    expect(en).not.toHaveProperty("offline.depthPending");
    expect(es).not.toHaveProperty("offline.depthPending");
  });
});

/**
 * The per-row save receipt (F-38).
 *
 * Every control here saves on the gesture and there is no Save button — which
 * is right, a switch has two states and flipping one IS the decision. The
 * review named what that lacked: nothing on screen said the save HAPPENED. The
 * control moved, and the control would have moved either way; a failed write
 * and a successful one looked identical until the page reloaded.
 */
describe("save feedback (F-38)", () => {
  /*
   * These need a SERVER, unlike every other test in this file.
   *
   * `PrefsProvider.setPref` resolves false when there is nowhere to save to —
   * honestly, and the page already says the preferences are not being
   * persisted — so a tick that appeared in the default harness would be
   * confirming a save that did not happen. The stub answers `Prefs/set` +
   * `Prefs/get` the way the server does.
   */
  function renderSaving(): void {
    const client = new JmapClient({ username: "u", password: "p" });
    vi.spyOn(client, "call").mockImplementation((invocations) => {
      const calls = invocations as [string, Record<string, unknown>, string][];
      return Promise.resolve({
        methodResponses: calls.map(([name, , id]) => [
          name,
          { list: [{ id: "singleton", ...DEFAULT_PREFS }], state: "s1" },
          id,
        ]),
      } as never);
    });
    const session: JmapSession = {
      capabilities: { [CAP_PREFS]: {} },
      accounts: {
        a: { name: "u", isPersonal: true, isReadOnly: false, accountCapabilities: {} },
      },
      primaryAccounts: {},
      username: "u",
      apiUrl: "/jmap/api",
      downloadUrl: "",
      uploadUrl: "",
      eventSourceUrl: "",
      state: "s",
    };
    localStorage.clear();
    applyTheme("light", document.documentElement);
    render(
      <I18nProvider locale="en">
        <PrefsProvider
          client={client}
          session={session}
          accountId="a"
          initialPrefs={DEFAULT_PREFS}
        >
          <SettingsPage
            tab={DEFAULT_SETTINGS_TAB}
            onSelectTab={() => undefined}
            onClose={() => undefined}
          />
        </PrefsProvider>
      </I18nProvider>,
    );
  }

  it("confirms a successful write beside the row that made it", async () => {
    const user = userEvent.setup();
    renderSaving();

    await user.click(screen.getByRole("switch", { name: en["settings.snippets.label"] }));

    // Beside the row, not as a toast: a toast for a setting the user is
    // looking straight at appears somewhere else on the screen to say so.
    const row = screen
      .getByRole("switch", { name: en["settings.snippets.label"] })
      .closest("div[class*='row']");
    expect(await within(row as HTMLElement).findByText(en["settings.saved"])).toBeInTheDocument();
  });

  it("announces it politely, so it does not interrupt what is being read", async () => {
    const user = userEvent.setup();
    renderSaving();

    await user.click(screen.getByRole("switch", { name: en["settings.hover.label"] }));

    const saved = await screen.findByText(en["settings.saved"]);
    // `status`, not `alert`: a confirmation of a thing the user just did is
    // read at the end of what the screen reader is saying.
    expect(saved).toHaveAttribute("role", "status");
  });

  it("confirms each row in its OWN place, never moving one tick around", async () => {
    const user = userEvent.setup();
    renderSaving();

    await user.click(screen.getByRole("switch", { name: en["settings.snippets.label"] }));
    await user.click(screen.getByRole("switch", { name: en["settings.hover.label"] }));

    // One shared flag would move a single tick from row to row, which reads as
    // the previous confirmation being retracted.
    expect(await screen.findAllByText(en["settings.saved"])).toHaveLength(2);
  });

  it("shows nothing on a row nobody touched", () => {
    renderSaving();
    expect(screen.queryByText(en["settings.saved"])).not.toBeInTheDocument();
  });
});

/**
 * L2-brand-admin: the Marca tab exists only for an administrator of this host.
 *
 * "Exists" is meant literally, and that is the property under test. Almost
 * nobody administers a brand, so almost every user must see a settings page
 * with no trace of the panel at all — not a disabled tab, not a skeleton
 * naming an absent capability. A skeleton saying "there is a brand panel you
 * may not use" would be precisely the enumeration the API's own 404 avoids.
 */
const BRAND_DOC: BrandAdminDoc = {
  host: "mail.acme.example",
  isDefault: false,
  name: "Acme Mail",
  shortName: "Acme",
  tagline: "",
  supportUrl: "",
  privacyUrl: "",
  termsUrl: "",
  colors: { primary: "#5b5bd6", onPrimary: "", splashFrom: "#1e1b4b", splashTo: "#4c1d95" },
  colorsConfigured: new Set(["primary", "splashFrom", "splashTo"] as const),
  assets: { logo: null, logoDark: null, icon: null, splash: null },
  iconSource: "default",
  iconIssue: "",
  brandAdmins: [],
  warnings: [],
  publicUrl: "/branding",
  manifestUrl: "/manifest.webmanifest",
  iconUrls: {},
  version: 1,
};

const BRAND_PROPS: BrandSectionProps = {
  doc: BRAND_DOC,
  onSave: () => Promise.resolve(true),
  onUploadAsset: () => Promise.resolve(true),
  onRemoveAsset: () => Promise.resolve(true),
  onReset: () => Promise.resolve(true),
};

describe("the Marca tab (L2-brand-admin)", () => {
  it("is ABSENT when the probe answered 404 — for almost every user", () => {
    renderPage();
    expect(
      screen.queryByRole("tab", { name: en["settings.section.brand"] }),
    ).not.toBeInTheDocument();
    // Not a skeleton either. There is nothing to see, which is the point.
    expect(screen.queryByText(en["brand.identity.heading"])).not.toBeInTheDocument();
  });

  it("appears, and opens, when the probe answered 200", async () => {
    const user = userEvent.setup();
    renderPage({ brand: BRAND_PROPS });
    const tab = screen.getByRole("tab", { name: en["settings.section.brand"] });
    expect(tab).toBeInTheDocument();
    await user.click(tab);
    expect(screen.getByText(en["brand.identity.heading"])).toBeInTheDocument();
    expect(screen.getByText(en["brand.colors.heading"])).toBeInTheDocument();
  });

  it("falls back to General when the ROUTE names it and the user may not see it", () => {
    /*
     * A bookmarked `/settings/brand` reaching somebody who is not an
     * administrator — or an administrator whose access was revoked. It lands on
     * a real tab rather than on an empty panel, and says nothing about why.
     */
    renderPage({ initialTab: "brand" });
    expect(
      screen.getByRole("heading", { name: en["settings.section.general"] }),
    ).toBeInTheDocument();
    expect(screen.queryByText(en["brand.identity.heading"])).not.toBeInTheDocument();
  });

  it("opens straight onto the deep link for an administrator", () => {
    renderPage({ initialTab: "brand", brand: BRAND_PROPS });
    expect(screen.getByText(en["brand.identity.heading"])).toBeInTheDocument();
  });

  it("keeps its rows out of a SEARCH for a non-administrator", async () => {
    const user = userEvent.setup();
    renderPage();
    // D-5's search suspends the tabs and renders every matching section
    // wherever it lives — which would have been the back door into a panel the
    // tab row correctly hid.
    await user.type(screen.getByRole("searchbox"), "logo");
    expect(screen.queryByText(en["brand.images.heading"])).not.toBeInTheDocument();
  });

  it("finds them for an administrator, by a word an administrator would type", async () => {
    const user = userEvent.setup();
    renderPage({ brand: BRAND_PROPS });
    await user.type(screen.getByRole("searchbox"), "logo");
    expect(screen.getByText(en["brand.images.heading"])).toBeInTheDocument();
    // …and only that group, which is what D-5 buys on a five-group tab.
    expect(screen.queryByText(en["brand.colors.heading"])).not.toBeInTheDocument();
  });

  it("keeps the arrow keys walking the tabs that EXIST", async () => {
    const user = userEvent.setup();
    renderPage({ initialTab: "offline" });
    // "offline" is last for a non-administrator, so ArrowRight must wrap to
    // "general" rather than land on a tab that is not rendered.
    await user.click(screen.getByRole("tab", { name: en["settings.section.offline"] }));
    await user.keyboard("{ArrowRight}");
    expect(
      screen.getByRole("tab", { name: en["settings.section.general"] }),
    ).toHaveAttribute("aria-selected", "true");
  });
});
