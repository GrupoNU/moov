package jmaphttp

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/GrupoNU/moov/internal/accounts"
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
	// and is therefore protected. The branding routes set it (W-A1), and the
	// reason is in branding.go — the brand IS the login screen, so it cannot
	// live behind the credentials the login screen exists to collect. A test
	// pins the public set so another one cannot appear unnoticed.
	public bool

	// tokenScope, when set, additionally lets the route accept a scoped
	// short-lived token in the query string (token.go) — for the browser
	// contexts that cannot attach an Authorization header. The zero value
	// keeps a route Basic-only, so /jmap/api and every other route refuse a
	// token by CONSTRUCTION, not by a check someone must remember. A test
	// pins the token-accepting set exactly as one pins the public set.
	tokenScope TokenScope

	// serviceScope, when set, makes the route part of the accounts API's
	// SECOND authentication class: it is served by serviceRoute, which
	// accepts a service-account key (Bearer msa1_…) and NOTHING else — a
	// mailbox credential presented here resolves to no service account and
	// gets the generic 404 (contract §4).
	//
	// It is a third field rather than a value of tokenScope because the two
	// grant incomparable things: a token is one mailbox's own short-lived
	// capability, a service-account key manages a whole domain's mailboxes
	// and can read no mail at all. Collapsing them would make a route able to
	// drift from one class to the other by a one-word edit. The value is the
	// scope the key must carry (accounts.ScopeRead / ScopeWrite), and the set
	// is pinned by test exactly as the public and token sets are.
	serviceScope string
}

