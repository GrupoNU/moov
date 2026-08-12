package jmaphttp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for migrations

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/store"
)

// Integration test: real HTTP Basic auth through the complete HTTP stack
// against a REAL Dovecot, with a real PostgreSQL store deciding provisioning.
//
// It is skipped unless the environment provides both worlds, following the
// repo's established conventions (internal/imap for Dovecot, internal/store
// for PostgreSQL):
//
//	MOOV_TEST_DATABASE_URL    the PostgreSQL DSN (own migrations are applied)
//	MOOV_IMAP_TEST_HOST       Dovecot host, e.g. "dovecot" on the Mailcow network
//	MOOV_IMAP_TEST_PORT       optional, default 143
//	MOOV_IMAP_TEST_USER       a DEDICATED test mailbox, never a real one
//	MOOV_IMAP_TEST_PASSWORD   environment only — never a file in this repo
//	MOOV_IMAP_TEST_SERVERNAME optional certificate name (S1 H2)
//	MOOV_IMAP_TEST_INSECURE   optional "1" to skip TLS verification (test nets only)
//
// Optionally, a second REAL mailbox that is deliberately NOT provisioned
// exercises the J-A1 403 path end to end:
//
//	MOOV_JMAP_TEST_UNPROVISIONED_USER / _PASSWORD
//
// The test provisions the account row itself and removes it afterwards; it
// performs no IMAP writes of any kind (auth is LOGIN + LOGOUT).
func TestIntegrationJMAPOverRealDovecot(t *testing.T) {
	dsn := os.Getenv("MOOV_TEST_DATABASE_URL")
	host := os.Getenv("MOOV_IMAP_TEST_HOST")
	user := os.Getenv("MOOV_IMAP_TEST_USER")
	pass := os.Getenv("MOOV_IMAP_TEST_PASSWORD")
	if dsn == "" || host == "" || user == "" || pass == "" {
		t.Skip("integration test: set MOOV_TEST_DATABASE_URL, MOOV_IMAP_TEST_HOST, " +
			"MOOV_IMAP_TEST_USER and MOOV_IMAP_TEST_PASSWORD to run")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// --- Store: migrate and provision the test account. -------------------
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	st, err := store.Open(ctx, store.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer st.Close()

	email := strings.ToLower(user)
	acct, err := st.GetAccountByEmail(ctx, email)
	created := false
	if errors.Is(err, store.ErrNotFound) {
		acct, err = st.CreateAccount(ctx, store.Account{
			Email:    email,
			IMAPHost: host,
		})
		created = true
	}
	if err != nil {
		t.Fatalf("provisioning the test account: %v", err)
	}
	if created {
		defer func() {
			if derr := st.DeleteAccount(context.Background(), acct.ID); derr != nil {
				t.Errorf("cleanup: deleting test account: %v", derr)
			}
		}()
	}

	// --- The real HTTP stack over the real validator. ----------------------
	port := 143
	if v := os.Getenv("MOOV_IMAP_TEST_PORT"); v != "" {
		port, err = strconv.Atoi(v)
		if err != nil {
			t.Fatalf("MOOV_IMAP_TEST_PORT: %v", err)
		}
	}
	validator := &IMAPLoginValidator{
		Host:               host,
		Port:               port,
		TLSServerName:      os.Getenv("MOOV_IMAP_TEST_SERVERNAME"),
		InsecureSkipVerify: os.Getenv("MOOV_IMAP_TEST_INSECURE") == "1",
		Logger:             discardLogger(),
	}
	auth, err := NewAuthenticator(AuthConfig{
		Validator: validator,
		Directory: st,
		Logger:    discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{Logger: discardLogger()}, auth)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := ts.Client()
	get := func(path, u, p string) *http.Response {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if u != "" {
			req.SetBasicAuth(u, p)
		}
		resp, derr := client.Do(req)
		if derr != nil {
			t.Fatal(derr)
		}
		return resp
	}

	// --- Session served through real Basic auth. ---------------------------
	t.Run("SessionWithRealCredentials", func(t *testing.T) {
		resp := get(PathWellKnown, user, pass)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("session = %d", resp.StatusCode)
		}
		var session map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
			t.Fatal(err)
		}
		accounts, ok := session["accounts"].(map[string]any)
		if !ok || len(accounts) != 1 {
			t.Fatalf("accounts = %v", session["accounts"])
		}
		if _, ok := accounts[jmap.EncodeAccountID(acct.ID)]; !ok {
			t.Fatalf("session lacks the provisioned account id %s", jmap.EncodeAccountID(acct.ID))
		}
		if session["username"] != email {
			t.Fatalf("username = %v", session["username"])
		}
	})

	// --- Core/echo round trip. ---------------------------------------------
	t.Run("CoreEchoRoundTrip", func(t *testing.T) {
		body := `{"using":["urn:ietf:params:jmap:core"],"methodCalls":[["Core/echo",{"ping":"real-dovecot"},"c1"]]}`
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+PathAPI, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth(user, pass)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("api = %d", resp.StatusCode)
		}
		var out struct {
			MethodResponses []json.RawMessage `json:"methodResponses"`
			SessionState    string            `json:"sessionState"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if len(out.MethodResponses) != 1 ||
			!strings.Contains(string(out.MethodResponses[0]), `"ping":"real-dovecot"`) {
			t.Fatalf("echo = %v", out.MethodResponses)
		}
		if out.SessionState == "" {
			t.Fatal("no sessionState")
		}
	})

	// --- Wrong password against the real Dovecot. --------------------------
	t.Run("WrongPasswordIs401", func(t *testing.T) {
		resp := get(PathWellKnown, user, "definitely-wrong-"+pass)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong password = %d, want 401", resp.StatusCode)
		}
		if resp.Header.Get("WWW-Authenticate") == "" {
			t.Fatal("401 without a Basic challenge")
		}
	})

	// --- Cache: a second valid request must not pay a second LOGIN. --------
	t.Run("SecondRequestServedFromCache", func(t *testing.T) {
		start := time.Now()
		resp := get(PathWellKnown, user, pass)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("cached request = %d", resp.StatusCode)
		}
		// Not a strict proof (that lives in the unit tests with a counting
		// fake); here we only sanity-check the cached path is fast.
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Logf("note: cached auth took %s — cache may not have been hit", elapsed)
		}
	})

	// --- J-A1 403: real login, unprovisioned account. ----------------------
	t.Run("UnprovisionedRealLoginIs403", func(t *testing.T) {
		u2 := os.Getenv("MOOV_JMAP_TEST_UNPROVISIONED_USER")
		p2 := os.Getenv("MOOV_JMAP_TEST_UNPROVISIONED_PASSWORD")
		if u2 == "" || p2 == "" {
			t.Skip("no second (unprovisioned) test mailbox configured; " +
				"the 403 path is covered by the fake-side unit tests")
		}
		resp := get(PathWellKnown, u2, p2)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("unprovisioned real login = %d, want 403", resp.StatusCode)
		}
	})
}
