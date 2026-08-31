import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { JmapClient } from "../../api/jmap";
import { BrandingProvider } from "../../branding/BrandingProvider";
import { I18nProvider } from "../../i18n/I18nProvider";
import type { IndexedAddress } from "../../mail/addressIndex";
import type { Identity } from "../../mail/write";
import { Composer } from "./Composer";
import { newDraft } from "./composerState";

/**
 * E7 — identity and daily sending (canon §2.3).
 *
 * Its own file rather than more of `Composer.test.tsx`, which is already the
 * home of the two catastrophic-failure tests (double send, false undo) and
 * reads better for staying focused on them.
 *
 * The test that matters most here is the Send & Archive INVERSE: the archive
 * is allowed to happen immediately only because undoing the send also
 * un-archives, and without that guarantee the feature would strand
 * conversations outside the inbox for messages that were never sent.
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
  return { client, requests };
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
  window.localStorage.clear();
});

afterEach(cleanup);

describe("Send & Archive", () => {
  it("is absent when there is no conversation to archive", () => {
    const { client } = harness([sendResponse(10)]);
    renderComposer(client);
    // A new message has nothing to archive, and a button that cannot act is
    // exactly the dead control P4 forbids.
    expect(screen.queryByRole("button", { name: "Send & archive" })).toBeNull();
  });

  it("sends and archives, reporting both in one message", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(0)]);
    const onSendAndArchive = vi.fn(() => Promise.resolve(true));
    const { onNotify } = renderComposer(client, { onSendAndArchive });

    await user.type(screen.getByLabelText("To"), "someone@example.com{Enter}");
    await user.click(await screen.findByRole("button", { name: "Send & archive" }));

    await waitFor(() => {
      expect(onSendAndArchive).toHaveBeenCalled();
    });
    expect(onNotify).toHaveBeenCalledWith("Message sent — conversation archived");
  });

  it("does not archive on a plain Send", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(0)]);
    const onSendAndArchive = vi.fn(() => Promise.resolve(true));
    const { onNotify } = renderComposer(client, { onSendAndArchive });

    await user.type(screen.getByLabelText("To"), "someone@example.com{Enter}");
    await user.click(await screen.findByRole("button", { name: "Send" }));

    await waitFor(() => {
      expect(onNotify).toHaveBeenCalledWith("Message sent");
    });
    expect(onSendAndArchive).not.toHaveBeenCalled();
  });

  it("archives IMMEDIATELY, while the undo window is still open", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    const onSendAndArchive = vi.fn(() => Promise.resolve(true));
    renderComposer(client, { onSendAndArchive });

    await user.type(screen.getByLabelText("To"), "someone@example.com{Enter}");
    await user.click(await screen.findByRole("button", { name: "Send & archive" }));

    // The undo window is open AND the archive has already happened — Gmail's
    // behaviour, and the reason the inverse below has to exist.
    expect(await screen.findByRole("button", { name: "Undo" })).toBeInTheDocument();
    await waitFor(() => {
      expect(onSendAndArchive).toHaveBeenCalled();
    });
  });

  /*
   * THE test of this feature. The archive is allowed to be immediate only
   * because undoing the send also un-archives; without this the user is left
   * hunting for a conversation that left their inbox for a message that was
   * never sent.
   */
  it("un-archives when the send is undone inside the window", async () => {
    const user = userEvent.setup();
    const { client } = harness([
      sendResponse(10),
      {
        methodResponses: [["EmailSubmission/set", { updated: { sub1: null } }, "s"]],
        sessionState: "s",
      },
    ]);
    const onSendAndArchive = vi.fn(() => Promise.resolve(true));
    const onUndoArchive = vi.fn(() => Promise.resolve());
    const { onNotify } = renderComposer(client, { onSendAndArchive, onUndoArchive });

    await user.type(screen.getByLabelText("To"), "someone@example.com{Enter}");
    await user.click(await screen.findByRole("button", { name: "Send & archive" }));
    await user.click(await screen.findByRole("button", { name: "Undo" }));

    await waitFor(() => {
      expect(onUndoArchive).toHaveBeenCalled();
    });
    expect(onNotify).toHaveBeenCalledWith(
      "Send canceled — the conversation is back in your inbox",
    );
  });

  it("does NOT un-archive when the cancel was refused", async () => {
    const user = userEvent.setup();
    const { client } = harness([
      sendResponse(10),
      {
        methodResponses: [
          [
            "EmailSubmission/set",
            { notUpdated: { sub1: { type: "cannotUnsend", description: "already released" } } },
            "s",
          ],
        ],
        sessionState: "s",
      },
    ]);
    const onSendAndArchive = vi.fn(() => Promise.resolve(true));
    const onUndoArchive = vi.fn(() => Promise.resolve());
    renderComposer(client, { onSendAndArchive, onUndoArchive });

    await user.type(screen.getByLabelText("To"), "someone@example.com{Enter}");
    await user.click(await screen.findByRole("button", { name: "Send & archive" }));
    await user.click(await screen.findByRole("button", { name: "Undo" }));

    await screen.findByText("already released");
    // The mail IS going out, so the conversation really has been replied to and
    // archived. Restoring it would contradict what happened.
    expect(onUndoArchive).not.toHaveBeenCalled();
  });

  it("says so honestly when the send worked but the archive did not", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    const onSendAndArchive = vi.fn(() => Promise.resolve(false));
    renderComposer(client, { onSendAndArchive });

    await user.type(screen.getByLabelText("To"), "someone@example.com{Enter}");
    await user.click(await screen.findByRole("button", { name: "Send & archive" }));

    expect(
      await screen.findByText(
        "The message was sent, but the conversation could not be archived",
      ),
    ).toBeInTheDocument();
  });
});

