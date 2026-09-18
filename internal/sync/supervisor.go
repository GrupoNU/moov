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

// SupervisionKind is what happened to an account under the supervisor.
type SupervisionKind string

// The supervision observations. They are deliberately few: this seam reports
// the account's LIFECYCLE under the supervisor, not its sync progress, which
// mailboxes.last_synced_at already records and moov_sync_lag_seconds already
// reads.
const (
	// ObsSupervised is emitted once, when the supervisor takes charge of an
	// account — at startup for an account that was already there, at adoption
	// for one created since. It starts the clock that ObsInitialSynced stops.
	ObsSupervised SupervisionKind = "supervised"

	// ObsInitialSynced is emitted when an account's initial sync is complete,
	// including the cheap case where it was already complete from a previous
	// run of the daemon. It is the moment the account stops being stranded.
	ObsInitialSynced SupervisionKind = "initial_synced"

	// ObsInitialSyncFailed is emitted each time an initial sync attempt fails
	// and the supervisor schedules a retry. The account remains stranded.
	ObsInitialSyncFailed SupervisionKind = "initial_sync_failed"
)

// SupervisionObservation is one fact about an account's supervision.
type SupervisionObservation struct {
	AccountID int64
	Email     string
	Kind      SupervisionKind
}

// SupervisionObserver receives supervision observations.
//
// It is the same seam shape as WatcherOptions.OnEvent, submit.Observer and
// mail.SubmissionObserver, and it exists for the same reason: internal/sync
// must not import internal/metrics. The engine declares a callback; cmd/moovd,
// the only place in the daemon that knows an exporter exists, adapts it.
//
// Implementations MUST NOT block: they are called from the supervisor's own
// goroutines, and a slow observer would delay a sync.
type SupervisionObserver func(SupervisionObservation)

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

	// OnAccount receives supervision observations, or nil to discard them.
	// It is how the daemon learns that an account is supervised but has never
	// finished an initial sync — the stranded shape of the 2026-09-17 defect.
	OnAccount SupervisionObserver

	// DiscoveryInterval is how often the supervisor re-reads the eligible
	// accounts so it can adopt the ones created since. Default
	// DefaultDiscoveryInterval. It is a ceiling on how long a brand-new
	// mailbox stays invisible, not a target: Nudge collapses the common case.
	DiscoveryInterval time.Duration
}

// DefaultDiscoveryInterval is how often the supervisor looks for accounts it is
// not already supervising.
//
// # Why thirty seconds
//
// The number is set by what a person is doing at the other end. A mailbox
// created through the accounts API is contractually usable the moment the call
// returns 201 (contract §2.4), and the gate criterion is that it be operational
// in under 60 s. The organizer who made it opens the webmail immediately, so an
// interval measured in minutes would mean a mailbox that is "created" and blank
// for the entire time anyone is looking at it — which is the defect this
// constant exists to bound, merely slower. Thirty seconds fits the worst case
// (interval plus one initial sync) inside the 60 s criterion with room for the
// sync itself.
//
// The other side of the trade is what the sweep costs, and it is close to
// nothing: one indexed SELECT over the accounts table, which has as many rows as
// the deployment has mailboxes — tens, not millions — and which almost never
// changes. Two per minute is noise next to what one IDLE connection does. There
// is no reason to be miserly here, and being miserly is what produced the bug.
//
// It is NOT shorter because below a few seconds the sweep stops being free
// relative to its own benefit: Nudge already makes provisioning immediate, so
// everything the ticker still catches (an account created by another process,
// by moovctl, or by a caller that died before nudging) is by definition not
// something anyone is watching a spinner for.
const DefaultDiscoveryInterval = 30 * time.Second

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

	// notify carries provisioning nudges (see Nudge). One buffered slot: a
	// second pending nudge would only buy a redundant sweep.
	notify chan struct{}
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
	if opts.DiscoveryInterval <= 0 {
		opts.DiscoveryInterval = DefaultDiscoveryInterval
	}

	return &Supervisor{
		store:  st,
		blobs:  blobs,
		opts:   opts,
		log:    opts.Options.Logger.With("component", "sync-supervisor"),
		notify: make(chan struct{}, 1),
	}, nil
}

