import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import { DEFAULT_LABEL_COLOR_ID } from "../../mail/labelPalette";
import type { Label } from "../../mail/labelStore";
import { LabelMenu } from "./LabelMenu";

/**
 * The "Label as" menu (E8) — the APG menu-button pattern, with checkboxes.
 *
 * Three things separate it from the move menu, and each is tested here because
 * each is a decision rather than an implementation detail: the items are
 * `menuitemcheckbox`, a partial selection reports `mixed`, and the menu STAYS
 * OPEN after a tick so several labels can be applied in one visit.
 */

function label(name: string): Label {
  return {
    keyword: `$label:${name}`,
    name,
    colorId: DEFAULT_LABEL_COLOR_ID,
    visibility: "show",
  };
}

const LABELS = [label("work"), label("clients")];

function renderMenu(
  overrides: Partial<React.ComponentProps<typeof LabelMenu>> = {},
): { onToggle: ReturnType<typeof vi.fn>; onManage: ReturnType<typeof vi.fn> } {
  const onToggle = vi.fn();
  const onManage = vi.fn();
  render(
    <I18nProvider locale="es">
      <LabelMenu
        labels={LABELS}
        selection={[{ "$label:work": true }]}
        disabled={false}
        onToggle={onToggle}
        onManage={onManage}
        triggerClassName={undefined}
        triggerContent="L"
        {...overrides}
      />
    </I18nProvider>,
  );
  return { onToggle, onManage };
}

async function open(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  await user.click(screen.getByRole("button", { name: /etiquetar como/i }));
}

describe("the trigger follows the APG menu-button pattern", () => {
  it("announces that it opens a menu, and whether it is open", async () => {
    const user = userEvent.setup();
    renderMenu();
    const trigger = screen.getByRole("button", { name: /etiquetar como/i });
    expect(trigger).toHaveAttribute("aria-haspopup", "menu");
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    await open(user);
    expect(trigger).toHaveAttribute("aria-expanded", "true");
  });

  it("is disabled with no selection, rather than opening onto an inert menu", () => {
    renderMenu({ disabled: true });
    expect(screen.getByRole("button", { name: /etiquetar como/i })).toBeDisabled();
  });

  it("gives the popup an accessible name", async () => {
    const user = userEvent.setup();
    renderMenu();
    await open(user);
    expect(screen.getByRole("menu", { name: /etiquetar como/i })).toBeInTheDocument();
  });
});

describe("the items are checkboxes, with a real mixed state", () => {
  it("reports checked for a label every message carries", async () => {
    const user = userEvent.setup();
    renderMenu({ selection: [{ "$label:work": true }, { "$label:work": true }] });
    await open(user);
    expect(screen.getByRole("menuitemcheckbox", { name: /work/i })).toHaveAttribute(
      "aria-checked",
      "true",
    );
  });

  it("reports unchecked for a label none carries", async () => {
    const user = userEvent.setup();
    renderMenu({ selection: [{}, {}] });
    await open(user);
    expect(screen.getByRole("menuitemcheckbox", { name: /work/i })).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  it("reports MIXED for a partial selection, instead of lying with a boolean", async () => {
    const user = userEvent.setup();
    renderMenu({ selection: [{ "$label:work": true }, {}] });
    await open(user);
    expect(screen.getByRole("menuitemcheckbox", { name: /work/i })).toHaveAttribute(
      "aria-checked",
      "mixed",
    );
  });
});

describe("clicking applies Gmail's rule", () => {
  it("APPLIES to all when the selection is mixed", async () => {
    const user = userEvent.setup();
    const { onToggle } = renderMenu({ selection: [{ "$label:work": true }, {}] });
    await open(user);
    await user.click(screen.getByRole("menuitemcheckbox", { name: /work/i }));
    // Not "toggle each": that would leave the selection mixed after an
    // explicit click, which is never what anyone meant.
    expect(onToggle).toHaveBeenCalledWith("$label:work", true);
  });

  it("REMOVES only when every message already has it", async () => {
    const user = userEvent.setup();
    const { onToggle } = renderMenu({ selection: [{ "$label:work": true }] });
    await open(user);
    await user.click(screen.getByRole("menuitemcheckbox", { name: /work/i }));
    expect(onToggle).toHaveBeenCalledWith("$label:work", false);
  });

  it("stays OPEN after a tick, so several labels take one visit", async () => {
    const user = userEvent.setup();
    const { onToggle } = renderMenu({ selection: [{}] });
    await open(user);
    await user.click(screen.getByRole("menuitemcheckbox", { name: /work/i }));
    expect(screen.getByRole("menu")).toBeInTheDocument();
    await user.click(screen.getByRole("menuitemcheckbox", { name: /clients/i }));
    expect(onToggle).toHaveBeenCalledTimes(2);
  });
});

describe("the empty and the escape hatch", () => {
  it("says so when there are no labels, rather than showing a blank popup", async () => {
    const user = userEvent.setup();
    renderMenu({ labels: [] });
    await open(user);
    expect(screen.getByText(/todavía no hay etiquetas/i)).toBeInTheDocument();
  });

  it("always offers the manager — the only way out of the empty state", async () => {
    const user = userEvent.setup();
    const { onManage } = renderMenu({ labels: [] });
    await open(user);
    await user.click(screen.getByRole("menuitem", { name: /administrar etiquetas/i }));
    expect(onManage).toHaveBeenCalled();
  });

  it("CLOSES on the manager, unlike a tick — it navigates away", async () => {
    const user = userEvent.setup();
    renderMenu();
    await open(user);
    await user.click(screen.getByRole("menuitem", { name: /administrar etiquetas/i }));
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });
});

describe("dismissal — both routes, because each one alone traps someone", () => {
  it("closes on Escape, which is the keyboard user's way out", async () => {
    const user = userEvent.setup();
    renderMenu();
    await open(user);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("returns focus to the trigger on close, so Tab order is not lost", async () => {
    const user = userEvent.setup();
    renderMenu();
    await open(user);
    await user.keyboard("{Escape}");
    expect(screen.getByRole("button", { name: /etiquetar como/i })).toHaveFocus();
  });

  it("moves focus INTO the menu on open, or it is unusable by keyboard", async () => {
    const user = userEvent.setup();
    renderMenu();
    await open(user);
    expect(screen.getByRole("menuitemcheckbox", { name: /work/i })).toHaveFocus();
  });
});

describe("the imperative open, which the `l` shortcut uses", () => {
  it("publishes an open() that raises the menu", () => {
    let openMenu: (() => void) | undefined;
    renderMenu({
      onReady: (fn) => {
        openMenu = fn;
      },
    });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    // `act` because this is a state update fired from OUTSIDE React's event
    // system — which is precisely what the `l` key handler does.
    act(() => {
      openMenu?.();
    });
    expect(screen.getByRole("menu")).toBeInTheDocument();
  });

  it("does nothing when the trigger is disabled — the same outcome as clicking it", () => {
    let openMenu: (() => void) | undefined;
    renderMenu({
      disabled: true,
      onReady: (fn) => {
        openMenu = fn;
      },
    });
    act(() => {
      openMenu?.();
    });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });
});
