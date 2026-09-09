package mail_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap/mail"
)

// folderVisibility — the one key schema v3 adds — driven through the REAL
// dispatch engine against a real PostgreSQL store, exactly as prefs_v2_test.go
// drives the v2 keys. The helpers are that file's (same package): prefsV2Object,
// prefsV2Update, prefsV2Refusal, prefsV2Sub, prefsV2RefusedProperties.
//
// What is worth proving here beyond "the map stores strings":
//
//   - the map holds ONLY explicit choices. An absent key is not "hidden" and not
//     "shown"; it is "the user said nothing", and what happens then is the
//     client's rail policy. Removing an entry with null is therefore a
//     DIFFERENT answer from setting it to "hide", and the two must not collapse.
//   - RFC 6901 escaping on a key that really does contain a slash. Dovecot hands
//     users folder names like "Sync issues/Conflicts" on its
//     own, so this is the ordinary case for this key rather than an adversarial
//     one — and the pointer it produces is the only shape in which a one-level
//     patch can address it at all.
//   - the two caps, each refused with a per-key §5.3 detail rather than
//     silently truncated.
//
// Requires MOOV_TEST_DATABASE_URL; skips cleanly without it.

// conflictsFolder is a name Dovecot produces by itself, with a slash inside it.
// Named once so every test that must escape it cannot disagree about the
// spelling.
const conflictsFolder = "Sync issues/Conflicts"

// conflictsPointer is that name as an RFC 6901 token: the slash becomes "~1",
// which is what keeps "folderVisibility/<name>" one level deep.
const conflictsPointer = "Sync issues~1Conflicts"

// prefsV3Map fetches the folderVisibility object off the served singleton.
func prefsV3Map(t *testing.T, f *fixture) map[string]any {
	t.Helper()
	return prefsV2Sub(t, prefsV2Object(t, f), "folderVisibility")
}

// ---------------------------------------------------------------------------
// the default, on the wire
// ---------------------------------------------------------------------------

// TestPrefsV3DefaultIsAnEmptyObject pins the shape an untouched account meets.
//
// `{}` and not null, for the reason prefsFolderVisibilityValue states: a client
// reading null would have to decide whether it meant "no choices" or "unknown",
// and a client patching into it would have to create the container first. For
// this key that is the COMMON path, not an edge — most accounts will never
// curate the rail — so the default shape must be the patchable one.
func TestPrefsV3DefaultIsAnEmptyObject(t *testing.T) {
	f := newFixture(t)
	obj := prefsV2Object(t, f)

	raw, ok := obj["folderVisibility"]
	if !ok {
		t.Fatal("the served object is missing folderVisibility although schemaVersion says 3")
	}
	if raw == nil {
		t.Fatal("folderVisibility is null; it must be {} so a client can patch into it " +
			"without creating the container first")
	}
	if got := prefsV3Map(t, f); len(got) != 0 {
		t.Errorf("folderVisibility = %v, want an empty object for an untouched account", got)
	}
}

// TestPrefsV3SchemaVersionIsThree pins the number the vendor capability
// advertises against the number the store stamps. They are the same constant
// (mail.PrefsSchemaVersion is store.PrefsSchemaVersion), and this is what would
// fail if someone re-declared one of them.
func TestPrefsV3SchemaVersionIsThree(t *testing.T) {
	if mail.PrefsSchemaVersion != 3 {
		t.Errorf("PrefsSchemaVersion = %d, want 3 (folderVisibility)", mail.PrefsSchemaVersion)
	}
}

// ---------------------------------------------------------------------------
// writing, whole and per key
// ---------------------------------------------------------------------------

// TestPrefsV3WholeMapReplacement drives the simple shape: a whole-value patch,
// read back through /get.
func TestPrefsV3WholeMapReplacement(t *testing.T) {
	f := newFixture(t)

	if got := prefsV2Update(t, f,
		`{"folderVisibility":{"Archivo":"hide","Notas":"show","Borradores":"showIfUnread"}}`); got["notUpdated"] != nil {
		t.Fatalf("a whole-map replacement was refused: %v", got["notUpdated"])
	}
	got := prefsV3Map(t, f)
	for name, want := range map[string]string{
		"Archivo": "hide", "Notas": "show", "Borradores": "showIfUnread",
	} {
		if got[name] != want {
			t.Errorf("folderVisibility[%q] = %v, want %q", name, got[name], want)
		}
	}
	if len(got) != 3 {
		t.Errorf("folderVisibility = %v, want exactly the three named", got)
	}

	// A second replacement REPLACES rather than merges — that is what a
	// whole-value patch means, and the per-key pointer is the shape for merging.
	if resp := prefsV2Update(t, f, `{"folderVisibility":{"Spam":"hide"}}`); resp["notUpdated"] != nil {
		t.Fatalf("the second replacement was refused: %v", resp["notUpdated"])
	}
	got = prefsV3Map(t, f)
	if len(got) != 1 || got["Spam"] != "hide" {
		t.Errorf("folderVisibility = %v, want only the replacement", got)
	}
}

