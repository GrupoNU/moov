package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// The write executor's unit suite (W1). Fake server, real store — the same
// split as the incremental tests, because the properties under test live on
// both sides of the boundary: the ordering (Dovecot FIRST, store second),
// the conflict refusal, the W-A2 destroy semantics, and the echo convergence
// with the incremental pass.

// writeEnv is a synced account plus a write executor over the same fake
// server, with a Trash and an Archive folder because W-A2 needs both
// destinations to exist.
type writeEnv struct {
	*testEnv
	srv    *fakeServer
	syncer *Syncer
	exec   *WriteExecutor
}

func newWriteEnv(t *testing.T, inboxMessages int) *writeEnv {
	t.Helper()

	env := newTestEnv(t)
	env.mustSyncableAccount(t)

	srv := newFakeServer()
	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, inboxMessages, referenceNow, "Inbox")
	srv.addMailbox("Archive", imap.RoleArchive, 200)
	trash := srv.addMailbox("Trash", imap.RoleTrash, 300)
	seedMailbox(trash, 1, referenceNow, "Trash")

	opts := env.testOptions(referenceNow)
	syncer := env.syncer(t, srv, opts)
	if _, err := syncer.Run(context.Background(), env.account); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	exec, err := NewWriteExecutor(env.store, ConnectorFunc(
		func(_ context.Context, _ store.Account, n int) ([]imap.Client, error) {
			return srv.clients(n), nil
		}), WriteOptions{Logger: env.logger})
	if err != nil {
		t.Fatalf("NewWriteExecutor: %v", err)
	}
	t.Cleanup(exec.Close)

	return &writeEnv{testEnv: env, srv: srv, syncer: syncer, exec: exec}
}

// stateByUID resolves a stored message by (mailbox name, uid).
func (e *writeEnv) stateByUID(t *testing.T, mailbox string, uid int64) store.MessageState {
	t.Helper()
	ctx := context.Background()
	row, err := e.store.GetMailboxByName(ctx, e.account.ID, mailbox)
	if err != nil {
		t.Fatalf("GetMailboxByName(%q): %v", mailbox, err)
	}
	st, err := e.store.GetMessageStateByUID(ctx, row.ID, row.UIDValidityOrZero(), uid)
	if err != nil {
		t.Fatalf("GetMessageStateByUID(%s, %d): %v", mailbox, uid, err)
	}
	return st
}

// state re-reads a message's current state row by id.
func (e *writeEnv) state(t *testing.T, messageID int64) store.MessageState {
	t.Helper()
	st, err := e.store.GetMessageState(context.Background(), messageID)
	if err != nil {
		t.Fatalf("GetMessageState(%d): %v", messageID, err)
	}
	return st
}

// serverMessage returns the fake server's copy of a message.
func (e *writeEnv) serverMessage(mailbox string, uid imap.UID) *fakeMessage {
	e.srv.mu.Lock()
	defer e.srv.mu.Unlock()
	mb := e.srv.mailbox(mailbox)
	if mb == nil {
		return nil
	}
	if m := mb.find(uid); m != nil {
		c := *m
		return &c
	}
	return nil
}

// pass runs one incremental pass over a mailbox — the echo pass, in these
// tests' vocabulary.
func (e *writeEnv) pass(t *testing.T, mailbox string) IncrementalResult {
	t.Helper()
	ctx := context.Background()
	row, err := e.store.GetMailboxByName(ctx, e.account.ID, mailbox)
	if err != nil {
		t.Fatalf("GetMailboxByName(%q): %v", mailbox, err)
	}
	mb := syncMailbox{row: row, info: imap.MailboxInfo{Name: mailbox}}

	var res IncrementalResult
	err = e.syncer.conns.withConn(ctx, func(c imap.Client) error {
		var perr error
		res, perr = e.syncer.incrementalMailbox(ctx, c, e.account, mb, e.logger)
		return perr
	})
	if err != nil {
		t.Fatalf("incremental pass on %q: %v", mailbox, err)
	}
	return res
}

// ---------------------------------------------------------------------------
// flags
// ---------------------------------------------------------------------------

