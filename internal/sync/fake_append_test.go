package sync

import (
	"context"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
)

// W3's extension to the fake server: APPEND. Same rationale as the W1/W2
// extensions in fake_write_test.go — the properties under test (Dovecot-first
// ordering, APPENDUID reflection, the \Sent dedupe) are ordering properties,
// and only a deterministic server makes them exact.

// Append implements imap.Client for the fake: the message lands with the next
// UID and a fresh modseq, and the mailbox notifies — the echo the executor's
// create reflection must converge under, exactly like a MOVE's.
func (c *fakeClient) Append(_ context.Context, mailbox string, raw []byte, flags []string, internalDate time.Time) (imap.AppendResult, error) {
	c.srv.mu.Lock()

	var out imap.AppendResult
	if err := c.srv.appendErr; err != nil {
		c.srv.mu.Unlock()
		return out, err
	}
	target := c.srv.mailbox(mailbox)
	if target == nil || target.noSelect {
		c.srv.mu.Unlock()
		return out, imap.ErrNoMailboxSelected
	}

	if internalDate.IsZero() {
		internalDate = time.Now()
	}
	system, keywords := splitFakeFlags(flags)
	uid := target.uidNext()
	target.messages = append(target.messages, fakeMessage{
		uid:          uid,
		raw:          append([]byte(nil), raw...),
		flags:        system,
		keywords:     keywords,
		internalDate: internalDate,
		modSeq:       target.nextModSeq(),
	})

	if !c.srv.noAppendUID {
		out.UID = uid
		out.UIDValidity = target.uidValidity
	}
	status := target.statusFor()
	c.srv.mu.Unlock()

	c.srv.notify(mailbox, status)
	return out, nil
}
