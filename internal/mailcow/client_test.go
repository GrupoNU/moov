package mailcow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capturedRequest is what the fake server records for a single call, so a test
// can assert the exact wire shape rather than only the outcome.
type capturedRequest struct {
	Method string
	Path   string
	APIKey string
	Host   string
	Accept string
	CType  string
	Body   []byte
}

// fakeMailcow is an httptest server standing in for Mailcow's API. Handlers are
// keyed by "METHOD /path".
type fakeMailcow struct {
	t        *testing.T
	server   *httptest.Server
	requests []capturedRequest
	handlers map[string]func(w http.ResponseWriter, body []byte)
}

func newFakeMailcow(t *testing.T) *fakeMailcow {
	t.Helper()
	f := &fakeMailcow{t: t, handlers: map[string]func(http.ResponseWriter, []byte){}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.requests = append(f.requests, capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			APIKey: r.Header.Get("X-API-Key"),
			Host:   r.Host,
			Accept: r.Header.Get("Accept"),
			CType:  r.Header.Get("Content-Type"),
			Body:   body,
		})
		key := r.Method + " " + r.URL.Path
		if h, ok := f.handlers[key]; ok {
			w.Header().Set("Content-Type", "application/json")
			h(w, body)
			return
		}
		f.t.Errorf("fake mailcow: unexpected request %s", key)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeMailcow) on(key string, h func(w http.ResponseWriter, body []byte)) {
	f.handlers[key] = h
}

// onJSON registers a handler returning a fixed JSON body with HTTP 200.
func (f *fakeMailcow) onJSON(key, response string) {
	f.on(key, func(w http.ResponseWriter, _ []byte) {
		_, _ = io.WriteString(w, response)
	})
}

func (f *fakeMailcow) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{BaseURL: f.server.URL, APIKey: "TEST-KEY-000000"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func (f *fakeMailcow) last() capturedRequest {
	f.t.Helper()
	if len(f.requests) == 0 {
		f.t.Fatal("no request was made")
	}
	return f.requests[len(f.requests)-1]
}

// --- CreateAppPassword ------------------------------------------------------

func TestCreateAppPasswordRequestShape(t *testing.T) {
	f := newFakeMailcow(t)
	f.onJSON("POST /api/v1/add/app-passwd",
		`[{"type":"success","log":["app_passwd","add",{}],"msg":"app_passwd_added"}]`)
	f.onJSON("GET /api/v1/get/app-passwd/all/user@example.com", `[
		{"id":7,"name":"moov-webmail-deadbeef","mailbox":"user@example.com",
		 "domain":"example.com","created":"2026-08-10 17:18:16",
		 "imap_access":1,"smtp_access":1,"sieve_access":1,"dav_access":0,
		 "eas_access":0,"pop3_access":0,"active":1}]`)

	ap, err := f.client(t).CreateAppPassword(context.Background(), CreateAppPasswordRequest{
		Mailbox:    "user@example.com",
		Password:   "s3cret-app-password",
		NameSuffix: "deadbeef",
	})
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	if ap.ID != 7 {
		t.Errorf("returned id = %d, want 7", ap.ID)
	}
	if ap.Name != "moov-webmail-deadbeef" {
		t.Errorf("returned name = %q", ap.Name)
	}

	// Exactly two calls: the create, then the id lookup.
	if len(f.requests) != 2 {
		t.Fatalf("made %d requests, want 2 (create + list)", len(f.requests))
	}

	create := f.requests[0]
	if create.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", create.Method)
	}
	if create.Path != "/api/v1/add/app-passwd" {
		t.Errorf("path = %s", create.Path)
	}
	if create.APIKey != "TEST-KEY-000000" {
		t.Errorf("X-API-Key = %q", create.APIKey)
	}
	if create.CType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (json_api.php reads the body "+
			"as JSON only when this is set)", create.CType)
	}

	var payload map[string]any
	if err := json.Unmarshal(create.Body, &payload); err != nil {
		t.Fatalf("create body is not a JSON object: %v (%s)", err, create.Body)
	}
	for k, want := range map[string]string{
		"active":      "1",
		"username":    "user@example.com",
		"app_name":    "moov-webmail-deadbeef",
		"app_passwd":  "s3cret-app-password",
		"app_passwd2": "s3cret-app-password",
	} {
		got, _ := payload[k].(string)
		if got != want {
			t.Errorf("body[%q] = %q, want %q", k, got, want)
		}
	}

	// The protocols array is the field upstream issue #4588 is about: sending
	// it wrong creates a credential that authenticates against nothing.
	protocols, ok := payload["protocols"].([]any)
	if !ok {
		t.Fatalf("body[\"protocols\"] is %T, want an array", payload["protocols"])
	}
	got := map[string]bool{}
	for _, p := range protocols {
		name, ok := p.(string)
		if !ok {
			t.Fatalf("protocols contains a %T, want a string", p)
		}
		got[name] = true
	}
	want := map[string]bool{"imap_access": true, "smtp_access": true, "sieve_access": true}
	if len(got) != len(want) {
		t.Fatalf("protocols = %v, want exactly %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("protocols is missing %q", k)
		}
	}
	// The scopes NOT granted matter as much as the ones that are.
	for _, denied := range []string{"dav_access", "eas_access", "pop3_access"} {
		if got[denied] {
			t.Errorf("protocols must not contain %q: Moov provisions imap+smtp+sieve only", denied)
		}
	}
}

