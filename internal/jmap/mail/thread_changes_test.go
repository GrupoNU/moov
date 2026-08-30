package mail

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Thread/changes — RFC 8621 §3.2 over RFC 8620 §5.2.
//
// This method was a DELIBERATE DECLINE through J3 and L3 epic E1, and L3 epic
// E4 implemented it: migration 0009's threads table supplies the two facts the
// decline rested on being unavailable — a per-conversation created_at, and a
// merge tombstone. handleThreadChanges carries the full argument.
//
// What these tests pin, in order of what would hurt most if it broke:
//
//  1. the §5.2 coalescing rules, one per branch, since a wrong created/updated
//     answer is what corrupts a client cache;
//  2. the honest DEGRADATION when the reader is absent — the pre-E4 refusal,
//     which a deployment on an older schema still gets;
//  3. the ordering of the validation, so the refusal can never become an
//     oracle about somebody else's account.

// TestE1MethodsAreRegistered is the whole point of registering methods that
// only ever refuse, plus the ones E1 and E4 genuinely added.
//
// A method absent from the registry answers `unknownMethod`, which RFC 8620
// §3.6.1 defines as the server not knowing the method name at all — a statement
// about the SERVER, which a client generalizes to the whole surface. A
// registered method makes a statement about the REQUEST, which a client does
// not generalize.
func TestE1MethodsAreRegistered(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	registry := jmap.NewRegistry()
	RegisterQueryMethods(registry, f.deps())

	registered := make(map[string]bool)
	for _, name := range registry.MethodNames() {
		registered[name] = true
	}

	for _, want := range []string{
		// Added by E1: real, and the reason Mailbox/queryChanges below is not
		// the only Mailbox list method a client can reach.
		"Mailbox/query",
		// Answered for real since E4; registered since E1.
		"Thread/changes",
		// Registered to decline. Each one's reasoning lives on its handler.
		"Email/queryChanges",
		"Mailbox/queryChanges",
	} {
		if !registered[want] {
			t.Errorf("%s is not registered, so a client calling it gets unknownMethod — "+
				"which reads as a partial server rather than as a method this one answers or declines", want)
		}
	}
}

// TestThreadChangesAnswersTheCoalescingRules drives all four §5.2 outcomes
// through one call, because the rules are about how a row is CLASSIFIED and
// classifying one wrong is invisible until a client's cache diverges.
func TestThreadChangesAnswersTheCoalescingRules(t *testing.T) {
	cursor := time.Unix(0, 1_000).UTC()
	after := func(d time.Duration) time.Time { return cursor.Add(d) }
	before := cursor.Add(-time.Hour)

	f := &fakeReaders{state: "1-1"}
	f.threadChanges = []ThreadChangeRow{
		// Existed before the cursor, touched after it -> updated.
		{ThreadID: 11, CreatedAt: before, UpdatedAt: after(time.Second)},
		// First appeared after the cursor -> created.
		{ThreadID: 12, CreatedAt: after(time.Second), UpdatedAt: after(2 * time.Second)},
		// Existed before, merged away since -> destroyed.
		{ThreadID: 13, CreatedAt: before, UpdatedAt: after(3 * time.Second), Destroyed: true},
		// Created AND destroyed since the cursor -> omitted entirely, per
		// §5.2: "the server SHOULD remove the id from the response entirely".
		{ThreadID: 14, CreatedAt: after(time.Second), UpdatedAt: after(4 * time.Second), Destroyed: true},
	}

	raw, merr := f.deps().handleThreadChanges(callerCtx(), json.RawMessage(
		`{"accountId":"`+testAccountJMAPID()+`","sinceState":"1000-1"}`))
	if merr != nil {
		t.Fatalf("Thread/changes refused: %v", merr)
	}
	resp, ok := raw.(*changesResponse)
	if !ok {
		t.Fatalf("Thread/changes returned %T, want *changesResponse", raw)
	}

	wantOne := func(label string, got []string, want int64) {
		t.Helper()
		if len(got) != 1 {
			t.Fatalf("%s = %v, want exactly one id", label, got)
		}
		if got[0] != EncodeThreadID(want) {
			t.Errorf("%s = %v, want thread %d", label, got, want)
		}
	}
	wantOne("created", resp.Created, 12)
	wantOne("updated", resp.Updated, 11)
	wantOne("destroyed", resp.Destroyed, 13)

	// Thread 14 must appear nowhere: a client that never saw it must not be
	// told it existed and then that it did not.
	ghost := EncodeThreadID(14)
	for label, list := range map[string][]string{
		"created": resp.Created, "updated": resp.Updated, "destroyed": resp.Destroyed,
	} {
		for _, id := range list {
			if id == ghost {
				t.Errorf("a thread created and destroyed since the cursor appeared in %s", label)
			}
		}
	}
}

