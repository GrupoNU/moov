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
)

// VacationResponse (RFC 8621 §8) — the singleton, materialized as the
// vacation section of the managed Sieve script (Dovecot is the source of
// truth; there is no vacation table).
//
// Conformance decisions, stated once:
//
//   - The object id is "singleton" (§8: "There MUST be exactly one
//     VacationResponse object per account. It has the id 'singleton'.").
//   - /get and /set are implemented; §8 defines no /changes for the type and
//     none is registered.
//   - restrictToContacts does NOT exist on this surface. RFC 8621 §8 defines
//     exactly seven properties (id, isEnabled, fromDate, toDate, subject,
//     textBody, htmlBody) — Gmail's contacts-only option is an extension,
//     and Moov has no contacts subsystem to honor it with. The property is
//     therefore neither advertised nor accepted: a client sending it gets
//     §5.3's invalidProperties, never store-and-ignore. Recorded decision.
//   - Timezone: fromDate/toDate are §8 UTCDates, honored to the second in
//     UTC by the generated Sieve guard. Gmail's "starts 12:00 AM, ends 11:59
//     PM" day boundaries are produced by the CLIENT sending day-aligned
//     instants in the user's zone — this server keeps no per-account
//     timezone and does not guess one. Recorded decision.
//   - htmlBody is sanitized server-side with the SAME sanitizer the identity
//     signatures use (sanitizeHTMLSignature): this is content the server
//     transmits on the user's behalf, so raw client HTML never reaches the
//     script.
//   - The anti-annoyance spec (canon §2.8) lives in the generated Sieve:
//     :days 4, edit-resetting :handle, spam/list guards. internal/sieve's
//     generator owns it and its tests pin it.

// vacationID is the wire id (§8).
const vacationID = "singleton"

// vacationProperties is the §8 property set.
var vacationProperties = map[string]bool{
	"id":        true,
	"isEnabled": true,
	"fromDate":  true,
	"toDate":    true,
	"subject":   true,
	"textBody":  true,
	"htmlBody":  true,
}

// RegisterVacationMethods mounts VacationResponse/get and /set under
// CapVacation.
func RegisterVacationMethods(registry *jmap.Registry, deps *Deps) {
	if registry == nil || deps == nil {
		panic("mail: RegisterVacationMethods requires a registry and deps")
	}
	if deps.Vacation == nil {
		panic("mail: RegisterVacationMethods requires Vacation")
	}
	registry.Register("VacationResponse/get", jmap.CapVacation, deps.handleVacationGet)
	registry.Register("VacationResponse/set", jmap.CapVacation, deps.handleVacationSet)
}

// handleVacationGet implements the §5.1 /get over the singleton — the same
// shape Prefs/get takes, citation for citation.
func (d *Deps) handleVacationGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	if bad := unknownProperties(req.Properties, vacationProperties); len(bad) > 0 {
		sort.Strings(bad)
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("unknown VacationResponse properties: %s", strings.Join(bad, ", "))
	}

	v, err := d.Vacation.GetVacation(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the vacation response", err)
	}
	state, err := d.Vacation.VacationState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the vacation state", err)
	}

	resp := newGetResponse(req.AccountID, state)
	if req.IDs == nil {
		resp.List = append(resp.List, vacationObject(v, req.Properties))
	} else {
		for _, id := range *req.IDs {
			if id == vacationID {
				resp.List = append(resp.List, vacationObject(v, req.Properties))
				continue
			}
			resp.NotFound = append(resp.NotFound, id)
		}
	}
	sort.Strings(resp.NotFound)
	return resp, nil
}

