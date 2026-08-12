package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Email/query — RFC 8620 §5.5 as extended by RFC 8621 §4.4.
//
// # The rule this file exists to obey
//
// L2-jmap-server §2.3 states it as a hard rule inherited from L2-sync-engine
// §4.3: the JMAP layer may only reach the database through the store's typed
// search repertoire. Spike S3 measured ten query shapes at 5M messages; eight
// pass with 4x-30x headroom and two (unbounded ranking, exact count) fail for
// reasons no index can fix. The repertoire in internal/store/search.go IS that
// result, encoded as methods.
//
// So this file is, deliberately, mostly a TRANSLATOR with a refusal path: it
// maps the RFC 8621 §4.4.1 FilterCondition onto SearchQuery's fields, and
// everything the repertoire cannot express becomes `unsupportedFilter` naming
// the node that could not be translated. It never falls back to SQL. A filter
// this server cannot answer honestly is one it declines, because the
// alternative — silently dropping a condition — returns messages the user
// explicitly excluded, which in a mail client is a privacy failure, not a
// missing feature.
//
// # The bound every caller inherits
//
// store.MaxSearchLimit is 200. That is not a paging window this file can page
// past: the repertoire has no OFFSET, so `position` is applied by SLICING a
// single bounded fetch. Results beyond the 200th are not reachable through
// Email/query in this phase. queryBounds() below is the single place that
// number is applied, and every response that was truncated by it says so
// through the `limit` property RFC 8620 §5.5 provides exactly for this
// ("The limit enforced by the server..."). Honest boundedness over a paging
// illusion that breaks at row 201.

// queryRequest is the RFC 8620 §5.5 /query arguments object, extended with the
// RFC 8621 §4.4 Email/query arguments.
//
// Position is *int64 and Limit is *uint64 because §5.5 gives absent and zero
// different meanings: an absent limit means "no limit presumed" (which this
// server clamps to its maximum), while limit:0 is a legal request for no ids
// at all. A plain value could not tell them apart.
type queryRequest struct {
	AccountID      string          `json:"accountId"`
	Filter         json.RawMessage `json:"filter"`
	Sort           []comparator    `json:"sort"`
	Position       *int64          `json:"position"`
	Anchor         *string         `json:"anchor"`
	AnchorOffset   int64           `json:"anchorOffset"`
	Limit          *uint64         `json:"limit"`
	CalculateTotal bool            `json:"calculateTotal"`

	// CollapseThreads is RFC 8621 §4.4's extra argument. It is parsed so the
	// handler can refuse it explicitly rather than ignore it — see
	// handleEmailQuery.
	CollapseThreads bool `json:"collapseThreads"`
}

// comparator is the §5.5 Comparator object.
type comparator struct {
	Property    string  `json:"property"`
	IsAscending *bool   `json:"isAscending"`
	Collation   *string `json:"collation"`
	// Keyword is the extra property §4.4.2 requires on a hasKeyword sort.
	Keyword string `json:"keyword"`
}

// ascending reports the comparator's direction; §5.5 defaults it to true.
func (c comparator) ascending() bool {
	return c.IsAscending == nil || *c.IsAscending
}

// queryResponse is the §5.5 /query response.
//
// Total is *uint64 and omitempty-free: §5.5 says total "MUST be omitted if the
// calculateTotal request argument is not true", and this server omits it in
// further cases documented at calculateTotal's handling below. A pointer makes
// "omitted" representable; a plain uint64 would put a false 0 on the wire.
type queryResponse struct {
	AccountID           string   `json:"accountId"`
	QueryState          string   `json:"queryState"`
	CanCalculateChanges bool     `json:"canCalculateChanges"`
	Position            uint64   `json:"position"`
	IDs                 []string `json:"ids"`
	Total               *uint64  `json:"total,omitempty"`
	Limit               *uint64  `json:"limit,omitempty"`
}

// The sort properties this server implements, which is what session.go
// advertises in emailQuerySortOptions. Keep the two in sync: advertising a
// sort the handler rejects is exactly the "declared != applied" lie J1's
// limits rule forbids.
const (
	// SortReceivedAt is the receivedAt comparator RFC 8621 §4.4.2 says MUST be
	// supported. It is the store's native ORDER BY date shape (S3 shape #1,
	// 9.3 ms p95) and the server's default.
	SortReceivedAt = "receivedAt"

	// SortRelevance is the BOUNDED relevance sort, exposed under the name
	// RFC 8621 §4.4.2 does not define — because what this server implements is
	// not a general relevance sort and must not be mistaken for one.
	//
	// §4.4.2 permits it: "The server MAY support sorting based on other
	// properties as well. A client can discover which properties are supported
	// by inspecting the account's capabilities object". So it is advertised in
	// emailQuerySortOptions and it is honest about its own shape: it ranks only
	// the store.RankCandidateWindow (200) most recent matches, per S3
	// mitigation #102 — unbounded ts_rank_cd measured 892 ms p95 and took the
	// instance's worst case to 68 s under concurrency.
	//
	// A client that asks for it gets relevance WITHIN the recent window, never
	// a promise that the globally most relevant message is in the list.
	SortRelevance = "relevance"

	// SortHasKeyword is the §4.4.2 hasKeyword comparator: messages carrying the
	// named keyword group ahead of those that do not.
	//
	// It is served over the bounded result window rather than by the database
	// (see translateSort), and it exists because a real client needs it: Bulwark
	// opens every folder with [hasKeyword $pinned, receivedAt].
	SortHasKeyword = "hasKeyword"
)

