package mail

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Mailbox/query — RFC 8620 §5.5 as extended by RFC 8621 §2.3.
//
// # Why this method is cheap here and expensive for Email
//
// Email/query is a whole file of refusals because a message list is unbounded:
// an account holds 26,869 of them on the pilot alone, so every filter has to be
// answerable by an index or declined. A FOLDER list has none of that. RFC 8621
// §2 gives Mailbox eleven properties, an account has tens of mailboxes, and
// Mailbox/get already returns every one of them in a single query
// (MailboxReader.Mailboxes). So this method's whole job is to take that list,
// apply a §2.3 FilterCondition to it in memory, and return the ids.
//
// That is not a shortcut around the store's repertoire rule (L2 §4.3) — it is
// what the rule is FOR. The rule bounds work; a set that is already bounded by
// the shape of the data, and already read in one round trip for every
// Mailbox/get a session issues, needs no index to stay bounded.
//
// # Why it is registered at all
//
// §2.3 is a standard method a conforming client may call, and until now this
// server answered it with `unknownMethod` — which a client reads as "this server
// is partial" rather than "this server does not do that". J4's own risk map
// recorded that as the class of gap real clients trip over, and it was right
// twice already: the `filter:null` enumeration and the [hasKeyword, receivedAt]
// sort were both found by real traffic, not by reading the RFC.
//
// # What §2.3 asks for, and what is honored
//
//	filter: FilterCondition — parentId, name, role, hasAnyRole, isSubscribed
//	sort:   the §5.5 comparators over sortOrder and name
//
// parentId, role, hasAnyRole and isSubscribed are served: each is a property of
// the row already in hand, and each has an exact, obvious meaning. `name` is
// REFUSED, and that refusal is the only interesting decision in this file — see
// translateMailboxFilter.
//
// The sort is the one Mailbox/get already uses (sortOrder, then name), and a
// client asking for a different one gets unsupportedSort rather than a list in
// an order it did not request.

// mailboxQueryRequest is the §5.5 /query arguments object for Mailbox.
//
// It reuses queryRequest's Position/Limit pointer discipline for the same
// reason: §5.5 gives absent and zero different meanings, and a plain value
// cannot tell them apart.
type mailboxQueryRequest struct {
	AccountID      string          `json:"accountId"`
	Filter         json.RawMessage `json:"filter"`
	Sort           []comparator    `json:"sort"`
	Position       *int64          `json:"position"`
	Anchor         *string         `json:"anchor"`
	AnchorOffset   int64           `json:"anchorOffset"`
	Limit          *uint64         `json:"limit"`
	CalculateTotal bool            `json:"calculateTotal"`

	// SortAsTree and FilterAsTree are RFC 8621 §2.3's two extra arguments.
	// Parsed so they can be refused by name rather than ignored — see
	// handleMailboxQuery.
	SortAsTree   bool `json:"sortAsTree"`
	FilterAsTree bool `json:"filterAsTree"`
}

// The Mailbox sort properties this server implements. §2.3: "The Mailboxes may
// be sorted by ... 'name', 'sortOrder', 'parentId'".
const (
	// SortMailboxSortOrder is the §2 sortOrder property — "Defines the sort
	// order of Mailboxes when presented in the client's UI" — and it is this
	// server's default, because it is the order Mailbox/get already returns.
	SortMailboxSortOrder = "sortOrder"

	// SortMailboxName is the §2 name property.
	SortMailboxName = "name"
)

