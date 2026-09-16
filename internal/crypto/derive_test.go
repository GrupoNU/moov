package crypto

import (
	"bytes"
	"errors"
	"testing"
)

func TestDeriveIsDeterministicPerKeyAndLabel(t *testing.T) {
	kr := testRing(t, testKey(t, 1, 0xA1))

	a, err := kr.Derive("moov/exports/v1")
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	b, err := kr.Derive("moov/exports/v1")
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("the same key and label derived two different secrets")
	}
	if len(a) != 32 {
		t.Fatalf("derived %d bytes, want 32", len(a))
	}

	// A different label is an independent secret.
	c, err := kr.Derive("moov/other/v1")
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if bytes.Equal(a, c) {
		t.Fatal("two labels derived the same secret")
	}

	// A different primary key is an independent secret: rotation invalidates
	// what the old key signed, by design.
	other := testRing(t, testKey(t, 2, 0xB2), testKey(t, 1, 0xA1))
	d, err := other.Derive("moov/exports/v1")
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if bytes.Equal(a, d) {
		t.Fatal("two primary keys derived the same secret")
	}

	// A ring whose primary is the SAME key derives the same value regardless
	// of which other keys it carries — the secondary keys do not participate.
	same := testRing(t, testKey(t, 1, 0xA1), testKey(t, 2, 0xB2))
	e, err := same.Derive("moov/exports/v1")
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if !bytes.Equal(a, e) {
		t.Fatal("adding a secondary key changed the primary's derivation")
	}
}

func TestDeriveRefusesEmptyLabel(t *testing.T) {
	kr := testRing(t, testKey(t, 1, 0xA1))
	if _, err := kr.Derive(""); !errors.Is(err, ErrEmptyLabel) {
		t.Fatalf("got %v, want ErrEmptyLabel", err)
	}
}

func TestDeriveDoesNotEqualTheMaterial(t *testing.T) {
	// The derived value must be an HMAC output, never the key bytes.
	material := bytes.Repeat([]byte{0xA1}, KeySize)
	kr := testRing(t, testKey(t, 1, 0xA1))
	d, err := kr.Derive("x")
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if bytes.Equal(d, material) {
		t.Fatal("Derive returned the raw key material")
	}
}
