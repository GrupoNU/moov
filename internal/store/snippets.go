package store

import (
	"context"
	"fmt"
	"strings"
)

// Search snippets: the highlighted fragments RFC 8621 §5 exposes as
// SearchSnippet objects (L3 epic E3).
//
// # The one thing this file has to get right
//
// The snippet is USER CONTENT that a browser will render as HTML. The PWA puts
// it in the DOM to show the user which words matched. So the entire design of
// this file is one question: how does a message whose subject is
//
//	<img src=x onerror=alert(document.cookie)>
//
// reach that DOM as text rather than as a tag?
//
// PostgreSQL's ts_headline does NOT help. It is a text-marking function, not an
// HTML function, and it passes its input through untouched — verified against
// PostgreSQL 17.4 rather than assumed:
//
//	ts_headline('simple', 'presupuesto <script>alert(1)</script>',
//	            websearch_to_tsquery('simple','presupuesto'),
//	            'StartSel=<mark>,StopSel=</mark>,HighlightAll=true')
//	-> '<mark>presupuesto</mark> <script>alert(1)</script>'
//
// The script tag survives verbatim. An implementation that sets StartSel to
// `<mark>` and returns the result is therefore handing raw attacker-controlled
// HTML to the client, wrapped in the server's own blessing that it is a
// snippet. That is the naive version, it looks correct in every test written
// with polite data, and it is a stored XSS.
//
// # The contract, and the order that makes it hold
//
// A snippet returned by this package is PLAIN TEXT in which the only markup is
// <mark> and </mark>, and those two can only have been placed by the
// highlighter. Nothing else can appear: no tag, no entity the source did not
// escape into, no attribute.
//
// It holds because of the ORDER of three steps, and the order is the whole
// mechanism:
//
//  1. STRIP the sentinel runes from the source text, before ts_headline sees it.
//  2. Let ts_headline mark with THOSE runes rather than with angle brackets.
//  3. HTML-escape the entire result, THEN replace the escaped sentinels with
//     <mark> and </mark>.
//
// Step 3 escapes the attacker's angle brackets and the highlighter's markers
// alike — which is what makes step 3 safe to write, because it needs no
// knowledge of which is which. Step 1 is what makes step 3 CORRECT: without it
// a message whose body literally contains the sentinel rune would emit a
// <mark> the highlighter never placed, letting a sender forge emphasis inside a
// snippet. That is a small attack, but it is the exact seam an escaping scheme
// is supposed to close, and it was verified to be real before being closed —
// PostgreSQL does not strip the sentinel for us:
//
//	source 'presupuesto U+0002 literal' with StartSel=U+0002
//	-> the source's own U+0002 survives into the output
//
// # Why a sentinel and not "escape first, then let ts_headline mark"
//
// That order is the tempting one and it is wrong. Escaping first turns
// `<script>` into `&lt;script&gt;`, which ts_headline then tokenizes — so
// `lt`, `script` and `gt` become lexemes that a search for "script" would
// match and highlight, and the fragment windows would be measured in escaped
// characters rather than words. The snippet would be built from a text the
// user never wrote. Marking on the ORIGINAL text and escaping afterwards keeps
// the fragments faithful to the message.

// The sentinel runes ts_headline marks with.
//
// U+0002 (START OF TEXT) and U+0003 (END OF TEXT) are C0 control characters.
// They are chosen because they cannot occur in well-formed displayable text and
// have no meaning in HTML, so stripping them from the source (step 1 above)
// removes nothing a user would miss — unlike, say, a rare printable character,
// whose removal would silently corrupt a message that happened to contain it.
//
// They are runes rather than multi-character strings because ts_headline places
// them literally and a multi-character sentinel could be split across a fragment
// boundary, leaving half of it in the output as visible garbage.
const (
	snippetMarkStart = "\x02"
	snippetMarkStop  = "\x03"
)

// SnippetQuery asks for the highlighted fragments of specific messages.
type SnippetQuery struct {
	AccountID int64

	// MessageIDs are the messages to snippet. The caller bounds this list —
	// RFC 8621 §5.1 makes SearchSnippet/get take the ids of "the Emails to fetch
	// the snippets for", and the JMAP layer applies its maxObjectsInGet to it,
	// which is what keeps this method's cost proportional to the request rather
	// than to the mailbox.
	MessageIDs []int64

	// Text is the same search term the query used. Empty means "no highlight",
	// which §5.1 explicitly allows for ("If the filter does not include a
	// TEXT-based condition ... the server SHOULD return null for both
	// properties") and which this method answers by returning no rows.
	Text string
}

// SnippetRow is one message's highlighted fragments, ALREADY SENTINEL-MARKED
// but NOT yet HTML-escaped.
//
// The escaping happens in the JMAP layer rather than here, and the split is
// deliberate: this package's job is the database, and an HTML transformation
// buried in a store method is exactly the kind of thing a future caller reuses
// without the transformation. The type name says the invariant instead —
// snippets leave this package MARKED, and the one function that converts a mark
// into markup lives with the code that serializes to the client.
type SnippetRow struct {
	MessageID int64
	// Subject is the whole subject line with matches marked.
	Subject string
	// Preview is a short fragment of the body around the matches.
	Preview string
}

