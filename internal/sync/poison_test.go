package sync

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// The sync-level regression for the 2026-08-12 production incident.
//
// # What happened
//
// Account 2's backfill failed permanently on its INBOX:
//
//	inserting 100 messages in "INBOX": inserting message 28 of 100:
//	ERROR: string is too long for tsvector (2062784 bytes, max 1048575 bytes)
//	(SQLSTATE 54000)
//
// One message carried >2 MB of extracted body text, which overflows
// PostgreSQL's 1 MiB tsvector limit. Two independent defects turned that into
// an outage rather than an incident:
//
//  1. Migration 0002 fed the generated `tsv` column an UNBOUNDED body_text, so
//     the row was unstorable at all. Migration 0003 caps each band at the
//     source.
//  2. The batch is 100 messages in ONE transaction, so the poison message took
//     99 innocent ones with it — and the supervisor, treating every error as
//     transient, retried the identical doomed batch every 5 minutes forever.
//     One message blocked an entire folder's backfill: the exact failure class
//     rule R4 (L2 §2.4) exists to prevent.
//
// The tests below pin BOTH halves. The first is the end-to-end property — a
// folder containing a pathological message syncs completely. The second pins
// the degraded path directly, because that is the layer that must keep working
// for the NEXT data-dependent error, whatever it turns out to be.

// hugeBodyText builds a body whose extracted text is adversarial for tsvector:
// every token distinct, so every token is a new lexeme rather than a cheap
// position on an existing one. A generator whose token space wraps produces a
// tsvector three orders of magnitude smaller and would not reproduce the bug.
func hugeBodyText(nbytes int) string {
	var b strings.Builder
	b.Grow(nbytes + 16)
	for i := 0; b.Len() < nbytes; i++ {
		b.WriteString(strconv.FormatInt(int64(i), 36))
		b.WriteByte(' ')
	}
	return b.String()[:nbytes]
}

// TestBackfillSurvivesAPoisonMessage is the end-to-end regression.
//
// A mailbox of 40 messages, one of which — in the middle of the first batch,
// as in production — carries a 2 MB body. Every message must end up stored and
// the mailbox must reach backfill_state='complete'.
//
// BEFORE the fix this fails: the batch aborts with SQLSTATE 54000, the error
// propagates out of the backfill, and the mailbox never completes.
func TestBackfillSurvivesAPoisonMessage(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	const total = 40
	const poisonIdx = 12 // mid-batch, as message 28 of 100 was

	srv := newFakeServer()
	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 900001)
	now := time.Now().UTC()
	seedMailbox(inbox, total, now, "Mensaje")

	// Replace one message's body with the pathological one. Everything else
	// about it stays a perfectly ordinary message, which is the point: nothing
	// upstream can tell it apart before the insert.
	poison := &inbox.messages[poisonIdx]
	poison.raw = buildMessage(poisonIdx, "Informe con adjunto gigante",
		now.Add(-time.Duration(total-1-poisonIdx)*time.Hour),
		hugeBodyText(2*1024*1024+64))

	opts := env.testOptions(now)
	opts.BatchSize = 10 // several batches, so the poison sits inside one of them
	s := env.syncer(t, srv, opts)

	if _, err := s.Run(ctx, env.account); err != nil {
		t.Fatalf("Run: %v\n\n"+
			"A single message with an oversized body must not fail the run. In "+
			"production this exact shape aborted a 100-message batch with "+
			"SQLSTATE 54000 and the supervisor retried it every 5 minutes "+
			"forever, blocking the whole folder's backfill (rule R4, L2 §2.4).", err)
	}

	// Every message landed — including the poison one, which is stored rather
	// than skipped: its blob is durable and its UID is occupied.
	if got := env.countMessages(t); got != total {
		t.Errorf("stored %d messages, want %d — the poison message must cost "+
			"itself at most, never its batch", got, total)
	}

	stored := env.storedUIDs(t)
	if len(stored["INBOX"]) != total {
		t.Errorf("INBOX holds %d uids, want %d", len(stored["INBOX"]), total)
	}

	// And the mailbox actually finished, rather than stalling mid-backfill.
	boxes, err := env.store.ListMailboxes(ctx, env.account.ID)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	for _, mb := range boxes {
		if mb.Name != "INBOX" {
			continue
		}
		if mb.BackfillState != store.BackfillComplete {
			t.Errorf("INBOX backfill_state = %q, want %q — the folder must reach "+
				"completion despite the poison message",
				mb.BackfillState, store.BackfillComplete)
		}
	}
}

