package jmaphttp

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests for the per-host PWA manifest and icons (branding_pwa.go). The
// properties pinned here are the ones a browser or a launcher would otherwise
// be the first to notice: the embedded defaults ARE web/public's files, an
// unconfigured host and a Moov-lookalike answer identically, a rendered icon
// has the size and opacity its purpose demands, and a logo that cannot be
// rendered falls back to Moov's icons out loud rather than in silence.

// --- fixtures ---------------------------------------------------------------

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// solidImage is a w by h opaque rectangle of one color.
func solidImage(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

var testRed = color.NRGBA{R: 220, G: 20, B: 30, A: 255}

// redLogoPNG is a 100x50 red rectangle: wide, so containment has to letterbox.
func redLogoPNG(t *testing.T) []byte { return encodePNG(t, solidImage(100, 50, testRed)) }

func decodePNG(t *testing.T, body []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("response is not a PNG: %v", err)
	}
	return img
}

// nrgbaAt reads a pixel in straight (non-premultiplied) form.
func nrgbaAt(img image.Image, x, y int) color.NRGBA {
	c, ok := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
	if !ok {
		panic("NRGBAModel.Convert did not return an NRGBA") // by contract of color.Model
	}
	return c
}

// iconPath builds the route for one icon name.
func iconPath(name string) string { return brandingIconPrefix + name + ".png" }

// getIcon fetches one icon and asserts the PNG contract shared by every
// answer on the route, branded or not.
func getIcon(t *testing.T, srv *Server, host, name string) []byte {
	t.Helper()
	rec := getBranding(t, srv, iconPath(name), host, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s (Host %s) = %d, want 200\nbody: %s", iconPath(name), host, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff on an icon")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q, want default-src 'none'", csp)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "public") {
		t.Errorf("Cache-Control = %q", cc)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("no ETag on an icon")
	}
	return rec.Body.Bytes()
}

// getManifest fetches the manifest and asserts its header contract.
func getManifest(t *testing.T, srv *Server, host string) ([]byte, string) {
	t.Helper()
	rec := getBranding(t, srv, PathBrandingManifest, host, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s (Host %s) = %d\nbody: %s", PathBrandingManifest, host, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != manifestContentType {
		t.Errorf("Content-Type = %q, want %q", ct, manifestContentType)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff on the manifest")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Errorf("Cache-Control = %q, want public, max-age=300", cc)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Error("no ETag on the manifest")
	}
	return rec.Body.Bytes(), etag
}

func decodeManifest(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("manifest is not JSON: %v\n%s", err, body)
	}
	return m
}

// --- the embedded defaults are web/public's files ---------------------------

// TestEmbeddedBrandAssetsMatchWebPublic is the drift pin: the server's
// defaults are copies of the PWA's static files, and a copy that is not
// byte-identical is a bug in whichever side changed without the other.
func TestEmbeddedBrandAssetsMatchWebPublic(t *testing.T) {
	webPublic := filepath.Join("..", "..", "web", "public")
	if _, err := os.Stat(webPublic); err != nil {
		t.Skipf("web/public not present at %s: %v", webPublic, err)
	}

	pairs := map[string]string{
		"brandassets/manifest.webmanifest": filepath.Join(webPublic, "manifest.webmanifest"),
	}
	for _, spec := range brandingIconSpecs {
		if spec.name == "favicon-32" {
			continue // derived, not embedded: web/public ships the favicon as SVG
		}
		pairs["brandassets/icons/"+spec.name+".png"] = filepath.Join(webPublic, "icons", spec.name+".png")
	}
	for embedded, source := range pairs {
		got, err := brandAssets.ReadFile(embedded)
		if err != nil {
			t.Errorf("embedded %s: %v", embedded, err)
			continue
		}
		want, err := os.ReadFile(source)
		if err != nil {
			t.Errorf("source %s: %v", source, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from %s: the embedded copy has drifted from web/public", embedded, source)
		}
	}
}

// TestEmbeddedManifestNamesOnlyRenderedIcons: every icon URL in the embedded
// manifest must map to an icon the server renders, or the rewritten manifest
// would promise a 404.
func TestEmbeddedManifestNamesOnlyRenderedIcons(t *testing.T) {
	if _, err := renderBrandingManifest(DefaultBranding()); err != nil {
		t.Fatalf("the embedded manifest does not render: %v", err)
	}
	// And every embedded icon file is one the spec table knows.
	entries, err := brandAssets.ReadDir("brandassets/icons")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		stem := strings.TrimSuffix(e.Name(), ".png")
		if _, ok := iconSpecByName(stem); !ok {
			t.Errorf("embedded icon %s has no spec; it can never be served", e.Name())
		}
	}
}

