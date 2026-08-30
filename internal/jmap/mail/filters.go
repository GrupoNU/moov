package mail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/GrupoNU/moov/internal/jmap"
)

// The vendor filter surface (jmap.CapFilters, GC-4): the server-side rule
// model over the managed Sieve script.
//
//	FilterRule/get|set          the rule list (types filter/blocked/neverSpam
//	                            — "Bloqueados" is the blocked subset)
//	Forwarding/get|set          the forward-all singleton ("Reenvío")
//	ForwardingAddress/get|set   the verified destinations and their
//	                            verification flow
//
// No /changes methods exist on this surface, by contract rather than
// omission: the capability is Moov's own, its one client is Moov's UI, and
// the SSE StateChange plus a cheap /get is the refresh path. The capability
// object says so (jmaphttp filtersCapability).
//
// FilterRule/get carries one extra top-level response property,
// `scriptActive`: whether the Moov-managed script currently holds the
// account's active slot. When another script is active (hand-written,
// SOGo's, Bulwark's) the rules exist but do not filter mail; hiding that
// would be pretending, and the vendor contract is the place where an extra
// property is ours to define.

// filterRuleProperties is the FilterRule property set.
var filterRuleProperties = map[string]bool{
	"id": true, "name": true, "type": true, "enabled": true,
	"from": true, "to": true, "subject": true,
	"sizeOver": true, "sizeUnder": true, "hasAttachment": true,
	"moveTo": true, "labels": true, "markRead": true, "star": true,
	"forward": true, "delete": true, "stop": true,
}

// forwardingID is the Forwarding singleton's wire id — the RFC 8621 §8
// singleton shape reused, exactly like Prefs.
const forwardingID = "singleton"

// RegisterFilterMethods mounts the vendor filter surface under CapFilters.
func RegisterFilterMethods(registry *jmap.Registry, deps *Deps) {
	if registry == nil || deps == nil {
		panic("mail: RegisterFilterMethods requires a registry and deps")
	}
	if deps.Filters == nil || deps.Forwarding == nil {
		panic("mail: RegisterFilterMethods requires Filters and Forwarding")
	}
	registry.Register("FilterRule/get", jmap.CapFilters, deps.handleFilterRuleGet)
	registry.Register("FilterRule/set", jmap.CapFilters, deps.handleFilterRuleSet)
	registry.Register("Forwarding/get", jmap.CapFilters, deps.handleForwardingGet)
	registry.Register("Forwarding/set", jmap.CapFilters, deps.handleForwardingSet)
	registry.Register("ForwardingAddress/get", jmap.CapFilters, deps.handleForwardingAddressGet)
	registry.Register("ForwardingAddress/set", jmap.CapFilters, deps.handleForwardingAddressSet)
}

// ---------------------------------------------------------------------------
// FilterRule
// ---------------------------------------------------------------------------

// filterGetResponse is a getResponse plus the vendor extra.
type filterGetResponse struct {
	getResponse
	ScriptActive bool `json:"scriptActive"`
}

func (d *Deps) handleFilterRuleGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	if bad := unknownProperties(req.Properties, filterRuleProperties); len(bad) > 0 {
		sort.Strings(bad)
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("unknown FilterRule properties: %s", strings.Join(bad, ", "))
	}

	cfg, err := d.Filters.GetFilters(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the filter rules", err)
	}
	state, err := d.Filters.FiltersState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the filter state", err)
	}

	resp := filterGetResponse{getResponse: *newGetResponse(req.AccountID, state), ScriptActive: cfg.ScriptActive}
	if req.IDs == nil {
		for _, r := range cfg.Rules {
			resp.List = append(resp.List, filterRuleObject(r, req.Properties))
		}
		return resp, nil
	}
	byID := make(map[string]FilterRuleValue, len(cfg.Rules))
	for _, r := range cfg.Rules {
		byID[r.ID] = r
	}
	for _, id := range *req.IDs {
		if r, ok := byID[id]; ok {
			resp.List = append(resp.List, filterRuleObject(r, req.Properties))
			continue
		}
		resp.NotFound = append(resp.NotFound, id)
	}
	sort.Strings(resp.NotFound)
	return resp, nil
}

