package branding

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A real 1x1 PNG: everything here sniffs bytes, so a stub will not do.
var testPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05,
	0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00,
	0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

var testJPEG = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("JFIF stub payload")...)

func webpBytes() []byte {
	b := []byte("RIFF")
	b = append(b, 0x10, 0x00, 0x00, 0x00)
	return append(b, []byte("WEBP")...)
}

func pngOfSize(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestNormalizeHostTable(t *testing.T) {
	cases := map[string]string{
		"mail.example.com":       "mail.example.com",
		"Mail.Example.COM:8443":  "mail.example.com",
		"mail.example.com.":      "mail.example.com",
		" mail.example.com ":     "mail.example.com",
		"":                       "",
		"..":                     "",
		"a/b":                    "",
		"a\\b":                   "",
		"mail..example.com":      "",
		"[::1]:8080":             "",
		"-bad.example":           "",
		".bad.example":           "",
		"mail.example.com%2f..":  "",
		"mail.example.com/../x":  "",
		"mail_example.com":       "",
		"mail.exämple.com":       "",
		"localhost":              "localhost",
		"xn--mnchen-3ya.example": "xn--mnchen-3ya.example",
	}
	for in, want := range cases {
		if got := NormalizeHost(in); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeMailbox(t *testing.T) {
	ok := map[string]string{
		"Ana@Acme.Example":     "ana@acme.example",
		" ana@acme.example ":   "ana@acme.example",
		"a.b+tag@acme.example": "a.b+tag@acme.example",
	}
	for in, want := range ok {
		got, valid := NormalizeMailbox(in)
		if !valid || got != want {
			t.Errorf("NormalizeMailbox(%q) = %q,%v; want %q,true", in, got, valid, want)
		}
	}
	for _, bad := range []string{"", "ana", "@acme.example", "ana@", "ana@acme..example",
		"an a@acme.example", "ana@[::1]", "ana@acme.example/x", "ana@@acme.example",
		"a\"b@acme.example", strings.Repeat("a", 65) + "@acme.example"} {
		if got, valid := NormalizeMailbox(bad); valid {
			t.Errorf("NormalizeMailbox(%q) accepted as %q", bad, got)
		}
	}
}

func TestGrantRevokeNormalizeSortAndDedupe(t *testing.T) {
	var f File
	for _, m := range []string{"Zoe@Acme.Example", "ana@acme.example", "ZOE@acme.example"} {
		if _, err := f.Grant(m); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(f.BrandAdmins, ","); got != "ana@acme.example,zoe@acme.example" {
		t.Fatalf("admins = %q", got)
	}
	if !f.IsBrandAdmin("ANA@ACME.EXAMPLE") || f.IsBrandAdmin("bob@acme.example") {
		t.Fatal("IsBrandAdmin does not normalize")
	}
	changed, _ := f.Grant("ana@acme.example")
	if changed {
		t.Fatal("re-granting reported a change")
	}
	found, _ := f.Revoke("Ana@acme.example")
	if !found || len(f.BrandAdmins) != 1 {
		t.Fatalf("revoke: found=%v admins=%v", found, f.BrandAdmins)
	}
	found, _ = f.Revoke("zoe@acme.example")
	if !found || f.BrandAdmins != nil {
		t.Fatalf("revoking the last admin should leave nil (omitted from JSON), got %#v", f.BrandAdmins)
	}
	if _, err := f.Grant("nonsense"); !errors.Is(err, ErrInvalidMailbox) {
		t.Fatalf("Grant(nonsense) = %v", err)
	}
}

func TestHasBrandIgnoresAdmins(t *testing.T) {
	if (File{BrandAdmins: []string{"a@b.example"}}).HasBrand() {
		t.Fatal("an admin-only document is not a brand")
	}
	if !(File{Colors: Colors{Primary: "#000"}}).HasBrand() {
		t.Fatal("a color is a brand")
	}
}

func TestAssetKinds(t *testing.T) {
	want := map[AssetKind]string{AssetLogo: "logo", AssetLogoDark: "logo-dark", AssetIcon: "icon", AssetSplash: "splash"}
	for k, base := range want {
		if k.BaseName() != base {
			t.Errorf("%s.BaseName() = %q, want %q", k, k.BaseName(), base)
		}
		if got, ok := ParseAssetKind(string(k)); !ok || got != k {
			t.Errorf("ParseAssetKind(%q) = %q,%v", k, got, ok)
		}
		var f File
		*k.Field(&f) = "x"
		raw, _ := json.Marshal(f)
		if !strings.Contains(string(raw), `"`+string(k)+`":"x"`) {
			t.Errorf("%s.Field does not map to the JSON field %q: %s", k, k, raw)
		}
	}
	for _, bad := range []string{"", "Logo", "logo-dark", "favicon", "../logo"} {
		if _, ok := ParseAssetKind(bad); ok {
			t.Errorf("ParseAssetKind(%q) accepted", bad)
		}
	}
	if !AssetLogo.FeedsIcons() || !AssetIcon.FeedsIcons() || AssetLogoDark.FeedsIcons() || AssetSplash.FeedsIcons() {
		t.Error("FeedsIcons: the chain is icon, then logo, and nothing else")
	}
}

func TestSniffAndExtensionAgree(t *testing.T) {
	for _, c := range []struct {
		body []byte
		ct   string
		ext  string
	}{
		{testPNG, "image/png", ".png"},
		{testJPEG, "image/jpeg", ".jpg"},
		{webpBytes(), "image/webp", ".webp"},
		{[]byte("GIF89a\x01\x00\x01\x00\x00"), "image/gif", ".gif"},
	} {
		ct, ok := SniffImageType(c.body)
		if !ok || ct != c.ct {
			t.Errorf("SniffImageType = %q,%v want %q", ct, ok, c.ct)
		}
		ext, ok := ExtensionFor(ct)
		if !ok || ext != c.ext {
			t.Errorf("ExtensionFor(%q) = %q,%v want %q", ct, ext, ok, c.ext)
		}
		found := false
		for _, a := range AllowedExtensions {
			if a == ext {
				found = true
			}
		}
		if !found {
			t.Errorf("%q is not in AllowedExtensions", ext)
		}
	}
	for _, bad := range [][]byte{nil, []byte("<svg xmlns='x'/>"), []byte("<html>"), []byte("\x89PNG")} {
		if _, ok := SniffImageType(bad); ok {
			t.Errorf("SniffImageType(%q) accepted", bad)
		}
	}
}

func TestLooksLikeSVG(t *testing.T) {
	for _, b := range []string{"<svg xmlns=\"http://www.w3.org/2000/svg\"/>", "  \n<?xml version=\"1.0\"?><svg/>",
		"\xef\xbb\xbf<SVG>", "<!DOCTYPE svg PUBLIC>"} {
		if !LooksLikeSVG([]byte(b)) {
			t.Errorf("LooksLikeSVG(%q) = false", b)
		}
	}
	for _, b := range []string{"<html>", "plain", "", "\x89PNG"} {
		if LooksLikeSVG([]byte(b)) {
			t.Errorf("LooksLikeSVG(%q) = true", b)
		}
	}
}

func TestIconSourceNotesWording(t *testing.T) {
	if notes := IconSourceNotes(AssetSplash, webpBytes()); notes != nil {
		t.Fatalf("splash never feeds the icons, got %v", notes)
	}
	notes := IconSourceNotes(AssetLogo, webpBytes())
	if len(notes) != 1 || !strings.Contains(notes[0], "will not be rendered from this logo") ||
		!strings.Contains(notes[0], ErrIconWebP.Error()) {
		t.Fatalf("webp logo notes = %v", notes)
	}
	notes = IconSourceNotes(AssetIcon, pngOfSize(t, 200, 40))
	if len(notes) != 1 || notes[0] != "the icon is 200x40, which is not square; launchers show a square, "+
		"so it will be contained inside one with bands of the plate color around it" {
		t.Fatalf("wide icon notes = %v", notes)
	}
	if notes := IconSourceNotes(AssetLogo, pngOfSize(t, 200, 40)); notes != nil {
		t.Fatalf("a wide LOGO is fine, got %v", notes)
	}
	if notes := IconSourceNotes(AssetIcon, pngOfSize(t, 64, 64)); notes != nil {
		t.Fatalf("a square icon has nothing to say, got %v", notes)
	}
	if err := ValidateIconSource(pngOfSize(t, MaxIconDimension+1, 1)); err == nil {
		t.Fatal("an oversized icon source was accepted")
	}
}

// --- the writer --------------------------------------------------------------

func TestDirWriteIsAtomicAndKeepsPreviousOnFailure(t *testing.T) {
	root := t.TempDir()
	d, err := HostDir(root, "mail.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write(File{Name: "First"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.StoreAsset(AssetLogo, testPNG); err != nil {
		t.Fatal(err)
	}

	writeFailureHook = func(string) error { return errors.New("disk on fire") }
	t.Cleanup(func() { writeFailureHook = func(string) error { return nil } })

	if _, err := d.Write(File{Name: "Second"}); err == nil {
		t.Fatal("the failing write reported success")
	}
	if _, err := d.StoreAsset(AssetLogo, testJPEG); err == nil {
		t.Fatal("the failing asset write reported success")
	}

	f, err := d.Read()
	if err != nil || f.Name != "First" {
		t.Fatalf("after a failed write the document is %+v (%v); want the previous one", f, err)
	}
	got, err := os.ReadFile(filepath.Join(d.Path(), "logo.png"))
	if err != nil || !bytes.Equal(got, testPNG) {
		t.Fatalf("after a failed write logo.png changed or vanished: %v", err)
	}
	entries, _ := os.ReadDir(d.Path())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
	if len(entries) != 2 {
		t.Errorf("directory holds %d entries, want branding.json and logo.png", len(entries))
	}
}

func TestDirPermissions(t *testing.T) {
	root := t.TempDir()
	d, _ := HostDir(root, "mail.example.com")
	if _, err := d.Write(File{Name: "Perm"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.StoreAsset(AssetIcon, testPNG); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(d.Path()); info.Mode().Perm()&0o055 != 0o055 {
		t.Errorf("directory mode = %v, want world-traversable (moovd runs as another user)", info.Mode())
	}
	for _, name := range []string{ConfigFile, "icon.png"} {
		if info, _ := os.Stat(filepath.Join(d.Path(), name)); info.Mode().Perm()&0o044 != 0o044 {
			t.Errorf("%s mode = %v, want world-readable", name, info.Mode())
		}
	}
}

func TestStoreAssetNamesAndSiblings(t *testing.T) {
	d, _ := HostDir(t.TempDir(), "mail.example.com")
	stored, err := d.StoreAsset(AssetLogoDark, testPNG)
	if err != nil || stored != "logo-dark.png" {
		t.Fatalf("StoreAsset = %q, %v", stored, err)
	}
	stored, err = d.StoreAsset(AssetLogoDark, testJPEG)
	if err != nil || stored != "logo-dark.jpg" {
		t.Fatalf("StoreAsset = %q, %v", stored, err)
	}
	if _, err := os.Stat(filepath.Join(d.Path(), "logo-dark.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("replacing the asset under a new extension left the old file behind")
	}
	for _, c := range []struct {
		body []byte
		want error
	}{
		{nil, ErrEmptyAsset},
		{bytes.Repeat([]byte{0}, MaxAssetBytes+1), ErrAssetTooLarge},
		{[]byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), ErrSVG},
		{[]byte("<html><script>alert(1)</script></html>"), ErrNotImage},
	} {
		if _, err := d.StoreAsset(AssetLogo, c.body); !errors.Is(err, c.want) {
			t.Errorf("StoreAsset(%d bytes) = %v, want %v", len(c.body), err, c.want)
		}
	}
	if _, err := d.StoreAsset(AssetKind("favicon"), testPNG); err == nil {
		t.Error("an unknown kind was stored")
	}
	if _, err := os.Stat(filepath.Join(d.Path(), "logo.png")); err == nil {
		t.Error("a refused asset left a file behind")
	}
}

func TestUnsetAndResetPreserveWhatTheyPromise(t *testing.T) {
	d, _ := HostDir(t.TempDir(), "mail.example.com")
	f := File{Name: "Acme", Colors: Colors{Primary: "#000000"}, BrandAdmins: []string{"ana@acme.example"}}
	f.Logo, _ = d.StoreAsset(AssetLogo, testPNG)
	f.Splash, _ = d.StoreAsset(AssetSplash, testJPEG)
	if err := os.WriteFile(filepath.Join(d.Path(), "operator-notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write(f); err != nil {
		t.Fatal(err)
	}

	kept, _, err := d.Reset()
	if err != nil {
		t.Fatal(err)
	}
	if kept.HasBrand() || len(kept.BrandAdmins) != 1 {
		t.Fatalf("Reset kept %+v; want only the admins", kept)
	}
	got, _ := d.Read()
	if got.Name != "" || got.Logo != "" || len(got.BrandAdmins) != 1 {
		t.Fatalf("Reset wrote %+v", got)
	}
	for _, gone := range []string{"logo.png", "splash.jpg"} {
		if _, err := os.Stat(filepath.Join(d.Path(), gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Reset left %s behind", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(d.Path(), "operator-notes.txt")); err != nil {
		t.Error("Reset removed a file it did not record")
	}

	if err := d.Unset(false); err != nil {
		t.Fatal(err)
	}
	if d.Exists() {
		t.Fatal("Unset left the document")
	}
	if _, err := os.Stat(d.Path()); err != nil {
		t.Error("Unset removed a directory that still held an operator's file")
	}
}

func TestHostDirRefusesUnnormalizedHost(t *testing.T) {
	for _, bad := range []string{"", "Mail.Example.com", "../x", "a/b", "mail.example.com:443"} {
		if _, err := HostDir(t.TempDir(), bad); err == nil {
			t.Errorf("HostDir(%q) accepted", bad)
		}
	}
	if _, err := HostDir("", "mail.example.com"); err == nil {
		t.Error("HostDir with no root accepted")
	}
}

func TestReadRejectsMalformedAndAcceptsAbsent(t *testing.T) {
	d, _ := HostDir(t.TempDir(), "mail.example.com")
	if f, err := d.Read(); err != nil || f.HasBrand() {
		t.Fatalf("Read of an absent document = %+v, %v", f, err)
	}
	_ = os.MkdirAll(d.Path(), 0o755)
	_ = os.WriteFile(d.ConfigPath(), []byte("{not json"), 0o644)
	if _, err := d.Read(); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
}

// TestDeriveSplashColorsTable pins the mix arithmetic. The expected values are
// each channel scaled by (1-amount) and rounded half-up, computed by hand, so
// the test fails if the constants or the rounding ever move rather than
// re-deriving whatever the implementation does.
func TestDeriveSplashColorsTable(t *testing.T) {
	cases := []struct {
		primary  string
		wantFrom string
		wantTo   string
	}{
		// The owner's pastel cyan, the color that found the bug: 0xb8=184,
		// 0xfa=250, 0xff=255. 30% -> 55.2/75/76.5 -> #374b4d (76.5 rounds to
		// 77 = 0x4d). 65% -> 119.6/162.5/165.75 -> #78a3a6.
		{"#b8faff", "#374b4d", "#78a3a6"},
		// Moov's own indigo, for a value anybody can check: 0x5b=91, 0xd6=214.
		// 30% -> 27.3/27.3/64.2 -> #1b1b40. 65% -> 59.15/59.15/139.1 -> #3b3b8b.
		{"#5b5bd6", "#1b1b40", "#3b3b8b"},
		// Black stays black at every mix; white is the pure fraction.
		{"#000000", "#000000", "#000000"},
		{"#ffffff", "#4d4d4d", "#a6a6a6"},
		// Three-digit form expands by DOUBLING the digit (#f00 is #ff0000, not
		// #f00000), which is the one place a hand-rolled parser gets it wrong.
		{"#f00", "#4d0000", "#a60000"},
		// Case and surrounding space are normalized before the mix.
		{"  #B8FAFF ", "#374b4d", "#78a3a6"},
	}
	for _, c := range cases {
		from, to := DeriveSplashColors(c.primary)
		if from != c.wantFrom || to != c.wantTo {
			t.Errorf("DeriveSplashColors(%q) = %q, %q; want %q, %q",
				c.primary, from, to, c.wantFrom, c.wantTo)
		}
	}
}

// TestDeriveSplashColorsRefusesNonColors: an unparseable primary derives
// nothing, so the caller keeps its own fallback rather than emitting a
// custom property built from garbage.
func TestDeriveSplashColorsRefusesNonColors(t *testing.T) {
	for _, bad := range []string{"", "rebeccapurple", "#12345", "#gggggg", "rgb(1,2,3)", "5b5bd6"} {
		from, to := DeriveSplashColors(bad)
		if from != "" || to != "" {
			t.Errorf("DeriveSplashColors(%q) = %q, %q; want two empty strings", bad, from, to)
		}
	}
}

// TestDeriveSplashColorsIsDeterministicAndDark: whatever the primary, both
// stops come back as valid lowercase hex, and `from` is never lighter than
// `to` — the gradient the panel paints runs deep-to-mid, not the other way.
func TestDeriveSplashColorsIsDeterministicAndDark(t *testing.T) {
	for _, primary := range []string{"#b8faff", "#5b5bd6", "#0f766e", "#ffcc00", "#123", "#010203"} {
		from, to := DeriveSplashColors(primary)
		if NormalizeHexColor(from) != from || len(from) != 7 {
			t.Errorf("from = %q for %q, want a lowercase #rrggbb", from, primary)
		}
		if NormalizeHexColor(to) != to || len(to) != 7 {
			t.Errorf("to = %q for %q, want a lowercase #rrggbb", to, primary)
		}
		fr, fg, fb, _ := parseHexColor(from)
		tr, tg, tb, _ := parseHexColor(to)
		if int(fr)+int(fg)+int(fb) > int(tr)+int(tg)+int(tb) {
			t.Errorf("for %q the from stop %q is lighter than the to stop %q", primary, from, to)
		}
		again1, again2 := DeriveSplashColors(primary)
		if again1 != from || again2 != to {
			t.Errorf("DeriveSplashColors(%q) is not deterministic", primary)
		}
	}
}
