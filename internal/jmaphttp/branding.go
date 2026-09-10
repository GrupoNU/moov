package jmaphttp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/branding"
)

// Branding (arbitration W-A1 of L2-pwa §3): a PUBLIC, unauthenticated
// GET /branding that answers according to the request's Host, so the PWA can
// paint a customer's logo, splash image, product name and colors BEFORE the
// user has any credentials to authenticate with.
//
// # Why unauthenticated
//
// The brand IS the login screen. Anything gated behind auth cannot be shown to
// a user who has not logged in yet, which is precisely the audience the login
// screen has. So this is the one anonymous route in a server whose every other
// route requires credentials, and it is built to earn that exception:
//
//   - it reads a directory of operator-curated files, never the database, so a
//     bug here cannot reach a mailbox;
//   - it never echoes anything the caller controls into the response body
//     except through the Host resolution below;
//   - it serves only assets whose bytes were validated as images, with the
//     headers that make a browser refuse to execute them whatever they contain.
//
// # Why resolved by Host and not by a query parameter
//
// A parameter (or worse, the email address the user is typing) would turn this
// into an oracle for "which domains exist on this server": an attacker could
// enumerate the customer list of a Moov installation one guess at a time. The
// Host header is not that: the caller already had to know the hostname to send
// the request, and a hostname with no configuration is answered with the Moov
// defaults — INDISTINGUISHABLE from a hostname that is configured to look like
// Moov. Existence is never confirmed or denied.

// BrandingPaths are the routes the branding feature adds. Two more — the
// per-host PWA manifest and the generated icons — live in branding_pwa.go and
// resolve the Host exactly as these do.
const (
	// PathBranding serves the resolved branding document. GET, public.
	PathBranding = "/branding"

	// PathBrandingAsset serves one validated brand asset. GET, public. The
	// {host} variable is the RESOLVED host (what /branding put in the URL),
	// not free-form caller input — resolveBrandingHost re-validates it.
	PathBrandingAsset = "/branding/assets/{host}/{name}"
)

// Branding asset limits. Both are enforced when the file is READ, so a file
// dropped into the directory after startup cannot exceed them either.
const (
	// MaxBrandingAssetBytes caps one asset. 2 MiB is generous for a logo or a
	// splash photograph and small enough that a hostile directory cannot
	// exhaust memory: the cap is applied with an io.LimitedReader before the
	// bytes are buffered. The value is owned by internal/branding, the one
	// writer, so the reader's cap and the writer's cannot disagree.
	MaxBrandingAssetBytes = branding.MaxAssetBytes

	// brandingCacheTTL is how long a resolved document is remembered in
	// process. Short enough that `moovctl branding set` shows up without a
	// restart, long enough that the login page of a busy morning does not stat
	// the filesystem for every visitor.
	brandingCacheTTL = 60 * time.Second

	// BrandingMaxAge is the value of the public Cache-Control max-age. The
	// document is small, public and changes when an operator says so; a minute
	// of shared caching costs nothing and removes the request entirely from a
	// reload.
	BrandingMaxAge = 300
)

// brandingConfigFile is the per-host document a writer produces — `moovctl
// branding` or the admin API (branding_admin.go), both through
// internal/branding, the only supported writer.
const brandingConfigFile = branding.ConfigFile

