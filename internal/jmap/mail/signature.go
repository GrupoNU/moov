package mail

import (
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// HTML signature sanitization — the one place in Moov where untrusted HTML is
// cleaned on the way IN rather than on the way out.
//
// # Why this inverts parser.SanitizeHook's rule
//
// internal/parser/hook.go states the project's general rule and its reason:
// the store keeps what the sender sent, and sanitization happens per render,
// "so a sanitizer bug discovered later could not be fixed by re-rendering,
// only by re-fetching every message". That reasoning is about RECEIVED mail —
// bytes Moov did not author, holds a canonical copy of, and only ever
// displays.
//
// htmlSignature is the opposite case on every axis:
//
//  1. It is CONTENT MOOV TRANSMITS. RFC 8621 §6 says a client "SHOULD insert"
//     it into new HTML messages; whatever ends up in the outgoing body is
//     signed by our DKIM key and carries our domain's reputation. There is no
//     "render" step to attach the policy to — once the message is assembled
//     and handed to Postfix it is bytes on the wire, at a recipient we do not
//     control and cannot re-render for.
//  2. There is no canonical upstream to re-fetch from. The user typed it; the
//     database IS the only copy. So the "sanitize late, keep the original"
//     argument buys nothing — there is nothing to preserve fidelity FOR.
//  3. It is served back to the composer, which puts it in a contenteditable
//     body. Storing raw would mean the one string in the system that is both
//     attacker-supplied and destined for an editable DOM is kept in its
//     dangerous form.
//
// So it is sanitized on the way in AND, per the project's defense-in-depth
// posture (ADR-001 §7), the display layers still apply their own passes. This
// is an additional layer, not a replacement for them.
//
// # The threat model
//
// The attacker here already has the account's credentials — Identity/set is
// authenticated, so "an attacker who can set a signature" is "an attacker who
// is the user". That makes self-XSS uninteresting. What is NOT uninteresting:
//
//   - Outbound XSS / phishing at the RECIPIENT. A stored <script>, an
//     onerror= handler, or a javascript: href rides out in every message the
//     account sends. Most receiving webmails sanitize, but sending attack
//     markup under our DKIM signature is how a domain gets blocklisted, and
//     "the recipient's client will probably catch it" is not a security
//     argument.
//   - Reputation and deliverability. Spam filters score <script> and hidden
//     iframes hard. A compromised account that silently appends a redirect to
//     every outgoing message damages the sending domain for every other user
//     on it.
//   - Structural escape. A signature is inserted into an existing <body>
//     (§6: "This text MUST be an HTML snippet to be inserted into the
//     '<body></body>' section"). Unbalanced tags or a stray </body> would let
//     the snippet restructure the surrounding message. Re-serializing through
//     a real parser is what makes that impossible: the output is always a
//     well-formed fragment, whatever the input was.
//   - Exfiltration via CSS and remote refs. Inline style is allowed but
//     url() is not (a "Spy Sheets" vector — ADR-001 §7 strips non-inlined CSS
//     for the same reason), and <style>/<link> never survive.
//
// # The policy: allowlist, never blocklist
//
// Elements and attributes not named below are dropped. A blocklist would have
// to anticipate every dangerous name; an allowlist fails closed on the ones
// nobody thought of. Dropped ELEMENTS keep their children (removing a <div>
// should not delete the text inside it) except for the few whose content is
// itself code or markup — script, style, and friends — which are removed
// whole, content and all.

// signatureAllowedElements is the element allowlist: the markup a signature
// legitimately needs — text structure, emphasis, links, images, tables (still
// the workhorse of email layout), and line breaks.
//
// Conspicuously absent, each for a reason:
//   - script, style, link, meta, base: code, or document-level directives.
//   - iframe, frame, object, embed, applet: nested browsing contexts.
//   - form, input, button, select, textarea: a signature that phishes for
//     credentials in the recipient's reading pane.
//   - svg, math: foreign content, with their own script vectors and their own
//     parser quirks.
//   - html, head, body: a snippet is a fragment (§6); document structure in
//     it can only be an attempt to restructure the containing message.
var signatureAllowedElements = map[atom.Atom]bool{
	atom.A: true, atom.B: true, atom.Big: true, atom.Blockquote: true,
	atom.Br: true, atom.Caption: true, atom.Center: true, atom.Cite: true,
	atom.Code: true, atom.Col: true, atom.Colgroup: true, atom.Dd: true,
	atom.Del: true, atom.Div: true, atom.Dl: true, atom.Dt: true,
	atom.Em: true, atom.Font: true, atom.H1: true, atom.H2: true,
	atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Hr: true, atom.I: true, atom.Img: true, atom.Ins: true,
	atom.Li: true, atom.Ol: true, atom.P: true, atom.Pre: true,
	atom.Q: true, atom.S: true, atom.Small: true, atom.Span: true,
	atom.Strike: true, atom.Strong: true, atom.Sub: true, atom.Sup: true,
	atom.Table: true, atom.Tbody: true, atom.Td: true, atom.Tfoot: true,
	atom.Th: true, atom.Thead: true, atom.Tr: true, atom.U: true,
	atom.Ul: true,
}

// signatureVoidElements never get a closing tag when re-serialized.
var signatureVoidElements = map[atom.Atom]bool{
	atom.Br: true, atom.Hr: true, atom.Img: true, atom.Col: true,
}

// signatureStrippedSubtrees are elements removed WITH their content, because
// their content is not text to be preserved — it is code (script), styling
// that can exfiltrate (style), or markup for a context we refuse entirely.
var signatureStrippedSubtrees = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Iframe: true, atom.Object: true,
	atom.Embed: true, atom.Applet: true, atom.Form: true, atom.Input: true,
	atom.Button: true, atom.Select: true, atom.Textarea: true, atom.Option: true,
	atom.Link: true, atom.Meta: true, atom.Base: true, atom.Title: true,
	atom.Noscript: true, atom.Template: true, atom.Frame: true, atom.Frameset: true,
	atom.Svg: true, atom.Math: true,
}

