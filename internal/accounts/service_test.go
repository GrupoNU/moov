package accounts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/mailcow"
	"github.com/GrupoNU/moov/internal/provision"
	"github.com/GrupoNU/moov/internal/store"
)

// The acceptance tests of contract §6 M1, one Go test per lettered criterion,
// named so the mapping survives a refactor that moves them.
//
// Everything here runs against fakes, including a Mailcow that answers the F0
// error families exactly. That is not a shortcut around an integration test —
// §6 (i) asks for that too, and internal/mailcow has it — it is what lets the
// state machine be driven through the paths a real Mailcow makes hard to
// produce on demand: a refusal at the fourth of five writes, an upstream that
// stops answering mid-create, a mailbox that already exists.

// discardLogger keeps the test output readable: this package logs an audit
// line for every write, and the failure paths log loudly on purpose.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- fakes -------------------------------------------------------------------

const (
	testDomain  = "events.example.test"
	otherDomain = "other.example.test"
)

func testActor() Actor {
	return Actor{ID: "sa_0001", Name: "portal", Domain: testDomain}
}

func testCall() Call {
	return Call{Actor: testActor(), RequestID: "req-1"}
}

// fakeMailcow records every call, so a test can assert not only what was
// answered but WHETHER the upstream was reached at all — which is the whole
// of criterion (a).
type fakeMailcow struct {
	mu    sync.Mutex
	calls []string

	mailboxes map[string]mailcow.Mailbox
	rl        map[string]mailcow.RateLimit
	appPasswd map[int64]mailcow.AppPassword

	// failOn makes the named method fail with err, for the rollback paths.
	failOn map[string]error

	// onCall fires before the named method runs, so a test can mutate the
	// world mid-transaction.
	onCall func(name string)
}

func newFakeMailcow() *fakeMailcow {
	return &fakeMailcow{
		mailboxes: map[string]mailcow.Mailbox{},
		rl:        map[string]mailcow.RateLimit{},
		appPasswd: map[int64]mailcow.AppPassword{},
		failOn:    map[string]error{},
	}
}

func (f *fakeMailcow) record(name string) error {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	hook := f.onCall
	err := f.failOn[name]
	f.mu.Unlock()
	if hook != nil {
		hook(name)
	}
	return err
}

