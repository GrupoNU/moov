package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/config"
	"github.com/GrupoNU/moov/internal/crypto"
	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/metrics"
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

	// writer is the engine's own write executor (L3 epic E4): the mute
	// archiver and the snooze waker both apply changes to Dovecot, and neither
	// is a client request, so neither may go through the JMAP component's
	// executor. nil when it could not be built, which degrades those two
	// features loudly and leaves the rest of the engine untouched.
	writer *syncengine.WriteExecutor

	// waker returns snoozed mail when its time comes. nil without a writer.
	waker *syncengine.Waker
}

// startSync builds the sync supervisor, or returns nil when it is not enabled.
//
// It returns a nil *syncComponents and a nil error for the disabled case, which
// is deliberate: "not configured" is a normal state for a daemon that has not
// been provisioned yet, not a failure to report.
func startSync(ctx context.Context, cfg config.Config, logger *slog.Logger, m *metrics.Metrics, broker *syncengine.Broker) (*syncComponents, error) {
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

	// The engine's own write executor (L3 epic E4). It is SEPARATE from the
	// JMAP component's, and that is deliberate rather than an oversight: the
	// two components already keep separate stores and separate connection
	// pools, and the sync side needs the executor for two operations no client
	// asked for — archiving a muted thread's reply, and returning a snoozed
	// message when its hour comes. Sharing one executor across components
	// would mean the sync engine's mute archiving competes for the same cached
	// per-account IMAP connection a user's clicks are using, which is exactly
	// the contention that made deleting a folder slow (W4b).
	//
	// A failure to build it is NOT fatal: mute and snooze wake stop working
	// (loudly), the rest of the engine syncs mail as before.
	writer, werr := syncengine.NewWriteExecutor(st, connector, syncengine.WriteOptions{
		Logger: logger,
		Broker: broker,
		Blobs:  blobs,
	})
	if werr != nil {
		logger.Warn("the engine's write executor could not be built; "+
			"muted threads will not be archived and snoozes will not wake", "error", werr)
	} else {
		archiver := syncengine.NewMuteArchiver(st, writer)
		archiver.Observer = triageMetrics{m}
		opts.Mutes = archiver
	}

	supOpts := syncengine.SupervisorOptions{
		Options:     opts,
		Connector:   connector,
		Concurrency: cfg.Sync.Accounts,
	}

	if cfg.Sync.WatcherEnabled {
		// The watcher-liveness gauge (the 2026-09-16 incident). It is wired
		// here, not inside internal/sync, for the same reason every other
		// observer seam in this daemon is: the engine must be buildable and
		// testable without a metrics registry, so it declares a callback and
		// this file is the only place that knows an exporter exists.
		activity := newWatcherActivity(m)
		watcher, werr := syncengine.NewPushWatcher(st, blobs, syncengine.WatcherOptions{
			Options:           opts,
			Connector:         connector,
			Debounce:          cfg.Sync.Debounce,
			ReconcileInterval: cfg.Sync.ReconcileInterval,
			IdleHeartbeat:     cfg.Sync.IdleHeartbeat,
			BreakerThreshold:  cfg.Sync.BreakerThreshold,
			BreakerCooldown:   cfg.Sync.BreakerCooldown,
			OnEvent:           activity.observe,
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

	comp := &syncComponents{store: st, supervisor: sup, writer: writer}

	// The snooze waker (L3 epic E4). It rides with the sync engine rather than
	// with the JMAP server because it is engine work: nothing a client asked
	// for is in flight, and it must keep running on a deployment that serves no
	// HTTP at all. It shares the engine's executor for the same reason the mute
	// archiver does.
	if writer != nil {
		waker, kerr := syncengine.NewWaker(st, writer, syncengine.WakerOptions{
			Logger:   logger,
			Observer: triageMetrics{m},
		})
		if kerr != nil {
			logger.Warn("the snooze waker could not be built; snoozed mail will not return on its own",
				"error", kerr)
		} else {
			comp.waker = waker
		}
	}

	logger.Info("sync supervisor configured",
		"blob_root", cfg.Sync.BlobRoot, "watcher", cfg.Sync.WatcherEnabled,
		"triage", writer != nil)
	return comp, nil
}

// watcherActivity turns the sync engine's watcher observations into the
// per-account liveness gauge (moov_sync_watcher_idle_seconds).
//
// # Why this lives here and not in internal/sync
//
// Because internal/sync must not import internal/metrics. That rule is not
// tidiness: the engine's tests construct watchers by the dozen, and an engine
// whose correctness depends on a metrics registry being present is one that
// cannot be tested without building one. So the engine declares a callback
// (WatcherOptions.OnEvent, already there for exactly this) and this file — the
// only place in the daemon that knows an exporter exists at all — adapts it.
// Same seam shape as submit.Observer and sync.MuteObserver.
//
// # Why a collector and not a Set on every observation
//
// The number that matters is how long a watcher has been SILENT, and silence
// produces no callbacks by definition. A gauge written only when something
// happens would freeze at its last value during exactly the outage it exists to
// reveal — which is the mistake moov_sync_lag_seconds made in a different form.
// So the observations record a timestamp, and the gauge is computed at scrape
// time as "now minus that", which rises on its own for as long as nothing
// happens.
//
// # Why the stuck-divergence counter rides the same seam
//
// Because it is the same shape of fact reported through the same callback, and
// the alternative — a second observer wired into the same Options field — would
// need one of them to call the other. One adapter, one registration, both
// series. The counter half needs no collector: unlike silence, a stuck
// divergence produces an observation every time it happens.
type watcherActivity struct {
	mu   sync.Mutex
	last map[int64]time.Time

	// m is the exporter, or nil when metrics are disabled. Only the
	// stuck-divergence counter needs it at observation time; the idle gauge is
	// rendered by the collector below.
	m *metrics.Metrics
}

// newWatcherActivity installs the collector and returns the observer.
func newWatcherActivity(m *metrics.Metrics) *watcherActivity {
	a := &watcherActivity{last: map[int64]time.Time{}, m: m}
	if m == nil {
		return a
	}
	m.WatcherIdleSeconds.SetCollector(a.samples)
	return a
}

// observe records that an account's watcher did something. It is called from
// the watcher's own goroutine and must not block (WatcherOptions.OnEvent's
// contract), which a map write under a mutex satisfies.
func (a *watcherActivity) observe(obs syncengine.WatchObservation) {
	if obs.AccountID == 0 {
		return
	}
	a.mu.Lock()
	a.last[obs.AccountID] = time.Now()
	a.mu.Unlock()

	// A sweep that found a divergence it could not repair (the 2026-09-17
	// defect). It is counted here and NOT treated as an error: nothing failed,
	// every call returned nil, and that is exactly what made the defect
	// invisible for five weeks.
	if obs.Kind == syncengine.ObsStuckDivergence && a.m != nil {
		a.m.IncStuckDivergence(obs.AccountID)
	}
}

// samples renders the gauge at scrape time.
func (a *watcherActivity) samples() []metrics.Sample {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	out := make([]metrics.Sample, 0, len(a.last))
	for id, at := range a.last {
		out = append(out, metrics.Sample{
			Labels: metrics.Labels{"account": strconv.FormatInt(id, 10)},
			Value:  now.Sub(at).Seconds(),
		})
	}
	return out
}

// close releases the components' resources.
func (c *syncComponents) close() {
	if c == nil {
		return
	}
	if c.writer != nil {
		c.writer.Close()
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
//
// The waker runs ALONGSIDE it, in its own goroutine, and its failure is
// deliberately not the supervisor's: a waker that cannot reach the database
// must not stop mail from syncing. It shares ctx, so shutdown stops both.
func runSync(ctx context.Context, c *syncComponents, logger *slog.Logger) error {
	if c == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	if c.waker != nil {
		go func() {
			if err := c.waker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("snooze waker stopped", "error", err)
			}
		}()
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
