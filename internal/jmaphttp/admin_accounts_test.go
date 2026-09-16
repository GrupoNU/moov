package jmaphttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/accounts"
	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/mailcow"
	"github.com/GrupoNU/moov/internal/provision"
	"github.com/GrupoNU/moov/internal/store"
)

// The TRANSPORT half of contract §6 M1. The state machine's own criteria are
// pinned in internal/accounts; what is pinned here is what only the wire can
// answer: the no-oracle 404, the second authentication class, the statuses
// and shapes of §2.2/§2.3, and the export download capability of §2.6.

const (
	apiDomain   = "events.example.test"
	apiOther    = "other.example.test"
	apiAddress  = "expo@events.example.test"
	apiHost     = "mail.example.test"
	testWriteSA = "sa_write"
	testReadSA  = "sa_read"
)

// --- fakes -------------------------------------------------------------------

// fakeKeyStore resolves presented keys. It is the accounts.KeyStore seam,
// which is all the HTTP layer needs to exercise authentication.
type fakeKeyStore struct {
	mu   sync.Mutex
	keys map[string]store.ServiceAccount // by hex of the hash
}

func newFakeKeyStore() *fakeKeyStore {
	return &fakeKeyStore{keys: map[string]store.ServiceAccount{}}
}

func (k *fakeKeyStore) add(secret string, sa store.ServiceAccount) {
	k.mu.Lock()
	defer k.mu.Unlock()
	sa.KeyHash = accounts.HashKey(secret)
	k.keys[string(sa.KeyHash)] = sa
}

func (k *fakeKeyStore) CreateServiceAccount(_ context.Context, sa store.ServiceAccount) (store.ServiceAccount, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.keys[string(sa.KeyHash)] = sa
	return sa, nil
}

func (k *fakeKeyStore) GetServiceAccountByHash(_ context.Context, h []byte) (store.ServiceAccount, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	sa, ok := k.keys[string(h)]
	if !ok {
		return store.ServiceAccount{}, store.ErrNotFound
	}
	return sa, nil
}

func (k *fakeKeyStore) ListServiceAccounts(_ context.Context) ([]store.ServiceAccount, error) {
	return nil, nil
}

func (k *fakeKeyStore) RevokeServiceAccount(_ context.Context, id string, at time.Time) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	for h, sa := range k.keys {
		if sa.ID == id {
			t := at
			sa.RevokedAt = &t
			k.keys[h] = sa
		}
	}
	return nil
}

// apiStore is the minimal accounts.Store the transport tests need.
type apiStore struct {
	mu       sync.Mutex
	accounts map[string]store.Account
	audit    []store.AuditLine
	exports  map[string]store.Export
	latest   map[string]string
	nextID   int64
}

func newAPIStore() *apiStore {
	return &apiStore{
		accounts: map[string]store.Account{},
		exports:  map[string]store.Export{},
		latest:   map[string]string{},
		nextID:   1,
	}
}

func (s *apiStore) seed(email string) store.Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := store.Account{
		ID: s.nextID, Email: email, State: store.AccountActive,
		DisplayName: "Seeded", QuotaMB: 2048,
		CreatedAt: time.Unix(1700000000, 0).UTC(),
		UpdatedAt: time.Unix(1700000001, 0).UTC(),
	}
	s.nextID++
	s.accounts[email] = a
	return a
}

func (s *apiStore) GetAccountByEmail(_ context.Context, email string) (store.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[email]
	if !ok {
		return store.Account{}, store.ErrNotFound
	}
	return a, nil
}

func (s *apiStore) DeleteAccount(context.Context, int64) error { return nil }

func (s *apiStore) AccountSyncSummary(context.Context, int64) (store.AccountSyncSummary, error) {
	return store.AccountSyncSummary{Messages: 3, EverSynced: true}, nil
}

func (s *apiStore) mutate(id int64, fn func(*store.Account)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for email, a := range s.accounts {
		if a.ID == id {
			fn(&a)
			s.accounts[email] = a
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *apiStore) SetAccountFacts(_ context.Context, id int64, f store.AccountFacts) error {
	return s.mutate(id, func(a *store.Account) {
		a.DisplayName, a.QuotaMB = f.DisplayName, f.QuotaMB
		a.SendPerDay, a.RecipientsPerMessage, a.AttachmentMB = f.SendPerDay, f.RecipientsPerMessage, f.AttachmentMB
	})
}

func (s *apiStore) SetAccountAppPasswordID(_ context.Context, id int64, appID int64) error {
	return s.mutate(id, func(a *store.Account) { a.MailcowAppPasswordID = &appID })
}

func (s *apiStore) SetAccountCredentials(context.Context, int64, string, []byte) error { return nil }

func (s *apiStore) SetAccountSuspended(_ context.Context, id int64, suspended bool, at time.Time) error {
	return s.mutate(id, func(a *store.Account) {
		a.Suspended = suspended
		if suspended {
			t := at
			a.SuspendedAt = &t
			return
		}
		a.SuspendedAt = nil
	})
}

func (s *apiStore) SetAccountReadOnly(_ context.Context, id int64, at time.Time) error {
	return s.mutate(id, func(a *store.Account) {
		a.ReadOnly = true
		t := at
		a.ReadOnlySince = &t
	})
}

func (s *apiStore) MarkAccountDeleting(_ context.Context, id int64, at time.Time) error {
	return s.mutate(id, func(a *store.Account) {
		t := at
		a.DeletingSince = &t
	})
}

func (s *apiStore) ListDeletingAccounts(context.Context) ([]store.Account, error) { return nil, nil }

func (s *apiStore) AppendAudit(_ context.Context, l store.AuditLine) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, l)
	return nil
}

func (s *apiStore) auditLines() []store.AuditLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.AuditLine(nil), s.audit...)
}