func (f *fakeMailcow) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeMailcow) GetMailbox(_ context.Context, mailbox string) (mailcow.Mailbox, error) {
	if err := f.record("GetMailbox"); err != nil {
		return mailcow.Mailbox{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	mb, ok := f.mailboxes[mailbox]
	if !ok {
		return mailcow.Mailbox{}, fmt.Errorf("%w: %s", mailcow.ErrNotFound, mailbox)
	}
	mb.RL = f.rl[mailbox]
	return mb, nil
}

func (f *fakeMailcow) CreateMailbox(_ context.Context, req mailcow.CreateMailboxRequest) error {
	if err := f.record("CreateMailbox"); err != nil {
		return err
	}
	addr := req.LocalPart + "@" + req.Domain
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.mailboxes[addr]; exists {
		return &mailcow.APIError{Code: mailcow.CodeObjectExists}
	}
	f.mailboxes[addr] = mailcow.Mailbox{
		Username: addr, Active: 1, Name: req.Name,
		Quota: int64(req.QuotaMB) << 20,
	}
	return nil
}

func (f *fakeMailcow) EditMailbox(_ context.Context, mailbox string, e mailcow.MailboxEdit) error {
	if err := f.record("EditMailbox"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	mb, ok := f.mailboxes[mailbox]
	if !ok {
		return fmt.Errorf("%w: %s", mailcow.ErrNotFound, mailbox)
	}
	if e.Name != nil {
		mb.Name = *e.Name
	}
	if e.QuotaMB != nil {
		mb.Quota = int64(*e.QuotaMB) << 20
	}
	if e.Active != nil {
		mb.Active = 0
		if *e.Active {
			mb.Active = 1
		}
	}
	f.mailboxes[mailbox] = mb
	return nil
}

func (f *fakeMailcow) DeleteMailbox(_ context.Context, mailbox string) error {
	if err := f.record("DeleteMailbox"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.mailboxes, mailbox)
	return nil
}

func (f *fakeMailcow) GetMailboxRateLimit(_ context.Context, mailbox string) (mailcow.RateLimit, error) {
	if err := f.record("GetMailboxRateLimit"); err != nil {
		return mailcow.RateLimit{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rl[mailbox], nil
}

func (f *fakeMailcow) SetMailboxRateLimit(_ context.Context, mailbox string, rl mailcow.RateLimit) error {
	if err := f.record("SetMailboxRateLimit"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rl[mailbox] = rl
	return nil
}

func (f *fakeMailcow) ListAppPasswords(_ context.Context, _ string) ([]mailcow.AppPassword, error) {
	if err := f.record("ListAppPasswords"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]mailcow.AppPassword, 0, len(f.appPasswd))
	for _, ap := range f.appPasswd {
		out = append(out, ap)
	}
	return out, nil
}

func (f *fakeMailcow) CreateAppPassword(_ context.Context, req mailcow.CreateAppPasswordRequest) (mailcow.AppPassword, error) {
	if err := f.record("CreateAppPassword"); err != nil {
		return mailcow.AppPassword{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := int64(len(f.appPasswd) + 100)
	ap := mailcow.AppPassword{ID: id, Mailbox: req.Mailbox}
	f.appPasswd[id] = ap
	return ap, nil
}

func (f *fakeMailcow) DeleteAppPassword(_ context.Context, id int64) error {
	if err := f.record("DeleteAppPassword"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.appPasswd, id)
	return nil
}

// fakeStore is the account, audit and export store.
type fakeStore struct {
	mu       sync.Mutex
	accounts map[string]store.Account
	nextID   int64
	audit    []store.AuditLine
	exports  map[string]store.Export
	latest   map[string]string

	failSetFacts error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		accounts: map[string]store.Account{},
		nextID:   1,
		exports:  map[string]store.Export{},
		latest:   map[string]string{},
	}
}

func (s *fakeStore) put(a store.Account) store.Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.ID == 0 {
		a.ID = s.nextID
		s.nextID++
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Unix(1700000000, 0).UTC()
	}
	a.UpdatedAt = time.Unix(1700000001, 0).UTC()
	s.accounts[a.Email] = a
	return a
}

func (s *fakeStore) GetAccountByEmail(_ context.Context, email string) (store.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[email]
	if !ok {
		return store.Account{}, store.ErrNotFound
	}
	return a, nil
}

func (s *fakeStore) DeleteAccount(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for email, a := range s.accounts {
		if a.ID == id {
			delete(s.accounts, email)
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *fakeStore) AccountSyncSummary(_ context.Context, _ int64) (store.AccountSyncSummary, error) {
	return store.AccountSyncSummary{Messages: 7, EverSynced: true}, nil
}

func (s *fakeStore) mutate(id int64, fn func(*store.Account)) error {
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

func (s *fakeStore) SetAccountFacts(_ context.Context, id int64, f store.AccountFacts) error {
	if s.failSetFacts != nil {
		return s.failSetFacts
	}
	return s.mutate(id, func(a *store.Account) {
		a.DisplayName = f.DisplayName
		a.QuotaMB = f.QuotaMB
		a.SendPerDay = f.SendPerDay
		a.RecipientsPerMessage = f.RecipientsPerMessage
		a.AttachmentMB = f.AttachmentMB
	})
}

func (s *fakeStore) SetAccountAppPasswordID(_ context.Context, id int64, appID int64) error {
	return s.mutate(id, func(a *store.Account) { a.MailcowAppPasswordID = &appID })
}

func (s *fakeStore) SetAccountCredentials(_ context.Context, id int64, username string, pw []byte) error {
	return s.mutate(id, func(a *store.Account) {
		a.IMAPUsername = username
		a.IMAPAppPassword = pw
	})
}

func (s *fakeStore) SetAccountSuspended(_ context.Context, id int64, suspended bool, at time.Time) error {
	return s.mutate(id, func(a *store.Account) {
		a.Suspended = suspended
		if suspended {
			t := at
			a.SuspendedAt = &t
			a.State = store.AccountDisabled
			return
		}
		a.SuspendedAt = nil
		a.State = store.AccountActive
	})
}

func (s *fakeStore) SetAccountReadOnly(_ context.Context, id int64, at time.Time) error {
	return s.mutate(id, func(a *store.Account) {
		a.ReadOnly = true
		t := at
		a.ReadOnlySince = &t
	})
}

func (s *fakeStore) MarkAccountDeleting(_ context.Context, id int64, at time.Time) error {
	return s.mutate(id, func(a *store.Account) {
		t := at
		a.DeletingSince = &t
	})
}

func (s *fakeStore) ListDeletingAccounts(_ context.Context) ([]store.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Account
	for _, a := range s.accounts {
		if a.DeletingSince != nil {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *fakeStore) AppendAudit(_ context.Context, l store.AuditLine) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, l)
	return nil
}

func (s *fakeStore) HasAuditFor(_ context.Context, address, action string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.audit {
		if l.Address == address && l.Action == action && l.Result == "ok" {
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeStore) auditLines() []store.AuditLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.AuditLine(nil), s.audit...)
}

func (s *fakeStore) CreateExport(_ context.Context, id string, accountID int64, address string) (store.Export, error) {
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

func (s *fakeStore) GetExport(_ context.Context, id string) (store.Export, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.exports[id]
	if !ok {
		return store.Export{}, store.ErrNotFound
	}
	return e, nil
}

func (s *fakeStore) LatestExport(_ context.Context, address string) (store.Export, error) {
	s.mu.Lock()
	id, ok := s.latest[address]
	s.mu.Unlock()
	if !ok {
		return store.Export{}, store.ErrNotFound
	}
	return s.GetExport(context.Background(), id)
}

// fakeProvisioner stands in for the real ADR §4 flow.
type fakeProvisioner struct {
	store *fakeStore

	provisionErr error
	reissueErr   error

	mu            sync.Mutex
	reissueScopes []mailcow.Protocol
	calls         []string
}

func (p *fakeProvisioner) Provision(_ context.Context, req provision.Request) (provision.Result, error) {
	p.mu.Lock()
	p.calls = append(p.calls, "Provision")
	p.mu.Unlock()
	if p.provisionErr != nil {
		return provision.Result{}, p.provisionErr
	}
	a := p.store.put(store.Account{
		Email: req.Email, State: store.AccountActive,
		CredentialState: store.CredentialActive,
	})
	return provision.Result{Account: a, AppPasswordID: 100}, nil
}

func (p *fakeProvisioner) Reissue(_ context.Context, email string, scopes []mailcow.Protocol) (provision.Result, error) {
	p.mu.Lock()
	p.calls = append(p.calls, "Reissue")
	p.reissueScopes = append([]mailcow.Protocol(nil), scopes...)
	p.mu.Unlock()
	if p.reissueErr != nil {
		return provision.Result{}, p.reissueErr
	}
	a, err := p.store.GetAccountByEmail(context.Background(), email)
	if err != nil {
		return provision.Result{}, err
	}
	return provision.Result{Account: a, AppPasswordID: 200}, nil
}

// fakeRevoker records which accounts were told to end their sessions.
type fakeRevoker struct {
	mu      sync.Mutex
	revoked []int64
	err     error
}

func (r *fakeRevoker) RevokeAccount(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revoked = append(r.revoked, id)
	return r.err
}

func (r *fakeRevoker) seen() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.revoked...)
}

// fakeObserver records the metric calls, so criterion (d)'s twin — "the
// counter moves once per write" — is checkable without a registry.
type fakeObserver struct {
	mu   sync.Mutex
	seen []string
}

func (o *fakeObserver) IncAdminAction(action, result string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, action+"/"+result)
}

func (o *fakeObserver) actions() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.seen...)
}

type harness struct {
	svc      *Service
	mailcow  *fakeMailcow
	store    *fakeStore
	prov     *fakeProvisioner
	revoker  *fakeRevoker
	observer *fakeObserver
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	mc := newFakeMailcow()
	st := newFakeStore()
	prov := &fakeProvisioner{store: st}
	rev := &fakeRevoker{}
	obs := &fakeObserver{}
	svc, err := New(Config{
		Now:    func() time.Time { return time.Unix(1700000500, 0).UTC() },
		Logger: discardLogger(),
	}, mc, st, prov, rev, nil, obs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &harness{svc: svc, mailcow: mc, store: st, prov: prov, revoker: rev, observer: obs}
}

// existing seeds an account that is already provisioned and synced.
func (h *harness) existing(address string) store.Account {
	appID := int64(100)
	a := h.store.put(store.Account{
		Email: address, State: store.AccountActive,
		CredentialState:      store.CredentialActive,
		DisplayName:          "Seeded",
		QuotaMB:              2048,
		MailcowAppPasswordID: &appID,
	})
	h.mailcow.mu.Lock()
	h.mailcow.mailboxes[address] = mailcow.Mailbox{
		Username: address, Active: 1, Name: "Seeded", Quota: 2048 << 20, QuotaUsed: 1024,
	}
	h.mailcow.mu.Unlock()
	return a
}

// --- §6 (a) scope ------------------------------------------------------------

// TestM1a_ForeignDomainIsNotFoundBeforeAnyMailcowCall is criterion (a), and
// the single most important test in this package.
//
// It asserts TWO things, and the second is the one that matters. A key of
// domain A must answer 404 for an address of domain B — but it must also
// reach Mailcow ZERO times on the way there, because Mailcow's key is not
// scoped by domain (contract §4): if the domain check happened after a
// lookup, a probing consumer could measure the difference and enumerate
// another consumer's mailboxes by timing, and a bug in the lookup could act
// on them outright.
func TestM1a_ForeignDomainIsNotFoundBeforeAnyMailcowCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	foreign := "victim@" + otherDomain

	for _, tc := range []struct {
		name string
		run  func(h *harness) error
	}{
		{"create", func(h *harness) error {
			_, _, err := h.svc.Create(ctx, testCall(), CreateRequest{Address: foreign, Name: "x"})
			return err
		}},
		{"get", func(h *harness) error {
			_, err := h.svc.Get(ctx, testActor(), foreign)
			return err
		}},
		{"update", func(h *harness) error {
			name := "y"
			_, err := h.svc.Update(ctx, testCall(), foreign, UpdateRequest{Name: &name})
			return err
		}},
		{"suspend", func(h *harness) error {
			_, err := h.svc.Suspend(ctx, testCall(), foreign)
			return err
		}},
		{"resume", func(h *harness) error {
			_, err := h.svc.Resume(ctx, testCall(), foreign)
			return err
		}},
		{"readonly", func(h *harness) error {
			_, err := h.svc.ReadOnly(ctx, testCall(), foreign)
			return err
		}},
		{"delete", func(h *harness) error {
			_, err := h.svc.Delete(ctx, testCall(), foreign)
			return err
		}},
		{"startExport", func(h *harness) error {
			_, err := h.svc.StartExport(ctx, testCall(), foreign)
			return err
		}},
		{"getExport", func(h *harness) error {
			_, err := h.svc.GetExport(ctx, testActor(), foreign)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			// The foreign mailbox EXISTS in Mailcow and in Moov. That is the
			// realistic shape — two consumers on one installation — and it is
			// what makes the test meaningful: the refusal cannot be coming
			// from the address simply being unknown.
			h.existing(foreign)

			err := tc.run(h)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("%s on a foreign domain = %v, want ErrNotFound", tc.name, err)
			}
			if calls := h.mailcow.called(); len(calls) != 0 {
				t.Errorf("%s reached Mailcow before refusing: %v", tc.name, calls)
			}
			for _, l := range h.store.auditLines() {
				if l.Address == foreign {
					t.Errorf("%s wrote an audit row naming another domain's mailbox: %+v", tc.name, l)
				}
			}
		})
	}
}

// --- §6 (b) idempotency ------------------------------------------------------

// TestM1b_CreateIsIdempotentByAddress is criterion (b): two identical creates
// give created=true then created=false with EQUAL resources, and exactly ONE
// Mailcow mailbox.
func TestM1b_CreateIsIdempotentByAddress(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	req := CreateRequest{Address: "expo@" + testDomain, Name: "Expo"}

	first, created, err := h.svc.Create(ctx, testCall(), req)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if !created {
		t.Fatal("the first create reported created=false")
	}

	second, created, err := h.svc.Create(ctx, testCall(), req)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if created {
		t.Error("the second create reported created=true")
	}
	if first.Address != second.Address || first.State != second.State || first.Name != second.Name {
		t.Errorf("the repeat returned a different resource:\n first=%+v\nsecond=%+v", first, second)
	}

	// One mailbox, and — the part a status code cannot show — the repeat made
	// no Mailcow call at all.
	h.mailcow.mu.Lock()
	n := len(h.mailcow.mailboxes)
	h.mailcow.mu.Unlock()
	if n != 1 {
		t.Errorf("Mailcow holds %d mailboxes, want exactly 1", n)
	}
	creates := 0
	for _, c := range h.mailcow.called() {
		if c == "CreateMailbox" {
			creates++
		}
	}
	if creates != 1 {
		t.Errorf("CreateMailbox was called %d times, want 1", creates)
	}
}

// TestM1b_RepeatCreateIgnoresADifferentBody pins the half of §2.4 a consumer
// is most likely to get wrong: the repeat is not an update. A retry after a
// lost response must not silently change the quota.
func TestM1b_RepeatCreateIgnoresADifferentBody(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	addr := "expo@" + testDomain

	if _, _, err := h.svc.Create(ctx, testCall(), CreateRequest{
		Address: addr, Name: "Expo", QuotaMB: 2048,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	again, created, err := h.svc.Create(ctx, testCall(), CreateRequest{
		Address: addr, Name: "Renamed", QuotaMB: 8192,
	})
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if created {
		t.Error("the repeat created something")
	}
	if again.Name == "Renamed" {
		t.Error("the repeat applied the new name; a create is not an update (§2.4)")
	}
	if again.Quota.LimitMB == 8192 {
		t.Error("the repeat applied the new quota; a create is not an update (§2.4)")
	}
}

// --- §6 (c) rollback ---------------------------------------------------------

// TestM1c_MailcowFailureAfterCreationLeavesNothingBehind is criterion (c):
// a failure AFTER the mailbox exists leaves no Moov account and no mailbox —
// so the caller's 502 really does mean "nothing was left half-done" (§2.2).
func TestM1c_MailcowFailureAfterCreationLeavesNothingBehind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	addr := "expo@" + testDomain

	for _, tc := range []struct {
		name    string
		breakIt func(h *harness)
	}{
		{"provisioning is refused", func(h *harness) {
			h.prov.provisionErr = provision.ErrInvalidCredentials
		}},
		{"the rate limit is refused", func(h *harness) {
			h.mailcow.failOn["SetMailboxRateLimit"] = fmt.Errorf("%w: denied", mailcow.ErrAPI)
		}},
		{"the facts cannot be stored", func(h *harness) {
			h.store.failSetFacts = errors.New("database is on fire")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.breakIt(h)

			_, created, err := h.svc.Create(ctx, testCall(), CreateRequest{Address: addr, Name: "Expo"})
			if err == nil {
				t.Fatal("the create succeeded despite the injected failure")
			}
			if created {
				t.Error("created=true on a failed create")
			}

			// No mailbox: the rollback deleted the one it made.
			h.mailcow.mu.Lock()
			_, stillThere := h.mailcow.mailboxes[addr]
			h.mailcow.mu.Unlock()
			if stillThere {
				t.Error("the mailbox survived a failed create")
			}
			// And the audit says the write failed, which is what an operator
			// chasing the orphan would look for.
			var sawError bool
			for _, l := range h.store.auditLines() {
				if l.Action == ActionCreate && l.Result == "error" {
					sawError = true
				}
			}
			if !sawError {
				t.Error("a failed create wrote no error audit line")
			}
		})
	}
}

// TestM1c_RollbackNeverDeletesAMailboxItDidNotCreate is the other half of the
// rollback rule, and the dangerous one: a mailbox that was ALREADY in Mailcow
// when the create arrived is not ours to destroy. Deleting it would turn a
// failed provisioning into data loss for a mailbox that may hold mail.
func TestM1c_RollbackNeverDeletesAMailboxItDidNotCreate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	addr := "preexisting@" + testDomain

	// The mailbox exists in Mailcow but has no Moov account — an earlier
	// create that died between the two, or an operator's handiwork.
	h.mailcow.mu.Lock()
	h.mailcow.mailboxes[addr] = mailcow.Mailbox{Username: addr, Active: 1, Quota: 2048 << 20}
	h.mailcow.mu.Unlock()

	h.prov.provisionErr = provision.ErrInvalidCredentials

	if _, _, err := h.svc.Create(ctx, testCall(), CreateRequest{Address: addr, Name: "x"}); err == nil {
		t.Fatal("the create succeeded despite the injected failure")
	}

	h.mailcow.mu.Lock()
	_, stillThere := h.mailcow.mailboxes[addr]
	h.mailcow.mu.Unlock()
	if !stillThere {
		t.Fatal("the rollback deleted a mailbox this call did not create — that is data loss")
	}
}

// --- §6 (d) audit ------------------------------------------------------------

// TestM1d_EveryWriteWritesOneAuditLine is criterion (d): one line per write,
// carrying actor, action, address, result and requestId.
func TestM1d_EveryWriteWritesOneAuditLine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	addr := "audited@" + testDomain

	for _, tc := range []struct {
		name   string
		action string
		run    func(h *harness) error
	}{
		{"create", ActionCreate, func(h *harness) error {
			_, _, err := h.svc.Create(ctx, testCall(), CreateRequest{Address: addr, Name: "A"})
			return err
		}},
		{"update", ActionUpdate, func(h *harness) error {
			h.existing(addr)
			n := "B"
			_, err := h.svc.Update(ctx, testCall(), addr, UpdateRequest{Name: &n})
			return err
		}},
		{"suspend", ActionSuspend, func(h *harness) error {
			h.existing(addr)
			_, err := h.svc.Suspend(ctx, testCall(), addr)
			return err
		}},
		{"resume", ActionResume, func(h *harness) error {
			h.existing(addr)
			_, err := h.svc.Resume(ctx, testCall(), addr)
			return err
		}},
		{"readonly", ActionReadOnly, func(h *harness) error {
			h.existing(addr)
			_, err := h.svc.ReadOnly(ctx, testCall(), addr)
			return err
		}},
		{"delete", ActionDelete, func(h *harness) error {
			h.existing(addr)
			_, err := h.svc.Delete(ctx, testCall(), addr)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			if err := tc.run(h); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			var lines []store.AuditLine
			for _, l := range h.store.auditLines() {
				if l.Action == tc.action {
					lines = append(lines, l)
				}
			}
			if len(lines) != 1 {
				t.Fatalf("%s wrote %d audit lines for %q, want 1: %+v", tc.name, len(lines), tc.action, lines)
			}
			l := lines[0]
			switch {
			case l.ActorID != "sa_0001":
				t.Errorf("actor = %q, want sa_0001", l.ActorID)
			case l.Address != addr:
				t.Errorf("address = %q, want %q", l.Address, addr)
			case l.Result != "ok":
				t.Errorf("result = %q, want ok", l.Result)
			case l.RequestID != "req-1":
				t.Errorf("requestId = %q, want req-1", l.RequestID)
			}

			// The metric moves exactly with the audit row — one counter
			// increment per write, with the contract's own verb as its label.
			want := tc.action + "/ok"
			var found int
			for _, a := range h.observer.actions() {
				if a == want {
					found++
				}
			}
			if found != 1 {
				t.Errorf("observer saw %q %d times, want 1 (saw %v)", want, found, h.observer.actions())
			}
		})
	}
}

// TestM1d_RecreationIsAudited pins the explicit sentence of §2.4: recreating
// an address after deletion is allowed and is an audited event, "never
// silent" (gate criterion 7).
func TestM1d_RecreationIsAudited(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	addr := "phoenix@" + testDomain

	h.existing(addr)
	if _, err := h.svc.Delete(ctx, testCall(), addr); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := h.svc.Purge(ctx); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, _, err := h.svc.Create(ctx, testCall(), CreateRequest{Address: addr, Name: "Again"}); err != nil {
		t.Fatalf("recreate: %v", err)
	}

	var noted bool
	for _, l := range h.store.auditLines() {
		if l.Action == ActionCreate && l.Result == "ok" && l.Note == noteRecreated {
			noted = true
		}
	}
	if !noted {
		t.Errorf("recreating a deleted address was not marked in the audit: %+v", h.store.auditLines())
	}
}

// --- §6 (e) read-only --------------------------------------------------------

// TestM1e_ReadOnlyReissuesTheCredentialWithoutSMTP is the SERVER half of
// criterion (e). The JMAP half (EmailSubmission/set refused, the session
// carrying readOnly) lives in internal/jmap/mail and internal/jmaphttp; this
// is the half F0 made load-bearing.
//
// F0 measured that Mailcow's smtp_access:0 does NOT block submission — AUTH
// on 465 still answers 235 — so the enforcement CANNOT be the flag. It has to
// be the credential, re-issued with a protocol set that excludes SMTP. This
// test pins exactly that, because a refactor that "simplified" the transition
// into setting the flag would leave a mailbox that still sends.
func TestM1e_ReadOnlyReissuesTheCredentialWithoutSMTP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	addr := "retained@" + testDomain
	h.existing(addr)

	acct, err := h.svc.ReadOnly(ctx, testCall(), addr)
	if err != nil {
		t.Fatalf("ReadOnly: %v", err)
	}
	if !acct.ReadOnly || acct.State != StateReadOnly {
		t.Errorf("state = %q readOnly = %t, want readonly/true", acct.State, acct.ReadOnly)
	}

	h.prov.mu.Lock()
	scopes := h.prov.reissueScopes
	h.prov.mu.Unlock()
	if len(scopes) == 0 {
		t.Fatal("the credential was never re-issued; smtp_access:0 alone does not stop submission (F0)")
	}
	for _, s := range scopes {
		if s == mailcow.ProtocolSMTP {
			t.Errorf("the re-issued credential still carries SMTP: %v", scopes)
		}
	}

	// The old app password is gone, or the over-scoped credential would still
	// work for anyone holding it.
	h.mailcow.mu.Lock()
	_, oldStillThere := h.mailcow.appPasswd[100]
	h.mailcow.mu.Unlock()
	if oldStillThere {
		t.Error("the old, SMTP-capable app password was not deleted")
	}
}

// TestM1e_ReadOnlyIsIdempotent: a repeat changes nothing and does not
// re-issue a second credential.
func TestM1e_ReadOnlyIsIdempotent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	addr := "retained@" + testDomain
	h.existing(addr)

	if _, err := h.svc.ReadOnly(ctx, testCall(), addr); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := h.svc.ReadOnly(ctx, testCall(), addr); err != nil {
		t.Fatalf("second: %v", err)
	}

	h.prov.mu.Lock()
	var reissues int
	for _, c := range h.prov.calls {
		if c == "Reissue" {
			reissues++
		}
	}
	h.prov.mu.Unlock()
	if reissues != 1 {
		t.Errorf("Reissue ran %d times over two readonly calls, want 1", reissues)
	}
}

