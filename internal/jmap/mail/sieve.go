package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/GrupoNU/moov/internal/jmap"
)

// SieveScript (RFC 9661) — the standard surface over the account's script
// storage, and the one that unlocks Bulwark's Filters tab.
//
// Conformance decisions, stated once:
//
//   - SieveScript/get (§2.3), /set (§2.4), /query (§2.5) and /validate
//     (§2.6) are implemented.
//   - SieveScript/changes is registered but always answers
//     cannotCalculateChanges: ManageSieve has no changelog, so this server
//     can prove WHAT the current state is (the state string moves with the
//     ledger) but not WHICH scripts changed between two cursors. RFC 8620
//     §5.2 names this exact refusal, and a registered refusal beats an
//     unknownMethod (the J3 rule). Never silent: this paragraph and the
//     conformance test are the record.
//   - SieveScript/queryChanges: same refusal, same reason (§5.6).
//   - The managed script ("moov") is served by /get and may be activated or
//     deactivated, but update and destroy answer `forbidden` — RFC 9661 §4:
//     the script materializing the VacationResponse "MUST NOT ... be
//     destroyed or have its content updated by the SieveScript/set method".
//   - Every content write is policy-gated: redirect targets must be verified
//     forwarding addresses (GC-4). The gate fails closed and answers
//     `forbidden` with the offending addresses named.

// MaxSieveScriptSize is the size this server accepts for one script and the
// maxSizeScript value the session advertises (declared == applied). 1 MiB —
// the Mailcow deployment's own sieve_max_script_size, read from the live
// Dovecot config during the E6 recon. ManageSieve advertises no size
// capability, so this cannot be probed; a deployment that raises Dovecot's
// limit raises nothing here until this constant follows.
const MaxSieveScriptSize = 1 << 20

// sieveScriptProperties is the §2.1 property set.
var sieveScriptProperties = map[string]bool{
	"id":       true,
	"name":     true,
	"blobId":   true,
	"isActive": true,
}

// RegisterSieveMethods mounts the RFC 9661 methods under CapSieve. Same
// panic-at-startup contract as every registrar here.
func RegisterSieveMethods(registry *jmap.Registry, deps *Deps) {
	if registry == nil || deps == nil {
		panic("mail: RegisterSieveMethods requires a registry and deps")
	}
	if deps.Sieve == nil || deps.Blobs == nil {
		panic("mail: RegisterSieveMethods requires Sieve and Blobs")
	}
	registry.Register("SieveScript/get", jmap.CapSieve, deps.handleSieveScriptGet)
	registry.Register("SieveScript/set", jmap.CapSieve, deps.handleSieveScriptSet)
	registry.Register("SieveScript/query", jmap.CapSieve, deps.handleSieveScriptQuery)
	registry.Register("SieveScript/validate", jmap.CapSieve, deps.handleSieveScriptValidate)
	registry.Register("SieveScript/changes", jmap.CapSieve, deps.handleSieveScriptChanges)
	registry.Register("SieveScript/queryChanges", jmap.CapSieve, deps.handleSieveScriptQueryChanges)
}

// ---------------------------------------------------------------------------
// SieveScript/get
// ---------------------------------------------------------------------------

// handleSieveScriptGet implements §2.3: "a standard '/get' method ... The
// 'ids' argument may be null to fetch all scripts at once."
func (d *Deps) handleSieveScriptGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	if bad := unknownProperties(req.Properties, sieveScriptProperties); len(bad) > 0 {
		sort.Strings(bad)
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("unknown SieveScript properties: %s", strings.Join(bad, ", "))
	}

	scripts, err := d.Sieve.ListScripts(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("listing sieve scripts", err)
	}
	state, err := d.Sieve.SieveState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the sieve state", err)
	}

	resp := newGetResponse(req.AccountID, state)
	if req.IDs == nil {
		for _, s := range scripts {
			resp.List = append(resp.List, sieveScriptObject(s, req.Properties))
		}
		return resp, nil
	}
	byID := make(map[int64]SieveScriptInfo, len(scripts))
	for _, s := range scripts {
		byID[s.ID] = s
	}
	for _, raw := range *req.IDs {
		id, err := DecodeSieveScriptID(raw)
		if err != nil {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		s, ok := byID[id]
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		resp.List = append(resp.List, sieveScriptObject(s, req.Properties))
	}
	sort.Strings(resp.NotFound)
	return resp, nil
}

