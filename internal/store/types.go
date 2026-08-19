package store

import (
	"strings"
	"time"
)

// Flags is the bitmask of IMAP system flags carried in message_state.flags.
//
// A bitmask rather than an array because the system flags are a fixed, small,
// closed set: a bitmask filter is free, and the partial index on unread
// messages is expressible as `(flags & 1) = 0`. User keywords — which are
// open-ended, and which A6 maps labels onto — live in the keywords column
// instead.
//
// Bit 0 is \Seen, matching the S3 benchmark corpus and the unread index in
// migration 0002. Do not renumber: the values are persisted.
type Flags uint64

// The IMAP system flags (RFC 3501 §2.3.2). FlagSeen must remain bit 0.
const (
	FlagSeen Flags = 1 << iota
	FlagAnswered
	FlagFlagged
	FlagDeleted
	FlagDraft
	// FlagRecent is accepted and stored but never set by Moov: \Recent is
	// session-scoped in IMAP and cannot be meaningfully cached. It exists so a
	// round-trip through the store does not silently drop a bit the server
	// reported.
	FlagRecent
)

// Has reports whether every flag in mask is set.
func (f Flags) Has(mask Flags) bool { return f&mask == mask }

// toDB converts the bitmask to the int64 PostgreSQL stores it as.
//
// The database column is bigint (signed) while Flags is unsigned, so the top
// bit is not representable. That is not a limitation worth engineering around
// — there are six system flags and room for 62 — but the conversion is done
// here, once, with the invariant stated, rather than scattered as untracked
// casts across every query.
func (f Flags) toDB() int64 {
	if f > maxStorableFlags {
		// Unreachable through the exported API — only six bits are ever set —
		// but clamping keeps the function total rather than relying on that.
		return int64(maxStorableFlags)
	}
	// #nosec G115 -- the guard above establishes f <= maxStorableFlags
	// (2^63-1), so the conversion to a signed int64 cannot overflow. gosec
	// does not track the comparison.
	return int64(f)
}

// flagsFromDB converts a stored bigint back to a bitmask. Negative values
// cannot occur through toDB and are clamped to zero rather than wrapping into
// a nonsense high bit.
func flagsFromDB(v int64) Flags {
	if v < 0 {
		return 0
	}
	return Flags(v)
}

// maxStorableFlags is every bit a signed 64-bit column can hold.
const maxStorableFlags Flags = 1<<63 - 1

// String renders the flags in IMAP spelling, for logs and test failures.
func (f Flags) String() string {
	names := []struct {
		bit  Flags
		name string
	}{
		{FlagSeen, `\Seen`},
		{FlagAnswered, `\Answered`},
		{FlagFlagged, `\Flagged`},
		{FlagDeleted, `\Deleted`},
		{FlagDraft, `\Draft`},
		{FlagRecent, `\Recent`},
	}
	var set []string
	for _, n := range names {
		if f&n.bit != 0 {
			set = append(set, n.name)
		}
	}
	if len(set) == 0 {
		return "(none)"
	}
	return strings.Join(set, " ")
}

// ParseStatus is the outcome of the MIME parsing cascade (L2 §2.4, S4).
type ParseStatus string

// The parse outcomes. They match the CHECK constraint in migration 0002.
const (
	ParseOK      ParseStatus = "ok"
	ParsePartial ParseStatus = "partial"
	ParseFailed  ParseStatus = "failed"
)

// MailboxRole is a normalized SPECIAL-USE attribute, without the leading
// backslash and lowercased. The empty value means an ordinary folder.
type MailboxRole string

// The SPECIAL-USE roles Moov recognizes (RFC 6154 plus \Inbox).
const (
	RoleNone    MailboxRole = ""
	RoleInbox   MailboxRole = "inbox"
	RoleArchive MailboxRole = "archive"
	RoleDrafts  MailboxRole = "drafts"
	RoleSent    MailboxRole = "sent"
	RoleJunk    MailboxRole = "junk"
	RoleTrash   MailboxRole = "trash"
	RoleAll     MailboxRole = "all"
	RoleFlagged MailboxRole = "flagged"
)

// BackfillState tracks how far the historical backfill of a mailbox has got
// (L2 §2.5). A mailbox becomes usable at recent_done; complete means the whole
// history is local.
type BackfillState string

// The backfill states. They match the CHECK constraint in migration 0002.
const (
	BackfillPending    BackfillState = "pending"
	BackfillRecentDone BackfillState = "recent_done"
	BackfillInProgress BackfillState = "in_progress"
	BackfillComplete   BackfillState = "complete"
)

// AccountState is the lifecycle of an account within the engine.
type AccountState string

// The account states. They match the CHECK constraint in migration 0002.
const (
	AccountActive   AccountState = "active"
	AccountPaused   AccountState = "paused"
	AccountDisabled AccountState = "disabled"
)

// CredentialState is the state of an account's stored IMAP credentials,
// tracked separately from the account lifecycle: credentials can be revoked
// upstream while the account remains one the engine intends to serve.
type CredentialState string

// The credential states. They match the CHECK constraint in migration 0002.
const (
	CredentialPending CredentialState = "pending"
	CredentialActive  CredentialState = "active"
	CredentialInvalid CredentialState = "invalid"
	CredentialRevoked CredentialState = "revoked"
)

// BreakerState is the per-account circuit breaker recorded in sync_log
// (ADR §4: repeated failed logins must not trip Mailcow's fail2ban).
type BreakerState string

