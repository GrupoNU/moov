package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// RFC conformance for L3 epic E6, cited clause by clause and driven through
// the real dispatch engine — the same discipline as conformance_test.go and
// conformance_e4_test.go (which record why the official jmapio suite cannot
// be used: no license, and architecturally write-only).
//
// The explicit skip record (L2 §2.5: "nunca silencioso") for this epic:
//
//   - SieveScript/changes and /queryChanges: registered, always answer
//     cannotCalculateChanges — ManageSieve has no changelog. Tested below.
//   - Quota/changes and /queryChanges: registered, same refusal — Dovecot
//     pushes no quota deltas. Tested below.
//   - VacationResponse/changes: NOT registered — RFC 8621 §8 defines only
//     /get and /set for the type, so there is no method to decline.
//   - VacationResponse restrictToContacts: refused with invalidProperties —
//     not an §8 property, and there is no contacts subsystem to honor it
//     honestly. Tested below.
//   - The vendor filter surface defines no /changes methods by contract
//     (filters.go's package comment; the capability object says
//     maxChangesSupported: false).

// --- fakes -----------------------------------------------------------------

// fakeSieve implements SieveStore, VacationStore, FilterStore and
// ForwardingStore in memory, mirroring the semantics the real adapter maps
// from ManageSieve.
type fakeSieve struct {
	scripts  []SieveScriptInfo
	contents map[int64][]byte
	nextID   int64
	managed  int64 // ledger id of the "moov" script, 0 = none

	vacation VacationValue
	vacState int

	rules      []FilterRuleValue
	forwardAll ForwardAllValue
	active     bool
	filtState  int

	addresses []ForwardingAddressValue
	nextFwd   int64
	fwdState  int
	verified  map[string]bool
	tokens    map[string]string // token -> email

	quota []QuotaValue
}

func newFakeSieve() *fakeSieve {
	return &fakeSieve{
		contents: map[int64][]byte{},
		nextID:   1,
		nextFwd:  1,
		verified: map[string]bool{},
		tokens:   map[string]string{},
		active:   true,
	}
}

func (f *fakeSieve) ListScripts(_ context.Context, accountID int64) ([]SieveScriptInfo, error) {
	requireTestAccount(accountID)
	return append([]SieveScriptInfo(nil), f.scripts...), nil
}

func (f *fakeSieve) ManagedScriptID(_ context.Context, accountID int64) (int64, bool, error) {
	requireTestAccount(accountID)
	return f.managed, f.managed != 0, nil
}

func (f *fakeSieve) CreateScript(_ context.Context, accountID int64, name string, content []byte) (SieveScriptInfo, error) {
	requireTestAccount(accountID)
	for _, s := range f.scripts {
		if s.Name == name {
			return SieveScriptInfo{}, &SieveNameTakenError{ExistingID: s.ID}
		}
	}
	if bytes.Contains(content, []byte("BROKEN")) {
		return SieveScriptInfo{}, &SieveInvalidError{Description: "line 1: error: unknown command"}
	}
	info := SieveScriptInfo{ID: f.nextID, Name: name, BlobID: fmt.Sprintf("sha-%d", f.nextID), Size: int64(len(content))}
	f.nextID++
	f.scripts = append(f.scripts, info)
	f.contents[info.ID] = content
	return info, nil
}

func (f *fakeSieve) UpdateScript(_ context.Context, accountID, id int64, newName *string, content []byte) (SieveScriptInfo, error) {
	requireTestAccount(accountID)
	for i, s := range f.scripts {
		if s.ID != id {
			continue
		}
		if newName != nil {
			for _, o := range f.scripts {
				if o.ID != id && o.Name == *newName {
					return SieveScriptInfo{}, &SieveNameTakenError{ExistingID: o.ID}
				}
			}
			f.scripts[i].Name = *newName
		}
		if content != nil {
			f.contents[id] = content
		}
		return f.scripts[i], nil
	}
	return SieveScriptInfo{}, ErrNotFound
}

func (f *fakeSieve) DestroyScript(_ context.Context, accountID, id int64) error {
	requireTestAccount(accountID)
	for i, s := range f.scripts {
		if s.ID != id {
			continue
		}
		if s.Active {
			return ErrSieveScriptActive
		}
		f.scripts = append(f.scripts[:i], f.scripts[i+1:]...)
		delete(f.contents, id)
		return nil
	}
	return ErrNotFound
}

