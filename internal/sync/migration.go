package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// MigrationOptions configures a bulk installation migration (A7 path 2, the
// 89-account Crash case).
type MigrationOptions struct {
	// Options is the per-account configuration. Its ParseWorkers field is
	// IGNORED here and replaced by the migration-wide pool (see Migrator).
	Options Options

	// Accounts is how many accounts are synced at once. Default
	// DefaultMigrationAccounts.
	//
	// It bounds IMAP sockets and database connections, not CPU: the parse pool
	// is shared and sized by core count, so raising this does not multiply the
	// CPU-bound work, it only widens the funnel feeding it.
	Accounts int

	// ConnectFn opens the IMAP connections for one account. Required: this
	// package does not own credentials (E7 does), so the caller supplies the
	// dialing.
	//
	// The returned clients are closed by the migrator when the account
	// finishes, including on failure.
	ConnectFn func(ctx context.Context, account store.Account, n int) ([]imap.Client, error)

	// ContinueOnError keeps the migration going when one account fails, which
	// is the correct default for a bulk run: eighty-eight accounts should not
	// be held back by one whose credentials are stale. The failure is recorded
	// in sync_log either way.
	ContinueOnError bool
}

// DefaultMigrationAccounts is how many accounts a migration syncs
// concurrently.
//
// Four rather than "as many as possible": S3 H6 measured the bulk path
// CPU-bound at ~2,063 rows/s in to_tsvector, so beyond a handful of accounts
// the extra concurrency buys queueing, not throughput, while each account adds
// two IMAP sockets against a Mailcow that has fail2ban watching (ADR §4).
const DefaultMigrationAccounts = 4

// MigrationResult summarizes a bulk run.
type MigrationResult struct {
	Accounts  int
	Succeeded int
	Failed    int

	Stored  int
	Skipped int
	// ParseFailed counts messages stored with parse_status='failed' (R4).
	ParseFailed int

	Elapsed time.Duration

	// Errors holds one entry per failed account, keyed by account id.
	Errors map[int64]error
}

// Rate returns the whole migration's throughput in messages per second, which
// is the figure to extrapolate an installation-sized run from.
func (r MigrationResult) Rate() float64 {
	if r.Elapsed <= 0 {
		return 0
	}
	return float64(r.Stored) / r.Elapsed.Seconds()
}

// Migrator runs many accounts through the same pipeline as a single-account
// initial sync (A7 path 2).
//
// # Why it is the same pipeline
//
// The spec sketches a separate COPY path with the GIN indexes built at the end.
// That optimization is not available through the store's API: internal/store
// exposes InsertMessages (batched pgx.Batch) and no bulk-copy method, and this
// epic may not change that package. Rather than reach around the store with raw
// SQL — which would escape exactly the account-scoping and index guarantees
// §4.3 exists to enforce — the migration reuses the batched path and gets its
// parallelism from running accounts concurrently against one shared, core-sized
// parse pool.
//
// What that costs is recorded honestly in the E5 report rather than hidden: the
// COPY + deferred-index path remains available as a store extension when
// measurements justify it, and the shape here (bounded accounts, shared parse
// pool) is what a COPY path would need anyway.
type Migrator struct {
	store *store.Store
	blobs BlobPutter
	opts  MigrationOptions
}

// NewMigrator builds a bulk migrator.
func NewMigrator(st *store.Store, blobs BlobPutter, opts MigrationOptions) (*Migrator, error) {
	if st == nil {
		return nil, errors.New("sync: a store is required")
	}
	if blobs == nil {
		return nil, errors.New("sync: a blob store is required")
	}
	if opts.ConnectFn == nil {
		return nil, errors.New("sync: MigrationOptions.ConnectFn is required")
	}

	opts.Options = opts.Options.withDefaults()
	if opts.Accounts <= 0 {
		opts.Accounts = DefaultMigrationAccounts
	}

	// THE POINT OF S3 H6: parse workers are budgeted per core for the whole
	// migration, then divided among the accounts running at once. Eighty-nine
	// accounts each spawning GOMAXPROCS parse goroutines would put hundreds of
	// CPU-bound goroutines on eight cores, where they would spend their time
	// context-switching rather than parsing.
	perAccount := runtime.GOMAXPROCS(0) / opts.Accounts
	if perAccount < 1 {
		perAccount = 1
	}
	opts.Options.ParseWorkers = perAccount

	return &Migrator{store: st, blobs: blobs, opts: opts}, nil
}

