package mail_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
	"github.com/GrupoNU/moov/internal/store"
)

// RFC conformance for L3 epic E4, cited clause by clause, driven through the
// real dispatch engine against a real PostgreSQL store — the same discipline as
// conformance_test.go, which explains at length why the official jmapio suite
// cannot be used here.
//
// What this file adds over triage_test.go and schedule_test.go: those run
// against fakes and prove a HANDLER behaves. These run against the database and
// prove the STORE agrees with it — Thread/changes reads real thread rows, and
// the merge tombstone it reports is one the real threading code wrote.
//
// Requires MOOV_TEST_DATABASE_URL; skips cleanly without it.

// e4Call dispatches one method through the engine with the triage capability
// available, so the vendor gate is exercised rather than bypassed.
func e4Call(t *testing.T, f *fixture, method, args string) map[string]any {
	t.Helper()
	registry := jmap.NewRegistry()
	mail.RegisterGetMethods(registry, f.deps)
	mail.RegisterQueryMethods(registry, f.deps)

	engine := jmap.NewEngine(registry, jmap.DefaultLimits(),
		[]string{jmap.CapCore, jmap.CapMail, jmap.CapTriage}, nil)

	body := fmt.Sprintf(
		`{"using":["urn:ietf:params:jmap:core","urn:ietf:params:jmap:mail"],`+
			`"methodCalls":[[%q,%s,"c1"]]}`, method, args)

	resp, rerr := engine.Process(f.callerCtx(), []byte(body), "session-1")
	if rerr != nil {
		t.Fatalf("request-level error: %v", rerr)
	}
	inv := resp.MethodResponses[0]
	if inv.Name == "error" {
		t.Fatalf("%s: %s", method, inv.Args)
	}
	var out map[string]any
	if err := json.Unmarshal(inv.Args, &out); err != nil {
		t.Fatalf("decoding args: %v", err)
	}
	return out
}

// seedThreadMessage seeds one message with the headers threading reads.
func seedThreadMessage(t *testing.T, f *fixture, uid int64, messageID, inReplyTo, subject string, refs ...string) int64 {
	t.Helper()
	headers := fmt.Sprintf(
		"From: remitente@example.test\r\n"+
			"To: destinatario@example.test\r\n"+
			"Subject: %s\r\n"+
			"Message-ID: <%s>\r\n"+
			"Date: Mon, 10 Aug 2026 %02d:00:00 +0000\r\n",
		subject, messageID, uid%24)
	if inReplyTo != "" {
		headers += fmt.Sprintf("In-Reply-To: <%s>\r\n", inReplyTo)
	}
	if len(refs) > 0 {
		headers += "References:"
		for _, r := range refs {
			headers += fmt.Sprintf(" <%s>", r)
		}
		headers += "\r\n"
	}
	raw := []byte(headers + "Content-Type: text/plain; charset=utf-8\r\n\r\ncuerpo\r\n")
	return f.seedRaw(t, raw, f.inbox, uid, 0, nil)
}

// ---------------------------------------------------------------------------
// Thread/changes — RFC 8621 §3.2 over RFC 8620 §5.2
// ---------------------------------------------------------------------------

// RFC 8620 §5.2: "created: Id[] — A list of ids for records that have been
// created since the old state." / "updated: Id[] — A list of ids for records
// that have been updated since the old state."
//
// This is the distinction the pre-E4 decline could not make, and it is made
// here against real rows written by the real threading code.
func TestConformanceThreadChangesTellsCreatedFromUpdated(t *testing.T) {
	f := newFixture(t)

	// A conversation that exists BEFORE the cursor.
	seedThreadMessage(t, f, 1, "root@example.test", "", "Presupuesto")
	before := e4Call(t, f, "Thread/changes",
		fmt.Sprintf(`{"accountId":%q,"sinceState":"0-0"}`, f.accountID()))
	cursor, _ := before["newState"].(string)
	if cursor == "" {
		t.Fatal("Thread/changes returned no newState")
	}
	if len(array(t, before, "created")) == 0 {
		t.Error("the first conversation was not reported created")
	}

	// A reply joins it: the conversation is UPDATED, not created.
	seedThreadMessage(t, f, 2, "r1@example.test", "root@example.test", "Re: Presupuesto", "root@example.test")
	// And a genuinely new conversation, which IS created.
	seedThreadMessage(t, f, 3, "otro@example.test", "", "Otra cosa")

	after := e4Call(t, f, "Thread/changes",
		fmt.Sprintf(`{"accountId":%q,"sinceState":%q}`, f.accountID(), cursor))

	created := array(t, after, "created")
	updated := array(t, after, "updated")
	if len(created) != 1 {
		t.Errorf("created = %v, want exactly the new conversation", created)
	}
	if len(updated) != 1 {
		t.Errorf("updated = %v, want exactly the conversation the reply joined", updated)
	}
	// The two must be DISJOINT: §5.2's lists describe different things, and a
	// client that sees an id in both cannot decide what to do with it.
	for _, c := range created {
		for _, u := range updated {
			if c == u {
				t.Errorf("thread %v is reported both created and updated", c)
			}
		}
	}
}

