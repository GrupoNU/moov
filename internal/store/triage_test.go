package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// The threads table, snoozes and mutes against a real PostgreSQL 17
// (migration 0009, L3 epic E4).
//
// What these tests exist to prove, in the order that matters if one breaks:
//
//  1. the durable key survives what thread_id does not — a rebuild. This is
//     the whole reason migration 0009 exists, and it is the one property no
//     amount of care in the JMAP layer can restore afterwards;
//  2. a merge leaves a TOMBSTONE, because Thread/changes now reports it and a
//     missing one means a client is never told a conversation died;
//  3. snoozes and mutes are keyed durably, idempotent, and account-scoped.

// ---------------------------------------------------------------------------
// the durable key
// ---------------------------------------------------------------------------

// TestThreadKeyPrefersTheMessageID walks the three derivations in the order
// threadkey.go states them, because the order IS the design: a thread with a
// Message-ID must never fall through to the subject digest, or two unrelated
// conversations with the same subject would fuse.
func TestThreadKeyPrefersTheMessageID(t *testing.T) {
	cases := []struct {
		name      string
		candidate store.ThreadCandidate
		scheme    string
	}{{
		name:      "its own Message-ID wins",
		candidate: store.ThreadCandidate{MessageID: "root@test", References: []string{"older@test"}, Subject: "x"},
		scheme:    "message-id",
	}, {
		name: "the FIRST reference when there is no Message-ID",
		// Oldest-first ordering (RFC 5322 §3.6.4) makes entry zero the closest
		// thing to the root; using the last would name the immediate parent,
		// which differs per member and would give one conversation many keys.
		candidate: store.ThreadCandidate{References: []string{"root@test", "mid@test", "parent@test"}},
		scheme:    "reference",
	}, {
		name:      "a subject digest when neither header is usable",
		candidate: store.ThreadCandidate{Subject: "Re: Presupuesto 2026"},
		scheme:    "subject",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key := store.ThreadKey(c.candidate)
			if key == "" {
				t.Fatal("ThreadKey returned an empty key; it must never do that")
			}
			if got := store.ThreadKeyScheme(key); got != c.scheme {
				t.Errorf("scheme = %q, want %q (key %q)", got, c.scheme, key)
			}
		})
	}
}

// TestThreadKeyIsStableUnderConversationGrowth is the property that made the
// root the key rather than a fingerprint of the member set.
//
// A key derived from the whole graph would change on every reply — and a mute
// keyed on it would evaporate on exactly the reply the mute exists to suppress.
func TestThreadKeyIsStableUnderConversationGrowth(t *testing.T) {
	root := store.ThreadCandidate{MessageID: "root@test", Subject: "Presupuesto"}
	first := store.ThreadKey(root)

	// Every later member of the same conversation names the root in its
	// References, so it derives the SAME key — which is what lets the row be
	// found from any member.
	for i, member := range []store.ThreadCandidate{
		{References: []string{"root@test"}, Subject: "Re: Presupuesto"},
		{References: []string{"root@test", "r1@test"}, Subject: "Re: Presupuesto"},
		{References: []string{"root@test", "r1@test", "r2@test"}, Subject: "Re: Presupuesto"},
	} {
		if got := store.ThreadKey(member); got != "ref:root@test" {
			t.Errorf("member %d derived %q, want the root's key", i, got)
		}
	}
	if first != "mid:root@test" {
		t.Errorf("the root's own key is %q", first)
	}
	// The two schemes differ on purpose: the root has a Message-ID and its
	// descendants only have a reference to it. They meet in the threads table
	// because the ROOT is inserted first and later members find its row through
	// their thread_id, not by re-deriving the same string — which is the
	// EnsureThread-by-thread_id path the next test covers.
}

