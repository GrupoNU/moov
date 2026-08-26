package jmaphttp

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests for the public branding endpoint (W-A1). The security-relevant claims
// in branding.go are each pinned by a test here: the route is reachable
// WITHOUT credentials, a hostile Host cannot reach the filesystem, an asset is
// identified by its bytes rather than its name, and SVG is refused.

// --- fixtures ---------------------------------------------------------------

// pngBytes is a real 1x1 PNG. Real magic bytes matter: the server sniffs.
var pngBytes = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05,
	0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00,
	0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

var jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("JFIF stub")...)

func webpBytes() []byte {
	b := make([]byte, 0, 16)
	b = append(b, []byte("RIFF")...)
	b = append(b, 0x10, 0x00, 0x00, 0x00)
	b = append(b, []byte("WEBP")...)
	return b
}

func gifBytes() []byte { return []byte("GIF89a\x01\x00\x01\x00\x00") }

// brandingServer builds a Server whose only wired feature is branding. It
// deliberately uses a validator that would REJECT every credential: if a
// branding test ever passes because the request was authenticated, this makes
// it fail instead.
func brandingServer(t *testing.T, dir string) *Server {
	t.Helper()
	auth, err := NewAuthenticator(AuthConfig{
		// A validator with no accepted credentials and an empty directory: if a
		// branding test ever passes because the request authenticated, it fails
		// here instead.
		Validator: &fakeValidator{},
		Directory: &fakeDirectory{},
	})
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	srv, err := New(Config{BrandingDir: dir}, auth)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

// getBranding issues an unauthenticated GET against the given host.
func getBranding(t *testing.T, srv *Server, path, host string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if host != "" {
		req.Host = host
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeBranding(t *testing.T, rec *httptest.ResponseRecorder) Branding {
	t.Helper()
	var doc Branding
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decoding branding response: %v\nbody: %s", err, rec.Body.String())
	}
	return doc
}

// writeBrand lays out one host's branding directory.
func writeBrand(t *testing.T, root, host string, doc map[string]any, assets map[string][]byte) {
	t.Helper()
	hostDir := filepath.Join(root, host)
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	for name, body := range assets {
		if err := os.WriteFile(filepath.Join(hostDir, name), body, 0o644); err != nil {
			t.Fatalf("writing asset %s: %v", name, err)
		}
	}
	if doc == nil {
		return
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshaling branding doc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostDir, "branding.json"), raw, 0o644); err != nil {
		t.Fatalf("writing branding.json: %v", err)
	}
}

// --- the anonymous-access contract -----------------------------------------

// TestBrandingIsPublic is the load-bearing test of W-A1: the brand must render
// on a screen whose whole purpose is to collect the credentials, so it cannot
// require them. The server under test rejects every credential, so a 200 here
// can only mean the route was never authenticated.
func TestBrandingIsPublic(t *testing.T) {
	srv := brandingServer(t, "")

	rec := getBranding(t, srv, PathBranding, "mail.example.com", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s without credentials = %d, want 200\nbody: %s",
			PathBranding, rec.Code, rec.Body.String())
	}
	if h := rec.Header().Get("WWW-Authenticate"); h != "" {
		t.Errorf("public route sent an auth challenge: %q", h)
	}

	// The contrast: every other route still demands credentials. Without this
	// half, a bug that made the whole server public would pass the half above.
	rec = getBranding(t, srv, PathWellKnown, "mail.example.com", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET %s without credentials = %d, want 401", PathWellKnown, rec.Code)
	}
}

// TestPublicRouteSetIsExactlyBranding pins WHICH routes are anonymous. A new
// route added with public:true — or a copy-paste that marks an authenticated
// one public — fails here rather than in production.
func TestPublicRouteSetIsExactlyBranding(t *testing.T) {
	srv := brandingServer(t, "")

	want := map[string]bool{
		PathBranding:      true,
		PathBrandingAsset: true,
	}
	got := make(map[string]bool)
	for _, rt := range srv.routes() {
		if rt.public {
			got[rt.pattern] = true
		}
	}
	if len(got) != len(want) {
		t.Fatalf("public routes = %v, want exactly %v", got, want)
	}
	for pattern := range want {
		if !got[pattern] {
			t.Errorf("route %s is no longer public", pattern)
		}
	}
}

