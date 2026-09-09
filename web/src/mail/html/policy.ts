/**
 * THE SANITIZER POLICY — what survives in a rendered mail body, and why.
 *
 * This file is the normative, reviewable statement of the client-side layer
 * of W-A4 / ADR §5: every tag, attribute, URL scheme and CSS property that a
 * message's HTML may keep is NAMED here, and everything unnamed is dropped.
 * Allowlist, never blocklist: a blocklist has to anticipate every dangerous
 * name; an allowlist fails closed on the ones nobody thought of.
 *
 * The threat model is the researched one (docs/research/04 §4.3): a competent
 * attacker who has read this source and sends mail crafted to escape it.
 * Fastmail, ProtonMail and Gmail have all shipped sanitizer bypasses; the
 * assumption here is that this sanitizer has bugs too, which is why the
 * OUTPUT still renders inside a sandboxed, scriptless, opaque-origin iframe
 * under `default-src 'none'` (see srcdoc.ts) — the layers fail independently.
 *
 * # What is deliberately refused, each with its attack
 *
 *   - script, and every on* attribute .... direct execution. (Excluded by
 *     construction: no on* name can appear in an attribute allowlist.)
 *   - style ELEMENTS, <link>, @import .... non-inlined CSS is the "Spy
 *     Sheets" exfiltration class: attribute selectors + conditional url()
 *     leak message content with no JavaScript at all. Gmail strips all
 *     non-inlined CSS; so does Moov. Inline style ATTRIBUTES survive with a
 *     filtered property/value grammar (sanitizeStyleValue below).
 *   - url()/image-set()/expression() in inline CSS ... the network half of
 *     the same class, and (expression) legacy execution.
 *   - <base> ............ rebases every relative URL in the document; with
 *     it, an attacker controls where "harmless" links really point.
 *   - <meta> ............ http-equiv=refresh is navigation hijack; charset
 *     tricks are a decoding-confusion primitive.
 *   - form, input, button, select, textarea, option ... a credential-phishing
 *     form INSIDE a mail body, rendered by our product's chrome.
 *   - iframe, frame, object, embed, applet ... nested browsing contexts and
 *     plugin surface.
 *   - svg, math ......... foreign-content parsing is where most mutation-XSS
 *     (mXSS) bypasses of the last decade lived (annotation-xml, mglyph,
 *     foreignObject). Refusing the namespaces entirely removes the class.
 *   - audio, video, source, track, picture ... media elements fetch remote
 *     resources outside the img pipeline the proxy controls.
 *   - id, class, name ... id/name are DOM-clobbering handles; class is dead
 *     weight once stylesheets are stripped. Dropping them costs nothing
 *     legitimate and removes the clobbering surface.
 *   - javascript:, vbscript:, data:text/html, file:, blob: URLs ... script
 *     or context-dependent navigation. Checked on the PARSED scheme (the URL
 *     constructor strips the same tab/newline noise a browser strips, so
 *     "jav\tascript:" cannot sneak past a string comparison).
 *   - relative URLs ..... inside the srcdoc iframe they resolve against the
 *     APP's own base URL — the confused-deputy shape. An email has no
 *     legitimate relative URL to point at us.
 *   - target/rel as authored ... target is forced to _blank and rel to
 *     "noopener noreferrer" on every kept link: no tab-napping handle, no
 *     Referer leak, and a click can never navigate the app.
 *   - srcset, ping, background, poster, formaction, usemap ... secondary URL
 *     carriers that would bypass the single-src image pipeline.
 *   - position/z-index/fixed in CSS ... overlay/scroll-jack primitives; a
 *     message may not float content over anything, even inside its own frame.
 */

/** Elements a mail body may keep. Text structure, emphasis, links, images,
 * lists, and tables — still the workhorse of email layout. */
export const EMAIL_ALLOWED_TAGS: readonly string[] = [
  "a", "abbr", "acronym", "address", "article", "aside",
  "b", "bdi", "bdo", "big", "blockquote", "br",
  "caption", "center", "cite", "code", "col", "colgroup",
  "dd", "del", "dfn", "div", "dl", "dt",
  "em", "figcaption", "figure", "font", "footer",
  "h1", "h2", "h3", "h4", "h5", "h6", "header", "hr",
  "i", "img", "ins", "kbd", "li", "main", "mark", "nav",
  "ol", "p", "pre", "q", "s", "samp", "section", "small",
  "span", "strike", "strong", "sub", "sup",
  "table", "tbody", "td", "tfoot", "th", "thead", "time", "tr", "tt",
  "u", "ul", "var", "wbr",
];

