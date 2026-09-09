package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/jmaphttp"
)

// Tests for `moovctl branding`. The theme is that this CLI and the server that
// reads its output must agree: a host or an asset the CLI accepts but the
// server refuses would produce a directory that is silently never served,
// which is the failure mode hardest to notice in production.

// A real 1x1 PNG — the CLI sniffs bytes, so a stub will not do.
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

// writeTempImage drops an image file in a temporary directory.
func writeTempImage(t *testing.T, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// readDoc loads the document the CLI wrote.
func readDoc(t *testing.T, root, host string) brandingDocument {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, host, brandingFileName))
	if err != nil {
		t.Fatalf("reading branding.json: %v", err)
	}
	var doc brandingDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("branding.json is not valid JSON: %v\n%s", err, raw)
	}
	return doc
}

func TestBrandingSetWritesDocumentAndAssets(t *testing.T) {
	root := t.TempDir()
	logo := writeTempImage(t, "company-logo.png", testPNG)
	splash := writeTempImage(t, "office.jpg", testJPEG)

	code, stdout, stderr := runCLI(t, "", "branding", "set",
		"-dir", root,
		"-host", "Mail.ACME.test",
		"-name", "Acme Mail",
		"-tagline", "Correo de la empresa",
		"-support-url", "mailto:it@acme.test",
		"-logo", logo,
		"-splash", splash,
		"-color-primary", "#C0FFEE",
		"-color-on-primary", "#000000",
	)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "mail.acme.test") {
		t.Errorf("stdout does not name the resolved host:\n%s", stdout)
	}

	// The host directory is the LOWERCASED hostname: that is what the server
	// resolves a Host header to.
	doc := readDoc(t, root, "mail.acme.test")
	if doc.Name != "Acme Mail" {
		t.Errorf("name = %q", doc.Name)
	}
	if doc.Tagline != "Correo de la empresa" {
		t.Errorf("tagline = %q", doc.Tagline)
	}
	if doc.SupportURL != "mailto:it@acme.test" {
		t.Errorf("supportUrl = %q", doc.SupportURL)
	}
	// The color is normalized to lowercase, matching the server's rule.
	if doc.Colors.Primary != "#c0ffee" {
		t.Errorf("primary = %q, want #c0ffee", doc.Colors.Primary)
	}

	// Assets are stored under OUR name derived from the sniffed type, never
	// the source filename.
	if doc.Logo != "logo.png" {
		t.Errorf("logo = %q, want logo.png", doc.Logo)
	}
	if doc.Splash != "splash.jpg" {
		t.Errorf("splash = %q, want splash.jpg", doc.Splash)
	}
	for _, name := range []string{"logo.png", "splash.jpg"} {
		if _, err := os.Stat(filepath.Join(root, "mail.acme.test", name)); err != nil {
			t.Errorf("asset %s was not written: %v", name, err)
		}
	}
}

// TestBrandingSetIsIncremental is the behavior that stops an operator from
// deleting a customer's logo by adjusting a color.
func TestBrandingSetIsIncremental(t *testing.T) {
	root := t.TempDir()
	logo := writeTempImage(t, "logo.png", testPNG)

	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "inc.test",
		"-name", "Original", "-logo", logo, "-color-primary", "#111111",
	); code != exitOK {
		t.Fatalf("first set exited %d: %s", code, stderr)
	}

	// Change ONLY the color.
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "inc.test", "-color-primary", "#222222",
	); code != exitOK {
		t.Fatalf("second set exited %d: %s", code, stderr)
	}

	doc := readDoc(t, root, "inc.test")
	if doc.Colors.Primary != "#222222" {
		t.Errorf("primary = %q, want the new #222222", doc.Colors.Primary)
	}
	if doc.Name != "Original" {
		t.Errorf("name = %q, want the preserved Original", doc.Name)
	}
	if doc.Logo != "logo.png" {
		t.Errorf("logo = %q, want the preserved logo.png", doc.Logo)
	}
}

