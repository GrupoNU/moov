package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Email/changes and Mailbox/changes — RFC 8620 §5.2, extended for Mailbox by
// RFC 8621 §2.2.
//
// # The cursor
//
// J2 designed the /get state string to be consumed here (adapter.go
// StateReader): it is `<max(message_state.updated_at) in nanos>-<row count>`,
// which moves forward on every change and never backwards. §5.2 requires
// exactly that of a cursor, because a client hands back the state string a
// /get gave it.
//
// The row COUNT rides along for a reason that matters here: a timestamp alone
// cannot distinguish "nothing changed" from "a tombstone was reaped". A reap
// lowers the count while max(updated_at) stays put, and a client holding the
// old state must see a different string or it will never learn the message is
// gone. changes.go reads only the timestamp half — the count exists so the
// STATE differs, not so the cursor moves.
//
// # Why this cannot be a simple "give me everything after T"
//
// §5.2's three coalescing rules are stated in terms of what happened to a
// record BETWEEN two states, not in terms of its latest row:
//
//	"If a record has been created AND updated since the old state, the server
//	 SHOULD just return the id in the 'created' list..."
//	"If a record has been updated AND destroyed since the old state, the server
//	 SHOULD just return the id in the 'destroyed' list..."
//	"If a record has been created AND destroyed since the old state, the server
//	 SHOULD remove the id from the response entirely."
//
// The store keeps one mutable state row per message, so it cannot replay a
// history — but it does not need to. Each row carries messages.created_at and
// message_state.deleted_at, and those two facts against the client's cursor
// decide all three rules exactly. classify() below is that decision, one
// branch per rule, with the rule quoted.

// changesRequest is the §5.2 arguments object.
//
// MaxChanges is *uint64 because §5.2 distinguishes absent ("the server may
// choose how many to return") from a given value, and requires rejecting 0:
// "If supplied by the client, the value MUST be a positive integer greater
// than 0. If a value outside of this range is given, the server MUST reject
// the call with an 'invalidArguments' error."
type changesRequest struct {
	AccountID  string  `json:"accountId"`
	SinceState string  `json:"sinceState"`
	MaxChanges *uint64 `json:"maxChanges"`
}

// changesResponse is the §5.2 response.
type changesResponse struct {
	AccountID      string   `json:"accountId"`
	OldState       string   `json:"oldState"`
	NewState       string   `json:"newState"`
	HasMoreChanges bool     `json:"hasMoreChanges"`
	Created        []string `json:"created"`
	Updated        []string `json:"updated"`
	Destroyed      []string `json:"destroyed"`
}

// mailboxChangesResponse adds RFC 8621 §2.2's extra response argument.
//
// UpdatedProperties is *[]string so the RFC's two distinct answers are both
// representable: a property list, or JSON null for "the server is unable to
// tell if only counts have changed".
type mailboxChangesResponse struct {
	changesResponse
	UpdatedProperties *[]string `json:"updatedProperties"`
}

// newChangesResponse returns a response whose id arrays are non-nil, so an
// empty result marshals as [] rather than null — §5.2 types all three as Id[].
func newChangesResponse(accountID, oldState string) *changesResponse {
	return &changesResponse{
		AccountID: accountID,
		OldState:  oldState,
		Created:   []string{},
		Updated:   []string{},
		Destroyed: []string{},
	}
}

// countProperties is the RFC 8621 §2.2 updatedProperties value: exactly the
// four count properties the RFC names, and nothing else.
var countProperties = []string{"totalEmails", "unreadEmails", "totalThreads", "unreadThreads"}

