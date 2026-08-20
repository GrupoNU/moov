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

// EmailSubmission and Identity — RFC 8621 §6 and §7 over the outbox queue
// (internal/submit, migration 0005), plus arbitration W-A3's undo window.
//
// # The undo model, against the RFC
//
// RFC 8621 §7.1 undoStatus: "pending: It may be possible to cancel this
// submission. / final: The message has been relayed ... / canceled: The
// message has been canceled". §7.5 permits the update "pending" → "canceled"
// and names cannotUnsend for the too-late case. Moov implements exactly that,
// with the window being the intent row's not_before (W-A3): while it has not
// passed, the row is uncancelable by nobody and unclaimable by the executor —
// after it, first mover wins atomically (store.CancelSendIntent's CAS vs the
// SKIP LOCKED claim).
//
// destroy carries ONE documented deviation, decided in W-A3 and enforced in
// store.DestroySendIntent: destroying a still-PENDING submission cancels it.
// §7.5 scopes destroy to record-keeping; W-A3 arbitrated that a user removing
// a pending submission means "do not send this", and sending mail the user
// visibly retracted is the worse failure. Outside the window the RFC
// semantics hold exactly: tombstone, send unaffected.
//
// # onSuccessUpdateEmail / onSuccessDestroyEmail (§7.5)
//
// Applied SYNCHRONOUSLY, per the letter of §7.5: "A single implicit Email/set
// call MUST be made after all create/update/destroy requests have been
// processed ... the response to this MUST be returned after the
// EmailSubmission/set response." That is why handleSubmissionSet is the
// server's first MultiHandler — it emits the implicit Email/set response
// under its own call id. The consequence for undo is documented rather than
// hidden: the draft moves to Sent (and sheds $draft) when the SUBMISSION is
// created, not when the mail leaves; a canceled submission leaves the moved
// message where the client put it, and the client that canceled repairs its
// own move — the same contract Fastmail's server-side undo has.
//
// # Identity (§6)
//
// One identity per account: the mailbox owner's own address. Mailcow accounts
// send as themselves (the app password is scoped to the mailbox), so a single
// server-defined identity is the truthful set, id and email immutable.
// Identity/set is refused with forbidden — §6.3: "servers MAY support this",
// and this one does not.

// The wire id prefix for EmailSubmission ids, in id.go's scheme
// (e-mail m-ailbox t-hread a-ccount s-ubmission).
const submissionIDPrefix = "s"

// EncodeSubmissionID renders a send-intent id as a JMAP Id.
func EncodeSubmissionID(id int64) string { return encodeID(submissionIDPrefix, id) }

// DecodeSubmissionID parses a JMAP EmailSubmission Id back to the intent id.
func DecodeSubmissionID(s string) (int64, error) {
	return decodeID(submissionIDPrefix, "submission", s)
}

// identityID is the account's single identity's id.
const identityID = "primary"

// ---------------------------------------------------------------------------
// contracts
// ---------------------------------------------------------------------------

// ErrCannotUnsend means a cancel arrived after the submission stopped being
// cancelable — RFC 8621 §7.5's cannotUnsend SetError condition.
var ErrCannotUnsend = errors.New("mail: the submission can no longer be canceled")

// SubmissionObserver receives one call per submission canceled through this
// layer (W4b metrics). It is the mirror of internal/submit's Observer, and
// exists for the same reason: this package must not import the metrics
// exporter to be able to count an undo.
type SubmissionObserver interface {
	SubmissionCanceled()
}