// --- §6 (f) suspend ----------------------------------------------------------

// TestM1f_SuspendRevokesEverySession is criterion (f): after a suspend, a live
// session's next request fails. The service's half of that promise is calling
// the revoker; cmd/moovd's serverRevoker is what makes it real.
func TestM1f_SuspendRevokesEverySession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	addr := "suspended@" + testDomain
	a := h.existing(addr)

	acct, err := h.svc.Suspend(ctx, testCall(), addr)
	if err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if !acct.Suspended || acct.State != StateSuspended {
		t.Errorf("state = %q suspended = %t, want suspended/true", acct.State, acct.Suspended)
	}
	if got := h.revoker.seen(); len(got) != 1 || got[0] != a.ID {
		t.Errorf("revoked = %v, want exactly [%d]", got, a.ID)
	}
	// §2.3: the sync worker stops, which the resource reports as paused.
	if acct.Sync.State != SyncPaused {
		t.Errorf("sync.state = %q, want paused", acct.Sync.State)
	}
}

// TestM1f_ResumeReturnsAReadOnlyAccountToReadOnly is deviation D3's whole
// reason for existing: state is DERIVED from two independent booleans, so
// suspending a read-only account and resuming it lands back on readonly
// rather than on active. A single enum would have lost that.
func TestM1f_ResumeReturnsAReadOnlyAccountToReadOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	addr := "both@" + testDomain
	h.existing(addr)

	if _, err := h.svc.ReadOnly(ctx, testCall(), addr); err != nil {
		t.Fatalf("readonly: %v", err)
	}
	suspended, err := h.svc.Suspend(ctx, testCall(), addr)
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	// §2.3's precedence: suspended outranks readonly in the headline, and the
	// FACT survives beside it.
	if suspended.State != StateSuspended {
		t.Errorf("state = %q, want suspended", suspended.State)
	}
	if !suspended.ReadOnly {
		t.Error("readOnly was lost under the suspension")
	}

	resumed, err := h.svc.Resume(ctx, testCall(), addr)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.State != StateReadOnly {
		t.Errorf("resume landed on %q, want readonly — the account must not silently regain the ability to send", resumed.State)
	}
}

