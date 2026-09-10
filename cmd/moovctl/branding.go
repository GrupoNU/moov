package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"math"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	// The decoders the CLI needs to measure an icon's aspect ratio. They
	// mirror the server's set, WebP excluded for the same reason: its decoder
	// is not vendored, and a WebP icon is refused as an icon source anyway.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/GrupoNU/moov/internal/jmaphttp"
)

// `moovctl branding` writes the per-host branding directories that the public
// GET /branding endpoint serves (arbitration W-A1 of L2-pwa §3).
//
// # Why a CLI and not an API
//
// Same reason `account add` is a CLI: this writes to the server's filesystem
// and there is no authenticated administrator concept in Moov yet. A web
// administration UI is a later phase (L2-pwa §3, W-A1: "una UI de
// administración es fase posterior"); until it exists, an operator with shell
// access is the only principal who can change a brand, which is exactly the
// right blast radius for something that renders on the login page.
//
// # The layout it writes
//
//	<dir>/<host>/branding.json     the document (name, colors, asset names)
//	<dir>/<host>/logo.png          the assets, copied and validated
//	<dir>/<host>/logo-dark.png
//	<dir>/<host>/icon.png
//	<dir>/<host>/splash.jpg
//
// The server re-validates everything it reads, so this CLI's validation is
// about giving the operator a clear refusal AT THE MOMENT they make a mistake
// rather than about protecting the server. Both layers are needed: a file
// dropped into the directory by hand never went through this one.

// envBrandingDir names the variable holding the branding root.
const envBrandingDir = "MOOV_BRANDING_DIR"

// defaultBrandingDir is where a deployment mounts the branding volume. It
// mirrors the path in deploy/docker-compose.yml.
const defaultBrandingDir = "/etc/moov/branding"

func brandingCommand(_ context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return usageErrorf("branding needs a subcommand (set, show, list, unset)")
	}
	switch args[0] {
	case "set":
		return brandingSet(e, args[1:])
	case "show":
		return brandingShow(e, args[1:])
	case "list":
		return brandingList(e, args[1:])
	case "unset":
		return brandingUnset(e, args[1:])
	default:
		return usageErrorf("unknown branding subcommand %q (want set, show, list or unset)", args[0])
	}
}