func (f *fakeSieve) ActivateScript(_ context.Context, accountID, id int64) error {
	requireTestAccount(accountID)
	found := id == 0
	for i := range f.scripts {
		f.scripts[i].Active = f.scripts[i].ID == id
		if f.scripts[i].ID == id {
			found = true
		}
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

func (f *fakeSieve) ValidateScript(_ context.Context, accountID int64, content []byte) error {
	requireTestAccount(accountID)
	if bytes.Contains(content, []byte("BROKEN")) {
		return &SieveInvalidError{Description: "line 1: error: unknown command"}
	}
	return nil
}

func (f *fakeSieve) CheckRedirectPolicy(_ context.Context, accountID int64, content []byte) error {
	requireTestAccount(accountID)
	// The same fail-closed contract as the real adapter, driven by markers
	// so the handler-level gate is what this exercises (the real scanner has
	// its own pinned tests in internal/sieve).
	if bytes.Contains(content, []byte("redirect-unverified")) {
		return fmt.Errorf("the script redirects to unverified addresses (evil@x)")
	}
	return nil
}

func (f *fakeSieve) SieveState(_ context.Context, accountID int64) (string, error) {
	requireTestAccount(accountID)
	return fmt.Sprintf("sieve-%d", len(f.scripts)), nil
}

func (f *fakeSieve) GetVacation(_ context.Context, accountID int64) (VacationValue, error) {
	requireTestAccount(accountID)
	return f.vacation, nil
}

func (f *fakeSieve) SetVacation(_ context.Context, accountID int64, v VacationValue) error {
	requireTestAccount(accountID)
	f.vacation = v
	f.vacState++
	return nil
}

func (f *fakeSieve) VacationState(_ context.Context, accountID int64) (string, error) {
	requireTestAccount(accountID)
	return fmt.Sprintf("vac-%d", f.vacState), nil
}

func (f *fakeSieve) GetFilters(_ context.Context, accountID int64) (FilterConfig, error) {
	requireTestAccount(accountID)
	return FilterConfig{Rules: append([]FilterRuleValue(nil), f.rules...),
		ForwardAll: f.forwardAll, ScriptActive: f.active}, nil
}

func (f *fakeSieve) PutFilters(_ context.Context, accountID int64, rules []FilterRuleValue, forwardAll ForwardAllValue) error {
	requireTestAccount(accountID)
	// The verified-forward rule, as the real model enforces it on push.
	for _, r := range rules {
		if r.Forward != "" && !f.verified[strings.ToLower(r.Forward)] {
			return &SieveInvalidError{Description: fmt.Sprintf(
				"forward target %q is not a verified forwarding address", r.Forward)}
		}
	}
	if forwardAll.Enabled && !f.verified[strings.ToLower(forwardAll.Address)] {
		return &SieveInvalidError{Description: fmt.Sprintf(
			"forwarding: %q is not a verified forwarding address", forwardAll.Address)}
	}
	f.rules = rules
	f.forwardAll = forwardAll
	f.filtState++
	return nil
}

func (f *fakeSieve) FiltersState(_ context.Context, accountID int64) (string, error) {
	requireTestAccount(accountID)
	return fmt.Sprintf("filt-%d", f.filtState), nil
}

func (f *fakeSieve) ListForwardingAddresses(_ context.Context, accountID int64) ([]ForwardingAddressValue, error) {
	requireTestAccount(accountID)
	return append([]ForwardingAddressValue(nil), f.addresses...), nil
}

func (f *fakeSieve) CreateForwardingAddress(_ context.Context, accountID int64, email string) (ForwardingAddressValue, error) {
	requireTestAccount(accountID)
	for _, a := range f.addresses {
		if a.Email == email {
			return ForwardingAddressValue{}, ErrForwardingExists
		}
	}
	row := ForwardingAddressValue{ID: f.nextFwd, Email: email, State: "pending"}
	f.nextFwd++
	f.addresses = append(f.addresses, row)
	f.tokens["token-for-"+email] = email
	f.fwdState++
	return row, nil
}

func (f *fakeSieve) DestroyForwardingAddress(_ context.Context, accountID, id int64) error {
	requireTestAccount(accountID)
	for i, a := range f.addresses {
		if a.ID != id {
			continue
		}
		for _, r := range f.rules {
			if r.Enabled && strings.EqualFold(r.Forward, a.Email) {
				return ErrForwardingInUse
			}
		}
		if f.forwardAll.Enabled && strings.EqualFold(f.forwardAll.Address, a.Email) {
			return ErrForwardingInUse
		}
		f.addresses = append(f.addresses[:i], f.addresses[i+1:]...)
		f.fwdState++
		return nil
	}
	return ErrNotFound
}

func (f *fakeSieve) VerifyForwarding(_ context.Context, accountID int64, token string) (string, error) {
	requireTestAccount(accountID)
	email, ok := f.tokens[token]
	if !ok {
		return "", ErrTokenInvalid
	}
	for i := range f.addresses {
		if f.addresses[i].Email == email {
			f.addresses[i].State = "accepted"
			now := time.Now()
			f.addresses[i].VerifiedAt = &now
		}
	}
	f.verified[email] = true
	f.fwdState++
	return email, nil
}

func (f *fakeSieve) ForwardingState(_ context.Context, accountID int64) (string, error) {
	requireTestAccount(accountID)
	return fmt.Sprintf("fwd-%d", f.fwdState), nil
}

func (f *fakeSieve) ReadQuota(_ context.Context, accountID int64) ([]QuotaValue, error) {
	requireTestAccount(accountID)
	return append([]QuotaValue(nil), f.quota...), nil
}

func requireTestAccount(accountID int64) {
	if accountID != testAccountID {
		panic(fmt.Sprintf("handler leaked a foreign account id: %d", accountID))
	}
}

// sieveBlobs is a minimal BlobReader for uploaded script content.
type sieveBlobs struct{ blobs map[string][]byte }

func (b *sieveBlobs) OpenBlob(_ context.Context, accountID int64, blobID string) (io.ReadCloser, int64, error) {
	requireTestAccount(accountID)
	content, ok := b.blobs[blobID]
	if !ok {
		return nil, 0, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(content)), int64(len(content)), nil
}

// --- harness ---------------------------------------------------------------

type e6Fixture struct {
	deps  *Deps
	sieve *fakeSieve
	blobs *sieveBlobs
}

func newE6Fixture() *e6Fixture {
	fs := newFakeSieve()
	blobs := &sieveBlobs{blobs: map[string][]byte{}}
	deps := &Deps{
		Limits:     jmap.DefaultLimits(),
		Blobs:      blobs,
		Sieve:      fs,
		Vacation:   fs,
		Filters:    fs,
		Forwarding: fs,
		Quota:      fs,
	}
	return &e6Fixture{deps: deps, sieve: fs, blobs: blobs}
}

// e6Call dispatches through the real engine with every E6 capability in
// "using", so the registry gating is exercised, not bypassed.
func (f *e6Fixture) e6Call(t *testing.T, method, args string) map[string]any {
	t.Helper()
	out, errName, errArgs := f.e6CallRaw(t, method, args)
	if errName != "" {
		t.Fatalf("%s answered a method error: %s %s", method, errName, errArgs)
	}
	return out
}

func (f *e6Fixture) e6CallRaw(t *testing.T, method, args string) (map[string]any, string, string) {
	t.Helper()
	registry := jmap.NewRegistry()
	RegisterSieveMethods(registry, f.deps)
	RegisterVacationMethods(registry, f.deps)
	RegisterFilterMethods(registry, f.deps)
	RegisterQuotaMethods(registry, f.deps)
	engine := jmap.NewEngine(registry, jmap.DefaultLimits(),
		[]string{jmap.CapCore, jmap.CapMail, jmap.CapSieve, jmap.CapVacation, jmap.CapQuota, jmap.CapFilters}, nil)

	body := fmt.Sprintf(`{"using":[%q,%q,%q,%q,%q],"methodCalls":[[%q,%s,"c1"]]}`,
		jmap.CapCore, jmap.CapSieve, jmap.CapVacation, jmap.CapQuota, jmap.CapFilters, method, args)
	resp, rerr := engine.Process(callerCtx(), []byte(body), "session-1")
	if rerr != nil {
		t.Fatalf("request-level error: %v", rerr)
	}
	inv := resp.MethodResponses[0]
	var out map[string]any
	if err := json.Unmarshal(inv.Args, &out); err != nil {
		t.Fatalf("decoding args: %v", err)
	}
	if inv.Name == "error" {
		return nil, fmt.Sprint(out["type"]), string(inv.Args)
	}
	return out, "", ""
}

func (f *e6Fixture) uploadScript(id string, content string) string {
	f.blobs.blobs[id] = []byte(content)
	return id
}

// --- SieveScript (RFC 9661) ------------------------------------------------

// §2.3: "This is a standard '/get' method ... The 'ids' argument may be null
// to fetch all scripts at once."
func TestConformanceSieveScriptGetNullIDs(t *testing.T) {
	f := newE6Fixture()
	f.sieve.scripts = []SieveScriptInfo{
		{ID: 1, Name: "moov", Active: true, BlobID: "sha-1", Size: 10},
		{ID: 2, Name: "old", BlobID: "sha-2", Size: 20},
	}
	out := f.e6Call(t, "SieveScript/get", fmt.Sprintf(`{"accountId":%q,"ids":null}`, testAccountJMAPID()))
	list := out["list"].([]any) //nolint:errcheck // engine responses are maps/lists by construction
	if len(list) != 2 {
		t.Fatalf("list = %v, want both scripts", list)
	}
	first := list[0].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	// §2.1: id (server-set), name, blobId, isActive (server-set).
	for _, k := range []string{"id", "name", "blobId", "isActive"} {
		if _, ok := first[k]; !ok {
			t.Errorf("SieveScript object missing %q", k)
		}
	}
	if first["isActive"] != true {
		t.Errorf("isActive = %v for the active script", first["isActive"])
	}
}

// §2.4 create with onSuccessActivateScript naming the creation reference:
// "The id of the SieveScript to activate if and only if all of the
// creations, modifications, and destructions (if any) succeed."
func TestConformanceSieveScriptCreateAndActivate(t *testing.T) {
	f := newE6Fixture()
	blob := f.uploadScript("b1", "keep;\r\n")
	out := f.e6Call(t, "SieveScript/set", fmt.Sprintf(
		`{"accountId":%q,"create":{"new":{"name":"mine","blobId":%q}},"onSuccessActivateScript":"#new"}`,
		testAccountJMAPID(), blob))
	created := out["created"].(map[string]any)["new"].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	if created["id"] == nil {
		t.Fatal("created did not carry the server-set id")
	}
	if len(f.sieve.scripts) != 1 || !f.sieve.scripts[0].Active {
		t.Fatalf("the created script was not activated: %+v", f.sieve.scripts)
	}
}

// §2.4: "The active SieveScript MUST NOT be destroyed unless it is first
// deactivated in a separate SieveScript/set method call." -> sieveIsActive.
func TestConformanceSieveScriptDestroyActiveRefused(t *testing.T) {
	f := newE6Fixture()
	f.sieve.scripts = []SieveScriptInfo{{ID: 1, Name: "mine", Active: true}}
	out := f.e6Call(t, "SieveScript/set", fmt.Sprintf(
		`{"accountId":%q,"destroy":[%q]}`, testAccountJMAPID(), EncodeSieveScriptID(1)))
	nd := out["notDestroyed"].(map[string]any)[EncodeSieveScriptID(1)].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	if nd["type"] != "sieveIsActive" {
		t.Errorf("SetError type = %v, want sieveIsActive (RFC 9661 §2.4)", nd["type"])
	}

	// And the two-call dance works: onSuccessDeactivateScript first, then a
	// separate destroy.
	f.e6Call(t, "SieveScript/set", fmt.Sprintf(
		`{"accountId":%q,"onSuccessDeactivateScript":true}`, testAccountJMAPID()))
	out = f.e6Call(t, "SieveScript/set", fmt.Sprintf(
		`{"accountId":%q,"destroy":[%q]}`, testAccountJMAPID(), EncodeSieveScriptID(1)))
	if destroyed := out["destroyed"].([]any); len(destroyed) != 1 { //nolint:errcheck // engine responses are maps/lists by construction
		t.Fatalf("destroyed = %v after deactivation", destroyed)
	}
}

// §2.4: a create over a taken name MUST be alreadyExists and "An
// 'existingId' property of type 'Id' MUST be included".
func TestConformanceSieveScriptAlreadyExistsCarriesExistingID(t *testing.T) {
	f := newE6Fixture()
	f.sieve.scripts = []SieveScriptInfo{{ID: 5, Name: "mine"}}
	blob := f.uploadScript("b1", "keep;\r\n")
	out := f.e6Call(t, "SieveScript/set", fmt.Sprintf(
		`{"accountId":%q,"create":{"dup":{"name":"mine","blobId":%q}}}`, testAccountJMAPID(), blob))
	nc := out["notCreated"].(map[string]any)["dup"].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	if nc["type"] != "alreadyExists" {
		t.Fatalf("SetError type = %v, want alreadyExists", nc["type"])
	}
	if nc["existingId"] != EncodeSieveScriptID(5) {
		t.Errorf("existingId = %v, want %q (§2.4: MUST be included)", nc["existingId"], EncodeSieveScriptID(5))
	}
}

// §2.4 invalidSieve: "The SieveScript content violates the Sieve grammar...".
func TestConformanceSieveScriptInvalidSieve(t *testing.T) {
	f := newE6Fixture()
	blob := f.uploadScript("b1", "BROKEN\r\n")
	out := f.e6Call(t, "SieveScript/set", fmt.Sprintf(
		`{"accountId":%q,"create":{"bad":{"name":"x","blobId":%q}}}`, testAccountJMAPID(), blob))
	nc := out["notCreated"].(map[string]any)["bad"].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	if nc["type"] != "invalidSieve" {
		t.Fatalf("SetError type = %v, want invalidSieve", nc["type"])
	}
	if !strings.Contains(fmt.Sprint(nc["description"]), "line 1") {
		t.Errorf("the server diagnostic (line numbers) was lost: %v", nc["description"])
	}
}

// RFC 9661 §4: the script materializing the VacationResponse may be fetched
// and activated but "MUST NOT ... be destroyed or have its content updated
// by the SieveScript/set method. Any such request MUST be rejected with a
// 'forbidden' SetError."
func TestConformanceManagedScriptProtectedPerSection4(t *testing.T) {
	f := newE6Fixture()
	f.sieve.scripts = []SieveScriptInfo{{ID: 1, Name: "moov", Active: false}}
	f.sieve.managed = 1
	blob := f.uploadScript("b1", "keep;\r\n")
	wire := EncodeSieveScriptID(1)

	out := f.e6Call(t, "SieveScript/set", fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"blobId":%q}}}`, testAccountJMAPID(), wire, blob))
	nu := out["notUpdated"].(map[string]any)[wire].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	if nu["type"] != "forbidden" {
		t.Errorf("update of the managed script = %v, want forbidden (§4)", nu["type"])
	}

	out = f.e6Call(t, "SieveScript/set", fmt.Sprintf(
		`{"accountId":%q,"destroy":[%q]}`, testAccountJMAPID(), wire))
	nd := out["notDestroyed"].(map[string]any)[wire].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	if nd["type"] != "forbidden" {
		t.Errorf("destroy of the managed script = %v, want forbidden (§4)", nd["type"])
	}

	// But activation is REQUIRED to work (§4: "MUST allow the
	// VacationResponse Sieve script to be activated or deactivated").
	f.e6Call(t, "SieveScript/set", fmt.Sprintf(
		`{"accountId":%q,"onSuccessActivateScript":%q}`, testAccountJMAPID(), wire))
	if !f.sieve.scripts[0].Active {
		t.Error("the managed script could not be activated; §4 requires it")
	}
}

// The GC-4 policy gate: raw content redirecting to an unverified address is
// refused with forbidden, and the gate fails closed.
func TestConformanceRawScriptRedirectPolicy(t *testing.T) {
	f := newE6Fixture()
	blob := f.uploadScript("b1", "redirect-unverified\r\n")
	out := f.e6Call(t, "SieveScript/set", fmt.Sprintf(
		`{"accountId":%q,"create":{"evil":{"name":"x","blobId":%q}}}`, testAccountJMAPID(), blob))
	nc := out["notCreated"].(map[string]any)["evil"].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	if nc["type"] != "forbidden" {
		t.Fatalf("SetError type = %v, want forbidden (GC-4 verified-forward)", nc["type"])
	}
	if len(f.sieve.scripts) != 0 {
		t.Error("the refused script was stored anyway")
	}
}

// §2.6: request {accountId, blobId}, response {accountId, error} where error
// is "An 'invalidSieve' SetError object ... or null".
func TestConformanceSieveScriptValidate(t *testing.T) {
	f := newE6Fixture()
	good := f.uploadScript("g", "keep;\r\n")
	bad := f.uploadScript("b", "BROKEN\r\n")

	out := f.e6Call(t, "SieveScript/validate", fmt.Sprintf(
		`{"accountId":%q,"blobId":%q}`, testAccountJMAPID(), good))
	if out["error"] != nil {
		t.Errorf("valid content answered error = %v, want null", out["error"])
	}
	out = f.e6Call(t, "SieveScript/validate", fmt.Sprintf(
		`{"accountId":%q,"blobId":%q}`, testAccountJMAPID(), bad))
	errObj, ok := out["error"].(map[string]any)
	if !ok || errObj["type"] != "invalidSieve" {
		t.Errorf("invalid content answered %v, want an invalidSieve SetError", out["error"])
	}
}

// §2.5: filter conditions name/isActive, sort by name/isActive; an unknown
// sort property is unsupportedSort.
func TestConformanceSieveScriptQuery(t *testing.T) {
	f := newE6Fixture()
	f.sieve.scripts = []SieveScriptInfo{
		{ID: 1, Name: "zeta"},
		{ID: 2, Name: "alpha", Active: true},
	}
	out := f.e6Call(t, "SieveScript/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"isActive":true},"calculateTotal":true}`, testAccountJMAPID()))
	ids := out["ids"].([]any) //nolint:errcheck // engine responses are maps/lists by construction
	if len(ids) != 1 || ids[0] != EncodeSieveScriptID(2) {
		t.Errorf("ids = %v, want just the active script", ids)
	}
	if out["total"] != float64(1) {
		t.Errorf("total = %v", out["total"])
	}

	out = f.e6Call(t, "SieveScript/query", fmt.Sprintf(
		`{"accountId":%q}`, testAccountJMAPID()))
	ids = out["ids"].([]any) //nolint:errcheck // engine responses are maps/lists by construction
	if len(ids) != 2 || ids[0] != EncodeSieveScriptID(2) {
		t.Errorf("default name sort broken: %v", ids)
	}

	_, errName, _ := f.e6CallRaw(t, "SieveScript/query", fmt.Sprintf(
		`{"accountId":%q,"sort":[{"property":"size"}]}`, testAccountJMAPID()))
	if errName != string(jmap.CodeUnsupportedSort) {
		t.Errorf("sort by size = %v, want unsupportedSort", errName)
	}
}