// handleEmailQuery implements Email/query.
func (d *Deps) handleEmailQuery(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseQuery(ctx, args)
	if merr != nil {
		return nil, merr
	}

	// §4.4.3 thread collapsing needs the thread of every candidate, and the
	// store has no thread column (see thread.go) — deriving threads for a
	// result window would be a query per row, which is precisely the unbounded
	// work L2 §4.3 forbids. unsupportedFilter is the RFC's channel for "valid
	// but I cannot process it"; the alternative of ignoring the argument would
	// return a list with duplicate threads the client asked to have collapsed.
	if req.CollapseThreads {
		return nil, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("collapseThreads is not supported: this server has no thread index yet")
	}

	filter, merr := translateFilter(req.Filter)
	if merr != nil {
		return nil, merr
	}
	order, merr := translateSort(req.Sort)
	if merr != nil {
		return nil, merr
	}
	// The two halves must agree: relevance ranking is a text operation, so a
	// relevance sort over a filter with no text has nothing to rank.
	if order.byRelevance && filter.text == "" {
		return nil, jmap.NewMethodError(jmap.CodeUnsupportedSort).
			WithDescription("the %q sort requires a text, from, to or subject filter to rank against", SortRelevance)
	}

	state, err := d.State.EmailState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading email state", err)
	}

	// Fetch the bounded candidate window ONCE, then window it locally. The
	// repertoire has no OFFSET, so this is the only correct way to serve
	// position/anchor over it, and it is why the window is capped.
	matches, err := d.Search.SearchEmails(ctx, caller.AccountID, filter, order)
	if err != nil {
		return nil, serverFail("searching emails", err)
	}

	resp := &queryResponse{
		AccountID: req.AccountID,
		// §5.5: queryState "MUST change if the results of the query ... have
		// changed". See queryStateFor for why the account's data state is the
		// correct — if coarse — answer.
		QueryState: queryStateFor(state),
		// §5.5: canCalculateChanges is "true if the server supports calling
		// Foo/queryChanges with these filter/sort parameters". This server
		// answers cannotCalculateChanges for every Email/queryChanges (ADR §2,
		// L2 §2.3), so the truthful value is false — always. Saying true here
		// would make a conforming client call queryChanges and fail, when it
		// could have refetched the list instead.
		CanCalculateChanges: false,
		IDs:                 []string{},
	}

	// Paging. Anchor resolution happens against the same fetched window, which
	// is what makes its boundedness visible rather than silent.
	start, merr := resolveStart(req, matches)
	if merr != nil {
		return nil, merr
	}

	limit, serverLimited := effectiveQueryLimit(req.Limit)
	end := start + limit
	if start > uint64(len(matches)) {
		// §5.5: "If the index is greater than or equal to the total number of
		// objects in the results list, then the ids array in the response will
		// be empty, but this is not an error."
		start = uint64(len(matches))
	}
	if end > uint64(len(matches)) {
		end = uint64(len(matches))
	}
	for _, m := range matches[start:end] {
		resp.IDs = append(resp.IDs, EncodeEmailID(m))
	}
	resp.Position = start

	// §5.5: the limit is returned "only if the server set a limit or used a
	// different limit than that given in the request" — which is how the client
	// learns the window is capped rather than inferring it from a short page.
	if serverLimited {
		l := limit
		resp.Limit = &l
	}

	if req.CalculateTotal {
		total, merr := d.queryTotal(ctx, caller.AccountID, filter, uint64(len(matches)))
		if merr != nil {
			return nil, merr
		}
		// A nil total is the deliberate omission documented in queryTotal.
		resp.Total = total
	}

	return resp, nil
}

// queryTotal answers calculateTotal, or declines to.
//
// # Why a capped count is NOT put in `total`
//
// RFC 8620 §5.5 defines the property in one sentence: "total: UnsignedInt
// (only if requested) — The total number of Foos in the results (given the
// 'filter')." It is a count, not an estimate, and §5.5 gives it a load-bearing
// role elsewhere: "If 'position' is >= 'total', this MUST be the empty list",
// and the negative-position rule says the value "MUST be added to the total
// number of results given the filter". A client doing either computation with
// a capped 200 would page wrongly.
//
// S3 H5 measured exact count(*) at 452 ms p95 — 4.5x over the Gmail-class bar
// — and showed that admitting it under load takes the instance's worst case
// from 0.7 s to 68 s. So this server cannot compute the number the RFC's
// `total` means, at the speed the product requires.
//
// Given "report a wrong number" versus "omit the property", this omits it. The
// RFC's own type signature — "(only if requested)", an optional property —
// means a client must already tolerate its absence, and every client's
// fallback (show what arrived, page until short) is CORRECT with an omitted
// total and WRONG with a capped one. Bulwark shows the ids it gets.
//
// The one case where an exact total is both cheap and correct is served: when
// the window was not truncated, the number of matches IS the total, because
// the search returned every match there was.
func (d *Deps) queryTotal(ctx context.Context, accountID int64, f searchFilter, matched uint64) (*uint64, *jmap.MethodError) {
	// The window is store.MaxSearchLimit deep. A result set shorter than the
	// window was exhausted, so its length is the exact total — no count query
	// at all, and no cap involved.
	//
	// searchWindow() is a small positive constant (200 by default, and
	// RegisterQueryMethods rejects anything larger), so the conversion is
	// exact; the guard is written out rather than asserted so gosec can see it.
	window := d.searchWindow()
	if window > 0 && matched < uint64(window) {
		total := matched
		return &total, nil
	}

	// The window was filled, so the true total is >= the window and unknown
	// without an exact count this server does not offer (S3 H5). The capped
	// count would report the ceiling, which is not "the total number of Emails
	// in the results". Omit.
	//
	// The count is still USEFUL to the product as a "199+" affordance — that is
	// what store.CountCapped exists for — but that affordance belongs to a
	// property that means "at least", and JMAP's `total` does not. When the
	// PWA needs it, it gets its own extension property rather than a lie in a
	// standard one.
	_ = ctx
	_ = accountID
	_ = f
	return nil, nil
}

