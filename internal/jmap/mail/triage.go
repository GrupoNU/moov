package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
	syncengine "github.com/GrupoNU/moov/internal/sync"
)

// Snooze and Mute — Moov's vendor triage objects, served under
// jmap.CapTriage (L3 epic E4, canon §2.2, arbitration GC-10).
//
// ===========================================================================
// WHY THESE ARE NEW OBJECTS AND NOT PROPERTIES ON Email
// ===========================================================================
//
// The tempting design is a `snoozedUntil` property on Email and a `muted`
// property on Thread. Both are refused, for the same reason:
//
//   - RFC 8621 §4.1 enumerates Email's properties and §4.6 makes exactly
//     three of them mutable (keywords, mailboxIds, and the receivedAt of a
//     draft). Adding a fourth in the standard namespace hands every
//     conforming client — including the Bulwark oracle this project checks
//     itself against — a property RFC 8621 does not define, in a namespace
//     it is entitled to assume it fully understands.
//   - RFC 8621 §3 gives Thread exactly two properties, both server-set, and
//     no /set method at all. There is no update to hang `muted` on.
//
// So the objects are new, under a URI Moov controls, per RFC 8620 §2 — the
// identical argument capabilities.go makes for Prefs, applied to a different
// question.
//
// ===========================================================================
// THE SURFACE, AND WHY IT IS THIS SMALL
// ===========================================================================
//
// Snooze/get   — the account's pending snoozes.
// Snooze/set   — create (snooze) and destroy (un-snooze).
// Mute/get     — the account's muted conversations.
// Mute/set     — create (mute) and destroy (unmute).
//
// There is deliberately NO Snooze/query, no Mute/query and no `in:snoozed`
// filter condition, and the absence is the design rather than an omission:
//
//   - `in:snoozed` is the SNOOZED MAILBOX (GC-10 makes snoozing a MOVE), so
//     it is answered by the Email/query filter condition that already exists,
//     `inMailbox`. Adding a second spelling for a question the standard
//     already answers would be two code paths that must agree forever.
//   - `is:muted` is answered by Mute/get: a client renders the badge from a
//     set of thread ids it already holds. A vendor FILTER CONDITION on
//     Email/query was the alternative and it is worse — it would make every
//     mail search join against the mute table for a predicate whose whole
//     result set is, in practice, a few dozen ids a client can cache. The
//     honest minimal surface is the one that answers the question without
//     touching the search path (S3's discipline: measure a new query shape or
//     do not ship it).
//
// # The id spaces
//
// A Snooze's id is the EMAIL's id, and a Mute's id is the THREAD's id. Both
// are deliberate: it makes the objects addressable by what a client already
// has in hand (the message it is looking at, the conversation it is reading)
// with no second identifier to map, and it makes /set naturally idempotent —
// snoozing the same message twice names the same object.

// SnoozeMailboxName is the folder snoozed mail lives in, re-exported from the
// sync engine (internal/sync/snooze.go, which owns the choice and the reason
// there is no SPECIAL-USE role to claim).
//
// It is re-exported rather than duplicated so the session object can advertise
// it — a client cannot resolve the folder by role, so the name is part of the
// capability's contract — without internal/jmaphttp importing the sync engine.
// One constant, two readers, no chance of them disagreeing.
const SnoozeMailboxName = syncengine.SnoozeMailboxName

// snoozeProperties and muteProperties are the served property sets.
var (
	snoozeProperties = map[string]bool{
		"id": true, "emailId": true, "until": true, "originMailboxName": true,
	}
	muteProperties = map[string]bool{
		"id": true, "threadId": true,
	}
)

// maxSnoozeHorizon bounds how far in the future a wake may be set.
//
// Gmail publishes no ceiling (canon §5 records its preset times as unsourced
// too), so this is Moov's number and it is stated rather than inherited. Five
// years is far past any plausible "remind me later" and near enough that a
// wake time is still a date a human recognizes rather than a rounding artifact
// of a client sending a millisecond timestamp as seconds — which is the actual
// failure this bound catches.
const maxSnoozeHorizon = 5 * 365 * 24 * time.Hour

// ---------------------------------------------------------------------------
// contracts
// ---------------------------------------------------------------------------

// SnoozeRecord is one pending snooze as the handlers see it.
type SnoozeRecord struct {
	// EmailID is the store id of the snoozed message.
	EmailID int64
	// Until is the wake time.
	Until time.Time
	// OriginMailboxName is the IMAP name of the folder it will return to.
	// Empty means the inbox.
	OriginMailboxName string
}