// --- defaults ---------------------------------------------------------------

func TestBrandingDefaultsWhenUnconfigured(t *testing.T) {
	// No directory at all: the pilot's configuration.
	srv := brandingServer(t, "")
	rec := getBranding(t, srv, PathBranding, "anything.example.com", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	doc := decodeBranding(t, rec)
	def := DefaultBranding()
	if doc.Name != def.Name {
		t.Errorf("name = %q, want %q", doc.Name, def.Name)
	}
	if !doc.Default {
		t.Error("default flag is false for an unconfigured host")
	}
	if doc.Colors.Primary != def.Colors.Primary {
		t.Errorf("primary = %q, want %q", doc.Colors.Primary, def.Colors.Primary)
	}
	if doc.LogoURL != "" || doc.SplashURL != "" {
		t.Errorf("unconfigured host advertised assets: logo=%q splash=%q", doc.LogoURL, doc.SplashURL)
	}
}

// TestBrandingDefaultColorsAreValid guards the default palette itself: every
// seed token must be a hex color the client can parse, or the PWA boots with
// broken custom properties for the majority of installations.
func TestBrandingDefaultColorsAreValid(t *testing.T) {
	def := DefaultBranding()
	for name, value := range map[string]string{
		"primary":    def.Colors.Primary,
		"onPrimary":  def.Colors.OnPrimary,
		"splashFrom": def.Colors.SplashFrom,
		"splashTo":   def.Colors.SplashTo,
	} {
		if normalizeHexColor(value) != value {
			t.Errorf("default color %s = %q is not a normalized hex color", name, value)
		}
	}
	if def.Name == "" {
		t.Error("the default branding has no product name")
	}
	if !def.Default {
		t.Error("DefaultBranding().Default must be true")
	}
}

// TestDefaultPaletteMeetsContrastAA pins the accessibility claim made in
// DefaultBranding's doc comment. Body text on the accent, and the accent as
// text on white, must both clear WCAG AA (4.5:1) — the login button and its
// label are exactly this pair.
func TestDefaultPaletteMeetsContrastAA(t *testing.T) {
	def := DefaultBranding()

	if ratio := contrastRatio(t, def.Colors.OnPrimary, def.Colors.Primary); ratio < 4.5 {
		t.Errorf("onPrimary on primary = %.2f:1, want >= 4.5:1 (WCAG AA)", ratio)
	}
	if ratio := contrastRatio(t, def.Colors.Primary, "#ffffff"); ratio < 4.5 {
		t.Errorf("primary as text on white = %.2f:1, want >= 4.5:1 (WCAG AA)", ratio)
	}
	// The splash gradient carries white overlay text at large sizes; 3:1 is the
	// AA threshold for large text, and both stops must clear it.
	for name, stop := range map[string]string{
		"splashFrom": def.Colors.SplashFrom,
		"splashTo":   def.Colors.SplashTo,
	} {
		if ratio := contrastRatio(t, "#ffffff", stop); ratio < 3.0 {
			t.Errorf("white on %s = %.2f:1, want >= 3:1 (AA large text)", name, ratio)
		}
	}
}

// contrastRatio computes the WCAG 2.x contrast ratio between two hex colors.
func contrastRatio(t *testing.T, fg, bg string) float64 {
	t.Helper()
	lf := relativeLuminance(t, fg)
	lb := relativeLuminance(t, bg)
	if lf < lb {
		lf, lb = lb, lf
	}
	return (lf + 0.05) / (lb + 0.05)
}

func relativeLuminance(t *testing.T, hex string) float64 {
	t.Helper()
	c := normalizeHexColor(hex)
	if c == "" {
		t.Fatalf("%q is not a hex color", hex)
	}
	if len(c) == 4 {
		c = string([]byte{'#', c[1], c[1], c[2], c[2], c[3], c[3]})
	}
	channel := func(b byte) float64 {
		v := float64(b) / 255.0
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	var rgb [3]byte
	for i := 0; i < 3; i++ {
		hi := hexNibble(c[1+i*2])
		lo := hexNibble(c[2+i*2])
		rgb[i] = byte(hi<<4 | lo)
	}
	return 0.2126*channel(rgb[0]) + 0.7152*channel(rgb[1]) + 0.0722*channel(rgb[2])
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return 0
}

// --- host resolution --------------------------------------------------------

func TestBrandingResolvesByHost(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "mail.acme.test", map[string]any{
		"name":    "Acme Mail",
		"tagline": "Correo de Acme",
		"logo":    "logo.png",
		"splash":  "splash.jpg",
		"colors": map[string]string{
			"primary":    "#C0FFEE",
			"onPrimary":  "#000",
			"splashFrom": "#102030",
			"splashTo":   "#405060",
		},
	}, map[string][]byte{
		"logo.png":   pngBytes,
		"splash.jpg": jpegBytes,
	})

	srv := brandingServer(t, root)

	// The configured host gets its brand.
	doc := decodeBranding(t, getBranding(t, srv, PathBranding, "mail.acme.test", nil))
	if doc.Name != "Acme Mail" {
		t.Errorf("name = %q, want %q", doc.Name, "Acme Mail")
	}
	if doc.Default {
		t.Error("a configured host is flagged default")
	}
	if doc.Tagline != "Correo de Acme" {
		t.Errorf("tagline = %q", doc.Tagline)
	}
	// Hex is normalized to lowercase, and the 3-digit form is preserved as-is.
	if doc.Colors.Primary != "#c0ffee" {
		t.Errorf("primary = %q, want %q", doc.Colors.Primary, "#c0ffee")
	}
	if doc.Colors.OnPrimary != "#000" {
		t.Errorf("onPrimary = %q, want %q", doc.Colors.OnPrimary, "#000")
	}
	if doc.LogoURL != "/branding/assets/mail.acme.test/logo.png" {
		t.Errorf("logoUrl = %q", doc.LogoURL)
	}
	if doc.SplashURL != "/branding/assets/mail.acme.test/splash.jpg" {
		t.Errorf("splashUrl = %q", doc.SplashURL)
	}

	// Another host on the same server falls back — and is INDISTINGUISHABLE
	// from a host branded to look like Moov, which is the non-enumeration
	// property W-A1 asks for.
	other := decodeBranding(t, getBranding(t, srv, PathBranding, "mail.other.test", nil))
	if other.Name != DefaultBranding().Name || !other.Default {
		t.Errorf("unconfigured host on a configured server: %+v", other)
	}
}

// TestBrandingHostNormalization covers the Host forms a real deployment sees
// and the ones an attacker sends.
func TestBrandingHostNormalization(t *testing.T) {
	cases := []struct {
		name string
		host string
		want string
	}{
		{"plain", "mail.example.com", "mail.example.com"},
		{"with port", "mail.example.com:8443", "mail.example.com"},
		{"uppercase", "Mail.EXAMPLE.com", "mail.example.com"},
		{"trailing dot", "mail.example.com.", "mail.example.com"},

		// Every one of these must resolve to "" so the request falls back to
		// the defaults instead of reaching the filesystem.
		{"empty", "", ""},
		{"traversal", "../../etc", ""},
		{"traversal encoded", "..%2Fetc", ""},
		{"slash", "mail.example.com/../x", ""},
		{"backslash", `mail\example`, ""},
		{"absolute path", "/etc/passwd", ""},
		{"dot", ".", ""},
		{"dotdot", "..", ""},
		{"leading dot", ".example.com", ""},
		{"leading dash", "-example.com", ""},
		{"ipv6 literal", "[::1]:8620", ""},
		{"underscore", "mail_example.com", ""},
		{"null byte", "mail.example.com\x00", ""},
		{"space", "mail example.com", ""},
		{"unicode", "mäil.example.com", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveBrandingHost(tc.host); got != tc.want {
				t.Errorf("resolveBrandingHost(%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}

// TestBrandingHostTraversalCannotEscape proves the normalization actually
// protects the filesystem: a secret placed OUTSIDE the branding root stays
// unreachable however the Host is spelled.
func TestBrandingHostTraversalCannotEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "branding")
	secret := filepath.Join(base, "secret")
	if err := os.MkdirAll(secret, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secret, "branding.json"),
		[]byte(`{"name":"LEAKED"}`), 0o644); err != nil {
		t.Fatalf("writing the decoy: %v", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	srv := brandingServer(t, root)
	for _, host := range []string{"../secret", "..%2Fsecret", "./../secret", `..\secret`} {
		doc := decodeBranding(t, getBranding(t, srv, PathBranding, host, nil))
		if doc.Name == "LEAKED" {
			t.Fatalf("Host %q escaped the branding root", host)
		}
		if !doc.Default {
			t.Errorf("Host %q did not fall back to the defaults: %+v", host, doc)
		}
	}
}

// --- malformed configuration ------------------------------------------------

// TestBrandingMalformedFallsBack covers the rule that a login screen must
// always render: every kind of broken configuration degrades to Moov's brand.
func TestBrandingMalformedFallsBack(t *testing.T) {
	root := t.TempDir()
	hostDir := filepath.Join(root, "broken.test")
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostDir, "branding.json"),
		[]byte("{not json at all"), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}

	srv := brandingServer(t, root)
	rec := getBranding(t, srv, PathBranding, "broken.test", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a broken config must still render)", rec.Code)
	}
	doc := decodeBranding(t, rec)
	if doc.Name != DefaultBranding().Name || !doc.Default {
		t.Errorf("malformed config did not fall back: %+v", doc)
	}
}

// TestBrandingPartialConfigKeepsDefaults: a customer who sets only a name must
// not end up with empty colors, which would render an unstyled page.
func TestBrandingPartialConfigKeepsDefaults(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "partial.test", map[string]any{"name": "Just A Name"}, nil)

	srv := brandingServer(t, root)
	doc := decodeBranding(t, getBranding(t, srv, PathBranding, "partial.test", nil))

	def := DefaultBranding()
	if doc.Name != "Just A Name" {
		t.Errorf("name = %q", doc.Name)
	}
	if doc.Colors.Primary != def.Colors.Primary {
		t.Errorf("primary = %q, want the default %q", doc.Colors.Primary, def.Colors.Primary)
	}
	if doc.Colors.OnPrimary != def.Colors.OnPrimary {
		t.Errorf("onPrimary = %q, want the default %q", doc.Colors.OnPrimary, def.Colors.OnPrimary)
	}
	if doc.Default {
		t.Error("a partially configured host must not be flagged default")
	}
}

// TestBrandingInvalidColorFallsBack: a typo'd color uses Moov's rather than
// emitting an unparseable custom property.
func TestBrandingInvalidColorFallsBack(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "badcolor.test", map[string]any{
		"name": "Bad Color",
		"colors": map[string]string{
			"primary":   "rebeccapurple",
			"onPrimary": "javascript:alert(1)",
		},
	}, nil)

	srv := brandingServer(t, root)
	doc := decodeBranding(t, getBranding(t, srv, PathBranding, "badcolor.test", nil))

	def := DefaultBranding()
	if doc.Colors.Primary != def.Colors.Primary {
		t.Errorf("primary = %q, want the default", doc.Colors.Primary)
	}
	if doc.Colors.OnPrimary != def.Colors.OnPrimary {
		t.Errorf("onPrimary = %q, want the default", doc.Colors.OnPrimary)
	}
}

