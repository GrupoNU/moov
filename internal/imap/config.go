package imap

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

// Config describes how to reach one Dovecot account.
//
// Zero values are filled in by Normalize with the defaults documented on each
// field, so a caller only has to set Host, Username and Password.
type Config struct {
	// Host is the IMAP server hostname. Inside the Moov deployment this is the
	// Docker network alias of the Mailcow Dovecot container ("dovecot").
	Host string

	// Port is the IMAP port. Defaults to 143 (STARTTLS). Moov speaks STARTTLS
	// on the cleartext port rather than implicit TLS on 993 because the
	// connection never leaves the Docker network; see doc.go.
	Port int

	// Username is the full mailbox address, e.g. "user@example.com".
	Username string

	// Password is the app password provisioned through the Mailcow API
	// (ADR §4). The user's own password never reaches this struct.
	Password string

	// TLSServerName is the name the server certificate is verified against.
	//
	// It exists because the two are legitimately different: Moov dials the
	// container alias "dovecot", while Mailcow's certificate is issued for the
	// public mail hostname (spike S1, finding H2). Verifying against Host
	// would fail for a perfectly valid certificate.
	//
	// Empty means "verify against Host", which is the standard behavior.
	TLSServerName string

	// TLSRootCAsPEM optionally replaces the system certificate pool, for
	// deployments whose Dovecot uses a private CA. Empty means the system pool.
	TLSRootCAsPEM []byte

	// InsecureSkipVerify disables certificate verification entirely.
	//
	// DEVELOPMENT ONLY. NEVER SET THIS IN PRODUCTION.
	//
	// It exists for one narrow case: a developer pointing Moov at a throwaway
	// Dovecot with a self-signed certificate whose name cannot be matched.
	// With it set, Moov cannot tell the real server from anything that
	// intercepts the connection, and it hands that impostor the account's app
	// password on the very next command. There is no scenario where this is
	// an acceptable production trade-off — the supported fixes are
	// TLSServerName for a name mismatch and TLSRootCAsPEM for a private CA,
	// both of which keep verification on.
	//
	// It is deliberately a per-Config field and not a global switch or an
	// environment variable: turning it on has to be a decision made at one
	// call site that a reviewer can see. Connect logs a warning every time it
	// is honored.
	InsecureSkipVerify bool

	// DialTimeout bounds establishing the TCP connection. Default 15s.
	DialTimeout time.Duration

	// CommandTimeout bounds a single non-streaming command. Default 60s.
	// It does not bound IDLE, which is long-lived by construction.
	CommandTimeout time.Duration

	// IdleInterval is how long one IDLE round lasts before the watcher
	// restarts it. Default 25 minutes: RFC 2177 requires a client to
	// re-issue IDLE at least every 29 minutes, and Dovecot drops idle
	// connections at 30 by default, so 25 leaves room for a slow round trip
	// (S2 H9 — a NOTIFY watcher only receives data while inside IDLE).
	IdleInterval time.Duration

	// ClientName is sent in the IMAP ID command, so the mailbox owner and the
	// server admin can tell Moov's connections apart in Dovecot's logs.
	// Default "moov".
	ClientName string
}

// Default values applied by Normalize.
const (
	DefaultPort           = 143
	DefaultDialTimeout    = 15 * time.Second
	DefaultCommandTimeout = 60 * time.Second
	DefaultIdleInterval   = 25 * time.Minute
	DefaultClientName     = "moov"
)

// ErrInvalidConfig is returned by Normalize for a Config that cannot be used.
var ErrInvalidConfig = errors.New("imap: invalid config")

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
	if c.IdleInterval <= 0 {
		c.IdleInterval = DefaultIdleInterval
	}
	if c.IdleInterval > 29*time.Minute {
		return c, fmt.Errorf("%w: IdleInterval %s exceeds the RFC 2177 limit of 29m",
			ErrInvalidConfig, c.IdleInterval)
	}
	if c.ClientName == "" {
		c.ClientName = DefaultClientName
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

// tlsConfig builds the STARTTLS configuration.
//
// Verification is on unless InsecureSkipVerify was explicitly set; there is no
// path through this function that silently disables it.
func (c Config) tlsConfig() (*tls.Config, error) {
	cfg := &tls.Config{
		ServerName: c.serverName(),
		MinVersion: tls.VersionTLS12,
		// #nosec G402 -- honoring the documented development-only escape
		// hatch of Config.InsecureSkipVerify. It defaults to false, it cannot
		// be turned on except at an explicit call site, and Connect logs a
		// warning whenever it is in effect.
		InsecureSkipVerify: c.InsecureSkipVerify,
	}
	if len(c.TLSRootCAsPEM) > 0 {
		pool, err := rootPoolFromPEM(c.TLSRootCAsPEM)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}
