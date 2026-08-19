package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// Threading against a real PostgreSQL 17.
//
// The JWZ cases the L2 spec names, plus the ones only a real database can
// prove: that a merge advances the state cursors Email/changes reads, that the
// account boundary holds, and that the reindex path is idempotent.

// threadFixture is a mailbox with a message factory, so a test states the
// HEADERS that matter and nothing else.
type threadFixture struct {
	t       *testing.T
	s       *store.Store
	account store.Account
	mailbox store.Mailbox
	nextUID int64
	clock   time.Time
}

// threadMailbox creates a mailbox for a threading test.
func threadMailbox(t *testing.T, s *store.Store, accountID int64, name string) store.Mailbox {
	t.Helper()
	mb, err := s.UpsertMailbox(context.Background(), store.Mailbox{
		AccountID: accountID, Name: name, Subscribed: true, Selectable: true,
	})
	if err != nil {
		t.Fatalf("UpsertMailbox(%s): %v", name, err)
	}
	return mb
}

func newThreadFixture(t *testing.T) *threadFixture {
	t.Helper()
	s := testStore(t)
	acct := newAccount(t, s)
	mb := threadMailbox(t, s, acct.ID, "INBOX")
	return &threadFixture{
		t:       t,
		s:       s,
		account: acct,
		mailbox: mb,
		nextUID: 1,
		clock:   time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC),
	}
}

// insert stores one message and threads it, exactly as the sync pipeline does:
// InsertMessages, then AssignThreads. Returns the message id.
func (f *threadFixture) insert(messageID, inReplyTo, subject string, refs ...string) int64 {
	f.t.Helper()
	return f.insertInAccount(f.account, f.mailbox, messageID, inReplyTo, subject, refs...)
}

func (f *threadFixture) insertInAccount(acct store.Account, mb store.Mailbox, messageID, inReplyTo, subject string, refs ...string) int64 {
	f.t.Helper()
	ctx := context.Background()

	// Each message is a minute later than the last, so `date` order matches
	// insertion order and the "oldest first" assertion is meaningful.
	f.clock = f.clock.Add(time.Minute)
	uid := f.nextUID
	f.nextUID++

	msg := store.NewMessage{
		Message: store.Message{
			AccountID:     acct.ID,
			RawSHA256:     seedBlob(f.t, f.s, fmt.Sprintf("%s-%d-%d", messageID, acct.ID, uid)),
			RawSize:       100,
			MessageID:     messageID,
			InReplyTo:     inReplyTo,
			ReferencesIDs: refs,
			Subject:       subject,
			Date:          f.clock,
		},
		State: store.MessageState{
			AccountID:   acct.ID,
			MailboxID:   mb.ID,
			UID:         uid,
			UIDValidity: 1,
		},
	}

	ids, err := f.s.InsertMessages(ctx, []store.NewMessage{msg})
	if err != nil {
		f.t.Fatalf("InsertMessages: %v", err)
	}

	all := refs
	if inReplyTo != "" {
		all = append(append([]string{}, refs...), inReplyTo)
	}
	if _, err := f.s.AssignThreads(ctx, acct.ID, ids, []store.ThreadCandidate{{
		MessageID:  messageID,
		References: all,
		Subject:    subject,
	}}); err != nil {
		f.t.Fatalf("AssignThreads: %v", err)
	}
	return ids[0]
}

// threadOf reads a message's stored thread id.
func (f *threadFixture) threadOf(id int64) int64 {
	f.t.Helper()
	m, err := f.s.GetMessage(context.Background(), id)
	if err != nil {
		f.t.Fatalf("GetMessage(%d): %v", id, err)
	}
	return m.ThreadID
}

