package store

import (
	"context"
	"fmt"
	"time"
)

// ExportStatus is the lifecycle of an account export (contract §2.6).
type ExportStatus string

// The export statuses. They match the CHECK constraint in migration 0012.
// "none" is a wire-only value for "no export was ever requested" and never a
// row's status.
const (
	ExportPending ExportStatus = "pending"
	ExportRunning ExportStatus = "running"
	ExportReady   ExportStatus = "ready"
	ExportFailed  ExportStatus = "failed"
	ExportExpired ExportStatus = "expired"
)

// Export is one background EML export job.
type Export struct {
	ID            string
	AccountID     *int64
	Address       string
	Status        ExportStatus
	RequestedAt   time.Time
	StartedAt     *time.Time
	CompletedAt   *time.Time
	MessagesDone  int
	MessagesTotal int
	Path          string
	Bytes         int64
	SHA256        string
	Messages      int
	Mailboxes     int
	Error         string
	PurgedAt      *time.Time
	UpdatedAt     time.Time
}

// Active reports whether the job is still to be produced.
func (e Export) Active() bool { return e.Status == ExportPending || e.Status == ExportRunning }

const exportColumns = `id, account_id, address, status, requested_at, started_at, completed_at,
	messages_done, messages_total, path, bytes, sha256, messages, mailboxes, error, purged_at, updated_at`

// CreateExport queues a job.
func (s *Store) CreateExport(ctx context.Context, id string, accountID int64, address string) (Export, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO account_exports (id, account_id, address, status)
		VALUES ($1, $2, $3, 'pending')
		RETURNING `+exportColumns, id, accountID, address)
	e, err := scanExport(row)
	if err != nil {
		return Export{}, fmt.Errorf("creating export %q: %w", id, err)
	}
	return e, nil
}

// GetExport looks a job up by id.
func (s *Store) GetExport(ctx context.Context, id string) (Export, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+exportColumns+` FROM account_exports WHERE id = $1`, id)
	e, err := scanExport(row)
	if err != nil {
		return Export{}, notFound(err, fmt.Sprintf("export %q", id))
	}
	return e, nil
}

// LatestExport returns the most recently requested job for an address, or
// ErrNotFound when none was ever requested.
func (s *Store) LatestExport(ctx context.Context, address string) (Export, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+exportColumns+`
		FROM account_exports WHERE address = $1 ORDER BY requested_at DESC, id DESC LIMIT 1`, address)
	e, err := scanExport(row)
	if err != nil {
		return Export{}, notFound(err, fmt.Sprintf("export for %q", address))
	}
	return e, nil
}

// ClaimPendingExport atomically moves the oldest pending job to running and
// returns it, or ErrNotFound when the queue is empty. The CAS in the WHERE is
// what lets several daemons share the table without a lock.
func (s *Store) ClaimPendingExport(ctx context.Context, at time.Time) (Export, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE account_exports SET status = 'running', started_at = $1, updated_at = now()
		 WHERE id = (SELECT id FROM account_exports WHERE status = 'pending'
		              ORDER BY requested_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING `+exportColumns, at)
	e, err := scanExport(row)
	if err != nil {
		return Export{}, notFound(err, "pending export")
	}
	return e, nil
}

// SetExportProgress records how far a running job got.
func (s *Store) SetExportProgress(ctx context.Context, id string, done, total int) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE account_exports SET messages_done = $2, messages_total = $3, updated_at = now()
		 WHERE id = $1 AND status = 'running'`, id, done, total); err != nil {
		return fmt.Errorf("updating export %q progress: %w", id, err)
	}
	return nil
}

// ExportResult is what a finished job records.
type ExportResult struct {
	Path      string
	Bytes     int64
	SHA256    string
	Messages  int
	Mailboxes int
}

// CompleteExport marks a job ready.
func (s *Store) CompleteExport(ctx context.Context, id string, r ExportResult, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE account_exports
		   SET status = 'ready', completed_at = $2, path = $3, bytes = $4, sha256 = $5,
		       messages = $6, mailboxes = $7, messages_done = $6, messages_total = $6,
		       updated_at = now()
		 WHERE id = $1 AND status = 'running'`,
		id, at, r.Path, r.Bytes, r.SHA256, r.Messages, r.Mailboxes)
	if err != nil {
		return fmt.Errorf("completing export %q: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("completing export %q: %w", id, ErrNotFound)
	}
	return nil
}

