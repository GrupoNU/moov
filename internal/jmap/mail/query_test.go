package mail

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Email/query over the fakes: the translation decisions, the paging algebra,
// and every refusal path. The store-backed behavior is in integration_test.go.

// query dispatches an Email/query and returns the decoded response.
func query(t *testing.T, f *fakeReaders, args string) map[string]any {
	t.Helper()
	result, merr := f.deps().handleEmailQuery(callerCtx(), json.RawMessage(args))
	if merr != nil {
		t.Fatalf("Email/query failed: %v", merr)
	}
	return reencode(t, result)
}

// queryError dispatches an Email/query expected to fail, returning the error.
func queryError(t *testing.T, f *fakeReaders, args string) *jmap.MethodError {
	t.Helper()
	result, merr := f.deps().handleEmailQuery(callerCtx(), json.RawMessage(args))
	if merr == nil {
		t.Fatalf("Email/query unexpectedly succeeded: %+v", result)
	}
	return merr
}

// reencode marshals a handler result and decodes it as generic JSON, so tests
// assert on the WIRE shape rather than on Go structs — which is what catches a
// mistyped json tag or a property that marshals as null when it must be [].
func reencode(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshaling result: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	return out
}

func idsOf(t *testing.T, resp map[string]any) []string {
	t.Helper()
	raw, ok := resp["ids"].([]any)
	if !ok {
		t.Fatalf("ids is %T, want an array", resp["ids"])
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("id is %T, want a string", v)
		}
		out = append(out, s)
	}
	return out
}

// intField reads a numeric response property, failing the test rather than
// panicking when it is absent or of the wrong type.
func intField(t *testing.T, resp map[string]any, key string) int {
	t.Helper()
	v, ok := resp[key].(float64)
	if !ok {
		t.Fatalf("%s is %T, want a number", key, resp[key])
	}
	return int(v)
}

// seedSearch gives the fake a corpus of n messages, newest first, and returns
// their ids in that order.
func seedSearch(f *fakeReaders, n int) []int64 {
	ids := make([]int64, 0, n)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for i := range n {
		id := int64(i + 1)
		ids = append(ids, id)
		f.hits = append(f.hits, searchHit{
			id: id,
			// Descending in time as the id grows, so the natural newest-first
			// order is exactly the id order — which makes the paging
			// assertions readable.
			date: base.Add(-time.Duration(i) * time.Minute),
		})
	}
	return ids
}

// ---------------------------------------------------------------------------
// filter translation
// ---------------------------------------------------------------------------

func TestQueryFilterShapesTheRepertoireServes(t *testing.T) {
	cases := []struct {
		name   string
		filter string
		want   searchFilter
	}{{
		name:   "inMailbox alone is the folder view",
		filter: `{"inMailbox":"m1"}`,
		want:   searchFilter{mailboxID: ptr(int64(1))},
	}, {
		name:   "text alone is the FTS shape",
		filter: `{"text":"factura"}`,
		want:   searchFilter{text: "factura"},
	}, {
		name:   "from is answered as full text (documented over-match)",
		filter: `{"from":"alice@example.com","inMailbox":"m1"}`,
		want:   searchFilter{text: "alice@example.com", mailboxID: ptr(int64(1))},
	}, {
		name:   "subject is answered as full text",
		filter: `{"subject":"invoice","inMailbox":"m2"}`,
		want:   searchFilter{text: "invoice", mailboxID: ptr(int64(2))},
	}, {
		name:   "notKeyword $seen is the unread filter",
		filter: `{"inMailbox":"m1","notKeyword":"$seen"}`,
		want:   searchFilter{mailboxID: ptr(int64(1)), unreadOnly: true},
	}, {
		name:   "a user keyword is a label filter (A6)",
		filter: `{"text":"x","hasKeyword":"proyecto"}`,
		want:   searchFilter{text: "x", keyword: "proyecto"},
	}, {
		name:   "after is the inclusive lower date bound",
		filter: `{"inMailbox":"m1","after":"2026-08-01T00:00:00Z"}`,
		want: searchFilter{
			mailboxID: ptr(int64(1)),
			since:     ptr(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)),
		},
	}, {
		name:   "an AND of supported conditions is itself supported",
		filter: `{"operator":"AND","conditions":[{"text":"pago"},{"inMailbox":"m3"},{"notKeyword":"$seen"}]}`,
		want:   searchFilter{text: "pago", mailboxID: ptr(int64(3)), unreadOnly: true},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, merr := translateFilter(json.RawMessage(tc.filter))
			if merr != nil {
				t.Fatalf("translateFilter: %v", merr)
			}
			if got.text != tc.want.text {
				t.Errorf("text = %q, want %q", got.text, tc.want.text)
			}
			if !eqPtr(got.mailboxID, tc.want.mailboxID) {
				t.Errorf("mailboxID = %v, want %v", deref(got.mailboxID), deref(tc.want.mailboxID))
			}
			if got.unreadOnly != tc.want.unreadOnly {
				t.Errorf("unreadOnly = %v, want %v", got.unreadOnly, tc.want.unreadOnly)
			}
			if got.keyword != tc.want.keyword {
				t.Errorf("keyword = %q, want %q", got.keyword, tc.want.keyword)
			}
			if (got.since == nil) != (tc.want.since == nil) {
				t.Errorf("since = %v, want %v", got.since, tc.want.since)
			} else if got.since != nil && !got.since.Equal(*tc.want.since) {
				t.Errorf("since = %v, want %v", *got.since, *tc.want.since)
			}
		})
	}
}

