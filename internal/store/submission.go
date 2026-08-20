package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The 'send' half of the intents table (migration 0005): the transactional
// outbox of ADR-001 §4, executed by internal/submit and served to clients as
// RFC 8621 §7 EmailSubmission objects.
//
// Everything here is deliberately separate from the generic intent helpers in
// sync.go: a send intent carries execution-phase columns (accepted_at above
// all) whose semantics are the send rules themselves, and mixing them into the
// generic claim/complete path would let a future flag/move executor touch
// columns whose only legitimate writers are the outbox and the JMAP
// submission surface.

// ErrSubmissionNotCancelable means a cancel arrived after the submission
// stopped being cancelable: the executor already claimed it (or the send
// already happened). RFC 8621 §7.5 names this condition cannotUnsend.
var ErrSubmissionNotCancelable = errors.New("store: the submission is no longer cancelable")

// SendIntent is one 'send' row of intents with its execution phase.
type SendIntent struct {
	ID        int64
	AccountID int64

	// EmailID is the draft being sent (messages.id).
	EmailID int64

	// MessageRFCID is the transmitted message's RFC 5322 Message-ID without
	// angle brackets — the ADR §4 dedupe key.
	MessageRFCID string

	// Payload is the submission's JSON document (envelope, identityId), owned
	// by the JMAP layer; the store treats it as opaque bytes.
	Payload []byte

	State     IntentState
	Attempts  int
	LastError string
	NotBefore time.Time

	// AcceptedAt is when the SMTP 250 to DATA was read; nil means the message
	// has never been accepted by the submission server. THE column the
	// never-retry-after-250 rule branches on.
	AcceptedAt    *time.Time
	AcceptedReply string

	// AppendedAt is when the \Sent copy landed (or was found already present
	// by the Message-ID dedupe).
	AppendedAt *time.Time

	// CanceledAt is the undo timestamp (undoStatus "canceled").
	CanceledAt *time.Time

	// DestroyedAt is the record's tombstone (EmailSubmission/set destroy).
	DestroyedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Accepted reports whether the SMTP server has durably accepted this message.
func (s SendIntent) Accepted() bool { return s.AcceptedAt != nil }

const sendIntentColumns = `id, account_id, email_id, message_rfc_id, payload,
	state, attempts, last_error, not_before, accepted_at, accepted_reply,
	appended_at, canceled_at, destroyed_at, created_at, updated_at`

// EnqueueSendIntent queues one submission for the outbox executor.
//
// notBefore is the undo window's end (W-A3): the executor's claim query only
// takes rows whose not_before has passed, so the window needs no timer — it is
// a property of the row.
func (s *Store) EnqueueSendIntent(ctx context.Context, accountID, emailID int64, messageRFCID string, payload []byte, notBefore time.Time) (SendIntent, error) {
	if notBefore.IsZero() {
		notBefore = time.Now()
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO intents (account_id, kind, payload, not_before, email_id, message_rfc_id)
		VALUES ($1, 'send', $2, $3, $4, $5)
		RETURNING `+sendIntentColumns,
		accountID, jsonOrEmptyObject(payload), notBefore, emailID, messageRFCID)

	in, err := scanSendIntent(row)
	if err != nil {
		return SendIntent{}, fmt.Errorf("enqueuing send intent: %w", err)
	}
	return in, nil
}

// GetSendIntent reads one send intent, scoped to the account.
//
// Tombstoned rows ARE returned: /changes needs to classify them, and the
// caller decides what a destroyed record answers. A row of another account or
// another kind is ErrNotFound — indistinguishable on purpose, the same
// no-oracle rule as every reader.
func (s *Store) GetSendIntent(ctx context.Context, accountID, id int64) (SendIntent, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+sendIntentColumns+` FROM intents
		 WHERE id = $1 AND account_id = $2 AND kind = 'send'`, id, accountID)
	in, err := scanSendIntent(row)
	if err != nil {
		return SendIntent{}, notFound(err, fmt.Sprintf("send intent %d", id))
	}
	return in, nil
}

// SendIntentsByID reads the requested send intents of one account. Unknown or
// foreign ids are simply absent, as in every batch reader.
func (s *Store) SendIntentsByID(ctx context.Context, accountID int64, ids []int64) ([]SendIntent, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+sendIntentColumns+` FROM intents
		 WHERE account_id = $1 AND kind = 'send' AND id = ANY($2)`, accountID, ids)
	if err != nil {
		return nil, fmt.Errorf("reading send intents: %w", err)
	}
	return collectSendIntents(rows)
}

// ListSendIntents returns the account's send intents, newest first, bounded.
// It serves EmailSubmission/get with ids:null.
func (s *Store) ListSendIntents(ctx context.Context, accountID int64, limit int) ([]SendIntent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+sendIntentColumns+` FROM intents
		 WHERE account_id = $1 AND kind = 'send'
		 ORDER BY id DESC
		 LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing send intents: %w", err)
	}
	return collectSendIntents(rows)
}

// CountSendIntents reports how many send intents the account holds, tombstones
// included — the count half of the EmailSubmission state string.
func (s *Store) CountSendIntents(ctx context.Context, accountID int64) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM intents
		 WHERE account_id = $1 AND kind = 'send'`, accountID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting send intents: %w", err)
	}
	return n, nil
}

// SendIntentWatermark returns the newest updated_at across the account's send
// intents, or the zero time when there are none — the watermark half of the
// EmailSubmission state string and the /changes cursor, mirroring the Email
// feed's max(message_state.updated_at).
func (s *Store) SendIntentWatermark(ctx context.Context, accountID int64) (time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT max(updated_at) FROM intents
		 WHERE account_id = $1 AND kind = 'send'`, accountID).Scan(&t)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading send intent watermark: %w", err)
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

