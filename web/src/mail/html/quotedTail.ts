/**
 * Quoted-tail detection — "Show trimmed content" (canon §2.1, L3 epic E1).
 *
 * Gmail collapses the repeated material at the bottom of a reply behind a "⋯"
 * toggle, and it is the single change that makes a 24-message thread readable
 * instead of a wall of the same text quoted twenty-four times.
 *
 * # WHERE this runs, and why it cannot run anywhere else
 *
 * The message body is rendered by a three-layer pipeline (MessageBody →
 * SecureHtmlBody → srcdoc.ts): raw HTML is sanitized by DOMPurify under
 * `policy.ts`, and the SANITIZED STRING is embedded in the srcdoc of an iframe
 * whose sandbox grants neither `allow-scripts` nor `allow-same-origin`.
 *
 * That rules out the obvious implementations, each for a hard reason:
 *
 *   1. **A script inside the frame** that hides the quote on click — needs
 *      `allow-scripts`, which is precisely what layer 3 exists to refuse.
 *   2. **The parent reaching into the frame's DOM** — needs
 *      `allow-same-origin`, same refusal (and the pair together voids the
 *      sandbox entirely).
 *   3. **Wrapping the tail in `<div class="quoted">` BEFORE sanitization** —
 *      `class` is deliberately dropped by the policy (DOM-clobbering surface,
 *      policy.ts), so the wrapper would arrive at the frame with no hook to
 *      style. Verified, not assumed: `class` is absent from
 *      EMAIL_ALLOWED_ATTRIBUTES.
 *   4. **Re-parsing the sanitized output into a DOM to cut it** — feeding
 *      sanitizer output back through a parser is the mutation-XSS shape the
 *      pipeline is built to avoid (sanitize.ts states the rule: the second
 *      pass always re-runs from the ORIGINAL html, never from prior output).
 *
 * So this module does the ONE thing that composes with all four constraints:
 * it finds the byte offset where the quoted tail begins **in the sanitized
 * string**, and returns the string SPLIT IN TWO. Nothing is parsed, nothing is
 * rewritten, not one byte of the sanitized markup is altered — {@link splitQuotedTail}
 * is a substring operation whose two halves concatenate back to the exact input.
 * A test pins that identity.
 *
 * `buildSrcDoc` then composes the two halves with a wrapper IT authors (trusted
 * markup, added after sanitization, never re-parsed) and a `<style>` rule in
 * the head it already owns. Showing or hiding the quote re-renders the srcdoc
 * with a different flag — no postMessage, no script, no DOM access, no new
 * capability granted to the frame.
 *
 * # The detection itself
 *
 * Deliberately CONSERVATIVE, because the failure modes are asymmetric: hiding
 * text the user wanted is a data-loss-shaped bug, while leaving a quote visible
 * is merely the status quo. So every rule below requires a structural anchor,
 * and the whole thing refuses when the tail would be most of the message
 * ({@link MIN_VISIBLE_CHARS}) — a "reply" that is 95% quote is usually a
 * forward, where the quote IS the content.
 *
 * The four shapes it recognises, all seen in the pilot's own corpus:
 *
 *   - **Gmail**: `<div class="gmail_quote">` wrapping an attribution line and
 *     a `<blockquote>`. The class is gone by the time we see it, so the anchor
 *     is the attribution text plus the blockquote that follows it.
 *   - **The attribution line**: "On <date>, <someone> wrote:" and its
 *     localised siblings — "El … escribió:", "Le … a écrit :", "Am … schrieb:".
 *     Both locales this app ships in are covered, plus the ones the pilot's
 *     mail actually carries.
 *   - **Outlook**: a horizontal rule or a bordered div followed by
 *     `From:`/`De:`/`Sent:`/`Enviado:` header lines — the "original message"
 *     divider.
 *   - **A trailing blockquote chain**: the last top-level element is a
 *     `<blockquote>` and everything after it is whitespace. This is the
 *     nested-reply shape, and it is the one that compounds — each round trip
 *     adds a level.
 */

/**
 * How much visible text must survive for a trim to be offered.
 *
 * Below this the "reply" is really a forward or a one-word ack on top of a
 * long quote, and collapsing it would leave the reader looking at an almost
 * empty pane with a button. Gmail shows the same restraint.
 */
export const MIN_VISIBLE_CHARS = 24;

/** The result of looking for a quoted tail. */
export interface QuotedTailSplit {
  /** The markup to show always. Never empty when `quoted` is non-empty. */
  readonly visible: string;
  /** The trimmed tail, or "" when there is nothing to trim. */
  readonly quoted: string;
}

