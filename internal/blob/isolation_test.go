package blob_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GrupoNU/moov/internal/store"
)

// Test isolation for a SHARED database.
//
// # The problem
//
// The garbage collector is deliberately global: `GC` scans every collectable
// row in `blobs`, because in production there is exactly one blob store per
// database and "every unreferenced blob" is the correct set. That is right for
// production and wrong for a test process that shares its database with other
// packages.
//
// `internal/sync` writes blob rows whose bytes live under ITS OWN root. When
// both suites run against one database, this package's GC sees those rows as
// perfectly ordinary garbage — they are unreferenced, and they are older than a
// nanosecond grace period. Three separate failures follow, all from that one
// cause, and all reproduced deterministically before this file existed:
//
//   - TestGCCollectsOnlyUnreferenced fails because the `LIMIT` fills with
//     foreign rows and the sweep never reaches the blob the test wrote.
//   - TestConcurrentGC fails counting "103 collections for 20 blobs": the
//     collectors did their job, on somebody else's garbage.
//   - TestConcurrentAddRefAndGC degrades from ~1.5 s to ~40 s, because every one
//     of its 40 rounds sweeps a table filling up from another package. This is
//     the failure that was originally reported; it is the mildest symptom, not
//     the disease.
//
// # The fix, and why this one
//
// Each test gets its own PostgreSQL schema holding its own `blobs` and
// `blob_refs`, reached by setting `search_path` on every connection in the
// pool. The sweep stays exactly as global as it is in production — it simply
// cannot see rows that are not in its schema, because they are in different
// tables.
//
// The alternatives were weighed and rejected:
//
//   - A GC filter (an account or root predicate) used only by tests would mean
//     the production sweep and the tested sweep are different queries. The
//     concurrency tests are the E3 acceptance criterion; a test that exercises a
//     narrowed query proves nothing about the one that runs in production.
//   - A table prefix is the same isolation this achieves, but requires the
//     production SQL to interpolate table names.
//   - A database per test is heavier (CREATE DATABASE cannot run in a
//     transaction, and migration 0001's ALTER DATABASE would run per test) for
//     the same guarantee.
//
// Production code is untouched: no GC filter, no test hook, no exported knob.
// The isolation is entirely in where the tables live.

// schemaCounter disambiguates schemas created inside the same nanosecond.
var schemaCounter atomic64

type atomic64 struct {
	mu sync.Mutex
	n  int64
}

func (a *atomic64) next() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n++
	return a.n
}

// isolatedSchemaDSN creates a dedicated schema for one test, migrates the Moov
// schema into it, and returns a DSN whose connections default to it.
//
// The schema is dropped when the test finishes, so a run leaves the database as
// it found it.
func isolatedSchemaDSN(t *testing.T, dsn string) string {
	t.Helper()

	schema := uniqueSchemaName(t)

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = admin.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := admin.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA %s", quoteIdent(schema))); err != nil {
		t.Fatalf("creating schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		// A fresh connection: the pool this schema served is already closed.
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancelCleanup()

		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Logf("cleanup: sql.Open: %v", err)
			return
		}
		defer func() { _ = db.Close() }()

		if _, err := db.ExecContext(cleanupCtx,
			fmt.Sprintf("DROP SCHEMA %s CASCADE", quoteIdent(schema))); err != nil {
			t.Logf("cleanup: dropping schema %s: %v", schema, err)
		}
	})

	scoped := dsnWithSearchPath(t, dsn, schema)

	// Migrate INTO the new schema: the connection's search_path decides where
	// goose puts both its version table and the schema itself.
	migrateDB, err := sql.Open("pgx", scoped)
	if err != nil {
		t.Fatalf("sql.Open (scoped): %v", err)
	}
	// One connection only, so every statement lands on the same session and
	// therefore the same search_path.
	migrateDB.SetMaxOpenConns(1)
	if err := store.Migrate(ctx, migrateDB); err != nil {
		_ = migrateDB.Close()
		t.Fatalf("Migrate into %s: %v", schema, err)
	}
	if err := migrateDB.Close(); err != nil {
		t.Fatalf("closing migration connection: %v", err)
	}

	return scoped
}

// uniqueSchemaName derives a schema name from the test's name, so a failure
// left mid-debug is traceable to the test that made it.
func uniqueSchemaName(t *testing.T) string {
	t.Helper()

	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, t.Name())

	// PostgreSQL identifiers truncate at 63 bytes; leave room for the suffix.
	const maxBase = 30
	if len(safe) > maxBase {
		safe = safe[:maxBase]
	}
	return fmt.Sprintf("moovtest_%s_%d_%d", safe, time.Now().UnixNano(), schemaCounter.next())
}

// dsnWithSearchPath returns dsn with search_path pinned to schema.
//
// It goes in the DSN rather than in an AfterConnect hook so that BOTH the
// database/sql connection used for migrations and the pgxpool used by the store
// resolve tables identically — one authority for where this test's tables live.
func dsnWithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parsing %s: %v", testDBEnv, err)
	}
	q := u.Query()
	// public stays on the path for the extensions migration 0001 installs
	// there (unaccent's functions are resolved by name at query time).
	q.Set("search_path", quoteIdent(schema)+",public")
	u.RawQuery = q.Encode()
	return u.String()
}

// isolatedPool opens a pool whose connections all see the test's own schema.
func isolatedPool(t *testing.T, scopedDSN string) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.New(context.Background(), scopedDSN)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// quoteIdent quotes a PostgreSQL identifier. The names this file builds are
// already restricted to [a-z0-9_], so this is belt and braces around an
// interpolation that would otherwise be the one injection point here.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
