package mailcow

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Errors this package returns. They are sentinels so provisioning can branch on
// the condition without matching strings.
var (
	// ErrUnauthorized is returned for HTTP 401 — a wrong API key, or a key
	// whose allowlist does not contain the address Moov presented (the S1 H5
	// failure mode). Mailcow reports both the same way.
	ErrUnauthorized = errors.New("mailcow: API rejected the key or the source address")

	// ErrForbidden is returned for HTTP 403, which Mailcow uses for a
	// read-only key attempting a write.
	ErrForbidden = errors.New("mailcow: API key is read-only")

	// ErrNotFound is returned when the object asked for does not exist — a
	// mailbox that is not on this server, an app password already deleted.
	ErrNotFound = errors.New("mailcow: not found")

	// ErrAPI is returned when Mailcow answers HTTP 200 with a failure body.
	// Its message carries Mailcow's own msg field, which is the only
	// diagnostic the API gives.
	ErrAPI = errors.New("mailcow: API reported a failure")

	// ErrUnexpectedResponse is returned for a body this client cannot parse —
	// which in practice means the request did not reach the API at all
	// (a proxy error page, a redirect to the login form).
	ErrUnexpectedResponse = errors.New("mailcow: unexpected API response")
)

// Protocol is one access scope of an app password. The values are the names
// Mailcow's `protocols` array uses.
type Protocol string

// The protocol scopes. Moov provisions exactly imap+smtp+sieve (ADR §4): IMAP
// to sync, SMTP to send, Sieve for filters. Notably NOT dav_access, eas_access
// or pop3_access — SOGo keeps CalDAV/CardDAV/ActiveSync, and an app password
// that cannot do those things is a smaller blast radius if it leaks.
const (
	ProtocolIMAP  Protocol = "imap_access"
	ProtocolSMTP  Protocol = "smtp_access"
	ProtocolSieve Protocol = "sieve_access"
	ProtocolPOP3  Protocol = "pop3_access"
	ProtocolDAV   Protocol = "dav_access"
	ProtocolEAS   Protocol = "eas_access"
)

// MoovScopes is the scope set Moov provisions: imap+smtp+sieve, nothing else.
//
// It is a function rather than a package-level slice so a caller cannot mutate
// the shared value and silently widen every future provisioning call.
func MoovScopes() []Protocol {
	return []Protocol{ProtocolIMAP, ProtocolSMTP, ProtocolSieve}
}

// Client talks to one Mailcow instance's admin API.
//
// It is safe for concurrent use: the underlying http.Client is, and nothing
// here holds mutable state.
type Client struct {
	cfg  Config
	http *http.Client

	// validated is set by ValidateKey. Until then a GET answering `{}` is
	// refused rather than read as "not found" (F0 rule 6).
	validated atomic.Bool
}

// New builds a client from a config, normalizing it first.
func New(cfg Config) (*Client, error) {
	cfg, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, http: newHTTPClient(cfg)}, nil
}

// NewWithHTTPClient builds a client using a caller-supplied http.Client.
//
// It exists for tests against httptest, and for a deployment that must route
// through a specific transport. The config's own transport settings —
// ForceIPv4, InsecureSkipVerify, Timeout — are the supplied client's business
// in that case, not this package's.
func NewWithHTTPClient(cfg Config, hc *http.Client) (*Client, error) {
	cfg, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	if hc == nil {
		return nil, fmt.Errorf("%w: nil http.Client", ErrInvalidConfig)
	}
	return &Client{cfg: cfg, http: hc}, nil
}

// Config returns the normalized configuration, for logging via its String
// method. The API key it carries is redacted by that method and by nothing
// else, so callers must not format the result any other way.
func (c *Client) Config() Config { return c.cfg }