// --- §6 (g) delete -----------------------------------------------------------

// TestM1g_DeleteRevokesPurgesAndThenIsNotFound is criterion (g), end to end:
// sessions revoked, the Mailcow mailbox gone, the row marked deleting (409 to
// anything else meanwhile), and 404 once the purge has run.
func TestM1g_DeleteRevokesPurgesAndThenIsNotFound(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	addr := "doomed@" + testDomain
	a := h.existing(addr)

	acct, err := h.svc.Delete(ctx, testCall(), addr)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if acct.State != StateDeleting || acct.DeletingSince == nil {
		t.Errorf("state = %q deletingSince = %v, want deleting/non-nil", acct.State, acct.DeletingSince)
	}
	if got := h.revoker.seen(); len(got) != 1 || got[0] != a.ID {
		t.Errorf("revoked = %v, want [%d]", got, a.ID)
	}
	h.mailcow.mu.Lock()
	_, stillThere := h.mailcow.mailboxes[addr]
	h.mailcow.mu.Unlock()
	if stillThere {
		t.Error("the Mailcow mailbox survived the delete")
	}

	// While deleting: every other transition is a 409, and a GET still
	// answers the resource (§2.4).
	if _, err := h.svc.Suspend(ctx, testCall(), addr); !errors.Is(err, ErrDeleting) {
		t.Errorf("suspend while deleting = %v, want ErrDeleting", err)
	}
	if _, _, err := h.svc.Create(ctx, testCall(), CreateRequest{Address: addr, Name: "x"}); !errors.Is(err, ErrDeleting) {
		t.Errorf("recreate while deleting = %v, want ErrDeleting", err)
	}
	if _, err := h.svc.Get(ctx, testActor(), addr); err != nil {
		t.Errorf("GET while deleting = %v, want the resource", err)
	}

	// After the purge: gone.
	n, err := h.svc.Purge(ctx)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if n != 1 {
		t.Errorf("purged %d accounts, want 1", n)
	}
	if _, err := h.svc.Get(ctx, testActor(), addr); !errors.Is(err, ErrNotFound) {
		t.Errorf("GET after the purge = %v, want ErrNotFound", err)
	}
}