// TestDefaultBrandingMatchesEmbeddedManifest: Moov's document and Moov's
// manifest say the same thing, so the default rewrite is a no-op in value.
func TestDefaultBrandingMatchesEmbeddedManifest(t *testing.T) {
	raw, err := brandAssets.ReadFile("brandassets/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	m := decodeManifest(t, raw)
	def := DefaultBranding()
	if m["name"] != def.Name {
		t.Errorf("manifest name = %v, DefaultBranding().Name = %q", m["name"], def.Name)
	}
	if m["short_name"] != def.ShortName {
		t.Errorf("manifest short_name = %v, DefaultBranding().ShortName = %q", m["short_name"], def.ShortName)
	}
	if m["theme_color"] != def.Colors.Primary {
		t.Errorf("manifest theme_color = %v, DefaultBranding().Colors.Primary = %q", m["theme_color"], def.Colors.Primary)
	}
}

// --- the manifest route -----------------------------------------------------

func TestBrandingManifestDefault(t *testing.T) {
	srv := brandingServer(t, "")
	body, _ := getManifest(t, srv, "anything.example.com")
	m := decodeManifest(t, body)

	def := DefaultBranding()
	if m["name"] != def.Name || m["short_name"] != def.ShortName || m["theme_color"] != def.Colors.Primary {
		t.Errorf("default manifest identity = %v / %v / %v", m["name"], m["short_name"], m["theme_color"])
	}

	// Every icon, including the shortcut's, points at the branding route.
	assertIconsRewritten(t, m["icons"])
	shortcuts, ok := m["shortcuts"].([]any)
	if !ok || len(shortcuts) == 0 {
		t.Fatalf("shortcuts = %v, want a non-empty list", m["shortcuts"])
	}
	for _, sc := range shortcuts {
		entry, ok := sc.(map[string]any)
		if !ok {
			t.Fatalf("shortcut = %v, want an object", sc)
		}
		assertIconsRewritten(t, entry["icons"])
	}

	// The application half is untouched.
	raw, _ := brandAssets.ReadFile("brandassets/manifest.webmanifest")
	orig := decodeManifest(t, raw)
	for _, key := range []string{"id", "start_url", "scope", "display", "protocol_handlers", "background_color", "lang"} {
		got, _ := json.Marshal(m[key])
		want, _ := json.Marshal(orig[key])
		if !bytes.Equal(got, want) {
			t.Errorf("%s = %s, want the embedded %s", key, got, want)
		}
	}
}

func assertIconsRewritten(t *testing.T, v any) {
	t.Helper()
	icons, ok := v.([]any)
	if !ok || len(icons) == 0 {
		t.Fatalf("icons = %v, want a non-empty list", v)
	}
	for _, ic := range icons {
		entry, ok := ic.(map[string]any)
		if !ok {
			t.Fatalf("icon = %v, want an object", ic)
		}
		src, _ := entry["src"].(string)
		if !strings.HasPrefix(src, brandingIconPrefix) {
			t.Errorf("icon src %q was not rewritten under %s", src, brandingIconPrefix)
		}
		stem := strings.TrimSuffix(strings.TrimPrefix(src, brandingIconPrefix), ".png")
		if _, known := iconSpecByName(stem); !known {
			t.Errorf("icon src %q names an icon the route does not serve", src)
		}
	}
}

// TestBrandingManifestBranded: a configured host's manifest carries the
// customer's identity, with short_name derived when not configured.
func TestBrandingManifestBranded(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "long.acme.test", map[string]any{
		"name":   "Acme Corporate Mailbox",
		"colors": map[string]string{"primary": "#C0FFEE"},
	}, nil)
	writeBrand(t, root, "short.acme.test", map[string]any{
		"name":      "Acme Corporate Mailbox",
		"shortName": "  AcmeMail  ",
	}, nil)
	srv := brandingServer(t, root)

	body, _ := getManifest(t, srv, "long.acme.test")
	m := decodeManifest(t, body)
	if m["name"] != "Acme Corporate Mailbox" {
		t.Errorf("name = %v", m["name"])
	}
	if m["short_name"] != "Acme" {
		t.Errorf("short_name = %v, want the derived first word Acme", m["short_name"])
	}
	if m["theme_color"] != "#c0ffee" {
		t.Errorf("theme_color = %v, want #c0ffee", m["theme_color"])
	}
	assertIconsRewritten(t, m["icons"])

	body, _ = getManifest(t, srv, "short.acme.test")
	if m := decodeManifest(t, body); m["short_name"] != "AcmeMail" {
		t.Errorf("configured short_name = %v, want AcmeMail (trimmed)", m["short_name"])
	}
}

