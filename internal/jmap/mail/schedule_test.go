package mail

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Schedule send (L3 epic E4, canon §2.3, support.google.com/mail/answer
// /9214606).
//
// The three behaviors the canon names, in the order a wrong one hurts most:
//
//  1. CANCEL REVERTS TO DRAFT. This is the one that cannot be repaired by a
//     client and is enforced server side (holdsItsDraft): without it a message
//     scheduled for Friday sits in Sent from Tuesday, telling the user it went
//     out, and is not editable.
//  2. MAX 100 SCHEDULED, refused with §5.3's overQuota.
//  3. The horizon this server advertises is the one it enforces (declared ==
//     applied, the J1 rule).

// scheduleFor renders a UTCDate that far in the future.
func scheduleFor(d time.Duration) string {
	return time.Now().Add(d).UTC().Format(time.RFC3339)
}

// TestScheduledSendUsesTheRequestedInstant proves sendAt reaches the queue as
// the release time rather than being swallowed.
func TestScheduledSendUsesTheRequestedInstant(t *testing.T) {
	_, subs, deps := submissionDeps(t)
	when := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)

	results, merr := deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t,
		map[string]any{
			"identityId": identityID,
			"emailId":    EncodeEmailID(10),
			"sendAt":     when.Format(time.RFC3339),
		}, nil))
	if merr != nil {
		t.Fatalf("EmailSubmission/set: %v", merr)
	}
	resp := firstResult(t, results)
	obj, ok := resp.Created["s1"].(map[string]any)
	if !ok {
		t.Fatalf("the scheduled create was refused: %+v", resp.NotCreated)
	}

	if got := subs.specs[1].SendAt.UTC(); !got.Equal(when) {
		t.Errorf("the spec's SendAt = %v, want %v", got, when)
	}
	if obj["sendAt"] != when.Format("2006-01-02T15:04:05Z") {
		t.Errorf("the created object reports sendAt %v, want %v", obj["sendAt"], when)
	}
	// It is still cancelable — that is what makes "cancel before sendAt" work,
	// and it is why no undo window is added on top of a schedule.
	if obj["undoStatus"] != "pending" {
		t.Errorf("undoStatus = %v, want pending", obj["undoStatus"])
	}
}

// TestScheduledSendKeepsItsDraft is the canon's "cancel reverts to draft",
// enforced where a client cannot get it wrong.
//
// The canonical client flow (web/src/mail/write.ts sendDraft) always sends
// onSuccessUpdateEmail to file the message into Sent. For a scheduled send the
// server must SUPPRESS that implicit Email/set — otherwise the message leaves
// Drafts days before it is sent.
func TestScheduledSendKeepsItsDraft(t *testing.T) {
	f, _, deps := submissionDeps(t)

	// Exactly what the PWA sends, plus a sendAt.
	args := submissionCreateArgs(t,
		map[string]any{
			"identityId": identityID,
			"emailId":    EncodeEmailID(10),
			"sendAt":     scheduleFor(72 * time.Hour),
		},
		map[string]any{
			"onSuccessUpdateEmail": map[string]any{
				"#s1": map[string]any{
					"mailboxIds":      map[string]any{EncodeMailboxID(41): true},
					"keywords/$draft": nil,
				},
			},
		})

	results, merr := deps.handleSubmissionSet(callerCtx(), args)
	if merr != nil {
		t.Fatalf("EmailSubmission/set: %v", merr)
	}
	if resp := firstResult(t, results); resp.Created["s1"] == nil {
		t.Fatalf("the scheduled create was refused: %+v", resp.NotCreated)
	}

	// The §7.5 implicit Email/set must NOT have run.
	for _, r := range results[1:] {
		if r.Name == "Email/set" {
			t.Fatalf("a scheduled submission filed its draft into Sent immediately; "+
				"the canon says a scheduled message stays a draft until it is sent (result %+v)", r.Result)
		}
	}
	if len(f.moveCalls) != 0 {
		t.Errorf("the draft was moved: %+v", f.moveCalls)
	}
	if len(f.flagCalls) != 0 {
		t.Errorf("the draft's keywords were changed: %+v", f.flagCalls)
	}
}

