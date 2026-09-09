/**
 * Sizing the message frame to its content WITHOUT a channel out of the
 * sandbox (C-03, decision 4 of the 2026-09-08 review: "keep the iframe").
 *
 * # The problem, and the five ways to not solve it
 *
 * The body renders in `<iframe sandbox srcdoc>` with neither `allow-scripts`
 * nor `allow-same-origin` (arbitration W-A4; `srcdoc.ts` pins the grant set).
 * Such a frame is a black box to its parent: the document inside has an
 * opaque origin, so `contentDocument` is null and `scrollHeight` unreadable,
 * and it runs no script, so it cannot `postMessage` its own height out. A
 * fixed-height frame (the state before C-03) therefore gave every message
 * 320px — a nested scrollbar on long mail, an empty box under short mail.
 *
 * The honest options, each rejected for a reason worth keeping:
 *
 *   (a) Measure in a hidden offscreen sandboxed frame — still cross-origin,
 *       still unreadable. Nothing to measure with.
 *   (b) `allow-same-origin` on a "dedicated" origin — srcdoc inherits the
 *       PARENT's origin under that grant. That is precisely the pair W-A4
 *       forbids; a bypass of layers 1-2 would then own the app.
 *   (c) Render the sanitized HTML in the parent DOM behind a Shadow DOM —
 *       drops layer 3 entirely. Decision 4 says no.
 *   (e) `ResizeObserver` on the frame's document — needs (b).
 *
 * Which leaves (d): the PARENT already owns the sanitized string — it builds
 * the srcdoc from it — so it can derive a height from that string without any
 * channel at all. That is what this module does, and the price is stated
 * plainly: it is an ESTIMATE. When it undershoots, the frame keeps its own
 * scrollbar (the pre-C-03 behavior, now rare); when it overshoots, there is
 * some white space under the message. Neither is a security event, and the
 * estimate is biased slightly tall because a clipped message with a nested
 * scrollbar is the more annoying of the two errors.
 *
 * # What the sandbox still buys, and what the estimate must never do
 *
 * The frame's height is decided HERE, from a string, under a clamp. Hostile
 * content therefore cannot grow the frame to cover app chrome by declaring a
 * 100000px image (the clamp), and the sandbox attributes and CSP are not
 * touched by any of this — `SecureHtmlBody.test.tsx` pins the attribute byte
 * for byte, so a future "let's just measure it properly" that adds a grant
 * fails a test rather than a review.
 *
 * # The heuristic
 *
 * A string scan, no DOM: closing block tags and `<br>` become line breaks,
 * table cells become spaces, remaining tags vanish, and each resulting line
 * costs `ceil(chars / charsPerLine)` rendered lines at the frame's base line
 * height (`srcdoc.ts` BASE_STYLES: 15px / 1.55). Images add their declared
 * height (capped), a default for undeclared ones, and the placeholder size for
 * blocked ones (no src). Block elements add their default margins. Everything
 * is then padded, scaled by the tall bias and clamped.
 *
 * The scan is linear in the string and bounded by the sanitizer's own output
 * size; it allocates the text copy once. It runs in a memo per srcdoc change.
 */

/** The base line height of the frame document (BASE_STYLES: 15px × 1.55). */
const LINE_PX = 23.25;

/** Average glyph advance at 15px in the frame's sans stack. Slightly under
 * the true figure so lines come out a little SHORTER than reality, i.e. more
 * of them — the tall bias, applied at the source. */
const GLYPH_PX = 7.3;

/** Vertical space a block element's default margins add (a `<p>` collapses
 * its 1em margins with its neighbours to roughly one line's worth). */
const BLOCK_MARGIN_PX = 12;

/** An image whose markup declares no height. Signatures and logos are small;
 * hero images are tall; this sits between and the clamp catches the rest. */
const UNDECLARED_IMAGE_PX = 180;

/** The largest height ONE declared image may contribute. A sender's
 * `height="99999"` must not be an instruction to us. */
const MAX_IMAGE_PX = 1600;

/** The dashed placeholder a blocked image renders as (BASE_STYLES). */
const BLOCKED_IMAGE_PX = 22;

/** The frame document's own padding (BASE_STYLES body padding: 4 + 16). */
const DOCUMENT_PADDING_PX = 20;

/** Overshoot on purpose: white space beats a nested scrollbar. */
const TALL_BIAS = 1.12;

/** The width assumed when the container has not been measured yet (jsdom,
 * first paint). A typical reading column. */
export const DEFAULT_FRAME_WIDTH_PX = 640;

/** The floor: a one-line message still gets a frame you can see. */
export const MIN_FRAME_PX = 64;

/**
 * The height at which a message is CUT and offered "show the whole message".
 *
 * Long newsletters and mega-threads with quotes shown routinely estimate to
 * several thousand pixels; rendering them at full height on open would put
 * the next message of a conversation a long scroll away. This is Gmail's own
 * shape for very long mail (it clips and offers to expand).
 */
export const COLLAPSED_MAX_PX = 1800;

/**
 * The hard ceiling even when expanded. Beyond it the frame scrolls internally
 * — the pre-C-03 behavior, kept as the backstop rather than an unbounded
 * element the browser must lay out.
 */
export const EXPANDED_MAX_PX = 12000;

/** Tags whose CLOSE ends a line of text. */
const LINE_ENDING_TAGS =
  /<\/(?:p|div|li|tr|h[1-6]|blockquote|pre|dd|dt|section|article|header|footer|table|ul|ol|address|figcaption|center)\s*>/gi;

