// Package branding owns the on-disk format of a host's brand and the ONE
// writer of it, shared by `moovctl branding` and the authenticated brand
// administration API in internal/jmaphttp.
//
// # Why a package of its own
//
// Until the admin API existed, the CLI was the only writer and the server the
// only reader, and each side carried its own copy of the rules ("what is a
// valid host", "what is the file called", "which bytes are an image") pinned
// together by tests. A second writer would have meant a third copy, and the
// failure mode of drift between writers is the worst kind: two tools that
// both report success and produce directories the other cannot read. So the
// rules and the writer live here, once, and both callers are thin.
//
// The package is deliberately free of HTTP and of the server's caches: it
// knows about a root directory, a host directory under it, a JSON document
// and a handful of image files, and nothing else. jmaphttp imports it; it
// imports nothing from jmaphttp.
package branding

import (
	"net"
	"path"
	"strings"
)

// ConfigFile is the per-host document's filename.
const ConfigFile = "branding.json"

// MaxAssetBytes caps one asset. 2 MiB is generous for a logo or a splash
// photograph and small enough that a hostile directory cannot exhaust memory:
// the server applies it with an io.LimitedReader before buffering.
const MaxAssetBytes = 2 << 20

// MaxShortNameRunes caps File.ShortName. Twelve is what the manifest spec
// recommends as the length launchers can show without truncation; the server
// truncates past it and the writers refuse past it.
const MaxShortNameRunes = 12

// MaxNameRunes and MaxTaglineRunes are the server's truncation points for the
// product name and the login-panel tagline. The writers refuse past them so an
// author learns at write time rather than on a phone.
const (
	MaxNameRunes    = 64
	MaxTaglineRunes = 160
)

// AllowedExtensions are the file extensions an asset may be stored under. The
// SERVER does not trust extensions at all (it sniffs), but the CLI checks them
// so an operator gets a clear refusal at the moment they pass a .svg rather
// than a silently unbranded login page later.
var AllowedExtensions = []string{".png", ".jpg", ".jpeg", ".webp", ".gif"}

// File is the on-disk shape of <root>/<host>/branding.json.
//
// It names local asset FILES, never URLs: the server turns names into
// same-origin URLs when it serves the public document, and keeping the two
// shapes distinct is what stops a customer-supplied string from becoming an
// <img src> pointing anywhere.
type File struct {
	Name       string `json:"name,omitempty"`
	ShortName  string `json:"shortName,omitempty"`
	Tagline    string `json:"tagline,omitempty"`
	SupportURL string `json:"supportUrl,omitempty"`
	PrivacyURL string `json:"privacyUrl,omitempty"`
	TermsURL   string `json:"termsUrl,omitempty"`
	Logo       string `json:"logo,omitempty"`
	LogoDark   string `json:"logoDark,omitempty"`
	Icon       string `json:"icon,omitempty"`
	Splash     string `json:"splash,omitempty"`
	Colors     Colors `json:"colors,omitempty"`

	// BrandAdmins are the mailbox addresses (lowercased) allowed to edit this
	// host's brand through the authenticated admin API. Granted by the
	// operator with `moovctl branding grant`; the server's public document
	// NEVER carries it (anti-enumeration: a stranger must not learn which
	// mailboxes exist, and a non-admin must not learn who the admins are).
	BrandAdmins []string `json:"brandAdmins,omitempty"`
}

// Colors is the color half of the document. Each value is a CSS hex literal
// or empty (meaning Moov's default for that token).
type Colors struct {
	Primary    string `json:"primary,omitempty"`
	OnPrimary  string `json:"onPrimary,omitempty"`
	SplashFrom string `json:"splashFrom,omitempty"`
	SplashTo   string `json:"splashTo,omitempty"`
}

// HasBrand reports whether the document configures anything a visitor can
// see. A document holding only BrandAdmins is administrative state, not a
// brand: the server serves Moov's defaults for it, flagged as such.
func (f File) HasBrand() bool {
	return f.Name != "" || f.ShortName != "" || f.Tagline != "" ||
		f.SupportURL != "" || f.PrivacyURL != "" || f.TermsURL != "" ||
		f.Logo != "" || f.LogoDark != "" || f.Icon != "" || f.Splash != "" ||
		f.Colors != Colors{}
}

// IsBrandAdmin reports whether a mailbox is on the document's admin list. The
// comparison is on the normalized form of both sides.
func (f File) IsBrandAdmin(mailbox string) bool {
	m, ok := NormalizeMailbox(mailbox)
	if !ok {
		return false
	}
	for _, a := range f.BrandAdmins {
		if a == m {
			return true
		}
	}
	return false
}