func (s *apiStore) HasAuditFor(context.Context, string, string) (bool, error) { return false, nil }

func (s *apiStore) CreateExport(_ context.Context, id string, accountID int64, address string) (store.Export, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	acct := accountID
	e := store.Export{
		ID: id, AccountID: &acct, Address: address,
		Status: store.ExportPending, RequestedAt: time.Unix(1700000100, 0).UTC(),
	}
	s.exports[id] = e
	s.latest[address] = id
	return e, nil
}

func (s *apiStore) GetExport(_ context.Context, id string) (store.Export, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.exports[id]
	if !ok {
		return store.Export{}, store.ErrNotFound
	}
	return e, nil
}

func (s *apiStore) putExport(e store.Export) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exports[e.ID] = e
	s.latest[e.Address] = e.ID
}

func (s *apiStore) LatestExport(_ context.Context, address string) (store.Export, error) {
	s.mu.Lock()
	id, ok := s.latest[address]
	s.mu.Unlock()
	if !ok {
		return store.Export{}, store.ErrNotFound
	}
	return s.GetExport(context.Background(), id)
}

// The ExportStore half, for the runner.
func (s *apiStore) ClaimPendingExport(context.Context, time.Time) (store.Export, error) {
	return store.Export{}, store.ErrNotFound
}
func (s *apiStore) SetExportProgress(context.Context, string, int, int) error { return nil }
func (s *apiStore) CompleteExport(context.Context, string, store.ExportResult, time.Time) error {
	return nil
}
func (s *apiStore) FailExport(context.Context, string, string, time.Time) error { return nil }
func (s *apiStore) ListExpirableExports(context.Context, time.Time, int) ([]store.Export, error) {
	return nil, nil
}
func (s *apiStore) PurgeExport(context.Context, string, time.Time) error { return nil }
func (s *apiStore) CountPendingExports(context.Context) (int, error)     { return 0, nil }
func (s *apiStore) ForEachAccountMessage(context.Context, int64, func(store.ExportMessage) error) error {
	return nil
}

// apiMailcow answers enough for the resource to build.
type apiMailcow struct{}

func (m *apiMailcow) GetMailbox(_ context.Context, mailbox string) (mailcow.Mailbox, error) {
	return mailcow.Mailbox{
		Username: mailbox, Active: 1, Name: "Seeded",
		Quota: 2048 << 20, QuotaUsed: 4096, Messages: 3,
		RL: mailcow.RateLimit{Value: accounts.DefaultSendPerDay, Frame: "d"},
	}, nil
}
func (m *apiMailcow) CreateMailbox(context.Context, mailcow.CreateMailboxRequest) error { return nil }
func (m *apiMailcow) EditMailbox(context.Context, string, mailcow.MailboxEdit) error    { return nil }
func (m *apiMailcow) DeleteMailbox(context.Context, string) error                       { return nil }
func (m *apiMailcow) GetMailboxRateLimit(context.Context, string) (mailcow.RateLimit, error) {
	return mailcow.RateLimit{Value: accounts.DefaultSendPerDay, Frame: "d"}, nil
}
func (m *apiMailcow) SetMailboxRateLimit(context.Context, string, mailcow.RateLimit) error {
	return nil
}
func (m *apiMailcow) ListAppPasswords(context.Context, string) ([]mailcow.AppPassword, error) {
	return nil, nil
}
func (m *apiMailcow) CreateAppPassword(context.Context, mailcow.CreateAppPasswordRequest) (mailcow.AppPassword, error) {
	return mailcow.AppPassword{ID: 100}, nil
}
func (m *apiMailcow) DeleteAppPassword(context.Context, int64) error { return nil }

type apiProvisioner struct{ store *apiStore }

func (p *apiProvisioner) Provision(_ context.Context, req provision.Request) (provision.Result, error) {
	return provision.Result{Account: p.store.seed(req.Email), AppPasswordID: 100}, nil
}

func (p *apiProvisioner) Reissue(_ context.Context, email string, _ []mailcow.Protocol) (provision.Result, error) {
	a, err := p.store.GetAccountByEmail(context.Background(), email)
	return provision.Result{Account: a, AppPasswordID: 200}, err
}

type apiRevoker struct{}

func (apiRevoker) RevokeAccount(context.Context, int64) error { return nil }

// --- the harness -------------------------------------------------------------

type apiHarness struct {
	srv     *Server
	store   *apiStore
	keys    *fakeKeyStore
	runner  *accounts.ExportRunner
	writeSA string
	readSA  string
}

// newAccountsServer builds a server with the accounts API enabled.
func newAccountsServer(t *testing.T) *apiHarness {
	t.Helper()
	return newAccountsServerWith(t, true)
}

func newAccountsServerWith(t *testing.T, enabled bool) *apiHarness {
	t.Helper()
	st := newAPIStore()
	keys := newFakeKeyStore()

	const writeSecret = accounts.KeyPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAY"
	const readSecret = accounts.KeyPrefix + "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBY"
	keys.add(writeSecret, store.ServiceAccount{
		ID: testWriteSA, Domain: apiDomain, Name: "portal",
		Scopes: []string{store.ScopeAccountsWrite},
	})
	keys.add(readSecret, store.ServiceAccount{
		ID: testReadSA, Domain: apiDomain, Name: "reader",
		Scopes: []string{store.ScopeAccountsRead},
	})

	h := &apiHarness{store: st, keys: keys, writeSA: writeSecret, readSA: readSecret}

	var cfgAccounts *AccountsAPIConfig
	if enabled {
		runner, err := accounts.NewExportRunner(accounts.ExportConfig{
			Dir:        t.TempDir(),
			SigningKey: []byte("test-export-signing-key-0123456789"),
			Logger:     discardLogger(),
		}, st, nopBlobs{}, nil)
		if err != nil {
			t.Fatalf("NewExportRunner: %v", err)
		}
		h.runner = runner
		svc, err := accounts.New(accounts.Config{Logger: discardLogger()},
			&apiMailcow{}, st, &apiProvisioner{store: st}, apiRevoker{}, runner, nil)
		if err != nil {
			t.Fatalf("accounts.New: %v", err)
		}
		cfgAccounts = &AccountsAPIConfig{
			Service: svc,
			Auth:    accounts.NewAuthenticator(keys, nil),
			Exports: runner,
		}
	}

	srv, _, _, _ := newTestServer(t, func(c *Config) { c.Accounts = cfgAccounts })
	h.srv = srv
	return h
}

