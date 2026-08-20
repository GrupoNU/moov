package imap

import (
	"context"
	"fmt"
	"time"

	goimap "github.com/emersion/go-imap/v2"
)

// APPEND — the write primitive of phase 2's W3 (L2-jmap-write §3): drafts are
// created by appending assembled RFC 5322 bytes to the Drafts folder, and the
// outbox appends the transmitted copy to \Sent after the SMTP 250 (ADR §4).
//
// It joins Client now for exactly the product reason testsupport.go's note
// anticipated ("If the engine ever genuinely needs to write messages — drafts,
// undo-send — APPEND joins Client then"); the test mutator delegates to this
// method rather than keeping a second, subtly different APPEND.

// AppendResult reports where an appended message landed.
type AppendResult struct {
	// UID is the new message's UID from the UIDPLUS [APPENDUID] response code
	// (RFC 4315 §3). Zero means the server did not say — possible only on a
	// server without UIDPLUS, which callers that need to reflect the append
	// locally must treat as "discover through the ordinary sync".
	UID UID

	// UIDValidity is the destination mailbox's UIDVALIDITY as reported by the
	// same [APPENDUID] code; zero when the server did not say.
	UIDValidity uint32
}

// Append implements Client.
func (cl *client) Append(ctx context.Context, mailbox string, raw []byte, flags []string, internalDate time.Time) (AppendResult, error) {
	var out AppendResult
	if mailbox == "" {
		return out, fmt.Errorf("imap: APPEND requires a mailbox name")
	}
	if len(raw) == 0 {
		// RFC 3501 §6.3.11 gives a zero-length literal no meaning, and Dovecot
		// refuses it; refusing here keeps the error legible.
		return out, fmt.Errorf("imap: APPEND requires a non-empty message")
	}

	gc, err := cl.conn()
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
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
		return out, fmt.Errorf("imap: writing APPEND literal for %q: %w", mailbox, werr)
	}
	if cerr := cmd.Close(); cerr != nil {
		return out, fmt.Errorf("imap: closing APPEND literal for %q: %w", mailbox, cerr)
	}
	data, err := cmd.Wait()
	if err != nil {
		return out, fmt.Errorf("imap: APPEND to %q: %w", mailbox, err)
	}
	if data != nil {
		out.UID = UID(data.UID)
		out.UIDValidity = data.UIDValidity
	}
	return out, nil
}
