package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzParse is the availability guarantee, stated as a property rather than as a
// list of cases: for ANY input, Parse returns a self-consistent result without
// panicking.
//
// The bar is zero panics. A panic in the parse path kills the sync worker, which
// is one of exactly two failure modes S4 §1 identified as genuinely threatening
// sync availability (the other, a hang, has no unbounded loop to come from here —
// every walk is bounded by the caps). The corpus is 110 deliberately hostile
// inputs that a human designed; the fuzzer's job is the shapes nobody predicted,
// which S4's "open risks" section names as the corpus's main limitation.
//
// Run it beyond the seed corpus with:
//
//	go test ./internal/parser -run FuzzParse -fuzz FuzzParse -fuzztime 120s
func FuzzParse(f *testing.F) {
	// Seed from every corpus file: the fuzzer's mutations are far more valuable
	// starting from real pathological MIME than from an empty string.
	seedFromCorpus(f)

	// Plus a few hand-picked shapes that stress specific branches, so the seed
	// set covers the mitigations even if the corpus directory is unavailable.
	for _, s := range []string{
		"",
		"\r\n",
		"Subject: x\r\n\r\nbody",
		"Subject: =?UTF-8?B?UmV1bmnDs24gbWVuc3VhbA?=\r\n\r\nb",
		"Content-Type: multipart/mixed; boundary=\"\"\r\n\r\n--\r\nx\r\n----\r\n",
		"Content-Type: multipart/mixed\r\n\r\nno boundary at all",
		"Content-Type: message/rfc822\r\n\r\nSubject: inner\r\n\r\ninner body",
		"Content-Type: text/plain; charset=unknown-8bit\r\n\r\n\xf1\xf2\xf3",
		"Content-Transfer-Encoding: base64\r\n\r\n!!!!not base64!!!!",
		"Subject: cr\rFrom: a@b.c\r\rbody",
		" leading continuation\r\nFrom: a@example.com\r\n\r\nbody",
		"\x00\x00\x00",
	} {
		f.Add([]byte(s))
	}

	// The shape this target actually caught: a deep AND unterminated multipart
	// nest, which drove enmime into superlinear work (18 s at depth 24, from
	// 1.4 KB of input) before prescan.go was written. Seeded explicitly rather
	// than left to the fuzzing cache, because a cache is not version-controlled
	// and this is the one input in the package with a known exploit history.
	f.Add(unterminatedNest(40))
	f.Add(unterminatedNest(17)) // just past MaxUnterminatedDepth
	f.Add(unterminatedNest(15)) // just under it: must still parse

	f.Fuzz(func(t *testing.T, raw []byte) {
		// Very large inputs are not where the interesting logic is — the corpus
		// already covers the wide and deep shapes deliberately, and the size cap
		// is unit-tested — but they dominate the fuzzer's time budget, because
		// each execution AND each minimization step re-parses megabytes. Skipping
		// them keeps throughput on the branch-heavy small inputs where new bugs
		// actually live. (Both bugs this target found were under 5 KB.)
		const maxFuzzInput = 64 << 10
		if len(raw) > maxFuzzInput {
			t.Skip("input larger than the fuzzing budget; size caps are unit-tested")
		}

		// Tight caps: they make the fuzzer spend its time on parsing logic rather
		// than on allocating for a giant generated input, and they exercise the
		// cap paths, which are the least-traveled code in the package.
		limits := Limits{
			MaxDepth: 20, MaxParts: 50, MaxTotalSize: 1 << 20,
			MaxRFC822Depth: 4, MaxPartSize: 1 << 18,
		}

		msg := ParseBytes(raw, limits)

		// Every invariant a consumer is entitled to rely on. A violation here is
		// a bug even without a panic, because the store and the JMAP layer index
		// off these fields.

		switch msg.Status {
		case StatusOK, StatusPartial, StatusFailed:
		default:
			t.Fatalf("invalid status %q", msg.Status)
		}
		if msg.Parser == "" {
			t.Fatal("result names no parser")
		}

		if msg.Status == StatusFailed {
			if len(msg.Parts) != 0 {
				t.Fatalf("failed parse carries %d parts", len(msg.Parts))
			}
			if len(msg.Headers.All) != 0 {
				t.Fatalf("failed parse carries %d headers (S4 §2 forbids this)",
					len(msg.Headers.All))
			}
		}

		for i, p := range msg.Parts {
			if p.Index != i {
				t.Fatalf("part %d has Index %d", i, p.Index)
			}
			if p.Parent >= i || p.Parent < -1 {
				t.Fatalf("part %d has invalid parent %d", i, p.Parent)
			}
			if p.Depth < 0 || p.Depth > limits.MaxDepth+1 {
				t.Fatalf("part %d has depth %d, outside the cap", i, p.Depth)
			}
			if p.Size != len(p.Content) {
				t.Fatalf("part %d: Size %d != len(Content) %d", i, p.Size, len(p.Content))
			}
			if int64(len(p.Content)) > limits.MaxPartSize {
				t.Fatalf("part %d exceeds MaxPartSize: %d bytes", i, len(p.Content))
			}
			// A NUL reaching this far would fail to store in PostgreSQL, which
			// is the concrete downstream break le-007 exists to prevent.
			for _, b := range p.Content {
				if b == 0 && p.IsText() {
					t.Fatalf("part %d: NUL byte survived in text content", i)
				}
			}
		}

		if len(msg.Parts) > limits.MaxParts {
			t.Fatalf("%d parts exceeds the cap of %d", len(msg.Parts), limits.MaxParts)
		}

		// Text destined for PostgreSQL must be NUL-free and valid UTF-8; the tsv
		// columns are text columns.
		for name, s := range map[string]string{
			"SubjectText": msg.SubjectText,
			"AddressText": msg.AddressText,
			"BodyText":    msg.BodyText,
		} {
			for _, r := range s {
				if r == 0 {
					t.Fatalf("%s contains a NUL byte", name)
				}
			}
			if !utf8.ValidString(s) {
				t.Fatalf("%s is not valid UTF-8", name)
			}
		}
		for _, r := range msg.Headers.Subject {
			if r == 0 {
				t.Fatal("Subject contains a NUL byte")
			}
		}

		// Determinism: the same bytes must always parse the same way, or a
		// reparse after a version bump would silently change stored data.
		again := ParseBytes(raw, limits)
		if again.Status != msg.Status || again.Parser != msg.Parser ||
			len(again.Parts) != len(msg.Parts) ||
			again.SubjectText != msg.SubjectText || again.BodyText != msg.BodyText {
			t.Fatal("Parse is not deterministic for identical input")
		}
	})
}

// unterminatedNest builds a multipart nest of the given depth with NO close
// delimiters — the shape that made enmime's cost explode.
func unterminatedNest(depth int) []byte {
	var b strings.Builder
	b.WriteString("Subject: unterminated\r\nMIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=\"b0\"\r\n\r\n")
	for i := 0; i < depth; i++ {
		b.WriteString("--b" + itoa(i) + "\r\n")
		b.WriteString("Content-Type: multipart/mixed; boundary=\"b" + itoa(i+1) + "\"\r\n\r\n")
	}
	return []byte(b.String())
}

// seedFromCorpus adds every corpus file to the fuzzing seed set.
func seedFromCorpus(f *testing.F) {
	f.Helper()
	dir := corpusDir()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // a missing corpus must not fail the fuzz target
		}
		if d.IsDir() || filepath.Ext(path) != ".eml" {
			return nil
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // fixed test-data path
		if readErr == nil {
			f.Add(data)
		}
		return nil
	})
	if err != nil {
		f.Logf("walking the corpus for seeds: %v", err)
	}
}
