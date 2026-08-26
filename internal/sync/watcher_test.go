package sync

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// The push watcher's behaviors: event → pass, overflow → resync, failure →
// backoff → breaker, and the reconciler catching what push missed.

// watcherHarness is a synced account with a watcher running against the fake
// server.
type watcherHarness struct {
	*syncedEnv

	watcher *PushWatcher
	cancel  context.CancelFunc
	done    chan error

	mu   sync.Mutex
	obs  []WatchObservation
	seen map[WatchObservationKind]int
}

// startWatcher builds and starts a watcher over an already-synced account.
func startWatcher(t *testing.T, env *syncedEnv, tune func(*WatcherOptions)) *watcherHarness {
	t.Helper()

	h := &watcherHarness{
		syncedEnv: env,
		done:      make(chan error, 1),
		seen:      map[WatchObservationKind]int{},
	}

	opts := WatcherOptions{
		Options: env.opts,
		Connector: ConnectorFunc(func(context.Context, store.Account, int) ([]imap.Client, error) {
			env.srv.mu.Lock()
			cerr := env.srv.connectErr
			env.srv.mu.Unlock()
			if cerr != nil {
				return nil, cerr
			}
			return env.srv.clients(watcherConnections), nil
		}),
		// Short, so the tests are fast; the behavior under test is unaffected
		// by the absolute values.
		Debounce:          20 * time.Millisecond,
		MaxDebounce:       100 * time.Millisecond,
		ReconcileInterval: -1, // off unless a test turns it on
		BackoffMin:        5 * time.Millisecond,
		BackoffMax:        20 * time.Millisecond,
		BreakerThreshold:  DefaultBreakerThreshold,
		BreakerCooldown:   50 * time.Millisecond,
		OnEvent: func(obs WatchObservation) {
			h.mu.Lock()
			h.obs = append(h.obs, obs)
			h.seen[obs.Kind]++
			h.mu.Unlock()
		},
	}
	if tune != nil {
		tune(&opts)
	}

	w, err := NewPushWatcher(env.store, env.blobs, opts)
	if err != nil {
		t.Fatalf("NewPushWatcher: %v", err)
	}
	h.watcher = w

	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(10 * time.Second):
			t.Error("the watcher did not stop within 10s of cancellation")
		}
	})

	go func() { h.done <- w.Watch(ctx, env.account) }()

	// The watcher sweeps every mailbox at connection time, so waiting for the
	// connection makes the tests deterministic: anything a test does afterwards
	// is genuinely "while the watcher is live".
	h.waitFor(t, ObsConnected, 1, "the watcher never connected")
	return h
}

// count returns how many observations of a kind have arrived.
func (h *watcherHarness) count(kind WatchObservationKind) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seen[kind]
}

// waitFor blocks until a kind has been observed at least n times.
func (h *watcherHarness) waitFor(t *testing.T, kind WatchObservationKind, n int, msg string) {
	t.Helper()
	waitFor(t, 20*time.Second, func() bool { return h.count(kind) >= n }, msg)
}

// TestWatcherAppliesADeliveryFromAnEvent is the acceptance criterion in its
// unit form: a message appears on the server, and the watcher makes it visible
// in the store without anybody asking.
func TestWatcherAppliesADeliveryFromAnEvent(t *testing.T) {
	env := newSyncedEnv(t, 5)
	h := startWatcher(t, env, nil)

	before := len(env.liveUIDs(t, "INBOX"))

	uid := env.srv.deliver("INBOX",
		buildMessage(700, "Pushed", referenceNow, "Delivered while watching."),
		nil, referenceNow)

	waitFor(t, 20*time.Second, func() bool {
		for _, u := range env.liveUIDs(t, "INBOX") {
			if imap.UID(u) == uid {
				return true
			}
		}
		return false
	}, "the delivered message never reached the store")

	if got := len(env.liveUIDs(t, "INBOX")); got != before+1 {
		t.Errorf("the mailbox holds %d messages, want %d", got, before+1)
	}
	_ = h
}

// TestWatcherAppliesAFlagChangeFromAnEvent covers the case the NOTIFY patch
// exists for.
//
// A \Flagged toggle moves neither MESSAGES nor UNSEEN, so HIGHESTMODSEQ is the
// only counter that changes — and stock go-imap, which cannot emit the STATUS
// keyword, produces no notification at all for it (S2 T4). Without the patch
// this test would hang.
func TestWatcherAppliesAFlagChangeFromAnEvent(t *testing.T) {
	env := newSyncedEnv(t, 5)
	startWatcher(t, env, nil)

	env.srv.setFlags("INBOX", 2, []string{"seen", "flagged"}, nil)

	waitFor(t, 20*time.Second, func() bool {
		flags, ok := env.flagsOf(t, "INBOX", 2)
		return ok && flags.Has(store.FlagFlagged)
	}, "the flag change never reached the store")
}

