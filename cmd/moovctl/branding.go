package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/GrupoNU/moov/internal/branding"
)

// `moovctl branding` writes the per-host branding directories that the public
// GET /branding endpoint serves (arbitration W-A1 of L2-pwa §3), and grants
// the mailboxes that may edit them through the authenticated admin API
// (L2-brand-admin, BA-1).
//
// # Why a CLI and an API
//
// The CLI is the OPERATOR's tool: it runs with shell access on the server,
// which is the right blast radius for granting who may change what renders on
// a login page. The API is the domain administrator's tool: it can only be
// reached by a mailbox the operator granted here. Both write through ONE
// implementation, internal/branding, so a directory the CLI produced is one
// the API can edit and vice versa — a test runs the same scenario through both
// and diffs the results.
//
// # The layout it writes
//
//	<dir>/<host>/branding.json     the document (name, colors, asset names, admins)
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
		return usageErrorf("branding needs a subcommand (set, show, list, unset, grant, revoke)")
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
	case "grant":
		return brandingGrant(e, args[1:], true)
	case "revoke":
		return brandingGrant(e, args[1:], false)
	default:
		return usageErrorf("unknown branding subcommand %q (want set, show, list, unset, grant or revoke)", args[0])
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

	hostDir, resolvedHost, err := brandingHostDir(*host, *dir)
	if err != nil {
		return err
	}

	// Start from what is already there, so unspecified flags are preserved.
	doc, err := hostDir.Read()
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
		if n := branding.RuneLen(v); n > maxShortNameRunes {
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
	for _, link := range []struct {
		flag  string
		value *string
		field *string
	}{
		{"support-url", supportURL, &doc.SupportURL},
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
		flag string
		src  *string
		kind branding.AssetKind
	}{
		{"logo", logo, branding.AssetLogo},
		{"logo-dark", logoDark, branding.AssetLogoDark},
		{"icon", icon, branding.AssetIcon},
		{"splash", splash, branding.AssetSplash},
	}
	for _, a := range assets {
		if !isFlagPassed(fs, a.flag) {
			continue
		}
		field := a.kind.Field(&doc)
		src := strings.TrimSpace(*a.src)
		if src == "" {
			// An explicit empty value removes the asset from the document. The
			// file itself is left on disk: deleting an operator's file as a
			// side effect of a config change would be a surprise.
			*field = ""
			changed = true
			continue
		}
		stored, body, err := copyBrandingAsset(src, hostDir, a.kind)
		if err != nil {
			return err
		}
		*field = stored
		changed = true
		outf(e.stdout, "Stored %s as %s.\n", a.flag, filepath.Join(hostDir.Path(), stored))
		// The icon, or the logo when there is no icon, is the source of the
		// installed app's icons, and not every image the login page can show
		// can be rendered into one (WebP in particular). Say so NOW, at the
		// terminal, rather than letting the operator find the wrong mark on a
		// customer's home screen. The sentences are the writer's own, so the
		// admin API's `warnings` say exactly the same thing.
		for _, note := range branding.IconSourceNotes(a.kind, body) {
			if strings.Contains(note, "not square") {
				outf(e.stdout, "  Warning: %s.\n", note)
			} else {
				outf(e.stdout, "  Note: %s.\n", note)
			}
		}
	}

	if !changed {
		return usageErrorf("branding set needs at least one field to change " +
			"(-name, -logo, -logo-dark, -icon, -splash, -color-primary, ...)")
	}

	if _, err := hostDir.Write(doc); err != nil {
		return err
	}

	outf(e.stdout, "Wrote branding for %s to %s.\n", resolvedHost, hostDir.ConfigPath())
	// Say the operational truth that is not obvious from the success message.
	outf(e.stdout, "  The running server picks it up within a minute; no restart is needed.\n")
	return nil
}

