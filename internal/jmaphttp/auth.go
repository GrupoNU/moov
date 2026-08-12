package jmaphttp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/store"
)

// Authentication per arbitration J-A1 (L2 §2.2): HTTP Basic, where the
// password is validated against Dovecot — the source of truth for auth
// (ADR §4) — by a real IMAP LOGIN. Positive results are cached (authcache.go)
// so not every request pays a LOGIN; failures are rate-limited (lockout.go)
// so Moov never becomes the IP Mailcow's fail2ban bans.
//
// Only provisioned accounts are served: a login Dovecot accepts for an
// account moovctl has not added answers 403 with a clear message. The server
// NEVER auto-provisions — provisioning creates state (app passwords, sync
// workers) that must remain an explicit operator action.

// CredentialValidator validates a username/password pair against the
// authentication authority.
//
// The contract separates the two failure modes an HTTP layer must never
// conflate: (false, nil) means the authority REJECTED the credentials → 401
// and a lockout strike; a non-nil error means the authority was UNREACHABLE
// or misbehaved → 503, no strike, because punishing users for an outage
// would lock everyone out exactly when the system is already hurting.
type CredentialValidator interface {
	Validate(ctx context.Context, username, password string) (bool, error)
}

// AccountDirectory is the slice of the store the auth layer needs:
// "is this authenticated user provisioned, and in what state". Satisfied by
// *store.Store.
type AccountDirectory interface {
	GetAccountByEmail(ctx context.Context, email string) (store.Account, error)
}

// Identity is an authenticated, provisioned caller.
type Identity struct {
	// Account is the provisioned store account.
	Account store.Account

	// AccountID is the account's JMAP id (jmap.EncodeAccountID of the row id).
	AccountID string
}

// AuthConfig configures NewAuthenticator. Validator and Directory are
// required; every other field has a production default.
type AuthConfig struct {
	Validator CredentialValidator
	Directory AccountDirectory

	// CacheTTL is how long a positive validation is remembered. Default 10
	// minutes (J-A1).
	CacheTTL time.Duration

	// LockoutBase is the lockout after the first failure for an IP+account
	// pair; each further failure doubles it up to LockoutMax. Defaults 5s /
	// 30m. With these values one pair can produce at most 7 upstream login
	// failures in any 10-minute window — under Mailcow netfilter's default
	// 10-failure ban threshold on its own, before the global budget below.
	LockoutBase time.Duration
	LockoutMax  time.Duration

	// GlobalFailureBudget / GlobalFailureWindow bound upstream login failures
	// ACROSS all clients: at most Budget failures reach Dovecot per Window,
	// refilling continuously. Defaults 20 per 10 minutes.
	GlobalFailureBudget int
	GlobalFailureWindow time.Duration

	// Realm is the Basic realm announced in WWW-Authenticate.
	Realm string

	// Logger receives auth events. Never logs passwords; usernames at most.
	Logger *slog.Logger

	// now overrides the clock in tests.
	now func() time.Time
}

// Authenticator implements the J-A1 flow. Safe for concurrent use.
type Authenticator struct {
	validator CredentialValidator
	directory AccountDirectory
	cache     *credentialCache
	lockout   *lockoutTable
	realm     string
	log       *slog.Logger

	// inflight coalesces concurrent validations of the SAME credential pair
	// into one upstream LOGIN: a burst of parallel requests from one client
	// (a webmail opening ten fetches at once) must cost Dovecot one LOGIN,
	// not ten.
	inflightMu sync.Mutex
	inflight   map[string]*inflightAuth
}

type inflightAuth struct {
	done  chan struct{}
	valid bool
	err   error
}

// Defaults for AuthConfig.
const (
	DefaultAuthCacheTTL        = 10 * time.Minute
	DefaultLockoutBase         = 5 * time.Second
	DefaultLockoutMax          = 30 * time.Minute
	DefaultGlobalFailureBudget = 20
	DefaultGlobalFailureWindow = 10 * time.Minute
	DefaultRealm               = "Moov JMAP"
)

