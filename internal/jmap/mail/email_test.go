package mail

import (
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
)

// A small real message, used where a bodyValue must come from an actual parse
// rather than a fixture string.
const plainMessage = "From: Alice <alice@example.com>\r\n" +
	"To: Bob <bob@example.com>\r\n" +
	"Subject: Hello\r\n" +
	"Date: Sat, 01 Aug 2026 09:59:00 +0000\r\n" +
	"Message-ID: <m1@example.com>\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Hello, world.\r\nSecond line.\r\n"

func TestEmailGetDefaultProperties(t *testing.T) {
	f := newFakeReaders()
	f.emails[testAccountID] = []EmailRow{sampleEmail(1, "Hello")}
	d := f.deps()

	got := callGet(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeEmailID(1)+`"]}`)

	list, _ := got["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("list has %d entries", len(list))
	}
	e, _ := list[0].(map[string]any)

	// Every property of the §4.6 default list must be present, and nothing
	// outside it — notably NOT bodyStructure, which the RFC omits from the
	// default set.
	for _, p := range defaultEmailProperties {
		if _, ok := e[p]; !ok {
			t.Errorf("default property %q is missing", p)
		}
	}
	if _, ok := e["bodyStructure"]; ok {
		t.Error("bodyStructure is not in the RFC 8621 §4.6 default property list")
	}

	if e["id"] != EncodeEmailID(1) {
		t.Errorf("id = %v", e["id"])
	}
	if e["threadId"] != EncodeThreadID(1) {
		t.Errorf("threadId = %v", e["threadId"])
	}
	if e["size"] != float64(1024) {
		t.Errorf("size = %v", e["size"])
	}
	if e["subject"] != "Hello" {
		t.Errorf("subject = %v", e["subject"])
	}
	// receivedAt is a UTCDate: RFC 3339 in UTC with a literal Z.
	if e["receivedAt"] != "2026-08-01T10:00:00Z" {
		t.Errorf("receivedAt = %v, want a UTCDate", e["receivedAt"])
	}

	// keywords is a Set: an object with true values (§4.1.1).
	kw, ok := e["keywords"].(map[string]any)
	if !ok {
		t.Fatalf("keywords is %T, want an object", e["keywords"])
	}
	if kw[KeywordSeen] != true {
		t.Errorf("keywords = %#v, want $seen true", kw)
	}

	// mailboxIds is a Set of Mailbox ids.
	mb, ok := e["mailboxIds"].(map[string]any)
	if !ok || mb[EncodeMailboxID(1)] != true {
		t.Errorf("mailboxIds = %#v", e["mailboxIds"])
	}

	// from is an EmailAddress[] (§4.1.2).
	from, ok := e["from"].([]any)
	if !ok || len(from) != 1 {
		t.Fatalf("from = %#v", e["from"])
	}
	addr, _ := from[0].(map[string]any)
	if addr["name"] != "Alice" || addr["email"] != "alice@example.com" {
		t.Errorf("from[0] = %#v", addr)
	}

	// An absent header renders as null, not as an empty array.
	if e["cc"] != nil {
		t.Errorf("cc = %#v, want null for an absent header", e["cc"])
	}

	// With no fetch*BodyValues argument, bodyValues is an empty object.
	bv, ok := e["bodyValues"].(map[string]any)
	if !ok || len(bv) != 0 {
		t.Errorf("bodyValues = %#v, want {} when no fetch argument is given", e["bodyValues"])
	}
}

func TestEmailGetIdsNullIsRefused(t *testing.T) {
	d := newFakeReaders().deps()
	merr := callGetErr(t, d.handleEmailGet, `{"accountId":"`+testAccountJMAPID()+`","ids":null}`)
	if merr.Code != jmap.CodeRequestTooLarge {
		t.Fatalf("code = %s, want requestTooLarge for an unbounded Email/get", merr.Code)
	}
}

func TestEmailGetForeignMessageIsNotFound(t *testing.T) {
	f := newFakeReaders()
	f.emails[testAccountID] = []EmailRow{sampleEmail(1, "Mine")}
	f.emails[otherAccountID] = []EmailRow{sampleEmail(2, "Theirs")}
	d := f.deps()

	got := callGet(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeEmailID(1)+`","`+EncodeEmailID(2)+`"]}`)

	if list, _ := got["list"].([]any); len(list) != 1 {
		t.Fatalf("list = %#v, want only the caller's message", got["list"])
	}
	nf, _ := got["notFound"].([]any)
	if len(nf) != 1 || nf[0] != EncodeEmailID(2) {
		t.Fatalf("notFound = %#v, want the foreign message id", nf)
	}
}

