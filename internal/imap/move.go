package imap

import (
	"context"
	"fmt"

	goimap "github.com/emersion/go-imap/v2"
)

// The write primitives of phase 2 (W1, L2-jmap-write §4): MOVE and the
// surgical UID EXPUNGE. They join Client for a product reason — Email/set
// moves and destroys real mail — exactly as testsupport.go's note anticipated.
//
// Both operate on the SELECTED mailbox, like StoreFlags, and both refuse to
// degrade into anything that could touch messages the caller did not name:
// Move without MOVE requires UIDPLUS for its fallback expunge, and Expunge
// requires UIDPLUS outright. A missing capability is a deployment-shaped
// error (ErrMissingCapability), never a silent behavioral downgrade — the
// collateral damage of a bare EXPUNGE in a mailbox other clients share is
// someone else's mail disappearing.

// Move implements Client.
func (cl *client) Move(ctx context.Context, uids []UID, dest string) (MoveResult, error) {
	var out MoveResult
	if len(uids) == 0 {
		return out, nil
	}
	if dest == "" {
		return out, fmt.Errorf("imap: MOVE requires a destination mailbox")
	}

	// go-imap's Move falls back to COPY + STORE \Deleted + EXPUNGE when the
	// server lacks MOVE (RFC 6851 §4.1's own equivalence). That fallback is
	// only acceptable when the expunge half can be scoped to our UIDs, which
	// needs UIDPLUS; without it the library would issue a bare EXPUNGE and
	// remove whatever any other client had marked \Deleted.
	if !cl.Capabilities().Has(CapMove) {
		if err := cl.requireCap(CapUIDPlus); err != nil {
			return out, fmt.Errorf("imap: server has neither MOVE nor UIDPLUS; refusing a move that could expunge other clients' messages: %w", err)
		}
	}

	if _, err := cl.selectedMailbox(); err != nil {
		return out, err
	}
	gc, err := cl.conn()
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}

	data, err := gc.Move(uidSetFromUIDs(uids), dest).Wait()
	if err != nil {
		return out, fmt.Errorf("imap: MOVE %d messages to %q: %w", len(uids), dest, err)
	}
	if data == nil {
		return out, nil
	}

	out.DestUIDValidity = data.UIDValidity
	out.DestUIDs = pairCopyUIDs(data.SourceUIDs, data.DestUIDs, len(uids))
	if out.DestUIDs == nil && data.UIDValidity != 0 {
		// COPYUID arrived but could not be paired (an unbounded set, or
		// mismatched halves). The move itself succeeded; only the reflection
		// shortcut is lost, and the caller's documented fallback covers it.
		cl.log.Warn("imap: MOVE returned a COPYUID this client could not pair; "+
			"the destination copy will be discovered by the ordinary sync", "dest", dest)
	}
	return out, nil
}

// pairCopyUIDs expands the two halves of a COPYUID response and pairs them
// positionally, per RFC 4315 §3: "the UIDs of the message(s) in the
// destination mailbox, in the same order as the source UIDs".
//
// It returns nil rather than a partial answer when anything about the sets is
// off — a dynamic set, a truncated expansion, halves of different lengths —
// because a WRONG mapping would rebind a message row to a UID that names a
// different message, which is precisely the corruption the mapping exists to
// avoid. nil is safe: the caller falls back to tombstone-and-rediscover.
func pairCopyUIDs(source, dest goimap.NumSet, expected int) map[UID]UID {
	srcSet, ok := source.(goimap.UIDSet)
	if !ok {
		return nil
	}
	dstSet, ok := dest.(goimap.UIDSet)
	if !ok {
		return nil
	}

	// The cap is the number of UIDs the caller asked to move: a COPYUID
	// naming more than that is a server answer this client cannot interpret.
	src, truncated := uidsFromUIDSet(srcSet, expected)
	if truncated {
		return nil
	}
	dst, truncated := uidsFromUIDSet(dstSet, expected)
	if truncated {
		return nil
	}
	if len(src) == 0 || len(src) != len(dst) {
		return nil
	}

	out := make(map[UID]UID, len(src))
	for i, s := range src {
		out[s] = dst[i]
	}
	return out
}

// Expunge implements Client.
func (cl *client) Expunge(ctx context.Context, uids []UID) error {
	if len(uids) == 0 {
		return nil
	}
	// The refusal, not a fallback: see the Client doc. A bare EXPUNGE is the
	// one IMAP command in this package's vocabulary that can destroy messages
	// the caller never named.
	if err := cl.requireCap(CapUIDPlus); err != nil {
		return fmt.Errorf("imap: UID EXPUNGE requires UIDPLUS: %w", err)
	}

	if _, err := cl.selectedMailbox(); err != nil {
		return err
	}
	gc, err := cl.conn()
	if err != nil {
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
		return fmt.Errorf("imap: marking %d messages \\Deleted: %w", len(uids), err)
	}
	if err := gc.UIDExpunge(set).Close(); err != nil {
		return fmt.Errorf("imap: UID EXPUNGE: %w", err)
	}
	return nil
}