// TriageStore is the snooze/mute surface as the JMAP layer sees it. The
// store-and-executor-backed implementation is triage_adapter.go.
type TriageStore interface {
	// ListSnoozes returns the account's pending snoozes.
	ListSnoozes(ctx context.Context, accountID int64, limit int) ([]SnoozeRecord, error)
	// SnoozeState is the state cursor, in the shared "<nanos>-<count>" grammar.
	SnoozeState(ctx context.Context, accountID int64) (string, error)
	// Snooze moves a message to the Snoozed folder and records its wake.
	Snooze(ctx context.Context, accountID, messageID int64, until time.Time) (SnoozeRecord, error)
	// Unsnooze returns a snoozed message to its origin now.
	Unsnooze(ctx context.Context, accountID, messageID int64) error

	// ListMutes returns the account's muted conversations, as Thread ids.
	ListMutes(ctx context.Context, accountID int64, limit int) ([]int64, error)
	// MuteState is the mute state cursor.
	MuteState(ctx context.Context, accountID int64) (string, error)
	// SetMuted mutes or unmutes one conversation.
	SetMuted(ctx context.Context, accountID, threadID int64, muted bool) error
}

// ErrSnoozeUnavailable means the Snoozed folder could not be prepared, so
// nothing was moved and nothing recorded.
var ErrSnoozeUnavailable = errors.New("mail: the Snoozed mailbox is unavailable")

// ErrNotSnoozed means an un-snooze named a message that is not snoozed.
var ErrNotSnoozed = errors.New("mail: the message is not snoozed")

// ---------------------------------------------------------------------------
// registration
// ---------------------------------------------------------------------------

// RegisterTriageMethods mounts the snooze and mute methods under the vendor
// triage capability.
//
// Same contract as every registrar here: a missing dependency panics at
// STARTUP, because a server that advertises the capability and cannot answer
// it is lying to every client that opted in.
//
// /changes is NOT registered for either type, and that is a decision rather
// than an oversight. Both objects are small, bounded sets an account holds a
// handful of, and both /get calls return the WHOLE set in one indexed read —
// so the recovery a /changes exists to avoid (refetch everything) is already
// the cheap path. Registering a /changes that merely declined would add a
// method for a client to call and get nothing from; registering a real one
// would add a watermark, a tombstone table and a coalescing rule set for a
// list of thirty ids. The state string still moves on every write, so a client
// polling /get on a state change gets its answer.
func RegisterTriageMethods(registry *jmap.Registry, deps *Deps) {
	if registry == nil || deps == nil {
		panic("mail: RegisterTriageMethods requires a registry and deps")
	}
	if deps.Triage == nil {
		panic("mail: RegisterTriageMethods requires a Triage store")
	}
	if deps.Emails == nil || deps.Threads == nil {
		// Both /set paths resolve wire ids against the account's real objects
		// before touching anything; without the readers they could not tell a
		// foreign id from an unknown one.
		panic("mail: RegisterTriageMethods requires Emails and Threads readers")
	}
	registry.Register("Snooze/get", jmap.CapTriage, deps.handleSnoozeGet)
	registry.Register("Snooze/set", jmap.CapTriage, deps.handleSnoozeSet)
	registry.Register("Mute/get", jmap.CapTriage, deps.handleMuteGet)
	registry.Register("Mute/set", jmap.CapTriage, deps.handleMuteSet)
}

// ---------------------------------------------------------------------------
// Snooze/get
// ---------------------------------------------------------------------------

