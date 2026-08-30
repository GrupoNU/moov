import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import { I18nProvider } from "../../i18n/I18nProvider";
import { PrefsProvider } from "../../mail/PrefsProvider";
import { DEFAULT_PREFS, densityMetrics, type Density, type Prefs } from "../../mail/prefs";
import { groupByThread } from "../../mail/threading";
import { KEYWORD_SEEN, type Email } from "../../mail/types";
import { MessageList } from "./MessageList";

/**
 * The list's preference-driven behaviour (L3 E5).
 *
 * Three settings reach into this component, and each one breaks in a way no
 * type checker can see:
 *
 *   - DENSITY changes the number the virtualizer divides by. Get it wrong and
 *     nothing throws — the list simply drifts away from its scrollbar, which
 *     is reported as "scrolling feels weird" and takes a day to find.
 *   - SNIPPETS must remove the preview from the DOM, not hide it with CSS: a
 *     screen reader would still read a preview the sighted user turned off.
 *   - HOVER ACTIONS must drop the buttons entirely, so there is nothing for
 *     Tab to reach and nothing invisible in the accessibility tree.
 *
 * What is NOT asserted here: that the rows LOOK denser. That is the
 * stylesheet's `--row-height`, and jsdom does not apply CSS modules — the
 * variable is stamped by MailScreen and verified in a real browser. What this
 * pins is the geometry the MATHS uses, which is the half that silently
 * corrupts.
 */

function email(id: string, overrides: Partial<Email> = {}): Email {
  return {
    id,
    threadId: `t-${id}`,
    mailboxIds: { inbox: true },
    keywords: { [KEYWORD_SEEN]: true },
    subject: `Subject ${id}`,
    preview: `Preview of ${id}`,
    from: [{ name: `Sender ${id}`, email: `${id}@example.com` }],
    receivedAt: "2026-08-20T10:00:00Z",
    ...overrides,
  };
}

function renderList(prefs: Partial<Prefs> = {}, count = 3) {
  const emails = Array.from({ length: count }, (_, i) => email(String(i)));
  return render(
    <I18nProvider locale="en">
      <PrefsProvider
        client={undefined}
        session={undefined}
        accountId=""
        initialPrefs={{ ...DEFAULT_PREFS, ...prefs }}
      >
        <MessageList
          listKey="mailbox:inbox"
          groups={groupByThread(emails)}
          selectedId="t-0"
          isLoading={false}
          empty={<p>empty</p>}
          onOpen={vi.fn()}
          onSelect={vi.fn()}
          onRowArchive={vi.fn()}
          onRowDelete={vi.fn()}
          onRowToggleRead={vi.fn()}
        />
      </PrefsProvider>
    </I18nProvider>,
  );
}

/** The absolute grid the rows are positioned inside. */
function grid(): HTMLElement {
  return screen.getByRole("grid");
}

describe("density drives the virtualization geometry", () => {
  it.each<Density>(["default", "comfortable", "compact"])(
    "sizes the scroll content from the %s row height",
    (density) => {
      renderList({ density }, 10);
      // The grid's height IS `itemCount * rowHeight` — the invariant that keeps
      // the scrollbar honest.
      expect(grid().style.height).toBe(`${10 * densityMetrics(density).rowHeight}px`);
    },
  );

  it.each<Density>(["default", "comfortable", "compact"])(
    "positions each row at its own multiple of the %s row height",
    (density) => {
      renderList({ density }, 3);
      const height = densityMetrics(density).rowHeight;
      const rows = screen.getAllByRole("row");
      rows.forEach((row, index) => {
        expect(row.style.transform).toBe(`translateY(${index * height}px)`);
      });
    },
  );

  it("actually MOVES the rows when the density changes", () => {
    /*
     * The regression that matters: a density whose value is stored, shown in
     * settings, and changes nothing on screen. Comparing two renders proves the
     * geometry is derived rather than constant.
     */
    const compact = render(
      <I18nProvider locale="en">
        <PrefsProvider
          client={undefined}
          session={undefined}
          accountId=""
          initialPrefs={{ ...DEFAULT_PREFS, density: "compact" }}
        >
          <MessageList
            listKey="k"
            groups={groupByThread([email("a"), email("b")])}
            selectedId="t-a"
            isLoading={false}
            empty={<p>empty</p>}
            onOpen={vi.fn()}
            onSelect={vi.fn()}
          />
        </PrefsProvider>
      </I18nProvider>,
    );
    const compactHeight = compact.getByRole("grid").style.height;
    compact.unmount();

    renderList({ density: "comfortable" }, 2);
    expect(grid().style.height).not.toBe(compactHeight);
  });
});

describe("showSnippets", () => {
  it("shows the preview line by default", () => {
    renderList();
    expect(screen.getByText("Preview of 0")).toBeInTheDocument();
  });

  it("REMOVES the preview from the DOM when turned off", () => {
    renderList({ showSnippets: false });
    // Not merely invisible: a hidden-by-CSS preview would still be read aloud.
    expect(screen.queryByText("Preview of 0")).not.toBeInTheDocument();
  });

  it("keeps the subject and sender, which are not snippets", () => {
    renderList({ showSnippets: false });
    expect(screen.getByText("Subject 0")).toBeInTheDocument();
    expect(screen.getByText("Sender 0")).toBeInTheDocument();
  });
});

describe("hoverActions", () => {
  it("renders the three row actions by default (Gmail ships them ON)", () => {
    renderList();
    expect(screen.getAllByRole("button", { name: /archive/i }).length).toBeGreaterThan(0);
  });

  it("renders NO row action buttons when turned off", () => {
    renderList({ hoverActions: false });
    /*
     * Dropped, not hidden. A CSS-hidden button is still a tab stop and still
     * in the accessibility tree — a keyboard user would reach a control the
     * setting says does not exist.
     */
    expect(screen.queryByRole("button", { name: /archive/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /delete/i })).not.toBeInTheDocument();
  });

  it("leaves the grid semantics intact either way", () => {
    // The virtualizer's aria-rowcount/aria-rowindex contract must survive a
    // cell disappearing from every row.
    renderList({ hoverActions: false }, 5);
    expect(grid()).toHaveAttribute("aria-rowcount", "5");
    expect(screen.getAllByRole("row")[0]).toHaveAttribute("aria-rowindex", "1");
  });
});
