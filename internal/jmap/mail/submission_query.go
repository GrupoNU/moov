package mail

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// EmailSubmission/query — RFC 8621 §7.3, the SUBSET this server can answer
// exactly (L3 epic E4).
//
// ===========================================================================
// WHY THIS METHOD EXISTS AT ALL
// ===========================================================================
//
// Schedule send needs a "Scheduled" view: the messages waiting to go out
// (canon §2.3 — Gmail's own left-nav Scheduled). Three ways to serve it were
// available and two are wrong:
//
//   - a `Scheduled` FOLDER, mirroring what snooze does with Snoozed. Wrong,
//     and instructively so: a scheduled message must stay a DRAFT (canon:
//     "cancel reverts to draft"), and a draft lives in Drafts. Moving it to a
//     second folder would make every other IMAP client show it outside Drafts,
//     where their own compose flows cannot reach it — the mirror image of why
//     snooze DOES move, where leaving the mail in the inbox was the lie.
//   - a vendor method (`Scheduled/get`). It would answer one question with a
//     shape no client library knows, when RFC 8621 already defines the exact
//     query this needs.
//   - EmailSubmission/query filtered on undoStatus. §7.3 defines it, its
//     FilterCondition includes undoStatus verbatim, and the answer is one
//     indexed read of rows this server already serves. That is what is
//     implemented.
//
// ===========================================================================
// THE SUBSET, AND WHY THE REST IS REFUSED RATHER THAN APPROXIMATED
// ===========================================================================
//
// §7.3's FilterCondition names: identityIds, emailIds, threadIds, undoStatus,
// before, after. This server answers undoStatus, before and after — the three
// that are properties of the submission row itself — and refuses the three id
// filters with unsupportedFilter.
//
// The refusal is not laziness about a join. identityIds and emailIds live
// inside the intent's opaque JSON payload and on a column respectively, so a
// filter over them would be implementable; the reason they are out is the S3
// discipline this repository applies to every new query shape: a filter that
// has not been measured does not ship. The submission set is small (an account
// holds tens of rows), so the honest MVP is the filter set the Scheduled view
// needs, with the rest refused in the RFC's own vocabulary rather than served
// by an unmeasured scan.
//
// §7.3 sort: "The Comparators are the same as for Email/query" is NOT what the
// RFC says — it names emailId, threadId and sentAt. This server sorts by
// sentAt only, descending by default, and refuses the other two for the same
// measured-or-refused reason.

// submissionSortProperty is the one comparator this server sorts by.
const submissionSortProperty = "sentAt"

// submissionFilter is the §7.3 FilterCondition subset.
type submissionFilter struct {
	// UndoStatus, when set, keeps only submissions in that state.
	UndoStatus string
	// Before and After bound sendAt. Zero means unbounded.
	Before time.Time
	After  time.Time
}

// matches applies the filter to one row.
func (f submissionFilter) matches(r SubmissionRow) bool {
	if r.Destroyed {
		// A tombstoned record is not in the data set: §5.5's query is over
		// "the set of records", and /get already treats a destroyed record as
		// notFound. Returning it here would let a client page to an id it
		// cannot then fetch.
		return false
	}
	if f.UndoStatus != "" && r.UndoStatus != f.UndoStatus {
		return false
	}
	// §7.3: "before: The sendAt property must be before this date-time";
	// "after: The sendAt property must be the same as or after this
	// date-time". Note the asymmetry — before is strict, after is inclusive —
	// which is the RFC's wording and is reproduced rather than smoothed.
	if !f.Before.IsZero() && !r.SendAt.Before(f.Before) {
		return false
	}
	if !f.After.IsZero() && r.SendAt.Before(f.After) {
		return false
	}
	return true
}

// submissionQueryRequest is the §5.5 arguments object, narrowed to what this
// method accepts.
type submissionQueryRequest struct {
	AccountID  string          `json:"accountId"`
	Filter     json.RawMessage `json:"filter"`
	Sort       json.RawMessage `json:"sort"`
	Position   int             `json:"position"`
	Limit      *uint64         `json:"limit"`
	CalculateTotal bool        `json:"calculateTotal"`
}

