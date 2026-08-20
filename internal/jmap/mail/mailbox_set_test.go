package mail

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Mailbox/set handler tests (W2): the RFC 8621 §2.5 create/update/destroy
// grammar over the shared §5.3 skeleton, against the fake writer. The
// executor's own IMAP behavior — Dovecot-first ordering, echo convergence,
// the store reflection that keeps an id stable — has its suite in
// internal/sync; this file tests the decisions the protocol layer makes.

// mailboxSetFixture seeds an account with a role folder, two ordinary ones and
// a nested pair, which is enough to exercise every shape.
//
//	INBOX          (role inbox, protected, 2 messages)
//	Trash          (role trash, protected)
//	Work           (ordinary, 0 messages)
//	Work/2026      (ordinary, child of Work)
//	Receipts       (ordinary, 3 messages)
func mailboxSetFixture() (*fakeReaders, *Deps) {
	f := newFakeReaders()
	f.mailboxes[testAccountID] = []MailboxRow{
		sampleMailbox(1, "INBOX", "inbox", 2, 1),
		sampleMailbox(2, "Trash", "trash", 0, 0),
		sampleMailbox(3, "Work", "", 0, 0),
		{ID: 4, Name: "2026", ParentID: 3, SortOrder: 100, IsSubscribed: true},
		sampleMailbox(5, "Receipts", "", 3, 0),
	}
	f.emails[testAccountID] = []EmailRow{sampleEmail(10, "first")}
	f.mailboxes[otherAccountID] = []MailboxRow{sampleMailbox(90, "Secret", "", 0, 0)}
	return f, f.deps()
}

func mailboxID(id int64) string { return EncodeMailboxID(id) }

// ---------------------------------------------------------------------------
// create
// ---------------------------------------------------------------------------

func TestMailboxSetCreateComposesTheFullPathFromParentId(t *testing.T) {
	f, d := mailboxSetFixture()

	resp := callGet(t, d.handleMailboxSet, setArgs(fmt.Sprintf(
		`"create":{"c1":{"name":"Invoices","parentId":%q}}`, mailboxID(3))))

	created := object(t, resp, "created")
	obj, ok := created["c1"].(map[string]any)
	if !ok {
		t.Fatalf("created lacks c1: %v", resp)
	}
	if obj["id"] == nil || obj["id"] == "" {
		t.Error("the created object carries no server-assigned id (RFC 8620 §5.3)")
	}
	// §2 types role, sortOrder, the counts and myRights as server-set, so §5.3
	// requires them back in the created object.
	if obj["role"] != nil {
		t.Errorf("a client-created mailbox must have no role, got %v", obj["role"])
	}
	if _, ok := obj["myRights"]; !ok {
		t.Error("the created object omits myRights, which the client needs to know what it may do")
	}

	if len(f.createMailboxCalls) != 1 {
		t.Fatalf("writer received %d create calls, want 1", len(f.createMailboxCalls))
	}
	// THE assertion: JMAP's leaf name + parentId became the full IMAP path.
	if got := f.createMailboxCalls[0].name; got != "Work/Invoices" {
		t.Errorf("the executor was asked to create %q, want %q", got, "Work/Invoices")
	}
	if !f.createMailboxCalls[0].subscribe {
		t.Error("a mailbox a user just created must be subscribed, or it is invisible in their client")
	}
}

func TestMailboxSetCreateAtTopLevelWithNullParent(t *testing.T) {
	f, d := mailboxSetFixture()

	callGet(t, d.handleMailboxSet, setArgs(`"create":{"c1":{"name":"Personal","parentId":null}}`))

	if len(f.createMailboxCalls) != 1 || f.createMailboxCalls[0].name != "Personal" {
		t.Fatalf("create calls = %+v, want one for %q", f.createMailboxCalls, "Personal")
	}
}

func TestMailboxSetCreateOmittingParentIdIsTopLevel(t *testing.T) {
	f, d := mailboxSetFixture()

	callGet(t, d.handleMailboxSet, setArgs(`"create":{"c1":{"name":"Personal"}}`))

	if len(f.createMailboxCalls) != 1 || f.createMailboxCalls[0].name != "Personal" {
		t.Fatalf("create calls = %+v, want one for %q", f.createMailboxCalls, "Personal")
	}
}