// TestBrandingPWAIsIndistinguishableForMoovLookalike is the non-enumeration
// property on the new routes: a host configured to look exactly like Moov and
// a host with no configuration answer byte for byte the same on the manifest
// AND on every icon, ETags included.
func TestBrandingPWAIsIndistinguishableForMoovLookalike(t *testing.T) {
	root := t.TempDir()
	def := DefaultBranding()
	writeBrand(t, root, "lookalike.test", map[string]any{
		"name":      def.Name,
		"shortName": def.ShortName,
		"colors": map[string]string{
			"primary":    def.Colors.Primary,
			"onPrimary":  def.Colors.OnPrimary,
			"splashFrom": def.Colors.SplashFrom,
			"splashTo":   def.Colors.SplashTo,
		},
	}, nil)
	srv := brandingServer(t, root)

	plainBody, plainETag := getManifest(t, srv, "nobody.test")
	likeBody, likeETag := getManifest(t, srv, "lookalike.test")
	if !bytes.Equal(plainBody, likeBody) || plainETag != likeETag {
		t.Errorf("manifests differ between an unconfigured host and a Moov lookalike:\n%s\n%s", plainBody, likeBody)
	}

	for _, spec := range brandingIconSpecs {
		plain := getIcon(t, srv, "nobody.test", spec.name)
		like := getIcon(t, srv, "lookalike.test", spec.name)
		if !bytes.Equal(plain, like) {
			t.Errorf("%s differs between an unconfigured host and a Moov lookalike", spec.name)
		}
		// And both are Moov's own bytes.
		want, _ := defaultIcon(spec)
		if !bytes.Equal(plain, want) {
			t.Errorf("%s for an unconfigured host is not the embedded default", spec.name)
		}
	}
}

func TestBrandingManifestConditionalRequest(t *testing.T) {
	srv := brandingServer(t, "")
	_, etag := getManifest(t, srv, "mail.example.com")

	rec := getBranding(t, srv, PathBrandingManifest, "mail.example.com",
		map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("If-None-Match with the current ETag = %d (%d bytes), want 304 and no body", rec.Code, rec.Body.Len())
	}
	rec = getBranding(t, srv, PathBrandingManifest, "mail.example.com",
		map[string]string{"If-None-Match": `"0000000000000000"`})
	if rec.Code != http.StatusOK {
		t.Errorf("stale If-None-Match = %d, want 200", rec.Code)
	}

	// An icon revalidates the same way.
	first := getBranding(t, srv, iconPath("icon-192"), "mail.example.com", nil)
	again := getBranding(t, srv, iconPath("icon-192"), "mail.example.com",
		map[string]string{"If-None-Match": first.Header().Get("ETag")})
	if again.Code != http.StatusNotModified {
		t.Errorf("icon If-None-Match = %d, want 304", again.Code)
	}
}

// TestBrandingManifestETagChangesWithBrand: a shared cache must not serve one
// customer's manifest to another, and a changed short name must invalidate.
func TestBrandingManifestETagChangesWithBrand(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "a.test", map[string]any{"name": "Brand A"}, nil)
	writeBrand(t, root, "b.test", map[string]any{"name": "Brand A", "shortName": "B"}, nil)
	srv := brandingServer(t, root)
	_, a := getManifest(t, srv, "a.test")
	_, b := getManifest(t, srv, "b.test")
	if a == b {
		t.Errorf("two different brands share the manifest ETag %s", a)
	}
}

// --- the icon route ---------------------------------------------------------