// §5.2: "destroyed: Id[] — A list of ids for records that have been destroyed
// since the old state."
//
// ADR-001 §2 arbitrated that a thread merge "emite destroyed+created", and the
// pre-0009 schema had nowhere to record the event. This asserts it now does.
func TestConformanceThreadChangesReportsAMerge(t *testing.T) {
	f := newFixture(t)

	// Two conversations that look unrelated.
	seedThreadMessage(t, f, 1, "a@example.test", "", "Presupuesto")
	seedThreadMessage(t, f, 2, "b@example.test", "", "Otra cosa")

	baseline := e4Call(t, f, "Thread/changes",
		fmt.Sprintf(`{"accountId":%q,"sinceState":"0-0"}`, f.accountID()))
	cursor, _ := baseline["newState"].(string)

	// A message naming both: their threads merge, and the loser dies.
	seedThreadMessage(t, f, 3, "bridge@example.test", "a@example.test",
		"Re: Presupuesto", "a@example.test", "b@example.test")

	after := e4Call(t, f, "Thread/changes",
		fmt.Sprintf(`{"accountId":%q,"sinceState":%q}`, f.accountID(), cursor))
	if len(array(t, after, "destroyed")) == 0 {
		t.Error("a merge reported no destroyed thread; a client would keep a conversation " +
			"that no longer exists in its cache forever")
	}
}

// §5.2: "hasMoreChanges: Boolean — If true, the client may call Foo/changes
// again with the newState returned to get further updates."
func TestConformanceThreadChangesPages(t *testing.T) {
	f := newFixture(t)
	for i := int64(1); i <= 4; i++ {
		seedThreadMessage(t, f, i, fmt.Sprintf("t%d@example.test", i), "", fmt.Sprintf("Asunto %d", i))
	}

	first := e4Call(t, f, "Thread/changes",
		fmt.Sprintf(`{"accountId":%q,"sinceState":"0-0","maxChanges":2}`, f.accountID()))
	if first["hasMoreChanges"] != true {
		t.Fatalf("hasMoreChanges = %v with four conversations and maxChanges 2", first["hasMoreChanges"])
	}
	total := len(array(t, first, "created")) + len(array(t, first, "updated"))
	if total != 2 {
		t.Fatalf("the first page has %d ids, want the requested 2", total)
	}

	// Resuming from newState must reach the rest without repeating any.
	seen := map[string]bool{}
	for _, id := range append(array(t, first, "created"), array(t, first, "updated")...) {
		seen[fmt.Sprint(id)] = true
	}
	cursor, _ := first["newState"].(string)
	second := e4Call(t, f, "Thread/changes",
		fmt.Sprintf(`{"accountId":%q,"sinceState":%q,"maxChanges":10}`, f.accountID(), cursor))
	for _, id := range append(array(t, second, "created"), array(t, second, "updated")...) {
		if seen[fmt.Sprint(id)] {
			t.Errorf("thread %v was returned on both pages", id)
		}
	}
}