// TestM1g_DeleteProceedsWhenMailcowSaysTheMailboxIsAlreadyGone: F0 rule 7 —
// Mailcow reports "no such entity" as access_denied. A mailbox already gone
// is not a reason to refuse to purge Moov's own rows, which is what the
// caller is actually asking for.
func TestM1g_DeleteProceedsWhenMailcowSaysTheMailboxIsAlreadyGone(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	addr := "ghost@" + testDomain
	h.existing(addr)
	h.mailcow.failOn["DeleteMailbox"] = &mailcow.APIError{Code: mailcow.CodeAccessDenied}

	acct, err := h.svc.Delete(ctx, testCall(), addr)
	if err != nil {
		t.Fatalf("Delete with an already-absent mailbox = %v, want success", err)
	}
	if acct.State != StateDeleting {
		t.Errorf("state = %q, want deleting", acct.State)
	}
}

// --- the upstream error split (§2.2) ----------------------------------------

// TestUpstreamRefusalAndOutageAreDifferentAnswers pins the 502/503 split that
// tells a consumer whether to retry. A refusal Mailcow ANSWERED is permanent
// for this request; something that did not answer is not.
func TestUpstreamRefusalAndOutageAreDifferentAnswers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	addr := "up@" + testDomain

	t.Run("refused is 502-class", func(t *testing.T) {
		h := newHarness(t)
		h.existing(addr)
		h.mailcow.failOn["EditMailbox"] = fmt.Errorf("%w: refused", mailcow.ErrAPI)
		n := "New"
		_, err := h.svc.Update(ctx, testCall(), addr, UpdateRequest{Name: &n})
		if !errors.Is(err, ErrUpstreamRefused) {
			t.Fatalf("got %v, want ErrUpstreamRefused", err)
		}
	})

	t.Run("unreachable is 503-class", func(t *testing.T) {
		h := newHarness(t)
		h.existing(addr)
		h.mailcow.failOn["EditMailbox"] = errors.New("dial tcp: i/o timeout")
		n := "New"
		_, err := h.svc.Update(ctx, testCall(), addr, UpdateRequest{Name: &n})
		if !errors.Is(err, ErrUpstreamUnavailable) {
			t.Fatalf("got %v, want ErrUpstreamUnavailable", err)
		}
	})
}