// signatureGlobalAttributes may appear on any allowed element.
//
// style is allowed because a signature without colors and spacing is not a
// signature — but its VALUE is filtered (sanitizeStyle), since CSS can fetch
// remote resources and, historically, execute.
//
// Everything event-shaped is excluded by construction: this is an allowlist,
// so no on* attribute can appear. Also absent: id and class (a signature has
// no business addressing the containing document's stylesheet), and every
// data-*/aria-* (no consumer, and one less surface).
var signatureGlobalAttributes = map[string]bool{
	"style": true, "title": true, "dir": true, "lang": true,
}

// signatureElementAttributes are the per-element additions.
var signatureElementAttributes = map[atom.Atom]map[string]bool{
	atom.A:          {"href": true, "target": true, "rel": true},
	atom.Img:        {"src": true, "alt": true, "width": true, "height": true},
	atom.Table:      {"width": true, "border": true, "cellpadding": true, "cellspacing": true, "align": true},
	atom.Td:         {"width": true, "height": true, "align": true, "valign": true, "colspan": true, "rowspan": true, "bgcolor": true},
	atom.Th:         {"width": true, "height": true, "align": true, "valign": true, "colspan": true, "rowspan": true, "bgcolor": true},
	atom.Tr:         {"align": true, "valign": true, "bgcolor": true},
	atom.Col:        {"width": true, "span": true, "align": true},
	atom.Colgroup:   {"width": true, "span": true, "align": true},
	atom.Div:        {"align": true},
	atom.P:          {"align": true},
	atom.Font:       {"color": true, "face": true, "size": true},
	atom.Ol:         {"start": true, "type": true},
	atom.Blockquote: {"cite": true},
}

// maxSignatureBytes caps a stored signature.
//
// Not a spec value — RFC 8621 §6 sets no limit — but an unbounded string that
// is prepended to every outgoing message is a self-inflicted denial of
// service: it multiplies into every send, every \Sent copy, and every
// Identity/get. 64 KiB is far beyond any real signature (the largest
// image-free corporate signatures run a few KiB) while staying well under the
// message size ceiling.
const maxSignatureBytes = 64 * 1024

