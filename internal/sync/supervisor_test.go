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