// TestPrefsV3PointerPatchWithSlashInTheName is the test this key exists to
// have.
//
// A Dovecot mailbox called "Sync issues/Conflicts" is a name
// the SERVER hands the user, not one they typed. Addressing it with a §5.3
// pointer requires RFC 6901 §3 escaping — "~1" for the slash — or the pointer
// is three tokens deep and this server refuses it as invalidPatch.
//
// Both halves are exercised: the escaped pointer must WORK, and the unescaped
// one must be REFUSED rather than silently creating a folder called "Sync
// issues" with a stray sub-token.
func TestPrefsV3PointerPatchWithSlashInTheName(t *testing.T) {
	f := newFixture(t)

	patch := fmt.Sprintf(`{"folderVisibility/%s":"showIfUnread"}`, conflictsPointer)
	if got := prefsV2Update(t, f, patch); got["notUpdated"] != nil {
		t.Fatalf("an RFC 6901-escaped pointer was refused: %v", got["notUpdated"])
	}
	got := prefsV3Map(t, f)
	if got[conflictsFolder] != "showIfUnread" {
		t.Fatalf("folderVisibility = %v, want the key %q — the ~1 escape must decode to a slash",
			got, conflictsFolder)
	}
	// And nothing was created under the truncated name.
	if _, stray := got["Sync issues"]; stray {
		t.Errorf("the escape decoded into two keys: %v", got)
	}

	// The UNESCAPED pointer is three tokens ("folderVisibility", "Sync
	// issues", "Conflicts") and must be refused as invalidPatch, per
	// §5.3's "the patch could not be applied".
	resp := prefsV2Update(t, f,
		`{"folderVisibility/Sync issues/Conflicts":"hide"}`)
	notUpdated, ok := resp["notUpdated"].(map[string]any)
	if !ok {
		t.Fatalf("an unescaped two-slash pointer was ACCEPTED: %v", resp)
	}
	entry := prefsSetError(t, notUpdated, "singleton")
	if entry["type"] != "invalidPatch" {
		t.Errorf("type = %v, want invalidPatch for a pointer deeper than one level", entry["type"])
	}
	// The earlier, correctly-escaped choice must be untouched by the refusal.
	if prefsV3Map(t, f)[conflictsFolder] != "showIfUnread" {
		t.Error("a refused patch changed the stored value")
	}
}

// TestPrefsV3RemovingAnEntryIsNotHiding pins the distinction the whole design
// rests on.
//
// §5.3: "If null, set to the default value if specified for the property;
// otherwise, remove the property from the patched object." For a member of this
// map that means FORGET the user's choice — the folder returns to whatever the
// client's own rail policy says — which is a different answer from "hide". A
// client that sent null meaning "hide" would watch the folder come back, so the
// two must be observably different, and they are: one leaves a key, the other
// leaves none.
func TestPrefsV3RemovingAnEntryIsNotHiding(t *testing.T) {
	f := newFixture(t)

	if got := prefsV2Update(t, f,
		`{"folderVisibility":{"Archivo":"hide","Notas":"show"}}`); got["notUpdated"] != nil {
		t.Fatalf("seeding was refused: %v", got["notUpdated"])
	}
	// Set one, forget the other, in ONE patch — the shape a settings screen
	// actually sends.
	if got := prefsV2Update(t, f,
		`{"folderVisibility/Archivo":"showIfUnread","folderVisibility/Notas":null}`); got["notUpdated"] != nil {
		t.Fatalf("a mixed set-and-remove patch was refused: %v", got["notUpdated"])
	}

	got := prefsV3Map(t, f)
	if got["Archivo"] != "showIfUnread" {
		t.Errorf("Archivo = %v, want the pointer-patched value", got["Archivo"])
	}
	if _, present := got["Notas"]; present {
		t.Errorf("Notas is still %v: null must REMOVE the entry, not set it to a value", got["Notas"])
	}
	if len(got) != 1 {
		t.Errorf("folderVisibility = %v, want only Archivo", got)
	}
	// The removal must not have left "hide" behind under another spelling: the
	// user has no choice recorded for Notas at all, which is what lets the
	// client decide.
	if got["Notas"] == "hide" {
		t.Error("removing an entry recorded it as hidden: the two answers collapsed")
	}
}

