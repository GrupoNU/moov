package sync

import (
	"sync"
	"time"
)

// The push broker (W4a, arbitration W-A4): the in-process fan-out that turns
// "account N changed" into an RFC 8620 §7.1 StateChange for every connected
// EventSource client.
//
// # Why a broker and not polling
//
// W-A4 is explicit: "Fuente: un broker in-process suscripto al ciclo del
// watcher/incremental (el engine ya sabe cuándo cambió una cuenta); sin
// polling interno." The sync engine is the only component that knows a
// change happened at the instant it happens — the incremental pass has just
// committed the rows. A poller would add latency (its interval) and load (a
// query per account per tick) to reproduce a fact the engine already holds.
//
// # What is published, and what is NOT
//
// The broker publishes an ACCOUNT ID and a moment, never the state strings
// themselves. That separation is the design's load-bearing decision:
//
// RFC 8620 §7.1 defines a TypeState's value as "the 'state' property that
// would currently be returned by a call to 'Foo/get'". The only component
// that can answer that is the one that serves Foo/get — mail.Adapter's
// StateReader, reading the store's own watermark. If the broker computed or
// cached state strings, a client comparing a pushed state against a /get
// state could see them disagree, which is precisely the desync the RFC's
// wording exists to prevent. So the broker says WHEN and WHO; the HTTP layer
// asks the same reader /get and /changes ask for the WHAT, at the moment it
// writes the event.
//
// It also makes the coalescing below trivially correct.
//
// # Coalescing
//
// A §7.1 payload is a SNAPSHOT of current state strings, not a diff — the
// client's own instruction is to "compare the new state strings with its
// current values to see whether it has the current data for these types".
// Two changes to one account therefore produce a payload that is identical
// in kind to one change: the latest state, read fresh. Dropping the earlier
// notification loses nothing a client could have acted on, and §7.2 blesses
// the practice for the equivalent push channel ("multiple changes to be
// coalesced into a single minimal StateChange").
//
// The RFC's own §7.1.1 example is a coalesced one: "the server has
// amalgamated a few changes together across two different accounts".
//
// Coalescing here is structural rather than timer-based: each subscriber
// holds a ONE-slot mailbox per account. A notification fills the slot; a
// second notification arriving before the subscriber drained the first
// overwrites it. A burst of a hundred mailbox passes becomes one wake-up
// carrying the newest moment, with no window to tune and no timer to leak.
//
// # Never blocking the sync engine
//
// notify() is called from inside the sync engine's hot paths — the same
// discipline as WatcherOptions.OnEvent ("it must not block"). Every send
// here is a non-blocking store into a slot that is always writable, so a
// subscriber that stopped reading (a browser tab frozen behind a suspended
// laptop) can never stall an incremental pass. Drop-oldest is the correct
// loss policy for a snapshot: the newest notification subsumes every one it
// displaces.

// StateChange is one push notification: the account whose data changed, and
// when the change was observed.
//
// It deliberately carries no state strings; see the package comment. The
// timestamp is for observability (measuring push latency end to end), not
// for ordering — a subscriber acts on the fact that something changed.
type StateChange struct {
	// AccountID is the account whose data changed.
	AccountID int64

	// At is when the change was observed by the sync engine.
	At time.Time
}

// Broker fans state-change notifications out to per-account subscribers.
//
// The zero value is not usable; call NewBroker. A nil *Broker is safe to
// call Notify on, which is what makes the hook at every publisher a plain
// one-liner with no enabled/disabled branch: a daemon with no JMAP server
// wires no broker and pays nothing.
type Broker struct {
	mu     sync.Mutex
	subs   map[int64]map[*subscription]struct{}
	closed bool

	// now is the clock, injectable for tests.
	now func() time.Time
}

// subscription is one subscriber's one-slot mailbox for one account.
//
// The slot is a buffered channel of capacity 1: a channel is what a caller
// selects on alongside its request context, and the select is the whole
// point — an SSE handler waits on (a change, the client going away, the
// server shutting down) in one statement.
//
// The channel is CLOSED to signal the end of the subscription, and a close
// racing a concurrent send would panic. sendMu serializes the two: both
// deliver and finish take it, so a send never overlaps the close, and closed
// tells deliver to stop before it touches the channel at all. The lock is
// held only across a non-blocking send, so a stuck subscriber still cannot
// delay a publisher.
type subscription struct {
	ch chan StateChange

	sendMu sync.Mutex
	closed bool
}