// FailExport marks a job failed with a human-readable reason (no internals —
// it is served to the API caller verbatim).
func (s *Store) FailExport(ctx context.Context, id string, reason string, at time.Time) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE account_exports SET status = 'failed', completed_at = $2, error = $3, updated_at = now()
		 WHERE id = $1 AND status IN ('pending', 'running')`, id, at, reason); err != nil {
		return fmt.Errorf("failing export %q: %w", id, err)
	}
	return nil
}

// ListExpirableExports returns the ready jobs completed before cutoff whose
// file has not been purged — the retention sweep's work list.
func (s *Store) ListExpirableExports(ctx context.Context, cutoff time.Time, limit int) ([]Export, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT `+exportColumns+`
		FROM account_exports
		WHERE status = 'ready' AND purged_at IS NULL AND completed_at < $1
		ORDER BY completed_at LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("listing expirable exports: %w", err)
	}
	defer rows.Close()
	var out []Export
	for rows.Next() {
		e, err := scanExport(rows)
		if err != nil {
			return nil, fmt.Errorf("listing expirable exports: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing expirable exports: %w", err)
	}
	return out, nil
}

// ListExportsOf returns every job of an address, newest first — what the
// account purge removes files for.
func (s *Store) ListExportsOf(ctx context.Context, address string) ([]Export, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+exportColumns+`
		FROM account_exports WHERE address = $1 ORDER BY requested_at DESC`, address)
	if err != nil {
		return nil, fmt.Errorf("listing exports of %q: %w", address, err)
	}
	defer rows.Close()
	var out []Export
	for rows.Next() {
		e, err := scanExport(rows)
		if err != nil {
			return nil, fmt.Errorf("listing exports of %q: %w", address, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing exports of %q: %w", address, err)
	}
	return out, nil
}

// PurgeExport records that the zip was removed: the row stays, as expired, so
// the signed URL answers 410 rather than 404.
func (s *Store) PurgeExport(ctx context.Context, id string, at time.Time) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE account_exports SET status = 'expired', purged_at = $2, path = '', updated_at = now()
		 WHERE id = $1`, id, at); err != nil {
		return fmt.Errorf("purging export %q: %w", id, err)
	}
	return nil
}

// CountPendingExports counts jobs not yet produced, for the gauge.
func (s *Store) CountPendingExports(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM account_exports WHERE status IN ('pending', 'running')`).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting pending exports: %w", err)
	}
	return n, nil
}

// ExportMessage is one message as the export writer sees it: where it lives,
// which blob holds its bytes, and the two facts the manifest records.
type ExportMessage struct {
	MailboxName string
	UID         int64
	RawSHA256   []byte
	RawSize     int64
	MessageID   string
	ReceivedAt  time.Time
}

// ForEachAccountMessage streams every live message of an account, ordered by
// mailbox name and then UID, so the zip's entry order is stable across runs
// and a manifest can be compared to the store afterwards (gate criterion 6).
//
// It streams rather than returning a slice: an account can hold hundreds of
// thousands of messages and the writer needs one at a time.
func (s *Store) ForEachAccountMessage(ctx context.Context, accountID int64, fn func(ExportMessage) error) error {
	rows, err := s.pool.Query(ctx, `
		SELECT b.name, st.uid, m.raw_sha256, m.raw_size, m.message_id,
		       COALESCE(m.internal_date, m.date)
		  FROM message_state st
		  JOIN messages m ON m.id = st.message_id
		  JOIN mailboxes b ON b.id = st.mailbox_id
		 WHERE st.account_id = $1 AND st.deleted_at IS NULL
		 ORDER BY b.name, st.uid`, accountID)
	if err != nil {
		return fmt.Errorf("listing messages of account %d: %w", accountID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var em ExportMessage
		if err := rows.Scan(&em.MailboxName, &em.UID, &em.RawSHA256, &em.RawSize,
			&em.MessageID, &em.ReceivedAt); err != nil {
			return fmt.Errorf("listing messages of account %d: %w", accountID, err)
		}
		if err := fn(em); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("listing messages of account %d: %w", accountID, err)
	}
	return nil
}

func scanExport(row scanner) (Export, error) {
	var e Export
	err := row.Scan(&e.ID, &e.AccountID, &e.Address, &e.Status, &e.RequestedAt, &e.StartedAt,
		&e.CompletedAt, &e.MessagesDone, &e.MessagesTotal, &e.Path, &e.Bytes, &e.SHA256,
		&e.Messages, &e.Mailboxes, &e.Error, &e.PurgedAt, &e.UpdatedAt)
	return e, err
}
