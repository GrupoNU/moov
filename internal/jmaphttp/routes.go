package jmaphttp

import (
	"net/http"
	"sort"
	"strings"
)

// Every route the JMAP server exposes, in one place (L2 §2.4). Spike S1 H7's
// lesson is codified here: a fronting proxy needs the complete route list to
// do same-origin serving, and /.well-known/jmap must serve the session
// DIRECTLY — jmap-proxy's redirect variant confused real clients.
const (
	// PathWellKnown serves the Session object (RFC 8620 §2.2). GET,
	// authenticated.
	PathWellKnown = "/.well-known/jmap"

	// PathAPI is the API endpoint (§3.1). POST, authenticated.
	PathAPI = "/jmap/api"

	// PathDownload is the blob download endpoint (§6.2). GET, authenticated.
	// Wired now, stubbed at 404 until J2. The Go 1.22 pattern variables match
	// the URI Template variables the session advertises (the {type} variable
	// rides the query string, as §2 recommends).
	PathDownload = "/jmap/download/{accountId}/{blobId}/{name}"

	// PathUpload is the upload endpoint (§6.1). POST, authenticated. Real
	// since W3 (upload.go); 501 when no uploader is wired.
	PathUpload = "/jmap/upload/{accountId}"

	// PathEventSource is the push endpoint (§7.3). GET; 501 in phase 1.
	PathEventSource = "/jmap/eventsource"
)

// route is one row of the route table.
type route struct {
	method  string
	pattern string
	handler http.HandlerFunc

	// public marks a route that is served WITHOUT authentication.
	//
	// The field is explicit, and named for what it grants rather than for what
	// it skips, because the default must stay "authenticated": a route added
	// to the table below without thinking about this field gets the zero value
	// and is therefore protected. Exactly two routes set it, both branding
	// (W-A1), and the reason is in branding.go — the brand IS the login
	// screen, so it cannot live behind the credentials the login screen exists
	// to collect. A test pins the public set so a third one cannot appear
	// unnoticed.
	public bool
}

// routes returns the complete route table. Every route gets a CORS preflight
// handler and, unless it is explicitly marked public, an authentication
// wrapper — both wired mechanically by Handler below so a route added here
// cannot forget either.
func (s *Server) routes() []route {
	return []route{
		{method: http.MethodGet, pattern: PathWellKnown, handler: s.handleSession},
		{method: http.MethodPost, pattern: PathAPI, handler: s.handleAPI},
		{method: http.MethodGet, pattern: PathDownload, handler: s.handleDownload},
		{method: http.MethodPost, pattern: PathUpload, handler: s.handleUpload},
		{method: http.MethodGet, pattern: PathEventSource, handler: s.handleEventSource},

		// Branding (W-A1): public by design, see branding.go.
		{method: http.MethodGet, pattern: PathBranding, handler: s.handleBranding, public: true},
		{method: http.MethodGet, pattern: PathBrandingAsset, handler: s.handleBrandingAsset, public: true},
	}
}

// Handler builds the complete HTTP handler: the route table wrapped in
// authentication per route, plus OPTIONS preflight per path, all inside the
// CORS, panic-recovery and logging middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	methodsByPattern := make(map[string][]string)
	for _, rt := range s.routes() {
		h := rt.handler
		if !rt.public {
			h = s.requireAuth(h)
		}
		mux.Handle(rt.method+" "+rt.pattern, h)
		methodsByPattern[rt.pattern] = append(methodsByPattern[rt.pattern], rt.method)
	}

	// OPTIONS per path, unauthenticated: browsers never send credentials on a
	// preflight, so an auth-gated preflight would break CORS entirely.
	for pattern, methods := range methodsByPattern {
		sort.Strings(methods)
		mux.Handle("OPTIONS "+pattern, s.preflightHandler(methods))
	}

	// Order, outermost first: logging sees the final status (including a
	// recovered panic's 500); recovery guards everything below it; CORS
	// headers go on every response — errors included, or the browser hides
	// the status from the client's JS.
	var h http.Handler = mux
	h = s.corsMiddleware(h)
	h = s.recoverMiddleware(h)
	h = s.logMiddleware(h)
	return h
}

// preflightHandler answers CORS preflight for one path.
func (s *Server) preflightHandler(methods []string) http.HandlerFunc {
	allow := strings.Join(append(append([]string{}, methods...), http.MethodOptions), ", ")
	return func(w http.ResponseWriter, r *http.Request) {
		// The Allow-Origin/credentials headers are already set by
		// corsMiddleware; here only the preflight-specific grants are added,
		// and only for an allowed origin asking a real preflight question.
		origin := r.Header.Get("Origin")
		if _, ok := s.cors.allows(origin); ok && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Set("Access-Control-Allow-Methods", allow)
			// Authorization and Content-Type are what a JMAP client sends
			// (L2 §2.4); nothing else is granted.
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		w.Header().Set("Allow", allow)
		w.WriteHeader(http.StatusNoContent)
	}
}