describe("blocked attachment extensions", () => {
  /** A File whose NAME is what the gate reads. */
  function file(name: string): File {
    return new File(["x"], name, { type: "application/octet-stream" });
  }

  /**
   * The real <input type="file">.
   *
   * The visible button and the clipped input share the accessible name (the
   * button forwards its click to the input), so the label alone is ambiguous.
   * Uploading has to target the input itself.
   */
  function fileInput(): HTMLInputElement {
    const input = document
      .querySelector<HTMLInputElement>('input[type="file"]');
    if (input === null) throw new Error("the composer has no file input");
    return input;
  }

  it("refuses an executable, naming it and saying why", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    renderComposer(client);

    await user.upload(fileInput(), file("setup.exe"));

    expect(
      await screen.findByText(
        "setup.exe was not attached: this kind of file is blocked because it presents a security risk.",
      ),
    ).toBeInTheDocument();
    // And it says what to do instead, rather than leaving a dead end.
    expect(
      screen.getByText("To send it, put it in a .zip first, or share it with a link."),
    ).toBeInTheDocument();
  });

  it("does not attach the blocked file at all — not even as a failed row", async () => {
    const user = userEvent.setup();
    const { client, requests } = harness([sendResponse(10)]);
    renderComposer(client);

    await user.upload(fileInput(), file("invoice.pdf.exe"));
    await screen.findByRole("alert");

    /*
     * A "failed" row would leave the name in the composer and invite a rename,
     * which is the behaviour the block exists to prevent. And nothing was
     * uploaded: the refusal happens before a single byte leaves the browser.
     */
    expect(screen.queryByText("invoice.pdf.exe")).toBeNull();
    expect(requests).toHaveLength(0);
  });

  it("attaches an ordinary document normally", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    renderComposer(client);

    await user.upload(fileInput(), file("informe.pdf"));

    expect(await screen.findByText("informe.pdf")).toBeInTheDocument();
    expect(screen.queryByText(/presents a security risk/)).toBeNull();
  });

  it("attaches a name whose blocked extension is not the final one", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    renderComposer(client);

    await user.upload(fileInput(), file("payload.exe.txt"));

    expect(await screen.findByText("payload.exe.txt")).toBeInTheDocument();
  });
});

