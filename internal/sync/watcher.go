package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// The push watcher (L2 §2.5, S2 T2d/T4).
//
// # One connection per account, not one per folder
//
// A 40-folder mailbox watched with one IDLE per folder is 40 sockets against a
// Mailcow with fail2ban watching (ADR §4). NOTIFY collapses that: S2 T2d proved
// a single connection watching the personal namespace received events for every
// folder. That measurement is the reason this type holds exactly one connection
// and the reason it must never grow a per-mailbox loop.
//
// # Why the follow-up fetch is not optional
//
// Dovecot rejects the NOTIFY MessageNew (fetch-att) form (S2 T2d), and for a
// non-selected mailbox it collapses every event class into one STATUS. So an
// event says "this mailbox changed" and nothing more: the watcher's answer is
// always an incremental pass against its own stored cursor, never an attempt to
// apply the notification's contents. That is what makes a dropped event
// harmless — the next event, or the reconciler, re-derives the same delta.
//
// # Why events are debounced
//
// A folder receiving a burst — a mailing list delivery, a client marking twenty
// messages read — produces one notification per change. Answering each with its
// own SELECT and FETCH would turn one user action into twenty round trips, and
// the twentieth pass would find nothing the first had not already fetched, since
// each pass reads the current state rather than a queued diff. Coalescing a
// burst into one pass is therefore free correctness-wise and is the difference
// between a watcher and a hammer.

// Watcher options and their defaults.
const (
	// DefaultDebounce is how long the watcher waits for a burst to settle
	// before running an incremental pass for a mailbox.
	//
	// 150 ms is chosen against the acceptance criterion, not for comfort: the
	// AC is "NOTIFY event → message visible in the store in under a second",
	// and the pass itself (SELECT + FETCH + parse + insert) is the rest of that
	// budget. Long enough to absorb a burst, short enough to be invisible.
	DefaultDebounce = 150 * time.Millisecond

	// DefaultMaxDebounce bounds how long coalescing may postpone a pass when
	// events keep arriving. Without it, a folder under continuous change would
	// have its pass deferred indefinitely — the classic debounce starvation
	// bug, and here it would mean mail that never appears.
	DefaultMaxDebounce = 2 * time.Second

	// DefaultReconcileInterval is the defensive STATUS sweep period (L2 §2.5:
	// "configurable, default 6 h").
	DefaultReconcileInterval = 6 * time.Hour

	// DefaultBackoffMin and DefaultBackoffMax bound the reconnection backoff.
	//
	// The maximum is minutes rather than seconds because the failures that get
	// this far are not transient, and because reconnecting fast against a
	// server that is refusing us is precisely what trips fail2ban (ADR §4).
	DefaultBackoffMin = 2 * time.Second
	DefaultBackoffMax = 5 * time.Minute

	// DefaultBreakerThreshold is how many consecutive failures open the
	// per-account circuit breaker.
	//
	// Five, not one: a single failure is a network hiccup and tripping on it
	// would stop syncing an account every time a container restarted. Five
	// consecutive failures, each already spaced by the backoff, is minutes of
	// sustained failure — a real condition, not a blip.
	DefaultBreakerThreshold = 5

	// DefaultBreakerCooldown is how long an open breaker stays open before the
	// watcher tries again (half-open).
	DefaultBreakerCooldown = 15 * time.Minute
)

