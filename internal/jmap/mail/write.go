package mail

import (
	"context"
	"errors"
	"strings"
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

	// KeywordBudget reports a mailbox's standing against the durable Maildir
	// keyword ceiling (A6 / validation V1). See KeywordBudget.
	KeywordBudget(ctx context.Context, accountID, mailboxID int64) (KeywordBudget, error)
}

// ---------------------------------------------------------------------------
// W2: the folder-write contract and the keyword ceiling
// ---------------------------------------------------------------------------

// The W2 sentinels, in the same style as W1's: conditions the handler branches
// on, never error text it matches.
var (
	// ErrMailboxExists means the requested mailbox name is already taken.
	ErrMailboxExists = errors.New("mail: a mailbox with that name already exists")

	// ErrInvalidName means the requested name cannot be an IMAP mailbox name.
	ErrInvalidName = errors.New("mail: invalid mailbox name")

	// ErrMailboxProtected means IMAP itself forbids the operation on this
	// mailbox — renaming or deleting INBOX (RFC 3501 §6.3.4, §6.3.5).
	ErrMailboxProtected = errors.New("mail: this mailbox cannot be renamed or deleted")

	// ErrMailboxHasChild means a destroy was refused because the mailbox has
	// descendants. It is the condition RFC 8621 §2.5 names mailboxHasChild.
	ErrMailboxHasChild = errors.New("mail: the mailbox has child mailboxes")
)

// KeywordBudget is a mailbox's standing against the durable keyword ceiling.
//
// # Why the JMAP layer needs this at all
//
// A Maildir folder holds 26 distinct keywords durably — one letter a-z in the
// message filename, per `dovecot-keywords`. Dovecot accepts more, serves them
// back correctly while its index is warm, and drops every one past the 26th the
// next time that index is rebuilt from disk: silently, all at once, possibly
// weeks later (validation V1, imap.MaxDurableKeywordsPerMailbox).
//
// So the server cannot be asked and the write cannot be verified. Moov has to
// count the mailbox's keywords itself and refuse the 27th — which is what makes
// this a JMAP-layer concern rather than a store detail: the refusal has to
// reach the client as a SetError it can show a user, before anything is
// written.
type KeywordBudget struct {
	// InUse are the distinct keywords the mailbox's live messages carry,
	// lowercased — the slots already spent.
	InUse []string

	// Limit is the ceiling. A real writer reports
	// imap.MaxDurableKeywordsPerMailbox; a zero or negative value means the
	// backend does not have the constraint and the check is skipped.
	Limit int
}

// maxDurableKeywords restates imap.MaxDurableKeywordsPerMailbox as a plain
// number.
//
// It is a duplicate ON PURPOSE: this package may never import internal/imap
// (the layering rule of write.go and doc.go — the JMAP surface reaches IMAP
// only through the sync engine's executor). The real limit still travels with
// each KeywordBudget from the writer, so this constant is used only where no
// writer has spoken: the test doubles' default, and the doc above. A test
// asserts the two agree, which is what keeps the restatement from drifting.
const maxDurableKeywords = 26

// Remaining is how many NEW distinct keywords still fit.
func (b KeywordBudget) Remaining() int {
	if n := b.Limit - len(b.InUse); n > 0 {
		return n
	}
	return 0
}

// Has reports whether a keyword already occupies a slot, matched
// case-insensitively — because dovecot-keywords allocates ONE slot per
// case-folded name, so "Work" and "work" are the same slot and counting them
// as two would let a client cross the real ceiling while Moov thought it had
// room.
func (b KeywordBudget) Has(keyword string) bool {
	want := strings.ToLower(strings.TrimSpace(keyword))
	for _, k := range b.InUse {
		if k == want {
			return true
		}
	}
	return false
}

// MailboxWriter applies Mailbox/set mutations (W2), Dovecot-first like
// EmailWriter.
//
// Names crossing this interface are FULL IMAP paths. The JMAP layer composes
// them from the client's leaf name and parentId before calling, because that
// composition needs the folder tree, which is a JMAP-layer read — and because
// the executor below has no business knowing that JMAP models hierarchy with a
// parent pointer while IMAP models it with a delimiter.
type MailboxWriter interface {
	// CreateMailbox creates a folder and returns the store id it landed on —
	// which becomes the JMAP Mailbox id.
	CreateMailbox(ctx context.Context, accountID int64, name string, subscribe bool) (int64, error)

	// RenameMailbox renames a folder, carrying its children. The mailbox id
	// SURVIVES: a rename updates the JMAP object, it does not replace it
	// (RFC 8621 §2.5).
	RenameMailbox(ctx context.Context, accountID, mailboxID int64, newName string) error

	// DestroyMailbox deletes a folder and everything left in it. The caller is
	// responsible for having emptied it first when that is what the client
	// asked for — see handleMailboxSet's onDestroyRemoveEmails.
	DestroyMailbox(ctx context.Context, accountID, mailboxID int64) error
}
