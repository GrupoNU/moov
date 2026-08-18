package store

import (
	"context"
	"fmt"
	"strings"

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

// RenameMailbox changes a mailbox's IMAP name IN PLACE, carrying its children
// with it — the store half of a Mailbox/set rename (W2).
//
// # Why in place, and why this matters more than it looks
//
// The row's id IS the JMAP Mailbox id. Reflecting a rename as delete-then-
// insert would mint a new id for the same folder, and every client would see
// the old mailbox destroyed and a new one created: bookmarks, filters and the
// Email.mailboxIds of every message inside it would all break, for what the
// user experienced as typing a new name. An UPDATE of the name column keeps
// the id, so a rename is a rename to a client (RFC 8621 §2.5 updates the
// object; it does not replace it).
//
// Children move because IMAP hierarchy lives in the name (there is no
// parent_id column — see the JMAP adapter's parentID): renaming "Work" to
// "Projects" must rewrite "Work/2026" to "Projects/2026", or the child becomes
// an orphan whose parent path names nothing. RFC 3501 §6.3.5 requires the
// SERVER to do the same thing to the real folders, so this mirrors it.
//
// Both halves happen in one transaction: a partial rename would leave a tree
// the JMAP layer renders with a hole in it.
//
// It returns ErrNotFound when no such mailbox belongs to the account, and the
// number of rows changed (the mailbox plus its descendants).
func (s *Store) RenameMailbox(ctx context.Context, accountID, mailboxID int64, newName string) (int, error) {
	if newName == "" {
		return 0, fmt.Errorf("renaming mailbox %d: the new name is empty", mailboxID)
	}

	var renamed int
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		var oldName, delimiter string
		if err := tx.QueryRow(ctx,
			`SELECT name, delimiter FROM mailboxes WHERE id = $1 AND account_id = $2 FOR UPDATE`,
			mailboxID, accountID).Scan(&oldName, &delimiter); err != nil {
			return notFound(err, fmt.Sprintf("mailbox %d", mailboxID))
		}
		if delimiter == "" {
			delimiter = "/"
		}
		if oldName == newName {
			renamed = 0
			return nil
		}

		// The children FIRST, then the mailbox itself. Order matters only for
		// readability here (the whole thing is one transaction), but doing the
		// prefix rewrite while the parent still carries the old name keeps the
		// statement's WHERE clause obvious.
		//
		// The prefix is oldName + delimiter, never oldName alone: a sibling
		// called "Workshop" must not be caught by a rename of "Work".
		//
		// The rewrite is expressed as "strip the old prefix, prepend the new
		// one" rather than a substring with a computed offset, because the
		// offset would be a bare integer parameter that PostgreSQL cannot
		// infer a type for in this position. overlay/substring with an
		// explicit length is the same idea; `$3 || right(name, -length($2))`
		// says it in one expression whose types are all pinned by the columns.
		prefix := oldName + delimiter
		tag, err := tx.Exec(ctx, `
			UPDATE mailboxes
			   SET name = $3 || right(name, -length($4)), updated_at = now()
			 WHERE account_id = $1 AND name LIKE $2 || '%'`,
			accountID, likeEscape(prefix), newName+delimiter, prefix)
		if err != nil {
			return fmt.Errorf("renaming the children of mailbox %d: %w", mailboxID, err)
		}
		renamed = int(tag.RowsAffected())

		tag, err = tx.Exec(ctx, `
			UPDATE mailboxes SET name = $3, updated_at = now()
			 WHERE id = $1 AND account_id = $2`, mailboxID, accountID, newName)
		if err != nil {
			return fmt.Errorf("renaming mailbox %d: %w", mailboxID, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("renaming mailbox %d: %w", mailboxID, ErrNotFound)
		}
		renamed += int(tag.RowsAffected())
		return nil
	})
	if err != nil {
		return 0, err
	}
	return renamed, nil
}

// likeEscape escapes the LIKE metacharacters in a literal prefix.
//
// A mailbox may legitimately be called "50%% off" or "a_b", and without this
// a rename of "a_b" would also rewrite "axb". The default escape character is
// a backslash, which must itself be escaped first.
func likeEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	return strings.ReplaceAll(s, "_", `\_`)
}