// handleEmailChanges implements Email/changes.
func (d *Deps) handleEmailChanges(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseChanges(ctx, args)
	if merr != nil {
		return nil, merr
	}

	window, merr := d.loadChangeWindow(ctx, caller.AccountID, req)
	if merr != nil {
		return nil, merr
	}

	resp := newChangesResponse(req.AccountID, req.SinceState)
	for _, c := range window.rows {
		switch classify(c, window.since) {
		case changeCreated:
			resp.Created = append(resp.Created, EncodeEmailID(c.MessageID))
		case changeUpdated:
			resp.Updated = append(resp.Updated, EncodeEmailID(c.MessageID))
		case changeDestroyed:
			resp.Destroyed = append(resp.Destroyed, EncodeEmailID(c.MessageID))
		case changeOmitted:
			// §5.2: created AND destroyed since the old state — "the server
			// SHOULD remove the id from the response entirely". The client
			// never saw this message and never will; telling it about a
			// message it must then discard is pure noise.
		}
	}
	resp.NewState = window.newState
	resp.HasMoreChanges = window.hasMore
	return resp, nil
}

// handleMailboxChanges implements Mailbox/changes (RFC 8621 §2.2).
//
// # Granularity, decided honestly
//
// A Mailbox object changes when its ROW changes (name, role, subscription) or
// when its COUNTS change — and its counts change on every message write, which
// is why J2's MailboxState shares the Email watermark (adapter.go: "a state
// derived only from the mailboxes table would fail to advance when totalEmails
// did").
//
// So this method reports a mailbox as UPDATED when either happened, and never
// reports created/destroyed. That last part is a real limitation stated
// plainly: the store's mailboxes table has no tombstone and no first-seen
// cursor comparable to messages.created_at, so a folder created or deleted
// since the client's state is reported as an update if its row moved, and
// missed entirely if it was deleted outright.
//
// The consequence for a client is bounded and self-correcting: Mailbox/get
// with ids:null returns the true folder list, which is what every client does
// on connect, and the folder set of a mailbox account changes rarely. The fix
// is a mailboxes tombstone column, named in the J3 report — not a guess here.
func (d *Deps) handleMailboxChanges(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseChanges(ctx, args)
	if merr != nil {
		return nil, merr
	}

	since, merr := cursorFromState(req.SinceState)
	if merr != nil {
		return nil, merr
	}
	if merr := d.checkCursorReachable(ctx, caller.AccountID, since); merr != nil {
		return nil, merr
	}

	counts, rowsChanged, err := d.Changes.MailboxesTouchedSince(ctx, caller.AccountID, since, maxMailboxesPerChanges)
	if err != nil {
		return nil, serverFail("reading mailbox changes", err)
	}

	state, err := d.State.MailboxState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading mailbox state", err)
	}

	resp := &mailboxChangesResponse{
		changesResponse: *newChangesResponse(req.AccountID, req.SinceState),
	}
	for _, id := range mergeMailboxIDs(counts, rowsChanged) {
		resp.Updated = append(resp.Updated, EncodeMailboxID(id))
	}
	resp.NewState = state
	// The whole folder set fits in one response (an account has tens of
	// mailboxes, and the query is bounded at maxMailboxesPerChanges), so there
	// is never a second page to fetch.
	resp.HasMoreChanges = false

	// §2.2: "If only the 'totalEmails', 'unreadEmails', 'totalThreads', and/or
	// 'unreadThreads' Mailbox properties have changed since the old state, this
	// will be the list of properties that may have changed. If the server is
	// unable to tell if only counts have changed, it MUST just be null."
	//
	// Moov CAN tell: counts move in message_state, everything else moves in the
	// mailboxes row, and the two were queried separately. So when no mailbox
	// row changed, the honest answer is the property list — which lets the
	// client back-reference it into a Mailbox/get and fetch four integers
	// instead of the whole object, exactly the optimization §2.2 describes.
	if len(rowsChanged) == 0 && len(counts) > 0 {
		props := append([]string(nil), countProperties...)
		resp.UpdatedProperties = &props
	}
	return resp, nil
}

// changeWindow is one page of changes plus the state that follows it.
type changeWindow struct {
	rows     []ChangeRow
	since    time.Time
	newState string
	hasMore  bool
}

