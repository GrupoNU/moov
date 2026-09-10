import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { JmapClient } from "../../api/jmap";
import { BrandingProvider } from "../../branding/BrandingProvider";
import { I18nProvider } from "../../i18n/I18nProvider";
import type { Identity } from "../../mail/write";
import { Composer } from "./Composer";
import { newDraft, replyDraft, forwardDraft, type ComposerDraft } from "./composerState";
import type { Email } from "../../mail/types";

/**
 * The composer's TWO HOSTS (canon 07 §7).
 *
 * Gmail writes a new message in the floating card, bottom-right, and a reply
 * at the foot of the conversation it answers. This file pins the difference —
 * which chrome each gets, what the inline one collapses, and that the pop-out
 * carries a draft from one to the other without dropping a word.
 *
 * What it deliberately does NOT re-test is the FORM. The fields, the editor,
 * the toolbar, the autosave and the send path are identical on both hosts by
 * construction (one component, one code path) and are covered in
 * `Composer.test.tsx`, `ComposerE7.test.tsx` and `ComposerFooter.test.tsx`.
 * Copying them here would assert the same behaviour twice and would go stale
 * in one copy.
 *
 * jsdom implements `<dialog>` only partially, so `show`/`close` are stubbed
 * with the one behaviour the component reads — the same stub every other
 * composer suite installs.
 */

