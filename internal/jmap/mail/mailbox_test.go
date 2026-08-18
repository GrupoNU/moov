package mail

import (
	"encoding/json"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
)

// callGet invokes a handler with the given arguments and returns the decoded
// response, failing the test on a method error.
func callGet(t *testing.T, h jmap.Handler, args string) map[string]any {
	t.Helper()
	res, merr := h(callerCtx(), json.RawMessage(args))
	if merr != nil {
		t.Fatalf("handler returned %v", merr)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshaling result: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	return out
}

// firstObject returns the nth object of a /get response's list, failing the
// test if the response is not shaped like one.
//
// It exists so the tests can navigate a decoded JSON response without an
// unchecked type assertion at every step: a panic in a test tells you a lot
// less than a message naming which property was the wrong type.
func firstObject(t *testing.T, resp map[string]any, n int) map[string]any {
	t.Helper()
	list, ok := resp["list"].([]any)
	if !ok {
		t.Fatalf("list is %T, want an array", resp["list"])
	}
	if n >= len(list) {
		t.Fatalf("list has %d entries, wanted index %d", len(list), n)
	}
	obj, ok := list[n].(map[string]any)
	if !ok {
		t.Fatalf("list[%d] is %T, want an object", n, list[n])
	}
	return obj
}

// object extracts a nested object property.
func object(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want an object", key, parent[key])
	}
	return v
}

// array extracts a nested array property.
func array(t *testing.T, parent map[string]any, key string) []any {
	t.Helper()
	v, ok := parent[key].([]any)
	if !ok {
		t.Fatalf("%s is %T, want an array", key, parent[key])
	}
	return v
}

// callGetErr invokes a handler expecting a method error.
func callGetErr(t *testing.T, h jmap.Handler, args string) *jmap.MethodError {
	t.Helper()
	res, merr := h(callerCtx(), json.RawMessage(args))
	if merr == nil {
		t.Fatalf("expected a method error, got result %+v", res)
	}
	return merr
}

