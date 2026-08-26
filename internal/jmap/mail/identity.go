package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Identity — RFC 8621 §6, over the identities table (migration 0006).
//
// # What changed, and why
//
// W3 served one identity per account, computed from the caller's address, and
// answered Identity/set with a flat `forbidden`. That was a defensible reading
// of §6.3 while nothing was editable — the RFC leaves /set support to the
// server — but it made a real user-facing feature impossible: a pilot user
// tried to save a signature and got "Server response was unexpected". §6 exists
// exactly for that, so identities are now stored state.
//
// # The property model, against §6
//
//	id            "Id" (immutable; server-set)          — the row, wire-encoded.
//	name          "String" (default: "")                — MUTABLE.
//	email         "String" (immutable)                  — refused on update.
//	replyTo       "EmailAddress[]|null" (default: null) — MUTABLE.
//	bcc           "EmailAddress[]|null" (default: null) — MUTABLE.
//	textSignature "String" (default: "")                — MUTABLE.
//	htmlSignature "String" (default: "")                — MUTABLE, sanitized.
//	mayDelete     "Boolean" (server-set)                — false; derived.
//
// email is refused because the RFC ITSELF types it "(immutable)" — this is not
// a local restriction being dressed up as one. §5.3 of RFC 8620 covers the
// answer: "Any attempt to set an immutable property [...] MUST be rejected
// with an 'invalidProperties' SetError." So an update naming it gets
// invalidProperties with the citation, not a blanket forbidden.
//
// # create: refused, deliberately (phase 1)
//
// §6.3 defines a create-specific SetError for precisely this: "forbiddenFrom:
// The user is not allowed to send from the address given as the 'email'
// property of the Identity." §9.6 makes it an obligation rather than an
// option: "If the user attempts to create a new Identity object, the server
// MUST reject it with the appropriate error if the user does not have
// permission to use that email address to send from."
//
// Moov today has NO way to establish that permission. The account's app
// password is scoped to one Mailcow mailbox; Mailcow may separately define
// aliases for it, but this server does not read them, and Email/set already
// enforces that every outgoing From equals the authenticated address
// (submission.go's forbiddenFrom check) because that is what Postfix will
// accept and what our DKIM key signs for. Creating an identity for an
// unverified address would therefore produce an identity that CANNOT send —
// the client would offer the user a From they get bounced on — and, worse,
// would be a standing invitation to relax the submission check later and turn
// it into a spoofing vector.
//
// So: create is refused with forbiddenFrom and a description naming the
// missing capability. That is the RFC's own error for "you may not send as
// this", stated honestly. It is a phase boundary, not a destination — when
// alias verification against the Mailcow API lands, this becomes "verify, then
// create", and nothing else in the design has to move: the storage already
// holds N rows per account (migration 0006).
//
// # destroy: refused for the default identity
//
// §6 mayDelete: "Servers may wish to set this to false for the user's username
// or other default address. Attempts to destroy an Identity with 'mayDelete:
// false' will be rejected with a standard 'forbidden' SetError." The default
// identity IS the account's mailbox; deleting it would leave the account
// unable to name a sender. It reports mayDelete:false and refuses with
// forbidden — the RFC's prescribed pairing, not an ad-hoc refusal. Since
// create is refused, every identity that exists is a default one, so destroy
// currently always refuses; the code still branches on mayDelete rather than
// refusing unconditionally, because that branch is the correct behavior
// already and alias identities will simply flow through it.

// identityID is the wire id of an account's DEFAULT identity.
//
// It is a CONSTANT rather than the row's encoded id, and that is load-bearing
// for the pilot: before migration 0006 this server told clients its one
// identity was "primary", and those clients persisted it — a Bulwark composer
// holds `identityId: "primary"`, and stored EmailSubmission payloads reference
// it. Rendering the default identity as "primary" regardless of its row id
// keeps every one of those references resolving across the deploy.
//
// Non-default identities (none today; alias identities later) will use the
// "i<base36>" scheme in id.go's grammar, which cannot collide: "primary" is
// not a string encodeID can produce.
const identityID = "primary"

// identityIDPrefix is the wire prefix for non-default identities.
const identityIDPrefix = "i"

