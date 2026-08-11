package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
	syncengine "github.com/GrupoNU/moov/internal/sync"
)

// The sync supervisor's wiring (E5).
//
// Configuration is read from the environment here rather than through
// internal/config because the settings below belong to this epic and adding
// them to the shared Config would make every consumer of that package depend on
// E5's shape. When E6 lands and the set stabilizes, they move there as one
// change.

// Environment variables this file reads.
const (
	// envSyncEnabled turns the supervisor on. It is opt-in for now: the daemon
	// must remain startable without a reachable Dovecot, which is what CI and a
	// bare `moovd -version` depend on.
	envSyncEnabled = "MOOV_SYNC_ENABLED"

	// envBlobRoot is the directory the content-addressed blobs live in.
	envBlobRoot = "MOOV_BLOB_ROOT"

	// envSyncConnections overrides the per-account IMAP connection budget.
	envSyncConnections = "MOOV_SYNC_CONNECTIONS"

	// envSyncParseWorkers overrides the CPU-bound parse pool (default
	// GOMAXPROCS — S3 H6).
	envSyncParseWorkers = "MOOV_SYNC_PARSE_WORKERS"

	// envSyncAccounts is how many accounts are initially synced at once.
	envSyncAccounts = "MOOV_SYNC_ACCOUNTS"

	// envIMAPServerName is the name the Dovecot certificate is verified
	// against, which differs from the host Moov dials (S1 H2).
	envIMAPServerName = "MOOV_IMAP_SERVER_NAME"

	// defaultBlobRoot is where blobs go when nothing else is configured.
	defaultBlobRoot = "/var/lib/moov/blobs"
)

// syncComponents holds everything the supervisor needs, so shutdown can release
// it in one place.
type syncComponents struct {
	store      *store.Store
	supervisor *syncengine.Supervisor
}

// startSync builds the sync supervisor, or returns nil when it is not enabled.
//
// It returns a nil *syncComponents and a nil error for the disabled case, which
// is deliberate: "not configured" is a normal state for a daemon that has not
// been provisioned yet, not a failure to report.
func startSync(ctx context.Context, dsn string, logger *slog.Logger) (*syncComponents, error) {
	if os.Getenv(envSyncEnabled) != "1" {
		logger.Info("sync supervisor disabled", "hint", envSyncEnabled+"=1 enables it")
		return nil, nil //nolint:nilnil // "disabled" is a valid, non-error outcome
	}

	st, err := store.Open(ctx, store.Config{DSN: dsn})
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}

	blobRoot := envOr(envBlobRoot, defaultBlobRoot)
	blobs, err := blob.New(blob.Config{Root: blobRoot, Pool: st.Pool()})
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("opening blob store: %w", err)
	}

	opts := syncengine.Options{Logger: logger}
	if n, err := envInt(envSyncConnections); err != nil {
		st.Close()
		return nil, err
	} else if n > 0 {
		opts.Connections = n
	}
	if n, err := envInt(envSyncParseWorkers); err != nil {
		st.Close()
		return nil, err
	} else if n > 0 {
		opts.ParseWorkers = n
	}

	supOpts := syncengine.SupervisorOptions{
		Options: opts,
		Connector: syncengine.ConnectorFunc(
			func(ctx context.Context, a store.Account, n int) ([]imap.Client, error) {
				return dialAccount(ctx, a, n, logger)
			}),

		// THE E6 SEAM: leaving this nil means the supervisor performs initial
		// syncs and then idles, which is exactly E5's scope. E6 sets it to its
		// NOTIFY+IDLE watcher and nothing else in this file changes.
		Watcher: nil,
	}
	if n, err := envInt(envSyncAccounts); err != nil {
		st.Close()
		return nil, err
	} else if n > 0 {
		supOpts.Concurrency = n
	}

	sup, err := syncengine.NewSupervisor(st, blobs, supOpts)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("building sync supervisor: %w", err)
	}

	logger.Info("sync supervisor configured", "blob_root", blobRoot)
	return &syncComponents{store: st, supervisor: sup}, nil
}

// close releases the components' resources.
func (c *syncComponents) close() {
	if c == nil {
		return
	}
	c.store.Close()
}

// dialAccount opens n IMAP connections for one account.
//
// # The credential gap (E7)
//
// accounts.imap_app_password holds AES-256-GCM ciphertext, and decrypting it is
// internal/crypto's job. Until that wiring exists, this function refuses rather
// than inventing a fallback: a sync engine that could read plaintext passwords
// from anywhere would defeat the point of encrypting them, and a placeholder
// that "works in development" is how such a fallback survives to production.
func dialAccount(ctx context.Context, account store.Account, n int, logger *slog.Logger) ([]imap.Client, error) {
	if len(account.IMAPAppPassword) == 0 {
		return nil, fmt.Errorf("account %d has no stored credentials", account.ID)
	}

	password, err := decryptAppPassword(account.IMAPAppPassword)
	if err != nil {
		return nil, fmt.Errorf("account %d: %w", account.ID, err)
	}

	cfg := imap.Config{
		Host:          account.IMAPHost,
		Port:          account.IMAPPort,
		Username:      account.IMAPUsername,
		Password:      password,
		TLSServerName: envOr(envIMAPServerName, account.IMAPServerName),
	}

	clients := make([]imap.Client, 0, n)
	for range n {
		c := imap.New(logger)
		if err := c.Connect(ctx, cfg); err != nil {
			for _, open := range clients {
				_ = open.Close()
			}
			return nil, fmt.Errorf("connecting account %d: %w", account.ID, err)
		}
		clients = append(clients, c)
	}
	return clients, nil
}

// errCredentialsNotWired reports that E7's decryption is not connected yet.
var errCredentialsNotWired = errors.New(
	"app password decryption is not wired yet (E7 owns internal/crypto); " +
		"the sync supervisor cannot connect until it is")

// decryptAppPassword is the E7 seam.
func decryptAppPassword(_ []byte) (string, error) {
	return "", errCredentialsNotWired
}

// runSync runs the supervisor until ctx ends, reporting the outcome.
func runSync(ctx context.Context, c *syncComponents, logger *slog.Logger) error {
	if c == nil {
		<-ctx.Done()
		return ctx.Err()
	}

	err := c.supervisor.Run(ctx)
	switch {
	case err == nil, errors.Is(err, context.Canceled):
		return ctx.Err()
	default:
		logger.Error("sync supervisor stopped", "error", err)
		return err
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envInt reads an optional positive integer. Zero means "not set".
func envInt(key string) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s: must not be negative, got %d", key, n)
	}
	return n, nil
}

// syncStartTimeout bounds opening the store and the blob directory, so a
// misconfigured database makes the daemon fail fast instead of hanging on
// start.
const syncStartTimeout = 30 * time.Second
