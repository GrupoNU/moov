package mail_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
)

// The Prefs surface (L3 epic E0) driven through the REAL dispatch engine
// against a real PostgreSQL store — the same shape the conformance suite uses,
// because the properties worth proving here are wire-level: the capability
// gates the methods, the singleton's id is what a client sends back, strict
// validation names every offending key, and the state cursor advances.
//
// Requires MOOV_TEST_DATABASE_URL; skips cleanly without it.

// prefsCall dispatches one preference method through the engine, opting into
// the vendor capability.
func prefsCall(t *testing.T, f *fixture, method, args string) map[string]any {
	t.Helper()
	inv := prefsDispatch(t, f, method, args, []string{jmap.CapCore, jmap.CapPrefs})
	if inv.Name == "error" {
		t.Fatalf("%s answered a method error: %s", method, inv.Args)
	}
	return decodeArgs(t, inv.Args)
}

// prefsCallExpectingError dispatches and REQUIRES a method-level error,
// returning it for inspection.
func prefsCallExpectingError(t *testing.T, f *fixture, method, args string) map[string]any {
	t.Helper()
	inv := prefsDispatch(t, f, method, args, []string{jmap.CapCore, jmap.CapPrefs})
	if inv.Name != "error" {
		t.Fatalf("%s succeeded; an error was expected. args=%s", method, inv.Args)
	}
	return decodeArgs(t, inv.Args)
}

