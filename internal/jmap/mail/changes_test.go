package mail

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Email/changes and Mailbox/changes over the fakes: the RFC 8620 §5.2
// coalescing rules, the maxChanges split, and the cannotCalculateChanges
// horizon.

func changes(t *testing.T, f *fakeReaders, method, args string) map[string]any {
	t.Helper()
	h := f.deps().handleEmailChanges
	if method == "Mailbox/changes" {
		h = f.deps().handleMailboxChanges
	}
	result, merr := h(callerCtx(), json.RawMessage(args))
	if merr != nil {
		t.Fatalf("%s failed: %v", method, merr)
	}
	return reencode(t, result)
}

func changesError(t *testing.T, f *fakeReaders, method, args string) *jmap.MethodError {
	t.Helper()
	h := f.deps().handleEmailChanges
	if method == "Mailbox/changes" {
		h = f.deps().handleMailboxChanges
	}
	result, merr := h(callerCtx(), json.RawMessage(args))
	if merr == nil {
		t.Fatalf("%s unexpectedly succeeded: %+v", method, result)
	}
	return merr
}

func stringsOf(t *testing.T, resp map[string]any, key string) []string {
	t.Helper()
	raw, ok := resp[key].([]any)
	if !ok {
		t.Fatalf("%s is %T, want an array", key, resp[key])
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("%s contains %T, want a string id", key, v)
		}
		out = append(out, s)
	}
	return out
}

// changeAt builds a ChangeRow whose creation and update times are expressed in
// seconds from a fixed base, so a test reads as a timeline.
var changeBase = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

func at(seconds int) time.Time { return changeBase.Add(time.Duration(seconds) * time.Second) }

// stateAt renders the cursor a client would be holding at a point in time —
// the same grammar J2's stateFor emits, which is what /changes consumes.
func stateAt(seconds int) string { return fmt.Sprintf("%d-0", at(seconds).UnixNano()) }

// ---------------------------------------------------------------------------
// the §5.2 coalescing rules
// ---------------------------------------------------------------------------

// Each of the three rules, implemented exactly as written, plus the plain
// created/updated/destroyed cases.
func TestEmailChangesAppliesTheCoalescingRules(t *testing.T) {
	cases := []struct {
		name string
		row  ChangeRow
		// which bucket the id must land in; "" means it must not appear at all
		want string
	}{{
		name: "created after the cursor is created",
		row:  ChangeRow{MessageID: 1, CreatedAt: at(20), UpdatedAt: at(20)},
		want: "created",
	}, {
		// §5.2: "If a record has been created AND updated since the old state,
		// the server SHOULD just return the id in the 'created' list".
		name: "created then updated is reported as created only",
		row:  ChangeRow{MessageID: 2, CreatedAt: at(20), UpdatedAt: at(30)},
		want: "created",
	}, {
		name: "an older message touched after the cursor is updated",
		row:  ChangeRow{MessageID: 3, CreatedAt: at(1), UpdatedAt: at(30)},
		want: "updated",
	}, {
		// §5.2: "If a record has been updated AND destroyed since the old
		// state, the server SHOULD just return the id in the 'destroyed'
		// list".
		name: "an older message tombstoned is destroyed",
		row:  ChangeRow{MessageID: 4, CreatedAt: at(1), UpdatedAt: at(30), Destroyed: true},
		want: "destroyed",
	}, {
		// §5.2: "If a record has been created AND destroyed since the old
		// state, the server SHOULD remove the id from the response entirely."
		name: "created and destroyed within the window appears nowhere",
		row:  ChangeRow{MessageID: 5, CreatedAt: at(20), UpdatedAt: at(30), Destroyed: true},
		want: "",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeReaders()
			f.changes = []ChangeRow{tc.row}

			resp := changes(t, f, "Email/changes", fmt.Sprintf(
				`{"accountId":%q,"sinceState":%q}`, testAccountJMAPID(), stateAt(10)))

			id := EncodeEmailID(tc.row.MessageID)
			for _, bucket := range []string{"created", "updated", "destroyed"} {
				got := stringsOf(t, resp, bucket)
				inBucket := len(got) == 1 && got[0] == id
				switch {
				case bucket == tc.want && !inBucket:
					t.Errorf("%s = %v, want it to contain %s", bucket, got, id)
				case bucket != tc.want && inBucket:
					t.Errorf("%s unexpectedly contains %s", bucket, id)
				}
			}
		})
	}
}