// SubmissionRow is one EmailSubmission as the handlers need it.
type SubmissionRow struct {
	ID         int64
	EmailID    int64
	IdentityID string
	MailFrom   string
	RcptTo     []string

	// SendAt is when the submission is (or was) released — the row's
	// not_before, which is RFC 8621 §7.1's sendAt for a server-delayed send.
	SendAt time.Time

	// UndoStatus is the derived §7.1 value: "pending" | "final" | "canceled".
	UndoStatus string

	// SMTPReply is the server's acceptance line, or the failure text for a
	// permanently failed submission. Empty while pending.
	SMTPReply string

	// Failed reports a permanent failure (deliveryStatus delivered:"no").
	Failed bool

	// Destroyed reports the record tombstone.
	Destroyed bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// SubmissionSpec is what a create enqueues.
type SubmissionSpec struct {
	EmailID    int64
	IdentityID string
	MailFrom   string
	RcptTo     []string
	// MessageRFCID is the Message-ID the transmission will carry (the dedupe
	// key); taken from the draft, or minted at create.
	MessageRFCID string
	// UndoWindow delays the release (W-A3).
	UndoWindow time.Duration
}

// SubmissionStore is the queue as the JMAP layer sees it. The store-backed
// implementation is submission_adapter.go; the fakes in the tests drive the
// handlers without PostgreSQL.
type SubmissionStore interface {
	SubmissionsByID(ctx context.Context, accountID int64, ids []int64) ([]SubmissionRow, error)
	ListSubmissions(ctx context.Context, accountID int64, limit int) ([]SubmissionRow, error)
	SubmissionState(ctx context.Context, accountID int64) (string, error)
	SubmissionsChangedSince(ctx context.Context, accountID int64, since time.Time, limit int) ([]SubmissionRow, error)

	Enqueue(ctx context.Context, accountID int64, spec SubmissionSpec) (SubmissionRow, error)
	// Cancel implements undoStatus -> "canceled". ErrNotFound for an unknown
	// or foreign id; ErrCannotUnsend when the executor won the race.
	Cancel(ctx context.Context, accountID, id int64) (SubmissionRow, error)
	// Destroy tombstones the record (canceling first when still pending — the
	// documented W-A3 deviation).
	Destroy(ctx context.Context, accountID, id int64) (SubmissionRow, error)
}

// ---------------------------------------------------------------------------
// registration
// ---------------------------------------------------------------------------

// The undo window's contract (config: MOOV_UNDO_WINDOW_SECONDS, default 10,
// clamped to [5, 30]).
const (
	DefaultUndoWindow = 10 * time.Second
	MinUndoWindow     = 5 * time.Second
	MaxUndoWindow     = 30 * time.Second
)

// clampUndoWindow applies the window contract.
func clampUndoWindow(d time.Duration) time.Duration {
	switch {
	case d == 0:
		return DefaultUndoWindow
	case d < MinUndoWindow:
		return MinUndoWindow
	case d > MaxUndoWindow:
		return MaxUndoWindow
	default:
		return d
	}
}

// RegisterSubmissionMethods registers the RFC 8621 §6/§7 methods (W3) under
// the submission capability. Same contract as the other registrars: missing
// dependencies panic at startup, never at the first send.
//
// EmailSubmission/set is registered as a MultiHandler because §7.5's implicit
// Email/set response is a second response under the same call id — see
// handleSubmissionSet.
func RegisterSubmissionMethods(registry *jmap.Registry, deps *Deps) {
	if registry == nil || deps == nil {
		panic("mail: RegisterSubmissionMethods requires a registry and deps")
	}
	if deps.Submissions == nil || deps.Emails == nil || deps.State == nil {
		panic("mail: RegisterSubmissionMethods requires Submissions, Emails and State")
	}
	if deps.Writer == nil {
		// The §7.5 implicit Email/set applies through the same writer the
		// explicit one uses; submission without it would enqueue mail it can
		// never bookkeep.
		panic("mail: RegisterSubmissionMethods requires Writer for onSuccessUpdateEmail")
	}
	deps.UndoWindow = clampUndoWindow(deps.UndoWindow)

	registry.Register("EmailSubmission/get", jmap.CapSubmission, deps.handleSubmissionGet)
	registry.RegisterMulti("EmailSubmission/set", jmap.CapSubmission, deps.handleSubmissionSet)
	registry.Register("EmailSubmission/changes", jmap.CapSubmission, deps.handleSubmissionChanges)
	registry.Register("Identity/get", jmap.CapSubmission, deps.handleIdentityGet)
	registry.Register("Identity/changes", jmap.CapSubmission, deps.handleIdentityChanges)
	registry.Register("Identity/set", jmap.CapSubmission, deps.handleIdentitySet)
}

// ---------------------------------------------------------------------------
// EmailSubmission/get
// ---------------------------------------------------------------------------

// submissionProperties is the §7.1 property set this server serves.
var submissionProperties = map[string]bool{
	"id": true, "identityId": true, "emailId": true, "threadId": true,
	"envelope": true, "sendAt": true, "undoStatus": true,
	"deliveryStatus": true, "dsnBlobIds": true, "mdnBlobIds": true,
}

// handleSubmissionGet implements EmailSubmission/get (RFC 8620 §5.1 over the
// §7.1 object).
func (d *Deps) handleSubmissionGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	if bad := unknownProperties(req.Properties, submissionProperties); len(bad) > 0 {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("unknown EmailSubmission properties: %s", strings.Join(bad, ", "))
	}

	state, err := d.Submissions.SubmissionState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading submission state", err)
	}
	resp := newGetResponse(req.AccountID, state)
	props, _ := propertySet(req.Properties)

	var rows []SubmissionRow
	if req.IDs == nil {
		// §5.1 null ids: every record. Bounded by the same ceiling the id
		// list form has; an account holds few submissions, and the newest
		// maxObjectsInGet of a busier one is the honest window.
		rows, err = d.Submissions.ListSubmissions(ctx, caller.AccountID, d.Limits.MaxObjectsInGet)
		if err != nil {
			return nil, serverFail("listing submissions", err)
		}
	} else {
		decoded, wireOf, unknown := decodeIDList(*req.IDs, DecodeSubmissionID)
		resp.NotFound = append(resp.NotFound, unknown...)
		rows, err = d.Submissions.SubmissionsByID(ctx, caller.AccountID, decoded)
		if err != nil {
			return nil, serverFail("reading submissions", err)
		}
		found := make(map[int64]bool, len(rows))
		for _, r := range rows {
			if !r.Destroyed {
				found[r.ID] = true
			}
		}
		for id, wire := range wireOf {
			if !found[id] {
				resp.NotFound = append(resp.NotFound, wire)
			}
		}
	}

	// Thread ids resolve through the message rows, batched.
	threadOf := d.submissionThreads(ctx, caller.AccountID, rows)
	for _, r := range rows {
		if r.Destroyed {
			continue
		}
		resp.List = append(resp.List, submissionObject(r, threadOf[r.EmailID], props))
	}
	sort.Strings(resp.NotFound)
	return resp, nil
}

