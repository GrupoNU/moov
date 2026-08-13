package mail

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GrupoNU/moov/internal/jmap"
)

// The /set method skeleton (RFC 8620 §5.3), shared by Email/set today and by
// Mailbox/set when W2 lands — the same role get.go plays for the /get family.

// setRequest is the standard /set arguments object (§5.3).
//
// IfInState is *string because §5.3 gives null a distinct meaning: "If null,
// any changes will be applied". Create/Update hold raw messages because each
// entry is interpreted per type (an Email object, a PatchObject).
type setRequest struct {
	AccountID string                     `json:"accountId"`
	IfInState *string                    `json:"ifInState"`
	Create    map[string]json.RawMessage `json:"create"`
	Update    map[string]json.RawMessage `json:"update"`
	Destroy   []string                   `json:"destroy"`
}

// objectCount is the combined total maxObjectsInSet is measured against
// (RFC 8620 §2: "the maximum number of objects the client may send to
// create, update, or destroy in a single /set type method call").
func (r *setRequest) objectCount() int {
	return len(r.Create) + len(r.Update) + len(r.Destroy)
}

// setError is the §5.3 SetError object.
//
// Properties lists the offending properties for invalidProperties errors —
// §5.3: "The SetError object SHOULD also have a property called 'properties'
// of type 'String[]' that lists ALL the properties that were invalid."
type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

// The SetError types this server emits.
//
// The first five are the §5.3 standard vocabulary. The last three reuse
// method-level codes from the single error registry (jmap/errors.go) as
// SetError types — a documented judgment call each:
//
//   - setErrStateMismatch: §5.3 defines stateMismatch only as the
//     whole-method answer to ifInState, and registers no SetError for a
//     per-record optimistic-concurrency refusal. Moov applies writes to
//     Dovecot with UNCHANGEDSINCE (W-A1), so a single message CAN be refused
//     because it changed concurrently while the rest of the batch succeeds —
//     per-id granularity is a W1 acceptance criterion. Reusing the reserved
//     stateMismatch name says exactly what happened in vocabulary the RFC
//     already defines, and a client that does not recognize it in this
//     position treats it as a generic failure and re-reads, which is also
//     the correct recovery.
//   - setErrServerUnavailable / setErrServerFail: §3.6.2 method-level
//     conditions scoped to one record; jmap-perl (the reference mapping
//     plane) emits serverFail in SetError position the same way.
const (
	setErrNotFound          = "notFound"
	setErrInvalidProperties = "invalidProperties"
	setErrInvalidPatch      = "invalidPatch"
	setErrWillDestroy       = "willDestroy"
	setErrForbidden         = "forbidden"

	setErrStateMismatch     = string(jmap.CodeStateMismatch)
	setErrServerFail        = string(jmap.CodeServerFail)
	setErrServerUnavailable = string(jmap.CodeServerUnavailable)
)

// setResponse is the standard /set response object (§5.3).
//
// The maps and slices are nil when empty ON PURPOSE, unlike the /get
// response: §5.3 types every one of them as "...|null" and prescribes null
// when there is nothing to report ("or null if no objects were successfully
// updated", etc.).
type setResponse struct {
	AccountID    string              `json:"accountId"`
	OldState     string              `json:"oldState"`
	NewState     string              `json:"newState"`
	Created      map[string]any      `json:"created"`
	Updated      map[string]any      `json:"updated"`
	Destroyed    []string            `json:"destroyed"`
	NotCreated   map[string]setError `json:"notCreated"`
	NotUpdated   map[string]setError `json:"notUpdated"`
	NotDestroyed map[string]setError `json:"notDestroyed"`
}

// parseSet decodes and validates the common /set arguments: the caller, the
// accountId, and the maxObjectsInSet ceiling — the same three checks
// parseGet performs for /get, in the same order, before any work happens.
func parseSet(ctx context.Context, args json.RawMessage, limits jmap.Limits) (*setRequest, jmap.Caller, *jmap.MethodError) {
	caller, ok := jmap.CallerFromContext(ctx)
	if !ok {
		return nil, caller, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}

	var req setRequest
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
	if merr := limits.CheckObjectsInSet(req.objectCount()); merr != nil {
		return nil, caller, merr
	}
	return &req, caller, nil
}

// splitPatchPointer splits a PatchObject key into its top-level property and
// an optional one-level sub-key, both with RFC 6901 escaping resolved —
// RFC 8620 §5.3: "The keys are a path in JSON Pointer format [RFC6901], with
// an implicit leading '/'".
//
// ok is false for a pointer deeper than one sub-level ("keywords/x/y"): no
// property Email/set can patch has nested structure below that, so a deeper
// path can never be valid and is the §5.3 invalidPatch condition.
func splitPatchPointer(key string) (property, sub string, hasSub, ok bool) {
	tokens := strings.Split(key, "/")
	switch len(tokens) {
	case 1:
		return unescapePointerToken(tokens[0]), "", false, true
	case 2:
		return unescapePointerToken(tokens[0]), unescapePointerToken(tokens[1]), true, true
	default:
		return "", "", false, false
	}
}

// unescapePointerToken resolves the two RFC 6901 escapes, in the order the
// RFC mandates (§4: "the string '~1' ... THEN ... the string '~0'", so that
// "~01" round-trips to "~1" and not to "/").
func unescapePointerToken(s string) string {
	if !strings.Contains(s, "~") {
		return s
	}
	s = strings.ReplaceAll(s, "~1", "/")
	return strings.ReplaceAll(s, "~0", "~")
}

// boolPatchValue interprets a PatchObject value for a set-shaped property
// (keywords, mailboxIds), whose members are typed "Boolean, MUST be true"
// (RFC 8621 §4.1.1).
//
//   - JSON true       -> add the member
//   - JSON null       -> remove it (§5.3: "If null, set to the default value
//     if specified for the property, otherwise remove")
//   - anything else   -> invalid: false is not a legal member value, and a
//     non-boolean is not even the right type
func boolPatchValue(raw json.RawMessage) (add, remove, valid bool) {
	switch strings.TrimSpace(string(raw)) {
	case "true":
		return true, false, true
	case "null":
		return false, true, true
	default:
		return false, false, false
	}
}