// vacationObject renders the §8 object.
func vacationObject(v VacationValue, properties *[]string) map[string]any {
	full := map[string]any{
		"id":        vacationID,
		"isEnabled": v.IsEnabled,
		"fromDate":  utcDateOrNull(v.FromDate),
		"toDate":    utcDateOrNull(v.ToDate),
		"subject":   stringOrNull(v.Subject),
		"textBody":  stringOrNull(v.TextBody),
		"htmlBody":  stringOrNull(v.HTMLBody),
	}
	if properties == nil {
		return full
	}
	out := map[string]any{"id": full["id"]}
	for _, name := range *properties {
		if val, ok := full[name]; ok {
			out[name] = val
		}
	}
	return out
}

func utcDateOrNull(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

func stringOrNull(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// handleVacationSet implements the §5.3 /set over the singleton: create and
// destroy are forbidden (the object always exists and cannot not exist),
// update patches the whole object, validates, sanitizes and stores.
func (d *Deps) handleVacationSet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseSet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}

	oldState, err := d.Vacation.VacationState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the vacation state", err)
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, jmap.NewMethodError(jmap.CodeStateMismatch).
			WithDescription("the vacation state has changed since the given ifInState")
	}

	resp := &setResponse{AccountID: req.AccountID, OldState: oldState}

	for creationID := range req.Create {
		if resp.NotCreated == nil {
			resp.NotCreated = map[string]setError{}
		}
		resp.NotCreated[creationID] = setError{Type: setErrForbidden,
			Description: `VacationResponse is a singleton and always exists (RFC 8621 §8); update "` +
				vacationID + `" instead`}
	}
	for _, id := range req.Destroy {
		if resp.NotDestroyed == nil {
			resp.NotDestroyed = map[string]setError{}
		}
		if id != vacationID {
			resp.NotDestroyed[id] = setError{Type: setErrNotFound,
				Description: `the only VacationResponse object is "` + vacationID + `"`}
			continue
		}
		resp.NotDestroyed[id] = setError{Type: setErrForbidden,
			Description: "VacationResponse is a singleton and cannot be destroyed; " +
				"set isEnabled to false to stop responding"}
	}

	if len(req.Update) > 0 {
		current, err := d.Vacation.GetVacation(ctx, caller.AccountID)
		if err != nil {
			return nil, serverFail("reading the vacation response", err)
		}
		for id, patchRaw := range req.Update {
			if id != vacationID {
				setNotUpdated(resp, id, setError{Type: setErrNotFound,
					Description: `the only VacationResponse object is "` + vacationID + `"`})
				continue
			}
			next, serr := applyVacationPatch(current, patchRaw)
			if serr != nil {
				setNotUpdated(resp, id, *serr)
				continue
			}
			if err := d.Vacation.SetVacation(ctx, caller.AccountID, *next); err != nil {
				var invalid *SieveInvalidError
				if errors.As(err, &invalid) {
					setNotUpdated(resp, id, setError{Type: setErrInvalidProperties,
						Description: invalid.Description})
					continue
				}
				setNotUpdated(resp, id, setError{Type: setErrServerFail,
					Description: "storing the vacation response failed"})
				continue
			}
			if resp.Updated == nil {
				resp.Updated = map[string]any{}
			}
			// The htmlBody the server stored may differ from the client's
			// (sanitization). §5.3: updated carries "any properties that
			// changed on the server as a side effect".
			if next.HTMLBody != nil {
				resp.Updated[id] = map[string]any{"htmlBody": *next.HTMLBody}
			} else {
				resp.Updated[id] = nil
			}
			current = *next
		}
	}

	newState, err := d.Vacation.VacationState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the vacation state", err)
	}
	resp.NewState = newState
	return resp, nil
}

