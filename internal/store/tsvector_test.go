package store_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// Regression tests for the tsvector size limit (production bug, 2026-08-12).
//
// # What happened
//
// Account 2's backfill failed permanently on a real message:
//
//	ERROR: string is too long for tsvector (2062784 bytes, max 1048575 bytes)
//	(SQLSTATE 54000) — inserting message 28 of 100 in "INBOX"
//
// PostgreSQL's tsvector has a hard 1 MiB limit (MAXSTRLEN, 2^20-1 bytes of
// lexeme data). Migration 0002 fed the generated `tsv` column an UNBOUNDED
// body_text, so any message whose extracted text exceeded roughly 440 KiB of
// dense unique tokens made the row unstorable. Because the insert is one
// batch of 100 in one transaction, the poison message failed the whole batch,
// and because the supervisor treats every error as transient it retried every
// 5 minutes forever — one message blocking an entire folder's backfill, which
// is exactly the failure class rule R4 (L2 §2.4) exists to prevent.
//
// # What these tests pin
//
//   - A message with a >2 MiB body_text must INSERT (migration 0003 caps each
//     band at the source with left()).
//   - An ADVERSARIAL input constructed at the cap — the token shape that
//     produces the largest possible tsvector per input byte — must also insert,
//     which is what proves the cap was chosen with real headroom rather than
//     by eyeballing the failing figure.
//   - The capped text is still SEARCHABLE up to the cap, and the body text
//     itself is stored in full (only the search vector is bounded).

// tsvBodyCapBytes mirrors the body cap migration 0003 applies. Kept here as a
// literal rather than read from the schema so the test fails loudly if the
// migration changes the number without anyone revisiting the arithmetic.
const tsvBodyCapBytes = 192 * 1024

// tsvLimitBytes is PostgreSQL's hard tsvector limit (MAXSTRLEN, 2^20-1).
const tsvLimitBytes = 1048575

// tsvRequiredMargin is the safety factor migration 0003 was chosen to hold
// against a worst-case input: the resulting tsvector must stay at or under
// 1/1.5 of the limit. The migration's own arithmetic targets 1.75x; requiring
// 1.5x here leaves room for measurement noise while still failing loudly if
// someone raises a cap without redoing the sums.
const tsvRequiredMargin = 1.5

// adversarialText builds the input shape that produces the LARGEST tsvector per
// input byte.
//
// Two properties make it the worst case, and both are load-bearing:
//
//   - Every token is DISTINCT. A repeated token collapses to one lexeme with a
//     position list, and positions are far cheaper than lexemes — 40,000
//     repetitions of one 4-character word produce a 528-byte tsvector, three
//     orders of magnitude below the same bytes spent on unique tokens. A
//     generator whose token space wraps silently stops being adversarial.
//   - Tokens are SHORT. Each lexeme pays a fixed per-entry overhead, so short
//     tokens maximize entries per byte. Measured on PostgreSQL 17.4, unique
//     4-character tokens peak at 2.367 bytes of tsvector per input byte
//     (migration 0003's comment carries the full table).
//
// Uniqueness across an arbitrarily long span is what forces the variable width:
// the counter is rendered in base 36 with no padding, so the token space never
// wraps. Tokens stay 4 characters for the first ~1.6M of them, which is past
// the point any cap here cares about.
func adversarialText(nbytes int) string {
	var b strings.Builder
	b.Grow(nbytes + 16)
	for i := 0; b.Len() < nbytes; i++ {
		b.WriteString(strconv.FormatInt(int64(i), 36))
		b.WriteByte(' ')
	}
	return b.String()[:nbytes]
}

