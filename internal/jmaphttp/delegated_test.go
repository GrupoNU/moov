package jmaphttp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// Tests for delegated sign-in (epic M2), one per acceptance criterion of
// contract §6 M2 plus the properties the design comment in delegated.go
// claims. Real Ed25519 and RSA keys, a real (TLS, httptest) JWKS server, a
// fake session store, a fake clock.

const (
	delegatedHostA = "mail.example.test"
	delegatedHostB = "other.example.test"
	issuerA        = "https://id.example.test"
	issuerB        = "https://sso.example.test"
	kidEd          = "ed-2026-09"
	kidRSA         = "rsa-2026-09"
	kidWeakRSA     = "rsa-weak"
)

// --- keys ------------------------------------------------------------------

type delegatedTestKeys struct {
	edPub    ed25519.PublicKey
	edPriv   ed25519.PrivateKey
	edOther  ed25519.PrivateKey
	rsaPriv  *rsa.PrivateKey
	rsaWeak  *rsa.PrivateKey
	rsaOther *rsa.PrivateKey
}

var (
	delegatedKeysOnce sync.Once
	delegatedKeys     delegatedTestKeys
)

// testKeys generates the key material once per test binary: RSA generation
// is the slow part and every test wants the same keys anyway.
func testKeys(t *testing.T) delegatedTestKeys {
	t.Helper()
	delegatedKeysOnce.Do(func() {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			panic(err)
		}
		_, other, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			panic(err)
		}
		rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		weak, err := rsa.GenerateKey(rand.Reader, 1024)
		if err != nil {
			panic(err)
		}
		rsaOther, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		delegatedKeys = delegatedTestKeys{edPub: pub, edPriv: priv, edOther: other,
			rsaPriv: rsaKey, rsaWeak: weak, rsaOther: rsaOther}
	})
	return delegatedKeys
}

// --- the fake JWKS endpoint --------------------------------------------------

type fakeJWKS struct {
	srv     *httptest.Server
	fetches atomic.Int64
	mu      sync.Mutex
	fail    bool
	body    []byte
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func jwksDocument(keys delegatedTestKeys) []byte {
	doc := map[string]any{"keys": []map[string]any{
		{"kty": "OKP", "crv": "Ed25519", "kid": kidEd, "use": "sig", "alg": "EdDSA", "x": b64(keys.edPub)},
		{"kty": "RSA", "kid": kidRSA, "use": "sig", "alg": "RS256",
			"n": b64(keys.rsaPriv.N.Bytes()), "e": b64(big.NewInt(int64(keys.rsaPriv.E)).Bytes())},
		// A 1024-bit key: §3.2 says ≥ 2048, so the parser must drop it and a
		// token naming its kid must die as "unknown kid".
		{"kty": "RSA", "kid": kidWeakRSA, "use": "sig", "alg": "RS256",
			"n": b64(keys.rsaWeak.N.Bytes()), "e": b64(big.NewInt(int64(keys.rsaWeak.E)).Bytes())},
		// An encryption key and a symmetric key: neither is a signing key.
		{"kty": "RSA", "kid": "enc", "use": "enc", "n": b64(keys.rsaPriv.N.Bytes()), "e": "AQAB"},
		{"kty": "oct", "kid": "hmac", "use": "sig", "alg": "HS256", "k": b64([]byte("secret"))},
	}}
	body, _ := json.Marshal(doc)
	return body
}

func newFakeJWKS(t *testing.T, keys delegatedTestKeys) *fakeJWKS {
	t.Helper()
	f := &fakeJWKS{body: jwksDocument(keys)}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.fetches.Add(1)
		f.mu.Lock()
		fail, body := f.fail, f.body
		f.mu.Unlock()
		if fail {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeJWKS) setFail(v bool) {
	f.mu.Lock()
	f.fail = v
	f.mu.Unlock()
}

func (f *fakeJWKS) url() string { return f.srv.URL + "/.well-known/jwks.json" }

// --- the fake session store ------------------------------------------------

type fakeDelegatedStore struct {
	mu       sync.Mutex
	nextID   int64
	sessions map[string]*store.DelegatedSession // by hex-ish string of hash
	jtis     map[string]time.Time
	touches  int
	err      error
}

func newFakeDelegatedStore() *fakeDelegatedStore {
	return &fakeDelegatedStore{sessions: map[string]*store.DelegatedSession{}, jtis: map[string]time.Time{}}
}

func (f *fakeDelegatedStore) CreateDelegatedSession(_ context.Context, s store.DelegatedSession) (store.DelegatedSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return store.DelegatedSession{}, f.err
	}
	f.nextID++
	s.ID = f.nextID
	cp := s
	f.sessions[string(s.TokenHash)] = &cp
	return s, nil
}

func (f *fakeDelegatedStore) GetDelegatedSession(_ context.Context, hash []byte) (store.DelegatedSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return store.DelegatedSession{}, f.err
	}
	s, ok := f.sessions[string(hash)]
	if !ok {
		return store.DelegatedSession{}, fmt.Errorf("delegated session: %w", store.ErrNotFound)
	}
	return *s, nil
}