/**
 * Attributes a kept element may carry. Presentational HTML attributes are
 * generously allowed (bgcolor, align, width... — 1990s markup is the living
 * dialect of email), because none of them can fetch, execute or address the
 * containing document. The three that CAN carry capability — href, src,
 * style — are re-checked value-by-value in the sanitizer's hook; being on
 * this list earns an attribute a check, not a pass.
 */
export const EMAIL_ALLOWED_ATTRIBUTES: readonly string[] = [
  "abbr", "align", "alt", "axis", "bgcolor", "border",
  "cellpadding", "cellspacing", "char", "charoff", "cite",
  "color", "colspan", "datetime", "dir", "face",
  "headers", "height", "href", "hspace", "lang", "nowrap",
  "reversed", "rowspan", "scope", "size", "span", "src", "start",
  "style", "summary", "title", "type", "valign", "value", "vspace", "width",
];

/** How a URL may be used, per classifyUrl. */
export type UrlClass =
  /** http/https — a link target, or a remote image (proxy territory). */
  | "web"
  | "mailto"
  | "tel"
  /** A raster image inlined as data: — no network fetch, no tracking. */
  | "data-image"
  /** A MIME content-id reference. Resolved by the PARENT (C-11): the part's
   * bytes come through the authenticated blob path and re-enter the
   * sanitizer as a raster `data:` URL — the only form the frame's CSP already
   * allows. Unresolved ones are dropped and counted, honestly. */
  | "cid"
  /** Everything else: javascript:, vbscript:, data:text/html, file:, blob:,
   * relative, unparseable. */
  | "refused";

/**
 * data: URLs are acceptable ONLY as raster images. SVG is excluded although
 * <img> would neuter its scripts, because policy simplicity beats cleverness
 * here: the only data: content this renderer ever passes is a format that
 * has no script grammar at all.
 */
const DATA_IMAGE_PATTERN = /^data:image\/(?:png|jpe?g|gif|webp);/i;

/**
 * Classifies a URL by what the policy lets it do.
 *
 * The check runs on the PARSED scheme, via the same URL parser the browser
 * navigates with: `new URL()` strips ASCII tab/CR/LF anywhere in the input
 * (per the URL spec) exactly as href resolution does, so the historical
 * bypass family — "jav\tascript:", "JaVaScRiPt:", leading whitespace,
 * newline-split schemes — is resolved to its real scheme BEFORE the decision
 * is made, and the decision cannot be tricked lexically. A string the parser
 * refuses (including every relative URL — there is deliberately no base) is
 * refused here too.
 */
export function classifyUrl(raw: string): UrlClass {
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    return "refused";
  }
  switch (parsed.protocol) {
    case "http:":
    case "https:":
      return "web";
    case "mailto:":
      return "mailto";
    case "tel:":
      return "tel";
    case "cid:":
      return "cid";
    case "data:":
      // Matched against the raw string: if whitespace tricks make the raw
      // form differ from what the parser saw, refusing is the correct
      // answer — a legitimate data: image has no whitespace in its head.
      return DATA_IMAGE_PATTERN.test(raw.trim()) ? "data-image" : "refused";
    default:
      return "refused";
  }
}

/** The canonical form of a remote URL — the exact string the proxy signs.
 * Canonicalizing through URL.href makes "same image" a syntactic identity
 * (case of host, default port, percent-encoding) instead of a string match. */
export function canonicalRemoteUrl(raw: string): string | undefined {
  try {
    const parsed = new URL(raw);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return undefined;
    return parsed.href;
  } catch {
    return undefined;
  }
}

/**
 * Inline-CSS properties a style attribute may keep: typography, color,
 * box-model, tables — the visual language of email. Absent and deliberate:
 *
 *   - background-image, list-style-image, content, cursor, mask, filter,
 *     border-image ... every property whose value fetches. (The value
 *     grammar below ALSO refuses url() wholesale, so allowing the shorthand
 *     `background` is safe: its url() form dies on the value check.)
 *   - position, top/right/bottom/left, z-index, transform, inset ... overlay
 *     and scroll-jack primitives.
 *   - behavior, -moz-binding, expression ... legacy code execution.
 *   - animation/transition ... nothing in a mail body needs to move; keyframe
 *     names would dangle anyway with stylesheets stripped.
 */