// searchWindow is the depth of the candidate window every query fetches.
func (d *Deps) searchWindow() int {
	if d.SearchWindow > 0 {
		return d.SearchWindow
	}
	return DefaultSearchWindow
}

// resolveStart computes the index of the first id to return, honoring anchor
// or position per §5.5.
func resolveStart(req *queryRequest, matches []int64) (uint64, *jmap.MethodError) {
	n := int64(len(matches))

	// §5.5: "If an 'anchor' argument is given, the anchor is looked for in the
	// results after filtering and sorting. If found, the 'anchorOffset' is then
	// added to its index. If the resulting index is now negative, it is clamped
	// to 0. This index is now used exactly as though it were supplied as the
	// 'position' argument. If the anchor is not found, the call is rejected
	// with an 'anchorNotFound' error." And: "If an 'anchor' is specified, any
	// position argument supplied by the client MUST be ignored."
	if req.Anchor != nil {
		anchorID, err := DecodeEmailID(*req.Anchor)
		if err != nil {
			// An id this server could never have issued cannot be in the
			// results, so it is not found — the same reasoning decodeIDList
			// applies for /get.
			return 0, jmap.NewMethodError(jmap.CodeAnchorNotFound).
				WithDescription("the anchor %q is not a valid Email id", *req.Anchor)
		}
		idx := int64(-1)
		for i, m := range matches {
			if m == anchorID {
				idx = int64(i)
				break
			}
		}
		if idx < 0 {
			// HONEST BOUNDEDNESS: the anchor is searched for in the fetched
			// window, which is store.MaxSearchLimit deep — not in the complete
			// result set, which this server cannot enumerate. So an anchor that
			// exists but sits beyond the window is reported as not found.
			//
			// anchorNotFound is the correct error either way: §5.5 defines it
			// as "An anchor argument was supplied, but it cannot be found in
			// the results of the query", and "the results of the query" is, for
			// this server, exactly the bounded window it can produce. The
			// description says so, so a developer sees the boundedness rather
			// than concluding the message vanished.
			return 0, jmap.NewMethodError(jmap.CodeAnchorNotFound).
				WithDescription("the anchor was not found within the first %d results this server windows over", len(matches))
		}
		start := idx + req.AnchorOffset
		if start < 0 {
			start = 0
		}
		// start is non-negative by the clamp above, so the conversion is exact.
		return uint64(start), nil //nolint:gosec // clamped to >= 0 on the line above
	}

	// §5.5 position: "The zero-based index of the first id ... If a negative
	// value is given, it is an offset from the end of the list. Specifically,
	// the negative value MUST be added to the total number of results given the
	// filter, and if still negative, it's clamped to '0'."
	//
	// "the total number of results given the filter" is, again, the bounded
	// window: a negative position over a filter with more matches than the
	// window counts back from the window's end, not the true end. Same
	// boundedness, and the same reason it is acceptable — a client paging
	// backwards from the end of a 200-deep window gets a consistent, stable
	// answer for as long as the query state holds.
	var pos int64
	if req.Position != nil {
		pos = *req.Position
	}
	if pos < 0 {
		pos += n
		if pos < 0 {
			pos = 0
		}
	}
	// pos is non-negative here: it was either given as >= 0, or clamped above.
	return uint64(pos), nil //nolint:gosec // clamped to >= 0 on the lines above
}

// effectiveQueryLimit applies the server's maximum to the requested limit,
// reporting whether the server changed it (which §5.5 requires be echoed).
func effectiveQueryLimit(requested *uint64) (limit uint64, serverLimited bool) {
	ceiling := uint64(DefaultSearchWindow)
	if requested == nil {
		// §5.5: "If null, no limit presumed. The server MAY choose to enforce a
		// maximum 'limit' argument. In this case, if a greater value is given
		// (or if it is null), the limit is clamped to the maximum; the new
		// limit is returned with the response so the client is aware."
		return ceiling, true
	}
	if *requested > ceiling {
		return ceiling, true
	}
	return *requested, false
}