// The recorded refusal: /changes and /queryChanges answer
// cannotCalculateChanges (RFC 8620 §5.2/§5.6 name the error; the package
// comment in sieve.go carries the reason).
func TestConformanceSieveScriptChangesRefusalIsExplicit(t *testing.T) {
	f := newE6Fixture()
	for _, method := range []string{"SieveScript/changes", "SieveScript/queryChanges"} {
		_, errName, _ := f.e6CallRaw(t, method, fmt.Sprintf(
			`{"accountId":%q,"sinceState":"x"}`, testAccountJMAPID()))
		if errName != string(jmap.CodeCannotCalculateChanges) {
			t.Errorf("%s = %v, want cannotCalculateChanges", method, errName)
		}
	}
}

// --- VacationResponse (RFC 8621 §8) ----------------------------------------

// §8: singleton id, and the §5.1 shape over it.
func TestConformanceVacationSingleton(t *testing.T) {
	f := newE6Fixture()
	out := f.e6Call(t, "VacationResponse/get", fmt.Sprintf(`{"accountId":%q,"ids":null}`, testAccountJMAPID()))
	list := out["list"].([]any) //nolint:errcheck // engine responses are maps/lists by construction
	if len(list) != 1 {
		t.Fatalf("list = %v, want the singleton", list)
	}
	obj := list[0].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	if obj["id"] != "singleton" {
		t.Errorf("id = %v, want singleton (§8)", obj["id"])
	}
	if obj["isEnabled"] != false {
		t.Errorf("a never-configured account must serve isEnabled false, got %v", obj["isEnabled"])
	}
	for _, k := range []string{"fromDate", "toDate", "subject", "textBody", "htmlBody"} {
		if v, ok := obj[k]; !ok || v != nil {
			t.Errorf("%s = %v, want null on a fresh account", k, v)
		}
	}

	// Any other id lands in notFound (§5.1).
	out = f.e6Call(t, "VacationResponse/get", fmt.Sprintf(`{"accountId":%q,"ids":["other"]}`, testAccountJMAPID()))
	if nf := out["notFound"].([]any); len(nf) != 1 || nf[0] != "other" { //nolint:errcheck // engine responses are maps/lists by construction
		t.Errorf("notFound = %v", nf)
	}
}

