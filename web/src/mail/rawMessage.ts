/**
 * Reading the raw RFC 822 message (E2 item 3 — Gmail's "Show original").
 *
 * The whole job is one rule from RFC 5322 §2.1: the header section ends at the
 * FIRST empty line, and everything after it is the body. Getting that boundary
 * right is the difference between showing headers and showing the first
 * kilobyte of a base64 attachment.
 *
 * Two details that are easy to get wrong and are therefore pinned by tests:
 *
 *   - the separator is CRLF CRLF on the wire, but a message that has been
 *     through anything at all may carry bare LFs. Both are accepted.
 *   - a message with NO body (headers only, which happens with bounces) has no
 *     blank line at all. The whole string is the header section, not nothing.
 */

/** The header section of a raw message: everything before the first blank line. */
export function headerSection(raw: string): string {
  // Normalise CRLF first so one index search finds either form. This copies
  // the string once; the alternative — searching for both separators and
  // taking the smaller index — is two searches and an off-by-one waiting to
  // happen when one form is absent.
  const normalised = raw.replace(/\r\n/g, "\n");
  const blank = normalised.indexOf("\n\n");
  if (blank < 0) return normalised.trimEnd();
  return normalised.slice(0, blank).trimEnd();
}

/**
 * Unfolds a header section for display.
 *
 * RFC 5322 §2.2.3 lets a long header value continue on the next line if that
 * line starts with whitespace. Displayed as-is, a folded `References` becomes
 * twenty lines that look like twenty headers. Unfolding joins them back, which
 * is what "show original" in every client displays, and keeps one header per
 * visual line.
 */
export function unfoldHeaders(headers: string): string {
  return headers.replace(/\n[ \t]+/g, " ");
}

/** The maximum bytes of raw message worth fetching to show its headers. */
export const HEADER_FETCH_LIMIT = 128 * 1024;
