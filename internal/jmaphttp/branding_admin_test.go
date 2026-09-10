package jmaphttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/GrupoNU/moov/internal/branding"
)

// Tests for the brand administration API (branding_admin.go, BA-1). The
// authenticated user throughout is user@example.com (testAccount); the host
// is brandHost. The theme: an admin can do everything the CLI can, a
// non-admin learns nothing, and every write is visible on the public routes
// immediately.

const brandHost = "mail.acme.example"

// brandAdminServer builds a server with a branding directory and, when
// grant is set, user@example.com granted on brandHost — through the shared
// writer, exactly as `moovctl branding grant` does it.
func brandAdminServer(t *testing.T, grant bool, mutate func(*Config)) (*Server, string, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	if grant {
		d, err := branding.HostDir(root, brandHost)
		if err != nil {
			t.Fatal(err)
		}
		var f branding.File
		if _, err := f.Grant("user@example.com"); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Write(f); err != nil {
			t.Fatal(err)
		}
	}
	logs := &bytes.Buffer{}
	s, _, _, _ := newTestServer(t, func(c *Config) {
		c.BrandingDir = root
		c.Logger = slog.New(slog.NewTextHandler(logs, nil))
		if mutate != nil {
			mutate(c)
		}
	})
	return s, root, logs
}

// adminReq issues one request against brandHost, authenticated unless told
// otherwise.
func adminReq(s *Server, method, path string, body []byte, contentType string, authed bool) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Host = brandHost
	if authed {
		r.SetBasicAuth("user@example.com", testPassword)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func putBrand(s *Server, body string) *httptest.ResponseRecorder {
	return adminReq(s, http.MethodPut, PathBrandingAdminBrand, []byte(body), "application/json", true)
}

func putAsset(s *Server, kind string, body []byte, contentType string) *httptest.ResponseRecorder {
	return adminReq(s, http.MethodPut, "/branding/admin/assets/"+kind, body, contentType, true)
}

func decodeAdminDoc(t *testing.T, w *httptest.ResponseRecorder) BrandAdminDoc {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}
	var doc BrandAdminDoc
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decoding BrandAdminDoc: %v\n%s", err, w.Body.String())
	}
	return doc
}

func fieldError(t *testing.T, w *httptest.ResponseRecorder) brandAdminFieldError {
	t.Helper()
	var fe brandAdminFieldError
	if err := json.Unmarshal(w.Body.Bytes(), &fe); err != nil {
		t.Fatalf("decoding field error: %v\n%s", err, w.Body.String())
	}
	return fe
}

func publicBrand(t *testing.T, s *Server) Branding {
	t.Helper()
	w := adminReq(s, http.MethodGet, PathBranding, nil, "", false)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /branding = %d", w.Code)
	}
	return decodeBranding(t, w)
}

// assertGeneric404 pins the body to the one every unknown resource gets.
func assertGeneric404(t *testing.T, w *httptest.ResponseRecorder, what string) {
	t.Helper()
	if w.Code != http.StatusNotFound {
		t.Fatalf("%s: status = %d, want 404\nbody: %s", what, w.Code, w.Body.String())
	}
	want := `{"detail":"not found","status":404,"type":"about:blank"}`
	if strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("%s: body = %s, want the generic problem %s", what, w.Body.String(), want)
	}
}

// --- authorization matrix ---------------------------------------------------

func TestBrandAdminAnonymousGets401(t *testing.T) {
	s, _, _ := brandAdminServer(t, true, nil)
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, PathBrandingAdmin},
		{http.MethodGet, PathBrandingAdminBrand},
		{http.MethodPut, PathBrandingAdminBrand},
		{http.MethodPut, "/branding/admin/assets/logo"},
		{http.MethodDelete, "/branding/admin/assets/logo"},
		{http.MethodPost, PathBrandingAdminReset},
	} {
		w := adminReq(s, rt.method, rt.path, nil, "", false)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymous = %d, want 401 like every protected route", rt.method, rt.path, w.Code)
		}
		if w.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s %s: no challenge", rt.method, rt.path)
		}
	}
}

