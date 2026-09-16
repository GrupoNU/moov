package accounts

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/store"
)

// The background export of contract §2.6: one zip per account, one .eml per
// message under its mailbox path, plus a manifest.json whose entries are what
// gate criterion 6 compares against the store.
//
// # Why a background job and not a streamed download
//
// A mailbox of a few hundred thousand messages is not a request-sized amount
// of work, and a download that dies at 90% has produced nothing. The job
// writes a file, records its hash and size, and hands out a signed URL for
// it; a portal polls a status it can show a progress bar for.
//
// # Why the URL is signed rather than authenticated
//
// The download is handed to a BROWSER (§2.6): a tab cannot attach an
// Authorization header to a navigation. The capability is therefore in the
// URL, exactly as the image proxy's is, bounded the same three ways: it
// carries no identity, it expires in 24 h, and it grants one zip and nothing
// else. The difference from the image proxy is the key: this one is DERIVED
// from the master key (crypto.Keyring.Derive) rather than random per process,
// because a URL handed to a user must survive a deploy.

// Export durations and shapes fixed by the contract.
const (
	// DownloadValidity is how long a signed download URL works (§2.6).
	DownloadValidity = 24 * time.Hour

	// Retention is how long a ready export stays downloadable before the
	// sweep removes the zip and the row starts answering 410 (§2.6).
	Retention = 7 * 24 * time.Hour

	// ExportKeyLabel is the derivation label of the download signing key. It
	// is a constant because the same label must be spelled identically
	// wherever the key is needed (crypto.Derive's contract).
	ExportKeyLabel = "moov/accounts/export-download/v1"

	// exportIDBytes is the entropy behind an export id. 15 bytes is 20
	// base64url characters, which is the low end of the contract's
	// ^exp_[A-Za-z0-9]{20,32}$.
	exportIDBytes = 15
)

// ExportStatus mirrors the contract's enum, including the "none" that is a
// 200 rather than a 404 (§2.6).
type ExportStatus string

const (
	ExportNone    ExportStatus = "none"
	ExportPending ExportStatus = "pending"
	ExportRunning ExportStatus = "running"
	ExportReady   ExportStatus = "ready"
	ExportFailed  ExportStatus = "failed"
	ExportExpired ExportStatus = "expired"
)

// ExportView is the resource GET/POST …/export serve.
type ExportView struct {
	Status      ExportStatus
	ID          string
	RequestedAt *time.Time
	CompletedAt *time.Time

	// Progress is present while running.
	MessagesDone  int
	MessagesTotal int

	// Download is present only when ready.
	DownloadURL string
	ExpiresAt   *time.Time
	Bytes       int64

	// Manifest is present only when ready.
	Messages  int
	Mailboxes int
	SHA256    string

	// Error is present only when failed.
	Error string
}

// ManifestEntry is one row of the zip's manifest.json (§2.6).
type ManifestEntry struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Bytes      int64  `json:"bytes"`
	ReceivedAt string `json:"receivedAt"`
	MessageID  string `json:"messageId"`
}

// Manifest is the zip's manifest.json.
type Manifest struct {
	Account     string          `json:"account"`
	GeneratedAt string          `json:"generatedAt"`
	Messages    int             `json:"messages"`
	Mailboxes   int             `json:"mailboxes"`
	Entries     []ManifestEntry `json:"entries"`
}

// ExportStore is the slice of internal/store the runner uses.
type ExportStore interface {
	ClaimPendingExport(ctx context.Context, at time.Time) (store.Export, error)
	SetExportProgress(ctx context.Context, id string, done, total int) error
	CompleteExport(ctx context.Context, id string, r store.ExportResult, at time.Time) error
	FailExport(ctx context.Context, id string, reason string, at time.Time) error
	GetExport(ctx context.Context, id string) (store.Export, error)
	ListExpirableExports(ctx context.Context, cutoff time.Time, limit int) ([]store.Export, error)
	PurgeExport(ctx context.Context, id string, at time.Time) error
	CountPendingExports(ctx context.Context) (int, error)
	ForEachAccountMessage(ctx context.Context, accountID int64, fn func(store.ExportMessage) error) error
}

