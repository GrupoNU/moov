package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/parser"
)

// Email/set create (W3): the handler over a fake creator, proving the RFC
// 8621 §4.6 rules, the echo-safety of the created id, and that what reaches
// the creator is real, parseable RFC 5322.

// fakeCreateCall is one recorded CreateMessage invocation.
type fakeCreateCall struct {
	accountID int64
	mailboxID int64
	raw       []byte
	flags     []string
}

// The creator half of the fakes, declared here beside its tests (same
// package as fakes_test.go; Go merges them).
func (f *fakeReaders) CreateMessage(_ context.Context, accountID, mailboxID int64, raw []byte, flags []string) (CreatedEmail, error) {
	if f.writeErr != nil {
		return CreatedEmail{}, f.writeErr
	}
	f.createCalls = append(f.createCalls, fakeCreateCall{
		accountID: accountID, mailboxID: mailboxID,
		raw: append([]byte(nil), raw...), flags: append([]string(nil), flags...),
	})
	f.advanceState()
	id := int64(555 + len(f.createCalls))
	return CreatedEmail{ID: id, ThreadID: id, BlobID: strings.Repeat("c", 64), Size: uint64(len(raw))}, nil
}

func jsonArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// draftCreateBody is the canonical composing shape Bulwark sends.
func draftCreateBody(mailboxWire string) map[string]any {
	return map[string]any{
		"mailboxIds": map[string]bool{mailboxWire: true},
		"keywords":   map[string]bool{"$draft": true, "$seen": true},
		"from":       []map[string]string{{"name": "User", "email": "user@example.com"}},
		"to":         []map[string]string{{"name": "Destino", "email": "dest@example.test"}},
		"subject":    "borrador de prueba",
		"bodyValues": map[string]any{
			"t": map[string]any{"value": "hola en texto\n"},
			"h": map[string]any{"value": "<p>hola en <b>html</b></p>\n"},
		},
		"textBody": []map[string]any{{"partId": "t", "type": "text/plain"}},
		"htmlBody": []map[string]any{{"partId": "h", "type": "text/html"}},
	}
}

func TestEmailCreateDraftHappyPath(t *testing.T) {
	f := newFakeReaders()
	drafts := sampleMailbox(31, "Drafts", "drafts", 0, 0)
	f.mailboxes[testAccountID] = []MailboxRow{drafts}
	deps := f.deps()

	created := jmap.NewCreationIDs(nil)
	ctx := jmap.WithCreationIDs(callerCtx(), created)

	res, merr := deps.handleEmailSet(ctx, jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
		"create":    map[string]any{"d1": draftCreateBody(EncodeMailboxID(31))},
	}))
	if merr != nil {
		t.Fatalf("Email/set: %v", merr)
	}
	resp, ok := res.(*setResponse)
	if !ok {
		t.Fatalf("result type = %T", res)
	}
	obj, ok := resp.Created["d1"].(map[string]any)
	if !ok {
		t.Fatalf("created lacks d1: %+v (notCreated: %+v)", resp.Created, resp.NotCreated)
	}

	// §5.3: the server-set properties come back; the wire id is echo-safe
	// (decoding it names the created row).
	wire, ok := obj["id"].(string)
	if !ok {
		t.Fatalf("created id is %T, want a string", obj["id"])
	}
	if id, err := DecodeEmailID(wire); err != nil || id != 556 {
		t.Errorf("created id %q decodes to (%d, %v), want 556", wire, id, err)
	}
	if obj["blobId"] == "" || obj["threadId"] == "" || obj["size"] == uint64(0) {
		t.Errorf("server-set properties missing: %+v", obj)
	}
	// The messageId the server minted is reported (§5.3: properties not sent
	// by the client).
	if _, ok := obj["messageId"]; !ok {
		t.Errorf("generated messageId not reported: %+v", obj)
	}

	// §3.3: the creation id is resolvable for the rest of the request — the
	// §7.5 flow depends on it.
	if got, ok := created.Resolve("#d1"); !ok || got != wire {
		t.Errorf("Resolve(#d1) = (%q, %v), want (%q, true)", got, ok, wire)
	}

	// The write reached the creator with the right scope and the DRAFT flags
	// in the writer vocabulary.
	if len(f.createCalls) != 1 {
		t.Fatalf("creator called %d times", len(f.createCalls))
	}
	call := f.createCalls[0]
	if call.accountID != testAccountID || call.mailboxID != 31 {
		t.Errorf("create scoped to (%d, %d)", call.accountID, call.mailboxID)
	}
	if got := strings.Join(call.flags, ","); got != "draft,seen" {
		t.Errorf("flags = %q, want draft,seen", got)
	}

	// The appended bytes are real mail: the production parser reads back what
	// the client meant.
	p := parser.Parse(strings.NewReader(string(call.raw)), parser.Limits{})
	if p.Status == parser.StatusFailed {
		t.Fatalf("the assembled draft does not parse:\n%s", call.raw)
	}
	if p.Headers.Subject != "borrador de prueba" {
		t.Errorf("subject = %q", p.Headers.Subject)
	}
	if !strings.Contains(p.BodyText, "hola en texto") {
		t.Errorf("text body lost: %q", p.BodyText)
	}
	if len(p.Headers.From) != 1 || p.Headers.From[0].Address != "user@example.com" {
		t.Errorf("from = %+v", p.Headers.From)
	}

	// The state strings bracket the write.
	if resp.OldState == resp.NewState {
		t.Error("newState did not move past oldState across a create")
	}
}

