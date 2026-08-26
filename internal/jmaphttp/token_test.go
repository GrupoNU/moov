package jmaphttp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// Tests for the scoped short-lived token mechanism (token.go).
//
// The claims under test are the security claims the design makes: a token is
// minted only by an authenticated request, grants exactly one scope at
// exactly the routes that accept it, dies at TTL, dies at revocation, dies
// with the account, and is worthless at /jmap/api.

// mintTestTokens mints via the real HTTP endpoint and returns scope → token.
func mintTestTokens(t *testing.T, s *Server, scopes ...string) map[string]string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"scopes": scopes})
	w := doReq(s, http.MethodPost, PathToken, string(body), true, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("mint status = %d (%s)", w.Code, w.Body)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("mint Cache-Control = %q, want no-store", cc)
	}
	var resp struct {
		Tokens map[string]struct {
			Token     string `json:"token"`
			ExpiresIn int64  `json:"expiresIn"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("mint response: %v", err)
	}
	out := make(map[string]string, len(resp.Tokens))
	for scope, tok := range resp.Tokens {
		if tok.Token == "" {
			t.Fatalf("scope %q minted an empty token", scope)
		}
		if want := int64(tokenTTL / time.Second); tok.ExpiresIn != want {
			t.Errorf("scope %q expiresIn = %d, want %d", scope, tok.ExpiresIn, want)
		}
		out[scope] = tok.Token
	}
	return out
}

// ---------------------------------------------------------------------------
// authority
// ---------------------------------------------------------------------------

func TestTokenMintVerifyRoundTrip(t *testing.T) {
	t.Parallel()
	a, err := newTokenAuthority()
	if err != nil {
		t.Fatal(err)
	}

	token, ttl, err := a.Mint(7, ScopePush)
	if err != nil {
		t.Fatal(err)
	}
	if ttl != tokenTTL {
		t.Errorf("ttl = %v, want %v", ttl, tokenTTL)
	}
	accountID, err := a.Verify(token, ScopePush)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if accountID != 7 {
		t.Errorf("accountID = %d, want 7", accountID)
	}
}

func TestTokenScopeRefusal(t *testing.T) {
	t.Parallel()
	a, _ := newTokenAuthority()

	push, _, _ := a.Mint(7, ScopePush)
	blob, _, _ := a.Mint(7, ScopeBlob)

	// A push token is NOT a blob token and vice versa — the scope check lives
	// in Verify, the one function every consumer calls.
	if _, err := a.Verify(push, ScopeBlob); err == nil {
		t.Error("a push token verified for the blob scope")
	}
	if _, err := a.Verify(blob, ScopePush); err == nil {
		t.Error("a blob token verified for the push scope")
	}
}

func TestTokenExpiry(t *testing.T) {
	t.Parallel()
	a, _ := newTokenAuthority()
	clock := newFakeClock()
	a.now = clock.Now

	token, _, _ := a.Mint(7, ScopePush)
	if _, err := a.Verify(token, ScopePush); err != nil {
		t.Fatalf("fresh token refused: %v", err)
	}

	clock.Advance(tokenTTL + time.Second)
	if _, err := a.Verify(token, ScopePush); err == nil {
		t.Error("an expired token verified")
	}
}

func TestTokenTamperingFailsClosed(t *testing.T) {
	t.Parallel()
	a, _ := newTokenAuthority()
	token, _, _ := a.Mint(7, ScopeBlob)

	parts := strings.Split(token, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])

	// Rewrite the account id inside the payload — the classic privilege
	// escalation — keeping the original signature.
	forged := strings.Replace(string(payload), "|7|", "|8|", 1)
	if forged == string(payload) {
		t.Fatal("test bug: payload did not contain the account field")
	}
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(forged)) + "." + parts[2]
	if _, err := a.Verify(tampered, ScopeBlob); err == nil {
		t.Error("a payload-tampered token verified")
	}

	// Rewrite the scope, keeping the signature.
	forgedScope := strings.Replace(string(payload), "blob|", "push|", 1)
	tampered = parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(forgedScope)) + "." + parts[2]
	if _, err := a.Verify(tampered, ScopePush); err == nil {
		t.Error("a scope-tampered token verified")
	}

	// Flip one signature byte.
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sig[0] ^= 0x01
	tampered = parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(sig)
	if _, err := a.Verify(tampered, ScopeBlob); err == nil {
		t.Error("a signature-tampered token verified")
	}

	// Garbage of every shape.
	for _, junk := range []string{"", "mt1", "mt1..", "mt0." + parts[1] + "." + parts[2], "not-a-token", token + "x"} {
		if _, err := a.Verify(junk, ScopeBlob); err == nil {
			t.Errorf("junk token %q verified", junk)
		}
	}
}

func TestTokenRevocation(t *testing.T) {
	t.Parallel()
	a, _ := newTokenAuthority()

	token, _, _ := a.Mint(7, ScopePush)
	other, _, _ := a.Mint(8, ScopePush)

	// Revoking with the WRONG account is a no-op: an authenticated user
	// cannot kill another account's tokens by pasting them.
	a.Revoke(token, 999)
	if _, err := a.Verify(token, ScopePush); err != nil {
		t.Fatal("a foreign revoke attempt killed the token")
	}

	a.Revoke(token, 7)
	if _, err := a.Verify(token, ScopePush); err == nil {
		t.Error("a revoked token verified")
	}
	if _, err := a.Verify(other, ScopePush); err != nil {
		t.Error("revoking one account's token killed another account's")
	}
}

func TestTokenRevokeAccount(t *testing.T) {
	t.Parallel()
	a, _ := newTokenAuthority()

	push, _, _ := a.Mint(7, ScopePush)
	blob, _, _ := a.Mint(7, ScopeBlob)
	other, _, _ := a.Mint(8, ScopeBlob)

	a.RevokeAccount(7)
	if _, err := a.Verify(push, ScopePush); err == nil {
		t.Error("push token survived RevokeAccount")
	}
	if _, err := a.Verify(blob, ScopeBlob); err == nil {
		t.Error("blob token survived RevokeAccount")
	}
	if _, err := a.Verify(other, ScopeBlob); err != nil {
		t.Error("RevokeAccount(7) killed account 8's token")
	}
}

func TestTokenDiesWithProcess(t *testing.T) {
	t.Parallel()
	// Two authorities model a restart: fresh random key, empty registry.
	a1, _ := newTokenAuthority()
	a2, _ := newTokenAuthority()

	token, _, _ := a1.Mint(7, ScopePush)
	if _, err := a2.Verify(token, ScopePush); err == nil {
		t.Error("a token minted before a 'restart' verified after it")
	}
}

func TestTokenPerAccountCapEvictsOldest(t *testing.T) {
	t.Parallel()
	a, _ := newTokenAuthority()
	clock := newFakeClock()
	a.now = clock.Now

	first, _, _ := a.Mint(7, ScopePush)
	for range maxTokensPerAccount {
		clock.Advance(time.Millisecond)
		if _, _, err := a.Mint(7, ScopePush); err != nil {
			t.Fatal(err)
		}
	}

	// The oldest was evicted to admit the newest; the account limited only
	// itself and minting never failed.
	if _, err := a.Verify(first, ScopePush); err == nil {
		t.Error("the oldest token survived past the per-account cap")
	}
	a.mu.Lock()
	n := len(a.byAccount[7])
	a.mu.Unlock()
	if n > maxTokensPerAccount {
		t.Errorf("account holds %d tokens, cap is %d", n, maxTokensPerAccount)
	}
}

// ---------------------------------------------------------------------------
// HTTP: minting and revoking
// ---------------------------------------------------------------------------

func TestTokenMintRequiresAuthentication(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newTestServer(t, nil)

	w := doReq(s, http.MethodPost, PathToken, `{"scopes":["push"]}`, false, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated mint = %d, want 401", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Error("no Basic challenge on the mint route")
	}
}

func TestTokenMintFailedLoginFeedsLockout(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newTestServer(t, nil)

	r := httptest.NewRequest(http.MethodPost, PathToken, strings.NewReader(`{"scopes":["push"]}`))
	r.Header.Set("Content-Type", "application/json")
	r.SetBasicAuth("user@example.com", "wrong-password")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password at mint = %d, want 401", w.Code)
	}

	// The failure entered the SAME lockout table every route feeds: the very
	// next attempt from this IP+account is locked out, wrong or right.
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, PathToken, strings.NewReader(`{"scopes":["push"]}`))
	r.Header.Set("Content-Type", "application/json")
	r.SetBasicAuth("user@example.com", testPassword)
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("post-failure mint = %d, want 429 (lockout applies to minting)", w.Code)
	}
}

func TestTokenMintValidation(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newTestServer(t, nil)

	for name, body := range map[string]string{
		"no scopes":     `{"scopes":[]}`,
		"unknown scope": `{"scopes":["admin"]}`,
		"not json":      `nope`,
		"too many":      `{"scopes":["push","blob","push","blob","push"]}`,
		"absent scopes": `{}`,
		"wrong shape":   `{"scopes":"push"}`,
	} {
		w := doReq(s, http.MethodPost, PathToken, body, true, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, w.Code)
		}
	}
}

func TestTokenRevokeEndpointKillsToken(t *testing.T) {
	t.Parallel()
	h := newSSEHarness(t, nil)
	s := serverOf(t, h)

	token := mintTestTokens(t, s, "push")["push"]

	// Works before revocation.
	w := doReq(s, http.MethodGet, PathEventSource+"?closeafter=state&access_token="+token, "", false, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("pre-revocation stream = %d, want 200 (%s)", w.Code, w.Body)
	}

	body, _ := json.Marshal(map[string]any{"tokens": []string{token}})
	w = doReq(s, http.MethodPost, PathTokenRevoke, string(body), true, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204 (%s)", w.Code, w.Body)
	}

	// Dead after revocation — this is the sign-out guarantee.
	w = doReq(s, http.MethodGet, PathEventSource+"?closeafter=state&access_token="+token, "", false, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("post-revocation stream = %d, want 403", w.Code)
	}
}

// serverOf digs the *Server back out of an SSE harness by rebuilding one with
// identical doubles. The harness exposes only the httptest server; for tests
// that mix recorder-driven requests (doReq) with token minting, a *Server is
// what is needed, so this helper builds the same shape newSSEHarness does.
func serverOf(t *testing.T, h *sseHarness) *Server {
	t.Helper()
	s, _, _, _ := newTestServer(t, func(c *Config) {
		c.Notifier = h.notifier
		c.State = h.states
	})
	return s
}

// ---------------------------------------------------------------------------
// HTTP: the routes that accept a token, and the ones that must not
// ---------------------------------------------------------------------------

// TestTokenAcceptingRoutesArePinned is the token twin of the public-routes
// pin: exactly two routes accept a token, each with its own scope. A route
// that grows a tokenScope without being added here fails the test.
func TestTokenAcceptingRoutesArePinned(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newTestServer(t, nil)

	want := map[string]TokenScope{
		PathDownload:    ScopeBlob,
		PathEventSource: ScopePush,
	}
	got := map[string]TokenScope{}
	for _, rt := range s.routes() {
		if rt.tokenScope != "" {
			got[rt.pattern] = rt.tokenScope
		}
		if rt.public && rt.tokenScope != "" {
			t.Errorf("route %s is both public and token-accepting, which is incoherent", rt.pattern)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("token-accepting routes = %v, want exactly %v", got, want)
	}
	for pattern, scope := range want {
		if got[pattern] != scope {
			t.Errorf("route %s scope = %q, want %q", pattern, got[pattern], scope)
		}
	}
}

// TestPushTokenRefusedAtAPI is the explicit pin the design demands: a token
// is NOT a general API credential. Presenting one at /jmap/api — in the query
// string or as a Bearer header — fails authentication outright.
func TestPushTokenRefusedAtAPI(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newTestServer(t, nil)
	tokens := mintTestTokens(t, s, "push", "blob")

	for scope, token := range tokens {
		// Query string: /jmap/api never reads access_token at all.
		w := doReq(s, http.MethodPost, PathAPI+"?access_token="+token,
			apiBody(`["Core/echo",{},"c0"]`), false, nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s token in query at /jmap/api = %d, want 401", scope, w.Code)
		}

		// Bearer header: no route anywhere accepts Bearer.
		w = doReq(s, http.MethodPost, PathAPI, apiBody(`["Core/echo",{},"c0"]`), false,
			map[string]string{"Authorization": "Bearer " + token})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s token as Bearer at /jmap/api = %d, want 401", scope, w.Code)
		}

		// The session endpoint is Basic-only too.
		w = doReq(s, http.MethodGet, PathWellKnown+"?access_token="+token, "", false, nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s token at session endpoint = %d, want 401", scope, w.Code)
		}

		// And upload, which shares the blob layer but not the scope decision.
		w = doReq(s, http.MethodPost, "/jmap/upload/a7?access_token="+token, "x", false,
			map[string]string{"Content-Type": "application/octet-stream"})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s token at upload = %d, want 401", scope, w.Code)
		}
	}
}

func TestBlobTokenServesDownload(t *testing.T) {
	t.Parallel()
	content := []byte("attachment bytes")
	s := newDownloadServer(t, &fakeBlobs{byAccount: map[int64]map[string][]byte{
		7: {"abc123": content},
	}})
	tokens := mintTestTokens(t, s, "blob", "push")

	// The blob token serves the download with no Authorization header.
	w := doReq(s, http.MethodGet, "/jmap/download/a7/abc123/file.bin?access_token="+tokens["blob"], "", false, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("tokened download = %d, want 200 (%s)", w.Code, w.Body)
	}
	if got := w.Body.String(); got != string(content) {
		t.Errorf("body = %q", got)
	}

	// The PUSH token does not: wrong scope, 403, and NO Basic challenge —
	// a challenge would pop the browser's native dialog on an <img>.
	w = doReq(s, http.MethodGet, "/jmap/download/a7/abc123/file.bin?access_token="+tokens["push"], "", false, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("push token at download = %d, want 403", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") != "" {
		t.Error("a token failure must not carry a Basic challenge")
	}

	// Garbage token: same refusal.
	w = doReq(s, http.MethodGet, "/jmap/download/a7/abc123/file.bin?access_token=mt1.junk.junk", "", false, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("garbage token at download = %d, want 403", w.Code)
	}

	// No token, no header: the ordinary Basic challenge, unchanged.
	w = doReq(s, http.MethodGet, "/jmap/download/a7/abc123/file.bin", "", false, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bare download = %d, want 401", w.Code)
	}

	// Basic STILL works on the tokened route — Bulwark's path is untouched.
	w = doReq(s, http.MethodGet, "/jmap/download/a7/abc123/file.bin", "", true, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Basic download = %d, want 200", w.Code)
	}

	// An Authorization header WINS over a query token: a bogus header with a
	// valid token is an authentication failure, not a silent fallback.
	w = doReq(s, http.MethodGet, "/jmap/download/a7/abc123/file.bin?access_token="+tokens["blob"], "", false,
		map[string]string{"Authorization": "Basic aW52YWxpZDppbnZhbGlk"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bogus Basic + valid token = %d, want 401 (header wins)", w.Code)
	}
}

func TestBlobTokenIsAccountScoped(t *testing.T) {
	t.Parallel()
	// The token belongs to account 7; the blob belongs to account 8. The
	// download must be the same 404 as a missing blob — the ownership rule
	// the Basic path enforces survives the token path unchanged.
	s := newDownloadServer(t, &fakeBlobs{byAccount: map[int64]map[string][]byte{
		8: {"abc123": []byte("someone else's mail")},
	}})
	token := mintTestTokens(t, s, "blob")["blob"]

	w := doReq(s, http.MethodGet, "/jmap/download/a8/abc123/file.bin?access_token="+token, "", false, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-account tokened download = %d, want 404", w.Code)
	}
}

func TestTokenForDisabledAccountStopsWorking(t *testing.T) {
	t.Parallel()
	content := []byte("bytes")
	blobs := &fakeBlobs{byAccount: map[int64]map[string][]byte{7: {"abc123": content}}}
	s, _, d, _ := newTestServer(t, func(c *Config) { c.Blobs = blobs })
	token := mintTestTokens(t, s, "blob")["blob"]

	w := doReq(s, http.MethodGet, "/jmap/download/a7/abc123/f.bin?access_token="+token, "", false, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("pre-disable download = %d, want 200", w.Code)
	}

	// Disable the account. The token itself is still cryptographically valid
	// and unexpired — but the store is consulted on every request, so it
	// stops working NOW, not at TTL.
	acct := testAccount()
	acct.State = store.AccountDisabled
	d.put(acct)

	w = doReq(s, http.MethodGet, "/jmap/download/a7/abc123/f.bin?access_token="+token, "", false, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("post-disable download = %d, want 403", w.Code)
	}
}

func TestInvalidateAccountTokens(t *testing.T) {
	t.Parallel()
	content := []byte("bytes")
	blobs := &fakeBlobs{byAccount: map[int64]map[string][]byte{7: {"abc123": content}}}
	s, _, _, _ := newTestServer(t, func(c *Config) { c.Blobs = blobs })
	token := mintTestTokens(t, s, "blob")["blob"]

	// The credential-rotation hook: invalidating the account kills its
	// outstanding tokens alongside its cached credentials.
	s.InvalidateAccountTokens(7)

	w := doReq(s, http.MethodGet, "/jmap/download/a7/abc123/f.bin?access_token="+token, "", false, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("post-invalidation download = %d, want 403", w.Code)
	}
}

// ---------------------------------------------------------------------------
// HTTP: the SSE path with a token (integration, real streaming server)
// ---------------------------------------------------------------------------

func TestEventSourceWithToken(t *testing.T) {
	t.Parallel()
	h := newSSEHarness(t, nil)

	// Mint over the SAME harness server the stream will hit, with Basic.
	body := bytes.NewReader([]byte(`{"scopes":["push"]}`))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, h.srv.URL+PathToken, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("user@example.com", testPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mint = %d", resp.StatusCode)
	}
	var minted struct {
		Tokens map[string]struct {
			Token string `json:"token"`
		} `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&minted); err != nil {
		t.Fatal(err)
	}
	token := minted.Tokens["push"].Token

	// Open the stream EXACTLY as a browser EventSource would: GET with the
	// token in the query and no Authorization header.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sreq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		h.srv.URL+PathEventSource+"?types=*&closeafter=no&ping=30&access_token="+token, nil)
	if err != nil {
		t.Fatal(err)
	}
	sresp, err := http.DefaultClient.Do(sreq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sresp.Body.Close() }()
	if sresp.StatusCode != http.StatusOK {
		t.Fatalf("tokened stream = %d", sresp.StatusCode)
	}
	if ct := sresp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}

	r := bufioReader(sresp.Body)

	// The immediate state event proves the stream authenticated as account 7
	// and reads its real states.
	ev := readEvent(t, r)
	if ev.name != "state" {
		t.Fatalf("first event = %q, want state", ev.name)
	}
	var change struct {
		Type    string                       `json:"@type"`
		Changed map[string]map[string]string `json:"changed"`
	}
	if err := json.Unmarshal([]byte(ev.data), &change); err != nil {
		t.Fatalf("StateChange payload: %v (%q)", err, ev.data)
	}
	if change.Type != "StateChange" {
		t.Errorf("@type = %q", change.Type)
	}
	if len(change.Changed) != 1 {
		t.Fatalf("changed names %d accounts, want 1", len(change.Changed))
	}

	// A live notification still flows on the tokened stream.
	h.notifier.waitForSubscriber(t, 7)
	h.states.set("mb-2", "em-2", "th-2")
	h.notifier.notify(7)
	ev = readEvent(t, r)
	if ev.name != "state" || !strings.Contains(ev.data, "em-2") {
		t.Fatalf("post-notification event = %+v", ev)
	}
}

