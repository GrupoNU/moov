package mail

import (
	"context"
	"errors"
)

// The write contract of W1 (L2-jmap-write §4), stated in the same style as
// contracts.go: this package's own vocabulary, the account id passed
// explicitly at every call, and absence expressed as ErrNotFound rather than
// as a distinguishable "forbidden".
//
// The layering rule behind the interface is the L2's own: "la capa JMAP jamás
// toca internal/imap directo". Handlers speak to an EmailWriter; the ONLY
// file that knows the real implementation is internal/sync's write executor
// lives behind it is write_adapter.go — the same confinement adapter.go gives
// the store.

// ErrWriteConflict means the message changed on the server after the state
// the client's update was computed from (the UNCHANGEDSINCE refusal of RFC
// 7162, surfaced per the W1 acceptance criteria). The store has been
// refreshed; a client that re-reads and resubmits will succeed.
var ErrWriteConflict = errors.New("mail: the message changed on the server; re-read and retry")

// ErrNoTrash means a destroy could not be honored as W-A2's move-to-Trash
// because the account has no \Trash role mailbox.
var ErrNoTrash = errors.New("mail: the account has no Trash mailbox")

// FlagsChange is one keyword mutation, already translated from JMAP keywords
// to the sync engine's flag vocabulary (imapNameForKeyword).
type FlagsChange struct {
	// Replace, when true, makes Flags the exact final set — the full-set
	// "keywords" form of RFC 8621 §4.6.
	Replace bool
	Flags   []string

	// Add and Remove are the patch form ("keywords/$seen": true|null).
	Add    []string
	Remove []string
}

// EmailWriter applies Email/set mutations. Every method is Dovecot-first per
// W-A1: on return the change is on the server AND reflected in the store, or
// an error says loudly that it is on neither.
type EmailWriter interface {
	// SetFlags applies a keyword change to one message.
	SetFlags(ctx context.Context, accountID, messageID int64, change FlagsChange) error

	// Move moves one message to another mailbox of the same account. An
	// unknown or foreign target mailbox is ErrNotFound.
	Move(ctx context.Context, accountID, messageID, mailboxID int64) error

	// Destroy destroys one message per W-A2: MOVE to \Trash from anywhere
	// else, \Deleted + UID EXPUNGE only from inside Trash.
	Destroy(ctx context.Context, accountID, messageID int64) error
}
