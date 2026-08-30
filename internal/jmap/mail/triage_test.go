package mail

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Snooze/set, Snooze/get, Mute/set and Mute/get against fakes (L3 epic E4).
//
// The fakes let these assert the PROTOCOL — the §5.1/§5.3 shapes, the SetError
// vocabulary, the validation order — without a database or an IMAP server.
// What the fakes deliberately do not prove is that a snooze actually MOVES the
// message; that is the store's and the engine's, and it is tested there.

// fakeTriage is an in-memory TriageStore.
type fakeTriage struct {
	snoozes map[int64]SnoozeRecord // by email id
	mutes   map[int64]bool         // by thread id

	// snoozeErr and muteErr make the next write fail, so the handlers'
	// error-mapping branches are reachable.
	snoozeErr error
	muteErr   error

	// version advances on every write, so the state string moves and a test
	// can assert oldState/newState bracket the call.
	version int64
}

func newFakeTriage() *fakeTriage {
	return &fakeTriage{snoozes: map[int64]SnoozeRecord{}, mutes: map[int64]bool{}}
}

func (f *fakeTriage) ListSnoozes(_ context.Context, _ int64, _ int) ([]SnoozeRecord, error) {
	out := make([]SnoozeRecord, 0, len(f.snoozes))
	for _, r := range f.snoozes {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeTriage) SnoozeState(_ context.Context, _ int64) (string, error) {
	return stateFor(time.Unix(0, f.version+1), int64(len(f.snoozes))), nil
}

func (f *fakeTriage) Snooze(_ context.Context, _ int64, messageID int64, until time.Time) (SnoozeRecord, error) {
	if f.snoozeErr != nil {
		return SnoozeRecord{}, f.snoozeErr
	}
	f.version++
	rec := SnoozeRecord{EmailID: messageID, Until: until}
	f.snoozes[messageID] = rec
	return rec, nil
}

func (f *fakeTriage) Unsnooze(_ context.Context, _ int64, messageID int64) error {
	if f.snoozeErr != nil {
		return f.snoozeErr
	}
	if _, ok := f.snoozes[messageID]; !ok {
		return ErrNotSnoozed
	}
	f.version++
	delete(f.snoozes, messageID)
	return nil
}

func (f *fakeTriage) ListMutes(_ context.Context, _ int64, _ int) ([]int64, error) {
	out := make([]int64, 0, len(f.mutes))
	for id, muted := range f.mutes {
		if muted {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeTriage) MuteState(_ context.Context, _ int64) (string, error) {
	return stateFor(time.Unix(0, f.version+1), int64(len(f.mutes))), nil
}

func (f *fakeTriage) SetMuted(_ context.Context, _ int64, threadID int64, muted bool) error {
	if f.muteErr != nil {
		return f.muteErr
	}
	f.version++
	if muted {
		f.mutes[threadID] = true
	} else {
		delete(f.mutes, threadID)
	}
	return nil
}

// triageDeps wires a fake triage store into the standard fake deps.
func triageDeps(t *testing.T) (*Deps, *fakeTriage) {
	t.Helper()
	f := &fakeReaders{state: "1-1"}
	tri := newFakeTriage()
	deps := f.deps()
	deps.Triage = tri
	return deps, tri
}

// callTriage runs one handler and decodes its response.
func callTriage(t *testing.T, h func(context.Context, json.RawMessage) (any, *jmap.MethodError), args string) any {
	t.Helper()
	out, merr := h(callerCtx(), json.RawMessage(args))
	if merr != nil {
		t.Fatalf("the call was refused: %s %s", merr.Code, merr.Description)
	}
	return out
}

// ---------------------------------------------------------------------------
// registration
// ---------------------------------------------------------------------------

// TestTriageMethodsAreRegisteredUnderTheVendorCapability holds the isolation
// property the whole vendor-capability design rests on: a client that has never
// heard of Moov's triage verbs must be unaffected by their existence
// (RFC 8620 §1.8).
func TestTriageMethodsAreRegisteredUnderTheVendorCapability(t *testing.T) {
	deps, _ := triageDeps(t)
	registry := jmap.NewRegistry()
	RegisterTriageMethods(registry, deps)

	registered := map[string]bool{}
	for _, name := range registry.MethodNames() {
		registered[name] = true
	}
	for _, name := range []string{"Snooze/get", "Snooze/set", "Mute/get", "Mute/set"} {
		if !registered[name] {
			t.Errorf("%s is not registered", name)
		}
	}

	// The GATING is the property that matters, and it is asserted through the
	// engine rather than through the registry: a client that opts into the
	// standard mail capability only must be told the method does not exist.
	//
	// RFC 8620 §1.8: "The server MUST only follow the specifications that are
	// opted into and behave as though it does not implement anything else."
	// That is what keeps Bulwark — which has never heard of these methods —
	// unaffected by their presence.
	engine := jmap.NewEngine(registry, jmap.DefaultLimits(),
		[]string{jmap.CapCore, jmap.CapMail, jmap.CapTriage}, nil)

	call := func(using string) jmap.Invocation {
		t.Helper()
		body := []byte(`{"using":["` + using + `"],"methodCalls":[["Mute/get",{"accountId":"` +
			testAccountJMAPID() + `"},"0"]]}`)
		resp, rerr := engine.Process(callerCtx(), body, "s-0")
		if rerr != nil {
			t.Fatalf("the request was rejected: %v", rerr)
		}
		if len(resp.MethodResponses) != 1 {
			t.Fatalf("got %d responses, want 1", len(resp.MethodResponses))
		}
		return resp.MethodResponses[0]
	}

	if got := call(jmap.CapMail); got.Name != "error" {
		t.Errorf("Mute/get answered %q for a client that did not opt into the triage capability; "+
			"§1.8 requires the server to behave as though the method does not exist", got.Name)
	}
	if got := call(jmap.CapTriage); got.Name == "error" {
		t.Errorf("Mute/get refused a client that DID opt in: %s", got.Args)
	}
}

// TestRegisterTriageMethodsPanicsWithoutAStore is the startup contract every
// registrar here keeps: a server that advertises a capability and cannot answer
// it is lying to every client that opted in, so the failure belongs at start
// rather than at the first click.
func TestRegisterTriageMethodsPanicsWithoutAStore(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("RegisterTriageMethods accepted a nil Triage store")
		}
	}()
	f := &fakeReaders{state: "1-1"}
	RegisterTriageMethods(jmap.NewRegistry(), f.deps())
}

// ---------------------------------------------------------------------------
// Snooze/set
// ---------------------------------------------------------------------------

func TestSnoozeSetCreatesAndDestroys(t *testing.T) {
	deps, tri := triageDeps(t)
	emailID := EncodeEmailID(42)
	until := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)

	raw := callTriage(t, deps.handleSnoozeSet, `{"accountId":"`+testAccountJMAPID()+`",
		"create":{"s1":{"emailId":"`+emailID+`","until":"`+until+`"}}}`)
	resp, ok := raw.(*setResponse)
	if !ok {
		t.Fatalf("Snooze/set returned %T", raw)
	}
	created, ok := resp.Created["s1"].(map[string]any)
	if !ok {
		t.Fatalf("the create was refused: %+v", resp.NotCreated)
	}
	// The object's id IS the Email id, which is what lets a client address the
	// snooze with the id it already holds for the message it is looking at.
	if created["id"] != emailID {
		t.Errorf("created id = %v, want the Email id %q", created["id"], emailID)
	}
	if _, snoozed := tri.snoozes[42]; !snoozed {
		t.Error("the store was not asked to snooze the message")
	}
	if resp.OldState == resp.NewState {
		t.Error("the state string did not move; a second session would never learn of the snooze")
	}

	// Destroy is the un-snooze.
	raw = callTriage(t, deps.handleSnoozeSet, `{"accountId":"`+testAccountJMAPID()+`",
		"destroy":["`+emailID+`"]}`)
	resp = raw.(*setResponse) //nolint:errcheck // shape asserted above
	if len(resp.Destroyed) != 1 || resp.Destroyed[0] != emailID {
		t.Fatalf("destroyed = %v, notDestroyed = %+v", resp.Destroyed, resp.NotDestroyed)
	}
	if _, still := tri.snoozes[42]; still {
		t.Error("the message is still snoozed after the destroy")
	}
}

