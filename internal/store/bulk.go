package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The bulk-migration write path (L2 §2.5 step 4, the 89-account Crash case).
//
// # Why this exists alongside InsertMessages
//
// InsertMessages is the right shape for a steady-state batch of ~100: it sends
// one round trip, returns the ids it assigned, and is idempotent per UID. What
// it cannot do is amortize the per-row protocol overhead across hundreds of
// thousands of rows, because every row is still an INSERT with its own
// parameter set and its own RETURNING.
//
// COPY is a different protocol: one stream, no per-row parse, no per-row plan.
// S3 H6 measured the bulk path CPU-bound in to_tsvector at ~2,063 rows/s, which
// means the database side has headroom the batched path does not use — and on
// an installation-sized migration that headroom is the difference between hours
// and a working day.
//
// # What this path does NOT do, deliberately
//
// The deferred-index strategy — dropping the GIN indexes, copying, rebuilding
// them at the end — is NOT implemented here and must not be. It is a decision
// about a whole database's availability, not about one batch: while the indexes
// are gone every search in the installation is a sequential scan, so only the
// caller running the migration knows whether that is acceptable and when the
// rebuild may run. This method is the fast write; the index strategy stays the
// caller's concern, as arbitrated.

// CopyMessages bulk-loads messages and their state using PostgreSQL's COPY
// protocol, returning how many rows were inserted.
//
// # The identity problem and how it is solved
//
// messages.id is GENERATED ALWAYS AS IDENTITY, so COPY cannot supply it and
// cannot report it either — COPY has no RETURNING. But message_state needs that
// id as its primary key, which makes a naive "copy both" impossible.
//
// The resolution is a temporary staging table keyed by the caller's own
// correlation index: the messages are copied into staging, moved into
// `messages` with INSERT … SELECT … RETURNING (which does report the ids), and
// the state rows are then built by joining the returned ids back to the
// staging rows. Everything happens in one transaction, so a failure leaves no
// half-loaded mailbox, and the staging table is ON COMMIT DROP so it cannot
// outlive the load.
//
// # Idempotency
//
// The same ON CONFLICT (mailbox_id, uidvalidity, uid) DO NOTHING rule as
// InsertMessages: a UID already stored is skipped rather than aborting the
// load. A migration that is re-run after an interruption therefore resumes
// instead of failing on the first message it already has. Messages whose state
// was skipped are removed again, so no orphan row survives.
//
// Every message must already have its bytes in the blob store: raw_sha256 is a
// foreign key, and the caller adds the blob references (blob.AddRefs) after
// this returns, using the ids reported through NewMessage identity order.
func (s *Store) CopyMessages(ctx context.Context, msgs []NewMessage) (CopyResult, error) {
	var res CopyResult
	if len(msgs) == 0 {
		return res, nil
	}

	err := s.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			CREATE TEMPORARY TABLE moov_copy_stage (
				seq             int         NOT NULL,
				account_id      bigint      NOT NULL,
				raw_sha256      bytea       NOT NULL,
				raw_size        bigint      NOT NULL,
				message_id      text,
				in_reply_to     text,
				references_ids  text[]      NOT NULL,
				subject         text        NOT NULL,
				from_addr       text        NOT NULL,
				to_addrs        text        NOT NULL,
				cc_addrs        text        NOT NULL,
				addresses       jsonb       NOT NULL,
				date            timestamptz NOT NULL,
				internal_date   timestamptz,
				mime_structure  jsonb       NOT NULL,
				has_attachments boolean     NOT NULL,
				preview         text        NOT NULL,
				body_text       text        NOT NULL,
				parse_status    text        NOT NULL,
				parser          text        NOT NULL,
				parser_version  int         NOT NULL,
				defects         jsonb       NOT NULL,
				mailbox_id      bigint      NOT NULL,
				uid             bigint      NOT NULL,
				uidvalidity     bigint      NOT NULL,
				flags           bigint      NOT NULL,
				keywords        text[]      NOT NULL,
				modseq_seen     bigint      NOT NULL
			) ON COMMIT DROP`); err != nil {
			return fmt.Errorf("creating copy staging table: %w", err)
		}

		rows := make([][]any, 0, len(msgs))
		for i := range msgs {
			m := &msgs[i].Message
			st := &msgs[i].State
			rows = append(rows, []any{
				i,
				m.AccountID, m.RawSHA256, m.RawSize,
				nullIfEmpty(m.MessageID), nullIfEmpty(m.InReplyTo), textArray(m.ReferencesIDs),
				m.Subject, m.FromAddr, m.ToAddrs, m.CcAddrs, jsonOrEmptyObject(m.Addresses),
				m.Date, m.InternalDate,
				jsonOrEmptyObject(m.MIMEStructure), m.HasAttachments, m.Preview, m.BodyText,
				string(defaultParseStatus(m.ParseStatus)), m.Parser, m.ParserVersion,
				jsonOrEmptyArray(m.Defects),
				st.MailboxID, st.UID, st.UIDValidity, st.Flags.toDB(),
				textArray(st.Keywords), st.ModSeqSeen,
			})
		}

		copied, err := tx.CopyFrom(ctx,
			pgx.Identifier{"moov_copy_stage"},
			[]string{
				"seq", "account_id", "raw_sha256", "raw_size",
				"message_id", "in_reply_to", "references_ids",
				"subject", "from_addr", "to_addrs", "cc_addrs", "addresses",
				"date", "internal_date",
				"mime_structure", "has_attachments", "preview", "body_text",
				"parse_status", "parser", "parser_version", "defects",
				"mailbox_id", "uid", "uidvalidity", "flags", "keywords", "modseq_seen",
			},
			pgx.CopyFromRows(rows))
		if err != nil {
			return fmt.Errorf("copying %d messages into staging: %w", len(msgs), err)
		}
		if copied != int64(len(msgs)) {
			return fmt.Errorf("copy staged %d of %d messages", copied, len(msgs))
		}

		// Skip UIDs already stored BEFORE inserting into messages, so no row is
		// created that the state insert would then have to undo. The ON
		// CONFLICT clause below is still there — this pre-filter and that clause
		// are the same check-then-act pair as in InsertMessages, and the clause
		// is what makes the race harmless.
		if _, err := tx.Exec(ctx, `
			DELETE FROM moov_copy_stage sg
			 USING message_state ms
			 WHERE ms.mailbox_id = sg.mailbox_id
			   AND ms.uidvalidity = sg.uidvalidity
			   AND ms.uid = sg.uid`); err != nil {
			return fmt.Errorf("filtering already-stored uids: %w", err)
		}

		// A duplicate UID WITHIN one call would violate the unique index no
		// matter what the conflict clause says, because ON CONFLICT cannot
		// resolve two rows of the same statement against each other. Keeping the
		// lowest seq is arbitrary but deterministic.
		if _, err := tx.Exec(ctx, `
			DELETE FROM moov_copy_stage sg
			 WHERE sg.seq > (
			     SELECT min(other.seq) FROM moov_copy_stage other
			      WHERE other.mailbox_id = sg.mailbox_id
			        AND other.uidvalidity = sg.uidvalidity
			        AND other.uid = sg.uid
			 )`); err != nil {
			return fmt.Errorf("de-duplicating staged uids: %w", err)
		}

		// INSERT … SELECT … RETURNING is what recovers the generated ids, which
		// COPY itself cannot report. The seq column travels with them so the
		// state rows can be joined back to the right message.
		if _, err := tx.Exec(ctx, `
			CREATE TEMPORARY TABLE moov_copy_ids (seq int NOT NULL, message_id bigint NOT NULL)
			ON COMMIT DROP`); err != nil {
			return fmt.Errorf("creating copy id table: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			WITH inserted AS (
			    INSERT INTO messages (account_id, raw_sha256, raw_size,
			        message_id, in_reply_to, references_ids,
			        subject, from_addr, to_addrs, cc_addrs, addresses,
			        date, internal_date,
			        mime_structure, has_attachments, preview, body_text,
			        parse_status, parser, parser_version, defects)
			    SELECT account_id, raw_sha256, raw_size,
			           message_id, in_reply_to, references_ids,
			           subject, from_addr, to_addrs, cc_addrs, addresses,
			           date, internal_date,
			           mime_structure, has_attachments, preview, body_text,
			           parse_status, parser, parser_version, defects
			      FROM moov_copy_stage
			     ORDER BY seq
			    RETURNING id
			)
			INSERT INTO moov_copy_ids (seq, message_id)
			SELECT sg.seq, ins.id
			  FROM (SELECT id, row_number() OVER () AS rn FROM inserted) ins
			  JOIN (SELECT seq, row_number() OVER (ORDER BY seq) AS rn FROM moov_copy_stage) sg
			    ON sg.rn = ins.rn`); err != nil {
			return fmt.Errorf("inserting copied messages: %w", err)
		}

		tag, err := tx.Exec(ctx, `
			INSERT INTO message_state (message_id, account_id, mailbox_id,
			    uid, uidvalidity, flags, keywords, modseq_seen)
			SELECT ids.message_id, sg.account_id, sg.mailbox_id,
			       sg.uid, sg.uidvalidity, sg.flags, sg.keywords, sg.modseq_seen
			  FROM moov_copy_stage sg
			  JOIN moov_copy_ids ids ON ids.seq = sg.seq
			ON CONFLICT (mailbox_id, uidvalidity, uid) DO NOTHING`)
		if err != nil {
			return fmt.Errorf("inserting copied message state: %w", err)
		}
		res.Inserted = int(tag.RowsAffected())

		// Any message whose state the conflict clause skipped is now an orphan.
		// Removing it inside the same transaction is what keeps "a message row
		// always has state" an invariant rather than a usual case.
		orphans, err := tx.Exec(ctx, `
			DELETE FROM messages m
			 USING moov_copy_ids ids
			 WHERE m.id = ids.message_id
			   AND NOT EXISTS (SELECT 1 FROM message_state ms WHERE ms.message_id = m.id)`)
		if err != nil {
			return fmt.Errorf("removing orphaned copied messages: %w", err)
		}
		res.Skipped = len(msgs) - res.Inserted
		_ = orphans

		// The ids the caller needs for blob references: only the ones that
		// survived, keyed by the caller's own index.
		idRows, err := tx.Query(ctx, `
			SELECT ids.seq, ids.message_id
			  FROM moov_copy_ids ids
			  JOIN message_state ms ON ms.message_id = ids.message_id
			 ORDER BY ids.seq`)
		if err != nil {
			return fmt.Errorf("reading copied message ids: %w", err)
		}
		defer idRows.Close()

		res.IDs = make(map[int]int64, res.Inserted)
		for idRows.Next() {
			var seq int
			var id int64
			if err := idRows.Scan(&seq, &id); err != nil {
				return fmt.Errorf("reading copied message ids: %w", err)
			}
			res.IDs[seq] = id
		}
		if err := idRows.Err(); err != nil {
			return fmt.Errorf("reading copied message ids: %w", err)
		}
		return nil
	})
	if err != nil {
		return CopyResult{}, err
	}
	return res, nil
}

// CopyResult reports what a bulk load committed.
type CopyResult struct {
	// Inserted is how many message_state rows were created, which is the
	// number of messages that are now stored.
	Inserted int

	// Skipped is how many of the input rows were already present, either
	// filtered before the insert or refused by the conflict clause.
	Skipped int

	// IDs maps the caller's index in the input slice to the assigned message
	// id, for the messages that were actually stored. An index missing from the
	// map was skipped and has no new row to reference.
	IDs map[int]int64
}

// ErrCopyUnsupported is returned when a bulk load is attempted through a
// connection that cannot COPY. It is a sentinel so a caller can fall back to
// InsertMessages rather than failing the migration.
var ErrCopyUnsupported = errors.New("store: the connection does not support COPY")
