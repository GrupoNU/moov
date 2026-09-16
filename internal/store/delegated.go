package store

import (
	"context"
	"fmt"
	"time"
)

// Delegated sign-in persistence (epic M2, migration 0013). The HTTP layer
// (internal/jmaphttp/delegated.go) owns the token format, the hashing and
// every policy decision; this file owns the rows.
//
// Every method takes the caller's clock (`now`) rather than reading the
// database's. Two reasons: the server's expiry arithmetic (TTL, renewal grace,
// absolute lifetime, JWT skew) is done in Go against ONE clock, and a second
// clock on the SQL side would let the two disagree by whatever the hosts drift;
// and the server's tests run against a fake clock, which a `now()` in SQL
// would silently ignore.

// DelegatedSession is one row of delegated_sessions.
type DelegatedSession struct {
	ID        int64
	TokenHash []byte
	AccountID int64
	Issuer    string
	CreatedAt time.Time
	// ExpiresAt is the sliding expiry; AbsoluteExpiresAt is the renewal
	// ceiling. Both are checked on every request by the caller.
	ExpiresAt         time.Time
	AbsoluteExpiresAt time.Time
	RevokedAt         *time.Time
	LastSeenAt        *time.Time
}

const delegatedSessionColumns = `id, token_hash, account_id, issuer, created_at,
	expires_at, absolute_expires_at, revoked_at, last_seen_at`

// CreateDelegatedSession inserts a session and returns it with its id.
func (s *Store) CreateDelegatedSession(ctx context.Context, d DelegatedSession) (DelegatedSession, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO delegated_sessions
		    (token_hash, account_id, issuer, created_at, expires_at, absolute_expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+delegatedSessionColumns,
		d.TokenHash, d.AccountID, d.Issuer, d.CreatedAt, d.ExpiresAt, d.AbsoluteExpiresAt)
	out, err := scanDelegatedSession(row)
	if err != nil {
		return DelegatedSession{}, fmt.Errorf("creating delegated session for account %d: %w", d.AccountID, err)
	}
	return out, nil
}

// GetDelegatedSession looks a session up by its token hash, whatever its
// state: the caller decides what "expired" and "revoked" mean, so it can
// answer a renewal grace and a hard revocation differently. ErrNotFound when
// no row carries the hash.
func (s *Store) GetDelegatedSession(ctx context.Context, tokenHash []byte) (DelegatedSession, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+delegatedSessionColumns+` FROM delegated_sessions WHERE token_hash = $1`, tokenHash)
	out, err := scanDelegatedSession(row)
	if err != nil {
		return DelegatedSession{}, notFound(err, "delegated session")
	}
	return out, nil
}

// TouchDelegatedSession records that the session was used. Best-effort and
// throttled by the caller; a failure here never fails a request.
func (s *Store) TouchDelegatedSession(ctx context.Context, id int64, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE delegated_sessions SET last_seen_at = $2 WHERE id = $1`, id, now)
	if err != nil {
		return fmt.Errorf("touching delegated session %d: %w", id, err)
	}
	return nil
}

// SetDelegatedSessionExpiry moves a session's sliding expiry — the renewal
// path uses it to give the OLD token its 60 s grace instead of killing it.
func (s *Store) SetDelegatedSessionExpiry(ctx context.Context, id int64, expiresAt time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE delegated_sessions SET expires_at = $2 WHERE id = $1 AND revoked_at IS NULL`,
		id, expiresAt)
	if err != nil {
		return fmt.Errorf("setting expiry of delegated session %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("setting expiry of delegated session %d: %w", id, ErrNotFound)
	}
	return nil
}

// RevokeDelegatedSession ends one session. Idempotent: revoking a revoked or
// unknown session is not an error, because sign-out must never fail on the
// user (contract §3.5).
func (s *Store) RevokeDelegatedSession(ctx context.Context, id int64, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE delegated_sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`,
		id, now)
	if err != nil {
		return fmt.Errorf("revoking delegated session %d: %w", id, err)
	}
	return nil
}

// RevokeDelegatedSessionsByIssuer ends every live session of one account that
// was created through one issuer (contract §3.6), returning how many were
// alive. Sessions from another issuer are untouched — that is the whole point
// of recording the issuer on the row.
func (s *Store) RevokeDelegatedSessionsByIssuer(ctx context.Context, accountID int64, issuer string, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE delegated_sessions SET revoked_at = $3
		 WHERE account_id = $1 AND issuer = $2 AND revoked_at IS NULL AND expires_at > $3`,
		accountID, issuer, now)
	if err != nil {
		return 0, fmt.Errorf("revoking delegated sessions of account %d via %q: %w", accountID, issuer, err)
	}
	return tag.RowsAffected(), nil
}

// RevokeDelegatedSessionsForAccount ends every live session of one account,
// whichever issuer created it — the accounts-API suspend/delete path (§2.4).
func (s *Store) RevokeDelegatedSessionsForAccount(ctx context.Context, accountID int64, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE delegated_sessions SET revoked_at = $2
		 WHERE account_id = $1 AND revoked_at IS NULL AND expires_at > $2`,
		accountID, now)
	if err != nil {
		return 0, fmt.Errorf("revoking delegated sessions of account %d: %w", accountID, err)
	}
	return tag.RowsAffected(), nil
}

// CountActiveDelegatedSessions is the sessions-active gauge's source: rows
// neither revoked nor expired at `now`.
func (s *Store) CountActiveDelegatedSessions(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM delegated_sessions
		 WHERE revoked_at IS NULL AND expires_at > $1 AND absolute_expires_at > $1`, now).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting active delegated sessions: %w", err)
	}
	return n, nil
}

// ConsumeDelegatedJTI records an accepted token id and reports whether it was
// FRESH. False means the (issuer, jti) pair was already recorded — a replay —
// and the caller must refuse the token.
//
// The insert IS the check: ON CONFLICT DO NOTHING turns the primary key into
// the replay test, atomically, so two concurrent presentations of one token
// can never both be admitted. Expired rows are swept in the same call, which
// keeps the table a few minutes deep without a background job; the sweep is
// bounded by the index on expires_at.
func (s *Store) ConsumeDelegatedJTI(ctx context.Context, issuer, jti string, expiresAt, now time.Time) (bool, error) {
	if _, err := s.pool.Exec(ctx, `DELETE FROM delegated_jti WHERE expires_at < $1`, now); err != nil {
		return false, fmt.Errorf("sweeping delegated jti cache: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO delegated_jti (issuer, jti, expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (issuer, jti) DO NOTHING`, issuer, jti, expiresAt)
	if err != nil {
		return false, fmt.Errorf("recording delegated jti: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func scanDelegatedSession(row scanner) (DelegatedSession, error) {
	var d DelegatedSession
	err := row.Scan(&d.ID, &d.TokenHash, &d.AccountID, &d.Issuer, &d.CreatedAt,
		&d.ExpiresAt, &d.AbsoluteExpiresAt, &d.RevokedAt, &d.LastSeenAt)
	return d, err
}