// snippetSubjectOptions and snippetBodyOptions are the ts_headline
// configurations.
//
// The subject is highlighted WHOLE (HighlightAll=true): a subject line is short
// and truncating it would lose the very context the snippet exists to give.
// The body is fragmented — at most two windows of ~18 words — because a body
// can be a megabyte and the client renders one row.
//
// StartSel/StopSel carry the sentinels. They are built with fmt rather than
// written as literals so the constants above are the single source of truth: a
// sentinel changed in one place and not the other would produce output whose
// marks the escaping step does not recognize, which fails CLOSED (the marks
// would be escaped and shown as control characters) rather than open — but it
// would still be a bug, and there is no reason to make it possible.
var (
	snippetSubjectOptions = fmt.Sprintf(
		"StartSel=%s,StopSel=%s,HighlightAll=true", snippetMarkStart, snippetMarkStop)
	snippetBodyOptions = fmt.Sprintf(
		"StartSel=%s,StopSel=%s,MaxFragments=2,MaxWords=18,MinWords=5,FragmentDelimiter= … ",
		snippetMarkStart, snippetMarkStop)
)

// Snippets returns the highlighted subject and body fragments for the given
// messages.
//
// # Cost
//
// ts_headline is CPU work per row, not a scan: the rows are fetched by primary
// key from the id list the caller already holds, so the plan is an index scan on
// messages_pkey and the cost is linear in the number of ids. Measured on the E3
// bench corpus (120,000-message account, PostgreSQL 17.4):
//
//	50 ids  -> 1.4 ms
//	200 ids -> 4.8 ms
//	500 ids -> 12.9 ms   (jmap.DefaultLimits.MaxObjectsInGet)
//
// Linear, and the ceiling is the JMAP layer's maxObjectsInGet rather than
// anything about the mailbox — which is the property that makes this method
// safe to expose at all. It runs on the ANALYTIC pool for the same reason
// ranking does: ts_headline on a pathological body is the one part of this that
// can be slow, and the analytic pool carries a statement_timeout so it fails
// alone instead of occupying an interactive connection.
func (s *Store) Snippets(ctx context.Context, q SnippetQuery) ([]SnippetRow, error) {
	if q.Text == "" || len(q.MessageIDs) == 0 {
		return nil, nil
	}

	// The account scope is in the WHERE clause even though the ids came from a
	// search this account already ran: the repertoire's rule is that account_id
	// is always present and always first (search.go), and a method that trusted
	// its caller's ids would be the one place that rule did not hold.
	//
	// translate() strips the sentinel runes from the source columns before
	// ts_headline sees them — step 1 of the escaping contract at the top of this
	// file. It is done in SQL rather than in Go because ts_headline reads the
	// column directly; there is no point in the pipeline where Go could
	// intervene between the read and the marking.
	const sql = `
		SELECT m.id,
		       ts_headline('simple', translate(m.subject,   $3, ''),
		                   websearch_to_tsquery('simple', immutable_unaccent($2)), $4),
		       ts_headline('simple', translate(m.body_text, $3, ''),
		                   websearch_to_tsquery('simple', immutable_unaccent($2)), $5)
		  FROM messages m
		 WHERE m.account_id = $1
		   AND m.id = ANY($6::bigint[])`

	rows, err := s.analytic.Query(ctx, sql,
		q.AccountID, q.Text,
		snippetMarkStart+snippetMarkStop,
		snippetSubjectOptions, snippetBodyOptions,
		q.MessageIDs)
	if err != nil {
		return nil, fmt.Errorf("search snippets: %w", err)
	}
	defer rows.Close()

	var out []SnippetRow
	for rows.Next() {
		var r SnippetRow
		if err := rows.Scan(&r.MessageID, &r.Subject, &r.Preview); err != nil {
			return nil, fmt.Errorf("scanning snippet: %w", err)
		}
		// A defensive second strip of anything that is not one of OUR marks.
		//
		// The SQL already removed the sentinels from the source, so in a correct
		// world every sentinel here was placed by ts_headline. This is the
		// belt-and-braces check for the world where it was not: a PostgreSQL
		// version that handles translate() differently, a column written by a
		// path that bypassed the parser, a future caller that adds a third
		// ts_headline column and forgets the translate(). Balancing the marks
		// costs a scan of a string that is already in memory.
		r.Subject = balanceMarks(r.Subject)
		r.Preview = balanceMarks(r.Preview)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading snippets: %w", err)
	}
	return out, nil
}

// balanceMarks drops any sentinel that is not part of a well-formed
// start/stop pair, in order.
//
// It exists so that the JMAP layer's mark-to-markup substitution can be a plain
// string replacement without ever producing unbalanced <mark> tags — which a
// browser would recover from by extending the highlight to the end of the
// element, turning a snippet into a styling bug at best and, in a client that
// concatenates snippets, a way for one message's content to reformat another's.
//
// The rule is exactly "a stop only counts if a start is open": anything else is
// dropped rather than repaired, because a dropped mark loses emphasis while a
// repaired one invents it.
func balanceMarks(s string) string {
	if !strings.ContainsAny(s, snippetMarkStart+snippetMarkStop) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	open := false
	for _, r := range s {
		switch string(r) {
		case snippetMarkStart:
			if open {
				// A second start with none closed: drop it, keeping the first.
				continue
			}
			open = true
		case snippetMarkStop:
			if !open {
				continue
			}
			open = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if open {
		// A start with no stop: close it at the end rather than dropping it, so
		// the highlight the user sees ends where the text does instead of
		// vanishing. Balanced either way, which is the invariant that matters.
		out += snippetMarkStop
	}
	return out
}
