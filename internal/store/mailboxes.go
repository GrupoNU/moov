package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const mailboxColumns = `id, account_id, name, delimiter, role, subscribed, selectable,
	uidvalidity, uidnext, highestmodseq,
	backfill_state, backfill_uid_low, backfill_updated_at,
	last_synced_at, created_at, updated_at`

// UpsertMailbox creates or updates a mailbox by (account_id, name), which is
// how IMAP identifies a folder.
//
// It deliberately does NOT touch the sync state — uidvalidity, highestmodseq
// and the backfill columns. A LIST refresh runs on every reconnect and must
// not reset the resume point of a mailbox it merely re-listed; those columns
// are owned by SetMailboxSyncState and SetBackfillProgress, which the sync
// engine calls when it actually knows something new about them.
func (s *Store) UpsertMailbox(ctx context.Context, m Mailbox) (Mailbox, error) {
	if m.Delimiter == "" {
		m.Delimiter = "/"
	}
	if m.BackfillState == "" {
		m.BackfillState = BackfillPending
	}

	// A NULL role, not an empty string: the CHECK constraint admits NULL for
	// an ordinary folder, and the partial unique index on (account_id, role)
	// depends on NULL meaning "no role".
	var role *string
	if m.Role != RoleNone {
		r := string(m.Role)
		role = &r
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO mailboxes (account_id, name, delimiter, role, subscribed, selectable, backfill_state)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (account_id, name) DO UPDATE
		   SET delimiter  = EXCLUDED.delimiter,
		       role       = EXCLUDED.role,
		       subscribed = EXCLUDED.subscribed,
		       selectable = EXCLUDED.selectable,
		       updated_at = now()
		RETURNING `+mailboxColumns,
		m.AccountID, m.Name, m.Delimiter, role, m.Subscribed, m.Selectable, m.BackfillState)

	out, err := scanMailbox(row)
	if err != nil {
		return Mailbox{}, fmt.Errorf("upserting mailbox %q: %w", m.Name, err)
	}
	return out, nil
}

// GetMailbox looks a mailbox up by id.
func (s *Store) GetMailbox(ctx context.Context, id int64) (Mailbox, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+mailboxColumns+` FROM mailboxes WHERE id = $1`, id)
	m, err := scanMailbox(row)
	if err != nil {
		return Mailbox{}, notFound(err, fmt.Sprintf("mailbox %d", id))
	}
	return m, nil
}

// GetMailboxByName looks a mailbox up by its IMAP name within an account.
func (s *Store) GetMailboxByName(ctx context.Context, accountID int64, name string) (Mailbox, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+mailboxColumns+` FROM mailboxes WHERE account_id = $1 AND name = $2`,
		accountID, name)
	m, err := scanMailbox(row)
	if err != nil {
		return Mailbox{}, notFound(err, fmt.Sprintf("mailbox %q", name))
	}
	return m, nil
}

// GetMailboxByRole looks a mailbox up by its SPECIAL-USE role — the way the
// JMAP layer asks for Inbox, Sent or Trash without knowing what the server
// calls them in this account's language.
func (s *Store) GetMailboxByRole(ctx context.Context, accountID int64, role MailboxRole) (Mailbox, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+mailboxColumns+` FROM mailboxes WHERE account_id = $1 AND role = $2`,
		accountID, string(role))
	m, err := scanMailbox(row)
	if err != nil {
		return Mailbox{}, notFound(err, fmt.Sprintf("mailbox with role %q", role))
	}
	return m, nil
}

// ListMailboxes returns an account's folders, ordered by name.
func (s *Store) ListMailboxes(ctx context.Context, accountID int64) ([]Mailbox, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+mailboxColumns+` FROM mailboxes WHERE account_id = $1 ORDER BY name`,
		accountID)
	if err != nil {
		return nil, fmt.Errorf("listing mailboxes: %w", err)
	}
	defer rows.Close()

	var out []Mailbox
	for rows.Next() {
		m, err := scanMailbox(rows)
		if err != nil {
			return nil, fmt.Errorf("listing mailboxes: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing mailboxes: %w", err)
	}
	return out, nil
}

// SetMailboxSyncState records the QRESYNC resume point after a successful
// SELECT or sync pass (S2 H1).
func (s *Store) SetMailboxSyncState(ctx context.Context, mailboxID, uidvalidity, uidnext, highestmodseq int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE mailboxes
		   SET uidvalidity = $2, uidnext = $3, highestmodseq = $4,
		       last_synced_at = now(), updated_at = now()
		 WHERE id = $1`, mailboxID, uidvalidity, uidnext, highestmodseq)
	if err != nil {
		return fmt.Errorf("setting sync state for mailbox %d: %w", mailboxID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("setting sync state for mailbox %d: %w", mailboxID, ErrNotFound)
	}
	return nil
}

// SetBackfillProgress records how far the historical backfill has reached
// (L2 §2.5 step 3).
//
// uidLow is the lowest UID synced so far: everything from there up is present
// locally, which is what makes the backfill resumable after a kill -9 at any
// point. A nil uidLow leaves the watermark untouched and only moves the state.
func (s *Store) SetBackfillProgress(ctx context.Context, mailboxID int64, state BackfillState, uidLow *int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE mailboxes
		   SET backfill_state = $2,
		       backfill_uid_low = COALESCE($3, backfill_uid_low),
		       backfill_updated_at = now(), updated_at = now()
		 WHERE id = $1`, mailboxID, state, uidLow)
	if err != nil {
		return fmt.Errorf("setting backfill progress for mailbox %d: %w", mailboxID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("setting backfill progress for mailbox %d: %w", mailboxID, ErrNotFound)
	}
	return nil
}

// InvalidateMailbox clears the sync state after a UIDVALIDITY change, which
// means the server's UIDs no longer refer to what we stored (L2 §2.5).
//
// The message rows are deleted — their IMAP identity is void — but the blobs
// they referenced survive with a dropped refcount, so the resync recovers the
// content by sha256 without re-downloading anything already held. That is the
// whole reason content identity is separate from IMAP identity (S2 H8).
func (s *Store) InvalidateMailbox(ctx context.Context, mailboxID int64) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		// message_state rows cascade from messages; deleting the messages of
		// this mailbox is enough. The join is on message_state because that is
		// where the mailbox association lives (A5).
		if _, err := tx.Exec(ctx, `
			DELETE FROM messages m
			 USING message_state ms
			 WHERE ms.message_id = m.id AND ms.mailbox_id = $1`, mailboxID); err != nil {
			return fmt.Errorf("invalidating mailbox %d: deleting messages: %w", mailboxID, err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE mailboxes
			   SET uidvalidity = NULL, uidnext = NULL, highestmodseq = NULL,
			       backfill_state = 'pending', backfill_uid_low = NULL,
			       updated_at = now()
			 WHERE id = $1`, mailboxID); err != nil {
			return fmt.Errorf("invalidating mailbox %d: %w", mailboxID, err)
		}
		return nil
	})
}

// DeleteMailbox removes a mailbox and its messages.
func (s *Store) DeleteMailbox(ctx context.Context, mailboxID int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM mailboxes WHERE id = $1`, mailboxID)
	if err != nil {
		return fmt.Errorf("deleting mailbox %d: %w", mailboxID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("deleting mailbox %d: %w", mailboxID, ErrNotFound)
	}
	return nil
}

func scanMailbox(row scanner) (Mailbox, error) {
	var m Mailbox
	var role *string
	err := row.Scan(&m.ID, &m.AccountID, &m.Name, &m.Delimiter, &role,
		&m.Subscribed, &m.Selectable,
		&m.UIDValidity, &m.UIDNext, &m.HighestModSeq,
		&m.BackfillState, &m.BackfillUIDLow, &m.BackfillUpdatedAt,
		&m.LastSyncedAt, &m.CreatedAt, &m.UpdatedAt)
	if role != nil {
		m.Role = MailboxRole(*role)
	}
	return m, err
}