// A client with no prior state (the "0-0" state J2 emits for an empty account)
// has seen nothing, so everything it is told about is created.
func TestEmailChangesFromTheZeroStateReportsEverythingAsCreated(t *testing.T) {
	f := newFakeReaders()
	f.changes = []ChangeRow{
		{MessageID: 1, CreatedAt: at(1), UpdatedAt: at(1)},
		{MessageID: 2, CreatedAt: at(2), UpdatedAt: at(9)},
	}

	resp := changes(t, f, "Email/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":"0-0"}`, testAccountJMAPID()))

	if got := stringsOf(t, resp, "created"); len(got) != 2 {
		t.Errorf("created = %v, want both messages", got)
	}
	if got := stringsOf(t, resp, "updated"); len(got) != 0 {
		t.Errorf("updated = %v, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// maxChanges and intermediate states
// ---------------------------------------------------------------------------

// §5.2: "the server MUST ensure the number of ids returned across 'created',
// 'updated', and 'destroyed' does not exceed this limit" — and when there are
// more, it "SHOULD generate an update to take the client to an intermediate
// state, from which the client can continue to call 'Foo/changes' until it is
// fully up to date."
//
// The test walks the whole window through intermediate states and checks the
// client ends up having seen every change exactly once.
func TestEmailChangesSplitsOnMaxChangesAndResumesExactly(t *testing.T) {
	const total = 25
	f := newFakeReaders()
	for i := 1; i <= total; i++ {
		f.changes = append(f.changes, ChangeRow{
			MessageID: int64(i),
			CreatedAt: at(100 + i), // all created after the cursor
			UpdatedAt: at(100 + i),
		})
	}

	for _, maxChanges := range []int{1, 4, 10, 25, 100} {
		t.Run(fmt.Sprintf("maxChanges=%d", maxChanges), func(t *testing.T) {
			seen := map[string]int{}
			state := stateAt(10)

			for round := 0; round < total+5; round++ {
				resp := changes(t, f, "Email/changes", fmt.Sprintf(
					`{"accountId":%q,"sinceState":%q,"maxChanges":%d}`,
					testAccountJMAPID(), state, maxChanges))

				ids := stringsOf(t, resp, "created")
				if len(ids) > maxChanges {
					t.Fatalf("returned %d ids, exceeding maxChanges=%d", len(ids), maxChanges)
				}
				for _, id := range ids {
					seen[id]++
				}

				// §5.2: oldState is "the 'sinceState' argument echoed back".
				if resp["oldState"] != state {
					t.Errorf("oldState = %v, want the echoed %q", resp["oldState"], state)
				}
				next, _ := resp["newState"].(string)
				if next == "" {
					t.Fatal("newState is empty")
				}
				more, ok := resp["hasMoreChanges"].(bool)
				if !ok {
					t.Fatalf("hasMoreChanges is %T, want a boolean", resp["hasMoreChanges"])
				}
				if !more {
					break
				}
				if next == state {
					t.Fatal("hasMoreChanges is true but newState did not advance; the client would loop forever")
				}
				state = next
			}

			if len(seen) != total {
				t.Errorf("saw %d distinct ids, want %d", len(seen), total)
			}
			for id, n := range seen {
				if n != 1 {
					t.Errorf("id %s was reported %d times; a split must not duplicate", id, n)
				}
			}
		})
	}
}

// §5.2: "If supplied by the client, the value MUST be a positive integer
// greater than 0. If a value outside of this range is given, the server MUST
// reject the call with an 'invalidArguments' error."
func TestEmailChangesRejectsZeroMaxChanges(t *testing.T) {
	f := newFakeReaders()
	merr := changesError(t, f, "Email/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":"0-0","maxChanges":0}`, testAccountJMAPID()))
	if merr.Code != jmap.CodeInvalidArguments {
		t.Errorf("code = %q, want invalidArguments", merr.Code)
	}
}

// When the client is caught up, newState must be the same string a /get would
// hand out — otherwise a client that alternates between the two would ping-pong
// between cursors and resync forever.
func TestEmailChangesCaughtUpAgreesWithTheGetState(t *testing.T) {
	f := newFakeReaders()
	f.changes = []ChangeRow{{MessageID: 1, CreatedAt: at(20), UpdatedAt: at(20)}}

	resp := changes(t, f, "Email/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":%q}`, testAccountJMAPID(), stateAt(10)))

	if resp["hasMoreChanges"] != false {
		t.Errorf("hasMoreChanges = %v, want false", resp["hasMoreChanges"])
	}
	if resp["newState"] != f.state {
		t.Errorf("newState = %v, want the /get state %q", resp["newState"], f.state)
	}
}

// ---------------------------------------------------------------------------
// cannotCalculateChanges
// ---------------------------------------------------------------------------

// A cursor AHEAD of everything the account has recorded was not issued for
// this account, so it cannot be enumerated from.
//
// §5.2: "'cannotCalculateChanges': The server cannot calculate the changes
// from the state string given by the client... The client MUST invalidate its
// Foo cache."
func TestEmailChangesRefusesACursorFromTheFuture(t *testing.T) {
	f := newFakeReaders()
	f.changes = []ChangeRow{{MessageID: 1, CreatedAt: at(10), UpdatedAt: at(10)}}

	merr := changesError(t, f, "Email/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":%q}`, testAccountJMAPID(), stateAt(9999)))
	if merr.Code != jmap.CodeCannotCalculateChanges {
		t.Errorf("code = %q, want cannotCalculateChanges", merr.Code)
	}
}

