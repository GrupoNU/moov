package jmaphttp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/store"
)

// Scoped, short-lived access tokens for the two routes a browser cannot send
// headers to (the PWA's gaps 4 and 6):
//
//   - EventSource (RFC 8620 §7.3): the EventSource API attaches no
//     Authorization header, so the push endpoint was unreachable from the PWA
//     — real-time delivery worked for curl and died in the browser.
//   - <a download> / <img>: a navigation or an image fetch attaches no header
//     either, so per-attachment downloads and cid: images had no path that
//     did not buffer whole blobs through fetch+objectURL.
//
// # Why a token, and why THIS shape of token
//
// RFC 8620 requires every request to be authenticated but deliberately leaves
// the scheme to the server (§8.2 defers to the RFC 7235 menu and asks the
// implementer to weigh the schemes' security characteristics). Basic stays the
// primary scheme (arbitration J-A1 — it is what Bulwark speaks and what the
// IMAP LOGIN validation needs); this file adds the narrowest possible second
// path for the two contexts where a header is PHYSICALLY impossible, following
// the local precedent of the image proxy (imgproxy.go): a capability minted by
// an authenticated request, signed with a per-process HMAC key, valid briefly.
//
// What keeps the token from being "a bearer credential worth stealing":
//
//   - SCOPE. A token names exactly one scope ("push" or "blob") and one
//     account. A push token opens an event stream — it can learn that state
//     changed, never read a message. A blob token reads blobs the account
//     already references — it cannot list them, query mail, or write
//     anything. Presenting either at /jmap/api (or anywhere else) fails,
//     because only the two routes that need tokens ever consult the verifier,
//     and the verifier refuses any scope but the route's own.
//   - TTL. tokenTTL (10 minutes — deliberately the same trust window J-A1
//     grants a cached positive password validation; a token never outlives
//     the trust already extended to the credential that minted it). The
//     client refreshes ahead of expiry with an ordinary authenticated call.
//   - REVOCATION. Tokens are registered at mint and checked against the
//     registry at use, so sign-out (POST /jmap/token/revoke) kills them
//     immediately rather than at TTL. Account state is re-checked from the
//     store on every use — a disabled account's tokens stop working on the
//     next request, exactly like its cached password does (requireProvisioned
//     runs the same check for Basic). Credential rotation upstream is bounded
//     by the same 10-minute window the credential cache already accepts, and
//     Server.InvalidateAccountTokens is the token half of that invalidation
//     hook.
//   - PROCESS LIFETIME. The key is random per process and the registry is in
//     memory, so a restart invalidates everything — the same trade imgproxy
//     documents. For SSE that costs one reconnect-and-remint (the client does
//     this unprompted on its refresh cycle); for downloads it costs a stale
//     href until the next refresh tick, a few minutes at worst. The
//     alternative — a persisted signing key — would create a durable secret
//     to provision, rotate and leak, to save at most a few minutes of token
//     lifetime that the TTL throws away anyway.
//
// # The query string, addressed rather than shrugged at
//
// EventSource and <img> can only carry the token in the URL, which is the one
// place secrets are notoriously copied from: access logs, Referer headers,
// browser history. Concretely:
//
//   - Our own logs never see it: logMiddleware logs the path and NEVER the
//     query string (its doc says why; TestLogMiddlewareNeverLogsQueryString
//     pins it), and metrics label by route pattern, never by URL.
//   - Referer cannot carry it: a token rides SUBRESOURCE URLs (a stream, an
//     image, a download), and only documents become referrers. No page URL
//     ever contains a token.
//   - History: EventSource connections and <a download> clicks do not create
//     history entries.
//   - The fronting proxy's access log is the residual exposure and is a
//     deployment concern: deploy/README.md documents that the pilot's Caddy
//     must not log query strings on /jmap/* (or accept that a VPN-only log
//     briefly holds 10-minute single-scope capabilities).
//
// # Minting is inside the existing gates
//
// POST /jmap/token is an ordinary authenticated route: failed Basic attempts
// feed the lockout and the global failure budget exactly as on any route, and
// successful mints ride the credential cache. On top of that the registry
// caps outstanding tokens per account (maxTokensPerAccount) by evicting the
// oldest-expiring token, so a hoarding client only limits itself.