// Run migrates the given accounts, at most Accounts at a time.
func (m *Migrator) Run(ctx context.Context, accounts []store.Account) (MigrationResult, error) {
	started := time.Now()
	log := m.opts.Options.Logger.With("component", "migration")

	res := MigrationResult{Accounts: len(accounts), Errors: map[int64]error{}}
	if len(accounts) == 0 {
		return res, nil
	}

	log.Info("bulk migration starting",
		"accounts", len(accounts),
		"concurrency", m.opts.Accounts,
		"parse_workers_per_account", m.opts.Options.ParseWorkers,
		"gomaxprocs", runtime.GOMAXPROCS(0),
	)

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, m.opts.Accounts)
	)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, acct := range accounts {
		// Acquiring a slot must PREFER making progress over noticing a
		// cancellation. A plain two-case select picks randomly when both are
		// ready, so once runCtx is canceled — which ContinueOnError=false does
		// deliberately — the loop could abandon accounts whose slot was free.
		// Checking the semaphore first makes the cancellation a fallback rather
		// than a coin flip.
		select {
		case sem <- struct{}{}:
		default:
			select {
			case sem <- struct{}{}:
			case <-runCtx.Done():
				// Stop launching; the accounts already running finish or
				// observe the cancellation themselves.
				wg.Wait()
				res.Elapsed = time.Since(started)
				return res, runCtx.Err()
			}
		}

		wg.Add(1)
		go func(a store.Account) {
			defer wg.Done()
			defer func() { <-sem }()

			one, err := m.runOne(runCtx, a, log)

			mu.Lock()
			defer mu.Unlock()
			res.Stored += one.RecentStored + one.BackfillStored
			res.Skipped += one.Skipped
			res.ParseFailed += one.Failed
			if err != nil {
				res.Failed++
				res.Errors[a.ID] = err
				if !m.opts.ContinueOnError {
					cancel()
				}
				return
			}
			res.Succeeded++
		}(acct)
	}

	wg.Wait()
	res.Elapsed = time.Since(started)

	log.Info("bulk migration finished",
		"succeeded", res.Succeeded,
		"failed", res.Failed,
		"stored", res.Stored,
		"skipped", res.Skipped,
		"parse_failed", res.ParseFailed,
		"elapsed", res.Elapsed.Round(time.Millisecond),
		"msgs_per_sec", fmt.Sprintf("%.1f", res.Rate()),
	)

	if res.Failed > 0 && !m.opts.ContinueOnError {
		return res, fmt.Errorf("bulk migration: %d of %d accounts failed", res.Failed, res.Accounts)
	}
	return res, nil
}

// runOne connects, syncs and disconnects one account.
func (m *Migrator) runOne(ctx context.Context, account store.Account, log *slog.Logger) (Result, error) {
	clients, err := m.opts.ConnectFn(ctx, account, m.opts.Options.Connections)
	if err != nil {
		return Result{AccountID: account.ID}, fmt.Errorf("connecting account %d: %w", account.ID, err)
	}
	defer func() {
		for _, c := range clients {
			if err := c.Close(); err != nil {
				log.Debug("closing connection", "account_id", account.ID, "error", err)
			}
		}
	}()

	syncer, err := New(m.store, m.blobs, clients, m.opts.Options)
	if err != nil {
		return Result{AccountID: account.ID}, err
	}
	return syncer.Run(ctx, account)
}