// SendIntentsChangedSince returns the account's send intents whose updated_at
// is strictly after the cursor, oldest first — the EmailSubmission/changes
// feed, ordered like the Email one and for the same §5.2 reason.
func (s *Store) SendIntentsChangedSince(ctx context.Context, accountID int64, since time.Time, limit int) ([]SendIntent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+sendIntentColumns+` FROM intents
		 WHERE account_id = $1 AND kind = 'send' AND updated_at > $2
		 ORDER BY updated_at, id
		 LIMIT $3`, accountID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("reading send intent changes: %w", err)
	}
	return collectSendIntents(rows)
}

// ---------------------------------------------------------------------------
// the executor's queue operations
// ---------------------------------------------------------------------------

// DueSendAccounts returns the accounts holding at least one runnable send
// intent — the outbox executor's poll. Bounded by the number of accounts, and
// served by the partial intents_send_due index.
func (s *Store) DueSendAccounts(ctx context.Context) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT account_id FROM intents
		 WHERE kind = 'send' AND state = 'queued' AND not_before <= now()`)
	if err != nil {
		return nil, fmt.Errorf("scanning due send accounts: %w", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning due send accounts: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanning due send accounts: %w", err)
	}
	return out, nil
}

// ClaimDueSendIntents atomically takes up to limit runnable send intents for
// an account and marks them in_flight.
//
// FOR UPDATE SKIP LOCKED is what makes two concurrent executors safe: each
// takes rows nobody else holds. Without FOR UPDATE both would execute the same
// intent — which for 'send' means the mail goes out twice, the one failure
// this epic exists to make impossible. The single-execution property has a
// dedicated race test (internal/submit).
//
// Only state='queued' rows are claimable, which is also what makes the cancel
// race one-winner: CancelSendIntent's compare-and-set requires 'queued', and a
// row is atomically either claimed here or canceled there, never both.
func (s *Store) ClaimDueSendIntents(ctx context.Context, accountID int64, limit int) ([]SendIntent, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE intents
		   SET state = 'in_flight', attempts = attempts + 1, updated_at = now()
		 WHERE id IN (
		     SELECT id FROM intents
		      WHERE account_id = $1 AND kind = 'send'
		        AND state = 'queued' AND not_before <= now()
		      ORDER BY not_before, id
		      LIMIT $2
		      FOR UPDATE SKIP LOCKED
		 )
		RETURNING `+sendIntentColumns, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("claiming send intents: %w", err)
	}
	return collectSendIntents(rows)
}