// queryStateFor derives the §5.5 queryState from the account's data state.
//
// §5.5 requires: "This string MUST change if the results of the query (i.e.,
// the matching ids and their sort order) have changed. The queryState string
// MAY change if something has changed on the server, which means the results
// may have changed but the server doesn't know for sure."
//
// That second sentence licenses exactly this implementation. Computing a state
// that changes ONLY when this particular filter's results change would mean
// evaluating the filter against every write — which is a materialized view per
// live query. Instead the account's own data watermark is used: it moves on
// every message change in the account, so it always changes when the results
// change (the MUST), and it sometimes changes when they did not (the
// explicitly permitted MAY).
//
// The cost of the coarseness is a client occasionally refetching a list that
// did not change. The cost of the alternative — a state that fails to move
// when results did — is a client showing stale mail forever, which is not a
// tradeoff worth making.
//
// It is prefixed rather than passed through so that a queryState can never be
// mistaken for the /get state string a client hands to Email/changes: the two
// are different cursors with different meanings, and RFC 8620 keeps them in
// separate namespaces (§5.5's queryState is only meaningful "when compared to
// future responses to a query with the same type/sort/filter").
func queryStateFor(dataState string) string {
	return "q" + dataState
}

// parseQuery decodes and validates the common /query arguments.
func parseQuery(ctx context.Context, args json.RawMessage) (*queryRequest, jmap.Caller, *jmap.MethodError) {
	caller, ok := jmap.CallerFromContext(ctx)
	if !ok {
		return nil, caller, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}

	var req queryRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, caller, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("arguments did not parse: %v", err)
	}
	if req.AccountID == "" {
		return nil, caller, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("the accountId argument is required")
	}
	if req.AccountID != caller.JMAPAccountID() {
		return nil, caller, jmap.NewMethodError(jmap.CodeAccountNotFound)
	}

	// §5.5 limit: "If a negative value is given, the call MUST be rejected with
	// an 'invalidArguments' error." A negative number cannot land in a uint64,
	// so it surfaces here as a JSON unmarshal failure into *uint64 — which
	// json reports as an error and the parse above already turned into
	// invalidArguments. This re-check covers the remaining case of a value too
	// large to be meaningful, keeping the error the RFC's rather than a silent
	// clamp.
	if req.Limit != nil && *req.Limit > uint64(1)<<32 {
		return nil, caller, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("limit is implausibly large")
	}

	return &req, caller, nil
}

// ---------------------------------------------------------------------------
// filter translation (RFC 8621 §4.4.1 -> the store repertoire)
// ---------------------------------------------------------------------------

// searchFilter is the repertoire's expressible filter: exactly the fields
// store.SearchQuery carries, in this package's vocabulary.
//
// Every field here corresponds to a validated S3 shape. There is no room in
// this struct for a condition the store cannot serve, which is the point: a
// filter that does not fit is refused at translation rather than half-applied.
type searchFilter struct {
	// text is the FTS term. RFC 8621 §4.4.1 defines several text conditions
	// (text, from, to, subject) that all become this one field — see
	// translateCondition for why that is a faithful reading and where it is
	// narrower than the RFC.
	text string

	mailboxID  *int64
	since      *time.Time
	before     *time.Time
	unreadOnly bool
	keyword    string

	// accountWide is RFC 8620 §5.5's `filter: null` — "all objects in the
	// account of this type". It is served by store.ListAccountMessages (J4).
	//
	// It is its own field rather than an inference from "no text and no
	// mailbox", because the two are different requests: `filter: null` asks for
	// the whole account, while an empty filter OBJECT (`{}`) is a condition with
	// zero properties, which §4.4.1 says "MUST always evaluate to true" but
	// which arrives through a different path and stays refused for the reason
	// translateCondition documents.
	accountWide bool
}

// sortSpec is the translated sort.
type sortSpec struct {
	byRelevance bool
	// ascending applies to the receivedAt sort. The repertoire orders by date
	// DESC natively; an ascending sort is served by reversing the bounded
	// window, which is exact because the window is the whole result set the
	// server exposes.
	ascending bool

	// keyword is a §4.4.2 hasKeyword PRIMARY comparator: messages carrying it
	// group ahead of (or behind) those that do not, with the receivedAt
	// comparator breaking ties. Empty means no keyword grouping.
	//
	// See translateSort for why this one multi-comparator shape is served while
	// the general case is still refused.
	keyword string
	// keywordFirst is the hasKeyword comparator's direction: true puts the
	// messages that HAVE the keyword first, which is isAscending:false.
	keywordFirst bool
}