func TestMailboxSetCreateRefusesADuplicatePathBeforeTheWrite(t *testing.T) {
	f, d := mailboxSetFixture()

	resp := callGet(t, d.handleMailboxSet, setArgs(fmt.Sprintf(
		`"create":{"c1":{"name":"2026","parentId":%q}}`, mailboxID(3))))

	serr := setErrorOf(t, resp, "notCreated", "c1")
	if serr["type"] != setErrInvalidProperties {
		t.Fatalf("type = %v, want invalidProperties", serr["type"])
	}
	if len(f.createMailboxCalls) != 0 {
		t.Error("a duplicate name reached the executor; the collision must be caught before the write")
	}
}

func TestMailboxSetCreateRefusesARoleTheClientPicked(t *testing.T) {
	f, d := mailboxSetFixture()

	resp := callGet(t, d.handleMailboxSet, setArgs(
		`"create":{"c1":{"name":"MySent","role":"sent"}}`))

	serr := setErrorOf(t, resp, "notCreated", "c1")
	if serr["type"] != setErrInvalidProperties {
		t.Fatalf("type = %v, want invalidProperties", serr["type"])
	}
	// RFC 8621 §2 types role as "immutable; server-set"; the refusal must say
	// so rather than dropping the property silently.
	if props := serr["properties"]; props == nil || !strings.Contains(fmt.Sprint(props), "role") {
		t.Errorf("the error does not name role: %v", serr)
	}
	if len(f.createMailboxCalls) != 0 {
		t.Error("a role-setting create reached the executor")
	}
}

func TestMailboxSetCreateAcceptsAnExplicitNullRole(t *testing.T) {
	// A round-tripping client sends back the whole object it read, including
	// role:null. Refusing that would break every such client for no reason:
	// null is what this server assigns anyway.
	_, d := mailboxSetFixture()

	resp := callGet(t, d.handleMailboxSet, setArgs(
		`"create":{"c1":{"name":"Roundtrip","role":null}}`))

	if _, ok := object(t, resp, "created")["c1"]; !ok {
		t.Fatalf("role:null was refused: %v", resp)
	}
}

func TestMailboxSetCreateRefusesServerSetProperties(t *testing.T) {
	for _, prop := range []string{
		`"totalEmails":5`, `"unreadEmails":1`, `"totalThreads":5`, `"unreadThreads":1`,
		`"sortOrder":3`, `"id":"Mb-1"`, `"myRights":{}`,
	} {
		t.Run(prop, func(t *testing.T) {
			_, d := mailboxSetFixture()
			resp := callGet(t, d.handleMailboxSet, setArgs(
				`"create":{"c1":{"name":"X",`+prop+`}}`))
			serr := setErrorOf(t, resp, "notCreated", "c1")
			if serr["type"] != setErrInvalidProperties {
				t.Fatalf("type = %v, want invalidProperties for %s", serr["type"], prop)
			}
		})
	}
}

func TestMailboxSetCreateRefusesANameCarryingTheDelimiter(t *testing.T) {
	// RFC 8621 §2: name "MUST NOT be the full path". A leaf carrying "/" would
	// silently place the folder somewhere other than where the client asked.
	f, d := mailboxSetFixture()

	resp := callGet(t, d.handleMailboxSet, setArgs(
		`"create":{"c1":{"name":"Work/Sneaky"}}`))

	serr := setErrorOf(t, resp, "notCreated", "c1")
	if serr["type"] != setErrInvalidProperties {
		t.Fatalf("type = %v, want invalidProperties", serr["type"])
	}
	if len(f.createMailboxCalls) != 0 {
		t.Error("a path-shaped leaf name reached the executor")
	}
}

func TestMailboxSetCreateRefusesUnrepresentableNames(t *testing.T) {
	cases := map[string]string{
		"empty":      "",
		"whitespace": "   ",
		// A real CRLF, which is what a command-injection attempt looks like
		// on the wire — the escaped-looking two-character sequence is a
		// different (and harmless) input.
		//
		// A NUL is deliberately NOT tested here: encoding/json rejects it
		// before any handler sees it, so the case belongs to the JSON layer.
		// validateLeafName still refuses it, and internal/imap's own suite
		// covers the primitive.
		"crlf": "Work\r\nA1 DELETE INBOX",
	}
	for label, name := range cases {
		t.Run(label, func(t *testing.T) {
			f, d := mailboxSetFixture()
			resp := callGet(t, d.handleMailboxSet, setArgs(
				fmt.Sprintf(`"create":{"c1":{"name":%q}}`, name)))
			notCreated, ok := resp["notCreated"].(map[string]any)
			if !ok {
				t.Fatalf("the name %q was accepted: %v", name, resp)
			}
			if _, ok := notCreated["c1"]; !ok {
				t.Fatalf("the name %q was accepted: %v", name, resp)
			}
			if len(f.createMailboxCalls) != 0 {
				t.Error("an unrepresentable name reached the executor")
			}
		})
	}
}

