import type { EmailBodyPart } from "./types";

/**
 * The raster types a card shows a THUMBNAIL of (C-10).
 *
 * The same closed set the HTML policy accepts as `data:` images: formats with
 * no script grammar at all. SVG is deliberately out — an `<img>` would neuter
 * its scripts, but a thumbnail is a convenience and the policy stays simple.
 */
const THUMBNAIL_TYPES: ReadonlySet<string> = new Set([
  "image/png",
  "image/jpeg",
  "image/jpg",
  "image/gif",
  "image/webp",
]);

/** A thumbnail is not worth buffering a photo library for: above this the
 * card shows the icon and the download still works. */
const MAX_THUMBNAIL_BYTES = 8 * 1024 * 1024;

/** True when a part gets a thumbnail rather than a file icon. */
export function isThumbnailable(part: EmailBodyPart): boolean {
  return (
    part.blobId !== null &&
    THUMBNAIL_TYPES.has(part.type.toLowerCase()) &&
    part.size <= MAX_THUMBNAIL_BYTES
  );
}
