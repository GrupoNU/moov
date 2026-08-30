/**
 * Offline search over the cache (L3 E9; canon §2.10 — "read, search, and reply").
 *
 * # What this is NOT, and why that is the honest design
 *
 * Online search is PostgreSQL's `tsvector` over the whole account (spike S3):
 * stemming, ranking, the operator language of canon §2.5, five million
 * messages. None of that exists in a browser, and the worst thing this module
 * could do is imitate it well enough that a user believes their search covered
 * their mail. It did not — it covered the cached headers per mailbox plus the
 * bodies they happened to open.
 *
 * So this is deliberately a plain substring matcher with no operator language
 * at all, and the UI labels its results as what they are ("resultados sobre el
 * correo guardado"). A `from:` that silently matched nothing would be worse
 * than a `from:` that is searched for literally, which is what happens here.
 *
 * # Matching rules
 *
 * Case-insensitive, accent-insensitive, AND across whitespace-separated terms.
 * Accent folding matters more in Spanish than the substring choice does:
 * "revision" must find "revisión", because nobody types the accent into a
 * search box. `String.normalize("NFD")` plus stripping the combining range is
 * the standard way to do that without a table, and it is what the server's own
 * `unaccent` does on the other side (E3).
 */

import type { Email } from "../mail/types";

/** The Unicode combining diacritical marks block, U+0300–U+036F. */
const COMBINING_MARKS = /[̀-ͯ]/g;

/** Folds case and accents so "Revisión" and "revision" are the same string. */
export function foldText(value: string): string {
  return value
    .normalize("NFD")
    // Removing the combining marks turns "ó" into "o" and leaves every
    // non-Latin script untouched.
    .replace(COMBINING_MARKS, "")
    .toLowerCase();
}

/** Splits a query into terms, dropping empties. */
export function queryTerms(query: string): readonly string[] {
  return foldText(query)
    .split(/\s+/)
    .filter((term) => term !== "");
}

/**
 * The text of one message that search looks at.
 *
 * Subject, every address in From/To/Cc (both display name and address — a user
 * searching "ada" means the person, and their address may be `al@…`), the
 * preview, and the plain-text body values when the body is cached. Headers
 * beyond those are not searched: they are not in the cached row, and inventing
 * a match on something the user cannot see is how a search result becomes
 * inexplicable.
 */
export function searchableText(email: Email): string {
  const parts: string[] = [];

  if (email.subject !== undefined && email.subject !== null) parts.push(email.subject);
  for (const list of [email.from, email.to, email.cc]) {
    for (const address of list ?? []) {
      if (address.name !== null) parts.push(address.name);
      parts.push(address.email);
    }
  }
  if (email.preview !== undefined) parts.push(email.preview);

  /*
   * Body values are present only for messages the user opened (cache.ts caches
   * bodies on open). That asymmetry is real and is exactly why the UI says the
   * results are "over saved mail" — a message whose body was never fetched can
   * only match on its header fields.
   */
  for (const value of Object.values(email.bodyValues ?? {})) {
    parts.push(value.value);
  }

  return foldText(parts.join("\n"));
}

/** True when every term appears somewhere in the message. */
export function matchesQuery(email: Email, terms: readonly string[]): boolean {
  if (terms.length === 0) return false;
  const haystack = searchableText(email);
  return terms.every((term) => haystack.includes(term));
}

/**
 * Runs an offline search.
 *
 * `bodies` is merged over `headers` by id, so a message whose body is cached is
 * searched with its body and a message whose body is not is still searched by
 * its header fields — rather than the two lists producing duplicate rows, which
 * is what a naive concatenation gives.
 *
 * Results are newest first, which is what every mail list in this app orders
 * by. There is no relevance ranking: with a substring matcher, a "relevance"
 * score would be an invention, and canon's own ranking work (S3) lives on the
 * server.
 */
export function searchOffline(
  query: string,
  headers: readonly Email[],
  bodies: readonly Email[] = [],
): readonly Email[] {
  const terms = queryTerms(query);
  if (terms.length === 0) return [];

  const merged = new Map<string, Email>();
  for (const email of headers) merged.set(email.id, email);
  for (const email of bodies) {
    const header = merged.get(email.id);
    // The body row wins on body fields; a body row is a superset of the
    // properties a header row carries.
    merged.set(email.id, header === undefined ? email : { ...header, ...email });
  }

  return [...merged.values()]
    .filter((email) => matchesQuery(email, terms))
    .sort((a, b) => (b.receivedAt ?? "").localeCompare(a.receivedAt ?? ""));
}
