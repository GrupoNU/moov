package jmaphttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/branding"
)

// The brand administration API (L2-brand-admin, epic BA-1): the authenticated
// routes a Settings -> Brand screen calls to edit the brand of the host the
// admin is on. Everything it writes is exactly what `moovctl branding` writes,
// through the same writer (internal/branding), so the CLI stays the
// operator's tool and the on-disk format does not change.
//
// # Who may write
//
// A BrandAdminSource answers "is this mailbox a brand admin of this host?".
// The first source is the operator-granted list in the host's own
// branding.json (`moovctl branding grant`); a second, Mailcow's domain admins,
// is designed (L2-brand-admin §2) and slots in behind the same interface —
// the composite below ORs its sources, and a test drives it with a fake
// second provider. Authorization is evaluated server-side on EVERY request
// from the store's cached read of the file, never from a client claim, and a
// provider that fails DENIES: a branding write is never urgent enough to
// guess.
//
// # Why a non-admin gets a 404
//
// The public brand document is built so that a hostname with no
// configuration is indistinguishable from one configured to look like Moov.
// The admin routes keep that property from the other side: an authenticated
// user who is not an admin of the host gets the same generic 404 an unknown
// route gets, so they learn neither whether the host is configured nor that
// the feature exists here. When the operator disables the API
// (MOOV_BRANDING_ADMIN=0) or configured no branding directory, every route
// answers that same 404.
//
// # Why there is no {host} in the URL
//
// The design doc's paths carried the host. They do not here: an admin edits
// the brand of the host they are ON, resolved from the Host header exactly as
// GET /branding resolves it. That removes a whole class of cross-host bugs
// (a body for host A written under host B) and makes the authorization
// question a single one — "are you an admin of where you are?".
//
// # CSRF
//
// Authentication is an Authorization header the PWA attaches itself (Basic,
// or nothing — there are no cookies anywhere in this server). A browser
// never adds that header to a cross-site request on its own, so a hostile
// page cannot make an admin's browser issue a write here; the CORS
// allow-list additionally refuses to read anything back from a foreign
// origin. There is therefore no CSRF token to carry.

// The admin routes. All authenticated; all under /branding/admin.
const (
	// PathBrandingAdmin answers whether the caller may edit this host's brand.
	// GET -> 200 {"host","canEdit":true} for an admin; 404 otherwise.
	PathBrandingAdmin = "/branding/admin"

	// PathBrandingAdminBrand reads (GET) or partially updates (PUT) the text
	// fields and colors.
	PathBrandingAdminBrand = "/branding/admin/brand"

	// PathBrandingAdminAsset uploads (PUT, raw image body) or removes (DELETE)
	// one asset; {kind} is logo, logoDark, icon or splash.
	PathBrandingAdminAsset = "/branding/admin/assets/{kind}"

	// PathBrandingAdminReset returns the host to Moov's brand (POST), keeping
	// the admin list so the caller keeps access.
	PathBrandingAdminReset = "/branding/admin/reset"
)

// Limits of the admin API.
const (
	// maxBrandAdminBodyBytes caps a PUT /brand body. The largest legal
	// document is a few hundred bytes; 64 KiB is room for any client's
	// whitespace and nothing more.
	maxBrandAdminBodyBytes = 64 << 10

	// brandAdminWritesPerMinute is the per-actor write budget: a token bucket
	// of this many, refilled at this rate. A person editing a brand makes a
	// handful of writes; a script in a loop is what this is for.
	brandAdminWritesPerMinute = 10
)

// BrandAdminSource answers whether a mailbox may administer a host's brand.
//
// One method, so a provider that talks to an external system (Mailcow) can be
// pinned by test to make exactly one kind of call. host is the normalized
// hostname (resolveBrandingHost); mailbox is the authenticated account's
// address, lowercased. An error means "could not decide", which the caller
// treats as a denial.
type BrandAdminSource interface {
	IsBrandAdmin(ctx context.Context, host, mailbox string) (bool, error)
}