// brandingSet writes or updates one host's branding.
//
// It is INCREMENTAL: flags that are not passed leave the existing value alone,
// so an operator can change one color without re-supplying the logo. That is
// the behavior a `set` on a multi-field record should have, and the
// alternative (unspecified means empty) would silently delete a customer's
// logo every time someone adjusted a color.
func brandingSet(e *env, args []string) error {
	fs := flag.NewFlagSet("branding set", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	host := fs.String("host", "", "the hostname this branding applies to (required, e.g. mail.example.com)")
	dir := fs.String("dir", "", "the branding root (default "+envBrandingDir+", or "+defaultBrandingDir+")")
	name := fs.String("name", "", "the product name shown in the UI and the browser tab")
	shortName := fs.String("short-name", "", "the name under the installed app's icon (at most 12 characters; "+
		"derived from -name when not set)")
	tagline := fs.String("tagline", "", "an optional line under the product name on the login panel")
	supportURL := fs.String("support-url", "", "where \"contact your administrator\" points (https:// or mailto:)")
	// The operator's own legal links, shown in the footer beside Moov's
	// non-removable source and license links. Same scheme allow-list as
	// -support-url: all three end up as an href on a page we serve.
	privacyURL := fs.String("privacy-url", "", "the operator's privacy policy, shown in the legal footer (https:// or mailto:)")
	termsURL := fs.String("terms-url", "", "the operator's terms of service, shown in the legal footer (https:// or mailto:)")
	logo := fs.String("logo", "", "path to the logo image (png, jpg, webp or gif)")
	logoDark := fs.String("logo-dark", "", "path to the wordmark for DARK backgrounds "+
		"(png, jpg, webp or gif): the login panel and the dark theme")
	icon := fs.String("icon", "", "path to the SQUARE icon the installed app's icons and the "+
		"favicon are rendered from (png, jpg or gif); defaults to the logo")
	splash := fs.String("splash", "", "path to the login panel image (png, jpg, webp or gif)")
	colorPrimary := fs.String("color-primary", "", "accent color as CSS hex, e.g. #5b5bd6")
	colorOnPrimary := fs.String("color-on-primary", "", "text color drawn on the accent, e.g. #ffffff")
	colorSplashFrom := fs.String("color-splash-from", "", "first stop of the login panel gradient")
	colorSplashTo := fs.String("color-splash-to", "", "second stop of the login panel gradient")

	fs.Usage = func() {
		out(e.stderr, "Usage: moovctl branding set -host <hostname> [flags]\n\n"+
			"Writes <dir>/<host>/branding.json and copies the given assets beside it.\n"+
			"Flags that are not passed keep their current value.\n\n"+
			"Three marks, three jobs:\n"+
			"  -logo       the top bar and the login panel.\n"+
			"  -logo-dark  the same wordmark drawn for dark backgrounds: the login\n"+
			"              panel and the dark theme. Without it the app puts the light\n"+
			"              logo on a small light plate, which is legible but is a plate\n"+
			"              you did not design.\n"+
			"  -icon       the SQUARE mark the installed app's icons and the favicon\n"+
			"              are rendered from; falls back to -logo. Give one when your\n"+
			"              primary color is dark, since those icons sit on a plate of\n"+
			"              it and a dark logo disappears into it.\n\n"+
			"SVG is deliberately not accepted: it is an XML document that can carry\n"+
			"scripts, and the login page is where passwords are typed. Export to PNG.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return usageErrorf("branding set takes no positional arguments")
	}

	resolvedHost, err := requireBrandingHost(*host)
	if err != nil {
		return err
	}
	root, err := brandingRoot(*dir)
	if err != nil {
		return err
	}

	hostDir := filepath.Join(root, resolvedHost)
	// 0755, not 0750: moovd runs as a DIFFERENT, unprivileged user than the
	// operator running this CLI (the container is distroless non-root), and it
	// must be able to traverse this directory to serve the brand. Nothing here
	// is secret — every byte is published to anonymous callers by design — so
	// world-traversable is the correct permission rather than a lax one.
	// #nosec G301 -- see above: public assets read by a different service user.
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", hostDir, err)
	}

	// Start from what is already there, so unspecified flags are preserved.
	doc, err := readBrandingFile(hostDir)
	if err != nil {
		return err
	}

	changed := false

	if isFlagPassed(fs, "name") {
		doc.Name = strings.TrimSpace(*name)
		changed = true
	}
	if isFlagPassed(fs, "short-name") {
		v := strings.TrimSpace(*shortName)
		// Refused rather than truncated: the server WOULD cut it to twelve,
		// but an operator who typed "Corporate Mailbox" should learn now that
		// the home screen will say "Corporate Ma", not discover it on a phone.
		if n := len([]rune(v)); n > maxShortNameRunes {
			return usageErrorf("-short-name %q is %d characters; the limit is %d (launchers truncate past it)",
				v, n, maxShortNameRunes)
		}
		doc.ShortName = v
		changed = true
	}
	if isFlagPassed(fs, "tagline") {
		doc.Tagline = strings.TrimSpace(*tagline)
		changed = true
	}
	if isFlagPassed(fs, "support-url") {
		v := strings.TrimSpace(*supportURL)
		if v != "" && !isSafeSupportURL(v) {
			return usageErrorf("-support-url %q must start with https://, http:// or mailto:", v)
		}
		doc.SupportURL = v
		changed = true
	}
	for _, link := range []struct {
		flag  string
		value *string
		field *string
	}{
		{"privacy-url", privacyURL, &doc.PrivacyURL},
		{"terms-url", termsURL, &doc.TermsURL},
	} {
		if !isFlagPassed(fs, link.flag) {
			continue
		}
		v := strings.TrimSpace(*link.value)
		if v != "" && !isSafeSupportURL(v) {
			return usageErrorf("-%s %q must start with https://, http:// or mailto:", link.flag, v)
		}
		*link.field = v
		changed = true
	}

	colors := []struct {
		flag  string
		value *string
		field *string
	}{
		{"color-primary", colorPrimary, &doc.Colors.Primary},
		{"color-on-primary", colorOnPrimary, &doc.Colors.OnPrimary},
		{"color-splash-from", colorSplashFrom, &doc.Colors.SplashFrom},
		{"color-splash-to", colorSplashTo, &doc.Colors.SplashTo},
	}
	for _, c := range colors {
		if !isFlagPassed(fs, c.flag) {
			continue
		}
		v := strings.TrimSpace(*c.value)
		if v == "" {
			*c.field = ""
			changed = true
			continue
		}
		norm := normalizeHexColorCLI(v)
		if norm == "" {
			return usageErrorf("-%s %q is not a CSS hex color (#rgb or #rrggbb)", c.flag, v)
		}
		*c.field = norm
		changed = true
	}

	assets := []struct {
		flag  string
		src   *string
		dest  string
		field *string
	}{
		{"logo", logo, "logo", &doc.Logo},
		{"logo-dark", logoDark, "logo-dark", &doc.LogoDark},
		{"icon", icon, "icon", &doc.Icon},
		{"splash", splash, "splash", &doc.Splash},
	}
	for _, a := range assets {
		if !isFlagPassed(fs, a.flag) {
			continue
		}
		src := strings.TrimSpace(*a.src)
		if src == "" {
			// An explicit empty value removes the asset from the document. The
			// file itself is left on disk: deleting an operator's file as a
			// side effect of a config change would be a surprise.
			*a.field = ""
			changed = true
			continue
		}
		stored, body, err := copyBrandingAsset(src, hostDir, a.dest)
		if err != nil {
			return err
		}
		*a.field = stored
		changed = true
		outf(e.stdout, "Stored %s as %s.\n", a.flag, filepath.Join(hostDir, stored))
		// The icon, or the logo when there is no icon, is the source of
		// the installed app's icons, and not every image the login page can
		// show can be rendered into one (WebP in particular). Say so NOW, at
		// the terminal, rather than letting the operator find the wrong mark
		// on a customer's home screen.
		if a.flag == "logo" || a.flag == "icon" {
			if err := jmaphttp.ValidateBrandingIconSource(body); err != nil {
				outf(e.stdout, "  Note: the PWA icons will not be rendered from this %s — %s.\n", a.flag, err)
			} else if a.flag == "icon" {
				// A launcher shows a SQUARE. A wide image is contained inside
				// it with its aspect kept, so it ends up small with bands of
				// plate above and below — legible, but not what an operator
				// supplying an "icon" expects to see on a home screen.
				if w, h, ok := imageDimensionsForCLI(body); ok && !isRoughlySquare(w, h) {
					outf(e.stdout, "  Warning: the icon is %dx%d, which is not square; "+
						"launchers show a square, so it will be contained inside one "+
						"with bands of the primary color around it.\n", w, h)
				}
			}
		}
	}

	if !changed {
		return usageErrorf("branding set needs at least one field to change " +
			"(-name, -logo, -logo-dark, -icon, -splash, -color-primary, ...)")
	}

	if err := writeBrandingFile(hostDir, doc); err != nil {
		return err
	}

	outf(e.stdout, "Wrote branding for %s to %s.\n",
		resolvedHost, filepath.Join(hostDir, brandingFileName))
	// Say the operational truth that is not obvious from the success message.
	outf(e.stdout, "  The running server picks it up within a minute; no restart is needed.\n")
	return nil
}

// brandingShow prints one host's configuration.
func brandingShow(e *env, args []string) error {
	fs := flag.NewFlagSet("branding show", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	host := fs.String("host", "", "the hostname to show (required)")
	dir := fs.String("dir", "", "the branding root (default "+envBrandingDir+")")
	fs.Usage = func() {
		out(e.stderr, "Usage: moovctl branding show -host <hostname>\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 0 {
		return usageErrorf("branding show takes no positional arguments")
	}

	resolvedHost, err := requireBrandingHost(*host)
	if err != nil {
		return err
	}
	root, err := brandingRoot(*dir)
	if err != nil {
		return err
	}

	hostDir := filepath.Join(root, resolvedHost)
	// "No branding for this host" is a VALID state to report, not a failure:
	// the host is simply served Moov's defaults. The existence check is
	// therefore a boolean question, asked with a helper that returns one, so
	// the discarded error cannot be mistaken for a swallowed failure.
	if !brandingFileExists(hostDir) {
		outf(e.stdout, "%s has no branding configured; it is served Moov's defaults.\n", resolvedHost)
		return nil
	}

	doc, err := readBrandingFile(hostDir)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	outf(w, "HOST\t%s\n", resolvedHost)
	outf(w, "NAME\t%s\n", orDash(doc.Name))
	outf(w, "SHORT NAME\t%s\n", orDash(doc.ShortName))
	outf(w, "TAGLINE\t%s\n", orDash(doc.Tagline))
	outf(w, "SUPPORT URL\t%s\n", orDash(doc.SupportURL))
	outf(w, "PRIVACY URL\t%s\n", orDash(doc.PrivacyURL))
	outf(w, "TERMS URL\t%s\n", orDash(doc.TermsURL))
	outf(w, "LOGO\t%s\n", orDash(doc.Logo))
	outf(w, "LOGO DARK\t%s\n", orDash(doc.LogoDark))
	outf(w, "ICON\t%s\n", orDash(doc.Icon))
	outf(w, "SPLASH\t%s\n", orDash(doc.Splash))
	outf(w, "PWA ICONS\t%s\n", pwaIconsStatus(hostDir, doc.Icon, doc.Logo))
	outf(w, "PRIMARY\t%s\n", orDash(doc.Colors.Primary))
	outf(w, "ON PRIMARY\t%s\n", orDash(doc.Colors.OnPrimary))
	outf(w, "SPLASH FROM\t%s\n", orDash(doc.Colors.SplashFrom))
	outf(w, "SPLASH TO\t%s\n", orDash(doc.Colors.SplashTo))
	return w.Flush()
}

// pwaIconsStatus says where the installed app's icons will come from, with the
// reason whenever it is not the operator's first choice: the server logs the
// same verdict, but an operator running `show` should not have to read the
// daemon log to learn why a customer's phone shows the wrong mark.
//
// It walks the SAME chain the server does — the square icon, then the logo,
// then Moov's own — and it reads the FILES rather than the wire, because this
// is the tool for a host whose server may not even be running yet.
func pwaIconsStatus(hostDir, icon, logo string) string {
	iconOK, iconWhy := iconSourceVerdict(hostDir, icon)
	logoOK, logoWhy := iconSourceVerdict(hostDir, logo)

	switch {
	case iconOK:
		return "generated from the icon " + sanitizeAssetName(icon)
	case logoOK && iconWhy != "":
		return fmt.Sprintf("generated from the logo %s (the icon is not usable: %s)",
			sanitizeAssetName(logo), iconWhy)
	case logoOK:
		return "generated from the logo " + sanitizeAssetName(logo)
	case iconWhy == "" && logoWhy == "":
		return "Moov's (no icon and no logo configured)"
	case iconWhy == "":
		return fmt.Sprintf("Moov's (no icon configured, and the logo is not usable: %s)", logoWhy)
	case logoWhy == "":
		return fmt.Sprintf("Moov's (the icon is not usable: %s, and no logo is configured)", iconWhy)
	default:
		return fmt.Sprintf("Moov's (the icon is not usable: %s; the logo is not usable: %s)", iconWhy, logoWhy)
	}
}

// iconSourceVerdict judges one configured asset as a source for the rendered
// icons. An empty reason with ok=false means "not configured", which is not a
// problem to report.
func iconSourceVerdict(hostDir, configured string) (ok bool, why string) {
	name := sanitizeAssetName(configured)
	if name == "" {
		if strings.TrimSpace(configured) == "" {
			return false, ""
		}
		return false, fmt.Sprintf("%q is not a usable filename", configured)
	}
	body, err := os.ReadFile(filepath.Join(hostDir, name)) // #nosec G304 -- a validated single component under the host directory.
	if err != nil {
		return false, fmt.Sprintf("%s cannot be read: %v", name, err)
	}
	if err := jmaphttp.ValidateBrandingIconSource(body); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// maxShortNameRunes mirrors the server's cap on shortName; the CLI refuses
// past it instead of letting the server truncate silently.
const maxShortNameRunes = 12

// brandingList prints every configured host.
func brandingList(e *env, args []string) error {
	fs := flag.NewFlagSet("branding list", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	dir := fs.String("dir", "", "the branding root (default "+envBrandingDir+")")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 0 {
		return usageErrorf("branding list takes no positional arguments")
	}

	root, err := brandingRoot(*dir)
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			outf(e.stdout, "No branding is configured (%s does not exist).\n", root)
			return nil
		}
		return fmt.Errorf("reading %s: %w", root, err)
	}

	var hosts []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, entry.Name(), brandingFileName)); err != nil {
			continue
		}
		hosts = append(hosts, entry.Name())
	}
	if len(hosts) == 0 {
		outln(e.stdout, "No branding is configured; every host is served Moov's defaults.")
		return nil
	}
	sort.Strings(hosts)

	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	outln(w, "HOST\tNAME\tLOGO\tLOGO DARK\tICON\tSPLASH\tPRIMARY")
	for _, h := range hosts {
		doc, err := readBrandingFile(filepath.Join(root, h))
		if err != nil {
			outf(w, "%s\t(unreadable)\t-\t-\t-\t-\t-\n", h)
			continue
		}
		outf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			h, orDash(doc.Name), orDash(doc.Logo), orDash(doc.LogoDark), orDash(doc.Icon),
			orDash(doc.Splash), orDash(doc.Colors.Primary))
	}
	return w.Flush()
}