// NewBroker builds a broker.
func NewBroker() *Broker {
	return &Broker{
		subs: make(map[int64]map[*subscription]struct{}),
		now:  time.Now,
	}
}

// StateEvents subscribes to one account's state changes (the contract named
// in L2-jmap-write §4: "el broker vive en internal/sync ... con interfaz
// StateEvents(accountID) <-chan StateChange").
//
// The returned channel delivers coalesced notifications and is CLOSED when
// the subscription ends — either because the caller invoked cancel, or
// because the broker was closed for shutdown. A receiver therefore learns
// about shutdown from the same select it already waits on, with no second
// signal to plumb.
//
// cancel is idempotent and must be called (defer it) or the subscription
// leaks for the process's lifetime.
func (b *Broker) StateEvents(accountID int64) (<-chan StateChange, func()) {
	sub := &subscription{ch: make(chan StateChange, 1)}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		// A subscription taken after shutdown gets an already-closed channel
		// rather than an error: the caller's select then sees the same
		// "stream is over" it would have seen a microsecond later anyway,
		// and there is no new failure mode for a handler to get wrong.
		sub.finish()
		return sub.ch, func() {}
	}
	if b.subs[accountID] == nil {
		b.subs[accountID] = make(map[*subscription]struct{})
	}
	b.subs[accountID][sub] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			if set := b.subs[accountID]; set != nil {
				delete(set, sub)
				if len(set) == 0 {
					// Reaping the empty account bucket keeps the map's size
					// proportional to CONNECTED accounts rather than to every
					// account that ever connected.
					delete(b.subs, accountID)
				}
			}
			b.mu.Unlock()
			sub.finish()
		})
	}
	return sub.ch, cancel
}

// Subscribers reports how many live subscriptions an account has. It exists
// for the connection cap and for metrics; both need the number, neither
// needs the set.
func (b *Broker) Subscribers(accountID int64) int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs[accountID])
}

// Notify publishes a state change for one account.
//
// It NEVER blocks and never fails: it is called from the sync engine's
// committed-write paths, where the change is already durable and a push is
// an optimization on top of it. A subscriber that is not keeping up has its
// pending notification replaced by this newer one (see the package comment
// on coalescing).
//
// A nil receiver is a no-op, which is what lets every publisher call it
// unconditionally.
func (b *Broker) Notify(accountID int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	// The set is copied under the lock and delivered outside it. Delivery is
	// a non-blocking channel send, so holding the lock across it would be
	// safe — but copying keeps the lock's critical section independent of
	// how many subscribers exist, which is the property that matters when
	// this is called from an incremental pass.
	subs := make([]*subscription, 0, len(b.subs[accountID]))
	for sub := range b.subs[accountID] {
		subs = append(subs, sub)
	}
	b.mu.Unlock()

	if len(subs) == 0 {
		return
	}
	change := StateChange{AccountID: accountID, At: b.now()}
	for _, sub := range subs {
		sub.deliver(change)
	}
}

// Close ends every subscription and refuses new ones. It is the clean
// shutdown path: every SSE handler's select sees its channel close and
// finishes its response, rather than being cut off mid-stream when the
// process exits.
func (b *Broker) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	var all []*subscription
	for _, set := range b.subs {
		for sub := range set {
			all = append(all, sub)
		}
	}
	b.subs = make(map[int64]map[*subscription]struct{})
	b.mu.Unlock()

	for _, sub := range all {
		sub.finish()
	}
}

// deliver stores a change in the subscription's one slot, coalescing with
// whatever is already there.
//
// The drain-then-send pair is the drop-OLDEST policy. A plain non-blocking
// send would be drop-NEWEST, which is the wrong way round for a snapshot:
// it would leave the subscriber holding a stale notification while the fresh
// one — the one whose state read would return current data — is discarded.
func (s *subscription) deliver(change StateChange) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.closed {
		return
	}

	select {
	case s.ch <- change:
		return
	default:
	}

	// The slot was full: drop the pending (older) notification and take its
	// place. The receive is non-blocking because the subscriber may have
	// drained the slot between the failed send and here; the send that
	// follows cannot block, since this goroutine holds the only lock that
	// permits a send and the slot is now empty.
	select {
	case <-s.ch:
	default:
	}
	select {
	case s.ch <- change:
	default:
	}
}

// finish closes the subscription exactly once, without ever racing a send.
func (s *subscription) finish() {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}
