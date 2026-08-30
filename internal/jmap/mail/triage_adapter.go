package mail

import (
	"context"
	"errors"
	"time"

	"github.com/GrupoNU/moov/internal/store"
	syncengine "github.com/GrupoNU/moov/internal/sync"
)

// The store-and-executor-backed implementation of TriageStore (L3 epic E4).
//
// It sits where WriterAdapter sits and for the same reason: this is the one
// file in the JMAP surface that knows both the store and the write executor
// exist. Everything above works against the TriageStore interface, so the
// handlers stay testable with fakes and this package never imports
// internal/imap.
//
// # Why it needs BOTH
//
// Snooze is half a Dovecot operation and half a bookkeeping row (GC-10): the
// MOVE goes through the executor, the wake time through the store. Mute is
// pure bookkeeping, but its key resolution — volatile thread id to durable
// thread row — is a store read. Splitting the two into separate adapters would
// mean two objects with one caller each, and a snooze that could be recorded
// without being moved.

// TriageAdapter implements TriageStore.
type TriageAdapter struct {
	store *store.Store
	exec  *syncengine.WriteExecutor
}

// NewTriageAdapter builds the adapter.
func NewTriageAdapter(st *store.Store, exec *syncengine.WriteExecutor) (*TriageAdapter, error) {
	if st == nil {
		return nil, errors.New("mail: a store is required")
	}
	if exec == nil {
		// Snoozing is a MOVE and unmuting is not; but a triage surface that
		// could mute and not snooze would advertise a capability it half
		// implements, which is exactly the "control that does nothing" the
		// plan's P4 forbids.
		return nil, errors.New("mail: a write executor is required")
	}
	return &TriageAdapter{store: st, exec: exec}, nil
}

// ---------------------------------------------------------------------------
// snoozes
// ---------------------------------------------------------------------------

// ListSnoozes implements TriageStore.
//
// The store keys snoozes by the DURABLE Message-ID; the JMAP surface needs
// store ids. The translation happens here, through the Snoozed mailbox: a
// pending snooze whose message is not in that folder is skipped rather than
// reported, because it has no Email id to name and reporting it would hand a
// client an id that Email/get answers notFound for.
//
// A skipped row is not lost — the waker retires it on its next pass (waker.go
// wakeOne's "no longer in the Snoozed folder" branch) — so the two agree on
// which rows are real without either having to ask the other.
func (a *TriageAdapter) ListSnoozes(ctx context.Context, accountID int64, limit int) ([]SnoozeRecord, error) {
	rows, err := a.store.PendingSnoozes(ctx, accountID, limit)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	mb, err := a.store.GetMailboxByName(ctx, accountID, syncengine.SnoozeMailboxName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// No Snoozed folder means nothing is snoozed, whatever the table
			// says. This is the rebuild case seen from the read side: the
			// folder is the source of truth (GC-10), so its absence wins.
			return nil, nil
		}
		return nil, err
	}
	out := make([]SnoozeRecord, 0, len(rows))
	for _, sn := range rows {
		id, err := a.store.MessageIDInMailbox(ctx, mb.ID, sn.MessageRFCID)
		if err != nil {
			continue
		}
		out = append(out, SnoozeRecord{
			EmailID:           id,
			Until:             sn.WakeAt,
			OriginMailboxName: sn.OriginMailbox,
		})
	}
	return out, nil
}

// SnoozeState implements TriageStore.
//
// Same "<nanos>-<count>" grammar every other type uses (adapter.go stateFor),
// over the snooze rows' own watermark. The count term does the same work it
// does for preferences: an un-snooze DELETES its row, which lowers the count
// while max(updated_at) stays where the last write left it, so without the
// count a client polling after an un-snooze would see an unchanged state and
// keep showing a message as snoozed forever.
func (a *TriageAdapter) SnoozeState(ctx context.Context, accountID int64) (string, error) {
	watermark, count, err := a.store.SnoozeWatermark(ctx, accountID)
	if err != nil {
		return "", err
	}
	return stateFor(watermark, count), nil
}

// Snooze implements TriageStore.
func (a *TriageAdapter) Snooze(ctx context.Context, accountID, messageID int64, until time.Time) (SnoozeRecord, error) {
	res, err := a.exec.ApplySnooze(ctx, accountID, messageID, until)
	if err != nil {
		return SnoozeRecord{}, mapTriageErr(err)
	}
	return SnoozeRecord{
		EmailID:           messageID,
		Until:             res.WakeAt,
		OriginMailboxName: res.OriginMailbox,
	}, nil
}

// Unsnooze implements TriageStore.
func (a *TriageAdapter) Unsnooze(ctx context.Context, accountID, messageID int64) error {
	_, err := a.exec.ApplyUnsnooze(ctx, accountID, messageID)
	return mapTriageErr(err)
}

// ---------------------------------------------------------------------------
// mutes
// ---------------------------------------------------------------------------

// ListMutes implements TriageStore.
func (a *TriageAdapter) ListMutes(ctx context.Context, accountID int64, limit int) ([]int64, error) {
	return a.store.ListMutedThreads(ctx, accountID, limit)
}

// MuteState implements TriageStore.
//
// The watermark is the newest mute row's, and the count is how many there are
// — the same reasoning as SnoozeState, with the same unmute-lowers-the-count
// case.
func (a *TriageAdapter) MuteState(ctx context.Context, accountID int64) (string, error) {
	watermark, count, err := a.store.MuteWatermark(ctx, accountID)
	if err != nil {
		return "", err
	}
	return stateFor(watermark, count), nil
}

// SetMuted implements TriageStore.
//
// It resolves the VOLATILE thread id the client sent to the DURABLE thread row
// the mute attaches to — the whole point of migration 0009's design. A
// conversation with no row yet (one the backfill skipped, or one whose members
// predate it) gets one created here rather than failing: refusing to mute a
// real conversation because our own backfill has not reached it would be a
// feature that works for some threads and silently not for others.
func (a *TriageAdapter) SetMuted(ctx context.Context, accountID, threadID int64, muted bool) error {
	row, err := a.store.ThreadRowByThreadID(ctx, accountID, threadID)
	if err == nil {
		return a.store.SetMute(ctx, accountID, row.ID, muted)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if !muted {
		// Unmuting a conversation with no row is already true: there is
		// nothing to remove. Succeeding is idempotent and correct; creating a
		// row just to delete it would be worse.
		return nil
	}
	created, err := a.store.EnsureThreadRowFor(ctx, accountID, threadID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	return a.store.SetMute(ctx, accountID, created.ID, muted)
}

// mapTriageErr translates the engine's sentinels into this package's.
func mapTriageErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syncengine.ErrWriteNotFound):
		return ErrNotFound
	case errors.Is(err, syncengine.ErrNotSnoozed):
		return ErrNotSnoozed
	case errors.Is(err, syncengine.ErrSnoozeUnavailable):
		return ErrSnoozeUnavailable
	default:
		return err
	}
}
