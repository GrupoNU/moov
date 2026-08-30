package mail_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap/mail"
)

// The v2 preference keys — labels, offlineDepth, addressAutocomplete,
// sendAndArchive, defaultReplyBehavior and signatures — driven through the REAL
// dispatch engine against a real PostgreSQL store, exactly as prefs_test.go
// drives the v1 ones.
//
// The properties worth proving here are the ones the v1 tests could not reach,
// because v1 had no structured property: patching INTO a map, the interaction
// between a whole-value replacement and a per-entry edit in one patch, the
// caps that have a reason behind them (the durable keyword ceiling, the
// signature byte budget), referential integrity between forNew and items, and
// the fact that a signature's HTML is sanitized on the way in.
//
// Requires MOOV_TEST_DATABASE_URL; skips cleanly without it.

// prefsV2Object fetches the singleton.
func prefsV2Object(t *testing.T, f *fixture) map[string]any {
	t.Helper()
	return firstObject(t, prefsCall(t, f, "Prefs/get", `{"accountId":"`+f.accountID()+`","ids":null}`), 0)
}

// prefsV2Update sends a patch and returns the raw response.
func prefsV2Update(t *testing.T, f *fixture, patch string) map[string]any {
	t.Helper()
	return prefsCall(t, f, "Prefs/set",
		`{"accountId":"`+f.accountID()+`","update":{"singleton":`+patch+`}}`)
}

// prefsV2Refusal sends a patch that MUST be refused and returns the SetError.
func prefsV2Refusal(t *testing.T, f *fixture, patch string) map[string]any {
	t.Helper()
	resp := prefsV2Update(t, f, patch)
	notUpdated, ok := resp["notUpdated"].(map[string]any)
	if !ok {
		t.Fatalf("the patch was ACCEPTED but should have been refused: %s\nresp=%v", patch, resp)
	}
	return prefsSetError(t, notUpdated, "singleton")
}

// prefsV2Sub is a checked accessor for a nested object on the served singleton.
func prefsV2Sub(t *testing.T, obj map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := obj[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want an object", key, obj[key])
	}
	return v
}

// prefsV2RefusedProperties is the sorted §5.3 invalidProperties list.
func prefsV2RefusedProperties(t *testing.T, entry map[string]any) []string {
	t.Helper()
	if entry["type"] != "invalidProperties" {
		t.Fatalf("type = %v, want invalidProperties (RFC 8620 §5.3)", entry["type"])
	}
	raw, ok := entry["properties"].([]any)
	if !ok {
		t.Fatalf("properties = %v, want the list of offending keys", entry["properties"])
	}
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		s, _ := p.(string)
		out = append(out, s)
	}
	return out
}

// ---------------------------------------------------------------------------
// the defaults, on the wire
// ---------------------------------------------------------------------------

// TestPrefsV2DefaultsAreServed pins that an account which has never saved
// anything still gets a COMPLETE v2 object — the same no-backfill property the
// v1 keys have, extended to keys added after rows already existed.
func TestPrefsV2DefaultsAreServed(t *testing.T) {
	f := newFixture(t)
	obj := prefsV2Object(t, f)

	for _, name := range []string{
		"labels", "offlineDepth", "addressAutocomplete",
		"sendAndArchive", "defaultReplyBehavior", "signatures",
	} {
		if _, ok := obj[name]; !ok {
			t.Errorf("the served object is missing the v2 property %q", name)
		}
	}

	// An empty labels map renders as {} and NOT as null: a client patching into
	// it must not have to create the container first.
	labels := prefsV2Sub(t, obj, "labels")
	if len(labels) != 0 {
		t.Errorf("labels = %v, want an empty object for an untouched account", labels)
	}

	depth := prefsV2Sub(t, obj, "offlineDepth")
	if depth["headersPerMailbox"] != float64(200) {
		t.Errorf("offlineDepth.headersPerMailbox = %v, want 200", depth["headersPerMailbox"])
	}
	if depth["bodies"] != float64(100) {
		t.Errorf("offlineDepth.bodies = %v, want 100", depth["bodies"])
	}

	if obj["addressAutocomplete"] != "auto" {
		t.Errorf("addressAutocomplete = %v, want Gmail's default \"auto\"", obj["addressAutocomplete"])
	}
	// The registered divergence: the button already shipped visible, so
	// defaulting it off would REMOVE a control users have.
	if obj["sendAndArchive"] != true {
		t.Error("sendAndArchive must default true — the Send & Archive button already shipped visible")
	}
	if obj["defaultReplyBehavior"] != "reply" {
		t.Errorf("defaultReplyBehavior = %v, want \"reply\" (canon §2.3)", obj["defaultReplyBehavior"])
	}

	sigs := prefsV2Sub(t, obj, "signatures")
	items, ok := sigs["items"].(map[string]any)
	if !ok || len(items) != 0 {
		t.Errorf("signatures.items = %v, want an empty object", sigs["items"])
	}
	// null, not "": the precedence rule's base case is "fall back to the
	// Identity's own signature", and null is how the wire says "none selected".
	if sigs["forNew"] != nil || sigs["forReply"] != nil {
		t.Errorf("signatures.forNew/forReply = %v/%v, want null", sigs["forNew"], sigs["forReply"])
	}
}