func TestMailboxSetCreateRefusesAForeignParent(t *testing.T) {
	f, d := mailboxSetFixture()

	// Mailbox 90 belongs to the OTHER account. It must be indistinguishable
	// from one that does not exist — never an existence oracle.
	resp := callGet(t, d.handleMailboxSet, setArgs(fmt.Sprintf(
		`"create":{"c1":{"name":"Sneaky","parentId":%q}}`, mailboxID(90))))

	serr := setErrorOf(t, resp, "notCreated", "c1")
	if serr["type"] != setErrInvalidProperties {
		t.Fatalf("type = %v, want invalidProperties", serr["type"])
	}
	if len(f.createMailboxCalls) != 0 {
		t.Error("a create under another account's mailbox reached the executor")
	}
}

func TestMailboxSetCreateSeesTheFolderTheEarlierCreateMade(t *testing.T) {
	// Two creates in one call, the second nested under the first is not
	// possible without back-references, but the second must at least see the
	// first's NAME for collision detection — the tree is re-read per mutation.
	f, d := mailboxSetFixture()

	resp := callGet(t, d.handleMailboxSet, setArgs(
		`"create":{"c1":{"name":"Twice"},"c2":{"name":"Twice"}}`))

	if _, ok := object(t, resp, "created")["c1"]; !ok {
		t.Fatalf("the first create failed: %v", resp)
	}
	serr := setErrorOf(t, resp, "notCreated", "c2")
	if serr["type"] != setErrInvalidProperties {
		t.Fatalf("the second create of the same name was not refused: %v", serr)
	}
	if len(f.createMailboxCalls) != 1 {
		t.Errorf("the executor was called %d times, want 1", len(f.createMailboxCalls))
	}
}

// ---------------------------------------------------------------------------
// update: rename and re-parent
// ---------------------------------------------------------------------------

func TestMailboxSetUpdateRenamesAndKeepsTheId(t *testing.T) {
	f, d := mailboxSetFixture()
	wire := mailboxID(3)

	resp := callGet(t, d.handleMailboxSet, setArgs(
		`"update":{"`+wire+`":{"name":"Projects"}}`))

	if _, ok := object(t, resp, "updated")[wire]; !ok {
		t.Fatalf("updated lacks the id: %v", resp)
	}
	if len(f.renameMailboxCalls) != 1 {
		t.Fatalf("writer received %d rename calls, want 1", len(f.renameMailboxCalls))
	}
	call := f.renameMailboxCalls[0]
	if call.mailboxID != 3 || call.newName != "Projects" {
		t.Errorf("call = %+v, want mailbox 3 renamed to Projects", call)
	}

	// THE W2 property: the JMAP id survived. A client that held Mb-3 still
	// holds a valid id for the same folder under its new name.
	after := callGet(t, d.handleMailboxGet, setArgs(`"ids":["`+wire+`"]`))
	obj := firstObject(t, after, 0)
	if obj["id"] != wire {
		t.Errorf("the mailbox id changed across a rename: %v", obj["id"])
	}
	if obj["name"] != "Projects" {
		t.Errorf("name = %v, want Projects", obj["name"])
	}
}

func TestMailboxSetUpdateReParentsByComposingTheNewPath(t *testing.T) {
	f, d := mailboxSetFixture()
	wire := mailboxID(5) // Receipts, top level

	callGet(t, d.handleMailboxSet, setArgs(fmt.Sprintf(
		`"update":{%q:{"parentId":%q}}`, wire, mailboxID(3))))

	if len(f.renameMailboxCalls) != 1 {
		t.Fatalf("writer received %d rename calls, want 1", len(f.renameMailboxCalls))
	}
	if got := f.renameMailboxCalls[0].newName; got != "Work/Receipts" {
		t.Errorf("re-parent composed %q, want %q", got, "Work/Receipts")
	}
}

func TestMailboxSetUpdateRenameAndReParentTogether(t *testing.T) {
	f, d := mailboxSetFixture()

	callGet(t, d.handleMailboxSet, setArgs(fmt.Sprintf(
		`"update":{%q:{"name":"Bills","parentId":%q}}`, mailboxID(5), mailboxID(3))))

	if len(f.renameMailboxCalls) != 1 {
		t.Fatalf("writer received %d rename calls, want 1", len(f.renameMailboxCalls))
	}
	if got := f.renameMailboxCalls[0].newName; got != "Work/Bills" {
		t.Errorf("composed %q, want %q", got, "Work/Bills")
	}
}

