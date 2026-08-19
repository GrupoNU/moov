package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// THE PLAN CANARY FOR THREADING.
//
// migration 0004 makes messages_acct_thread a PARTIAL index (WHERE thread_id IS
// NOT NULL), because a plain index on these columns competes with the composite
// GIN and collapses search — a regression the search canary caught twice while
// this was being built, and which is documented at length in the migration.
//
// A partial index has a failure mode the search canary cannot see: it is
// silently ignored by any query whose predicate PostgreSQL cannot prove implies
// the index predicate. Threading would still be CORRECT, just unindexed, and
// every functional test would stay green while Thread/get degraded to a heap
// scan. That is precisely the class of regression bench_test.go exists to catch
// for search, and this is its threading counterpart.
//
// It asserts both halves of the arrangement:
//
//  1. the thread read USES the index, so the partial predicate did not lock the
//     store out of its own index;
//  2. a search does NOT use it, so the index has not gone back to competing
//     with the GIN.
func TestThreadReadUsesTheThreadIndex(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	acct := newAccount(t, s)
	mb := threadMailbox(t, s, acct.ID, "INBOX")

	// Enough rows that an index is plausibly cheaper than a scan. A handful
	// would leave the planner free to pick a seq scan on size alone, which
	// would make the assertion meaningless rather than wrong.
	const n = 2000
	clock := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	rows := make([]store.NewMessage, 0, n)
	for i := range n {
		clock = clock.Add(time.Minute)
		rows = append(rows, store.NewMessage{
			Message: store.Message{
				AccountID: acct.ID,
				RawSHA256: seedBlob(t, s, "plan-"+itoa(i)+"-"+acct.Email),
				RawSize:   100,
				MessageID: "plan-" + itoa(i) + "@test",
				Subject:   "Plan canary message " + itoa(i),
				BodyText:  "cuerpo del mensaje numero " + itoa(i),
				Date:      clock,
			},
			State: store.MessageState{
				AccountID: acct.ID, MailboxID: mb.ID,
				UID: int64(i + 1), UIDValidity: 1,
			},
		})
	}
	ids, err := s.InsertMessages(ctx, rows)
	if err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `ANALYZE messages`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}

	thread := ids[0]

	t.Run("the thread read uses messages_acct_thread", func(t *testing.T) {
		plan := explain(t, s, `
			SELECT m.thread_id, m.id
			  FROM messages m
			  JOIN message_state ms ON ms.message_id = m.id
			 WHERE m.thread_id IS NOT NULL
			   AND m.thread_id = ANY($1)
			   AND m.account_id = $2
			   AND ms.deleted_at IS NULL
			 ORDER BY m.thread_id, m.date, m.id
			 LIMIT 500`, []int64{thread}, acct.ID)

		if !strings.Contains(plan, "messages_acct_thread") {
			t.Errorf("the thread read does NOT use messages_acct_thread.\n"+
				"The index is partial (WHERE thread_id IS NOT NULL, migration 0004); a query "+
				"that omits that predicate cannot use it, so Thread/get would silently degrade "+
				"to a heap scan while every functional test stayed green.\nPlan:\n%s", plan)
		}
	})

	t.Run("a search does NOT use messages_acct_thread", func(t *testing.T) {
		// The other half of the bargain. If this fails, the threading index has
		// gone back to competing with the composite GIN for search queries,
		// which S3 §5.2 measured at up to 6,600x.
		plan := explain(t, s, `
			SELECT m.id FROM messages m
			 WHERE m.account_id = $1
			   AND m.tsv @@ websearch_to_tsquery('simple', immutable_unaccent($2))
			 ORDER BY m.date DESC LIMIT 50`, acct.ID, "canary")

		if strings.Contains(plan, "messages_acct_thread") {
			t.Errorf("a full-text search reached messages_acct_thread.\n"+
				"That index must not be reachable from an account_id predicate alone, or it "+
				"competes with gin(account_id, tsv) and search collapses (S3 §5.2).\nPlan:\n%s", plan)
		}
	})
}

// explain returns a query's plan as text.
func explain(t *testing.T, s *store.Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.Pool().Query(context.Background(), "EXPLAIN (FORMAT TEXT) "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scanning plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading plan: %v", err)
	}
	return plan.String()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
