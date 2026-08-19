package store

import (
	"strings"
	"unicode"
)

// Subject normalization for the JWZ subject fallback.
//
// RFC 8621 §3 states the second of its two suggested threading conditions as:
//
//	"After stripping automatically added prefixes such as 'Fwd:', 'Re:',
//	 '[List-Tag]', etc., and ignoring white space, the subjects are the same."
//
// JWZ's own step 5 (jwz.org/doc/threading.html) is the same idea with the
// stripping specified: remove any number of leading "Re:"-like tokens and
// bracketed list tags, then compare.
//
// This lives in Go rather than in SQL deliberately (migration 0004 states why):
// the rule set is a moving target, a GENERATED column would make every change a
// full table rewrite, and the cases below want a unit-test table.

// maxSubjectKeyLength bounds a stored subject key.
//
// The key is the primary key of thread_subject_keys, and PostgreSQL's btree
// cannot index a value past roughly a third of a page (~2,700 bytes). A subject
// long enough to approach that is not a thread key anyone would recognize, so
// the bound is set far below it, at a length that still distinguishes any real
// subject. RFC 5322 recommends 78 characters per header line and 998 as the
// hard limit, so 512 already accepts subjects well past what a mailer produces.
const maxSubjectKeyLength = 512

// minSubjectKeyLength refuses a key too short to mean anything.
//
// Joining on a two-character subject would put unrelated conversations together
// on a coincidence. The Message-ID graph handles real replies; the fallback
// exists only for mailers that drop References, and those still carry a real
// subject.
const minSubjectKeyLength = 3

// NormalizeSubject reduces a subject to its thread key, and reports whether the
// original carried a reply/forward marker.
//
// The second return value is load-bearing, not a convenience. The subject
// fallback may only ever join a message that LOOKS like a reply onto an
// existing thread; applying it to a bare original subject would put every
// message titled "Hello" in a mailbox into one thread, which is worse than not
// threading at all. So the caller needs to know whether "Re:" was there, and
// that fact is only available while stripping it.
//
// It returns an empty key for a subject that cannot serve as one — empty,
// whitespace, or too short after normalization — which the caller treats as
// "no fallback available".
func NormalizeSubject(subject string) (key string, isReply bool) {
	s := strings.TrimSpace(subject)

	// Strip leading prefixes repeatedly: real mail accumulates them
	// ("Re: Fwd: [list] Re: subject"), and each layer is added by a different
	// hop, so a single pass would leave the rest.
	for {
		stripped, marker, ok := stripOnePrefix(s)
		if !ok {
			break
		}
		s = stripped
		// A list tag is NOT a reply marker: "[moov-dev] release plan" is an
		// original post. Only Re:/Fwd:-class tokens mark a reply.
		if marker {
			isReply = true
		}
	}

	key = collapseSubject(s)
	if len([]rune(key)) < minSubjectKeyLength {
		return "", isReply
	}
	if len(key) > maxSubjectKeyLength {
		// Truncate on a rune boundary: the key goes into a text column and a
		// split multi-byte character is invalid UTF-8.
		key = truncateRunes(key, maxSubjectKeyLength)
	}
	return key, isReply
}

// replyPrefixes are the reply/forward tokens, lowercased and without their
// trailing colon.
//
// The list is multilingual on purpose. Moov's reference installation is
// Spanish/English (the same reason the FTS configuration is 'simple' rather
// than a single stemmer, migration 0002), and a Spanish "RV:" or a German
// "AW:" is exactly as automatic as "Re:". Missing one costs a split thread,
// which is the failure the fallback exists to prevent.
//
// Sorted longest-first is NOT required — the match is exact against the token
// before the colon — but the set must stay lowercase, because the comparison
// folds case.
var replyPrefixes = map[string]bool{
	// English / universal (RFC 5322 §3.6.5 mentions only "Re:").
	"re": true, "fw": true, "fwd": true, "forward": true,
	// Spanish.
	"rv": true, "res": true, "resp": true, "reenv": true,
	// Portuguese, Italian, French.
	"enc": true, "r": true, "ref": true, "tr": true, "rif": true,
	// German, Dutch.
	"aw": true, "wg": true, "antw": true, "doorst": true,
	// Nordic.
	"sv": true, "vs": true, "vb": true, "vl": true,
}