// WatcherOptions configures the push watcher.
type WatcherOptions struct {
	// Options is the sync configuration the incremental passes run under.
	Options Options

	// Connector opens the watcher's IMAP connections. Required.
	Connector Connector

	// Debounce is how long a burst of events for one mailbox is coalesced.
	// Default DefaultDebounce.
	Debounce time.Duration

	// MaxDebounce bounds coalescing. Default DefaultMaxDebounce.
	MaxDebounce time.Duration

	// ReconcileInterval is the defensive STATUS sweep period. Default
	// DefaultReconcileInterval. Negative disables the reconciler, which is a
	// thing only a test should do.
	ReconcileInterval time.Duration

	// BackoffMin and BackoffMax bound the reconnection backoff. Defaults
	// DefaultBackoffMin / DefaultBackoffMax.
	BackoffMin time.Duration
	BackoffMax time.Duration

	// BreakerThreshold is how many consecutive failures open the breaker.
	// Default DefaultBreakerThreshold.
	BreakerThreshold int

	// BreakerCooldown is how long the breaker stays open. Default
	// DefaultBreakerCooldown.
	BreakerCooldown time.Duration

	// OnEvent, when set, is called for every watcher observation. It exists for
	// tests and for E8's metrics; it must not block.
	OnEvent func(WatchObservation)
}

// WatchObservation is one thing the watcher did, reported to OnEvent.
type WatchObservation struct {
	AccountID int64
	Kind      WatchObservationKind
	Mailbox   string
	Result    IncrementalResult
	Err       error
}

// WatchObservationKind classifies a WatchObservation.
type WatchObservationKind string

// The observation kinds.
const (
	// ObsConnected means the watcher established its NOTIFY+IDLE connection.
	ObsConnected WatchObservationKind = "connected"
	// ObsPass means an incremental pass ran for one mailbox.
	ObsPass WatchObservationKind = "pass"
	// ObsOverflow means NOTIFICATIONOVERFLOW forced a full account resync.
	ObsOverflow WatchObservationKind = "overflow"
	// ObsReconciled means the defensive sweep repaired a divergence.
	ObsReconciled WatchObservationKind = "reconciled"
	// ObsBreakerOpen means the circuit breaker tripped.
	ObsBreakerOpen WatchObservationKind = "breaker-open"
	// ObsDisconnected means the watcher's connection ended and it will retry.
	ObsDisconnected WatchObservationKind = "disconnected"
)

// withDefaults returns a copy with every zero field filled in.
func (o WatcherOptions) withDefaults() WatcherOptions {
	o.Options = o.Options.withDefaults()
	if o.Debounce <= 0 {
		o.Debounce = DefaultDebounce
	}
	if o.MaxDebounce <= 0 {
		o.MaxDebounce = DefaultMaxDebounce
	}
	if o.MaxDebounce < o.Debounce {
		o.MaxDebounce = o.Debounce
	}
	if o.ReconcileInterval == 0 {
		o.ReconcileInterval = DefaultReconcileInterval
	}
	if o.BackoffMin <= 0 {
		o.BackoffMin = DefaultBackoffMin
	}
	if o.BackoffMax <= 0 {
		o.BackoffMax = DefaultBackoffMax
	}
	if o.BackoffMax < o.BackoffMin {
		o.BackoffMax = o.BackoffMin
	}
	if o.BreakerThreshold <= 0 {
		o.BreakerThreshold = DefaultBreakerThreshold
	}
	if o.BreakerCooldown <= 0 {
		o.BreakerCooldown = DefaultBreakerCooldown
	}
	return o
}

// PushWatcher is E6's implementation of the supervisor's Watcher seam.
//
// One instance serves every account: the supervisor calls Watch once per synced
// account, each in its own goroutine, and each call owns its account's
// connections for as long as it runs.
type PushWatcher struct {
	store *store.Store
	blobs BlobPutter
	opts  WatcherOptions
	log   *slog.Logger
}

// NewPushWatcher builds the watcher.
func NewPushWatcher(st *store.Store, blobs BlobPutter, opts WatcherOptions) (*PushWatcher, error) {
	if st == nil {
		return nil, errors.New("sync: a store is required")
	}
	if blobs == nil {
		return nil, errors.New("sync: a blob store is required")
	}
	if opts.Connector == nil {
		return nil, errors.New("sync: a Connector is required")
	}

	opts = opts.withDefaults()
	return &PushWatcher{
		store: st,
		blobs: blobs,
		opts:  opts,
		log:   opts.Options.Logger.With("component", "sync-watcher"),
	}, nil
}