// TestBrandingSetRefusesSVG pins the arbitration: SVG never becomes an asset,
// and the operator is told WHY at the moment they try.
func TestBrandingSetRefusesSVG(t *testing.T) {
	root := t.TempDir()
	svg := writeTempImage(t, "logo.svg",
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))

	code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "svg.test", "-logo", svg)
	if code == exitOK {
		t.Fatal("an SVG logo was accepted")
	}
	if !strings.Contains(strings.ToLower(stderr), "svg") {
		t.Errorf("the refusal does not mention SVG:\n%s", stderr)
	}
	if !strings.Contains(strings.ToLower(stderr), "png") {
		t.Errorf("the refusal does not suggest PNG:\n%s", stderr)
	}
	// Nothing was written.
	if _, err := os.Stat(filepath.Join(root, "svg.test", brandingFileName)); err == nil {
		t.Error("a document was written despite the refusal")
	}
}

// TestBrandingSetRefusesDisguisedContent: an extension the CLI accepts with
// bytes it does not.
func TestBrandingSetRefusesDisguisedContent(t *testing.T) {
	root := t.TempDir()
	cases := map[string][]byte{
		"svg-as-png.png":  []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
		"html-as-png.png": []byte("<!doctype html><script>alert(1)</script>"),
		"text-as-jpg.jpg": []byte("this is just text"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeTempImage(t, name, body)
			code, _, stderr := runCLI(t, "", "branding", "set",
				"-dir", root, "-host", "disguise.test", "-logo", path)
			if code == exitOK {
				t.Fatalf("%s was accepted", name)
			}
			if !strings.Contains(stderr, "bytes were checked") {
				t.Errorf("the refusal does not explain that content was checked:\n%s", stderr)
			}
		})
	}
}

// TestBrandingSetRefusesOversizedAsset.
func TestBrandingSetRefusesOversizedAsset(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, jmaphttp.MaxBrandingAssetBytes+1)
	copy(big, testPNG)
	path := writeTempImage(t, "big.png", big)

	code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "big.test", "-logo", path)
	if code == exitOK {
		t.Fatal("an oversized asset was accepted")
	}
	if !strings.Contains(stderr, "limit") {
		t.Errorf("the refusal does not mention the limit:\n%s", stderr)
	}
}

// TestBrandingSetRejectsBadInput covers the usage-level refusals.
func TestBrandingSetRejectsBadInput(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		args []string
	}{
		{"no host", []string{"branding", "set", "-dir", root, "-name", "X"}},
		{"empty host", []string{"branding", "set", "-dir", root, "-host", "", "-name", "X"}},
		{"host with slash", []string{"branding", "set", "-dir", root, "-host", "a/b", "-name", "X"}},
		{"host traversal", []string{"branding", "set", "-dir", root, "-host", "../etc", "-name", "X"}},
		{"host underscore", []string{"branding", "set", "-dir", root, "-host", "a_b.test", "-name", "X"}},
		{"no fields", []string{"branding", "set", "-dir", root, "-host", "x.test"}},
		{"bad color", []string{"branding", "set", "-dir", root, "-host", "x.test", "-color-primary", "red"}},
		{"bad color length", []string{"branding", "set", "-dir", root, "-host", "x.test", "-color-primary", "#12345"}},
		{"javascript support url", []string{"branding", "set", "-dir", root, "-host", "x.test",
			"-support-url", "javascript:alert(1)"}},
		{"positional argument", []string{"branding", "set", "-dir", root, "-host", "x.test", "extra"}},
		{"unknown subcommand", []string{"branding", "frobnicate"}},
		{"no subcommand", []string{"branding"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, "", tc.args...)
			if code != exitUsage {
				t.Errorf("exit = %d, want %d (usage)\nstderr: %s", code, exitUsage, stderr)
			}
		})
	}
}

// TestBrandingSetReplacesAssetAcrossExtensions: swapping a PNG logo for a JPEG
// must not leave the stale PNG behind for a cached document to point at.
func TestBrandingSetReplacesAssetAcrossExtensions(t *testing.T) {
	root := t.TempDir()

	png := writeTempImage(t, "a.png", testPNG)
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "swap.test", "-logo", png); code != exitOK {
		t.Fatalf("first set exited %d: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "swap.test", "logo.png")); err != nil {
		t.Fatalf("logo.png missing: %v", err)
	}

	jpg := writeTempImage(t, "b.jpg", testJPEG)
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "swap.test", "-logo", jpg); code != exitOK {
		t.Fatalf("second set exited %d: %s", code, stderr)
	}

	if doc := readDoc(t, root, "swap.test"); doc.Logo != "logo.jpg" {
		t.Errorf("logo = %q, want logo.jpg", doc.Logo)
	}
	if _, err := os.Stat(filepath.Join(root, "swap.test", "logo.png")); err == nil {
		t.Error("the superseded logo.png is still on disk")
	}
}

