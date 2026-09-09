package jmaphttp

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	// The decoders the logo may arrive in. Registering them is what lets
	// image.DecodeConfig and image.Decode recognize JPEG and GIF; PNG is
	// imported by name because the icons are ENCODED as PNG too. WebP is
	// deliberately absent: its decoder lives in golang.org/x/image, which is
	// not vendored, so a WebP logo is declared unusable rather than decoded.
	_ "image/gif"
	_ "image/jpeg"
)

// The per-host PWA manifest and icons.
//
// The web shell links `/branding/manifest.webmanifest` and the icons under
// `/branding/icons/` instead of the static files under /icons/, so that an
// installed Moov on a branded host carries the CUSTOMER's name and mark on the
// home screen — the one place a login-page brand would otherwise leak Moov's
// through. Both routes resolve the Host header exactly as GET /branding does,
// with the same non-enumeration property: a host with no configuration and a
// host configured to look exactly like Moov answer byte for byte the same.
//
// # Defaults are embedded, not derived
//
// Moov's own manifest and icons are copied verbatim from web/public into
// brandassets/ and embedded. A test pins the two copies byte for byte, so the
// PWA's static files and the server's defaults cannot drift apart. The only
// generated default is favicon-32, which web/public does not have as a PNG;
// it is rendered once from the embedded 192 px icon.
//
// # Icons are rendered, cached, and fall back loudly
//
// The icons are rendered from the brand's optional SQUARE `icon` when it has
// one, and from its `logo` otherwise — a chain of icon, then logo, then Moov's
// own. The two exist separately because the maskable and Apple icons sit on an
// opaque plate of the primary color, so a black wordmark on a brand whose
// primary is black renders invisible; the square glyph a brand kit keeps for
// dark backgrounds goes in `icon`.
//
// A dedicated icon is also plated at EVERY size, where a logo is plated only
// on the maskable and Apple icons: a mark drawn for a dark plate is invisible
// on the transparent canvas a desktop launcher and a browser tab put behind
// it. See specForSource.
//
// The chosen source (PNG, JPEG or GIF; dimensions capped before decoding) is
// rendered into every icon size on demand and cached by host, source digest,
// which source it was, accent color and name for the document cache's TTL. A source that cannot be
// rendered — WebP, undecodable, oversized, missing — falls through to the next
// link AND is declared: one log line per host per TTL naming the file that
// failed and where the icons are coming from instead, and a line in `moovctl
// branding show`. Silent fallback would put Moov's mark on a customer's
// phone with nothing to tell the operator why.

const (
	// PathBrandingManifest serves the per-host web app manifest. GET, public.
	PathBrandingManifest = "/branding/manifest.webmanifest"

	// PathBrandingIcon serves one per-host PWA icon. GET, public. The {file}
	// variable must be one of the known icon names plus ".png"; anything else
	// is the same 404 as an unknown asset.
	PathBrandingIcon = "/branding/icons/{file}"

	// brandingIconPrefix is what the manifest's icon URLs are rewritten to.
	brandingIconPrefix = "/branding/icons/"

	// MaxBrandingLogoDimension caps the width and height of a logo BEFORE it
	// is decoded. image.DecodeConfig reads only the header, so a 40,000 px
	// PNG that inflates to gigabytes is refused for the cost of a few bytes
	// — the decompression-bomb defense. 4096 is far more than any icon needs.
	MaxBrandingLogoDimension = 4096

	// manifestContentType is the registered media type for a web app manifest.
	manifestContentType = "application/manifest+json"
)

//go:embed brandassets/manifest.webmanifest brandassets/icons/*.png
var brandAssets embed.FS

// iconSpec describes one icon the shell and the manifest reference.
type iconSpec struct {
	// name is the file stem: the route serves <name>.png.
	name string
	// size is the square edge in pixels.
	size int
	// pad is the fraction of the edge left empty on EACH side. The logo is
	// contained (aspect preserved) inside the remaining square.
	pad float64
	// opaque paints the accent color behind the logo instead of leaving the
	// canvas transparent.
	opaque bool
}