// ListChildMailboxes returns the mailboxes whose IMAP path makes them
// descendants of the given one — the "does this mailbox have children" question
// RFC 8621 §2.5's mailboxHasChild answers, and the set a rename carries along.
//
// It is a path-prefix query rather than a parent_id lookup because the store
// has no parent_id column: the hierarchy IS the name (migration 0002).
func (s *Store) ListChildMailboxes(ctx context.Context, accountID, mailboxID int64) ([]Mailbox, error) {
	parent, err := s.GetMailbox(ctx, mailboxID)
	if err != nil {
		return nil, err
	}
	if parent.AccountID != accountID {
		return nil, fmt.Errorf("mailbox %d: %w", mailboxID, ErrNotFound)
	}
	delim := parent.Delimiter
	if delim == "" {
		delim = "/"
	}

	rows, err := s.pool.Query(ctx,
		`SELECT `+mailboxColumns+` FROM mailboxes
		  WHERE account_id = $1 AND name LIKE $2 || '%' ORDER BY name`,
		accountID, likeEscape(parent.Name+delim))
	if err != nil {
		return nil, fmt.Errorf("listing children of mailbox %d: %w", mailboxID, err)
	}
	defer rows.Close()

	var out []Mailbox
	for rows.Next() {
		m, err := scanMailbox(rows)
		if err != nil {
			return nil, fmt.Errorf("listing children of mailbox %d: %w", mailboxID, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing children of mailbox %d: %w", mailboxID, err)
	}
	return out, nil
}

// TombstoneMailbox marks a mailbox's live messages as deleted, without
// removing the mailbox row — the store half of a Mailbox/set destroy (W2).
//
// Tombstones rather than DELETEs for exactly the reason MarkDeleted gives:
// Email/changes must keep reporting those messages as destroyed until every
// client has caught up (RFC 8620 §5.2). A DELETE would make them vanish from
// the change feed and leave clients showing mail that no longer exists.
//
// The mailbox row itself is removed separately by DeleteMailbox, AFTER this,
// so the two are ordered and each is idempotent on replay.
func (s *Store) TombstoneMailbox(ctx context.Context, mailboxID int64) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE message_state
		   SET deleted_at = now(), updated_at = now()
		 WHERE mailbox_id = $1 AND deleted_at IS NULL`, mailboxID)
	if err != nil {
		return 0, fmt.Errorf("tombstoning the messages of mailbox %d: %w", mailboxID, err)
	}
	return int(tag.RowsAffected()), nil
}

// DistinctKeywordsInMailbox returns the distinct user keywords currently in
// use on the LIVE messages of a mailbox, lowercased and sorted.
//
// # Why this query exists (A6 / validation V1)
//
// A Maildir folder holds at most 26 keywords DURABLY: `dovecot-keywords`
// encodes each as one letter a-z in the message filename and stops at index
// 25. Dovecot itself accepts and serves more — V1 put 500 on one message and
// read all 500 back — and they vanish silently the next time the index is
// rebuilt from disk, all at once, possibly weeks later
// (imap.MaxDurableKeywordsPerMailbox documents the measurement).
//
// The server therefore cannot be asked, and the read-back after a STORE cannot
// detect it. The only place the ceiling can be enforced is here, by counting
// what the folder already carries before writing something new into it — which
// is what L2 §2.3 means by "no labels that exist only in the DB, silently".
//
// Case folding matches IMAP's own flag comparison (RFC 3501 §2.3.2:
// keywords are case-insensitive), because "Work" and "work" occupy ONE slot in
// dovecot-keywords, not two. Counting them separately would let a client cross
// the real ceiling while Moov believed it had room.
//
// Tombstoned messages are excluded: their keywords are not in the live folder
// any more. That is a deliberate under-count in one narrow case — Dovecot may
// keep the keyword registered in dovecot-keywords after the last message
// carrying it is expunged, since the file is a per-folder registry rather than
// a per-message one — and the JMAP layer's error message says so rather than
// claiming the number is exact.
func (s *Store) DistinctKeywordsInMailbox(ctx context.Context, mailboxID int64) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT lower(k)
		  FROM message_state ms, unnest(ms.keywords) AS k
		 WHERE ms.mailbox_id = $1 AND ms.deleted_at IS NULL
		 ORDER BY 1`, mailboxID)
	if err != nil {
		return nil, fmt.Errorf("counting the keywords of mailbox %d: %w", mailboxID, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("counting the keywords of mailbox %d: %w", mailboxID, err)
		}
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("counting the keywords of mailbox %d: %w", mailboxID, err)
	}
	return out, nil
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
