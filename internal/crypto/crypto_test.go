package crypto

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// testKey builds a deterministic, non-zero key for a test. Deterministic
// material is fine here — it never leaves the test binary — and makes failures
// reproducible.
func testKey(t *testing.T, id KeyID, fill byte) Key {
	t.Helper()
	material := bytes.Repeat([]byte{fill}, KeySize)
	k, err := NewKey(id, material)
	if err != nil {
		t.Fatalf("NewKey(%d, %#x...): %v", id, fill, err)
	}
	return k
}

func testRing(t *testing.T, keys ...Key) *Keyring {
	t.Helper()
	kr, err := NewKeyring(keys...)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return kr
}

func TestSealOpenRoundTrip(t *testing.T) {
	kr := testRing(t, testKey(t, 1, 0xA1))
	aad := AccountAAD(42)

	cases := []struct {
		name      string
		plaintext []byte
	}{
		{"empty", []byte{}},
		{"one byte", []byte("x")},
		{"app password", []byte("keyleudecticidechothistishownsan31")},
		{"unicode", []byte("contraseña-ñ-日本語-🔐")},
		{"nul bytes", []byte{0, 1, 0, 2, 0}},
		{"64 KiB", bytes.Repeat([]byte("m"), 64*1024)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sealed, err := kr.Seal(tc.plaintext, aad)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}

			// The plaintext must not be visible in the envelope.
			//
			// Only checked for plaintexts long enough for the check to mean
			// something: a one-byte "plaintext" occurs by chance in ~11% of
			// random 29-byte envelopes, so asserting its absence tests the
			// random number generator's luck rather than the cipher. Four
			// bytes puts the false-positive rate below one in 10^7, and the
			// long cases below carry the real signal anyway.
			const meaningful = 4
			if len(tc.plaintext) >= meaningful && bytes.Contains(sealed, tc.plaintext) {
				t.Fatal("the ciphertext contains the plaintext verbatim")
			}

			got, err := kr.Open(sealed, aad)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !bytes.Equal(got, tc.plaintext) {
				t.Fatalf("round trip mismatch:\n got %q\nwant %q", got, tc.plaintext)
			}
		})
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	// A fresh nonce per seal is the single most important property here:
	// nonce reuse under GCM leaks the authentication key. Two seals of the
	// same plaintext must differ.
	kr := testRing(t, testKey(t, 1, 0xA1))
	aad := AccountAAD(1)
	plaintext := []byte("same-input-every-time")

	const n = 50
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		sealed, err := kr.Seal(plaintext, aad)
		if err != nil {
			t.Fatalf("Seal %d: %v", i, err)
		}
		nonce := string(sealed[headerSize : headerSize+NonceSize])
		if seen[nonce] {
			t.Fatalf("nonce repeated after %d seals", i+1)
		}
		seen[nonce] = true
	}
}

