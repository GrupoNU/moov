package mail

import (
	"encoding/json"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Email/query with collapseThreads (RFC 8621 §4.4.3), L3 epic E1.
//
// The store's own collapse — the windowing, the cross-page dedupe, the SQL and
// its plan — is proven in internal/store/collapse_test.go. What is proven HERE
// is the layer above it: that the argument is honored rather than refused, that
// it reaches the collapsed repertoire rather than the flat one, that paging over
// a collapsed list obeys §5.5, and that the one refusal left says why in terms
// that cannot go stale.

// seedThreads gives the fake a corpus of n messages grouped into conversations
// of `perThread` consecutive messages, newest first, and returns the ids.
func seedThreads(f *fakeReaders, n, perThread int) []int64 {
	ids := seedSearch(f, n)
	f.hitThreads = make(map[int64]int64, n)
	for i, id := range ids {
		f.hitThreads[id] = ids[(i/perThread)*perThread]
	}
	return ids
}

// TestQueryCollapseThreadsIsServed is the retirement of a stale refusal.
//
// Until L3 epic E1 this argument was answered with unsupportedFilter citing
// "this server has no thread index yet" — true when it was written and false
// from migration 0004 onward. The test asserts the positive: the request
// succeeds, and it returns one id per conversation.
func TestQueryCollapseThreadsIsServed(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	ids := seedThreads(f, 12, 3) // 12 messages, 4 threads

	resp := query(t, f, `{"accountId":"`+testAccountJMAPID()+`","filter":{"inMailbox":"m1"},"collapseThreads":true}`)
	got := idsOf(t, resp)

	if len(got) != 4 {
		t.Fatalf("the collapsed query returned %d ids, want 4 (one per thread): %v", len(got), got)
	}
	// §4.4.3 keeps the FIRST matching email of each thread in the list's order,
	// which for the default newest-first sort is each thread's newest member —
	// the first id of each run in the seeded corpus.
	for i, want := range []int64{ids[0], ids[3], ids[6], ids[9]} {
		if got[i] != EncodeEmailID(want) {
			t.Errorf("id %d is %s, want %s (the thread's first matching message)",
				i, got[i], EncodeEmailID(want))
		}
	}
}

// TestQueryCollapseReachesTheCollapsedRepertoire proves the argument changes
// which STORE SHAPE is asked, not just what is filtered afterwards.
//
// A layer that collapsed the flat result in Go would pass the test above and
// would be the unbounded post-filter the store method exists to prevent — so
// the distinction is asserted directly, through fakes that answer differently.
func TestQueryCollapseReachesTheCollapsedRepertoire(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	seedThreads(f, 9, 3)

	flat := idsOf(t, query(t, f, `{"accountId":"`+testAccountJMAPID()+`","filter":{"inMailbox":"m1"}}`))
	collapsed := idsOf(t, query(t, f,
		`{"accountId":"`+testAccountJMAPID()+`","filter":{"inMailbox":"m1"},"collapseThreads":true}`))

	if len(flat) != 9 {
		t.Fatalf("the uncollapsed query returned %d ids, want 9", len(flat))
	}
	if len(collapsed) != 3 {
		t.Fatalf("the collapsed query returned %d ids, want 3", len(collapsed))
	}
	if collapsed[0] != flat[0] {
		t.Errorf("the first collapsed id is %s but the first flat id is %s; "+
			"the newest message must lead both lists", collapsed[0], flat[0])
	}
}

// TestQueryCollapsePagesByConversation pins §5.5's paging over a collapsed list:
// position and limit count CONVERSATIONS, not the messages behind them.
func TestQueryCollapsePagesByConversation(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	seedThreads(f, 20, 4) // 5 threads

	page1 := idsOf(t, query(t, f,
		`{"accountId":"`+testAccountJMAPID()+`","filter":{"inMailbox":"m1"},"collapseThreads":true,"limit":2}`))
	if len(page1) != 2 {
		t.Fatalf("page 1 has %d ids, want 2", len(page1))
	}

	resp := query(t, f,
		`{"accountId":"`+testAccountJMAPID()+`","filter":{"inMailbox":"m1"},"collapseThreads":true,"position":2,"limit":2}`)
	page2 := idsOf(t, resp)
	if len(page2) != 2 {
		t.Fatalf("page 2 has %d ids, want 2", len(page2))
	}
	if got := intField(t, resp, "position"); got != 2 {
		t.Errorf("position is %d, want 2", got)
	}

	// The two pages must be disjoint: position 2 over a COLLAPSED list means
	// "skip two conversations", and a layer that skipped two MESSAGES would
	// overlap here (the first four messages are all thread one).
	for _, a := range page1 {
		for _, b := range page2 {
			if a == b {
				t.Errorf("id %s appears on both pages; position counted messages rather than conversations", a)
			}
		}
	}

	// And the whole walk yields exactly the five conversations.
	all := idsOf(t, query(t, f,
		`{"accountId":"`+testAccountJMAPID()+`","filter":{"inMailbox":"m1"},"collapseThreads":true,"limit":50}`))
	if len(all) != 5 {
		t.Errorf("the full collapsed list has %d ids, want 5", len(all))
	}
}

// TestQueryCollapseTotalCountsConversations checks §5.5's `total` over a
// collapsed list: it is "the total number of Foos in the results", and the
// results of a collapsed query are CONVERSATIONS — so a folder of 12 messages
// in 3 threads has a total of 3, not 12.
func TestQueryCollapseTotalCountsConversations(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	seedThreads(f, 12, 4) // 3 threads

	// The limit is above the conversation count on purpose. `total` is only
	// exact when the search was not truncated, and a collapsed query truncated
	// at limit:1 genuinely does not know how many conversations lie beyond the
	// first — which is the property TestQueryCollapseTotalIsOmittedWhenTruncated
	// below pins from the other side.
	resp := query(t, f,
		`{"accountId":"`+testAccountJMAPID()+`","filter":{"inMailbox":"m1"},"collapseThreads":true,"calculateTotal":true,"limit":10}`)

	total, ok := resp["total"].(float64)
	if !ok {
		t.Fatalf("total is %T, want a number — the result set was not truncated, so it is exactly known",
			resp["total"])
	}
	if int(total) != 3 {
		t.Errorf("total is %d, want 3 (conversations, not the 12 messages behind them)", int(total))
	}
}

// TestQueryCollapseTotalIsOmittedWhenTruncated is the other half, and it pins a
// real bug this epic found.
//
// queryTotal used to test "was the result shorter than the WINDOW?" and nothing
// else. A request with limit:1 returns one id, which is trivially shorter than
// the 200-deep window, so the server answered total:1 for a mailbox holding
// thousands — a number §5.5 gives a load-bearing role ("If 'position' is >=
// 'total', this MUST be the empty list"), so a client paging on it would stop
// after the first page.
//
// It survived because every calculateTotal test used the default limit, where
// the requested depth and the window coincide. The bound is now the tighter of
// the two, and this asserts the case that was wrong.
func TestQueryCollapseTotalIsOmittedWhenTruncated(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	seedThreads(f, 12, 4) // 3 threads, and limit:1 cannot see past the first

	resp := query(t, f,
		`{"accountId":"`+testAccountJMAPID()+`","filter":{"inMailbox":"m1"},"collapseThreads":true,"calculateTotal":true,"limit":1}`)

	if v, present := resp["total"]; present {
		t.Errorf("total = %v; the search stopped at the requested depth, so the true count is "+
			"unknown and §5.5's total must be omitted rather than guessed", v)
	}
}

// TestQueryTotalRespectsTheRequestedDepth proves the same fix on the
// UNCOLLAPSED path, which is where the bug actually lived — collapse only
// surfaced it.
func TestQueryTotalRespectsTheRequestedDepth(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	seedSearch(f, 40)

	t.Run("omitted when the requested depth was filled", func(t *testing.T) {
		resp := query(t, f,
			`{"accountId":"`+testAccountJMAPID()+`","filter":{"inMailbox":"m1"},"calculateTotal":true,"limit":5}`)
		if v, present := resp["total"]; present {
			t.Errorf("total = %v, want it omitted: five ids out of a 40-message corpus says "+
				"nothing about the other 35", v)
		}
	})

	t.Run("exact when the corpus ran out first", func(t *testing.T) {
		resp := query(t, f,
			`{"accountId":"`+testAccountJMAPID()+`","filter":{"inMailbox":"m1"},"calculateTotal":true,"limit":100}`)
		total, ok := resp["total"].(float64)
		if !ok || int(total) != 40 {
			t.Errorf("total is %v, want 40", resp["total"])
		}
	})
}

// TestQueryCollapseWithTheKeywordSort proves the pair a real client sends still
// works when collapsed: Bulwark opens every folder with
// [hasKeyword $pinned, receivedAt], and the conversation list must open the same
// way or E1's UI half has nothing to render.
func TestQueryCollapseWithTheKeywordSort(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	ids := seedThreads(f, 9, 3)
	// Pin the newest message of the LAST thread, which is bottom of the list by
	// date. With the comparator it must lead.
	f.hits[6].hasKeyword = true

	resp := query(t, f, `{"accountId":"`+testAccountJMAPID()+`","filter":{"inMailbox":"m1"},`+
		`"collapseThreads":true,`+
		`"sort":[{"property":"hasKeyword","keyword":"$pinned","isAscending":false},`+
		`{"property":"receivedAt","isAscending":false}]}`)
	got := idsOf(t, resp)

	if len(got) != 3 {
		t.Fatalf("got %d collapsed ids, want 3: %v", len(got), got)
	}
	if got[0] != EncodeEmailID(ids[6]) {
		t.Errorf("the pinned conversation is at position %v, want first: %v",
			indexOf(got, EncodeEmailID(ids[6])), got)
	}
}

// TestQueryCollapseRefusesRelevanceWithAReasonThatCannotGoStale is the
// counterpart of the retired refusal.
//
// The old message named a MISSING FEATURE ("no thread index yet") and outlived
// it silently. This one names a structural property of the relevance sort — its
// order is not an index order, so it has no cursor — which cannot become false
// while the code stays as it is. The message is pinned so a future edit that
// weakens it fails here.
func TestQueryCollapseRefusesRelevanceWithAReasonThatCannotGoStale(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	seedThreads(f, 6, 2)

	merr := queryError(t, f, `{"accountId":"`+testAccountJMAPID()+`","filter":{"text":"factura"},`+
		`"collapseThreads":true,"sort":[{"property":"relevance","isAscending":false}]}`)

	if merr.Code != jmap.CodeUnsupportedSort {
		t.Errorf("got %s, want %s — it is the SORT that makes this unanswerable, not the filter",
			merr.Code, jmap.CodeUnsupportedSort)
	}
	for _, want := range []string{"collapseThreads", "relevance", "cursor", "receivedAt"} {
		if !contains(merr.Description, want) {
			t.Errorf("the refusal does not mention %q, so it does not tell the client what to do "+
				"instead: %q", want, merr.Description)
		}
	}
	// The retired reason must not come back.
	if contains(merr.Description, "thread index") {
		t.Errorf("the refusal still cites the absent thread index, which migration 0004 created: %q",
			merr.Description)
	}
}

// TestQueryCollapseIsServedForEveryFilterShapeTheRepertoireHas walks the three
// shapes a client actually sends, so a collapse that only worked for one folder
// view could not pass.
func TestQueryCollapseIsServedForEveryFilterShapeTheRepertoireHas(t *testing.T) {
	cases := []struct{ name, filter string }{
		{"inMailbox (the folder view)", `{"inMailbox":"m1"}`},
		{"filter:null (the account-wide listing)", `null`},
		{"text (full-text search)", `{"text":"factura"}`},
		{"inMailbox AND unread", `{"operator":"AND","conditions":[{"inMailbox":"m1"},{"notKeyword":"$seen"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeReaders{state: "1-1"}
			seedThreads(f, 8, 2)

			resp := query(t, f, `{"accountId":"`+testAccountJMAPID()+`","filter":`+c.filter+`,"collapseThreads":true}`)
			if got := idsOf(t, resp); len(got) != 4 {
				t.Errorf("got %d collapsed ids, want 4: %v", len(got), got)
			}
		})
	}
}

// TestQueryChangesStillDeclinesWithCollapse pins the ADR §2 decision against a
// plausible misreading: collapseThreads does not change what /queryChanges can
// do, and E1 did not quietly make it answerable.
//
// §5.6 would require the positional delta of a COLLAPSED list between two query
// states, which is strictly harder than the flat case this server already
// declines — the same message can enter and leave a collapsed list without
// itself changing at all, because a NEWER member of its thread arrived.
func TestQueryChangesStillDeclinesWithCollapse(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	seedThreads(f, 6, 2)

	_, merr := f.deps().handleEmailQueryChanges(callerCtx(), json.RawMessage(
		`{"accountId":"`+testAccountJMAPID()+`","sinceQueryState":"q1-1","filter":{"inMailbox":"m1"},"collapseThreads":true}`))
	if merr == nil {
		t.Fatal("Email/queryChanges answered a collapsed query; ADR §2 declines every one of them")
	}
	if merr.Code != jmap.CodeCannotCalculateChanges {
		t.Errorf("got %s, want %s", merr.Code, jmap.CodeCannotCalculateChanges)
	}
}

// TestQueryCollapseAdvertisesCanCalculateChangesFalse keeps the pre-announcement
// honest for collapsed queries too: §5.5's canCalculateChanges is what stops a
// conforming client from calling the method the test above proves refuses it.
func TestQueryCollapseAdvertisesCanCalculateChangesFalse(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	seedThreads(f, 4, 2)

	resp := query(t, f, `{"accountId":"`+testAccountJMAPID()+`","filter":{"inMailbox":"m1"},"collapseThreads":true}`)
	if v, ok := resp["canCalculateChanges"].(bool); !ok || v {
		t.Errorf("canCalculateChanges is %v, want false", resp["canCalculateChanges"])
	}
}

func indexOf(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}
