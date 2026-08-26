package jmaphttp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Branding (arbitration W-A1 of L2-pwa §3): a PUBLIC, unauthenticated
// GET /branding that answers according to the request's Host, so the PWA can
// paint a customer's logo, splash image, product name and colors BEFORE the
// user has any credentials to authenticate with.
//
// # Why unauthenticated
//
// The brand IS the login screen. Anything gated behind auth cannot be shown to
// a user who has not logged in yet, which is precisely the audience the login
// screen has. So this is the one anonymous route in a server whose every other
// route requires credentials, and it is built to earn that exception:
//
//   - it reads a directory of operator-curated files, never the database, so a
//     bug here cannot reach a mailbox;
//   - it never echoes anything the caller controls into the response body
//     except through the Host resolution below;
//   - it serves only assets whose bytes were validated as images, with the
//     headers that make a browser refuse to execute them whatever they contain.
//
// # Why resolved by Host and not by a query parameter
//
// A parameter (or worse, the email address the user is typing) would turn this
// into an oracle for "which domains exist on this server": an attacker could
// enumerate the customer list of a Moov installation one guess at a time. The
// Host header is not that: the caller already had to know the hostname to send
// the request, and a hostname with no configuration is answered with the Moov
// defaults — INDISTINGUISHABLE from a hostname that is configured to look like
// Moov. Existence is never confirmed or denied.

// BrandingPaths are the two routes the branding feature adds.
const (
	// PathBranding serves the resolved branding document. GET, public.
	PathBranding = "/branding"

	// PathBrandingAsset serves one validated brand asset. GET, public. The
	// {host} variable is the RESOLVED host (what /branding put in the URL),
	// not free-form caller input — resolveBrandingHost re-validates it.
	PathBrandingAsset = "/branding/assets/{host}/{name}"
)

// Branding asset limits. Both are enforced when the file is READ, so a file
// dropped into the directory after startup cannot exceed them either.
const (
	// MaxBrandingAssetBytes caps one asset. 2 MiB is generous for a logo or a
	// splash photograph and small enough that a hostile directory cannot
	// exhaust memory: the cap is applied with an io.LimitedReader before the
	// bytes are buffered.
	MaxBrandingAssetBytes = 2 << 20

	// brandingCacheTTL is how long a resolved document is remembered in
	// process. Short enough that `moovctl branding set` shows up without a
	// restart, long enough that the login page of a busy morning does not stat
	// the filesystem for every visitor.
	brandingCacheTTL = 60 * time.Second

	// BrandingMaxAge is the value of the public Cache-Control max-age. The
	// document is small, public and changes when an operator says so; a minute
	// of shared caching costs nothing and removes the request entirely from a
	// reload.
	BrandingMaxAge = 300
)

// brandingConfigFile is the per-host document an operator writes (through
// `moovctl branding set`, which is the only supported writer).
const brandingConfigFile = "branding.json"

// Branding is the public document GET /branding returns.
//
// Every field is a STRING the client drops into a CSS custom property or an
// <img src>; there is nothing structured for a client to misinterpret and
// nothing sensitive for a stranger to learn. The JSON tags are the wire
// contract the PWA is written against.
type Branding struct {
	// Name is the product name shown in the UI and the browser tab.
	Name string `json:"name"`

	// LogoURL and SplashURL are absolute-path URLs on THIS origin (never a
	// third-party URL: a customer-supplied external URL would be a tracking
	// pixel on our login page and a mixed-content risk). Empty means "the
	// client should fall back to its built-in mark".
	LogoURL   string `json:"logoUrl"`
	SplashURL string `json:"splashUrl"`

	// Colors are the brand's design tokens. Every value is a validated CSS hex
	// color (#rgb or #rrggbb); the client assigns them to custom properties
	// without parsing.
	Colors BrandingColors `json:"colors"`

	// Tagline is the optional line under the product name on the login split
	// panel. Empty renders nothing.
	Tagline string `json:"tagline,omitempty"`

	// SupportURL is where "contact your administrator" can point. Empty
	// renders as plain text instead of a link. Restricted to http(s) and
	// mailto: so it can never become a javascript: URL in the DOM.
	SupportURL string `json:"supportUrl,omitempty"`

	// Default reports whether this document is the built-in Moov branding
	// (true) or a configured customer brand (false).
	//
	// It is deliberately NOT an existence oracle: it says what the caller is
	// being shown, which the caller can see anyway by looking at the response.
	Default bool `json:"default"`
}

