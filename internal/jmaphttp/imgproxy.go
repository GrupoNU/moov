package jmaphttp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"
)

// The remote-image proxy (ADR-001 §5, W-A4 layer 3's supply line).
//
// # Why this endpoint exists
//
// A mail body's <img src="https://sender.example/pixel?id=..."> is a tracking
// beacon: loading it directly hands the sender the reader's IP address, rough
// location, client fingerprint and the exact moment the message was opened.
// The PWA therefore blocks remote images by default, and when the user opts
// in, the bytes come through HERE — the sender's server sees one request from
// Moov's infrastructure with a generic User-Agent, never the reader.
//
// # The two halves, and why signing is separate from fetching
//
//   - POST /jmap/imgproxy/sign (authenticated): the client submits the image
//     URLs its sanitizer collected and receives, for each acceptable one, a
//     relative proxy path carrying an expiry and an HMAC.
//   - GET /jmap/imgproxy?u=..&e=..&s=.. (no HTTP auth): serves the image. It
//     cannot require an Authorization header, because the requester is an
//     <img> inside a sandboxed, opaque-origin iframe — a context that can
//     attach neither headers nor cookies (the same constraint the download
//     route documented for <a download>). The HMAC IS the authorization: only
//     our server can mint one, minting requires an authenticated call, and
//     the signature covers the exact URL and expiry, so the endpoint cannot
//     be used as an open relay by anyone who has not first authenticated.
//
// The capability is bearer-style: whoever holds a signed path can fetch that
// one image until the expiry. That is the same trade Gmail's image proxy
// makes, bounded here by a short TTL and by the signature being unforgeable
// per-URL (an attacker cannot derive the signature for a DIFFERENT url from
// a leaked one).
//
// # The SSRF defenses (the reason this file is mostly refusals)
//
// A proxy that fetches attacker-chosen URLs from inside the VPS is standing
// next to Dovecot, Postgres, the Docker network and the Tailscale VPN. Every
// fetch therefore goes through vetIP, applied to the ADDRESS ACTUALLY DIALED:
// the check lives inside the transport's DialContext, after DNS resolution,
// so a hostname that resolves to a private address is refused, a DNS answer
// that CHANGES between check and use cannot bypass it (the vetted IP is the
// dialed IP — there is no second resolution to rebind), and every redirect
// hop is re-vetted because each new connection goes through the same dialer.
// The ranges refused include RFC 1918, loopback, link-local (and with it the
// 169.254.169.254 cloud metadata service), CGNAT 100.64/10 (which is where
// Tailscale lives — an attacker probing our tailnet through this proxy is
// exactly the scenario to kill), IPv6 ULA/link-local, NAT64, and the
// documentation/benchmark ranges. IPv4-mapped IPv6 is unmapped before
// checking so ::ffff:127.0.0.1 is loopback, not "some IPv6 address".
//
// Beyond addressing: only http/https on default ports (a fixed port surface
// means the proxy cannot be used to port-scan even public hosts), at most
// maxImageRedirects redirects, a hard byte cap read through a limited reader,
// a total-request timeout, and a response served ONLY if its magic bytes
// sniff as a raster image (sniffImageType — the same PNG/JPEG/WebP/GIF-only
// sniffer branding.go uses, which makes SVG — a script container — impossible
// to serve by construction, no matter what Content-Type the upstream claims).
//
// # Key lifetime
//
// The HMAC key is random per process, deliberately: no new secret to
// provision, rotate or leak. A restart invalidates outstanding signed URLs,
// which costs one extra round of "show images" clicks after a deploy —
// signatures are minted on demand by a client that is already authenticated,
// so nothing breaks, it just re-signs.
const (
	// PathImageProxy serves a signed remote image. GET; public in the route
	// table's sense (no HTTP auth is POSSIBLE for an <img> in a sandboxed
	// iframe) but authorized by HMAC — see the package comment above and the
	// public-routes pin in branding_test.go.
	PathImageProxy = "/jmap/imgproxy"

	// PathImageProxySign mints signed proxy paths. POST, authenticated.
	PathImageProxySign = "/jmap/imgproxy/sign"
)

const (
	// imageProxyTTL is how long a signed path stays valid. Long enough to
	// cover a reading session on one message; short enough that a leaked URL
	// is a stale capability by the time it travels anywhere.
	imageProxyTTL = 1 * time.Hour

	// maxImageBytes caps the upstream response. 10 MiB is far beyond any
	// legitimate mail image and small enough that the full-buffering the
	// sniffer needs stays bounded.
	maxImageBytes = 10 << 20

	// maxImageRedirects bounds a redirect chain. CDNs use one or two;
	// anything deeper is either broken or probing.
	maxImageRedirects = 5

	// maxSignURLs and maxSignURLBytes bound one signing request. 128 images
	// covers the worst newsletter; 2 KiB covers real-world tracking URLs.
	maxSignURLs     = 128
	maxSignURLBytes = 2048

	// maxConcurrentImageFetches bounds simultaneous upstream fetches across
	// the whole server. The GET side has no user identity to key a per-user
	// gate on, so the bound is global: it turns "make Moov hold open ten
	// thousand slow upstream sockets" into a 503 after the sixteenth.
	maxConcurrentImageFetches = 16

	// imageFetchTimeout bounds one whole upstream fetch, connect included.
	imageFetchTimeout = 20 * time.Second
)

