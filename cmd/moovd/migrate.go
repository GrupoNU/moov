package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx", for goose

	"github.com/GrupoNU/moov/internal/config"
	"github.com/GrupoNU/moov/internal/store"
)

// Schema migration on startup (J4).
//
// # Why this exists at all
//
// internal/store/migrate.go documents Migrate as "called explicitly by moovd on
// start", and internal/store/store.go's Open repeats it — but until J4 nothing
// in cmd/moovd actually called it. Every environment up to this point applied
// the schema out of band (`make migrate`, or a test harness), so the gap never
// showed.
//
// The deploy is what makes it load-bearing. The production image is
// distroless/static (see the Dockerfile): no shell, no `go run ./tools/migrate`,
// no way to exec anything but moovd itself. A daemon that cannot apply its own
// schema would need a second image built solely to run migrations — a deploy
// artifact whose Go version, module set and migration files could all drift from
// the binary that reads the resulting tables. Since the migrations are already
// EMBEDDED in the binary (store.migrationsFS), the daemon carries the exact
// schema it was compiled against, and applying it here is both the simplest and
// the only drift-free option.
//
// # Why it is opt-in rather than automatic
//
// MOOV_MIGRATE_ON_START defaults to FALSE. A process that only reads must not be
// able to alter the schema by accident, which is the property store.Open's
// comment is protecting; and in a multi-replica deployment, three daemons racing
// to migrate on boot is a way to discover lock contention during an incident
// rather than during a review. So the single-writer deploy turns it on
// explicitly (deploy/env.example documents it), and anything else stays inert.
//
// goose itself takes a database-level advisory lock, so even with the flag on,
// concurrent starts serialize rather than corrupt. The opt-in is defense in
// depth over a guarantee, not a substitute for one.

// migrateTimeout bounds the whole migration run.
//
// Generous on purpose: migration 0002 builds the composite GIN index S3 requires,
// and on a store that already holds mail that is minutes of work, not seconds. A
// deploy that takes four minutes is fine; one that gives up halfway through an
// index build is not.
const migrateTimeout = 10 * time.Minute

// migrateOnStart applies pending migrations when the deployment asks for it.
//
// It opens its OWN database/sql handle and closes it before returning: goose
// speaks database/sql while the rest of the daemon speaks pgx natively, and a
// migration connection has no business outliving the migration.
func migrateOnStart(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if !cfg.MigrateOnStart {
		logger.Debug("startup migrations disabled",
			"hint", "MOOV_MIGRATE_ON_START=1 applies pending migrations before serving")
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, migrateTimeout)
	defer cancel()

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("opening a migration connection: %w", err)
	}
	defer func() { _ = db.Close() }()

	// A single connection: goose runs its statements in order, and a pool would
	// only add ways for an advisory lock to be taken on one connection and
	// awaited on another.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("reaching the database to migrate: %w", err)
	}

	start := time.Now()
	logger.Info("applying database migrations")
	if err := store.Migrate(ctx, db); err != nil {
		return fmt.Errorf("migrating: %w", err)
	}
	logger.Info("database migrations applied", "took", time.Since(start).Round(time.Millisecond))
	return nil
}
