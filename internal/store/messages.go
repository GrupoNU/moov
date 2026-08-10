package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const messageColumns = `id, account_id, raw_sha256, raw_size,
	message_id, in_reply_to, references_ids,
	subject, from_addr, to_addrs, cc_addrs, addresses,
	date, internal_date,
	mime_structure, has_attachments, preview, body_text,
	parse_status, parser, parser_version, defects, created_at`

const messageStateColumns = `message_id, account_id, mailbox_id, uid, uidvalidity,
	flags, keywords, modseq_seen, deleted_at, updated_at`

// InsertMessages writes a batch of messages and their initial state in one
// transaction, returning the assigned ids in the order given.
//
// Batching is not a micro-optimization here. S3 §4.4 measured 0.25 ms per
// message against a live GIN index, and initial sync is CPU-bound on
// to_tsvector at ~2,063 rows/s — so the sync engine feeds this method in
// batches and the per-statement round trip must not dominate. pgx.Batch sends
// the whole set in one network exchange.
//
// Each message must already have a blob reference: raw_sha256 is a foreign key
// to blobs, so the caller stores the bytes (blob.Put) before inserting the row.
// That ordering is what makes the raw message durable before any parsing is
// attempted (L2 §2.4).
func (s *Store) InsertMessages(ctx context.Context, msgs []NewMessage) ([]int64, error) {
	if len(msgs) == 0 {
		return nil, nil
	}

	ids := make([]int64, len(msgs))
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for i := range msgs {
			m := &msgs[i].Message
			batch.Queue(`
				INSERT INTO messages (account_id, raw_sha256, raw_size,
					message_id, in_reply_to, references_ids,
					subject, from_addr, to_addrs, cc_addrs, addresses,
					date, internal_date,
					mime_structure, has_attachments, preview, body_text,
					parse_status, parser, parser_version, defects)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
				RETURNING id`,
				m.AccountID, m.RawSHA256, m.RawSize,
				nullIfEmpty(m.MessageID), nullIfEmpty(m.InReplyTo), textArray(m.ReferencesIDs),
				m.Subject, m.FromAddr, m.ToAddrs, m.CcAddrs, jsonOrEmptyObject(m.Addresses),
				m.Date, m.InternalDate,
				jsonOrEmptyObject(m.MIMEStructure), m.HasAttachments, m.Preview, m.BodyText,
				defaultParseStatus(m.ParseStatus), m.Parser, m.ParserVersion,
				jsonOrEmptyArray(m.Defects))
		}

		results := tx.SendBatch(ctx, batch)
		for i := range msgs {
			if err := results.QueryRow().Scan(&ids[i]); err != nil {
				_ = results.Close()
				return fmt.Errorf("inserting message %d of %d: %w", i+1, len(msgs), err)
			}
		}
		if err := results.Close(); err != nil {
			return fmt.Errorf("inserting messages: %w", err)
		}

		stateBatch := &pgx.Batch{}
		for i := range msgs {
			st := &msgs[i].State
			stateBatch.Queue(`
				INSERT INTO message_state (message_id, account_id, mailbox_id,
					uid, uidvalidity, flags, keywords, modseq_seen)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
				ids[i], st.AccountID, st.MailboxID,
				st.UID, st.UIDValidity, st.Flags.toDB(),
				textArray(st.Keywords), st.ModSeqSeen)
		}
		stateResults := tx.SendBatch(ctx, stateBatch)
		for i := range msgs {
			if _, err := stateResults.Exec(); err != nil {
				_ = stateResults.Close()
				return fmt.Errorf("inserting message state %d of %d: %w", i+1, len(msgs), err)
			}
		}
		if err := stateResults.Close(); err != nil {
			return fmt.Errorf("inserting message state: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// UpdateFlags applies a batch of flag and keyword changes.
//
// THIS IS THE A5 METHOD. It writes to message_state and nothing else, so the
// ~2.2 KB generated tsv is never rewritten and the GIN indexes are never
// touched. S3 §4.5 measured the alternative — flags living on the messages row
// — at ~0.58 ms per message in batches, because changing one int rewrote the
// whole row into two GIN indexes. Flag churn dominates the write load of an
// established mailbox, which is what made that cost worth designing away.
//
// Any statement here that joins to `messages`, or that updates it, defeats the
// arbitration. There is a test asserting the tsv is untouched across a flag
// update; keep it passing.
func (s *Store) UpdateFlags(ctx context.Context, updates []FlagUpdate) error {
	if len(updates) == 0 {
		return nil
	}

	return s.InTx(ctx, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, u := range updates {
			batch.Queue(`
				UPDATE message_state
				   SET flags = $2, keywords = $3, modseq_seen = $4, updated_at = now()
				 WHERE message_id = $1`,
				u.MessageID, u.Flags.toDB(), textArray(u.Keywords), u.ModSeqSeen)
		}
		results := tx.SendBatch(ctx, batch)
		defer func() { _ = results.Close() }()

		for i := range updates {
			if _, err := results.Exec(); err != nil {
				return fmt.Errorf("updating flags %d of %d: %w", i+1, len(updates), err)
			}
		}
		return nil
	})
}

// MoveMessages reassigns messages to a different mailbox and UID.
//
// A move is an UPDATE of message_state — the content is never touched (L2
// §2.3). uids must be parallel to messageIDs.
func (s *Store) MoveMessages(ctx context.Context, messageIDs []int64, toMailboxID, uidvalidity int64, uids []int64) error {
	if len(messageIDs) == 0 {
		return nil
	}
	if len(messageIDs) != len(uids) {
		return fmt.Errorf("moving messages: %d ids but %d uids", len(messageIDs), len(uids))
	}

	return s.InTx(ctx, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for i, id := range messageIDs {
			batch.Queue(`
				UPDATE message_state
				   SET mailbox_id = $2, uid = $3, uidvalidity = $4, updated_at = now()
				 WHERE message_id = $1`, id, toMailboxID, uids[i], uidvalidity)
		}
		results := tx.SendBatch(ctx, batch)
		defer func() { _ = results.Close() }()

		for i := range messageIDs {
			if _, err := results.Exec(); err != nil {
				return fmt.Errorf("moving message %d of %d: %w", i+1, len(messageIDs), err)
			}
		}
		return nil
	})
}

// MarkDeleted tombstones messages that were expunged on the server.
//
// The rows are marked rather than deleted because JMAP Email/changes must keep
// reporting them as destroyed until every client has caught up. Reaping the
// tombstones after all clients have moved past them is a later concern.
func (s *Store) MarkDeleted(ctx context.Context, mailboxID, uidvalidity int64, uids []int64) error {
	if len(uids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE message_state
		   SET deleted_at = now(), updated_at = now()
		 WHERE mailbox_id = $1 AND uidvalidity = $2 AND uid = ANY($3) AND deleted_at IS NULL`,
		mailboxID, uidvalidity, uids)
	if err != nil {
		return fmt.Errorf("marking %d messages deleted: %w", len(uids), err)
	}
	return nil
}

// GetMessage returns the immutable half of a message.
func (s *Store) GetMessage(ctx context.Context, id int64) (Message, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages WHERE id = $1`, id)
	m, err := scanMessage(row)
	if err != nil {
		return Message{}, notFound(err, fmt.Sprintf("message %d", id))
	}
	return m, nil
}

// GetMessageState returns the volatile half.
func (s *Store) GetMessageState(ctx context.Context, messageID int64) (MessageState, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+messageStateColumns+` FROM message_state WHERE message_id = $1`, messageID)
	st, err := scanMessageState(row)
	if err != nil {
		return MessageState{}, notFound(err, fmt.Sprintf("message state %d", messageID))
	}
	return st, nil
}

// GetMessageStateByUID resolves an IMAP identity to stored state — the sync
// engine's "do I already have this UID" question, served by the unique index
// on (mailbox_id, uidvalidity, uid).
func (s *Store) GetMessageStateByUID(ctx context.Context, mailboxID, uidvalidity, uid int64) (MessageState, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+messageStateColumns+`
		FROM message_state WHERE mailbox_id = $1 AND uidvalidity = $2 AND uid = $3`,
		mailboxID, uidvalidity, uid)
	st, err := scanMessageState(row)
	if err != nil {
		return MessageState{}, notFound(err, fmt.Sprintf("message uid %d", uid))
	}
	return st, nil
}

// ExistingUIDs returns which of the given UIDs are already stored for a
// mailbox.
//
// This is how the sync engine turns a server-side UID range into the set it
// actually needs to fetch, in one round trip rather than one query per UID.
func (s *Store) ExistingUIDs(ctx context.Context, mailboxID, uidvalidity int64, uids []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(uids))
	if len(uids) == 0 {
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT uid FROM message_state
		 WHERE mailbox_id = $1 AND uidvalidity = $2 AND uid = ANY($3)`,
		mailboxID, uidvalidity, uids)
	if err != nil {
		return nil, fmt.Errorf("checking existing uids: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("checking existing uids: %w", err)
		}
		out[uid] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("checking existing uids: %w", err)
	}
	return out, nil
}