// TestPrefsV2SurvivesAV1Account is the migration acceptance at the WIRE level:
// an account whose row was written under v1 must serve a complete v2 object,
// with its v1 choices intact and the new keys defaulted.
//
// The v1 row is seeded through the store's own writer and then rewritten in
// place to look like v1 — going around the Go API, because a v1 document is
// unreachable through it once the build ships v2, and that is precisely the
// state the chain exists to handle.
func TestPrefsV2SurvivesAV1Account(t *testing.T) {
	f := newFixture(t)

	// Save something under the current build, then downgrade the stored
	// document to a genuine v1: v1 keys only, stamped 1.
	prefsV2Update(t, f, `{"theme":"dark","density":"compact"}`)
	if _, err := f.store.Pool().Exec(f.ctx, `
		UPDATE account_prefs
		   SET prefs = jsonb_build_object(
		           'v', 1, 'theme', 'dark', 'density', 'compact',
		           'undoSendSeconds', 30, 'keyboardShortcuts', false),
		       schema_version = 1
		 WHERE account_id = $1`, f.account.ID); err != nil {
		t.Fatalf("downgrading the stored document to v1: %v", err)
	}

	obj := prefsV2Object(t, f)
	// The v1 choices survived.
	if obj["theme"] != "dark" || obj["density"] != "compact" {
		t.Errorf("a v1 document lost its choices: theme=%v density=%v", obj["theme"], obj["density"])
	}
	if obj["undoSendSeconds"] != float64(30) {
		t.Errorf("undoSendSeconds = %v, want the stored 30", obj["undoSendSeconds"])
	}
	if obj["keyboardShortcuts"] != false {
		t.Error("keyboardShortcuts lost its stored false")
	}
	// And the v2 keys came from the defaults, not from zero values.
	if obj["addressAutocomplete"] != "auto" {
		t.Errorf("addressAutocomplete = %v, want the default on a v1 row", obj["addressAutocomplete"])
	}
	if obj["sendAndArchive"] != true {
		t.Error("sendAndArchive read as its ZERO value on a v1 row, not its default")
	}
	if obj["defaultReplyBehavior"] != "reply" {
		t.Errorf("defaultReplyBehavior = %v, want the default on a v1 row", obj["defaultReplyBehavior"])
	}
	depth := prefsV2Sub(t, obj, "offlineDepth")
	if depth["headersPerMailbox"] != float64(200) || depth["bodies"] != float64(100) {
		t.Errorf("offlineDepth = %v, want the defaults on a v1 row", depth)
	}

	// A save from that account must now write v2 and keep everything.
	prefsV2Update(t, f, `{"sendAndArchive":false}`)
	after := prefsV2Object(t, f)
	if after["theme"] != "dark" {
		t.Error("saving a v2 key dropped a v1 choice")
	}
	if after["sendAndArchive"] != false {
		t.Error("the v2 save did not take")
	}
}

// ---------------------------------------------------------------------------
// the scalars
// ---------------------------------------------------------------------------

func TestPrefsV2ScalarsRoundTrip(t *testing.T) {
	f := newFixture(t)

	prefsV2Update(t, f, `{"addressAutocomplete":"manual","sendAndArchive":false,"defaultReplyBehavior":"replyAll"}`)
	obj := prefsV2Object(t, f)

	if obj["addressAutocomplete"] != "manual" {
		t.Errorf("addressAutocomplete = %v, want the saved value", obj["addressAutocomplete"])
	}
	if obj["sendAndArchive"] != false {
		t.Errorf("sendAndArchive = %v, want the saved value", obj["sendAndArchive"])
	}
	if obj["defaultReplyBehavior"] != "replyAll" {
		t.Errorf("defaultReplyBehavior = %v, want the saved value", obj["defaultReplyBehavior"])
	}
}

func TestPrefsV2ScalarsRejectValuesOutsideTheirDomain(t *testing.T) {
	f := newFixture(t)

	entry := prefsV2Refusal(t, f,
		`{"addressAutocomplete":"telepathic","defaultReplyBehavior":"replyNone","sendAndArchive":"yes"}`)
	got := prefsV2RefusedProperties(t, entry)
	want := map[string]bool{"addressAutocomplete": true, "defaultReplyBehavior": true, "sendAndArchive": true}
	if len(got) != len(want) {
		t.Errorf("properties = %v, want all %d offending keys named at once (§5.3)", got, len(want))
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("unexpected property %q in the refusal", name)
		}
	}
	// And nothing landed.
	if prefsV2Object(t, f)["addressAutocomplete"] != "auto" {
		t.Error("a refused patch changed the stored object")
	}
}

// ---------------------------------------------------------------------------
// labels
// ---------------------------------------------------------------------------

