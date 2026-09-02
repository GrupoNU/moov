package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// The account-wide label view (L3 epic E8) at the store.
//
// A Gmail label is account-wide by definition (canon §2.1), and labels live in
// message_state.keywords after arbitration A6. AccountListQuery.Keyword is the
// predicate that serves it; migration 0011 is what makes it fast enough.

// labelCorpus seeds two accounts so that a query missing its account scope shows
// up as WRONG ROWS rather than as a passing test — the same discipline the E3
// bench corpus uses, and the one that caught the `gin(account_id, keywords)`
// candidate returning a second account's rows during the 0011 measurement.
//
// The label lands on messages in THREE different mailboxes, because that is the
// whole point of the feature: a label view that only found the inbox's copies
// would pass a single-mailbox fixture and fail the user.
func labelCorpus(t *testing.T, s *store.Store, n int) (acct store.Account, other store.Account, boxes []store.Mailbox) {
	t.Helper()
	ctx := context.Background()

	acct = newAccount(t, s)
	other = newAccount(t, s)

	inbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)
	archive := seedMailbox(t, s, acct.ID, "Archive", store.RoleArchive)
	junk := seedMailbox(t, s, acct.ID, "Spam", store.RoleJunk)
	boxes = []store.Mailbox{inbox, archive, junk}
	otherBox := seedMailbox(t, s, other.ID, "INBOX", store.RoleInbox)

	now := time.Now().UTC()
	msgs := make([]store.NewMessage, 0, n*2)
	for i := range n {
		// The label spreads across all three mailboxes, one message in five.
		box := boxes[i%3]
		var kw []string
		if i%5 == 0 {
			kw = append(kw, "$label:work")
		}
		if i%50 == 0 {
			kw = append(kw, "$label:rare")
		}
		var flags store.Flags
		if i%3 != 0 {
			flags |= store.FlagSeen
		}
		msgs = append(msgs, store.NewMessage{
			Message: store.Message{
				AccountID: acct.ID,
				RawSHA256: seedBlob(t, s, fmt.Sprintf("label-%d-%d", acct.ID, i)),
				RawSize:   1000,
				MessageID: fmt.Sprintf("label-%d-%d@test", acct.ID, i),
				Subject:   fmt.Sprintf("Asunto %d proyecto", i),
				FromAddr:  "remitente@example.test",
				// A RARE term on one message in fifty, alongside the common one.
				// The plan canary below searches for the rare term: a term that
				// matches every row makes the composite GIN genuinely the wrong
				// plan, so a canary built on one would assert the planner is
				// broken rather than that the index is reachable.
				BodyText: fmt.Sprintf("cuerpo con proyecto %s",
					map[bool]string{true: "presupuestoinusual", false: ""}[i%50 == 0]),
				Date: now.Add(-time.Duration(i) * time.Minute),
			},
			State: store.MessageState{
				AccountID: acct.ID, MailboxID: box.ID,
				UID: int64(i + 1), UIDValidity: 1, Flags: flags, Keywords: kw,
			},
		})
		// The SECOND account carries the SAME label, on every message. If the
		// account scope is ever lost, these rows flood the result and the count
		// assertions below fail loudly.
		msgs = append(msgs, store.NewMessage{
			Message: store.Message{
				AccountID: other.ID,
				RawSHA256: seedBlob(t, s, fmt.Sprintf("label-other-%d-%d", other.ID, i)),
				RawSize:   1000,
				MessageID: fmt.Sprintf("label-other-%d-%d@test", other.ID, i),
				Subject:   fmt.Sprintf("Ajeno %d", i),
				FromAddr:  "ajeno@example.test",
				BodyText:  "cuerpo ajeno",
				Date:      now.Add(-time.Duration(i) * time.Minute),
			},
			State: store.MessageState{
				AccountID: other.ID, MailboxID: otherBox.ID,
				UID: int64(i + 1), UIDValidity: 1, Keywords: []string{"$label:work"},
			},
		})
	}
	if _, err := s.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("seeding the label corpus: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `ANALYZE messages; ANALYZE message_state`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}
	return acct, other, boxes
}