// FindMessageByHash looks for content already stored under this account,
// addressed by the sha256 of its raw bytes.
//
// This is what lets a resync after a UIDVALIDITY change, or a message moved
// between folders, avoid re-downloading bytes already held (S2 H8).
func (s *Store) FindMessageByHash(ctx context.Context, accountID int64, sha256 []byte) (Message, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+messageColumns+`
		FROM messages WHERE account_id = $1 AND raw_sha256 = $2 LIMIT 1`, accountID, sha256)
	m, err := scanMessage(row)
	if err != nil {
		return Message{}, notFound(err, "message by hash")
	}
	return m, nil
}

// ChangedSince feeds JMAP Email/changes: the message state rows an account has
// touched since a cursor, oldest first, bounded by limit.
//
// Served by the (account_id, updated_at) index. The cursor is a timestamp
// rather than a counter because updated_at is what the index orders by; the
// JMAP layer wraps it in whatever opaque state string it hands clients.
func (s *Store) ChangedSince(ctx context.Context, accountID int64, since time.Time, limit int) ([]MessageState, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT `+messageStateColumns+`
		FROM message_state
		 WHERE account_id = $1 AND updated_at > $2
		 ORDER BY updated_at, message_id
		 LIMIT $3`, accountID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("reading changes: %w", err)
	}
	defer rows.Close()

	var out []MessageState
	for rows.Next() {
		st, err := scanMessageState(rows)
		if err != nil {
			return nil, fmt.Errorf("reading changes: %w", err)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading changes: %w", err)
	}
	return out, nil
}

// CountMailboxMessages returns the total and unread counts of a mailbox,
// excluding tombstones. This is the folder badge.
func (s *Store) CountMailboxMessages(ctx context.Context, mailboxID int64) (total, unread int64, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE (flags & 1) = 0)
		  FROM message_state
		 WHERE mailbox_id = $1 AND deleted_at IS NULL`, mailboxID).Scan(&total, &unread)
	if err != nil {
		return 0, 0, fmt.Errorf("counting mailbox %d: %w", mailboxID, err)
	}
	return total, unread, nil
}

