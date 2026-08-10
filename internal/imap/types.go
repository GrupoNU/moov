package imap

import (
	"context"
	"io"
	"time"
)

// This file holds every type that crosses the package boundary. None of them
// embeds, aliases or exposes a go-imap type: that is the whole point of the
// architecture rule in doc.go. When a go-imap type is needed internally it is
// converted at the edge, in convert.go.

// UID is an IMAP unique identifier, unique within a (mailbox, UIDVALIDITY)
// pair and never reused while UIDVALIDITY holds (RFC 3501 §2.3.1.1).
type UID uint32

// ModSeq is a CONDSTORE modification sequence (RFC 7162). It increases
// monotonically per mailbox on every change; the sync engine stores the
// highest one it has seen and asks the server for everything above it.
type ModSeq uint64

// MailboxRole is the SPECIAL-USE role of a mailbox (RFC 6154), normalized to
// Moov's own vocabulary. RoleNone means an ordinary user folder.
type MailboxRole string

// The SPECIAL-USE roles Moov recognizes. Dovecot reports these as attributes
// on the LIST response.
const (
	RoleNone    MailboxRole = ""
	RoleInbox   MailboxRole = "inbox"
	RoleArchive MailboxRole = "archive"
	RoleDrafts  MailboxRole = "drafts"
	RoleJunk    MailboxRole = "junk"
	RoleSent    MailboxRole = "sent"
	RoleTrash   MailboxRole = "trash"
	RoleAll     MailboxRole = "all"
	RoleFlagged MailboxRole = "flagged"
)

// MailboxInfo describes one mailbox as returned by ListMailboxes.
//
// The STATUS fields are populated in the same round trip via LIST-STATUS,
// which our Dovecot advertises (S2 T2a); without it they stay zero and
// HasStatus is false.
type MailboxInfo struct {
	// Name is the mailbox name in UTF-8, already decoded from modified UTF-7.
	Name string

	// Delimiter is the hierarchy separator, typically "/" on Dovecot.
	Delimiter string

	// Role is the SPECIAL-USE role, or RoleNone.
	Role MailboxRole

	// Subscribed reports whether the mailbox is subscribed.
	Subscribed bool

	// NoSelect is set for a mailbox that exists only as a parent node and
	// cannot be selected. The sync engine skips these.
	NoSelect bool

	// HasStatus reports whether the STATUS fields below were returned.
	HasStatus bool

	NumMessages   uint32
	NumUnseen     uint32
	UIDNext       UID
	UIDValidity   uint32
	HighestModSeq ModSeq
	SizeBytes     int64
}

// SelectResult is the outcome of SelectQResync.
type SelectResult struct {
	// UIDValidity of the mailbox as the server reports it now. If it differs
	// from the value passed in, the server ignored the QRESYNC parameter and
	// the caller must invalidate its local state for this mailbox and resync
	// from scratch (L2 §2.5).
	UIDValidity uint32

	// UIDValidityChanged is the pre-computed answer to that question, so no
	// caller has to remember to compare.
	UIDValidityChanged bool

	// HighestModSeq is the mailbox's current highest modification sequence.
	HighestModSeq ModSeq

	// UIDNext is the UID the next appended message will get.
	UIDNext UID

	// NumMessages is the message count reported by EXISTS.
	NumMessages uint32

	// VanishedUIDs are the messages expunged since the modseq passed in,
	// delivered by the server as VANISHED (EARLIER) during the SELECT
	// (S2 T1 and T3). Empty when the caller synced from scratch.
	VanishedUIDs []UID

	// ReadOnly reports a mailbox selected as EXAMINE / [READ-ONLY].
	ReadOnly bool
}

// FetchSpec selects what FetchMessages retrieves.
type FetchSpec struct {
	// Body requests the complete raw message (RFC822), streamed. When false
	// only envelope-level data and the header block are fetched, which is what
	// the initial listing pass needs.
	Body bool

	// Headers requests the full header block as raw bytes. Moov parses headers
	// itself (internal/parser) rather than trusting the server's ENVELOPE.
	Headers bool

	// Flags requests the message's flags and keywords.
	Flags bool

	// InternalDate requests the server's internal date.
	InternalDate bool

	// Size requests RFC822.SIZE.
	Size bool

	// ChangedSince, when non-zero, restricts the fetch to messages whose
	// modseq is greater than it (CONDSTORE, RFC 7162).
	ChangedSince ModSeq

	// Vanished, valid only together with ChangedSince on a QRESYNC-enabled
	// connection, asks the server to also report expunged messages as
	// VANISHED (EARLIER) instead of leaving the caller to infer them.
	Vanished bool
}