// TestBrandingIconsRenderFromLogo checks every icon's geometry and opacity
// against a wide red logo on a known accent color.
func TestBrandingIconsRenderFromLogo(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "icons.test", map[string]any{
		"name":   "Icons",
		"logo":   "logo.png",
		"colors": map[string]string{"primary": "#123456"},
	}, map[string][]byte{"logo.png": redLogoPNG(t)})
	srv := brandingServer(t, root)
	/*
	 * The plate follows the MARK, not the primary. Pure red has a WCAG
	 * relative luminance of 0.2126 — a dark mark — so its plate is white,
	 * and the deep-blue primary #123456 is deliberately never painted here.
	 * That is the point of the rule: a dark mark on a dark primary was the
	 * invisible-icon bug.
	 */
	plate := color.NRGBA{R: 255, G: 255, B: 255, A: 255}

	for _, spec := range brandingIconSpecs {
		t.Run(spec.name, func(t *testing.T) {
			body := getIcon(t, srv, "icons.test", spec.name)
			img := decodePNG(t, body)
			if b := img.Bounds(); b.Dx() != spec.size || b.Dy() != spec.size {
				t.Fatalf("size = %dx%d, want %dx%d", b.Dx(), b.Dy(), spec.size, spec.size)
			}
			def, _ := defaultIcon(spec)
			if bytes.Equal(body, def) {
				t.Fatal("a host with a valid logo was served the default icon")
			}

			// The logo is centered: the exact center pixel is red whatever the
			// padding.
			c := nrgbaAt(img, spec.size/2, spec.size/2)
			if c.R < 200 || c.G > 40 || c.B > 50 || c.A != 255 {
				t.Errorf("center pixel = %v, want the red logo", c)
			}

			// The corner is outside the contained logo: transparent on an
			// "any" icon, the plate on an opaque one.
			corner := nrgbaAt(img, 1, 1)
			if spec.opaque {
				if corner != plate {
					t.Errorf("corner = %v, want the opaque plate %v", corner, plate)
				}
			} else if corner.A != 0 {
				t.Errorf("corner = %v, want transparent", corner)
			}

			// Opaque icons are opaque EVERYWHERE — iOS composites alpha onto
			// black, and a masked launcher shows whatever is behind a hole.
			if spec.opaque {
				for y := 0; y < spec.size; y += 7 {
					for x := 0; x < spec.size; x += 7 {
						if a := nrgbaAt(img, x, y).A; a != 255 {
							t.Fatalf("pixel (%d,%d) alpha = %d on an opaque icon", x, y, a)
						}
					}
				}
			}

			// Containment: the wide logo is letterboxed — at the padded inner
			// box's top edge, the middle column is still background because
			// the logo (2:1) is only half as tall as the box.
			pad := int(float64(spec.size)*spec.pad + 0.5)
			if spec.size >= 180 {
				top := nrgbaAt(img, spec.size/2, pad+1)
				if spec.opaque && top != plate {
					t.Errorf("above the letterboxed logo = %v, want the plate", top)
				}
				if !spec.opaque && top.A != 0 {
					t.Errorf("above the letterboxed logo = %v, want transparent", top)
				}
				// And just inside the padding on the horizontal axis the logo
				// IS present: it spans the full inner width.
				side := nrgbaAt(img, pad+1, spec.size/2)
				if side.R < 200 {
					t.Errorf("inside the left padding = %v, want the logo to reach the inner box", side)
				}
				// While the padding itself is background.
				edge := nrgbaAt(img, pad/2, spec.size/2)
				if !spec.opaque && edge.A != 0 {
					t.Errorf("inside the padding = %v, want transparent", edge)
				}
			}
		})
	}
}

// TestBrandingIconsFromJPEGAndGIF: the other two stdlib formats render too.
func TestBrandingIconsFromJPEGAndGIF(t *testing.T) {
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, solidImage(64, 64, testRed), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	var gifBuf bytes.Buffer
	if err := gif.Encode(&gifBuf, solidImage(64, 64, testRed), nil); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{"logo.jpg": jpg.Bytes(), "logo.gif": gifBuf.Bytes()}
	for file, body := range cases {
		t.Run(file, func(t *testing.T) {
			root := t.TempDir()
			writeBrand(t, root, "fmt.test", map[string]any{"logo": file}, map[string][]byte{file: body})
			srv := brandingServer(t, root)
			icon := getIcon(t, srv, "fmt.test", "icon-192")
			img := decodePNG(t, icon)
			if img.Bounds().Dx() != 192 {
				t.Errorf("width = %d", img.Bounds().Dx())
			}
			if c := nrgbaAt(img, 96, 96); c.R < 180 || c.A != 255 {
				t.Errorf("center = %v, want red", c)
			}
		})
	}
}

