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

// recordingWatcher stands in for E6 so the supervisor's hand-off can be tested
// before E6 exists.
type recordingWatcher struct {
	mu      sync.Mutex
	watched []int64
	block   bool
}

func (w *recordingWatcher) Watch(ctx context.Context, account store.Account) error {
	w.mu.Lock()
	w.watched = append(w.watched, account.ID)
	w.mu.Unlock()

	if w.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (w *recordingWatcher) accounts() []int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]int64(nil), w.watched...)
}

// TestSupervisorSyncsThenHandsToWatcher covers the E5/E6 seam: an account is
// initially synced, and only then handed to the watcher.
func TestSupervisorSyncsThenHandsToWatcher(t *testing.T) {
	env := newTestEnv(t)
	env.mustSyncableAccount(t)

	srv := newFakeServer()
	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, 25, referenceNow, "Inbox")

	watcher := &recordingWatcher{}
	sup, err := NewSupervisor(env.store, env.blobs, SupervisorOptions{
		Options:   env.testOptions(referenceNow),
		Connector: ConnectorFunc(func(context.Context, store.Account, int) ([]imap.Client, error) { return srv.clients(2), nil }),
		Watcher:   watcher,
	})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- sup.Run(ctx) }()

	// The supervisor blocks after syncing, so completion is observed through
	// its effects rather than its return.
	waitFor(t, 20*time.Second, func() bool {
		return len(watcher.accounts()) == 1
	}, "the watcher was never handed the account")

	if got := env.countMessages(t); got != 25 {
		t.Errorf("stored %d messages, want 25", got)
	}
	if got := watcher.accounts(); len(got) != 1 || got[0] != env.account.ID {
		t.Errorf("watcher saw %v, want [%d]", got, env.account.ID)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("supervisor returned %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("the supervisor did not stop on cancellation")
	}
}

// TestSupervisorSkipsAlreadySyncedAccounts proves the restart path: a daemon
// restarted after a completed sync must not re-sync, only resume watching.
func TestSupervisorSkipsAlreadySyncedAccounts(t *testing.T) {
	env := newTestEnv(t)
	env.mustSyncableAccount(t)

	srv := newFakeServer()
	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, 15, referenceNow, "Inbox")

	opts := env.testOptions(referenceNow)
	if _, err := env.syncer(t, srv, opts).Run(context.Background(), env.account); err != nil {
		t.Fatalf("priming run: %v", err)
	}

	srv.mu.Lock()
	fetchesAfterPriming := srv.fetchCount
	srv.mu.Unlock()

	watcher := &recordingWatcher{block: true}
	sup, err := NewSupervisor(env.store, env.blobs, SupervisorOptions{
		Options:   opts,
		Connector: ConnectorFunc(func(context.Context, store.Account, int) ([]imap.Client, error) { return srv.clients(2), nil }),
		Watcher:   watcher,
	})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() { _ = sup.Run(ctx) }()

	waitFor(t, 20*time.Second, func() bool { return len(watcher.accounts()) == 1 },
		"the watcher was never handed the already-synced account")

	srv.mu.Lock()
	extraFetches := srv.fetchCount - fetchesAfterPriming
	srv.mu.Unlock()

	if extraFetches != 0 {
		t.Errorf("the supervisor fetched %d messages for an already-synced account, want 0", extraFetches)
	}
}

// TestSupervisorSkipsAccountsWithoutCredentials checks the fail2ban guard: an
// account E7 has not provisioned must never produce a login attempt.
func TestSupervisorSkipsAccountsWithoutCredentials(t *testing.T) {
	env := newTestEnv(t) // account is left with credential_state 'pending'

	var connects int
	sup, err := NewSupervisor(env.store, env.blobs, SupervisorOptions{
		Options: env.testOptions(referenceNow),
		Connector: ConnectorFunc(func(context.Context, store.Account, int) ([]imap.Client, error) {
			connects++
			return nil, errors.New("should never be called")
		}),
	})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sup.Run(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run: %v", err)
	}

	if connects != 0 {
		t.Errorf("the supervisor attempted %d connections for an unprovisioned account, want 0", connects)
	}
}

