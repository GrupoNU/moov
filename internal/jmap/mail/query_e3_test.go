package mail

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
)

// L3 epic E3: the filter conditions Email/query gained, the operators it now
// accepts, and the Gmail default exclusion.
//
// These tests run against the FAKES, which ignore the filter when producing
// results — so none of them assert "the right messages came back". They assert
// the thing the fakes CAN prove and that no result-level test can: that a
// condition survived translation and reached the reader, rather than being
// silently dropped. Silent dropping is the failure query.go exists to prevent
// (a filter the user typed, ignored, returning mail they excluded), and it is
// invisible to any assertion made on ids.
//
// The end-to-end proof — that the SQL these translate into returns the right
// rows off a real PostgreSQL, on the plans measured — is
// query_e3_integration_test.go and internal/store/search_e3_test.go.

// runQuery issues an Email/query and returns the filter the reader received.
func runQuery(t *testing.T, f *fakeReaders, filter string) searchFilter {
	t.Helper()
	_, merr := f.deps().handleEmailQuery(callerCtx(), json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"filter":%s}`, testAccountJMAPID(), filter)))
	if merr != nil {
		t.Fatalf("Email/query(%s) failed: %v — %s", filter, merr.Code, merr.Description)
	}
	return f.lastFilter
}

// RFC 8621 §4.4.1: "hasAttachment: Boolean — If true, filters on Emails where
// the attachments property is not empty; if false, filters on Emails where it
// is empty."
func TestQueryHasAttachmentReachesTheReader(t *testing.T) {
	for _, want := range []bool{true, false} {
		t.Run(fmt.Sprint(want), func(t *testing.T) {
			f := newFakeReaders()
			got := runQuery(t, f, fmt.Sprintf(`{"inMailbox":"m1","hasAttachment":%t}`, want))
			if got.hasAttachment == nil {
				t.Fatal("hasAttachment was dropped in translation")
			}
			if *got.hasAttachment != want {
				t.Errorf("hasAttachment = %t, want %t", *got.hasAttachment, want)
			}
		})
	}
}

// RFC 8621 §4.4.1: "cc: String — Looks for the text in the Cc header field of
// the message"; bcc likewise.
//
// The two are asserted to land in SEPARATE fields, because the bug they invite
// is one field: cc_addrs is a column and bcc lives in the addresses JSONB, so a
// shared field would mean one of the two silently searching the wrong place.
func TestQueryCcAndBccAreSeparateConditions(t *testing.T) {
	f := newFakeReaders()
	got := runQuery(t, f, `{"inMailbox":"m1","cc":"copiado@x.test","bcc":"oculto@x.test"}`)
	if got.cc != "copiado@x.test" {
		t.Errorf("cc = %q, want the Cc term", got.cc)
	}
	if got.bcc != "oculto@x.test" {
		t.Errorf("bcc = %q, want the Bcc term", got.bcc)
	}
	// And neither may leak into the full-text term, which is what would turn an
	// exact per-column match into the documented from/to/subject over-match.
	if got.text != "" {
		t.Errorf("text = %q, want empty: cc/bcc are per-column, not full-text", got.text)
	}
}

// RFC 8621 §4.4.1: "minSize: UnsignedInt — The size of the Email in octets is
// greater than or equal to this number"; "maxSize: ... is less than this
// number."
func TestQuerySizeBoundsReachTheReader(t *testing.T) {
	f := newFakeReaders()
	got := runQuery(t, f, `{"inMailbox":"m1","minSize":1000,"maxSize":25000000}`)
	if got.minSize == nil || *got.minSize != 1000 {
		t.Errorf("minSize = %v, want 1000", got.minSize)
	}
	if got.maxSize == nil || *got.maxSize != 25000000 {
		t.Errorf("maxSize = %v, want 25000000", got.maxSize)
	}
}

// §4.4.1 types both sizes UnsignedInt, so a negative one is not a size this
// server can act on. It is named rather than coerced to zero — which would
// silently widen the filter.
func TestQueryNegativeSizeIsRefusedRatherThanCoerced(t *testing.T) {
	f := newFakeReaders()
	merr := queryError(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":"m1","minSize":-1}}`, testAccountJMAPID()))
	if merr.Code != jmap.CodeUnsupportedFilter {
		t.Errorf("code = %q, want unsupportedFilter", merr.Code)
	}
	if !contains(merr.Description, "minSize") {
		t.Errorf("description %q does not name the node", merr.Description)
	}
}