// TestDegradedInsertQuarantinesOnlyTheBadMessage pins the SECOND layer on its
// own, with the migration's cap deliberately taken out of the picture.
//
// Migration 0003 makes the tsvector overflow unreachable, which is correct —
// but it also means the end-to-end test above would keep passing if the
// degraded path were deleted. The degraded path is what protects against the
// NEXT data-dependent insert error, so it needs a test that does not depend on
// the one cause that is now fixed.
//
// A CHECK constraint on `messages` supplies a data error (SQLSTATE 23514) that
// no cap can prevent, targeting exactly one message of a batch.
func TestDegradedInsertQuarantinesOnlyTheBadMessage(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	const total = 20
	const poisonIdx = 6
	const poisonSubject = "ASUNTO-QUE-LA-BASE-RECHAZA"

	// A constraint that rejects one specific message by content. It is dropped
	// again on cleanup, and it is scoped by subject so no other test's rows can
	// trip it.
	if _, err := env.store.Pool().Exec(ctx, `
		ALTER TABLE messages ADD CONSTRAINT moov_test_reject_subject
		CHECK (subject <> '`+poisonSubject+`') NOT VALID`); err != nil {
		t.Fatalf("adding the test constraint: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.store.Pool().Exec(context.Background(),
			`ALTER TABLE messages DROP CONSTRAINT IF EXISTS moov_test_reject_subject`)
	})

	srv := newFakeServer()
	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 900002)
	now := time.Now().UTC()
	seedMailbox(inbox, total, now, "Mensaje")

	poison := &inbox.messages[poisonIdx]
	poison.raw = buildMessage(poisonIdx, poisonSubject,
		now.Add(-time.Duration(total-1-poisonIdx)*time.Hour),
		"un cuerpo perfectamente normal")

	opts := env.testOptions(now)
	opts.BatchSize = 10
	s := env.syncer(t, srv, opts)

	if _, err := s.Run(ctx, env.account); err != nil {
		t.Fatalf("Run: %v\n\n"+
			"A row the database refuses for its CONTENT must cost one message, "+
			"not the batch it traveled in. This is the failure CLASS the "+
			"degraded path exists for — the tsvector overflow was only its "+
			"first instance.", err)
	}

	// Every message is present: the 19 good ones normally, the rejected one
	// quarantined with its derived fields stripped.
	if got := env.countMessages(t); got != total {
		t.Errorf("stored %d messages, want %d", got, total)
	}

	// The rejected one is recorded as failed, which is what makes it visible to
	// the re-parse sweep and to the failure-rate alert rather than silently
	// absorbed.
	if got := env.countByParseStatus(t, store.ParseFailed); got != 1 {
		t.Errorf("%d messages with parse_status='failed', want exactly 1 — the "+
			"quarantined message must be reported, not hidden", got)
	}

	// Its subject was stripped (that is what the database refused), and the
	// good messages kept theirs.
	var withSubject int
	if err := env.store.Pool().QueryRow(ctx, `
		SELECT count(*) FROM messages
		 WHERE account_id = $1 AND subject <> ''`, env.account.ID).Scan(&withSubject); err != nil {
		t.Fatalf("counting subjects: %v", err)
	}
	if withSubject != total-1 {
		t.Errorf("%d messages kept a subject, want %d — only the quarantined row "+
			"should have been stripped", withSubject, total-1)
	}

	// The blob reference survived for the quarantined message too: its raw bytes
	// remain durable, so a later schema fix or parser bump can re-derive it.
	var refs int
	if err := env.store.Pool().QueryRow(ctx, `
		SELECT count(*) FROM blob_refs WHERE account_id = $1 AND owner_kind = 'message'`,
		env.account.ID).Scan(&refs); err != nil {
		t.Fatalf("counting blob refs: %v", err)
	}
	if refs != total {
		t.Errorf("%d blob references, want %d — a quarantined message must keep "+
			"its raw bytes referenced, or the GC reclaims the only copy", refs, total)
	}
}

// TestIsDataError pins the classification the degraded path turns on.
//
// Getting this wrong in either direction is expensive: treating a connection
// loss as a data error would quarantine perfectly good messages en masse, and
// treating a data error as transient is the production bug itself.
func TestIsDataError(t *testing.T) {
	cases := []struct {
		code string
		name string
		want bool
	}{
		{"54000", "program_limit_exceeded (the tsvector overflow)", true},
		{"22001", "string_data_right_truncation", true},
		{"22021", "character_not_in_repertoire", true},
		{"23502", "not_null_violation", true},
		{"23514", "check_violation", true},
		{"23503", "foreign_key_violation", true},

		// Excluded on purpose: InsertMessages resolves a duplicate UID through
		// its ON CONFLICT clause, so a 23505 reaching the degraded path means
		// something unmodelled, and swallowing it would hide it.
		{"23505", "unique_violation", false},

		// Transient / systemic: these must keep the existing retry semantics.
		{"40P01", "deadlock_detected", false},
		{"40001", "serialization_failure", false},
		{"08006", "connection_failure", false},
		{"53300", "too_many_connections", false},
		{"57014", "query_canceled", false},
		{"XX000", "internal_error", false},
	}

	for _, tc := range cases {
		t.Run(tc.code+"_"+tc.name, func(t *testing.T) {
			err := fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: tc.code, Message: tc.name})
			if got := isDataError(err); got != tc.want {
				t.Errorf("isDataError(%s %s) = %v, want %v", tc.code, tc.name, got, tc.want)
			}
		})
	}

	if isDataError(nil) {
		t.Error("isDataError(nil) = true, want false")
	}
	if isDataError(fmt.Errorf("a plain error")) {
		t.Error("isDataError on a non-pg error = true, want false")
	}
}
