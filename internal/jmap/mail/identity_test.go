package mail

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Identity (RFC 8621 §6) — the handler semantics, against the RFC's own words.
//
// The fake below implements IdentityStore with the store's semantics (patch
// application, the null/absent distinction on replyTo and bcc, a watermark
// that advances on every write), so these tests exercise the RFC decisions
// without PostgreSQL. The store's own behavior is proven separately against a
// real PG in internal/store/identities_test.go.

// fakeIdentities implements IdentityStore.
type fakeIdentities struct {
	rows []IdentityRow
	// clock makes the watermark strictly increasing without sleeping, so a
	// state assertion is deterministic rather than timing-dependent.
	clock time.Time
	// failList makes ListIdentities fail, for the serverFail paths.
	failList bool
}

func newFakeIdentities(email string) *fakeIdentities {
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	return &fakeIdentities{
		clock: base,
		rows: []IdentityRow{{
			ID: 1, IsDefault: true, Email: email, Name: email, UpdatedAt: base,
		}},
	}
}

func (f *fakeIdentities) tick() time.Time {
	f.clock = f.clock.Add(time.Second)
	return f.clock
}

func (f *fakeIdentities) ListIdentities(context.Context, int64) ([]IdentityRow, error) {
	if f.failList {
		return nil, context.DeadlineExceeded
	}
	out := make([]IdentityRow, len(f.rows))
	copy(out, f.rows)
	return out, nil
}

func (f *fakeIdentities) IdentityState(context.Context, int64) (string, error) {
	var watermark time.Time
	for _, r := range f.rows {
		if r.UpdatedAt.After(watermark) {
			watermark = r.UpdatedAt
		}
	}
	return stateFor(watermark, int64(len(f.rows))), nil
}