// oversizedPNG is a real, small-on-disk PNG whose header claims one side past
// the limit: what a decompression bomb looks like to DecodeConfig.
func oversizedPNG(t *testing.T) []byte {
	t.Helper()
	return encodePNG(t, image.NewNRGBA(image.Rect(0, 0, MaxBrandingLogoDimension+1, 1)))
}

// logCapture collects slog records so a test can count declarations.
type logCapture struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, r)
	return nil
}
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

func (c *logCapture) count(substr string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.recs {
		if strings.Contains(r.Message, substr) {
			n++
		}
	}
	return n
}

// TestBrandingIconsFallBackAndDeclare: every unusable logo yields Moov's
// icons — byte-identical to an unconfigured host — and exactly one log line
// per host per TTL says so. For WebP the login-page logo itself STILL works;
// only the icons fall back.
func TestBrandingIconsFallBackAndDeclare(t *testing.T) {
	cases := []struct {
		name      string
		file      string
		body      []byte
		logoStays bool // LogoURL is still advertised on GET /branding
	}{
		{"webp", "logo.webp", webpBytes(), true},
		{"oversized", "logo.png", oversizedPNG(t), true},
		{"png magic then junk", "logo.png", append(append([]byte{}, pngBytes[:8]...), []byte("not a png body")...), true},
		{"missing file", "logo.png", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			assets := map[string][]byte{}
			if tc.body != nil {
				assets[tc.file] = tc.body
			}
			writeBrand(t, root, "fallback.test", map[string]any{"name": "Fallback", "logo": tc.file}, assets)

			logs := &logCapture{}
			now := time.Now()
			store := newBrandingStore(root, slog.New(logs), func() time.Time { return now })

			doc, _ := store.resolve("fallback.test")
			if tc.logoStays && doc.LogoURL == "" {
				t.Error("the login logo was dropped; only the icons should fall back")
			}
			if !tc.logoStays && doc.LogoURL != "" {
				t.Errorf("logoUrl = %q for a missing file", doc.LogoURL)
			}

			for _, spec := range brandingIconSpecs {
				got, gotETag := store.icon("fallback.test", spec)
				want, wantETag := defaultIcon(spec)
				if !bytes.Equal(got, want) || gotETag != wantETag {
					t.Errorf("%s: an unusable logo did not fall back to the embedded default", spec.name)
				}
			}

			// Declared once, however many requests arrived within the TTL.
			// The message names the failure, not the destination: the same
			// line is emitted when the icon falls through to the logo, and
			// the "using" attribute says where the icons came from.
			const msg = "cannot be rendered as PWA icons"
			if n := logs.count(msg); n != 1 {
				t.Errorf("fallback declared %d times within one TTL, want exactly 1", n)
			}
			// ...and once more after it.
			now = now.Add(brandingCacheTTL + time.Second)
			store.resolve("fallback.test")
			if n := logs.count(msg); n != 2 {
				t.Errorf("fallback declared %d times across two TTLs, want 2", n)
			}
		})
	}
}

// TestBrandingIconsSilentWhenNoLogo: a host with no logo is not a fallback
// worth a warning — it is a brand without a mark, served Moov's icons quietly.
func TestBrandingIconsSilentWhenNoLogo(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "nologo.test", map[string]any{"name": "No Logo"}, nil)
	logs := &logCapture{}
	store := newBrandingStore(root, slog.New(logs), nil)
	store.resolve("nologo.test")
	if got, _ := store.icon("nologo.test", brandingIconSpecs[0]); got == nil {
		t.Error("no icon served")
	}
	if n := logs.count("cannot be rendered as PWA icons"); n != 0 {
		t.Errorf("a host without a logo logged %d fallback warnings", n)
	}
}