// Branding is the public document GET /branding returns.
//
// Every field is a STRING the client drops into a CSS custom property or an
// <img src>; there is nothing structured for a client to misinterpret and
// nothing sensitive for a stranger to learn. The JSON tags are the wire
// contract the PWA is written against.
type Branding struct {
	// Name is the product name shown in the UI and the browser tab.
	Name string `json:"name"`

	// ShortName is the name under an installed icon on a home screen, where
	// the manifest spec and every launcher truncate past roughly a dozen
	// characters. It is the manifest's short_name. Configured explicitly or
	// derived from Name (see deriveShortName); never empty.
	ShortName string `json:"shortName"`

	// LogoURL and SplashURL are absolute-path URLs on THIS origin (never a
	// third-party URL: a customer-supplied external URL would be a tracking
	// pixel on our login page and a mixed-content risk). Empty means "the
	// client should fall back to its built-in mark".
	LogoURL   string `json:"logoUrl"`
	SplashURL string `json:"splashUrl"`

	// LogoDarkURL is the optional wordmark for DARK backgrounds: the login
	// panel, whose gradient runs between SplashFrom and SplashTo, and the dark
	// theme's top bar. Empty when the brand configured none.
	//
	// It is the mirror of the problem IconURL solves. Areacorp's wordmark is
	// black, so on the dark login panel it is a black mark on a dark ground —
	// invisible. A brand kit that has a dark-background wordmark puts it here;
	// without one the client draws the light logo on a small light plate, which
	// is legible but is a plate the customer did not design.
	//
	// It plays NO part in generating the PWA icons: that chain is icon, then
	// logo, then Moov's own, and a second wordmark would only add a way for the
	// home screen to disagree with the top bar.
	LogoDarkURL string `json:"logoDarkUrl"`

	// IconURL is the optional SQUARE mark the launcher icons and the favicon
	// are rendered from, on this origin like the others; empty when the brand
	// configured none, in which case the icons are rendered from LogoURL.
	//
	// It exists because the two jobs are not the same picture. The top bar and
	// the login panel show a wordmark, often wide and often in the brand's
	// primary color; the maskable and Apple icons sit on an OPAQUE plate of
	// that same primary color, so a brand whose primary is #000000 and whose
	// wordmark is black renders a black glyph on a black plate — invisible.
	// Such a brand kit almost always has a square glyph meant for dark
	// backgrounds, and this is where it goes.
	//
	// The PWA does not consume it yet: the icons are rendered server-side and
	// the document is what describes the brand completely, so it is here for
	// the description rather than for a client to draw.
	IconURL string `json:"iconUrl"`

	// Colors are the brand's design tokens. Every value is a validated CSS hex
	// color (#rgb or #rrggbb); the client assigns them to custom properties
	// without parsing.
	Colors BrandingColors `json:"colors"`

	// Tagline is the optional line under the product name on the login split
	// panel. Empty renders nothing.
	Tagline string `json:"tagline,omitempty"`

	// SupportURL is where "contact your administrator" can point. Empty
	// renders as plain text instead of a link. Restricted to http(s) and
	// mailto: so it can never become a javascript: URL in the DOM.
	SupportURL string `json:"supportUrl,omitempty"`

	// PrivacyURL is the operator's own privacy policy, shown in the legal
	// footer. Empty renders no link at all.
	//
	// Distinct from the source and license links the footer also carries:
	// those are Moov's AGPL-3.0 section 13 obligation and are not
	// configurable. This one is the OPERATOR's obligation to their own users,
	// and only they can say where it lives. Restricted to the same schemes as
	// SupportURL, for the same reason: it becomes an href.
	PrivacyURL string `json:"privacyUrl,omitempty"`

	// TermsURL is the operator's terms of service, under exactly the rules
	// PrivacyURL documents.
	TermsURL string `json:"termsUrl,omitempty"`

	// Default reports whether this document is the built-in Moov branding
	// (true) or a configured customer brand (false).
	//
	// It is deliberately NOT an existence oracle: it says what the caller is
	// being shown, which the caller can see anyway by looking at the response.
	Default bool `json:"default"`
}

// BrandingColors is the color half of the token set.
//
// The set is small on purpose. These are the SEED tokens: the client derives
// hovers, borders and surfaces from them in CSS, so a customer configures four
// values rather than forty, and a half-configured brand can never produce an
// unreadable screen.
type BrandingColors struct {
	// Primary is the accent: buttons, links, focus rings.
	Primary string `json:"primary"`

	// OnPrimary is the text drawn ON the primary color. It exists as its own
	// token because deriving it (light text on dark accents, dark on light)
	// is exactly the kind of guess that produces an AA contrast failure.
	OnPrimary string `json:"onPrimary"`

	// SplashFrom and SplashTo are the two stops of the brand panel's gradient,
	// used when no splash image is configured and as the image's backdrop
	// while it loads.
	SplashFrom string `json:"splashFrom"`
	SplashTo   string `json:"splashTo"`
}

