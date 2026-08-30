package sieve

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

// Config describes how to reach one account's ManageSieve endpoint.
//
// It mirrors imap.Config field for field where the fields mean the same
// thing, because the deployment facts are identical: same Dovecot container,
// same certificate-name mismatch (S1 H2), same app password (ADR §4 — the
// credential is provisioned with scope imap+smtp+sieve, so the value that
// opens IMAP opens this too). The duplication over importing imap.Config is
// deliberate: the two packages are independent protocol confinements, and a
// shared config type would be a dependency between them for the sake of six
// fields.
type Config struct {
	// Host is the ManageSieve server hostname — inside the Moov deployment,
	// the Docker network alias "dovecot".
	Host string

	// Port is the ManageSieve port. Defaults to 4190 (RFC 5804 §17).
	Port int

	// Username is the full mailbox address.
	Username string

	// Password is the app password provisioned through the Mailcow API. The
	// user's own password never reaches this struct.
	Password string

	// TLSServerName is the name the server certificate is verified against,
	// for the deployment where the dialed host ("dovecot") differs from the
	// certificate's name (the public mail hostname). Empty means "verify
	// against Host".
	TLSServerName string

	// TLSRootCAsPEM optionally replaces the system certificate pool, for a
	// Dovecot behind a private CA. Empty means the system pool.
	TLSRootCAsPEM []byte

	// InsecureSkipVerify disables certificate verification entirely.
	//
	// DEVELOPMENT ONLY — the same contract, wording and reasoning as
	// imap.Config.InsecureSkipVerify: it exists for a throwaway server with an
	// unmatchable self-signed certificate, it must be set at an explicit call
	// site, and Connect logs a warning every time it is honored.
	InsecureSkipVerify bool

	// DialTimeout bounds establishing the TCP connection. Default 15s.
	DialTimeout time.Duration

	// CommandTimeout bounds a single command round trip. Default 60s. There
	// is no long-lived command in ManageSieve (nothing like IDLE), so every
	// operation gets this deadline.
	CommandTimeout time.Duration
}

// Defaults applied by Normalize.
const (
	DefaultPort           = 4190
	DefaultDialTimeout    = 15 * time.Second
	DefaultCommandTimeout = 60 * time.Second
)

// ErrInvalidConfig is returned by Normalize for a Config that cannot be used.
var ErrInvalidConfig = errors.New("sieve: invalid config")

// Normalize validates the config and fills in defaults, returning the result.
// The receiver is not modified.
func (c Config) Normalize() (Config, error) {
	if c.Host == "" {
		return c, fmt.Errorf("%w: Host is required", ErrInvalidConfig)
	}
	if c.Username == "" {
		return c, fmt.Errorf("%w: Username is required", ErrInvalidConfig)
	}
	if c.Password == "" {
		return c, fmt.Errorf("%w: Password is required", ErrInvalidConfig)
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.Port < 1 || c.Port > 65535 {
		return c, fmt.Errorf("%w: Port %d out of range", ErrInvalidConfig, c.Port)
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = DefaultDialTimeout
	}
	if c.CommandTimeout <= 0 {
		c.CommandTimeout = DefaultCommandTimeout
	}
	return c, nil
}

// Address is the host:port to dial.
func (c Config) Address() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// serverName is the name the certificate is verified against.
func (c Config) serverName() string {
	if c.TLSServerName != "" {
		return c.TLSServerName
	}
	return c.Host
}

// tlsConfig builds the STARTTLS configuration. Verification is on unless
// InsecureSkipVerify was explicitly set; there is no path through this
// function that silently disables it.
func (c Config) tlsConfig() (*tls.Config, error) {
	cfg := &tls.Config{
		ServerName: c.serverName(),
		MinVersion: tls.VersionTLS12,
		// #nosec G402 -- honoring the documented development-only escape
		// hatch of Config.InsecureSkipVerify, exactly as internal/imap does.
		InsecureSkipVerify: c.InsecureSkipVerify,
	}
	if len(c.TLSRootCAsPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(c.TLSRootCAsPEM) {
			return nil, fmt.Errorf("%w: TLSRootCAsPEM contains no usable certificate", ErrInvalidConfig)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}
