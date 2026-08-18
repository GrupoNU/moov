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

// KeywordBudget implements EmailWriter.
func (a *WriterAdapter) KeywordBudget(ctx context.Context, accountID, mailboxID int64) (KeywordBudget, error) {
	b, err := a.exec.KeywordBudgetFor(ctx, accountID, mailboxID)
	if err != nil {
		return KeywordBudget{Limit: b.Limit}, mapWriteErr(err)
	}
	return KeywordBudget{InUse: b.InUse, Limit: b.Limit}, nil
}

// ---- MailboxWriter (W2) ----------------------------------------------------

// CreateMailbox implements MailboxWriter.
func (a *WriterAdapter) CreateMailbox(ctx context.Context, accountID int64, name string, subscribe bool) (int64, error) {
	res, err := a.exec.ApplyMailboxCreate(ctx, accountID, name, subscribe)
	if err != nil {
		return 0, mapWriteErr(err)
	}
	return res.MailboxID, nil
}

// RenameMailbox implements MailboxWriter.
func (a *WriterAdapter) RenameMailbox(ctx context.Context, accountID, mailboxID int64, newName string) error {
	_, err := a.exec.ApplyMailboxRename(ctx, accountID, mailboxID, newName)
	return mapWriteErr(err)
}

// DestroyMailbox implements MailboxWriter.
func (a *WriterAdapter) DestroyMailbox(ctx context.Context, accountID, mailboxID int64) error {
	_, err := a.exec.ApplyMailboxDestroy(ctx, accountID, mailboxID)
	return mapWriteErr(err)
}

// Compile-time assertions: the adapter is the sole bridge, so a signature
// drifting from either interface must fail here rather than at wiring time.
var (
	_ EmailWriter   = (*WriterAdapter)(nil)
	_ MailboxWriter = (*WriterAdapter)(nil)
)

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

	// ---- W2 -----------------------------------------------------------------
	case errors.Is(err, syncengine.ErrMailboxNotFound):
		return ErrNotFound
	case errors.Is(err, syncengine.ErrMailboxNameTaken):
		return ErrMailboxExists
	case errors.Is(err, syncengine.ErrMailboxNameInvalid):
		return ErrInvalidName
	case errors.Is(err, syncengine.ErrMailboxProtected):
		return ErrMailboxProtected
	case errors.Is(err, syncengine.ErrMailboxHasChildren):
		return ErrMailboxHasChild
	default:
		return err
	}
}
