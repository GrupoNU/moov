package imap

import (
	"crypto/tls"
	"errors"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{Host: "dovecot", Username: "u@example.com", Password: "pw"}
}

func TestConfigNormalizeDefaults(t *testing.T) {
	got, err := validConfig().Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	if got.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", got.Port, DefaultPort)
	}
	if got.DialTimeout != DefaultDialTimeout {
		t.Errorf("DialTimeout = %s, want %s", got.DialTimeout, DefaultDialTimeout)
	}
	if got.IdleInterval != DefaultIdleInterval {
		t.Errorf("IdleInterval = %s, want %s", got.IdleInterval, DefaultIdleInterval)
	}
	if got.ClientName != DefaultClientName {
		t.Errorf("ClientName = %q, want %q", got.ClientName, DefaultClientName)
	}

	// The default must never be the insecure one. This is not a style
	// assertion: the field hands the account's app password to whatever
	// answers the socket, so a refactor that flipped its default would be a
	// credential leak, and this is the test that says so.
	if got.InsecureSkipVerify {
		t.Error("InsecureSkipVerify defaults to true; it must default to false")
	}
}

func TestConfigNormalizeRejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"no host", Config{Username: "u", Password: "p"}},
		{"no username", Config{Host: "h", Password: "p"}},
		{"no password", Config{Host: "h", Username: "u"}},
		{"port too high", Config{Host: "h", Username: "u", Password: "p", Port: 70000}},
		{"port negative", Config{Host: "h", Username: "u", Password: "p", Port: -1}},
		{
			// RFC 2177 requires IDLE to be re-issued within 29 minutes. A
			// longer interval means the server drops the watcher's connection
			// and Moov stops receiving push entirely — a silent failure, so it
			// is rejected at construction rather than discovered in production.
			"idle interval beyond the RFC 2177 limit",
			Config{Host: "h", Username: "u", Password: "p", IdleInterval: 35 * time.Minute},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.cfg.Normalize(); !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("Normalize() error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestConfigAddress(t *testing.T) {
	cfg, err := Config{Host: "dovecot", Username: "u", Password: "p"}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Address(), "dovecot:143"; got != want {
		t.Errorf("Address() = %q, want %q", got, want)
	}

	// An IPv6 literal must come out bracketed, or the dial fails.
	cfg.Host = "::1"
	if got, want := cfg.Address(), "[::1]:143"; got != want {
		t.Errorf("Address() = %q, want %q", got, want)
	}
}

// TestTLSServerNameIsIndependentOfHost covers spike S1 finding H2: Moov dials
// the Docker alias "dovecot" while Mailcow's certificate is issued for the
// public mail hostname. Verifying against the dialed name would reject a
// perfectly valid certificate, and the tempting "fix" is to disable
// verification. TLSServerName is the correct fix, so it has to work.
func TestTLSServerNameIsIndependentOfHost(t *testing.T) {
	cfg, err := Config{
		Host:          "dovecot",
		Username:      "u",
		Password:      "p",
		TLSServerName: "mail.example.com",
	}.Normalize()
	if err != nil {
		t.Fatal(err)
	}

	tlsCfg, err := cfg.tlsConfig()
	if err != nil {
		t.Fatalf("tlsConfig: %v", err)
	}
	if got, want := tlsCfg.ServerName, "mail.example.com"; got != want {
		t.Errorf("ServerName = %q, want %q", got, want)
	}
	if tlsCfg.InsecureSkipVerify {
		t.Error("setting TLSServerName must not disable verification")
	}
	if tlsCfg.MinVersion < tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want at least TLS 1.2", tlsCfg.MinVersion)
	}
}

func TestTLSDefaultsToVerifyingAgainstHost(t *testing.T) {
	cfg, err := validConfig().Normalize()
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := cfg.tlsConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := tlsCfg.ServerName, "dovecot"; got != want {
		t.Errorf("ServerName = %q, want %q", got, want)
	}
	if tlsCfg.InsecureSkipVerify {
		t.Error("verification must be on by default")
	}
}

// TestInsecureSkipVerifyIsOptIn documents that the escape hatch works when
// explicitly asked for — and only then. It is a per-Config field on purpose:
// there is no global switch and no environment variable, so turning it on is
// always visible at one reviewable call site.
func TestInsecureSkipVerifyIsOptIn(t *testing.T) {
	cfg, err := Config{
		Host: "h", Username: "u", Password: "p",
		InsecureSkipVerify: true,
	}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := cfg.tlsConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !tlsCfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify was requested but not honored")
	}
}

func TestTLSRootCAsPEMRejectsGarbage(t *testing.T) {
	cfg, err := Config{
		Host: "h", Username: "u", Password: "p",
		TLSRootCAsPEM: []byte("this is not a certificate"),
	}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	// Silently ignoring an unusable CA bundle would leave the connection
	// verifying against the system pool while the operator believes it is
	// pinned to their private CA. Failing loudly is the only safe answer.
	if _, err := cfg.tlsConfig(); err == nil {
		t.Error("tlsConfig accepted a PEM bundle with no certificate in it")
	}
}
