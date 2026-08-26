package mail

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// HTML signature sanitization (signature.go).
//
// These are the tests that matter most in this epic: htmlSignature is the one
// string in Moov that is attacker-supplied AND transmitted in outgoing mail
// under our DKIM signature. Every case below is a thing that must not survive
// into a message we send.

func TestSanitizeHTMLSignatureKeepsLegitimateMarkup(t *testing.T) {
	cases := map[string]string{
		"plain text":   "Diego Nannini",
		"bold":         "<b>Diego</b>",
		"line break":   "Diego<br>Grupo NU",
		"link":         `<a href="https://example.com">site</a>`,
		"mailto":       `<a href="mailto:x@example.com">mail</a>`,
		"image":        `<img src="https://example.com/logo.png" alt="logo">`,
		"table":        `<table><tr><td>Diego</td></tr></table>`,
		"inline style": `<span style="color: #333">Diego</span>`,
		"nested":       `<div><p><em>Diego</em> — <strong>NU</strong></p></div>`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out := sanitizeHTMLSignature(in)
			if out == "" {
				t.Fatalf("legitimate signature markup was dropped entirely: %q", in)
			}
			if !strings.Contains(out, "Diego") && !strings.Contains(out, "site") &&
				!strings.Contains(out, "mail") && !strings.Contains(out, "logo") {
				t.Errorf("sanitize(%q) = %q — the content did not survive", in, out)
			}
		})
	}
}

// The core security assertion: a catalog of injection attempts, each of
// which must leave no executable trace.
func TestSanitizeHTMLSignatureStripsScriptVectors(t *testing.T) {
	vectors := map[string]string{
		"script element":        `<script>alert(1)</script>`,
		"script with content":   `hi<script>fetch('//evil')</script>there`,
		"onerror handler":       `<img src=x onerror="alert(1)">`,
		"onload handler":        `<div onload="alert(1)">x</div>`,
		"onclick handler":       `<a href="https://ok.example" onclick="steal()">x</a>`,
		"onmouseover":           `<span onmouseover="alert(1)">x</span>`,
		"javascript href":       `<a href="javascript:alert(1)">x</a>`,
		"javascript uppercase":  `<a href="JaVaScRiPt:alert(1)">x</a>`,
		"javascript tab":        "<a href=\"java\tscript:alert(1)\">x</a>",
		"javascript newline":    "<a href=\"java\nscript:alert(1)\">x</a>",
		"javascript leading ws": `<a href="   javascript:alert(1)">x</a>`,
		"data url html":         `<a href="data:text/html;base64,PHNjcmlwdD4=">x</a>`,
		"data url img":          `<img src="data:text/html,<script>alert(1)</script>">`,
		"vbscript":              `<a href="vbscript:msgbox(1)">x</a>`,
		"iframe":                `<iframe src="https://evil.example"></iframe>`,
		"object":                `<object data="evil.swf"></object>`,
		"embed":                 `<embed src="evil.swf">`,
		"form phishing":         `<form action="https://evil.example"><input name="pw" type="password"></form>`,
		"style element":         `<style>body{background:url(//evil)}</style>`,
		"link stylesheet":       `<link rel="stylesheet" href="//evil/x.css">`,
		"meta refresh":          `<meta http-equiv="refresh" content="0;url=//evil">`,
		"base tag":              `<base href="//evil/">`,
		"svg script":            `<svg><script>alert(1)</script></svg>`,
		"svg onload":            `<svg onload="alert(1)"></svg>`,
		"math":                  `<math><mtext></mtext></math>`,
		"nested obfuscation":    `<scr<script>ipt>alert(1)</script>`,
		"css url exfil":         `<div style="background-image: url('//evil/pixel.png')">x</div>`,
		"css expression":        `<div style="width: expression(alert(1))">x</div>`,
		"css behavior":          `<div style="behavior: url(#default#time2)">x</div>`,
		"position overlay":      `<div style="position: absolute; top: 0; z-index: 9999">x</div>`,
		"comment smuggling":     `<!--[if IE]><script>alert(1)</script><![endif]-->`,
		"unclosed tag escape":   `<div><script>alert(1)`,
		"body escape":           `</body><script>alert(1)</script><body>`,
		"null byte scheme":      "<a href=\"java\x00script:alert(1)\">x</a>",
		"xlink href":            `<a xlink:href="javascript:alert(1)">x</a>`,
	}

	// Substrings that must NEVER appear in sanitized output. Checked
	// case-insensitively, because a browser's parser is.
	forbidden := []string{
		"<script", "javascript:", "vbscript:", "data:text/html",
		"onerror", "onload", "onclick", "onmouseover", "onfocus",
		"<iframe", "<object", "<embed", "<form", "<input",
		"<style", "<link", "<meta", "<base", "<svg", "<math",
		"expression(", "behavior:", "url(", "position:", "z-index",
	}

	for name, in := range vectors {
		t.Run(name, func(t *testing.T) {
			out := strings.ToLower(sanitizeHTMLSignature(in))
			for _, bad := range forbidden {
				if strings.Contains(out, bad) {
					t.Errorf("sanitize(%q) = %q — still contains %q", in, out, bad)
				}
			}
		})
	}
}