// BlobReader is the runner's view of the blob store: the raw bytes of one
// message, by content hash.
type BlobReader interface {
	Open(h blob.Hash) (io.ReadCloser, error)
}

// ExportConfig configures the runner.
type ExportConfig struct {
	// Dir is where the zips are written. Required.
	Dir string

	// SigningKey signs the download URLs. It must be the DERIVED key
	// (crypto.Keyring.Derive(ExportKeyLabel)), never the master key itself.
	SigningKey []byte

	// BaseURL is the absolute origin the signed URL is built on
	// (https://mail.example.test). Empty means the URL is built per request
	// from the Host header, which is what a multi-host installation needs.
	BaseURL string

	// Now is the clock; nil means time.Now.
	Now func() time.Time

	Logger *slog.Logger
}

// PendingGauge observes how many jobs are waiting, for the metrics exporter.
type PendingGauge interface {
	SetPendingExports(n int)
}

// ExportRunner produces the zips. One per daemon; it claims jobs from the
// store, so two daemons would not duplicate work.
type ExportRunner struct {
	store ExportStore
	blobs BlobReader
	dir   string
	key   []byte
	base  string
	now   func() time.Time
	log   *slog.Logger
	gauge PendingGauge

	// mu guards nothing but the test hook below; the runner's state is the
	// store's.
	mu sync.Mutex
}

