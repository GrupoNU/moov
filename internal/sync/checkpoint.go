package sync

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GrupoNU/moov/internal/store"
)

// Checkpoint schema version. The checkpoint column is opaque JSON owned by this
// package (store.SyncCheckpoint.Checkpoint), so it needs its own version marker
// for the day its shape changes: an unrecognized version is treated as "no
// checkpoint" and the account resyncs, which is always safe because every write
// is idempotent.
const checkpointVersion = 1

// accountCheckpoint is what this package stores under the account-wide scope.
//
// It records only which PHASE the account reached, not per-mailbox progress:
// mailbox progress belongs on the mailbox row (backfill_state,
// backfill_uid_low), where it is updated in the same breath as the mailbox's
// other sync state and cannot drift from it. Keeping one fact in one place is
// what makes "resume" a lookup rather than a reconciliation between two
// records that disagree.
type accountCheckpoint struct {
	Version int   `json:"version"`
	Phase   Phase `json:"phase"`
}

// mailboxCheckpoint is the per-mailbox scope's payload.
//
// UIDLow is the resume watermark: every UID from UIDLow upwards has been
// fetched and committed. The backfill walks DOWNWARD from the top of the
// mailbox, so the newest mail — the mail a user is most likely to look for
// first — lands first, and the watermark only ever moves down.
type mailboxCheckpoint struct {
	Version int `json:"version"`

	// UIDValidity the watermark belongs to. A mismatch means the mailbox was
	// recreated on the server and the watermark refers to UIDs that no longer
	// exist, so it must be discarded rather than trusted (L2 §2.5).
	UIDValidity uint32 `json:"uidvalidity"`

	// UIDLow is the lowest UID already stored. Zero means nothing yet.
	UIDLow uint32 `json:"uid_low"`

	// RecentDone records that the phase-A window of this mailbox is committed.
	RecentDone bool `json:"recent_done,omitempty"`

	// Complete records that the backfill reached UID 1.
	Complete bool `json:"complete,omitempty"`
}

// mailboxScope is the sync_log scope naming one mailbox.
//
// Scoping by mailbox ID rather than by name is deliberate: a rename would
// otherwise orphan the checkpoint and silently restart a finished backfill,
// while the ID follows the folder through any rename the server reports.
func mailboxScope(mailboxID int64) string {
	return fmt.Sprintf("mailbox:%d", mailboxID)
}

// loadAccountPhase reads how far the account got. An account that has never
// synced reads as PhaseDiscover, which is where a run starts anyway.
func (s *Syncer) loadAccountPhase(ctx context.Context, accountID int64) (Phase, error) {
	cp, err := s.store.GetCheckpoint(ctx, accountID, store.AccountScope)
	if err != nil {
		return PhaseDiscover, fmt.Errorf("reading account checkpoint: %w", err)
	}
	ac, ok := decodeAccountCheckpoint(cp.Checkpoint)
	if !ok {
		return PhaseDiscover, nil
	}
	return ac.Phase, nil
}

// decodeAccountCheckpoint reads an account checkpoint, reporting whether it is
// usable.
//
// An unreadable checkpoint is NOT an error to propagate. A decode failure or an
// unrecognized version means only that this run cannot use the record, and the
// correct response is to sync from the beginning: every write in this package is
// idempotent, so a redundant pass re-derives the same state and costs time
// rather than correctness. Returning an error instead would refuse to sync an
// account because of a field the engine itself wrote in a previous version —
// turning a self-healing condition into an outage.
func decodeAccountCheckpoint(payload []byte) (accountCheckpoint, bool) {
	var ac accountCheckpoint
	if err := json.Unmarshal(payload, &ac); err != nil {
		return accountCheckpoint{}, false
	}
	if ac.Version != checkpointVersion {
		return accountCheckpoint{}, false
	}
	return ac, true
}

// saveAccountPhase records the phase reached. It is called only after the work
// the phase describes is durably committed.
func (s *Syncer) saveAccountPhase(ctx context.Context, accountID int64, phase Phase) error {
	payload, err := json.Marshal(accountCheckpoint{Version: checkpointVersion, Phase: phase})
	if err != nil {
		return fmt.Errorf("encoding account checkpoint: %w", err)
	}
	if err := s.store.SaveCheckpoint(ctx, accountID, store.AccountScope, payload); err != nil {
		return fmt.Errorf("saving account checkpoint: %w", err)
	}
	return nil
}

// loadMailboxCheckpoint reads a mailbox's resume point, discarding one that
// belongs to a superseded UIDVALIDITY.
func (s *Syncer) loadMailboxCheckpoint(ctx context.Context, accountID, mailboxID int64, uidValidity uint32) (mailboxCheckpoint, error) {
	cp, err := s.store.GetCheckpoint(ctx, accountID, mailboxScope(mailboxID))
	if err != nil {
		return mailboxCheckpoint{}, fmt.Errorf("reading mailbox checkpoint: %w", err)
	}

	mc, ok := decodeMailboxCheckpoint(cp.Checkpoint)
	if !ok {
		return mailboxCheckpoint{Version: checkpointVersion, UIDValidity: uidValidity}, nil
	}
	if mc.UIDValidity != uidValidity {
		// The mailbox was recreated: the UIDs the watermark names are gone and
		// the ones the server serves now are different messages under the same
		// numbers. Trusting it here is precisely how a resync silently skips
		// real mail.
		return mailboxCheckpoint{Version: checkpointVersion, UIDValidity: uidValidity}, nil
	}
	return mc, nil
}

// decodeMailboxCheckpoint reads a mailbox checkpoint, reporting whether it is
// usable. Unreadable means "start this mailbox from the top", for the same
// reason as decodeAccountCheckpoint.
func decodeMailboxCheckpoint(payload []byte) (mailboxCheckpoint, bool) {
	var mc mailboxCheckpoint
	if err := json.Unmarshal(payload, &mc); err != nil {
		return mailboxCheckpoint{}, false
	}
	if mc.Version != checkpointVersion {
		return mailboxCheckpoint{}, false
	}
	return mc, true
}

// saveMailboxCheckpoint records a mailbox's resume point.
//
// ORDERING IS THE WHOLE POINT: this is called AFTER the batch it describes is
// committed. A checkpoint written first would, on a crash in between, claim
// messages are stored that are not — and since the backfill never revisits a
// UID below the watermark, those messages would be lost silently and forever.
// Written second, the same crash merely repeats a window, which the
// already-stored filter turns into a no-op.
func (s *Syncer) saveMailboxCheckpoint(ctx context.Context, accountID, mailboxID int64, mc mailboxCheckpoint) error {
	mc.Version = checkpointVersion
	payload, err := json.Marshal(mc)
	if err != nil {
		return fmt.Errorf("encoding mailbox checkpoint: %w", err)
	}
	if err := s.store.SaveCheckpoint(ctx, accountID, mailboxScope(mailboxID), payload); err != nil {
		return fmt.Errorf("saving mailbox checkpoint: %w", err)
	}
	return nil
}
