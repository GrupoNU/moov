package sync

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// Initial sync against a real Dovecot.
//
// Skipped unless the environment below is set, so `go test ./...` stays
// hermetic. It exists for the reason spike S2 established: go-imap's own suite
// is green against bytes a real server rejects, and every finding that mattered
// came from Dovecot rather than from a unit test. The fake proves the
// pipeline's LOGIC; this proves the pipeline against a server.
//
// Environment (same names as internal/imap's integration suite, so one
// configuration serves both):
//
//	MOOV_IMAP_TEST_HOST       required — "dovecot" inside the Mailcow network
//	MOOV_IMAP_TEST_PORT       optional, default 143
//	MOOV_IMAP_TEST_USER       required — a DEDICATED test mailbox
//	MOOV_IMAP_TEST_PASSWORD   required — environment only, never a file
//	MOOV_IMAP_TEST_SERVERNAME optional — the name the certificate carries
//	MOOV_IMAP_TEST_INSECURE   optional — "1" to skip verification (dev only)
//	MOOV_TEST_DATABASE_URL    required — the store
//
// # Seeding is out of band, and why
//
// This test does NOT create its own messages. internal/imap exposes no APPEND
// or CREATE (L2 §4.1 has no need for them — the sync engine only ever reads),
// and the architecture rule forbids reaching past the Client interface to
// go-imap from this package. Rather than widen a production interface to serve
// a test, the mailbox is seeded by a separate tool and this test consumes what
// it finds:
//
//	MOOV_SYNC_TEST_FOLDERS  required — "Folder:count,Folder:count,…" describing
//	                        what the seeder put where
//	MOOV_SYNC_TEST_NEEDLE   required — a rare term planted in exactly one of
//	                        those messages, used to prove the FTS path
//
// The contract gap is recorded in the E5 report: if the engine ever needs to
// write messages (drafts, undo-send), APPEND joins the interface then, for a
// product reason rather than a test one.

// integrationConfig builds the IMAP configuration or skips the test.
func integrationConfig(t *testing.T) imap.Config {
	t.Helper()

	host := os.Getenv("MOOV_IMAP_TEST_HOST")
	user := os.Getenv("MOOV_IMAP_TEST_USER")
	pass := os.Getenv("MOOV_IMAP_TEST_PASSWORD")
	if host == "" || user == "" || pass == "" {
		t.Skip("integration test: set MOOV_IMAP_TEST_HOST, MOOV_IMAP_TEST_USER and " +
			"MOOV_IMAP_TEST_PASSWORD to run (see internal/sync/integration_test.go)")
	}

	cfg := imap.Config{
		Host:          host,
		Username:      user,
		Password:      pass,
		TLSServerName: os.Getenv("MOOV_IMAP_TEST_SERVERNAME"),
	}
	if p := os.Getenv("MOOV_IMAP_TEST_PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("MOOV_IMAP_TEST_PORT=%q is not a number: %v", p, err)
		}
		cfg.Port = n
	}
	if os.Getenv("MOOV_IMAP_TEST_INSECURE") == "1" {
		cfg.InsecureSkipVerify = true
	}
	return cfg
}

// expectedFolder is one seeded folder and how many messages it should hold.
type expectedFolder struct {
	name  string
	count int
}

// seededFolders parses MOOV_SYNC_TEST_FOLDERS.
func seededFolders(t *testing.T) []expectedFolder {
	t.Helper()

	spec := os.Getenv("MOOV_SYNC_TEST_FOLDERS")
	if spec == "" {
		t.Skip("integration test: set MOOV_SYNC_TEST_FOLDERS (\"Folder:count,…\") " +
			"after seeding the mailbox; see internal/sync/integration_test.go")
	}

	var out []expectedFolder
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, countStr, ok := strings.Cut(entry, ":")
		if !ok {
			t.Fatalf("MOOV_SYNC_TEST_FOLDERS entry %q is not \"Folder:count\"", entry)
		}
		n, err := strconv.Atoi(strings.TrimSpace(countStr))
		if err != nil {
			t.Fatalf("MOOV_SYNC_TEST_FOLDERS entry %q has a non-numeric count: %v", entry, err)
		}
		out = append(out, expectedFolder{name: strings.TrimSpace(name), count: n})
	}
	if len(out) == 0 {
		t.Fatal("MOOV_SYNC_TEST_FOLDERS is set but describes no folders")
	}
	return out
}

