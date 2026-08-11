package sync

import (
	"sort"
	"time"
)

// debouncer coalesces per-mailbox notifications into one pass per burst.
//
// # The contract
//
// touch(mailbox) records that a mailbox changed. ready() fires when at least
// one mailbox has settled, and take() returns those mailboxes and forgets them.
// A mailbox is settled when either:
//
//   - quiet time has passed since its last event (the ordinary case: a burst
//     ends and one pass covers all of it), or
//   - maxWait has passed since its FIRST event (the starvation guard).
//
// The second rule is what makes this safe for a folder under continuous change.
// A pure "wait for quiet" debouncer never fires while events keep arriving, and
// here that would mean a busy mailbox whose mail never appears — the worst
// possible failure for a mail client, and one that only shows up under exactly
// the load that matters.
//
// # Why it is not just a time.Timer per mailbox
//
// A timer per mailbox is a goroutine (or a callback on the runtime timer
// goroutine) mutating shared state from outside the event loop, which needs a
// lock and makes the loop's ordering non-deterministic — and therefore makes
// the tests probabilistic. Instead this keeps deadlines as data and exposes one
// channel that the loop selects on, so every state change happens on the
// dispatch goroutine and the whole thing is testable with an injected clock.
type debouncer struct {
	quiet   time.Duration
	maxWait time.Duration
	now     func() time.Time

	// pending maps a mailbox to its deadlines.
	pending map[string]*pendingMailbox

	// timer fires at the earliest deadline among pending. Nil when nothing is
	// pending.
	timer *time.Timer
}

// pendingMailbox is one mailbox waiting for its burst to settle.
type pendingMailbox struct {
	// first is when the burst started, which maxWait is measured from.
	first time.Time
	// last is the most recent event, which quiet is measured from.
	last time.Time
}

// deadline is when this mailbox becomes due: the earlier of "quiet since the
// last event" and "maxWait since the first".
func (p *pendingMailbox) deadline(quiet, maxWait time.Duration) time.Time {
	settle := p.last.Add(quiet)
	limit := p.first.Add(maxWait)
	if limit.Before(settle) {
		return limit
	}
	return settle
}

func newDebouncer(quiet, maxWait time.Duration, now func() time.Time) *debouncer {
	if now == nil {
		now = time.Now
	}
	return &debouncer{
		quiet:   quiet,
		maxWait: maxWait,
		now:     now,
		pending: map[string]*pendingMailbox{},
	}
}

// touch records an event for a mailbox and re-arms the timer.
func (d *debouncer) touch(mailbox string) {
	now := d.now()
	p, ok := d.pending[mailbox]
	if !ok {
		p = &pendingMailbox{first: now}
		d.pending[mailbox] = p
	}
	p.last = now
	d.arm()
}

// ready returns the channel that fires when something is due.
//
// A nil channel when nothing is pending is deliberate and is what makes the
// dispatch loop's select correct: a receive from a nil channel blocks forever,
// so the "something is due" case simply does not participate until there is
// something. The alternative — a channel that never fires — would need a
// sentinel value, and a busy-looping default case would burn a core.
func (d *debouncer) ready() <-chan time.Time {
	if d.timer == nil {
		return nil
	}
	return d.timer.C
}

// take returns every mailbox that is due now and forgets them.
//
// A mailbox that is NOT yet due stays pending and the timer is re-armed for it,
// which is what lets one wake-up serve several mailboxes with different
// deadlines without losing the ones that are not ready.
func (d *debouncer) take() []string {
	now := d.now()

	var due []string
	for name, p := range d.pending {
		if !p.deadline(d.quiet, d.maxWait).After(now) {
			due = append(due, name)
		}
	}
	for _, name := range due {
		delete(d.pending, name)
	}

	// Sorted so a burst across several folders is processed in a deterministic
	// order. It costs nothing at these sizes and turns a flaky test into a
	// reproducible one.
	sort.Strings(due)

	d.stopTimer()
	d.arm()
	return due
}

// reset forgets everything pending.
//
// It is called when a sweep of the whole account is about to run: every pending
// per-mailbox pass is subsumed by it, and running them afterwards would be
// round trips that can only confirm what the sweep just established.
func (d *debouncer) reset() {
	d.pending = map[string]*pendingMailbox{}
	d.stopTimer()
}

// stop releases the timer. It is safe to call more than once.
func (d *debouncer) stop() { d.stopTimer() }

// arm sets the timer to the earliest pending deadline.
func (d *debouncer) arm() {
	if len(d.pending) == 0 {
		d.stopTimer()
		return
	}

	now := d.now()
	var earliest time.Time
	for _, p := range d.pending {
		dl := p.deadline(d.quiet, d.maxWait)
		if earliest.IsZero() || dl.Before(earliest) {
			earliest = dl
		}
	}

	wait := earliest.Sub(now)
	if wait < 0 {
		// Already due. Zero rather than negative: time.NewTimer treats a
		// non-positive duration as "fire immediately", which is what is wanted,
		// but being explicit keeps the intent legible.
		wait = 0
	}

	d.stopTimer()
	d.timer = time.NewTimer(wait)
}

// stopTimer stops and clears the timer.
func (d *debouncer) stopTimer() {
	if d.timer == nil {
		return
	}
	d.timer.Stop()
	d.timer = nil
}

// pendingCount reports how many mailboxes are waiting. Tests use it; the
// dispatch loop does not need it.
func (d *debouncer) pendingCount() int { return len(d.pending) }