// sieveScriptObject renders one §2.1 object, honoring the properties filter.
func sieveScriptObject(s SieveScriptInfo, properties *[]string) map[string]any {
	full := map[string]any{
		"id":       EncodeSieveScriptID(s.ID),
		"name":     s.Name,
		"blobId":   s.BlobID,
		"isActive": s.Active,
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

// ---------------------------------------------------------------------------
// SieveScript/set
// ---------------------------------------------------------------------------

// sieveSetExtras are §2.4's additional /set arguments.
type sieveSetExtras struct {
	// OnSuccessActivateScript: "The id of the SieveScript to activate if and
	// only if all of the creations, modifications, and destructions (if any)
	// succeed." May be a creation reference ("#id").
	OnSuccessActivateScript *string `json:"onSuccessActivateScript"`

	// OnSuccessDeactivateScript: "If 'true', the currently active
	// SieveScript (if any) will be deactivated ..." and, when both are
	// present, "MUST be processed first".
	OnSuccessDeactivateScript bool `json:"onSuccessDeactivateScript"`
}

// sieveCreateArgs is the client-settable half of a §2.1 object.
type sieveCreateArgs struct {
	Name   *string `json:"name"`
	BlobID *string `json:"blobId"`
}

func (d *Deps) handleSieveScriptSet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseSet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	var extras sieveSetExtras
	if err := json.Unmarshal(args, &extras); err != nil {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("arguments did not parse: %v", err)
	}

	oldState, err := d.Sieve.SieveState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the sieve state", err)
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, jmap.NewMethodError(jmap.CodeStateMismatch).
			WithDescription("the sieve state has changed since the given ifInState")
	}

	managedID, managedExists, err := d.Sieve.ManagedScriptID(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("resolving the managed script", err)
	}

	resp := &setResponse{AccountID: req.AccountID, OldState: oldState}
	allOK := true

	// §5.3 order: create, update, destroy. Creation ids sorted for
	// determinism.
	createIDs := make([]string, 0, len(req.Create))
	for id := range req.Create {
		createIDs = append(createIDs, id)
	}
	sort.Strings(createIDs)
	createdWire := map[string]string{} // creation id -> wire id

	for _, creationID := range createIDs {
		info, serr := d.sieveCreateOne(ctx, caller.AccountID, req.Create[creationID])
		if serr != nil {
			allOK = false
			if resp.NotCreated == nil {
				resp.NotCreated = map[string]setError{}
			}
			resp.NotCreated[creationID] = *serr
			continue
		}
		if resp.Created == nil {
			resp.Created = map[string]any{}
		}
		wire := EncodeSieveScriptID(info.ID)
		createdWire[creationID] = wire
		// §5.3: created carries the server-set properties.
		resp.Created[creationID] = map[string]any{"id": wire, "isActive": false}
	}

	for id, patch := range req.Update {
		serr := d.sieveUpdateOne(ctx, caller.AccountID, id, patch, managedID, managedExists)
		if serr != nil {
			allOK = false
			setNotUpdated(resp, id, *serr)
			continue
		}
		if resp.Updated == nil {
			resp.Updated = map[string]any{}
		}
		resp.Updated[id] = nil
	}

	for _, raw := range req.Destroy {
		serr := d.sieveDestroyOne(ctx, caller.AccountID, raw, managedID, managedExists)
		if serr != nil {
			allOK = false
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = map[string]setError{}
			}
			resp.NotDestroyed[raw] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, raw)
	}

	// §2.4: the activation arguments apply "if and only if all of the
	// creations, modifications, and destructions (if any) succeed", with
	// deactivation processed first.
	if allOK {
		if extras.OnSuccessDeactivateScript {
			if err := d.Sieve.ActivateScript(ctx, caller.AccountID, 0); err != nil {
				return nil, serverFail("deactivating the active script", err)
			}
		}
		if extras.OnSuccessActivateScript != nil {
			wire := *extras.OnSuccessActivateScript
			if resolved, ok := createdWire[strings.TrimPrefix(wire, "#")]; ok && strings.HasPrefix(wire, "#") {
				wire = resolved
			}
			id, derr := DecodeSieveScriptID(wire)
			if derr != nil {
				return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
					WithDescription("onSuccessActivateScript: %v", derr)
			}
			if err := d.Sieve.ActivateScript(ctx, caller.AccountID, id); err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
						WithDescription("onSuccessActivateScript names a script that does not exist")
				}
				return nil, serverFail("activating the script", err)
			}
		}
	}

	newState, err := d.Sieve.SieveState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the sieve state", err)
	}
	resp.NewState = newState
	return resp, nil
}