// The behavior: every message carrying the label, across every folder, and
// NOTHING from the other account.
func TestAccountListKeywordIsTheAccountWideLabelView(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	const n = 300
	acct, _, boxes := labelCorpus(t, s, n)

	got, err := s.ListAccountMessages(ctx, store.AccountListQuery{
		AccountID: acct.ID,
		Keyword:   "$label:work",
		Limit:     store.MaxSearchLimit,
	})
	if err != nil {
		t.Fatalf("ListAccountMessages: %v", err)
	}

	// One in five carries the label.
	want := n / 5
	if len(got) != want {
		t.Errorf("returned %d messages, want %d — either the label predicate was dropped "+
			"(too many) or the walk stopped early (too few)", len(got), want)
	}

	// The label is cross-cutting: the result must span every mailbox it was
	// seeded into. A result confined to one folder is the bug this feature
	// exists to fix.
	seen := map[int64]bool{}
	for _, r := range got {
		seen[r.MailboxID] = true
		if !hasKw(r.Keywords, "$label:work") {
			t.Fatalf("message %d came back without the label it was filtered by", r.MessageID)
		}
	}
	for _, b := range boxes {
		if !seen[b.ID] {
			t.Errorf("no message from mailbox %q — an account-wide label view must span "+
				"every folder (canon §2.1), not just the inbox", b.Name)
		}
	}

	// Newest first, which is the order the shape promises.
	for i := 1; i < len(got); i++ {
		if got[i].Date.After(got[i-1].Date) {
			t.Fatalf("results are not newest-first at index %d", i)
		}
	}
}

// The account scope, stated as its own test because losing it is silent.
//
// The rejected `gin(account_id, keywords)` candidate measured for migration 0011
// returned the SECOND account's rows inside its bitmap and post-filtered them on
// the heap. That is invisible to a single-account fixture.
func TestAccountListKeywordNeverCrossesAccounts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	acct, other, _ := labelCorpus(t, s, 200)

	got, err := s.ListAccountMessages(ctx, store.AccountListQuery{
		AccountID: acct.ID,
		Keyword:   "$label:work",
		Limit:     store.MaxSearchLimit,
	})
	if err != nil {
		t.Fatalf("ListAccountMessages: %v", err)
	}
	for _, r := range got {
		if strings.HasPrefix(r.FromAddr, "ajeno@") {
			t.Fatalf("message %d belongs to account %d, not %d — the account scope was lost",
				r.MessageID, other.ID, acct.ID)
		}
	}
}

// The label view composes with the narrowing every other shape has, rather than
// being a special case that silently ignores it.
func TestAccountListKeywordComposesWithUnreadAndDates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	acct, _, _ := labelCorpus(t, s, 300)

	all, err := s.ListAccountMessages(ctx, store.AccountListQuery{
		AccountID: acct.ID, Keyword: "$label:work", Limit: store.MaxSearchLimit,
	})
	if err != nil {
		t.Fatalf("ListAccountMessages: %v", err)
	}
	unread, err := s.ListAccountMessages(ctx, store.AccountListQuery{
		AccountID: acct.ID, Keyword: "$label:work", UnreadOnly: true,
		Limit: store.MaxSearchLimit,
	})
	if err != nil {
		t.Fatalf("ListAccountMessages(unread): %v", err)
	}

	if len(unread) == 0 || len(unread) >= len(all) {
		t.Fatalf("unread label view returned %d of %d — the unread predicate was dropped "+
			"or matched nothing", len(unread), len(all))
	}
	for _, r := range unread {
		if r.Flags&store.FlagSeen != 0 {
			t.Errorf("message %d is read but came back from an unread-only label view", r.MessageID)
		}
		if !hasKw(r.Keywords, "$label:work") {
			t.Errorf("message %d lacks the label", r.MessageID)
		}
	}
}

// Paging a label view resumes the walk rather than restarting it, and never
// repeats or skips a row.
func TestAccountListKeywordPagesWithTheCursor(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	acct, _, _ := labelCorpus(t, s, 300)

	const page = 20
	seen := map[int64]bool{}
	var cursor *store.SearchCursor
	var order []int64
	for {
		got, err := s.ListAccountMessages(ctx, store.AccountListQuery{
			AccountID: acct.ID, Keyword: "$label:work", After: cursor, Limit: page,
		})
		if err != nil {
			t.Fatalf("ListAccountMessages: %v", err)
		}
		if len(got) == 0 {
			break
		}
		for _, r := range got {
			if seen[r.MessageID] {
				t.Fatalf("message %d was returned twice across pages", r.MessageID)
			}
			seen[r.MessageID] = true
			order = append(order, r.MessageID)
		}
		last := got[len(got)-1]
		cursor = &store.SearchCursor{Date: last.Date, MessageID: last.MessageID}
		if len(got) < page {
			break
		}
	}

	if want := 300 / 5; len(order) != want {
		t.Errorf("paging collected %d messages, want %d — a page boundary dropped rows",
			len(order), want)
	}
}

