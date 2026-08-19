package mail_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for migrations

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
	"github.com/GrupoNU/moov/internal/parser"
	"github.com/GrupoNU/moov/internal/store"
)

// Integration tests for the J2 readers against a REAL store and a real blob
// tree, seeded through the store's own APIs with messages parsed from the S4
// MIME corpus — so the structure the JMAP layer renders is the structure the
// production parser actually produces, not a fixture someone wrote by hand.
//
//	MOOV_TEST_DATABASE_URL   the PostgreSQL DSN (migrations are applied)
//
// Without it the tests skip, following the convention of internal/store.

const testDBEnv = "MOOV_TEST_DATABASE_URL"

type fixture struct {
	store   *store.Store
	blobs   *blob.Store
	deps    *mail.Deps
	account store.Account
	inbox   store.Mailbox
	ctx     context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	dsn := os.Getenv(testDBEnv)
	if dsn == "" {
		t.Skipf("%s is not set; start a dev database with `make db-up` to run the integration tests", testDBEnv)
	}

	ctx := context.Background()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	st, err := store.Open(ctx, store.Config{DSN: dsn, MaxConns: 8, AnalyticMaxConns: 2})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(st.Close)

	blobs, err := blob.New(blob.Config{Root: t.TempDir(), Pool: st.Pool()})
	if err != nil {
		t.Fatalf("blob.New: %v", err)
	}

	acct, err := st.CreateAccount(ctx, store.Account{
		Email:    fmt.Sprintf("j2-%d@example.test", time.Now().UnixNano()),
		IMAPHost: "dovecot.internal",
		IMAPPort: 143,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	t.Cleanup(func() {
		if err := st.DeleteAccount(context.Background(), acct.ID); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})

	inbox, err := st.UpsertMailbox(ctx, store.Mailbox{
		AccountID: acct.ID, Name: "INBOX", Delimiter: "/",
		Role: store.RoleInbox, Subscribed: true, Selectable: true,
	})
	if err != nil {
		t.Fatalf("UpsertMailbox: %v", err)
	}

	deps, err := mail.NewDeps(st, blobs, jmap.DefaultLimits())
	if err != nil {
		t.Fatalf("NewDeps: %v", err)
	}

	return &fixture{store: st, blobs: blobs, deps: deps, account: acct, inbox: inbox, ctx: ctx}
}

// seedRaw stores a raw message: blob first (so the foreign key holds), then
// the parsed row pair, exactly as the sync engine does.
func (f *fixture) seedRaw(t *testing.T, raw []byte, mailbox store.Mailbox, uid int64, flags store.Flags, keywords []string) int64 {
	t.Helper()

	h, size, err := f.blobs.Put(f.ctx, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("blob.Put: %v", err)
	}

	p := parser.ParseBytes(raw, parser.DefaultLimits())

	msg := store.Message{
		AccountID: f.account.ID,
		RawSHA256: h.Bytes(),
		RawSize:   size,
		MessageID: p.Headers.MessageID,
		Subject:   p.SubjectText,
		FromAddr:  addrText(p.Headers.From),
		ToAddrs:   addrText(p.Headers.To),
		Date:      messageDate(p.Headers.Date),
		BodyText:  p.BodyText,
		Preview:   preview(p.BodyText),

		HasAttachments: len(p.Attachments()) > 0,
		ParseStatus:    storeStatus(p.Status),
		Parser:         string(p.Parser),
		ParserVersion:  1,

		Addresses:     encodeAddresses(p.Headers),
		MIMEStructure: encodeStructure(p.Parts),
	}
	if len(p.Headers.InReplyTo) > 0 {
		msg.InReplyTo = p.Headers.InReplyTo[0]
	}
	msg.ReferencesIDs = p.Headers.References

	ids, err := f.store.InsertMessages(f.ctx, []store.NewMessage{{
		Message: msg,
		State: store.MessageState{
			AccountID: f.account.ID, MailboxID: mailbox.ID,
			UID: uid, UIDValidity: 1, Flags: flags, Keywords: keywords,
		},
	}})
	if err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
	if len(ids) != 1 || ids[0] == 0 {
		t.Fatalf("InsertMessages returned %v", ids)
	}

	// The blob reference must exist for the download ownership check to pass —
	// the same AddRef the sync engine performs in the message's transaction.
	if err := f.blobs.AddRefTx(f.ctx, h, f.account.ID, blob.OwnerMessage, ids[0]); err != nil {
		t.Fatalf("AddRef: %v", err)
	}

	// Threading, which is a SEPARATE step from the insert in the real pipeline
	// too (internal/sync commitBatch): InsertMessages leaves every message as
	// its own thread and AssignThreads groups them. Omitting it here would make
	// the fixture disagree with production — every seeded message would be a
	// singleton thread — so it is performed for the same reason AddRef is.
	refs := msg.ReferencesIDs
	if msg.InReplyTo != "" {
		refs = append(append([]string{}, msg.ReferencesIDs...), msg.InReplyTo)
	}
	if _, err := f.store.AssignThreads(f.ctx, f.account.ID, ids, []store.ThreadCandidate{{
		MessageID:  msg.MessageID,
		References: refs,
		Subject:    msg.Subject,
	}}); err != nil {
		t.Fatalf("AssignThreads: %v", err)
	}
	return ids[0]
}

// corpusMessage loads a .eml from the S4 corpus.
func corpusMessage(t *testing.T, rel string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "testdata", "mime-corpus", rel)
	raw, err := os.ReadFile(path) //nolint:gosec // a test reading the repo's own corpus
	if err != nil {
		t.Skipf("corpus case %s is unavailable: %v", rel, err)
	}
	return raw
}

func (f *fixture) callerCtx() context.Context {
	return jmap.WithCaller(f.ctx, jmap.Caller{AccountID: f.account.ID, Email: f.account.Email})
}

func (f *fixture) accountID() string { return jmap.EncodeAccountID(f.account.ID) }

// call dispatches a method through a registry, exactly as the engine would.
func (f *fixture) call(t *testing.T, method, args string) map[string]any {
	t.Helper()
	registry := jmap.NewRegistry()
	mail.RegisterGetMethods(registry, f.deps)

	engine := jmap.NewEngine(registry, jmap.DefaultLimits(),
		[]string{jmap.CapCore, jmap.CapMail}, nil)

	body := fmt.Sprintf(
		`{"using":["urn:ietf:params:jmap:core","urn:ietf:params:jmap:mail"],`+
			`"methodCalls":[[%q,%s,"c1"]]}`, method, args)

	resp, rerr := engine.Process(f.callerCtx(), []byte(body), "session-1")
	if rerr != nil {
		t.Fatalf("request-level error: %v", rerr)
	}
	if len(resp.MethodResponses) != 1 {
		t.Fatalf("got %d method responses", len(resp.MethodResponses))
	}
	inv := resp.MethodResponses[0]
	if inv.Name == "error" {
		t.Fatalf("method error: %s", inv.Args)
	}
	var out map[string]any
	if err := json.Unmarshal(inv.Args, &out); err != nil {
		t.Fatalf("decoding args: %v", err)
	}
	return out
}

// Response navigation helpers, so a malformed response fails with a message
// naming the property rather than panicking on a type assertion.

func firstObject(t *testing.T, resp map[string]any, n int) map[string]any {
	t.Helper()
	list := array(t, resp, "list")
	if n >= len(list) {
		t.Fatalf("list has %d entries, wanted index %d", len(list), n)
	}
	obj, ok := list[n].(map[string]any)
	if !ok {
		t.Fatalf("list[%d] is %T, want an object", n, list[n])
	}
	return obj
}

func object(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want an object", key, parent[key])
	}
	return v
}

