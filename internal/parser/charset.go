package parser

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/saintfish/chardet"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// The charset cascade: declared -> heuristic detection -> windows-1252.
//
// S4 §4.3 is the reason this exists and the reason the last step is 1252 rather
// than UTF-8. Legacy single-byte charsets map EVERY possible byte, so a message
// declaring windows-1252 over UTF-8 bytes (corpus cs-002) or ISO-8859-1 over
// KOI8-R bytes (cs-008) decodes with no error at all into text that is
// definitely not what the sender meant. "No parse error" must never be read as
// "text is correct".
//
// windows-1252 is the floor because it is what mail actually contains when it
// lies or says nothing: it is a superset of ISO-8859-1 over the C1 range, where
// real mail puts smart quotes and dashes. Defaulting to UTF-8 instead would turn
// those bytes into replacement characters — research 04 §4.2 settles this, and
// the choice is deliberate enough to be worth restating here.

// charsetResult carries the outcome of decoding one byte slice.
type charsetResult struct {
	// Text is the decoded UTF-8 text. On a decode fault it holds everything that
	// converted before the fault (never nil-because-of-error).
	Text []byte
	// Charset is the charset actually used.
	Charset string
	// Guessed is true when Charset did not come from an honest declaration.
	Guessed bool
	// Defects records what went wrong on the way.
	Defects []Defect
}

// decodeCharset converts raw bytes to UTF-8, running the full cascade.
//
// declared is the charset from the message (possibly "", possibly junk). part is
// the part index for defect attribution, or -1.
func decodeCharset(raw []byte, declared string, part int) charsetResult {
	res := charsetResult{Charset: declared}

	if len(raw) == 0 {
		res.Text = raw
		if res.Charset == "" {
			res.Charset = "utf-8"
		}
		return res
	}

	normalized := normalizeCharsetName(declared)

	// Step 1: the declaration, when it names an encoding we have a table for.
	if normalized != "" {
		if enc, ok := lookupEncoding(normalized); ok {
			// UTF-8 declared and valid is the overwhelmingly common case, and it
			// needs no transformation at all.
			if isUTF8Name(normalized) {
				if utf8.Valid(raw) {
					res.Text = raw
					res.Charset = "utf-8"
					return res
				}
				// Declared UTF-8 but not valid UTF-8 (corpus cs-001, cs-006,
				// cs-014). The declaration is a lie; fall through to detection
				// rather than litter the text with replacement characters.
				res.Defects = append(res.Defects, Defect{
					Code:       DefectCharsetGuessed,
					Part:       part,
					Detail:     "declared utf-8 but bytes are not valid utf-8; detecting",
					CorpusCase: "cs-001/cs-006/cs-014 (S4 §4.3)",
				})
				return detectAndDecode(raw, part, res.Defects)
			}

			text, err := transformBytes(enc, raw)
			res.Text = text
			if err != nil {
				// Keep what converted; a partial decode is real data (S4 §4.2).
				res.Defects = append(res.Defects, Defect{
					Code:       DefectPartialDecode,
					Part:       part,
					Detail:     "charset " + normalized + ": " + err.Error(),
					CorpusCase: "cs-004 (S4 §4.2)",
				})
			}
			return res
		}

		// A charset was declared and it is not one we know. unknown-8bit is the
		// registered RFC 1428 token for "8-bit data, encoding unknown" — an
		// explicit declaration of ignorance by the sender, and the one case where
		// heuristic detection is unambiguously licensed rather than merely
		// tolerated (corpus cs-011).
		res.Defects = append(res.Defects, Defect{
			Code:       DefectCharsetUnknown,
			Part:       part,
			Detail:     "unrecognized charset " + declared,
			CorpusCase: "cs-010/cs-011/cs-013",
		})
		return detectAndDecode(raw, part, res.Defects)
	}

	// Step 2: nothing declared. Valid UTF-8 is taken at face value — this is not
	// a guess, it is a property of the bytes, and UTF-8 validity is a strong
	// enough signal that treating it as detected noise would be perverse.
	if utf8.Valid(raw) {
		res.Text = raw
		res.Charset = "utf-8"
		return res
	}

	return detectAndDecode(raw, part, res.Defects)
}