// Everything outside the repertoire must be REFUSED, and the refusal must name
// the node — §5.5 tells the client to "suggest that the user simplify their
// search", which it can only do if it knows which part failed.
func TestQueryUnsupportedFiltersAreRefusedByName(t *testing.T) {
	cases := []struct {
		name      string
		filter    string
		mustName  string
		errorCode jmap.ErrorCode
	}{{
		name:      "OR is not a validated shape",
		filter:    `{"operator":"OR","conditions":[{"text":"a"},{"text":"b"}]}`,
		mustName:  "OR",
		errorCode: jmap.CodeUnsupportedFilter,
	}, {
		name:      "NOT is not a validated shape",
		filter:    `{"operator":"NOT","conditions":[{"text":"a"}]}`,
		mustName:  "NOT",
		errorCode: jmap.CodeUnsupportedFilter,
	}, {
		name:      "body-only search has no per-field index",
		filter:    `{"body":"contenido"}`,
		mustName:  "body",
		errorCode: jmap.CodeUnsupportedFilter,
	}, {
		name:      "header filters have no index",
		filter:    `{"header":["X-Spam"]}`,
		mustName:  "header",
		errorCode: jmap.CodeUnsupportedFilter,
	}, {
		name:      "hasAttachment is a store gap, not a silent pass",
		filter:    `{"inMailbox":"m1","hasAttachment":true}`,
		mustName:  "hasAttachment",
		errorCode: jmap.CodeUnsupportedFilter,
	}, {
		name:      "minSize has no index",
		filter:    `{"inMailbox":"m1","minSize":1000}`,
		mustName:  "minSize",
		errorCode: jmap.CodeUnsupportedFilter,
	}, {
		name:      "inMailboxOtherThan needs a negated mailbox predicate",
		filter:    `{"inMailboxOtherThan":["m1"]}`,
		mustName:  "inMailboxOtherThan",
		errorCode: jmap.CodeUnsupportedFilter,
	}, {
		name:      "thread keyword conditions need the thread index",
		filter:    `{"someInThreadHaveKeyword":"$flagged"}`,
		mustName:  "someInThreadHaveKeyword",
		errorCode: jmap.CodeUnsupportedFilter,
	}, {
		name:      "$flagged is a bitmask flag with no predicate",
		filter:    `{"text":"x","hasKeyword":"$flagged"}`,
		mustName:  "$flagged",
		errorCode: jmap.CodeUnsupportedFilter,
	}, {
		name:      "a date-only filter cannot be enumerated",
		filter:    `{"after":"2026-01-01T00:00:00Z"}`,
		mustName:  "inMailbox",
		errorCode: jmap.CodeUnsupportedFilter,
	}, {
		name:      "a null filter would be the whole account",
		filter:    `null`,
		mustName:  "inMailbox",
		errorCode: jmap.CodeUnsupportedFilter,
	}}

	f := newFakeReaders()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merr := queryError(t, f, fmt.Sprintf(
				`{"accountId":%q,"filter":%s}`, testAccountJMAPID(), tc.filter))
			if merr.Code != tc.errorCode {
				t.Errorf("code = %q, want %q", merr.Code, tc.errorCode)
			}
			if !contains(merr.Description, tc.mustName) {
				t.Errorf("description %q does not name %q", merr.Description, tc.mustName)
			}
		})
	}
}

