/**
 * `List-Unsubscribe` parsing (E2 item 6, client half — canon §2.2).
 *
 * # What the header actually looks like in the wild
 *
 * RFC 2369 defines it as a comma-separated list of URIs, each in angle
 * brackets, optionally with RFC 2822 comments between them:
 *
 *   List-Unsubscribe: <mailto:x@y.z?subject=unsubscribe>, <https://y.z/u?k=1>
 *   List-Unsubscribe: <https://y.z/u> (Click to unsubscribe)
 *
 * Real senders violate every part of that. This parser is therefore written
 * against the mess, not the grammar: it takes what is inside angle brackets,
 * ignores everything else on the line, and refuses any scheme it does not
 * recognise. Refusing is the security-relevant half — a `javascript:` URI in a
 * header that the UI turns into a clickable control is a live XSS, and the
 * allowlist below is what makes that unreachable rather than merely unlikely.
 *
 * # Why `mailto:` wins when both are present
 *
 * Gmail prefers the mail path, and so do we, for a reason that is about the
 * user rather than about protocol taste: an HTTP unsubscribe is a page visit
 * that confirms the address is live and read, to a party the user is trying to
 * stop hearing from. A `mailto:` goes out from their own account with their own
 * client, shows them exactly what is being sent, and is cancellable.
 *
 * # RFC 8058 one-click
 *
 * `List-Unsubscribe-Post: List-Unsubscribe=One-Click` promises that a bare
 * POST to the HTTPS URI unsubscribes with no further interaction. That POST
 * cannot be made from this app: it is cross-origin, unauthenticated, and would
 * be blocked (and, if it were not, would leak the user's IP to the sender).
 * It belongs on the server, and {@link UnsubscribeInfo.oneClick} carries the
 * fact forward so the later epic has it without re-parsing.
 */

import type { Email, EmailHeader } from "./types";

/** The schemes a parsed URI may use. Everything else is discarded. */
const ALLOWED_SCHEMES: ReadonlySet<string> = new Set(["mailto:", "https:", "http:"]);

/** What the header offered, already validated. */
export interface UnsubscribeInfo {
  /** The `mailto:` URI, when one was offered and parsed. */
  readonly mailto: UnsubscribeMailto | undefined;
  /** The http(s) URI, when one was offered. */
  readonly url: string | undefined;
  /**
   * True when the sender advertised RFC 8058 one-click.
   *
   * TODO(E-server): the one-click POST is server work — the browser cannot
   * make it (cross-origin, and it would leak the reader's IP). This flag is
   * the client's half: it is parsed, carried, and today only informs the UI
   * that the http path is a true one-click rather than a landing page.
   */
  readonly oneClick: boolean;
}

/** A `mailto:` unsubscribe, split into what a composer needs. */
export interface UnsubscribeMailto {
  readonly to: string;
  /** From the URI's `?subject=`, when it carried one. */
  readonly subject: string | undefined;
  /** From the URI's `?body=`, when it carried one. */
  readonly body: string | undefined;
}

/** Case-insensitive header lookup, returning the first match. */
export function headerValue(
  headers: readonly EmailHeader[] | undefined,
  name: string,
): string | undefined {
  if (headers === undefined) return undefined;
  const wanted = name.toLowerCase();
  for (const header of headers) {
    if (header.name.toLowerCase() === wanted) return header.value.trim();
  }
  return undefined;
}

/**
 * Extracts the `<...>` URIs from a `List-Unsubscribe` value.
 *
 * Anything outside the brackets — RFC 2822 comments, stray commas, the
 * whitespace senders fold the header with — is ignored rather than parsed.
 * That is the robust reading: the brackets are the one part every sender gets
 * right, because without them the header does not work anywhere.
 */
export function parseUnsubscribeUris(value: string | undefined): readonly string[] {
  if (value === undefined || value === "") return [];
  const out: string[] = [];
  const pattern = /<([^<>]+)>/g;
  let match = pattern.exec(value);
  while (match !== null) {
    // Folded headers arrive with embedded newlines and their continuation
    // whitespace; a URI never legally contains either, so stripping all
    // whitespace inside the brackets un-folds without corrupting anything.
    const uri = (match[1] ?? "").replace(/\s+/g, "");
    if (uri !== "" && hasAllowedScheme(uri)) out.push(uri);
    match = pattern.exec(value);
  }
  return out;
}