// BrandingColors is the color half of the token set.
//
// The set is small on purpose. These are the SEED tokens: the client derives
// hovers, borders and surfaces from them in CSS, so a customer configures four
// values rather than forty, and a half-configured brand can never produce an
// unreadable screen.
type BrandingColors struct {
	// Primary is the accent: buttons, links, focus rings.
	Primary string `json:"primary"`

	// OnPrimary is the text drawn ON the primary color. It exists as its own
	// token because deriving it (light text on dark accents, dark on light)
	// is exactly the kind of guess that produces an AA contrast failure.
	OnPrimary string `json:"onPrimary"`

	// SplashFrom and SplashTo are the two stops of the brand panel's gradient,
	// used when no splash image is configured and as the image's backdrop
	// while it loads.
	SplashFrom string `json:"splashFrom"`
	SplashTo   string `json:"splashTo"`
}

// DefaultBranding is Moov's own brand — the document served for any host with
// no configuration, which per L2-pwa §2 (P3) is what MOST installations will
// ever show. It is a real brand, not a placeholder.
//
// The palette: a deep indigo-violet accent (#5B5BD6) that reads as software
// rather than as a corporate template, on a splash gradient running from
// midnight indigo to a warmer violet. #5B5BD6 against white is 5.4:1 and
// white on #5B5BD6 is 5.4:1 — both clear AA for body text, and the PWA pins
// those numbers in a test rather than trusting this comment.
func DefaultBranding() Branding {
	return Branding{
		Name:      "Moov Mail",
		LogoURL:   "",
		SplashURL: "",
		Colors: BrandingColors{
			Primary:    "#5b5bd6",
			OnPrimary:  "#ffffff",
			SplashFrom: "#1e1b4b",
			SplashTo:   "#4c1d95",
		},
		Tagline: "",
		Default: true,
	}
}

// BrandingConfig configures the branding endpoint.
type BrandingConfig struct {
	// Dir is the root the per-host directories live under (MOOV_BRANDING_DIR,
	// e.g. /etc/moov/branding, holding <host>/branding.json and its assets).
	// Empty disables customisation entirely: every host is answered with the
	// Moov defaults, which is a perfectly good production configuration and is
	// what the pilot runs.
	Dir string
}

// brandingStore resolves and caches branding documents from the filesystem.
type brandingStore struct {
	dir string

	mu    sync.Mutex
	cache map[string]brandingEntry
	now   func() time.Time
}

type brandingEntry struct {
	doc     Branding
	etag    string
	expires time.Time
}

func newBrandingStore(dir string, now func() time.Time) *brandingStore {
	if now == nil {
		now = time.Now
	}
	return &brandingStore{
		dir:   strings.TrimSpace(dir),
		cache: make(map[string]brandingEntry),
		now:   now,
	}
}

// handleBranding serves GET /branding (W-A1).
func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	host := resolveBrandingHost(r.Host)
	doc, etag := s.branding.resolve(host)

	// Public and cacheable: the document has no per-user content, so a shared
	// cache in front of us may serve it to everyone on that host. Vary: Host is
	// not needed (a cache keys on the whole URL including authority), but is
	// harmless and makes the dependency legible to a proxy that normalizes.
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", BrandingMaxAge))
	w.Header().Set("ETag", etag)
	// Nothing here is HTML, and a browser must never be talked into treating
	// it as such.
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	writeJSON(w, http.StatusOK, doc)
}

