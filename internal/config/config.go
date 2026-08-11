// Package config loads moovd's runtime configuration from the environment.
//
// Environment only, deliberately: moovd runs as a container in a Docker stack
// joined to the Mailcow network (ADR-001), where env vars are the native
// configuration channel and a config file would be one more mounted volume to
// keep in sync. Every variable is prefixed MOOV_.
//
// Secrets are never defaulted and never logged. Load rejects a configuration
// that is missing a required secret rather than starting in a degraded state;
// String() redacts every secret-bearing field so a config dump is safe to log.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the complete runtime configuration of moovd.
//
// Fields are added by the epic that needs them (E2 adds IMAP, E3 the store
// tuning, E7 the Mailcow API credentials); E1 defines only what the daemon
// skeleton itself uses, so that nothing here is speculative.
type Config struct {
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
	// LogFormat is json (production, structured) or text (developer console).
	LogFormat string

	// HTTPAddr is the listen address of the operational HTTP server
	// (/healthz, /metrics — E8). Empty disables it.
	HTTPAddr string

	// DatabaseURL is the PostgreSQL connection string of the Moov store.
	// Required. It is a secret: it normally carries a password.
	DatabaseURL string

	// ShutdownTimeout bounds graceful shutdown before the process exits
	// anyway. A sync worker that will not stop must not hold the deploy.
	ShutdownTimeout time.Duration

	// Sync is the sync engine's configuration (E5/E6).
	Sync SyncConfig
}

// SyncConfig is everything the sync supervisor, its initial-sync pipeline and
// its push watcher read from the environment.
//
// It lives here rather than in cmd/moovd — where E5 first put it, deliberately
// and temporarily — now that E6 has settled the full set. Two reasons made the
// move worth doing at this point and not earlier: the settings are no longer
// one epic's shape, and a daemon whose configuration is split between a config
// package and ad-hoc os.Getenv calls has no single place to answer "what is
// this process actually running with", which is the question an operator asks
// first when an account is not syncing.
type SyncConfig struct {
	// Enabled turns the supervisor on (MOOV_SYNC_ENABLED).
	//
	// Opt-in, because the daemon must stay startable without a reachable
	// Dovecot: that is what CI and a first boot before provisioning depend on.
	Enabled bool

	// BlobRoot is the directory the content-addressed blobs live in
	// (MOOV_BLOB_ROOT).
	BlobRoot string

	// Connections is the per-account IMAP connection budget
	// (MOOV_SYNC_CONNECTIONS). Zero means the engine's default.
	Connections int

	// ParseWorkers overrides the CPU-bound parse pool (MOOV_SYNC_PARSE_WORKERS).
	// Zero means GOMAXPROCS, which is what S3 H6 measured the bulk path against.
	ParseWorkers int

	// Accounts is how many accounts are initially synced at once
	// (MOOV_SYNC_ACCOUNTS). Zero means the engine's default.
	Accounts int

	// IMAPServerName is the name Dovecot's certificate is verified against
	// (MOOV_IMAP_SERVER_NAME), which legitimately differs from the host dialed:
	// Moov dials the container alias while the certificate carries the public
	// mail hostname (S1 H2). Empty means "verify against the account's host".
	IMAPServerName string

	// WatcherEnabled turns E6's push watcher on (MOOV_SYNC_WATCHER).
	//
	// Defaults to TRUE when the supervisor is enabled: push is the product
	// (regla 1 — "push real"), so an engine that syncs once and then goes quiet
	// is the degraded mode, not the normal one. The variable exists to turn it
	// OFF for a migration run, where a watcher per account would compete with
	// the bulk load for the same connection budget.
	WatcherEnabled bool

	// Debounce is how long a burst of NOTIFY events for one mailbox is
	// coalesced before a pass runs (MOOV_SYNC_DEBOUNCE). Zero means the
	// engine's default.
	Debounce time.Duration

	// ReconcileInterval is the defensive STATUS sweep period
	// (MOOV_SYNC_RECONCILE_INTERVAL). Zero means the engine's default of 6 h
	// (L2 §2.5).
	ReconcileInterval time.Duration

	// BreakerThreshold is how many consecutive watcher failures open an
	// account's circuit breaker (MOOV_SYNC_BREAKER_THRESHOLD). Zero means the
	// engine's default.
	BreakerThreshold int

	// BreakerCooldown is how long an open breaker stays open
	// (MOOV_SYNC_BREAKER_COOLDOWN). Zero means the engine's default.
	//
	// It is a fail2ban control as much as a retry control (ADR §4): the breaker
	// is what stops an account with revoked credentials from producing a failed
	// login every few seconds until Mailcow bans the engine's IP.
	BreakerCooldown time.Duration
}