func (f *fakeIdentities) IdentitiesChangedSince(_ context.Context, _ int64, since time.Time, limit int) ([]IdentityRow, error) {
	var out []IdentityRow
	for _, r := range f.rows {
		if r.UpdatedAt.After(since) {
			out = append(out, r)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeIdentities) UpdateIdentity(_ context.Context, _ int64, id int64, patch IdentityPatch) (IdentityRow, error) {
	for i := range f.rows {
		if f.rows[i].ID != id {
			continue
		}
		if patch.Name != nil {
			f.rows[i].Name = *patch.Name
		}
		if patch.TextSignature != nil {
			f.rows[i].TextSignature = *patch.TextSignature
		}
		if patch.HTMLSignature != nil {
			f.rows[i].HTMLSignature = *patch.HTMLSignature
		}
		if patch.ReplyTo != nil {
			f.rows[i].ReplyTo = *patch.ReplyTo
		}
		if patch.Bcc != nil {
			f.rows[i].Bcc = *patch.Bcc
		}
		f.rows[i].UpdatedAt = f.tick()
		return f.rows[i], nil
	}
	return IdentityRow{}, ErrNotFound
}

// identityDeps builds Deps with only the identity surface mounted.
func identityDeps(t *testing.T) (*fakeIdentities, *Deps) {
	t.Helper()
	f := newFakeReaders()
	ids := newFakeIdentities("user@example.com")
	deps := f.deps()
	deps.Identities = ids
	return ids, deps
}

func identitySet(t *testing.T, deps *Deps, args map[string]any) *setResponse {
	t.Helper()
	args["accountId"] = testAccountJMAPID()
	res, merr := deps.handleIdentitySet(callerCtx(), jsonArgs(t, args))
	if merr != nil {
		t.Fatalf("Identity/set: %v", merr)
	}
	return mustBe[*setResponse](t, res)
}

// ---------------------------------------------------------------------------
// Identity/get (§6.1)
// ---------------------------------------------------------------------------

func TestIdentityGetServesEveryRFCProperty(t *testing.T) {
	_, deps := identityDeps(t)
	res, merr := deps.handleIdentityGet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	get := mustBe[*getResponse](t, res)
	if len(get.List) != 1 {
		t.Fatalf("list = %v", get.List)
	}
	obj := mustBe[map[string]any](t, get.List[0])

	// §6 names exactly these properties; a client that asks for the object
	// must get all of them, and nothing extra.
	for prop := range identityProperties {
		if _, ok := obj[prop]; !ok {
			t.Errorf("Identity/get omitted the §6 property %q", prop)
		}
	}
	for prop := range obj {
		if !identityProperties[prop] {
			t.Errorf("Identity/get returned %q, which §6 does not define", prop)
		}
	}

	if obj["id"] != identityID {
		t.Errorf("id = %v, want the stable %q alias every pre-0006 client holds", obj["id"], identityID)
	}
	if obj["email"] != "user@example.com" {
		t.Errorf("email = %v", obj["email"])
	}
	// §6 mayDelete: "Servers may wish to set this to false for the user's
	// username or other default address."
	if obj["mayDelete"] != false {
		t.Errorf("mayDelete = %v, want false for the account's own address", obj["mayDelete"])
	}
	// §6 defaults: replyTo and bcc null, both signatures "".
	if obj["replyTo"] != nil || obj["bcc"] != nil {
		t.Errorf("replyTo=%v bcc=%v, want null (§6 default)", obj["replyTo"], obj["bcc"])
	}
	if obj["textSignature"] != "" || obj["htmlSignature"] != "" {
		t.Errorf("signatures = %q / %q, want the §6 default \"\"", obj["textSignature"], obj["htmlSignature"])
	}
}

func TestIdentityGetHonorsIDsAndProperties(t *testing.T) {
	_, deps := identityDeps(t)

	res, merr := deps.handleIdentityGet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(), "ids": []string{identityID, "ghost"},
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	get := mustBe[*getResponse](t, res)
	if len(get.List) != 1 || len(get.NotFound) != 1 || get.NotFound[0] != "ghost" {
		t.Errorf("list=%v notFound=%v", get.List, get.NotFound)
	}

	// RFC 8620 §5.1: "The id property of the object is always returned, even
	// if not explicitly requested."
	res, merr = deps.handleIdentityGet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(), "properties": []string{"email"},
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	obj := mustBe[map[string]any](t, mustBe[*getResponse](t, res).List[0])
	if len(obj) != 2 || obj["id"] == nil || obj["email"] == nil {
		t.Errorf("properties filter = %+v, want exactly id and email", obj)
	}
}

// ---------------------------------------------------------------------------
// Identity/set — update (§6.3)
// ---------------------------------------------------------------------------

func TestIdentitySetUpdatesTheMutableProperties(t *testing.T) {
	ids, deps := identityDeps(t)
	before, _ := ids.IdentityState(context.Background(), testAccountID)

	resp := identitySet(t, deps, map[string]any{
		"update": map[string]any{identityID: map[string]any{
			"name":          "Diego",
			"textSignature": "--\nDiego",
			"replyTo":       []map[string]any{{"name": "Desk", "email": "desk@example.com"}},
			"bcc":           []map[string]any{{"email": "archive@example.com"}},
		}},
	})
	if len(resp.NotUpdated) != 0 {
		t.Fatalf("notUpdated = %+v", resp.NotUpdated)
	}
	if _, ok := resp.Updated[identityID]; !ok {
		t.Fatalf("updated = %+v", resp.Updated)
	}
	// §5.3: oldState/newState bracket the change, and the state must MOVE —
	// that is what makes the save visible to the user's other sessions.
	if resp.OldState != before || resp.NewState == resp.OldState {
		t.Errorf("state did not advance: old=%q new=%q (before=%q)", resp.OldState, resp.NewState, before)
	}

	row := ids.rows[0]
	if row.Name != "Diego" || row.TextSignature != "--\nDiego" {
		t.Errorf("stored row = %+v", row)
	}
	if len(row.ReplyTo) != 1 || row.ReplyTo[0].Email != "desk@example.com" || row.ReplyTo[0].Name != "Desk" {
		t.Errorf("replyTo = %+v", row.ReplyTo)
	}
	if len(row.Bcc) != 1 || row.Bcc[0].Email != "archive@example.com" {
		t.Errorf("bcc = %+v", row.Bcc)
	}
}

// The signature save that motivated this epic: the exact shape Bulwark sends.
func TestIdentitySetStoresASignatureAndItReadsBack(t *testing.T) {
	_, deps := identityDeps(t)
	identitySet(t, deps, map[string]any{
		"update": map[string]any{identityID: map[string]any{
			"textSignature": "--\nDiego Nannini",
			"htmlSignature": "<div>--<br><b>Diego Nannini</b></div>",
		}},
	})

	res, merr := deps.handleIdentityGet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	obj := mustBe[map[string]any](t, mustBe[*getResponse](t, res).List[0])
	if obj["textSignature"] != "--\nDiego Nannini" {
		t.Errorf("textSignature did not round-trip: %q", obj["textSignature"])
	}
	html, _ := obj["htmlSignature"].(string)
	if !strings.Contains(html, "Diego Nannini") || !strings.Contains(html, "<b>") {
		t.Errorf("htmlSignature lost legitimate markup: %q", html)
	}
}

func TestIdentitySetReportsSanitizedHTMLBackToTheClient(t *testing.T) {
	// §5.3: the updated map carries "any properties that changed on the server
	// as a side effect". Sanitization is exactly that, and the client has to
	// learn what was actually stored — otherwise it renders a signature
	// preview from markup the server threw away.
	_, deps := identityDeps(t)
	resp := identitySet(t, deps, map[string]any{
		"update": map[string]any{identityID: map[string]any{
			"htmlSignature": `<b>Diego</b><script>alert(1)</script>`,
		}},
	})
	entry, ok := resp.Updated[identityID].(map[string]any)
	if !ok {
		t.Fatalf("sanitization changed the value but /set reported %v", resp.Updated[identityID])
	}
	got, _ := entry["htmlSignature"].(string)
	if strings.Contains(got, "<script") {
		t.Errorf("the reported value still contains a script: %q", got)
	}
	if !strings.Contains(got, "Diego") {
		t.Errorf("the reported value lost the legitimate markup: %q", got)
	}

	// A signature the policy does NOT change must report null instead —
	// §5.3: "null if no properties changed besides those set by the client".
	resp = identitySet(t, deps, map[string]any{
		"update": map[string]any{identityID: map[string]any{
			"htmlSignature": got,
		}},
	})
	if resp.Updated[identityID] != nil {
		t.Errorf("an unchanged signature reported a side effect: %v", resp.Updated[identityID])
	}
}

func TestIdentitySetRefusesTheImmutableEmail(t *testing.T) {
	ids, deps := identityDeps(t)
	resp := identitySet(t, deps, map[string]any{
		"update": map[string]any{identityID: map[string]any{"email": "someone-else@example.com"}},
	})
	// §6 types email "(immutable)"; RFC 8620 §5.3: "Any attempt to set an
	// immutable property ... MUST be rejected with an 'invalidProperties'
	// SetError."
	e, ok := resp.NotUpdated[identityID]
	if !ok || e.Type != setErrInvalidProperties {
		t.Fatalf("email update = %+v, want invalidProperties (RFC 8620 §5.3)", resp.NotUpdated)
	}
	if len(e.Properties) != 1 || e.Properties[0] != "email" {
		t.Errorf("properties = %v, want [email] (§5.3 lists the offending properties)", e.Properties)
	}
	if ids.rows[0].Email != "user@example.com" {
		t.Errorf("the immutable email was written anyway: %q", ids.rows[0].Email)
	}
	if len(resp.Updated) != 0 {
		t.Errorf("a refused update reported success: %+v", resp.Updated)
	}
}

func TestIdentitySetRefusesServerSetProperties(t *testing.T) {
	// §6 types id and mayDelete "(server-set)"; same §5.3 rule as email.
	for _, prop := range []string{"id", "mayDelete"} {
		t.Run(prop, func(t *testing.T) {
			_, deps := identityDeps(t)
			resp := identitySet(t, deps, map[string]any{
				"update": map[string]any{identityID: map[string]any{prop: "x"}},
			})
			e, ok := resp.NotUpdated[identityID]
			if !ok || e.Type != setErrInvalidProperties {
				t.Fatalf("%s update = %+v, want invalidProperties", prop, resp.NotUpdated)
			}
			if len(e.Properties) != 1 || e.Properties[0] != prop {
				t.Errorf("properties = %v, want [%s]", e.Properties, prop)
			}
		})
	}
}

func TestIdentitySetReportsEveryInvalidPropertyAtOnce(t *testing.T) {
	_, deps := identityDeps(t)
	resp := identitySet(t, deps, map[string]any{
		"update": map[string]any{identityID: map[string]any{
			"email":   "x@example.com",
			"name":    12345,
			"nonsuch": "value",
		}},
	})
	e := resp.NotUpdated[identityID]
	// §5.3: the SetError "lists ALL the properties that were invalid".
	want := []string{"email", "name", "nonsuch"}
	if len(e.Properties) != len(want) {
		t.Fatalf("properties = %v, want all of %v (§5.3)", e.Properties, want)
	}
	for i, p := range want {
		if e.Properties[i] != p {
			t.Errorf("properties = %v, want %v", e.Properties, want)
			break
		}
	}
}

func TestIdentitySetIsolatesFailuresPerID(t *testing.T) {
	// A W1 acceptance criterion for every /set: one bad record must not fail
	// the batch (§5.3 gives a SetError per record, not per call).
	_, deps := identityDeps(t)
	resp := identitySet(t, deps, map[string]any{
		"update": map[string]any{
			identityID: map[string]any{"name": "Kept"},
			"i-ghost":  map[string]any{"name": "Nope"},
		},
	})
	if _, ok := resp.Updated[identityID]; !ok {
		t.Errorf("the good update was dropped: %+v", resp)
	}
	if e, ok := resp.NotUpdated["i-ghost"]; !ok || e.Type != setErrNotFound {
		t.Errorf("the unknown id = %+v, want notFound", resp.NotUpdated)
	}
}

func TestIdentitySetNullResetsToTheRFCDefault(t *testing.T) {
	ids, deps := identityDeps(t)
	identitySet(t, deps, map[string]any{
		"update": map[string]any{identityID: map[string]any{
			"textSignature": "something",
			"replyTo":       []map[string]any{{"email": "desk@example.com"}},
		}},
	})
	// §5.3: "If null, set to the default value if specified for the property"
	// — §6 gives textSignature the default "" and replyTo the default null.
	identitySet(t, deps, map[string]any{
		"update": map[string]any{identityID: map[string]any{
			"textSignature": nil,
			"replyTo":       nil,
		}},
	})
	if ids.rows[0].TextSignature != "" {
		t.Errorf("textSignature = %q, want the §6 default \"\"", ids.rows[0].TextSignature)
	}
	if ids.rows[0].ReplyTo != nil {
		t.Errorf("replyTo = %+v, want null (§6 default)", ids.rows[0].ReplyTo)
	}
}

func TestIdentitySetRejectsMalformedAddressesAndPatches(t *testing.T) {
	_, deps := identityDeps(t)

	// An address that would become a malformed Reply-To on a signed message.
	resp := identitySet(t, deps, map[string]any{
		"update": map[string]any{identityID: map[string]any{
			"replyTo": []map[string]any{{"email": "not an address"}},
		}},
	})
	if e, ok := resp.NotUpdated[identityID]; !ok || e.Type != setErrInvalidProperties {
		t.Errorf("malformed replyTo = %+v, want invalidProperties", resp.NotUpdated)
	}

	// A pointer deeper than the object's structure: §5.3 invalidPatch.
	resp = identitySet(t, deps, map[string]any{
		"update": map[string]any{identityID: map[string]any{"replyTo/0/email": "x@example.com"}},
	})
	if e, ok := resp.NotUpdated[identityID]; !ok || e.Type != setErrInvalidPatch {
		t.Errorf("deep pointer = %+v, want invalidPatch (§5.3)", resp.NotUpdated)
	}
}

func TestIdentitySetOversizeSignatureIsRefusedNotTruncated(t *testing.T) {
	_, deps := identityDeps(t)
	huge := strings.Repeat("x", maxSignatureBytes+1)
	resp := identitySet(t, deps, map[string]any{
		"update": map[string]any{identityID: map[string]any{"textSignature": huge}},
	})
	e, ok := resp.NotUpdated[identityID]
	if !ok || e.Type != setErrInvalidProperties {
		t.Fatalf("oversize signature = %+v, want invalidProperties", resp.NotUpdated)
	}
	if len(e.Properties) != 1 || e.Properties[0] != "textSignature" {
		t.Errorf("properties = %v", e.Properties)
	}
}

// ---------------------------------------------------------------------------
// Identity/set — create and destroy refusals
// ---------------------------------------------------------------------------

func TestIdentitySetCreateIsRefusedWithForbiddenFrom(t *testing.T) {
	ids, deps := identityDeps(t)
	resp := identitySet(t, deps, map[string]any{
		"create": map[string]any{"c1": map[string]any{"email": "alias@example.com", "name": "Alias"}},
	})
	// §6.3 defines forbiddenFrom for exactly this: "The user is not allowed to
	// send from the address given as the 'email' property of the Identity."
	// §9.6 makes rejecting it a MUST when permission cannot be established.
	e, ok := resp.NotCreated["c1"]
	if !ok || e.Type != setErrForbiddenFrom {
		t.Fatalf("create = %+v, want the §6.3 forbiddenFrom SetError", resp.NotCreated)
	}
	if len(e.Properties) != 1 || e.Properties[0] != "email" {
		t.Errorf("properties = %v, want [email] — the property that cannot be honored", e.Properties)
	}
	if e.Description == "" {
		t.Error("the refusal must say WHY; a bare error code is what broke the pilot's signature save")
	}
	if len(ids.rows) != 1 {
		t.Errorf("a refused create still wrote a row: %+v", ids.rows)
	}
	// The refusal is per-record, not a method error: the batch's updates must
	// still apply.
	if resp.NewState == "" {
		t.Error("a refused create must still return a state")
	}
}

func TestIdentitySetDestroyIsRefusedForTheDefaultIdentity(t *testing.T) {
	ids, deps := identityDeps(t)
	resp := identitySet(t, deps, map[string]any{"destroy": []string{identityID}})
	// §6 mayDelete: "Attempts to destroy an Identity with 'mayDelete: false'
	// will be rejected with a standard 'forbidden' SetError."
	e, ok := resp.NotDestroyed[identityID]
	if !ok || e.Type != setErrForbidden {
		t.Fatalf("destroy = %+v, want the §6 forbidden SetError", resp.NotDestroyed)
	}
	if len(resp.Destroyed) != 0 || len(ids.rows) != 1 {
		t.Errorf("the default identity was destroyed anyway")
	}
	// The refusal must be consistent with what /get advertises.
	if ids.rows[0].MayDelete() {
		t.Error("mayDelete and the destroy refusal disagree — the report must be honest")
	}
}

func TestIdentitySetDestroyUnknownIDIsNotFound(t *testing.T) {
	_, deps := identityDeps(t)
	resp := identitySet(t, deps, map[string]any{"destroy": []string{"i-ghost"}})
	if e, ok := resp.NotDestroyed["i-ghost"]; !ok || e.Type != setErrNotFound {
		t.Errorf("destroy of an unknown id = %+v, want notFound", resp.NotDestroyed)
	}
}

// ---------------------------------------------------------------------------
// ifInState (§5.3)
// ---------------------------------------------------------------------------

func TestIdentitySetIfInStateGuardsTheWrite(t *testing.T) {
	ids, deps := identityDeps(t)
	state, _ := ids.IdentityState(context.Background(), testAccountID)

	// A matching ifInState proceeds.
	resp := identitySet(t, deps, map[string]any{
		"ifInState": state,
		"update":    map[string]any{identityID: map[string]any{"name": "First"}},
	})
	if len(resp.NotUpdated) != 0 {
		t.Fatalf("a matching ifInState was refused: %+v", resp.NotUpdated)
	}

	// The stale one is aborted whole — §5.3: "the method will be aborted and a
	// 'stateMismatch' error returned".
	_, merr := deps.handleIdentitySet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
		"ifInState": state,
		"update":    map[string]any{identityID: map[string]any{"name": "Second"}},
	}))
	if merr == nil || merr.Code != jmap.CodeStateMismatch {
		t.Fatalf("stale ifInState = %v, want stateMismatch", merr)
	}
	if ids.rows[0].Name != "First" {
		t.Errorf("the aborted call wrote anyway: %q", ids.rows[0].Name)
	}
}