// RecoverInFlightSendIntents returns every send intent stranded in_flight —
// rows whose executor died mid-run. The outbox calls it once at startup
// (W-A3: "el arranque reconcilia ... antes de tocar nada pendiente").
//
// It bumps attempts and updated_at so the recovery is visible in the row's
// history, and so a row that keeps stranding climbs toward the attempt cap
// instead of looping forever.
//
// The single-daemon assumption is load-bearing and stated: with one moovd, an
// in_flight row at startup can have no living owner. A multi-instance
// deployment would need a lease column before it may call this.
func (s *Store) RecoverInFlightSendIntents(ctx context.Context) ([]SendIntent, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE intents
		   SET attempts = attempts + 1, updated_at = now()
		 WHERE kind = 'send' AND state = 'in_flight'
		RETURNING `+sendIntentColumns)
	if err != nil {
		return nil, fmt.Errorf("recovering in-flight send intents: %w", err)
	}
	return collectSendIntents(rows)
}

// MarkSendIntentAccepted persists the SMTP acceptance. It is the one write
// that MUST happen before any action that follows the 250 (ADR §4), so it is
// a single-statement autocommit UPDATE — the smallest durable thing PostgreSQL
// can do. The residual crash window of the whole design is exactly this
// statement, and it is documented as such in internal/submit.
func (s *Store) MarkSendIntentAccepted(ctx context.Context, id int64, reply string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE intents
		   SET accepted_at = now(), accepted_reply = $2, last_error = NULL, updated_at = now()
		 WHERE id = $1 AND kind = 'send' AND accepted_at IS NULL`, id, reply)
	if err != nil {
		return fmt.Errorf("marking send intent %d accepted: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		// Already accepted (an idempotent replay of the recovery path) is not
		// an error; a missing row is.
		in, gerr := s.getAnySendIntent(ctx, id)
		if gerr != nil {
			return fmt.Errorf("marking send intent %d accepted: %w", id, ErrNotFound)
		}
		if !in.Accepted() {
			return fmt.Errorf("marking send intent %d accepted: row not updated", id)
		}
	}
	return nil
}

// MarkSendIntentAppended records the \Sent copy phase as done, idempotently.
func (s *Store) MarkSendIntentAppended(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE intents
		   SET appended_at = coalesce(appended_at, now()), updated_at = now()
		 WHERE id = $1 AND kind = 'send'`, id)
	if err != nil {
		return fmt.Errorf("stamping appended_at on send intent %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("stamping appended_at on send intent %d: %w", id, ErrNotFound)
	}
	return nil
}

// CancelSendIntent is the undo (W-A3, RFC 8621 §7.5 undoStatus -> "canceled").
//
// The compare-and-set is the whole design: the row moves to 'canceled' only
// from 'queued' with no acceptance recorded, in one statement. If the executor
// claimed it first (state 'in_flight') or the send already happened, the
// update matches nothing and the caller gets ErrSubmissionNotCancelable — the
// §7.5 cannotUnsend condition — never a cancel that pretends to have stopped a
// message that is already on the wire.
func (s *Store) CancelSendIntent(ctx context.Context, accountID, id int64) (SendIntent, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE intents
		   SET state = 'canceled', canceled_at = now(), updated_at = now()
		 WHERE id = $1 AND account_id = $2 AND kind = 'send'
		   AND state = 'queued' AND accepted_at IS NULL
		RETURNING `+sendIntentColumns, id, accountID)

	in, err := scanSendIntent(row)
	if err == nil {
		return in, nil
	}
	if !isNoRows(err) {
		return SendIntent{}, fmt.Errorf("canceling send intent %d: %w", id, err)
	}

	// Nothing matched: distinguish "no such submission" from "too late", but
	// only within the caller's own account — a foreign row stays ErrNotFound.
	existing, gerr := s.GetSendIntent(ctx, accountID, id)
	if gerr != nil {
		return SendIntent{}, gerr
	}
	if existing.State == IntentCanceled {
		// An idempotent replay of a cancel is a success, not an error: the
		// client asked for a state the row is in.
		return existing, nil
	}
	return existing, fmt.Errorf("canceling send intent %d: %w", id, ErrSubmissionNotCancelable)
}