func array(t *testing.T, parent map[string]any, key string) []any {
	t.Helper()
	v, ok := parent[key].([]any)
	if !ok {
		t.Fatalf("%s is %T, want an array", key, parent[key])
	}
	return v
}

// ---------------------------------------------------------------------------

func TestIntegrationMailboxGetOverRealStore(t *testing.T) {
	f := newFixture(t)

	sent, err := f.store.UpsertMailbox(f.ctx, store.Mailbox{
		AccountID: f.account.ID, Name: "Sent", Delimiter: "/",
		Role: store.RoleSent, Subscribed: true, Selectable: true,
	})
	if err != nil {
		t.Fatalf("UpsertMailbox: %v", err)
	}
	// A nested folder, to prove parentId resolution against a real hierarchy.
	if _, err := f.store.UpsertMailbox(f.ctx, store.Mailbox{
		AccountID: f.account.ID, Name: "INBOX/Work", Delimiter: "/",
		Subscribed: true, Selectable: true,
	}); err != nil {
		t.Fatalf("UpsertMailbox: %v", err)
	}

	raw := corpusMessage(t, "09-real-world/007-format-flowed-edges.eml")
	f.seedRaw(t, raw, f.inbox, 1, 0, nil)              // unread
	f.seedRaw(t, raw, f.inbox, 2, store.FlagSeen, nil) // read
	f.seedRaw(t, raw, sent, 3, store.FlagSeen, nil)

	got := f.call(t, "Mailbox/get", `{"accountId":"`+f.accountID()+`","ids":null}`)
	list := array(t, got, "list")
	if len(list) != 3 {
		t.Fatalf("got %d mailboxes, want 3", len(list))
	}

	byName := map[string]map[string]any{}
	for _, item := range list {
		m, _ := item.(map[string]any)
		name, _ := m["name"].(string)
		byName[name] = m
	}

	inbox := byName["INBOX"]
	if inbox == nil {
		t.Fatal("INBOX is missing")
	}
	if inbox["role"] != "inbox" {
		t.Errorf("INBOX role = %v", inbox["role"])
	}
	if inbox["totalEmails"] != float64(2) {
		t.Errorf("INBOX totalEmails = %v, want 2", inbox["totalEmails"])
	}
	if inbox["unreadEmails"] != float64(1) {
		t.Errorf("INBOX unreadEmails = %v, want 1", inbox["unreadEmails"])
	}
	if inbox["parentId"] != nil {
		t.Errorf("INBOX parentId = %v, want null", inbox["parentId"])
	}

	if byName["Sent"]["role"] != "sent" {
		t.Errorf("Sent role = %v", byName["Sent"]["role"])
	}

	// The nested folder reports its LEAF name and its real parent.
	work := byName["Work"]
	if work == nil {
		t.Fatal(`the nested mailbox is not reported under its leaf name "Work"`)
	}
	if work["parentId"] != inbox["id"] {
		t.Errorf("Work parentId = %v, want INBOX's id %v", work["parentId"], inbox["id"])
	}
	if work["role"] != nil {
		t.Errorf("an ordinary folder must have a null role, got %v", work["role"])
	}
}