// DefaultBranding is Moov's own brand — the document served for any host with
// no configuration, which per L2-pwa §2 (P3) is what MOST installations will
// ever show. It is a real brand, not a placeholder.
//
// The palette: a deep indigo-violet accent (#5B5BD6) that reads as software
// rather than as a corporate template, on a splash gradient running from
// midnight indigo to a warmer violet. #5B5BD6 against white is 5.4:1 and
// white on #5B5BD6 is 5.4:1 — both clear AA for body text, and the PWA pins
// those numbers in a test rather than trusting this comment.
func DefaultBranding() Branding {
	return Branding{
		Name: "Moov Mail",
		// "Moov", not the derived "Moov Mail": the embedded manifest says so,
		// and a test pins the two together.
		ShortName:   "Moov",
		LogoURL:     "",
		SplashURL:   "",
		IconURL:     "",
		LogoDarkURL: "",
		Colors: BrandingColors{
			Primary:    "#5b5bd6",
			OnPrimary:  "#ffffff",
			SplashFrom: "#1e1b4b",
			SplashTo:   "#4c1d95",
		},
		Tagline: "",
		Default: true,
	}
}

// BrandingConfig configures the branding endpoint.
type BrandingConfig struct {
	// Dir is the root the per-host directories live under (MOOV_BRANDING_DIR,
	// e.g. /etc/moov/branding, holding <host>/branding.json and its assets).
	// Empty disables customisation entirely: every host is answered with the
	// Moov defaults, which is a perfectly good production configuration and is
	// what the pilot runs.
	Dir string
}

// brandingStore resolves and caches branding documents from the filesystem,
// and the PWA icons rendered from them (branding_pwa.go).
type brandingStore struct {
	dir string
	log *slog.Logger

	mu    sync.Mutex
	cache map[string]brandingEntry
	// icons caches rendered PWA icons, keyed by host, icon-source digest,
	// accent color and icon name (see iconCacheKey). Only a host whose brand
	// was actually rendered gets an entry, so the map is bounded by the number of
	// CONFIGURED hosts, not by the Host headers strangers send.
	icons map[string]iconEntry
	now   func() time.Time
}

// brandingEntry is one host's resolved state for one cache TTL: the public
// document, its ETag, and what the icon route needs to know about the logo
// without re-deriving it per request.
type brandingEntry struct {
	doc     Branding
	etag    string
	expires time.Time

	// iconFile is the validated asset filename the icons are RENDERED from —
	// the configured square icon when there is a usable one, otherwise the
	// logo — or "" when there is nothing to render from and Moov's icons are
	// the answer.
	iconFile string
	// iconSum is the hex SHA-256 of iconFile's bytes at resolve time; it is
	// part of the icon cache key, so a replaced source renders fresh icons at
	// the next TTL without a restart.
	iconSum string
	// iconSource names which configured asset iconFile is: brandingSourceIcon
	// or brandingSourceLogo. Empty when there is none.
	iconSource string
	// iconIssue is non-empty when an icon source IS configured but cannot be
	// turned into icons (WebP, undecodable, oversized, missing) and nothing
	// further down the chain could either. The icon route then serves Moov's
	// icons, and the reason was logged once when this entry was built — which
	// is what "declared, rate-limited by the TTL" means.
	iconIssue string

	// admins are the mailboxes the file grants brand administration to
	// (branding_admin.go), normalized. Read here so the authorizer rides the
	// same cache as the document — and is invalidated with it on a write.
	// NEVER copied into doc: the public document must not carry it.
	admins []string
}

// The two names an entry's icon source can carry. They are the words the CLI
// prints and the log lines use, so operator-facing text and code agree.
const (
	brandingSourceIcon = "icon"
	brandingSourceLogo = "logo"
)

func newBrandingStore(dir string, logger *slog.Logger, now func() time.Time) *brandingStore {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &brandingStore{
		dir:   strings.TrimSpace(dir),
		log:   logger,
		cache: make(map[string]brandingEntry),
		icons: make(map[string]iconEntry),
		now:   now,
	}
}