// TokenScope names what a token is FOR. The scope is inside the signed
// payload, so it cannot be altered without invalidating the signature.
type TokenScope string

const (
	// ScopePush grants GET /jmap/eventsource for one account: an event stream
	// of state strings. It reads no mail.
	ScopePush TokenScope = "push"

	// ScopeBlob grants GET /jmap/download/{accountId}/... for one account.
	//
	// The scope is the ACCOUNT's blobs, not one blob, and the choice is
	// deliberate: the consumer is a message's attachment list and its cid:
	// images — up to dozens of blob URLs per message, known only as the
	// message renders. Minting per blob would put an authenticated round trip
	// in front of every inline image, which is the latency the token exists
	// to remove; and it would grant nothing narrower in practice, because the
	// downloadable set is already limited to blobs the account references
	// (OpenBlob's ownership rule). The token holder can read blobs the
	// account can read, for ten minutes, and nothing else.
	ScopeBlob TokenScope = "blob"
)

// Paths for minting and revoking. Both are ordinary authenticated routes in
// the route table.
const (
	// PathToken mints tokens. POST, authenticated, never public.
	PathToken = "/jmap/token"

	// PathTokenRevoke revokes previously minted tokens. POST, authenticated:
	// sign-out calls it while the credential is still in hand, and a revoke
	// that cannot authenticate is bounded by the TTL anyway.
	PathTokenRevoke = "/jmap/token/revoke" //nolint:gosec // G101: a URL path, not a credential
)

const (
	// TokenQueryParam is the query parameter carrying a token at the two
	// routes that accept one. The name follows RFC 6750 §2.3, whose warnings
	// about URI-carried tokens are exactly the ones the package comment
	// mitigates; it applies here because header transport is impossible for
	// these clients, which is the one case §2.3 reserves the form for.
	TokenQueryParam = "access_token"

	// tokenTTL is a token's lifetime. See the package comment for why it
	// equals DefaultAuthCacheTTL on purpose.
	tokenTTL = 10 * time.Minute

	// maxTokensPerAccount bounds the registry per account. A client mints two
	// scopes per tab and refreshes ahead of expiry, so even many tabs across
	// many devices stay far under this; hitting it evicts the oldest-expiring
	// token, which hurts only the account doing the hoarding.
	maxTokensPerAccount = 64

	// maxScopesPerMint and maxTokensPerRevoke bound the request bodies.
	maxScopesPerMint   = 4
	maxTokensPerRevoke = 16

	// tokenPrefix versions the wire format: a future format change makes old
	// tokens unparseable rather than ambiguously parseable.
	tokenPrefix = "mt1"

	// tokenNonceBytes sizes the random token id. 16 bytes = 128 bits, far
	// beyond guessable even without the HMAC that already makes guessing
	// pointless.
	tokenNonceBytes = 16
)

// tokenCanonical is the byte string the HMAC covers. Versioned and
// NUL-separated for the same reasons imgproxy's signCanonical is.
func tokenCanonical(payload string) []byte {
	return []byte("moov-token-v1\x00" + payload)
}

// errTokenInvalid is the single external truth about any refused token. The
// internal reasons (bad MAC, expired, revoked, wrong scope) are deliberately
// not distinguished to the client: an attacker probing the endpoint learns
// only "no".
var errTokenInvalid = errors.New("jmaphttp: token invalid")

// tokenGrant is one outstanding token's registry entry.
type tokenGrant struct {
	accountID int64
	scope     TokenScope
	expires   time.Time
}

// tokenAuthority mints and verifies scoped tokens. Safe for concurrent use.
type tokenAuthority struct {
	key []byte
	now func() time.Time

	mu sync.Mutex
	// grants indexes by nonce (the token id). The registry is what makes
	// revocation possible on a signed token: the signature proves WE minted
	// it, the registry proves it is still WANTED.
	grants map[string]tokenGrant
	// byAccount indexes nonces per account for RevokeAccount and the cap.
	byAccount map[int64]map[string]struct{}
}