// TestOrdinarySendStillFilesItsDraft is the control for the test above: the
// suppression must be scoped to SCHEDULED submissions, or every send would
// leave its draft behind.
func TestOrdinarySendStillFilesItsDraft(t *testing.T) {
	_, _, deps := submissionDeps(t)

	args := submissionCreateArgs(t,
		map[string]any{"identityId": identityID, "emailId": EncodeEmailID(10)},
		map[string]any{
			"onSuccessUpdateEmail": map[string]any{
				"#s1": map[string]any{"keywords/$draft": nil},
			},
		})

	results, merr := deps.handleSubmissionSet(callerCtx(), args)
	if merr != nil {
		t.Fatalf("EmailSubmission/set: %v", merr)
	}
	found := false
	for _, r := range results[1:] {
		if r.Name == "Email/set" {
			found = true
		}
	}
	if !found {
		t.Error("an ordinary send did not run the §7.5 implicit Email/set; " +
			"the scheduled-send suppression is too broad and every send now leaves a draft")
	}
}

// TestScheduledSendAlsoSuppressesTheDestroy covers the other onSuccess
// argument: destroying a scheduled message's draft would delete mail the user
// still expects to go out and still expects to be able to edit.
func TestScheduledSendAlsoSuppressesTheDestroy(t *testing.T) {
	f, _, deps := submissionDeps(t)

	args := submissionCreateArgs(t,
		map[string]any{
			"identityId": identityID,
			"emailId":    EncodeEmailID(10),
			"sendAt":     scheduleFor(48 * time.Hour),
		},
		map[string]any{"onSuccessDestroyEmail": []any{"#s1"}})

	if _, merr := deps.handleSubmissionSet(callerCtx(), args); merr != nil {
		t.Fatalf("EmailSubmission/set: %v", merr)
	}
	if len(f.destroyCalls) != 0 {
		t.Errorf("a scheduled submission destroyed its own draft: %v", f.destroyCalls)
	}
}

// TestSendAtValidation covers parseSendAt's three answers. The past case is
// the interesting one: it is ACCEPTED as "send now" rather than refused,
// because a client whose clock computed a schedule a moment too late means
// "go", and refusing would turn a harmless skew into a failed send.
func TestSendAtValidation(t *testing.T) {
	t.Run("a malformed timestamp is invalidProperties", func(t *testing.T) {
		_, _, deps := submissionDeps(t)
		results, merr := deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t,
			map[string]any{"identityId": identityID, "emailId": EncodeEmailID(10),
				"sendAt": "next friday"}, nil))
		if merr != nil {
			t.Fatalf("EmailSubmission/set: %v", merr)
		}
		serr, refused := firstResult(t, results).NotCreated["s1"]
		if !refused {
			t.Fatal("a malformed sendAt was accepted")
		}
		if serr.Type != setErrInvalidProperties || len(serr.Properties) != 1 ||
			serr.Properties[0] != "sendAt" {
			t.Errorf("got %q %v, want invalidProperties naming sendAt", serr.Type, serr.Properties)
		}
	})

	t.Run("past the horizon is refused and names the limit", func(t *testing.T) {
		_, _, deps := submissionDeps(t)
		results, merr := deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t,
			map[string]any{"identityId": identityID, "emailId": EncodeEmailID(10),
				"sendAt": scheduleFor(MaxDelayedSend + 24*time.Hour)}, nil))
		if merr != nil {
			t.Fatalf("EmailSubmission/set: %v", merr)
		}
		serr, refused := firstResult(t, results).NotCreated["s1"]
		if !refused {
			t.Fatal("a sendAt past maxDelayedSend was accepted; the advertised limit is not enforced")
		}
		if !contains(serr.Description, "maxDelayedSend") {
			t.Errorf("the refusal does not name the advertised limit: %q", serr.Description)
		}
	})

	t.Run("a sendAt in the past sends now", func(t *testing.T) {
		_, subs, deps := submissionDeps(t)
		results, merr := deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t,
			map[string]any{"identityId": identityID, "emailId": EncodeEmailID(10),
				"sendAt": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}, nil))
		if merr != nil {
			t.Fatalf("EmailSubmission/set: %v", merr)
		}
		if resp := firstResult(t, results); resp.Created["s1"] == nil {
			t.Fatalf("a past sendAt was refused: %+v", resp.NotCreated)
		}
		// It falls back to the ordinary undo window rather than releasing
		// instantly, so the user still gets their undo.
		if !subs.specs[1].SendAt.IsZero() {
			t.Errorf("a past sendAt became a schedule: %v", subs.specs[1].SendAt)
		}
		if subs.windows[0] != DefaultUndoWindow {
			t.Errorf("undo window = %v, want the default", subs.windows[0])
		}
	})
}