// Watch implements the Watcher seam. It runs until ctx ends.
//
// # The loop's shape
//
// Connect, watch until something breaks, back off, connect again. The breaker
// sits around that loop rather than inside it: it counts consecutive failures
// of the whole cycle, so a watcher that connects successfully and then loses
// the connection an hour later starts from a clean count, while one that cannot
// connect at all trips and stops trying.
func (w *PushWatcher) Watch(ctx context.Context, account store.Account) error {
	log := w.log.With("account_id", account.ID, "email", account.Email)

	state, err := w.loadBreaker(ctx, account.ID)
	if err != nil {
		log.Warn("reading breaker state; assuming closed", "error", err)
	}

	backoff := w.opts.BackoffMin
	failures := state.consecutive

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// An open breaker is honored before any socket is opened: that is the
		// whole point of it existing (ADR §4).
		//
		// The deadline arithmetic uses the real clock for the same reason the
		// debouncer does: Options.Clock is often pinned to a fixed instant, and
		// a cooldown measured against a frozen clock never expires — an account
		// whose breaker opened once would never be retried again.
		if wait := state.remaining(time.Now()); wait > 0 {
			log.Warn("circuit breaker is open; not connecting", "retry_in", wait.Round(time.Second))
			if !sleepCtx(ctx, wait) {
				return ctx.Err()
			}
			// Half-open: one attempt is allowed through. A success closes the
			// breaker, a failure re-opens it for another cooldown.
			if err := w.store.SetBreakerState(ctx, account.ID, store.AccountScope,
				store.BreakerHalfOpen, nil); err != nil {
				log.Warn("recording half-open breaker", "error", err)
			}
			state = breakerState{}
		}

		runErr := w.runOnce(ctx, account, log)

		switch {
		case runErr == nil, errors.Is(runErr, context.Canceled):
			return ctx.Err()
		case errors.Is(runErr, context.DeadlineExceeded):
			return runErr
		}

		failures++
		w.emit(WatchObservation{AccountID: account.ID, Kind: ObsDisconnected, Err: runErr})

		count, rerr := w.store.RecordSyncError(ctx, account.ID, store.AccountScope,
			fmt.Sprintf("watcher: %v", runErr))
		if rerr != nil {
			log.Warn("recording watcher error", "error", rerr)
		} else if count > failures {
			// The stored count includes failures from before this process
			// started, which is exactly what makes the breaker survive a
			// restart instead of giving a broken account a fresh five attempts
			// on every deploy.
			failures = count
		}

		if failures >= w.opts.BreakerThreshold {
			until := time.Now().Add(w.opts.BreakerCooldown)
			if err := w.store.SetBreakerState(ctx, account.ID, store.AccountScope,
				store.BreakerOpen, &until); err != nil {
				log.Error("opening circuit breaker", "error", err)
			}
			log.Error("circuit breaker opened after consecutive failures",
				"failures", failures, "cooldown", w.opts.BreakerCooldown, "error", runErr)
			w.emit(WatchObservation{AccountID: account.ID, Kind: ObsBreakerOpen, Err: runErr})

			state = breakerState{open: true, until: until}
			failures = 0
			backoff = w.opts.BackoffMin
			continue
		}

		delay := jitter(backoff)
		log.Warn("watcher connection ended; retrying",
			"error", runErr, "failures", failures, "retry_in", delay.Round(time.Millisecond))
		if !sleepCtx(ctx, delay) {
			return ctx.Err()
		}
		backoff = nextBackoff(backoff, w.opts.BackoffMax)
	}
}