// TestWatcherCoalescesABurstIntoOnePass proves the debounce is wired to the
// dispatch loop, not merely implemented.
//
// Twenty deliveries produce twenty notifications; answering each with its own
// SELECT and FETCH would be twenty round trips where one suffices, and the
// twentieth would find nothing the first had not already fetched.
func TestWatcherCoalescesABurstIntoOnePass(t *testing.T) {
	env := newSyncedEnv(t, 3)
	h := startWatcher(t, env, func(o *WatcherOptions) {
		o.Debounce = 120 * time.Millisecond
		o.MaxDebounce = 2 * time.Second
	})

	passesBefore := h.count(ObsPass)

	const burst = 20
	for i := range burst {
		env.srv.deliver("INBOX",
			buildMessage(800+i, "Burst", referenceNow, "Burst body."), nil, referenceNow)
		time.Sleep(2 * time.Millisecond)
	}

	waitFor(t, 20*time.Second, func() bool {
		return len(env.liveUIDs(t, "INBOX")) == 3+burst
	}, "the burst never fully reached the store")

	// Let any straggler pass land before counting.
	time.Sleep(400 * time.Millisecond)

	passes := h.count(ObsPass) - passesBefore
	if passes == 0 {
		t.Fatal("no pass ran for the burst")
	}
	if passes > 5 {
		t.Errorf("the burst of %d events produced %d passes; the debounce is not coalescing",
			burst, passes)
	}
	t.Logf("%d events coalesced into %d passes", burst, passes)
}

// TestWatcherResyncsOnOverflow covers NOTIFICATIONOVERFLOW: the server gave up
// tracking, so nothing the watcher believes is trustworthy and only a full
// account sweep restores the invariant (L2 §2.5).
//
// The test makes the changes SILENTLY first — so no per-mailbox event could
// have applied them — and only then sends the overflow. Anything that lands
// afterwards can only have come from the sweep.
func TestWatcherResyncsOnOverflow(t *testing.T) {
	env := newSyncedEnv(t, 4)
	h := startWatcher(t, env, nil)

	env.srv.setSilentNotify(true)
	env.srv.deliver("INBOX",
		buildMessage(850, "Missed by push", referenceNow, "Body."), nil, referenceNow)
	env.srv.setFlags("INBOX", 1, []string{"seen", "flagged"}, nil)
	env.srv.setSilentNotify(false)

	// Nothing should have moved: the mutations produced no events.
	time.Sleep(150 * time.Millisecond)
	if got := len(env.liveUIDs(t, "INBOX")); got != 4 {
		t.Fatalf("the silent delivery reached the store without an event (%d messages)", got)
	}

	env.srv.overflow()

	h.waitFor(t, ObsOverflow, 1, "the overflow event was never observed")
	waitFor(t, 20*time.Second, func() bool {
		return len(env.liveUIDs(t, "INBOX")) == 5
	}, "the overflow resync did not pick up the silently delivered message")

	flags, ok := env.flagsOf(t, "INBOX", 1)
	if !ok || !flags.Has(store.FlagFlagged) {
		t.Errorf("the overflow resync did not pick up the silent flag change (flags=%v)", flags)
	}
}

// TestWatcherDiscoversAMailboxCreatedWhileWatching checks that a folder created
// by another client is picked up rather than ignored until a restart.
func TestWatcherDiscoversAMailboxCreatedWhileWatching(t *testing.T) {
	env := newSyncedEnv(t, 3)
	startWatcher(t, env, nil)

	// A new folder with a message in it, announced by an overflow — which is
	// the coarse signal that forces a rediscovery.
	mb := env.srv.addMailbox("Projects", imap.RoleNone, 200)
	seedMailboxLocked(env.srv, "Projects", 4, referenceNow, "Project")
	_ = mb

	env.srv.overflow()

	waitFor(t, 20*time.Second, func() bool {
		return len(env.liveUIDs(t, "Projects")) == 4
	}, "the new mailbox was never discovered and backfilled")
}

// TestWatcherReconnectsAfterTheConnectionDrops proves the retry loop works and
// that a reconnection sweeps — which is what stops an account being stale for
// up to the reconciler's whole interval after a blip.
func TestWatcherReconnectsAfterTheConnectionDrops(t *testing.T) {
	env := newSyncedEnv(t, 3)
	h := startWatcher(t, env, nil)

	// The connection dies while a change happens that nobody hears.
	env.srv.setSilentNotify(true)
	env.srv.deliver("INBOX",
		buildMessage(860, "Arrived while disconnected", referenceNow, "Body."), nil, referenceNow)
	env.srv.setSilentNotify(false)
	env.srv.breakWatchers()

	h.waitFor(t, ObsDisconnected, 1, "the dropped connection was never observed")
	h.waitFor(t, ObsConnected, 2, "the watcher never reconnected")

	// The reconnection sweep is what finds the message: no event was ever sent
	// for it.
	waitFor(t, 20*time.Second, func() bool {
		return len(env.liveUIDs(t, "INBOX")) == 4
	}, "the reconnection sweep did not pick up the change made while disconnected")
}

