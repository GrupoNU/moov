package sync

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// The defensive reconciler: it must find what push missed.
//
// Every test here injects a divergence the WATCHER CANNOT SEE — the fake's
// silent-notify mode applies a mutation without emitting an event, which models
// exactly the three real ways an event is lost (a dropped channel entry, a
// Dovecot NOTIFY regression, a watcher that was down). Anything the reconciler
// then finds, it found by comparing state rather than by being told.

// reconcilerFixture is a synced account plus a watcher object to drive
// Reconcile through. The watcher is NOT started: these tests call Reconcile
// directly so the sweep is the only thing that could have repaired anything.
type reconcilerFixture struct {
	*syncedEnv
	watcher *PushWatcher
}

func newReconcilerFixture(t *testing.T, messages int) *reconcilerFixture {
	t.Helper()

	env := newSyncedEnv(t, messages)
	w, err := NewPushWatcher(env.store, env.blobs, WatcherOptions{
		Options:   env.opts,
		Connector: ConnectorFunc(func(context.Context, store.Account, int) ([]imap.Client, error) { return env.srv.clients(2), nil }),
		// Off: these tests drive Reconcile by hand.
		ReconcileInterval: -1,
	})
	if err != nil {
		t.Fatalf("NewPushWatcher: %v", err)
	}
	return &reconcilerFixture{syncedEnv: env, watcher: w}
}

