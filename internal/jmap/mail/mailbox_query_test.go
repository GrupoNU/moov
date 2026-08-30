package mail

import (
	"encoding/json"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Mailbox/query — RFC 8621 §2.3, registered in L3 epic E1.
//
// Before E1 this method answered `unknownMethod`, which a client reads as "this
// server is partial" rather than "this server does not do that". These tests
// hold it to the two things that matter: the order matches Mailbox/get's (so a
// client calling both sees ONE folder list), and every condition it accepts is
// exact while every condition it refuses says why.

// mailboxQuery dispatches a Mailbox/query and returns the decoded response.
func mailboxQuery(t *testing.T, f *fakeReaders, args string) map[string]any {
	t.Helper()
	result, merr := f.deps().handleMailboxQuery(callerCtx(), json.RawMessage(args))
	if merr != nil {
		t.Fatalf("Mailbox/query failed: %v", merr)
	}
	return reencode(t, result)
}

// mailboxQueryError dispatches a Mailbox/query expected to fail.
func mailboxQueryError(t *testing.T, f *fakeReaders, args string) *jmap.MethodError {
	t.Helper()
	result, merr := f.deps().handleMailboxQuery(callerCtx(), json.RawMessage(args))
	if merr == nil {
		t.Fatalf("Mailbox/query unexpectedly succeeded: %+v", result)
	}
	return merr
}

// seedMailboxTree gives the fake a folder tree with roles, a child, and an
// unsubscribed folder — enough for every §2.3 condition to have both a match
// and a non-match, which is what makes a passing filter test meaningful.
func seedMailboxTree(f *fakeReaders) {
	f.mailboxes = map[int64][]MailboxRow{
		testAccountID: {
			{ID: 1, Name: "INBOX", Role: "inbox", SortOrder: 10, IsSubscribed: true},
			{ID: 2, Name: "Drafts", Role: "drafts", SortOrder: 20, IsSubscribed: true},
			{ID: 3, Name: "Sent", Role: "sent", SortOrder: 30, IsSubscribed: true},
			{ID: 4, Name: "Work", SortOrder: 100, IsSubscribed: true},
			{ID: 5, Name: "2026", ParentID: 4, SortOrder: 100, IsSubscribed: true},
			{ID: 6, Name: "Archivo viejo", SortOrder: 100, IsSubscribed: false},
		},
	}
}

// TestMailboxQueryDefaultOrderMatchesMailboxGet is the property a client
// actually depends on: §5.5 lets the default order be "server dependent", and
// the only defensible choice is the one Mailbox/get already returns — otherwise
// a client that calls both renders the folder list in two different orders.
func TestMailboxQueryDefaultOrderMatchesMailboxGet(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	seedMailboxTree(f)

	resp := mailboxQuery(t, f, `{"accountId":"`+testAccountJMAPID()+`"}`)
	got := idsOf(t, resp)

	// sortOrder ascending, name breaking ties: inbox(10), drafts(20), sent(30),
	// then the three sortOrder-100 folders by name — "2026" < "archivo viejo" <
	// "work" once folded.
	want := []string{
		EncodeMailboxID(1), EncodeMailboxID(2), EncodeMailboxID(3),
		EncodeMailboxID(5), EncodeMailboxID(6), EncodeMailboxID(4),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d ids, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("id %d is %s, want %s (sortOrder then name)", i, got[i], want[i])
		}
	}
}

