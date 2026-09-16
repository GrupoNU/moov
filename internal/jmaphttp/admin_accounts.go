package jmaphttp

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/accounts"
)

// The per-domain accounts API of epic M1
// (docs/specs/L2-accounts-api-contract.md §2): the routes an external system
// calls with a service-account key to manage ONE domain's mailboxes.
//
// This file is the TRANSPORT only. The state machine, the field rules and the
// ordering of the Mailcow and Moov writes live in internal/accounts; here
// there is nothing but statuses, bodies and the two rules that are the wire's
// own.
//
// # Rule 1: the no-oracle 404
//
// Everything the caller is not entitled to see answers the same 404 with the
// same body, byte for byte, as a route that does not exist (§2.1): the
// feature being off, a missing header, a malformed, revoked or under-scoped
// key, an address outside the key's domain, an account that does not exist.
// A consumer therefore cannot use this API to learn which domains or
// mailboxes an installation has. It is the same policy the brand-admin API
// applies to a non-admin, and it is enforced HERE rather than per handler:
// serviceRoute below turns every accounts.ErrNotFound into that one body.
//
// # Rule 2: this is a SECOND authentication class
//
// A service-account key is not a mailbox credential and must never be
// interchangeable with one. It therefore gets its own wrapper rather than a
// branch inside requireAuth: the route table carries a serviceScope field,
// Handler dispatches on it, and the pinned route-set tests grew a third set
// so that a route joining this class - or a mailbox route accidentally
// acquiring it - fails a test rather than shipping. In the other direction, a
// session token presented here resolves to no service account and gets the
// generic 404, which is exactly what §4 promises ("the accounts API ignores
// session tokens").

// The accounts-API routes.
const (
	// PathAdminAccounts is the collection: POST creates (idempotent by
	// address).
	PathAdminAccounts = "/admin/accounts"

	// PathAdminAccount is one account: GET, PATCH, DELETE.
	//
	// The {address} variable holds an email address, which contains an "@"
	// and dots. Go 1.22's pattern matching takes a single path segment for a
	// wildcard, which is exactly right here: an address has no "/" once it
	// has been percent-decoded, and a caller that sends one is not naming an
	// address this server can own.
	PathAdminAccount = "/admin/accounts/{address}"

	// The transitions of §2.4.
	PathAdminAccountSuspend  = "/admin/accounts/{address}/suspend"
	PathAdminAccountResume   = "/admin/accounts/{address}/resume"
	PathAdminAccountReadOnly = "/admin/accounts/{address}/readonly"

	// PathAdminAccountExport starts (POST) and reports (GET) the export job.
	PathAdminAccountExport = "/admin/accounts/{address}/export"

	// PathAdminExportDownload serves a ready export. PUBLIC in the route
	// table's sense and authorized by the signature in its query string -
	// see the comment on the route row and on the public-set pin.
	PathAdminExportDownload = "/admin/exports/{exportId}"
)

// Limits of the accounts API.
const (
	// maxAccountsBodyBytes is §2.2's 16 KiB cap.
	maxAccountsBodyBytes = 16 << 10

	// accountsRequestsPerMinute and accountsBurst are §2.2's budget: 120
	// requests per minute per key, burst 30.
	accountsRequestsPerMinute = 120
	accountsBurst             = 30

	// maxRequestIDLen is §2.2's cap on an echoed X-Request-Id.
	maxRequestIDLen = 64
)

// headerRequestID is the header §2.2 echoes so a consumer's log and Moov's
// audit can be joined.
const headerRequestID = "X-Request-Id"

// accountsAPI is the server-side state of the routes.
type accountsAPI struct {
	// enabled is false when the operator did not turn the feature on
	// (MOOV_ACCOUNTS_API) or configured no Mailcow write key. Every route
	// then answers the generic 404 - not a 501, which would tell a prober
	// that the feature exists and is merely off.
	enabled bool

	svc     *accounts.Service
	auth    *accounts.Authenticator
	exports *accounts.ExportRunner

	// limiter budgets requests per KEY, not per IP: the budget belongs to the
	// credential, so one consumer behind a NAT cannot spend another's.
	limiter *rateLimiter
}