func TestPrefsV2LabelsRoundTrip(t *testing.T) {
	f := newFixture(t)

	prefsV2Update(t, f, `{"labels":{
		"Facturas":{"color":"amber","visibility":"showIfUnread"},
		"Equipo":{"color":"teal","visibility":"hide"}}}`)

	labels := prefsV2Sub(t, prefsV2Object(t, f), "labels")
	if len(labels) != 2 {
		t.Fatalf("labels = %v, want 2 entries", labels)
	}
	facturas, ok := labels["Facturas"].(map[string]any)
	if !ok {
		t.Fatalf("the Facturas entry is %T, want an object", labels["Facturas"])
	}
	if facturas["color"] != "amber" || facturas["visibility"] != "showIfUnread" {
		t.Errorf("Facturas = %v, want the saved metadata", facturas)
	}
}

// TestPrefsV2LabelPointerPatchEditsOneEntry is the property a whole-map
// replacement cannot give: a client toggling ONE label's color must not have to
// send all twenty-six, and must not clobber the others by omitting them.
func TestPrefsV2LabelPointerPatchEditsOneEntry(t *testing.T) {
	f := newFixture(t)

	prefsV2Update(t, f, `{"labels":{
		"A":{"color":"red","visibility":"show"},
		"B":{"color":"blue","visibility":"show"}}}`)

	// One label, by pointer. RFC 8620 §5.3: "The keys are a path in JSON
	// Pointer format, with an implicit leading '/'".
	prefsV2Update(t, f, `{"labels/A":{"color":"lime","visibility":"hide"}}`)

	labels := prefsV2Sub(t, prefsV2Object(t, f), "labels")
	if len(labels) != 2 {
		t.Fatalf("labels = %v, want both entries to survive a single-entry patch", labels)
	}
	a, _ := labels["A"].(map[string]any)
	if a["color"] != "lime" || a["visibility"] != "hide" {
		t.Errorf("A = %v, want the patched metadata", a)
	}
	b, _ := labels["B"].(map[string]any)
	if b["color"] != "blue" {
		t.Errorf("B = %v, want to be untouched by a patch that never named it", b)
	}
}

// TestPrefsV2LabelPointerNullRemovesTheEntry pins §5.3's null semantics on a
// map member: "otherwise remove the property". It removes the PRESENTATION,
// never the label — the label is an IMAP keyword (arbitrage A6).
func TestPrefsV2LabelPointerNullRemovesTheEntry(t *testing.T) {
	f := newFixture(t)

	prefsV2Update(t, f, `{"labels":{
		"A":{"color":"red","visibility":"show"},
		"B":{"color":"blue","visibility":"show"}}}`)
	prefsV2Update(t, f, `{"labels/A":null}`)

	labels := prefsV2Sub(t, prefsV2Object(t, f), "labels")
	if _, still := labels["A"]; still {
		t.Error("a null pointer patch did not remove the entry")
	}
	if _, ok := labels["B"]; !ok {
		t.Error("removing one entry removed another")
	}
}

// TestPrefsV2LabelsNullClearsTheWholeMap covers §5.3's other null: naming the
// property itself with null resets it to the default, which here is "no custom
// presentation at all".
func TestPrefsV2LabelsNullClearsTheWholeMap(t *testing.T) {
	f := newFixture(t)

	prefsV2Update(t, f, `{"labels":{"A":{"color":"red","visibility":"show"}}}`)
	prefsV2Update(t, f, `{"labels":null}`)

	if labels := prefsV2Sub(t, prefsV2Object(t, f), "labels"); len(labels) != 0 {
		t.Errorf("labels = %v, want empty after a null patch", labels)
	}
}

// TestPrefsV2LabelWholeAndPointerCompose is the reason the structured
// properties are applied AFTER the loop rather than inside it: a patch can
// carry both spellings, and Go's random map iteration would otherwise make the
// result depend on which key came out first.
//
// The defined order is: whole-value replacement first, per-entry edits on top.
func TestPrefsV2LabelWholeAndPointerCompose(t *testing.T) {
	f := newFixture(t)

	prefsV2Update(t, f, `{"labels":{"Old":{"color":"red","visibility":"show"}}}`)

	// Replace the map AND edit an entry, in one patch. Run it several times:
	// a result that depended on map iteration order would be flaky, not wrong,
	// and a single run could miss it.
	for i := range 8 {
		prefsV2Update(t, f, `{
			"labels":{"A":{"color":"blue","visibility":"show"}},
			"labels/B":{"color":"pink","visibility":"hide"}}`)

		labels := prefsV2Sub(t, prefsV2Object(t, f), "labels")
		if len(labels) != 2 {
			t.Fatalf("run %d: labels = %v, want exactly A (from the replacement) and B (from the edit)", i, labels)
		}
		if _, gone := labels["Old"]; gone {
			t.Errorf("run %d: the whole-map replacement did not drop the previous entry", i)
		}
		a, _ := labels["A"].(map[string]any)
		if a["color"] != "blue" {
			t.Errorf("run %d: A = %v", i, a)
		}
		b, _ := labels["B"].(map[string]any)
		if b["color"] != "pink" {
			t.Errorf("run %d: B = %v, want the per-entry edit applied ON TOP of the replacement", i, b)
		}
	}
}