// EncodeIdentityID renders a store identity row as a JMAP Id, mapping the
// account's default identity onto the stable "primary" alias.
func EncodeIdentityID(id int64, isDefault bool) string {
	if isDefault {
		return identityID
	}
	return encodeID(identityIDPrefix, id)
}

// RegisterIdentityMethods mounts the RFC 8621 §6 methods under the submission
// capability (§6 is defined by the submission spec, and §1.1 puts Identity in
// "urn:ietf:params:jmap:submission").
//
// It is separate from RegisterSubmissionMethods so the identity surface can be
// mounted on its own — a deployment (or a test) that wants identities without
// the outbox gets exactly that. RegisterSubmissionMethods calls this, so the
// two never drift apart in a full wiring, and registering twice is prevented
// by the registry itself.
func RegisterIdentityMethods(registry *jmap.Registry, deps *Deps) {
	if registry == nil || deps == nil {
		panic("mail: RegisterIdentityMethods requires a registry and deps")
	}
	if deps.Identities == nil {
		panic("mail: RegisterIdentityMethods requires Identities")
	}
	registry.Register("Identity/get", jmap.CapSubmission, deps.handleIdentityGet)
	registry.Register("Identity/changes", jmap.CapSubmission, deps.handleIdentityChanges)
	registry.Register("Identity/set", jmap.CapSubmission, deps.handleIdentitySet)
}

// ---------------------------------------------------------------------------
// contracts
// ---------------------------------------------------------------------------

// IdentityRow is one §6 Identity as the handlers need it.
type IdentityRow struct {
	ID        int64
	IsDefault bool

	Email         string
	Name          string
	ReplyTo       []EmailAddress
	Bcc           []EmailAddress
	TextSignature string
	HTMLSignature string

	// UpdatedAt is the row's watermark, which /changes pages on. It is not a
	// §6 property and is never rendered on the wire.
	UpdatedAt time.Time
}

// MayDelete is §6's mayDelete: false for the account's own address.
func (r IdentityRow) MayDelete() bool { return !r.IsDefault }

// WireID is the row's JMAP Id.
func (r IdentityRow) WireID() string { return EncodeIdentityID(r.ID, r.IsDefault) }

// IdentityPatch is a validated partial update, in the store's shape. A nil
// field means "not named by the client".
type IdentityPatch struct {
	Name          *string
	TextSignature *string
	HTMLSignature *string
	ReplyTo       *[]EmailAddress
	Bcc           *[]EmailAddress

	// rawHTMLSignature is what the CLIENT sent for htmlSignature, before
	// sanitization. It never reaches the store — it exists so /set can tell
	// whether sanitizing changed the value and report the stored form back
	// under §5.3's "properties that changed on the server as a side effect".
	//
	// Comparing the stored value against HTMLSignature instead would always
	// find them equal, since HTMLSignature IS the sanitized string: the client
	// would silently never learn that its markup was filtered.
	rawHTMLSignature *string
}

// IdentityStore is the identity surface as the JMAP layer sees it. The
// store-backed implementation is identity_adapter.go.
type IdentityStore interface {
	// ListIdentities returns the account's identities, oldest first. It
	// materializes the default identity if the account somehow has none, so a
	// provisioned mailbox always has an identity to send from.
	ListIdentities(ctx context.Context, accountID int64) ([]IdentityRow, error)
	IdentityState(ctx context.Context, accountID int64) (string, error)
	IdentitiesChangedSince(ctx context.Context, accountID int64, since time.Time, limit int) ([]IdentityRow, error)
	UpdateIdentity(ctx context.Context, accountID, id int64, patch IdentityPatch) (IdentityRow, error)
}

// ---------------------------------------------------------------------------
// Identity/get (§6.1)
// ---------------------------------------------------------------------------

// identityProperties is the §6 property set this server serves.
var identityProperties = map[string]bool{
	"id": true, "name": true, "email": true, "replyTo": true, "bcc": true,
	"textSignature": true, "htmlSignature": true, "mayDelete": true,
}

