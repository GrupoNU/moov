package mail

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// EmailSubmission and Identity (W3): the handlers over a fake queue, proving
// the §7 rules — envelope derivation with Bcc, the forbidden* family, the
// undo transitions, the implicit Email/set of §7.5, and the record tombstone.

// fakeSubmissions implements SubmissionStore with the store's semantics.
type fakeSubmissions struct {
	rows    map[int64]*SubmissionRow
	specs   map[int64]SubmissionSpec
	next    int64
	stateN  int
	nowFn   func() time.Time
	enqErr  error
	windows []time.Duration
}

func newFakeSubmissions() *fakeSubmissions {
	return &fakeSubmissions{
		rows:  map[int64]*SubmissionRow{},
		specs: map[int64]SubmissionSpec{},
		nowFn: time.Now,
	}
}

func (f *fakeSubmissions) SubmissionsByID(_ context.Context, _ int64, ids []int64) ([]SubmissionRow, error) {
	var out []SubmissionRow
	for _, id := range ids {
		if r, ok := f.rows[id]; ok {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *fakeSubmissions) ListSubmissions(_ context.Context, _ int64, _ int) ([]SubmissionRow, error) {
	var out []SubmissionRow
	for _, r := range f.rows {
		out = append(out, *r)
	}
	return out, nil
}

func (f *fakeSubmissions) SubmissionState(context.Context, int64) (string, error) {
	return stateFor(time.Unix(int64(1700000000+f.stateN), 0), int64(len(f.rows))), nil
}

func (f *fakeSubmissions) SubmissionsChangedSince(_ context.Context, _ int64, since time.Time, _ int) ([]SubmissionRow, error) {
	var out []SubmissionRow
	for _, r := range f.rows {
		if r.UpdatedAt.After(since) {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *fakeSubmissions) Enqueue(_ context.Context, _ int64, spec SubmissionSpec) (SubmissionRow, error) {
	if f.enqErr != nil {
		return SubmissionRow{}, f.enqErr
	}
	f.next++
	f.stateN++
	f.windows = append(f.windows, spec.UndoWindow)
	now := f.nowFn()
	row := &SubmissionRow{
		ID: f.next, EmailID: spec.EmailID, IdentityID: spec.IdentityID,
		MailFrom: spec.MailFrom, RcptTo: spec.RcptTo,
		SendAt: now.Add(spec.UndoWindow), UndoStatus: "pending",
		CreatedAt: now, UpdatedAt: now,
	}
	f.rows[row.ID] = row
	f.specs[row.ID] = spec
	return *row, nil
}

func (f *fakeSubmissions) Cancel(_ context.Context, _ int64, id int64) (SubmissionRow, error) {
	r, ok := f.rows[id]
	if !ok {
		return SubmissionRow{}, ErrNotFound
	}
	switch r.UndoStatus {
	case "pending":
		r.UndoStatus = "canceled"
		r.UpdatedAt = f.nowFn()
		f.stateN++
		return *r, nil
	case "canceled":
		return *r, nil // idempotent replay
	default:
		return SubmissionRow{}, ErrCannotUnsend
	}
}

func (f *fakeSubmissions) Destroy(_ context.Context, _ int64, id int64) (SubmissionRow, error) {
	r, ok := f.rows[id]
	if !ok {
		return SubmissionRow{}, ErrNotFound
	}
	if r.UndoStatus == "pending" {
		r.UndoStatus = "canceled" // the W-A3 deviation, as the store implements it
	}
	r.Destroyed = true
	r.UpdatedAt = f.nowFn()
	f.stateN++
	return *r, nil
}

// submissionDeps builds Deps with the submission surface mounted, over an
// account holding one sendable draft (id 10) whose From is the caller.
func submissionDeps(t *testing.T) (*fakeReaders, *fakeSubmissions, *Deps) {
	t.Helper()
	f := newFakeReaders()
	f.mailboxes[testAccountID] = []MailboxRow{
		sampleMailbox(31, "Drafts", "drafts", 1, 0),
		sampleMailbox(41, "Sent", "sent", 0, 0),
	}
	draft := sampleEmail(10, "outgoing")
	draft.MailboxIDs = []int64{31}
	draft.Addresses = map[string][]EmailAddress{
		"from": {{Name: "User", Email: "user@example.com"}},
		"to":   {{Email: "to-a@example.test"}, {Email: "to-b@example.test"}},
		"cc":   {{Email: "cc@example.test"}},
		"bcc":  {{Email: "bcc-hidden@example.test"}, {Email: "TO-A@example.test"}},
	}
	f.emails[testAccountID] = []EmailRow{draft}

	subs := newFakeSubmissions()
	deps := f.deps()
	deps.Submissions = subs
	deps.UndoWindow = clampUndoWindow(0)
	return f, subs, deps
}

func submissionCreateArgs(t *testing.T, create map[string]any, extra map[string]any) json.RawMessage {
	t.Helper()
	args := map[string]any{
		"accountId": testAccountJMAPID(),
		"create":    map[string]any{"s1": create},
	}
	for k, v := range extra {
		args[k] = v
	}
	return jsonArgs(t, args)
}

func firstResult(t *testing.T, results []jmap.NamedResult) *setResponse {
	t.Helper()
	if len(results) == 0 || results[0].Name != "EmailSubmission/set" {
		t.Fatalf("results = %+v, want EmailSubmission/set first", results)
	}
	resp, ok := results[0].Result.(*setResponse)
	if !ok {
		t.Fatalf("first result type = %T", results[0].Result)
	}
	return resp
}

// mustBe is the checked form of a type assertion, for test readability and
// the errcheck gate alike.
func mustBe[T any](t *testing.T, v any) T {
	t.Helper()
	out, ok := v.(T)
	if !ok {
		t.Fatalf("value type = %T, want %T", v, out)
	}
	return out
}

// ---------------------------------------------------------------------------
// create
// ---------------------------------------------------------------------------

func TestSubmissionCreateDerivesEnvelopePerRFC(t *testing.T) {
	_, subs, deps := submissionDeps(t)

	results, merr := deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t,
		map[string]any{"identityId": identityID, "emailId": EncodeEmailID(10)}, nil))
	if merr != nil {
		t.Fatalf("EmailSubmission/set: %v", merr)
	}
	resp := firstResult(t, results)
	obj, ok := resp.Created["s1"].(map[string]any)
	if !ok {
		t.Fatalf("create failed: %+v", resp.NotCreated)
	}

	// §7.1.2's derived envelope: mailFrom = the account, rcptTo = To+Cc+Bcc
	// deduplicated case-insensitively — the Bcc recipient IS in the envelope
	// even though the transmitted headers will not carry it (rule 4).
	spec := subs.specs[1]
	if spec.MailFrom != "user@example.com" {
		t.Errorf("mailFrom = %q", spec.MailFrom)
	}
	want := []string{"to-a@example.test", "to-b@example.test", "cc@example.test", "bcc-hidden@example.test"}
	if strings.Join(spec.RcptTo, ",") != strings.Join(want, ",") {
		t.Errorf("rcptTo = %v, want %v (deduplicated, Bcc included)", spec.RcptTo, want)
	}
	if spec.MessageRFCID != "msg-outgoing@example.com" {
		t.Errorf("dedupe key = %q, want the draft's own Message-ID", spec.MessageRFCID)
	}

	// The undo window reached the row (W-A3), and the created answer carries
	// what the undo button needs.
	if subs.windows[0] != DefaultUndoWindow {
		t.Errorf("undo window = %v, want the default %v", subs.windows[0], DefaultUndoWindow)
	}
	if obj["undoStatus"] != "pending" {
		t.Errorf("undoStatus = %v, want pending", obj["undoStatus"])
	}
	if _, ok := obj["sendAt"]; !ok {
		t.Error("created answer lacks sendAt")
	}
}

func TestSubmissionCreateRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		create   map[string]any
		mutate   func(f *fakeReaders)
		wantType string
	}{
		"forbiddenMailFrom": {
			create: map[string]any{
				"identityId": identityID, "emailId": EncodeEmailID(10),
				"envelope": map[string]any{
					"mailFrom": map[string]any{"email": "somebody-else@example.com"},
					"rcptTo":   []map[string]any{{"email": "to-a@example.test"}},
				},
			},
			wantType: setErrForbiddenMailFrom,
		},
		"forbiddenFrom": {
			create: map[string]any{"identityId": identityID, "emailId": EncodeEmailID(10)},
			mutate: func(f *fakeReaders) {
				f.emails[testAccountID][0].Addresses["from"] = []EmailAddress{{Email: "spoofed@example.com"}}
			},
			wantType: setErrForbiddenFrom,
		},
		"noRecipients": {
			create: map[string]any{"identityId": identityID, "emailId": EncodeEmailID(10)},
			mutate: func(f *fakeReaders) {
				f.emails[testAccountID][0].Addresses = map[string][]EmailAddress{
					"from": {{Email: "user@example.com"}},
				}
			},
			wantType: setErrNoRecipients,
		},
		"invalidRecipients non-ascii": {
			create: map[string]any{
				"identityId": identityID, "emailId": EncodeEmailID(10),
				"envelope": map[string]any{
					"mailFrom": map[string]any{"email": "user@example.com"},
					"rcptTo":   []map[string]any{{"email": "señor@example.test"}},
				},
			},
			wantType: setErrInvalidRecipients,
		},
		"invalidRecipients malformed": {
			create: map[string]any{
				"identityId": identityID, "emailId": EncodeEmailID(10),
				"envelope": map[string]any{
					"mailFrom": map[string]any{"email": "user@example.com"},
					"rcptTo":   []map[string]any{{"email": "not-an-address"}},
				},
			},
			wantType: setErrInvalidRecipients,
		},
		"wrong identity": {
			create:   map[string]any{"identityId": "other", "emailId": EncodeEmailID(10)},
			wantType: setErrInvalidProperties,
		},
		"unknown email": {
			create:   map[string]any{"identityId": identityID, "emailId": EncodeEmailID(999)},
			wantType: setErrInvalidProperties,
		},
		"unresolved creation ref": {
			create:   map[string]any{"identityId": identityID, "emailId": "#never-created"},
			wantType: setErrInvalidProperties,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, subs, deps := submissionDeps(t)
			if tc.mutate != nil {
				tc.mutate(f)
			}
			results, merr := deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t, tc.create, nil))
			if merr != nil {
				t.Fatalf("method error: %v", merr)
			}
			resp := firstResult(t, results)
			serr, refused := resp.NotCreated["s1"]
			if !refused {
				t.Fatalf("create succeeded: %+v", resp.Created)
			}
			if serr.Type != tc.wantType {
				t.Errorf("SetError = %q (%s), want %q", serr.Type, serr.Description, tc.wantType)
			}
			if len(subs.rows) != 0 {
				t.Error("a refused create still enqueued")
			}
		})
	}
}

