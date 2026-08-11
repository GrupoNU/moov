package mailcow

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// GeneratedPasswordLength is the length of a generated app password in
// characters.
//
// 32 characters of the alphabet below is ~165 bits of entropy. That is far more
// than a password policy requires, and the reason is that this credential is
// never typed by a human and never remembered: its only cost is bytes, and the
// only thing that could ever attack it is an offline crack of the bcrypt hash
// Mailcow stores. There is no reason to be modest.
const GeneratedPasswordLength = 32

// passwordAlphabet excludes characters that cause trouble in the places this
// value travels: the IMAP and SMTP wire protocols, a shell command line an
// operator might paste it into, and Mailcow's own form handling.
//
// Specifically absent: quotes and backslashes (IMAP literals and shell
// quoting), whitespace, and the visually ambiguous 0/O and 1/l/I — not because
// a machine confuses them, but because a human reading one out of a log during
// an incident does.
const passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GeneratePassword returns a new app password from the system CSPRNG.
//
// The plaintext is returned to the caller and exists nowhere else: Mailcow
// stores only a bcrypt hash of it, and Moov stores only the AES-256-GCM sealed
// form. Losing it means re-provisioning, which is the correct trade.
func GeneratePassword() (string, error) {
	upper := big.NewInt(int64(len(passwordAlphabet)))
	out := make([]byte, GeneratedPasswordLength)
	for i := range out {
		// rand.Int over the alphabet size rather than a byte modulo: modulo
		// bias is small here but free to avoid, and "we did the unbiased thing"
		// is the only answer worth having in a public security review.
		n, err := rand.Int(rand.Reader, upper)
		if err != nil {
			return "", fmt.Errorf("mailcow: generating app password: %w", err)
		}
		out[i] = passwordAlphabet[n.Int64()]
	}
	return string(out), nil
}
