package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// Incremental sync: the delta must be exactly right.
//
// Every test here follows the same shape — sync an account, change the server
// while the engine is "offline", run one incremental pass, assert the store now
// matches the server — because that is the acceptance criterion in its most
// direct form. What varies is WHICH change is made, and the three that matter
// are flags, expunges and arrivals, alone and together.

// syncedEnv is an account whose initial sync has run against srv, ready for an
// incremental pass.
type syncedEnv struct {
	*testEnv
	srv    *fakeServer
	syncer *Syncer
	opts   Options
}

// newSyncedEnv builds a store-backed account, seeds a mailbox and runs the
// initial sync, which is the precondition of every incremental test.
func newSyncedEnv(t *testing.T, messages int) *syncedEnv {
	t.Helper()

	env := newTestEnv(t)
	env.mustSyncableAccount(t)

	srv := newFakeServer()
	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, messages, referenceNow, "Inbox")

	opts := env.testOptions(referenceNow)
	syncer := env.syncer(t, srv, opts)
	if _, err := syncer.Run(context.Background(), env.account); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	return &syncedEnv{testEnv: env, srv: srv, syncer: syncer, opts: opts}
}

// pass runs one incremental pass over a mailbox and returns its result.
func (e *syncedEnv) pass(t *testing.T, mailbox string) IncrementalResult {
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

// flagsOf reads a stored message's flags by UID.
func (e *syncedEnv) flagsOf(t *testing.T, mailbox string, uid int64) (store.Flags, bool) {
	t.Helper()
	ctx := context.Background()

	row, err := e.store.GetMailboxByName(ctx, e.account.ID, mailbox)
	if err != nil {
		t.Fatalf("GetMailboxByName: %v", err)
	}
	st, err := e.store.GetMessageStateByUID(ctx, row.ID, row.UIDValidityOrZero(), uid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, false
		}
		t.Fatalf("GetMessageStateByUID: %v", err)
	}
	return st.Flags, true
}

// liveUIDs returns the UIDs of a mailbox that are stored and not tombstoned,
// which is what a user would see.
func (e *syncedEnv) liveUIDs(t *testing.T, mailbox string) []int64 {
	t.Helper()

	rows, err := e.store.Pool().Query(context.Background(), `
		SELECT ms.uid
		  FROM message_state ms
		  JOIN mailboxes mb ON mb.id = ms.mailbox_id
		 WHERE ms.account_id = $1 AND mb.name = $2 AND ms.deleted_at IS NULL
		 ORDER BY ms.uid`, e.account.ID, mailbox)
	if err != nil {
		t.Fatalf("querying live uids: %v", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			t.Fatalf("scanning uid: %v", err)
		}
		out = append(out, uid)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading live uids: %v", err)
	}
	return out
}

