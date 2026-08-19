package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The JMAP settings (J1). Same conventions as the sync settings tests:
// t.Setenv scopes every variable to the test, and Load is exercised whole so
// interactions (validation, String) are covered.

func TestJMAPDefaults(t *testing.T) {
	t.Setenv("MOOV_DATABASE_URL", "postgres://u:p@h/db")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	j := cfg.JMAP
	if j.Enabled {
		t.Fatal("JMAP must be opt-in")
	}
	if j.Addr != DefaultJMAPAddr {
		t.Fatalf("Addr = %q", j.Addr)
	}
	if j.IMAPPort != 143 {
		t.Fatalf("IMAPPort = %d", j.IMAPPort)
	}
	if j.AuthCacheTTL != 0 {
		t.Fatalf("AuthCacheTTL = %s, want 0 (meaning the jmaphttp default)", j.AuthCacheTTL)
	}
}

func TestJMAPFullConfiguration(t *testing.T) {
	t.Setenv("MOOV_DATABASE_URL", "postgres://u:p@h/db")
	t.Setenv("MOOV_JMAP_ENABLED", "1")
	t.Setenv("MOOV_JMAP_ADDR", ":9999")
	t.Setenv("MOOV_JMAP_EXTERNAL_URL", "https://mail.example.com/")
	t.Setenv("MOOV_JMAP_CORS_ORIGINS", "https://a.example, https://b.example ,")
	t.Setenv("MOOV_JMAP_IMAP_HOST", "dovecot")
	t.Setenv("MOOV_JMAP_IMAP_PORT", "10143")
	t.Setenv("MOOV_JMAP_IMAP_SERVER_NAME", "mail.example.com")
	t.Setenv("MOOV_JMAP_AUTH_CACHE_TTL", "5m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	j := cfg.JMAP
	if !j.Enabled || j.Addr != ":9999" || j.IMAPHost != "dovecot" || j.IMAPPort != 10143 {
		t.Fatalf("parsed = %+v", j)
	}
	if j.ExternalURL != "https://mail.example.com" {
		t.Fatalf("ExternalURL = %q (trailing slash must be trimmed)", j.ExternalURL)
	}
	if len(j.CORSOrigins) != 2 || j.CORSOrigins[0] != "https://a.example" || j.CORSOrigins[1] != "https://b.example" {
		t.Fatalf("CORSOrigins = %v", j.CORSOrigins)
	}
	if j.IMAPServerName != "mail.example.com" {
		t.Fatalf("IMAPServerName = %q", j.IMAPServerName)
	}
	if j.AuthCacheTTL != 5*time.Minute {
		t.Fatalf("AuthCacheTTL = %s", j.AuthCacheTTL)
	}
}

func TestJMAPServerNameFallsBackToSyncVariable(t *testing.T) {
	t.Setenv("MOOV_DATABASE_URL", "postgres://u:p@h/db")
	t.Setenv("MOOV_IMAP_SERVER_NAME", "mail.example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.JMAP.IMAPServerName != "mail.example.com" {
		t.Fatalf("IMAPServerName = %q, want the MOOV_IMAP_SERVER_NAME fallback", cfg.JMAP.IMAPServerName)
	}
}

func TestJMAPEnabledRequiresIMAPHost(t *testing.T) {
	t.Setenv("MOOV_DATABASE_URL", "postgres://u:p@h/db")
	t.Setenv("MOOV_JMAP_ENABLED", "1")

	_, err := Load()
	if !errors.Is(err, ErrMissingRequired) {
		t.Fatalf("err = %v, want ErrMissingRequired for MOOV_JMAP_IMAP_HOST", err)
	}
}

func TestJMAPRejectsBadValues(t *testing.T) {
	for name, env := range map[string][2]string{
		"bad port":     {"MOOV_JMAP_IMAP_PORT", "eighty"},
		"port range":   {"MOOV_JMAP_IMAP_PORT", "70000"},
		"bad ttl":      {"MOOV_JMAP_AUTH_CACHE_TTL", "soon"},
		"negative ttl": {"MOOV_JMAP_AUTH_CACHE_TTL", "-1m"},
		"bad enabled":  {"MOOV_JMAP_ENABLED", "yes please"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("MOOV_DATABASE_URL", "postgres://u:p@h/db")
			t.Setenv(env[0], env[1])
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%q was accepted", env[0], env[1])
			}
		})
	}
}

func TestJMAPStringIsGreppableAndSecretFree(t *testing.T) {
	t.Setenv("MOOV_DATABASE_URL", "postgres://u:secret@h/db")
	t.Setenv("MOOV_JMAP_ENABLED", "1")
	t.Setenv("MOOV_JMAP_IMAP_HOST", "dovecot")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := cfg.String()
	for _, want := range []string{"jmap_enabled=true", "jmap_addr=", "jmap_imap_host=dovecot"} {
		if !strings.Contains(s, want) {
			t.Errorf("Config.String() lacks %q: %s", want, s)
		}
	}
	if strings.Contains(s, "secret") {
		t.Fatalf("Config.String() leaks the database password: %s", s)
	}
}

// The SSE push cap (W4a, W-A4).

func TestJMAPSSECapDefault(t *testing.T) {
	t.Setenv("MOOV_DATABASE_URL", "postgres://u:p@h/db")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.JMAP.MaxSSEPerAccount; got != DefaultMaxSSEPerAccount {
		t.Errorf("MaxSSEPerAccount = %d, want the W-A4 default %d", got, DefaultMaxSSEPerAccount)
	}
}

func TestJMAPSSECapFromEnv(t *testing.T) {
	t.Setenv("MOOV_DATABASE_URL", "postgres://u:p@h/db")
	t.Setenv("MOOV_SSE_MAX_CONN_PER_ACCOUNT", "9")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.JMAP.MaxSSEPerAccount; got != 9 {
		t.Errorf("MaxSSEPerAccount = %d, want 9", got)
	}
	if !strings.Contains(cfg.JMAP.String(), "sse_max_conn_per_account=9") {
		t.Errorf("String() does not report the cap: %s", cfg.JMAP.String())
	}
}

// A cap below 1 would silently disable push for every account, so it is a
// configuration error rather than a value to clamp.
func TestJMAPSSECapRejectsNonPositive(t *testing.T) {
	for _, v := range []string{"0", "-1", "many"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("MOOV_DATABASE_URL", "postgres://u:p@h/db")
			t.Setenv("MOOV_SSE_MAX_CONN_PER_ACCOUNT", v)

			if _, err := Load(); err == nil {
				t.Errorf("MOOV_SSE_MAX_CONN_PER_ACCOUNT=%q was accepted", v)
			}
		})
	}
}
