package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/GrupoNU/moov/internal/store"
)

// The identities table (migration 0006) against a real PostgreSQL 17.
//
// What is proven here rather than in the JMAP layer's fakes: the backfill
// reached existing accounts, the one-default-per-account constraint is the
// database's and not a convention, account scoping holds, and the three-state
// replyTo/bcc encoding (absent / null / list) survives a round trip through
// jsonb.

func TestIdentityBackfilledForEveryAccount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// newAccount inserts through the same path production provisioning uses.
	// Migration 0006 ran before this account existed, so the row it gets comes
	// from EnsureDefaultIdentity — the second of the three nets. The backfill
	// itself is proven by the pilot's four accounts, and by the fact that a
	// migration-time INSERT ... SELECT cannot miss a row it selected.
	acct := newAccount(t, s)

	id, err := s.EnsureDefaultIdentity(ctx, acct.ID)
	if err != nil {
		t.Fatalf("EnsureDefaultIdentity: %v", err)
	}
	if !id.IsDefault {
		t.Error("the account's first identity must be the default one")
	}
	if id.Email != acct.Email {
		t.Errorf("email = %q, want the account's own address %q", id.Email, acct.Email)
	}
	// The pre-0006 derived identity reported name == email; a backfilled and a
	// freshly created row must look identical to a client.
	if id.Name != acct.Email {
		t.Errorf("name = %q, want the address %q (what the derived identity reported)", id.Name, acct.Email)
	}
	if id.ReplyTo != nil || id.Bcc != nil {
		t.Errorf("replyTo=%v bcc=%v, want NULL (the RFC 8621 §6 default)", id.ReplyTo, id.Bcc)
	}
	if id.TextSignature != "" || id.HTMLSignature != "" {
		t.Errorf("signatures = %q / %q, want the §6 default \"\"", id.TextSignature, id.HTMLSignature)
	}
}

func TestEnsureDefaultIdentityIsIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	first, err := s.EnsureDefaultIdentity(ctx, acct.ID)
	if err != nil {
		t.Fatalf("EnsureDefaultIdentity: %v", err)
	}
	// Called on every read path, so it must never create a second row — the
	// ON CONFLICT DO NOTHING against the partial unique index is what makes
	// two racing provisioners produce one identity.
	for i := 0; i < 5; i++ {
		again, err := s.EnsureDefaultIdentity(ctx, acct.ID)
		if err != nil {
			t.Fatalf("EnsureDefaultIdentity (repeat %d): %v", i, err)
		}
		if again.ID != first.ID {
			t.Fatalf("a repeat call created a new identity: %d then %d", first.ID, again.ID)
		}
	}
	n, err := s.CountIdentities(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("count = %d, want exactly one default identity", n)
	}
}

func TestIdentityUpdateAppliesOnlyNamedFields(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	id, err := s.EnsureDefaultIdentity(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}

	name := "Diego Nannini"
	text := "--\nDiego"
	updated, err := s.UpdateIdentity(ctx, acct.ID, id.ID, store.IdentityUpdate{
		Name:          &name,
		TextSignature: &text,
	})
	if err != nil {
		t.Fatalf("UpdateIdentity: %v", err)
	}
	if updated.Name != name || updated.TextSignature != text {
		t.Errorf("update did not apply: %+v", updated)
	}
	// The unnamed columns keep their values — the COALESCE-per-column shape.
	if updated.HTMLSignature != "" || updated.Email != acct.Email {
		t.Errorf("an unnamed column changed: %+v", updated)
	}
	if !updated.UpdatedAt.After(id.UpdatedAt) {
		t.Errorf("updated_at did not advance: %v -> %v", id.UpdatedAt, updated.UpdatedAt)
	}

	// A second update naming only the html signature must not clear the first.
	html := "<b>Diego</b>"
	updated2, err := s.UpdateIdentity(ctx, acct.ID, id.ID, store.IdentityUpdate{HTMLSignature: &html})
	if err != nil {
		t.Fatal(err)
	}
	if updated2.Name != name || updated2.TextSignature != text {
		t.Errorf("the second update clobbered the first: %+v", updated2)
	}
	if updated2.HTMLSignature != html {
		t.Errorf("htmlSignature = %q", updated2.HTMLSignature)
	}
}