describe("plain-text mode", () => {
  it("switches the body to plain from the menu, and remembers the choice", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    renderComposer(client, { draft: { ...newDraft(true), subject: "Hola" } });

    await user.click(screen.getByRole("button", { name: "More options" }));
    const toggle = await screen.findByRole("menuitemcheckbox", { name: "Plain text mode" });
    // Rich to begin with, so the toggle reads unchecked.
    expect(toggle).toHaveAttribute("aria-checked", "false");

    await user.click(toggle);

    // The choice outlives this composer — the whole point of persisting it.
    expect(window.localStorage.getItem("moov.composeBodyMode.v1")).toBe("plain");
  });

  it("reports the current mode in the menu", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("moov.composeBodyMode.v1", "plain");
    const { client } = harness([sendResponse(10)]);
    renderComposer(client, { draft: { ...newDraft(true), subject: "Hola" } });

    await user.click(screen.getByRole("button", { name: "More options" }));
    expect(
      await screen.findByRole("menuitemcheckbox", { name: "Plain text mode" }),
    ).toHaveAttribute("aria-checked", "true");
  });

  it("opens a new message in the remembered plain mode", () => {
    window.localStorage.setItem("moov.composeBodyMode.v1", "plain");
    const { client } = harness([sendResponse(10)]);
    // `newDraft(true)` asks for rich, but with no content yet the remembered
    // preference decides — that is what "remembered" has to mean.
    renderComposer(client, { draft: { ...newDraft(true), subject: "Hola" } });

    // The plain surface is a textarea, not the contenteditable rich surface.
    expect(screen.getByRole("textbox", { name: "Message" }).tagName).toBe("TEXTAREA");
  });

  it("never lets a remembered 'plain' flatten a reply's quoted HTML", () => {
    window.localStorage.setItem("moov.composeBodyMode.v1", "plain");
    const { client } = harness([sendResponse(10)]);
    /*
     * A composition that ALREADY carries HTML — a reply, a forward, a resumed
     * draft. The preference is a default for NEW writing, never a filter over
     * content the user is replying to, and flattening quoted material would
     * destroy exactly what they are responding to.
     */
    renderComposer(client, {
      draft: { ...newDraft(true), html: "<p>lo citado</p>", subject: "Re: algo" },
    });

    // The rich surface is present, so the quoted material survived.
    expect(screen.getByRole("textbox", { name: "Message" }).tagName).not.toBe("TEXTAREA");
  });
});

describe("address autocomplete in the composer", () => {
  const suggestions: readonly IndexedAddress[] = [
    {
      email: "ana@example.com",
      displayName: "Ana Gómez",
      lastSeenAt: 10,
      timesSeen: 4,
      source: "sent",
    },
  ];

  it("completes a recipient from the index", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(10)]);
    renderComposer(client, { addressSuggestions: suggestions });

    await user.type(screen.getByLabelText("To"), "ana");
    await user.keyboard("{ArrowDown}{Enter}");

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
    });
    expect(screen.getByTitle("ana@example.com")).toBeInTheDocument();
  });

  it("records the addresses a SUCCESSFUL send went to", async () => {
    const user = userEvent.setup();
    const { client } = harness([sendResponse(0)]);
    const onRecordAddresses = vi.fn();
    renderComposer(client, { onRecordAddresses });

    await user.type(screen.getByLabelText("To"), "nueva@example.com{Enter}");
    await user.click(await screen.findByRole("button", { name: "Send" }));

    await waitFor(() => {
      expect(onRecordAddresses).toHaveBeenCalledWith([{ name: null, email: "nueva@example.com" }]);
    });
  });

  it("records nothing when the send failed", async () => {
    const user = userEvent.setup();
    const { client } = harness([
      {
        methodResponses: [
          ["Email/set", { created: { draft: { id: "e-new" } } }, "c"],
          [
            "EmailSubmission/set",
            { notCreated: { sendIt: { type: "forbiddenFrom", description: "refused" } } },
            "s",
          ],
        ],
        sessionState: "s",
      },
    ]);
    const onRecordAddresses = vi.fn();
    renderComposer(client, { onRecordAddresses });

    await user.type(screen.getByLabelText("To"), "rechazada@example.com{Enter}");
    await user.click(await screen.findByRole("button", { name: "Send" }));

    await screen.findByText("refused");
    // An address the server refused is not one you corresponded with, and
    // indexing it would promote a typo into tomorrow's suggestions.
    expect(onRecordAddresses).not.toHaveBeenCalled();
  });
});