// A contradiction the repertoire cannot express must be refused rather than
// resolved: "in mailbox A AND in mailbox B" matches nothing, and answering
// with one of them would return mail the user excluded.
func TestQueryContradictoryAndIsRefused(t *testing.T) {
	f := newFakeReaders()
	merr := queryError(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"operator":"AND","conditions":[{"inMailbox":"m1"},{"inMailbox":"m2"}]}}`,
		testAccountJMAPID()))
	if merr.Code != jmap.CodeUnsupportedFilter {
		t.Errorf("code = %q, want unsupportedFilter", merr.Code)
	}
}

// ---------------------------------------------------------------------------
// sort translation
// ---------------------------------------------------------------------------

func TestQuerySortSupportAndRefusals(t *testing.T) {
	cases := []struct {
		name string
		sort string
		want *sortSpec // nil means the sort must be refused
	}{{
		name: "no sort defaults to newest first",
		sort: `null`,
		want: &sortSpec{ascending: false},
	}, {
		name: "receivedAt descending is the native shape",
		sort: `[{"property":"receivedAt","isAscending":false}]`,
		want: &sortSpec{ascending: false},
	}, {
		name: "receivedAt ascending reverses the bounded window",
		sort: `[{"property":"receivedAt","isAscending":true}]`,
		want: &sortSpec{ascending: true},
	}, {
		name: "relevance is the bounded opt-in",
		sort: `[{"property":"relevance","isAscending":false}]`,
		want: &sortSpec{byRelevance: true},
	}, {
		name: "size is a SHOULD this server does not implement",
		sort: `[{"property":"size"}]`,
		want: nil,
	}, {
		name: "subject would need a collated index",
		sort: `[{"property":"subject"}]`,
		want: nil,
	}, {
		name: "a multi-key sort cannot be honored by one ORDER BY",
		sort: `[{"property":"receivedAt"},{"property":"size"}]`,
		want: nil,
	}, {
		name: "an explicit collation is refused, matching the empty advertisement",
		sort: `[{"property":"receivedAt","collation":"i;unicode-casemap"}]`,
		want: nil,
	}, {
		name: "ascending relevance is not a thing to serve",
		sort: `[{"property":"relevance","isAscending":true}]`,
		want: nil,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var comparators []comparator
			if tc.sort != "null" {
				if err := json.Unmarshal([]byte(tc.sort), &comparators); err != nil {
					t.Fatalf("bad test sort: %v", err)
				}
			}
			got, merr := translateSort(comparators)
			if tc.want == nil {
				if merr == nil {
					t.Fatalf("sort was accepted, want unsupportedSort")
				}
				if merr.Code != jmap.CodeUnsupportedSort {
					t.Errorf("code = %q, want unsupportedSort", merr.Code)
				}
				return
			}
			if merr != nil {
				t.Fatalf("translateSort: %v", merr)
			}
			if got != *tc.want {
				t.Errorf("sortSpec = %+v, want %+v", got, *tc.want)
			}
		})
	}
}

// The relevance sort ranks text; asking for it without text has nothing to
// rank, and must fail loudly rather than quietly become a date sort.
func TestQueryRelevanceWithoutTextIsRefused(t *testing.T) {
	f := newFakeReaders()
	merr := queryError(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":"m1"},"sort":[{"property":"relevance","isAscending":false}]}`,
		testAccountJMAPID()))
	if merr.Code != jmap.CodeUnsupportedSort {
		t.Errorf("code = %q, want unsupportedSort", merr.Code)
	}
}

// The advertised sort options and the ones translateSort accepts must be the
// same set — J1's declared == applied rule, extended to comparators. This test
// is the mechanical link between session.go's array and this package.
func TestAdvertisedSortOptionsAreExactlyTheImplementedOnes(t *testing.T) {
	advertised := []string{SortReceivedAt, SortRelevance}

	for _, property := range advertised {
		c := comparator{Property: property}
		if property == SortRelevance {
			asc := false
			c.IsAscending = &asc
		}
		if _, merr := translateSort([]comparator{c}); merr != nil {
			t.Errorf("advertised sort %q is refused by the handler: %v", property, merr)
		}
	}

	// And nothing outside the list is quietly accepted.
	for _, property := range []string{"size", "from", "to", "subject", "sentAt", "hasKeyword", "id"} {
		if _, merr := translateSort([]comparator{{Property: property}}); merr == nil {
			t.Errorf("sort %q is accepted but not advertised", property)
		}
	}
}

