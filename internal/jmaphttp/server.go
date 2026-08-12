package jmaphttp

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Timeouts for the *http.Server that mounts Handler. They are exported so
// cmd/moovd constructs its server from the same values this package was
// designed against.
const (
	// ReadHeaderTimeout bounds reading the request headers; it is the defense
	// against slowloris-style connection hoarding.
	ReadHeaderTimeout = 10 * time.Second
	// ReadTimeout bounds reading a whole request. Generous because a request
	// body may legitimately approach maxSizeRequest over a slow uplink.
	ReadTimeout = 2 * time.Minute
	// WriteTimeout bounds writing a response. Phase 2's EventSource endpoint
	// will need a dedicated server or per-route control; phase 1 has no
	// long-lived responses.
	WriteTimeout = 2 * time.Minute
	// IdleTimeout closes keep-alive connections that go quiet.
	IdleTimeout = 5 * time.Minute
)

// Config configures the JMAP HTTP server.
type Config struct {
	// BaseURL is the external base URL clients reach the server at
	// (e.g. "https://mail.example.com"), used to build the absolute URLs of
	// the Session object. Empty means "derive from each request": scheme from
	// X-Forwarded-Proto (set by the fronting proxy) or the connection, host
	// from the Host header.
	BaseURL string

	// AllowedOrigins is the CORS allow-list (L2 §2.4). Empty disables CORS
	// entirely: no Access-Control headers are ever emitted, so browsers only
	// permit same-origin use. The single entry "*" allows any origin —
	// without credentials support, see cors.go.
	AllowedOrigins []string

	// Limits are the request limits, both advertised in the session and
	// enforced (declared == applied). The zero value means jmap.DefaultLimits.
	Limits jmap.Limits

	// Logger receives structured request and error logs. nil means
	// slog.Default().
	Logger *slog.Logger
}

// Server is the JMAP HTTP server: routes, auth, CORS and the protocol engine.
// Construct with New, mount with Handler.
type Server struct {
	cfg      Config
	auth     *Authenticator
	registry *jmap.Registry
	engine   *jmap.Engine
	cors     *corsPolicy
	gate     *concurrencyGate
	log      *slog.Logger
}

