/**
 * The iframe document — layer 3 of W-A4, the one that holds when 1 and 2
 * fail.
 *
 * The sanitized markup is wrapped in a complete document and handed to an
 * <iframe srcdoc> whose sandbox grants NEITHER allow-scripts NOR
 * allow-same-origin (the pair that together would void the sandbox). The
 * frame therefore runs with an OPAQUE origin: even markup that somehow
 * carried script could not touch the app's DOM, storage, or credentials —
 * and cannot run in the first place, because scripts are sandbox-refused AND
 * CSP-refused.
 *
 * # The CSP, directive by directive
 *
 *   default-src 'none'          — nothing loads unless named below. Fonts,
 *                                 frames, media, XHR/fetch, plugins: refused.
 *   img-src data: <o>/jmap/imgproxy
 *                               — the ONLY reachable network resource is our
 *                                 own HMAC-signed image proxy (plus inline
 *                                 data: rasters, which involve no network).
 *                                 This is the second, independent statement
 *                                 of the image pipeline: even if a sanitizer
 *                                 bypass emitted <img src=https://evil/>,
 *                                 the fetch dies HERE. It also kills the
 *                                 network half of CSS-only exfiltration
 *                                 ("Spy Sheets"): a smuggled url() can only
 *                                 point at a proxy path whose HMAC the
 *                                 attacker cannot mint per probed value.
 *   style-src 'unsafe-inline'   — inline styles only: OUR base stylesheet
 *                                 and the filtered style attributes. No
 *                                 external sheets — @import has nowhere
 *                                 allowed to go.
 *   form-action 'none'          — form submission targets (default-src does
 *                                 NOT cover this directive) — a phishing
 *                                 form that survived sanitization still has
 *                                 nowhere to submit.
 *   base-uri 'none'             — a smuggled <base> cannot rebase anything.
 *
 * Delivered as a <meta> in the document's head, before any content — the
 * only delivery a srcdoc document has. We author the whole head, so no
 * hostile byte precedes it.
 *
 * # Why the body renders on white in both app themes
 *
 * Mail HTML is authored against a white canvas; "dark-moding" it means
 * rewriting the sender's colors, a heuristic (Gmail's) with its own long bug
 * tail and — worse here — one that would have to run inside the sanitizer's
 * threat surface. Fastmail renders mail on white in dark mode; Moov does the
 * same, states `color-scheme: light` so form-free UA widgets agree, and the
 * surrounding pane provides the theme continuity.
 */

/** True for the "https://host[:port]" shapes acceptable as the app origin in
 * the CSP. Anything else (jsdom's defaults included) fails closed to a CSP
 * without a network img-src — images stay blocked, nothing breaks. */
function isUsableOrigin(origin: string): boolean {
  if (origin === "") return false;
  let parsed: URL;
  try {
    parsed = new URL(origin);
  } catch {
    return false;
  }
  return (
    (parsed.protocol === "https:" || parsed.protocol === "http:") &&
    parsed.origin === origin
  );
}

/** The base stylesheet of the message document: readable defaults that a
 * message's own inline styles may override. Images are capped to the frame's
 * width; everything else may be as wide as the sender made it — WIDE CONTENT
 * SCROLLS INSIDE THE FRAME (the frame's own scrollbars), never the app. */
const BASE_STYLES = `
  :root { color-scheme: light; }
  html, body { margin: 0; padding: 0; }
  body {
    padding: 4px 2px 16px;
    background: #ffffff;
    color: #1f2328;
    font-family: -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    font-size: 15px;
    line-height: 1.55;
    overflow-wrap: break-word;
  }
  img { max-width: 100%; height: auto; border: 0; }
  img:not([src]) {
    min-width: 24px;
    min-height: 20px;
    background: #f2f4f7;
    border: 1px dashed #c6cdd5;
    border-radius: 3px;
  }
  a { color: #1857b3; }
  blockquote {
    margin: 8px 0 8px 2px;
    padding-left: 12px;
    border-left: 3px solid #d0d7de;
    color: #57606a;
  }
  pre { white-space: pre-wrap; overflow-wrap: break-word; }
  hr { border: 0; border-top: 1px solid #d8dee4; }
`;