// nopBlobs satisfies accounts.BlobReader. The transport tests never produce
// a zip — RunOnce is not called — so a reader that refuses everything is the
// honest stand-in; the real production path is exercised in internal/accounts.
type nopBlobs struct{}

func (nopBlobs) Open(blob.Hash) (io.ReadCloser, error) {
	return nil, errors.New("no blobs in this test")
}

// do issues one request to the accounts API with the given bearer key.
func (h *apiHarness) do(method, path, body, key string, header map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Host = apiHost
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(w, r)
	return w
}

// genericNotFound is §2.1's body, byte for byte. Every "no" on this API must
// equal it exactly — which is what makes the API useless as an oracle.
const genericNotFound = `{"detail":"not found","status":404,"type":"about:blank"}`

func assertGenericNotFound(t *testing.T, w *httptest.ResponseRecorder, what string) {
	t.Helper()
	if w.Code != http.StatusNotFound {
		t.Errorf("%s: status = %d, want 404", what, w.Code)
		return
	}
	if got := strings.TrimSpace(w.Body.String()); got != genericNotFound {
		t.Errorf("%s: body = %s, want the generic %s", what, got, genericNotFound)
	}
	if ct := w.Header().Get("Content-Type"); ct != problemContentType {
		t.Errorf("%s: content-type = %q, want %q", what, ct, problemContentType)
	}
}

// --- §6 (a) on the wire: the no-oracle rule ----------------------------------

// TestM1a_EveryRefusalIsTheByteIdenticalGeneric404 is contract §2.1 as a
// property rather than a promise.
//
// Six different reasons to refuse — no header, a malformed key, a revoked
// one, an under-scoped one, a foreign domain, an unknown mailbox — must
// produce ONE answer, byte for byte. If any of them differed by a status, a
// body, or even a content type, a consumer could distinguish "not your
// domain" from "no such mailbox" and enumerate another installation's
// mailboxes one request at a time.
func TestM1a_EveryRefusalIsTheByteIdenticalGeneric404(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)
	h.store.seed(apiAddress)
	h.store.seed("victim@" + apiOther)
	h.keys.add(accounts.KeyPrefix+"CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCY", store.ServiceAccount{
		ID: "sa_revoked", Domain: apiDomain,
		Scopes:    []string{store.ScopeAccountsWrite},
		RevokedAt: ptrTime(time.Unix(1, 0)),
	})

	for _, tc := range []struct {
		name, key, path string
	}{
		{"no credential at all", "", PathAdminAccounts + "/" + apiAddress},
		{"a malformed key", "not-a-key", PathAdminAccounts + "/" + apiAddress},
		{"a key of the right shape that does not exist",
			accounts.KeyPrefix + "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZY",
			PathAdminAccounts + "/" + apiAddress},
		{"a revoked key",
			accounts.KeyPrefix + "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCY",
			PathAdminAccounts + "/" + apiAddress},
		{"a valid key naming ANOTHER domain's existing mailbox",
			"write", PathAdminAccounts + "/victim@" + apiOther},
		{"a valid key naming a mailbox that does not exist",
			"write", PathAdminAccounts + "/ghost@" + apiDomain},
		{"a valid key naming a syntactically impossible address",
			"write", PathAdminAccounts + "/..@" + apiDomain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := tc.key
			if key == "write" {
				key = h.writeSA
			}
			assertGenericNotFound(t, h.do(http.MethodGet, tc.path, "", key, nil), tc.name)
		})
	}
}

// TestM1a_AnUnderScopedKeyIsAlsoJustNotFound: a read key may not write, and
// learns nothing from trying — not a 403, which would confirm the resource.
func TestM1a_AnUnderScopedKeyIsAlsoJustNotFound(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)
	h.store.seed(apiAddress)

	// The read key CAN read...
	if w := h.do(http.MethodGet, PathAdminAccounts+"/"+apiAddress, "", h.readSA, nil); w.Code != http.StatusOK {
		t.Fatalf("the read key could not read: %d %s", w.Code, w.Body)
	}
	// ...and learns nothing by trying to write.
	assertGenericNotFound(t,
		h.do(http.MethodPost, PathAdminAccounts+"/"+apiAddress+"/suspend", "", h.readSA, nil),
		"a read key attempting a write")
}

// TestM1a_TheDisabledFeatureIsIndistinguishableFromAbsent is the other half
// of §2.1: with the accounts API off, every route answers the SAME generic
// 404 — never a 501, which would tell a prober the feature exists here.
func TestM1a_TheDisabledFeatureIsIndistinguishableFromAbsent(t *testing.T) {
	t.Parallel()
	off := newAccountsServerWith(t, false)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, PathAdminAccounts},
		{http.MethodGet, PathAdminAccounts + "/" + apiAddress},
		{http.MethodPatch, PathAdminAccounts + "/" + apiAddress},
		{http.MethodDelete, PathAdminAccounts + "/" + apiAddress},
		{http.MethodPost, PathAdminAccounts + "/" + apiAddress + "/suspend"},
		{http.MethodPost, PathAdminAccounts + "/" + apiAddress + "/resume"},
		{http.MethodPost, PathAdminAccounts + "/" + apiAddress + "/readonly"},
		{http.MethodPost, PathAdminAccounts + "/" + apiAddress + "/export"},
		{http.MethodGet, PathAdminAccounts + "/" + apiAddress + "/export"},
		{http.MethodGet, "/admin/exports/exp_anything?exp=1&sig=x"},
	} {
		w := off.do(tc.method, tc.path, `{}`, off.writeSA, nil)
		assertGenericNotFound(t, w, tc.method+" "+tc.path+" with the feature off")
	}
}

