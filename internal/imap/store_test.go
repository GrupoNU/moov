package imap

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"
)

// The conditional-STORE conflict logic is the one place in this package where
// a bug corrupts data instead of merely failing: a rejected write that reads as
// success leaves Moov's flag state permanently diverged from Dovecot's, and it
// only happens under concurrent modification, so it does not show up in
// ordinary use (S2 H6). These tests exercise that decision directly.
//
// They run against a stub rather than a server, because what is under test is
// the interpretation of the server's answer — including the case where the
// server gives an answer stock go-imap throws away.

func testClient() *client {
	return &client{
		log:      slog.New(slog.DiscardHandler),
		caps:     Capabilities{CapCondStore: {}, CapQResync: {}, CapIdle: {}},
		selected: "INBOX",
	}
}

// stubReader returns a flagReader that answers with a fixed set of states,
// standing in for what the server would report on a read-back.
func stubReader(states ...messageState) flagReader {
	return func(ctx context.Context, _ []UID) ([]messageState, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return states, nil
	}
}

func TestVerifyByReadBackIdentifiesConflicts(t *testing.T) {
	cl := testClient()

	// The caller wrote against modseq 19. Two messages have moved past it —
	// somebody else changed them — and one has not.
	read := stubReader(
		messageState{uid: 8, modSeq: 20},  // changed after 19: the server refused it
		messageState{uid: 9, modSeq: 19},  // unchanged: the write applied
		messageState{uid: 10, modSeq: 25}, // changed after 19: refused
	)

	rejected, err := cl.verifyByReadBack(context.Background(), read, []UID{8, 9, 10}, 19)
	if err != nil {
		t.Fatalf("verifyByReadBack: %v", err)
	}
	if want := []UID{8, 10}; !reflect.DeepEqual(rejected, want) {
		t.Errorf("rejected = %v, want %v", rejected, want)
	}
}

// TestVerifyByReadBackIgnoresVanishedMessages covers a distinction that is easy
// to get wrong. A message the read-back does not find was expunged, not
// refused. Calling it a conflict would send the caller into a retry loop
// against a message that will never come back; the correct channel for that
// news is VANISHED.
func TestVerifyByReadBackIgnoresVanishedMessages(t *testing.T) {
	cl := testClient()
	// UID 9 was asked for but the server returned nothing: it is gone.
	read := stubReader(messageState{uid: 8, modSeq: 20})

	rejected, err := cl.verifyByReadBack(context.Background(), read, []UID{8, 9}, 19)
	if err != nil {
		t.Fatalf("verifyByReadBack: %v", err)
	}
	if want := []UID{8}; !reflect.DeepEqual(rejected, want) {
		t.Errorf("rejected = %v, want %v (an expunged message is not a conflict)", rejected, want)
	}
}

func TestVerifyByReadBackFindsNoConflictWhenNothingMoved(t *testing.T) {
	cl := testClient()
	read := stubReader(
		messageState{uid: 8, modSeq: 19},
		messageState{uid: 9, modSeq: 12},
	)

	rejected, err := cl.verifyByReadBack(context.Background(), read, []UID{8, 9}, 19)
	if err != nil {
		t.Fatalf("verifyByReadBack: %v", err)
	}
	if len(rejected) != 0 {
		t.Errorf("rejected = %v, want none", rejected)
	}
}

func TestVerifyByReadBackHonorsContext(t *testing.T) {
	cl := testClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := cl.verifyByReadBack(ctx, stubReader(), []UID{1}, 1); err == nil {
		t.Error("a canceled context must abort the read-back")
	}
}

func TestStoreFlagsRejectsUnconditionalWriteWithoutCondstore(t *testing.T) {
	cl := testClient()
	cl.caps = Capabilities{CapQResync: {}, CapIdle: {}} // no CONDSTORE

	// Asking for a conditional write on a server that cannot do one must fail
	// rather than quietly downgrade to an unconditional write: the caller's
	// whole reason to pass unchangedSince is that it must not clobber a
	// concurrent change.
	_, err := cl.StoreFlags(context.Background(), []UID{1}, FlagDelta{Op: FlagsAdd, Flags: []string{"seen"}}, 42)
	if err == nil {
		t.Fatal("a conditional STORE without CONDSTORE must fail")
	}
	var missing *MissingCapabilityError
	if !errors.As(err, &missing) {
		t.Errorf("error = %v, want MissingCapabilityError", err)
	}
	if !errors.Is(err, ErrMissingCapability) {
		t.Error("MissingCapabilityError must unwrap to ErrMissingCapability")
	}
}

func TestStoreFlagsEmptyUIDsIsANoOp(t *testing.T) {
	cl := testClient()
	// No connection is set: reaching the network at all would panic or return
	// ErrNotConnected, so a clean nil result proves nothing was issued.
	res, err := cl.StoreFlags(context.Background(), nil, FlagDelta{Op: FlagsAdd}, 0)
	if err != nil {
		t.Fatalf("StoreFlags with no UIDs: %v", err)
	}
	if res.Conflicted() || len(res.Updated) != 0 {
		t.Errorf("result = %+v, want zero", res)
	}
}

func TestStoreResultConflicted(t *testing.T) {
	if (StoreResult{}).Conflicted() {
		t.Error("an empty result must not report a conflict")
	}
	if !(StoreResult{Rejected: []UID{1}}).Conflicted() {
		t.Error("a result with rejected UIDs must report a conflict")
	}
}

func TestFlagOpString(t *testing.T) {
	for op, want := range map[FlagOp]string{
		FlagsAdd: "add", FlagsRemove: "remove", FlagsSet: "set", FlagOp(99): "unknown",
	} {
		if got := op.String(); got != want {
			t.Errorf("FlagOp(%d).String() = %q, want %q", op, got, want)
		}
	}
}

func TestEventKindString(t *testing.T) {
	for kind, want := range map[EventKind]string{
		EventMailboxChanged: "mailbox-changed", EventOverflow: "overflow", EventKind(99): "unknown",
	} {
		if got := kind.String(); got != want {
			t.Errorf("EventKind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}