// TestBrandingClearAsset: an explicit empty value removes the reference.
func TestBrandingClearAsset(t *testing.T) {
	root := t.TempDir()
	logo := writeTempImage(t, "logo.png", testPNG)

	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "clear.test", "-name", "Clear", "-logo", logo); code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "clear.test", "-logo", ""); code != exitOK {
		t.Fatalf("clearing exited %d: %s", code, stderr)
	}
	doc := readDoc(t, root, "clear.test")
	if doc.Logo != "" {
		t.Errorf("logo = %q, want it cleared", doc.Logo)
	}
	if doc.Name != "Clear" {
		t.Errorf("name = %q, want it preserved", doc.Name)
	}
}

func TestBrandingShowAndList(t *testing.T) {
	root := t.TempDir()
	logo := writeTempImage(t, "logo.png", testPNG)

	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "shown.test",
		"-name", "Shown Co", "-logo", logo, "-color-primary", "#abcdef"); code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}

	code, stdout, stderr := runCLI(t, "", "branding", "show", "-dir", root, "-host", "shown.test")
	if code != exitOK {
		t.Fatalf("show exited %d: %s", code, stderr)
	}
	for _, want := range []string{"shown.test", "Shown Co", "logo.png", "#abcdef"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show output missing %q:\n%s", want, stdout)
		}
	}

	// An unconfigured host is reported as such, not as an error: "this host
	// gets Moov's defaults" is a valid state, not a failure.
	code, stdout, _ = runCLI(t, "", "branding", "show", "-dir", root, "-host", "absent.test")
	if code != exitOK {
		t.Errorf("show of an unconfigured host exited %d, want 0", code)
	}
	if !strings.Contains(stdout, "default") {
		t.Errorf("show does not say the host gets the defaults:\n%s", stdout)
	}

	code, stdout, stderr = runCLI(t, "", "branding", "list", "-dir", root)
	if code != exitOK {
		t.Fatalf("list exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "shown.test") || !strings.Contains(stdout, "Shown Co") {
		t.Errorf("list output:\n%s", stdout)
	}

	// An empty root lists cleanly rather than failing.
	code, stdout, _ = runCLI(t, "", "branding", "list", "-dir", t.TempDir())
	if code != exitOK {
		t.Errorf("list of an empty root exited %d", code)
	}
	if !strings.Contains(strings.ToLower(stdout), "no branding") {
		t.Errorf("list of an empty root:\n%s", stdout)
	}
}

func TestBrandingUnset(t *testing.T) {
	root := t.TempDir()
	logo := writeTempImage(t, "logo.png", testPNG)

	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "gone.test", "-name", "Gone", "-logo", logo); code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}

	code, stdout, stderr := runCLI(t, "", "branding", "unset", "-dir", root, "-host", "gone.test")
	if code != exitOK {
		t.Fatalf("unset exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "default") {
		t.Errorf("unset does not say the host reverts to the defaults:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(root, "gone.test", brandingFileName)); err == nil {
		t.Error("branding.json survived unset")
	}
	if _, err := os.Stat(filepath.Join(root, "gone.test", "logo.png")); err == nil {
		t.Error("the asset survived unset without -keep-assets")
	}

	// Unsetting an unconfigured host is a no-op, not an error.
	if code, _, _ := runCLI(t, "", "branding", "unset", "-dir", root, "-host", "never.test"); code != exitOK {
		t.Errorf("unset of an unconfigured host exited %d, want 0", code)
	}
}

func TestBrandingUnsetKeepAssets(t *testing.T) {
	root := t.TempDir()
	logo := writeTempImage(t, "logo.png", testPNG)

	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "keep.test", "-logo", logo); code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	if code, _, stderr := runCLI(t, "", "branding", "unset",
		"-dir", root, "-host", "keep.test", "-keep-assets"); code != exitOK {
		t.Fatalf("unset exited %d: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "keep.test", "logo.png")); err != nil {
		t.Errorf("-keep-assets did not keep the asset: %v", err)
	}
}

// TestBrandingRootFromEnvironment: MOOV_BRANDING_DIR is honored when -dir is
// absent, so a deployment can set it once.
func TestBrandingRootFromEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv(envBrandingDir, root)

	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-host", "env.test", "-name", "From Env"); code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	if doc := readDoc(t, root, "env.test"); doc.Name != "From Env" {
		t.Errorf("name = %q", doc.Name)
	}
}

