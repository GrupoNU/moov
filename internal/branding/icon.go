package branding

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"math"

	// The decoders an icon source may arrive in. Registering them is what
	// lets image.DecodeConfig recognize PNG, JPEG and GIF. WebP is
	// deliberately absent: its decoder lives in golang.org/x/image, which is
	// not vendored, so a WebP source is declared unusable rather than decoded.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// MaxIconDimension caps the width and height of an image the PWA icons are
// rendered from. It is checked from the header before any pixels are decoded,
// so a PNG that inflates to gigabytes is refused for the cost of a few bytes —
// the decompression-bomb defense. 4096 is far more than any icon needs.
const MaxIconDimension = 4096

// ErrIconWebP is the sentence for the one format the server accepts as an
// asset yet cannot render into icons. A sentinel because it is the format an
// operator is most likely to bring.
var ErrIconWebP = errors.New("the logo is WebP, which cannot be decoded for PWA icons; provide it as PNG, JPEG or GIF")

// ValidateIconSource reports whether an image's bytes can be rendered into
// PWA icons, and if not, why — in a sentence an operator can act on. The CLI
// uses it to warn at write time, the admin API to fill `warnings`, and the
// server applies the same function when it resolves a host, so no two
// verdicts can disagree.
//
// It reads only the image header (image.DecodeConfig), never the pixels, so
// it is safe to call on anything that passed the size cap.
func ValidateIconSource(data []byte) error {
	contentType, ok := SniffImageType(data)
	if !ok {
		return errors.New("the logo is not a PNG, JPEG, GIF or WebP image")
	}
	if contentType == "image/webp" {
		return ErrIconWebP
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("the logo cannot be decoded (%w)", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return fmt.Errorf("the logo has no pixels (%dx%d)", cfg.Width, cfg.Height)
	}
	if cfg.Width > MaxIconDimension || cfg.Height > MaxIconDimension {
		return fmt.Errorf("the logo is %dx%d; the limit is %d px on each side",
			cfg.Width, cfg.Height, MaxIconDimension)
	}
	return nil
}

// ImageDimensions reads an image's pixel size from its header, never the
// pixels. false means the format has no decoder registered here (WebP) or
// the bytes are not an image.
func ImageDimensions(b []byte) (width, height int, ok bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// MaxIconAspectDrift is how far from 1:1 an icon may be before its author is
// warned. Ten per cent is enough to cover the odd off-by-a-pixel export and
// tight enough to catch a wordmark handed to the icon slot by mistake.
const MaxIconAspectDrift = 0.10

// IsRoughlySquare reports whether an image is close enough to 1:1 to fill a
// launcher's square without visible bands.
func IsRoughlySquare(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	ratio := float64(width) / float64(height)
	return math.Abs(ratio-1) <= MaxIconAspectDrift
}

// IconSourceNotes are the sentences an author reads after storing an asset
// that feeds the PWA icons: that they will NOT be rendered from it (and why),
// or that the icon is not square. One wording, printed by the CLI and carried
// in the admin API's `warnings`, so the two never say different things about
// the same file. Empty for kinds that do not feed the icons and for a source
// with nothing to say.
func IconSourceNotes(kind AssetKind, body []byte) []string {
	if !kind.FeedsIcons() {
		return nil
	}
	if err := ValidateIconSource(body); err != nil {
		return []string{fmt.Sprintf("the PWA icons will not be rendered from this %s: %v", kind, err)}
	}
	if kind != AssetIcon {
		return nil
	}
	// A launcher shows a SQUARE. A wide image is contained inside it with its
	// aspect kept, so it ends up small with bands of plate above and below —
	// legible, but not what an author supplying an "icon" expects to see.
	var notes []string
	if w, h, ok := ImageDimensions(body); ok && !IsRoughlySquare(w, h) {
		notes = append(notes, fmt.Sprintf("the icon is %dx%d, which is not square; "+
			"launchers show a square, so it will be contained inside one "+
			"with bands of the plate color around it", w, h))
	}
	// The favicon and the "any" launcher icons are drawn on a TRANSPARENT
	// canvas, so a light mark has nothing behind it on a light tab. Nothing
	// here can fix that honestly (plating the favicon frames every brand's tab;
	// recoloring wrecks any mark that is not a flat silhouette), so it is
	// declared and the operator decides.
	if IconOnTransparentMayVanish(body) {
		notes = append(notes, LightIconOnTransparentNote)
	}
	return notes
}

// IconOnTransparentMayVanish reports whether an icon source's visible pixels
// are mostly LIGHT — the case where the transparent-canvas icons (the favicon
// and the "any" launcher sizes) can disappear against a light background.
//
// Decoding failures answer false: a source that cannot be decoded is already
// declared by ValidateIconSource, and a second complaint about the same file
// would be noise.
func IconOnTransparentMayVanish(body []byte) bool {
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return false
	}
	return !IconIsDark(img)
}

// --- how dark is the mark? ----------------------------------------------------

// DarkMarkLuminanceMax is the mean relative luminance below which an icon
// counts as a DARK mark, and therefore needs a light plate behind it wherever
// a plate is painted at all.
//
// 0.5 is the midpoint of the WCAG relative-luminance scale rather than a tuned
// constant, and that is deliberate: the decision it drives is binary (white
// plate or the brand's primary), the inputs are real logos rather than a
// distribution anyone has measured, and a threshold nobody can justify is a
// threshold the next person will move at random.
const DarkMarkLuminanceMax = 0.5

// MeanIconLuminance reports the alpha-weighted mean WCAG relative luminance of
// an image's OPAQUE pixels, and whether there were any.
//
// # Why alpha-weighted, and why opaque pixels only
//
// A logo is mostly transparent. Averaging every pixel would measure the empty
// canvas — a black glyph on a transparent square would come back as whatever
// the encoder wrote into the invisible pixels, which for most PNGs is either
// black (making every icon "dark") or white (making every icon "light"). Both
// answers are about the file's padding rather than about the mark.
//
// So each pixel contributes in proportion to how visible it is: a fully opaque
// pixel counts once, a half-transparent edge counts half, a fully transparent
// one not at all. That is also what makes the answer stable under
// anti-aliasing, which is most of the pixels in a small glyph.
//
// Returns (0, false) when the image has no visible pixels at all, so a caller
// cannot mistake "entirely transparent" for "black".
func MeanIconLuminance(img image.Image) (float64, bool) {
	bounds := img.Bounds()
	var sum, weight float64
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			// RGBA() returns alpha-PREMULTIPLIED values in 0..65535. Dividing
			// by alpha recovers the pixel's own color, which is what has to be
			// measured: the premultiplied value of a half-transparent white is
			// a mid grey, and mid grey is not what the eye will see once the
			// launcher composites it.
			r16, g16, b16, a16 := img.At(x, y).RGBA()
			if a16 == 0 {
				continue
			}
			a := float64(a16) / 65535
			r := float64(r16) / float64(a16)
			g := float64(g16) / float64(a16)
			b := float64(b16) / float64(a16)
			sum += relativeLuminance(r, g, b) * a
			weight += a
		}
	}
	if weight == 0 {
		return 0, false
	}
	return sum / weight, true
}

