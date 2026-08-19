package jmaphttp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
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
	// WriteTimeout bounds writing a response.
	//
	// It is ZERO — no deadline — since W4a, and that is a deliberate,
	// narrow trade rather than an oversight.
	//
	// http.Server's WriteTimeout is absolute: it is set once from the moment
	// the request is read and is never extended by a successful write. An
	// EventSource stream is a response that must stay open for hours (RFC
	// 8620 §7.3: "a long running HTTP request, where the server can push data
	// to the client by appending data without ending the response"), so ANY
	// non-zero value here is a timer that kills every healthy push connection
	// at exactly that interval — the failure would look like a client bug and
	// would not appear in a short test.
	//
	// What is lost is a write deadline on the ordinary API responses. What
	// replaces it, for the slowloris-style risks that deadline addressed:
	//
	//   - ReadHeaderTimeout and ReadTimeout below still bound the REQUEST
	//     side, which is where a slow-sending attacker operates.
	//   - IdleTimeout still reaps quiet keep-alive connections.
	//   - Every handler's work is bounded by the request context, which
	//     net/http cancels when the client disconnects; the streaming handler
	//     selects on it (eventsource.go) and every other handler passes it to
	//     the store.
	//   - maxConcurrentRequests (§2) bounds how many requests one user may
	//     hold open at once, and MaxSSEPerAccount bounds the streams.
	//
	// The alternative — a second http.Server on its own listener just for
	// this route — was rejected for W4a: it would need its own TLS, its own
	// CORS, its own auth wiring and its own entry in the fronting proxy, all
	// to reinstate a deadline whose job is already covered above.
	WriteTimeout = 0
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

	// Blobs serves the download endpoint. nil leaves the route answering the
	// same 404 as a missing blob — which is what a server built only for
	// protocol tests wants, and what keeps this dependency optional without
	// ever making the route leak the difference.
	Blobs BlobReader

	// Notifier and State power the EventSource endpoint (W4a, RFC 8620 §7.3).
	//
	// Both are required for push: Notifier says WHEN an account changed
	// (internal/sync's Broker), State says what its current per-type state
	// strings are (mail.Adapter, the same reader /get and /changes use — see
	// eventsource.go on why that identity matters). With either nil the route
	// answers 501, which is exactly what it answered before push existed.
	Notifier StateNotifier
	State    StateSource

	// MaxSSEPerAccount caps concurrent EventSource connections per account
	// (W-A4). Zero means DefaultMaxSSEPerAccount.
	MaxSSEPerAccount int

	// Metrics receives per-request observations (E8-lite). nil disables
	// recording entirely, which is what every protocol test runs with.
	//
	// It is an INTERFACE declared in this package rather than an import of
	// internal/metrics, for the same reason BlobReader is: the transport needs
	// exactly one method, and a local seam keeps a protocol package free of a
	// dependency on a concrete telemetry implementation. metrics.Metrics
	// satisfies it by construction.
	Metrics RequestRecorder
}

// RequestRecorder is the metrics layer's view of an HTTP request.
//
// One method, deliberately: everything a dashboard needs about the transport is
// (which route, what status, how long), and a wider interface here would invite
// the protocol layer to grow opinions about telemetry.
type RequestRecorder interface {
	// ObserveHTTP records one finished request. route is the ROUTE PATTERN
	// (/jmap/download/{accountId}/...), never the concrete path: the pattern is
	// a bounded label set, the path is not.
	ObserveHTTP(route string, status int, d time.Duration)
}