// fileBrandAdmins is provider 1: the brandAdmins list in the host's own
// branding.json, read through the branding store so it rides the same 60 s
// cache as the document and is invalidated with it by every write.
type fileBrandAdmins struct {
	store *brandingStore
}

func (f fileBrandAdmins) IsBrandAdmin(_ context.Context, host, mailbox string) (bool, error) {
	e := f.store.resolveEntry(host)
	for _, a := range e.admins {
		if a == mailbox {
			return true, nil
		}
	}
	return false, nil
}

// compositeBrandAdmins ORs its sources in order. The first "yes" wins without
// consulting the rest — so an operator-granted admin keeps access while a
// later provider is unreachable — and the first error denies: a provider that
// cannot answer is never skipped over to ask the next.
type compositeBrandAdmins []BrandAdminSource

func (c compositeBrandAdmins) IsBrandAdmin(ctx context.Context, host, mailbox string) (bool, error) {
	for _, src := range c {
		ok, err := src.IsBrandAdmin(ctx, host, mailbox)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// brandAdminAPI is the server-side state of the admin routes.
type brandAdminAPI struct {
	// enabled is false when the operator disabled the API or configured no
	// branding directory; every route then answers 404.
	enabled bool
	store   *brandingStore
	source  BrandAdminSource
	log     brandAdminLogger

	// limiter budgets writes per actor.
	limiter *writeLimiter

	// hostMu serializes read-modify-write of one host's branding.json. The
	// CLI is not covered by it (a different process); two panels on the same
	// host are.
	mu     sync.Mutex
	hostMu map[string]*sync.Mutex
}

// brandAdminLogger is the slice of slog the API uses, so a test can capture
// the audit and denial lines without a real handler.
type brandAdminLogger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

func newBrandAdminAPI(cfg *Config, store *brandingStore, logger brandAdminLogger) *brandAdminAPI {
	sources := compositeBrandAdmins{fileBrandAdmins{store: store}}
	sources = append(sources, cfg.BrandAdminSources...)
	return &brandAdminAPI{
		enabled: !cfg.DisableBrandingAdmin && store != nil && store.dir != "",
		store:   store,
		source:  sources,
		log:     logger,
		limiter: newWriteLimiter(brandAdminWritesPerMinute, time.Minute, store.now),
		hostMu:  make(map[string]*sync.Mutex),
	}
}

// lockHost returns the mutex for one host, creating it on first use. The map
// is bounded by the number of hosts admins actually write to, which is the
// number of CONFIGURED hosts: an unconfigured host never gets past authorize.
func (a *brandAdminAPI) lockHost(host string) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	m, ok := a.hostMu[host]
	if !ok {
		m = &sync.Mutex{}
		a.hostMu[host] = m
	}
	return m
}

// brandAdminCall is what every admin handler receives once the caller is
// known to be an admin of the host: the resolved host, the actor's mailbox
// and the directory to write.
type brandAdminCall struct {
	host  string
	actor string
	dir   branding.Dir
}

// brandAdminRoute wraps a handler in the authorization gate. It runs INSIDE
// requireAuth (the route table's default), so an anonymous caller got the
// ordinary 401 before reaching here, exactly as on every protected route.
func (s *Server) brandAdminRoute(next func(http.ResponseWriter, *http.Request, brandAdminCall)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := identityFromContext(r.Context())
		if !ok {
			writeGenericProblem(w, http.StatusInternalServerError, "authentication context missing")
			return
		}
		call, ok := s.brandAdmin.authorize(r.Context(), r.Host, id.Account.Email)
		if !ok {
			// The same body an unknown resource gets anywhere on this server:
			// a non-admin learns nothing (see the file comment).
			writeGenericProblem(w, http.StatusNotFound, "not found")
			return
		}
		next(w, r, call)
	}
}