// brandingUnset removes one host's configuration, returning it to the Moov
// defaults.
func brandingUnset(e *env, args []string) error {
	fs := flag.NewFlagSet("branding unset", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	host := fs.String("host", "", "the hostname to reset (required)")
	dir := fs.String("dir", "", "the branding root (default "+envBrandingDir+")")
	keepAssets := fs.Bool("keep-assets", false,
		"leave the copied image files on disk (only branding.json is removed)")
	fs.Usage = func() {
		out(e.stderr, "Usage: moovctl branding unset -host <hostname> [-keep-assets]\n\n"+
			"Removes the host's branding so it is served Moov's defaults again.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 0 {
		return usageErrorf("branding unset takes no positional arguments")
	}

	resolvedHost, err := requireBrandingHost(*host)
	if err != nil {
		return err
	}
	root, err := brandingRoot(*dir)
	if err != nil {
		return err
	}

	hostDir := filepath.Join(root, resolvedHost)
	docPath := filepath.Join(hostDir, brandingFileName)
	// Unsetting a host that has no branding is a no-op worth reporting, not a
	// failure — see brandingFileExists on why this is a boolean question.
	if !brandingFileExists(hostDir) {
		outf(e.stdout, "%s has no branding configured.\n", resolvedHost)
		return nil
	}

	doc, err := readBrandingFile(hostDir)
	if err != nil {
		return err
	}
	if err := os.Remove(docPath); err != nil {
		return fmt.Errorf("removing %s: %w", docPath, err)
	}

	if !*keepAssets {
		// Only the files this CLI wrote, by their recorded names — never a
		// blanket wipe of the directory, which might hold something an
		// operator put there.
		for _, asset := range []string{doc.Logo, doc.LogoDark, doc.Icon, doc.Splash} {
			if name := sanitizeAssetName(asset); name != "" {
				_ = os.Remove(filepath.Join(hostDir, name))
			}
		}
		// Succeeds only if the directory is now empty, which is the intent.
		_ = os.Remove(hostDir)
	}

	outf(e.stdout, "Removed branding for %s; it is served Moov's defaults again.\n", resolvedHost)
	return nil
}

// brandingFileName is the per-host document's filename. It must match
// jmaphttp's brandingConfigFile — a test pins the two together, since the
// server does not export the constant and this CLI must not import a private
// one.
const brandingFileName = "branding.json"

// brandingDocument is the on-disk shape. It mirrors jmaphttp's brandingFile;
// the duplication is deliberate — the CLI writes files, the server reads them,
// and coupling them through an exported type would make the wire format of a
// config file part of the server's Go API.
type brandingDocument struct {
	Name       string           `json:"name,omitempty"`
	ShortName  string           `json:"shortName,omitempty"`
	Tagline    string           `json:"tagline,omitempty"`
	SupportURL string           `json:"supportUrl,omitempty"`
	PrivacyURL string           `json:"privacyUrl,omitempty"`
	TermsURL   string           `json:"termsUrl,omitempty"`
	Logo       string           `json:"logo,omitempty"`
	LogoDark   string           `json:"logoDark,omitempty"`
	Icon       string           `json:"icon,omitempty"`
	Splash     string           `json:"splash,omitempty"`
	Colors     brandingDocColor `json:"colors,omitempty"`
}

type brandingDocColor struct {
	Primary    string `json:"primary,omitempty"`
	OnPrimary  string `json:"onPrimary,omitempty"`
	SplashFrom string `json:"splashFrom,omitempty"`
	SplashTo   string `json:"splashTo,omitempty"`
}

// brandingFileExists reports whether a host has a branding document.
//
// It answers a BOOLEAN question, deliberately, rather than returning the
// os.Stat error. "This host has no branding" is a normal, reportable state —
// the host is served Moov's defaults — so a caller that returned nil after a
// non-nil error would read like a swallowed failure (and `nilerr` correctly
// flagged exactly that shape). Collapsing the question to a bool puts the
// judgement here, once, with the reason attached.
//
// A permission error is treated as "absent" for the same reason: the CLI can
// report nothing useful about a directory it cannot read, and any subsequent
// write fails loudly with the real cause.
func brandingFileExists(hostDir string) bool {
	info, err := os.Stat(filepath.Join(hostDir, brandingFileName))
	return err == nil && info.Mode().IsRegular()
}

// readBrandingFile loads a host's document, returning the zero value when the
// file does not exist yet (the first `set` for a host).
func readBrandingFile(hostDir string) (brandingDocument, error) {
	var doc brandingDocument
	raw, err := os.ReadFile(filepath.Join(hostDir, brandingFileName)) // #nosec G304 -- hostDir is built from a validated hostname under an operator-supplied root.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return doc, nil
		}
		return doc, fmt.Errorf("reading %s: %w", filepath.Join(hostDir, brandingFileName), err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return doc, fmt.Errorf("%s is not valid JSON: %w", filepath.Join(hostDir, brandingFileName), err)
	}
	return doc, nil
}

// writeBrandingFile writes the document atomically.
//
// Atomic because the server reads this file on a timer: a torn write would be
// read as malformed JSON and, per the server's fallback rule, would silently
// serve Moov's brand to a customer for as long as the tear lasted.
func writeBrandingFile(hostDir string, doc brandingDocument) error {
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the branding document: %w", err)
	}
	body = append(body, '\n')

	final := filepath.Join(hostDir, brandingFileName)
	tmp, err := os.CreateTemp(hostDir, brandingFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating a temporary file in %s: %w", hostDir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	// The server serves this to anonymous callers; it is world-readable by
	// design and holds nothing secret.
	// 0644 for the same reason the directory is 0755: the daemon reads this as
	// another user and its contents are served publicly.
	// #nosec G302 -- public, non-secret configuration read by the daemon.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("setting the mode of %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmpName, final, err)
	}
	return nil
}

