package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Snoozes and mutes (migration 0009, L3 epic E4, arbitration GC-10).
//
// # What lives here and what does NOT
//
// GC-10 is the governing constraint and it is worth restating at the code that
// implements it, because the whole epic is audited against it. Its wording, in
// English: state that lives ONLY in PostgreSQL violates the invariant, because
// other IMAP clients would still see the mail in INBOX and a cache rebuild
// would lose it. (The plan's original Spanish is in
// docs/specs/L3-gmail-class-plan.md §3.)
//
// So neither of these tables holds where mail IS. A snooze is a MOVE to the
// Snoozed folder in Dovecot; a mute's effect is an archive in Dovecot. Both
// are visible to every other IMAP client and both survive a rebuild because
// Dovecot performed them.
//
// What these tables hold is the two facts IMAP has no vocabulary for:
//
//   - snoozes: WHEN a snoozed message should come back, and WHERE from. Losing
//     it degrades to "the message stays in Snoozed until the user moves it" —
//     visible and recoverable, never a lie about where the mail is.
//   - mutes: WHICH conversations are muted. Losing it degrades to "replies
//     start reaching the inbox again" — again visible, and again not a lie.
//
// Both are keyed durably (a Message-ID; a threads.id whose identity is a root
// Message-ID) so a rebuild keeps them anyway.

// SnoozeState is the lifecycle of one snooze row.
type SnoozeState string

// The three snooze states (migration 0009's CHECK constraint).
const (
	SnoozePending SnoozeState = "pending"
	SnoozeWoken   SnoozeState = "woken"
	SnoozeFailed  SnoozeState = "failed"
)

// Snooze is one pending (or completed) wake.
type Snooze struct {
	ID        int64
	AccountID int64

	// MessageRFCID is the snoozed message's RFC 5322 Message-ID without angle
	// brackets — the durable key.
	MessageRFCID string

	WakeAt time.Time

	// OriginMailbox is the IMAP name of the folder the message came from.
	// Empty means INBOX.
	OriginMailbox string

	State     SnoozeState
	Attempts  int
	LastError string

	CreatedAt time.Time
	UpdatedAt time.Time
}

const snoozeColumns = `id, account_id, message_rfc_id, wake_at, origin_mailbox,
	state, attempts, last_error, created_at, updated_at`

// PutSnooze records (or re-records) a pending snooze.
//
// Snoozing an already-snoozed message REPLACES the wake time rather than
// erroring, which is what "snooze it again for longer" means and what the
// partial unique index snoozes_account_message_pending is shaped for. The
// attempt counter and the error are cleared with it: a re-snooze is a fresh
// schedule, not a retry of the old one.
func (s *Store) PutSnooze(ctx context.Context, sn Snooze) (Snooze, error) {
	if sn.MessageRFCID == "" {
		return Snooze{}, fmt.Errorf("snoozing: a message-id is required")
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO snoozes (account_id, message_rfc_id, wake_at, origin_mailbox, state)
		VALUES ($1, $2, $3, $4, 'pending')
		ON CONFLICT (account_id, message_rfc_id) WHERE state = 'pending'
		DO UPDATE SET wake_at        = EXCLUDED.wake_at,
		              origin_mailbox = EXCLUDED.origin_mailbox,
		              attempts       = 0,
		              last_error     = NULL,
		              updated_at     = now()
		RETURNING `+snoozeColumns,
		sn.AccountID, sn.MessageRFCID, sn.WakeAt, sn.OriginMailbox)

	out, err := scanSnooze(row)
	if err != nil {
		return Snooze{}, fmt.Errorf("recording snooze for %q: %w", sn.MessageRFCID, err)
	}
	return out, nil
}

// CancelSnooze removes the pending snooze of a message (the un-snooze), and
// reports whether there was one.
//
// The row is DELETED rather than marked, unlike the wake paths: a canceled
// snooze has no history worth keeping — the message is back where the user put
// it, and the record would only make the partial unique index carry a row that
// can never fire again.
func (s *Store) CancelSnooze(ctx context.Context, accountID int64, messageRFCID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM snoozes
		 WHERE account_id = $1 AND message_rfc_id = $2 AND state = 'pending'`,
		accountID, messageRFCID)
	if err != nil {
		return false, fmt.Errorf("canceling snooze for %q: %w", messageRFCID, err)
	}
	return tag.RowsAffected() > 0, nil
}

