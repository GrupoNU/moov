package jmaphttp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/branding"
	"github.com/GrupoNU/moov/internal/store"
)

// Delegated sign-in (epic M2; contract docs/specs/L2-accounts-api-contract.md
// §3, OpenAPI docs/specs/openapi-accounts-and-delegated.yaml).
//
// # What this adds to the authentication story
//
// Arbitration J-A1 made HTTP Basic the primary scheme: the password is
// validated by a real IMAP LOGIN against Dovecot, the source of truth. That is
// right for a user who HAS a password. An external system that owns a mailbox
// — a portal that created it through the accounts API for an event, a CRM
// for a shared inbox — has a user who does not: the portal authenticated them
// its own way and wants to open Moov for them without a second login. So the
// portal signs a short-lived JWT, the browser carries it in the URL FRAGMENT
// (never sent to any server), the PWA posts it to /auth/delegated/exchange,
// and this file turns it into an opaque Moov session token the PWA then sends
// as `Authorization: Bearer` everywhere Basic is accepted.
//
// The Bearer path resolves to the same Identity the Basic path does, through
// the same per-request store consultation (requireProvisionedByID), so
// everything downstream — the JMAP session object, method dispatch, push and
// blob tokens, uploads, the brand admin — is unchanged and unaware. That is
// the design's whole economy: one new credential type, zero new code paths
// below the authenticator.
//
// # Why an opaque session token and not the JWT itself
//
// The JWT is a five-minute, single-use capability signed by someone else. The
// session is Moov's own: random, stored hashed with the account it grants,
// checked against the store on every request (revocation is immediate, as a
// disabled account's password is), renewable up to an absolute lifetime,
// revocable by the user (logout), by the issuer (§3.6) and by the accounts
// API (suspend, delete). None of that is expressible with a token whose life
// is fixed by its signer. The precedent is token.go: mint our own artifact,
// register it, verify against the registry — here the registry is a table
// (migration 0013), because a session must survive a restart where a
// ten-minute push token need not.
//
// # What a refusal says
//
// A refused TOKEN is one 401 with one message ("invalid delegated token"),
// whatever failed: an attacker probing the exchange learns "no" and nothing
// about which issuer, key, audience or clock rule stopped them. The internal
// reason goes to a debug log line without the token. A refused ACCOUNT is a
// 403 with a machine-readable `code` (notProvisioned / suspended / disabled),
// because the PWA renders different screens for those and the fact is not
// secret: the token's signature already proved the caller is the issuer.
//
// A host with NO issuer configured answers the generic 404 on every
// /auth/delegated/* route, byte-identical to an unknown path: the feature
// does not exist there, and nothing should confirm otherwise.
//
// # Bearer tokens never ride a query string
//
// The token-in-query set (routes.go tokenScope) stays exactly the two scoped
// push/blob tokens token.go minted for the header-less contexts. A session
// token in `access_token` is refused, by construction: requireAuthOrToken
// hands anything without an Authorization header to the scoped verifier,
// which does not know the mds1_ format. TestSessionTokenRefusedInQuery pins
// it.

// Paths. All four are POST; all four are in the route table as `public` in
// the table's sense — they authenticate THEMSELVES (a JWT, or the session
// they act on) rather than through the Basic gate, and a host without the
// feature must answer 404 before any credential is looked at.
const (
	// PathDelegatedExchange turns an issuer-signed JWT into a session (§3.4).
	PathDelegatedExchange = "/auth/delegated/exchange"
	// PathDelegatedRenew issues a fresh session to a bearer (§3.5).
	PathDelegatedRenew = "/auth/delegated/renew"
	// PathDelegatedLogout ends the bearer's session; 204 always (§3.5).
	PathDelegatedLogout = "/auth/delegated/logout"
	// PathDelegatedRevoke ends every session of one subject created through
	// the signing issuer (§3.6).
	PathDelegatedRevoke = "/auth/delegated/revoke"
)