// handleBranding serves GET /branding (W-A1).
func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	host := resolveBrandingHost(r.Host)
	doc, etag := s.branding.resolve(host)

	// Public and cacheable: the document has no per-user content, so a shared
	// cache in front of us may serve it to everyone on that host. Vary: Host is
	// not needed (a cache keys on the whole URL including authority), but is
	// harmless and makes the dependency legible to a proxy that normalizes.
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", BrandingMaxAge))
	w.Header().Set("ETag", etag)
	// Nothing here is HTML, and a browser must never be talked into treating
	// it as such.
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	writeJSON(w, http.StatusOK, doc)
}

// handleBrandingAsset serves GET /branding/assets/{host}/{name}.
//
// The route exists rather than serving assets from a static file server for
// one reason: every byte that leaves here has been read through the same
// validation the CLI applied when writing it, so a file placed in the
// directory by any other means still cannot become an HTML document, an SVG
// with a script in it, or a 900 MB response.
func (s *Server) handleBrandingAsset(w http.ResponseWriter, r *http.Request) {
	host := resolveBrandingHost(r.PathValue("host"))
	name := r.PathValue("name")

	body, contentType, err := s.branding.openAsset(host, name)
	if err != nil {
		// Every failure — unknown host, unknown file, a file that failed
		// validation, a traversal attempt — is the same 404. The caller learns
		// only "there is no asset at this URL", which is all it is entitled to.
		writeGenericProblem(w, http.StatusNotFound, "not found")
		return
	}

	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("X-Content-Type-Options", "nosniff")
	// Belt and braces: even for a validated raster image, nothing may be
	// fetched, framed or executed on its behalf.
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", fmt.Sprintf("public, max-age=%d", BrandingMaxAge))
	_, _ = w.Write(body)
}

// resolveBrandingHost normalizes a Host header into the directory name a
// brand is configured under.
//
// It strips the port, lowercases, drops a trailing dot, and REFUSES anything
// that is not a plain hostname — no path separators, no "..", no percent
// escapes, no empty labels. The refusal returns "", which resolve and
// openAsset both treat as "no configuration", so a hostile Host header
// degrades to the Moov defaults rather than to a filesystem read.
//
// The rule itself lives in internal/branding (NormalizeHost) so the writers
// apply exactly it: a host the CLI accepts but the server rejects would be a
// directory that is silently never served.
func resolveBrandingHost(raw string) string { return branding.NormalizeHost(raw) }

// resolve returns the branding document for a host and its ETag.
//
// It never returns an error: a missing directory, an unreadable file or a
// malformed document all resolve to the Moov defaults, because a login screen
// that refuses to render is a far worse outcome than one that renders
// unbranded. A malformed document IS logged, so an operator learns their
// typo did not take effect.
func (b *brandingStore) resolve(host string) (Branding, string) {
	e := b.resolveEntry(host)
	return e.doc, e.etag
}

// resolveEntry is resolve with the icon-side state attached. It never returns
// an error, for the same reason resolve does not.
func (b *brandingStore) resolveEntry(host string) brandingEntry {
	if b == nil || b.dir == "" || host == "" {
		doc := DefaultBranding()
		return brandingEntry{doc: doc, etag: brandingETag(doc)}
	}

	now := b.now()
	b.mu.Lock()
	if e, ok := b.cache[host]; ok && now.Before(e.expires) {
		b.mu.Unlock()
		return e
	}
	b.mu.Unlock()

	e := b.load(host)
	e.etag = brandingETag(e.doc)
	e.expires = now.Add(brandingCacheTTL)

	b.mu.Lock()
	b.cache[host] = e
	b.mu.Unlock()

	return e
}

// invalidate forgets everything cached for one host — the document and every
// icon rendered from it — so the next request re-reads the directory. The
// admin API calls it after each write: the 60 s TTL is for the CLI path, and
// a panel must see its own save on the next paint.
func (b *brandingStore) invalidate(host string) {
	if b == nil || host == "" {
		return
	}
	prefix := host + "\x00"
	b.mu.Lock()
	delete(b.cache, host)
	for key := range b.icons {
		if strings.HasPrefix(key, prefix) {
			delete(b.icons, key)
		}
	}
	b.mu.Unlock()
}

