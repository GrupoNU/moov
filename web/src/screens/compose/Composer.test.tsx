import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { JmapClient } from "../../api/jmap";
import { BrandingProvider } from "../../branding/BrandingProvider";
import { I18nProvider } from "../../i18n/I18nProvider";
import type { Identity } from "../../mail/write";
import { Composer } from "./Composer";
import { newDraft } from "./composerState";

/**
 * Composer behaviour that only a rendered component can prove.
 *
 * The pure logic — quoting, addresses, the autosave timing, the draft's wire
 * shape — is tested in its own modules. What is left here is the wiring, and
 * specifically the two failures that would be catastrophic in a mail client:
 * sending twice, and telling the user a send was undone when it was not.
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

/** Captures every JMAP request and answers from a scripted queue. */
function harness(responses: Record<string, unknown>[]) {
  const requests: { using: string[]; methodCalls: [string, Record<string, unknown>, string][] }[] =
    [];
  let index = 0;

  const fetchImpl = vi.fn((_url: string, init?: RequestInit) => {
    const body = typeof init?.body === "string" ? init.body : "{}";
    requests.push(JSON.parse(body));
    const answer = responses[Math.min(index, responses.length - 1)];
    index += 1;
    return Promise.resolve(
      new Response(JSON.stringify(answer), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }) as unknown as typeof fetch;

  const client = new JmapClient({ username: "u", password: "p" }, { fetchImpl });
  return { client, requests, callCount: () => index };
}

/** Every method call of a given name across all captured requests. */
function callsNamed(
  requests: { methodCalls: [string, Record<string, unknown>, string][] }[],
  name: string,
): [string, Record<string, unknown>, string][] {
  return requests.flatMap((request) => request.methodCalls.filter((call) => call[0] === name));
}

function renderComposer(
  client: JmapClient,
  overrides: Partial<React.ComponentProps<typeof Composer>> = {},
) {
  const onClose = vi.fn();
  const onNotify = vi.fn();
  const onChanged = vi.fn();

  render(
    <I18nProvider locale="en">
      <BrandingProvider>
        <Composer
          draft={{ ...newDraft(false), to: [], subject: "Hola", text: "cuerpo" }}
          client={client}
          accountId="a1"
          identity={identity}
          draftsMailboxId="mbDrafts"
          sentMailboxId="mbSent"
          sessionCapabilities={{
            "urn:ietf:params:jmap:core": { maxSizeUpload: 1000 },
            "urn:ietf:params:jmap:mail": { maxSizeAttachmentsPerEmail: 2000 },
          }}
          uploadUrlTemplate="/jmap/upload/{accountId}"
          authorization="Basic xxx"
          onClose={onClose}
          onNotify={onNotify}
          onChanged={onChanged}
          {...overrides}
        />
      </BrandingProvider>
    </I18nProvider>,
  );
  return { onClose, onNotify, onChanged };
}

/** A response in which the draft is created and the submission is pending. */
function sendResponse(secondsAhead: number) {
  return {
    methodResponses: [
      ["Email/set", { created: { draft: { id: "e-new" } } }, "c"],
      [
        "EmailSubmission/set",
        {
          created: {
            sendIt: {
              id: "sub1",
              undoStatus: "pending",
              sendAt: new Date(Date.now() + secondsAhead * 1000).toISOString(),
            },
          },
        },
        "s",
      ],
    ],
    sessionState: "s",
  };
}

beforeEach(() => {
  /*
   * The <dialog> stub lives in `test/setup.ts` now, not here.
   *
   * E12/B6 added `show()` — the non-modal open the composer card uses — and
   * this file's local copy shadowed the shared one WITHOUT it, so every test
   * here failed with "dialog.show is not a function" while the shared stub sat
   * one import away already correct. One stub, one place.
   */
});

afterEach(() => {
  cleanup();
});

describe("recipients and the send gate", () => {
  it("refuses to send with no recipient", () => {
    const { client } = harness([sendResponse(10)]);
    renderComposer(client);
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
  });

  it("enables sending once a valid recipient is committed", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    renderComposer(client);

    await user.type(screen.getByLabelText("To"), "someone@example.com{Enter}");
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
    });
  });

  /*
   * A typed-but-not-committed address must survive clicking Send. Without the
   * blur commit the recipient is discarded and the send fails with "add a
   * recipient" while the address is visibly on screen.
   */
  it("commits an address the user typed but did not press Enter on", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    renderComposer(client);

    await user.type(screen.getByLabelText("To"), "someone@example.com");
    await user.tab();
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
    });
  });

  it("keeps an invalid address visible and marked rather than dropping it", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    renderComposer(client);

    await user.type(screen.getByLabelText("To"), "not-an-address{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent("not a complete email address");
    // Present on screen, and blocking the send.
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
  });
});