func TestCreateAppPasswordGeneratesUniqueNames(t *testing.T) {
	// The name is what the id lookup matches on, so two concurrent
	// provisioning runs for one mailbox must not collide.
	f := newFakeMailcow(t)
	f.onJSON("POST /api/v1/add/app-passwd", `{"type":"success","msg":"app_passwd_added"}`)
	f.onJSON("GET /api/v1/get/app-passwd/all/user@example.com", `{}`)

	c := f.client(t)
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		// The lookup finds nothing, so this errors — the name is what is under
		// test, and it is captured from the request either way.
		_, _ = c.CreateAppPassword(context.Background(), CreateAppPasswordRequest{
			Mailbox: "user@example.com", Password: "x",
		})
		var payload map[string]any
		if err := json.Unmarshal(f.requests[len(f.requests)-2].Body, &payload); err != nil {
			t.Fatalf("decoding create body: %v", err)
		}
		name, _ := payload["app_name"].(string)
		if !strings.HasPrefix(name, DefaultAppNamePrefix+"-") {
			t.Fatalf("app_name %q does not carry the %q prefix", name, DefaultAppNamePrefix)
		}
		if seen[name] {
			t.Fatalf("app_name %q was generated twice", name)
		}
		seen[name] = true
	}
}

func TestCreateAppPasswordReportsOrphanOnLookupFailure(t *testing.T) {
	// The dangerous case: Mailcow created the credential, the follow-up
	// lookup failed. The error MUST name what was created, or a live app
	// password is left behind that nobody can find.
	f := newFakeMailcow(t)
	f.onJSON("POST /api/v1/add/app-passwd", `{"type":"success","msg":"app_passwd_added"}`)
	f.on("GET /api/v1/get/app-passwd/all/user@example.com", func(w http.ResponseWriter, _ []byte) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, err := f.client(t).CreateAppPassword(context.Background(), CreateAppPasswordRequest{
		Mailbox: "user@example.com", Password: "x", NameSuffix: "abc123",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "moov-webmail-abc123") {
		t.Errorf("error does not name the created app password: %v", err)
	}
	if !strings.Contains(err.Error(), "by hand") {
		t.Errorf("error does not tell the operator to clean up: %v", err)
	}
}

func TestCreateAppPasswordDoesNotRetry(t *testing.T) {
	// A blind retry on a write would mint a second live credential.
	f := newFakeMailcow(t)
	calls := 0
	f.on("POST /api/v1/add/app-passwd", func(w http.ResponseWriter, _ []byte) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, err := f.client(t).CreateAppPassword(context.Background(), CreateAppPasswordRequest{
		Mailbox: "user@example.com", Password: "x",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Fatalf("the create was attempted %d times; writes must never be retried", calls)
	}
}

func TestCreateAppPasswordSurfacesAPIFailureOn200(t *testing.T) {
	// Mailcow answers HTTP 200 for failures. The body decides, not the status.
	f := newFakeMailcow(t)
	f.onJSON("POST /api/v1/add/app-passwd",
		`[{"type":"danger","log":["app_passwd","add",{}],"msg":"access_denied"}]`)

	_, err := f.client(t).CreateAppPassword(context.Background(), CreateAppPasswordRequest{
		Mailbox: "user@example.com", Password: "x",
	})
	if !errors.Is(err, ErrAPI) {
		t.Fatalf("got %v, want ErrAPI", err)
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("error drops Mailcow's own diagnostic: %v", err)
	}
	// It must not have gone on to the lookup after a failed create.
	if len(f.requests) != 1 {
		t.Errorf("made %d requests after a failed create, want 1", len(f.requests))
	}
}

func TestCreateAppPasswordValidatesInput(t *testing.T) {
	f := newFakeMailcow(t)
	c := f.client(t)

	cases := []struct{ name, mailbox, password string }{
		{"empty mailbox", "", "pw"},
		{"mailbox without domain", "user", "pw"},
		{"mailbox with a path separator", "user/../admin@example.com", "pw"},
		{"mailbox with a newline", "user@example.com\r\nX: y", "pw"},
		{"empty password", "user@example.com", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.CreateAppPassword(context.Background(), CreateAppPasswordRequest{
				Mailbox: tc.mailbox, Password: tc.password,
			})
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("got %v, want ErrInvalidConfig", err)
			}
		})
	}
	if len(f.requests) != 0 {
		t.Fatalf("invalid input reached the network (%d requests)", len(f.requests))
	}
}

// --- DeleteAppPassword ------------------------------------------------------

func TestDeleteAppPasswordRequestShape(t *testing.T) {
	f := newFakeMailcow(t)
	f.onJSON("POST /api/v1/delete/app-passwd",
		`[{"type":"success","log":["app_passwd","delete",{"id":["7"]}],"msg":["app_passwd_removed","7"]}]`)

	if err := f.client(t).DeleteAppPassword(context.Background(), 7); err != nil {
		t.Fatalf("DeleteAppPassword: %v", err)
	}

	req := f.last()
	// POST, not DELETE — the endpoint is /delete/app-passwd.
	if req.Method != http.MethodPost {
		t.Errorf("method = %s, want POST (Mailcow's delete endpoint is a POST)", req.Method)
	}
	if req.Path != "/api/v1/delete/app-passwd" {
		t.Errorf("path = %s", req.Path)
	}

	// The body must be a bare JSON ARRAY. json_api.php assigns the raw body to
	// $_POST['items'] for deletes, so an object body deletes nothing while
	// still reporting success.
	var ids []string
	if err := json.Unmarshal(req.Body, &ids); err != nil {
		t.Fatalf("delete body must be a JSON array of ids, got %s: %v", req.Body, err)
	}
	if len(ids) != 1 || ids[0] != "7" {
		t.Errorf("delete body = %v, want [\"7\"]", ids)
	}
}

func TestDeleteAppPasswordRejectsBadID(t *testing.T) {
	f := newFakeMailcow(t)
	for _, id := range []int64{0, -1} {
		if err := f.client(t).DeleteAppPassword(context.Background(), id); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("id %d: got %v, want ErrInvalidConfig", id, err)
		}
	}
	if len(f.requests) != 0 {
		t.Fatalf("an invalid id reached the network")
	}
}

func TestDeleteAppPasswordSurfacesFailure(t *testing.T) {
	f := newFakeMailcow(t)
	f.onJSON("POST /api/v1/delete/app-passwd",
		`[{"type":"danger","msg":"app_passwd_id_invalid"}]`)

	err := f.client(t).DeleteAppPassword(context.Background(), 99)
	if !errors.Is(err, ErrAPI) {
		t.Fatalf("got %v, want ErrAPI", err)
	}
}

// --- GetMailbox -------------------------------------------------------------

// realMailboxJSON is the response our live Mailcow (commit 281cf93,
// 2026-03-31) returned for moov-test@atmosfera.cloud, trimmed to the fields
// this client decodes. Keeping the real shape — including the string-typed
// booleans inside attributes — is what makes this test a regression guard for
// the decoder rather than a test of a fixture we invented.
const realMailboxJSON = `{
	"username": "moov-test@atmosfera.cloud",
	"active": 1,
	"active_int": 1,
	"domain": "atmosfera.cloud",
	"name": "Moov Spike Test",
	"local_part": "moov-test",
	"quota": 1073741824,
	"messages": 4,
	"attributes": {
		"force_pw_update": "0",
		"tls_enforce_in": "0",
		"sogo_access": "1",
		"imap_access": "1",
		"pop3_access": "1",
		"smtp_access": "1",
		"sieve_access": "1",
		"eas_access": "1",
		"dav_access": "1",
		"mailbox_format": "maildir:"
	}
}`

func TestGetMailbox(t *testing.T) {
	f := newFakeMailcow(t)
	f.onJSON("GET /api/v1/get/mailbox/moov-test@atmosfera.cloud", realMailboxJSON)

	mb, err := f.client(t).GetMailbox(context.Background(), "moov-test@atmosfera.cloud")
	if err != nil {
		t.Fatalf("GetMailbox: %v", err)
	}

	if req := f.last(); req.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", req.Method)
	}
	if mb.Username != "moov-test@atmosfera.cloud" {
		t.Errorf("Username = %q", mb.Username)
	}
	if mb.Domain != "atmosfera.cloud" {
		t.Errorf("Domain = %q", mb.Domain)
	}
	if mb.Quota != 1073741824 {
		t.Errorf("Quota = %d", mb.Quota)
	}
	if !mb.IsActive() {
		t.Error("IsActive() = false for a mailbox with active:1")
	}
	// The attributes are string-typed booleans in the real response; flexInt
	// is what makes them decode.
	if !mb.AllowsMoovScopes() {
		t.Error("AllowsMoovScopes() = false for a mailbox granting imap+smtp+sieve")
	}
}