// Run supervises every eligible account and blocks until ctx ends.
//
// It blocks rather than returning because it owns the watchers: returning would
// mean either killing them or orphaning them, and a supervisor that outlives
// what it supervises is how goroutines leak. moovd cancels ctx to stop it.
//
// # Why this is a loop and not a startup snapshot
//
// Until 2026-09-17 this method read the eligible accounts ONCE, iterated that
// slice and blocked. The supervised set was therefore frozen at the instant the
// process started, and an account created afterwards was invisible to the
// engine until somebody restarted moovd. That is not a theoretical gap: the
// accounts API exists precisely so a portal can create mailboxes with nobody
// watching a terminal, and the first mailbox it created that way
// (unidos@corppass.events) never synced. Every observable signal said it was
// fine — 201 from the API, mailbox in Mailcow, credential validated against
// Dovecot, row active/active, delegated session opening the webmail — and the
// inbox showed loading skeletons forever. Mail delivered to it changed nothing,
// because no watcher was ever attached to notice.
//
// So discovery is periodic: the supervised set is a moving target.
//
// # What the loop deliberately does NOT do
//
// It does not drop accounts that stop being eligible. Suspend, read-only and
// delete already revoke the sessions through SessionRevoker, which is the path
// that actually stops a user reaching their mail; tearing the watcher down from
// here as well would be a second, racing implementation of the same policy, and
// the failure mode of getting it wrong is worse than the cost of not doing it.
// A suspended account keeps a watcher that syncs mail nobody can read — a few
// idle IMAP connections — until the next restart. A deleted account is a
// different story and needs no special case either: its rows are removed by the
// purge job, so its syncer and watcher start failing against a missing account
// and unwind through the paths that already exist for a broken account. That is
// why eligibleAccounts is only ever consulted to ADD.
func (s *Supervisor) Run(ctx context.Context) error {
	accounts, err := s.eligibleAccounts(ctx)
	if err != nil {
		return err
	}

	s.log.Info("sync supervisor starting", "accounts", len(accounts),
		"concurrency", s.opts.Concurrency, "watcher", s.opts.Watcher != nil,
		"discovery_interval", s.opts.DiscoveryInterval)

	var wg sync.WaitGroup
	sem := make(chan struct{}, s.opts.Concurrency)

	// supervised is the set of accounts that already have a goroutine. It is the
	// guard against the one thing adoption must never do: start a SECOND
	// supervisor for an account that already has one. Two goroutines on one
	// account means two IMAP sessions, two syncers writing the same
	// (mailbox_id, uidvalidity, uid) rows and two watchers fighting over the
	// same NOTIFY connection. The concurrency semaphore does not prevent any of
	// that: it bounds how many initial syncs run at once, not which accounts
	// they are for, and it is released while the watcher is still running.
	//
	// It needs no mutex because it is read and written from this goroutine only.
	supervised := map[int64]struct{}{}

	// start launches one account, or does nothing if it already has a goroutine.
	// It reports false only when ctx ended while waiting for a slot.
	start := func(a store.Account) bool {
		if _, ok := supervised[a.ID]; ok {
			return true
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return false
		}
		supervised[a.ID] = struct{}{}
		wg.Add(1)
		go func(a store.Account) {
			defer wg.Done()
			// The slot is released by superviseAccount the moment the INITIAL
			// SYNC is done — not when this goroutine ends, which is when the
			// account's watcher dies, i.e. at shutdown.
			//
			// Releasing it here instead (the shape this had until 2026-09-16)
			// means every slot is held for the process's lifetime, so with
			// Concurrency slots filled, account number Concurrency+1 waits on a
			// semaphore that is never posted: no sync, no watcher, no error, no
			// log line. That is what stranded the pilot's fifth account, and
			// TestSupervisorWatchesEveryAccountBeyondConcurrency pins it.
			s.superviseAccount(ctx, a, func() { <-sem })
		}(a)
		return true
	}

	for _, acct := range accounts {
		if !start(acct) {
			wg.Wait()
			return ctx.Err()
		}
	}

	ticker := time.NewTicker(s.opts.DiscoveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Every account goroutine watches the same ctx, so they are already
			// unwinding; waiting for them is what makes shutdown a clean stop
			// rather than a race between the daemon exiting and watchers
			// closing their IMAP connections.
			wg.Wait()
			return ctx.Err()
		case <-s.notify:
			// A provisioning nudge: the common case becomes immediate instead
			// of waiting out a sweep. See Nudge for why it is an optimization
			// and never the mechanism.
		case <-ticker.C:
		}

		fresh, err := s.eligibleAccounts(ctx)
		if err != nil {
			// A failed sweep is not fatal. A database that is briefly
			// unreachable must not take down an engine whose already-supervised
			// accounts are working; the next tick tries again. It is logged at
			// warn because a sweep that keeps failing means new accounts are
			// silently not being adopted, which is this very defect wearing a
			// different hat.
			if ctx.Err() == nil {
				s.log.Warn("discovering accounts failed; will retry", "error", err,
					"retry_in", s.opts.DiscoveryInterval)
			}
			continue
		}

		for _, a := range fresh {
			if _, known := supervised[a.ID]; known {
				continue
			}
			s.log.Info("adopting an account that appeared since startup",
				"account_id", a.ID, "email", a.Email)
			if !start(a) {
				wg.Wait()
				return ctx.Err()
			}
		}
	}
}