// copyBrandingAsset validates an image and copies it beside the document,
// returning the stored filename and the bytes it stored (so the caller can
// judge them further without a second read).
//
// The stored name is OURS ("logo.png"), derived from the sniffed type — never
// the source filename. A customer's file called "../../etc/passwd.png" or one
// with a name in a script the filesystem renders oddly cannot become part of a
// URL that way.
func copyBrandingAsset(src, hostDir, base string) (string, []byte, error) {
	info, err := os.Stat(src)
	if err != nil {
		return "", nil, fmt.Errorf("reading %s: %w", src, err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not a regular file", src)
	}
	if info.Size() > jmaphttp.MaxBrandingAssetBytes {
		return "", nil, fmt.Errorf("%s is %d bytes; the limit is %d (%d MiB)",
			src, info.Size(), jmaphttp.MaxBrandingAssetBytes, jmaphttp.MaxBrandingAssetBytes>>20)
	}
	if info.Size() == 0 {
		return "", nil, fmt.Errorf("%s is empty", src)
	}

	// The extension check is for the operator's benefit — it names the real
	// problem ("SVG is not accepted") instead of the downstream symptom ("not
	// a supported image"). The content check below is the one that decides.
	ext := strings.ToLower(filepath.Ext(src))
	if ext == ".svg" || ext == ".svgz" {
		return "", nil, fmt.Errorf("%s: SVG is not accepted — it is an XML document that can carry "+
			"scripts, and this asset is served from the origin the login page runs on. "+
			"Export it to PNG", src)
	}
	if !containsFold(jmaphttp.AllowedBrandingExtensions, ext) {
		return "", nil, fmt.Errorf("%s: %q is not a supported image extension (want %s)",
			src, ext, strings.Join(jmaphttp.AllowedBrandingExtensions, ", "))
	}

	body, err := os.ReadFile(src) // #nosec G304 -- an operator-supplied path is the point of the flag; size was capped above.
	if err != nil {
		return "", nil, fmt.Errorf("reading %s: %w", src, err)
	}
	if len(body) > jmaphttp.MaxBrandingAssetBytes {
		return "", nil, fmt.Errorf("%s grew past the %d byte limit while being read",
			src, jmaphttp.MaxBrandingAssetBytes)
	}

	suffix, ok := imageExtensionForCLI(body)
	if !ok {
		return "", nil, fmt.Errorf("%s does not contain a PNG, JPEG, WebP or GIF image "+
			"(its bytes were checked, not its extension)", src)
	}

	stored := base + suffix
	dest := filepath.Join(hostDir, stored)

	// Remove a previously stored asset under a DIFFERENT extension, so
	// replacing logo.png with a logo.webp does not leave the old file behind
	// to be served by a stale document.
	for _, other := range jmaphttp.AllowedBrandingExtensions {
		if !strings.EqualFold(other, suffix) {
			_ = os.Remove(filepath.Join(hostDir, base+other))
		}
	}

	// #nosec G306 -- a brand image, served to anonymous callers by design and
	// read by the daemon under a different user; 0600 would break both.
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return "", nil, fmt.Errorf("writing %s: %w", dest, err)
	}
	return stored, body, nil
}

