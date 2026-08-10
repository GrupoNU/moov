package imap

import (
	"context"
	"fmt"

	goimap "github.com/emersion/go-imap/v2"
)

// storeBatchSize caps how many UIDs go into one STORE command.
//
// Batching is not an optimization here, it is the design: S3 H9 measured a
// batched flag update as ~23x cheaper than one per message, and the sync
// engine's dominant write pattern on an established mailbox is flag churn.
// 512 keeps the command line well inside any server's limit while making the
// per-command overhead negligible.
const storeBatchSize = 512

// StoreFlags implements Client.
//
// # The silent-write hazard
//
// A conditional STORE that the server refuses does NOT fail. It completes with
// a tagged OK, returns no FETCH responses for the refused messages, and names
// them only in the [MODIFIED] response code (RFC 7162 §3.1.3). Stock go-imap
// discards that code entirely, so a rejected write is indistinguishable from
// an applied one (S2 H6) — and the failure mode is Moov's flag state silently
// diverging from Dovecot's, which no ordinary test would catch because it only
// happens under concurrent modification.
//
// This implementation closes the hole from both ends:
//
//  1. Patch 0003 surfaces [MODIFIED], so the server's own answer is used when
//     it gives one.
//  2. When it does not — an unpatched vendor tree, or a server that omits the
//     code — the flags are read back and compared. Slower, always correct.
//
// StoreResult.VerifiedByReadBack says which path produced the answer, so the
// safety net can eventually be retired on evidence rather than on faith.
func (cl *client) StoreFlags(ctx context.Context, uids []UID, delta FlagDelta, unchangedSince ModSeq) (StoreResult, error) {
	var out StoreResult

	// Arguments are validated before the connection is touched, so a caller
	// mistake reports the mistake rather than "not connected" — and so an
	// empty UID set costs nothing at all.
	if len(uids) == 0 {
		return out, nil
	}
	op, err := storeOpToGoIMAP(delta.Op)
	if err != nil {
		return out, err
	}
	if unchangedSince != 0 {
		// A conditional write on a server without CONDSTORE must fail rather
		// than silently downgrade: the caller's whole reason for passing a
		// modseq is that it must not clobber a concurrent change.
		if err := cl.requireCap(CapCondStore); err != nil {
			return out, err
		}
	}

	if _, err := cl.selectedMailbox(); err != nil {
		return out, err
	}
	gc, err := cl.conn()
	if err != nil {
		return out, err
	}
	storeFlags := &goimap.StoreFlags{
		Op: op,
		// Silent is deliberately false: the untagged FETCH responses are how
		// the applied set is known, and asking the server to suppress them
		// would mean guessing.
		Silent: false,
		Flags:  flagsToGoIMAP(delta.Flags),
	}

	var opts *goimap.StoreOptions
	if unchangedSince != 0 {
		opts = &goimap.StoreOptions{UnchangedSince: uint64(unchangedSince)}
	}

	for start := 0; start < len(uids); start += storeBatchSize {
		end := min(start+storeBatchSize, len(uids))
		batch := uids[start:end]

		if err := ctx.Err(); err != nil {
			return out, err
		}

		res, err := cl.storeBatch(ctx, gc, readFlagsVia(gc), batch, storeFlags, opts, unchangedSince)
		if err != nil {
			return out, err
		}
		out.Updated = append(out.Updated, res.Updated...)
		out.Rejected = append(out.Rejected, res.Rejected...)
		if res.HighestModSeq > out.HighestModSeq {
			out.HighestModSeq = res.HighestModSeq
		}
		// One batch needing a read-back taints the whole result: the caller's
		// metric should say "this call used the fallback".
		out.VerifiedByReadBack = out.VerifiedByReadBack || res.VerifiedByReadBack
	}

	if out.Conflicted() {
		cl.log.Debug("imap: conditional STORE rejected messages",
			"rejected", len(out.Rejected), "updated", len(out.Updated),
			"unchanged_since", unchangedSince, "read_back", out.VerifiedByReadBack)
	}
	return out, nil
}

// storeBatch issues one STORE and works out what actually happened.
func (cl *client) storeBatch(
	ctx context.Context,
	gc goStoreClient,
	readBack flagReader,
	batch []UID,
	flags *goimap.StoreFlags,
	opts *goimap.StoreOptions,
	unchangedSince ModSeq,
) (StoreResult, error) {
	var out StoreResult

	cmd := gc.Store(uidSetFromUIDs(batch), flags, opts)
	msgs, err := cmd.Collect()
	if err != nil {
		return out, fmt.Errorf("imap: STORE: %w", err)
	}

	// Every message the server echoed back is one it updated.
	updated := make(map[UID]struct{}, len(msgs))
	for _, m := range msgs {
		if m.UID == 0 {
			continue
		}
		updated[UID(m.UID)] = struct{}{}
		if ModSeq(m.ModSeq) > out.HighestModSeq {
			out.HighestModSeq = ModSeq(m.ModSeq)
		}
	}
	for _, u := range batch {
		if _, ok := updated[u]; ok {
			out.Updated = append(out.Updated, u)
		}
	}

	if unchangedSince == 0 {
		// An unconditional STORE cannot be refused per-message: whatever the
		// server did not echo, it applied silently or the message no longer
		// exists. Neither is a conflict.
		return out, nil
	}

	// The authoritative answer, when patch 0003 is in the vendored tree.
	if modified := cmd.Modified(); modified != nil {
		if set, ok := modified.(goimap.UIDSet); ok {
			rejected, truncated := uidsFromUIDSet(set, len(batch))
			if truncated {
				cl.log.Warn("imap: [MODIFIED] set larger than the batch; falling back to read-back")
			} else {
				out.Rejected = rejected
				return out, nil
			}
		}
	}

	// No [MODIFIED]. Either every message was updated, or the code was not
	// surfaced. Only a read-back can tell the two apart, and only for the
	// messages that were not echoed — the echoed ones are confirmed already.
	missing := make([]UID, 0, len(batch)-len(updated))
	for _, u := range batch {
		if _, ok := updated[u]; !ok {
			missing = append(missing, u)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}

	rejected, err := cl.verifyByReadBack(ctx, readBack, missing, unchangedSince)
	if err != nil {
		return out, err
	}
	out.Rejected = rejected
	out.VerifiedByReadBack = true
	return out, nil
}

// verifyByReadBack re-reads the modseq of messages the STORE did not confirm
// and reports which ones the server must have refused.
//
// The test is the same one the server applied: a message whose modseq is above
// unchangedSince changed after the caller last saw it, so a conditional write
// against that modseq was rejected. A message that no longer exists is not
// reported as rejected — it was expunged, which is a different condition the
// caller learns about through VANISHED, and calling it a conflict would send
// the caller into a retry loop against a message that will never come back.
func (cl *client) verifyByReadBack(ctx context.Context, read flagReader, uids []UID, unchangedSince ModSeq) ([]UID, error) {
	states, err := read(ctx, uids)
	if err != nil {
		return nil, err
	}

	rejected := make([]UID, 0, len(uids))
	for _, s := range states {
		if s.modSeq > unchangedSince {
			rejected = append(rejected, s.uid)
		}
	}
	sortUIDs(rejected)
	return rejected, nil
}