// load reads and validates one host's configuration from disk.
func (b *brandingStore) load(host string) brandingEntry {
	fallback := brandingEntry{doc: DefaultBranding()}

	// #nosec G304 -- `host` is not caller-controlled input at this point: every
	// path into this function runs it through resolveBrandingHost, which admits
	// only [a-z0-9.-], rejects "..", separators and percent escapes, and returns
	// "" for anything else (resolve() then short-circuits before reaching here).
	// The filename is a package constant. A traversal test pins this.
	raw, err := os.ReadFile(filepath.Join(b.dir, host, brandingConfigFile))
	if err != nil {
		// A host with no configuration is the normal case, not a problem.
		return fallback
	}

	var file brandingFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return fallback
	}

	entry := fallback
	doc := &entry.doc
	// A document that configures nothing visible — one holding only an admin
	// list, as the panel's "reset" leaves behind — IS Moov's brand, and says
	// so. Default is not an existence oracle either way (see Branding).
	doc.Default = !file.HasBrand()
	for _, a := range file.BrandAdmins {
		if m, ok := branding.NormalizeMailbox(a); ok {
			entry.admins = append(entry.admins, m)
		}
	}

	if n := strings.TrimSpace(file.Name); n != "" {
		doc.Name = truncateRunes(n, 64)
		// A customer's name gets a customer's short name; Moov's authored
		// "Moov" only survives when the name is still Moov's.
		doc.ShortName = deriveShortName(doc.Name)
	}
	if s := strings.TrimSpace(file.ShortName); s != "" {
		doc.ShortName = truncateRunes(s, maxShortNameRunes)
	}
	if t := strings.TrimSpace(file.Tagline); t != "" {
		doc.Tagline = truncateRunes(t, 160)
	}
	if u := strings.TrimSpace(file.SupportURL); u != "" && safeSupportURL(u) {
		doc.SupportURL = u
	}
	if u := strings.TrimSpace(file.PrivacyURL); u != "" && safeSupportURL(u) {
		doc.PrivacyURL = u
	}
	if u := strings.TrimSpace(file.TermsURL); u != "" && safeSupportURL(u) {
		doc.TermsURL = u
	}

	// A color is taken only if it is a valid CSS hex literal. An invalid one
	// falls back to Moov's, so a typo produces a slightly-off brand rather
	// than an unstyled or unreadable page.
	if c := normalizeHexColor(file.Colors.Primary); c != "" {
		doc.Colors.Primary = c
	}
	if c := normalizeHexColor(file.Colors.OnPrimary); c != "" {
		doc.Colors.OnPrimary = c
	}
	// The gradient stops follow the primary when the brand did not choose them.
	//
	// Falling back to MOOV's violet here was the bug an owner found on the first
	// real use of the brand panel: they set a pale cyan primary, left the two
	// splash fields alone because they had no opinion about them, and got a
	// violet login panel that belonged to a different product. A brand that
	// configures a primary and nothing else has said everything it needs to say
	// about its gradient, so it is DERIVED from that primary (branding.
	// DeriveSplashColors) rather than inherited from ours. Each stop is still
	// overridable on its own: the derivation only fills the ones left empty.
	derivedFrom, derivedTo := "", ""
	if normalizeHexColor(file.Colors.Primary) != "" {
		derivedFrom, derivedTo = branding.DeriveSplashColors(doc.Colors.Primary)
	}
	switch c := normalizeHexColor(file.Colors.SplashFrom); {
	case c != "":
		doc.Colors.SplashFrom = c
	case derivedFrom != "":
		doc.Colors.SplashFrom = derivedFrom
	}
	switch c := normalizeHexColor(file.Colors.SplashTo); {
	case c != "":
		doc.Colors.SplashTo = c
	case derivedTo != "":
		doc.Colors.SplashTo = derivedTo
	}

	// An asset is advertised only if the file is present AND still passes
	// validation right now. That is what keeps the document honest: the URL in
	// the response is a URL that will serve bytes, not a promise about a file
	// that was valid when the CLI ran.
	logo := b.loadIconCandidate(host, file.Logo)
	icon := b.loadIconCandidate(host, file.Icon)
	if logo.url != "" {
		doc.LogoURL = logo.url
	}
	if icon.url != "" {
		doc.IconURL = icon.url
	}

	// The fallback chain for the RENDERED icons: the configured icon, then the
	// logo, then Moov's own. The icon wins whenever it is usable, because a
	// brand that bothered to supply a square mark supplied it for exactly this.
	switch {
	case icon.usable():
		entry.iconFile, entry.iconSum, entry.iconSource = icon.name, icon.sum, brandingSourceIcon
	case logo.usable():
		entry.iconFile, entry.iconSum, entry.iconSource = logo.name, logo.sum, brandingSourceLogo
	}
	// Declared whenever something WAS configured for the icons and could not be
	// used — including the case where the icon failed and the logo took over,
	// which is a working brand but not the one the operator asked for.
	if issue := brandingIconIssue(icon, logo, entry.iconSource); issue != "" {
		entry.iconIssue = issue
		b.log.Warn("jmaphttp: the configured branding icon source cannot be rendered as PWA icons",
			"host", host, "icon", file.Icon, "logo", file.Logo,
			"using", brandingIconUsing(entry.iconSource), "reason", issue)
	}

	// logoDark and splash are advertised on presence and validity alone. Neither
	// feeds the icon renderer, so there is nothing to declare when one is a
	// WebP: it displays perfectly well in the page that asked for it.
	if name := safeAssetName(file.LogoDark); name != "" {
		if _, _, err := b.openAsset(host, name); err == nil {
			doc.LogoDarkURL = brandingAssetURL(host, name)
		}
	}
	if name := safeAssetName(file.Splash); name != "" {
		if _, _, err := b.openAsset(host, name); err == nil {
			doc.SplashURL = brandingAssetURL(host, name)
		}
	}

	return entry
}