// The E3 headline: hasKeyword on an IMAP SYSTEM flag, which is what makes
// Gmail's `is:starred` (canon §2.5) answerable. It must become a BITMASK bit,
// never an entry in the keyword array — the array is where A6 puts labels, and
// a system flag is not there, so routing it that way would return nothing while
// looking like it worked.
func TestQuerySystemFlagsBecomeBitmaskPredicates(t *testing.T) {
	cases := []struct {
		keyword string
		bit     uint64
	}{
		{KeywordFlagged, 1 << 2},
		{KeywordAnswered, 1 << 1},
		{KeywordDraft, 1 << 4},
		{KeywordSeen, 1 << 0},
	}
	for _, tc := range cases {
		t.Run(tc.keyword, func(t *testing.T) {
			f := newFakeReaders()
			got := runQuery(t, f, fmt.Sprintf(`{"text":"x","hasKeyword":%q}`, tc.keyword))
			if got.flagsAll&tc.bit == 0 {
				t.Errorf("flagsAll = %b, want bit %b set", got.flagsAll, tc.bit)
			}
			if got.keyword != "" {
				t.Errorf("keyword = %q, want empty: a system flag is a bitmask bit, "+
					"and the keyword array does not hold it", got.keyword)
			}
		})
	}
}

// A USER keyword still goes to the array — the other half of the same
// distinction, asserted so a future refactor cannot collapse the two paths.
func TestQueryUserKeywordStillUsesTheArray(t *testing.T) {
	f := newFakeReaders()
	got := runQuery(t, f, `{"text":"x","hasKeyword":"$MoovL7"}`)
	if got.keyword != "$MoovL7" {
		t.Errorf("keyword = %q, want the label", got.keyword)
	}
	if got.flagsAll != 0 {
		t.Errorf("flagsAll = %b, want 0: a label is not a system flag", got.flagsAll)
	}
}

// §4.4.1 notKeyword, extended in E3 from $seen alone to all four system flags.
//
// $seen keeps its own field rather than becoming a FlagsNone bit, because it is
// spelled as the literal `(flags & 1) = 0` that the message_state_unread
// partial index is built on — the single most common filter in the product, and
// the one place the exact spelling decides whether an index applies.
func TestQueryNotKeywordSeenKeepsTheUnreadPath(t *testing.T) {
	f := newFakeReaders()
	got := runQuery(t, f, `{"text":"x","notKeyword":"$seen"}`)
	if !got.unreadOnly {
		t.Error("notKeyword:$seen did not set the unread filter")
	}
	if got.flagsNone != 0 {
		t.Errorf("flagsNone = %b, want 0: $seen must stay on the partial-index path", got.flagsNone)
	}
}

func TestQueryNotKeywordOtherSystemFlagsBecomeExcludedBits(t *testing.T) {
	f := newFakeReaders()
	got := runQuery(t, f, `{"text":"x","notKeyword":"$draft"}`)
	if got.flagsNone&(1<<4) == 0 {
		t.Errorf("flagsNone = %b, want the $draft bit set", got.flagsNone)
	}
}

// RFC 8621 §4.4.1 inMailboxOtherThan, and the Gmail default exclusion built on
// it (canon §2.5, citing support.google.com/mail/answer/7190: "Spam/Trash
// excluded by default; in:anywhere includes them").
//
// This is a DELIBERATE, cited deviation from a naive reading of the RFC — which
// says nothing about scope — and the four cases below are the whole policy. If
// any one of them changes, the search stops behaving the way the canon says
// Gmail behaves, and that must be a decision rather than a regression.
func TestQueryDefaultExclusionFollowsTheGmailRule(t *testing.T) {
	cases := []struct {
		name    string
		filter  string
		exclude bool
		why     string
	}{{
		name:    "a plain text search excludes Spam and Trash",
		filter:  `{"text":"factura"}`,
		exclude: true,
		why:     "canon §2.5: Spam and Trash are excluded from a standard search",
	}, {
		name:    "an explicit inMailbox is in:spam / in:trash and is served",
		filter:  `{"text":"factura","inMailbox":"m1"}`,
		exclude: false,
		why:     "the user named a folder; excluding on top would make in:trash return nothing",
	}, {
		name:    "an empty inMailboxOtherThan is in:anywhere",
		filter:  `{"text":"factura","inMailboxOtherThan":[]}`,
		exclude: false,
		why:     "canon §2.5: in:anywhere includes Spam and Trash",
	}, {
		name:    "filter:null is the RFC's account enumeration, untouched",
		filter:  `null`,
		exclude: false,
		why:     "RFC 8620 §5.5: 'all objects in the account of this type'",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeReaders()
			got := runQuery(t, f, tc.filter)
			if got.defaultExclusion != tc.exclude {
				t.Errorf("defaultExclusion = %t, want %t — %s",
					got.defaultExclusion, tc.exclude, tc.why)
			}
		})
	}
}