func TestMailboxGetReturnsAllStandardProperties(t *testing.T) {
	f := newFakeReaders()
	f.mailboxes[testAccountID] = []MailboxRow{
		{
			ID: 1, Name: "INBOX", Role: "inbox", SortOrder: 10, IsSubscribed: true,
			TotalEmails: 42, UnreadEmails: 7, TotalThreads: 40, UnreadThreads: 6,
		},
	}
	d := f.deps()

	got := callGet(t, d.handleMailboxGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":null}`)

	if got["accountId"] != testAccountJMAPID() {
		t.Errorf("accountId = %v", got["accountId"])
	}
	if got["state"] != "state-1" {
		t.Errorf("state = %v", got["state"])
	}
	list, _ := got["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("list has %d entries, want 1", len(list))
	}
	mb, _ := list[0].(map[string]any)

	// Every property of RFC 8621 §2 must be present when properties is null.
	want := map[string]any{
		"id":            EncodeMailboxID(1),
		"name":          "INBOX",
		"parentId":      nil,
		"role":          "inbox",
		"sortOrder":     float64(10),
		"totalEmails":   float64(42),
		"unreadEmails":  float64(7),
		"totalThreads":  float64(40),
		"unreadThreads": float64(6),
		"isSubscribed":  true,
	}
	for k, v := range want {
		if mb[k] != v {
			t.Errorf("%s = %#v, want %#v", k, mb[k], v)
		}
	}

	rights, ok := mb["myRights"].(map[string]any)
	if !ok {
		t.Fatalf("myRights is %T", mb["myRights"])
	}
	// Truthful per W2 (L2-jmap-write §3). This fixture is the INBOX, a
	// protected role: everything Email/set and Mailbox/set honor for it is
	// true, and the two operations Mailbox/set REFUSES on a role folder —
	// rename and delete — are false, so a client never offers an action that
	// will be denied.
	for _, r := range []string{
		"mayReadItems", "mayAddItems", "mayRemoveItems", "maySetSeen", "maySetKeywords",
		"mayCreateChild",
	} {
		if rights[r] != true {
			t.Errorf("%s = %v, want true", r, rights[r])
		}
	}
	for _, r := range []string{"mayRename", "mayDelete"} {
		if rights[r] != false {
			t.Errorf("%s = %v, want false (Mailbox/set refuses it on a protected role)", r, rights[r])
		}
	}
	if rights["maySubmit"] != false {
		t.Errorf("maySubmit = %v, want false (submission is W3)", rights["maySubmit"])
	}
	// All nine members of MailboxRights are present, not just the true ones.
	if len(rights) != 9 {
		t.Errorf("myRights has %d members, want the 9 of RFC 8621 §2", len(rights))
	}
}

func TestMailboxGetNotFoundAndForeignAccountAreIndistinguishable(t *testing.T) {
	f := newFakeReaders()
	f.mailboxes[testAccountID] = []MailboxRow{sampleMailbox(1, "INBOX", "inbox", 1, 0)}
	// A mailbox of ANOTHER account, with an id the caller will ask for.
	f.mailboxes[otherAccountID] = []MailboxRow{sampleMailbox(99, "Secret", "", 5, 5)}
	d := f.deps()

	got := callGet(t, d.handleMailboxGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+
			EncodeMailboxID(1)+`","`+EncodeMailboxID(99)+`","`+EncodeMailboxID(1234)+`","garbage"]}`)

	list, _ := got["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("list has %d entries, want only the caller's own mailbox", len(list))
	}
	notFound, _ := got["notFound"].([]any)
	if len(notFound) != 3 {
		t.Fatalf("notFound = %v, want the foreign id, the missing id and the garbage id", notFound)
	}
	// The foreign mailbox must appear in notFound exactly like a missing one:
	// no error, no distinct signal.
	seen := map[string]bool{}
	for _, v := range notFound {
		s, _ := v.(string)
		seen[s] = true
	}
	for _, id := range []string{EncodeMailboxID(99), EncodeMailboxID(1234), "garbage"} {
		if !seen[id] {
			t.Errorf("notFound is missing %q", id)
		}
	}
}

func TestMailboxGetSelectiveProperties(t *testing.T) {
	f := newFakeReaders()
	f.mailboxes[testAccountID] = []MailboxRow{sampleMailbox(1, "INBOX", "inbox", 3, 1)}
	d := f.deps()

	got := callGet(t, d.handleMailboxGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":null,"properties":["name","totalEmails"]}`)

	mb := firstObject(t, got, 0)
	// id is always returned even when not requested (RFC 8620 §5.1).
	if _, ok := mb["id"]; !ok {
		t.Error("id must always be returned")
	}
	if mb["name"] != "INBOX" || mb["totalEmails"] != float64(3) {
		t.Errorf("requested properties missing: %#v", mb)
	}
	for _, unwanted := range []string{"role", "myRights", "unreadEmails", "isSubscribed"} {
		if _, ok := mb[unwanted]; ok {
			t.Errorf("%s was returned but not requested", unwanted)
		}
	}
}

func TestMailboxGetUnknownPropertyIsInvalidArguments(t *testing.T) {
	d := newFakeReaders().deps()
	merr := callGetErr(t, d.handleMailboxGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":null,"properties":["name","nope"]}`)
	if merr.Code != jmap.CodeInvalidArguments {
		t.Fatalf("code = %s, want invalidArguments", merr.Code)
	}
}

func TestMailboxGetForeignAccountIdIsAccountNotFound(t *testing.T) {
	d := newFakeReaders().deps()
	merr := callGetErr(t, d.handleMailboxGet,
		`{"accountId":"`+jmap.EncodeAccountID(otherAccountID)+`","ids":null}`)
	if merr.Code != jmap.CodeAccountNotFound {
		t.Fatalf("code = %s, want accountNotFound", merr.Code)
	}
}

func TestMailboxGetMissingAccountIdIsInvalidArguments(t *testing.T) {
	d := newFakeReaders().deps()
	merr := callGetErr(t, d.handleMailboxGet, `{"ids":null}`)
	if merr.Code != jmap.CodeInvalidArguments {
		t.Fatalf("code = %s, want invalidArguments", merr.Code)
	}
}

// The maxObjectsInGet enforcement point, at the handler level. The HTTP-level
// proof lives in internal/jmaphttp (server_test.go).
func TestMailboxGetEnforcesMaxObjectsInGet(t *testing.T) {
	f := newFakeReaders()
	d := f.deps()
	d.Limits.MaxObjectsInGet = 2

	merr := callGetErr(t, d.handleMailboxGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["m1","m2","m3"]}`)
	if merr.Code != jmap.CodeRequestTooLarge {
		t.Fatalf("code = %s, want requestTooLarge", merr.Code)
	}

	// At the limit it passes.
	callGet(t, d.handleMailboxGet, `{"accountId":"`+testAccountJMAPID()+`","ids":["m1","m2"]}`)
}

// A /get whose reader fails must not leak the underlying error to the client.
func TestMailboxGetReaderErrorIsServerFailWithoutDetail(t *testing.T) {
	f := newFakeReaders()
	f.err = errSecret
	d := f.deps()

	merr := callGetErr(t, d.handleMailboxGet, `{"accountId":"`+testAccountJMAPID()+`","ids":null}`)
	if merr.Code != jmap.CodeServerFail {
		t.Fatalf("code = %s, want serverFail", merr.Code)
	}
	if contains(merr.Description, "constraint") || contains(merr.Description, errSecret.Error()) {
		t.Fatalf("description leaks the internal error: %q", merr.Description)
	}
}

func TestMailboxGetWithoutCallerIsForbidden(t *testing.T) {
	d := newFakeReaders().deps()
	// No caller in the context: a transport wiring bug.
	_, merr := d.handleMailboxGet(contextWithoutCaller(), json.RawMessage(`{"accountId":"a7"}`))
	if merr == nil || merr.Code != jmap.CodeForbidden {
		t.Fatalf("merr = %v, want forbidden", merr)
	}
}

func TestMailboxRoleMapping(t *testing.T) {
	// The S1-validated SPECIAL-USE roles must each map to their JMAP name.
	for _, role := range []string{"inbox", "archive", "drafts", "sent", "junk", "trash", "all", "flagged"} {
		got, ok := jmapRole(role)
		if !ok || got != role {
			t.Errorf("role %q mapped to (%q, %v)", role, got, ok)
		}
	}
	// An ordinary folder has no role, rendered as null.
	if _, ok := jmapRole(""); ok {
		t.Error("the empty role must not map to a JMAP role")
	}
	// An unrecognized role is not invented into existence.
	if _, ok := jmapRole("important"); ok {
		t.Error("an unknown role must not map")
	}
}

func TestMailboxHierarchyNameAndParent(t *testing.T) {
	byName := map[string]int64{"INBOX": 1, "INBOX/Work": 2}

	// RFC 8621 §2: name "MUST NOT be the full path".
	if got := leafName("INBOX/Work/2026", "/"); got != "2026" {
		t.Errorf("leafName = %q, want the leaf only", got)
	}
	if got := leafName("INBOX", "/"); got != "INBOX" {
		t.Errorf("leafName of a top-level box = %q", got)
	}
	if got := parentID("INBOX/Work", "/", byName); got != 1 {
		t.Errorf("parentID = %d, want 1", got)
	}
	if got := parentID("INBOX", "/", byName); got != 0 {
		t.Errorf("a top-level mailbox must have parent 0, got %d", got)
	}
	// A hierarchy gap (the parent path is not itself a mailbox) is top level,
	// because JMAP cannot name a parent that does not exist.
	if got := parentID("Missing/Child", "/", byName); got != 0 {
		t.Errorf("orphan parent = %d, want 0", got)
	}
	// A dot-delimited server is handled by the stored delimiter, not a guess.
	if got := leafName("INBOX.Work", "."); got != "Work" {
		t.Errorf("dot delimiter: leafName = %q", got)
	}
}

func contextWithoutCaller() contextNoCaller { return contextNoCaller{} }
