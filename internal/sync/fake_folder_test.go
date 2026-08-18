package sync

import (
	"context"
	"strings"

	"github.com/GrupoNU/moov/internal/imap"
)

// W2's extensions to the fake server: the folder mutations CREATE, RENAME,
// DELETE, SUBSCRIBE and STATUS.
//
// The properties W2 must guarantee are ordering properties like W1's — the
// store is untouched when IMAP refuses, a rename carries children on BOTH
// sides, a destroy tombstones before it drops the row — and only a
// deterministic server makes them exact. This fake therefore models the parts
// of RFC 3501 §6.3 those properties depend on, and refuses the same things a
// real server refuses:
//
//   - CREATE of an existing name -> ErrMailboxExists
//   - RENAME/DELETE of a missing name -> ErrMailboxMissing
//   - RENAME carries the source's children with it (§6.3.5)
//   - a fresh mailbox gets its own UIDVALIDITY, which is what the executor
//     reads back through STATUS

// nextUIDValidity hands out a fresh UIDVALIDITY for a newly created mailbox.
// Monotonic, so a delete-and-recreate of the same name yields a DIFFERENT
// value — which is what makes "the UIDs you had mean nothing now" testable.
func (s *fakeServer) nextUIDValidity() uint32 {
	s.uidValiditySeq++
	return 1000 + s.uidValiditySeq
}

// CreateMailbox implements imap.Client for the fake.
func (c *fakeClient) CreateMailbox(_ context.Context, name string) error {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()

	if c.closed {
		return imap.ErrNotConnected
	}
	if err := c.srv.createErr; err != nil {
		return err
	}
	if name == "" || strings.ContainsAny(name, "\r\n\x00") {
		return imap.ErrInvalidMailboxName
	}
	if c.srv.mailbox(name) != nil {
		return imap.ErrMailboxExists
	}

	mb := &fakeMailbox{
		name:          name,
		uidValidity:   c.srv.nextUIDValidity(),
		highestModSeq: 1,
		subscribed:    true,
	}
	c.srv.mailboxes = append(c.srv.mailboxes, mb)
	return nil
}

// RenameMailbox implements imap.Client for the fake, carrying children along
// as RFC 3501 §6.3.5 requires.
func (c *fakeClient) RenameMailbox(_ context.Context, from, to string) error {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()

	if c.closed {
		return imap.ErrNotConnected
	}
	if err := c.srv.renameErr; err != nil {
		return err
	}
	if to == "" || strings.ContainsAny(to, "\r\n\x00") {
		return imap.ErrInvalidMailboxName
	}
	// The real client refuses this before the wire; the fake refuses it too so
	// a test that bypasses the executor still cannot empty the fake inbox.
	if strings.EqualFold(from, "INBOX") {
		return imap.ErrRenameInbox
	}
	src := c.srv.mailbox(from)
	if src == nil {
		return imap.ErrMailboxMissing
	}
	if c.srv.mailbox(to) != nil {
		return imap.ErrMailboxExists
	}

	prefix := from + "/"
	for _, mb := range c.srv.mailboxes {
		switch {
		case mb.name == from:
			mb.name = to
		case strings.HasPrefix(mb.name, prefix):
			mb.name = to + "/" + strings.TrimPrefix(mb.name, prefix)
		}
	}
	return nil
}

// DeleteMailbox implements imap.Client for the fake.
func (c *fakeClient) DeleteMailbox(_ context.Context, name string) error {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()

	if c.closed {
		return imap.ErrNotConnected
	}
	if err := c.srv.deleteErr; err != nil {
		return err
	}
	if strings.EqualFold(name, "INBOX") {
		return imap.ErrDeleteInbox
	}

	for i, mb := range c.srv.mailboxes {
		if mb.name == name {
			c.srv.mailboxes = append(c.srv.mailboxes[:i], c.srv.mailboxes[i+1:]...)
			if c.selected == mb {
				c.selected = nil
			}
			return nil
		}
	}
	return imap.ErrMailboxMissing
}

// SetSubscribed implements imap.Client for the fake.
func (c *fakeClient) SetSubscribed(_ context.Context, name string, subscribed bool) error {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()

	if c.closed {
		return imap.ErrNotConnected
	}
	if err := c.srv.subscribeErr; err != nil {
		return err
	}
	mb := c.srv.mailbox(name)
	if mb == nil {
		return imap.ErrMailboxMissing
	}
	mb.subscribed = subscribed
	return nil
}

// StatusMailbox implements imap.Client for the fake.
func (c *fakeClient) StatusMailbox(_ context.Context, name string) (imap.MailboxInfo, error) {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()

	if c.closed {
		return imap.MailboxInfo{}, imap.ErrNotConnected
	}
	if err := c.srv.statusErr; err != nil {
		return imap.MailboxInfo{}, err
	}
	mb := c.srv.mailbox(name)
	if mb == nil {
		return imap.MailboxInfo{}, imap.ErrMailboxMissing
	}
	return imap.MailboxInfo{
		Name:          mb.name,
		Delimiter:     "/",
		Subscribed:    mb.subscribed,
		HasStatus:     true,
		NumMessages:   uint32(len(mb.messages)),
		UIDNext:       mb.uidNext(),
		UIDValidity:   mb.uidValidity,
		HighestModSeq: mb.highestModSeq,
	}, nil
}