// runOnce holds one connection for as long as it lives: it opens the watch,
// starts the reconciler, and dispatches events until something ends.
//
// It returns nil only when ctx ended, which is the clean-shutdown path. Any
// other return is a failure the caller counts against the breaker.
func (w *PushWatcher) runOnce(ctx context.Context, account store.Account, log *slog.Logger) error {
	// Two connections: one is pinned to NOTIFY+IDLE for the whole session and
	// cannot issue any other command (imap.Client's contract), so the
	// incremental passes need one of their own. This is the "watcher + N
	// workers" budget of L2 §2.5, with N=1 — deliberately small, because a
	// mailbox is not read faster through more sockets and every socket is one
	// more thing fail2ban counts.
	clients, err := w.opts.Connector.Connect(ctx, account, watcherConnections)
	if err != nil {
		return fmt.Errorf("connecting watcher: %w", err)
	}
	defer func() {
		for _, c := range clients {
			if cerr := c.Close(); cerr != nil {
				log.Debug("closing watcher connection", "error", cerr)
			}
		}
	}()
	if len(clients) < watcherConnections {
		return fmt.Errorf("watcher needs %d connections, got %d", watcherConnections, len(clients))
	}

	watchConn := clients[0]
	syncer, err := New(w.store, w.blobs, clients[1:], w.opts.Options)
	if err != nil {
		return err
	}

	// The watch's context is a child, so ending the session (a reconnect, a
	// failed pass) tears down the IDLE loop and the reconciler together instead
	// of leaving either running against a connection nobody owns.
	sessionCtx, endSession := context.WithCancel(ctx)
	defer endSession()

	events, err := watchConn.Watch(sessionCtx, imap.WatchSpec{})
	if err != nil {
		return fmt.Errorf("starting NOTIFY watch: %w", err)
	}
	log.Info("watcher connected", "notify", true)
	w.emit(WatchObservation{AccountID: account.ID, Kind: ObsConnected})

	// A pass over every mailbox at connection time is not optional: whatever
	// changed while the watcher was down produced no event anybody heard, and
	// without this the account would sit stale until the first new change or
	// the reconciler's next sweep — which is up to six hours (L2 §2.5).
	if err := w.sweepAll(sessionCtx, syncer, account, log, "reconnect"); err != nil {
		return err
	}

	var wg sync.WaitGroup
	reconcileErr := make(chan error, 1)
	if w.opts.ReconcileInterval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.runReconciler(sessionCtx, syncer, account, log); err != nil &&
				!errors.Is(err, context.Canceled) {
				select {
				case reconcileErr <- err:
				default:
				}
				endSession()
			}
		}()
	}
	defer wg.Wait()

	err = w.dispatch(sessionCtx, syncer, account, events, log)

	select {
	case rerr := <-reconcileErr:
		// A reconciler failure is the real cause; the dispatch loop only ended
		// because the reconciler canceled the session.
		if err == nil || errors.Is(err, context.Canceled) {
			return rerr
		}
	default:
	}
	return err
}

// watcherConnections is the per-account socket budget of a watching account:
// one pinned to NOTIFY+IDLE, one for the incremental passes it triggers.
const watcherConnections = 2