// TestPrefsV3WholeReplacementComposesWithPointerEdits pins the ORDER
// applyPrefsPatch defines: the whole-value replacement lands first, then the
// per-entry pointers apply on top of it.
//
// Without the deferral this would be nondeterministic — Go's map iteration is
// random — so a patch carrying both would mean different things on different
// runs. It is the same property applyLabelsPatch has, tested here because the
// two maps share the mechanism but not the code path.
func TestPrefsV3WholeReplacementComposesWithPointerEdits(t *testing.T) {
	f := newFixture(t)

	// Run it several times: a nondeterministic order would show up as a flake,
	// and a flake in one run of the suite is a bug that ships.
	for i := range 8 {
		resp := prefsV2Update(t, f,
			`{"folderVisibility":{"A":"hide","B":"hide"},`+
				`"folderVisibility/A":"show","folderVisibility/C":"showIfUnread"}`)
		if resp["notUpdated"] != nil {
			t.Fatalf("run %d: the composed patch was refused: %v", i, resp["notUpdated"])
		}
		got := prefsV3Map(t, f)
		if got["A"] != "show" || got["B"] != "hide" || got["C"] != "showIfUnread" || len(got) != 3 {
			t.Fatalf("run %d: folderVisibility = %v; the pointer edits must land ON TOP of the "+
				"whole-value replacement", i, got)
		}
	}
}

// TestPrefsV3NullClearsTheWholeMap pins §5.3's other null: on the PROPERTY
// itself it means "set to the default value", and this property's default is
// the absence of any choice.
func TestPrefsV3NullClearsTheWholeMap(t *testing.T) {
	f := newFixture(t)

	if got := prefsV2Update(t, f, `{"folderVisibility":{"Archivo":"hide"}}`); got["notUpdated"] != nil {
		t.Fatalf("seeding was refused: %v", got["notUpdated"])
	}
	if got := prefsV2Update(t, f, `{"folderVisibility":null}`); got["notUpdated"] != nil {
		t.Fatalf("clearing with null was refused: %v", got["notUpdated"])
	}
	if got := prefsV3Map(t, f); len(got) != 0 {
		t.Errorf("folderVisibility = %v, want empty after a null on the property", got)
	}
}