// handleMailboxQuery implements Mailbox/query.
func (d *Deps) handleMailboxQuery(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	caller, ok := jmap.CallerFromContext(ctx)
	if !ok {
		return nil, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}

	var req mailboxQueryRequest
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

	// §2.3's tree arguments, refused explicitly.
	//
	// sortAsTree: "If true, when sorting the query results and comparing
	// Mailboxes A and B: If A is an ancestor of B, it always comes first
	// regardless of the Comparator objects." filterAsTree: "If true, a Mailbox
	// is only included if all its ancestors are included."
	//
	// Both are real, implementable, and NOT implemented — so they are declined
	// rather than accepted-and-ignored. Accepting them would return a flat list
	// to a client that asked for a tree-consistent one and would render the
	// hierarchy wrong, which is worse than an error the client can handle.
	if req.SortAsTree || req.FilterAsTree {
		which := "sortAsTree"
		if req.FilterAsTree {
			which = "filterAsTree"
		}
		return nil, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("the %q argument (RFC 8621 §2.3) is not supported; this server returns a flat "+
				"list and the client composes the tree from each Mailbox's parentId", which)
	}

	filter, merr := translateMailboxFilter(req.Filter)
	if merr != nil {
		return nil, merr
	}
	order, merr := translateMailboxSort(req.Sort)
	if merr != nil {
		return nil, merr
	}

	state, err := d.State.MailboxState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading mailbox state", err)
	}
	rows, err := d.Mailboxes.Mailboxes(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("listing mailboxes", err)
	}

	matched := make([]MailboxRow, 0, len(rows))
	for _, row := range rows {
		if filter.matches(row) {
			matched = append(matched, row)
		}
	}
	sortMailboxRows(matched, order)

	resp := &queryResponse{
		AccountID: req.AccountID,
		// The same state string Mailbox/get hands out, prefixed exactly as
		// Email/query prefixes its own: a queryState and a /get state are
		// different cursors in different namespaces (§5.5), and one must never be
		// mistakable for the other.
		QueryState: queryStateFor(state),
		// §5.5: "true if the server supports calling Mailbox/queryChanges with
		// these filter/sort parameters". It does not — changes.go declines every
		// /queryChanges by design — so the truthful answer is false. Saying true
		// would send a conforming client to a method that refuses it.
		CanCalculateChanges: false,
		IDs:                 []string{},
	}

	ids := make([]int64, 0, len(matched))
	for _, row := range matched {
		ids = append(ids, row.ID)
	}

	// Paging over the complete, in-memory result set. Unlike Email/query there
	// is no window here and no honest-boundedness caveat: `matched` IS every
	// mailbox that matches, so anchor resolution and a negative position are
	// exact rather than exact-within-a-window.
	start, merr := resolveMailboxStart(&req, ids)
	if merr != nil {
		return nil, merr
	}
	limit, serverLimited := effectiveQueryLimit(req.Limit)

	end := start + limit
	if start > uint64(len(ids)) {
		start = uint64(len(ids))
	}
	if end > uint64(len(ids)) {
		end = uint64(len(ids))
	}
	for _, id := range ids[start:end] {
		resp.IDs = append(resp.IDs, EncodeMailboxID(id))
	}
	resp.Position = start
	if serverLimited {
		l := limit
		resp.Limit = &l
	}

	if req.CalculateTotal {
		// The one place this server can answer §5.5's `total` honestly and
		// always: the folder list is complete, so its length IS "the total
		// number of Mailboxes in the results". Email/query has to omit the
		// property because its result set is windowed (queryTotal); here there is
		// no window to be wrong about.
		total := uint64(len(ids))
		resp.Total = &total
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// filter translation (RFC 8621 §2.3)
// ---------------------------------------------------------------------------

// mailboxFilter is the §2.3 FilterCondition, translated.
//
// Every field is a pointer or a sentinel so "not specified" is distinguishable
// from "specified as the zero value" — `parentId: null` is a REAL condition
// ("mailboxes at the top level"), not the absence of one, and conflating the two
// would silently widen a filter the client meant to narrow.
type mailboxFilter struct {
	// parentID is set when a parentId condition was given. parentIDNull records
	// that the given value was JSON null.
	parentID     *int64
	parentIDSet  bool
	parentIDNull bool

	// role is the §2.3 role condition; roleNull records `role: null`
	// ("mailboxes with no role").
	role     string
	roleSet  bool
	roleNull bool

	// hasAnyRole is §2.3's "Mailboxes with a non-null role".
	hasAnyRole    *bool
	isSubscribed  *bool
	filterMatched bool
}

// matches applies the filter to one row.
func (f mailboxFilter) matches(row MailboxRow) bool {
	if f.parentIDSet {
		switch {
		case f.parentIDNull:
			// §2: parentId is "null if this Mailbox is at the top level", which
			// the store spells as 0.
			if row.ParentID != 0 {
				return false
			}
		case f.parentID == nil || row.ParentID != *f.parentID:
			return false
		}
	}
	if f.roleSet {
		jmapName, hasRole := jmapRole(row.Role)
		switch {
		case f.roleNull:
			if hasRole {
				return false
			}
		case !hasRole || !strings.EqualFold(jmapName, f.role):
			return false
		}
	}
	if f.hasAnyRole != nil {
		_, hasRole := jmapRole(row.Role)
		if hasRole != *f.hasAnyRole {
			return false
		}
	}
	if f.isSubscribed != nil && row.IsSubscribed != *f.isSubscribed {
		return false
	}
	return true
}

// translateMailboxFilter maps a §2.3 filter onto mailboxFilter, or refuses.
//
// The operator rules are Email/query's, for the same reasons: AND is the shape
// the conjunction below already is, and OR/NOT would need a set algebra this
// method does not have. They are refused by name so a client can, per §5.5,
// "suggest that the user simplify their search".
func translateMailboxFilter(raw json.RawMessage) (mailboxFilter, *jmap.MethodError) {
	var f mailboxFilter
	if len(raw) == 0 || string(raw) == "null" {
		// §5.5: "If null, all objects in the account of this type are included in
		// the results." For mailboxes that is simply the whole folder list, which
		// costs the one query Mailbox/get already runs — so unlike Email/query
		// (where the same argument needed a store method of its own) there is
		// nothing to arrange.
		return f, nil
	}

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return f, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("the filter is not an object")
	}
	if _, isOperator := probe["operator"]; isOperator {
		var op struct {
			Operator   string            `json:"operator"`
			Conditions []json.RawMessage `json:"conditions"`
		}
		if err := json.Unmarshal(raw, &op); err != nil {
			return f, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
				WithDescription("the filter operator did not parse: %v", err)
		}
		if strings.ToUpper(op.Operator) != "AND" {
			return f, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
				WithDescription("the %q filter operator is not supported; this server supports AND of "+
					"simple Mailbox conditions", op.Operator)
		}
		for _, cond := range op.Conditions {
			sub, merr := translateMailboxFilter(cond)
			if merr != nil {
				return f, merr
			}
			var err error
			f, err = mergeMailboxFilters(f, sub)
			if err != nil {
				return f, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
					WithDescription("the filter conditions cannot be combined: %v", err)
			}
		}
		return f, nil
	}
	return translateMailboxCondition(probe)
}