// TestMailboxQueryFilters walks every §2.3 condition this server serves, each
// with a match and a non-match in the tree.
func TestMailboxQueryFilters(t *testing.T) {
	cases := []struct {
		name   string
		filter string
		want   []int64
	}{{
		// §2.3 parentId: the id of the parent, or null for the top level.
		name:   "parentId names one folder's children",
		filter: `{"parentId":"` + EncodeMailboxID(4) + `"}`,
		want:   []int64{5},
	}, {
		name:   "parentId:null is the top level, not the absence of a condition",
		filter: `{"parentId":null}`,
		want:   []int64{1, 2, 3, 6, 4},
	}, {
		name:   "role selects one role folder",
		filter: `{"role":"drafts"}`,
		want:   []int64{2},
	}, {
		// §2.3 role: null — "Mailboxes with no role".
		name:   "role:null is the folders with no role",
		filter: `{"role":null}`,
		want:   []int64{5, 6, 4},
	}, {
		name:   "hasAnyRole:true is the role folders",
		filter: `{"hasAnyRole":true}`,
		want:   []int64{1, 2, 3},
	}, {
		name:   "hasAnyRole:false is their complement",
		filter: `{"hasAnyRole":false}`,
		want:   []int64{5, 6, 4},
	}, {
		name:   "isSubscribed:false finds the unsubscribed folder",
		filter: `{"isSubscribed":false}`,
		want:   []int64{6},
	}, {
		// §4.4.1's rule, inherited: "If multiple properties are specified, ALL
		// must apply".
		name:   "two properties in one condition are ANDed",
		filter: `{"hasAnyRole":false,"isSubscribed":true}`,
		want:   []int64{5, 4},
	}, {
		name:   "an AND operator is the same conjunction",
		filter: `{"operator":"AND","conditions":[{"parentId":null},{"hasAnyRole":false}]}`,
		want:   []int64{6, 4},
	}, {
		// §5.5: "If null, all objects in the account of this type are included."
		name:   "filter:null is the whole folder list",
		filter: `null`,
		want:   []int64{1, 2, 3, 5, 6, 4},
	}, {
		// §4.4.1: "If zero properties are specified ... the condition MUST always
		// evaluate to true." Unlike Email/query, where that means an enumeration
		// the repertoire cannot do, here it is simply the folder list.
		name:   "an empty condition matches everything",
		filter: `{}`,
		want:   []int64{1, 2, 3, 5, 6, 4},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeReaders{state: "1-1"}
			seedMailboxTree(f)

			resp := mailboxQuery(t, f, `{"accountId":"`+testAccountJMAPID()+`","filter":`+c.filter+`}`)
			got := idsOf(t, resp)

			want := make([]string, 0, len(c.want))
			for _, id := range c.want {
				want = append(want, EncodeMailboxID(id))
			}
			if len(got) != len(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("got %v, want %v", got, want)
				}
			}
		})
	}
}

