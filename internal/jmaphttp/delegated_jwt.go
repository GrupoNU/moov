package jmaphttp

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The delegated sign-in token verifier (contract §3.2), written against the
// standard library and nothing else.
//
// # Why no JWT library
//
// The profile this server accepts is small and closed: a compact JWS, two
// algorithms (EdDSA over Ed25519, RS256), a required `kid`, eight claims with
// fixed rules, a size cap. A general JWT library brings the whole of RFC 7515-
// 7519 — `alg: none`, HMAC family, nested JWE, `x5c` chains, JSON serialization
// — and the history of JWT vulnerabilities is largely the history of servers
// accepting a branch of that generality they never meant to. Writing the
// narrow verifier is less code than auditing the wide one, it keeps the
// vendored tree hermetic (the same argument internal/metrics makes), and the
// two primitives that must be right — ed25519.Verify and rsa.VerifyPKCS1v15 —
// are the standard library's, constant-time and maintained.
//
// # The order of checks, and what it leaks
//
// Every refusal collapses to errDelegatedTokenInvalid on the wire (one 401,
// one message), so the ORDER only matters internally. It is: size, structure,
// header (alg/kid/crit), issuer resolution for this host, key lookup,
// signature, then claims. Nothing read from the payload is acted on before the
// signature verifies except `iss`, which selects the key set — the standard
// bootstrap, and harmless: an attacker naming a real issuer still has to
// produce that issuer's signature. The internal reason travels back to the
// handler for a debug log line that never includes the token.
//
// # JWKS caching
//
// Keys are fetched from the issuer's HTTPS URL and cached for jwksCacheTTL.
// An unknown `kid` triggers a refetch, at most once per jwksRefetchMinGap — so
// a rotation is picked up on the first token signed with the new key, while a
// flood of garbage `kid`s cannot turn this server into a request generator
// against the issuer. If the fetch fails and no cached key (stale or not)
// matches, the caller answers 503 with Retry-After: the only case where the
// issuer's availability shows through (§3.2).

const (
	// maxDelegatedTokenBytes caps a presented token (§3.2 "Size limit 4096").
	maxDelegatedTokenBytes = 4096

	// delegatedMaxLifetime is the longest a token may live (exp − iat).
	delegatedMaxLifetime = 300 * time.Second

	// delegatedClockSkew is the tolerance applied to exp, nbf and iat.
	delegatedClockSkew = 30 * time.Second

	// jwksCacheTTL is how long a fetched key set is fresh.
	jwksCacheTTL = 10 * time.Minute

	// jwksRefetchMinGap bounds unknown-kid refetches.
	jwksRefetchMinGap = 60 * time.Second

	// jwksFetchTimeout bounds one fetch; jwksMaxBody bounds its body.
	jwksFetchTimeout = 5 * time.Second
	jwksMaxBody      = 256 * 1024

	// minRSABits is the smallest RSA modulus accepted (§3.2 "≥ 2048 bits").
	minRSABits = 2048

	// maxJTILength bounds what is stored in the replay cache. A UUID is 36.
	maxJTILength = 256
)

// The two purposes a token may carry, each bound to exactly one route.
const (
	purposeLogin  = "login"
	purposeRevoke = "revoke"
)

// errDelegatedTokenInvalid is the single external truth about a refused
// token. Wrapped with an internal reason for the debug log; never with the
// token itself.
var errDelegatedTokenInvalid = errors.New("invalid delegated token")

// errDelegatedKeysUnavailable is the 503 case: the issuer's JWKS could not be
// fetched and no cached key matched.
var errDelegatedKeysUnavailable = errors.New("delegated issuer keys unavailable")

func refuseToken(reason string) error {
	return fmt.Errorf("%w: %s", errDelegatedTokenInvalid, reason)
}

// DelegatedIssuer is one configured issuer for one host (contract §3.3).
type DelegatedIssuer struct {
	// Host is what the browser reaches and what `aud` must equal: a bare
	// hostname, lower-case, no scheme, no port.
	Host string
	// Issuer is the exact `iss` string.
	Issuer string
	// JWKSURL is where the issuer publishes its signing keys. HTTPS only.
	JWKSURL string
}

// delegatedClaims is the verified content of a token — only the claims the
// contract reads. Anything else in the payload is ignored.
type delegatedClaims struct {
	Issuer   string
	Subject  string
	JTI      string
	Purpose  string
	Expires  time.Time
	IssuedAt time.Time
}

// jwksKey is one usable signing key.
type jwksKey struct {
	kid string
	alg string
	ed  ed25519.PublicKey
	rsa *rsa.PublicKey
}

