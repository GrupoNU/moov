package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The `threads` table (migration 0009): thread rows with durable identity,
// maintained on insert and on merge, with tombstones.
//
// # The relationship to threads.go
//
// threads.go owns the ALGORITHM (which conversation a message belongs to) and
// writes `messages.thread_id`. This file owns the ROW that gives that
// conversation a name a rebuild cannot take away. The two are wired together
// inside AssignThreads' transaction, so a thread row and the thread_id it
// describes are always written together or not at all — an unpaired thread row
// would be worse than none, because a mute would attach to a conversation that
// does not exist.
//
// # Why the row is upserted rather than inserted
//
// Two sync workers can thread messages of the same conversation at the same
// time (the watcher and the reconciler overlap routinely — E6). Both derive
// the same durable key, so both try to create the same row. `ON CONFLICT
// (account_id, root_message_id)` makes the second one a no-op update rather
// than a unique-violation that would abort a whole batch's transaction. This
// is the same idempotency the message insert path has, for the same reason.

// Thread is one row of the threads table.
type Thread struct {
	ID        int64
	AccountID int64

	// RootMessageID is the durable key (threadkey.go).
	RootMessageID string

	// ThreadID is the current messages.thread_id of this conversation.
	ThreadID int64

	// MergedInto names the surviving thread when this one was absorbed; zero
	// when the thread is live.
	MergedInto int64

	// DestroyedAt is the merge tombstone; nil when the thread is live.
	DestroyedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Destroyed reports whether this thread was absorbed by a merge.
func (t Thread) Destroyed() bool { return t.DestroyedAt != nil }

const threadRowColumns = `id, account_id, root_message_id, thread_id,
	merged_into, destroyed_at, created_at, updated_at`

// EnsureThread creates or refreshes the thread row for one conversation and
// returns it.
//
// It is called from inside AssignThreads' transaction (hence the tx
// parameter), which is what keeps the row and messages.thread_id consistent.
//
// The update arm handles the case that makes this more than an insert: a merge
// moved the conversation onto a SMALLER thread_id, so the row's cached
// thread_id is stale and must follow. `updated_at` moves with it, which is
// what makes Thread/changes report the conversation as updated.
//
// The `WHERE` on the update arm is the no-op guard: re-threading a message
// that changed nothing (the overwhelmingly common case — every reply after the
// first) must not bump the watermark, or every client would refetch every
// thread on every incoming message.
func EnsureThread(ctx context.Context, tx pgx.Tx, accountID int64, rootKey string, threadID int64) (Thread, error) {
	const q = `
		INSERT INTO threads (account_id, root_message_id, thread_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (account_id, root_message_id) DO UPDATE
		   SET thread_id  = EXCLUDED.thread_id,
		       updated_at = now()
		 WHERE threads.thread_id <> EXCLUDED.thread_id
		RETURNING ` + threadRowColumns

	row := tx.QueryRow(ctx, q, accountID, rootKey, threadID)
	t, err := scanThreadRow(row)
	if err == nil {
		return t, nil
	}
	if !isNoRows(err) {
		return Thread{}, fmt.Errorf("ensuring thread %q: %w", rootKey, err)
	}
	// The conflict arm's WHERE filtered the update out: the row exists and
	// already says what we wanted it to say. RETURNING yields nothing for a
	// suppressed DO UPDATE, so the row is read back. This is the common path
	// and it is one indexed lookup.
	return threadByKey(ctx, tx, accountID, rootKey)
}

func threadByKey(ctx context.Context, tx pgx.Tx, accountID int64, rootKey string) (Thread, error) {
	row := tx.QueryRow(ctx,
		`SELECT `+threadRowColumns+` FROM threads
		  WHERE account_id = $1 AND root_message_id = $2`, accountID, rootKey)
	t, err := scanThreadRow(row)
	if err != nil {
		return Thread{}, notFound(err, fmt.Sprintf("thread %q", rootKey))
	}
	return t, nil
}

// TombstoneThread records a merge: the losing thread keeps its row, gains a
// tombstone and points at the winner.
//
// This is the record migration 0009 exists to create and the one
// Thread/changes could not previously produce (changes.go: "a thread that lost
// its id to a merge is destroyed with no message tombstoned at all. Nothing in
// the schema records that event").
//
// It is idempotent: a thread already tombstoned onto the same winner is left
// alone (including its updated_at, so a replayed merge does not re-notify).
// A thread tombstoned onto a DIFFERENT winner is re-pointed — that happens
// when A merges into B and B later merges into C, and the honest answer for a
// client holding A is C rather than a dead B.
func TombstoneThread(ctx context.Context, tx pgx.Tx, accountID, loserID, winnerID int64) error {
	if loserID == winnerID {
		return fmt.Errorf("threads: refusing to merge thread row %d into itself", loserID)
	}
	const q = `
		UPDATE threads
		   SET merged_into  = $3,
		       destroyed_at = coalesce(destroyed_at, now()),
		       updated_at   = now()
		 WHERE id = $1 AND account_id = $2
		   AND (merged_into IS DISTINCT FROM $3)`
	if _, err := tx.Exec(ctx, q, loserID, accountID, winnerID); err != nil {
		return fmt.Errorf("tombstoning thread row %d: %w", loserID, err)
	}
	return nil
}

// ThreadRowsByThreadIDs maps live messages.thread_id values to their rows.
//
// The engine asks this on every inbound message ("is this conversation
// muted?"), so it is one round trip for a batch, served by the partial unique
// index threads_account_thread_live.
func (s *Store) ThreadRowsByThreadIDs(ctx context.Context, accountID int64, threadIDs []int64) (map[int64]Thread, error) {
	out := make(map[int64]Thread, len(threadIDs))
	if len(threadIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+threadRowColumns+` FROM threads
		 WHERE account_id = $1 AND thread_id = ANY($2) AND destroyed_at IS NULL`,
		accountID, threadIDs)
	if err != nil {
		return nil, fmt.Errorf("reading thread rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scanThreadRow(rows)
		if err != nil {
			return nil, fmt.Errorf("reading thread rows: %w", err)
		}
		out[t.ThreadID] = t
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading thread rows: %w", err)
	}
	return out, nil
}

// ThreadRowByThreadID reads the live row for one messages.thread_id.
func (s *Store) ThreadRowByThreadID(ctx context.Context, accountID, threadID int64) (Thread, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+threadRowColumns+` FROM threads
		 WHERE account_id = $1 AND thread_id = $2 AND destroyed_at IS NULL`,
		accountID, threadID)
	t, err := scanThreadRow(row)
	if err != nil {
		return Thread{}, notFound(err, fmt.Sprintf("thread row for thread %d", threadID))
	}
	return t, nil
}

// ThreadRowsChangedSince feeds Thread/changes: rows of one account whose
// watermark is strictly after the cursor, oldest first.
//
// Strictly after, like every other /changes feed here: the cursor a client
// holds IS the watermark of what it has seen.
func (s *Store) ThreadRowsChangedSince(ctx context.Context, accountID int64, since time.Time, limit int) ([]Thread, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+threadRowColumns+` FROM threads
		 WHERE account_id = $1 AND updated_at > $2
		 ORDER BY updated_at, id
		 LIMIT $3`, accountID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("reading thread changes: %w", err)
	}
	return collectThreadRows(rows)
}