// TestPrefsV3RoundTripsThroughTheStore proves the value survives storage rather
// than only the handler's in-memory patch: write, read back, and compare the
// exact map — including the awkward keys.
func TestPrefsV3RoundTripsThroughTheStore(t *testing.T) {
	f := newFixture(t)

	want := map[string]string{
		"Archivo":         "hide",
		conflictsFolder:   "showIfUnread",
		"Ñandú/Ünïcode~2": "show", // a tilde AND a slash: both RFC 6901 escapes.
		"INBOX":           "show",
	}
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if got := prefsV2Update(t, f, `{"folderVisibility":`+string(body)+`}`); got["notUpdated"] != nil {
		t.Fatalf("the write was refused: %v", got["notUpdated"])
	}

	got := prefsV3Map(t, f)
	if len(got) != len(want) {
		t.Fatalf("folderVisibility has %d entries, want %d: %v", len(got), len(want), got)
	}
	for name, v := range want {
		if got[name] != v {
			t.Errorf("folderVisibility[%q] = %v, want %q", name, got[name], v)
		}
	}

	// Idempotence: the same patch twice leaves the same object. It is the
	// property the read-patch-write shape gives for free, and the one a
	// settings screen retrying a save depends on.
	if resp := prefsV2Update(t, f, `{"folderVisibility":`+string(body)+`}`); resp["notUpdated"] != nil {
		t.Fatalf("the repeated write was refused: %v", resp["notUpdated"])
	}
	if again := prefsV3Map(t, f); len(again) != len(want) {
		t.Errorf("the repeated write changed the object: %v", again)
	}
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

// TestPrefsV3RefusesAnUnknownVisibility pins that a value outside the closed
// domain is refused with a §5.3 invalidProperties naming the OFFENDING POINTER,
// not the bare property.
//
// The pointer matters: a settings screen highlights what invalidProperties
// names, and naming "folderVisibility" when one folder of two hundred is wrong
// tells the user nothing.
func TestPrefsV3RefusesAnUnknownVisibility(t *testing.T) {
	f := newFixture(t)

	entry := prefsV2Refusal(t, f, `{"folderVisibility/Archivo":"collapsed"}`)
	props := prefsV2RefusedProperties(t, entry)
	if len(props) != 1 || props[0] != "folderVisibility/Archivo" {
		t.Errorf("properties = %v, want [\"folderVisibility/Archivo\"] — the pointer the client sent", props)
	}
	// The description must carry the domain, or the client cannot show what to
	// choose instead.
	desc, _ := entry["description"].(string)
	for _, want := range []string{"show", "hide", "showIfUnread", "collapsed"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the refusal does not mention %q: %q", want, desc)
		}
	}
	// Nothing was stored.
	if got := prefsV3Map(t, f); len(got) != 0 {
		t.Errorf("a refused patch stored something: %v", got)
	}

	// The same refusal through a whole-map write, so neither spelling can
	// accept what the other refuses.
	entry = prefsV2Refusal(t, f, `{"folderVisibility":{"Archivo":"collapsed"}}`)
	if props := prefsV2RefusedProperties(t, entry); len(props) != 1 || props[0] != "folderVisibility" {
		t.Errorf("properties = %v, want [\"folderVisibility\"] for a whole-map write", props)
	}

	// And a value of the wrong TYPE, which is the other way a client gets this
	// wrong — an object where a string belongs.
	entry = prefsV2Refusal(t, f, `{"folderVisibility/Archivo":{"visibility":"hide"}}`)
	if entry["type"] != "invalidProperties" {
		t.Errorf("type = %v, want invalidProperties for a non-string visibility", entry["type"])
	}
}

// TestPrefsV3RefusesTheOffendingPointerEscaped pins that a refusal on a key
// containing a slash names the ESCAPED pointer — the exact string the client
// sent — and not the decoded name it never wrote.
//
// This is what makes the error usable: a settings screen looks up the failing
// key in its OWN request, and a name spelled differently there matches nothing.
func TestPrefsV3RefusesTheOffendingPointerEscaped(t *testing.T) {
	f := newFixture(t)

	patch := fmt.Sprintf(`{"folderVisibility/%s":"collapsed"}`, conflictsPointer)
	entry := prefsV2Refusal(t, f, patch)
	props := prefsV2RefusedProperties(t, entry)
	want := "folderVisibility/" + conflictsPointer
	if len(props) != 1 || props[0] != want {
		t.Errorf("properties = %v, want [%q] — the pointer as the client wrote it, re-escaped",
			props, want)
	}
	// Specifically NOT the decoded form, which would be a pointer the client
	// cannot find in its request and which reads as a different, deeper path.
	if len(props) == 1 && props[0] == "folderVisibility/"+conflictsFolder {
		t.Error("the refusal names the DECODED key: a client cannot match that against its own patch")
	}
}

// TestPrefsV3RefusesAnOversizeFolderName pins the 255-byte key cap.
//
// 255 is the length a mailbox name can actually have (Dovecot's Maildir++
// layout puts it in a filesystem path component), so a longer key names no
// mailbox that exists and would only let this map hold content.
func TestPrefsV3RefusesAnOversizeFolderName(t *testing.T) {
	f := newFixture(t)

	// Exactly at the cap: accepted. The boundary is tested from both sides, or
	// an off-by-one would pass unnoticed.
	atCap := strings.Repeat("a", mail.MaxFolderNameBytes())
	if got := prefsV2Update(t, f,
		`{"folderVisibility":{"`+atCap+`":"hide"}}`); got["notUpdated"] != nil {
		t.Fatalf("a name of exactly %d bytes was refused: %v",
			mail.MaxFolderNameBytes(), got["notUpdated"])
	}

	over := strings.Repeat("a", mail.MaxFolderNameBytes()+1)
	entry := prefsV2Refusal(t, f, `{"folderVisibility/`+over+`":"hide"}`)
	if entry["type"] != "invalidProperties" {
		t.Errorf("type = %v, want invalidProperties", entry["type"])
	}
	desc, _ := entry["description"].(string)
	if !strings.Contains(desc, fmt.Sprint(mail.MaxFolderNameBytes())) {
		t.Errorf("the refusal does not state the limit: %q", desc)
	}
	// The oversize name must not be echoed back whole — an error is not a way
	// to make the server repeat a kilobyte at you.
	if strings.Contains(desc, over) {
		t.Error("the refusal echoes the whole oversize name back")
	}

	// The cap is measured in BYTES, matching the filesystem's own unit: a name
	// of 128 two-byte characters is 256 bytes and must be refused although it
	// is only 128 runes.
	wide := strings.Repeat("ñ", 128)
	if len([]rune(wide)) > mail.MaxFolderNameBytes() {
		t.Fatal("the fixture is not a byte-vs-rune case")
	}
	entry = prefsV2Refusal(t, f, `{"folderVisibility":{"`+wide+`":"hide"}}`)
	if entry["type"] != "invalidProperties" {
		t.Errorf("a %d-byte / %d-rune name was accepted: the cap is not measured in bytes",
			len(wide), len([]rune(wide)))
	}
}