// --- the CLI/server agreement ----------------------------------------------

// TestHostSanitisationMatchesServer is the pin that keeps the two
// implementations from drifting. A host the CLI writes a directory for but the
// server refuses to resolve would be branding that silently never appears.
func TestHostSanitisationMatchesServer(t *testing.T) {
	hosts := []string{
		"mail.example.com", "MAIL.EXAMPLE.COM", "mail.example.com.",
		"a.b.c.d.example.com", "xn--80ak6aa92e.com", "host-with-dashes.test",
		"", ".", "..", "../etc", "a/b", `a\b`, "a_b", "a b", "a%2fb",
		"-leading.test", ".leading.test", "[::1]", "mail.example.com:443",
		"UPPER.Example.Com", "trailing..dots.test", "hüst.test",
	}
	for _, h := range hosts {
		t.Run(h, func(t *testing.T) {
			cli := sanitizeBrandingHost(h)
			// The server additionally strips a port; the CLI takes a bare
			// hostname, so a value with a port is compared after that split.
			srv := jmaphttp.ResolveBrandingHostForTest(h)
			if cli != srv {
				t.Errorf("sanitizeBrandingHost(%q) = %q but the server resolves %q",
					h, cli, srv)
			}
		})
	}
}

// TestBrandingFileNameMatchesServer pins the filename the two sides agree on.
func TestBrandingFileNameMatchesServer(t *testing.T) {
	if brandingFileName != jmaphttp.BrandingConfigFileForTest {
		t.Errorf("the CLI writes %q but the server reads %q",
			brandingFileName, jmaphttp.BrandingConfigFileForTest)
	}
}

// TestImageExtensionsMatchServerAllowlist: every extension the CLI can store
// must be one the server's allowlist names, and every allowed extension must
// be producible by the sniffer. Otherwise an operator gets a file the server
// will not serve, or an allowlist entry that is dead.
func TestImageExtensionsMatchServerAllowlist(t *testing.T) {
	bodies := map[string][]byte{
		".png":  testPNG,
		".jpg":  testJPEG,
		".webp": append(append([]byte("RIFF"), 0x10, 0x00, 0x00, 0x00), []byte("WEBP")...),
		".gif":  []byte("GIF89a\x01\x00\x01\x00\x00"),
	}
	for ext, body := range bodies {
		got, ok := imageExtensionForCLI(body)
		if !ok {
			t.Errorf("the CLI does not recognize a %s image", ext)
			continue
		}
		if got != ext {
			t.Errorf("a %s image is stored as %q", ext, got)
		}
		if !containsFold(jmaphttp.AllowedBrandingExtensions, got) {
			t.Errorf("the CLI stores %q, which the server's allowlist does not name", got)
		}
	}
	// .jpeg is accepted as INPUT (the allowlist names it) even though output is
	// normalized to .jpg.
	if !containsFold(jmaphttp.AllowedBrandingExtensions, ".jpeg") {
		t.Error("the allowlist should accept .jpeg as an input extension")
	}
	// And SVG is in neither.
	if containsFold(jmaphttp.AllowedBrandingExtensions, ".svg") {
		t.Error("SVG must never be in the allowlist")
	}
}

// TestBrandingHelpIsDocumented: the top-level usage names the new command.
func TestBrandingHelpIsDocumented(t *testing.T) {
	code, stdout, _ := runCLI(t, "", "help")
	if code != exitOK {
		t.Fatalf("help exited %d", code)
	}
	for _, want := range []string{"branding set", "branding show", "branding list", "branding unset"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help does not document %q", want)
		}
	}
	if !strings.Contains(stdout, envBrandingDir) {
		t.Errorf("help does not document %s", envBrandingDir)
	}
}

