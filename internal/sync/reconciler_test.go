package sync

import (
	"context"
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
