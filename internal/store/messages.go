package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const messageColumns = `id, account_id, raw_sha256, raw_size,
	message_id, in_reply_to, references_ids,
	subject, from_addr, to_addrs, cc_addrs, addresses,
	date, internal_date,
	mime_structure, has_attachments, preview, body_text,
	parse_status, parser, parser_version, defects, created_at, thread_id`

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
//
// # Idempotency at the database, not only in the caller
//
// The message_state insert carries ON CONFLICT (mailbox_id, uidvalidity, uid)
// DO NOTHING, so a UID that is already stored is skipped instead of aborting
// the batch on the unique index. The sync engine still pre-filters known UIDs —
// that is what keeps a resumed run from re-downloading bodies — but a
// pre-filter is a check-then-act and therefore racy by construction: two passes
// over the same mailbox (a watcher event and a reconciler sweep arriving
// together, which E6 makes routine) can both read "not stored" before either
// writes. Before this clause that race aborted a whole batch of a hundred
// messages; now it costs one skipped row.
//
// The returned slice therefore holds the id of every message row that was
// inserted, positionally aligned with msgs. A skipped entry is reported as id 0
// so the alignment survives — a caller adding blob references must skip those
// rather than referencing a message it did not create.
func (s *Store) InsertMessages(ctx context.Context, msgs []NewMessage) ([]int64, error) {
	if len(msgs) == 0 {
		return nil, nil
	}

	ids := make([]int64, len(msgs))
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for i := range msgs {
			m := &msgs[i].Message
			// thread_id is set to the row's OWN id by a BEFORE INSERT trigger
			// (migration 0004, moov_thread_id_default).
			//
			// The column is NOT NULL and its correct initial value is the
			// identity the INSERT is generating, which no DEFAULT expression can
			// reference — a DEFAULT is evaluated before the identity exists. The
			// obvious alternatives were both worse:
			//
			//   * a CTE (INSERT … RETURNING, then UPDATE FROM it) does NOT work:
			//     every statement of a WITH clause sees the same snapshot, so
			//     the UPDATE cannot see the row the INSERT just wrote and
			//     matches nothing. This was tried and it silently returned zero
			//     rows for every insert.
			//   * a second UPDATE statement per batch costs an extra round trip
			//     on the hot sync path and widens the window in which a NOT NULL
			//     column would have to be nullable.
			//
			// The trigger is invisible to every caller and cannot be forgotten
			// by one, which is the property that matters: bulk.go's COPY path
			// and any future writer get the same guarantee without repeating it.
			//
			// The result is the JWZ base case made durable — every message is
			// its own thread until AssignThreads finds it a relative — which is
			// what makes the assignment step a grouping pass rather than a
			// correctness requirement. A crash between the two leaves valid data.
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

		// RETURNING message_id tells the two cases apart: a row comes back for
		// an insert that happened and none for one the conflict clause skipped.
		// pgx reports the skipped case as ErrNoRows on the query result, which
		// is why these are QueryRow rather than Exec.
		stateBatch := &pgx.Batch{}
		for i := range msgs {
			st := &msgs[i].State
			stateBatch.Queue(`
				INSERT INTO message_state (message_id, account_id, mailbox_id,
					uid, uidvalidity, flags, keywords, modseq_seen)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
				ON CONFLICT (mailbox_id, uidvalidity, uid) DO NOTHING
				RETURNING message_id`,
				ids[i], st.AccountID, st.MailboxID,
				st.UID, st.UIDValidity, st.Flags.toDB(),
				textArray(st.Keywords), st.ModSeqSeen)
		}
		stateResults := tx.SendBatch(ctx, stateBatch)
		orphans := make([]int64, 0, len(msgs))
		for i := range msgs {
			var stored int64
			if err := stateResults.QueryRow().Scan(&stored); err != nil {
				if !isNoRows(err) {
					_ = stateResults.Close()
					return fmt.Errorf("inserting message state %d of %d: %w", i+1, len(msgs), err)
				}
				// The UID was already present. The messages row inserted above
				// now has no state and would be an orphan: a message nothing
				// points at, holding a blob reference forever. It is removed
				// inside the same transaction, and the caller is told through a
				// zero id.
				orphans = append(orphans, ids[i])
				ids[i] = 0
			}
		}
		if err := stateResults.Close(); err != nil {
			return fmt.Errorf("inserting message state: %w", err)
		}

		if len(orphans) > 0 {
			if _, err := tx.Exec(ctx,
				`DELETE FROM messages WHERE id = ANY($1)`, orphans); err != nil {
				return fmt.Errorf("removing %d messages whose uid was already stored: %w",
					len(orphans), err)
			}
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

// MessageStatesByUID resolves a set of IMAP UIDs to their stored state in one
// round trip, keyed by UID.
//
// It is the incremental path's lookup (E6): a CHANGEDSINCE pass reports UIDs
// and flags, and turning those into UPDATEs needs the message_id behind each
// UID. ExistingUIDs answers only "is it there"; this answers "what does Moov
// currently believe about it", which is what makes a no-op change detectable
// and therefore skippable — a flag update that changes nothing must not move
// updated_at, because that column is the cursor JMAP Email/changes pages
// through and every spurious bump makes clients re-fetch.
//
// Tombstoned rows are included. A message the server expunged and Moov has
// marked deleted still occupies its UID until the tombstone is reaped, and
// hiding it here would make the caller treat the UID as new and re-fetch a
// message that no longer exists.
func (s *Store) MessageStatesByUID(ctx context.Context, mailboxID, uidvalidity int64, uids []int64) (map[int64]MessageState, error) {
	out := make(map[int64]MessageState, len(uids))
	if len(uids) == 0 {
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `SELECT `+messageStateColumns+`
		FROM message_state
		 WHERE mailbox_id = $1 AND uidvalidity = $2 AND uid = ANY($3)`,
		mailboxID, uidvalidity, uids)
	if err != nil {
		return nil, fmt.Errorf("reading message states by uid: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		st, err := scanMessageState(rows)
		if err != nil {
			return nil, fmt.Errorf("reading message states by uid: %w", err)
		}
		out[st.UID] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading message states by uid: %w", err)
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
	if err := scanMessageInto(row, &m); err != nil {
		return Message{}, err
	}
	return m, nil
}

// scanMessageInto scans the messageColumns list into m.
//
// It is factored out of scanMessage so the batch read in threads.go, which
// scans a message and its state from ONE row, uses the identical column order.
// Two scan functions listing the same columns by hand is how a column added to
// messageColumns ends up silently missing from one of them.
func scanMessageInto(row scanner, m *Message) error {
	var messageID, inReplyTo *string
	err := row.Scan(&m.ID, &m.AccountID, &m.RawSHA256, &m.RawSize,
		&messageID, &inReplyTo, &m.ReferencesIDs,
		&m.Subject, &m.FromAddr, &m.ToAddrs, &m.CcAddrs, &m.Addresses,
		&m.Date, &m.InternalDate,
		&m.MIMEStructure, &m.HasAttachments, &m.Preview, &m.BodyText,
		&m.ParseStatus, &m.Parser, &m.ParserVersion, &m.Defects, &m.CreatedAt,
		&m.ThreadID)
	if messageID != nil {
		m.MessageID = *messageID
	}
	if inReplyTo != nil {
		m.InReplyTo = *inReplyTo
	}
	return err
}

// scanMessageAndState scans a joined row carrying both halves.
//
// The scan is done through a shim rather than by calling scanMessageInto and
// scanMessageState in sequence, because pgx's Scan consumes the whole row in
// one call: the two column sets have to be presented to a single Scan.
func scanMessageAndState(row scanner, m *Message, st *MessageState) error {
	var (
		messageID, inReplyTo *string
		flags                int64
	)
	err := row.Scan(&m.ID, &m.AccountID, &m.RawSHA256, &m.RawSize,
		&messageID, &inReplyTo, &m.ReferencesIDs,
		&m.Subject, &m.FromAddr, &m.ToAddrs, &m.CcAddrs, &m.Addresses,
		&m.Date, &m.InternalDate,
		&m.MIMEStructure, &m.HasAttachments, &m.Preview, &m.BodyText,
		&m.ParseStatus, &m.Parser, &m.ParserVersion, &m.Defects, &m.CreatedAt,
		&m.ThreadID,
		&st.MessageID, &st.AccountID, &st.MailboxID, &st.UID, &st.UIDValidity,
		&flags, &st.Keywords, &st.ModSeqSeen, &st.DeletedAt, &st.UpdatedAt)
	if messageID != nil {
		m.MessageID = *messageID
	}
	if inReplyTo != nil {
		m.InReplyTo = *inReplyTo
	}
	st.Flags = flagsFromDB(flags)
	return err
}

// splitColumns splits a column list constant into its individual column names,
// dropping the whitespace and newlines the constants are formatted with.
func splitColumns(columns string) []string {
	raw := strings.Split(columns, ",")
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// joinColumns is splitColumns' inverse.
func joinColumns(parts []string) string { return strings.Join(parts, ", ") }

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