// TestM1a_AMailboxCredentialIsNotAServiceAccountKey is §4's promise in the
// other direction: "the accounts API ignores session tokens". Basic auth —
// the credential every other route on this server accepts — buys nothing
// here, and says so with the generic 404 rather than a 401 that would
// confirm the route.
func TestM1a_AMailboxCredentialIsNotAServiceAccountKey(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)
	h.store.seed(apiAddress)

	r := httptest.NewRequest(http.MethodGet, PathAdminAccounts+"/"+apiAddress, nil)
	r.Host = apiHost
	r.SetBasicAuth("user@example.com", testPassword)
	w := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(w, r)
	assertGenericNotFound(t, w, "a Basic mailbox credential at the accounts API")
}

// --- the authentication class ------------------------------------------------

// TestServiceRouteSetIsExactlyTheAccountsAPI is the third pinned route set,
// beside the public one and the token one.
//
// A service-account key manages a whole domain's mailboxes and can read no
// mail; a scoped token is one mailbox's short-lived capability; Basic is one
// mailbox's own credential. They are not interchangeable, and a route that
// joined this class — or left it — by a one-word edit would be a privilege
// change nobody reviewed. It fails here instead.
func TestServiceRouteSetIsExactlyTheAccountsAPI(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)

	want := map[string]string{
		PathAdminAccounts:        accounts.ScopeWrite,
		PathAdminAccountSuspend:  accounts.ScopeWrite,
		PathAdminAccountResume:   accounts.ScopeWrite,
		PathAdminAccountReadOnly: accounts.ScopeWrite,
	}
	// The two patterns carrying more than one method are checked by method.
	multi := map[string]map[string]string{
		PathAdminAccount: {
			http.MethodGet:    accounts.ScopeRead,
			http.MethodPatch:  accounts.ScopeWrite,
			http.MethodDelete: accounts.ScopeWrite,
		},
		PathAdminAccountExport: {
			http.MethodGet:  accounts.ScopeRead,
			http.MethodPost: accounts.ScopeWrite,
		},
	}

	seen := map[string]bool{}
	for _, rt := range h.srv.routes() {
		if rt.serviceScope == "" {
			continue
		}
		seen[rt.method+" "+rt.pattern] = true
		if rt.public {
			t.Errorf("route %s is both public and service-authenticated, which is incoherent", rt.pattern)
		}
		if rt.tokenScope != "" {
			t.Errorf("route %s accepts both a service key and a scoped token, which are incomparable grants", rt.pattern)
		}
		if byMethod, ok := multi[rt.pattern]; ok {
			if byMethod[rt.method] != rt.serviceScope {
				t.Errorf("%s %s scope = %q, want %q", rt.method, rt.pattern, rt.serviceScope, byMethod[rt.method])
			}
			continue
		}
		if want[rt.pattern] != rt.serviceScope {
			t.Errorf("%s scope = %q, want %q (or the route does not belong to this class)",
				rt.pattern, rt.serviceScope, want[rt.pattern])
		}
	}

	wantCount := len(want) + 3 + 2
	if len(seen) != wantCount {
		t.Errorf("service routes = %d, want exactly %d: %v", len(seen), wantCount, seen)
	}
	// The signed download is deliberately NOT in this class: it takes no key.
	for _, rt := range h.srv.routes() {
		if rt.pattern == PathAdminExportDownload && rt.serviceScope != "" {
			t.Error("the signed export download must not require a service key; its authority is its signature")
		}
	}
}

// --- §2.4 statuses on the wire -----------------------------------------------

// TestCreateAnswers201ThenIdempotent200 is §2.4's create row as a consumer
// sees it, plus §2.7's Location header.
func TestCreateAnswers201ThenIdempotent200(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)
	body := `{"address":"` + apiAddress + `","name":"Expo"}`

	first := h.do(http.MethodPost, PathAdminAccounts, body, h.writeSA, nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d %s, want 201", first.Code, first.Body)
	}
	if loc := first.Header().Get("Location"); loc != PathAdminAccounts+"/"+apiAddress {
		t.Errorf("Location = %q, want %q", loc, PathAdminAccounts+"/"+apiAddress)
	}

	second := h.do(http.MethodPost, PathAdminAccounts, body, h.writeSA, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("repeat create = %d %s, want 200", second.Code, second.Body)
	}
	// §2.4: "200 with the current resource" — equal bodies.
	if first.Body.String() != second.Body.String() {
		t.Errorf("the repeat returned a different body:\n 201: %s\n 200: %s", first.Body, second.Body)
	}
}