/**
 * The attribution lines that open a quote, as TEXT patterns.
 *
 * Matched against the sanitized markup with tags still in it, so each pattern
 * tolerates markup between its landmarks: real clients wrap the date in a
 * `<span>`, the name in a `<b>`, and the address in an `<a>`. `[^]{0,400}` is
 * the tolerance — bounded, so a pathological body cannot make the regex walk
 * the whole document between two landmarks (the superlinear-blowup class E4's
 * fuzzing found in enmime).
 *
 * Each alternative is anchored on a VERB in the past tense followed by a
 * colon, which is what makes it an attribution rather than prose: "wrote:",
 * "escribió:", "a écrit :", "schrieb:", "ha scritto:", "escreveu:".
 */
const ATTRIBUTION_PATTERNS: readonly RegExp[] = [
  // English: "On Mon, Aug 4, 2026 at 10:11, Ana <ana@x> wrote:"
  /\bOn\b[^]{0,400}?\bwrote\s*:/i,
  // Spanish: "El lun, 4 ago 2026 a las 10:11, Ana escribió:"
  /\bEl\b[^]{0,400}?\bescribi(?:ó|o)\s*:/i,
  // French: "Le lun. 4 août 2026 à 10:11, Ana a écrit :"
  /\bLe\b[^]{0,400}?\ba\s+(?:é|e)crit\s*:/i,
  // German: "Am 04.08.2026 um 10:11 schrieb Ana:"
  /\bAm\b[^]{0,400}?\bschrieb\b[^]{0,120}?:/i,
  // Italian / Portuguese.
  /\bIl\b[^]{0,400}?\bha\s+scritto\s*:/i,
  /\bEm\b[^]{0,400}?\bescreveu\s*:/i,
];

/**
 * The Outlook "original message" divider: a `From:`-style header block.
 *
 * Outlook does not quote with `<blockquote>` — it appends a rule and then a
 * miniature header block. The anchor is the header LABEL at the start of a
 * line-ish position, immediately followed (within a short window) by a second
 * label, because a lone "From:" appears in ordinary prose all the time and a
 * From/Sent/To triple does not.
 */
const OUTLOOK_HEADER_PATTERN =
  /(?:<hr\b[^>]*>|<div\b[^>]*>|<p\b[^>]*>|^)\s*(?:<[^>]+>\s*)*(?:\*{0,2})\s*(?:From|De|Von|Da|Van)\s*:[^]{0,300}?(?:Sent|Enviado|Gesendet|Date|Fecha|Data|Verzonden|To|Para|An|A)\s*:/i;