// handleIdentityGet implements Identity/get (§6.1: "This is a standard '/get'
// method as described in [RFC8620], Section 5.1. The 'ids' argument may be
// null to fetch all at once").
func (d *Deps) handleIdentityGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}

	rows, err := d.Identities.ListIdentities(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading identities", err)
	}
	state, err := d.Identities.IdentityState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the identity state", err)
	}

	byID := make(map[string]IdentityRow, len(rows))
	for _, r := range rows {
		byID[r.WireID()] = r
	}

	resp := newGetResponse(req.AccountID, state)
	if req.IDs == nil {
		// ids:null — every identity, in row order.
		for _, r := range rows {
			resp.List = append(resp.List, identityObject(r, req.Properties))
		}
	} else {
		for _, wire := range *req.IDs {
			r, ok := byID[wire]
			if !ok {
				resp.NotFound = append(resp.NotFound, wire)
				continue
			}
			resp.List = append(resp.List, identityObject(r, req.Properties))
		}
	}
	sort.Strings(resp.NotFound)
	return resp, nil
}

// identityObject renders one §6 Identity, honoring the /get properties filter.
//
// id is always present regardless of the filter — RFC 8620 §5.1: "The id
// property of the object is always returned, even if not explicitly requested."
func identityObject(r IdentityRow, properties *[]string) map[string]any {
	full := map[string]any{
		"id":    r.WireID(),
		"name":  r.Name,
		"email": r.Email,
		// §6 types replyTo and bcc "EmailAddress[]|null" with a null default.
		// A nil slice marshals to JSON null here on purpose: null means "this
		// identity configures none", while [] would claim a configured empty
		// list. addressList preserves the distinction.
		"replyTo":       identityAddressList(r.ReplyTo),
		"bcc":           identityAddressList(r.Bcc),
		"textSignature": r.TextSignature,
		"htmlSignature": r.HTMLSignature,
		"mayDelete":     r.MayDelete(),
	}
	if properties == nil {
		return full
	}
	out := map[string]any{"id": full["id"]}
	for _, p := range *properties {
		if v, ok := full[p]; ok {
			out[p] = v
		}
	}
	return out
}

// identityAddressList renders an EmailAddress list, preserving null.
func identityAddressList(list []EmailAddress) any {
	if list == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		entry := map[string]any{"email": a.Email}
		// §4.1.2.3 types name "String|null"; an absent name is null, not "".
		if a.Name != "" {
			entry["name"] = a.Name
		} else {
			entry["name"] = nil
		}
		out = append(out, entry)
	}
	return out
}

// resolveIdentity looks up an identity by wire id for the submission path.
//
// §7.5: "If the Email or Identity id given cannot be found, the submission
// creation is rejected with a standard 'invalidProperties' SetError" — which
// is why the failure is returned as a SetError rather than a method error.
func (d *Deps) resolveIdentity(ctx context.Context, accountID int64, wire string) (IdentityRow, *setError) {
	rows, err := d.Identities.ListIdentities(ctx, accountID)
	if err != nil {
		return IdentityRow{}, &setError{Type: setErrServerFail,
			Description: "reading the identity failed"}
	}
	for _, r := range rows {
		if r.WireID() == wire {
			return r, nil
		}
	}
	return IdentityRow{}, &setError{Type: setErrInvalidProperties, Properties: []string{"identityId"},
		Description: fmt.Sprintf("identityId %q names no identity of this account (RFC 8621 §7.5; Identity/get lists them)", wire)}
}

// ---------------------------------------------------------------------------
// Identity/changes (§6.2)
// ---------------------------------------------------------------------------

// maxIdentitiesPerChanges bounds one /changes page. An account has a handful
// of identities, so this is a safety rail rather than a paging mechanism.
const maxIdentitiesPerChanges = 256

