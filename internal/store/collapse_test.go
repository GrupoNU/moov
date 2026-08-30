package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// The collapsed repertoire (L3 epic E1): correctness, paging, and the plan
// canary that keeps the shape from silently regressing into the unbounded one.

// collapseCorpus seeds one mailbox with `threads` conversations of
// `perThread` messages each, newest thread first, and returns the account, the
// mailbox, and the ids in insertion order.
//
// Threading is assigned DIRECTLY rather than through AssignThreads: this file
// tests the collapse query, and going through the full JWZ path would make each
// test cost the threading path's several round trips per message for no extra
// coverage (threads_test.go owns that). The assignment still respects invariant
// I1 — the thread id is the OLDEST member's id — because a collapse that reads a
// thread_id which is not a real message id would pass a test and fail in
// production.
func collapseCorpus(t *testing.T, s *store.Store, threads, perThread int) (store.Account, store.Mailbox, []int64) {
	t.Helper()
	ctx := context.Background()

	acct := newAccount(t, s)
	inbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	now := time.Now().UTC()
	msgs := make([]store.NewMessage, 0, threads*perThread)
	uid := int64(0)
	for th := range threads {
		for k := range perThread {
			uid++
			msgs = append(msgs, store.NewMessage{
				Message: store.Message{
					AccountID: acct.ID,
					RawSHA256: seedBlob(t, s, fmt.Sprintf("collapse-%d-%d-%d", acct.ID, th, k)),
					RawSize:   200,
					MessageID: fmt.Sprintf("collapse-%d-%d-%d@test", acct.ID, th, k),
					Subject:   fmt.Sprintf("Hilo %d mensaje %d %s", th, k, needleRare),
					FromAddr:  "remitente@example.test",
					ToAddrs:   "destinatario@example.test",
					BodyText:  "cuerpo con el termino " + needleRare,
					Preview:   "cuerpo con el termino",
					// Strictly decreasing, so message 0 of thread 0 is the newest
					// in the whole corpus and every (date, id) pair is unique —
					// the collapse's order is only unambiguous if the underlying
					// message order is.
					Date: now.Add(-time.Duration(uid) * time.Minute),
				},
				State: store.MessageState{
					AccountID: acct.ID, MailboxID: inbox.ID,
					UID: uid, UIDValidity: 1, ModSeqSeen: uid,
				},
			})
		}
	}
	ids, err := s.InsertMessages(ctx, msgs)
	if err != nil {
		t.Fatalf("seeding %d messages: %v", len(msgs), err)
	}

	// Group them: every run of perThread consecutive ids is one thread, rooted
	// at the run's smallest id (invariant I1).
	for th := range threads {
		root := ids[th*perThread]
		members := ids[th*perThread : (th+1)*perThread]
		if _, err := s.Pool().Exec(ctx,
			`UPDATE messages SET thread_id = $1 WHERE account_id = $2 AND id = ANY($3)`,
			root, acct.ID, members); err != nil {
			t.Fatalf("assigning thread %d: %v", th, err)
		}
	}
	if _, err := s.Pool().Exec(ctx, `ANALYZE messages`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}
	return acct, inbox, ids
}

// TestCollapseKeepsOneRowPerThread is the RFC 8621 §4.4.3 semantics itself:
// "Emails in the same Thread as a previous Email in the list ... will be removed
// from the list". For a newest-first list that means each thread is represented
// by its NEWEST message, exactly once.
func TestCollapseKeepsOneRowPerThread(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	const threads, perThread = 40, 5
	acct, inbox, ids := collapseCorpus(t, s, threads, perThread)

	res, err := s.ListCollapsedMessages(ctx, store.CollapsedQuery{
		AccountID: acct.ID,
		MailboxID: &inbox.ID,
		Limit:     store.MaxSearchLimit,
	})
	if err != nil {
		t.Fatalf("ListCollapsedMessages: %v", err)
	}

	if len(res.Rows) != threads {
		t.Fatalf("got %d collapsed rows, want %d (one per thread)", len(res.Rows), threads)
	}

	// The survivor of each thread must be its NEWEST member. The corpus dates
	// descend with the id, so the newest member of thread `th` is the FIRST id
	// of its run.
	want := make([]int64, 0, threads)
	for th := range threads {
		want = append(want, ids[th*perThread])
	}
	for i, r := range res.Rows {
		if r.MessageID != want[i] {
			t.Errorf("row %d is message %d, want %d (the thread's newest member)",
				i, r.MessageID, want[i])
		}
	}

	// And the order is the caller's, not the collapse's internal one.
	for i := 1; i < len(res.Rows); i++ {
		if !res.Rows[i-1].Date.After(res.Rows[i].Date) {
			t.Fatalf("row %d (%s) is not newer than row %d (%s); the re-sort did not apply",
				i-1, res.Rows[i-1].Date, i, res.Rows[i].Date)
		}
	}
}

