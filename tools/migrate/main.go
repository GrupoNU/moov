// Command migrate applies Moov's database migrations from the command line.
//
// moovd applies migrations itself on start, so this tool is not part of the
// deployment path. It exists for development and for operators: applying
// migrations to a fresh dev database, inspecting the current version, and
// rolling one back deliberately.
//
// Usage:
//
//	migrate [-db DSN] up        apply every pending migration (default)
//	migrate [-db DSN] down      roll back exactly one migration
//	migrate [-db DSN] version   print the applied version
//
// The DSN comes from -db, or from MOOV_DATABASE_URL, or from
// MOOV_TEST_DATABASE_URL, in that order.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"

	"github.com/GrupoNU/moov/internal/store"
)

func main() {
	dsn := flag.String("db", "", "PostgreSQL connection string (default: $MOOV_DATABASE_URL)")
	timeout := flag.Duration("timeout", 2*time.Minute, "overall timeout")
	flag.Parse()

	if err := run(*dsn, flag.Arg(0), *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run(dsn, command string, timeout time.Duration) error {
	if dsn == "" {
		for _, env := range []string{"MOOV_DATABASE_URL", "MOOV_TEST_DATABASE_URL"} {
			if v := os.Getenv(env); v != "" {
				dsn = v
				break
			}
		}
	}
	if dsn == "" {
		return fmt.Errorf("no database configured: pass -db or set MOOV_DATABASE_URL")
	}
	if command == "" {
		command = "up"
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connecting: %w", err)
	}

	switch command {
	case "up":
		if err := store.Migrate(ctx, db); err != nil {
			return err
		}
	case "down":
		if err := store.MigrateDown(ctx, db); err != nil {
			return err
		}
	case "version":
	default:
		return fmt.Errorf("unknown command %q: want up, down or version", command)
	}

	v, err := store.MigrationVersion(ctx, db)
	if err != nil {
		return err
	}
	fmt.Printf("migration version: %d\n", v)
	return nil
}
