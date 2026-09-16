package store

import (
	"context"
	"fmt"
	"time"
)

// The accounts-API writes on the accounts table (migration 0012). Each method
// changes exactly the facts one contract transition names, in one statement,
// so a transition is atomic from the store's point of view and the row can
// never be observed half-way through it.

// AccountFacts is what the accounts API sets on creation and on PATCH: the
// mirrored name and quota, and the Moov-enforced limits. A nil limit leaves
// the column NULL ("installation default"); DisplayName and QuotaMB are
// written as given.
type AccountFacts struct {
	DisplayName          string
	QuotaMB              int
	SendPerDay           *int
	RecipientsPerMessage *int
	AttachmentMB         *int
}

// SetAccountFacts writes the API-managed facts of an account.
func (s *Store) SetAccountFacts(ctx context.Context, accountID int64, f AccountFacts) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts
		   SET display_name = $2, quota_mb = $3, send_per_day = $4,
		       recipients_per_message = $5, attachment_mb = $6, updated_at = now()
		 WHERE id = $1`,
		accountID, f.DisplayName, f.QuotaMB, f.SendPerDay, f.RecipientsPerMessage, f.AttachmentMB)
	if err != nil {
		return fmt.Errorf("setting facts for account %d: %w", accountID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("setting facts for account %d: %w", accountID, ErrNotFound)
	}
	return nil
}

// SetAccountAppPasswordID records which Mailcow app password the stored
// credential is, so a later re-issue can delete the old one. 0 clears it.
func (s *Store) SetAccountAppPasswordID(ctx context.Context, accountID int64, id int64) error {
	var v *int64
	if id > 0 {
		v = &id
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts SET mailcow_app_password_id = $2, updated_at = now() WHERE id = $1`,
		accountID, v)
	if err != nil {
		return fmt.Errorf("setting app password id for account %d: %w", accountID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("setting app password id for account %d: %w", accountID, ErrNotFound)
	}
	return nil
}

// SetAccountSuspended records a suspension or its lift.
//
// The engine state moves with the fact: suspended ⇒ disabled, which is the
// gate the authenticator and the sync supervisor already honour; lifted ⇒
// active. It never touches read_only, so a suspended read-only account comes
// back read-only (contract §2.4, deviation D3).
func (s *Store) SetAccountSuspended(ctx context.Context, accountID int64, suspended bool, at time.Time) error {
	state := AccountActive
	var when *time.Time
	if suspended {
		state = AccountDisabled
		when = &at
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts
		   SET suspended = $2, suspended_at = $3, state = $4, updated_at = now()
		 WHERE id = $1 AND deleting_since IS NULL`,
		accountID, suspended, when, state)
	if err != nil {
		return fmt.Errorf("setting suspension for account %d: %w", accountID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("setting suspension for account %d: %w", accountID, ErrNotFound)
	}
	return nil
}

// SetAccountReadOnly records the retention lock. It is written after the
// credential was re-issued (SetAccountCredentials) so the flag never claims a
// lock the app password does not enforce.
func (s *Store) SetAccountReadOnly(ctx context.Context, accountID int64, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts
		   SET read_only = true, read_only_since = COALESCE(read_only_since, $2), updated_at = now()
		 WHERE id = $1 AND deleting_since IS NULL`,
		accountID, at)
	if err != nil {
		return fmt.Errorf("setting read-only for account %d: %w", accountID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("setting read-only for account %d: %w", accountID, ErrNotFound)
	}
	return nil
}

// MarkAccountDeleting starts the purge: the row is disabled (sessions fail on
// their next request, the supervisor drops it), the credential is marked
// revoked, and deleting_since is set. It is a compare-and-set: a second call
// on an account already deleting reports ErrNotFound, which is how the API's
// "409 while deleting" is decided without a read-then-write race.
func (s *Store) MarkAccountDeleting(ctx context.Context, accountID int64, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts
		   SET deleting_since = $2, state = 'disabled', credential_state = 'revoked',
		       updated_at = now()
		 WHERE id = $1 AND deleting_since IS NULL`,
		accountID, at)
	if err != nil {
		return fmt.Errorf("marking account %d deleting: %w", accountID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("marking account %d deleting: %w", accountID, ErrNotFound)
	}
	return nil
}

// TouchAccountAccess records an authenticated request by the mailbox. The
// caller throttles; this method is one unconditional UPDATE.
func (s *Store) TouchAccountAccess(ctx context.Context, accountID int64, at time.Time) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE accounts SET last_access_at = $2 WHERE id = $1`, accountID, at); err != nil {
		return fmt.Errorf("touching access for account %d: %w", accountID, err)
	}
	return nil
}

// ListDeletingAccounts returns the accounts whose purge started and has not
// finished — what a restarted daemon resumes.
func (s *Store) ListDeletingAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+accountColumns+`
		FROM accounts WHERE deleting_since IS NOT NULL ORDER BY deleting_since`)
	if err != nil {
		return nil, fmt.Errorf("listing deleting accounts: %w", err)
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("listing deleting accounts: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing deleting accounts: %w", err)
	}
	return out, nil
}

// AccountBlobHashes returns the distinct raw-message hashes an account
// references, for the purge to offer to the blob collector once the rows are
// gone. Content-addressed blobs may be shared with another account, so the
// collector — not this list — decides what is actually removed.
func (s *Store) AccountBlobHashes(ctx context.Context, accountID int64) ([][]byte, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT raw_sha256 FROM messages WHERE account_id = $1`, accountID)
	if err != nil {
		return nil, fmt.Errorf("listing blob hashes of account %d: %w", accountID, err)
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var h []byte
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("listing blob hashes of account %d: %w", accountID, err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing blob hashes of account %d: %w", accountID, err)
	}
	return out, nil
}

// AccountSyncSummary is what the accounts API reports as `sync`: the message
// count Moov holds, the most recent mailbox sync, whether any mailbox has been
// synced at all, and whether a breaker is open.
type AccountSyncSummary struct {
	Messages    int64
	LastSyncAt  *time.Time
	EverSynced  bool
	BreakerOpen bool
}

// AccountSyncSummary reads the sync facts of one account in two queries.
func (s *Store) AccountSyncSummary(ctx context.Context, accountID int64) (AccountSyncSummary, error) {
	var out AccountSyncSummary
	err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM message_state
		         WHERE account_id = $1 AND deleted_at IS NULL),
		       (SELECT max(last_synced_at) FROM mailboxes WHERE account_id = $1),
		       (SELECT count(*) > 0 FROM mailboxes
		         WHERE account_id = $1 AND last_synced_at IS NOT NULL),
		       (SELECT count(*) > 0 FROM sync_log
		         WHERE account_id = $1 AND breaker_state = 'open')`,
		accountID).Scan(&out.Messages, &out.LastSyncAt, &out.EverSynced, &out.BreakerOpen)
	if err != nil {
		return AccountSyncSummary{}, fmt.Errorf("summarizing sync of account %d: %w", accountID, err)
	}
	return out, nil
}
