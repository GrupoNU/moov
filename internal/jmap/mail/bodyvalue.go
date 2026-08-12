package mail

import (
	"strings"
	"unicode/utf8"
)

// EmailBodyValue construction — RFC 8621 §4.1.4.

// bodyValue is the EmailBodyValue object of RFC 8621 §4.1.4.
type bodyValue struct {
	// Value: "The value of the body part after decoding Content-Transfer-
	// Encoding and the Content-Type charset, if both known to the server, and
	// with any CRLF replaced with a single LF."
	Value string `json:"value"`

	// IsEncodingProblem: "This is true if malformed sections were found while
	// decoding the charset, or the charset was unknown, or the
	// content-transfer-encoding was unknown."
	IsEncodingProblem bool `json:"isEncodingProblem"`

	// IsTruncated: "This is true if the value has been truncated."
	IsTruncated bool `json:"isTruncated"`
}

// newBodyValue builds an EmailBodyValue from a part's decoded content.
//
// # The truncation unit is OCTETS, and the RFC is unambiguous about it
//
// maxBodyValueBytes is defined in RFC 8621 §4.2 as: "If greater than zero, the
// value property of any EmailBodyValue object returned in bodyValues MUST be
// truncated if necessary so it does not exceed this number of octets in size."
//
// Octets — not characters, not UTF-16 code units. The property name says
// "Bytes" and the prose says "octets", and the two agree. (The UTF-16 code
// unit is the unit of a different JMAP field entirely: RFC 8621 §4.1.4's
// sibling `preview` and the Email `size` are octets too; nothing in RFC 8621
// counts UTF-16.) So the cap here is applied to len(s) on a UTF-8 Go string,
// which is a count of octets by construction.
//
// The same sentence continues with the constraint that makes a naive
// s[:max] wrong: "The server MUST ensure the truncation results in valid
// UTF-8 and does not occur mid-codepoint." So the cut is moved backwards to a
// rune boundary. Cutting mid-codepoint would produce a JSON string that is
// either invalid UTF-8 or silently repaired into U+FFFD by the client's
// decoder, and the client cannot tell the difference from a corrupt message.
//
// CRLF normalization happens before truncation, deliberately: the octet count
// the client budgeted against is the count of what it receives, and
// normalizing after would let a value exceed the cap it was just truncated to.
func newBodyValue(content string, maxOctets uint64, encodingProblem bool) bodyValue {
	// §4.1.4: "with any CRLF replaced with a single LF".
	value := strings.ReplaceAll(content, "\r\n", "\n")

	bv := bodyValue{Value: value, IsEncodingProblem: encodingProblem}

	// §4.2: "If not given, or 0, no truncation is performed."
	//
	// The comparison is done in uint64 — the type maxBodyValueBytes is
	// declared as (UnsignedInt) — rather than by converting the budget to an
	// int. A client may legitimately send a budget larger than an int can hold
	// on a 32-bit build, and converting first would wrap it into a small or
	// negative number and truncate a body the client asked to receive whole.
	// After this guard the budget is known to be smaller than the value's
	// length, so it fits an int by construction.
	if maxOctets == 0 || uint64(len(value)) <= maxOctets {
		return bv
	}

	bv.Value = truncateUTF8(value, int(maxOctets)) //nolint:gosec // guarded above: maxOctets < len(value), which is an int
	bv.IsTruncated = true
	return bv
}

// truncateUTF8 cuts s to at most maxOctets bytes without splitting a rune.
//
// The loop walks back at most three bytes: a UTF-8 sequence is at most four
// bytes long, so a cut landing inside one is at most three bytes past its
// start. Backing off to the start of that sequence — rather than trying to
// keep it — is what guarantees the result never exceeds the cap the client
// asked for, which is the direction the MUST points.
func truncateUTF8(s string, maxOctets int) string {
	if maxOctets <= 0 {
		return ""
	}
	if len(s) <= maxOctets {
		return s
	}
	cut := maxOctets
	// DecodeLastRuneInString reports RuneError with size 1 for an invalid or
	// incomplete trailing sequence, which is precisely the mid-codepoint case.
	for cut > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:cut])
		if r != utf8.RuneError || size > 1 {
			break
		}
		cut--
	}
	return s[:cut]
}