// translateMailboxCondition maps one §2.3 FilterCondition.
func translateMailboxCondition(props map[string]json.RawMessage) (mailboxFilter, *jmap.MethodError) {
	var f mailboxFilter
	if len(props) == 0 {
		// An empty condition "MUST always evaluate to true" (§4.4.1's rule,
		// which §2.3 inherits from §5.5). For mailboxes that is the whole list,
		// and the whole list is what this server can serve — so unlike
		// Email/query, this one is accepted rather than refused.
		return f, nil
	}

	for name, raw := range props {
		f.filterMatched = true
		switch name {
		case "parentId":
			f.parentIDSet = true
			if string(raw) == "null" {
				f.parentIDNull = true
				continue
			}
			var wire string
			if err := json.Unmarshal(raw, &wire); err != nil {
				return f, unsupportedNode(name, "not a Mailbox id or null")
			}
			id, err := DecodeMailboxID(wire)
			if err != nil {
				// An id this server never issued names no mailbox, so nothing
				// can be its child. Refusing names the node, which tells a
				// client more than an empty list would.
				return f, unsupportedNode(name, "not a mailbox id issued by this server")
			}
			f.parentID = &id

		case "role":
			f.roleSet = true
			if string(raw) == "null" {
				f.roleNull = true
				continue
			}
			role, merr := stringNode(name, raw)
			if merr != nil {
				return f, merr
			}
			f.role = role

		case "hasAnyRole":
			var v bool
			if err := json.Unmarshal(raw, &v); err != nil {
				return f, unsupportedNode(name, "not a boolean")
			}
			f.hasAnyRole = &v

		case "isSubscribed":
			var v bool
			if err := json.Unmarshal(raw, &v); err != nil {
				return f, unsupportedNode(name, "not a boolean")
			}
			f.isSubscribed = &v

		case "name":
			// §2.3: "name: String — The Mailbox name must contain this string
			// (case-insensitive)."
			//
			// REFUSED, and this is the one judgement call in the file. The
			// condition is trivially implementable over the rows already in hand,
			// and implementing it would still be WRONG, because "contain" is a
			// substring test over a name whose JMAP form is the LEAF only
			// (adapter.go splits the IMAP path on the delimiter to produce
			// name + parentId). A client searching for "Work" means the folder
			// called Work; a naive substring test over the leaf misses
			// "INBOX/Work" only if the leaf is not "Work", and matches
			// "Workshop", "Homework" and "Networking" besides — and the client
			// cannot see which reading it got.
			//
			// The honest options were "refuse" and "implement a substring test
			// over the leaf name, documented" — and the second is what §2.3
			// literally specifies. It is not served here because the delimiter
			// question deserves a decision made deliberately with a client that
			// wants it, rather than one made in passing by whoever registered
			// this method. Named in the E1 report as the cheapest thing to add.
			return f, unsupportedNode(name, "the substring semantics of RFC 8621 §2.3 over this "+
				"server's leaf-only Mailbox names are not settled; filter by parentId, role, "+
				"hasAnyRole or isSubscribed, or read the whole list with Mailbox/get")

		default:
			return f, unsupportedNode(name, "not a Mailbox filter condition this server supports")
		}
	}
	return f, nil
}

