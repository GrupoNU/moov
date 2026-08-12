package mail

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The truncation contract of RFC 8621 §4.2 / §4.1.4:
//
//	"the value property ... MUST be truncated if necessary so it does not
//	 exceed this number of octets in size ... The server MUST ensure the
//	 truncation results in valid UTF-8 and does not occur mid-codepoint."
//
// Both halves are load-bearing and both are tested here: the unit is octets
// (not runes, not UTF-16 code units), and the result is always valid UTF-8.

func TestBodyValueTruncationCountsOctetsNotRunes(t *testing.T) {
	// Ten runes, twenty octets: every character is two bytes in UTF-8.
	const s = "ññññññññññ"
	if utf8.RuneCountInString(s) != 10 || len(s) != 20 {
		t.Fatalf("fixture is wrong: %d runes, %d octets", utf8.RuneCountInString(s), len(s))
	}

	bv := newBodyValue(s, 10, false)
	if len(bv.Value) > 10 {
		t.Errorf("value is %d octets, want at most 10", len(bv.Value))
	}
	// If the cap were counted in RUNES, all ten characters (20 octets) would
	// have survived. This assertion is what pins the unit.
	if utf8.RuneCountInString(bv.Value) != 5 {
		t.Errorf("kept %d runes from a 10-octet budget; the unit must be octets",
			utf8.RuneCountInString(bv.Value))
	}
	if !bv.IsTruncated {
		t.Error("isTruncated must be set")
	}
}

func TestBodyValueTruncationNeverSplitsACodepoint(t *testing.T) {
	// A 4-octet rune (an emoji) straddling every possible cut point.
	const s = "ab😀cd" // 2 + 4 + 2 octets
	for budget := 1; budget <= len(s)+2; budget++ {
		bv := newBodyValue(s, uint64(budget), false)
		if !utf8.ValidString(bv.Value) {
			t.Fatalf("budget %d produced invalid UTF-8: %q", budget, bv.Value)
		}
		if len(bv.Value) > budget {
			t.Fatalf("budget %d produced %d octets", budget, len(bv.Value))
		}
		wantTruncated := budget < len(s)
		if bv.IsTruncated != wantTruncated {
			t.Errorf("budget %d: isTruncated = %v, want %v", budget, bv.IsTruncated, wantTruncated)
		}
	}
}

func TestBodyValueZeroMeansNoTruncation(t *testing.T) {
	long := strings.Repeat("x", 10_000)
	bv := newBodyValue(long, 0, false)
	if bv.IsTruncated {
		t.Error("maxBodyValueBytes=0 means no truncation (RFC 8621 §4.2)")
	}
	if len(bv.Value) != len(long) {
		t.Errorf("value was shortened to %d octets", len(bv.Value))
	}
}

func TestBodyValueNormalizesCRLFBeforeCounting(t *testing.T) {
	// §4.1.4 requires CRLF -> LF. Doing it AFTER truncation would let the
	// value exceed the budget it was just cut to.
	s := strings.Repeat("a\r\n", 10) // 30 octets raw, 20 after normalization
	bv := newBodyValue(s, 0, false)
	if strings.Contains(bv.Value, "\r") {
		t.Errorf("CR survived normalization: %q", bv.Value)
	}
	if len(bv.Value) != 20 {
		t.Errorf("normalized length = %d, want 20", len(bv.Value))
	}

	capped := newBodyValue(s, 10, false)
	if len(capped.Value) > 10 {
		t.Errorf("normalized-then-truncated value is %d octets, want <= 10", len(capped.Value))
	}
}

func TestBodyValueEncodingProblemIsReported(t *testing.T) {
	bv := newBodyValue("text", 0, true)
	if !bv.IsEncodingProblem {
		t.Error("isEncodingProblem must be carried through")
	}
}

func TestTruncateUTF8EdgeCases(t *testing.T) {
	if got := truncateUTF8("abc", 0); got != "" {
		t.Errorf("zero budget = %q", got)
	}
	if got := truncateUTF8("abc", 10); got != "abc" {
		t.Errorf("budget above length = %q", got)
	}
	// A budget that lands inside the only rune yields the empty string rather
	// than a broken byte.
	if got := truncateUTF8("😀", 2); got != "" {
		t.Errorf("mid-rune cut = %q, want empty", got)
	}
}