// imageExtensionForCLI sniffs an image and returns the extension to store it
// under. It mirrors jmaphttp.sniffImageType's format list; the CLI cannot call
// that unexported function, and a test pins the two lists together.
func imageExtensionForCLI(b []byte) (string, bool) {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return ".png", true
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return ".jpg", true
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return ".webp", true
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return ".gif", true
	}
	return "", false
}

// imageDimensionsForCLI reads an image's pixel dimensions from its header,
// for the squareness warning. It reads only the header (image.DecodeConfig),
// never the pixels; false means the format has no decoder registered here
// (WebP), in which case the operator already got the harder warning that the
// icons cannot be rendered from it at all.
func imageDimensionsForCLI(b []byte) (width, height int, ok bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// maxIconAspectDrift is how far from 1:1 an icon may be before the operator is
// warned. Ten per cent is enough to cover the odd off-by-a-pixel export and
// tight enough to catch a wordmark handed to -icon by mistake.
const maxIconAspectDrift = 0.10

// isRoughlySquare reports whether an image is close enough to 1:1 to fill a
// launcher's square without visible bands.
func isRoughlySquare(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	ratio := float64(width) / float64(height)
	return math.Abs(ratio-1) <= maxIconAspectDrift
}

// requireBrandingHost validates the -host flag.
func requireBrandingHost(raw string) (string, error) {
	h := strings.TrimSpace(raw)
	if h == "" {
		return "", usageErrorf("-host is required (the hostname the browser reaches Moov at)")
	}
	resolved := sanitizeBrandingHost(h)
	if resolved == "" {
		return "", usageErrorf("-host %q is not a plain hostname", raw)
	}
	return resolved, nil
}

// sanitizeBrandingHost normalizes a hostname to the directory name the server
// resolves. It intentionally applies the SAME rules as
// jmaphttp.resolveBrandingHost — a host this CLI accepts but the server
// rejects would produce a directory that is silently never served, so a test
// pins the two implementations against a shared table.
func sanitizeBrandingHost(raw string) string {
	h := strings.TrimSpace(raw)
	if h == "" {
		return ""
	}
	// A port is stripped rather than refused, exactly as the server strips it
	// from a Host header. An operator pasting the URL they reach Moov at
	// ("mail.example.com:8443") means the host, and refusing that would be a
	// confusing failure with no upside — the two sides would also disagree,
	// which is what the pin test caught.
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.ToLower(h)
	h = strings.TrimSuffix(h, ".")
	if h == "" || strings.ContainsAny(h, `/\[]%:`) {
		return ""
	}
	if strings.Contains(h, "..") || h == "." || h == ".." {
		return ""
	}
	for _, c := range h {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.':
		default:
			return ""
		}
	}
	if strings.HasPrefix(h, ".") || strings.HasPrefix(h, "-") {
		return ""
	}
	return h
}

