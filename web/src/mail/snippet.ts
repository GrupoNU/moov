/**
 * Search snippets: parsing the server's `<mark>` contract WITHOUT trusting it
 * (L3 epic E3, RFC 8621 §5).
 *
 * # What the server promises, and why this file assumes it might not
 *
 * `internal/jmap/mail/snippet.go` states the contract in capitals: "the
 * subject and preview of a SearchSnippet are PLAIN TEXT whose only markup is
 * <mark> and </mark>. No other tag, no attribute, no unescaped ampersand or
 * angle bracket, and never an unbalanced mark." It is pinned server-side by
 * `TestSnippetsContainOnlyMarkMarkup`, and the mechanism behind it is sound:
 * the store marks matches with control-character sentinels having first
 * stripped those sentinels from the source, and the handler HTML-escapes the
 * whole string BEFORE turning the escaped sentinels back into tags.
 *
 * This module still does not trust it, and the same file says why that is the
 * house position: "The PWA still renders it inside its own sanitize pipeline —
 * defense in depth is the project's posture (ADR §'Seguridad HTML': three
 * layers) — but it does not have to trust this endpoint to be safe."
 *
 * # How the distrust is implemented: there is no HTML path at all
 *
 * The obvious implementation — `dangerouslySetInnerHTML` with a DOMPurify pass
 * — would be *a* defense. This is a better one: the snippet is never treated
 * as markup in the first place. {@link parseSnippet} splits the string on the
 * two literal token sequences `<mark>` and `</mark>` and returns a list of
 * plain-text segments. React renders each segment as a TEXT NODE, and text
 * nodes cannot execute: a segment containing `<script>alert(1)</script>`
 * appears on screen as those literal characters.
 *
 * So a compromised or buggy server cannot inject anything through this path,
 * because nothing on this path can interpret markup. That is a stronger
 * property than sanitizing, and it costs less code.
 *
 * The remaining escape sequences (`&amp;`, `&lt;`, `&gt;`, `&quot;`, `&#39;`)
 * ARE decoded, because the server escaped them and a user should read "R&D",
 * not "R&amp;D". The decode is a fixed table applied to text that will become a
 * text node — it can never re-create a tag, because the result is never parsed
 * as HTML.
 */

import type { JmapClient } from "../api/jmap";
import { CAP_CORE, CAP_MAIL } from "../api/jmap";

/** One run of snippet text, and whether the server marked it as a match. */
export interface SnippetSegment {
  readonly text: string;
  readonly isMatch: boolean;
}

/** A message's highlighted subject and preview, as the server returned them. */
export interface SearchSnippet {
  readonly emailId: string;
  /** RFC 8621 §5: null when the search did not match the subject. */
  readonly subject: string | undefined;
  readonly preview: string | undefined;
}

const OPEN = "<mark>";
const CLOSE = "</mark>";

/**
 * The HTML entities the server's `html.EscapeString` produces, reversed.
 *
 * Go's `html.EscapeString` escapes exactly these five, so the table is
 * complete rather than a sample. Numeric entities beyond `&#39;` are left
 * alone: the server does not emit them, and decoding arbitrary numeric escapes
 * is how an unescaping routine becomes an attack surface of its own.
 */
const ENTITIES: readonly (readonly [string, string])[] = [
  ["&lt;", "<"],
  ["&gt;", ">"],
  ["&quot;", '"'],
  ["&#39;", "'"],
  // `&amp;` LAST, so "&amp;lt;" decodes to the literal "&lt;" rather than to
  // "<". Decoding the ampersand first would let a doubly-escaped sequence
  // become a real angle bracket — the classic unescaping bug.
  ["&amp;", "&"],
];

/** Decodes the five entities the server escapes, in the safe order. */
function decodeEntities(text: string): string {
  let out = text;
  for (const [entity, char] of ENTITIES) {
    out = out.split(entity).join(char);
  }
  return out;
}

/**
 * Splits a snippet into text segments, marked and unmarked.
 *
 * Everything that is not one of the two literal token sequences is TEXT,
 * including any other angle bracket — which is the whole security property.
 * An unbalanced or nested mark cannot produce anything worse than a segment
 * whose `isMatch` is wrong, because the output is a list of strings either
 * way.
 */
export function parseSnippet(raw: string | undefined | null): readonly SnippetSegment[] {
  if (raw === undefined || raw === null || raw === "") return [];

  const segments: SnippetSegment[] = [];
  let index = 0;
  let inMark = false;

  const push = (text: string, isMatch: boolean): void => {
    if (text === "") return;
    segments.push({ text: decodeEntities(text), isMatch });
  };

  while (index < raw.length) {
    const token = inMark ? CLOSE : OPEN;
    const next = raw.indexOf(token, index);
    if (next < 0) {
      // No further token: the rest is one segment in the current state. An
      // unterminated <mark> therefore highlights to the end rather than
      // dropping text — degraded, never lossy.
      push(raw.slice(index), inMark);
      break;
    }
    push(raw.slice(index, next), inMark);
    index = next + token.length;
    inMark = !inMark;
  }

  return segments;
}

/** True when any segment was marked — i.e. the snippet shows a real match. */
export function hasHighlight(segments: readonly SnippetSegment[]): boolean {
  return segments.some((segment) => segment.isMatch);
}

/**
 * Fetches snippets for a set of messages (RFC 8621 §5.1).
 *
 * The `filter` argument MUST be the same one `Email/query` was given — §5.1
 * says so, and the server re-runs it to know what to highlight. Passing a
 * different filter would highlight the wrong words.
 *
 * The caller passes only the ids currently ON SCREEN. That is not an
 * optimisation but a correctness bound: the window is at most 200 rows, of
 * which a viewport shows perhaps 20, and asking the server to headline 200
 * message bodies to paint 20 rows is the same category of mistake as fetching
 * body values to render a list.
 */
export async function fetchSnippets(
  client: JmapClient,
  accountId: string,
  filter: Record<string, unknown> | null,
  emailIds: readonly string[],
  signal?: AbortSignal,
): Promise<readonly SearchSnippet[]> {
  if (emailIds.length === 0) return [];

  const response = await client.call(
    [["SearchSnippet/get", { accountId, filter, emailIds: [...emailIds] }, "s"]],
    [CAP_CORE, CAP_MAIL],
    signal,
  );

  for (const [name, args, id] of response.methodResponses) {
    if (id !== "s") continue;
    if (name === "error") {
      /*
       * A snippet failure is NEVER fatal: the result list is perfectly usable
       * without highlighting, and a server that has no SearchSnippet/get at
       * all (an older build) answers `unknownMethod` here. Returning empty
       * degrades to a plain list, which is exactly the right outcome.
       */
      return [];
    }
    const list = (args as { list?: unknown }).list;
    if (!Array.isArray(list)) return [];
    return list.flatMap((entry): SearchSnippet[] => {
      if (typeof entry !== "object" || entry === null) return [];
      const row = entry as Record<string, unknown>;
      const emailId = row.emailId;
      if (typeof emailId !== "string") return [];
      return [
        {
          emailId,
          subject: typeof row.subject === "string" ? row.subject : undefined,
          preview: typeof row.preview === "string" ? row.preview : undefined,
        },
      ];
    });
  }

  return [];
}

/** Indexes snippets by message id, for a row to look its own up. */
export function snippetIndex(
  snippets: readonly SearchSnippet[],
): ReadonlyMap<string, SearchSnippet> {
  return new Map(snippets.map((snippet) => [snippet.emailId, snippet]));
}