// detectAndDecode runs steps 2 and 3 of the cascade: chardet, then the
// windows-1252 floor. It always sets Guessed, because by the time it is called
// the message's own declaration has been found absent or untrustworthy.
func detectAndDecode(raw []byte, part int, prior []Defect) charsetResult {
	res := charsetResult{Guessed: true, Defects: prior}

	if name, ok := detectCharset(raw); ok {
		if enc, encOK := lookupEncoding(name); encOK {
			text, err := transformBytes(enc, raw)
			if err == nil || len(text) > 0 {
				res.Text = text
				res.Charset = name
				res.Defects = append(res.Defects, Defect{
					Code:       DefectCharsetGuessed,
					Part:       part,
					Detail:     "detected " + name,
					CorpusCase: "S4 H6",
				})
				if err != nil {
					res.Defects = append(res.Defects, Defect{
						Code:   DefectPartialDecode,
						Part:   part,
						Detail: "detected charset " + name + ": " + err.Error(),
					})
				}
				return res
			}
		}
	}

	// Step 3: the floor. windows-1252 maps every byte, so this cannot fail and
	// cannot lose bytes — it can only be wrong, which is why Guessed is set.
	text, _ := transformBytes(charmap.Windows1252, raw)
	res.Text = text
	res.Charset = "windows-1252"
	res.Defects = append(res.Defects, Defect{
		Code:       DefectCharsetGuessed,
		Part:       part,
		Detail:     "fell through to windows-1252 (research 04 §4.2: the fallback is 1252, not utf-8)",
		CorpusCase: "S4 H6",
	})
	return res
}

// detectorPool is shared: chardet.Detector is stateless in practice but
// allocating one per part is wasteful on a 1000-part message.
var textDetector = chardet.NewTextDetector()

// detectCharset runs the heuristic detector, returning the charset name and
// whether the result is worth acting on.
func detectCharset(raw []byte) (string, bool) {
	// chardet on a very short sample is close to a coin flip. Below this many
	// bytes the windows-1252 floor is the more honest answer, since it at least
	// never destroys bytes.
	const minDetectBytes = 16
	if len(raw) < minDetectBytes {
		return "", false
	}

	// Bound the sample: detection quality plateaus long before a 25 MB body, and
	// scanning all of it on every part is pure cost.
	const maxDetectBytes = 64 << 10
	sample := raw
	if len(sample) > maxDetectBytes {
		sample = sample[:maxDetectBytes]
	}

	result, err := textDetector.DetectBest(sample)
	if err != nil || result == nil {
		return "", false
	}

	// chardet reports a 0-100 confidence. Low-confidence guesses are worse than
	// the deterministic 1252 floor, because they vary with content in ways no
	// operator can reason about.
	const minConfidence = 30
	if result.Confidence < minConfidence {
		return "", false
	}
	return strings.ToLower(result.Charset), true
}

// lookupEncoding resolves a charset name to a decoder, trying the IANA registry
// first and the HTML index second. The HTML index is consulted because it
// carries the aliases mail actually uses in the wild (and maps the legacy
// "x-" spellings), which ianaindex alone rejects.
func lookupEncoding(name string) (encoding.Encoding, bool) {
	if name == "" {
		return nil, false
	}
	if enc, err := ianaindex.MIME.Encoding(name); err == nil && enc != nil {
		return enc, true
	}
	if enc, err := ianaindex.IANA.Encoding(name); err == nil && enc != nil {
		return enc, true
	}
	if enc, err := htmlindex.Get(name); err == nil && enc != nil {
		return enc, true
	}
	return nil, false
}

// normalizeCharsetName cleans up the charset label as it appears in real mail:
// quoted, uppercased, whitespace-padded, or carrying junk after a semicolon.
func normalizeCharsetName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, `"'`)
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	// Some senders emit charset="utf-8; format=flowed" — the parameter split
	// already happened, but a stray semicolon survives often enough to handle.
	if i := strings.IndexAny(name, ";,"); i >= 0 {
		name = strings.TrimSpace(name[:i])
	}
	lower := strings.ToLower(name)

	// Aliases that appear in mail and that neither index resolves usefully.
	switch lower {
	case "unknown-8bit", "unknown", "x-unknown", "none", "default", "ansi_x3.4-1968":
		// Explicit ignorance or a meaningless label: let the cascade detect.
		return lower
	case "x-user-defined":
		// Netscape's "raw bytes" label (corpus cs-010). Byte-for-byte it behaves
		// like Latin-1 for the low range; 1252 is the better superset for mail.
		return "windows-1252"
	case "iso-8859-8-i", "iso-8859-8i":
		// Logical-order Hebrew: the same table, different display direction.
		return "iso-8859-8"
	case "cp932", "windows-932":
		return "shift_jis"
	case "utf8":
		return "utf-8"
	}
	return lower
}

