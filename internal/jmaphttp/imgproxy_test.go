package jmaphttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The image proxy's tests are mostly REFUSAL tests, deliberately: the epic's
// bar (W-A4) is that each defense refuses independently, so each refusal is
// exercised on its own — signature, expiry, scheme, port, address ranges,
// redirect budget, size cap, content sniff — plus one honest happy path.

// pngPixel is a minimal valid PNG header, enough for sniffImageType.
func pngPixel() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 8)...)
}

// signedPath mints a signed proxy path for raw through the server's own
// signer — the same path production clients receive.
func signedPath(t *testing.T, s *Server, raw string) string {
	t.Helper()
	p := s.imgproxy.signedPathFor(raw)
	if p == "" {
		t.Fatalf("signedPathFor(%q) refused a URL the test needs signed", raw)
	}
	return p
}

// allowLoopback lets the test's own httptest servers (which listen on
// 127.0.0.1, an address production always refuses) play the public internet.
// Everything else still goes through the REAL vet, so a redirect into a
// private range is still refused.
func allowLoopback(s *Server) {
	s.imgproxy.allowNonDefaultPorts = true
	s.imgproxy.vet = func(addr netip.Addr) error {
		if addr.Unmap().IsLoopback() {
			return nil
		}
		return vetIP(addr)
	}
	// The vetting dialer lives inside the client's transport, which captured
	// the hook fields' OLD values via the method receiver — rebuild it so the
	// overrides apply.
	s.imgproxy.client = s.imgproxy.newClient()
}

// --- signing ----------------------------------------------------------------

func TestImageProxySignRequiresAuth(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	rec := doReq(s, http.MethodPost, PathImageProxySign, `{"urls":["https://example.com/a.png"]}`, false, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated sign = %d, want 401", rec.Code)
	}
}

func TestImageProxySignMintsVerifiablePaths(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	rec := doReq(s, http.MethodPost, PathImageProxySign,
		`{"urls":["https://example.com/a.png","https://example.com/b.gif?id=7"]}`, true, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sign = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		URLs map[string]string `json:"urls"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.URLs) != 2 {
		t.Fatalf("signed %d urls, want 2: %v", len(resp.URLs), resp.URLs)
	}
	for original, path := range resp.URLs {
		if !strings.HasPrefix(path, PathImageProxy+"?") {
			t.Errorf("signed path %q does not start with %q", path, PathImageProxy+"?")
		}
		u, err := url.Parse(path)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		decoded, err := base64.RawURLEncoding.DecodeString(q.Get("u"))
		if err != nil || string(decoded) != original {
			t.Errorf("u decodes to %q, want %q", decoded, original)
		}
		e, err := strconv.ParseInt(q.Get("e"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if !s.imgproxy.verify(q.Get("u"), e, q.Get("s")) {
			t.Errorf("signature on %q does not verify", path)
		}
	}
}

// TestImageProxySignRefusals: each row is one URL the signer must refuse.
// Refusal is silent omission from the response — the client's images simply
// stay blocked, which is the state they were already in.
func TestImageProxySignRefusals(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	cases := map[string]string{
		"non-http scheme":         "ftp://example.com/a.png",
		"javascript":              "javascript:alert(1)",
		"data URL":                "data:image/png;base64,AAAA",
		"relative":                "/etc/passwd",
		"schemeless":              "example.com/a.png",
		"userinfo trick":          "https://safe.example@10.0.0.1/a.png",
		"explicit non-http port":  "https://example.com:8443/a.png",
		"loopback literal":        "http://127.0.0.1/a.png",
		"loopback v6 literal":     "http://[::1]/a.png",
		"private literal":         "http://10.1.2.3/a.png",
		"link-local metadata":     "http://169.254.169.254/latest/meta-data/",
		"cgnat (tailscale) range": "http://100.100.1.1/a.png",
		"v4-mapped v6 loopback":   "http://[::ffff:127.0.0.1]/a.png",
		"oversized url":           "https://example.com/" + strings.Repeat("a", maxSignURLBytes),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(map[string][]string{"urls": {raw}})
			rec := doReq(s, http.MethodPost, PathImageProxySign, string(body), true, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("sign = %d, want 200", rec.Code)
			}
			var resp struct {
				URLs map[string]string `json:"urls"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if len(resp.URLs) != 0 {
				t.Errorf("refusable URL %q was signed: %v", raw, resp.URLs)
			}
		})
	}
}

func TestImageProxySignCapsTheList(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	urls := make([]string, maxSignURLs+1)
	for i := range urls {
		urls[i] = fmt.Sprintf("https://example.com/%d.png", i)
	}
	body, _ := json.Marshal(map[string][]string{"urls": urls})
	rec := doReq(s, http.MethodPost, PathImageProxySign, string(body), true, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized list = %d, want 400", rec.Code)
	}
}

// --- serving: the request-side refusals -------------------------------------