// TestScheduledSendCapIsTheCanonsHundred holds the canon's published number,
// refused with the §5.3 type the RFC defines for a server-side count limit.
func TestScheduledSendCapIsTheCanonsHundred(t *testing.T) {
	_, subs, deps := submissionDeps(t)
	future := time.Now().Add(48 * time.Hour)

	// Seed the account at the cap. These are rows, not requests, so the test
	// costs nothing and exercises the boundary exactly.
	for i := 1; i <= MaxScheduledPerAccount(); i++ {
		id := int64(1000 + i)
		subs.rows[id] = &SubmissionRow{
			ID: id, EmailID: 10, UndoStatus: "pending",
			SendAt: future, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
	}

	results, merr := deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t,
		map[string]any{"identityId": identityID, "emailId": EncodeEmailID(10),
			"sendAt": scheduleFor(72 * time.Hour)}, nil))
	if merr != nil {
		t.Fatalf("EmailSubmission/set: %v", merr)
	}
	serr, refused := firstResult(t, results).NotCreated["s1"]
	if !refused {
		t.Fatalf("the %dst scheduled send was accepted; the cap is not enforced",
			MaxScheduledPerAccount()+1)
	}
	if serr.Type != setErrOverQuota {
		t.Errorf("type = %q, want overQuota — RFC 8620 §5.3's own type for "+
			"'the create would exceed a server-defined limit on the number of objects'", serr.Type)
	}

	// An ORDINARY send is unaffected: the cap is on scheduled sends, and
	// counting it against every send would make a busy account unable to mail.
	results, merr = deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t,
		map[string]any{"identityId": identityID, "emailId": EncodeEmailID(10)}, nil))
	if merr != nil {
		t.Fatalf("EmailSubmission/set: %v", merr)
	}
	if resp := firstResult(t, results); resp.Created["s1"] == nil {
		t.Errorf("an ordinary send was refused at the scheduled cap: %+v", resp.NotCreated)
	}
}

// TestScheduledSendCapCountsOnlyLiveFutureReleases keeps the cap from being
// consumed by rows that will never send.
func TestScheduledSendCapCountsOnlyLiveFutureReleases(t *testing.T) {
	_, subs, deps := submissionDeps(t)
	future := time.Now().Add(48 * time.Hour)
	past := time.Now().Add(-time.Hour)

	// One of each kind that must NOT count.
	subs.rows[1001] = &SubmissionRow{ID: 1001, UndoStatus: "canceled", SendAt: future}
	subs.rows[1002] = &SubmissionRow{ID: 1002, UndoStatus: "pending", SendAt: future, Destroyed: true}
	subs.rows[1003] = &SubmissionRow{ID: 1003, UndoStatus: "final", SendAt: past}

	results, merr := deps.handleSubmissionSet(callerCtx(), submissionCreateArgs(t,
		map[string]any{"identityId": identityID, "emailId": EncodeEmailID(10),
			"sendAt": scheduleFor(72 * time.Hour)}, nil))
	if merr != nil {
		t.Fatalf("EmailSubmission/set: %v", merr)
	}
	if resp := firstResult(t, results); resp.Created["s1"] == nil {
		t.Errorf("canceled, destroyed or already-sent submissions consumed the cap: %+v",
			resp.NotCreated)
	}
}

// ---------------------------------------------------------------------------
// EmailSubmission/query — the Scheduled view
// ---------------------------------------------------------------------------

func TestSubmissionQueryFiltersOnUndoStatus(t *testing.T) {
	_, subs, deps := submissionDeps(t)
	future := time.Now().Add(48 * time.Hour)

	subs.rows[1] = &SubmissionRow{ID: 1, UndoStatus: "pending", SendAt: future}
	subs.rows[2] = &SubmissionRow{ID: 2, UndoStatus: "final", SendAt: time.Now().Add(-time.Hour)}
	subs.rows[3] = &SubmissionRow{ID: 3, UndoStatus: "canceled", SendAt: future}
	// A tombstoned record is not in the data set: /get answers notFound for
	// it, so paging to it would hand a client an id it cannot fetch.
	subs.rows[4] = &SubmissionRow{ID: 4, UndoStatus: "pending", SendAt: future, Destroyed: true}

	raw, merr := deps.handleSubmissionQuery(callerCtx(), json.RawMessage(
		`{"accountId":"`+testAccountJMAPID()+`","filter":{"undoStatus":"pending"},"calculateTotal":true}`))
	if merr != nil {
		t.Fatalf("EmailSubmission/query: %v", merr)
	}
	out := mustBe[map[string]any](t, raw)
	ids := mustBe[[]string](t, out["ids"])
	if len(ids) != 1 || ids[0] != EncodeSubmissionID(1) {
		t.Errorf("ids = %v, want only the pending, live submission", ids)
	}
	if out["total"] != 1 {
		t.Errorf("total = %v, want 1 (exact, since the whole set was filtered in memory)", out["total"])
	}
	// §5.5: canCalculateChanges must be truthful, and /queryChanges is not
	// registered for this type.
	if out["canCalculateChanges"] != false {
		t.Errorf("canCalculateChanges = %v, want false", out["canCalculateChanges"])
	}
}