// sanitizeHTMLSignature returns markup safe to embed in an outgoing message.
//
// It parses the input as an HTML FRAGMENT in body context — the context §6
// specifies — walks the resulting tree applying the allowlist, and
// re-serializes. Parsing rather than regexing is the whole point: the input
// is normalized by a real HTML5 parser, so the classic filter bypasses
// (mismatched quotes, `<scr<script>ipt>`, mangled entities, unclosed tags)
// are resolved into a tree BEFORE any decision is made, and a decision on a
// tree cannot be tricked by lexical tricks.
func sanitizeHTMLSignature(in string) string {
	if in == "" {
		return ""
	}

	// The check is `in == ""` and NOT `strings.TrimSpace(in) == ""`, which is
	// a fix the fuzzer earned: TrimSpace does not treat NUL as space, but the
	// HTML parser rewrites NUL to U+FFFD, so " \x00" passed the trim guard,
	// walked, and came back as " ". Feeding that " " in again then hit the
	// trim guard and returned "" — sanitize(sanitize(x)) != sanitize(x).
	//
	// Idempotence is not cosmetic here. The stored value is re-sanitized on
	// every save, and the /set response reports htmlSignature back to the
	// client only when sanitization CHANGED it (identity.go). A non-idempotent
	// sanitizer would make a signature that never stops "changing", so every
	// save would look like a server-side rewrite of the value just written.

	// Parse in <body> context: §6 says the snippet goes "into the
	// '<body></body>' section", so that is the context whose parsing rules
	// apply. A <td> outside a table, for example, is handled the same way the
	// recipient's parser will handle it.
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(in), body)
	if err != nil {
		// The fragment parser is extremely permissive; a failure means the
		// input is not usable as HTML at all. Failing closed (dropping it) is
		// the only safe answer — a partial parse must never be forwarded.
		return ""
	}

	var b strings.Builder
	for _, n := range nodes {
		writeSanitized(&b, n)
	}
	out := b.String()
	if len(out) > maxSignatureBytes {
		// Truncating HTML would produce a broken fragment, so an oversize
		// signature is refused whole. The handler checks the INPUT size first
		// and reports it properly (invalidProperties); reaching this line
		// means sanitization expanded the markup past the cap, which is not a
		// user-actionable condition — dropping is the safe answer.
		return ""
	}
	return out
}

// writeSanitized renders one node and its descendants under the policy.
func writeSanitized(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		// EscapeString handles <, >, &, ' and " — so text can never re-enter
		// as markup no matter what the user typed.
		b.WriteString(html.EscapeString(n.Data))
		return

	case html.ElementNode:
		if signatureStrippedSubtrees[n.DataAtom] {
			// Removed with its content: see signatureStrippedSubtrees.
			return
		}
		if !signatureAllowedElements[n.DataAtom] {
			// Unknown or disallowed element: unwrap it. The text a user wrote
			// inside a <section> is still their text; only the tag goes.
			writeChildren(b, n)
			return
		}
		writeElement(b, n)
		return

	case html.DocumentNode:
		writeChildren(b, n)
		return

	default:
		// CommentNode, DoctypeNode and the rest are dropped silently. A
		// comment is a known smuggling vector (conditional comments were
		// parsed as markup by old clients) and carries nothing a signature
		// needs.
		return
	}
}

func writeChildren(b *strings.Builder, n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		writeSanitized(b, c)
	}
}