func TestSubmissionCreateTooManyRecipients(t *testing.T) {
	_, _, deps := submissionDeps(t)
	rcpts := make([]map[string]any, maxRecipients+1)
	for i := range rcpts {
		rcpts[i] = map[string]any{"email": strings.ReplaceAll("rcpt-N@example.test", "N", string(rune('a'+i%26))) + string(rune('a'+i/26))}
	}
	results, merr := deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t,
		map[string]any{
			"identityId": identityID, "emailId": EncodeEmailID(10),
			"envelope": map[string]any{
				"mailFrom": map[string]any{"email": "user@example.com"},
				"rcptTo":   rcpts,
			},
		}, nil))
	if merr != nil {
		t.Fatal(merr)
	}
	resp := firstResult(t, results)
	if serr := resp.NotCreated["s1"]; serr.Type != setErrTooManyRecipients {
		t.Errorf("SetError = %q, want tooManyRecipients", serr.Type)
	}
}

// ---------------------------------------------------------------------------
// §7.5's canonical flow: #creation refs + onSuccessUpdateEmail
// ---------------------------------------------------------------------------

func TestSubmissionSetOnSuccessEmitsTheImplicitEmailSet(t *testing.T) {
	f, subs, deps := submissionDeps(t)

	// The draft was created earlier in the request; the creation-id map knows
	// it (the engine seeds this; here the test does).
	created := jmap.NewCreationIDs(nil)
	created.Record("draft", EncodeEmailID(10))
	ctx := jmap.WithCreationIDs(callerCtx(), created)

	// The exact §7.5 example shape: emailId by reference, onSuccess keyed by
	// the SUBMISSION's own creation id, moving the draft to Sent and clearing
	// $draft.
	results, merr := deps.handleSubmissionSet(ctx, submissionCreateArgs(t,
		map[string]any{"identityId": identityID, "emailId": "#draft"},
		map[string]any{
			"onSuccessUpdateEmail": map[string]any{
				"#s1": map[string]any{
					"mailboxIds/" + EncodeMailboxID(41): true,
					"mailboxIds/" + EncodeMailboxID(31): nil,
					"keywords/$draft":                   nil,
				},
			},
		}))
	if merr != nil {
		t.Fatalf("EmailSubmission/set: %v", merr)
	}
	if len(results) != 2 {
		t.Fatalf("got %d responses, want 2 (EmailSubmission/set + the §7.5 implicit Email/set)", len(results))
	}
	if results[1].Name != "Email/set" {
		t.Fatalf("second response = %q, want Email/set", results[1].Name)
	}

	// The submission resolved #draft to the store row.
	if subs.specs[1].EmailID != 10 {
		t.Errorf("emailId resolved to %d, want 10", subs.specs[1].EmailID)
	}

	// The implicit call went through W1's real machinery: the move to Sent
	// and the keyword removal are recorded on the writer.
	implicit := mustBe[*setResponse](t, results[1].Result)
	if _, ok := implicit.Updated[EncodeEmailID(10)]; !ok {
		t.Fatalf("implicit Email/set did not update the draft: %+v", implicit)
	}
	if len(f.moveCalls) != 1 || f.moveCalls[0].mailboxID != 41 {
		t.Errorf("moves = %+v, want one move to Sent (41)", f.moveCalls)
	}
	foundDraftRemoval := false
	for _, c := range f.flagCalls {
		for _, k := range c.change.Remove {
			if k == "draft" {
				foundDraftRemoval = true
			}
		}
	}
	if !foundDraftRemoval {
		t.Errorf("flag calls = %+v; $draft was never removed", f.flagCalls)
	}
}