const (
	// sessionTokenPrefix versions the wire format, like token.go's "mt1".
	sessionTokenPrefix = "mds1_"
	// sessionTokenBytes is the random payload: 256 bits.
	sessionTokenBytes = 32
	// sessionTokenLength is the prefix plus 43 base64url characters.
	sessionTokenLength = len(sessionTokenPrefix) + 43

	// DefaultDelegatedSessionTTL is the sliding session lifetime (§3.4).
	DefaultDelegatedSessionTTL = 12 * time.Hour
	// DefaultDelegatedSessionMax is the absolute lifetime (§3.4,
	// MOOV_DELEGATED_SESSION_MAX).
	DefaultDelegatedSessionMax = 168 * time.Hour
	// delegatedRenewLead is how long before expiry the client is told to
	// renew: renewAfter = expiresAt − lead.
	delegatedRenewLead = time.Hour
	// delegatedRenewGrace is how long the OLD token stays valid after a
	// renewal, so requests in flight complete (§3.5).
	delegatedRenewGrace = 60 * time.Second

	// delegatedTouchInterval throttles last_seen writes per session.
	delegatedTouchInterval = time.Minute

	// delegatedExchangeRate is exchange/revoke calls per client IP per
	// minute (§3.4 "30 exchanges per minute per client IP").
	delegatedExchangeRate = 30
	// delegatedBearerFailureRate is refused bearer presentations per client
	// IP per minute. A stale token from a sleeping tab is routine and must
	// not lock anyone out; sixty a minute is far above that and far below
	// anything that could dent a 256-bit search space.
	delegatedBearerFailureRate = 60

	// maxDelegatedBody caps exchange/revoke bodies (OpenAPI TooLarge: 16 KiB).
	maxDelegatedBody = 16 * 1024

	// jwksUnavailableRetryAfter is the Retry-After on the 503.
	jwksUnavailableRetryAfter = 30
)

// The exchange outcomes DelegatedObserver counts. Constants, not free
// strings, and pinned against internal/metrics by test (cmd/moovd) because the
// agreement is not something the compiler checks.
const (
	// DelegatedExchangeOK is a session issued.
	DelegatedExchangeOK = "ok"
	// DelegatedExchangeInvalid is a refused token (the single 401).
	DelegatedExchangeInvalid = "invalid"
	// DelegatedExchangeAccount is a valid token for an account that cannot be
	// signed in (the 403s).
	DelegatedExchangeAccount = "account"
)

// DelegatedSessionStore is the persistence the session layer needs: the
// delegated_sessions and delegated_jti rows of migration 0013. *store.Store
// satisfies it; tests use an in-memory fake.
type DelegatedSessionStore interface {
	CreateDelegatedSession(ctx context.Context, s store.DelegatedSession) (store.DelegatedSession, error)
	GetDelegatedSession(ctx context.Context, tokenHash []byte) (store.DelegatedSession, error)
	TouchDelegatedSession(ctx context.Context, id int64, now time.Time) error
	SetDelegatedSessionExpiry(ctx context.Context, id int64, expiresAt time.Time) error
	RevokeDelegatedSession(ctx context.Context, id int64, now time.Time) error
	RevokeDelegatedSessionsByIssuer(ctx context.Context, accountID int64, issuer string, now time.Time) (int64, error)
	RevokeDelegatedSessionsForAccount(ctx context.Context, accountID int64, now time.Time) (int64, error)
	ConsumeDelegatedJTI(ctx context.Context, issuer, jti string, expiresAt, now time.Time) (bool, error)
}

// AccountStatus is what the Session response says about the account beyond
// its existence: the display name, and the two facts the accounts API (M1)
// adds to the store — read-only retention and suspension.
type AccountStatus struct {
	// Name is the display name (`account.name`).
	Name string
	// ReadOnly mirrors the retention phase (`readOnly`).
	ReadOnly bool
	// Suspended makes every delegated request a 403 `suspended`.
	Suspended bool
}

// AccountStatusSource is the seam through which M1's account facts reach
// this layer. nil in DelegatedConfig means: name = the address, read-only
// false, suspended false — the truth before M1 lands, and the truth on an
// installation that never uses the accounts API. Wiring M1 is one line in
// cmd/moovd/delegated.go.
type AccountStatusSource interface {
	AccountStatus(ctx context.Context, accountID int64) (AccountStatus, error)
}

// DelegatedObserver receives exchange outcomes (E8-lite). Declared here, like
// submit.Observer, so this package never imports the metrics implementation.
type DelegatedObserver interface {
	DelegatedExchange(result string)
}