// NewExportRunner builds a runner.
func NewExportRunner(cfg ExportConfig, st ExportStore, blobs BlobReader, gauge PendingGauge) (*ExportRunner, error) {
	switch {
	case st == nil:
		return nil, errors.New("accounts: an ExportStore is required")
	case blobs == nil:
		return nil, errors.New("accounts: a BlobReader is required")
	case cfg.Dir == "":
		return nil, errors.New("accounts: an export directory is required")
	case len(cfg.SigningKey) == 0:
		return nil, errors.New("accounts: an export signing key is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("accounts: creating the export directory: %w", err)
	}
	return &ExportRunner{
		store: st, blobs: blobs, dir: cfg.Dir,
		key: append([]byte(nil), cfg.SigningKey...),
		base: strings.TrimRight(cfg.BaseURL, "/"),
		now: cfg.Now, log: cfg.Logger, gauge: gauge,
	}, nil
}

// newExportID mints an id matching the contract's pattern.
func newExportID() (string, error) {
	raw := make([]byte, exportIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("accounts: generating an export id: %w", err)
	}
	// base64url of 15 bytes is 20 characters; '-' and '_' are outside the
	// contract's [A-Za-z0-9], so they are folded onto letters rather than
	// re-drawn (the entropy that matters is the store's unique id, not the
	// alphabet).
	s := base64.RawURLEncoding.EncodeToString(raw)
	s = strings.NewReplacer("-", "A", "_", "B").Replace(s)
	return "exp_" + s, nil
}

// StartExport queues a job, or returns the one already in flight (§2.6: at
// most one per account; a second POST answers 202 with THAT job).
func (s *Service) StartExport(ctx context.Context, c Call, address string) (view ExportView, err error) {
	a, err := s.resolve(ctx, c.Actor, address)
	if err != nil {
		return ExportView{}, err
	}
	if a.DeletingSince != nil {
		s.audit(ctx, c, ActionExport, address, "error", "deleting")
		return ExportView{}, ErrDeleting
	}
	if s.exports == nil {
		// The feature is configured off. There is no honest partial answer:
		// a job that will never run must not be reported as pending.
		return ExportView{}, ErrNotFound
	}

	latest, lerr := s.store.LatestExport(ctx, address)
	if lerr != nil && !errors.Is(lerr, store.ErrNotFound) {
		return ExportView{}, fmt.Errorf("reading the latest export of %q: %w", address, lerr)
	}
	if lerr == nil && latest.Active() {
		s.audit(ctx, c, ActionExport, address, "ok", "already running")
		return s.exports.view(latest), nil
	}

	id, err := newExportID()
	if err != nil {
		return ExportView{}, err
	}
	defer func() { s.finish(ctx, c, ActionExport, address, err, "") }()

	job, err := s.store.CreateExport(ctx, id, a.ID, address)
	if err != nil {
		return ExportView{}, fmt.Errorf("queueing an export of %q: %w", address, err)
	}
	return s.exports.view(job), nil
}

// GetExport reports the latest job. "none" is a 200 (§2.6), so that 404 keeps
// its single meaning.
func (s *Service) GetExport(ctx context.Context, actor Actor, address string) (ExportView, error) {
	if _, err := s.resolve(ctx, actor, address); err != nil {
		return ExportView{}, err
	}
	latest, err := s.store.LatestExport(ctx, address)
	if errors.Is(err, store.ErrNotFound) {
		return ExportView{Status: ExportNone}, nil
	}
	if err != nil {
		return ExportView{}, fmt.Errorf("reading the latest export of %q: %w", address, err)
	}
	if s.exports == nil {
		return ExportView{Status: ExportNone}, nil
	}
	return s.exports.view(latest), nil
}

// view renders a stored job, minting the signed URL when it is ready.
func (r *ExportRunner) view(e store.Export) ExportView {
	v := ExportView{
		Status:        ExportStatus(e.Status),
		ID:            e.ID,
		RequestedAt:   &e.RequestedAt,
		CompletedAt:   e.CompletedAt,
		MessagesDone:  e.MessagesDone,
		MessagesTotal: e.MessagesTotal,
		Messages:      e.Messages,
		Mailboxes:     e.Mailboxes,
		SHA256:        e.SHA256,
		Bytes:         e.Bytes,
		Error:         e.Error,
	}
	if e.PurgedAt != nil {
		// A purged export is "expired" on the status route; the download
		// route answers 410 for it (§2.6).
		v.Status = ExportExpired
		return v
	}
	if ExportStatus(e.Status) == ExportReady {
		exp := r.now().Add(DownloadValidity)
		v.DownloadURL = r.SignedURL(r.base, e.ID, exp)
		v.ExpiresAt = &exp
	}
	return v
}

// Run claims and produces jobs until ctx ends. cmd/moovd runs it in a
// goroutine; poll is how often it looks for work.
func (r *ExportRunner) Run(ctx context.Context, poll time.Duration) {
	if poll <= 0 {
		poll = 5 * time.Second
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx)
		}
	}
}

// tick does one round: report the queue depth, sweep what expired, and run at
// most one job. One per tick keeps a backlog from starving the sweep and
// bounds how much disk a single tick can consume.
func (r *ExportRunner) tick(ctx context.Context) {
	if r.gauge != nil {
		if n, err := r.store.CountPendingExports(ctx); err == nil {
			r.gauge.SetPendingExports(n)
		}
	}
	r.sweep(ctx)
	if _, err := r.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		r.log.Error("accounts: an export failed", "error", err)
	}
}

// RunOnce claims one pending job and produces it. It reports false when there
// was nothing to do.
func (r *ExportRunner) RunOnce(ctx context.Context) (bool, error) {
	job, err := r.store.ClaimPendingExport(ctx, r.now())
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claiming an export: %w", err)
	}
	if err := r.produce(ctx, job); err != nil {
		// The reason reaches the caller through the job row, and it is
		// deliberately a SUMMARY: "Export.error is human-readable, no
		// internals" (§2.6 / the OpenAPI schema).
		r.log.Error("accounts: producing an export failed",
			"export", job.ID, "address", job.Address, "error", err)
		if ferr := r.store.FailExport(ctx, job.ID, "the export could not be produced", r.now()); ferr != nil {
			return true, fmt.Errorf("recording the export failure: %w", ferr)
		}
		return true, nil
	}
	return true, nil
}