// TestWriteBrandingFileIsAtomic: no temporary file is left behind, and the
// result is valid JSON.
func TestWriteBrandingFileIsAtomic(t *testing.T) {
	dir := t.TempDir()
	if err := writeBrandingFile(dir, brandingDocument{Name: "Atomic"}); err != nil {
		t.Fatalf("writeBrandingFile: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 || entries[0].Name() != brandingFileName {
		t.Errorf("directory contents = %v, want only %s", entries, brandingFileName)
	}
}

// --- short name and the PWA icons -------------------------------------------

// TestBrandingSetShortName: persisted verbatim within the cap, refused past
// it — the server would truncate, the CLI says so first.
func TestBrandingSetShortName(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "short.test", "-name", "Acme Corporate Mailbox",
		"-short-name", " Acme ",
	); code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	if doc := readDoc(t, root, "short.test"); doc.ShortName != "Acme" {
		t.Errorf("shortName = %q, want Acme (trimmed)", doc.ShortName)
	}

	code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "short.test", "-short-name", "Corporate Mailbox")
	if code != exitUsage {
		t.Errorf("a 17-character short name exited %d, want %d (usage)", code, exitUsage)
	}
	if !strings.Contains(stderr, "12") {
		t.Errorf("the refusal does not name the limit:\n%s", stderr)
	}
	// Refused means unchanged.
	if doc := readDoc(t, root, "short.test"); doc.ShortName != "Acme" {
		t.Errorf("shortName = %q after a refused set, want the previous Acme", doc.ShortName)
	}

	// Twelve exactly, in runes not bytes, is accepted.
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "short.test", "-short-name", "Ñandú Correo"); code != exitOK {
		t.Errorf("a 12-rune short name exited %d: %s", code, stderr)
	}

	code, stdout, _ := runCLI(t, "", "branding", "show", "-dir", root, "-host", "short.test")
	if code != exitOK || !strings.Contains(stdout, "SHORT NAME") || !strings.Contains(stdout, "Ñandú Correo") {
		t.Errorf("show does not print the short name:\n%s", stdout)
	}
}

// TestBrandingDeclaresIconFallback: a logo the server can display but cannot
// render into icons is declared at `set` and explained by `show`; a
// renderable one is reported as the icon source.
func TestBrandingDeclaresIconFallback(t *testing.T) {
	root := t.TempDir()
	webp := writeTempImage(t, "logo.webp",
		append(append([]byte("RIFF"), 0x10, 0x00, 0x00, 0x00), []byte("WEBPVP8 ")...))

	code, stdout, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "webp.test", "-logo", webp)
	if code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "PWA icons") || !strings.Contains(stdout, "WebP") {
		t.Errorf("set did not declare the icon fallback for a WebP logo:\n%s", stdout)
	}

	code, stdout, _ = runCLI(t, "", "branding", "show", "-dir", root, "-host", "webp.test")
	if code != exitOK {
		t.Fatalf("show exited %d", code)
	}
	if !strings.Contains(stdout, "PWA ICONS") || !strings.Contains(stdout, "Moov's") || !strings.Contains(stdout, "WebP") {
		t.Errorf("show does not explain the icon fallback:\n%s", stdout)
	}

	// A PNG logo: no note at set, and show names it as the source.
	png := writeTempImage(t, "logo.png", testPNG)
	code, stdout, stderr = runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "png.test", "-logo", png)
	if code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "PWA icons will stay") {
		t.Errorf("set warned about a renderable PNG logo:\n%s", stdout)
	}
	_, stdout, _ = runCLI(t, "", "branding", "show", "-dir", root, "-host", "png.test")
	if !strings.Contains(stdout, "generated from logo.png") {
		t.Errorf("show does not name the PNG as the icon source:\n%s", stdout)
	}

	// No logo at all: Moov's, quietly.
	if code, _, stderr := runCLI(t, "", "branding", "set",
		"-dir", root, "-host", "bare.test", "-name", "Bare"); code != exitOK {
		t.Fatalf("set exited %d: %s", code, stderr)
	}
	_, stdout, _ = runCLI(t, "", "branding", "show", "-dir", root, "-host", "bare.test")
	if !strings.Contains(stdout, "no logo configured") {
		t.Errorf("show does not say there is no logo:\n%s", stdout)
	}
}

// TestShortNameCapMatchesServer pins the CLI's cap to the server's.
func TestShortNameCapMatchesServer(t *testing.T) {
	if maxShortNameRunes != jmaphttp.MaxBrandingShortNameRunesForTest {
		t.Errorf("the CLI refuses past %d runes but the server truncates at %d",
			maxShortNameRunes, jmaphttp.MaxBrandingShortNameRunesForTest)
	}
}
