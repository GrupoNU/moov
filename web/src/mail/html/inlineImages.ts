/**
 * Inline (`cid:`) images, resolved in the PARENT (C-11).
 *
 * # Why not point the frame at the download route
 *
 * The frame's CSP is `img-src data: <origin>/jmap/imgproxy` (srcdoc.ts).
 * `/jmap/download/…` is not in it, the route wants HTTP Basic or a query
 * token, and the sandboxed document — opaque origin, no script — can carry
 * neither. Widening `img-src` to the download route AND writing the
 * account-bound blob token into the srcdoc would hand a sanitizer bypass a
 * token to exfiltrate. That is a posture change decision 4 does not permit.
 *
 * # What works without changing a byte of the posture
 *
 * `data:` is ALREADY admitted. The parent fetches each referenced part through
 * the same authenticated `downloadBlob` the attachment cards use, encodes it
 * as `data:image/<raster>;base64,…`, and hands the sanitizer a cid → URL map;
 * the sanitizer accepts a value only if it classifies as a raster data: image.
 * No directive widens, no token enters the document, and the bytes are the
 * message's own — there is nothing to track.
 *
 * # The caps, and what happens past them
 *
 * A data: URL rides inside the srcdoc string, so an inline image costs its
 * size ×4/3 in the document. A signature logo is kilobytes; a scanned
 * contract pasted inline can be many megabytes. Above the per-image cap or
 * once the per-message budget is spent, the part is left unresolved and the
 * existing "N embedded images cannot be displayed" notice says so — the
 * attachment card below still offers the file.
 */

import type { EmailBodyPart } from "../types";
import { normalizeCid } from "./sanitize";

/** The raster types accepted — the same closed set as the `data:` policy. */
const INLINE_TYPES: ReadonlySet<string> = new Set([
  "image/png",
  "image/jpeg",
  "image/jpg",
  "image/gif",
  "image/webp",
]);

/** The largest single inline image inlined into the document. */
export const MAX_INLINE_IMAGE_BYTES = 2 * 1024 * 1024;

/** The per-message budget across all inline images. */
export const MAX_INLINE_TOTAL_BYTES = 6 * 1024 * 1024;

/** True for a part that can be inlined at all: a raster with a cid and a blob. */
export function isInlineCandidate(part: EmailBodyPart): boolean {
  return (
    part.cid !== null &&
    part.cid.trim() !== "" &&
    part.blobId !== null &&
    INLINE_TYPES.has(part.type.toLowerCase()) &&
    part.size <= MAX_INLINE_IMAGE_BYTES
  );
}

/**
 * The parts to fetch for a set of referenced cids, in reference order, under
 * the budget. Exact match first, case-insensitive second (senders disagree
 * with RFC 2392 about case, and a missing logo is the worse outcome).
 */
export function partsToInline(
  referencedCids: readonly string[],
  parts: readonly EmailBodyPart[],
): readonly { readonly cid: string; readonly part: EmailBodyPart }[] {
  const candidates = parts.filter(isInlineCandidate);
  const exact = new Map<string, EmailBodyPart>();
  const folded = new Map<string, EmailBodyPart>();
  for (const part of candidates) {
    const id = normalizeCid(part.cid ?? "");
    if (!exact.has(id)) exact.set(id, part);
    const lower = id.toLowerCase();
    if (!folded.has(lower)) folded.set(lower, part);
  }
  const chosen: { cid: string; part: EmailBodyPart }[] = [];
  let budget = MAX_INLINE_TOTAL_BYTES;
  for (const cid of referencedCids) {
    const part = exact.get(cid) ?? folded.get(cid.toLowerCase());
    if (part === undefined) continue;
    if (part.size > budget) continue;
    budget -= part.size;
    chosen.push({ cid, part });
  }
  return chosen;
}

/** The mime type the data: URL declares — the PART's, lower-cased, never the
 * fetched Blob's (a server may answer octet-stream for a download). */
export function inlineMimeType(part: EmailBodyPart): string {
  const type = part.type.toLowerCase();
  return type === "image/jpg" ? "image/jpeg" : type;
}

/** Encodes bytes as a `data:` URL, in chunks so a large image cannot blow
 * the argument list of `String.fromCharCode`. */
export function toDataUrl(mimeType: string, bytes: Uint8Array): string {
  let binary = "";
  const chunk = 0x8000;
  for (let offset = 0; offset < bytes.length; offset += chunk) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunk));
  }
  return `data:${mimeType};base64,${btoa(binary)}`;
}

/**
 * The bytes of a Blob. `Blob.arrayBuffer()` where the platform has it, and
 * `FileReader` where it does not (older webviews; jsdom) — the fallback
 * exists so the test environment exercises the same path as the browser.
 */
export function readBlobBytes(blob: Blob): Promise<Uint8Array> {
  const withBuffer = blob as Blob & { arrayBuffer?: () => Promise<ArrayBuffer> };
  if (typeof withBuffer.arrayBuffer === "function") {
    return withBuffer.arrayBuffer().then((buffer) => new Uint8Array(buffer));
  }
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      resolve(new Uint8Array(reader.result as ArrayBuffer));
    };
    reader.onerror = () => {
      reject(reader.error ?? new Error("blob read failed"));
    };
    reader.readAsArrayBuffer(blob);
  });
}