// TestSnoozeSetValidatesUntil covers every refusal parseSnoozeUntil can emit.
// Each one exists to catch a CLIENT bug rather than to restrict a user, and
// the descriptions say which.
func TestSnoozeSetValidatesUntil(t *testing.T) {
	cases := []struct {
		name  string
		until string
	}{
		{"a malformed timestamp", "tomorrow"},
		{"a time in the past", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
		// The classic milliseconds-sent-as-seconds mistake lands ~50,000 years
		// out; anything past the horizon is refused.
		{"a time past the horizon", time.Now().Add(10 * 365 * 24 * time.Hour).UTC().Format(time.RFC3339)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deps, tri := triageDeps(t)
			raw := callTriage(t, deps.handleSnoozeSet, `{"accountId":"`+testAccountJMAPID()+`",
				"create":{"s1":{"emailId":"`+EncodeEmailID(42)+`","until":"`+c.until+`"}}}`)
			resp := raw.(*setResponse) //nolint:errcheck // shape asserted elsewhere
			serr, refused := resp.NotCreated["s1"]
			if !refused {
				t.Fatalf("%s was accepted", c.name)
			}
			if serr.Type != setErrInvalidProperties {
				t.Errorf("type = %q, want invalidProperties", serr.Type)
			}
			if len(serr.Properties) != 1 || serr.Properties[0] != "until" {
				t.Errorf("properties = %v, want [until] — §5.3 asks for the offending property",
					serr.Properties)
			}
			if len(tri.snoozes) != 0 {
				t.Error("a refused create still snoozed the message")
			}
		})
	}
}