// §5.3 over the singleton: create and destroy forbidden, update works, and
// the update round-trips through /get.
func TestConformanceVacationSetRoundTrip(t *testing.T) {
	f := newE6Fixture()
	out := f.e6Call(t, "VacationResponse/set", fmt.Sprintf(
		`{"accountId":%q,"update":{"singleton":{"isEnabled":true,"subject":"Fuera",`+
			`"textBody":"Vuelvo el lunes","fromDate":"2026-09-01T00:00:00Z","toDate":"2026-09-15T23:59:59Z"}}}`,
		testAccountJMAPID()))
	if _, ok := out["updated"].(map[string]any)["singleton"]; !ok { //nolint:errcheck // engine responses are maps by construction
		t.Fatalf("update refused: %v", out)
	}

	got := f.e6Call(t, "VacationResponse/get", fmt.Sprintf(`{"accountId":%q}`, testAccountJMAPID()))
	obj := got["list"].([]any)[0].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	if obj["isEnabled"] != true || obj["subject"] != "Fuera" || obj["fromDate"] != "2026-09-01T00:00:00Z" {
		t.Errorf("round trip lost data: %v", obj)
	}

	out = f.e6Call(t, "VacationResponse/set", fmt.Sprintf(
		`{"accountId":%q,"create":{"c1":{}},"destroy":["singleton"]}`, testAccountJMAPID()))
	if nc := out["notCreated"].(map[string]any)["c1"].(map[string]any); nc["type"] != "forbidden" { //nolint:errcheck // engine responses are maps/lists by construction
		t.Errorf("create = %v, want forbidden (singleton)", nc["type"])
	}
	if nd := out["notDestroyed"].(map[string]any)["singleton"].(map[string]any); nd["type"] != "forbidden" { //nolint:errcheck // engine responses are maps/lists by construction
		t.Errorf("destroy = %v, want forbidden (singleton)", nd["type"])
	}
}

