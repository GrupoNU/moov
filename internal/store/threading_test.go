package store

import (
	"strings"
	"testing"
)

// Subject normalization is pure and therefore tested in-package, with no
// database. The cases below are the ones RFC 8621 §3 names ("stripping
// automatically added prefixes such as 'Fwd:', 'Re:', '[List-Tag]', etc., and
// ignoring white space"), plus the ones real mail produces that the RFC's
// "etc." is quietly standing in for.

func TestNormalizeSubject(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantKey string
		wantRe  bool
	}{
		// ---- the base case ------------------------------------------------
		{
			name:    "plain subject is its own key",
			in:      "Quarterly report",
			wantKey: "quarterly report",
			wantRe:  false,
		},

		// ---- RFC 8621 §3's named prefixes ---------------------------------
		{
			name:    "Re: is stripped and marks a reply",
			in:      "Re: Quarterly report",
			wantKey: "quarterly report",
			wantRe:  true,
		},
		{
			name:    "Fwd: is stripped and marks a reply",
			in:      "Fwd: Quarterly report",
			wantKey: "quarterly report",
			wantRe:  true,
		},
		{
			name: "a list tag is stripped but is NOT a reply marker",
			// This is the distinction that keeps the subject fallback from
			// joining every original post on a list into one thread.
			in:      "[moov-dev] Quarterly report",
			wantKey: "quarterly report",
			wantRe:  false,
		},
		{
			name:    "stacked prefixes are all stripped",
			in:      "Re: Fwd: [moov-dev] Re: Quarterly report",
			wantKey: "quarterly report",
			wantRe:  true,
		},

		// ---- case and whitespace ("ignoring white space") -----------------
		{
			name:    "case is folded",
			in:      "RE: QUARTERLY Report",
			wantKey: "quarterly report",
			wantRe:  true,
		},
		{
			name:    "internal whitespace is collapsed",
			in:      "Re:   Quarterly    report  ",
			wantKey: "quarterly report",
			wantRe:  true,
		},
		{
			name:    "tabs and newlines are whitespace too",
			in:      "Re:\tQuarterly\r\n report",
			wantKey: "quarterly report",
			wantRe:  true,
		},

		// ---- mailer counters ----------------------------------------------
		{
			name:    "a bracketed counter is part of the prefix",
			in:      "Re[2]: Quarterly report",
			wantKey: "quarterly report",
			wantRe:  true,
		},
		{
			name:    "a parenthesized counter too",
			in:      "Re(3): Quarterly report",
			wantKey: "quarterly report",
			wantRe:  true,
		},

		// ---- multilingual, which the installed base needs ------------------
		{
			name:    "Spanish RV: is a forward marker",
			in:      "RV: Informe trimestral",
			wantKey: "informe trimestral",
			wantRe:  true,
		},
		{
			name:    "German AW: is a reply marker",
			in:      "AW: Quartalsbericht",
			wantKey: "quartalsbericht",
			wantRe:  true,
		},
		{
			name:    "accents survive normalization",
			in:      "Re: Facturación de Añejo",
			wantKey: "facturación de añejo",
			wantRe:  true,
		},

		// ---- what must NOT be stripped ------------------------------------
		{
			name: "a colon in an ordinary subject is not a prefix",
			// The bug this guards: "Meeting notes: Q3" must not become "q3".
			in:      "Meeting notes: Q3",
			wantKey: "meeting notes: q3",
			wantRe:  false,
		},
		{
			name:    "an unknown token before a colon is left alone",
			in:      "URGENT: pay the invoice",
			wantKey: "urgent: pay the invoice",
			wantRe:  false,
		},
		{
			name:    "an unmatched bracket is part of the subject",
			in:      "[unclosed tag and more",
			wantKey: "[unclosed tag and more",
			wantRe:  false,
		},
		{
			name:    "an empty bracket pair is not a list tag",
			in:      "[] nothing here",
			wantKey: "[] nothing here",
			wantRe:  false,
		},

		// ---- keys that cannot serve ----------------------------------------
		{
			name:    "an empty subject yields no key",
			in:      "",
			wantKey: "",
			wantRe:  false,
		},
		{
			name:    "whitespace only yields no key",
			in:      "   \t  ",
			wantKey: "",
			wantRe:  false,
		},
		{
			name: "a subject of only prefixes yields no key but is still a reply",
			// "Re:" with nothing after it must not join every empty-subject
			// message into one thread, but the reply marker is real.
			in:      "Re: ",
			wantKey: "",
			wantRe:  true,
		},
		{
			name:    "a subject shorter than the minimum yields no key",
			in:      "Re: ok",
			wantKey: "",
			wantRe:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, isReply := NormalizeSubject(tc.in)
			if key != tc.wantKey {
				t.Errorf("NormalizeSubject(%q) key = %q, want %q", tc.in, key, tc.wantKey)
			}
			if isReply != tc.wantRe {
				t.Errorf("NormalizeSubject(%q) isReply = %v, want %v", tc.in, isReply, tc.wantRe)
			}
		})
	}
}

