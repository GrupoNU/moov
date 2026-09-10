package branding

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Dir is one host's branding directory: <root>/<host>. It is the ONE writer
// of a brand — `moovctl branding` and the admin API both go through it, and a
// test in cmd/moovctl runs the same scenario through both and diffs the
// directories.
//
// # What every write guarantees
//
//   - The document is written ATOMICALLY (temp file in the same directory,
//     then rename). The server reads it on a timer, and a torn write would be
//     read as malformed JSON — which, per the server's fallback rule, would
//     silently serve Moov's brand to a customer for as long as the tear lasted.
//   - Assets are written the same way, so a reader never sees half a PNG.
//   - Directories are 0755 and files 0644. Not laxity: moovd runs distroless
//     as a DIFFERENT, unprivileged user than the operator running the CLI and
//     must traverse and read these, and every byte here is published to
//     anonymous callers by design. There is nothing secret to protect.
type Dir struct {
	path string
}

// HostDir resolves the directory for a host under a root. The host must
// already be normalized (NormalizeHost); anything else is refused so a
// caller cannot reach outside the root by accident.
func HostDir(root, host string) (Dir, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return Dir{}, errors.New("branding: no root directory")
	}
	if host == "" || NormalizeHost(host) != host {
		return Dir{}, fmt.Errorf("branding: %q is not a normalized hostname", host)
	}
	return Dir{path: filepath.Join(root, host)}, nil
}

// DirAt wraps an already-resolved host directory path. For callers — the CLI
// mostly — that built the path themselves from a validated host.
func DirAt(path string) Dir { return Dir{path: path} }

// Path is the directory's filesystem path.
func (d Dir) Path() string { return d.path }

// ConfigPath is the document's filesystem path.
func (d Dir) ConfigPath() string { return filepath.Join(d.path, ConfigFile) }

// Exists reports whether the host has a document.
//
// It answers a BOOLEAN question, deliberately, rather than returning the
// os.Stat error. "This host has no branding" is a normal, reportable state —
// the host is served Moov's defaults — and a permission error is treated as
// "absent" for the same reason: nothing useful can be said about a directory
// that cannot be read, and any subsequent write fails loudly with the cause.
func (d Dir) Exists() bool {
	info, err := os.Stat(d.ConfigPath())
	return err == nil && info.Mode().IsRegular()
}

// Read loads the document, returning the zero value when there is none yet
// (the first write for a host).
func (d Dir) Read() (File, error) {
	var f File
	raw, err := os.ReadFile(d.ConfigPath()) // #nosec G304 -- a validated host directory under an operator-supplied root.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return f, nil
		}
		return f, fmt.Errorf("reading %s: %w", d.ConfigPath(), err)
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("%s is not valid JSON: %w", d.ConfigPath(), err)
	}
	return f, nil
}

// Write stores the document atomically, creating the directory if needed.
// It returns the bytes written, so a caller that audits can hash them.
func (d Dir) Write(f File) ([]byte, error) {
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding the branding document: %w", err)
	}
	body = append(body, '\n')
	if err := d.mkdir(); err != nil {
		return nil, err
	}
	if err := writeAtomic(d.path, ConfigFile, body); err != nil {
		return nil, err
	}
	return body, nil
}

// StoreAsset validates image bytes and stores them under the kind's name
// ("logo.png", "logo-dark.jpg", ...), returning the stored filename.
//
// The extension comes from the SNIFFED type, never from any source filename.
// A previously stored asset of the same kind under a different extension is
// removed, so replacing logo.png with a logo.webp does not leave the old file
// behind for a stale document to serve. The document is NOT touched: the
// caller records the returned name and writes it.
func (d Dir) StoreAsset(kind AssetKind, body []byte) (string, error) {
	if kind.Field(&File{}) == nil {
		return "", fmt.Errorf("branding: unknown asset kind %q", kind)
	}
	if len(body) == 0 {
		return "", ErrEmptyAsset
	}
	if len(body) > MaxAssetBytes {
		return "", ErrAssetTooLarge
	}
	contentType, ok := SniffImageType(body)
	if !ok {
		if LooksLikeSVG(body) {
			return "", ErrSVG
		}
		return "", ErrNotImage
	}
	suffix, _ := ExtensionFor(contentType)
	stored := kind.BaseName() + suffix

	if err := d.mkdir(); err != nil {
		return "", err
	}
	if err := writeAtomic(d.path, stored, body); err != nil {
		return "", err
	}
	// Only AFTER the new file is in place: a write that failed must leave the
	// previous asset — whatever its extension — exactly where it was.
	for _, other := range AllowedExtensions {
		if !strings.EqualFold(other, suffix) {
			_ = os.Remove(filepath.Join(d.path, kind.BaseName()+other))
		}
	}
	return stored, nil
}

