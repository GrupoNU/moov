package mail

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
)

// SearchSnippet/get (RFC 8621 §5), L3 epic E3.
//
// The escaping tests are the reason this file exists. Everything else here is
// ordinary protocol conformance; TestSnippetsContainOnlyMarkMarkup and its
// neighbours are the ones that fail if the endpoint becomes a stored XSS.

// snippetResult runs SearchSnippet/get and returns the decoded response.
func snippetResult(t *testing.T, f *fakeReaders, args string) snippetResponse {
	t.Helper()
	out, merr := f.deps().handleSearchSnippetGet(callerCtx(), json.RawMessage(args))
	if merr != nil {
		t.Fatalf("SearchSnippet/get failed: %v — %s", merr.Code, merr.Description)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshalling the response: %v", err)
	}
	var resp snippetResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	return resp
}

// seedSnippet stores a MARKED fragment as the store would return it: the
// sentinels are what ts_headline placed, and the surrounding text is whatever
// the message contained — including, in the attack tests, markup.
func seedSnippet(f *fakeReaders, id int64, subject, preview string) {
	if f.snippets == nil {
		f.snippets = map[int64]SnippetView{}
	}
	f.snippets[id] = SnippetView{MessageID: id, Subject: subject, Preview: preview}
}

// markOnly matches a string whose ONLY markup is <mark> and </mark>.
//
// It is written as "no < survives except as part of a mark tag" rather than as
// a list of forbidden tags, because a denylist of tags is exactly the kind of
// check that passes until someone finds the tag it forgot.
var markOnly = regexp.MustCompile(`^(?:[^<>]|<mark>|</mark>)*$`)

// THE CONTRACT. A snippet's subject and preview are plain text whose only
// markup is <mark> and </mark>.
//
// The corpus is deliberately hostile: every one of these is a real thing that
// arrives in mail, and every one of them would reach the DOM as markup under
// the naive implementation (StartSel=<mark> straight out of ts_headline, which
// PostgreSQL 17.4 was verified to pass through untouched — see
// internal/store/snippets.go).
func TestSnippetsContainOnlyMarkMarkup(t *testing.T) {
	hostile := []struct {
		name string
		text string
	}{
		{"script tag", `<script>alert(document.cookie)</script>`},
		{"img onerror", `<img src=x onerror=alert(1)>`},
		{"an attribute break-out", `" onmouseover="alert(1)`},
		{"a forged closing mark", `</mark><script>x</script><mark>`},
		{"an ampersand entity", `AT&T &amp; friends &lt;b&gt;`},
		{"an iframe", `<iframe src="javascript:alert(1)"></iframe>`},
		{"a bare less-than", `5 < 7 and 9 > 3`},
		{"single quotes", `it's a 'quoted' word`},
	}

	for _, tc := range hostile {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeReaders()
			// The hostile text arrives WITH a legitimate highlight around a
			// matched word, which is the realistic shape: the attacker controls
			// the message, the server controls the marks.
			seedSnippet(f, 1,
				escapedMarkStart+"factura"+escapedMarkStop+" "+tc.text,
				tc.text+" "+escapedMarkStart+"factura"+escapedMarkStop)

			resp := snippetResult(t, f, fmt.Sprintf(
				`{"accountId":%q,"filter":{"text":"factura"},"emailIds":[%q]}`,
				testAccountJMAPID(), EncodeEmailID(1)))

			if len(resp.List) != 1 {
				t.Fatalf("list = %d entries, want 1", len(resp.List))
			}
			for _, field := range []struct {
				name string
				val  *string
			}{{"subject", resp.List[0].Subject}, {"preview", resp.List[0].Preview}} {
				if field.val == nil {
					t.Fatalf("%s is null, want a highlighted snippet", field.name)
				}
				got := *field.val
				if !markOnly.MatchString(got) {
					t.Errorf("%s = %q\ncontains markup other than <mark>; the hostile text reached "+
						"the client unescaped", field.name, got)
				}
				// The highlight itself must survive — an implementation that
				// escaped everything and marked nothing would pass the check
				// above and be useless.
				if !strings.Contains(got, "<mark>factura</mark>") {
					t.Errorf("%s = %q, want the match highlighted", field.name, got)
				}
				// And no control character may reach the client: a sentinel
				// that failed to become a mark must not be emitted raw.
				if strings.ContainsAny(got, escapedMarkStart+escapedMarkStop) {
					t.Errorf("%s = %q, contains a raw sentinel character", field.name, got)
				}
			}
		})
	}
}

// The marks a snippet carries must be BALANCED, because an unbalanced <mark>
// makes a browser extend the highlight to the end of the element — which in a
// list of snippets lets one message's content restyle the rows below it.
func TestSnippetMarksAreAlwaysBalanced(t *testing.T) {
	f := newFakeReaders()
	// A start with no stop: what a fragment boundary produces.
	seedSnippet(f, 1, escapedMarkStart+"cortado", "sin marcas")

	resp := snippetResult(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"cortado"},"emailIds":[%q]}`,
		testAccountJMAPID(), EncodeEmailID(1)))

	got := *resp.List[0].Subject
	if strings.Count(got, "<mark>") != strings.Count(got, "</mark>") {
		t.Errorf("subject = %q has unbalanced marks", got)
	}
}

// RFC 8621 §5: "subject: String|null", "preview: String|null" — and §5.1: "If
// the filter does not include a TEXT-based condition ... the server SHOULD
// return null for both properties."
//
// null rather than "": an empty string tells a client there IS a snippet which
// happens to be empty, which is a different fact.
func TestSnippetsAreNullWithoutATextCondition(t *testing.T) {
	f := newFakeReaders()
	seedSnippet(f, 1, escapedMarkStart+"x"+escapedMarkStop, "y")

	resp := snippetResult(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":"m1"},"emailIds":[%q]}`,
		testAccountJMAPID(), EncodeEmailID(1)))

	if len(resp.List) != 1 {
		t.Fatalf("list = %d entries, want 1", len(resp.List))
	}
	if resp.List[0].Subject != nil || resp.List[0].Preview != nil {
		t.Errorf("subject = %v, preview = %v; want both null for a filter with no text",
			resp.List[0].Subject, resp.List[0].Preview)
	}
}

