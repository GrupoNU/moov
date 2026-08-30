package store

import (
	"fmt"
	"strings"
)

// The E3 narrowing predicates: the filter conditions every shape in the
// repertoire shares, in one place.
//
// # Why one struct rather than six more fields on four query types
//
// search.go carries SearchQuery, MailboxListQuery and AccountListQuery;
// collapse.go carries CollapsedQuery. Before L3 epic E3 the four agreed on
// their narrowing by convention — each spelled out `if q.UnreadOnly { ... }`
// in its own builder — and that was already the source of one shipped bug
// (the `before` bound applied in Go on one path and in SQL on another,
// documented at SearchQuery.Until).
//
// E3 adds SIX more conditions to all four. Copying them into four builders
// would mean twenty-four hand-written predicate blocks that must stay
// byte-identical, when what "identical" means is load-bearing: an expression
// index only applies if the query repeats the indexed expression CHARACTER FOR
// CHARACTER (migration 0008), so a paraphrase in one builder is a silent
// return to a sequential scan on one code path and not the others.
//
// So the predicates are written ONCE, here, and the four builders append them.
// The appending is still explicit at each call site — this is not a query
// builder and must not become one; it is a fixed vocabulary of validated
// shapes, which is the whole discipline of L2 §4.3.
//
// # What each one costs
//
// Measured with EXPLAIN (ANALYZE, BUFFERS) on the E3 bench corpus — 120,000
// messages on the account under test, 20,000 on a second account so a missing
// account scope shows up as wrong rows rather than as a passing test —
// PostgreSQL 17.4, the dev instance. The numbers are in the doc comment of each
// field, and migration 0008's header carries the cc/bcc comparison in full.
//
// The honest summary: three of the six (HasAttachment, size bounds, and the
// role exclusion) are FILTERS on a walk the shape already performs, so they cost
// what the walk costs and nothing more. Two (Cc, Bcc) get their own index
// because without one they were 107-167 ms sequential scans. One (the flag
// bitmask) is a filter on message_state, which is where its selectivity problem
// lives; see FlagsAll.
type Narrowing struct {
	// HasAttachment restricts to messages with (true) or without (false)
	// attachments when non-nil. RFC 8621 §4.4.1's `hasAttachment`.
	//
	// It reads messages.has_attachments, a boolean the parser sets at ingest
	// (migration 0002), so it is a filter on the (account_id, date DESC) walk
	// the date-ordered shapes already run: 2.0 ms on the folder view, 0.6 ms on
	// the text path.
	//
	// No index, deliberately. A boolean that is true for ~14% of a mailbox has
	// no selectivity worth an index — a bitmap over a seventh of the table costs
	// more to build than the filter costs to apply — and migration 0004's rule
	// forbids adding one whose only effect is to give the planner another way to
	// lose to the composite GIN.
	HasAttachment *bool

	// Cc and Bcc restrict to messages whose Cc / Bcc recipients contain the
	// given substring, case-insensitively. RFC 8621 §4.4.1's `cc` and `bcc`.
	//
	// These are the two conditions migration 0008 exists for. Both are
	// unanchored substring matches served by a trigram GIN index — 1.8 ms (cc)
	// and 0.6 ms (bcc) against 145.9 ms and 167.5 ms unindexed, and 0.1 ms
	// against 125.6 ms when nothing matches, which is the shape a search box
	// produces on every keystroke.
	//
	// # These are EXACT, not the over-match from/to/subject accept
	//
	// query.go's translateCondition documents that `from`, `to` and `subject`
	// are answered with a whole-message tsvector match — deliberate
	// over-matching, because the store has one tsvector per message and no
	// per-field index. `cc` and `bcc` do NOT inherit that posture, and the
	// difference is the point of the migration:
	//
	//   * cc_addrs is its own column, so `cc:ana@x.test` matches the Cc header
	//     and nothing else. Answering it from the tsvector would also return
	//     every message merely MENTIONING Ana in its body — and a user filtering
	//     by cc is trying to find the mail where a specific person was copied,
	//     which is exactly what that over-match destroys.
	//   * bcc is not in the tsvector at all (0002 puts only from/to/cc in weight
	//     band B), so there was never an over-match available: it was an index
	//     or a refusal.
	//
	// The posture is therefore INCONSISTENT with from/to/subject on purpose, and
	// the inconsistency runs in the safe direction — these two are stricter than
	// the RFC requires, never looser. query.go states the same thing where a
	// client sees it.
	Cc  string
	Bcc string

	// MinSize and MaxSize bound messages.raw_size when non-nil: at or above
	// MinSize, strictly below MaxSize. RFC 8621 §4.4.1's `minSize` ("The size of
	// the Email in octets is greater than or equal to this number") and
	// `maxSize` ("... is less than this number").
	//
	// raw_size IS the JMAP Email.size property — adapter.go maps one to the
	// other — so this filters on the number the client sees rather than on a
	// proxy for it. That is why E3 added no size column: the store already had
	// the right one.
	//
	// 1.3 ms on the folder view. Like HasAttachment it is a filter on the date
	// walk, and like HasAttachment it gets no index: a range over a column with
	// no correlation to the sort order cannot beat the walk the LIMIT already
	// stops early, and the pathological case (a bound matching NOTHING, 79.9 ms
	// on 120,000 messages) is bounded by the same MaxSearchLimit every other
	// shape is. It is the slowest of the unindexed narrowings and it is recorded
	// as such rather than hidden.
	MinSize *int64
	MaxSize *int64

	// FlagsAll and FlagsNone are bitmask predicates over message_state.flags:
	// every bit in FlagsAll must be set, every bit in FlagsNone must be clear.
	//
	// They are what makes RFC 8621 §4.4.1's `hasKeyword` and `notKeyword`
	// answerable for the IMAP SYSTEM flags. Arbitration A6 puts user labels in
	// message_state.keywords (a text[] with its own GIN index), but $seen,
	// $flagged, $answered and $draft are bits in a bitmask — which is why
	// `hasKeyword:"$flagged"` was refused before E3 with "the repertoire has no
	// predicate for it". This is that predicate.
	//
	// # The cost, stated honestly
	//
	// A bitmask test is not indexable in the general case, and this one is not
	// indexed: message_state has exactly one partial index over flags
	// (message_state_unread, `WHERE (flags & 1) = 0`), built for the ONE
	// bitmask predicate that had a caller before E3. Measured on the folder view
	// at 8% selectivity, `is:starred` costs 77.5 ms — under the 100 ms bar, but
	// by the smallest margin of any shape in this file, and it gets there by a
	// parallel sequential scan of message_state rather than by an index.
	//
	// That is accepted rather than indexed away, for the reason 0004's rule
	// gives: a partial index per system flag would be four more indexes on the
	// hot write path (every flag change rewrites them) to serve a filter that
	// already fits the budget. If `is:starred` ever becomes a default view
	// rather than an explicit search, it earns its own partial index and its own
	// measurement — recorded here so that decision is made with the number in
	// hand rather than rediscovered.
	//
	// UnreadOnly on the query types remains SEPARATE from FlagsNone even though
	// it is expressible as one: it is spelled `(flags & 1) = 0` literally so it
	// matches the message_state_unread partial-index predicate character for
	// character, which is the only reason that index applies at all.
	FlagsAll  Flags
	FlagsNone Flags

	// ExcludeMailboxIDs removes messages held in the named mailboxes. RFC 8621
	// §4.4.1's `inMailboxOtherThan` ("A Mailbox id ... the Email must be in at
	// least one Mailbox not in this list").
	//
	// It is the mechanism behind Gmail's rule that a plain search excludes Spam
	// and Trash (canon §2.5); query.go owns that policy and this owns the
	// predicate. 0.7 ms account-wide, 0.8 ms on the text path — the exclusion is
	// evaluated on the message_state row the join already fetches by primary
	// key, so it adds a filter to a lookup rather than a lookup of its own.
	//
	// An EMPTY slice is not the same as a nil one to the caller — "exclude
	// nothing" is a legitimate request (Gmail's `in:anywhere`) — but it produces
	// no predicate either way, so both are simply omitted here.
	ExcludeMailboxIDs []int64
}