// TestBrandingMissingAssetIsNotAdvertised: the document promises only URLs
// that will actually serve bytes.
func TestBrandingMissingAssetIsNotAdvertised(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "noassets.test", map[string]any{
		"name":   "No Assets",
		"logo":   "logo.png", // named, never written
		"splash": "splash.png",
	}, nil)

	srv := brandingServer(t, root)
	doc := decodeBranding(t, getBranding(t, srv, PathBranding, "noassets.test", nil))
	if doc.LogoURL != "" {
		t.Errorf("logoUrl = %q for a file that does not exist", doc.LogoURL)
	}
	if doc.SplashURL != "" {
		t.Errorf("splashUrl = %q for a file that does not exist", doc.SplashURL)
	}
}

// TestBrandingSupportURLSchemes: only schemes that cannot execute.
func TestBrandingSupportURLSchemes(t *testing.T) {
	cases := []struct {
		name string
		url  string
		kept bool
	}{
		{"https", "https://support.acme.test", true},
		{"http", "http://intranet.acme.test/help", true},
		{"mailto", "mailto:it@acme.test", true},
		{"javascript", "javascript:alert(document.cookie)", false},
		{"data", "data:text/html,<script>alert(1)</script>", false},
		{"vbscript", "vbscript:msgbox(1)", false},
		{"file", "file:///etc/passwd", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeBrand(t, root, "support.test", map[string]any{
				"name":       "Support",
				"supportUrl": tc.url,
			}, nil)
			srv := brandingServer(t, root)
			doc := decodeBranding(t, getBranding(t, srv, PathBranding, "support.test", nil))
			if tc.kept && doc.SupportURL != tc.url {
				t.Errorf("supportUrl = %q, want it kept", doc.SupportURL)
			}
			if !tc.kept && doc.SupportURL != "" {
				t.Errorf("supportUrl = %q, want it dropped", doc.SupportURL)
			}
		})
	}
}

