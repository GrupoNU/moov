package jmaphttp

import (
	"image"
	"math"
)

// A small, dependency-free raster resampler for the branded PWA icons.
//
// # Why hand-written
//
// golang.org/x/image/draw has exactly this, and is not vendored. The vendor
// tree is hermetic (CLAUDE.md: go-imap is pinned and patched in place; nothing
// enters vendor/ for convenience), so the choice was between adding a
// dependency for two loops and writing the two loops. The loops are below,
// separable (one axis at a time), and pinned by unit tests that state the
// arithmetic outright: a 2x2 checkerboard shrunk to 1x1 is the mean, and a
// scale of 1 is byte-identical.
//
// # Filters
//
// Shrinking uses an area average (a "box" filter): every source pixel
// contributes to the destination pixel in proportion to how much of it the
// destination covers. It is the correct filter for shrinking a logo — no
// aliasing, no dropped thin strokes — and it is exact for integer ratios.
//
// Growing uses bilinear interpolation between the two nearest source pixels
// with pixel-center alignment. A logo that arrives smaller than 512 px is
// already lossy; bilinear keeps its edges soft rather than blocky.
//
// # Premultiplied alpha
//
// Everything here operates on *image.RGBA, which Go defines as
// alpha-PREMULTIPLIED. That is not incidental: averaging straight (non-
// premultiplied) color next to a transparent pixel drags the transparent
// pixel's meaningless color — usually black — into the edge, and every
// anti-aliased logo edge acquires a dark fringe. In premultiplied space a
// transparent pixel is (0,0,0,0) and contributes nothing, which is what
// "transparent" means. The PNG encoder un-premultiplies on the way out.

// resampleRGBA returns src scaled to w by h.
//
// w and h must be positive; the result is a fresh image whose bounds start at
// the origin. A scale of exactly 1 on both axes returns a pixel-for-pixel copy.
func resampleRGBA(src *image.RGBA, w, h int) *image.RGBA {
	if w <= 0 || h <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 0, 0))
	}
	// Two separable passes: width first, then height. The intermediate image
	// is w wide and as tall as the source.
	horizontal := resampleAxis(src, w, src.Bounds().Dy(), true)
	return resampleAxis(horizontal, w, h, false)
}

// tap describes which source samples feed one destination sample along an
// axis: the index of the first source sample and the weight of each
// consecutive one. Weights sum to 1.
type tap struct {
	start   int
	weights []float64
}

// axisTaps computes the resampling taps for one axis.
func axisTaps(srcLen, dstLen int) []tap {
	taps := make([]tap, dstLen)
	switch {
	case dstLen == srcLen:
		// Identity, stated explicitly so a scale of 1 cannot drift by a
		// rounding error: one tap, weight exactly 1.
		for i := range taps {
			taps[i] = tap{start: i, weights: []float64{1}}
		}
	case dstLen < srcLen:
		// Area average. Destination sample i covers the source interval
		// [i*scale, (i+1)*scale); each source sample is weighted by the length
		// of its overlap with that interval.
		scale := float64(srcLen) / float64(dstLen)
		for i := range taps {
			lo := float64(i) * scale
			hi := float64(i+1) * scale
			first := int(math.Floor(lo))
			last := int(math.Ceil(hi)) - 1
			if last >= srcLen {
				last = srcLen - 1
			}
			weights := make([]float64, 0, last-first+1)
			sum := 0.0
			for j := first; j <= last; j++ {
				overlap := math.Min(hi, float64(j+1)) - math.Max(lo, float64(j))
				if overlap < 0 {
					overlap = 0
				}
				weights = append(weights, overlap)
				sum += overlap
			}
			for k := range weights {
				weights[k] /= sum
			}
			taps[i] = tap{start: first, weights: weights}
		}
	default:
		// Bilinear, pixel-center aligned: destination center (i+0.5) maps to
		// source coordinate (i+0.5)*scale, and the sample sits between the two
		// source pixels whose centers bracket it. Edges clamp.
		scale := float64(srcLen) / float64(dstLen)
		for i := range taps {
			pos := (float64(i)+0.5)*scale - 0.5
			if pos < 0 {
				pos = 0
			}
			j0 := int(math.Floor(pos))
			if j0 > srcLen-1 {
				j0 = srcLen - 1
			}
			t := pos - float64(j0)
			if j0 == srcLen-1 {
				taps[i] = tap{start: j0, weights: []float64{1}}
				continue
			}
			taps[i] = tap{start: j0, weights: []float64{1 - t, t}}
		}
	}
	return taps
}

// resampleAxis resamples src along one axis. When horizontal is true the
// result is dstW wide and keeps the source height; otherwise it is dstH tall
// and keeps the source width (dstW must then equal the source width).
func resampleAxis(src *image.RGBA, dstW, dstH int, horizontal bool) *image.RGBA {
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	if srcW == 0 || srcH == 0 {
		return dst
	}

	var taps []tap
	if horizontal {
		taps = axisTaps(srcW, dstW)
	} else {
		taps = axisTaps(srcH, dstH)
	}

	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			var acc [4]float64
			var tp tap
			if horizontal {
				tp = taps[x]
			} else {
				tp = taps[y]
			}
			for k, wgt := range tp.weights {
				var sx, sy int
				if horizontal {
					sx, sy = tp.start+k, y
				} else {
					sx, sy = x, tp.start+k
				}
				off := src.PixOffset(b.Min.X+sx, b.Min.Y+sy)
				acc[0] += wgt * float64(src.Pix[off+0])
				acc[1] += wgt * float64(src.Pix[off+1])
				acc[2] += wgt * float64(src.Pix[off+2])
				acc[3] += wgt * float64(src.Pix[off+3])
			}
			d := dst.PixOffset(x, y)
			dst.Pix[d+0] = clampByte(acc[0])
			dst.Pix[d+1] = clampByte(acc[1])
			dst.Pix[d+2] = clampByte(acc[2])
			dst.Pix[d+3] = clampByte(acc[3])
		}
	}
	return dst
}

// clampByte rounds to the nearest integer and clamps to [0, 255].
func clampByte(v float64) uint8 {
	r := math.Round(v)
	if r < 0 {
		return 0
	}
	if r > 255 {
		return 255
	}
	return uint8(r)
}
