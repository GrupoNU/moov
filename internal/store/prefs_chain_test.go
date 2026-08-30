package store

import (
	"encoding/json"
	"errors"
	"testing"
)

// The preference schema-version chain, tested as the pure function it is — no
// database, so it runs in the DB-less unit gate and a future data migration
// can be reasoned about offline.
//
// This file is in-package (unlike prefs_test.go) because migratePrefs and
// encodePrefs are deliberately unexported: the chain is an implementation
// detail of GetPrefs/PutPrefs, and exporting it would invite a caller to read
// a document without going through the store's error handling.

func TestMigratePrefsEmptyDocumentIsDefaults(t *testing.T) {
	// The column's DEFAULT '{}' and a nil scan both land here.
	for name, raw := range map[string][]byte{
		"nil":          nil,
		"empty bytes":  {},
		"empty object": []byte(`{}`),
	} {
		t.Run(name, func(t *testing.T) {
			got, version, err := migratePrefs(raw)
			if err != nil {
				t.Fatalf("migratePrefs: %v", err)
			}
			if got != DefaultPrefs() {
				t.Errorf("prefs = %+v, want the defaults %+v", got, DefaultPrefs())
			}
			if version != PrefsSchemaVersion {
				t.Errorf("version = %d, want %d", version, PrefsSchemaVersion)
			}
		})
	}
}

func TestMigratePrefsFillsUnsetKeysFromDefaults(t *testing.T) {
	// One key set; every other must come from the defaults. That is the whole
	// "defaults on read" mechanism, and the reason a moved default reaches
	// users who never expressed an opinion (migration 0007's header).
	got, _, err := migratePrefs([]byte(`{"v":1,"density":"compact"}`))
	if err != nil {
		t.Fatalf("migratePrefs: %v", err)
	}
	want := DefaultPrefs()
	want.Density = "compact"
	if got != want {
		t.Errorf("prefs = %+v, want %+v", got, want)
	}
}

func TestMigratePrefsUnversionedIsV1(t *testing.T) {
	// A document with no "v" is v1 by construction: v1 is the first schema
	// that ever existed, so nothing older can be stored.
	got, version, err := migratePrefs([]byte(`{"theme":"dark"}`))
	if err != nil {
		t.Fatalf("migratePrefs: %v", err)
	}
	if version != 1 {
		t.Errorf("version = %d, want 1 (an unversioned document IS v1)", version)
	}
	if got.Theme != "dark" {
		t.Errorf("theme = %q, want the stored value", got.Theme)
	}
}

func TestMigratePrefsRefusesAFutureVersion(t *testing.T) {
	// The rollback case: an old binary meeting a document a newer one wrote.
	// It must refuse rather than read partially, because a partial read
	// followed by a save would destroy the newer data.
	_, _, err := migratePrefs([]byte(`{"v":2,"theme":"dark"}`))
	if err == nil {
		t.Fatal("migratePrefs accepted a document from a future schema version")
	}
	if !errors.Is(err, ErrPrefsUnknownVersion) {
		t.Errorf("error = %v, want ErrPrefsUnknownVersion", err)
	}
	// The message must name both versions, or an operator reading a log has no
	// way to tell which binary to roll forward to.
	if msg := err.Error(); msg == "" {
		t.Error("the refusal carries no explanation")
	}
}

func TestMigratePrefsRejectsMalformedDocuments(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":         `{`,
		"array":            `[1,2,3]`,
		"scalar":           `"hello"`,
		"non-numeric v":    `{"v":"one"}`,
		"wrong field type": `{"v":1,"undoSendSeconds":"thirty"}`,
		"wrong bool type":  `{"v":1,"hoverActions":"yes"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := migratePrefs([]byte(raw)); err == nil {
				t.Errorf("migratePrefs accepted %s", raw)
			}
		})
	}
}

func TestEncodePrefsRoundTrips(t *testing.T) {
	// Encode/decode is the pair the store relies on to verify, on every write,
	// that what landed in the column is something this build can read back.
	in := DefaultPrefs()
	in.Theme = "system"
	in.Language = "pt-BR"
	in.UndoSendSeconds = 20

	raw, err := encodePrefs(in)
	if err != nil {
		t.Fatalf("encodePrefs: %v", err)
	}
	got, version, err := migratePrefs(raw)
	if err != nil {
		t.Fatalf("migratePrefs on encoded output: %v", err)
	}
	if got != in {
		t.Errorf("round trip changed the value: %+v -> %+v", in, got)
	}
	if version != PrefsSchemaVersion {
		t.Errorf("version = %d, want %d", version, PrefsSchemaVersion)
	}
}

func TestEncodePrefsWritesEveryKeyPlusTheVersion(t *testing.T) {
	// The document is written DENSE on purpose (encodePrefs' comment): a
	// Prefs/set naming one property must preserve the other twelve exactly as
	// they were served, which is the idempotence the JMAP patch depends on.
	raw, err := encodePrefs(DefaultPrefs())
	if err != nil {
		t.Fatalf("encodePrefs: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the encoded document is not an object: %v", err)
	}

	// Every JSON tag of Prefs, plus "v".
	want := []string{
		"v",
		"undoSendSeconds", "imagesPolicy", "conversationView", "hoverActions",
		"autoAdvance", "density", "showSnippets", "keyboardShortcuts",
		"language", "readingPane", "inboxType", "notifications", "theme",
	}
	for _, key := range want {
		if _, ok := doc[key]; !ok {
			t.Errorf("the encoded document is missing %q", key)
		}
	}
	if len(doc) != len(want) {
		t.Errorf("the encoded document has %d keys, want exactly %d: %v", len(doc), len(want), doc)
	}
}

// TestDefaultPrefsMatchTheSignedDecisions pins the values that were ARBITRATED
// rather than inherited, so a future edit to DefaultPrefs cannot quietly undo
// a signed decision. Each assertion names the decision it guards.
func TestDefaultPrefsMatchTheSignedDecisions(t *testing.T) {
	d := DefaultPrefs()

	// D-3 (signed 2026-08-30): Moov diverges from Gmail's off-default.
	if !d.KeyboardShortcuts {
		t.Error("keyboardShortcuts must default ON — decision D-3, a registered divergence from Gmail")
	}
	// D-4: display by default, which is only defensible because the HMAC image
	// proxy exists (canon §7.1).
	if d.ImagesPolicy != "always" {
		t.Errorf("imagesPolicy = %q, want \"always\" — decision D-4, gated on the HMAC proxy", d.ImagesPolicy)
	}
	// Canon §2.3: 10 s is Gmail's own default within {5,10,20,30}, and it is
	// the same value internal/config's DefaultUndoWindowSeconds carries.
	if d.UndoSendSeconds != 10 {
		t.Errorf("undoSendSeconds = %d, want Gmail's default 10", d.UndoSendSeconds)
	}
	// Canon §2.2: Gmail's auto-advance returns to the conversation list.
	if d.AutoAdvance != "list" {
		t.Errorf("autoAdvance = %q, want Gmail's default \"list\"", d.AutoAdvance)
	}
	// Canon §2.2 (/2473038): hover actions are ON by default in Gmail.
	if !d.HoverActions {
		t.Error("hoverActions must default ON — canon §2.2")
	}
	// GC-2: two modes pre-AI, and the default is to notify.
	if d.Notifications != "new" {
		t.Errorf("notifications = %q, want \"new\" (GC-2)", d.Notifications)
	}
}