// PendingSnoozes lists an account's pending snoozes, soonest first.
func (s *Store) PendingSnoozes(ctx context.Context, accountID int64, limit int) ([]Snooze, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+snoozeColumns+` FROM snoozes
		 WHERE account_id = $1 AND state = 'pending'
		 ORDER BY wake_at, id
		 LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing snoozes: %w", err)
	}
	return collectSnoozes(rows)
}

// SnoozesByMessageIDs reads the pending snoozes of a set of messages, keyed by
// Message-ID — the batch question the JMAP read path asks when rendering a
// list ("which of these are snoozed, and until when?").
func (s *Store) SnoozesByMessageIDs(ctx context.Context, accountID int64, messageRFCIDs []string) (map[string]Snooze, error) {
	out := map[string]Snooze{}
	if len(messageRFCIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+snoozeColumns+` FROM snoozes
		 WHERE account_id = $1 AND state = 'pending' AND message_rfc_id = ANY($2)`,
		accountID, messageRFCIDs)
	if err != nil {
		return nil, fmt.Errorf("reading snoozes: %w", err)
	}
	list, err := collectSnoozes(rows)
	if err != nil {
		return nil, err
	}
	for _, sn := range list {
		out[sn.MessageRFCID] = sn
	}
	return out, nil
}

// ClaimDueSnoozes atomically takes up to limit snoozes whose wake time has
// passed, across ALL accounts, and returns them still marked pending.
//
// # Why the claim does not change state
//
// Unlike the outbox's ClaimDueSendIntents, this does not move the rows to an
// in-flight state, and the difference is deliberate. A send that runs twice
// sends the mail twice — an unrecoverable user-visible failure — so the outbox
// pays for exactly-once with a claim state and a recovery sweep. A wake that
// runs twice performs a MOVE of a message that is no longer in the source
// folder, which the write executor answers with "not found" and which changes
// nothing. The cheap idempotency of the operation is what lets this stay a
// plain SELECT ... FOR UPDATE SKIP LOCKED with no extra state to recover.
//
// SKIP LOCKED still matters: it is what keeps two wakers (a future
// multi-instance deployment, or a test running beside the daemon) from
// serializing on the same rows.
func (s *Store) ClaimDueSnoozes(ctx context.Context, now time.Time, limit int) ([]Snooze, error) {
	if limit <= 0 {
		limit = 50
	}
	if now.IsZero() {
		now = time.Now()
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+snoozeColumns+` FROM snoozes
		 WHERE state = 'pending' AND wake_at <= $1
		 ORDER BY wake_at, id
		 LIMIT $2
		 FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claiming due snoozes: %w", err)
	}
	return collectSnoozes(rows)
}

// MarkSnoozeWoken records a successful wake.
func (s *Store) MarkSnoozeWoken(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE snoozes SET state = 'woken', last_error = NULL, updated_at = now()
		 WHERE id = $1 AND state = 'pending'`, id)
	if err != nil {
		return fmt.Errorf("marking snooze %d woken: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("marking snooze %d woken: %w", id, ErrNotFound)
	}
	return nil
}

// FailSnooze records a wake attempt that failed.
//
// retryAt nil means the failure is PERMANENT: the row moves to 'failed' and
// the waker never looks at it again. A non-nil retryAt keeps the row pending
// and pushes its wake time out, which is the transient case (Dovecot was down,
// the connection dropped).
//
// The distinction is the caller's, and it is what keeps a snooze from becoming
// a silent disappearance: a permanently failed wake leaves the message in the
// Snoozed folder where the user can see it, with a row that says why.
func (s *Store) FailSnooze(ctx context.Context, id int64, message string, retryAt *time.Time) error {
	var err error
	if retryAt != nil {
		_, err = s.pool.Exec(ctx, `
			UPDATE snoozes
			   SET attempts = attempts + 1, last_error = $2, wake_at = $3, updated_at = now()
			 WHERE id = $1 AND state = 'pending'`, id, message, *retryAt)
	} else {
		_, err = s.pool.Exec(ctx, `
			UPDATE snoozes
			   SET state = 'failed', attempts = attempts + 1, last_error = $2, updated_at = now()
			 WHERE id = $1 AND state = 'pending'`, id, message)
	}
	if err != nil {
		return fmt.Errorf("failing snooze %d: %w", id, err)
	}
	return nil
}

// DueSnoozeAccounts returns the accounts holding at least one due snooze — the
// waker's cheap poll, mirroring DueSendAccounts.
func (s *Store) DueSnoozeAccounts(ctx context.Context, now time.Time) ([]int64, error) {
	if now.IsZero() {
		now = time.Now()
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT account_id FROM snoozes
		 WHERE state = 'pending' AND wake_at <= $1`, now)
	if err != nil {
		return nil, fmt.Errorf("scanning due snooze accounts: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning due snooze accounts: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanning due snooze accounts: %w", err)
	}
	return out, nil
}