// TestWatcherOpensTheBreakerAfterRepeatedFailures is the fail2ban guard
// (ADR §4): an account that cannot connect must stop trying, not try faster.
func TestWatcherOpensTheBreakerAfterRepeatedFailures(t *testing.T) {
	env := newSyncedEnv(t, 2)

	env.srv.mu.Lock()
	env.srv.connectErr = errors.New("simulated connection refused")
	env.srv.mu.Unlock()

	h := &watcherHarness{syncedEnv: env, done: make(chan error, 1), seen: map[WatchObservationKind]int{}}
	w, err := NewPushWatcher(env.store, env.blobs, WatcherOptions{
		Options: env.opts,
		Connector: ConnectorFunc(func(context.Context, store.Account, int) ([]imap.Client, error) {
			env.srv.mu.Lock()
			cerr := env.srv.connectErr
			env.srv.mu.Unlock()
			if cerr != nil {
				return nil, cerr
			}
			return env.srv.clients(watcherConnections), nil
		}),
		ReconcileInterval: -1,
		BackoffMin:        time.Millisecond,
		BackoffMax:        2 * time.Millisecond,
		BreakerThreshold:  3,
		BreakerCooldown:   30 * time.Second,
		OnEvent: func(obs WatchObservation) {
			h.mu.Lock()
			h.obs = append(h.obs, obs)
			h.seen[obs.Kind]++
			h.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewPushWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { h.done <- w.Watch(ctx, env.account) }()

	h.waitFor(t, ObsBreakerOpen, 1, "the breaker never opened despite repeated failures")

	// The breaker must be PERSISTED, not merely in memory: a restarted process
	// has to honor it, or every deploy would give a broken account a fresh
	// budget of failed logins.
	cp, err := env.store.GetCheckpoint(context.Background(), env.account.ID, store.AccountScope)
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if cp.BreakerState != store.BreakerOpen {
		t.Errorf("the persisted breaker state is %q, want %q", cp.BreakerState, store.BreakerOpen)
	}
	if cp.BreakerUntil == nil {
		t.Error("the persisted breaker has no cooldown deadline")
	}
	if cp.LastError == "" {
		t.Error("the failure was not recorded for an operator to see")
	}

	// And once open, it stops connecting: the whole point.
	attemptsAtOpen := h.count(ObsDisconnected)
	time.Sleep(200 * time.Millisecond)
	if extra := h.count(ObsDisconnected) - attemptsAtOpen; extra > 0 {
		t.Errorf("the watcher made %d more connection attempts after the breaker opened, want 0", extra)
	}

	cancel()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		t.Error("the watcher did not stop after cancellation")
	}
}

// TestWatcherRecoversWhenTheBreakerHalfOpens proves the breaker is a pause, not
// a death sentence: an account whose server comes back must resume.
func TestWatcherRecoversWhenTheBreakerHalfOpens(t *testing.T) {
	env := newSyncedEnv(t, 2)

	env.srv.mu.Lock()
	env.srv.connectErr = errors.New("simulated outage")
	env.srv.mu.Unlock()

	h := &watcherHarness{syncedEnv: env, done: make(chan error, 1), seen: map[WatchObservationKind]int{}}
	w, err := NewPushWatcher(env.store, env.blobs, WatcherOptions{
		Options: env.opts,
		Connector: ConnectorFunc(func(context.Context, store.Account, int) ([]imap.Client, error) {
			env.srv.mu.Lock()
			cerr := env.srv.connectErr
			env.srv.mu.Unlock()
			if cerr != nil {
				return nil, cerr
			}
			return env.srv.clients(watcherConnections), nil
		}),
		ReconcileInterval: -1,
		BackoffMin:        time.Millisecond,
		BackoffMax:        2 * time.Millisecond,
		BreakerThreshold:  2,
		BreakerCooldown:   150 * time.Millisecond,
		OnEvent: func(obs WatchObservation) {
			h.mu.Lock()
			h.obs = append(h.obs, obs)
			h.seen[obs.Kind]++
			h.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewPushWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { h.done <- w.Watch(ctx, env.account) }()

	h.waitFor(t, ObsBreakerOpen, 1, "the breaker never opened")

	// The server comes back during the cooldown.
	env.srv.mu.Lock()
	env.srv.connectErr = nil
	env.srv.mu.Unlock()

	h.waitFor(t, ObsConnected, 1, "the watcher never reconnected after the breaker's cooldown")

	cancel()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		t.Error("the watcher did not stop after cancellation")
	}
}

// TestWatcherUsesOneWatchConnectionPerAccount is the architectural claim of
// S2 T2d, asserted rather than assumed.
//
// A watcher that grew a per-folder loop would still pass every behavioral test
// above while opening one socket per folder — and Mailcow's fail2ban would
// treat a 40-folder account as an attack (ADR §4).
func TestWatcherUsesOneWatchConnectionPerAccount(t *testing.T) {
	env := newSyncedEnv(t, 3)

	// Several folders, so a per-folder implementation would be visible.
	for _, name := range []string{"Archive", "Sent", "Drafts", "Projects"} {
		env.srv.addMailbox(name, imap.RoleNone, 300)
		seedMailboxLocked(env.srv, name, 2, referenceNow, name)
	}

	startWatcher(t, env, nil)

	if got := env.srv.watcherCount(); got != 1 {
		t.Errorf("the account has %d live watches, want exactly 1 (S2 T2d: NOTIFY collapses the fan-out)", got)
	}
}

// TestWatcherClearsTheErrorCountOnASuccessfulSession is the regression test for
// the production defect that left every pilot account's push permanently
// degraded (found on the live pilot, 2026-08-26).
//
// # The defect
//
// Watch seeds its local failure count from the PERSISTED consecutive-error
// count, and nothing on the success path ever cleared that column: the only
// writer that zeroes it is SaveCheckpoint, which the watcher's steady state
// (SetMailboxSyncState) never calls. So the counter was a RATCHET. Once an
// account had accumulated BreakerThreshold failures — over days, across
// restarts, from transient causes like a container recreation — every
// subsequent disconnect re-read a count already past the threshold and
// re-opened the breaker immediately, no matter how healthy the server was.
//
// The observable result on the pilot: all four accounts sat in 'half_open'
// with counts of 17-50 and last_success_at 5-14 days stale, each getting ONE
// connection attempt per 15-minute cooldown instead of a live watcher. Between
// those attempts nothing observed the account, so 620 messages expunged by
// another client stayed visible in Moov indefinitely.
//
// # What this test pins
//
// A watcher that connects successfully must clear the persisted error history,
// so that a later failure starts counting from zero rather than from whatever
// a bad week left behind. It is written as the sequence that broke: pre-load a
// count at the threshold, connect successfully, and require that the stored
// count is back to zero and the breaker closed.
func TestWatcherClearsTheErrorCountOnASuccessfulSession(t *testing.T) {
	env := newSyncedEnv(t, 2)
	ctx := context.Background()

	// The account arrives with a history of failures that has already reached
	// the breaker threshold — exactly the state the pilot accounts were in.
	for i := 0; i < DefaultBreakerThreshold; i++ {
		if _, err := env.store.RecordSyncError(ctx, env.account.ID, store.AccountScope,
			"stale failure from an earlier outage"); err != nil {
			t.Fatalf("seeding the error history: %v", err)
		}
	}

	before, err := env.store.GetCheckpoint(ctx, env.account.ID, store.AccountScope)
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if before.ConsecutiveErrors < DefaultBreakerThreshold {
		t.Fatalf("seeded %d consecutive errors, want at least %d",
			before.ConsecutiveErrors, DefaultBreakerThreshold)
	}

	// The server is healthy: this session must succeed.
	h := startWatcher(t, env, nil)
	h.waitFor(t, ObsConnected, 1, "the watcher never connected")

	// A successful session must retire the stale history. Without this, the
	// next disconnect — a container restart, a Dovecot reload — re-reads a
	// count past the threshold and re-opens the breaker at once, which is the
	// production failure.
	waitFor(t, 10*time.Second, func() bool {
		cp, cerr := env.store.GetCheckpoint(ctx, env.account.ID, store.AccountScope)
		return cerr == nil && cp.ConsecutiveErrors == 0
	}, "a successful watcher session never cleared the persisted error count "+
		"(the breaker ratchet: every later disconnect re-opens the breaker immediately)")

	after, err := env.store.GetCheckpoint(ctx, env.account.ID, store.AccountScope)
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if after.ConsecutiveErrors != 0 {
		t.Errorf("consecutive_errors is %d after a successful session, want 0",
			after.ConsecutiveErrors)
	}
	if after.BreakerState == store.BreakerOpen {
		t.Errorf("the breaker is still %q after a successful session, want it closed",
			after.BreakerState)
	}
	if after.LastSuccessAt == nil {
		t.Error("a successful watcher session did not record last_success_at; " +
			"the health of a push-only account would be invisible to an operator")
	}
}
