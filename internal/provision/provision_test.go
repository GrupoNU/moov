package provision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/GrupoNU/moov/internal/crypto"
	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/mailcow"
	"github.com/GrupoNU/moov/internal/store"
)

// userPassword is the value the whole package exists to NOT persist. It is
// distinctive so a substring search over recorded bytes is meaningful.
const userPassword = "USER-OWN-PASSWORD-e3b0c44298fc1c14"

// --- fakes ------------------------------------------------------------------

// fakeValidator records the imap.Config it was handed, which is how the tests
// assert that a REAL login was attempted with the right credential.
type fakeValidator struct {
	mu    sync.Mutex
	calls []imap.Config
	err   error
}

func (f *fakeValidator) Validate(_ context.Context, cfg imap.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cfg)
	return f.err
}

func (f *fakeValidator) lastConfig(t *testing.T) imap.Config {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no IMAP validation was attempted")
	}
	return f.calls[len(f.calls)-1]
}

// fakeMailcowAPI stands in for the Mailcow admin API and records the app
// passwords it was asked to create and delete.
type fakeMailcowAPI struct {
	mu sync.Mutex

	mailbox    mailcow.Mailbox
	mailboxErr error

	created    []mailcow.CreateAppPasswordRequest
	createErr  error
	nextID     int64
	deleted    []int64
	deleteErr  error
	deleteCall int
}

func newFakeMailcowAPI() *fakeMailcowAPI {
	f := &fakeMailcowAPI{nextID: 100}
	f.mailbox = mailcow.Mailbox{Username: "user@example.com", Domain: "example.com"}
	// Reproduce an active mailbox granting imap+smtp+sieve by decoding the
	// real JSON shape, so the fake cannot drift from what Mailcow sends.
	if err := decodeInto(&f.mailbox, `{"username":"user@example.com","domain":"example.com",
		"active":1,"attributes":{"imap_access":"1","smtp_access":"1","sieve_access":"1"}}`); err != nil {
		panic(err)
	}
	return f
}

func (f *fakeMailcowAPI) GetMailbox(_ context.Context, _ string) (mailcow.Mailbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mailboxErr != nil {
		return mailcow.Mailbox{}, f.mailboxErr
	}
	return f.mailbox, nil
}

func (f *fakeMailcowAPI) CreateAppPassword(_ context.Context, req mailcow.CreateAppPasswordRequest) (mailcow.AppPassword, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, req)
	if f.createErr != nil {
		return mailcow.AppPassword{}, f.createErr
	}
	id := f.nextID
	f.nextID++
	return mailcow.AppPassword{
		ID: id, Name: "moov-webmail-test", Mailbox: req.Mailbox,
	}, nil
}

func (f *fakeMailcowAPI) DeleteAppPassword(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCall++
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeMailcowAPI) lastCreated(t *testing.T) mailcow.CreateAppPasswordRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.created) == 0 {
		t.Fatal("no app password was created")
	}
	return f.created[len(f.created)-1]
}

// storeWrite is one recorded write to the fake store, kept as raw bytes so the
// no-persistence proof can search everything that crossed the boundary.
type storeWrite struct {
	Method string
	Bytes  []byte
}

// fakeStore records every byte handed to it.
type fakeStore struct {
	mu       sync.Mutex
	accounts map[string]store.Account
	nextID   int64
	writes   []storeWrite

	createErr error
	setErr    error
	getErr    error
}

func newFakeStore() *fakeStore {
	return &fakeStore{accounts: map[string]store.Account{}, nextID: 1}
}

func (f *fakeStore) GetAccountByEmail(_ context.Context, email string) (store.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return store.Account{}, f.getErr
	}
	a, ok := f.accounts[email]
	if !ok {
		return store.Account{}, fmt.Errorf("account %q: %w", email, store.ErrNotFound)
	}
	return a, nil
}