// handleSnoozeGet implements the standard /get (RFC 8620 §5.1) over the
// snooze set.
//
// ids:null returns every pending snooze — §5.1's "all records in the data
// set" — which is bounded by the account's own behavior and by the store's
// limit. An ids array is honored literally, and an id naming a message that is
// not snoozed lands in notFound, which is §5.1's answer for "a record that
// does not exist".
func (d *Deps) handleSnoozeGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	if bad := unknownProperties(req.Properties, snoozeProperties); len(bad) > 0 {
		sort.Strings(bad)
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("unknown Snooze properties: %s", strings.Join(bad, ", "))
	}

	state, err := d.Triage.SnoozeState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the snooze state", err)
	}
	records, err := d.Triage.ListSnoozes(ctx, caller.AccountID, d.Limits.MaxObjectsInGet)
	if err != nil {
		return nil, serverFail("listing snoozes", err)
	}

	resp := newGetResponse(req.AccountID, state)
	props, _ := propertySet(req.Properties)

	if req.IDs == nil {
		for _, r := range records {
			resp.List = append(resp.List, snoozeObject(r, props))
		}
		return resp, nil
	}

	byEmail := make(map[int64]SnoozeRecord, len(records))
	for _, r := range records {
		byEmail[r.EmailID] = r
	}
	for _, wire := range *req.IDs {
		id, err := DecodeEmailID(wire)
		if err != nil {
			resp.NotFound = append(resp.NotFound, wire)
			continue
		}
		r, ok := byEmail[id]
		if !ok {
			resp.NotFound = append(resp.NotFound, wire)
			continue
		}
		resp.List = append(resp.List, snoozeObject(r, props))
	}
	sort.Strings(resp.NotFound)
	return resp, nil
}

// snoozeObject renders one Snooze, honoring the /get properties filter. id is
// always present (§5.1).
func snoozeObject(r SnoozeRecord, props map[string]bool) map[string]any {
	out := map[string]any{"id": EncodeEmailID(r.EmailID)}
	if wants(props, "emailId") {
		// The same value as id, spelled as what it IS. Redundant on the wire
		// and worth the bytes: a client reading `emailId` does not have to
		// know that this object's id happens to be an Email id, so the
		// coincidence stays an implementation detail rather than a contract
		// clients build on.
		out["emailId"] = EncodeEmailID(r.EmailID)
	}
	if wants(props, "until") {
		out["until"] = r.Until.UTC().Format("2006-01-02T15:04:05Z")
	}
	if wants(props, "originMailboxName") {
		if r.OriginMailboxName == "" {
			// null rather than "": the empty string is the STORE's spelling of
			// "the inbox" (migration 0009), and a client should not have to
			// know that. null reads as "the default", which is what it means.
			out["originMailboxName"] = nil
		} else {
			out["originMailboxName"] = r.OriginMailboxName
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Snooze/set
// ---------------------------------------------------------------------------

// handleSnoozeSet implements /set (RFC 8620 §5.3) over the snooze set.
//
//	create  -> snooze. The creation object names an emailId and an `until`.
//	update  -> re-snooze to a new time. Only `until` is patchable; the emailId
//	           is the object's identity and §5.3 requires rejecting an update
//	           that names an immutable property.
//	destroy -> un-snooze: the message returns to its origin NOW.
//
// The state string advances on every successful operation, which is what
// makes a second tab's Snooze/get refresh.
func (d *Deps) handleSnoozeSet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseSet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	oldState, err := d.Triage.SnoozeState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the snooze state", err)
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, jmap.NewMethodError(jmap.CodeStateMismatch).
			WithDescription("the snooze state has changed since the given ifInState")
	}

	resp := &setResponse{AccountID: req.AccountID, OldState: oldState}
	created := jmap.CreationIDsFromContext(ctx)

	// Creates first, in a deterministic order, so a batch's results do not
	// depend on map iteration — the same ordering every /set here applies.
	createIDs := make([]string, 0, len(req.Create))
	for cid := range req.Create {
		createIDs = append(createIDs, cid)
	}
	sort.Strings(createIDs)

	for _, cid := range createIDs {
		rec, serr := d.applySnoozeCreate(ctx, caller, req.Create[cid])
		if serr != nil {
			if resp.NotCreated == nil {
				resp.NotCreated = map[string]setError{}
			}
			resp.NotCreated[cid] = *serr
			continue
		}
		if resp.Created == nil {
			resp.Created = map[string]any{}
		}
		wire := EncodeEmailID(rec.EmailID)
		resp.Created[cid] = map[string]any{
			"id":      wire,
			"emailId": wire,
			"until":   rec.Until.UTC().Format("2006-01-02T15:04:05Z"),
		}
		created.Record(cid, wire)
	}

	destroySet := make(map[string]bool, len(req.Destroy))
	for _, id := range req.Destroy {
		destroySet[id] = true
	}
	updateIDs := make([]string, 0, len(req.Update))
	for id := range req.Update {
		updateIDs = append(updateIDs, id)
	}
	sort.Strings(updateIDs)

	for _, wire := range updateIDs {
		if destroySet[wire] {
			setNotUpdated(resp, wire, setError{Type: setErrWillDestroy,
				Description: "the same id is also in destroy; the update was ignored"})
			continue
		}
		serr := d.applySnoozeUpdate(ctx, caller, wire, req.Update[wire])
		if serr != nil {
			setNotUpdated(resp, wire, *serr)
			continue
		}
		if resp.Updated == nil {
			resp.Updated = map[string]any{}
		}
		resp.Updated[wire] = nil
	}

	seen := make(map[string]bool, len(req.Destroy))
	for _, wire := range req.Destroy {
		if seen[wire] {
			continue
		}
		seen[wire] = true
		if serr := d.applySnoozeDestroy(ctx, caller, wire); serr != nil {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = map[string]setError{}
			}
			resp.NotDestroyed[wire] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, wire)
	}

	newState, err := d.Triage.SnoozeState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the snooze state", err)
	}
	resp.NewState = newState
	return resp, nil
}