// ---------------------------------------------------------------------------
// paging
// ---------------------------------------------------------------------------

// Walking a static corpus page by page must visit every id exactly once, in
// order — the property that makes a message list correct. This is the
// property-based paging test the epic asks for: it runs every page size
// against every corpus size rather than one hand-picked pair.
func TestQueryPagingVisitsEveryIDExactlyOnce(t *testing.T) {
	for _, corpus := range []int{0, 1, 7, 50, 99} {
		for _, pageSize := range []int{1, 3, 10, 50} {
			t.Run(fmt.Sprintf("corpus=%d/page=%d", corpus, pageSize), func(t *testing.T) {
				f := newFakeReaders()
				want := seedSearch(f, corpus)

				var got []string
				for position := 0; ; position += pageSize {
					resp := query(t, f, fmt.Sprintf(
						`{"accountId":%q,"filter":{"inMailbox":"m1"},"position":%d,"limit":%d}`,
						testAccountJMAPID(), position, pageSize))

					// §5.5: position in the response is "the zero-based index
					// of the first result in the 'ids' array within the
					// complete list of query results".
					if p := intField(t, resp, "position"); p != position && p != corpus {
						t.Fatalf("position = %d, want %d", p, position)
					}
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
				for i, id := range want {
					if got[i] != EncodeEmailID(id) {
						t.Errorf("page walk[%d] = %s, want %s", i, got[i], EncodeEmailID(id))
					}
				}
			})
		}
	}
}

// §5.5: "If a negative value is given, it is an offset from the end of the
// list. Specifically, the negative value MUST be added to the total number of
// results given the filter, and if still negative, it's clamped to 0."
func TestQueryNegativePositionCountsFromTheEnd(t *testing.T) {
	f := newFakeReaders()
	ids := seedSearch(f, 10)

	cases := []struct {
		position int
		wantFrom int // index into ids
	}{
		{position: -1, wantFrom: 9},
		{position: -3, wantFrom: 7},
		{position: -10, wantFrom: 0},
		{position: -25, wantFrom: 0}, // clamped
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("position=%d", tc.position), func(t *testing.T) {
			resp := query(t, f, fmt.Sprintf(
				`{"accountId":%q,"filter":{"inMailbox":"m1"},"position":%d}`,
				testAccountJMAPID(), tc.position))

			got := idsOf(t, resp)
			want := ids[tc.wantFrom:]
			if len(got) != len(want) {
				t.Fatalf("got %d ids, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != EncodeEmailID(want[i]) {
					t.Errorf("ids[%d] = %s, want %s", i, got[i], EncodeEmailID(want[i]))
				}
			}
			if p := intField(t, resp, "position"); p != tc.wantFrom {
				t.Errorf("position = %d, want %d", p, tc.wantFrom)
			}
		})
	}
}

