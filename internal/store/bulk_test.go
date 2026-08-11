package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// The bulk COPY path and the ON CONFLICT idempotency E6 added.

// bulkMessage builds one message destined for a given UID.
func bulkMessage(t *testing.T, s *store.Store, accountID, mailboxID, uid int64, subject string) store.NewMessage {
	t.Helper()
	return store.NewMessage{
		Message: store.Message{
			AccountID: accountID,
			RawSHA256: seedBlob(t, s, fmt.Sprintf("bulk-%d-%d-%s", accountID, uid, subject)),
			RawSize:   int64(len(subject) + 64),
			Subject:   subject,
			FromAddr:  "sender@example.test",
			BodyText:  "cuerpo de prueba para la carga masiva",
			Date:      time.Now().UTC().Add(-time.Duration(uid) * time.Minute),
		},
		State: store.MessageState{
			AccountID: accountID, MailboxID: mailboxID,
			UID: uid, UIDValidity: 1, ModSeqSeen: uid,
		},
	}
}

// TestCopyMessagesLoadsABatch is the basic contract: the rows land, the ids
// come back, and both halves of the A5 split are present.
func TestCopyMessagesLoadsABatch(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	mbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	const n = 50
	msgs := make([]store.NewMessage, 0, n)
	for i := range n {
		msgs = append(msgs, bulkMessage(t, s, acct.ID, mbox.ID, int64(i+1), fmt.Sprintf("Copy %d", i)))
	}

	res, err := s.CopyMessages(ctx, msgs)
	if err != nil {
		t.Fatalf("CopyMessages: %v", err)
	}
	if res.Inserted != n {
		t.Fatalf("CopyMessages inserted %d, want %d", res.Inserted, n)
	}
	if len(res.IDs) != n {
		t.Fatalf("CopyMessages reported %d ids, want %d", len(res.IDs), n)
	}

	// Every reported id must resolve to a real row with real state, which is
	// what a caller adding blob references depends on.
	for seq, id := range res.IDs {
		msg, err := s.GetMessage(ctx, id)
		if err != nil {
			t.Fatalf("GetMessage(%d) for seq %d: %v", id, seq, err)
		}
		if msg.AccountID != acct.ID {
			t.Errorf("message %d belongs to account %d, want %d", id, msg.AccountID, acct.ID)
		}
		st, err := s.GetMessageState(ctx, id)
		if err != nil {
			t.Fatalf("message %d has no state row: %v", id, err)
		}
		// The seq must name the right message: a join that lost the
		// correlation would silently attach every state row to the wrong
		// message, which is the one bug this path can have that nothing else
		// would catch.
		wantSubject := fmt.Sprintf("Copy %d", seq)
		if msg.Subject != wantSubject {
			t.Errorf("seq %d resolved to subject %q, want %q", seq, msg.Subject, wantSubject)
		}
		if st.UID != int64(seq+1) {
			t.Errorf("seq %d has uid %d, want %d", seq, st.UID, seq+1)
		}
	}

	// The generated tsv must exist: a bulk path that bypassed it would load
	// messages nobody can find.
	var indexed int
	if err := s.Pool().QueryRow(ctx, `
		SELECT count(*) FROM messages
		 WHERE account_id = $1 AND tsv IS NOT NULL AND tsv != ''::tsvector`,
		acct.ID).Scan(&indexed); err != nil {
		t.Fatalf("counting indexed messages: %v", err)
	}
	if indexed != n {
		t.Errorf("%d of %d copied messages have a search vector", indexed, n)
	}
}