func TestIntegrationEmailGetOverRealStore(t *testing.T) {
	f := newFixture(t)

	raw := corpusMessage(t, "09-real-world/011-apple-inline-image-in-alternative.eml")
	id := f.seedRaw(t, raw, f.inbox, 1, store.FlagSeen|store.FlagFlagged, []string{"$MoovL7"})

	got := f.call(t, "Email/get", `{"accountId":"`+f.accountID()+
		`","ids":["`+mail.EncodeEmailID(id)+`"],"fetchTextBodyValues":true,"fetchHTMLBodyValues":true}`)

	list := array(t, got, "list")
	if len(list) != 1 {
		t.Fatalf("got %d emails", len(list))
	}
	e, _ := list[0].(map[string]any)

	if e["id"] != mail.EncodeEmailID(id) {
		t.Errorf("id = %v", e["id"])
	}
	// blobId is the sha256 hex: 64 hex characters, and it must be downloadable.
	blobID, _ := e["blobId"].(string)
	if len(blobID) != 64 {
		t.Errorf("blobId = %q, want a 64-character sha256 hex", blobID)
	}
	if e["size"] != float64(len(raw)) {
		t.Errorf("size = %v, want %d", e["size"], len(raw))
	}

	// Flags and the A6 label both surface as JMAP keywords.
	kw := object(t, e, "keywords")
	for _, want := range []string{mail.KeywordSeen, mail.KeywordFlagged, "$moovl7"} {
		if kw[want] != true {
			t.Errorf("keywords = %#v, want %s", kw, want)
		}
	}

	// A real multipart/alternative with an inline image: the body lists must
	// be populated from the structure the production parser produced.
	if len(array(t, e, "textBody")) == 0 {
		t.Error("textBody is empty for a real multipart message")
	}
	bv := object(t, e, "bodyValues")
	if len(bv) == 0 {
		t.Error("bodyValues is empty despite fetchTextBodyValues")
	}
	for partID, v := range bv {
		val, _ := v.(map[string]any)
		if _, ok := val["value"].(string); !ok {
			t.Errorf("part %s has no string value", partID)
		}
		if val["isTruncated"] != false {
			t.Errorf("part %s is truncated without a maxBodyValueBytes", partID)
		}
	}

	// The download path serves the same bytes the blobId names.
	rc, size, err := f.deps.Blobs.OpenBlob(f.ctx, f.account.ID, blobID)
	if err != nil {
		t.Fatalf("OpenBlob: %v", err)
	}
	defer func() { _ = rc.Close() }()
	if size != int64(len(raw)) {
		t.Errorf("blob size = %d, want %d", size, len(raw))
	}
}

