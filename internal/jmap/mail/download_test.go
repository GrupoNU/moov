package mail

import (
	"strings"
	"testing"
)

// The download safety rules. Each case names the attack it prevents, because a
// future reader deciding to "simplify" this table needs to know what it costs.

// The invisible characters the sanitizer must remove, named so the test table
// stays readable and so this file contains no literal bidi control.
const (
	// rlo is U+202E RIGHT-TO-LEFT OVERRIDE.
	rlo = "\u202e"
	// zwsp is U+200B ZERO WIDTH SPACE.
	zwsp = "\u200b"
)

func TestDownloadHeadersAllowlist(t *testing.T) {
	cases := []struct {
		name            string
		requestedType   string
		wantType        string
		wantDisposition string
	}{
		{"plain text is inline", "text/plain", "text/plain", "inline"},
		{"png is inline", "image/png", "image/png", "inline"},
		{"jpeg is inline", "image/jpeg", "image/jpeg", "inline"},
		{"gif is inline", "image/gif", "image/gif", "inline"},
		{"pdf is inline", "application/pdf", "application/pdf", "inline"},

		// THE case: HTML served inline in the API origin is stored XSS with
		// the user's session attached.
		{"html downloads", "text/html", DefaultDownloadType, "attachment"},
		// SVG carries <script>; it is not an image for this purpose.
		{"svg downloads", "image/svg+xml", DefaultDownloadType, "attachment"},
		{"javascript downloads", "text/javascript", DefaultDownloadType, "attachment"},
		{"executable downloads", "application/x-msdownload", DefaultDownloadType, "attachment"},
		{"xml downloads", "application/xml", DefaultDownloadType, "attachment"},
		{"no type downloads", "", DefaultDownloadType, "attachment"},
		{"garbage type downloads", "not a media type", DefaultDownloadType, "attachment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ct, disp := DownloadHeaders(tc.requestedType, "file.bin")
			if ct != tc.wantType {
				t.Errorf("Content-Type = %q, want %q", ct, tc.wantType)
			}
			if !strings.HasPrefix(disp, tc.wantDisposition) {
				t.Errorf("Content-Disposition = %q, want %s", disp, tc.wantDisposition)
			}
		})
	}
}

// A charset parameter must not survive: "text/plain; charset=utf-7" is the
// classic way to smuggle markup past a type check.
func TestDownloadHeadersDropMediaTypeParameters(t *testing.T) {
	ct, _ := DownloadHeaders("text/plain; charset=utf-7", "a.txt")
	if ct != "text/plain" {
		t.Errorf("Content-Type = %q, want the bare type with no parameters", ct)
	}
	if strings.Contains(ct, "utf-7") {
		t.Error("a client-supplied charset reached the response header")
	}
}

func TestDownloadHeadersUppercaseTypeIsNormalized(t *testing.T) {
	ct, disp := DownloadHeaders("IMAGE/PNG", "x.png")
	if ct != "image/png" || !strings.HasPrefix(disp, "inline") {
		t.Errorf("got (%q, %q); the allowlist must be case-insensitive", ct, disp)
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain name survives", "invoice.pdf", "invoice.pdf"},
		{"unicode survives", "factura-año.pdf", "factura-año.pdf"},
		{"japanese survives", "請求書.pdf", "請求書.pdf"},

		// Path traversal — corpus case structural-013.
		{"traversal is stripped", "../../etc/passwd", "passwd"},
		{"absolute path is stripped", "/etc/shadow", "shadow"},

		// A Windows-style path loses its separators rather than being split on
		// them: the backslash is deleted (it is the quoted-string escape
		// character and must never reach the header), which flattens the path
		// into one harmless name. No directory survives, which is the property
		// that matters; see the comment in SanitizeFilename for why deleting
		// beats splitting here.
		{"windows path is flattened", `..\..\windows\system32\evil.dll`, "....windowssystem32evil.dll"},
		{"backslash in a name is removed", `evil\.txt`, "evil.txt"},

		// Header injection.
		{"CRLF is removed", "a\r\nX-Evil: 1.txt", "aX-Evil: 1.txt"},
		{"newline is removed", "a\nb.txt", "ab.txt"},

		// Quoted-string escape.
		{"quotes are removed", `evil".txt`, "evil.txt"},

		// Display spoofing. U+202E (RIGHT-TO-LEFT OVERRIDE) makes a file named
		// "evil<RLO>txt.exe" render as "evilexe.txt" in most UIs, which is how
		// an executable attachment passes for a text file.
		//
		// The characters are built from escapes rather than written literally,
		// so that this source line does not itself reorder in an editor — the
		// exact confusion the test is about.
		{"RTL override is removed", "evil" + rlo + "txt.exe", "eviltxt.exe"},
		{"zero width is removed", "in" + zwsp + "voice.pdf", "invoice.pdf"},

		// Degenerate input.
		{"NUL is removed", "a\x00b.txt", "ab.txt"},
		{"empty stays empty", "", ""},
		{"dots only is dropped", "...", ""},
		{"dot is dropped", ".", ""},
		{"parent is dropped", "..", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeFilename(tc.in)
			if got != tc.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Whatever the input, the output must be safe to place in a
			// quoted header parameter.
			for _, bad := range []string{"\r", "\n", `"`, `\`, "/", "\x00"} {
				if strings.Contains(got, bad) {
					t.Errorf("output %q still contains %q", got, bad)
				}
			}
		})
	}
}

func TestSanitizeFilenameIsBounded(t *testing.T) {
	long := strings.Repeat("a", 5000) + ".pdf"
	got := SanitizeFilename(long)
	if len(got) > maxFilenameLength {
		t.Errorf("sanitized name is %d octets, want at most %d", len(got), maxFilenameLength)
	}
}

// A long multi-byte name must be cut on a rune boundary, or the header value
// becomes invalid UTF-8.
func TestSanitizeFilenameTruncatesOnRuneBoundary(t *testing.T) {
	got := SanitizeFilename(strings.Repeat("ñ", 200))
	if !isValidUTF8(got) {
		t.Errorf("truncation produced invalid UTF-8: %q", got)
	}
}

func TestDownloadHeadersWithoutNameOmitsFilename(t *testing.T) {
	_, disp := DownloadHeaders("application/zip", "")
	if strings.Contains(disp, "filename") {
		t.Errorf("disposition = %q, want no filename parameter", disp)
	}
	if disp != "attachment" {
		t.Errorf("disposition = %q", disp)
	}
}

// A name that sanitizes away entirely must not leave a dangling parameter.
func TestDownloadHeadersWithUnusableNameOmitsFilename(t *testing.T) {
	_, disp := DownloadHeaders("application/zip", "../..")
	if strings.Contains(disp, `filename=""`) || strings.Contains(disp, "filename") {
		t.Errorf("disposition = %q, want no filename parameter", disp)
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