// §5.5: "If the index is greater than or equal to the total number of objects
// in the results list, then the 'ids' array in the response will be empty, but
// this is not an error."
func TestQueryPositionPastTheEndIsEmptyNotAnError(t *testing.T) {
	f := newFakeReaders()
	seedSearch(f, 5)

	resp := query(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":"m1"},"position":99}`, testAccountJMAPID()))
	if ids := idsOf(t, resp); len(ids) != 0 {
		t.Errorf("ids = %v, want empty", ids)
	}
}

// Anchor paging must agree with position paging over the same corpus: the
// anchor's index IS a position, per §5.5 ("This index is now used exactly as
// though it were supplied as the 'position' argument").
func TestQueryAnchorResolvesToTheSamePageAsPosition(t *testing.T) {
	f := newFakeReaders()
	ids := seedSearch(f, 20)

	for _, idx := range []int{0, 1, 9, 19} {
		for _, offset := range []int{0, -1, 2} {
			t.Run(fmt.Sprintf("anchor=%d/offset=%d", idx, offset), func(t *testing.T) {
				byAnchor := query(t, f, fmt.Sprintf(
					`{"accountId":%q,"filter":{"inMailbox":"m1"},"anchor":%q,"anchorOffset":%d,"limit":3}`,
					testAccountJMAPID(), EncodeEmailID(ids[idx]), offset))

				want := idx + offset
				if want < 0 {
					want = 0 // §5.5: "If the resulting index is now negative, it is clamped to 0."
				}
				byPosition := query(t, f, fmt.Sprintf(
					`{"accountId":%q,"filter":{"inMailbox":"m1"},"position":%d,"limit":3}`,
					testAccountJMAPID(), want))

				a, p := idsOf(t, byAnchor), idsOf(t, byPosition)
				if len(a) != len(p) {
					t.Fatalf("anchor page has %d ids, position page has %d", len(a), len(p))
				}
				for i := range a {
					if a[i] != p[i] {
						t.Errorf("ids[%d]: anchor gave %s, position gave %s", i, a[i], p[i])
					}
				}
			})
		}
	}
}

// §5.5: "If an 'anchor' is specified, any position argument supplied by the
// client MUST be ignored."
func TestQueryAnchorOverridesPosition(t *testing.T) {
	f := newFakeReaders()
	ids := seedSearch(f, 10)

	resp := query(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":"m1"},"anchor":%q,"position":7,"limit":2}`,
		testAccountJMAPID(), EncodeEmailID(ids[3])))

	if p := intField(t, resp, "position"); p != 3 {
		t.Errorf("position = %d, want 3 (the anchor's index, not the ignored position)", p)
	}
}

// §5.5: "If the anchor is not found, the call is rejected with an
// 'anchorNotFound' error." Both an id outside the corpus and one this server
// could never have issued are "not found".
func TestQueryAnchorNotFound(t *testing.T) {
	f := newFakeReaders()
	seedSearch(f, 5)

	for _, anchor := range []string{EncodeEmailID(999), "not-an-id", "m1"} {
		t.Run(anchor, func(t *testing.T) {
			merr := queryError(t, f, fmt.Sprintf(
				`{"accountId":%q,"filter":{"inMailbox":"m1"},"anchor":%q}`,
				testAccountJMAPID(), anchor))
			if merr.Code != jmap.CodeAnchorNotFound {
				t.Errorf("code = %q, want anchorNotFound", merr.Code)
			}
		})
	}
}

// The window bound must be VISIBLE to the client, through the `limit` property
// §5.5 provides for exactly this ("This is only returned if the server set a
// limit or used a different limit than that given in the request").
func TestQueryServerLimitIsReportedWhenItBinds(t *testing.T) {
	f := newFakeReaders()
	f.searchWindow = 10
	seedSearch(f, 10)

	t.Run("no limit given is clamped and reported", func(t *testing.T) {
		resp := query(t, f, fmt.Sprintf(
			`{"accountId":%q,"filter":{"inMailbox":"m1"}}`, testAccountJMAPID()))
		if resp["limit"] == nil {
			t.Error("limit was not reported although the server imposed one")
		}
	})

	t.Run("a limit within the window is not overridden", func(t *testing.T) {
		resp := query(t, f, fmt.Sprintf(
			`{"accountId":%q,"filter":{"inMailbox":"m1"},"limit":3}`, testAccountJMAPID()))
		if resp["limit"] != nil {
			t.Errorf("limit = %v, want it omitted when the client's limit stands", resp["limit"])
		}
		if got := idsOf(t, resp); len(got) != 3 {
			t.Errorf("got %d ids, want 3", len(got))
		}
	})
}

// ---------------------------------------------------------------------------
// total
// ---------------------------------------------------------------------------

