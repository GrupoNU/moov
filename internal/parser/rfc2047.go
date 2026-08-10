package parser

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"regexp"
	"strings"
	"unicode/utf8"
)

// RFC 2047 encoded-word decoding, with the retry pass that S4 §4.1 requires.
//
// The finding: on corpus case ew-004 the subject is
//
//	=?UTF-8?B?UmV1bmnDs24gbWVuc3VhbA?=
//
// whose base64 payload is 22 characters, so len % 4 == 2. That is unambiguously
// decodable — Go's own base64.RawStdEncoding decodes it to "Reunión mensual" —
// but BOTH go-message and enmime decline to decode it, emit the raw encoded-word
// as the subject, and report NO error and NO defect. The user sees MIME markup
// in their subject line and nothing downstream can tell that anything is wrong.
//
// The mitigation is cheap and it is this file: decode normally, then look for a
// residual "=?...?=" in the result, and if one is there retry it with the raw
// (unpadded) base64 encodings. It converts a visible user-facing defect into a
// correct subject.

// encodedWordRe matches an RFC 2047 encoded-word. The charset and encoding
// tokens are deliberately permissive — the point is to FIND words the standard
// decoder left behind, including malformed ones, not to validate them.
var encodedWordRe = regexp.MustCompile(`=\?([^?]*)\?([bBqQ])\?([^?]*)\?=`)

// headerDecoder is the standard decoder, extended with our charset cascade so
// that an encoded-word naming a legacy charset decodes instead of erroring.
var headerDecoder = mime.WordDecoder{
	CharsetReader: charsetReaderFor,
}

// decodeHeaderValue decodes one header value: unfold, RFC 2047 decode, retry any
// residual encoded-words, strip NULs.
//
// It never returns an error. A header that cannot be decoded yields the most
// legible text available plus defects, because a message must never be lost over
// a subject line.
func decodeHeaderValue(raw string, part int) (string, []Defect) {
	var defects []Defect

	value := unfoldHeader(raw)

	decoded, err := headerDecoder.DecodeHeader(value)
	if err != nil || decoded == "" && value != "" {
		// The standard decoder gave up on the whole string. Fall back to
		// decoding word by word so one bad encoded-word cannot cost the rest of
		// the header.
		decoded = decodeWordwise(value)
	}

	// The S4 §4.1 retry pass.
	if encodedWordRe.MatchString(decoded) {
		defects = append(defects, Defect{
			Code:       DefectRFC2047Residual,
			Part:       part,
			Detail:     "encoded-word survived standard decoding",
			CorpusCase: "ew-004 (S4 §4.1)",
		})
		retried, changed := retryEncodedWords(decoded)
		if changed {
			decoded = retried
			defects = append(defects, Defect{
				Code:       DefectRFC2047Retried,
				Part:       part,
				Detail:     "decoded with raw (unpadded) base64",
				CorpusCase: "ew-004 (S4 §4.1)",
			})
		}
	}

	if clean, stripped := sanitizeText(decoded); stripped {
		decoded = clean
		defects = append(defects, Defect{
			Code:       DefectNULStripped,
			Part:       part,
			Detail:     "NUL bytes removed from header value",
			CorpusCase: "le-007",
		})
	}

	// Raw 8-bit bytes in a header value (corpus hdr-003) never pass through an
	// encoded-word, so nothing above has decoded them: they arrive here as
	// whatever bytes the sender emitted. RFC 5322 requires header values to be
	// ASCII and RFC 2047 exists precisely so they can carry more, but real mail
	// puts raw Latin-1 and UTF-8 in headers constantly.
	//
	// Left alone, those bytes reach the store as invalid UTF-8, and the tsv and
	// subject columns are PostgreSQL TEXT — which rejects them outright. Running
	// the same charset cascade used for bodies is the fix, and it must be here
	// rather than at the call sites, because every header path funnels through
	// this function.
	//
	// Found by fuzzing: the invariant "everything this package emits is valid
	// UTF-8" was asserted in FuzzParse before it was true.
	if !utf8.ValidString(decoded) {
		res := decodeCharset([]byte(decoded), "", part)
		decoded = string(res.Text)
		defects = append(defects, Defect{
			Code:       DefectCharsetGuessed,
			Part:       part,
			Detail:     "raw 8-bit header bytes decoded as " + res.Charset,
			CorpusCase: "hdr-003 (S4 H6)",
		})
		// The cascade's floor is windows-1252, which maps every byte, so this
		// should be unreachable. Coercing anyway keeps the package's UTF-8
		// guarantee total rather than nearly-total: a consumer must never have to
		// re-validate what this package returns.
		if !utf8.ValidString(decoded) {
			decoded = strings.ToValidUTF8(decoded, "�")
		}
	}

	return decoded, defects
}

// decodeWordwise decodes each encoded-word independently, leaving text that
// fails to decode exactly as it was found.
//
// This matters for corpus ew-007 and ew-010, where an encoded-word is glued to
// plain text or never terminated: the standard decoder can reject the entire
// header, and returning nothing would lose a subject that is mostly readable.
func decodeWordwise(value string) string {
	var b strings.Builder
	last := 0
	for _, loc := range encodedWordRe.FindAllStringIndex(value, -1) {
		b.WriteString(value[last:loc[0]])
		word := value[loc[0]:loc[1]]
		if dec, err := headerDecoder.Decode(word); err == nil {
			b.WriteString(dec)
		} else if dec, ok := decodeEncodedWordRaw(word); ok {
			b.WriteString(dec)
		} else {
			b.WriteString(word)
		}
		last = loc[1]
	}
	b.WriteString(value[last:])
	return b.String()
}