// relativeLuminance is WCAG 2.x relative luminance for channels already
// normalized to 0..1. Written out rather than pulled from a color library so
// the number this package decides on is the number the contrast rules in the
// PWA are defined against.
func relativeLuminance(r, g, b float64) float64 {
	lin := func(c float64) float64 {
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// IconIsDark reports whether an image's visible pixels are dark enough to need
// a light plate. An image with nothing visible is treated as dark, which is the
// safe default: a white plate shows an empty icon as empty rather than hiding
// the fact behind the brand's own color.
func IconIsDark(img image.Image) bool {
	mean, ok := MeanIconLuminance(img)
	if !ok {
		return true
	}
	return mean < DarkMarkLuminanceMax
}

// LightIconOnTransparentNote is the warning an operator gets when their icon's
// visible pixels are mostly LIGHT and the canvas it is drawn on is transparent
// — the favicon and the "any" launcher icons.
//
// It is a warning rather than a fix because there is no honest fix available
// here: plating the favicon is what this change just removed (it put a frame
// around every brand's tab icon to rescue one), and recoloring somebody's mark
// wrecks any logo that is not a flat silhouette. What the operator can do,
// and we cannot, is supply a version with a dark outline.
const LightIconOnTransparentNote = "the icon's visible pixels are mostly light, and the " +
	"favicon and launcher icons are drawn on a transparent canvas: a light icon may vanish " +
	"on light tabs; consider a version with a dark outline"