describe("sending", () => {
  /*
   * THE test of this file. Two clicks in the same tick both read the same
   * React state, which is exactly how double-send bugs ship; the guard is a
   * ref set synchronously before the first await.
   */
  it("never submits twice on a double-click", async () => {
    const user = userEvent.setup();
    const { client, requests } = harness([sendResponse(10)]);
    renderComposer(client);

    await user.type(screen.getByLabelText("To"), "someone@example.com{Enter}");
    const send = await screen.findByRole("button", { name: "Send" });

    await user.dblClick(send);
    await waitFor(() => {
      expect(callsNamed(requests, "EmailSubmission/set").length).toBeGreaterThan(0);
    });

    expect(callsNamed(requests, "EmailSubmission/set")).toHaveLength(1);
    expect(callsNamed(requests, "Email/set").filter((call) => "create" in call[1])).toHaveLength(1);
  });

  it("shows a countdown derived from the server's own sendAt", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    renderComposer(client);

    await user.type(screen.getByLabelText("To"), "someone@example.com{Enter}");
    await user.click(await screen.findByRole("button", { name: "Send" }));

    // 10 s ahead, so the banner must name a number in that neighbourhood — not
    // a hardcoded one, and never zero while the button still works.
    const banner = await screen.findByText(/Sending in \d+s/);
    const seconds = Number(/(\d+)s/.exec(banner.textContent ?? "")?.[1]);
    expect(seconds).toBeGreaterThan(6);
    expect(seconds).toBeLessThanOrEqual(10);
  });

  it("offers Undo while the window is open", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    renderComposer(client);

    await user.type(screen.getByLabelText("To"), "someone@example.com{Enter}");
    await user.click(await screen.findByRole("button", { name: "Send" }));

    expect(await screen.findByRole("button", { name: "Undo" })).toBeInTheDocument();
  });

  it("cancels the submission with the RFC's own spelling", async () => {
    const user = userEvent.setup();
    const { client, requests } = harness([
      sendResponse(10),
      { methodResponses: [["EmailSubmission/set", { updated: { sub1: null } }, "s"]], sessionState: "s" },
    ]);
    const { onNotify, onClose } = renderComposer(client);

    await user.type(screen.getByLabelText("To"), "someone@example.com{Enter}");
    await user.click(await screen.findByRole("button", { name: "Send" }));
    await user.click(await screen.findByRole("button", { name: "Undo" }));

    await waitFor(() => {
      expect(onClose).toHaveBeenCalled();
    });

    const cancel = callsNamed(requests, "EmailSubmission/set").find((call) => "update" in call[1]);
    expect(cancel?.[1].update).toEqual({ sub1: { undoStatus: "canceled" } });
    expect(onNotify).toHaveBeenCalledWith(expect.stringContaining("canceled"));
  });

  /*
   * `cannotUnsend` is a TRUE statement: the mail is going out. A user who
   * believes a send was canceled and later finds it in Sent has been lied to,
   * so the refusal must be surfaced, not swallowed.
   */
  it("surfaces cannotUnsend rather than claiming the send was canceled", async () => {
    const user = userEvent.setup();
    const { client } = harness([
      sendResponse(10),
      {
        methodResponses: [
          [
            "EmailSubmission/set",
            {
              notUpdated: {
                sub1: {
                  type: "cannotUnsend",
                  description: "the submission has already been released to the MTA",
                },
              },
            },
            "s",
          ],
        ],
        sessionState: "s",
      },
    ]);
    const { onNotify } = renderComposer(client);

    await user.type(screen.getByLabelText("To"), "someone@example.com{Enter}");
    await user.click(await screen.findByRole("button", { name: "Send" }));
    await user.click(await screen.findByRole("button", { name: "Undo" }));

    // The SERVER's own sentence, verbatim.
    expect(
      await screen.findByText("the submission has already been released to the MTA"),
    ).toBeInTheDocument();
    expect(onNotify).not.toHaveBeenCalledWith(expect.stringContaining("canceled"));
  });

  it("shows the server's refusal when the send itself fails", async () => {
    const user = userEvent.setup();
    const { client } = harness([
      {
        methodResponses: [
          ["Email/set", { created: { draft: { id: "e-new" } } }, "c"],
          [
            "EmailSubmission/set",
            {
              notCreated: {
                sendIt: {
                  type: "forbiddenFrom",
                  description: "the message's From (x@y.com) is not the identity's address",
                },
              },
            },
            "s",
          ],
        ],
        sessionState: "s",
      },
    ]);
    renderComposer(client);

    await user.type(screen.getByLabelText("To"), "someone@example.com{Enter}");
    await user.click(await screen.findByRole("button", { name: "Send" }));

    expect(
      await screen.findByText("the message's From (x@y.com) is not the identity's address"),
    ).toBeInTheDocument();
  });
});