// DelegatedConfig enables delegated sign-in on a Server. nil (the Config
// default) leaves every /auth/delegated/* route answering 404 and the Bearer
// scheme refused everywhere.
type DelegatedConfig struct {
	// Issuers is the MOOV_DELEGATED_ISSUERS list. Required, non-empty.
	Issuers []DelegatedIssuer
	// Sessions persists sessions and accepted jtis. Required.
	Sessions DelegatedSessionStore
	// Accounts supplies name / read-only / suspended. Optional.
	Accounts AccountStatusSource
	// Observer counts exchange outcomes. Optional.
	Observer DelegatedObserver
	// SessionTTL and SessionMax override the defaults. Zero means default.
	SessionTTL time.Duration
	SessionMax time.Duration
	// HTTPClient fetches JWKS documents. nil means a client with the fetch
	// timeout and default transport.
	HTTPClient *http.Client

	// now overrides the clock in tests.
	now func() time.Time
}

// delegatedAPI is the running feature: verifier, policy and limiters.
type delegatedAPI struct {
	verifier *delegatedVerifier
	sessions DelegatedSessionStore
	accounts AccountStatusSource
	observer DelegatedObserver
	ttl      time.Duration
	maxLife  time.Duration
	now      func() time.Time
	log      *slog.Logger

	// exchangeLimiter bounds exchange+revoke per client IP; bearerLimiter
	// bounds REFUSED bearer presentations per client IP.
	exchangeLimiter *ipRateLimiter
	bearerLimiter   *ipRateLimiter
}

func newDelegatedAPI(cfg *DelegatedConfig, log *slog.Logger) (*delegatedAPI, error) {
	if cfg.Sessions == nil {
		return nil, errors.New("delegated: a session store is required")
	}
	now := cfg.now
	if now == nil {
		now = time.Now
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: jwksFetchTimeout}
	}
	verifier, err := newDelegatedVerifier(cfg.Issuers, client, now)
	if err != nil {
		return nil, err
	}
	ttl := cfg.SessionTTL
	if ttl <= 0 {
		ttl = DefaultDelegatedSessionTTL
	}
	maxLife := cfg.SessionMax
	if maxLife <= 0 {
		maxLife = DefaultDelegatedSessionMax
	}
	if maxLife < ttl {
		return nil, fmt.Errorf("delegated: session max %s is shorter than the session TTL %s", maxLife, ttl)
	}
	accounts := cfg.Accounts
	if accounts == nil {
		accounts = defaultAccountStatus{}
	}
	return &delegatedAPI{
		verifier:        verifier,
		sessions:        cfg.Sessions,
		accounts:        accounts,
		observer:        cfg.Observer,
		ttl:             ttl,
		maxLife:         maxLife,
		now:             now,
		log:             log,
		exchangeLimiter: newIPRateLimiter(delegatedExchangeRate, time.Minute, now),
		bearerLimiter:   newIPRateLimiter(delegatedBearerFailureRate, time.Minute, now),
	}, nil
}

// defaultAccountStatus is the pre-M1 truth: the address is the name, and
// nothing is read-only or suspended.
type defaultAccountStatus struct{}

func (defaultAccountStatus) AccountStatus(context.Context, int64) (AccountStatus, error) {
	return AccountStatus{}, nil
}

func (d *delegatedAPI) observe(result string) {
	if d.observer != nil {
		d.observer.DelegatedExchange(result)
	}
}

// ---------------------------------------------------------------------------
// The session token
// ---------------------------------------------------------------------------

// mintSessionToken returns a fresh token and its hash.
func mintSessionToken() (token string, hash []byte, err error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("delegated: generating a session token: %w", err)
	}
	token = sessionTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return token, hashSessionToken(token), nil
}

