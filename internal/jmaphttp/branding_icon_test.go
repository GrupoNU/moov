package jmaphttp

import (
	"bytes"
	"image"
	"image/color"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/branding"
)

// Tests for the optional square icon.
//
// A brand's logo and a brand's launcher icon are not the same picture. The
// maskable and Apple icons sit on an OPAQUE plate of the primary color, so a
// customer whose primary is #000000 and whose logo is a black wordmark gets a
// black glyph on a black plate. The icon field is where the square glyph a
// brand kit keeps for dark backgrounds goes, and these tests pin the chain it
// introduces: icon, then logo, then Moov's own.

// whiteIconPNG is a 64x64 white square: what a real dark-background glyph
// looks like to the renderer, and impossible to confuse with the wide red logo.
func whiteIconPNG(t *testing.T) []byte {
	t.Helper()
	return encodePNG(t, solidImage(64, 64, color.NRGBA{R: 255, G: 255, B: 255, A: 255}))
}

// blackGlyphPNG is Areacorp's shape: a 64x64 BLACK mark on a transparent
// canvas — the file a brand kit keeps for light backgrounds, and the one whose
// plate must come out white rather than the brand's near-black primary.
//
// Transparent around the mark rather than a solid square, because that is what
// makes it a real test of the luminance rule: an average over EVERY pixel
// would be dominated by the empty canvas, and only the alpha-weighted mean of
// the visible pixels answers "the mark is dark".
func blackGlyphPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	// A centered 32x32 block: half the edge, so it survives the maskable safe
	// zone and still leaves a transparent border to average over.
	for y := 16; y < 48; y++ {
		for x := 16; x < 48; x++ {
			img.SetNRGBA(x, y, color.NRGBA{A: 255})
		}
	}
	return encodePNG(t, img)
}

// TestBrandingIconPreferredOverLogo is the customer case that motivated the
// field: a black primary and a black wordmark, rescued by a white square icon.
// The center pixel of icon-maskable-192 is the proof — it is the plate color
// when the wordmark is used and the glyph's white when the icon is.
func TestBrandingIconPreferredOverLogo(t *testing.T) {
	black := color.NRGBA{A: 255}
	blackWordmark := encodePNG(t, solidImage(200, 40, black))

	// Without an icon: the wordmark on the black plate, invisible.
	logoOnly := t.TempDir()
	writeBrand(t, logoOnly, "areacorp.test", map[string]any{
		"name": "Areacorp", "logo": "logo.png",
		"colors": map[string]string{"primary": "#000000"},
	}, map[string][]byte{"logo.png": blackWordmark})

	// With one: the white glyph, legible.
	withIcon := t.TempDir()
	writeBrand(t, withIcon, "areacorp.test", map[string]any{
		"name": "Areacorp", "logo": "logo.png", "icon": "icon.png",
		"colors": map[string]string{"primary": "#000000"},
	}, map[string][]byte{"logo.png": blackWordmark, "icon.png": whiteIconPNG(t)})

	before := getIcon(t, brandingServer(t, logoOnly), "areacorp.test", "icon-maskable-192")
	after := getIcon(t, brandingServer(t, withIcon), "areacorp.test", "icon-maskable-192")
	if bytes.Equal(before, after) {
		t.Fatal("the configured icon did not change the rendered launcher icon")
	}
	if c := nrgbaAt(decodePNG(t, before), 96, 96); c != black {
		t.Errorf("without an icon the center pixel = %v, want the black wordmark on the black plate", c)
	}
	if c := nrgbaAt(decodePNG(t, after), 96, 96); c.R < 250 || c.G < 250 || c.B < 250 || c.A != 255 {
		t.Errorf("with an icon the center pixel = %v, want the white glyph", c)
	}
	// The plate is still the brand's: a square glyph fills the inner box, so
	// the padding is where the plate shows.
	if c := nrgbaAt(decodePNG(t, after), 4, 4); c != black {
		t.Errorf("the padding = %v, want the brand's plate", c)
	}

	// The document describes the brand fully: both URLs, both on this origin.
	srv := brandingServer(t, withIcon)
	doc := decodeBranding(t, getBranding(t, srv, PathBranding, "areacorp.test", nil))
	if doc.IconURL != "/branding/assets/areacorp.test/icon.png" {
		t.Errorf("iconUrl = %q", doc.IconURL)
	}
	if doc.LogoURL != "/branding/assets/areacorp.test/logo.png" {
		t.Errorf("logoUrl = %q; the top bar's logo is untouched by the icon", doc.LogoURL)
	}
	// And the icon is fetchable, like any other asset.
	if rec := getBranding(t, srv, doc.IconURL, "areacorp.test", nil); rec.Code != http.StatusOK {
		t.Errorf("GET %s = %d, want 200", doc.IconURL, rec.Code)
	}
}