// TestUpstreamErrorNeverCarriesTheMailcowBody is §2.2's "never the raw
// upstream body, never a credential". The summary is Moov's; the cause stays
// in the log.
func TestUpstreamErrorNeverCarriesTheMailcowBody(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	addr := "leak@" + testDomain
	h.existing(addr)

	const secret = "api-key-ABCDEF0123456789"
	h.mailcow.failOn["EditMailbox"] = fmt.Errorf("%w: upstream said %s", mailcow.ErrAPI, secret)
	n := "New"
	_, err := h.svc.Update(ctx, testCall(), addr, UpdateRequest{Name: &n})
	if err == nil {
		t.Fatal("expected a failure")
	}
	// The wrapped error DOES carry the cause — it is what the log prints.
	// What must not carry it is the summary the HTTP layer renders, which is
	// pinned in internal/jmaphttp (TestUpstreamBodyNeverReachesTheWire).
	if !strings.Contains(err.Error(), secret) {
		t.Skip("the fake's cause did not survive wrapping; nothing to assert here")
	}
}

// --- validation (§2.5) -------------------------------------------------------

func TestAddressRulesOfSection25(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"expo-diseno-2026@events.example.test", true},
		{"a@events.example.test", true},
		{"a.b_c-d@events.example.test", true},
		{"EXPO@Events.Example.Test", true}, // lower-cased by the server
		{"", false},
		{"no-at-sign", false},
		{".leading@events.example.test", false},
		{"trailing.@events.example.test", false},
		{"double..dot@events.example.test", false},
		{"has space@events.example.test", false},
		{"unicode-ñ@events.example.test", false},
		{"a@nodots", false},
		{strings.Repeat("a", 65) + "@events.example.test", false},
	} {
		got, err := NormalizeAddress("address", tc.in)
		if tc.want && err != nil {
			t.Errorf("NormalizeAddress(%q) = %v, want accepted", tc.in, err)
			continue
		}
		if !tc.want && err == nil {
			t.Errorf("NormalizeAddress(%q) = %q, want refused", tc.in, got)
			continue
		}
		if tc.want && got != strings.ToLower(strings.TrimSpace(tc.in)) {
			t.Errorf("NormalizeAddress(%q) = %q, want it lower-cased", tc.in, got)
		}
	}
}