// The recorded restrictToContacts decision: refused with invalidProperties,
// never silently absorbed — with the reason in the description.
func TestConformanceVacationRestrictToContactsRefused(t *testing.T) {
	f := newE6Fixture()
	out := f.e6Call(t, "VacationResponse/set", fmt.Sprintf(
		`{"accountId":%q,"update":{"singleton":{"isEnabled":false,"restrictToContacts":true}}}`,
		testAccountJMAPID()))
	nu := out["notUpdated"].(map[string]any)["singleton"].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	if nu["type"] != "invalidProperties" {
		t.Fatalf("restrictToContacts = %v, want invalidProperties", nu["type"])
	}
	if !strings.Contains(fmt.Sprint(nu["description"]), "contacts subsystem") {
		t.Errorf("the refusal does not carry its reason: %v", nu["description"])
	}
	if f.sieve.vacState != 0 {
		t.Error("a refused update still reached the store (store-and-ignore is forbidden)")
	}
}

// The canon's subject-or-body rule: enabling with all three null is refused.
func TestConformanceVacationEnableNeedsContent(t *testing.T) {
	f := newE6Fixture()
	out := f.e6Call(t, "VacationResponse/set", fmt.Sprintf(
		`{"accountId":%q,"update":{"singleton":{"isEnabled":true}}}`, testAccountJMAPID()))
	nu := out["notUpdated"].(map[string]any)["singleton"].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	if nu["type"] != "invalidProperties" {
		t.Errorf("empty enable = %v, want invalidProperties", nu["type"])
	}
}

