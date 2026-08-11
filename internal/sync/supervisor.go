package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// Connector opens IMAP connections for an account.
//
// It is the seam between this package and credential handling: decrypting an
// app password is E7's job (internal/crypto), and a sync engine that could read
// a credential store would be a sync engine that has to be trusted with one.
type Connector interface {
	// Connect returns n connected clients for the account. The caller closes
	// them. Fewer than n is acceptable; zero must be an error.
	Connect(ctx context.Context, account store.Account, n int) ([]imap.Client, error)
}

// ConnectorFunc adapts a function to Connector.
type ConnectorFunc func(ctx context.Context, account store.Account, n int) ([]imap.Client, error)

// Connect implements Connector.
func (f ConnectorFunc) Connect(ctx context.Context, account store.Account, n int) ([]imap.Client, error) {
	return f(ctx, account, n)
}

// Watcher is the seam E6 fills.
//
// The supervisor calls Watch for every account whose initial sync is complete
// and then leaves it alone. E5 ships no implementation: with a nil Watcher the
// supervisor performs initial syncs and stops, which is exactly the scope of
// this epic. E6 supplies the NOTIFY+IDLE watcher and the incremental fetch
// behind this one method, without touching anything above.
type Watcher interface {
	// Watch runs until ctx ends. It is called once per synced account, in its
	// own goroutine.
	Watch(ctx context.Context, account store.Account) error
}

// SupervisorOptions configures the sync supervisor.
type SupervisorOptions struct {
	// Options is the per-account initial-sync configuration.
	Options Options

	// Connector opens IMAP connections. Required.
	Connector Connector

	// Watcher is E6's incremental engine. Nil means initial sync only.
	Watcher Watcher

	// Concurrency is how many accounts are initially synced at once. Default
	// DefaultMigrationAccounts.
	Concurrency int

	// RetryDelay is how long a failed account waits before the supervisor
	// tries again. Default DefaultRetryDelay. Zero attempts is not an option:
	// an account whose sync failed once must not be abandoned for the lifetime
	// of the process.
	RetryDelay time.Duration
}

// DefaultRetryDelay is how long a failed account waits before a retry.
//
// Minutes rather than seconds, because the failures that reach this level are
// not transient — bad credentials, a server that is down — and retrying them
// quickly is how an engine gets its IP banned by fail2ban (ADR §4). The
// per-account circuit breaker in sync_log is the finer-grained control; this is
// the coarse floor under it.
const DefaultRetryDelay = 5 * time.Minute

// Supervisor drives the initial sync of every enabled account and hands the
// synced ones to the watcher.
//
// It is what moovd starts. Its whole job is deciding WHICH accounts need work
// and keeping the concurrency bounded; the work itself belongs to Syncer.
type Supervisor struct {
	store *store.Store
	blobs BlobPutter
	opts  SupervisorOptions
	log   *slog.Logger
}

// NewSupervisor builds a supervisor.
func NewSupervisor(st *store.Store, blobs BlobPutter, opts SupervisorOptions) (*Supervisor, error) {
	if st == nil {
		return nil, errors.New("sync: a store is required")
	}
	if blobs == nil {
		return nil, errors.New("sync: a blob store is required")
	}
	if opts.Connector == nil {
		return nil, errors.New("sync: a Connector is required")
	}

	opts.Options = opts.Options.withDefaults()
	if opts.Concurrency <= 0 {
		opts.Concurrency = DefaultMigrationAccounts
	}
	if opts.RetryDelay <= 0 {
		opts.RetryDelay = DefaultRetryDelay
	}

	return &Supervisor{
		store: st,
		blobs: blobs,
		opts:  opts,
		log:   opts.Options.Logger.With("component", "sync-supervisor"),
	}, nil
}

// Run syncs every enabled account and then blocks until ctx ends.
//
// It blocks rather than returning because it owns the watchers: returning would
// mean either killing them or orphaning them, and a supervisor that outlives
// what it supervises is how goroutines leak. moovd cancels ctx to stop it.
func (s *Supervisor) Run(ctx context.Context) error {
	accounts, err := s.eligibleAccounts(ctx)
	if err != nil {
		return err
	}

	s.log.Info("sync supervisor starting", "accounts", len(accounts),
		"concurrency", s.opts.Concurrency, "watcher", s.opts.Watcher != nil)

	var wg sync.WaitGroup
	sem := make(chan struct{}, s.opts.Concurrency)

	for _, acct := range accounts {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}

		wg.Add(1)
		go func(a store.Account) {
			defer wg.Done()
			defer func() { <-sem }()
			s.superviseAccount(ctx, a)
		}(acct)
	}

	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}

	s.log.Info("initial sync finished for every account; supervisor idle")

	// With no watcher there is nothing left to do, but returning would tell
	// moovd a component exited unexpectedly. Waiting for the shutdown signal is
	// the honest report of "done, still running".
	<-ctx.Done()
	return ctx.Err()
}