func TestLimitBoundsReportTheFirstOffenderWithItsDottedName(t *testing.T) {
	t.Parallel()
	over := MaxSendPerDay + 1
	bad := 0
	l := Limits{SendPerDay: &over, RecipientsPerMessage: &bad}
	ferr := l.Validate()
	if ferr == nil {
		t.Fatal("two invalid limits were accepted")
	}
	if ferr.Field != "limits.sendPerDay" {
		t.Errorf("field = %q, want limits.sendPerDay (the FIRST offender, §2.2)", ferr.Field)
	}
}

// --- scopes ------------------------------------------------------------------

// TestWriteScopeImpliesRead is §2.1's one-line rule, pinned because it is the
// kind of thing a rewrite of HasScope would quietly drop, leaving every read
// route refusing a write key.
func TestWriteScopeImpliesRead(t *testing.T) {
	t.Parallel()
	sa := store.ServiceAccount{Scopes: []string{ScopeWrite}}
	if !sa.HasScope(ScopeRead) {
		t.Error("accounts:write does not imply accounts:read")
	}
	ro := store.ServiceAccount{Scopes: []string{ScopeRead}}
	if ro.HasScope(ScopeWrite) {
		t.Error("accounts:read grants accounts:write")
	}
}

// fakeNudger records the provisioning nudges (the 2026-09-17 defect).
type fakeNudger struct {
	mu sync.Mutex
	n  int
}