// dispatch is the event loop: it coalesces notifications per mailbox and runs
// one incremental pass per settled burst.
//
// # Why the debounce is per mailbox and not global
//
// Two folders changing at once are two independent passes against two different
// cursors; making them wait for each other would delay the one a user is
// looking at because an archive folder happened to receive a message. The timer
// is therefore per mailbox, and a pass for one folder never blocks another's.
func (w *PushWatcher) dispatch(
	ctx context.Context,
	syncer *Syncer,
	account store.Account,
	events <-chan imap.Event,
	log *slog.Logger,
) error {
	// time.Now, NOT Options.Clock.
	//
	// Options.Clock exists so a test can pin the 30-day recent window to a
	// fixed instant, and it is therefore frequently a CONSTANT function. A
	// debouncer driven by a constant clock has deadlines that never arrive:
	// every event would be coalesced forever and no pass would ever run. The
	// debounce measures elapsed wall time between notifications, which is a
	// different quantity from "what date does the engine think it is", so it
	// takes the real clock and the two concerns stop sharing a dial.
	d := newDebouncer(w.opts.Debounce, w.opts.MaxDebounce, time.Now)
	defer d.stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case ev, ok := <-events:
			if !ok {
				// The channel closes when the watch ends: cancellation, a
				// closed client, or a broken connection. Only the first is
				// clean, and ctx says which this was.
				if err := ctx.Err(); err != nil {
					return err
				}
				return errors.New("watch ended: the IMAP connection closed")
			}

			switch ev.Kind {
			case imap.EventOverflow:
				// The server abandoned its tracking, so nothing the watcher
				// believes is trustworthy. A full account sweep is the only
				// answer (L2 §2.5) — and it is a sweep rather than a rebuild,
				// because the per-mailbox cursors are still valid: what
				// overflowed is the notification queue, not UIDVALIDITY.
				log.Warn("NOTIFICATIONOVERFLOW; resyncing the whole account")
				w.emit(WatchObservation{AccountID: account.ID, Kind: ObsOverflow})
				d.reset()
				if err := w.sweepAll(ctx, syncer, account, log, "overflow"); err != nil {
					return err
				}

			case imap.EventMailboxChanged:
				if ev.Mailbox == "" {
					// A change the server could not attribute to a folder.
					// Sweeping everything is heavy-handed but correct, and it
					// does not happen in normal operation.
					d.reset()
					if err := w.sweepAll(ctx, syncer, account, log, "unattributed-event"); err != nil {
						return err
					}
					continue
				}
				d.touch(ev.Mailbox)

			default:
				log.Debug("ignoring unknown watch event", "kind", ev.Kind)
			}

		case <-d.ready():
			for _, mailbox := range d.take() {
				if err := w.passOne(ctx, syncer, account, mailbox, log); err != nil {
					return err
				}
			}
		}
	}
}

// passOne runs one incremental pass for one mailbox by name.
//
// A mailbox the store does not know is not an error: NOTIFY reports folders
// created by other clients, and the correct response is to discover it, which
// the reconciler's sweep does. Failing the watcher over a folder it has never
// seen would take down the whole account's push for a mailbox that may not even
// be selectable.
func (w *PushWatcher) passOne(
	ctx context.Context,
	syncer *Syncer,
	account store.Account,
	mailbox string,
	log *slog.Logger,
) error {
	row, err := w.store.GetMailboxByName(ctx, account.ID, mailbox)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			log.Info("event for an unknown mailbox; the reconciler will discover it",
				"mailbox", mailbox)
			return nil
		}
		return fmt.Errorf("looking up mailbox %q: %w", mailbox, err)
	}
	if !row.Selectable {
		return nil
	}

	mb := syncMailbox{row: row, info: imap.MailboxInfo{Name: row.Name, Role: imap.MailboxRole(row.Role)}}

	var res IncrementalResult
	err = syncer.conns.withConn(ctx, func(c imap.Client) error {
		var perr error
		res, perr = syncer.incrementalMailbox(ctx, c, account, mb, log)
		return perr
	})
	if errors.Is(err, errMailboxNeedsInitialSync) {
		// A mailbox that has never been selected has no delta to ask for. The
		// initial-sync machinery owns it, and running it here is what makes a
		// folder created after the account was synced become usable without
		// waiting for a restart.
		log.Info("mailbox has no cursor; running initial backfill", "mailbox", mailbox)
		if _, berr := syncer.backfillMailbox(ctx, account, mb, log); berr != nil {
			return berr
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("incremental pass on %q: %w", mailbox, err)
	}

	w.emit(WatchObservation{
		AccountID: account.ID, Kind: ObsPass, Mailbox: mailbox, Result: res,
	})
	return nil
}