/** The explicit "-----Original Message-----" separator some clients emit. */
const ORIGINAL_MESSAGE_PATTERN =
  /-{2,}\s*(?:Original Message|Mensaje original|Mensaje Original|Forwarded message|Mensaje reenviado|Ursprüngliche Nachricht|Message d'origine)\s*-{2,}/i;

/**
 * Finds where a trailing `<blockquote>` chain begins.
 *
 * "Trailing" is the load-bearing word: a blockquote in the MIDDLE of a reply
 * is the author quoting a line to answer it inline, and hiding that would
 * destroy the reply's meaning. So this only fires when the blockquote's
 * matching close tag is followed by nothing but whitespace and closing tags.
 *
 * The scan is a depth counter over `<blockquote` / `</blockquote>` tokens
 * rather than a parse: it needs only to pair the tags, and the input is
 * already sanitized markup where those tags are well-formed by construction
 * (DOMPurify serializes a real tree).
 */
function trailingBlockquoteStart(html: string): number | undefined {
  const token = /<\/?blockquote\b[^>]*>/gi;
  const opens: number[] = [];
  let depth = 0;
  let lastTopLevelOpen: number | undefined;
  let lastTopLevelEnd: number | undefined;

  for (const match of html.matchAll(token)) {
    const at = match.index;
    if (at === undefined) continue;
    const isClose = match[0].startsWith("</");
    if (isClose) {
      depth -= 1;
      if (depth === 0) {
        lastTopLevelOpen = opens.pop();
        lastTopLevelEnd = at + match[0].length;
      } else if (depth < 0) {
        // Unbalanced markup: refuse rather than guess. Sanitized output is
        // balanced, so this means something upstream changed and the safe
        // answer is "no trim".
        return undefined;
      }
    } else {
      if (depth === 0) opens.push(at);
      depth += 1;
    }
  }

  if (depth !== 0 || lastTopLevelOpen === undefined || lastTopLevelEnd === undefined) {
    return undefined;
  }

  // Everything after the last top-level blockquote must be closing tags and
  // whitespace — otherwise the author wrote below the quote (bottom-posting),
  // and that text is theirs, not the quote's.
  const after = html.slice(lastTopLevelEnd);
  if (!isInsubstantial(after)) return undefined;

  return lastTopLevelOpen;
}

/**
 * True when a fragment carries no reader-visible content: whitespace, closing
 * tags, empty containers and `<br>`s.
 *
 * Used for the "is there anything after the quote" question, so it must not be
 * fooled by `<div>&nbsp;</div>` — the empty-paragraph padding every client
 * appends. Entities that render as space are treated as space.
 */
function isInsubstantial(fragment: string): boolean {
  const text = fragment
    .replace(/<[^>]*>/g, "")
    .replace(/&nbsp;|&#160;|&#xa0;/gi, " ")
    .trim();
  return text === "";
}

/** The visible text length of a fragment, for the MIN_VISIBLE_CHARS rule. */
function visibleLength(fragment: string): number {
  return fragment
    .replace(/<[^>]*>/g, " ")
    .replace(/&nbsp;|&#160;|&#xa0;/gi, " ")
    .replace(/&[a-z]+;|&#\d+;/gi, "x")
    .replace(/\s+/g, " ")
    .trim().length;
}

/**
 * Backs an offset up to the start of the element that contains it.
 *
 * An attribution line is found by its TEXT, which sits inside a `<div>` or
 * `<p>` that the quote's own wrapper opened. Cutting at the text offset would
 * leave that opening tag on the visible side and the closing tag on the hidden
 * side — which the two halves' concatenation would still restore exactly, but
 * which would render as an unbalanced fragment in each half.
 *
 * So the cut moves back to the nearest preceding tag boundary at or before the
 * match, bounded by a short lookback: the wrapper is adjacent to its text in
 * every client that emits one.
 */
function backUpToTagStart(html: string, offset: number): number {
  const lookbackStart = Math.max(0, offset - 600);
  const window = html.slice(lookbackStart, offset);
  // The last OPENING tag in the window — a closing tag before the match means
  // the previous element ended and the match starts fresh text.
  const opens = [...window.matchAll(/<(?!\/)[a-z][^>]*>/gi)];
  const closes = [...window.matchAll(/<\/[a-z][^>]*>/gi)];
  const lastOpen = opens[opens.length - 1];
  const lastClose = closes[closes.length - 1];

  if (lastOpen?.index === undefined) return offset;
  // Only back up when the opening tag is the LAST tag before the match; if a
  // close came after it, the element already ended and there is nothing to
  // keep together.
  if (lastClose?.index !== undefined && lastClose.index > lastOpen.index) return offset;

  // Only when the gap between the tag and the match is insubstantial — that
  // is what makes it "the element that opened for this line" rather than one
  // that happens to be nearby.
  const between = window.slice(lastOpen.index + lastOpen[0].length);
  if (!isInsubstantial(between)) return offset;

  return lookbackStart + lastOpen.index;
}

/**
 * Finds the offset where the quoted tail starts, or undefined for no quote.
 *
 * The candidates are gathered from every rule and the EARLIEST wins: a message
 * that has both an attribution line and a trailing blockquote (the usual case
 * — the line introduces the quote) must cut at the line, not at the quote,
 * or the attribution would be left dangling above a collapsed block.
 */
export function findQuotedTailOffset(html: string): number | undefined {
  const candidates: number[] = [];

  for (const pattern of ATTRIBUTION_PATTERNS) {
    const match = pattern.exec(html);
    if (match?.index !== undefined) candidates.push(backUpToTagStart(html, match.index));
  }

  const outlook = OUTLOOK_HEADER_PATTERN.exec(html);
  if (outlook?.index !== undefined) {
    // The pattern may begin AT the divider tag it anchors on, in which case
    // that offset is already an element boundary.
    candidates.push(backUpToTagStart(html, outlook.index));
  }

  const original = ORIGINAL_MESSAGE_PATTERN.exec(html);
  if (original?.index !== undefined) {
    candidates.push(backUpToTagStart(html, original.index));
  }

  const blockquote = trailingBlockquoteStart(html);
  if (blockquote !== undefined) candidates.push(blockquote);

  if (candidates.length === 0) return undefined;

  const offset = Math.min(...candidates);

  // Nothing to hide, or nothing left to show.
  if (offset <= 0) return undefined;
  const visible = html.slice(0, offset);
  const quoted = html.slice(offset);
  if (isInsubstantial(quoted)) return undefined;
  if (visibleLength(visible) < MIN_VISIBLE_CHARS) return undefined;

  return offset;
}

/**
 * Splits sanitized markup into its always-visible head and its quoted tail.
 *
 * PURE SUBSTRING SPLIT — `visible + quoted === html`, always, which is the
 * property that makes this safe to run on sanitizer output: no re-parse, no
 * re-serialize, no mutation, and therefore no way for this module to
 * reintroduce anything the sanitizer removed.
 *
 * When there is no detectable quote, `quoted` is "" and `visible` is the whole
 * input — callers render exactly what they rendered before this existed.
 */
export function splitQuotedTail(html: string): QuotedTailSplit {
  const offset = findQuotedTailOffset(html);
  if (offset === undefined) return { visible: html, quoted: "" };
  return { visible: html.slice(0, offset), quoted: html.slice(offset) };
}