/**
 * How a quoted tail is rendered inside the frame (L3 epic E1).
 *
 * The frame has no script and no same-origin access, so the tail cannot be
 * toggled from inside it or reached from outside it. What CAN be done without
 * granting a single new capability is to build a DIFFERENT document: the tail
 * is either present in the body or absent from it, and the parent re-renders
 * the srcdoc when the user presses the toggle. The button itself lives in the
 * APP's chrome, above the frame, where it is a real focusable control under
 * the app's own CSS and keyboard handling.
 *
 * The wrapper below is markup THIS FUNCTION authors — trusted by construction,
 * appended after sanitization, never fed back through a parser. The sanitized
 * halves are embedded verbatim, exactly as the single-argument form embeds the
 * whole string.
 */
export interface SrcDocOptions {
  /**
   * The quoted tail, already split off the sanitized markup by
   * `mail/html/quotedTail.ts`. Concatenating `sanitizedHtml + quotedHtml`
   * reproduces the sanitizer's output byte for byte.
   */
  readonly quotedHtml?: string;
  /** Whether the tail is rendered. False hides it by simply not emitting it. */
  readonly showQuoted?: boolean;
}

/**
 * The quoted tail's own styling: Gmail's grey, indented treatment, so a
 * revealed quote reads as quoted material rather than as more of the reply.
 *
 * Emitted only when there IS a tail, so a message without one carries no extra
 * bytes and no extra selector.
 */
const QUOTED_STYLES = `
  .moov-quoted {
    margin-top: 12px;
    padding-top: 8px;
    border-top: 1px solid #e4e8ec;
    color: #57606a;
  }
`;

/**
 * Builds the complete srcdoc document around sanitized markup.
 *
 * `sanitizedHtml` MUST be the output of sanitizeEmailHtml — this function
 * adds isolation, not sanitization. `appOrigin` is the app's own origin
 * (window.location.origin), which the CSP needs spelled out because a
 * sandboxed srcdoc document has an opaque origin: 'self' would match
 * nothing.
 *
 * `options.quotedHtml` is the trimmed tail (canon §2.1). When it is present
 * and `showQuoted` is false, the tail is simply NOT EMITTED — hiding it with
 * CSS would still ship the sender's quoted bytes into the document, where a
 * "select all, copy" would silently pick up text the reader was told was
 * hidden. Not emitting it is both the smaller document and the honest one.
 */
export function buildSrcDoc(
  sanitizedHtml: string,
  appOrigin: string,
  options: SrcDocOptions = {},
): string {
  const imgSources = isUsableOrigin(appOrigin)
    ? `data: ${appOrigin}/jmap/imgproxy`
    : "data:";
  const csp = [
    "default-src 'none'",
    `img-src ${imgSources}`,
    "style-src 'unsafe-inline'",
    "form-action 'none'",
    "base-uri 'none'",
  ].join("; ");

  /*
   * The tail is emitted only when it is BOTH present and shown — and its
   * stylesheet rides with it. A hidden quote leaves no trace in the document:
   * not the markup, not the selector that would style it. That is what makes
   * "hidden" mean hidden rather than "present but transparent", which a
   * select-all inside the frame would cheerfully copy.
   */
  const quoted = options.quotedHtml ?? "";
  const showQuoted = quoted !== "" && options.showQuoted === true;

  return (
    "<!doctype html><html><head>" +
    '<meta charset="utf-8">' +
    `<meta http-equiv="Content-Security-Policy" content="${csp}">` +
    `<style>${BASE_STYLES}${showQuoted ? QUOTED_STYLES : ""}</style>` +
    "</head><body>" +
    sanitizedHtml +
    (showQuoted ? `<div class="moov-quoted">${quoted}</div>` : "") +
    "</body></html>"
  );
}

/** The sandbox grants for the message iframe, exported so a test can pin
 * them: popups so a link can open its new tab, escaping the sandbox so the
 * OPENED page runs normally (it is someone's real site, not mail content).
 * Absent and load-bearing: allow-scripts, allow-same-origin,
 * allow-top-navigation, allow-forms, allow-modals, allow-downloads. */
export const MESSAGE_SANDBOX = "allow-popups allow-popups-to-escape-sandbox";