// THE REGRESSION TEST for the bug this epic's integration run caught: a
// perfectly valid cursor whose changes have all since been touched must NOT be
// refused.
//
// The tempting horizon check — compare the cursor against min(updated_at) —
// fails exactly here, because updated_at is rewritten in place, so touching
// every row moves the minimum PAST a cursor that is merely up to date. That
// would force a full mailbox reload on every routine sync (changes.go
// checkCursorReachable).
func TestEmailChangesAcceptsACursorOlderThanEveryCurrentRow(t *testing.T) {
	f := newFakeReaders()
	// The client synced at t=10. Since then EVERY row has been rewritten, so
	// the oldest surviving change (t=100) is far newer than the cursor.
	f.changes = []ChangeRow{
		{MessageID: 1, CreatedAt: at(1), UpdatedAt: at(100)},
		{MessageID: 2, CreatedAt: at(2), UpdatedAt: at(200)},
	}

	resp := changes(t, f, "Email/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":%q}`, testAccountJMAPID(), stateAt(10)))

	if got := stringsOf(t, resp, "updated"); len(got) != 2 {
		t.Errorf("updated = %v, want both rewritten messages reported as updates", got)
	}
}

// A cursor exactly at the account's watermark is answerable and reports
// nothing further.
func TestEmailChangesAcceptsACursorAtTheWatermark(t *testing.T) {
	f := newFakeReaders()
	f.changes = []ChangeRow{{MessageID: 1, CreatedAt: at(100), UpdatedAt: at(100)}}

	resp := changes(t, f, "Email/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":%q}`, testAccountJMAPID(), stateAt(100)))
	for _, bucket := range []string{"created", "updated", "destroyed"} {
		if got := stringsOf(t, resp, bucket); len(got) != 0 {
			t.Errorf("%s = %v, want empty at the watermark", bucket, got)
		}
	}
}

// A state string this server never issued must be refused, not silently read
// as "from the beginning" — which would hand the client the whole mailbox as
// "created" and look like success.
func TestEmailChangesRefusesAForeignStateString(t *testing.T) {
	f := newFakeReaders()
	for _, state := range []string{"garbage", "abc-1", "-1"} {
		t.Run(state, func(t *testing.T) {
			merr := changesError(t, f, "Email/changes", fmt.Sprintf(
				`{"accountId":%q,"sinceState":%q}`, testAccountJMAPID(), state))
			if merr.Code != jmap.CodeCannotCalculateChanges {
				t.Errorf("code = %q, want cannotCalculateChanges", merr.Code)
			}
		})
	}
}

func TestChangesRequiresSinceState(t *testing.T) {
	f := newFakeReaders()
	merr := changesError(t, f, "Email/changes", fmt.Sprintf(
		`{"accountId":%q}`, testAccountJMAPID()))
	if merr.Code != jmap.CodeInvalidArguments {
		t.Errorf("code = %q, want invalidArguments", merr.Code)
	}
}