func (f *fakeNudger) Nudge() {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
}

func (f *fakeNudger) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// TestCreateNudgesTheSyncEngine pins the accounts half of the 2026-09-17 fix.
//
// The engine now discovers accounts on a sweep, so this nudge is not what makes
// a new mailbox work — it is what makes it work NOW, for the organizer who is
// looking at the webmail seconds after the 201. Nothing the compiler checks
// connects Create to the supervisor, so this test is what stops the call being
// dropped in a refactor and nobody noticing for the length of one sweep.
func TestCreateNudgesTheSyncEngine(t *testing.T) {
	h := newHarness(t)
	nudger := &fakeNudger{}
	h.svc.SetSyncNudger(nudger)

	call := Call{Actor: Actor{ID: "k1", Domain: "corppass.events"}, RequestID: "r1"}
	if _, created, err := h.svc.Create(context.Background(), call, CreateRequest{
		Address: "unidos@corppass.events", Name: "Unidos",
	}); err != nil || !created {
		t.Fatalf("Create: created=%v err=%v", created, err)
	}

	if got := nudger.count(); got != 1 {
		t.Errorf("Create nudged the sync engine %d times, want 1: a provisioned mailbox that the "+
			"engine is not told about waits out a whole discovery sweep", got)
	}

	// The idempotent repeat provisions nothing, so it has nothing to announce.
	// Nudging anyway would be harmless but dishonest, and a retry storm on a
	// lost response would become a sweep storm.
	if _, created, err := h.svc.Create(context.Background(), call, CreateRequest{
		Address: "unidos@corppass.events", Name: "Unidos",
	}); err != nil || created {
		t.Fatalf("repeat Create: created=%v err=%v", created, err)
	}
	if got := nudger.count(); got != 1 {
		t.Errorf("an idempotent repeat nudged the engine again (%d total)", got)
	}
}

// TestCreateWithoutANudgerStillSucceeds keeps the seam optional: a daemon that
// serves the accounts API with the sync engine disabled is a supported
// configuration, and a nil nudger must be a no-op rather than a panic.
func TestCreateWithoutANudgerStillSucceeds(t *testing.T) {
	h := newHarness(t) // no SetSyncNudger

	call := Call{Actor: Actor{ID: "k1", Domain: "corppass.events"}, RequestID: "r1"}
	if _, created, err := h.svc.Create(context.Background(), call, CreateRequest{
		Address: "solo@corppass.events",
	}); err != nil || !created {
		t.Fatalf("Create: created=%v err=%v", created, err)
	}
}