// A reply and its original must produce the SAME key. That equality is the
// whole mechanism of the subject fallback, so it gets its own test rather than
// being implied by the table above.
func TestNormalizeSubjectJoinsReplyToOriginal(t *testing.T) {
	original, wasReply := NormalizeSubject("Presupuesto 2026")
	if wasReply {
		t.Fatal("an original subject must not be marked as a reply")
	}

	for _, reply := range []string{
		"Re: Presupuesto 2026",
		"RE: Presupuesto 2026",
		"Re: [contabilidad] Presupuesto 2026",
		"Fwd: Re: Presupuesto 2026",
		"RV: Presupuesto 2026",
		"Re[4]:  Presupuesto   2026 ",
	} {
		key, isReply := NormalizeSubject(reply)
		if key != original {
			t.Errorf("NormalizeSubject(%q) = %q, want %q (the original's key)", reply, key, original)
		}
		if !isReply {
			t.Errorf("NormalizeSubject(%q) must be marked as a reply", reply)
		}
	}
}

// The key is bounded so it fits a btree index entry, and truncation must not
// split a multi-byte character — a text column rejects invalid UTF-8.
func TestNormalizeSubjectBoundsTheKey(t *testing.T) {
	// Multi-byte throughout, so a naive byte cut lands mid-rune.
	long := strings.Repeat("ñ", maxSubjectKeyLength)
	key, _ := NormalizeSubject(long)

	if len(key) > maxSubjectKeyLength {
		t.Fatalf("key is %d bytes, want at most %d", len(key), maxSubjectKeyLength)
	}
	if !isValidUTF8(key) {
		t.Fatal("truncation split a multi-byte character")
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// referenceSet is what the assignment feeds to the ancestor lookup, so its
// de-duplication and self-exclusion are threading behavior, not a detail.
func TestThreadCandidateReferenceSet(t *testing.T) {
	c := ThreadCandidate{
		MessageID: "self@example.test",
		References: []string{
			"a@example.test",
			"",                  // an empty entry from a malformed header
			"a@example.test",    // a duplicate
			"self@example.test", // its own id, which some mailers append
			"b@example.test",
		},
	}

	got := c.referenceSet()
	want := []string{"a@example.test", "b@example.test"}

	if len(got) != len(want) {
		t.Fatalf("referenceSet() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("referenceSet() = %v, want %v", got, want)
		}
	}
}

// A candidate with no references must produce no lookup at all: the empty slice
// is what makes the base case skip a query rather than issue one matching
// nothing.
func TestThreadCandidateReferenceSetEmpty(t *testing.T) {
	for _, c := range []ThreadCandidate{
		{},
		{References: []string{}},
		{MessageID: "x@example.test", References: []string{"x@example.test"}},
		{References: []string{"", ""}},
	} {
		if got := c.referenceSet(); len(got) != 0 {
			t.Errorf("referenceSet() = %v, want empty", got)
		}
	}
}