// empty reports whether this narrowing constrains anything.
func (n Narrowing) empty() bool {
	return n.HasAttachment == nil && n.Cc == "" && n.Bcc == "" &&
		n.MinSize == nil && n.MaxSize == nil &&
		n.FlagsAll == 0 && n.FlagsNone == 0 && len(n.ExcludeMailboxIDs) == 0
}

// The SQL expressions the trigram indexes of migration 0008 are built over.
//
// They are constants rather than inline strings because an expression index
// applies only when the query repeats the indexed expression CHARACTER FOR
// CHARACTER. A paraphrase — a different cast, ILIKE instead of lower()+LIKE,
// a different jsonpath spelling — silently loses the index and returns the
// shape to the 107-167 ms sequential scan the migration was written to remove,
// with no error and no failing test unless one asserts the plan. search_test.go
// asserts the plan; these constants are what make that assertion meaningful.
const (
	// ccIndexedExpr matches `CREATE INDEX messages_cc_trgm ... (lower(cc_addrs)
	// gin_trgm_ops)`.
	ccIndexedExpr = "lower(m.cc_addrs)"

	// bccIndexedExpr matches `CREATE INDEX messages_bcc_trgm ...
	// ((lower(jsonb_path_query_array(addresses, '$.bcc[*].email')::text))
	// gin_trgm_ops)`.
	bccIndexedExpr = "lower(jsonb_path_query_array(m.addresses, '$.bcc[*].email')::text)"
)

