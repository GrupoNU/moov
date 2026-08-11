package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/config"
	"github.com/GrupoNU/moov/internal/store"
)

// testConfig is a minimal valid configuration for the wiring tests, which never
// reach a database.
func testConfig() config.Config {
	return config.Config{
		LogLevel:        "error",
		LogFormat:       "text",
		HTTPAddr:        ":0",
		DatabaseURL:     "postgres://moov@localhost/moov",
		ShutdownTimeout: time.Second,
	}
}

// TestStartSyncIsOptIn checks that the daemon starts without a database when
// the supervisor is not enabled.
//
// This is what lets `moovd` run in CI and on a freshly provisioned host that
// has no Dovecot yet. A daemon that crash-looped until every dependency existed
// would be undeployable in exactly the situations where it needs to be looked
// at.
func TestStartSyncIsOptIn(t *testing.T) {
	t.Setenv(envSyncEnabled, "")

	cfg := testConfig()
	logger := newLogger(cfg, discardFile(t))

	components, err := startSync(context.Background(), "postgres://unreachable/db", logger)
	if err != nil {
		t.Fatalf("startSync with the supervisor disabled returned %v, want nil", err)
	}
	if components != nil {
		t.Error("startSync built components while disabled")
	}

	// close on the nil case must be safe: serve defers it unconditionally.
	components.close()
}

// TestRunSyncWaitsForCancellationWhenDisabled documents the shape serve()
// depends on: with no components, runSync still blocks until the signal
// arrives rather than returning immediately and looking like a crash.
func TestRunSyncWaitsForCancellationWhenDisabled(t *testing.T) {
	cfg := testConfig()
	logger := newLogger(cfg, discardFile(t))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runSync(ctx, nil, logger) }()

	select {
	case <-done:
		t.Fatal("runSync returned before the context was canceled")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("runSync = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runSync did not return after cancellation")
	}
}

// TestDialAccountRefusesWithoutCredentials is the security assertion of this
// file: the connector must not invent a password.
//
// Until E7 wires decryption, an account whose ciphertext cannot be turned into
// a password must fail loudly. The failure mode this prevents is a placeholder
// that "works in development" and reaches production as a plaintext fallback.
func TestDialAccountRefusesWithoutCredentials(t *testing.T) {
	cfg := testConfig()
	logger := newLogger(cfg, discardFile(t))

	t.Run("no stored credentials", func(t *testing.T) {
		_, err := dialAccount(context.Background(), store.Account{ID: 1}, 1, logger)
		if err == nil {
			t.Fatal("dialAccount connected for an account with no credentials")
		}
	})

	t.Run("ciphertext present but decryption not wired", func(t *testing.T) {
		acct := store.Account{ID: 2, IMAPAppPassword: []byte("ciphertext")}
		_, err := dialAccount(context.Background(), acct, 1, logger)
		if !errors.Is(err, errCredentialsNotWired) {
			t.Fatalf("dialAccount = %v, want errCredentialsNotWired", err)
		}
	})
}

func TestEnvInt(t *testing.T) {
	t.Run("unset means zero", func(t *testing.T) {
		t.Setenv("MOOV_TEST_ENVINT", "")
		n, err := envInt("MOOV_TEST_ENVINT")
		if err != nil || n != 0 {
			t.Errorf("envInt = (%d, %v), want (0, nil)", n, err)
		}
	})

	t.Run("valid value", func(t *testing.T) {
		t.Setenv("MOOV_TEST_ENVINT", "6")
		n, err := envInt("MOOV_TEST_ENVINT")
		if err != nil || n != 6 {
			t.Errorf("envInt = (%d, %v), want (6, nil)", n, err)
		}
	})

	t.Run("garbage is an error, not a silent default", func(t *testing.T) {
		t.Setenv("MOOV_TEST_ENVINT", "many")
		if _, err := envInt("MOOV_TEST_ENVINT"); err == nil {
			t.Error("envInt accepted a non-numeric value")
		}
	})

	t.Run("negative is rejected", func(t *testing.T) {
		t.Setenv("MOOV_TEST_ENVINT", "-3")
		if _, err := envInt("MOOV_TEST_ENVINT"); err == nil {
			t.Error("envInt accepted a negative value")
		}
	})
}