// validateTimeout bounds one upstream LOGIN validation, independently of the
// request's own deadline.
const validateTimeout = 15 * time.Second

// errBudgetExhausted is the internal signal that the global failure budget
// refused an attempt before it reached Dovecot.
var errBudgetExhausted = errors.New("jmaphttp: global auth failure budget exhausted")

// NewAuthenticator builds an Authenticator.
func NewAuthenticator(cfg AuthConfig) (*Authenticator, error) {
	if cfg.Validator == nil || cfg.Directory == nil {
		return nil, errors.New("jmaphttp: AuthConfig requires Validator and Directory")
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = DefaultAuthCacheTTL
	}
	if cfg.LockoutBase <= 0 {
		cfg.LockoutBase = DefaultLockoutBase
	}
	if cfg.LockoutMax <= 0 {
		cfg.LockoutMax = DefaultLockoutMax
	}
	if cfg.GlobalFailureBudget <= 0 {
		cfg.GlobalFailureBudget = DefaultGlobalFailureBudget
	}
	if cfg.GlobalFailureWindow <= 0 {
		cfg.GlobalFailureWindow = DefaultGlobalFailureWindow
	}
	if cfg.Realm == "" {
		cfg.Realm = DefaultRealm
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}

	cache, err := newCredentialCache(cfg.CacheTTL, cfg.now)
	if err != nil {
		return nil, err
	}
	return &Authenticator{
		validator: cfg.Validator,
		directory: cfg.Directory,
		cache:     cache,
		lockout: newLockoutTable(cfg.LockoutBase, cfg.LockoutMax,
			cfg.GlobalFailureBudget, cfg.GlobalFailureWindow, cfg.now),
		realm:    cfg.Realm,
		log:      cfg.Logger,
		inflight: make(map[string]*inflightAuth),
	}, nil
}

// InvalidateUser drops the user's cached credentials — the J-A1 invalidation
// hook. Call it when credentials are revoked upstream or the account is
// disabled; the very next request re-validates against Dovecot.
func (a *Authenticator) InvalidateUser(email string) {
	a.cache.invalidateUser(strings.ToLower(email))
}

// InvalidateAll drops every cached credential.
func (a *Authenticator) InvalidateAll() {
	a.cache.invalidateAll()
}

// Authenticate runs the full J-A1 flow for a request. On failure it writes
// the error response and returns ok=false.
func (a *Authenticator) Authenticate(w http.ResponseWriter, r *http.Request) (*Identity, bool) {
	user, pass, ok := r.BasicAuth()
	if !ok || user == "" {
		a.challenge(w, "authentication required")
		return nil, false
	}
	// Email local parts are case-sensitive in theory and case-insensitive in
	// every real deployment (Dovecot/Mailcow included); the lowercased form
	// is the cache/lockout/lookup key so "User@x" and "user@x" are one
	// principal, while the LOGIN itself forwards the bytes as received.
	userKey := strings.ToLower(user)

	if a.cache.check(userKey, pass) {
		return a.requireProvisioned(r.Context(), w, userKey)
	}

	lockKey := clientIP(r) + "|" + userKey
	if wait := a.lockout.lockedFor(lockKey); wait > 0 {
		a.tooMany(w, wait, "too many failed attempts for this account; retry later")
		return nil, false
	}

	valid, err := a.validateCoalesced(r.Context(), userKey, user, pass)
	switch {
	case errors.Is(err, errBudgetExhausted):
		a.log.Warn("jmaphttp: auth refused by global failure budget", "user", userKey)
		a.tooMany(w, 30*time.Second, "authentication is temporarily rate-limited; retry later")
		return nil, false
	case err != nil:
		a.log.Error("jmaphttp: credential validation unavailable", "user", userKey, "error", err)
		writeGenericProblem(w, http.StatusServiceUnavailable,
			"the authentication backend is temporarily unavailable")
		return nil, false
	case !valid:
		lock := a.lockout.recordFailure(lockKey)
		a.log.Info("jmaphttp: authentication failed", "user", userKey, "lockout", lock.String())
		a.challenge(w, "invalid credentials")
		return nil, false
	}

	a.lockout.recordSuccess(lockKey)
	a.cache.put(userKey, pass)
	return a.requireProvisioned(r.Context(), w, userKey)
}