// TestInsertMessageWithHugeBodyText is the primary regression.
//
// BEFORE migration 0003 this fails with SQLSTATE 54000 ("string is too long for
// tsvector"), which is the production error verbatim. AFTER it, the insert
// succeeds and the message is stored and searchable.
func TestInsertMessageWithHugeBodyText(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	mbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	// >2 MiB, matching the production message's scale (2,062,784 bytes of
	// extracted text). A distinctive marker sits at the very front so the
	// searchability assertion below has something to look for.
	body := "facturaenorme " + adversarialText(2*1024*1024+64)

	ids, err := s.InsertMessages(ctx, []store.NewMessage{{
		Message: store.Message{
			AccountID: acct.ID,
			RawSHA256: seedBlob(t, s, "huge-body-message"),
			RawSize:   int64(len(body)),
			Subject:   "Mensaje con cuerpo enorme",
			FromAddr:  "remitente@example.test",
			BodyText:  body,
			Date:      time.Now().UTC(),
		},
		State: store.MessageState{
			AccountID: acct.ID, MailboxID: mbox.ID, UID: 1, UIDValidity: 1,
		},
	}})
	if err != nil {
		t.Fatalf("InsertMessages with a %d-byte body: %v\n\n"+
			"This is the production bug: PostgreSQL's tsvector caps at 1 MiB "+
			"(MAXSTRLEN) and migration 0002 fed it an unbounded body_text. "+
			"Migration 0003 caps each band at the source.", len(body), err)
	}

	// The body is stored IN FULL. Only the search vector is bounded — the
	// message itself must not be truncated, or reading it back would show the
	// user a mutilated mail.
	got, err := s.GetMessage(ctx, ids[0])
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if len(got.BodyText) != len(body) {
		t.Errorf("body_text was truncated: stored %d bytes, wrote %d.\n"+
			"The CAP belongs to the tsv expression, not to the column: the store "+
			"keeps the whole message and only bounds what it indexes.",
			len(got.BodyText), len(body))
	}

	// And the head of the body is searchable: the cap bounds recall for the
	// tail of a huge message, it does not disable indexing.
	var tsvBytes int
	if err := s.Pool().QueryRow(ctx,
		`SELECT pg_column_size(tsv) FROM messages WHERE id = $1`, ids[0]).Scan(&tsvBytes); err != nil {
		t.Fatalf("reading tsv size: %v", err)
	}
	t.Logf("tsv of a %d-byte body: %d bytes (%.1f%% of the 1048575-byte limit)",
		len(body), tsvBytes, 100*float64(tsvBytes)/1048575)

	var hits int
	if err := s.Pool().QueryRow(ctx, `
		SELECT count(*) FROM messages
		 WHERE account_id = $1 AND tsv @@ to_tsquery('simple', 'facturaenorme')`,
		acct.ID).Scan(&hits); err != nil {
		t.Fatalf("searching the capped body: %v", err)
	}
	if hits != 1 {
		t.Errorf("a term at the head of the body is not searchable (%d hits, want 1); "+
			"the cap must bound the tail, not disable body indexing", hits)
	}
}