// TestCopyMessagesIsIdempotent is what makes an interrupted migration
// resumable: re-running it must skip what is already stored rather than fail on
// the unique index.
func TestCopyMessagesIsIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	mbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	msgs := make([]store.NewMessage, 0, 10)
	for i := range 10 {
		msgs = append(msgs, bulkMessage(t, s, acct.ID, mbox.ID, int64(i+1), fmt.Sprintf("Idem %d", i)))
	}

	first, err := s.CopyMessages(ctx, msgs)
	if err != nil {
		t.Fatalf("first CopyMessages: %v", err)
	}
	if first.Inserted != 10 {
		t.Fatalf("first load inserted %d, want 10", first.Inserted)
	}

	second, err := s.CopyMessages(ctx, msgs)
	if err != nil {
		t.Fatalf("second CopyMessages: %v", err)
	}
	if second.Inserted != 0 {
		t.Errorf("the second load inserted %d rows, want 0", second.Inserted)
	}
	if second.Skipped != 10 {
		t.Errorf("the second load skipped %d rows, want 10", second.Skipped)
	}

	assertNoOrphanMessages(t, s, acct.ID)

	var total int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM messages WHERE account_id = $1`, acct.ID).Scan(&total); err != nil {
		t.Fatalf("counting messages: %v", err)
	}
	if total != 10 {
		t.Errorf("the account holds %d messages after two identical loads, want 10", total)
	}
}

// TestCopyMessagesDeduplicatesWithinOneCall covers the case ON CONFLICT cannot
// resolve: two rows of the SAME statement claiming one UID. PostgreSQL cannot
// arbitrate between them, so the duplicate has to be removed before the insert.
func TestCopyMessagesDeduplicatesWithinOneCall(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	mbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	a := bulkMessage(t, s, acct.ID, mbox.ID, 7, "First claim on uid 7")
	b := bulkMessage(t, s, acct.ID, mbox.ID, 7, "Second claim on uid 7")

	res, err := s.CopyMessages(ctx, []store.NewMessage{a, b})
	if err != nil {
		t.Fatalf("CopyMessages with a duplicate uid: %v", err)
	}
	if res.Inserted != 1 {
		t.Errorf("inserted %d rows for a duplicated uid, want 1", res.Inserted)
	}
	assertNoOrphanMessages(t, s, acct.ID)
}

// TestCopyMessagesEmptyIsANoOp keeps the trivial path from touching the
// database at all.
func TestCopyMessagesEmptyIsANoOp(t *testing.T) {
	s := testStore(t)
	res, err := s.CopyMessages(context.Background(), nil)
	if err != nil {
		t.Fatalf("CopyMessages(nil): %v", err)
	}
	if res.Inserted != 0 || len(res.IDs) != 0 {
		t.Errorf("CopyMessages(nil) = %+v, want a zero result", res)
	}
}

// TestInsertMessagesSkipsAnExistingUID is the ON CONFLICT clause E6 added to
// the batched path.
//
// Before it, a UID that was already stored aborted the WHOLE batch of a hundred
// messages on the unique index. E6 makes concurrent passes over one mailbox
// routine — a watcher event and a reconciler sweep can arrive together — and
// both may read "not stored" before either writes, so the race is a design
// consequence rather than a remote possibility.
func TestInsertMessagesSkipsAnExistingUID(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	mbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	first := bulkMessage(t, s, acct.ID, mbox.ID, 1, "Already stored")
	ids, err := s.InsertMessages(ctx, []store.NewMessage{first})
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if len(ids) != 1 || ids[0] == 0 {
		t.Fatalf("first insert returned %v, want one non-zero id", ids)
	}
	existingID := ids[0]

	// A batch where one message duplicates an existing UID and the others are
	// new: the new ones must still land.
	batch := []store.NewMessage{
		bulkMessage(t, s, acct.ID, mbox.ID, 2, "New A"),
		bulkMessage(t, s, acct.ID, mbox.ID, 1, "Duplicate of uid 1"),
		bulkMessage(t, s, acct.ID, mbox.ID, 3, "New B"),
	}
	ids, err = s.InsertMessages(ctx, batch)
	if err != nil {
		t.Fatalf("batch with a duplicate uid: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("InsertMessages returned %d ids, want 3 (alignment must survive)", len(ids))
	}
	if ids[0] == 0 || ids[2] == 0 {
		t.Errorf("the new messages got ids %v; a duplicate elsewhere must not skip them", ids)
	}
	if ids[1] != 0 {
		t.Errorf("the duplicate got id %d, want 0 to mark it skipped", ids[1])
	}

	// The pre-existing row is untouched — not overwritten by the duplicate.
	msg, err := s.GetMessage(ctx, existingID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.Subject != "Already stored" {
		t.Errorf("the existing message's subject is now %q; the duplicate overwrote it", msg.Subject)
	}

	assertNoOrphanMessages(t, s, acct.ID)
}

// assertNoOrphanMessages checks the invariant "a message row always has state".
//
// An orphan is not cosmetic: it holds a blob reference forever, so the GC can
// never collect bytes nothing points at any more.
func assertNoOrphanMessages(t *testing.T, s *store.Store, accountID int64) {
	t.Helper()
	var orphans int
	err := s.Pool().QueryRow(context.Background(), `
		SELECT count(*) FROM messages m
		 WHERE m.account_id = $1
		   AND NOT EXISTS (SELECT 1 FROM message_state ms WHERE ms.message_id = m.id)`,
		accountID).Scan(&orphans)
	if err != nil {
		t.Fatalf("counting orphans: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d message rows have no state; each holds a blob reference forever", orphans)
	}
}

// TestMessageStatesByUID covers the incremental path's lookup.
func TestMessageStatesByUID(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	mbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	msgs := make([]store.NewMessage, 0, 5)
	for i := range 5 {
		m := bulkMessage(t, s, acct.ID, mbox.ID, int64(i+1), fmt.Sprintf("State %d", i))
		m.State.Flags = store.FlagSeen
		m.State.Keywords = []string{"$MoovL1"}
		msgs = append(msgs, m)
	}
	if _, err := s.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	got, err := s.MessageStatesByUID(ctx, mbox.ID, 1, []int64{1, 3, 99})
	if err != nil {
		t.Fatalf("MessageStatesByUID: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("MessageStatesByUID returned %d rows, want 2 (uid 99 does not exist)", len(got))
	}
	for _, uid := range []int64{1, 3} {
		row, ok := got[uid]
		if !ok {
			t.Fatalf("uid %d is missing from the result", uid)
		}
		if !row.Flags.Has(store.FlagSeen) {
			t.Errorf("uid %d has flags %v, want \\Seen", uid, row.Flags)
		}
		if len(row.Keywords) != 1 || row.Keywords[0] != "$MoovL1" {
			t.Errorf("uid %d has keywords %v, want [$MoovL1]", uid, row.Keywords)
		}
	}

	t.Run("empty input does not query", func(t *testing.T) {
		got, err := s.MessageStatesByUID(ctx, mbox.ID, 1, nil)
		if err != nil || len(got) != 0 {
			t.Errorf("MessageStatesByUID(nil) = (%v, %v), want (empty, nil)", got, err)
		}
	})

	t.Run("tombstoned rows are still reported", func(t *testing.T) {
		if err := s.MarkDeleted(ctx, mbox.ID, 1, []int64{2}); err != nil {
			t.Fatalf("MarkDeleted: %v", err)
		}
		got, err := s.MessageStatesByUID(ctx, mbox.ID, 1, []int64{2})
		if err != nil {
			t.Fatalf("MessageStatesByUID: %v", err)
		}
		if len(got) != 1 {
			t.Fatal("a tombstoned row disappeared from the lookup; its UID would look free")
		}
		if got[2].DeletedAt == nil {
			t.Error("the tombstone's deleted_at is not set")
		}
	})
}

// TestUIDValidityOrZero covers the small accessor the sync engine leans on.
func TestUIDValidityOrZero(t *testing.T) {
	if got := (store.Mailbox{}).UIDValidityOrZero(); got != 0 {
		t.Errorf("a never-selected mailbox reports uidvalidity %d, want 0", got)
	}
	v := int64(4242)
	if got := (store.Mailbox{UIDValidity: &v}).UIDValidityOrZero(); got != 4242 {
		t.Errorf("UIDValidityOrZero = %d, want 4242", got)
	}
}