// TestBrandingIconFallsBackToLogo: a WebP, undecodable or missing icon is not
// the end of the chain — the logo renders, and the declaration names the FILE
// that failed and where the icons came from instead.
func TestBrandingIconFallsBackToLogo(t *testing.T) {
	cases := map[string][]byte{
		"webp":         webpBytes(),
		"undecodable":  append(append([]byte{}, pngBytes[:8]...), []byte("not a png body")...),
		"missing file": nil,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			assets := map[string][]byte{"logo.png": redLogoPNG(t)}
			file := "icon.png"
			if name == "webp" {
				file = "icon.webp"
			}
			if body != nil {
				assets[file] = body
			}
			writeBrand(t, root, "chain.test", map[string]any{
				"name": "Chain", "logo": "logo.png", "icon": file,
			}, assets)

			logs := &logCapture{}
			store := newBrandingStore(root, slog.New(logs), nil)
			entry := store.resolveEntry("chain.test")
			if entry.iconSource != brandingSourceLogo {
				t.Fatalf("icon source = %q, want the logo", entry.iconSource)
			}
			if entry.iconFile != "logo.png" {
				t.Errorf("icon file = %q, want logo.png", entry.iconFile)
			}
			if entry.iconIssue == "" {
				t.Fatal("the fall-through to the logo was not declared")
			}
			for _, want := range []string{file, "icon, then logo, then Moov", "the configured logo"} {
				if !strings.Contains(entry.iconIssue, want) {
					t.Errorf("the declaration does not mention %q:\n%s", want, entry.iconIssue)
				}
			}
			if n := logs.count("cannot be rendered as PWA icons"); n != 1 {
				t.Errorf("declared %d times within one TTL, want 1", n)
			}

			// The icons ARE the logo's: not Moov's, and identical to what the
			// same brand renders with no icon configured at all.
			spec, _ := iconSpecByName("icon-192")
			got, _ := store.icon("chain.test", spec)
			if def, _ := defaultIcon(spec); bytes.Equal(got, def) {
				t.Error("a usable logo was skipped for Moov's icons")
			}
			plain := t.TempDir()
			writeBrand(t, plain, "chain.test", map[string]any{"name": "Chain", "logo": "logo.png"},
				map[string][]byte{"logo.png": redLogoPNG(t)})
			want, _ := newBrandingStore(plain, nil, nil).icon("chain.test", spec)
			if !bytes.Equal(got, want) {
				t.Error("the logo fallback did not render exactly what the logo alone renders")
			}

			// A WebP or undecodable icon still displays wherever a client wants
			// it: only the rendering fell back.
			if name != "missing file" && entry.doc.IconURL == "" {
				t.Error("the icon URL was dropped; only the rendering should fall back")
			}
			if name == "missing file" && entry.doc.IconURL != "" {
				t.Errorf("iconUrl = %q for a missing file", entry.doc.IconURL)
			}
		})
	}
}

// TestBrandingIconAndLogoBothUnusableFallsBackToMoov: the end of the chain,
// with both failures named in one declaration.
func TestBrandingIconAndLogoBothUnusableFallsBackToMoov(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "both.test", map[string]any{
		"name": "Both", "logo": "logo.webp", "icon": "icon.webp",
	}, map[string][]byte{"logo.webp": webpBytes(), "icon.webp": webpBytes()})

	logs := &logCapture{}
	store := newBrandingStore(root, slog.New(logs), nil)
	entry := store.resolveEntry("both.test")
	if entry.iconSource != "" || entry.iconFile != "" {
		t.Fatalf("icon source = %q/%q, want none", entry.iconSource, entry.iconFile)
	}
	for _, want := range []string{"icon.webp", "logo.webp", "Moov's icons"} {
		if !strings.Contains(entry.iconIssue, want) {
			t.Errorf("the declaration does not mention %q:\n%s", want, entry.iconIssue)
		}
	}
	if n := logs.count("cannot be rendered as PWA icons"); n != 1 {
		t.Errorf("declared %d times, want 1", n)
	}
	for _, spec := range brandingIconSpecs {
		got, gotETag := store.icon("both.test", spec)
		want, wantETag := defaultIcon(spec)
		if !bytes.Equal(got, want) || gotETag != wantETag {
			t.Errorf("%s: did not fall back to the embedded default", spec.name)
		}
	}
}

