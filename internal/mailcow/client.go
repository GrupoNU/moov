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
	Active     flexInt `json:"active"`
	Quota      int64   `json:"quota"`
	Messages   int64   `json:"messages"`
	Attributes struct {
		IMAPAccess  flexInt `json:"imap_access"`
		SMTPAccess  flexInt `json:"smtp_access"`
		SieveAccess flexInt `json:"sieve_access"`
	} `json:"attributes"`
}

// IsActive reports whether the mailbox is enabled.
func (m Mailbox) IsActive() bool { return m.Active != 0 }

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
		return Mailbox{}, fmt.Errorf("%w: mailbox %q", ErrNotFound, mailbox)
	}
	return m, nil
}

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
		return body, nil
	case http.StatusUnauthorized:
		// Mailcow's body here names the rejected source IP, which is the
		// single most useful diagnostic for the S1 H5 allowlist failure.
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

// apiResult is Mailcow's mutation response envelope. Both msg and type vary in
// shape between endpoints, so both are decoded permissively.
type apiResult struct {
	Type string          `json:"type"`
	Msg  json.RawMessage `json:"msg"`
}

// checkAPIResult interprets a mutation response.
//
// Mailcow answers HTTP 200 for failures, so this — not the status code — is
// what decides whether a write happened. The response is sometimes an object
// and sometimes an array of them; both are handled.
func checkAPIResult(body []byte, wantMsg string) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return fmt.Errorf("%w: empty response body", ErrUnexpectedResponse)
	}

	var results []apiResult
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &results); err != nil {
			return fmt.Errorf("%w: decoding result: %w (%s)", ErrUnexpectedResponse, err, snippet(body))
		}
	} else {
		var one apiResult
		if err := json.Unmarshal(trimmed, &one); err != nil {
			return fmt.Errorf("%w: decoding result: %w (%s)", ErrUnexpectedResponse, err, snippet(body))
		}
		results = []apiResult{one}
	}
	if len(results) == 0 {
		return fmt.Errorf("%w: response carried no result (%s)", ErrUnexpectedResponse, snippet(body))
	}

	// Every element must report success: a partial failure is a failure.
	var sawExpected bool
	for _, r := range results {
		msg := string(bytes.Trim(r.Msg, `"`))
		if r.Type != "success" {
			return fmt.Errorf("%w: type=%q msg=%s", ErrAPI, r.Type, snippet(r.Msg))
		}
		if wantMsg != "" && strings.Contains(msg, wantMsg) {
			sawExpected = true
		}
	}

	// The expected msg is checked but its absence is NOT fatal: Mailcow's msg
	// strings are localization keys that have been renamed across releases,
	// and refusing a success that used a new key would break Moov on a
	// Mailcow upgrade that changed nothing important. type=success is the
	// contract; the msg is corroboration.
	_ = sawExpected
	return nil
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
