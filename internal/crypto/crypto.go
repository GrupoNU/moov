package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"strconv"
)

// KeySize is the master key length in bytes. AES-256 by contract (E7): 128-bit
// keys are not offered, because "which AES" is not a knob an operator should be
// able to get wrong.
const KeySize = 32

// NonceSize is the GCM nonce length in bytes — the standard 96-bit nonce, the
// only size crypto/cipher's NewGCM produces and the size with the best-analyzed
// security margin.
const NonceSize = 12

// EnvelopeV1 is the current envelope format version: version byte, key id byte,
// 12-byte nonce, then ciphertext with its appended GCM tag.
const EnvelopeV1 byte = 1

// headerSize is the version and key id bytes that precede the nonce.
const headerSize = 2

// overhead is the smallest a sealed value can be: header, nonce and the GCM
// tag of an empty plaintext.
const overhead = headerSize + NonceSize + 16

// KeyID names one master key within a [Keyring]. It is stored in every
// ciphertext so rotation can tell which key opens which row.
//
// Zero is not a valid id. It is reserved so that a zero-valued byte — the value
// an uninitialized buffer or a truncated read produces — can never be mistaken
// for a legitimate key reference.
type KeyID uint8

// Errors returned by this package. They are sentinels so callers can branch on
// the condition without matching strings.
var (
	// ErrNoKey is returned when the keyring holds no key at all. Moov refuses
	// to start rather than running with credentials it cannot protect.
	ErrNoKey = errors.New("crypto: no master key configured")

	// ErrKeySize is returned for a key that is not exactly KeySize bytes.
	// Short keys are refused loudly instead of being stretched or padded:
	// silently hashing a weak key into 32 bytes would hide the weakness.
	ErrKeySize = errors.New("crypto: master key must be exactly 32 bytes")

	// ErrWeakKey is returned for an all-zero key. It is not a general strength
	// test — there is no such thing for a random string — but the all-zero
	// value is the specific accident that a misconfigured mount, an empty
	// secret or a zeroed buffer produces, and it must never be usable.
	ErrWeakKey = errors.New("crypto: master key is all zeroes")

	// ErrUnknownKey is returned by Open for a ciphertext sealed under a key id
	// the keyring does not hold — the signature of a rotation whose old key was
	// dropped too early.
	ErrUnknownKey = errors.New("crypto: ciphertext names a key that is not loaded")

	// ErrBadEnvelope is returned for a value too short to be an envelope, or
	// one whose version byte this build does not understand.
	ErrBadEnvelope = errors.New("crypto: malformed ciphertext envelope")

	// ErrDecrypt is returned when authentication fails: a tampered ciphertext,
	// the wrong key, or the right ciphertext presented with the wrong
	// additional data (e.g. moved to another account's row).
	//
	// The three cases are deliberately indistinguishable. Telling a caller
	// which one it was is a decryption oracle, and the operator response is the
	// same in every case: the value is not usable, re-provision.
	ErrDecrypt = errors.New("crypto: cannot decrypt (wrong key, tampered data, or wrong context)")

	// ErrDuplicateKeyID is returned when a keyring is built with the same id
	// twice, which would make it ambiguous which key opens a ciphertext.
	ErrDuplicateKeyID = errors.New("crypto: duplicate key id in keyring")

	// ErrZeroKeyID is returned for key id 0, which is reserved.
	ErrZeroKeyID = errors.New("crypto: key id 0 is reserved")
)

// Key is one master key and the AEAD built from it.
//
// The raw key material is kept only inside the cipher.AEAD; nothing in this
// package exposes it again, so a Key cannot be accidentally logged or
// serialized back out.
type Key struct {
	id   KeyID
	aead cipher.AEAD
}

