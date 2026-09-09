package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for `moovctl branding set -icon`.
//
// The icon is the SQUARE mark the installed app's icons and the favicon are
// rendered from; the logo stays what the top bar and the login panel show. The
// two are separate because the maskable and Apple icons sit on an opaque plate
// of the primary color, so a customer whose primary is black and whose logo is
// a black wordmark would otherwise ship an invisible home-screen icon.

// squarePNG and widePNG are real images, because the CLI sniffs bytes and
// measures pixels rather than trusting a filename.
func squarePNG(t *testing.T) []byte { return solidPNG(t, 64, 64) }
func widePNG(t *testing.T) []byte   { return solidPNG(t, 200, 40) }

func solidPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// TestBrandingSetIconRoundTrip: set stores the icon under OUR name, show
// prints it and names it as the icon source, and unset removes it with the
// rest.
func TestBrandingSetIconRoundTrip(t *testing.T) {
	root := t.TempDir()
	logo := writeTempImage(t, "wordmark.png", widePNG(t))
	icon := writeTempImage(t, "glyph-for-dark.png", squarePNG(t))

	code, stdout, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "icon.test", "-name", "Areacorp",
		"-logo", logo, "-icon", icon, "-color-primary", "#000000")
	if code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "icon.png") {
		t.Errorf("set did not report where the icon was stored:\n%s", stdout)
	}
	// A square icon is not warned about, and a renderable one is not declared.
	if strings.Contains(stdout, "not square") {
		t.Errorf("a 64x64 icon was called not square:\n%s", stdout)
	}
	if strings.Contains(stdout, "will not be rendered") {
		t.Errorf("a renderable PNG icon was declared unusable:\n%s", stdout)
	}

	doc := readDoc(t, root, "icon.test")
	if doc.Icon != "icon.png" {
		t.Errorf("icon = %q, want icon.png (the stored name is ours, not the source's)", doc.Icon)
	}
	if doc.Logo != "logo.png" {
		t.Errorf("logo = %q, want it stored beside the icon", doc.Logo)
	}
	for _, name := range []string{"icon.png", "logo.png"} {
		if _, err := os.Stat(filepath.Join(root, "icon.test", name)); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
	}

	code, stdout, stderr = runCLI(t, "", "branding", "show", "-dir", root, "-host", "icon.test")
	if code != exitOK {
		t.Fatalf("show exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "ICON") {
		t.Errorf("show has no ICON row:\n%s", stdout)
	}
	if !strings.Contains(stdout, "generated from the icon icon.png") {
		t.Errorf("show does not name the icon as the PWA source:\n%s", stdout)
	}

	// list carries it too, so an operator auditing many hosts sees which have
	// a dedicated icon.
	_, stdout, _ = runCLI(t, "", "branding", "list", "-dir", root)
	if !strings.Contains(stdout, "ICON") || !strings.Contains(stdout, "icon.png") {
		t.Errorf("list does not carry the icon:\n%s", stdout)
	}

	// Clearing stops advertising it; the file stays, like every other asset.
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "icon.test", "-icon", ""); code != exitOK {
		t.Fatalf("clearing exited %d: %s", code, stderr)
	}
	if got := readDoc(t, root, "icon.test").Icon; got != "" {
		t.Errorf("icon = %q, want it cleared", got)
	}
	if _, err := os.Stat(filepath.Join(root, "icon.test", "icon.png")); err != nil {
		t.Errorf("clearing -icon deleted the operator's file: %v", err)
	}
	// And with no icon the chain reports the logo.
	_, stdout, _ = runCLI(t, "", "branding", "show", "-dir", root, "-host", "icon.test")
	if !strings.Contains(stdout, "generated from the logo logo.png") {
		t.Errorf("show does not fall back to the logo:\n%s", stdout)
	}

	// unset removes the icon file along with the others.
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "icon.test", "-icon", writeTempImage(t, "g.png", squarePNG(t))); code != exitOK {
		t.Fatalf("re-setting exited %d: %s", code, stderr)
	}
	if code, _, stderr := runCLI(t, "", "branding", "unset", "-dir", root, "-host", "icon.test"); code != exitOK {
		t.Fatalf("unset exited %d: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "icon.test", "icon.png")); !os.IsNotExist(err) {
		t.Errorf("unset left icon.png behind: %v", err)
	}
}