// submissionThreads resolves emailId -> wire threadId for the rows that still
// have a live message; a destroyed draft yields "" and the property nulls.
func (d *Deps) submissionThreads(ctx context.Context, accountID int64, rows []SubmissionRow) map[int64]string {
	ids := make([]int64, 0, len(rows))
	seen := map[int64]bool{}
	for _, r := range rows {
		if r.EmailID > 0 && !seen[r.EmailID] {
			seen[r.EmailID] = true
			ids = append(ids, r.EmailID)
		}
	}
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out
	}
	emails, err := d.Emails.EmailsByID(ctx, accountID, ids)
	if err != nil {
		return out
	}
	for _, e := range emails {
		out[e.ID] = e.ThreadID
	}
	return out
}

// submissionObject renders one §7.1 EmailSubmission.
func submissionObject(r SubmissionRow, threadID string, props map[string]bool) map[string]any {
	out := map[string]any{"id": EncodeSubmissionID(r.ID)}
	if wants(props, "identityId") {
		out["identityId"] = r.IdentityID
	}
	if wants(props, "emailId") {
		out["emailId"] = EncodeEmailID(r.EmailID)
	}
	if wants(props, "threadId") {
		if threadID != "" {
			out["threadId"] = threadID
		} else {
			out["threadId"] = nil
		}
	}
	if wants(props, "envelope") {
		rcpts := make([]map[string]any, 0, len(r.RcptTo))
		for _, rcpt := range r.RcptTo {
			rcpts = append(rcpts, map[string]any{"email": rcpt, "parameters": nil})
		}
		out["envelope"] = map[string]any{
			"mailFrom": map[string]any{"email": r.MailFrom, "parameters": nil},
			"rcptTo":   rcpts,
		}
	}
	if wants(props, "sendAt") {
		// §7.1 types sendAt as UTCDate.
		out["sendAt"] = r.SendAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if wants(props, "undoStatus") {
		out["undoStatus"] = r.UndoStatus
	}
	if wants(props, "deliveryStatus") {
		out["deliveryStatus"] = deliveryStatus(r)
	}
	// §7.1: dsnBlobIds/mdnBlobIds are "Id[] (server-set)". This server
	// requests no DSNs, so empty is the truthful constant.
	if wants(props, "dsnBlobIds") {
		out["dsnBlobIds"] = []string{}
	}
	if wants(props, "mdnBlobIds") {
		out["mdnBlobIds"] = []string{}
	}
	return out
}

// deliveryStatus renders §7.1's DeliveryStatus map, or null before anything
// is known.
//
// §7.1: delivered is "queued" / "yes" / "no" / "unknown". Postfix's 250
// means our submission server QUEUED the message — end-to-end delivery is
// unknowable without DSN parsing, which this server does not do — so an
// accepted submission reports "unknown" (§7.1's own reading: "We do not know
// if the message was delivered successfully"), and a permanent failure
// reports "no" with the refusal as the smtpReply.
func deliveryStatus(r SubmissionRow) any {
	if r.SMTPReply == "" && !r.Failed {
		return nil
	}
	delivered := "unknown"
	if r.Failed {
		delivered = "no"
	}
	per := map[string]any{}
	for _, rcpt := range r.RcptTo {
		per[rcpt] = map[string]any{
			"smtpReply": r.SMTPReply,
			"delivered": delivered,
			"displayed": "unknown",
		}
	}
	return per
}

// ---------------------------------------------------------------------------
// EmailSubmission/set
// ---------------------------------------------------------------------------

// The §7.5 SetError vocabulary this handler emits, beyond §5.3's.
const (
	setErrForbiddenMailFrom = "forbiddenMailFrom"
	setErrForbiddenFrom     = "forbiddenFrom"
	setErrNoRecipients      = "noRecipients"
	setErrInvalidRecipients = "invalidRecipients"
	setErrTooManyRecipients = "tooManyRecipients"
	setErrCannotUnsend      = "cannotUnsend"
)

// maxRecipients bounds one submission's envelope. Postfix's own default
// ceiling is 1000 (smtpd_recipient_limit); 100 is far under it and far over
// any human send, and §7.5 names tooManyRecipients for the refusal.
const maxRecipients = 100

// submissionSetExtra is EmailSubmission/set's two §7.5 extra arguments.
type submissionSetExtra struct {
	OnSuccessUpdateEmail  map[string]json.RawMessage `json:"onSuccessUpdateEmail"`
	OnSuccessDestroyEmail []string                   `json:"onSuccessDestroyEmail"`
}

// handleSubmissionSet implements EmailSubmission/set (§7.5) as a
// MultiHandler: its own response first, then — when any onSuccess argument
// named a successful submission — the implicit Email/set response.
func (d *Deps) handleSubmissionSet(ctx context.Context, args json.RawMessage) ([]jmap.NamedResult, *jmap.MethodError) {
	req, caller, merr := parseSet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	var extra submissionSetExtra
	if err := json.Unmarshal(args, &extra); err != nil {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("arguments did not parse: %v", err)
	}

	oldState, err := d.Submissions.SubmissionState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading submission state", err)
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, jmap.NewMethodError(jmap.CodeStateMismatch).
			WithDescription("ifInState does not match the current EmailSubmission state; fetch changes and retry")
	}

	resp := &setResponse{AccountID: req.AccountID, OldState: oldState}
	created := jmap.CreationIDsFromContext(ctx)

	// creationEmail maps "#creationId" and plain submission ids to the
	// EMAIL each successful submission is about, which is what the onSuccess
	// arguments resolve through (§7.5: the id "may be a creation id
	// reference, prefixed with #").
	emailOf := map[string]int64{}

	// ---- create -----------------------------------------------------------
	createIDs := make([]string, 0, len(req.Create))
	for cid := range req.Create {
		createIDs = append(createIDs, cid)
	}
	sort.Strings(createIDs)

	for _, cid := range createIDs {
		row, serr := d.applySubmissionCreate(ctx, caller, req.Create[cid])
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
		wire := EncodeSubmissionID(row.ID)
		resp.Created[cid] = map[string]any{
			// §5.3 server-set properties of the fresh submission. undoStatus
			// and sendAt are the two a client acts on (the undo button and
			// its countdown).
			"id":         wire,
			"undoStatus": row.UndoStatus,
			"sendAt":     row.SendAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
		created.Record(cid, wire)
		emailOf["#"+cid] = row.EmailID
		emailOf[wire] = row.EmailID
	}

	// ---- update (undoStatus -> canceled) ----------------------------------
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
			d.noteNotUpdated(resp, wire, setError{Type: setErrWillDestroy,
				Description: "the same id is also in destroy; the update was ignored"})
			continue
		}
		row, serr := d.applySubmissionUpdate(ctx, caller.AccountID, wire, req.Update[wire])
		if serr != nil {
			d.noteNotUpdated(resp, wire, *serr)
			continue
		}
		if resp.Updated == nil {
			resp.Updated = map[string]any{}
		}
		resp.Updated[wire] = nil
		emailOf[wire] = row.EmailID
	}

	// ---- destroy ----------------------------------------------------------
	seen := make(map[string]bool, len(req.Destroy))
	for _, wire := range req.Destroy {
		if seen[wire] {
			continue
		}
		seen[wire] = true
		row, serr := d.applySubmissionDestroy(ctx, caller.AccountID, wire)
		if serr != nil {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = map[string]setError{}
			}
			resp.NotDestroyed[wire] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, wire)
		emailOf[wire] = row.EmailID
	}

	newState, err := d.Submissions.SubmissionState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading submission state", err)
	}
	resp.NewState = newState

	results := []jmap.NamedResult{{Name: "EmailSubmission/set", Result: resp}}
	if implicit := d.applyOnSuccess(ctx, req.AccountID, extra, emailOf); implicit != nil {
		results = append(results, *implicit)
	}
	return results, nil
}

