package jmap

import (
	"fmt"
	"strconv"
	"strings"
)

// JMAP account ids (L2 §4): "accountId JMAP = id de accounts codificado
// (opaco, estable)".
//
// The encoding is deterministic — the same store row always yields the same
// accountId, across processes and restarts, which is what lets clients cache
// state keyed by account — and opaque in the JMAP sense: clients must treat
// it as an arbitrary Id string (RFC 8620 §1.2) and never parse it. It is not
// a secret: knowing an accountId grants nothing, because every request is
// authorized against the authenticated caller's own account.
//
// The alphabet is base-36 lowercase with an "a" prefix, which satisfies the
// RFC 8620 §1.2 Id constraints (URL-safe characters, does not start with a
// digit or "-", 1-255 octets).

// EncodeAccountID converts a store account id (a positive int64) into its
// JMAP accountId.
func EncodeAccountID(id int64) string {
	return "a" + strconv.FormatInt(id, 36)
}

// DecodeAccountID converts a JMAP accountId back to the store account id.
//
// It accepts exactly the canonical form EncodeAccountID produces — anything
// else ("A1", "a01", padding, a negative) is rejected, so two different
// strings can never alias the same account.
func DecodeAccountID(s string) (int64, error) {
	rest, ok := strings.CutPrefix(s, "a")
	if !ok || rest == "" {
		return 0, fmt.Errorf("jmap: invalid account id %q", s)
	}
	id, err := strconv.ParseInt(rest, 36, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("jmap: invalid account id %q", s)
	}
	if EncodeAccountID(id) != s {
		return 0, fmt.Errorf("jmap: non-canonical account id %q", s)
	}
	return id, nil
}