// DefaultBlobRoot is where blobs go when nothing else is configured.
const DefaultBlobRoot = "/var/lib/moov/blobs"

// Default values for every non-secret setting.
const (
	DefaultLogLevel        = "info"
	DefaultLogFormat       = "json"
	DefaultHTTPAddr        = ":8080"
	DefaultShutdownTimeout = 30 * time.Second
)

// ErrMissingRequired is returned when a required variable is unset or empty.
var ErrMissingRequired = errors.New("required configuration is missing")

// Load reads the configuration from the process environment and validates it.
//
// It returns a non-nil error for a missing required value or an unparseable
// one; callers must treat that as fatal rather than substituting a default,
// because "started with the wrong database" is worse than "did not start".
func Load() (Config, error) {
	c := Config{
		LogLevel:        envOr("MOOV_LOG_LEVEL", DefaultLogLevel),
		LogFormat:       envOr("MOOV_LOG_FORMAT", DefaultLogFormat),
		HTTPAddr:        envOr("MOOV_HTTP_ADDR", DefaultHTTPAddr),
		DatabaseURL:     os.Getenv("MOOV_DATABASE_URL"),
		ShutdownTimeout: DefaultShutdownTimeout,
	}

	if v := os.Getenv("MOOV_SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("MOOV_SHUTDOWN_TIMEOUT: %w", err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("MOOV_SHUTDOWN_TIMEOUT: must be positive, got %s", d)
		}
		c.ShutdownTimeout = d
	}

	sync, err := loadSync()
	if err != nil {
		return Config{}, err
	}
	c.Sync = sync

	return c, c.Validate()
}

// loadSync reads the sync engine's settings.
func loadSync() (SyncConfig, error) {
	var s SyncConfig
	var err error

	if s.Enabled, err = ParseBool("MOOV_SYNC_ENABLED", false); err != nil {
		return SyncConfig{}, err
	}
	// Push is the default behavior of an enabled engine, not an extra.
	if s.WatcherEnabled, err = ParseBool("MOOV_SYNC_WATCHER", true); err != nil {
		return SyncConfig{}, err
	}

	s.BlobRoot = envOr("MOOV_BLOB_ROOT", DefaultBlobRoot)
	s.IMAPServerName = os.Getenv("MOOV_IMAP_SERVER_NAME")

	for _, f := range []struct {
		key string
		dst *int
	}{
		{"MOOV_SYNC_CONNECTIONS", &s.Connections},
		{"MOOV_SYNC_PARSE_WORKERS", &s.ParseWorkers},
		{"MOOV_SYNC_ACCOUNTS", &s.Accounts},
		{"MOOV_SYNC_BREAKER_THRESHOLD", &s.BreakerThreshold},
	} {
		v, err := envPositiveInt(f.key)
		if err != nil {
			return SyncConfig{}, err
		}
		*f.dst = v
	}

	for _, f := range []struct {
		key string
		dst *time.Duration
	}{
		{"MOOV_SYNC_DEBOUNCE", &s.Debounce},
		{"MOOV_SYNC_RECONCILE_INTERVAL", &s.ReconcileInterval},
		{"MOOV_SYNC_BREAKER_COOLDOWN", &s.BreakerCooldown},
	} {
		v, err := envDuration(f.key)
		if err != nil {
			return SyncConfig{}, err
		}
		*f.dst = v
	}

	return s, nil
}

