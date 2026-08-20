package imap

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Client is the contract of docs/specs/L2-sync-engine.md §4.1: everything the
// rest of the engine is allowed to ask of Dovecot.
//
// It is an interface rather than a concrete type for two reasons. The obvious
// one is that internal/sync can be tested against a fake. The load-bearing one
// is that it is the seam the architecture rule protects: as long as this is
// the only vocabulary the engine speaks, a go-imap bump costs one package
// (doc.go).
//
// # Concurrency
//
// An implementation is NOT safe for concurrent use. An IMAP connection is a
// single command stream with one selected mailbox, so two goroutines issuing
// commands on one Client would interleave into nonsense. The engine's model is
// a small bounded pool of Clients per account (L2 §2.5), each owned by one
// goroutine at a time. The single exception is Close, which may be called from
// another goroutine to unblock a stuck one.
//
// # Selected-mailbox state
//
// SelectQResync, FetchChanges, FetchMessages and StoreFlags all act on the
// currently selected mailbox. FetchChanges and FetchMessages return a
// streaming iterator that owns the connection until it is closed; no other
// method may be called in between.
type Client interface {
	// Connect dials the server, negotiates STARTTLS, authenticates, enables
	// QRESYNC (which implies CONDSTORE) and probes capabilities after login —
	// NOTIFY is not advertised before authentication (S2 T2a).
	//
	// It returns ErrMissingCapability if the server lacks an extension the
	// engine cannot work without.
	Connect(ctx context.Context, cfg Config) error

	// Capabilities reports the post-login capability set, lowercased. It is
	// valid only after a successful Connect.
	Capabilities() Capabilities

	// ListMailboxes returns the account's mailboxes with their SPECIAL-USE
	// roles, and their STATUS counters in the same round trip when the server
	// supports LIST-STATUS.
	ListMailboxes(ctx context.Context) ([]MailboxInfo, error)

	// SelectQResync selects a mailbox with the QRESYNC parameter, so the
	// server replays the delta since the caller's remembered state: the
	// messages expunged meanwhile arrive as VANISHED (EARLIER) in
	// SelectResult.VanishedUIDs, and the changed ones as unilateral FETCHes.
	//
	// Passing uidValidity 0 or modSeq 0 selects the mailbox plainly, which is
	// the correct call for a mailbox Moov has never synced.
	//
	// A UIDVALIDITY that no longer matches is reported in the result rather
	// than as an error: it is an expected, recoverable condition that means
	// "throw away the local state for this mailbox" (L2 §2.5).
	SelectQResync(ctx context.Context, mailbox string, uidValidity uint32, modSeq ModSeq) (SelectResult, error)

	// FetchChanges streams every message in the selected mailbox whose modseq
	// is above since, and collects the UIDs expunged meanwhile
	// (UID FETCH 1:* (FLAGS) (CHANGEDSINCE n VANISHED)).
	//
	// This is the incremental path on a live connection, as opposed to
	// SelectQResync which is the reconnection path.
	FetchChanges(ctx context.Context, since ModSeq) (ChangeIter, error)

	// FetchMessages streams the given UIDs of the selected mailbox.
	//
	// Message bodies are handed over as readers over the connection and are
	// only valid until the iterator advances: a mailbox with a 40 MB
	// attachment must not cost 40 MB of heap (see Message).
	//
	// An empty uids slice returns an empty iterator without touching the
	// network.
	FetchMessages(ctx context.Context, uids []UID, spec FetchSpec) (MessageIter, error)

	// Watch starts a watcher on this connection and returns its event channel.
	//
	// It issues NOTIFY SET STATUS (PERSONAL …) with the patched encoder and
	// then keeps the connection inside a maintenance IDLE loop, because
	// go-imap only reads the socket while a command is in flight (S2 T2d).
	// The STATUS keyword is what makes flag changes in non-selected folders
	// visible at all (S2 T4).
	//
	// The channel is closed when the watch ends, whether from ctx being
	// canceled, Close being called, or a connection error. Any error is
	// available from Err after the channel closes.
	//
	// The connection is dedicated to watching for as long as the watch runs:
	// no other method may be called on this Client until the channel closes.
	Watch(ctx context.Context, spec WatchSpec) (<-chan Event, error)

	// StoreFlags applies a flag delta to the given UIDs of the selected
	// mailbox, in batches.
	//
	// When unchangedSince is non-zero the write is conditional (RFC 7162): the
	// server applies it only to messages that have not changed since that
	// modseq, and names the ones it refused in StoreResult.Rejected. The
	// result is authoritative whether or not the server surfaced [MODIFIED],
	// because the implementation falls back to reading the flags back (S2 H6).
	//
	// A nil error does NOT mean every message was written. Check
	// StoreResult.Conflicted.
	StoreFlags(ctx context.Context, uids []UID, delta FlagDelta, unchangedSince ModSeq) (StoreResult, error)

	// Move moves the given UIDs from the selected mailbox to dest (RFC 6851).
	//
	// On a server without MOVE the equivalent COPY + STORE \Deleted +
	// UID EXPUNGE sequence of RFC 6851 §4.1 is used — but only when UIDPLUS is
	// available, because the fallback would otherwise degrade to a bare
	// EXPUNGE, which also removes messages OTHER clients marked \Deleted.
	// Rather than risk collateral expunges, a server with neither extension
	// gets ErrMissingCapability.
	//
	// MoveResult carries the COPYUID mapping (RFC 4315 §3) when the server
	// provides one, which is what lets the caller reflect the move locally
	// without re-downloading the message (W1, L2-jmap-write §4).
	Move(ctx context.Context, uids []UID, dest string) (MoveResult, error)

	// Append stores a message in a mailbox (RFC 3501 §6.3.11) and returns the
	// UID the server assigned via UIDPLUS [APPENDUID] (RFC 4315 §3), or a zero
	// UID on a server without UIDPLUS — the message was still appended.
	//
	// It does not require the mailbox to be selected and does not change the
	// selection. flags use this package's normalized vocabulary (bare system
	// flag names, user keywords verbatim), the same one StoreFlags takes.
	//
	// This is W3's write primitive: Email/set create appends assembled drafts,
	// and the outbox appends the transmitted copy to \Sent after the SMTP 250
	// (ADR §4). It joined Client for that product reason, exactly as
	// testsupport.go's note anticipated.
	Append(ctx context.Context, mailbox string, raw []byte, flags []string, internalDate time.Time) (AppendResult, error)

	// Expunge permanently removes the given UIDs from the selected mailbox:
	// STORE +FLAGS.SILENT \Deleted, then UID EXPUNGE (RFC 4315 §2.1).
	//
	// UID EXPUNGE rather than EXPUNGE is a correctness requirement, not a
	// preference: a bare EXPUNGE removes every message any client has marked
	// \Deleted, which in a mailbox shared with other IMAP clients means
	// destroying messages this caller was never asked about. A server without
	// UIDPLUS therefore gets ErrMissingCapability instead of a degraded path.
	//
	// This is the W-A2 destroy-inside-Trash primitive (L2-jmap-write §2) and
	// the only production path that removes mail irrecoverably.
	Expunge(ctx context.Context, uids []UID) error

	// CreateMailbox creates a mailbox (RFC 3501 §6.3.3).
	//
	// The name is UTF-8; the wire encoding to modified UTF-7 is go-imap's, the
	// exact inverse of what ListMailboxes decodes. It never sets a SPECIAL-USE
	// attribute: roles belong to the server (folder.go).
	//
	// A name already taken is ErrMailboxExists; a name the protocol cannot
	// carry is ErrInvalidMailboxName.
	CreateMailbox(ctx context.Context, name string) error

	// RenameMailbox renames a mailbox, carrying its children with it as
	// RFC 3501 §6.3.5 requires.
	//
	// Renaming INBOX is REFUSED with ErrRenameInbox: the RFC gives it a bulk-
	// move semantics that empties INBOX and leaves its sub-hierarchy behind,
	// which no JMAP update can honestly express (folder.go documents it).
	RenameMailbox(ctx context.Context, from, to string) error

	// DeleteMailbox permanently removes a mailbox and its messages
	// (RFC 3501 §6.3.4). INBOX is refused with ErrDeleteInbox.
	//
	// This is destructive and irreversible on the server. Every caller that
	// wants a reversible delete must move the messages elsewhere FIRST — which
	// is exactly what Mailbox/set's onDestroyRemoveEmails does.
	DeleteMailbox(ctx context.Context, name string) error

	// SetSubscribed subscribes or unsubscribes a mailbox (RFC 3501 §6.3.6,
	// §6.3.7) — the server-side state RFC 8621 §2's isSubscribed maps onto.
	SetSubscribed(ctx context.Context, name string, subscribed bool) error

	// StatusMailbox returns one mailbox's STATUS counters without selecting it
	// (RFC 3501 §6.3.10). It is how a caller learns the UIDVALIDITY a freshly
	// created mailbox was assigned.
	StatusMailbox(ctx context.Context, name string) (MailboxInfo, error)

	// Metadata returns the METADATA operations for label definitions (A6).
	// The returned value borrows the connection and must not outlive it.
	Metadata() MetadataOps

	// Close logs out and releases the connection. It is safe to call more than
	// once and safe to call from another goroutine to unblock a stuck one.
	Close() error
}