export const EMAIL_ALLOWED_CSS_PROPERTIES: ReadonlySet<string> = new Set([
  "background", "background-color",
  "border", "border-bottom", "border-bottom-color", "border-bottom-style",
  "border-bottom-width", "border-collapse", "border-color", "border-left",
  "border-left-color", "border-left-style", "border-left-width",
  "border-radius", "border-right", "border-right-color", "border-right-style",
  "border-right-width", "border-spacing", "border-style", "border-top",
  "border-top-color", "border-top-style", "border-top-width", "border-width",
  "box-sizing", "caption-side", "clear", "color", "direction", "display",
  "empty-cells", "float", "font", "font-family", "font-size", "font-style",
  "font-variant", "font-weight", "height", "letter-spacing", "line-height",
  "list-style-position", "list-style-type",
  "margin", "margin-bottom", "margin-left", "margin-right", "margin-top",
  "max-height", "max-width", "min-height", "min-width", "opacity",
  "overflow", "overflow-wrap", "overflow-x", "overflow-y",
  "padding", "padding-bottom", "padding-left", "padding-right", "padding-top",
  "table-layout", "text-align", "text-decoration", "text-decoration-color",
  "text-decoration-line", "text-decoration-style", "text-indent",
  "text-transform", "vertical-align", "visibility", "white-space", "width",
  "word-break", "word-spacing",
]);

/**
 * Functional notations a CSS value may contain. Color functions and the math
 * functions — nothing that names a resource. Every "(" in a value must be
 * introduced by one of these, or the whole declaration is dropped: that one
 * rule refuses url(, expression(, image-set(, attr(, var( (indirection that
 * defeats review), element(, paint( and every notation not yet invented.
 */
export const EMAIL_ALLOWED_CSS_FUNCTIONS: ReadonlySet<string> = new Set([
  "rgb", "rgba", "hsl", "hsla", "calc", "clamp", "min", "max",
]);

/** A hard cap on one style attribute. Real inline mail styles run a few
 * hundred bytes; a multi-kilobyte one is either generated noise or a payload
 * hunting for parser differentials, and neither needs preserving. */
const MAX_STYLE_ATTRIBUTE_LENGTH = 4096;

// eslint-disable-next-line no-control-regex -- matching control characters IS the check: they are the classic scheme/value smuggling vector.
const CONTROL_CHARS = /[\u0000-\u001f\u007f-\u009f]/;

/** Characters that have no place in a CSS value under this policy:
 * quotes and backslashes (escape smuggling — `\75rl(` is `url(`),
 * angle brackets (markup re-entry), and `@` (at-rule smuggling). */
const REFUSED_VALUE_CHARS = /["'<>\\@]/;

const FUNCTION_TOKEN = /([a-zA-Z-]+)\(/g;

/**
 * Filters one style attribute's value down to the policy. Returns the
 * surviving declarations, or "" when nothing survives (the caller then drops
 * the attribute entirely).
 *
 * The grammar is deliberately primitive — split on ";", cut at ":" — and that
 * primitivity is safe ONLY because of what the value refusals exclude: with
 * quotes, backslashes and comments refused, a ";" or ":" cannot be hidden
 * inside a token, so the primitive split and a real CSS tokenizer agree on
 * every value this function accepts. (On values they would disagree about,
 * this function refuses — fail closed, not clever.)
 */
export function sanitizeStyleValue(style: string): string {
  if (style.length > MAX_STYLE_ATTRIBUTE_LENGTH) return "";

  const kept: string[] = [];
  for (const declaration of style.split(";")) {
    const colon = declaration.indexOf(":");
    if (colon <= 0) continue;
    const property = declaration.slice(0, colon).trim().toLowerCase();
    const value = declaration.slice(colon + 1).trim();
    if (property === "" || value === "") continue;
    if (!EMAIL_ALLOWED_CSS_PROPERTIES.has(property)) continue;

    if (CONTROL_CHARS.test(value) || REFUSED_VALUE_CHARS.test(value)) continue;
    if (value.includes("/*") || value.includes("*/")) continue;

    // Every open paren must be introduced by an allowlisted function name,
    // and parens must balance; otherwise the declaration dies whole.
    const opens = (value.match(/\(/g) ?? []).length;
    const closes = (value.match(/\)/g) ?? []).length;
    const tokens = [...value.matchAll(FUNCTION_TOKEN)];
    if (opens !== closes || tokens.length !== opens) continue;
    const allAllowed = tokens.every((match) =>
      EMAIL_ALLOWED_CSS_FUNCTIONS.has((match[1] ?? "").toLowerCase()),
    );
    if (!allAllowed) continue;

    kept.push(`${property}: ${value}`);
  }
  return kept.join("; ");
}
