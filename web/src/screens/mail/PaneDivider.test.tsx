import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import { en } from "../../i18n/strings";
import { PANE_BOUNDS, type PaneAxis } from "../../mail/viewChrome";
import { PaneDivider } from "./PaneDivider";

/**
 * The pane divider (E12/B5).
 *
 * Two things here break silently and are what these tests are for.
 *
 * The first is the SIGN. The reading pane sits to the right of the divider in
 * one layout and below it in the other, so "drag toward smaller" is a different
 * direction in each — and getting it backwards produces a divider that runs
 * away from the pointer, the classic splitter bug. It is asserted through the
 * keyboard rather than through a synthetic drag, because jsdom has no layout
 * and a `pointermove` at a fabricated `clientX` proves only that arithmetic
 * ran; the arrows exercise the same sign through a path a user really has.
 *
 * The second is the ARIA. A separator that is not focusable is a decorative
 * rule, and one without `aria-valuenow` is a widget a screen reader cannot
 * describe — in both cases the resizer silently becomes pointer-only, which is
 * exactly the failure the `role="separator"` pattern exists to prevent.
 */

function renderDivider(axis: PaneAxis = "width", size = 520) {
  const handlers = { onResize: vi.fn(), onReset: vi.fn() };
  render(
    <I18nProvider locale="en">
      <PaneDivider axis={axis} size={size} {...handlers} />
    </I18nProvider>,
  );
  return handlers;
}

function divider(): HTMLElement {
  return screen.getByRole("separator", { name: en["shell.resizePane"] });
}

describe("the ARIA contract", () => {
  it("is a FOCUSABLE separator — which is what makes it a window splitter", () => {
    renderDivider();
    /*
     * The single most important assertion in this file. Without `tabIndex`
     * this is a decorative rule and the resizer is pointer-only, which is a
     * resizer half the audience does not have.
     */
    expect(divider()).toHaveAttribute("tabindex", "0");
  });

  it("reports its position and its range, so it can be described at all", () => {
    renderDivider("width", 520);
    const strip = divider();
    expect(strip).toHaveAttribute("aria-valuenow", "520");
    expect(strip).toHaveAttribute("aria-valuemin", String(PANE_BOUNDS.width.min));
    expect(strip).toHaveAttribute("aria-valuemax", String(PANE_BOUNDS.width.max));
  });

  it("speaks the value with its UNIT, since a bare number says nothing", () => {
    renderDivider("width", 520);
    expect(divider()).toHaveAttribute("aria-valuetext", "520 pixels");
  });

  it("declares which way it splits", () => {
    renderDivider("width");
    expect(divider()).toHaveAttribute("aria-orientation", "vertical");
    // A fresh render, because the orientation is fixed at construction.
    render(
      <I18nProvider locale="en">
        <PaneDivider axis="height" size={420} onResize={vi.fn()} onReset={vi.fn()} />
      </I18nProvider>,
    );
    expect(
      screen.getAllByRole("separator", { name: en["shell.resizePane"] })[1],
    ).toHaveAttribute("aria-orientation", "horizontal");
  });
});

describe("the keyboard, and the sign convention it exercises", () => {
  it("GROWS the right-hand pane when the divider moves left", async () => {
    const user = userEvent.setup();
    const handlers = renderDivider("width", 500);

    divider().focus();
    await user.keyboard("{ArrowLeft}");

    /*
     * The reading pane is to the RIGHT of the divider, so dragging (or
     * arrowing) left gives it more room. This is the sign that, reversed,
     * makes the divider run away from the pointer.
     */
    expect(handlers.onResize).toHaveBeenCalledWith(516);
  });

  it("SHRINKS it when the divider moves right", async () => {
    const user = userEvent.setup();
    const handlers = renderDivider("width", 500);

    divider().focus();
    await user.keyboard("{ArrowRight}");

    expect(handlers.onResize).toHaveBeenCalledWith(484);
  });

  it("inverts the sign for the BELOW layout, where the pane is underneath", async () => {
    const user = userEvent.setup();
    const handlers = renderDivider("height", 400);

    divider().focus();
    await user.keyboard("{ArrowDown}");

    // Down makes the pane below the divider SMALLER — the opposite of what
    // "down" means for a pane to the right.
    expect(handlers.onResize).toHaveBeenCalledWith(416);
  });

  it("answers BOTH arrow pairs, because a user cannot see which kind it is", async () => {
    const user = userEvent.setup();
    const handlers = renderDivider("width", 500);

    divider().focus();
    await user.keyboard("{ArrowUp}");

    /*
     * A vertical splitter "should" only take Left/Right. But someone who has
     * just tabbed onto a thin grey strip does not know which kind it is, and
     * pressing the pair that does nothing reads as a broken control. Up
     * behaves as Left here.
     */
    expect(handlers.onResize).toHaveBeenCalledWith(516);
  });

  it("takes a coarse step with Shift", async () => {
    const user = userEvent.setup();
    const handlers = renderDivider("width", 500);

    divider().focus();
    await user.keyboard("{Shift>}{ArrowLeft}{/Shift}");

    expect(handlers.onResize).toHaveBeenCalledWith(564);
  });

  it("CLAMPS at the bounds rather than running past them", async () => {
    const user = userEvent.setup();
    const handlers = renderDivider("width", PANE_BOUNDS.width.min);

    divider().focus();
    await user.keyboard("{ArrowRight}");

    /*
     * The bound is not advisory: below the minimum the reading pane cannot show
     * a quoted line without wrapping every four words, and the LIST beside it
     * becomes a column of ellipses. The clamp is what keeps a held-down arrow
     * from destroying the layout it is adjusting.
     */
    expect(handlers.onResize).toHaveBeenCalledWith(PANE_BOUNDS.width.min);
  });

  it("jumps to each end with Home and End", async () => {
    const user = userEvent.setup();
    const handlers = renderDivider("width", 500);

    divider().focus();
    await user.keyboard("{Home}");
    expect(handlers.onResize).toHaveBeenCalledWith(PANE_BOUNDS.width.min);

    await user.keyboard("{End}");
    expect(handlers.onResize).toHaveBeenCalledWith(PANE_BOUNDS.width.max);
  });

  it("resets with Enter — the keyboard's double-click", async () => {
    const user = userEvent.setup();
    const handlers = renderDivider("width", 900);

    divider().focus();
    await user.keyboard("{Enter}");

    expect(handlers.onReset).toHaveBeenCalledTimes(1);
    // Enter RESETS; it must not also nudge the size.
    expect(handlers.onResize).not.toHaveBeenCalled();
  });

  it("ignores keys it does not own, so typing near it is harmless", async () => {
    const user = userEvent.setup();
    const handlers = renderDivider();

    divider().focus();
    await user.keyboard("j");

    expect(handlers.onResize).not.toHaveBeenCalled();
    expect(handlers.onReset).not.toHaveBeenCalled();
  });
});

describe("the pointer", () => {
  it("resets on a double-click", async () => {
    const user = userEvent.setup();
    const handlers = renderDivider();

    await user.dblClick(divider());

    expect(handlers.onReset).toHaveBeenCalledTimes(1);
  });
});