// newTokenAuthority builds an authority with a fresh random key.
func newTokenAuthority() (*tokenAuthority, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("token: generating the signing key: %w", err)
	}
	return &tokenAuthority{
		key:       key,
		now:       time.Now,
		grants:    make(map[string]tokenGrant),
		byAccount: make(map[int64]map[string]struct{}),
	}, nil
}

// Mint issues one token for one account and scope.
func (a *tokenAuthority) Mint(accountID int64, scope TokenScope) (token string, ttl time.Duration, err error) {
	if scope != ScopePush && scope != ScopeBlob {
		return "", 0, fmt.Errorf("token: unknown scope %q", scope)
	}

	nonceRaw := make([]byte, tokenNonceBytes)
	if _, randErr := rand.Read(nonceRaw); randErr != nil {
		return "", 0, fmt.Errorf("token: generating a nonce: %w", randErr)
	}
	nonce := hex.EncodeToString(nonceRaw)
	expires := a.now().Add(tokenTTL)

	payload := string(scope) + "|" + strconv.FormatInt(accountID, 10) + "|" +
		strconv.FormatInt(expires.Unix(), 10) + "|" + nonce
	mac := hmac.New(sha256.New, a.key)
	mac.Write(tokenCanonical(payload))
	token = tokenPrefix + "." +
		base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked(accountID)
	if perAccount := a.byAccount[accountID]; len(perAccount) >= maxTokensPerAccount {
		a.evictOldestLocked(accountID)
	}
	a.grants[nonce] = tokenGrant{accountID: accountID, scope: scope, expires: expires}
	if a.byAccount[accountID] == nil {
		a.byAccount[accountID] = make(map[string]struct{})
	}
	a.byAccount[accountID][nonce] = struct{}{}

	return token, tokenTTL, nil
}

// Verify checks a presented token for one required scope and returns the
// account it grants. Every failure is errTokenInvalid — see its doc.
func (a *tokenAuthority) Verify(token string, want TokenScope) (int64, error) {
	scope, accountID, expiresUnix, nonce, ok := a.parseAndAuthenticate(token)
	if !ok {
		return 0, errTokenInvalid
	}
	// Scope is enforced HERE, in the one function every token-accepting route
	// calls, so a push token at the download route (or vice versa) dies in
	// the same place a forged one does.
	if scope != want {
		return 0, errTokenInvalid
	}
	if a.now().Unix() > expiresUnix {
		return 0, errTokenInvalid
	}

	a.mu.Lock()
	grant, present := a.grants[nonce]
	a.mu.Unlock()
	// The registry must agree with the signed payload. A missing entry means
	// revoked (or a restart, where the key check above already failed); a
	// mismatched entry cannot happen without a bug, and failing closed on it
	// is the only safe answer.
	if !present || grant.accountID != accountID || grant.scope != scope {
		return 0, errTokenInvalid
	}
	return accountID, nil
}

// Revoke invalidates one token, if it is genuine and belongs to the given
// account. The account check is what stops an authenticated user revoking
// another account's tokens by pasting them here.
func (a *tokenAuthority) Revoke(token string, accountID int64) {
	_, tokenAccount, _, nonce, ok := a.parseAndAuthenticate(token)
	if !ok || tokenAccount != accountID {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropLocked(nonce)
}

// RevokeAccount invalidates every outstanding token for an account — the
// token half of the J-A1 invalidation hook (Authenticator.InvalidateUser is
// the credential half; Server.InvalidateAccountTokens joins them).
func (a *tokenAuthority) RevokeAccount(accountID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for nonce := range a.byAccount[accountID] {
		delete(a.grants, nonce)
	}
	delete(a.byAccount, accountID)
}

// parseAndAuthenticate splits a token and checks its MAC in constant time.
// Nothing derived from the payload is trusted before the MAC verifies.
func (a *tokenAuthority) parseAndAuthenticate(token string) (scope TokenScope, accountID, expiresUnix int64, nonce string, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != tokenPrefix {
		return "", 0, 0, "", false
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", 0, 0, "", false
	}
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", 0, 0, "", false
	}
	mac := hmac.New(sha256.New, a.key)
	mac.Write(tokenCanonical(string(payloadRaw)))
	if subtle.ConstantTimeCompare(got, mac.Sum(nil)) != 1 {
		return "", 0, 0, "", false
	}

	fields := strings.Split(string(payloadRaw), "|")
	if len(fields) != 4 {
		return "", 0, 0, "", false
	}
	accountID, err = strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return "", 0, 0, "", false
	}
	expiresUnix, err = strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return "", 0, 0, "", false
	}
	return TokenScope(fields[0]), accountID, expiresUnix, fields[3], true
}

