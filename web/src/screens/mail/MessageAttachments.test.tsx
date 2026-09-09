import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";

import { JmapClient } from "../../api/jmap";
import { I18nProvider } from "../../i18n/I18nProvider";
import { isThumbnailable } from "../../mail/attachmentThumbnail";
import type { EmailBodyPart } from "../../mail/types";
import { AttachmentList } from "./MessageAttachments";

/**
 * Attachment cards (C-10): a thumbnail for an image through the authenticated
 * blob path, an icon for everything else, and the object URL's lifetime —
 * created once the bytes land, revoked when the card goes.
 *
 * jsdom has no `URL.createObjectURL`; both halves are stubbed so the test can
 * see the pairing, which is the thing that matters: an object URL that is
 * never revoked is a message that never leaves memory.
 */

function part(overrides: Partial<EmailBodyPart>): EmailBodyPart {
  return {
    partId: "2",
    blobId: "b-2",
    size: 1024,
    name: "photo.png",
    type: "image/png",
    charset: null,
    disposition: "attachment",
    cid: null,
    language: null,
    location: null,
    ...overrides,
  };
}

const created: string[] = [];
const revoked: string[] = [];

/*
 * Installed on the real URL constructor (not a replacement object: `new URL()`
 * is used all over the client) and left in place for the whole file — the
 * revocation happens in React's unmount, which Testing Library runs in ITS
 * afterEach, and a stub torn down before that would make the cleanup throw.
 */
beforeAll(() => {
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    value: (blob: Blob) => {
      const url = `blob:mock/${created.length}-${blob.size}`;
      created.push(url);
      return url;
    },
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    configurable: true,
    value: (url: string) => {
      revoked.push(url);
    },
  });
});

afterAll(() => {
  delete (URL as { createObjectURL?: unknown }).createObjectURL;
  delete (URL as { revokeObjectURL?: unknown }).revokeObjectURL;
});

beforeEach(() => {
  created.length = 0;
  revoked.length = 0;
});

function renderList(parts: readonly EmailBodyPart[], client: JmapClient, blobToken?: string) {
  return render(
    <I18nProvider locale="es">
      <AttachmentList
        attachments={parts}
        client={client}
        accountId="a"
        {...(blobToken === undefined ? {} : { blobToken })}
      />
    </I18nProvider>,
  );
}

describe("what gets a thumbnail", () => {
  it("rasters with a blobId under the cap; nothing else", () => {
    expect(isThumbnailable(part({}))).toBe(true);
    expect(isThumbnailable(part({ type: "image/jpeg" }))).toBe(true);
    expect(isThumbnailable(part({ type: "IMAGE/PNG" }))).toBe(true);
    // No script grammar allowed in, even for a convenience.
    expect(isThumbnailable(part({ type: "image/svg+xml" }))).toBe(false);
    expect(isThumbnailable(part({ type: "application/pdf" }))).toBe(false);
    expect(isThumbnailable(part({ blobId: null }))).toBe(false);
    expect(isThumbnailable(part({ size: 9 * 1024 * 1024 }))).toBe(false);
  });
});

describe("the image card", () => {
  it("fetches the bytes with the authenticated client and shows them as an object URL", async () => {
    const client = new JmapClient({ username: "u", password: "p" });
    const download = vi
      .spyOn(client, "downloadBlob")
      .mockResolvedValue(new Blob([new Uint8Array(16)], { type: "image/png" }));

    renderList([part({})], client, "tok");

    const image = await screen.findByRole("presentation");
    expect(image).toHaveAttribute("src", created[0]);
    expect(download).toHaveBeenCalledWith("a", "b-2", "photo.png", "image/png", expect.anything());
    // The download itself is still a real link, token attached.
    const link = screen.getByRole("link", { name: /photo\.png/ });
    expect(link.getAttribute("href")).toContain("access_token=tok");
    expect(link).toHaveAttribute("download", "photo.png");
  });

  it("revokes the object URL when the card unmounts", async () => {
    const client = new JmapClient({ username: "u", password: "p" });
    vi.spyOn(client, "downloadBlob").mockResolvedValue(new Blob([new Uint8Array(4)]));
    const { unmount } = renderList([part({})], client);
    await screen.findByRole("presentation");
    expect(revoked).toEqual([]);

    unmount();
    expect(revoked).toEqual(created);
  });

  it("falls back to the icon, quietly, when the fetch fails", async () => {
    const client = new JmapClient({ username: "u", password: "p" });
    vi.spyOn(client, "downloadBlob").mockRejectedValue(new Error("nope"));
    renderList([part({})], client);
    await waitFor(() => {
      expect(document.querySelector('[data-thumbnail-state="failed"]')).not.toBeNull();
    });
    expect(screen.queryByRole("presentation")).not.toBeInTheDocument();
    // The name and size are still there: the card degraded, it did not vanish.
    expect(screen.getByText("photo.png")).toBeInTheDocument();
  });
});

describe("the file card", () => {
  it("shows an icon, name and size for a non-image, and fetches nothing", () => {
    const client = new JmapClient({ username: "u", password: "p" });
    const download = vi.spyOn(client, "downloadBlob");
    renderList([part({ type: "application/pdf", name: "contrato.pdf", size: 204800 })], client, "tok");
    expect(screen.getByText("contrato.pdf")).toBeInTheDocument();
    expect(screen.getByText(/200/)).toBeInTheDocument();
    expect(download).not.toHaveBeenCalled();
    expect(screen.queryByRole("presentation")).not.toBeInTheDocument();
  });
});