// TestTsvectorCapSurvivesAdversarialInputAtTheCap is the arithmetic proof.
//
// It constructs, at EXACTLY the cap, the input shape that yields the largest
// tsvector per byte — many long unique tokens — in all three weighted bands at
// once, and asserts the row inserts. This is what makes the chosen cap a
// justified number rather than a guess: if someone raises the body cap without
// redoing the arithmetic, this test is what catches it.
func TestTsvectorCapSurvivesAdversarialInputAtTheCap(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	mbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	// Every band filled to well beyond its own cap with worst-case tokens, so
	// the generated column has to truncate all three and the result is the
	// largest tsvector this schema can produce.
	subject := adversarialText(64 * 1024)
	addrs := adversarialText(64 * 1024)
	body := adversarialText(tsvBodyCapBytes * 3)

	ids, err := s.InsertMessages(ctx, []store.NewMessage{{
		Message: store.Message{
			AccountID: acct.ID,
			RawSHA256: seedBlob(t, s, "adversarial-at-cap"),
			RawSize:   int64(len(body)),
			Subject:   subject,
			FromAddr:  addrs,
			ToAddrs:   addrs,
			CcAddrs:   addrs,
			BodyText:  body,
			Date:      time.Now().UTC(),
		},
		State: store.MessageState{
			AccountID: acct.ID, MailboxID: mbox.ID, UID: 2, UIDValidity: 1,
		},
	}})
	if err != nil {
		t.Fatalf("adversarial input at the cap did not insert: %v\n\n"+
			"The caps in migration 0003 must leave headroom for the WORST-CASE "+
			"tsvector, not the average one: measured peak is 2.367 bytes of "+
			"tsvector per input byte (unique 4-character tokens).", err)
	}

	var tsvBytes int
	if err := s.Pool().QueryRow(ctx,
		`SELECT pg_column_size(tsv) FROM messages WHERE id = $1`, ids[0]).Scan(&tsvBytes); err != nil {
		t.Fatalf("reading tsv size: %v", err)
	}

	t.Logf("worst-case tsv: %d bytes (%.1f%% of the %d-byte limit, margin %.2fx)",
		tsvBytes, 100*float64(tsvBytes)/tsvLimitBytes, tsvLimitBytes,
		float64(tsvLimitBytes)/float64(tsvBytes))

	// Not merely "under the limit" — under it with the margin the cap was
	// CHOSEN for. Landing at 99% would technically pass an insert today and
	// fail on the next message with a slightly worse token distribution, which
	// is the same class of latent bug as the one being fixed.
	if maxAllowed := int(float64(tsvLimitBytes) / tsvRequiredMargin); tsvBytes > maxAllowed {
		t.Errorf("worst-case tsv is %d bytes; the caps must keep it at or under %d "+
			"(a %.2fx margin below the %d-byte limit).\n"+
			"Measured peak expansion is ~2.83 bytes of tsvector per input byte, so a "+
			"body cap of B bytes must satisfy roughly 3*B < %d. Redo the arithmetic "+
			"in migration 0003 before raising any cap.",
			tsvBytes, maxAllowed, tsvRequiredMargin, tsvLimitBytes, tsvLimitBytes)
	}
}

// TestHugeBodyDoesNotPoisonABatch is the batch-level consequence.
//
// The production failure was not that one message could not be stored — it was
// that one message took NINETY-NINE innocent ones down with it, permanently,
// because they shared a transaction. This asserts the whole batch lands.
func TestHugeBodyDoesNotPoisonABatch(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	mbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	const batchSize = 20
	const poisonIdx = 7 // as in production: a message in the middle of the batch

	msgs := make([]store.NewMessage, batchSize)
	base := time.Now().UTC().Add(-time.Duration(batchSize) * time.Hour)
	for i := range batchSize {
		body := fmt.Sprintf("cuerpo normal del mensaje %d", i)
		if i == poisonIdx {
			body = adversarialText(2*1024*1024 + 128)
		}
		msgs[i] = store.NewMessage{
			Message: store.Message{
				AccountID: acct.ID,
				RawSHA256: seedBlob(t, s, fmt.Sprintf("batch-poison-%d", i)),
				RawSize:   int64(len(body)),
				Subject:   fmt.Sprintf("Mensaje %d", i),
				BodyText:  body,
				Date:      base.Add(time.Duration(i) * time.Hour),
			},
			State: store.MessageState{
				AccountID: acct.ID, MailboxID: mbox.ID,
				UID: int64(i + 1), UIDValidity: 1, ModSeqSeen: int64(i),
			},
		}
	}

	if _, err := s.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("a batch containing one oversized message failed as a unit: %v\n\n"+
			"This is the production failure mode: message %d of the batch poisoned "+
			"the other %d, and the supervisor retried the same doomed batch every "+
			"5 minutes forever.", err, poisonIdx+1, batchSize-1)
	}

	var stored int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM messages WHERE account_id = $1`, acct.ID).Scan(&stored); err != nil {
		t.Fatalf("counting stored messages: %v", err)
	}
	if stored != batchSize {
		t.Errorf("stored %d of %d messages; the whole batch must land", stored, batchSize)
	}
}
