package imap

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2/imapclient"
)

// Entry names Moov owns. RFC 5464 §3.2 reserves /private/vendor/<token> for a
// vendor's own use, and "moov" is the token; nothing under this prefix belongs
// to any other client.
const (
	// EntryLabels holds the label definitions of arbitrage A6: the mapping
	// from IMAP keyword to label name, color and order. It lives in a private
	// annotation on the account root so the definition, like the assignment,
	// is reconstructible from Dovecot and the "Moov is a cache" invariant of
	// ADR-001 holds for labels too.
	EntryLabels = "/private/vendor/moov/labels"

	// EntrySyncState is reserved for engine state that must survive a rebuild
	// of the local store. Nothing writes it yet; it is declared here so the
	// namespace is documented in one place.
	EntrySyncState = "/private/vendor/moov/syncstate"
)

// MaxDurableKeywordsPerMailbox is the number of distinct IMAP keywords a
// Maildir folder can hold *durably*: 26.
//
// # Why this constant exists rather than a runtime check
//
// Asking the server does not work. Validation V1
// (docs/spikes/V1-metadata-dovecot.md) put 500 keywords on one message and
// Dovecot accepted every one, persisted them in its index, and returned them
// on the next FETCH. But on disk, after a force-resync, only 26 remain: the
// Maildir format encodes keywords as one letter a-z in the message filename,
// and `dovecot-keywords` stops at index 25.
//
// So keywords past the 26th live only in Dovecot's in-memory index. They read
// back correctly for as long as that index stays warm, and they vanish the
// next time it is rebuilt from the Maildir — all of them at once, silently,
// possibly weeks later.
//
// This is why the read-back verification in StoreFlags cannot catch it: at the
// moment of the read-back the keyword really is there. The engine has to
// enforce the ceiling itself, by counting the keywords already assigned in a
// mailbox before writing a new one, and refusing to create the 27th label with
// an explicit error (L2 §2.3: "no labels that exist only in the DB, silently").
//
// The budget is shared with standard keywords other clients set ($Forwarded,
// $MDNSent, NonJunk), which consume from the same 26.
const MaxDurableKeywordsPerMailbox = 26

// Metadata implements Client.
func (cl *client) Metadata() MetadataOps {
	return metadataOps{cl: cl}
}

// metadataOps implements MetadataOps against the live connection.
//
// It is a value borrowing the client rather than a separate object with its
// own connection: METADATA commands share the single command stream like
// everything else, so it must not outlive the Client it came from.
type metadataOps struct {
	cl *client
}

// Get implements MetadataOps.
func (m metadataOps) Get(ctx context.Context, mailbox string, entries []string) ([]Annotation, error) {
	if err := m.cl.requireCap(CapMetadata); err != nil {
		return nil, err
	}
	gc, err := m.cl.conn()
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	data, err := gc.GetMetadata(mailbox, entries, &imapclient.GetMetadataOptions{}).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap: GETMETADATA %q: %w", mailbox, err)
	}

	// The result is built from the requested entries rather than from what the
	// server returned, so an absent entry comes back with a nil Value instead
	// of vanishing. METADATA distinguishes "no such entry" from "empty entry",
	// and a caller that cannot tell them apart cannot implement "create the
	// label set if it does not exist yet".
	out := make([]Annotation, 0, len(entries))
	for _, name := range entries {
		ann := Annotation{Name: name}
		if data != nil {
			if v, ok := data.Entries[name]; ok && v != nil {
				ann.Value = *v
			}
		}
		out = append(out, ann)
	}
	return out, nil
}

// Set implements MetadataOps.
func (m metadataOps) Set(ctx context.Context, mailbox string, entries []Annotation) error {
	if err := m.cl.requireCap(CapMetadata); err != nil {
		return err
	}
	gc, err := m.cl.conn()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// go-imap spells "delete this entry" as a nil *[]byte, which is RFC 5464's
	// NIL value. An Annotation with a nil Value means the same thing on
	// Moov's side, so the two line up without a special case.
	payload := make(map[string]*[]byte, len(entries))
	for _, e := range entries {
		if e.Value == nil {
			payload[e.Name] = nil
			continue
		}
		v := e.Value
		payload[e.Name] = &v
	}

	if err := gc.SetMetadata(mailbox, payload).Wait(); err != nil {
		return fmt.Errorf("imap: SETMETADATA %q: %w", mailbox, err)
	}
	return nil
}

// Compile-time assertion.
var _ MetadataOps = metadataOps{}
