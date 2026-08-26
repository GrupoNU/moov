/**
 * The client-side sanitizer — layer 2 of W-A4's three (server policy →
 * DOMPurify here → sandboxed iframe in srcdoc.ts).
 *
 * DOMPurify does the heavy lifting this project must not hand-roll: it
 * parses hostile markup with the browser's own parser into an inert tree
 * (no script execution, no resource fetches), applies the allowlists from
 * policy.ts, and — critically — carries a decade of accumulated defenses
 * against mutation XSS, the class where SERIALIZING a "clean" tree and
 * re-parsing it yields different, dirty markup (namespace confusion,
 * annotation-xml, noscript differentials). Rolling our own walker would
 * discard exactly that hard-won knowledge; our job is the POLICY, and the
 * email-specific value grammar DOMPurify does not know about: URL scheme
 * classes per attribute role, the inline-CSS grammar, forced link hygiene,
 * and the remote-image pipeline. Those run in one afterSanitizeAttributes
 * hook below.
 *
 * # The image pipeline, precisely
 *
 * A remote (http/https) <img src> is NEVER emitted as-authored. Pass one
 * (blocked, the default) strips the src and counts it; the collected URLs
 * feed the banner ("N images hidden") and, on the user's explicit opt-in,
 * the signing call. Pass two — always re-run from the ORIGINAL html, never
 * from prior output — rewrites each src to the HMAC-signed, same-origin
 * proxy path the server minted. A direct fetch to the sender's server (the
 * IP/timestamp leak) is unrepresentable in the output: the only remote
 * bytes an <img> can name are `/jmap/imgproxy?...`, and the iframe's CSP
 * (srcdoc.ts) enforces the same statement a second time.
 *
 * `cid:` images (inline MIME parts) are counted and dropped: body parts have
 * no blobId on this server yet (README gap 5), so there is nothing to fetch
 * — and saying "N inline images unavailable" is honest where a broken image
 * icon would look like a bug.
 *
 * # Re-entrancy
 *
 * The hook reads its per-call state through a module-level slot, set for the
 * duration of one synchronous sanitize() call. JavaScript's run-to-completion
 * makes that race-free; the finally block makes it leak-free.
 */

import DOMPurify from "dompurify";

import {
  EMAIL_ALLOWED_ATTRIBUTES,
  EMAIL_ALLOWED_TAGS,
  canonicalRemoteUrl,
  classifyUrl,
  sanitizeStyleValue,
} from "./policy";

export interface SanitizeEmailHtmlOptions {
  /** When false (the default posture), remote image srcs are stripped. */
  readonly allowRemoteImages: boolean;
  /**
   * Maps a canonical remote URL to its signed proxy path. Only consulted
   * when allowRemoteImages is true; an image whose URL the signer refused
   * (no mapping) stays blocked — the state it started in.
   */
  readonly proxiedUrlFor?: (canonicalUrl: string) => string | undefined;
}

export interface SanitizedEmailHtml {
  /** Markup that satisfies policy.ts. Safe to place in the sandboxed iframe —
   * and ONLY there; nothing in this module makes it safe for the app's DOM. */
  readonly html: string;
  /** Canonical remote image URLs found, in document order, deduplicated.
   * These are what the proxy signs. */
  readonly remoteImageUrls: readonly string[];
  /** How many <img> occurrences ended this pass without a src (blocked
   * remote or refused scheme). Drives the "N images hidden" banner. */
  readonly blockedImageCount: number;
  /** How many cid: inline images were dropped as unavailable. */
  readonly droppedInlineImageCount: number;
}

interface HookState {
  options: SanitizeEmailHtmlOptions;
  remote: Set<string>;
  blocked: number;
  droppedInline: number;
}

/**
 * A dedicated DOMPurify instance, so this module's hooks cannot collide with
 * any other future use of the shared default instance.
 */
const purify = DOMPurify(window);

let state: HookState | null = null;

/**
 * The DOMPurify configuration, frozen so a later call cannot drift it.
 *
 * Notes on the non-obvious choices:
 *   - ALLOWED_URI_REGEXP is a coarse pre-filter (DOMPurify applies it to
 *     every URI-typed attribute); the hook below applies the real per-role
 *     policy. Two checks, agreeing, is the point — not redundancy to remove.
 *   - ALLOW_DATA_ATTR false: data-* is a smuggling surface with no consumer
 *     inside the frame.
 *   - KEEP_CONTENT true: an unknown element's TEXT is the sender's text;
 *     only the tag dies. DOMPurify's default forbid-contents list still
 *     removes script/style/svg/math subtrees whole, content included.
 *   - SANITIZE_DOM true (default) keeps DOMPurify's own clobbering guards on
 *     top of ours (we additionally drop id/name entirely via the allowlist).
 */