// ---------------------------------------------------------------------------
// scanning and small helpers
// ---------------------------------------------------------------------------

func scanMessage(row scanner) (Message, error) {
	var m Message
	var messageID, inReplyTo *string
	err := row.Scan(&m.ID, &m.AccountID, &m.RawSHA256, &m.RawSize,
		&messageID, &inReplyTo, &m.ReferencesIDs,
		&m.Subject, &m.FromAddr, &m.ToAddrs, &m.CcAddrs, &m.Addresses,
		&m.Date, &m.InternalDate,
		&m.MIMEStructure, &m.HasAttachments, &m.Preview, &m.BodyText,
		&m.ParseStatus, &m.Parser, &m.ParserVersion, &m.Defects, &m.CreatedAt)
	if messageID != nil {
		m.MessageID = *messageID
	}
	if inReplyTo != nil {
		m.InReplyTo = *inReplyTo
	}
	return m, err
}

func scanMessageState(row scanner) (MessageState, error) {
	var st MessageState
	var flags int64
	err := row.Scan(&st.MessageID, &st.AccountID, &st.MailboxID, &st.UID, &st.UIDValidity,
		&flags, &st.Keywords, &st.ModSeqSeen, &st.DeletedAt, &st.UpdatedAt)
	st.Flags = flagsFromDB(flags)
	return st, err
}

// nullIfEmpty maps "" to NULL, so a missing Message-ID does not become an
// empty string that the partial index on message_id would happily store.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func jsonOrEmptyObject(b []byte) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	return b
}

func jsonOrEmptyArray(b []byte) []byte {
	if len(b) == 0 {
		return []byte("[]")
	}
	return b
}

// textArray normalizes a nil slice to an empty one.
//
// A nil []string encodes as SQL NULL, not '{}', and every text[] column in
// this schema is NOT NULL — so passing a nil slice through fails the insert
// rather than storing an empty array, which is what the caller meant.
func textArray(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func defaultParseStatus(p ParseStatus) ParseStatus {
	if p == "" {
		return ParseOK
	}
	return p
}