// Grant adds a mailbox to the admin list, keeping it normalized, sorted and
// free of duplicates. It reports whether the list changed.
func (f *File) Grant(mailbox string) (bool, error) {
	m, ok := NormalizeMailbox(mailbox)
	if !ok {
		return false, ErrInvalidMailbox
	}
	if f.IsBrandAdmin(m) {
		return false, nil
	}
	f.BrandAdmins = insertSorted(f.BrandAdmins, m)
	return true, nil
}

// Revoke removes a mailbox from the admin list, reporting whether it was
// there.
func (f *File) Revoke(mailbox string) (bool, error) {
	m, ok := NormalizeMailbox(mailbox)
	if !ok {
		return false, ErrInvalidMailbox
	}
	kept := f.BrandAdmins[:0:0]
	found := false
	for _, a := range f.BrandAdmins {
		if a == m {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if len(kept) == 0 {
		kept = nil
	}
	f.BrandAdmins = kept
	return found, nil
}

func insertSorted(list []string, v string) []string {
	out := make([]string, 0, len(list)+1)
	inserted := false
	for _, a := range list {
		if !inserted && v < a {
			out = append(out, v)
			inserted = true
		}
		out = append(out, a)
	}
	if !inserted {
		out = append(out, v)
	}
	return out
}

// AssetKind names one of the four image slots of a brand. Its string form is
// the JSON field name in File and the {kind} segment of the admin API.
type AssetKind string

// The four asset kinds.
const (
	AssetLogo     AssetKind = "logo"
	AssetLogoDark AssetKind = "logoDark"
	AssetIcon     AssetKind = "icon"
	AssetSplash   AssetKind = "splash"
)

// AssetKinds lists every kind, in the order the CLI and the admin document
// present them.
var AssetKinds = []AssetKind{AssetLogo, AssetLogoDark, AssetIcon, AssetSplash}

// ParseAssetKind accepts exactly the four kind names.
func ParseAssetKind(s string) (AssetKind, bool) {
	for _, k := range AssetKinds {
		if string(k) == s {
			return k, true
		}
	}
	return "", false
}

// BaseName is the filename stem an asset of this kind is stored under
// ("logo", "logo-dark", "icon", "splash"); the extension comes from the
// sniffed image type. The stored name is always OURS, never the source
// filename, so a name like "../../etc/passwd.png" can never become part of
// a URL.
func (k AssetKind) BaseName() string {
	if k == AssetLogoDark {
		return "logo-dark"
	}
	return string(k)
}

// Field returns the document field that records this kind's stored filename.
func (k AssetKind) Field(f *File) *string {
	switch k {
	case AssetLogo:
		return &f.Logo
	case AssetLogoDark:
		return &f.LogoDark
	case AssetIcon:
		return &f.Icon
	case AssetSplash:
		return &f.Splash
	}
	return nil
}

// FeedsIcons reports whether this kind is a source for the rendered PWA
// icons (the chain is icon, then logo, then Moov's own). logoDark and splash
// never are, so a WebP there is fine and nothing is declared about it.
func (k AssetKind) FeedsIcons() bool { return k == AssetLogo || k == AssetIcon }

// NormalizeHost normalizes a hostname — a Host header or a -host flag — into
// the directory name a brand is configured under.
//
// It strips the port, lowercases, drops a trailing dot, and REFUSES anything
// that is not a plain hostname: no path separators, no "..", no percent
// escapes, no empty labels, no IPv6 literal. The refusal returns "", which
// every caller treats as "no configuration", so a hostile Host header
// degrades to the Moov defaults rather than to a filesystem read.
func NormalizeHost(raw string) string {
	h := strings.TrimSpace(raw)
	if h == "" {
		return ""
	}
	// Host may carry a port; SplitHostPort fails when it does not, which is
	// the common case, so its error is not interesting.
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	// An IPv6 literal arrives bracketed; brackets are not legal in a path
	// component on every platform, and nobody brands an IP address.
	if h == "" || strings.ContainsAny(h, `/\[]%:`) {
		return ""
	}
	if h == "." || h == ".." || strings.Contains(h, "..") {
		return ""
	}
	// A conservative hostname alphabet. Anything outside it cannot name a
	// directory we created, so there is nothing to lose by refusing it.
	for _, c := range h {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.':
		default:
			return ""
		}
	}
	if strings.HasPrefix(h, ".") || strings.HasPrefix(h, "-") {
		return ""
	}
	return h
}

// SafeAssetName reduces a configured filename to a single, safe path
// component, or "" if it cannot be one.
//
// SVG IS DELIBERATELY NOT ACCEPTED anywhere in this package. An SVG is an XML
// document that can carry <script>, external references and CSS, so serving
// an operator-supplied one from our own origin would hand any customer who
// can upload a logo a stored-XSS primitive on the login page — the page that
// exists to receive passwords. Sanitizing SVG correctly is a project in
// itself; refusing it costs a customer one export step.
func SafeAssetName(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return ""
	}
	if strings.ContainsAny(n, `/\`) || strings.Contains(n, "..") {
		return ""
	}
	if n != path.Base(n) || n == "." || n == ".." {
		return ""
	}
	if strings.HasPrefix(n, ".") {
		return ""
	}
	if len(n) > 128 {
		return ""
	}
	for _, c := range n {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
		default:
			return ""
		}
	}
	// The configuration file itself is never an asset.
	if strings.EqualFold(n, ConfigFile) {
		return ""
	}
	return n
}

// NormalizeHexColor validates a CSS hex color and returns it lowercased, or
// "" if it is not one. Only #rgb and #rrggbb are accepted: a single syntax
// the client can render, compare and contrast-check without a CSS parser.
func NormalizeHexColor(s string) string {
	c := strings.TrimSpace(s)
	if len(c) != 4 && len(c) != 7 {
		return ""
	}
	if c[0] != '#' {
		return ""
	}
	for _, ch := range c[1:] {
		switch {
		case ch >= '0' && ch <= '9', ch >= 'a' && ch <= 'f', ch >= 'A' && ch <= 'F':
		default:
			return ""
		}
	}
	return strings.ToLower(c)
}

// SafeURL allows only the schemes that can appear in an href on the login
// page without becoming script execution.
func SafeURL(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "https://") ||
		strings.HasPrefix(l, "http://") ||
		strings.HasPrefix(l, "mailto:")
}

// NormalizeMailbox lowercases and trims a mailbox address and reports whether
// it has the one shape an admin grant accepts: a non-empty local part, one
// "@", a domain that NormalizeHost would accept, and no whitespace or control
// characters. It is a syntactic check, not a deliverability one: the grant is
// matched against the address the store authenticated, which Mailcow already
// vouched for.
func NormalizeMailbox(raw string) (string, bool) {
	m := strings.ToLower(strings.TrimSpace(raw))
	at := strings.LastIndexByte(m, '@')
	if at <= 0 || at == len(m)-1 {
		return "", false
	}
	local, domain := m[:at], m[at+1:]
	if len(m) > 254 || len(local) > 64 {
		return "", false
	}
	for _, c := range local {
		if c <= ' ' || c == 0x7f || c == '@' || c == '"' || c == '\\' || c == '/' {
			return "", false
		}
	}
	if NormalizeHost(domain) != domain {
		return "", false
	}
	return m, true
}

// SniffImageType identifies a raster image by its magic bytes and returns the
// Content-Type to declare.
//
// Hand-rolled rather than http.DetectContentType because that function's
// allowlist is far wider than four image formats — it would happily classify
// bytes as text/html, which is the one answer the asset route must never
// give.
func SniffImageType(b []byte) (string, bool) {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png", true
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg", true
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp", true
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "image/gif", true
	}
	return "", false
}

// ExtensionFor maps a sniffed Content-Type to the extension an asset of that
// type is stored under. The result is always a member of AllowedExtensions.
func ExtensionFor(contentType string) (string, bool) {
	switch contentType {
	case "image/png":
		return ".png", true
	case "image/jpeg":
		return ".jpg", true
	case "image/webp":
		return ".webp", true
	case "image/gif":
		return ".gif", true
	}
	return "", false
}

// LooksLikeSVG reports whether a body that is NOT a raster image is
// recognizably an SVG (or any XML document), so the refusal can name the real
// problem — "SVG is not accepted" — instead of the downstream symptom "not a
// supported image". It is only ever consulted after SniffImageType said no.
func LooksLikeSVG(b []byte) bool {
	// A leading UTF-8 byte order mark is stripped by its bytes, so no editor
	// can swallow the literal.
	head := strings.TrimPrefix(string(b[:min(len(b), 512)]), "\xef\xbb\xbf")
	head = strings.ToLower(strings.TrimLeft(head, " \t\r\n"))
	return strings.HasPrefix(head, "<?xml") || strings.HasPrefix(head, "<svg") ||
		strings.HasPrefix(head, "<!doctype svg")
}

// TruncateRunes caps a string by RUNES, so a multi-byte name is cut at a
// character boundary rather than mid-codepoint.
func TruncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}

// RuneLen is the length of a string in runes, the unit every cap in this
// package is expressed in.
func RuneLen(s string) int { return len([]rune(s)) }
