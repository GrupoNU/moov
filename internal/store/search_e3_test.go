package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// L3 epic E3: the narrowing predicates, their plans, and the two indexes
// migration 0008 adds for them.
//
// Two kinds of test live here and they prove different things:
//
//   - the FUNCTIONAL ones prove each predicate selects the right rows, which is
//     what a wrong SQL operator or a swapped column breaks;
//   - the PLAN ones prove each predicate reaches the index it was measured on,
//     which is what NOTHING functional can catch. A cc filter that lost its
//     trigram index returns exactly the same rows, correctly, at 145 ms instead
//     of 1.8 ms — and the only way that surfaces before a user's mailbox grows
//     is an assertion on the plan.
//
// The plan tests are the reason migration 0008's header spells its expressions
// out character for character: an expression index applies only when the query
// repeats the expression exactly, so a paraphrase is a silent 80x regression.

// e3Corpus seeds an account whose messages vary along every E3 axis.
//
// The distributions matter: each predicate must be SELECTIVE enough that a plan
// choosing it is meaningful, and the corpus must be big enough that a
// sequential scan is not simply the cheapest honest plan for a tiny table —
// which would make every plan assertion vacuous rather than wrong.
func e3Corpus(t *testing.T, s *store.Store, n int) (store.Account, store.Mailbox, store.Mailbox, store.Mailbox) {
	t.Helper()
	ctx := context.Background()

	acct := newAccount(t, s)
	inbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)
	junk := seedMailbox(t, s, acct.ID, "Spam", store.RoleJunk)
	trash := seedMailbox(t, s, acct.ID, "Trash", store.RoleTrash)

	now := time.Now().UTC()
	msgs := make([]store.NewMessage, 0, n)
	for i := range n {
		box := inbox.ID
		switch {
		case i%20 == 0:
			box = junk.ID
		case i%20 == 1:
			box = trash.ID
		}
		var flags store.Flags
		if i%10 < 7 {
			flags |= store.FlagSeen
		}
		if i%13 == 0 {
			flags |= store.FlagFlagged
		}
		// The address suffix is derived from the message's ORDINAL among the
		// messages that have a Cc at all, not from `i` — so `copiado7@` is
		// guaranteed to exist. Deriving it from `i % 40` was a bug the first run
		// of TestNarrowingCcMatchesTheCcHeaderOnly caught: only multiples of 11
		// get a Cc, and `11k mod 40` never lands on 7, so the test filtered for
		// an address the corpus never seeded and failed on the corpus rather
		// than on the code.
		cc := ""
		if i%11 == 0 {
			cc = fmt.Sprintf("copiado%d@example.test", (i/11)%40)
		}
		bcc := "[]"
		if i%37 == 0 {
			bcc = fmt.Sprintf(`[{"name":"B","email":"oculto%d@example.test"}]`, (i/37)%30)
		}
		msgs = append(msgs, store.NewMessage{
			Message: store.Message{
				AccountID: acct.ID,
				RawSHA256: seedBlob(t, s, fmt.Sprintf("e3-%d-%d", acct.ID, i)),
				// Sizes spread over four orders of magnitude, so a size bound is
				// selective at some points and not at others.
				RawSize:        int64(1000 + (i%997)*5000),
				MessageID:      fmt.Sprintf("e3-%d-%d@test", acct.ID, i),
				Subject:        fmt.Sprintf("Asunto %d presupuesto", i),
				FromAddr:       "remitente@example.test",
				ToAddrs:        "destinatario@example.test",
				CcAddrs:        cc,
				Addresses:      []byte(fmt.Sprintf(`{"from":[],"to":[],"cc":[],"bcc":%s}`, bcc)),
				BodyText:       "cuerpo con presupuesto y reunion",
				Preview:        "cuerpo con presupuesto",
				HasAttachments: i%7 == 0,
				Date:           now.Add(-time.Duration(i) * time.Minute),
			},
			State: store.MessageState{
				AccountID: acct.ID, MailboxID: box,
				UID: int64(i + 1), UIDValidity: 1, Flags: flags,
			},
		})
	}
	if _, err := s.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("seeding the E3 corpus: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `ANALYZE messages; ANALYZE message_state`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}
	return acct, inbox, junk, trash
}