// TestSnoozeSetRequiresBothProperties holds §5.3's invalidProperties contract
// for a create missing a required field.
func TestSnoozeSetRequiresBothProperties(t *testing.T) {
	for _, c := range []struct{ name, obj, want string }{
		{"no emailId", `{"until":"2030-01-01T00:00:00Z"}`, "emailId"},
		{"no until", `{"emailId":"` + EncodeEmailID(1) + `"}`, "until"},
	} {
		t.Run(c.name, func(t *testing.T) {
			deps, _ := triageDeps(t)
			raw := callTriage(t, deps.handleSnoozeSet,
				`{"accountId":"`+testAccountJMAPID()+`","create":{"s1":`+c.obj+`}}`)
			resp := raw.(*setResponse) //nolint:errcheck
			serr, refused := resp.NotCreated["s1"]
			if !refused {
				t.Fatal("the incomplete create was accepted")
			}
			if len(serr.Properties) != 1 || serr.Properties[0] != c.want {
				t.Errorf("properties = %v, want [%s]", serr.Properties, c.want)
			}
		})
	}
}

// TestSnoozeUpdateOnlyTouchesUntil holds §5.3's rule that an update naming an
// immutable or server-set property is refused rather than ignored.
func TestSnoozeUpdateOnlyTouchesUntil(t *testing.T) {
	deps, tri := triageDeps(t)
	emailID := EncodeEmailID(42)
	tri.snoozes[42] = SnoozeRecord{EmailID: 42, Until: time.Now().Add(time.Hour)}

	later := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
	raw := callTriage(t, deps.handleSnoozeSet, `{"accountId":"`+testAccountJMAPID()+`",
		"update":{"`+emailID+`":{"until":"`+later+`"}}}`)
	resp := raw.(*setResponse) //nolint:errcheck
	if _, ok := resp.Updated[emailID]; !ok {
		t.Fatalf("the re-snooze was refused: %+v", resp.NotUpdated)
	}

	// Naming anything else is invalidProperties.
	raw = callTriage(t, deps.handleSnoozeSet, `{"accountId":"`+testAccountJMAPID()+`",
		"update":{"`+emailID+`":{"emailId":"`+EncodeEmailID(9)+`"}}}`)
	resp = raw.(*setResponse) //nolint:errcheck
	serr, refused := resp.NotUpdated[emailID]
	if !refused {
		t.Fatal("an update naming emailId was accepted; emailId is the object's identity")
	}
	if serr.Type != setErrInvalidProperties {
		t.Errorf("type = %q, want invalidProperties", serr.Type)
	}
}