// Message is one message delivered by a fetch iterator.
//
// # Lifetime
//
// Body, when non-nil, is a reader over the connection itself, not a buffer:
// the whole point is that a 40 MB message never becomes a 40 MB allocation.
// It is only valid until the iterator advances. A consumer that needs the
// bytes later must copy them (typically straight into the blob store) before
// calling Next again. After the iterator advances, reads return an error.
type Message struct {
	// UID identifies the message within the selected mailbox.
	UID UID

	// SeqNum is the message sequence number in the current session. It is
	// unstable across expunges and is exposed only for diagnostics.
	SeqNum uint32

	// ModSeq is the message's modification sequence, present when the mailbox
	// supports CONDSTORE.
	ModSeq ModSeq

	// Flags are the system flags, normalized to lowercase without the leading
	// backslash: "seen", "answered", "flagged", "deleted", "draft".
	Flags []string

	// Keywords are the non-system flags, i.e. the user keywords Moov uses for
	// labels (A6). Case is preserved as the server reports it.
	Keywords []string

	// InternalDate is the server's arrival timestamp, zero when not requested.
	InternalDate time.Time

	// Size is RFC822.SIZE in bytes, zero when not requested.
	Size int64

	// Header is the raw header block, present when FetchSpec.Headers was set.
	// It is buffered because headers are bounded and every consumer needs the
	// whole block; unlike Body it stays valid after the iterator advances.
	Header []byte

	// Body streams the complete raw message when FetchSpec.Body was set.
	// See the lifetime note on the type.
	Body io.Reader
}

// MessageIter is a streaming cursor over fetched messages.
//
// It is a pull iterator rather than a channel so that backpressure is
// automatic: the network read only advances when the consumer asks for the
// next message, which is what keeps a 500k-message backfill inside a bounded
// memory budget.
//
// Usage:
//
//	it, err := c.FetchMessages(ctx, uids, spec)
//	if err != nil { return err }
//	defer it.Close()
//	for {
//	    msg, err := it.Next()
//	    if err != nil { return err }
//	    if msg == nil { break }
//	    // consume msg (and msg.Body) before the next Next
//	}
//	return it.Close()
//
// Close must always be called; it releases the underlying command and is the
// only place a protocol-level error is guaranteed to surface. Calling it more
// than once is safe.
type MessageIter interface {
	// Next returns the next message, or nil when the fetch is exhausted.
	// A non-nil error ends the iteration.
	Next() (*Message, error)

	// Close finishes the command and returns the first error seen.
	Close() error
}

// ChangeIter is the iterator returned by FetchChanges. It carries both sides
// of a delta: the messages that changed and the ones that vanished.
//
// Vanished is only complete once the iteration has finished, because the
// server may interleave VANISHED responses with the FETCH ones. Callers should
// drain Next to nil, call Close, and only then read Vanished.
type ChangeIter interface {
	MessageIter

	// Vanished returns the UIDs the server reported as expunged. Valid after
	// the iteration completes.
	Vanished() []UID
}

// FlagOp is the operation StoreFlags performs.
type FlagOp int

// The three STORE operations of RFC 3501 §6.4.6.
const (
	// FlagsAdd adds the given flags, leaving the others untouched (+FLAGS).
	FlagsAdd FlagOp = iota
	// FlagsRemove removes the given flags (-FLAGS).
	FlagsRemove
	// FlagsSet replaces the message's flags with exactly the given set
	// (FLAGS). It clobbers flags set by other clients and is rarely what the
	// sync engine wants.
	FlagsSet
)

// String implements fmt.Stringer.
func (op FlagOp) String() string {
	switch op {
	case FlagsAdd:
		return "add"
	case FlagsRemove:
		return "remove"
	case FlagsSet:
		return "set"
	default:
		return "unknown"
	}
}

// FlagDelta is one flag mutation to apply to a set of messages.
type FlagDelta struct {
	// Op is the operation to perform.
	Op FlagOp

	// Flags are system flags in Moov's normalized form ("seen", "flagged", …)
	// and/or user keywords. Both are accepted in the same slice; the
	// conversion layer re-attaches the backslash to the system ones.
	Flags []string
}

