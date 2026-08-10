package parser

import (
	"bytes"
	"net/textproto"
	"strings"
)

// Line-ending pre-normalization (S4 H9).
//
// Corpus case le-002 is an entire message in bare-CR line endings (classic Mac
// OS). To any CRLF- or LF-oriented parser that file is a single 401-byte line
// with no header/body separator and no delimiter lines anywhere, and both
// libraries duly return one part where the manifest expects two. The manifest is
// describing the DESIRED ENGINE behavior, not predicting the libraries — S4 §6
// records exactly that, and says CR-only mail needs pre-normalization in the
// engine if it is to be supported at all. This file is that decision, made
// explicitly rather than inherited from whatever a library happens to do.
//
// The scope is narrow by construction, and it has to be: the transformation is
// applied ONLY when the buffer contains CR and no LF at all. Corpus case le-010
// (a bare CR inside an otherwise normal body) must be left alone, and this rule
// guarantees it is, because that message contains LFs.

// normalizeLineEndings converts bare-CR input to CRLF.
//
// It returns the possibly-rewritten bytes and whether it did anything. LF-only
// input is deliberately left untouched: both parsers handle it correctly (S4 §6
// notes le-001 is the case that most needed to pass, and it does), and rewriting
// it would risk breaking base64 alignment for no gain.
func normalizeLineEndings(raw []byte) ([]byte, bool) {
	if !bytes.ContainsRune(raw, '\r') {
		return raw, false
	}
	if bytes.ContainsRune(raw, '\n') {
		// Mixed or normal input. Not our case: leave the bytes alone.
		return raw, false
	}
	// CR present, LF absent anywhere: this is a CR-only message.
	return bytes.ReplaceAll(raw, []byte("\r"), []byte("\r\n")), true
}

// canonicalHeaderKey is textproto's canonicalization, applied through one
// function so that every map in this package agrees on the spelling.
//
// Header names containing bytes that are illegal in a field name (spaces, NULs —
// corpus hdr-010 and le-007) make textproto return the input unchanged, which
// would leave two spellings of the same header in one map. Sanitizing first
// keeps the map keys consistent.
func canonicalHeaderKey(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if clean, stripped := sanitizeText(name); stripped {
		name = strings.TrimSpace(clean)
	}
	return textproto.CanonicalMIMEHeaderKey(name)
}

// repairHeaderName fixes the two malformed header-name shapes corpus hdr-010
// puts in one message, and reports whether the line is usable at all.
//
//   - "Subject : x" — whitespace before the colon is illegal (RFC 5322 field
//     names may not contain whitespace), but trimming recovers the only Subject
//     the message has. Dropping it loses real user data, so we trim.
//   - ": empty header name" — no name at all. There is no key to file the value
//     under, so the line is discarded.
func repairHeaderName(name string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", false
	}
	// A field name may not contain whitespace at all; if the remainder still
	// does, the line is too mangled to attribute confidently.
	if strings.ContainsAny(trimmed, " \t") {
		return "", false
	}
	return trimmed, true
}

// stripAngleBrackets removes the < > around a Message-ID or Content-ID.
func stripAngleBrackets(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return strings.TrimSpace(s)
}

// splitMessageIDs splits a References or In-Reply-To value into bare IDs.
//
// Corpus real-world-010 carries a very long References chain folded across many
// lines, which is the ordinary shape of a long thread and must not be truncated.
func splitMessageIDs(s string) []string {
	var out []string
	for {
		start := strings.IndexByte(s, '<')
		if start < 0 {
			break
		}
		end := strings.IndexByte(s[start:], '>')
		if end < 0 {
			break
		}
		id := strings.TrimSpace(s[start+1 : start+end])
		if id != "" {
			out = append(out, id)
		}
		s = s[start+end+1:]
	}
	if len(out) == 0 {
		// No angle brackets at all: some senders emit a bare id. Take
		// whitespace-separated tokens rather than returning nothing.
		for _, f := range strings.Fields(s) {
			if f != "" {
				out = append(out, stripAngleBrackets(f))
			}
		}
	}
	return out
}

// looksLikeHeaderBlock reports whether b plausibly begins with RFC 5322 headers.
//
// Used by the salvage layer to decide whether to try splitting a header block
// off the front of an unparseable message, or to treat the whole thing as body.
func looksLikeHeaderBlock(b []byte) bool {
	const sample = 8 << 10
	if len(b) > sample {
		b = b[:sample]
	}
	lines := bytes.SplitN(b, []byte("\n"), 6)
	seen := 0
	for _, line := range lines {
		line = bytes.TrimRight(line, "\r")
		if len(line) == 0 {
			break
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue // fold continuation
		}
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		name := string(line[:colon])
		if _, ok := repairHeaderName(name); ok {
			seen++
		}
	}
	return seen > 0
}

// splitHeaderBody splits a raw message into its header block and body at the
// first blank line, tolerating the separator variants real mail uses.
//
// Corpus hdr-008 terminates the header block with a line containing a single
// space, which strictly is a fold continuation meaning the headers never end.
// The manifest calls the lenient reading preferred, because the strict one
// leaves the user with no body at all — so a whitespace-only line counts as a
// separator here. Corpus hdr-007 ends mid-header with no blank line at all, so
// EOF must terminate the block too.
func splitHeaderBody(raw []byte) (header, body []byte) {
	for i := 0; i < len(raw); {
		end := bytes.IndexByte(raw[i:], '\n')
		var line []byte
		var next int
		if end < 0 {
			line = raw[i:]
			next = len(raw)
		} else {
			line = raw[i : i+end]
			next = i + end + 1
		}
		if len(bytes.TrimRight(line, " \t\r")) == 0 {
			return raw[:i], raw[next:]
		}
		i = next
	}
	// No blank line anywhere: the whole input is header (corpus hdr-007,
	// structural-001), and the body is empty.
	return raw, nil
}