// imageProxy holds the signing key and the vetted HTTP client.
//
// The unexported hook fields exist for the tests and ONLY the tests: the
// production constructor never touches them, so the zero behavior — real DNS,
// real vetting, default ports only — is what runs in production.
type imageProxy struct {
	key []byte
	now func() time.Time

	// lookupIP resolves a hostname. Tests inject fake answers to prove the
	// resolver-facing refusals without real DNS.
	lookupIP func(ctx context.Context, host string) ([]netip.Addr, error)

	// vet decides whether a resolved address may be dialed. Tests override it
	// to let a loopback httptest server play "the public internet".
	vet func(addr netip.Addr) error

	// allowNonDefaultPorts is set only by tests (httptest listens on a random
	// port). In production only 80/443 are dialable.
	allowNonDefaultPorts bool

	client   *http.Client
	fetchSem chan struct{}
}

// newImageProxy builds the proxy with a fresh random key.
func newImageProxy() (*imageProxy, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("imgproxy: generating the signing key: %w", err)
	}
	p := &imageProxy{
		key:      key,
		now:      time.Now,
		fetchSem: make(chan struct{}, maxConcurrentImageFetches),
	}
	p.lookupIP = p.defaultLookupIP
	p.vet = vetIP
	p.client = p.newClient()
	return p, nil
}

func (p *imageProxy) defaultLookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	return addrs, nil
}

// newClient builds the outbound HTTP client whose EVERY connection passes
// through the vetting dialer. Building it once (rather than per request)
// keeps connection reuse, but the dial check runs for each new connection —
// including each redirect hop that lands on a new host.
func (p *imageProxy) newClient() *http.Client {
	transport := &http.Transport{
		DialContext:           p.dialVetted,
		MaxIdleConns:          maxConcurrentImageFetches,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		// No HTTP/2 push, no protocol upgrades: a plain image fetch.
		ForceAttemptHTTP2: false,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   imageFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxImageRedirects {
				return errors.New("too many redirects")
			}
			// The dialer re-vets whatever host this resolves to; the scheme
			// check here closes the residual gap (a redirect to file: or
			// ftp: would otherwise be attempted by a permissive client).
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-http scheme %q", req.URL.Scheme)
			}
			if err := p.checkPort(req.URL); err != nil {
				return err
			}
			return nil
		},
	}
}