// hashSessionToken is what the store holds: SHA-256 of the presented bytes.
// A hash rather than an HMAC because the token already carries 256 bits of
// entropy — there is nothing a keyed hash would add except a key to manage.
func hashSessionToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// looksLikeSessionToken is the cheap structural check before the store is
// consulted. It is not a security boundary (the hash lookup is); it keeps
// M1's msa1_ keys and arbitrary garbage from costing a query.
func looksLikeSessionToken(token string) bool {
	if len(token) != sessionTokenLength || !strings.HasPrefix(token, sessionTokenPrefix) {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(token[len(sessionTokenPrefix):])
	return err == nil
}

// issueSession creates a session for the account and builds the response.
func (d *delegatedAPI) issueSession(ctx context.Context, acct store.Account, issuer string, absolute time.Time) (string, store.DelegatedSession, error) {
	token, hash, err := mintSessionToken()
	if err != nil {
		return "", store.DelegatedSession{}, err
	}
	now := d.now()
	expires := now.Add(d.ttl)
	if expires.After(absolute) {
		// Near the end of the absolute lifetime a renewal yields a shorter
		// session rather than one that outlives the ceiling.
		expires = absolute
	}
	sess, err := d.sessions.CreateDelegatedSession(ctx, store.DelegatedSession{
		TokenHash:         hash,
		AccountID:         acct.ID,
		Issuer:            issuer,
		CreatedAt:         now,
		ExpiresAt:         expires,
		AbsoluteExpiresAt: absolute,
	})
	if err != nil {
		return "", store.DelegatedSession{}, err
	}
	return token, sess, nil
}

// sessionResponse is the OpenAPI `Session` schema, field for field.
type sessionResponse struct {
	TokenType         string          `json:"tokenType"`
	SessionToken      string          `json:"sessionToken"`
	ExpiresAt         string          `json:"expiresAt"`
	RenewAfter        string          `json:"renewAfter"`
	AbsoluteExpiresAt string          `json:"absoluteExpiresAt"`
	Account           sessionAccount  `json:"account"`
	ReadOnly          bool            `json:"readOnly"`
	JMAP              sessionJMAPLink `json:"jmap"`
}

type sessionAccount struct {
	Address string `json:"address"`
	Name    string `json:"name"`
}

type sessionJMAPLink struct {
	SessionURL string `json:"sessionUrl"`
}

// wireTime is the contract's timestamp form: UTC, millisecond precision, Z.
func wireTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func (d *delegatedAPI) writeSession(w http.ResponseWriter, token string, sess store.DelegatedSession, acct store.Account, status AccountStatus) {
	name := status.Name
	if name == "" {
		name = acct.Email
	}
	renewAfter := sess.ExpiresAt.Add(-delegatedRenewLead)
	if renewAfter.Before(sess.CreatedAt) {
		renewAfter = sess.CreatedAt
	}
	// A response carrying a credential must never be cached by anything.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, sessionResponse{
		TokenType:         "Bearer",
		SessionToken:      token,
		ExpiresAt:         wireTime(sess.ExpiresAt),
		RenewAfter:        wireTime(renewAfter),
		AbsoluteExpiresAt: wireTime(sess.AbsoluteExpiresAt),
		Account:           sessionAccount{Address: acct.Email, Name: name},
		ReadOnly:          status.ReadOnly,
		JMAP:              sessionJMAPLink{SessionURL: PathWellKnown},
	})
}

// ---------------------------------------------------------------------------
// Account resolution shared by the exchange and the bearer path
// ---------------------------------------------------------------------------

