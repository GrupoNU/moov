package jmap

import "testing"

func TestAccountIDRoundTrip(t *testing.T) {
	for _, id := range []int64{1, 35, 36, 1000, 1<<62 + 12345} {
		enc := EncodeAccountID(id)
		got, err := DecodeAccountID(enc)
		if err != nil {
			t.Fatalf("id %d (%q): %v", id, enc, err)
		}
		if got != id {
			t.Fatalf("id %d round-tripped to %d", id, got)
		}
	}
}

func TestAccountIDStability(t *testing.T) {
	// The encoding is a client-visible contract: clients cache state keyed by
	// accountId, so the mapping may never change. Pin it.
	if got := EncodeAccountID(1); got != "a1" {
		t.Fatalf("EncodeAccountID(1) = %q; the encoding changed, which breaks every client cache", got)
	}
	if got := EncodeAccountID(37); got != "a11" {
		t.Fatalf("EncodeAccountID(37) = %q", got)
	}
}

func TestDecodeAccountIDRejectsNonCanonical(t *testing.T) {
	for _, s := range []string{
		"", "a", "1", "b1", "A1", "a01", "a-1", "a0", "a 1", "a1x!", "aZZ",
	} {
		if id, err := DecodeAccountID(s); err == nil {
			t.Errorf("%q decoded to %d; must be rejected", s, id)
		}
	}
}