func TestEmailGetBodyValuesFromRealParse(t *testing.T) {
	f := newFakeReaders()
	row := sampleEmail(1, "Hello")
	f.emails[testAccountID] = []EmailRow{row}
	f.raw[1] = []byte(plainMessage)
	d := f.deps()

	got := callGet(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeEmailID(1)+`"],`+
			`"properties":["id","bodyValues","textBody"],"fetchTextBodyValues":true}`)

	e := firstObject(t, got, 0)
	bv := object(t, e, "bodyValues")
	if len(bv) == 0 {
		t.Fatalf("bodyValues is empty; want the text part")
	}
	part := object(t, bv, "0")
	value, _ := part["value"].(string)
	if !strings.Contains(value, "Hello, world.") {
		t.Errorf("value = %q", value)
	}
	// §4.1.4: "with any CRLF replaced with a single LF".
	if strings.Contains(value, "\r\n") {
		t.Errorf("value still contains CRLF: %q", value)
	}
	if !strings.Contains(value, "\n") {
		t.Errorf("the line break was lost entirely: %q", value)
	}
	if part["isTruncated"] != false {
		t.Errorf("isTruncated = %v, want false", part["isTruncated"])
	}
	if part["isEncodingProblem"] != false {
		t.Errorf("isEncodingProblem = %v, want false for a clean utf-8 part", part["isEncodingProblem"])
	}
}

// RFC 8621 §4.1.3 defines `headers` as "a list of all header fields in the
// message, in the same order they appear in the message". It is a mandatory
// Email property, and omitting it from the known-property set made the server
// answer the WHOLE Email/get with invalidArguments — which is how the pilot's
// reading pane stayed empty: Bulwark asks for `headers` on every message open,
// got an error instead of an Email, and rendered nothing.
func TestEmailGetHeadersInMessageOrder(t *testing.T) {
	f := newFakeReaders()
	f.emails[testAccountID] = []EmailRow{sampleEmail(1, "Hello")}
	f.raw[1] = []byte(plainMessage)
	d := f.deps()

	got := callGet(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeEmailID(1)+`"],`+
			`"properties":["id","headers"]}`)

	e := firstObject(t, got, 0)
	raw, ok := e["headers"].([]any)
	if !ok {
		t.Fatalf("headers is %T, want a list", e["headers"])
	}

	// Order is the point: plainMessage is From, To, Subject, Date, Message-ID,
	// Content-Type, and the list must reproduce that sequence.
	wantOrder := []string{"From", "To", "Subject", "Date", "Message-ID", "Content-Type"}
	if len(raw) != len(wantOrder) {
		t.Fatalf("got %d headers, want %d: %+v", len(raw), len(wantOrder), raw)
	}
	for i, want := range wantOrder {
		h, ok := raw[i].(map[string]any)
		if !ok {
			t.Fatalf("headers[%d] is %T, want an object", i, raw[i])
		}
		if h["name"] != want {
			t.Errorf("headers[%d].name = %v, want %q", i, h["name"], want)
		}
		if _, ok := h["value"].(string); !ok {
			t.Errorf("headers[%d].value = %T, want a string", i, h["value"])
		}
	}

	// §4.1.3 keeps the capitalization the message used, so a client that
	// re-renders the header block gets "Message-ID", not "Message-Id".
	first, _ := raw[0].(map[string]any)
	if v, _ := first["value"].(string); !strings.Contains(v, "alice@example.com") {
		t.Errorf("From value = %q, want the raw address", v)
	}
}

// The exact Email/get the pilot's Bulwark sends when a message is clicked,
// property list verbatim from the captured request. It must return an Email,
// never an error — this is the regression that closes the empty reading pane.
func TestEmailGetBulwarkMessageOpenRequest(t *testing.T) {
	f := newFakeReaders()
	f.emails[testAccountID] = []EmailRow{sampleEmail(1, "Hello")}
	f.raw[1] = []byte(plainMessage)
	d := f.deps()

	got := callGet(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeEmailID(1)+`"],`+
			`"properties":["id","threadId","mailboxIds","keywords","size","receivedAt",`+
			`"sentAt","from","to","cc","bcc","replyTo","subject","preview","textBody",`+
			`"htmlBody","bodyValues","hasAttachment","attachments","messageId","inReplyTo",`+
			`"references","headers","bodyStructure","blobId"],`+
			`"fetchTextBodyValues":true,"fetchHTMLBodyValues":true,"fetchAllBodyValues":true,`+
			`"maxBodyValueBytes":256000}`)

	e := firstObject(t, got, 0)
	if _, ok := e["headers"].([]any); !ok {
		t.Errorf("headers = %T, want a list", e["headers"])
	}
	// The body must arrive in the same response: a pane with headers and no
	// text is still an empty pane to the user.
	bv := object(t, e, "bodyValues")
	if len(bv) == 0 {
		t.Fatal("bodyValues is empty; the reading pane would render nothing")
	}
	if v, _ := object(t, bv, "0")["value"].(string); !strings.Contains(v, "Hello, world.") {
		t.Errorf("body value = %q", v)
	}
}