func filterRuleObject(r FilterRuleValue, properties *[]string) map[string]any {
	full := map[string]any{
		"id":            r.ID,
		"name":          r.Name,
		"type":          r.Type,
		"enabled":       r.Enabled,
		"from":          emptyList(r.From),
		"to":            emptyList(r.To),
		"subject":       emptyList(r.Subject),
		"sizeOver":      r.SizeOver,
		"sizeUnder":     r.SizeUnder,
		"hasAttachment": nullableBool(r.HasAttachment),
		"moveTo":        r.MoveTo,
		"labels":        emptyList(r.Labels),
		"markRead":      r.MarkRead,
		"star":          r.Star,
		"forward":       r.Forward,
		"delete":        r.Delete,
		"stop":          r.Stop,
	}
	if properties == nil {
		return full
	}
	out := map[string]any{"id": full["id"]}
	for _, name := range *properties {
		if v, ok := full[name]; ok {
			out[name] = v
		}
	}
	return out
}

func emptyList(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nullableBool(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

// handleFilterRuleSet applies create/update/destroy over the CURRENT rule
// list and stores the result in ONE push — the script is regenerated once
// per call, not once per rule.
func (d *Deps) handleFilterRuleSet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseSet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	oldState, err := d.Filters.FiltersState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the filter state", err)
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, jmap.NewMethodError(jmap.CodeStateMismatch).
			WithDescription("the filter state has changed since the given ifInState")
	}

	cfg, err := d.Filters.GetFilters(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the filter rules", err)
	}
	rules := cfg.Rules
	resp := &setResponse{AccountID: req.AccountID, OldState: oldState}
	dirty := false

	createIDs := make([]string, 0, len(req.Create))
	for id := range req.Create {
		createIDs = append(createIDs, id)
	}
	sort.Strings(createIDs)
	for _, creationID := range createIDs {
		rule, serr := decodeFilterRule(req.Create[creationID], nil)
		if serr != nil {
			if resp.NotCreated == nil {
				resp.NotCreated = map[string]setError{}
			}
			resp.NotCreated[creationID] = *serr
			continue
		}
		id, err := newFilterRuleID()
		if err != nil {
			return nil, serverFail("minting a rule id", err)
		}
		rule.ID = id
		rules = append(rules, *rule)
		dirty = true
		if resp.Created == nil {
			resp.Created = map[string]any{}
		}
		resp.Created[creationID] = map[string]any{"id": id}
	}

	for id, patchRaw := range req.Update {
		idx := ruleIndex(rules, id)
		if idx < 0 {
			setNotUpdated(resp, id, setError{Type: setErrNotFound, Description: "no such rule"})
			continue
		}
		current := rules[idx]
		rule, serr := decodeFilterRule(patchRaw, &current)
		if serr != nil {
			setNotUpdated(resp, id, *serr)
			continue
		}
		rule.ID = id
		rules[idx] = *rule
		dirty = true
		if resp.Updated == nil {
			resp.Updated = map[string]any{}
		}
		resp.Updated[id] = nil
	}

	for _, id := range req.Destroy {
		idx := ruleIndex(rules, id)
		if idx < 0 {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = map[string]setError{}
			}
			resp.NotDestroyed[id] = setError{Type: setErrNotFound, Description: "no such rule"}
			continue
		}
		rules = append(rules[:idx], rules[idx+1:]...)
		dirty = true
		resp.Destroyed = append(resp.Destroyed, id)
	}

	if dirty {
		if err := d.Filters.PutFilters(ctx, caller.AccountID, rules, cfg.ForwardAll); err != nil {
			var invalid *SieveInvalidError
			if errors.As(err, &invalid) {
				// The push validates the WHOLE model; a refusal here voids
				// the batch. §5.3 stateMismatch-like partial application
				// would leave the script and the response disagreeing.
				return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
					WithDescription("the resulting rule set is invalid: %s", invalid.Description)
			}
			return nil, serverFail("storing the filter rules", err)
		}
	}

	newState, err := d.Filters.FiltersState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the filter state", err)
	}
	resp.NewState = newState
	return resp, nil
}

