package store

import (
	"context"
	"fmt"
	"time"
)

// ServiceAccount is one API key of the per-domain accounts API (migration
// 0012). The secret is never here: only its SHA-256, and only for lookup.
type ServiceAccount struct {
	ID         string
	KeyHash    []byte
	Domain     string
	Scopes     []string
	Name       string
	CreatedAt  time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// Revoked reports whether the key has been revoked.
func (sa ServiceAccount) Revoked() bool { return sa.RevokedAt != nil }

// HasScope reports whether the key carries scope, honoring the contract's
// "accounts:write implies accounts:read".
func (sa ServiceAccount) HasScope(scope string) bool {
	for _, s := range sa.Scopes {
		if s == scope {
			return true
		}
		if scope == ScopeAccountsRead && s == ScopeAccountsWrite {
			return true
		}
	}
	return false
}

// The scopes this version defines (contract §2.1).
const (
	ScopeAccountsRead  = "accounts:read"
	ScopeAccountsWrite = "accounts:write"
)

const serviceAccountColumns = `id, key_hash, domain, scopes, name, created_at, revoked_at, last_used_at`

// CreateServiceAccount inserts a key. The caller has already hashed the secret
// and lower-cased the domain; the CHECK constraint refuses a domain that is
// not lower-case rather than silently folding it.
func (s *Store) CreateServiceAccount(ctx context.Context, sa ServiceAccount) (ServiceAccount, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO service_accounts (id, key_hash, domain, scopes, name)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+serviceAccountColumns,
		sa.ID, sa.KeyHash, sa.Domain, sa.Scopes, sa.Name)
	out, err := scanServiceAccount(row)
	if err != nil {
		if isUniqueViolation(err) {
			return ServiceAccount{}, fmt.Errorf("creating service account %q: already exists", sa.ID)
		}
		return ServiceAccount{}, fmt.Errorf("creating service account %q: %w", sa.ID, err)
	}
	return out, nil
}

// GetServiceAccountByHash resolves a presented key by its hash. Revoked keys
// are returned too — the caller decides, and a revoked key must be
// indistinguishable on the wire from an unknown one, so the distinction is
// only ever logged.
func (s *Store) GetServiceAccountByHash(ctx context.Context, keyHash []byte) (ServiceAccount, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+serviceAccountColumns+`
		FROM service_accounts WHERE key_hash = $1`, keyHash)
	sa, err := scanServiceAccount(row)
	if err != nil {
		return ServiceAccount{}, notFound(err, "service account")
	}
	return sa, nil
}

// GetServiceAccount looks a key up by its id.
func (s *Store) GetServiceAccount(ctx context.Context, id string) (ServiceAccount, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+serviceAccountColumns+`
		FROM service_accounts WHERE id = $1`, id)
	sa, err := scanServiceAccount(row)
	if err != nil {
		return ServiceAccount{}, notFound(err, fmt.Sprintf("service account %q", id))
	}
	return sa, nil
}

// ListServiceAccounts returns every key, oldest first, revoked ones included.
func (s *Store) ListServiceAccounts(ctx context.Context) ([]ServiceAccount, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+serviceAccountColumns+`
		FROM service_accounts ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("listing service accounts: %w", err)
	}
	defer rows.Close()
	var out []ServiceAccount
	for rows.Next() {
		sa, err := scanServiceAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("listing service accounts: %w", err)
		}
		out = append(out, sa)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing service accounts: %w", err)
	}
	return out, nil
}

// RevokeServiceAccount marks a key revoked. Idempotent: revoking twice keeps
// the first timestamp.
func (s *Store) RevokeServiceAccount(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE service_accounts SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, id, at)
	if err != nil {
		return fmt.Errorf("revoking service account %q: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("revoking service account %q: %w", id, ErrNotFound)
	}
	return nil
}

// TouchServiceAccount records a use. Best effort and unconditional; the
// caller throttles so a busy key does not turn every request into a write.
func (s *Store) TouchServiceAccount(ctx context.Context, id string, at time.Time) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE service_accounts SET last_used_at = $2 WHERE id = $1`, id, at); err != nil {
		return fmt.Errorf("touching service account %q: %w", id, err)
	}
	return nil
}

func scanServiceAccount(row scanner) (ServiceAccount, error) {
	var sa ServiceAccount
	err := row.Scan(&sa.ID, &sa.KeyHash, &sa.Domain, &sa.Scopes, &sa.Name,
		&sa.CreatedAt, &sa.RevokedAt, &sa.LastUsedAt)
	return sa, err
}
