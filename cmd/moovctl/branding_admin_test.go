package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/branding"
	"github.com/GrupoNU/moov/internal/jmaphttp"
	"github.com/GrupoNU/moov/internal/store"
)

// Tests for `moovctl branding grant/revoke` and for the one property that
// justifies internal/branding: the CLI and the admin API are two callers of
// ONE writer, so the same scenario through both produces the same directory.

func TestBrandingGrantRevokeRoundTrip(t *testing.T) {
	root := t.TempDir()

	// Granting on a host with no branding creates an admin-only document.
	code, stdout, stderr := runCLI(t, "", "branding", "grant", "-dir", root, "-host", "Mail.ACME.test", "-user", "Ana@Acme.Test")
	if code != exitOK {
		t.Fatalf("grant exit = %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "Granted ana@acme.test") || !strings.Contains(stdout, "mail.acme.test") {
		t.Fatalf("grant stdout:\n%s", stdout)
	}
	doc := readDoc(t, root, "mail.acme.test")
	if len(doc.BrandAdmins) != 1 || doc.BrandAdmins[0] != "ana@acme.test" || doc.HasBrand() {
		t.Fatalf("after grant: %+v", doc)
	}

	// Idempotent; a second admin sorts in.
	if code, stdout, _ = runCLI(t, "", "branding", "grant", "-dir", root, "-host", "mail.acme.test", "-user", "ana@acme.test"); code != exitOK || !strings.Contains(stdout, "already") {
		t.Fatalf("re-grant: exit %d\n%s", code, stdout)
	}
	if code, _, stderr = runCLI(t, "", "branding", "grant", "-dir", root, "-host", "mail.acme.test", "-user", "abel@acme.test"); code != exitOK {
		t.Fatalf("second grant: %d\n%s", code, stderr)
	}
	if got := strings.Join(readDoc(t, root, "mail.acme.test").BrandAdmins, ","); got != "abel@acme.test,ana@acme.test" {
		t.Fatalf("admins = %q", got)
	}

	// show and list surface the list.
	_, stdout, _ = runCLI(t, "", "branding", "show", "-dir", root, "-host", "mail.acme.test")
	if !strings.Contains(stdout, "BRAND ADMINS") || !strings.Contains(stdout, "abel@acme.test, ana@acme.test") {
		t.Fatalf("show lacks the admins row:\n%s", stdout)
	}
	_, stdout, _ = runCLI(t, "", "branding", "list", "-dir", root)
	if !strings.Contains(stdout, "BRAND ADMINS") || !strings.Contains(stdout, "abel@acme.test, ana@acme.test") {
		t.Fatalf("list lacks the admins column:\n%s", stdout)
	}

	// A grant does not disturb an existing brand, and set does not disturb
	// the grants.
	if code, _, stderr = runCLI(t, "", "branding", "set", "-dir", root, "-host", "mail.acme.test", "-name", "Acme"); code != exitOK {
		t.Fatalf("set: %d\n%s", code, stderr)
	}
	doc = readDoc(t, root, "mail.acme.test")
	if doc.Name != "Acme" || len(doc.BrandAdmins) != 2 {
		t.Fatalf("set lost the grants or the grant lost the name: %+v", doc)
	}

	// Revoke.
	if code, stdout, _ = runCLI(t, "", "branding", "revoke", "-dir", root, "-host", "mail.acme.test", "-user", "ANA@acme.test"); code != exitOK || !strings.Contains(stdout, "Revoked") {
		t.Fatalf("revoke: %d\n%s", code, stdout)
	}
	if code, stdout, _ = runCLI(t, "", "branding", "revoke", "-dir", root, "-host", "mail.acme.test", "-user", "nobody@acme.test"); code != exitOK || !strings.Contains(stdout, "is not a brand admin") {
		t.Fatalf("revoke of a stranger: %d\n%s", code, stdout)
	}
	doc = readDoc(t, root, "mail.acme.test")
	if len(doc.BrandAdmins) != 1 || doc.BrandAdmins[0] != "abel@acme.test" || doc.Name != "Acme" {
		t.Fatalf("after revoke: %+v", doc)
	}

	// Refusals are usage errors.
	for _, args := range [][]string{
		{"branding", "grant", "-dir", root, "-host", "mail.acme.test", "-user", "not-a-mailbox"},
		{"branding", "grant", "-dir", root, "-host", "mail.acme.test"},
		{"branding", "grant", "-dir", root, "-user", "ana@acme.test"},
		{"branding", "revoke", "-dir", root, "-host", "bad host", "-user", "ana@acme.test"},
	} {
		if code, _, _ := runCLI(t, "", args...); code != exitUsage {
			t.Errorf("%v: exit = %d, want %d", args, code, exitUsage)
		}
	}
}

// --- CLI/API writer parity ---------------------------------------------------

type parityValidator struct{}

func (parityValidator) Validate(_ context.Context, username, password string) (bool, error) {
	return username == "ana@acme.test" && password == "pw", nil
}

type parityDirectory struct{}

func (parityDirectory) account() store.Account {
	return store.Account{ID: 1, Email: "ana@acme.test", State: store.AccountActive, UpdatedAt: time.Unix(0, 0)}
}

func (d parityDirectory) GetAccountByEmail(_ context.Context, email string) (store.Account, error) {
	if email != "ana@acme.test" {
		return store.Account{}, fmt.Errorf("%s: %w", email, store.ErrNotFound)
	}
	return d.account(), nil
}

func (d parityDirectory) GetAccount(_ context.Context, id int64) (store.Account, error) {
	if id != 1 {
		return store.Account{}, store.ErrNotFound
	}
	return d.account(), nil
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestCLIAndAPIWriteTheSameDirectory runs one full scenario — every text
// field, every color, all four assets, one admin — through `moovctl branding`
// into one root and through the HTTP admin API into another, then diffs the
// two host directories byte for byte.
func TestCLIAndAPIWriteTheSameDirectory(t *testing.T) {
	const host = "mail.acme.test"
	logo, logoDark, icon := pngOf(t, 300, 80), pngOf(t, 300, 80), pngOf(t, 200, 40)
	splash := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("JFIF parity")...)

	// --- through the CLI ---
	cliRoot := t.TempDir()
	logoPath := writeTempImage(t, "logo.png", logo)
	logoDarkPath := writeTempImage(t, "logo-dark.png", logoDark)
	iconPath := writeTempImage(t, "icon.png", icon)
	splashPath := writeTempImage(t, "splash.jpg", splash)
	code, stdout, stderr := runCLI(t, "", "branding", "set", "-dir", cliRoot, "-host", host,
		"-name", "Acme Mail", "-short-name", "Acme", "-tagline", "Correo de Acme",
		"-support-url", "mailto:it@acme.test", "-privacy-url", "https://acme.test/privacy", "-terms-url", "https://acme.test/terms",
		"-color-primary", "#0F766E", "-color-on-primary", "#FFFFFF", "-color-splash-from", "#042f2e", "-color-splash-to", "#115E59",
		"-logo", logoPath, "-logo-dark", logoDarkPath, "-icon", iconPath, "-splash", splashPath)
	if code != exitOK {
		t.Fatalf("cli set: %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "Warning: the icon is 200x40, which is not square") {
		t.Fatalf("cli did not warn about the wide icon:\n%s", stdout)
	}
	if code, _, stderr = runCLI(t, "", "branding", "grant", "-dir", cliRoot, "-host", host, "-user", "ana@acme.test"); code != exitOK {
		t.Fatalf("cli grant: %d\n%s", code, stderr)
	}

	// --- through the API ---
	apiRoot := t.TempDir()
	if code, _, stderr = runCLI(t, "", "branding", "grant", "-dir", apiRoot, "-host", host, "-user", "ana@acme.test"); code != exitOK {
		t.Fatalf("api-side grant: %d\n%s", code, stderr)
	}
	auth, err := jmaphttp.NewAuthenticator(jmaphttp.AuthConfig{Validator: parityValidator{}, Directory: parityDirectory{}})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := jmaphttp.New(jmaphttp.Config{BrandingDir: apiRoot}, auth)
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()
	do := func(method, path string, body []byte, contentType string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Host = host
		r.SetBasicAuth("ana@acme.test", "pw")
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s = %d\n%s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	do(http.MethodPut, jmaphttp.PathBrandingAdminBrand, []byte(`{
		"name":"Acme Mail","shortName":"Acme","tagline":"Correo de Acme",
		"supportUrl":"mailto:it@acme.test","privacyUrl":"https://acme.test/privacy","termsUrl":"https://acme.test/terms",
		"colors":{"primary":"#0F766E","onPrimary":"#FFFFFF","splashFrom":"#042f2e","splashTo":"#115E59"}}`), "application/json")
	do(http.MethodPut, "/branding/admin/assets/logo", logo, "image/png")
	do(http.MethodPut, "/branding/admin/assets/logoDark", logoDark, "image/png")
	w := do(http.MethodPut, "/branding/admin/assets/icon", icon, "image/png")
	var doc jmaphttp.BrandAdminDoc
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Warnings) != 1 || !strings.Contains(doc.Warnings[0], "the icon is 200x40, which is not square") {
		t.Fatalf("api warnings = %v; the CLI printed the same sentence", doc.Warnings)
	}
	do(http.MethodPut, "/branding/admin/assets/splash", splash, "image/jpeg")

	// --- diff ---
	cliFiles, apiFiles := dirDigest(t, filepath.Join(cliRoot, host)), dirDigest(t, filepath.Join(apiRoot, host))
	if len(cliFiles) != 5 {
		t.Fatalf("cli dir = %v, want branding.json + 4 assets", keys(cliFiles))
	}
	for name, sum := range cliFiles {
		if apiFiles[name] != sum {
			raw1, _ := os.ReadFile(filepath.Join(cliRoot, host, name))
			raw2, _ := os.ReadFile(filepath.Join(apiRoot, host, name))
			t.Errorf("%s differs between CLI and API\ncli: %s\napi: %s", name, raw1, raw2)
		}
	}
	for name := range apiFiles {
		if _, ok := cliFiles[name]; !ok {
			t.Errorf("API wrote %s, the CLI did not", name)
		}
	}
	// Both are what the server reads, and they resolve identically.
	var cliDoc, apiDoc branding.File
	if raw, err := os.ReadFile(filepath.Join(cliRoot, host, branding.ConfigFile)); err == nil {
		_ = json.Unmarshal(raw, &cliDoc)
	}
	if raw, err := os.ReadFile(filepath.Join(apiRoot, host, branding.ConfigFile)); err == nil {
		_ = json.Unmarshal(raw, &apiDoc)
	}
	if fmt.Sprintf("%+v", cliDoc) != fmt.Sprintf("%+v", apiDoc) {
		t.Fatalf("documents differ:\ncli: %+v\napi: %+v", cliDoc, apiDoc)
	}
}

func dirDigest(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	return out
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