// serviceCall is what a handler receives once the caller is known.
type serviceCall struct {
	actor     accounts.Actor
	requestID string
}

// newAccountsAPI builds the routes' state. A nil config — or one missing
// either half of the credential story — means the feature is off, which is a
// complete and supported configuration and the default one.
func newAccountsAPI(cfg *AccountsAPIConfig) *accountsAPI {
	a := &accountsAPI{}
	if cfg != nil {
		a.enabled = cfg.Service != nil && cfg.Auth != nil
		a.svc, a.auth, a.exports = cfg.Service, cfg.Auth, cfg.Exports
		a.limiter = newRateLimiter(accountsBurst, accountsRequestsPerMinute, time.Minute, cfg.Now)
		return a
	}
	// The limiter exists even when the feature does not, so serviceRoute has
	// no nil branch to get wrong; it is simply never reached.
	a.limiter = newRateLimiter(accountsBurst, accountsRequestsPerMinute, time.Minute, nil)
	return a
}

// serviceRoute wraps a handler in the service-account gate: the feature
// check, the credential, the scope, the budget and the request id.
//
// It is the ONLY place a service-account key is accepted, and the only place
// that renders the generic 404, so the no-oracle rule cannot be forgotten by
// a handler.
func (s *Server) serviceRoute(scope string, next func(http.ResponseWriter, *http.Request, serviceCall)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := s.accountsAPI
		// The request id is resolved first so that even a refusal carries
		// one: a consumer chasing a 404 needs the id to find Moov's line.
		requestID := resolveRequestID(r)
		w.Header().Set(headerRequestID, requestID)

		if a == nil || !a.enabled {
			writeNotFound(w)
			return
		}
		actor, err := a.auth.Authenticate(r.Context(), accounts.BearerToken(r.Header.Get("Authorization")), scope)
		if err != nil {
			if !errors.Is(err, accounts.ErrNotFound) {
				// A store failure is not a caller problem, but it must not
				// become an oracle either: the caller still gets the generic
				// 404 and the cause goes to the log.
				s.log.Error("accounts api: resolving a service-account key failed", "error", err)
			}
			writeNotFound(w)
			return
		}
		if wait, ok := a.limiter.allow(actor.ID); !ok {
			secs := int(math.Ceil(wait.Seconds()))
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeJSON(w, http.StatusTooManyRequests, reasonBody{
				Reason: "too many requests; try again in " + strconv.Itoa(secs) + " s",
			})
			return
		}
		next(w, r, serviceCall{actor: actor, requestID: requestID})
	}
}

// writeNotFound renders §2.1's body. Every "no" on this API goes through it,
// which is what makes them byte-identical.
func writeNotFound(w http.ResponseWriter) {
	writeGenericProblem(w, http.StatusNotFound, "not found")
}

// resolveRequestID echoes the caller's id when it is well formed and mints
// one otherwise (§2.2).
//
// A malformed id is REPLACED rather than refused: the header is a correlation
// aid, and failing a mailbox creation over a stray character in a log field
// would be the wrong trade. The validation exists because the value is echoed
// into a response header and written to the audit row, so it must not be able
// to carry a newline or arbitrary length.
func resolveRequestID(r *http.Request) string {
	given := strings.TrimSpace(r.Header.Get(headerRequestID))
	if given != "" && len(given) <= maxRequestIDLen && validRequestID(given) {
		return given
	}
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		// Losing the id is not a reason to fail the request; the audit line
		// then simply has none, which the log makes visible.
		return ""
	}
	return "moov-" + hex.EncodeToString(raw)
}