// ThreadRowWatermark is the newest updated_at across an account's thread rows —
// the watermark half of the Thread state string.
func (s *Store) ThreadRowWatermark(ctx context.Context, accountID int64) (time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT max(updated_at) FROM threads WHERE account_id = $1`, accountID).Scan(&t)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading thread watermark: %w", err)
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

// CountThreadRows counts an account's thread rows, tombstones included — the
// count half of the state string, and what makes the state move when a reap
// removes rows without changing the watermark.
func (s *Store) CountThreadRows(ctx context.Context, accountID int64) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM threads WHERE account_id = $1`, accountID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting thread rows: %w", err)
	}
	return n, nil
}

// BackfillThreadRows creates the missing thread rows for one account, in
// bounded batches, and returns how many were created.
//
// It exists for the same three reasons ReindexThreads does, and for one more:
// migration 0009's SQL backfill deliberately skips threads whose oldest member
// has neither a Message-ID nor a References chain, because the subject digest
// is Go code (threadkey.go) and a SQL approximation would seed keys the runtime
// never produces — the identical argument 0004 made about thread_subject_keys.
// This closes that gap online.
//
// Idempotent by the upsert, so it is safe to run repeatedly and safe to run
// while the sync engine inserts.
func (s *Store) BackfillThreadRows(ctx context.Context, accountID int64, batchSize int, afterThreadID int64) (created int, lastThreadID int64, err error) {
	if batchSize <= 0 {
		batchSize = 500
	}
	err = s.InTx(ctx, func(tx pgx.Tx) error {
		// The oldest member of each thread, in thread_id order so the caller can
		// page. DISTINCT ON is served by messages_acct_thread (account, thread,
		// date) — the same index ThreadMembers uses.
		rows, qerr := tx.Query(ctx, `
			SELECT DISTINCT ON (m.thread_id)
			       m.thread_id, m.message_id, m.in_reply_to, m.references_ids, m.subject
			  FROM messages m
			 WHERE m.account_id = $1 AND m.thread_id IS NOT NULL AND m.thread_id > $2
			 ORDER BY m.thread_id, m.date, m.id
			 LIMIT $3`, accountID, afterThreadID, batchSize)
		if qerr != nil {
			return fmt.Errorf("backfilling thread rows: %w", qerr)
		}
		type work struct {
			threadID  int64
			candidate ThreadCandidate
		}
		var batch []work
		for rows.Next() {
			var (
				threadID  int64
				messageID *string
				inReplyTo *string
				refs      []string
				subject   string
			)
			if err := rows.Scan(&threadID, &messageID, &inReplyTo, &refs, &subject); err != nil {
				rows.Close()
				return fmt.Errorf("backfilling thread rows: %w", err)
			}
			c := ThreadCandidate{References: refs, Subject: subject}
			if messageID != nil {
				c.MessageID = *messageID
			}
			if inReplyTo != nil && *inReplyTo != "" {
				c.References = append(append([]string{}, refs...), *inReplyTo)
			}
			batch = append(batch, work{threadID: threadID, candidate: c})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("backfilling thread rows: %w", err)
		}
		for _, w := range batch {
			if _, err := EnsureThread(ctx, tx, accountID, ThreadKey(w.candidate), w.threadID); err != nil {
				return err
			}
			created++
			lastThreadID = w.threadID
		}
		return nil
	})
	if err != nil {
		return 0, afterThreadID, err
	}
	if lastThreadID == 0 {
		lastThreadID = afterThreadID
	}
	return created, lastThreadID, nil
}

// ---------------------------------------------------------------------------
// scanning
// ---------------------------------------------------------------------------

func scanThreadRow(row scanner) (Thread, error) {
	var t Thread
	var mergedInto *int64
	err := row.Scan(&t.ID, &t.AccountID, &t.RootMessageID, &t.ThreadID,
		&mergedInto, &t.DestroyedAt, &t.CreatedAt, &t.UpdatedAt)
	if mergedInto != nil {
		t.MergedInto = *mergedInto
	}
	return t, err
}

func collectThreadRows(rows pgx.Rows) ([]Thread, error) {
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThreadRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning thread rows: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanning thread rows: %w", err)
	}
	return out, nil
}