// translateFilter maps a §4.4.1 filter onto the repertoire, or refuses.
//
// It is the ENTRY point: it translates the whole filter and then checks that
// the result is answerable. The answerability check belongs here rather than
// in the recursive step, because a single condition of an AND — "notKeyword
// $seen", say — is perfectly translatable on its own and only has to name a
// mailbox or a text once the conjunction is complete.
func translateFilter(raw json.RawMessage) (searchFilter, *jmap.MethodError) {
	f, merr := translateNode(raw)
	if merr != nil {
		return f, merr
	}
	// A filter with only a date range or only a keyword names no mailbox and no
	// text. The account-wide listing (J4) can serve the plain `filter: null`
	// case, but NOT one carrying conditions the account-wide method has no
	// parameters for — that would silently drop the condition, which is the
	// privacy failure this file exists to avoid.
	if f.text == "" && f.mailboxID == nil && !f.accountWide {
		return f, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("this filter needs an inMailbox or a text condition to be answerable")
	}
	// A keyword filter is only answerable on the TEXT path: it becomes the
	// `ms.keywords @> ARRAY[...]` predicate of store.SearchQuery, and the
	// folder-view method (ListMailboxMessages) takes no keyword parameter and
	// returns no keywords column to post-filter on.
	//
	// Refusing is the only honest option — the alternative is a folder listing
	// that silently ignores the label the user filtered by. The store change
	// that lifts this is in the J3 report (a keyword-aware folder view).
	if f.keyword != "" && f.text == "" {
		return f, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("filter condition %q is not supported without a text condition: "+
				"the folder view has no keyword predicate", "hasKeyword")
	}
	return f, nil
}

// translateNode translates one filter node — an operator or a condition —
// without judging whether the result is answerable on its own.
func translateNode(raw json.RawMessage) (searchFilter, *jmap.MethodError) {
	var f searchFilter
	if len(raw) == 0 || string(raw) == "null" {
		// §5.5: "If null, all objects in the account of this type are included
		// in the results."
		//
		// J3 reported this as a repertoire gap and refused it. J4 closed it:
		// store.ListAccountMessages is the account-wide, date-ordered shape,
		// served by the same (account_id, date DESC) index as shape #1 and
		// bounded by the same LIMIT. The refusal was blocking real software —
		// the official conformance suite enumerates the account in its SETUP
		// step, so it could not run a single test against this server.
		//
		// accountWide is what carries the intent down to SearchEmails, and it is
		// a distinct field rather than "no text and no mailbox" so that an
		// EMPTY filter object cannot be mistaken for an explicit null one.
		f.accountWide = true
		return f, nil
	}

	// A filter is either a FilterOperator (it has an "operator" property) or a
	// FilterCondition (§5.5).
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return f, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("the filter is not an object")
	}
	if _, isOperator := probe["operator"]; isOperator {
		return translateOperator(raw)
	}
	return translateCondition(probe)
}

// translateOperator handles the §5.5 FilterOperator.
//
// AND is supported because the repertoire's own WHERE clause is a conjunction:
// every SearchQuery field ANDs with the others, so an AND of conditions the
// repertoire understands is itself a shape it understands.
//
// OR and NOT are refused. This is the "lo más potente del mercado" rule
// yielding to the harder constraint of §4.3: serving OR needs either a UNION
// of two index scans or a post-filter over an unbounded candidate set, and
// serving NOT needs the complement of a match set — none of which is in the
// eight validated shapes, and all of which would reintroduce the unbounded
// work S3 showed sinks the instance under concurrency. Refusing names the node
// so the client can, per §5.5, "suggest that the user simplify their search".
func translateOperator(raw json.RawMessage) (searchFilter, *jmap.MethodError) {
	var op struct {
		Operator   string            `json:"operator"`
		Conditions []json.RawMessage `json:"conditions"`
	}
	if err := json.Unmarshal(raw, &op); err != nil {
		return searchFilter{}, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("the filter operator did not parse: %v", err)
	}

	if strings.ToUpper(op.Operator) != "AND" {
		return searchFilter{}, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("the %q filter operator is not supported; this server supports AND of simple conditions", op.Operator)
	}

	// Merge the conditions. A conflict — two different mailboxes, two different
	// texts — is refused rather than silently resolved: "in mailbox A AND in
	// mailbox B" matches nothing in a store where a message has one mailbox,
	// and answering with the results of one of them would be wrong in a way
	// the user cannot see.
	var merged searchFilter
	for i, cond := range op.Conditions {
		sub, merr := translateNode(cond)
		if merr != nil {
			return searchFilter{}, merr
		}
		var err error
		merged, err = mergeFilters(merged, sub)
		if err != nil {
			return searchFilter{}, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
				WithDescription("filter condition %d cannot be combined: %v", i, err)
		}
	}
	return merged, nil
}

// mergeFilters ANDs two translated filters, refusing contradictions the
// repertoire cannot express.
func mergeFilters(a, b searchFilter) (searchFilter, error) {
	out := a

	if b.text != "" {
		if out.text != "" && out.text != b.text {
			// Two text conditions would need two tsquery predicates. The
			// repertoire takes one — and websearch_to_tsquery already ANDs the
			// words within it, so the client can express this as one condition.
			return out, fmt.Errorf("two different text conditions in one filter")
		}
		out.text = b.text
	}
	if b.mailboxID != nil {
		if out.mailboxID != nil && *out.mailboxID != *b.mailboxID {
			return out, fmt.Errorf("two different inMailbox conditions")
		}
		out.mailboxID = b.mailboxID
	}
	if b.since != nil {
		// The later "after" wins: it is the stricter bound, so the conjunction
		// is exact.
		if out.since == nil || b.since.After(*out.since) {
			out.since = b.since
		}
	}
	if b.before != nil {
		if out.before == nil || b.before.Before(*out.before) {
			out.before = b.before
		}
	}
	if b.unreadOnly {
		out.unreadOnly = true
	}
	if b.keyword != "" {
		if out.keyword != "" && out.keyword != b.keyword {
			return out, fmt.Errorf("two different keyword conditions")
		}
		out.keyword = b.keyword
	}
	return out, nil
}