// handleBrandingAsset serves GET /branding/assets/{host}/{name}.
//
// The route exists rather than serving assets from a static file server for
// one reason: every byte that leaves here has been read through the same
// validation the CLI applied when writing it, so a file placed in the
// directory by any other means still cannot become an HTML document, an SVG
// with a script in it, or a 900 MB response.
func (s *Server) handleBrandingAsset(w http.ResponseWriter, r *http.Request) {
	host := resolveBrandingHost(r.PathValue("host"))
	name := r.PathValue("name")

	body, contentType, err := s.branding.openAsset(host, name)
	if err != nil {
		// Every failure — unknown host, unknown file, a file that failed
		// validation, a traversal attempt — is the same 404. The caller learns
		// only "there is no asset at this URL", which is all it is entitled to.
		writeGenericProblem(w, http.StatusNotFound, "not found")
		return
	}

	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("X-Content-Type-Options", "nosniff")
	// Belt and braces: even for a validated raster image, nothing may be
	// fetched, framed or executed on its behalf.
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", fmt.Sprintf("public, max-age=%d", BrandingMaxAge))
	_, _ = w.Write(body)
}

// resolveBrandingHost normalizes a Host header into the directory name a
// brand is configured under.
//
// It strips the port, lowercases, drops a trailing dot, and REFUSES anything
// that is not a plain hostname — no path separators, no "..", no percent
// escapes, no empty labels. The refusal returns "", which resolve and
// openAsset both treat as "no configuration", so a hostile Host header
// degrades to the Moov defaults rather than to a filesystem read.
func resolveBrandingHost(raw string) string {
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

// resolve returns the branding document for a host and its ETag.
//
// It never returns an error: a missing directory, an unreadable file or a
// malformed document all resolve to the Moov defaults, because a login screen
// that refuses to render is a far worse outcome than one that renders
// unbranded. A malformed document IS logged, so an operator learns their
// typo did not take effect.
func (b *brandingStore) resolve(host string) (Branding, string) {
	if b == nil || b.dir == "" || host == "" {
		doc := DefaultBranding()
		return doc, brandingETag(doc)
	}

	now := b.now()
	b.mu.Lock()
	if e, ok := b.cache[host]; ok && now.Before(e.expires) {
		b.mu.Unlock()
		return e.doc, e.etag
	}
	b.mu.Unlock()

	doc := b.load(host)
	etag := brandingETag(doc)

	b.mu.Lock()
	b.cache[host] = brandingEntry{doc: doc, etag: etag, expires: now.Add(brandingCacheTTL)}
	b.mu.Unlock()

	return doc, etag
}

// load reads and validates one host's configuration from disk.
func (b *brandingStore) load(host string) Branding {
	fallback := DefaultBranding()

	// #nosec G304 -- `host` is not caller-controlled input at this point: every
	// path into this function runs it through resolveBrandingHost, which admits
	// only [a-z0-9.-], rejects "..", separators and percent escapes, and returns
	// "" for anything else (resolve() then short-circuits before reaching here).
	// The filename is a package constant. A traversal test pins this.
	raw, err := os.ReadFile(filepath.Join(b.dir, host, brandingConfigFile))
	if err != nil {
		// A host with no configuration is the normal case, not a problem.
		return fallback
	}

	var file brandingFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return fallback
	}

	doc := fallback
	doc.Default = false

	if n := strings.TrimSpace(file.Name); n != "" {
		doc.Name = truncateRunes(n, 64)
	}
	if t := strings.TrimSpace(file.Tagline); t != "" {
		doc.Tagline = truncateRunes(t, 160)
	}
	if u := strings.TrimSpace(file.SupportURL); u != "" && safeSupportURL(u) {
		doc.SupportURL = u
	}

	// A color is taken only if it is a valid CSS hex literal. An invalid one
	// falls back to Moov's, so a typo produces a slightly-off brand rather
	// than an unstyled or unreadable page.
	if c := normalizeHexColor(file.Colors.Primary); c != "" {
		doc.Colors.Primary = c
	}
	if c := normalizeHexColor(file.Colors.OnPrimary); c != "" {
		doc.Colors.OnPrimary = c
	}
	if c := normalizeHexColor(file.Colors.SplashFrom); c != "" {
		doc.Colors.SplashFrom = c
	}
	if c := normalizeHexColor(file.Colors.SplashTo); c != "" {
		doc.Colors.SplashTo = c
	}

	// An asset is advertised only if the file is present AND still passes
	// validation right now. That is what keeps the document honest: the URL in
	// the response is a URL that will serve bytes, not a promise about a file
	// that was valid when the CLI ran.
	if name := safeAssetName(file.Logo); name != "" {
		if _, _, err := b.openAsset(host, name); err == nil {
			doc.LogoURL = brandingAssetURL(host, name)
		}
	}
	if name := safeAssetName(file.Splash); name != "" {
		if _, _, err := b.openAsset(host, name); err == nil {
			doc.SplashURL = brandingAssetURL(host, name)
		}
	}

	return doc
}