// superviseAccount runs one account's initial sync (if needed) and then its
// watcher.
func (s *Supervisor) superviseAccount(ctx context.Context, account store.Account) {
	log := s.log.With("account_id", account.ID, "email", account.Email)

	for {
		if err := ctx.Err(); err != nil {
			return
		}

		err := s.syncOnce(ctx, account, log)
		switch {
		case err == nil:
			s.runWatcher(ctx, account, log)
			return
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return
		}

		log.Error("initial sync failed; will retry", "error", err, "retry_in", s.opts.RetryDelay)
		select {
		case <-time.After(s.opts.RetryDelay):
		case <-ctx.Done():
			return
		}
	}
}

// syncOnce performs the account's initial sync unless the checkpoints say it is
// already complete.
func (s *Supervisor) syncOnce(ctx context.Context, account store.Account, log *slog.Logger) error {
	done, err := s.alreadyComplete(ctx, account)
	if err != nil {
		return err
	}
	if done {
		log.Info("initial sync already complete; skipping to the watcher")
		return nil
	}

	clients, err := s.opts.Connector.Connect(ctx, account, s.opts.Options.Connections)
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer func() {
		for _, c := range clients {
			if cerr := c.Close(); cerr != nil {
				log.Debug("closing connection", "error", cerr)
			}
		}
	}()

	syncer, err := New(s.store, s.blobs, clients, s.opts.Options)
	if err != nil {
		return err
	}

	res, err := syncer.Run(ctx, account)
	if err != nil {
		return err
	}
	log.Info("initial sync done",
		"mailboxes", res.Mailboxes,
		"stored", res.RecentStored+res.BackfillStored,
		"skipped", res.Skipped,
		"parse_failed", res.Failed,
		"elapsed", res.Elapsed.Round(time.Millisecond),
	)
	return nil
}

// runWatcher hands the account to E6, if there is an E6 yet.
func (s *Supervisor) runWatcher(ctx context.Context, account store.Account, log *slog.Logger) {
	if s.opts.Watcher == nil {
		log.Debug("no watcher configured; account is synced and idle (E6 not wired)")
		return
	}
	if err := s.opts.Watcher.Watch(ctx, account); err != nil &&
		!errors.Is(err, context.Canceled) {
		log.Error("watcher stopped", "error", err)
	}
}

// alreadyComplete reports whether the account's checkpoints say the initial
// sync finished.
//
// The account-level phase is necessary but not sufficient: it is written when
// the run reaches the end, while a mailbox added since then has never been
// backfilled. So the mailbox rows are the authority, and the account phase is
// the cheap negative check.
func (s *Supervisor) alreadyComplete(ctx context.Context, account store.Account) (bool, error) {
	phase, err := s.loadPhase(ctx, account.ID)
	if err != nil {
		return false, err
	}
	if phase != PhaseComplete {
		return false, nil
	}

	boxes, err := s.store.ListMailboxes(ctx, account.ID)
	if err != nil {
		return false, fmt.Errorf("listing stored mailboxes: %w", err)
	}
	if len(boxes) == 0 {
		return false, nil
	}
	for _, b := range boxes {
		if b.Selectable && b.BackfillState != store.BackfillComplete {
			return false, nil
		}
	}
	return true, nil
}

// loadPhase reads the account-scope checkpoint without needing a Syncer.
func (s *Supervisor) loadPhase(ctx context.Context, accountID int64) (Phase, error) {
	tmp := &Syncer{store: s.store, opts: s.opts.Options}
	return tmp.loadAccountPhase(ctx, accountID)
}

// eligibleAccounts returns the accounts the engine should sync: active, with
// usable credentials.
//
// An account with credential_state 'pending' is skipped rather than attempted:
// E7 has not provisioned it yet, and trying to log in without a password is a
// failed authentication against a server with fail2ban watching (ADR §4).
func (s *Supervisor) eligibleAccounts(ctx context.Context) ([]store.Account, error) {
	all, err := s.store.ListAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing accounts: %w", err)
	}

	out := make([]store.Account, 0, len(all))
	for _, a := range all {
		if a.State != store.AccountActive {
			continue
		}
		if a.CredentialState != store.CredentialActive {
			s.log.Debug("skipping account without active credentials",
				"account_id", a.ID, "credential_state", a.CredentialState)
			continue
		}
		out = append(out, a)
	}
	return out, nil
}