// assertSameThread is the core assertion of almost every case below.
func (f *threadFixture) assertSameThread(want int64, ids ...int64) {
	f.t.Helper()
	for _, id := range ids {
		if got := f.threadOf(id); got != want {
			f.t.Errorf("message %d is in thread %d, want %d", id, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// The JWZ cases
// ---------------------------------------------------------------------------

// A linear reply chain is one thread, keyed by its oldest member.
func TestThreadingLinearChain(t *testing.T) {
	f := newThreadFixture(t)

	root := f.insert("root@test", "", "Presupuesto 2026")
	reply := f.insert("r1@test", "root@test", "Re: Presupuesto 2026", "root@test")
	reply2 := f.insert("r2@test", "r1@test", "Re: Presupuesto 2026", "root@test", "r1@test")

	f.assertSameThread(root, root, reply, reply2)

	// And the thread reads back in receivedAt order, oldest first (RFC 8621 S3).
	members, err := f.s.ThreadMembers(context.Background(), f.account.ID, []int64{root})
	if err != nil {
		t.Fatalf("ThreadMembers: %v", err)
	}
	want := []int64{root, reply, reply2}
	if len(members[root]) != len(want) {
		t.Fatalf("thread has %v, want %v", members[root], want)
	}
	for i, id := range want {
		if members[root][i] != id {
			t.Fatalf("thread order is %v, want %v", members[root], want)
		}
	}
}

// A reply whose References name an ancestor this account does NOT store still
// joins the ancestors it does store - the "non-local root" case the derived
// implementation handled only partially.
func TestThreadingIgnoresUnknownAncestors(t *testing.T) {
	f := newThreadFixture(t)

	known := f.insert("known@test", "", "Hilo de trabajo")
	reply := f.insert("r@test", "known@test", "Re: Hilo de trabajo",
		"never-stored-1@test", "never-stored-2@test", "known@test")

	f.assertSameThread(known, known, reply)
}

// THE HARD CASE: a parent arriving AFTER its children must merge their threads.
//
// RFC 8621 S3: "If messages are delivered out of order for some reason, a user
// may have two Emails in the same Thread but without headers that associate
// them with each other. The arrival of a third Email may provide the missing
// references to join them all together into a single Thread."
func TestThreadingOutOfOrderParentMerges(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	// Two replies to a parent that has not arrived. They name the parent but
	// not each other, so the Message-ID graph cannot join them yet.
	//
	// The subjects are deliberately DIFFERENT from each other, so the subject
	// fallback cannot join them either and the merge path is genuinely the
	// thing under test.
	childA := f.insert("a@test", "parent@test", "Re: Reunion de equipo", "parent@test")
	childB := f.insert("b@test", "parent@test", "Re: Otro asunto distinto", "parent@test")

	if f.threadOf(childA) == f.threadOf(childB) {
		t.Fatalf("children joined before the parent arrived; the merge case is not being exercised")
	}

	// The parent arrives last.
	parent := f.insert("parent@test", "", "Reunion de equipo")

	// All three are now one thread - and the winner is the OLDEST id, which is
	// childA, NOT the parent. That is invariant I2: thread identity is the
	// smallest member id, so it only ever moves down, never up.
	want := childA
	f.assertSameThread(want, childA, childB, parent)

	if want > childB || want > parent {
		t.Fatalf("the merge winner %d is not the oldest of {%d, %d, %d}", want, childA, childB, parent)
	}

	members, err := f.s.ThreadMembers(ctx, f.account.ID, []int64{want})
	if err != nil {
		t.Fatalf("ThreadMembers: %v", err)
	}
	if len(members[want]) != 3 {
		t.Fatalf("merged thread has %d members, want 3: %v", len(members[want]), members[want])
	}

	// The losing thread id must name nothing any more: Thread/get on it is
	// notFound, which is the "destroyed" half of ADR-001's destroyed+created.
	loser := childB
	exists, err := f.s.ThreadExists(ctx, f.account.ID, []int64{loser})
	if err != nil {
		t.Fatalf("ThreadExists: %v", err)
	}
	if exists[loser] {
		t.Errorf("the losing thread id %d still names a thread after the merge", loser)
	}
}

// A merge MUST advance the state cursors of the messages it moved, or
// Email/changes never tells a client the threadId changed and the client serves
// a stale thread forever.
//
// This is the consistency requirement, and it is the reason mergeThread writes
// message_state as well as messages.
func TestThreadMergeAdvancesStateCursors(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	childA := f.insert("a2@test", "p2@test", "Re: Cursor uno", "p2@test")
	childB := f.insert("b2@test", "p2@test", "Re: Cursor dos", "p2@test")
	if f.threadOf(childA) == f.threadOf(childB) {
		t.Skip("children were joined without the parent; the merge path is not exercised")
	}

	before, err := f.s.GetMessageState(ctx, childB)
	if err != nil {
		t.Fatalf("GetMessageState: %v", err)
	}

	// PostgreSQL's now() is the transaction start time, so two statements in
	// quick succession can share it. A pause makes the comparison meaningful
	// rather than flaky.
	time.Sleep(10 * time.Millisecond)

	f.insert("p2@test", "", "Cursor uno")

	after, err := f.s.GetMessageState(ctx, childB)
	if err != nil {
		t.Fatalf("GetMessageState: %v", err)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("merging did not advance message %d's updated_at (%v -> %v); "+
			"Email/changes would never report the threadId change",
			childB, before.UpdatedAt, after.UpdatedAt)
	}

	// And ChangedSince - the query Email/changes actually issues - reports it.
	changed, err := f.s.ChangedSince(ctx, f.account.ID, before.UpdatedAt, 100)
	if err != nil {
		t.Fatalf("ChangedSince: %v", err)
	}
	var found bool
	for _, c := range changed {
		if c.MessageID == childB {
			found = true
		}
	}
	if !found {
		t.Errorf("message %d is not in ChangedSince after its thread changed", childB)
	}
}

// The subject fallback joins a reply that carries NO References at all - the
// mailers that strip them, which is the gap the derived implementation named
// explicitly ("a reply that drops References starts a new thread").
func TestThreadingSubjectFallback(t *testing.T) {
	f := newThreadFixture(t)

	original := f.insert("orig@test", "", "Factura 2026-0042")
	// No In-Reply-To, no References. Only "Re:" and the same subject.
	reply := f.insert("noref@test", "", "Re: Factura 2026-0042")

	f.assertSameThread(original, original, reply)
}

// The fallback must NOT join two unrelated originals that happen to share a
// subject. Only a message that LOOKS like a reply may join by subject.
//
// Without this rule every "Hello" in a mailbox becomes one thread, which is
// worse than not threading.
func TestThreadingSubjectFallbackRequiresReplyMarker(t *testing.T) {
	f := newThreadFixture(t)

	first := f.insert("o1@test", "", "Reporte semanal")
	second := f.insert("o2@test", "", "Reporte semanal")

	if f.threadOf(first) == f.threadOf(second) {
		t.Error("two originals with the same subject were joined; " +
			"the subject fallback must require a reply marker")
	}
}

// A subject too short to be distinctive must not become a thread key.
func TestThreadingSubjectFallbackIgnoresTrivialSubjects(t *testing.T) {
	f := newThreadFixture(t)

	first := f.insert("t1@test", "", "ok")
	second := f.insert("t2@test", "", "Re: ok")

	if f.threadOf(first) == f.threadOf(second) {
		t.Error("a two-character subject was used as a thread key")
	}
}

// THE ISOLATION PROPERTY: threading never crosses an account boundary, however
// identical the headers are. Two tenants receiving the same mailing list post
// must not share a thread.
func TestThreadingIsAccountScoped(t *testing.T) {
	f := newThreadFixture(t)

	other := newAccount(t, f.s)
	otherMB := threadMailbox(t, f.s, other.ID, "INBOX")

	// The very same Message-ID and subject in both accounts.
	mine := f.insert("shared@test", "", "Anuncio general")
	theirs := f.insertInAccount(other, otherMB, "shared@test", "", "Anuncio general")

	// A reply in the other account must join THEIR copy, not mine.
	theirReply := f.insertInAccount(other, otherMB, "their-reply@test", "shared@test",
		"Re: Anuncio general", "shared@test")

	if f.threadOf(theirReply) != f.threadOf(theirs) {
		t.Error("a reply did not join its own account's thread")
	}
	if f.threadOf(theirReply) == f.threadOf(mine) {
		t.Error("threading crossed an account boundary")
	}

	// And the thread read is scoped too: asking MY account for THEIR thread id
	// returns nothing.
	members, err := f.s.ThreadMembers(context.Background(), f.account.ID, []int64{f.threadOf(theirs)})
	if err != nil {
		t.Fatalf("ThreadMembers: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("ThreadMembers leaked another account's thread: %v", members)
	}
}

// Re:/Fwd: normalization must not override the Message-ID graph: a reply whose
// subject was rewritten entirely still threads by its References.
func TestThreadingGraphBeatsSubject(t *testing.T) {
	f := newThreadFixture(t)

	root := f.insert("g1@test", "", "Tema original")
	// Subject changed completely, as happens when someone renames a thread.
	reply := f.insert("g2@test", "g1@test", "Otro asunto totalmente distinto", "g1@test")

	f.assertSameThread(root, root, reply)
}

// ---------------------------------------------------------------------------
// The reads
// ---------------------------------------------------------------------------

// MessagesByIDs is the batch read that closes the J2 round-trip gap. It must
// return both halves, enforce the account scope, and exclude tombstones.
func TestMessagesByIDs(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	a := f.insert("m1@test", "", "Mensaje uno")
	b := f.insert("m2@test", "", "Mensaje dos")

	other := newAccount(t, f.s)
	otherMB := threadMailbox(t, f.s, other.ID, "INBOX")
	foreign := f.insertInAccount(other, otherMB, "m3@test", "", "Mensaje tres")

	got, err := f.s.MessagesByIDs(ctx, f.account.ID, []int64{a, b, foreign, 999999})
	if err != nil {
		t.Fatalf("MessagesByIDs: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (a foreign id and a missing one must be absent)", len(got))
	}
	if _, ok := got[foreign]; ok {
		t.Error("MessagesByIDs returned another account's message")
	}

	// Both halves are populated, which is the point of the join.
	ma := got[a]
	if ma.Message.Subject != "Mensaje uno" {
		t.Errorf("message subject = %q, want %q", ma.Message.Subject, "Mensaje uno")
	}
	if ma.State.MailboxID != f.mailbox.ID {
		t.Errorf("state mailbox = %d, want %d", ma.State.MailboxID, f.mailbox.ID)
	}
	if ma.Message.ThreadID == 0 {
		t.Error("thread_id was not read back")
	}
}

func TestMessagesByIDsExcludesTombstones(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	id := f.insert("tomb@test", "", "Mensaje borrado")
	st, err := f.s.GetMessageState(ctx, id)
	if err != nil {
		t.Fatalf("GetMessageState: %v", err)
	}
	if err := f.s.MarkDeleted(ctx, st.MailboxID, st.UIDValidity, []int64{st.UID}); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	got, err := f.s.MessagesByIDs(ctx, f.account.ID, []int64{id})
	if err != nil {
		t.Fatalf("MessagesByIDs: %v", err)
	}
	if len(got) != 0 {
		t.Error("a tombstoned message must not be returned by a /get read")
	}
}

// The exact counts (RFC 8621 S2), which the adapter could only approximate
// before migration 0004.
func TestCountMailboxThreadsIsExact(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	// One thread of three messages, plus two single-message threads.
	root := f.insert("c1@test", "", "Hilo largo de trabajo")
	f.insert("c2@test", "c1@test", "Re: Hilo largo de trabajo", "c1@test")
	f.insert("c3@test", "c2@test", "Re: Hilo largo de trabajo", "c1@test", "c2@test")
	f.insert("c4@test", "", "Mensaje suelto uno")
	f.insert("c5@test", "", "Mensaje suelto dos")

	total, unread, err := f.s.CountMailboxThreads(ctx, f.mailbox.ID)
	if err != nil {
		t.Fatalf("CountMailboxThreads: %v", err)
	}

	// 5 messages, 3 threads. The old approximation returned 5.
	if total != 3 {
		t.Errorf("totalThreads = %d, want 3 (5 messages in 3 threads)", total)
	}
	if unread != 3 {
		t.Errorf("unreadThreads = %d, want 3 (nothing is marked seen)", unread)
	}

	messages, _, err := f.s.CountMailboxMessages(ctx, f.mailbox.ID)
	if err != nil {
		t.Fatalf("CountMailboxMessages: %v", err)
	}
	if messages == total {
		t.Error("the thread count equals the message count; it is still the approximation")
	}

	// Marking the whole three-message thread seen must drop unreadThreads to 2.
	members, err := f.s.ThreadMembers(ctx, f.account.ID, []int64{root})
	if err != nil {
		t.Fatalf("ThreadMembers: %v", err)
	}
	var updates []store.FlagUpdate
	for _, id := range members[root] {
		updates = append(updates, store.FlagUpdate{MessageID: id, Flags: store.FlagSeen})
	}
	if err := f.s.UpdateFlags(ctx, updates); err != nil {
		t.Fatalf("UpdateFlags: %v", err)
	}

	_, unread, err = f.s.CountMailboxThreads(ctx, f.mailbox.ID)
	if err != nil {
		t.Fatalf("CountMailboxThreads: %v", err)
	}
	if unread != 2 {
		t.Errorf("unreadThreads = %d after reading one thread, want 2", unread)
	}
}

// A thread with one message read and one unread still counts as ONE unread
// thread - the property that distinguishes a thread count from a message count.
func TestCountMailboxThreadsPartiallyRead(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	root := f.insert("p1@test", "", "Hilo parcialmente leido")
	f.insert("p2@test", "p1@test", "Re: Hilo parcialmente leido", "p1@test")

	if err := f.s.UpdateFlags(ctx, []store.FlagUpdate{
		{MessageID: root, Flags: store.FlagSeen},
	}); err != nil {
		t.Fatalf("UpdateFlags: %v", err)
	}

	total, unread, err := f.s.CountMailboxThreads(ctx, f.mailbox.ID)
	if err != nil {
		t.Fatalf("CountMailboxThreads: %v", err)
	}
	if total != 1 || unread != 1 {
		t.Errorf("total=%d unread=%d, want 1 and 1 (one thread, partially read)", total, unread)
	}
}

// CountMailboxes returns the whole tree's four counts in one query, and must
// agree with the per-mailbox methods it replaces.
func TestCountMailboxesAgreesWithPerMailboxCounts(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	second := threadMailbox(t, f.s, f.account.ID, "Archive")

	f.insert("x1@test", "", "Hilo A completo")
	f.insert("x2@test", "x1@test", "Re: Hilo A completo", "x1@test")
	f.insertInAccount(f.account, second, "x3@test", "", "Hilo B completo")

	all, err := f.s.CountMailboxes(ctx, f.account.ID)
	if err != nil {
		t.Fatalf("CountMailboxes: %v", err)
	}

	for _, mb := range []store.Mailbox{f.mailbox, second} {
		wantTotal, wantUnread, err := f.s.CountMailboxMessages(ctx, mb.ID)
		if err != nil {
			t.Fatalf("CountMailboxMessages: %v", err)
		}
		wantT, wantU, err := f.s.CountMailboxThreads(ctx, mb.ID)
		if err != nil {
			t.Fatalf("CountMailboxThreads: %v", err)
		}

		got := all[mb.ID]
		if got.TotalEmails != wantTotal || got.UnreadEmails != wantUnread ||
			got.TotalThreads != wantT || got.UnreadThreads != wantU {
			t.Errorf("mailbox %s: CountMailboxes gave %+v, want {%d %d %d %d}",
				mb.Name, got, wantTotal, wantUnread, wantT, wantU)
		}
	}
}

// ThreadExists answers Thread/get's notFound, and must be account-scoped.
func TestThreadExists(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	id := f.insert("e1@test", "", "Hilo que existe")
	thread := f.threadOf(id)

	got, err := f.s.ThreadExists(ctx, f.account.ID, []int64{thread, 999999})
	if err != nil {
		t.Fatalf("ThreadExists: %v", err)
	}
	if !got[thread] {
		t.Error("an existing thread was reported as absent")
	}
	if got[999999] {
		t.Error("a nonexistent thread was reported as present")
	}
}

// ---------------------------------------------------------------------------
// Reindex
// ---------------------------------------------------------------------------

// ReindexThreads must be idempotent: running it on already-threaded data
// changes nothing. That is what makes it safe to run at any time, which is what
// makes it the documented completion path for a large installation's backfill.
func TestReindexThreadsIsIdempotent(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	root := f.insert("i1@test", "", "Hilo para reindexar")
	r1 := f.insert("i2@test", "i1@test", "Re: Hilo para reindexar", "i1@test")
	r2 := f.insert("i3@test", "i2@test", "Re: Hilo para reindexar", "i1@test", "i2@test")

	before := f.threadOf(root)
	f.assertSameThread(before, root, r1, r2)

	// First pass: everything is already correct, so nothing changes.
	changed, _, err := f.s.ReindexThreads(ctx, f.account.ID, 100, 0)
	if err != nil {
		t.Fatalf("ReindexThreads: %v", err)
	}
	if changed != 0 {
		t.Errorf("reindexing already-threaded data changed %d messages, want 0", changed)
	}
	f.assertSameThread(before, root, r1, r2)

	// Second pass: still nothing.
	changed, _, err = f.s.ReindexThreads(ctx, f.account.ID, 100, 0)
	if err != nil {
		t.Fatalf("ReindexThreads (second pass): %v", err)
	}
	if changed != 0 {
		t.Errorf("a second reindex changed %d messages, want 0", changed)
	}
}

// ReindexThreads repairs data that was never threaded - the state migration
// 0004's backfill leaves on a large installation where it was skipped, and the
// state a crash between InsertMessages and AssignThreads leaves.
func TestReindexThreadsRepairsUnthreadedData(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	// Insert WITHOUT threading, which is exactly the post-crash state.
	ids := f.insertRawChain()

	// Each is its own thread - valid, but not grouped.
	for _, id := range ids {
		if f.threadOf(id) != id {
			t.Fatalf("message %d is not its own thread before reindexing", id)
		}
	}

	changed, _, err := f.s.ReindexThreads(ctx, f.account.ID, 100, 0)
	if err != nil {
		t.Fatalf("ReindexThreads: %v", err)
	}
	if changed == 0 {
		t.Fatal("reindexing unthreaded data changed nothing")
	}

	f.assertSameThread(ids[0], ids...)
}

// insertRawChain stores a reply chain WITHOUT calling AssignThreads.
func (f *threadFixture) insertRawChain() []int64 {
	f.t.Helper()
	ctx := context.Background()

	specs := []struct{ msgID, inReplyTo string }{
		{"raw1@test", ""},
		{"raw2@test", "raw1@test"},
		{"raw3@test", "raw2@test"},
	}

	var out []int64
	for _, sp := range specs {
		f.clock = f.clock.Add(time.Minute)
		uid := f.nextUID
		f.nextUID++

		var refs []string
		if sp.inReplyTo != "" {
			refs = []string{"raw1@test"}
		}

		ids, err := f.s.InsertMessages(ctx, []store.NewMessage{{
			Message: store.Message{
				AccountID:     f.account.ID,
				RawSHA256:     seedBlob(f.t, f.s, fmt.Sprintf("raw-%d-%d", f.account.ID, uid)),
				RawSize:       100,
				MessageID:     sp.msgID,
				InReplyTo:     sp.inReplyTo,
				ReferencesIDs: refs,
				Subject:       "Cadena sin threading",
				Date:          f.clock,
			},
			State: store.MessageState{
				AccountID:   f.account.ID,
				MailboxID:   f.mailbox.ID,
				UID:         uid,
				UIDValidity: 1,
			},
		}})
		if err != nil {
			f.t.Fatalf("InsertMessages: %v", err)
		}
		out = append(out, ids[0])
	}
	return out
}

// Every freshly inserted message is its own thread before assignment - the JWZ
// base case, and the invariant that makes a crash between insert and assignment
// leave VALID data rather than a torn write.
func TestInsertMakesEveryMessageItsOwnThread(t *testing.T) {
	f := newThreadFixture(t)
	ids := f.insertRawChain()

	for _, id := range ids {
		if got := f.threadOf(id); got != id {
			t.Errorf("message %d has thread %d before assignment, want its own id", id, got)
		}
	}
}

// A merge must never run backwards. The direction guard in mergeThread is an
// assertion of invariant I2, and this proves it holds through the public API:
// however the messages arrive, the thread id is the smallest member's.
func TestThreadIDIsAlwaysTheOldestMember(t *testing.T) {
	f := newThreadFixture(t)

	// A chain delivered in the worst possible order: newest first.
	c := f.insert("n3@test", "n2@test", "Re: Orden invertido", "n1@test", "n2@test")
	b := f.insert("n2@test", "n1@test", "Re: Orden invertido", "n1@test")
	a := f.insert("n1@test", "", "Orden invertido")

	// a is the LAST inserted and therefore has the LARGEST id, so the winner
	// must be c - the oldest ROW, not the oldest MESSAGE. Thread identity is a
	// property of storage order, which is what makes it monotone.
	want := c
	if b < want {
		want = b
	}
	if a < want {
		want = a
	}
	f.assertSameThread(want, a, b, c)
}