// htmlBody is sanitized server-side (the identity-signature sanitizer): a
// script never reaches the stored object, and §5.3's updated map reports the
// rewritten property.
func TestConformanceVacationHTMLBodySanitized(t *testing.T) {
	f := newE6Fixture()
	out := f.e6Call(t, "VacationResponse/set", fmt.Sprintf(
		`{"accountId":%q,"update":{"singleton":{"isEnabled":true,`+
			`"htmlBody":"<p>ok</p><script>alert(1)</script>"}}}`, testAccountJMAPID()))
	updated, ok := out["updated"].(map[string]any)["singleton"].(map[string]any)
	if !ok {
		t.Fatalf("update refused: %v", out)
	}
	served := fmt.Sprint(updated["htmlBody"])
	if strings.Contains(served, "<script") {
		t.Fatalf("the reported htmlBody still contains a script: %q", served)
	}
	if f.sieve.vacation.HTMLBody == nil || strings.Contains(*f.sieve.vacation.HTMLBody, "<script") {
		t.Fatal("the STORED htmlBody was not sanitized")
	}
}

// --- the vendor filter surface ---------------------------------------------

// FilterRule/set create + the scriptActive honesty bit on /get.
func TestConformanceFilterRulesAndScriptActive(t *testing.T) {
	f := newE6Fixture()
	out := f.e6Call(t, "FilterRule/set", fmt.Sprintf(
		`{"accountId":%q,"create":{"n":{"type":"blocked","from":["bad@spam.example"]}}}`,
		testAccountJMAPID()))
	created := out["created"].(map[string]any)["n"].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	ruleID := fmt.Sprint(created["id"])
	if ruleID == "" || ruleID == "<nil>" {
		t.Fatal("no server-set rule id")
	}

	f.sieve.active = false // another script took the active slot
	got := f.e6Call(t, "FilterRule/get", fmt.Sprintf(`{"accountId":%q}`, testAccountJMAPID()))
	if got["scriptActive"] != false {
		t.Error("scriptActive did not report the foreign-active state; the UI would pretend the rules filter mail")
	}
	list := got["list"].([]any)                                          //nolint:errcheck // engine responses are maps/lists by construction
	if len(list) != 1 || list[0].(map[string]any)["type"] != "blocked" { //nolint:errcheck // engine responses are maps/lists by construction
		t.Errorf("list = %v", list)
	}
}