// TestThreadChangesPagesAtTheLastRowReturned holds the cursor contract: when a
// response is truncated, newState must name the last row IN IT, or the next
// page either repeats rows or skips them.
func TestThreadChangesPagesAtTheLastRowReturned(t *testing.T) {
	base := time.Unix(0, 1_000).UTC()
	f := &fakeReaders{state: "9999-9"}
	for i := 1; i <= 5; i++ {
		f.threadChanges = append(f.threadChanges, ThreadChangeRow{
			ThreadID:  int64(100 + i),
			CreatedAt: base.Add(-time.Hour),
			UpdatedAt: base.Add(time.Duration(i) * time.Second),
		})
	}

	raw, merr := f.deps().handleThreadChanges(callerCtx(), json.RawMessage(
		`{"accountId":"`+testAccountJMAPID()+`","sinceState":"1000-1","maxChanges":2}`))
	if merr != nil {
		t.Fatalf("Thread/changes refused: %v", merr)
	}
	resp := raw.(*changesResponse) //nolint:errcheck // shape asserted above
	if !resp.HasMoreChanges {
		t.Fatal("hasMoreChanges is false with rows left to return")
	}
	if got := len(resp.Updated); got != 2 {
		t.Fatalf("returned %d ids, want the requested 2", got)
	}
	// The second row's watermark, which is where the next page must resume.
	if want := stateForCursor(base.Add(2 * time.Second)); resp.NewState != want {
		t.Errorf("newState = %q, want %q (the last row RETURNED, not the last row that exists)",
			resp.NewState, want)
	}
}

// TestThreadChangesDeclinesWithoutTheReader is the honest degradation: a
// deployment whose schema predates migration 0009 has no thread rows to read,
// and must say cannotCalculateChanges rather than answer an empty diff — which
// a client would take as "nothing changed" and cache forever.
func TestThreadChangesDeclinesWithoutTheReader(t *testing.T) {
	f := &fakeReaders{state: "1-1"}
	deps := f.deps()
	deps.ThreadChanges = nil

	_, merr := deps.handleThreadChanges(callerCtx(), json.RawMessage(
		`{"accountId":"`+testAccountJMAPID()+`","sinceState":"1-1"}`))
	if merr == nil {
		t.Fatal("Thread/changes answered with no thread-change reader wired")
	}
	if merr.Code != jmap.CodeCannotCalculateChanges {
		t.Fatalf("got %s, want %s — §5.2 defines this code for exactly this situation",
			merr.Code, jmap.CodeCannotCalculateChanges)
	}
	// §5.2's recovery obliges the client to invalidate its cache; the
	// description must name what to resync with, or the refusal reads as a
	// transient failure worth retrying.
	if !contains(merr.Description, "Email/changes") {
		t.Errorf("the refusal does not name the recovery: %q", merr.Description)
	}
}

// TestThreadChangesValidatesBeforeAnswering keeps the method from becoming an
// oracle. A request naming somebody else's account must get accountNotFound —
// not an empty diff, which would confirm the account exists.
func TestThreadChangesValidatesBeforeAnswering(t *testing.T) {
	f := &fakeReaders{state: "1-1"}

	cases := []struct {
		name string
		args string
		want jmap.ErrorCode
	}{{
		name: "a foreign account is accountNotFound",
		args: `{"accountId":"` + jmap.EncodeAccountID(otherAccountID) + `","sinceState":"1-1"}`,
		want: jmap.CodeAccountNotFound,
	}, {
		name: "a missing accountId is invalidArguments",
		args: `{"sinceState":"1-1"}`,
		want: jmap.CodeInvalidArguments,
	}, {
		// §5.2 makes sinceState required.
		name: "a missing sinceState is invalidArguments",
		args: `{"accountId":"` + testAccountJMAPID() + `"}`,
		want: jmap.CodeInvalidArguments,
	}, {
		// A state string this server never issued cannot be a cursor, and §5.2
		// names cannotCalculateChanges for it.
		name: "a foreign state string is cannotCalculateChanges",
		args: `{"accountId":"` + testAccountJMAPID() + `","sinceState":"not-a-cursor"}`,
		want: jmap.CodeCannotCalculateChanges,
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, merr := f.deps().handleThreadChanges(callerCtx(), json.RawMessage(c.args))
			if merr == nil {
				t.Fatal("the call succeeded")
			}
			if merr.Code != c.want {
				t.Errorf("got %s, want %s", merr.Code, c.want)
			}
		})
	}
}

// TestThreadChangesRefusesWithoutACaller is the same authentication guard every
// handler carries: no caller in context is forbidden, never a guess at whose
// mail is being asked about.
func TestThreadChangesRefusesWithoutACaller(t *testing.T) {
	f := &fakeReaders{state: "1-1"}

	_, merr := f.deps().handleThreadChanges(contextNoCaller{}, json.RawMessage(
		`{"accountId":"`+testAccountJMAPID()+`","sinceState":"1-1"}`))
	if merr == nil || merr.Code != jmap.CodeForbidden {
		t.Errorf("got %v, want forbidden", merr)
	}
}