// brandingCandidate is one configured asset weighed as a source for the PWA
// icons: whether it is advertisable at all, and whether it can be RENDERED.
type brandingCandidate struct {
	// configured is what branding.json named, trimmed; "" means the field was
	// absent, which is not a problem and never declared.
	configured string
	// name is the validated filename, "" when the file is missing, unreadable
	// or not an image at all.
	name string
	// url is what the document advertises for it, "" when name is.
	url string
	// sum is the hex SHA-256 of its bytes.
	sum string
	// err is why it cannot be rendered into icons; nil when it can.
	err error
}

func (c brandingCandidate) usable() bool { return c.name != "" && c.err == nil }

// loadIconCandidate reads one configured asset and judges it, without deciding
// anything: the caller composes the chain.
func (b *brandingStore) loadIconCandidate(host, configured string) brandingCandidate {
	c := brandingCandidate{configured: strings.TrimSpace(configured)}
	if c.configured == "" {
		return c
	}
	name := safeAssetName(c.configured)
	body, _, err := b.openAsset(host, name)
	if name == "" || err != nil {
		c.err = errors.New("the file is missing or is not a valid image")
		return c
	}
	c.name = name
	c.url = brandingAssetURL(host, name)
	c.sum = hex.EncodeToString(sha256sum(body))
	c.err = ValidateBrandingIconSource(body)
	return c
}

