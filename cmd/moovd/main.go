// Command moovd is the Moov Mail sync engine daemon.
//
// It synchronizes Dovecot mailboxes over IMAP into Moov's own PostgreSQL store
// (metadata + full-text index + content-addressed blobs), which the JMAP layer
// then reads. The full design is docs/specs/L2-sync-engine.md; the architecture
// decision behind it is docs/adr/ADR-001-arquitectura.md.
//
// This is the E1 skeleton: it loads configuration, sets up structured logging,
// and implements the process lifecycle (start, wait for a signal, shut down
// within a bounded deadline). The sync workers themselves arrive with E5/E6 and
// plug into the run loop below. There is deliberately no business logic here.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GrupoNU/moov/internal/config"
	"github.com/GrupoNU/moov/internal/metrics"
	"github.com/GrupoNU/moov/internal/store"
	syncengine "github.com/GrupoNU/moov/internal/sync"
	"github.com/GrupoNU/moov/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print version information and exit")
	// -health exists for the container healthcheck. The production image is
	// distroless: no shell, no curl, nothing to probe with but this binary, so
	// the binary probes itself (see deploy/docker-compose.yml).
	healthCheck := flag.Bool("health", false,
		"probe this daemon's own /healthz and exit 0 if healthy (for container healthchecks)")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Get().String())
		return
	}

	if *healthCheck {
		if err := probeHealth(); err != nil {
			fmt.Fprintf(os.Stderr, "moovd: unhealthy: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		// The logger may not exist yet when configuration itself failed, so
		// this last-resort report goes straight to stderr.
		fmt.Fprintf(os.Stderr, "moovd: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	logger := newLogger(cfg, os.Stderr)
	slog.SetDefault(logger)

	build := version.Get()
	logger.Info("starting moovd",
		"version", build.Version,
		"commit", build.Commit,
		"built", build.Date,
		"go", build.Go,
		"pid", os.Getpid(),
	)
	logger.Debug("effective configuration", "config", cfg.String())

	// NotifyContext cancels ctx on the first SIGINT/SIGTERM. The second one is
	// left to the default handler so an operator can always kill a wedged
	// process without waiting for the shutdown deadline.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runErr := serve(ctx, cfg, logger)

	stop()
	logger.Info("shutdown complete")

	// A canceled context is how a clean shutdown ends; it is not a failure.
	if errors.Is(runErr, context.Canceled) {
		return nil
	}
	return runErr
}

// serve runs the daemon until the context is canceled or a component fails.
//
// E5 starts the sync supervisor here. It is opt-in (MOOV_SYNC_ENABLED=1) so
// that a daemon without a reachable Dovecot — CI, a first boot before
// provisioning — still starts and reports itself healthy instead of crash
// looping. The operational HTTP server (E8) joins this list later and is
// stopped in reverse start order under ShutdownTimeout.
func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	// A component that fails on its own (the JMAP listener dying, say) must
	// bring the daemon down cleanly rather than leave it half-alive; the
	// cause-carrying cancel is how it reports why.
	ctx, fail := context.WithCancelCause(ctx)
	defer fail(nil)

	// Migrations first, before any component opens a pool against the schema
	// they are about to change. A failure here is fatal: serving JMAP reads
	// against a half-migrated store would answer with missing columns rather
	// than with an error an operator can act on.
	if err := migrateOnStart(ctx, cfg, logger); err != nil {
		return fmt.Errorf("startup migrations: %w", err)
	}

	// One metric set for the whole process, shared by the JMAP server (HTTP and
	// per-method observations) and the operational endpoint that exposes it.
	m := metrics.New()

	// The push broker (W4a) is created here, above both components, because it
	// is the seam BETWEEN them: the sync engine publishes to it and the JMAP
	// server's EventSource endpoint subscribes to it. Either component may be
	// disabled independently, and the broker is harmless in both directions —
	// publishing with no subscribers is a map lookup, subscribing with no
	// publisher is a channel nobody writes to.
	broker := syncengine.NewBroker()

	startCtx, cancelStart := context.WithTimeout(ctx, syncStartTimeout)
	components, err := startSync(startCtx, cfg, logger, broker)
	if err != nil {
		cancelStart()
		return fmt.Errorf("starting sync: %w", err)
	}
	defer components.close()

	jmapComp, err := startJMAP(startCtx, cfg, logger, m, broker, fail)
	if err != nil {
		cancelStart()
		return fmt.Errorf("starting jmap: %w", err)
	}

	// The operational server starts last and is given the sync engine's store
	// so its collectors can read checkpoints; with sync disabled it gets nil and
	// simply reports no sync series.
	var syncStore *store.Store
	if components != nil {
		syncStore = components.store
	}
	opsComp, err := startOps(startCtx, cfg, m, syncStore, logger, fail)
	cancelStart()
	if err != nil {
		return fmt.Errorf("starting operational server: %w", err)
	}

	logger.Info("moovd is running",
		"http_addr", cfg.HTTPAddr,
		"sync", components != nil,
		"jmap", jmapComp != nil,
		"ops", opsComp != nil,
	)

	// runSync blocks until ctx ends; with the supervisor disabled it simply
	// waits for the signal, which keeps this function's shape identical in
	// both configurations.
	runErr := runSync(ctx, components, logger)

	logger.Info("signal received, shutting down", "grace", cfg.ShutdownTimeout)

	// The shutdown context is derived from Background, not from the canceled
	// ctx: components need a live context to finish their work in flight.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	// The broker closes FIRST, before any server drains: closing it ends every
	// EventSource subscription, so each streaming handler returns and its
	// response finishes normally (W-A4's "cierre limpio en shutdown"). Without
	// this, Shutdown would wait out its whole grace period on connections that
	// are healthy and, by design, never end on their own.
	broker.Close()

	// Then reverse start order: the operational server drains first (nothing
	// depends on it), then the JMAP server, so in-flight API requests finish
	// against a still-open store.
	opsComp.shutdown(shutdownCtx)
	jmapComp.shutdown(shutdownCtx)

	if err := shutdown(shutdownCtx, logger); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	// A component that failed for its own reasons is reported; a cancellation
	// is the normal end of a clean shutdown and is left to run() to interpret.
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return runErr
	}
	// When the shutdown was triggered by a component failure (see `fail`
	// above), the cause is the story; a plain context.Canceled is a normal
	// signal-driven exit.
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return ctx.Err()
}

// shutdown stops the running components in reverse start order, bounded by the
// deadline already set on ctx.
func shutdown(ctx context.Context, logger *slog.Logger) error {
	// Nothing to stop yet. The deadline is still honored so the shape of the
	// contract — "shutdown either completes or reports why it could not" — is
	// exercised from day one instead of being retrofitted later.
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			logger.Warn("shutdown deadline exceeded, exiting anyway")
			return nil
		}
		return ctx.Err()
	default:
		return nil
	}
}

// newLogger builds the structured logger described by cfg. JSON in production
// so log pipelines can index the fields; text for a readable dev console.
func newLogger(cfg config.Config, w *os.File) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// Timestamps in UTC RFC 3339, so logs from containers in different
			// zones sort and correlate.
			if a.Key == slog.TimeKey {
				if t, ok := a.Value.Any().(time.Time); ok {
					a.Value = slog.StringValue(t.UTC().Format(time.RFC3339Nano))
				}
			}
			return a
		},
	}

	var h slog.Handler
	if cfg.LogFormat == "text" {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h).With("service", "moovd")
}