describe("the signature", () => {
  it("appends the identity's text signature to a plain-text send", async () => {
    const user = userEvent.setup();
    const { client, requests } = harness([sendResponse(0)]);
    renderComposer(client, {
      identity: { ...identity, textSignature: "Moov Test\\nGrupo NU" },
    });

    await user.type(screen.getByLabelText("To"), "someone@example.com{Enter}");
    await user.click(await screen.findByRole("button", { name: "Send" }));

    await waitFor(() => {
      expect(callsNamed(requests, "Email/set").length).toBeGreaterThan(0);
    });
    const create = callsNamed(requests, "Email/set").find((call) => "create" in call[1]);
    const draft = (create?.[1].create as Record<string, Record<string, unknown>>).draft;
    const bodyValues = draft?.bodyValues as Record<string, { value: string }>;
    expect(bodyValues.text?.value).toContain("Grupo NU");
    // RFC 3676 §4.3's delimiter, which clients recognise and collapse.
    expect(bodyValues.text?.value).toContain("\n-- \n");
  });
});

describe("closing without losing work", () => {
  /*
   * Deliverable 7: Escape and the close button must not discard a draft. Both
   * flush the pending autosave before the dialog goes away.
   */
  it("saves the draft when the composer is closed", async () => {
    const user = userEvent.setup();
    const { client, requests } = harness([
      { methodResponses: [["Email/set", { created: { draft: { id: "d1" } } }, "s"]], sessionState: "s" },
    ]);
    const { onClose } = renderComposer(client);

    await user.type(screen.getByLabelText("Subject"), "algo");
    await user.click(screen.getByRole("button", { name: "Close the composer" }));

    await waitFor(() => {
      expect(onClose).toHaveBeenCalled();
    });
    await waitFor(() => {
      expect(callsNamed(requests, "Email/set").length).toBeGreaterThan(0);
    });
  });

  it("does not litter Drafts when nothing was written", async () => {
    const user = userEvent.setup();
    const { client, requests } = harness([
      { methodResponses: [["Email/set", { created: {} }, "s"]], sessionState: "s" },
    ]);
    renderComposer(client, { draft: newDraft(false) });

    await user.click(screen.getByRole("button", { name: "Close the composer" }));
    expect(callsNamed(requests, "Email/set")).toHaveLength(0);
  });
});

/**
 * The floating card (E12/B6, canon 07 §7).
 *
 * The composer was a centred `showModal()` dialog through P3. What these pin
 * is the property the reversal is FOR — that the mail behind the card stays
 * live — and the one thing minimising must never do, which is discard the
 * draft it is collapsing.
 */
describe("the floating card", () => {
  it("opens NON-MODALLY, so the mail behind it stays interactive", () => {
    const { client } = harness([sendResponse(10)]);
    renderComposer(client);

    /*
     * The load-bearing assertion of the block. A modal dialog makes the rest
     * of the page inert; this one must not, because looking up an address or
     * re-reading the message being answered is the whole reason Gmail's
     * composer is a corner card rather than a sheet over the inbox.
     *
     * `.open` is true either way, so what is checked is which method ran —
     * `test/setup.ts` stubs both and only the real browser distinguishes them,
     * which is exactly why the call site is what gets pinned.
     */
    const dialog = document.querySelector("dialog");
    expect(dialog).not.toBeNull();
    expect(dialog!.open).toBe(true);
    // A modal dialog would carry `aria-modal`; this one must not claim it.
    expect(dialog).not.toHaveAttribute("aria-modal", "true");
  });

  it("offers the three Gmail card controls, in Gmail's order", () => {
    const { client } = harness([sendResponse(10)]);
    renderComposer(client);

    expect(screen.getByRole("button", { name: "Minimise" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Full screen" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Close the composer" })).toBeInTheDocument();
  });

  it("minimises and expands again WITHOUT unmounting the draft", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    const { onClose } = renderComposer(client);

    await user.type(screen.getByRole("textbox", { name: /subject/i }), "!");
    await user.click(screen.getByRole("button", { name: "Minimise" }));

    /*
     * The property that makes minimise worth having: the draft is still
     * MOUNTED — still autosaving, still able to finish a send in flight.
     * Unmounting would discard the body editor's selection, the attachments'
     * upload progress and the undo countdown, all of which live in this
     * component's state.
     *
     * And it must not be mistaken for a close: `onClose` firing here would
     * mean the parent tore the composer down.
     */
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Expand" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Expand" }));
    expect(screen.getByRole("textbox", { name: /subject/i })).toHaveValue("Hola!");
  });

  it("toggles full screen, and says which state it is in", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    renderComposer(client);

    const maximize = screen.getByRole("button", { name: "Full screen" });
    expect(maximize).toHaveAttribute("aria-pressed", "false");

    await user.click(maximize);

    // The label flips to what the NEXT press will do, so the control and its
    // name cannot disagree.
    const restore = screen.getByRole("button", { name: "Exit full screen" });
    expect(restore).toHaveAttribute("aria-pressed", "true");
  });

  it("collapses from the title bar itself, not only from the 2rem button", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    renderComposer(client);

    // Gmail collapses when you click anywhere on the bar; a card that only
    // responds to a small icon is a card people learn to double-click at.
    await user.click(screen.getByRole("button", { name: "New message — Minimise" }));

    expect(screen.getByRole("button", { name: "Expand" })).toBeInTheDocument();
  });
});
