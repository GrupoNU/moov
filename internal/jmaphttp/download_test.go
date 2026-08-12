package jmaphttp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap/mail"
)

// The blob download route (J2). The authorization shape — every failure is the
// same 404 — was fixed when the route was stubbed; these tests hold it to that
// now that the route actually serves bytes.

// fakeBlobs is a BlobReader over an in-memory map, scoped by account.
type fakeBlobs struct {
	// byAccount[accountID][blobID] = content
	byAccount map[int64]map[string][]byte
	err       error
}

func (f *fakeBlobs) OpenBlob(_ context.Context, accountID int64, blobID string) (io.ReadCloser, int64, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	content, ok := f.byAccount[accountID][blobID]
	if !ok {
		return nil, 0, mail.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(content)), int64(len(content)), nil
}

// newDownloadServer builds a server whose download route is backed by blobs
// owned by the test account (id 7, matching testAccount()).
func newDownloadServer(t *testing.T, blobs *fakeBlobs) *Server {
	t.Helper()
	s, _, _, _ := newTestServer(t, func(c *Config) { c.Blobs = blobs })
	return s
}

func TestDownloadServesOwnedBlob(t *testing.T) {
	content := []byte("hello, this is the raw message")
	s := newDownloadServer(t, &fakeBlobs{byAccount: map[int64]map[string][]byte{
		7: {"abc123": content},
	}})

	w := doReq(s, http.MethodGet, "/jmap/download/a7/abc123/message.txt", "", true, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body)
	}
	if got := w.Body.String(); got != string(content) {
		t.Errorf("body = %q", got)
	}
	if got := w.Header().Get("Content-Length"); got != strconv.Itoa(len(content)) {
		t.Errorf("Content-Length = %q, want %d", got, len(content))
	}
	// Safe headers on every response, allowlisted or not.
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "private") || !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q: other people's mail must not be cached by a shared cache", cc)
	}
	// Phase 1 serves whole blobs only, and says so.
	if got := w.Header().Get("Accept-Ranges"); got != "none" {
		t.Errorf("Accept-Ranges = %q, want none", got)
	}
}

// THE no-oracle test: a blob of another account, a blob that does not exist,
// and a malformed accountId must all be one indistinguishable answer.
func TestDownloadForeignBlobIsIndistinguishableFromMissing(t *testing.T) {
	s := newDownloadServer(t, &fakeBlobs{byAccount: map[int64]map[string][]byte{
		7: {"mine": []byte("my mail")},
		8: {"theirs": []byte("somebody else's mail")},
	}})

	// The baseline: a blob that genuinely does not exist.
	missing := doReq(s, http.MethodGet, "/jmap/download/a7/nosuchblob/x.txt", "", true, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing blob = %d", missing.Code)
	}

	cases := map[string]string{
		"another account's blob id":       "/jmap/download/a7/theirs/x.txt",
		"another account's id and blob":   "/jmap/download/a8/theirs/x.txt",
		"another account's id, own blob":  "/jmap/download/a8/mine/x.txt",
		"a malformed account id":          "/jmap/download/zzz/mine/x.txt",
		"an account id that cannot exist": "/jmap/download/a999999/mine/x.txt",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			w := doReq(s, http.MethodGet, path, "", true, nil)
			if w.Code != missing.Code {
				t.Errorf("status = %d, want %d (identical to a missing blob)", w.Code, missing.Code)
			}
			if w.Body.String() != missing.Body.String() {
				t.Errorf("body = %q, want %q: the response distinguishes existence",
					w.Body.String(), missing.Body.String())
			}
		})
	}

	// And the owned blob really is served, so the test above is not vacuous.
	ok := doReq(s, http.MethodGet, "/jmap/download/a7/mine/x.txt", "", true, nil)
	if ok.Code != http.StatusOK {
		t.Fatalf("the caller's own blob = %d, want 200", ok.Code)
	}
}

// An internal reader failure must not be distinguishable either: a 500 would
// tell a prober that this blob id is different from the others.
func TestDownloadInternalErrorIsAlsoAPlain404(t *testing.T) {
	s := newDownloadServer(t, &fakeBlobs{err: io.ErrUnexpectedEOF})
	w := doReq(s, http.MethodGet, "/jmap/download/a7/anything/x.txt", "", true, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDownloadContentTypeAllowlist(t *testing.T) {
	s := newDownloadServer(t, &fakeBlobs{byAccount: map[int64]map[string][]byte{
		7: {"b": []byte("<script>alert(1)</script>")},
	}})

	cases := []struct {
		query      string
		wantType   string
		wantInline bool
	}{
		{"text/plain", "text/plain", true},
		{"image/png", "image/png", true},
		{"application/pdf", "application/pdf", true},
		// The attack this allowlist exists for.
		{"text/html", "application/octet-stream", false},
		{"image/svg+xml", "application/octet-stream", false},
		{"", "application/octet-stream", false},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			path := "/jmap/download/a7/b/f.dat"
			if tc.query != "" {
				path += "?type=" + tc.query
			}
			w := doReq(s, http.MethodGet, path, "", true, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			if got := w.Header().Get("Content-Type"); got != tc.wantType {
				t.Errorf("Content-Type = %q, want %q", got, tc.wantType)
			}
			disp := w.Header().Get("Content-Disposition")
			isInline := strings.HasPrefix(disp, "inline")
			if isInline != tc.wantInline {
				t.Errorf("Content-Disposition = %q, want inline=%v", disp, tc.wantInline)
			}
		})
	}
}

// A hostile filename must not be able to inject a header or a path.
func TestDownloadSanitizesFilename(t *testing.T) {
	s := newDownloadServer(t, &fakeBlobs{byAccount: map[int64]map[string][]byte{
		7: {"b": []byte("x")},
	}})

	for _, name := range []string{
		"..%2f..%2fetc%2fpasswd",
		"evil%22.txt",
		"a%0d%0aX-Injected:%201.txt",
		"%e2%80%aetxt.exe",
	} {
		t.Run(name, func(t *testing.T) {
			w := doReq(s, http.MethodGet, "/jmap/download/a7/b/"+name, "", true, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			disp := w.Header().Get("Content-Disposition")
			for _, bad := range []string{"\r", "\n", "/", `\`} {
				if strings.Contains(disp, bad) {
					t.Errorf("Content-Disposition %q contains %q", disp, bad)
				}
			}
			if w.Header().Get("X-Injected") != "" {
				t.Error("a header was injected through the filename")
			}
			// The quoted-string must be well formed: an odd number of quotes
			// means the value escaped its own quoting.
			if strings.Count(disp, `"`)%2 != 0 {
				t.Errorf("Content-Disposition %q has unbalanced quotes", disp)
			}
		})
	}
}

func TestDownloadRequiresAuthentication(t *testing.T) {
	s := newDownloadServer(t, &fakeBlobs{byAccount: map[int64]map[string][]byte{
		7: {"b": []byte("x")},
	}})
	w := doReq(s, http.MethodGet, "/jmap/download/a7/b/x.txt", "", false, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated download = %d, want 401", w.Code)
	}
}