// TestBrandingSetIconWarnsWhenNotSquare: a launcher shows a square, so a
// wordmark handed to -icon by mistake is worth a word at the terminal.
func TestBrandingSetIconWarnsWhenNotSquare(t *testing.T) {
	root := t.TempDir()

	_, stdout, _ := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "wide.test", "-icon", writeTempImage(t, "wide.png", widePNG(t)))
	if !strings.Contains(stdout, "not square") || !strings.Contains(stdout, "200x40") {
		t.Errorf("set did not warn about a 200x40 icon:\n%s", stdout)
	}
	// It is a warning, not a refusal: the operator may know exactly what they
	// are doing, and the icon is stored either way.
	if got := readDoc(t, root, "wide.test").Icon; got != "icon.png" {
		t.Errorf("icon = %q; the warning must not block the write", got)
	}

	// Within 10% of 1:1 is square enough for a launcher.
	_, stdout, _ = runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "near.test", "-icon", writeTempImage(t, "near.png", solidPNG(t, 105, 100)))
	if strings.Contains(stdout, "not square") {
		t.Errorf("a 105x100 icon was called not square:\n%s", stdout)
	}

	// The logo is NOT held to the square rule: a wide wordmark is exactly what
	// it should be.
	_, stdout, _ = runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "logo.test", "-logo", writeTempImage(t, "wm.png", widePNG(t)))
	if strings.Contains(stdout, "not square") {
		t.Errorf("a wide logo was warned about:\n%s", stdout)
	}
}

// TestBrandingSetIconDeclaresUnrenderable: a WebP icon is stored (it is a
// valid image) but cannot become PWA icons, and both the moment of the write
// and `show` say which file failed and what took over.
func TestBrandingSetIconDeclaresUnrenderable(t *testing.T) {
	root := t.TempDir()
	webp := writeTempImage(t, "glyph.webp",
		append(append([]byte("RIFF"), 0x10, 0x00, 0x00, 0x00), []byte("WEBPVP8 ")...))

	// With a usable logo behind it: the chain lands on the logo.
	code, stdout, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "webpicon.test",
		"-logo", writeTempImage(t, "wm.png", widePNG(t)), "-icon", webp)
	if code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "will not be rendered from this icon") || !strings.Contains(stdout, "WebP") {
		t.Errorf("set did not declare the unrenderable icon:\n%s", stdout)
	}

	_, stdout, _ = runCLI(t, "", "branding", "show", "-dir", root, "-host", "webpicon.test")
	if !strings.Contains(stdout, "generated from the logo logo.png") ||
		!strings.Contains(stdout, "the icon is not usable") || !strings.Contains(stdout, "WebP") {
		t.Errorf("show does not explain the fall-through to the logo:\n%s", stdout)
	}

	// With nothing behind it: Moov's, and said so.
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "alone.test", "-icon", webp); code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	_, stdout, _ = runCLI(t, "", "branding", "show", "-dir", root, "-host", "alone.test")
	if !strings.Contains(stdout, "Moov's") || !strings.Contains(stdout, "no logo is configured") {
		t.Errorf("show does not report Moov's icons:\n%s", stdout)
	}

	// And a host with neither says so plainly.
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "bare2.test", "-name", "Bare"); code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	_, stdout, _ = runCLI(t, "", "branding", "show", "-dir", root, "-host", "bare2.test")
	if !strings.Contains(stdout, "no icon and no logo configured") {
		t.Errorf("show does not report an unmarked brand:\n%s", stdout)
	}
}

// TestBrandingSetIconIsIncremental: touching the icon leaves the logo alone
// and vice versa — the property that stops a color tweak from deleting a
// customer's mark.
func TestBrandingSetIconIsIncremental(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "inc.test", "-name", "Inc",
		"-logo", writeTempImage(t, "wm.png", widePNG(t)),
		"-icon", writeTempImage(t, "g.png", squarePNG(t))); code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "inc.test", "-color-primary", "#123456"); code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	doc := readDoc(t, root, "inc.test")
	if doc.Icon != "icon.png" || doc.Logo != "logo.png" {
		t.Errorf("a color change disturbed the assets: icon=%q logo=%q", doc.Icon, doc.Logo)
	}
	if doc.Colors.Primary != "#123456" {
		t.Errorf("primary = %q", doc.Colors.Primary)
	}
}