// stripOnePrefix removes one leading prefix from a subject.
//
// It returns the remainder, whether the prefix was a REPLY marker (as opposed
// to a list tag), and whether anything was stripped at all.
func stripOnePrefix(s string) (rest string, replyMarker bool, ok bool) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	if s == "" {
		return s, false, false
	}

	// A bracketed list tag: "[moov-dev] subject". Only at the very start, and
	// only when it closes — an unmatched "[" is part of the subject.
	if s[0] == '[' {
		if end := strings.IndexByte(s, ']'); end > 0 {
			inner := s[1:end]
			// A tag with no content ("[]") or one containing a bracket is not a
			// list tag; leaving it alone is the conservative reading.
			if inner != "" && !strings.ContainsAny(inner, "[]") {
				return s[end+1:], false, true
			}
		}
		return s, false, false
	}

	// A "Re:"-class token. The token is everything up to the first colon, and
	// mailers append a counter to it ("Re[2]:", "Re(3):"), which is part of the
	// automatic decoration and must go with it.
	colon := strings.IndexByte(s, ':')
	if colon <= 0 || colon > 16 {
		// No colon, or one too far in to be a prefix token. 16 bytes is well
		// past the longest entry in replyPrefixes plus a counter, and it stops
		// a subject like "Meeting notes: Q3" from being decapitated.
		return s, false, false
	}
	token := strings.ToLower(strings.TrimSpace(s[:colon]))
	token = stripCounter(token)
	if !replyPrefixes[token] {
		return s, false, false
	}
	return s[colon+1:], true, true
}

// stripCounter removes a mailer's repetition counter from a reply token:
// "re[2]" and "re(3)" both become "re".
func stripCounter(token string) string {
	if len(token) < 3 {
		return token
	}
	last := token[len(token)-1]
	var open byte
	switch last {
	case ']':
		open = '['
	case ')':
		open = '('
	default:
		return token
	}
	i := strings.LastIndexByte(token, open)
	if i <= 0 {
		return token
	}
	// Everything between the brackets must be digits, or this is not a counter.
	for _, r := range token[i+1 : len(token)-1] {
		if !unicode.IsDigit(r) {
			return token
		}
	}
	return strings.TrimSpace(token[:i])
}

// collapseSubject applies the "ignoring white space" half of the RFC's rule and
// folds case, so two spellings of the same subject produce one key.
//
// Case folding is not in the RFC's wording, and it is applied anyway: mailers
// and users routinely change the capitalization of a subject on reply, and a
// key that distinguishes "Invoice" from "invoice" would split those threads for
// no benefit. Unicode-aware lowering (strings.ToLower) rather than ASCII, since
// the installed base is Spanish.
func collapseSubject(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func truncateRunes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	for len(cut) > 0 && !isRuneBoundary(s, len(cut)) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// isRuneBoundary reports whether i is the start of a rune in s.
func isRuneBoundary(s string, i int) bool {
	if i <= 0 || i >= len(s) {
		return true
	}
	// A continuation byte is 10xxxxxx.
	return s[i]&0xC0 != 0x80
}

// ThreadCandidate is everything the threading assignment needs to know about
// one incoming message, before it has an id.
//
// It is built by the sync engine from the parsed headers (internal/sync) and
// consumed by AssignThreads. Keeping it a plain struct rather than passing a
// store.Message means the caller cannot accidentally make threading depend on a
// column that is not a header.
type ThreadCandidate struct {
	// MessageID is the message's own Message-ID, without angle brackets. May be
	// empty: forged and missing Message-IDs are routine.
	MessageID string
	// References is the parsed References chain, oldest first, plus In-Reply-To
	// when the caller has one. Order is not trusted (see AssignThreads).
	References []string
	// Subject is the raw decoded subject; normalization happens here.
	Subject string
}

// referenceSet returns every distinct ancestor Message-ID this candidate names,
// excluding its own.
//
// A message that references itself is not rare — some mailers append their own
// Message-ID to References — and following it would make the message its own
// parent, which the id-ordering guard would reject anyway but which is clearer
// to exclude here.
func (c ThreadCandidate) referenceSet() []string {
	if len(c.References) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(c.References))
	out := make([]string, 0, len(c.References))
	for _, ref := range c.References {
		if ref == "" || ref == c.MessageID || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}