// TestTransitionStatusesMatchTheContract walks §2.4's status column.
func TestTransitionStatusesMatchTheContract(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)
	h.store.seed(apiAddress)
	base := PathAdminAccounts + "/" + apiAddress

	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"get", http.MethodGet, base, "", http.StatusOK},
		{"patch", http.MethodPatch, base, `{"name":"New"}`, http.StatusOK},
		{"suspend", http.MethodPost, base + "/suspend", `{"reason":"x"}`, http.StatusOK},
		{"resume", http.MethodPost, base + "/resume", "", http.StatusOK},
		{"readonly", http.MethodPost, base + "/readonly", "", http.StatusOK},
		{"export POST", http.MethodPost, base + "/export", "", http.StatusAccepted},
		{"export GET", http.MethodGet, base + "/export", "", http.StatusOK},
		{"delete", http.MethodDelete, base,
			`{"confirm":"` + apiAddress + `"}`, http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := h.do(tc.method, tc.path, tc.body, h.writeSA, nil)
			if w.Code != tc.want {
				t.Errorf("%s = %d %s, want %d", tc.name, w.Code, w.Body, tc.want)
			}
		})
	}

	// The account is now deleting: §2.4's 409 for anything else, and the
	// body names the state.
	w := h.do(http.MethodPost, base+"/suspend", "", h.writeSA, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("suspend while deleting = %d %s, want 409", w.Code, w.Body)
	}
	var conflict struct {
		Reason string `json:"reason"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &conflict); err != nil {
		t.Fatalf("decoding the 409: %v", err)
	}
	if conflict.State != string(accounts.StateDeleting) {
		t.Errorf("409 state = %q, want deleting", conflict.State)
	}
}

// TestDeleteRequiresTheAddressRepeatedInTheBody is §2.5's confirmation.
func TestDeleteRequiresTheAddressRepeatedInTheBody(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)
	h.store.seed(apiAddress)
	base := PathAdminAccounts + "/" + apiAddress

	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"no body at all", "", http.StatusUnsupportedMediaType},
		{"an empty object", `{}`, http.StatusBadRequest},
		{"another address", `{"confirm":"other@` + apiDomain + `"}`, http.StatusBadRequest},
		{"the right address", `{"confirm":"` + apiAddress + `"}`, http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := h.do(http.MethodDelete, base, tc.body, h.writeSA, nil)
			if w.Code != tc.want {
				t.Fatalf("%s = %d %s, want %d", tc.name, w.Code, w.Body, tc.want)
			}
			if tc.want != http.StatusBadRequest {
				return
			}
			var fe fieldErrorBody
			if err := json.Unmarshal(w.Body.Bytes(), &fe); err != nil {
				t.Fatalf("decoding the 400: %v", err)
			}
			if fe.Field != "confirm" {
				t.Errorf("field = %q, want confirm", fe.Field)
			}
		})
	}
}

// --- §2.2: the error vocabulary ---------------------------------------------

// TestFieldErrorsNameTheFirstOffender is §2.2's 400 shape, including the rule
// that an unknown field is an error rather than a silent no-op — so a
// consumer's typo is caught at the boundary instead of being ignored.
func TestFieldErrorsNameTheFirstOffender(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)

	for _, tc := range []struct {
		name, body, wantField string
	}{
		{"a bad slug", `{"address":"has space@` + apiDomain + `","name":"x"}`, "address"},
		{"an empty name", `{"address":"a@` + apiDomain + `","name":""}`, "name"},
		{"a quota under the floor", `{"address":"a@` + apiDomain + `","name":"x","quotaMB":1}`, "quotaMB"},
		{"a limit out of range",
			`{"address":"a@` + apiDomain + `","name":"x","limits":{"sendPerDay":99999}}`, "limits.sendPerDay"},
		{"an unknown field", `{"address":"a@` + apiDomain + `","name":"x","quota":2048}`, "quota"},
		{"not an object at all", `["nope"]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := h.do(http.MethodPost, PathAdminAccounts, tc.body, h.writeSA, nil)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d %s, want 400", tc.name, w.Code, w.Body)
			}
			var fe fieldErrorBody
			if err := json.Unmarshal(w.Body.Bytes(), &fe); err != nil {
				t.Fatalf("decoding the 400: %v", err)
			}
			if fe.Field != tc.wantField {
				t.Errorf("field = %q, want %q (body was %s)", fe.Field, tc.wantField, w.Body)
			}
			if fe.Reason == "" {
				t.Error("the 400 carries no reason")
			}
		})
	}
}

// TestBodyLimitsOfSection22 covers the 413 and 415 rows.
func TestBodyLimitsOfSection22(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)

	t.Run("415 for a non-JSON content type", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, PathAdminAccounts, strings.NewReader(`{}`))
		r.Host = apiHost
		r.Header.Set("Authorization", "Bearer "+h.writeSA)
		r.Header.Set("Content-Type", "text/plain")
		w := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("status = %d %s, want 415", w.Code, w.Body)
		}
	})

	t.Run("413 over 16 KiB", func(t *testing.T) {
		big := `{"address":"a@` + apiDomain + `","name":"` + strings.Repeat("x", 17<<10) + `"}`
		w := h.do(http.MethodPost, PathAdminAccounts, big, h.writeSA, nil)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", w.Code)
		}
	})
}

