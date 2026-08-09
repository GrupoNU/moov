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
	"github.com/GrupoNU/moov/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print version information and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Get().String())
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
// E1 has no components to run, so it blocks on the signal. As epics land, each
// long-lived component (sync supervisor E5/E6, operational HTTP server E8) is
// started here and stopped in reverse order under shutdownTimeout.
func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	logger.Info("moovd is running; no sync components are wired yet (E1 scaffolding)",
		"http_addr", cfg.HTTPAddr,
	)

	<-ctx.Done()
	logger.Info("signal received, shutting down", "grace", cfg.ShutdownTimeout)

	// The shutdown context is derived from Background, not from the canceled
	// ctx: components need a live context to finish their work in flight.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := shutdown(shutdownCtx, logger); err != nil {
		return fmt.Errorf("shutdown: %w", err)
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
