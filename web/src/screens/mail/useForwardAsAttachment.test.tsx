import { act, render, screen } from "@testing-library/react";
import { useState } from "react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from "vitest";

import { JmapClient } from "../../api/jmap";
import type { Email } from "../../mail/types";
import type { ComposerAttachment } from "../compose/AttachmentList";
import { useForwardAsAttachment } from "./useForwardAsAttachment";

/**
 * Forward as attachment, at the level where the round trip happens (E7).
 *
 * The filename rules and the size arithmetic are pinned in
 * `mail/forwardAsAttachment.test.ts`. What is proven here is the wiring: that
 * the blob really is re-typed as `message/rfc822` on the way up, and — the one
 * that matters for multi-select — that one message failing does not take the
 * others with it.
 */

/** Captures every upload, answering with a blob id. */
function uploadHarness() {
  const uploads: { name: string; type: string }[] = [];

  class FakeXHR {
    status = 200;
    response: unknown = { blobId: "blob-new", size: 42, type: "message/rfc822" };
    responseType = "";
    upload = { onprogress: null };
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    onabort: (() => void) | null = null;
    ontimeout: (() => void) | null = null;
    open(): void {
      // no-op
    }
    setRequestHeader(): void {
      // no-op
    }
    send(body: File): void {
      uploads.push({ name: body.name, type: body.type });
      this.onload?.();
    }
  }

  vi.stubGlobal("XMLHttpRequest", FakeXHR);
  return { uploads };
}

function email(id: string, subject: string, size = 100): Email {
  return {
    id,
    blobId: `b-${id}`,
    threadId: `t-${id}`,
    mailboxIds: { mb1: true },
    keywords: {},
    subject,
    receivedAt: "2026-08-30T10:00:00Z",
    size,
  };
}

/** Renders the hook's result so assertions can read it out of the DOM. */
function Probe({
  client,
  emails,
  capabilities,
}: {
  readonly client: JmapClient;
  readonly emails: readonly Email[];
  readonly capabilities?: Readonly<Record<string, unknown>>;
}): React.JSX.Element {
  const { prepare, isPreparing } = useForwardAsAttachment({
    client,
    accountId: "a1",
    authorization: "Basic xxx",
    uploadUrlTemplate: "/jmap/upload/{accountId}",
    sessionCapabilities: capabilities ?? {
      "urn:ietf:params:jmap:core": { maxSizeUpload: 1000 },
      "urn:ietf:params:jmap:mail": { maxSizeAttachmentsPerEmail: 10_000 },
    },
  });
  const [result, setResult] = useState<{
    attachments: readonly ComposerAttachment[];
    refused: readonly string[];
  }>();

  return (
    <div>
      <span data-testid="preparing">{String(isPreparing)}</span>
      <span data-testid="names">
        {(result?.attachments ?? []).map((entry) => entry.name).join("|")}
      </span>
      <span data-testid="types">
        {(result?.attachments ?? []).map((entry) => entry.type).join("|")}
      </span>
      <span data-testid="refused">{(result?.refused ?? []).join("|")}</span>
      <button
        type="button"
        onClick={() => {
          void prepare(emails).then(setResult);
        }}
      >
        prepare
      </button>
    </div>
  );
}

/**
 * A client whose `downloadBlob` answers with bytes, or throws for one blob id.
 *
 * The spy is returned ALONGSIDE the client rather than read back off it at the
 * assertion: `expect(client.downloadBlob)` reads an unbound method, which lint
 * rejects for a good reason — a method plucked off an object loses its `this`.
 */
function clientWith(options: { readonly failFor?: string } = {}): {
  readonly client: JmapClient;
  readonly downloads: MockInstance<JmapClient["downloadBlob"]>;
} {
  const client = new JmapClient({ username: "u", password: "p" });
  const downloads = vi
    .spyOn(client, "downloadBlob")
    .mockImplementation((_account, blobId) => {
      if (options.failFor !== undefined && blobId === options.failFor) {
        return Promise.reject(new Error("410 gone"));
      }
      return Promise.resolve(new Blob(["From: a@x.com\r\n\r\nhola"]));
    });
  return { client, downloads };
}