// pruneLocked drops the account's expired grants. Called under mu.
func (a *tokenAuthority) pruneLocked(accountID int64) {
	now := a.now()
	for nonce := range a.byAccount[accountID] {
		if g, ok := a.grants[nonce]; !ok || now.After(g.expires) {
			a.dropLocked(nonce)
		}
	}
}

// evictOldestLocked removes the account's oldest-expiring grant. Called under
// mu, only when the cap is reached.
func (a *tokenAuthority) evictOldestLocked(accountID int64) {
	var oldest string
	var oldestAt time.Time
	for nonce := range a.byAccount[accountID] {
		g := a.grants[nonce]
		if oldest == "" || g.expires.Before(oldestAt) {
			oldest, oldestAt = nonce, g.expires
		}
	}
	if oldest != "" {
		a.dropLocked(oldest)
	}
}

// dropLocked removes one grant from both indexes. Called under mu.
func (a *tokenAuthority) dropLocked(nonce string) {
	if g, ok := a.grants[nonce]; ok {
		if per := a.byAccount[g.accountID]; per != nil {
			delete(per, nonce)
			if len(per) == 0 {
				delete(a.byAccount, g.accountID)
			}
		}
	}
	delete(a.grants, nonce)
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

// requireAuthOrToken authenticates a route that accepts EITHER Basic (the
// primary scheme — Bulwark, curl, any RFC 8620 client) OR a scoped token in
// the query string (the PWA's header-less contexts).
//
// The precedence rule: an Authorization header always wins, and a request
// with NEITHER gets the ordinary Basic challenge. A request that presents
// ONLY a token and fails gets 403 WITHOUT a WWW-Authenticate challenge —
// answering 401+challenge would make every expired <img> token pop the
// browser's native credential dialog, which is the exact failure mode the
// imgproxy avoided the same way.
func (s *Server) requireAuthOrToken(scope TokenScope, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get(TokenQueryParam)
		if r.Header.Get("Authorization") != "" || token == "" {
			s.requireAuth(next)(w, r)
			return
		}

		accountID, err := s.tokens.Verify(token, scope)
		if err != nil {
			writeGenericProblem(w, http.StatusForbidden, "invalid, expired or revoked token")
			return
		}
		// The store is re-consulted on EVERY tokened request, exactly as
		// requireProvisioned does for Basic: disabling or deleting the account
		// takes effect on the next request, not at the token's TTL.
		id, ok := s.auth.requireProvisionedByID(r.Context(), w, accountID)
		if !ok {
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	}
}

// InvalidateAccountTokens revokes every outstanding token for an account.
// Callers that invalidate a user's cached credentials (account disabled,
// credential rotated upstream) should call this alongside
// Authenticator.InvalidateUser so both artifacts die together.
func (s *Server) InvalidateAccountTokens(accountID int64) {
	s.tokens.RevokeAccount(accountID)
}

// mintRequest and mintResponse are the wire shapes of POST /jmap/token.
type mintRequest struct {
	Scopes []string `json:"scopes"`
}

type mintedToken struct {
	Token string `json:"token"`
	// ExpiresIn is seconds until expiry, relative on purpose: the client
	// schedules its refresh from this and never needs a synchronized clock.
	ExpiresIn int64 `json:"expiresIn"`
}

type mintResponse struct {
	Tokens map[string]mintedToken `json:"tokens"`
}

// handleTokenMint serves POST /jmap/token. Authenticated by the route table;
// tokens are minted for the CALLER's account only — there is nothing in the
// request that could name another one.
func (s *Server) handleTokenMint(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromContext(r.Context())
	if !ok {
		writeGenericProblem(w, http.StatusInternalServerError, "authentication context missing")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		writeGenericProblem(w, http.StatusBadRequest, "unreadable request body")
		return
	}
	var req mintRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeGenericProblem(w, http.StatusBadRequest, "the request body is not valid JSON")
		return
	}
	if len(req.Scopes) == 0 || len(req.Scopes) > maxScopesPerMint {
		writeGenericProblem(w, http.StatusBadRequest,
			fmt.Sprintf(`"scopes" must name between 1 and %d scopes`, maxScopesPerMint))
		return
	}

	// Deduplicate and validate BEFORE minting anything: a request naming an
	// unknown scope mints nothing, rather than half of what it asked.
	scopes := make([]TokenScope, 0, len(req.Scopes))
	seen := make(map[TokenScope]bool)
	for _, raw := range req.Scopes {
		scope := TokenScope(raw)
		if scope != ScopePush && scope != ScopeBlob {
			writeGenericProblem(w, http.StatusBadRequest,
				fmt.Sprintf("unknown scope %q", raw))
			return
		}
		if !seen[scope] {
			seen[scope] = true
			scopes = append(scopes, scope)
		}
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i] < scopes[j] })

	resp := mintResponse{Tokens: make(map[string]mintedToken, len(scopes))}
	for _, scope := range scopes {
		token, ttl, mintErr := s.tokens.Mint(id.Account.ID, scope)
		if mintErr != nil {
			s.log.Error("jmaphttp: minting a token failed",
				"account_id", id.Account.ID, "scope", scope, "error", mintErr)
			writeGenericProblem(w, http.StatusInternalServerError, "minting failed")
			return
		}
		resp.Tokens[string(scope)] = mintedToken{Token: token, ExpiresIn: int64(ttl / time.Second)}
	}

	// A response carrying capabilities must never be cached by anything.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