// A message the cascade cannot parse must still be served, with its raw blob
// downloadable — the S4 honesty rule.
func TestIntegrationParseFailedMessageIsStillServed(t *testing.T) {
	f := newFixture(t)

	raw := corpusMessage(t, "07-structural/002-empty-file.eml")
	if len(raw) == 0 {
		// An empty corpus file is the point of the case, but the store needs
		// SOME bytes to hash; a single newline is the minimal degenerate mail.
		raw = []byte("\r\n")
	}
	id := f.seedRaw(t, raw, f.inbox, 1, 0, nil)

	got := f.call(t, "Email/get", `{"accountId":"`+f.accountID()+
		`","ids":["`+mail.EncodeEmailID(id)+`"],"fetchAllBodyValues":true}`)

	e := firstObject(t, got, 0)
	if e["blobId"] == nil || e["blobId"] == "" {
		t.Error("a degenerate message must still expose a downloadable blobId")
	}
	// Whatever the parse outcome, the response must be well formed.
	if _, ok := e["bodyValues"].(map[string]any); !ok {
		t.Errorf("bodyValues = %#v, want an object", e["bodyValues"])
	}
}

// Account scoping, proved against the database rather than a fake: a second
// real account's message must be invisible.
func TestIntegrationAccountScopingIsEnforcedInSQL(t *testing.T) {
	f := newFixture(t)

	other, err := f.store.CreateAccount(f.ctx, store.Account{
		Email:    fmt.Sprintf("j2-other-%d@example.test", time.Now().UnixNano()),
		IMAPHost: "dovecot.internal", IMAPPort: 143,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	defer func() { _ = f.store.DeleteAccount(context.Background(), other.ID) }()

	otherBox, err := f.store.UpsertMailbox(f.ctx, store.Mailbox{
		AccountID: other.ID, Name: "INBOX", Delimiter: "/",
		Role: store.RoleInbox, Subscribed: true, Selectable: true,
	})
	if err != nil {
		t.Fatalf("UpsertMailbox: %v", err)
	}

	raw := corpusMessage(t, "09-real-world/007-format-flowed-edges.eml")

	// Seed a message into the OTHER account, by hand (the fixture seeds into
	// its own account).
	h, size, err := f.blobs.Put(f.ctx, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("blob.Put: %v", err)
	}
	ids, err := f.store.InsertMessages(f.ctx, []store.NewMessage{{
		Message: store.Message{
			AccountID: other.ID, RawSHA256: h.Bytes(), RawSize: size,
			Subject: "secret", Date: time.Now(), ParseStatus: store.ParseOK,
		},
		State: store.MessageState{
			AccountID: other.ID, MailboxID: otherBox.ID, UID: 1, UIDValidity: 1,
		},
	}})
	if err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
	foreignID := ids[0]
	if err := f.blobs.AddRefTx(f.ctx, h, other.ID, blob.OwnerMessage, foreignID); err != nil {
		t.Fatalf("AddRef: %v", err)
	}

	// Email/get for the foreign message: notFound, no data.
	got := f.call(t, "Email/get", `{"accountId":"`+f.accountID()+
		`","ids":["`+mail.EncodeEmailID(foreignID)+`"]}`)
	if len(array(t, got, "list")) != 0 {
		t.Fatalf("a foreign message was returned: %#v", got["list"])
	}
	nf := array(t, got, "notFound")
	if len(nf) != 1 || nf[0] != mail.EncodeEmailID(foreignID) {
		t.Fatalf("notFound = %#v", nf)
	}

	// The blob is real and belongs to the other account: the ownership check
	// must refuse it for this caller even though the bytes exist on disk.
	if _, _, err := f.deps.Blobs.OpenBlob(f.ctx, f.account.ID, h.String()); err == nil {
		t.Fatal("OpenBlob served another account's blob")
	}
	// And it IS downloadable by its owner, so the refusal is scoping and not
	// a broken lookup.
	rc, _, err := f.deps.Blobs.OpenBlob(f.ctx, other.ID, h.String())
	if err != nil {
		t.Fatalf("the owning account cannot download its own blob: %v", err)
	}
	_ = rc.Close()
}

// Threading against the real store: the thread_id column of migration 0004,
// assigned by the same JWZ pass the sync pipeline runs, read back through
// Email/get and Thread/get.
func TestIntegrationThreadGetOverRealStore(t *testing.T) {
	f := newFixture(t)

	root := []byte("From: a@example.com\r\nTo: b@example.com\r\n" +
		"Subject: Root\r\nMessage-ID: <root@example.com>\r\n" +
		"Date: Sat, 01 Aug 2026 09:00:00 +0000\r\n\r\nthe first message\r\n")
	reply := []byte("From: b@example.com\r\nTo: a@example.com\r\n" +
		"Subject: Re: Root\r\nMessage-ID: <reply@example.com>\r\n" +
		"In-Reply-To: <root@example.com>\r\nReferences: <root@example.com>\r\n" +
		"Date: Sat, 01 Aug 2026 10:00:00 +0000\r\n\r\nthe reply\r\n")

	rootID := f.seedRaw(t, root, f.inbox, 1, 0, nil)
	replyID := f.seedRaw(t, reply, f.inbox, 2, 0, nil)

	// Both messages must report the SAME threadId, and it must be the root's.
	got := f.call(t, "Email/get", `{"accountId":"`+f.accountID()+
		`","ids":["`+mail.EncodeEmailID(rootID)+`","`+mail.EncodeEmailID(replyID)+
		`"],"properties":["id","threadId"]}`)

	threads := map[string]string{}
	for _, item := range array(t, got, "list") {
		e, _ := item.(map[string]any)
		id, _ := e["id"].(string)
		th, _ := e["threadId"].(string)
		threads[id] = th
	}
	wantThread := mail.EncodeThreadID(rootID)
	for id, th := range threads {
		if th != wantThread {
			t.Errorf("email %s has threadId %s, want %s", id, th, wantThread)
		}
	}

	// Thread/get returns both, oldest first.
	got = f.call(t, "Thread/get", `{"accountId":"`+f.accountID()+`","ids":["`+wantThread+`"]}`)
	list := array(t, got, "list")
	if len(list) != 1 {
		t.Fatalf("got %d threads", len(list))
	}
	emailIDs := array(t, firstObject(t, got, 0), "emailIds")
	if len(emailIDs) != 2 {
		t.Fatalf("emailIds = %#v, want both messages", emailIDs)
	}
	if emailIDs[0] != mail.EncodeEmailID(rootID) {
		t.Errorf("emailIds[0] = %v, want the root (oldest first)", emailIDs[0])
	}
	if emailIDs[1] != mail.EncodeEmailID(replyID) {
		t.Errorf("emailIds[1] = %v, want the reply", emailIDs[1])
	}
}

// The state string must advance when the account's data changes, because J3
// will hand it to /changes as a cursor.
func TestIntegrationStateAdvancesOnChange(t *testing.T) {
	f := newFixture(t)

	before, err := f.deps.State.EmailState(f.ctx, f.account.ID)
	if err != nil {
		t.Fatalf("EmailState: %v", err)
	}

	raw := corpusMessage(t, "09-real-world/007-format-flowed-edges.eml")
	f.seedRaw(t, raw, f.inbox, 1, 0, nil)

	after, err := f.deps.State.EmailState(f.ctx, f.account.ID)
	if err != nil {
		t.Fatalf("EmailState: %v", err)
	}
	if before == after {
		t.Errorf("state did not advance after a message arrived: %q", after)
	}
	if after == "" {
		t.Error("state must never be empty")
	}
}

// A batch at the maxObjectsInGet limit must work against the real store: this
// is the read amplification the store-gap note in the report is about.
func TestIntegrationEmailGetBatch(t *testing.T) {
	f := newFixture(t)

	raw := corpusMessage(t, "09-real-world/007-format-flowed-edges.eml")
	const n = 25
	ids := make([]string, 0, n)
	for i := range n {
		// Each message needs distinct bytes, or the content-addressed blob
		// dedupes them into one row.
		msg := append([]byte(fmt.Sprintf("X-Seq: %d\r\n", i)), raw...)
		id := f.seedRaw(t, msg, f.inbox, int64(i+1), 0, nil)
		ids = append(ids, `"`+mail.EncodeEmailID(id)+`"`)
	}

	got := f.call(t, "Email/get", `{"accountId":"`+f.accountID()+
		`","ids":[`+strings.Join(ids, ",")+`],"properties":["id","subject","threadId"]}`)

	if list := array(t, got, "list"); len(list) != n {
		t.Fatalf("got %d emails, want %d", len(list), n)
	}
	if len(array(t, got, "notFound")) != 0 {
		t.Errorf("notFound = %#v", got["notFound"])
	}
}

// ---------------------------------------------------------------------------
// Seeding helpers: the same derivations internal/sync performs, kept here so
// the fixture does not depend on that package's unexported functions.
// ---------------------------------------------------------------------------

func addrText(addrs []parser.Address) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if s := a.String(); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

func messageDate(header string) time.Time {
	if header != "" {
		if t, err := parseDate(header); err == nil {
			return t
		}
	}
	return time.Now().UTC()
}

func parseDate(s string) (time.Time, error) {
	return time.Parse(time.RFC1123Z, s)
}

func preview(body string) string {
	s := strings.Join(strings.Fields(body), " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func storeStatus(p parser.ParseStatus) store.ParseStatus {
	switch p {
	case parser.StatusPartial:
		return store.ParsePartial
	case parser.StatusFailed:
		return store.ParseFailed
	case parser.StatusOK:
		return store.ParseOK
	default:
		return store.ParseOK
	}
}

func encodeAddresses(h parser.CanonHeaders) []byte {
	type jsonAddress struct {
		Name  string `json:"name,omitempty"`
		Email string `json:"email,omitempty"`
	}
	doc := map[string][]jsonAddress{}
	for key, list := range map[string][]parser.Address{
		"from": h.From, "sender": h.Sender, "replyTo": h.ReplyTo,
		"to": h.To, "cc": h.Cc, "bcc": h.Bcc,
	} {
		if len(list) == 0 {
			continue
		}
		out := make([]jsonAddress, 0, len(list))
		for _, a := range list {
			out = append(out, jsonAddress{Name: a.Name, Email: a.Address})
		}
		doc[key] = out
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func encodeStructure(parts []parser.Part) []byte {
	out := make([]mail.StructurePart, 0, len(parts))
	for _, p := range parts {
		out = append(out, mail.StructurePart{
			Index: p.Index, Parent: p.Parent, Depth: p.Depth,
			MediaType: p.MediaType, Charset: p.Charset, Encoding: p.Encoding,
			Disposition: p.Disposition, Filename: p.Filename, ContentID: p.ContentID,
			Size: p.Size, IsAttachment: p.IsAttachment, IsMultipart: p.IsMultipart,
			IsRFC822: p.IsRFC822, Partial: p.PartiallyDecoded,
		})
	}
	b, err := json.Marshal(map[string]any{"parts": out})
	if err != nil {
		return []byte("{}")
	}
	return b
}
