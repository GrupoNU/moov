package mail

import (
	"context"
	"time"

	"github.com/GrupoNU/moov/internal/blob"
)

// The reads J2 needs that internal/store does not expose as methods.
//
// # Why these are here and not in internal/store, where they belong
//
// The J2 scope explicitly excluded modifying internal/store, so these reads
// were written against the pool the store already exports (store.Pool()), and
// each was listed in the J2 report as a store gap with the method signature it
// should become.
//
// TWO OF THE FOUR HAVE SINCE MOVED. The threading pair — resolving a Message-ID
// to a message, and finding the messages that reference one — are now
// store.AssignThreads and store.ThreadMembers, which read a real thread_id
// column instead of deriving a thread per request (migration 0004). What
// remains here are the two reads that are genuinely about the JMAP layer's own
// concerns rather than about mail: a blob ownership check and the /get state
// watermark.
//
// They hold to the store's own rules regardless (search.go's three
// guarantees), because those rules are about the database, not about which
// package the SQL lives in:
//
//   - account_id is in the WHERE clause of every one of them, always;
//   - every read that can return more than one row has a LIMIT;
//   - each is served by an index migration 0002 already creates, named in the
//     comment above the query.

// accountReferencesBlob reports whether an account holds any reference to a
// blob — the ownership check the download route enforces.
//
// Served by blob_refs_sha. It asks blob_refs rather than messages because the
// reference table is the authority on who holds what (migration 0002's comment
// on blob_refs), and because it stays correct when drafts and detached parts
// start holding references without a message row.
//
// EXISTS rather than a count: the question is boolean and the index can stop
// at the first hit.
func (a *Adapter) accountReferencesBlob(ctx context.Context, accountID int64, h blob.Hash) (bool, error) {
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM blob_refs
			 WHERE sha256 = $1 AND account_id = $2
		)`

	var exists bool
	if err := a.store.Pool().QueryRow(ctx, q, h.Bytes(), accountID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// dataState reads the account's change watermark for the /get state string.
//
// Served by message_state_acct_updated — the index migration 0002 creates for
// "Email/changes feeds: everything that moved for an account since a cursor",
// which makes this a single index scan to the extreme.
//
// The count travels with the watermark because a timestamp alone cannot
// distinguish "nothing changed" from "a row was deleted": a tombstone reap
// lowers the count while the maximum updated_at stays put, and a client
// holding the old state must see a different string.
func (a *Adapter) dataState(ctx context.Context, accountID int64) (string, error) {
	const q = `
		SELECT coalesce(max(updated_at), to_timestamp(0)), count(*)
		  FROM message_state
		 WHERE account_id = $1`

	var (
		watermark time.Time
		count     int64
	)
	if err := a.store.Pool().QueryRow(ctx, q, accountID).Scan(&watermark, &count); err != nil {
		return "", err
	}
	if watermark.Unix() <= 0 {
		watermark = time.Time{}
	}
	return stateFor(watermark, count), nil
}
