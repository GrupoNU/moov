package parser

import (
	"bytes"
)

// Pre-scan: a cheap structural bound applied BEFORE either library sees the bytes.
//
// # Why this exists (a finding beyond S4)
//
// S4 measured both libraries against 110 corpus cases and found no hangs. Fuzzing
// this implementation found one anyway, because the corpus's nesting bombs are all
// WELL FORMED and the hazard needs a nest that is deep AND truncated.
//
// enmime builds its part tree eagerly, with each nesting level wrapping the level
// below in a boundaryReader. When the nest is unterminated, every level re-scans
// the remaining input looking for a boundary that never comes, and the cost
// compounds per level. Measured on this VPS, with a truncated multipart nest and
// nothing else in the message:
//
//	depth 10 →  23 ms      depth 20 →  1.1 s
//	depth 14 →  24 ms      depth 22 →  5.0 s
//	depth 18 → 289 ms      depth 24 → 18.1 s
//
// Roughly 4x per two levels. At depth 30 a 1.6 KB message would occupy a parse
// worker for hours. That is a denial-of-service vector reachable by anyone who
// can send mail, and it is the exact failure mode S4 §1 named as one of the two
// that genuinely threaten sync availability: "a hang that holds a folder open
// forever".
//
// The engine's own MaxDepth cap does NOT prevent this, which is the subtle part:
// the cap is enforced by the walkers, and the walkers only run after the library
// has already built its tree. The bound has to be applied to the INPUT.
//
// So: count boundary-ish nesting textually first, in one linear pass with no
// allocation, and refuse the message before any library touches it. A linear
// scan cannot itself be the vector.

// prescanDepth estimates the maximum multipart nesting depth of a raw message by
// counting distinct declared boundaries.
//
// It deliberately does NOT parse. It counts the `boundary=` parameters that
// appear in the input, which is an upper bound on how deeply a library could
// nest: a message cannot nest deeper than it declares boundaries. Over-counting
// is harmless (the real cap check still runs in the walker, where the true depth
// is known); under-counting is what must not happen, and counting declarations
// cannot under-count.
//
// The scan is bounded to the first prescanLimit bytes. The blowup is driven by
// the nesting declared in the header region of a message, which is at the front;
// scanning a 100 MB attachment for boundary parameters would be its own cost.
func prescanDepth(raw []byte) int {
	const prescanLimit = 1 << 20 // 1 MB is far past any realistic header region
	if len(raw) > prescanLimit {
		raw = raw[:prescanLimit]
	}

	// Count occurrences of "boundary" (case-insensitively) that look like a
	// Content-Type parameter. Distinct nesting levels each declare one.
	var count int
	for i := 0; i+8 <= len(raw); {
		j := indexFoldASCII(raw[i:], []byte("boundary"))
		if j < 0 {
			break
		}
		pos := i + j + len("boundary")
		// Require the '=' that makes it a parameter rather than prose.
		k := pos
		for k < len(raw) && (raw[k] == ' ' || raw[k] == '\t') {
			k++
		}
		if k < len(raw) && raw[k] == '=' {
			count++
		}
		i = pos
	}
	return count
}

// indexFoldASCII is bytes.Index with ASCII case folding, which is what header
// parameter names need. It avoids allocating a lowercased copy of the input.
func indexFoldASCII(haystack, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
	first := lowerASCII(needle[0])
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if lowerASCII(haystack[i]) != first {
			continue
		}
		if hasPrefixFoldASCII(haystack[i:], needle) {
			return i
		}
	}
	return -1
}

func hasPrefixFoldASCII(b, prefix []byte) bool {
	if len(b) < len(prefix) {
		return false
	}
	for i := range prefix {
		if lowerASCII(b[i]) != lowerASCII(prefix[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// isUnterminatedNest reports whether the message declares more boundaries than it
// closes, which is the condition that turns depth into a blowup.
//
// A well-formed nest of any depth is cheap for both libraries (S4 §5 measured 500
// levels at 38 ms in go-message and 458 ms in enmime). It is specifically the
// MISSING close delimiters that make each level re-scan to EOF. Checking for the
// combination keeps legitimate deep mail working while refusing the hostile shape.
func isUnterminatedNest(raw []byte, declared int) bool {
	if declared == 0 {
		return false
	}
	// Count close delimiters: a line consisting of "--<boundary>--". Counting the
	// "--\r\n" / "--\n" line endings that terminate them is a good enough proxy
	// and costs one pass.
	closes := bytes.Count(raw, []byte("--\r\n")) + bytes.Count(raw, []byte("--\n"))
	if bytes.HasSuffix(raw, []byte("--")) {
		closes++
	}
	return closes < declared
}
