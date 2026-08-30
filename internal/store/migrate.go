package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

// migrationsFS holds the SQL migrations, embedded in the binary.
//
// Embedding rather than shipping a directory is deliberate: a moovd binary
// carries the exact schema it was built against, so a container image can never
// be deployed alongside the wrong migration set, and running migrations needs
// no volume mount and no second image.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// MigrationsDir is the path of the embedded migrations inside migrationsFS.
const MigrationsDir = "migrations"

// Migrate applies every pending migration to db, in order.
//
// It is idempotent: goose records applied versions in its own table, so running
// it on every start — which is what moovd does — is safe and is how a deploy
// stays in step with its schema.
func Migrate(ctx context.Context, db *sql.DB) error {
	provider, err := newProvider(db)
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	return nil
}

// MigrateDown rolls back exactly one migration. It exists for development and
// for tests; production rollbacks are a deliberate operator action, never
// automatic.
func MigrateDown(ctx context.Context, db *sql.DB) error {
	provider, err := newProvider(db)
	if err != nil {
		return err
	}
	if _, err := provider.Down(ctx); err != nil {
		return fmt.Errorf("rolling back migration: %w", err)
	}
	return nil
}

// MigrateDownTo rolls back every migration ABOVE version, leaving version
// itself applied. Passing 0 rolls everything back.
//
// It exists because MigrateDown ("exactly one") makes a caller's correctness
// depend on which migration happens to be the head — a test that rolls back
// one step to reach the state before migration N silently starts testing
// migration N+1 the day one is added. Naming the target instead of counting
// steps is invariant under new migrations, which is the property a test that
// exercises a SPECIFIC migration needs.
//
// Like MigrateDown it is for development and tests; production rollbacks stay
// a deliberate operator action.
func MigrateDownTo(ctx context.Context, db *sql.DB, version int64) error {
	provider, err := newProvider(db)
	if err != nil {
		return err
	}
	if _, err := provider.DownTo(ctx, version); err != nil {
		return fmt.Errorf("rolling back migrations above version %d: %w", version, err)
	}
	return nil
}

// MigrationVersion reports the highest migration version applied to db.
func MigrationVersion(ctx context.Context, db *sql.DB) (int64, error) {
	provider, err := newProvider(db)
	if err != nil {
		return 0, err
	}
	v, err := provider.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading migration version: %w", err)
	}
	return v, nil
}

func newProvider(db *sql.DB) (*goose.Provider, error) {
	// The embedded FS is rooted at the repository package, so goose is pointed
	// at the migrations subdirectory explicitly.
	sub, err := fs.Sub(migrationsFS, MigrationsDir)
	if err != nil {
		return nil, fmt.Errorf("opening embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, sub)
	if err != nil {
		return nil, fmt.Errorf("creating migration provider: %w", err)
	}
	return provider, nil
}
