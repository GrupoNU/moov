package store

import (
	"context"
	"fmt"
	"time"
)

const syncLogColumns = `account_id, scope, checkpoint, state_counter,
	last_success_at, last_error, last_error_at, consecutive_errors,
	breaker_state, breaker_until, updated_at`

// GetCheckpoint reads the sync state for an account and scope.
//
// A missing row is not an error: an account that has never synced has no
// checkpoint, and forcing every caller to distinguish ErrNotFound from a
// genuine failure would produce the same defaulting logic at every call site.
// The zero-valued checkpoint it returns instead means "start from the
// beginning", which is exactly what the sync engine should do.
func (s *Store) GetCheckpoint(ctx context.Context, accountID int64, scope string) (SyncCheckpoint, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+syncLogColumns+`
		FROM sync_log WHERE account_id = $1 AND scope = $2`, accountID, scope)

	cp, err := scanCheckpoint(row)
	if err != nil {
		if isNoRows(err) {
			return SyncCheckpoint{
				AccountID:    accountID,
				Scope:        scope,
				Checkpoint:   []byte("{}"),
				BreakerState: BreakerClosed,
			}, nil
		}
		return SyncCheckpoint{}, fmt.Errorf("reading checkpoint %d/%s: %w", accountID, scope, err)
	}
	return cp, nil
}

// SaveCheckpoint records progress after a successful sync pass.
//
// It clears the error history and closes the breaker: reaching this call means
// the account is talking to Dovecot again, so leaving a stale error or an open
// breaker behind would be a lie the next pass has to work around.
//
// state_counter is incremented monotonically and never reset, because JMAP
// hands it to clients as the state string of Email/changes — a counter that
// moved backwards would make clients silently miss changes.
func (s *Store) SaveCheckpoint(ctx context.Context, accountID int64, scope string, checkpoint []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sync_log (account_id, scope, checkpoint, state_counter,
		                      last_success_at, consecutive_errors, breaker_state)
		VALUES ($1, $2, $3, 1, now(), 0, 'closed')
		ON CONFLICT (account_id, scope) DO UPDATE
		   SET checkpoint         = EXCLUDED.checkpoint,
		       state_counter      = sync_log.state_counter + 1,
		       last_success_at    = now(),
		       last_error         = NULL,
		       last_error_at      = NULL,
		       consecutive_errors = 0,
		       breaker_state      = 'closed',
		       breaker_until      = NULL,
		       updated_at         = now()`,
		accountID, scope, jsonOrEmptyObject(checkpoint))
	if err != nil {
		return fmt.Errorf("saving checkpoint %d/%s: %w", accountID, scope, err)
	}
	return nil
}

// RecordSyncError increments the consecutive-error counter and stores the
// message, leaving the checkpoint itself untouched so a retry resumes from the
// last known-good point.
//
// It returns the new consecutive-error count, which is what the caller uses to
// decide whether to trip the breaker.
func (s *Store) RecordSyncError(ctx context.Context, accountID int64, scope, message string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		INSERT INTO sync_log (account_id, scope, last_error, last_error_at, consecutive_errors)
		VALUES ($1, $2, $3, now(), 1)
		ON CONFLICT (account_id, scope) DO UPDATE
		   SET last_error         = EXCLUDED.last_error,
		       last_error_at      = now(),
		       consecutive_errors = sync_log.consecutive_errors + 1,
		       updated_at         = now()
		RETURNING consecutive_errors`,
		accountID, scope, message).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("recording sync error %d/%s: %w", accountID, scope, err)
	}
	return count, nil
}

