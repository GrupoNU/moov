package store_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"

	"github.com/GrupoNU/moov/internal/store"
)

// These tests run against a real PostgreSQL 17. They are not skipped in CI: the
// workflow starts a postgres:17 service container and sets MOOV_TEST_DATABASE_URL,
// because the whole point of migration 0001 is that the S3 settings are ACTIVE,
// and a skipped test proves nothing about that. Locally, `make db-up` starts the
// same server through docker-compose.dev.yml.
//
// Without MOOV_TEST_DATABASE_URL the tests skip with an explicit message, so a
// contributor with no Docker can still run `go test ./...` and see why these
// were not executed.

const testDBEnv = "MOOV_TEST_DATABASE_URL"

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv(testDBEnv)
	if dsn == "" {
		t.Skipf("%s is not set; start a dev database with `make db-up` to run the store tests", testDBEnv)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connecting to %s: %v", testDBEnv, err)
	}
	return db
}

func migrated(t *testing.T) *sql.DB {
	t.Helper()

	db := openTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

// AC (L2 §3, E1): "the 3 S3 configs applied by migration, verified by test".
// Two of the three are database-level and belong to migration 0001; the third
// (STATISTICS 4000 on messages.tsv) belongs to the migration that creates the
// column, in E3 — see the note at the end of 0001.
func TestMigrationCreatesRequiredExtensions(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	// btree_gin: the composite gin(account_id, tsv) index of L2 §2.3 cannot
	// exist without it (S3 H2).
	// unaccent: diacritic-insensitive search for the es/pt installed base.
	for _, ext := range []string{"btree_gin", "unaccent"} {
		t.Run(ext, func(t *testing.T) {
			var installed bool
			err := db.QueryRowContext(ctx,
				`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1)`, ext,
			).Scan(&installed)
			if err != nil {
				t.Fatalf("querying pg_extension: %v", err)
			}
			if !installed {
				t.Errorf("extension %s is not installed; migration 0001 did not take effect", ext)
			}
		})
	}
}

// plan_cache_mode = force_custom_plan (S3 H3). Verified on a FRESH connection,
// not the migrating one: an ALTER DATABASE setting only reaches sessions that
// started after it was applied, so checking it on the same connection that ran
// the migration would pass even if the setting had not been persisted at all.
func TestForceCustomPlanIsActiveOnNewConnections(t *testing.T) {
	migrated(t) // apply migrations, then discard the connection

	fresh := openTestDB(t)
	ctx := context.Background()

	var mode string
	if err := fresh.QueryRowContext(ctx, `SHOW plan_cache_mode`).Scan(&mode); err != nil {
		t.Fatalf("SHOW plan_cache_mode: %v", err)
	}
	if mode != "force_custom_plan" {
		t.Errorf("plan_cache_mode = %q, want %q.\n"+
			"This is the S3 H3 setting: without it PostgreSQL adopts a generic plan "+
			"after five executions and full-text search falls off a cliff. If you are "+
			"connecting through PgBouncer, see risk 2 in L2 §5 and the caveat in "+
			"migration 0001.", mode, "force_custom_plan")
	}
}

// The third mandatory S3 setting, and the marker migration 0001 left for E3:
// STATISTICS 4000 on messages.tsv.
//
// Without it the planner misestimates tsvector selectivity by ~500x (4,951
// estimated rows against 10 actual), decides it can satisfy LIMIT 50 by
// walking the date index, and filters 999,990 rows — 13,085 ms for a query
// that takes 1.6 ms on the composite GIN (S3 §5.3). The setting is invisible
// in every functional test: queries return the right rows either way, and only
// the latency collapses. That is exactly why it is asserted here.
//
// pg_attribute.attstattarget is NULL when the column uses the system default
// and holds the explicit target otherwise, so a NULL scan here means the
// ALTER was lost.
func TestTsvStatisticsTargetIsSet(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	var target *int
	err := db.QueryRowContext(ctx, `
		SELECT a.attstattarget
		  FROM pg_attribute a
		  JOIN pg_class c ON c.oid = a.attrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relname = 'messages' AND a.attname = 'tsv'`,
	).Scan(&target)
	if err != nil {
		t.Fatalf("querying attstattarget for messages.tsv: %v", err)
	}
	if target == nil {
		t.Fatalf("messages.tsv has the default statistics target; " +
			"migration 0002 must run ALTER TABLE messages ALTER COLUMN tsv SET STATISTICS 4000 (S3 §5.3)")
	}
	if *target != 4000 {
		t.Errorf("messages.tsv statistics target = %d, want 4000 (S3 §5.3)", *target)
	}
}

// The composite GIN index of S3 §5.2 must exist, and must be the composite
// one: a plain gin(tsv) passes every correctness test and is up to 6,600x
// slower on a rare term, because without account_id inside the index the cost
// scales with the whole installation rather than the user's mailbox.
func TestCompositeGINIndexExists(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	var indexdef string
	err := db.QueryRowContext(ctx, `
		SELECT indexdef FROM pg_indexes
		 WHERE schemaname = 'public' AND tablename = 'messages'
		   AND indexname = 'messages_acct_tsv_gin'`).Scan(&indexdef)
	if err != nil {
		t.Fatalf("messages_acct_tsv_gin not found: %v", err)
	}

	// The column order matters: gin (account_id, tsv), not gin (tsv).
	if !strings.Contains(indexdef, "gin") {
		t.Errorf("index is not a GIN index: %s", indexdef)
	}
	if !strings.Contains(indexdef, "account_id") {
		t.Errorf("index does not include account_id, which is the whole point (S3 §5.2): %s", indexdef)
	}
	if !strings.Contains(indexdef, "tsv") {
		t.Errorf("index does not include tsv: %s", indexdef)
	}
}

// Every table of L2 §2.3 must exist after the migrations, including the A5
// split: messages (immutable) and message_state (volatile) as SEPARATE tables.
func TestCoreSchemaTablesExist(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	for _, table := range []string{
		"accounts", "mailboxes", "messages", "message_state",
		"blobs", "blob_refs", "sync_log", "intents",
	} {
		t.Run(table, func(t *testing.T) {
			var exists bool
			err := db.QueryRowContext(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM information_schema.tables
					 WHERE table_schema = 'public' AND table_name = $1
				)`, table).Scan(&exists)
			if err != nil {
				t.Fatalf("querying information_schema: %v", err)
			}
			if !exists {
				t.Errorf("table %s does not exist", table)
			}
		})
	}
}

// A5 in its structural form: the volatile columns must live on message_state,
// and must NOT be on messages.
//
// A flag column on `messages` would mean every read/unread toggle rewrites the
// ~2.2 KB generated tsv into the GIN index (S3 §4.5). This test fails the
// moment somebody "simplifies" the schema by merging the two tables back.
func TestVolatileColumnsAreNotOnMessages(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	for _, col := range []string{"flags", "keywords", "uid", "mailbox_id", "modseq_seen"} {
		t.Run(col, func(t *testing.T) {
			var exists bool
			err := db.QueryRowContext(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM information_schema.columns
					 WHERE table_schema = 'public' AND table_name = 'messages'
					   AND column_name = $1
				)`, col).Scan(&exists)
			if err != nil {
				t.Fatalf("querying information_schema: %v", err)
			}
			if exists {
				t.Errorf("messages.%s exists; volatile state belongs on message_state "+
					"(arbitration A5 — otherwise a flag change rewrites the tsv into the GIN index, S3 §4.5)", col)
			}
		})
	}
}

// Migrations must be idempotent: moovd runs them on every start.
func TestMigrateIsIdempotent(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	before, err := store.MigrationVersion(ctx, db)
	if err != nil {
		t.Fatalf("MigrationVersion: %v", err)
	}
	if before < 1 {
		t.Fatalf("migration version = %d, want at least 1", before)
	}

	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	after, err := store.MigrationVersion(ctx, db)
	if err != nil {
		t.Fatalf("MigrationVersion after second run: %v", err)
	}
	if after != before {
		t.Errorf("migration version moved from %d to %d on a no-op run", before, after)
	}
}
