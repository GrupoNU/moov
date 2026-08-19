package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// Migration 0004 (threading) against a real PostgreSQL 17.
//
// Two properties are worth proving at the SQL level rather than through the Go
// API, because both are invisible to it: that the schema objects the design
// depends on actually exist, and that the BACKFILL — which only ever runs once,
// on data written before this migration — produces the same grouping the
// runtime assignment produces on new arrivals.

// The column, its NOT NULL, and the index that makes both thread reads cheap.
func TestThreadingSchemaExists(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	t.Run("thread_id is NOT NULL", func(t *testing.T) {
		var nullable string
		err := db.QueryRowContext(ctx, `
			SELECT is_nullable FROM information_schema.columns
			 WHERE table_schema = 'public' AND table_name = 'messages'
			   AND column_name = 'thread_id'`).Scan(&nullable)
		if err == sql.ErrNoRows {
			t.Fatal("messages.thread_id does not exist; migration 0004 did not take effect")
		}
		if err != nil {
			t.Fatalf("querying information_schema: %v", err)
		}
		if nullable != "NO" {
			t.Errorf("messages.thread_id is nullable; every message must have a thread "+
				"(got is_nullable=%q)", nullable)
		}
	})

	t.Run("the composite thread index exists", func(t *testing.T) {
		var indexdef string
		err := db.QueryRowContext(ctx, `
			SELECT indexdef FROM pg_indexes
			 WHERE schemaname = 'public' AND indexname = 'messages_acct_thread'`).Scan(&indexdef)
		if err == sql.ErrNoRows {
			t.Fatal("messages_acct_thread does not exist; Thread/get and the exact " +
				"mailbox counts would both fall back to a scan")
		}
		if err != nil {
			t.Fatalf("querying pg_indexes: %v", err)
		}
		for _, want := range []string{"account_id", "thread_id", "date"} {
			if !containsWord(indexdef, want) {
				t.Errorf("messages_acct_thread does not include %s: %s", want, indexdef)
			}
		}

		// THREAD_ID MUST LEAD. An index leading with account_id is a
		// general-purpose "every message of this account" index, and the planner
		// will happily use it for a full-text search and then FILTER on tsv -
		// which is exactly the collapse the composite GIN exists to prevent
		// (S3 S5.2, up to 6,600x). The first draft of this index led with
		// account_id and broke TestCompositeGINIndexIsUsableForSearch; this
		// assertion is what stops that from coming back.
		lead := indexOf(indexdef, "(")
		if lead < 0 || indexOf(indexdef[lead:], "thread_id") < 0 ||
			indexOf(indexdef[lead:], "thread_id") > indexOf(indexdef[lead:], "account_id") {
			t.Errorf("messages_acct_thread must lead with thread_id, not account_id, "+
				"or it competes with the composite GIN for every search query: %s", indexdef)
		}
	})

	t.Run("the subject key table exists", func(t *testing.T) {
		var exists bool
		err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				 WHERE table_schema = 'public' AND table_name = 'thread_subject_keys')`,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("querying information_schema: %v", err)
		}
		if !exists {
			t.Error("thread_subject_keys does not exist; the subject fallback has nowhere to look")
		}
	})

	t.Run("the thread_id default trigger exists", func(t *testing.T) {
		var exists bool
		err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_trigger
				 WHERE tgname = 'messages_thread_id_default' AND NOT tgisinternal)`,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("querying pg_trigger: %v", err)
		}
		if !exists {
			t.Error("messages_thread_id_default is missing; a message would be inserted " +
				"with no thread and the NOT NULL would reject it")
		}
	})
}