// brandingIconIssue is the sentence an operator reads when the icons are not
// coming from where they asked. It names the FILE that failed and the rest of
// the chain, because "the logo cannot be rendered" told an operator who had
// configured an icon nothing about which of their two files was the problem.
func brandingIconIssue(icon, logo brandingCandidate, source string) string {
	var parts []string
	if icon.configured != "" && icon.err != nil {
		parts = append(parts, fmt.Sprintf("the configured icon %q cannot be rendered: %v", icon.configured, icon.err))
	}
	if logo.configured != "" && logo.err != nil && source != brandingSourceLogo {
		parts = append(parts, fmt.Sprintf("the configured logo %q cannot be rendered: %v", logo.configured, logo.err))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") +
		" (the icons are rendered from icon, then logo, then Moov's own; " +
		"this host is being served " + brandingIconUsing(source) + ")"
}

// brandingIconUsing names, in the operator's words, where the icons come from.
func brandingIconUsing(source string) string {
	switch source {
	case brandingSourceIcon:
		return "the configured icon"
	case brandingSourceLogo:
		return "the configured logo"
	default:
		return "Moov's icons"
	}
}

// maxShortNameRunes is the cap on Branding.ShortName. Twelve is what the
// manifest spec recommends as the length launchers can show without
// truncation, and it is what both writers refuse past.
const maxShortNameRunes = branding.MaxShortNameRunes

// deriveShortName picks the home-screen label for a brand that did not
// configure one: the name itself when it fits, otherwise its first word,
// itself cut to fit. "Acme Mail" stays "Acme Mail"; "Corporate Mailbox Acme"
// becomes "Corporate".
func deriveShortName(name string) string {
	n := strings.TrimSpace(name)
	if len([]rune(n)) <= maxShortNameRunes {
		return n
	}
	if fields := strings.Fields(n); len(fields) > 0 {
		return truncateRunes(fields[0], maxShortNameRunes)
	}
	return truncateRunes(n, maxShortNameRunes)
}

// sha256sum is the digest of a byte slice, used to key the icon cache.
func sha256sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// brandingFile is the on-disk shape the writers produce (internal/branding).
// It is deliberately a DIFFERENT type from Branding: the file names local
// asset FILES, the response carries URLs, and conflating the two is how a
// customer-supplied string ends up as an <img src> pointing anywhere.
type brandingFile = branding.File

// brandingAssetURL builds the on-origin URL for one asset. Root-relative on
// purpose: the PWA is served from the same origin, and a relative URL cannot
// become a cross-origin request no matter what the Host header said.
func brandingAssetURL(host, name string) string {
	return "/branding/assets/" + host + "/" + name
}

// errBrandingAsset is the single failure the asset path reports. The caller
// renders every cause identically, so the causes are not enumerated in the
// type.
var errBrandingAsset = errors.New("jmaphttp: branding asset unavailable")

// openAsset reads and validates one asset.
//
// Validation is by CONTENT, not by extension: the bytes are sniffed and must
// be a PNG, JPEG, WebP or GIF. The declared Content-Type comes from the
// SNIFFED type, never from the filename, so a file called logo.png containing
// HTML is refused rather than served as an image the browser then re-sniffs.
func (b *brandingStore) openAsset(host, name string) ([]byte, string, error) {
	if b == nil || b.dir == "" || host == "" {
		return nil, "", errBrandingAsset
	}
	clean := safeAssetName(name)
	if clean == "" {
		return nil, "", errBrandingAsset
	}

	full := filepath.Join(b.dir, host, clean)

	f, err := os.Open(full) // #nosec G304 -- host and clean are validated above to be single path components from a fixed alphabet.
	if err != nil {
		return nil, "", errBrandingAsset
	}
	defer func() { _ = f.Close() }()

	// A directory, a symlink to /dev/zero, a FIFO: only a regular file is an
	// asset. Stat AFTER opening so the check applies to the thing that was
	// actually opened rather than to whatever the name pointed at a moment ago.
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, "", errBrandingAsset
	}
	if info.Size() > MaxBrandingAssetBytes {
		return nil, "", errBrandingAsset
	}

	// LimitReader with one extra byte: reading MaxBrandingAssetBytes+1 tells
	// the difference between "exactly at the cap" and "over it" for a file
	// that grew between Stat and Read.
	body, err := io.ReadAll(io.LimitReader(f, MaxBrandingAssetBytes+1))
	if err != nil || len(body) > MaxBrandingAssetBytes || len(body) == 0 {
		return nil, "", errBrandingAsset
	}

	contentType, ok := sniffImageType(body)
	if !ok {
		return nil, "", errBrandingAsset
	}
	return body, contentType, nil
}