func TestMailboxSetUpdateMoveToTopLevelWithNullParent(t *testing.T) {
	f, d := mailboxSetFixture()

	callGet(t, d.handleMailboxSet, setArgs(fmt.Sprintf(
		`"update":{%q:{"parentId":null}}`, mailboxID(4)))) // Work/2026 -> 2026

	if len(f.renameMailboxCalls) != 1 {
		t.Fatalf("writer received %d rename calls, want 1", len(f.renameMailboxCalls))
	}
	if got := f.renameMailboxCalls[0].newName; got != "2026" {
		t.Errorf("composed %q, want %q", got, "2026")
	}
}

func TestMailboxSetUpdateRefusesACycle(t *testing.T) {
	// RFC 8621 §2.5 invalidProperties: "The parentId is a descendant of this
	// Mailbox". Work under Work/2026 would detach the whole subtree.
	f, d := mailboxSetFixture()

	resp := callGet(t, d.handleMailboxSet, setArgs(fmt.Sprintf(
		`"update":{%q:{"parentId":%q}}`, mailboxID(3), mailboxID(4))))

	serr := setErrorOf(t, resp, "notUpdated", mailboxID(3))
	if serr["type"] != setErrInvalidProperties {
		t.Fatalf("type = %v, want invalidProperties", serr["type"])
	}
	if !strings.Contains(fmt.Sprint(serr["description"]), "descendant") {
		t.Errorf("the error does not explain the cycle: %v", serr)
	}
	if len(f.renameMailboxCalls) != 0 {
		t.Error("a cycle reached the executor")
	}
}

func TestMailboxSetUpdateRefusesBeingItsOwnParent(t *testing.T) {
	f, d := mailboxSetFixture()

	resp := callGet(t, d.handleMailboxSet, setArgs(fmt.Sprintf(
		`"update":{%q:{"parentId":%q}}`, mailboxID(3), mailboxID(3))))

	serr := setErrorOf(t, resp, "notUpdated", mailboxID(3))
	if serr["type"] != setErrInvalidProperties {
		t.Fatalf("type = %v, want invalidProperties", serr["type"])
	}
	if len(f.renameMailboxCalls) != 0 {
		t.Error("a self-parent reached the executor")
	}
}

func TestMailboxSetUpdateRefusesRenamingAProtectedRole(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   int64
	}{{"inbox", 1}, {"trash", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			f, d := mailboxSetFixture()
			wire := mailboxID(tc.id)

			resp := callGet(t, d.handleMailboxSet, setArgs(
				`"update":{"`+wire+`":{"name":"Renamed"}}`))

			serr := setErrorOf(t, resp, "notUpdated", wire)
			if serr["type"] != setErrForbidden {
				t.Fatalf("type = %v, want forbidden", serr["type"])
			}
			if len(f.renameMailboxCalls) != 0 {
				t.Error("a protected-role rename reached the executor")
			}

			// The refusal must be TRUTHFUL in myRights, or a client offers an
			// action it will be denied (RFC 8621 §2, the rights-drive-the-UI
			// contract).
			got := callGet(t, d.handleMailboxGet, setArgs(`"ids":["`+wire+`"]`))
			rights := object(t, firstObject(t, got, 0), "myRights")
			if rights["mayRename"] != false {
				t.Errorf("myRights.mayRename = %v for %s, want false", rights["mayRename"], tc.name)
			}
			if rights["mayDelete"] != false {
				t.Errorf("myRights.mayDelete = %v for %s, want false", rights["mayDelete"], tc.name)
			}
		})
	}
}

func TestMailboxSetUpdateRefusesACollidingName(t *testing.T) {
	f, d := mailboxSetFixture()

	resp := callGet(t, d.handleMailboxSet, setArgs(fmt.Sprintf(
		`"update":{%q:{"name":"Work"}}`, mailboxID(5))))

	serr := setErrorOf(t, resp, "notUpdated", mailboxID(5))
	if serr["type"] != setErrInvalidProperties {
		t.Fatalf("type = %v, want invalidProperties", serr["type"])
	}
	if len(f.renameMailboxCalls) != 0 {
		t.Error("a colliding rename reached the executor")
	}
}

func TestMailboxSetUpdateRefusesRole(t *testing.T) {
	_, d := mailboxSetFixture()

	resp := callGet(t, d.handleMailboxSet, setArgs(fmt.Sprintf(
		`"update":{%q:{"role":"archive"}}`, mailboxID(3))))

	serr := setErrorOf(t, resp, "notUpdated", mailboxID(3))
	if serr["type"] != setErrInvalidProperties {
		t.Fatalf("type = %v, want invalidProperties", serr["type"])
	}
}