// TestBrandingIconRouteRefusesUnknownNames: the route serves exactly the
// spec table, and every refusal is the same 404 as an unknown asset.
func TestBrandingIconRouteRefusesUnknownNames(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "names.test", map[string]any{"logo": "logo.png"},
		map[string][]byte{"logo.png": redLogoPNG(t)})
	srv := brandingServer(t, root)

	for _, name := range []string{
		"icon-192", "icon-192.PNG", "icon-192.png.png", "icon-1024.png",
		"favicon.svg", "branding.json", "logo.png", ".png", "..%2F..%2Fbranding.json",
	} {
		rec := getBranding(t, srv, brandingIconPrefix+name, "names.test", nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s%s = %d, want 404", brandingIconPrefix, name, rec.Code)
		}
	}
	// A path that walks up out of the icon route reaches, at most, the mux's
	// redirect to the cleaned path or the public asset it names — never a
	// different file. The asset route's own traversal tests cover the rest.
	rec := getBranding(t, srv, brandingIconPrefix+"../assets/names.test/logo.png", "names.test", nil)
	switch rec.Code {
	case http.StatusNotFound, http.StatusMovedPermanently:
	case http.StatusOK:
		if !bytes.Equal(rec.Body.Bytes(), redLogoPNG(t)) {
			t.Errorf("traversal through the icon route served %d unexpected bytes", rec.Body.Len())
		}
	default:
		t.Errorf("traversal through the icon route = %d", rec.Code)
	}
}

