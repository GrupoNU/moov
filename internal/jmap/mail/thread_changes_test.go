package mail

import (
	"encoding/json"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Thread/changes — RFC 8621 §3.2 over RFC 8620 §5.2, registered in L3 epic E1
// as a DELIBERATE DECLINE.
//
// The full reasoning is on handleThreadChanges. What these tests pin is the
// difference between a decline and a gap, because on the wire they look nothing
// alike and a client treats them nothing alike:
//
//   - unknownMethod says "this server is partial"; a client stops trusting the
//     whole surface;
//   - cannotCalculateChanges says "resync"; §5.2 defines the recovery and every
//     conforming client already implements it.

// TestE1MethodsAreRegistered is the whole point of registering methods that
// only ever refuse, plus the one E1 genuinely added.
//
// A method absent from the registry answers `unknownMethod`, which RFC 8620
// §3.6.1 defines as the server not knowing the method name at all — a statement
// about the SERVER, which a client generalizes to the whole surface. A
// registered method that declines makes a statement about the REQUEST, which a
// client does not generalize.
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
		// Registered to decline. Each one's reasoning lives on its handler.
		"Thread/changes",
		"Email/queryChanges",
		"Mailbox/queryChanges",
	} {
		if !registered[want] {
			t.Errorf("%s is not registered, so a client calling it gets unknownMethod — "+
				"which reads as a partial server rather than as a method this one answers or declines", want)
		}
	}
}

// TestThreadChangesDeclinesWithTheRecoveryNamed holds the refusal to being
// actionable: §5.2's cannotCalculateChanges obliges the client to invalidate its
// cache, and the description must say what to resync with.
func TestThreadChangesDeclines(t *testing.T) {
	f := &fakeReaders{state: "1-1"}

	_, merr := f.deps().handleThreadChanges(callerCtx(), json.RawMessage(
		`{"accountId":"`+testAccountJMAPID()+`","sinceState":"1-1"}`))
	if merr == nil {
		t.Fatal("Thread/changes answered; it cannot compute created/destroyed exactly (see the handler)")
	}
	if merr.Code != jmap.CodeCannotCalculateChanges {
		t.Fatalf("got %s, want %s — §5.2 defines this code for exactly this situation",
			merr.Code, jmap.CodeCannotCalculateChanges)
	}
	// The client's prescribed recovery must be named, and the structural reason
	// with it, so the refusal cannot be mistaken for a transient failure.
	for _, want := range []string{"Email/changes", "merge"} {
		if !contains(merr.Description, want) {
			t.Errorf("the refusal does not mention %q: %q", want, merr.Description)
		}
	}
}

// TestThreadChangesValidatesBeforeDeclining keeps the refusal from becoming an
// oracle. A request naming somebody else's account must get accountNotFound —
// not a refusal, which would confirm that the account exists.
func TestThreadChangesValidatesBeforeDeclining(t *testing.T) {
	f := &fakeReaders{state: "1-1"}

	cases := []struct {
		name string
		args string
		want jmap.ErrorCode
	}{{
		name: "a foreign account is accountNotFound, never a refusal",
		args: `{"accountId":"` + jmap.EncodeAccountID(otherAccountID) + `","sinceState":"1-1"}`,
		want: jmap.CodeAccountNotFound,
	}, {
		name: "a missing accountId is invalidArguments",
		args: `{"sinceState":"1-1"}`,
		want: jmap.CodeInvalidArguments,
	}, {
		// §5.2 makes sinceState required; declining without checking would hide
		// a client bug behind a refusal it was going to get anyway.
		name: "a missing sinceState is invalidArguments",
		args: `{"accountId":"` + testAccountJMAPID() + `"}`,
		want: jmap.CodeInvalidArguments,
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