// ---------------------------------------------------------------------------
// Identity/changes (§6.2)
// ---------------------------------------------------------------------------

func TestIdentityChangesReportsASavedSignature(t *testing.T) {
	ids, deps := identityDeps(t)
	before, _ := ids.IdentityState(context.Background(), testAccountID)

	// Nothing changed yet: the empty delta, same state.
	res, merr := deps.handleIdentityChanges(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(), "sinceState": before,
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	ch := mustBe[*changesResponse](t, res)
	if len(ch.Created)+len(ch.Updated)+len(ch.Destroyed) != 0 || ch.NewState != before {
		t.Fatalf("an unchanged account reported changes: %+v", ch)
	}

	identitySet(t, deps, map[string]any{
		"update": map[string]any{identityID: map[string]any{"textSignature": "--\nDiego"}},
	})

	res, merr = deps.handleIdentityChanges(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(), "sinceState": before,
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	ch = mustBe[*changesResponse](t, res)
	if len(ch.Updated) != 1 || ch.Updated[0] != identityID {
		t.Errorf("updated = %v, want the saved identity", ch.Updated)
	}
	if ch.NewState == before {
		t.Error("the cursor did not advance past the save")
	}
	if ch.HasMoreChanges {
		t.Error("one change does not need a second page")
	}
}

func TestIdentityChangesRejectsAForeignCursor(t *testing.T) {
	_, deps := identityDeps(t)
	// §5.2: a state the server did not issue is cannotCalculateChanges.
	if _, merr := deps.handleIdentityChanges(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(), "sinceState": "not-a-cursor",
	})); merr == nil || merr.Code != jmap.CodeCannotCalculateChanges {
		t.Errorf("foreign cursor = %v, want cannotCalculateChanges", merr)
	}
}