func TestChangesRefusesAnotherAccount(t *testing.T) {
	f := newFakeReaders()
	merr := changesError(t, f, "Email/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":"0-0"}`, jmap.EncodeAccountID(otherAccountID)))
	if merr.Code != jmap.CodeAccountNotFound {
		t.Errorf("code = %q, want accountNotFound", merr.Code)
	}
}

// ---------------------------------------------------------------------------
// Mailbox/changes
// ---------------------------------------------------------------------------

// RFC 8621 §2.2: "If only the 'totalEmails', 'unreadEmails', 'totalThreads',
// and/or 'unreadThreads' Mailbox properties have changed since the old state,
// this will be the list of properties that may have changed. If the server is
// unable to tell if only counts have changed, it MUST just be null."
func TestMailboxChangesReportsUpdatedProperties(t *testing.T) {
	t.Run("only counts moved: the property list", func(t *testing.T) {
		f := newFakeReaders()
		f.mailboxCountChanges = []int64{1, 2}

		resp := changes(t, f, "Mailbox/changes", fmt.Sprintf(
			`{"accountId":%q,"sinceState":%q}`, testAccountJMAPID(), stateAt(10)))

		props := stringsOf(t, resp, "updatedProperties")
		want := map[string]bool{
			"totalEmails": true, "unreadEmails": true,
			"totalThreads": true, "unreadThreads": true,
		}
		if len(props) != len(want) {
			t.Fatalf("updatedProperties = %v, want the four count properties", props)
		}
		for _, p := range props {
			if !want[p] {
				t.Errorf("updatedProperties contains %q, which is not a count property", p)
			}
		}
		if got := stringsOf(t, resp, "updated"); len(got) != 2 {
			t.Errorf("updated = %v, want both mailboxes", got)
		}
	})

	t.Run("a mailbox row moved: null, because more than counts changed", func(t *testing.T) {
		f := newFakeReaders()
		f.mailboxCountChanges = []int64{1}
		f.mailboxRowChanges = []int64{1}

		resp := changes(t, f, "Mailbox/changes", fmt.Sprintf(
			`{"accountId":%q,"sinceState":%q}`, testAccountJMAPID(), stateAt(10)))

		if v, present := resp["updatedProperties"]; !present || v != nil {
			t.Errorf("updatedProperties = %v, want null when a mailbox row itself changed", v)
		}
	})

	t.Run("nothing changed: no updated ids", func(t *testing.T) {
		f := newFakeReaders()

		resp := changes(t, f, "Mailbox/changes", fmt.Sprintf(
			`{"accountId":%q,"sinceState":%q}`, testAccountJMAPID(), stateAt(10)))

		if got := stringsOf(t, resp, "updated"); len(got) != 0 {
			t.Errorf("updated = %v, want empty", got)
		}
	})
}

// A mailbox touched both ways must appear once, not twice.
func TestMailboxChangesDeduplicates(t *testing.T) {
	f := newFakeReaders()
	f.mailboxCountChanges = []int64{1, 2}
	f.mailboxRowChanges = []int64{2, 3}

	resp := changes(t, f, "Mailbox/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":%q}`, testAccountJMAPID(), stateAt(10)))

	got := stringsOf(t, resp, "updated")
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Errorf("mailbox %s reported twice", id)
		}
		seen[id] = true
	}
	if len(got) != 3 {
		t.Errorf("updated = %v, want three distinct mailboxes", got)
	}
}

// ---------------------------------------------------------------------------
// queryChanges
// ---------------------------------------------------------------------------

// Both /queryChanges methods exist and decline conformingly (ADR §2). The
// method must EXIST — unknownMethod would read to a client as a broken server.
func TestQueryChangesDeclinesConformingly(t *testing.T) {
	f := newFakeReaders()
	deps := f.deps()

	for name, h := range map[string]func(ctx contextType, args json.RawMessage) (any, *jmap.MethodError){
		"Email/queryChanges":   deps.handleEmailQueryChanges,
		"Mailbox/queryChanges": deps.handleMailboxQueryChanges,
	} {
		t.Run(name, func(t *testing.T) {
			_, merr := h(callerCtx(), json.RawMessage(fmt.Sprintf(
				`{"accountId":%q,"sinceQueryState":"q1"}`, testAccountJMAPID())))
			if merr == nil {
				t.Fatal("queryChanges succeeded; it must decline")
			}
			if merr.Code != jmap.CodeCannotCalculateChanges {
				t.Errorf("code = %q, want cannotCalculateChanges", merr.Code)
			}
		})
	}
}

// The refusal must not become an oracle: another account's id gets
// accountNotFound, which is what every other method answers.
func TestQueryChangesChecksTheAccountBeforeDeclining(t *testing.T) {
	f := newFakeReaders()
	_, merr := f.deps().handleEmailQueryChanges(callerCtx(), json.RawMessage(fmt.Sprintf(
		`{"accountId":%q}`, jmap.EncodeAccountID(otherAccountID))))
	if merr == nil || merr.Code != jmap.CodeAccountNotFound {
		t.Errorf("error = %v, want accountNotFound", merr)
	}
}

// Every method J3 registers must be present under the mail capability, so a
// missing registration fails here rather than at a client's first call.
func TestRegisterQueryMethodsRegistersTheWholeFamily(t *testing.T) {
	f := newFakeReaders()
	registry := jmap.NewRegistry()
	RegisterQueryMethods(registry, f.deps())

	want := []string{
		"Email/changes", "Email/query", "Email/queryChanges",
		"Mailbox/changes", "Mailbox/queryChanges",
	}
	got := map[string]bool{}
	for _, name := range registry.MethodNames() {
		got[name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("method %q was not registered", name)
		}
	}
}