// ---------------------------------------------------------------------------
// functional: each predicate selects what it says
// ---------------------------------------------------------------------------

// RFC 8621 §4.4.1 hasAttachment, over both the folder view and the text search,
// because E3's whole point was adding each condition to ALL the shapes at once
// — a predicate served on one path and dropped on another is the silent filter
// loss query.go refuses filters to avoid.
func TestNarrowingHasAttachmentOnEveryShape(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct, inbox, _, _ := e3Corpus(t, s, 400)

	yes, no := true, false

	folderYes, err := s.ListMailboxMessages(ctx, store.MailboxListQuery{
		AccountID: acct.ID, MailboxID: inbox.ID, Limit: 200,
		Narrow: store.Narrowing{HasAttachment: &yes},
	})
	if err != nil {
		t.Fatalf("folder view: %v", err)
	}
	if len(folderYes) == 0 {
		t.Fatal("hasAttachment:true matched nothing; the corpus seeds one in seven")
	}

	folderNo, err := s.ListMailboxMessages(ctx, store.MailboxListQuery{
		AccountID: acct.ID, MailboxID: inbox.ID, Limit: 200,
		Narrow: store.Narrowing{HasAttachment: &no},
	})
	if err != nil {
		t.Fatalf("folder view: %v", err)
	}
	// The two are disjoint and neither is the whole folder: a predicate that
	// was silently dropped would make both return the same rows.
	for _, a := range folderYes {
		for _, b := range folderNo {
			if a.MessageID == b.MessageID {
				t.Fatalf("message %d matched both hasAttachment true and false", a.MessageID)
			}
		}
	}

	textYes, err := s.Search(ctx, store.SearchQuery{
		AccountID: acct.ID, Text: "presupuesto", Limit: 200,
		Narrow: store.Narrowing{HasAttachment: &yes},
	})
	if err != nil {
		t.Fatalf("text search: %v", err)
	}
	if len(textYes) == 0 {
		t.Error("hasAttachment reached the folder view but not the text search")
	}

	acctWide, err := s.ListAccountMessages(ctx, store.AccountListQuery{
		AccountID: acct.ID, Limit: 200,
		Narrow: store.Narrowing{HasAttachment: &yes},
	})
	if err != nil {
		t.Fatalf("account-wide: %v", err)
	}
	if len(acctWide) == 0 {
		t.Error("hasAttachment reached the folder view but not the account-wide shape")
	}

	collapsed, err := s.ListCollapsedMessages(ctx, store.CollapsedQuery{
		AccountID: acct.ID, MailboxID: &inbox.ID, Limit: 50,
		Narrow: store.Narrowing{HasAttachment: &yes},
	})
	if err != nil {
		t.Fatalf("collapsed: %v", err)
	}
	if len(collapsed.Rows) == 0 {
		t.Error("hasAttachment reached the folder view but not the collapsed shape")
	}
}

// §4.4.1 cc: "Looks for the text in the Cc header field of the message."
//
// The assertion is that every returned row ACTUALLY carries the address in its
// Cc — the exactness that separates this from the documented from/to/subject
// over-match, and the reason migration 0008 exists.
func TestNarrowingCcMatchesTheCcHeaderOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct, _, _, _ := e3Corpus(t, s, 400)

	got, err := s.ListAccountMessages(ctx, store.AccountListQuery{
		AccountID: acct.ID, Limit: 200,
		Narrow: store.Narrowing{Cc: "copiado7@example.test"},
	})
	if err != nil {
		t.Fatalf("cc search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the cc filter matched nothing; the corpus seeds copiado7@")
	}
	for _, r := range got {
		full, err := s.GetMessage(ctx, r.MessageID)
		if err != nil {
			t.Fatalf("reading message %d: %v", r.MessageID, err)
		}
		if !strings.Contains(strings.ToLower(full.CcAddrs), "copiado7@example.test") {
			t.Errorf("message %d has Cc %q, which does not contain the filtered address — "+
				"the cc filter is matching something other than the Cc header",
				r.MessageID, full.CcAddrs)
		}
	}
}