// New builds a Server over an Authenticator.
//
// The returned server has Core/echo registered; J2/J3 add the mail methods
// through Registry before Handler is mounted.
func New(cfg Config, auth *Authenticator) (*Server, error) {
	if auth == nil {
		return nil, errors.New("jmaphttp: an Authenticator is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	cfg.Limits = fillLimitDefaults(cfg.Limits)
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	registry := jmap.NewRegistry()
	jmap.RegisterCore(registry)

	// The capability set the engine accepts in "using" is exactly the set the
	// session advertises — one list, used twice, so they cannot drift.
	engine := jmap.NewEngine(registry, cfg.Limits, supportedCapabilities(), cfg.Logger)

	return &Server{
		cfg:      cfg,
		auth:     auth,
		registry: registry,
		engine:   engine,
		cors:     newCORSPolicy(cfg.AllowedOrigins),
		gate:     newConcurrencyGate(cfg.Limits.MaxConcurrentRequests),
		log:      cfg.Logger,
	}, nil
}

// fillLimitDefaults substitutes the default for every unset (non-positive)
// limit. A zero limit is always a configuration mistake, never an intent:
// advertising or enforcing "0" would brick the endpoint it governs.
func fillLimitDefaults(l jmap.Limits) jmap.Limits {
	d := jmap.DefaultLimits()
	if l.MaxSizeUpload <= 0 {
		l.MaxSizeUpload = d.MaxSizeUpload
	}
	if l.MaxConcurrentUpload <= 0 {
		l.MaxConcurrentUpload = d.MaxConcurrentUpload
	}
	if l.MaxSizeRequest <= 0 {
		l.MaxSizeRequest = d.MaxSizeRequest
	}
	if l.MaxConcurrentRequests <= 0 {
		l.MaxConcurrentRequests = d.MaxConcurrentRequests
	}
	if l.MaxCallsInRequest <= 0 {
		l.MaxCallsInRequest = d.MaxCallsInRequest
	}
	if l.MaxObjectsInGet <= 0 {
		l.MaxObjectsInGet = d.MaxObjectsInGet
	}
	if l.MaxObjectsInSet <= 0 {
		l.MaxObjectsInSet = d.MaxObjectsInSet
	}
	return l
}

// supportedCapabilities is the single source for what this server speaks:
// advertised in the Session object AND accepted in a request's "using" list.
func supportedCapabilities() []string {
	return []string{jmap.CapCore, jmap.CapMail}
}

// Registry exposes the method registry so the mail packages (J2/J3) register
// Mailbox/*, Email/* and Thread/* handlers at startup, before Handler is
// mounted.
func (s *Server) Registry() *jmap.Registry { return s.registry }

// handleAPI serves POST /jmap/api (RFC 8620 §3.1).
func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromContext(r.Context())
	if !ok {
		// Unreachable: the route table wraps this handler in requireAuth.
		writeGenericProblem(w, http.StatusInternalServerError, "authentication context missing")
		return
	}

	// §3.1: "The request MUST be of type 'application/json'". A wrong media
	// type is the §3.6.1 notJSON problem; 415 is the precise HTTP status for
	// it. Parameters (charset=utf-8) are accepted, the type itself must match.
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		perr := jmap.NewRequestError(jmap.ProblemNotJSON,
			`the request Content-Type must be "application/json" (RFC 8620 §3.1)`)
		perr.Status = http.StatusUnsupportedMediaType
		writeRequestError(w, perr)
		return
	}

	// maxConcurrentRequests (§2), per authenticated user, enforced from the
	// same Limits struct the session advertises. §3.6.1's "limit" problem
	// carried with 429, the precise HTTP status for concurrency exhaustion.
	user := strings.ToLower(id.Account.Email)
	if !s.gate.tryAcquire(user) {
		w.Header().Set("Retry-After", "2")
		writeRequestError(w, jmap.NewLimitError(http.StatusTooManyRequests,
			"maxConcurrentRequests", "too many concurrent API requests for this user"))
		return
	}
	defer s.gate.release(user)

	// maxSizeRequest (§2): the body is hard-capped by the same declared
	// value. MaxBytesReader fails the read mid-stream, so an oversized body
	// costs bandwidth only up to the limit.
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.Limits.MaxSizeRequest)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeRequestError(w, jmap.NewLimitError(http.StatusRequestEntityTooLarge,
				"maxSizeRequest", "the request body exceeds maxSizeRequest"))
			return
		}
		writeGenericProblem(w, http.StatusBadRequest, "reading the request body failed")
		return
	}

	ctx := jmap.WithCaller(r.Context(), jmap.Caller{
		AccountID: id.Account.ID,
		Email:     id.Account.Email,
	})

	resp, rerr := s.engine.Process(ctx, body, s.sessionState(r, id))
	if rerr != nil {
		writeRequestError(w, rerr)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleDownload is the wired-but-stubbed blob route: real blob serving (with
// per-account ownership checks and safe Content-Type headers, L2 §2.3)
// arrives with J2. Until then every download answers 404 with a problem body.
//
// The ownership check is already live so the route's authorization shape is
// final from day one: a download URL naming an account that is not the
// caller's answers the same 404 as a missing blob — never 403, which would
// confirm to a probing client that the foreign accountId exists.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromContext(r.Context())
	if !ok {
		writeGenericProblem(w, http.StatusInternalServerError, "authentication context missing")
		return
	}
	accountID, err := jmap.DecodeAccountID(r.PathValue("accountId"))
	if err != nil || accountID != id.Account.ID {
		writeGenericProblem(w, http.StatusNotFound, "not found")
		return
	}
	writeGenericProblem(w, http.StatusNotFound,
		"blob download is not available yet (arrives with epic J2)")
}

// handleUpload is the phase-1 upload stub: uploadUrl must exist in the
// session (RFC 8620 §2), and L2 §2.3 stubs it at 501 until a write phase.
func (s *Server) handleUpload(w http.ResponseWriter, _ *http.Request) {
	writeGenericProblem(w, http.StatusNotImplemented,
		"upload is not implemented in this phase (read-only server)")
}

// handleEventSource is the phase-1 push stub (SSE push is phase 2, L2 §1).
func (s *Server) handleEventSource(w http.ResponseWriter, _ *http.Request) {
	writeGenericProblem(w, http.StatusNotImplemented,
		"event source push is not implemented in this phase")
}

// concurrencyGate enforces maxConcurrentRequests per authenticated user.
//
// RFC 8620 §2 defines the limit per server-and-endpoint; scoping it per user
// is the strictest reading that cannot let one user starve another: N users
// may hold N×max requests, each individually capped at the advertised value.
type concurrencyGate struct {
	mu       sync.Mutex
	max      int
	inFlight map[string]int
}

func newConcurrencyGate(maxPerUser int) *concurrencyGate {
	return &concurrencyGate{max: maxPerUser, inFlight: make(map[string]int)}
}

func (g *concurrencyGate) tryAcquire(user string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inFlight[user] >= g.max {
		return false
	}
	g.inFlight[user]++
	return true
}

func (g *concurrencyGate) release(user string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if n := g.inFlight[user]; n <= 1 {
		delete(g.inFlight, user)
	} else {
		g.inFlight[user] = n - 1
	}
}