// reconcile runs one sweep.
func (f *reconcilerFixture) reconcile(t *testing.T) ReconcileResult {
	t.Helper()
	res, err := f.watcher.Reconcile(context.Background(), f.syncer, f.account, f.logger)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

// TestReconcilerFindsAndRepairsAMissedDelivery is the E6 acceptance criterion
// for the reconciler: a divergence injected behind the watcher's back is found
// and repaired.
func TestReconcilerFindsAndRepairsAMissedDelivery(t *testing.T) {
	f := newReconcilerFixture(t, 5)

	// The message arrives and NO event is emitted: push has failed.
	f.srv.setSilentNotify(true)
	uid := f.srv.deliver("INBOX",
		buildMessage(600, "Nobody was told", referenceNow, "Body."), nil, referenceNow)
	f.srv.setSilentNotify(false)

	if got := len(f.liveUIDs(t, "INBOX")); got != 5 {
		t.Fatalf("the silent delivery reached the store without a sweep (%d messages)", got)
	}

	res := f.reconcile(t)

	if res.Diverged != 1 {
		t.Fatalf("the sweep found %d divergences, want 1: %+v", res.Diverged, res.Divergences)
	}
	if res.Repaired != 1 {
		t.Errorf("the sweep repaired %d divergences, want 1", res.Repaired)
	}
	if len(res.Divergences) != 1 || res.Divergences[0].Mailbox != "INBOX" {
		t.Fatalf("divergences = %+v, want one on INBOX", res.Divergences)
	}
	// The reason must name what actually moved, because that string is what an
	// operator reads when asking "is NOTIFY healthy".
	if !strings.Contains(res.Divergences[0].Reason, "uidnext") {
		t.Errorf("divergence reason = %q, want it to name uidnext", res.Divergences[0].Reason)
	}

	live := f.liveUIDs(t, "INBOX")
	if len(live) != 6 {
		t.Fatalf("after the sweep the mailbox holds %d messages, want 6", len(live))
	}
	var found bool
	for _, u := range live {
		if imap.UID(u) == uid {
			found = true
		}
	}
	if !found {
		t.Errorf("the missed message (uid %d) is still not stored", uid)
	}
}

// TestReconcilerFindsAMissedFlagChange is the case only HIGHESTMODSEQ can
// catch.
//
// A \Flagged toggle changes neither MESSAGES nor UIDNEXT (S2 T4), so a
// reconciler comparing only counts would report the mailbox as healthy while
// Moov shows the wrong flags indefinitely.
func TestReconcilerFindsAMissedFlagChange(t *testing.T) {
	f := newReconcilerFixture(t, 5)

	f.srv.setSilentNotify(true)
	f.srv.setFlags("INBOX", 3, []string{"seen", "flagged"}, nil)
	f.srv.setSilentNotify(false)

	res := f.reconcile(t)

	if res.Diverged != 1 {
		t.Fatalf("the sweep found %d divergences, want 1: %+v", res.Diverged, res.Divergences)
	}
	if !strings.Contains(res.Divergences[0].Reason, "highestmodseq") {
		t.Errorf("divergence reason = %q, want it to name highestmodseq — "+
			"a flag toggle moves no other counter", res.Divergences[0].Reason)
	}

	flags, ok := f.flagsOf(t, "INBOX", 3)
	if !ok || !flags.Has(store.FlagFlagged) {
		t.Errorf("the missed flag change was not repaired (flags=%v)", flags)
	}
}

// TestReconcilerFindsAMissedExpunge covers the third kind of lost event.
func TestReconcilerFindsAMissedExpunge(t *testing.T) {
	f := newReconcilerFixture(t, 5)

	f.srv.setSilentNotify(true)
	f.srv.expunge("INBOX", 4)
	f.srv.setSilentNotify(false)

	res := f.reconcile(t)
	if res.Diverged != 1 {
		t.Fatalf("the sweep found %d divergences, want 1: %+v", res.Diverged, res.Divergences)
	}

	for _, u := range f.liveUIDs(t, "INBOX") {
		if u == 4 {
			t.Fatal("the expunged message is still live after the sweep")
		}
	}
}

// TestReconcilerIsQuietWhenNothingDiverged is what makes it affordable to run
// on a schedule.
//
// A sweep over a healthy account must cost one LIST-STATUS and find nothing. A
// reconciler that reported spurious divergences would both waste round trips
// and destroy the signal value of the divergence metric, which is the number
// telling an operator whether push is working.
func TestReconcilerIsQuietWhenNothingDiverged(t *testing.T) {
	f := newReconcilerFixture(t, 6)

	f.srv.mu.Lock()
	fetchesBefore := f.srv.fetchCount
	f.srv.mu.Unlock()

	res := f.reconcile(t)

	if res.Diverged != 0 {
		t.Errorf("a healthy account reported %d divergences: %+v", res.Diverged, res.Divergences)
	}
	if res.Checked == 0 {
		t.Error("the sweep checked no mailboxes")
	}

	f.srv.mu.Lock()
	extra := f.srv.fetchCount - fetchesBefore
	f.srv.mu.Unlock()
	if extra != 0 {
		t.Errorf("a sweep over a healthy account downloaded %d bodies, want 0", extra)
	}

	// And it is idempotent: a second sweep also finds nothing.
	if second := f.reconcile(t); second.Diverged != 0 {
		t.Errorf("the second sweep reported %d divergences", second.Diverged)
	}

	// The escalation bound must not make a healthy sweep cost anything. This
	// caught a real regression: the first version of the bound cleared it by
	// reading the checkpoint row for EVERY healthy mailbox on EVERY sweep,
	// which is a query per mailbox per sweep for bookkeeping about a failure
	// that had never happened — spending exactly the budget this test exists
	// to protect. A healthy mailbox is now dismissed from an in-memory set.
	if f.watcher.hasEscalation(f.mailboxID(t, "INBOX")) {
		t.Error("a healthy mailbox carries an escalation bound")
	}
}

// mailboxID looks up a mailbox row id by name.
func (e *syncedEnv) mailboxID(t *testing.T, name string) int64 {
	t.Helper()
	row, err := e.store.GetMailboxByName(context.Background(), e.account.ID, name)
	if err != nil {
		t.Fatalf("GetMailboxByName(%q): %v", name, err)
	}
	return row.ID
}

// TestReconcilerDiscoversAMailboxCreatedSilently covers the structural
// divergence: a folder that exists on the server and nowhere locally. No
// per-mailbox pass could find it, because there is no local mailbox to pass
// over.
func TestReconcilerDiscoversAMailboxCreatedSilently(t *testing.T) {
	f := newReconcilerFixture(t, 3)

	f.srv.addMailbox("Newsletters", imap.RoleNone, 400)
	seedMailboxLocked(f.srv, "Newsletters", 3, referenceNow, "News")

	res := f.reconcile(t)

	if res.Diverged == 0 {
		t.Fatal("the sweep did not notice a mailbox that exists only on the server")
	}
	var named bool
	for _, d := range res.Divergences {
		if d.Mailbox == "Newsletters" {
			named = true
		}
	}
	if !named {
		t.Errorf("divergences = %+v, want one naming Newsletters", res.Divergences)
	}

	if got := len(f.liveUIDs(t, "Newsletters")); got != 3 {
		t.Errorf("the discovered mailbox holds %d messages, want 3", got)
	}
}

// TestReconcilerNoticesAMailboxDeletedOnTheServer covers the other structural
// direction. It is reported rather than silently ignored: a folder Moov still
// shows but the server no longer has is a visible inconsistency for the user.
func TestReconcilerNoticesAMailboxDeletedOnTheServer(t *testing.T) {
	f := newReconcilerFixture(t, 3)

	// A second folder that is synced and then disappears.
	f.srv.addMailbox("Temp", imap.RoleNone, 500)
	seedMailboxLocked(f.srv, "Temp", 2, referenceNow, "Temp")
	if _, err := f.syncer.Run(context.Background(), f.account); err != nil {
		t.Fatalf("syncing the second folder: %v", err)
	}

	f.srv.mu.Lock()
	kept := f.srv.mailboxes[:0]
	for _, m := range f.srv.mailboxes {
		if m.name != "Temp" {
			kept = append(kept, m)
		}
	}
	f.srv.mailboxes = kept
	f.srv.mu.Unlock()

	res := f.reconcile(t)

	var named bool
	for _, d := range res.Divergences {
		if d.Mailbox == "Temp" && strings.Contains(d.Reason, "no longer exists") {
			named = true
		}
	}
	if !named {
		t.Errorf("divergences = %+v, want one saying Temp no longer exists", res.Divergences)
	}
}

// TestReconcilerRunsOnItsSchedule proves the periodic loop is wired, not just
// the sweep it calls.
//
// The delivery is made AFTER the watcher has connected and swept, so the
// connect-time sweep cannot be what repairs it: only the scheduled tick can.
// And it is made silently, so no notification could have triggered a pass
// either — which leaves the reconciler as the only possible explanation for the
// message appearing.
func TestReconcilerRunsOnItsSchedule(t *testing.T) {
	env := newSyncedEnv(t, 3)

	h := startWatcher(t, env, func(o *WatcherOptions) {
		o.ReconcileInterval = 150 * time.Millisecond
	})

	// startWatcher already waited for ObsConnected, so the reconnect sweep has
	// happened. Anything from here on is the schedule's doing.
	env.srv.setSilentNotify(true)
	env.srv.deliver("INBOX",
		buildMessage(610, "For the scheduled sweep", referenceNow, "Body."), nil, referenceNow)
	env.srv.setSilentNotify(false)

	waitFor(t, 20*time.Second, func() bool {
		return len(env.liveUIDs(t, "INBOX")) == 4
	}, "the scheduled reconciler never picked up the silent delivery")

	h.waitFor(t, ObsReconciled, 1, "no reconciliation was reported through OnEvent")
}

// TestCompareMailboxState covers the comparison directly, including the
// backwards-movement case.
//
// A counter that moved BACKWARDS is not impossible and must not be ignored: a
// mailbox recreated with the same name resets UIDNEXT, and that is precisely
// the UIDVALIDITY case that must trigger a resync. Treating only forward
// movement as divergence would make the one situation that corrupts data the
// one situation the sweep skips.
func TestCompareMailboxState(t *testing.T) {
	ptr := func(v int64) *int64 { return &v }

	base := store.Mailbox{
		UIDValidity:   ptr(100),
		UIDNext:       ptr(50),
		HighestModSeq: ptr(900),
	}
	matching := imap.MailboxInfo{UIDValidity: 100, UIDNext: 50, HighestModSeq: 900}

	t.Run("identical is not a divergence", func(t *testing.T) {
		if _, diverged := compareMailboxState(base, matching); diverged {
			t.Error("identical state reported as diverged")
		}
	})

	t.Run("uidnext moved forward", func(t *testing.T) {
		info := matching
		info.UIDNext = 55
		reason, diverged := compareMailboxState(base, info)
		if !diverged || !strings.Contains(reason, "uidnext") {
			t.Errorf("compareMailboxState = (%q, %v), want a uidnext divergence", reason, diverged)
		}
	})

	t.Run("uidnext moved backwards", func(t *testing.T) {
		info := matching
		info.UIDNext = 3
		if _, diverged := compareMailboxState(base, info); !diverged {
			t.Error("a backwards uidnext was not reported; that is the recreated-mailbox case")
		}
	})

	t.Run("only the modseq moved", func(t *testing.T) {
		info := matching
		info.HighestModSeq = 901
		reason, diverged := compareMailboxState(base, info)
		if !diverged || !strings.Contains(reason, "highestmodseq") {
			t.Errorf("compareMailboxState = (%q, %v), want a highestmodseq divergence", reason, diverged)
		}
	})

	t.Run("uidvalidity changed", func(t *testing.T) {
		info := matching
		info.UIDValidity = 101
		reason, diverged := compareMailboxState(base, info)
		if !diverged || !strings.Contains(reason, "uidvalidity") {
			t.Errorf("compareMailboxState = (%q, %v), want a uidvalidity divergence", reason, diverged)
		}
	})

	t.Run("a never-synced mailbox has nothing to compare", func(t *testing.T) {
		if _, diverged := compareMailboxState(store.Mailbox{}, matching); diverged {
			t.Error("a mailbox with no stored counters reported as diverged")
		}
	})
}

// TestReconcilerRepairsAMessageLostBelowTheCursor is the production defect of
// 2026-09-17, written as a test.
//
// # The incident
//
// The idle heartbeat (c6b0850) probes a quiet session by running Reconcile. It
// fired 906 times overnight and reported 180 divergences — all on one account,
// always INBOX, zero errors. The account had 24,147 messages in INBOX on
// Dovecot and 24,146 in Moov, and diffing the UID lists gave exactly one
// missing UID: 23840, a message from five weeks earlier that some one-off
// failure had lost. It was absent from message_state entirely.
//
// The sweep detected it every two minutes, ran an incremental pass, declared
// `repaired=1`, and changed nothing — because incrementalMailbox advances from
// the STORED CURSOR to find NEW UIDs, and the gap was old, far below it. The
// pass returned no error, and "no error" was what the old code counted as a
// repair.
//
// So this test reproduces the shape exactly: a message that is on the server,
// below the cursor, and missing locally. The sweep must not be allowed to call
// that repaired unless it is.
func TestReconcilerRepairsAMessageLostBelowTheCursor(t *testing.T) {
	f := newReconcilerFixture(t, 6)

	// The loss: one OLD message's state row disappears, with the mailbox's
	// cursor left untouched and far above it. This is what the production
	// account looked like — not a tombstone, not a soft delete, simply a row
	// that is not there.
	const lost = int64(2)
	f.deleteMessageState(t, "INBOX", lost)

	if before := f.liveUIDs(t, "INBOX"); len(before) != 5 {
		t.Fatalf("setup: the mailbox holds %d messages, want 5 after the loss", len(before))
	}

	res := f.reconcile(t)

	if res.Diverged != 1 {
		t.Fatalf("the sweep found %d divergences, want 1: %+v", res.Diverged, res.Divergences)
	}

	// The assertion the old code fails: Repaired is a claim about the STORE,
	// not about whether a function returned nil.
	live := f.liveUIDs(t, "INBOX")
	var back bool
	for _, u := range live {
		if u == lost {
			back = true
		}
	}
	if !back {
		t.Errorf("uid %d is still missing after the sweep (live=%v)", lost, live)
	}
	if res.Repaired != 1 {
		t.Errorf("the sweep repaired %d divergences, want 1", res.Repaired)
	}
	if len(live) != 6 {
		t.Errorf("the mailbox holds %d messages after the sweep, want 6", len(live))
	}
}

// deleteMessageState removes one message's state row, modeling a message the
// engine lost — the row was never written, or was written and lost. It does NOT
// tombstone: a tombstone is a message the engine knows about and believes
// expunged, which is a different (and correctly handled) state.
func (e *syncedEnv) deleteMessageState(t *testing.T, mailbox string, uid int64) {
	t.Helper()

	tag, err := e.store.Pool().Exec(context.Background(), `
		DELETE FROM message_state ms
		 USING mailboxes mb
		 WHERE ms.mailbox_id = mb.id
		   AND ms.account_id = $1 AND mb.name = $2 AND ms.uid = $3`,
		e.account.ID, mailbox, uid)
	if err != nil {
		t.Fatalf("deleting message_state for uid %d: %v", uid, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("deleting message_state for uid %d removed %d rows, want 1", uid, tag.RowsAffected())
	}
}

// TestReconcilerReportsADivergenceItCannotRepair is the honesty half of the
// 2026-09-17 fix.
//
// The incident's defining property was a WARN that said `repaired=1` while
// nothing had been repaired — a number an operator would trust, and which was
// false 906 times in one night. So a divergence that survives every repair the
// engine has must be counted as UNREPAIRED, named in the result, and emitted as
// its own observation kind, not folded into a success.
//
// The unfixable divergence here is a mailbox that advertises a message it will
// not serve. That is the real residual class — a message the parser refuses, an
// index the server has not caught up with — and it is the one a backfill walk
// cannot close either, which is exactly why the escalation needs a bound.
func TestReconcilerReportsADivergenceItCannotRepair(t *testing.T) {
	f := newReconcilerFixture(t, 4)

	var stuck []WatchObservation
	f.watcher.opts.OnEvent = func(obs WatchObservation) {
		if obs.Kind == ObsStuckDivergence {
			stuck = append(stuck, obs)
		}
	}

	f.srv.setSilentNotify(true)
	f.srv.setPhantom("INBOX", 1)
	f.srv.setSilentNotify(false)

	res := f.reconcile(t)

	if res.Diverged != 1 {
		t.Fatalf("the sweep found %d divergences, want 1: %+v", res.Diverged, res.Divergences)
	}
	if res.Repaired != 0 {
		t.Errorf("the sweep claimed %d repairs of a divergence it cannot fix, want 0", res.Repaired)
	}
	if res.Unrepaired != 1 {
		t.Errorf("the sweep reported %d unrepaired divergences, want 1", res.Unrepaired)
	}
	if res.Escalated != 1 {
		t.Errorf("the sweep escalated %d times, want 1 — an incremental pass cannot "+
			"close a gap below the cursor, so the first failure must reach for the walk",
			res.Escalated)
	}

	// The reason must still NAME the thing that is wrong. "It did not work" is
	// not a diagnosis; "messages stored=4 server=5" is where an investigation
	// starts.
	if len(res.Stuck) != 1 || res.Stuck[0].Mailbox != "INBOX" {
		t.Fatalf("stuck = %+v, want one on INBOX", res.Stuck)
	}
	if !strings.Contains(res.Stuck[0].Reason, "messages stored=") {
		t.Errorf("stuck reason = %q, want it to name the counts that still disagree",
			res.Stuck[0].Reason)
	}

	if len(stuck) != 1 || stuck[0].Mailbox != "INBOX" {
		t.Fatalf("observations = %+v, want one ObsStuckDivergence on INBOX", stuck)
	}
	if stuck[0].AccountID != f.account.ID {
		t.Errorf("the stuck observation names account %d, want %d", stuck[0].AccountID, f.account.ID)
	}
}

// TestReconcilerBoundsRepeatedEscalations is the bound: the fix must not become
// a new infinite loop with a bigger hammer.
//
// The heartbeat runs Reconcile every two minutes. A mailbox that diverges for a
// reason no backfill can fix would, without a bound, be walked end to end every
// two minutes forever — which on the 24k-message INBOX that produced the
// incident is far worse than the useless incremental pass it replaced. So the
// SECOND sweep over the same stuck mailbox must still report it, still count it
// as unrepaired, and NOT walk it again.
func TestReconcilerBoundsRepeatedEscalations(t *testing.T) {
	f := newReconcilerFixture(t, 4)

	f.srv.setSilentNotify(true)
	f.srv.setPhantom("INBOX", 1)
	f.srv.setSilentNotify(false)

	first := f.reconcile(t)
	if first.Escalated != 1 {
		t.Fatalf("the first sweep escalated %d times, want 1", first.Escalated)
	}

	second := f.reconcile(t)

	if second.Diverged != 1 {
		t.Fatalf("the second sweep found %d divergences, want 1 — the mailbox is still broken",
			second.Diverged)
	}
	if second.Unrepaired != 1 {
		t.Errorf("the second sweep reported %d unrepaired, want 1 — silence about a "+
			"still-broken mailbox is the bug this whole change exists to end",
			second.Unrepaired)
	}
	if second.Escalated != 0 {
		t.Errorf("the second sweep escalated %d times, want 0: a walk 15 minutes after "+
			"the last one is the bound, and without it the heartbeat walks a 24k "+
			"mailbox every two minutes forever", second.Escalated)
	}
	if second.Repaired != 0 {
		t.Errorf("the second sweep claimed %d repairs, want 0", second.Repaired)
	}

	// The in-memory set that lets a healthy sweep skip the query must actually
	// know about this mailbox — otherwise clearEscalation would never clear a
	// real bound, and a mailbox that recovered would serve out its backoff
	// forever.
	if !f.watcher.hasEscalation(f.mailboxID(t, "INBOX")) {
		t.Error("a mailbox with a persisted bound is missing from the in-memory set; " +
			"clearEscalation would never clear it")
	}
}

// TestReconcilerClearsTheEscalationBoundAfterASuccess proves the bound is not a
// one-way ratchet.
//
// A mailbox that failed once and then recovered must pay nothing on its next
// incident: the backoff exists to stop a HOPELESS mailbox from being walked
// forever, not to punish a mailbox that had one bad afternoon. Without the
// clear, a folder that hit a transient problem in August would still be waiting
// out a 24-hour backoff in September while real mail went missing.
func TestReconcilerClearsTheEscalationBoundAfterASuccess(t *testing.T) {
	f := newReconcilerFixture(t, 4)

	// Fail once: the bound is now armed.
	f.srv.setSilentNotify(true)
	f.srv.setPhantom("INBOX", 1)
	f.srv.setSilentNotify(false)
	if first := f.reconcile(t); first.Unrepaired != 1 {
		t.Fatalf("the first sweep reported %d unrepaired, want 1", first.Unrepaired)
	}

	// The condition clears — the message the server was counting turns out to
	// exist after all.
	f.srv.setSilentNotify(true)
	f.srv.setPhantom("INBOX", 0)
	f.srv.setSilentNotify(false)

	if second := f.reconcile(t); second.Diverged != 0 {
		t.Fatalf("a healed mailbox still reported %d divergences: %+v",
			second.Diverged, second.Divergences)
	}

	// Break it again in the way only a walk can fix. If the bound had survived
	// the success, this sweep would refuse to escalate and the message would
	// stay missing.
	f.deleteMessageState(t, "INBOX", 2)

	third := f.reconcile(t)
	if third.Escalated != 1 {
		t.Errorf("the sweep escalated %d times after an intervening success, want 1 — "+
			"a recovered mailbox must not still be serving out an old backoff",
			third.Escalated)
	}
	if third.Repaired != 1 {
		t.Errorf("the sweep repaired %d, want 1", third.Repaired)
	}
	var back bool
	for _, u := range f.liveUIDs(t, "INBOX") {
		if u == 2 {
			back = true
		}
	}
	if !back {
		t.Error("uid 2 is still missing after the sweep")
	}
}

// TestEscalationBackoffGrows covers the delay schedule directly, because the
// sweep tests can only observe "escalated or not" and the SHAPE of the backoff
// — immediate, then 15 minutes, doubling to a one-day ceiling — is the actual
// design decision.
func TestEscalationBackoffGrows(t *testing.T) {
	f := newReconcilerFixture(t, 2)
	ctx := context.Background()

	row, err := f.store.GetMailboxByName(ctx, f.account.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName: %v", err)
	}
	mb := syncMailbox{row: row, info: imap.MailboxInfo{Name: "INBOX"}}

	t.Run("a mailbox with no failures escalates immediately", func(t *testing.T) {
		allowed, failures, wait, err := f.watcher.escalationAllowed(ctx, f.account, mb)
		if err != nil {
			t.Fatalf("escalationAllowed: %v", err)
		}
		if !allowed || failures != 0 || wait != 0 {
			t.Errorf("escalationAllowed = (%v, %d, %v), want (true, 0, 0) — the common "+
				"case is a real gap one walk closes, and making a user wait for it "+
				"would be absurd", allowed, failures, wait)
		}
	})

	cases := []struct {
		failures int
		want     time.Duration
	}{
		{1, escalationBase},
		{2, 2 * escalationBase},
		{3, 4 * escalationBase},
		{99, escalationMax},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d failures", tc.failures), func(t *testing.T) {
			if err := f.watcher.saveEscalation(ctx, f.account.ID, row.ID, escalationState{
				Failures:    tc.failures,
				LastAttempt: time.Now(),
			}); err != nil {
				t.Fatalf("saveEscalation: %v", err)
			}
			allowed, failures, wait, err := f.watcher.escalationAllowed(ctx, f.account, mb)
			if err != nil {
				t.Fatalf("escalationAllowed: %v", err)
			}
			if allowed {
				t.Fatal("a mailbox that just failed was allowed to escalate again at once")
			}
			if failures != tc.failures {
				t.Errorf("failures = %d, want %d", failures, tc.failures)
			}
			// The wait is measured against a real clock, so it is a hair under
			// the nominal delay by the time it is read.
			if wait > tc.want || wait < tc.want-time.Minute {
				t.Errorf("wait = %v, want about %v", wait, tc.want)
			}
		})
	}

	t.Run("a bound whose delay has elapsed allows the walk", func(t *testing.T) {
		if err := f.watcher.saveEscalation(ctx, f.account.ID, row.ID, escalationState{
			Failures:    1,
			LastAttempt: time.Now().Add(-2 * escalationBase),
		}); err != nil {
			t.Fatalf("saveEscalation: %v", err)
		}
		allowed, _, _, err := f.watcher.escalationAllowed(ctx, f.account, mb)
		if err != nil {
			t.Fatalf("escalationAllowed: %v", err)
		}
		if !allowed {
			t.Error("the backoff never expires; a ceiling that never retries is a mailbox " +
				"abandoned forever")
		}
	})
}