// envPositiveInt reads an optional non-negative integer. Zero means "not set",
// which every consumer reads as "use the engine's default".
func envPositiveInt(key string) (int, error) {
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

// envDuration reads an optional positive duration. Zero means "not set".
func envDuration(key string) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: must be positive, got %s", key, d)
	}
	return d, nil
}

// Validate checks the invariants Load relies on. It is exported so tests and
// future config sources (a flag set, a fixture) can reuse the same rules.
func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("%w: MOOV_DATABASE_URL", ErrMissingRequired)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("MOOV_LOG_LEVEL: want one of debug|info|warn|error, got %q", c.LogLevel)
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		return fmt.Errorf("MOOV_LOG_FORMAT: want one of json|text, got %q", c.LogFormat)
	}
	return nil
}

// String renders the configuration for logging with every secret redacted.
// Config must never be formatted with %+v anywhere; use this.
func (c Config) String() string {
	return fmt.Sprintf(
		"log_level=%s log_format=%s http_addr=%s database_url=%s shutdown_timeout=%s %s",
		c.LogLevel, c.LogFormat, c.HTTPAddr, redactDSN(c.DatabaseURL), c.ShutdownTimeout,
		c.Sync.String(),
	)
}

// String renders the sync settings for logging. Nothing here is a secret — the
// master key is read by internal/crypto and never passes through this struct —
// but the same one-line, greppable shape is kept so a config dump reads as one
// record.
func (s SyncConfig) String() string {
	return fmt.Sprintf(
		"sync_enabled=%t sync_watcher=%t blob_root=%s sync_connections=%d "+
			"sync_parse_workers=%d sync_accounts=%d imap_server_name=%s "+
			"sync_debounce=%s sync_reconcile_interval=%s "+
			"sync_breaker_threshold=%d sync_breaker_cooldown=%s",
		s.Enabled, s.WatcherEnabled, s.BlobRoot, s.Connections,
		s.ParseWorkers, s.Accounts, orUnset(s.IMAPServerName),
		orDefault(s.Debounce), orDefault(s.ReconcileInterval),
		s.BreakerThreshold, orDefault(s.BreakerCooldown),
	)
}

// orUnset renders an empty string as an explicit marker, so a log line
// distinguishes "not configured" from "configured as empty".
func orUnset(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

// orDefault renders a zero duration as the marker meaning "the engine's
// default applies", which is what a zero value means throughout this struct.
func orDefault(d time.Duration) string {
	if d == 0 {
		return "(default)"
	}
	return d.String()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// redactDSN removes the password from a PostgreSQL connection string while
// keeping the parts that make a log line diagnostic (host, database, user).
// It handles both URL form (postgres://user:pw@host/db) and keyword form
// (host=… password=…), and falls back to full redaction if it cannot tell.
func redactDSN(dsn string) string {
	if dsn == "" {
		return "(unset)"
	}
	const mask = "***"

	if strings.Contains(dsn, "://") {
		scheme, rest, _ := strings.Cut(dsn, "://")
		userinfo, hostpart, hasAt := strings.Cut(rest, "@")
		if !hasAt {
			return dsn // no credentials embedded
		}
		user, _, hasPw := strings.Cut(userinfo, ":")
		if !hasPw {
			return dsn
		}
		return scheme + "://" + user + ":" + mask + "@" + hostpart
	}

	if !strings.Contains(dsn, "password=") {
		return dsn
	}
	fields := strings.Fields(dsn)
	for i, f := range fields {
		if strings.HasPrefix(f, "password=") {
			fields[i] = "password=" + mask
		}
	}
	return strings.Join(fields, " ")
}

// ParseBool is a small helper for the boolean env vars later epics will add,
// kept here so every package parses them the same way.
func ParseBool(key string, fallback bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}