// TestIsRoughlySquare pins the threshold the warning uses.
func TestIsRoughlySquare(t *testing.T) {
	cases := []struct {
		w, h int
		want bool
	}{
		{512, 512, true},
		{105, 100, true},
		{100, 105, true},
		{120, 100, false},
		{100, 120, false},
		{200, 40, false},
		{0, 10, false},
		{10, 0, false},
	}
	for _, c := range cases {
		if got := isRoughlySquare(c.w, c.h); got != c.want {
			t.Errorf("isRoughlySquare(%d, %d) = %v, want %v", c.w, c.h, got, c.want)
		}
	}
}

// TestImageDimensionsForCLI: the header is read, the pixels are not, and a
// format with no decoder here answers false rather than guessing.
func TestImageDimensionsForCLI(t *testing.T) {
	if w, h, ok := imageDimensionsForCLI(widePNG(t)); !ok || w != 200 || h != 40 {
		t.Errorf("PNG = %dx%d ok=%v, want 200x40 true", w, h, ok)
	}
	webp := append(append([]byte("RIFF"), 0x10, 0x00, 0x00, 0x00), []byte("WEBPVP8 ")...)
	if _, _, ok := imageDimensionsForCLI(webp); ok {
		t.Error("WebP reported dimensions; its decoder is not vendored")
	}
	if _, _, ok := imageDimensionsForCLI([]byte("not an image")); ok {
		t.Error("junk reported dimensions")
	}
}

// TestBrandingSetIconHelp: the flag and the reason it exists are in the usage,
// because "why do I need two images" is the first question it raises.
func TestBrandingSetIconHelp(t *testing.T) {
	_, _, stderr := runCLI(t, "", "branding", "set", "-h")
	for _, want := range []string{"-icon", "SQUARE", "plate"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("branding set usage does not mention %q:\n%s", want, stderr)
		}
	}
}

// --- the dark-background wordmark --------------------------------------------

// TestBrandingSetLogoDarkRoundTrip: stored under OUR name, shown in show and
// list, cleared by an empty value, removed by unset — and never confused with
// the light logo or with the icon.
func TestBrandingSetLogoDarkRoundTrip(t *testing.T) {
	root := t.TempDir()

	code, stdout, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "dark.test", "-name", "Areacorp",
		"-logo", writeTempImage(t, "wordmark-black.png", widePNG(t)),
		"-logo-dark", writeTempImage(t, "wordmark-white.png", widePNG(t)))
	if code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "logo-dark.png") {
		t.Errorf("set did not report where the dark logo was stored:\n%s", stdout)
	}
	// A dark wordmark is not an icon source, so no squareness warning and no
	// icon declaration is owed for it.
	if strings.Contains(stdout, "not square") {
		t.Errorf("a wide dark logo was warned about as if it were an icon:\n%s", stdout)
	}

	doc := readDoc(t, root, "dark.test")
	if doc.LogoDark != "logo-dark.png" {
		t.Errorf("logoDark = %q, want logo-dark.png", doc.LogoDark)
	}
	if doc.Logo != "logo.png" {
		t.Errorf("logo = %q; the light logo must be stored separately", doc.Logo)
	}
	for _, name := range []string{"logo.png", "logo-dark.png"} {
		if _, err := os.Stat(filepath.Join(root, "dark.test", name)); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
	}

	_, stdout, _ = runCLI(t, "", "branding", "show", "-dir", root, "-host", "dark.test")
	if !strings.Contains(stdout, "LOGO DARK") || !strings.Contains(stdout, "logo-dark.png") {
		t.Errorf("show does not carry the dark logo:\n%s", stdout)
	}
	// The PWA icons still come from the light logo: the dark one is not in that
	// chain at all.
	if !strings.Contains(stdout, "generated from the logo logo.png") {
		t.Errorf("the dark logo disturbed the icon chain:\n%s", stdout)
	}

	_, stdout, _ = runCLI(t, "", "branding", "list", "-dir", root)
	if !strings.Contains(stdout, "LOGO DARK") || !strings.Contains(stdout, "logo-dark.png") {
		t.Errorf("list does not carry the dark logo:\n%s", stdout)
	}

	// Clearing stops advertising it and leaves the file, like every other asset.
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "dark.test", "-logo-dark", ""); code != exitOK {
		t.Fatalf("clearing exited %d: %s", code, stderr)
	}
	after := readDoc(t, root, "dark.test")
	if after.LogoDark != "" {
		t.Errorf("logoDark = %q, want it cleared", after.LogoDark)
	}
	if after.Logo != "logo.png" || after.Name != "Areacorp" {
		t.Errorf("clearing the dark logo disturbed the rest: %+v", after)
	}
	if _, err := os.Stat(filepath.Join(root, "dark.test", "logo-dark.png")); err != nil {
		t.Errorf("clearing -logo-dark deleted the operator's file: %v", err)
	}

	// unset removes it with the others.
	if code, _, stderr := runCLI(t, "", "branding", "set", "-dir", root, "-host", "dark.test",
		"-logo-dark", writeTempImage(t, "w.png", widePNG(t))); code != exitOK {
		t.Fatalf("re-setting exited %d: %s", code, stderr)
	}
	if code, _, stderr := runCLI(t, "", "branding", "unset", "-dir", root, "-host", "dark.test"); code != exitOK {
		t.Fatalf("unset exited %d: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "dark.test", "logo-dark.png")); !os.IsNotExist(err) {
		t.Errorf("unset left logo-dark.png behind: %v", err)
	}
}

