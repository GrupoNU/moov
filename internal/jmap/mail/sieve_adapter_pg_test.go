package mail_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/jmap/mail"
	"github.com/GrupoNU/moov/internal/sieve"
	"github.com/GrupoNU/moov/internal/store"
)

// The SieveAdapter against a REAL store and blob tree (env-gated like every
// PG test here) with an in-memory ManageSieve — the full path the conformance
// fakes cannot cover: the managed script is genuinely GENERATED, PARSED and
// TAKEN OVER by internal/sieve's real code, the ledger really maps ids, the
// forwarding facts really gate the generator, and the blobs really pin.

// memSieveClient implements sieve.Client in memory (the mail_test twin of
// internal/sieve's memClient, which lives in that package's test binary).
type memSieveClient struct {
	scripts map[string][]byte
	active  string
	caps    sieve.Capabilities
}

func newMemSieveClient() *memSieveClient {
	return &memSieveClient{
		scripts: map[string][]byte{},
		caps: sieve.Capabilities{Extensions: []string{
			"fileinto", "reject", "envelope", "vacation", "imap4flags", "copy",
			"include", "variables", "body", "relational", "date", "index",
			"duplicate", "mime", "foreverypart", "regex",
		}},
	}
}

func (m *memSieveClient) Connect(context.Context, sieve.Config) error { return nil }
func (m *memSieveClient) Capabilities() sieve.Capabilities            { return m.caps }
func (m *memSieveClient) Close() error                                { return nil }

func (m *memSieveClient) ListScripts(context.Context) ([]sieve.ScriptInfo, error) {
	var out []sieve.ScriptInfo
	for name := range m.scripts {
		out = append(out, sieve.ScriptInfo{Name: name, Active: name == m.active})
	}
	return out, nil
}

func (m *memSieveClient) GetScript(_ context.Context, name string) ([]byte, error) {
	c, ok := m.scripts[name]
	if !ok {
		return nil, sieve.ErrScriptNotFound
	}
	return c, nil
}

func (m *memSieveClient) PutScript(_ context.Context, name string, content []byte) (string, error) {
	m.scripts[name] = append([]byte(nil), content...)
	return "", nil
}

func (m *memSieveClient) CheckScript(context.Context, []byte) (string, error) { return "", nil }

func (m *memSieveClient) SetActive(_ context.Context, name string) error {
	if name == "" {
		m.active = ""
		return nil
	}
	if _, ok := m.scripts[name]; !ok {
		return sieve.ErrScriptNotFound
	}
	m.active = name
	return nil
}

func (m *memSieveClient) DeleteScript(_ context.Context, name string) error {
	if name == m.active {
		return sieve.ErrScriptActive
	}
	if _, ok := m.scripts[name]; !ok {
		return sieve.ErrScriptNotFound
	}
	delete(m.scripts, name)
	return nil
}

func (m *memSieveClient) RenameScript(_ context.Context, oldName, newName string) error {
	c, ok := m.scripts[oldName]
	if !ok {
		return sieve.ErrScriptNotFound
	}
	if _, taken := m.scripts[newName]; taken {
		return sieve.ErrScriptExists
	}
	delete(m.scripts, oldName)
	m.scripts[newName] = c
	if m.active == oldName {
		m.active = newName
	}
	return nil
}

// capturingMailer records the verification mails instead of sending them.
type capturingMailer struct {
	to    []string
	token []string
	fail  bool
}

func (c *capturingMailer) SendVerification(_ context.Context, _ int64, to, token string, _ time.Time) error {
	if c.fail {
		return errors.New("smtp said no")
	}
	c.to = append(c.to, to)
	c.token = append(c.token, token)
	return nil
}

// plainTokens is a transparent ForwardingTokens for the adapter tests (the
// real keyring-sealed implementation lives in cmd/moovd with its own test).
type plainTokens struct{}

func (plainTokens) Mint(accountID int64, email string, expires time.Time) (string, error) {
	return fmt.Sprintf("tok|%d|%s|%d", accountID, email, expires.Unix()), nil
}

func (plainTokens) Verify(accountID int64, token string) (string, error) {
	parts := strings.Split(token, "|")
	if len(parts) != 4 || parts[0] != "tok" || parts[1] != fmt.Sprint(accountID) {
		return "", errors.New("bad token")
	}
	var exp int64
	if _, err := fmt.Sscanf(parts[3], "%d", &exp); err != nil || time.Now().Unix() > exp {
		return "", errors.New("bad token")
	}
	return parts[2], nil
}

type sieveFixture struct {
	*fixture
	client  *memSieveClient
	adapter *mail.SieveAdapter
	mailer  *capturingMailer
}