// retryEncodedWords re-attempts every residual encoded-word with raw base64.
// Returns the rewritten string and whether anything actually changed.
func retryEncodedWords(s string) (string, bool) {
	changed := false
	out := encodedWordRe.ReplaceAllStringFunc(s, func(word string) string {
		if dec, ok := decodeEncodedWordRaw(word); ok {
			changed = true
			return dec
		}
		return word
	})
	return out, changed
}

// decodeEncodedWordRaw decodes an encoded-word using the unpadded base64
// encodings the standard decoder refuses, then runs the payload through the
// charset cascade.
func decodeEncodedWordRaw(word string) (string, bool) {
	m := encodedWordRe.FindStringSubmatch(word)
	if m == nil {
		return "", false
	}
	charsetName, enc, payload := m[1], strings.ToLower(m[2]), m[3]

	var decoded []byte
	switch enc {
	case "b":
		// The whole point: RawStdEncoding accepts len%4==2 and len%4==3, which
		// StdEncoding rejects. Both raw variants are tried because base64url
		// shows up in encoded-words from a few broken generators.
		for _, e := range []*base64.Encoding{
			base64.RawStdEncoding, base64.RawURLEncoding,
			base64.StdEncoding, base64.URLEncoding,
		} {
			if b, err := e.DecodeString(payload); err == nil {
				decoded = b
				break
			} else if len(b) > 0 && decoded == nil {
				// Keep a partial decode rather than nothing (S4 §4.2).
				decoded = b
			}
		}
	case "q":
		b, err := decodeQuotedPrintableWord(payload)
		if err != nil && len(b) == 0 {
			return "", false
		}
		decoded = b
	default:
		return "", false
	}

	if decoded == nil {
		return "", false
	}

	res := decodeCharset(decoded, charsetName, -1)
	text := string(stripBOM(res.Text))
	if text == "" {
		return "", false
	}
	return text, true
}

// decodeQuotedPrintableWord decodes the Q encoding of RFC 2047, which differs
// from ordinary quoted-printable in one respect: '_' means a space.
func decodeQuotedPrintableWord(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '_':
			out = append(out, ' ')
		case '=':
			if i+2 >= len(s) {
				// Truncated escape at end of word: keep the literal bytes rather
				// than dropping them (corpus ew-011, cte-006).
				out = append(out, s[i:]...)
				return out, errTruncatedEscape
			}
			hi, hiOK := unhex(s[i+1])
			lo, loOK := unhex(s[i+2])
			if !hiOK || !loOK {
				// Invalid escape: RFC-conforming decoders differ here. Keeping
				// the literal text is the reading that loses nothing.
				out = append(out, c)
				continue
			}
			out = append(out, hi<<4|lo)
			i += 2
		default:
			out = append(out, c)
		}
	}
	return out, nil
}

// errTruncatedEscape marks a Q-encoded word ending mid-escape.
var errTruncatedEscape = errStr("truncated quoted-printable escape")

// errStr is a tiny error type, so this package needs no error-wrapping ceremony
// for the handful of sentinel conditions it reports internally.
type errStr string

func (e errStr) Error() string { return string(e) }

// unhex decodes one hexadecimal digit, accepting both cases (corpus cte-007
// covers lowercase hex, which some MUAs emit despite the RFC requiring upper).
func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// charsetReaderFor adapts our charset cascade to mime.WordDecoder, so that an
// encoded-word declaring KOI8-R or GB18030 decodes rather than erroring out.
func charsetReaderFor(charsetName string, input io.Reader) (io.Reader, error) {
	raw, err := io.ReadAll(input)
	// Partial reads are still data (S4 §4.2).
	if err != nil && len(raw) == 0 {
		return nil, err
	}
	res := decodeCharset(raw, charsetName, -1)
	return bytes.NewReader(res.Text), nil
}

// unfoldHeader joins a folded header value into one line.
//
// RFC 5322 folding inserts CRLF before leading whitespace; unfolding replaces
// the line break with nothing and keeps the whitespace. Corpus hdr-004 folds
// with mixed tabs and spaces, and ew-012 folds in the MIDDLE of an encoded-word,
// which is illegal but real — joining first is what lets the encoded-word
// decoder see a whole word.
func unfoldHeader(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			b.WriteByte(' ')
			// Swallow the folding whitespace that follows the break, so the
			// value keeps exactly one separating space.
			for i+1 < len(s) && (s[i+1] == ' ' || s[i+1] == '\t') {
				i++
			}
		case '\n':
			b.WriteByte(' ')
			for i+1 < len(s) && (s[i+1] == ' ' || s[i+1] == '\t') {
				i++
			}
		default:
			b.WriteByte(s[i])
		}
	}
	return strings.TrimSpace(b.String())
}

// joinEncodedWordFolds removes the whitespace BETWEEN two adjacent
// encoded-words, which RFC 2047 §6.2 requires a decoder to drop.
//
// Corpus ew-001 and ew-002 split a multi-byte character across two encoded-words
// precisely to test this: without the join, "acci" + "ón" arrives as "acci ón".
func joinEncodedWordFolds(s string) string {
	locs := encodedWordRe.FindAllStringIndex(s, -1)
	if len(locs) < 2 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for i := 1; i < len(locs); i++ {
		gapStart, gapEnd := locs[i-1][1], locs[i][0]
		if gapEnd <= gapStart {
			continue
		}
		if strings.TrimSpace(s[gapStart:gapEnd]) == "" {
			b.WriteString(s[last:gapStart])
			last = gapEnd
		}
	}
	b.WriteString(s[last:])
	return b.String()
}