// The breaker states. They match the CHECK constraint in migration 0002.
const (
	BreakerClosed   BreakerState = "closed"
	BreakerOpen     BreakerState = "open"
	BreakerHalfOpen BreakerState = "half_open"
)

// IntentKind is the type of a queued client write (L2 §4.3).
type IntentKind string

// The intent kinds. They match the CHECK constraint in migration 0002.
const (
	IntentFlag IntentKind = "flag"
	IntentMove IntentKind = "move"
	IntentSend IntentKind = "send"
)

// IntentState is where a queued intent is in its lifecycle.
type IntentState string

// The intent states. They match the CHECK constraint in migration 0002.
const (
	IntentQueued   IntentState = "queued"
	IntentInFlight IntentState = "in_flight"
	IntentDone     IntentState = "done"
	IntentFailed   IntentState = "failed"
)

// AccountScope is the reserved sync_log scope for account-wide state, as
// opposed to a scope naming a single mailbox.
const AccountScope = "account"

// Account is a synchronized mailbox owner.
//
// IMAPAppPassword is ciphertext (AES-256-GCM, E7) and this package treats it
// as opaque bytes: the store neither encrypts nor decrypts, it only persists.
// The user's own password is never stored in any form.
type Account struct {
	ID              int64
	Email           string
	IMAPHost        string
	IMAPPort        int
	IMAPServerName  string
	IMAPUsername    string
	IMAPAppPassword []byte
	CredentialState CredentialState
	State           AccountState
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Mailbox is one IMAP folder of one account, with its QRESYNC resume point and
// backfill progress.
//
// UIDValidity, UIDNext and HighestModSeq are nil until the mailbox is first
// selected, which is what distinguishes "never synced" from "synced, empty".
type Mailbox struct {
	ID            int64
	AccountID     int64
	Name          string
	Delimiter     string
	Role          MailboxRole
	Subscribed    bool
	Selectable    bool
	UIDValidity   *int64
	UIDNext       *int64
	HighestModSeq *int64

	BackfillState     BackfillState
	BackfillUIDLow    *int64
	BackfillUpdatedAt *time.Time

	LastSyncedAt *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// UIDValidityOrZero returns the mailbox's UIDVALIDITY, or 0 when it has never
// been selected.
//
// The pointer distinguishes "never synced" from "synced, and the server said
// zero", which is a distinction the sync engine needs. Everywhere else — a
// lookup keyed by (mailbox, uidvalidity, uid), a log line — the caller wants a
// number, and writing the nil check at each of those call sites is how one of
// them eventually gets it wrong.
func (m Mailbox) UIDValidityOrZero() int64 {
	if m.UIDValidity == nil {
		return 0
	}
	return *m.UIDValidity
}

// Message is the immutable half of a stored message (A5): everything derived
// from the raw bytes at parse time. Nothing here changes because a user
// clicked something.
type Message struct {
	ID        int64
	AccountID int64

	RawSHA256 []byte
	RawSize   int64

	MessageID     string
	InReplyTo     string
	ReferencesIDs []string

	Subject   string
	FromAddr  string
	ToAddrs   string
	CcAddrs   string
	Addresses []byte // JSON, the structured address lists

	Date         time.Time
	InternalDate *time.Time

	MIMEStructure  []byte // JSON, the flattened part tree
	HasAttachments bool
	Preview        string
	BodyText       string

	ParseStatus   ParseStatus
	Parser        string
	ParserVersion int
	Defects       []byte // JSON array

	CreatedAt time.Time

	// ThreadID is the message's thread: the id of the thread's OLDEST member
	// (migration 0004). It is NOT set by the caller on insert — InsertMessages
	// stores the row's own id, making every message its own thread, and
	// AssignThreads then resolves the real one. See threads.go for why the two
	// are separate steps and why that intermediate state is valid rather than
	// torn.
	ThreadID int64
}

// MessageState is the volatile half (A5): the narrow, hot row that flag
// updates and moves write to, so the ~2.2 KB tsv is never rewritten.
type MessageState struct {
	MessageID   int64
	AccountID   int64
	MailboxID   int64
	UID         int64
	UIDValidity int64
	Flags       Flags
	Keywords    []string
	ModSeqSeen  int64
	DeletedAt   *time.Time
	UpdatedAt   time.Time
}

// NewMessage is one message to be inserted by InsertMessages: the immutable
// row and its initial state, which are always written together.
type NewMessage struct {
	Message Message
	State   MessageState
}

// FlagUpdate is a single message's new flag state, as applied by
// UpdateFlags. It touches message_state only — that is the whole point of A5.
type FlagUpdate struct {
	MessageID  int64
	Flags      Flags
	Keywords   []string
	ModSeqSeen int64
}

// SyncCheckpoint is the per-account, per-scope sync state: the resume point,
// the error history and the circuit breaker.
type SyncCheckpoint struct {
	AccountID         int64
	Scope             string
	Checkpoint        []byte // opaque JSON, owned by the sync engine
	StateCounter      int64
	LastSuccessAt     *time.Time
	LastError         string
	LastErrorAt       *time.Time
	ConsecutiveErrors int
	BreakerState      BreakerState
	BreakerUntil      *time.Time
	UpdatedAt         time.Time
}

// Intent is a queued client write the sync engine will execute against IMAP
// (L2 §4.3). The JMAP layer enqueues; it never talks to Dovecot itself.
type Intent struct {
	ID        int64
	AccountID int64
	Kind      IntentKind
	Payload   []byte // JSON
	State     IntentState
	Attempts  int
	LastError string
	NotBefore time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}