// isUTF8Name reports whether the name denotes UTF-8 (with or without BOM).
func isUTF8Name(name string) bool {
	switch name {
	case "utf-8", "utf8", "unicode-1-1-utf-8", "csutf8":
		return true
	}
	return false
}

// transformBytes decodes raw with enc, returning the bytes converted before any
// fault alongside the fault itself.
//
// This is the S4 §4.2 mitigation applied to charset conversion. transform.Bytes
// returns (converted, err) with converted populated up to the failure point; the
// idiomatic `if err != nil { return nil, err }` throws away recoverable text.
func transformBytes(enc encoding.Encoding, raw []byte) ([]byte, error) {
	if enc == nil {
		return raw, nil
	}
	out, _, err := transform.Bytes(enc.NewDecoder(), raw)
	if err != nil && len(out) == 0 {
		// Nothing converted at all. A decoder substitutes U+FFFD for bytes it
		// cannot map, so reaching here means the transform aborted on a
		// structural fault (a truncated multi-byte sequence, or an ISO-2022
		// escape cut in half — corpus cs-004). Retry byte by byte so the legible
		// prefix survives instead of the whole part coming back empty.
		return decodePrefix(enc, raw), err
	}
	return out, err
}

// decodePrefix finds the longest prefix of raw that enc can decode, by binary
// search on the input length.
//
// This is the charset-layer expression of the same principle as S4 §4.2: bytes
// that converted successfully are real data and must not be thrown away because
// later bytes are broken. A message whose text is fine until a truncated escape
// sequence should show the user the text, not an empty body.
func decodePrefix(enc encoding.Encoding, raw []byte) []byte {
	lo, hi := 0, len(raw)
	var best []byte
	for lo <= hi {
		mid := (lo + hi) / 2
		out, _, err := transform.Bytes(enc.NewDecoder(), raw[:mid])
		if err == nil {
			best = out
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best
}

// stripBOM removes a leading byte-order mark, which otherwise reaches the search
// index as a zero-width character and shows up in the UI (corpus cs-014).
func stripBOM(b []byte) []byte {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return b[3:]
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}), bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return b[2:]
	}
	return b
}

// utf16BOMDecode handles the case of a text part whose bytes carry a UTF-16 BOM
// regardless of what was declared. Mail from some Windows clients does this.
func utf16BOMDecode(raw []byte) ([]byte, bool) {
	if len(raw) < 2 {
		return nil, false
	}
	var enc encoding.Encoding
	switch {
	case raw[0] == 0xFE && raw[1] == 0xFF:
		enc = unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM)
	case raw[0] == 0xFF && raw[1] == 0xFE:
		enc = unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM)
	default:
		return nil, false
	}
	out, err := transformBytes(enc, raw)
	if err != nil && len(out) == 0 {
		return nil, false
	}
	return out, true
}

// sanitizeText makes decoded text safe to store in PostgreSQL and sane to index.
//
// NUL is the specific hazard: it cannot be stored in a PostgreSQL text column at
// all, and a C-string-based consumer downstream truncates at it silently. Corpus
// case le-007 puts NULs in a header value and a header name precisely to force
// this decision. Stripping (rather than substituting U+FFFD) is what the case's
// notes name first, and it yields readable text.
//
// Returns the cleaned text and whether anything was removed.
func sanitizeText(s string) (string, bool) {
	if !strings.ContainsRune(s, 0) {
		return s, false
	}
	return strings.ReplaceAll(s, "\x00", ""), true
}

// sanitizeBytes is sanitizeText for byte slices.
func sanitizeBytes(b []byte) ([]byte, bool) {
	if !bytes.ContainsRune(b, 0) {
		return b, false
	}
	return bytes.ReplaceAll(b, []byte{0}, nil), true
}