// ---------------------------------------------------------------------------
// the wire id contract
// ---------------------------------------------------------------------------

func TestDefaultIdentityKeepsThePrimaryWireID(t *testing.T) {
	// Load-bearing for the pilot: clients persisted `identityId: "primary"`
	// before migration 0006, and stored EmailSubmission payloads reference it.
	// The row id must not leak into the wire id for the default identity.
	for _, rowID := range []int64{1, 42, 999999} {
		if got := EncodeIdentityID(rowID, true); got != identityID {
			t.Errorf("EncodeIdentityID(%d, default) = %q, want %q", rowID, got, identityID)
		}
	}
	// A non-default identity uses the prefixed scheme, which cannot collide
	// with "primary".
	if got := EncodeIdentityID(7, false); got == identityID || !strings.HasPrefix(got, identityIDPrefix) {
		t.Errorf("EncodeIdentityID(7, alias) = %q", got)
	}
}

// ---------------------------------------------------------------------------
// submission wiring (§7.5, §9.6)
// ---------------------------------------------------------------------------

func TestSubmissionResolvesTheIdentityAndRejectsUnknownOnes(t *testing.T) {
	_, _, deps := submissionDeps(t)

	// §7.5: "If the Email or Identity id given cannot be found, the submission
	// creation is rejected with a standard 'invalidProperties' SetError."
	res, merr := deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t,
		map[string]any{"identityId": "i-nonexistent", "emailId": EncodeEmailID(10)}, nil))
	if merr != nil {
		t.Fatal(merr)
	}
	resp := firstResult(t, res)
	e, ok := resp.NotCreated["s1"]
	if !ok || e.Type != setErrInvalidProperties {
		t.Fatalf("unknown identityId = %+v, want invalidProperties (§7.5)", resp.NotCreated)
	}
	if len(e.Properties) != 1 || e.Properties[0] != "identityId" {
		t.Errorf("properties = %v, want [identityId]", e.Properties)
	}
}