// TestPrefsV3RefusesAnEmptyFolderName pins that a key which names no mailbox is
// refused rather than stored. An empty key is not a folder; it is a way to put
// a value in the document that nothing will ever read.
func TestPrefsV3RefusesAnEmptyFolderName(t *testing.T) {
	f := newFixture(t)
	for _, patch := range []string{
		`{"folderVisibility":{"":"hide"}}`,
		`{"folderVisibility":{"   ":"hide"}}`,
	} {
		entry := prefsV2Refusal(t, f, patch)
		if entry["type"] != "invalidProperties" {
			t.Errorf("%s: type = %v, want invalidProperties", patch, entry["type"])
		}
	}
}

// TestPrefsV3RefusesOverTheEntryCap pins the 200-entry cap, checked on the
// RESULT of the patch.
//
// The cap is not a protocol fact the way the label cap is (there is no Maildir
// ceiling on mailboxes); it bounds what a user can plausibly have expressed an
// opinion about, and it defends a column every session read pulls whole.
func TestPrefsV3RefusesOverTheEntryCap(t *testing.T) {
	f := newFixture(t)
	limit := mail.MaxFolderVisibility()

	// Exactly at the cap: accepted.
	full := make(map[string]string, limit)
	for i := range limit {
		full[fmt.Sprintf("Carpeta %03d", i)] = "hide"
	}
	body, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if got := prefsV2Update(t, f, `{"folderVisibility":`+string(body)+`}`); got["notUpdated"] != nil {
		t.Fatalf("a map of exactly %d entries was refused: %v", limit, got["notUpdated"])
	}
	if got := prefsV3Map(t, f); len(got) != limit {
		t.Fatalf("stored %d entries, want %d", len(got), limit)
	}

	// One more, by POINTER, so the check is exercised against the result rather
	// than against the patch's own size.
	entry := prefsV2Refusal(t, f, `{"folderVisibility/Una más":"hide"}`)
	props := prefsV2RefusedProperties(t, entry)
	if len(props) != 1 || props[0] != "folderVisibility" {
		t.Errorf("properties = %v, want [\"folderVisibility\"] — the cap is a property-level fact", props)
	}
	desc, _ := entry["description"].(string)
	if !strings.Contains(desc, fmt.Sprint(limit)) {
		t.Errorf("the refusal does not state the cap: %q", desc)
	}
	// The account is unchanged: a refused patch stores nothing.
	if got := prefsV3Map(t, f); len(got) != limit {
		t.Errorf("the refused patch changed the stored map: %d entries", len(got))
	}

	// A patch that FORGETS one and adds one is legal at the cap — which is why
	// the check runs on the result and not per key. Checking per key would
	// refuse this at the addition.
	if got := prefsV2Update(t, f,
		`{"folderVisibility/Carpeta 000":null,"folderVisibility/Una más":"show"}`); got["notUpdated"] != nil {
		t.Fatalf("a swap at the cap was refused: %v", got["notUpdated"])
	}
	after := prefsV3Map(t, f)
	if len(after) != limit {
		t.Errorf("after the swap there are %d entries, want %d", len(after), limit)
	}
	if after["Una más"] != "show" {
		t.Errorf("the swap did not add the new folder: %v", after["Una más"])
	}
	if _, present := after["Carpeta 000"]; present {
		t.Error("the swap did not forget the old folder")
	}
}

