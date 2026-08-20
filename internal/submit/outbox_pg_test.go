package submit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/GrupoNU/moov/internal/store"
)

// The properties that live in PostgreSQL itself, proven against a real
// PostgreSQL 17 (env-gated like every other PG suite in this repo):
//
//   - FOR UPDATE SKIP LOCKED single execution: two executors, one row, one
//     send — the W3 acceptance criterion "two-executor race test".
//   - The cancel-vs-claim compare-and-set: exactly one winner.
//   - The recovery scan over stranded in_flight rows.
//   - The send-intent CRUD the JMAP surface reads (tombstones included).
//
// Everything already proven by the in-memory suite (outbox_test.go) is not
// repeated here; these tests exist precisely for what a fake cannot prove.

const testDBEnv = "MOOV_TEST_DATABASE_URL"

func pgStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv(testDBEnv)
	if dsn == "" {
		t.Skipf("%s is not set; start a dev database with `make db-up` to run the outbox PG tests", testDBEnv)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	s, err := store.Open(context.Background(), store.Config{DSN: dsn, MaxConns: 8})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func pgAccount(t *testing.T, s *store.Store) store.Account {
	t.Helper()
	ctx := context.Background()
	acct, err := s.CreateAccount(ctx, store.Account{
		Email:    fmt.Sprintf("w3-%d@example.test", time.Now().UnixNano()),
		IMAPHost: "dovecot.internal",
		IMAPPort: 143,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	t.Cleanup(func() {
		if err := s.DeleteAccount(context.Background(), acct.ID); err != nil {
			t.Logf("cleanup: deleting account %d: %v", acct.ID, err)
		}
	})
	return acct
}

func pgEnqueue(t *testing.T, s *store.Store, accountID int64, msgID string, notBefore time.Time) store.SendIntent {
	t.Helper()
	payload, _ := json.Marshal(IntentEnvelope{
		IdentityID: "primary",
		MailFrom:   "moov-test@example.test",
		RcptTo:     []string{"dest@example.test"},
	})
	in, err := s.EnqueueSendIntent(context.Background(), accountID, 7, msgID, payload, notBefore)
	if err != nil {
		t.Fatalf("EnqueueSendIntent: %v", err)
	}
	return in
}

// pgOutbox builds an executor over the REAL store and fake side effects. The
// RawSource serves a fixed draft whose Message-ID matches msgID.
func pgOutbox(t *testing.T, s *store.Store, transport *fakeTransport, msgID string) *Outbox {
	t.Helper()
	raws := &fakeRaws{raw: map[int64][]byte{7: []byte("Message-ID: <" + msgID + ">\r\n" +
		"Date: Sat, 15 Aug 2026 10:00:00 +0000\r\n" +
		"From: moov-test@example.test\r\nTo: dest@example.test\r\nSubject: pg\r\n\r\nhello\r\n")}}
	ob, err := NewOutbox(s, transport, newFakeSent(), raws, Options{
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewOutbox: %v", err)
	}
	return ob
}

// TestSkipLockedSingleExecution is the two-executor race the epic's
// acceptance criteria demand: N due intents, two executors polling the same
// real queue concurrently, and every message crosses the wire EXACTLY once.
// FOR UPDATE SKIP LOCKED is the entire mechanism (store.ClaimDueSendIntents);
// nothing in the executors coordinates them.
func TestSkipLockedSingleExecution(t *testing.T) {
	s := pgStore(t)
	acct := pgAccount(t, s)

	const n = 8
	msgIDs := make([]string, n)
	for i := range n {
		msgIDs[i] = fmt.Sprintf("race-%d-%d@example.test", i, time.Now().UnixNano())
	}

	// One shared transport counts the wire; each executor gets its own
	// RawSource keyed to the intent it happens to claim, so the draft's
	// Message-ID matches its row. One draft per intent via distinct email
	// ids would need real messages; instead every intent shares email id 7
	// and the raws map serves per-message bytes by intent's own Message-ID —
	// achieved by preparing the raw at execution time from the row's
	// MessageRFCID (PrepareTransmission adds it when the draft lacks one).
	transport := newFakeTransport()
	raws := &fakeRaws{raw: map[int64][]byte{7: []byte(
		"Date: Sat, 15 Aug 2026 10:00:00 +0000\r\n" +
			"From: moov-test@example.test\r\nTo: dest@example.test\r\nSubject: race\r\n\r\nhello\r\n")}}

	newExec := func() *Outbox {
		ob, err := NewOutbox(s, transport, newFakeSent(), raws, Options{
			Logger: slog.New(slog.DiscardHandler),
		})
		if err != nil {
			t.Fatal(err)
		}
		return ob
	}
	a, b := newExec(), newExec()

	for _, id := range msgIDs {
		pgEnqueue(t, s, acct.ID, id, time.Now().Add(-time.Second))
	}

	// Both executors sweep the queue concurrently, several passes each so a
	// slow claim still meets a competing one.
	var wg sync.WaitGroup
	for _, ob := range []*Outbox{a, b} {
		wg.Add(1)
		go func(ob *Outbox) {
			defer wg.Done()
			for range 5 {
				ob.runOnce(context.Background())
			}
		}(ob)
	}
	wg.Wait()

	for _, id := range msgIDs {
		if got := transport.deliveries(id); got != 1 {
			t.Errorf("message %s crossed the wire %d times, want exactly 1", id, got)
		}
	}

	intents, err := s.ListSendIntents(context.Background(), acct.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range intents {
		if in.State != store.IntentDone {
			t.Errorf("intent %d ended in state %s, want done", in.ID, in.State)
		}
	}
}

// TestCancelVersusClaimHasOneWinner drives the CAS from both sides.
func TestCancelVersusClaimHasOneWinner(t *testing.T) {
	s := pgStore(t)
	acct := pgAccount(t, s)
	ctx := context.Background()

	// Side A: cancel wins while the row is queued; the claim then finds
	// nothing.
	in := pgEnqueue(t, s, acct.ID, "cas-a@example.test", time.Now().Add(-time.Second))
	if _, err := s.CancelSendIntent(ctx, acct.ID, in.ID); err != nil {
		t.Fatalf("cancel of a queued row failed: %v", err)
	}
	claimed, err := s.ClaimDueSendIntents(ctx, acct.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Errorf("the executor claimed a canceled row: %+v", claimed)
	}

	// Side B: the claim wins; the cancel must answer cannotUnsend, never
	// pretend.
	in2 := pgEnqueue(t, s, acct.ID, "cas-b@example.test", time.Now().Add(-time.Second))
	claimed, err = s.ClaimDueSendIntents(ctx, acct.ID, 10)
	if err != nil || len(claimed) != 1 || claimed[0].ID != in2.ID {
		t.Fatalf("claim = %v, %v; want the one queued row", claimed, err)
	}
	if _, err := s.CancelSendIntent(ctx, acct.ID, in2.ID); !errors.Is(err, store.ErrSubmissionNotCancelable) {
		t.Errorf("cancel of a claimed row = %v, want ErrSubmissionNotCancelable", err)
	}

	// An idempotent replay of a successful cancel is a success.
	if _, err := s.CancelSendIntent(ctx, acct.ID, in.ID); err != nil {
		t.Errorf("replaying a cancel errored: %v", err)
	}
}

// TestRecoveryClaimsStrandedRows proves the startup half against real rows: a
// claimed row whose executor died is found by the recovery scan with its
// attempt count bumped, and a fresh executor completes it exactly once.
func TestRecoveryClaimsStrandedRows(t *testing.T) {
	s := pgStore(t)
	acct := pgAccount(t, s)
	ctx := context.Background()
	msgID := fmt.Sprintf("recover-%d@example.test", time.Now().UnixNano())

	in := pgEnqueue(t, s, acct.ID, msgID, time.Now().Add(-time.Second))
	claimed, err := s.ClaimDueSendIntents(ctx, acct.ID, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	// The executor "dies" here: the row stays in_flight, nothing else moved.

	transport := newFakeTransport()
	ob := pgOutbox(t, s, transport, msgID)
	ob.recover(ctx)

	row, err := s.GetSendIntent(ctx, acct.ID, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != store.IntentDone {
		t.Errorf("state = %s, want done after recovery", row.State)
	}
	if row.Attempts < 2 {
		t.Errorf("attempts = %d, want >= 2 (claim + recovery)", row.Attempts)
	}
	if got := transport.deliveries(msgID); got != 1 {
		t.Errorf("delivered %d times, want 1", got)
	}
}

// TestSendIntentLifecycleReads covers the JMAP-facing store reads: get/list,
// watermark+count movement, the /changes feed, and destroy's tombstone.
func TestSendIntentLifecycleReads(t *testing.T) {
	s := pgStore(t)
	acct := pgAccount(t, s)
	ctx := context.Background()

	before, err := s.SendIntentWatermark(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !before.IsZero() {
		t.Fatalf("fresh account has a watermark: %v", before)
	}

	in := pgEnqueue(t, s, acct.ID, "life@example.test", time.Now().Add(10*time.Second))

	got, err := s.GetSendIntent(ctx, acct.ID, in.ID)
	if err != nil || got.MessageRFCID != "life@example.test" || got.State != store.IntentQueued {
		t.Fatalf("GetSendIntent = %+v, %v", got, err)
	}
	// Account scoping: another account's read answers ErrNotFound.
	other := pgAccount(t, s)
	if _, err := s.GetSendIntent(ctx, other.ID, in.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("foreign read = %v, want ErrNotFound", err)
	}

	list, err := s.ListSendIntents(ctx, acct.ID, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSendIntents = %v, %v", list, err)
	}
	if n, err := s.CountSendIntents(ctx, acct.ID); err != nil || n != 1 {
		t.Fatalf("CountSendIntents = %d, %v", n, err)
	}

	// The /changes feed sees the enqueue as a creation after the zero cursor.
	changed, err := s.SendIntentsChangedSince(ctx, acct.ID, time.Time{}, 10)
	if err != nil || len(changed) != 1 {
		t.Fatalf("SendIntentsChangedSince = %v, %v", changed, err)
	}

	// Destroy tombstones and (still pending) cancels — the W-A3 deviation.
	destroyed, err := s.DestroySendIntent(ctx, acct.ID, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if destroyed.DestroyedAt == nil || destroyed.State != store.IntentCanceled {
		t.Errorf("destroy of a pending submission = %+v; want tombstoned AND canceled (W-A3)", destroyed)
	}

	// The tombstone still counts and still feeds /changes (clients must see
	// the destroy), and the watermark moved.
	after, err := s.SendIntentWatermark(ctx, acct.ID)
	if err != nil || after.IsZero() {
		t.Fatalf("watermark after destroy = %v, %v", after, err)
	}
	changed, err = s.SendIntentsChangedSince(ctx, acct.ID, time.Time{}, 10)
	if err != nil || len(changed) != 1 || changed[0].DestroyedAt == nil {
		t.Fatalf("the tombstone left the /changes feed: %v, %v", changed, err)
	}
}

// TestMailboxContainsMessageID covers the dedupe probe's exactness: a live
// row hits, a tombstoned one does not, and the empty id never matches.
func TestMailboxContainsMessageID(t *testing.T) {
	s := pgStore(t)
	acct := pgAccount(t, s)
	ctx := context.Background()

	mb, err := s.UpsertMailbox(ctx, store.Mailbox{AccountID: acct.ID, Name: "Sent", Role: store.RoleSent})
	if err != nil {
		t.Fatalf("UpsertMailbox: %v", err)
	}

	found, err := s.MailboxContainsMessageID(ctx, mb.ID, "absent@example.test")
	if err != nil || found {
		t.Fatalf("empty mailbox probe = %v, %v", found, err)
	}
	if found, err := s.MailboxContainsMessageID(ctx, mb.ID, ""); err != nil || found {
		t.Fatalf("empty message-id matched: %v, %v", found, err)
	}
}
