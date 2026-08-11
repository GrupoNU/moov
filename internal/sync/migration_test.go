package sync

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// Tests for the bulk installation migration (A7 path 2 — the 89-account Crash
// case).

// migrationEnv builds n accounts, each backed by its own fake server, and
// returns them with a Connector that routes to the right one.
type migrationEnv struct {
	*testEnv
	accounts []store.Account
	servers  map[int64]*fakeServer
}

func newMigrationEnv(t *testing.T, accounts, messagesEach int) *migrationEnv {
	t.Helper()
	base := newTestEnv(t)
	ctx := context.Background()

	me := &migrationEnv{testEnv: base, servers: map[int64]*fakeServer{}}

	// The env's own account is the first; the rest are created here and
	// cleaned up with it.
	for i := range accounts {
		acct := base.account
		if i > 0 {
			var err error
			acct, err = base.store.CreateAccount(ctx, store.Account{
				Email:    fmt.Sprintf("bulk-%d-%d@example.test", i, time.Now().UnixNano()),
				IMAPHost: "dovecot.internal",
				IMAPPort: 143,
			})
			if err != nil {
				t.Fatalf("CreateAccount %d: %v", i, err)
			}
			id := acct.ID
			t.Cleanup(func() {
				if err := base.store.DeleteAccount(context.Background(), id); err != nil {
					t.Logf("cleanup: deleting account %d: %v", id, err)
				}
			})
		}

		srv := newFakeServer()
		inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
		seedMailbox(inbox, messagesEach, referenceNow, "Bulk")

		me.accounts = append(me.accounts, acct)
		me.servers[acct.ID] = srv
	}
	return me
}

// connector routes each account to its own fake server.
func (m *migrationEnv) connector() func(context.Context, store.Account, int) ([]imap.Client, error) {
	return func(_ context.Context, a store.Account, n int) ([]imap.Client, error) {
		srv, ok := m.servers[a.ID]
		if !ok {
			return nil, fmt.Errorf("no fake server for account %d", a.ID)
		}
		return srv.clients(n), nil
	}
}

// TestMigrationSyncsEveryAccount is the bulk path's baseline, and the place the
// extrapolated installation-migration rate is measured.
func TestMigrationSyncsEveryAccount(t *testing.T) {
	const accounts, each = 6, 60
	me := newMigrationEnv(t, accounts, each)

	mig, err := NewMigrator(me.store, me.blobs, MigrationOptions{
		Options:   me.testOptions(referenceNow),
		Accounts:  3,
		ConnectFn: me.connector(),
	})
	if err != nil {
		t.Fatalf("NewMigrator: %v", err)
	}

	res, err := mig.Run(context.Background(), me.accounts)
	if err != nil {
		t.Fatalf("migration: %v (per-account errors: %v)", err, res.Errors)
	}

	if res.Succeeded != accounts {
		t.Errorf("%d accounts succeeded, want %d (errors: %v)", res.Succeeded, accounts, res.Errors)
	}
	if res.Failed != 0 {
		t.Errorf("%d accounts failed, want 0", res.Failed)
	}
	if want := accounts * each; res.Stored != want {
		t.Errorf("stored %d messages, want %d", res.Stored, want)
	}

	// The figure the report extrapolates the 89-account case from.
	t.Logf("BULK: %d accounts x %d messages = %d in %s = %.1f msg/s",
		accounts, each, res.Stored, res.Elapsed.Round(time.Millisecond), res.Rate())

	// Every account must be individually complete, not merely counted.
	for _, a := range me.accounts {
		boxes, err := me.store.ListMailboxes(context.Background(), a.ID)
		if err != nil {
			t.Fatalf("ListMailboxes for account %d: %v", a.ID, err)
		}
		for _, b := range boxes {
			if b.Selectable && b.BackfillState != store.BackfillComplete {
				t.Errorf("account %d mailbox %q is %q, want %q",
					a.ID, b.Name, b.BackfillState, store.BackfillComplete)
			}
		}
	}
}