func TestSubmissionSetOnSuccessSkipsFailedSubmissions(t *testing.T) {
	_, _, deps := submissionDeps(t)
	// The create fails (wrong identity); its onSuccess entry must apply to
	// nothing, and no second response is emitted.
	results, merr := deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t,
		map[string]any{"identityId": "other", "emailId": EncodeEmailID(10)},
		map[string]any{
			"onSuccessUpdateEmail": map[string]any{
				"#s1": map[string]any{"keywords/$draft": nil},
			},
		}))
	if merr != nil {
		t.Fatal(merr)
	}
	if len(results) != 1 {
		t.Fatalf("got %d responses, want 1 (nothing succeeded, no implicit call)", len(results))
	}
}

// ---------------------------------------------------------------------------
// update (undo) and destroy
// ---------------------------------------------------------------------------

func enqueueOne(t *testing.T, deps *Deps) string {
	t.Helper()
	results, merr := deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t,
		map[string]any{"identityId": identityID, "emailId": EncodeEmailID(10)}, nil))
	if merr != nil {
		t.Fatal(merr)
	}
	resp := firstResult(t, results)
	obj, ok := resp.Created["s1"].(map[string]any)
	if !ok {
		t.Fatalf("create failed: %+v", resp.NotCreated)
	}
	return mustBe[string](t, obj["id"])
}