func TestSanitizeHTMLSignatureRefusesRelativeAndOpaqueURLs(t *testing.T) {
	// A relative href in a mail body resolves at the RECIPIENT against their
	// webmail's own origin — a confused-deputy shape, so it is refused rather
	// than rewritten.
	for _, in := range []string{
		`<a href="/admin/delete">x</a>`,
		`<a href="../secret">x</a>`,
		`<a href="settings">x</a>`,
		`<img src="/tracker.gif">`,
		`<a href="cid:part1@example">x</a>`,
		`<a href="file:///etc/passwd">x</a>`,
		`<a href="about:blank">x</a>`,
	} {
		out := sanitizeHTMLSignature(in)
		if strings.Contains(out, "href=") || strings.Contains(out, "src=") {
			t.Errorf("sanitize(%q) = %q — kept a non-absolute or opaque URL", in, out)
		}
	}
}

func TestSanitizeHTMLSignatureOutputIsWellFormed(t *testing.T) {
	// §6: the snippet is "inserted into the '<body></body>' section of the
	// HTML", so a fragment that does not balance would restructure the
	// containing message. Re-serializing through the parser guarantees it
	// cannot.
	for _, in := range []string{
		`<div><b>unclosed`,
		`</div></div>stray closers`,
		`<table><td>no row</td>`,
		`<p>a<p>b<p>c`,
	} {
		out := sanitizeHTMLSignature(in)
		if strings.Count(out, "<") != strings.Count(out, ">") {
			t.Errorf("sanitize(%q) = %q — unbalanced angle brackets", in, out)
		}
		// Every opened non-void element is closed.
		for _, tag := range []string{"div", "b", "table", "p"} {
			if strings.Count(out, "<"+tag) != strings.Count(out, "</"+tag+">") {
				t.Errorf("sanitize(%q) = %q — %s tags do not balance", in, out, tag)
			}
		}
	}
}

func TestSanitizeHTMLSignatureUnwrapsUnknownElementsButKeepsText(t *testing.T) {
	// A disallowed element that is not a code container loses its tag, not its
	// content: the user's words are still the user's words.
	out := sanitizeHTMLSignature(`<section><article>Diego Nannini</article></section>`)
	if !strings.Contains(out, "Diego Nannini") {
		t.Errorf("unwrapping an unknown element ate its text: %q", out)
	}
	if strings.Contains(out, "<section") || strings.Contains(out, "<article") {
		t.Errorf("disallowed elements survived: %q", out)
	}

	// A code container loses its content too — that content is not text.
	out = sanitizeHTMLSignature(`<script>var secret = 1;</script>`)
	if strings.Contains(out, "secret") {
		t.Errorf("script content survived as text: %q", out)
	}
}

func TestSanitizeHTMLSignatureEscapesTextSoItCannotReenterAsMarkup(t *testing.T) {
	out := sanitizeHTMLSignature(`a &lt;script&gt;alert(1)&lt;/script&gt; b`)
	// The entities decode to text during parsing; re-serializing must escape
	// them again, or a second parse would see a real script element.
	if strings.Contains(out, "<script") {
		t.Fatalf("escaped text re-entered as markup: %q", out)
	}
	if !strings.Contains(out, "&lt;") {
		t.Errorf("text was not re-escaped: %q", out)
	}
	// Idempotent: sanitizing the output again must not change it.
	if again := sanitizeHTMLSignature(out); again != out {
		t.Errorf("not idempotent:\n  once = %q\n twice = %q", out, again)
	}
}

func TestSanitizeHTMLSignatureIsDeterministic(t *testing.T) {
	// Attribute order is normalized, so an unchanged signature does not look
	// changed (and bump the state) on a re-save.
	in := `<a title="t" href="https://example.com" style="color: #333">x</a>`
	first := sanitizeHTMLSignature(in)
	for i := 0; i < 20; i++ {
		if got := sanitizeHTMLSignature(in); got != first {
			t.Fatalf("output varies between runs:\n %q\n %q", first, got)
		}
	}
}

func TestSanitizeStyleKeepsVisualPropertiesOnly(t *testing.T) {
	got := sanitizeStyle("color: #333; position: absolute; font-size: 12px; background-image: url(//evil)")
	if !strings.Contains(got, "color") || !strings.Contains(got, "font-size") {
		t.Errorf("visual properties were dropped: %q", got)
	}
	if strings.Contains(got, "position") || strings.Contains(got, "background-image") || strings.Contains(got, "url") {
		t.Errorf("a capability property survived: %q", got)
	}
}

