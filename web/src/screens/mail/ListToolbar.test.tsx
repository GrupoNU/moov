import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import { en } from "../../i18n/strings";
import { MAX_REACH, PAGE_SIZE, type PageState } from "../../mail/paging";
import { ListToolbar } from "./ListToolbar";

/**
 * The list's chrome strip (E12/B4, canon 07 §3).
 *
 * The arithmetic of paging is tested where it lives (`mail/paging.test.ts`).
 * What is tested HERE is the wiring that arithmetic cannot see: that the
 * dropdown's six scopes reach the same reducer the keyboard chords do, and —
 * the one that matters most — that the "older" arrow is genuinely DISABLED at
 * the boundaries rather than merely styled as if it were. An arrow that pages
 * into a permanently empty list is indistinguishable, to the user, from the app
 * having lost their mail.
 */

function renderToolbar(overrides: Record<string, unknown> = {}) {
  const handlers = {
    onSelectBy: vi.fn(),
    onRefresh: vi.fn(),
    onNewerPage: vi.fn(),
    onOlderPage: vi.fn(),
  };
  render(
    <I18nProvider locale="en">
      <ListToolbar
        allSelected={false}
        someSelected={false}
        totalCount={PAGE_SIZE}
        isRefreshing={false}
        page={{ position: 0, shown: PAGE_SIZE, total: 15224 } satisfies PageState}
        {...handlers}
        {...overrides}
      />
    </I18nProvider>,
  );
  return handlers;
}

describe("the select-all control", () => {
  it("is a checkbox and a menu button — two roles, because two behaviours", () => {
    renderToolbar();
    /*
     * Fusing them into one widget would leave a screen-reader user with a
     * control whose role can describe the toggle or the menu but not both.
     */
    expect(screen.getByRole("checkbox", { name: en["action.selectAll"] })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: en["action.selectMenu"] })).toBeInTheDocument();
  });

  it("shows the honest indeterminate state for a partial selection", () => {
    renderToolbar({ someSelected: true, allSelected: false });
    const box = screen.getByRole("checkbox", { name: en["action.selectAll"] });
    // A plain unchecked box would claim nothing is selected.
    expect((box as HTMLInputElement).indeterminate).toBe(true);
  });

  it("offers Gmail's exact six scopes, wired to ONE reducer", async () => {
    const user = userEvent.setup();
    const handlers = renderToolbar();

    await user.click(screen.getByRole("button", { name: en["action.selectMenu"] }));

    /*
     * These are the same six the `* a`/`* n`/`* r`/`* u`/`* s`/`* t` chords
     * resolve to (canon §2.4), reaching the same `selectionByScope`. A menu
     * that disagreed with the keyboard about what "unread" selects would be
     * invisible until someone used both.
     */
    for (const name of [
      en["action.select.all"],
      en["action.select.none"],
      en["action.select.read"],
      en["action.select.unread"],
      en["action.select.starred"],
      en["action.select.unstarred"],
    ]) {
      expect(screen.getByRole("menuitem", { name })).toBeInTheDocument();
    }

    await user.click(screen.getByRole("menuitem", { name: en["action.select.unread"] }));
    expect(handlers.onSelectBy).toHaveBeenCalledWith("unread");
  });

  it("routes the plain checkbox through the SAME reducer, not a second path", async () => {
    const user = userEvent.setup();
    const handlers = renderToolbar();

    await user.click(screen.getByRole("checkbox", { name: en["action.selectAll"] }));

    expect(handlers.onSelectBy).toHaveBeenCalledWith("all");
  });

  it("disables both halves over an empty list", () => {
    renderToolbar({ totalCount: 0, page: { position: 0, shown: 0, total: 0 } });
    expect(screen.getByRole("checkbox", { name: en["action.selectAll"] })).toBeDisabled();
    expect(screen.getByRole("button", { name: en["action.selectMenu"] })).toBeDisabled();
  });
});