func TestImageProxyRefusesUnsignedRequests(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)

	u := base64.RawURLEncoding.EncodeToString([]byte("https://example.com/a.png"))
	e := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)

	cases := map[string]struct {
		query string
		want  int
	}{
		"no params":      {"", http.StatusBadRequest},
		"missing sig":    {"?u=" + u + "&e=" + e, http.StatusBadRequest},
		"garbage sig":    {"?u=" + u + "&e=" + e + "&s=AAAA", http.StatusForbidden},
		"malformed e":    {"?u=" + u + "&e=soon&s=AAAA", http.StatusBadRequest},
		"sig not base64": {"?u=" + u + "&e=" + e + "&s=%2e%2e", http.StatusForbidden},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := doReq(s, http.MethodGet, PathImageProxy+tc.query, "", false, nil)
			if rec.Code != tc.want {
				t.Errorf("GET %s = %d, want %d", tc.query, rec.Code, tc.want)
			}
		})
	}
}

func TestImageProxyRefusesTamperedURL(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	path := signedPath(t, s, "https://example.com/a.png")

	// Swap the signed u for a different target, keeping e and s: the HMAC
	// must not transfer.
	u, _ := url.Parse(path)
	q := u.Query()
	q.Set("u", base64.RawURLEncoding.EncodeToString([]byte("http://127.0.0.1/admin")))
	rec := doReq(s, http.MethodGet, PathImageProxy+"?"+q.Encode(), "", false, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tampered u = %d, want 403", rec.Code)
	}
}

func TestImageProxyRefusesExpiredSignature(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	// Mint in the past by moving the proxy's clock back, then serve at "now".
	s.imgproxy.now = func() time.Time { return time.Now().Add(-2 * imageProxyTTL) }
	path := signedPath(t, s, "https://example.com/a.png")
	s.imgproxy.now = time.Now

	rec := doReq(s, http.MethodGet, path, "", false, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expired signature = %d, want 403", rec.Code)
	}
}

// --- the address vet itself -------------------------------------------------

func TestVetIPRefusesEveryPrivateShape(t *testing.T) {
	refused := []string{
		"127.0.0.1", "127.8.8.8", "::1",
		"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "fe80::1",
		"100.64.0.1", "100.127.255.254", // CGNAT / Tailscale
		"0.0.0.0", "::",
		"224.0.0.1", "ff02::1",
		"255.255.255.255", "240.0.0.1",
		"fc00::1", "fd12:3456::1", // ULA
		"::ffff:10.0.0.1", "::ffff:127.0.0.1", // v4-mapped
		"192.0.2.10", "198.51.100.1", "203.0.113.9", // TEST-NETs
		"198.18.0.1", "192.0.0.8", "192.88.99.1",
		"64:ff9b::a00:1", "2001:db8::1", "2002:a00:1::1", "2001:0:1::1",
	}
	for _, raw := range refused {
		if err := vetIP(netip.MustParseAddr(raw)); err == nil {
			t.Errorf("vetIP(%s) = nil, want refusal", raw)
		}
	}
	allowed := []string{"93.184.216.34", "8.8.8.8", "2606:2800:220:1::1", "1.1.1.1"}
	for _, raw := range allowed {
		if err := vetIP(netip.MustParseAddr(raw)); err != nil {
			t.Errorf("vetIP(%s) = %v, want nil (public address)", raw, err)
		}
	}
}

// TestImageProxyRefusesPrivateResolution proves the SSRF check runs on what
// DNS actually ANSWERS, not on the name: a benign-looking hostname resolving
// to a private address is refused at dial time.
func TestImageProxyRefusesPrivateResolution(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	s.imgproxy.allowNonDefaultPorts = true
	s.imgproxy.lookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.66.0.1")}, nil
	}
	s.imgproxy.client = s.imgproxy.newClient()

	rec := doReq(s, http.MethodGet, signedPath(t, s, "https://cdn.innocent.example/a.png"), "", false, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("private resolution = %d, want 502 (a generic refusal, not an address oracle)", rec.Code)
	}
}

// TestImageProxyRefusesMixedResolution: ONE private answer poisons the whole
// answer set, because which record a retry dials is the attacker's choice.
func TestImageProxyRefusesMixedResolution(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	s.imgproxy.allowNonDefaultPorts = true
	s.imgproxy.lookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("93.184.216.34"),
			netip.MustParseAddr("192.168.0.10"),
		}, nil
	}
	s.imgproxy.client = s.imgproxy.newClient()

	rec := doReq(s, http.MethodGet, signedPath(t, s, "https://cdn.innocent.example/a.png"), "", false, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("mixed public+private resolution = %d, want 502", rec.Code)
	}
}

// --- serving through a real (loopback) upstream -----------------------------