// handleIdentityChanges implements Identity/changes (§6.2: "This is a standard
// '/changes' method as described in [RFC8620], Section 5.2").
//
// The cursor is the same "<nanos>-<count>" grammar every other type uses
// (adapter.go stateFor), over max(identities.updated_at) and the row count —
// so a client that saved a signature sees the state advance, which is what
// makes the save visible to its other sessions.
//
// created vs updated: an identity's created_at is not carried on IdentityRow,
// so a row that appeared since the cursor is reported as UPDATED rather than
// CREATED. That is a deliberate, bounded imprecision and not a silent one:
// §5.2's coalescing rules let a server report a created-and-updated record as
// created, but reporting a genuinely new record as updated makes a client
// fetch it — which is the same repair, one round trip later. Since create is
// refused (§6.3, see the file header), the only way a row appears at all is
// the account's own provisioning, before any client holds a cursor. When alias
// creation lands this must become a real created/updated split, exactly as
// changes.go's classify() does for messages.
func (d *Deps) handleIdentityChanges(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseChanges(ctx, args)
	if merr != nil {
		return nil, merr
	}
	since, merr := cursorFromState(req.SinceState)
	if merr != nil {
		return nil, merr
	}

	// The comparison is done in uint64 and the assignment only ever narrows a
	// value already proven smaller than maxIdentitiesPerChanges, so no
	// conversion can overflow in either direction.
	limit := maxIdentitiesPerChanges
	if req.MaxChanges != nil && *req.MaxChanges < maxIdentitiesPerChanges {
		limit = int(*req.MaxChanges) //nolint:gosec // bounded by the line above
	}

	// One row over the limit, so a full page can be distinguished from a page
	// that happens to end exactly at it — the same probe the other /changes
	// handlers use for hasMoreChanges.
	rows, err := d.Identities.IdentitiesChangedSince(ctx, caller.AccountID, since, limit+1)
	if err != nil {
		return nil, serverFail("reading identity changes", err)
	}

	resp := newChangesResponse(req.AccountID, req.SinceState)
	if len(rows) > limit {
		rows = rows[:limit]
		resp.HasMoreChanges = true
	}
	for _, r := range rows {
		resp.Updated = append(resp.Updated, r.WireID())
	}

	if resp.HasMoreChanges {
		// A truncated page must hand back a cursor that resumes exactly where
		// it stopped, not the settled state — otherwise the changes past the
		// cut are never delivered.
		resp.NewState = stateForCursor(rows[len(rows)-1].UpdatedAt)
	} else {
		state, err := d.Identities.IdentityState(ctx, caller.AccountID)
		if err != nil {
			return nil, serverFail("reading the identity state", err)
		}
		resp.NewState = state
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Identity/set (§6.3)
// ---------------------------------------------------------------------------

// setErrForbiddenFrom is §6.3's create-specific SetError: "The user is not
// allowed to send from the address given as the 'email' property of the
// Identity." §7.5 defines the same name for EmailSubmission/set, which is
// where submission.go's constant comes from — this is the §6 use of it.

// handleIdentitySet implements Identity/set (§6.3: "This is a standard '/set'
// method as described in [RFC8620], Section 5.3").
//
// Per-id isolation is the point: one bad update must not fail the batch. §5.3
// requires exactly that ("The SetError object ... for each record that failed"),
// and it is a W1 acceptance criterion for every /set this server serves.
func (d *Deps) handleIdentitySet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseSet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}

	oldState, err := d.Identities.IdentityState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the identity state", err)
	}
	// §5.3 ifInState: "If supplied, the string must match the current state of
	// the account ... otherwise, the method will be aborted and a
	// 'stateMismatch' error returned."
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, jmap.NewMethodError(jmap.CodeStateMismatch).
			WithDescription("the identity state has changed since the given ifInState")
	}

	rows, err := d.Identities.ListIdentities(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading identities", err)
	}
	byID := make(map[string]IdentityRow, len(rows))
	for _, r := range rows {
		byID[r.WireID()] = r
	}

	resp := &setResponse{AccountID: req.AccountID, OldState: oldState}

	// create — refused, per §6.3 forbiddenFrom and §9.6. See the file header.
	for creationID := range req.Create {
		if resp.NotCreated == nil {
			resp.NotCreated = map[string]setError{}
		}
		resp.NotCreated[creationID] = setError{
			Type:       setErrForbiddenFrom,
			Properties: []string{"email"},
			Description: "this server cannot yet verify that an account may send from a second address, " +
				"so it refuses to create an identity it could not honor (RFC 8621 §6.3 forbiddenFrom, §9.6); " +
				"the account's own address is available as the default identity",
		}
	}

	// update
	for wire, patchRaw := range req.Update {
		row, ok := byID[wire]
		if !ok {
			setNotUpdated(resp, wire, setError{Type: setErrNotFound,
				Description: "no identity of this account has that id"})
			continue
		}
		patch, serr := interpretIdentityPatch(patchRaw, row)
		if serr != nil {
			setNotUpdated(resp, wire, *serr)
			continue
		}
		updated, err := d.Identities.UpdateIdentity(ctx, caller.AccountID, row.ID, *patch)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				setNotUpdated(resp, wire, setError{Type: setErrNotFound,
					Description: "no identity of this account has that id"})
				continue
			}
			setNotUpdated(resp, wire, setError{Type: setErrServerFail,
				Description: "storing the identity failed"})
			continue
		}
		if resp.Updated == nil {
			resp.Updated = map[string]any{}
		}
		// §5.3: the updated map carries "any properties that changed on the
		// server as a side effect". htmlSignature is exactly that case — the
		// stored value is the SANITIZED one, so a client that sent markup the
		// policy removed learns what was actually kept rather than believing
		// its own input round-tripped.
		// The comparison is against what the CLIENT sent (rawHTMLSignature),
		// not against patch.HTMLSignature — the latter is already the
		// sanitized string, so it would always compare equal and the client
		// would never be told its markup was filtered.
		if patch.rawHTMLSignature != nil && updated.HTMLSignature != *patch.rawHTMLSignature {
			resp.Updated[wire] = map[string]any{"htmlSignature": updated.HTMLSignature}
		} else {
			// §5.3: "null if no properties changed besides those set by the client".
			resp.Updated[wire] = nil
		}
	}

	// destroy — §6 mayDelete: "Attempts to destroy an Identity with
	// 'mayDelete: false' will be rejected with a standard 'forbidden'
	// SetError."
	for _, wire := range req.Destroy {
		row, ok := byID[wire]
		if !ok {
			setNotDestroyed(resp, wire, setError{Type: setErrNotFound,
				Description: "no identity of this account has that id"})
			continue
		}
		if !row.MayDelete() {
			setNotDestroyed(resp, wire, setError{Type: setErrForbidden,
				Description: "this identity is the account's own mailbox address and reports mayDelete:false (RFC 8621 §6)"})
			continue
		}
		// Unreachable while create is refused: every stored identity is the
		// default one. Left as an explicit refusal rather than a store call,
		// so no destroy path exists before the verification story does.
		setNotDestroyed(resp, wire, setError{Type: setErrForbidden,
			Description: "destroying identities is not supported on this server"})
	}

	newState, err := d.Identities.IdentityState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the identity state", err)
	}
	resp.NewState = newState
	return resp, nil
}