// loadChangeWindow reads one maxChanges-bounded page of changes and computes
// the state that follows it.
func (d *Deps) loadChangeWindow(ctx context.Context, accountID int64, req *changesRequest) (*changeWindow, *jmap.MethodError) {
	since, merr := cursorFromState(req.SinceState)
	if merr != nil {
		return nil, merr
	}
	if merr := d.checkCursorReachable(ctx, accountID, since); merr != nil {
		return nil, merr
	}

	limit := defaultChangesLimit
	if req.MaxChanges != nil {
		// §5.2: "the server MUST ensure the number of ids returned across
		// 'created', 'updated', and 'destroyed' does not exceed this limit".
		// Since each row yields at most one id, fetching maxChanges rows
		// satisfies it.
		//
		// The ceiling makes the conversion exact regardless of what the client
		// sent: the result is at most maxChangesCeiling, a small constant.
		capped := min(*req.MaxChanges, uint64(maxChangesCeiling))
		limit = int(capped) //nolint:gosec // capped to maxChangesCeiling on the line above
	}

	// One row past the limit, so "is there more?" is answered by the fetch
	// itself rather than by a second count query.
	rows, err := d.Changes.ChangedSince(ctx, accountID, since, limit+1)
	if err != nil {
		return nil, serverFail("reading changes", err)
	}

	w := &changeWindow{since: since}
	if len(rows) > limit {
		// §5.2: "If there are more changes than this between the client's state
		// and the current server state, the server SHOULD generate an update to
		// take the client to an intermediate state, from which the client can
		// continue to call 'Foo/changes' until it is fully up to date."
		//
		// The intermediate state is the watermark of the LAST row returned. It
		// is a real, resumable cursor rather than an opaque token because the
		// feed is ordered by (updated_at, message_id) — so resuming from it
		// yields exactly the rows this page did not.
		//
		// This server returns the OLDEST changes first, not the newest. §5.2
		// suggests the reverse ("for many types, it will provide a better user
		// experience to return the more recent changes first"), but that is a
		// SHOULD about user experience, and the same section imposes a MUST
		// that oldest-first satisfies for free: "the server MUST NOT return a
		// record as created after a response that deems it as updated or
		// destroyed". Replaying history in the order it happened cannot violate
		// that ordering rule; serving newest-first out of a single mutable
		// state row could.
		rows = rows[:limit]
		w.hasMore = true
	}
	w.rows = rows

	switch {
	case w.hasMore:
		// Resume from the last row this page delivered.
		w.newState = stateForCursor(rows[len(rows)-1].UpdatedAt)
	default:
		// Caught up: the new state is the account's current data state, which
		// is the same string a /get would hand out — so a client that syncs
		// through /changes and one that refetches through /get converge on the
		// identical cursor.
		state, err := d.State.EmailState(ctx, accountID)
		if err != nil {
			return nil, serverFail("reading email state", err)
		}
		w.newState = state
	}
	return w, nil
}