// TestIntegrationInitialSyncAgainstDovecot runs the initial sync against a real
// Dovecot and verifies counts, flags, bodies, and that a planted needle is
// findable through the store's search API.
func TestIntegrationInitialSyncAgainstDovecot(t *testing.T) {
	cfg := integrationConfig(t)
	plan := seededFolders(t)

	needle := os.Getenv("MOOV_SYNC_TEST_NEEDLE")
	if needle == "" {
		t.Skip("integration test: set MOOV_SYNC_TEST_NEEDLE to the term planted by the seeder")
	}

	env := newTestEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// A real account row pointing at the real server.
	if err := env.store.SetAccountCredentials(ctx, env.account.ID, cfg.Username, []byte("x")); err != nil {
		t.Fatalf("SetAccountCredentials: %v", err)
	}

	clients := make([]imap.Client, 0, 2)
	for range 2 {
		c := imap.New(env.logger)
		if err := c.Connect(ctx, cfg); err != nil {
			t.Fatalf("connecting sync client: %v", err)
		}
		defer func() { _ = c.Close() }()
		clients = append(clients, c)
	}

	opts := Options{
		Logger:      env.logger,
		Connections: 2,
		// The window and batch defaults, because this run is also where the
		// real-server messages/second figure comes from.
		FetchWindow: DefaultFetchWindow,
		BatchSize:   DefaultBatchSize,
	}
	s, err := New(env.store, env.blobs, clients, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	started := time.Now()
	res, err := s.Run(ctx, env.account)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("initial sync against Dovecot: %v", err)
	}
	if !res.Complete {
		t.Error("the run did not complete")
	}

	total := res.RecentStored + res.BackfillStored
	t.Logf("REAL SERVER: %d messages across %d mailboxes in %s = %.1f msg/s",
		total, res.Mailboxes, elapsed.Round(time.Millisecond),
		float64(total)/elapsed.Seconds())
	t.Logf("  recent phase: %d in %s | backfill: %d | skipped: %d | parse failed: %d",
		res.RecentStored, res.RecentElapsed.Round(time.Millisecond),
		res.BackfillStored, res.Skipped, res.Failed)

	// Every seeded message must be present in its folder.
	for _, p := range plan {
		mb, err := env.store.GetMailboxByName(ctx, env.account.ID, p.name)
		if err != nil {
			t.Errorf("mailbox %q was not stored: %v", p.name, err)
			continue
		}
		gotTotal, _, err := env.store.CountMailboxMessages(ctx, mb.ID)
		if err != nil {
			t.Errorf("counting %q: %v", p.name, err)
			continue
		}
		if gotTotal != int64(p.count) {
			t.Errorf("%q holds %d messages, want %d", p.name, gotTotal, p.count)
		}
		if mb.BackfillState != store.BackfillComplete {
			t.Errorf("%q backfill state is %q, want %q",
				p.name, mb.BackfillState, store.BackfillComplete)
		}
	}

	// The planted needle must be findable through the search API — the only
	// assertion that proves the parser's text reached the FTS column through a
	// real fetch rather than a synthetic one.
	hits, err := env.store.Search(ctx, store.SearchQuery{AccountID: env.account.ID, Text: needle})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("search for the needle returned %d hits, want 1", len(hits))
	}
	if !strings.Contains(hits[0].Subject, needle) {
		t.Errorf("the hit's subject is %q, which does not carry the needle", hits[0].Subject)
	}

	// A spot check of the stored body: the bytes must have survived the round
	// trip through the blob store and the parser.
	msg, err := env.store.GetMessage(ctx, hits[0].MessageID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !strings.Contains(msg.BodyText, needle) {
		t.Errorf("the stored body does not contain the needle: %q", msg.BodyText)
	}
	if msg.ParseStatus != store.ParseOK {
		t.Errorf("the needle message parsed as %q, want %q", msg.ParseStatus, store.ParseOK)
	}
	if len(msg.RawSHA256) != 32 {
		t.Errorf("the stored sha256 is %d bytes, want 32", len(msg.RawSHA256))
	}

	// Flags: the seeder marks a known message \Seen, and the sync must reflect
	// it. A wrong bitmask silently mis-renders every message list.
	seenCount := 0
	rows, err := env.store.Pool().Query(ctx,
		`SELECT count(*) FROM message_state WHERE account_id = $1 AND (flags & 1) = 1`,
		env.account.ID)
	if err != nil {
		t.Fatalf("counting seen messages: %v", err)
	}
	for rows.Next() {
		if err := rows.Scan(&seenCount); err != nil {
			t.Fatalf("scanning seen count: %v", err)
		}
	}
	rows.Close()
	if seenCount == 0 {
		t.Error("no message came back with \\Seen, but the seeder marked some")
	}
	t.Logf("  %d of %d messages carry \\Seen", seenCount, total)

	// A second run must store nothing new: idempotency against a real server,
	// not just against the fake.
	rerun, err := s.Run(ctx, env.account)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if n := rerun.RecentStored + rerun.BackfillStored; n != 0 {
		t.Errorf("the second run stored %d messages, want 0", n)
	}
}