// writeAccountProblem is the OpenAPI `AccountProblem`: a problem document
// with the RFC 7807 extension member `code`.
func writeAccountProblem(w http.ResponseWriter, code, detail string) {
	body, err := json.Marshal(map[string]any{
		"type":   "about:blank",
		"status": http.StatusForbidden,
		"code":   code,
		"detail": detail,
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", problemContentType)
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write(body)
}

// admitAccount applies the account-state rules every delegated request
// shares: disabled in the store → `disabled`; suspended per the status source
// → `suspended`. Existence is the caller's question (the exchange answers
// `notProvisioned` by address, the bearer path answers by id). Returns the
// status for the response on success.
func (d *delegatedAPI) admitAccount(ctx context.Context, w http.ResponseWriter, acct store.Account) (AccountStatus, bool) {
	if acct.State == store.AccountDisabled {
		writeAccountProblem(w, "disabled", "this account is disabled")
		return AccountStatus{}, false
	}
	status, err := d.accounts.AccountStatus(ctx, acct.ID)
	if err != nil {
		d.log.Error("delegated: account status lookup failed", "account_id", acct.ID, "error", err)
		writeGenericProblem(w, http.StatusServiceUnavailable, "account lookup failed")
		return AccountStatus{}, false
	}
	if status.Suspended {
		writeAccountProblem(w, "suspended", "this account is suspended")
		return AccountStatus{}, false
	}
	return status, true
}

// ---------------------------------------------------------------------------
// The bearer path (Authorization: Bearer mds1_…)
// ---------------------------------------------------------------------------

// bearerToken extracts a Bearer credential; ok is false when the header
// carries another scheme or nothing.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}

// errSessionRefused is a presented session token that is not a live session:
// malformed, unknown, revoked, expired or past its absolute lifetime. The
// wrapped reason is for the debug log; the wire answer is one 401.
var errSessionRefused = errors.New("delegated session refused")

// resolveSession finds the live session behind a presented token. A refusal
// is errSessionRefused (or store.ErrNotFound); anything else is a store
// failure and must NOT be answered as a bad token.
func (d *delegatedAPI) resolveSession(ctx context.Context, token string) (store.DelegatedSession, error) {
	if !looksLikeSessionToken(token) {
		return store.DelegatedSession{}, fmt.Errorf("%w: not a session token", errSessionRefused)
	}
	sess, err := d.sessions.GetDelegatedSession(ctx, hashSessionToken(token))
	if err != nil {
		return store.DelegatedSession{}, err
	}
	now := d.now()
	switch {
	case sess.RevokedAt != nil:
		return store.DelegatedSession{}, fmt.Errorf("%w: revoked", errSessionRefused)
	case !sess.ExpiresAt.After(now):
		return store.DelegatedSession{}, fmt.Errorf("%w: expired", errSessionRefused)
	case !sess.AbsoluteExpiresAt.After(now):
		return store.DelegatedSession{}, fmt.Errorf("%w: past absolute lifetime", errSessionRefused)
	}
	return sess, nil
}

// authenticateBearer is the Bearer twin of Authenticator.Authenticate: it
// resolves a session token to an Identity, writing the error response on
// failure. Called by requireAuth for any request whose Authorization scheme is
// Bearer.
//
// Bad tokens never reach Dovecot — there is no LOGIN on this path at all —
// but they do count against a per-IP budget so the endpoint cannot be used
// to search the token space at line rate.
func (s *Server) authenticateBearer(w http.ResponseWriter, r *http.Request) (*Identity, bool) {
	d := s.delegated
	if d == nil {
		writeBearerChallenge(w, s.auth.realm)
		return nil, false
	}
	ip := clientIP(r)
	if ok, wait := d.bearerLimiter.peek(ip); !ok {
		writeTooMany(w, wait, "too many failed authentication attempts; retry later")
		return nil, false
	}
	token, _ := bearerToken(r)
	sess, err := d.resolveSession(r.Context(), token)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) && !errors.Is(err, errSessionRefused) {
			d.log.Error("delegated: session lookup failed", "error", err)
			writeGenericProblem(w, http.StatusServiceUnavailable, "session lookup failed")
			return nil, false
		}
		d.bearerLimiter.take(ip)
		d.log.Debug("delegated: bearer refused", "reason", err.Error(), "remote", ip)
		writeBearerChallenge(w, s.auth.realm)
		return nil, false
	}

	// The store is consulted on EVERY request, exactly as the Basic path
	// does: disabling, suspending or deleting the account takes effect on
	// the next request, not at the session's TTL.
	id, ok := s.auth.requireProvisionedByID(r.Context(), w, sess.AccountID)
	if !ok {
		return nil, false
	}
	if _, ok := d.admitAccount(r.Context(), w, id.Account); !ok {
		return nil, false
	}
	id.Delegated = &sess
	d.touch(r.Context(), sess)
	return id, true
}

// touch records use, at most once per delegatedTouchInterval per session.
// Best-effort: its failure is logged and never fails the request.
func (d *delegatedAPI) touch(ctx context.Context, sess store.DelegatedSession) {
	now := d.now()
	if sess.LastSeenAt != nil && now.Sub(*sess.LastSeenAt) < delegatedTouchInterval {
		return
	}
	if err := d.sessions.TouchDelegatedSession(ctx, sess.ID, now); err != nil {
		d.log.Warn("delegated: recording session use failed", "session_id", sess.ID, "error", err)
	}
}

// writeBearerChallenge is the 401 for a refused bearer (RFC 6750 §3). The
// scheme in the challenge is Bearer, not Basic: a browser shows its native
// credential dialog only for Basic, and a delegated user has no password to
// type into one.
func writeBearerChallenge(w http.ResponseWriter, realm string) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf("Bearer realm=%q", realm))
	writeGenericProblem(w, http.StatusUnauthorized, "invalid session")
}