// sieveCreateOne performs one §2.4 create.
func (d *Deps) sieveCreateOne(ctx context.Context, accountID int64, raw json.RawMessage) (SieveScriptInfo, *setError) {
	var args sieveCreateArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return SieveScriptInfo{}, &setError{Type: setErrInvalidProperties,
			Description: "a SieveScript create must be an object with name and blobId"}
	}
	if args.Name == nil || strings.TrimSpace(*args.Name) == "" {
		// §2.1 permits a server-dependent default name; this server requires
		// one — refusing beats inventing names the user then has to explain.
		return SieveScriptInfo{}, &setError{Type: setErrInvalidProperties, Properties: []string{"name"},
			Description: "name is required"}
	}
	if serr := validateSieveName(*args.Name); serr != nil {
		return SieveScriptInfo{}, serr
	}
	if args.BlobID == nil {
		return SieveScriptInfo{}, &setError{Type: setErrInvalidProperties, Properties: []string{"blobId"},
			Description: "blobId is required"}
	}
	content, serr := d.readSieveBlob(ctx, accountID, *args.BlobID)
	if serr != nil {
		return SieveScriptInfo{}, serr
	}
	if serr := d.sievePolicyCheck(ctx, accountID, content); serr != nil {
		return SieveScriptInfo{}, serr
	}
	info, err := d.Sieve.CreateScript(ctx, accountID, *args.Name, content)
	if err != nil {
		return SieveScriptInfo{}, sieveSetError(err)
	}
	return info, nil
}

// sieveUpdateOne performs one §2.4 update.
func (d *Deps) sieveUpdateOne(ctx context.Context, accountID int64, rawID string, patch json.RawMessage, managedID int64, managedExists bool) *setError {
	id, err := DecodeSieveScriptID(rawID)
	if err != nil {
		return &setError{Type: setErrNotFound, Description: "no such script"}
	}
	if managedExists && id == managedID {
		// RFC 9661 §4: "MUST NOT allow the VacationResponse Sieve script to
		// be destroyed or have its content updated ... Any such request MUST
		// be rejected with a 'forbidden' SetError."
		return &setError{Type: setErrForbidden,
			Description: "the moov script is server-managed (RFC 9661 §4): " +
				"edit filters and the vacation response through their own settings"}
	}
	var args sieveCreateArgs
	if err := json.Unmarshal(patch, &args); err != nil {
		return &setError{Type: setErrInvalidPatch,
			Description: "an update must be a PatchObject with name and/or blobId"}
	}
	if args.Name != nil {
		if serr := validateSieveName(*args.Name); serr != nil {
			return serr
		}
	}
	var content []byte
	if args.BlobID != nil {
		var serr *setError
		content, serr = d.readSieveBlob(ctx, accountID, *args.BlobID)
		if serr != nil {
			return serr
		}
		if serr := d.sievePolicyCheck(ctx, accountID, content); serr != nil {
			return serr
		}
	}
	if _, err := d.Sieve.UpdateScript(ctx, accountID, id, args.Name, content); err != nil {
		return sieveSetError(err)
	}
	return nil
}