// TestPrefsV2LabelRejectsAColorOutsideThePalette is the closed-palette rule
// enforced at the SERVER, not only in the UI: any client that opts into the
// vendor capability can write here, and an unvalidated color field is a
// free-text column reachable over the API.
func TestPrefsV2LabelRejectsAColorOutsideThePalette(t *testing.T) {
	f := newFixture(t)

	for name, patch := range map[string]string{
		"a hex value":      `{"labels":{"A":{"color":"#ff0000","visibility":"show"}}}`,
		"an unknown name":  `{"labels":{"A":{"color":"chartreuse","visibility":"show"}}}`,
		"a CSS expression": `{"labels":{"A":{"color":"rgb(1,2,3)","visibility":"show"}}}`,
		"the wrong case":   `{"labels":{"A":{"color":"Amber","visibility":"show"}}}`,
		"empty":            `{"labels":{"A":{"color":"","visibility":"show"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			entry := prefsV2Refusal(t, f, patch)
			if entry["type"] != "invalidProperties" {
				t.Errorf("type = %v, want invalidProperties", entry["type"])
			}
			// The refusal must NAME the palette, or a client has to guess.
			// These are all STRINGS outside the closed set, so listing the
			// domain is the useful answer.
			if desc, _ := entry["description"].(string); !strings.Contains(desc, "amber") {
				t.Errorf("the refusal does not list the palette: %q", desc)
			}
		})
	}

	// A non-string is a TYPE error, not a domain error, and its refusal names
	// the type rather than reciting twelve color names — the accurate message
	// for a client that sent 7 where a string belongs.
	entry := prefsV2Refusal(t, f, `{"labels":{"A":{"color":7,"visibility":"show"}}}`)
	if desc, _ := entry["description"].(string); !strings.Contains(desc, "string") {
		t.Errorf("a non-string color was not refused as a type error: %q", desc)
	}

	// Every advertised palette id must be accepted, or the session object would
	// be advertising a value the validator refuses.
	for _, id := range mail.LabelColorChoices() {
		patch := fmt.Sprintf(`{"labels":{"A":{"color":%q,"visibility":"show"}}}`, id)
		resp := prefsV2Update(t, f, patch)
		if resp["notUpdated"] != nil {
			t.Errorf("the advertised palette color %q was refused: %v", id, resp["notUpdated"])
		}
	}
}

func TestPrefsV2LabelRejectsAnUnknownVisibility(t *testing.T) {
	f := newFixture(t)
	entry := prefsV2Refusal(t, f, `{"labels":{"A":{"color":"red","visibility":"maybe"}}}`)
	if entry["type"] != "invalidProperties" {
		t.Errorf("type = %v, want invalidProperties", entry["type"])
	}
	// Every advertised visibility must be accepted.
	for _, v := range mail.LabelVisibilityChoices() {
		patch := fmt.Sprintf(`{"labels":{"A":{"color":"red","visibility":%q}}}`, v)
		if resp := prefsV2Update(t, f, patch); resp["notUpdated"] != nil {
			t.Errorf("the advertised visibility %q was refused: %v", v, resp["notUpdated"])
		}
	}
}

// TestPrefsV2LabelRejectsUnknownNestedKeys extends the anti-silence rule one
// level down: a key inside a label's metadata that the server does not
// implement must be refused, not dropped.
func TestPrefsV2LabelRejectsUnknownNestedKeys(t *testing.T) {
	f := newFixture(t)

	for name, patch := range map[string]string{
		"an extra key":     `{"labels":{"A":{"color":"red","visibility":"show","glow":true}}}`,
		"a missing color":  `{"labels":{"A":{"visibility":"show"}}}`,
		"a missing vis":    `{"labels":{"A":{"color":"red"}}}`,
		"not an object":    `{"labels":{"A":"red"}}`,
		"labels not a map": `{"labels":[1,2,3]}`,
	} {
		t.Run(name, func(t *testing.T) {
			entry := prefsV2Refusal(t, f, patch)
			if entry["type"] != "invalidProperties" {
				t.Errorf("type = %v, want invalidProperties", entry["type"])
			}
		})
	}
}

// TestPrefsV2LabelCapIsTheKeywordCeiling is the cap with a REASON: 26 is
// internal/imap.MaxDurableKeywordsPerMailbox (metadata.go:52), a Maildir fact
// — a keyword is one letter a-z in the filename, and dovecot-keywords stops at
// index 25 — so a 27th label cannot durably exist and metadata for it is dead
// weight.
func TestPrefsV2LabelCapIsTheKeywordCeiling(t *testing.T) {
	f := newFixture(t)

	cap := mail.MaxLabelPrefs()
	if cap != 26 {
		t.Fatalf("MaxLabelPrefs = %d, want the durable keyword ceiling 26", cap)
	}

	// Exactly at the cap: accepted.
	atCap := make([]string, 0, cap)
	for i := range cap {
		atCap = append(atCap, fmt.Sprintf(`"L%d":{"color":"red","visibility":"show"}`, i))
	}
	if resp := prefsV2Update(t, f, `{"labels":{`+strings.Join(atCap, ",")+`}}`); resp["notUpdated"] != nil {
		t.Fatalf("a map of exactly %d labels was refused: %v", cap, resp["notUpdated"])
	}
	if got := len(prefsV2Sub(t, prefsV2Object(t, f), "labels")); got != cap {
		t.Fatalf("stored %d labels, want %d", got, cap)
	}

	// One over, by whole replacement: refused.
	overCap := append(append([]string(nil), atCap...),
		fmt.Sprintf(`"L%d":{"color":"red","visibility":"show"}`, cap))
	entry := prefsV2Refusal(t, f, `{"labels":{`+strings.Join(overCap, ",")+`}}`)
	if desc, _ := entry["description"].(string); !strings.Contains(desc, "26") {
		t.Errorf("the refusal does not name the ceiling: %q", desc)
	}

	// One over, by ADDING to a full map: refused too. The cap must be checked
	// on the result, not only on a whole replacement.
	if e := prefsV2Refusal(t, f, `{"labels/Extra":{"color":"red","visibility":"show"}}`); e["type"] != "invalidProperties" {
		t.Errorf("adding a 27th label by pointer was not refused: %v", e)
	}

	// And the legal shape the per-key check would have wrongly refused:
	// removing one and adding one in the same patch keeps the count at the cap.
	resp := prefsV2Update(t, f, `{"labels/L0":null,"labels/Nueva":{"color":"blue","visibility":"show"}}`)
	if resp["notUpdated"] != nil {
		t.Fatalf("a patch that removes one label and adds one was refused: %v", resp["notUpdated"])
	}
	labels := prefsV2Sub(t, prefsV2Object(t, f), "labels")
	if len(labels) != cap {
		t.Errorf("after a swap the map holds %d labels, want %d", len(labels), cap)
	}
	if _, ok := labels["Nueva"]; !ok {
		t.Error("the swap did not add the new label")
	}
}

// ---------------------------------------------------------------------------
// offlineDepth
// ---------------------------------------------------------------------------

func TestPrefsV2OfflineDepthRoundTrips(t *testing.T) {
	f := newFixture(t)

	prefsV2Update(t, f, `{"offlineDepth":{"headersPerMailbox":500,"bodies":250}}`)
	depth := prefsV2Sub(t, prefsV2Object(t, f), "offlineDepth")
	if depth["headersPerMailbox"] != float64(500) || depth["bodies"] != float64(250) {
		t.Errorf("offlineDepth = %v, want the saved values", depth)
	}

	// A pointer patch on one member, and the other must be untouched.
	prefsV2Update(t, f, `{"offlineDepth/bodies":60}`)
	depth = prefsV2Sub(t, prefsV2Object(t, f), "offlineDepth")
	if depth["bodies"] != float64(60) {
		t.Errorf("offlineDepth.bodies = %v, want the patched 60", depth["bodies"])
	}
	if depth["headersPerMailbox"] != float64(500) {
		t.Errorf("offlineDepth.headersPerMailbox = %v, want to survive a patch that never named it",
			depth["headersPerMailbox"])
	}

	// Naming only one member in a WHOLE-value patch keeps the other too: the
	// object has two independent settings, and "change that one" is the read a
	// user expects from naming one.
	prefsV2Update(t, f, `{"offlineDepth":{"headersPerMailbox":300}}`)
	depth = prefsV2Sub(t, prefsV2Object(t, f), "offlineDepth")
	if depth["headersPerMailbox"] != float64(300) {
		t.Errorf("offlineDepth.headersPerMailbox = %v, want 300", depth["headersPerMailbox"])
	}
	if depth["bodies"] != float64(60) {
		t.Errorf("offlineDepth.bodies = %v, want the untouched 60", depth["bodies"])
	}
}

// TestPrefsV2OfflineDepthEnforcesItsBounds walks both edges of both ranges.
// The floors matter: a header depth below a screenful makes the offline list
// visibly truncated at the first scroll, which a user reads as data loss.
func TestPrefsV2OfflineDepthEnforcesItsBounds(t *testing.T) {
	f := newFixture(t)

	headersMin, headersMax, bodiesMin, bodiesMax := mail.OfflineDepthBounds()

	// The edges are INCLUSIVE.
	for _, ok := range []string{
		fmt.Sprintf(`{"offlineDepth":{"headersPerMailbox":%d}}`, headersMin),
		fmt.Sprintf(`{"offlineDepth":{"headersPerMailbox":%d}}`, headersMax),
		fmt.Sprintf(`{"offlineDepth":{"bodies":%d}}`, bodiesMin),
		fmt.Sprintf(`{"offlineDepth":{"bodies":%d}}`, bodiesMax),
	} {
		if resp := prefsV2Update(t, f, ok); resp["notUpdated"] != nil {
			t.Errorf("a value AT the boundary was refused: %s -> %v", ok, resp["notUpdated"])
		}
	}

	for name, patch := range map[string]string{
		"headers under the floor": fmt.Sprintf(`{"offlineDepth":{"headersPerMailbox":%d}}`, headersMin-1),
		"headers over the cap":    fmt.Sprintf(`{"offlineDepth":{"headersPerMailbox":%d}}`, headersMax+1),
		"bodies under the floor":  fmt.Sprintf(`{"offlineDepth":{"bodies":%d}}`, bodiesMin-1),
		"bodies over the cap":     fmt.Sprintf(`{"offlineDepth":{"bodies":%d}}`, bodiesMax+1),
		"zero":                    `{"offlineDepth":{"bodies":0}}`,
		"negative":                `{"offlineDepth":{"headersPerMailbox":-1}}`,
		"fractional":              `{"offlineDepth":{"bodies":100.5}}`,
		"a string":                `{"offlineDepth":{"bodies":"many"}}`,
		"an unknown member":       `{"offlineDepth":{"attachments":5}}`,
		"not an object":           `{"offlineDepth":42}`,
	} {
		t.Run(name, func(t *testing.T) {
			entry := prefsV2Refusal(t, f, patch)
			if entry["type"] != "invalidProperties" {
				t.Errorf("type = %v, want invalidProperties", entry["type"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// signatures
// ---------------------------------------------------------------------------

func TestPrefsV2SignaturesRoundTrip(t *testing.T) {
	f := newFixture(t)

	prefsV2Update(t, f, `{"signatures":{
		"items":{
			"work":{"name":"Work","textBody":"-- \nDiego","htmlBody":"<p>Diego</p>"},
			"personal":{"name":"Personal","textBody":"d","htmlBody":"<b>d</b>"}},
		"forNew":"work","forReply":"personal"}}`)

	sigs := prefsV2Sub(t, prefsV2Object(t, f), "signatures")
	items, ok := sigs["items"].(map[string]any)
	if !ok || len(items) != 2 {
		t.Fatalf("signatures.items = %v, want 2", sigs["items"])
	}
	work, _ := items["work"].(map[string]any)
	if work["name"] != "Work" || work["textBody"] != "-- \nDiego" {
		t.Errorf("the work signature = %v", work)
	}
	if sigs["forNew"] != "work" || sigs["forReply"] != "personal" {
		t.Errorf("forNew/forReply = %v/%v, want the saved selection", sigs["forNew"], sigs["forReply"])
	}

	// The pointer spelling of a selection change.
	prefsV2Update(t, f, `{"signatures/forReply":"work"}`)
	sigs = prefsV2Sub(t, prefsV2Object(t, f), "signatures")
	if sigs["forReply"] != "work" {
		t.Errorf("forReply = %v, want the patched value", sigs["forReply"])
	}
	if items, _ := sigs["items"].(map[string]any); len(items) != 2 {
		t.Error("patching the selection dropped the items")
	}
}

// TestPrefsV2SignatureSelectionMustResolve is the referential-integrity rule.
//
// A dangling reference is REFUSED rather than silently cleared, because the
// fallback silence would produce — the Identity's own signature — is a
// DIFFERENT signature going out under the user's name. That is exactly the
// class of substitution a settings screen must never make quietly.
func TestPrefsV2SignatureSelectionMustResolve(t *testing.T) {
	f := newFixture(t)

	entry := prefsV2Refusal(t, f, `{"signatures":{"items":{},"forNew":"ghost"}}`)
	props := prefsV2RefusedProperties(t, entry)
	if len(props) != 1 || props[0] != "signatures/forNew" {
		t.Errorf("properties = %v, want exactly [signatures/forNew] — the pointer, not the bare property", props)
	}

	// Selecting one that exists, and CREATING it in the same patch, must work:
	// the check runs on the RESULT, so the order the two keys arrive in cannot
	// matter. Repeated, because Go's map iteration order is random.
	for i := range 8 {
		f2 := newFixture(t)
		resp := prefsV2Update(t, f2, `{"signatures":{
			"items":{"new":{"name":"New","textBody":"x","htmlBody":"<p>x</p>"}},
			"forNew":"new"}}`)
		if resp["notUpdated"] != nil {
			t.Fatalf("run %d: creating a signature and selecting it in one patch was refused: %v",
				i, resp["notUpdated"])
		}
	}

	// Removing the items while a selection still points into them is refused
	// for the same reason.
	f3 := newFixture(t)
	prefsV2Update(t, f3, `{"signatures":{
		"items":{"a":{"name":"A","textBody":"x","htmlBody":"<p>x</p>"}},"forNew":"a"}}`)
	if e := prefsV2Refusal(t, f3, `{"signatures/items":{}}`); e["type"] != "invalidProperties" {
		t.Errorf("emptying items while forNew still points into them was not refused: %v", e)
	}

	// And clearing the selection with null is how a client gets back to the
	// Identity's own signature.
	resp := prefsV2Update(t, f3, `{"signatures/forNew":null}`)
	if resp["notUpdated"] != nil {
		t.Fatalf("clearing a selection with null was refused: %v", resp["notUpdated"])
	}
	if sigs := prefsV2Sub(t, prefsV2Object(t, f3), "signatures"); sigs["forNew"] != nil {
		t.Errorf("forNew = %v, want null after being cleared", sigs["forNew"])
	}
}

// TestPrefsV2SignatureHTMLIsSanitizedOnTheWayIn pins that a named signature
// goes through the SAME sanitizer the per-identity htmlSignature does.
//
// signature.go documents why that one string inverts the project's
// sanitize-on-render rule, and every word applies here: it is content Moov
// transmits under its own DKIM key, the database is the only copy, and it is
// served back into a contenteditable. A named signature that skipped the pass
// would be a second, unsanitized path to the same outgoing bytes.
func TestPrefsV2SignatureHTMLIsSanitizedOnTheWayIn(t *testing.T) {
	f := newFixture(t)

	hostile := `<p>Diego</p><script>alert(1)</script>` +
		`<a href="javascript:alert(2)">click</a>` +
		`<img src="x" onerror="alert(3)">` +
		`<iframe src="https://evil.example"></iframe>`
	patch, err := json.Marshal(map[string]any{
		"signatures": map[string]any{
			"items": map[string]any{
				"work": map[string]any{"name": "Work", "textBody": "Diego", "htmlBody": hostile},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp := prefsV2Update(t, f, string(patch)); resp["notUpdated"] != nil {
		t.Fatalf("a sanitizable signature was refused outright: %v", resp["notUpdated"])
	}

	sigs := prefsV2Sub(t, prefsV2Object(t, f), "signatures")
	items, _ := sigs["items"].(map[string]any)
	work, _ := items["work"].(map[string]any)
	stored, _ := work["htmlBody"].(string)

	for _, forbidden := range []string{"<script", "javascript:", "onerror", "<iframe"} {
		if strings.Contains(strings.ToLower(stored), forbidden) {
			t.Errorf("the stored signature still contains %q: %s", forbidden, stored)
		}
	}
	// The legitimate content survived — a sanitizer that dropped everything
	// would pass the assertions above and be useless.
	if !strings.Contains(stored, "Diego") {
		t.Errorf("sanitization removed the signature's actual content: %s", stored)
	}
	// textBody is NOT HTML and must be stored verbatim.
	if work["textBody"] != "Diego" {
		t.Errorf("textBody = %v, want it stored verbatim", work["textBody"])
	}
}

// TestPrefsV2SignatureCaps walks each cap: the item count, the per-signature
// bytes (shared with Identity), the name, and the total budget.
func TestPrefsV2SignatureCaps(t *testing.T) {
	f := newFixture(t)

	maxItems := mail.MaxSignatureItems()
	if maxItems != 10 {
		t.Fatalf("MaxSignatureItems = %d, want 10", maxItems)
	}

	// Exactly at the item cap: accepted.
	atCap := make([]string, 0, maxItems)
	for i := range maxItems {
		atCap = append(atCap, fmt.Sprintf(`"s%d":{"name":"S%d","textBody":"x","htmlBody":"<p>x</p>"}`, i, i))
	}
	if resp := prefsV2Update(t, f, `{"signatures":{"items":{`+strings.Join(atCap, ",")+`}}}`); resp["notUpdated"] != nil {
		t.Fatalf("exactly %d signatures were refused: %v", maxItems, resp["notUpdated"])
	}

	// One over: refused, naming the number.
	over := append(append([]string(nil), atCap...),
		fmt.Sprintf(`"s%d":{"name":"S","textBody":"x","htmlBody":"<p>x</p>"}`, maxItems))
	entry := prefsV2Refusal(t, f, `{"signatures":{"items":{`+strings.Join(over, ",")+`}}}`)
	if desc, _ := entry["description"].(string); !strings.Contains(desc, "10") {
		t.Errorf("the refusal does not name the cap: %q", desc)
	}

	// A single oversize signature: refused on the INPUT size, exactly as
	// Identity/set measures it, so the user is told rather than having the
	// sanitizer silently drop it.
	huge := strings.Repeat("x", mail.MaxSignatureBytes()+1)
	body, err := json.Marshal(map[string]any{
		"signatures": map[string]any{
			"items": map[string]any{"big": map[string]any{"name": "Big", "textBody": huge}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if e := prefsV2Refusal(t, f, string(body)); e["type"] != "invalidProperties" {
		t.Errorf("an oversize signature was not refused: %v", e)
	}

	// The TOTAL budget, which is the cap that would be VACUOUS if it were set
	// to items × per-item: that product is the sum of the maxima the per-item
	// check already enforces, so nothing could ever exceed it. The collection
	// cap is smaller, and this is what proves it binds — signatures each well
	// under the per-item cap that together are over the collection's.
	if mail.MaxSignaturesBytes() >= maxItems*mail.MaxSignatureBytes() {
		t.Fatalf("the collection cap (%d) is at or above items × per-item (%d): it can never bind",
			mail.MaxSignaturesBytes(), maxItems*mail.MaxSignatureBytes())
	}
	perItem := mail.MaxSignaturesBytes()/maxItems + 1024 // over budget in aggregate, under the per-item cap
	if perItem > mail.MaxSignatureBytes() {
		t.Fatalf("the fixture would trip the per-item cap first: %d > %d", perItem, mail.MaxSignatureBytes())
	}
	chunk := strings.Repeat("y", perItem)
	items := map[string]any{}
	for i := range maxItems {
		items[fmt.Sprintf("s%d", i)] = map[string]any{"name": "S", "textBody": chunk}
	}
	body, err = json.Marshal(map[string]any{"signatures": map[string]any{"items": items}})
	if err != nil {
		t.Fatal(err)
	}
	e := prefsV2Refusal(t, f, string(body))
	if e["type"] != "invalidProperties" {
		t.Errorf("a collection over the total byte budget was not refused: %v", e)
	}
	if desc, _ := e["description"].(string); !strings.Contains(desc, "collection") {
		t.Errorf("the refusal does not distinguish the collection cap from the per-item one: %q", desc)
	}

	// An oversize NAME: a dropdown label, not content.
	longName := strings.Repeat("n", 65)
	body, err = json.Marshal(map[string]any{
		"signatures": map[string]any{
			"items": map[string]any{"a": map[string]any{"name": longName}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if e := prefsV2Refusal(t, f, string(body)); e["type"] != "invalidProperties" {
		t.Errorf("an oversize signature NAME was not refused: %v", e)
	}
}

func TestPrefsV2SignatureRejectsUnknownNestedKeys(t *testing.T) {
	f := newFixture(t)

	for name, patch := range map[string]string{
		"unknown key on the object": `{"signatures":{"items":{},"autoInsert":true}}`,
		"unknown key on an item":    `{"signatures":{"items":{"a":{"name":"A","fontSize":12}}}}`,
		"an empty id":               `{"signatures":{"items":{"":{"name":"A"}}}}`,
		"items not an object":       `{"signatures":{"items":[1,2]}}`,
		"an item not an object":     `{"signatures":{"items":{"a":"just a string"}}}`,
		"forNew not a string":       `{"signatures":{"items":{},"forNew":42}}`,
		"signatures not an object":  `{"signatures":"none"}`,
		"unknown pointer sub-key":   `{"signatures/autoInsert":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			entry := prefsV2Refusal(t, f, patch)
			if entry["type"] != "invalidProperties" {
				t.Errorf("type = %v, want invalidProperties", entry["type"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// patch-shape rules
// ---------------------------------------------------------------------------

// TestPrefsV2ScalarsHaveNoNestedProperties pins that only the three structured
// properties accept a sub-pointer. "theme/dark" cannot apply to the object at
// all, which is §5.3's invalidPatch condition — a REQUEST-shape error, not a
// value error, so it is not reported as invalidProperties.
func TestPrefsV2ScalarsHaveNoNestedProperties(t *testing.T) {
	f := newFixture(t)

	for name, patch := range map[string]string{
		"a scalar with a sub-key":   `{"theme/dark":true}`,
		"a v2 scalar with one":      `{"sendAndArchive/enabled":true}`,
		"a two-level pointer":       `{"signatures/items/work":{"name":"W"}}`,
		"a three-level pointer":     `{"labels/A/color/hue":"red"}`,
		"an unknown with a sub-key": `{"telepathy/mode":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp := prefsV2Update(t, f, patch)
			notUpdated, ok := resp["notUpdated"].(map[string]any)
			if !ok {
				t.Fatalf("the patch was accepted: %v", resp)
			}
			entry := prefsSetError(t, notUpdated, "singleton")
			if entry["type"] != "invalidPatch" {
				t.Errorf("type = %v, want invalidPatch (§5.3: a pointer that cannot apply)", entry["type"])
			}
		})
	}
}

// TestPrefsV2SetIsIdempotent extends the v1 idempotence property to the
// structured keys: a retrying client, or a double-clicked toggle, must not
// accumulate anything.
func TestPrefsV2SetIsIdempotent(t *testing.T) {
	f := newFixture(t)

	patch := `{"labels":{"A":{"color":"red","visibility":"show"}},` +
		`"signatures":{"items":{"w":{"name":"W","textBody":"x","htmlBody":"<p>x</p>"}},"forNew":"w"},` +
		`"offlineDepth":{"headersPerMailbox":300,"bodies":80},` +
		`"addressAutocomplete":"manual"}`

	prefsV2Update(t, f, patch)
	first, err := json.Marshal(prefsV2Object(t, f))
	if err != nil {
		t.Fatal(err)
	}
	prefsV2Update(t, f, patch)
	second, err := json.Marshal(prefsV2Object(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("the second application changed the object:\n%s\n%s", first, second)
	}
}

// TestPrefsV2PropertyFilterServesTheStructuredKeys pins that /get's
// `properties` filter works on the v2 keys too — a settings screen that only
// wants the signatures should not have to fetch the whole object.
func TestPrefsV2PropertyFilterServesTheStructuredKeys(t *testing.T) {
	f := newFixture(t)

	resp := prefsCall(t, f, "Prefs/get",
		`{"accountId":"`+f.accountID()+`","ids":null,"properties":["signatures","labels"]}`)
	obj := firstObject(t, resp, 0)

	// §5.1: id is always returned even when not requested.
	if obj["id"] != "singleton" {
		t.Errorf("id = %v, want the singleton even under a filter", obj["id"])
	}
	if _, ok := obj["signatures"]; !ok {
		t.Error("the filtered object is missing the requested signatures")
	}
	if _, ok := obj["labels"]; !ok {
		t.Error("the filtered object is missing the requested labels")
	}
	if _, ok := obj["theme"]; ok {
		t.Error("the filtered object carries theme, which was not requested")
	}
	if len(obj) != 3 {
		t.Errorf("the filtered object has %d keys, want id + the 2 requested: %v", len(obj), obj)
	}
}