// The judgment call of this epic, pinned by tests: `total` is EXACT when the
// window was not filled, and OMITTED when it was — never the capped number,
// because §5.5 defines total as "The total number of Foos in the results
// (given the 'filter')".
func TestQueryTotalIsExactOrOmittedNeverCapped(t *testing.T) {
	t.Run("omitted when calculateTotal is false", func(t *testing.T) {
		f := newFakeReaders()
		seedSearch(f, 5)
		resp := query(t, f, fmt.Sprintf(
			`{"accountId":%q,"filter":{"inMailbox":"m1"}}`, testAccountJMAPID()))
		if _, present := resp["total"]; present {
			// §5.5: "This argument MUST be omitted if the 'calculateTotal'
			// request argument is not true."
			t.Error("total is present although calculateTotal was not requested")
		}
	})

	t.Run("exact when the window was not filled", func(t *testing.T) {
		f := newFakeReaders()
		f.searchWindow = 10
		seedSearch(f, 4)
		resp := query(t, f, fmt.Sprintf(
			`{"accountId":%q,"filter":{"inMailbox":"m1"},"calculateTotal":true}`, testAccountJMAPID()))
		total, present := resp["total"]
		if !present {
			t.Fatal("total is missing although it is exactly computable")
		}
		if n, ok := total.(float64); !ok || int(n) != 4 {
			t.Errorf("total = %v, want 4", total)
		}
	})

	t.Run("omitted rather than capped when the window filled", func(t *testing.T) {
		f := newFakeReaders()
		f.searchWindow = 10
		seedSearch(f, 10) // exactly the window: the true total is unknown
		resp := query(t, f, fmt.Sprintf(
			`{"accountId":%q,"filter":{"inMailbox":"m1"},"calculateTotal":true}`, testAccountJMAPID()))
		if v, present := resp["total"]; present {
			t.Errorf("total = %v; a filled window means the true total is unknown and it must be omitted", v)
		}
	})
}

// ---------------------------------------------------------------------------
// response invariants
// ---------------------------------------------------------------------------

func TestQueryResponseInvariants(t *testing.T) {
	f := newFakeReaders()
	seedSearch(f, 3)
	resp := query(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":"m1"}}`, testAccountJMAPID()))

	// canCalculateChanges must be false: this server answers every
	// /queryChanges with cannotCalculateChanges, and §5.5 makes this the
	// advance warning that lets a conforming client not bother.
	if resp["canCalculateChanges"] != false {
		t.Errorf("canCalculateChanges = %v, want false", resp["canCalculateChanges"])
	}
	// queryState must not be confusable with the /get state a client hands to
	// Email/changes: they are different cursors.
	qs, _ := resp["queryState"].(string)
	if qs == "" {
		t.Error("queryState is empty")
	}
	if qs == f.state {
		t.Errorf("queryState %q is identical to the /get state; the two must not be interchangeable", qs)
	}
	// An empty result must marshal as [], never null.
	empty := query(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":"m1"},"position":99}`, testAccountJMAPID()))
	if _, ok := empty["ids"].([]any); !ok {
		t.Errorf("ids = %v (%T), want an empty array", empty["ids"], empty["ids"])
	}
}

// Account scoping: a query naming another account is refused before any read.
func TestQueryRefusesAnotherAccount(t *testing.T) {
	f := newFakeReaders()
	merr := queryError(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":"m1"}}`, jmap.EncodeAccountID(otherAccountID)))
	if merr.Code != jmap.CodeAccountNotFound {
		t.Errorf("code = %q, want accountNotFound", merr.Code)
	}
}

func TestQueryRefusesWithoutCaller(t *testing.T) {
	f := newFakeReaders()
	_, merr := f.deps().handleEmailQuery(contextNoCaller{}, json.RawMessage(
		fmt.Sprintf(`{"accountId":%q,"filter":{"inMailbox":"m1"}}`, testAccountJMAPID())))
	if merr == nil || merr.Code != jmap.CodeForbidden {
		t.Errorf("error = %v, want forbidden", merr)
	}
}

// collapseThreads is refused rather than ignored: silently returning a list
// with duplicate threads is a wrong answer to the question asked.
func TestQueryCollapseThreadsIsRefused(t *testing.T) {
	f := newFakeReaders()
	merr := queryError(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":"m1"},"collapseThreads":true}`, testAccountJMAPID()))
	if merr.Code != jmap.CodeUnsupportedFilter {
		t.Errorf("code = %q, want unsupportedFilter", merr.Code)
	}
}

// An internal reader failure must not leak database text to the client.
func TestQueryDoesNotLeakInternalErrors(t *testing.T) {
	f := newFakeReaders()
	f.err = errSecret
	merr := queryError(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":"m1"}}`, testAccountJMAPID()))
	if contains(merr.Description, "constraint") || contains(merr.Description, "pq:") {
		t.Errorf("description leaks internals: %q", merr.Description)
	}
}

func ptr[T any](v T) *T { return &v }

func eqPtr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func deref(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
