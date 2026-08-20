package imap

import (
	"context"
	"fmt"
	"time"
)

// Mailbox mutation for INTEGRATION TESTS ONLY.
//
// # Why this is in the production package
//
// The sync engine never writes to a mailbox: L2 §4.1's Client is a read
// interface plus flag updates, and that is the whole shape of the engine's
// relationship with Dovecot. But E6's acceptance criteria are all about what
// happens when a mailbox CHANGES — a message is delivered, another client marks
// one read, someone deletes one — and proving those against a real server means
// making those changes from the test.
//
// go-imap may only be imported from this package (ADR-001, L2 §2.1, enforced by
// both depguard and an architecture test that walks every file in the repo). So
// a test in internal/sync cannot APPEND a message itself, and a helper binary
// under tools/ could not either. The options were to weaken the architecture
// rule, or to put the mutation surface in the one package that is allowed to
// have it. This is the second.
//
// # Why it is a separate type and not methods on Client
//
// Because the confinement rule is not the only thing protecting the design —
// the narrowness of Client is. Adding Append and Expunge to Client would put
// them within reach of every consumer and make "the engine only reads" a
// convention rather than a fact. A distinct type, documented as test support,
// keeps the production contract exactly as L2 §4.1 defines it while giving the
// integration suites a supported way to arrange the world.
//
// W3 was the product reason this note always anticipated: drafts and the
// outbox's \Sent copy need APPEND for real, so it is now the production
// primitive Client.Append (append.go) and the mutator delegates to it — the
// same move CreateMailbox, DeleteMailbox and Expunge made in W1/W2.

// MailboxMutator performs the mailbox mutations an integration test needs to
// arrange a scenario. It is NOT part of the sync engine's contract and must not
// be used by production code.
type MailboxMutator struct {
	cl *client
}

// Mutator returns the test-support mutation surface for a connected client.
//
// It returns an error rather than panicking on a closed client so a test's
// failure is a test failure rather than a crash that takes the suite with it.
func Mutator(c Client) (*MailboxMutator, error) {
	cl, ok := c.(*client)
	if !ok {
		return nil, fmt.Errorf("imap: Mutator needs a client from New, got %T", c)
	}
	if _, err := cl.conn(); err != nil {
		return nil, err
	}
	return &MailboxMutator{cl: cl}, nil
}

// CreateMailbox creates a mailbox.
//
// Since W2 this is the production primitive Client.CreateMailbox (folder.go);
// the mutator delegates rather than keeping a second, subtly different CREATE.
func (m *MailboxMutator) CreateMailbox(ctx context.Context, name string) error {
	return m.cl.CreateMailbox(ctx, name)
}

// DeleteMailbox removes a mailbox. Also the production primitive since W2,
// including its UNSELECT-first discipline.
func (m *MailboxMutator) DeleteMailbox(ctx context.Context, name string) error {
	return m.cl.DeleteMailbox(ctx, name)
}

// Append stores a message in a mailbox and returns its UID.
//
// Since W3 this is the production primitive Client.Append (append.go) — drafts
// and the outbox's \Sent copy need it for real — so the mutator delegates,
// exactly as it does for CreateMailbox and Expunge.
//
// The UID comes from the UIDPLUS [APPENDUID] response code; a server without
// UIDPLUS returns 0, which callers must treat as "unknown" rather than as an
// error, since the message was still appended.
func (m *MailboxMutator) Append(ctx context.Context, mailbox string, raw []byte, flags []string, internalDate time.Time) (UID, error) {
	res, err := m.cl.Append(ctx, mailbox, raw, flags, internalDate)
	if err != nil {
		return 0, err
	}
	return res.UID, nil
}

// Expunge permanently removes the given UIDs from the selected mailbox.
//
// Since W1 this is the production primitive Client.Expunge (move.go), which
// carries the same \Deleted + UID EXPUNGE discipline this helper always had;
// the mutator delegates rather than duplicating it.
func (m *MailboxMutator) Expunge(ctx context.Context, uids []UID) error {
	return m.cl.Expunge(ctx, uids)
}

// Select selects a mailbox plainly, so a mutation that needs a selection has
// one. It deliberately does not use QRESYNC: arranging a scenario should not
// also replay a delta.
func (m *MailboxMutator) Select(ctx context.Context, mailbox string) error {
	_, err := m.cl.SelectQResync(ctx, mailbox, 0, 0)
	return err
}