// authorize decides one request. Every "no" is the same "no" to the caller;
// the reasons differ only in the log, and only the provider failure is logged
// (it is an outage, not a user).
func (a *brandAdminAPI) authorize(ctx context.Context, rawHost, email string) (brandAdminCall, bool) {
	if a == nil || !a.enabled {
		return brandAdminCall{}, false
	}
	host := resolveBrandingHost(rawHost)
	actor, ok := branding.NormalizeMailbox(email)
	if host == "" || !ok {
		return brandAdminCall{}, false
	}
	isAdmin, err := a.source.IsBrandAdmin(ctx, host, actor)
	if err != nil {
		a.log.Warn("branding admin: authorization provider failed; denying",
			"host", host, "actor", actor, "error", err)
		return brandAdminCall{}, false
	}
	if !isAdmin {
		return brandAdminCall{}, false
	}
	dir, err := branding.HostDir(a.store.dir, host)
	if err != nil {
		return brandAdminCall{}, false
	}
	return brandAdminCall{host: host, actor: actor, dir: dir}, true
}

// brandAdminWrite wraps a WRITE handler in the per-host lock around its
// read-modify-write. The per-actor budget is NOT taken here: a request refused
// for its shape (a bad color, an SVG) writes nothing and costs nothing, so
// each handler charges it — chargeBrandWrite — at the moment it is about to
// touch the directory.
func (s *Server) brandAdminWrite(next func(http.ResponseWriter, *http.Request, brandAdminCall)) http.HandlerFunc {
	return s.brandAdminRoute(func(w http.ResponseWriter, r *http.Request, c brandAdminCall) {
		mu := s.brandAdmin.lockHost(c.host)
		mu.Lock()
		defer mu.Unlock()
		next(w, r, c)
	})
}

// chargeBrandWrite takes one token from the actor's write budget, or answers
// 429 with a Retry-After and reports false. Called by every write handler
// once the request has been validated and is about to change the directory.
func (s *Server) chargeBrandWrite(w http.ResponseWriter, c brandAdminCall) bool {
	wait, ok := s.brandAdmin.limiter.allow(c.actor)
	if ok {
		return true
	}
	secs := int(math.Ceil(wait.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"reason": fmt.Sprintf("too many brand writes; try again in %d s", secs),
	})
	return false
}

// --- the handlers ------------------------------------------------------------

// handleBrandAdminProbe serves GET /branding/admin.
func (s *Server) handleBrandAdminProbe(w http.ResponseWriter, _ *http.Request, c brandAdminCall) {
	writeJSON(w, http.StatusOK, map[string]any{"host": c.host, "canEdit": true})
}

// handleBrandAdminGet serves GET /branding/admin/brand.
func (s *Server) handleBrandAdminGet(w http.ResponseWriter, _ *http.Request, c brandAdminCall) {
	s.writeBrandAdminDoc(w, c)
}

// brandAdminPatch is the PUT /brand body: every field optional, absent means
// unchanged, "" means clear (a color cleared goes back to Moov's default).
// Unknown fields are refused, so a client's typo ("color" for "colors") is a 400 rather
// than a silent no-op.
type brandAdminPatch struct {
	Name       *string `json:"name"`
	ShortName  *string `json:"shortName"`
	Tagline    *string `json:"tagline"`
	SupportURL *string `json:"supportUrl"`
	PrivacyURL *string `json:"privacyUrl"`
	TermsURL   *string `json:"termsUrl"`
	Colors     *struct {
		Primary    *string `json:"primary"`
		OnPrimary  *string `json:"onPrimary"`
		SplashFrom *string `json:"splashFrom"`
		SplashTo   *string `json:"splashTo"`
	} `json:"colors"`
}

// brandAdminFieldError is the 400 body: the FIRST invalid field and why.
type brandAdminFieldError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// handleBrandAdminPut serves PUT /branding/admin/brand.
func (s *Server) handleBrandAdminPut(w http.ResponseWriter, r *http.Request, c brandAdminCall) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"reason": "Content-Type must be application/json"})
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBrandAdminBodyBytes))
	dec.DisallowUnknownFields()
	var patch brandAdminPatch
	if err := dec.Decode(&patch); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"reason": "the body is too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, brandAdminFieldError{Field: "", Reason: "the body is not a valid brand document: " + err.Error()})
		return
	}

	file, err := c.dir.Read()
	if err != nil {
		s.brandAdminStoreError(w, c, "reading the brand", err)
		return
	}
	changed, ferr := applyBrandAdminPatch(&file, patch)
	if ferr != nil {
		// Validated in full before anything was written: a 400 here means the
		// directory is exactly as it was.
		writeJSON(w, http.StatusBadRequest, ferr)
		return
	}
	if !s.chargeBrandWrite(w, c) {
		return
	}
	body, err := c.dir.Write(file)
	if err != nil {
		s.brandAdminStoreError(w, c, "writing the brand", err)
		return
	}
	s.brandAdmin.store.invalidate(c.host)
	s.brandAdmin.audit(c, "put-brand", "fields", strings.Join(changed, ","), len(body), sha256Hex(body))
	s.writeBrandAdminDoc(w, c)
}