// TestIncrementalAppliesFlagsExpungeAndNewTogether is the E6 acceptance
// criterion in one test: everything changes while the engine is offline, and
// one pass afterwards has to reconcile all of it.
//
// The three changes are made together on purpose. Handled separately each is
// easy; handled together they interact — a UID that is both new and flagged
// must not be stored twice, and a UID that is both flagged and expunged must
// end up deleted rather than updated.
func TestIncrementalAppliesFlagsExpungeAndNewTogether(t *testing.T) {
	env := newSyncedEnv(t, 10)

	before := env.liveUIDs(t, "INBOX")
	if len(before) != 10 {
		t.Fatalf("initial sync stored %d messages, want 10", len(before))
	}
	if flags, ok := env.flagsOf(t, "INBOX", 2); !ok || flags.Has(store.FlagSeen) {
		t.Fatalf("uid 2 starts \\Seen=%v (present=%v), want unseen", flags, ok)
	}

	// --- the account is "offline" and the server changes underneath it -------

	// A flag change another client made.
	env.srv.setFlags("INBOX", 2, []string{"seen", "flagged"}, nil)
	// A message someone deleted.
	env.srv.expunge("INBOX", 5)
	// A message that arrived.
	newUID := env.srv.deliver("INBOX",
		buildMessage(900, "Arrived while offline", referenceNow, "New body."),
		nil, referenceNow)
	// And a message that both arrived AND was flagged before the engine looked,
	// which is the interleaving that breaks a naive implementation.
	flaggedNewUID := env.srv.deliver("INBOX",
		buildMessage(901, "Arrived and flagged", referenceNow, "Another new body."),
		nil, referenceNow)
	env.srv.setFlags("INBOX", flaggedNewUID, []string{"seen"}, nil)

	// --- one pass has to fix all of it --------------------------------------

	res := env.pass(t, "INBOX")

	if res.New != 2 {
		t.Errorf("pass stored %d new messages, want 2", res.New)
	}
	if res.Vanished != 1 {
		t.Errorf("pass tombstoned %d messages, want 1", res.Vanished)
	}
	if res.FlagsUpdated != 1 {
		t.Errorf("pass updated %d flag sets, want 1 (uid 2; the new ones arrive with their flags)",
			res.FlagsUpdated)
	}

	// The flag change landed.
	flags, ok := env.flagsOf(t, "INBOX", 2)
	if !ok {
		t.Fatal("uid 2 disappeared")
	}
	if !flags.Has(store.FlagSeen) || !flags.Has(store.FlagFlagged) {
		t.Errorf("uid 2 has flags %v, want \\Seen and \\Flagged", flags)
	}

	// The expunge landed: gone from the live set, still present as a tombstone
	// so JMAP Email/changes can report it destroyed.
	live := env.liveUIDs(t, "INBOX")
	for _, uid := range live {
		if uid == 5 {
			t.Error("uid 5 was expunged on the server but is still live locally")
		}
	}
	if _, present := env.flagsOf(t, "INBOX", 5); !present {
		t.Error("uid 5's tombstone was deleted; Email/changes cannot report it destroyed")
	}

	// The arrivals landed, and the one that was flagged carries its flags.
	var sawNew, sawFlaggedNew bool
	for _, uid := range live {
		switch imap.UID(uid) {
		case newUID:
			sawNew = true
		case flaggedNewUID:
			sawFlaggedNew = true
		}
	}
	if !sawNew {
		t.Errorf("the delivered message (uid %d) is not stored", newUID)
	}
	if !sawFlaggedNew {
		t.Errorf("the delivered-and-flagged message (uid %d) is not stored", flaggedNewUID)
	}
	if f, ok := env.flagsOf(t, "INBOX", int64(flaggedNewUID)); ok && !f.Has(store.FlagSeen) {
		t.Errorf("the delivered-and-flagged message has flags %v, want \\Seen", f)
	}

	// The live set is exactly what the server holds: 10 - 1 expunged + 2 new.
	if len(live) != 11 {
		t.Errorf("the mailbox holds %d live messages, want 11: %v", len(live), live)
	}
}

// TestIncrementalIsIdempotent proves a second pass over an unchanged mailbox
// does nothing at all.
//
// It matters more than it looks. The watcher runs a pass per event and the
// reconciler runs one per sweep, so passes over unchanged mailboxes are the
// COMMON case, not the exception. A pass that re-fetched, or that bumped
// updated_at, would turn every idle account into constant write load and would
// make every JMAP client re-download a mailbox that did not change.
func TestIncrementalIsIdempotent(t *testing.T) {
	env := newSyncedEnv(t, 8)

	env.srv.mu.Lock()
	fetchesBefore := env.srv.fetchCount
	env.srv.mu.Unlock()

	first := env.pass(t, "INBOX")
	if first.Changed() {
		t.Errorf("a pass over an unchanged mailbox reported changes: %+v", first)
	}

	second := env.pass(t, "INBOX")
	if second.Changed() {
		t.Errorf("the second pass reported changes: %+v", second)
	}

	env.srv.mu.Lock()
	extra := env.srv.fetchCount - fetchesBefore
	env.srv.mu.Unlock()
	if extra != 0 {
		t.Errorf("passes over an unchanged mailbox downloaded %d bodies, want 0", extra)
	}
}