func TestEnvelopeLayout(t *testing.T) {
	kr := testRing(t, testKey(t, 7, 0xB2))
	sealed, err := kr.Seal([]byte("payload"), AccountAAD(3))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if got := sealed[0]; got != EnvelopeV1 {
		t.Errorf("version byte = %d, want %d", got, EnvelopeV1)
	}
	if got := KeyID(sealed[1]); got != 7 {
		t.Errorf("key id byte = %d, want 7", got)
	}
	// header + nonce + plaintext + 16-byte GCM tag.
	if want := headerSize + NonceSize + len("payload") + 16; len(sealed) != want {
		t.Errorf("envelope length = %d, want %d", len(sealed), want)
	}

	id, err := EnvelopeKeyID(sealed)
	if err != nil {
		t.Fatalf("EnvelopeKeyID: %v", err)
	}
	if id != 7 {
		t.Errorf("EnvelopeKeyID = %d, want 7", id)
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	kr := testRing(t, testKey(t, 1, 0xC3))
	aad := AccountAAD(9)
	original, err := kr.Seal([]byte("app-password-value"), aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Every single byte of the envelope is covered: flipping any bit anywhere
	// — key id, nonce, ciphertext or tag — must make Open fail. The version
	// byte is the one exception in KIND of error, not in whether it fails.
	for i := range original {
		t.Run("byte "+itoa(i), func(t *testing.T) {
			tampered := bytes.Clone(original)
			tampered[i] ^= 0x01

			_, err := kr.Open(tampered, aad)
			if err == nil {
				t.Fatalf("Open accepted a ciphertext with byte %d flipped", i)
			}
			switch i {
			case 0:
				// Version byte: reported as a malformed envelope.
				if !errors.Is(err, ErrBadEnvelope) {
					t.Fatalf("flipping the version byte: got %v, want ErrBadEnvelope", err)
				}
			case 1:
				// Key id: the flipped id (1^1 = 0) is the reserved zero, so
				// this build reports a malformed envelope rather than an
				// unknown key. Either is a refusal, which is what matters.
				if !errors.Is(err, ErrBadEnvelope) && !errors.Is(err, ErrUnknownKey) {
					t.Fatalf("flipping the key id: got %v, want ErrBadEnvelope or ErrUnknownKey", err)
				}
			default:
				if !errors.Is(err, ErrDecrypt) {
					t.Fatalf("flipping byte %d: got %v, want ErrDecrypt", i, err)
				}
			}
		})
	}
}

func TestOpenRejectsRelabeledKeyID(t *testing.T) {
	// The key id byte is authenticated. An attacker who holds two ciphertexts
	// must not be able to relabel one as the other's key and have it open,
	// even when both keys are loaded.
	kr := testRing(t, testKey(t, 1, 0xD4), testKey(t, 2, 0xE5))
	aad := AccountAAD(5)

	sealed, err := kr.Seal([]byte("secret"), aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed[1] != 1 {
		t.Fatalf("expected the primary key id 1 in the envelope, got %d", sealed[1])
	}

	relabeled := bytes.Clone(sealed)
	relabeled[1] = 2 // a key that IS loaded

	if _, err := kr.Open(relabeled, aad); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("relabeled envelope: got %v, want ErrDecrypt", err)
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	sealer := testRing(t, testKey(t, 1, 0x11))
	aad := AccountAAD(1)
	sealed, err := sealer.Seal([]byte("app-password"), aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Same key id, different material: this is the "someone restored a
	// database dump against a different deployment's key" case.
	other := testRing(t, testKey(t, 1, 0x22))
	if _, err := other.Open(sealed, aad); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong key material: got %v, want ErrDecrypt", err)
	}

	// Key id not loaded at all: a distinguishable operator error, because the
	// fix is different (re-add the key, do not re-provision).
	missing := testRing(t, testKey(t, 9, 0x11))
	if _, err := missing.Open(sealed, aad); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown key id: got %v, want ErrUnknownKey", err)
	}
}

func TestOpenRejectsWrongAAD(t *testing.T) {
	// This is the property that stops an attacker with database write access
	// from moving account A's credential into account B's row.
	kr := testRing(t, testKey(t, 1, 0x33))

	sealed, err := kr.Seal([]byte("account-1-app-password"), AccountAAD(1))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	for _, aad := range [][]byte{AccountAAD(2), AccountAAD(0), AccountAAD(-1), nil, []byte("")} {
		if _, err := kr.Open(sealed, aad); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("Open with aad %q: got %v, want ErrDecrypt", aad, err)
		}
	}

	// And the correct one still works, so the test is not vacuous.
	if _, err := kr.Open(sealed, AccountAAD(1)); err != nil {
		t.Fatalf("Open with the correct aad: %v", err)
	}
}

func TestAccountAADIsUnambiguous(t *testing.T) {
	// Distinct ids must produce distinct, non-prefix-colliding contexts:
	// AAD is length-covered by GCM, but the values should be plainly
	// different rather than relying on that.
	if got, want := string(AccountAAD(12)), "moov:account:12"; got != want {
		t.Errorf("AccountAAD(12) = %q, want %q", got, want)
	}
	seen := map[string]bool{}
	for _, id := range []int64{0, 1, 2, 12, 120, 1200, -1} {
		s := string(AccountAAD(id))
		if seen[s] {
			t.Fatalf("AccountAAD collision at id %d: %q", id, s)
		}
		seen[s] = true
	}
}

func TestOpenRejectsMalformedEnvelopes(t *testing.T) {
	kr := testRing(t, testKey(t, 1, 0x44))
	aad := AccountAAD(1)
	valid, err := kr.Seal([]byte("x"), aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	cases := []struct {
		name     string
		envelope []byte
		want     error
	}{
		{"nil", nil, ErrBadEnvelope},
		{"empty", []byte{}, ErrBadEnvelope},
		{"one byte", []byte{EnvelopeV1}, ErrBadEnvelope},
		{"header only", []byte{EnvelopeV1, 1}, ErrBadEnvelope},
		{"truncated to one byte short", valid[:len(valid)-1], ErrDecrypt},
		{"header+nonce, no tag", valid[:headerSize+NonceSize], ErrBadEnvelope},
		{"unsupported version", append([]byte{99}, valid[1:]...), ErrBadEnvelope},
		{"reserved key id 0", append([]byte{EnvelopeV1, 0}, valid[2:]...), ErrBadEnvelope},
		{"all zeroes", make([]byte, overhead+4), ErrBadEnvelope},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := kr.Open(tc.envelope, aad)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Open: got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEnvelopeVersionIsPinned(t *testing.T) {
	// A change to the envelope version is a storage format change, and every
	// stored credential is written in the old one. This test exists so that
	// bumping the constant is a deliberate act with a migration attached,
	// rather than a one-character edit that passes CI.
	if EnvelopeV1 != 1 {
		t.Fatalf("EnvelopeV1 = %d: changing the envelope version requires a "+
			"documented migration for every stored credential", EnvelopeV1)
	}
	if headerSize != 2 || NonceSize != 12 || KeySize != 32 {
		t.Fatalf("envelope geometry changed (header=%d nonce=%d key=%d): "+
			"stored ciphertexts assume header=2 nonce=12 key=32",
			headerSize, NonceSize, KeySize)
	}
}

func TestNewKeyRefusesBadKeys(t *testing.T) {
	cases := []struct {
		name     string
		id       KeyID
		material []byte
		want     error
	}{
		{"zero key id", 0, bytes.Repeat([]byte{1}, KeySize), ErrZeroKeyID},
		{"nil material", 1, nil, ErrKeySize},
		{"empty material", 1, []byte{}, ErrKeySize},
		{"16 bytes (AES-128)", 1, bytes.Repeat([]byte{1}, 16), ErrKeySize},
		{"31 bytes", 1, bytes.Repeat([]byte{1}, 31), ErrKeySize},
		{"33 bytes", 1, bytes.Repeat([]byte{1}, 33), ErrKeySize},
		{"all zeroes", 1, make([]byte, KeySize), ErrWeakKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewKey(tc.id, tc.material)
			if !errors.Is(err, tc.want) {
				t.Fatalf("NewKey: got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewKeyringRefusesBadRings(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if _, err := NewKeyring(); !errors.Is(err, ErrNoKey) {
			t.Fatalf("got %v, want ErrNoKey", err)
		}
	})
	t.Run("duplicate id", func(t *testing.T) {
		_, err := NewKeyring(testKey(t, 3, 0x01), testKey(t, 3, 0x02))
		if !errors.Is(err, ErrDuplicateKeyID) {
			t.Fatalf("got %v, want ErrDuplicateKeyID", err)
		}
	})
	t.Run("zero value key", func(t *testing.T) {
		if _, err := NewKeyring(Key{}); !errors.Is(err, ErrZeroKeyID) {
			t.Fatalf("got %v, want ErrZeroKeyID", err)
		}
	})
}

func TestRotation(t *testing.T) {
	// The full documented procedure, step by step.
	old := testKey(t, 1, 0x55)
	oldRing := testRing(t, old)
	aad := AccountAAD(77)
	secret := []byte("provisioned-app-password")

	sealedOld, err := oldRing.Seal(secret, aad)
	if err != nil {
		t.Fatalf("Seal under the old key: %v", err)
	}

	// Step 2: new key becomes primary, old key stays loaded.
	newKey := testKey(t, 2, 0x66)
	rotating := testRing(t, newKey, old)

	if rotating.PrimaryID() != 2 {
		t.Fatalf("PrimaryID = %d, want 2", rotating.PrimaryID())
	}
	// Old ciphertexts still open.
	got, err := rotating.Open(sealedOld, aad)
	if err != nil {
		t.Fatalf("old ciphertext under the rotating ring: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatal("old ciphertext decrypted to the wrong value")
	}

	// Step 3: re-seal.
	resealed, changed, err := rotating.Rotate(sealedOld, aad)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if !changed {
		t.Fatal("Rotate reported no change for a ciphertext under the old key")
	}
	if id, _ := EnvelopeKeyID(resealed); id != 2 {
		t.Fatalf("re-sealed envelope names key %d, want 2", id)
	}
	got, err = rotating.Open(resealed, aad)
	if err != nil {
		t.Fatalf("Open re-sealed: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatal("re-sealed value differs from the original")
	}

	// Idempotent: rotating again is a no-op, byte for byte, which is what
	// makes the pass restartable after a kill.
	again, changed, err := rotating.Rotate(resealed, aad)
	if err != nil {
		t.Fatalf("second Rotate: %v", err)
	}
	if changed {
		t.Fatal("Rotate re-sealed a value already under the primary key")
	}
	if !bytes.Equal(again, resealed) {
		t.Fatal("a no-op Rotate returned a different envelope")
	}

	// Step 4: the old key is dropped. New ciphertexts open; any row missed by
	// step 3 is now reported as ErrUnknownKey rather than failing silently.
	final := testRing(t, newKey)
	if _, err := final.Open(resealed, aad); err != nil {
		t.Fatalf("re-sealed value after dropping the old key: %v", err)
	}
	if _, err := final.Open(sealedOld, aad); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("missed row after dropping the old key: got %v, want ErrUnknownKey", err)
	}
}

func TestRotateRejectsGarbage(t *testing.T) {
	kr := testRing(t, testKey(t, 2, 0x77), testKey(t, 1, 0x88))
	if _, _, err := kr.Rotate([]byte{1, 2, 3}, nil); !errors.Is(err, ErrBadEnvelope) {
		t.Fatalf("got %v, want ErrBadEnvelope", err)
	}

	// A ciphertext under a key that is loaded but with the wrong aad must not
	// be silently re-sealed as garbage.
	sealed, err := kr.Seal([]byte("v"), AccountAAD(1))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, _, err := kr.Rotate(sealed, AccountAAD(2)); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("got %v, want ErrDecrypt", err)
	}
}

func TestKeyringIDs(t *testing.T) {
	kr := testRing(t, testKey(t, 3, 0x01), testKey(t, 1, 0x02), testKey(t, 2, 0x03))
	ids := kr.IDs()
	if len(ids) != 3 {
		t.Fatalf("IDs() = %v, want 3 entries", ids)
	}
	if ids[0] != 3 {
		t.Errorf("IDs()[0] = %d, want the primary 3", ids[0])
	}
}

func TestGenerateKey(t *testing.T) {
	a, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if len(a) != KeySize {
		t.Fatalf("GenerateKey returned %d bytes, want %d", len(a), KeySize)
	}
	b, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two generated keys are identical")
	}
	// A generated key must be immediately usable.
	if _, err := NewKey(1, a); err != nil {
		t.Fatalf("a generated key was rejected: %v", err)
	}
	// And must round-trip through the configuration encoding.
	kr, err := ParseKeyring(EncodeKey(a))
	if err != nil {
		t.Fatalf("ParseKeyring(EncodeKey(generated)): %v", err)
	}
	if kr.PrimaryID() != 1 {
		t.Fatalf("PrimaryID = %d, want 1", kr.PrimaryID())
	}
}

// itoa avoids pulling strconv into the test file for one call site.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestNoSecretsInErrorStrings(t *testing.T) {
	// Errors from this package end up in operator logs. None of them may
	// contain key material or plaintext.
	const secret = "SUPER-SECRET-APP-PASSWORD"
	material := bytes.Repeat([]byte{0x99}, KeySize)
	encoded := EncodeKey(material)

	kr := testRing(t, testKey(t, 1, 0x99))
	sealed, err := kr.Seal([]byte(secret), AccountAAD(1))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, openErr := kr.Open(sealed, AccountAAD(2))
	_, parseErr := ParseKeyring("not-base64-" + encoded)
	_, shortErr := ParseKeyring(EncodeKey([]byte("too short")))

	for _, e := range []error{openErr, parseErr, shortErr} {
		if e == nil {
			t.Fatal("expected an error")
		}
		msg := e.Error()
		if strings.Contains(msg, secret) {
			t.Fatalf("error leaks the plaintext: %s", msg)
		}
		if strings.Contains(msg, encoded) {
			t.Fatalf("error leaks key material: %s", msg)
		}
	}
}