// checkCursorReachable refuses a cursor the store can no longer enumerate from.
//
// # What this can and cannot detect, and why the difference matters
//
// §5.2 wants cannotCalculateChanges when "the client's state being too old" —
// i.e. when the rows that would say what the client missed have been REAPED.
// Detecting that requires knowing the reap horizon: the instant before which
// tombstones no longer exist.
//
// The store does not record one. It tombstones rather than deletes precisely
// so /changes keeps working (messages.go MarkDeleted), and reaping "after all
// clients have caught up" is called out there as a later concern — so today
// nothing is ever reaped and no cursor is genuinely unanswerable.
//
// The tempting proxy — min(message_state.updated_at), the oldest change the
// account still holds — is WRONG, and wrong in the dangerous direction. That
// value moves FORWARD every time a row is touched, because updated_at is
// rewritten in place rather than appended. An account whose every message has
// been reflagged since a client last synced has a min() later than that
// client's perfectly valid cursor, and comparing the two would answer
// cannotCalculateChanges to a client that is merely up to date, forcing a full
// mailbox reload on every routine sync. It was measured doing exactly that
// against real rows while this was being written.
//
// So this checks only what it can actually know: that the cursor is not in the
// FUTURE relative to the account's own watermark. A future cursor cannot have
// come from this account's data — it is a state from another account, another
// installation, or a corrupted cache — and enumerating from it would silently
// report "no changes" forever.
//
// When a reap job lands (E3's tombstone reaping), it must record the horizon
// it reaped to — a `reaped_through` column on the account, or a sync_log row —
// and this function gains one comparison against it. That is named in the J3
// report as the prerequisite for reaping, precisely so reaping cannot ship
// without it and silently start losing changes.
func (d *Deps) checkCursorReachable(ctx context.Context, accountID int64, since time.Time) *jmap.MethodError {
	// The zero cursor means "from the beginning", which is always answerable:
	// every surviving row is after it.
	if since.IsZero() {
		return nil
	}

	newest, err := d.Changes.NewestChangeAt(ctx, accountID)
	if err != nil {
		return serverFail("reading the change watermark", err)
	}
	// An account with no rows has nothing to report; an empty answer is
	// correct rather than an error.
	if newest.IsZero() {
		return nil
	}
	// A cursor at or before the account's own watermark is enumerable: nothing
	// is reaped, so every change after it is still there.
	if !since.After(newest) {
		return nil
	}

	// A cursor AHEAD of everything this account has ever recorded was not
	// issued from this account's data.
	//
	// §5.2: "'cannotCalculateChanges': The server cannot calculate the changes
	// from the state string given by the client... The client MUST invalidate
	// its Foo cache." A full reload is the correct recovery; answering "no
	// changes" would leave the client permanently and silently stale.
	return jmap.NewMethodError(jmap.CodeCannotCalculateChanges).
		WithDescription("the given state is ahead of this account's data and was not issued for it; " +
			"reload from Email/query and Email/get")
}

// changeKind is the §5.2 bucket a changed record falls into.
type changeKind int

const (
	changeCreated changeKind = iota
	changeUpdated
	changeDestroyed
	changeOmitted
)

// classify applies the three §5.2 coalescing rules to one changed row.
//
// The client's cursor is the "old state"; a row's created_at and its tombstone
// decide the rest. Each branch quotes the rule it implements.
func classify(c ChangeRow, since time.Time) changeKind {
	// "Created since the old state" means the message row was written after the
	// client's cursor. A zero cursor (a client with no prior state) makes
	// everything created, which is right: it has seen nothing.
	created := since.IsZero() || c.CreatedAt.After(since)

	switch {
	case created && c.Destroyed:
		// "If a record has been created AND destroyed since the old state, the
		// server SHOULD remove the id from the response entirely."
		return changeOmitted

	case c.Destroyed:
		// "If a record has been updated AND destroyed since the old state, the
		// server SHOULD just return the id in the 'destroyed' list" — and a
		// row that existed before the cursor and is now a tombstone is exactly
		// that case. The tombstone is what the store keeps expressly so this
		// answer is possible (messages.go MarkDeleted).
		return changeDestroyed

	case created:
		// "If a record has been created AND updated since the old state, the
		// server SHOULD just return the id in the 'created' list" — so a row
		// created after the cursor is reported as created regardless of how
		// many times it has been touched since.
		return changeCreated

	default:
		return changeUpdated
	}
}

// ---------------------------------------------------------------------------
// queryChanges
// ---------------------------------------------------------------------------

// handleEmailQueryChanges and handleMailboxQueryChanges implement the
// /queryChanges methods of RFC 8620 §5.6 by declining, which is a conforming
// answer and a deliberate architectural decision (ADR §2, L2 §2.3: "queryChanges
// → cannotCalculateChanges (legítimo)").
//
// # Why declining is the right answer rather than a gap
//
// §5.6 requires a server that answers queryChanges to report the exact
// positional delta of a result LIST between two query states — which ids left
// the list, which joined, and at which index. Computing that requires either
// storing every client's last result list, or being able to reconstruct the
// full ordered result set at an arbitrary past state. This server can do
// neither: its result sets are windowed at 200 by the search repertoire (S3),
// and it stores no per-client query state.
//
// A server that guessed here would corrupt client caches silently, which is
// far worse than the honest refusal §5.6 explicitly provides for. The client's
// prescribed fallback — re-run Email/query and diff locally — is cheap,
// because the id list is small and Email/query is 9 ms.
//
// Email/query already tells clients this in advance through
// canCalculateChanges:false (§5.5), so a well-behaved client never calls these
// at all. They are registered so the methods EXIST and answer conformingly
// rather than returning unknownMethod, which a client would read as a broken
// or partial server.
func (d *Deps) handleEmailQueryChanges(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	return queryChangesRefusal(ctx, args, "Email")
}

