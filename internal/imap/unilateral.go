package imap

import (
	"sort"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// go-imap delivers everything the server volunteers — VANISHED, STATUS,
// NOTIFICATIONOVERFLOW — through a single handler struct installed at
// connection time. That handler runs on the library's decoder goroutine, so
// every callback here must be short and must never issue a command: doing so
// would deadlock the connection it is being called from.
//
// The pattern used throughout is a small piece of state guarded by cl.mu that
// a command "arms" before issuing and "disarms" afterwards, so the callbacks
// know which in-flight operation, if any, wants the data.

// unilateralState is the mutable target of the callbacks.
type unilateralState struct {
	// vanished accumulates UIDs from VANISHED responses while a collector is
	// armed. nil means nobody is listening and the data is dropped.
	vanished []UID
	// vanishedArmed distinguishes "armed, nothing yet" from "not armed".
	vanishedArmed bool
}

// unilateralHandler builds the handler installed on the connection.
func (cl *client) unilateralHandler() *imapclient.UnilateralDataHandler {
	return &imapclient.UnilateralDataHandler{
		Vanished: func(uids goimap.UIDSet, earlier bool) {
			expanded, truncated := uidsFromUIDSet(uids, maxVanishedUIDs)
			if truncated {
				cl.log.Warn("imap: unilateral VANISHED set too large to expand", "earlier", earlier)
			}
			cl.mu.Lock()
			if cl.uni.vanishedArmed {
				cl.uni.vanished = append(cl.uni.vanished, expanded...)
			}
			cl.mu.Unlock()
		},

		Status: func(data *goimap.StatusData) {
			// A STATUS arriving unsolicited is a NOTIFY notification: the
			// server saying "this mailbox changed". With the patched encoder
			// it carries HIGHESTMODSEQ, which for a pure flag change is the
			// only field that moves (S2 T4).
			cl.emitEvent(Event{
				Kind:    EventMailboxChanged,
				Mailbox: data.Mailbox,
				Status:  eventStatusFromGoIMAP(data),
			})
		},

		List: func(data *goimap.ListData) {
			// NOTIFY MailboxName events arrive as unsolicited LIST responses:
			// a mailbox was created, deleted, renamed or (un)subscribed. The
			// consumer's answer is the same as for any other change — re-read
			// the tree — so it is reported as a change on that mailbox.
			cl.emitEvent(Event{
				Kind:    EventMailboxChanged,
				Mailbox: data.Mailbox,
			})
		},

		NotificationOverflow: func() {
			// The server gave up tracking. Everything the watcher believes is
			// now suspect and only a full resync restores the invariant
			// (L2 §2.5).
			cl.log.Warn("imap: NOTIFICATIONOVERFLOW; the watch is no longer authoritative")
			cl.emitEvent(Event{Kind: EventOverflow})
		},
	}
}

// armVanishedCollector starts collecting VANISHED UIDs and returns the
// accessor that stops collecting and yields what arrived.
//
// It exists because VANISHED for a QRESYNC SELECT reaches the client by two
// routes at once in the patched library: SelectData.VanishedUIDs and this
// handler. Collecting both and de-duplicating is cheaper than depending on
// which one upstream keeps.
func (cl *client) armVanishedCollector() func() []UID {
	cl.mu.Lock()
	cl.uni.vanished = nil
	cl.uni.vanishedArmed = true
	cl.mu.Unlock()

	return func() []UID {
		cl.mu.Lock()
		defer cl.mu.Unlock()
		out := cl.uni.vanished
		cl.uni.vanished = nil
		return out
	}
}

// disarmVanishedCollector stops collecting. It is idempotent and safe to defer
// alongside the accessor returned by armVanishedCollector.
func (cl *client) disarmVanishedCollector() {
	cl.mu.Lock()
	cl.uni.vanishedArmed = false
	cl.uni.vanished = nil
	cl.mu.Unlock()
}

// emitEvent delivers an event to the live watcher, if any.
//
// Delivery is non-blocking. This runs on the library's decoder goroutine: if
// it blocked on a full channel, the connection would stop reading and the
// watch would wedge. Dropping an EventMailboxChanged is recoverable — the
// reconciler pass exists precisely to catch what a watcher missed (L2 §2.5) —
// whereas a wedged connection is not, so the trade is one-sided.
//
// EventOverflow is the exception and is never dropped silently: losing it
// would leave the engine believing a watch that the server has abandoned.
func (cl *client) emitEvent(ev Event) {
	ev.At = timeNow()

	cl.mu.Lock()
	w := cl.watch
	cl.mu.Unlock()
	if w == nil {
		return
	}

	select {
	case w.events <- ev:
	default:
		if ev.Kind == EventOverflow {
			// Force it through on a dedicated goroutine rather than dropping
			// it. The watcher drains its channel on shutdown, so this cannot
			// leak past the end of the watch.
			go func() {
				select {
				case w.events <- ev:
				case <-w.done:
				}
			}()
			return
		}
		w.dropped.Add(1)
		cl.log.Debug("imap: event channel full, dropped event; the reconciler will catch it",
			"mailbox", ev.Mailbox)
	}
}

// sortUIDs sorts a UID slice ascending.
func sortUIDs(uids []UID) {
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
}
