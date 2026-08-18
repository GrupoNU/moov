package imap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	goimap "github.com/emersion/go-imap/v2"
)

// The folder-mutation primitives of W2 (L2-jmap-write §3, Mailbox/set):
// CREATE, RENAME, DELETE and the SUBSCRIBE pair.
//
// They join Client for the same product reason Move and Expunge did in W1 —
// a user creating a folder in the webmail must create it in Dovecot, because
// Dovecot is the source of truth (ADR-001) and Moov's store is a cache. What
// testsupport.go's MailboxMutator has carried for the integration suites is
// promoted here, with the production-grade guards the test helper never
// needed: an INBOX rename refusal, name validation before the wire, and
// SelectResult on the way back out of a create.
//
// # Character encoding
//
// Names cross this boundary as UTF-8 and are encoded to modified UTF-7 by
// go-imap's own wire encoder (imapwire.Encoder.Mailbox → internal/utf7), the
// exact inverse of what ListMailboxes already relies on to hand back UTF-8
// names (types.go MailboxInfo.Name). So nothing in this file encodes anything
// by hand: a second encoder here would double-encode the moment the server
// negotiates UTF8=ACCEPT, and hand-rolled modified UTF-7 is a classic source
// of folder names that read as "&AOk-" in every other client.

// CreateMailbox implements Client.
func (cl *client) CreateMailbox(ctx context.Context, name string) error {
	if err := validateMailboxName(name); err != nil {
		return err
	}
	gc, err := cl.conn()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// No CreateOptions: the SPECIAL-USE flag of RFC 6154 §3 is deliberately
	// never set from here. A role is the server's to assign — Dovecot creates
	// the account's \Sent, \Trash and friends itself — and a client that could
	// mint a second \Sent would produce a mailbox tree with two of a role that
	// the store's partial unique index on (account_id, role) forbids.
	if err := gc.Create(name, nil).Wait(); err != nil {
		if mailboxExistsError(err) {
			return fmt.Errorf("imap: CREATE %q: %w", name, ErrMailboxExists)
		}
		return fmt.Errorf("imap: CREATE %q: %w", name, err)
	}
	return nil
}

// RenameMailbox implements Client.
//
// # The INBOX special case, and why it is refused rather than implemented
//
// RFC 3501 §6.3.5 gives RENAME INBOX a semantics unlike every other rename:
//
//	"Renaming INBOX is permitted, and has special behavior. It moves all
//	 messages in INBOX to a new mailbox with the given name, leaving INBOX
//	 empty."
//
// So it is not a rename at all: it is a bulk move that leaves the source
// mailbox in place, and — the part that matters here — INBOX's own
// sub-hierarchy is explicitly NOT renamed with it, unlike an ordinary rename
// which RFC 3501 requires to carry every child along ("If the server's
// hierarchy separator character appears in the name, the server SHOULD create
// any superior hierarchical names... Renaming a mailbox with inferior
// hierarchical names also renames them").
//
// Exposing that through a JMAP Mailbox/set update would be a lie in both
// directions: the client asked to rename one object and would instead get a
// new mailbox, an emptied INBOX, and a set of children left behind under the
// old path — none of which JMAP's update model can express. Moov therefore
// refuses it here, in the layer that knows the protocol, so no caller can
// reach it by accident. The JMAP layer refuses it earlier and more legibly,
// as a role-protection SetError; this is the backstop.
func (cl *client) RenameMailbox(ctx context.Context, from, to string) error {
	if err := validateMailboxName(to); err != nil {
		return err
	}
	if from == "" {
		return fmt.Errorf("imap: RENAME requires a source mailbox")
	}
	if isInboxName(from) {
		return fmt.Errorf("imap: refusing to rename INBOX: RFC 3501 §6.3.5 makes it a bulk move "+
			"that empties INBOX and leaves its children behind, which is not a rename: %w", ErrRenameInbox)
	}
	if from == to {
		// A no-op the server would answer with either OK or "already exists"
		// depending on its mood. Answering here keeps the caller's replay
		// idempotent without depending on which.
		return nil
	}

	gc, err := cl.conn()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := gc.Rename(from, to, nil).Wait(); err != nil {
		switch {
		case mailboxExistsError(err):
			return fmt.Errorf("imap: RENAME %q to %q: %w", from, to, ErrMailboxExists)
		case mailboxMissingError(err):
			return fmt.Errorf("imap: RENAME %q to %q: %w", from, to, ErrMailboxMissing)
		}
		return fmt.Errorf("imap: RENAME %q to %q: %w", from, to, err)
	}

	// The connection's selected-mailbox memory now names a mailbox that no
	// longer exists under that name. Clearing it turns a later
	// selectedMailbox() into an honest ErrNoMailboxSelected instead of a
	// command issued against a stale name.
	cl.mu.Lock()
	if cl.selected == from || strings.HasPrefix(cl.selected, from+"/") {
		cl.selected = ""
	}
	cl.mu.Unlock()
	return nil
}