// newHTTPClient builds the transport described by cfg.
//
// The IPv4 pin is implemented in DialContext rather than by rewriting the URL,
// because the address to force is only known after DNS resolution — which is
// exactly where the S1 H5 failure happens: the name resolves to both families
// and Go's happy-eyeballs dialer may pick the IPv6 one.
func newHTTPClient(cfg Config) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if cfg.ForceIPv4 {
				// "tcp4" makes the resolver return only A records and the
				// dialer refuse an IPv6 literal — S1 H5.
				switch network {
				case "tcp", "tcp6":
					network = "tcp4"
				}
			}
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			// #nosec G402 -- honoring the documented development-only escape
			// hatch of Config.InsecureSkipVerify, which defaults to false and
			// can only be set at an explicit call site or by an environment
			// variable an operator had to write.
			InsecureSkipVerify: cfg.InsecureSkipVerify,
		},
		MaxIdleConnsPerHost: 2,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
		// Provisioning must never follow a redirect: Mailcow's nginx answers a
		// wrong virtual host with a redirect to the UI, and following it would
		// send the API key somewhere it was not addressed to.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("%w: server redirected to %s (check BaseURL and HostHeader)",
				ErrUnexpectedResponse, req.URL.Redacted())
		},
	}
}

// Mailbox is the subset of a Mailcow mailbox object Moov needs.
//
// Mailcow returns a large object with a nested attributes map of string-typed
// booleans; only the fields provisioning actually uses are decoded, so an
// upstream addition cannot break this client.
type Mailbox struct {
	Username string `json:"username"`
	Domain   string `json:"domain"`
	Name     string `json:"name"`
	// Active is 1 or 0. Mailcow sends it as a number here and as a string
	// elsewhere, which is why it is decoded through flexInt.
	Active flexInt `json:"active"`
	// Quota is in BYTES on read (it is written in MB — see CreateMailbox).
	Quota    int64 `json:"quota"`
	Messages int64 `json:"messages"`
	// QuotaUsed is the mailbox's real disk usage in bytes, as Dovecot
	// reported it to Mailcow (F0 §3.2). It is what the accounts API serves
	// as quota.usedBytes.
	QuotaUsed int64 `json:"quota_used"`
	// LastIMAPLogin and LastSMTPLogin are Unix seconds, 0 when never.
	LastIMAPLogin flexInt `json:"last_imap_login"`
	LastSMTPLogin flexInt `json:"last_smtp_login"`
	// RL is the effective per-mailbox rate limit and RLScope says whether it
	// is the mailbox's own ("mailbox") or inherited from the domain
	// ("domain"). Mailcow sends `rl` as an object when one applies and as
	// `false` otherwise, hence the custom decoder.
	RL      RateLimit `json:"rl"`
	RLScope string    `json:"rl_scope"`

	Attributes struct {
		IMAPAccess  flexInt `json:"imap_access"`
		SMTPAccess  flexInt `json:"smtp_access"`
		SieveAccess flexInt `json:"sieve_access"`
	} `json:"attributes"`
}

// IsActive reports whether the mailbox is enabled.
func (m Mailbox) IsActive() bool { return m.Active != 0 }

// RateLimit is a Mailcow sending rate limit: Value messages per Frame, where
// Frame is one of "s", "m", "h", "d" (F0 answer P1 confirmed "d").
//
// The zero value means "no limit of its own" — a mailbox inheriting the
// domain's limit reads as the inherited value with RLScope "domain".
type RateLimit struct {
	Value int
	Frame string
}

// IsZero reports whether no limit is set.
func (rl RateLimit) IsZero() bool { return rl.Value == 0 && rl.Frame == "" }