// TestMailboxQueryRefusals holds every refusal to naming what it refused, so a
// client can act on it — §5.5's own rationale for unsupportedFilter ("the client
// can then suggest that the user simplify their search").
func TestMailboxQueryRefusals(t *testing.T) {
	cases := []struct {
		name    string
		args    string
		code    jmap.ErrorCode
		mention string
	}{{
		// §2.3's name condition is a substring test over a name whose JMAP form
		// is the LEAF only. It is refused rather than approximated — the
		// reasoning is on translateMailboxCondition — and the refusal must name
		// the alternatives.
		name:    "the name condition is refused, naming what to use instead",
		args:    `{"filter":{"name":"Work"}}`,
		code:    jmap.CodeUnsupportedFilter,
		mention: "parentId",
	}, {
		name:    "OR is refused by name",
		args:    `{"filter":{"operator":"OR","conditions":[{"role":"inbox"},{"role":"sent"}]}}`,
		code:    jmap.CodeUnsupportedFilter,
		mention: "OR",
	}, {
		name:    "NOT is refused by name",
		args:    `{"filter":{"operator":"NOT","conditions":[{"role":"inbox"}]}}`,
		code:    jmap.CodeUnsupportedFilter,
		mention: "NOT",
	}, {
		name:    "an unknown condition is refused, not ignored",
		args:    `{"filter":{"totalEmails":5}}`,
		code:    jmap.CodeUnsupportedFilter,
		mention: "totalEmails",
	}, {
		// §2.3's sortAsTree/filterAsTree: real, implementable, and NOT
		// implemented — so declined rather than accepted-and-ignored, which
		// would render the hierarchy wrong.
		name:    "sortAsTree is declined by name",
		args:    `{"sortAsTree":true}`,
		code:    jmap.CodeUnsupportedFilter,
		mention: "sortAsTree",
	}, {
		name:    "filterAsTree is declined by name",
		args:    `{"filterAsTree":true}`,
		code:    jmap.CodeUnsupportedFilter,
		mention: "filterAsTree",
	}, {
		name:    "an unsortable property is refused, naming the ones served",
		args:    `{"sort":[{"property":"totalEmails"}]}`,
		code:    jmap.CodeUnsupportedSort,
		mention: "sortOrder",
	}, {
		// The name sort compares strings, so a collation is meaningful — which
		// makes refusing it more important, not less: session.go advertises no
		// collation algorithms and this server folds case with Go's own rules.
		name:    "a named collation is refused because none is advertised",
		args:    `{"sort":[{"property":"name","collation":"i;ascii-casemap"}]}`,
		code:    jmap.CodeUnsupportedSort,
		mention: "collation",
	}, {
		name:    "two comparators are refused",
		args:    `{"sort":[{"property":"sortOrder"},{"property":"name"}]}`,
		code:    jmap.CodeUnsupportedSort,
		mention: "single",
	}, {
		name:    "contradictory conditions are refused rather than silently resolved",
		args:    `{"filter":{"operator":"AND","conditions":[{"role":"inbox"},{"role":"sent"}]}}`,
		code:    jmap.CodeUnsupportedFilter,
		mention: "role",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeReaders{state: "1-1"}
			seedMailboxTree(f)

			// Splice the accountId into whatever the case supplies.
			args := `{"accountId":"` + testAccountJMAPID() + `",` + c.args[1:]
			merr := mailboxQueryError(t, f, args)

			if merr.Code != c.code {
				t.Errorf("got %s, want %s (%q)", merr.Code, c.code, merr.Description)
			}
			if !contains(merr.Description, c.mention) {
				t.Errorf("the refusal does not mention %q, so the client cannot act on it: %q",
					c.mention, merr.Description)
			}
		})
	}
}

