package store

import (
	"context"
	"fmt"
	"time"
)

// AuditLine is one accounts-API write (contract §2.4): who did what to which
// address, with what result, under which request id, and why the caller said
// they did it. Rows are append-only and outlive the account they name.
type AuditLine struct {
	ID        int64
	At        time.Time
	ActorID   string
	ActorName string
	Action    string
	Address   string
	Result    string
	RequestID string
	Reason    string
	// Note carries a server-side qualifier the contract names, such as
	// "recreated" for a create that follows a deletion.
	Note string
}

// AppendAudit writes one line.
func (s *Store) AppendAudit(ctx context.Context, l AuditLine) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO account_audit (actor_id, actor_name, action, address, result, request_id, reason, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		l.ActorID, l.ActorName, l.Action, l.Address, l.Result, l.RequestID, l.Reason, l.Note); err != nil {
		return fmt.Errorf("appending audit line: %w", err)
	}
	return nil
}

// ListAudit returns the most recent lines for an address, newest first.
func (s *Store) ListAudit(ctx context.Context, address string, limit int) ([]AuditLine, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, at, actor_id, actor_name, action, address, result, request_id, reason, note
		  FROM account_audit WHERE address = $1 ORDER BY at DESC, id DESC LIMIT $2`,
		address, limit)
	if err != nil {
		return nil, fmt.Errorf("listing audit for %q: %w", address, err)
	}
	defer rows.Close()
	var out []AuditLine
	for rows.Next() {
		var l AuditLine
		if err := rows.Scan(&l.ID, &l.At, &l.ActorID, &l.ActorName, &l.Action, &l.Address,
			&l.Result, &l.RequestID, &l.Reason, &l.Note); err != nil {
			return nil, fmt.Errorf("listing audit for %q: %w", address, err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing audit for %q: %w", address, err)
	}
	return out, nil
}

// HasAuditFor reports whether any line names the address — how a create
// learns it is a RECREATION after a deletion (contract §2.4: audited, never
// silent).
func (s *Store) HasAuditFor(ctx context.Context, address, action string) (bool, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM account_audit WHERE address = $1 AND action = $2 AND result = 'ok'`,
		address, action).Scan(&n); err != nil {
		return false, fmt.Errorf("checking audit for %q: %w", address, err)
	}
	return n > 0, nil
}
