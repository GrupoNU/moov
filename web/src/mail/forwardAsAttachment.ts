/**
 * Forward as attachment (L3 E7; canon §2.3, /mail/answer/9337672).
 *
 * # What this is for, and why it is not the same as a forward
 *
 * An ordinary forward quotes the original into a new message's body: the
 * recipient reads the text, but the original's headers, its exact MIME
 * structure and its DKIM signature are gone. Forwarding AS AN ATTACHMENT sends
 * the message itself as a `message/rfc822` part, so the recipient can open it
 * as a message — with its true From, its Received chain and its signature
 * intact.
 *
 * That is why it is the form people use to report phishing to a security team,
 * to escalate a thread to a lawyer, or to hand a bounce to an administrator.
 * The quoted-body forward is the one that loses exactly the evidence those
 * three cases need.
 *
 * # Why the download must go through the authenticated fetch
 *
 * The bytes come from the same route `DownloadOriginalButton` uses, and for the
 * same reason it cannot be a plain `<a download>`: the download route requires
 * HTTP Basic, and a browser navigation sends no `Authorization` header — the
 * live pilot answers 401 with `WWW-Authenticate: Basic`, which pops the
 * browser's own credential dialog at the user. So the blob is fetched with the
 * header attached and re-uploaded as an attachment.
 *
 * # The size gate is the client's courtesy, not the rule
 *
 * The server enforces `maxSizeUpload` and `maxSizeAttachmentsPerEmail`
 * regardless (J1's "declared == applied"). Checking here saves the user
 * uploading twenty megabytes before being told no, and — the case that matters
 * for multi-select — lets a batch attach the messages that FIT and report the
 * ones that did not, rather than failing the whole operation because the
 * fourth message was large.
 */

import { displaySubject } from "./threading";

/**
 * Builds the `.eml` filename for a message.
 *
 * Sanitised the same way `DownloadOriginalButton` already does — this is that
 * rule, extracted so the download and the attachment cannot drift apart on it,
 * and hardened for the two cases an attachment name faces that a download name
 * does not.
 *
 * The rules, each earning its place:
 *
 *   - **only letters, numbers, space, dot, underscore and hyphen survive.**
 *     A filename crosses into a MIME header, a `Content-Disposition` parameter
 *     and then the recipient's filesystem; quotes, semicolons and control
 *     characters are how a name breaks a header or escapes a directory. The
 *     allowlist is narrow on purpose — `\p{L}` keeps accents and non-Latin
 *     scripts, so it is not an ASCII-only rule.
 *   - **60 characters**, because a subject can be a paragraph and a filename
 *     should still be readable in an attachment row.
 *   - **a blank result becomes `message`**, never an empty name or a bare
 *     `.eml`: a message with no subject, or one whose subject is entirely
 *     emoji, must still produce a file the recipient can save.
 */
export function emlFilename(subject: string | null | undefined): string {
  const cleaned = (displaySubject(subject) ?? "")
    .replace(/[^\p{L}\p{N} ._-]/gu, "")
    // Collapse the runs of spaces the stripping leaves behind, so a subject
    // full of punctuation does not become "a    b    c".
    .replace(/\s+/gu, " ")
    .slice(0, 60)
    .trim()
    // A leading dot would make the file hidden on Unix, and a trailing one is
    // stripped by Windows — either way the name is not what we wrote.
    .replace(/^\.+/u, "")
    .replace(/\.+$/u, "")
    .trim();

  return `${cleaned === "" ? "message" : cleaned}.eml`;
}

/** The MIME type of a forwarded message, per RFC 2046 §5.2.1. */
export const RFC822_TYPE = "message/rfc822";

/**
 * Decides which messages fit under the advertised caps.
 *
 * Pure, so the "which ones were too big" arithmetic is testable without a
 * network. `alreadyAttached` is the bytes the composer is already carrying,
 * because the per-email cap applies to the total and a forward-as-attachment
 * into a composer that already holds a PDF has less room than an empty one.
 *
 * Returns both halves. The refused list is not an error to swallow: the user
 * asked for N messages and must be told which of them did not make it, by name.
 */
export function partitionBySize<T extends { readonly size: number }>(
  candidates: readonly T[],
  limits: {
    /** Per-blob cap (`maxSizeUpload`), or undefined when unadvertised. */
    readonly perFile?: number | undefined;
    /** Per-message total (`maxSizeAttachmentsPerEmail`). */
    readonly total?: number | undefined;
    readonly alreadyAttached?: number;
  },
): { readonly accepted: readonly T[]; readonly refused: readonly T[] } {
  const accepted: T[] = [];
  const refused: T[] = [];
  let running = limits.alreadyAttached ?? 0;

  for (const candidate of candidates) {
    if (limits.perFile !== undefined && candidate.size > limits.perFile) {
      refused.push(candidate);
      continue;
    }
    if (limits.total !== undefined && running + candidate.size > limits.total) {
      refused.push(candidate);
      continue;
    }
    running += candidate.size;
    accepted.push(candidate);
  }

  return { accepted, refused };
}