// writeElement renders an allowed element with its surviving attributes.
func writeElement(b *strings.Builder, n *html.Node) {
	name := n.Data
	b.WriteString("<")
	b.WriteString(name)

	// Attributes are emitted in sorted order so the stored form is a
	// deterministic function of the input. That is what lets a test assert on
	// the exact output, and what keeps an unchanged signature from looking
	// changed (and bumping the state) on a re-save.
	attrs := make([]html.Attribute, 0, len(n.Attr))
	for _, a := range n.Attr {
		// Namespaced attributes (xlink:href and friends) are dropped: they
		// only arise in foreign content, which is refused entirely, and
		// xlink:href is a script vector in its own right.
		if a.Namespace != "" {
			continue
		}
		key := strings.ToLower(a.Key)
		if !signatureGlobalAttributes[key] && !signatureElementAttributes[n.DataAtom][key] {
			continue
		}
		val, ok := sanitizeAttributeValue(n.DataAtom, key, a.Val)
		if !ok {
			continue
		}
		attrs = append(attrs, html.Attribute{Key: key, Val: val})
	}
	sort.Slice(attrs, func(i, j int) bool { return attrs[i].Key < attrs[j].Key })

	for _, a := range attrs {
		b.WriteString(" ")
		b.WriteString(a.Key)
		b.WriteString(`="`)
		b.WriteString(html.EscapeString(a.Val))
		b.WriteString(`"`)
	}

	if signatureVoidElements[n.DataAtom] {
		b.WriteString(">")
		return
	}
	b.WriteString(">")
	writeChildren(b, n)
	b.WriteString("</")
	b.WriteString(name)
	b.WriteString(">")
}

// sanitizeAttributeValue filters an allowed attribute's VALUE. Being on the
// allowlist earns an attribute a check, not a pass.
func sanitizeAttributeValue(elem atom.Atom, key, val string) (string, bool) {
	switch key {
	case "href", "src", "cite":
		return sanitizeURL(key, val)
	case "style":
		s := sanitizeStyle(val)
		return s, s != ""
	case "target":
		// Only _blank survives, and writeElement's caller pairs it with rel
		// below. Other targets (_parent, _top, a frame name) only mean
		// something in a framed context, which a mail body never legitimately
		// has.
		if strings.EqualFold(strings.TrimSpace(val), "_blank") {
			return "_blank", true
		}
		return "", false
	case "rel":
		// Kept verbatim only for the values that ADD safety.
		var keep []string
		for _, tok := range strings.Fields(strings.ToLower(val)) {
			switch tok {
			case "noopener", "noreferrer", "nofollow":
				keep = append(keep, tok)
			}
		}
		if len(keep) == 0 {
			return "", false
		}
		return strings.Join(keep, " "), true
	default:
		// Presentational attributes: reject anything with markup-significant
		// or control characters. They are meant to hold short tokens
		// (numbers, colors, alignments), so this costs nothing legitimate.
		if strings.ContainsAny(val, "<>\"'") || strings.ContainsFunc(val, isControl) {
			return "", false
		}
		return val, true
	}
}

// isControl reports the C0/C1 control characters, which have no place in an
// attribute value and are the classic way to break a naive scheme check
// ("java\x00script:").
func isControl(r rune) bool {
	return r < 0x20 || (r >= 0x7f && r <= 0x9f)
}

// sanitizeURL applies the URL scheme allowlist.
//
// The check is on the DECODED, control-stripped, lowercased prefix, because
// every historical bypass of a scheme check has been a lexical trick on the
// scheme itself: "java\tscript:", "JaVaScRiPt:", "java&#x09;script:" (already
// decoded by the parser at this point), leading whitespace, embedded NULs.
// Stripping the characters that cannot legally appear in a scheme first, then
// comparing, removes the whole class.
//
// Allowed: http, https, mailto, tel. Notably NOT allowed:
//   - javascript: — script execution.
//   - data: — a data:text/html URL is a same-document script vector, and a
//     data: image in a signature would inflate every outgoing message.
//   - cid: — a Content-ID reference to a MIME part the signature does not
//     own; it would resolve against whatever the containing message happens
//     to carry.
//   - file:, about:, blob:, vbscript: — local or context-dependent.
//
// A RELATIVE URL is refused too, and that is deliberate rather than an
// oversight: in a mail body there is no base document to resolve against, so
// a relative href resolves at the recipient against their webmail's own
// origin — which is exactly the confused-deputy shape to avoid.
func sanitizeURL(key, val string) (string, bool) {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return "", false
	}

	// The scheme, as a parser would read it: everything before the first
	// colon, with control and whitespace characters removed.
	scheme, rest, hasColon := strings.Cut(trimmed, ":")
	if !hasColon {
		return "", false // relative — see above.
	}
	var clean strings.Builder
	for _, r := range scheme {
		if isControl(r) || r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		clean.WriteRune(r)
	}
	switch strings.ToLower(clean.String()) {
	case "http", "https", "mailto", "tel":
	default:
		return "", false
	}

	// A URL containing raw control characters is refused outright rather than
	// cleaned: it cannot have been typed on purpose, and "clean it up and use
	// it" is how a sanitizer ends up constructing a URL the author never
	// wrote.
	if strings.ContainsFunc(trimmed, isControl) {
		return "", false
	}
	_ = rest
	_ = key
	return trimmed, true
}

