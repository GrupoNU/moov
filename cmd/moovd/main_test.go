package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/config"
)

// serve must return once its context is canceled, and it must not report the
// cancellation as an unexpected failure to the caller.
func TestServeStopsOnContextCancel(t *testing.T) {
	cfg := config.Config{
		LogLevel: "error", LogFormat: "text",
		HTTPAddr:        ":0",
		DatabaseURL:     "postgres://moov@localhost/moov",
		ShutdownTimeout: time.Second,
	}
	logger := newLogger(cfg, discardFile(t))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, logger) }()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serve() = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve() did not return within 5s of cancellation")
	}
}

// A shutdown context that is already past its deadline must not turn into an
// error: the daemon logs and exits rather than hanging.
func TestShutdownToleratesExpiredDeadline(t *testing.T) {
	cfg := config.Config{LogLevel: "error", LogFormat: "text"}
	logger := newLogger(cfg, discardFile(t))

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	if err := shutdown(ctx, logger); err != nil {
		t.Fatalf("shutdown() = %v, want nil", err)
	}
}

func TestNewLoggerHonoursLevel(t *testing.T) {
	f := discardFile(t)

	quiet := newLogger(config.Config{LogLevel: "error", LogFormat: "json"}, f)
	if quiet.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("logger at level=error should not be enabled for debug")
	}

	loud := newLogger(config.Config{LogLevel: "debug", LogFormat: "text"}, f)
	if !loud.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("logger at level=debug should be enabled for debug")
	}
}

// discardFile returns an *os.File that swallows writes, so tests do not print
// log output. os.DevNull resolves correctly on Linux and Windows alike.
func discardFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}