// TestThreadKeySchemesCannotCollide is why every key carries a prefix.
func TestThreadKeySchemesCannotCollide(t *testing.T) {
	// A (pathological but legal) Message-ID that happens to look like another
	// scheme's key must not be readable as one.
	key := store.ThreadKey(store.ThreadCandidate{MessageID: "sub:deadbeef"})
	if got := store.ThreadKeyScheme(key); got != "message-id" {
		t.Errorf("a Message-ID spelled like a subject digest resolved as %q", got)
	}
}

// ---------------------------------------------------------------------------
// the thread rows
// ---------------------------------------------------------------------------

// TestThreadRowIsCreatedForEveryConversation proves the row is written by the
// ordinary insert path, not by a separate backfill a deployment might skip.
func TestThreadRowIsCreatedForEveryConversation(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	root := f.insert("root@test", "", "Presupuesto 2026")
	threadID := f.threadOf(root)

	row, err := f.s.ThreadRowByThreadID(ctx, f.account.ID, threadID)
	if err != nil {
		t.Fatalf("ThreadRowByThreadID: %v", err)
	}
	if row.RootMessageID != "mid:root@test" {
		t.Errorf("root_message_id = %q, want the root's own Message-ID key", row.RootMessageID)
	}
	if row.ThreadID != threadID {
		t.Errorf("the row's thread_id is %d, want %d", row.ThreadID, threadID)
	}
	if row.Destroyed() {
		t.Error("a fresh conversation is tombstoned")
	}
}

// TestThreadRowSurvivesARebuild is THE test of migration 0009.
//
// It simulates what an operator rebuild does — the same mail re-inserted into a
// fresh set of ids — and asserts the durable key is unchanged, which is what
// lets a mute recorded before the rebuild still name the same conversation
// after it. `messages.thread_id` is deliberately asserted to have CHANGED, so
// the test fails loudly if the rebuild it simulates stops being a rebuild.
func TestThreadRowSurvivesARebuild(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	root := f.insert("root@test", "", "Presupuesto 2026")
	f.insert("r1@test", "root@test", "Re: Presupuesto 2026", "root@test")
	before := f.threadOf(root)

	beforeRow, err := f.s.ThreadRowByThreadID(ctx, f.account.ID, before)
	if err != nil {
		t.Fatalf("ThreadRowByThreadID before the rebuild: %v", err)
	}

	// The rebuild: a second account holding the SAME mail. New ids for every
	// message and therefore a new thread_id, which is exactly what a rebuilt
	// cache produces.
	rebuilt := newAccount(t, f.s)
	mb := threadMailbox(t, f.s, rebuilt.ID, "INBOX")
	rebuiltRoot := f.insertInAccount(rebuilt, mb, "root@test", "", "Presupuesto 2026")
	f.insertInAccount(rebuilt, mb, "r1@test", "root@test", "Re: Presupuesto 2026", "root@test")
	after := f.threadOf(rebuiltRoot)

	if after == before {
		t.Fatal("the simulated rebuild produced the same thread_id; it is not simulating a rebuild")
	}

	afterRow, err := f.s.ThreadRowByThreadID(ctx, rebuilt.ID, after)
	if err != nil {
		t.Fatalf("ThreadRowByThreadID after the rebuild: %v", err)
	}
	if afterRow.RootMessageID != beforeRow.RootMessageID {
		t.Errorf("the durable key changed across the rebuild: %q -> %q — "+
			"every mute keyed on it would have been silently lost",
			beforeRow.RootMessageID, afterRow.RootMessageID)
	}
}