// brandingIconSpecs is the complete set of icons the server renders. The
// manifest's icon entries and the shell's <link>s must name only these; a
// test walks the embedded manifest and checks.
//
// The plate column below describes the LOGO as the source. A dedicated `icon`
// is plated at every size instead — see specForSource.
//
//   - icon-*: the "any" purpose icons — the logo on a transparent square with
//     a little breathing room, as a desktop or Android launcher shows them.
//   - icon-maskable-*: launchers that mask (Android adaptive icons) keep only
//     the inner 80% circle, so the logo is contained inside that safe zone and
//     the whole plate is painted in the accent color — a transparent plate
//     would be masked onto whatever the launcher chooses, usually white.
//   - apple-touch-icon: iOS discards alpha and composites onto BLACK, so it
//     is opaque on the accent color, at the 180 px iOS asks for.
//   - favicon-32: the tab icon, transparent, no padding — at 32 px every
//     pixel counts.
var brandingIconSpecs = []iconSpec{
	{name: "icon-192", size: 192, pad: 0.10},
	{name: "icon-512", size: 512, pad: 0.10},
	{name: "icon-maskable-192", size: 192, pad: 0.20, opaque: true},
	{name: "icon-maskable-512", size: 512, pad: 0.20, opaque: true},
	{name: "apple-touch-icon", size: 180, pad: 0.10, opaque: true},
	{name: "favicon-32", size: 32, pad: 0},
}

// iconSpecByName looks an icon up by its file stem.
func iconSpecByName(name string) (iconSpec, bool) {
	for _, s := range brandingIconSpecs {
		if s.name == name {
			return s, true
		}
	}
	return iconSpec{}, false
}

// iconEntry is one rendered icon in the store's cache.
type iconEntry struct {
	body    []byte
	etag    string
	expires time.Time
}

// --- the manifest route ------------------------------------------------------

