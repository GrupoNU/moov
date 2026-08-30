package mail

import (
	"context"
	"encoding/json"
	"html"
	"strings"

	"github.com/GrupoNU/moov/internal/jmap"
)

// SearchSnippet/get — RFC 8621 §5.
//
// §5: "When doing a search on a String property, the client may wish to show
// the relevant section of the body that matches the search as a preview and
// to highlight any matching terms in both this and the subject of the Email.
// Search snippets represent this data."
//
// # Why this exists when neither reference client asks for it
//
// Gmail's web UI has no API for it, and Bulwark never calls it. It is
// implemented anyway, and the reason is in the L3 plan's own words for epic E3:
// this is one of the places Moov exceeds both references rather than matching
// them. A search result list that shows WHY each row matched — the sentence
// containing the term, with the term marked — is the difference between a
// result list and a search engine, and RFC 8621 already specifies exactly how
// to say it.
//
// # The security contract, and where each half of it lives
//
// A snippet is user content that ends up in the DOM. The full argument is in
// internal/store/snippets.go, which proved against PostgreSQL 17.4 that
// ts_headline passes `<script>` through untouched — so the naive
// `StartSel=<mark>` implementation is a stored XSS.
//
// The split of responsibility:
//
//	store  — marks matches with control-character sentinels, having FIRST
//	         stripped those sentinels from the source text, and returns
//	         balanced marks.
//	here   — HTML-escapes the whole string, THEN turns the escaped sentinels
//	         into <mark> and </mark>.
//
// The order is the mechanism. Escaping first would make the attacker's angle
// brackets and the highlighter's markers indistinguishable in the right
// direction: everything is escaped, and only then is the one thing that
// provably came from the highlighter turned back into markup.
//
// THE CONTRACT THIS SERVER PROMISES THE PWA, pinned by
// TestSnippetsContainOnlyMarkMarkup: the subject and preview of a
// SearchSnippet are PLAIN TEXT whose only markup is <mark> and </mark>. No
// other tag, no attribute, no unescaped ampersand or angle bracket, and never
// an unbalanced mark. The PWA still renders it inside its own sanitize
// pipeline — defense in depth is the project's posture (ADR §"Seguridad HTML":
// three layers) — but it does not have to trust this endpoint to be safe, and
// this endpoint does not rely on the PWA to make it safe.

// snippetRequest is the §5.1 SearchSnippet/get arguments.
//
// §5.1: "accountId: Id", "filter: FilterOperator|FilterCondition|null — The
// same filter as passed to Email/query", "emailIds: Id[] — The ids of the
// Emails to fetch snippets for."
type snippetRequest struct {
	AccountID string          `json:"accountId"`
	Filter    json.RawMessage `json:"filter"`
	EmailIDs  []string        `json:"emailIds"`
}

// snippetResponse is the §5.1 response.
//
// §5.1: "list: SearchSnippet[]", "notFound: Id[]|null — A list of Email ids
// requested that could not be found".
//
// NotFound is a slice with omitempty deliberately absent: §5.1 makes it
// nullable, and a client distinguishing "none missing" from "the server did not
// say" is served better by an explicit empty array than by a missing key.
type snippetResponse struct {
	AccountID string        `json:"accountId"`
	List      []snippetView `json:"list"`
	NotFound  []string      `json:"notFound"`
}

// snippetView is the §5 SearchSnippet object.
//
// §5: "emailId: Id", "subject: String|null — The subject of the Email with
// matching search terms highlighted", "preview: String|null — The relevant
// section of the body with matching search terms highlighted".
//
// Both are POINTERS because §5.1 requires null rather than an empty string in
// the two cases that actually occur: "If the search does not match the Email's
// subject or body, the value is null" — and, more importantly, "If the filter
// does not include a TEXT-based condition ... the server SHOULD return null for
// both properties". A "" would tell a client there is a snippet that happens to
// be empty.
type snippetView struct {
	EmailID string  `json:"emailId"`
	Subject *string `json:"subject"`
	Preview *string `json:"preview"`
}

// SnippetReader produces highlighted fragments for specific messages.
//
// Like SearchReader it takes the TRANSLATED filter rather than raw JMAP
// arguments, so the decisions about which filters this server understands stay
// in query.go and this interface can only be asked for a search it could have
// run.
type SnippetReader interface {
	// Snippets returns one row per message that HAS a snippet. A message with
	// no match in either its subject or its body is simply absent, which is how
	// the handler knows to render §5's nulls without a second query.
	//
	// The returned strings are MARKED but NOT escaped — the store's half of the
	// contract documented at the top of this file. The handler escapes.
	Snippets(ctx context.Context, accountID int64, f searchFilter, messageIDs []int64) ([]SnippetView, error)
}

// SnippetView is one message's marked fragments, in this package's vocabulary.
type SnippetView struct {
	MessageID int64
	Subject   string
	Preview   string
}

