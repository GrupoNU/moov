/**
 * The `mailto:` protocol handler (E9, decision D-1).
 *
 * # What the OS actually hands us
 *
 * A registered `protocol_handlers` entry does not deliver a `mailto:` URI to
 * the app directly. The browser percent-encodes the WHOLE URI and substitutes
 * it into the registered URL template's `%s`, then navigates to the result. So
 * clicking `mailto:ana@example.com?subject=Hola` on a page opens:
 *
 *   /mail/inbox?compose=mailto%3Aana%40example.com%3Fsubject%3DHola
 *
 * The app therefore sees an ordinary navigation with a query parameter, which
 * is the whole reason this is testable as a pure function: there is no browser
 * API to mock, only a URL to parse.
 *
 * # Why the composer opens instead of the OS handing it back
 *
 * Same reasoning as the unsubscribe path in `mail/unsubscribe.ts`: the user
 * sees exactly what is about to leave their address, from the account they are
 * signed into, and can cancel. That module already solved the hard half —
 * `mailto:` is an opaque-path URL, so `URL.searchParams` is empty for it and
 * the address lives percent-encoded in `pathname` — so this reuses
 * {@link parseMailtoUri} rather than growing a second parser that would drift.
 *
 * # What this module deliberately does NOT support
 *
 * `cc`, `bcc` and multiple comma-separated recipients are in RFC 6068 and are
 * NOT handled here. `parseMailtoUri` returns one recipient plus subject and
 * body, which is what the overwhelming majority of real `mailto:` links carry.
 * Extending it is a change to that shared parser with its own tests, not a
 * divergent copy in this file — and shipping the common case correctly beats
 * shipping the whole RFC with two parsers that disagree.
 */

import { parseMailtoUri, type UnsubscribeMailto } from "../mail/unsubscribe";

/** The query parameter the manifest's `protocol_handlers` template writes. */
export const COMPOSE_PARAM = "compose";

/** A composition requested by the OS, already validated. */
export interface ComposeRequest {
  readonly to: string;
  readonly subject: string | undefined;
  readonly body: string | undefined;
}

/**
 * Reads a compose request out of a URL's query string.
 *
 * Returns `undefined` for every URL that does not carry a usable one —
 * no parameter, an empty parameter, a non-`mailto:` scheme, or a `mailto:`
 * with no recipient. That total behaviour matters: this runs on EVERY
 * navigation, and a parser that threw would turn a malformed link someone
 * shared into a blank app.
 *
 * Refusing a non-`mailto:` scheme is the security-relevant half. This value
 * comes from outside the app — anyone can send a link to
 * `/mail/inbox?compose=javascript:...` — and it must never reach anything that
 * treats it as a URL to follow. Only `mailto:` is accepted, and only its
 * decomposed parts (an address, two strings) leave this module.
 */
export function parseComposeRequest(url: string): ComposeRequest | undefined {
  let parsed: URL;
  try {
    // A relative URL needs a base to parse; the base is discarded immediately.
    parsed = new URL(url, "http://localhost");
  } catch {
    return undefined;
  }

  const raw = parsed.searchParams.get(COMPOSE_PARAM);
  if (raw === null || raw === "") return undefined;

  // `searchParams` has already percent-DECODED one layer, which is exactly the
  // layer the browser added when it substituted into `%s`. What is left is the
  // original `mailto:` URI, whose own encoding parseMailtoUri handles.
  const mailto: UnsubscribeMailto | undefined = parseMailtoUri(raw);
  if (mailto === undefined) return undefined;

  return { to: mailto.to, subject: mailto.subject, body: mailto.body };
}

/**
 * The same URL with the compose parameter removed.
 *
 * The app replaces its URL with this once the composer is open, for two
 * reasons. A reload must not re-open a composer the user already dismissed —
 * that is the "why is this dialog immortal" bug. And the parameter carries a
 * correspondent's address, which does not belong in a URL that stays in the
 * address bar, the history, and any screenshot of either.
 *
 * Every other parameter and the path are preserved, so this is safe to call
 * unconditionally.
 */
export function urlWithoutCompose(url: string): string {
  let parsed: URL;
  try {
    parsed = new URL(url, "http://localhost");
  } catch {
    return url;
  }
  parsed.searchParams.delete(COMPOSE_PARAM);
  const query = parsed.searchParams.toString();
  return `${parsed.pathname}${query === "" ? "" : `?${query}`}`;
}