// TestSnoozeCreateMapsTheEngineSentinels checks the two engine failures a user
// can actually cause reach the client as distinguishable SetErrors rather than
// as one opaque serverFail.
func TestSnoozeCreateMapsTheEngineSentinels(t *testing.T) {
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"an unknown message is notFound", ErrNotFound, setErrNotFound},
		{"an unavailable Snoozed folder is forbidden", ErrSnoozeUnavailable, setErrForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			deps, tri := triageDeps(t)
			tri.snoozeErr = c.err
			raw := callTriage(t, deps.handleSnoozeSet, `{"accountId":"`+testAccountJMAPID()+`",
				"create":{"s1":{"emailId":"`+EncodeEmailID(42)+`","until":"`+until+`"}}}`)
			resp := raw.(*setResponse) //nolint:errcheck
			serr, refused := resp.NotCreated["s1"]
			if !refused {
				t.Fatal("the failing create was reported as a success")
			}
			if serr.Type != c.want {
				t.Errorf("type = %q, want %q", serr.Type, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Snooze/get
// ---------------------------------------------------------------------------

func TestSnoozeGetServesTheSetAndNotFound(t *testing.T) {
	deps, tri := triageDeps(t)
	wake := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	tri.snoozes[42] = SnoozeRecord{EmailID: 42, Until: wake, OriginMailboxName: ""}

	// ids:null returns the whole set (§5.1).
	raw := callTriage(t, deps.handleSnoozeGet, `{"accountId":"`+testAccountJMAPID()+`"}`)
	resp, ok := raw.(*getResponse)
	if !ok {
		t.Fatalf("Snooze/get returned %T", raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("list has %d objects, want 1", len(resp.List))
	}
	obj := resp.List[0].(map[string]any) //nolint:errcheck // the handler builds maps
	if obj["until"] != "2026-09-01T08:00:00Z" {
		t.Errorf("until = %v, want the UTCDate form (RFC 8620 §1.4)", obj["until"])
	}
	// The store spells "the inbox" as the empty string; the wire must not.
	if obj["originMailboxName"] != nil {
		t.Errorf("originMailboxName = %v, want null for the inbox default", obj["originMailboxName"])
	}

	// An id naming a message that is not snoozed is notFound (§5.1).
	raw = callTriage(t, deps.handleSnoozeGet, `{"accountId":"`+testAccountJMAPID()+`",
		"ids":["`+EncodeEmailID(999)+`"]}`)
	resp = raw.(*getResponse) //nolint:errcheck
	if len(resp.NotFound) != 1 {
		t.Errorf("notFound = %v, want the unsnoozed id", resp.NotFound)
	}
	if len(resp.List) != 0 {
		t.Errorf("list = %v, want empty", resp.List)
	}
}

// TestSnoozeGetRefusesUnknownProperties holds §5.1's invalidArguments rule.
func TestSnoozeGetRefusesUnknownProperties(t *testing.T) {
	deps, _ := triageDeps(t)
	_, merr := deps.handleSnoozeGet(callerCtx(), json.RawMessage(
		`{"accountId":"`+testAccountJMAPID()+`","properties":["until","invented"]}`))
	if merr == nil || merr.Code != jmap.CodeInvalidArguments {
		t.Errorf("got %v, want invalidArguments", merr)
	}
}

// ---------------------------------------------------------------------------
// Mute/set and Mute/get
// ---------------------------------------------------------------------------

func TestMuteSetCreatesAndDestroys(t *testing.T) {
	deps, tri := triageDeps(t)
	threadID := EncodeThreadID(7)

	raw := callTriage(t, deps.handleMuteSet, `{"accountId":"`+testAccountJMAPID()+`",
		"create":{"m1":{"threadId":"`+threadID+`"}}}`)
	resp := raw.(*setResponse) //nolint:errcheck
	if _, ok := resp.Created["m1"]; !ok {
		t.Fatalf("the mute was refused: %+v", resp.NotCreated)
	}
	if !tri.mutes[7] {
		t.Error("the store was not asked to mute the thread")
	}

	// Muting again succeeds: the store's ON CONFLICT DO NOTHING makes it
	// idempotent, which is what a client retrying a lost request needs.
	raw = callTriage(t, deps.handleMuteSet, `{"accountId":"`+testAccountJMAPID()+`",
		"create":{"m2":{"threadId":"`+threadID+`"}}}`)
	resp = raw.(*setResponse) //nolint:errcheck
	if _, ok := resp.Created["m2"]; !ok {
		t.Errorf("re-muting an already-muted thread was refused: %+v", resp.NotCreated)
	}

	raw = callTriage(t, deps.handleMuteSet, `{"accountId":"`+testAccountJMAPID()+`",
		"destroy":["`+threadID+`"]}`)
	resp = raw.(*setResponse) //nolint:errcheck
	if len(resp.Destroyed) != 1 {
		t.Fatalf("destroyed = %v, notDestroyed = %+v", resp.Destroyed, resp.NotDestroyed)
	}
	if tri.mutes[7] {
		t.Error("the thread is still muted after the destroy")
	}
}

// TestMuteHasNoMutableProperties: muting is a binary fact, so "change it" is
// create or destroy. §5.3's invalidProperties is the answer for an update that
// names nothing changeable — which is more useful than silently succeeding.
func TestMuteHasNoMutableProperties(t *testing.T) {
	deps, _ := triageDeps(t)
	threadID := EncodeThreadID(7)

	raw := callTriage(t, deps.handleMuteSet, `{"accountId":"`+testAccountJMAPID()+`",
		"update":{"`+threadID+`":{"threadId":"`+EncodeThreadID(8)+`"}}}`)
	resp := raw.(*setResponse) //nolint:errcheck
	serr, refused := resp.NotUpdated[threadID]
	if !refused {
		t.Fatal("an update on a Mute was accepted")
	}
	if serr.Type != setErrInvalidProperties {
		t.Errorf("type = %q, want invalidProperties", serr.Type)
	}
}

func TestMuteGetServesTheMutedSet(t *testing.T) {
	deps, tri := triageDeps(t)
	tri.mutes[7] = true

	raw := callTriage(t, deps.handleMuteGet, `{"accountId":"`+testAccountJMAPID()+`"}`)
	resp := raw.(*getResponse) //nolint:errcheck
	if len(resp.List) != 1 {
		t.Fatalf("list has %d objects, want 1", len(resp.List))
	}
	obj := resp.List[0].(map[string]any) //nolint:errcheck
	if obj["id"] != EncodeThreadID(7) {
		t.Errorf("id = %v, want the Thread id", obj["id"])
	}

	// An unmuted thread is notFound — which is the whole `is:muted` answer: a
	// client asks about the threads it is showing and badges the ones that come
	// back.
	raw = callTriage(t, deps.handleMuteGet, `{"accountId":"`+testAccountJMAPID()+`",
		"ids":["`+EncodeThreadID(7)+`","`+EncodeThreadID(8)+`"]}`)
	resp = raw.(*getResponse) //nolint:errcheck
	if len(resp.List) != 1 || len(resp.NotFound) != 1 {
		t.Errorf("list=%d notFound=%v, want one of each", len(resp.List), resp.NotFound)
	}
}

// ---------------------------------------------------------------------------
// the boundaries every method here keeps
// ---------------------------------------------------------------------------

// TestTriageMethodsAreAccountScoped keeps the surface from becoming an oracle:
// a request naming somebody else's account must get accountNotFound before
// anything is read or written.
func TestTriageMethodsAreAccountScoped(t *testing.T) {
	deps, tri := triageDeps(t)
	foreign := jmap.EncodeAccountID(otherAccountID)

	handlers := map[string]func(context.Context, json.RawMessage) (any, *jmap.MethodError){
		"Snooze/get": deps.handleSnoozeGet,
		"Snooze/set": deps.handleSnoozeSet,
		"Mute/get":   deps.handleMuteGet,
		"Mute/set":   deps.handleMuteSet,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			_, merr := h(callerCtx(), json.RawMessage(`{"accountId":"`+foreign+`",
				"sinceState":"1-1","create":{"x":{"threadId":"`+EncodeThreadID(1)+`"}}}`))
			if merr == nil || merr.Code != jmap.CodeAccountNotFound {
				t.Errorf("got %v, want accountNotFound", merr)
			}
		})
	}
	if len(tri.mutes) != 0 || len(tri.snoozes) != 0 {
		t.Error("a cross-account request wrote something")
	}
}

// TestTriageMethodsRefuseWithoutACaller is the authentication guard.
func TestTriageMethodsRefuseWithoutACaller(t *testing.T) {
	deps, _ := triageDeps(t)
	handlers := map[string]func(context.Context, json.RawMessage) (any, *jmap.MethodError){
		"Snooze/get": deps.handleSnoozeGet,
		"Snooze/set": deps.handleSnoozeSet,
		"Mute/get":   deps.handleMuteGet,
		"Mute/set":   deps.handleMuteSet,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			_, merr := h(contextNoCaller{}, json.RawMessage(`{"accountId":"`+testAccountJMAPID()+`"}`))
			if merr == nil || merr.Code != jmap.CodeForbidden {
				t.Errorf("got %v, want forbidden", merr)
			}
		})
	}
}

// TestTriageSetHonorsIfInState holds §5.3's optimistic-concurrency contract:
// a stale ifInState aborts the whole method rather than applying half of it.
func TestTriageSetHonorsIfInState(t *testing.T) {
	deps, tri := triageDeps(t)
	_, merr := deps.handleMuteSet(callerCtx(), json.RawMessage(
		`{"accountId":"`+testAccountJMAPID()+`","ifInState":"stale-0",
		  "create":{"m1":{"threadId":"`+EncodeThreadID(7)+`"}}}`))
	if merr == nil || merr.Code != jmap.CodeStateMismatch {
		t.Fatalf("got %v, want stateMismatch", merr)
	}
	if len(tri.mutes) != 0 {
		t.Error("a stateMismatch still applied the create")
	}
}