// TestRequestIDIsEchoedOrMintedAndReachesTheAudit is §2.2's correlation rule,
// and it is checked on BOTH sides: a consumer chasing a request needs the id
// in the response AND in Moov's audit row, or the two logs cannot be joined.
func TestRequestIDIsEchoedOrMintedAndReachesTheAudit(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)
	h.store.seed(apiAddress)

	t.Run("a well-formed id is echoed and audited", func(t *testing.T) {
		const id = "portal-evt-8812-suspend"
		w := h.do(http.MethodPost, PathAdminAccounts+"/"+apiAddress+"/suspend", "", h.writeSA,
			map[string]string{headerRequestID: id})
		if w.Code != http.StatusOK {
			t.Fatalf("suspend = %d %s", w.Code, w.Body)
		}
		if got := w.Header().Get(headerRequestID); got != id {
			t.Errorf("echoed id = %q, want %q", got, id)
		}
		var found bool
		for _, l := range h.store.auditLines() {
			if l.RequestID == id {
				found = true
			}
		}
		if !found {
			t.Errorf("the request id never reached the audit: %+v", h.store.auditLines())
		}
	})

	t.Run("a missing id is minted", func(t *testing.T) {
		w := h.do(http.MethodGet, PathAdminAccounts+"/"+apiAddress, "", h.writeSA, nil)
		if got := w.Header().Get(headerRequestID); got == "" {
			t.Error("no request id was minted")
		}
	})

	t.Run("a malformed id is replaced, not refused", func(t *testing.T) {
		w := h.do(http.MethodGet, PathAdminAccounts+"/"+apiAddress, "", h.writeSA,
			map[string]string{headerRequestID: "has spaces and \"quotes\""})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 — a stray character in a log field must not fail a request", w.Code)
		}
		got := w.Header().Get(headerRequestID)
		if strings.ContainsAny(got, " \"") {
			t.Errorf("echoed id = %q, want a minted replacement", got)
		}
	})

	t.Run("even a refusal carries one", func(t *testing.T) {
		w := h.do(http.MethodGet, PathAdminAccounts+"/ghost@"+apiDomain, "", h.writeSA, nil)
		if w.Header().Get(headerRequestID) == "" {
			t.Error("a 404 carried no request id; a consumer chasing it has nothing to search for")
		}
	})
}

// TestRateLimitIs120PerMinuteBurst30 is §2.2's budget, per KEY.
//
// The budget belongs to the CREDENTIAL, not to the source address: one
// consumer behind a NAT must not be able to spend another's. The second half
// of the test is what proves that.
func TestRateLimitIs120PerMinuteBurst30(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)
	h.store.seed(apiAddress)
	path := PathAdminAccounts + "/" + apiAddress

	var limited *httptest.ResponseRecorder
	for i := 0; i < accountsBurst+5; i++ {
		w := h.do(http.MethodGet, path, "", h.writeSA, nil)
		if w.Code == http.StatusTooManyRequests {
			limited = w
			break
		}
	}
	if limited == nil {
		t.Fatalf("the budget never ran out over %d requests", accountsBurst+5)
	}
	if limited.Header().Get("Retry-After") == "" {
		t.Error("the 429 carries no Retry-After")
	}

	// The OTHER key still has its own budget.
	if w := h.do(http.MethodGet, path, "", h.readSA, nil); w.Code != http.StatusOK {
		t.Errorf("a second key was refused at %d; the budget is per key, not per server", w.Code)
	}
}

// --- §2.3: the resource shape ------------------------------------------------