function hasAllowedScheme(uri: string): boolean {
  const lower = uri.toLowerCase();
  for (const scheme of ALLOWED_SCHEMES) {
    if (lower.startsWith(scheme)) return true;
  }
  return false;
}

/**
 * Splits a `mailto:` URI into recipient and prefilled fields.
 *
 * Hand-rolled rather than `new URL()`: `mailto:` is an opaque-path URL, so
 * `URL.searchParams` is empty for it in every engine, and the address lives in
 * `pathname` percent-encoded. Doing it explicitly is both correct and shorter
 * than working around the standard API.
 */
export function parseMailtoUri(uri: string): UnsubscribeMailto | undefined {
  if (!uri.toLowerCase().startsWith("mailto:")) return undefined;
  const rest = uri.slice("mailto:".length);
  const queryAt = rest.indexOf("?");
  const rawTo = queryAt < 0 ? rest : rest.slice(0, queryAt);
  const to = safeDecode(rawTo);
  // A mailto with no recipient is unusable and, worse, would open a composer
  // addressed to nobody that the user might send into the void.
  if (to === "" || !to.includes("@")) return undefined;

  let subject: string | undefined;
  let body: string | undefined;
  if (queryAt >= 0) {
    for (const pair of rest.slice(queryAt + 1).split("&")) {
      const equals = pair.indexOf("=");
      if (equals < 0) continue;
      const key = pair.slice(0, equals).toLowerCase();
      const value = safeDecode(pair.slice(equals + 1).replace(/\+/g, " "));
      if (key === "subject" && subject === undefined) subject = value;
      else if (key === "body" && body === undefined) body = value;
    }
  }

  return {
    to,
    ...(subject !== undefined && subject !== "" ? { subject } : { subject: undefined }),
    ...(body !== undefined && body !== "" ? { body } : { body: undefined }),
  };
}

/** `decodeURIComponent` that returns the input rather than throwing on `%zz`. */
function safeDecode(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

/**
 * Reads everything the unsubscribe control needs out of a message's headers.
 *
 * Returns `undefined` when the message offers nothing usable, which is the
 * signal for "render no button at all" — a disabled or dead Unsubscribe is
 * worse than none, because it teaches the user the feature does not work.
 */
export function unsubscribeInfo(email: Email): UnsubscribeInfo | undefined {
  const uris = parseUnsubscribeUris(headerValue(email.headers, "List-Unsubscribe"));
  if (uris.length === 0) return undefined;

  let mailto: UnsubscribeMailto | undefined;
  let url: string | undefined;
  for (const uri of uris) {
    const lower = uri.toLowerCase();
    if (lower.startsWith("mailto:")) {
      mailto ??= parseMailtoUri(uri);
    } else {
      url ??= uri;
    }
  }
  if (mailto === undefined && url === undefined) return undefined;

  const post = headerValue(email.headers, "List-Unsubscribe-Post");
  const oneClick = post?.toLowerCase().includes("list-unsubscribe=one-click") === true;

  return { mailto, url, oneClick };
}

/**
 * The list's own name, for the "you are unsubscribing from X" line.
 *
 * Canon's list-ID fallback: `List-ID` is `Some Name <list.example.com>`, and
 * the human half is optional. When it is absent the bracketed identifier is
 * shown instead — it is at least a stable name the user may recognise, which
 * is strictly better than the sender address of a no-reply robot.
 */
export function listIdLabel(email: Email): string | undefined {
  const raw = headerValue(email.headers, "List-ID");
  if (raw === undefined || raw === "") return undefined;
  const bracket = /<([^<>]+)>/.exec(raw);
  const name = raw.slice(0, bracket?.index ?? raw.length).trim().replace(/^"|"$/g, "").trim();
  if (name !== "") return name;
  return bracket?.[1] ?? undefined;
}