// StoreResult reports the outcome of StoreFlags.
//
// The important field is Rejected. A conditional STORE that the server refuses
// still completes with a tagged OK and no error (S2 H6): the failure is
// reported only in the [MODIFIED] response code. Treating a nil error as
// success would silently drop the write and leave Moov's flag state diverged
// from Dovecot's — exactly the corruption the L2 warns about.
type StoreResult struct {
	// Updated are the UIDs the server confirmed as changed.
	Updated []UID

	// Rejected are the UIDs the server refused because they changed since
	// UnchangedSince. Non-empty means an optimistic-concurrency conflict: the
	// caller must re-read those messages and retry with a fresh modseq.
	Rejected []UID

	// HighestModSeq is the mailbox's modseq after the store, when reported.
	HighestModSeq ModSeq

	// VerifiedByReadBack reports how Rejected was determined. True means the
	// client had to re-read the flags to find out, because the server or the
	// library did not surface [MODIFIED]; false means [MODIFIED] was used
	// directly. It exists so the sync engine can emit a metric and so this
	// safety net can eventually be retired with evidence rather than by
	// assumption (L2 §2.5).
	VerifiedByReadBack bool
}

// Conflicted reports whether the server refused part of the write.
func (r StoreResult) Conflicted() bool { return len(r.Rejected) > 0 }

// EventKind classifies a Watch event.
type EventKind int

// The event kinds a watcher emits.
const (
	// EventMailboxChanged means "something changed in this mailbox".
	//
	// It carries no detail on purpose: for a non-selected mailbox Dovecot
	// collapses every event class into a single STATUS response, and it
	// rejects the NOTIFY MessageNew (fetch-att) form that would carry the
	// data (S2 T2d). NOTIFY is notification-only here, so the consumer always
	// answers with a batched FETCH against its own state.
	EventMailboxChanged EventKind = iota

	// EventOverflow means the server sent NOTIFICATIONOVERFLOW: it gave up on
	// tracking and the watch is no longer authoritative. The only correct
	// response is a full resync of the account (L2 §2.5).
	EventOverflow
)

// String implements fmt.Stringer.
func (k EventKind) String() string {
	switch k {
	case EventMailboxChanged:
		return "mailbox-changed"
	case EventOverflow:
		return "overflow"
	default:
		return "unknown"
	}
}

// Event is one notification from a watcher.
type Event struct {
	// Kind is what happened.
	Kind EventKind

	// Mailbox is the affected mailbox, set for EventMailboxChanged and empty
	// for EventOverflow, which is account-wide.
	Mailbox string

	// Status carries whatever counters the server included in the STATUS
	// response. With the patched NOTIFY encoder this includes HIGHESTMODSEQ,
	// which is what makes a pure flag change visible at all (S2 T4): a
	// \Flagged toggle changes neither MESSAGES nor UNSEEN, so the modseq is
	// the only signal.
	Status EventStatus

	// At is when the client observed the event.
	At time.Time
}

// EventStatus holds the counters of a NOTIFY-induced STATUS response. Each
// field has a Has* companion because zero is a meaningful value for all of
// them and the server sends only what changed.
type EventStatus struct {
	NumMessages      uint32
	HasNumMessages   bool
	NumUnseen        uint32
	HasNumUnseen     bool
	UIDNext          UID
	HasUIDNext       bool
	HighestModSeq    ModSeq
	HasHighestModSeq bool
}

// WatchSpec configures a watcher.
type WatchSpec struct {
	// Mailboxes, when non-empty, restricts the watch to these mailboxes
	// (NOTIFY SET … MAILBOXES (…)). Empty means the whole personal namespace
	// (NOTIFY SET STATUS (PERSONAL …)), which is what the sync engine uses:
	// one connection covers every folder of the account (S2 T2d).
	Mailboxes []string

	// BufferSize is the capacity of the event channel. Default 64. Events are
	// dropped rather than blocking the protocol reader if the consumer stalls;
	// a dropped EventMailboxChanged is harmless because the reconciler catches
	// it, which is why dropping beats deadlocking the connection.
	BufferSize int
}

// Annotation is one IMAP METADATA entry (RFC 5464).
type Annotation struct {
	// Name is the full entry name, e.g. "/private/vendor/moov/labels".
	Name string

	// Value is the entry's raw content. A nil Value means the entry does not
	// exist, which METADATA distinguishes from an empty one.
	Value []byte
}

// MetadataOps is the METADATA surface used for label definitions (A6): the
// mapping from IMAP keyword to label name, color and order lives in a private
// annotation on the account's root mailbox, so it is reconstructible from
// Dovecot like everything else.
type MetadataOps interface {
	// Get reads the given entries of a mailbox. An empty mailbox name means
	// server-level metadata. Entries that do not exist come back with a nil
	// Value rather than being omitted, so a caller can tell "absent" from
	// "empty".
	Get(ctx context.Context, mailbox string, entries []string) ([]Annotation, error)

	// Set writes entries. An Annotation with a nil Value deletes the entry,
	// which is how RFC 5464 expresses removal.
	Set(ctx context.Context, mailbox string, entries []Annotation) error
}