// decodeFilterRule reads a create object or applies a patch over base.
func decodeFilterRule(raw json.RawMessage, base *FilterRuleValue) (*FilterRuleValue, *setError) {
	var in struct {
		Name          *string  `json:"name"`
		Type          *string  `json:"type"`
		Enabled       *bool    `json:"enabled"`
		From          []string `json:"from"`
		To            []string `json:"to"`
		Subject       []string `json:"subject"`
		SizeOver      *int64   `json:"sizeOver"`
		SizeUnder     *int64   `json:"sizeUnder"`
		HasAttachment *bool    `json:"hasAttachment"`
		MoveTo        *string  `json:"moveTo"`
		Labels        []string `json:"labels"`
		MarkRead      *bool    `json:"markRead"`
		Star          *bool    `json:"star"`
		Forward       *string  `json:"forward"`
		Delete        *bool    `json:"delete"`
		Stop          *bool    `json:"stop"`
		ID            *string  `json:"id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &setError{Type: setErrInvalidProperties,
			Description: "a FilterRule must be an object"}
	}
	if in.ID != nil {
		return nil, &setError{Type: setErrInvalidProperties, Properties: []string{"id"},
			Description: "id is server-set"}
	}

	out := FilterRuleValue{Type: "filter", Enabled: true}
	if base != nil {
		out = *base
	}
	if in.Name != nil {
		out.Name = *in.Name
	}
	if in.Type != nil {
		out.Type = *in.Type
	}
	if in.Enabled != nil {
		out.Enabled = *in.Enabled
	}
	if in.From != nil {
		out.From = in.From
	}
	if in.To != nil {
		out.To = in.To
	}
	if in.Subject != nil {
		out.Subject = in.Subject
	}
	if in.SizeOver != nil {
		out.SizeOver = *in.SizeOver
	}
	if in.SizeUnder != nil {
		out.SizeUnder = *in.SizeUnder
	}
	if in.HasAttachment != nil {
		out.HasAttachment = in.HasAttachment
	}
	if in.MoveTo != nil {
		out.MoveTo = *in.MoveTo
	}
	if in.Labels != nil {
		out.Labels = in.Labels
	}
	if in.MarkRead != nil {
		out.MarkRead = *in.MarkRead
	}
	if in.Star != nil {
		out.Star = *in.Star
	}
	if in.Forward != nil {
		out.Forward = *in.Forward
	}
	if in.Delete != nil {
		out.Delete = *in.Delete
	}
	if in.Stop != nil {
		out.Stop = *in.Stop
	}
	// Detailed validation (criteria present, verified forward, extension
	// availability) happens in the model on push, with every problem named.
	return &out, nil
}

func ruleIndex(rules []FilterRuleValue, id string) int {
	for i, r := range rules {
		if r.ID == id {
			return i
		}
	}
	return -1
}

// newFilterRuleID mints a stable opaque rule id ("r" + 12 hex chars).
func newFilterRuleID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "r" + hex.EncodeToString(b[:]), nil
}

// ---------------------------------------------------------------------------
// Forwarding (the forward-all singleton)
// ---------------------------------------------------------------------------

var forwardingProperties = map[string]bool{
	"id": true, "enabled": true, "address": true, "disposition": true,
}

func (d *Deps) handleForwardingGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	if bad := unknownProperties(req.Properties, forwardingProperties); len(bad) > 0 {
		sort.Strings(bad)
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("unknown Forwarding properties: %s", strings.Join(bad, ", "))
	}
	cfg, err := d.Filters.GetFilters(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the forwarding configuration", err)
	}
	state, err := d.Filters.FiltersState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the filter state", err)
	}
	obj := map[string]any{
		"id":          forwardingID,
		"enabled":     cfg.ForwardAll.Enabled,
		"address":     nullableString(cfg.ForwardAll.Address),
		"disposition": dispositionOrDefault(cfg.ForwardAll.Disposition),
	}
	resp := newGetResponse(req.AccountID, state)
	if req.IDs == nil {
		resp.List = append(resp.List, obj)
	} else {
		for _, id := range *req.IDs {
			if id == forwardingID {
				resp.List = append(resp.List, obj)
				continue
			}
			resp.NotFound = append(resp.NotFound, id)
		}
	}
	sort.Strings(resp.NotFound)
	return resp, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func dispositionOrDefault(s string) string {
	if s == "" {
		return "keep"
	}
	return s
}

func (d *Deps) handleForwardingSet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseSet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	oldState, err := d.Filters.FiltersState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the filter state", err)
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, jmap.NewMethodError(jmap.CodeStateMismatch).
			WithDescription("the filter state has changed since the given ifInState")
	}

	resp := &setResponse{AccountID: req.AccountID, OldState: oldState}
	for creationID := range req.Create {
		if resp.NotCreated == nil {
			resp.NotCreated = map[string]setError{}
		}
		resp.NotCreated[creationID] = setError{Type: setErrForbidden,
			Description: `Forwarding is a singleton; update "` + forwardingID + `"`}
	}
	for _, id := range req.Destroy {
		if resp.NotDestroyed == nil {
			resp.NotDestroyed = map[string]setError{}
		}
		resp.NotDestroyed[id] = setError{Type: setErrForbidden,
			Description: "Forwarding is a singleton; set enabled to false instead"}
	}

	if len(req.Update) > 0 {
		cfg, err := d.Filters.GetFilters(ctx, caller.AccountID)
		if err != nil {
			return nil, serverFail("reading the forwarding configuration", err)
		}
		for id, patchRaw := range req.Update {
			if id != forwardingID {
				setNotUpdated(resp, id, setError{Type: setErrNotFound,
					Description: `the only Forwarding object is "` + forwardingID + `"`})
				continue
			}
			next, serr := applyForwardingPatch(cfg.ForwardAll, patchRaw)
			if serr != nil {
				setNotUpdated(resp, id, *serr)
				continue
			}
			if err := d.Filters.PutFilters(ctx, caller.AccountID, cfg.Rules, *next); err != nil {
				var invalid *SieveInvalidError
				if errors.As(err, &invalid) {
					setNotUpdated(resp, id, setError{Type: setErrInvalidProperties,
						Description: invalid.Description})
					continue
				}
				setNotUpdated(resp, id, setError{Type: setErrServerFail,
					Description: "storing the forwarding configuration failed"})
				continue
			}
			if resp.Updated == nil {
				resp.Updated = map[string]any{}
			}
			resp.Updated[id] = nil
			cfg.ForwardAll = *next
		}
	}

	newState, err := d.Filters.FiltersState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the filter state", err)
	}
	resp.NewState = newState
	return resp, nil
}

func applyForwardingPatch(current ForwardAllValue, raw json.RawMessage) (*ForwardAllValue, *setError) {
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
				Description: fmt.Sprintf("%q is not a patchable path on a Forwarding object", key)}
		}
		switch property {
		case "enabled":
			var b bool
			if err := json.Unmarshal(val, &b); err != nil {
				fail(property, "enabled must be true or false")
				continue
			}
			next.Enabled = b
		case "address":
			if strings.TrimSpace(string(val)) == "null" {
				next.Address = ""
				continue
			}
			var s string
			if err := json.Unmarshal(val, &s); err != nil {
				fail(property, "address must be a string or null")
				continue
			}
			next.Address = strings.ToLower(strings.TrimSpace(s))
		case "disposition":
			var s string
			if err := json.Unmarshal(val, &s); err != nil || (s != "keep" && s != "archive") {
				fail(property, `disposition must be "keep" or "archive"`)
				continue
			}
			next.Disposition = s
		case "id":
			fail(property, "id is server-set")
		default:
			fail(property, fmt.Sprintf("%q is not a property of a Forwarding object", property))
		}
	}
	if next.Enabled && next.Address == "" {
		fail("address", "enabled forwarding needs a destination address")
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		details := make([]string, 0, len(bad))
		for _, p := range bad {
			details = append(details, reasons[p])
		}
		return nil, &setError{Type: setErrInvalidProperties, Properties: bad,
			Description: strings.Join(details, "; ")}
	}
	return &next, nil
}

// ---------------------------------------------------------------------------
// ForwardingAddress
// ---------------------------------------------------------------------------

var forwardingAddressProperties = map[string]bool{
	"id": true, "email": true, "state": true, "verifiedAt": true,
}

func (d *Deps) handleForwardingAddressGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	if bad := unknownProperties(req.Properties, forwardingAddressProperties); len(bad) > 0 {
		sort.Strings(bad)
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("unknown ForwardingAddress properties: %s", strings.Join(bad, ", "))
	}
	rows, err := d.Forwarding.ListForwardingAddresses(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("listing forwarding addresses", err)
	}
	state, err := d.Forwarding.ForwardingState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the forwarding state", err)
	}
	resp := newGetResponse(req.AccountID, state)
	if req.IDs == nil {
		for _, r := range rows {
			resp.List = append(resp.List, forwardingAddressObject(r, req.Properties))
		}
		return resp, nil
	}
	byID := make(map[int64]ForwardingAddressValue, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	for _, raw := range *req.IDs {
		id, err := DecodeForwardingID(raw)
		if err != nil {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		if r, ok := byID[id]; ok {
			resp.List = append(resp.List, forwardingAddressObject(r, req.Properties))
			continue
		}
		resp.NotFound = append(resp.NotFound, raw)
	}
	sort.Strings(resp.NotFound)
	return resp, nil
}

func forwardingAddressObject(r ForwardingAddressValue, properties *[]string) map[string]any {
	var verifiedAt any
	if r.VerifiedAt != nil {
		verifiedAt = r.VerifiedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	full := map[string]any{
		"id":         EncodeForwardingID(r.ID),
		"email":      r.Email,
		"state":      r.State,
		"verifiedAt": verifiedAt,
	}
	if properties == nil {
		return full
	}
	out := map[string]any{"id": full["id"]}
	for _, name := range *properties {
		if v, ok := full[name]; ok {
			out[name] = v
		}
	}
	return out
}

// handleForwardingAddressSet: create sends the verification mail (pending),
// destroy removes (refusing in-use), update is forbidden — destroy and
// re-create is the resend path, and there is nothing else to edit on a
// verification fact.
func (d *Deps) handleForwardingAddressSet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseSet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	oldState, err := d.Forwarding.ForwardingState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the forwarding state", err)
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, jmap.NewMethodError(jmap.CodeStateMismatch).
			WithDescription("the forwarding state has changed since the given ifInState")
	}

	resp := &setResponse{AccountID: req.AccountID, OldState: oldState}

	createIDs := make([]string, 0, len(req.Create))
	for id := range req.Create {
		createIDs = append(createIDs, id)
	}
	sort.Strings(createIDs)
	for _, creationID := range createIDs {
		var in struct {
			Email *string `json:"email"`
		}
		fail := func(se setError) {
			if resp.NotCreated == nil {
				resp.NotCreated = map[string]setError{}
			}
			resp.NotCreated[creationID] = se
		}
		if err := json.Unmarshal(req.Create[creationID], &in); err != nil || in.Email == nil {
			fail(setError{Type: setErrInvalidProperties, Properties: []string{"email"},
				Description: "a ForwardingAddress create needs an email"})
			continue
		}
		email := strings.ToLower(strings.TrimSpace(*in.Email))
		if !plausibleAddress(email) {
			fail(setError{Type: setErrInvalidProperties, Properties: []string{"email"},
				Description: fmt.Sprintf("%q is not an email address", email)})
			continue
		}
		row, err := d.Forwarding.CreateForwardingAddress(ctx, caller.AccountID, email)
		if err != nil {
			switch {
			case errors.Is(err, ErrForwardingExists):
				fail(setError{Type: "alreadyExists",
					Description: "that address is already registered; destroy it to resend the verification"})
			default:
				fail(setError{Type: setErrServerFail, Description: err.Error()})
			}
			continue
		}
		if resp.Created == nil {
			resp.Created = map[string]any{}
		}
		resp.Created[creationID] = map[string]any{
			"id":    EncodeForwardingID(row.ID),
			"state": row.State,
		}
	}

	for id := range req.Update {
		setNotUpdated(resp, id, setError{Type: setErrForbidden,
			Description: "a ForwardingAddress cannot be edited; destroy it and create it again to resend the verification"})
	}

	for _, raw := range req.Destroy {
		fail := func(se setError) {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = map[string]setError{}
			}
			resp.NotDestroyed[raw] = se
		}
		id, err := DecodeForwardingID(raw)
		if err != nil {
			fail(setError{Type: setErrNotFound, Description: "no such forwarding address"})
			continue
		}
		if err := d.Forwarding.DestroyForwardingAddress(ctx, caller.AccountID, id); err != nil {
			switch {
			case errors.Is(err, ErrForwardingInUse):
				fail(setError{Type: setErrForbidden,
					Description: "the address is still used by a filter or the forwarding setting; remove that first"})
			case errors.Is(err, ErrNotFound):
				fail(setError{Type: setErrNotFound, Description: "no such forwarding address"})
			default:
				fail(setError{Type: setErrServerFail, Description: "destroying the address failed"})
			}
			continue
		}
		resp.Destroyed = append(resp.Destroyed, raw)
	}

	newState, err := d.Forwarding.ForwardingState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the forwarding state", err)
	}
	resp.NewState = newState
	return resp, nil
}

// plausibleAddress is the same shallow shape check the model applies.
func plausibleAddress(a string) bool {
	if a == "" || strings.ContainsAny(a, " \t\r\n") {
		return false
	}
	at := strings.IndexByte(a, '@')
	return at > 0 && at < len(a)-1 && !strings.Contains(a[at+1:], "@")
}