// MessageIDInMailbox resolves a durable RFC 5322 Message-ID to the store id of
// the live message carrying it in one mailbox.
//
// It is the indirection that makes the snooze table's durable key usable: the
// waker holds a Message-ID (which survives a cache rebuild) and needs a
// messages.id (which does not).
//
// A Message-ID is not unique in a mailbox — a user can have two copies of the
// same mail — so the NEWEST is chosen. That is the right tiebreak for the
// waker's question: if a message was snoozed and a second copy of it arrived
// while it slept, the one the user wants back is the one in Snoozed, and the
// mailbox scope already ensures that; among two copies IN Snoozed, the newer
// is the one the second snooze recorded.
func (s *Store) MessageIDInMailbox(ctx context.Context, mailboxID int64, messageRFCID string) (int64, error) {
	if messageRFCID == "" {
		return 0, ErrNotFound
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT m.id
		  FROM messages m
		  JOIN message_state ms ON ms.message_id = m.id
		 WHERE ms.mailbox_id = $1
		   AND m.message_id = $2
		   AND ms.deleted_at IS NULL
		 ORDER BY m.id DESC
		 LIMIT 1`, mailboxID, messageRFCID).Scan(&id)
	if err != nil {
		return 0, notFound(err, fmt.Sprintf("message %q in mailbox %d", messageRFCID, mailboxID))
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// mutes
// ---------------------------------------------------------------------------

// SetMute mutes or unmutes a conversation, by its durable thread row id.
//
// Idempotent in both directions: muting a muted thread and unmuting an unmuted
// one both succeed and report the resulting state, which is what a /set
// handler needs to answer without a read-modify-write round trip.
func (s *Store) SetMute(ctx context.Context, accountID, threadRowID int64, muted bool) error {
	if muted {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO mutes (thread_id, account_id)
			VALUES ($1, $2)
			ON CONFLICT (thread_id) DO NOTHING`, threadRowID, accountID)
		if err != nil {
			return fmt.Errorf("muting thread %d: %w", threadRowID, err)
		}
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`DELETE FROM mutes WHERE thread_id = $1 AND account_id = $2`, threadRowID, accountID)
	if err != nil {
		return fmt.Errorf("unmuting thread %d: %w", threadRowID, err)
	}
	return nil
}

// MutedThreadRows reports which of the given thread ROW ids are muted.
func (s *Store) MutedThreadRows(ctx context.Context, accountID int64, threadRowIDs []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(threadRowIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT thread_id FROM mutes
		 WHERE account_id = $1 AND thread_id = ANY($2)`, accountID, threadRowIDs)
	if err != nil {
		return nil, fmt.Errorf("reading mutes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("reading mutes: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading mutes: %w", err)
	}
	return out, nil
}

// IsThreadMuted answers the engine's per-message question: is the conversation
// this message just joined muted?
//
// It takes the VOLATILE thread id (messages.thread_id) and resolves it through
// the durable row in one query, because that is the id the sync pipeline has in
// hand and making the engine do the resolution itself would put the durable-key
// design into three call sites instead of one.
func (s *Store) IsThreadMuted(ctx context.Context, accountID, threadID int64) (bool, error) {
	var muted bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM mutes mu
			  JOIN threads t ON t.id = mu.thread_id
			 WHERE mu.account_id = $1
			   AND t.account_id = $1
			   AND t.thread_id = $2
			   AND t.destroyed_at IS NULL
		)`, accountID, threadID).Scan(&muted)
	if err != nil {
		return false, fmt.Errorf("checking mute of thread %d: %w", threadID, err)
	}
	return muted, nil
}