func TestGetMailboxDeniedScopes(t *testing.T) {
	f := newFakeMailcow(t)
	f.onJSON("GET /api/v1/get/mailbox/no-imap@example.com", `{
		"username":"no-imap@example.com","active":1,"domain":"example.com",
		"attributes":{"imap_access":"0","smtp_access":"1","sieve_access":"1"}}`)

	mb, err := f.client(t).GetMailbox(context.Background(), "no-imap@example.com")
	if err != nil {
		t.Fatalf("GetMailbox: %v", err)
	}
	if mb.AllowsMoovScopes() {
		t.Error("AllowsMoovScopes() = true for a mailbox with imap_access 0")
	}
}

func TestGetMailboxNotFound(t *testing.T) {
	// Mailcow answers an absent mailbox with `{}` and HTTP 200.
	f := newFakeMailcow(t)
	f.onJSON("GET /api/v1/get/mailbox/ghost@example.com", `{}`)

	_, err := f.client(t).GetMailbox(context.Background(), "ghost@example.com")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// --- ListAppPasswords -------------------------------------------------------

func TestListAppPasswordsEmptyObject(t *testing.T) {
	// A mailbox with no app passwords answers `{}`, not `[]`.
	f := newFakeMailcow(t)
	f.onJSON("GET /api/v1/get/app-passwd/all/user@example.com", `{}`)

	list, err := f.client(t).ListAppPasswords(context.Background(), "user@example.com")
	if err != nil {
		t.Fatalf("ListAppPasswords: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("got %d app passwords, want 0", len(list))
	}
}

func TestListAppPasswordsDecodesRealShape(t *testing.T) {
	f := newFakeMailcow(t)
	// The real response shape, with one substitution: the password field
	// carries a PLACEHOLDER rather than the bcrypt hash the live server
	// returned. The hash is of a throwaway probe password that no longer
	// exists, but it is still key material from a production mail server and
	// this repository is public — so the fixture keeps the shape (the field is
	// present, and it is a {SCHEME}hash string) without carrying the value.
	// The client does not decode this field at all, which is the point being
	// regression-tested.
	f.onJSON("GET /api/v1/get/app-passwd/all/moov-test@atmosfera.cloud", `[{
		"id": 1, "name": "moov-e7-probe", "mailbox": "moov-test@atmosfera.cloud",
		"domain": "atmosfera.cloud",
		"password": "{BLF-CRYPT}$2y$10$PLACEHOLDER.NOT.A.REAL.HASH.0000000000000000000000",
		"created": "2026-08-10 17:18:16", "modified": null,
		"imap_access": 1, "smtp_access": 1, "dav_access": 0, "eas_access": 0,
		"pop3_access": 0, "sieve_access": 1, "active": 1}]`)

	list, err := f.client(t).ListAppPasswords(context.Background(), "moov-test@atmosfera.cloud")
	if err != nil {
		t.Fatalf("ListAppPasswords: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d app passwords, want 1", len(list))
	}
	ap := list[0]
	if ap.ID != 1 || ap.Name != "moov-e7-probe" {
		t.Errorf("decoded %+v", ap)
	}
	if ap.IMAPAccess == 0 || ap.SMTPAccess == 0 || ap.SieveAccess == 0 {
		t.Error("the imap/smtp/sieve scopes did not decode")
	}
}

// --- Transport and auth -----------------------------------------------------

func TestUnauthorizedCarriesMailcowDiagnostic(t *testing.T) {
	// The 401 body names the rejected source IP, which is the whole diagnostic
	// for the S1 H5 allowlist failure.
	f := newFakeMailcow(t)
	f.on("GET /api/v1/get/mailbox/user@example.com", func(w http.ResponseWriter, _ []byte) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","msg":"api access denied for ip 217.216.83.79"}`)
	})

	_, err := f.client(t).GetMailbox(context.Background(), "user@example.com")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
	if !strings.Contains(err.Error(), "217.216.83.79") {
		t.Errorf("error drops the rejected address: %v", err)
	}
}

func TestReadOnlyKeyIsDistinguishable(t *testing.T) {
	f := newFakeMailcow(t)
	f.on("POST /api/v1/delete/app-passwd", func(w http.ResponseWriter, _ []byte) {
		w.WriteHeader(http.StatusForbidden)
	})
	if err := f.client(t).DeleteAppPassword(context.Background(), 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("got %v, want ErrForbidden", err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	// Mailcow's nginx redirects a wrong virtual host to the UI. Following it
	// would send the API key to a URL it was not addressed to.
	f := newFakeMailcow(t)
	f.on("GET /api/v1/get/mailbox/user@example.com", func(w http.ResponseWriter, _ []byte) {
		w.Header().Set("Location", "https://elsewhere.example/login")
		w.WriteHeader(http.StatusFound)
	})

	_, err := f.client(t).GetMailbox(context.Background(), "user@example.com")
	if !errors.Is(err, ErrUnexpectedResponse) {
		t.Fatalf("got %v, want ErrUnexpectedResponse", err)
	}
	if !strings.Contains(err.Error(), "BaseURL") {
		t.Errorf("error does not point at the misconfiguration: %v", err)
	}
}

func TestHostHeaderOverride(t *testing.T) {
	// Reaching nginx by container IP needs the Host header to match
	// MAILCOW_HOSTNAME, or the default vhost answers instead of the API.
	f := newFakeMailcow(t)
	f.onJSON("GET /api/v1/get/mailbox/user@example.com",
		`{"username":"user@example.com","domain":"example.com","active":1}`)

	c, err := New(Config{
		BaseURL: f.server.URL, APIKey: "K", HostHeader: "mail.atmosfera.cloud",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.GetMailbox(context.Background(), "user@example.com"); err != nil {
		t.Fatalf("GetMailbox: %v", err)
	}
	if got := f.last().Host; got != "mail.atmosfera.cloud" {
		t.Errorf("Host header = %q, want mail.atmosfera.cloud", got)
	}
}

func TestGarbageResponseIsNotSuccess(t *testing.T) {
	// A proxy error page instead of the API must never read as success.
	f := newFakeMailcow(t)
	f.on("POST /api/v1/delete/app-passwd", func(w http.ResponseWriter, _ []byte) {
		_, _ = io.WriteString(w, "<html><body>502 Bad Gateway</body></html>")
	})
	if err := f.client(t).DeleteAppPassword(context.Background(), 1); !errors.Is(err, ErrUnexpectedResponse) {
		t.Fatalf("got %v, want ErrUnexpectedResponse", err)
	}
}

func TestContextCancellation(t *testing.T) {
	f := newFakeMailcow(t)
	f.onJSON("GET /api/v1/get/mailbox/user@example.com", `{"username":"user@example.com"}`)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.client(t).GetMailbox(ctx, "user@example.com")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestErrorsNeverLeakTheAPIKey(t *testing.T) {
	// Every error from this package ends up in an operator log.
	const key = "SUPER-SECRET-API-KEY-123456"
	f := newFakeMailcow(t)
	f.on("POST /api/v1/add/app-passwd", func(w http.ResponseWriter, _ []byte) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","msg":"api access denied"}`)
	})
	f.on("GET /api/v1/get/mailbox/user@example.com", func(w http.ResponseWriter, _ []byte) {
		w.WriteHeader(http.StatusForbidden)
	})

	c, err := New(Config{BaseURL: f.server.URL, APIKey: key})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, createErr := c.CreateAppPassword(context.Background(), CreateAppPasswordRequest{
		Mailbox: "user@example.com", Password: "pw",
	})
	_, getErr := c.GetMailbox(context.Background(), "user@example.com")

	for _, e := range []error{createErr, getErr} {
		if e == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(e.Error(), key) {
			t.Fatalf("error leaks the API key: %v", e)
		}
	}

	// And neither does the loggable config rendering.
	if s := c.Config().String(); strings.Contains(s, key) {
		t.Fatalf("Config.String() leaks the API key: %s", s)
	}
}

// --- flexInt ----------------------------------------------------------------

func TestFlexInt(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{`1`, 1}, {`"1"`, 1}, {`0`, 0}, {`"0"`, 0},
		{`null`, 0}, {`""`, 0}, {`1073741824`, 1073741824}, {`"42"`, 42},
	}
	for _, tc := range cases {
		var f flexInt
		if err := json.Unmarshal([]byte(tc.in), &f); err != nil {
			t.Errorf("flexInt(%s): %v", tc.in, err)
			continue
		}
		if int64(f) != tc.want {
			t.Errorf("flexInt(%s) = %d, want %d", tc.in, f, tc.want)
		}
	}

	var f flexInt
	if err := json.Unmarshal([]byte(`"not a number"`), &f); err == nil {
		t.Error("flexInt accepted a non-numeric string")
	}
}

// --- password generation ----------------------------------------------------

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		pw, err := GeneratePassword()
		if err != nil {
			t.Fatalf("GeneratePassword: %v", err)
		}
		if len(pw) != GeneratedPasswordLength {
			t.Fatalf("length = %d, want %d", len(pw), GeneratedPasswordLength)
		}
		if seen[pw] {
			t.Fatalf("a password repeated after %d draws", i+1)
		}
		seen[pw] = true
		// The alphabet exists so the value survives IMAP literals, shell
		// quoting and a human reading it aloud.
		for _, r := range pw {
			if !strings.ContainsRune(passwordAlphabet, r) {
				t.Fatalf("password contains %q, which is outside the alphabet", r)
			}
		}
	}
}