// TestReconcilerBudgetsEscalationsPerSweep is the other half of "bounded": the
// backoff limits how OFTEN one mailbox is walked, this limits how MANY are
// walked at once.
//
// The account that produced the incident has 24 mailboxes and 26,869 messages.
// A sweep that found several of them diverged and walked each one back to back
// would replace a silent no-op with a thundering herd against the Dovecot this
// engine is built not to disturb (ADR §4). So a sweep spends one walk, reports
// the rest honestly, and picks them up on the next tick.
func TestReconcilerBudgetsEscalationsPerSweep(t *testing.T) {
	f := newReconcilerFixture(t, 4)

	// A second folder, synced, so both are candidates for escalation.
	f.srv.addMailbox("Archive", imap.RoleArchive, 700)
	seedMailboxLocked(f.srv, "Archive", 4, referenceNow, "Archive")
	if _, err := f.syncer.Run(context.Background(), f.account); err != nil {
		t.Fatalf("syncing the second folder: %v", err)
	}

	// Both diverge in the way only a walk could fix, and in a way no walk can
	// actually fix — so neither can consume the budget and then vanish from
	// the comparison.
	f.srv.setSilentNotify(true)
	f.srv.setPhantom("INBOX", 1)
	f.srv.setPhantom("Archive", 1)
	f.srv.setSilentNotify(false)

	res := f.reconcile(t)

	if res.Diverged != 2 {
		t.Fatalf("the sweep found %d divergences, want 2: %+v", res.Diverged, res.Divergences)
	}
	if res.Escalated != 1 {
		t.Errorf("the sweep escalated %d times, want 1 — one walk per sweep is the budget",
			res.Escalated)
	}
	// Neither was repaired, and BOTH must still be reported: a mailbox that did
	// not get the budget is deferred, never silently dropped.
	if res.Unrepaired != 2 {
		t.Errorf("the sweep reported %d unrepaired, want 2 — a deferred mailbox is "+
			"still a broken mailbox and must still be visible", res.Unrepaired)
	}
	if len(res.Stuck) != 2 {
		t.Errorf("stuck = %+v, want both mailboxes named", res.Stuck)
	}
}
