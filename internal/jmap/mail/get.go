package mail

import (
	"context"
	"encoding/json"

	"github.com/GrupoNU/moov/internal/jmap"
)

// The /get method skeleton shared by Mailbox/get, Thread/get and Email/get
// (RFC 8620 §5.1).

// getRequest is the standard /get arguments object.
//
// ids is *[]string rather than []string because §5.1 gives null a distinct
// meaning from absent-or-empty: "If null, then all records ... are returned".
// A plain slice cannot tell `"ids": null` from `"ids": []`, and conflating
// them would turn a request for nothing into a request for the whole mailbox.
type getRequest struct {
	AccountID  string    `json:"accountId"`
	IDs        *[]string `json:"ids"`
	Properties *[]string `json:"properties"`
}

// getResponse is the standard /get response object (§5.1).
//
// State is the "state" property: "A (preferably short) string representing the
// state on the server for all the data of this type in the account". Phase 1
// has no /changes yet (J3 owns it), so state is derived per type from the
// account's data — see state.go for why that is honest rather than a stub.
//
// NotFound must always marshal as an array, never null: §5.1 types it as
// Id[], and clients iterate it without a null check.
type getResponse struct {
	AccountID string   `json:"accountId"`
	State     string   `json:"state"`
	List      []any    `json:"list"`
	NotFound  []string `json:"notFound"`
}

// newGetResponse returns a response with its slices non-nil, so an empty
// result marshals as [] rather than null.
func newGetResponse(accountID, state string) *getResponse {
	return &getResponse{
		AccountID: accountID,
		State:     state,
		List:      []any{},
		NotFound:  []string{},
	}
}

// parseGet decodes and validates the common /get arguments.
//
// It performs, in order, the three checks every /get owes the caller:
//
//  1. the arguments parse (invalidArguments, §3.6.2);
//  2. the accountId is the authenticated caller's own (accountNotFound) — a
//     request naming somebody else's account is refused before any read, which
//     is what keeps account scoping from depending on each handler remembering
//     to pass the right id;
//  3. the id count is within maxObjectsInGet (requestTooLarge, §5.1), checked
//     BEFORE touching the store, as the J2 contract in limits.go requires.
func parseGet(ctx context.Context, args json.RawMessage, limits jmap.Limits) (*getRequest, jmap.Caller, *jmap.MethodError) {
	caller, ok := jmap.CallerFromContext(ctx)
	if !ok {
		// A handler running without an authenticated caller is a transport
		// wiring bug. forbidden is the only safe answer: never guess an
		// account (caller.go says exactly this).
		return nil, caller, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}

	var req getRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, caller, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("arguments did not parse: %v", err)
	}

	// §5.1 makes accountId required. RFC 8620 §3.6.2 defines accountNotFound
	// as "The accountId does not correspond to a valid account" — which is the
	// right answer for another user's account too, since it does not
	// correspond to a valid account *for this caller*, and saying anything
	// more specific would confirm the other account exists.
	if req.AccountID == "" {
		return nil, caller, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("the accountId argument is required")
	}
	if req.AccountID != caller.JMAPAccountID() {
		return nil, caller, jmap.NewMethodError(jmap.CodeAccountNotFound)
	}

	if req.IDs != nil {
		if merr := limits.CheckObjectsInGet(len(*req.IDs)); merr != nil {
			return nil, caller, merr
		}
	}

	return &req, caller, nil
}

// propertySet turns the requested property list into a lookup, and reports
// whether the client asked for a specific set at all.
//
// §5.1: "If null, all properties of the object are returned". A property the
// server does not recognize is an invalidArguments error per §5.1 ("If any of
// the properties are not valid ... MUST return invalidArguments"), which the
// callers check against their own known set.
func propertySet(requested *[]string) (set map[string]bool, selective bool) {
	if requested == nil {
		return nil, false
	}
	set = make(map[string]bool, len(*requested))
	for _, p := range *requested {
		set[p] = true
	}
	return set, true
}

// wants reports whether a property should be included. A nil set means the
// client asked for everything.
func wants(set map[string]bool, property string) bool {
	return set == nil || set[property]
}

// unknownProperties returns the requested properties that are not in known,
// so a handler can answer invalidArguments naming them.
func unknownProperties(requested *[]string, known map[string]bool) []string {
	if requested == nil {
		return nil
	}
	var bad []string
	for _, p := range *requested {
		if !known[p] {
			bad = append(bad, p)
		}
	}
	return bad
}