// routes returns the complete route table. Every route gets a CORS preflight
// handler and, unless it is explicitly marked public, an authentication
// wrapper — both wired mechanically by Handler below so a route added here
// cannot forget either.
func (s *Server) routes() []route {
	return []route{
		{method: http.MethodGet, pattern: PathWellKnown, handler: s.handleSession},
		{method: http.MethodPost, pattern: PathAPI, handler: s.handleAPI},
		// Download and EventSource additionally accept a scoped token in the
		// query string: an <a download>, an <img> and an EventSource cannot
		// attach an Authorization header (verified against the live pilot —
		// web/README.md gap 4). Each route accepts ONLY its own scope.
		{method: http.MethodGet, pattern: PathDownload, handler: s.handleDownload, tokenScope: ScopeBlob},
		{method: http.MethodPost, pattern: PathUpload, handler: s.handleUpload},
		{method: http.MethodGet, pattern: PathEventSource, handler: s.handleEventSource, tokenScope: ScopePush},

		// Token minting and revocation (token.go). Basic-authenticated like
		// any other route — minting rides the same cache/lockout/budget path.
		{method: http.MethodPost, pattern: PathToken, handler: s.handleTokenMint},
		{method: http.MethodPost, pattern: PathTokenRevoke, handler: s.handleTokenRevoke},

		// Forwarding verification (E6, forwarding.go). Authenticated by the
		// table's default; the token in the query is consent evidence, not a
		// credential.
		{method: http.MethodGet, pattern: PathForwardingVerify, handler: s.handleForwardingVerify},

		// Branding (W-A1): public by design, see branding.go.
		{method: http.MethodGet, pattern: PathBranding, handler: s.handleBranding, public: true},
		{method: http.MethodGet, pattern: PathBrandingAsset, handler: s.handleBrandingAsset, public: true},
		// The PWA manifest and icons the shell links (branding_pwa.go): the
		// browser fetches them before, and independently of, any login.
		{method: http.MethodGet, pattern: PathBrandingManifest, handler: s.handleBrandingManifest, public: true},
		{method: http.MethodGet, pattern: PathBrandingIcon, handler: s.handleBrandingIcon, public: true},

		// Brand administration (BA-1, branding_admin.go). Authenticated by the
		// table's default, then authorized per host by brandAdminRoute; the
		// writes additionally pass the per-actor budget and the per-host lock
		// (brandAdminWrite). A non-admin gets the generic 404.
		{method: http.MethodGet, pattern: PathBrandingAdmin, handler: s.brandAdminRoute(s.handleBrandAdminProbe)},
		{method: http.MethodGet, pattern: PathBrandingAdminBrand, handler: s.brandAdminRoute(s.handleBrandAdminGet)},
		{method: http.MethodPut, pattern: PathBrandingAdminBrand, handler: s.brandAdminWrite(s.handleBrandAdminPut)},
		{method: http.MethodPut, pattern: PathBrandingAdminAsset, handler: s.brandAdminWrite(s.handleBrandAdminPutAsset)},
		{method: http.MethodDelete, pattern: PathBrandingAdminAsset, handler: s.brandAdminWrite(s.handleBrandAdminDeleteAsset)},
		{method: http.MethodPost, pattern: PathBrandingAdminReset, handler: s.brandAdminWrite(s.handleBrandAdminReset)},

		// The remote-image proxy (ADR §5). Signing requires auth; serving a
		// signed image CANNOT (the requester is an <img> in a sandboxed
		// iframe, which can attach no header), so the GET route is public in
		// this table's sense and authorized by HMAC instead — the full
		// argument is in imgproxy.go, and the public-set pin in
		// branding_test.go names it.
		{method: http.MethodPost, pattern: PathImageProxySign, handler: s.handleImageProxySign},
		{method: http.MethodGet, pattern: PathImageProxy, handler: s.handleImageProxy, public: true},

		// The per-domain accounts API (epic M1, admin_accounts.go). Every row
		// carries a serviceScope, which is what puts it in the service-account
		// authentication class instead of the mailbox one; reads need
		// accounts:read, writes need accounts:write (which implies read).
		// With the feature off, serviceRoute answers the generic 404, so the
		// rows are unconditional — their presence reveals nothing.
		{method: http.MethodPost, pattern: PathAdminAccounts, handler: s.serviceRoute(accounts.ScopeWrite, s.handleAccountCreate), serviceScope: accounts.ScopeWrite},
		{method: http.MethodGet, pattern: PathAdminAccount, handler: s.serviceRoute(accounts.ScopeRead, s.handleAccountGet), serviceScope: accounts.ScopeRead},
		{method: http.MethodPatch, pattern: PathAdminAccount, handler: s.serviceRoute(accounts.ScopeWrite, s.handleAccountUpdate), serviceScope: accounts.ScopeWrite},
		{method: http.MethodDelete, pattern: PathAdminAccount, handler: s.serviceRoute(accounts.ScopeWrite, s.handleAccountDelete), serviceScope: accounts.ScopeWrite},
		{method: http.MethodPost, pattern: PathAdminAccountSuspend, handler: s.serviceRoute(accounts.ScopeWrite, s.transitionHandler(suspendOp)), serviceScope: accounts.ScopeWrite},
		{method: http.MethodPost, pattern: PathAdminAccountResume, handler: s.serviceRoute(accounts.ScopeWrite, s.transitionHandler(resumeOp)), serviceScope: accounts.ScopeWrite},
		{method: http.MethodPost, pattern: PathAdminAccountReadOnly, handler: s.serviceRoute(accounts.ScopeWrite, s.transitionHandler(readOnlyOp)), serviceScope: accounts.ScopeWrite},
		{method: http.MethodPost, pattern: PathAdminAccountExport, handler: s.serviceRoute(accounts.ScopeWrite, s.handleExportStart), serviceScope: accounts.ScopeWrite},
		{method: http.MethodGet, pattern: PathAdminAccountExport, handler: s.serviceRoute(accounts.ScopeRead, s.handleExportGet), serviceScope: accounts.ScopeRead},

		// The signed export download (§2.6). PUBLIC in this table's sense,
		// deliberately, and the public-set pin names it: the URL is handed to
		// a BROWSER TAB, which attaches no Authorization header — the same
		// shape PathImageProxy has, and the same reasoning. Its authority is
		// the HMAC in its query string, bound to the origin, the export id
		// and a 24 h expiry, granting exactly one zip and nothing else; a
		// tampered, foreign-host or expired signature answers the generic
		// 404. It is NOT in the service class: a key cannot open it and it
		// cannot manage anything.
		{method: http.MethodGet, pattern: PathAdminExportDownload, handler: s.handleExportDownload, public: true},

		// --- Delegated sign-in (M2, delegated.go) -------------------------------
		// Public in the table's sense — each route authenticates ITSELF: the
		// exchange and revoke verify an issuer-signed JWT, renew and logout
		// resolve the bearer session they act on. They cannot sit behind the
		// Basic gate: a portal user has no password, and a host with no issuer
		// configured must answer the generic 404 before any credential is
		// examined. The public-set pin in branding_test.go names all four.
		{method: http.MethodPost, pattern: PathDelegatedExchange, handler: s.handleDelegatedExchange, public: true},
		{method: http.MethodPost, pattern: PathDelegatedRenew, handler: s.handleDelegatedRenew, public: true},
		{method: http.MethodPost, pattern: PathDelegatedLogout, handler: s.handleDelegatedLogout, public: true},
		{method: http.MethodPost, pattern: PathDelegatedRevoke, handler: s.handleDelegatedRevoke, public: true},
		// --- end delegated sign-in ------------------------------------------------
	}
}

// The three transition operations, as values, so the route table names them
// once and the shared transitionHandler stays one function rather than three.
func suspendOp(s *accounts.Service) func(context.Context, accounts.Call, string) (accounts.Account, error) {
	return s.Suspend
}

func resumeOp(s *accounts.Service) func(context.Context, accounts.Call, string) (accounts.Account, error) {
	return s.Resume
}

func readOnlyOp(s *accounts.Service) func(context.Context, accounts.Call, string) (accounts.Account, error) {
	return s.ReadOnly
}

// Handler builds the complete HTTP handler: the route table wrapped in
// authentication per route, plus OPTIONS preflight per path, all inside the
// CORS, panic-recovery and logging middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	methodsByPattern := make(map[string][]string)
	for _, rt := range s.routes() {
		h := rt.handler
		switch {
		case rt.public:
			// Served without authentication; the set is pinned by test.
		case rt.serviceScope != "":
			// The accounts API's own class: the handler in the row is ALREADY
			// wrapped in serviceRoute (which is where the key, the scope, the
			// budget and the no-oracle 404 live), so the mailbox
			// authenticator must not also run — a service-account key is not
			// a mailbox credential and requireAuth would refuse it with a 401
			// that tells a prober the route exists.
		case rt.tokenScope != "":
			h = s.requireAuthOrToken(rt.tokenScope, h)
		default:
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