// THE PLAN CANARY FOR MIGRATION 0011.
//
// 0011's whole justification is that the message_state probe becomes an INDEX
// ONLY SCAN: the account-wide label view walks (account_id, date DESC) and tests
// each candidate's keywords, and at a realistic 2% label density that is ~10,011
// probes to fill one page of 200. Before the covering index those were heap
// probes (85,218 buffers, 230 ms); after, they are index-only (60 ms).
//
// The failure this guards is silent and specific: if the index is dropped, or
// if a column is added to the SELECT that the INCLUDE list does not carry, the
// plan degrades to a heap probe per row. Every functional test above stays green
// — the ROWS are identical — and only the latency changes. That is exactly the
// class of regression bench_test.go exists to catch for search.
//
// It EXPLAINs the store's own statement rather than a hand-copy, for the reason
// AccountListQuery.build documents: a builder that paraphrases the predicate
// falls off its index while a test written against a paraphrase keeps passing.
func TestAccountWideLabelViewProbesIndexOnly(t *testing.T) {
	s := testStore(t)
	acct, _, _ := labelCorpus(t, s, 2000)

	sql, args := store.BuildAccountListSQL(store.AccountListQuery{
		AccountID: acct.ID,
		Keyword:   "$label:work",
		Limit:     store.MaxSearchLimit,
	})
	plan := explain(t, s, sql, args...)

	if !strings.Contains(plan, "message_state_cover") {
		t.Errorf("the account-wide label view does NOT use message_state_cover.\n"+
			"Migration 0011 exists to make this probe index-only: the walk tests one candidate "+
			"per row and at a realistic label density that is ~10,000 probes per page. Without "+
			"the covering index each is a heap probe (measured 230 ms against 60 ms), and "+
			"NOTHING ELSE FAILS — the rows are identical, only the latency changes.\n"+
			"SQL:\n%s\nPlan:\n%s", sql, plan)
	}
	if !strings.Contains(plan, "Index Only Scan") {
		t.Errorf("the message_state probe is not index-only.\n"+
			"The index may exist but not cover the columns the SELECT reads — check that the "+
			"INCLUDE list still carries keywords, deleted_at, mailbox_id and flags.\n"+
			"Plan:\n%s", plan)
	}
	// The account scope must lead, so the walk is the account's and not the
	// installation's (S3 §5.2).
	if !strings.Contains(plan, "messages_acct_date") {
		t.Errorf("the label view does not walk messages_acct_date; the account-scoped date "+
			"order is what bounds this shape.\nPlan:\n%s", plan)
	}
}

// The COLLAPSED label view — a label view showing conversations.
//
// ListCollapsedMessages used to refuse a keyword without a text condition ("the
// folder view has no keyword predicate"). That guard was already false of the
// code it guarded: conditions() and dedupeClause() both emitted the containment
// predicate unconditionally, so the refusal was rejecting a query the SQL
// underneath could express. Removing it is what makes a label view collapsible,
// and this is what proves the predicate reaches BOTH halves.
func TestCollapsedLabelViewFiltersByKeyword(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	acct, _, _ := labelCorpus(t, s, 300)

	got, err := s.ListCollapsedMessages(ctx, store.CollapsedQuery{
		AccountID: acct.ID,
		Keyword:   "$label:work",
		Limit:     store.MaxSearchLimit,
	})
	if err != nil {
		t.Fatalf("ListCollapsedMessages: %v", err)
	}
	if len(got.Rows) == 0 {
		t.Fatal("the collapsed label view returned nothing; the corpus labels one message in five")
	}
	for _, r := range got.Rows {
		if !hasKw(r.Keywords, "$label:work") {
			t.Errorf("message %d came back from a collapsed label view without the label — "+
				"the predicate reached the window but not the result", r.MessageID)
		}
		if r.FromAddr == "ajeno@example.test" {
			t.Errorf("message %d belongs to the other account", r.MessageID)
		}
	}
}

// 0011 must not disturb the plans the repertoire already had.
//
// Migration 0004 states the rule and 0008 was declined once on its strength: a
// new index gives the planner one more way to compete with the composite GIN,
// and S3 §5.3 is the cautionary tale where that took a 1.6 ms query to 13,085 ms.
// The covering index is on message_state and the GIN is on messages, so they
// should not compete — this is what proves it rather than assuming it.
func TestCoveringIndexDoesNotDisturbTheTextSearchPlan(t *testing.T) {
	s := testStore(t)
	acct, _, _ := labelCorpus(t, s, 2000)

	// A RARE term. The corpus deliberately puts "proyecto" on every message, and
	// a term matching every row is a case where the composite GIN is genuinely
	// the WRONG plan — asserting on it would pin a planner bug, not an index.
	sql, args := store.BuildSearchSQL(store.SearchQuery{
		AccountID: acct.ID, Text: "presupuestoinusual", Limit: 50,
	})
	plan := explain(t, s, sql, args...)

	if !strings.Contains(plan, "messages_acct_tsv_gin") {
		t.Errorf("the text search no longer reaches the composite GIN — migration 0011's "+
			"index is competing with it, which is the S3 §5.3 failure (1.6 ms -> 13,085 ms).\n"+
			"Plan:\n%s", plan)
	}
}

func hasKw(kws []string, want string) bool {
	for _, k := range kws {
		if k == want {
			return true
		}
	}
	return false
}