beforeEach(() => {
  uploadHarness();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("useForwardAsAttachment", () => {
  it("names each attachment from its subject, with .eml", async () => {
    const user = userEvent.setup();
    render(<Probe client={clientWith().client} emails={[email("m1", "Informe trimestral")]} />);

    await user.click(screen.getByRole("button", { name: "prepare" }));

    expect(screen.getByTestId("names")).toHaveTextContent("Informe trimestral.eml");
  });

  it("uploads as message/rfc822, so the recipient can open it as a message", async () => {
    const user = userEvent.setup();
    render(<Probe client={clientWith().client} emails={[email("m1", "algo")]} />);

    await user.click(screen.getByRole("button", { name: "prepare" }));

    // RFC 2046 §5.2.1 — the type is what makes it a message rather than a file
    // to save, and it is asserted on the attachment the composer will carry.
    expect(screen.getByTestId("types")).toHaveTextContent("message/rfc822");
  });

  it("attaches several messages at once", async () => {
    const user = userEvent.setup();
    render(
      <Probe
        client={clientWith().client}
        emails={[email("m1", "uno"), email("m2", "dos"), email("m3", "tres")]}
      />,
    );

    await user.click(screen.getByRole("button", { name: "prepare" }));

    expect(screen.getByTestId("names")).toHaveTextContent("uno.eml|dos.eml|tres.eml");
    expect(screen.getByTestId("refused")).toHaveTextContent("");
  });

  /*
   * The multi-select promise: the user asked for four things, three are
   * possible, so three happen and the fourth is NAMED. Failing the whole
   * operation, or silently forwarding three, are both worse.
   */
  it("keeps the others when one message cannot be downloaded", async () => {
    const user = userEvent.setup();
    render(
      <Probe
        client={clientWith({ failFor: "b-m2" }).client}
        emails={[email("m1", "uno"), email("m2", "roto"), email("m3", "tres")]}
      />,
    );

    await user.click(screen.getByRole("button", { name: "prepare" }));

    expect(screen.getByTestId("names")).toHaveTextContent("uno.eml|tres.eml");
    expect(screen.getByTestId("refused")).toHaveTextContent("roto.eml");
  });

  it("refuses a message over the per-file cap, by name, without downloading it", async () => {
    const user = userEvent.setup();
    const { client, downloads } = clientWith();
    render(
      <Probe
        client={client}
        emails={[email("m1", "chico", 100), email("m2", "enorme", 5000)]}
        capabilities={{
          "urn:ietf:params:jmap:core": { maxSizeUpload: 1000 },
          "urn:ietf:params:jmap:mail": { maxSizeAttachmentsPerEmail: 10_000 },
        }}
      />,
    );

    await user.click(screen.getByRole("button", { name: "prepare" }));

    expect(screen.getByTestId("names")).toHaveTextContent("chico.eml");
    expect(screen.getByTestId("refused")).toHaveTextContent("enorme.eml");
    // The gate runs on the size the server already told us, so the oversized
    // message is never downloaded — the whole point of a client-side check.
    expect(downloads).toHaveBeenCalledTimes(1);
  });

  it("stops at the per-message total across several messages", async () => {
    const user = userEvent.setup();
    render(
      <Probe
        client={clientWith().client}
        emails={[email("m1", "uno", 400), email("m2", "dos", 400), email("m3", "tres", 400)]}
        capabilities={{
          "urn:ietf:params:jmap:core": { maxSizeUpload: 1000 },
          "urn:ietf:params:jmap:mail": { maxSizeAttachmentsPerEmail: 1000 },
        }}
      />,
    );

    await user.click(screen.getByRole("button", { name: "prepare" }));

    expect(screen.getByTestId("names")).toHaveTextContent("uno.eml|dos.eml");
    expect(screen.getByTestId("refused")).toHaveTextContent("tres.eml");
  });

  it("names a message it has no blob for rather than dropping it", async () => {
    const user = userEvent.setup();
    // A row that came from a projection rather than a fetch: no blob to
    // download. Built by OMITTING the field, which is how the wire delivers it.
    const { blobId: _dropped, ...noBlob } = email("m1", "sin blob");
    render(<Probe client={clientWith().client} emails={[noBlob]} />);

    await user.click(screen.getByRole("button", { name: "prepare" }));

    expect(screen.getByTestId("names")).toHaveTextContent("");
    expect(screen.getByTestId("refused")).toHaveTextContent("sin blob.eml");
  });

  it("does nothing for an empty selection", async () => {
    const user = userEvent.setup();
    const { client, downloads } = clientWith();
    render(<Probe client={client} emails={[]} />);

    await user.click(screen.getByRole("button", { name: "prepare" }));
    await act(async () => {
      await Promise.resolve();
    });

    expect(downloads).not.toHaveBeenCalled();
  });
});
