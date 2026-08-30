package sieve

import (
	"context"
	"strings"
	"testing"
)

// memClient is an in-memory Client for the manager tests: a map of scripts
// plus the active marker, mimicking Dovecot's semantics for the operations
// the manager uses. The wire is already covered by client_test.go; these
// tests are about ORCHESTRATION — what gets preserved, backed up, imported
// and activated, in which order.
type memClient struct {
	scripts map[string][]byte
	active  string
	caps    Capabilities
	puts    []string // names PUTSCRIPT touched, in order, for assertions
}

func newMemClient() *memClient {
	return &memClient{
		scripts: map[string][]byte{},
		caps: Capabilities{Extensions: []string{
			"fileinto", "reject", "envelope", "vacation", "imap4flags", "copy",
			"include", "variables", "body", "relational", "date", "index",
			"duplicate", "mime", "foreverypart", "regex",
		}},
	}
}

func (m *memClient) Connect(context.Context, Config) error { return nil }
func (m *memClient) Capabilities() Capabilities            { return m.caps }
func (m *memClient) Close() error                          { return nil }

func (m *memClient) ListScripts(context.Context) ([]ScriptInfo, error) {
	var out []ScriptInfo
	for name := range m.scripts {
		out = append(out, ScriptInfo{Name: name, Active: name == m.active})
	}
	return out, nil
}

func (m *memClient) GetScript(_ context.Context, name string) ([]byte, error) {
	c, ok := m.scripts[name]
	if !ok {
		return nil, ErrScriptNotFound
	}
	return c, nil
}

func (m *memClient) PutScript(_ context.Context, name string, content []byte) (string, error) {
	m.scripts[name] = append([]byte(nil), content...)
	m.puts = append(m.puts, name)
	return "", nil
}

func (m *memClient) CheckScript(context.Context, []byte) (string, error) { return "", nil }

func (m *memClient) SetActive(_ context.Context, name string) error {
	if name == "" {
		m.active = ""
		return nil
	}
	if _, ok := m.scripts[name]; !ok {
		return ErrScriptNotFound
	}
	m.active = name
	return nil
}

func (m *memClient) DeleteScript(_ context.Context, name string) error {
	if name == m.active {
		return ErrScriptActive
	}
	if _, ok := m.scripts[name]; !ok {
		return ErrScriptNotFound
	}
	delete(m.scripts, name)
	return nil
}

func (m *memClient) RenameScript(_ context.Context, oldName, newName string) error {
	c, ok := m.scripts[oldName]
	if !ok {
		return ErrScriptNotFound
	}
	if _, taken := m.scripts[newName]; taken {
		return ErrScriptExists
	}
	delete(m.scripts, oldName)
	m.scripts[newName] = c
	if m.active == oldName {
		m.active = newName
	}
	return nil
}

func simpleModel() *Script {
	return &Script{Version: MetadataVersion, Rules: []Rule{{
		ID: "b1", Type: RuleBlocked, Enabled: true,
		Criteria: Criteria{From: []string{"bad@spam.example"}},
	}}}
}