// UnmarshalJSON accepts the object form {"value":"300","frame":"d"} and the
// `false`/`null`/`{}` forms Mailcow uses for "none".
func (rl *RateLimit) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' {
		*rl = RateLimit{}
		return nil
	}
	var raw struct {
		Value flexInt `json:"value"`
		Frame string  `json:"frame"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*rl = RateLimit{Value: int(raw.Value), Frame: raw.Frame}
	return nil
}

// AllowsMoovScopes reports whether the MAILBOX itself permits the protocols
// Moov needs. An app password cannot grant access the mailbox denies, so
// provisioning checks this before minting a credential that would not work.
func (m Mailbox) AllowsMoovScopes() bool {
	return m.Attributes.IMAPAccess != 0 &&
		m.Attributes.SMTPAccess != 0 &&
		m.Attributes.SieveAccess != 0
}

// AppPassword is one row of Mailcow's app_passwd table.
//
// The Password field of a listed app password is the BCRYPT HASH, not the
// plaintext — Mailcow cannot return the plaintext, which is why provisioning
// generates it locally and keeps its own encrypted copy. It is deliberately
// not decoded here: Moov has no use for the hash and no reason to hold it.
type AppPassword struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Mailbox     string  `json:"mailbox"`
	Domain      string  `json:"domain"`
	Created     string  `json:"created"`
	Active      flexInt `json:"active"`
	IMAPAccess  flexInt `json:"imap_access"`
	SMTPAccess  flexInt `json:"smtp_access"`
	SieveAccess flexInt `json:"sieve_access"`
}

// GetMailbox reads a mailbox's details.
//
// It returns ErrNotFound when the mailbox does not exist on this server, which
// Mailcow signals with an empty JSON object rather than a 404.
func (c *Client) GetMailbox(ctx context.Context, mailbox string) (Mailbox, error) {
	if err := validateMailbox(mailbox); err != nil {
		return Mailbox{}, err
	}

	body, err := c.do(ctx, http.MethodGet, "/get/mailbox/"+url.PathEscape(mailbox), nil)
	if err != nil {
		return Mailbox{}, err
	}

	// An absent mailbox comes back as `{}` (or `[]`), which decodes into a
	// zero Mailbox rather than failing. The empty Username is what identifies
	// it, so the check is on the decoded value, not on the error.
	var m Mailbox
	if err := json.Unmarshal(body, &m); err != nil {
		return Mailbox{}, fmt.Errorf("%w: decoding mailbox: %w", ErrUnexpectedResponse, err)
	}
	if m.Username == "" {
		return Mailbox{}, c.emptyObject(fmt.Sprintf("mailbox %q", mailbox))
	}
	return m, nil
}

// emptyObject is the F0 rule for a GET that answered `{}`: it is "not found"
// only once the key has been validated, because the same body is what a
// silently rejected key produces (rule 6 of the note).
func (c *Client) emptyObject(what string) error {
	if !c.validated.Load() {
		return fmt.Errorf("%w: %s answered an empty object", ErrKeyNotValidated, what)
	}
	return fmt.Errorf("%w: %s", ErrNotFound, what)
}

// ValidateKey proves the configured key works from this address, so that a
// later `{}` can be trusted as "does not exist".
//
// It reads the server version: a call every key may make, whose answer is a
// non-empty object with a version string — never `{}` — so a valid key is
// positively identified rather than inferred from the absence of an error.
// The accounts API calls it at startup and refuses to enable itself when it
// fails; moovctl calls it before provisioning for the same reason.
func (c *Client) ValidateKey(ctx context.Context) error {
	body, err := c.do(ctx, http.MethodGet, statusVersionPath, nil)
	if err != nil {
		return fmt.Errorf("validating the API key: %w", err)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &v); err != nil || v.Version == "" {
		return fmt.Errorf("%w: validating the API key: %s did not answer a version (%s)",
			ErrUnexpectedResponse, statusVersionPath, snippet(body))
	}
	c.validated.Store(true)
	return nil
}

// Validated reports whether ValidateKey succeeded on this client.
func (c *Client) Validated() bool { return c.validated.Load() }

// ListAppPasswords returns the app passwords of one mailbox.
func (c *Client) ListAppPasswords(ctx context.Context, mailbox string) ([]AppPassword, error) {
	if err := validateMailbox(mailbox); err != nil {
		return nil, err
	}

	body, err := c.do(ctx, http.MethodGet, "/get/app-passwd/all/"+url.PathEscape(mailbox), nil)
	if err != nil {
		return nil, err
	}

	// A mailbox with no app passwords answers `{}`, not `[]`. Decoding
	// straight into a slice would fail on the object, so the object case is
	// recognized as "empty" first.
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return nil, nil
	}

	var out []AppPassword
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, fmt.Errorf("%w: decoding app password list: %w", ErrUnexpectedResponse, err)
	}
	return out, nil
}

// CreateAppPasswordRequest is the input to CreateAppPassword.
type CreateAppPasswordRequest struct {
	// Mailbox is the full address the app password belongs to. Required.
	Mailbox string

	// Password is the plaintext app password to register. Required.
	//
	// The caller generates it — provisioning does, with crypto/rand — so that
	// the plaintext exists in Moov's process before it exists anywhere else
	// and can be sealed immediately. Mailcow stores only a bcrypt hash and can
	// never give it back.
	Password string

	// Scopes are the protocols to grant. Empty means MoovScopes().
	//
	// It is never sent empty on the wire: Mailcow treats a missing protocols
	// array as "grant nothing" and creates a credential that authenticates
	// against no service at all (upstream issue #4588).
	Scopes []Protocol

	// NameSuffix disambiguates the app_name. Empty means a random suffix,
	// which is what makes the post-create lookup of the id unambiguous.
	NameSuffix string
}

// CreateAppPassword mints an app password for a mailbox and returns the created
// row, including the id needed to delete it later.
//
// # Not retried
//
// A failed write is NOT retried inside this method. If the request reached
// Mailcow and the response was lost, a retry mints a second live credential
// that nothing is tracking — an orphaned key to a user's mailbox. The caller
// gets the error and decides; provisioning treats it as fatal and reconciles by
// listing (see internal/provision).
//
// # Two round trips
//
// Mailcow's create response does not include the new row's id, so this makes a
// follow-up list call and matches on the generated app name. When the create
// succeeded but the lookup failed, the error names the app_name that was
// created, so an operator can find and remove it by hand — reporting "it
// failed" while leaving a live credential behind unnamed would be the worst of
// both.
func (c *Client) CreateAppPassword(ctx context.Context, req CreateAppPasswordRequest) (AppPassword, error) {
	if err := validateMailbox(req.Mailbox); err != nil {
		return AppPassword{}, err
	}
	if req.Password == "" {
		return AppPassword{}, fmt.Errorf("%w: Password is required", ErrInvalidConfig)
	}

	scopes := req.Scopes
	if len(scopes) == 0 {
		scopes = MoovScopes()
	}
	protocols := make([]string, len(scopes))
	for i, s := range scopes {
		protocols[i] = string(s)
	}

	suffix := req.NameSuffix
	if suffix == "" {
		var err error
		if suffix, err = randomSuffix(); err != nil {
			return AppPassword{}, err
		}
	}
	appName := c.cfg.AppNamePrefix + "-" + suffix

	// active, app_passwd2 and the protocols array are all mandatory in
	// practice: json_api.php reads them unconditionally, and a missing
	// protocols array creates a credential with no access (#4588).
	payload := map[string]any{
		"active":      "1",
		"username":    req.Mailbox,
		"app_name":    appName,
		"app_passwd":  req.Password,
		"app_passwd2": req.Password,
		"protocols":   protocols,
	}

	body, err := c.do(ctx, http.MethodPost, "/add/app-passwd", payload)
	if err != nil {
		return AppPassword{}, err
	}
	if err := checkAPIResult(body, "app_passwd_added"); err != nil {
		return AppPassword{}, err
	}

	// Second round trip for the id the create response withheld.
	list, err := c.ListAppPasswords(ctx, req.Mailbox)
	if err != nil {
		return AppPassword{}, fmt.Errorf(
			"mailcow: app password %q was created for %s but listing it failed "+
				"(remove it by hand if provisioning does not continue): %w",
			appName, req.Mailbox, err)
	}
	for _, ap := range list {
		if ap.Name == appName {
			return ap, nil
		}
	}
	return AppPassword{}, fmt.Errorf(
		"%w: app password %q reported as created for %s but is not in the list "+
			"(remove it by hand if it exists)", ErrUnexpectedResponse, appName, req.Mailbox)
}

// DeleteAppPassword removes an app password by id.
//
// The body is a bare JSON array, which is what json_api.php's delete path
// expects; an object body is silently ignored and the row survives. Like
// create, it is not retried.
func (c *Client) DeleteAppPassword(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("%w: app password id must be positive, got %d", ErrInvalidConfig, id)
	}

	// Ids go as STRINGS: that is the form the API's own examples use, and PHP
	// compares them loosely either way.
	payload := []string{fmt.Sprintf("%d", id)}

	body, err := c.do(ctx, http.MethodPost, "/delete/app-passwd", payload)
	if err != nil {
		return err
	}
	return checkAPIResult(body, "app_passwd_removed")
}

// do performs one API call and returns the raw response body.
func (c *Client) do(ctx context.Context, method, path string, payload any) ([]byte, error) {
	var reqBody io.Reader
	var encoded []byte
	if payload != nil {
		var err error
		if encoded, err = json.Marshal(payload); err != nil {
			return nil, fmt.Errorf("mailcow: encoding request: %w", err)
		}
		reqBody = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("mailcow: building request: %w", err)
	}

	req.Header.Set("X-API-Key", c.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "moov/provisioning")
	if encoded != nil {
		// json_api.php only reads the request body as JSON when the
		// Content-Type says so; without this it falls back to form parsing and
		// sees an empty request.
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cfg.HostHeader != "" {
		// Host must be set on the field, not only the header map: net/http
		// takes the Host from the URL otherwise.
		req.Host = c.cfg.HostHeader
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error stringifies with the full URL, which is not secret, but
		// never with headers — the API key cannot appear here.
		return nil, fmt.Errorf("mailcow: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// A bounded read: a misrouted request can return an arbitrarily large HTML
	// page, and this client must not buffer it.
	const maxBody = 4 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("mailcow: reading response of %s %s: %w", method, path, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		// F0: the "error" family arrives inside a 200 on reads. It is looked
		// for on EVERY 200, because an error object would otherwise decode
		// into an empty Mailbox and read as "not found".
		if err := errorEnvelope(body); err != nil {
			return nil, err
		}
		return body, nil
	case http.StatusUnauthorized:
		// Writes with a bad key get a real 401 (reads get a 200, above).
		// The body still says WHICH failure: a wrong key, or a valid key from
		// an address outside its allow-list — the S1 H5 case, whose message
		// names the IP Mailcow saw.
		if err := errorEnvelope(body); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, snippet(body))
	case http.StatusForbidden:
		return nil, fmt.Errorf("%w: %s", ErrForbidden, snippet(body))
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s %s", ErrNotFound, method, path)
	default:
		return nil, fmt.Errorf("%w: %s %s returned HTTP %d: %s",
			ErrUnexpectedResponse, method, path, resp.StatusCode, snippet(body))
	}
}

// flexInt decodes a value Mailcow sends sometimes as a number and sometimes as
// a quoted string ("1" vs 1), which it does inconsistently across endpoints and
// even across fields of one object.
type flexInt int64

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexInt) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*f = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s = strings.TrimSpace(s); s == "" {
			*f = 0
			return nil
		}
		var n int64
		if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
			return fmt.Errorf("mailcow: %q is not an integer", s)
		}
		*f = flexInt(n)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

// validateMailbox rejects a value that cannot be a mailbox address before it
// reaches the URL path.
//
// The check is deliberately structural rather than a full RFC 5322 validation:
// its job is to stop a caller from putting a path segment, a newline or an
// empty string into a request URL, not to decide what Mailcow accepts as an
// address — that is Mailcow's call.
func validateMailbox(mailbox string) error {
	if strings.TrimSpace(mailbox) == "" {
		return fmt.Errorf("%w: mailbox is required", ErrInvalidConfig)
	}
	if mailbox != strings.TrimSpace(mailbox) {
		return fmt.Errorf("%w: mailbox %q has surrounding whitespace", ErrInvalidConfig, mailbox)
	}
	if strings.ContainsAny(mailbox, "/?#\\\r\n\t ") {
		return fmt.Errorf("%w: mailbox %q contains a character that cannot appear in an address",
			ErrInvalidConfig, mailbox)
	}
	if at := strings.IndexByte(mailbox, '@'); at <= 0 || at == len(mailbox)-1 {
		return fmt.Errorf("%w: mailbox %q is not a full address", ErrInvalidConfig, mailbox)
	}
	return nil
}

// randomSuffix returns 8 hex characters from the system CSPRNG, used to make
// each minted app_name unique.
func randomSuffix() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mailcow: generating app name suffix: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// snippet bounds an untrusted response fragment before it reaches a log line or
// an error, and strips newlines so one HTML error page cannot forge log
// entries.
func snippet(b []byte) string {
	const limit = 200
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	if s == "" {
		return "(empty)"
	}
	return s
}