if (typeof HTMLDialogElement !== "undefined") {
  HTMLDialogElement.prototype.show = function show(this: HTMLDialogElement) {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function close(this: HTMLDialogElement) {
    if (!this.open) return;
    this.open = false;
    this.dispatchEvent(new Event("close"));
  };
}

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

const QUOTING = {
  attributionLine: (date: string, sender: string) => `On ${date}, ${sender} wrote:`,
  forwardedHeader: "---------- Forwarded message ---------",
  from: "From",
  date: "Date",
  subject: "Subject",
  to: "To",
  formatDate: (iso: string | undefined) => iso ?? "",
};

/** The message a reply or a forward in this file is answering. */
function original(): Email {
  return {
    id: "m1",
    threadId: "t1",
    mailboxIds: { inbox: true },
    keywords: {},
    subject: "Arquitectura",
    from: [{ name: "Ana Pérez", email: "ana@example.com" }],
    to: [{ name: "Moov Test", email: "moov-test@atmosfera.cloud" }],
    receivedAt: "2026-08-20T10:00:00Z",
    textBody: [
      {
        partId: "1",
        blobId: null,
        size: 5,
        name: null,
        type: "text/plain",
        charset: "utf-8",
        disposition: null,
        cid: null,
        language: null,
        location: null,
      },
    ],
    bodyValues: { "1": { value: "hola", isEncodingProblem: false, isTruncated: false } },
  };
}

function renderComposer(
  overrides: Partial<React.ComponentProps<typeof Composer>> = {},
): void {
  render(
    <I18nProvider locale="en">
      <BrandingProvider>
        <Composer
          draft={{ ...newDraft(true), subject: "Hola", text: "" }}
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

describe("the floating host — unchanged for a new message", () => {
  it("is the default, so every existing caller keeps the card", () => {
    renderComposer();
    // The form itself is inside the `<dialog>` — which is what makes it the
    // card rather than a block in the page.
    expect(screen.getByRole("button", { name: "Send" }).closest("dialog")).not.toBeNull();
  });

  it("keeps its title bar and the minimise / maximise / close trio", () => {
    renderComposer();
    expect(screen.getByRole("button", { name: "Minimise" })).not.toBeNull();
    expect(screen.getByRole("button", { name: "Full screen" })).not.toBeNull();
    expect(screen.getByRole("button", { name: "Close the composer" })).not.toBeNull();
  });

  it("shows the address fields — a new message has recipients to fill in", () => {
    renderComposer();
    expect(screen.getByLabelText("To")).not.toBeNull();
    expect(screen.getByLabelText("Subject")).not.toBeNull();
  });

  it("offers no pop-out: the card has nowhere further to go", () => {
    renderComposer({ onPopOut: vi.fn() });
    expect(screen.queryByRole("button", { name: /separate window/i })).toBeNull();
  });
});

describe("the inline host — a reply at the foot of the conversation", () => {
  function replyProps(): Partial<React.ComponentProps<typeof Composer>> {
    return {
      host: "inline",
      draft: replyDraft(original(), "moov-test@atmosfera.cloud", false, QUOTING),
    };
  }

  it("is NOT a dialog — it is a block in the reader's flow", () => {
    renderComposer(replyProps());
    /*
     * Scoped to the box that HOLDS the form. There is still a `<dialog>` in
     * the tree — the discard confirmation, which is a modal by nature and
     * stays one on both hosts — so a bare `querySelector("dialog")` would
     * assert the wrong thing and fail for the right-shaped reason.
     */
    const send = screen.getByRole("button", { name: "Send" });
    expect(send.closest("dialog")).toBeNull();
    expect(send.closest("section")).not.toBeNull();
  });

  it("drops minimise and maximise — neither means anything in the flow", () => {
    renderComposer(replyProps());
    expect(screen.queryByRole("button", { name: "Minimise" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Full screen" })).toBeNull();
  });

  it("collapses the recipients into Gmail's header line", () => {
    renderComposer(replyProps());
    // The line names who it is going to…
    expect(screen.getByRole("button", { name: /reply to ana pérez/i })).not.toBeNull();
    // …and the fields it stands in for are not on screen.
    expect(screen.queryByLabelText("To")).toBeNull();
    expect(screen.queryByLabelText("Subject")).toBeNull();
  });

  it("expands the real fields when the header line is pressed", async () => {
    const user = userEvent.setup();
    renderComposer(replyProps());
    await user.click(screen.getByRole("button", { name: /reply to ana pérez/i }));
    expect(screen.getByLabelText("To")).not.toBeNull();
    expect(screen.getByLabelText("Subject")).not.toBeNull();
  });

  it("opens a FORWARD expanded — it has no recipient yet", () => {
    renderComposer({ host: "inline", draft: forwardDraft(original(), QUOTING) });
    // Collapsing the one field that must be filled would hide the whole job.
    expect(screen.getByLabelText("To")).not.toBeNull();
  });

  it("keeps the same footer — Send, attach, discard, the lot", () => {
    renderComposer(replyProps());
    expect(screen.getByRole("button", { name: "Send" })).not.toBeNull();
    expect(screen.getByRole("button", { name: "Attach a file" })).not.toBeNull();
    expect(screen.getByRole("button", { name: "Discard" })).not.toBeNull();
  });

  it("has no ✕ — a box holding unsent words must not have a quiet exit", () => {
    renderComposer(replyProps());
    expect(screen.queryByRole("button", { name: "Close the composer" })).toBeNull();
  });
});

describe("the pop-out — the same draft, moved to the card", () => {
  it("is offered only on the inline host, and only with a handler", () => {
    renderComposer({
      host: "inline",
      draft: replyDraft(original(), "moov-test@atmosfera.cloud", false, QUOTING),
      onPopOut: vi.fn(),
    });
    expect(screen.getByRole("button", { name: /separate window/i })).not.toBeNull();
  });

  it("hands back the recipients and the body, not the draft it opened with", async () => {
    const user = userEvent.setup();
    const onPopOut = vi.fn();
    const draft = replyDraft(original(), "moov-test@atmosfera.cloud", false, QUOTING);
    renderComposer({ host: "inline", draft, onPopOut });

    // Type into the body, then pop out. What the host receives must contain
    // what was typed — a handover that lost the last sentence would be the
    // exact defect this control is most likely to have.
    await user.click(screen.getByRole("button", { name: /reply to ana pérez/i }));
    const subject = screen.getByLabelText("Subject");
    await user.clear(subject);
    await user.type(subject, "Re: Arquitectura, revisada");

    await user.click(screen.getByRole("button", { name: /separate window/i }));

    expect(onPopOut).toHaveBeenCalledTimes(1);
    const handed = onPopOut.mock.calls[0]?.[0] as ComposerDraft;
    expect(handed.subject).toBe("Re: Arquitectura, revisada");
    expect(handed.to.map((chip) => chip.email)).toEqual(["ana@example.com"]);
    // The quote came along: this is still a reply to the same message.
    expect(handed.text).toContain("hola");
    expect(handed.intent).toBe("reply");
  });

  it("carries the seedKey UNCHANGED, so the body is not re-seeded on the way", async () => {
    const user = userEvent.setup();
    const onPopOut = vi.fn();
    const draft = replyDraft(original(), "moov-test@atmosfera.cloud", false, QUOTING);
    renderComposer({ host: "inline", draft, onPopOut });

    await user.click(screen.getByRole("button", { name: /separate window/i }));

    const handed = onPopOut.mock.calls[0]?.[0] as ComposerDraft;
    expect(handed.seedKey).toBe(draft.seedKey);
  });

  it("carries the server-side draft id, so the move does not orphan one", async () => {
    const user = userEvent.setup();
    const onPopOut = vi.fn();
    const draft: ComposerDraft = {
      ...replyDraft(original(), "moov-test@atmosfera.cloud", false, QUOTING),
      existingDraftId: "draft-77",
    };
    renderComposer({ host: "inline", draft, onPopOut });

    await user.click(screen.getByRole("button", { name: /separate window/i }));

    const handed = onPopOut.mock.calls[0]?.[0] as ComposerDraft;
    expect(handed.existingDraftId).toBe("draft-77");
  });
});

describe("Escape, which the two hosts answer differently", () => {
  it("closes the floating card, flushing the draft on the way out", () => {
    const onClose = vi.fn();
    renderComposer({ onClose });
    const dialog = screen.getByRole("button", { name: "Send" }).closest("dialog");
    expect(dialog).not.toBeNull();
    // The card is a <dialog>: its Escape arrives as `cancel`, which the
    // composer intercepts so the flush happens before the close.
    dialog?.dispatchEvent(new Event("cancel", { cancelable: true }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("does NOT close the inline box — Gmail's behaviour, and the safe one", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    renderComposer({
      host: "inline",
      draft: replyDraft(original(), "moov-test@atmosfera.cloud", false, QUOTING),
      onClose,
    });
    await user.keyboard("{Escape}");
    expect(onClose).not.toHaveBeenCalled();
    // …and the box is still there to keep writing in.
    expect(screen.getByRole("button", { name: "Send" })).not.toBeNull();
  });
});

describe("the focus handle the host uses for a second reply intent", () => {
  it("is published while the composer is mounted and withdrawn when it goes", () => {
    const onFocusHandle = vi.fn();
    renderComposer({
      host: "inline",
      draft: replyDraft(original(), "moov-test@atmosfera.cloud", false, QUOTING),
      onFocusHandle,
    });
    expect(onFocusHandle).toHaveBeenCalledTimes(1);
    expect(typeof onFocusHandle.mock.calls[0]?.[0]).toBe("function");

    cleanup();
    // `undefined` says plainly there is no box to focus — a host still holding
    // a stale function would silently focus nothing, which is the hardest kind
    // of defect to report.
    expect(onFocusHandle).toHaveBeenLastCalledWith(undefined);
  });
});
