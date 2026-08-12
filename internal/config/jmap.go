package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// JMAPConfig is the JMAP HTTP server's configuration (epic J1, L2-jmap-server
// §2.1: same daemon, its own listener and settings).
type JMAPConfig struct {
	// Enabled turns the JMAP server on (MOOV_JMAP_ENABLED).
	//
	// Opt-in for the same reason the sync supervisor is: the daemon must stay
	// startable — CI, first boot — without a reachable Dovecot, and the JMAP
	// server cannot authenticate anyone without one.
	Enabled bool

	// Addr is the JMAP listener address (MOOV_JMAP_ADDR). The default port
	// 8620 is a mnemonic for RFC 8620.
	Addr string

	// ExternalURL is the base URL clients reach the server at
	// (MOOV_JMAP_EXTERNAL_URL, e.g. "https://mail.example.com"), used for the
	// absolute URLs in the session object. Empty means "derive from each
	// request", which is correct behind the S1 same-origin proxy.
	ExternalURL string

	// CORSOrigins is the allowed-origin list (MOOV_JMAP_CORS_ORIGINS,
	// comma-separated; the single value "*" allows any origin without
	// credential support — see internal/jmaphttp/cors.go). Empty disables
	// CORS: same-origin clients only.
	CORSOrigins []string

	// IMAPHost is the Dovecot host auth LOGIN validation dials
	// (MOOV_JMAP_IMAP_HOST). Required when Enabled: inside the deployment it
	// is the Mailcow container alias "dovecot".
	IMAPHost string

	// IMAPPort is the Dovecot IMAP port (MOOV_JMAP_IMAP_PORT, default 143 —
	// STARTTLS on the internal network, as everywhere else in Moov).
	IMAPPort int

	// IMAPServerName is the certificate name for the auth connection
	// (MOOV_JMAP_IMAP_SERVER_NAME, falling back to MOOV_IMAP_SERVER_NAME —
	// the same S1 H2 alias-vs-certificate split the sync engine handles).
	IMAPServerName string

	// AuthCacheTTL is how long a positive Basic-auth validation is cached
	// (MOOV_JMAP_AUTH_CACHE_TTL). Zero means the jmaphttp default of 10
	// minutes (arbitration J-A1).
	AuthCacheTTL time.Duration
}

// DefaultJMAPAddr is the default JMAP listen address.
const DefaultJMAPAddr = ":8620"

// loadJMAP reads the JMAP server's settings.
func loadJMAP() (JMAPConfig, error) {
	var j JMAPConfig
	var err error

	if j.Enabled, err = ParseBool("MOOV_JMAP_ENABLED", false); err != nil {
		return JMAPConfig{}, err
	}
	j.Addr = envOr("MOOV_JMAP_ADDR", DefaultJMAPAddr)
	j.ExternalURL = strings.TrimRight(os.Getenv("MOOV_JMAP_EXTERNAL_URL"), "/")
	j.IMAPHost = os.Getenv("MOOV_JMAP_IMAP_HOST")

	if v := os.Getenv("MOOV_JMAP_CORS_ORIGINS"); v != "" {
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				j.CORSOrigins = append(j.CORSOrigins, o)
			}
		}
	}

	j.IMAPPort = 143
	if v := os.Getenv("MOOV_JMAP_IMAP_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return JMAPConfig{}, fmt.Errorf("MOOV_JMAP_IMAP_PORT: %w", err)
		}
		if n < 1 || n > 65535 {
			return JMAPConfig{}, fmt.Errorf("MOOV_JMAP_IMAP_PORT: %d out of range", n)
		}
		j.IMAPPort = n
	}

	j.IMAPServerName = envOr("MOOV_JMAP_IMAP_SERVER_NAME", os.Getenv("MOOV_IMAP_SERVER_NAME"))

	if j.AuthCacheTTL, err = envDuration("MOOV_JMAP_AUTH_CACHE_TTL"); err != nil {
		return JMAPConfig{}, err
	}

	return j, nil
}

// validate checks the invariants of an enabled JMAP server.
func (j JMAPConfig) validate() error {
	if !j.Enabled {
		return nil
	}
	if j.IMAPHost == "" {
		return fmt.Errorf("%w: MOOV_JMAP_IMAP_HOST (required when MOOV_JMAP_ENABLED=1)", ErrMissingRequired)
	}
	return nil
}

// String renders the JMAP settings for logging. Nothing here is a secret.
func (j JMAPConfig) String() string {
	return fmt.Sprintf(
		"jmap_enabled=%t jmap_addr=%s jmap_external_url=%s jmap_cors_origins=%s "+
			"jmap_imap_host=%s jmap_imap_port=%d jmap_imap_server_name=%s jmap_auth_cache_ttl=%s",
		j.Enabled, j.Addr, orUnset(j.ExternalURL), orUnset(strings.Join(j.CORSOrigins, ",")),
		orUnset(j.IMAPHost), j.IMAPPort, orUnset(j.IMAPServerName), orDefault(j.AuthCacheTTL),
	)
}