func TestMailboxSetUpdateRefusesADeepPointer(t *testing.T) {
	// No Mailbox property has nested structure, so a sub-key pointer can never
	// be valid: §5.3 invalidPatch, not invalidProperties.
	_, d := mailboxSetFixture()

	resp := callGet(t, d.handleMailboxSet, setArgs(fmt.Sprintf(
		`"update":{%q:{"myRights/mayDelete":true}}`, mailboxID(3))))

	serr := setErrorOf(t, resp, "notUpdated", mailboxID(3))
	if serr["type"] != setErrInvalidPatch {
		t.Fatalf("type = %v, want invalidPatch", serr["type"])
	}
}

func TestMailboxSetUpdateOfAnUnknownIdIsNotFound(t *testing.T) {
	_, d := mailboxSetFixture()

	for _, wire := range []string{mailboxID(9999), mailboxID(90), "not-an-id"} {
		resp := callGet(t, d.handleMailboxSet, setArgs(
			`"update":{"`+wire+`":{"name":"X"}}`))
		serr := setErrorOf(t, resp, "notUpdated", wire)
		if serr["type"] != setErrNotFound {
			t.Errorf("%s: type = %v, want notFound", wire, serr["type"])
		}
	}
}

func TestMailboxSetUpdateToTheSameNameIsANoop(t *testing.T) {
	f, d := mailboxSetFixture()

	resp := callGet(t, d.handleMailboxSet, setArgs(fmt.Sprintf(
		`"update":{%q:{"name":"Work"}}`, mailboxID(3))))

	if _, ok := object(t, resp, "updated")[mailboxID(3)]; !ok {
		t.Fatalf("a no-op update was not reported as updated: %v", resp)
	}
	if len(f.renameMailboxCalls) != 0 {
		t.Error("a no-op rename reached the executor")
	}
}

// ---------------------------------------------------------------------------
// destroy
// ---------------------------------------------------------------------------

func TestMailboxSetDestroyEmptyMailboxSucceeds(t *testing.T) {
	f, d := mailboxSetFixture()
	wire := mailboxID(4) // Work/2026, empty, no children

	resp := callGet(t, d.handleMailboxSet, setArgs(`"destroy":["`+wire+`"]`))

	destroyed := array(t, resp, "destroyed")
	if len(destroyed) != 1 || destroyed[0] != wire {
		t.Fatalf("destroyed = %v, want [%s]", destroyed, wire)
	}
	if len(f.destroyMailboxCalls) != 1 || f.destroyMailboxCalls[0] != 4 {
		t.Errorf("destroy calls = %v, want [4]", f.destroyMailboxCalls)
	}
}

func TestMailboxSetDestroyRefusesANonEmptyMailboxByDefault(t *testing.T) {
	// RFC 8621 §2.5: "If false, any attempt to destroy a Mailbox that still
	// has Emails in it will be rejected with a mailboxHasEmail SetError."
	f, d := mailboxSetFixture()
	wire := mailboxID(5) // Receipts, 3 messages

	resp := callGet(t, d.handleMailboxSet, setArgs(`"destroy":["`+wire+`"]`))

	serr := setErrorOf(t, resp, "notDestroyed", wire)
	if serr["type"] != setErrMailboxHasEmail {
		t.Fatalf("type = %v, want mailboxHasEmail", serr["type"])
	}
	if len(f.destroyMailboxCalls) != 0 {
		t.Error("a non-empty mailbox was destroyed without onDestroyRemoveEmails")
	}
	if len(f.destroyCalls) != 0 {
		t.Error("messages were moved despite onDestroyRemoveEmails being false")
	}
}

func TestMailboxSetDestroyWithOnDestroyRemoveEmailsMovesToTrashFirst(t *testing.T) {
	// THE documented deviation from §2.5: the true branch moves the messages
	// to Trash (reusing Email/set's W-A2 destroy) instead of destroying them.
	f, d := mailboxSetFixture()
	f.emails[testAccountID] = []EmailRow{sampleEmail(20, "receipt-a"), sampleEmail(21, "receipt-b")}
	f.hits = []searchHit{{id: 20}, {id: 21}}
	wire := mailboxID(5)

	resp := callGet(t, d.handleMailboxSet, setArgs(
		`"destroy":["`+wire+`"],"onDestroyRemoveEmails":true`))

	destroyed := array(t, resp, "destroyed")
	if len(destroyed) != 1 {
		t.Fatalf("destroyed = %v (notDestroyed=%v)", destroyed, resp["notDestroyed"])
	}
	// The messages went through Email/set's destroy — the SAME path W-A2
	// governs — before the folder was removed.
	if len(f.destroyCalls) != 2 {
		t.Errorf("messages destroyed = %v, want both 20 and 21 moved to Trash", f.destroyCalls)
	}
	if len(f.destroyMailboxCalls) != 1 {
		t.Errorf("the mailbox was not destroyed after being emptied: %v", f.destroyMailboxCalls)
	}
}