// ListMutedThreads returns the account's muted conversations as their CURRENT
// messages.thread_id values — what the JMAP layer renders as Thread ids.
//
// Tombstoned rows are excluded: a merged thread's id is stale, and the mute
// travels with the merge because the surviving row keeps its own mute (or does
// not). Reporting a dead id would hand a client something Thread/get answers
// notFound for.
func (s *Store) ListMutedThreads(ctx context.Context, accountID int64, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.pool.Query(ctx, `
		SELECT t.thread_id
		  FROM mutes mu
		  JOIN threads t ON t.id = mu.thread_id
		 WHERE mu.account_id = $1 AND t.destroyed_at IS NULL
		 ORDER BY t.thread_id
		 LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing muted threads: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("listing muted threads: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing muted threads: %w", err)
	}
	return out, nil
}

// SnoozeWatermark is the newest updated_at across an account's PENDING
// snoozes and how many there are — the two halves of the Snooze state string.
//
// Scoped to pending rather than to every row, deliberately: a woken snooze is
// no longer an object the JMAP surface serves, so including it would make the
// state move for a change no client can observe — a refresh storm with nothing
// to refresh.
func (s *Store) SnoozeWatermark(ctx context.Context, accountID int64) (time.Time, int64, error) {
	var (
		watermark *time.Time
		count     int64
	)
	err := s.pool.QueryRow(ctx, `
		SELECT max(updated_at), count(*) FROM snoozes
		 WHERE account_id = $1 AND state = 'pending'`, accountID).Scan(&watermark, &count)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("reading the snooze watermark: %w", err)
	}
	if watermark == nil {
		return time.Time{}, count, nil
	}
	return *watermark, count, nil
}

// MuteWatermark is the newest updated_at across an account's mutes and how
// many there are — the two halves of the Mute state string.
//
// One query for both, because the state string always wants both and asking
// separately would double a read that already scans the same index range.
func (s *Store) MuteWatermark(ctx context.Context, accountID int64) (time.Time, int64, error) {
	var (
		watermark *time.Time
		count     int64
	)
	err := s.pool.QueryRow(ctx, `
		SELECT max(updated_at), count(*) FROM mutes WHERE account_id = $1`,
		accountID).Scan(&watermark, &count)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("reading the mute watermark: %w", err)
	}
	if watermark == nil {
		return time.Time{}, count, nil
	}
	return *watermark, count, nil
}

// EnsureThreadRowFor creates the durable thread row for an existing
// conversation that does not have one yet, deriving the key from the
// conversation's OLDEST member — the same rule threadkey.go states and the
// same one migration 0009's backfill applies.
//
// It exists for the one case the backfill cannot cover: a thread whose oldest
// member has neither a Message-ID nor a References chain, which the migration
// deliberately skips because the subject digest is Go code. Rather than
// leaving those conversations permanently unmutable, this creates the row on
// demand with the runtime's own derivation.
//
// ErrNotFound when the thread has no members at all.
func (s *Store) EnsureThreadRowFor(ctx context.Context, accountID, threadID int64) (Thread, error) {
	var out Thread
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		key, err := threadRootKey(ctx, tx, accountID, threadID)
		if err != nil {
			return err
		}
		row, err := ensureThreadForWinner(ctx, tx, accountID, key, threadID)
		if err != nil {
			return err
		}
		out = row
		return nil
	})
	return out, err
}

// ---------------------------------------------------------------------------
// scanning
// ---------------------------------------------------------------------------

func scanSnooze(row scanner) (Snooze, error) {
	var sn Snooze
	var lastError *string
	err := row.Scan(&sn.ID, &sn.AccountID, &sn.MessageRFCID, &sn.WakeAt,
		&sn.OriginMailbox, &sn.State, &sn.Attempts, &lastError,
		&sn.CreatedAt, &sn.UpdatedAt)
	if lastError != nil {
		sn.LastError = *lastError
	}
	return sn, err
}

func collectSnoozes(rows pgx.Rows) ([]Snooze, error) {
	defer rows.Close()
	var out []Snooze
	for rows.Next() {
		sn, err := scanSnooze(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning snoozes: %w", err)
		}
		out = append(out, sn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanning snoozes: %w", err)
	}
	return out, nil
}