// handleSubmissionQuery implements EmailSubmission/query.
func (d *Deps) handleSubmissionQuery(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	caller, ok := jmap.CallerFromContext(ctx)
	if !ok {
		return nil, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}
	var req submissionQueryRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("arguments did not parse: %v", err)
	}
	if req.AccountID == "" {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("the accountId argument is required")
	}
	// The account check runs before anything else, so a request naming
	// somebody else's account never learns whether it exists.
	if req.AccountID != caller.JMAPAccountID() {
		return nil, jmap.NewMethodError(jmap.CodeAccountNotFound)
	}

	filter, merr := parseSubmissionFilter(req.Filter)
	if merr != nil {
		return nil, merr
	}
	descending, merr := parseSubmissionSort(req.Sort)
	if merr != nil {
		return nil, merr
	}

	state, err := d.Submissions.SubmissionState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading submission state", err)
	}
	rows, err := d.Submissions.ListSubmissions(ctx, caller.AccountID, d.Limits.MaxObjectsInGet)
	if err != nil {
		return nil, serverFail("listing submissions", err)
	}

	matched := make([]SubmissionRow, 0, len(rows))
	for _, r := range rows {
		if filter.matches(r) {
			matched = append(matched, r)
		}
	}
	// The sort is total: sendAt with the id as tiebreak, so two submissions
	// released in the same second do not swap places between two requests —
	// the property §5.5 needs for `position` to mean anything.
	sort.Slice(matched, func(i, j int) bool {
		a, b := matched[i], matched[j]
		if !a.SendAt.Equal(b.SendAt) {
			if descending {
				return a.SendAt.After(b.SendAt)
			}
			return a.SendAt.Before(b.SendAt)
		}
		if descending {
			return a.ID > b.ID
		}
		return a.ID < b.ID
	})

	total := len(matched)
	position := req.Position
	if position < 0 {
		// §5.5 allows a negative position ("counted from the end"), and this
		// server does not implement it. Clamping to zero would silently answer
		// a different question than the one asked.
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("a negative position is not supported by EmailSubmission/query")
	}
	if position > total {
		position = total
	}
	window := matched[position:]
	if req.Limit != nil {
		limit := int(min(*req.Limit, uint64(d.Limits.MaxObjectsInGet))) //nolint:gosec // capped
		if limit < len(window) {
			window = window[:limit]
		}
	}

	ids := make([]string, 0, len(window))
	for _, r := range window {
		ids = append(ids, EncodeSubmissionID(r.ID))
	}

	out := map[string]any{
		"accountId":     req.AccountID,
		"queryState":    state,
		// §5.5: "canCalculateChanges: This is true if the server supports
		// calling EmailSubmission/queryChanges". It does not — the method is
		// not registered — so the truthful value is false, which is what stops
		// a client from calling it and meeting an unknownMethod.
		"canCalculateChanges": false,
		"position":            position,
		"ids":                 ids,
	}
	if req.CalculateTotal {
		// Exact, not an estimate: the whole set was read and filtered in
		// memory, so `total` is the real count rather than the capped
		// approximation Email/query has to give.
		out["total"] = total
	}
	return out, nil
}

// parseSubmissionFilter reads the §7.3 FilterCondition subset, refusing the
// conditions this server does not answer.
func parseSubmissionFilter(raw json.RawMessage) (submissionFilter, *jmap.MethodError) {
	var f submissionFilter
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return f, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return f, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("filter must be a FilterCondition object (RFC 8621 §7.3)")
	}
	// A FilterOperator (AND/OR/NOT) is refused explicitly rather than being
	// read as a condition with three unknown properties, so the error names
	// the real limitation.
	if _, isOperator := obj["operator"]; isOperator {
		return f, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
			WithDescription("EmailSubmission/query accepts a single FilterCondition; " +
				"AND/OR/NOT operators are not supported")
	}

	for key, val := range obj {
		switch key {
		case "undoStatus":
			var s string
			if err := json.Unmarshal(val, &s); err != nil {
				return f, jmap.NewMethodError(jmap.CodeInvalidArguments).
					WithDescription("undoStatus must be a string")
			}
			switch s {
			case "pending", "final", "canceled":
				f.UndoStatus = s
			default:
				return f, jmap.NewMethodError(jmap.CodeInvalidArguments).
					WithDescription(`undoStatus must be "pending", "final" or "canceled" (RFC 8621 §7.1)`)
			}
		case "before", "after":
			var s string
			if err := json.Unmarshal(val, &s); err != nil {
				return f, jmap.NewMethodError(jmap.CodeInvalidArguments).
					WithDescription("%s must be a UTCDate", key)
			}
			t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
			if err != nil {
				return f, jmap.NewMethodError(jmap.CodeInvalidArguments).
					WithDescription(`%s must be a UTCDate such as "2026-09-01T08:00:00Z"`, key)
			}
			if key == "before" {
				f.Before = t.UTC()
			} else {
				f.After = t.UTC()
			}
		case "identityIds", "emailIds", "threadIds":
			// §5.5: "unsupportedFilter: The filter is syntactically valid, but
			// the server cannot process it." Named individually so the client
			// learns WHICH condition it must drop.
			return f, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
				WithDescription("EmailSubmission/query does not filter on %s; "+
					"this server answers undoStatus, before and after", key)
		default:
			return f, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
				WithDescription("%q is not an EmailSubmission FilterCondition property (RFC 8621 §7.3)", key)
		}
	}
	return f, nil
}

// parseSubmissionSort reads the §5.5 sort array, accepting only sentAt.
//
// The default is DESCENDING, which is not §5.5's default (it has none — sort
// is optional and an absent sort leaves the order server-defined). Newest
// first is chosen because the two views this serves — the Scheduled list and a
// recent-sends list — both read newest-first, and a server-defined order that
// matches the only use is better than an arbitrary one that does not.
func parseSubmissionSort(raw json.RawMessage) (descending bool, merr *jmap.MethodError) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return true, nil
	}
	var comparators []struct {
		Property    string `json:"property"`
		IsAscending *bool  `json:"isAscending"`
	}
	if err := json.Unmarshal(raw, &comparators); err != nil {
		return false, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("sort must be an array of Comparator objects (RFC 8620 §5.5)")
	}
	if len(comparators) == 0 {
		return true, nil
	}
	if len(comparators) > 1 {
		return false, jmap.NewMethodError(jmap.CodeUnsupportedSort).
			WithDescription("EmailSubmission/query sorts by one comparator")
	}
	c := comparators[0]
	if c.Property != submissionSortProperty {
		// §5.5: "unsupportedSort: The sort is syntactically valid, but
		// includes a property the server does not support sorting on."
		return false, jmap.NewMethodError(jmap.CodeUnsupportedSort).
			WithDescription("EmailSubmission/query sorts on %q only; %q is not supported",
				submissionSortProperty, c.Property)
	}
	// §5.5: isAscending "defaults to true" when omitted.
	if c.IsAscending == nil || *c.IsAscending {
		return false, nil
	}
	return true, nil
}