// TestCollapseNeverRepeatsAThreadAcrossPages is the paging invariant, and it is
// the one a naive implementation breaks.
//
// Resuming from the last RETURNED row would re-scan the collapsed-away members
// of that row's thread and emit the thread a second time. The cursor is the last
// SCANNED row precisely to prevent that, and this test is what pins it: it walks
// the whole corpus in small windows and asserts every thread appears exactly
// once across all pages.
func TestCollapseNeverRepeatsAThreadAcrossPages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	const threads, perThread = 60, 4
	acct, inbox, _ := collapseCorpus(t, s, threads, perThread)

	seen := map[int64]int{}
	var cursor *store.SearchCursor
	pages := 0
	for {
		res, err := s.ListCollapsedMessages(ctx, store.CollapsedQuery{
			AccountID: acct.ID,
			MailboxID: &inbox.ID,
			After:     cursor,
			Limit:     10,
			// A window far smaller than the corpus, so the walk really is
			// multi-page rather than one call that happens to see everything.
			Window: 25,
		})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		pages++
		if pages > 100 {
			t.Fatal("the paging walk did not terminate within 100 pages")
		}

		for _, r := range res.Rows {
			// Resolve the row's thread so duplicates are detected by CONVERSATION
			// rather than by message: two different messages of one thread on two
			// pages is exactly the bug this test exists for.
			var threadID int64
			if err := s.Pool().QueryRow(ctx,
				`SELECT thread_id FROM messages WHERE id = $1`, r.MessageID).Scan(&threadID); err != nil {
				t.Fatalf("reading thread of %d: %v", r.MessageID, err)
			}
			seen[threadID]++
		}

		if !res.WindowExhausted {
			break
		}
		if res.NextCursor == nil {
			t.Fatal("the window was exhausted but no cursor was returned; the walk cannot continue")
		}
		cursor = res.NextCursor
	}

	if len(seen) != threads {
		t.Errorf("the walk found %d distinct threads, want %d", len(seen), threads)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("thread %d appeared %d times across pages, want exactly 1", id, n)
		}
	}
	if pages < 2 {
		t.Errorf("the walk finished in %d page(s); the window was meant to force several", pages)
	}
}

