package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/config"
	"github.com/GrupoNU/moov/internal/crypto"
	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
	syncengine "github.com/GrupoNU/moov/internal/sync"
)

// The sync engine's wiring: supervisor (E5) plus push watcher (E6).
//
// Configuration comes from internal/config, which E6 folded E5's ad-hoc
// os.Getenv calls into. Credentials come from internal/crypto (E7): this file
// is where an account's stored ciphertext becomes an IMAP password, and it is
// deliberately the ONLY place in the daemon that can perform that conversion.

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
func startSync(ctx context.Context, cfg config.Config, logger *slog.Logger, broker *syncengine.Broker) (*syncComponents, error) {
	if !cfg.Sync.Enabled {
		logger.Info("sync supervisor disabled", "hint", "MOOV_SYNC_ENABLED=1 enables it")
		return nil, nil //nolint:nilnil // "disabled" is a valid, non-error outcome
	}

	// The keyring is loaded ONCE, at startup, and a failure here is fatal.
	//
	// Both properties are deliberate. Loading once means the master key is read
	// from the environment or its file at a single known moment rather than on
	// every connection, so a key file that is rotated underneath a running
	// process cannot half-apply. Failing fatally means a daemon that cannot
	// decrypt credentials refuses to start instead of running and reporting
	// every account as broken — which is the same outcome, arrived at hours
	// later and looking like a Dovecot problem.
	keyring, err := crypto.LoadKeyring()
	if err != nil {
		return nil, fmt.Errorf("loading the master keyring: %w", err)
	}
	logger.Info("credential keyring loaded", "key_ids", keyring.IDs(), "primary", keyring.PrimaryID())

	st, err := store.Open(ctx, store.Config{DSN: cfg.DatabaseURL})
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}

	blobs, err := blob.New(blob.Config{Root: cfg.Sync.BlobRoot, Pool: st.Pool()})
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("opening blob store: %w", err)
	}

	opts := syncengine.Options{
		Logger:       logger,
		Connections:  cfg.Sync.Connections,
		ParseWorkers: cfg.Sync.ParseWorkers,
		// W4a: every sync path that advances an account's state notifies the
		// broker, which the JMAP EventSource endpoint fans out to browsers.
		// SupervisorOptions and WatcherOptions both embed these Options, so
		// this one assignment reaches the incremental passes and the watcher.
		Broker: broker,
	}

	dialer := &accountDialer{keyring: keyring, serverName: cfg.Sync.IMAPServerName, logger: logger}
	connector := syncengine.ConnectorFunc(dialer.connect)

	supOpts := syncengine.SupervisorOptions{
		Options:     opts,
		Connector:   connector,
		Concurrency: cfg.Sync.Accounts,
	}

	if cfg.Sync.WatcherEnabled {
		watcher, werr := syncengine.NewPushWatcher(st, blobs, syncengine.WatcherOptions{
			Options:           opts,
			Connector:         connector,
			Debounce:          cfg.Sync.Debounce,
			ReconcileInterval: cfg.Sync.ReconcileInterval,
			BreakerThreshold:  cfg.Sync.BreakerThreshold,
			BreakerCooldown:   cfg.Sync.BreakerCooldown,
		})
		if werr != nil {
			st.Close()
			return nil, fmt.Errorf("building push watcher: %w", werr)
		}
		supOpts.Watcher = watcher
	} else {
		logger.Warn("push watcher disabled; the engine will sync once and idle",
			"hint", "MOOV_SYNC_WATCHER=1 re-enables it")
	}

	sup, err := syncengine.NewSupervisor(st, blobs, supOpts)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("building sync supervisor: %w", err)
	}

	logger.Info("sync supervisor configured",
		"blob_root", cfg.Sync.BlobRoot, "watcher", cfg.Sync.WatcherEnabled)
	return &syncComponents{store: st, supervisor: sup}, nil
}

// close releases the components' resources.
func (c *syncComponents) close() {
	if c == nil {
		return
	}
	c.store.Close()
}

// accountDialer opens IMAP connections for an account, decrypting its stored
// app password on the way.
//
// It is a type rather than a closure so the keyring has exactly one owner and
// one lifetime, and so the decryption path is a named thing that can be pointed
// at in a review.
type accountDialer struct {
	keyring *crypto.Keyring

	// serverName overrides the certificate name for every account, for the
	// deployment where Dovecot is reached by a container alias (S1 H2).
	serverName string

	logger *slog.Logger

	// mu guards nothing but is the seam where a future connection budget across
	// accounts would live; it exists so that the dialer is safe to share across
	// the supervisor's per-account goroutines today, which it is by being
	// stateless.
	mu sync.Mutex
}

// connect opens n IMAP connections for one account.
func (d *accountDialer) connect(ctx context.Context, account store.Account, n int) ([]imap.Client, error) {
	password, err := d.password(account)
	if err != nil {
		return nil, fmt.Errorf("account %d: %w", account.ID, err)
	}

	serverName := d.serverName
	if serverName == "" {
		serverName = account.IMAPServerName
	}

	cfg := imap.Config{
		Host:          account.IMAPHost,
		Port:          account.IMAPPort,
		Username:      account.IMAPUsername,
		Password:      password,
		TLSServerName: serverName,
	}

	clients := make([]imap.Client, 0, n)
	for range n {
		c := imap.New(d.logger)
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

// password decrypts an account's stored app password.
//
// # The AAD is the account id, and that is load-bearing
//
// crypto.AccountAAD binds the ciphertext to the account it belongs to, so an
// envelope copied from one account's row into another's fails to open rather
// than silently authenticating as the wrong mailbox. That is a real failure
// mode for a multi-tenant engine — a bad restore, a mistaken UPDATE — and the
// AAD turns it from "reads someone else's mail" into an error.
func (d *accountDialer) password(account store.Account) (string, error) {
	if len(account.IMAPAppPassword) == 0 {
		return "", errors.New("no stored credentials")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	plaintext, err := d.keyring.Open(account.IMAPAppPassword, crypto.AccountAAD(account.ID))
	if err != nil {
		return "", fmt.Errorf("decrypting the app password: %w", err)
	}
	return string(plaintext), nil
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

// syncStartTimeout bounds opening the store and the blob directory, so a
// misconfigured database makes the daemon fail fast instead of hanging on
// start.
const syncStartTimeout = 30 * time.Second