func TestMailboxSetDestroyRefusesAMailboxWithChildren(t *testing.T) {
	// RFC 8621 §2.5 mailboxHasChild: "The client MUST remove these before it
	// can delete the parent Mailbox."
	f, d := mailboxSetFixture()
	wire := mailboxID(3) // Work has child Work/2026

	resp := callGet(t, d.handleMailboxSet, setArgs(`"destroy":["`+wire+`"]`))

	serr := setErrorOf(t, resp, "notDestroyed", wire)
	if serr["type"] != setErrMailboxHasChild {
		t.Fatalf("type = %v, want mailboxHasChild", serr["type"])
	}
	if len(f.destroyMailboxCalls) != 0 {
		t.Error("a mailbox with children was destroyed")
	}
}

func TestMailboxSetDestroyRefusesProtectedRoles(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   int64
	}{{"inbox", 1}, {"trash", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			f, d := mailboxSetFixture()
			wire := mailboxID(tc.id)

			resp := callGet(t, d.handleMailboxSet, setArgs(`"destroy":["`+wire+`"]`))

			serr := setErrorOf(t, resp, "notDestroyed", wire)
			if serr["type"] != setErrForbidden {
				t.Fatalf("type = %v, want forbidden", serr["type"])
			}
			if len(f.destroyMailboxCalls) != 0 {
				t.Errorf("the %s mailbox was destroyed", tc.name)
			}
		})
	}
}

func TestMailboxSetDestroyChecksChildrenBeforeMovingAnything(t *testing.T) {
	// A parent with both children and messages must refuse on the CHILD
	// condition without having moved a single message: an irreversible-feeling
	// action must not half-happen before it is refused.
	f, d := mailboxSetFixture()
	f.mailboxes[testAccountID][2].TotalEmails = 4 // Work now holds messages too
	f.hits = []searchHit{{id: 10}}

	resp := callGet(t, d.handleMailboxSet, setArgs(
		`"destroy":["`+mailboxID(3)+`"],"onDestroyRemoveEmails":true`))

	serr := setErrorOf(t, resp, "notDestroyed", mailboxID(3))
	if serr["type"] != setErrMailboxHasChild {
		t.Fatalf("type = %v, want mailboxHasChild", serr["type"])
	}
	if len(f.destroyCalls) != 0 {
		t.Errorf("messages were moved before the child check refused: %v", f.destroyCalls)
	}
}

func TestMailboxSetDestroyOfAnUnknownIdIsNotFound(t *testing.T) {
	_, d := mailboxSetFixture()

	for _, wire := range []string{mailboxID(9999), mailboxID(90), "garbage"} {
		resp := callGet(t, d.handleMailboxSet, setArgs(`"destroy":["`+wire+`"]`))
		serr := setErrorOf(t, resp, "notDestroyed", wire)
		if serr["type"] != setErrNotFound {
			t.Errorf("%s: type = %v, want notFound", wire, serr["type"])
		}
	}
}

// ---------------------------------------------------------------------------
// /set mechanics shared with Email/set
// ---------------------------------------------------------------------------

func TestMailboxSetIfInStateMismatchAbortsTheWholeCall(t *testing.T) {
	f, d := mailboxSetFixture()

	merr := callGetErr(t, d.handleMailboxSet, setArgs(
		`"ifInState":"stale","destroy":["`+mailboxID(4)+`"]`))

	if merr.Code != jmap.CodeStateMismatch {
		t.Fatalf("code = %v, want stateMismatch", merr.Code)
	}
	if len(f.destroyMailboxCalls) != 0 {
		t.Error("a stale ifInState still wrote")
	}
}

func TestMailboxSetStatesBracketTheCall(t *testing.T) {
	f, d := mailboxSetFixture()
	before := f.state

	resp := callGet(t, d.handleMailboxSet, setArgs(`"destroy":["`+mailboxID(4)+`"]`))

	if resp["oldState"] != before {
		t.Errorf("oldState = %v, want %q", resp["oldState"], before)
	}
	if resp["newState"] == before {
		t.Error("newState did not advance across a successful destroy")
	}
}