// TestMigrationContinuesPastAFailedAccount is the property that matters at 89
// accounts: one stale credential must not hold back the other 88.
func TestMigrationContinuesPastAFailedAccount(t *testing.T) {
	const accounts, each = 4, 20
	me := newMigrationEnv(t, accounts, each)

	// The second account cannot connect.
	broken := me.accounts[1].ID
	base := me.connector()

	mig, err := NewMigrator(me.store, me.blobs, MigrationOptions{
		Options:  me.testOptions(referenceNow),
		Accounts: 2,
		ConnectFn: func(ctx context.Context, a store.Account, n int) ([]imap.Client, error) {
			if a.ID == broken {
				return nil, errors.New("simulated bad credentials")
			}
			return base(ctx, a, n)
		},
		ContinueOnError: true,
	})
	if err != nil {
		t.Fatalf("NewMigrator: %v", err)
	}

	res, err := mig.Run(context.Background(), me.accounts)
	if err != nil {
		t.Fatalf("migration with ContinueOnError: %v", err)
	}

	if res.Succeeded != accounts-1 {
		t.Errorf("%d accounts succeeded, want %d", res.Succeeded, accounts-1)
	}
	if res.Failed != 1 {
		t.Errorf("%d accounts failed, want 1", res.Failed)
	}
	if _, ok := res.Errors[broken]; !ok {
		t.Errorf("the failing account %d is not named in Errors: %v", broken, res.Errors)
	}
	if want := (accounts - 1) * each; res.Stored != want {
		t.Errorf("stored %d messages, want %d", res.Stored, want)
	}
}

// TestMigrationStopsOnErrorWhenAsked checks the opposite policy, which is what
// a supervised run of a handful of accounts wants.
func TestMigrationStopsOnErrorWhenAsked(t *testing.T) {
	const accounts, each = 4, 10
	me := newMigrationEnv(t, accounts, each)

	broken := me.accounts[0].ID
	base := me.connector()

	mig, err := NewMigrator(me.store, me.blobs, MigrationOptions{
		Options:  me.testOptions(referenceNow),
		Accounts: 1, // serial, so the failure is observed before the rest start
		ConnectFn: func(ctx context.Context, a store.Account, n int) ([]imap.Client, error) {
			if a.ID == broken {
				return nil, errors.New("simulated bad credentials")
			}
			return base(ctx, a, n)
		},
		ContinueOnError: false,
	})
	if err != nil {
		t.Fatalf("NewMigrator: %v", err)
	}

	res, err := mig.Run(context.Background(), me.accounts)
	if err == nil {
		t.Fatal("the migration reported success despite a failed account")
	}
	if res.Failed == 0 {
		t.Error("Result.Failed is 0 after a failure")
	}
}

// TestMigratorSharesTheParsePoolAcrossAccounts is the S3 H6 budgeting rule:
// parse workers are per core for the whole migration, not per account.
func TestMigratorSharesTheParsePoolAcrossAccounts(t *testing.T) {
	me := newMigrationEnv(t, 1, 1)

	mig, err := NewMigrator(me.store, me.blobs, MigrationOptions{
		Options:   me.testOptions(referenceNow),
		Accounts:  8,
		ConnectFn: me.connector(),
	})
	if err != nil {
		t.Fatalf("NewMigrator: %v", err)
	}

	// With eight accounts in flight the per-account pool must be a fraction of
	// the machine, never the whole of it: eighty-nine accounts each spawning
	// GOMAXPROCS parse goroutines would put hundreds of CPU-bound goroutines on
	// a handful of cores.
	if got := mig.opts.Options.ParseWorkers; got < 1 {
		t.Errorf("per-account parse workers = %d, want at least 1", got)
	}
	if got, limit := mig.opts.Options.ParseWorkers, me.testOptions(referenceNow).ParseWorkers; got > limit {
		t.Errorf("per-account parse workers = %d, which exceeds the whole-machine budget %d", got, limit)
	}
}

// TestNewMigratorRequiresAConnector guards the one option with no sensible
// default: this package does not own credentials (E7 does).
func TestNewMigratorRequiresAConnector(t *testing.T) {
	if _, err := NewMigrator(nil, nil, MigrationOptions{}); err == nil {
		t.Error("NewMigrator accepted a nil store")
	}
}

func TestMigrationResultRate(t *testing.T) {
	r := MigrationResult{Stored: 900, Elapsed: 3 * time.Second}
	if got := r.Rate(); got != 300 {
		t.Errorf("Rate = %v, want 300", got)
	}
	if got := (MigrationResult{Stored: 5}).Rate(); got != 0 {
		t.Errorf("Rate with no elapsed time = %v, want 0", got)
	}
}