// §5.2: "If the server cannot calculate the changes from the state string
// given by the client, [it] MUST return a cannotCalculateChanges error."
func TestConformanceThreadChangesRefusesAnUnknownCursor(t *testing.T) {
	f := newFixture(t)
	registry := jmap.NewRegistry()
	mail.RegisterQueryMethods(registry, f.deps)
	engine := jmap.NewEngine(registry, jmap.DefaultLimits(),
		[]string{jmap.CapCore, jmap.CapMail}, nil)

	body := fmt.Sprintf(
		`{"using":["urn:ietf:params:jmap:core","urn:ietf:params:jmap:mail"],`+
			`"methodCalls":[["Thread/changes",{"accountId":%q,"sinceState":"not-a-cursor"},"c1"]]}`,
		f.accountID())
	resp, rerr := engine.Process(f.callerCtx(), []byte(body), "session-1")
	if rerr != nil {
		t.Fatalf("request-level error: %v", rerr)
	}
	inv := resp.MethodResponses[0]
	if inv.Name != "error" {
		t.Fatalf("a foreign state string was accepted: %s", inv.Args)
	}
	var e map[string]any
	if err := json.Unmarshal(inv.Args, &e); err != nil {
		t.Fatalf("decoding the error: %v", err)
	}
	if e["type"] != string(jmap.CodeCannotCalculateChanges) {
		t.Errorf("type = %v, want cannotCalculateChanges", e["type"])
	}
}

// ---------------------------------------------------------------------------
// Snooze and Mute — Moov's vendor objects, over RFC 8620 §5.1/§5.3
// ---------------------------------------------------------------------------

// triageEngine mounts the triage methods over the real store-backed adapter.
//
// The write executor is NOT wired, so a snooze CREATE would fail — which is
// correct and is why these tests exercise the READ side and the mute side,
// where the store is the whole implementation. The engine-side snooze (the
// Dovecot MOVE) is proven in internal/sync against a fake IMAP server.
func triageEngine(t *testing.T, f *fixture, tri mail.TriageStore) *jmap.Engine {
	t.Helper()
	registry := jmap.NewRegistry()
	deps := *f.deps
	deps.Triage = tri
	mail.RegisterTriageMethods(registry, &deps)
	return jmap.NewEngine(registry, jmap.DefaultLimits(),
		[]string{jmap.CapCore, jmap.CapMail, jmap.CapTriage}, nil)
}

// storeTriage is the half of TriageStore that needs no IMAP: the mute surface,
// backed by the real store, plus the snooze reads.
type storeTriage struct {
	f *fixture
}

func (s storeTriage) ListSnoozes(ctx context.Context, accountID int64, limit int) ([]mail.SnoozeRecord, error) {
	rows, err := s.f.store.PendingSnoozes(ctx, accountID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]mail.SnoozeRecord, 0, len(rows))
	for _, sn := range rows {
		out = append(out, mail.SnoozeRecord{Until: sn.WakeAt, OriginMailboxName: sn.OriginMailbox})
	}
	return out, nil
}

func (s storeTriage) SnoozeState(ctx context.Context, accountID int64) (string, error) {
	_, count, err := s.f.store.SnoozeWatermark(ctx, accountID)
	return fmt.Sprintf("1-%d", count), err
}

func (s storeTriage) Snooze(context.Context, int64, int64, time.Time) (mail.SnoozeRecord, error) {
	return mail.SnoozeRecord{}, mail.ErrSnoozeUnavailable
}

func (s storeTriage) Unsnooze(context.Context, int64, int64) error { return mail.ErrNotSnoozed }

func (s storeTriage) ListMutes(ctx context.Context, accountID int64, limit int) ([]int64, error) {
	return s.f.store.ListMutedThreads(ctx, accountID, limit)
}

func (s storeTriage) MuteState(ctx context.Context, accountID int64) (string, error) {
	_, count, err := s.f.store.MuteWatermark(ctx, accountID)
	return fmt.Sprintf("1-%d", count), err
}

func (s storeTriage) SetMuted(ctx context.Context, accountID, threadID int64, muted bool) error {
	row, err := s.f.store.ThreadRowByThreadID(ctx, accountID, threadID)
	if err != nil {
		created, cerr := s.f.store.EnsureThreadRowFor(ctx, accountID, threadID)
		if cerr != nil {
			return mail.ErrNotFound
		}
		row = created
	}
	return s.f.store.SetMute(ctx, accountID, row.ID, muted)
}

