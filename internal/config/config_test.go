package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("MOOV_DATABASE_URL", "")

	if _, err := Load(); !errors.Is(err, ErrMissingRequired) {
		t.Fatalf("Load() error = %v, want ErrMissingRequired", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("MOOV_DATABASE_URL", "postgres://moov@localhost/moov")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got.LogLevel != DefaultLogLevel {
		t.Errorf("LogLevel = %q, want %q", got.LogLevel, DefaultLogLevel)
	}
	if got.LogFormat != DefaultLogFormat {
		t.Errorf("LogFormat = %q, want %q", got.LogFormat, DefaultLogFormat)
	}
	if got.HTTPAddr != DefaultHTTPAddr {
		t.Errorf("HTTPAddr = %q, want %q", got.HTTPAddr, DefaultHTTPAddr)
	}
	if got.ShutdownTimeout != DefaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %v, want %v", got.ShutdownTimeout, DefaultShutdownTimeout)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	tests := []struct {
		name string
		key  string
		val  string
	}{
		{"log level", "MOOV_LOG_LEVEL", "verbose"},
		{"log format", "MOOV_LOG_FORMAT", "xml"},
		{"shutdown timeout syntax", "MOOV_SHUTDOWN_TIMEOUT", "soon"},
		{"shutdown timeout sign", "MOOV_SHUTDOWN_TIMEOUT", "-5s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MOOV_DATABASE_URL", "postgres://moov@localhost/moov")
			t.Setenv(tt.key, tt.val)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() with %s=%q succeeded, want error", tt.key, tt.val)
			}
		})
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("MOOV_DATABASE_URL", "postgres://moov@localhost/moov")
	t.Setenv("MOOV_LOG_LEVEL", "debug")
	t.Setenv("MOOV_LOG_FORMAT", "text")
	t.Setenv("MOOV_HTTP_ADDR", "127.0.0.1:9999")
	t.Setenv("MOOV_SHUTDOWN_TIMEOUT", "5s")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got.LogLevel != "debug" || got.LogFormat != "text" || got.HTTPAddr != "127.0.0.1:9999" {
		t.Errorf("overrides not applied: %+v", got)
	}
	if got.ShutdownTimeout != 5*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 5s", got.ShutdownTimeout)
	}
}

// The password must never reach a log line. This test is the guard for that
// promise; it is deliberately exhaustive about DSN shapes because the redactor
// silently doing nothing is the failure mode that matters.
func TestStringRedactsPassword(t *testing.T) {
	const secret = "sup3rs3cr3t"

	tests := []struct {
		name string
		dsn  string
		keep string // a substring that must survive redaction, for diagnosability
	}{
		{"url form", "postgres://moov:" + secret + "@db:5432/moov?sslmode=disable", "db:5432"},
		{"keyword form", "host=db user=moov password=" + secret + " dbname=moov", "dbname=moov"},
		{"url without password", "postgres://moov@db:5432/moov", "moov@db"},
		{"no credentials", "host=db dbname=moov", "dbname=moov"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{
				LogLevel: "info", LogFormat: "json",
				HTTPAddr: ":8080", DatabaseURL: tt.dsn,
			}
			out := c.String()
			if strings.Contains(out, secret) {
				t.Fatalf("String() leaked the password: %s", out)
			}
			if !strings.Contains(out, tt.keep) {
				t.Errorf("String() dropped diagnostic detail %q: %s", tt.keep, out)
			}
		})
	}
}

func TestStringHandlesUnsetDSN(t *testing.T) {
	c := Config{LogLevel: "info", LogFormat: "json"}
	if !strings.Contains(c.String(), "(unset)") {
		t.Errorf("String() = %q, want it to mark the DSN as unset", c.String())
	}
}
