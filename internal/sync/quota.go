package sync

import (
	"context"
	"fmt"
)

// QuotaUsage is one quota resource in this package's own vocabulary, so the
// JMAP layer reads quotas without importing internal/imap (the layer rule
// write_adapter.go states).
type QuotaUsage struct {
	// Root is the quota root name ("User quota" on Dovecot).
	Root string
	// Resource is "STORAGE" (bytes) or "MESSAGE" (count).
	Resource string
	Usage    int64
	Limit    int64
}

// ReadQuota reads the account's IMAP quota through the executor's cached
// per-account connection — the same socket and the same serialization every
// client write uses, so a quota read never opens a second connection per
// account and never interleaves with a write (L3 epic E6, Quota/get's
// transport; IMAP over the Mailcow admin API per the E6 arbitration: fewer
// moving parts, the per-account credential is already dialed, and the value
// Dovecot enforces is the value served).
func (w *WriteExecutor) ReadQuota(ctx context.Context, accountID int64) ([]QuotaUsage, error) {
	account, err := w.store.GetAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("loading account %d: %w", accountID, err)
	}
	ac, err := w.forAccount(account.ID)
	if err != nil {
		return nil, err
	}
	ac.mu.Lock()
	defer ac.mu.Unlock()

	// Two attempts, exactly like withMailbox's dead-idle handling — and the
	// retry is unconditionally safe here, because GETQUOTAROOT is a read: a
	// command with no outcome to double-apply.
	var lastErr error
	for attempt := range 2 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c, err := ac.ensure(ctx, w.connector, account)
		if err != nil {
			return nil, fmt.Errorf("connecting for a quota read: %w", err)
		}
		res, qerr := c.GetQuota(ctx)
		if qerr == nil {
			out := make([]QuotaUsage, 0, len(res))
			for _, r := range res {
				out = append(out, QuotaUsage{Root: r.Root, Resource: r.Resource, Usage: r.Usage, Limit: r.Limit})
			}
			return out, nil
		}
		lastErr = qerr
		if attempt == 0 && isConnectionDead(qerr) {
			ac.discard()
			continue
		}
		break
	}
	return nil, fmt.Errorf("reading the quota for account %d: %w", accountID, lastErr)
}