// sieveDestroyOne performs one §2.4 destroy.
func (d *Deps) sieveDestroyOne(ctx context.Context, accountID int64, rawID string, managedID int64, managedExists bool) *setError {
	id, err := DecodeSieveScriptID(rawID)
	if err != nil {
		return &setError{Type: setErrNotFound, Description: "no such script"}
	}
	if managedExists && id == managedID {
		return &setError{Type: setErrForbidden,
			Description: "the moov script is server-managed (RFC 9661 §4) and cannot be destroyed"}
	}
	if err := d.Sieve.DestroyScript(ctx, accountID, id); err != nil {
		return sieveSetError(err)
	}
	return nil
}

// readSieveBlob loads uploaded script content, bounded by MaxSieveScriptSize.
func (d *Deps) readSieveBlob(ctx context.Context, accountID int64, blobID string) ([]byte, *setError) {
	rc, size, err := d.Blobs.OpenBlob(ctx, accountID, blobID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, &setError{Type: "blobNotFound", Properties: []string{"blobId"},
				Description: "the blobId does not resolve for this account"}
		}
		return nil, &setError{Type: setErrServerFail, Description: "reading the script blob failed"}
	}
	defer func() { _ = rc.Close() }()
	if size > MaxSieveScriptSize {
		return nil, &setError{Type: "tooLarge",
			Description: fmt.Sprintf("the script exceeds maxSizeScript (%d bytes)", MaxSieveScriptSize)}
	}
	content, err := io.ReadAll(io.LimitReader(rc, MaxSieveScriptSize+1))
	if err != nil {
		return nil, &setError{Type: setErrServerFail, Description: "reading the script blob failed"}
	}
	if len(content) > MaxSieveScriptSize {
		return nil, &setError{Type: "tooLarge",
			Description: fmt.Sprintf("the script exceeds maxSizeScript (%d bytes)", MaxSieveScriptSize)}
	}
	return content, nil
}

// sievePolicyCheck is the GC-4 redirect gate for raw content.
func (d *Deps) sievePolicyCheck(ctx context.Context, accountID int64, content []byte) *setError {
	if err := d.Sieve.CheckRedirectPolicy(ctx, accountID, content); err != nil {
		return &setError{Type: setErrForbidden, Description: err.Error()}
	}
	return nil
}

// validateSieveName applies §2.1's name rules (Net-Unicode, no controls, no
// line/paragraph separators) plus RFC 5804 §1.6's 128-character bound.
func validateSieveName(name string) *setError {
	bad := func(why string) *setError {
		return &setError{Type: setErrInvalidProperties, Properties: []string{"name"}, Description: why}
	}
	if name == "" {
		return bad("name must not be empty")
	}
	runes := []rune(name)
	if len(runes) > 128 {
		return bad("name must be at most 128 characters (RFC 5804 §1.6)")
	}
	for _, r := range runes {
		if r <= 0x1f || (r >= 0x7f && r <= 0x9f) || r == 0x2028 || r == 0x2029 {
			return bad("name contains characters RFC 9661 §2.1 forbids")
		}
	}
	return nil
}