// First push on an empty account: script stored and activated, no backups.
func TestPushOnEmptyAccount(t *testing.T) {
	mc := newMemClient()
	mgr := NewManager(mc, testLogger(t))
	if _, err := mgr.Push(testCtx(t), simpleModel(), GenerateEnv{}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if mc.active != ManagedScriptName {
		t.Errorf("active = %q, want %q", mc.active, ManagedScriptName)
	}
	if len(mc.scripts) != 1 {
		t.Errorf("scripts = %v, want just the managed one", names(mc))
	}
}

// The takeover contract, exactly as the epic states it: a foreign ACTIVE
// script is imported verbatim into the external section (requires merged),
// a backup copy is stored under moov-backup-<name>, the ORIGINAL is left
// stored untouched, and only then is ours activated.
func TestTakeoverOfForeignActiveScript(t *testing.T) {
	foreign := "require \"fileinto\";\r\n# sogo rule\r\nif header :contains \"subject\" \"x\" { fileinto \"X\"; }\r\n"
	mc := newMemClient()
	mc.scripts["sogo"] = []byte(foreign)
	mc.active = "sogo"

	mgr := NewManager(mc, testLogger(t))
	if _, err := mgr.Push(testCtx(t), simpleModel(), GenerateEnv{}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	if mc.active != ManagedScriptName {
		t.Errorf("active = %q, want ours", mc.active)
	}
	if got := string(mc.scripts["sogo"]); got != foreign {
		t.Errorf("the foreign script was modified:\n got  %q\n want %q", got, foreign)
	}
	backup, ok := mc.scripts["moov-backup-sogo"]
	if !ok {
		t.Fatalf("no backup copy was stored; scripts: %v", names(mc))
	}
	if string(backup) != foreign {
		t.Errorf("the backup is not byte-identical to the original")
	}
	ours := string(mc.scripts[ManagedScriptName])
	if !strings.Contains(ours, "# sogo rule") || !strings.Contains(ours, "fileinto \"X\"") {
		t.Errorf("the foreign rules were not imported into the managed script:\n%s", ours)
	}
	if !strings.Contains(ours, "fileinto \"Junk\"") {
		t.Errorf("the model's own rules are missing:\n%s", ours)
	}
	// The backup must be stored BEFORE the managed script is written.
	if len(mc.puts) < 2 || mc.puts[0] != "moov-backup-sogo" {
		t.Errorf("put order = %v, want the backup first", mc.puts)
	}
}

// A drifted managed script (hand-edited) is backed up before regeneration.
func TestDriftedManagedScriptIsBackedUp(t *testing.T) {
	mc := newMemClient()
	mgr := NewManager(mc, testLogger(t))
	ctx := testCtx(t)
	if _, err := mgr.Push(ctx, simpleModel(), GenerateEnv{}); err != nil {
		t.Fatalf("Push 1: %v", err)
	}
	// Hand-edit the stored script.
	edited := append([]byte(nil), mc.scripts[ManagedScriptName]...)
	edited = append(edited, []byte("# hand edit\r\nkeep;\r\n")...)
	mc.scripts[ManagedScriptName] = edited

	state, err := mgr.Read(ctx, GenerateEnv{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !state.Drifted {
		t.Fatal("the hand edit was not detected as drift")
	}

	if _, err := mgr.Push(ctx, state.Model, GenerateEnv{}); err != nil {
		t.Fatalf("Push 2: %v", err)
	}
	backup, ok := mc.scripts["moov-backup-moov"]
	if !ok {
		t.Fatalf("no backup of the drifted script; scripts: %v", names(mc))
	}
	if string(backup) != string(edited) {
		t.Error("the drift backup is not byte-identical to the edited script")
	}
}

// A script stored under our name WITHOUT our metadata belongs to someone:
// backed up AND imported, never clobbered.
func TestForeignContentUnderOurNameIsImported(t *testing.T) {
	foreign := "# someone's script stored as moov\r\nkeep;\r\n"
	mc := newMemClient()
	mc.scripts[ManagedScriptName] = []byte(foreign)
	mc.active = ManagedScriptName

	mgr := NewManager(mc, testLogger(t))
	if _, err := mgr.Push(testCtx(t), simpleModel(), GenerateEnv{}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if string(mc.scripts["moov-backup-moov"]) != foreign {
		t.Error("the foreign content was not backed up byte-identical")
	}
	if !strings.Contains(string(mc.scripts[ManagedScriptName]), "# someone's script stored as moov") {
		t.Error("the foreign content was not imported into the external section")
	}
}

// Read on an account where another client (Bulwark, a hand-written script)
// holds the active slot reports OursActive false — the honesty bit the
// settings UI surfaces.
func TestReadReportsForeignActive(t *testing.T) {
	mc := newMemClient()
	mgr := NewManager(mc, testLogger(t))
	ctx := testCtx(t)
	if _, err := mgr.Push(ctx, simpleModel(), GenerateEnv{}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	mc.scripts["bulwark"] = []byte("keep;\r\n")
	mc.active = "bulwark"

	state, err := mgr.Read(ctx, GenerateEnv{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if state.OursActive {
		t.Error("OursActive = true while bulwark holds the active slot")
	}
	if state.ActiveScript != "bulwark" {
		t.Errorf("ActiveScript = %q", state.ActiveScript)
	}
	if len(state.Model.Rules) != 1 {
		t.Errorf("the managed model was lost: %+v", state.Model)
	}
}

func names(m *memClient) []string {
	out := make([]string, 0, len(m.scripts))
	for n := range m.scripts {
		out = append(out, n)
	}
	return out
}