func TestSubmissionUpdateCancelsWithinTheWindow(t *testing.T) {
	_, subs, deps := submissionDeps(t)
	wire := enqueueOne(t, deps)

	results, merr := deps.handleSubmissionSet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
		"update":    map[string]any{wire: map[string]any{"undoStatus": "canceled"}},
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	resp := firstResult(t, results)
	if _, ok := resp.Updated[wire]; !ok {
		t.Fatalf("cancel refused: %+v", resp.NotUpdated)
	}
	if subs.rows[1].UndoStatus != "canceled" {
		t.Errorf("undoStatus = %q", subs.rows[1].UndoStatus)
	}
}

func TestSubmissionUpdateAfterWindowIsCannotUnsend(t *testing.T) {
	_, subs, deps := submissionDeps(t)
	wire := enqueueOne(t, deps)
	subs.rows[1].UndoStatus = "final" // the executor claimed it

	results, merr := deps.handleSubmissionSet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
		"update":    map[string]any{wire: map[string]any{"undoStatus": "canceled"}},
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	resp := firstResult(t, results)
	if serr := resp.NotUpdated[wire]; serr.Type != setErrCannotUnsend {
		t.Errorf("SetError = %q, want cannotUnsend (§7.5)", serr.Type)
	}
}

func TestSubmissionUpdateRejectsAnythingButCancel(t *testing.T) {
	_, _, deps := submissionDeps(t)
	wire := enqueueOne(t, deps)

	for name, patch := range map[string]map[string]any{
		"other property":    {"emailId": EncodeEmailID(10)},
		"undo to final":     {"undoStatus": "final"},
		"undo to nonsense":  {"undoStatus": "yes please"},
		"undo wrong type":   {"undoStatus": 7},
		"empty patchobject": {},
	} {
		t.Run(name, func(t *testing.T) {
			results, merr := deps.handleSubmissionSet(callerCtx(), jsonArgs(t, map[string]any{
				"accountId": testAccountJMAPID(),
				"update":    map[string]any{wire: patch},
			}))
			if merr != nil {
				t.Fatal(merr)
			}
			resp := firstResult(t, results)
			if serr := resp.NotUpdated[wire]; serr.Type != setErrInvalidProperties {
				t.Errorf("SetError = %q, want invalidProperties", serr.Type)
			}
		})
	}
}