// sieveSetError maps the adapter vocabulary onto §2.4's SetError types.
func sieveSetError(err error) *setError {
	var invalid *SieveInvalidError
	if errors.As(err, &invalid) {
		// §2.4 invalidSieve: "The SieveScript content violates the Sieve
		// grammar and/or one or more extensions mentioned in the script's
		// 'require' statement(s) are not supported."
		return &setError{Type: "invalidSieve", Description: invalid.Description}
	}
	var taken *SieveNameTakenError
	if errors.As(err, &taken) {
		se := &setError{Type: "alreadyExists",
			Description: "a script with that name already exists"}
		if taken.ExistingID > 0 {
			se.ExistingID = EncodeSieveScriptID(taken.ExistingID)
		}
		return se
	}
	switch {
	case errors.Is(err, ErrSieveManaged):
		return &setError{Type: setErrForbidden,
			Description: "the moov script is server-managed (RFC 9661 §4)"}
	case errors.Is(err, ErrSieveScriptActive):
		// §2.4 sieveIsActive, plus its rule: "The active SieveScript MUST
		// NOT be destroyed unless it is first deactivated in a separate
		// SieveScript/set method call."
		return &setError{Type: "sieveIsActive",
			Description: "the script is active; deactivate it first in a separate call"}
	case errors.Is(err, errSieveTooLarge):
		return &setError{Type: "tooLarge", Description: "the script exceeds the server's size limit"}
	case errors.Is(err, errSieveOverQuota):
		return &setError{Type: "overQuota", Description: "the script would exceed the server's script quota"}
	case errors.Is(err, ErrNotFound):
		return &setError{Type: setErrNotFound, Description: "no such script"}
	}
	return &setError{Type: setErrServerFail, Description: "the operation failed on the server"}
}

// ---------------------------------------------------------------------------
// SieveScript/validate
// ---------------------------------------------------------------------------

// sieveValidateRequest is §2.6's request shape.
type sieveValidateRequest struct {
	AccountID string  `json:"accountId"`
	BlobID    *string `json:"blobId"`
}

// handleSieveScriptValidate implements §2.6: "functionality equivalent to
// that of the CHECKSCRIPT command".
func (d *Deps) handleSieveScriptValidate(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	caller, ok := jmap.CallerFromContext(ctx)
	if !ok {
		return nil, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}
	var req sieveValidateRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("arguments did not parse: %v", err)
	}
	if req.AccountID != caller.JMAPAccountID() {
		return nil, jmap.NewMethodError(jmap.CodeAccountNotFound).
			WithDescription("accountId %q is not an account this session can use", req.AccountID)
	}
	if req.BlobID == nil {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("blobId is required")
	}
	content, serr := d.readSieveBlob(ctx, caller.AccountID, *req.BlobID)
	if serr != nil {
		// §2.6's response reports content problems through `error`; blob
		// resolution problems are method-level.
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("%s", serr.Description)
	}

	// §2.6: "error: SetError|null — An 'invalidSieve' SetError object if
	// the script content is invalid ... or null if the script content is
	// valid."
	var errObj *setError
	if err := d.Sieve.ValidateScript(ctx, caller.AccountID, content); err != nil {
		var invalid *SieveInvalidError
		if errors.As(err, &invalid) {
			errObj = &setError{Type: "invalidSieve", Description: invalid.Description}
		} else {
			return nil, serverFail("validating the script", err)
		}
	}
	return map[string]any{
		"accountId": req.AccountID,
		"error":     errObj,
	}, nil
}

// ---------------------------------------------------------------------------
// SieveScript/query
// ---------------------------------------------------------------------------

// sieveQueryFilter is §2.5's FilterCondition: name (contains) and isActive
// (equals).
type sieveQueryFilter struct {
	Name     *string `json:"name"`
	IsActive *bool   `json:"isActive"`

	// Operator marks a FilterOperator (AND/OR/NOT), which this server does
	// not implement over a list this small — refused with unsupportedFilter
	// rather than half-implemented.
	Operator *string `json:"operator"`
}