// BlobReader is the download endpoint's view of the blob layer.
//
// It is declared here, as a one-method interface, rather than imported from
// internal/jmap/mail: the HTTP layer needs nothing else from that package's
// contracts, and a local interface keeps the transport testable with a fake
// that has no store behind it. mail.Adapter satisfies it by construction.
type BlobReader interface {
	// OpenBlob returns the blob's content and size, or mail.ErrNotFound when
	// the account does not reference it — the two cases the route must render
	// identically.
	OpenBlob(ctx context.Context, accountID int64, blobID string) (io.ReadCloser, int64, error)
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
	blobs    BlobReader
	metrics  RequestRecorder
	log      *slog.Logger

	// Push (W4a).
	notifier         StateNotifier
	state            StateSource
	maxSSEPerAccount int
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

	maxSSE := cfg.MaxSSEPerAccount
	if maxSSE <= 0 {
		maxSSE = DefaultMaxSSEPerAccount
	}

	return &Server{
		cfg:              cfg,
		auth:             auth,
		registry:         registry,
		engine:           engine,
		cors:             newCORSPolicy(cfg.AllowedOrigins),
		gate:             newConcurrencyGate(cfg.Limits.MaxConcurrentRequests),
		blobs:            cfg.Blobs,
		metrics:          cfg.Metrics,
		log:              cfg.Logger,
		notifier:         cfg.Notifier,
		state:            cfg.State,
		maxSSEPerAccount: maxSSE,
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

// handleDownload serves GET /jmap/download/{accountId}/{blobId}/{name}
// (RFC 8620 §6.2).
//
// The authorization shape was fixed when the route was stubbed and is
// unchanged: a download URL naming an account that is not the caller's answers
// the same 404 as a missing blob — never 403, which would confirm to a probing
// client that the foreign accountId exists. J2 fills in the body: an ownership
// check in the store, then the bytes with headers that can never execute.
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
	if s.blobs == nil {
		// No blob reader wired (a server built for protocol tests only). Same
		// 404 as everything else on this route: the client cannot tell, and
		// must not be able to tell, why.
		writeGenericProblem(w, http.StatusNotFound, "not found")
		return
	}

	rc, size, err := s.blobs.OpenBlob(r.Context(), accountID, r.PathValue("blobId"))
	if err != nil {
		// Every failure is the same 404, including an unexpected internal one:
		// distinguishing "missing" from "broken" here would leak existence
		// through the error channel that the status code closes. The real
		// error goes to the log for the operator.
		if !errors.Is(err, mail.ErrNotFound) {
			s.log.Error("jmap: opening blob for download failed",
				"account_id", accountID, "error", err)
		}
		writeGenericProblem(w, http.StatusNotFound, "not found")
		return
	}
	defer func() { _ = rc.Close() }()

	// §6.2: "the server MUST NOT use the type given by the client without
	// validating it". DownloadHeaders applies the allowlist and sanitizes the
	// name; nothing client-supplied reaches a header unchecked.
	contentType, disposition := mail.DownloadHeaders(
		r.URL.Query().Get("type"), r.PathValue("name"))

	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", disposition)
	// Belt and braces with the allowlist: even for an allowlisted type, a
	// browser must not be allowed to sniff the bytes into something else.
	h.Set("X-Content-Type-Options", "nosniff")
	// Other people's mail is never a shared cache's business.
	h.Set("Cache-Control", "private, max-age=0, no-store")
	// Defense in depth for the inline types: no scripts, no plugins, no
	// framing, whatever the bytes turn out to be.
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(size, 10))
	}
	// Range requests are not supported in phase 1 (L2 §2.3 scopes download to
	// whole blobs). Saying so explicitly stops a client from issuing a Range
	// request and misreading the complete 200 response as a partial one.
	h.Set("Accept-Ranges", "none")

	if _, err := io.Copy(w, rc); err != nil {
		// The status is already sent; all that is left is the log. A client
		// disconnect mid-download is routine, not an error worth alarming on.
		s.log.Debug("jmap: blob download interrupted",
			"account_id", accountID, "error", err)
	}
}

// handleUpload is the phase-1 upload stub: uploadUrl must exist in the
// session (RFC 8620 §2), and L2 §2.3 stubs it at 501 until a write phase.
func (s *Server) handleUpload(w http.ResponseWriter, _ *http.Request) {
	writeGenericProblem(w, http.StatusNotImplemented,
		"upload is not implemented in this phase (read-only server)")
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