// TestBrandingIconCacheKeyFollowsTheIconDigest: replacing the icon file
// renders fresh icons at the next TTL.
func TestBrandingIconCacheKeyFollowsTheIconDigest(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "digest.test", map[string]any{
		"logo": "logo.png", "icon": "icon.png",
		"colors": map[string]string{"primary": "#ff0000"},
	}, map[string][]byte{"logo.png": redLogoPNG(t), "icon.png": whiteIconPNG(t)})

	now := time.Now()
	store := newBrandingStore(root, nil, func() time.Time { return now })
	spec, _ := iconSpecByName("icon-192")

	first, firstETag := store.icon("digest.test", spec)
	firstSum := store.resolveEntry("digest.test").iconSum

	// A different image under the same NAME: inside the TTL nothing moves.
	writeBrand(t, root, "digest.test", nil, map[string][]byte{
		"icon.png": encodePNG(t, solidImage(64, 64, color.NRGBA{G: 200, A: 255})),
	})
	if again, etag := store.icon("digest.test", spec); !bytes.Equal(again, first) || etag != firstETag {
		t.Error("the icon changed inside the TTL")
	}

	// After it, a new digest, a new key and new bytes.
	now = now.Add(brandingCacheTTL + time.Second)
	after, afterETag := store.icon("digest.test", spec)
	afterSum := store.resolveEntry("digest.test").iconSum
	if afterSum == firstSum {
		t.Fatal("the icon digest did not follow the file")
	}
	if iconCacheKey("digest.test", firstSum, brandingSourceIcon, "#ff0000", spec.name) ==
		iconCacheKey("digest.test", afterSum, brandingSourceIcon, "#ff0000", spec.name) {
		t.Error("the cache key did not change with the icon digest")
	}
	if bytes.Equal(after, first) || afterETag == firstETag {
		t.Error("replacing the icon did not render fresh bytes after the TTL")
	}
	if c := nrgbaAt(decodePNG(t, after), 96, 96); c.G < 150 || c.R > 100 {
		t.Errorf("center = %v, want the replaced green glyph", c)
	}

	// And the digest that keys the cache is the ICON's, not the logo's: the
	// same host with the icon removed keys on a different sum.
	logoOnly := t.TempDir()
	writeBrand(t, logoOnly, "digest.test", map[string]any{
		"logo": "logo.png", "colors": map[string]string{"primary": "#ff0000"},
	}, map[string][]byte{"logo.png": redLogoPNG(t)})
	logoSum := newBrandingStore(logoOnly, nil, nil).resolveEntry("digest.test").iconSum
	if logoSum == afterSum || logoSum == firstSum {
		t.Error("the icon and the logo produced the same cache digest")
	}
}

// TestBrandingIconETagChanges: iconUrl is part of the document's fingerprint,
// so configuring one invalidates a cached /branding rather than leaving the
// old document in front of a browser for the whole max-age.
func TestBrandingIconETagChanges(t *testing.T) {
	base := DefaultBranding()
	withIcon := base
	withIcon.IconURL = "/branding/assets/h/icon.png"
	if brandingETag(base) == brandingETag(withIcon) {
		t.Error("changing iconUrl did not change the ETag")
	}

	root := t.TempDir()
	writeBrand(t, root, "etag.test", map[string]any{"name": "Etag", "logo": "logo.png"},
		map[string][]byte{"logo.png": redLogoPNG(t), "icon.png": whiteIconPNG(t)})
	now := time.Now()
	store := newBrandingStore(root, nil, func() time.Time { return now })
	_, firstETag := store.resolve("etag.test")

	writeBrand(t, root, "etag.test", map[string]any{
		"name": "Etag", "logo": "logo.png", "icon": "icon.png",
	}, nil)
	now = now.Add(brandingCacheTTL + time.Second)
	doc, afterETag := store.resolve("etag.test")
	if doc.IconURL == "" {
		t.Fatal("the icon was not picked up")
	}
	if afterETag == firstETag {
		t.Error("adding an icon did not change the document ETag")
	}
}