// TestBrandingTruncatesLongStrings bounds what a config file can put on the
// page.
func TestBrandingTruncatesLongStrings(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "long.test", map[string]any{
		"name":    strings.Repeat("A", 500),
		"tagline": strings.Repeat("B", 500),
	}, nil)

	srv := brandingServer(t, root)
	doc := decodeBranding(t, getBranding(t, srv, PathBranding, "long.test", nil))
	if len([]rune(doc.Name)) != 64 {
		t.Errorf("name length = %d runes, want 64", len([]rune(doc.Name)))
	}
	if len([]rune(doc.Tagline)) != 160 {
		t.Errorf("tagline length = %d runes, want 160", len([]rune(doc.Tagline)))
	}
}

// --- headers and caching ----------------------------------------------------

func TestBrandingHeaders(t *testing.T) {
	srv := brandingServer(t, "")
	rec := getBranding(t, srv, PathBranding, "mail.example.com", nil)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "public") ||
		!strings.Contains(cc, "max-age=") {
		t.Errorf("Cache-Control = %q, want a public max-age (W-A1: cacheable)", cc)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("no ETag; the document cannot be revalidated")
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options: nosniff")
	}
}

func TestBrandingConditionalRequest(t *testing.T) {
	srv := brandingServer(t, "")

	first := getBranding(t, srv, PathBranding, "mail.example.com", nil)
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the first response")
	}

	second := getBranding(t, srv, PathBranding, "mail.example.com",
		map[string]string{"If-None-Match": etag})
	if second.Code != http.StatusNotModified {
		t.Errorf("If-None-Match with the current ETag = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 carried a body of %d bytes", second.Body.Len())
	}

	// A weak validator must match too (RFC 9110 §13.1.2 weak comparison).
	weak := getBranding(t, srv, PathBranding, "mail.example.com",
		map[string]string{"If-None-Match": "W/" + etag})
	if weak.Code != http.StatusNotModified {
		t.Errorf("weak If-None-Match = %d, want 304", weak.Code)
	}

	stale := getBranding(t, srv, PathBranding, "mail.example.com",
		map[string]string{"If-None-Match": `"0000000000000000"`})
	if stale.Code != http.StatusOK {
		t.Errorf("stale If-None-Match = %d, want 200", stale.Code)
	}
}

