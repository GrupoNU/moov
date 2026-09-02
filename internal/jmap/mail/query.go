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

	// CollapseThreads is RFC 8621 §4.4.3's extra argument: "If true, Emails in
	// the same Thread as a previous Email in the list (given the filter and sort
	// order) will be removed from the list."
	//
	// It is SERVED since L3 epic E1 (store.ListCollapsedMessages). It used to be
	// refused with "this server has no thread index yet" — true when written and
	// stale from migration 0004 onward, which is how a refusal outlives its
	// reason. The one combination still refused says exactly why, and a test
	// pins the message; see collapseRefusal.
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
	// §4.4.3 collapseThreads. The ONE combination this server cannot collapse,
	// stated with its actual reason (see collapseRefusal).
	if merr := collapseRefusal(req, order); merr != nil {
		return nil, merr
	}

	state, err := d.State.EmailState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading email state", err)
	}

	// Fetch exactly as deep as this request needs, then window it locally.
	//
	// The depth is position+limit, not a fixed window: the repertoire pages
	// with a keyset cursor (store.SearchCursor), so reaching row 400 costs two
	// pages rather than one impossible one. A request that asks for the first
	// page still costs exactly one page, which is what keeps the common case at
	// the latency S3 measured.
	limit, serverLimited := effectiveQueryLimit(req.Limit)
	reach, merr := queryReach(req, limit)
	if merr != nil {
		return nil, merr
	}

	// The collapsed and uncollapsed reads are separate repertoire shapes with
	// separate bounds (search.go SearchThreads), not one shape with a flag, so
	// the choice is made here rather than pushed down as an argument.
	var matches []int64
	if req.CollapseThreads {
		matches, err = d.Search.SearchThreads(ctx, caller.AccountID, filter, order, reach)
	} else {
		matches, err = d.Search.SearchEmails(ctx, caller.AccountID, filter, order, reach)
	}
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
		total, merr := d.queryTotal(ctx, caller.AccountID, filter, uint64(len(matches)), reach)
		if merr != nil {
			return nil, merr
		}
		// A nil total is the deliberate omission documented in queryTotal.
		resp.Total = total
	}

	return resp, nil
}