// TestBrandingIconDoesNotLeakExistence: configuring an icon must not break the
// property the whole endpoint rests on — an unconfigured host and a host
// configured to look like Moov answer byte for byte the same.
func TestBrandingIconDoesNotLeakExistence(t *testing.T) {
	root := t.TempDir()
	def := DefaultBranding()
	// A Moov-lookalike: Moov's name and colors, and NO assets — exactly what
	// an unconfigured host resolves to.
	writeBrand(t, root, "lookalike.test", map[string]any{
		"name": def.Name, "shortName": def.ShortName,
		"colors": map[string]string{
			"primary": def.Colors.Primary, "onPrimary": def.Colors.OnPrimary,
			"splashFrom": def.Colors.SplashFrom, "splashTo": def.Colors.SplashTo,
		},
	}, nil)
	srv := brandingServer(t, root)

	for _, spec := range brandingIconSpecs {
		if !bytes.Equal(getIcon(t, srv, "lookalike.test", spec.name), getIcon(t, srv, "absent.test", spec.name)) {
			t.Errorf("%s differs between a lookalike and an unconfigured host", spec.name)
		}
	}
	// Only "default" may differ — it reports what is being shown, not whether
	// a configuration exists, and it was already the documented exception.
	da := decodeBranding(t, getBranding(t, srv, PathBranding, "lookalike.test", nil))
	db := decodeBranding(t, getBranding(t, srv, PathBranding, "absent.test", nil))
	if da.IconURL != "" || db.IconURL != "" {
		t.Errorf("iconUrl leaked: %q vs %q", da.IconURL, db.IconURL)
	}
	da.Default, db.Default = false, false
	if da != db {
		t.Errorf("documents differ:\n%+v\n%+v", da, db)
	}
}

// TestFaviconAndAnyIconsAreNeverPlated: the mark AS IT WAS UPLOADED, on a
// transparent canvas — whichever file it came from.
//
// An earlier version plated a dedicated `icon` at EVERY size, to rescue a
// white-on-dark glyph that vanished on the transparent ones. The cost was a
// frame around every other brand's tab icon, and the owner caught it: a plate
// in the tab is chrome the operator did not draw. The white glyph is handled
// by the plate COLOR on the icons that are plated, plus a declared warning.
func TestFaviconAndAnyIconsAreNeverPlated(t *testing.T) {
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}

	root := t.TempDir()
	writeBrand(t, root, "flat.test", map[string]any{
		"name": "Areacorp", "logo": "logo.png", "icon": "icon.png",
		"colors": map[string]string{"primary": "#000000"},
	}, map[string][]byte{
		"logo.png": encodePNG(t, solidImage(200, 40, white)),
		"icon.png": blackGlyphPNG(t),
	})
	srv := brandingServer(t, root)

	for _, spec := range brandingIconSpecs {
		if spec.opaque {
			continue
		}
		t.Run(spec.name, func(t *testing.T) {
			img := decodePNG(t, getIcon(t, srv, "flat.test", spec.name))
			if a := nrgbaAt(img, 0, 0).A; a != 0 {
				t.Errorf("corner of %s has alpha %d, want a transparent canvas", spec.name, a)
			}
		})
	}

	// favicon-32 keeps pad 0 now that it is never plated: at 32 px every pixel
	// counts, and the padding floor existed only to frame a plate.
	fav, ok := iconSpecByName("favicon-32")
	if !ok || fav.pad != 0 || fav.opaque {
		t.Fatalf("favicon-32 spec = %+v, want transparent with no padding", fav)
	}
}