// TestBrandingETagDiffersPerBrand: two hosts with different brands must not
// share a validator, or a shared cache would serve one customer's brand to
// another.
func TestBrandingETagDiffersPerBrand(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "a.test", map[string]any{"name": "Brand A"}, nil)
	writeBrand(t, root, "b.test", map[string]any{"name": "Brand B"}, nil)

	srv := brandingServer(t, root)
	a := getBranding(t, srv, PathBranding, "a.test", nil).Header().Get("ETag")
	b := getBranding(t, srv, PathBranding, "b.test", nil).Header().Get("ETag")
	if a == b {
		t.Errorf("two different brands share the ETag %s", a)
	}
}

// --- assets -----------------------------------------------------------------

func TestBrandingAssetServesValidatedImages(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		body     []byte
		wantType string
	}{
		{"png", "logo.png", pngBytes, "image/png"},
		{"jpeg", "logo.jpg", jpegBytes, "image/jpeg"},
		{"webp", "logo.webp", webpBytes(), "image/webp"},
		{"gif", "logo.gif", gifBytes(), "image/gif"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeBrand(t, root, "assets.test", map[string]any{"logo": tc.file},
				map[string][]byte{tc.file: tc.body})

			srv := brandingServer(t, root)
			rec := getBranding(t, srv, "/branding/assets/assets.test/"+tc.file, "assets.test", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != tc.wantType {
				t.Errorf("Content-Type = %q, want %q", ct, tc.wantType)
			}
			if rec.Body.Len() != len(tc.body) {
				t.Errorf("served %d bytes, want %d", rec.Body.Len(), len(tc.body))
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Error("missing nosniff on an asset")
			}
			if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
				t.Errorf("CSP = %q, want default-src 'none'", csp)
			}
		})
	}
}