// A client's own inMailboxOtherThan reaches the reader intact, and suppresses
// the server's default one: two exclusions silently merged would give the
// client a scope it did not ask for.
func TestQueryClientExclusionReplacesTheDefault(t *testing.T) {
	f := newFakeReaders()
	got := runQuery(t, f, `{"text":"x","inMailboxOtherThan":["m1"]}`)
	if got.defaultExclusion {
		t.Error("an explicit inMailboxOtherThan must suppress the server default")
	}
	if len(got.excludeMailboxIDs) != 1 {
		t.Errorf("excludeMailboxIDs = %v, want the one id the client named", got.excludeMailboxIDs)
	}
}

// RFC 8620 §5.5 FilterOperator OR, served since E3 as a union of bounded
// branches (translateOperator states the rule).
func TestQueryOrIsAUnionOfBoundedBranches(t *testing.T) {
	f := newFakeReaders()
	got := runQuery(t, f, `{"operator":"OR","conditions":[{"text":"ana"},{"text":"juan"}]}`)
	if len(got.or) != 2 {
		t.Fatalf("or = %d branches, want 2", len(got.or))
	}
	if got.or[0].text != "ana" || got.or[1].text != "juan" {
		t.Errorf("branches = %q/%q, want the two terms", got.or[0].text, got.or[1].text)
	}
	// Every branch carries the default exclusion in its own right: the
	// exclusion is a property of each search, not of the merge.
	for i, b := range got.or {
		if !b.defaultExclusion {
			t.Errorf("branch %d lost the default exclusion", i)
		}
	}
}

// An OR of one is that one — a client habit worth not punishing, and worth
// pinning so it stays on the single-search fast path rather than becoming a
// one-branch union.
func TestQueryOrOfOneIsUnwrapped(t *testing.T) {
	f := newFakeReaders()
	got := runQuery(t, f, `{"operator":"OR","conditions":[{"text":"solo"}]}`)
	if len(got.or) != 0 {
		t.Errorf("or = %d branches, want the filter unwrapped", len(got.or))
	}
	if got.text != "solo" {
		t.Errorf("text = %q, want the single condition", got.text)
	}
}

// A nested OR would multiply the branch count past the flat bound, so it is
// refused with the remedy named (§5.5's OR is associative, so flattening loses
// nothing).
func TestQueryNestedOrIsRefusedWithTheRemedy(t *testing.T) {
	f := newFakeReaders()
	merr := queryError(t, f, fmt.Sprintf(`{"accountId":%q,"filter":%s}`, testAccountJMAPID(),
		`{"operator":"OR","conditions":[{"text":"a"},{"operator":"OR","conditions":[{"text":"b"},{"text":"c"}]}]}`))
	if merr.Code != jmap.CodeUnsupportedFilter {
		t.Errorf("code = %q, want unsupportedFilter", merr.Code)
	}
	if !contains(merr.Description, "flatten") {
		t.Errorf("description %q does not name the remedy", merr.Description)
	}
}

// An OR inside an AND is refused rather than half-served, and the refusal names
// what would close it — which is the difference between a boundary and a
// mystery.
func TestQueryOrInsideAndIsRefusedByName(t *testing.T) {
	f := newFakeReaders()
	merr := queryError(t, f, fmt.Sprintf(`{"accountId":%q,"filter":%s}`, testAccountJMAPID(),
		`{"operator":"AND","conditions":[{"inMailbox":"m1"},{"operator":"OR","conditions":[{"text":"a"},{"text":"b"}]}]}`))
	if merr.Code != jmap.CodeUnsupportedFilter {
		t.Errorf("code = %q, want unsupportedFilter", merr.Code)
	}
	if !contains(merr.Description, "distribute") {
		t.Errorf("description %q does not name the remedy", merr.Description)
	}
}