// mergeMailboxFilters ANDs two translated filters, refusing contradictions.
func mergeMailboxFilters(a, b mailboxFilter) (mailboxFilter, error) {
	out := a
	if b.parentIDSet {
		if out.parentIDSet && !sameParent(out, b) {
			// "child of A AND child of B" matches nothing in a tree where a
			// mailbox has one parent. Answering with one of them would be wrong
			// in a way the user cannot see.
			return out, errTwoConditions("parentId")
		}
		out.parentIDSet, out.parentID, out.parentIDNull = true, b.parentID, b.parentIDNull
	}
	if b.roleSet {
		if out.roleSet && (out.roleNull != b.roleNull || !strings.EqualFold(out.role, b.role)) {
			return out, errTwoConditions("role")
		}
		out.roleSet, out.role, out.roleNull = true, b.role, b.roleNull
	}
	if b.hasAnyRole != nil {
		if out.hasAnyRole != nil && *out.hasAnyRole != *b.hasAnyRole {
			return out, errTwoConditions("hasAnyRole")
		}
		out.hasAnyRole = b.hasAnyRole
	}
	if b.isSubscribed != nil {
		if out.isSubscribed != nil && *out.isSubscribed != *b.isSubscribed {
			return out, errTwoConditions("isSubscribed")
		}
		out.isSubscribed = b.isSubscribed
	}
	out.filterMatched = out.filterMatched || b.filterMatched
	return out, nil
}

func sameParent(a, b mailboxFilter) bool {
	if a.parentIDNull != b.parentIDNull {
		return false
	}
	if a.parentIDNull {
		return true
	}
	return a.parentID != nil && b.parentID != nil && *a.parentID == *b.parentID
}

// errTwoConditions is the contradiction message shared by every merge branch.
func errTwoConditions(node string) error {
	return &conditionConflict{node: node}
}

type conditionConflict struct{ node string }

func (e *conditionConflict) Error() string {
	return "two different " + e.node + " conditions in one filter"
}

// ---------------------------------------------------------------------------
// sort translation (RFC 8621 §2.3)
// ---------------------------------------------------------------------------

// mailboxSortSpec is the translated Mailbox sort.
type mailboxSortSpec struct {
	// byName sorts on the §2 name property; otherwise the sort is by sortOrder
	// with name breaking ties, which is Mailbox/get's own order.
	byName    bool
	ascending bool
}