// TestBrandingAssetRefusesNonImages is the core of the "no unsanitised SVG"
// rule: content decides, not the filename. An SVG named .png, an HTML document
// named .png, and a script all get the same 404.
func TestBrandingAssetRefusesNonImages(t *testing.T) {
	cases := []struct {
		name string
		file string
		body []byte
	}{
		{"svg named png", "logo.png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		{"svg named svg", "logo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)},
		{"html named png", "logo.png", []byte(`<!doctype html><script>alert(1)</script>`)},
		{"empty", "logo.png", []byte{}},
		{"text", "logo.png", []byte("not an image at all")},
		{"html named jpg", "logo.jpg", []byte("<html><body>hi</body></html>")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeBrand(t, root, "hostile.test", map[string]any{"logo": tc.file},
				map[string][]byte{tc.file: tc.body})

			srv := brandingServer(t, root)

			// The asset itself is refused...
			rec := getBranding(t, srv, "/branding/assets/hostile.test/"+tc.file, "hostile.test", nil)
			if rec.Code != http.StatusNotFound {
				t.Errorf("serving %s = %d, want 404\nbody: %s", tc.name, rec.Code, rec.Body.String())
			}
			// ...and the document never points at it, so no client tries.
			doc := decodeBranding(t, getBranding(t, srv, PathBranding, "hostile.test", nil))
			if doc.LogoURL != "" {
				t.Errorf("logoUrl = %q for an invalid asset", doc.LogoURL)
			}
		})
	}
}

// TestBrandingAssetPathSafety: the asset route cannot be walked out of its
// directory, and cannot serve the configuration file.
func TestBrandingAssetPathSafety(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "branding")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "secret.png"), pngBytes, 0o644); err != nil {
		t.Fatalf("writing the decoy: %v", err)
	}
	writeBrand(t, root, "safe.test", map[string]any{"name": "Safe", "logo": "logo.png"},
		map[string][]byte{"logo.png": pngBytes})

	srv := brandingServer(t, root)

	for _, name := range []string{
		"../secret.png",
		"..%2Fsecret.png",
		"branding.json",
		".hidden.png",
		"sub/dir.png",
	} {
		rec := getBranding(t, srv, "/branding/assets/safe.test/"+name, "safe.test", nil)
		if rec.Code == http.StatusOK {
			t.Errorf("asset %q was served (status 200); it must be refused", name)
		}
	}

	// safeAssetName is the unit under those requests.
	for _, bad := range []string{
		"", "..", ".", "../x.png", `..\x.png`, "a/b.png", `a\b.png`,
		".hidden", "branding.json", "BRANDING.JSON", strings.Repeat("a", 200) + ".png",
		"logo name.png", "logo\x00.png", "lögo.png",
	} {
		if got := safeAssetName(bad); got != "" {
			t.Errorf("safeAssetName(%q) = %q, want \"\"", bad, got)
		}
	}
	for _, ok := range []string{"logo.png", "splash.jpg", "brand_mark-2.webp"} {
		if got := safeAssetName(ok); got != ok {
			t.Errorf("safeAssetName(%q) = %q, want it kept", ok, got)
		}
	}
}

// TestBrandingAssetSizeCap: an oversized file is refused rather than buffered.
func TestBrandingAssetSizeCap(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, MaxBrandingAssetBytes+1)
	copy(big, pngBytes)
	writeBrand(t, root, "big.test", map[string]any{"logo": "logo.png"},
		map[string][]byte{"logo.png": big})

	srv := brandingServer(t, root)
	rec := getBranding(t, srv, "/branding/assets/big.test/logo.png", "big.test", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("oversized asset = %d, want 404", rec.Code)
	}
}

