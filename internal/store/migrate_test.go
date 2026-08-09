package store_test

import (
	"context"
	"database/sql"
	"os"
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