// handleBrandingManifest serves GET /branding/manifest.webmanifest.
func (s *Server) handleBrandingManifest(w http.ResponseWriter, r *http.Request) {
	host := resolveBrandingHost(r.Host)
	doc, _ := s.branding.resolve(host)

	body, err := renderBrandingManifest(doc)
	if err != nil {
		// Unreachable in a built binary: the embedded manifest is parsed by a
		// test. Answered honestly rather than with a half-manifest.
		s.log.Error("jmaphttp: rendering the branding manifest failed", "host", host, "error", err)
		writeGenericProblem(w, http.StatusInternalServerError, "manifest unavailable")
		return
	}
	etag := bytesETag(body)

	h := w.Header()
	h.Set("Cache-Control", fmt.Sprintf("public, max-age=%d", BrandingMaxAge))
	h.Set("ETag", etag)
	h.Set("X-Content-Type-Options", "nosniff")

	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	h.Set("Content-Type", manifestContentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// renderBrandingManifest produces the manifest for one resolved brand: the
// embedded Moov manifest with name, short_name and theme_color replaced and
// every icon URL pointed at the per-host icon route.
//
// It is the SAME transformation for the default brand, with Moov's values, so
// an unconfigured host and a host configured to look like Moov are
// indistinguishable. Everything else — id, start_url, scope, display,
// protocol_handlers, shortcuts — is passed through untouched: those describe
// the application, not the brand.
func renderBrandingManifest(doc Branding) ([]byte, error) {
	raw, err := brandAssets.ReadFile("brandassets/manifest.webmanifest")
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("embedded manifest: %w", err)
	}

	m["name"] = doc.Name
	m["short_name"] = doc.ShortName
	m["theme_color"] = doc.Colors.Primary

	if err := rewriteManifestIcons(m["icons"]); err != nil {
		return nil, fmt.Errorf("embedded manifest icons: %w", err)
	}
	if shortcuts, ok := m["shortcuts"].([]any); ok {
		for _, sc := range shortcuts {
			entry, ok := sc.(map[string]any)
			if !ok {
				continue
			}
			if err := rewriteManifestIcons(entry["icons"]); err != nil {
				return nil, fmt.Errorf("embedded manifest shortcut icons: %w", err)
			}
		}
	}

	// json.Marshal sorts map keys, so the output is deterministic — which is
	// what makes the ETag stable and the "byte-identical" property testable.
	return json.Marshal(m)
}

// rewriteManifestIcons points each icon's src at the branding icon route,
// refusing an icon whose name the server does not render (that would be a
// manifest promising a URL that 404s).
func rewriteManifestIcons(v any) error {
	icons, ok := v.([]any)
	if !ok {
		return nil
	}
	for _, ic := range icons {
		entry, ok := ic.(map[string]any)
		if !ok {
			continue
		}
		src, _ := entry["src"].(string)
		base := path.Base(src)
		stem := strings.TrimSuffix(base, ".png")
		if stem == base || stem == "" {
			return fmt.Errorf("icon src %q is not a .png", src)
		}
		if _, known := iconSpecByName(stem); !known {
			return fmt.Errorf("icon src %q names an icon the server does not render", src)
		}
		entry["src"] = brandingIconPrefix + base
	}
	return nil
}

// --- the icon route -----------------------------------------------------------

// handleBrandingIcon serves GET /branding/icons/{file}.
func (s *Server) handleBrandingIcon(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	stem, isPNG := strings.CutSuffix(file, ".png")
	spec, known := iconSpecByName(stem)
	if !isPNG || !known {
		// The same 404 as the asset route: an unknown name, a traversal
		// attempt, a wrong extension all look alike to the caller.
		writeGenericProblem(w, http.StatusNotFound, "not found")
		return
	}

	host := resolveBrandingHost(r.Host)
	body, etag := s.branding.icon(host, spec)

	h := w.Header()
	h.Set("Cache-Control", fmt.Sprintf("public, max-age=%d", BrandingMaxAge))
	h.Set("ETag", etag)
	h.Set("X-Content-Type-Options", "nosniff")
	// As on the asset route: nothing may be fetched, framed or executed on
	// behalf of an image, whatever its bytes turn out to be.
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")

	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	h.Set("Content-Type", "image/png")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// icon returns the PNG bytes and ETag of one icon for a host: rendered from
// the winner of the icon -> logo -> Moov chain. It never fails — the fallback
// IS the answer for every failure.
func (b *brandingStore) icon(host string, spec iconSpec) ([]byte, string) {
	if b == nil || b.dir == "" || host == "" {
		return defaultIcon(spec)
	}
	e := b.resolveEntry(host)
	// iconFile is already the winner of the chain, and it is empty precisely
	// when Moov's own icons are the answer.
	if e.iconFile == "" {
		return defaultIcon(spec)
	}
	spec = specForSource(spec, e.iconSource)

	key := iconCacheKey(host, e.iconSum, e.iconSource, e.doc.Colors.Primary, spec.name)
	now := b.now()
	b.mu.Lock()
	if cached, ok := b.icons[key]; ok && now.Before(cached.expires) {
		b.mu.Unlock()
		return cached.body, cached.etag
	}
	b.mu.Unlock()

	sourceBytes, _, err := b.openAsset(host, e.iconFile)
	if err != nil {
		return defaultIcon(spec)
	}
	source, err := decodeBrandingLogo(sourceBytes)
	if err != nil {
		// The entry said it was usable a moment ago; the file changed
		// underneath. The next TTL will re-check and declare it.
		return defaultIcon(spec)
	}
	body, err := renderBrandingIcon(source, spec, e.doc.Colors.Primary)
	if err != nil {
		return defaultIcon(spec)
	}
	etag := bytesETag(body)

	b.mu.Lock()
	b.icons[key] = iconEntry{body: body, etag: etag, expires: now.Add(brandingCacheTTL)}
	b.mu.Unlock()
	return body, etag
}

// specForSource adjusts a spec for WHERE the mark came from.
//
// When the source is the dedicated square icon, EVERY size is painted on an
// opaque plate of the primary color — not just the maskable and Apple ones.
// An operator who supplies an `icon` supplies a mark drawn FOR that plate, and
// found on the pilot with a real brand kit: a white glyph for dark backgrounds
// rendered correctly on the maskable pair and then vanished on icon-192,
// icon-512 and favicon-32, which were transparent, on a light desktop and a
// light browser tab. The plate is what makes such a mark legible everywhere.
//
// When the source is the LOGO, nothing changes: a wordmark on a transparent
// square is what a launcher and a tab have always been given, and quietly
// painting plates behind every existing customer's logo would be a visible
// change nobody asked for.
//
// favicon-32 needs one more thing than the plate. Its spec has NO padding, on
// the reasoning that at 32 px every pixel counts — but a SQUARE mark then
// covers the plate edge to edge, and Areacorp's white glyph is a white square
// on a light tab all over again. So a plated source gets a small padding floor
// there, which is what actually makes the plate visible around the mark. Only
// favicon-32 is affected: every other spec already pads.
func specForSource(spec iconSpec, source string) iconSpec {
	if source != brandingSourceIcon {
		return spec
	}
	spec.opaque = true
	if spec.pad < minPlatedIconPad {
		spec.pad = minPlatedIconPad
	}
	return spec
}

// minPlatedIconPad is the padding a plated icon gets at minimum. At 32 px it
// is 3 px on each side: enough for the plate to read as a frame, small enough
// that the mark keeps 26 of the 32 pixels.
const minPlatedIconPad = 0.10

// iconCacheKey joins the inputs an icon depends on. NUL-separated: none of the
// parts can contain one (host, source and name come from fixed alphabets, the
// digest is hex, the color is a validated hex literal).
//
// sourceSum is the digest of whichever file the icons are rendered FROM, and
// source says which of the two it was — the same bytes render DIFFERENTLY as
// an icon (always plated) and as a logo (plated only where the purpose demands
// it), so the digest alone would not separate them.
func iconCacheKey(host, sourceSum, source, primary, name string) string {
	return host + "\x00" + sourceSum + "\x00" + source + "\x00" + primary + "\x00" + name
}

// --- the embedded defaults ----------------------------------------------------

var (
	defaultIconsOnce sync.Once
	defaultIcons     map[string]iconEntry
)

// defaultIcon returns Moov's own icon for a spec. Five come straight from the
// embedded files; favicon-32 is rendered once from the embedded 192 px icon
// because web/public ships the favicon as SVG, which the server cannot
// rasterize. Both paths are deterministic, so the ETag is stable across
// processes.
func defaultIcon(spec iconSpec) ([]byte, string) {
	defaultIconsOnce.Do(loadDefaultIcons)
	e, ok := defaultIcons[spec.name]
	if !ok {
		// Cannot happen: loadDefaultIcons covers every spec or panics at
		// first use, which a test exercises.
		return nil, `""`
	}
	return e.body, e.etag
}

func loadDefaultIcons() {
	defaultIcons = make(map[string]iconEntry, len(brandingIconSpecs))
	for _, spec := range brandingIconSpecs {
		body, err := brandAssets.ReadFile("brandassets/icons/" + spec.name + ".png")
		if err == nil {
			defaultIcons[spec.name] = iconEntry{body: body, etag: bytesETag(body)}
			continue
		}
		// Not embedded: derive it from the embedded 192 px icon.
		src, err := brandAssets.ReadFile("brandassets/icons/icon-192.png")
		if err != nil {
			panic("jmaphttp: embedded icon-192.png is missing: " + err.Error())
		}
		img, err := decodeBrandingLogo(src)
		if err != nil {
			panic("jmaphttp: embedded icon-192.png does not decode: " + err.Error())
		}
		derived := iconSpec{name: spec.name, size: spec.size, pad: 0, opaque: false}
		body, err = renderBrandingIcon(img, derived, DefaultBranding().Colors.Primary)
		if err != nil {
			panic("jmaphttp: rendering the default " + spec.name + " failed: " + err.Error())
		}
		defaultIcons[spec.name] = iconEntry{body: body, etag: bytesETag(body)}
	}
}

// --- decoding and rendering ---------------------------------------------------

// errBrandingIconWebP is the one unusable-logo cause worth naming in a
// sentinel: it is the format an operator is most likely to bring and the
// only one the server accepts as an asset yet cannot render.
var errBrandingIconWebP = errors.New("the logo is WebP, which cannot be decoded for PWA icons; provide it as PNG, JPEG or GIF")

// ValidateBrandingIconSource reports whether a logo's bytes can be rendered
// into PWA icons, and if not, why — in a sentence an operator can act on. It
// is exported for `moovctl branding`, which uses it to warn at `set` time and
// to explain the fallback in `show`; the server applies the same function when
// it resolves a host, so the CLI's verdict and the server's cannot disagree.
//
// It reads only the image header (image.DecodeConfig), never the pixels, so
// it is safe to call on anything that passed the size cap.
func ValidateBrandingIconSource(data []byte) error {
	contentType, ok := sniffImageType(data)
	if !ok {
		return errors.New("the logo is not a PNG, JPEG, GIF or WebP image")
	}
	if contentType == "image/webp" {
		return errBrandingIconWebP
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("the logo cannot be decoded (%w)", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return fmt.Errorf("the logo has no pixels (%dx%d)", cfg.Width, cfg.Height)
	}
	if cfg.Width > MaxBrandingLogoDimension || cfg.Height > MaxBrandingLogoDimension {
		return fmt.Errorf("the logo is %dx%d; the limit is %d px on each side",
			cfg.Width, cfg.Height, MaxBrandingLogoDimension)
	}
	return nil
}

// decodeBrandingLogo validates and decodes a logo into a premultiplied RGBA
// image at the origin, the form the resampler and the compositor work on.
func decodeBrandingLogo(data []byte) (*image.RGBA, error) {
	if err := ValidateBrandingIconSource(data); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("the logo cannot be decoded (%w)", err)
	}
	b := src.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	return rgba, nil
}

// renderBrandingIcon composes one icon: the logo, contained inside the
// spec's padded square with its aspect ratio preserved and centered, over a
// transparent canvas or an opaque plate of the accent color.
func renderBrandingIcon(logo *image.RGBA, spec iconSpec, primaryHex string) ([]byte, error) {
	size := spec.size
	canvas := image.NewRGBA(image.Rect(0, 0, size, size))
	if spec.opaque {
		plate, ok := parseHexColor(primaryHex)
		if !ok {
			plate, _ = parseHexColor(DefaultBranding().Colors.Primary)
		}
		draw.Draw(canvas, canvas.Bounds(), image.NewUniform(plate), image.Point{}, draw.Src)
	}

	pad := int(math.Round(float64(size) * spec.pad))
	inner := size - 2*pad
	if inner < 1 {
		inner = 1
	}
	lw, lh := logo.Bounds().Dx(), logo.Bounds().Dy()
	if lw > 0 && lh > 0 {
		scale := math.Min(float64(inner)/float64(lw), float64(inner)/float64(lh))
		dw := clampInt(int(math.Round(float64(lw)*scale)), 1, inner)
		dh := clampInt(int(math.Round(float64(lh)*scale)), 1, inner)
		scaled := resampleRGBA(logo, dw, dh)
		x := (size - dw) / 2
		y := (size - dh) / 2
		draw.Draw(canvas, image.Rect(x, y, x+dw, y+dh), scaled, image.Point{}, draw.Over)
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, canvas); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// parseHexColor turns a validated #rgb or #rrggbb literal into an opaque
// color. The bool is false for anything normalizeHexColor would refuse.
func parseHexColor(s string) (color.NRGBA, bool) {
	c := normalizeHexColor(s)
	if c == "" {
		return color.NRGBA{}, false
	}
	if len(c) == 4 {
		c = string([]byte{'#', c[1], c[1], c[2], c[2], c[3], c[3]})
	}
	raw, err := hex.DecodeString(c[1:])
	if err != nil || len(raw) != 3 {
		return color.NRGBA{}, false
	}
	return color.NRGBA{R: raw[0], G: raw[1], B: raw[2], A: 255}, true
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// bytesETag is a strong validator over a response body.
func bytesETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:])[:16] + `"`
}
