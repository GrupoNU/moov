package imap

import (
	"context"
	"errors"
	"fmt"
	"io"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// fetchOptions builds the go-imap FETCH options for a Moov FetchSpec.
//
// UID and ModSeq are always requested regardless of the spec: the UID is the
// only stable handle Moov has on a message (there is no OBJECTID on our
// Dovecot — S2 T2a), and the modseq is the cursor the next incremental fetch
// resumes from. A fetch that returned neither would produce data the sync
// engine cannot place.
func fetchOptions(spec FetchSpec) *goimap.FetchOptions {
	opts := &goimap.FetchOptions{
		UID:          true,
		ModSeq:       true,
		Flags:        spec.Flags,
		InternalDate: spec.InternalDate,
		RFC822Size:   spec.Size,
		ChangedSince: uint64(spec.ChangedSince),
		Vanished:     spec.Vanished,
	}

	// Peek is set on every body section. Without it the server sets \Seen as a
	// side effect of syncing, which would mark a user's whole mailbox read the
	// first time Moov backfills it.
	switch {
	case spec.Body:
		// BODY.PEEK[] is the whole message, headers included, so a separate
		// header section would fetch the same bytes twice.
		opts.BodySection = []*goimap.FetchItemBodySection{{Peek: true}}
	case spec.Headers:
		opts.BodySection = []*goimap.FetchItemBodySection{
			{Specifier: goimap.PartSpecifierHeader, Peek: true},
		}
	}
	return opts
}

// FetchMessages implements Client.
func (cl *client) FetchMessages(ctx context.Context, uids []UID, spec FetchSpec) (MessageIter, error) {
	if _, err := cl.selectedMailbox(); err != nil {
		return nil, err
	}
	gc, err := cl.conn()
	if err != nil {
		return nil, err
	}
	if len(uids) == 0 {
		// No command is issued at all: an empty UID set would be a syntax
		// error on the wire, and "nothing to fetch" is a normal caller state.
		return emptyIter{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cmd := gc.Fetch(uidSetFromUIDs(uids), fetchOptions(spec))
	return newMessageIter(ctx, cmd, spec), nil
}

// FetchChanges implements Client.
//
// This is the live-connection incremental path: UID FETCH 1:* (FLAGS)
// (CHANGEDSINCE n VANISHED). The server replies with a FETCH per changed
// message and a VANISHED (EARLIER) naming everything expunged since (S2 T1).
func (cl *client) FetchChanges(ctx context.Context, since ModSeq) (ChangeIter, error) {
	if _, err := cl.selectedMailbox(); err != nil {
		return nil, err
	}
	gc, err := cl.conn()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	spec := FetchSpec{
		Flags:        true,
		InternalDate: true,
		Size:         true,
		ChangedSince: since,
		// VANISHED needs QRESYNC enabled and is only legal alongside
		// CHANGEDSINCE on a UID FETCH. since == 0 would mean "everything
		// changed since the beginning of time", where VANISHED is both
		// meaningless and rejected by some servers.
		Vanished: since != 0,
	}

	// The whole mailbox ("1:*"), filtered server-side by CHANGEDSINCE. Stop 0
	// is go-imap's spelling of "*".
	var all goimap.UIDSet
	all.AddRange(1, 0)

	collected := cl.armVanishedCollector()
	cmd := gc.Fetch(all, fetchOptions(spec))

	return &changeIter{
		messageIter: newMessageIter(ctx, cmd, spec),
		collect: func() []UID {
			out := collected()
			cl.disarmVanishedCollector()
			return dedupeUIDs(out)
		},
	}, nil
}

// messageIter adapts go-imap's FetchCommand to MessageIter.
//
// The adaptation that matters is the body: go-imap hands over a literal reader
// that is only valid until the message is advanced past, and this iterator
// preserves that property rather than buffering, because buffering is what
// turns a backfill of a mailbox with large attachments into an OOM.
type messageIter struct {
	ctx    context.Context
	cmd    *imapclient.FetchCommand
	spec   FetchSpec
	closed bool
	err    error

	// current is the message the caller is on; its body reader is invalidated
	// when Next advances.
	current *bodyReader
}

func newMessageIter(ctx context.Context, cmd *imapclient.FetchCommand, spec FetchSpec) *messageIter {
	return &messageIter{ctx: ctx, cmd: cmd, spec: spec}
}

// Next implements MessageIter.
func (it *messageIter) Next() (*Message, error) {
	if it.closed {
		return nil, ErrIteratorClosed
	}
	if it.err != nil {
		return nil, it.err
	}
	if err := it.ctx.Err(); err != nil {
		it.err = err
		return nil, err
	}

	// Invalidate the previous message's body: the bytes belong to the
	// connection and advancing overwrites them.
	if it.current != nil {
		it.current.invalidate()
		it.current = nil
	}

	data := it.cmd.Next()
	if data == nil {
		return nil, nil
	}

	msg := &Message{SeqNum: data.SeqNum}

	for {
		item := data.Next()
		if item == nil {
			break
		}
		switch v := item.(type) {
		case imapclient.FetchItemDataUID:
			msg.UID = UID(v.UID)
		case imapclient.FetchItemDataFlags:
			msg.Flags, msg.Keywords = splitFlags(v.Flags)
		case imapclient.FetchItemDataInternalDate:
			msg.InternalDate = v.Time
		case imapclient.FetchItemDataRFC822Size:
			msg.Size = v.Size
		case imapclient.FetchItemDataModSeq:
			msg.ModSeq = ModSeq(v.ModSeq)
		case imapclient.FetchItemDataBodySection:
			if err := it.attachBody(msg, v); err != nil {
				it.err = err
				return nil, err
			}
			// The body is a live reader over the connection: stop consuming
			// items and hand the message over now, so the caller reads the
			// literal before anything else advances the stream. Any remaining
			// items for this message are discarded by the library when the
			// next message is pulled.
			if msg.Body != nil {
				return msg, nil
			}
		default:
			// Items Moov did not ask for. Ignored rather than an error: a
			// server volunteering extra data is not a protocol violation.
		}
	}

	return msg, nil
}

// attachBody wires a body section into the message: buffered for headers,
// streamed for a full body.
func (it *messageIter) attachBody(msg *Message, v imapclient.FetchItemDataBodySection) error {
	if v.Literal == nil {
		return nil
	}
	if v.Section != nil && v.Section.Specifier == goimap.PartSpecifierHeader {
		// Headers are bounded and every consumer wants the whole block, so
		// buffering is correct here and keeps the reader from being a trap.
		raw, err := io.ReadAll(v.Literal)
		if err != nil {
			return fmt.Errorf("imap: reading header section: %w", err)
		}
		msg.Header = raw
		return nil
	}

	br := &bodyReader{r: v.Literal}
	it.current = br
	msg.Body = br
	return nil
}

// Close implements MessageIter.
func (it *messageIter) Close() error {
	if it.closed {
		return it.err
	}
	it.closed = true
	if it.current != nil {
		it.current.invalidate()
		it.current = nil
	}
	if err := it.cmd.Close(); err != nil && it.err == nil {
		it.err = fmt.Errorf("imap: FETCH: %w", err)
	}
	return it.err
}

// changeIter is a messageIter that also reports the VANISHED set.
type changeIter struct {
	*messageIter
	collect  func() []UID
	vanished []UID
	done     bool
}

// Close implements MessageIter and finalizes the vanished set.
func (it *changeIter) Close() error {
	err := it.messageIter.Close()
	if !it.done {
		it.vanished = it.collect()
		it.done = true
	}
	return err
}

// Vanished implements ChangeIter.
func (it *changeIter) Vanished() []UID {
	if !it.done {
		// Calling Vanished before Close is a caller mistake, but returning
		// what has arrived so far beats returning nil and looking like "no
		// expunges" — a silently empty delta is the bug that is hard to find.
		return it.collect()
	}
	return it.vanished
}

// bodyReader wraps a literal so that reads after the iterator advanced fail
// loudly instead of returning whatever bytes the connection has moved on to.
type bodyReader struct {
	r       io.Reader
	invalid bool
}

func (b *bodyReader) Read(p []byte) (int, error) {
	if b.invalid {
		return 0, fmt.Errorf("imap: message body read after the iterator advanced: %w", ErrIteratorClosed)
	}
	return b.r.Read(p)
}

// invalidate marks the reader dead. The remaining bytes are NOT drained here:
// go-imap discards the unread literal itself when the command advances, and
// draining from this side would race with the decoder goroutine.
func (b *bodyReader) invalidate() { b.invalid = true }

// emptyIter is the zero-work iterator returned for an empty UID set.
type emptyIter struct{}

func (emptyIter) Next() (*Message, error) { return nil, nil }
func (emptyIter) Close() error            { return nil }
func (emptyIter) Vanished() []UID         { return nil }

// Compile-time assertions.
var (
	_ MessageIter = (*messageIter)(nil)
	_ ChangeIter  = (*changeIter)(nil)
	_ ChangeIter  = emptyIter{}
	_ error       = (*MissingCapabilityError)(nil)
	_             = errors.Is
)
