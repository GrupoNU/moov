package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/config"
	"github.com/GrupoNU/moov/internal/crypto"
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
	cfg := testConfig()
	cfg.Sync.Enabled = false
	logger := newLogger(cfg, discardFile(t))

	components, err := startSync(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("startSync with the supervisor disabled returned %v, want nil", err)
	}
	if components != nil {
		t.Error("startSync built components while disabled")
	}

	// close on the nil case must be safe: serve defers it unconditionally.
	components.close()
}

// TestStartSyncRequiresAKeyring is the security assertion at the daemon's
// boundary: an engine that cannot decrypt credentials must refuse to start.
//
// The failure it prevents is subtle. A daemon that started anyway would run,
// pass its health check, and report every account as a connection failure —
// which looks like a Dovecot outage and gets debugged as one, while the actual
// cause is a missing environment variable that nothing ever printed.
func TestStartSyncRequiresAKeyring(t *testing.T) {
	t.Setenv(crypto.EnvMasterKey, "")
	t.Setenv(crypto.EnvMasterKeyFile, "")

	cfg := testConfig()
	cfg.Sync.Enabled = true
	logger := newLogger(cfg, discardFile(t))

	components, err := startSync(context.Background(), cfg, logger)
	if err == nil {
		components.close()
		t.Fatal("startSync built a sync engine with no master key")
	}
	if !strings.Contains(err.Error(), "keyring") {
		t.Errorf("startSync = %v, want an error naming the keyring", err)
	}
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

// testDialer builds a dialer over a fresh single-key keyring.
func testDialer(t *testing.T) (*accountDialer, *crypto.Keyring) {
	t.Helper()

	material, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	key, err := crypto.NewKey(1, material)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	kr, err := crypto.NewKeyring(key)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}

	cfg := testConfig()
	return &accountDialer{keyring: kr, logger: newLogger(cfg, discardFile(t))}, kr
}

// TestDialerDecryptsAStoredAppPassword is the E7 wiring this epic completed:
// what E5 left as a refusal is now a real round trip through internal/crypto.
func TestDialerDecryptsAStoredAppPassword(t *testing.T) {
	dialer, kr := testDialer(t)

	const secret = "an-app-password-from-mailcow"
	const accountID int64 = 42

	envelope, err := kr.Seal([]byte(secret), crypto.AccountAAD(accountID))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// The ciphertext must not be the plaintext in disguise — the assertion that
	// would catch an encryption path quietly reduced to a copy.
	if strings.Contains(string(envelope), secret) {
		t.Fatal("the sealed envelope contains the plaintext password")
	}

	got, err := dialer.password(store.Account{ID: accountID, IMAPAppPassword: envelope})
	if err != nil {
		t.Fatalf("password: %v", err)
	}
	if got != secret {
		t.Errorf("password = %q, want %q", got, secret)
	}
}

// TestDialerRejectsAnEnvelopeFromAnotherAccount is the multi-tenancy assertion.
//
// The AAD binds a ciphertext to its account, so an envelope moved between rows
// — a bad restore, a mistaken UPDATE, a copied fixture — must fail to open. The
// alternative is an engine that logs into the wrong mailbox and reports success,
// which is the worst failure this system can have.
func TestDialerRejectsAnEnvelopeFromAnotherAccount(t *testing.T) {
	dialer, kr := testDialer(t)

	envelope, err := kr.Seal([]byte("secret"), crypto.AccountAAD(1))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if _, err := dialer.password(store.Account{ID: 2, IMAPAppPassword: envelope}); err == nil {
		t.Fatal("an envelope sealed for account 1 opened for account 2")
	}
}

// TestDialerRefusesWithoutCredentials keeps E5's guarantee: the connector must
// never invent a password for an account that has none.
func TestDialerRefusesWithoutCredentials(t *testing.T) {
	dialer, _ := testDialer(t)

	if _, err := dialer.password(store.Account{ID: 1}); err == nil {
		t.Fatal("password returned a credential for an account with none")
	}
}

// TestDialerRejectsAForeignKeyring proves the envelope is authenticated, not
// merely obscured: a keyring that did not seal it cannot open it.
func TestDialerRejectsAForeignKeyring(t *testing.T) {
	_, kr := testDialer(t)
	other, _ := testDialer(t)

	envelope, err := kr.Seal([]byte("secret"), crypto.AccountAAD(7))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if _, err := other.password(store.Account{ID: 7, IMAPAppPassword: envelope}); err == nil {
		t.Fatal("a foreign keyring opened the envelope")
	}
}
