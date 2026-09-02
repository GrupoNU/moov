package mail

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
)

// The account-wide label view (L3 epic E8).
//
// A Gmail label view is ACCOUNT-WIDE by definition (canon §2.1): clicking a
// label in the sidebar shows every message carrying it, in every folder. The
// PWA sends a bare `{"hasKeyword":"$label:work"}` for exactly that, and this
// server used to refuse it — so the sidebar rendered an empty list from a
// REFUSAL rather than from an empty mailbox. Migration 0011 made the plan fast
// enough to serve it (89 ms p95 at 400,000 messages, 2% label density).
//
// These tests pin the three halves of that decision:
//
//  1. a bare CUSTOM keyword is answerable, and it is scoped account-wide;
//  2. a bare SYSTEM flag is still refused, because it is a bitmask predicate
//     with no index and the decision to refuse it bare was re-verified;
//  3. the refusal that remains says something CURRENT, not something stale.

// The headline: the exact filter the PWA's sidebar sends.
func TestQueryBareLabelIsAnsweredAccountWide(t *testing.T) {
	f := newFakeReaders()
	got := runQuery(t, f, `{"hasKeyword":"$label:work"}`)

	if got.keyword != "$label:work" {
		t.Errorf("keyword = %q, want %q — the label condition was dropped in translation, "+
			"which would list the whole account as though it were the label",
			got.keyword, "$label:work")
	}
	if !got.accountWide {
		t.Error("a bare label filter was NOT scoped account-wide.\n" +
			"Without the scope it reaches the folder-view branch of fetchPage, which has no " +
			"mailbox to walk and no keyword predicate — canon §2.1 makes a label view " +
			"account-wide, so scoping it to one folder would hide the mail the user filed away.")
	}
	if got.mailboxID != nil {
		t.Errorf("mailboxID = %v, want nil: a label view names no folder", *got.mailboxID)
	}
}

// The scope must NOT bring Gmail's default Spam/Trash exclusion with it.
//
// applyDefaultExclusion returns early for an account-wide filter, and a label
// view inherits that. It is the right answer here for the same reason it is
// right for `filter: null`: the user asked for a LABEL, and a message they
// labeled and then filed in Spam is still a message carrying that label. But
// it is inherited rather than chosen, so it is pinned — if the exclusion policy
// ever grows a case, this test says which way the label view was decided.
func TestQueryBareLabelDoesNotInheritTheSpamExclusion(t *testing.T) {
	f := newFakeReaders()
	got := runQuery(t, f, `{"hasKeyword":"$label:work"}`)

	if got.defaultExclusion {
		t.Error("the account-wide label view applied the default Spam/Trash exclusion; " +
			"§5.5's account enumeration means everything, and a labeled message is labeled " +
			"wherever it sits")
	}
}

// A BARE SYSTEM FLAG STAYS REFUSED — the decision this change deliberately did
// not widen.
//
// The four IMAP system flags are BITS in message_state.flags, not entries in
// the keywords array (arbitration A6, migration 0002). They therefore never
// reach searchFilter.keyword at all — applyHasKeyword routes them to the
// flagsAll bitmask — so the relaxation above cannot reach them by construction.
//
// That is not an accident to be discovered later: store.Narrowing.FlagsAll
// records that the bitmask has no index and measures `is:starred` at 77.5 ms
// by a PARALLEL SEQUENTIAL SCAN of message_state on a 120,000-message account.
// Account-wide and unbounded, that is the shape the repertoire exists to make
// unrepresentable.
func TestQueryBareSystemFlagIsStillRefused(t *testing.T) {
	for _, kw := range []string{KeywordFlagged, KeywordAnswered, KeywordDraft, KeywordSeen} {
		t.Run(kw, func(t *testing.T) {
			_, merr := translateFilter(json.RawMessage(fmt.Sprintf(`{"hasKeyword":%q}`, kw)))
			if merr == nil {
				t.Fatalf("a bare hasKeyword:%q was ACCEPTED.\n"+
					"System flags are a bitmask with no index (store.Narrowing.FlagsAll: 77.5 ms "+
					"by parallel seq scan at 120k messages). Account-wide that is the unbounded "+
					"scan the repertoire forbids — only the keywords ARRAY was relaxed.", kw)
			}
			if merr.Code != jmap.CodeUnsupportedFilter {
				t.Errorf("code = %q, want %q", merr.Code, jmap.CodeUnsupportedFilter)
			}
		})
	}
}

// A label AND a folder is the one keyword shape still refused, and the refusal
// must name a reason that is TRUE TODAY.
//
// query.go's collapseRefusal documents the failure this guards against: the old
// collapseThreads refusal ("this server has no thread index yet") was correct
// when written and became false the day migration 0004 landed, and nothing made
// it say so. A refusal whose reason has expired is worse than no refusal,
// because it sends the reader looking for the wrong thing.
//
// So this pins the SUBSTANCE: the message must point at the folder view's
// missing keyword predicate (still true — ListMailboxMessages takes no keyword
// parameter) and must offer the remedy that now exists (drop the mailbox).
func TestQueryLabelWithMailboxIsRefusedWithACurrentReason(t *testing.T) {
	_, merr := translateFilter(json.RawMessage(
		`{"operator":"AND","conditions":[{"inMailbox":"m1"},{"hasKeyword":"$label:work"}]}`))
	if merr == nil {
		t.Fatal("a label scoped to a folder was accepted, but ListMailboxMessages has no " +
			"keyword parameter — it would silently ignore the label")
	}
	if merr.Code != jmap.CodeUnsupportedFilter {
		t.Fatalf("code = %q, want %q", merr.Code, jmap.CodeUnsupportedFilter)
	}

	// The remedy must be named. A client that is only told "no" cannot act.
	for _, want := range []string{"folder view has no keyword predicate", "account-wide"} {
		if !strings.Contains(merr.Description, want) {
			t.Errorf("the refusal does not mention %q.\nGot: %s", want, merr.Description)
		}
	}
	// And it must NOT claim a text condition is the only way out — that was the
	// old reason, and it is now false: dropping the mailbox works.
	if strings.Contains(merr.Description, "is not supported without a text condition") {
		t.Errorf("the refusal still gives the PRE-0011 reason, which is now stale: a bare "+
			"label IS served account-wide.\nGot: %s", merr.Description)
	}
}

// The bare-label relaxation must not leak into the OR boundedness rule.
//
// translateOperator's rule is "a branch of an OR must be a filter this server
// would serve on its own", and answerable() IS that test — so a label branch is
// now legal. What must stay true is that the branch carries its SCOPE into the
// union: a branch judged answerable because it was scoped account-wide, but
// appended without the scope, would reach the folder-view branch of fetchPage
// with no mailbox.
func TestQueryLabelBranchOfAnORKeepsItsScope(t *testing.T) {
	got, merr := translateFilter(json.RawMessage(
		`{"operator":"OR","conditions":[{"hasKeyword":"$label:work"},{"hasKeyword":"$label:home"}]}`))
	if merr != nil {
		t.Fatalf("an OR of two label views was refused: %s", merr.Description)
	}
	if len(got.or) != 2 {
		t.Fatalf("branches = %d, want 2", len(got.or))
	}
	for i, br := range got.or {
		if !br.accountWide {
			t.Errorf("branch %d lost its account-wide scope on the way into the union; "+
				"it would reach the folder view with no mailbox to walk", i)
		}
		if br.keyword == "" {
			t.Errorf("branch %d lost its keyword", i)
		}
	}
}