// TestBrandingAssetUnknownHost: no asset route on an unbranded host.
func TestBrandingAssetUnknownHost(t *testing.T) {
	srv := brandingServer(t, t.TempDir())
	rec := getBranding(t, srv, "/branding/assets/nobody.test/logo.png", "nobody.test", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// --- caching behavior of the store ----------------------------------------

// TestBrandingStoreCachesAndExpires pins the TTL behavior: a change is picked
// up without a restart (the operational promise `moovctl branding set` prints),
// but not at the cost of a filesystem read per request.
func TestBrandingStoreCachesAndExpires(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "ttl.test", map[string]any{"name": "First"}, nil)

	now := time.Now()
	clock := func() time.Time { return now }
	store := newBrandingStore(root, clock)

	if doc, _ := store.resolve("ttl.test"); doc.Name != "First" {
		t.Fatalf("name = %q, want First", doc.Name)
	}

	// Rewritten on disk, but within the TTL the cached value stands.
	writeBrand(t, root, "ttl.test", map[string]any{"name": "Second"}, nil)
	if doc, _ := store.resolve("ttl.test"); doc.Name != "First" {
		t.Errorf("name = %q inside the TTL, want the cached First", doc.Name)
	}

	// Past the TTL it is re-read.
	now = now.Add(brandingCacheTTL + time.Second)
	if doc, _ := store.resolve("ttl.test"); doc.Name != "Second" {
		t.Errorf("name = %q after the TTL, want Second", doc.Name)
	}
}

// --- color and helper units ------------------------------------------------

func TestNormalizeHexColor(t *testing.T) {
	cases := map[string]string{
		"#fff":       "#fff",
		"#FFF":       "#fff",
		"#5b5bd6":    "#5b5bd6",
		"#5B5BD6":    "#5b5bd6",
		" #abc ":     "#abc",
		"#ffff":      "",
		"#ff":        "",
		"fff":        "",
		"#gggggg":    "",
		"":           "",
		"red":        "",
		"rgb(1,2,3)": "",
		"#12345678":  "",
		"url(x)":     "",
	}
	for in, want := range cases {
		if got := normalizeHexColor(in); got != want {
			t.Errorf("normalizeHexColor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSniffImageType(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want string
		ok   bool
	}{
		{"png", pngBytes, "image/png", true},
		{"jpeg", jpegBytes, "image/jpeg", true},
		{"webp", webpBytes(), "image/webp", true},
		{"gif87", []byte("GIF87a\x00\x00"), "image/gif", true},
		{"gif89", gifBytes(), "image/gif", true},
		{"svg", []byte("<svg xmlns='x'></svg>"), "", false},
		{"html", []byte("<!doctype html>"), "", false},
		{"empty", nil, "", false},
		{"short", []byte{0x89}, "", false},
		{"riff not webp", []byte("RIFF\x00\x00\x00\x00AVI "), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sniffImageType(tc.body)
			if ok != tc.ok || got != tc.want {
				t.Errorf("sniffImageType = (%q, %t), want (%q, %t)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestBrandingCORSPreflight: the PWA may be served from another origin in
// development, so branding needs the same preflight every other route gets.
func TestBrandingCORSPreflight(t *testing.T) {
	auth, err := NewAuthenticator(AuthConfig{
		// A validator with no accepted credentials and an empty directory: if a
		// branding test ever passes because the request authenticated, it fails
		// here instead.
		Validator: &fakeValidator{},
		Directory: &fakeDirectory{},
	})
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	srv, err := New(Config{AllowedOrigins: []string{"http://localhost:5173"}}, auth)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodOptions, PathBranding, nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight = %d, want 204", rec.Code)
	}
	if origin := rec.Header().Get("Access-Control-Allow-Origin"); origin != "http://localhost:5173" {
		t.Errorf("Allow-Origin = %q", origin)
	}
	if methods := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(methods, "GET") {
		t.Errorf("Allow-Methods = %q, want GET", methods)
	}

	// And a real GET from that origin carries the header too.
	get := getBranding(t, srv, PathBranding, "mail.example.com",
		map[string]string{"Origin": "http://localhost:5173"})
	if origin := get.Header().Get("Access-Control-Allow-Origin"); origin != "http://localhost:5173" {
		t.Errorf("GET Allow-Origin = %q", origin)
	}
}