// NewKey builds a Key from raw material, validating length and refusing the
// all-zero value.
//
// The caller's slice is not retained: the key is expanded into the AES schedule
// and the input may be zeroed afterwards.
func NewKey(id KeyID, material []byte) (Key, error) {
	if id == 0 {
		return Key{}, ErrZeroKeyID
	}
	if len(material) != KeySize {
		return Key{}, fmt.Errorf("%w: got %d", ErrKeySize, len(material))
	}
	// subtle.ConstantTimeCompare against a zero array rather than a byte loop
	// with an early exit: the comparison is not secret-dependent here, but
	// using the constant-time primitive by default is the habit worth keeping.
	var zero [KeySize]byte
	if subtle.ConstantTimeCompare(material, zero[:]) == 1 {
		return Key{}, ErrWeakKey
	}

	block, err := aes.NewCipher(material)
	if err != nil {
		// Unreachable: the length was already checked, and that is the only
		// error NewCipher returns.
		return Key{}, fmt.Errorf("crypto: building AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Key{}, fmt.Errorf("crypto: building GCM: %w", err)
	}
	return Key{id: id, aead: aead}, nil
}

// ID returns the key's identifier.
func (k Key) ID() KeyID { return k.id }

// GenerateKey returns KeySize bytes from the system CSPRNG, for `moovctl key
// generate` and for tests.
func GenerateKey() ([]byte, error) {
	b := make([]byte, KeySize)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("crypto: reading random key material: %w", err)
	}
	return b, nil
}

// Keyring is the set of master keys the process holds: one primary, which every
// new seal uses, and any number of retired keys kept only so ciphertexts sealed
// under them still open during a rotation.
//
// A Keyring is immutable after construction and safe for concurrent use.
type Keyring struct {
	primary KeyID
	keys    map[KeyID]Key
}

// NewKeyring builds a keyring whose first key is the primary.
//
// It fails on an empty list, a duplicate id, or a zero id. There is no
// "optional" keyring: a Moov that cannot encrypt must not run.
func NewKeyring(keys ...Key) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, ErrNoKey
	}
	kr := &Keyring{primary: keys[0].id, keys: make(map[KeyID]Key, len(keys))}
	for _, k := range keys {
		if k.id == 0 || k.aead == nil {
			return nil, ErrZeroKeyID
		}
		if _, dup := kr.keys[k.id]; dup {
			return nil, fmt.Errorf("%w: %d", ErrDuplicateKeyID, k.id)
		}
		kr.keys[k.id] = k
	}
	return kr, nil
}

// PrimaryID is the id new seals are written under. Rotation is "make a new key
// primary, then re-seal every row that does not carry this id".
func (kr *Keyring) PrimaryID() KeyID { return kr.primary }

// IDs returns every key id the ring holds, primary first, for diagnostics.
// Key material is never exposed.
func (kr *Keyring) IDs() []KeyID {
	out := make([]KeyID, 0, len(kr.keys))
	out = append(out, kr.primary)
	for id := range kr.keys {
		if id != kr.primary {
			out = append(out, id)
		}
	}
	return out
}

// Seal encrypts plaintext under the primary key, binding it to aad.
//
// The returned envelope is safe to store and safe to log the LENGTH of, but is
// of course never itself logged. aad must be reproduced exactly at Open time;
// for account credentials it is [AccountAAD].
func (kr *Keyring) Seal(plaintext, aad []byte) ([]byte, error) {
	k, ok := kr.keys[kr.primary]
	if !ok {
		return nil, ErrNoKey
	}

	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		// A CSPRNG failure must never degrade into a deterministic nonce:
		// nonce reuse under GCM is catastrophic (it leaks the authentication
		// key). Failing the operation is the only correct response.
		return nil, fmt.Errorf("crypto: reading nonce: %w", err)
	}

	header := [headerSize]byte{EnvelopeV1, byte(k.id)}

	// The header is authenticated by prepending it to the caller's aad, so the
	// version and key id cannot be rewritten without breaking the tag.
	fullAAD := make([]byte, 0, headerSize+len(aad))
	fullAAD = append(fullAAD, header[:]...)
	fullAAD = append(fullAAD, aad...)

	// One allocation for the whole envelope: header ‖ nonce ‖ sealed. Seal
	// appends to its first argument, so passing the header+nonce prefix as the
	// destination writes the ciphertext straight after them.
	out := make([]byte, 0, headerSize+NonceSize+len(plaintext)+k.aead.Overhead())
	out = append(out, header[:]...)
	out = append(out, nonce...)
	return k.aead.Seal(out, nonce, plaintext, fullAAD), nil
}

