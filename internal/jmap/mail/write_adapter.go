package mail

import (
	"context"
	"errors"

	syncengine "github.com/GrupoNU/moov/internal/sync"
)

// The sync-engine-backed implementation of EmailWriter.
//
// This file is the ONLY place in the JMAP surface that knows the write
// executor exists, exactly as adapter.go is the only place that knows the
// store does. Everything above it — the /set handler, the tests — works
// against the EmailWriter interface in write.go, which is what keeps the
// L2's layer rule ("la capa JMAP jamás toca internal/imap directo") a
// mechanical fact rather than a convention: this package never imports
// internal/imap, and the executor is the one that does.

// WriterAdapter implements EmailWriter over *sync.WriteExecutor.
type WriterAdapter struct {
	exec *syncengine.WriteExecutor
}

// NewWriterAdapter builds the adapter.
func NewWriterAdapter(exec *syncengine.WriteExecutor) (*WriterAdapter, error) {
	if exec == nil {
		return nil, errors.New("mail: a write executor is required")
	}
	return &WriterAdapter{exec: exec}, nil
}

// SetFlags implements EmailWriter.
func (a *WriterAdapter) SetFlags(ctx context.Context, accountID, messageID int64, change FlagsChange) error {
	_, err := a.exec.ApplyFlagChange(ctx, accountID, messageID, syncengine.FlagChange{
		Replace: change.Replace,
		Flags:   change.Flags,
		Add:     change.Add,
		Remove:  change.Remove,
	})
	return mapWriteErr(err)
}

// Move implements EmailWriter.
func (a *WriterAdapter) Move(ctx context.Context, accountID, messageID, mailboxID int64) error {
	_, err := a.exec.ApplyMove(ctx, accountID, messageID, mailboxID)
	return mapWriteErr(err)
}

// Destroy implements EmailWriter.
func (a *WriterAdapter) Destroy(ctx context.Context, accountID, messageID int64) error {
	_, err := a.exec.ApplyDestroy(ctx, accountID, messageID)
	return mapWriteErr(err)
}

// mapWriteErr translates the executor's sentinels into this package's, so the
// handlers branch on one vocabulary.
func mapWriteErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syncengine.ErrWriteNotFound):
		return ErrNotFound
	case errors.Is(err, syncengine.ErrWriteConflict):
		return ErrWriteConflict
	case errors.Is(err, syncengine.ErrNoTrashMailbox):
		return ErrNoTrash
	default:
		return err
	}
}
