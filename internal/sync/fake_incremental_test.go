package sync

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
)

// E6's extensions to the fake server: modseq bookkeeping, expunge with a
// VANISHED trail, and a watcher that emits events.
//
// The reason these live in the fake rather than only in the integration suite
// is the same reason E5 built the fake at all: the properties E6 must guarantee
// are properties of ORDERING under concurrency — a flag change and an expunge
// arriving in the same delta, an overflow interrupting a burst, a breaker
// counting failures across reconnections. Against a real server those are
// timing-dependent and therefore either flaky or untestable; here they are
// exact, because the test decides when each event happens.
//
// The integration suite then proves the same paths against Dovecot, where the
// wire format and the server's own choices are what is under test.

// nextModSeq assigns the mailbox's next modification sequence.
//
// CONDSTORE requires it to be strictly increasing per mailbox on every change
// (RFC 7162), which is the property the whole incremental path rests on: the
// engine asks for "everything above N" and trusts that nothing at or below N
// changed. A fake that reused a modseq would let a test pass that a real server
// would fail.
func (m *fakeMailbox) nextModSeq() imap.ModSeq {
	m.highestModSeq++
	return m.highestModSeq
}

// appendMessage adds a message to the mailbox as a delivery would, assigning
// the next UID and modseq. The caller holds the server lock.
func (m *fakeMailbox) appendMessage(raw []byte, flags []string, internalDate time.Time) imap.UID {
	uid := m.uidNext()
	m.messages = append(m.messages, fakeMessage{
		uid:          uid,
		raw:          raw,
		flags:        flags,
		internalDate: internalDate,
		modSeq:       m.nextModSeq(),
	})
	return uid
}

// setFlags replaces a message's flags, bumping its modseq the way a real STORE
// does.
func (m *fakeMailbox) setFlags(uid imap.UID, flags, keywords []string) bool {
	msg := m.find(uid)
	if msg == nil {
		return false
	}
	msg.flags = flags
	msg.keywords = keywords
	msg.modSeq = m.nextModSeq()
	return true
}

// expunge removes a message and records it in the vanished trail.
//
// The trail is what QRESYNC replays: a client reconnecting with an old modseq
// is told which UIDs disappeared while it was away. Keeping it keyed by modseq
// is what makes "vanished since N" answerable, which is exactly the question
// SelectQResync asks.
func (m *fakeMailbox) expunge(uid imap.UID) bool {
	for i := range m.messages {
		if m.messages[i].uid != uid {
			continue
		}
		m.messages = append(m.messages[:i], m.messages[i+1:]...)
		m.vanished = append(m.vanished, vanishedRecord{uid: uid, modSeq: m.nextModSeq()})
		return true
	}
	return false
}

// vanishedSince returns the UIDs expunged after the given modseq.
func (m *fakeMailbox) vanishedSince(since imap.ModSeq) []imap.UID {
	var out []imap.UID
	for _, v := range m.vanished {
		if v.modSeq > since {
			out = append(out, v.uid)
		}
	}
	return out
}

// changedSince returns the messages whose modseq is above the cursor, ascending
// by UID.
func (m *fakeMailbox) changedSince(since imap.ModSeq) []fakeMessage {
	var out []fakeMessage
	for _, msg := range m.messages {
		if msg.modSeq > since {
			out = append(out, msg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].uid < out[j].uid })
	return out
}

// vanishedRecord is one expunge in a mailbox's history.
type vanishedRecord struct {
	uid    imap.UID
	modSeq imap.ModSeq
}

// ---------------------------------------------------------------------------
// server-side mutation helpers, as a test script uses them
// ---------------------------------------------------------------------------

// deliver appends a message to a mailbox and notifies any watcher, which is
// what an SMTP delivery looks like from IMAP's side.
func (s *fakeServer) deliver(mailbox string, raw []byte, flags []string, at time.Time) imap.UID {
	s.mu.Lock()
	mb := s.mailbox(mailbox)
	if mb == nil {
		s.mu.Unlock()
		panic("fake: deliver to unknown mailbox " + mailbox)
	}
	uid := mb.appendMessage(raw, flags, at)
	status := mb.statusFor()
	s.mu.Unlock()

	s.notify(mailbox, status)
	return uid
}

// setFlags changes a message's flags, as another client would.
func (s *fakeServer) setFlags(mailbox string, uid imap.UID, flags, keywords []string) {
	s.mu.Lock()
	mb := s.mailbox(mailbox)
	if mb == nil {
		s.mu.Unlock()
		panic("fake: setFlags on unknown mailbox " + mailbox)
	}
	ok := mb.setFlags(uid, flags, keywords)
	status := mb.statusFor()
	s.mu.Unlock()

	if !ok {
		panic(fmt.Sprintf("fake: setFlags on unknown uid %d", uid))
	}
	s.notify(mailbox, status)
}

