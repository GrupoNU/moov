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
	if w, h, ok := ImageDimensions(body); ok && !IsRoughlySquare(w, h) {
		return []string{fmt.Sprintf("the icon is %dx%d, which is not square; "+
			"launchers show a square, so it will be contained inside one "+
			"with bands of the primary color around it", w, h)}
	}
	return nil
}