// §4.4.1 bcc. Bcc lives only in the addresses JSONB (migration 0002 keeps it
// out of the tsvector deliberately), so this is the condition that had no
// over-match to fall back on: it was an index or a refusal.
func TestNarrowingBccReadsTheAddressesJSONB(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct, _, _, _ := e3Corpus(t, s, 400)

	got, err := s.ListAccountMessages(ctx, store.AccountListQuery{
		AccountID: acct.ID, Limit: 200,
		Narrow: store.Narrowing{Bcc: "oculto7@example.test"},
	})
	if err != nil {
		t.Fatalf("bcc search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the bcc filter matched nothing; the corpus seeds oculto7@")
	}
	for _, r := range got {
		full, err := s.GetMessage(ctx, r.MessageID)
		if err != nil {
			t.Fatalf("reading message %d: %v", r.MessageID, err)
		}
		if !strings.Contains(strings.ToLower(string(full.Addresses)), "oculto7@example.test") {
			t.Errorf("message %d does not carry the filtered address in its bcc", r.MessageID)
		}
	}

	// And a bcc term must NOT be findable through the full-text search, which
	// is the property that made this an index-or-refusal decision in the first
	// place. If this ever starts passing, migration 0002's weight bands changed
	// and the bcc filter's justification changed with them.
	viaFTS, err := s.Search(ctx, store.SearchQuery{
		AccountID: acct.ID, Text: "oculto7", Limit: 50,
	})
	if err != nil {
		t.Fatalf("fts probe: %v", err)
	}
	if len(viaFTS) != 0 {
		t.Errorf("a bcc address is full-text searchable (%d hits); migration 0002 keeps bcc "+
			"OUT of the tsvector, and migration 0008's justification rests on that", len(viaFTS))
	}
}