func prefsDispatch(t *testing.T, f *fixture, method, args string, using []string) jmap.Invocation {
	t.Helper()
	registry := jmap.NewRegistry()
	mail.RegisterPrefsMethods(registry, f.deps)

	engine := jmap.NewEngine(registry, jmap.DefaultLimits(),
		[]string{jmap.CapCore, jmap.CapMail, jmap.CapPrefs}, nil)

	usingJSON, err := json.Marshal(using)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"using":%s,"methodCalls":[[%q,%s,"c1"]]}`, usingJSON, method, args)

	resp, rerr := engine.Process(f.callerCtx(), []byte(body), "session-1")
	if rerr != nil {
		t.Fatalf("request-level error: %v", rerr)
	}
	if len(resp.MethodResponses) != 1 {
		t.Fatalf("got %d method responses, want 1", len(resp.MethodResponses))
	}
	return resp.MethodResponses[0]
}

// prefsString is a checked string accessor: a shape violation fails with the
// property's name rather than panicking on a type assertion.
func prefsString(t *testing.T, parent map[string]any, key string) string {
	t.Helper()
	v, ok := parent[key].(string)
	if !ok {
		t.Fatalf("%s is %T, want a string", key, parent[key])
	}
	return v
}

// ---------------------------------------------------------------------------
// Prefs/get
// ---------------------------------------------------------------------------

// TestPrefsGetServesDefaultsForAnUntouchedAccount is the property migration
// 0007's no-backfill decision rests on: an account with no row must still get
// a complete, correct object.
func TestPrefsGetServesDefaultsForAnUntouchedAccount(t *testing.T) {
	f := newFixture(t)

	resp := prefsCall(t, f, "Prefs/get", `{"accountId":"`+f.accountID()+`","ids":null}`)
	obj := firstObject(t, resp, 0)

	if obj["id"] != "singleton" {
		t.Errorf("id = %v, want \"singleton\" (RFC 8621 §8's shape)", obj["id"])
	}

	defaults := mail.DefaultPrefsValue()
	if got := obj["undoSendSeconds"]; got != float64(defaults.UndoSendSeconds) {
		t.Errorf("undoSendSeconds = %v, want %d", got, defaults.UndoSendSeconds)
	}
	// D-3: the registered divergence from Gmail's off-default must survive all
	// the way to the wire, not merely to the store's constructor.
	if obj["keyboardShortcuts"] != true {
		t.Error("keyboardShortcuts must be true on the wire — decision D-3")
	}
	// D-4: display by default, gated on the HMAC proxy.
	if obj["imagesPolicy"] != "always" {
		t.Errorf("imagesPolicy = %v, want \"always\" — decision D-4", obj["imagesPolicy"])
	}
	// "String|null": an unset language is null, never "".
	if obj["language"] != nil {
		t.Errorf("language = %v, want null for the auto case", obj["language"])
	}

	// Every property the object advertises must be present when no filter is
	// given (§5.1: "If null, all properties of the object are returned").
	for _, name := range []string{
		"undoSendSeconds", "imagesPolicy", "conversationView", "hoverActions",
		"autoAdvance", "density", "showSnippets", "keyboardShortcuts",
		"language", "readingPane", "inboxType", "notifications", "theme",
	} {
		if _, ok := obj[name]; !ok {
			t.Errorf("the served object is missing %q", name)
		}
	}
}

// TestPrefsGetHonorsTheIDList pins the singleton's id semantics: the one id
// resolves, everything else is notFound (§5.1), and the object is never
// duplicated into a plural.
func TestPrefsGetHonorsTheIDList(t *testing.T) {
	f := newFixture(t)

	resp := prefsCall(t, f, "Prefs/get",
		`{"accountId":"`+f.accountID()+`","ids":["singleton","nope"]}`)

	list := array(t, resp, "list")
	if len(list) != 1 {
		t.Fatalf("list has %d entries, want exactly the singleton", len(list))
	}
	missing := array(t, resp, "notFound")
	if len(missing) != 1 || missing[0] != "nope" {
		t.Errorf("notFound = %v, want [\"nope\"]", missing)
	}
}

// TestPrefsGetPropertyFilter pins §5.1's two rules at once: only the requested
// properties come back, and id comes back regardless.
func TestPrefsGetPropertyFilter(t *testing.T) {
	f := newFixture(t)

	resp := prefsCall(t, f, "Prefs/get",
		`{"accountId":"`+f.accountID()+`","ids":null,"properties":["theme"]}`)
	obj := firstObject(t, resp, 0)

	if _, ok := obj["theme"]; !ok {
		t.Error("the requested property is missing")
	}
	if _, ok := obj["id"]; !ok {
		t.Error("id must be returned even when not requested (RFC 8620 §5.1)")
	}
	if _, ok := obj["density"]; ok {
		t.Error("an unrequested property was returned")
	}
}

// TestPrefsGetRefusesAnUnknownProperty is §5.1's invalidArguments condition.
func TestPrefsGetRefusesAnUnknownProperty(t *testing.T) {
	f := newFixture(t)

	errObj := prefsCallExpectingError(t, f, "Prefs/get",
		`{"accountId":"`+f.accountID()+`","ids":null,"properties":["fontSize"]}`)
	if errObj["type"] != "invalidArguments" {
		t.Errorf("type = %v, want invalidArguments (RFC 8620 §5.1)", errObj["type"])
	}
}

// ---------------------------------------------------------------------------
// the capability gate
// ---------------------------------------------------------------------------

// TestPrefsMethodsRequireTheCapability is the isolation property the whole
// vendor-capability design rests on: a client that does not opt in sees the
// methods as though they did not exist (RFC 8620 §1.8), which is exactly what
// keeps a standards-only client unaffected by E0.
func TestPrefsMethodsRequireTheCapability(t *testing.T) {
	f := newFixture(t)

	for _, method := range []string{"Prefs/get", "Prefs/set", "Prefs/changes"} {
		t.Run(method, func(t *testing.T) {
			inv := prefsDispatch(t, f, method,
				`{"accountId":"`+f.accountID()+`"}`,
				// Core and mail only — the standards-conforming client.
				[]string{jmap.CapCore, jmap.CapMail})
			if inv.Name != "error" {
				t.Fatalf("%s answered without the capability in \"using\"", method)
			}
			errObj := decodeArgs(t, inv.Args)
			if errObj["type"] != "unknownMethod" {
				t.Errorf("type = %v, want unknownMethod (RFC 8620 §1.8)", errObj["type"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Prefs/set
// ---------------------------------------------------------------------------

// TestPrefsSetRoundTrips is the core acceptance: a save is durable, visible to
// the next /get, and moves the state.
func TestPrefsSetRoundTrips(t *testing.T) {
	f := newFixture(t)

	before := prefsCall(t, f, "Prefs/get", `{"accountId":"`+f.accountID()+`","ids":null}`)
	oldState := prefsString(t, before, "state")

	resp := prefsCall(t, f, "Prefs/set", `{"accountId":"`+f.accountID()+`","update":{
		"singleton":{"theme":"dark","density":"compact","undoSendSeconds":30,"language":"es-AR"}}}`)

	updated, ok := resp["updated"].(map[string]any)
	if !ok {
		t.Fatalf("updated = %v, want an object naming the singleton", resp["updated"])
	}
	if _, ok := updated["singleton"]; !ok {
		t.Fatal("the singleton is not in the updated map")
	}
	// §5.3: null when nothing changed beyond what the client set. Nothing here
	// is transformed on the way in — values are accepted verbatim or refused —
	// so null is the truthful answer.
	if updated["singleton"] != nil {
		t.Errorf("updated[singleton] = %v, want null (no server-side side effects)", updated["singleton"])
	}
	if resp["notUpdated"] != nil {
		t.Fatalf("notUpdated = %v, want none", resp["notUpdated"])
	}
	if prefsString(t, resp, "oldState") != oldState {
		t.Errorf("oldState = %q, want the state /get reported (%q)", prefsString(t, resp, "oldState"), oldState)
	}
	if prefsString(t, resp, "newState") == oldState {
		t.Error("the state did not advance after a save; the user's other sessions would never learn")
	}

	after := prefsCall(t, f, "Prefs/get", `{"accountId":"`+f.accountID()+`","ids":null}`)
	obj := firstObject(t, after, 0)
	if obj["theme"] != "dark" {
		t.Errorf("theme = %v, want the saved value", obj["theme"])
	}
	if obj["density"] != "compact" {
		t.Errorf("density = %v, want the saved value", obj["density"])
	}
	if obj["undoSendSeconds"] != float64(30) {
		t.Errorf("undoSendSeconds = %v, want 30", obj["undoSendSeconds"])
	}
	if obj["language"] != "es-AR" {
		t.Errorf("language = %v, want the saved tag", obj["language"])
	}
	// The properties the patch did NOT name must be untouched. This is the
	// idempotence property the dense-write decision exists for: a settings
	// screen that saves one control must not reset the other twelve.
	defaults := mail.DefaultPrefsValue()
	if obj["readingPane"] != defaults.ReadingPane {
		t.Errorf("readingPane = %v, want the untouched default %q", obj["readingPane"], defaults.ReadingPane)
	}
	if obj["keyboardShortcuts"] != defaults.KeyboardShortcuts {
		t.Error("keyboardShortcuts changed although the patch never named it")
	}
}

// TestPrefsSetIsIdempotent pins that applying the same patch twice yields the
// same object — the property a retrying client (or a double-clicked toggle)
// depends on.
func TestPrefsSetIsIdempotent(t *testing.T) {
	f := newFixture(t)

	patch := `{"accountId":"` + f.accountID() + `","update":{"singleton":{"theme":"dark"}}}`
	prefsCall(t, f, "Prefs/set", patch)
	first := firstObject(t, prefsCall(t, f, "Prefs/get", `{"accountId":"`+f.accountID()+`","ids":null}`), 0)

	prefsCall(t, f, "Prefs/set", patch)
	second := firstObject(t, prefsCall(t, f, "Prefs/get", `{"accountId":"`+f.accountID()+`","ids":null}`), 0)

	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Errorf("the second application changed the object:\n%s\n%s", firstJSON, secondJSON)
	}
}

// TestPrefsSetRejectsInvalidValues is the strict-validation acceptance
// criterion: every offending key is named at once, with a per-key reason.
func TestPrefsSetRejectsInvalidValues(t *testing.T) {
	f := newFixture(t)

	resp := prefsCall(t, f, "Prefs/set", `{"accountId":"`+f.accountID()+`","update":{
		"singleton":{"density":"enormous","undoSendSeconds":7,"theme":"neon","autoAdvance":"sideways"}}}`)

	notUpdated, ok := resp["notUpdated"].(map[string]any)
	if !ok {
		t.Fatalf("notUpdated = %v, want the refusal", resp["notUpdated"])
	}
	entry, ok := notUpdated["singleton"].(map[string]any)
	if !ok {
		t.Fatalf("notUpdated[singleton] = %v, want a SetError", notUpdated["singleton"])
	}
	if entry["type"] != "invalidProperties" {
		t.Errorf("type = %v, want invalidProperties (RFC 8620 §5.3)", entry["type"])
	}
	// §5.3: "lists ALL the properties that were invalid" — one round trip per
	// mistake is not a usable settings screen.
	props, ok := entry["properties"].([]any)
	if !ok {
		t.Fatalf("properties = %v, want the list of offending keys", entry["properties"])
	}
	want := map[string]bool{"density": true, "undoSendSeconds": true, "theme": true, "autoAdvance": true}
	if len(props) != len(want) {
		t.Errorf("properties = %v, want all %d offending keys", props, len(want))
	}
	for _, p := range props {
		name, _ := p.(string)
		if !want[name] {
			t.Errorf("unexpected property %q in the refusal", name)
		}
	}
	if entry["description"] == nil || entry["description"] == "" {
		t.Error("the refusal carries no per-key explanation")
	}

	// And nothing was stored: a partially applied invalid patch would be worse
	// than a refused one.
	obj := firstObject(t, prefsCall(t, f, "Prefs/get", `{"accountId":"`+f.accountID()+`","ids":null}`), 0)
	if obj["density"] != mail.DefaultPrefsValue().Density {
		t.Error("a refused patch changed the stored object")
	}
}

// TestPrefsSetRejectsUnknownKeys is the anti-silence rule: a preference this
// server does not implement must not be accepted and quietly dropped, because
// the user would watch the control move and nothing change.
func TestPrefsSetRejectsUnknownKeys(t *testing.T) {
	f := newFixture(t)

	resp := prefsCall(t, f, "Prefs/set", `{"accountId":"`+f.accountID()+`","update":{
		"singleton":{"telepathyMode":true}}}`)

	notUpdated, ok := resp["notUpdated"].(map[string]any)
	if !ok {
		t.Fatalf("an unknown key was ACCEPTED; the setting would silently do nothing. resp=%v", resp)
	}
	entry := prefsSetError(t, notUpdated, "singleton")
	if entry["type"] != "invalidProperties" {
		t.Errorf("type = %v, want invalidProperties", entry["type"])
	}
}

// prefsSetError is a checked accessor for one entry of a notCreated /
// notUpdated / notDestroyed map, so a wrong shape fails with the id rather
// than panicking.
func prefsSetError(t *testing.T, m map[string]any, id string) map[string]any {
	t.Helper()
	e, ok := m[id].(map[string]any)
	if !ok {
		t.Fatalf("entry %q is %T, want a SetError object", id, m[id])
	}
	return e
}

// TestPrefsSetRefusesCreateAndDestroy pins the singleton's two structural
// refusals, each with the §5.3 error the RFC names.
func TestPrefsSetRefusesCreateAndDestroy(t *testing.T) {
	f := newFixture(t)

	resp := prefsCall(t, f, "Prefs/set", `{"accountId":"`+f.accountID()+`",
		"create":{"c1":{"theme":"dark"}},"destroy":["singleton"]}`)

	notCreated, ok := resp["notCreated"].(map[string]any)
	if !ok {
		t.Fatalf("notCreated = %v, want the refusal", resp["notCreated"])
	}
	if e := prefsSetError(t, notCreated, "c1"); e["type"] != "forbidden" {
		t.Errorf("create refusal type = %v, want forbidden", e["type"])
	}

	notDestroyed, ok := resp["notDestroyed"].(map[string]any)
	if !ok {
		t.Fatalf("notDestroyed = %v, want the refusal", resp["notDestroyed"])
	}
	if e := prefsSetError(t, notDestroyed, "singleton"); e["type"] != "forbidden" {
		t.Errorf("destroy refusal type = %v, want forbidden", e["type"])
	}

	// Both refusals must leave the object intact and readable.
	obj := firstObject(t, prefsCall(t, f, "Prefs/get", `{"accountId":"`+f.accountID()+`","ids":null}`), 0)
	if obj["id"] != "singleton" {
		t.Error("the singleton did not survive a refused destroy")
	}
}

// TestPrefsSetUnknownIDIsNotFound: any id but the singleton's.
func TestPrefsSetUnknownIDIsNotFound(t *testing.T) {
	f := newFixture(t)

	resp := prefsCall(t, f, "Prefs/set", `{"accountId":"`+f.accountID()+`","update":{
		"p1":{"theme":"dark"}}}`)
	notUpdated, ok := resp["notUpdated"].(map[string]any)
	if !ok {
		t.Fatalf("notUpdated = %v, want the refusal", resp["notUpdated"])
	}
	if e := prefsSetError(t, notUpdated, "p1"); e["type"] != "notFound" {
		t.Errorf("type = %v, want notFound", e["type"])
	}
}

// TestPrefsSetHonorsIfInState is §5.3's optimistic concurrency: a stale
// ifInState aborts the whole method with stateMismatch.
func TestPrefsSetHonorsIfInState(t *testing.T) {
	f := newFixture(t)

	errObj := prefsCallExpectingError(t, f, "Prefs/set",
		`{"accountId":"`+f.accountID()+`","ifInState":"not-the-state","update":{
			"singleton":{"theme":"dark"}}}`)
	if errObj["type"] != "stateMismatch" {
		t.Errorf("type = %v, want stateMismatch (RFC 8620 §5.3)", errObj["type"])
	}

	// The correct state must be accepted.
	get := prefsCall(t, f, "Prefs/get", `{"accountId":"`+f.accountID()+`","ids":null}`)
	state := prefsString(t, get, "state")
	resp := prefsCall(t, f, "Prefs/set",
		fmt.Sprintf(`{"accountId":%q,"ifInState":%q,"update":{"singleton":{"theme":"dark"}}}`,
			f.accountID(), state))
	if resp["notUpdated"] != nil {
		t.Errorf("a matching ifInState was refused: %v", resp["notUpdated"])
	}
}

// TestPrefsSetLanguageNullClearsIt pins the "String|null" round trip: null in,
// null out, and the stored form never leaks the empty string onto the wire.
func TestPrefsSetLanguageNullClearsIt(t *testing.T) {
	f := newFixture(t)

	prefsCall(t, f, "Prefs/set", `{"accountId":"`+f.accountID()+`","update":{
		"singleton":{"language":"pt-BR"}}}`)
	prefsCall(t, f, "Prefs/set", `{"accountId":"`+f.accountID()+`","update":{
		"singleton":{"language":null}}}`)

	obj := firstObject(t, prefsCall(t, f, "Prefs/get", `{"accountId":"`+f.accountID()+`","ids":null}`), 0)
	if obj["language"] != nil {
		t.Errorf("language = %v, want null after being cleared", obj["language"])
	}
}

// TestPrefsSetRejectsAMalformedLanguageTag: the shape check is shallow by
// design (it is not a registry lookup), but it must keep a sentence out of the
// column.
func TestPrefsSetRejectsAMalformedLanguageTag(t *testing.T) {
	f := newFixture(t)

	resp := prefsCall(t, f, "Prefs/set", `{"accountId":"`+f.accountID()+`","update":{
		"singleton":{"language":"please use spanish"}}}`)
	if resp["notUpdated"] == nil {
		t.Fatal("a sentence was accepted as a language tag")
	}
}

// ---------------------------------------------------------------------------
// Prefs/changes
// ---------------------------------------------------------------------------

// TestPrefsChangesReportsTheSave is why /changes is implemented rather than
// declined: the answer is exact, so declining would have been the dishonest
// option.
func TestPrefsChangesReportsTheSave(t *testing.T) {
	f := newFixture(t)

	get := prefsCall(t, f, "Prefs/get", `{"accountId":"`+f.accountID()+`","ids":null}`)
	cursor := prefsString(t, get, "state")

	// Nothing has changed yet.
	resp := prefsCall(t, f, "Prefs/changes",
		fmt.Sprintf(`{"accountId":%q,"sinceState":%q}`, f.accountID(), cursor))
	if n := len(array(t, resp, "created")) + len(array(t, resp, "updated")); n != 0 {
		t.Errorf("%d changes reported before any save", n)
	}
	if resp["hasMoreChanges"] != false {
		t.Errorf("hasMoreChanges = %v, want false: one object never exceeds a limit", resp["hasMoreChanges"])
	}

	prefsCall(t, f, "Prefs/set", `{"accountId":"`+f.accountID()+`","update":{
		"singleton":{"theme":"dark"}}}`)

	resp = prefsCall(t, f, "Prefs/changes",
		fmt.Sprintf(`{"accountId":%q,"sinceState":%q}`, f.accountID(), cursor))
	// The cursor was taken BEFORE the object existed (its count term was 0),
	// so the singleton is genuinely new to this client and belongs in created.
	created := array(t, resp, "created")
	if len(created) != 1 || created[0] != "singleton" {
		t.Errorf("created = %v, want [\"singleton\"] for a cursor predating the object", created)
	}
	if len(array(t, resp, "destroyed")) != 0 {
		t.Error("the singleton cannot be destroyed, so destroyed must always be empty")
	}

	// A cursor taken AFTER the object existed sees the next save as an update.
	afterFirst := prefsString(t, prefsCall(t, f, "Prefs/get", `{"accountId":"`+f.accountID()+`","ids":null}`), "state")
	prefsCall(t, f, "Prefs/set", `{"accountId":"`+f.accountID()+`","update":{
		"singleton":{"theme":"light"}}}`)
	resp = prefsCall(t, f, "Prefs/changes",
		fmt.Sprintf(`{"accountId":%q,"sinceState":%q}`, f.accountID(), afterFirst))
	updated := array(t, resp, "updated")
	if len(updated) != 1 || updated[0] != "singleton" {
		t.Errorf("updated = %v, want [\"singleton\"] for a cursor after the object existed", updated)
	}
}

// TestPrefsChangesRefusesAForeignCursor: a state string this server never
// issued is §5.2's cannotCalculateChanges, never a silent "start from zero"
// that would hand the client a bogus creation.
func TestPrefsChangesRefusesAForeignCursor(t *testing.T) {
	f := newFixture(t)

	errObj := prefsCallExpectingError(t, f, "Prefs/changes",
		`{"accountId":"`+f.accountID()+`","sinceState":"whatever"}`)
	if errObj["type"] != "cannotCalculateChanges" {
		t.Errorf("type = %v, want cannotCalculateChanges (RFC 8620 §5.2)", errObj["type"])
	}
}

// ---------------------------------------------------------------------------
// account scoping
// ---------------------------------------------------------------------------

// TestPrefsRefusesAForeignAccount: naming somebody else's account is
// accountNotFound before any read, the same rule every other method here
// follows.
func TestPrefsRefusesAForeignAccount(t *testing.T) {
	f := newFixture(t)

	other := jmap.EncodeAccountID(f.account.ID + 99_999)
	for _, method := range []string{"Prefs/get", "Prefs/set"} {
		errObj := prefsCallExpectingError(t, f, method, `{"accountId":"`+other+`"}`)
		if errObj["type"] != "accountNotFound" {
			t.Errorf("%s: type = %v, want accountNotFound", method, errObj["type"])
		}
	}
}

// ---------------------------------------------------------------------------
// the send path
// ---------------------------------------------------------------------------

// TestPrefsDriveTheUndoWindow is the E0 acceptance criterion that reaches
// outside the settings screen: the stored preference must actually govern the
// window a submission gets.
//
// It exercises the resolution directly rather than through a full send,
// because a real EmailSubmission needs a draft, an identity and an SMTP
// transport — none of which this property depends on. What matters is that the
// number the enqueue path uses comes from the account's row.
func TestPrefsDriveTheUndoWindow(t *testing.T) {
	f := newFixture(t)

	// Default: the daemon's configured window, since no preference is stored.
	f.deps.UndoWindow = 10_000_000_000 // 10s
	if got := mail.UndoWindowForTest(f.callerCtx(), f.deps, f.account.ID); got.Seconds() != 10 {
		t.Errorf("window = %v, want the configured default 10s for an account with no preference", got)
	}

	prefsCall(t, f, "Prefs/set", `{"accountId":"`+f.accountID()+`","update":{
		"singleton":{"undoSendSeconds":30}}}`)

	if got := mail.UndoWindowForTest(f.callerCtx(), f.deps, f.account.ID); got.Seconds() != 30 {
		t.Errorf("window = %v, want the account's stored 30s", got)
	}
}