// writeTooMany is the 429 with Retry-After and the OpenAPI `Reason` body.
func writeTooMany(w http.ResponseWriter, wait time.Duration, reason string) {
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", fmt.Sprintf("%d", secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]string{
		"reason": fmt.Sprintf("%s; try again in %d s", reason, secs),
	})
}

// RevokeDelegatedSessions ends every delegated session of an account and the
// push/blob tokens it minted — the seam M1's accounts.SessionRevoker wires
// for suspend and delete (contract §2.4). Safe to call when delegated sign-in
// is not configured: there is then nothing to revoke but the tokens.
func (s *Server) RevokeDelegatedSessions(ctx context.Context, accountID int64) error {
	s.tokens.RevokeAccount(accountID)
	if s.delegated == nil {
		return nil
	}
	_, err := s.delegated.sessions.RevokeDelegatedSessionsForAccount(ctx, accountID, s.delegated.now())
	return err
}

// ---------------------------------------------------------------------------
// The four routes
// ---------------------------------------------------------------------------

// delegatedHost resolves the host the browser reached, or "" when it cannot
// be a configured host (a port is stripped; anything unparseable is nothing).
func delegatedHost(r *http.Request) string {
	return branding.NormalizeHost(r.Host)
}

// enabledFor answers the 404 question for a request: the feature exists for
// this host, or the generic not-found is written and false returned.
func (s *Server) delegatedEnabledFor(w http.ResponseWriter, r *http.Request) (*delegatedAPI, string, bool) {
	host := delegatedHost(r)
	if s.delegated == nil || host == "" || !s.delegated.verifier.configuredFor(host) {
		writeGenericProblem(w, http.StatusNotFound, "not found")
		return nil, "", false
	}
	return s.delegated, host, true
}

// exchangeRequest is the OpenAPI `ExchangeRequest`.
type exchangeRequest struct {
	Token *string `json:"token"`
}

// readTokenBody reads and validates an exchange/revoke body, writing the
// 415/413/400 answers the OpenAPI document specifies.
func readTokenBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{
			"reason": "Content-Type must be application/json",
		})
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDelegatedBody+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"field": "", "reason": "unreadable request body"})
		return "", false
	}
	if len(body) > maxDelegatedBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"reason": "the body is too large"})
		return "", false
	}
	var req exchangeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"field": "", "reason": "the body is not a JSON object"})
		return "", false
	}
	if req.Token == nil || *req.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"field": "token", "reason": "required"})
		return "", false
	}
	return *req.Token, true
}

// verifyPresented runs the shared front half of exchange and revoke: the
// per-IP budget, the body, the token, and the jti. On success the claims are
// verified and the jti consumed.
func (s *Server) verifyPresented(w http.ResponseWriter, r *http.Request, purpose string) (*delegatedAPI, delegatedClaims, bool) {
	d, host, ok := s.delegatedEnabledFor(w, r)
	if !ok {
		return nil, delegatedClaims{}, false
	}
	ip := clientIP(r)
	if ok, wait := d.exchangeLimiter.take(ip); !ok {
		writeTooMany(w, wait, "too many requests")
		return nil, delegatedClaims{}, false
	}
	token, ok := readTokenBody(w, r)
	if !ok {
		return nil, delegatedClaims{}, false
	}
	claims, err := d.verifier.verify(r.Context(), token, host, purpose)
	if err != nil {
		if errors.Is(err, errDelegatedKeysUnavailable) {
			d.log.Warn("delegated: issuer keys unavailable", "host", host, "error", err.Error())
			w.Header().Set("Retry-After", fmt.Sprintf("%d", jwksUnavailableRetryAfter))
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"reason": "the issuer's signing keys are temporarily unavailable",
			})
			return nil, delegatedClaims{}, false
		}
		d.refuseTokenResponse(w, host, ip, err)
		return nil, delegatedClaims{}, false
	}
	fresh, err := d.sessions.ConsumeDelegatedJTI(r.Context(), claims.Issuer, claims.JTI, claims.Expires, d.now())
	if err != nil {
		d.log.Error("delegated: jti cache failed", "error", err)
		writeGenericProblem(w, http.StatusServiceUnavailable, "replay cache unavailable")
		return nil, delegatedClaims{}, false
	}
	if !fresh {
		d.refuseTokenResponse(w, host, ip, refuseToken("jti replayed"))
		return nil, delegatedClaims{}, false
	}
	return d, claims, true
}