// TestIncrementalSkipsNoOpFlagUpdates checks that a flag change which changes
// nothing does not touch the row.
//
// updated_at is the cursor JMAP Email/changes pages through, so a write that
// moves it without changing anything makes every connected client re-fetch a
// message for no reason. The server legitimately reports a message as "changed"
// when another client set the flags it already had.
func TestIncrementalSkipsNoOpFlagUpdates(t *testing.T) {
	env := newSyncedEnv(t, 6)

	// Seeded uid 1 is \Seen (flagsForIndex(0)). Setting it again is a server
	// change — the modseq moves — that must not become a local write.
	env.srv.setFlags("INBOX", 1, []string{"seen"}, nil)

	res := env.pass(t, "INBOX")
	if res.FlagsUpdated != 0 {
		t.Errorf("a no-op flag change produced %d updates, want 0", res.FlagsUpdated)
	}
}

// TestIncrementalDetectsKeywordChanges checks the A6 half of the flag path:
// keywords carry labels, so a keyword that changes must be applied even when
// the system flags do not move.
func TestIncrementalDetectsKeywordChanges(t *testing.T) {
	env := newSyncedEnv(t, 4)

	env.srv.setFlags("INBOX", 3, []string{"seen"}, []string{"$MoovL1"})

	res := env.pass(t, "INBOX")
	if res.FlagsUpdated != 1 {
		t.Fatalf("a keyword change produced %d updates, want 1", res.FlagsUpdated)
	}

	ctx := context.Background()
	row, err := env.store.GetMailboxByName(ctx, env.account.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName: %v", err)
	}
	st, err := env.store.GetMessageStateByUID(ctx, row.ID, row.UIDValidityOrZero(), 3)
	if err != nil {
		t.Fatalf("GetMessageStateByUID: %v", err)
	}
	if len(st.Keywords) != 1 || st.Keywords[0] != "$MoovL1" {
		t.Errorf("keywords = %v, want [$MoovL1]", st.Keywords)
	}
}

// TestIncrementalRefusesAMailboxWithoutACursor documents the boundary between
// E5 and E6: a mailbox that has never been selected has no delta to ask for,
// and the incremental path must say so rather than issuing a QRESYNC SELECT
// with a zero cursor — which a server answers with the entire mailbox as if
// everything had just changed.
func TestIncrementalRefusesAMailboxWithoutACursor(t *testing.T) {
	env := newTestEnv(t)
	env.mustSyncableAccount(t)

	srv := newFakeServer()
	srv.addMailbox("INBOX", imap.RoleInbox, 100)

	opts := env.testOptions(referenceNow)
	syncer := env.syncer(t, srv, opts)

	row, err := env.store.UpsertMailbox(context.Background(), store.Mailbox{
		AccountID: env.account.ID, Name: "INBOX", Selectable: true,
	})
	if err != nil {
		t.Fatalf("UpsertMailbox: %v", err)
	}

	mb := syncMailbox{row: row, info: imap.MailboxInfo{Name: "INBOX"}}
	err = syncer.conns.withConn(context.Background(), func(c imap.Client) error {
		_, perr := syncer.incrementalMailbox(context.Background(), c, env.account, mb, env.logger)
		return perr
	})
	if !errors.Is(err, errMailboxNeedsInitialSync) {
		t.Fatalf("incrementalMailbox = %v, want errMailboxNeedsInitialSync", err)
	}
}

// TestIncrementalResyncsOnUIDValidityChange is the correctness case that
// produces visibly WRONG mail rather than merely missing mail if it is got
// wrong: the same UID numbers now name different messages.
func TestIncrementalResyncsOnUIDValidityChange(t *testing.T) {
	env := newSyncedEnv(t, 6)

	// The mailbox is recreated on the server: new UIDVALIDITY, different
	// content under the same UID numbers.
	env.srv.mu.Lock()
	mb := env.srv.mailbox("INBOX")
	mb.uidValidity = 999
	mb.messages = nil
	mb.vanished = nil
	mb.highestModSeq = 0
	env.srv.mu.Unlock()
	seedMailboxLocked(env.srv, "INBOX", 3, referenceNow, "Recreated")

	res := env.pass(t, "INBOX")
	if !res.Resynced {
		t.Fatal("a changed UIDVALIDITY did not trigger a resync")
	}

	live := env.liveUIDs(t, "INBOX")
	if len(live) != 3 {
		t.Errorf("after the resync the mailbox holds %d messages, want 3: %v", len(live), live)
	}

	// The stored subjects must be the NEW mailbox's, not the old one's. This is
	// the assertion that would catch a resync that kept stale rows under
	// recycled UIDs.
	var subjects int
	err := env.store.Pool().QueryRow(context.Background(), `
		SELECT count(*) FROM messages
		 WHERE account_id = $1 AND subject LIKE 'Recreated%'`, env.account.ID).Scan(&subjects)
	if err != nil {
		t.Fatalf("counting subjects: %v", err)
	}
	if subjects != 3 {
		t.Errorf("%d messages carry the recreated mailbox's subjects, want 3", subjects)
	}
}