func newSieveFixture(t *testing.T) *sieveFixture {
	t.Helper()
	f := newFixture(t)
	client := newMemSieveClient()
	mailer := &capturingMailer{}
	adapter, err := mail.NewSieveAdapter(mail.SieveAdapterConfig{
		Store: f.store,
		Blobs: f.blobs,
		Connect: func(context.Context, store.Account) (sieve.Client, error) {
			return client, nil
		},
		Tokens: plainTokens{},
		Mailer: mailer,
	})
	if err != nil {
		t.Fatalf("NewSieveAdapter: %v", err)
	}
	return &sieveFixture{fixture: f, client: client, adapter: adapter, mailer: mailer}
}

// Vacation set -> the REAL generator writes the managed script, activates
// it, and a later get parses the REAL script back.
func TestSieveAdapterVacationRoundTripThroughRealScript(t *testing.T) {
	f := newSieveFixture(t)
	ctx := f.ctx

	subject := "Fuera de la oficina"
	text := "Vuelvo el lunes."
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	err := f.adapter.SetVacation(ctx, f.account.ID, mail.VacationValue{
		IsEnabled: true, Subject: &subject, TextBody: &text, FromDate: &from,
	})
	if err != nil {
		t.Fatalf("SetVacation: %v", err)
	}

	script, ok := f.client.scripts["moov"]
	if !ok || f.client.active != "moov" {
		t.Fatalf("the managed script was not stored+activated: %v (active %q)", mapKeys(f.client.scripts), f.client.active)
	}
	for _, want := range []string{"vacation :days 4", `:subject "Fuera de la oficina"`, `not header :contains "x-spam-flag" "YES"`} {
		if !strings.Contains(string(script), want) {
			t.Errorf("the stored script is missing %q", want)
		}
	}

	got, err := f.adapter.GetVacation(ctx, f.account.ID)
	if err != nil {
		t.Fatalf("GetVacation: %v", err)
	}
	if !got.IsEnabled || got.Subject == nil || *got.Subject != subject ||
		got.FromDate == nil || !got.FromDate.Equal(from) {
		t.Errorf("round trip lost data: %+v", got)
	}
}

// The takeover contract through the adapter: a foreign ACTIVE script is
// preserved verbatim (imported + backed up + left stored) before ours
// activates.
func TestSieveAdapterTakeoverPreservesForeignScript(t *testing.T) {
	f := newSieveFixture(t)
	ctx := f.ctx

	foreign := "require \"fileinto\";\r\n# sogo made this\r\nif true { fileinto \"X\"; }\r\n"
	f.client.scripts["sogo"] = []byte(foreign)
	f.client.active = "sogo"

	err := f.adapter.PutFilters(ctx, f.account.ID, []mail.FilterRuleValue{
		{ID: "b1", Type: "blocked", Enabled: true, From: []string{"bad@spam.example"}},
	}, mail.ForwardAllValue{})
	if err != nil {
		t.Fatalf("PutFilters: %v", err)
	}

	if got := string(f.client.scripts["sogo"]); got != foreign {
		t.Errorf("the foreign script was modified:\n got %q\nwant %q", got, foreign)
	}
	if got := string(f.client.scripts["moov-backup-sogo"]); got != foreign {
		t.Errorf("no byte-identical backup: %q", got)
	}
	ours := string(f.client.scripts["moov"])
	if !strings.Contains(ours, "# sogo made this") || !strings.Contains(ours, `fileinto "X"`) {
		t.Errorf("the foreign rules were not imported:\n%s", ours)
	}
	if f.client.active != "moov" {
		t.Errorf("active = %q", f.client.active)
	}

	cfg, err := f.adapter.GetFilters(ctx, f.account.ID)
	if err != nil {
		t.Fatalf("GetFilters: %v", err)
	}
	if !cfg.ScriptActive || len(cfg.Rules) != 1 {
		t.Errorf("GetFilters = %+v", cfg)
	}
}