// brandingGrant adds (grant=true) or removes (grant=false) a mailbox from the
// host's brand admins — the mailboxes allowed to edit the brand from
// Settings in the webmail. A grant on a host with no branding yet creates the
// document with only the admin list in it; the host keeps serving Moov's
// brand until someone configures one.
func brandingGrant(e *env, args []string, grant bool) error {
	verb := "revoke"
	if grant {
		verb = "grant"
	}
	fs := flag.NewFlagSet("branding "+verb, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	host := fs.String("host", "", "the hostname whose brand the user may edit (required)")
	user := fs.String("user", "", "the mailbox address (required, e.g. ana@acme.example)")
	dir := fs.String("dir", "", "the branding root (default "+envBrandingDir+")")
	fs.Usage = func() {
		if grant {
			out(e.stderr, "Usage: moovctl branding grant -host <hostname> -user <mailbox>\n\n"+
				"Lets the mailbox edit the host's brand from Settings in the webmail\n"+
				"(the authenticated /branding/admin API). The mailbox must be able to log\n"+
				"in to Moov on that host; this command only records the permission.\n\n")
		} else {
			out(e.stderr, "Usage: moovctl branding revoke -host <hostname> -user <mailbox>\n\n"+
				"Removes the mailbox from the host's brand admins. Takes effect on the\n"+
				"server within a minute; no restart is needed.\n\n")
		}
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 0 {
		return usageErrorf("branding %s takes no positional arguments", verb)
	}
	mailbox, ok := branding.NormalizeMailbox(*user)
	if !ok {
		return usageErrorf("-user %q is not a mailbox address (want local@domain)", *user)
	}
	hostDir, resolvedHost, err := brandingHostDir(*host, *dir)
	if err != nil {
		return err
	}
	doc, err := hostDir.Read()
	if err != nil {
		return err
	}

	if grant {
		changed, err := doc.Grant(mailbox)
		if err != nil {
			return err
		}
		if !changed {
			outf(e.stdout, "%s is already a brand admin of %s.\n", mailbox, resolvedHost)
			return nil
		}
		if _, err := hostDir.Write(doc); err != nil {
			return err
		}
		outf(e.stdout, "Granted %s brand administration of %s.\n", mailbox, resolvedHost)
		outf(e.stdout, "  The running server honors it within a minute; no restart is needed.\n")
		return nil
	}

	found, err := doc.Revoke(mailbox)
	if err != nil {
		return err
	}
	if !found {
		outf(e.stdout, "%s is not a brand admin of %s.\n", mailbox, resolvedHost)
		return nil
	}
	if _, err := hostDir.Write(doc); err != nil {
		return err
	}
	outf(e.stdout, "Revoked %s's brand administration of %s.\n", mailbox, resolvedHost)
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

	hostDir, resolvedHost, err := brandingHostDir(*host, *dir)
	if err != nil {
		return err
	}
	// "No branding for this host" is a VALID state to report, not a failure:
	// the host is simply served Moov's defaults.
	if !hostDir.Exists() {
		outf(e.stdout, "%s has no branding configured; it is served Moov's defaults.\n", resolvedHost)
		return nil
	}

	doc, err := hostDir.Read()
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
	outf(w, "PWA ICONS\t%s\n", pwaIconsStatus(hostDir.Path(), doc.Icon, doc.Logo))
	outf(w, "PRIMARY\t%s\n", orDash(doc.Colors.Primary))
	outf(w, "ON PRIMARY\t%s\n", orDash(doc.Colors.OnPrimary))
	outf(w, "SPLASH FROM\t%s\n", orDash(doc.Colors.SplashFrom))
	outf(w, "SPLASH TO\t%s\n", orDash(doc.Colors.SplashTo))
	outf(w, "BRAND ADMINS\t%s\n", orDash(strings.Join(doc.BrandAdmins, ", ")))
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
	if err := branding.ValidateIconSource(body); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// maxShortNameRunes is the server's cap on shortName; the CLI refuses past it
// instead of letting the server truncate silently.
const maxShortNameRunes = branding.MaxShortNameRunes

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
		if branding.DirAt(filepath.Join(root, entry.Name())).Exists() {
			hosts = append(hosts, entry.Name())
		}
	}
	if len(hosts) == 0 {
		outln(e.stdout, "No branding is configured; every host is served Moov's defaults.")
		return nil
	}
	sort.Strings(hosts)

	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	outln(w, "HOST\tNAME\tLOGO\tLOGO DARK\tICON\tSPLASH\tPRIMARY\tBRAND ADMINS")
	for _, h := range hosts {
		doc, err := branding.DirAt(filepath.Join(root, h)).Read()
		if err != nil {
			outf(w, "%s\t(unreadable)\t-\t-\t-\t-\t-\t-\n", h)
			continue
		}
		outf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			h, orDash(doc.Name), orDash(doc.Logo), orDash(doc.LogoDark), orDash(doc.Icon),
			orDash(doc.Splash), orDash(doc.Colors.Primary), orDash(strings.Join(doc.BrandAdmins, ", ")))
	}
	return w.Flush()
}

// brandingUnset removes one host's configuration, returning it to the Moov
// defaults. Everything goes, the admin list included: this is the operator's
// full reset. (The panel's own "reset" keeps the admins so its user keeps
// access; that one is branding.Dir.Reset.)
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

	hostDir, resolvedHost, err := brandingHostDir(*host, *dir)
	if err != nil {
		return err
	}
	// Unsetting a host that has no branding is a no-op worth reporting, not a
	// failure.
	if !hostDir.Exists() {
		outf(e.stdout, "%s has no branding configured.\n", resolvedHost)
		return nil
	}
	if err := hostDir.Unset(*keepAssets); err != nil {
		return err
	}
	outf(e.stdout, "Removed branding for %s; it is served Moov's defaults again.\n", resolvedHost)
	return nil
}