// sanitizeStyle filters an inline style attribute to a property allowlist.
//
// Inline CSS is allowed because signatures are styled text, but CSS is a
// capability surface, not just presentation:
//
//   - url() fetches a remote resource, which is a tracking pixel by another
//     name and, in a signature, one that fires in the SENDER's own Sent copy
//     and in every recipient's client. ADR-001 §7 already strips non-inlined
//     CSS for the "Spy Sheets" class of attack; this is the same decision one
//     level down.
//   - expression() executed script in old IE, and behavior:/-moz-binding
//     bound code to elements. Long dead, but the allowlist excludes them for
//     free.
//   - position/z-index/opacity let a snippet cover the surrounding message —
//     a signature that renders on top of the quoted thread is a spoofing
//     primitive.
//
// So: an allowlist of visual properties, and any declaration whose value
// contains a parenthesis, a semicolon-smuggled escape, or a backslash escape
// is dropped whole.
func sanitizeStyle(val string) string {
	var keep []string
	for _, decl := range strings.Split(val, ";") {
		prop, value, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		prop = strings.ToLower(strings.TrimSpace(prop))
		value = strings.TrimSpace(value)
		if prop == "" || value == "" || !signatureAllowedStyleProps[prop] {
			continue
		}
		// "(" catches url(, expression(, and every other functional notation
		// in one rule — none of the allowed properties needs one except
		// colors, which are accepted in their hex/keyword forms. "\\" catches
		// CSS escape sequences, the standard way to hide a keyword from a
		// substring check.
		if strings.ContainsAny(value, "(){}\\\"'<>") || strings.ContainsFunc(value, isControl) {
			continue
		}
		keep = append(keep, prop+": "+value)
	}
	if len(keep) == 0 {
		return ""
	}
	return strings.Join(keep, "; ")
}

// signatureAllowedStyleProps is the inline-CSS property allowlist: typography,
// color, spacing and borders — what styling a signature actually requires.
//
// Excluded and worth naming: position, display, float, z-index, opacity,
// visibility, transform, content, behavior, filter, background-image — the
// properties that let a snippet leave its own box, hide itself, or fetch.
var signatureAllowedStyleProps = map[string]bool{
	"color": true, "background-color": true,
	"font": true, "font-family": true, "font-size": true, "font-style": true,
	"font-weight": true, "font-variant": true,
	"line-height": true, "letter-spacing": true, "word-spacing": true,
	"text-align": true, "text-decoration": true, "text-indent": true,
	"text-transform": true, "vertical-align": true, "white-space": true,
	"margin": true, "margin-top": true, "margin-bottom": true,
	"margin-left": true, "margin-right": true,
	"padding": true, "padding-top": true, "padding-bottom": true,
	"padding-left": true, "padding-right": true,
	"border": true, "border-top": true, "border-bottom": true,
	"border-left": true, "border-right": true, "border-color": true,
	"border-style": true, "border-width": true, "border-collapse": true,
	"border-radius": true,
	"width":         true, "height": true, "max-width": true, "min-width": true,
}