// TestBrandingSetLogoDarkIsIndependent: the three marks do not overwrite one
// another, and replacing one across extensions does not disturb the others.
func TestBrandingSetLogoDarkIsIndependent(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "three.test",
		"-logo", writeTempImage(t, "l.png", widePNG(t)),
		"-logo-dark", writeTempImage(t, "d.png", widePNG(t)),
		"-icon", writeTempImage(t, "i.png", squarePNG(t))); code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	doc := readDoc(t, root, "three.test")
	if doc.Logo != "logo.png" || doc.LogoDark != "logo-dark.png" || doc.Icon != "icon.png" {
		t.Fatalf("the three marks collided: %+v", doc)
	}

	// Replacing the dark logo with a JPEG removes logo-dark.png but must not
	// touch logo.png — the "logo-dark" base is its own, not a suffix of "logo".
	if code, _, stderr := runCLI(t, "", "branding", "set", "-dir", root, "-host", "three.test",
		"-logo-dark", writeTempImage(t, "d.jpg", testJPEG)); code != exitOK {
		t.Fatalf("replacing exited %d: %s", code, stderr)
	}
	doc = readDoc(t, root, "three.test")
	if doc.LogoDark != "logo-dark.jpg" {
		t.Errorf("logoDark = %q, want logo-dark.jpg", doc.LogoDark)
	}
	if doc.Logo != "logo.png" {
		t.Errorf("logo = %q, want it untouched", doc.Logo)
	}
	if _, err := os.Stat(filepath.Join(root, "three.test", "logo.png")); err != nil {
		t.Errorf("replacing the dark logo deleted logo.png: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "three.test", "logo-dark.png")); !os.IsNotExist(err) {
		t.Error("the superseded logo-dark.png was left behind to be served by a stale document")
	}
	// And a color-only change keeps all three.
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "three.test", "-color-primary", "#000000"); code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	doc = readDoc(t, root, "three.test")
	if doc.Logo == "" || doc.LogoDark == "" || doc.Icon == "" {
		t.Errorf("a color change dropped a mark: %+v", doc)
	}
}

// TestBrandingSetLogoDarkHelp: the flag and why it exists are in the usage.
func TestBrandingSetLogoDarkHelp(t *testing.T) {
	_, _, stderr := runCLI(t, "", "branding", "set", "-h")
	for _, want := range []string{"-logo-dark", "dark"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("branding set usage does not mention %q:\n%s", want, stderr)
		}
	}
}