// Capabilities is a post-login capability set, keyed by lowercase name.
type Capabilities map[string]struct{}

// Has reports whether the server advertises a capability. The name is matched
// case-insensitively, as RFC 3501 requires.
func (c Capabilities) Has(name string) bool {
	_, ok := c[normalizeCap(name)]
	return ok
}

// Names returns the capabilities in sorted order, for logging.
func (c Capabilities) Names() []string {
	return sortedKeys(c)
}

// The capability names this package cares about, in the lowercase form
// Capabilities uses.
const (
	CapIMAP4rev1  = "imap4rev1"
	CapCondStore  = "condstore"
	CapQResync    = "qresync"
	CapIdle       = "idle"
	CapNotify     = "notify"
	CapMetadata   = "metadata"
	CapSpecialUse = "special-use"
	CapListStatus = "list-status"
	CapUIDPlus    = "uidplus"
	CapMove       = "move"
	CapStartTLS   = "starttls"
)

// requiredCapabilities are the extensions the sync engine cannot work without.
//
// QRESYNC is the resync path; CONDSTORE is what makes an incremental fetch
// possible at all; IDLE is what keeps a watcher's socket being read. Anything
// missing here is a deployment error, not a runtime condition to degrade
// through, so Connect fails loudly rather than silently falling back to
// polling the whole mailbox.
//
// NOTIFY, METADATA, SPECIAL-USE and LIST-STATUS are deliberately NOT required:
// each has a documented, correct degradation (per-mailbox IDLE, labels in the
// store, heuristic role detection, a second STATUS round trip), and requiring
// them would lock Moov to Dovecot when the protocol does not.
var requiredCapabilities = []string{CapCondStore, CapQResync, CapIdle}

