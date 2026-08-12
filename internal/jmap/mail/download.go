package mail

import (
	"mime"
	"strings"
	"unicode"
)

// Blob download safety: the Content-Type allowlist and filename sanitization
// that internal/jmaphttp's handleDownload applies (L2 §2.3: "Content-Type
// seguro (nunca ejecutable inline: application/octet-stream +
// Content-Disposition salvo tipos allowlisted)").
//
// This logic lives here rather than in the HTTP package because it is a
// product rule about mail content, and because it is worth testing without a
// server. The HTTP handler calls DownloadHeaders and writes what it returns.

// inlineTypes is the allowlist: the only media types this server will serve
// with Content-Disposition: inline, i.e. rendered by the browser in the origin
// of the API.
//
// The list is short on purpose, and everything on it is a format with no
// scripting capability:
//
//   - text/plain — inert. NOT text/html, which is the whole attack: an HTML
//     attachment served inline is a stored XSS in the API's origin, with the
//     user's session attached. HTML bodies reach the client through
//     bodyValues, where the client sanitizes them (ADR §5's three layers,
//     which are the PWA's job in phase 2 — L2 §2.6 is explicit that phase 1
//     serves raw HTML only through the authenticated API).
//   - image/png, image/jpeg, image/gif — raster formats without script.
//     image/svg+xml is deliberately absent: SVG carries <script> and is an XSS
//     vector, so it downloads instead.
//   - application/pdf — browser PDF viewers are sandboxed and users expect
//     inline preview; the risk is accepted and named here rather than
//     inherited silently.
//
// Everything else becomes application/octet-stream with an attachment
// disposition, which no browser will render.
var inlineTypes = map[string]bool{
	"text/plain":      true,
	"image/png":       true,
	"image/jpeg":      true,
	"image/gif":       true,
	"application/pdf": true,
}

// DefaultDownloadType is what a blob is served as when its type is not
// allowlisted.
const DefaultDownloadType = "application/octet-stream"

// DownloadHeaders decides the response headers for a blob download.
//
// requestedType is the client's "type" path/query variable — RFC 8620 §6.2
// says the download URL "MAY include a type... the server MUST NOT use the
// type given by the client without validating it", which is exactly what the
// allowlist below does.
//
// name is the client's requested filename, sanitized before it ever reaches a
// header.
func DownloadHeaders(requestedType, name string) (contentType, disposition string) {
	mediaType := normalizeMediaType(requestedType)

	safeName := SanitizeFilename(name)
	if inlineTypes[mediaType] {
		return mediaType, contentDisposition("inline", safeName)
	}
	return DefaultDownloadType, contentDisposition("attachment", safeName)
}

// normalizeMediaType parses and lowercases a media type, dropping parameters.
//
// Parameters are dropped rather than echoed because a charset the client chose
// is a way to smuggle a rendering decision past the allowlist — "text/plain;
// charset=utf-7" being the classic one, where UTF-7 lets `<script>` survive as
// ASCII that a sniffing browser decodes back into markup.
func normalizeMediaType(v string) string {
	if v == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(v)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(mt))
}

// contentDisposition builds the header value, quoting the filename.
//
// The filename is already sanitized to a conservative character set, so simple
// quoting is sufficient and RFC 5987's filename* encoding is unnecessary — the
// sanitizer has removed everything that would need escaping. A blob with no
// usable name gets the disposition without a filename parameter, which is
// valid and lets the browser fall back to the URL's last segment.
func contentDisposition(kind, name string) string {
	if name == "" {
		return kind
	}
	return kind + `; filename="` + name + `"`
}

// maxFilenameLength bounds the sanitized name. Long enough for any real
// attachment, short enough that a header cannot be inflated by a crafted name.
const maxFilenameLength = 100

