/**
 * The preview line's client-side cleanup (B-08).
 *
 * # The defect
 *
 * The side-by-side review measured Moov's preview line running roughly 500px
 * longer than Gmail's, and found the extra length was not content: it was
 * tracking URLs. Marketing mail opens with them, and the server's `preview`
 * property is the first N characters of the body text, so a row that should
 * read "Revísalo para evitar interrupciones" reads
 * "Revísalo para evitar interrupciones donweb ( https://donweb.com?utm_campaign=
 * Aviso%2520de%2520vencimiento%2520del%2520servi&utm_content=…" and the actual
 * sentence is pushed off the row.
 *
 * Gmail's preview line does not show URLs. This is that rule, applied on the
 * CLIENT.
 *
 * # Why the client and not the server
 *
 * The server's `preview` is what the store holds and what every other consumer
 * reads — the offline cache, the search snippet's fallback, a future API
 * client. Stripping there would mean the stored preview and the message body
 * disagree about what the message says, and it would be irreversible: a preview
 * is computed once at sync time and never recomputed. Doing it at render keeps
 * the store honest and keeps this reversible by a redeploy.
 *
 * It is also the layer that can afford it. This runs over at most one page of
 * rows, on strings already capped at preview length.
 *
 * # What it does NOT touch
 *
 * The SEARCH snippet. A search for "donweb.com" must show the user why the row
 * matched, and a match inside a URL is a legitimate answer; `SnippetText`
 * renders the server's highlighted string and this function is not on that
 * path. The rule is about idle browsing, exactly as the `showSnippets`
 * preference is.
 */

/**
 * How long a preview may be before it is cut, in characters.
 *
 * The row clips with an ellipsis at whatever width it has, so this is not what
 * makes the line fit — CSS does that. It is a bound on the WORK: the regex pass
 * below is linear, but a pathological preview (a base64 blob pasted into a
 * body) makes it linear over something very long, once per row, on every
 * render. 320 characters is far more than any row can display at 2560px and far
 * less than a blob.
 *
 * The cut is at a word boundary where one is near, so the ellipsis does not
 * land mid-word when the row happens to be wide enough to show the end.
 */
export const PREVIEW_MAX_CHARS = 320;

/**
 * A URL in running text.
 *
 * Deliberately narrow: an explicit scheme (`http`, `https`) or a `www.` prefix,
 * running to the first whitespace. It does NOT try to recognise bare domains
 * ("donweb.com"), and that restraint is the design — "Escribinos a soporte.com
 * o llamanos" is a sentence, and a matcher greedy enough to catch every bare
 * domain also eats ordinary prose containing a dot.
 *
 * Trailing punctuation is excluded from the match so "visitá https://x.com."
 * does not swallow the full stop that ends the sentence — `[^\s<>"']+` is
 * greedy and would, which is why the trailing class is spelled out separately.
 *
 * The `*` rather than `+` after the scheme is deliberate: a bare "https://"
 * with nothing usable after it is a fragment a body really does contain (a
 * truncated link, a template that never got its value), and it has to MATCH so
 * that `domainOf` can fail on it and the caller can drop it. With `+` it would
 * not match at all and would survive into the row as literal "https://".
 */
const URL_PATTERN =
  /\b(?:https?:\/\/|www\.)(?:[^\s<>"']*[^\s<>"'.,;:!?)\]}])?/gi;

/**
 * Angle-bracketed and parenthesised wrappers around a URL, and the bare label
 * that so often precedes one.
 *
 * Mail composers wrap links as `<https://…>` or `( https://… )`, and once the
 * URL is gone the wrapper is punctuation with nothing inside it. Left alone,
 * the row reads "Revísalo para evitar interrupciones donweb ( )".
 */
const EMPTY_WRAPPER = /[([<]\s*[)\]>]/g;

/** Runs of whitespace, including the ones the removals just created. */
const WHITESPACE = /\s+/g;

/**
 * The domain of a URL, or undefined when it does not have a usable one.
 *
 * `URL` rather than a regex, because host parsing is where hand-rolled regexes
 * are wrong in ways that matter (userinfo before an `@`, ports, IPv6 literals).
 * A `www.` prefix is given the scheme the pattern implies it has.
 */
function domainOf(match: string): string | undefined {
  try {
    const url = new URL(match.startsWith("www.") ? `https://${match}` : match);
    // The leading `www.` is noise in a preview line; every other subdomain is
    // information ("mail.google.com" is not "google.com").
    const host = url.hostname.replace(/^www\./i, "");
    return host === "" ? undefined : host;
  } catch {
    return undefined;
  }
}

export interface PreviewOptions {
  /**
   * What replaces a URL.
   *
   * `"domain"` (the default) leaves the bare host, so "…más info en
   * https://donweb.com/?utm_campaign=…" becomes "…más info en donweb.com".
   * The reader keeps the one piece of the URL that tells them anything, and
   * loses the sixty characters of campaign parameters that tell them nothing.
   *
   * `"drop"` removes it entirely, for the callers that want Gmail's terser
   * line. Both are honest: neither invents text.
   */
  readonly urls?: "domain" | "drop";
  /** Overrides {@link PREVIEW_MAX_CHARS}; mainly for tests. */
  readonly maxChars?: number;
}

/**
 * The preview line as a row should show it.
 *
 * Pure, total, and never throws: an unparseable URL falls back to dropping it,
 * an empty input returns "". Everything here is a string transformation over
 * text that will become a TEXT NODE — nothing on this path is ever interpreted
 * as markup, so it carries none of `snippet.ts`'s escaping obligations.
 */
export function previewText(raw: string, options: PreviewOptions = {}): string {
  if (raw === "") return "";
  const mode = options.urls ?? "domain";
  const maxChars = options.maxChars ?? PREVIEW_MAX_CHARS;

  /*
   * The length bound is applied FIRST, before the regex passes.
   *
   * Trimming afterwards would mean running the URL scan over the whole
   * pathological string to then throw most of it away — the cost this bound
   * exists to avoid. A little slack is kept so a URL straddling the cut is
   * still recognised as one rather than surviving as a truncated fragment.
   */
  const bounded = raw.length > maxChars * 2 ? raw.slice(0, maxChars * 2) : raw;

  const withoutUrls = bounded.replace(URL_PATTERN, (match) => {
    if (mode === "drop") return " ";
    // A URL with no parseable host — a truncated link, a template that never
    // got its value — is dropped rather than left as a fragment.
    return domainOf(match) ?? " ";
  });

  const tidy = withoutUrls
    .replace(EMPTY_WRAPPER, " ")
    .replace(WHITESPACE, " ")
    .trim();

  if (tidy.length <= maxChars) return tidy;

  /*
   * The cut. A word boundary within the last 40 characters is preferred so the
   * line does not end mid-word; past that the break is taken where it falls,
   * because a "word" that long is not one.
   *
   * No ellipsis is appended: the row clips with a CSS `text-overflow`, and a
   * literal "…" inside a string that is then clipped produces "text…" followed
   * by the clip's own ellipsis on a wide row, or a wasted character on a narrow
   * one.
   */
  const hard = tidy.slice(0, maxChars);
  const lastSpace = hard.lastIndexOf(" ");
  return lastSpace > maxChars - 40 ? hard.slice(0, lastSpace) : hard;
}
