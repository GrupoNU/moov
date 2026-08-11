package sync

import (
	"testing"
	"time"
)

// The debouncer's two rules, tested against a controlled clock.
//
// A real clock would make these tests either slow (waiting out real durations)
// or flaky (racing them), and the behavior under test is entirely about WHEN
// something becomes due — so the clock is the input, not an environmental
// hazard.

// fakeClock is a manually advanced clock.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// TestDebouncerCoalescesABurst is the ordinary case: many events for one
// mailbox produce exactly one pass.
func TestDebouncerCoalescesABurst(t *testing.T) {
	clk := &fakeClock{now: referenceNow}
	d := newDebouncer(100*time.Millisecond, time.Second, clk.Now)
	defer d.stop()

	for range 20 {
		d.touch("INBOX")
		clk.advance(10 * time.Millisecond)
	}
	if got := d.pendingCount(); got != 1 {
		t.Fatalf("%d mailboxes pending after a burst on one, want 1", got)
	}

	// Still inside the quiet window: nothing is due.
	if due := d.take(); len(due) != 0 {
		t.Errorf("take() = %v during the burst, want nothing", due)
	}

	// The burst ends and the quiet window elapses.
	clk.advance(100 * time.Millisecond)
	due := d.take()
	if len(due) != 1 || due[0] != "INBOX" {
		t.Fatalf("take() = %v after the burst settled, want [INBOX]", due)
	}
	if d.pendingCount() != 0 {
		t.Error("the mailbox is still pending after being taken")
	}
}

// TestDebouncerStarvationGuard is the rule that makes this safe for a busy
// mailbox.
//
// A pure "wait for quiet" debouncer never fires while events keep arriving. For
// a mail engine that means a folder under continuous change is a folder whose
// mail never appears — the worst possible failure, and one that only shows up
// under exactly the load that matters.
func TestDebouncerStarvationGuard(t *testing.T) {
	clk := &fakeClock{now: referenceNow}
	quiet := 100 * time.Millisecond
	maxWait := 500 * time.Millisecond
	d := newDebouncer(quiet, maxWait, clk.Now)
	defer d.stop()

	// Events arrive faster than the quiet window, forever.
	for range 10 {
		d.touch("INBOX")
		clk.advance(50 * time.Millisecond)
	}

	// 500 ms have passed since the first event, so the cap has been reached
	// even though the mailbox never went quiet.
	due := d.take()
	if len(due) != 1 || due[0] != "INBOX" {
		t.Fatalf("take() = %v, want [INBOX]: the starvation guard did not fire", due)
	}
}

// TestDebouncerIsPerMailbox checks that folders do not wait for each other.
//
// A shared timer would delay the folder a user is looking at because an archive
// folder happened to receive a message.
func TestDebouncerIsPerMailbox(t *testing.T) {
	clk := &fakeClock{now: referenceNow}
	d := newDebouncer(100*time.Millisecond, time.Second, clk.Now)
	defer d.stop()

	d.touch("INBOX")
	clk.advance(60 * time.Millisecond)
	d.touch("Archive")

	// INBOX settles first: 100 ms after its only event.
	clk.advance(40 * time.Millisecond)
	due := d.take()
	if len(due) != 1 || due[0] != "INBOX" {
		t.Fatalf("take() = %v, want [INBOX] only — Archive is not due yet", due)
	}
	if d.pendingCount() != 1 {
		t.Errorf("%d mailboxes still pending, want 1 (Archive)", d.pendingCount())
	}

	// Archive settles 100 ms after its own event.
	clk.advance(60 * time.Millisecond)
	due = d.take()
	if len(due) != 1 || due[0] != "Archive" {
		t.Fatalf("take() = %v, want [Archive]", due)
	}
}

// TestDebouncerTakeReturnsSortedNames keeps a multi-folder burst deterministic,
// which is what makes the tests above reproducible rather than order-dependent.
func TestDebouncerTakeReturnsSortedNames(t *testing.T) {
	clk := &fakeClock{now: referenceNow}
	d := newDebouncer(10*time.Millisecond, time.Second, clk.Now)
	defer d.stop()

	for _, name := range []string{"Zebra", "Archive", "INBOX", "Drafts"} {
		d.touch(name)
	}
	clk.advance(20 * time.Millisecond)

	due := d.take()
	want := []string{"Archive", "Drafts", "INBOX", "Zebra"}
	if len(due) != len(want) {
		t.Fatalf("take() = %v, want %v", due, want)
	}
	for i := range want {
		if due[i] != want[i] {
			t.Fatalf("take() = %v, want %v", due, want)
		}
	}
}

// TestDebouncerResetDropsEverything covers the overflow path: a full-account
// sweep subsumes every pending per-mailbox pass, and running them afterwards
// would be round trips that can only confirm what the sweep established.
func TestDebouncerResetDropsEverything(t *testing.T) {
	clk := &fakeClock{now: referenceNow}
	d := newDebouncer(10*time.Millisecond, time.Second, clk.Now)
	defer d.stop()

	d.touch("INBOX")
	d.touch("Archive")
	d.reset()

	if d.pendingCount() != 0 {
		t.Errorf("%d mailboxes pending after reset, want 0", d.pendingCount())
	}
	if d.ready() != nil {
		t.Error("the timer is still armed after reset")
	}
}

// TestDebouncerReadyIsNilWhenIdle documents the property the dispatch loop's
// select depends on: with nothing pending the channel is nil, so the "something
// is due" case simply does not participate rather than firing spuriously or
// busy-looping.
func TestDebouncerReadyIsNilWhenIdle(t *testing.T) {
	clk := &fakeClock{now: referenceNow}
	d := newDebouncer(10*time.Millisecond, time.Second, clk.Now)
	defer d.stop()

	if d.ready() != nil {
		t.Fatal("ready() is non-nil with nothing pending")
	}
	d.touch("INBOX")
	if d.ready() == nil {
		t.Fatal("ready() is nil with a mailbox pending")
	}
}

// TestJitterStaysInRange checks the retry spread. Jitter matters here beyond
// the usual: an installation-wide failure fails every account's watcher at the
// same instant, and without it they would all reconnect in the same
// millisecond — which is what fail2ban is looking for (ADR §4).
func TestJitterStaysInRange(t *testing.T) {
	const base = time.Second
	for range 200 {
		got := jitter(base)
		if got < base/2 || got > base {
			t.Fatalf("jitter(%s) = %s, want [%s, %s]", base, got, base/2, base)
		}
	}
	if got := jitter(0); got != 0 {
		t.Errorf("jitter(0) = %s, want 0", got)
	}
}

// TestNextBackoffDoublesToACeiling covers the growth and its bound.
func TestNextBackoffDoublesToACeiling(t *testing.T) {
	const maximum = 10 * time.Second

	got := time.Second
	for range 10 {
		got = nextBackoff(got, maximum)
		if got > maximum {
			t.Fatalf("backoff grew past the ceiling: %s > %s", got, maximum)
		}
	}
	if got != maximum {
		t.Errorf("backoff settled at %s, want the ceiling %s", got, maximum)
	}
}
