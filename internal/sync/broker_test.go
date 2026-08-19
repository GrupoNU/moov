package sync

import (
	"sync"
	"testing"
	"time"
)

// Broker tests (W4a). The properties that matter are the three the sync
// engine's correctness depends on: a burst coalesces, a stalled subscriber
// never blocks a publisher (and keeps the NEWEST notification, not the
// oldest), and cancel/Close release everything without racing a publisher.

func TestBrokerDeliversToSubscriber(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	defer b.Close()

	events, cancel := b.StateEvents(7)
	defer cancel()

	b.Notify(7)

	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatal("channel closed instead of delivering")
		}
		if ev.AccountID != 7 {
			t.Fatalf("account id = %d, want 7", ev.AccountID)
		}
		if ev.At.IsZero() {
			t.Error("timestamp is zero; the broker must stamp the observation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no notification delivered")
	}
}

func TestBrokerIsolatesAccounts(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	defer b.Close()

	seven, cancelSeven := b.StateEvents(7)
	defer cancelSeven()
	eight, cancelEight := b.StateEvents(8)
	defer cancelEight()

	b.Notify(8)

	select {
	case <-eight:
	case <-time.After(2 * time.Second):
		t.Fatal("account 8 got no notification")
	}

	// An account's subscriber must never see another account's change: that
	// would leak the existence of activity in a mailbox the caller does not
	// own, and would wake every browser on every account's every change.
	select {
	case ev := <-seven:
		t.Fatalf("account 7 received a notification for another account: %+v", ev)
	default:
	}
}

// TestBrokerCoalescesBurst is the coalescing claim of W-A4: a burst to one
// account collapses to a single wake-up carrying the LATEST moment, because a
// §7.1 payload is a state snapshot rather than a diff.
func TestBrokerCoalescesBurst(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	defer b.Close()

	// A deterministic clock: each Notify advances it by a second, so the
	// delivered event's timestamp identifies WHICH notification survived.
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	b.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Second)
		return now
	}

	events, cancel := b.StateEvents(1)
	defer cancel()

	// The subscriber does not read while the burst is published.
	const burst = 100
	for range burst {
		b.Notify(1)
	}

	// Exactly one notification is pending...
	var got StateChange
	select {
	case got = <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("no notification after the burst")
	}

	// ...and it is the LAST one, not the first. Drop-oldest is what makes the
	// surviving notification point at current state; drop-newest would leave
	// the subscriber holding a stale wake-up.
	want := time.Unix(1_700_000_000, 0).Add(burst * time.Second)
	if !got.At.Equal(want) {
		t.Errorf("coalesced event carries %v, want the newest %v", got.At, want)
	}

	select {
	case extra := <-events:
		t.Fatalf("burst of %d produced more than one notification: %+v", burst, extra)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestBrokerNeverBlocksPublisher is the discipline the hook points depend on:
// Notify is called from inside an incremental pass, so a subscriber that has
// stopped reading must not be able to stall the sync engine.
func TestBrokerNeverBlocksPublisher(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	defer b.Close()

	// Subscribe and never read.
	_, cancel := b.StateEvents(42)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 10_000 {
			b.Notify(42)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Notify blocked on a subscriber that stopped reading")
	}
}

func TestBrokerCancelStopsDelivery(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	defer b.Close()

	events, cancel := b.StateEvents(3)

	if got := b.Subscribers(3); got != 1 {
		t.Fatalf("Subscribers = %d, want 1", got)
	}

	cancel()

	if got := b.Subscribers(3); got != 0 {
		t.Errorf("Subscribers after cancel = %d, want 0", got)
	}

	// The channel is closed, which is how a streaming handler learns its
	// subscription ended.
	select {
	case _, ok := <-events:
		if ok {
			t.Error("a value arrived after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the channel was not closed by cancel")
	}

	// A notification after cancel reaches nobody and must not panic on a
	// closed channel.
	b.Notify(3)
}

func TestBrokerCancelIsIdempotent(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	defer b.Close()

	_, cancel := b.StateEvents(5)
	cancel()
	cancel()
	cancel()
}

// TestBrokerCloseEndsEverySubscription covers the clean-shutdown path: every
// stream must end by itself so the HTTP server can drain instead of waiting
// out its grace period on connections that never end on their own.
func TestBrokerCloseEndsEverySubscription(t *testing.T) {
	t.Parallel()

	b := NewBroker()

	a, cancelA := b.StateEvents(1)
	defer cancelA()
	c, cancelC := b.StateEvents(2)
	defer cancelC()

	b.Close()

	for i, ch := range []<-chan StateChange{a, c} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("subscription %d delivered a value instead of closing", i)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("subscription %d was not closed by Close", i)
		}
	}

	// Close is idempotent, and Notify after Close is a no-op rather than a
	// panic — shutdown races an in-flight sync pass in production.
	b.Close()
	b.Notify(1)
}

func TestBrokerSubscribeAfterCloseYieldsClosedChannel(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.Close()

	events, cancel := b.StateEvents(9)
	defer cancel()

	select {
	case _, ok := <-events:
		if ok {
			t.Error("a post-shutdown subscription delivered a value")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a post-shutdown subscription must hand back a closed channel")
	}
}

// TestBrokerNilIsSafe is what makes every hook a plain one-liner: a daemon
// with no JMAP server wires no broker, and the sync engine calls Notify
// unconditionally.
func TestBrokerNilIsSafe(t *testing.T) {
	t.Parallel()

	var b *Broker
	b.Notify(1)
	b.Close()
	if got := b.Subscribers(1); got != 0 {
		t.Errorf("Subscribers on a nil broker = %d, want 0", got)
	}
}

// TestBrokerConcurrentSubscribeNotifyCancel is the race-detector's test: the
// production pattern is publishers inside sync passes racing subscribers that
// come and go with browser tabs.
func TestBrokerConcurrentSubscribeNotifyCancel(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	defer b.Close()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				b.Notify(1)
			}
		}()
	}

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				events, cancel := b.StateEvents(1)
				select {
				case <-events:
				default:
				}
				cancel()
			}
		}()
	}

	// Subscribers churn for a bounded time, then the publishers stop.
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestBrokerMultipleSubscribersAllReceive(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	defer b.Close()

	const n = 3
	chans := make([]<-chan StateChange, n)
	for i := range n {
		ch, cancel := b.StateEvents(11)
		defer cancel()
		chans[i] = ch
	}

	b.Notify(11)

	// Every open stream of the account gets the wake-up: two browser tabs on
	// the same mailbox must both refresh.
	for i, ch := range chans {
		select {
		case ev := <-ch:
			if ev.AccountID != 11 {
				t.Errorf("subscriber %d got account %d", i, ev.AccountID)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("subscriber %d received nothing", i)
		}
	}
}