// TestAccountResourceShapeMatchesSection23 pins the wire shape against the
// published contract: the field names, the nesting, and — the part a Go
// struct makes easy to get wrong — that the nullable timestamps are present
// as null rather than absent.
func TestAccountResourceShapeMatchesSection23(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)
	h.store.seed(apiAddress)

	w := h.do(http.MethodGet, PathAdminAccounts+"/"+apiAddress, "", h.writeSA, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", w.Code, w.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	for _, k := range []string{
		"address", "domain", "name", "state", "readOnly", "suspended",
		"quota", "limits", "sync", "createdAt", "updatedAt",
		"lastAccessAt", "readOnlySince", "suspendedAt", "deletingSince",
	} {
		if _, ok := raw[k]; !ok {
			t.Errorf("the resource is missing %q", k)
		}
	}
	// The four nullable timestamps must be present AND null on a plain
	// active account — a portal reading readOnlySince gets the key either
	// way, so its parser meets one shape.
	for _, k := range []string{"lastAccessAt", "readOnlySince", "suspendedAt", "deletingSince"} {
		if string(raw[k]) != "null" {
			t.Errorf("%s = %s on an active account, want null", k, raw[k])
		}
	}

	var res struct {
		State  string `json:"state"`
		Domain string `json:"domain"`
		Quota  struct {
			LimitMB   int   `json:"limitMB"`
			UsedBytes int64 `json:"usedBytes"`
		} `json:"quota"`
		Limits struct {
			SendPerDay int `json:"sendPerDay"`
		} `json:"limits"`
		Sync struct {
			State string `json:"state"`
		} `json:"sync"`
		CreatedAt string `json:"createdAt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decoding typed: %v", err)
	}
	switch {
	case res.State != string(accounts.StateActive):
		t.Errorf("state = %q, want active", res.State)
	case res.Domain != apiDomain:
		t.Errorf("domain = %q, want %q", res.Domain, apiDomain)
	case res.Quota.UsedBytes != 4096:
		t.Errorf("quota.usedBytes = %d, want Mailcow's 4096 (§2.3: the portal shows quota)", res.Quota.UsedBytes)
	case res.Limits.SendPerDay != accounts.DefaultSendPerDay:
		t.Errorf("limits.sendPerDay = %d, want the effective default %d",
			res.Limits.SendPerDay, accounts.DefaultSendPerDay)
	case res.Sync.State != string(accounts.SyncReady):
		t.Errorf("sync.state = %q, want ready", res.Sync.State)
	}
	// §2.3: RFC 3339 UTC with milliseconds, one spelling everywhere.
	if _, err := time.Parse(rfc3339Milli, res.CreatedAt); err != nil {
		t.Errorf("createdAt = %q, which is not the contract's format: %v", res.CreatedAt, err)
	}
}

// --- §2.6 / §6 (h): the export download capability ---------------------------

// TestExportStatusNoneIsA200 pins §2.6's deliberate choice: "no export was
// ever requested" is a 200 with status "none", so 404 keeps its single
// meaning on this API.
func TestExportStatusNoneIsA200(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)
	h.store.seed(apiAddress)

	w := h.do(http.MethodGet, PathAdminAccounts+"/"+apiAddress+"/export", "", h.writeSA, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET export = %d %s, want 200", w.Code, w.Body)
	}
	var e exportBody
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if e.Status != string(accounts.ExportNone) {
		t.Errorf("status = %q, want none", e.Status)
	}
	if e.Download != nil || e.Manifest != nil || e.Progress != nil {
		t.Error(`a "none" export advertised a download, manifest or progress`)
	}
}

// TestM1h_ExportDownloadRefusesEverythingButAValidSignature is criterion (h)'s
// second half: the download URL is unusable after `exp`, after tampering, and
// after a purge (410).
//
// Every refusal but the purge is the generic 404, so a signature probe learns
// nothing — not even whether the export id exists. The purge is the one
// documented exception, and it is safe: only a holder of a VALID signature
// ever sees it, and they already knew the export existed.
func TestM1h_ExportDownloadRefusesEverythingButAValidSignature(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)
	a := h.store.seed(apiAddress)

	const id = "exp_ABCDEFGHIJKLMNOPQRST"
	origin := "http://" + apiHost
	completed := time.Unix(1700000200, 0).UTC()
	acct := a.ID
	h.store.putExport(store.Export{
		ID: id, AccountID: &acct, Address: apiAddress,
		Status: store.ExportReady, RequestedAt: time.Unix(1700000100, 0).UTC(),
		CompletedAt: &completed, Path: "", Bytes: 1024,
		SHA256: strings.Repeat("a", 64), Messages: 3, Mailboxes: 1,
	})

	valid := h.runner.SignedURL(origin, id, time.Now().Add(24*time.Hour))
	query := valid[strings.Index(valid, "?"):]

	t.Run("a tampered signature is the generic 404", func(t *testing.T) {
		bad := strings.Replace(query, "sig=", "sig=x", 1)
		assertGenericNotFound(t, h.do(http.MethodGet, "/admin/exports/"+id+bad, "", "", nil),
			"a tampered signature")
	})

	t.Run("a signature for ANOTHER host does not verify here", func(t *testing.T) {
		// The origin is part of the signed message, so a URL minted for one
		// brand's host cannot be replayed against another's — which matters
		// precisely because one binary serves several trust domains.
		foreign := h.runner.SignedURL("http://elsewhere.example.test", id, time.Now().Add(time.Hour))
		fq := foreign[strings.Index(foreign, "?"):]
		assertGenericNotFound(t, h.do(http.MethodGet, "/admin/exports/"+id+fq, "", "", nil),
			"a signature minted for another host")
	})

	t.Run("an expired signature is the generic 404", func(t *testing.T) {
		expired := h.runner.SignedURL(origin, id, time.Now().Add(-time.Minute))
		eq := expired[strings.Index(expired, "?"):]
		assertGenericNotFound(t, h.do(http.MethodGet, "/admin/exports/"+id+eq, "", "", nil),
			"an expired signature")
	})

	t.Run("an unknown export id is the generic 404", func(t *testing.T) {
		other := h.runner.SignedURL(origin, "exp_ZZZZZZZZZZZZZZZZZZZZ", time.Now().Add(time.Hour))
		oq := other[strings.Index(other, "?"):]
		assertGenericNotFound(t,
			h.do(http.MethodGet, "/admin/exports/exp_ZZZZZZZZZZZZZZZZZZZZ"+oq, "", "", nil),
			"a validly signed unknown id")
	})

	t.Run("a purged export is the documented 410", func(t *testing.T) {
		purged := time.Unix(1700009999, 0).UTC()
		h.store.putExport(store.Export{
			ID: id, AccountID: &acct, Address: apiAddress,
			Status: store.ExportReady, RequestedAt: time.Unix(1700000100, 0).UTC(),
			PurgedAt: &purged,
		})
		w := h.do(http.MethodGet, "/admin/exports/"+id+query, "", "", nil)
		if w.Code != http.StatusGone {
			t.Fatalf("a purged export = %d %s, want 410", w.Code, w.Body)
		}
	})
}

// TestExportDownloadNeedsNoAuthorizationHeader is the property that makes the
// capability useful: the URL is handed to a browser TAB, which attaches no
// header. A route that also demanded a key would be a link nobody can open.
func TestExportDownloadNeedsNoAuthorizationHeader(t *testing.T) {
	t.Parallel()
	h := newAccountsServer(t)

	// An unsigned request is refused — the route is public, not open.
	assertGenericNotFound(t,
		h.do(http.MethodGet, "/admin/exports/exp_ABCDEFGHIJKLMNOPQRST", "", "", nil),
		"an unsigned download")

	// And a request WITHOUT any credential reaches the handler at all, rather
	// than being turned away by the mailbox authenticator: the proof is that
	// it gets the accounts API's generic 404 and not a 401.
	w := h.do(http.MethodGet, "/admin/exports/exp_ABCDEFGHIJKLMNOPQRST?exp=1&sig=x", "", "", nil)
	if w.Code == http.StatusUnauthorized {
		t.Fatal("the signed download sits behind mailbox authentication; a browser tab could never open it")
	}
}

// --- the upstream summary ----------------------------------------------------

// TestUpstreamBodyNeverReachesTheWire is §2.2's "never the raw upstream body,
// never a credential" — checked where it matters, on the response.
func TestUpstreamBodyNeverReachesTheWire(t *testing.T) {
	t.Parallel()
	const secret = "MAILCOW-KEY-DEADBEEF"

	h := newAccountsServer(t)
	h.store.seed(apiAddress)
	// Swap in a Mailcow whose every failure quotes a credential.
	svc, err := accounts.New(accounts.Config{Logger: discardLogger()},
		leakyMailcow{secret: secret}, h.store, &apiProvisioner{store: h.store}, apiRevoker{}, nil, nil)
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	h.srv.accountsAPI.svc = svc

	w := h.do(http.MethodPatch, PathAdminAccounts+"/"+apiAddress, `{"name":"New"}`, h.writeSA, nil)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d %s, want 502", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatalf("the response quotes the upstream body, credential and all: %s", w.Body)
	}
	var rb reasonBody
	if err := json.Unmarshal(w.Body.Bytes(), &rb); err != nil {
		t.Fatalf("decoding the 502: %v", err)
	}
	if rb.Upstream != "mailcow" {
		t.Errorf("upstream = %q, want mailcow", rb.Upstream)
	}
	if rb.Reason == "" {
		t.Error("the 502 carries no summary of its own")
	}
}

// leakyMailcow fails every write with an error quoting a credential, which is
// exactly the shape §2.2 exists to stop from reaching a consumer.
type leakyMailcow struct{ secret string }

func (l leakyMailcow) err() error {
	return fmt.Errorf("%w: upstream said key=%s", mailcow.ErrAPI, l.secret)
}

func (l leakyMailcow) GetMailbox(_ context.Context, mailbox string) (mailcow.Mailbox, error) {
	return mailcow.Mailbox{Username: mailbox, Active: 1}, nil
}
func (l leakyMailcow) CreateMailbox(context.Context, mailcow.CreateMailboxRequest) error {
	return l.err()
}
func (l leakyMailcow) EditMailbox(context.Context, string, mailcow.MailboxEdit) error { return l.err() }
func (l leakyMailcow) DeleteMailbox(context.Context, string) error                    { return l.err() }
func (l leakyMailcow) GetMailboxRateLimit(context.Context, string) (mailcow.RateLimit, error) {
	return mailcow.RateLimit{}, l.err()
}
func (l leakyMailcow) SetMailboxRateLimit(context.Context, string, mailcow.RateLimit) error {
	return l.err()
}
func (l leakyMailcow) ListAppPasswords(context.Context, string) ([]mailcow.AppPassword, error) {
	return nil, l.err()
}
func (l leakyMailcow) CreateAppPassword(context.Context, mailcow.CreateAppPasswordRequest) (mailcow.AppPassword, error) {
	return mailcow.AppPassword{}, l.err()
}
func (l leakyMailcow) DeleteAppPassword(context.Context, int64) error { return l.err() }

func ptrTime(t time.Time) *time.Time { return &t }

// --- §6 (e), the JMAP half ---------------------------------------------------

// TestM1e_TheSessionCarriesReadOnlyInBothPlaces is the session half of
// criterion (e).
//
// Both sources are checked because the PWA reads both and treats either as
// yes (web/src/mail/prefs.ts): RFC 8620's own isReadOnly, which means exactly
// "the entire account is read-only", and the vendor entry the product acts
// on. They come from ONE field on the account row, so this test is really
// asking whether that single source reached both renderings.
func TestM1e_TheSessionCarriesReadOnlyInBothPlaces(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		readOnly bool
	}{
		{"an ordinary account", false},
		{"an account in read-only retention", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, dir, _ := newTestServer(t, func(c *Config) { c.Prefs = true })
			a := testAccount()
			a.ReadOnly = tc.readOnly
			dir.put(a)

			w := doReq(s, http.MethodGet, PathWellKnown, "", true, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("session = %d %s", w.Code, w.Body)
			}
			var session struct {
				Accounts map[string]struct {
					IsReadOnly          bool                       `json:"isReadOnly"`
					AccountCapabilities map[string]json.RawMessage `json:"accountCapabilities"`
				} `json:"accounts"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
				t.Fatalf("decoding the session: %v", err)
			}
			if len(session.Accounts) != 1 {
				t.Fatalf("session carries %d accounts, want 1", len(session.Accounts))
			}
			for _, acct := range session.Accounts {
				if acct.IsReadOnly != tc.readOnly {
					t.Errorf("isReadOnly = %t, want %t", acct.IsReadOnly, tc.readOnly)
				}
				raw, ok := acct.AccountCapabilities["https://moov.email/ns/prefs"]
				if !ok {
					t.Fatal("the prefs account capability is missing")
				}
				var prefs struct {
					ReadOnly bool `json:"readOnly"`
				}
				if err := json.Unmarshal(raw, &prefs); err != nil {
					t.Fatalf("decoding the prefs capability: %v", err)
				}
				if prefs.ReadOnly != tc.readOnly {
					t.Errorf("prefs.readOnly = %t, want %t", prefs.ReadOnly, tc.readOnly)
				}
			}
		})
	}
}

// TestM1e_ReadOnlyTravelsOnTheCaller pins the plumbing the refusal depends
// on: the flag reaches jmap.Caller, which is what EmailSubmission/set reads.
//
// Without this the refusal in internal/jmap/mail would be unreachable in
// production while its own unit test passed — the exact shape of the
// event.code bug the E12 gate caught.
func TestM1e_ReadOnlyTravelsOnTheCaller(t *testing.T) {
	t.Parallel()
	s, _, dir, _ := newTestServer(t, nil)
	a := testAccount()
	a.ReadOnly = true
	dir.put(a)

	var seen jmap.Caller
	var got bool
	s.Registry().Register("Test/peek", jmap.CapCore,
		func(ctx context.Context, _ json.RawMessage) (any, *jmap.MethodError) {
			seen, got = jmap.CallerFromContext(ctx)
			return map[string]any{}, nil
		})

	w := doReq(s, http.MethodPost, PathAPI, apiBody(`["Test/peek",{},"c0"]`), true, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("api = %d %s", w.Code, w.Body)
	}
	if !got {
		t.Fatal("no caller reached the method handler")
	}
	if !seen.ReadOnly {
		t.Error("the read-only flag did not reach jmap.Caller; EmailSubmission/set would never refuse in production")
	}
}