func TestBrandAdminNonAdminGetsGeneric404(t *testing.T) {
	// The host IS configured (a brand, no admins): the user must not learn it.
	s, root, logs := brandAdminServer(t, false, nil)
	writeBrand(t, root, brandHost, map[string]any{"name": "Acme"}, nil)
	for _, rt := range []struct {
		method, path string
		body         []byte
	}{
		{http.MethodGet, PathBrandingAdmin, nil},
		{http.MethodGet, PathBrandingAdminBrand, nil},
		{http.MethodPut, PathBrandingAdminBrand, []byte(`{"name":"x"}`)},
		{http.MethodPut, "/branding/admin/assets/logo", testPNGBytes()},
		{http.MethodDelete, "/branding/admin/assets/logo", nil},
		{http.MethodPost, PathBrandingAdminReset, nil},
	} {
		w := adminReq(s, rt.method, rt.path, rt.body, "application/json", true)
		assertGeneric404(t, w, rt.method+" "+rt.path)
	}
	if got := publicBrand(t, s); got.Name != "Acme" {
		t.Fatalf("a non-admin's PUT changed the brand: %+v", got)
	}
	if strings.Contains(logs.String(), "branding admin: write") {
		t.Fatal("a denied request produced an audit line")
	}
}

func TestBrandAdminAdminGets200(t *testing.T) {
	s, _, _ := brandAdminServer(t, true, nil)
	w := adminReq(s, http.MethodGet, PathBrandingAdmin, nil, "", true)
	if w.Code != http.StatusOK {
		t.Fatalf("probe = %d: %s", w.Code, w.Body.String())
	}
	var probe map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &probe)
	if probe["host"] != brandHost || probe["canEdit"] != true {
		t.Fatalf("probe body = %s", w.Body.String())
	}
	doc := decodeAdminDoc(t, adminReq(s, http.MethodGet, PathBrandingAdminBrand, nil, "", true))
	if doc.Host != brandHost || !doc.Default || doc.Name != "" || doc.IconSource != "default" {
		t.Fatalf("fresh doc = %+v", doc)
	}
	if len(doc.BrandAdmins) != 1 || doc.BrandAdmins[0] != "user@example.com" {
		t.Fatalf("brandAdmins = %v", doc.BrandAdmins)
	}
	if doc.Colors != DefaultBranding().Colors {
		t.Fatalf("an unconfigured brand should report Moov's effective colors, got %+v", doc.Colors)
	}
	for _, k := range []string{"logo", "logoDark", "icon", "splash"} {
		if v, ok := doc.Assets[k]; !ok || v != nil {
			t.Errorf("assets[%s] = %v, want an explicit null", k, v)
		}
	}
	if doc.PublicURL != PathBranding || doc.ManifestURL != PathBrandingManifest || len(doc.IconURLs) == 0 {
		t.Fatalf("links = %q %q %v", doc.PublicURL, doc.ManifestURL, doc.IconURLs)
	}
	if doc.Version == "" || strings.Contains(doc.Version, `"`) {
		t.Fatalf("version = %q, want the unquoted ETag", doc.Version)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "" && cc != "no-store" {
		t.Fatalf("probe Cache-Control = %q", cc)
	}
}

// A host the user reaches under a different Host header is not theirs.
func TestBrandAdminIsPerHost(t *testing.T) {
	s, _, _ := brandAdminServer(t, true, nil)
	r := httptest.NewRequest(http.MethodGet, PathBrandingAdmin, nil)
	r.Host = "other.example"
	r.SetBasicAuth("user@example.com", testPassword)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	assertGeneric404(t, w, "admin of another host")
}

// fakeAdminSource is the stand-in for the planned Mailcow provider.
type fakeAdminSource struct {
	calls atomic.Int32
	yes   bool
	err   error
}

func (f *fakeAdminSource) IsBrandAdmin(context.Context, string, string) (bool, error) {
	f.calls.Add(1)
	return f.yes, f.err
}