// sanitizeAssetName reduces a recorded asset name to a safe single component,
// used when unset removes the files it wrote.
func sanitizeAssetName(name string) string {
	n := strings.TrimSpace(name)
	if n == "" || strings.ContainsAny(n, `/\`) || strings.Contains(n, "..") ||
		strings.HasPrefix(n, ".") || n != filepath.Base(n) {
		return ""
	}
	return n
}

// normalizeHexColorCLI validates a CSS hex color, mirroring the server's
// rule (only #rgb and #rrggbb).
func normalizeHexColorCLI(s string) string {
	c := strings.TrimSpace(s)
	if len(c) != 4 && len(c) != 7 {
		return ""
	}
	if c[0] != '#' {
		return ""
	}
	for _, ch := range c[1:] {
		switch {
		case ch >= '0' && ch <= '9', ch >= 'a' && ch <= 'f', ch >= 'A' && ch <= 'F':
		default:
			return ""
		}
	}
	return strings.ToLower(c)
}

// isSafeSupportURL mirrors the server's scheme allow-list.
func isSafeSupportURL(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "https://") ||
		strings.HasPrefix(l, "http://") ||
		strings.HasPrefix(l, "mailto:")
}

// brandingRoot resolves the branding directory: the flag, then the
// environment, then the deployment default.
func brandingRoot(flagValue string) (string, error) {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(os.Getenv(envBrandingDir)); v != "" {
		return v, nil
	}
	return defaultBrandingDir, nil
}

// isFlagPassed reports whether a flag appeared on the command line, which is
// what makes `set` incremental rather than destructive.
func isFlagPassed(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