// DestroySendIntent tombstones the EmailSubmission record and, when the
// submission is still cancelable, cancels it in the same statement.
//
// The cancel half is a DOCUMENTED DEVIATION from RFC 8621 §7.5, which scopes
// destroy to record-keeping (its destroy MUST NOT change what is sent). W-A3
// arbitrated the opposite for the undo window — "EmailSubmission/set destroy
// (cancela limpio)" — because a user who removes a pending submission means
// "do not send this", and sending mail the user visibly tried to retract is
// the worse failure. Outside the window the RFC semantics hold exactly: the
// record is tombstoned and the send is not affected.
func (s *Store) DestroySendIntent(ctx context.Context, accountID, id int64) (SendIntent, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE intents
		   SET destroyed_at = coalesce(destroyed_at, now()),
		       canceled_at  = CASE WHEN state = 'queued' AND accepted_at IS NULL
		                           THEN now() ELSE canceled_at END,
		       state        = CASE WHEN state = 'queued' AND accepted_at IS NULL
		                           THEN 'canceled' ELSE state END,
		       updated_at   = now()
		 WHERE id = $1 AND account_id = $2 AND kind = 'send'
		RETURNING `+sendIntentColumns, id, accountID)

	in, err := scanSendIntent(row)
	if err != nil {
		return SendIntent{}, notFound(err, fmt.Sprintf("send intent %d", id))
	}
	return in, nil
}

// IntentCanceled is the undo state (migration 0005).
const IntentCanceled IntentState = "canceled"

// ---------------------------------------------------------------------------
// the Message-ID dedupe probe
// ---------------------------------------------------------------------------

// MailboxContainsMessageID reports whether a mailbox holds a live message with
// the given RFC 5322 Message-ID — the ADR §4 dedupe question, asked against
// \Sent before ever appending a copy there.
//
// It reads Moov's own store rather than issuing an IMAP SEARCH, which is exact
// for every copy Moov itself put there (the write path reflects synchronously)
// and lags only by the watcher's latency for copies other software delivered.
// internal/submit documents the consequence honestly: the worst outcome of
// that lag is a duplicate \Sent COPY, never a duplicate SEND.
func (s *Store) MailboxContainsMessageID(ctx context.Context, mailboxID int64, messageID string) (bool, error) {
	if messageID == "" {
		return false, nil
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM messages m
			  JOIN message_state st ON st.message_id = m.id
			 WHERE m.message_id = $2
			   AND st.mailbox_id = $1
			   AND st.deleted_at IS NULL
		)`, mailboxID, messageID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("probing mailbox %d for message-id: %w", mailboxID, err)
	}
	return exists, nil
}

// ---------------------------------------------------------------------------
// scanning
// ---------------------------------------------------------------------------

// getAnySendIntent reads a send intent without an account scope. Unexported on
// purpose: only the executor-side idempotency checks may look across accounts.
func (s *Store) getAnySendIntent(ctx context.Context, id int64) (SendIntent, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+sendIntentColumns+` FROM intents
		 WHERE id = $1 AND kind = 'send'`, id)
	in, err := scanSendIntent(row)
	if err != nil {
		return SendIntent{}, notFound(err, fmt.Sprintf("send intent %d", id))
	}
	return in, nil
}

func scanSendIntent(row scanner) (SendIntent, error) {
	var in SendIntent
	var emailID *int64
	var messageRFCID, lastError, acceptedReply *string
	err := row.Scan(&in.ID, &in.AccountID, &emailID, &messageRFCID, &in.Payload,
		&in.State, &in.Attempts, &lastError, &in.NotBefore,
		&in.AcceptedAt, &acceptedReply, &in.AppendedAt,
		&in.CanceledAt, &in.DestroyedAt, &in.CreatedAt, &in.UpdatedAt)
	if emailID != nil {
		in.EmailID = *emailID
	}
	if messageRFCID != nil {
		in.MessageRFCID = *messageRFCID
	}
	if lastError != nil {
		in.LastError = *lastError
	}
	if acceptedReply != nil {
		in.AcceptedReply = *acceptedReply
	}
	return in, err
}

func collectSendIntents(rows pgx.Rows) ([]SendIntent, error) {
	defer rows.Close()
	var out []SendIntent
	for rows.Next() {
		in, err := scanSendIntent(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning send intents: %w", err)
		}
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanning send intents: %w", err)
	}
	return out, nil
}