// refuseTokenResponse is THE 401 for a refused token: one message, and the
// reason only in a debug log that never sees the token.
func (d *delegatedAPI) refuseTokenResponse(w http.ResponseWriter, host, ip string, err error) {
	d.observe(DelegatedExchangeInvalid)
	d.log.Debug("delegated: token refused", "host", host, "remote", ip, "reason", err.Error())
	writeGenericProblem(w, http.StatusUnauthorized, "invalid delegated token")
}

// handleDelegatedExchange serves POST /auth/delegated/exchange (§3.4).
func (s *Server) handleDelegatedExchange(w http.ResponseWriter, r *http.Request) {
	d, claims, ok := s.verifyPresented(w, r, purposeLogin)
	if !ok {
		return
	}
	ctx := r.Context()
	acct, err := s.auth.directory.GetAccountByEmail(ctx, claims.Subject)
	switch {
	case errors.Is(err, store.ErrNotFound):
		d.observe(DelegatedExchangeAccount)
		writeAccountProblem(w, "notProvisioned", "this mailbox is not provisioned in Moov")
		return
	case err != nil:
		d.log.Error("delegated: account lookup failed", "error", err)
		writeGenericProblem(w, http.StatusServiceUnavailable, "account lookup failed")
		return
	}
	status, ok := d.admitAccount(ctx, w, acct)
	if !ok {
		d.observe(DelegatedExchangeAccount)
		return
	}
	token, sess, err := d.issueSession(ctx, acct, claims.Issuer, d.now().Add(d.maxLife))
	if err != nil {
		d.log.Error("delegated: issuing a session failed", "account_id", acct.ID, "error", err)
		writeGenericProblem(w, http.StatusServiceUnavailable, "session could not be created")
		return
	}
	d.observe(DelegatedExchangeOK)
	d.log.Info("delegated: session issued", "account_id", acct.ID, "issuer", claims.Issuer, "session_id", sess.ID)
	d.writeSession(w, token, sess, acct, status)
}

// handleDelegatedRenew serves POST /auth/delegated/renew (§3.5). The route
// is self-authenticating: a bearer, and only a bearer — a Basic login has no
// session to renew and answers the same 401 an invalid bearer does.
func (s *Server) handleDelegatedRenew(w http.ResponseWriter, r *http.Request) {
	d, _, ok := s.delegatedEnabledFor(w, r)
	if !ok {
		return
	}
	if _, isBearer := bearerToken(r); !isBearer {
		writeBearerChallenge(w, s.auth.realm)
		return
	}
	id, ok := s.authenticateBearer(w, r)
	if !ok {
		return
	}
	old := id.Delegated
	ctx := r.Context()
	status, err := d.accounts.AccountStatus(ctx, id.Account.ID)
	if err != nil {
		d.log.Error("delegated: account status lookup failed", "account_id", id.Account.ID, "error", err)
		writeGenericProblem(w, http.StatusServiceUnavailable, "account lookup failed")
		return
	}
	token, sess, err := d.issueSession(ctx, id.Account, old.Issuer, old.AbsoluteExpiresAt)
	if err != nil {
		d.log.Error("delegated: renewing a session failed", "account_id", id.Account.ID, "error", err)
		writeGenericProblem(w, http.StatusServiceUnavailable, "session could not be renewed")
		return
	}
	// The old token keeps working for the grace window; a shorter remaining
	// life is left alone.
	grace := d.now().Add(delegatedRenewGrace)
	if grace.Before(old.ExpiresAt) {
		if err := d.sessions.SetDelegatedSessionExpiry(ctx, old.ID, grace); err != nil {
			d.log.Warn("delegated: shortening the renewed session failed", "session_id", old.ID, "error", err)
		}
	}
	d.writeSession(w, token, sess, id.Account, status)
}

