package store

import (
	"context"
	"fmt"
	"time"
)

// The two E6 ledgers (migration 0010). Neither holds rules: the filter model
// lives in the managed Sieve script on Dovecot (internal/sieve). This file
// holds only what cannot live there — the RFC 9661 id-to-name mapping, and
// the forwarding-address verification facts.

// SieveScriptRow is one row of the id ledger: a stable JMAP id for a
// ManageSieve script name.
type SieveScriptRow struct {
	ID         int64
	AccountID  int64
	Name       string
	ContentSHA string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

const sieveScriptColumns = `id, account_id, name, content_sha, created_at, updated_at`

func scanSieveScript(row scanner) (SieveScriptRow, error) {
	var r SieveScriptRow
	err := row.Scan(&r.ID, &r.AccountID, &r.Name, &r.ContentSHA, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// SyncSieveScripts reconciles the ledger against the names ManageSieve just
// listed: rows appear for new names, rows for vanished names are deleted
// (the deletion moves the watermark out from under any cursor — see
// SieveScriptState's count term). It returns the full ledger, name-keyed
// callers sort as they need.
//
// The ledger is cache, never truth: a wiped ledger re-mints ids on the next
// sync, clients see a changed state string and refetch. That is the A5
// posture, applied to script ids.
func (s *Store) SyncSieveScripts(ctx context.Context, accountID int64, names []string) ([]SieveScriptRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("syncing sieve scripts for account %d: %w", accountID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, name := range names {
		if _, err := tx.Exec(ctx, `
			INSERT INTO sieve_scripts (account_id, name)
			VALUES ($1, $2)
			ON CONFLICT (account_id, name) DO NOTHING`, accountID, name); err != nil {
			return nil, fmt.Errorf("upserting sieve script %q: %w", name, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM sieve_scripts
		 WHERE account_id = $1 AND NOT (name = ANY($2))`, accountID, names); err != nil {
		return nil, fmt.Errorf("pruning sieve scripts: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT `+sieveScriptColumns+` FROM sieve_scripts
		 WHERE account_id = $1 ORDER BY id`, accountID)
	if err != nil {
		return nil, fmt.Errorf("listing sieve scripts: %w", err)
	}
	defer rows.Close()
	var out []SieveScriptRow
	for rows.Next() {
		r, err := scanSieveScript(rows)
		if err != nil {
			return nil, fmt.Errorf("listing sieve scripts: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing sieve scripts: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("syncing sieve scripts: %w", err)
	}
	return out, nil
}

// GetSieveScriptByName resolves one ledger row by its current name.
func (s *Store) GetSieveScriptByName(ctx context.Context, accountID int64, name string) (SieveScriptRow, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+sieveScriptColumns+` FROM sieve_scripts
		 WHERE account_id = $1 AND name = $2`, accountID, name)
	r, err := scanSieveScript(row)
	if err != nil {
		return SieveScriptRow{}, notFound(err, fmt.Sprintf("sieve script %q", name))
	}
	return r, nil
}

// GetSieveScript resolves one ledger row by id, account-scoped.
func (s *Store) GetSieveScript(ctx context.Context, accountID, id int64) (SieveScriptRow, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+sieveScriptColumns+` FROM sieve_scripts
		 WHERE account_id = $1 AND id = $2`, accountID, id)
	r, err := scanSieveScript(row)
	if err != nil {
		return SieveScriptRow{}, notFound(err, fmt.Sprintf("sieve script %d", id))
	}
	return r, nil
}

// UpsertSieveScript ensures a ledger row for a name and returns it.
func (s *Store) UpsertSieveScript(ctx context.Context, accountID int64, name string) (SieveScriptRow, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO sieve_scripts (account_id, name)
		VALUES ($1, $2)
		ON CONFLICT (account_id, name) DO UPDATE SET updated_at = now()
		RETURNING `+sieveScriptColumns, accountID, name)
	r, err := scanSieveScript(row)
	if err != nil {
		return SieveScriptRow{}, fmt.Errorf("upserting sieve script %q: %w", name, err)
	}
	return r, nil
}

// RenameSieveScript points an id at a new name (the RFC 9661 §2.1 contract:
// the id survives the rename).
func (s *Store) RenameSieveScript(ctx context.Context, accountID, id int64, newName string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sieve_scripts SET name = $3, updated_at = now()
		 WHERE account_id = $1 AND id = $2`, accountID, id, newName)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("renaming sieve script %d to %q: name taken", id, newName)
		}
		return fmt.Errorf("renaming sieve script %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("renaming sieve script %d: %w", id, ErrNotFound)
	}
	return nil
}

// SetSieveScriptSHA records the content digest last seen for a script and
// moves its watermark.
func (s *Store) SetSieveScriptSHA(ctx context.Context, accountID, id int64, sha string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sieve_scripts SET content_sha = $3, updated_at = now()
		 WHERE account_id = $1 AND id = $2 AND content_sha <> $3`, accountID, id, sha)
	if err != nil {
		return fmt.Errorf("recording sieve script %d content: %w", id, err)
	}
	_ = tag // an unchanged sha legitimately affects zero rows
	return nil
}

// DeleteSieveScript removes a ledger row.
func (s *Store) DeleteSieveScript(ctx context.Context, accountID, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM sieve_scripts WHERE account_id = $1 AND id = $2`, accountID, id)
	if err != nil {
		return fmt.Errorf("deleting sieve script %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("deleting sieve script %d: %w", id, ErrNotFound)
	}
	return nil
}

// SieveScriptWatermark and CountSieveScripts feed the adapter's state cursor,
// in the same "<nanos>-<count>" grammar every other type uses.
func (s *Store) SieveScriptWatermark(ctx context.Context, accountID int64) (time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT max(updated_at) FROM sieve_scripts WHERE account_id = $1`, accountID).Scan(&t)
	if err != nil {
		return time.Time{}, fmt.Errorf("sieve script watermark for account %d: %w", accountID, err)
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

// CountSieveScripts is the count term of the SieveScript state cursor.
func (s *Store) CountSieveScripts(ctx context.Context, accountID int64) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM sieve_scripts WHERE account_id = $1`, accountID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting sieve scripts for account %d: %w", accountID, err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// forwarding addresses
// ---------------------------------------------------------------------------

// Forwarding address states.
const (
	ForwardingPending  = "pending"
	ForwardingAccepted = "accepted"
)

// ForwardingAddress is one destination and its verification state.
type ForwardingAddress struct {
	ID             int64
	AccountID      int64
	Email          string
	State          string
	TokenExpiresAt *time.Time
	VerifiedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const forwardingColumns = `id, account_id, email, state, token_expires_at, verified_at, created_at, updated_at`

func scanForwarding(row scanner) (ForwardingAddress, error) {
	var f ForwardingAddress
	err := row.Scan(&f.ID, &f.AccountID, &f.Email, &f.State,
		&f.TokenExpiresAt, &f.VerifiedAt, &f.CreatedAt, &f.UpdatedAt)
	return f, err
}

// CreateForwardingAddress inserts a pending row. The email must arrive
// lowercased (the adapter normalizes); a duplicate is an error the JMAP
// layer maps to alreadyExists.
func (s *Store) CreateForwardingAddress(ctx context.Context, accountID int64, email string, tokenExpires time.Time) (ForwardingAddress, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO forwarding_addresses (account_id, email, token_expires_at)
		VALUES ($1, $2, $3)
		RETURNING `+forwardingColumns, accountID, email, tokenExpires)
	f, err := scanForwarding(row)
	if err != nil {
		if isUniqueViolation(err) {
			return ForwardingAddress{}, fmt.Errorf("forwarding address %q: already exists", email)
		}
		return ForwardingAddress{}, fmt.Errorf("creating forwarding address %q: %w", email, err)
	}
	return f, nil
}

// AcceptForwardingAddress flips a pending row to accepted. It is idempotent:
// accepting an accepted address succeeds without moving verified_at, so a
// twice-clicked verification link answers success both times.
func (s *Store) AcceptForwardingAddress(ctx context.Context, accountID int64, email string) (ForwardingAddress, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE forwarding_addresses
		   SET state = 'accepted',
		       verified_at = COALESCE(verified_at, now()),
		       token_expires_at = NULL,
		       updated_at = now()
		 WHERE account_id = $1 AND email = $2
		RETURNING `+forwardingColumns, accountID, email)
	f, err := scanForwarding(row)
	if err != nil {
		return ForwardingAddress{}, notFound(err, fmt.Sprintf("forwarding address %q", email))
	}
	return f, nil
}

// RenewForwardingToken moves the pending row's expiry for a re-sent
// verification mail.
func (s *Store) RenewForwardingToken(ctx context.Context, accountID, id int64, tokenExpires time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE forwarding_addresses
		   SET token_expires_at = $3, updated_at = now()
		 WHERE account_id = $1 AND id = $2 AND state = 'pending'`, accountID, id, tokenExpires)
	if err != nil {
		return fmt.Errorf("renewing forwarding token %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("renewing forwarding token %d: %w", id, ErrNotFound)
	}
	return nil
}

// GetForwardingAddress resolves one row by id, account-scoped.
func (s *Store) GetForwardingAddress(ctx context.Context, accountID, id int64) (ForwardingAddress, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+forwardingColumns+` FROM forwarding_addresses
		 WHERE account_id = $1 AND id = $2`, accountID, id)
	f, err := scanForwarding(row)
	if err != nil {
		return ForwardingAddress{}, notFound(err, fmt.Sprintf("forwarding address %d", id))
	}
	return f, nil
}

// ListForwardingAddresses returns the account's rows, oldest first.
func (s *Store) ListForwardingAddresses(ctx context.Context, accountID int64) ([]ForwardingAddress, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+forwardingColumns+` FROM forwarding_addresses
		 WHERE account_id = $1 ORDER BY id`, accountID)
	if err != nil {
		return nil, fmt.Errorf("listing forwarding addresses: %w", err)
	}
	defer rows.Close()
	var out []ForwardingAddress
	for rows.Next() {
		f, err := scanForwarding(rows)
		if err != nil {
			return nil, fmt.Errorf("listing forwarding addresses: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing forwarding addresses: %w", err)
	}
	return out, nil
}

// AcceptedForwardingAddresses returns the lowercased set the script
// generator may target — THE enforcement input of GC-4's verified-forward
// rule.
func (s *Store) AcceptedForwardingAddresses(ctx context.Context, accountID int64) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT email FROM forwarding_addresses
		 WHERE account_id = $1 AND state = 'accepted'`, accountID)
	if err != nil {
		return nil, fmt.Errorf("listing accepted forwarding addresses: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, fmt.Errorf("listing accepted forwarding addresses: %w", err)
		}
		out[email] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing accepted forwarding addresses: %w", err)
	}
	return out, nil
}

// DeleteForwardingAddress removes a row.
func (s *Store) DeleteForwardingAddress(ctx context.Context, accountID, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM forwarding_addresses WHERE account_id = $1 AND id = $2`, accountID, id)
	if err != nil {
		return fmt.Errorf("deleting forwarding address %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("deleting forwarding address %d: %w", id, ErrNotFound)
	}
	return nil
}

// ForwardingWatermark and CountForwardingAddresses feed the state cursor.
func (s *Store) ForwardingWatermark(ctx context.Context, accountID int64) (time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT max(updated_at) FROM forwarding_addresses WHERE account_id = $1`, accountID).Scan(&t)
	if err != nil {
		return time.Time{}, fmt.Errorf("forwarding watermark for account %d: %w", accountID, err)
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

// CountForwardingAddresses is the count term of the forwarding state cursor.
func (s *Store) CountForwardingAddresses(ctx context.Context, accountID int64) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM forwarding_addresses WHERE account_id = $1`, accountID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting forwarding addresses for account %d: %w", accountID, err)
	}
	return n, nil
}