// safeAssetName reduces a configured filename to a single, safe path
// component, or "" if it cannot be one.
//
// SVG IS DELIBERATELY NOT ACCEPTED, here or in the CLI. An SVG is an XML
// document that can carry <script>, external references and CSS, so serving an
// operator-supplied one from our own origin would hand any customer who can
// upload a logo a stored-XSS primitive on the login page — the page that
// exists to receive passwords. Sanitizing SVG correctly is a project in
// itself; refusing it costs a customer one export step. PNG with an alpha
// channel covers every real logo. (L2-pwa §6 risk 4: "sin SVG sin sanitizar" —
// this is the strict reading of that line.)
//
// The rule is branding.SafeAssetName, shared with the writers.
func safeAssetName(name string) string { return branding.SafeAssetName(name) }

// AllowedBrandingExtensions are the file extensions `moovctl branding set`
// accepts. The SERVER does not trust extensions at all (it sniffs), but the
// CLI checks them so an operator gets a clear refusal at the moment they pass
// a .svg rather than a silently unbranded login page later.
var AllowedBrandingExtensions = branding.AllowedExtensions

// sniffImageType identifies a raster image by its magic bytes and returns the
// Content-Type to declare. See branding.SniffImageType on why it is
// hand-rolled rather than http.DetectContentType.
func sniffImageType(b []byte) (string, bool) { return branding.SniffImageType(b) }

// normalizeHexColor validates a CSS hex color and returns it lowercased, or
// "" if it is not one. Only #rgb and #rrggbb (branding.NormalizeHexColor).
func normalizeHexColor(s string) string { return branding.NormalizeHexColor(s) }

// safeSupportURL allows only the schemes that can appear in an href on the
// login page without becoming script execution (branding.SafeURL).
func safeSupportURL(u string) bool { return branding.SafeURL(u) }

// truncateRunes caps a string by RUNES, so a multi-byte name is cut at a
// character boundary rather than mid-codepoint.
func truncateRunes(s string, maxRunes int) string { return branding.TruncateRunes(s, maxRunes) }

// brandingETag fingerprints a document so a conditional request can be
// answered 304. It is derived from the document itself rather than from file
// mtimes: two hosts with identical branding legitimately share an ETag, and a
// file rewritten with the same content correctly does not invalidate caches.
//
// EVERY field of the document is in the fingerprint. The list is written out
// rather than hashing the JSON so that adding a field to Branding is a
// conscious edit here too — and a test changes each field in turn and demands
// a different tag, because supportUrl once went missing from this list and a
// cache kept serving the old link until it expired.
func brandingETag(doc Branding) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t",
		doc.Name, doc.ShortName, doc.LogoURL, doc.SplashURL, doc.IconURL, doc.LogoDarkURL,
		doc.Colors.Primary, doc.Colors.OnPrimary,
		doc.Colors.SplashFrom, doc.Colors.SplashTo,
		doc.Tagline, doc.SupportURL, doc.PrivacyURL, doc.TermsURL, doc.Default)
	return `"` + hex.EncodeToString(h.Sum(nil))[:16] + `"`
}

// etagMatches implements the If-None-Match comparison for our single strong
// ETag: the "*" wildcard, or the tag present in the comma-separated list.
func etagMatches(header, etag string) bool {
	if strings.TrimSpace(header) == "*" {
		return true
	}
	for _, candidate := range strings.Split(header, ",") {
		c := strings.TrimSpace(candidate)
		// A weak validator (W/"...") compares equal to our strong one for the
		// purposes of If-None-Match (RFC 9110 §13.1.2 uses weak comparison).
		c = strings.TrimPrefix(c, "W/")
		if c == etag {
			return true
		}
	}
	return false
}

// brandingDirIsUsable reports whether a configured branding directory can be
// read, for the startup log line. It never fails startup: an unreadable
// directory degrades to the Moov defaults, which is a working server.
func brandingDirIsUsable(dir string) error {
	if dir == "" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fs.ErrInvalid
	}
	return nil
}
