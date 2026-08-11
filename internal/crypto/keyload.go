package crypto

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Environment variables this package reads.
const (
	// EnvMasterKey holds the master key material directly. Its value is one or
	// more comma-separated entries; the first is the primary key.
	//
	// An entry is either a bare base64 key (which is taken as key id 1, the
	// single-key case an operator starting out should not have to think about)
	// or "<id>:<base64 key>" for an explicit id, which is the form rotation
	// requires. Mixing the two forms in one list is rejected, because
	// "<base64>,2:<base64>" reads as if the bare one were also id 2's peer
	// while it is silently id 1.
	EnvMasterKey = "MOOV_MASTER_KEY"

	// EnvMasterKeyFile points at a file holding what EnvMasterKey would hold.
	// It is the form to prefer in production: a Docker secret or a mounted
	// file does not appear in `docker inspect`, in a crash dump of the
	// environment, or in a child process's /proc/<pid>/environ.
	EnvMasterKeyFile = "MOOV_MASTER_KEY_FILE"
)

// LoadKeyring builds the process keyring from the environment.
//
// Exactly one source must be set. Setting both is an error rather than a
// precedence rule: an operator who has set both has a mistaken belief about
// which one is live, and silently picking one leaves that belief intact until
// it matters.
//
// Setting neither is also an error. There is no default key, no key generated
// at startup and no unencrypted fallback: Moov holds credentials that let it
// read someone's mail, and starting without the means to protect them is not a
// degraded mode worth having.
func LoadKeyring() (*Keyring, error) {
	inline := os.Getenv(EnvMasterKey)
	path := os.Getenv(EnvMasterKeyFile)

	switch {
	case inline != "" && path != "":
		return nil, fmt.Errorf("crypto: both %s and %s are set; set exactly one",
			EnvMasterKey, EnvMasterKeyFile)
	case inline != "":
		kr, err := ParseKeyring(inline)
		if err != nil {
			// The error never quotes the value: a parse failure on a secret
			// must not put the secret in a log line.
			return nil, fmt.Errorf("%s: %w", EnvMasterKey, err)
		}
		return kr, nil
	case path != "":
		raw, err := os.ReadFile(path) // #nosec G304 -- the path is operator-supplied configuration, which is the point of the variable.
		if err != nil {
			return nil, fmt.Errorf("%s: reading %s: %w", EnvMasterKeyFile, path, err)
		}
		kr, err := ParseKeyring(string(raw))
		if err != nil {
			return nil, fmt.Errorf("%s (%s): %w", EnvMasterKeyFile, path, err)
		}
		return kr, nil
	default:
		return nil, fmt.Errorf("%w: set %s or %s (generate one with `moovctl key generate`)",
			ErrNoKey, EnvMasterKey, EnvMasterKeyFile)
	}
}

// ParseKeyring parses the textual keyring form described on EnvMasterKey.
//
// Whitespace around entries — and trailing newlines, which every text editor
// adds to a key file — is ignored.
func ParseKeyring(s string) (*Keyring, error) {
	entries := splitEntries(s)
	if len(entries) == 0 {
		return nil, ErrNoKey
	}

	// Either every entry carries an explicit id or none does. See EnvMasterKey.
	explicit := strings.Contains(entries[0], ":")
	keys := make([]Key, 0, len(entries))
	for i, entry := range entries {
		hasID := strings.Contains(entry, ":")
		if hasID != explicit {
			return nil, fmt.Errorf(
				"crypto: entry %d mixes the bare-key and \"<id>:<key>\" forms; use one or the other", i+1)
		}

		id := KeyID(1)
		material := entry
		if hasID {
			idStr, keyStr, _ := strings.Cut(entry, ":")
			n, err := strconv.ParseUint(strings.TrimSpace(idStr), 10, 8)
			if err != nil {
				return nil, fmt.Errorf("crypto: entry %d: key id must be 1-255: %w", i+1, err)
			}
			if n == 0 {
				return nil, fmt.Errorf("crypto: entry %d: %w", i+1, ErrZeroKeyID)
			}
			id = KeyID(n)
			material = strings.TrimSpace(keyStr)
		} else if len(entries) > 1 {
			return nil, fmt.Errorf(
				"crypto: %d keys given without ids; a keyring with more than one key needs \"<id>:<key>\" entries",
				len(entries))
		}

		raw, err := decodeKey(material)
		if err != nil {
			return nil, fmt.Errorf("crypto: entry %d (key id %d): %w", i+1, id, err)
		}
		k, err := NewKey(id, raw)
		zero(raw)
		if err != nil {
			return nil, fmt.Errorf("crypto: entry %d (key id %d): %w", i+1, id, err)
		}
		keys = append(keys, k)
	}
	return NewKeyring(keys...)
}

// EncodeKey renders key material in the form the environment expects. Used by
// `moovctl key generate` and by tests; never used on a loaded key, whose
// material this package does not keep.
func EncodeKey(material []byte) string {
	return base64.StdEncoding.EncodeToString(material)
}

// decodeKey accepts standard and URL-safe base64, with or without padding.
//
// Being liberal here is deliberate: an operator copying a key out of a secret
// manager should not have a mail server fail to start over an alphabet
// variant. Being liberal about the LENGTH would be a different matter, and
// NewKey is not.
func decodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ErrNoKey
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	// The undecodable value is NOT included in the error: it is a secret, or
	// something an operator believes is one.
	return nil, fmt.Errorf("%w (%d characters of base64 expected to decode to %d bytes)",
		errNotBase64, base64.StdEncoding.EncodedLen(KeySize), KeySize)
}

// errNotBase64 is unexported: callers branch on ErrKeySize or ErrNoKey, and a
// malformed encoding is reported as a message rather than a condition anything
// recovers from differently.
var errNotBase64 = fmt.Errorf("crypto: master key is not valid base64")

// splitEntries splits on commas and newlines — a key file with one entry per
// line is as natural a form as a comma-separated variable — dropping empties.
func splitEntries(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