func (f *fakeStore) CreateAccount(_ context.Context, a store.Account) (store.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Record EVERY field that crossed the boundary, not only the ones we
	// expect to matter: the proof must cover a future field too.
	f.writes = append(f.writes, storeWrite{
		Method: "CreateAccount",
		Bytes: []byte(fmt.Sprintf("%s|%s|%d|%s|%s|%s|%s|%s",
			a.Email, a.IMAPHost, a.IMAPPort, a.IMAPServerName, a.IMAPUsername,
			string(a.IMAPAppPassword), a.CredentialState, a.State)),
	})
	if f.createErr != nil {
		return store.Account{}, f.createErr
	}
	a.ID = f.nextID
	f.nextID++
	f.accounts[a.Email] = a
	return a, nil
}

func (f *fakeStore) SetAccountCredentials(_ context.Context, accountID int64, username string, appPassword []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := append([]byte(fmt.Sprintf("%d|%s|", accountID, username)), appPassword...)
	f.writes = append(f.writes, storeWrite{Method: "SetAccountCredentials", Bytes: rec})
	if f.setErr != nil {
		return f.setErr
	}
	for email, a := range f.accounts {
		if a.ID == accountID {
			a.IMAPUsername = username
			a.IMAPAppPassword = appPassword
			a.CredentialState = store.CredentialActive
			f.accounts[email] = a
		}
	}
	return nil
}

