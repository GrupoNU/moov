package mail

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Mailbox/get — RFC 8621 §2.1 over the §2 Mailbox object.

// mailboxProperties is every property this server implements, and therefore
// the set a selective `properties` argument is validated against (RFC 8620
// §5.1: an unrecognized property is invalidArguments).
//
// It is the complete §2 property list. "id" is included even though §5.1 says
// it "MUST always be returned" whether asked for or not — listing it keeps a
// client that names it explicitly from getting invalidArguments for a
// perfectly legal request.
var mailboxProperties = map[string]bool{
	"id":            true,
	"name":          true,
	"parentId":      true,
	"role":          true,
	"sortOrder":     true,
	"totalEmails":   true,
	"unreadEmails":  true,
	"totalThreads":  true,
	"unreadThreads": true,
	"myRights":      true,
	"isSubscribed":  true,
}

// mailboxRights is the myRights value every mailbox reports.
//
// RFC 8621 §2 defines MailboxRights with nine boolean members, and is
// explicit that these rights are what the client uses to decide which
// actions to present — so the rule here (regla J1) is that a right is true
// exactly when a registered method actually honors it. The truth as of W1
// (Email/set):
//
//   - mayReadItems: the read families of J2/J3.
//   - maySetSeen / maySetKeywords: Email/set keywords, both forms.
//   - mayAddItems / mayRemoveItems: §2 defines them as adding/removing "the
//     ids of Emails to/from this Mailbox (by either creating a new Email or
//     MOVING an existing one)" — Email/set mailboxIds moves are real, and
//     destroy removes (W-A2). Email CREATION is still refused (W3), which a
//     client discovers per-call; the alternative — advertising
//     mayAddItems:false — would hide the moves that DO work behind a right
//     the RFC scopes to both.
//   - mayCreateChild / mayRename / mayDelete: false until Mailbox/set (W2).
//   - maySubmit: false until EmailSubmission (W3).
type mailboxRights struct {
	MayReadItems   bool `json:"mayReadItems"`
	MayAddItems    bool `json:"mayAddItems"`
	MayRemoveItems bool `json:"mayRemoveItems"`
	MaySetSeen     bool `json:"maySetSeen"`
	MaySetKeywords bool `json:"maySetKeywords"`
	MayCreateChild bool `json:"mayCreateChild"`
	MayRename      bool `json:"mayRename"`
	MayDelete      bool `json:"mayDelete"`
	MaySubmit      bool `json:"maySubmit"`
}

// writableRights is the phase-2 (W1) answer for every mailbox.
func writableRights() mailboxRights {
	return mailboxRights{
		MayReadItems:   true,
		MayAddItems:    true,
		MayRemoveItems: true,
		MaySetSeen:     true,
		MaySetKeywords: true,
	}
}

// handleMailboxGet implements Mailbox/get.
func (d *Deps) handleMailboxGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	if bad := unknownProperties(req.Properties, mailboxProperties); len(bad) > 0 {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("unknown Mailbox properties: %v", bad)
	}
	props, _ := propertySet(req.Properties)

	state, err := d.State.MailboxState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading mailbox state", err)
	}
	resp := newGetResponse(req.AccountID, state)

	var rows []MailboxRow
	if req.IDs == nil {
		// §5.1: "If null, then all records of the data type ... are returned".
		rows, err = d.Mailboxes.Mailboxes(ctx, caller.AccountID)
		if err != nil {
			return nil, serverFail("listing mailboxes", err)
		}
	} else {
		ids, wire, unknown := decodeIDList(*req.IDs, DecodeMailboxID)
		resp.NotFound = append(resp.NotFound, unknown...)

		rows, err = d.Mailboxes.MailboxesByID(ctx, caller.AccountID, ids)
		if err != nil {
			return nil, serverFail("reading mailboxes", err)
		}
		// Every requested id the reader did not return is notFound — whether
		// it does not exist or belongs to another account (contracts.go: the
		// two are deliberately indistinguishable).
		found := make(map[int64]bool, len(rows))
		for _, r := range rows {
			found[r.ID] = true
		}
		for _, id := range ids {
			if !found[id] {
				resp.NotFound = append(resp.NotFound, wire[id])
			}
		}
	}

	// A stable order for a set-valued response: the RFC imposes none, and an
	// unstable one would make golden tests flap and diffs against jmap-perl
	// unreadable.
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })

	for _, row := range rows {
		resp.List = append(resp.List, renderMailbox(row, props))
	}
	return resp, nil
}

// renderMailbox builds one Mailbox object (RFC 8621 §2).
func renderMailbox(row MailboxRow, props map[string]bool) map[string]any {
	// §5.1: "The id property of each object is always returned, even if not
	// explicitly requested."
	out := map[string]any{"id": EncodeMailboxID(row.ID)}

	if wants(props, "name") {
		out["name"] = row.Name
	}
	if wants(props, "parentId") {
		// §2: "The Mailbox id for the parent of this Mailbox, or null if this
		// Mailbox is at the top level". The store's 0 means top level.
		if row.ParentID > 0 {
			out["parentId"] = EncodeMailboxID(row.ParentID)
		} else {
			out["parentId"] = nil
		}
	}
	if wants(props, "role") {
		if role, ok := jmapRole(row.Role); ok {
			out["role"] = role
		} else {
			out["role"] = nil
		}
	}
	if wants(props, "sortOrder") {
		out["sortOrder"] = row.SortOrder
	}
	if wants(props, "totalEmails") {
		out["totalEmails"] = row.TotalEmails
	}
	if wants(props, "unreadEmails") {
		out["unreadEmails"] = row.UnreadEmails
	}
	if wants(props, "totalThreads") {
		out["totalThreads"] = row.TotalThreads
	}
	if wants(props, "unreadThreads") {
		out["unreadThreads"] = row.UnreadThreads
	}
	if wants(props, "myRights") {
		out["myRights"] = writableRights()
	}
	if wants(props, "isSubscribed") {
		out["isSubscribed"] = row.IsSubscribed
	}
	return out
}

// serverFail wraps an unexpected reader error as the RFC 8620 §3.6.2
// serverFail method error.
//
// The underlying error is deliberately NOT put on the wire: it can name
// database objects and constraint text, which is information a client has no
// business seeing. It goes to the log through the dispatcher's own error path
// instead, and the description carries only what operation failed.
func serverFail(what string, err error) *jmap.MethodError {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// The client hung up or the request timed out; the server did not
		// fail. §3.6.2's serverUnavailable ("Some internal server resource was
		// temporarily unavailable") is the honest code, and it tells the
		// client the call is worth retrying.
		return jmap.NewMethodError(jmap.CodeServerUnavailable).
			WithDescription("%s: request canceled or timed out", what)
	}
	return jmap.NewMethodError(jmap.CodeServerFail).WithDescription("%s failed", what)
}
