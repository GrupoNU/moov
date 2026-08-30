package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// The E6 ledgers (migration 0010) against a real PostgreSQL: the id survives
// a rename, the reconciler prunes and mints, and the verification facts move
// exactly the enforcement input they should.

func TestSieveScriptLedgerSyncAndRename(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	rows, err := s.SyncSieveScripts(ctx, acct.ID, []string{"moov", "sogo"})
	if err != nil {
		t.Fatalf("SyncSieveScripts: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rows)
	}
	byName := map[string]store.SieveScriptRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	moovID := byName["moov"].ID

	// A second sync with the same names mints nothing new.
	rows, err = s.SyncSieveScripts(ctx, acct.ID, []string{"moov", "sogo"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("resync: rows=%d err=%v", len(rows), err)
	}
	for _, r := range rows {
		if r.Name == "moov" && r.ID != moovID {
			t.Errorf("resync changed the id of %q: %d -> %d", r.Name, moovID, r.ID)
		}
	}

	// RFC 9661 §2.1: "id: Id (immutable; server-set)". The rename moves the
	// name; the id stays.
	if err := s.RenameSieveScript(ctx, acct.ID, moovID, "renamed"); err != nil {
		t.Fatalf("RenameSieveScript: %v", err)
	}
	got, err := s.GetSieveScript(ctx, acct.ID, moovID)
	if err != nil {
		t.Fatalf("GetSieveScript: %v", err)
	}
	if got.Name != "renamed" {
		t.Errorf("name = %q after rename", got.Name)
	}

	// The reconciler prunes names the server no longer has.
	rows, err = s.SyncSieveScripts(ctx, acct.ID, []string{"renamed"})
	if err != nil {
		t.Fatalf("SyncSieveScripts prune: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != moovID {
		t.Errorf("after prune rows = %+v, want just id %d", rows, moovID)
	}

	// The count term of the state cursor fell with the prune; the watermark
	// exists.
	n, err := s.CountSieveScripts(ctx, acct.ID)
	if err != nil || n != 1 {
		t.Errorf("count = %d err=%v, want 1", n, err)
	}
	if wm, err := s.SieveScriptWatermark(ctx, acct.ID); err != nil || wm.IsZero() {
		t.Errorf("watermark = %v err=%v", wm, err)
	}
}

func TestSieveScriptSHATouchesOnlyOnChange(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	row, err := s.UpsertSieveScript(ctx, acct.ID, "moov")
	if err != nil {
		t.Fatalf("UpsertSieveScript: %v", err)
	}
	if err := s.SetSieveScriptSHA(ctx, acct.ID, row.ID, "abc"); err != nil {
		t.Fatalf("SetSieveScriptSHA: %v", err)
	}
	after1, _ := s.GetSieveScript(ctx, acct.ID, row.ID)
	if err := s.SetSieveScriptSHA(ctx, acct.ID, row.ID, "abc"); err != nil {
		t.Fatalf("SetSieveScriptSHA same: %v", err)
	}
	after2, _ := s.GetSieveScript(ctx, acct.ID, row.ID)
	if !after1.UpdatedAt.Equal(after2.UpdatedAt) {
		t.Error("an unchanged sha moved the watermark; every /get would push a phantom state change")
	}
}

func TestForwardingVerificationLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	exp := time.Now().Add(48 * time.Hour)
	f, err := s.CreateForwardingAddress(ctx, acct.ID, "dest@example.org", exp)
	if err != nil {
		t.Fatalf("CreateForwardingAddress: %v", err)
	}
	if f.State != store.ForwardingPending || f.TokenExpiresAt == nil {
		t.Errorf("fresh row = %+v, want pending with an expiry", f)
	}

	// A pending address is NOT enforcement input.
	accepted, err := s.AcceptedForwardingAddresses(ctx, acct.ID)
	if err != nil || len(accepted) != 0 {
		t.Errorf("accepted set before verification = %v err=%v, want empty", accepted, err)
	}

	// Duplicate creation is refused (one row per address).
	if _, err := s.CreateForwardingAddress(ctx, acct.ID, "dest@example.org", exp); err == nil {
		t.Error("a duplicate forwarding address was accepted")
	}

	got, err := s.AcceptForwardingAddress(ctx, acct.ID, "dest@example.org")
	if err != nil {
		t.Fatalf("AcceptForwardingAddress: %v", err)
	}
	if got.State != store.ForwardingAccepted || got.VerifiedAt == nil || got.TokenExpiresAt != nil {
		t.Errorf("accepted row = %+v", got)
	}

	// Idempotent: a second accept succeeds and keeps the original moment.
	again, err := s.AcceptForwardingAddress(ctx, acct.ID, "dest@example.org")
	if err != nil {
		t.Fatalf("second accept: %v", err)
	}
	if !again.VerifiedAt.Equal(*got.VerifiedAt) {
		t.Error("a re-accept moved verified_at")
	}

	accepted, err = s.AcceptedForwardingAddresses(ctx, acct.ID)
	if err != nil || !accepted["dest@example.org"] {
		t.Errorf("accepted set = %v err=%v", accepted, err)
	}

	// Destroy removes the enforcement input with the row.
	if err := s.DeleteForwardingAddress(ctx, acct.ID, f.ID); err != nil {
		t.Fatalf("DeleteForwardingAddress: %v", err)
	}
	accepted, _ = s.AcceptedForwardingAddresses(ctx, acct.ID)
	if len(accepted) != 0 {
		t.Error("a destroyed address survived in the accepted set")
	}
}

func TestForwardingIsAccountScoped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a1 := newAccount(t, s)
	a2 := newAccount(t, s)

	f, err := s.CreateForwardingAddress(ctx, a1.ID, "dest@example.org", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateForwardingAddress: %v", err)
	}
	if _, err := s.AcceptForwardingAddress(ctx, a2.ID, "dest@example.org"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-account accept = %v, want ErrNotFound", err)
	}
	if _, err := s.GetForwardingAddress(ctx, a2.ID, f.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-account get = %v, want ErrNotFound", err)
	}
	if err := s.DeleteForwardingAddress(ctx, a2.ID, f.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-account delete = %v, want ErrNotFound", err)
	}
}