func TestApplyFlagChangePatchWritesServerFirstAndReflects(t *testing.T) {
	e := newWriteEnv(t, 4)
	st := e.stateByUID(t, "INBOX", 2) // uid 2 is unflagged in the seed

	res, err := e.exec.ApplyFlagChange(context.Background(), e.account.ID, st.MessageID,
		FlagChange{Add: []string{"flagged", "$w1test"}})
	if err != nil {
		t.Fatalf("ApplyFlagChange: %v", err)
	}

	// The server has it.
	msg := e.serverMessage("INBOX", 2)
	if msg == nil || !containsFold(msg.flags, "flagged") || !containsFold(msg.keywords, "$w1test") {
		t.Fatalf("server flags = %v %v, want flagged + $w1test", msg.flags, msg.keywords)
	}
	// The store reflects the server's read-back, including the fresh modseq.
	after := e.state(t, st.MessageID)
	if !after.Flags.Has(store.FlagFlagged) {
		t.Errorf("store flags = %v, want \\Flagged set", after.Flags)
	}
	if !sameKeywords(after.Keywords, []string{"$w1test"}) {
		t.Errorf("store keywords = %v, want [$w1test]", after.Keywords)
	}
	if after.ModSeqSeen != int64(msg.modSeq) {
		t.Errorf("modseq_seen = %d, want the server's %d", after.ModSeqSeen, msg.modSeq)
	}
	if res.Flags != after.Flags {
		t.Errorf("result flags = %v, store has %v", res.Flags, after.Flags)
	}
}

func TestApplyFlagChangeStoreUntouchedWhenIMAPFails(t *testing.T) {
	// THE W-A1 ordering test: if Dovecot refuses, the store must not move.
	e := newWriteEnv(t, 3)
	st := e.stateByUID(t, "INBOX", 1)

	injected := errors.New("injected: connection lost mid-STORE")
	e.srv.storeErr = injected

	_, err := e.exec.ApplyFlagChange(context.Background(), e.account.ID, st.MessageID,
		FlagChange{Add: []string{"flagged"}})
	if err == nil {
		t.Fatal("ApplyFlagChange succeeded with a failing server")
	}

	after := e.state(t, st.MessageID)
	if after.Flags != st.Flags || !sameKeywords(after.Keywords, st.Keywords) ||
		after.ModSeqSeen != st.ModSeqSeen || !after.UpdatedAt.Equal(st.UpdatedAt) {
		t.Errorf("store row moved despite the IMAP failure:\n before %+v\n after  %+v", st, after)
	}
}

func TestApplyFlagChangeReplaceConflictIsSurfaced(t *testing.T) {
	e := newWriteEnv(t, 3)
	st := e.stateByUID(t, "INBOX", 1)

	// Another client changes the message AFTER the store's snapshot: the
	// modseq moves and the content genuinely differs from what Moov served.
	e.srv.setFlags("INBOX", 1, []string{"seen", "answered"}, []string{"racing"})

	_, err := e.exec.ApplyFlagChange(context.Background(), e.account.ID, st.MessageID,
		FlagChange{Replace: true, Flags: []string{"seen"}})
	if !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("err = %v, want ErrWriteConflict", err)
	}

	// The refusal reflected the server's CURRENT state into the store, so the
	// client's re-read sees the truth rather than the stale row that caused
	// the conflict.
	after := e.state(t, st.MessageID)
	if !after.Flags.Has(store.FlagAnswered) || !sameKeywords(after.Keywords, []string{"racing"}) {
		t.Errorf("store was not refreshed on conflict: %+v", after)
	}
	// And the server kept the concurrent writer's state: nothing clobbered.
	msg := e.serverMessage("INBOX", 1)
	if !containsFold(msg.flags, "answered") {
		t.Errorf("server flags = %v; the conditional STORE must not have applied", msg.flags)
	}
}