// translateCondition maps one §4.4.1 FilterCondition onto the repertoire.
//
// §4.4.1: "If multiple properties are specified, ALL must apply for the
// condition to be true (it is equivalent to splitting the object into
// one-property conditions and making them all the child of an AND filter
// operator)" — which is why the properties below accumulate into one filter.
func translateCondition(props map[string]json.RawMessage) (searchFilter, *jmap.MethodError) {
	var f searchFilter

	// §4.4.1: "If zero properties are specified on the FilterCondition, the
	// condition MUST always evaluate to true" — i.e. the whole account, which
	// is the enumeration the repertoire cannot do (see translateFilter).
	if len(props) == 0 {
		return f, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("an empty filter condition matches the whole account, which this server cannot enumerate")
	}

	for name, raw := range props {
		switch name {
		case "inMailbox":
			var wire string
			if err := json.Unmarshal(raw, &wire); err != nil {
				return f, unsupportedNode(name, "not a string")
			}
			id, err := DecodeMailboxID(wire)
			if err != nil {
				// A mailbox id this server never issued names no mailbox. The
				// filter is valid JMAP but unsatisfiable; refusing it names the
				// node, which is more useful to a client than an empty list.
				return f, unsupportedNode(name, "not a mailbox id issued by this server")
			}
			f.mailboxID = &id

		case "text", "from", "to", "subject":
			// The store indexes ONE tsvector per message, built from the header
			// fields and the body text (S3's corpus and internal/store's
			// generated tsv column). It has no per-field index, so a targeted
			// from/to/subject search cannot be narrowed to that field — it can
			// only be answered as a full-text search over everything.
			//
			// §4.4.1 permits the breadth for `text` ("The server MUST look up
			// text in the From, To, Cc, Bcc, and Subject header fields ... and
			// SHOULD look inside any text/* ... The server MAY extend the
			// search to any additional textual property") but NOT the reverse:
			// `from` means "Looks for the text in the From header field", and
			// answering it with a whole-message match returns messages that
			// merely MENTION the address in their body.
			//
			// That is over-matching, not under-matching, and this server
			// accepts it deliberately: the alternative is refusing the three
			// most common searches a mail client issues. It is recorded as the
			// one place Moov is broader than the RFC, it is what S3 measured
			// (shape #6 is "remitente" over the same single tsv), and the
			// per-field indexes that would make it exact are named in the J3
			// report.
			var text string
			if err := json.Unmarshal(raw, &text); err != nil {
				return f, unsupportedNode(name, "not a string")
			}
			if strings.TrimSpace(text) == "" {
				return f, unsupportedNode(name, "empty search text")
			}
			if f.text != "" && f.text != text {
				return f, unsupportedNode(name, "a second, different text condition")
			}
			f.text = text

		case "after":
			// §4.4.1: "after: UTCDate — The 'receivedAt' date-time of the Email
			// must be the same or after this date-time" — inclusive, which is
			// exactly SearchQuery.Since's `>=`.
			t, merr := parseUTCDate(name, raw)
			if merr != nil {
				return f, merr
			}
			f.since = &t

		case "before":
			// §4.4.1: "before: UTCDate — The 'receivedAt' date-time of the
			// Email must be before this date-time" — exclusive.
			t, merr := parseUTCDate(name, raw)
			if merr != nil {
				return f, merr
			}
			f.before = &t

		case "hasKeyword":
			kw, merr := stringNode(name, raw)
			if merr != nil {
				return f, merr
			}
			merr = f.applyHasKeyword(kw)
			if merr != nil {
				return f, merr
			}

		case "notKeyword":
			kw, merr := stringNode(name, raw)
			if merr != nil {
				return f, merr
			}
			// The repertoire's only negative predicate is UnreadOnly, which is
			// exactly "not $seen". Every other notKeyword would need a NOT over
			// the keywords array, which is not a validated shape.
			if strings.EqualFold(kw, KeywordSeen) {
				f.unreadOnly = true
				break
			}
			return f, unsupportedNode(name,
				fmt.Sprintf("only notKeyword:%q is supported (it is the unread filter); %q would need a negated keyword index", KeywordSeen, kw))

		default:
			// Everything else in §4.4.1 — inMailboxOtherThan, minSize, maxSize,
			// the three inThread keyword conditions, cc, bcc, body, header,
			// hasAttachment — has no shape in the repertoire. §5.5's
			// unsupportedFilter is precisely "The filter is syntactically
			// valid, but the server cannot process it."
			//
			// hasAttachment and cc/bcc are the ones worth closing first: the
			// store HAS a has_attachments column and a cc column, so they are a
			// store-method away rather than an index away. Named in the J3
			// report.
			return f, unsupportedNode(name, "not supported by this server")
		}
	}

	return f, nil
}

