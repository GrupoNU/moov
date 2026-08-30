package sync

import (
	"context"
	"testing"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// The write connection's selection cache (L3 epic E1, the W4b latency debt).
//
// # The debt these tests pay
//
// W4b measured destroying a folder at 1.8-6.1 s and recorded it honestly as
// over the Gmail-class bar. The documented suspicion was the IMAP DELETE itself
// — that it must release the selected mailbox and races the watcher's
// reconciler — and that turned out to be only half true: internal/imap's
// DeleteMailbox already UNSELECTs before DELETE (folder.go), so that part was
// never the cost.
//
// The cost was one layer up. withMailbox used to SELECT unconditionally on every
// call — including when the connection already had that exact mailbox open — so
// any RUN of writes against one folder paid a full round trip per message for a
// selection it already had.
//
// These tests pin the fix as a ROUND-TRIP property, which is the only way it can
// be pinned: the results are byte-identical with and without the cache, and only
// the latency differs. A behavioral test cannot see the difference; a counter
// can.
//
// # What was and was not fixed
//
// A run of same-folder writes — flags, archiving, labeling, the operations a
// user actually repeats — went from N SELECTs to 1.
//
// A folder DELETE did NOT reach 1, and TestEmptyingAFolderCostsTwoSelectsPerMove
// carries the honest number and the reason: Mailbox/set empties a folder by
// MOVING each message, and a MOVE genuinely has to select the DESTINATION
// afterwards to read the message's new modseq, which moves the selection off the
// source. The steady state is 2 per move rather than 1, and the remaining
// halving needs a batched MOVE in mailbox_set.go — named there, not attempted
// here.

// selectCountingEnv is writeEnv plus a handle on the clients the connector
// handed out, so a test can read how many SELECTs actually crossed the wire.
type selectCountingEnv struct {
	*writeEnv
	handed []*fakeClient
}

func newSelectCountingEnv(t *testing.T, inboxMessages int) *selectCountingEnv {
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

	out := &selectCountingEnv{}
	exec, err := NewWriteExecutor(env.store, ConnectorFunc(
		func(_ context.Context, _ store.Account, n int) ([]imap.Client, error) {
			clients := srv.clients(n)
			for _, c := range clients {
				if fc, ok := c.(*fakeClient); ok {
					out.handed = append(out.handed, fc)
				}
			}
			return clients, nil
		}), WriteOptions{Logger: env.logger})
	if err != nil {
		t.Fatalf("NewWriteExecutor: %v", err)
	}
	t.Cleanup(exec.Close)

	out.writeEnv = &writeEnv{testEnv: env, srv: srv, syncer: syncer, exec: exec}
	return out
}

// selects totals the SELECT commands every handed-out write connection issued.
func (e *selectCountingEnv) selects() int {
	e.srv.mu.Lock()
	defer e.srv.mu.Unlock()
	n := 0
	for _, c := range e.handed {
		n += c.selectCount
	}
	return n
}

// TestConsecutiveWritesToOneMailboxSelectOnce is the measurement the fix exists
// for, expressed as an invariant.
//
// Ten flag writes against one folder used to cost ten SELECTs; they now cost
// one. That ratio is what turns a folder-delete's 2N round trips into N+1, and
// the assertion is deliberately exact rather than "fewer than before" — a cache
// that re-selected every other call would still be an improvement and would
// still be a bug.
func TestConsecutiveWritesToOneMailboxSelectOnce(t *testing.T) {
	env := newSelectCountingEnv(t, 10)
	ctx := context.Background()

	before := env.selects()
	for uid := int64(1); uid <= 10; uid++ {
		st := env.stateByUID(t, "INBOX", uid)
		if _, err := env.exec.ApplyFlagChange(ctx, env.account.ID, st.MessageID,
			FlagChange{Add: []string{"$pinned"}}); err != nil {
			t.Fatalf("ApplyFlagChange(uid %d): %v", uid, err)
		}
	}
	got := env.selects() - before

	if got != 1 {
		t.Errorf("ten writes to one mailbox issued %d SELECTs, want exactly 1.\n"+
			"Each redundant SELECT is a full round trip, and this is the cost that put "+
			"folder-delete at 1.8-6.1 s in W4b (mailbox_set.go empties a folder one message "+
			"at a time).", got)
	}
}

// TestSwitchingMailboxesReselects is the other half of the bargain: the cache
// must not be a source of wrong answers.
//
// A write to a DIFFERENT folder has to re-SELECT, or it would issue its command
// against whatever the connection last had open — which for a MOVE means moving
// somebody else's message.
func TestSwitchingMailboxesReselects(t *testing.T) {
	env := newSelectCountingEnv(t, 4)
	ctx := context.Background()

	inboxState := env.stateByUID(t, "INBOX", 1)
	trashState := env.stateByUID(t, "Trash", 1)

	before := env.selects()
	// INBOX, then Trash, then INBOX again: three selections of two folders.
	for _, st := range []store.MessageState{inboxState, trashState, inboxState} {
		if _, err := env.exec.ApplyFlagChange(ctx, env.account.ID, st.MessageID,
			FlagChange{Add: []string{"$touched"}}); err != nil {
			t.Fatalf("ApplyFlagChange: %v", err)
		}
	}
	got := env.selects() - before

	if got != 3 {
		t.Errorf("alternating between two mailboxes issued %d SELECTs, want 3 — "+
			"a cached selection must be dropped the moment the target folder changes, or a "+
			"write lands in the wrong folder", got)
	}
}

// TestFolderCommandsInvalidateTheSelection closes the one failure mode a
// selection cache can introduce.
//
// CREATE, RENAME and DELETE all change what a mailbox NAME refers to, and DELETE
// unselects on the server besides. A cache that survived them would let the next
// write skip a SELECT it genuinely needs — issuing a command against a mailbox
// this connection no longer has open, which the server answers with an error at
// best and the wrong folder at worst.
func TestFolderCommandsInvalidateTheSelection(t *testing.T) {
	env := newSelectCountingEnv(t, 4)
	ctx := context.Background()

	// Select INBOX by writing to it.
	st := env.stateByUID(t, "INBOX", 1)
	if _, err := env.exec.ApplyFlagChange(ctx, env.account.ID, st.MessageID,
		FlagChange{Add: []string{"$first"}}); err != nil {
		t.Fatalf("ApplyFlagChange: %v", err)
	}

	// A folder command on the same connection.
	if _, err := env.exec.ApplyMailboxCreate(ctx, env.account.ID, "Proyectos", true); err != nil {
		t.Fatalf("ApplyMailboxCreate: %v", err)
	}

	// The next write to INBOX must SELECT again rather than trust a selection
	// the folder command invalidated.
	before := env.selects()
	if _, err := env.exec.ApplyFlagChange(ctx, env.account.ID, st.MessageID,
		FlagChange{Add: []string{"$second"}}); err != nil {
		t.Fatalf("ApplyFlagChange after the folder command: %v", err)
	}
	if got := env.selects() - before; got != 1 {
		t.Errorf("the write after a folder command issued %d SELECTs, want 1 — "+
			"a folder command changes what a mailbox name means, so any remembered "+
			"selection is void", got)
	}
}

// TestEmptyingAFolderCostsTwoSelectsPerMove is the debt measured end to end
// through the path that actually pays it — AND the honest record of how much of
// it the selection cache could not remove.
//
// # What the cache does and does not buy here
//
// Mailbox/set's destroy moves every message of the doomed folder to Trash and
// then deletes it (mailbox_set.go emptyMailboxToTrash). Each move needs the
// SOURCE selected to issue the MOVE — which the cache now serves for free after
// the first — and then genuinely has to SELECT the DESTINATION, because a MOVE
// assigns the message a new modseq in the target mailbox and the row's
// modseq_seen is wrong without it (a stale modseq_seen is a false conflict on
// every later conditional write, which is a user's click bouncing for no
// reason).
//
// That destination SELECT leaves the connection off the source, so the next
// move re-selects it. The steady state is therefore TWO selects per move, not
// one — the cache removed the redundant source SELECT of the FIRST move only.
//
// # Why this is not the fix it looks like it should be
//
// The remaining round trip is not waste; it buys a correct modseq. Removing it
// would need one of:
//
//   - a second connection dedicated to the destination, so neither side loses
//     its selection (real, and it doubles the account's write sockets);
//   - a batched MOVE of the whole UID set in one command, which IMAP supports
//     and which would make the whole folder-empty ONE move and TWO selects —
//     the actual fix, and one that belongs to mailbox_set.go's emptying loop
//     rather than to the connection layer, because it changes the JMAP layer's
//     per-message reflection contract (W-A2 reuses Email/set's destroy exactly
//     so the two semantics are provably identical, and batching breaks that
//     reuse).
//
// Neither is in E1's scope. What E1 achieved is recorded by the assertion below
// and by TestConsecutiveWritesToOneMailboxSelectOnce: a run of same-folder
// writes is now 1 SELECT instead of N, which is the whole win for flags,
// archiving and labeling — the operations a user actually repeats. Folder
// delete keeps a 2N floor until the batched MOVE lands.
func TestEmptyingAFolderCostsTwoSelectsPerMove(t *testing.T) {
	const messages = 12
	env := newSelectCountingEnv(t, messages)
	ctx := context.Background()

	before := env.selects()

	// The move-everything-out loop, exactly as emptyMailboxToTrash drives it:
	// one Destroy per message, in sequence.
	for uid := int64(1); uid <= messages; uid++ {
		st := env.stateByUID(t, "INBOX", uid)
		if _, err := env.exec.ApplyDestroy(ctx, env.account.ID, st.MessageID); err != nil {
			t.Fatalf("ApplyDestroy(uid %d): %v", uid, err)
		}
	}
	got := env.selects() - before

	// Two per move: the source (re-selected because the previous move's
	// destination SELECT moved off it) and the destination.
	want := messages * 2
	if got != want {
		t.Errorf("emptying a %d-message folder issued %d SELECTs, want %d.\n"+
			"More than that means the source SELECT is no longer being served from cache; "+
			"fewer means the destination SELECT that keeps modseq_seen truthful was dropped, "+
			"which turns every later conditional write into a false conflict.", messages, got, want)
	}
}