// validRequestID implements §2.2's [A-Za-z0-9._-].
func validRequestID(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// reasonBody is §2.2's {"reason": "..."} shape.
type reasonBody struct {
	Reason string `json:"reason"`
	// State is set only on the 409, where the contract adds it.
	State string `json:"state,omitempty"`
	// Upstream is set only on 502/503.
	Upstream string `json:"upstream,omitempty"`
}

// fieldErrorBody is §2.2's {"field": "...", "reason": "..."} shape.
type fieldErrorBody struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// writeAccountsError maps a service error onto the contract's vocabulary.
//
// It is exhaustive by construction: anything that is not one of the typed
// failures is a 500 with no detail, because a status this function had to
// guess would be a status the contract does not define.
func (s *Server) writeAccountsError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *accounts.FieldError
	switch {
	case errors.As(err, &fe):
		writeJSON(w, http.StatusBadRequest, fieldErrorBody{Field: fe.Field, Reason: fe.Reason})
	case errors.Is(err, accounts.ErrNotFound):
		writeNotFound(w)
	case errors.Is(err, accounts.ErrDeleting):
		writeJSON(w, http.StatusConflict, reasonBody{
			Reason: "the account is being deleted", State: string(accounts.StateDeleting),
		})
	case errors.Is(err, accounts.ErrExportPurged):
		writeGenericProblem(w, http.StatusGone, "this export has been purged; request a new one")
	case errors.Is(err, accounts.ErrUpstreamRefused):
		// The SUMMARY only. §2.2: never the raw upstream body, never a
		// credential - so the wrapped cause stays in the log, where the
		// handler already put it.
		s.log.Warn("accounts api: upstream refused", "error", err,
			"request_id", w.Header().Get(headerRequestID))
		writeJSON(w, http.StatusBadGateway, reasonBody{
			Reason: summaryOf(err), Upstream: "mailcow",
		})
	case errors.Is(err, accounts.ErrUpstreamUnavailable):
		s.log.Warn("accounts api: upstream unavailable", "error", err,
			"request_id", w.Header().Get(headerRequestID))
		w.Header().Set("Retry-After", "30")
		writeJSON(w, http.StatusServiceUnavailable, reasonBody{
			Reason: "Mailcow is temporarily unavailable", Upstream: "mailcow",
		})
	default:
		s.log.Error("accounts api: unhandled failure", "error", err,
			"path", r.URL.Path, "request_id", w.Header().Get(headerRequestID))
		writeGenericProblem(w, http.StatusInternalServerError, "internal error")
	}
}

// summaryOf extracts Moov's own summary from a wrapped upstream error: the
// text between the sentinel and the cause. It never reaches for the cause
// itself, which is where an upstream body or a credential could hide.
func summaryOf(err error) string {
	msg := err.Error()
	const marker = "upstream refused: "
	i := strings.Index(msg, marker)
	if i < 0 {
		return "the upstream operation was refused"
	}
	rest := msg[i+len(marker):]
	if j := strings.Index(rest, ": "); j > 0 {
		return rest[:j]
	}
	return rest
}

// rateLimiter is a token bucket with a burst that differs from the refill
// rate - which writeLimiter, whose burst IS its rate, cannot express. §2.2
// asks for 120 per minute with a burst of 30: a consumer may spend 30 at
// once and then sustain two per second.
type rateLimiter struct {
	burst  float64
	perSec float64
	now    func() time.Time

	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

func newRateLimiter(burst, rate int, window time.Duration, now func() time.Time) *rateLimiter {
	if now == nil {
		now = time.Now
	}
	return &rateLimiter{
		burst:   float64(burst),
		perSec:  float64(rate) / window.Seconds(),
		now:     now,
		buckets: make(map[string]*tokenBucket),
	}
}

func (l *rateLimiter) allow(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.perSec)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	return time.Duration((1 - b.tokens) / l.perSec * float64(time.Second)), false
}
