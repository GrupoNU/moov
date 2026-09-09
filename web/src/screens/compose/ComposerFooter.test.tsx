import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { JmapClient } from "../../api/jmap";
import { BrandingProvider } from "../../branding/BrandingProvider";
import { I18nProvider } from "../../i18n/I18nProvider";
import type { Identity } from "../../mail/write";
import { Composer } from "./Composer";
import { newDraft } from "./composerState";

/**
 * T4 — the composer's FOOTER, which is where Gmail keeps everything that acts
 * on the message you are writing (canon 07 §7; review items D-01…D-09).
 *
 * The review found the composer structurally right and its footer wrong: the
 * formatting row was above the body, "Descartar" was a word next to the ⋯, the
 * row held four controls where Gmail holds nine, and Send was a rectangle.
 * These tests pin the SHAPE, because the shape is the finding — a screenshot
 * comparison is what caught it and a screenshot comparison is not a test.
 */

const identity: Identity = {
  id: "primary",
  name: "Moov Test",
  email: "moov-test@atmosfera.cloud",
  replyTo: null,
  bcc: null,
  textSignature: "",
  htmlSignature: "",
  mayDelete: false,
};

function client(): JmapClient {
  const fetchImpl = vi.fn(() =>
    Promise.resolve(
      new Response(JSON.stringify({ methodResponses: [], sessionState: "s" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  ) as unknown as typeof fetch;
  return new JmapClient({ username: "u", password: "p" }, { fetchImpl });
}

function renderComposer(
  overrides: Partial<React.ComponentProps<typeof Composer>> = {},
): void {
  render(
    <I18nProvider locale="en">
      <BrandingProvider>
        <Composer
          draft={{ ...newDraft(true), to: [], subject: "Hola", text: "" }}
          client={client()}
          accountId="a1"
          identity={identity}
          draftsMailboxId="mbDrafts"
          sentMailboxId="mbSent"
          sessionCapabilities={{}}
          uploadUrlTemplate="/jmap/upload/{accountId}"
          authorization="Basic xxx"
          onClose={vi.fn()}
          onNotify={vi.fn()}
          onChanged={vi.fn()}
          {...overrides}
        />
      </BrandingProvider>
    </I18nProvider>,
  );
}

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  localStorage.clear();
});

describe("D-01/D-04 — the formatting row lives in the footer, behind Aa", () => {
  it("is hidden by default, as Gmail's is", () => {
    renderComposer();
    expect(screen.queryByRole("toolbar", { name: "Formatting" })).toBeNull();
  });

  it("no longer offers the permanent Plain text | Formatting segmented control", () => {
    renderComposer();
    // D-04: the ⋯ menu's menuitemcheckbox is the canonical control for this one
    // state. A second, always-visible pair of buttons is how two controls end
    // up disagreeing about one boolean.
    expect(screen.queryByRole("button", { name: "Plain text" })).toBeNull();
  });

  it("Aa reveals the row INSIDE the footer, not above the body", async () => {
    const user = userEvent.setup();
    renderComposer();

    await user.click(screen.getByRole("button", { name: "Formatting options" }));

    const toolbar = screen.getByRole("toolbar", { name: "Formatting" });
    expect(toolbar).not.toBeNull();
    // The finding was positional, so the assertion is positional: the row must
    // be a descendant of the footer that holds Send.
    const footer = screen.getByRole("button", { name: "Send" }).closest("footer");
    expect(footer).not.toBeNull();
    expect(footer?.contains(toolbar)).toBe(true);
    // And it really is the formatting row: the commands came with it.
    expect(within(toolbar).getByRole("button", { name: "Bold" })).not.toBeNull();
  });

  it("remembers the choice across composers (view chrome, not a preference)", async () => {
    const user = userEvent.setup();
    renderComposer();
    await user.click(screen.getByRole("button", { name: "Formatting options" }));
    cleanup();

    renderComposer();
    expect(screen.getByRole("toolbar", { name: "Formatting" })).not.toBeNull();
  });

  it("switches to rich mode when opened from plain, rather than showing dead buttons", async () => {
    const user = userEvent.setup();
    // A plain-text composition: the commands would act on a textarea.
    renderComposer({ draft: { ...newDraft(false), to: [], subject: "", text: "hola" } });

    await user.click(screen.getByRole("button", { name: "Formatting options" }));

    // The rich surface is now the body, and the row is showing over it.
    expect(screen.getByRole("textbox", { name: "Message" }).getAttribute("contenteditable")).toBe(
      "true",
    );
    expect(screen.getByRole("toolbar", { name: "Formatting" })).not.toBeNull();
  });
});

describe("D-02 — discard is a trash icon isolated at the right", () => {
  it("is no longer a word beside the ... menu", () => {
    renderComposer();
    const discard = screen.getByRole("button", { name: "Discard" });
    // An icon-only control: its accessible name comes from aria-label, and its
    // visible text is empty. A word here is what put it next to the overflow.
    expect(discard.textContent).toBe("");
    expect(discard.querySelector("svg")).not.toBeNull();
  });

  it("sits after every other footer control, at the far right", () => {
    renderComposer();
    const footer = screen.getByRole("button", { name: "Send" }).closest("footer");
    expect(footer).not.toBeNull();
    const buttons = [...(footer?.querySelectorAll("button") ?? [])];
    const discard = screen.getByRole("button", { name: "Discard" });
    expect(buttons[buttons.length - 1]).toBe(discard);
  });

  it("still confirms before destroying a draft that has content", async () => {
    const user = userEvent.setup();
    renderComposer({
      draft: { ...newDraft(true), to: [], subject: "algo", text: "cuerpo" },
    });

    await user.click(screen.getByRole("button", { name: "Discard" }));

    // E11's own dialog, unchanged: the icon lowers the chance of a reflex
    // press, it does not replace the guard behind it.
    expect(
      await screen.findByText("Discard this draft? What you wrote will be lost."),
    ).not.toBeNull();
  });
});