/** Tags that are a line break on their own. */
const SELF_BREAKING_TAGS = /<(?:br|hr)\b[^>]*>/gi;

/** Tags whose close separates text on the SAME line. */
const CELL_TAGS = /<\/(?:td|th)\s*>/gi;

/** Block openers, counted for their margins. */
const BLOCK_OPENERS = /<(?:p|div|h[1-6]|blockquote|pre|ul|ol|table|hr)\b/gi;

/** Every remaining tag. */
const ANY_TAG = /<[^>]*>/g;

/** One `<img …>`, with its attributes. */
const IMG_TAG = /<img\b([^>]*)>/gi;

/** A declared pixel height/width: `height="120"`, `height=120`, `height='120px'`. */
const HEIGHT_ATTR = /\bheight\s*=\s*["']?\s*(\d{1,5})(?:px)?\s*["']?/i;
const WIDTH_ATTR = /\bwidth\s*=\s*["']?\s*(\d{1,5})(?:px)?\s*["']?/i;
const SRC_ATTR = /\bsrc\s*=/i;

/** Named entities the scan turns back into their width-bearing character. */
const ENTITIES = /&(?:nbsp|#160|amp|lt|gt|quot|#39);/gi;

/** The text lines of the markup, tags removed and entities normalised. */
function textLines(html: string): readonly string[] {
  const separated = html
    .replace(SELF_BREAKING_TAGS, "\n")
    .replace(LINE_ENDING_TAGS, "\n")
    .replace(CELL_TAGS, " ")
    .replace(ANY_TAG, "")
    .replace(ENTITIES, " ");
  return separated.split("\n");
}

/** Pixels the images in the markup will occupy, under the per-image cap. */
function imageHeight(html: string, widthPx: number): number {
  let total = 0;
  for (const match of html.matchAll(IMG_TAG)) {
    const attributes = match[1] ?? "";
    if (!SRC_ATTR.test(attributes)) {
      total += BLOCKED_IMAGE_PX;
      continue;
    }
    const declaredHeight = HEIGHT_ATTR.exec(attributes);
    if (declaredHeight?.[1] !== undefined) {
      const declaredWidth = WIDTH_ATTR.exec(attributes);
      let height = Number(declaredHeight[1]);
      // An image wider than the frame is scaled down by BASE_STYLES
      // (max-width: 100%; height: auto) — so is its height.
      if (declaredWidth?.[1] !== undefined) {
        const width = Number(declaredWidth[1]);
        if (width > widthPx && width > 0) height = (height * widthPx) / width;
      }
      total += Math.min(MAX_IMAGE_PX, height);
      continue;
    }
    total += UNDECLARED_IMAGE_PX;
  }
  return total;
}

/**
 * The frame height, in CSS pixels, that the sanitized markup is expected to
 * need at the given width — before the collapsed/expanded cap is applied.
 *
 * `sanitizedHtml` is the string that goes into the srcdoc (the visible half
 * plus the quoted tail when it is shown). `widthPx` is the frame's laid-out
 * width; unmeasured callers pass {@link DEFAULT_FRAME_WIDTH_PX}.
 */
export function estimateFrameHeight(sanitizedHtml: string, widthPx: number): number {
  const width = Number.isFinite(widthPx) && widthPx > 0 ? widthPx : DEFAULT_FRAME_WIDTH_PX;
  const charsPerLine = Math.max(16, Math.floor(width / GLYPH_PX));

  let lines = 0;
  for (const line of textLines(sanitizedHtml)) {
    const length = line.trim().length;
    if (length === 0) continue;
    lines += Math.ceil(length / charsPerLine);
  }

  // A run of `<br>`s is vertical space the sender meant; count breaks that
  // produced empty lines at a fraction each, rather than ignoring them.
  const breaks = (sanitizedHtml.match(SELF_BREAKING_TAGS) ?? []).length;
  const blocks = (sanitizedHtml.match(BLOCK_OPENERS) ?? []).length;

  const text = lines * LINE_PX + breaks * (LINE_PX * 0.5);
  const margins = blocks * BLOCK_MARGIN_PX;
  const images = imageHeight(sanitizedHtml, width);

  const raw = (text + margins + images) * TALL_BIAS + DOCUMENT_PADDING_PX;
  return Math.max(MIN_FRAME_PX, Math.min(EXPANDED_MAX_PX, Math.round(raw)));
}

/** What the frame should be told, given an estimate and whether the user
 * asked for the whole message. */
export interface FrameSizing {
  /** The `height` to set on the frame, in CSS pixels. */
  readonly heightPx: number;
  /** True when the estimate exceeds the collapsed cap — i.e. an expander is
   * warranted. Stays true after expanding (the control flips to "less"). */
  readonly isClipped: boolean;
}

/**
 * Applies the cap: the collapsed maximum unless expanded, and the hard
 * ceiling always. The two constants are what the tests assert against.
 */
export function frameSizing(estimatePx: number, expanded: boolean): FrameSizing {
  const isClipped = estimatePx > COLLAPSED_MAX_PX;
  const cap = expanded ? EXPANDED_MAX_PX : COLLAPSED_MAX_PX;
  return {
    heightPx: Math.max(MIN_FRAME_PX, Math.min(cap, estimatePx)),
    isClipped,
  };
}