// Nudge asks the supervisor to look for new accounts now rather than at the
// next sweep.
//
// # Why this exists on top of the sweep
//
// Because a human is waiting. A mailbox created through the accounts API is
// usable the moment the call returns 201 (contract §2.4), and the organizer who
// created it opens the webmail seconds later — the gate criterion is "operational
// in under 60 s". The sweep alone bounds the worst case at DiscoveryInterval;
// the nudge collapses the common case to the time one sweep takes.
//
// # Why it is not the mechanism
//
// Because it can be missed, and a signal that can be missed is not a guarantee.
// It is deliberately non-blocking: if a nudge is already pending this one is
// coalesced into it, because two sweeps back to back find the same rows. And it
// only reaches the process it was called in, so an account created by any other
// path — moovctl, an operator's SQL, a second daemon, a create whose caller
// died before nudging — is adopted by the ticker or not at all. The ticker is
// the contract; this is latency.
//
// Safe from any goroutine, and safe before Run starts: the buffered slot means
// an early nudge is simply consumed by the first iteration of the loop.
func (s *Supervisor) Nudge() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// superviseAccount runs one account's initial sync (if needed) and then its
// watcher.
//
// releaseSlot gives back this account's concurrency slot. It is called exactly
// once, as soon as the account stops doing initial-sync work — whether that is
// success, a context end, or entering the retry wait. It is deliberately NOT
// deferred to the end of this function: the function does not end until the
// watcher dies, and holding a slot for that long strands every account past the
// limit (see the call site).
//
// The retry wait releases too, because it is minutes long by design
// (DefaultRetryDelay): an account whose credentials are wrong must not hold a
// slot that a healthy account could use. It re-acquires nothing on the way
// back, which means a retrying account is no longer bounded by Concurrency —
// the right trade, since the retry path is rate-limited by RetryDelay and by
// the per-account breaker, and the bound exists to protect Dovecot from a
// thundering herd of INITIAL syncs, not from one slow retry loop.
func (s *Supervisor) superviseAccount(ctx context.Context, account store.Account, releaseSlot func()) {
	log := s.log.With("account_id", account.ID, "email", account.Email)

	var once sync.Once
	release := func() { once.Do(releaseSlot) }
	defer release()

	s.observe(account, ObsSupervised)

	for {
		if err := ctx.Err(); err != nil {
			return
		}

		err := s.syncOnce(ctx, account, log)
		switch {
		case err == nil:
			s.observe(account, ObsInitialSynced)
			release()
			s.runWatcher(ctx, account, log)
			return
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return
		}

		log.Error("initial sync failed; will retry", "error", err, "retry_in", s.opts.RetryDelay)
		s.observe(account, ObsInitialSyncFailed)
		release()
		select {
		case <-time.After(s.opts.RetryDelay):
		case <-ctx.Done():
			return
		}
	}
}

// observe reports one supervision fact, if anyone is listening.
//
// It is a method rather than a direct call so that a nil observer — the normal
// case in tests and in any deployment without metrics — costs one nil check
// instead of a guard at every call site.
func (s *Supervisor) observe(account store.Account, kind SupervisionKind) {
	if s.opts.OnAccount == nil {
		return
	}
	s.opts.OnAccount(SupervisionObservation{
		AccountID: account.ID,
		Email:     account.Email,
		Kind:      kind,
	})
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