func (d *Deps) handleMailboxQueryChanges(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	return queryChangesRefusal(ctx, args, "Mailbox")
}

// queryChangesRefusal validates the caller and account, then declines.
//
// The account check runs FIRST so that a request naming somebody else's
// account gets accountNotFound rather than cannotCalculateChanges — the
// refusal must never become an oracle that confirms another account exists.
func queryChangesRefusal(ctx context.Context, args json.RawMessage, typeName string) (any, *jmap.MethodError) {
	caller, ok := jmap.CallerFromContext(ctx)
	if !ok {
		return nil, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}
	var req struct {
		AccountID string `json:"accountId"`
	}
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
	return nil, jmap.NewMethodError(jmap.CodeCannotCalculateChanges).
		WithDescription("%s/queryChanges is not supported; re-run %s/query and diff locally "+
			"(the query response advertises canCalculateChanges:false)", typeName, typeName)
}

// ---------------------------------------------------------------------------
// cursor encoding
// ---------------------------------------------------------------------------

// parseChanges decodes and validates the common /changes arguments.
func parseChanges(ctx context.Context, args json.RawMessage) (*changesRequest, jmap.Caller, *jmap.MethodError) {
	caller, ok := jmap.CallerFromContext(ctx)
	if !ok {
		return nil, caller, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}

	var req changesRequest
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
	if req.SinceState == "" {
		return nil, caller, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("the sinceState argument is required")
	}
	// §5.2: "If supplied by the client, the value MUST be a positive integer
	// greater than 0. If a value outside of this range is given, the server
	// MUST reject the call with an 'invalidArguments' error."
	if req.MaxChanges != nil && *req.MaxChanges == 0 {
		return nil, caller, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("maxChanges must be a positive integer greater than 0")
	}
	return &req, caller, nil
}

// cursorFromState parses a state string back into the watermark it encodes.
//
// The grammar is J2's (adapter.go stateFor): "<nanos>-<count>". A string that
// does not parse is not an error this server invents a meaning for — it is a
// state this server never issued, which §5.2 answers with
// cannotCalculateChanges ("The server cannot calculate the changes from the
// state string given by the client"). Guessing "start from zero" instead would
// silently hand a client the entire mailbox as "created", which looks like
// success and is not.
func cursorFromState(state string) (time.Time, *jmap.MethodError) {
	nanos, _, ok := strings.Cut(state, "-")
	if !ok {
		return time.Time{}, jmap.NewMethodError(jmap.CodeCannotCalculateChanges).
			WithDescription("the given state was not issued by this server")
	}
	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil || n < 0 {
		return time.Time{}, jmap.NewMethodError(jmap.CodeCannotCalculateChanges).
			WithDescription("the given state was not issued by this server")
	}
	if n == 0 {
		// stateFor renders an account that has never had a message as "0-0";
		// the zero time is the cursor that means "everything".
		return time.Time{}, nil
	}
	return time.Unix(0, n).UTC(), nil
}

// stateForCursor renders an intermediate cursor in the same grammar J2's
// stateFor uses, so a client cannot tell an intermediate state from a settled
// one — and must not, since both are opaque and both resume correctly.
//
// The shape is "<nanos>-<count>" with the count fixed at 0. cursorFromState
// reads only the nanos half, so the count is inert for resumption; it is in
// the grammar so that J2's settled states and J3's intermediate ones are
// indistinguishable on the wire, which is what keeps the state opaque as RFC
// 8620 §5.2 intends.
func stateForCursor(t time.Time) string {
	return fmt.Sprintf("%d-0", t.UTC().UnixNano())
}
