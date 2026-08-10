package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// Search correctness, one planted needle per shape.
//
// This mirrors spike S3's own discipline: it ran a needle gate BEFORE any
// timing, on the principle that latency on wrong results is worthless. The
// corpus here is small — these are correctness tests, not benchmarks; the
// latency canary lives in bench_test.go.
//
// The needles are deliberately absurd words ("zanzibarita") so a match cannot
// be a coincidence of the generated filler text, exactly as S3 did.

const (
	needleRare     = "zanzibarita"
	needleInvoice  = "INV-2024-0857"
	needlePhraseA  = "quetzal ferroviario nocturno"
	needleAccented = "acción"
	needlePrefix   = "facturacion"
)

// searchCorpus seeds one account with a small corpus containing a planted
// needle for every search shape, and returns the account and its mailboxes.
type searchCorpus struct {
	Account store.Account
	Inbox   store.Mailbox
	Archive store.Mailbox
	// IDs of the messages carrying each needle.
	RareID     int64
	InvoiceID  int64
	PhraseID   int64
	AccentedID int64
	UnreadID   int64
	KeywordID  int64
}

func seedSearchCorpus(t *testing.T, s *store.Store) searchCorpus {
	t.Helper()
	ctx := context.Background()

	acct := newAccount(t, s)
	inbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)
	archive := seedMailbox(t, s, acct.ID, "Archive", store.RoleArchive)

	now := time.Now().UTC()
	var msgs []store.NewMessage

	add := func(mbox store.Mailbox, uid int64, subject, from, body string, flags store.Flags, keywords []string, age time.Duration) {
		msgs = append(msgs, store.NewMessage{
			Message: store.Message{
				AccountID: acct.ID,
				RawSHA256: seedBlob(t, s, fmt.Sprintf("corpus-%d-%d-%s", acct.ID, uid, subject)),
				RawSize:   int64(len(body) + 100),
				Subject:   subject,
				FromAddr:  from,
				ToAddrs:   "destinatario@example.test",
				BodyText:  body,
				Preview:   truncate(body, 60),
				Date:      now.Add(-age),
			},
			State: store.MessageState{
				AccountID: acct.ID, MailboxID: mbox.ID,
				UID: uid, UIDValidity: 1, Flags: flags,
				Keywords: keywords, ModSeqSeen: uid,
			},
		})
	}

	// Filler: common words shared by many messages, so "common word" shapes
	// have something to be selective against.
	for i := range 40 {
		add(inbox, int64(100+i),
			fmt.Sprintf("Reunion semanal %d", i),
			fmt.Sprintf("colega%d@example.test", i%7),
			"agenda de la reunion con los temas pendientes del equipo",
			store.FlagSeen, nil, time.Duration(i)*time.Hour)
	}

	// Shape #2 — rare word.
	add(inbox, 1, "Informe zanzibarita", "rara@example.test",
		"documento con el termino zanzibarita en el cuerpo", store.FlagSeen, nil, time.Hour)

	// Unique invoice code: exact-token retrieval.
	add(inbox, 2, "Comprobante "+needleInvoice, "facturacion@example.test",
		"adjuntamos el comprobante "+needleInvoice+" correspondiente", store.FlagSeen, nil, 2*time.Hour)

	// Shape #4 — phrase.
	add(inbox, 3, "Cronica", "poeta@example.test",
		"el "+needlePhraseA+" cruzo la llanura", store.FlagSeen, nil, 3*time.Hour)

	// Accent handling through unaccent().
	add(inbox, 4, "Plan de "+needleAccented, "plan@example.test",
		"detalle del plan de "+needleAccented+" inmediata", store.FlagSeen, nil, 4*time.Hour)

	// Shape #7 — unread only.
	add(inbox, 5, "Pendiente zanzibarita urgente", "pendiente@example.test",
		"este mensaje sigue sin leer y menciona zanzibarita", 0, nil, 5*time.Hour)

	// Keyword/label membership (A6).
	add(inbox, 6, "Etiquetado zanzibarita", "etiqueta@example.test",
		"mensaje con etiqueta y zanzibarita", store.FlagSeen, []string{"$MoovL3"}, 6*time.Hour)

	// Shape #5 — prefix: a longer word sharing a stem-like prefix.
	add(archive, 7, "Area de "+needlePrefix, "admin@example.test",
		"el area de "+needlePrefix+" respondio", store.FlagSeen, nil, 7*time.Hour)

	// Shape #6 — mailbox + recency: an old message that a 90-day filter drops.
	add(archive, 8, "Historico zanzibarita", "viejo@example.test",
		"mensaje antiguo con zanzibarita", store.FlagSeen, nil, 400*24*time.Hour)

	ids, err := s.InsertMessages(ctx, msgs)
	if err != nil {
		t.Fatalf("seeding search corpus: %v", err)
	}

	// The needle messages are the ones appended after the 40 filler rows.
	const filler = 40
	return searchCorpus{
		Account:    acct,
		Inbox:      inbox,
		Archive:    archive,
		RareID:     ids[filler],
		InvoiceID:  ids[filler+1],
		PhraseID:   ids[filler+2],
		AccentedID: ids[filler+3],
		UnreadID:   ids[filler+4],
		KeywordID:  ids[filler+5],
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func containsID(results []store.SearchResult, id int64) bool {
	for _, r := range results {
		if r.MessageID == id {
			return true
		}
	}
	return false
}

// Shape #2: a rare word must be found, and only in the account that has it.
func TestSearchRareWord(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := seedSearchCorpus(t, s)

	got, err := s.Search(ctx, store.SearchQuery{AccountID: c.Account.ID, Text: needleRare})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Four messages carry the needle: rare, unread, keyword, historic.
	if len(got) != 4 {
		t.Errorf("found %d matches for %q, want 4", len(got), needleRare)
	}
	if !containsID(got, c.RareID) {
		t.Errorf("the planted rare-word needle %d was not returned", c.RareID)
	}

	// Results must be date-descending: that ordering is what lets the planner
	// stop at LIMIT, and it is what the UI renders.
	for i := 1; i < len(got); i++ {
		if got[i].Date.After(got[i-1].Date) {
			t.Errorf("results are not date-descending: %v then %v", got[i-1].Date, got[i].Date)
		}
	}
}

// Account scoping is the property the composite GIN index encodes and the
// property a multi-tenant mail store cannot get wrong.
func TestSearchIsAccountScoped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := seedSearchCorpus(t, s)
	other := newAccount(t, s)

	got, err := s.Search(ctx, store.SearchQuery{AccountID: other.ID, Text: needleRare})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("another account's search returned %d rows for %q; account scoping is broken",
			len(got), needleRare)
	}
	_ = c
}