// Errors this package returns. They are sentinels so the sync engine can
// branch on the condition (retry, resync, give up) without matching strings.
var (
	// ErrNotConnected is returned by any method called before Connect
	// succeeded or after Close.
	ErrNotConnected = errors.New("imap: not connected")

	// ErrNoMailboxSelected is returned by a method that needs a selected
	// mailbox when none is.
	ErrNoMailboxSelected = errors.New("imap: no mailbox selected")

	// ErrMissingCapability is returned by Connect when the server lacks a
	// required extension, and by a method whose optional extension is absent.
	ErrMissingCapability = errors.New("imap: server lacks a required capability")

	// ErrIteratorClosed is returned by an iterator used after Close, and by a
	// Message body read after the iterator advanced past it.
	ErrIteratorClosed = errors.New("imap: iterator is closed")

	// ErrWatchNotSupported is returned by Watch when the server advertises
	// neither NOTIFY nor a usable IDLE.
	ErrWatchNotSupported = errors.New("imap: server supports neither NOTIFY nor IDLE")

	// ErrInvalidMailboxName is returned by CreateMailbox and RenameMailbox for
	// a name the IMAP protocol cannot carry at all (empty, only whitespace, or
	// containing CR/LF/NUL). It is a caller error, not a server condition: the
	// JMAP layer turns it into an invalidProperties SetError naming "name".
	ErrInvalidMailboxName = errors.New("imap: invalid mailbox name")

	// ErrMailboxExists is returned when a CREATE or RENAME target name is
	// already taken ([ALREADYEXISTS], RFC 9051 §6.3.4).
	ErrMailboxExists = errors.New("imap: a mailbox with that name already exists")

	// ErrMailboxMissing is returned when a RENAME or DELETE names a mailbox
	// that is not there ([NONEXISTENT], RFC 9051 §6.3.5). It is distinct from
	// ErrMailboxStale: the mailbox is gone for everyone, not just for this
	// session's view of it.
	ErrMailboxMissing = errors.New("imap: no such mailbox")

	// ErrRenameInbox is returned by RenameMailbox for INBOX. RFC 3501 §6.3.5
	// makes that command a bulk move that empties INBOX and leaves its children
	// behind — see RenameMailbox for why this package refuses to expose it.
	ErrRenameInbox = errors.New("imap: INBOX cannot be renamed")

	// ErrDeleteInbox is returned by DeleteMailbox for INBOX (RFC 3501 §6.3.4:
	// "It is an error to attempt to delete INBOX").
	ErrDeleteInbox = errors.New("imap: INBOX cannot be deleted")

	// ErrMailboxStale is returned by SelectQResync when this SESSION's view of
	// a mailbox no longer matches the server's, because the mailbox was deleted
	// or recreated by another client while this connection had it open.
	//
	// It is a connection-level condition, not a mailbox-level one: the mailbox
	// is fine and a new connection selects it without complaint, but this one
	// will keep refusing it for the rest of its life — Dovecot holds the stale
	// view per session, and neither UNSELECT nor a plain SELECT clears it
	// (measured in E6 against Dovecot 2.3.21.1).
	//
	// The correct response is therefore to discard the connection and retry on
	// a fresh one, after which the ordinary UIDVALIDITY-changed branch handles
	// the rest.
	ErrMailboxStale = errors.New("imap: this session's view of the mailbox is stale; reconnect")
)

// MissingCapabilityError names the capabilities that were absent.
type MissingCapabilityError struct {
	Missing []string
}

// Error implements error.
func (e *MissingCapabilityError) Error() string {
	return fmt.Sprintf("imap: server lacks required capabilities: %v", e.Missing)
}

// Unwrap lets errors.Is match ErrMissingCapability.
func (e *MissingCapabilityError) Unwrap() error { return ErrMissingCapability }

// Compile-time assertion that the concrete implementation satisfies the
// contract. If a signature in §4.1 changes, this is what fails first.
var _ Client = (*client)(nil)
