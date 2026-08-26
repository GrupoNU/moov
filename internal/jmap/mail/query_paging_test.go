package mail

import (
	"fmt"
	"testing"
)

// Email/query paging past the search window (regression, 2026-08-26).
//
// # The defect
//
// Email/query served `position` by SLICING one bounded fetch of
// DefaultSearchWindow (200) rows, so position:200 and position:400 both
// returned ZERO ids against a 626-message INBOX on the live pilot: every
// message past the 200th was unreachable by any client, through any argument.
// The `before` filter could not be used as a cursor either, because it was
// applied after the SQL LIMIT.
//
// The existing property test (TestQueryPagingVisitsEveryIDExactlyOnce) could
// not catch this: its largest corpus is 99, entirely inside the window. These
// tests are deliberately sized ACROSS the boundary.

// TestQueryReachesPastTheSearchWindow is the defect in its most direct form:
// the exact positions that returned nothing on the pilot must return ids.
func TestQueryReachesPastTheSearchWindow(t *testing.T) {
	const corpus = 626 // the pilot's INBOX at the time of the report
	f := newFakeReaders()
	want := seedSearch(f, corpus)

	for _, position := range []int{200, 400, 600} {
		t.Run(fmt.Sprintf("position=%d", position), func(t *testing.T) {
			resp := query(t, f, fmt.Sprintf(
				`{"accountId":%q,"filter":{"inMailbox":"m1"},"position":%d,"limit":10}`,
				testAccountJMAPID(), position))

			ids := idsOf(t, resp)
			if len(ids) == 0 {
				t.Fatalf("position %d returned no ids over a %d-message corpus: "+
					"messages past the search window are unreachable", position, corpus)
			}
			if p := intField(t, resp, "position"); p != position {
				t.Errorf("position = %d, want %d", p, position)
			}
			// The ids must be the RIGHT ones, not merely non-empty: a paging
			// fix that returns the first page for every position would satisfy
			// a non-emptiness check while showing the user the same mail
			// forever.
			if got, expect := ids[0], EncodeEmailID(want[position]); got != expect {
				t.Errorf("first id at position %d = %s, want %s", position, got, expect)
			}
		})
	}
}

// TestQueryPagingWalksACorpusLargerThanTheWindow is the full walk across the
// boundary: every id exactly once, in order, over a corpus several windows
// deep. It is the property test the original one could not express while its
// corpus fit inside a single window.
func TestQueryPagingWalksACorpusLargerThanTheWindow(t *testing.T) {
	for _, corpus := range []int{201, 350, 626} {
		for _, pageSize := range []int{50, 200} {
			t.Run(fmt.Sprintf("corpus=%d/page=%d", corpus, pageSize), func(t *testing.T) {
				f := newFakeReaders()
				want := seedSearch(f, corpus)

				var got []string
				for position := 0; ; position += pageSize {
					resp := query(t, f, fmt.Sprintf(
						`{"accountId":%q,"filter":{"inMailbox":"m1"},"position":%d,"limit":%d}`,
						testAccountJMAPID(), position, pageSize))
					page := idsOf(t, resp)
					if len(page) == 0 {
						break
					}
					got = append(got, page...)
					if len(page) < pageSize {
						break
					}
				}

				if len(got) != len(want) {
					t.Fatalf("walked %d ids, corpus has %d", len(got), len(want))
				}
				seen := make(map[string]bool, len(got))
				for i, id := range got {
					if seen[id] {
						t.Fatalf("id %s was returned on two different pages", id)
					}
					seen[id] = true
					if expect := EncodeEmailID(want[i]); id != expect {
						t.Fatalf("page walk[%d] = %s, want %s", i, id, expect)
					}
				}
			})
		}
	}
}

// TestQueryRequestsOnlyTheDepthItNeeds pins the cost model.
//
// Paging must not become "fetch everything and slice": the whole point of the
// keyset cursor is that a request costs position+limit rows, so the first page
// of a 26k-message mailbox stays as cheap as it was before paging existed. A
// regression here would not fail any correctness assertion — it would just make
// every query walk the ceiling — so it is asserted directly.
func TestQueryRequestsOnlyTheDepthItNeeds(t *testing.T) {
	f := newFakeReaders()
	seedSearch(f, 1000)

	query(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":"m1"},"position":0,"limit":50}`,
		testAccountJMAPID()))
	if f.lastReach != 50 {
		t.Errorf("the first page asked for a reach of %d, want 50", f.lastReach)
	}

	query(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":"m1"},"position":400,"limit":50}`,
		testAccountJMAPID()))
	if f.lastReach != 450 {
		t.Errorf("position 400 with limit 50 asked for a reach of %d, want 450", f.lastReach)
	}
}

// TestQueryReachIsBounded pins the ceiling: no single request may ask the
// database for unbounded work, which is the guarantee L2 §4.3 makes and that
// making depth a variable could otherwise have quietly removed.
func TestQueryReachIsBounded(t *testing.T) {
	f := newFakeReaders()
	seedSearch(f, 10)

	query(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":"m1"},"position":99999999,"limit":50}`,
		testAccountJMAPID()))
	if f.lastReach > MaxQueryReach {
		t.Errorf("an absurd position asked for a reach of %d, want at most %d",
			f.lastReach, MaxQueryReach)
	}
}