// RemoveAsset deletes one stored file by its recorded name. A missing file is
// not an error (the document said it was there; it is gone either way). A
// name that is not a safe single component is ignored rather than resolved.
func (d Dir) RemoveAsset(name string) error {
	clean := SafeAssetName(name)
	if clean == "" {
		return nil
	}
	err := os.Remove(filepath.Join(d.path, clean))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", clean, err)
	}
	return nil
}

// Unset removes the document and, unless keepAssets, the files it recorded —
// only those, by their recorded names, never a blanket wipe of a directory
// that might hold something an operator put there. The directory itself is
// removed only if that left it empty. Nothing is preserved: this is the
// operator's full reset, `moovctl branding unset`.
func (d Dir) Unset(keepAssets bool) error {
	f, err := d.Read()
	if err != nil {
		return err
	}
	if err := os.Remove(d.ConfigPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", d.ConfigPath(), err)
	}
	if !keepAssets {
		d.removeRecordedAssets(f)
		_ = os.Remove(d.path)
	}
	return nil
}

// Reset returns the host to Moov's brand while PRESERVING the admin list:
// every visible field is cleared and every recorded asset removed, and the
// document is rewritten with only BrandAdmins in it — so an admin who resets
// the brand from the panel keeps the access to build it again. It returns
// the document as written.
func (d Dir) Reset() (File, []byte, error) {
	f, err := d.Read()
	if err != nil {
		return File{}, nil, err
	}
	d.removeRecordedAssets(f)
	kept := File{BrandAdmins: f.BrandAdmins}
	body, err := d.Write(kept)
	return kept, body, err
}

func (d Dir) removeRecordedAssets(f File) {
	for _, asset := range []string{f.Logo, f.LogoDark, f.Icon, f.Splash} {
		_ = d.RemoveAsset(asset)
	}
}

func (d Dir) mkdir() error {
	// #nosec G301 -- public assets read by a different service user; see the
	// type comment.
	if err := os.MkdirAll(d.path, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", d.path, err)
	}
	return nil
}

// writeAtomic writes body to <dir>/<name> through a temp file and a rename,
// leaving the previous file intact if anything before the rename fails.
func writeAtomic(dir, name string, body []byte) (err error) {
	final := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, name+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating a temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// A no-op once the rename succeeds; the cleanup on every failure path.
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, werr := tmp.Write(body); werr != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, werr)
	}
	if werr := writeFailureHook(tmpName); werr != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("closing %s: %w", tmpName, cerr)
	}
	// 0644 for the same reason the directory is 0755: the daemon reads this
	// as another user and its contents are served publicly.
	// #nosec G302 -- public, non-secret files read by the daemon.
	if cerr := os.Chmod(tmpName, 0o644); cerr != nil {
		return fmt.Errorf("setting the mode of %s: %w", tmpName, cerr)
	}
	if rerr := os.Rename(tmpName, final); rerr != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmpName, final, rerr)
	}
	return nil
}

// writeFailureHook is a seam for the atomicity test: it is called with the
// temp file's path after the body is written and before the rename, and a
// non-nil return aborts the write. Production never sets it. It exists
// because the natural way to provoke a failed write — a read-only directory
// — does not fail for root, which is what the CI container runs as.
var writeFailureHook = func(string) error { return nil }

// The typed refusals of StoreAsset, so an HTTP caller can map each to a
// status and a CLI caller to a sentence, without parsing messages.
var (
	// ErrEmptyAsset: zero bytes.
	ErrEmptyAsset = errors.New("the image is empty")
	// ErrAssetTooLarge: over MaxAssetBytes.
	ErrAssetTooLarge = fmt.Errorf("the image is larger than %d bytes (%d MiB)", MaxAssetBytes, MaxAssetBytes>>20)
	// ErrSVG: recognizably SVG/XML, which is refused by policy.
	ErrSVG = errors.New("SVG is not accepted (its bytes were checked, not its extension): it is an XML " +
		"document that can carry scripts, and this asset is served from the origin the login page " +
		"runs on. Export it to PNG")
	// ErrNotImage: not a PNG, JPEG, WebP or GIF by its bytes.
	ErrNotImage = errors.New("the file does not contain a PNG, JPEG, WebP or GIF image " +
		"(its bytes were checked, not its extension)")
	// ErrInvalidMailbox: a grant or revoke with something that is not a
	// mailbox address.
	ErrInvalidMailbox = errors.New("not a mailbox address (want local@domain)")
)