// Open decrypts an envelope produced by Seal, using whichever loaded key the
// envelope names.
//
// It returns ErrDecrypt for anything that fails authentication, without
// distinguishing the cause; ErrUnknownKey when the named key is simply not
// loaded, which is an operator error with a different fix (re-add the key)
// rather than a compromised value.
func (kr *Keyring) Open(envelope, aad []byte) ([]byte, error) {
	if len(envelope) < overhead {
		return nil, fmt.Errorf("%w: %d bytes is shorter than the minimum %d",
			ErrBadEnvelope, len(envelope), overhead)
	}
	version, keyID := envelope[0], KeyID(envelope[1])
	if version != EnvelopeV1 {
		return nil, fmt.Errorf("%w: unsupported envelope version %d", ErrBadEnvelope, version)
	}
	if keyID == 0 {
		return nil, fmt.Errorf("%w: envelope names reserved key id 0", ErrBadEnvelope)
	}

	k, ok := kr.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("%w: key id %d (loaded: %v)", ErrUnknownKey, keyID, kr.IDs())
	}

	nonce := envelope[headerSize : headerSize+NonceSize]
	sealed := envelope[headerSize+NonceSize:]

	fullAAD := make([]byte, 0, headerSize+len(aad))
	fullAAD = append(fullAAD, envelope[:headerSize]...)
	fullAAD = append(fullAAD, aad...)

	// nil destination: Open must not write plaintext into a buffer the caller
	// might still hold a view of.
	plaintext, err := k.aead.Open(nil, nonce, sealed, fullAAD)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

// EnvelopeKeyID reports which key sealed an envelope, without decrypting it.
//
// This is what makes `moovctl key rotate` cheap: it can tell which rows still
// need re-sealing by reading two bytes, rather than decrypting every row.
func EnvelopeKeyID(envelope []byte) (KeyID, error) {
	if len(envelope) < overhead {
		return 0, fmt.Errorf("%w: %d bytes", ErrBadEnvelope, len(envelope))
	}
	if envelope[0] != EnvelopeV1 {
		return 0, fmt.Errorf("%w: unsupported envelope version %d", ErrBadEnvelope, envelope[0])
	}
	id := KeyID(envelope[1])
	if id == 0 {
		return 0, fmt.Errorf("%w: envelope names reserved key id 0", ErrBadEnvelope)
	}
	return id, nil
}

// Rotate re-seals an envelope under the primary key.
//
// It returns the new envelope and whether anything changed: a value already
// sealed under the primary is returned untouched with changed=false, which is
// what makes the rotation pass idempotent and restartable.
//
// Every envelope is opened, including the ones already under the primary key.
// The cheap alternative — trusting the key id byte and skipping — would let a
// rotation pass walk silently past a row that is corrupt, was sealed for a
// different account, or was tampered with, and report success. A rotation is
// exactly the moment those rows should be found, so the two header bytes decide
// whether to RE-SEAL, never whether to VERIFY.
//
// The plaintext exists only inside this function's frame.
func (kr *Keyring) Rotate(envelope, aad []byte) (out []byte, changed bool, err error) {
	id, err := EnvelopeKeyID(envelope)
	if err != nil {
		return nil, false, err
	}

	plaintext, err := kr.Open(envelope, aad)
	if err != nil {
		return nil, false, err
	}
	defer zero(plaintext)

	if id == kr.primary {
		return envelope, false, nil
	}

	resealed, err := kr.Seal(plaintext, aad)
	if err != nil {
		return nil, false, err
	}
	return resealed, true, nil
}

// AccountAAD is the additional authenticated data binding a credential
// ciphertext to one account row.
//
// Format: "moov:account:<id>". It is a stable, versioned-by-prefix string
// rather than the raw id bytes so that a future second use of this keyring
// (say, sealing an OAuth refresh token) cannot collide with account
// credentials by accident.
//
// Changing this string invalidates every stored credential. It must not be
// changed without a re-provisioning plan.
func AccountAAD(accountID int64) []byte {
	return []byte("moov:account:" + strconv.FormatInt(accountID, 10))
}

// zero overwrites a plaintext buffer.
//
// Go's garbage collector may already have copied the value elsewhere, so this
// is a defense in depth rather than a guarantee: it shortens the window in
// which a core dump or a reused heap page exposes a credential, and costs
// nothing.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