func containsWord(haystack, needle string) bool {
	return len(haystack) > 0 && len(needle) > 0 &&
		(indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// THE BACKFILL TEST.
//
// Migration 0004's backfill runs exactly once, over data that existed before
// threading did, and there is no way to exercise it through the Go API — by the
// time the API is usable the migration has already run. So this test builds the
// pre-0004 state by hand: it inserts a reply chain with every message in its own
// thread (which is what a pre-0004 row set looks like after the column is added
// and step 1 has run), then executes the backfill's own statements and asserts
// the grouping.
//
// The statements below are the migration's steps 2 and 3 VERBATIM. If they
// drift from the migration, this test stops proving anything — which is why
// they are quoted here in full rather than factored into a helper the migration
// does not use.
func TestMigration0004BackfillGroupsExistingMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	acct := newAccount(t, s)
	mb := threadMailbox(t, s, acct.ID, "INBOX")

	// A reply chain inserted WITHOUT threading: exactly the pre-0004 shape,
	// every message its own thread.
	clock := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	type spec struct {
		msgID     string
		inReplyTo string
		refs      []string
	}
	specs := []spec{
		{msgID: "bf-root@test"},
		{msgID: "bf-a@test", inReplyTo: "bf-root@test", refs: []string{"bf-root@test"}},
		{msgID: "bf-b@test", inReplyTo: "bf-a@test", refs: []string{"bf-root@test", "bf-a@test"}},
		// A message naming only its immediate parent, so step 3's flattening is
		// what has to resolve it rather than step 2.
		{msgID: "bf-c@test", inReplyTo: "bf-b@test", refs: []string{"bf-b@test"}},
		// An unrelated message, which must stay in its own thread.
		{msgID: "bf-other@test"},
	}

	ids := make([]int64, 0, len(specs))
	for i, sp := range specs {
		clock = clock.Add(time.Minute)
		got, err := s.InsertMessages(ctx, []store.NewMessage{{
			Message: store.Message{
				AccountID:     acct.ID,
				RawSHA256:     seedBlob(t, s, "backfill-"+sp.msgID),
				RawSize:       100,
				MessageID:     sp.msgID,
				InReplyTo:     sp.inReplyTo,
				ReferencesIDs: sp.refs,
				Subject:       "Backfill chain",
				Date:          clock,
			},
			State: store.MessageState{
				AccountID: acct.ID, MailboxID: mb.ID,
				UID: int64(i + 1), UIDValidity: 1,
			},
		}})
		if err != nil {
			t.Fatalf("InsertMessages: %v", err)
		}
		ids = append(ids, got[0])
	}

	// Every message starts as its own thread (the trigger), which is the state
	// migration step 1 establishes.
	for _, id := range ids {
		m, err := s.GetMessage(ctx, id)
		if err != nil {
			t.Fatalf("GetMessage: %v", err)
		}
		if m.ThreadID != id {
			t.Fatalf("message %d starts in thread %d, want its own id", id, m.ThreadID)
		}
	}

	// ---- migration 0004, step 2 (verbatim) --------------------------------
	if _, err := s.Pool().Exec(ctx, `
		WITH refs AS (
		    SELECT m.id AS child_id,
		           unnest(
		               CASE WHEN m.in_reply_to IS NULL OR m.in_reply_to = ''
		                    THEN m.references_ids
		                    ELSE m.references_ids || ARRAY[m.in_reply_to]
		               END
		           ) AS ref,
		           m.account_id
		      FROM messages m
		), parents AS (
		    SELECT r.child_id, min(p.id) AS parent_id
		      FROM refs r
		      JOIN messages p
		        ON p.account_id = r.account_id
		       AND p.message_id = r.ref
		     WHERE p.id < r.child_id
		     GROUP BY r.child_id
		)
		UPDATE messages m
		   SET thread_id = p.parent_id
		  FROM parents p
		 WHERE m.id = p.child_id`); err != nil {
		t.Fatalf("backfill step 2: %v", err)
	}

	// ---- migration 0004, step 3 (verbatim, three passes) -------------------
	for pass := 0; pass < 3; pass++ {
		if _, err := s.Pool().Exec(ctx, `
			UPDATE messages m
			   SET thread_id = t.thread_id
			  FROM messages t
			 WHERE m.thread_id = t.id
			   AND t.thread_id <> t.id
			   AND t.thread_id < m.thread_id`); err != nil {
			t.Fatalf("backfill step 3 pass %d: %v", pass+1, err)
		}
	}

	// The four chained messages are now one thread, keyed by the oldest.
	root := ids[0]
	for _, id := range ids[:4] {
		m, err := s.GetMessage(ctx, id)
		if err != nil {
			t.Fatalf("GetMessage: %v", err)
		}
		if m.ThreadID != root {
			t.Errorf("after the backfill, message %d is in thread %d, want %d", id, m.ThreadID, root)
		}
	}

	// The unrelated message stayed alone.
	other := ids[4]
	m, err := s.GetMessage(ctx, other)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if m.ThreadID != other {
		t.Errorf("an unrelated message was absorbed into thread %d", m.ThreadID)
	}

	// AND THE POINT OF THE WHOLE TEST: the runtime assignment agrees with the
	// backfill. Re-threading the same data through the Go path must not move
	// anything, which is what makes a partially-backfilled installation
	// converge rather than flip-flop.
	changed, _, err := s.ReindexThreads(ctx, acct.ID, 100, 0)
	if err != nil {
		t.Fatalf("ReindexThreads: %v", err)
	}
	if changed != 0 {
		t.Errorf("the runtime assignment disagreed with the migration backfill on %d messages; "+
			"a partially backfilled installation would never converge", changed)
	}
}

// The backfill must not group across accounts, exactly as the runtime path must
// not. Two tenants storing the same Message-ID is routine (a mailing list), and
// the migration's join is the one place where forgetting account_id would be
// invisible until a customer saw another customer's mail in a thread.
func TestMigration0004BackfillIsAccountScoped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	a := newAccount(t, s)
	b := newAccount(t, s)
	mbA := threadMailbox(t, s, a.ID, "INBOX")
	mbB := threadMailbox(t, s, b.ID, "INBOX")

	clock := time.Date(2026, 8, 13, 11, 0, 0, 0, time.UTC)
	insert := func(acct store.Account, mb store.Mailbox, uid int64, msgID, inReplyTo string, refs []string) int64 {
		clock = clock.Add(time.Minute)
		got, err := s.InsertMessages(ctx, []store.NewMessage{{
			Message: store.Message{
				AccountID: acct.ID,
				RawSHA256: seedBlob(t, s, "scoped-"+msgID+"-"+acct.Email),
				RawSize:   100, MessageID: msgID, InReplyTo: inReplyTo,
				ReferencesIDs: refs, Subject: "Scoped", Date: clock,
			},
			State: store.MessageState{
				AccountID: acct.ID, MailboxID: mb.ID, UID: uid, UIDValidity: 1,
			},
		}})
		if err != nil {
			t.Fatalf("InsertMessages: %v", err)
		}
		return got[0]
	}

	// Account A holds the root. Account B holds a reply naming that same
	// Message-ID but has no copy of the root itself.
	rootA := insert(a, mbA, 1, "scoped-root@test", "", nil)
	replyB := insert(b, mbB, 1, "scoped-reply@test", "scoped-root@test", []string{"scoped-root@test"})

	if _, err := s.Pool().Exec(ctx, `
		WITH refs AS (
		    SELECT m.id AS child_id,
		           unnest(
		               CASE WHEN m.in_reply_to IS NULL OR m.in_reply_to = ''
		                    THEN m.references_ids
		                    ELSE m.references_ids || ARRAY[m.in_reply_to]
		               END
		           ) AS ref,
		           m.account_id
		      FROM messages m
		), parents AS (
		    SELECT r.child_id, min(p.id) AS parent_id
		      FROM refs r
		      JOIN messages p
		        ON p.account_id = r.account_id
		       AND p.message_id = r.ref
		     WHERE p.id < r.child_id
		     GROUP BY r.child_id
		)
		UPDATE messages m
		   SET thread_id = p.parent_id
		  FROM parents p
		 WHERE m.id = p.child_id`); err != nil {
		t.Fatalf("backfill step 2: %v", err)
	}

	m, err := s.GetMessage(ctx, replyB)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if m.ThreadID == rootA {
		t.Fatal("the backfill joined a reply to another ACCOUNT's message; " +
			"the join lost its account_id predicate")
	}
	if m.ThreadID != replyB {
		t.Errorf("the reply is in thread %d, want its own id %d (its root is in another account)",
			m.ThreadID, replyB)
	}
}