func (f *fakeDelegatedStore) byID(id int64) *store.DelegatedSession {
	for _, s := range f.sessions {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func (f *fakeDelegatedStore) TouchDelegatedSession(_ context.Context, id int64, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touches++
	if s := f.byID(id); s != nil {
		t := now
		s.LastSeenAt = &t
	}
	return nil
}

func (f *fakeDelegatedStore) SetDelegatedSessionExpiry(_ context.Context, id int64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.byID(id)
	if s == nil || s.RevokedAt != nil {
		return store.ErrNotFound
	}
	s.ExpiresAt = at
	return nil
}

func (f *fakeDelegatedStore) RevokeDelegatedSession(_ context.Context, id int64, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.byID(id); s != nil && s.RevokedAt == nil {
		t := now
		s.RevokedAt = &t
	}
	return nil
}

func (f *fakeDelegatedStore) RevokeDelegatedSessionsByIssuer(_ context.Context, accountID int64, issuer string, now time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, s := range f.sessions {
		if s.AccountID == accountID && s.Issuer == issuer && s.RevokedAt == nil && s.ExpiresAt.After(now) {
			t := now
			s.RevokedAt = &t
			n++
		}
	}
	return n, nil
}

func (f *fakeDelegatedStore) RevokeDelegatedSessionsForAccount(_ context.Context, accountID int64, now time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, s := range f.sessions {
		if s.AccountID == accountID && s.RevokedAt == nil && s.ExpiresAt.After(now) {
			t := now
			s.RevokedAt = &t
			n++
		}
	}
	return n, nil
}

func (f *fakeDelegatedStore) ConsumeDelegatedJTI(_ context.Context, issuer, jti string, expiresAt, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, exp := range f.jtis {
		if exp.Before(now) {
			delete(f.jtis, k)
		}
	}
	key := issuer + "\x00" + jti
	if _, seen := f.jtis[key]; seen {
		return false, nil
	}
	f.jtis[key] = expiresAt
	return true, nil
}

// --- signing ---------------------------------------------------------------

// tokenSpec describes one token to sign. Zero values mean "the good token".
type tokenSpec struct {
	alg     string
	kid     string
	header  map[string]any // extra/override header members
	claims  map[string]any // extra/override claims; a nil value deletes
	signKey any            // ed25519.PrivateKey, *rsa.PrivateKey, []byte (HMAC), or nil for the default of alg
	parts   int            // 0 → 3
	padding bool           // emit base64 WITH padding (invalid for JWS)
}

// signToken builds a compact JWS for host/purpose with the fake clock's now.
func signToken(t *testing.T, keys delegatedTestKeys, now time.Time, spec tokenSpec) string {
	t.Helper()
	alg := spec.alg
	if alg == "" {
		alg = "EdDSA"
	}
	kid := spec.kid
	if kid == "" {
		kid = map[string]string{"EdDSA": kidEd, "RS256": kidRSA}[alg]
	}
	header := map[string]any{"alg": alg, "typ": "JWT"}
	if kid != "-" {
		header["kid"] = kid
	}
	for k, v := range spec.header {
		if v == nil {
			delete(header, k)
		} else {
			header[k] = v
		}
	}
	claims := map[string]any{
		"iss":     issuerA,
		"aud":     delegatedHostA,
		"sub":     testAccount().Email,
		"iat":     now.Unix(),
		"exp":     now.Add(5 * time.Minute).Unix(),
		"jti":     fmt.Sprintf("jti-%d", time.Now().UnixNano()),
		"purpose": purposeLogin,
	}
	for k, v := range spec.claims {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	hb, _ := json.Marshal(header)
	pb, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	if spec.padding {
		enc = base64.URLEncoding
	}
	signed := enc.EncodeToString(hb) + "." + enc.EncodeToString(pb)

	var sig []byte
	key := spec.signKey
	switch {
	case alg == "none":
		sig = nil
	case alg == "HS256":
		mac := hmac.New(sha256.New, []byte("secret"))
		mac.Write([]byte(signed))
		sig = mac.Sum(nil)
	case alg == "RS256" || func() bool { _, ok := key.(*rsa.PrivateKey); return ok }():
		priv, _ := key.(*rsa.PrivateKey)
		if priv == nil {
			priv = keys.rsaPriv
		}
		digest := sha256.Sum256([]byte(signed))
		var err error
		sig, err = rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
	default:
		priv, _ := key.(ed25519.PrivateKey)
		if priv == nil {
			priv = keys.edPriv
		}
		sig = ed25519.Sign(priv, []byte(signed))
	}
	token := signed + "." + enc.EncodeToString(sig)
	if spec.parts == 2 {
		token = signed
	}
	return token
}

// --- the server under test ---------------------------------------------------

type delegatedFixture struct {
	s      *Server
	v      *fakeValidator
	d      *fakeDirectory
	clock  *fakeClock
	store  *fakeDelegatedStore
	jwks   *fakeJWKS
	keys   delegatedTestKeys
	status *fakeAccountStatus
	obs    *fakeDelegatedObserver
	logs   *bytes.Buffer

	// ip, when set, is the client address every request comes from; empty
	// means the shared default. freshIP hands out a new one per call.
	ipMu sync.Mutex
	ip   string
	ipN  int
}

// remoteAddr is the RemoteAddr of the next request.
func (fx *delegatedFixture) remoteAddr() string {
	fx.ipMu.Lock()
	defer fx.ipMu.Unlock()
	if fx.ip != "" {
		return fx.ip
	}
	return "203.0.113.10:4444"
}

// freshIP moves the fixture to a client address no request has used, so the
// per-IP exchange budget starts full.
func (fx *delegatedFixture) freshIP() {
	fx.ipMu.Lock()
	defer fx.ipMu.Unlock()
	fx.ipN++
	fx.ip = fmt.Sprintf("198.18.%d.%d:5555", fx.ipN/256, fx.ipN%256)
}

type fakeAccountStatus struct {
	mu        sync.Mutex
	name      string
	readOnly  bool
	suspended map[int64]bool
	err       error
}

func (f *fakeAccountStatus) AccountStatus(_ context.Context, id int64) (AccountStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return AccountStatus{}, f.err
	}
	return AccountStatus{Name: f.name, ReadOnly: f.readOnly, Suspended: f.suspended[id]}, nil
}

type fakeDelegatedObserver struct {
	mu     sync.Mutex
	counts map[string]int
}

func (o *fakeDelegatedObserver) DelegatedExchange(result string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.counts == nil {
		o.counts = map[string]int{}
	}
	o.counts[result]++
}

func (o *fakeDelegatedObserver) count(result string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.counts[result]
}

func newDelegatedFixture(t *testing.T, mutate func(*DelegatedConfig)) *delegatedFixture {
	t.Helper()
	keys := testKeys(t)
	jwks := newFakeJWKS(t, keys)
	fx := &delegatedFixture{
		v:      &fakeValidator{valid: map[string]string{"user@example.com": testPassword}},
		d:      &fakeDirectory{},
		clock:  newFakeClock(),
		store:  newFakeDelegatedStore(),
		jwks:   jwks,
		keys:   keys,
		status: &fakeAccountStatus{suspended: map[int64]bool{}},
		obs:    &fakeDelegatedObserver{},
		logs:   &bytes.Buffer{},
	}
	fx.d.put(testAccount())
	logger := slog.New(slog.NewTextHandler(fx.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	auth, err := newTestAuth(fx.v, fx.d, fx.clock, func(c *AuthConfig) { c.Logger = logger })
	if err != nil {
		t.Fatal(err)
	}
	dcfg := &DelegatedConfig{
		Issuers: []DelegatedIssuer{
			{Host: delegatedHostA, Issuer: issuerA, JWKSURL: jwks.url()},
			{Host: delegatedHostA, Issuer: issuerB, JWKSURL: jwks.url()},
			{Host: delegatedHostB, Issuer: issuerA, JWKSURL: jwks.url()},
		},
		Sessions:   fx.store,
		Accounts:   fx.status,
		Observer:   fx.obs,
		HTTPClient: jwks.srv.Client(),
		now:        fx.clock.Now,
	}
	if mutate != nil {
		mutate(dcfg)
	}
	s, err := New(Config{Logger: logger, Delegated: dcfg}, auth)
	if err != nil {
		t.Fatal(err)
	}
	fx.s = s
	return fx
}

// do runs a request through the full handler with Host set. fx.ip selects
// the client address, because the exchange budget (§3.4, 30/min) is PER IP:
// a test that walks a long matrix of tokens is not testing the rate limiter
// and must not trip it, so it moves to a fresh IP for each case.
func (fx *delegatedFixture) do(method, path, body string, header map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Host = delegatedHostA
	r.RemoteAddr = fx.remoteAddr()
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	fx.s.Handler().ServeHTTP(w, r)
	return w
}

func exchangeBody(token string) string {
	b, _ := json.Marshal(map[string]string{"token": token})
	return string(b)
}

// exchange posts the token and returns the recorder.
func (fx *delegatedFixture) exchange(token string) *httptest.ResponseRecorder {
	return fx.do(http.MethodPost, PathDelegatedExchange, exchangeBody(token), nil)
}

// session exchanges a good token and returns the parsed Session.
func (fx *delegatedFixture) session(t *testing.T, spec tokenSpec) sessionResponse {
	t.Helper()
	w := fx.exchange(signToken(t, fx.keys, fx.clock.Now(), spec))
	if w.Code != http.StatusOK {
		t.Fatalf("exchange: status %d body %s", w.Code, w.Body.String())
	}
	var resp sessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

const invalidTokenBody = `{"detail":"invalid delegated token","status":401,"type":"about:blank"}`
const notFoundBody = `{"detail":"not found","status":404,"type":"about:blank"}`

// ---------------------------------------------------------------------------
// The feature does not exist unless configured
// ---------------------------------------------------------------------------

func TestDelegatedRoutesAre404WithoutIssuers(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newTestServer(t, nil)
	// The reference: what every feature-off route in this server answers.
	ref := doReq(s, http.MethodGet, PathBrandingAdmin, "", true, nil)
	if ref.Code != http.StatusNotFound || ref.Body.String() != notFoundBody {
		t.Fatalf("reference 404 = %d %s", ref.Code, ref.Body.String())
	}
	for _, p := range []string{PathDelegatedExchange, PathDelegatedRenew, PathDelegatedLogout, PathDelegatedRevoke} {
		w := doReq(s, http.MethodPost, p, `{"token":"x"}`, false, bearer("mds1_"+strings.Repeat("A", 43)))
		if w.Code != http.StatusNotFound || w.Body.String() != notFoundBody {
			t.Errorf("%s unconfigured: %d %s, want the generic 404", p, w.Code, w.Body.String())
		}
		if w.Header().Get("Content-Type") != ref.Header().Get("Content-Type") {
			t.Errorf("%s: content type %q differs from the reference 404", p, w.Header().Get("Content-Type"))
		}
	}
	// And the Bearer scheme is refused everywhere, with no LOGIN attempted.
	w := doReq(s, http.MethodGet, PathWellKnown, "", false, bearer("mds1_"+strings.Repeat("A", 43)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bearer without feature: %d", w.Code)
	}
}

func TestDelegatedRoutesAre404ForAnUnconfiguredHost(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	token := signToken(t, fx.keys, fx.clock.Now(), tokenSpec{claims: map[string]any{"aud": "nobody.example.test"}})
	r := httptest.NewRequest(http.MethodPost, PathDelegatedExchange, strings.NewReader(exchangeBody(token)))
	r.Header.Set("Content-Type", "application/json")
	r.Host = "nobody.example.test:8443"
	w := httptest.NewRecorder()
	fx.s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || w.Body.String() != notFoundBody {
		t.Fatalf("unconfigured host: %d %s", w.Code, w.Body.String())
	}
}

func TestDelegatedConfigIsValidatedAtStartup(t *testing.T) {
	t.Parallel()
	good := DelegatedIssuer{Host: delegatedHostA, Issuer: issuerA, JWKSURL: "https://id.example.test/jwks"}
	cases := map[string][]DelegatedIssuer{
		"empty":            {},
		"http jwks":        {{Host: delegatedHostA, Issuer: issuerA, JWKSURL: "http://id.example.test/jwks"}},
		"duplicate":        {good, good},
		"host with port":   {{Host: "mail.example.test:443", Issuer: issuerA, JWKSURL: good.JWKSURL}},
		"upper-case host":  {{Host: "Mail.Example.Test", Issuer: issuerA, JWKSURL: good.JWKSURL}},
		"missing issuer":   {{Host: delegatedHostA, JWKSURL: good.JWKSURL}},
		"missing jwks url": {{Host: delegatedHostA, Issuer: issuerA}},
	}
	for name, issuers := range cases {
		t.Run(name, func(t *testing.T) {
			auth, _ := newTestAuth(&fakeValidator{}, &fakeDirectory{}, newFakeClock(), nil)
			_, err := New(Config{Delegated: &DelegatedConfig{Issuers: issuers, Sessions: newFakeDelegatedStore()}}, auth)
			if err == nil {
				t.Fatal("server started with an invalid delegated configuration")
			}
		})
	}
	auth, _ := newTestAuth(&fakeValidator{}, &fakeDirectory{}, newFakeClock(), nil)
	if _, err := New(Config{Delegated: &DelegatedConfig{Issuers: []DelegatedIssuer{good}, Sessions: newFakeDelegatedStore()}}, auth); err != nil {
		t.Fatalf("a valid configuration was refused: %v", err)
	}
}

// ---------------------------------------------------------------------------
// §6 M2 (a): the verification matrix — every row refused with ONE message
// ---------------------------------------------------------------------------

func TestDelegatedExchangeVerificationMatrix(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	keys := fx.keys
	now := fx.clock.Now()
	far := now.Add(2 * time.Hour)

	refused := []struct {
		name string
		spec tokenSpec
	}{
		{"alg none", tokenSpec{alg: "none", kid: kidEd}},
		{"alg HS256", tokenSpec{alg: "HS256", kid: kidEd}},
		{"alg ES256 claimed", tokenSpec{header: map[string]any{"alg": "ES256"}}},
		{"kid missing", tokenSpec{kid: "-"}},
		{"kid unknown", tokenSpec{kid: "no-such-key"}},
		{"kid of a 1024-bit RSA key", tokenSpec{alg: "RS256", kid: kidWeakRSA, signKey: keys.rsaWeak}},
		{"kid of an enc key", tokenSpec{alg: "RS256", kid: "enc"}},
		{"kid of a symmetric key", tokenSpec{alg: "HS256", kid: "hmac"}},
		{"ed25519 signed by another key", tokenSpec{signKey: keys.edOther}},
		{"rsa signed by another key", tokenSpec{alg: "RS256", signKey: keys.rsaOther}},
		{"alg/key mismatch (RS256 with the Ed kid)", tokenSpec{alg: "RS256", kid: kidEd}},
		{"crit header", tokenSpec{header: map[string]any{"crit": []string{"b64"}}}},
		{"typ not JWT", tokenSpec{header: map[string]any{"typ": "JWE"}}},
		{"iss unknown", tokenSpec{claims: map[string]any{"iss": "https://evil.example.test"}}},
		{"iss missing", tokenSpec{claims: map[string]any{"iss": nil}}},
		{"aud of another host (c)", tokenSpec{claims: map[string]any{"aud": delegatedHostB}}},
		{"aud missing", tokenSpec{claims: map[string]any{"aud": nil}}},
		{"aud array without host", tokenSpec{claims: map[string]any{"aud": []string{delegatedHostB, "x"}}}},
		{"aud with scheme", tokenSpec{claims: map[string]any{"aud": "https://" + delegatedHostA}}},
		{"sub missing", tokenSpec{claims: map[string]any{"sub": nil}}},
		{"sub not an address", tokenSpec{claims: map[string]any{"sub": "user"}}},
		{"iat missing", tokenSpec{claims: map[string]any{"iat": nil}}},
		{"exp missing", tokenSpec{claims: map[string]any{"exp": nil}}},
		{"exp not a number", tokenSpec{claims: map[string]any{"exp": "soon"}}},
		{"lifetime 301 s", tokenSpec{claims: map[string]any{"exp": now.Add(301 * time.Second).Unix()}}},
		{"lifetime 5 min but iat in the future", tokenSpec{claims: map[string]any{"iat": far.Unix(), "exp": far.Add(5 * time.Minute).Unix()}}},
		{"expired beyond skew", tokenSpec{claims: map[string]any{"iat": now.Add(-6 * time.Minute).Unix(), "exp": now.Add(-31 * time.Second).Unix()}}},
		{"nbf in the future", tokenSpec{claims: map[string]any{"nbf": now.Add(2 * time.Minute).Unix()}}},
		{"nbf malformed", tokenSpec{claims: map[string]any{"nbf": "later"}}},
		{"jti missing", tokenSpec{claims: map[string]any{"jti": nil}}},
		{"jti empty", tokenSpec{claims: map[string]any{"jti": ""}}},
		{"jti oversized", tokenSpec{claims: map[string]any{"jti": strings.Repeat("j", 300)}}},
		{"purpose revoke at the exchange", tokenSpec{claims: map[string]any{"purpose": purposeRevoke}}},
		{"purpose missing", tokenSpec{claims: map[string]any{"purpose": nil}}},
		{"two parts", tokenSpec{parts: 2}},
		{"padded base64", tokenSpec{padding: true}},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			fx.freshIP()
			w := fx.exchange(signToken(t, keys, now, tc.spec))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status %d body %s, want 401", w.Code, w.Body.String())
			}
			if w.Body.String() != invalidTokenBody {
				t.Fatalf("body %s, want exactly %s (one message for every refusal)", w.Body.String(), invalidTokenBody)
			}
			if w.Header().Get("Content-Type") != problemContentType {
				t.Fatalf("content type %q", w.Header().Get("Content-Type"))
			}
			if w.Header().Get("WWW-Authenticate") != "" {
				t.Fatal("a refused exchange must not challenge (nothing to type into a dialog)")
			}
		})
	}

	t.Run("garbage", func(t *testing.T) {
		for _, raw := range []string{"x", "a.b.c", "....", strings.Repeat("A", 5000)} {
			fx.freshIP()
			if w := fx.exchange(raw); w.Code != http.StatusUnauthorized || w.Body.String() != invalidTokenBody {
				t.Errorf("%q: %d %s", raw[:min(len(raw), 8)], w.Code, w.Body.String())
			}
		}
	})

	accepted := []struct {
		name string
		spec tokenSpec
	}{
		{"EdDSA", tokenSpec{}},
		{"RS256", tokenSpec{alg: "RS256"}},
		{"aud as an array containing the host", tokenSpec{claims: map[string]any{"aud": []string{"x", delegatedHostA}}}},
		{"nbf in the past", tokenSpec{claims: map[string]any{"nbf": now.Add(-time.Minute).Unix()}}},
		{"nbf inside the skew", tokenSpec{claims: map[string]any{"nbf": now.Add(20 * time.Second).Unix()}}},
		{"expired inside the skew", tokenSpec{claims: map[string]any{"iat": now.Add(-5 * time.Minute).Unix(), "exp": now.Add(-20 * time.Second).Unix()}}},
		{"lifetime exactly 300 s", tokenSpec{claims: map[string]any{"exp": now.Add(300 * time.Second).Unix()}}},
		{"typ absent", tokenSpec{header: map[string]any{"typ": nil}}},
		{"sub in mixed case", tokenSpec{claims: map[string]any{"sub": "User@Example.com"}}},
		{"a name claim is ignored", tokenSpec{claims: map[string]any{"name": "Someone Else"}}},
		{"float NumericDate", tokenSpec{claims: map[string]any{"iat": float64(now.Unix()) + 0.5, "exp": float64(now.Unix()) + 200.5}}},
		{"the second issuer of this host", tokenSpec{claims: map[string]any{"iss": issuerB}}},
	}
	for _, tc := range accepted {
		t.Run("accepted: "+tc.name, func(t *testing.T) {
			fx.freshIP()
			resp := fx.session(t, tc.spec)
			if resp.TokenType != "Bearer" || !strings.HasPrefix(resp.SessionToken, "mds1_") || len(resp.SessionToken) != sessionTokenLength {
				t.Fatalf("session = %+v", resp)
			}
			if resp.Account.Address != testAccount().Email || resp.Account.Name != testAccount().Email {
				t.Fatalf("account = %+v (the name comes from the account, never from the token)", resp.Account)
			}
			if resp.JMAP.SessionURL != PathWellKnown || resp.ReadOnly {
				t.Fatalf("session = %+v", resp)
			}
		})
	}
}

func TestDelegatedSessionResponseShape(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	fx.status.name = "Expo Diseño 2026"
	fx.status.readOnly = true

	w := fx.exchange(signToken(t, fx.keys, fx.clock.Now(), tokenSpec{}))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Exactly the OpenAPI Session members, no more, no fewer.
	want := []string{"tokenType", "sessionToken", "expiresAt", "renewAfter", "absoluteExpiresAt", "account", "readOnly", "jmap"}
	if len(got) != len(want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("member %q missing", k)
		}
	}
	now := fx.clock.Now()
	if got["expiresAt"] != wireTime(now.Add(12*time.Hour)) {
		t.Errorf("expiresAt = %v, want 12 h from now", got["expiresAt"])
	}
	if got["renewAfter"] != wireTime(now.Add(11*time.Hour)) {
		t.Errorf("renewAfter = %v, want expiresAt − 1 h", got["renewAfter"])
	}
	if got["absoluteExpiresAt"] != wireTime(now.Add(168*time.Hour)) {
		t.Errorf("absoluteExpiresAt = %v, want 7 d from now", got["absoluteExpiresAt"])
	}
	expires, ok := got["expiresAt"].(string)
	if !ok || !strings.HasSuffix(expires, "Z") || len(expires) != len("2026-10-22T21:40:55.310Z") {
		t.Errorf("expiresAt = %v, want millisecond UTC form", got["expiresAt"])
	}
	acct, ok := got["account"].(map[string]any)
	if !ok {
		t.Fatalf("account = %v, want an object", got["account"])
	}
	if acct["name"] != "Expo Diseño 2026" || got["readOnly"] != true {
		t.Errorf("account/readOnly = %v / %v: the status source was not consulted", acct, got["readOnly"])
	}
	if fx.obs.count(DelegatedExchangeOK) != 1 {
		t.Errorf("ok exchanges observed = %d", fx.obs.count(DelegatedExchangeOK))
	}
}

func TestDelegatedExchangeBodyErrors(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)

	cases := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantBody    string
	}{
		{"missing token", `{}`, "application/json", 400, `{"field":"token","reason":"required"}`},
		{"empty token", `{"token":""}`, "application/json", 400, `{"field":"token","reason":"required"}`},
		{"not an object", `["x"]`, "application/json", 400, `{"field":"","reason":"the body is not a JSON object"}`},
		{"not json", `nope`, "application/json", 400, `{"field":"","reason":"the body is not a JSON object"}`},
		{"wrong media type", `{"token":"x"}`, "text/plain", 415, `{"reason":"Content-Type must be application/json"}`},
		{"too large", `{"token":"` + strings.Repeat("A", 17*1024) + `"}`, "application/json", 413, `{"reason":"the body is too large"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, PathDelegatedExchange, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			r.Host = delegatedHostA
			w := httptest.NewRecorder()
			fx.s.Handler().ServeHTTP(w, r)
			if w.Code != tc.wantStatus || w.Body.String() != tc.wantBody {
				t.Fatalf("%d %s, want %d %s", w.Code, w.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// §6 M2 (b): replay
// ---------------------------------------------------------------------------

func TestDelegatedReplayIsRefused(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	token := signToken(t, fx.keys, fx.clock.Now(), tokenSpec{})
	if w := fx.exchange(token); w.Code != http.StatusOK {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	if w := fx.exchange(token); w.Code != http.StatusUnauthorized || w.Body.String() != invalidTokenBody {
		t.Fatalf("replay: %d %s, want the single 401", w.Code, w.Body.String())
	}
	// A different token with the SAME jti is the same replay.
	dup := signToken(t, fx.keys, fx.clock.Now(), tokenSpec{claims: map[string]any{"jti": "fixed"}})
	dup2 := signToken(t, fx.keys, fx.clock.Now(), tokenSpec{claims: map[string]any{"jti": "fixed", "name": "x"}})
	if w := fx.exchange(dup); w.Code != http.StatusOK {
		t.Fatalf("fixed jti first: %d", w.Code)
	}
	if w := fx.exchange(dup2); w.Code != http.StatusUnauthorized {
		t.Fatalf("fixed jti second: %d, want 401", w.Code)
	}
	// The same jti through ANOTHER issuer is a different token.
	other := signToken(t, fx.keys, fx.clock.Now(), tokenSpec{claims: map[string]any{"jti": "fixed", "iss": issuerB}})
	if w := fx.exchange(other); w.Code != http.StatusOK {
		t.Fatalf("same jti, other issuer: %d %s", w.Code, w.Body.String())
	}
	if fx.obs.count(DelegatedExchangeInvalid) != 2 {
		t.Errorf("invalid exchanges observed = %d, want 2", fx.obs.count(DelegatedExchangeInvalid))
	}
}

// ---------------------------------------------------------------------------
// §6 M2 (d): the account refusals say which screen to show
// ---------------------------------------------------------------------------

func TestDelegatedAccountRefusalsCarryACode(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	now := fx.clock.Now()

	problem := func(t *testing.T, w *httptest.ResponseRecorder, code string) {
		t.Helper()
		if w.Code != http.StatusForbidden {
			t.Fatalf("status %d body %s, want 403", w.Code, w.Body.String())
		}
		var p map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if p["code"] != code || p["type"] != "about:blank" || p["status"] != float64(403) || p["detail"] == "" {
			t.Fatalf("problem = %v, want code %q", p, code)
		}
		if w.Header().Get("Content-Type") != problemContentType {
			t.Fatalf("content type %q", w.Header().Get("Content-Type"))
		}
	}

	// Signature fine, no such account: notProvisioned — never auto-provision.
	w := fx.exchange(signToken(t, fx.keys, now, tokenSpec{claims: map[string]any{"sub": "ghost@example.com"}}))
	problem(t, w, "notProvisioned")
	if _, err := fx.d.GetAccountByEmail(context.Background(), "ghost@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("the exchange provisioned an account")
	}

	// Suspended per the status source.
	fx.status.mu.Lock()
	fx.status.suspended[testAccount().ID] = true
	fx.status.mu.Unlock()
	problem(t, fx.exchange(signToken(t, fx.keys, now, tokenSpec{})), "suspended")

	// Disabled in the store.
	fx.status.mu.Lock()
	fx.status.suspended[testAccount().ID] = false
	fx.status.mu.Unlock()
	disabled := testAccount()
	disabled.State = store.AccountDisabled
	fx.d.put(disabled)
	problem(t, fx.exchange(signToken(t, fx.keys, now, tokenSpec{})), "disabled")

	if fx.obs.count(DelegatedExchangeAccount) != 3 {
		t.Errorf("account refusals observed = %d, want 3", fx.obs.count(DelegatedExchangeAccount))
	}
}

// A suspension takes effect on the very next request of a LIVE session, and
// so does disabling and deleting the account (the store is consulted every
// time, as for Basic).
func TestDelegatedSessionRecheckedOnEveryRequest(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	sess := fx.session(t, tokenSpec{})

	if w := fx.do(http.MethodGet, PathWellKnown, "", bearer(sess.SessionToken)); w.Code != http.StatusOK {
		t.Fatalf("live session: %d", w.Code)
	}
	fx.status.mu.Lock()
	fx.status.suspended[testAccount().ID] = true
	fx.status.mu.Unlock()
	w := fx.do(http.MethodGet, PathWellKnown, "", bearer(sess.SessionToken))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"suspended"`) {
		t.Fatalf("after suspend: %d %s", w.Code, w.Body.String())
	}
	fx.status.mu.Lock()
	fx.status.suspended[testAccount().ID] = false
	fx.status.mu.Unlock()

	disabled := testAccount()
	disabled.State = store.AccountDisabled
	fx.d.put(disabled)
	if w := fx.do(http.MethodGet, PathWellKnown, "", bearer(sess.SessionToken)); w.Code != http.StatusForbidden {
		t.Fatalf("after disable: %d", w.Code)
	}

	fx.d.mu.Lock()
	delete(fx.d.accounts, testAccount().Email)
	fx.d.mu.Unlock()
	if w := fx.do(http.MethodGet, PathWellKnown, "", bearer(sess.SessionToken)); w.Code != http.StatusForbidden {
		t.Fatalf("after delete: %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// §6 M2 (e): renewal, and the absolute lifetime
// ---------------------------------------------------------------------------

func TestDelegatedRenewIssuesANewTokenAndKeepsTheOldFor60s(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	first := fx.session(t, tokenSpec{})

	fx.clock.Advance(11 * time.Hour)
	w := fx.do(http.MethodPost, PathDelegatedRenew, "", bearer(first.SessionToken))
	if w.Code != http.StatusOK {
		t.Fatalf("renew: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("renew response is cacheable")
	}
	var second sessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if second.SessionToken == first.SessionToken {
		t.Fatal("renew returned the same token")
	}
	if second.AbsoluteExpiresAt != first.AbsoluteExpiresAt {
		t.Fatalf("absolute lifetime moved: %s → %s", first.AbsoluteExpiresAt, second.AbsoluteExpiresAt)
	}
	if second.ExpiresAt != wireTime(fx.clock.Now().Add(12*time.Hour)) {
		t.Fatalf("renewed expiresAt = %s, want a fresh 12 h", second.ExpiresAt)
	}

	// Both work inside the grace window…
	for _, tok := range []string{first.SessionToken, second.SessionToken} {
		if w := fx.do(http.MethodGet, PathWellKnown, "", bearer(tok)); w.Code != http.StatusOK {
			t.Fatalf("inside grace: %d", w.Code)
		}
	}
	// …and only the new one after it.
	fx.clock.Advance(61 * time.Second)
	if w := fx.do(http.MethodGet, PathWellKnown, "", bearer(first.SessionToken)); w.Code != http.StatusUnauthorized {
		t.Fatalf("old token after grace: %d, want 401", w.Code)
	}
	if w := fx.do(http.MethodGet, PathWellKnown, "", bearer(second.SessionToken)); w.Code != http.StatusOK {
		t.Fatalf("new token after grace: %d", w.Code)
	}
	// The old token cannot renew any more either.
	if w := fx.do(http.MethodPost, PathDelegatedRenew, "", bearer(first.SessionToken)); w.Code != http.StatusUnauthorized {
		t.Fatalf("renew with the old token: %d", w.Code)
	}
}

func TestDelegatedRenewPastAbsoluteLifetimeIs401(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, func(c *DelegatedConfig) {
		c.SessionTTL = time.Hour
		c.SessionMax = 3 * time.Hour
	})
	sess := fx.session(t, tokenSpec{})
	if sess.AbsoluteExpiresAt != wireTime(fx.clock.Now().Add(3*time.Hour)) {
		t.Fatalf("absolute = %s", sess.AbsoluteExpiresAt)
	}
	token := sess.SessionToken
	// Renew twice inside the lifetime; the last renewal is capped at the
	// ceiling rather than overshooting it.
	for i := range 2 {
		fx.clock.Advance(50 * time.Minute)
		w := fx.do(http.MethodPost, PathDelegatedRenew, "", bearer(token))
		if w.Code != http.StatusOK {
			t.Fatalf("renew %d: %d %s", i, w.Code, w.Body.String())
		}
		var next sessionResponse
		_ = json.Unmarshal(w.Body.Bytes(), &next)
		token = next.SessionToken
		if next.ExpiresAt > next.AbsoluteExpiresAt {
			t.Fatalf("renewal overshot the absolute lifetime: %s > %s", next.ExpiresAt, next.AbsoluteExpiresAt)
		}
	}
	// Past the ceiling: the session is dead and cannot be renewed.
	fx.clock.Advance(2 * time.Hour)
	if w := fx.do(http.MethodPost, PathDelegatedRenew, "", bearer(token)); w.Code != http.StatusUnauthorized {
		t.Fatalf("renew past absolute: %d, want 401", w.Code)
	}
	if w := fx.do(http.MethodGet, PathWellKnown, "", bearer(token)); w.Code != http.StatusUnauthorized {
		t.Fatalf("use past absolute: %d, want 401", w.Code)
	}
}

func TestDelegatedRenewRefusesBasic(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	r := httptest.NewRequest(http.MethodPost, PathDelegatedRenew, nil)
	r.Host = delegatedHostA
	r.SetBasicAuth("user@example.com", testPassword)
	w := httptest.NewRecorder()
	fx.s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("basic at renew: %d, want 401 (nothing to renew)", w.Code)
	}
	if fx.v.callCount() != 0 {
		t.Fatal("a Basic credential reached Dovecot from the renew route")
	}
}

// ---------------------------------------------------------------------------
// §6 M2 (f): issuer revoke ends the session on the next request, Basic alone
// ---------------------------------------------------------------------------

func TestDelegatedIssuerRevoke(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	now := fx.clock.Now()

	viaA1 := fx.session(t, tokenSpec{})
	viaA2 := fx.session(t, tokenSpec{})
	viaB := fx.session(t, tokenSpec{claims: map[string]any{"iss": issuerB}})

	// A login token is not accepted at the revoke route, and vice versa.
	if w := fx.do(http.MethodPost, PathDelegatedRevoke, exchangeBody(signToken(t, fx.keys, now, tokenSpec{})), nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("login token at revoke: %d", w.Code)
	}

	revoke := signToken(t, fx.keys, now, tokenSpec{claims: map[string]any{"purpose": purposeRevoke}})
	w := fx.do(http.MethodPost, PathDelegatedRevoke, exchangeBody(revoke), nil)
	if w.Code != http.StatusOK || w.Body.String() != `{"revoked":2}` {
		t.Fatalf("revoke: %d %s, want 200 {\"revoked\":2}", w.Code, w.Body.String())
	}
	// Replaying the revoke token is refused; a fresh one is idempotent.
	if w := fx.do(http.MethodPost, PathDelegatedRevoke, exchangeBody(revoke), nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed revoke: %d", w.Code)
	}
	again := signToken(t, fx.keys, now, tokenSpec{claims: map[string]any{"purpose": purposeRevoke}})
	if w := fx.do(http.MethodPost, PathDelegatedRevoke, exchangeBody(again), nil); w.Body.String() != `{"revoked":0}` {
		t.Fatalf("second revoke: %s", w.Body.String())
	}

	for _, tok := range []string{viaA1.SessionToken, viaA2.SessionToken} {
		if w := fx.do(http.MethodGet, PathWellKnown, "", bearer(tok)); w.Code != http.StatusUnauthorized {
			t.Fatalf("revoked session still works: %d", w.Code)
		}
	}
	if w := fx.do(http.MethodGet, PathWellKnown, "", bearer(viaB.SessionToken)); w.Code != http.StatusOK {
		t.Fatalf("session from the other issuer was revoked too: %d", w.Code)
	}
	// Basic is untouched.
	r := httptest.NewRequest(http.MethodGet, PathWellKnown, nil)
	r.Host = delegatedHostA
	r.SetBasicAuth("user@example.com", testPassword)
	rec := httptest.NewRecorder()
	fx.s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("basic after revoke: %d", rec.Code)
	}

	// An unknown subject revokes nothing and says so — no 403 oracle here.
	ghost := signToken(t, fx.keys, now, tokenSpec{claims: map[string]any{"purpose": purposeRevoke, "sub": "ghost@example.com"}})
	if w := fx.do(http.MethodPost, PathDelegatedRevoke, exchangeBody(ghost), nil); w.Code != http.StatusOK || w.Body.String() != `{"revoked":0}` {
		t.Fatalf("ghost revoke: %d %s", w.Code, w.Body.String())
	}
}

// The seam M1's accounts.SessionRevoker wires: suspend/delete end every
// delegated session AND the account's push/blob tokens.
func TestRevokeDelegatedSessionsSeam(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	a := fx.session(t, tokenSpec{})
	b := fx.session(t, tokenSpec{claims: map[string]any{"iss": issuerB}})
	push := mintTestTokens(t, fx.s, "push")["push"]

	if err := fx.s.RevokeDelegatedSessions(context.Background(), testAccount().ID); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{a.SessionToken, b.SessionToken} {
		if w := fx.do(http.MethodGet, PathWellKnown, "", bearer(tok)); w.Code != http.StatusUnauthorized {
			t.Fatalf("session survived the account revoke: %d", w.Code)
		}
	}
	if _, err := fx.s.tokens.Verify(push, ScopePush); err == nil {
		t.Fatal("push token survived the account revoke")
	}
	// Harmless when the feature is off.
	s, _, _, _ := newTestServer(t, nil)
	if err := s.RevokeDelegatedSessions(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// §6 M2 (g): the Bearer-accepting set equals the Basic set
// ---------------------------------------------------------------------------

func TestBearerAcceptedExactlyWhereBasicIs(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	sess := fx.session(t, tokenSpec{})

	for _, rt := range fx.s.routes() {
		if rt.public {
			continue
		}
		body := ""
		if rt.method == http.MethodPost || rt.method == http.MethodPut {
			body = "{}"
		}
		path := strings.NewReplacer("{accountId}", "a1", "{blobId}", "b1", "{name}", "n", "{asset}", "logo").Replace(rt.pattern)

		viaBasic := httptest.NewRequest(rt.method, path, strings.NewReader(body))
		viaBasic.Host = delegatedHostA
		viaBasic.Header.Set("Content-Type", "application/json")
		viaBasic.SetBasicAuth("user@example.com", testPassword)
		wb := httptest.NewRecorder()
		fx.s.Handler().ServeHTTP(wb, viaBasic)

		viaBearer := httptest.NewRequest(rt.method, path, strings.NewReader(body))
		viaBearer.Host = delegatedHostA
		viaBearer.Header.Set("Content-Type", "application/json")
		viaBearer.Header.Set("Authorization", "Bearer "+sess.SessionToken)
		wr := httptest.NewRecorder()
		fx.s.Handler().ServeHTTP(wr, viaBearer)

		if wb.Code == http.StatusUnauthorized {
			t.Errorf("%s %s: Basic itself got 401; the comparison is meaningless", rt.method, rt.pattern)
		}
		if wr.Code != wb.Code {
			t.Errorf("%s %s: bearer %d, basic %d — the two schemes must be accepted on exactly the same routes",
				rt.method, rt.pattern, wr.Code, wb.Code)
		}
	}
}

func TestBearerResolvesToTheSameIdentity(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	sess := fx.session(t, tokenSpec{})
	w := fx.do(http.MethodGet, PathWellKnown, "", bearer(sess.SessionToken))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var obj map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &obj)
	if obj["username"] != testAccount().Email {
		t.Fatalf("username = %v", obj["username"])
	}
	if fx.v.callCount() != 0 {
		t.Fatal("a bearer request reached Dovecot")
	}
	// Push and blob tokens mint under a bearer session, exactly as under
	// Basic — the PWA's SSE depends on it.
	mint := fx.do(http.MethodPost, PathToken, `{"scopes":["push","blob"]}`, bearer(sess.SessionToken))
	if mint.Code != http.StatusOK {
		t.Fatalf("mint under bearer: %d %s", mint.Code, mint.Body.String())
	}
	var minted mintResponse
	_ = json.Unmarshal(mint.Body.Bytes(), &minted)
	if minted.Tokens["push"].Token == "" || minted.Tokens["blob"].Token == "" {
		t.Fatalf("minted = %+v", minted)
	}
	if id, err := fx.s.tokens.Verify(minted.Tokens["push"].Token, ScopePush); err != nil || id != testAccount().ID {
		t.Fatalf("push token minted under bearer does not verify: %v", err)
	}
	// A bearer on a token route with the header wins over the query.
	w = fx.do(http.MethodGet, PathEventSource, "", bearer(sess.SessionToken))
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Fatalf("bearer at eventsource: %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// §6 M2 (h): the token-in-query set is unchanged — a session token in
// access_token is refused
// ---------------------------------------------------------------------------

func TestSessionTokenRefusedInQuery(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	sess := fx.session(t, tokenSpec{})
	for _, p := range []string{PathEventSource, "/jmap/download/a1/b1/n"} {
		w := fx.do(http.MethodGet, p+"?access_token="+sess.SessionToken, "", nil)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s with a session token in the query: %d, want 403", p, w.Code)
		}
	}
	// And at /jmap/api, in the query, with no header: the ordinary challenge.
	w := fx.do(http.MethodPost, PathAPI+"?access_token="+sess.SessionToken, apiBody(""), nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("api with session token in query: %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Bad bearers: never Dovecot, but budgeted per IP
// ---------------------------------------------------------------------------

func TestBadBearerNeverReachesDovecotAndIsBudgeted(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	bad := []string{
		"mds1_" + strings.Repeat("A", 43), // well-formed, unknown
		"msa1_" + strings.Repeat("A", 43), // M1's service-account key: never a session
		"mds1_short",                      // malformed
		strings.Repeat("A", 5000),         // garbage
		"mt1.cHVzaHw3fDF8YWJj.AAAA",       // a scoped token in the header
	}
	for _, tok := range bad {
		w := fx.do(http.MethodPost, PathAPI, apiBody(""), bearer(tok))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%.12s: %d, want 401", tok, w.Code)
		}
		if ch := w.Header().Get("WWW-Authenticate"); !strings.HasPrefix(ch, "Bearer ") {
			t.Errorf("challenge %q, want a Bearer challenge (never Basic: no dialog)", ch)
		}
		if w.Body.String() != `{"detail":"invalid session","status":401,"type":"about:blank"}` {
			t.Errorf("body %s", w.Body.String())
		}
	}
	if fx.v.callCount() != 0 {
		t.Fatalf("%d LOGINs reached Dovecot for bad bearers", fx.v.callCount())
	}

	// The budget: 60 refusals per minute per IP, then 429 with Retry-After.
	var last *httptest.ResponseRecorder
	for range delegatedBearerFailureRate + 5 {
		last = fx.do(http.MethodGet, PathWellKnown, "", bearer("mds1_"+strings.Repeat("B", 43)))
	}
	if last.Code != http.StatusTooManyRequests || last.Header().Get("Retry-After") == "" {
		t.Fatalf("after %d failures: %d (Retry-After %q)", delegatedBearerFailureRate+5, last.Code, last.Header().Get("Retry-After"))
	}
	// A GOOD session from the same IP is refused while the budget is empty
	// (the limiter is per IP, and it protects the token space, not the user)…
	sess := fx.session(t, tokenSpec{})
	if w := fx.do(http.MethodGet, PathWellKnown, "", bearer(sess.SessionToken)); w.Code != http.StatusTooManyRequests {
		t.Fatalf("good bearer during lockout: %d", w.Code)
	}
	// …and works again once it refills.
	fx.clock.Advance(time.Minute)
	if w := fx.do(http.MethodGet, PathWellKnown, "", bearer(sess.SessionToken)); w.Code != http.StatusOK {
		t.Fatalf("good bearer after refill: %d", w.Code)
	}
}

func TestDelegatedExchangeRateLimit(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	var last *httptest.ResponseRecorder
	for range delegatedExchangeRate + 1 {
		last = fx.exchange("garbage")
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("exchange %d: %d, want 429", delegatedExchangeRate+1, last.Code)
	}
	if last.Header().Get("Retry-After") == "" || !strings.Contains(last.Body.String(), `"reason"`) {
		t.Fatalf("429 shape: Retry-After %q body %s", last.Header().Get("Retry-After"), last.Body.String())
	}
	// Exchange and revoke share the budget (both are "presentations").
	revoke := signToken(t, fx.keys, fx.clock.Now(), tokenSpec{claims: map[string]any{"purpose": purposeRevoke}})
	if w := fx.do(http.MethodPost, PathDelegatedRevoke, exchangeBody(revoke), nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("revoke during exchange lockout: %d", w.Code)
	}
	// Another IP is unaffected.
	r := httptest.NewRequest(http.MethodPost, PathDelegatedExchange, strings.NewReader(exchangeBody("garbage")))
	r.Header.Set("Content-Type", "application/json")
	r.Host = delegatedHostA
	r.RemoteAddr = "198.51.100.7:1"
	w := httptest.NewRecorder()
	fx.s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("other IP: %d", w.Code)
	}
	fx.clock.Advance(time.Minute)
	if w := fx.exchange("garbage"); w.Code != http.StatusUnauthorized {
		t.Fatalf("after refill: %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// JWKS: cache, rotation, throttle, outage
// ---------------------------------------------------------------------------

func TestDelegatedJWKSCachingRotationAndOutage(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	now := fx.clock.Now

	fx.session(t, tokenSpec{})
	fx.session(t, tokenSpec{alg: "RS256"})
	if n := fx.jwks.fetches.Load(); n != 1 {
		t.Fatalf("fetches after two exchanges = %d, want 1 (cached)", n)
	}

	// Unknown kid → one refetch; a second unknown kid within 60 s → none.
	fx.exchange(signToken(t, fx.keys, now(), tokenSpec{kid: "rotated-1"}))
	fx.exchange(signToken(t, fx.keys, now(), tokenSpec{kid: "rotated-2"}))
	if n := fx.jwks.fetches.Load(); n != 2 {
		t.Fatalf("fetches after two unknown kids = %d, want 2 (refetch at most once per 60 s)", n)
	}

	// A rotation: publish the new key, and the first token signed with it is
	// accepted once the throttle allows a refetch.
	_, newPriv, _ := ed25519.GenerateKey(rand.Reader)
	fx.jwks.mu.Lock()
	var doc map[string]any
	_ = json.Unmarshal(fx.jwks.body, &doc)
	keys, ok := doc["keys"].([]any)
	if !ok {
		t.Fatalf("the JWKS document has no keys array: %v", doc)
	}
	newPub, ok := newPriv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("an Ed25519 private key did not yield an Ed25519 public key")
	}
	keys = append(keys, map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "ed-2026-10", "use": "sig", "alg": "EdDSA",
		"x": b64(newPub)})
	doc["keys"] = keys
	fx.jwks.body, _ = json.Marshal(doc)
	fx.jwks.mu.Unlock()

	fx.clock.Advance(61 * time.Second)
	if w := fx.exchange(signToken(t, fx.keys, now(), tokenSpec{kid: "ed-2026-10", signKey: newPriv})); w.Code != http.StatusOK {
		t.Fatalf("token with the rotated key: %d %s", w.Code, w.Body.String())
	}

	// Outage with a matching cached key: still served. Outage with an
	// unknown kid: 503 + Retry-After, the one case that shows through.
	fx.jwks.setFail(true)
	fx.clock.Advance(11 * time.Minute) // cache stale
	if w := fx.exchange(signToken(t, fx.keys, now(), tokenSpec{})); w.Code != http.StatusOK {
		t.Fatalf("stale cached key during outage: %d %s", w.Code, w.Body.String())
	}
	fx.clock.Advance(61 * time.Second)
	w := fx.exchange(signToken(t, fx.keys, now(), tokenSpec{kid: "never-seen"}))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("unknown kid during outage: %d %s (Retry-After %q)", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
	}
	if !strings.Contains(w.Body.String(), `"reason"`) {
		t.Fatalf("503 body %s", w.Body.String())
	}
}

func TestDelegatedJWKSUnreachableFromTheStartIs503(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	fx.jwks.setFail(true)
	w := fx.exchange(signToken(t, fx.keys, fx.clock.Now(), tokenSpec{}))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// Unreachable is not "invalid": no invalid exchange is counted.
	if fx.obs.count(DelegatedExchangeInvalid) != 0 {
		t.Fatal("an outage was counted as an invalid token")
	}
}

// ---------------------------------------------------------------------------
// Logout
// ---------------------------------------------------------------------------

func TestDelegatedLogoutIs204AlwaysAndRevokes(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	sess := fx.session(t, tokenSpec{})
	push := mintTestTokensWith(t, fx, sess.SessionToken)

	if w := fx.do(http.MethodPost, PathDelegatedLogout, "", bearer(sess.SessionToken)); w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := fx.do(http.MethodGet, PathWellKnown, "", bearer(sess.SessionToken)); w.Code != http.StatusUnauthorized {
		t.Fatalf("session after logout: %d", w.Code)
	}
	if _, err := fx.s.tokens.Verify(push, ScopePush); err == nil {
		t.Fatal("push token survived logout")
	}
	// Dead session, garbage, no header: 204, 204, 204.
	for _, h := range []map[string]string{bearer(sess.SessionToken), bearer("nonsense"), nil} {
		if w := fx.do(http.MethodPost, PathDelegatedLogout, "", h); w.Code != http.StatusNoContent {
			t.Errorf("logout with %v: %d, want 204", h, w.Code)
		}
	}
}

func mintTestTokensWith(t *testing.T, fx *delegatedFixture, session string) string {
	t.Helper()
	w := fx.do(http.MethodPost, PathToken, `{"scopes":["push"]}`, bearer(session))
	if w.Code != http.StatusOK {
		t.Fatalf("mint: %d %s", w.Code, w.Body.String())
	}
	var resp mintResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return resp.Tokens["push"].Token
}

// ---------------------------------------------------------------------------
// Secrets never reach the log
// ---------------------------------------------------------------------------

func TestDelegatedNeverLogsTokens(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	good := signToken(t, fx.keys, fx.clock.Now(), tokenSpec{})
	bad := signToken(t, fx.keys, fx.clock.Now(), tokenSpec{signKey: fx.keys.edOther})

	fx.exchange(bad)
	w := fx.exchange(good)
	var resp sessionResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	fx.do(http.MethodGet, PathWellKnown, "", bearer(resp.SessionToken))
	fx.do(http.MethodGet, PathWellKnown, "", bearer("mds1_"+strings.Repeat("Q", 43)))
	fx.do(http.MethodPost, PathDelegatedRenew, "", bearer(resp.SessionToken))

	logged := fx.logs.String()
	if logged == "" {
		t.Fatal("nothing was logged")
	}
	for _, secret := range []string{good, bad, resp.SessionToken, strings.Repeat("Q", 43)} {
		// The JWT's payload segment alone is also a secret's worth of
		// information (it is the token minus a signature).
		for _, piece := range strings.Split(secret, ".") {
			if len(piece) > 16 && strings.Contains(logged, piece) {
				t.Fatalf("a token fragment reached the log: %.16s…", piece)
			}
		}
	}
	if !strings.Contains(logged, "token refused") || !strings.Contains(logged, "reason=") {
		t.Fatal("the debug reason line is missing — refusals must be diagnosable by the operator")
	}
}

// ---------------------------------------------------------------------------
// The last-seen write is throttled
// ---------------------------------------------------------------------------

func TestDelegatedTouchIsThrottled(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	sess := fx.session(t, tokenSpec{})
	for range 5 {
		fx.do(http.MethodGet, PathWellKnown, "", bearer(sess.SessionToken))
	}
	fx.store.mu.Lock()
	n := fx.store.touches
	fx.store.mu.Unlock()
	if n != 1 {
		t.Fatalf("touches after 5 requests = %d, want 1", n)
	}
	fx.clock.Advance(2 * time.Minute)
	fx.do(http.MethodGet, PathWellKnown, "", bearer(sess.SessionToken))
	fx.store.mu.Lock()
	n = fx.store.touches
	fx.store.mu.Unlock()
	if n != 2 {
		t.Fatalf("touches after the interval = %d, want 2", n)
	}
}

// A store failure on the bearer path is a 503, never a 401 with a strike.
func TestBearerStoreFailureIs503WithoutStrike(t *testing.T) {
	t.Parallel()
	fx := newDelegatedFixture(t, nil)
	sess := fx.session(t, tokenSpec{})
	fx.store.mu.Lock()
	fx.store.err = errors.New("pg down")
	fx.store.mu.Unlock()
	w := fx.do(http.MethodGet, PathWellKnown, "", bearer(sess.SessionToken))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	fx.store.mu.Lock()
	fx.store.err = nil
	fx.store.mu.Unlock()
	if ok, _ := fx.s.delegated.bearerLimiter.peek("203.0.113.10"); !ok {
		t.Fatal("an outage charged the client's budget")
	}
}