// expunge removes a message, as another client would.
func (s *fakeServer) expunge(mailbox string, uid imap.UID) {
	s.mu.Lock()
	mb := s.mailbox(mailbox)
	if mb == nil {
		s.mu.Unlock()
		panic("fake: expunge on unknown mailbox " + mailbox)
	}
	ok := mb.expunge(uid)
	status := mb.statusFor()
	s.mu.Unlock()

	if !ok {
		panic(fmt.Sprintf("fake: expunge of unknown uid %d", uid))
	}
	s.notify(mailbox, status)
}

// statusFor builds the STATUS counters a NOTIFY notification carries. The
// caller holds the server lock.
//
// HIGHESTMODSEQ is included because that is what the PATCHED encoder gets from
// Dovecot (S2 T4), and it is the only counter that moves for a pure flag
// change. A fake that omitted it would make the flag-change path look untested
// when it is precisely the path the patch exists for.
func (m *fakeMailbox) statusFor() imap.EventStatus {
	return imap.EventStatus{
		NumMessages:      uint32(len(m.messages)),
		HasNumMessages:   true,
		UIDNext:          m.uidNext(),
		HasUIDNext:       true,
		HighestModSeq:    m.highestModSeq,
		HasHighestModSeq: true,
	}
}

// notify delivers an event to every live watcher.
func (s *fakeServer) notify(mailbox string, status imap.EventStatus) {
	s.mu.Lock()
	watchers := append([]*fakeWatch(nil), s.watchers...)
	silent := s.silentNotify
	s.mu.Unlock()

	if silent {
		// Modeling a lost notification, which is what the reconciler exists to
		// catch: the mutation happens, no event is delivered.
		return
	}

	for _, w := range watchers {
		w.send(imap.Event{
			Kind:    imap.EventMailboxChanged,
			Mailbox: mailbox,
			Status:  status,
			At:      time.Now(),
		})
	}
}

// overflow delivers NOTIFICATIONOVERFLOW to every watcher.
func (s *fakeServer) overflow() {
	s.mu.Lock()
	watchers := append([]*fakeWatch(nil), s.watchers...)
	s.mu.Unlock()

	for _, w := range watchers {
		w.send(imap.Event{Kind: imap.EventOverflow, At: time.Now()})
	}
}

// setSilentNotify makes mutations produce no events, so a test can create a
// divergence behind the watcher's back — which is exactly the E6 acceptance
// criterion for the reconciler.
func (s *fakeServer) setSilentNotify(silent bool) {
	s.mu.Lock()
	s.silentNotify = silent
	s.mu.Unlock()
}

// breakWatchers ends every live watch, as a dropped connection would.
func (s *fakeServer) breakWatchers() {
	s.mu.Lock()
	watchers := append([]*fakeWatch(nil), s.watchers...)
	s.watchers = nil
	s.mu.Unlock()

	for _, w := range watchers {
		w.close()
	}
}

// ---------------------------------------------------------------------------
// the watch itself
// ---------------------------------------------------------------------------

// fakeWatch is one live watcher's channel.
type fakeWatch struct {
	ch   chan imap.Event
	once sync.Once
	done chan struct{}
}

func newFakeWatch(buffer int) *fakeWatch {
	if buffer <= 0 {
		buffer = 64
	}
	return &fakeWatch{ch: make(chan imap.Event, buffer), done: make(chan struct{})}
}

// send delivers an event, dropping it if the consumer is behind — the same
// trade the real client makes, and for the same reason: blocking here would
// wedge the connection, while a dropped event is recovered by the reconciler.
func (w *fakeWatch) send(ev imap.Event) {
	select {
	case <-w.done:
	case w.ch <- ev:
	default:
	}
}

func (w *fakeWatch) close() {
	w.once.Do(func() {
		close(w.done)
		close(w.ch)
	})
}

// Watch implements the client side of the watcher seam.
func (c *fakeClient) watch(ctx context.Context, spec imap.WatchSpec) (<-chan imap.Event, error) {
	c.srv.mu.Lock()
	if c.srv.watchErr != nil {
		err := c.srv.watchErr
		c.srv.mu.Unlock()
		return nil, err
	}
	w := newFakeWatch(spec.BufferSize)
	c.srv.watchers = append(c.srv.watchers, w)
	c.srv.mu.Unlock()

	// The watch ends with the context, exactly as the real one does, so a
	// canceled session releases the connection instead of leaking a goroutine.
	go func() {
		select {
		case <-ctx.Done():
			c.srv.removeWatcher(w)
			w.close()
		case <-w.done:
		}
	}()

	return w.ch, nil
}

// removeWatcher detaches a watch from the server.
func (s *fakeServer) removeWatcher(target *fakeWatch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.watchers[:0]
	for _, w := range s.watchers {
		if w != target {
			out = append(out, w)
		}
	}
	s.watchers = out
}

// watcherCount reports how many watches are live, which is what proves the
// "one connection per account" claim rather than assuming it.
func (s *fakeServer) watcherCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.watchers)
}