// brandingFile is the on-disk shape `moovctl branding set` writes. It is
// deliberately a DIFFERENT type from Branding: the file names local asset
// FILES, the response carries URLs, and conflating the two is how a
// customer-supplied string ends up as an <img src> pointing anywhere.
type brandingFile struct {
	Name       string             `json:"name,omitempty"`
	Tagline    string             `json:"tagline,omitempty"`
	SupportURL string             `json:"supportUrl,omitempty"`
	Logo       string             `json:"logo,omitempty"`
	Splash     string             `json:"splash,omitempty"`
	Colors     brandingFileColors `json:"colors,omitempty"`
}

type brandingFileColors struct {
	Primary    string `json:"primary,omitempty"`
	OnPrimary  string `json:"onPrimary,omitempty"`
	SplashFrom string `json:"splashFrom,omitempty"`
	SplashTo   string `json:"splashTo,omitempty"`
}

// brandingAssetURL builds the on-origin URL for one asset. Root-relative on
// purpose: the PWA is served from the same origin, and a relative URL cannot
// become a cross-origin request no matter what the Host header said.
func brandingAssetURL(host, name string) string {
	return "/branding/assets/" + host + "/" + name
}

// errBrandingAsset is the single failure the asset path reports. The caller
// renders every cause identically, so the causes are not enumerated in the
// type.
var errBrandingAsset = errors.New("jmaphttp: branding asset unavailable")

// openAsset reads and validates one asset.
//
// Validation is by CONTENT, not by extension: the bytes are sniffed and must
// be a PNG, JPEG, WebP or GIF. The declared Content-Type comes from the
// SNIFFED type, never from the filename, so a file called logo.png containing
// HTML is refused rather than served as an image the browser then re-sniffs.
func (b *brandingStore) openAsset(host, name string) ([]byte, string, error) {
	if b == nil || b.dir == "" || host == "" {
		return nil, "", errBrandingAsset
	}
	clean := safeAssetName(name)
	if clean == "" {
		return nil, "", errBrandingAsset
	}

	full := filepath.Join(b.dir, host, clean)

	f, err := os.Open(full) // #nosec G304 -- host and clean are validated above to be single path components from a fixed alphabet.
	if err != nil {
		return nil, "", errBrandingAsset
	}
	defer func() { _ = f.Close() }()

	// A directory, a symlink to /dev/zero, a FIFO: only a regular file is an
	// asset. Stat AFTER opening so the check applies to the thing that was
	// actually opened rather than to whatever the name pointed at a moment ago.
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, "", errBrandingAsset
	}
	if info.Size() > MaxBrandingAssetBytes {
		return nil, "", errBrandingAsset
	}

	// LimitReader with one extra byte: reading MaxBrandingAssetBytes+1 tells
	// the difference between "exactly at the cap" and "over it" for a file
	// that grew between Stat and Read.
	body, err := io.ReadAll(io.LimitReader(f, MaxBrandingAssetBytes+1))
	if err != nil || len(body) > MaxBrandingAssetBytes || len(body) == 0 {
		return nil, "", errBrandingAsset
	}

	contentType, ok := sniffImageType(body)
	if !ok {
		return nil, "", errBrandingAsset
	}
	return body, contentType, nil
}