// SetBreakerState opens, half-opens or closes the per-account circuit breaker.
//
// The breaker is not a nicety: repeated failed logins against Mailcow trip its
// fail2ban and get the whole engine's IP banned (ADR §4), so an account whose
// credentials went bad must stop retrying rather than retry faster.
func (s *Store) SetBreakerState(ctx context.Context, accountID int64, scope string, state BreakerState, until *time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sync_log (account_id, scope, breaker_state, breaker_until)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (account_id, scope) DO UPDATE
		   SET breaker_state = EXCLUDED.breaker_state,
		       breaker_until = EXCLUDED.breaker_until,
		       updated_at    = now()`,
		accountID, scope, state, until)
	if err != nil {
		return fmt.Errorf("setting breaker state %d/%s: %w", accountID, scope, err)
	}
	return nil
}

// ListCheckpoints returns every scope's state for an account, which is what an
// operator and the E8 metrics exporter read.
func (s *Store) ListCheckpoints(ctx context.Context, accountID int64) ([]SyncCheckpoint, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+syncLogColumns+`
		FROM sync_log WHERE account_id = $1 ORDER BY scope`, accountID)
	if err != nil {
		return nil, fmt.Errorf("listing checkpoints: %w", err)
	}
	defer rows.Close()

	var out []SyncCheckpoint
	for rows.Next() {
		cp, err := scanCheckpoint(rows)
		if err != nil {
			return nil, fmt.Errorf("listing checkpoints: %w", err)
		}
		out = append(out, cp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing checkpoints: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// intents (L2 §4.3)
// ---------------------------------------------------------------------------

const intentColumns = `id, account_id, kind, payload, state, attempts,
	last_error, not_before, created_at, updated_at`

// EnqueueIntent queues a client write for the sync engine to execute against
// IMAP. This is the only way the JMAP layer causes a change on the server.
//
// notBefore lets a caller delay execution — which is how undo-send works: the
// message is queued a few seconds in the future and can be canceled before
// the engine picks it up.
func (s *Store) EnqueueIntent(ctx context.Context, accountID int64, kind IntentKind, payload []byte, notBefore time.Time) (Intent, error) {
	if notBefore.IsZero() {
		notBefore = time.Now()
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO intents (account_id, kind, payload, not_before)
		VALUES ($1, $2, $3, $4)
		RETURNING `+intentColumns,
		accountID, kind, jsonOrEmptyObject(payload), notBefore)

	in, err := scanIntent(row)
	if err != nil {
		return Intent{}, fmt.Errorf("enqueuing %s intent: %w", kind, err)
	}
	return in, nil
}

// ClaimIntents atomically takes up to limit runnable intents for an account
// and marks them in_flight.
//
// FOR UPDATE SKIP LOCKED is what makes this safe with several workers: a
// worker takes rows nobody else holds and never blocks on a row another worker
// is already executing. Without SKIP LOCKED, two workers would serialize on
// the queue head; without FOR UPDATE, they would both execute the same intent,
// which for a 'send' means the mail goes out twice.
func (s *Store) ClaimIntents(ctx context.Context, accountID int64, limit int) ([]Intent, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE intents
		   SET state = 'in_flight', attempts = attempts + 1, updated_at = now()
		 WHERE id IN (
		     SELECT id FROM intents
		      WHERE account_id = $1 AND state = 'queued' AND not_before <= now()
		      ORDER BY not_before, id
		      LIMIT $2
		      FOR UPDATE SKIP LOCKED
		 )
		RETURNING `+intentColumns, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("claiming intents: %w", err)
	}
	defer rows.Close()

	var out []Intent
	for rows.Next() {
		in, err := scanIntent(rows)
		if err != nil {
			return nil, fmt.Errorf("claiming intents: %w", err)
		}
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claiming intents: %w", err)
	}
	return out, nil
}

// CompleteIntent marks an intent done.
func (s *Store) CompleteIntent(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE intents SET state = 'done', last_error = NULL, updated_at = now()
		 WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("completing intent %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("completing intent %d: %w", id, ErrNotFound)
	}
	return nil
}

// FailIntent records a failure. retryAt non-nil returns the intent to the
// queue at that time; nil marks it permanently failed.
func (s *Store) FailIntent(ctx context.Context, id int64, message string, retryAt *time.Time) error {
	state := IntentFailed
	notBefore := time.Now()
	if retryAt != nil {
		state = IntentQueued
		notBefore = *retryAt
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE intents
		   SET state = $2, last_error = $3, not_before = $4, updated_at = now()
		 WHERE id = $1`, id, state, message, notBefore)
	if err != nil {
		return fmt.Errorf("failing intent %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("failing intent %d: %w", id, ErrNotFound)
	}
	return nil
}

// GetIntent reads one intent by id.
func (s *Store) GetIntent(ctx context.Context, id int64) (Intent, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+intentColumns+` FROM intents WHERE id = $1`, id)
	in, err := scanIntent(row)
	if err != nil {
		return Intent{}, notFound(err, fmt.Sprintf("intent %d", id))
	}
	return in, nil
}

// ---------------------------------------------------------------------------
// scanning
// ---------------------------------------------------------------------------

func scanCheckpoint(row scanner) (SyncCheckpoint, error) {
	var cp SyncCheckpoint
	var lastError *string
	err := row.Scan(&cp.AccountID, &cp.Scope, &cp.Checkpoint, &cp.StateCounter,
		&cp.LastSuccessAt, &lastError, &cp.LastErrorAt, &cp.ConsecutiveErrors,
		&cp.BreakerState, &cp.BreakerUntil, &cp.UpdatedAt)
	if lastError != nil {
		cp.LastError = *lastError
	}
	return cp, err
}

func scanIntent(row scanner) (Intent, error) {
	var in Intent
	var lastError *string
	err := row.Scan(&in.ID, &in.AccountID, &in.Kind, &in.Payload, &in.State,
		&in.Attempts, &lastError, &in.NotBefore, &in.CreatedAt, &in.UpdatedAt)
	if lastError != nil {
		in.LastError = *lastError
	}
	return in, err
}
