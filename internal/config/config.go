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
}

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

	return c, c.Validate()
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
		"log_level=%s log_format=%s http_addr=%s database_url=%s shutdown_timeout=%s",
		c.LogLevel, c.LogFormat, c.HTTPAddr, redactDSN(c.DatabaseURL), c.ShutdownTimeout,
	)
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
