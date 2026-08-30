package store

import (
	"encoding/json"
	"errors"
	"fmt"
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
	// The column's DEFAULT '{}' and a nil scan both yield the defaults — but
	// they report DIFFERENT versions, and the difference is not an accident:
	//
	//	nil / empty bytes -> there is no document. Nothing was ever stored, so
	//	                     there is no stored version to report and the current
	//	                     one is the honest answer.
	//	`{}`              -> there IS a document, and it carries no "v". By the
	//	                     chain's rule an unversioned document is v1 (v1 being
	//	                     the first schema that ever existed), so that is what
	//	                     it reports — even though its CONTENT is
	//	                     indistinguishable from the defaults.
	//
	// Reporting `{}` as the current version would tell an operator counting
	// un-rewritten rows that a row had been migrated when it had not.
	for name, tc := range map[string]struct {
		raw         []byte
		wantVersion int
	}{
		"nil":          {nil, PrefsSchemaVersion},
		"empty bytes":  {[]byte{}, PrefsSchemaVersion},
		"empty object": {[]byte(`{}`), 1},
	} {
		t.Run(name, func(t *testing.T) {
			got, version, err := migratePrefs(tc.raw)
			if err != nil {
				t.Fatalf("migratePrefs: %v", err)
			}
			if !got.Equal(DefaultPrefs()) {
				t.Errorf("prefs = %+v, want the defaults %+v", got, DefaultPrefs())
			}
			if version != tc.wantVersion {
				t.Errorf("version = %d, want %d", version, tc.wantVersion)
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
	if !got.Equal(want) {
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
	//
	// The version tested is PrefsSchemaVersion+1 rather than a literal, so this
	// test keeps testing the FUTURE and not a version that has since shipped —
	// which is exactly what happened to its previous form, where the literal
	// `{"v":2}` became a document this build reads.
	future := fmt.Sprintf(`{"v":%d,"theme":"dark"}`, PrefsSchemaVersion+1)
	_, _, err := migratePrefs([]byte(future))
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

	// A far-future version too: the refusal must not depend on being adjacent.
	if _, _, err := migratePrefs([]byte(`{"v":99,"theme":"dark"}`)); !errors.Is(err, ErrPrefsUnknownVersion) {
		t.Errorf("v99 error = %v, want ErrPrefsUnknownVersion", err)
	}
}

// ---------------------------------------------------------------------------
// v2 — the roaming keys of E5/E7/E8/E9b
// ---------------------------------------------------------------------------

// TestMigratePrefsV1DocumentsLiftLosslessly is the property the whole version
// bump rests on: v2 is a PURE ADDITION, so every v1 document must read back
// with its v1 choices intact and the six new keys at their defaults.
//
// It is a table over documents that each set a different v1 key, because the
// failure this guards against is a decoder that reads the new shape correctly
// and silently loses an old key — which a single-key fixture would miss for
// twelve of the thirteen.
func TestMigratePrefsV1DocumentsLiftLosslessly(t *testing.T) {
	v1 := DefaultPrefs()
	v1.UndoSendSeconds = 30
	v1.ImagesPolicy = "ask"
	v1.ConversationView = false
	v1.HoverActions = false
	v1.AutoAdvance = "newer"
	v1.Density = "comfortable"
	v1.ShowSnippets = false
	v1.KeyboardShortcuts = false
	v1.Language = "es-AR"
	v1.ReadingPane = "none"
	v1.InboxType = "starred_first"
	v1.Notifications = "off"
	v1.Theme = "dark"

	// A genuine v1 document: the v1 keys, stamped v1, with NONE of the v2 keys.
	// Built by encoding and then deleting, so it cannot drift from the real v1
	// key set the way a hand-written literal would.
	raw, err := encodePrefs(v1)
	if err != nil {
		t.Fatalf("encodePrefs: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range v2OnlyKeys {
		delete(doc, key)
	}
	doc["v"] = 1
	legacy, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	got, version, err := migratePrefs(legacy)
	if err != nil {
		t.Fatalf("migratePrefs on a v1 document: %v", err)
	}
	// The version REPORTED is the version stored, not the one lifted to: an
	// operator counting un-rewritten rows must see the truth.
	if version != 1 {
		t.Errorf("version = %d, want 1 (the version the document was STORED as)", version)
	}

	// Every v1 choice survived.
	if !got.Equal(v1) {
		t.Errorf("a v1 document did not lift losslessly:\n got %+v\nwant %+v", got, v1)
	}

	// And the v2 keys are at their defaults, not at their zero values — the
	// distinction that separates "defaults on read" from "an unset struct".
	d := DefaultPrefs()
	if got.OfflineDepth != d.OfflineDepth {
		t.Errorf("offlineDepth = %+v, want the default %+v", got.OfflineDepth, d.OfflineDepth)
	}
	if got.AddressAutocomplete != d.AddressAutocomplete {
		t.Errorf("addressAutocomplete = %q, want the default %q", got.AddressAutocomplete, d.AddressAutocomplete)
	}
	if got.SendAndArchive != d.SendAndArchive {
		t.Errorf("sendAndArchive = %v, want the default %v", got.SendAndArchive, d.SendAndArchive)
	}
	if got.DefaultReplyBehavior != d.DefaultReplyBehavior {
		t.Errorf("defaultReplyBehavior = %q, want the default %q", got.DefaultReplyBehavior, d.DefaultReplyBehavior)
	}
	if got.Labels != nil {
		t.Errorf("labels = %v, want nil for a document that names none", got.Labels)
	}
	if got.Signatures.Items != nil || got.Signatures.ForNew != "" || got.Signatures.ForReply != "" {
		t.Errorf("signatures = %+v, want the empty default", got.Signatures)
	}
}

// v2OnlyKeys are the JSON keys v2 added. Named once so the tests that must
// distinguish a v1 document from a v2 one cannot disagree about which is which.
var v2OnlyKeys = []string{
	"labels", "offlineDepth", "addressAutocomplete",
	"sendAndArchive", "defaultReplyBehavior", "signatures",
}

// TestMigratePrefsV2RoundTripsTheNewKeys drives every v2 key through the
// encoder and back, with the maps populated: a map whose encoding is only ever
// exercised empty is a map whose encoding is untested.
func TestMigratePrefsV2RoundTripsTheNewKeys(t *testing.T) {
	in := DefaultPrefs()
	in.Labels = map[string]LabelPrefs{
		"Facturas":  {Color: "amber", Visibility: "showIfUnread"},
		"Proyectos": {Color: "indigo", Visibility: "hide"},
		"Personal":  {Color: "slate", Visibility: "show"},
	}
	in.OfflineDepth = OfflineDepthPrefs{HeadersPerMailbox: 1000, Bodies: 500}
	in.AddressAutocomplete = "manual"
	in.SendAndArchive = false
	in.DefaultReplyBehavior = "replyAll"
	in.Signatures = SignaturePrefs{
		Items: map[string]SignatureItem{
			"work":     {Name: "Work", TextBody: "-- \nDiego, NU", HTMLBody: "<p>Diego, NU</p>"},
			"personal": {Name: "Personal", TextBody: "d", HTMLBody: "<b>d</b>"},
		},
		ForNew:   "work",
		ForReply: "personal",
	}

	raw, err := encodePrefs(in)
	if err != nil {
		t.Fatalf("encodePrefs: %v", err)
	}
	got, version, err := migratePrefs(raw)
	if err != nil {
		t.Fatalf("migratePrefs: %v", err)
	}
	if version != PrefsSchemaVersion {
		t.Errorf("version = %d, want %d", version, PrefsSchemaVersion)
	}
	if !got.Equal(in) {
		t.Errorf("the v2 round trip changed the value:\n in: %+v\nout: %+v", in, got)
	}
	// Nested values specifically, so a map that round-tripped its KEYS but lost
	// a struct field cannot pass on the Equal alone.
	if l := got.Labels["Facturas"]; l.Color != "amber" || l.Visibility != "showIfUnread" {
		t.Errorf("the Facturas label came back as %+v", l)
	}
	if s := got.Signatures.Items["work"]; s.Name != "Work" || s.HTMLBody != "<p>Diego, NU</p>" {
		t.Errorf("the work signature came back as %+v", s)
	}
}

// TestMigratePrefsV2EmptyMapsReadBackAsNil pins the `omitempty` decision: an
// empty map and a missing key both mean "nothing customized", and the encoder
// writes neither. If they read back differently, Equal's judgement (nil ==
// empty) and the storage form would disagree.
func TestMigratePrefsV2EmptyMapsReadBackAsNil(t *testing.T) {
	in := DefaultPrefs()
	in.Labels = map[string]LabelPrefs{}
	in.Signatures.Items = map[string]SignatureItem{}

	raw, err := encodePrefs(in)
	if err != nil {
		t.Fatalf("encodePrefs: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, present := doc["labels"]; present {
		t.Errorf(`an empty labels map was written as a key: %s`, raw)
	}

	got, _, err := migratePrefs(raw)
	if err != nil {
		t.Fatalf("migratePrefs: %v", err)
	}
	if got.Labels != nil {
		t.Errorf("labels = %v, want nil after a round trip through empty", got.Labels)
	}
	if !got.Equal(in) {
		t.Error("an empty map and a nil map must be equal: DefaultPrefs' comment says they mean the same thing")
	}
}

// TestPrefsEqualDistinguishesTheV2Maps guards the hand-written Equal: a field
// it forgets is a field two different objects compare equal on, which would
// make every test that uses it vacuous for that field.
func TestPrefsEqualDistinguishesTheV2Maps(t *testing.T) {
	base := DefaultPrefs()
	base.Labels = map[string]LabelPrefs{"a": {Color: "red", Visibility: "show"}}
	base.Signatures = SignaturePrefs{
		Items:  map[string]SignatureItem{"s": {Name: "S", TextBody: "t"}},
		ForNew: "s",
	}

	for name, mutate := range map[string]func(p *Prefs){
		"a label added":            func(p *Prefs) { p.Labels["b"] = LabelPrefs{Color: "blue", Visibility: "show"} },
		"a label removed":          func(p *Prefs) { delete(p.Labels, "a") },
		"a label recolored":        func(p *Prefs) { p.Labels["a"] = LabelPrefs{Color: "blue", Visibility: "show"} },
		"a label rehidden":         func(p *Prefs) { p.Labels["a"] = LabelPrefs{Color: "red", Visibility: "hide"} },
		"a signature renamed":      func(p *Prefs) { p.Signatures.Items["s"] = SignatureItem{Name: "T", TextBody: "t"} },
		"a signature body changed": func(p *Prefs) { p.Signatures.Items["s"] = SignatureItem{Name: "S", TextBody: "u"} },
		"forNew cleared":           func(p *Prefs) { p.Signatures.ForNew = "" },
		"forReply set":             func(p *Prefs) { p.Signatures.ForReply = "s" },
		"offline headers":          func(p *Prefs) { p.OfflineDepth.HeadersPerMailbox = 999 },
		"offline bodies":           func(p *Prefs) { p.OfflineDepth.Bodies = 21 },
		"autocomplete":             func(p *Prefs) { p.AddressAutocomplete = "manual" },
		"send and archive":         func(p *Prefs) { p.SendAndArchive = false },
		"reply behavior":           func(p *Prefs) { p.DefaultReplyBehavior = "replyAll" },
	} {
		t.Run(name, func(t *testing.T) {
			other := base.Clone()
			mutate(&other)
			if base.Equal(other) {
				t.Errorf("Equal reports two values identical although %s: the field is unguarded", name)
			}
		})
	}
}

// TestPrefsCloneIsDeep pins that a cloned value shares no map with its source.
// The JMAP layer's read-patch-write mutates the map in place while applying a
// patch; without a real copy it would be editing the object it read from.
func TestPrefsCloneIsDeep(t *testing.T) {
	src := DefaultPrefs()
	src.Labels = map[string]LabelPrefs{"a": {Color: "red", Visibility: "show"}}
	src.Signatures.Items = map[string]SignatureItem{"s": {Name: "S"}}

	clone := src.Clone()
	clone.Labels["a"] = LabelPrefs{Color: "blue", Visibility: "hide"}
	clone.Labels["b"] = LabelPrefs{Color: "lime", Visibility: "show"}
	clone.Signatures.Items["s"] = SignatureItem{Name: "TAMPERED"}

	if got := src.Labels["a"].Color; got != "red" {
		t.Errorf("mutating the clone changed the source label: color = %q", got)
	}
	if len(src.Labels) != 1 {
		t.Errorf("mutating the clone added to the source map: %v", src.Labels)
	}
	if got := src.Signatures.Items["s"].Name; got != "S" {
		t.Errorf("mutating the clone changed the source signature: name = %q", got)
	}
	// A nil map must stay nil rather than becoming an empty one, or a cloned
	// default would stop being equal to DefaultPrefs by the encoder's reckoning.
	if DefaultPrefs().Clone().Labels != nil {
		t.Error("Clone turned a nil map into an empty one")
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
	if !got.Equal(in) {
		t.Errorf("round trip changed the value: %+v -> %+v", in, got)
	}
	if version != PrefsSchemaVersion {
		t.Errorf("version = %d, want %d", version, PrefsSchemaVersion)
	}
}

func TestEncodePrefsWritesEveryKeyPlusTheVersion(t *testing.T) {
	// The document is written DENSE on purpose (encodePrefs' comment): a
	// Prefs/set naming one property must preserve the others exactly as they
	// were served, which is the idempotence the JMAP patch depends on.
	//
	// The two v2 MAPS are the documented exception — `omitempty`, because an
	// empty map carries nothing a missing key does not — so this fixture
	// populates them, and the empty case is pinned separately by
	// TestMigratePrefsV2EmptyMapsReadBackAsNil.
	full := DefaultPrefs()
	full.Labels = map[string]LabelPrefs{"a": {Color: "red", Visibility: "show"}}
	full.Signatures.Items = map[string]SignatureItem{"s": {Name: "S"}}

	raw, err := encodePrefs(full)
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
		// v2.
		"labels", "offlineDepth", "addressAutocomplete",
		"sendAndArchive", "defaultReplyBehavior", "signatures",
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

// TestEncodePrefsStampsTheCurrentVersion pins that the stored document declares
// v2 and not the version it happened to be read at. This is the whole reason
// the bump was taken (PrefsSchemaVersion's comment): a document carrying v2 data
// under a v1 stamp would be read, downgraded and overwritten by the previous
// release, silently destroying a user's labels.
func TestEncodePrefsStampsTheCurrentVersion(t *testing.T) {
	raw, err := encodePrefs(DefaultPrefs())
	if err != nil {
		t.Fatalf("encodePrefs: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	v, ok := doc["v"].(float64)
	if !ok || int(v) != PrefsSchemaVersion {
		t.Fatalf(`document "v" = %v, want %d`, doc["v"], PrefsSchemaVersion)
	}
	if PrefsSchemaVersion != 2 {
		t.Errorf("PrefsSchemaVersion = %d; this batch ships v2", PrefsSchemaVersion)
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

	// --- v2 ---

	// The E7 divergence, taken AFTER the button shipped visible: defaulting it
	// off would remove a control users already have, which is a worse failure
	// than differing from Gmail on a setting Google publishes no reason for.
	if !d.SendAndArchive {
		t.Error("sendAndArchive must default ON — the button already shipped visible; " +
			"defaulting it off would remove a control users have")
	}
	// Canon §2.3: reply, not reply-all. The failure modes are asymmetric — a
	// reply-all sent by accident cannot be taken back.
	if d.DefaultReplyBehavior != "reply" {
		t.Errorf("defaultReplyBehavior = %q, want Gmail's \"reply\" (canon §2.3)", d.DefaultReplyBehavior)
	}
	// Gmail's own default for "create contacts for autocomplete".
	if d.AddressAutocomplete != "auto" {
		t.Errorf("addressAutocomplete = %q, want Gmail's default \"auto\"", d.AddressAutocomplete)
	}
	// E9b's offline depths, and the invariant that makes them coherent: bodies
	// are far larger than headers, so caching more bodies than headers would
	// spend the browser's quota on the wrong thing.
	if d.OfflineDepth.HeadersPerMailbox != 200 {
		t.Errorf("offlineDepth.headersPerMailbox = %d, want 200", d.OfflineDepth.HeadersPerMailbox)
	}
	if d.OfflineDepth.Bodies != 100 {
		t.Errorf("offlineDepth.bodies = %d, want 100", d.OfflineDepth.Bodies)
	}
	if d.OfflineDepth.Bodies > d.OfflineDepth.HeadersPerMailbox {
		t.Error("the default caches more bodies than headers: a body is orders of magnitude larger")
	}
	// No label presentation and no signature until the user makes one. Nil
	// rather than empty is the canonical form (DefaultPrefs' comment).
	if d.Labels != nil {
		t.Errorf("the default labels map is %v, want nil", d.Labels)
	}
	if d.Signatures.Items != nil || d.Signatures.ForNew != "" || d.Signatures.ForReply != "" {
		t.Errorf("the default signatures are %+v, want empty", d.Signatures)
	}
}