// jwksCache is one issuer's key set with the fetch policy above.
type jwksCache struct {
	url    string
	client *http.Client
	now    func() time.Time

	mu          sync.Mutex
	keys        map[string]jwksKey
	fetchedAt   time.Time
	lastAttempt time.Time
	inflight    *sync.WaitGroup
}

func newJWKSCache(url string, client *http.Client, now func() time.Time) *jwksCache {
	return &jwksCache{url: url, client: client, now: now, keys: map[string]jwksKey{}}
}

// lookup returns the key for kid, fetching or refetching per the policy.
func (c *jwksCache) lookup(ctx context.Context, kid string) (jwksKey, error) {
	c.mu.Lock()
	now := c.now()
	fresh := !c.fetchedAt.IsZero() && now.Sub(c.fetchedAt) < jwksCacheTTL
	if k, ok := c.keys[kid]; ok && fresh {
		c.mu.Unlock()
		return k, nil
	}
	// Refetch when the cache is stale, or when the kid is unknown and the
	// throttle allows. Concurrent lookups share one fetch.
	mayFetch := !fresh || now.Sub(c.lastAttempt) >= jwksRefetchMinGap
	if !mayFetch {
		k, ok := c.keys[kid]
		c.mu.Unlock()
		if ok {
			return k, nil
		}
		return jwksKey{}, refuseToken("unknown kid (refetch throttled)")
	}
	if c.inflight != nil {
		wg := c.inflight
		c.mu.Unlock()
		wg.Wait()
		return c.lookupCached(kid)
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	c.inflight = wg
	c.lastAttempt = now
	c.mu.Unlock()

	keys, fetchErr := c.fetch(ctx)

	c.mu.Lock()
	if fetchErr == nil {
		c.keys = keys
		c.fetchedAt = c.now()
	}
	c.inflight = nil
	c.mu.Unlock()
	wg.Done()

	k, err := c.lookupCached(kid)
	if err != nil && fetchErr != nil {
		// Nothing cached matches AND the issuer is unreachable: the one case
		// that shows through (503).
		return jwksKey{}, fmt.Errorf("%w: %w", errDelegatedKeysUnavailable, fetchErr)
	}
	return k, err
}

func (c *jwksCache) lookupCached(kid string) (jwksKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if k, ok := c.keys[kid]; ok {
		return k, nil
	}
	return jwksKey{}, refuseToken("unknown kid")
}

// fetch downloads and parses the key set. Every parse problem is a fetch
// failure: a key set this server cannot read is as good as unreachable.
func (c *jwksCache) fetch(ctx context.Context) (map[string]jwksKey, error) {
	ctx, cancel := context.WithTimeout(ctx, jwksFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("building jwks request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching jwks: status %d", resp.StatusCode)
	}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil ||
		(mt != "application/json" && mt != "application/jwk-set+json") {
		return nil, fmt.Errorf("fetching jwks: content type %q", resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, jwksMaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("reading jwks: %w", err)
	}
	if len(body) > jwksMaxBody {
		return nil, errors.New("reading jwks: body too large")
	}
	return parseJWKS(body)
}

// jwk is the wire shape of one key; only the members this profile reads.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// parseJWKS keeps the keys that satisfy §3.2's JWKS requirements and skips
// the rest: an issuer may publish keys for other purposes, and one unusable
// key must not disable the usable ones.
func parseJWKS(body []byte) (map[string]jwksKey, error) {
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parsing jwks: %w", err)
	}
	out := make(map[string]jwksKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kid == "" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		if _, dup := out[k.Kid]; dup {
			// A reused kid is exactly what §3.2 forbids; refusing both is the
			// only answer that cannot pick the wrong one.
			delete(out, k.Kid)
			continue
		}
		switch k.Kty {
		case "OKP":
			if k.Crv != "Ed25519" || (k.Alg != "" && k.Alg != "EdDSA") {
				continue
			}
			x, err := base64.RawURLEncoding.DecodeString(k.X)
			if err != nil || len(x) != ed25519.PublicKeySize {
				continue
			}
			out[k.Kid] = jwksKey{kid: k.Kid, alg: "EdDSA", ed: ed25519.PublicKey(x)}
		case "RSA":
			if k.Alg != "" && k.Alg != "RS256" {
				continue
			}
			pub, ok := parseRSAPublic(k.N, k.E)
			if !ok {
				continue
			}
			out[k.Kid] = jwksKey{kid: k.Kid, alg: "RS256", rsa: pub}
		}
	}
	return out, nil
}

func parseRSAPublic(nRaw, eRaw string) (*rsa.PublicKey, bool) {
	nb, err := base64.RawURLEncoding.DecodeString(nRaw)
	if err != nil || len(nb) == 0 {
		return nil, false
	}
	eb, err := base64.RawURLEncoding.DecodeString(eRaw)
	if err != nil || len(eb) == 0 || len(eb) > 4 {
		return nil, false
	}
	n := new(big.Int).SetBytes(nb)
	if n.BitLen() < minRSABits {
		return nil, false
	}
	e := int(new(big.Int).SetBytes(eb).Int64())
	// Odd and at least 3: the same sanity bounds crypto/x509 applies.
	if e < 3 || e%2 == 0 {
		return nil, false
	}
	return &rsa.PublicKey{N: n, E: e}, true
}

// delegatedVerifier resolves issuers per host and verifies tokens.
type delegatedVerifier struct {
	// issuers indexes by host, then by issuer string.
	issuers map[string]map[string]*DelegatedIssuer
	caches  map[string]*jwksCache // by JWKS URL: one issuer may serve many hosts
	now     func() time.Time
}

func newDelegatedVerifier(issuers []DelegatedIssuer, client *http.Client, now func() time.Time) (*delegatedVerifier, error) {
	if len(issuers) == 0 {
		return nil, errors.New("delegated: at least one issuer is required")
	}
	v := &delegatedVerifier{
		issuers: map[string]map[string]*DelegatedIssuer{},
		caches:  map[string]*jwksCache{},
		now:     now,
	}
	for i := range issuers {
		iss := issuers[i]
		if iss.Host == "" || iss.Issuer == "" || iss.JWKSURL == "" {
			return nil, errors.New("delegated: host, issuer and jwksUrl are all required")
		}
		if strings.ToLower(iss.Host) != iss.Host || strings.ContainsAny(iss.Host, ":/") {
			return nil, fmt.Errorf("delegated: host %q must be a bare lower-case hostname", iss.Host)
		}
		if !strings.HasPrefix(strings.ToLower(iss.JWKSURL), "https://") {
			return nil, fmt.Errorf("delegated: jwksUrl for %q must be https", iss.Issuer)
		}
		byIssuer := v.issuers[iss.Host]
		if byIssuer == nil {
			byIssuer = map[string]*DelegatedIssuer{}
			v.issuers[iss.Host] = byIssuer
		}
		if _, dup := byIssuer[iss.Issuer]; dup {
			return nil, fmt.Errorf("delegated: issuer %q configured twice for host %q", iss.Issuer, iss.Host)
		}
		byIssuer[iss.Issuer] = &iss
		if _, ok := v.caches[iss.JWKSURL]; !ok {
			v.caches[iss.JWKSURL] = newJWKSCache(iss.JWKSURL, client, now)
		}
	}
	return v, nil
}

// configuredFor reports whether any issuer serves host — the "does the feature
// exist here" question the 404 hinges on.
func (v *delegatedVerifier) configuredFor(host string) bool {
	return len(v.issuers[host]) > 0
}

// jwsHeader is the protected header, only the members the profile reads.
type jwsHeader struct {
	Alg  string          `json:"alg"`
	Kid  string          `json:"kid"`
	Typ  string          `json:"typ"`
	Crit json.RawMessage `json:"crit"`
}

// jwtPayload is the raw claim set. Numeric dates are json.Number so a float
// (RFC 7519 allows one) and an integer both parse; anything else is refused.
type jwtPayload struct {
	Iss     string          `json:"iss"`
	Aud     json.RawMessage `json:"aud"`
	Sub     string          `json:"sub"`
	Iat     json.Number     `json:"iat"`
	Exp     json.Number     `json:"exp"`
	Nbf     json.Number     `json:"nbf"`
	Jti     string          `json:"jti"`
	Purpose string          `json:"purpose"`
}

// verify checks a token presented at host for purpose. On success the claims
// are trustworthy; on failure the error is errDelegatedTokenInvalid (wrapped
// with the internal reason) or errDelegatedKeysUnavailable.
func (v *delegatedVerifier) verify(ctx context.Context, token, host, purpose string) (delegatedClaims, error) {
	if len(token) > maxDelegatedTokenBytes {
		return delegatedClaims{}, refuseToken("token too large")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return delegatedClaims{}, refuseToken("not a compact JWS")
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return delegatedClaims{}, refuseToken("header not base64url")
	}
	var hdr jwsHeader
	if err := json.Unmarshal(headerRaw, &hdr); err != nil {
		return delegatedClaims{}, refuseToken("header not JSON")
	}
	if hdr.Alg != "EdDSA" && hdr.Alg != "RS256" {
		return delegatedClaims{}, refuseToken("alg not accepted")
	}
	if hdr.Kid == "" {
		return delegatedClaims{}, refuseToken("kid missing")
	}
	if len(hdr.Crit) != 0 {
		// RFC 7515 §4.1.11: an implementation that does not understand a
		// critical extension MUST reject. This one understands none.
		return delegatedClaims{}, refuseToken("crit not supported")
	}
	if hdr.Typ != "" && !strings.EqualFold(hdr.Typ, "JWT") {
		return delegatedClaims{}, refuseToken("typ not JWT")
	}

	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return delegatedClaims{}, refuseToken("payload not base64url")
	}
	var pl jwtPayload
	if err := json.Unmarshal(payloadRaw, &pl); err != nil {
		return delegatedClaims{}, refuseToken("payload not JSON")
	}

	// Issuer resolution for THIS host selects the key set; nothing else from
	// the payload is used until the signature verifies.
	iss := v.issuers[host][pl.Iss]
	if iss == nil {
		return delegatedClaims{}, refuseToken("issuer not configured for host")
	}
	key, err := v.caches[iss.JWKSURL].lookup(ctx, hdr.Kid)
	if err != nil {
		return delegatedClaims{}, err
	}
	if key.alg != hdr.Alg {
		return delegatedClaims{}, refuseToken("alg does not match key")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return delegatedClaims{}, refuseToken("signature not base64url")
	}
	signed := []byte(parts[0] + "." + parts[1])
	switch key.alg {
	case "EdDSA":
		if !ed25519.Verify(key.ed, signed, sig) {
			return delegatedClaims{}, refuseToken("bad signature")
		}
	case "RS256":
		digest := sha256.Sum256(signed)
		if err := rsa.VerifyPKCS1v15(key.rsa, crypto.SHA256, digest[:], sig); err != nil {
			return delegatedClaims{}, refuseToken("bad signature")
		}
	}

	// From here the payload is authentic. Now the claim rules of §3.2.
	if !audienceContains(pl.Aud, host) {
		return delegatedClaims{}, refuseToken("aud does not name this host")
	}
	sub := strings.ToLower(strings.TrimSpace(pl.Sub))
	if sub == "" || !strings.Contains(sub, "@") {
		return delegatedClaims{}, refuseToken("sub missing")
	}
	iat, ok := numericDate(pl.Iat)
	if !ok {
		return delegatedClaims{}, refuseToken("iat missing")
	}
	exp, ok := numericDate(pl.Exp)
	if !ok {
		return delegatedClaims{}, refuseToken("exp missing")
	}
	now := v.now()
	if exp.Sub(iat) > delegatedMaxLifetime {
		return delegatedClaims{}, refuseToken("lifetime over 300 s")
	}
	if !exp.After(now.Add(-delegatedClockSkew)) {
		return delegatedClaims{}, refuseToken("expired")
	}
	if iat.After(now.Add(delegatedClockSkew)) {
		// An iat in the future would let exp sit arbitrarily far out while
		// still satisfying exp − iat ≤ 300: the lifetime rule is only meaningful
		// with iat anchored to the present.
		return delegatedClaims{}, refuseToken("iat in the future")
	}
	if pl.Nbf != "" {
		nbf, ok := numericDate(pl.Nbf)
		if !ok {
			return delegatedClaims{}, refuseToken("nbf malformed")
		}
		if nbf.After(now.Add(delegatedClockSkew)) {
			return delegatedClaims{}, refuseToken("not yet valid")
		}
	}
	if pl.Jti == "" || len(pl.Jti) > maxJTILength {
		return delegatedClaims{}, refuseToken("jti missing or oversized")
	}
	if pl.Purpose != purpose {
		return delegatedClaims{}, refuseToken("purpose does not match route")
	}

	return delegatedClaims{
		Issuer:   pl.Iss,
		Subject:  sub,
		JTI:      pl.Jti,
		Purpose:  pl.Purpose,
		Expires:  exp,
		IssuedAt: iat,
	}, nil
}

// audienceContains handles the string-or-array form of `aud` (RFC 7519
// §4.1.3). Comparison is exact: the host is a normalized lower-case name and
// the issuer is told to sign exactly that.
func audienceContains(raw json.RawMessage, host string) bool {
	if len(raw) == 0 {
		return false
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single == host
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return false
	}
	for _, a := range many {
		if a == host {
			return true
		}
	}
	return false
}

// numericDate parses a NumericDate; false when absent or malformed.
func numericDate(n json.Number) (time.Time, bool) {
	if n == "" {
		return time.Time{}, false
	}
	if i, err := n.Int64(); err == nil {
		return time.Unix(i, 0), true
	}
	f, err := n.Float64()
	if err != nil {
		return time.Time{}, false
	}
	sec := int64(f)
	return time.Unix(sec, int64((f-float64(sec))*1e9)), true
}