// appendConditions appends this narrowing's predicates to a WHERE clause under
// construction, returning the extended clause list and argument list.
//
// The signature is (conds, args) in and out rather than a method on a builder
// object because that is the shape every caller in this package already has:
// each query builder owns its own $-numbering, and passing the argument slice
// through is what keeps the numbering correct without a shared mutable
// builder that could renumber someone else's parameters.
//
// `mAlias` and `msAlias` are the table aliases in the caller's FROM clause.
// They are parameters rather than hardcoded because collapse.go's dedupe
// anti-join uses different ones (p / ps) over the same predicates, and a
// narrowing that silently applied to the wrong table there would exclude
// conversations the user is entitled to see.
func (n Narrowing) appendConditions(conds []string, args []any, mAlias, msAlias string) ([]string, []any) {
	if n.empty() {
		return conds, args
	}

	if n.HasAttachment != nil {
		args = append(args, *n.HasAttachment)
		conds = append(conds, fmt.Sprintf("%s.has_attachments = $%d", mAlias, len(args)))
	}

	if n.Cc != "" {
		// The LIKE pattern is built as an ARGUMENT, not concatenated into the
		// SQL: the wildcards are added to the bound value so the user's text
		// never reaches the parser, and so a term containing % or _ is a
		// literal search rather than a wildcard the user did not ask for
		// (escapeLikeTerm handles that).
		args = append(args, "%"+escapeLikeTerm(strings.ToLower(n.Cc))+"%")
		conds = append(conds, fmt.Sprintf(
			"%s LIKE $%d ESCAPE '\\'", strings.ReplaceAll(ccIndexedExpr, "m.", mAlias+"."), len(args)))
	}

	if n.Bcc != "" {
		args = append(args, "%"+escapeLikeTerm(strings.ToLower(n.Bcc))+"%")
		conds = append(conds, fmt.Sprintf(
			"%s LIKE $%d ESCAPE '\\'", strings.ReplaceAll(bccIndexedExpr, "m.", mAlias+"."), len(args)))
	}

	if n.MinSize != nil {
		// §4.4.1 minSize: "greater than or equal to".
		args = append(args, *n.MinSize)
		conds = append(conds, fmt.Sprintf("%s.raw_size >= $%d", mAlias, len(args)))
	}
	if n.MaxSize != nil {
		// §4.4.1 maxSize: "less than" — exclusive, like `before`.
		args = append(args, *n.MaxSize)
		conds = append(conds, fmt.Sprintf("%s.raw_size < $%d", mAlias, len(args)))
	}

	if n.FlagsAll != 0 {
		args = append(args, int64(n.FlagsAll))
		conds = append(conds, fmt.Sprintf("(%s.flags & $%d) = $%d", msAlias, len(args), len(args)))
	}
	if n.FlagsNone != 0 {
		args = append(args, int64(n.FlagsNone))
		conds = append(conds, fmt.Sprintf("(%s.flags & $%d) = 0", msAlias, len(args)))
	}

	if len(n.ExcludeMailboxIDs) > 0 {
		args = append(args, n.ExcludeMailboxIDs)
		conds = append(conds, fmt.Sprintf("%s.mailbox_id <> ALL ($%d::bigint[])", msAlias, len(args)))
	}

	return conds, args
}

// escapeLikeTerm neutralizes the LIKE metacharacters in user input.
//
// Without it, a user searching for `a_b@x.test` gets every address with any
// character where the underscore is, and one searching for `%` matches every
// message that has any Cc at all. Neither is an injection — the term is a bound
// parameter throughout — but both are results the user did not ask for, which
// is the same class of wrongness the repertoire refuses filters over.
//
// The backslash is escaped FIRST, because escaping it after % and _ would
// double-escape the backslashes this function itself just introduced. The
// ESCAPE '\' clause at every call site names the character.
func escapeLikeTerm(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}
