package main

import (
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/crypto"
)

// The forwarding token implementation (E6): keyring-sealed, account-bound,
// expiring. These properties are the security design — none is compiler-
// checked, so each is pinned.

func testKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	material, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	key, err := crypto.NewKey(1, material)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	kr, err := crypto.NewKeyring(key)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return kr
}

func TestForwardingTokenRoundTrip(t *testing.T) {
	tok := &forwardingTokens{keyring: testKeyring(t)}
	expires := time.Now().Add(time.Hour)

	token, err := tok.Mint(7, "dest@example.org", expires)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if strings.Contains(token, "dest@example.org") {
		t.Fatal("the token carries the address in cleartext; it must be sealed")
	}
	email, err := tok.Verify(7, token)
	if err != nil || email != "dest@example.org" {
		t.Fatalf("Verify = %q, %v", email, err)
	}
}

// Account binding: a token minted for one account never verifies for
// another — the AAD is what makes a copied token useless elsewhere.
func TestForwardingTokenIsAccountBound(t *testing.T) {
	tok := &forwardingTokens{keyring: testKeyring(t)}
	token, err := tok.Mint(7, "dest@example.org", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := tok.Verify(8, token); err == nil {
		t.Fatal("a token minted for account 7 verified for account 8")
	}
}

func TestForwardingTokenExpires(t *testing.T) {
	tok := &forwardingTokens{keyring: testKeyring(t)}
	token, err := tok.Mint(7, "dest@example.org", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := tok.Verify(7, token); err == nil {
		t.Fatal("an expired token verified")
	}
}

func TestForwardingTokenTamperRefused(t *testing.T) {
	tok := &forwardingTokens{keyring: testKeyring(t)}
	token, err := tok.Mint(7, "dest@example.org", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	tampered := token[:len(token)-2] + "AA"
	if _, err := tok.Verify(7, tampered); err == nil {
		t.Fatal("a tampered token verified")
	}
	if _, err := tok.Verify(7, "not-base64!!"); err == nil {
		t.Fatal("garbage verified")
	}
}

// The verification mail: plain text, Auto-Submitted (no responder loops),
// the token present exactly once, and the consent framing intact.
func TestVerificationMessageShape(t *testing.T) {
	expires := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	raw, err := verificationMessage("owner@moov.example", "dest@other.example", "TOKEN123", expires)
	if err != nil {
		t.Fatalf("verificationMessage: %v", err)
	}
	msg := string(raw)
	for _, want := range []string{
		"From: <owner@moov.example>",
		"To: <dest@other.example>",
		"Auto-Submitted: auto-generated",
		"Message-ID: <moov-fwd-",
		"TOKEN123",
		"If you do not know who this is, ignore this message",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("verification mail missing %q", want)
		}
	}
	if strings.Count(msg, "TOKEN123") != 1 {
		t.Error("the token should appear exactly once")
	}
	if !strings.Contains(msg, "\r\n\r\n") {
		t.Error("no header/body separator")
	}
}