// applyOnSuccess performs §7.5's implicit Email/set for the onSuccess
// arguments that reference SUCCESSFUL submissions, and returns its response —
// or nil when there is nothing to apply.
//
// "The server MUST perform the changes as a single implicit call to
// Email/set" — implemented by constructing the very arguments an explicit
// call would carry and running the real handler, so the two are one code path
// and cannot diverge (the reuse rule 5 of the brief names as "through W1's
// machinery").
func (d *Deps) applyOnSuccess(ctx context.Context, accountID string, extra submissionSetExtra, emailOf map[string]int64) *jmap.NamedResult {
	update := map[string]json.RawMessage{}
	for ref, patch := range extra.OnSuccessUpdateEmail {
		emailID, ok := emailOf[ref]
		if !ok || emailID == 0 {
			// §7.5 scopes the map to submissions this call touched; an entry
			// for a failed or unknown one applies to nothing.
			continue
		}
		update[EncodeEmailID(emailID)] = patch
	}
	var destroy []string
	for _, ref := range extra.OnSuccessDestroyEmail {
		if emailID, ok := emailOf[ref]; ok && emailID != 0 {
			destroy = append(destroy, EncodeEmailID(emailID))
		}
	}
	if len(update) == 0 && len(destroy) == 0 {
		return nil
	}

	implicitArgs, err := json.Marshal(struct {
		AccountID string                     `json:"accountId"`
		Update    map[string]json.RawMessage `json:"update,omitempty"`
		Destroy   []string                   `json:"destroy,omitempty"`
	}{accountID, update, destroy})
	if err != nil {
		return &jmap.NamedResult{Name: "error",
			Result: map[string]any{"type": string(jmap.CodeServerFail),
				"description": "building the implicit Email/set failed"}}
	}

	result, merr := d.handleEmailSet(ctx, implicitArgs)
	if merr != nil {
		// §3.6.2's error-response shape, under the same call id — the engine
		// gives every NamedResult the calling invocation's id.
		out := map[string]any{"type": string(merr.Code)}
		if merr.Description != "" {
			out["description"] = merr.Description
		}
		return &jmap.NamedResult{Name: "error", Result: out}
	}
	return &jmap.NamedResult{Name: "Email/set", Result: result}
}