// applyBrandAdminPatch validates every field of the patch against the rules
// the CLI applies, and only then applies them. It returns the names of the
// fields that were set (for the audit line) or the first error.
func applyBrandAdminPatch(file *branding.File, p brandAdminPatch) ([]string, *brandAdminFieldError) {
	type textField struct {
		name     string
		value    *string
		target   *string
		maxRunes int
		url      bool
	}
	fields := []textField{
		{"name", p.Name, &file.Name, branding.MaxNameRunes, false},
		{"shortName", p.ShortName, &file.ShortName, branding.MaxShortNameRunes, false},
		{"tagline", p.Tagline, &file.Tagline, branding.MaxTaglineRunes, false},
		{"supportUrl", p.SupportURL, &file.SupportURL, 0, true},
		{"privacyUrl", p.PrivacyURL, &file.PrivacyURL, 0, true},
		{"termsUrl", p.TermsURL, &file.TermsURL, 0, true},
	}
	type colorField struct {
		name   string
		value  *string
		target *string
	}
	var colors []colorField
	if p.Colors != nil {
		colors = []colorField{
			{"colors.primary", p.Colors.Primary, &file.Colors.Primary},
			{"colors.onPrimary", p.Colors.OnPrimary, &file.Colors.OnPrimary},
			{"colors.splashFrom", p.Colors.SplashFrom, &file.Colors.SplashFrom},
			{"colors.splashTo", p.Colors.SplashTo, &file.Colors.SplashTo},
		}
	}

	// Pass 1: validate everything; nothing is assigned yet.
	pending := make(map[string]string)
	var changed []string
	for _, f := range fields {
		if f.value == nil {
			continue
		}
		v := strings.TrimSpace(*f.value)
		if hasControlRunes(v) {
			return nil, &brandAdminFieldError{Field: f.name, Reason: "must not contain control characters"}
		}
		switch {
		case f.url && v != "" && !branding.SafeURL(v):
			return nil, &brandAdminFieldError{Field: f.name, Reason: "must start with https://, http:// or mailto:"}
		case f.maxRunes > 0 && branding.RuneLen(v) > f.maxRunes:
			return nil, &brandAdminFieldError{Field: f.name,
				Reason: fmt.Sprintf("is %d characters; the limit is %d", branding.RuneLen(v), f.maxRunes)}
		case f.url && len(v) > 2048:
			return nil, &brandAdminFieldError{Field: f.name, Reason: "is longer than 2048 characters"}
		}
		pending[f.name] = v
		changed = append(changed, f.name)
	}
	for _, cf := range colors {
		if cf.value == nil {
			continue
		}
		v := strings.TrimSpace(*cf.value)
		if v != "" {
			if v = branding.NormalizeHexColor(v); v == "" {
				return nil, &brandAdminFieldError{Field: cf.name, Reason: "is not a CSS hex color (#rgb or #rrggbb)"}
			}
		}
		pending[cf.name] = v
		changed = append(changed, cf.name)
	}

	// Pass 2: apply.
	for _, f := range fields {
		if v, ok := pending[f.name]; ok {
			*f.target = v
		}
	}
	for _, cf := range colors {
		if v, ok := pending[cf.name]; ok {
			*cf.target = v
		}
	}
	return changed, nil
}