// The forwarding verification flow end to end against the real ledger: the
// generator refuses the unverified address, the token flips it, and then the
// SAME rule generates — pinning that the enforcement input is the accepted
// set and nothing else.
func TestSieveAdapterForwardingVerificationGatesTheGenerator(t *testing.T) {
	f := newSieveFixture(t)
	ctx := f.ctx

	forward := func() error {
		return f.adapter.PutFilters(ctx, f.account.ID, []mail.FilterRuleValue{
			{ID: "r1", Type: "filter", Enabled: true, From: []string{"a@b.c"}, Forward: "dest@other.example"},
		}, mail.ForwardAllValue{})
	}
	err := forward()
	var invalid *mail.SieveInvalidError
	if !errors.As(err, &invalid) || !strings.Contains(invalid.Description, "dest@other.example") {
		t.Fatalf("unverified forward = %v, want the model refusal naming the address", err)
	}

	row, err := f.adapter.CreateForwardingAddress(ctx, f.account.ID, "Dest@Other.Example")
	if err != nil {
		t.Fatalf("CreateForwardingAddress: %v", err)
	}
	if row.State != store.ForwardingPending || len(f.mailer.token) != 1 || f.mailer.to[0] != "dest@other.example" {
		t.Fatalf("row=%+v mailer=%+v", row, f.mailer)
	}
	// Pending is NOT verified: the rule still refuses.
	if err := forward(); err == nil {
		t.Fatal("a pending address unlocked the generator")
	}

	email, err := f.adapter.VerifyForwarding(ctx, f.account.ID, f.mailer.token[0])
	if err != nil || email != "dest@other.example" {
		t.Fatalf("VerifyForwarding = %q, %v", email, err)
	}
	if err := forward(); err != nil {
		t.Fatalf("a verified address still refused: %v", err)
	}
	if !strings.Contains(string(f.client.scripts["moov"]), `redirect :copy "dest@other.example"`) {
		t.Error("the generated script is missing the redirect")
	}

	// In use -> destroy refused; token junk -> the no-oracle sentinel.
	if err := f.adapter.DestroyForwardingAddress(ctx, f.account.ID, row.ID); !errors.Is(err, mail.ErrForwardingInUse) {
		t.Errorf("destroy in-use = %v, want ErrForwardingInUse", err)
	}
	if _, err := f.adapter.VerifyForwarding(ctx, f.account.ID, "garbage"); !errors.Is(err, mail.ErrTokenInvalid) {
		t.Errorf("garbage token = %v, want ErrTokenInvalid", err)
	}
}

// A failed verification mail leaves no pending row behind.
func TestSieveAdapterFailedMailLeavesNoRow(t *testing.T) {
	f := newSieveFixture(t)
	f.mailer.fail = true
	if _, err := f.adapter.CreateForwardingAddress(f.ctx, f.account.ID, "dest@x.example"); err == nil {
		t.Fatal("a failed verification mail did not fail the create")
	}
	rows, err := f.adapter.ListForwardingAddresses(f.ctx, f.account.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows = %+v, want none after the failed send", rows)
	}
}

// ListScripts pins content into the blob store (downloadable via the
// account's pin) and the ledger keeps ids stable across renames.
func TestSieveAdapterLedgerAndBlobs(t *testing.T) {
	f := newSieveFixture(t)
	ctx := f.ctx

	content := []byte("# user script\r\nkeep;\r\n")
	info, err := f.adapter.CreateScript(ctx, f.account.ID, "mine", content)
	if err != nil {
		t.Fatalf("CreateScript: %v", err)
	}
	rc, err := f.blobs.Open(mustHash(t, info.BlobID))
	if err != nil {
		t.Fatalf("the script blob is not readable: %v", err)
	}
	_ = rc.Close()
	if info.Size != int64(len(content)) {
		t.Errorf("reported size = %d, want %d", info.Size, len(content))
	}

	newName := "renamed"
	updated, err := f.adapter.UpdateScript(ctx, f.account.ID, info.ID, &newName, nil)
	if err != nil {
		t.Fatalf("UpdateScript: %v", err)
	}
	if updated.ID != info.ID || updated.Name != "renamed" {
		t.Errorf("rename changed the id or lost the name: %+v (RFC 9661 §2.1: id immutable)", updated)
	}
	if _, ok := f.client.scripts["renamed"]; !ok {
		t.Error("the server-side rename did not happen")
	}

	// The redirect policy scan over REAL content, fail-closed included.
	err = f.adapter.CheckRedirectPolicy(ctx, f.account.ID, []byte("redirect \"nobody@evil.example\";\r\n"))
	if err == nil || !strings.Contains(err.Error(), "nobody@evil.example") {
		t.Errorf("policy scan = %v, want a refusal naming the address", err)
	}
	if err := f.adapter.CheckRedirectPolicy(ctx, f.account.ID, []byte("redirect \"unterminated")); err == nil {
		t.Error("an unscannable script passed the policy gate; it must fail closed")
	}
	if err := f.adapter.CheckRedirectPolicy(ctx, f.account.ID, []byte("keep;\r\n")); err != nil {
		t.Errorf("a redirect-free script was refused: %v", err)
	}
}

func mapKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mustHash(t *testing.T, s string) blob.Hash {
	t.Helper()
	h, err := blob.ParseHash(s)
	if err != nil {
		t.Fatalf("ParseHash(%q): %v", s, err)
	}
	return h
}