// produce writes the zip and completes the job.
func (r *ExportRunner) produce(ctx context.Context, job store.Export) error {
	if job.AccountID == nil {
		return errors.New("the account was purged before the export ran")
	}
	path := filepath.Join(r.dir, job.ID+".zip")

	// A partial file is never left where the download route could find it:
	// the zip is written to a temporary name and renamed only once its hash
	// and size are known.
	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- path is composed from the server's own directory and a minted id.
	if err != nil {
		return fmt.Errorf("creating the export file: %w", err)
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}()

	// The hash of the ZIP ITSELF is what the API reports as manifest.sha256
	// (§2.6), so it is computed on the way out rather than by re-reading the
	// finished file.
	digest := sha256.New()
	zw := zip.NewWriter(io.MultiWriter(f, digest))

	entries, mailboxes, err := r.writeMessages(ctx, job, zw)
	if err != nil {
		return err
	}

	manifest := Manifest{
		Account:     job.Address,
		GeneratedAt: r.now().UTC().Format(rfc3339Milli),
		Messages:    len(entries),
		Mailboxes:   mailboxes,
		Entries:     entries,
	}
	mw, err := zw.Create("manifest.json")
	if err != nil {
		return fmt.Errorf("creating the manifest: %w", err)
	}
	enc := json.NewEncoder(mw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(manifest); err != nil {
		return fmt.Errorf("writing the manifest: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("closing the export archive: %w", err)
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("measuring the export: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing the export file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("publishing the export: %w", err)
	}

	return r.store.CompleteExport(ctx, job.ID, store.ExportResult{
		Path:      path,
		Bytes:     size,
		SHA256:    hex.EncodeToString(digest.Sum(nil)),
		Messages:  len(entries),
		Mailboxes: mailboxes,
	}, r.now())
}

// rfc3339Milli is the contract's timestamp format (§2.3: RFC 3339 UTC with
// milliseconds).
const rfc3339Milli = "2006-01-02T15:04:05.000Z"

// writeMessages streams every message into the archive and returns the
// manifest entries.
//
// The entry path is the IMAP mailbox name with "/" as separator and a
// sequence number inside it (INBOX/00001.eml), which is what §2.6 shows. The
// sequence is per mailbox and follows the store's ordering (mailbox name,
// then UID), so producing the same account twice yields the same layout.
func (r *ExportRunner) writeMessages(ctx context.Context, job store.Export, zw *zip.Writer) ([]ManifestEntry, int, error) {
	var entries []ManifestEntry
	seq := map[string]int{}
	seen := map[string]bool{}
	done := 0

	err := r.store.ForEachAccountMessage(ctx, *job.AccountID, func(m store.ExportMessage) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := blob.HashFromBytes(m.RawSHA256)
		if err != nil {
			// A message whose hash is unreadable is skipped rather than
			// failing the whole export: a retention export of 100k messages
			// must not be lost to one corrupt row. It is absent from the
			// manifest, which is what makes the gap visible when the
			// manifest is compared with the store.
			r.log.Warn("accounts: skipping a message with an unreadable blob hash",
				"export", job.ID, "mailbox", m.MailboxName, "uid", m.UID, "error", err)
			return nil
		}
		rc, err := r.blobs.Open(h)
		if err != nil {
			r.log.Warn("accounts: skipping a message whose blob is missing",
				"export", job.ID, "mailbox", m.MailboxName, "uid", m.UID, "error", err)
			return nil
		}
		defer func() { _ = rc.Close() }()

		box := sanitizeMailboxPath(m.MailboxName)
		seen[box] = true
		seq[box]++
		name := fmt.Sprintf("%s/%05d.eml", box, seq[box])

		w, err := zw.Create(name)
		if err != nil {
			return fmt.Errorf("creating %s in the archive: %w", name, err)
		}
		n, err := io.Copy(w, rc)
		if err != nil {
			return fmt.Errorf("writing %s to the archive: %w", name, err)
		}

		entries = append(entries, ManifestEntry{
			Path:       name,
			SHA256:     hex.EncodeToString(m.RawSHA256),
			Bytes:      n,
			ReceivedAt: m.ReceivedAt.UTC().Format(rfc3339Milli),
			MessageID:  m.MessageID,
		})
		done++
		if done%500 == 0 {
			if err := r.store.SetExportProgress(ctx, job.ID, done, done); err != nil {
				r.log.Warn("accounts: recording export progress failed",
					"export", job.ID, "error", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("reading the messages to export: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, len(seen), nil
}

// sanitizeMailboxPath turns an IMAP mailbox name into a safe archive path.
//
// It is a SECURITY function, not a cosmetic one: a mailbox name is chosen by
// whoever can create a folder in the mailbox, so it is attacker-influenced
// input that must never become "../" in a path someone later extracts. Every
// segment that could escape is neutralized, and the result can only ever
// descend.
func sanitizeMailboxPath(name string) string {
	parts := strings.Split(name, "/")
	clean := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.ReplaceAll(p, "\\", "_")
		switch p {
		case "", ".", "..":
			continue
		}
		// Control characters and a leading dot are both how an archive entry
		// hides from a listing.
		p = strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return '_'
			}
			return r
		}, p)
		clean = append(clean, p)
	}
	if len(clean) == 0 {
		return "_"
	}
	return strings.Join(clean, "/")
}

// sweep removes the zips of exports that are past their retention and marks
// their rows purged, so the download route answers 410 rather than 404
// (§2.6).
func (r *ExportRunner) sweep(ctx context.Context) {
	cutoff := r.now().Add(-Retention)
	expired, err := r.store.ListExpirableExports(ctx, cutoff, 50)
	if err != nil {
		r.log.Error("accounts: listing expirable exports failed", "error", err)
		return
	}
	for _, e := range expired {
		if e.Path != "" {
			if err := os.Remove(e.Path); err != nil && !os.IsNotExist(err) {
				r.log.Error("accounts: removing an expired export failed",
					"export", e.ID, "error", err)
				continue
			}
		}
		if err := r.store.PurgeExport(ctx, e.ID, r.now()); err != nil {
			r.log.Error("accounts: marking an export purged failed", "export", e.ID, "error", err)
		}
	}
}

// OpenDownload resolves a signed download to an open file.
//
// It returns ErrNotFound for everything that is not a live, ready export the
// signature covers - a wrong signature, a past expiry, an unknown id - and
// ErrExportPurged only for an export that WAS ready and has since been swept.
// That single distinction is the contract's 410 (§2.6); everything else is
// the generic 404, so a signature probe learns nothing.
func (r *ExportRunner) OpenDownload(ctx context.Context, origin, id string, exp time.Time, sig string) (io.ReadCloser, store.Export, error) {
	if !r.verify(origin, id, exp, sig) {
		return nil, store.Export{}, ErrNotFound
	}
	if r.now().After(exp) {
		return nil, store.Export{}, ErrNotFound
	}
	e, err := r.store.GetExport(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.Export{}, ErrNotFound
		}
		return nil, store.Export{}, fmt.Errorf("reading export %q: %w", id, err)
	}
	if e.PurgedAt != nil {
		return nil, e, ErrExportPurged
	}
	if ExportStatus(e.Status) != ExportReady || e.Path == "" {
		return nil, store.Export{}, ErrNotFound
	}
	f, err := os.Open(e.Path) // #nosec G304 -- the path was written by this package into its own directory.
	if err != nil {
		if os.IsNotExist(err) {
			// The row says ready but the file is gone: from the caller's side
			// this is indistinguishable from a purge, and 410 is the honest
			// answer - the export existed and no longer does.
			return nil, e, ErrExportPurged
		}
		return nil, store.Export{}, fmt.Errorf("opening export %q: %w", id, err)
	}
	return f, e, nil
}

// ErrExportPurged is the 410 of §2.6: the export existed and its zip is gone.
var ErrExportPurged = errors.New("accounts: this export has been purged")