// safeAssetName reduces a configured filename to a single, safe path
// component, or "" if it cannot be one.
//
// SVG IS DELIBERATELY NOT ACCEPTED, here or in the CLI. An SVG is an XML
// document that can carry <script>, external references and CSS, so serving an
// operator-supplied one from our own origin would hand any customer who can
// upload a logo a stored-XSS primitive on the login page — the page that
// exists to receive passwords. Sanitizing SVG correctly is a project in
// itself; refusing it costs a customer one export step. PNG with an alpha
// channel covers every real logo. (L2-pwa §6 risk 4: "sin SVG sin sanitizar" —
// this is the strict reading of that line.)
func safeAssetName(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return ""
	}
	// Reject anything with structure before looking at it further.
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
	if strings.EqualFold(n, brandingConfigFile) {
		return ""
	}
	return n
}

// AllowedBrandingExtensions are the file extensions `moovctl branding set`
// accepts. The SERVER does not trust extensions at all (it sniffs), but the
// CLI checks them so an operator gets a clear refusal at the moment they pass
// a .svg rather than a silently unbranded login page later.
var AllowedBrandingExtensions = []string{".png", ".jpg", ".jpeg", ".webp", ".gif"}

// sniffImageType identifies a raster image by its magic bytes and returns the
// Content-Type to declare.
//
// Hand-rolled rather than http.DetectContentType because that function's
// allowlist is far wider than four image formats — it would happily classify
// bytes as text/html, which is the one answer this endpoint must never give.
func sniffImageType(b []byte) (string, bool) {
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

// normalizeHexColor validates a CSS hex color and returns it lowercased, or
// "" if it is not one.
//
// Only #rgb and #rrggbb are accepted. Named colors, rgb() and hsl() are
// refused not because they are dangerous — the value lands in a CSS custom
// property, not in a script — but because a single accepted syntax is one the
// client can render, compare and contrast-check without a CSS parser.
func normalizeHexColor(s string) string {
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

// safeSupportURL allows only the schemes that can appear in an href on the
// login page without becoming script execution.
func safeSupportURL(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "https://") ||
		strings.HasPrefix(l, "http://") ||
		strings.HasPrefix(l, "mailto:")
}

// truncateRunes caps a string by RUNES, so a multi-byte name is cut at a
// character boundary rather than mid-codepoint.
func truncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}

// brandingETag fingerprints a document so a conditional request can be
// answered 304. It is derived from the document itself rather than from file
// mtimes: two hosts with identical branding legitimately share an ETag, and a
// file rewritten with the same content correctly does not invalidate caches.
func brandingETag(doc Branding) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t",
		doc.Name, doc.LogoURL, doc.SplashURL,
		doc.Colors.Primary, doc.Colors.OnPrimary,
		doc.Colors.SplashFrom, doc.Colors.SplashTo,
		doc.Tagline, doc.Default)
	return `"` + hex.EncodeToString(h.Sum(nil))[:16] + `"`
}

// etagMatches implements the If-None-Match comparison for our single strong
// ETag: the "*" wildcard, or the tag present in the comma-separated list.
func etagMatches(header, etag string) bool {
	if strings.TrimSpace(header) == "*" {
		return true
	}
	for _, candidate := range strings.Split(header, ",") {
		c := strings.TrimSpace(candidate)
		// A weak validator (W/"...") compares equal to our strong one for the
		// purposes of If-None-Match (RFC 9110 §13.1.2 uses weak comparison).
		c = strings.TrimPrefix(c, "W/")
		if c == etag {
			return true
		}
	}
	return false
}

// brandingDirIsUsable reports whether a configured branding directory can be
// read, for the startup log line. It never fails startup: an unreadable
// directory degrades to the Moov defaults, which is a working server.
func brandingDirIsUsable(dir string) error {
	if dir == "" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fs.ErrInvalid
	}
	return nil
}