// SanitizeFilename makes a client-supplied name safe to put in a header and to
// suggest to a filesystem.
//
// What it defends against, in the order the checks appear:
//
//   - HEADER INJECTION: CR and LF in a filename would end the header and let
//     the caller inject arbitrary response headers. Go's net/http actually
//     rejects such a write, but relying on that means the defense lives in a
//     dependency's error path rather than in this code.
//   - PATH TRAVERSAL: "../../etc/passwd" is reduced to its base name. The
//     corpus has a case for exactly this (structural-013), and the parser
//     deliberately does NOT sanitize filenames — its doc says storage is
//     responsible, and this is storage.
//   - QUOTE ESCAPING: a quote in the name would terminate the quoted string
//     and let the rest be read as further parameters.
//   - CONTROL AND FORMAT CHARACTERS: including the right-to-left override
//     (U+202E), the trick that displays "evil‮txt.exe" as "evilexe.txt".
//
// Unicode letters are otherwise preserved: an attachment named in Spanish or
// Japanese must keep its name, and stripping to ASCII would mangle the
// majority of real mail in the installed base.
func SanitizeFilename(name string) string {
	if name == "" {
		return ""
	}

	// ORDER MATTERS HERE, and the reason is worth stating because the obvious
	// alternative is wrong.
	//
	// A backslash is two different things at once: a path separator on
	// Windows, and a perfectly ordinary character in a filename everywhere
	// else — including inside the quoted-string of a Content-Disposition
	// header, where it is the escape character. It must not survive into the
	// header under any reading, so it is DELETED first, before any path
	// splitting happens.
	//
	// Deleting it first is what makes both readings safe at once:
	//   `..\..\evil.dll` -> `....evil.dll` -> no directory survives, and
	//   `evil\.txt`      -> `evil.txt`     -> the visible name is preserved.
	// Splitting on it instead would resolve the second case to ".txt",
	// discarding the part of the name a user actually reads — which is how a
	// sanitizer ends up hiding what a file is called.
	name = strings.ReplaceAll(name, `\`, "")

	// Now only "/" is a separator, and the last segment is the file name.
	name = lastSegment(name, "/")
	if strings.Trim(name, ".") == "" {
		return ""
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '"' || r == '\\':
			// Would escape the quoted-string context.
			continue
		case r == '\r' || r == '\n':
			continue
		case r == '/' || r == 0:
			continue
		case unicode.IsControl(r):
			continue
		case isFormatOrBidi(r):
			continue
		default:
			b.WriteRune(r)
		}
	}

	out := strings.TrimSpace(b.String())
	// A name that is only dots would still read as a traversal to some
	// consumers, and conveys nothing.
	if strings.Trim(out, ".") == "" {
		return ""
	}
	if len(out) > maxFilenameLength {
		out = truncateUTF8(out, maxFilenameLength)
		out = strings.TrimSpace(out)
	}
	return out
}

// lastSegment returns what follows the final occurrence of sep, or the whole
// string when it does not occur.
//
// A name ENDING in the separator ("evil/") keeps what precedes it rather than
// yielding the empty string, so a trailing slash cannot erase a name.
func lastSegment(s, sep string) string {
	trimmed := strings.TrimRight(s, sep)
	if trimmed == "" {
		return s
	}
	if i := strings.LastIndex(trimmed, sep); i >= 0 {
		return trimmed[i+len(sep):]
	}
	return trimmed
}

// isFormatOrBidi reports whether a rune is an invisible formatting or
// bidirectional-override character — the family that makes a filename display
// as something other than what it is.
func isFormatOrBidi(r rune) bool {
	if unicode.Is(unicode.Cf, r) {
		return true
	}
	// The explicit bidi controls, listed rather than assumed to be in Cf, and
	// written as escapes because the literal characters would reorder this
	// source line in any editor that renders them.
	switch r {
	case '\u202a', '\u202b', '\u202c', '\u202d', '\u202e', // LRE RLE PDF LRO RLO
		'\u2066', '\u2067', '\u2068', '\u2069', // LRI RLI FSI PDI
		'\u200e', '\u200f': // LRM RLM
		return true
	}
	return false
}