// TestPlateColorFollowsTheMark: white behind a dark mark, the primary behind a
// light one.
//
// The failure it prevents is the one that made `icon` exist in the first place
// and then outlived it: a brand whose primary is near-black and whose glyph is
// black rendered a black mark on a black plate. Painting the primary is right
// only when the mark contrasts with it, and the only thing we know about the
// mark is its pixels.
func TestPlateColorFollowsTheMark(t *testing.T) {
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	black := color.NRGBA{A: 255}
	teal := color.NRGBA{R: 0x0f, G: 0x76, B: 0x6e, A: 255}

	// Areacorp's case: a BLACK glyph on a brand whose primary is black.
	darkRoot := t.TempDir()
	writeBrand(t, darkRoot, "dark.test", map[string]any{
		"name": "Areacorp", "icon": "icon.png",
		"colors": map[string]string{"primary": "#000000"},
	}, map[string][]byte{"icon.png": blackGlyphPNG(t)})

	// A LIGHT glyph, on a brand with a real primary: the plate is that primary,
	// which is what the glyph was drawn for.
	lightRoot := t.TempDir()
	writeBrand(t, lightRoot, "light.test", map[string]any{
		"name": "Lightcorp", "icon": "icon.png",
		"colors": map[string]string{"primary": "#0f766e"},
	}, map[string][]byte{"icon.png": whiteIconPNG(t)})

	darkSrv := brandingServer(t, darkRoot)
	lightSrv := brandingServer(t, lightRoot)

	for _, spec := range brandingIconSpecs {
		if !spec.opaque {
			continue
		}
		t.Run(spec.name, func(t *testing.T) {
			// A dark mark gets a WHITE plate — the ground almost every dark
			// logo was drawn against, and the one that keeps it visible.
			darkImg := decodePNG(t, getIcon(t, darkSrv, "dark.test", spec.name))
			if c := nrgbaAt(darkImg, 0, 0); c != white {
				t.Errorf("dark mark: corner of %s = %v, want the white plate", spec.name, c)
			}
			if c := nrgbaAt(darkImg, spec.size/2, spec.size/2); c != black {
				t.Errorf("dark mark: center of %s = %v, want the black glyph", spec.name, c)
			}

			// A light mark gets the brand's primary.
			lightImg := decodePNG(t, getIcon(t, lightSrv, "light.test", spec.name))
			if c := nrgbaAt(lightImg, 0, 0); c != teal {
				t.Errorf("light mark: corner of %s = %v, want the primary %v", spec.name, c, teal)
			}
			if c := nrgbaAt(lightImg, spec.size/2, spec.size/2); c != white {
				t.Errorf("light mark: center of %s = %v, want the white glyph", spec.name, c)
			}

			// Opaque EVERYWHERE, either way: a hole is what a masking launcher
			// and iOS both render badly.
			for _, img := range []image.Image{darkImg, lightImg} {
				for y := 0; y < spec.size; y += 7 {
					for x := 0; x < spec.size; x += 7 {
						if a := nrgbaAt(img, x, y).A; a != 255 {
							t.Fatalf("pixel (%d,%d) of %s has alpha %d", x, y, spec.name, a)
						}
					}
				}
			}
		})
	}
}

// TestBlackGlyphIsVisibleOnBothTheTabAndTheLauncher is the owner's case stated
// as the outcome rather than as the mechanism: Areacorp's black mark must be
// visible in a browser tab AND on a home screen, which the single-color plate
// could not deliver at the same time.
func TestBlackGlyphIsVisibleOnBothTheTabAndTheLauncher(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "areacorp.test", map[string]any{
		"name": "Areacorp", "icon": "icon.png",
		"colors": map[string]string{"primary": "#000000"},
	}, map[string][]byte{"icon.png": blackGlyphPNG(t)})
	srv := brandingServer(t, root)

	// The tab: transparent canvas, and the glyph's own dark pixels present.
	fav := decodePNG(t, getIcon(t, srv, "areacorp.test", "favicon-32"))
	if a := nrgbaAt(fav, 0, 0).A; a != 0 {
		t.Errorf("favicon corner alpha = %d, want transparent", a)
	}
	center := nrgbaAt(fav, 16, 16)
	if center.A != 255 || center.R > 32 || center.G > 32 || center.B > 32 {
		t.Errorf("favicon center = %v, want the opaque dark glyph", center)
	}

	// The launcher: a white plate, so the same black glyph reads on it.
	mask := decodePNG(t, getIcon(t, srv, "areacorp.test", "icon-maskable-192"))
	if c := nrgbaAt(mask, 0, 0); c != (color.NRGBA{R: 255, G: 255, B: 255, A: 255}) {
		t.Errorf("maskable corner = %v, want the white plate", c)
	}
}