// translateMailboxSort maps the §5.5 sort array onto the one order this server
// produces, or refuses.
func translateMailboxSort(sorts []comparator) (mailboxSortSpec, *jmap.MethodError) {
	// §5.5: "If all comparators are the same (this includes the case where an
	// empty array or null is given as the 'sort' argument), the sort order is
	// server dependent, but it MUST be stable between calls."
	//
	// The server-dependent choice is Mailbox/get's own order — sortOrder, then
	// name — so a client that calls both sees one folder list rather than two
	// orderings of it.
	if len(sorts) == 0 {
		return mailboxSortSpec{ascending: true}, nil
	}
	if len(sorts) > 1 {
		return mailboxSortSpec{}, jmap.NewMethodError(jmap.CodeUnsupportedSort).
			WithDescription("this server serves a single Mailbox comparator (%q or %q); %d were given",
				SortMailboxSortOrder, SortMailboxName, len(sorts))
	}

	c := sorts[0]
	if c.Collation != nil && *c.Collation != "" {
		// The name sort compares strings, so a collation is meaningful here in a
		// way it is not for Email/query's date and rank sorts — which makes
		// refusing it MORE important, not less: this server sorts names by Go's
		// byte-wise comparison after a case fold, which is not any RFC 4790
		// algorithm, and session.go's collationAlgorithms is empty. Accepting a
		// named collation would be a promise of ordering it does not keep.
		return mailboxSortSpec{}, jmap.NewMethodError(jmap.CodeUnsupportedSort).
			WithDescription("collation %q is not supported; this server advertises no collation algorithms",
				*c.Collation)
	}

	switch c.Property {
	case SortMailboxSortOrder:
		return mailboxSortSpec{ascending: c.ascending()}, nil
	case SortMailboxName:
		return mailboxSortSpec{byName: true, ascending: c.ascending()}, nil
	default:
		// §2.3 also lists parentId as sortable. It is not served: sorting a flat
		// list by an opaque id is an order no user can perceive, and the tree
		// ordering a client actually wants from it is sortAsTree — which is
		// refused above, by name, for being unimplemented rather than
		// meaningless.
		return mailboxSortSpec{}, jmap.NewMethodError(jmap.CodeUnsupportedSort).
			WithDescription("sorting Mailboxes on %q is not supported; this server sorts on %q or %q",
				c.Property, SortMailboxSortOrder, SortMailboxName)
	}
}

// sortMailboxRows orders the matched rows.
//
// The tiebreak chain always ends at the id, so the order is TOTAL — §5.5
// requires a query order be "stable between calls", and two folders sharing a
// sortOrder and a name would otherwise be free to swap places between requests
// and corrupt a client's paging.
func sortMailboxRows(rows []MailboxRow, order mailboxSortSpec) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if order.byName {
			if !strings.EqualFold(a.Name, b.Name) {
				return lessString(strings.ToLower(a.Name), strings.ToLower(b.Name), order.ascending)
			}
			return lessInt64(a.ID, b.ID, order.ascending)
		}
		if a.SortOrder != b.SortOrder {
			return lessUint64(a.SortOrder, b.SortOrder, order.ascending)
		}
		if !strings.EqualFold(a.Name, b.Name) {
			return lessString(strings.ToLower(a.Name), strings.ToLower(b.Name), order.ascending)
		}
		return lessInt64(a.ID, b.ID, order.ascending)
	})
}

func lessString(a, b string, ascending bool) bool {
	if ascending {
		return a < b
	}
	return a > b
}

func lessUint64(a, b uint64, ascending bool) bool {
	if ascending {
		return a < b
	}
	return a > b
}

func lessInt64(a, b int64, ascending bool) bool {
	if ascending {
		return a < b
	}
	return a > b
}

// resolveMailboxStart applies §5.5's anchor and position rules to the complete
// result set.
//
// It is Email/query's resolveStart minus every boundedness caveat: `ids` is the
// whole match set, not a window of it, so an anchor that exists is always found
// and a negative position always counts back from the true end.
func resolveMailboxStart(req *mailboxQueryRequest, ids []int64) (uint64, *jmap.MethodError) {
	n := int64(len(ids))

	if req.Anchor != nil {
		anchorID, err := DecodeMailboxID(*req.Anchor)
		if err != nil {
			return 0, jmap.NewMethodError(jmap.CodeAnchorNotFound).
				WithDescription("the anchor %q is not a valid Mailbox id", *req.Anchor)
		}
		idx := int64(-1)
		for i, id := range ids {
			if id == anchorID {
				idx = int64(i)
				break
			}
		}
		if idx < 0 {
			// §5.5: "If the anchor is not found, the call is rejected with an
			// 'anchorNotFound' error." Unlike Email/query's, this one means the
			// mailbox genuinely is not in the results — there is no window it
			// could be hiding beyond.
			return 0, jmap.NewMethodError(jmap.CodeAnchorNotFound).
				WithDescription("the anchor is not among the Mailboxes this filter matched")
		}
		start := idx + req.AnchorOffset
		if start < 0 {
			start = 0
		}
		return uint64(start), nil //nolint:gosec // clamped to >= 0 on the line above
	}

	var pos int64
	if req.Position != nil {
		pos = *req.Position
	}
	if pos < 0 {
		// §5.5: "the negative value MUST be added to the total number of results
		// given the filter, and if still negative, it's clamped to '0'."
		pos += n
		if pos < 0 {
			pos = 0
		}
	}
	return uint64(pos), nil //nolint:gosec // clamped to >= 0 on the lines above
}