// A message that exists but did not MATCH gets nulls, not notFound. Conflating
// the two would tell a client its own search results do not exist.
func TestSnippetsDistinguishNoMatchFromNotFound(t *testing.T) {
	f := newFakeReaders()
	seedSnippet(f, 1, escapedMarkStart+"hit"+escapedMarkStop, "")

	resp := snippetResult(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"hit"},"emailIds":[%q,%q,"not-an-id"]}`,
		testAccountJMAPID(), EncodeEmailID(1), EncodeEmailID(2)))

	if len(resp.List) != 2 {
		t.Fatalf("list = %d entries, want 2 (both decodable ids)", len(resp.List))
	}
	if resp.List[0].Subject == nil {
		t.Error("the matching message has no snippet")
	}
	if resp.List[1].Subject != nil {
		t.Error("the non-matching message must get null, not a snippet")
	}
	if len(resp.NotFound) != 1 || resp.NotFound[0] != "not-an-id" {
		t.Errorf("notFound = %v, want the undecodable id only", resp.NotFound)
	}
}

// §5.1 does not fix the order of `list`, but returning the client's own order
// is what lets it zip the snippets against ids it already holds.
func TestSnippetsPreserveTheRequestedOrder(t *testing.T) {
	f := newFakeReaders()
	for _, id := range []int64{1, 2, 3} {
		seedSnippet(f, id, fmt.Sprintf("%sm%d%s", escapedMarkStart, id, escapedMarkStop), "")
	}
	resp := snippetResult(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"m"},"emailIds":[%q,%q,%q]}`,
		testAccountJMAPID(), EncodeEmailID(3), EncodeEmailID(1), EncodeEmailID(2)))

	want := []string{EncodeEmailID(3), EncodeEmailID(1), EncodeEmailID(2)}
	for i, w := range want {
		if resp.List[i].EmailID != w {
			t.Errorf("list[%d].emailId = %q, want %q", i, resp.List[i].EmailID, w)
		}
	}
}

// RFC 8620 §2 maxObjectsInGet is defined for "a single /get type method call",
// which this is. Applying it here is what keeps the ts_headline cost
// proportional to the request rather than to the mailbox — and it makes the
// limit the session ADVERTISES the limit this method APPLIES (J1's rule).
func TestSnippetsEnforceMaxObjectsInGet(t *testing.T) {
	f := newFakeReaders()
	limit := jmap.DefaultLimits().MaxObjectsInGet

	ids := make([]string, 0, limit+1)
	for i := range limit + 1 {
		ids = append(ids, fmt.Sprintf("%q", EncodeEmailID(int64(i+1))))
	}
	_, merr := f.deps().handleSearchSnippetGet(callerCtx(), json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"x"},"emailIds":[%s]}`,
		testAccountJMAPID(), strings.Join(ids, ","))))

	if merr == nil {
		t.Fatal("a request over maxObjectsInGet was accepted")
	}
	if merr.Code != jmap.CodeRequestTooLarge {
		t.Errorf("code = %q, want requestTooLarge", merr.Code)
	}
}

// §5.1: the filter is "the same filter as passed to Email/query". A filter this
// server would refuse to SEARCH must be refused here too — otherwise a client
// could learn, through snippets, about matches a query would not return.
func TestSnippetsRefuseTheSameFiltersQueryRefuses(t *testing.T) {
	f := newFakeReaders()
	_, merr := f.deps().handleSearchSnippetGet(callerCtx(), json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"filter":{"operator":"NOT","conditions":[{"text":"a"}]},"emailIds":[%q]}`,
		testAccountJMAPID(), EncodeEmailID(1))))

	if merr == nil {
		t.Fatal("SearchSnippet/get accepted a filter Email/query refuses")
	}
	if merr.Code != jmap.CodeUnsupportedFilter {
		t.Errorf("code = %q, want unsupportedFilter", merr.Code)
	}
}

// Another account's id gets accountNotFound, exactly as every other method
// answers — a snippet endpoint that leaked existence would be an oracle over
// other people's mail.
func TestSnippetsRejectAForeignAccount(t *testing.T) {
	f := newFakeReaders()
	_, merr := f.deps().handleSearchSnippetGet(callerCtx(), json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"x"},"emailIds":[]}`,
		jmap.EncodeAccountID(otherAccountID))))
	if merr == nil || merr.Code != jmap.CodeAccountNotFound {
		t.Errorf("error = %v, want accountNotFound", merr)
	}
}

// markupSnippet is the one function that can emit markup, so its contract is
// pinned directly as well as through the handler: nothing in, null out.
func TestMarkupSnippetReturnsNullForEmpty(t *testing.T) {
	if got := markupSnippet(""); got != nil {
		t.Errorf("markupSnippet(\"\") = %q, want nil (RFC 8621 §5's null)", *got)
	}
}
