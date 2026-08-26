package jmaphttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestBrandingServesWhatMoovctlWrote closes the loop the two unit suites leave
// open: cmd/moovctl's tests prove the CLI writes a directory, and branding_test
// proves the server reads one, but neither proves the SERVER reads what the CLI
// WROTE. This runs against a directory produced by the real binary.
//
// It is env-gated so it never runs in CI without the fixture.
func TestBrandingServesWhatMoovctlWrote(t *testing.T) {
	dir := os.Getenv("MOOV_TEST_BRANDING_DIR")
	if dir == "" {
		t.Skip("set MOOV_TEST_BRANDING_DIR to a directory written by `moovctl branding set`")
	}

	srv := brandingServer(t, dir)

	req := httptest.NewRequest(http.MethodGet, PathBranding, nil)
	req.Host = "mail.areacorp.test"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var doc Branding
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	t.Logf("served document: %+v", doc)

	if doc.Name != "Areacorp Mail" {
		t.Errorf("name = %q", doc.Name)
	}
	if doc.Colors.Primary != "#0f7b6c" {
		t.Errorf("primary = %q", doc.Colors.Primary)
	}
	if doc.Default {
		t.Error("a configured host was flagged default")
	}
	if doc.LogoURL != "/branding/assets/mail.areacorp.test/logo.png" {
		t.Fatalf("logoUrl = %q", doc.LogoURL)
	}

	// And the advertised URL really serves image bytes.
	assetReq := httptest.NewRequest(http.MethodGet, doc.LogoURL, nil)
	assetReq.Host = "mail.areacorp.test"
	assetRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(assetRec, assetReq)

	if assetRec.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", assetRec.Code)
	}
	if ct := assetRec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("asset Content-Type = %q, want image/png", ct)
	}
	t.Logf("asset: %d bytes, %s, nosniff=%q",
		assetRec.Body.Len(), assetRec.Header().Get("Content-Type"),
		assetRec.Header().Get("X-Content-Type-Options"))
}