func TestApplyFlagChangeReplaceRetriesWhenContentStillMatches(t *testing.T) {
	e := newWriteEnv(t, 3)
	st := e.stateByUID(t, "INBOX", 1) // seeded flags: ["seen"]

	// The modseq moves twice but the content ends where the store believes it
	// is (a toggle and its undo — or an echo the incremental pass skipped).
	// The client's premise still holds, so the write must succeed, not
	// bounce forever on a stale modseq_seen.
	e.srv.setFlags("INBOX", 1, []string{"seen", "flagged"}, nil)
	e.srv.setFlags("INBOX", 1, []string{"seen"}, nil)

	_, err := e.exec.ApplyFlagChange(context.Background(), e.account.ID, st.MessageID,
		FlagChange{Replace: true, Flags: []string{"seen", "answered"}})
	if err != nil {
		t.Fatalf("ApplyFlagChange = %v, want the self-healing retry to succeed", err)
	}
	msg := e.serverMessage("INBOX", 1)
	if !containsFold(msg.flags, "answered") {
		t.Errorf("server flags = %v, want answered applied", msg.flags)
	}
}

func TestApplyFlagChangeReplacePreservesFlagsInvisibleToJMAP(t *testing.T) {
	e := newWriteEnv(t, 3)

	// Arrange \Deleted on both sides, as a synced pre-expunge state would be.
	e.srv.setFlags("INBOX", 1, []string{"seen", "deleted"}, nil)
	if res := e.pass(t, "INBOX"); res.FlagsUpdated != 1 {
		t.Fatalf("arranging pass applied %d flag updates, want 1", res.FlagsUpdated)
	}
	st := e.stateByUID(t, "INBOX", 1)
	if !st.Flags.Has(store.FlagDeleted) {
		t.Fatalf("arrangement failed: store lacks \\Deleted: %v", st.Flags)
	}

	// A JMAP full-set replace cannot express \Deleted (RFC 8621 §4.1.1 maps
	// no keyword to it), so it must survive the replace.
	if _, err := e.exec.ApplyFlagChange(context.Background(), e.account.ID, st.MessageID,
		FlagChange{Replace: true, Flags: []string{"flagged"}}); err != nil {
		t.Fatalf("ApplyFlagChange: %v", err)
	}
	msg := e.serverMessage("INBOX", 1)
	if !containsFold(msg.flags, "deleted") {
		t.Errorf("server flags = %v; the replace erased \\Deleted, state JMAP cannot even see", msg.flags)
	}
	if !containsFold(msg.flags, "flagged") || containsFold(msg.flags, "seen") {
		t.Errorf("server flags = %v, want exactly flagged (+deleted preserved)", msg.flags)
	}
}