func TestEventSourceExpiredTokenIs403(t *testing.T) {
	t.Parallel()
	h := newSSEHarness(t, nil)
	s := serverOf(t, h)
	clock := newFakeClock()
	s.tokens.now = clock.Now

	token, _, err := s.tokens.Mint(7, ScopePush)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(tokenTTL + time.Minute)

	w := doReq(s, http.MethodGet, PathEventSource+"?access_token="+token, "", false, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expired token stream = %d, want 403", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") != "" {
		t.Error("an expired token must not trigger the native credential dialog")
	}
}

// ---------------------------------------------------------------------------
// logging: the token must never reach a log line
// ---------------------------------------------------------------------------

// TestLogMiddlewareNeverLogsQueryString pins the property the query-string
// token design depends on: request logs carry the path, never the query. If
// someone "improves" the log line with r.URL.String(), this fails.
func TestLogMiddlewareNeverLogsQueryString(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	v := &fakeValidator{valid: map[string]string{"user@example.com": testPassword}}
	d := &fakeDirectory{}
	d.put(testAccount())
	auth, err := newTestAuth(v, d, newFakeClock(), func(c *AuthConfig) { c.Logger = logger })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Logger: logger}, auth)
	if err != nil {
		t.Fatal(err)
	}

	token := mintTestTokens(t, s, "push")["push"]
	secret := "SECRET-MARKER-" + token

	// Drive a request whose query carries the marker through the full
	// middleware stack (an unknown path still traverses logMiddleware).
	r := httptest.NewRequest(http.MethodGet, PathEventSource+"?access_token="+secret, nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	logged := buf.String()
	if logged == "" {
		t.Fatal("nothing was logged; the middleware is not wired")
	}
	if strings.Contains(logged, secret) || strings.Contains(logged, token) {
		t.Fatal("a token reached the request log")
	}
	if strings.Contains(logged, "access_token") {
		t.Fatal("the query string reached the request log")
	}
}

// bufioReader mirrors the harness's reader construction for a raw response.
func bufioReader(r io.Reader) *bufio.Reader { return bufio.NewReader(r) }