// A LIKE metacharacter in the search term must be a literal, not a wildcard.
// Without escaping, a user searching for "%" matches every message that has any
// Cc at all — not an injection (the term is a bound parameter throughout) but a
// result the user did not ask for.
func TestNarrowingAddressTermsEscapeLikeMetacharacters(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct, _, _, _ := e3Corpus(t, s, 200)

	for _, term := range []string{"%", "_", `\`, "%@%"} {
		got, err := s.ListAccountMessages(ctx, store.AccountListQuery{
			AccountID: acct.ID, Limit: 200,
			Narrow: store.Narrowing{Cc: term},
		})
		if err != nil {
			t.Fatalf("cc %q: %v", term, err)
		}
		if len(got) != 0 {
			t.Errorf("cc:%q matched %d messages; a LIKE metacharacter must be a literal, "+
				"and no seeded address contains it", term, len(got))
		}
	}
}

// §4.4.1 minSize ("greater than or equal to") and maxSize ("less than") — the
// inclusive/exclusive asymmetry is in the RFC and is easy to get backwards.
func TestNarrowingSizeBoundsHonorTheRFCsInclusivity(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct, _, _, _ := e3Corpus(t, s, 400)

	// Pick a real size out of the corpus so the boundary is exercised on a row
	// that exists, rather than on a gap between rows.
	var pivot int64
	if err := s.Pool().QueryRow(ctx,
		`SELECT raw_size FROM messages WHERE account_id=$1 ORDER BY raw_size LIMIT 1 OFFSET 100`,
		acct.ID).Scan(&pivot); err != nil {
		t.Fatalf("picking a pivot size: %v", err)
	}

	atOrAbove, err := s.ListAccountMessages(ctx, store.AccountListQuery{
		AccountID: acct.ID, Limit: 200, Narrow: store.Narrowing{MinSize: &pivot},
	})
	if err != nil {
		t.Fatalf("minSize: %v", err)
	}
	for _, r := range atOrAbove {
		full, _ := s.GetMessage(ctx, r.MessageID)
		if full.RawSize < pivot {
			t.Errorf("minSize %d returned a %d-octet message", pivot, full.RawSize)
		}
	}
	// minSize is INCLUSIVE, so the pivot row itself must be in the result.
	foundPivot := false
	for _, r := range atOrAbove {
		full, _ := s.GetMessage(ctx, r.MessageID)
		if full.RawSize == pivot {
			foundPivot = true
			break
		}
	}
	if !foundPivot {
		t.Error("minSize excluded a message of exactly that size; §4.4.1 says " +
			"'greater than or equal to'")
	}

	below, err := s.ListAccountMessages(ctx, store.AccountListQuery{
		AccountID: acct.ID, Limit: 200, Narrow: store.Narrowing{MaxSize: &pivot},
	})
	if err != nil {
		t.Fatalf("maxSize: %v", err)
	}
	for _, r := range below {
		full, _ := s.GetMessage(ctx, r.MessageID)
		if full.RawSize >= pivot {
			// maxSize is EXCLUSIVE.
			t.Errorf("maxSize %d returned a %d-octet message; §4.4.1 says 'less than'",
				pivot, full.RawSize)
		}
	}
}

// The bitmask predicate that makes Gmail's `is:starred` answerable (canon §2.5).
func TestNarrowingFlagBitmaskSelectsSystemFlags(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct, _, _, _ := e3Corpus(t, s, 400)

	flagged, err := s.ListAccountMessages(ctx, store.AccountListQuery{
		AccountID: acct.ID, Limit: 200,
		Narrow: store.Narrowing{FlagsAll: store.FlagFlagged},
	})
	if err != nil {
		t.Fatalf("flagged search: %v", err)
	}
	if len(flagged) == 0 {
		t.Fatal("no flagged messages; the corpus flags one in thirteen")
	}
	for _, r := range flagged {
		if !r.Flags.Has(store.FlagFlagged) {
			t.Errorf("message %d came back for FlagsAll:$flagged without the bit", r.MessageID)
		}
	}

	notFlagged, err := s.ListAccountMessages(ctx, store.AccountListQuery{
		AccountID: acct.ID, Limit: 200,
		Narrow: store.Narrowing{FlagsNone: store.FlagFlagged},
	})
	if err != nil {
		t.Fatalf("not-flagged search: %v", err)
	}
	for _, r := range notFlagged {
		if r.Flags.Has(store.FlagFlagged) {
			t.Errorf("message %d came back for FlagsNone:$flagged WITH the bit", r.MessageID)
		}
	}
}

// §4.4.1 inMailboxOtherThan, which carries the Gmail default exclusion of Spam
// and Trash (canon §2.5). This is the store half; the POLICY that decides when
// to apply it is pinned in internal/jmap/mail.
func TestNarrowingExcludesNamedMailboxes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct, _, junk, trash := e3Corpus(t, s, 400)

	all, err := s.ListAccountMessages(ctx, store.AccountListQuery{
		AccountID: acct.ID, Limit: 200,
	})
	if err != nil {
		t.Fatalf("unfiltered: %v", err)
	}
	sawSpecial := false
	for _, r := range all {
		if r.MailboxID == junk.ID || r.MailboxID == trash.ID {
			sawSpecial = true
			break
		}
	}
	if !sawSpecial {
		t.Fatal("the corpus put nothing in Spam or Trash, so the exclusion proves nothing")
	}

	excluded, err := s.ListAccountMessages(ctx, store.AccountListQuery{
		AccountID: acct.ID, Limit: 200,
		Narrow: store.Narrowing{ExcludeMailboxIDs: []int64{junk.ID, trash.ID}},
	})
	if err != nil {
		t.Fatalf("excluded: %v", err)
	}
	if len(excluded) == 0 {
		t.Fatal("the exclusion removed everything")
	}
	for _, r := range excluded {
		if r.MailboxID == junk.ID || r.MailboxID == trash.ID {
			t.Errorf("message %d from an excluded mailbox survived inMailboxOtherThan", r.MessageID)
		}
	}
}

// The narrowing must reach the collapsed shape's cross-page DEDUPE anti-join,
// not only its candidate window. Omitting it there breaks in the direction that
// HIDES mail: a thread whose only newer member fails the narrowing was never
// offered on an earlier page, so excluding it now would drop the conversation
// entirely.
func TestNarrowingReachesTheCollapsedDedupeClause(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct, inbox, _, _ := e3Corpus(t, s, 400)

	yes := true
	narrow := store.Narrowing{HasAttachment: &yes}

	first, err := s.ListCollapsedMessages(ctx, store.CollapsedQuery{
		AccountID: acct.ID, MailboxID: &inbox.ID, Limit: 5, Window: 50, Narrow: narrow,
	})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.NextCursor == nil {
		t.Skip("the corpus did not fill the window, so there is no resumed page to test")
	}

	second, err := s.ListCollapsedMessages(ctx, store.CollapsedQuery{
		AccountID: acct.ID, MailboxID: &inbox.ID, Limit: 5, Window: 50,
		Narrow: narrow, After: first.NextCursor,
	})
	if err != nil {
		t.Fatalf("resumed page: %v", err)
	}
	// Every row of the resumed page must still satisfy the narrowing — a dedupe
	// clause that dropped it would let through rows the filter excludes.
	for _, r := range second.Rows {
		full, _ := s.GetMessage(ctx, r.MessageID)
		if !full.HasAttachments {
			t.Errorf("resumed page returned message %d without an attachment; the narrowing "+
				"did not reach the dedupe clause", r.MessageID)
		}
	}
}

// ---------------------------------------------------------------------------
// the plan canaries
// ---------------------------------------------------------------------------

// The cc and bcc filters MUST reach the trigram indexes of migration 0008.
//
// This is the assertion no functional test can make. A cc filter that lost its
// index returns exactly the same rows, correctly, at 145.9 ms instead of 1.8 ms
// — and the loss is easy: an expression index applies only when the query
// repeats the indexed expression CHARACTER FOR CHARACTER, so ILIKE instead of
// lower()+LIKE, or a different jsonpath spelling, silently drops back to the
// sequential scan the migration was written to remove.
func TestAddressFiltersReachTheirTrigramIndexes(t *testing.T) {
	s := testStore(t)
	acct, _, _, _ := e3Corpus(t, s, 3000)

	t.Run("cc", func(t *testing.T) {
		plan := explain(t, s, `
			SELECT m.id FROM messages m JOIN message_state ms ON ms.message_id = m.id
			 WHERE m.account_id = $1 AND ms.deleted_at IS NULL
			   AND lower(m.cc_addrs) LIKE $2 ESCAPE '\'
			 ORDER BY m.date DESC, m.id DESC LIMIT 50`,
			acct.ID, "%copiado7@example.test%")

		if !strings.Contains(plan, "messages_cc_trgm") {
			t.Errorf("the cc filter does NOT reach messages_cc_trgm.\n"+
				"Without it the predicate is a sequential scan whose cost is the mailbox's "+
				"size (measured 145.9 ms at 120k messages against 1.8 ms indexed), and the "+
				"filter still returns the right rows — so nothing but this assertion catches "+
				"it. The query must repeat migration 0008's expression character for "+
				"character.\nPlan:\n%s", plan)
		}
	})

	t.Run("bcc", func(t *testing.T) {
		plan := explain(t, s, `
			SELECT m.id FROM messages m JOIN message_state ms ON ms.message_id = m.id
			 WHERE m.account_id = $1 AND ms.deleted_at IS NULL
			   AND lower(jsonb_path_query_array(m.addresses, '$.bcc[*].email')::text) LIKE $2 ESCAPE '\'
			 ORDER BY m.date DESC, m.id DESC LIMIT 50`,
			acct.ID, "%oculto7@example.test%")

		if !strings.Contains(plan, "messages_bcc_trgm") {
			t.Errorf("the bcc filter does NOT reach messages_bcc_trgm.\n"+
				"Measured 167.5 ms unindexed against 0.6 ms indexed at 120k messages.\n"+
				"Plan:\n%s", plan)
		}
	})
}

// The SQL the store actually BUILDS must be the SQL the plan test asserts on.
//
// The test above proves a hand-written query reaches the index. This proves the
// store emits that query — closing the gap where the index works fine and the
// repertoire spells the predicate differently, which would make the plan test a
// reassuring lie.
func TestTheStoresOwnAddressPredicateReachesTheIndex(t *testing.T) {
	s := testStore(t)
	acct, _, _, _ := e3Corpus(t, s, 3000)

	cases := []struct {
		name  string
		build func() (string, []any)
		index string
	}{{
		name: "account-wide cc",
		build: func() (string, []any) {
			return store.BuildAccountListSQL(store.AccountListQuery{
				AccountID: acct.ID, Limit: 50,
				Narrow: store.Narrowing{Cc: "copiado7@example.test"},
			})
		},
		index: "messages_cc_trgm",
	}, {
		name: "account-wide bcc",
		build: func() (string, []any) {
			return store.BuildAccountListSQL(store.AccountListQuery{
				AccountID: acct.ID, Limit: 50,
				Narrow: store.Narrowing{Bcc: "oculto7@example.test"},
			})
		},
		index: "messages_bcc_trgm",
	}, {
		name: "text search with a cc narrowing",
		build: func() (string, []any) {
			return store.BuildSearchSQL(store.SearchQuery{
				AccountID: acct.ID, Text: "presupuesto", Limit: 50,
				Narrow: store.Narrowing{Cc: "copiado7@example.test"},
			})
		},
		index: "messages_cc_trgm",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, args := tc.build()
			plan := explain(t, s, sql, args...)
			if !strings.Contains(plan, tc.index) {
				t.Errorf("the STORE'S OWN statement does not reach %s.\n"+
					"The hand-written plan test may still pass — that one proves the index "+
					"works, this one proves the repertoire uses it. An expression index "+
					"applies only when the query repeats migration 0008's expression "+
					"character for character, so this is what catches a paraphrase in the "+
					"builder.\nSQL:\n%s\nPlan:\n%s", tc.index, sql, plan)
			}
		})
	}
}

// The E3 predicates must not disturb the plans the repertoire already had.
//
// Migration 0004 states the rule and internal/store/collapse.go declined to
// write migration 0008 once on its strength: an index gives the planner one more
// way to compete with the composite GIN, and S3 §5.3 is the cautionary tale
// where that took a 1.6 ms query to 13,085 ms. The rule was TESTED rather than
// assumed when 0008 was written — the four pre-existing text plans were
// EXPLAIN'd before and after, and were byte-identical — and this keeps it true.
func TestTrigramIndexesDoNotDisturbTheTextSearchPlan(t *testing.T) {
	s := testStore(t)
	acct, inbox, _, _ := e3Corpus(t, s, 3000)

	plans := map[string]string{
		"rare term alone": `
			SELECT m.id FROM messages m JOIN message_state ms ON ms.message_id = m.id
			 WHERE m.account_id = $1
			   AND m.tsv @@ websearch_to_tsquery('simple', immutable_unaccent($2))
			   AND ms.deleted_at IS NULL
			 ORDER BY m.date DESC, m.id DESC LIMIT 50`,
		"rare term in a folder": `
			SELECT m.id FROM messages m JOIN message_state ms ON ms.message_id = m.id
			 WHERE m.account_id = $1
			   AND m.tsv @@ websearch_to_tsquery('simple', immutable_unaccent($2))
			   AND ms.deleted_at IS NULL AND ms.mailbox_id = $3
			 ORDER BY m.date DESC, m.id DESC LIMIT 50`,
	}

	for name, q := range plans {
		t.Run(name, func(t *testing.T) {
			args := []any{acct.ID, "presupuesto"}
			if strings.Contains(q, "$3") {
				args = append(args, inbox.ID)
			}
			plan := explain(t, s, q, args...)
			for _, unwanted := range []string{"messages_cc_trgm", "messages_bcc_trgm"} {
				if strings.Contains(plan, unwanted) {
					t.Errorf("a text search reached %s.\n"+
						"The E3 address indexes must be INERT for every shape the repertoire "+
						"already served — that is migration 0004's rule, and the reason 0008 "+
						"could be written at all.\nPlan:\n%s", unwanted, plan)
				}
			}
		})
	}
}

// Migration 0008 must actually create both indexes. A migration that ran but
// created nothing would let every plan test above fail with a confusing message
// about query spelling.
func TestMigration0008CreatesBothTrigramIndexes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for _, name := range []string{"messages_cc_trgm", "messages_bcc_trgm"} {
		var exists bool
		if err := s.Pool().QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE tablename='messages' AND indexname=$1)`,
			name).Scan(&exists); err != nil {
			t.Fatalf("checking for %s: %v", name, err)
		}
		if !exists {
			t.Errorf("index %s does not exist; migration 0008 did not create it", name)
		}
	}
}