// TestLightIconOnTransparentIsDeclared: the case nothing can fix honestly is
// declared instead.
//
// Plating the favicon is what this change removed; recoloring somebody's mark
// wrecks any logo that is not a flat silhouette. What the operator can do, and
// we cannot, is supply a version with a dark outline — so they are told.
func TestLightIconOnTransparentIsDeclared(t *testing.T) {
	if !branding.IconOnTransparentMayVanish(whiteIconPNG(t)) {
		t.Error("a white glyph must be declared as possibly vanishing on a light tab")
	}
	if branding.IconOnTransparentMayVanish(blackGlyphPNG(t)) {
		t.Error("a black glyph has no such problem and must not be warned about")
	}

	notes := branding.IconSourceNotes(branding.AssetIcon, whiteIconPNG(t))
	found := false
	for _, n := range notes {
		if n == branding.LightIconOnTransparentNote {
			found = true
		}
	}
	if !found {
		t.Errorf("IconSourceNotes = %v, want it to carry the light-icon note", notes)
	}
}

// --- the optional dark-background wordmark -----------------------------------
//
// The mirror of the problem the square icon solves, found on the same live
// gate: Areacorp's wordmark is black, so on the dark login panel it is a black
// mark on a dark ground. `logoDark` is where a brand kit's dark-background
// wordmark goes. It is advertised on presence and validity alone and plays NO
// part in generating the icons — that chain stays icon, then logo, then Moov's.

func TestBrandingLogoDarkAdvertised(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "dark.test", map[string]any{
		"name": "Areacorp", "logo": "logo.png", "logoDark": "logo-dark.png",
	}, map[string][]byte{
		"logo.png":      redLogoPNG(t),
		"logo-dark.png": whiteIconPNG(t),
	})
	srv := brandingServer(t, root)
	doc := decodeBranding(t, getBranding(t, srv, PathBranding, "dark.test", nil))

	if doc.LogoDarkURL != "/branding/assets/dark.test/logo-dark.png" {
		t.Errorf("logoDarkUrl = %q", doc.LogoDarkURL)
	}
	if doc.LogoURL != "/branding/assets/dark.test/logo.png" {
		t.Errorf("logoUrl = %q; the light logo is untouched", doc.LogoURL)
	}
	// It serves bytes, like any other asset, with the same hostile-content
	// headers.
	rec := getBranding(t, srv, doc.LogoDarkURL, "dark.test", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", doc.LogoDarkURL, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff on the dark logo")
	}
}

// TestBrandingLogoDarkAbsentOrInvalid: not configured, missing on disk, not an
// image, or a name that is not a safe single component — all of them mean the
// same thing, an empty logoDarkUrl, with no effect on anything else.
func TestBrandingLogoDarkAbsentOrInvalid(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		assets     map[string][]byte
	}{
		{"not configured", "", map[string][]byte{"logo.png": redLogoPNG(t)}},
		{"missing on disk", "logo-dark.png", map[string][]byte{"logo.png": redLogoPNG(t)}},
		{"not an image", "logo-dark.png", map[string][]byte{
			"logo.png": redLogoPNG(t), "logo-dark.png": []byte("<!doctype html><script>alert(1)</script>"),
		}},
		{"traversal", "../secret.png", map[string][]byte{"logo.png": redLogoPNG(t)}},
		{"the config file itself", brandingConfigFile, map[string][]byte{"logo.png": redLogoPNG(t)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			doc := map[string]any{"name": "Dark", "logo": "logo.png"}
			if tc.configured != "" {
				doc["logoDark"] = tc.configured
			}
			writeBrand(t, root, "dark.test", doc, tc.assets)

			store := newBrandingStore(root, nil, nil)
			entry := store.resolveEntry("dark.test")
			if entry.doc.LogoDarkURL != "" {
				t.Errorf("logoDarkUrl = %q, want it not advertised", entry.doc.LogoDarkURL)
			}
			// The rest of the brand is unaffected, and the icons still come
			// from the light logo: a dark wordmark is not an icon source.
			if entry.doc.LogoURL == "" {
				t.Error("the light logo was dropped")
			}
			if entry.iconSource != brandingSourceLogo || entry.iconFile != "logo.png" {
				t.Errorf("icon source = %q/%q, want the logo", entry.iconSource, entry.iconFile)
			}
			if entry.iconIssue != "" {
				t.Errorf("a dark-logo problem was declared as an ICON problem: %s", entry.iconIssue)
			}
		})
	}
}