// A message whose blob is gone still has to answer `headers` with a list:
// §4.1.3 types it EmailHeader[], not nullable, so null would be a type error
// for a client that iterates it.
func TestEmailGetHeadersMissingBlobIsEmptyList(t *testing.T) {
	f := newFakeReaders()
	f.emails[testAccountID] = []EmailRow{sampleEmail(1, "Hello")}
	// No f.raw entry: the blob is absent.
	d := f.deps()

	got := callGet(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeEmailID(1)+`"],`+
			`"properties":["id","subject","headers"]}`)

	e := firstObject(t, got, 0)
	raw, ok := e["headers"].([]any)
	if !ok {
		t.Fatalf("headers = %T, want an empty list rather than null", e["headers"])
	}
	if len(raw) != 0 {
		t.Errorf("headers = %+v, want empty", raw)
	}
	// The metadata the store itself holds is still served.
	if e["subject"] != "Hello" {
		t.Errorf("subject = %v, want the stored value", e["subject"])
	}
}

func TestEmailGetMaxBodyValueBytesTruncates(t *testing.T) {
	f := newFakeReaders()
	f.emails[testAccountID] = []EmailRow{sampleEmail(1, "Hello")}
	f.raw[1] = []byte(plainMessage)
	d := f.deps()

	got := callGet(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeEmailID(1)+`"],`+
			`"properties":["id","bodyValues"],"fetchTextBodyValues":true,"maxBodyValueBytes":5}`)

	part := object(t, object(t, firstObject(t, got, 0), "bodyValues"), "0")
	value, _ := part["value"].(string)
	// The cap is in OCTETS (RFC 8621 §4.2), so the byte length is what must
	// not be exceeded.
	if len(value) > 5 {
		t.Errorf("value is %d octets, want at most 5", len(value))
	}
	if part["isTruncated"] != true {
		t.Error("isTruncated must be true when the value was cut")
	}
}

func TestEmailGetParseFailedServesMinimalEmail(t *testing.T) {
	f := newFakeReaders()
	row := sampleEmail(1, "Broken")
	row.ParseFailed = true
	row.Structure = nil
	f.emails[testAccountID] = []EmailRow{row}
	d := f.deps()

	got := callGet(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeEmailID(1)+`"],`+
			`"properties":["id","blobId","subject","bodyStructure","textBody","attachments","bodyValues"],`+
			`"fetchTextBodyValues":true}`)

	e := firstObject(t, got, 0)
	// The raw blob stays downloadable — that is the whole point of the
	// minimal representation.
	if e["blobId"] == nil || e["blobId"] == "" {
		t.Error("a parse-failed message must still expose its blobId")
	}
	if e["subject"] != "Broken" {
		t.Errorf("metadata must survive a failed parse: subject = %v", e["subject"])
	}
	if len(array(t, e, "textBody")) != 0 {
		t.Error("textBody must be empty for an unparseable message, not invented")
	}
	if len(object(t, e, "bodyValues")) != 0 {
		t.Error("bodyValues must be empty for an unparseable message")
	}
	if e["bodyStructure"] == nil {
		t.Error("bodyStructure must still be a valid EmailBodyPart")
	}
}

func TestEmailGetUnknownPropertiesAreRejected(t *testing.T) {
	d := newFakeReaders().deps()

	merr := callGetErr(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["e1"],"properties":["id","bogus"]}`)
	if merr.Code != jmap.CodeInvalidArguments {
		t.Fatalf("properties: code = %s", merr.Code)
	}

	merr = callGetErr(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["e1"],"bodyProperties":["bogus"]}`)
	if merr.Code != jmap.CodeInvalidArguments {
		t.Fatalf("bodyProperties: code = %s", merr.Code)
	}
}

