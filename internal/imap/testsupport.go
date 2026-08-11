package imap

import (
	"context"
	"fmt"
	"time"

	goimap "github.com/emersion/go-imap/v2"
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
// If the engine ever genuinely needs to write messages — drafts, undo-send —
// APPEND joins Client then, for a product reason, and this type does not become
// that path by default.

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
func (m *MailboxMutator) CreateMailbox(ctx context.Context, name string) error {
	gc, err := m.cl.conn()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := gc.Create(name, nil).Wait(); err != nil {
		return fmt.Errorf("imap: CREATE %q: %w", name, err)
	}
	return nil
}

// DeleteMailbox removes a mailbox.
func (m *MailboxMutator) DeleteMailbox(ctx context.Context, name string) error {
	gc, err := m.cl.conn()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// A selected mailbox cannot be deleted on some servers, so the selection is
	// released first. The error is ignored: not having one selected is fine.
	_ = gc.Unselect().Wait()
	if err := gc.Delete(name).Wait(); err != nil {
		return fmt.Errorf("imap: DELETE %q: %w", name, err)
	}
	m.cl.mu.Lock()
	m.cl.selected = ""
	m.cl.mu.Unlock()
	return nil
}

// Append stores a message in a mailbox and returns its UID.
//
// The UID comes from the UIDPLUS [APPENDUID] response code; a server without
// UIDPLUS returns 0, which callers must treat as "unknown" rather than as an
// error, since the message was still appended.
func (m *MailboxMutator) Append(ctx context.Context, mailbox string, raw []byte, flags []string, internalDate time.Time) (UID, error) {
	gc, err := m.cl.conn()
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	var opts *goimap.AppendOptions
	if len(flags) > 0 || !internalDate.IsZero() {
		opts = &goimap.AppendOptions{
			Flags: flagsToGoIMAP(flags),
			Time:  internalDate,
		}
	}

	cmd := gc.Append(mailbox, int64(len(raw)), opts)
	if _, werr := cmd.Write(raw); werr != nil {
		return 0, fmt.Errorf("imap: writing APPEND literal for %q: %w", mailbox, werr)
	}
	if cerr := cmd.Close(); cerr != nil {
		return 0, fmt.Errorf("imap: closing APPEND literal for %q: %w", mailbox, cerr)
	}
	data, err := cmd.Wait()
	if err != nil {
		return 0, fmt.Errorf("imap: APPEND to %q: %w", mailbox, err)
	}
	return UID(data.UID), nil
}

// Expunge permanently removes the given UIDs from the selected mailbox.
//
// It sets \Deleted and then issues UID EXPUNGE, which is the surgical form: a
// bare EXPUNGE would also remove messages another client had marked \Deleted
// and not yet expunged, which in a shared test mailbox means deleting somebody
// else's fixture.
func (m *MailboxMutator) Expunge(ctx context.Context, uids []UID) error {
	if len(uids) == 0 {
		return nil
	}
	gc, err := m.cl.conn()
	if err != nil {
		return err
	}
	if _, err := m.cl.selectedMailbox(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	set := uidSetFromUIDs(uids)
	if err := gc.Store(set, &goimap.StoreFlags{
		Op:     goimap.StoreFlagsAdd,
		Silent: true,
		Flags:  []goimap.Flag{goimap.FlagDeleted},
	}, nil).Close(); err != nil {
		return fmt.Errorf("imap: marking %d messages deleted: %w", len(uids), err)
	}

	if err := gc.UIDExpunge(set).Close(); err != nil {
		return fmt.Errorf("imap: UID EXPUNGE: %w", err)
	}
	return nil
}

// Select selects a mailbox plainly, so a mutation that needs a selection has
// one. It deliberately does not use QRESYNC: arranging a scenario should not
// also replay a delta.
func (m *MailboxMutator) Select(ctx context.Context, mailbox string) error {
	_, err := m.cl.SelectQResync(ctx, mailbox, 0, 0)
	return err
}