func TestBrandAdminCompositeORsProviders(t *testing.T) {
	// Not in the file; the second provider says yes -> admin.
	second := &fakeAdminSource{yes: true}
	s, _, _ := brandAdminServer(t, false, func(c *Config) { c.BrandAdminSources = []BrandAdminSource{second} })
	if w := adminReq(s, http.MethodGet, PathBrandingAdmin, nil, "", true); w.Code != http.StatusOK {
		t.Fatalf("second provider's yes was ignored: %d", w.Code)
	}
	if second.calls.Load() == 0 {
		t.Fatal("the second provider was never consulted")
	}

	// In the file: the second provider is not even asked.
	second = &fakeAdminSource{yes: false}
	s, _, _ = brandAdminServer(t, true, func(c *Config) { c.BrandAdminSources = []BrandAdminSource{second} })
	if w := adminReq(s, http.MethodGet, PathBrandingAdmin, nil, "", true); w.Code != http.StatusOK {
		t.Fatalf("file grant = %d", w.Code)
	}
	if second.calls.Load() != 0 {
		t.Fatal("the file's yes should short-circuit the composite")
	}
}

func TestBrandAdminProviderErrorDeniesAndLogs(t *testing.T) {
	failing := &fakeAdminSource{err: errors.New("mailcow unreachable")}
	s, _, logs := brandAdminServer(t, false, func(c *Config) { c.BrandAdminSources = []BrandAdminSource{failing} })
	w := adminReq(s, http.MethodGet, PathBrandingAdmin, nil, "", true)
	assertGeneric404(t, w, "provider error")
	if !strings.Contains(logs.String(), "authorization provider failed") ||
		!strings.Contains(logs.String(), "mailcow unreachable") {
		t.Fatalf("the provider failure was not logged:\n%s", logs.String())
	}
}

func TestBrandAdminDisabledByOperatorIs404(t *testing.T) {
	s, _, _ := brandAdminServer(t, true, func(c *Config) { c.DisableBrandingAdmin = true })
	assertGeneric404(t, adminReq(s, http.MethodGet, PathBrandingAdmin, nil, "", true), "MOOV_BRANDING_ADMIN=0")
	assertGeneric404(t, putBrand(s, `{"name":"x"}`), "MOOV_BRANDING_ADMIN=0 write")
}

func TestBrandAdminWithoutBrandingDirIs404(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	assertGeneric404(t, adminReq(s, http.MethodGet, PathBrandingAdmin, nil, "", true), "no MOOV_BRANDING_DIR")
}

// --- anti-enumeration -------------------------------------------------------

func TestPublicBrandingNeverCarriesBrandAdmins(t *testing.T) {
	s, root, _ := brandAdminServer(t, true, nil)
	writeBrand(t, root, brandHost, map[string]any{"name": "Acme", "brandAdmins": []string{"user@example.com", "boss@acme.example"}}, nil)
	for _, path := range []string{PathBranding, PathBrandingManifest} {
		w := adminReq(s, http.MethodGet, path, nil, "", false)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, w.Code)
		}
		body := strings.ToLower(w.Body.String())
		for _, leak := range []string{"brandadmins", "admin", "user@example.com", "boss@acme.example"} {
			if strings.Contains(body, leak) {
				t.Fatalf("GET %s leaks %q:\n%s", path, leak, w.Body.String())
			}
		}
	}
	// And the public struct itself has no such field, by reflection on its
	// JSON: adding one to Branding would fail here.
	raw, _ := json.Marshal(Branding{})
	if strings.Contains(strings.ToLower(string(raw)), "admin") {
		t.Fatalf("Branding carries an admin field: %s", raw)
	}
}

// --- PUT /brand: validation and partial semantics -----------------------------