// TestMailboxQuerySorts covers the two orders this server produces, in both
// directions.
func TestMailboxQuerySorts(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	seedMailboxTree(f)

	t.Run("name ascending", func(t *testing.T) {
		got := idsOf(t, mailboxQuery(t, f,
			`{"accountId":"`+testAccountJMAPID()+`","sort":[{"property":"name","isAscending":true}]}`))
		// 2026, archivo viejo, drafts, inbox, sent, work — case-folded.
		want := []string{
			EncodeMailboxID(5), EncodeMailboxID(6), EncodeMailboxID(2),
			EncodeMailboxID(1), EncodeMailboxID(3), EncodeMailboxID(4),
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	})

	t.Run("sortOrder descending reverses the default", func(t *testing.T) {
		asc := idsOf(t, mailboxQuery(t, f, `{"accountId":"`+testAccountJMAPID()+`"}`))
		desc := idsOf(t, mailboxQuery(t, f,
			`{"accountId":"`+testAccountJMAPID()+`","sort":[{"property":"sortOrder","isAscending":false}]}`))
		if len(asc) != len(desc) {
			t.Fatalf("the two orders have different lengths: %d and %d", len(asc), len(desc))
		}
		for i := range asc {
			if asc[i] != desc[len(desc)-1-i] {
				t.Fatalf("descending is not the reverse of ascending:\n asc=%v\ndesc=%v", asc, desc)
			}
		}
	})
}

// TestMailboxQueryPagingAndTotal pins §5.5's paging over a result set that is
// COMPLETE — the one place this server can honor `total` unconditionally,
// because unlike Email/query there is no window for it to be wrong about.
func TestMailboxQueryPagingAndTotal(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	seedMailboxTree(f)

	resp := mailboxQuery(t, f,
		`{"accountId":"`+testAccountJMAPID()+`","position":2,"limit":2,"calculateTotal":true}`)

	got := idsOf(t, resp)
	if len(got) != 2 {
		t.Fatalf("got %d ids, want 2: %v", len(got), got)
	}
	if p := intField(t, resp, "position"); p != 2 {
		t.Errorf("position is %d, want 2", p)
	}
	total, ok := resp["total"].(float64)
	if !ok || int(total) != 6 {
		t.Errorf("total is %v, want 6 — the folder list is complete, so the count is exact",
			resp["total"])
	}

	t.Run("a negative position counts back from the true end", func(t *testing.T) {
		r := mailboxQuery(t, f, `{"accountId":"`+testAccountJMAPID()+`","position":-2}`)
		ids := idsOf(t, r)
		if len(ids) != 2 {
			t.Fatalf("got %d ids, want the last 2: %v", len(ids), ids)
		}
		if p := intField(t, r, "position"); p != 4 {
			t.Errorf("position is %d, want 4", p)
		}
	})

	t.Run("a position past the end is an empty list, not an error", func(t *testing.T) {
		// §5.5: "If the index is greater than or equal to the total number of
		// objects in the results list, then the ids array in the response will be
		// empty, but this is not an error."
		r := mailboxQuery(t, f, `{"accountId":"`+testAccountJMAPID()+`","position":99}`)
		if ids := idsOf(t, r); len(ids) != 0 {
			t.Errorf("got %v, want an empty list", ids)
		}
	})

	t.Run("an anchor resolves against the complete result set", func(t *testing.T) {
		r := mailboxQuery(t, f,
			`{"accountId":"`+testAccountJMAPID()+`","anchor":"`+EncodeMailboxID(3)+`","anchorOffset":1,"limit":1}`)
		ids := idsOf(t, r)
		if len(ids) != 1 || ids[0] != EncodeMailboxID(5) {
			t.Errorf("got %v, want the folder after Sent in the default order", ids)
		}
	})

	t.Run("an anchor that is not in the results is anchorNotFound", func(t *testing.T) {
		merr := mailboxQueryError(t, f,
			`{"accountId":"`+testAccountJMAPID()+`","filter":{"role":"inbox"},"anchor":"`+EncodeMailboxID(4)+`"}`)
		if merr.Code != jmap.CodeAnchorNotFound {
			t.Errorf("got %s, want %s", merr.Code, jmap.CodeAnchorNotFound)
		}
	})
}

// TestMailboxQueryIsHonestAboutQueryChanges keeps the §5.5 pre-announcement
// truthful: Mailbox/queryChanges declines every call, so advertising
// canCalculateChanges:true would send a conforming client to a method that
// refuses it.
func TestMailboxQueryIsHonestAboutQueryChanges(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	seedMailboxTree(f)

	resp := mailboxQuery(t, f, `{"accountId":"`+testAccountJMAPID()+`"}`)
	if v, ok := resp["canCalculateChanges"].(bool); !ok || v {
		t.Errorf("canCalculateChanges is %v, want false", resp["canCalculateChanges"])
	}
	if s, _ := resp["queryState"].(string); s == "" {
		t.Error("queryState is empty; §5.5 makes it the cursor a client compares future responses to")
	}

	// And the refusal is still there, unchanged by E1.
	_, merr := f.deps().handleMailboxQueryChanges(callerCtx(), json.RawMessage(
		`{"accountId":"`+testAccountJMAPID()+`","sinceQueryState":"q1-1"}`))
	if merr == nil || merr.Code != jmap.CodeCannotCalculateChanges {
		t.Errorf("Mailbox/queryChanges answered %v, want cannotCalculateChanges", merr)
	}
}

// TestMailboxQueryRejectsAForeignAccount is the no-oracle rule: a request naming
// somebody else's account must not be able to distinguish "no such account" from
// "not yours", and must never return their folder list.
func TestMailboxQueryRejectsAForeignAccount(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	seedMailboxTree(f)

	merr := mailboxQueryError(t, f, `{"accountId":"`+jmap.EncodeAccountID(otherAccountID)+`"}`)
	if merr.Code != jmap.CodeAccountNotFound {
		t.Errorf("got %s, want %s", merr.Code, jmap.CodeAccountNotFound)
	}
}