// TestIncrementalAdvancesTheCursorOnlyAfterCommitting is the ordering rule the
// whole incremental path rests on.
//
// A cursor advanced before the delta is committed would, on a crash in between,
// permanently skip the changes it claimed — and unlike a missed backfill
// window, nothing ever revisits them. The test forces a failure in the middle
// of the pass and asserts the cursor did not move.
func TestIncrementalAdvancesTheCursorOnlyAfterCommitting(t *testing.T) {
	env := newSyncedEnv(t, 5)

	ctx := context.Background()
	before, err := env.store.GetMailboxByName(ctx, env.account.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName: %v", err)
	}
	cursorBefore := *before.HighestModSeq

	// A new message arrives, and the fetch of it will fail.
	env.srv.deliver("INBOX", buildMessage(950, "Will not be fetched", referenceNow, "Body."), nil, referenceNow)
	env.srv.mu.Lock()
	env.srv.failAfterFetches = env.srv.fetchCount // the very next body fetch fails
	env.srv.mu.Unlock()

	mb := syncMailbox{row: before, info: imap.MailboxInfo{Name: "INBOX"}}
	err = env.syncer.conns.withConn(ctx, func(c imap.Client) error {
		_, perr := env.syncer.incrementalMailbox(ctx, c, env.account, mb, env.logger)
		return perr
	})
	if err == nil {
		t.Fatal("the pass succeeded despite an injected fetch failure")
	}

	after, err := env.store.GetMailboxByName(ctx, env.account.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName: %v", err)
	}
	if *after.HighestModSeq != cursorBefore {
		t.Errorf("the cursor moved to %d despite the failure (was %d); the delta would be skipped forever",
			*after.HighestModSeq, cursorBefore)
	}

	// And once the failure is lifted, the same delta is applied — proving the
	// change was deferred rather than lost.
	env.srv.mu.Lock()
	env.srv.failAfterFetches = 0
	env.srv.mu.Unlock()

	res := env.pass(t, "INBOX")
	if res.New != 1 {
		t.Errorf("the retried pass stored %d new messages, want 1", res.New)
	}
}

// TestSameKeywords covers the set comparison directly, including the ordering
// case that would otherwise produce a write on every pass.
func TestSameKeywords(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"both empty", nil, nil, true},
		{"empty and empty slice", nil, []string{}, true},
		{"identical", []string{"a", "b"}, []string{"a", "b"}, true},
		{"reordered is still the same set", []string{"a", "b"}, []string{"b", "a"}, true},
		{"different length", []string{"a"}, []string{"a", "b"}, false},
		{"different content", []string{"a"}, []string{"b"}, false},
		{"duplicates matter", []string{"a", "a"}, []string{"a", "b"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameKeywords(tc.a, tc.b); got != tc.want {
				t.Errorf("sameKeywords(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestDedupeUIDs covers the VANISHED de-duplication, which exists because the
// UIDs reach this package by two routes at once.
func TestDedupeUIDs(t *testing.T) {
	got := dedupeUIDs([]imap.UID{3, 1, 3, 2, 1})
	want := []imap.UID{3, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("dedupeUIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupeUIDs = %v, want %v (order must be preserved)", got, want)
		}
	}
}

// seedMailboxLocked seeds a mailbox that already exists on the server.
func seedMailboxLocked(srv *fakeServer, name string, n int, newest time.Time, prefix string) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	seedMailbox(srv.mailbox(name), n, newest, prefix)
}
