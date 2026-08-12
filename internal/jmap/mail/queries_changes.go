package mail

import (
	"context"
	"time"
)

// The reads J3 needs that internal/store does not expose as methods.
//
// # Why these are here and not in internal/store, where they belong
//
// Same reason and same rules as queries.go (J2): the store is another epic's
// surface, and the J3 scope excludes modifying it. Every query below is
// written against store.Pool() and is listed in the J3 report as a store gap
// with the method signature it should become.
//
// They hold to the store's own three guarantees regardless (search.go), because
// those rules are about the database, not about which package the SQL lives in:
//
//   - account_id is in the WHERE clause of every one of them, always;
//   - every read that can return more than one row has a LIMIT;
//   - each is served by an index migration 0002 already creates, named in the
//     comment above the query.
//
// # Why the store's own ChangedSince is not enough
//
// store.ChangedSince returns MessageState rows. RFC 8620 §5.2 needs one fact
// those rows do not carry: WHEN THE MESSAGE WAS CREATED. Without it a server
// cannot tell "created since the old state" from "updated since the old
// state", and the §5.2 coalescing rules are stated entirely in those terms.
// The obvious workaround — call it created if the client has never seen it —
// requires knowing what the client has seen, which is exactly what a stateless
// cursor does not encode.
//
// So changedSinceRows is store.ChangedSince plus a join to messages.created_at.
// The store method it should become is:
//
//	func (s *Store) ChangedSinceWithCreation(ctx, accountID int64, since time.Time, limit int) ([]MessageChange, error)

// changedSinceRows reads the account's changes after a cursor, oldest first.
//
// Served by message_state_acct_updated — the index migration 0002 creates for
// "Email/changes feeds: everything that moved for an account since a cursor".
// The join to messages is on the primary key, which is why it does not disturb
// the plan (the same reasoning search.go gives for its own message_state join).
//
// ORDER BY updated_at, message_id: the timestamp is the cursor, and the id
// breaks ties so that the order is TOTAL. That totality is what makes paging
// safe — two rows sharing a microsecond would otherwise be able to swap places
// between two calls, and a maxChanges split could then return one of them
// twice or neither.
func (a *Adapter) changedSinceRows(ctx context.Context, accountID int64, since time.Time, limit int) ([]ChangeRow, error) {
	if limit <= 0 {
		limit = defaultChangesLimit
	}
	const q = `
		SELECT ms.message_id, ms.mailbox_id, m.created_at, ms.updated_at,
		       (ms.deleted_at IS NOT NULL) AS destroyed
		  FROM message_state ms
		  JOIN messages m ON m.id = ms.message_id
		 WHERE ms.account_id = $1 AND ms.updated_at > $2
		 ORDER BY ms.updated_at, ms.message_id
		 LIMIT $3`

	rows, err := a.store.Pool().Query(ctx, q, accountID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ChangeRow
	for rows.Next() {
		var c ChangeRow
		if err := rows.Scan(&c.MessageID, &c.MailboxID, &c.CreatedAt, &c.UpdatedAt, &c.Destroyed); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// newestChangeAt returns the account's current change watermark.
//
// It is deliberately the SAME max(updated_at) that queries.go dataState reads
// for the /get state string, so a cursor issued by a /get can never look like
// a future cursor to the check in changes.go — the two would otherwise be able
// to disagree, and a client would be told to reload on every call.
//
// A min() over the same column looks like the more natural "can I still
// enumerate from here?" test and is NOT: updated_at is rewritten in place, so
// the minimum moves forward as messages are touched. changes.go
// checkCursorReachable carries the full reasoning and the measurement.
//
// Served by message_state_acct_updated — a single index scan to the last row.
//
// Should become: func (s *Store) NewestChangeAt(ctx, accountID) (time.Time, error)
func (a *Adapter) newestChangeAt(ctx context.Context, accountID int64) (time.Time, error) {
	const q = `
		SELECT coalesce(max(updated_at), to_timestamp(0))
		  FROM message_state
		 WHERE account_id = $1`

	var t time.Time
	if err := a.store.Pool().QueryRow(ctx, q, accountID).Scan(&t); err != nil {
		return time.Time{}, err
	}
	if t.Unix() <= 0 {
		return time.Time{}, nil
	}
	return t, nil
}

// mailboxesWithMessageChanges returns the mailboxes whose message contents
// moved after a cursor — i.e. whose COUNTS may have changed.
//
// DISTINCT over the same account-scoped index walk. The LIMIT bounds it at the
// number of mailboxes an account can have, which is small, and it is applied
// after the distinct so a busy account does not report a truncated folder set.
//
// Should become: func (s *Store) MailboxesWithChangesSince(ctx, accountID, since, limit) ([]int64, error)
func (a *Adapter) mailboxesWithMessageChanges(ctx context.Context, accountID int64, since time.Time, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = maxMailboxesPerChanges
	}
	const q = `
		SELECT DISTINCT mailbox_id
		  FROM message_state
		 WHERE account_id = $1 AND updated_at > $2
		 ORDER BY mailbox_id
		 LIMIT $3`

	rows, err := a.store.Pool().Query(ctx, q, accountID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// mailboxRowsChangedSince returns the mailboxes whose OWN row changed after a
// cursor — a rename, a new folder, a subscription or role change.
//
// Kept separate from the count changes above because RFC 8621 §2.2's
// updatedProperties is precisely the distinction between the two (see
// adapter_query.go mailboxesTouchedSince).
//
// Served by the (account_id) index on mailboxes; an account has tens of rows.
//
// Should become: func (s *Store) MailboxRowsChangedSince(ctx, accountID, since, limit) ([]int64, error)
func (a *Adapter) mailboxRowsChangedSince(ctx context.Context, accountID int64, since time.Time, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = maxMailboxesPerChanges
	}
	const q = `
		SELECT id
		  FROM mailboxes
		 WHERE account_id = $1 AND updated_at > $2
		 ORDER BY id
		 LIMIT $3`

	rows, err := a.store.Pool().Query(ctx, q, accountID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

const (
	// defaultChangesLimit is how many changes a /changes call returns when the
	// client names no maxChanges. RFC 8620 §5.2: "If not given by the client,
	// the server may choose how many to return."
	//
	// 256 is a page a client can apply in one frame while being large enough
	// that a normal sync (a few new messages, a few flag updates) completes in
	// one round trip.
	defaultChangesLimit = 256

	// maxChangesCeiling caps what a client may ask for, so one call cannot be
	// made unbounded by naming a huge maxChanges. §5.2 explicitly permits the
	// server to return fewer than requested ("The server MAY choose to return
	// fewer than this value").
	maxChangesCeiling = 4096

	// maxMailboxesPerChanges bounds the folder set one Mailbox/changes reports.
	// An account with more folders than this exists, but not in the reference
	// mailboxes; the bound is here so the query is bounded, per the store rule.
	maxMailboxesPerChanges = 1000
)