func TestMailboxSetUpdateAndDestroyOfTheSameIdIsWillDestroy(t *testing.T) {
	// §5.3 willDestroy: "The client requested that an object be both updated
	// and destroyed in the same /set request".
	f, d := mailboxSetFixture()
	wire := mailboxID(4)

	resp := callGet(t, d.handleMailboxSet, setArgs(
		`"update":{"`+wire+`":{"name":"X"}},"destroy":["`+wire+`"]`))

	serr := setErrorOf(t, resp, "notUpdated", wire)
	if serr["type"] != setErrWillDestroy {
		t.Fatalf("type = %v, want willDestroy", serr["type"])
	}
	if len(f.renameMailboxCalls) != 0 {
		t.Error("the ignored update still reached the executor")
	}
}

func TestMailboxSetIsolatesErrorsPerId(t *testing.T) {
	// One bad id must never fail the batch.
	f, d := mailboxSetFixture()

	resp := callGet(t, d.handleMailboxSet, setArgs(fmt.Sprintf(
		`"create":{"good":{"name":"Fine"},"bad":{"name":"Work"}},`+
			`"destroy":[%q,%q]`, mailboxID(4), mailboxID(1))))

	if _, ok := object(t, resp, "created")["good"]; !ok {
		t.Error("the good create was lost")
	}
	if _, ok := object(t, resp, "notCreated")["bad"]; !ok {
		t.Error("the bad create was not reported")
	}
	if got := array(t, resp, "destroyed"); len(got) != 1 || got[0] != mailboxID(4) {
		t.Errorf("destroyed = %v, want the empty folder only", got)
	}
	if _, ok := object(t, resp, "notDestroyed")[mailboxID(1)]; !ok {
		t.Error("the refused destroy was not reported")
	}
	if len(f.createMailboxCalls) != 1 {
		t.Errorf("create calls = %d, want 1", len(f.createMailboxCalls))
	}
}

func TestMailboxSetRefusesAnotherAccountsAccountId(t *testing.T) {
	_, d := mailboxSetFixture()

	merr := callGetErr(t, d.handleMailboxSet,
		`{"accountId":"`+EncodeMailboxID(otherAccountID)+`","create":{}}`)
	if merr.Code == "" {
		t.Fatal("a foreign accountId was accepted")
	}
}

func TestMailboxSetRefusesWithoutAnAuthenticatedCaller(t *testing.T) {
	_, d := mailboxSetFixture()

	_, merr := d.handleMailboxSet(contextNoCaller{}, []byte(setArgs(`"create":{}`)))
	if merr == nil {
		t.Fatal("an unauthenticated caller was accepted")
	}
}

// ---------------------------------------------------------------------------
// writer-error mapping
// ---------------------------------------------------------------------------

func TestMailboxSetMapsWriterErrorsOntoSetErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"already exists", ErrMailboxExists, setErrInvalidProperties},
		{"invalid name", ErrInvalidName, setErrInvalidProperties},
		{"protected", ErrMailboxProtected, setErrForbidden},
		{"has child", ErrMailboxHasChild, setErrMailboxHasChild},
		{"not found", ErrNotFound, setErrNotFound},
		{"anything else", errSecret, setErrServerFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, d := mailboxSetFixture()
			f.mailboxWriteErr = tc.err

			resp := callGet(t, d.handleMailboxSet, setArgs(`"create":{"c1":{"name":"New"}}`))
			serr := setErrorOf(t, resp, "notCreated", "c1")
			if serr["type"] != tc.want {
				t.Fatalf("type = %v, want %v", serr["type"], tc.want)
			}
			// Internal error text must never reach the wire.
			if strings.Contains(fmt.Sprint(serr["description"]), "messages_pkey") {
				t.Errorf("the internal error leaked: %v", serr["description"])
			}
		})
	}
}

func TestMailboxSetEmptyingRefusalDoesNotDestroyTheMailbox(t *testing.T) {
	// If the messages cannot be moved, the folder must survive: a partial
	// destroy that removed the folder but not the mail would be data loss.
	f, d := mailboxSetFixture()
	f.emails[testAccountID] = []EmailRow{sampleEmail(20, "receipt")}
	f.hits = []searchHit{{id: 20}}
	f.writeErr = ErrNoTrash

	resp := callGet(t, d.handleMailboxSet, setArgs(
		`"destroy":["`+mailboxID(5)+`"],"onDestroyRemoveEmails":true`))

	if _, ok := object(t, resp, "notDestroyed")[mailboxID(5)]; !ok {
		t.Fatalf("the destroy was not refused: %v", resp)
	}
	if len(f.destroyMailboxCalls) != 0 {
		t.Error("the mailbox was destroyed even though it could not be emptied")
	}
}