// An exact token with punctuation — an invoice code — must be retrievable.
func TestSearchExactToken(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := seedSearchCorpus(t, s)

	got, err := s.Search(ctx, store.SearchQuery{AccountID: c.Account.ID, Text: needleInvoice})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !containsID(got, c.InvoiceID) {
		t.Errorf("invoice code %q not found; got %d results", needleInvoice, len(got))
	}
}

// Shape #4: a quoted phrase must match the sequence, not merely the words.
func TestSearchPhrase(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := seedSearchCorpus(t, s)

	got, err := s.Search(ctx, store.SearchQuery{
		AccountID: c.Account.ID,
		Text:      `"` + needlePhraseA + `"`,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !containsID(got, c.PhraseID) {
		t.Errorf("phrase %q not found; got %d results", needlePhraseA, len(got))
	}

	// The same words out of order must NOT match as a phrase.
	scrambled, err := s.Search(ctx, store.SearchQuery{
		AccountID: c.Account.ID,
		Text:      `"nocturno ferroviario quetzal"`,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(scrambled) != 0 {
		t.Errorf("a scrambled phrase matched %d messages; phrase search is not ordered", len(scrambled))
	}
}

// unaccent(): "accion" must find "acción" and vice versa. This is why
// migration 0001 installs the extension and 0002 wraps it as IMMUTABLE.
func TestSearchIsAccentInsensitive(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := seedSearchCorpus(t, s)

	for _, term := range []string{"accion", "acción", "ACCION"} {
		t.Run(term, func(t *testing.T) {
			got, err := s.Search(ctx, store.SearchQuery{AccountID: c.Account.ID, Text: term})
			if err != nil {
				t.Fatalf("Search(%q): %v", term, err)
			}
			if !containsID(got, c.AccentedID) {
				t.Errorf("searching %q did not find the accented message %d", term, c.AccentedID)
			}
		})
	}
}

// Shape #5: search-as-you-type. Also the recall mechanism the 'simple'
// configuration depends on, since it does no stemming.
func TestSearchPrefix(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := seedSearchCorpus(t, s)

	got, err := s.SearchPrefix(ctx, store.SearchQuery{AccountID: c.Account.ID, Text: "factura"})
	if err != nil {
		t.Fatalf("SearchPrefix: %v", err)
	}
	if len(got) == 0 {
		t.Errorf("prefix %q found nothing, but %q and a from-address both start with it",
			"factura", needlePrefix)
	}

	// Malformed input must return no results rather than a database error:
	// this runs on every keystroke.
	for _, bad := range []string{"(", "&&&", `"unbalanced`, "!", ""} {
		t.Run("malformed_"+bad, func(t *testing.T) {
			if _, err := s.SearchPrefix(ctx, store.SearchQuery{AccountID: c.Account.ID, Text: bad}); err != nil {
				t.Errorf("SearchPrefix(%q) errored: %v — search-as-you-type must degrade, not fail", bad, err)
			}
		})
	}
}

// Shape #6 (mailbox + recency) and the mailbox filter.
func TestSearchFilters(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := seedSearchCorpus(t, s)

	t.Run("mailbox", func(t *testing.T) {
		got, err := s.Search(ctx, store.SearchQuery{
			AccountID: c.Account.ID, Text: needleRare, MailboxID: &c.Archive.ID,
		})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("archive-scoped search returned %d results, want 1 (the historic message)", len(got))
		}
	})

	t.Run("since", func(t *testing.T) {
		since := time.Now().UTC().Add(-90 * 24 * time.Hour)
		got, err := s.Search(ctx, store.SearchQuery{
			AccountID: c.Account.ID, Text: needleRare, Since: &since,
		})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		// The 400-day-old message must be excluded.
		if len(got) != 3 {
			t.Errorf("90-day search returned %d results, want 3 (excluding the 400-day-old one)", len(got))
		}
	})

	t.Run("unread", func(t *testing.T) {
		got, err := s.Search(ctx, store.SearchQuery{
			AccountID: c.Account.ID, Text: needleRare, UnreadOnly: true,
		})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 1 || !containsID(got, c.UnreadID) {
			t.Errorf("unread-only search returned %d results, want just the unread needle %d",
				len(got), c.UnreadID)
		}
	})

	t.Run("keyword", func(t *testing.T) {
		got, err := s.Search(ctx, store.SearchQuery{
			AccountID: c.Account.ID, Text: needleRare, Keyword: "$MoovL3",
		})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 1 || !containsID(got, c.KeywordID) {
			t.Errorf("keyword search returned %d results, want just the labeled message %d",
				len(got), c.KeywordID)
		}
	})
}

// The limit is always applied, and is capped: an unbounded LIMIT would
// reintroduce the unbounded-work problem that sinks shapes #9 and #10.
func TestSearchLimitIsAlwaysBounded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := seedSearchCorpus(t, s)

	got, err := s.Search(ctx, store.SearchQuery{
		AccountID: c.Account.ID, Text: "reunion", Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("explicit limit 5 returned %d results", len(got))
	}

	// A caller asking for more than MaxSearchLimit is silently capped.
	huge, err := s.Search(ctx, store.SearchQuery{
		AccountID: c.Account.ID, Text: "reunion", Limit: 100000,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(huge) > store.MaxSearchLimit {
		t.Errorf("returned %d results, exceeding MaxSearchLimit %d", len(huge), store.MaxSearchLimit)
	}
}

// Shape #9's mitigation: relevance ranking, bounded to a recent window.
func TestSearchByRelevance(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := seedSearchCorpus(t, s)

	got, err := s.SearchByRelevance(ctx, store.SearchQuery{
		AccountID: c.Account.ID, Text: needleRare,
	})
	if err != nil {
		t.Fatalf("SearchByRelevance: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("relevance search returned nothing")
	}

	// Ranks must be descending, and non-zero for a match.
	for i := 1; i < len(got); i++ {
		if got[i].Rank > got[i-1].Rank {
			t.Errorf("results are not rank-descending: %f then %f", got[i-1].Rank, got[i].Rank)
		}
	}
	if got[0].Rank <= 0 {
		t.Errorf("top rank = %f, want > 0", got[0].Rank)
	}

	// A subject hit (weight A) must outrank a body-only hit (weight C). This
	// is the whole reason the weight scheme exists.
	subjectFirst := strings.Contains(strings.ToLower(got[0].Subject), needleRare)
	if !subjectFirst {
		t.Errorf("top relevance result %q does not have the term in its subject; "+
			"the A/B/C weight scheme is not being applied", got[0].Subject)
	}
}

// Shape #10's mitigation: capped counting with "n+" semantics.
func TestCountCapped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := seedSearchCorpus(t, s)

	// Below the cap: an exact count, not capped.
	count, capped, err := s.CountCapped(ctx, store.SearchQuery{
		AccountID: c.Account.ID, Text: needleRare,
	}, 100)
	if err != nil {
		t.Fatalf("CountCapped: %v", err)
	}
	if count != 4 {
		t.Errorf("count = %d, want 4", count)
	}
	if capped {
		t.Error("count was reported as capped, but it is below the cap")
	}

	// At the cap: the count stops and reports "n+".
	count, capped, err = s.CountCapped(ctx, store.SearchQuery{
		AccountID: c.Account.ID, Text: "reunion",
	}, 10)
	if err != nil {
		t.Fatalf("CountCapped: %v", err)
	}
	if count != 10 {
		t.Errorf("capped count = %d, want exactly the cap 10", count)
	}
	if !capped {
		t.Error("count hit the cap but was not reported as capped; the UI would show an exact total that is wrong")
	}
}

// Tombstoned messages must disappear from search: an expunged message is gone
// as far as the user is concerned, even though the row survives for
// Email/changes.
func TestSearchExcludesDeleted(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := seedSearchCorpus(t, s)

	before, err := s.Search(ctx, store.SearchQuery{AccountID: c.Account.ID, Text: needleRare})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if err := s.MarkDeleted(ctx, c.Inbox.ID, 1, []int64{1}); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	after, err := s.Search(ctx, store.SearchQuery{AccountID: c.Account.ID, Text: needleRare})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(after) != len(before)-1 {
		t.Errorf("after tombstoning one message, search returned %d results, want %d",
			len(after), len(before)-1)
	}
	if containsID(after, c.RareID) {
		t.Errorf("the tombstoned message %d is still in search results", c.RareID)
	}
}

// The folder view, which shares the account-scoped, always-limited discipline.
func TestListMailboxMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := seedSearchCorpus(t, s)

	got, err := s.ListMailboxMessages(ctx, c.Account.ID, c.Inbox.ID, 10)
	if err != nil {
		t.Fatalf("ListMailboxMessages: %v", err)
	}
	if len(got) != 10 {
		t.Errorf("returned %d messages, want 10", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Date.After(got[i-1].Date) {
			t.Error("folder listing is not date-descending")
		}
	}
}

// The composite GIN index must be USABLE for the search predicate.
//
// This asserts reachability, not plan choice. On the 48-row corpus these tests
// seed, walking (account_id, date DESC) and filtering really is cheaper than a
// bitmap scan, and the planner is right to prefer it — so asserting "the plan
// contains messages_acct_tsv_gin" here would be asserting that the planner
// makes a bad decision on small data.
//
// What can be checked at this size is that the index is a candidate at all:
// with sequential and index scans disabled, the only way to answer the query
// is the composite GIN. If the index were dropped, built without account_id,
// or made unusable for this predicate, the plan below could not exist.
//
// The plan CHOICE on a realistic corpus is verified where it is meaningful:
// TestSearchSmokeBenchmark in bench_test.go, whose 20k rows make the wrong
// plan visible as a latency collapse.
func TestCompositeGINIndexIsUsableForSearch(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := seedSearchCorpus(t, s)

	conn, err := s.Pool().Acquire(ctx)
	if err != nil {
		t.Fatalf("acquiring connection: %v", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Leave the composite GIN as the only usable index.
	//
	// Every other index on this table leads with account_id, so any of them
	// can serve the account predicate as a bitmap source and then filter on
	// tsv — which on a 48-row corpus is genuinely the cheapest plan. Turning
	// scan types off does not isolate the GIN, because a GIN index is itself
	// only reachable through a bitmap scan. Dropping the competitors inside a
	// transaction that rolls back is the unambiguous way to ask the question.
	// messages_pkey is deliberately not in this list: it backs a constraint
	// and cannot be dropped directly, and a primary key on `id` cannot serve
	// an account_id predicate anyway.
	for _, idx := range []string{
		"messages_acct_date",
		"messages_acct_sha",
		"messages_acct_msgid",
		"messages_reparse",
	} {
		if _, err := tx.Exec(ctx, `DROP INDEX IF EXISTS `+idx); err != nil {
			t.Fatalf("dropping competing index %s: %v", idx, err)
		}
	}
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disabling seqscan: %v", err)
	}

	rows, err := tx.Query(ctx, `
		EXPLAIN (FORMAT TEXT)
		SELECT m.id FROM messages m
		 WHERE m.account_id = $1
		   AND m.tsv @@ websearch_to_tsquery('simple', immutable_unaccent($2))
		 ORDER BY m.date DESC LIMIT 50`, c.Account.ID, needleRare)
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

	if !strings.Contains(plan.String(), "messages_acct_tsv_gin") {
		t.Errorf("the composite GIN index is not usable for the search predicate (S3 §5.2).\n"+
			"With seq and index scans disabled it is the only way to answer this query, so its\n"+
			"absence means the index is missing or does not cover (account_id, tsv).\nPlan:\n%s",
			plan.String())
	}
}