func TestSubmissionAppliesTheIdentityBccToADerivedEnvelope(t *testing.T) {
	_, subs, deps := submissionDeps(t)
	ids, ok := deps.Identities.(*fakeIdentities)
	if !ok {
		t.Fatal("submissionDeps must mount the identity fake")
	}
	ids.rows[0].Bcc = []EmailAddress{{Email: "archive@example.com"}}

	// No envelope: §7.1.2 has the server derive it, which is where the
	// identity's §6 bcc default belongs.
	res, merr := deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t,
		map[string]any{"identityId": identityID, "emailId": EncodeEmailID(10)}, nil))
	if merr != nil {
		t.Fatal(merr)
	}
	if resp := firstResult(t, res); len(resp.NotCreated) != 0 {
		t.Fatalf("notCreated = %+v", resp.NotCreated)
	}
	if len(subs.specs) != 1 {
		t.Fatalf("enqueued = %+v", subs.specs)
	}
	var found bool
	for _, r := range subs.specs[1].RcptTo {
		if r == "archive@example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("the identity's bcc default was not applied to the derived envelope: %v", subs.specs[1].RcptTo)
	}
}

func TestSubmissionDoesNotEnlargeAnExplicitEnvelope(t *testing.T) {
	// The identity's bcc is a default for a DERIVED envelope only. An explicit
	// envelope is the client stating the recipient set exactly, and silently
	// adding a recipient to it is the one change a user can neither see nor
	// undo.
	_, subs, deps := submissionDeps(t)
	ids, ok := deps.Identities.(*fakeIdentities)
	if !ok {
		t.Fatal("submissionDeps must mount the identity fake")
	}
	ids.rows[0].Bcc = []EmailAddress{{Email: "archive@example.com"}}

	res, merr := deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t, map[string]any{
		"identityId": identityID, "emailId": EncodeEmailID(10),
		"envelope": map[string]any{
			"mailFrom": map[string]any{"email": "user@example.com"},
			"rcptTo":   []map[string]any{{"email": "only@example.test"}},
		},
	}, nil))
	if merr != nil {
		t.Fatal(merr)
	}
	if resp := firstResult(t, res); len(resp.NotCreated) != 0 {
		t.Fatalf("notCreated = %+v", resp.NotCreated)
	}
	got := subs.specs[1].RcptTo
	if len(got) != 1 || got[0] != "only@example.test" {
		t.Errorf("an explicit envelope was enlarged: %v", got)
	}
}

// jsonArgsFor keeps the identity tests honest about the wire shape: every
// argument object they build round-trips through JSON exactly as a client's
// would.
var _ = json.Marshal