func TestWriteRefusesForeignAndTombstonedMessages(t *testing.T) {
	e := newWriteEnv(t, 2)
	st := e.stateByUID(t, "INBOX", 1)

	// A caller that is not the owner: same message id, wrong account.
	if _, err := e.exec.ApplyFlagChange(context.Background(), e.account.ID+999, st.MessageID,
		FlagChange{Add: []string{"seen"}}); !errors.Is(err, ErrWriteNotFound) {
		t.Errorf("foreign account err = %v, want ErrWriteNotFound", err)
	}

	// A tombstoned message is gone as far as writes are concerned.
	row, err := e.store.GetMailboxByName(context.Background(), e.account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.MarkDeleted(context.Background(), row.ID, st.UIDValidity, []int64{st.UID}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.exec.ApplyFlagChange(context.Background(), e.account.ID, st.MessageID,
		FlagChange{Add: []string{"seen"}}); !errors.Is(err, ErrWriteNotFound) {
		t.Errorf("tombstoned err = %v, want ErrWriteNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// echo convergence and replay
// ---------------------------------------------------------------------------

func TestFlagWriteEchoConvergesWithoutFlapping(t *testing.T) {
	e := newWriteEnv(t, 4)
	st := e.stateByUID(t, "INBOX", 2)

	if _, err := e.exec.ApplyFlagChange(context.Background(), e.account.ID, st.MessageID,
		FlagChange{Add: []string{"flagged"}}); err != nil {
		t.Fatalf("ApplyFlagChange: %v", err)
	}
	reflected := e.state(t, st.MessageID)

	// The watcher's echo: the incremental pass sees our own write come back
	// as a CHANGEDSINCE row. It must recognize the state as already applied —
	// zero flag updates, and updated_at (the Email/changes cursor) untouched,
	// or every own-write would make the client re-fetch its own change twice.
	res := e.pass(t, "INBOX")
	if res.FlagsUpdated != 0 || res.New != 0 {
		t.Errorf("echo pass applied changes: %+v (must recognize its own write)", res)
	}
	after := e.state(t, st.MessageID)
	if !after.UpdatedAt.Equal(reflected.UpdatedAt) {
		t.Errorf("updated_at moved on the echo (%v -> %v): clients would re-fetch for nothing",
			reflected.UpdatedAt, after.UpdatedAt)
	}

	// And a second pass stays quiet too — converged, not oscillating.
	if res := e.pass(t, "INBOX"); res.Changed() {
		t.Errorf("second echo pass still reports changes: %+v", res)
	}
}

func TestFlagWriteReplayIsIdempotent(t *testing.T) {
	e := newWriteEnv(t, 3)
	st := e.stateByUID(t, "INBOX", 2)
	change := FlagChange{Add: []string{"flagged"}}

	first, err := e.exec.ApplyFlagChange(context.Background(), e.account.ID, st.MessageID, change)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	afterFirst := e.state(t, st.MessageID)

	// The same /set replayed (a client retry after a lost response).
	second, err := e.exec.ApplyFlagChange(context.Background(), e.account.ID, st.MessageID, change)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	afterSecond := e.state(t, st.MessageID)

	if first.Flags != second.Flags || !sameKeywords(first.Keywords, second.Keywords) {
		t.Errorf("replay changed the outcome: %+v vs %+v", first, second)
	}
	// A no-op replay must not bump the server modseq (the fake mirrors
	// Dovecot there) nor the row's cursor.
	if afterSecond.ModSeqSeen != afterFirst.ModSeqSeen || !afterSecond.UpdatedAt.Equal(afterFirst.UpdatedAt) {
		t.Errorf("replay moved the row: %+v vs %+v", afterFirst, afterSecond)
	}
}

// ---------------------------------------------------------------------------
// moves
// ---------------------------------------------------------------------------

func TestApplyMoveReflectsThroughCopyUIDWithoutRefetch(t *testing.T) {
	e := newWriteEnv(t, 4)
	st := e.stateByUID(t, "INBOX", 3)
	archive, err := e.store.GetMailboxByName(context.Background(), e.account.ID, "Archive")
	if err != nil {
		t.Fatal(err)
	}

	downloadsBefore := e.srv.fetchCount

	res, err := e.exec.ApplyMove(context.Background(), e.account.ID, st.MessageID, archive.ID)
	if err != nil {
		t.Fatalf("ApplyMove: %v", err)
	}

	// Server: gone from INBOX, present in Archive.
	if m := e.serverMessage("INBOX", 3); m != nil {
		t.Error("the message is still in INBOX on the server")
	}
	moved := e.serverMessage("Archive", imap.UID(res.UID))
	if moved == nil {
		t.Fatalf("the message is not in Archive at uid %d", res.UID)
	}

	// Store: the SAME row re-pointed — same message id, new mailbox and UID,
	// fresh modseq. No tombstone, no second row.
	after := e.state(t, st.MessageID)
	if after.MailboxID != archive.ID || after.UID != res.UID || after.DeletedAt != nil {
		t.Errorf("row not re-pointed: %+v", after)
	}
	if after.ModSeqSeen != int64(moved.modSeq) {
		t.Errorf("modseq_seen = %d, want the destination's %d", after.ModSeqSeen, moved.modSeq)
	}

	// The echo, from both sides: the source pass sees VANISHED for a row that
	// already moved; the destination pass sees a "new" UID that is already
	// stored. Neither may duplicate, tombstone or re-download anything.
	e.pass(t, "INBOX")
	destRes := e.pass(t, "Archive")
	if destRes.New != 0 {
		t.Errorf("destination echo pass fetched %d messages; the reflection should have made it a no-op", destRes.New)
	}
	final := e.state(t, st.MessageID)
	if final.DeletedAt != nil {
		t.Error("the source echo tombstoned the moved row")
	}
	if e.srv.fetchCount != downloadsBefore {
		t.Errorf("the move cost %d body downloads; COPYUID reflection should cost none",
			e.srv.fetchCount-downloadsBefore)
	}
}

func TestApplyMoveWithoutCopyUIDDegradesToTombstone(t *testing.T) {
	e := newWriteEnv(t, 3)
	st := e.stateByUID(t, "INBOX", 1)
	archive, err := e.store.GetMailboxByName(context.Background(), e.account.ID, "Archive")
	if err != nil {
		t.Fatal(err)
	}

	e.srv.noCopyUID = true
	if _, err := e.exec.ApplyMove(context.Background(), e.account.ID, st.MessageID, archive.ID); err != nil {
		t.Fatalf("ApplyMove: %v", err)
	}

	// The server moved it; the store, without a mapping, tombstoned the
	// source row — the documented degradation.
	after := e.state(t, st.MessageID)
	if after.DeletedAt == nil {
		t.Errorf("row not tombstoned on the unmapped path: %+v", after)
	}
	// The ordinary sync then ingests the destination copy as a new message.
	res := e.pass(t, "Archive")
	if res.New != 1 {
		t.Errorf("destination pass stored %d messages, want 1 (the rediscovered copy)", res.New)
	}
}

func TestApplyMoveToSameMailboxIsANoOp(t *testing.T) {
	e := newWriteEnv(t, 2)
	st := e.stateByUID(t, "INBOX", 1)

	res, err := e.exec.ApplyMove(context.Background(), e.account.ID, st.MessageID, st.MailboxID)
	if err != nil {
		t.Fatalf("ApplyMove: %v", err)
	}
	if res.MailboxID != st.MailboxID || res.UID != st.UID {
		t.Errorf("no-op move changed identity: %+v", res)
	}
}

func TestApplyMoveRefusesForeignTarget(t *testing.T) {
	e := newWriteEnv(t, 2)
	st := e.stateByUID(t, "INBOX", 1)

	if _, err := e.exec.ApplyMove(context.Background(), e.account.ID, st.MessageID, 999999); !errors.Is(err, ErrWriteNotFound) {
		t.Errorf("err = %v, want ErrWriteNotFound for an unknown target mailbox", err)
	}
}

// ---------------------------------------------------------------------------
// destroy (W-A2)
// ---------------------------------------------------------------------------

func TestApplyDestroyOutsideTrashMovesToTrash(t *testing.T) {
	e := newWriteEnv(t, 3)
	st := e.stateByUID(t, "INBOX", 2)
	trash, err := e.store.GetMailboxByName(context.Background(), e.account.ID, "Trash")
	if err != nil {
		t.Fatal(err)
	}

	res, err := e.exec.ApplyDestroy(context.Background(), e.account.ID, st.MessageID)
	if err != nil {
		t.Fatalf("ApplyDestroy: %v", err)
	}
	if res.Expunged {
		t.Error("destroy outside Trash reported an expunge; W-A2 makes it a move")
	}

	// Reversible: the message EXISTS, in Trash, on both sides.
	if m := e.serverMessage("Trash", imap.UID(res.UID)); m == nil {
		t.Fatal("the message is not in the server's Trash")
	}
	after := e.state(t, st.MessageID)
	if after.MailboxID != trash.ID || after.DeletedAt != nil {
		t.Errorf("store row = %+v, want alive in Trash", after)
	}
}

func TestApplyDestroyInsideTrashExpunges(t *testing.T) {
	e := newWriteEnv(t, 2)
	st := e.stateByUID(t, "Trash", 1)

	res, err := e.exec.ApplyDestroy(context.Background(), e.account.ID, st.MessageID)
	if err != nil {
		t.Fatalf("ApplyDestroy: %v", err)
	}
	if !res.Expunged {
		t.Error("destroy inside Trash must be the final expunge (W-A2)")
	}
	if m := e.serverMessage("Trash", 1); m != nil {
		t.Error("the message survived the expunge on the server")
	}
	after := e.state(t, st.MessageID)
	if after.DeletedAt == nil {
		t.Error("the store row was not tombstoned")
	}

	// The echo (VANISHED for an already-tombstoned row) converges.
	e.pass(t, "Trash")
	if again := e.state(t, st.MessageID); !again.UpdatedAt.Equal(after.UpdatedAt) {
		t.Error("the expunge echo re-touched the tombstone")
	}

	// And the replay: destroying a destroyed message is notFound, per §5.3.
	if _, err := e.exec.ApplyDestroy(context.Background(), e.account.ID, st.MessageID); !errors.Is(err, ErrWriteNotFound) {
		t.Errorf("replayed destroy err = %v, want ErrWriteNotFound", err)
	}
}

func TestApplyDestroyStoreUntouchedWhenExpungeFails(t *testing.T) {
	e := newWriteEnv(t, 2)
	st := e.stateByUID(t, "Trash", 1)

	e.srv.expungeErr = errors.New("injected: expunge refused")
	if _, err := e.exec.ApplyDestroy(context.Background(), e.account.ID, st.MessageID); err == nil {
		t.Fatal("ApplyDestroy succeeded with a failing server")
	}
	after := e.state(t, st.MessageID)
	if after.DeletedAt != nil {
		t.Error("the store tombstoned a message the server still holds")
	}
}

func TestApplyDestroyWithoutTrashMailbox(t *testing.T) {
	env := newTestEnv(t)
	env.mustSyncableAccount(t)

	srv := newFakeServer()
	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, 1, referenceNow, "Inbox")

	opts := env.testOptions(referenceNow)
	syncer := env.syncer(t, srv, opts)
	if _, err := syncer.Run(context.Background(), env.account); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	exec, err := NewWriteExecutor(env.store, ConnectorFunc(
		func(_ context.Context, _ store.Account, n int) ([]imap.Client, error) {
			return srv.clients(n), nil
		}), WriteOptions{Logger: env.logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(exec.Close)

	ctx := context.Background()
	row, err := env.store.GetMailboxByName(ctx, env.account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	st, err := env.store.GetMessageStateByUID(ctx, row.ID, row.UIDValidityOrZero(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.ApplyDestroy(ctx, env.account.ID, st.MessageID); !errors.Is(err, ErrNoTrashMailbox) {
		t.Errorf("err = %v, want ErrNoTrashMailbox", err)
	}
}

// TestWriteExecutorConnectionDiesAndSelfHeals proves the dead-idle-connection
// recovery: the first SELECT after a closed connection fails, the executor
// redials once, and the write proceeds — without the write command itself
// ever being retried.
func TestWriteExecutorConnectionDiesAndSelfHeals(t *testing.T) {
	e := newWriteEnv(t, 2)
	st := e.stateByUID(t, "INBOX", 1)
	ctx := context.Background()

	// First write establishes the cached connection.
	if _, err := e.exec.ApplyFlagChange(ctx, e.account.ID, st.MessageID,
		FlagChange{Add: []string{"flagged"}}); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Kill it behind the executor's back: the next SELECT on this client
	// answers ErrNotConnected, exactly like a socket the server timed out.
	ac, err := e.exec.forAccount(e.account.ID)
	if err != nil {
		t.Fatal(err)
	}
	ac.mu.Lock()
	if ac.client == nil {
		ac.mu.Unlock()
		t.Fatal("no cached connection after a write")
	}
	_ = ac.client.Close()
	ac.mu.Unlock()

	// The executor's SELECT probe fails, discards, redials once, succeeds.
	if _, err := e.exec.ApplyFlagChange(ctx, e.account.ID, st.MessageID,
		FlagChange{Remove: []string{"flagged"}}); err != nil {
		t.Fatalf("write after connection death: %v", err)
	}
	msg := e.serverMessage("INBOX", 1)
	if containsFold(msg.flags, "flagged") {
		t.Errorf("server flags = %v, want flagged removed by the post-redial write", msg.flags)
	}
}
