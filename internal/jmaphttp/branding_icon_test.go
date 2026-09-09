package jmaphttp

import (
	"bytes"
	"image/color"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
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

// TestBrandingIconIsPlatedAtEverySize is the rule found on the pilot with a
// real brand kit: Areacorp's primary is #000000 and its icon is a white glyph
// drawn for that plate. The maskable pair rendered correctly, and then the
// glyph VANISHED on icon-192, icon-512 and favicon-32 — transparent canvases,
// white mark, light desktop and light browser tab.
//
// So when the source is the dedicated icon, every generated size is opaque on
// the primary. When the source is the logo, nothing changed: a wordmark on a
// transparent square is what a launcher and a tab have always been handed.
func TestBrandingIconIsPlatedAtEverySize(t *testing.T) {
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	black := color.NRGBA{A: 255}

	withIcon := t.TempDir()
	writeBrand(t, withIcon, "plated.test", map[string]any{
		"name": "Areacorp", "logo": "logo.png", "icon": "icon.png",
		"colors": map[string]string{"primary": "#000000"},
	}, map[string][]byte{
		"logo.png": encodePNG(t, solidImage(200, 40, white)),
		"icon.png": whiteIconPNG(t),
	})
	logoOnly := t.TempDir()
	writeBrand(t, logoOnly, "plated.test", map[string]any{
		"name": "Areacorp", "logo": "logo.png",
		"colors": map[string]string{"primary": "#000000"},
	}, map[string][]byte{"logo.png": encodePNG(t, solidImage(200, 40, white))})

	iconSrv := brandingServer(t, withIcon)
	logoSrv := brandingServer(t, logoOnly)

	for _, spec := range brandingIconSpecs {
		t.Run(spec.name, func(t *testing.T) {
			// Source = icon: the corner is the primary, opaque, at EVERY size —
			// including the two that used to be transparent and favicon-32,
			// which has no padding at all and so is plate only where the glyph
			// does not reach.
			img := decodePNG(t, getIcon(t, iconSrv, "plated.test", spec.name))
			if c := nrgbaAt(img, 0, 0); c != black {
				t.Errorf("source=icon: corner of %s = %v, want the opaque primary %v",
					spec.name, c, black)
			}
			// Opaque EVERYWHERE, not just in the corner: a hole is what a
			// masking launcher and iOS both render badly.
			for y := 0; y < spec.size; y += 7 {
				for x := 0; x < spec.size; x += 7 {
					if a := nrgbaAt(img, x, y).A; a != 255 {
						t.Fatalf("source=icon: pixel (%d,%d) of %s has alpha %d",
							x, y, spec.name, a)
					}
				}
			}
			// And the mark is still there, in the middle, legible against it.
			if c := nrgbaAt(img, spec.size/2, spec.size/2); c != white {
				t.Errorf("source=icon: center of %s = %v, want the white glyph", spec.name, c)
			}

			// Source = logo: today's behavior, untouched. Transparent where
			// the spec says transparent, plated where it says plated.
			logoImg := decodePNG(t, getIcon(t, logoSrv, "plated.test", spec.name))
			corner := nrgbaAt(logoImg, 0, 0)
			if spec.opaque {
				if corner != black {
					t.Errorf("source=logo: corner of %s = %v, want the plate", spec.name, corner)
				}
			} else if corner.A != 0 {
				t.Errorf("source=logo: corner of %s = %v, want it still transparent", spec.name, corner)
			}
		})
	}

	// The two sizes the pilot caught are worth naming, so a future change that
	// quietly un-plates them fails with the symptom rather than with a corner.
	for _, name := range []string{"icon-192", "favicon-32"} {
		img := decodePNG(t, getIcon(t, iconSrv, "plated.test", name))
		if nrgbaAt(img, 0, 0).A != 255 {
			t.Errorf("%s went back to a transparent canvas: a white glyph is invisible on it", name)
		}
	}

	// favicon-32 needs the padding floor as well as the plate: its own spec has
	// none, so a SQUARE mark would cover the plate edge to edge and a white
	// glyph would be a white square on a light tab all over again.
	fav := decodePNG(t, getIcon(t, iconSrv, "plated.test", "favicon-32"))
	if c := nrgbaAt(fav, 0, 0); c != black {
		t.Errorf("favicon-32 corner = %v, want the plate framing the glyph", c)
	}
	if c := nrgbaAt(fav, 16, 16); c != white {
		t.Errorf("favicon-32 center = %v, want the glyph", c)
	}
	// And the logo's favicon-32 keeps its every-pixel-counts zero padding.
	favLogo := decodePNG(t, getIcon(t, logoSrv, "plated.test", "favicon-32"))
	if a := nrgbaAt(favLogo, 0, 0).A; a != 0 {
		t.Errorf("source=logo: favicon-32 corner alpha = %d, want it transparent as before", a)
	}
}

// TestSpecForSource pins the rule itself, away from any rendering.
func TestSpecForSource(t *testing.T) {
	for _, spec := range brandingIconSpecs {
		got := specForSource(spec, brandingSourceIcon)
		if !got.opaque {
			t.Errorf("%s: a dedicated icon must be plated", spec.name)
		}
		// A plated icon never has less padding than the floor, and never MORE
		// than its own spec asked for: the maskable 20% survives.
		if got.pad < minPlatedIconPad {
			t.Errorf("%s: plated padding = %v, want at least %v", spec.name, got.pad, minPlatedIconPad)
		}
		if spec.pad > minPlatedIconPad && got.pad != spec.pad {
			t.Errorf("%s: padding moved from %v to %v", spec.name, spec.pad, got.pad)
		}
		// Nothing else about the spec moves.
		if got.name != spec.name || got.size != spec.size {
			t.Errorf("%s: specForSource changed more than the plate: %+v", spec.name, got)
		}

		// The logo's spec is returned untouched, in every field.
		if got := specForSource(spec, brandingSourceLogo); got != spec {
			t.Errorf("%s: the logo's spec changed: %+v", spec.name, got)
		}
		// So is an absent source (Moov's own icons never reach here anyway).
		if got := specForSource(spec, ""); got != spec {
			t.Errorf("%s: an empty source changed the spec: %+v", spec.name, got)
		}
	}
}