// DeleteMailbox implements Client.
//
// DELETE removes the mailbox and every message in it, permanently and without
// a Trash detour: RFC 3501 §6.3.4, "The DELETE command permanently removes the
// mailbox with the given name". Nothing in this package softens that, because
// softening it here would hide the destruction from the layer that has to
// decide whether it is allowed — see Mailbox/set's onDestroyRemoveEmails,
// which moves the messages to Trash BEFORE calling this.
//
// A mailbox with inferior names is left to the server: RFC 3501 requires it to
// reject the DELETE of a \Noselect parent that still has children, and to keep
// the children of a deletable parent alive. Guessing either way from here
// would diverge from whatever the server actually does.
func (cl *client) DeleteMailbox(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("imap: DELETE requires a mailbox name")
	}
	if isInboxName(name) {
		// RFC 3501 §6.3.4: "It is an error to attempt to delete INBOX". The
		// server would refuse; refusing here names the reason.
		return fmt.Errorf("imap: refusing to delete INBOX (RFC 3501 §6.3.4): %w", ErrDeleteInbox)
	}

	gc, err := cl.conn()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// A selected mailbox cannot be deleted on some servers, and Dovecot leaves
	// the deleting session with a stale view of it either way (see
	// ErrMailboxStale). Releasing the selection first avoids both. The error is
	// ignored on purpose: having nothing selected is the desired state, and
	// UNSELECT failing because of that is not a failure of the delete.
	cl.mu.Lock()
	selected := cl.selected
	cl.mu.Unlock()
	if selected != "" {
		_ = gc.Unselect().Wait()
		cl.mu.Lock()
		cl.selected = ""
		cl.mu.Unlock()
	}

	if err := gc.Delete(name).Wait(); err != nil {
		if mailboxMissingError(err) {
			return fmt.Errorf("imap: DELETE %q: %w", name, ErrMailboxMissing)
		}
		return fmt.Errorf("imap: DELETE %q: %w", name, err)
	}
	return nil
}

// SetSubscribed implements Client: SUBSCRIBE / UNSUBSCRIBE (RFC 3501 §6.3.6
// and §6.3.7).
//
// Subscription is what RFC 8621 §2's isSubscribed maps onto, and it is stored
// server-side, so it belongs with the other folder mutations rather than in
// Moov's own row: a folder Moov unsubscribes must be unsubscribed for every
// client of that account.
func (cl *client) SetSubscribed(ctx context.Context, name string, subscribed bool) error {
	if name == "" {
		return fmt.Errorf("imap: SUBSCRIBE requires a mailbox name")
	}
	gc, err := cl.conn()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if subscribed {
		if err := gc.Subscribe(name).Wait(); err != nil {
			return fmt.Errorf("imap: SUBSCRIBE %q: %w", name, err)
		}
		return nil
	}
	if err := gc.Unsubscribe(name).Wait(); err != nil {
		return fmt.Errorf("imap: UNSUBSCRIBE %q: %w", name, err)
	}
	return nil
}

// StatusMailbox implements Client: a STATUS of one mailbox, which is how a
// caller learns the UIDVALIDITY of a mailbox it just created without selecting
// it (RFC 3501 §6.3.10).
//
// It returns the same MailboxInfo shape ListMailboxes does, with HasStatus set,
// so a caller has one type to reason about. Role, Subscribed and NoSelect are
// NOT reported: STATUS carries none of them, and inventing values would be
// worse than their absence.
func (cl *client) StatusMailbox(ctx context.Context, name string) (MailboxInfo, error) {
	out := MailboxInfo{Name: name}
	if name == "" {
		return out, fmt.Errorf("imap: STATUS requires a mailbox name")
	}
	gc, err := cl.conn()
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}

	data, err := gc.Status(name, statusOptions()).Wait()
	if err != nil {
		return out, fmt.Errorf("imap: STATUS %q: %w", name, err)
	}
	applyStatus(&out, data)
	return out, nil
}