// applyHasKeyword maps a §4.4.1 hasKeyword onto the repertoire.
func (f *searchFilter) applyHasKeyword(kw string) *jmap.MethodError {
	// $seen is the system flag the store keeps as a bit, and the repertoire
	// exposes only its negative (UnreadOnly). "has $seen" is therefore NOT
	// expressible: there is no ReadOnly field, and inverting UnreadOnly is not
	// a thing a caller may do.
	if strings.EqualFold(kw, KeywordSeen) {
		return unsupportedNode("hasKeyword",
			fmt.Sprintf("%q is not filterable; the repertoire exposes only its negation (notKeyword:%q)", KeywordSeen, KeywordSeen))
	}

	// Everything else goes to the keywords array, which is where the store
	// keeps user keywords AND where arbitration A6 puts labels — so a label
	// filter is a keyword filter, by design.
	//
	// The system flags other than \Seen ($flagged, $answered, $draft) live in
	// the flags bitmask, not the keywords array, so filtering on them would
	// need a bitmask predicate the repertoire does not expose. They are refused
	// rather than silently missed: a client filtering for $flagged must not get
	// a list that quietly ignores the condition.
	switch {
	case strings.EqualFold(kw, KeywordFlagged),
		strings.EqualFold(kw, KeywordAnswered),
		strings.EqualFold(kw, KeywordDraft):
		return unsupportedNode("hasKeyword",
			fmt.Sprintf("%q is an IMAP system flag stored as a bitmask; the repertoire has no predicate for it", kw))
	}

	if f.keyword != "" && f.keyword != kw {
		return unsupportedNode("hasKeyword", "a second, different keyword condition")
	}
	f.keyword = kw
	return nil
}

// stringNode decodes a string-valued filter property.
func stringNode(name string, raw json.RawMessage) (string, *jmap.MethodError) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", unsupportedNode(name, "not a string")
	}
	if s == "" {
		return "", unsupportedNode(name, "empty value")
	}
	return s, nil
}

// parseUTCDate decodes a §4.4.1 UTCDate.
func parseUTCDate(name string, raw json.RawMessage) (time.Time, *jmap.MethodError) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return time.Time{}, unsupportedNode(name, "not a UTCDate string")
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, unsupportedNode(name, "not an RFC 3339 date-time")
	}
	return t.UTC(), nil
}

// unsupportedNode builds the §5.5 unsupportedFilter error NAMING the node that
// could not be translated, which is what lets a client tell the user which part
// of their search to simplify.
func unsupportedNode(node, why string) *jmap.MethodError {
	return jmap.NewMethodError(jmap.CodeUnsupportedFilter).
		WithDescription("filter condition %q is not supported: %s", node, why)
}

// ---------------------------------------------------------------------------
// sort translation (RFC 8621 §4.4.2)
// ---------------------------------------------------------------------------

// translateSort maps the §5.5 sort array onto the repertoire's two orders.
func translateSort(sort []comparator) (sortSpec, *jmap.MethodError) {
	// §5.5: "If all comparators are the same (this includes the case where an
	// empty array or null is given as the 'sort' argument), the sort order is
	// server dependent, but it MUST be stable between calls." Newest-first is
	// the server-dependent choice, and it is the store's native, fastest shape.
	if len(sort) == 0 {
		return sortSpec{ascending: false}, nil
	}

	// A hasKeyword comparator followed by receivedAt is served as ONE shape.
	//
	// # Why this specific pair, when the general multi-key sort is still refused
	//
	// It is what a real client asks for. Bulwark's message list opens every
	// folder with sort: [hasKeyword $pinned desc, receivedAt desc] — pinned mail
	// on top, everything else newest-first — and until J4 that request was
	// answered with unsupportedSort, which made the inbox render EMPTY while the
	// folder counts beside it showed four messages. RFC 8621 §4.4.2 lists
	// hasKeyword among the properties a server SHOULD support sorting on, so the
	// refusal was a genuine conformance gap, not a client quirk.
	//
	// It is also cheap and EXACT here, which is what separates it from the sorts
	// still refused. The comparator partitions the result window into "has the
	// keyword" and "does not", and the store now returns each row's keywords
	// (added in J4 alongside this). Because the partition is applied to the SAME
	// bounded window the query already fetched — never to a larger candidate set
	// — it costs a stable sort over at most store.MaxSearchLimit rows and adds no
	// database work at all. There is no unbounded scan hiding in it, which is the
	// property L2 §4.3 actually protects.
	//
	// Everything else stays refused: a general multi-key sort over properties the
	// store cannot order by (size, from, subject) would need either sortable
	// indexes that do not exist or a post-sort over an unbounded set, and §5.5's
	// rule that "a later comparator decides ties" means applying only the first
	// would return an order the client did not ask for and then break its paging.
	if len(sort) > 1 {
		return translateKeywordSort(sort)
	}

	c := sort[0]
	// §5.5: collation applies to string comparisons. Neither of this server's
	// sorts compares strings (one is a date, one is a rank), so a collation is
	// meaningless here — and §5.5 makes an unrecognized collation an
	// unsupportedSort. Rejecting any explicit collation keeps the promise in
	// session.go's collationAlgorithms (which is empty) truthful.
	if c.Collation != nil && *c.Collation != "" {
		return sortSpec{}, jmap.NewMethodError(jmap.CodeUnsupportedSort).
			WithDescription("collation %q is not supported; this server advertises no collation algorithms", *c.Collation)
	}

	switch c.Property {
	case SortReceivedAt:
		return sortSpec{ascending: c.ascending()}, nil

	case SortRelevance:
		// Relevance is inherently descending: the best match first. §5.5 lets
		// isAscending reverse a comparator, and reversing a bounded relevance
		// window means "the least relevant of the 200 most recent", which is
		// not something to serve on purpose.
		if c.ascending() {
			return sortSpec{}, jmap.NewMethodError(jmap.CodeUnsupportedSort).
				WithDescription("the %q sort is descending only; pass isAscending:false", SortRelevance)
		}
		return sortSpec{byRelevance: true}, nil

	case SortHasKeyword:
		// A lone hasKeyword comparator: group by the keyword, and let the
		// server's default newest-first order break the ties.
		kw, merr := keywordComparator(c)
		if merr != nil {
			return sortSpec{}, merr
		}
		return sortSpec{keyword: kw, keywordFirst: !c.ascending(), ascending: false}, nil

	default:
		// §5.5 unsupportedSort: "The 'sort' is syntactically valid, but it
		// includes a property the server does not support sorting on".
		//
		// §4.4.2 lists size/from/to/subject/sentAt as SHOULD — all of them need
		// either a sortable index the store does not have or the thread
		// derivation it cannot afford. They are named in the J3 report with what
		// each would cost. (hasKeyword, the fourth SHOULD, is served above.)
		return sortSpec{}, jmap.NewMethodError(jmap.CodeUnsupportedSort).
			WithDescription("sorting on %q is not supported; this server sorts on %q, %q or %q",
				c.Property, SortReceivedAt, SortRelevance, SortHasKeyword)
	}
}