const PURIFY_CONFIG = Object.freeze({
  ALLOWED_TAGS: [...EMAIL_ALLOWED_TAGS],
  ALLOWED_ATTR: [...EMAIL_ALLOWED_ATTRIBUTES],
  ALLOW_DATA_ATTR: false,
  ALLOW_ARIA_ATTR: false,
  ALLOWED_URI_REGEXP: /^(?:https?|mailto|tel|cid|data):/i,
  KEEP_CONTENT: true,
  SANITIZE_DOM: true,
});

purify.addHook("afterSanitizeAttributes", (node) => {
  const current = state;
  if (current === null) return;
  if (!(node instanceof Element)) return;

  applyStylePolicy(node);

  const tag = node.tagName.toUpperCase();
  if (tag === "A") {
    applyLinkPolicy(node);
  } else if (tag === "IMG") {
    applyImagePolicy(node, current);
  } else if (node.hasAttribute("src")) {
    // src on anything that is not an <img> has no sanctioned meaning here.
    node.removeAttribute("src");
  }

  // cite= (blockquote/q/del/ins) is semantic, never fetched — but there is
  // no reason to store a non-web URL in it.
  if (node.hasAttribute("cite") && classifyUrl(node.getAttribute("cite") ?? "") !== "web") {
    node.removeAttribute("cite");
  }
});

function applyStylePolicy(node: Element): void {
  if (!node.hasAttribute("style")) return;
  const cleaned = sanitizeStyleValue(node.getAttribute("style") ?? "");
  if (cleaned === "") {
    node.removeAttribute("style");
  } else {
    node.setAttribute("style", cleaned);
  }
}

/**
 * Link hygiene: keep only schemes a click can safely mean, force every web
 * link into a NEW tab with no opener and no referrer. A link that survives
 * can therefore never navigate the iframe's parent (no allow-top-navigation
 * in the sandbox either — layer 3 states this twice), never reach back
 * through window.opener, and never leak the mailbox's URL in a Referer.
 */
function applyLinkPolicy(node: Element): void {
  const href = node.getAttribute("href");
  if (href === null) {
    node.removeAttribute("target");
    return;
  }
  const cls = classifyUrl(href);
  switch (cls) {
    case "web":
      node.setAttribute("target", "_blank");
      node.setAttribute("rel", "noopener noreferrer");
      return;
    case "mailto":
    case "tel":
      // Handled by the OS/registered app, not a browsing context; a target
      // would only create an about:blank tab on some platforms.
      node.removeAttribute("target");
      node.setAttribute("rel", "noopener noreferrer");
      return;
    default:
      // Includes relative URLs and in-page fragments: with target forced to
      // _blank a fragment could not scroll anyway, and a relative URL would
      // resolve against the app. The text of the link survives; only its
      // destination is gone.
      node.removeAttribute("href");
      node.removeAttribute("target");
      node.removeAttribute("rel");
  }
}

function applyImagePolicy(node: Element, current: HookState): void {
  const src = node.getAttribute("src");
  if (src === null) return;

  const cls = classifyUrl(src);
  if (cls === "data-image") return; // inline bytes: no fetch, no tracking.

  if (cls === "cid") {
    node.removeAttribute("src");
    current.droppedInline += 1;
    return;
  }

  if (cls === "web") {
    const canonical = canonicalRemoteUrl(src);
    if (canonical !== undefined) {
      current.remote.add(canonical);
      if (current.options.allowRemoteImages) {
        const proxied = current.options.proxiedUrlFor?.(canonical) ?? "";
        if (proxied.startsWith("/jmap/imgproxy?")) {
          node.setAttribute("src", proxied);
          return;
        }
      }
    }
    node.removeAttribute("src");
    current.blocked += 1;
    return;
  }

  // refused schemes
  node.removeAttribute("src");
  current.blocked += 1;
}

/**
 * Sanitizes one message's HTML under the full policy.
 *
 * Never throws for hostile CONTENT — DOMPurify's contract is to return a
 * clean string for arbitrary input. It can still throw for environmental
 * reasons (no DOM), and callers treat any throw as "no safe rendering
 * exists" and fall back to the plain-text alternative.
 */
export function sanitizeEmailHtml(
  html: string,
  options: SanitizeEmailHtmlOptions,
): SanitizedEmailHtml {
  const local: HookState = {
    options,
    remote: new Set<string>(),
    blocked: 0,
    droppedInline: 0,
  };
  state = local;
  try {
    const out = purify.sanitize(html, PURIFY_CONFIG);
    return {
      html: out,
      remoteImageUrls: [...local.remote],
      blockedImageCount: local.blocked,
      droppedInlineImageCount: local.droppedInline,
    };
  } finally {
    state = null;
  }
}