func hasControlRunes(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// handleBrandAdminPutAsset serves PUT /branding/admin/assets/{kind}.
func (s *Server) handleBrandAdminPutAsset(w http.ResponseWriter, r *http.Request, c brandAdminCall) {
	kind, ok := branding.ParseAssetKind(r.PathValue("kind"))
	if !ok {
		writeGenericProblem(w, http.StatusNotFound, "not found")
		return
	}
	// The declared type must at least CLAIM to be an image; the bytes decide
	// the rest. A form post or a JSON body here is a client bug worth a
	// precise answer.
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || !strings.HasPrefix(mt, "image/") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"reason": "Content-Type must be image/*"})
		return
	}
	// One byte past the cap tells "at the cap" from "over it" without
	// buffering an unbounded body.
	body, err := io.ReadAll(io.LimitReader(r.Body, branding.MaxAssetBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"reason": "reading the body failed"})
		return
	}
	if len(body) > branding.MaxAssetBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"reason": branding.ErrAssetTooLarge.Error()})
		return
	}

	file, err := c.dir.Read()
	if err != nil {
		s.brandAdminStoreError(w, c, "reading the brand", err)
		return
	}
	// The content verdict BEFORE the budget is charged: a refused upload
	// writes nothing and costs nothing. StoreAsset re-applies the same
	// checks; they are the writer's own functions, so the two cannot differ.
	switch _, isImage := branding.SniffImageType(body); {
	case len(body) == 0:
		writeJSON(w, http.StatusBadRequest, map[string]any{"reason": branding.ErrEmptyAsset.Error()})
		return
	case !isImage && branding.LooksLikeSVG(body):
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"reason": branding.ErrSVG.Error()})
		return
	case !isImage:
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"reason": branding.ErrNotImage.Error()})
		return
	}
	if !s.chargeBrandWrite(w, c) {
		return
	}
	stored, err := c.dir.StoreAsset(kind, body)
	if err != nil {
		switch {
		case errors.Is(err, branding.ErrEmptyAsset):
			writeJSON(w, http.StatusBadRequest, map[string]any{"reason": err.Error()})
		case errors.Is(err, branding.ErrAssetTooLarge):
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"reason": err.Error()})
		case errors.Is(err, branding.ErrSVG), errors.Is(err, branding.ErrNotImage):
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"reason": err.Error()})
		default:
			s.brandAdminStoreError(w, c, "storing the asset", err)
		}
		return
	}
	*kind.Field(&file) = stored
	if _, err := c.dir.Write(file); err != nil {
		s.brandAdminStoreError(w, c, "writing the brand", err)
		return
	}
	s.brandAdmin.store.invalidate(c.host)
	s.brandAdmin.audit(c, "put-asset", "kind", string(kind), len(body), sha256Hex(body))
	s.writeBrandAdminDoc(w, c)
}

// handleBrandAdminDeleteAsset serves DELETE /branding/admin/assets/{kind}.
// Idempotent: deleting an asset that is not configured is a 200 with the
// unchanged document.
func (s *Server) handleBrandAdminDeleteAsset(w http.ResponseWriter, r *http.Request, c brandAdminCall) {
	kind, ok := branding.ParseAssetKind(r.PathValue("kind"))
	if !ok {
		writeGenericProblem(w, http.StatusNotFound, "not found")
		return
	}
	file, err := c.dir.Read()
	if err != nil {
		s.brandAdminStoreError(w, c, "reading the brand", err)
		return
	}
	field := kind.Field(&file)
	if name := *field; name != "" {
		if !s.chargeBrandWrite(w, c) {
			return
		}
		if err := c.dir.RemoveAsset(name); err != nil {
			s.brandAdminStoreError(w, c, "removing the asset", err)
			return
		}
		*field = ""
		body, err := c.dir.Write(file)
		if err != nil {
			s.brandAdminStoreError(w, c, "writing the brand", err)
			return
		}
		s.brandAdmin.store.invalidate(c.host)
		s.brandAdmin.audit(c, "delete-asset", "kind", string(kind), len(body), sha256Hex(body))
	}
	s.writeBrandAdminDoc(w, c)
}

