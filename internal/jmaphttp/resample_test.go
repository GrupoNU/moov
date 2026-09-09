package jmaphttp

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

// The resampler's arithmetic, stated as tests. Each case is small enough to
// be checked by hand, which is the point: a filter that is "probably right"
// produces icons that are subtly wrong on every customer's home screen.

// rgbaFrom builds an image at the origin from rows of premultiplied pixels.
func rgbaFrom(t *testing.T, rows [][]color.RGBA) *image.RGBA {
	t.Helper()
	h := len(rows)
	w := len(rows[0])
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y, row := range rows {
		if len(row) != w {
			t.Fatalf("row %d has %d pixels, want %d", y, len(row), w)
		}
		for x, c := range row {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

var (
	black       = color.RGBA{0, 0, 0, 255}
	white       = color.RGBA{255, 255, 255, 255}
	transparent = color.RGBA{0, 0, 0, 0}
)

// TestResampleCheckerToOnePixelIsTheMean is the box filter's defining
// property: an integer shrink averages exactly the pixels it covers.
func TestResampleCheckerToOnePixelIsTheMean(t *testing.T) {
	src := rgbaFrom(t, [][]color.RGBA{
		{white, black},
		{black, white},
	})
	got := resampleRGBA(src, 1, 1).RGBAAt(0, 0)
	// (255+0+0+255)/4 = 127.5, rounded half away from zero.
	want := color.RGBA{128, 128, 128, 255}
	if got != want {
		t.Errorf("mean of a checkerboard = %v, want %v", got, want)
	}
}

// TestResampleIdentityIsByteIdentical: a scale of 1 must not touch a byte.
// This is what makes a logo that is already 512x512 round-trip losslessly.
func TestResampleIdentityIsByteIdentical(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 7, 5))
	for i := range src.Pix {
		src.Pix[i] = uint8((i*37 + 11) % 256)
	}
	// Premultiplied invariant: color channels must not exceed alpha. Force it
	// so the fixture is a legal RGBA image (the resampler does not care, but
	// the test should not lie about what it feeds in).
	for i := 0; i < len(src.Pix); i += 4 {
		a := src.Pix[i+3]
		for c := 0; c < 3; c++ {
			if src.Pix[i+c] > a {
				src.Pix[i+c] = a
			}
		}
	}
	got := resampleRGBA(src, 7, 5)
	if !bytes.Equal(got.Pix, src.Pix) {
		t.Error("identity resample changed pixel bytes")
	}
	if got == src {
		t.Error("identity resample returned the same image rather than a copy")
	}
}

// TestResampleNonIntegerShrinkWeightsByCoverage: 3 -> 2 splits the middle
// pixel in half between the two outputs.
func TestResampleNonIntegerShrinkWeightsByCoverage(t *testing.T) {
	src := rgbaFrom(t, [][]color.RGBA{{
		{0, 0, 0, 255}, {90, 90, 90, 255}, {180, 180, 180, 255},
	}})
	got := resampleRGBA(src, 2, 1)
	// dst0 covers [0,1.5): (0*1 + 90*0.5)/1.5 = 30
	// dst1 covers [1.5,3): (90*0.5 + 180*1)/1.5 = 150
	if p := got.RGBAAt(0, 0); p.R != 30 {
		t.Errorf("dst0 = %d, want 30", p.R)
	}
	if p := got.RGBAAt(1, 0); p.R != 150 {
		t.Errorf("dst1 = %d, want 150", p.R)
	}
}

// TestResampleUpscaleIsBilinear: growing interpolates monotonically between
// the source samples and keeps both ends.
func TestResampleUpscaleIsBilinear(t *testing.T) {
	src := rgbaFrom(t, [][]color.RGBA{{black, white}})
	got := resampleRGBA(src, 4, 1)
	var vals []uint8
	for x := 0; x < 4; x++ {
		vals = append(vals, got.RGBAAt(x, 0).R)
	}
	// Centers at 0.25, 0.75, 1.25, 1.75 in source space map to
	// -0.25 (clamped to 0), 0.25, 0.75, 1.25 (clamped to the last sample).
	want := []uint8{0, 64, 191, 255}
	for i := range want {
		if vals[i] != want[i] {
			t.Errorf("upscaled = %v, want %v", vals, want)
			break
		}
	}

	// A solid color grown in both axes stays that color everywhere.
	solid := rgbaFrom(t, [][]color.RGBA{{{10, 20, 30, 255}}})
	big := resampleRGBA(solid, 5, 3)
	for y := 0; y < 3; y++ {
		for x := 0; x < 5; x++ {
			if p := big.RGBAAt(x, y); p != (color.RGBA{10, 20, 30, 255}) {
				t.Fatalf("pixel (%d,%d) = %v, want the solid color", x, y, p)
			}
		}
	}
}

// TestResampleIsPremultiplied pins the reason the resampler works in RGBA
// space: a transparent pixel contributes no color, so an opaque red pixel
// averaged with three transparent ones is still red, just fainter — not a
// dark red.
func TestResampleIsPremultiplied(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	src := rgbaFrom(t, [][]color.RGBA{
		{red, transparent},
		{transparent, transparent},
	})
	got := resampleRGBA(src, 1, 1).RGBAAt(0, 0)
	if got.A != 64 {
		t.Fatalf("alpha = %d, want 64 (a quarter coverage)", got.A)
	}
	straight, ok := color.NRGBAModel.Convert(got).(color.NRGBA)
	if !ok {
		t.Fatal("NRGBAModel.Convert did not return an NRGBA")
	}
	if straight.R < 250 || straight.G != 0 || straight.B != 0 {
		t.Errorf("un-premultiplied color = %v, want pure red (no dark fringe)", straight)
	}
}

// TestResampleHandlesOffsetBounds: an image whose bounds do not start at the
// origin (a sub-image) is read through its own offsets, not assumed at zero.
func TestResampleHandlesOffsetBounds(t *testing.T) {
	full := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			full.SetRGBA(x, y, black)
		}
	}
	full.SetRGBA(2, 2, white)
	full.SetRGBA(3, 2, white)
	full.SetRGBA(2, 3, white)
	full.SetRGBA(3, 3, white)
	sub, ok := full.SubImage(image.Rect(2, 2, 4, 4)).(*image.RGBA)
	if !ok {
		t.Fatal("SubImage of an *image.RGBA is not an *image.RGBA")
	}

	got := resampleRGBA(sub, 1, 1).RGBAAt(0, 0)
	if got != white {
		t.Errorf("sub-image resample = %v, want white (read at the wrong offset)", got)
	}
}

// TestResampleDegenerateSizes: zero or negative targets yield an empty image
// rather than a panic — the caller clamps, but the primitive must not trust
// it.
func TestResampleDegenerateSizes(t *testing.T) {
	src := rgbaFrom(t, [][]color.RGBA{{white}})
	for _, dims := range [][2]int{{0, 1}, {1, 0}, {-3, 2}} {
		got := resampleRGBA(src, dims[0], dims[1])
		if got.Bounds().Dx() != 0 || got.Bounds().Dy() != 0 {
			t.Errorf("resample to %v = %v, want empty", dims, got.Bounds())
		}
	}
}