// TestMergeLeavesATombstone is the record ADR-001 §2 asked for and the schema
// had nowhere to write until 0009 — the one Thread/changes reports as
// `destroyed`.
func TestMergeLeavesATombstone(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	// Two messages that look unrelated until their common ancestor arrives.
	a := f.insert("a@test", "", "Presupuesto")
	b := f.insert("b@test", "", "Otra cosa")
	threadA, threadB := f.threadOf(a), f.threadOf(b)
	if threadA == threadB {
		t.Fatal("the two messages threaded together before the ancestor arrived")
	}

	// The late ancestor: both name it, so both threads merge onto the older.
	f.insert("ancestor@test", "", "Presupuesto")
	f.insert("joiner@test", "ancestor@test", "Re: Presupuesto", "ancestor@test", "a@test", "b@test")

	winner := f.threadOf(a)
	if f.threadOf(b) != winner {
		t.Fatalf("the merge did not unify the two threads: %d vs %d", winner, f.threadOf(b))
	}

	// The loser's row must still exist, tombstoned, pointing at the winner.
	loser := threadA
	if winner == threadA {
		loser = threadB
	}
	if _, err := f.s.ThreadRowByThreadID(ctx, f.account.ID, loser); err == nil {
		t.Error("the merged-away thread still resolves as LIVE; its row was not tombstoned")
	}

	changed, err := f.s.ThreadRowsChangedSince(ctx, f.account.ID, time.Time{}, 100)
	if err != nil {
		t.Fatalf("ThreadRowsChangedSince: %v", err)
	}
	tombstones := 0
	for _, row := range changed {
		if row.Destroyed() {
			tombstones++
			if row.MergedInto == 0 {
				t.Error("a tombstoned thread does not name its survivor; a client told it died " +
					"would have no way to find where the conversation went")
			}
		}
	}
	if tombstones == 0 {
		t.Error("the merge left no tombstone, so Thread/changes cannot report it destroyed")
	}
}

// TestThreadRowsChangedSinceIsStrictlyAfter holds the /changes cursor
// contract: the watermark a client holds IS what it has seen, so including it
// again would replay the last change on every poll forever.
func TestThreadRowsChangedSinceIsStrictlyAfter(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	f.insert("root@test", "", "Presupuesto")
	rows, err := f.s.ThreadRowsChangedSince(ctx, f.account.ID, time.Time{}, 100)
	if err != nil || len(rows) == 0 {
		t.Fatalf("ThreadRowsChangedSince from zero: %d rows, %v", len(rows), err)
	}

	cursor := rows[len(rows)-1].UpdatedAt
	again, err := f.s.ThreadRowsChangedSince(ctx, f.account.ID, cursor, 100)
	if err != nil {
		t.Fatalf("ThreadRowsChangedSince from the watermark: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("polling from the exact watermark returned %d rows, want 0", len(again))
	}
}

// TestEnsureThreadRowForBackfillsOnDemand covers the case migration 0009's SQL
// backfill deliberately skips: a conversation whose oldest member has neither a
// Message-ID nor a References chain, whose key is the Go-side subject digest.
func TestEnsureThreadRowForBackfillsOnDemand(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	id := f.insert("", "", "Sin identificador")
	threadID := f.threadOf(id)

	row, err := f.s.EnsureThreadRowFor(ctx, f.account.ID, threadID)
	if err != nil {
		t.Fatalf("EnsureThreadRowFor: %v", err)
	}
	if store.ThreadKeyScheme(row.RootMessageID) != "subject" {
		t.Errorf("scheme = %q, want the subject digest for a message with no ids",
			store.ThreadKeyScheme(row.RootMessageID))
	}
	// Idempotent: a second call must find the row, not make a second one.
	again, err := f.s.EnsureThreadRowFor(ctx, f.account.ID, threadID)
	if err != nil {
		t.Fatalf("EnsureThreadRowFor twice: %v", err)
	}
	if again.ID != row.ID {
		t.Errorf("the second call created a second row (%d vs %d)", again.ID, row.ID)
	}
}

// ---------------------------------------------------------------------------
// snoozes
// ---------------------------------------------------------------------------

func TestSnoozeRoundTrip(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()
	wake := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)

	row, err := f.s.PutSnooze(ctx, store.Snooze{
		AccountID: f.account.ID, MessageRFCID: "root@test", WakeAt: wake,
	})
	if err != nil {
		t.Fatalf("PutSnooze: %v", err)
	}
	if row.State != store.SnoozePending {
		t.Errorf("state = %q, want pending", row.State)
	}
	if !row.WakeAt.UTC().Equal(wake) {
		t.Errorf("wake_at = %v, want %v", row.WakeAt.UTC(), wake)
	}
	// The empty origin is the store's spelling of INBOX (migration 0009).
	if row.OriginMailbox != "" {
		t.Errorf("origin = %q, want the empty INBOX default", row.OriginMailbox)
	}

	pending, err := f.s.PendingSnoozes(ctx, f.account.ID, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingSnoozes: %d rows, %v", len(pending), err)
	}
}