// sweepAll runs an incremental pass over every known mailbox of the account,
// discovering new folders first.
//
// It is the answer to every condition where per-mailbox event state cannot be
// trusted: reconnection, overflow, and the reconciler finding a divergence.
func (w *PushWatcher) sweepAll(
	ctx context.Context,
	syncer *Syncer,
	account store.Account,
	log *slog.Logger,
	reason string,
) error {
	started := time.Now()

	// Discovery first: a folder created while the watcher was down exists on
	// the server and nowhere locally, and no per-mailbox pass would ever find
	// it. discover also upserts, so this is what makes the tree current.
	boxes, err := syncer.discover(ctx, account, log)
	if err != nil {
		return fmt.Errorf("sweep %s: %w", reason, err)
	}

	var changed int
	for i := range boxes {
		if err := ctx.Err(); err != nil {
			return err
		}
		mb := boxes[i]

		var res IncrementalResult
		perr := syncer.conns.withConn(ctx, func(c imap.Client) error {
			var e error
			res, e = syncer.incrementalMailbox(ctx, c, account, mb, log)
			return e
		})
		if errors.Is(perr, errMailboxNeedsInitialSync) {
			if _, berr := syncer.backfillMailbox(ctx, account, mb, log); berr != nil {
				return fmt.Errorf("sweep %s: backfilling %q: %w", reason, mb.info.Name, berr)
			}
			changed++
			continue
		}
		if perr != nil {
			return fmt.Errorf("sweep %s: %q: %w", reason, mb.info.Name, perr)
		}
		if res.Changed() {
			changed++
		}
		w.emit(WatchObservation{
			AccountID: account.ID, Kind: ObsPass, Mailbox: mb.info.Name, Result: res,
		})
	}

	log.Info("account sweep finished",
		"reason", reason, "mailboxes", len(boxes), "changed", changed,
		"elapsed", time.Since(started).Round(time.Millisecond))
	return nil
}

// emit reports an observation, if anybody is listening.
func (w *PushWatcher) emit(obs WatchObservation) {
	if w.opts.OnEvent != nil {
		w.opts.OnEvent(obs)
	}
}

// breakerState is the persisted circuit breaker as this package reads it.
type breakerState struct {
	open        bool
	until       time.Time
	consecutive int
}

// remaining reports how long an open breaker still blocks connecting.
func (b breakerState) remaining(now time.Time) time.Duration {
	if !b.open || b.until.IsZero() {
		return 0
	}
	if d := b.until.Sub(now); d > 0 {
		return d
	}
	return 0
}

// loadBreaker reads the persisted breaker so a restarted process does not give
// a failing account a fresh budget of attempts.
func (w *PushWatcher) loadBreaker(ctx context.Context, accountID int64) (breakerState, error) {
	cp, err := w.store.GetCheckpoint(ctx, accountID, store.AccountScope)
	if err != nil {
		return breakerState{}, err
	}
	out := breakerState{consecutive: cp.ConsecutiveErrors}
	if cp.BreakerState == store.BreakerOpen {
		out.open = true
		if cp.BreakerUntil != nil {
			out.until = *cp.BreakerUntil
		}
	}
	return out, nil
}

// nextBackoff doubles the delay up to the ceiling.
func nextBackoff(current, maximum time.Duration) time.Duration {
	next := current * 2
	if next > maximum || next <= 0 {
		return maximum
	}
	return next
}

// jitter spreads a delay over [d/2, d).
//
// It matters more here than in a typical retry loop: an installation-wide
// event — the mail server restarting, the Docker network flapping — fails every
// account's watcher at the same instant, and without jitter all of them would
// reconnect in the same millisecond, which is a self-inflicted thundering herd
// against exactly the server that just proved it was struggling. And a burst of
// simultaneous connections is what fail2ban is looking for (ADR §4).
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1)) //nolint:gosec // spreading retries, not a security decision
}

// sleepCtx waits for d or until ctx ends, reporting whether the wait completed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