// dialVetted resolves addr's host, refuses every non-public answer, and dials
// the vetted IP it just checked — the resolve-check-dial sequence is one
// operation, which is what closes the DNS-rebinding TOCTOU.
func (p *imageProxy) dialVetted(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("imgproxy: malformed dial address %q: %w", addr, err)
	}
	if !p.allowNonDefaultPorts && port != "80" && port != "443" {
		return nil, fmt.Errorf("imgproxy: port %s refused", port)
	}

	var candidates []netip.Addr
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		candidates = []netip.Addr{ip}
	} else {
		resolved, lookupErr := p.lookupIP(ctx, host)
		if lookupErr != nil {
			return nil, fmt.Errorf("imgproxy: resolving %q: %w", host, lookupErr)
		}
		candidates = resolved
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("imgproxy: %q resolved to no addresses", host)
	}

	// ALL answers must be public, not just the one dialed: a hostname mixing
	// a public and a private A record is under the attacker's control, and
	// which answer a retry would dial is not.
	for _, ip := range candidates {
		if vetErr := p.vet(ip); vetErr != nil {
			return nil, fmt.Errorf("imgproxy: refusing %q: %w", host, vetErr)
		}
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var lastErr error
	for _, ip := range candidates {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

func (p *imageProxy) checkPort(u *url.URL) error {
	if p.allowNonDefaultPorts {
		return nil
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		return fmt.Errorf("imgproxy: explicit port %s refused", port)
	}
	return nil
}

// forbiddenPrefixes are the address ranges vetIP refuses beyond what netip's
// own predicates cover. Each entry names its reason, because a bare CIDR list
// is unreviewable.
var forbiddenPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT — and the Tailscale VPN this VPS is on.
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments.
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1.
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2.
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3.
	netip.MustParsePrefix("198.18.0.0/15"),   // Benchmarking.
	netip.MustParsePrefix("192.88.99.0/24"),  // Deprecated 6to4 relay anycast.
	netip.MustParsePrefix("240.0.0.0/4"),     // Reserved, incl. 255.255.255.255 broadcast.
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64 — embeds an IPv4 address.
	netip.MustParsePrefix("64:ff9b:1::/48"),  // Local-use NAT64.
	netip.MustParsePrefix("2001:db8::/32"),   // IPv6 documentation.
	netip.MustParsePrefix("2002::/16"),       // 6to4 — embeds an IPv4 address.
	netip.MustParsePrefix("2001::/32"),       // Teredo — embeds an IPv4 address.
}

// vetIP refuses every address that is not plausibly "a public image host".
// It fails CLOSED: anything weird is refused, and the caller treats refusal
// as a generic upstream failure so the response is not an address oracle.
func vetIP(addr netip.Addr) error {
	// ::ffff:10.0.0.1 must be judged as 10.0.0.1, not as an exotic IPv6.
	ip := addr.Unmap()
	if !ip.IsValid() {
		return errors.New("invalid address")
	}
	switch {
	case ip.IsLoopback():
		return errors.New("loopback address")
	case ip.IsPrivate():
		return errors.New("private address")
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return errors.New("link-local address")
	case ip.IsUnspecified():
		return errors.New("unspecified address")
	case ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return errors.New("multicast address")
	}
	for _, prefix := range forbiddenPrefixes {
		if prefix.Contains(ip) {
			return fmt.Errorf("address in refused range %s", prefix)
		}
	}
	return nil
}

// ---- signing ----

// signCanonical is the byte string the HMAC covers. Versioned so a future
// format change invalidates old signatures instead of aliasing them, and
// NUL-separated so no crafted u can collide with an (u, e) boundary.
func signCanonical(u string, e int64) []byte {
	return []byte("moov-imgproxy-v1\x00" + u + "\x00" + strconv.FormatInt(e, 10))
}

func (p *imageProxy) sign(u string, e int64) string {
	mac := hmac.New(sha256.New, p.key)
	mac.Write(signCanonical(u, e))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (p *imageProxy) verify(u string, e int64, s string) bool {
	got, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, p.key)
	mac.Write(signCanonical(u, e))
	want := mac.Sum(nil)
	// hmac.Equal is constant-time; the length check inside it is not a leak
	// (the attacker knows the MAC length).
	return subtle.ConstantTimeCompare(got, want) == 1
}

// signedPathFor returns the relative proxy path for one acceptable URL, or
// "" when the URL is refused. Refusals here are the CHEAP ones — scheme,
// port, size, literal-IP ranges; hostname resolution is deferred to fetch
// time, where the dialer applies the authoritative check.
func (p *imageProxy) signedPathFor(raw string) string {
	if len(raw) > maxSignURLBytes {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	if u.Host == "" || u.User != nil {
		// A userinfo component (https://user:pass@host/) in an image URL is
		// only ever a trick — against parsers, or against this check.
		return ""
	}
	if err := p.checkPort(u); err != nil {
		return ""
	}
	// A literal IP in a refused range fails fast at signing; hostnames are
	// vetted post-resolution by the dialer.
	if ip, ipErr := netip.ParseAddr(u.Hostname()); ipErr == nil {
		if p.vet(ip) != nil {
			return ""
		}
	}

	encoded := base64.RawURLEncoding.EncodeToString([]byte(raw))
	expiry := p.now().Add(imageProxyTTL).Unix()
	q := url.Values{}
	q.Set("u", encoded)
	q.Set("e", strconv.FormatInt(expiry, 10))
	q.Set("s", p.sign(encoded, expiry))
	return PathImageProxy + "?" + q.Encode()
}

// ---- HTTP handlers ----

// signRequest and signResponse are the wire shapes of POST /jmap/imgproxy/sign.
type signRequest struct {
	URLs []string `json:"urls"`
}

type signResponse struct {
	// URLs maps each accepted original URL to its relative signed proxy
	// path. Refused URLs are absent — the client leaves those images
	// blocked, which is the safe default it started from.
	URLs map[string]string `json:"urls"`
}

// handleImageProxySign mints signed proxy paths. Authenticated by the route
// table (it is NOT public); the identity is not otherwise used — a signed
// image URL is not an account-scoped resource, exactly as one message's
// remote image is not.
func (s *Server) handleImageProxySign(w http.ResponseWriter, r *http.Request) {
	if s.imgproxy == nil {
		writeGenericProblem(w, http.StatusNotImplemented, "the image proxy is not enabled")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxSignURLs)*maxSignURLBytes+4096))
	if err != nil {
		writeGenericProblem(w, http.StatusBadRequest, "unreadable request body")
		return
	}
	var req signRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeGenericProblem(w, http.StatusBadRequest, "the request body is not valid JSON")
		return
	}
	if len(req.URLs) > maxSignURLs {
		writeGenericProblem(w, http.StatusBadRequest,
			fmt.Sprintf("at most %d urls per request", maxSignURLs))
		return
	}
	resp := signResponse{URLs: make(map[string]string, len(req.URLs))}
	for _, raw := range req.URLs {
		if path := s.imgproxy.signedPathFor(raw); path != "" {
			resp.URLs[raw] = path
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleImageProxy serves one signed image.
//
// Every refusal that involves the upstream — vetting, connect, status, size,
// content — is the SAME 502, deliberately: distinct statuses would make this
// endpoint an oracle for probing the network around the server ("403 means
// the address was private, so it exists").
func (s *Server) handleImageProxy(w http.ResponseWriter, r *http.Request) {
	if s.imgproxy == nil {
		writeGenericProblem(w, http.StatusNotImplemented, "the image proxy is not enabled")
		return
	}
	p := s.imgproxy

	q := r.URL.Query()
	encoded := q.Get("u")
	expiryRaw := q.Get("e")
	sig := q.Get("s")
	if encoded == "" || expiryRaw == "" || sig == "" {
		writeGenericProblem(w, http.StatusBadRequest, "missing u, e or s")
		return
	}
	expiry, err := strconv.ParseInt(expiryRaw, 10, 64)
	if err != nil {
		writeGenericProblem(w, http.StatusBadRequest, "malformed expiry")
		return
	}

	// Signature first, then expiry: an attacker without the key learns
	// nothing about WHY a forged URL failed, and a tampered expiry fails the
	// signature rather than the clock.
	if !p.verify(encoded, expiry, sig) {
		writeGenericProblem(w, http.StatusForbidden, "invalid signature")
		return
	}
	if p.now().Unix() > expiry {
		writeGenericProblem(w, http.StatusForbidden, "signature expired")
		return
	}

	rawURL, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		// Unreachable for genuinely signed paths; a decode failure here means
		// the signature covered undecodable bytes, which only we could have
		// produced. Fail closed anyway.
		writeGenericProblem(w, http.StatusBadRequest, "malformed url")
		return
	}
	target, err := url.Parse(string(rawURL))
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.User != nil {
		writeGenericProblem(w, http.StatusBadRequest, "malformed url")
		return
	}
	if err := p.checkPort(target); err != nil {
		writeGenericProblem(w, http.StatusBadRequest, "refused port")
		return
	}

	select {
	case p.fetchSem <- struct{}{}:
		defer func() { <-p.fetchSem }()
	default:
		w.Header().Set("Retry-After", "2")
		writeGenericProblem(w, http.StatusServiceUnavailable, "image proxy is busy")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), imageFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		writeGenericProblem(w, http.StatusBadRequest, "malformed url")
		return
	}
	// A generic client identity; nothing about the reader crosses this hop.
	// (Fastmail's proxy documents the same posture.)
	req.Header.Set("User-Agent", "MoovMail-ImageProxy/1")
	req.Header.Set("Accept", "image/*")

	resp, err := p.client.Do(req)
	if err != nil {
		s.log.Debug("imgproxy: upstream fetch failed", "error", err)
		writeGenericProblem(w, http.StatusBadGateway, "the image could not be fetched")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		writeGenericProblem(w, http.StatusBadGateway, "the image could not be fetched")
		return
	}
	if resp.ContentLength > maxImageBytes {
		writeGenericProblem(w, http.StatusBadGateway, "the image could not be fetched")
		return
	}

	// Buffer the (bounded) body: the sniffer needs the head, Content-Length
	// needs the total, and a cap enforced mid-stream would otherwise truncate
	// an image after headers already promised success.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		writeGenericProblem(w, http.StatusBadGateway, "the image could not be fetched")
		return
	}
	if len(data) > maxImageBytes {
		writeGenericProblem(w, http.StatusBadGateway, "the image could not be fetched")
		return
	}

	// Content identity comes from the BYTES, never from the upstream's
	// Content-Type header: sniffImageType recognizes PNG/JPEG/WebP/GIF and
	// nothing else, so text/html, SVG, and "image/png that is actually HTML"
	// are all the same refusal.
	contentType, ok := sniffImageType(data)
	if !ok {
		writeGenericProblem(w, http.StatusBadGateway, "the response is not an image")
		return
	}

	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	// Belt and braces for the day a sniffer bug serves something else: never
	// re-sniffed, never framed, no capabilities if opened as a document.
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// CORP must be cross-origin: the legitimate consumer is an <img> inside
	// a SANDBOXED iframe, whose origin is opaque — `same-origin` here would
	// block our own reader. Access control is the HMAC, not CORP.
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	// Cacheable by the browser for the signature's order of lifetime; private
	// because the signed URL itself is a capability.
	h.Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