func TestBrandAdminPutValidatesEveryFieldWithoutWriting(t *testing.T) {
	s, _, _ := brandAdminServer(t, true, nil)
	if w := putBrand(s, `{"name":"Acme Mail","tagline":"hello"}`); w.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}
	long := strings.Repeat("x", 65)
	cases := []struct {
		body, field, reason string
	}{
		{`{"name":"` + long + `"}`, "name", "limit is 64"},
		{`{"shortName":"Corporate Mailbox"}`, "shortName", "limit is 12"},
		{`{"tagline":"` + strings.Repeat("y", 161) + `"}`, "tagline", "limit is 160"},
		{`{"supportUrl":"javascript:alert(1)"}`, "supportUrl", "https://, http:// or mailto:"},
		{`{"privacyUrl":"ftp://x"}`, "privacyUrl", "https://, http:// or mailto:"},
		{`{"termsUrl":"//evil"}`, "termsUrl", "https://, http:// or mailto:"},
		{`{"name":"a\u0000b"}`, "name", "control characters"},
		{`{"colors":{"primary":"red"}}`, "colors.primary", "CSS hex color"},
		{`{"colors":{"onPrimary":"#12345"}}`, "colors.onPrimary", "CSS hex color"},
		{`{"colors":{"splashFrom":"#ggg"}}`, "colors.splashFrom", "CSS hex color"},
		{`{"colors":{"splashTo":"123456"}}`, "colors.splashTo", "CSS hex color"},
		// A valid field before an invalid one: still nothing written.
		{`{"name":"Changed","colors":{"primary":"nope"}}`, "colors.primary", "CSS hex color"},
	}
	for _, c := range cases {
		w := putBrand(s, c.body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400 (%s)", c.body, w.Code, w.Body.String())
			continue
		}
		fe := fieldError(t, w)
		if fe.Field != c.field || !strings.Contains(fe.Reason, c.reason) {
			t.Errorf("PUT %s -> %+v, want field %q reason containing %q", c.body, fe, c.field, c.reason)
		}
	}
	// Nothing was written by any of them.
	doc := decodeAdminDoc(t, adminReq(s, http.MethodGet, PathBrandingAdminBrand, nil, "", true))
	if doc.Name != "Acme Mail" || doc.Tagline != "hello" || doc.Colors != DefaultBranding().Colors {
		t.Fatalf("a refused PUT changed the brand: %+v", doc)
	}

	// Shape errors.
	if w := putBrand(s, `{"color":{"primary":"#000"}}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400", w.Code)
	}
	if w := putBrand(s, `not json`); w.Code != http.StatusBadRequest {
		t.Errorf("garbage = %d, want 400", w.Code)
	}
	if w := adminReq(s, http.MethodPut, PathBrandingAdminBrand, []byte(`{"name":"x"}`), "text/plain", true); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain = %d, want 415", w.Code)
	}
	big := `{"name":"` + strings.Repeat("z", maxBrandAdminBodyBytes) + `"}`
	if w := putBrand(s, big); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize body = %d, want 413", w.Code)
	}
}

func TestBrandAdminPutPartialSemantics(t *testing.T) {
	s, _, _ := brandAdminServer(t, true, nil)
	doc := decodeAdminDoc(t, putBrand(s, `{"name":"Acme Mail","shortName":"Acme","tagline":"Correo","supportUrl":"mailto:help@acme.example","colors":{"primary":"#0F766E","splashTo":"#115e59"}}`))
	if doc.Name != "Acme Mail" || doc.ShortName != "Acme" || doc.Tagline != "Correo" ||
		doc.SupportURL != "mailto:help@acme.example" || doc.Colors.Primary != "#0f766e" || doc.Colors.SplashTo != "#115e59" ||
		doc.Colors.OnPrimary != "#ffffff" || doc.Default {
		t.Fatalf("after full PUT: %+v", doc)
	}

	// Absent = unchanged; "" = cleared; a cleared color is Moov's again.
	doc = decodeAdminDoc(t, putBrand(s, `{"tagline":"","colors":{"primary":""}}`))
	if doc.Name != "Acme Mail" || doc.ShortName != "Acme" || doc.SupportURL != "mailto:help@acme.example" {
		t.Fatalf("absent fields changed: %+v", doc)
	}
	if doc.Tagline != "" || doc.Colors.Primary != "#5b5bd6" || doc.Colors.SplashTo != "#115e59" {
		t.Fatalf("cleared fields: %+v", doc)
	}
	// An empty patch is a no-op 200.
	doc = decodeAdminDoc(t, putBrand(s, `{}`))
	if doc.Name != "Acme Mail" {
		t.Fatalf("empty patch changed the brand: %+v", doc)
	}
	// The public document agrees, right now.
	if pub := publicBrand(t, s); pub.Name != "Acme Mail" || pub.Tagline != "" || pub.Colors.Primary != "#5b5bd6" {
		t.Fatalf("public document lags the write: %+v", pub)
	}
}

// TestBrandAdminDocReportsWhichColorsAreConfigured: `colors` is the EFFECTIVE
// palette (a picker needs a color), so the panel cannot tell a value the
// operator typed from one the server derived or defaulted. ColorsConfigured is
// how it tells — and it is what lets the splash fields show the derived
// gradient as a PLACEHOLDER instead of as text the operator seems to have
// entered.
func TestBrandAdminDocReportsWhichColorsAreConfigured(t *testing.T) {
	s, _, _ := brandAdminServer(t, true, nil)

	doc := decodeAdminDoc(t, adminReq(s, http.MethodGet, PathBrandingAdminBrand, nil, "", true))
	if len(doc.ColorsConfigured) != 0 {
		t.Fatalf("an unconfigured host reports %v as configured", doc.ColorsConfigured)
	}

	doc = decodeAdminDoc(t, putBrand(s, `{"colors":{"primary":"#b8faff"}}`))
	if got := strings.Join(doc.ColorsConfigured, ","); got != "primary" {
		t.Fatalf("configured = %q, want just the primary", got)
	}
	// The gradient the panel will PLACEHOLD is the derived one, and it is the
	// one the public document serves.
	wantFrom, wantTo := branding.DeriveSplashColors("#b8faff")
	if doc.Colors.SplashFrom != wantFrom || doc.Colors.SplashTo != wantTo {
		t.Fatalf("gradient = %q -> %q, want the derived %q -> %q",
			doc.Colors.SplashFrom, doc.Colors.SplashTo, wantFrom, wantTo)
	}

	doc = decodeAdminDoc(t, putBrand(s, `{"colors":{"splashFrom":"#001122"}}`))
	if got := strings.Join(doc.ColorsConfigured, ","); got != "primary,splashFrom" {
		t.Fatalf("configured = %q, want the primary and the from stop", got)
	}

	// Clearing returns the field to automatic, in the list and in the value.
	doc = decodeAdminDoc(t, putBrand(s, `{"colors":{"splashFrom":""}}`))
	if got := strings.Join(doc.ColorsConfigured, ","); got != "primary" {
		t.Fatalf("configured = %q after clearing the from stop", got)
	}
	if doc.Colors.SplashFrom != wantFrom {
		t.Fatalf("splashFrom = %q after clearing, want the derived %q", doc.Colors.SplashFrom, wantFrom)
	}
}

// --- assets ---------------------------------------------------------------------

func testPNGBytes() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T',
		0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05,
		0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00,
		0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
	}
}

func TestBrandAdminUploadEveryKind(t *testing.T) {
	s, root, logs := brandAdminServer(t, true, nil)
	square := encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 64, 64)))
	for kind, file := range map[string]string{"logo": "logo.png", "logoDark": "logo-dark.png", "icon": "icon.png", "splash": "splash.png"} {
		doc := decodeAdminDoc(t, putAsset(s, kind, square, "image/png"))
		a := doc.Assets[kind]
		if a == nil {
			t.Fatalf("%s: asset missing from the doc: %+v", kind, doc)
		}
		wantPrefix := "/branding/assets/" + brandHost + "/" + file + "?v="
		if !strings.HasPrefix(a.URL, wantPrefix) || len(a.URL) != len(wantPrefix)+16 {
			t.Fatalf("%s: url = %q, want %s<16 hex>", kind, a.URL, wantPrefix)
		}
		if a.Bytes != len(square) || a.Width != 64 || a.Height != 64 {
			t.Fatalf("%s: asset = %+v", kind, a)
		}
		if _, err := os.Stat(filepath.Join(root, brandHost, file)); err != nil {
			t.Fatalf("%s: %s not written: %v", kind, file, err)
		}
		if len(doc.Warnings) != 0 {
			t.Fatalf("%s: unexpected warnings %v", kind, doc.Warnings)
		}
		// The public asset route serves it WITH the cache-buster in the URL.
		w := adminReq(s, http.MethodGet, a.URL, nil, "", false)
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), square) {
			t.Fatalf("%s: GET %s = %d (%d bytes)", kind, a.URL, w.Code, w.Body.Len())
		}
	}
	pub := publicBrand(t, s)
	if pub.LogoURL == "" || pub.LogoDarkURL == "" || pub.IconURL == "" || pub.SplashURL == "" {
		t.Fatalf("public document does not advertise the uploads: %+v", pub)
	}
	doc := decodeAdminDoc(t, adminReq(s, http.MethodGet, PathBrandingAdminBrand, nil, "", true))
	if doc.IconSource != "icon" {
		t.Fatalf("iconSource = %q, want icon", doc.IconSource)
	}
	// Replacing under another type renames the file and drops the old one.
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("JFIF stub")...)
	doc = decodeAdminDoc(t, putAsset(s, "splash", jpeg, "image/jpeg"))
	if !strings.Contains(doc.Assets["splash"].URL, "/splash.jpg?v=") {
		t.Fatalf("splash url = %q", doc.Assets["splash"].URL)
	}
	if _, err := os.Stat(filepath.Join(root, brandHost, "splash.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("splash.png left behind after splash.jpg replaced it")
	}
	// Audit lines: one per write, with the digest and never the bytes.
	if n := strings.Count(logs.String(), `action=put-asset`); n != 5 {
		t.Fatalf("audit lines = %d, want 5:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "sha256=") || !strings.Contains(logs.String(), "actor=user@example.com") {
		t.Fatalf("audit line lacks digest or actor:\n%s", logs.String())
	}
}

func TestBrandAdminUploadRefusals(t *testing.T) {
	s, root, _ := brandAdminServer(t, true, nil)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	html := []byte(`<html><body>not a png</body></html>`)
	cases := []struct {
		name        string
		kind        string
		body        []byte
		contentType string
		status      int
		reason      string
	}{
		{"svg as image/svg+xml", "logo", svg, "image/svg+xml", http.StatusUnsupportedMediaType, "SVG is not accepted"},
		{"svg declared as png", "logo", svg, "image/png", http.StatusUnsupportedMediaType, "SVG is not accepted"},
		{"html declared as png", "icon", html, "image/png", http.StatusUnsupportedMediaType, "bytes were checked"},
		{"json body", "logo", []byte(`{"logo":"x"}`), "application/json", http.StatusUnsupportedMediaType, "image/*"},
		{"no content type", "logo", testPNGBytes(), "", http.StatusUnsupportedMediaType, "image/*"},
		{"empty", "splash", []byte{}, "image/png", http.StatusBadRequest, "empty"},
		{"oversize", "splash", bytes.Repeat([]byte{0x89}, branding.MaxAssetBytes+1), "image/png", http.StatusRequestEntityTooLarge, "larger than"},
	}
	for _, c := range cases {
		w := putAsset(s, c.kind, c.body, c.contentType)
		if w.Code != c.status {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, w.Code, c.status, w.Body.String())
			continue
		}
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if reason, _ := body["reason"].(string); !strings.Contains(reason, c.reason) {
			t.Errorf("%s: reason = %q, want it to contain %q", c.name, reason, c.reason)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(root, brandHost))
	for _, e := range entries {
		if e.Name() != branding.ConfigFile {
			t.Errorf("a refused upload left %s behind", e.Name())
		}
	}
	// An unknown kind is the generic 404, not a 400 that lists the kinds.
	assertGeneric404(t, putAsset(s, "favicon", testPNGBytes(), "image/png"), "unknown kind")
}

func TestBrandAdminNonSquareIconWarnsLikeTheCLI(t *testing.T) {
	s, _, _ := brandAdminServer(t, true, nil)
	wide := encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 200, 40)))
	doc := decodeAdminDoc(t, putAsset(s, "icon", wide, "image/png"))
	want := "the icon is 200x40, which is not square; launchers show a square, so it will be contained inside one with bands of the primary color around it"
	if len(doc.Warnings) != 1 || doc.Warnings[0] != want {
		t.Fatalf("warnings = %v, want exactly [%s]", doc.Warnings, want)
	}
	// A WebP logo: accepted as an asset, declared unusable for the icons.
	doc = decodeAdminDoc(t, putAsset(s, "logo", webpBytes(), "image/webp"))
	joined := strings.Join(doc.Warnings, " | ")
	if !strings.Contains(joined, "will not be rendered from this logo") || !strings.Contains(joined, "WebP") {
		t.Fatalf("webp logo warnings = %v", doc.Warnings)
	}
	if doc.IconSource != "icon" {
		t.Fatalf("iconSource = %q; the wide icon still wins over an unusable logo", doc.IconSource)
	}
}

func TestBrandAdminUploadInvalidatesIcons(t *testing.T) {
	s, _, _ := brandAdminServer(t, true, nil)
	before := adminReq(s, http.MethodGet, "/branding/icons/icon-192.png", nil, "", false)
	if before.Code != http.StatusOK {
		t.Fatalf("icon before = %d", before.Code)
	}
	glyph := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := range glyph.Pix {
		glyph.Pix[i] = 0xff
	}
	doc := decodeAdminDoc(t, putAsset(s, "icon", encodePNG(t, glyph), "image/png"))
	after := adminReq(s, http.MethodGet, "/branding/icons/icon-192.png", nil, "", false)
	if after.Code != http.StatusOK || bytes.Equal(after.Body.Bytes(), before.Body.Bytes()) {
		t.Fatal("the icon route still serves the pre-upload icon: the icon cache was not invalidated")
	}
	if after.Header().Get("ETag") == before.Header().Get("ETag") {
		t.Fatal("icon ETag unchanged")
	}
	// And the doc's icon URLs carry a buster that changed with the source.
	if !strings.Contains(doc.IconURLs["icon-192"], "?v=") {
		t.Fatalf("iconUrls = %v, want a cache-buster once a source exists", doc.IconURLs)
	}
}

func TestBrandAdminDeleteAsset(t *testing.T) {
	s, root, _ := brandAdminServer(t, true, nil)
	decodeAdminDoc(t, putAsset(s, "logo", testPNGBytes(), "image/png"))
	if publicBrand(t, s).LogoURL == "" {
		t.Fatal("seed: logo not advertised")
	}
	doc := decodeAdminDoc(t, adminReq(s, http.MethodDelete, "/branding/admin/assets/logo", nil, "", true))
	if doc.Assets["logo"] != nil {
		t.Fatalf("logo still in the doc: %+v", doc.Assets["logo"])
	}
	if _, err := os.Stat(filepath.Join(root, brandHost, "logo.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("logo.png not removed")
	}
	if publicBrand(t, s).LogoURL != "" {
		t.Fatal("public document still advertises the deleted logo")
	}
	// Idempotent.
	if w := adminReq(s, http.MethodDelete, "/branding/admin/assets/logo", nil, "", true); w.Code != http.StatusOK {
		t.Fatalf("second delete = %d", w.Code)
	}
}

func TestBrandAdminResetPreservesAdmins(t *testing.T) {
	s, root, _ := brandAdminServer(t, true, nil)
	decodeAdminDoc(t, putBrand(s, `{"name":"Acme","colors":{"primary":"#000000"}}`))
	decodeAdminDoc(t, putAsset(s, "logo", testPNGBytes(), "image/png"))
	if publicBrand(t, s).Default {
		t.Fatal("seed: brand not applied")
	}

	doc := decodeAdminDoc(t, adminReq(s, http.MethodPost, PathBrandingAdminReset, nil, "", true))
	if !doc.Default || doc.Name != "" || doc.Assets["logo"] != nil || doc.Colors != DefaultBranding().Colors {
		t.Fatalf("after reset: %+v", doc)
	}
	if len(doc.BrandAdmins) != 1 || doc.BrandAdmins[0] != "user@example.com" {
		t.Fatalf("reset dropped the admins: %v", doc.BrandAdmins)
	}
	if _, err := os.Stat(filepath.Join(root, brandHost, "logo.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reset left logo.png")
	}
	pub := publicBrand(t, s)
	if !pub.Default || pub.Name != "Moov Mail" {
		t.Fatalf("public document after reset: %+v", pub)
	}
	// The admin still has access to build it again.
	if w := adminReq(s, http.MethodGet, PathBrandingAdmin, nil, "", true); w.Code != http.StatusOK {
		t.Fatalf("admin lost access after reset: %d", w.Code)
	}
	decodeAdminDoc(t, putBrand(s, `{"name":"Acme again"}`))
}

// --- budget and concurrency -------------------------------------------------------

func TestBrandAdminWriteBudget(t *testing.T) {
	s, _, _ := brandAdminServer(t, true, nil)
	for i := 0; i < brandAdminWritesPerMinute; i++ {
		if w := putBrand(s, `{}`); w.Code != http.StatusOK {
			t.Fatalf("write %d = %d, want 200", i+1, w.Code)
		}
	}
	w := putBrand(s, `{}`)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("write %d = %d, want 429", brandAdminWritesPerMinute+1, w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	// Reads are not budgeted.
	if w := adminReq(s, http.MethodGet, PathBrandingAdminBrand, nil, "", true); w.Code != http.StatusOK {
		t.Fatalf("read during exhaustion = %d", w.Code)
	}
}

func TestBrandAdminConcurrentWritesAreSerialized(t *testing.T) {
	s, root, _ := brandAdminServer(t, true, nil)
	var wg sync.WaitGroup
	codes := make([]int, brandAdminWritesPerMinute)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := `{"name":"Writer ` + string(rune('A'+i)) + `","tagline":"t` + string(rune('A'+i)) + `"}`
			codes[i] = putBrand(s, body).Code
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("concurrent write %d = %d", i, c)
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, brandHost, branding.ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	var f branding.File
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("branding.json torn: %v\n%s", err, raw)
	}
	// Whichever writer won, its two fields landed TOGETHER.
	if !strings.HasPrefix(f.Name, "Writer ") || f.Tagline != "t"+f.Name[len("Writer "):] {
		t.Fatalf("interleaved read-modify-write: %+v", f)
	}
	if len(f.BrandAdmins) != 1 {
		t.Fatalf("the admin list was lost in the race: %+v", f)
	}
}

func TestWriteLimiterRefills(t *testing.T) {
	clock := newFakeClock()
	l := newWriteLimiter(2, 60e9, clock.Now)
	if _, ok := l.allow("a"); !ok {
		t.Fatal("first")
	}
	if _, ok := l.allow("a"); !ok {
		t.Fatal("second")
	}
	wait, ok := l.allow("a")
	if ok || wait <= 0 || wait > 30e9 {
		t.Fatalf("third: ok=%v wait=%v", ok, wait)
	}
	if _, ok := l.allow("b"); !ok {
		t.Fatal("another key has its own bucket")
	}
	clock.Advance(wait)
	if _, ok := l.allow("a"); !ok {
		t.Fatal("no refill after the advertised wait")
	}
}

// The store forgets a host's document and icons on invalidate, and nothing
// else's.
func TestBrandingStoreInvalidateIsPerHost(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "a.example", map[string]any{"name": "A", "icon": "icon.png"}, map[string][]byte{"icon.png": testPNGBytes()})
	writeBrand(t, root, "b.example", map[string]any{"name": "B"}, nil)
	store := newBrandingStore(root, discardLogger(), nil)
	store.resolve("a.example")
	store.resolve("b.example")
	store.icon("a.example", brandingIconSpecs[0])
	store.invalidate("a.example")
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.cache["a.example"]; ok {
		t.Fatal("a.example still cached")
	}
	if _, ok := store.cache["b.example"]; !ok {
		t.Fatal("b.example was evicted too")
	}
	for k := range store.icons {
		if strings.HasPrefix(k, "a.example\x00") {
			t.Fatalf("icon %q still cached", k)
		}
	}
}
