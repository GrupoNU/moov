package blob

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Pin expiry — the retention half of the JMAP upload story (RFC 8620 §6.1).
//
// An upload takes a 'pin' reference owned by the uploading account
// (mail.Adapter.UploadBlob), which keeps the bytes alive and account-scoped
// while a draft is being composed. §6.1 lets the server delete an upload
// "if it is not referenced by another object" after a retention period; the
// pin IS a reference, so retention here means expiring the pin itself — after
// which the ordinary refcount/GC machinery decides, exactly as for any other
// dereferenced blob: if a message attached the bytes meanwhile, the message's
// own reference holds them; if nothing did, the GC grace period runs and the
// bytes go.

// ExpirePins removes pin references older than maxAge and returns how many it
// removed. limit bounds one pass, like GC's.
//
// It follows AddRef/RemoveRef's locking discipline to the letter: the blob
// row is locked before blob_refs is touched and the refcount is recomputed
// from the rows, per hash, so an expiry can never race an AddRef into a wrong
// count (blob.go documents why that invariant is load-bearing).
func (s *Store) ExpirePins(ctx context.Context, maxAge time.Duration, limit int) (int, error) {
	if limit <= 0 {
		limit = 1000
	}
	cutoff := time.Now().Add(-maxAge)

	// The candidate hashes first, outside any lock: the per-hash transaction
	// below re-checks under its lock, same shape as GC's collectOne.
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT sha256 FROM blob_refs
		 WHERE owner_kind = 'pin' AND created_at < $1
		 LIMIT $2`, cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("blob: scanning expired pins: %w", err)
	}
	var hashes []Hash
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return 0, fmt.Errorf("blob: scanning expired pins: %w", err)
		}
		h, err := HashFromBytes(raw)
		if err != nil {
			rows.Close()
			return 0, err
		}
		hashes = append(hashes, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("blob: scanning expired pins: %w", err)
	}

	removed := 0
	for _, h := range hashes {
		n, err := s.expirePinsOf(ctx, h, cutoff)
		if err != nil {
			return removed, err
		}
		removed += n
	}
	return removed, nil
}

// expirePinsOf removes one blob's expired pins under its row lock.
func (s *Store) expirePinsOf(ctx context.Context, h Hash, cutoff time.Time) (int, error) {
	var removed int64
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var refcount int64
		if err := tx.QueryRow(ctx, `SELECT refcount FROM blobs WHERE sha256 = $1 FOR UPDATE`,
			h.Bytes()).Scan(&refcount); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // collected meanwhile; its refs are gone with it
			}
			return fmt.Errorf("blob: locking %s for pin expiry: %w", h, err)
		}
		tag, err := tx.Exec(ctx, `
			DELETE FROM blob_refs
			 WHERE sha256 = $1 AND owner_kind = 'pin' AND created_at < $2`,
			h.Bytes(), cutoff)
		if err != nil {
			return fmt.Errorf("blob: expiring pins of %s: %w", h, err)
		}
		removed = tag.RowsAffected()
		if removed == 0 {
			return nil
		}
		return syncRefcount(ctx, tx, h)
	})
	return int(removed), err
}