// ---------------------------------------------------------------------------
// myRights and sortOrder
// ---------------------------------------------------------------------------

func TestRightsAreTruthfulPerMailbox(t *testing.T) {
	ordinary := rightsFor("")
	if !ordinary.MayRename || !ordinary.MayDelete || !ordinary.MayCreateChild {
		t.Errorf("an ordinary folder must be renameable, deletable and a valid parent: %+v", ordinary)
	}
	for role := range protectedRoles {
		r := rightsFor(role)
		if r.MayRename || r.MayDelete {
			t.Errorf("%s reports mayRename=%v mayDelete=%v, but Mailbox/set refuses both",
				role, r.MayRename, r.MayDelete)
		}
		if !r.MayCreateChild {
			t.Errorf("%s must still accept children: creating INBOX/Work is legal", role)
		}
		// maySubmit is true since W3: EmailSubmission really sends.
		if !r.MaySubmit {
			t.Errorf("%s reports maySubmit=false, but EmailSubmission/set is real since W3", role)
		}
	}
}

func TestSortOrderForRoleMatchesTheAdapter(t *testing.T) {
	// sortOrderForRole restates adapter.go's sortOrderFor by value because
	// this file may not import the store. The two must not drift.
	for role, want := range map[string]uint64{
		"inbox": 10, "drafts": 20, "sent": 30, "archive": 40,
		"flagged": 50, "all": 60, "junk": 70, "trash": 80, "": 100,
	} {
		if got := sortOrderForRole(role); got != want {
			t.Errorf("sortOrderForRole(%q) = %d, want %d", role, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// tree helpers
// ---------------------------------------------------------------------------

func TestMailboxTreePathsAndDescendants(t *testing.T) {
	f, d := mailboxSetFixture()
	_ = f

	tree, merr := d.readTree(callerCtx(), testAccountID)
	if merr != nil {
		t.Fatalf("readTree: %v", merr)
	}

	if got := tree.paths[4]; got != "Work/2026" {
		t.Errorf("path of the nested mailbox = %q, want %q", got, "Work/2026")
	}
	if got := tree.paths[3]; got != "Work" {
		t.Errorf("path of a top-level mailbox = %q, want %q", got, "Work")
	}
	if kids := tree.descendants(3); len(kids) != 1 || kids[0].ID != 4 {
		t.Errorf("descendants(Work) = %+v, want [2026]", kids)
	}
	if kids := tree.descendants(4); len(kids) != 0 {
		t.Errorf("a leaf reported descendants: %+v", kids)
	}
	if !tree.isDescendantOf(4, 3) {
		t.Error("2026 was not recognized as a descendant of Work")
	}
	if tree.isDescendantOf(3, 4) {
		t.Error("Work was wrongly reported as a descendant of its own child")
	}
	if _, ok := tree.findByPath("Work/2026"); !ok {
		t.Error("findByPath missed an existing path")
	}
	if _, ok := tree.findByPath("Work/Nope"); ok {
		t.Error("findByPath invented a mailbox")
	}
}

func TestValidateLeafNameCitesTheRFCForEachRefusal(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"whitespace", "  "},
		{"delimiter", "a/b"},
		{"control", "a\nb"},
		{"too long", strings.Repeat("x", maxMailboxNameBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serr := validateLeafName(tc.input, "/")
			if serr == nil {
				t.Fatalf("accepted %q", tc.input)
			}
			if serr.Type != setErrInvalidProperties {
				t.Errorf("type = %s, want invalidProperties", serr.Type)
			}
			if len(serr.Properties) != 1 || serr.Properties[0] != "name" {
				t.Errorf("properties = %v, want [name]", serr.Properties)
			}
		})
	}
	if serr := validateLeafName("Facturación", "/"); serr != nil {
		t.Errorf("a legitimate non-ASCII name was refused: %v", serr)
	}
}

func TestMailboxSetErrorNeverLeaksInternalText(t *testing.T) {
	serr := mailboxSetError(errors.New("pq: relation \"mailboxes\" does not exist"), "creating the mailbox")
	if serr.Type != setErrServerFail {
		t.Fatalf("type = %s, want serverFail", serr.Type)
	}
	if strings.Contains(serr.Description, "mailboxes") || strings.Contains(serr.Description, "pq:") {
		t.Errorf("the internal error leaked: %q", serr.Description)
	}
}