// handleSieveScriptQuery implements §2.5 over the in-memory list — the whole
// collection is at most a handful of scripts, so the standard /query
// machinery (anchors, windows) reduces to slicing.
func (d *Deps) handleSieveScriptQuery(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	caller, ok := jmap.CallerFromContext(ctx)
	if !ok {
		return nil, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}
	var req queryRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("arguments did not parse: %v", err)
	}
	if req.AccountID != caller.JMAPAccountID() {
		return nil, jmap.NewMethodError(jmap.CodeAccountNotFound).
			WithDescription("accountId %q is not an account this session can use", req.AccountID)
	}

	var filter sieveQueryFilter
	if len(req.Filter) > 0 && string(req.Filter) != "null" {
		if err := json.Unmarshal(req.Filter, &filter); err != nil {
			return nil, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
				WithDescription("the filter did not parse: %v", err)
		}
		if filter.Operator != nil {
			return nil, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
				WithDescription("FilterOperator is not supported for SieveScript/query; use a flat FilterCondition")
		}
	}

	scripts, err := d.Sieve.ListScripts(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("listing sieve scripts", err)
	}
	state, err := d.Sieve.SieveState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the sieve state", err)
	}

	matched := scripts[:0:0]
	for _, s := range scripts {
		if filter.Name != nil && !strings.Contains(s.Name, *filter.Name) {
			continue
		}
		if filter.IsActive != nil && s.Active != *filter.IsActive {
			continue
		}
		matched = append(matched, s)
	}

	// §2.5 sort properties: name, isActive. Default: name ascending.
	comparators := req.Sort
	if len(comparators) == 0 {
		asc := true
		comparators = []comparator{{Property: "name", IsAscending: &asc}}
	}
	for _, c := range comparators {
		if c.Property != "name" && c.Property != "isActive" {
			return nil, jmap.NewMethodError(jmap.CodeUnsupportedSort).
				WithDescription("SieveScript/query sorts by name or isActive (RFC 9661 §2.5); %q is neither", c.Property)
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		for _, c := range comparators {
			asc := c.IsAscending == nil || *c.IsAscending
			var less, eq bool
			switch c.Property {
			case "name":
				less, eq = matched[i].Name < matched[j].Name, matched[i].Name == matched[j].Name
			case "isActive":
				// false < true ascending
				less = !matched[i].Active && matched[j].Active
				eq = matched[i].Active == matched[j].Active
			}
			if eq {
				continue
			}
			if asc {
				return less
			}
			return !less
		}
		return matched[i].ID < matched[j].ID
	})

	position := int64(0)
	if req.Position != nil {
		position = *req.Position
	}
	if position < 0 {
		// §5.5: a negative position counts from the end.
		position += int64(len(matched))
	}
	if position < 0 {
		position = 0
	}
	if position > int64(len(matched)) {
		position = int64(len(matched))
	}
	window := matched[position:]
	if req.Limit != nil && uint64(len(window)) > *req.Limit {
		window = window[:*req.Limit]
	}

	ids := make([]string, 0, len(window))
	for _, s := range window {
		ids = append(ids, EncodeSieveScriptID(s.ID))
	}
	total := uint64(len(matched))
	resp := queryResponse{
		AccountID:  req.AccountID,
		QueryState: state,
		// canCalculateChanges false: /queryChanges answers the honest
		// refusal (see the package comment).
		CanCalculateChanges: false,
		Position:            uint64(position), //nolint:gosec // clamped to [0, len(matched)] above
		IDs:                 ids,
	}
	if req.CalculateTotal {
		resp.Total = &total
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// the honest refusals
// ---------------------------------------------------------------------------

// handleSieveScriptChanges answers cannotCalculateChanges — see the package
// comment for the record of why.
func (d *Deps) handleSieveScriptChanges(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	if _, ok := jmap.CallerFromContext(ctx); !ok {
		return nil, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}
	return nil, jmap.NewMethodError(jmap.CodeCannotCalculateChanges).
		WithDescription("ManageSieve keeps no changelog; refetch with SieveScript/get")
}

// handleSieveScriptQueryChanges answers the same refusal for §5.6.
func (d *Deps) handleSieveScriptQueryChanges(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	if _, ok := jmap.CallerFromContext(ctx); !ok {
		return nil, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}
	return nil, jmap.NewMethodError(jmap.CodeCannotCalculateChanges).
		WithDescription("ManageSieve keeps no changelog; re-run SieveScript/query")
}