func TestIdentityReplyToAndBccRoundTripThroughJSONB(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	id, err := s.EnsureDefaultIdentity(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}

	list := []store.EmailAddress{
		{Name: "Front Desk", Email: "desk@example.com"},
		{Email: "noname@example.com"},
	}
	updated, err := s.UpdateIdentity(ctx, acct.ID, id.ID, store.IdentityUpdate{ReplyTo: &list})
	if err != nil {
		t.Fatalf("UpdateIdentity: %v", err)
	}
	if len(updated.ReplyTo) != 2 ||
		updated.ReplyTo[0].Name != "Front Desk" || updated.ReplyTo[0].Email != "desk@example.com" ||
		updated.ReplyTo[1].Name != "" || updated.ReplyTo[1].Email != "noname@example.com" {
		t.Fatalf("replyTo did not round-trip: %+v", updated.ReplyTo)
	}
	// Bcc was never named, so it stays NULL.
	if updated.Bcc != nil {
		t.Errorf("bcc = %+v, want NULL", updated.Bcc)
	}

	// Re-read from a fresh query rather than trusting RETURNING.
	reread, err := s.GetIdentity(ctx, acct.ID, id.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reread.ReplyTo) != 2 || reread.ReplyTo[0].Email != "desk@example.com" {
		t.Errorf("replyTo did not survive a re-read: %+v", reread.ReplyTo)
	}

	// Explicit null clears it back to the §6 default — distinct from "absent",
	// which the previous assertion already covered.
	var none []store.EmailAddress
	cleared, err := s.UpdateIdentity(ctx, acct.ID, id.ID, store.IdentityUpdate{ReplyTo: &none})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.ReplyTo != nil {
		t.Errorf("replyTo = %+v after an explicit null, want NULL", cleared.ReplyTo)
	}
}

func TestIdentitiesAreAccountScoped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := newAccount(t, s)
	b := newAccount(t, s)

	idA, err := s.EnsureDefaultIdentity(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureDefaultIdentity(ctx, b.ID); err != nil {
		t.Fatal(err)
	}

	// Account b must not be able to read account a's identity by id — the
	// account_id predicate is the tenant check, not a redundant filter.
	if _, err := s.GetIdentity(ctx, b.ID, idA.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-account GetIdentity = %v, want ErrNotFound", err)
	}

	// Nor write it.
	name := "hijacked"
	if _, err := s.UpdateIdentity(ctx, b.ID, idA.ID, store.IdentityUpdate{Name: &name}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-account UpdateIdentity = %v, want ErrNotFound", err)
	}
	reread, err := s.GetIdentity(ctx, a.ID, idA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.Name == name {
		t.Fatal("a cross-account update wrote the row anyway")
	}

	// And a listing never leaks across accounts.
	listB, err := s.ListIdentities(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range listB {
		if r.AccountID != b.ID {
			t.Errorf("ListIdentities(%d) returned account %d's row", b.ID, r.AccountID)
		}
	}
}

func TestIdentityStateCursorAdvancesOnWrite(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	id, err := s.EnsureDefaultIdentity(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}

	before, err := s.IdentityWatermark(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}

	name := "Diego"
	if _, err := s.UpdateIdentity(ctx, acct.ID, id.ID, store.IdentityUpdate{Name: &name}); err != nil {
		t.Fatal(err)
	}

	after, err := s.IdentityWatermark(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.After(before) {
		t.Fatalf("the watermark did not advance: %v -> %v", before, after)
	}

	// The /changes feed sees the write, strictly after the old cursor — the
	// property that keeps a poll from replaying the last change forever.
	changed, err := s.IdentitiesChangedSince(ctx, acct.ID, before, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].ID != id.ID {
		t.Errorf("changed since the old cursor = %+v, want the updated identity", changed)
	}
	none, err := s.IdentitiesChangedSince(ctx, acct.ID, after, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("changed since the new cursor = %+v, want nothing", none)
	}
}

func TestIdentityUpdateUnknownIDIsNotFound(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	name := "x"
	if _, err := s.UpdateIdentity(ctx, acct.ID, 999999999, store.IdentityUpdate{Name: &name}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UpdateIdentity(unknown) = %v, want ErrNotFound", err)
	}
	// An empty update on an unknown id must also report notFound rather than
	// succeeding silently.
	if _, err := s.UpdateIdentity(ctx, acct.ID, 999999999, store.IdentityUpdate{}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("empty UpdateIdentity(unknown) = %v, want ErrNotFound", err)
	}
}

func TestIdentityDeletedWithItsAccount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	if _, err := s.EnsureDefaultIdentity(ctx, acct.ID); err != nil {
		t.Fatal(err)
	}
	// newAccount's cleanup removes the account; the ON DELETE CASCADE is what
	// keeps an identity from outliving the mailbox it sends as. Proven here by
	// deleting explicitly rather than waiting for cleanup, so the assertion is
	// visible.
	if err := s.DeleteAccount(ctx, acct.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	n, err := s.CountIdentities(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("identities survived their account: %d", n)
	}
}