func setNotUpdated(resp *setResponse, id string, e setError) {
	if resp.NotUpdated == nil {
		resp.NotUpdated = map[string]setError{}
	}
	resp.NotUpdated[id] = e
}

func setNotDestroyed(resp *setResponse, id string, e setError) {
	if resp.NotDestroyed == nil {
		resp.NotDestroyed = map[string]setError{}
	}
	resp.NotDestroyed[id] = e
}

// interpretIdentityPatch validates one §5.3 PatchObject against the §6 object.
//
// Every rejection is collected before returning, because §5.3 asks for all of
// them at once: "The SetError object SHOULD also have a property called
// 'properties' ... that lists ALL the properties that were invalid."
func interpretIdentityPatch(raw json.RawMessage, row IdentityRow) (*IdentityPatch, *setError) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, &setError{Type: setErrInvalidPatch,
			Description: "an update must be a PatchObject (RFC 8620 §5.3)"}
	}

	patch := &IdentityPatch{}
	var bad []string
	var reasons []string
	fail := func(prop, why string) {
		bad = append(bad, prop)
		reasons = append(reasons, why)
	}

	for key, val := range fields {
		property, _, hasSub, ok := splitPatchPointer(key)
		if !ok || hasSub {
			// No §6 property has nested structure a patch could address: name
			// and the signatures are strings, replyTo/bcc are lists replaced
			// whole. §5.3's invalidPatch is the answer for a pointer that
			// cannot apply.
			return nil, &setError{Type: setErrInvalidPatch,
				Description: fmt.Sprintf("%q is not a patchable path on an Identity (RFC 8621 §6)", key)}
		}

		switch property {
		case "name":
			s, ok := patchString(val)
			if !ok {
				fail("name", "name must be a string")
				continue
			}
			patch.Name = &s

		case "textSignature":
			s, ok := patchString(val)
			if !ok {
				fail("textSignature", "textSignature must be a string")
				continue
			}
			if len(s) > maxSignatureBytes {
				fail("textSignature", fmt.Sprintf("textSignature exceeds this server's %d-byte limit", maxSignatureBytes))
				continue
			}
			patch.TextSignature = &s

		case "htmlSignature":
			s, ok := patchString(val)
			if !ok {
				fail("htmlSignature", "htmlSignature must be a string")
				continue
			}
			if len(s) > maxSignatureBytes {
				fail("htmlSignature", fmt.Sprintf("htmlSignature exceeds this server's %d-byte limit", maxSignatureBytes))
				continue
			}
			// Sanitized HERE, before it is stored — signature.go documents why
			// this one string inverts the project's sanitize-on-render rule.
			clean := sanitizeHTMLSignature(s)
			patch.HTMLSignature = &clean
			raw := s
			patch.rawHTMLSignature = &raw

		case "replyTo":
			list, ok := patchAddresses(val)
			if !ok {
				fail("replyTo", "replyTo must be an EmailAddress[] or null (RFC 8621 §6)")
				continue
			}
			patch.ReplyTo = list

		case "bcc":
			list, ok := patchAddresses(val)
			if !ok {
				fail("bcc", "bcc must be an EmailAddress[] or null (RFC 8621 §6)")
				continue
			}
			patch.Bcc = list

		case "email":
			// §6 types email "(immutable)"; RFC 8620 §5.3: "Any attempt to set
			// an immutable property ... MUST be rejected with an
			// 'invalidProperties' SetError."
			fail("email", fmt.Sprintf(
				"email is immutable (RFC 8621 §6) — this identity sends as %s, the account's own mailbox; "+
					"a different sending address would need an identity of its own, which this server cannot yet verify",
				row.Email))

		case "id", "mayDelete":
			// Both are "(server-set)" in §6; same §5.3 rule.
			fail(property, fmt.Sprintf("%s is server-set and cannot be changed (RFC 8621 §6)", property))

		default:
			// §5.3: "any property ... that is not a valid property of the
			// object" is the invalidProperties condition.
			fail(property, fmt.Sprintf("%q is not a property of an Identity (RFC 8621 §6)", property))
		}
	}

	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, &setError{Type: setErrInvalidProperties, Properties: bad,
			Description: strings.Join(reasons, "; ")}
	}
	return patch, nil
}