// The verified-forward rule holds through the vendor surface too: a rule
// forwarding to an unverified address is refused, and verification unlocks
// it.
func TestConformanceFilterForwardRequiresVerifiedAddress(t *testing.T) {
	f := newE6Fixture()
	_, errName, errArgs := f.e6CallRaw(t, "FilterRule/set", fmt.Sprintf(
		`{"accountId":%q,"create":{"n":{"type":"filter","from":["a@b.c"],"forward":"dest@x.example"}}}`,
		testAccountJMAPID()))
	if errName != string(jmap.CodeInvalidArguments) {
		t.Fatalf("unverified forward = %v (%s), want invalidArguments carrying the model refusal", errName, errArgs)
	}

	// Verify the address (create + token), then the same rule is accepted.
	f.e6Call(t, "ForwardingAddress/set", fmt.Sprintf(
		`{"accountId":%q,"create":{"a":{"email":"dest@x.example"}}}`, testAccountJMAPID()))
	if _, err := f.sieve.VerifyForwarding(context.Background(), testAccountID, "token-for-dest@x.example"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	out := f.e6Call(t, "FilterRule/set", fmt.Sprintf(
		`{"accountId":%q,"create":{"n":{"type":"filter","from":["a@b.c"],"forward":"dest@x.example"}}}`,
		testAccountJMAPID()))
	if _, ok := out["created"].(map[string]any)["n"]; !ok { //nolint:errcheck // engine responses are maps by construction
		t.Fatalf("verified forward still refused: %v", out)
	}
}

// ForwardingAddress lifecycle: create -> pending; destroy of an in-use
// address refused; update always forbidden.
func TestConformanceForwardingAddressLifecycle(t *testing.T) {
	f := newE6Fixture()
	out := f.e6Call(t, "ForwardingAddress/set", fmt.Sprintf(
		`{"accountId":%q,"create":{"a":{"email":"Dest@X.Example"}}}`, testAccountJMAPID()))
	created := out["created"].(map[string]any)["a"].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	if created["state"] != "pending" {
		t.Errorf("state = %v, want pending until the token comes back", created["state"])
	}
	wire := fmt.Sprint(created["id"])

	// The email was normalized to lowercase.
	got := f.e6Call(t, "ForwardingAddress/get", fmt.Sprintf(`{"accountId":%q}`, testAccountJMAPID()))
	if got["list"].([]any)[0].(map[string]any)["email"] != "dest@x.example" { //nolint:errcheck // engine responses are maps/lists by construction
		t.Errorf("email was not normalized: %v", got["list"])
	}

	out = f.e6Call(t, "ForwardingAddress/set", fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"email":"other@x"}}}`, testAccountJMAPID(), wire))
	if nu := out["notUpdated"].(map[string]any)[wire].(map[string]any); nu["type"] != "forbidden" { //nolint:errcheck // engine responses are maps/lists by construction
		t.Errorf("update = %v, want forbidden", nu["type"])
	}

	// Make it verified and referenced, then destroying is refused.
	if _, err := f.sieve.VerifyForwarding(context.Background(), testAccountID, "token-for-dest@x.example"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	f.e6Call(t, "Forwarding/set", fmt.Sprintf(
		`{"accountId":%q,"update":{"singleton":{"enabled":true,"address":"dest@x.example"}}}`,
		testAccountJMAPID()))
	out = f.e6Call(t, "ForwardingAddress/set", fmt.Sprintf(
		`{"accountId":%q,"destroy":[%q]}`, testAccountJMAPID(), wire))
	if nd := out["notDestroyed"].(map[string]any)[wire].(map[string]any); nd["type"] != "forbidden" { //nolint:errcheck // engine responses are maps/lists by construction
		t.Errorf("destroy of an in-use address = %v, want forbidden", nd["type"])
	}
}

// Forwarding singleton: enabling with an unverified address is refused
// through the model's enforcement.
func TestConformanceForwardAllRequiresVerifiedAddress(t *testing.T) {
	f := newE6Fixture()
	out := f.e6Call(t, "Forwarding/set", fmt.Sprintf(
		`{"accountId":%q,"update":{"singleton":{"enabled":true,"address":"nobody@x.example"}}}`,
		testAccountJMAPID()))
	nu := out["notUpdated"].(map[string]any)["singleton"].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	if nu["type"] != "invalidProperties" {
		t.Errorf("unverified forward-all = %v, want invalidProperties", nu["type"])
	}
}

// --- Quota (RFC 9425) ------------------------------------------------------

// §4.2 over the live values, with §4.1's required properties and the
// documented enums (§3.1 scope, §3.2 resourceType).
func TestConformanceQuotaGet(t *testing.T) {
	f := newE6Fixture()
	f.sieve.quota = []QuotaValue{
		{Name: "User quota", ResourceType: "octets", Used: 512 * 1024, HardLimit: 3 * 1024 * 1024 * 1024},
		{Name: "User quota", ResourceType: "count", Used: 42, HardLimit: 100000},
	}
	out := f.e6Call(t, "Quota/get", fmt.Sprintf(`{"accountId":%q,"ids":null}`, testAccountJMAPID()))
	list := out["list"].([]any) //nolint:errcheck // engine responses are maps/lists by construction
	if len(list) != 2 {
		t.Fatalf("list = %v", list)
	}
	storage := list[0].(map[string]any) //nolint:errcheck // engine responses are maps/lists by construction
	if storage["id"] != "storage" || storage["resourceType"] != "octets" ||
		storage["scope"] != "account" || storage["used"] != float64(512*1024) ||
		storage["hardLimit"] != float64(3*1024*1024*1024) {
		t.Errorf("storage quota = %v", storage)
	}
	types := storage["types"].([]any) //nolint:errcheck // engine responses are maps/lists by construction
	if len(types) != 1 || types[0] != "Email" {
		t.Errorf("types = %v, want [Email]", types)
	}

	// An unlimited account: EMPTY list, no fabricated hardLimit.
	f.sieve.quota = nil
	out = f.e6Call(t, "Quota/get", fmt.Sprintf(`{"accountId":%q,"ids":null}`, testAccountJMAPID()))
	if list := out["list"].([]any); len(list) != 0 { //nolint:errcheck // engine responses are maps/lists by construction
		t.Errorf("an unlimited account served %v, want no objects", list)
	}
}

// §4.4 filter/sort, and the recorded /changes refusal (§4.3/§4.5).
func TestConformanceQuotaQueryAndChangesRefusal(t *testing.T) {
	f := newE6Fixture()
	f.sieve.quota = []QuotaValue{
		{Name: "User quota", ResourceType: "octets", Used: 10, HardLimit: 100},
		{Name: "User quota", ResourceType: "count", Used: 5, HardLimit: 50},
	}
	out := f.e6Call(t, "Quota/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"resourceType":"count"}}`, testAccountJMAPID()))
	ids := out["ids"].([]any) //nolint:errcheck // engine responses are maps/lists by construction
	if len(ids) != 1 || ids[0] != "message" {
		t.Errorf("ids = %v", ids)
	}

	for _, method := range []string{"Quota/changes", "Quota/queryChanges"} {
		_, errName, _ := f.e6CallRaw(t, method, fmt.Sprintf(
			`{"accountId":%q,"sinceState":"x"}`, testAccountJMAPID()))
		if errName != string(jmap.CodeCannotCalculateChanges) {
			t.Errorf("%s = %v, want cannotCalculateChanges", method, errName)
		}
	}
}