func TestSubmissionDestroyTombstonesAndGetForgets(t *testing.T) {
	_, subs, deps := submissionDeps(t)
	wire := enqueueOne(t, deps)

	results, merr := deps.handleSubmissionSet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
		"destroy":   []string{wire},
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	resp := firstResult(t, results)
	if len(resp.Destroyed) != 1 || resp.Destroyed[0] != wire {
		t.Fatalf("destroyed = %v (%+v)", resp.Destroyed, resp.NotDestroyed)
	}
	// The W-A3 deviation: destroying a PENDING submission cancels it.
	if subs.rows[1].UndoStatus != "canceled" {
		t.Errorf("undoStatus after destroy = %q, want canceled (W-A3)", subs.rows[1].UndoStatus)
	}

	// /get answers notFound for the tombstone.
	res, merr := deps.handleSubmissionGet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(), "ids": []string{wire},
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	get := mustBe[*getResponse](t, res)
	if len(get.List) != 0 || len(get.NotFound) != 1 {
		t.Errorf("get after destroy: list=%v notFound=%v", get.List, get.NotFound)
	}
}

// ---------------------------------------------------------------------------
// /get and /changes shapes
// ---------------------------------------------------------------------------

func TestSubmissionGetShape(t *testing.T) {
	_, subs, deps := submissionDeps(t)
	wire := enqueueOne(t, deps)
	subs.rows[1].SMTPReply = "250 2.0.0 Ok: queued as ABC"
	subs.rows[1].UndoStatus = "final"

	res, merr := deps.handleSubmissionGet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(), "ids": []string{wire},
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	get := mustBe[*getResponse](t, res)
	if len(get.List) != 1 {
		t.Fatalf("list = %v", get.List)
	}
	obj := mustBe[map[string]any](t, get.List[0])
	if obj["emailId"] != EncodeEmailID(10) || obj["identityId"] != identityID {
		t.Errorf("identity/email = %v / %v", obj["identityId"], obj["emailId"])
	}
	env := mustBe[map[string]any](t, obj["envelope"])
	if mustBe[map[string]any](t, env["mailFrom"])["email"] != "user@example.com" {
		t.Errorf("envelope = %v", env)
	}
	// deliveryStatus: accepted -> delivered "unknown" (Postfix queued it; the
	// end is unknowable without DSNs), smtpReply carried verbatim.
	ds := mustBe[map[string]any](t, obj["deliveryStatus"])
	first := mustBe[map[string]any](t, ds["to-a@example.test"])
	if first["delivered"] != "unknown" || !strings.HasPrefix(mustBe[string](t, first["smtpReply"]), "250") {
		t.Errorf("deliveryStatus = %v", ds)
	}
	if _, ok := obj["dsnBlobIds"].([]string); !ok {
		t.Errorf("dsnBlobIds = %T, want an (empty) array", obj["dsnBlobIds"])
	}

	// An unknown property is §5.1 invalidArguments.
	if _, merr := deps.handleSubmissionGet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(), "properties": []string{"nope"},
	})); merr == nil || merr.Code != jmap.CodeInvalidArguments {
		t.Errorf("unknown property = %v, want invalidArguments", merr)
	}
}

