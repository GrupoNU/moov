package store_test

import (
	"context"
	"testing"

	"github.com/GrupoNU/moov/internal/store"
)

// Migration 0007 (account_prefs) against a real PostgreSQL 17.
//
// The properties proven here are invisible to the Go API: the schema objects
// exist with the shape the store expects, the two CHECK constraints are the
// DATABASE's and not a convention the Go code politely observes (that half is
// in prefs_test.go, where the constraint can be provoked with a direct
// INSERT), and the migration is replay-safe — moovd runs Migrate on every
// start, so a file that is not idempotent breaks the next deploy rather than
// the one that introduced it.

func TestPrefsSchemaExists(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	t.Run("the table exists with the expected columns", func(t *testing.T) {
		want := map[string]string{
			"account_id":     "bigint",
			"prefs":          "jsonb",
			"schema_version": "integer",
			"created_at":     "timestamp with time zone",
			"updated_at":     "timestamp with time zone",
		}
		rows, err := db.QueryContext(ctx, `
			SELECT column_name, data_type FROM information_schema.columns
			 WHERE table_schema = 'public' AND table_name = 'account_prefs'`)
		if err != nil {
			t.Fatalf("querying information_schema: %v", err)
		}
		defer func() { _ = rows.Close() }()

		got := map[string]string{}
		for rows.Next() {
			var name, kind string
			if err := rows.Scan(&name, &kind); err != nil {
				t.Fatal(err)
			}
			got[name] = kind
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 {
			t.Fatal("the account_prefs table does not exist")
		}
		for name, kind := range want {
			if got[name] != kind {
				t.Errorf("column %s is %q, want %q", name, got[name], kind)
			}
		}
	})

	t.Run("account_id is the primary key", func(t *testing.T) {
		// The singleton shape rests on this: one preference document per
		// mailbox, no surrogate id, and therefore no way for an account to end
		// up with two conflicting documents.
		var count int
		if err := db.QueryRowContext(ctx, `
			SELECT count(*)
			  FROM information_schema.table_constraints tc
			  JOIN information_schema.key_column_usage k
			    ON k.constraint_name = tc.constraint_name
			 WHERE tc.table_name = 'account_prefs'
			   AND tc.constraint_type = 'PRIMARY KEY'
			   AND k.column_name = 'account_id'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Error("account_id is not the primary key of account_prefs")
		}
	})

	t.Run("the CHECK constraints exist", func(t *testing.T) {
		for _, name := range []string{"account_prefs_is_object", "account_prefs_version_positive"} {
			var exists bool
			if err := db.QueryRowContext(ctx, `
				SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = $1)`, name).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if !exists {
				t.Errorf("constraint %s is missing; the Go layer would be the only thing "+
					"keeping an uninterpretable document out of the column", name)
			}
		}
	})

	t.Run("the watermark index exists", func(t *testing.T) {
		var exists bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_indexes
			                WHERE tablename = 'account_prefs' AND indexname = 'account_prefs_updated')`).
			Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Error("account_prefs_updated is missing")
		}
	})
}

// TestPrefsMigrationIsReplaySafe pins what moovd does on every start. Unlike
// 0006 this migration writes no rows, so the risk is narrower — but the
// CREATE ... IF NOT EXISTS forms are what make a re-run a no-op rather than a
// "relation already exists" failure, and they are load-bearing for the
// parallel test suite (several packages migrate the same database at once).
func TestPrefsMigrationIsReplaySafe(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	for i := range 3 {
		if err := store.Migrate(ctx, db); err != nil {
			t.Fatalf("re-running migrations (pass %d): %v", i, err)
		}
	}

	var n int64
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM account_prefs`).Scan(&n); err != nil {
		t.Fatalf("the account_prefs table did not survive a migration replay: %v", err)
	}
}