// An AND of E3 conditions merges the way each condition's conjunction works:
// range bounds tighten, exclusion sets union, contradictions refuse.
func TestQueryAndMergesE3ConditionsCorrectly(t *testing.T) {
	f := newFakeReaders()
	got := runQuery(t, f, `{"operator":"AND","conditions":[
		{"inMailbox":"m1"},
		{"minSize":1000},
		{"minSize":5000},
		{"maxSize":900000},
		{"maxSize":100000}]}`)
	if got.minSize == nil || *got.minSize != 5000 {
		t.Errorf("minSize = %v, want the LARGER lower bound (the stricter one)", got.minSize)
	}
	if got.maxSize == nil || *got.maxSize != 100000 {
		t.Errorf("maxSize = %v, want the SMALLER upper bound (the stricter one)", got.maxSize)
	}
}

func TestQueryAndRefusesContradictoryHasAttachment(t *testing.T) {
	f := newFakeReaders()
	merr := queryError(t, f, fmt.Sprintf(`{"accountId":%q,"filter":%s}`, testAccountJMAPID(),
		`{"operator":"AND","conditions":[{"inMailbox":"m1"},{"hasAttachment":true},{"hasAttachment":false}]}`))
	if merr.Code != jmap.CodeUnsupportedFilter {
		t.Errorf("code = %q, want unsupportedFilter", merr.Code)
	}
}

// A mailbox id this server never issued names no mailbox. In inMailbox that is
// a refusal (the filter is unsatisfiable and saying so beats an empty list); in
// inMailboxOtherThan it is HARMLESS, because excluding a mailbox that does not
// exist excludes nothing — so the rest of the list must still apply.
func TestQueryUnknownExclusionIdIsDroppedNotRefused(t *testing.T) {
	f := newFakeReaders()
	got := runQuery(t, f, `{"text":"x","inMailboxOtherThan":["not-an-id","m1"]}`)
	if len(got.excludeMailboxIDs) != 1 {
		t.Errorf("excludeMailboxIDs = %v, want the one decodable id", got.excludeMailboxIDs)
	}
}

// D-7: the reach ceiling is the number the measurements support, and ADR §6's
// target. A test pins it because the value is a DECISION with evidence behind
// it (search.go carries the numbers), not a tuning knob — changing it silently
// would discard the measurement that justified it.
func TestMaxQueryReachIsTheSignedD7Ceiling(t *testing.T) {
	const adrTarget = 100000
	if MaxQueryReach != adrTarget {
		t.Errorf("MaxQueryReach = %d, want %d (ADR §6 target, D-7 resolved with the "+
			"measurements recorded at the constant)", MaxQueryReach, adrTarget)
	}
	// The owner's real account, which the old 10,000 ceiling cut off at 37%.
	const ownerAccountSize = 26869
	if MaxQueryReach < ownerAccountSize {
		t.Errorf("MaxQueryReach = %d cannot page the owner's %d-message account",
			MaxQueryReach, ownerAccountSize)
	}
}

// The bit values query.go uses for the system flags must be the store's own.
// They are restated in this package as untyped constants (search.go's rule
// keeps the translation layer off the store), so nothing but a test can catch a
// divergence — and a wrong bit would filter on the wrong flag SILENTLY, which
// is the worst failure available here.
func TestSystemFlagBitsAreTheOnesTheStoreUses(t *testing.T) {
	cases := map[string]uint64{
		KeywordSeen:     1 << 0,
		KeywordAnswered: 1 << 1,
		KeywordFlagged:  1 << 2,
		KeywordDraft:    1 << 4,
	}
	for kw, want := range cases {
		got, ok := systemFlagBit(kw)
		if !ok {
			t.Errorf("systemFlagBit(%q) reports no bit", kw)
			continue
		}
		if got != want {
			t.Errorf("systemFlagBit(%q) = %b, want %b", kw, got, want)
		}
	}
	if _, ok := systemFlagBit("$MoovL7"); ok {
		t.Error("a user label must not map to a system flag bit")
	}
}