func TestSanitizeHTMLSignatureHandlesEmptyAndHugeInput(t *testing.T) {
	if got := sanitizeHTMLSignature(""); got != "" {
		t.Errorf("empty input = %q", got)
	}
	// Whitespace-only input is PRESERVED, not collapsed to "". That looks like
	// a missed trim and is deliberate: an early `TrimSpace(in) == ""` return is
	// what made the sanitizer non-idempotent (the fuzzer found " \x00", which
	// the HTML parser rewrites to a U+FFFD the trim guard does not see, so the
	// first pass returned " " and a second pass on that " " returned ""). Since
	// sanitize(sanitize(x)) must equal sanitize(x) — every save re-sanitizes,
	// and /set only reports htmlSignature back when sanitization changed it —
	// whitespace has to survive its own round trip. It is harmless content.
	if got := sanitizeHTMLSignature("   \n\t "); got != "   \n\t " {
		t.Errorf("blank input = %q, want it preserved so the sanitizer stays idempotent", got)
	}
	if got := sanitizeHTMLSignature(" \x00"); sanitizeHTMLSignature(got) != got {
		t.Errorf("the NUL case the fuzzer found regressed: sanitize(%q) is not idempotent", " \x00")
	}
	// Deep nesting must not blow the stack or hang — the parser bounds its own
	// tree depth, and the walk follows it.
	deep := strings.Repeat("<div>", 5000) + "x" + strings.Repeat("</div>", 5000)
	out := sanitizeHTMLSignature(deep)
	if !strings.Contains(out, "x") && out != "" {
		t.Errorf("deep nesting produced unexpected output of length %d", len(out))
	}
}

func TestSanitizeHTMLSignatureTargetGetsNoOpener(t *testing.T) {
	// target="_blank" without rel is a reverse-tabnabbing vector in clients
	// that render mail in a real browsing context.
	out := sanitizeHTMLSignature(`<a href="https://example.com" target="_evil">x</a>`)
	if strings.Contains(out, "target=") {
		t.Errorf("a non-_blank target survived: %q", out)
	}
	out = sanitizeHTMLSignature(`<a href="https://example.com" target="_blank" rel="noopener">x</a>`)
	if !strings.Contains(out, `rel="noopener"`) {
		t.Errorf("a safety-adding rel was dropped: %q", out)
	}
}

// sanitizedAttr is one attribute recovered from sanitized output.
type sanitizedAttr struct{ key, val string }

// attributesOf re-parses sanitized output and returns every attribute on every
// element.
//
// It parses rather than pattern-matches on purpose: the assertion is about
// what a BROWSER will see, so the check has to read the output the same way
// one would. A regex over the string would be exactly the kind of lexical
// reasoning the sanitizer exists to avoid relying on.
func attributesOf(fragment string) []sanitizedAttr {
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), body)
	if err != nil {
		return nil
	}
	var out []sanitizedAttr
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				out = append(out, sanitizedAttr{key: strings.ToLower(a.Key), val: a.Val})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return out
}

// FuzzSanitizeHTMLSignature is the property the catalog above cannot cover
// exhaustively: whatever the input, the output must never contain an
// executable construct, and sanitizing twice must equal sanitizing once.
func FuzzSanitizeHTMLSignature(f *testing.F) {
	for _, seed := range []string{
		"", "hi", "<b>x</b>", "<script>alert(1)</script>",
		`<img src=x onerror=alert(1)>`, `<a href="javascript:x">y</a>`,
		"<div style=\"color:red\">x</div>", "<!--c--><p>x", "</body><script>x",
		"<svg/onload=alert(1)>", "<scr<script>ipt>x</script>",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := sanitizeHTMLSignature(in)

		// The property is about MARKUP, not about text. "javascript:0" typed
		// as prose is prose — it is escaped as text and renders as the literal
		// characters, which is harmless and must not be mangled. What must
		// never survive is an executable CONSTRUCT: a dangerous element, an
		// event-handler attribute, or a scheme inside an attribute VALUE.
		// (The fuzzer found this distinction itself, by producing the bare
		// string "jAvAsCript:0" against an earlier, cruder assertion.)
		low := strings.ToLower(out)
		for _, tag := range []string{"<script", "<iframe", "<object", "<embed", "<style", "<link", "<form", "<svg", "<math", "<base", "<meta"} {
			if strings.Contains(low, tag) {
				t.Fatalf("sanitize(%q) = %q — contains the element %q", in, out, tag)
			}
		}
		// Attributes only exist inside tags, so any `name="value"` pair in the
		// output came from writeElement and passed the allowlist. Assert on
		// the two things the allowlist must never let through.
		for _, attr := range attributesOf(out) {
			if strings.HasPrefix(attr.key, "on") {
				t.Fatalf("sanitize(%q) = %q — kept the event handler %q", in, out, attr.key)
			}
			if attr.key == "href" || attr.key == "src" || attr.key == "cite" {
				v := strings.ToLower(strings.TrimSpace(attr.val))
				if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") &&
					!strings.HasPrefix(v, "mailto:") && !strings.HasPrefix(v, "tel:") {
					t.Fatalf("sanitize(%q) = %q — %s=%q is not an allowed scheme", in, out, attr.key, attr.val)
				}
			}
		}

		if again := sanitizeHTMLSignature(out); again != out {
			t.Fatalf("not idempotent for %q:\n once = %q\ntwice = %q", in, out, again)
		}
	})
}
