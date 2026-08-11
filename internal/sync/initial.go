package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"time"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/parser"
	"github.com/GrupoNU/moov/internal/store"
)

// Options configures an initial sync run. The zero value is valid and means
// every default below.
type Options struct {
	// RecentWindow is how far back phase A reaches in the inbox (L2 §2.5
	// step 2). Default DefaultRecentWindow.
	RecentWindow time.Duration

	// Connections is the number of IMAP connections used per account. Default
	// DefaultConnections.
	//
	// It is small on purpose and it is not a throughput dial: ADR §4 requires
	// the engine to stay clear of Mailcow's fail2ban, and a mailbox is not
	// faster to read through ten sockets than through two — the server
	// serializes on the same maildir either way. Two is one connection
	// backfilling while another finishes a window.
	Connections int

	// ParseWorkers is the size of the CPU-bound parse pool. Default
	// GOMAXPROCS.
	//
	// S3 H6 measured the bulk path CPU-bound in to_tsvector at ~2,063 rows/s,
	// and the MIME cascade is the other half of that cost. Both scale with
	// cores, not with accounts, which is why this is sized from GOMAXPROCS and
	// NOT multiplied per account: eighty-nine accounts on an eight-core box
	// must not spawn eighty-nine parse pools.
	ParseWorkers int

	// FetchWindow is how many UIDs one backfill window covers. Default
	// DefaultFetchWindow.
	FetchWindow int

	// BatchSize is how many parsed messages are written per store transaction.
	// Default DefaultBatchSize.
	BatchSize int

	// Limits bounds the MIME parser. The zero value means parser defaults.
	Limits parser.Limits

	// Logger receives structured progress. Default slog.Default().
	Logger *slog.Logger

	// Clock returns the current time. Injectable so a test can make the
	// 30-day window deterministic instead of depending on when it runs.
	Clock func() time.Time

	// OnProgress, when set, is called after every committed batch. It exists
	// for tests and for the E8 metrics exporter; it must not block.
	OnProgress func(Progress)
}

// Defaults for Options.
const (
	// DefaultRecentWindow is the phase-A reach: "INBOX last 30 days" (L2 §2.5).
	DefaultRecentWindow = 30 * 24 * time.Hour

	// DefaultConnections is the per-account connection budget (L2 §2.5,
	// ADR §4).
	DefaultConnections = 2

	// DefaultFetchWindow is how many UIDs a backfill window spans.
	//
	// It is the checkpoint granularity: a kill -9 costs at most the messages
	// of one window that had not yet been committed, and every committed batch
	// inside a window is durable regardless. 500 is large enough that the
	// per-window round trips disappear into the fetch and small enough that a
	// window is minutes, not hours.
	DefaultFetchWindow = 500

	// DefaultBatchSize is how many messages one store transaction writes
	// (L2 §3/E5: "batches of ~100").
	DefaultBatchSize = 100
)

