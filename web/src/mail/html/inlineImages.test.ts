import { describe, expect, it } from "vitest";

import type { EmailBodyPart } from "../types";
import {
  MAX_INLINE_IMAGE_BYTES,
  MAX_INLINE_TOTAL_BYTES,
  inlineMimeType,
  isInlineCandidate,
  partsToInline,
  toDataUrl,
} from "./inlineImages";

function part(overrides: Partial<EmailBodyPart>): EmailBodyPart {
  return {
    partId: "3",
    blobId: "b-3",
    size: 4096,
    name: "logo.png",
    type: "image/png",
    charset: null,
    disposition: "inline",
    cid: "logo@x",
    language: null,
    location: null,
    ...overrides,
  };
}

describe("inline candidates", () => {
  it("wants a cid, a blob and a raster under the cap", () => {
    expect(isInlineCandidate(part({}))).toBe(true);
    expect(isInlineCandidate(part({ cid: null }))).toBe(false);
    expect(isInlineCandidate(part({ blobId: null }))).toBe(false);
    expect(isInlineCandidate(part({ type: "image/svg+xml" }))).toBe(false);
    expect(isInlineCandidate(part({ type: "application/pdf" }))).toBe(false);
    expect(isInlineCandidate(part({ size: MAX_INLINE_IMAGE_BYTES + 1 }))).toBe(false);
  });
});

describe("choosing what to fetch", () => {
  it("matches referenced cids to parts, exact first, then case-folded", () => {
    const parts = [part({ cid: "<logo@x>" }), part({ partId: "4", blobId: "b-4", cid: "Banner@X" })];
    const chosen = partsToInline(["logo@x", "banner@x", "missing@x"], parts);
    expect(chosen.map((c) => c.cid)).toEqual(["logo@x", "banner@x"]);
    expect(chosen.map((c) => c.part.blobId)).toEqual(["b-3", "b-4"]);
  });

  it("fetches only what the body references — an unreferenced part costs nothing", () => {
    const chosen = partsToInline([], [part({})]);
    expect(chosen).toEqual([]);
  });

  it("stops at the per-message budget and leaves the rest unresolved", () => {
    const big = MAX_INLINE_IMAGE_BYTES;
    const parts = Array.from({ length: 5 }, (_, i) =>
      part({ partId: String(i), blobId: `b-${i}`, cid: `img${i}@x`, size: big }),
    );
    const chosen = partsToInline(parts.map((p) => p.cid ?? ""), parts);
    expect(chosen.length).toBe(Math.floor(MAX_INLINE_TOTAL_BYTES / big));
  });
});

describe("encoding", () => {
  it("declares the PART's raster type, never the blob's", () => {
    expect(inlineMimeType(part({ type: "IMAGE/JPG" }))).toBe("image/jpeg");
    expect(inlineMimeType(part({ type: "image/webp" }))).toBe("image/webp");
  });

  it("produces a data: URL the policy classifies as a raster image", () => {
    const url = toDataUrl("image/png", new Uint8Array([137, 80, 78, 71]));
    expect(url).toBe("data:image/png;base64,iVBORw==");
  });

  it("encodes past the chunk boundary without dropping bytes", () => {
    const bytes = new Uint8Array(0x8000 * 2 + 7).map((_, i) => i % 251);
    const url = toDataUrl("image/png", bytes);
    const decoded = Uint8Array.from(atob(url.slice(url.indexOf(",") + 1)), (c) => c.charCodeAt(0));
    expect(decoded).toEqual(bytes);
  });
});