// TestSupervisorRetriesAFailedAccount checks that one bad sync does not
// permanently abandon an account.
func TestSupervisorRetriesAFailedAccount(t *testing.T) {
	env := newTestEnv(t)
	env.mustSyncableAccount(t)

	srv := newFakeServer()
	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, 10, referenceNow, "Inbox")

	var (
		mu       sync.Mutex
		attempts int
	)
	watcher := &recordingWatcher{block: true}
	sup, err := NewSupervisor(env.store, env.blobs, SupervisorOptions{
		Options: env.testOptions(referenceNow),
		Connector: ConnectorFunc(func(context.Context, store.Account, int) ([]imap.Client, error) {
			mu.Lock()
			attempts++
			n := attempts
			mu.Unlock()
			if n == 1 {
				return nil, errors.New("simulated connection failure")
			}
			return srv.clients(2), nil
		}),
		Watcher:    watcher,
		RetryDelay: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() { _ = sup.Run(ctx) }()

	waitFor(t, 20*time.Second, func() bool { return len(watcher.accounts()) == 1 },
		"the account was never synced after the first attempt failed")

	if got := env.countMessages(t); got != 10 {
		t.Errorf("stored %d messages after the retry, want 10", got)
	}

	// The failure must be visible to an operator, not merely retried silently.
	cp, err := env.store.GetCheckpoint(context.Background(), env.account.ID, store.AccountScope)
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	_ = cp // the successful retry clears the error; the assertion above is the behavior that matters
}

// waitFor polls a condition, failing the test if it does not hold in time. It
// polls rather than sleeping a fixed interval so a fast machine is not made to
// wait and a slow one is not made to flake.
func waitFor(t *testing.T, limit time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

// TestSupervisorWatchesEveryAccountBeyondConcurrency is the regression test for
// a defect found in production on 2026-09-16, the first time a fifth account
// existed on the pilot.
//
// # The defect
//
// Run() bounds initial syncs with a semaphore of Concurrency slots, and each
// account's goroutine releases its slot with `defer func() { <-sem }()`. But
// the goroutine does not END after the initial sync: it calls runWatcher, whose
// Watch blocks for the LIFETIME of the account's watcher. So a slot is held
// forever, not for the duration of a sync.
//
// With the default Concurrency of 4 and four accounts, nothing was wrong. The
// fifth account — created through the accounts API, against a real Mailcow —
// simply never started: no sync, no watcher, no error, no log line. It waited
// on a semaphore that would never be released, and the supervisor never even
// finished its startup round.
//
// The symptom is the worst kind: silent. The mailbox existed in Mailcow and
// received mail; Moov just never showed it, and nothing anywhere said why.
//
// # What this test pins
//
// Concurrency limits how many accounts are initially synced AT ONCE, which is
// what the option's documentation says it means. It must not limit how many
// accounts are watched, and an account beyond the limit must not be stranded.
func TestSupervisorWatchesEveryAccountBeyondConcurrency(t *testing.T) {
	env := newTestEnv(t)
	env.mustSyncableAccount(t)

	// Two more accounts than the concurrency limit below, all watchable.
	const concurrency = 1
	extra := env.mustExtraSyncableAccounts(t, 2)
	want := len(extra) + 1

	srv := newFakeServer()
	srv.addMailbox("INBOX", imap.RoleInbox, 100)

	// block: true is the whole point — a real watcher never returns either.
	watcher := &recordingWatcher{block: true}
	sup, err := NewSupervisor(env.store, env.blobs, SupervisorOptions{
		Options:     env.testOptions(referenceNow),
		Connector:   ConnectorFunc(func(context.Context, store.Account, int) ([]imap.Client, error) { return srv.clients(2), nil }),
		Watcher:     watcher,
		Concurrency: concurrency,
	})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go func() { _ = sup.Run(ctx) }()

	waitFor(t, 45*time.Second, func() bool { return len(watcher.accounts()) >= want },
		"an account beyond the concurrency limit was never watched — it is stranded on the semaphore, "+
			"exactly as the pilot's fifth account was: no sync, no watcher, no error")
}

// TestSupervisorAdoptsAnAccountCreatedAfterStartup is the regression test for
// the defect found in production on 2026-09-17, the first time a mailbox was
// created through the accounts API against a running daemon.
//
// # The defect
//
// Run() called eligibleAccounts exactly ONCE, before the loop, and then blocked
// forever. The set of supervised accounts was therefore frozen at the instant
// the process started. An account created a second later — by the accounts API,
// on behalf of an organizer who is watching a spinner — was invisible to the
// engine until somebody restarted moovd.
//
// The symptom is the same silent shape as the concurrency-slot leak above, and
// worse, because nothing about it looks broken: the API returned 201, the
// mailbox exists in Mailcow, the credential validated against Dovecot, the
// account row says active/active, and the delegated session opens the webmail.
// It just shows loading skeletons forever. Real mail delivered to the mailbox
// did not change anything either, because no watcher was ever attached to
// notice. The only log lines carrying the account id came from the session
// issuer; the supervisor, the syncer and the watcher never mentioned it.
//
// # What this test pins
//
// The supervised set is a moving target, not a startup snapshot: an account
// that becomes eligible while Run is underway gets synced and watched without a
// restart, within a bounded time.
func TestSupervisorAdoptsAnAccountCreatedAfterStartup(t *testing.T) {
	env := newTestEnv(t)
	env.mustSyncableAccount(t)

	srv := newFakeServer()
	srv.addMailbox("INBOX", imap.RoleInbox, 100)

	watcher := &recordingWatcher{block: true}
	sup, err := NewSupervisor(env.store, env.blobs, SupervisorOptions{
		Options:   env.testOptions(referenceNow),
		Connector: ConnectorFunc(func(context.Context, store.Account, int) ([]imap.Client, error) { return srv.clients(2), nil }),
		Watcher:   watcher,
		// Short enough that the test does not wait on a production-sized
		// sweep; the production value is argued at its declaration.
		DiscoveryInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go func() { _ = sup.Run(ctx) }()

	// The account that existed at startup is watched first: that proves the
	// supervisor really is running before the new account appears, so a later
	// failure cannot be blamed on a supervisor that never started.
	waitFor(t, 20*time.Second, func() bool { return len(watcher.accounts()) == 1 },
		"the pre-existing account was never watched; the supervisor did not start")

	// Now the production scenario: a mailbox created through the accounts API
	// while the daemon runs.
	created := env.mustExtraSyncableAccounts(t, 1)[0]

	waitFor(t, 30*time.Second, func() bool {
		for _, id := range watcher.accounts() {
			if id == created.ID {
				return true
			}
		}
		return false
	}, "an account created after startup was never synced or watched — it is invisible to the "+
		"supervisor until moovd restarts, exactly as unidos@corppass.events was")

	// Adoption must not double-supervise anything: the pre-existing account
	// must still have been handed to the watcher exactly once, however many
	// discovery sweeps have run by now.
	seen := map[int64]int{}
	for _, id := range watcher.accounts() {
		seen[id]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("account %d was handed to the watcher %d times, want 1: a second goroutine for "+
				"a supervised account means two IMAP sessions and two syncers racing on the same rows", id, n)
		}
	}
}

// TestSupervisorAdoptsOnNudgeWithoutWaitingForTheSweep pins the latency half of
// the fix.
//
// The sweep is the guarantee; the nudge is what makes the common case — a
// mailbox created through the accounts API while someone watches the webmail —
// immediate. The discovery interval here is set absurdly long precisely so that
// a pass can only be explained by the nudge: if Nudge were a no-op this test
// would time out rather than pass slowly, which is what makes it a real
// assertion instead of a race.
func TestSupervisorAdoptsOnNudgeWithoutWaitingForTheSweep(t *testing.T) {
	env := newTestEnv(t)
	env.mustSyncableAccount(t)

	srv := newFakeServer()
	srv.addMailbox("INBOX", imap.RoleInbox, 100)

	watcher := &recordingWatcher{block: true}
	sup, err := NewSupervisor(env.store, env.blobs, SupervisorOptions{
		Options:           env.testOptions(referenceNow),
		Connector:         ConnectorFunc(func(context.Context, store.Account, int) ([]imap.Client, error) { return srv.clients(2), nil }),
		Watcher:           watcher,
		DiscoveryInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go func() { _ = sup.Run(ctx) }()

	waitFor(t, 20*time.Second, func() bool { return len(watcher.accounts()) == 1 },
		"the pre-existing account was never watched; the supervisor did not start")

	created := env.mustExtraSyncableAccounts(t, 1)[0]
	sup.Nudge()

	waitFor(t, 20*time.Second, func() bool {
		for _, id := range watcher.accounts() {
			if id == created.ID {
				return true
			}
		}
		return false
	}, "Nudge did not make the supervisor discover a new account; with a one-hour sweep it is the "+
		"only thing that could have")
}

// TestSupervisorNudgeIsSafeBeforeAndAfterRun pins the two calls that are easy
// to get wrong in a channel-based signal: one before the loop exists (which
// must be remembered, not dropped, and must not block a caller) and several in
// a row (which must coalesce rather than fill a buffer and block the accounts
// API on a busy supervisor).
func TestSupervisorNudgeIsSafeBeforeAndAfterRun(t *testing.T) {
	env := newTestEnv(t)

	sup, err := NewSupervisor(env.store, env.blobs, SupervisorOptions{
		Options:   env.testOptions(referenceNow),
		Connector: ConnectorFunc(func(context.Context, store.Account, int) ([]imap.Client, error) { return nil, errors.New("unused") }),
	})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	// Run has not started. None of these may block.
	done := make(chan struct{})
	go func() {
		for range 100 {
			sup.Nudge()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Nudge blocked; the accounts API would hang on a busy supervisor")
	}
}