func TestEmailGetMissingBlobServesMetadataWithoutBodies(t *testing.T) {
	f := newFakeReaders()
	f.emails[testAccountID] = []EmailRow{sampleEmail(1, "Hello")}
	// No raw bytes registered: the blob is gone but the row remains.
	d := f.deps()

	got := callGet(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeEmailID(1)+`"],`+
			`"properties":["id","subject","bodyValues"],"fetchTextBodyValues":true}`)

	e := firstObject(t, got, 0)
	if e["subject"] != "Hello" {
		t.Error("metadata must survive a missing blob")
	}
	if len(object(t, e, "bodyValues")) != 0 {
		t.Error("bodyValues must be empty when the blob is gone")
	}
}

// Multipart/alternative is where textBody and htmlBody legitimately disagree,
// which is the entire reason RFC 8621 §4.1.4 defines them as separate lists.
func TestBodyStructureAlternativePicksDifferentBranches(t *testing.T) {
	parts := []StructurePart{
		{Index: 0, Parent: -1, MediaType: "multipart/alternative", IsMultipart: true},
		{Index: 1, Parent: 0, MediaType: "text/plain", Size: 10},
		{Index: 2, Parent: 0, MediaType: "text/html", Size: 20},
	}
	root := bodyPartTree(parts)
	text, html, attachments := bodyStructureLists(root)

	if len(text) != 1 || text[0].part.Index != 1 {
		t.Errorf("textBody = %v, want the text/plain branch", partIndexes(text))
	}
	if len(html) != 1 || html[0].part.Index != 2 {
		t.Errorf("htmlBody = %v, want the text/html branch", partIndexes(html))
	}
	if len(attachments) != 0 {
		t.Errorf("attachments = %v, want none", partIndexes(attachments))
	}
}

func TestBodyStructureMixedWithAttachment(t *testing.T) {
	parts := []StructurePart{
		{Index: 0, Parent: -1, MediaType: "multipart/mixed", IsMultipart: true},
		{Index: 1, Parent: 0, MediaType: "text/plain", Size: 10},
		{Index: 2, Parent: 0, MediaType: "application/pdf", Size: 999,
			IsAttachment: true, Disposition: "attachment", Filename: "invoice.pdf"},
	}
	root := bodyPartTree(parts)
	text, html, attachments := bodyStructureLists(root)

	if len(text) != 1 || len(html) != 1 {
		t.Errorf("body lists = %v / %v, want the text part in both", partIndexes(text), partIndexes(html))
	}
	if len(attachments) != 1 || attachments[0].part.Index != 2 {
		t.Fatalf("attachments = %v, want the pdf", partIndexes(attachments))
	}
}

// A single-branch alternative must still yield a body in BOTH lists, so a
// client asking for htmlBody on a text-only message is not left with nothing.
func TestBodyStructureSingleBranchAlternative(t *testing.T) {
	parts := []StructurePart{
		{Index: 0, Parent: -1, MediaType: "multipart/alternative", IsMultipart: true},
		{Index: 1, Parent: 0, MediaType: "text/plain", Size: 10},
	}
	root := bodyPartTree(parts)
	text, html, _ := bodyStructureLists(root)
	if len(text) != 1 || len(html) != 1 {
		t.Errorf("text=%v html=%v, want the single branch in both", partIndexes(text), partIndexes(html))
	}
}

// Corrupt structure documents must not hang or panic the handler: mail that
// survives the S4 corpus is exactly the mail that produces strange trees.
func TestBodyPartTreeSurvivesCorruptInput(t *testing.T) {
	cases := map[string][]StructurePart{
		"cycle": {
			{Index: 0, Parent: 1, MediaType: "multipart/mixed", IsMultipart: true},
			{Index: 1, Parent: 0, MediaType: "text/plain"},
		},
		"parent out of range": {
			{Index: 0, Parent: -1, MediaType: "multipart/mixed", IsMultipart: true},
			{Index: 1, Parent: 99, MediaType: "text/plain"},
		},
		"duplicate index": {
			{Index: 0, Parent: -1, MediaType: "multipart/mixed", IsMultipart: true},
			{Index: 0, Parent: -1, MediaType: "text/plain"},
		},
		"self parent": {
			{Index: 0, Parent: 0, MediaType: "text/plain"},
		},
	}
	for name, parts := range cases {
		t.Run(name, func(t *testing.T) {
			root := bodyPartTree(parts)
			if root == nil {
				t.Fatal("tree is nil")
			}
			// Must terminate, and every part must remain reachable somewhere.
			text, html, att := bodyStructureLists(root)
			total := len(text) + len(html) + len(att)
			if total == 0 {
				t.Error("every part became invisible")
			}
		})
	}
}

func partIndexes(nodes []*bodyPartNode) []int {
	out := make([]int, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.part.Index)
	}
	return out
}