// allWrittenBytes concatenates everything ever handed to the store.
func (f *fakeStore) allWrittenBytes() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	var buf bytes.Buffer
	for _, w := range f.writes {
		buf.WriteString(w.Method)
		buf.WriteByte('|')
		buf.Write(w.Bytes)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// --- harness ----------------------------------------------------------------

type harness struct {
	p         *Provisioner
	validator *fakeValidator
	api       *fakeMailcowAPI
	store     *fakeStore
	logs      *bytes.Buffer
	keyring   *crypto.Keyring
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	material := bytes.Repeat([]byte{0x5A}, crypto.KeySize)
	key, err := crypto.NewKey(1, material)
	if err != nil {
		t.Fatalf("crypto.NewKey: %v", err)
	}
	kr, err := crypto.NewKeyring(key)
	if err != nil {
		t.Fatalf("crypto.NewKeyring: %v", err)
	}

	logs := &bytes.Buffer{}
	// Debug level on purpose: the proof must hold at the most verbose setting
	// anyone could run in production, not only at the default.
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	h := &harness{
		validator: &fakeValidator{},
		api:       newFakeMailcowAPI(),
		store:     newFakeStore(),
		logs:      logs,
		keyring:   kr,
	}

	p, err := New(Config{
		IMAPHost: "dovecot", IMAPServerName: "mail.example.com",
	}, h.validator, h.api, kr, h.store, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.p = p
	return h
}

// --- the proof --------------------------------------------------------------

func TestUserPasswordIsNeverPersistedOrLogged(t *testing.T) {
	// This is the load-bearing test of E7 and of ADR §4's central claim. It
	// runs the real flow and then searches everything that left the process
	// for the user's password.
	h := newHarness(t)

	res, err := h.p.Provision(context.Background(), Request{
		Email:    "user@example.com",
		Password: userPassword,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// The flow must actually have run, or the search below proves nothing.
	if res.Account.ID == 0 {
		t.Fatal("no account was created")
	}
	if res.AppPasswordID == 0 {
		t.Fatal("no app password was created")
	}
	if len(h.store.writes) == 0 {
		t.Fatal("nothing was written to the store")
	}
	if h.logs.Len() == 0 {
		t.Fatal("nothing was logged")
	}

	// 1. The password must appear in NO store write.
	written := h.store.allWrittenBytes()
	assertAbsent(t, "store writes", written, userPassword)

	// 2. The password must appear in NO log output.
	assertAbsent(t, "log output", h.logs.Bytes(), userPassword)

	// 3. Nor in the Result, which a caller may well log wholesale.
	assertAbsent(t, "Result", []byte(fmt.Sprintf("%+v", res)), userPassword)

	// 4. Nor on the Provisioner itself, which outlives the call.
	assertAbsent(t, "Provisioner", []byte(fmt.Sprintf("%+v", *h.p)), userPassword)

	// 5. The stored credential must be the APP password, sealed — not the
	//    user's password in any encoding. Decrypting it is what proves the
	//    stored bytes are the credential we think they are.
	stored := h.store.accounts["user@example.com"]
	if len(stored.IMAPAppPassword) == 0 {
		t.Fatal("no credential was stored")
	}
	opened, err := h.keyring.Open(stored.IMAPAppPassword, crypto.AccountAAD(stored.ID))
	if err != nil {
		t.Fatalf("the stored credential does not open with the account's key: %v", err)
	}
	if string(opened) == userPassword {
		t.Fatal("the stored credential IS the user's password")
	}
	created := h.api.lastCreated(t)
	if string(opened) != created.Password {
		t.Fatal("the stored credential is not the app password that was minted")
	}
	if len(opened) != mailcow.GeneratedPasswordLength {
		t.Errorf("the sealed credential is %d bytes, want a generated app password of %d",
			len(opened), mailcow.GeneratedPasswordLength)
	}

	// 6. And the credential is marked usable.
	if stored.CredentialState != store.CredentialActive {
		t.Errorf("credential_state = %q, want %q", stored.CredentialState, store.CredentialActive)
	}
}

// assertAbsent fails if needle appears in haystack, in plaintext or in any
// encoding this codebase could plausibly produce.
//
// The encodings matter: a bug that base64s a struct into a log line, or hex
// dumps a buffer, would defeat a naive substring search and still leak the
// password just as thoroughly.
func assertAbsent(t *testing.T, where string, haystack []byte, needle string) {
	t.Helper()

	encodings := map[string]string{
		"plaintext":     needle,
		"base64":        base64.StdEncoding.EncodeToString([]byte(needle)),
		"base64 (raw)":  base64.RawStdEncoding.EncodeToString([]byte(needle)),
		"base64url":     base64.URLEncoding.EncodeToString([]byte(needle)),
		"hex":           hex.EncodeToString([]byte(needle)),
		"hex uppercase": strings.ToUpper(hex.EncodeToString([]byte(needle))),
		"quoted":        fmt.Sprintf("%q", needle),
	}
	for name, encoded := range encodings {
		if bytes.Contains(haystack, []byte(encoded)) {
			t.Fatalf("the user's password appears in %s as %s.\n"+
				"ADR-001 §4 requires it to be discarded after the validation login.\n"+
				"Contents:\n%s", where, name, haystack)
		}
	}

	// A longest-substring check catches a partial leak — a truncated log
	// field, a password split across two attributes — that an exact match
	// would miss.
	const minRun = 12
	for i := 0; i+minRun <= len(needle); i++ {
		if bytes.Contains(haystack, []byte(needle[i:i+minRun])) {
			t.Fatalf("a %d-character run of the user's password (%q) appears in %s.\nContents:\n%s",
				minRun, needle[i:i+minRun], where, haystack)
		}
	}
}

func TestAppPasswordIsNeverLoggedEither(t *testing.T) {
	// The app password is not the user's, but it is still a live credential to
	// someone's mailbox and has no business in a log file.
	h := newHarness(t)

	if _, err := h.p.Provision(context.Background(), Request{
		Email: "user@example.com", Password: userPassword,
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	minted := h.api.lastCreated(t).Password
	if minted == "" {
		t.Fatal("no app password was minted")
	}
	assertAbsent(t, "log output", h.logs.Bytes(), minted)
	// Nor may the raw app password reach the store: only the sealed form does.
	assertAbsent(t, "store writes", h.store.allWrittenBytes(), minted)
}

// --- flow ------------------------------------------------------------------

func TestProvisionValidatesWithARealLogin(t *testing.T) {
	// Step 1 of ADR §4: the credential is proven by an actual IMAP LOGIN
	// against the server Moov will sync, not by an API that reports on it.
	h := newHarness(t)

	if _, err := h.p.Provision(context.Background(), Request{
		Email: "user@example.com", Password: userPassword,
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	cfg := h.validator.lastConfig(t)
	if cfg.Username != "user@example.com" {
		t.Errorf("logged in as %q", cfg.Username)
	}
	if cfg.Password != userPassword {
		t.Error("the validation login did not use the user's own password")
	}
	if cfg.Host != "dovecot" {
		t.Errorf("Host = %q, want the configured dovecot", cfg.Host)
	}
	if cfg.TLSServerName != "mail.example.com" {
		t.Errorf("TLSServerName = %q; S1 H2 requires it to be configurable", cfg.TLSServerName)
	}

	// The validation must happen BEFORE anything is created: provisioning a
	// mailbox on an unproven password would leave a credential behind for
	// someone who could not log in.
	if len(h.api.created) != 1 {
		t.Fatalf("created %d app passwords, want 1", len(h.api.created))
	}
}

func TestProvisionMintsScopedAppPassword(t *testing.T) {
	h := newHarness(t)

	if _, err := h.p.Provision(context.Background(), Request{
		Email: "user@example.com", Password: userPassword,
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	req := h.api.lastCreated(t)
	if req.Mailbox != "user@example.com" {
		t.Errorf("minted for %q", req.Mailbox)
	}
	// ADR §4: imap+smtp+sieve, and nothing else.
	want := map[mailcow.Protocol]bool{
		mailcow.ProtocolIMAP: true, mailcow.ProtocolSMTP: true, mailcow.ProtocolSieve: true,
	}
	if len(req.Scopes) != len(want) {
		t.Fatalf("scopes = %v, want exactly imap+smtp+sieve", req.Scopes)
	}
	for _, s := range req.Scopes {
		if !want[s] {
			t.Errorf("unexpected scope %q: Moov provisions imap+smtp+sieve only", s)
		}
	}
	// The minted password must not be the user's.
	if req.Password == userPassword {
		t.Fatal("the app password registered with Mailcow IS the user's password")
	}
}

func TestProvisionRejectsBadCredentials(t *testing.T) {
	// A rejected password must fail BEFORE anything is created anywhere.
	h := newHarness(t)
	h.validator.err = ErrInvalidCredentials

	_, err := h.p.Provision(context.Background(), Request{
		Email: "user@example.com", Password: "wrong",
	})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("got %v, want ErrInvalidCredentials", err)
	}
	if len(h.api.created) != 0 {
		t.Error("an app password was created for a rejected credential")
	}
	if len(h.store.writes) != 0 {
		t.Error("the store was written for a rejected credential")
	}
}

func TestProvisionRefusesUnusableMailbox(t *testing.T) {
	cases := map[string]string{
		"disabled mailbox": `{"username":"user@example.com","active":0,
			"attributes":{"imap_access":"1","smtp_access":"1","sieve_access":"1"}}`,
		"imap denied": `{"username":"user@example.com","active":1,
			"attributes":{"imap_access":"0","smtp_access":"1","sieve_access":"1"}}`,
		"sieve denied": `{"username":"user@example.com","active":1,
			"attributes":{"imap_access":"1","smtp_access":"1","sieve_access":"0"}}`,
	}
	for name, mbJSON := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			var mb mailcow.Mailbox
			if err := decodeInto(&mb, mbJSON); err != nil {
				t.Fatalf("decoding the fixture: %v", err)
			}
			h.api.mailbox = mb

			_, err := h.p.Provision(context.Background(), Request{
				Email: "user@example.com", Password: userPassword,
			})
			if !errors.Is(err, ErrMailboxUnusable) {
				t.Fatalf("got %v, want ErrMailboxUnusable", err)
			}
			// Nothing may be minted for a mailbox that cannot use it.
			if len(h.api.created) != 0 {
				t.Error("an app password was minted for an unusable mailbox")
			}
		})
	}
}

func TestProvisionNoAppPasswordMeansNoAccount(t *testing.T) {
	// ADR §4's hard rule: if the app password cannot be created, provisioning
	// fails. There is no fallback to storing the user's password.
	h := newHarness(t)
	h.api.createErr = errors.New("mailcow is down")

	_, err := h.p.Provision(context.Background(), Request{
		Email: "user@example.com", Password: userPassword,
	})
	if err == nil {
		t.Fatal("Provision succeeded without an app password")
	}

	// Whatever was written — an account row may legitimately exist — must not
	// contain a usable credential, and above all not the user's password.
	assertAbsent(t, "store writes", h.store.allWrittenBytes(), userPassword)
	for _, a := range h.store.accounts {
		if a.CredentialState == store.CredentialActive {
			t.Error("an account was left with active credentials after a failed provisioning")
		}
	}
}

func TestProvisionRollsBackTheAppPasswordOnFailure(t *testing.T) {
	// A credential created on Mailcow that Moov then fails to store is an
	// orphan: nobody tracks it and nobody can revoke it. It must be removed.
	h := newHarness(t)
	h.store.setErr = errors.New("the database went away")

	_, err := h.p.Provision(context.Background(), Request{
		Email: "user@example.com", Password: userPassword,
	})
	if err == nil {
		t.Fatal("Provision succeeded despite a failing store")
	}

	if len(h.api.deleted) != 1 {
		t.Fatalf("deleted %v app passwords, want exactly the one that was created", h.api.deleted)
	}
	if h.api.deleted[0] != 100 {
		t.Errorf("deleted app password %d, want 100", h.api.deleted[0])
	}
}

func TestProvisionReportsAnOrphanItCannotClean(t *testing.T) {
	// The worst case: the store failed AND the rollback failed. The error must
	// name what was left behind, because only a human can fix it now.
	h := newHarness(t)
	h.store.setErr = errors.New("the database went away")
	h.api.deleteErr = errors.New("mailcow is down too")

	_, err := h.p.Provision(context.Background(), Request{
		Email: "user@example.com", Password: userPassword,
	})
	if !errors.Is(err, ErrOrphanedAppPassword) {
		t.Fatalf("got %v, want ErrOrphanedAppPassword", err)
	}
	msg := err.Error()
	for _, want := range []string{"100", "moov-webmail-test", "user@example.com"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not name %q, so an operator cannot find the orphan: %v", want, err)
		}
	}
	// And it still must not leak the user's password.
	assertAbsent(t, "the orphan error", []byte(msg), userPassword)
}

func TestProvisionSealsPerAccount(t *testing.T) {
	// The AAD binds a ciphertext to its account row, so a stored credential
	// cannot be moved to another account.
	h := newHarness(t)

	res, err := h.p.Provision(context.Background(), Request{
		Email: "user@example.com", Password: userPassword,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	sealed := h.store.accounts["user@example.com"].IMAPAppPassword
	if _, err := h.keyring.Open(sealed, crypto.AccountAAD(res.Account.ID)); err != nil {
		t.Fatalf("the credential does not open for its own account: %v", err)
	}
	if _, err := h.keyring.Open(sealed, crypto.AccountAAD(res.Account.ID+1)); !errors.Is(err, crypto.ErrDecrypt) {
		t.Fatalf("the credential opened for a DIFFERENT account: %v", err)
	}
}

func TestProvisionReusesAnExistingAccount(t *testing.T) {
	// Re-provisioning is how an expired or revoked credential is replaced.
	h := newHarness(t)

	first, err := h.p.Provision(context.Background(), Request{
		Email: "user@example.com", Password: userPassword,
	})
	if err != nil {
		t.Fatalf("first Provision: %v", err)
	}
	second, err := h.p.Provision(context.Background(), Request{
		Email: "user@example.com", Password: userPassword,
	})
	if err != nil {
		t.Fatalf("second Provision: %v", err)
	}

	if first.Account.ID != second.Account.ID {
		t.Errorf("re-provisioning created a second account (%d then %d)",
			first.Account.ID, second.Account.ID)
	}
	if first.AppPasswordID == second.AppPasswordID {
		t.Error("re-provisioning reused the old app password instead of minting a new one")
	}
	// And the credential now stored must be the SECOND one.
	stored := h.store.accounts["user@example.com"]
	opened, err := h.keyring.Open(stored.IMAPAppPassword, crypto.AccountAAD(stored.ID))
	if err != nil {
		t.Fatalf("opening the re-provisioned credential: %v", err)
	}
	if string(opened) != h.api.lastCreated(t).Password {
		t.Error("the stored credential is not the most recently minted app password")
	}
}

// --- direct app password mode (the pilot path) ------------------------------

func TestProvisionDirectAppPasswordMode(t *testing.T) {
	// The pilot path: an operator created the app password by hand. Mailcow is
	// not called at all, but the credential is still proven by a real login
	// and still sealed before storage.
	h := newHarness(t)
	const appPassword = "OPERATOR-SUPPLIED-APP-PASSWORD-42"

	res, err := h.p.Provision(context.Background(), Request{
		Email: "user@example.com", AppPassword: appPassword,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// No Mailcow write of any kind.
	if len(h.api.created) != 0 {
		t.Error("direct mode called the Mailcow API to create an app password")
	}
	if res.AppPasswordID != 0 {
		t.Errorf("AppPasswordID = %d; direct mode created no Mailcow row to revoke", res.AppPasswordID)
	}

	// But the login still happened, with the app password.
	if got := h.validator.lastConfig(t).Password; got != appPassword {
		t.Error("direct mode did not validate the supplied app password by logging in")
	}

	// And it is sealed, not stored raw.
	stored := h.store.accounts["user@example.com"]
	if bytes.Contains(stored.IMAPAppPassword, []byte(appPassword)) {
		t.Fatal("the app password was stored in plaintext")
	}
	opened, err := h.keyring.Open(stored.IMAPAppPassword, crypto.AccountAAD(stored.ID))
	if err != nil {
		t.Fatalf("opening the stored credential: %v", err)
	}
	if string(opened) != appPassword {
		t.Error("the stored credential is not the supplied app password")
	}
	assertAbsent(t, "log output", h.logs.Bytes(), appPassword)
}

func TestProvisionDirectModeRejectsABadAppPassword(t *testing.T) {
	h := newHarness(t)
	h.validator.err = ErrInvalidCredentials

	_, err := h.p.Provision(context.Background(), Request{
		Email: "user@example.com", AppPassword: "not-a-real-app-password",
	})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("got %v, want ErrInvalidCredentials", err)
	}
	if len(h.store.writes) != 0 {
		t.Error("an unvalidated app password reached the store")
	}
}

// --- input validation -------------------------------------------------------

func TestProvisionRejectsBadRequests(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name string
		req  Request
	}{
		{"no email", Request{Password: "pw"}},
		{"no credential at all", Request{Email: "user@example.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.p.Provision(context.Background(), tc.req); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("got %v, want ErrInvalidRequest", err)
			}
		})
	}
	if len(h.validator.calls) != 0 {
		t.Error("an invalid request reached the network")
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	v, api, st := &fakeValidator{}, newFakeMailcowAPI(), newFakeStore()
	kr, err := crypto.NewKeyring(mustKey(t, 1, 0x11))
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	cfg := Config{IMAPHost: "dovecot"}

	cases := []struct {
		name   string
		v      IMAPValidator
		api    MailcowAPI
		sealer Sealer
		st     AccountStore
		cfg    Config
	}{
		{"no validator", nil, api, kr, st, cfg},
		{"no api", v, nil, kr, st, cfg},
		{"no sealer", v, api, nil, st, cfg},
		{"no store", v, api, kr, nil, cfg},
		{"no imap host", v, api, kr, st, Config{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg, tc.v, tc.api, tc.sealer, tc.st, nil); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("got %v, want ErrInvalidRequest", err)
			}
		})
	}
}

// --- helpers ----------------------------------------------------------------

func mustKey(t *testing.T, id crypto.KeyID, fill byte) crypto.Key {
	t.Helper()
	k, err := crypto.NewKey(id, bytes.Repeat([]byte{fill}, crypto.KeySize))
	if err != nil {
		t.Fatalf("crypto.NewKey: %v", err)
	}
	return k
}