func TestSubmissionQueryRefusesWhatItCannotAnswer(t *testing.T) {
	_, _, deps := submissionDeps(t)

	cases := []struct {
		name string
		args string
		want jmap.ErrorCode
	}{{
		name: "an id filter is unsupportedFilter, named",
		args: `{"accountId":"` + testAccountJMAPID() + `","filter":{"emailIds":["x"]}}`,
		want: jmap.CodeUnsupportedFilter,
	}, {
		name: "a FilterOperator is unsupportedFilter",
		args: `{"accountId":"` + testAccountJMAPID() + `","filter":{"operator":"AND","conditions":[]}}`,
		want: jmap.CodeUnsupportedFilter,
	}, {
		name: "an unknown condition is unsupportedFilter",
		args: `{"accountId":"` + testAccountJMAPID() + `","filter":{"invented":true}}`,
		want: jmap.CodeUnsupportedFilter,
	}, {
		name: "an unsupported sort property is unsupportedSort",
		args: `{"accountId":"` + testAccountJMAPID() + `","sort":[{"property":"emailId"}]}`,
		want: jmap.CodeUnsupportedSort,
	}, {
		name: "an out-of-domain undoStatus is invalidArguments",
		args: `{"accountId":"` + testAccountJMAPID() + `","filter":{"undoStatus":"maybe"}}`,
		want: jmap.CodeInvalidArguments,
	}, {
		name: "a foreign account is accountNotFound, checked first",
		args: `{"accountId":"` + jmap.EncodeAccountID(otherAccountID) + `","filter":{"emailIds":["x"]}}`,
		want: jmap.CodeAccountNotFound,
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, merr := deps.handleSubmissionQuery(callerCtx(), json.RawMessage(c.args))
			if merr == nil {
				t.Fatal("the call succeeded")
			}
			if merr.Code != c.want {
				t.Errorf("got %s (%s), want %s", merr.Code, merr.Description, c.want)
			}
		})
	}
}

// TestSubmissionQuerySortsNewestFirstByDefault pins the server-defined order,
// since §5.5 defines none and both views this serves read newest-first.
func TestSubmissionQuerySortsNewestFirstByDefault(t *testing.T) {
	_, subs, deps := submissionDeps(t)
	base := time.Now().Add(24 * time.Hour)
	for i := 1; i <= 3; i++ {
		subs.rows[int64(i)] = &SubmissionRow{
			ID: int64(i), UndoStatus: "pending", SendAt: base.Add(time.Duration(i) * time.Hour),
		}
	}

	raw, merr := deps.handleSubmissionQuery(callerCtx(), json.RawMessage(
		`{"accountId":"`+testAccountJMAPID()+`"}`))
	if merr != nil {
		t.Fatalf("EmailSubmission/query: %v", merr)
	}
	ids := mustBe[[]string](t, mustBe[map[string]any](t, raw)["ids"])
	if len(ids) != 3 || ids[0] != EncodeSubmissionID(3) {
		t.Errorf("ids = %v, want newest first", ids)
	}

	// isAscending defaults to true when a comparator is given (§5.5).
	raw, merr = deps.handleSubmissionQuery(callerCtx(), json.RawMessage(
		`{"accountId":"`+testAccountJMAPID()+`","sort":[{"property":"sentAt"}]}`))
	if merr != nil {
		t.Fatalf("EmailSubmission/query ascending: %v", merr)
	}
	ids = mustBe[[]string](t, mustBe[map[string]any](t, raw)["ids"])
	if len(ids) != 3 || ids[0] != EncodeSubmissionID(1) {
		t.Errorf("ascending ids = %v, want oldest first", ids)
	}
}