// translateKeywordSort handles the one multi-comparator shape this server
// serves: [hasKeyword, receivedAt].
//
// The shape is pinned deliberately rather than generalized. Accepting an
// arbitrary list of comparators would mean promising an ordering the repertoire
// cannot produce; accepting exactly the pair a conforming client actually sends
// — and that the bounded window can be sorted by exactly — closes the real gap
// without opening that door.
func translateKeywordSort(sort []comparator) (sortSpec, *jmap.MethodError) {
	if len(sort) != 2 || sort[0].Property != SortHasKeyword || sort[1].Property != SortReceivedAt {
		return sortSpec{}, jmap.NewMethodError(jmap.CodeUnsupportedSort).
			WithDescription(
				"this server serves a single comparator, or the pair [%q, %q]; %d comparators were given",
				SortHasKeyword, SortReceivedAt, len(sort))
	}

	for _, c := range sort {
		if c.Collation != nil && *c.Collation != "" {
			return sortSpec{}, jmap.NewMethodError(jmap.CodeUnsupportedSort).
				WithDescription("collation %q is not supported; this server advertises no collation algorithms",
					*c.Collation)
		}
	}

	kw, merr := keywordComparator(sort[0])
	if merr != nil {
		return sortSpec{}, merr
	}
	return sortSpec{
		keyword: kw,
		// §4.4.2: the comparator sorts on "whether the Email has the keyword".
		// isAscending:false therefore means "those that have it come first",
		// which is what a client pinning messages to the top asks for.
		keywordFirst: !sort[0].ascending(),
		ascending:    sort[1].ascending(),
	}, nil
}

// keywordComparator validates a §4.4.2 hasKeyword comparator and returns its
// keyword.
func keywordComparator(c comparator) (string, *jmap.MethodError) {
	// §4.4.2: the hasKeyword comparator "MUST" carry the keyword argument. A
	// comparator without one names no partition and cannot be honored.
	if strings.TrimSpace(c.Keyword) == "" {
		return "", jmap.NewMethodError(jmap.CodeUnsupportedSort).
			WithDescription("the %q sort requires a non-empty %q argument", SortHasKeyword, "keyword")
	}
	return c.Keyword, nil
}

// sortIDs is a helper for the deterministic ordering of equal-keyed results.
// The store already orders by date DESC; ties break on id so the order is
// total, which §5.5 requires ("it MUST be stable between calls").
//
// keywordFirst, when the sort carries a §4.4.2 hasKeyword comparator, is applied
// as the PRIMARY key: the hits carrying the keyword group ahead of those that do
// not (or behind, for an ascending comparator), and the date comparison below
// decides ties within each group — exactly the "a later comparator decides ties"
// semantics of §5.5.
func sortIDsStable(results []searchHit, ascending bool, keywordSort bool, keywordFirst bool) []int64 {
	sort.SliceStable(results, func(i, j int) bool {
		if keywordSort && results[i].hasKeyword != results[j].hasKeyword {
			// The one that HAS the keyword sorts first when keywordFirst.
			return results[i].hasKeyword == keywordFirst
		}
		if results[i].date.Equal(results[j].date) {
			// A stable tiebreak on id, in the same direction as the dates, so
			// paging never revisits or skips a row.
			if ascending {
				return results[i].id < results[j].id
			}
			return results[i].id > results[j].id
		}
		if ascending {
			return results[i].date.Before(results[j].date)
		}
		return results[i].date.After(results[j].date)
	})
	out := make([]int64, 0, len(results))
	for _, r := range results {
		out = append(out, r.id)
	}
	return out
}