func TestSubmissionChangesClassifies(t *testing.T) {
	_, subs, deps := submissionDeps(t)
	wire := enqueueOne(t, deps)
	_ = wire

	// From the zero cursor the enqueue is a creation.
	res, merr := deps.handleSubmissionChanges(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(), "sinceState": "0-0",
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	ch := mustBe[*changesResponse](t, res)
	if len(ch.Created) != 1 || len(ch.Updated) != 0 {
		t.Errorf("changes after create: %+v", ch)
	}

	// A tombstoned row created before the cursor reports destroyed; one
	// created and destroyed inside the window is omitted (§5.2's rules).
	cursor := subs.rows[1].UpdatedAt.Add(time.Millisecond)
	subs.rows[1].Destroyed = true
	subs.rows[1].UpdatedAt = cursor.Add(time.Second)

	res, merr = deps.handleSubmissionChanges(callerCtx(), jsonArgs(t, map[string]any{
		"accountId":  testAccountJMAPID(),
		"sinceState": stateForCursor(cursor),
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	ch = mustBe[*changesResponse](t, res)
	if len(ch.Destroyed) != 1 {
		t.Errorf("destroyed = %v", ch)
	}

	res, merr = deps.handleSubmissionChanges(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(), "sinceState": "0-0",
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	ch = mustBe[*changesResponse](t, res)
	if len(ch.Created) != 0 || len(ch.Destroyed) != 0 || len(ch.Updated) != 0 {
		t.Errorf("created-and-destroyed within the window must be omitted (§5.2): %+v", ch)
	}
}

// ---------------------------------------------------------------------------
// Identity (§6)
// ---------------------------------------------------------------------------

func TestIdentityGetServesTheAccountIdentity(t *testing.T) {
	_, _, deps := submissionDeps(t)
	res, merr := deps.handleIdentityGet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	get := mustBe[*getResponse](t, res)
	if len(get.List) != 1 {
		t.Fatalf("list = %v", get.List)
	}
	id := mustBe[map[string]any](t, get.List[0])
	if id["id"] != identityID || id["email"] != "user@example.com" || id["mayDelete"] != false {
		t.Errorf("identity = %+v", id)
	}

	// Unknown ids land in notFound, the known one in the list.
	res, merr = deps.handleIdentityGet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(), "ids": []string{identityID, "ghost"},
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	get = mustBe[*getResponse](t, res)
	if len(get.List) != 1 || len(get.NotFound) != 1 || get.NotFound[0] != "ghost" {
		t.Errorf("list=%v notFound=%v", get.List, get.NotFound)
	}
}

func TestIdentitySetIsForbidden(t *testing.T) {
	_, _, deps := submissionDeps(t)
	_, merr := deps.handleIdentitySet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
		"create":    map[string]any{"i2": map[string]any{"email": "alias@example.com"}},
	}))
	if merr == nil || merr.Code != jmap.CodeForbidden {
		t.Errorf("Identity/set = %v, want forbidden (§6.3 permits the refusal)", merr)
	}
}

func TestIdentityChangesIsConstant(t *testing.T) {
	_, _, deps := submissionDeps(t)
	res, merr := deps.handleIdentityChanges(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(), "sinceState": identityState,
	}))
	if merr != nil {
		t.Fatal(merr)
	}
	ch := mustBe[*changesResponse](t, res)
	if len(ch.Created)+len(ch.Updated)+len(ch.Destroyed) != 0 || ch.NewState != identityState {
		t.Errorf("identity changes = %+v", ch)
	}
	if _, merr := deps.handleIdentityChanges(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(), "sinceState": "1234-5",
	})); merr == nil || merr.Code != jmap.CodeCannotCalculateChanges {
		t.Errorf("foreign identity cursor = %v, want cannotCalculateChanges", merr)
	}
}

// ---------------------------------------------------------------------------
// registration truth
// ---------------------------------------------------------------------------

func TestRegisterSubmissionMethodsPanicsWithoutDeps(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("RegisterSubmissionMethods accepted missing deps; a wiring bug must fail at startup")
		}
	}()
	deps := newFakeReaders().deps() // no Submissions
	RegisterSubmissionMethods(jmap.NewRegistry(), deps)
}

func TestUndoWindowClamp(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{
		0:                DefaultUndoWindow,
		2 * time.Second:  MinUndoWindow,
		10 * time.Second: 10 * time.Second,
		5 * time.Minute:  MaxUndoWindow,
	} {
		if got := clampUndoWindow(in); got != want {
			t.Errorf("clampUndoWindow(%v) = %v, want %v", in, got, want)
		}
	}
}