// handleBrandAdminReset serves POST /branding/admin/reset: back to Moov's
// brand, admin list preserved (branding.Dir.Reset).
func (s *Server) handleBrandAdminReset(w http.ResponseWriter, _ *http.Request, c brandAdminCall) {
	if !s.chargeBrandWrite(w, c) {
		return
	}
	_, body, err := c.dir.Reset()
	if err != nil {
		s.brandAdminStoreError(w, c, "resetting the brand", err)
		return
	}
	s.brandAdmin.store.invalidate(c.host)
	s.brandAdmin.audit(c, "reset", "fields", "all", len(body), sha256Hex(body))
	s.writeBrandAdminDoc(w, c)
}

// brandAdminStoreError is the one 500 the API has: the filesystem failed.
// The cause is logged with the host and actor; the caller gets the generic
// problem body, because a path on the server's disk is not theirs to read.
func (s *Server) brandAdminStoreError(w http.ResponseWriter, c brandAdminCall, what string, err error) {
	s.brandAdmin.log.Warn("branding admin: "+what+" failed", "host", c.host, "actor", c.actor, "error", err)
	writeGenericProblem(w, http.StatusInternalServerError, what+" failed")
}

// audit is the one log line every write leaves: host, actor, action, what
// changed, how many bytes, and their digest. Never the bytes themselves, and
// never a URL value — a customer's support link is theirs, not the log's.
func (a *brandAdminAPI) audit(c brandAdminCall, action, whatKey, what string, n int, sum string) {
	a.log.Info("branding admin: write",
		"host", c.host, "actor", c.actor, "action", action, whatKey, what, "bytes", n, "sha256", sum)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- the document ------------------------------------------------------------

// BrandAdminDoc is what every admin route returns: the configured document
// as the panel needs to render and edit it. Text fields are AS CONFIGURED
// (empty when unset, so the screen shows a placeholder rather than Moov's
// value as if the customer had typed it); colors are the EFFECTIVE values
// the public document serves, because a color picker needs a color.
type BrandAdminDoc struct {
	Host    string `json:"host"`
	Default bool   `json:"default"`

	Name       string `json:"name"`
	ShortName  string `json:"shortName"`
	Tagline    string `json:"tagline"`
	SupportURL string `json:"supportUrl"`
	PrivacyURL string `json:"privacyUrl"`
	TermsURL   string `json:"termsUrl"`

	Colors BrandingColors `json:"colors"`

	// Assets has one entry per kind, null when that kind is not configured
	// or its file is missing or invalid (in which case Warnings says so).
	Assets map[string]*BrandAdminAsset `json:"assets"`

	// IconSource is where the PWA icons come from: "icon", "logo" or
	// "default". IconIssue is non-empty when a configured source cannot be
	// rendered — the same sentence the daemon log carries.
	IconSource string `json:"iconSource"`
	IconIssue  string `json:"iconIssue"`

	// BrandAdmins is the operator-granted list. It is here — the caller IS
	// one of them — and nowhere in the public document.
	BrandAdmins []string `json:"brandAdmins"`

	// Warnings are the notes the CLI prints at write time, recomputed from
	// the files on every read: a logo the icons cannot be rendered from, an
	// icon that is not square, a recorded asset that is missing.
	Warnings []string `json:"warnings"`

	PublicURL   string            `json:"publicUrl"`
	ManifestURL string            `json:"manifestUrl"`
	IconURLs    map[string]string `json:"iconUrls"`

	// Version is the ETag of the public document, without its quotes: the
	// value a client compares to know whether its view is current.
	Version string `json:"version"`
}

// BrandAdminAsset describes one stored asset. URL carries a cache-buster
// (?v=<digest prefix>) so a screen that just uploaded can re-fetch past the
// browser's cache of the previous file; the public asset route ignores the
// query string.
type BrandAdminAsset struct {
	URL    string `json:"url"`
	Bytes  int    `json:"bytes"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

func (s *Server) writeBrandAdminDoc(w http.ResponseWriter, c brandAdminCall) {
	doc, err := s.brandAdminDoc(c)
	if err != nil {
		s.brandAdminStoreError(w, c, "reading the brand", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, doc)
}

// brandAdminDoc assembles the document from the file (as configured) and the
// store's resolution of it (as served).
func (s *Server) brandAdminDoc(c brandAdminCall) (BrandAdminDoc, error) {
	file, err := c.dir.Read()
	if err != nil {
		return BrandAdminDoc{}, err
	}
	entry := s.branding.resolveEntry(c.host)

	doc := BrandAdminDoc{
		Host:        c.host,
		Default:     entry.doc.Default,
		Name:        strings.TrimSpace(file.Name),
		ShortName:   strings.TrimSpace(file.ShortName),
		Tagline:     strings.TrimSpace(file.Tagline),
		SupportURL:  strings.TrimSpace(file.SupportURL),
		PrivacyURL:  strings.TrimSpace(file.PrivacyURL),
		TermsURL:    strings.TrimSpace(file.TermsURL),
		Colors:      entry.doc.Colors,
		Assets:      make(map[string]*BrandAdminAsset, len(branding.AssetKinds)),
		IconSource:  entry.iconSource,
		IconIssue:   entry.iconIssue,
		BrandAdmins: append([]string{}, entry.admins...),
		Warnings:    []string{},
		PublicURL:   PathBranding,
		ManifestURL: PathBrandingManifest,
		IconURLs:    make(map[string]string, len(brandingIconSpecs)),
		Version:     strings.Trim(entry.etag, `"`),
	}
	if doc.IconSource == "" {
		doc.IconSource = "default"
	}
	sort.Strings(doc.BrandAdmins)

	for _, kind := range branding.AssetKinds {
		name := strings.TrimSpace(*kind.Field(&file))
		doc.Assets[string(kind)] = nil
		if name == "" {
			continue
		}
		body, _, err := s.branding.openAsset(c.host, safeAssetName(name))
		if err != nil {
			doc.Warnings = append(doc.Warnings,
				fmt.Sprintf("the configured %s %q is missing or is not a valid image", kind, name))
			continue
		}
		sum := sha256Hex(body)
		asset := &BrandAdminAsset{
			URL:   brandingAssetURL(c.host, name) + "?v=" + sum[:16],
			Bytes: len(body),
		}
		if w, h, ok := branding.ImageDimensions(body); ok {
			asset.Width, asset.Height = w, h
		}
		doc.Assets[string(kind)] = asset
		doc.Warnings = append(doc.Warnings, branding.IconSourceNotes(kind, body)...)
	}

	// The icon URLs get a buster derived from what they are rendered from, so
	// a screen re-fetches them after an upload and not otherwise.
	iconV := ""
	if entry.iconSum != "" {
		iconV = "?v=" + sha256Hex([]byte(entry.iconSum + "\x00" + entry.doc.Colors.Primary))[:16]
	}
	for _, spec := range brandingIconSpecs {
		doc.IconURLs[spec.name] = strings.Replace(PathBrandingIcon, "{file}", spec.name+".png", 1) + iconV
	}
	return doc, nil
}

// --- the write budget ----------------------------------------------------------

// writeLimiter is a per-key token bucket: `burst` tokens, refilled linearly
// over `window`. Small and local because nothing else in the server needs a
// rate (as opposed to a concurrency) limit yet; the auth lockout is a
// different shape (per failure, exponential).
type writeLimiter struct {
	mu      sync.Mutex
	burst   float64
	perSec  float64
	now     func() time.Time
	buckets map[string]*tokenBucket
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newWriteLimiter(burst int, window time.Duration, now func() time.Time) *writeLimiter {
	if now == nil {
		now = time.Now
	}
	return &writeLimiter{
		burst:   float64(burst),
		perSec:  float64(burst) / window.Seconds(),
		now:     now,
		buckets: make(map[string]*tokenBucket),
	}
}

// allow takes one token for key, or reports how long until one is available.
func (l *writeLimiter) allow(key string) (time.Duration, bool) {
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
	wait := time.Duration((1 - b.tokens) / l.perSec * float64(time.Second))
	return wait, false
}