func TestImageProxyServesARealImage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != "MoovMail-ImageProxy/1" {
			t.Errorf("upstream saw User-Agent %q, want the generic proxy identity", ua)
		}
		if c := r.Header.Get("Cookie"); c != "" {
			t.Errorf("upstream saw cookies: %q", c)
		}
		w.Header().Set("Content-Type", "application/octet-stream") // lies are ignored; bytes decide
		_, _ = w.Write(pngPixel())
	}))
	defer upstream.Close()

	s, _, _, _ := newTestServer(t, nil)
	allowLoopback(s)

	rec := doReq(s, http.MethodGet, signedPath(t, s, upstream.URL+"/a.png"), "", false, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxying a real PNG = %d, body %s", rec.Code, rec.Body.String())
	}
	h := rec.Header()
	if ct := h.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png (sniffed, not the upstream's lie)", ct)
	}
	if v := h.Get("X-Content-Type-Options"); v != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", v)
	}
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q, want default-src 'none'", csp)
	}
	if corp := h.Get("Cross-Origin-Resource-Policy"); corp != "cross-origin" {
		// same-origin would block our own sandboxed (opaque-origin) iframe.
		t.Errorf("CORP = %q, want cross-origin", corp)
	}
	if got := rec.Body.Bytes(); string(got) != string(pngPixel()) {
		t.Errorf("body was altered in transit")
	}
}

func TestImageProxyRefusesNonImageContent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png") // claim image, serve HTML
		_, _ = w.Write([]byte("<!doctype html><script>alert(1)</script>"))
	}))
	defer upstream.Close()

	s, _, _, _ := newTestServer(t, nil)
	allowLoopback(s)

	rec := doReq(s, http.MethodGet, signedPath(t, s, upstream.URL+"/fake.png"), "", false, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("HTML claiming to be an image = %d, want 502", rec.Code)
	}
}

func TestImageProxyRefusesSVG(t *testing.T) {
	// SVG is refused BY CONSTRUCTION (the sniffer only knows rasters); this
	// test pins the property because an SVG served from our origin would be a
	// script container if anything ever rendered it as a document.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	}))
	defer upstream.Close()

	s, _, _, _ := newTestServer(t, nil)
	allowLoopback(s)

	rec := doReq(s, http.MethodGet, signedPath(t, s, upstream.URL+"/a.svg"), "", false, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("SVG = %d, want 502", rec.Code)
	}
}

func TestImageProxyRefusesOversizedImage(t *testing.T) {
	big := append(pngPixel(), make([]byte, maxImageBytes)...) // cap + header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(big)
	}))
	defer upstream.Close()

	s, _, _, _ := newTestServer(t, nil)
	allowLoopback(s)

	rec := doReq(s, http.MethodGet, signedPath(t, s, upstream.URL+"/big.png"), "", false, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("oversized image = %d, want 502", rec.Code)
	}
}

func TestImageProxyRefusesUpstreamErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not here", http.StatusNotFound)
	}))
	defer upstream.Close()

	s, _, _, _ := newTestServer(t, nil)
	allowLoopback(s)

	rec := doReq(s, http.MethodGet, signedPath(t, s, upstream.URL+"/gone.png"), "", false, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("upstream 404 = %d, want 502", rec.Code)
	}
}

// TestImageProxyRevetsEveryRedirectHop: a public upstream that answers with a
// redirect to a host resolving PRIVATE must be refused at the hop, not
// followed. This is the DNS-rebinding/redirect half of the SSRF defense.
func TestImageProxyRevetsEveryRedirectHop(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://internal-service.example/secret.png", http.StatusFound)
	}))
	defer upstream.Close()

	s, _, _, _ := newTestServer(t, nil)
	allowLoopback(s)
	// The redirect target resolves to a private address; the loopback
	// override does NOT cover it, so the real vet refuses it.
	s.imgproxy.lookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.9.8.7")}, nil
	}
	s.imgproxy.client = s.imgproxy.newClient()

	rec := doReq(s, http.MethodGet, signedPath(t, s, upstream.URL+"/a.png"), "", false, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("redirect to private resolution = %d, want 502", rec.Code)
	}
}

func TestImageProxyBoundsRedirectChains(t *testing.T) {
	var upstream *httptest.Server
	hops := 0
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, upstream.URL+fmt.Sprintf("/hop%d", hops), http.StatusFound)
	}))
	defer upstream.Close()

	s, _, _, _ := newTestServer(t, nil)
	allowLoopback(s)

	rec := doReq(s, http.MethodGet, signedPath(t, s, upstream.URL+"/a.png"), "", false, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("endless redirect chain = %d, want 502", rec.Code)
	}
	if hops > maxImageRedirects+1 {
		t.Errorf("followed %d hops, budget is %d", hops, maxImageRedirects)
	}
}

func TestImageProxyRefusesRedirectToNonHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "file:///etc/passwd")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()

	s, _, _, _ := newTestServer(t, nil)
	allowLoopback(s)

	rec := doReq(s, http.MethodGet, signedPath(t, s, upstream.URL+"/a.png"), "", false, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("redirect to file: = %d, want 502", rec.Code)
	}
}