// TestPrefsV3RefusesAnUnknownNestedShape pins that folderVisibility is a map of
// STRINGS and nothing else — an array, a number, a nested object are all
// refused rather than coerced.
//
// §5.3's rule is the same one the top-level keys get: refusing loudly is the
// point, because a silently dropped preference is a control the user watched
// move and that changed nothing.
func TestPrefsV3RefusesAnUnknownNestedShape(t *testing.T) {
	f := newFixture(t)

	for name, patch := range map[string]string{
		"array":         `{"folderVisibility":["Archivo"]}`,
		"scalar":        `{"folderVisibility":"hide"}`,
		"number member": `{"folderVisibility":{"Archivo":3}}`,
		"bool member":   `{"folderVisibility/Archivo":true}`,
		"nested member": `{"folderVisibility":{"Archivo":{"show":true}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			entry := prefsV2Refusal(t, f, patch)
			if entry["type"] != "invalidProperties" {
				t.Errorf("type = %v, want invalidProperties", entry["type"])
			}
			if got := prefsV3Map(t, f); len(got) != 0 {
				t.Errorf("a refused patch stored something: %v", got)
			}
		})
	}
}

// TestPrefsV3ReportsEveryOffenceAtOnce pins §5.3's "lists ALL the properties
// that were invalid" across the new key and an old one, so a settings screen
// does not have to submit twice to discover two mistakes.
func TestPrefsV3ReportsEveryOffenceAtOnce(t *testing.T) {
	f := newFixture(t)

	entry := prefsV2Refusal(t, f,
		`{"folderVisibility/Archivo":"collapsed","theme":"neon","density":"roomy"}`)
	props := prefsV2RefusedProperties(t, entry)
	want := map[string]bool{"folderVisibility/Archivo": true, "theme": true, "density": true}
	if len(props) != len(want) {
		t.Fatalf("properties = %v, want all three offenses at once", props)
	}
	for _, p := range props {
		if !want[p] {
			t.Errorf("properties includes %q, which is not one of the three offenses", p)
		}
	}
}

// TestPrefsV3IsFilterableByProperties pins that the new key is accepted by
// /get's `properties` filter — RFC 8620 §5.1 makes an unlisted property an
// invalidArguments, so a key served but absent from the set would refuse a
// client that filtered on exactly what it received.
func TestPrefsV3IsFilterableByProperties(t *testing.T) {
	f := newFixture(t)

	resp := prefsCall(t, f, "Prefs/get",
		`{"accountId":"`+f.accountID()+`","ids":null,"properties":["folderVisibility"]}`)
	obj := firstObject(t, resp, 0)
	if _, ok := obj["folderVisibility"]; !ok {
		t.Errorf("filtering on folderVisibility returned an object without it: %v", obj)
	}
	// id is always returned, §5.1, filter or no filter.
	if obj["id"] != "singleton" {
		t.Errorf("id = %v, want the singleton", obj["id"])
	}
	// And nothing else came along.
	if len(obj) != 2 {
		t.Errorf("the filtered object has %d keys, want id + folderVisibility: %v", len(obj), obj)
	}
}

// TestPrefsV3SurvivesAPatchOfOtherProperties pins the idempotence the dense
// write buys: saving an unrelated setting must not disturb the folder rail.
//
// This is the failure a sparse-write store would have, and the one the L3
// settings screen would hit constantly — every save names one control.
func TestPrefsV3SurvivesAPatchOfOtherProperties(t *testing.T) {
	f := newFixture(t)

	if got := prefsV2Update(t, f,
		`{"folderVisibility":{"Archivo":"hide","`+conflictsFolder+`":"showIfUnread"}}`); got["notUpdated"] != nil {
		t.Fatalf("seeding was refused: %v", got["notUpdated"])
	}
	if got := prefsV2Update(t, f, `{"theme":"dark","density":"compact"}`); got["notUpdated"] != nil {
		t.Fatalf("an unrelated patch was refused: %v", got["notUpdated"])
	}

	obj := prefsV2Object(t, f)
	if obj["theme"] != "dark" {
		t.Errorf("theme = %v, want the patched value", obj["theme"])
	}
	got := prefsV2Sub(t, obj, "folderVisibility")
	if got["Archivo"] != "hide" || got[conflictsFolder] != "showIfUnread" || len(got) != 2 {
		t.Errorf("folderVisibility = %v, want both entries untouched by an unrelated save", got)
	}
}