// applySnoozeCreate validates one creation object and performs the snooze.
func (d *Deps) applySnoozeCreate(ctx context.Context, caller jmap.Caller, raw json.RawMessage) (SnoozeRecord, *setError) {
	var zero SnoozeRecord
	var obj struct {
		EmailID *string `json:"emailId"`
		Until   *string `json:"until"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return zero, &setError{Type: setErrInvalidProperties,
			Description: "a create must be a Snooze object with emailId and until"}
	}
	if obj.EmailID == nil {
		return zero, &setError{Type: setErrInvalidProperties, Properties: []string{"emailId"},
			Description: "emailId is required"}
	}
	if obj.Until == nil {
		return zero, &setError{Type: setErrInvalidProperties, Properties: []string{"until"},
			Description: "until is required: an RFC 3339 UTC date-time"}
	}
	until, serr := parseSnoozeUntil(*obj.Until)
	if serr != nil {
		return zero, serr
	}

	// A creation reference is resolved the same way EmailSubmission's emailId
	// is (§5.3's "#" prefix), so a client can snooze a message it created in
	// the same request. Rare, and free to support because the machinery is
	// already there.
	wire := *obj.EmailID
	if resolved, ok := jmap.CreationIDsFromContext(ctx).Resolve(wire); ok {
		wire = resolved
	}
	emailID, err := DecodeEmailID(wire)
	if err != nil {
		return zero, &setError{Type: setErrNotFound,
			Description: "emailId is not an Email id this server issued"}
	}

	rec, err := d.Triage.Snooze(ctx, caller.AccountID, emailID, until)
	switch {
	case err == nil:
		return rec, nil
	case errors.Is(err, ErrNotFound):
		return zero, &setError{Type: setErrNotFound,
			Description: "emailId names no message of this account"}
	case errors.Is(err, ErrSnoozeUnavailable):
		// §5.3's forbidden is the closest standard type: the operation is
		// refused for a reason the client cannot fix by changing a property.
		// The description carries the real cause, which is what a user-facing
		// message needs.
		return zero, &setError{Type: setErrForbidden, Description: err.Error()}
	default:
		return zero, &setError{Type: setErrServerFail, Description: "snoozing the message failed"}
	}
}

// applySnoozeUpdate re-snoozes to a new time. Only `until` is patchable.
func (d *Deps) applySnoozeUpdate(ctx context.Context, caller jmap.Caller, wire string, raw json.RawMessage) *setError {
	emailID, err := DecodeEmailID(wire)
	if err != nil {
		return &setError{Type: setErrNotFound}
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(raw, &patch); err != nil || patch == nil {
		return &setError{Type: setErrInvalidPatch,
			Description: "an update must be a PatchObject (RFC 8620 §5.3)"}
	}

	var until time.Time
	var bad []string
	for key, val := range patch {
		property, _, hasSub, ok := splitPatchPointer(key)
		if !ok || hasSub {
			return &setError{Type: setErrInvalidPatch,
				Description: fmt.Sprintf("%q is not a patchable path on a Snooze object", key)}
		}
		if property != "until" {
			// §5.3: naming an immutable or unknown property is the
			// invalidProperties condition. id, emailId and originMailboxName
			// are all identity or server-set.
			bad = append(bad, property)
			continue
		}
		var s string
		if err := json.Unmarshal(val, &s); err != nil {
			bad = append(bad, property)
			continue
		}
		t, serr := parseSnoozeUntil(s)
		if serr != nil {
			return serr
		}
		until = t
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return &setError{Type: setErrInvalidProperties, Properties: bad,
			Description: "only until may be updated on a Snooze; everything else is identity or server-set"}
	}
	if until.IsZero() {
		return &setError{Type: setErrInvalidProperties,
			Description: "the update changes nothing this server can change"}
	}

	// A re-snooze is a snooze: the store's upsert replaces the wake time, and
	// the message is already in Snoozed so the MOVE is the no-op ApplyMove
	// short-circuits on. Reusing the create path is what keeps "snooze again"
	// from becoming a second, subtly different operation.
	_, err = d.Triage.Snooze(ctx, caller.AccountID, emailID, until)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return &setError{Type: setErrNotFound}
	case errors.Is(err, ErrSnoozeUnavailable):
		return &setError{Type: setErrForbidden, Description: err.Error()}
	default:
		return &setError{Type: setErrServerFail, Description: "re-snoozing the message failed"}
	}
}

// applySnoozeDestroy un-snoozes: the message returns to its origin now.
func (d *Deps) applySnoozeDestroy(ctx context.Context, caller jmap.Caller, wire string) *setError {
	emailID, err := DecodeEmailID(wire)
	if err != nil {
		return &setError{Type: setErrNotFound}
	}
	err = d.Triage.Unsnooze(ctx, caller.AccountID, emailID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrNotSnoozed):
		return &setError{Type: setErrNotFound,
			Description: "the message is not snoozed"}
	default:
		return &setError{Type: setErrServerFail, Description: "un-snoozing the message failed"}
	}
}

// parseSnoozeUntil validates a wake time.
//
// The format is RFC 8620 §1.4's UTCDate ("YYYY-MM-DDTHH:MM:SSZ"), which is
// what every other date on this server's wire uses. Any INSTANT is accepted —
// the presets ("tomorrow morning", "next week") are the UI's concern, exactly
// as the canon's §5 says Gmail's own preset times are undocumented and
// therefore not a contract to copy.
//
// Two bounds, both about catching a client bug rather than restricting a user:
//
//   - the past is refused. A wake in the past would fire on the very next
//     poll, so the message would vanish and reappear — which looks like a bug
//     even when it is exactly what was asked for. Refusing says so.
//   - beyond maxSnoozeHorizon is refused, which catches the classic
//     milliseconds-sent-as-seconds mistake (a JS timestamp read as Unix
//     seconds lands about fifty thousand years out).
func parseSnoozeUntil(s string) (time.Time, *setError) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, &setError{Type: setErrInvalidProperties, Properties: []string{"until"},
			Description: `until must be a UTCDate such as "2026-09-01T08:00:00Z" (RFC 8620 §1.4)`}
	}
	t = t.UTC()
	now := time.Now().UTC()
	if !t.After(now) {
		return time.Time{}, &setError{Type: setErrInvalidProperties, Properties: []string{"until"},
			Description: "until must be in the future"}
	}
	if t.After(now.Add(maxSnoozeHorizon)) {
		return time.Time{}, &setError{Type: setErrInvalidProperties, Properties: []string{"until"},
			Description: "until is further away than this server schedules snoozes"}
	}
	return t, nil
}

// ---------------------------------------------------------------------------
// Mute/get
// ---------------------------------------------------------------------------

// handleMuteGet implements /get over the muted conversations.
//
// This is the whole `is:muted` surface (see the file header): a client fetches
// the set once, caches it, and badges the threads it recognizes. The set is
// small by nature — a user mutes conversations they want to forget, and there
// are not thousands of those.
func (d *Deps) handleMuteGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	if bad := unknownProperties(req.Properties, muteProperties); len(bad) > 0 {
		sort.Strings(bad)
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("unknown Mute properties: %s", strings.Join(bad, ", "))
	}

	state, err := d.Triage.MuteState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the mute state", err)
	}
	threadIDs, err := d.Triage.ListMutes(ctx, caller.AccountID, d.Limits.MaxObjectsInGet)
	if err != nil {
		return nil, serverFail("listing mutes", err)
	}

	resp := newGetResponse(req.AccountID, state)
	props, _ := propertySet(req.Properties)
	muted := make(map[string]bool, len(threadIDs))
	for _, id := range threadIDs {
		muted[EncodeThreadID(id)] = true
	}

	if req.IDs == nil {
		for _, id := range threadIDs {
			resp.List = append(resp.List, muteObject(EncodeThreadID(id), props))
		}
		return resp, nil
	}
	for _, wire := range *req.IDs {
		if muted[wire] {
			resp.List = append(resp.List, muteObject(wire, props))
			continue
		}
		resp.NotFound = append(resp.NotFound, wire)
	}
	sort.Strings(resp.NotFound)
	return resp, nil
}

func muteObject(threadWireID string, props map[string]bool) map[string]any {
	out := map[string]any{"id": threadWireID}
	if wants(props, "threadId") {
		out["threadId"] = threadWireID
	}
	return out
}

// ---------------------------------------------------------------------------
// Mute/set
// ---------------------------------------------------------------------------

// handleMuteSet implements /set over the muted conversations.
//
//	create  -> mute the named threadId.
//	update  -> refused: a Mute has no mutable property. Muting is a binary
//	           fact, so "change it" is create or destroy, and §5.3's
//	           invalidProperties is the answer for an update that names
//	           nothing changeable.
//	destroy -> unmute.
//
// Muting is idempotent by design (the store's ON CONFLICT DO NOTHING), so a
// create naming an already-muted thread succeeds rather than erroring — which
// is what a client retrying a request whose response it lost needs.
func (d *Deps) handleMuteSet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseSet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	oldState, err := d.Triage.MuteState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the mute state", err)
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, jmap.NewMethodError(jmap.CodeStateMismatch).
			WithDescription("the mute state has changed since the given ifInState")
	}

	resp := &setResponse{AccountID: req.AccountID, OldState: oldState}
	created := jmap.CreationIDsFromContext(ctx)

	createIDs := make([]string, 0, len(req.Create))
	for cid := range req.Create {
		createIDs = append(createIDs, cid)
	}
	sort.Strings(createIDs)

	for _, cid := range createIDs {
		wire, serr := d.applyMuteCreate(ctx, caller, req.Create[cid])
		if serr != nil {
			if resp.NotCreated == nil {
				resp.NotCreated = map[string]setError{}
			}
			resp.NotCreated[cid] = *serr
			continue
		}
		if resp.Created == nil {
			resp.Created = map[string]any{}
		}
		resp.Created[cid] = map[string]any{"id": wire, "threadId": wire}
		created.Record(cid, wire)
	}

	for wire := range req.Update {
		setNotUpdated(resp, wire, setError{Type: setErrInvalidProperties,
			Description: "a Mute has no mutable properties: create it to mute a conversation, destroy it to unmute"})
	}

	seen := make(map[string]bool, len(req.Destroy))
	for _, wire := range req.Destroy {
		if seen[wire] {
			continue
		}
		seen[wire] = true
		if serr := d.applyMuteDestroy(ctx, caller, wire); serr != nil {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = map[string]setError{}
			}
			resp.NotDestroyed[wire] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, wire)
	}

	newState, err := d.Triage.MuteState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the mute state", err)
	}
	resp.NewState = newState
	return resp, nil
}

func (d *Deps) applyMuteCreate(ctx context.Context, caller jmap.Caller, raw json.RawMessage) (string, *setError) {
	var obj struct {
		ThreadID *string `json:"threadId"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil || obj.ThreadID == nil {
		return "", &setError{Type: setErrInvalidProperties, Properties: []string{"threadId"},
			Description: "threadId is required"}
	}
	wire := *obj.ThreadID
	if resolved, ok := jmap.CreationIDsFromContext(ctx).Resolve(wire); ok {
		wire = resolved
	}
	threadID, err := DecodeThreadID(wire)
	if err != nil {
		return "", &setError{Type: setErrNotFound,
			Description: "threadId is not a Thread id this server issued"}
	}
	if serr := d.setMuted(ctx, caller.AccountID, threadID, true); serr != nil {
		return "", serr
	}
	return EncodeThreadID(threadID), nil
}

func (d *Deps) applyMuteDestroy(ctx context.Context, caller jmap.Caller, wire string) *setError {
	threadID, err := DecodeThreadID(wire)
	if err != nil {
		return &setError{Type: setErrNotFound}
	}
	return d.setMuted(ctx, caller.AccountID, threadID, false)
}

func (d *Deps) setMuted(ctx context.Context, accountID, threadID int64, muted bool) *setError {
	err := d.Triage.SetMuted(ctx, accountID, threadID, muted)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return &setError{Type: setErrNotFound,
			Description: "threadId names no conversation of this account"}
	default:
		return &setError{Type: setErrServerFail, Description: "changing the mute failed"}
	}
}