// patchString reads a string-valued patch entry.
//
// JSON null is accepted and means the empty string: §5.3 says "If null, set to
// the default value if specified for the property", and §6 gives name,
// textSignature and htmlSignature the default "".
func patchString(raw json.RawMessage) (string, bool) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "", true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// patchAddresses reads an EmailAddress[]|null patch entry.
//
// It returns a pointer so "set to null" is distinguishable from "not named":
// the outer pointer is always non-nil here (the property WAS named), and the
// list it points at is nil for the RFC's null.
func patchAddresses(raw json.RawMessage) (*[]EmailAddress, bool) {
	if strings.TrimSpace(string(raw)) == "null" {
		var none []EmailAddress
		return &none, true
	}
	var in []struct {
		Name  *string `json:"name"`
		Email string  `json:"email"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, false
	}
	out := make([]EmailAddress, 0, len(in))
	for _, a := range in {
		addr := strings.TrimSpace(a.Email)
		// An address that will be written into a Reply-To or Bcc header must
		// actually be one: an unparseable value would become a malformed
		// header on a message we sign.
		if _, err := mail.ParseAddress(addr); err != nil {
			return nil, false
		}
		entry := EmailAddress{Email: addr}
		if a.Name != nil {
			entry.Name = strings.TrimSpace(*a.Name)
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		// An explicit empty array configures nothing, which is §6's null.
		var none []EmailAddress
		return &none, true
	}
	return &out, true
}