// --- capability gating -----------------------------------------------------

// RFC 8620 §1.8: a request that does not opt into a capability must not see
// its methods. The registry gates on "using", so an E6 method without the E6
// capability is unknownMethod.
func TestConformanceE6MethodsAreCapabilityGated(t *testing.T) {
	f := newE6Fixture()
	registry := jmap.NewRegistry()
	RegisterSieveMethods(registry, f.deps)
	RegisterVacationMethods(registry, f.deps)
	RegisterQuotaMethods(registry, f.deps)
	RegisterFilterMethods(registry, f.deps)
	engine := jmap.NewEngine(registry, jmap.DefaultLimits(),
		[]string{jmap.CapCore, jmap.CapMail, jmap.CapSieve, jmap.CapVacation, jmap.CapQuota, jmap.CapFilters}, nil)

	body := fmt.Sprintf(`{"using":[%q,%q],"methodCalls":[["SieveScript/get",{"accountId":%q},"c1"]]}`,
		jmap.CapCore, jmap.CapMail, testAccountJMAPID())
	resp, rerr := engine.Process(callerCtx(), []byte(body), "session-1")
	if rerr != nil {
		t.Fatalf("request-level error: %v", rerr)
	}
	inv := resp.MethodResponses[0]
	if inv.Name != "error" || !bytes.Contains(inv.Args, []byte("unknownMethod")) {
		t.Fatalf("SieveScript/get without the sieve capability answered %s %s; §1.8 requires the server "+
			"to behave as though it does not implement it", inv.Name, inv.Args)
	}
}