// requireProvisioned maps an authenticated user onto a provisioned, servable
// account, or writes the J-A1 403.
//
// The store lookup runs on EVERY request — the credential cache deliberately
// does not cache provisioning: disabling an account or deleting it must take
// effect on the next request, not after a TTL.
func (a *Authenticator) requireProvisioned(ctx context.Context, w http.ResponseWriter, userKey string) (*Identity, bool) {
	acct, err := a.directory.GetAccountByEmail(ctx, userKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// J-A1: valid Dovecot login, no Moov account → a clear 403, never
		// auto-provisioning.
		writeGenericProblem(w, http.StatusForbidden,
			"this mailbox authenticated correctly but is not provisioned in Moov; "+
				"an administrator must add it with `moovctl account add` first")
		return nil, false
	case err != nil:
		a.log.Error("jmaphttp: account lookup failed", "user", userKey, "error", err)
		writeGenericProblem(w, http.StatusServiceUnavailable, "account lookup failed")
		return nil, false
	}

	if acct.State == store.AccountDisabled {
		writeGenericProblem(w, http.StatusForbidden,
			"this account is disabled in Moov")
		return nil, false
	}

	return &Identity{Account: acct, AccountID: jmap.EncodeAccountID(acct.ID)}, true
}

// validateCoalesced validates one credential pair upstream, deduplicating
// concurrent identical attempts.
func (a *Authenticator) validateCoalesced(ctx context.Context, userKey, user, pass string) (bool, error) {
	key := a.cache.macHex(userKey, pass)

	a.inflightMu.Lock()
	if call, exists := a.inflight[key]; exists {
		a.inflightMu.Unlock()
		select {
		case <-call.done:
			return call.valid, call.err
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	call := &inflightAuth{done: make(chan struct{})}
	a.inflight[key] = call
	a.inflightMu.Unlock()

	call.valid, call.err = a.validateUpstream(ctx, user, pass)

	a.inflightMu.Lock()
	delete(a.inflight, key)
	a.inflightMu.Unlock()
	close(call.done)

	return call.valid, call.err
}

// validateUpstream is the single path to the validator, with the global
// failure budget checked before Dovecot is touched and charged after a
// genuine rejection.
func (a *Authenticator) validateUpstream(ctx context.Context, user, pass string) (bool, error) {
	if !a.lockout.budgetAvailable() {
		return false, errBudgetExhausted
	}

	vctx, cancel := context.WithTimeout(ctx, validateTimeout)
	defer cancel()

	valid, err := a.validator.Validate(vctx, user, pass)
	if err == nil && !valid {
		a.lockout.consumeBudget()
	}
	return valid, err
}

// challenge writes the 401 with the Basic challenge (RFC 7617; charset per
// its §2.1 so clients send UTF-8 credentials).
func (a *Authenticator) challenge(w http.ResponseWriter, detail string) {
	w.Header().Set("WWW-Authenticate",
		fmt.Sprintf("Basic realm=%q, charset=\"UTF-8\"", a.realm))
	writeGenericProblem(w, http.StatusUnauthorized, detail)
}

// tooMany writes the 429 with a Retry-After.
func (a *Authenticator) tooMany(w http.ResponseWriter, wait time.Duration, detail string) {
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", fmt.Sprintf("%d", secs))
	writeGenericProblem(w, http.StatusTooManyRequests, detail)
}

// clientIP extracts the peer IP for the lockout key. Deliberately the TCP
// peer, never X-Forwarded-For: a spoofable header would let an attacker
// rotate lockout keys at will. Behind the same-origin proxy this collapses
// to the proxy's IP, which only makes the limiter stricter (see lockout.go).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