// collapseRefusal decides whether this request's collapseThreads can be honored.
//
// # What is served
//
// Everything the date-ordered repertoire serves: the folder view
// (inMailbox, with or without the [hasKeyword, receivedAt] pair a real client
// opens every folder with), the account-wide `filter: null` listing, and
// full-text search — each with the date-range and unread narrowing, collapsed in
// the database inside a bounded window (store.ListCollapsedMessages).
//
// # What is refused, and why it is not the old reason
//
// Exactly one shape: collapseThreads together with the "relevance" sort.
//
// That sort is not a general relevance ranking — it is a bounded re-rank of the
// store.RankCandidateWindow most recent matches (S3 mitigation #102, documented
// at SortRelevance). Its output order is therefore not the index's order, so
// there is no keyset cursor that can resume it, so there is no SECOND window to
// collapse into. A collapsed relevance list could be served for its first window
// and could never be paged, and a list that silently stops paging hides mail —
// which is the failure this file refuses things to avoid.
//
// unsupportedSort is the right code rather than unsupportedFilter: §5.5 defines
// it as "The 'sort' is syntactically valid, but it includes a property the
// server does not support sorting on", and it is the SORT, not the filter, that
// makes this combination unanswerable. The client's remedy is named in the
// message — drop the sort, keep the collapse — which is a request this server
// serves.
//
// The old refusal ("this server has no thread index yet") was correct when it
// was written and became false the day migration 0004 landed, and nothing made
// it say so. That is why this one names a structural property of the sort rather
// than the absence of a feature: a reason that cannot go stale without the code
// changing underneath it.
func collapseRefusal(req *queryRequest, order sortSpec) *jmap.MethodError {
	if !req.CollapseThreads || !order.byRelevance {
		return nil
	}
	return jmap.NewMethodError(jmap.CodeUnsupportedSort).
		WithDescription("collapseThreads cannot be combined with the %q sort: that sort ranks a bounded "+
			"window of recent matches rather than an index order, so a collapsed result has no cursor to "+
			"page with; request the same filter with the %q sort to collapse it",
			SortRelevance, SortReceivedAt)
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
// the search was NOT truncated, the number of matches IS the total, because it
// returned every match there was.
//
// # There are TWO bounds, and the test is against the tighter of them
//
// A search can be cut short by either:
//
//   - the WINDOW, store.MaxSearchLimit deep, which is how far the repertoire
//     will look at all; or
//   - the REACH, position+limit clamped to MaxQueryReach, which is how far THIS
//     request asked it to look.
//
// A result is exhausted only if it is shorter than BOTH. Checking only the
// window was a bug, and a silent one: SearchEmails and SearchThreads return at
// most `reach` ids, so a request with limit:1 comes back with one id — trivially
// fewer than the 200-deep window — and the server reported total:1 for a mailbox
// holding thousands. Nothing caught it because every existing calculateTotal
// test used the default limit, where reach equals the window and the two bounds
// coincide. L3 epic E1's collapsed-query tests used limit:1 and it surfaced on
// the first run.
//
// Checking only reach would break the other direction just as quietly: a
// window-truncated result that happens to be shorter than a large requested
// reach would be reported as complete.
func (d *Deps) queryTotal(ctx context.Context, accountID int64, f searchFilter, matched uint64, reach int) (*uint64, *jmap.MethodError) {
	// Both are small positive ints — searchWindow() is a constant that
	// RegisterQueryMethods bounds, and queryReach clamps reach to MaxQueryReach
	// — so the conversions are exact. The guards are written out rather than
	// asserted so gosec can see them.
	bound := d.searchWindow()
	if reach > 0 && reach < bound {
		bound = reach
	}
	if bound > 0 && matched < uint64(bound) {
		total := matched
		return &total, nil
	}

	// The tighter bound was filled, so the true total is >= it and unknown
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

// queryReach computes how many rows this request must fetch to serve its page.
//
// # The three cases
//
//   - A non-negative position needs position+limit rows: everything up to the
//     page, plus the page. This is the ordinary paging case, and it is the one
//     that used to be impossible — the old code fetched a fixed window and
//     SLICED it, so any position at or past the window returned nothing.
//   - A NEGATIVE position is an offset from the end (§5.5), and the end of a
//     result set cannot be known without walking it. It therefore reaches the
//     ceiling: the answer is exact whenever the result set fits inside it,
//     which is the same honest boundedness the anchor case has always had.
//   - An ANCHOR is looked for in the results, and its position is likewise not
//     known in advance, so it reaches the ceiling too.
//
// Every case is clamped to MaxQueryReach, so no single request can ask the
// database for unbounded work — the guarantee of L2 §4.3, preserved now that
// depth is a variable rather than a constant.
func queryReach(req *queryRequest, limit uint64) (int, *jmap.MethodError) {
	if req.Anchor != nil || (req.Position != nil && *req.Position < 0) {
		return MaxQueryReach, nil
	}

	var pos uint64
	if req.Position != nil {
		// Non-negative by the branch above.
		pos = uint64(*req.Position) //nolint:gosec // guarded by the check above
	}
	if pos > MaxQueryReach {
		// §5.5 says a position at or past the end yields an empty list, not an
		// error, and this server cannot see past its reach — so a position
		// beyond it is answered with the empty list rather than a refusal, and
		// the response's `limit` tells the client a server bound applied.
		return MaxQueryReach, nil
	}

	reach := pos + limit
	if reach > MaxQueryReach {
		reach = MaxQueryReach
	}
	return int(reach), nil //nolint:gosec // clamped to MaxQueryReach above
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

	// The E3 conditions. Each one is a §4.4.1 FilterCondition the store gained a
	// predicate for in L3 epic E3; store.Narrowing documents what each costs and
	// which of them migration 0008 built an index for.
	hasAttachment *bool
	cc            string
	bcc           string
	minSize       *int64
	maxSize       *int64

	// flagsAll and flagsNone are the system-flag BITS a hasKeyword / notKeyword
	// names. See applyHasKeyword for why the IMAP system flags need a different
	// predicate from every other keyword.
	//
	// They are a raw uint64 rather than store.Flags for the reason
	// systemFlagKeywords states: the bit values are restated in this package as
	// untyped constants so the translation layer does not depend on the store,
	// and the adapter converts. A wrong bit here is caught by
	// TestSystemFlagBitsMatchTheStore, which compares the two tables.
	flagsAll  uint64
	flagsNone uint64

	// excludeMailboxIDs is §4.4.1's inMailboxOtherThan, and it carries the
	// Gmail default-exclusion policy (canon §2.5). defaultExclusion records
	// whether THIS server put those ids there or the client did — see
	// applyDefaultExclusion, which needs to tell an explicit `in:spam` from the
	// absence of any mailbox condition.
	excludeMailboxIDs []int64
	defaultExclusion  bool

	// or, when non-empty, holds the translated branches of a §5.5 OR operator.
	// A filter carrying them is served as a UNION of bounded searches rather
	// than as one WHERE clause; see translateOperator for the boundedness rule
	// that decides which OR shapes are accepted.
	or []searchFilter

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
	if len(f.or) > 0 {
		// Each branch was checked for answerability as it was translated
		// (translateOr), and a union has no conditions of its own to check.
		// The default exclusion is applied per branch, because "exclude Spam
		// and Trash from this search" is a property of each search, not of the
		// merge.
		for i := range f.or {
			f.or[i] = applyDefaultExclusion(f.or[i])
		}
		return f, nil
	}
	f, merr = answerable(f)
	if merr != nil {
		return f, merr
	}
	return applyDefaultExclusion(f), nil
}

// answerable reports whether a translated filter names a shape the repertoire
// serves, or the §5.5 error saying why not.
//
// It is separated from translateFilter because translateOr needs exactly this
// test, applied to each branch: the rule that makes OR bounded is "every branch
// must be a filter this server would serve on its own", and this function IS
// "would serve on its own". Sharing it is what keeps the two from drifting into
// different notions of answerable.
//
// It RETURNS the filter because deciding a bare label filter is answerable also
// decides its SCOPE (accountWide), and those two must not come apart: a caller
// that took the verdict but dropped the scope would send a keyword filter down
// the folder-view branch, which has no mailbox to walk. Returning the value
// makes losing it a compile error rather than an empty inbox.
func answerable(f searchFilter) (searchFilter, *jmap.MethodError) {
	// A BARE CUSTOM KEYWORD IS ANSWERABLE ACCOUNT-WIDE — the label view.
	//
	// `{"hasKeyword":"$label:work"}` with nothing beside it is what a Gmail
	// label view IS (canon §2.1): clicking a label in the sidebar shows every
	// message carrying it, in every folder. The PWA's sidebar (L3 epic E8)
	// sends exactly this shape, and until migration 0011 it was refused — so
	// the feature rendered an empty list from a refusal rather than from an
	// empty mailbox.
	//
	// It is served by the account-wide walk carrying the same
	// `ms.keywords @> ARRAY[...]` containment predicate the text path always
	// had (store.AccountListQuery.Keyword). Measured at 400,000 messages:
	// 89 ms p95 at the realistic 2% label density, against 230 ms before 0011
	// made the message_state probe index-only.
	//
	// The scope is set HERE rather than by the caller, because a label filter
	// naming no mailbox means "the whole account" — and saying so explicitly
	// keeps the account-wide walk's contract ("this shape enumerates the
	// account") true instead of inferred.
	if f.keyword != "" && f.text == "" && f.mailboxID == nil && !f.accountWide {
		f.accountWide = true
	}

	// A filter with only a date range names no mailbox and no text. The
	// account-wide listing (J4) can serve the plain `filter: null` case, but
	// NOT one carrying conditions the account-wide method has no parameters
	// for — that would silently drop the condition, which is the privacy
	// failure this file exists to avoid.
	if f.text == "" && f.mailboxID == nil && !f.accountWide {
		return f, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("this filter needs an inMailbox or a text condition to be answerable")
	}

	// A keyword AND a mailbox, with no text, is the one keyword shape still
	// refused — and the reason is narrow and current: the FOLDER VIEW
	// (store.ListMailboxMessages) takes no keyword parameter. The two shapes
	// that do carry the containment predicate are the text search and the
	// account-wide walk, and neither is what this filter names.
	//
	// It is refused rather than silently widened to the whole account, because
	// a client that asked for "this label, in this folder" and got the label
	// across every folder would be shown mail it excluded — the privacy failure
	// this file exists to avoid, arriving as a helpful-looking fallback.
	//
	// The remedy is named so a client can act on it: drop the mailbox (a label
	// view is account-wide anyway, canon §2.1) or add a text condition.
	if f.keyword != "" && f.text == "" && f.mailboxID != nil {
		return f, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("filter condition %q cannot be combined with %q without a text condition: "+
				"the folder view has no keyword predicate, while the account-wide label view has one — "+
				"drop the %q to search the label across the account, or add a text condition",
				"hasKeyword", "inMailbox", "inMailbox")
	}
	return f, nil
}

// applyDefaultExclusion implements Gmail's rule that a search does not return
// Spam or Trash unless it was asked to.
//
// # The behavior, and the source it comes from
//
// docs/research/06-gmail-canon.md §2.5, citing Google's own operator reference
// (support.google.com/mail/answer/7190, retrieved 2026-08-30): "Spam/Trash
// excluded by default; `in:anywhere` includes them." That is not a nicety of
// Gmail's UI — it is what makes search usable in a real mailbox, where Trash
// holds every message the user has already decided they do not want and Spam
// holds mail they never asked for. A search that surfaces both puts the
// user's own deleted mail next to the message they were looking for.
//
// # Why this is a legitimate reading of the RFC and not a deviation from it
//
// RFC 8621 §4.4.1 defines the FilterConditions and their semantics; it does not
// say what a server does with mail the user has thrown away, and it never
// requires that an unfiltered query return every message. §5.5's `filter: null`
// is the closest thing — "all objects in the account of this type" — and this
// server applies NO exclusion to it, precisely because that one has a stated
// meaning. The exclusion applies to SEARCHES: a filter carrying a condition,
// where the RFC constrains which messages match the condition and leaves the
// server's own notion of scope alone.
//
// The distinction is drawn deliberately at that line, and the three cases are:
//
//	filter: null                  -> no exclusion. §5.5 says "all objects".
//	{text:"x"}                    -> Spam and Trash excluded. The Gmail default.
//	{text:"x", inMailbox:<junk>}  -> served. That IS `in:spam`.
//	{text:"x", inMailboxOtherThan:[]} -> no exclusion. That IS `in:anywhere`.
//
// The last one is the escape hatch, and it costs the client nothing to reach:
// an explicit inMailboxOtherThan — even an empty one — is the client saying it
// has its own opinion about scope, so the server steps back. A client that has
// never heard of this behavior and wants everything sends an empty
// inMailboxOtherThan and gets everything.
//
// # Why an explicit inMailbox is enough to disable it
//
// Because `in:spam` and `in:trash` are themselves Gmail operators (canon §2.5),
// and a user who names a folder has already said which folder they mean. An
// exclusion applied on top of an explicit inMailbox would make `in:trash`
// return nothing — a control that silently does the opposite of what it says.
//
// # The mailbox ids are not resolved here
//
// This function marks the INTENT; the adapter resolves it to ids, because the
// junk and trash mailboxes are rows in the database and this package does not
// read the database (search.go's rule). The mark is a boolean rather than a
// list so that a filter can be compared, logged and tested without a store.
func applyDefaultExclusion(f searchFilter) searchFilter {
	// §5.5's account enumeration means what it says: everything.
	if f.accountWide {
		return f
	}
	// The client named a folder, or named its own exclusions. Either way it has
	// stated its scope, and the server does not add to it.
	if f.mailboxID != nil || f.excludeMailboxIDs != nil {
		return f
	}
	f.defaultExclusion = true
	return f
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

// maxOrBranches bounds how many branches one OR may have.
//
// Each branch is a SEPARATE bounded search (see translateOperator), so an OR of
// n branches costs n times one search. The bound is what keeps that product
// finite: without it a client could send a hundred-branch OR and turn one
// Email/query into a hundred index walks, which is unbounded work assembled out
// of bounded pieces — the exact failure L2 §4.3 forbids, arriving by a door the
// per-shape limits do not watch.
//
// Four is chosen against the operator language rather than the database: canon
// §2.5's OR is a search-box operator, and the searches a person actually types
// ("from:ana OR from:juan", "in:inbox OR in:archive") have two or three
// branches. A client that needs more issues more queries and merges them, where
// the cost is visible to it rather than hidden in one request.
const maxOrBranches = 4

// translateOperator handles the §5.5 FilterOperator.
//
// # AND
//
// Served, and it always has been: the repertoire's own WHERE clause is a
// conjunction, so every SearchQuery field ANDs with the others and an AND of
// conditions the repertoire understands is itself a shape it understands.
//
// # OR — served since L3 epic E3, under one rule
//
// RFC 8621 §4.4.2 lists OR among the FilterOperators, canon §2.5 puts it in
// Gmail's daily operator language, and until E3 it was refused outright with a
// reason that was true of the implementation rather than of the data: "serving
// OR needs either a UNION of two index scans or a post-filter over an unbounded
// candidate set".
//
// The first half of that sentence is the answer. A UNION OF BOUNDED SEARCHES IS
// BOUNDED. Each branch is translated independently, each is run as its own
// bounded, account-scoped, LIMITed search through the same repertoire, and the
// results are merged in memory (adapter_query.go searchUnion). No branch sees a
// larger candidate set than it would as a standalone query, and the merge is a
// sort over at most maxOrBranches * reach ids that are already in hand.
//
// THE RULE, stated so a future condition inherits it: A BRANCH OF AN OR MUST BE
// A FILTER THIS SERVER WOULD SERVE ON ITS OWN. Not "a filter that is nearly
// answerable", not "a filter that becomes answerable once the other branch
// narrows it" — a disjunction never narrows, it only widens, so a branch that
// would scan the account alone scans the account here too. translateFilter's
// answerability check is therefore applied to EVERY branch, which is what makes
// this rule mechanical rather than a promise.
//
// The rule's practical consequences, spelled out because they are what a client
// will hit:
//
//   - `{from:ana} OR {from:juan}` — two text searches, each on the composite
//     GIN. Served. (Two branches, 0.3 ms each on the bench corpus.)
//   - `{inMailbox:A} OR {inMailbox:B}` — two folder walks. Served.
//   - `{text:x} OR {hasAttachment:true}` — REFUSED, because the second branch
//     alone is "every message with an attachment in the account", which
//     translateFilter already refuses as needing an inMailbox or a text. The
//     refusal names the branch.
//
// # NOT — refused, deliberately, with the reason stated at the data
//
// §5.5 defines NOT as "all of the conditions must be FALSE". Its result set is
// the COMPLEMENT of a match set, and no index in this store produces a
// complement: the composite GIN answers "contains this lexeme", the keyword GIN
// answers containment, the date index answers a range. A complement is
// everything the index did NOT return, which can only be computed by visiting
// every row of the account and testing each one — 120,000 rows on the bench
// corpus, 26,869 on the owner's real account, and growing with the mailbox
// rather than with the answer.
//
// That is the one thing this repertoire refuses on principle, so NOT is refused
// on principle. The NEGATIONS THAT ARE CHEAP ARE ALREADY SERVED, at the
// condition level where they carry their own predicate: notKeyword for the four
// system flags (a bitmask test), and the exclusion of whole mailboxes
// (inMailboxOtherThan, which is Gmail's `-in:spam`). A client wanting "not from
// Ana" has no cheap form and gets an honest refusal rather than a query that
// works on a test mailbox and times out on a real one.
func translateOperator(raw json.RawMessage) (searchFilter, *jmap.MethodError) {
	var op struct {
		Operator   string            `json:"operator"`
		Conditions []json.RawMessage `json:"conditions"`
	}
	if err := json.Unmarshal(raw, &op); err != nil {
		return searchFilter{}, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("the filter operator did not parse: %v", err)
	}

	switch strings.ToUpper(op.Operator) {
	case "AND":
		return translateAnd(op.Conditions)
	case "OR":
		return translateOr(op.Conditions)
	case "NOT":
		return searchFilter{}, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("the %q filter operator is not supported: its result is the complement of a match "+
				"set, which no index in this store can produce, so serving it would mean testing every message "+
				"in the account; the cheap negations ARE served as conditions — notKeyword for the IMAP system "+
				"flags, and inMailboxOtherThan to exclude whole folders", op.Operator)
	default:
		return searchFilter{}, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("the %q filter operator is not supported; this server supports AND and OR", op.Operator)
	}
}

// translateAnd merges a conjunction into one filter.
func translateAnd(conditions []json.RawMessage) (searchFilter, *jmap.MethodError) {
	// Merge the conditions. A conflict — two different mailboxes, two different
	// texts — is refused rather than silently resolved: "in mailbox A AND in
	// mailbox B" matches nothing in a store where a message has one mailbox,
	// and answering with the results of one of them would be wrong in a way
	// the user cannot see.
	var merged searchFilter
	for i, cond := range conditions {
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

// translateOr builds the disjunction, enforcing the boundedness rule stated at
// translateOperator.
func translateOr(conditions []json.RawMessage) (searchFilter, *jmap.MethodError) {
	if len(conditions) == 0 {
		// §5.5 does not define an empty OR. Logically it is FALSE (nothing
		// matches), which is a legal but useless answer; naming it beats
		// returning an empty list a client would read as "you have no mail".
		return searchFilter{}, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("an OR with no conditions matches nothing; omit the operator instead")
	}
	if len(conditions) == 1 {
		// An OR of one is that one. Unwrapping it rather than building a
		// single-branch union keeps the common client habit of wrapping
		// everything in an operator on the fast path.
		return translateNode(conditions[0])
	}
	if len(conditions) > maxOrBranches {
		return searchFilter{}, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("an OR of %d conditions exceeds this server's limit of %d: each branch is a "+
				"separate bounded search, so the branches multiply the work one request may do; issue the "+
				"extra branches as separate queries",
				len(conditions), maxOrBranches)
	}

	var f searchFilter
	for i, cond := range conditions {
		branch, merr := translateNode(cond)
		if merr != nil {
			return searchFilter{}, merr
		}
		// The nested-OR check comes FIRST, and the order is not cosmetic. A
		// nested OR carries no conditions of its own, so the answerability test
		// below would reject it for "needs an inMailbox or a text condition" —
		// a true statement about the wrong problem, which would send a client
		// looking for a missing condition instead of flattening its operator.
		// The more specific diagnosis wins.
		if len(branch.or) > 0 {
			// A nested OR would multiply branch counts past the bound the flat
			// check enforces — 4 branches each holding 4 is 16 searches. The
			// client can flatten it; §5.5's OR is associative, so nothing is
			// lost.
			return searchFilter{}, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
				WithDescription("a nested OR is not supported: flatten it into one OR of at most %d conditions",
					maxOrBranches)
		}
		// THE RULE: every branch must stand alone. A disjunction only widens, so
		// a branch that would scan the account as a standalone query scans it
		// here too — and translateFilter's own answerability check is the exact
		// test for "would this server serve it".
		// The RETURNED branch is appended, not the one that went in: a bare
		// label branch is answerable only because answerable() scopes it
		// account-wide, and dropping that scope here would push a keyword
		// filter down the folder-view branch with no mailbox to walk.
		branch, merr = answerable(branch)
		if merr != nil {
			return searchFilter{}, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
				WithDescription("branch %d of the OR is not answerable on its own, and a disjunction cannot "+
					"narrow it: %s", i, merr.Description)
		}
		f.or = append(f.or, branch)
	}
	return f, nil
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

	// The E3 conditions. Each merges the way its own conjunction works: the
	// contradictory ones refuse, the range bounds tighten, the sets union.
	if b.hasAttachment != nil {
		if out.hasAttachment != nil && *out.hasAttachment != *b.hasAttachment {
			return out, fmt.Errorf("hasAttachment required both true and false")
		}
		out.hasAttachment = b.hasAttachment
	}
	if b.cc != "" {
		if out.cc != "" && out.cc != b.cc {
			// Two substring conditions on one column is a legitimate
			// conjunction ("contains A and contains B") that the store's single
			// LIKE predicate cannot express. Refusing names it; the client can
			// send the narrower of the two.
			return out, fmt.Errorf("two different cc conditions")
		}
		out.cc = b.cc
	}
	if b.bcc != "" {
		if out.bcc != "" && out.bcc != b.bcc {
			return out, fmt.Errorf("two different bcc conditions")
		}
		out.bcc = b.bcc
	}
	if b.minSize != nil {
		// The LARGER lower bound wins: it is the stricter one, so the
		// conjunction is exact.
		if out.minSize == nil || *b.minSize > *out.minSize {
			out.minSize = b.minSize
		}
	}
	if b.maxSize != nil {
		if out.maxSize == nil || *b.maxSize < *out.maxSize {
			out.maxSize = b.maxSize
		}
	}

	// Flag bits union, and a bit required on one side while excluded on the
	// other is the unsatisfiable filter applyHasKeyword already names when both
	// arrive in ONE condition. Arriving through two conditions of an AND, it
	// means the same thing, so it gets the same answer rather than an empty
	// list the user would read as "no such mail".
	if b.flagsAll&out.flagsNone != 0 || b.flagsNone&out.flagsAll != 0 {
		return out, fmt.Errorf("a system flag is both required and excluded, which no message can satisfy")
	}
	out.flagsAll |= b.flagsAll
	out.flagsNone |= b.flagsNone

	if b.excludeMailboxIDs != nil {
		// The union: "not in A" AND "not in B" is "not in A or B". Exact, and
		// the only merge here that GROWS a condition rather than tightening it.
		if out.excludeMailboxIDs == nil {
			out.excludeMailboxIDs = []int64{}
		}
		out.excludeMailboxIDs = append(out.excludeMailboxIDs, b.excludeMailboxIDs...)
	}

	if len(b.or) > 0 {
		// An OR nested inside an AND ("in:inbox AND (from:ana OR from:juan)")
		// is a legitimate and common search, and it is REFUSED here rather than
		// half-served. Distributing the AND over the OR is what would serve it
		// — (inbox AND ana) OR (inbox AND juan) — and that is a genuine
		// implementation, not a refusal in disguise: it is left out because
		// each distributed branch must then be re-checked for answerability and
		// the branch count multiplies, and shipping it without measuring the
		// multiplied shape is exactly the kind of thing this file does not do.
		// Named so a client can flatten it, and named so the next epic knows
		// what closing it costs.
		return out, fmt.Errorf("an OR nested inside an AND is not supported; " +
			"distribute it into one OR of complete conditions")
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
			if merr := f.applyNotKeyword(kw); merr != nil {
				return f, merr
			}

		case "hasAttachment":
			// §4.4.1: "hasAttachment: Boolean — If true, filters on Emails
			// where the attachments property is not empty; if false, filters on
			// Emails where it IS empty."
			//
			// Served by messages.has_attachments, the boolean the parser sets at
			// ingest. It is a filter on the walk each shape already performs
			// (store.Narrowing: 2.0 ms on the folder view, 0.6 ms on the text
			// path) rather than an index of its own.
			var b bool
			if err := json.Unmarshal(raw, &b); err != nil {
				return f, unsupportedNode(name, "not a boolean")
			}
			if f.hasAttachment != nil && *f.hasAttachment != b {
				return f, unsupportedNode(name, "a second, contradictory hasAttachment condition")
			}
			f.hasAttachment = &b

		case "cc", "bcc":
			// §4.4.1: "cc: String — Looks for the text in the Cc header field of
			// the message"; bcc likewise for Bcc.
			//
			// # These two are EXACT, unlike from/to/subject above
			//
			// The text conditions three cases up are answered with a
			// whole-message tsvector match — documented over-matching, because
			// the store has one tsvector and no per-field index. These do NOT
			// inherit that: migration 0008 gave each its own trigram index, so
			// `cc:ana@x.test` matches the Cc header and only the Cc header.
			//
			// The inconsistency is deliberate and runs in the SAFE direction —
			// stricter than the RFC requires, never looser — and it exists
			// because the alternative for each was worse:
			//
			//   * answering `cc` from the tsvector would also return every
			//     message that merely MENTIONS the address in its body, and
			//     someone filtering by cc is looking for the mail where a
			//     specific person was copied;
			//   * `bcc` had no over-match option at all. Bcc is not in the
			//     tsvector (migration 0002 puts only from/to/cc in weight band
			//     B), so it was an index or a refusal.
			//
			// The match is an unanchored, case-insensitive substring, which is
			// what makes a partial address ("ana", "@example.test") useful in a
			// search box. store.Narrowing escapes the LIKE metacharacters so a
			// term containing % or _ stays literal.
			text, merr := stringNode(name, raw)
			if merr != nil {
				return f, merr
			}
			target := &f.cc
			if name == "bcc" {
				target = &f.bcc
			}
			if *target != "" && *target != text {
				return f, unsupportedNode(name, "a second, different address condition")
			}
			*target = text

		case "minSize", "maxSize":
			// §4.4.1: "minSize: UnsignedInt — The size of the Email in octets is
			// greater than or equal to this number"; "maxSize: UnsignedInt — ...
			// is less than this number". Inclusive lower, exclusive upper.
			//
			// The store filters on messages.raw_size, which IS the JMAP
			// Email.size property (adapter.go maps one to the other) — so this
			// filters on the number the client sees rather than on a proxy for
			// it, and E3 needed no size column because the store already had the
			// right one.
			var n uint64
			if err := json.Unmarshal(raw, &n); err != nil {
				// §4.4.1 types these UnsignedInt, so a negative or fractional
				// value is not a size this server can act on. Naming the node
				// beats coercing it.
				return f, unsupportedNode(name, "not an unsigned integer")
			}
			if n > 1<<62 {
				// Beyond any message a mail system will hold, and past the point
				// where the int64 the store column uses stays exact.
				return f, unsupportedNode(name, "implausibly large")
			}
			v := int64(n) //nolint:gosec // bounded on the line above
			if name == "minSize" {
				f.minSize = &v
			} else {
				f.maxSize = &v
			}

		case "inMailboxOtherThan":
			// §4.4.1: "inMailboxOtherThan: Id[] — A list of Mailbox ids. The
			// Email must be in at least one Mailbox not in this list."
			//
			// In Moov a message is in EXACTLY ONE mailbox (session.go advertises
			// maxMailboxesPerEmail:1, and W1 enforces it), so "in at least one
			// mailbox not in this list" reduces to "its mailbox is not in this
			// list" — an exclusion, which is the predicate the store implements.
			// The reduction is exact rather than approximate BECAUSE of that
			// advertised limit; on a server with multi-mailbox messages it would
			// not be, and this comment is here so a future multi-mailbox model
			// revisits it rather than inheriting it.
			//
			// It is also the mechanism behind this server's Gmail-default
			// exclusion of Spam and Trash; applyDefaultExclusion owns that
			// policy and explains it against the canon.
			var wire []string
			if err := json.Unmarshal(raw, &wire); err != nil {
				return f, unsupportedNode(name, "not an array of mailbox ids")
			}
			// A non-nil empty slice, so that "the client sent []" is
			// distinguishable from "the client sent nothing" — the difference
			// between Gmail's `in:anywhere` and an ordinary search, which
			// applyDefaultExclusion decides on exactly this test. json
			// unmarshals `[]` to an empty non-nil slice and `null` to nil, but
			// only when the destination starts nil, so it is made explicit here
			// rather than relied upon.
			if f.excludeMailboxIDs == nil {
				f.excludeMailboxIDs = []int64{}
			}
			for _, w := range wire {
				id, err := DecodeMailboxID(w)
				if err != nil {
					// Unlike inMailbox, an unknown id here is HARMLESS: it names
					// a mailbox to exclude, and a mailbox that does not exist
					// holds nothing, so excluding it excludes nothing. Refusing
					// would reject a filter whose meaning is perfectly clear.
					// The id is dropped and the rest of the list still applies.
					continue
				}
				f.excludeMailboxIDs = append(f.excludeMailboxIDs, id)
			}
			// An explicit inMailboxOtherThan is the CLIENT's exclusion, so it
			// suppresses the server's default one — including when the client
			// sends an empty list, which is exactly how a client asks for
			// Gmail's `in:anywhere`.
			f.defaultExclusion = false

		default:
			// Everything else in §4.4.1 — the three inThread keyword conditions,
			// body, header, attachments — has no shape in the repertoire. §5.5's
			// unsupportedFilter is precisely "The filter is syntactically valid,
			// but the server cannot process it."
			//
			// `body` and `header` are the ones a future epic would close: both
			// need a per-field index the single generated tsvector cannot
			// provide, which is the same gap that makes from/to/subject
			// over-match. The J3 report names what each would cost.
			return f, unsupportedNode(name, "not supported by this server")
		}
	}

	return f, nil
}

// applyHasKeyword maps a §4.4.1 hasKeyword onto the repertoire.
//
// # The two places a keyword can live, and why that decides the predicate
//
// Arbitration A6 puts user labels in message_state.keywords, a text[] with its
// own GIN index. But the four IMAP SYSTEM flags — \Seen, \Answered, \Flagged,
// \Draft — are bits in message_state.flags, never strings in that array,
// because they are a fixed closed set and a bitmask filter costs nothing
// (migration 0002 says so where the column is declared).
//
// So `hasKeyword:"$flagged"` and `hasKeyword:"$MoovL7"` are the same JMAP
// condition over two different physical representations, and translating both
// to the array predicate would answer "no" for every system flag — silently,
// with a list that quietly ignores the condition the user typed.
//
// Before L3 epic E3 that was handled by REFUSING the system flags: "an IMAP
// system flag stored as a bitmask; the repertoire has no predicate for it".
// True when written. E3 added the predicate (store.Narrowing FlagsAll /
// FlagsNone), which is what makes `is:starred` — a CORE Gmail search operator
// (canon §2.5) — answerable at all.
func (f *searchFilter) applyHasKeyword(kw string) *jmap.MethodError {
	// A system flag becomes a bit in the mask rather than an entry in the
	// keyword field. systemFlagForKeywordBit is the same table jmapKeywords
	// renders WITH, so the two directions cannot disagree.
	if bit, ok := systemFlagBit(kw); ok {
		if f.flagsNone&bit != 0 {
			// "has $flagged AND not $flagged" matches nothing. §4.4.1 makes a
			// multi-property condition a conjunction, so this is a filter the
			// client can express and no message can satisfy — and answering it
			// with an empty list would be CORRECT but indistinguishable from
			// "you have no flagged mail". Naming it is more useful.
			return unsupportedNode("hasKeyword",
				fmt.Sprintf("%q is required and excluded by the same filter, which no message can satisfy", kw))
		}
		f.flagsAll |= bit
		return nil
	}

	// Everything else goes to the keywords array, which is where the store
	// keeps user keywords AND where arbitration A6 puts labels — so a label
	// filter is a keyword filter, by design.
	if f.keyword != "" && !strings.EqualFold(f.keyword, kw) {
		// Still refused: the array predicate is `keywords @> ARRAY[$n]`, a
		// single-element containment, and two of them would need either a
		// two-element array (which is a different condition — containment of
		// BOTH, expressible, but not what one @> with one parameter does) or a
		// second predicate the shapes were not measured with. One label at a
		// time, named rather than dropped.
		return unsupportedNode("hasKeyword", "a second, different keyword condition")
	}
	f.keyword = kw
	return nil
}

// applyNotKeyword maps a §4.4.1 notKeyword onto the repertoire.
//
// §4.4.1: "notKeyword: String — A keyword that must not be in the Email's
// keywords property."
//
// Before E3 exactly one was served — notKeyword:$seen, which is the unread
// filter and which the store expresses with the literal `(flags & 1) = 0` that
// matches the message_state_unread partial index character for character. E3
// extends it to the other three SYSTEM flags through the same bitmask
// predicate that hasKeyword now uses.
//
// It stops there. A negated USER keyword (`notKeyword:"$MoovL7"`) stays refused,
// and the reason is the boundedness rule rather than effort: the keywords array
// has a GIN index that answers containment, and NOT containment is its
// complement — a set the index cannot produce, so the predicate degrades to a
// filter over every message in the account. That is the unbounded scan the
// repertoire exists to make unrepresentable, and it is exactly the shape S3
// measured an instance collapsing under.
func (f *searchFilter) applyNotKeyword(kw string) *jmap.MethodError {
	if strings.EqualFold(kw, KeywordSeen) {
		// The unread filter keeps its own field rather than becoming a
		// FlagsNone bit: it is spelled as the literal the partial index is
		// built on, and routing it through the generic mask would lose that
		// index for the single most common filter in the product.
		f.unreadOnly = true
		return nil
	}
	if bit, ok := systemFlagBit(kw); ok {
		if f.flagsAll&bit != 0 {
			return unsupportedNode("notKeyword",
				fmt.Sprintf("%q is required and excluded by the same filter, which no message can satisfy", kw))
		}
		f.flagsNone |= bit
		return nil
	}
	return unsupportedNode("notKeyword",
		fmt.Sprintf("only the IMAP system flags are negatable (%q, %q, %q, %q); %q lives in the keyword array, "+
			"whose index answers containment but not its complement — negating it would scan the whole account",
			KeywordSeen, KeywordFlagged, KeywordAnswered, KeywordDraft, kw))
}

// systemFlagBit returns the flag BIT a JMAP keyword names, if it names one.
//
// It is systemFlagForKeyword's untyped twin: that one returns a store.Flags for
// callers that already hold store rows, this one returns the raw bit for the
// translation layer, which must not depend on the store (search.go's rule).
// Both read the same systemFlagKeywords table, so they cannot disagree.
func systemFlagBit(keyword string) (uint64, bool) {
	for _, f := range systemFlagKeywords {
		if strings.EqualFold(f.keyword, keyword) {
			return f.bit, true
		}
	}
	return 0, false
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