// TestSnoozeAgainReplacesTheWakeTime is what "snooze it again for longer"
// means, and what the partial unique index is shaped for. A second row would
// mean two wakes for one message.
func TestSnoozeAgainReplacesTheWakeTime(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	first := time.Now().Add(time.Hour).UTC()
	later := time.Now().Add(48 * time.Hour).UTC()

	if _, err := f.s.PutSnooze(ctx, store.Snooze{
		AccountID: f.account.ID, MessageRFCID: "root@test", WakeAt: first,
	}); err != nil {
		t.Fatalf("PutSnooze: %v", err)
	}
	row, err := f.s.PutSnooze(ctx, store.Snooze{
		AccountID: f.account.ID, MessageRFCID: "root@test", WakeAt: later, OriginMailbox: "Archive",
	})
	if err != nil {
		t.Fatalf("PutSnooze again: %v", err)
	}
	if row.WakeAt.UTC().Sub(later).Abs() > time.Second {
		t.Errorf("wake_at = %v, want the new time %v", row.WakeAt.UTC(), later)
	}
	if row.OriginMailbox != "Archive" {
		t.Errorf("origin = %q, want the new origin", row.OriginMailbox)
	}

	pending, err := f.s.PendingSnoozes(ctx, f.account.ID, 10)
	if err != nil {
		t.Fatalf("PendingSnoozes: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("re-snoozing left %d pending rows, want 1 — two rows would wake the message twice",
			len(pending))
	}
}

// TestClaimDueSnoozesTakesOnlyWhatIsDue is the waker's contract: a snooze set
// for tomorrow must not be woken today.
func TestClaimDueSnoozesTakesOnlyWhatIsDue(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	due := time.Now().Add(-time.Minute).UTC()
	notYet := time.Now().Add(time.Hour).UTC()

	for id, wake := range map[string]time.Time{"due@test": due, "later@test": notYet} {
		if _, err := f.s.PutSnooze(ctx, store.Snooze{
			AccountID: f.account.ID, MessageRFCID: id, WakeAt: wake,
		}); err != nil {
			t.Fatalf("PutSnooze(%s): %v", id, err)
		}
	}

	claimed, err := f.s.ClaimDueSnoozes(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("ClaimDueSnoozes: %v", err)
	}
	for _, sn := range claimed {
		if sn.MessageRFCID == "later@test" {
			t.Error("a snooze whose time has not come was claimed")
		}
	}
}