// ---------------------------------------------------------------------------
// name validation
// ---------------------------------------------------------------------------

// validateMailboxName refuses names the protocol cannot carry, before the
// command is ever built.
//
// The point is not to duplicate the server's own validation — the server is
// the authority on what names it accepts, and a name this passes may still be
// refused. The point is that the characters below cannot be encoded into a
// well-formed IMAP command at all, so sending them produces a protocol-level
// failure whose error text tells the user nothing. Refusing here produces an
// error the JMAP layer can turn into an invalidProperties naming "name".
//
//   - Empty: no mailbox has no name.
//   - CR and LF: they terminate an IMAP line (RFC 3501 §2.2). A name carrying
//     one is a command-injection primitive, not a folder name.
//   - NUL: not representable in an IMAP string or literal.
//   - A name that is only whitespace: accepted by some servers, invisible in
//     every client, and indistinguishable from a bug.
func validateMailboxName(name string) error {
	if name == "" {
		return fmt.Errorf("imap: %w: the name is empty", ErrInvalidMailboxName)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("imap: %w: the name is only whitespace", ErrInvalidMailboxName)
	}
	if strings.ContainsAny(name, "\r\n\x00") {
		return fmt.Errorf("imap: %w: the name contains a control character that cannot appear "+
			"in an IMAP mailbox name (RFC 3501 §2.2)", ErrInvalidMailboxName)
	}
	if len(name) > MaxMailboxNameBytes {
		return fmt.Errorf("imap: %w: the name is %d bytes, over the %d-byte limit",
			ErrInvalidMailboxName, len(name), MaxMailboxNameBytes)
	}
	return nil
}

// MaxMailboxNameBytes bounds a mailbox name.
//
// RFC 3501 sets no limit, but Maildir++ encodes the whole path into a
// directory name and every filesystem Moov can run on caps a path component
// somewhere near 255 bytes. 512 is deliberately looser than that — the server
// is still the authority and will refuse what it cannot store — and tight
// enough that a megabyte-long name never reaches the wire.
const MaxMailboxNameBytes = 512

// isInboxName reports the reserved INBOX name, case-insensitively as RFC 3501
// §5.1 requires ("INBOX is a special name reserved... case-insensitive").
//
// It matches only the mailbox itself, never a child: "INBOX/Work" is an
// ordinary folder that may be renamed and deleted like any other.
func isInboxName(name string) bool { return equalFoldASCII(name, "INBOX") }

// equalFoldASCII is strings.EqualFold restricted to ASCII, which is what
// RFC 3501's case-insensitive INBOX comparison means. The Unicode-aware
// version would additionally fold characters that cannot appear in the literal
// being matched, so the narrower rule is the correct one.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// mailboxExistsError reports whether a CREATE or RENAME failed because the
// target name is taken.
//
// The condition has no response code of its own in RFC 3501 — RFC 9051 §6.3.4
// added [ALREADYEXISTS], and Dovecot 2.3 does emit it — so the check is
// code-first with a text fallback for a server that does not. It exists so the
// JMAP layer can answer invalidProperties on "name" instead of a generic
// serverFail, which is the difference between a client showing "that folder
// already exists" and showing "something went wrong".
func mailboxExistsError(err error) bool {
	if err == nil {
		return false
	}
	var imapErr *goimap.Error
	if errors.As(err, &imapErr) && imapErr != nil {
		if strings.EqualFold(string(imapErr.Code), "ALREADYEXISTS") {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "alreadyexists") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "mailbox exists")
}

// mailboxMissingError reports whether a command failed because the named
// mailbox does not exist ([NONEXISTENT], RFC 9051 §6.3.5).
func mailboxMissingError(err error) bool {
	if err == nil {
		return false
	}
	var imapErr *goimap.Error
	if errors.As(err, &imapErr) && imapErr != nil {
		if strings.EqualFold(string(imapErr.Code), "NONEXISTENT") {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "nonexistent") ||
		strings.Contains(msg, "doesn't exist") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "no such mailbox")
}