// applySubmissionCreate validates one §7.5 creation object and enqueues it.
func (d *Deps) applySubmissionCreate(ctx context.Context, caller jmap.Caller, raw json.RawMessage) (SubmissionRow, *setError) {
	var zero SubmissionRow

	var obj struct {
		IdentityID *string `json:"identityId"`
		EmailID    *string `json:"emailId"`
		Envelope   *struct {
			MailFrom struct {
				Email string `json:"email"`
			} `json:"mailFrom"`
			RcptTo []struct {
				Email string `json:"email"`
			} `json:"rcptTo"`
		} `json:"envelope"`
		UndoStatus *string `json:"undoStatus"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return zero, &setError{Type: setErrInvalidProperties,
			Description: "a create must be an EmailSubmission object (RFC 8621 §7.5)"}
	}

	// identityId: required (§7.1), and it must be the account's one identity.
	if obj.IdentityID == nil || *obj.IdentityID != identityID {
		return zero, &setError{Type: setErrInvalidProperties, Properties: []string{"identityId"},
			Description: fmt.Sprintf("identityId must be this account's identity (%q; Identity/get lists it)", identityID)}
	}
	// undoStatus on create may only ask for what the server does anyway.
	if obj.UndoStatus != nil && *obj.UndoStatus != "pending" && *obj.UndoStatus != "final" {
		return zero, &setError{Type: setErrInvalidProperties, Properties: []string{"undoStatus"},
			Description: `undoStatus on create must be "pending" or "final" (RFC 8621 §7.1)`}
	}

	// emailId: required, resolving a §5.3 creation reference — the §7.5
	// canonical flow creates the draft and the submission in one request.
	if obj.EmailID == nil {
		return zero, &setError{Type: setErrInvalidProperties, Properties: []string{"emailId"},
			Description: "emailId is required (RFC 8621 §7.1)"}
	}
	wireEmail, resolved := jmap.CreationIDsFromContext(ctx).Resolve(*obj.EmailID)
	if !resolved {
		return zero, &setError{Type: setErrInvalidProperties, Properties: []string{"emailId"},
			Description: fmt.Sprintf("emailId %q references a creation id this request did not create (RFC 8620 §5.3)", *obj.EmailID)}
	}
	emailID, err := DecodeEmailID(wireEmail)
	if err != nil {
		return zero, &setError{Type: setErrInvalidProperties, Properties: []string{"emailId"},
			Description: "emailId is not an Email id this server issued"}
	}

	rows, err := d.Emails.EmailsByID(ctx, caller.AccountID, []int64{emailID})
	if err != nil {
		return zero, &setError{Type: setErrServerFail, Description: "reading the message failed"}
	}
	if len(rows) == 0 {
		return zero, &setError{Type: setErrInvalidProperties, Properties: []string{"emailId"},
			Description: "emailId names no message of this account"}
	}
	email := rows[0]

	// forbiddenFrom (§7.5): "The From address of the Email is not allowed" —
	// every From must be the authenticated mailbox, because the app password
	// authorizes exactly that sender and Postfix will reject or rewrite
	// anything else after DKIM signing as the account.
	for _, from := range email.Addresses["from"] {
		if !strings.EqualFold(strings.TrimSpace(from.Email), caller.Email) {
			return zero, &setError{Type: setErrForbiddenFrom,
				Description: fmt.Sprintf("the message's From (%s) is not the authenticated account (%s)", from.Email, caller.Email)}
		}
	}

	spec := SubmissionSpec{
		EmailID:    emailID,
		IdentityID: identityID,
		UndoWindow: d.UndoWindow,
	}

	// The envelope: given, or derived per §7.1.2: "If the envelope property
	// is null or omitted ... the server MUST generate this: mailFrom MUST be
	// the email in the From header ... rcptTo MUST be the deduplicated set of
	// email addresses in the To, Cc and Bcc headers".
	if obj.Envelope != nil {
		spec.MailFrom = strings.TrimSpace(obj.Envelope.MailFrom.Email)
		for _, r := range obj.Envelope.RcptTo {
			spec.RcptTo = append(spec.RcptTo, strings.TrimSpace(r.Email))
		}
	} else {
		spec.MailFrom = caller.Email
		seen := map[string]bool{}
		for _, field := range []string{"to", "cc", "bcc"} {
			for _, a := range email.Addresses[field] {
				addr := strings.TrimSpace(a.Email)
				key := strings.ToLower(addr)
				if addr == "" || seen[key] {
					continue
				}
				seen[key] = true
				spec.RcptTo = append(spec.RcptTo, addr)
			}
		}
	}

	// forbiddenMailFrom (§7.5): the envelope sender must be the account.
	if !strings.EqualFold(spec.MailFrom, caller.Email) {
		return zero, &setError{Type: setErrForbiddenMailFrom,
			Description: fmt.Sprintf("the envelope mailFrom (%s) is not the authenticated account (%s)", spec.MailFrom, caller.Email)}
	}
	if len(spec.RcptTo) == 0 {
		// §7.5: "noRecipients: The envelope [or generated envelope] does not
		// have any rcptTo email addresses."
		return zero, &setError{Type: setErrNoRecipients,
			Description: "the submission has no recipients: the envelope (or the To/Cc/Bcc headers it derives from) names nobody"}
	}
	if len(spec.RcptTo) > maxRecipients {
		return zero, &setError{Type: setErrTooManyRecipients,
			Description: fmt.Sprintf("the submission names %d recipients; this server accepts at most %d", len(spec.RcptTo), maxRecipients)}
	}
	if bad := invalidRecipients(spec.RcptTo); len(bad) > 0 {
		// §7.5: "invalidRecipients: The rcptTo property ... contains at least
		// one rcptTo value that is not a valid email address for sending to."
		return zero, &setError{Type: setErrInvalidRecipients,
			Description: "not valid addresses for sending: " + strings.Join(bad, ", ")}
	}

	// The dedupe key (ADR §4): the draft's own Message-ID when it has one —
	// Moov-assembled drafts always do — minted here otherwise, and stored on
	// the row so every re-preparation after a crash reuses it.
	if len(email.MessageID) > 0 && email.MessageID[0] != "" {
		spec.MessageRFCID = email.MessageID[0]
	} else {
		asm, aerr := newAssembler(nil, nil)
		if aerr != nil {
			return zero, &setError{Type: setErrServerFail, Description: "minting a Message-ID failed"}
		}
		id, aerr := asm.newMessageID(caller.Email)
		if aerr != nil {
			return zero, &setError{Type: setErrServerFail, Description: "minting a Message-ID failed"}
		}
		spec.MessageRFCID = id
	}

	row, err := d.Submissions.Enqueue(ctx, caller.AccountID, spec)
	if err != nil {
		return zero, &setError{Type: setErrServerFail, Description: "enqueuing the submission failed"}
	}
	return row, nil
}

// invalidRecipients returns the addresses that cannot be sent to: not an
// addr-spec, or non-ASCII (SMTPUTF8 is deliberately out of scope —
// internal/submit's client doc; refusing here is what makes that a create
// error rather than a delivery surprise).
func invalidRecipients(rcpts []string) []string {
	var bad []string
	for _, r := range rcpts {
		if !validRecipient(r) {
			bad = append(bad, r)
		}
	}
	return bad
}

func validRecipient(addr string) bool {
	if addr == "" {
		return false
	}
	for i := 0; i < len(addr); i++ {
		if addr[i] < '!' || addr[i] > '~' {
			return false // control, space, or non-ASCII
		}
	}
	parsed, err := mail.ParseAddress(addr)
	if err != nil || parsed.Address != addr {
		return false // not a bare addr-spec this envelope can carry
	}
	return strings.Count(addr, "@") == 1
}

// applySubmissionUpdate applies one §7.5 update: the only mutable property is
// undoStatus, and the only legal transition is "pending" -> "canceled".
func (d *Deps) applySubmissionUpdate(ctx context.Context, accountID int64, wire string, raw json.RawMessage) (SubmissionRow, *setError) {
	var zero SubmissionRow
	id, err := DecodeSubmissionID(wire)
	if err != nil {
		return zero, &setError{Type: setErrNotFound}
	}

	var patch map[string]json.RawMessage
	if err := json.Unmarshal(raw, &patch); err != nil || patch == nil {
		return zero, &setError{Type: setErrInvalidProperties,
			Description: "an update must be a PatchObject (RFC 8620 §5.3)"}
	}
	var wantCancel bool
	var badProps []string
	for key, val := range patch {
		if key != "undoStatus" {
			badProps = append(badProps, key)
			continue
		}
		var status string
		if err := json.Unmarshal(val, &status); err != nil || status != "canceled" {
			badProps = append(badProps, key)
			continue
		}
		wantCancel = true
	}
	if len(badProps) > 0 {
		sort.Strings(badProps)
		return zero, &setError{Type: setErrInvalidProperties, Properties: badProps,
			Description: `only undoStatus may be updated, and only to "canceled" (RFC 8621 §7.5)`}
	}
	if !wantCancel {
		return zero, &setError{Type: setErrInvalidProperties,
			Description: "the update changes nothing this server can change"}
	}

	row, err := d.Submissions.Cancel(ctx, accountID, id)
	switch {
	case err == nil:
		d.observeCancel(row)
		return row, nil
	case errors.Is(err, ErrNotFound):
		return zero, &setError{Type: setErrNotFound}
	case errors.Is(err, ErrCannotUnsend):
		// §7.5: "cannotUnsend: The client attempted to update the undoStatus
		// of a valid EmailSubmission object from 'pending' to 'canceled', but
		// the message cannot be unsent."
		return zero, &setError{Type: setErrCannotUnsend,
			Description: "the undo window has passed and the message is being (or has been) sent"}
	default:
		return zero, &setError{Type: setErrServerFail, Description: "canceling the submission failed"}
	}
}

// applySubmissionDestroy tombstones one record (see the file header for the
// documented W-A3 cancel-if-pending deviation, which lives in the store's
// single-statement destroy).
func (d *Deps) applySubmissionDestroy(ctx context.Context, accountID int64, wire string) (SubmissionRow, *setError) {
	var zero SubmissionRow
	id, err := DecodeSubmissionID(wire)
	if err != nil {
		return zero, &setError{Type: setErrNotFound}
	}
	row, err := d.Submissions.Destroy(ctx, accountID, id)
	switch {
	case err == nil:
		// Only a destroy that CANCELED counts as an undo (the W-A3 deviation
		// path). Tombstoning an already-final record is record-keeping, and
		// counting it would report undos the user never performed.
		d.observeCancel(row)
		return row, nil
	case errors.Is(err, ErrNotFound):
		return zero, &setError{Type: setErrNotFound}
	default:
		return zero, &setError{Type: setErrServerFail, Description: "destroying the submission failed"}
	}
}

// observeCancel counts one undo, when the row really ended up canceled.
//
// The guard is the undoStatus rather than a "did this call change anything"
// signal, because the store's cancel is deliberately idempotent: replaying a
// cancel on an already-canceled row is a success, not an error (a client that
// retries a request whose response it lost must not see a spurious failure).
// The cost of that idempotency is that a replayed cancel is indistinguishable
// here from the original, so a client that retries its undo can count two.
// That is the honest trade and it is the right way round: the alternative —
// having the store report "already canceled" as a distinct outcome — would put
// a metrics concern into a correctness path.
func (d *Deps) observeCancel(row SubmissionRow) {
	if d.SubmissionObserver == nil || row.UndoStatus != "canceled" {
		return
	}
	d.SubmissionObserver.SubmissionCanceled()
}

// ---------------------------------------------------------------------------
// EmailSubmission/changes
// ---------------------------------------------------------------------------

// handleSubmissionChanges implements EmailSubmission/changes (RFC 8620 §5.2),
// on the same cursor grammar and the same three coalescing rules as
// Email/changes — the feed is the intent rows' updated_at, and the tombstone
// is destroyed_at.
func (d *Deps) handleSubmissionChanges(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseChanges(ctx, args)
	if merr != nil {
		return nil, merr
	}
	since, merr := cursorFromState(req.SinceState)
	if merr != nil {
		return nil, merr
	}

	limit := defaultChangesLimit
	if req.MaxChanges != nil {
		capped := min(*req.MaxChanges, uint64(maxChangesCeiling))
		limit = int(capped) //nolint:gosec // capped to maxChangesCeiling on the line above
	}

	rows, err := d.Submissions.SubmissionsChangedSince(ctx, caller.AccountID, since, limit+1)
	if err != nil {
		return nil, serverFail("reading submission changes", err)
	}

	resp := newChangesResponse(req.AccountID, req.SinceState)
	hasMore := false
	if len(rows) > limit {
		rows = rows[:limit]
		hasMore = true
	}
	for _, r := range rows {
		created := since.IsZero() || r.CreatedAt.After(since)
		switch {
		case created && r.Destroyed:
			// §5.2: created AND destroyed since the old state — omitted.
		case r.Destroyed:
			resp.Destroyed = append(resp.Destroyed, EncodeSubmissionID(r.ID))
		case created:
			resp.Created = append(resp.Created, EncodeSubmissionID(r.ID))
		default:
			resp.Updated = append(resp.Updated, EncodeSubmissionID(r.ID))
		}
	}
	resp.HasMoreChanges = hasMore
	if hasMore {
		resp.NewState = stateForCursor(rows[len(rows)-1].UpdatedAt)
	} else {
		state, err := d.Submissions.SubmissionState(ctx, caller.AccountID)
		if err != nil {
			return nil, serverFail("reading submission state", err)
		}
		resp.NewState = state
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Identity (§6)
// ---------------------------------------------------------------------------

// identityState is the constant Identity state: the set never changes while
// the account exists, and a constant is the honest cursor for /changes.
const identityState = "0-identity"

// handleIdentityGet implements Identity/get (§6.1): the one server-defined
// identity, the account's own address.
func (d *Deps) handleIdentityGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}

	resp := newGetResponse(req.AccountID, identityState)
	include := req.IDs == nil
	if req.IDs != nil {
		for _, wire := range *req.IDs {
			if wire == identityID {
				include = true
			} else {
				resp.NotFound = append(resp.NotFound, wire)
			}
		}
	}
	if include {
		resp.List = append(resp.List, map[string]any{
			"id":    identityID,
			"name":  caller.Email,
			"email": caller.Email,
			// §6.1: null means "the client SHOULD use the value of email";
			// explicit empties would claim configured values that do not exist.
			"replyTo":       nil,
			"bcc":           nil,
			"textSignature": "",
			"htmlSignature": "",
			// The identity cannot be deleted: it IS the account.
			"mayDelete": false,
		})
	}
	sort.Strings(resp.NotFound)
	return resp, nil
}

// handleIdentityChanges implements Identity/changes: nothing ever changes, so
// a matching cursor yields the empty delta and anything else is a state this
// server never issued (§5.2 cannotCalculateChanges).
func (d *Deps) handleIdentityChanges(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, _, merr := parseChanges(ctx, args)
	if merr != nil {
		return nil, merr
	}
	if req.SinceState != identityState {
		return nil, jmap.NewMethodError(jmap.CodeCannotCalculateChanges).
			WithDescription("the given state was not issued by this server")
	}
	resp := newChangesResponse(req.AccountID, req.SinceState)
	resp.NewState = identityState
	return resp, nil
}

// handleIdentitySet refuses every mutation: §6.3 makes server support for
// Identity/set optional, and this server's one identity is derived from the
// account itself — there is nothing a client could truthfully change.
func (d *Deps) handleIdentitySet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	if _, _, merr := parseSet(ctx, args, d.Limits); merr != nil {
		return nil, merr
	}
	return nil, jmap.NewMethodError(jmap.CodeForbidden).
		WithDescription("identities on this server are derived from the account and cannot be created, updated or destroyed (RFC 8621 §6.3 permits this refusal)")
}