// RFC 8620 §5.3: "create: Id[Foo]|null — A map of a creation id to a map of
// the record's properties" / "destroyed: Id[] — A list of Foo ids for records
// that were successfully destroyed".
//
// Muting through the real store, over the real durable-key resolution: this is
// the path that would silently do nothing if the volatile-to-durable id
// translation were wrong.
func TestConformanceMuteSetOverTheRealStore(t *testing.T) {
	f := newFixture(t)
	id := seedThreadMessage(t, f, 1, "root@example.test", "", "Presupuesto")

	msg, err := f.store.GetMessage(f.ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	threadWireID := mail.EncodeThreadID(msg.ThreadID)
	engine := triageEngine(t, f, storeTriage{f})

	call := func(args string) map[string]any {
		t.Helper()
		body := fmt.Sprintf(
			`{"using":["urn:ietf:params:jmap:core","https://moov.email/ns/triage"],`+
				`"methodCalls":[["Mute/set",%s,"c1"]]}`, args)
		resp, rerr := engine.Process(f.callerCtx(), []byte(body), "session-1")
		if rerr != nil {
			t.Fatalf("request-level error: %v", rerr)
		}
		inv := resp.MethodResponses[0]
		if inv.Name == "error" {
			t.Fatalf("Mute/set: %s", inv.Args)
		}
		var out map[string]any
		if uerr := json.Unmarshal(inv.Args, &out); uerr != nil {
			t.Fatalf("decoding: %v", uerr)
		}
		return out
	}

	out := call(fmt.Sprintf(`{"accountId":%q,"create":{"m1":{"threadId":%q}}}`,
		f.accountID(), threadWireID))
	if object(t, out, "created")["m1"] == nil {
		t.Fatalf("the mute was refused: %v", out["notCreated"])
	}

	// The engine's own question — "is this conversation muted?" — must now
	// answer yes, through the same volatile thread id the client sent.
	muted, err := f.store.IsThreadMuted(f.ctx, f.account.ID, msg.ThreadID)
	if err != nil || !muted {
		t.Fatalf("IsThreadMuted = %v, %v after a successful Mute/set", muted, err)
	}

	out = call(fmt.Sprintf(`{"accountId":%q,"destroy":[%q]}`, f.accountID(), threadWireID))
	if len(array(t, out, "destroyed")) != 1 {
		t.Fatalf("the unmute was refused: %v", out["notDestroyed"])
	}
	muted, err = f.store.IsThreadMuted(f.ctx, f.account.ID, msg.ThreadID)
	if err != nil || muted {
		t.Fatalf("IsThreadMuted = %v, %v after a successful destroy", muted, err)
	}
}

// RFC 8620 §1.8: "The client MUST opt in to use an extension by passing the
// appropriate capability identifier in the 'using' array [...] The server MUST
// only follow the specifications that are opted into and behave as though it
// does not implement anything else when processing a request."
//
// THE property the whole vendor-capability design exists for: Bulwark, and
// every other conforming client, must be unable to tell these methods exist.
func TestConformanceTriageIsInvisibleWithoutTheCapability(t *testing.T) {
	f := newFixture(t)
	engine := triageEngine(t, f, storeTriage{f})

	body := fmt.Sprintf(
		`{"using":["urn:ietf:params:jmap:core","urn:ietf:params:jmap:mail"],`+
			`"methodCalls":[["Mute/get",{"accountId":%q},"c1"]]}`, f.accountID())
	resp, rerr := engine.Process(f.callerCtx(), []byte(body), "session-1")
	if rerr != nil {
		t.Fatalf("request-level error: %v", rerr)
	}
	inv := resp.MethodResponses[0]
	if inv.Name != "error" {
		t.Fatalf("Mute/get answered a client that opted into the mail capability only: %s", inv.Args)
	}
	var e map[string]any
	if err := json.Unmarshal(inv.Args, &e); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	// §3.6.1's unknownMethod is the answer §1.8 prescribes: the server behaves
	// as though it does not implement the extension.
	if e["type"] != string(jmap.CodeUnknownMethod) {
		t.Errorf("type = %v, want unknownMethod", e["type"])
	}
}

// RFC 8620 §5.1: "notFound: Id[] — This array contains the ids passed to the
// method for records that do not exist."
//
// `is:muted` IS this method: a client asks about the threads it is showing and
// badges the ones that come back. An unmuted thread must therefore be notFound
// rather than an object with muted:false, which would double the payload of
// every list render.
func TestConformanceMuteGetAnswersIsMuted(t *testing.T) {
	f := newFixture(t)
	a := seedThreadMessage(t, f, 1, "a@example.test", "", "Presupuesto")
	b := seedThreadMessage(t, f, 2, "b@example.test", "", "Otra cosa")

	msgA, _ := f.store.GetMessage(f.ctx, a)
	msgB, _ := f.store.GetMessage(f.ctx, b)

	row, err := f.store.ThreadRowByThreadID(f.ctx, f.account.ID, msgA.ThreadID)
	if err != nil {
		t.Fatalf("ThreadRowByThreadID: %v", err)
	}
	if err := f.store.SetMute(f.ctx, f.account.ID, row.ID, true); err != nil {
		t.Fatalf("SetMute: %v", err)
	}

	engine := triageEngine(t, f, storeTriage{f})
	body := fmt.Sprintf(
		`{"using":["urn:ietf:params:jmap:core","https://moov.email/ns/triage"],`+
			`"methodCalls":[["Mute/get",{"accountId":%q,"ids":[%q,%q]},"c1"]]}`,
		f.accountID(), mail.EncodeThreadID(msgA.ThreadID), mail.EncodeThreadID(msgB.ThreadID))
	resp, rerr := engine.Process(f.callerCtx(), []byte(body), "session-1")
	if rerr != nil {
		t.Fatalf("request-level error: %v", rerr)
	}
	var out map[string]any
	if err := json.Unmarshal(resp.MethodResponses[0].Args, &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(array(t, out, "list")) != 1 {
		t.Errorf("list = %v, want only the muted conversation", out["list"])
	}
	if len(array(t, out, "notFound")) != 1 {
		t.Errorf("notFound = %v, want the unmuted conversation", out["notFound"])
	}
}

// RFC 8620 §5.1: "state: String — A string representing the state on the
// server for all the data of this type in the account."
//
// The count term is what makes an UNMUTE observable: the row is deleted, so
// the watermark alone would not distinguish before from after, and a client
// polling on the state would badge the conversation as muted forever.
func TestConformanceMuteStateMovesOnEveryWrite(t *testing.T) {
	f := newFixture(t)
	id := seedThreadMessage(t, f, 1, "root@example.test", "", "Presupuesto")
	msg, _ := f.store.GetMessage(f.ctx, id)
	row, err := f.store.ThreadRowByThreadID(f.ctx, f.account.ID, msg.ThreadID)
	if err != nil {
		t.Fatalf("ThreadRowByThreadID: %v", err)
	}

	tri := storeTriage{f}
	empty, err := tri.MuteState(f.ctx, f.account.ID)
	if err != nil {
		t.Fatalf("MuteState: %v", err)
	}
	if err := f.store.SetMute(f.ctx, f.account.ID, row.ID, true); err != nil {
		t.Fatalf("SetMute: %v", err)
	}
	muted, _ := tri.MuteState(f.ctx, f.account.ID)
	if muted == empty {
		t.Fatal("the mute state did not move when a conversation was muted")
	}
	if err := f.store.SetMute(f.ctx, f.account.ID, row.ID, false); err != nil {
		t.Fatalf("SetMute(false): %v", err)
	}
	unmuted, _ := tri.MuteState(f.ctx, f.account.ID)
	if unmuted == muted {
		t.Error("the mute state did not move when the conversation was unmuted; " +
			"a second session would badge it as muted forever")
	}
}

// A defensive assertion about the snooze folder's NAME, which is part of the
// capability's contract precisely because RFC 6154 has no SPECIAL-USE
// attribute for snoozed mail and a client cannot resolve it by role.
func TestConformanceSnoozeMailboxNameIsPublished(t *testing.T) {
	if mail.SnoozeMailboxName == "" {
		t.Fatal("the Snoozed mailbox has no published name; a client could not find the folder")
	}
	// It must not collide with a role folder, which a client resolves the
	// standard way — a "Snoozed" folder carrying \Archive would be ambiguous.
	for _, role := range []store.MailboxRole{
		store.RoleInbox, store.RoleArchive, store.RoleDrafts, store.RoleSent,
		store.RoleJunk, store.RoleTrash, store.RoleAll, store.RoleFlagged,
	} {
		if mail.SnoozeMailboxName == string(role) {
			t.Errorf("the Snoozed folder's name collides with the %q role", role)
		}
	}
}