// TestBrandingPWARoutesHostileHost: a Host that fails normalization degrades
// to the defaults on both new routes, never to a filesystem read.
func TestBrandingPWARoutesHostileHost(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "branding")
	if err := os.MkdirAll(filepath.Join(base, "secret"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "secret", "branding.json"),
		[]byte(`{"name":"LEAKED","logo":"logo.png"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "secret", "logo.png"), redLogoPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	srv := brandingServer(t, root)
	defManifest, _ := getManifest(t, srv, "plain.test")
	defIcon := getIcon(t, srv, "plain.test", "icon-192")

	for _, host := range []string{"../secret", "..%2Fsecret", `..\secret`, "[::1]:8620", "mail example.com", ""} {
		body, _ := getManifest(t, srv, host)
		if !bytes.Equal(body, defManifest) {
			t.Errorf("Host %q: manifest differs from the default:\n%s", host, body)
		}
		if strings.Contains(string(body), "LEAKED") {
			t.Fatalf("Host %q escaped the branding root", host)
		}
		if icon := getIcon(t, srv, host, "icon-192"); !bytes.Equal(icon, defIcon) {
			t.Errorf("Host %q: icon differs from the default", host)
		}
	}
}

// TestBrandingIconCacheFollowsLogoAndColour: a changed accent color or logo
// renders fresh icons at the next TTL, and the key tells the inputs apart.
func TestBrandingIconCacheFollowsLogoAndColour(t *testing.T) {
	root := t.TempDir()
	/*
	 * A LIGHT mark on purpose: the plate follows the primary only behind one
	 * (a dark mark gets white whatever the brand color), and this test is
	 * about the plate tracking a color CHANGE across the cache TTL.
	 */
	writeBrand(t, root, "cache.test", map[string]any{
		"logo": "logo.png", "colors": map[string]string{"primary": "#ff0000"},
	}, map[string][]byte{"logo.png": whiteIconPNG(t)})

	now := time.Now()
	store := newBrandingStore(root, nil, func() time.Time { return now })
	spec, _ := iconSpecByName("icon-maskable-192")

	first, firstETag := store.icon("cache.test", spec)
	if c := nrgbaAt(decodePNG(t, first), 1, 1); c != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("plate = %v, want red", c)
	}

	// Within the TTL a color change on disk is not seen (document semantics).
	writeBrand(t, root, "cache.test", map[string]any{
		"logo": "logo.png", "colors": map[string]string{"primary": "#0000ff"},
	}, nil)
	if again, etag := store.icon("cache.test", spec); !bytes.Equal(again, first) || etag != firstETag {
		t.Error("the icon changed inside the TTL")
	}

	// After it, the plate is blue.
	now = now.Add(brandingCacheTTL + time.Second)
	after, afterETag := store.icon("cache.test", spec)
	if c := nrgbaAt(decodePNG(t, after), 1, 1); c != (color.NRGBA{B: 255, A: 255}) {
		t.Errorf("plate after the TTL = %v, want blue", c)
	}
	if afterETag == firstETag {
		t.Error("ETag did not change with the accent color")
	}

	// The key separates every input.
	keys := map[string]bool{
		iconCacheKey("h", "aaa", "logo", "#000", "icon-192"):  true,
		iconCacheKey("h", "aaa", "logo", "#000", "icon-512"):  true,
		iconCacheKey("h", "aaa", "logo", "#fff", "icon-192"):  true,
		iconCacheKey("h", "bbb", "logo", "#000", "icon-192"):  true,
		iconCacheKey("h2", "aaa", "logo", "#000", "icon-192"): true,
		// The same bytes render differently as an icon (always plated) and as
		// a logo, so the source is part of the key too.
		iconCacheKey("h", "aaa", "icon", "#000", "icon-192"): true,
	}
	if len(keys) != 6 {
		t.Errorf("icon cache keys collide: %d distinct of 6", len(keys))
	}
}

// TestDefaultFaviconIsDerivedAndStable: the one default that is rendered
// rather than embedded has the right size and the same bytes every time.
func TestDefaultFaviconIsDerivedAndStable(t *testing.T) {
	spec, _ := iconSpecByName("favicon-32")
	body, etag := defaultIcon(spec)
	img := decodePNG(t, body)
	if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 32 {
		t.Errorf("default favicon = %dx%d, want 32x32", b.Dx(), b.Dy())
	}
	if again, etag2 := defaultIcon(spec); !bytes.Equal(again, body) || etag2 != etag {
		t.Error("default favicon is not stable across calls")
	}
	// Not blank: the 192 px mark survives the downscale.
	opaque := 0
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			if nrgbaAt(img, x, y).A > 0 {
				opaque++
			}
		}
	}
	if opaque == 0 {
		t.Error("default favicon is fully transparent")
	}
}

// --- ValidateBrandingIconSource ---------------------------------------------

func TestValidateBrandingIconSource(t *testing.T) {
	cases := []struct {
		name    string
		body    []byte
		wantErr string // substring; "" means valid
	}{
		{"png", redLogoPNG(t), ""},
		{"tiny png", pngBytes, ""},
		{"webp", webpBytes(), "WebP"},
		{"gif stub header only", gifBytes(), "decoded"},
		{"oversized", oversizedPNG(t), "limit"},
		{"svg", []byte("<svg/>"), "not a PNG"},
		{"empty", nil, "not a PNG"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBrandingIconSource(tc.body)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Errorf("accepted, want an error mentioning %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// --- shortName and the ETag -------------------------------------------------

func TestDeriveShortName(t *testing.T) {
	cases := map[string]string{
		"Moov Mail":                 "Moov Mail",
		"Acme":                      "Acme",
		"  Acme Mail  ":             "Acme Mail",
		"Corporate Mailbox Acme":    "Corporate",
		"Supercalifragilistic Mail": "Supercalifra",
		"Ñandú Correo Electrónico":  "Ñandú",
		"ExactlyTwelv":              "ExactlyTwelv",
		"ThirteenChars":             "ThirteenChar",
	}
	for in, want := range cases {
		if got := deriveShortName(in); got != want {
			t.Errorf("deriveShortName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBrandingShortNameInDocument: GET /branding carries shortName, derived,
// configured, or capped at twelve runes.
func TestBrandingShortNameInDocument(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "derived.test", map[string]any{"name": "Acme Corporate Mailbox"}, nil)
	writeBrand(t, root, "explicit.test", map[string]any{"name": "Acme Correo", "shortName": "Correo Acme"}, nil)
	writeBrand(t, root, "toolong.test", map[string]any{"name": "Acme", "shortName": "ABCDEFGHIJKLMNOP"}, nil)
	writeBrand(t, root, "nameless.test", map[string]any{"tagline": "just a tagline"}, nil)
	srv := brandingServer(t, root)

	if doc := decodeBranding(t, getBranding(t, srv, PathBranding, "nobody.test", nil)); doc.ShortName != "Moov" {
		t.Errorf("default shortName = %q, want Moov", doc.ShortName)
	}
	if doc := decodeBranding(t, getBranding(t, srv, PathBranding, "derived.test", nil)); doc.ShortName != "Acme" {
		t.Errorf("derived shortName = %q, want Acme", doc.ShortName)
	}
	if doc := decodeBranding(t, getBranding(t, srv, PathBranding, "explicit.test", nil)); doc.ShortName != "Correo Acme" {
		t.Errorf("explicit shortName = %q", doc.ShortName)
	}
	if doc := decodeBranding(t, getBranding(t, srv, PathBranding, "toolong.test", nil)); doc.ShortName != "ABCDEFGHIJKL" {
		t.Errorf("capped shortName = %q, want 12 runes", doc.ShortName)
	}
	// A configured host that did not change the name keeps Moov's authored
	// short name rather than a derivation of "Moov Mail".
	if doc := decodeBranding(t, getBranding(t, srv, PathBranding, "nameless.test", nil)); doc.ShortName != "Moov" {
		t.Errorf("nameless shortName = %q, want Moov", doc.ShortName)
	}
}

// TestBrandingETagCoversEveryField mutates each field of the document in turn
// and demands a fresh ETag. supportUrl was once missing from the fingerprint,
// which meant changing it did not invalidate any cache until the max-age ran
// out; this test is how that class of bug stays fixed.
func TestBrandingETagCoversEveryField(t *testing.T) {
	base := DefaultBranding()
	baseTag := brandingETag(base)

	mutations := map[string]func(*Branding){
		"Name":              func(d *Branding) { d.Name += "x" },
		"ShortName":         func(d *Branding) { d.ShortName += "x" },
		"LogoURL":           func(d *Branding) { d.LogoURL = "/branding/assets/h/logo.png" },
		"SplashURL":         func(d *Branding) { d.SplashURL = "/branding/assets/h/splash.png" },
		"IconURL":           func(d *Branding) { d.IconURL = "/branding/assets/h/icon.png" },
		"LogoDarkURL":       func(d *Branding) { d.LogoDarkURL = "/branding/assets/h/logo-dark.png" },
		"Colors.Primary":    func(d *Branding) { d.Colors.Primary = "#000001" },
		"Colors.OnPrimary":  func(d *Branding) { d.Colors.OnPrimary = "#000001" },
		"Colors.SplashFrom": func(d *Branding) { d.Colors.SplashFrom = "#000001" },
		"Colors.SplashTo":   func(d *Branding) { d.Colors.SplashTo = "#000001" },
		"Tagline":           func(d *Branding) { d.Tagline = "t" },
		"SupportURL":        func(d *Branding) { d.SupportURL = "mailto:it@example.com" },
		"PrivacyURL":        func(d *Branding) { d.PrivacyURL = "https://example.com/privacy" },
		"TermsURL":          func(d *Branding) { d.TermsURL = "https://example.com/terms" },
		"Default":           func(d *Branding) { d.Default = !d.Default },
	}
	for field, mutate := range mutations {
		doc := base
		mutate(&doc)
		if brandingETag(doc) == baseTag {
			t.Errorf("changing %s did not change the ETag", field)
		}
	}

	// And the pin that the list above is complete: every exported field of
	// Branding (flattened) is named in mutations.
	var fields []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		raw, _ := json.Marshal(v)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(raw, &m)
		for k := range m {
			var nested map[string]json.RawMessage
			if json.Unmarshal(m[k], &nested) == nil && len(nested) > 0 {
				var sub any
				_ = json.Unmarshal(m[k], &sub)
				walk(prefix+k+".", sub)
				continue
			}
			fields = append(fields, prefix+k)
		}
	}
	full := base
	// omitempty fields must be present to be seen
	full.Tagline, full.SupportURL = "t", "u"
	full.PrivacyURL, full.TermsURL = "p", "q"
	walk("", full)
	jsonToGo := map[string]string{
		"name": "Name", "shortName": "ShortName", "logoUrl": "LogoURL", "splashUrl": "SplashURL", "iconUrl": "IconURL",
		"logoDarkUrl":    "LogoDarkURL",
		"colors.primary": "Colors.Primary", "colors.onPrimary": "Colors.OnPrimary",
		"colors.splashFrom": "Colors.SplashFrom", "colors.splashTo": "Colors.SplashTo",
		"tagline": "Tagline", "supportUrl": "SupportURL",
		"privacyUrl": "PrivacyURL", "termsUrl": "TermsURL",
		"default": "Default",
	}
	for _, f := range fields {
		goName, known := jsonToGo[f]
		if !known {
			t.Errorf("Branding has a new field %q: add it to brandingETag AND to this test", f)
			continue
		}
		if _, covered := mutations[goName]; !covered {
			t.Errorf("field %s is not exercised by the ETag test", goName)
		}
	}
}