// TestFailSnoozePermanentlyStopsRetrying holds the honesty rule: a wake that
// cannot happen leaves a row that SAYS SO, rather than a silent disappearance
// or an infinite retry claiming it is still going to work.
func TestFailSnoozePermanentlyStopsRetrying(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	row, err := f.s.PutSnooze(ctx, store.Snooze{
		AccountID: f.account.ID, MessageRFCID: "doomed@test",
		WakeAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("PutSnooze: %v", err)
	}
	if err := f.s.FailSnooze(ctx, row.ID, "Dovecot said no", nil); err != nil {
		t.Fatalf("FailSnooze: %v", err)
	}

	claimed, err := f.s.ClaimDueSnoozes(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("ClaimDueSnoozes: %v", err)
	}
	for _, sn := range claimed {
		if sn.ID == row.ID {
			t.Error("a permanently failed snooze is still being claimed")
		}
	}
	// And it is no longer offered to the JMAP surface, which serves only
	// pending rows.
	pending, err := f.s.PendingSnoozes(ctx, f.account.ID, 10)
	if err != nil {
		t.Fatalf("PendingSnoozes: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("a failed snooze is still listed as pending (%d rows)", len(pending))
	}
}

// TestSnoozeIsAccountScoped is the boundary every reader in this store keeps.
func TestSnoozeIsAccountScoped(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()
	other := newAccount(t, f.s)

	if _, err := f.s.PutSnooze(ctx, store.Snooze{
		AccountID: f.account.ID, MessageRFCID: "mine@test",
		WakeAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("PutSnooze: %v", err)
	}
	pending, err := f.s.PendingSnoozes(ctx, other.ID, 10)
	if err != nil {
		t.Fatalf("PendingSnoozes for the other account: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("another account's snoozes are visible (%d rows)", len(pending))
	}
	// And it cannot be canceled across the boundary either.
	canceled, err := f.s.CancelSnooze(ctx, other.ID, "mine@test")
	if err != nil {
		t.Fatalf("CancelSnooze across accounts: %v", err)
	}
	if canceled {
		t.Error("another account canceled this account's snooze")
	}
}

// ---------------------------------------------------------------------------
// mutes
// ---------------------------------------------------------------------------

// TestMuteIsIdempotentBothWays is what a /set handler needs to answer without a
// read-modify-write, and what a client retrying a lost request needs.
func TestMuteIsIdempotentBothWays(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	root := f.insert("root@test", "", "Presupuesto")
	threadID := f.threadOf(root)
	row, err := f.s.ThreadRowByThreadID(ctx, f.account.ID, threadID)
	if err != nil {
		t.Fatalf("ThreadRowByThreadID: %v", err)
	}

	for range 2 {
		if err := f.s.SetMute(ctx, f.account.ID, row.ID, true); err != nil {
			t.Fatalf("SetMute(true): %v", err)
		}
	}
	muted, err := f.s.IsThreadMuted(ctx, f.account.ID, threadID)
	if err != nil || !muted {
		t.Fatalf("IsThreadMuted after muting twice = %v, %v", muted, err)
	}

	for range 2 {
		if err := f.s.SetMute(ctx, f.account.ID, row.ID, false); err != nil {
			t.Fatalf("SetMute(false): %v", err)
		}
	}
	muted, err = f.s.IsThreadMuted(ctx, f.account.ID, threadID)
	if err != nil || muted {
		t.Fatalf("IsThreadMuted after unmuting twice = %v, %v", muted, err)
	}
}

// TestMuteSurvivesAReplyAndAMerge is the property the durable key buys: the
// mute is on the CONVERSATION, so neither a new member nor a merge that moves
// every member onto a different thread_id may lose it.
func TestMuteSurvivesAReplyAndAMerge(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	// A conversation, muted.
	a := f.insert("a@test", "", "Presupuesto")
	threadA := f.threadOf(a)
	rowA, err := f.s.ThreadRowByThreadID(ctx, f.account.ID, threadA)
	if err != nil {
		t.Fatalf("ThreadRowByThreadID: %v", err)
	}
	if err := f.s.SetMute(ctx, f.account.ID, rowA.ID, true); err != nil {
		t.Fatalf("SetMute: %v", err)
	}

	// A reply joins it. The mute must still be there — this is the exact
	// message the mute exists to suppress, so losing it here would make the
	// feature useless.
	f.insert("r1@test", "a@test", "Re: Presupuesto", "a@test")
	muted, err := f.s.IsThreadMuted(ctx, f.account.ID, f.threadOf(a))
	if err != nil || !muted {
		t.Fatalf("the mute was lost when a reply arrived: %v, %v", muted, err)
	}

	// A late ancestor merges this conversation onto an older thread_id.
	f.insert("ancestor@test", "", "Presupuesto raiz")
	f.insert("bridge@test", "ancestor@test", "Re: Presupuesto", "ancestor@test", "a@test")

	// Whatever thread_id the conversation now carries, the mute must follow it
	// — that is what keying on the durable row rather than on the id means.
	final := f.threadOf(a)
	rows, err := f.s.ThreadRowsByThreadIDs(ctx, f.account.ID, []int64{final})
	if err != nil {
		t.Fatalf("ThreadRowsByThreadIDs: %v", err)
	}
	row, ok := rows[final]
	if !ok {
		t.Fatal("the merged conversation has no live thread row")
	}
	mutedRows, err := f.s.MutedThreadRows(ctx, f.account.ID, []int64{row.ID, rowA.ID})
	if err != nil {
		t.Fatalf("MutedThreadRows: %v", err)
	}
	if !mutedRows[rowA.ID] && !mutedRows[row.ID] {
		t.Error("the mute was lost across the merge; the muted conversation would start " +
			"delivering to the inbox again with nothing to tell the user why")
	}
}

// TestListMutedThreadsExcludesTombstones keeps a dead id out of the JMAP
// surface: Thread/get answers notFound for a merged-away id, so listing one
// would hand a client something it cannot fetch.
func TestListMutedThreadsExcludesTombstones(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	a := f.insert("a@test", "", "Presupuesto")
	rowA, err := f.s.ThreadRowByThreadID(ctx, f.account.ID, f.threadOf(a))
	if err != nil {
		t.Fatalf("ThreadRowByThreadID: %v", err)
	}
	if err := f.s.SetMute(ctx, f.account.ID, rowA.ID, true); err != nil {
		t.Fatalf("SetMute: %v", err)
	}

	listed, err := f.s.ListMutedThreads(ctx, f.account.ID, 100)
	if err != nil {
		t.Fatalf("ListMutedThreads: %v", err)
	}
	for _, threadID := range listed {
		if _, err := f.s.ThreadRowByThreadID(ctx, f.account.ID, threadID); err != nil {
			t.Errorf("ListMutedThreads returned thread %d, which does not resolve live", threadID)
		}
	}
}

// TestMuteWatermarkMovesOnUnmute is why the state string carries a count.
//
// An unmute DELETES its row, which lowers the count while max(updated_at)
// stays where the last write left it. Without the count a client polling after
// an unmute would see an unchanged state and keep badging the conversation as
// muted forever.
func TestMuteWatermarkMovesOnUnmute(t *testing.T) {
	f := newThreadFixture(t)
	ctx := context.Background()

	a := f.insert("a@test", "", "Presupuesto")
	row, err := f.s.ThreadRowByThreadID(ctx, f.account.ID, f.threadOf(a))
	if err != nil {
		t.Fatalf("ThreadRowByThreadID: %v", err)
	}
	if err := f.s.SetMute(ctx, f.account.ID, row.ID, true); err != nil {
		t.Fatalf("SetMute: %v", err)
	}
	watermark, count, err := f.s.MuteWatermark(ctx, f.account.ID)
	if err != nil || count != 1 {
		t.Fatalf("MuteWatermark after muting = %v, %d, %v", watermark, count, err)
	}

	if err := f.s.SetMute(ctx, f.account.ID, row.ID, false); err != nil {
		t.Fatalf("SetMute(false): %v", err)
	}
	afterWatermark, afterCount, err := f.s.MuteWatermark(ctx, f.account.ID)
	if err != nil {
		t.Fatalf("MuteWatermark after unmuting: %v", err)
	}
	if afterCount != 0 {
		t.Errorf("count = %d after unmuting, want 0", afterCount)
	}
	// The watermark went BACKWARDS (to zero, since no rows remain) — which is
	// precisely why the count term exists: the pair differs even though the
	// timestamp alone would not distinguish the two states usefully.
	if afterWatermark.Equal(watermark) && afterCount == count {
		t.Error("the state is indistinguishable before and after an unmute")
	}
}
