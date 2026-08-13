package imap

import (
	"context"
	"errors"
	"testing"

	goimap "github.com/emersion/go-imap/v2"
)

// The COPYUID pairing is the one piece of Move where a bug corrupts data
// rather than failing: a wrong (source -> dest) pair rebinds a message row to
// a UID that names a DIFFERENT message. Like the conditional-STORE tests in
// store_test.go, these exercise the decision directly, against constructed
// sets rather than a server.

func uidSet(uids ...uint32) goimap.UIDSet {
	var s goimap.UIDSet
	for _, u := range uids {
		s.AddNum(goimap.UID(u))
	}
	return s
}

func TestPairCopyUIDsPairsPositionally(t *testing.T) {
	// RFC 4315 §3: dest UIDs correspond to source UIDs in order. Ranges are
	// the common wire form, so the pairing must survive expansion.
	got := pairCopyUIDs(uidSet(3, 7, 9), uidSet(101, 102, 103), 3)
	want := map[UID]UID{3: 101, 7: 102, 9: 103}
	if len(got) != len(want) {
		t.Fatalf("mapping = %v, want %v", got, want)
	}
	for s, d := range want {
		if got[s] != d {
			t.Errorf("source %d -> %d, want %d", s, got[s], d)
		}
	}
}

func TestPairCopyUIDsRefusesMismatchedHalves(t *testing.T) {
	// Halves of different lengths cannot be paired; a partial guess would be
	// the corruption the function exists to prevent. nil is the safe answer —
	// the caller tombstones and lets the sync rediscover.
	if got := pairCopyUIDs(uidSet(1, 2), uidSet(50), 2); got != nil {
		t.Errorf("mismatched halves paired anyway: %v", got)
	}
}

func TestPairCopyUIDsRefusesOversizedSets(t *testing.T) {
	// A COPYUID naming more UIDs than were moved is a server answer this
	// client cannot interpret.
	if got := pairCopyUIDs(uidSet(1, 2, 3), uidSet(10, 11, 12), 2); got != nil {
		t.Errorf("oversized set paired anyway: %v", got)
	}
}

func TestPairCopyUIDsRefusesDynamicSets(t *testing.T) {
	// A set containing "*" cannot be expanded (its size depends on server
	// state this client does not have).
	var dyn goimap.UIDSet
	dyn.AddRange(1, 0) // 1:* — 0 is the wildcard in go-imap's model
	if got := pairCopyUIDs(dyn, uidSet(10), 1); got != nil {
		t.Errorf("dynamic set paired anyway: %v", got)
	}
}

func TestPairCopyUIDsRefusesEmptySets(t *testing.T) {
	if got := pairCopyUIDs(uidSet(), uidSet(), 1); got != nil {
		t.Errorf("empty sets produced a mapping: %v", got)
	}
}

// The argument and capability guards, which must fire before the connection
// is touched — same discipline as StoreFlags.

func TestMoveValidatesBeforeTouchingTheConnection(t *testing.T) {
	cl := testClient() // has no goimap connection: any network use would panic

	// Empty UID set: nothing to do, no connection needed.
	if _, err := cl.Move(context.Background(), nil, "Trash"); err != nil {
		t.Errorf("Move with no uids = %v, want nil", err)
	}

	// Missing destination is a caller mistake, reported as such.
	if _, err := cl.Move(context.Background(), []UID{1}, ""); err == nil {
		t.Error("Move with an empty destination did not fail")
	}
}

func TestMoveWithoutMoveOrUIDPlusIsRefused(t *testing.T) {
	// Neither MOVE nor UIDPLUS: the fallback would degrade to a bare EXPUNGE
	// that removes other clients' \Deleted messages. Refusal, not downgrade.
	cl := testClient()
	_, err := cl.Move(context.Background(), []UID{1}, "Trash")
	if !errors.Is(err, ErrMissingCapability) {
		t.Errorf("err = %v, want ErrMissingCapability", err)
	}
}

func TestExpungeRequiresUIDPlus(t *testing.T) {
	cl := testClient()
	err := cl.Expunge(context.Background(), []UID{1})
	if !errors.Is(err, ErrMissingCapability) {
		t.Errorf("err = %v, want ErrMissingCapability (a bare EXPUNGE is never acceptable)", err)
	}

	// And the empty set costs nothing, like everywhere else in the package.
	if err := cl.Expunge(context.Background(), nil); err != nil {
		t.Errorf("Expunge with no uids = %v, want nil", err)
	}
}

func TestExpungeRequiresConnection(t *testing.T) {
	cl := testClient()
	cl.caps[CapUIDPlus] = struct{}{}
	// selected is set by testClient but there is no live connection.
	if err := cl.Expunge(context.Background(), []UID{1}); !errors.Is(err, ErrNotConnected) {
		t.Errorf("err = %v, want ErrNotConnected", err)
	}
}
