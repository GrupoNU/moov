package store_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// Tests for the delegated-session rows (migration 0013), against a real
// PostgreSQL. Policy — TTLs, grace, skew — is the HTTP layer's and is tested
// there against a fake store; here the questions are the SQL's: does a hash
// find its row, does an issuer-scoped revoke leave the other issuer alone,
// does the jti insert refuse a second presentation.

func hashOf(t *testing.T) []byte {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return sum[:]
}

func TestDelegatedSessionRoundTrip(t *testing.T) {
	s := testStore(t)
	acct := newAccount(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	hash := hashOf(t)
	created, err := s.CreateDelegatedSession(ctx, store.DelegatedSession{
		TokenHash:         hash,
		AccountID:         acct.ID,
		Issuer:            "https://id.example.test",
		CreatedAt:         now,
		ExpiresAt:         now.Add(12 * time.Hour),
		AbsoluteExpiresAt: now.Add(168 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateDelegatedSession: %v", err)
	}
	if created.ID == 0 || created.AccountID != acct.ID || created.RevokedAt != nil {
		t.Fatalf("created = %+v", created)
	}

	got, err := s.GetDelegatedSession(ctx, hash)
	if err != nil {
		t.Fatalf("GetDelegatedSession: %v", err)
	}
	if got.ID != created.ID || !got.ExpiresAt.Equal(now.Add(12*time.Hour)) {
		t.Fatalf("got = %+v, want id %d", got, created.ID)
	}

	if _, err := s.GetDelegatedSession(ctx, hashOf(t)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown hash: err = %v, want ErrNotFound", err)
	}

	// The sliding expiry moves (renewal grace) and last-seen records use.
	if err := s.SetDelegatedSessionExpiry(ctx, created.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("SetDelegatedSessionExpiry: %v", err)
	}
	if err := s.TouchDelegatedSession(ctx, created.ID, now); err != nil {
		t.Fatalf("TouchDelegatedSession: %v", err)
	}
	got, _ = s.GetDelegatedSession(ctx, hash)
	if !got.ExpiresAt.Equal(now.Add(time.Minute)) || got.LastSeenAt == nil {
		t.Fatalf("after expiry+touch = %+v", got)
	}

	// Revocation is idempotent.
	for range 2 {
		if err := s.RevokeDelegatedSession(ctx, created.ID, now); err != nil {
			t.Fatalf("RevokeDelegatedSession: %v", err)
		}
	}
	got, _ = s.GetDelegatedSession(ctx, hash)
	if got.RevokedAt == nil {
		t.Fatal("session not revoked")
	}
	// A revoked session's expiry can no longer be moved (a renewal that
	// raced a revoke must lose).
	if err := s.SetDelegatedSessionExpiry(ctx, created.ID, now.Add(time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expiry on revoked session: err = %v, want ErrNotFound", err)
	}
}

func TestDelegatedSessionRevokeByIssuerLeavesOtherIssuerAlone(t *testing.T) {
	s := testStore(t)
	acct := newAccount(t, s)
	other := newAccount(t, s)
	ctx := context.Background()
	now := time.Now().UTC()

	mk := func(accountID int64, issuer string) {
		t.Helper()
		_, err := s.CreateDelegatedSession(ctx, store.DelegatedSession{
			TokenHash: hashOf(t), AccountID: accountID, Issuer: issuer,
			CreatedAt: now, ExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	mk(acct.ID, "https://a.example.test")
	mk(acct.ID, "https://a.example.test")
	mk(acct.ID, "https://b.example.test")
	mk(other.ID, "https://a.example.test")

	before, err := s.CountActiveDelegatedSessions(ctx, now)
	if err != nil {
		t.Fatal(err)
	}

	n, err := s.RevokeDelegatedSessionsByIssuer(ctx, acct.ID, "https://a.example.test", now)
	if err != nil {
		t.Fatalf("RevokeDelegatedSessionsByIssuer: %v", err)
	}
	if n != 2 {
		t.Fatalf("revoked %d sessions, want 2 (issuer a of this account only)", n)
	}
	// Idempotent: nothing left to revoke for that pair.
	if n, _ := s.RevokeDelegatedSessionsByIssuer(ctx, acct.ID, "https://a.example.test", now); n != 0 {
		t.Fatalf("second revoke = %d, want 0", n)
	}

	after, _ := s.CountActiveDelegatedSessions(ctx, now)
	if before-after != 2 {
		t.Fatalf("active count moved by %d, want 2", before-after)
	}

	// The account-wide revoke takes the remaining one of THIS account, not the
	// other account's.
	if n, _ := s.RevokeDelegatedSessionsForAccount(ctx, acct.ID, now); n != 1 {
		t.Fatalf("account-wide revoke = %d, want 1", n)
	}
	if n, _ := s.RevokeDelegatedSessionsForAccount(ctx, other.ID, now); n != 1 {
		t.Fatalf("other account still had %d live sessions, want 1", n)
	}
}

func TestDelegatedJTIRefusesReplayAndSweepsExpired(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	issuer := "https://id.example.test/" + sanitizeName(t.Name())
	jti := hashOf(t)
	id := string(jti[:8])

	fresh, err := s.ConsumeDelegatedJTI(ctx, issuer, id, now.Add(5*time.Minute), now)
	if err != nil || !fresh {
		t.Fatalf("first presentation: fresh=%v err=%v", fresh, err)
	}
	fresh, err = s.ConsumeDelegatedJTI(ctx, issuer, id, now.Add(5*time.Minute), now)
	if err != nil || fresh {
		t.Fatalf("replay: fresh=%v err=%v, want false", fresh, err)
	}
	// The same jti under ANOTHER issuer is a different token: ids are only
	// unique within the issuer that minted them.
	if fresh, _ := s.ConsumeDelegatedJTI(ctx, issuer+"-other", id, now.Add(5*time.Minute), now); !fresh {
		t.Fatal("the same jti from another issuer was refused")
	}

	// Once its exp passes the row is swept by the next consume, and the id
	// becomes presentable again — which is correct, because the token
	// carrying it is expired and refused on exp before jti is even checked.
	later := now.Add(10 * time.Minute)
	if fresh, _ := s.ConsumeDelegatedJTI(ctx, issuer, id, later.Add(5*time.Minute), later); !fresh {
		t.Fatal("expired jti row was not swept")
	}
}

func TestDelegatedSessionsCascadeWithAccount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	acct, err := s.CreateAccount(ctx, store.Account{
		Email: "t-cascade-" + sanitizeName(t.Name()) + "@example.test", IMAPHost: "dovecot.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	hash := hashOf(t)
	if _, err := s.CreateDelegatedSession(ctx, store.DelegatedSession{
		TokenHash: hash, AccountID: acct.ID, Issuer: "https://id.example.test",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(ctx, acct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDelegatedSession(ctx, hash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session survived its account: err = %v", err)
	}
}