// brandingFileName is the per-host document's filename, owned by the shared
// writer so the CLI and the server cannot disagree about it.
const brandingFileName = branding.ConfigFile

// brandingDocument is the on-disk shape, owned by internal/branding: the CLI
// writes files, the server reads them, and both name the SAME type now that
// the API writes them too.
type brandingDocument = branding.File

// brandingHostDir validates the -host flag, resolves the root and returns
// the host's directory and normalized name.
func brandingHostDir(hostFlag, dirFlag string) (branding.Dir, string, error) {
	resolvedHost, err := requireBrandingHost(hostFlag)
	if err != nil {
		return branding.Dir{}, "", err
	}
	root, err := brandingRoot(dirFlag)
	if err != nil {
		return branding.Dir{}, "", err
	}
	d, err := branding.HostDir(root, resolvedHost)
	if err != nil {
		return branding.Dir{}, "", err
	}
	return d, resolvedHost, nil
}

// writeBrandingFile writes the document atomically through the shared writer.
func writeBrandingFile(hostDir string, doc brandingDocument) error {
	_, err := branding.DirAt(hostDir).Write(doc)
	return err
}

// copyBrandingAsset validates an image FILE and stores it beside the document
// under the kind's own name, returning the stored filename and the bytes (so
// the caller can judge them further without a second read).
//
// The path and extension checks are the CLI's: they name the operator's real
// problem ("SVG is not accepted") at the terminal. The content check and the
// write are the shared writer's, exactly as the API does them.
func copyBrandingAsset(src string, hostDir branding.Dir, kind branding.AssetKind) (string, []byte, error) {
	info, err := os.Stat(src)
	if err != nil {
		return "", nil, fmt.Errorf("reading %s: %w", src, err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not a regular file", src)
	}
	if info.Size() > branding.MaxAssetBytes {
		return "", nil, fmt.Errorf("%s is %d bytes; the limit is %d (%d MiB)",
			src, info.Size(), branding.MaxAssetBytes, branding.MaxAssetBytes>>20)
	}
	if info.Size() == 0 {
		return "", nil, fmt.Errorf("%s is empty", src)
	}

	ext := strings.ToLower(filepath.Ext(src))
	if ext == ".svg" || ext == ".svgz" {
		return "", nil, fmt.Errorf("%s: %w", src, branding.ErrSVG)
	}
	if !containsFold(branding.AllowedExtensions, ext) {
		return "", nil, fmt.Errorf("%s: %q is not a supported image extension (want %s)",
			src, ext, strings.Join(branding.AllowedExtensions, ", "))
	}

	body, err := os.ReadFile(src) // #nosec G304 -- an operator-supplied path is the point of the flag; size was capped above.
	if err != nil {
		return "", nil, fmt.Errorf("reading %s: %w", src, err)
	}
	stored, err := hostDir.StoreAsset(kind, body)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", src, err)
	}
	return stored, body, nil
}

// imageExtensionForCLI sniffs an image and returns the extension it is stored
// under — the shared writer's rule, exposed for the pin test.
func imageExtensionForCLI(b []byte) (string, bool) {
	ct, ok := branding.SniffImageType(b)
	if !ok {
		return "", false
	}
	return branding.ExtensionFor(ct)
}

// imageDimensionsForCLI reads an image's pixel dimensions from its header.
func imageDimensionsForCLI(b []byte) (width, height int, ok bool) {
	return branding.ImageDimensions(b)
}

// isRoughlySquare reports whether an image is close enough to 1:1 to fill a
// launcher's square without visible bands.
func isRoughlySquare(width, height int) bool { return branding.IsRoughlySquare(width, height) }

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
// resolves — the SAME rule, because it is the same function.
func sanitizeBrandingHost(raw string) string { return branding.NormalizeHost(raw) }

// sanitizeAssetName reduces a recorded asset name to a safe single component.
func sanitizeAssetName(name string) string { return branding.SafeAssetName(name) }

// normalizeHexColorCLI validates a CSS hex color (only #rgb and #rrggbb).
func normalizeHexColorCLI(s string) string { return branding.NormalizeHexColor(s) }

// isSafeSupportURL is the href scheme allow-list.
func isSafeSupportURL(u string) bool { return branding.SafeURL(u) }

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
