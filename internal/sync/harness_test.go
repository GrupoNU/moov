package sync

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for the migrator

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/store"
)

// The store-integration harness.
//
// These tests need a real PostgreSQL because the properties under test are
// database properties: the unique index on (mailbox_id, uidvalidity, uid) is
// what makes a duplicate impossible, and a transaction boundary is what makes a
// checkpoint honest. A mocked store would test the mock.
//
// MOOV_TEST_DATABASE_URL is the same variable internal/store uses, so one
// `make db-up` serves both suites and CI's PG service container needs no
// additional configuration.
const testDBEnv = "MOOV_TEST_DATABASE_URL"

// testEnv is everything one test needs: a migrated store, a blob store on a
// temp dir, and an account of its own.
type testEnv struct {
	store   *store.Store
	blobs   *blob.Store
	account store.Account
	logger  *slog.Logger
}

// newTestEnv builds an isolated environment, skipping when no database is
// configured.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	dsn := os.Getenv(testDBEnv)
	if dsn == "" {
		t.Skipf("%s is not set; start a dev database with `make db-up` to run the sync tests", testDBEnv)
	}

	ctx := context.Background()

	// Migrations first: they are idempotent, so several packages' suites can
	// each ensure the schema without coordinating.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("opening migration connection: %v", err)
	}
	migCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := store.Migrate(migCtx, db); err != nil {
		_ = db.Close()
		t.Fatalf("Migrate: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing migration connection: %v", err)
	}

	st, err := store.Open(ctx, store.Config{DSN: dsn, MaxConns: 8, AnalyticMaxConns: 2})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(st.Close)

	blobs, err := blob.New(blob.Config{Root: t.TempDir(), Pool: st.Pool()})
	if err != nil {
		t.Fatalf("blob.New: %v", err)
	}

	// One account per test, removed afterwards with everything that cascades
	// from it. That is what lets the suite run without serializing on a
	// truncate, and it mirrors the multi-tenant shape of production.
	email := fmt.Sprintf("e5-%s-%d@example.test", sanitizeTestName(t.Name()), time.Now().UnixNano())
	acct, err := st.CreateAccount(ctx, store.Account{
		Email:    email,
		IMAPHost: "dovecot.internal",
		IMAPPort: 143,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	t.Cleanup(func() {
		if err := st.DeleteAccount(context.Background(), acct.ID); err != nil {
			t.Logf("cleanup: deleting account %d: %v", acct.ID, err)
		}
	})

	return &testEnv{
		store:   st,
		blobs:   blobs,
		account: acct,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// testOptions returns options with a fixed clock, so the 30-day window is the
// same window on every run rather than depending on the wall clock.
func (e *testEnv) testOptions(now time.Time) Options {
	return Options{
		Logger:       e.logger,
		Clock:        func() time.Time { return now },
		FetchWindow:  50,
		BatchSize:    10,
		Connections:  2,
		ParseWorkers: 4,
	}
}

// syncer builds a Syncer over the fake server.
func (e *testEnv) syncer(t *testing.T, srv *fakeServer, opts Options) *Syncer {
	t.Helper()
	s, err := New(e.store, e.blobs, srv.clients(opts.Connections), opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// storedUIDs returns every stored (mailbox name, uid) pair of the account, which
// is the ground truth the loss/duplicate assertions compare against.
func (e *testEnv) storedUIDs(t *testing.T) map[string][]int64 {
	t.Helper()
	ctx := context.Background()

	rows, err := e.store.Pool().Query(ctx, `
		SELECT mb.name, ms.uid
		  FROM message_state ms
		  JOIN mailboxes mb ON mb.id = ms.mailbox_id
		 WHERE ms.account_id = $1
		 ORDER BY mb.name, ms.uid`, e.account.ID)
	if err != nil {
		t.Fatalf("querying stored uids: %v", err)
	}
	defer rows.Close()

	out := map[string][]int64{}
	for rows.Next() {
		var name string
		var uid int64
		if err := rows.Scan(&name, &uid); err != nil {
			t.Fatalf("scanning stored uid: %v", err)
		}
		out[name] = append(out[name], uid)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading stored uids: %v", err)
	}
	return out
}

// countMessages returns how many message rows the account has.
func (e *testEnv) countMessages(t *testing.T) int {
	t.Helper()
	var n int
	err := e.store.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM messages WHERE account_id = $1`, e.account.ID).Scan(&n)
	if err != nil {
		t.Fatalf("counting messages: %v", err)
	}
	return n
}

// countByParseStatus returns how many rows carry a given parse status.
func (e *testEnv) countByParseStatus(t *testing.T, status store.ParseStatus) int {
	t.Helper()
	var n int
	err := e.store.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM messages WHERE account_id = $1 AND parse_status = $2`,
		e.account.ID, string(status)).Scan(&n)
	if err != nil {
		t.Fatalf("counting %s messages: %v", status, err)
	}
	return n
}

// sanitizeTestName makes a test name safe for an email local part.
func sanitizeTestName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := b.String()
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// mustSyncableAccount marks the test account active with credentials, which is
// what the supervisor's eligibility filter requires.
func (e *testEnv) mustSyncableAccount(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := e.store.SetAccountCredentials(ctx, e.account.ID, e.account.Email, []byte("ciphertext")); err != nil {
		t.Fatalf("SetAccountCredentials: %v", err)
	}
	acct, err := e.store.GetAccount(ctx, e.account.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	e.account = acct
}
