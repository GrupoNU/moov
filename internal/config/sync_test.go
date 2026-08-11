package config

import (
	"strings"
	"testing"
	"time"
)

// The sync settings E6 folded into this package.

// TestSyncDefaults checks the defaults an unconfigured daemon runs with.
func TestSyncDefaults(t *testing.T) {
	t.Setenv("MOOV_DATABASE_URL", "postgres://moov@localhost/moov")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	if got.Sync.Enabled {
		t.Error("the sync engine defaults to enabled; it must be opt-in so a daemon " +
			"without a reachable Dovecot still starts")
	}
	// Push is the product (regla 1), so an ENABLED engine watches by default.
	if !got.Sync.WatcherEnabled {
		t.Error("the watcher defaults to disabled; push is the normal mode, not an extra")
	}
	if got.Sync.BlobRoot != DefaultBlobRoot {
		t.Errorf("BlobRoot = %q, want %q", got.Sync.BlobRoot, DefaultBlobRoot)
	}
	// Zero means "the engine's own default", which is where the numbers with
	// measurements behind them live.
	for name, v := range map[string]int{
		"Connections":      got.Sync.Connections,
		"ParseWorkers":     got.Sync.ParseWorkers,
		"Accounts":         got.Sync.Accounts,
		"BreakerThreshold": got.Sync.BreakerThreshold,
	} {
		if v != 0 {
			t.Errorf("%s = %d with nothing set, want 0 (meaning the engine's default)", name, v)
		}
	}
	for name, v := range map[string]time.Duration{
		"Debounce":          got.Sync.Debounce,
		"ReconcileInterval": got.Sync.ReconcileInterval,
		"BreakerCooldown":   got.Sync.BreakerCooldown,
	} {
		if v != 0 {
			t.Errorf("%s = %s with nothing set, want 0 (meaning the engine's default)", name, v)
		}
	}
}

// TestSyncReadsEveryVariable is the coverage that stops a setting from being
// added to the struct and silently never read.
func TestSyncReadsEveryVariable(t *testing.T) {
	t.Setenv("MOOV_DATABASE_URL", "postgres://moov@localhost/moov")
	t.Setenv("MOOV_SYNC_ENABLED", "1")
	t.Setenv("MOOV_SYNC_WATCHER", "0")
	t.Setenv("MOOV_BLOB_ROOT", "/srv/moov/blobs")
	t.Setenv("MOOV_SYNC_CONNECTIONS", "3")
	t.Setenv("MOOV_SYNC_PARSE_WORKERS", "12")
	t.Setenv("MOOV_SYNC_ACCOUNTS", "7")
	t.Setenv("MOOV_SYNC_BREAKER_THRESHOLD", "9")
	t.Setenv("MOOV_IMAP_SERVER_NAME", "mail.example.test")
	t.Setenv("MOOV_SYNC_DEBOUNCE", "250ms")
	t.Setenv("MOOV_SYNC_RECONCILE_INTERVAL", "2h")
	t.Setenv("MOOV_SYNC_BREAKER_COOLDOWN", "20m")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	s := got.Sync

	if !s.Enabled {
		t.Error("Enabled = false with MOOV_SYNC_ENABLED=1")
	}
	if s.WatcherEnabled {
		t.Error("WatcherEnabled = true with MOOV_SYNC_WATCHER=0")
	}
	if s.BlobRoot != "/srv/moov/blobs" {
		t.Errorf("BlobRoot = %q", s.BlobRoot)
	}
	if s.Connections != 3 || s.ParseWorkers != 12 || s.Accounts != 7 || s.BreakerThreshold != 9 {
		t.Errorf("integer settings = %d/%d/%d/%d, want 3/12/7/9",
			s.Connections, s.ParseWorkers, s.Accounts, s.BreakerThreshold)
	}
	if s.IMAPServerName != "mail.example.test" {
		t.Errorf("IMAPServerName = %q", s.IMAPServerName)
	}
	if s.Debounce != 250*time.Millisecond {
		t.Errorf("Debounce = %s, want 250ms", s.Debounce)
	}
	if s.ReconcileInterval != 2*time.Hour {
		t.Errorf("ReconcileInterval = %s, want 2h", s.ReconcileInterval)
	}
	if s.BreakerCooldown != 20*time.Minute {
		t.Errorf("BreakerCooldown = %s, want 20m", s.BreakerCooldown)
	}
}

// TestSyncRejectsBadValues checks that a malformed setting is fatal rather than
// silently defaulted.
//
// "Started with the wrong configuration" is worse than "did not start": a
// mistyped reconcile interval that silently becomes six hours is a bug nobody
// finds, while a refusal to boot is a bug everybody finds immediately.
func TestSyncRejectsBadValues(t *testing.T) {
	cases := []struct {
		key, value string
	}{
		{"MOOV_SYNC_CONNECTIONS", "many"},
		{"MOOV_SYNC_CONNECTIONS", "-1"},
		{"MOOV_SYNC_PARSE_WORKERS", "lots"},
		{"MOOV_SYNC_ACCOUNTS", "-4"},
		{"MOOV_SYNC_BREAKER_THRESHOLD", "-1"},
		{"MOOV_SYNC_DEBOUNCE", "soon"},
		{"MOOV_SYNC_DEBOUNCE", "0s"},
		{"MOOV_SYNC_DEBOUNCE", "-1s"},
		{"MOOV_SYNC_RECONCILE_INTERVAL", "never"},
		{"MOOV_SYNC_BREAKER_COOLDOWN", "-5m"},
		{"MOOV_SYNC_ENABLED", "perhaps"},
		{"MOOV_SYNC_WATCHER", "sometimes"},
	}

	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv("MOOV_DATABASE_URL", "postgres://moov@localhost/moov")
			t.Setenv(tc.key, tc.value)

			if _, err := Load(); err == nil {
				t.Errorf("Load() accepted %s=%q", tc.key, tc.value)
			} else if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("Load() = %v, want an error naming %s", err, tc.key)
			}
		})
	}
}

// TestSyncStringIsLoggableAndComplete checks the config dump.
//
// It is the line an operator greps when asking "what is this process actually
// running with", so a setting missing from it is a setting nobody can verify in
// production. And nothing here may be a secret: the master key is read by
// internal/crypto and must never pass through this struct.
func TestSyncStringIsLoggableAndComplete(t *testing.T) {
	t.Setenv("MOOV_DATABASE_URL", "postgres://moov:hunter2@localhost/moov")
	t.Setenv("MOOV_SYNC_ENABLED", "1")
	t.Setenv("MOOV_SYNC_DEBOUNCE", "300ms")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	line := got.String()
	for _, want := range []string{
		"sync_enabled=true", "sync_watcher=true", "blob_root=",
		"sync_connections=", "sync_parse_workers=", "sync_accounts=",
		"sync_debounce=300ms", "sync_reconcile_interval=(default)",
		"sync_breaker_threshold=", "sync_breaker_cooldown=(default)",
		"imap_server_name=(unset)",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the config dump is missing %q:\n%s", want, line)
		}
	}

	// The DSN password must still be redacted with the sync fields appended.
	if strings.Contains(line, "hunter2") {
		t.Errorf("the config dump leaked the database password:\n%s", line)
	}
}