// TestCollapseReportsWindowExhaustion pins the distinction the caller pages on:
// a short page because the RESULT SET ended is not the same as a short page
// because the WINDOW filled, and conflating them hides mail.
func TestCollapseReportsWindowExhaustion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	const threads, perThread = 30, 4 // 120 messages
	acct, inbox, _ := collapseCorpus(t, s, threads, perThread)

	t.Run("a window larger than the corpus is not exhausted", func(t *testing.T) {
		res, err := s.ListCollapsedMessages(ctx, store.CollapsedQuery{
			AccountID: acct.ID, MailboxID: &inbox.ID,
			Limit: store.MaxSearchLimit, Window: 500,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.WindowExhausted {
			t.Errorf("WindowExhausted is true after scanning %d of 120 messages", res.Scanned)
		}
		if res.NextCursor != nil {
			t.Error("a cursor was returned for an exhausted result set; the caller would page forever")
		}
		if res.Scanned != threads*perThread {
			t.Errorf("Scanned = %d, want %d (the whole corpus)", res.Scanned, threads*perThread)
		}
	})

	t.Run("a window smaller than the corpus is exhausted and yields a cursor", func(t *testing.T) {
		res, err := s.ListCollapsedMessages(ctx, store.CollapsedQuery{
			AccountID: acct.ID, MailboxID: &inbox.ID,
			Limit: 5, Window: 20,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.WindowExhausted {
			t.Error("WindowExhausted is false after filling a 20-message window")
		}
		if res.NextCursor == nil {
			t.Fatal("no cursor for an exhausted window")
		}
		if res.Scanned != 20 {
			t.Errorf("Scanned = %d, want 20", res.Scanned)
		}
	})
}

// TestCollapseHonorsTheFilters proves the narrowing survives the collapse: a
// predicate applied to the window is applied to the conversation list, and one
// that is not expressible is REFUSED rather than dropped.
func TestCollapseHonorsTheFilters(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	const threads, perThread = 20, 3
	acct, inbox, ids := collapseCorpus(t, s, threads, perThread)

	t.Run("the text path collapses too", func(t *testing.T) {
		res, err := s.ListCollapsedMessages(ctx, store.CollapsedQuery{
			AccountID: acct.ID, Text: needleRare, Limit: store.MaxSearchLimit,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != threads {
			t.Errorf("the text collapse returned %d rows, want %d", len(res.Rows), threads)
		}
	})

	t.Run("the account-wide path collapses too", func(t *testing.T) {
		res, err := s.ListCollapsedMessages(ctx, store.CollapsedQuery{
			AccountID: acct.ID, Limit: store.MaxSearchLimit,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != threads {
			t.Errorf("the account-wide collapse returned %d rows, want %d", len(res.Rows), threads)
		}
	})

	t.Run("unreadOnly narrows the window before the collapse", func(t *testing.T) {
		// Mark every message of the FIRST thread read. Its newest member then
		// leaves the window, so the thread must either vanish or be represented
		// by a still-unread member — never by a read one.
		if _, err := s.Pool().Exec(ctx,
			`UPDATE message_state SET flags = flags | 1 WHERE account_id = $1 AND message_id = ANY($2)`,
			acct.ID, ids[0:perThread]); err != nil {
			t.Fatal(err)
		}
		res, err := s.ListCollapsedMessages(ctx, store.CollapsedQuery{
			AccountID: acct.ID, MailboxID: &inbox.ID,
			UnreadOnly: true, Limit: store.MaxSearchLimit,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != threads-1 {
			t.Errorf("the unread collapse returned %d rows, want %d", len(res.Rows), threads-1)
		}
		for _, r := range res.Rows {
			if r.Flags.Has(store.FlagSeen) {
				t.Errorf("message %d is \\Seen but survived an unreadOnly collapse", r.MessageID)
			}
		}
	})

	t.Run("a keyword without a text condition is refused, not dropped", func(t *testing.T) {
		_, err := s.ListCollapsedMessages(ctx, store.CollapsedQuery{
			AccountID: acct.ID, MailboxID: &inbox.ID, Keyword: "$pinned",
		})
		if err == nil {
			t.Fatal("a keyword filter on the folder path was accepted; it has no predicate to apply, " +
				"so accepting it would return mail the caller filtered out")
		}
		if !strings.Contains(err.Error(), "keyword") {
			t.Errorf("the refusal does not name the condition it refused: %v", err)
		}
	})
}

// TestCollapseScopesByAccount is the guarantee every repertoire method owes:
// account_id is in the WHERE clause, always (search.go's first rule).
func TestCollapseScopesByAccount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	mine, myInbox, _ := collapseCorpus(t, s, 5, 2)
	theirs, _, _ := collapseCorpus(t, s, 5, 2)

	res, err := s.ListCollapsedMessages(ctx, store.CollapsedQuery{
		AccountID: theirs.ID, MailboxID: &myInbox.ID, Limit: store.MaxSearchLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 0 {
		t.Errorf("account %d saw %d rows of account %d's mailbox", theirs.ID, len(res.Rows), mine.ID)
	}
}

// ---------------------------------------------------------------------------
// the plan canary
// ---------------------------------------------------------------------------

// TestCollapsePlanStaysBounded is the regression that matters most here, and
// the one no functional test can catch.
//
// The rejected candidate — DISTINCT ON over the whole folder — is CORRECT. It
// passes every assertion above. It is rejected only because its cost is the
// folder's size rather than the page's: measured at 94.4 ms on a 30,000-message
// account against 6.8 ms for the bounded shape, with a Seq Scan and a sort of
// every row. Someone simplifying this query into the "obvious" one-level form
// would break nothing a test asserts and would put search back over the
// Gmail-class bar on any real mailbox.
//
// So the plan itself is asserted: the window must be served by an INDEX SCAN,
// and the statement must contain no sequential scan of `messages`.
func TestCollapsePlanStaysBounded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Enough rows that a sequential scan is not simply the cheapest honest plan.
	// A handful would let the planner pick a Seq Scan on size alone, which would
	// make the assertion meaningless rather than wrong.
	const threads, perThread = 700, 4 // 2,800 messages
	acct, inbox, _ := collapseCorpus(t, s, threads, perThread)
	if _, err := s.Pool().Exec(ctx, `ANALYZE messages; ANALYZE message_state`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}

	// The statement ListCollapsedMessages issues for the folder path, verbatim
	// except for the parameters being inlined by EXPLAIN's own binding.
	const q = `
		WITH w AS (
			SELECT m.id, m.date, m.subject, m.from_addr, m.preview, m.thread_id,
			       ms.mailbox_id, ms.flags, ms.keywords
			  FROM messages m
			  JOIN message_state ms ON ms.message_id = m.id
			 WHERE m.account_id = $1 AND ms.deleted_at IS NULL AND ms.mailbox_id = $2
			 ORDER BY m.date DESC, m.id DESC
			 LIMIT $3
		), d AS (
			SELECT DISTINCT ON (w.thread_id)
			       w.id, w.date, w.subject, w.from_addr, w.preview,
			       w.mailbox_id, w.flags, w.keywords
			  FROM w
			 ORDER BY w.thread_id, w.date DESC, w.id DESC
		)
		SELECT d.id, d.date, d.subject, d.from_addr, d.preview,
		       d.mailbox_id, d.flags, d.keywords,
		       (SELECT count(*) FROM w) AS scanned,
		       (SELECT w2.date FROM w w2 ORDER BY w2.date, w2.id LIMIT 1) AS edge_date,
		       (SELECT w2.id   FROM w w2 ORDER BY w2.date, w2.id LIMIT 1) AS edge_id
		  FROM d
		 ORDER BY d.date DESC, d.id DESC
		 LIMIT $4`

	plan := explain(t, s, q, acct.ID, inbox.ID, store.CollapseWindow, 50)

	if !strings.Contains(plan, "messages_acct_date") {
		t.Errorf("the collapse window is NOT served by messages_acct_date.\n"+
			"That index is what makes the window a bounded index walk instead of a scan; "+
			"without it the shape degrades into the 94 ms candidate this design rejected.\nPlan:\n%s", plan)
	}
	if strings.Contains(plan, "Seq Scan on messages") {
		t.Errorf("the collapse plan sequentially scans `messages`.\n"+
			"Its cost is then the FOLDER's size rather than the window's, which is the "+
			"unbounded shape the repertoire exists to make unrepresentable (measured 94.4 ms "+
			"at 30k messages against 6.8 ms bounded).\nPlan:\n%s", plan)
	}

	// The collapse must not reach messages_acct_thread either. That index is
	// partial and deliberately unreachable from an account_id predicate alone
	// (migration 0004): a collapse that started using it would mean the planner
	// found a new route into it, which is the regression threads_plan_test.go
	// caught twice while 0004 was being written.
	if strings.Contains(plan, "messages_acct_thread") {
		t.Errorf("the collapse reached messages_acct_thread.\n"+
			"It groups by thread_id but never FILTERS on one, so a plan that uses that "+
			"partial index has found a route migration 0004 spent its length closing.\nPlan:\n%s", plan)
	}
}