func TestEmailCreateRefusals(t *testing.T) {
	base := func() map[string]any { return draftCreateBody(EncodeMailboxID(31)) }
	for name, tc := range map[string]struct {
		mutate   func(m map[string]any)
		wantType string
		wantProp string
	}{
		"server-set receivedAt": {
			mutate:   func(m map[string]any) { m["receivedAt"] = "2026-08-15T10:00:00Z" },
			wantType: setErrInvalidProperties, wantProp: "receivedAt",
		},
		"two mailboxes": {
			mutate: func(m map[string]any) {
				m["mailboxIds"] = map[string]bool{EncodeMailboxID(31): true, EncodeMailboxID(32): true}
			},
			wantType: setErrInvalidProperties, wantProp: "mailboxIds",
		},
		"no mailboxIds": {
			mutate:   func(m map[string]any) { delete(m, "mailboxIds") },
			wantType: setErrInvalidProperties, wantProp: "mailboxIds",
		},
		"bodyStructure excludes textBody": {
			mutate: func(m map[string]any) {
				m["bodyStructure"] = map[string]any{"partId": "t", "type": "text/plain"}
			},
			wantType: setErrInvalidProperties, wantProp: "bodyStructure",
		},
		"unknown partId": {
			mutate: func(m map[string]any) {
				m["textBody"] = []map[string]any{{"partId": "missing", "type": "text/plain"}}
			},
			wantType: setErrInvalidProperties,
		},
		"modeled header spelled as header:*": {
			mutate:   func(m map[string]any) { m["header:Subject"] = "sneaky" },
			wantType: setErrInvalidProperties, wantProp: "header:Subject",
		},
		"foreign attachment blob": {
			mutate: func(m map[string]any) {
				m["attachments"] = []map[string]any{{"blobId": strings.Repeat("d", 64), "type": "application/pdf"}}
			},
			wantType: setErrBlobNotFound,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeReaders()
			f.mailboxes[testAccountID] = []MailboxRow{
				sampleMailbox(31, "Drafts", "drafts", 0, 0),
				sampleMailbox(32, "Otro", "", 0, 0),
			}
			deps := f.deps()
			body := base()
			tc.mutate(body)

			res, merr := deps.handleEmailSet(callerCtx(), jsonArgs(t, map[string]any{
				"accountId": testAccountJMAPID(),
				"create":    map[string]any{"d1": body},
			}))
			if merr != nil {
				t.Fatalf("method error: %v", merr)
			}
			resp, ok := res.(*setResponse)
			if !ok {
				t.Fatalf("result type = %T", res)
			}
			serr, refused := resp.NotCreated["d1"]
			if !refused {
				t.Fatalf("create succeeded: %+v", resp.Created)
			}
			if serr.Type != tc.wantType {
				t.Errorf("SetError type = %q (%s), want %q", serr.Type, serr.Description, tc.wantType)
			}
			if tc.wantProp != "" && !strings.Contains(strings.Join(serr.Properties, ","), tc.wantProp) {
				t.Errorf("properties %v lack %q", serr.Properties, tc.wantProp)
			}
			if len(f.createCalls) != 0 {
				t.Error("a refused create still reached the creator")
			}
		})
	}
}

