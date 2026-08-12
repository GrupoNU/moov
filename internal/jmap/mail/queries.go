package mail

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/store"
)

// The reads J2 needs that internal/store does not expose as methods.
//
// # Why these are here and not in internal/store, where they belong
//
// The J2 scope explicitly excludes modifying internal/store: the store is
// another epic's surface and a concurrent change there would collide. These
// four reads are therefore written against the pool the store already exports
// (store.Pool()), and every one of them is listed in the J2 report as a store
// gap with the method signature it should become.
//
// They hold to the store's own rules regardless (search.go's three
// guarantees), because those rules are about the database, not about which
// package the SQL lives in:
//
//   - account_id is in the WHERE clause of every one of them, always;
//   - every read that can return more than one row has a LIMIT;
//   - each is served by an index migration 0002 already creates, named in the
//     comment above the query.
//
// When they move into the store, this file disappears and adapter.go's calls
// change name only.

// messageByMessageID resolves a Message-ID header to a stored message id.
//
// Served by messages_acct_msgid — the partial index migration 0002 creates
// with the comment "Threading (JWZ) resolves parents by Message-ID within an
// account", which is exactly this query.
//
// LIMIT 1 because a Message-ID is supposed to be unique but is not
// guaranteed to be: duplicates occur with mailing lists that deliver a copy to
// several folders. The oldest wins, so a thread root is stable rather than
// depending on which duplicate the planner reaches first.
func (a *Adapter) messageByMessageID(ctx context.Context, accountID int64, messageID string) (int64, error) {
	const q = `
		SELECT id FROM messages
		 WHERE account_id = $1 AND message_id = $2
		 ORDER BY id
		 LIMIT 1`

	var id int64
	err := a.store.Pool().QueryRow(ctx, q, accountID, messageID).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return id, nil
}

// messagesReferencing returns the account's messages whose References or
// In-Reply-To name the given Message-ID — the descendants of a thread root.
//
// The bound is deliberate and load-bearing: a thread is a conversation, and a
// conversation with more than threadMemberLimit messages is either a mailing
// list or an abuse case. Returning the oldest N keeps Thread/get bounded in
// the same spirit as the store's own LIMIT rule, and the cap is far above any
// real thread.
//
// Tombstoned messages are excluded: a destroyed message is not a member of a
// thread any more.
func (a *Adapter) messagesReferencing(ctx context.Context, accountID int64, messageID string) ([]store.Message, error) {
	const q = `
		SELECT m.id, m.date, m.internal_date
		  FROM messages m
		  JOIN message_state ms ON ms.message_id = m.id
		 WHERE m.account_id = $1
		   AND ms.deleted_at IS NULL
		   AND ($2 = ANY(m.references_ids) OR m.in_reply_to = $2)
		 ORDER BY m.id
		 LIMIT $3`

	rows, err := a.store.Pool().Query(ctx, q, accountID, messageID, threadMemberLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.Message
	for rows.Next() {
		var m store.Message
		if err := rows.Scan(&m.ID, &m.Date, &m.InternalDate); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// threadMemberLimit bounds a derived thread. Chosen well above any real
// conversation: the longest threads in the reference mailboxes are in the low
// hundreds.
const threadMemberLimit = 500

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