// applyVacationPatch validates a PatchObject against the current object,
// collecting every problem (§5.3's list-them-all contract).
func applyVacationPatch(current VacationValue, raw json.RawMessage) (*VacationValue, *setError) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, &setError{Type: setErrInvalidPatch,
			Description: "an update must be a PatchObject (RFC 8620 §5.3)"}
	}

	next := current
	var bad []string
	reasons := map[string]string{}
	fail := func(property, why string) {
		if _, seen := reasons[property]; !seen {
			bad = append(bad, property)
		}
		reasons[property] = why
	}

	for key, val := range fields {
		property, _, hasSub, ok := splitPatchPointer(key)
		if !ok || hasSub {
			return nil, &setError{Type: setErrInvalidPatch,
				Description: fmt.Sprintf("%q is not a patchable path on a VacationResponse", key)}
		}
		switch property {
		case "isEnabled":
			var b bool
			if err := json.Unmarshal(val, &b); err != nil {
				fail(property, "isEnabled must be true or false")
				continue
			}
			next.IsEnabled = b
		case "fromDate":
			patchUTCDate(val, property, &next.FromDate, fail)
		case "toDate":
			patchUTCDate(val, property, &next.ToDate, fail)
		case "subject":
			patchNullableString(val, property, &next.Subject, fail)
			if next.Subject != nil && strings.ContainsAny(*next.Subject, "\r\n") {
				fail(property, "subject must be a single line")
			}
		case "textBody":
			patchNullableString(val, property, &next.TextBody, fail)
		case "htmlBody":
			patchNullableString(val, property, &next.HTMLBody, fail)
			if next.HTMLBody != nil {
				// Sanitized on the way IN, with the signature sanitizer:
				// this HTML is transmitted in outgoing auto-replies, so the
				// database and the script never hold raw client markup.
				clean := sanitizeHTMLSignature(*next.HTMLBody)
				next.HTMLBody = &clean
			}
		case "id":
			fail(property, `id is server-set: the VacationResponse object is "`+vacationID+`"`)
		case "restrictToContacts":
			// The recorded decision (package comment): not an RFC 8621 §8
			// property, and Moov has no contacts subsystem — refused, never
			// silently absorbed.
			fail(property, "restrictToContacts is not part of RFC 8621 §8 and this server has no "+
				"contacts subsystem to honor it; the property is not supported")
		default:
			fail(property, fmt.Sprintf("%q is not a property of a VacationResponse (RFC 8621 §8)", property))
		}
	}

	// §8: subject/textBody/htmlBody "MUST NOT be all null if isEnabled is
	// true" is the canon's subject-or-body rule; enforced whenever the
	// result is enabled.
	if next.IsEnabled && emptyOpt(next.Subject) && emptyOpt(next.TextBody) && emptyOpt(next.HTMLBody) {
		fail("isEnabled", "an enabled vacation response needs a subject or a body")
	}
	if next.FromDate != nil && next.ToDate != nil && next.ToDate.Before(*next.FromDate) {
		fail("toDate", "toDate must not be before fromDate")
	}

	if len(bad) > 0 {
		sort.Strings(bad)
		details := make([]string, 0, len(bad))
		for _, property := range bad {
			details = append(details, reasons[property])
		}
		return nil, &setError{Type: setErrInvalidProperties, Properties: bad,
			Description: strings.Join(details, "; ")}
	}
	return &next, nil
}

// patchUTCDate reads a §8 UTCDate|null property.
func patchUTCDate(raw json.RawMessage, property string, dst **time.Time, fail func(string, string)) {
	if strings.TrimSpace(string(raw)) == "null" {
		*dst = nil
		return
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		fail(property, property+" must be a UTCDate string or null")
		return
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		fail(property, property+` must be a UTCDate such as "2026-09-01T00:00:00Z"`)
		return
	}
	u := t.UTC().Truncate(time.Second)
	*dst = &u
}

// patchNullableString reads a String|null property, mapping the empty string
// onto null (a cleared text field means "unset", exactly as Prefs.language
// documents).
func patchNullableString(raw json.RawMessage, property string, dst **string, fail func(string, string)) {
	if strings.TrimSpace(string(raw)) == "null" {
		*dst = nil
		return
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		fail(property, property+" must be a string or null")
		return
	}
	if s == "" {
		*dst = nil
		return
	}
	*dst = &s
}

func emptyOpt(s *string) bool { return s == nil || strings.TrimSpace(*s) == "" }