func TestEmailCreateKeywordCeilingHoldsOnCreate(t *testing.T) {
	// The A6/V1 ceiling applies to creates exactly as to updates: a draft
	// whose keywords would need the folder's 27th slot is refused before any
	// bytes are assembled.
	f := newFakeReaders()
	f.mailboxes[testAccountID] = []MailboxRow{sampleMailbox(31, "Drafts", "drafts", 0, 0)}
	inUse := make([]string, 26)
	for i := range inUse {
		inUse[i] = fmt.Sprintf("label%02d", i)
	}
	f.keywordsInUse = map[int64][]string{31: inUse}
	deps := f.deps()

	body := draftCreateBody(EncodeMailboxID(31))
	body["keywords"] = map[string]bool{"$draft": true, "FreshLabel": true}

	res, merr := deps.handleEmailSet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
		"create":    map[string]any{"d1": body},
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	resp, ok := res.(*setResponse)
	if !ok {
		t.Fatalf("result type = %T", res)
	}
	serr, refused := resp.NotCreated["d1"]
	if !refused || serr.Type != setErrInvalidProperties {
		t.Fatalf("ceiling did not hold on create: %+v / %+v", resp.Created, resp.NotCreated)
	}
	if !strings.Contains(serr.Description, "Maildir") {
		t.Errorf("the ceiling refusal lost its explanation: %q", serr.Description)
	}
}

func TestEmailCreateAttachmentFromOwnBlob(t *testing.T) {
	f := newFakeReaders()
	f.mailboxes[testAccountID] = []MailboxRow{sampleMailbox(31, "Drafts", "drafts", 0, 0)}
	// The account owns a blob (the fake scopes OpenBlob by ownership).
	blobID := "aa" + repeatHex(62)
	f.emails[testAccountID] = []EmailRow{sampleEmail(1, "holder")}
	f.blobs[blobID] = []byte("%PDF-fake-bytes")
	deps := f.deps()

	body := draftCreateBody(EncodeMailboxID(31))
	body["attachments"] = []map[string]any{
		{"blobId": blobID, "type": "application/pdf", "name": "informe.pdf"},
	}

	res, merr := deps.handleEmailSet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
		"create":    map[string]any{"d1": body},
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	resp, ok := res.(*setResponse)
	if !ok {
		t.Fatalf("result type = %T", res)
	}
	if _, ok := resp.Created["d1"]; !ok {
		t.Fatalf("create failed: %+v", resp.NotCreated)
	}

	p := parser.Parse(strings.NewReader(string(f.createCalls[0].raw)), parser.Limits{})
	foundPDF := false
	for _, part := range p.Parts {
		if part.MediaType == "application/pdf" && part.Filename == "informe.pdf" && part.IsAttachment {
			foundPDF = true
		}
	}
	if !foundPDF {
		t.Errorf("the attached blob did not round-trip into the MIME structure: %+v", p.Parts)
	}
}

func TestEmailCreateWithoutCreatorKeepsTheHonestRefusal(t *testing.T) {
	f := newFakeReaders()
	f.mailboxes[testAccountID] = []MailboxRow{sampleMailbox(31, "Drafts", "drafts", 0, 0)}
	deps := f.deps()
	deps.Creator = nil // a deployment without the append path

	res, merr := deps.handleEmailSet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
		"create":    map[string]any{"d1": draftCreateBody(EncodeMailboxID(31))},
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	resp, ok := res.(*setResponse)
	if !ok {
		t.Fatalf("result type = %T", res)
	}
	if serr := resp.NotCreated["d1"]; serr.Type != setErrServerUnavailable {
		t.Errorf("refusal type = %q, want serverUnavailable", serr.Type)
	}
}

func TestEmailCreateResolvesMailboxCreationReference(t *testing.T) {
	// "Create the folder and file the draft into it" in one request: the
	// mailboxIds key is a #creation-id minted by an earlier Mailbox/set.
	f := newFakeReaders()
	f.mailboxes[testAccountID] = []MailboxRow{sampleMailbox(31, "Drafts", "drafts", 0, 0)}
	deps := f.deps()

	created := jmap.NewCreationIDs(nil)
	ctx := jmap.WithCreationIDs(callerCtx(), created)

	// The earlier Mailbox/set create.
	res, merr := deps.handleMailboxSet(ctx, jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
		"create":    map[string]any{"folder": map[string]any{"name": "Proyectos"}},
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	if mresp, ok := res.(*setResponse); !ok || mresp.Created["folder"] == nil {
		t.Fatalf("mailbox create failed: %+v", res)
	}

	body := draftCreateBody("ignored")
	body["mailboxIds"] = map[string]bool{"#folder": true}

	res, merr = deps.handleEmailSet(ctx, jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
		"create":    map[string]any{"d1": body},
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	resp, ok := res.(*setResponse)
	if !ok {
		t.Fatalf("result type = %T", res)
	}
	if _, ok := resp.Created["d1"]; !ok {
		t.Fatalf("create through #folder failed: %+v", resp.NotCreated)
	}
	if got := f.createCalls[0].mailboxID; got != 9001 {
		t.Errorf("draft filed into mailbox %d, want the freshly created 9001", got)
	}
}
