package mailcow

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Environment variables this package reads.
const (
	// EnvBaseURL is the API root, with or without the /api/v1 suffix:
	// "https://nginx" and "https://nginx/api/v1" both work.
	EnvBaseURL = "MOOV_MAILCOW_BASE_URL"

	// EnvAPIKey holds a read-write Mailcow API key. Read-only keys cannot
	// create app passwords.
	//
	// #nosec G101 -- this is the NAME of an environment variable, not a key.
	EnvAPIKey = "MOOV_MAILCOW_API_KEY"

	// EnvAPIKeyFile points at a file containing the key, for deployments that
	// pass secrets as mounted files rather than environment variables.
	//
	// #nosec G101 -- likewise a variable name, not a credential.
	EnvAPIKeyFile = "MOOV_MAILCOW_API_KEY_FILE"

	// EnvHostHeader overrides the Host header. It is needed when the API is
	// reached at a container name or IP while Mailcow's nginx routes by the
	// public hostname — the normal case inside the Docker network.
	EnvHostHeader = "MOOV_MAILCOW_HOST_HEADER"

	// EnvInsecureSkipVerify disables TLS verification. Development only; see
	// Config.InsecureSkipVerify.
	EnvInsecureSkipVerify = "MOOV_MAILCOW_INSECURE_SKIP_VERIFY"
)

// Default values applied by Normalize.
const (
	// DefaultTimeout bounds a single API call end to end. Mailcow's API is a
	// PHP endpoint doing one or two small SQL statements; anything near this
	// is a stuck request, not a slow one.
	DefaultTimeout = 30 * time.Second

	// DefaultAppNamePrefix is the app_name every Moov-minted app password
	// starts with, so an administrator looking at the Mailcow UI can tell at a
	// glance which credentials belong to the webmail (ADR §4: "webmail-*").
	DefaultAppNamePrefix = "moov-webmail"

	// apiPath is the versioned API root appended to a base URL that does not
	// already carry it.
	apiPath = "/api/v1"
)

// ErrInvalidConfig is returned by Normalize for a config that cannot be used.
var ErrInvalidConfig = errors.New("mailcow: invalid config")

// Config describes how to reach one Mailcow instance's admin API.
type Config struct {
	// BaseURL is the API root. Required. Inside the Moov deployment this is
	// the Mailcow nginx container on the shared Docker network.
	BaseURL string

	// APIKey is a read-write Mailcow API key. Required, and a secret: it is
	// never logged, never included in an error, and never written to a file in
	// this repository.
	APIKey string

	// HostHeader overrides the Host header sent with each request. Empty means
	// the host from BaseURL.
	//
	// Mailcow's nginx serves the API on a virtual host matching
	// MAILCOW_HOSTNAME. Reaching it by container IP without this set gets the
	// default server, which is a redirect rather than the API.
	HostHeader string

	// AppNamePrefix is the prefix of the app_name given to minted app
	// passwords. Empty means DefaultAppNamePrefix.
	AppNamePrefix string

	// Timeout bounds a single API call. Zero means DefaultTimeout.
	Timeout time.Duration

	// ForceIPv4 makes the client dial IPv4 only (spike S1, H5). It defaults to
	// TRUE via Normalize; set ForceIPv6Allowed to opt out.
	ForceIPv4 bool

	// ForceIPv6Allowed opts out of ForceIPv4 for an IPv6-only deployment.
	//
	// It is a separate negative flag rather than making ForceIPv4 default to
	// false, because the safe value has to be the one a zero-valued struct
	// produces: an operator who forgets this field should get the behavior
	// that works, not the one that fails intermittently on a dual-stack host.
	ForceIPv6Allowed bool

	// InsecureSkipVerify disables TLS certificate verification.
	//
	// DEVELOPMENT ONLY. With it set, Moov cannot distinguish the real Mailcow
	// from anything that intercepts the connection, and it sends that impostor
	// an API key that can create credentials for every mailbox on the server.
	//
	// It exists for one real case: reaching nginx by container IP inside the
	// Docker network, where the certificate is issued for the public hostname
	// and no name matches. The supported fix for THAT case is HostHeader plus
	// a BaseURL using the routable name; this flag is the fallback when a
	// deployment cannot do either.
	InsecureSkipVerify bool
}

// LoadConfig builds a Config from the environment.
func LoadConfig() (Config, error) {
	c := Config{
		BaseURL:    os.Getenv(EnvBaseURL),
		APIKey:     os.Getenv(EnvAPIKey),
		HostHeader: os.Getenv(EnvHostHeader),
	}

	if path := os.Getenv(EnvAPIKeyFile); path != "" {
		if c.APIKey != "" {
			return Config{}, fmt.Errorf("mailcow: both %s and %s are set; set exactly one",
				EnvAPIKey, EnvAPIKeyFile)
		}
		raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied configuration path.
		if err != nil {
			return Config{}, fmt.Errorf("%s: reading %s: %w", EnvAPIKeyFile, path, err)
		}
		c.APIKey = strings.TrimSpace(string(raw))
	}

	if v := os.Getenv(EnvInsecureSkipVerify); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes":
			c.InsecureSkipVerify = true
		case "0", "false", "no", "":
		default:
			return Config{}, fmt.Errorf("%s: want a boolean, got %q", EnvInsecureSkipVerify, v)
		}
	}

	return c.Normalize()
}

// Normalize validates the config and fills in defaults, returning the result.
// The receiver is not modified.
func (c Config) Normalize() (Config, error) {
	c.BaseURL = strings.TrimSpace(c.BaseURL)
	c.APIKey = strings.TrimSpace(c.APIKey)

	if c.BaseURL == "" {
		return c, fmt.Errorf("%w: BaseURL is required (%s)", ErrInvalidConfig, EnvBaseURL)
	}
	if c.APIKey == "" {
		return c, fmt.Errorf("%w: APIKey is required (%s or %s)",
			ErrInvalidConfig, EnvAPIKey, EnvAPIKeyFile)
	}

	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return c, fmt.Errorf("%w: BaseURL: %w", ErrInvalidConfig, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return c, fmt.Errorf("%w: BaseURL scheme must be http or https, got %q",
			ErrInvalidConfig, u.Scheme)
	}
	if u.Host == "" {
		return c, fmt.Errorf("%w: BaseURL has no host", ErrInvalidConfig)
	}

	// Accept the base URL with or without the version suffix, and normalize to
	// carrying it, so callers never have to guess which form is expected.
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, apiPath) {
		u.Path += apiPath
	}
	u.RawQuery, u.Fragment = "", ""
	c.BaseURL = u.String()

	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.AppNamePrefix == "" {
		c.AppNamePrefix = DefaultAppNamePrefix
	}
	if !c.ForceIPv6Allowed {
		c.ForceIPv4 = true
	}
	return c, nil
}

// String renders the config for logging with the API key redacted. A Config
// must never be formatted with %+v anywhere; use this.
func (c Config) String() string {
	key := "(unset)"
	if c.APIKey != "" {
		key = "***"
	}
	return fmt.Sprintf("base_url=%s api_key=%s host_header=%s timeout=%s force_ipv4=%t insecure_skip_verify=%t",
		c.BaseURL, key, orNone(c.HostHeader), c.Timeout, c.ForceIPv4, c.InsecureSkipVerify)
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