describe("refresh", () => {
  it("is inert WHILE refreshing, so a second request cannot race the first", () => {
    renderToolbar({ isRefreshing: true });
    // Two in flight could paint the older answer last, which reads as the
    // refresh having undone itself.
    expect(screen.getByRole("button", { name: en["list.refresh"] })).toBeDisabled();
  });
});

describe("the pager", () => {
  it("writes Gmail's range with the total the server gave", () => {
    renderToolbar();
    expect(screen.getByText("1–50 of 15,224")).toBeInTheDocument();
  });

  it("uses the STRING TABLE's separator, not the host machine's", () => {
    /*
     * A real bug this test caught. The strings used a bare `toLocaleString()`,
     * which follows the AMBIENT environment — so the English pager rendered
     * "15.224" on a machine set to Spanish: the wrong separator for the
     * language actually on screen. The string table IS the locale, so the tag
     * is explicit in each table now.
     *
     * Asserted in both directions, because a single-locale check would pass
     * with the bug on an English machine and fail on a Spanish one — a test
     * whose result depends on who runs it.
     */
    render(
      <I18nProvider locale="es">
        <ListToolbar
          onSelectBy={vi.fn()}
          allSelected={false}
          someSelected={false}
          totalCount={PAGE_SIZE}
          onRefresh={vi.fn()}
          isRefreshing={false}
          page={{ position: 0, shown: PAGE_SIZE, total: 15224 }}
        />
      </I18nProvider>,
    );
    expect(screen.getByText("1–50 de 15.224")).toBeInTheDocument();
    // The English one, rendered by the harness above, uses a comma.
    renderToolbar();
    expect(screen.getByText("1–50 of 15,224")).toBeInTheDocument();
  });

  it("announces the range, because the arrows change it rather than name it", () => {
    renderToolbar();
    /*
     * Without a live region a screen-reader user presses "older" and hears
     * nothing at all — the arrows' labels never change, so there is no other
     * signal that the page moved.
     */
    expect(screen.getByText("1–50 of 15,224")).toHaveAttribute("aria-live", "polite");
  });

  it("has no NEWER arrow on the first page", () => {
    renderToolbar();
    expect(screen.getByRole("button", { name: en["list.page.newer"] })).toBeDisabled();
  });

  it("has both arrows in the middle of a result", () => {
    renderToolbar({ page: { position: 100, shown: PAGE_SIZE, total: 15224 } });
    expect(screen.getByRole("button", { name: en["list.page.newer"] })).toBeEnabled();
    expect(screen.getByRole("button", { name: en["list.page.older"] })).toBeEnabled();
  });

  it("has no OLDER arrow at the server's reach ceiling", () => {
    /*
     * The boundary that only the client can respect: `mail.MaxQueryReach` is a
     * real wall past which `Email/query` answers the empty list. An arrow there
     * would look like the app losing the user's mail rather than like the end
     * of what the server will serve.
     */
    renderToolbar({
      page: { position: MAX_REACH - PAGE_SIZE, shown: PAGE_SIZE, total: MAX_REACH * 2 },
    });
    expect(screen.getByRole("button", { name: en["list.page.older"] })).toBeDisabled();
  });

  it("has no OLDER arrow on a short page — the result is exhausted", () => {
    renderToolbar({ page: { position: 0, shown: 12, total: 12 } });
    expect(screen.getByRole("button", { name: en["list.page.older"] })).toBeDisabled();
  });

  it("writes the range ALONE when the server declined to count", () => {
    /*
     * Not a degraded sentence — a different true one. The server omits `total`
     * when the result filled its window, so a number here would be a floor
     * dressed as a total.
     */
    renderToolbar({ page: { position: 0, shown: PAGE_SIZE, total: undefined } });
    expect(screen.getByText("1–50")).toBeInTheDocument();
  });

  it("draws NO pager at all when the view does not page", () => {
    /*
     * The Outbox is a local queue and Scheduled lists submissions; a pager over
     * either would be chrome describing a thing that does not exist.
     */
    renderToolbar({ page: undefined });
    expect(
      screen.queryByRole("button", { name: en["list.page.older"] }),
    ).not.toBeInTheDocument();
  });
});