// handleDelegatedLogout serves POST /auth/delegated/logout (§3.5): 204
// always. A token that resolves is revoked along with the account's push/blob
// tokens; one that does not resolve gets the same 204, because a sign-out
// must never fail on the user and "was it alive" tells a caller nothing.
func (s *Server) handleDelegatedLogout(w http.ResponseWriter, r *http.Request) {
	d, _, ok := s.delegatedEnabledFor(w, r)
	if !ok {
		return
	}
	if token, isBearer := bearerToken(r); isBearer {
		if sess, err := d.resolveSession(r.Context(), token); err == nil {
			if err := d.sessions.RevokeDelegatedSession(r.Context(), sess.ID, d.now()); err != nil {
				d.log.Warn("delegated: logout revoke failed", "session_id", sess.ID, "error", err)
			}
			s.tokens.RevokeAccount(sess.AccountID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDelegatedRevoke serves POST /auth/delegated/revoke (§3.6).
func (s *Server) handleDelegatedRevoke(w http.ResponseWriter, r *http.Request) {
	d, claims, ok := s.verifyPresented(w, r, purposeRevoke)
	if !ok {
		return
	}
	ctx := r.Context()
	acct, err := s.auth.directory.GetAccountByEmail(ctx, claims.Subject)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Nothing to revoke. Idempotent, and no oracle about provisioning:
		// the answer is the count, which is honestly zero.
		writeJSON(w, http.StatusOK, map[string]int64{"revoked": 0})
		return
	case err != nil:
		d.log.Error("delegated: account lookup failed", "error", err)
		writeGenericProblem(w, http.StatusServiceUnavailable, "account lookup failed")
		return
	}
	n, err := d.sessions.RevokeDelegatedSessionsByIssuer(ctx, acct.ID, claims.Issuer, d.now())
	if err != nil {
		d.log.Error("delegated: issuer revoke failed", "account_id", acct.ID, "error", err)
		writeGenericProblem(w, http.StatusServiceUnavailable, "sessions could not be revoked")
		return
	}
	if n > 0 {
		s.tokens.RevokeAccount(acct.ID)
	}
	d.log.Info("delegated: sessions revoked by issuer", "account_id", acct.ID, "issuer", claims.Issuer, "count", n)
	writeJSON(w, http.StatusOK, map[string]int64{"revoked": n})
}

// ---------------------------------------------------------------------------
// Per-IP token buckets
// ---------------------------------------------------------------------------

// ipRateLimiter is a token bucket per key (a client IP), `rate` tokens per
// `window`, refilling continuously. It is the shape lockoutTable's global
// budget has, applied per client. Behind the same-origin proxy every client
// shares one IP, which — as lockout.go notes for its own table — only makes
// the limit stricter, never looser.
type ipRateLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	capacity float64
	perSec   float64
	entries  map[string]*bucketEntry
}

type bucketEntry struct {
	tokens float64
	last   time.Time
}

func newIPRateLimiter(rate int, window time.Duration, now func() time.Time) *ipRateLimiter {
	return &ipRateLimiter{
		now:      now,
		capacity: float64(rate),
		perSec:   float64(rate) / window.Seconds(),
		entries:  make(map[string]*bucketEntry),
	}
}

// take spends one token for key. When none is left it reports how long until
// the next one.
func (l *ipRateLimiter) take(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.refill(key)
	if e.tokens >= 1 {
		e.tokens--
		return true, 0
	}
	return false, l.wait(e)
}

// peek reports whether a token is available WITHOUT spending it — for the
// bearer path, which charges failures only.
func (l *ipRateLimiter) peek(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.refill(key)
	if e.tokens >= 1 {
		return true, 0
	}
	return false, l.wait(e)
}

func (l *ipRateLimiter) wait(e *bucketEntry) time.Duration {
	missing := 1 - e.tokens
	return time.Duration(math.Ceil(missing/l.perSec)) * time.Second
}

// refill tops up key's bucket for the time elapsed. Callers hold mu.
func (l *ipRateLimiter) refill(key string) *bucketEntry {
	now := l.now()
	if len(l.entries) >= pruneThreshold {
		for k, e := range l.entries {
			if now.Sub(e.last) > pruneIdle {
				delete(l.entries, k)
			}
		}
	}
	e, ok := l.entries[key]
	if !ok {
		e = &bucketEntry{tokens: l.capacity, last: now}
		l.entries[key] = e
		return e
	}
	if elapsed := now.Sub(e.last).Seconds(); elapsed > 0 {
		e.tokens = math.Min(l.capacity, e.tokens+elapsed*l.perSec)
		e.last = now
	}
	return e
}