// revokeRequest is the wire shape of POST /jmap/token/revoke.
type revokeRequest struct {
	Tokens []string `json:"tokens"`
}

// handleTokenRevoke serves POST /jmap/token/revoke: the sign-out path. It
// revokes only tokens that verify AND belong to the caller's account, and it
// answers 204 regardless — a revocation is idempotent, and distinguishing
// "was revoked" from "was already dead" tells a caller nothing actionable.
func (s *Server) handleTokenRevoke(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromContext(r.Context())
	if !ok {
		writeGenericProblem(w, http.StatusInternalServerError, "authentication context missing")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeGenericProblem(w, http.StatusBadRequest, "unreadable request body")
		return
	}
	var req revokeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeGenericProblem(w, http.StatusBadRequest, "the request body is not valid JSON")
		return
	}
	if len(req.Tokens) > maxTokensPerRevoke {
		writeGenericProblem(w, http.StatusBadRequest,
			fmt.Sprintf("at most %d tokens per request", maxTokensPerRevoke))
		return
	}
	for _, token := range req.Tokens {
		s.tokens.Revoke(token, id.Account.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// requireProvisionedByID is requireProvisioned keyed by account id — the
// token path's identity resolution. Same contract: 403 for missing or
// disabled, 503 for a store failure, and the store consulted every time.
func (a *Authenticator) requireProvisionedByID(ctx context.Context, w http.ResponseWriter, accountID int64) (*Identity, bool) {
	acct, err := a.directory.GetAccount(ctx, accountID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// The account was deleted after the token was minted. The generic
		// wording matches the invalid-token answer on purpose: existence of
		// account ids is nobody's business.
		writeGenericProblem(w, http.StatusForbidden, "invalid, expired or revoked token")
		return nil, false
	case err != nil:
		a.log.Error("jmaphttp: account lookup failed", "account_id", accountID, "error", err)
		writeGenericProblem(w, http.StatusServiceUnavailable, "account lookup failed")
		return nil, false
	}
	if acct.State == store.AccountDisabled {
		writeGenericProblem(w, http.StatusForbidden, "this account is disabled in Moov")
		return nil, false
	}
	return &Identity{Account: acct, AccountID: jmap.EncodeAccountID(acct.ID)}, true
}