// TestBrandingLogoDarkIsNotAnIconSource: even when it is the ONLY usable
// image, the icons are Moov's. A second wordmark must never become the mark on
// a home screen, or the phone and the top bar could disagree.
func TestBrandingLogoDarkIsNotAnIconSource(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "only.test", map[string]any{
		"name": "Only Dark", "logoDark": "logo-dark.png",
	}, map[string][]byte{"logo-dark.png": whiteIconPNG(t)})

	store := newBrandingStore(root, nil, nil)
	entry := store.resolveEntry("only.test")
	if entry.doc.LogoDarkURL == "" {
		t.Fatal("the dark logo was not advertised")
	}
	if entry.iconFile != "" || entry.iconSource != "" {
		t.Errorf("the dark logo became an icon source: %q/%q", entry.iconFile, entry.iconSource)
	}
	for _, spec := range brandingIconSpecs {
		got, gotETag := store.icon("only.test", spec)
		want, wantETag := defaultIcon(spec)
		if !bytes.Equal(got, want) || gotETag != wantETag {
			t.Errorf("%s: a dark wordmark was rendered as an icon", spec.name)
		}
	}
}

// TestBrandingLogoDarkETagChanges: logoDarkUrl is in the fingerprint, so
// adding one invalidates a cached document instead of leaving the old one in
// front of a browser for the whole max-age.
func TestBrandingLogoDarkETagChanges(t *testing.T) {
	base := DefaultBranding()
	withDark := base
	withDark.LogoDarkURL = "/branding/assets/h/logo-dark.png"
	if brandingETag(base) == brandingETag(withDark) {
		t.Error("changing logoDarkUrl did not change the ETag")
	}

	root := t.TempDir()
	writeBrand(t, root, "etagdark.test", map[string]any{"name": "Etag", "logo": "logo.png"},
		map[string][]byte{"logo.png": redLogoPNG(t), "logo-dark.png": whiteIconPNG(t)})
	now := time.Now()
	store := newBrandingStore(root, nil, func() time.Time { return now })
	_, first := store.resolve("etagdark.test")

	writeBrand(t, root, "etagdark.test", map[string]any{
		"name": "Etag", "logo": "logo.png", "logoDark": "logo-dark.png",
	}, nil)
	now = now.Add(brandingCacheTTL + time.Second)
	doc, after := store.resolve("etagdark.test")
	if doc.LogoDarkURL == "" {
		t.Fatal("the dark logo was not picked up")
	}
	if after == first {
		t.Error("adding a dark logo did not change the document ETag")
	}
}

// TestBrandingLogoDarkDoesNotLeakExistence: an unconfigured host and a
// Moov-lookalike still answer identically, dark logo included.
func TestBrandingLogoDarkDoesNotLeakExistence(t *testing.T) {
	root := t.TempDir()
	def := DefaultBranding()
	writeBrand(t, root, "lookalikedark.test", map[string]any{
		"name": def.Name, "shortName": def.ShortName,
		"colors": map[string]string{
			"primary": def.Colors.Primary, "onPrimary": def.Colors.OnPrimary,
			"splashFrom": def.Colors.SplashFrom, "splashTo": def.Colors.SplashTo,
		},
	}, nil)
	srv := brandingServer(t, root)

	a := decodeBranding(t, getBranding(t, srv, PathBranding, "lookalikedark.test", nil))
	b := decodeBranding(t, getBranding(t, srv, PathBranding, "absent.test", nil))
	if a.LogoDarkURL != "" || b.LogoDarkURL != "" {
		t.Errorf("logoDarkUrl leaked: %q vs %q", a.LogoDarkURL, b.LogoDarkURL)
	}
	a.Default, b.Default = false, false
	if a != b {
		t.Errorf("documents differ:\n%+v\n%+v", a, b)
	}
}