// withDefaults returns a copy with every zero field filled in.
func (o Options) withDefaults() Options {
	if o.RecentWindow <= 0 {
		o.RecentWindow = DefaultRecentWindow
	}
	if o.Connections <= 0 {
		o.Connections = DefaultConnections
	}
	if o.ParseWorkers <= 0 {
		o.ParseWorkers = runtime.GOMAXPROCS(0)
	}
	if o.FetchWindow <= 0 {
		o.FetchWindow = DefaultFetchWindow
	}
	if o.BatchSize <= 0 {
		o.BatchSize = DefaultBatchSize
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return o
}

// Phase names a stage of the initial sync, for logs, checkpoints and progress.
type Phase string

// The initial-sync phases (L2 §2.5 / A7 path 1).
const (
	// PhaseDiscover is the LIST + STATUS pass that builds the mailbox tree.
	PhaseDiscover Phase = "discover"

	// PhaseRecent is the inbox's recent window: the phase whose completion
	// makes the account usable.
	PhaseRecent Phase = "recent"

	// PhaseBackfill is the historical pass over every mailbox.
	PhaseBackfill Phase = "backfill"

	// PhaseComplete means every mailbox reached backfill_state 'complete'.
	PhaseComplete Phase = "complete"
)

// Progress is one observation of an in-flight run.
type Progress struct {
	AccountID int64
	Phase     Phase
	Mailbox   string

	// Stored is how many messages this run has committed so far.
	Stored int
	// Failed is how many were stored with parse_status='failed' (R4).
	Failed int
	// Skipped is how many were already present, i.e. the idempotency path.
	Skipped int
}

// Result summarizes a finished run.
type Result struct {
	AccountID int64

	// Mailboxes is how many folders were discovered and upserted.
	Mailboxes int

	// RecentStored is what phase A committed, and RecentDuration how long it
	// took — the number the "<60 s for a 10k mailbox" AC is measured against.
	RecentStored  int
	RecentElapsed time.Duration

	// BackfillStored is what phase B committed.
	BackfillStored int

	// Skipped counts messages already present, which is the idempotency path a
	// resumed run takes.
	Skipped int

	// Failed counts messages stored with parse_status='failed'. Never an
	// error: a message the cascade cannot read is still durable as a blob and
	// still occupies its UID, and stopping the run for it would let one broken
	// message hold a mailbox hostage (R4).
	Failed int

	// Elapsed is the whole run.
	Elapsed time.Duration

	// Complete reports whether every mailbox finished its backfill. False
	// means the run was interrupted and a later run resumes from the
	// checkpoints.
	Complete bool
}

// Rate returns the phase-A throughput in messages per second, which is the
// figure the E5 acceptance criterion is stated in.
func (r Result) Rate() float64 {
	if r.RecentElapsed <= 0 {
		return 0
	}
	return float64(r.RecentStored) / r.RecentElapsed.Seconds()
}

// ErrNoConnections is returned when a Syncer is built with an empty pool.
var ErrNoConnections = errors.New("sync: at least one IMAP connection is required")

// Syncer runs the initial sync of one account.
//
// # Why the pipeline is shaped this way
//
// The three costs of a backfill are on different resources: the FETCH is
// network- and server-bound, the MIME cascade is CPU-bound (S3 H6, S4), and the
// insert is database-bound. Running them in one loop makes the whole run as
// slow as their sum. So a Syncer is a three-stage pipeline — fetch, parse pool,
// batched writer — with bounded queues between the stages, and the bound is
// what keeps a 500k-message mailbox inside a fixed memory budget instead of
// reading the mailbox into a slice.
//
// # Why every stage is idempotent
//
// The acceptance criterion is a kill -9 at ANY point, so no stage may assume it
// runs to completion. Blobs are content-addressed, so a re-Put of the same
// bytes is free (blob.Put). Message rows are keyed by (mailbox, uidvalidity,
// uid), so a re-fetched UID is filtered out before it reaches the store. And
// the checkpoint is written only AFTER the batch it describes is committed, so
// the worst a crash can do is repeat work — never skip it.
type Syncer struct {
	store *store.Store
	blobs BlobPutter
	conns *connPool
	opts  Options
}

// BlobPutter is the slice of internal/blob the sync engine uses.
//
// It is an interface rather than *blob.Store so that this package's unit tests
// do not need a filesystem and a database to exercise the pipeline's failure
// paths. The production implementation is *blob.Store, whose Put is
// write-once, content-addressed and therefore already idempotent.
type BlobPutter interface {
	Put(ctx context.Context, r io.Reader) (blob.Hash, int64, error)
}

// New builds a Syncer over an already-connected pool of IMAP clients.
//
// The clients are supplied rather than dialed here because who owns a
// connection's lifecycle is a decision that belongs to the supervisor: E6's
// watcher holds one of the same account's connections for as long as the
// account is active, and a Syncer that dialed its own would double the
// account's socket count behind the supervisor's back.
func New(st *store.Store, blobs BlobPutter, clients []imap.Client, opts Options) (*Syncer, error) {
	if st == nil {
		return nil, errors.New("sync: a store is required")
	}
	if blobs == nil {
		return nil, errors.New("sync: a blob store is required")
	}
	if len(clients) == 0 {
		return nil, ErrNoConnections
	}

	opts = opts.withDefaults()
	if len(clients) < opts.Connections {
		opts.Connections = len(clients)
	}
	return &Syncer{
		store: st,
		blobs: blobs,
		conns: newConnPool(clients[:opts.Connections]),
		opts:  opts,
	}, nil
}

// Run performs the initial sync of an account: discovery, then the recent
// window, then the backfill (L2 §2.5, A7 path 1).
//
// It is safe to call on an account that is partly synced — which is the normal
// case, because that is what a resumed run is. Every phase consults the
// checkpoints first and skips what is already done.
func (s *Syncer) Run(ctx context.Context, account store.Account) (Result, error) {
	started := s.opts.Clock()
	log := s.opts.Logger.With("account_id", account.ID, "email", account.Email)

	res := Result{AccountID: account.ID}

	boxes, err := s.discover(ctx, account, log)
	if err != nil {
		return res, s.recordFailure(ctx, account.ID, PhaseDiscover, err)
	}
	res.Mailboxes = len(boxes)

	recent, err := s.runRecent(ctx, account, boxes, log)
	res.RecentStored, res.RecentElapsed = recent.stored, recent.elapsed
	res.Skipped += recent.skipped
	res.Failed += recent.failed
	if err != nil {
		return res, s.recordFailure(ctx, account.ID, PhaseRecent, err)
	}

	back, err := s.runBackfill(ctx, account, boxes, log)
	res.BackfillStored = back.stored
	res.Skipped += back.skipped
	res.Failed += back.failed
	res.Elapsed = s.opts.Clock().Sub(started)
	if err != nil {
		return res, s.recordFailure(ctx, account.ID, PhaseBackfill, err)
	}

	res.Complete = true
	if err := s.saveAccountPhase(ctx, account.ID, PhaseComplete); err != nil {
		return res, err
	}

	log.Info("initial sync complete",
		"mailboxes", res.Mailboxes,
		"recent_stored", res.RecentStored,
		"backfill_stored", res.BackfillStored,
		"skipped", res.Skipped,
		"parse_failed", res.Failed,
		"elapsed", res.Elapsed.Round(time.Millisecond),
		"recent_msgs_per_sec", fmt.Sprintf("%.1f", res.Rate()),
	)
	return res, nil
}

// recordFailure persists the error against the account's sync log and returns
// it unchanged.
//
// The checkpoint itself is deliberately NOT touched: RecordSyncError leaves the
// resume point alone, so a retry restarts from the last committed window rather
// than from the beginning. A context cancellation is recorded too — an operator
// looking at a half-synced account should see that it was interrupted, not an
// empty error column.
func (s *Syncer) recordFailure(ctx context.Context, accountID int64, phase Phase, cause error) error {
	// The failing context may be the one that was canceled, so the bookkeeping
	// gets a fresh, short-lived context of its own; otherwise a shutdown would
	// lose exactly the record explaining why the run stopped.
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	msg := fmt.Sprintf("%s: %v", phase, cause)
	if _, err := s.store.RecordSyncError(recCtx, accountID, store.AccountScope, msg); err != nil {
		s.opts.Logger.Warn("recording sync error failed",
			"account_id", accountID, "phase", phase, "error", err)
	}
	return fmt.Errorf("%s phase: %w", phase, cause)
}