// handleSearchSnippetGet implements SearchSnippet/get.
func (d *Deps) handleSearchSnippetGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	caller, ok := jmap.CallerFromContext(ctx)
	if !ok {
		return nil, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}

	var req snippetRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("arguments did not parse: %v", err)
	}
	if req.AccountID == "" {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("the accountId argument is required")
	}
	if req.AccountID != caller.JMAPAccountID() {
		return nil, jmap.NewMethodError(jmap.CodeAccountNotFound)
	}

	// The SAME bound every /get carries. §5.1 does not name a limit for
	// emailIds, and §2's maxObjectsInGet is defined for "the maximum number of
	// objects that the client may request in a single /get type method call" —
	// which this is. Applying it here is what keeps the ts_headline cost
	// proportional to a request rather than to a mailbox, and it means the
	// limit the session ADVERTISES is the limit this method APPLIES, which is
	// J1's declared-equals-applied rule.
	if merr := d.Limits.CheckObjectsInGet(len(req.EmailIDs)); merr != nil {
		return nil, merr
	}

	// The filter is translated with the same function Email/query uses, so a
	// filter this server would refuse to SEARCH is refused here too rather than
	// silently ignored. §5.1 says the filter is "the same filter as passed to
	// Email/query"; running it through a laxer parser would let a client
	// discover, through snippets, matches a query would not return.
	filter, merr := translateFilter(req.Filter)
	if merr != nil {
		return nil, merr
	}

	resp := &snippetResponse{
		AccountID: req.AccountID,
		List:      []snippetView{},
		NotFound:  []string{},
	}

	ids := make([]int64, 0, len(req.EmailIDs))
	order := make([]string, 0, len(req.EmailIDs))
	for _, wire := range req.EmailIDs {
		id, err := DecodeEmailID(wire)
		if err != nil {
			// An id this server could never have issued names no message, so it
			// is notFound — the same reasoning /get applies, and §5.1's notFound
			// is defined as exactly "requested that could not be found".
			resp.NotFound = append(resp.NotFound, wire)
			continue
		}
		ids = append(ids, id)
		order = append(order, wire)
	}
	if len(ids) == 0 {
		return resp, nil
	}

	rows, err := d.Snippets.Snippets(ctx, caller.AccountID, filter, ids)
	if err != nil {
		return nil, serverFail("building search snippets", err)
	}
	byID := make(map[int64]SnippetView, len(rows))
	for _, r := range rows {
		byID[r.MessageID] = r
	}

	// §5.1 does not fix the order of `list`, but returning it in the order the
	// client ASKED is what lets a client zip the snippets against the ids it
	// already holds without a lookup — and it makes the response deterministic,
	// which is what a golden test needs.
	for i, wire := range order {
		row, ok := byID[ids[i]]
		if !ok {
			// The message exists as far as this method can tell — the store
			// returns rows only for messages that MATCHED — so an absent row is
			// §5's "the search does not match", which is null for both
			// properties, NOT a notFound. Conflating the two would tell a client
			// its own search results do not exist.
			resp.List = append(resp.List, snippetView{EmailID: wire})
			continue
		}
		resp.List = append(resp.List, snippetView{
			EmailID: wire,
			Subject: markupSnippet(row.Subject),
			Preview: markupSnippet(row.Preview),
		})
	}
	return resp, nil
}

// The escaped forms of the store's sentinels.
//
// html.EscapeString leaves control characters alone — it rewrites only
// <, >, &, ' and " — so the sentinels survive escaping unchanged and these are
// simply the sentinels themselves. They are named constants anyway, so that the
// substitution below reads as "replace the escaped start marker" rather than as
// a bare control character nobody can see in a diff, and so that a future
// escaper that DID touch them fails the round-trip test instead of silently
// emitting control characters to the client.
const (
	escapedMarkStart = "\x02"
	escapedMarkStop  = "\x03"
)

// markupSnippet turns a marked fragment into the §5 highlighted string.
//
// The three steps, in the order that makes the contract hold:
//
//  1. If there is nothing, return nil — §5's null, not an empty string.
//  2. HTML-escape EVERYTHING. After this line the string contains no markup at
//     all: every < the message carried is now &lt;, and the only characters
//     that are not ordinary text are the sentinels, which escaping does not
//     touch.
//  3. Replace the sentinels with <mark> and </mark>. This is the ONLY markup
//     this function can produce, because it is the only markup it writes.
//
// Step 2 before step 3 is the whole security property. Reversed — marks first,
// escape second — the escaper would turn the <mark> tags into &lt;mark&gt; and
// the snippet would show the tags as text; done with `StartSel=<mark>` in the
// database and no escaping at all, the message's own HTML would reach the DOM.
// Both mistakes are natural and this ordering is neither.
// It also BALANCES the marks, and does so here rather than trusting the reader
// to have done it. internal/store balances too, and the duplication is
// deliberate: an unbalanced <mark> makes a browser extend the highlight to the
// end of the element — which, in a list of snippets, lets one message's content
// restyle the rows below it — and this function is the last place before the
// wire. A safety property that depends on a cooperating implementation of an
// INTERFACE is not a property of the server; it is a property of one
// implementation. This one holds for every SnippetReader.
func markupSnippet(marked string) *string {
	if marked == "" {
		return nil
	}
	escaped := html.EscapeString(marked)
	out := strings.NewReplacer(
		escapedMarkStart, "<mark>",
		escapedMarkStop, "</mark>",
	).Replace(balanceSnippetMarks(escaped))
	return &out
}

// balanceSnippetMarks drops every sentinel that is not part of a well-formed
// start/stop pair, in order, and closes a trailing open one.
//
// The rule: a stop counts only if a start is open; a second start while one is
// open is dropped; a start still open at the end is closed rather than dropped,
// so the highlight ends where the text does instead of vanishing. Balanced
// either way, which is the invariant the caller needs.
func balanceSnippetMarks(s string) string {
	if !strings.ContainsAny(s, escapedMarkStart+escapedMarkStop) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(escapedMarkStop))
	open := false
	for _, r := range s {
		switch string(r) {
		case escapedMarkStart:
			if open {
				continue
			}
			open = true
		case escapedMarkStop:
			if !open {
				continue
			}
			open = false
		}
		b.WriteRune(r)
	}
	if open {
		b.WriteString(escapedMarkStop)
	}
	return b.String()
}
