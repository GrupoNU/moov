package parser

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The floor of the cascade: salvage (S4 H3).
//
// Three of the 110 corpus cases are rejected by BOTH libraries, and the manifest
// expects two of them to yield usable content anyway:
//
//   - bnd-010 (empty boundary parameter) — expect: partial. The body text is
//     human-readable even though the structure is not recoverable.
//   - structural-004 (multipart with no boundary param) — expect: ok, and the
//     notes are emphatic: "Returning a multipart with zero children and an
//     unreachable body is the failure mode — the user would see a blank message
//     ... losing the bytes is not [defensible]."
//   - hdr-009 (leading continuation line) — expect: partial, with the orphan
//     line discarded and the rest parsed normally.
//
// S4 §2 says plainly that this is engine work and neither library does it. So
// the engine does it here: split the header block off by hand, decode what the
// headers say, and expose the whole body as one text part.
//
// The other half of S4 §2 is a warning that this file also honors: enmime
// DAMAGED the header block while failing on hdr-009 — its error text shows it
// concatenated the orphan line into the following From header. Partial headers
// from a hard-failed parse are therefore never trusted. Salvage re-derives the
// headers from the raw bytes itself rather than reaching into a failed parse.

// parseSalvage is the last layer that can still produce content.
//
// It never fails in the way the other two do: either it finds legible text and
// returns a result, or it reports that it found none and the caller emits
// StatusFailed.
func parseSalvage(raw []byte, limits Limits, priorDefects []Defect) (*ParsedMessage, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}

	out := &ParsedMessage{
		Status:  StatusPartial,
		Parser:  ParserSalvage,
		RawSize: len(raw),
		Defects: priorDefects,
	}

	headerBytes, bodyBytes := splitHeaderBody(raw)

	// If the front of the message does not look like headers at all, the whole
	// input is body. Corpus hdr-011 (no headers whatsoever) takes this path.
	if !looksLikeHeaderBlock(headerBytes) {
		bodyBytes = raw
		headerBytes = nil
	}

	out.Headers = salvageHeaders(headerBytes, out)

	charset := salvageDeclaredCharset(out.Headers)
	res := decodeCharset(bodyBytes, charset, 0)
	text := stripBOM(res.Text)
	if clean, stripped := sanitizeBytes(text); stripped {
		text = clean
		out.addDefect(Defect{
			Code:   DefectNULStripped,
			Part:   0,
			Detail: "NUL bytes removed from salvaged body",
		})
	}

	// The salvage is only worth returning if a human could read the result. A
	// blob of binary presented as a text body would be worse than nothing: it
	// would pollute the search index and show the user garbage.
	if !isLegibleText(text) {
		return nil, false
	}

	mediaType := salvageMediaType(out.Headers.Get("Content-Type"))
	if mediaType == "" || strings.HasPrefix(mediaType, "multipart/") {
		// The declared multipart is uninterpretable, which is exactly why we are
		// here. Degrade it to a text leaf: corpus structural-004 requires the
		// bytes to survive, and whether the part is reported as text/plain or
		// application/octet-stream is explicitly a defensible choice.
		mediaType = "text/plain"
	}

	out.Parts = []Part{{
		Index:            0,
		Parent:           -1,
		Depth:            0,
		MediaType:        mediaType,
		Params:           map[string]string{"charset": res.Charset},
		Charset:          res.Charset,
		CharsetGuessed:   res.Guessed,
		Content:          text,
		Size:             len(text),
		PartiallyDecoded: true,
		Headers:          map[string][]string{},
	}}
	for _, d := range res.Defects {
		out.addDefect(d)
	}
	out.addDefect(Defect{
		Code:       DefectSalvaged,
		Part:       0,
		Detail:     "structure unrecoverable; body exposed as a single text part",
		CorpusCase: "bnd-010/structural-004 (S4 H3, §2)",
	})

	return out, true
}

// salvageHeaders parses a header block by hand, tolerating everything the
// libraries reject.
//
// Written from the corpus cases that reach it:
//   - hdr-009: the first line is a fold continuation with no header to continue.
//     It is DISCARDED — attaching its text to the following From header is
//     called a defect by the case notes, and it is precisely what enmime did.
//   - hdr-005: a line with no colon in the middle of the block. Skipped, so the
//     Content-Type that follows still gets honored.
//   - hdr-010: "Subject : x" is repaired by trimming; ": no name" is dropped.
//   - hdr-007: the block ends mid-header at EOF; the truncated fragment is kept
//     as a header but its value is not trusted for dispatch.
func salvageHeaders(headerBytes []byte, out *ParsedMessage) CanonHeaders {
	h := CanonHeaders{All: map[string][]string{}}
	if len(headerBytes) == 0 {
		h.populate(out)
		return h
	}

	var (
		currentKey string
		currentVal strings.Builder
		first      = true
	)

	flush := func() {
		if currentKey == "" {
			return
		}
		decoded, defects := decodeHeaderValue(currentVal.String(), -1)
		for _, d := range defects {
			out.addDefect(d)
			switch d.Code {
			case DefectRFC2047Retried:
				h.RFC2047Retried = true
			case DefectCharsetGuessed, DefectCharsetUnknown:
				h.CharsetGuessed = true
			default:
			}
		}
		h.All[currentKey] = append(h.All[currentKey], decoded)
		currentKey = ""
		currentVal.Reset()
	}

	for _, rawLine := range bytes.Split(headerBytes, []byte("\n")) {
		line := string(bytes.TrimRight(rawLine, "\r"))
		if line == "" {
			continue
		}

		if line[0] == ' ' || line[0] == '\t' {
			if first || currentKey == "" {
				// An orphaned continuation with nothing to continue (hdr-009).
				out.addDefect(Defect{
					Code:       DefectMalformedHeader,
					Part:       -1,
					Detail:     "orphaned fold continuation discarded",
					CorpusCase: "hdr-009",
				})
				first = false
				continue
			}
			currentVal.WriteByte(' ')
			currentVal.WriteString(strings.TrimSpace(line))
			first = false
			continue
		}
		first = false

		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			// No colon: neither a header nor a continuation (hdr-005). Skipping
			// it keeps the Content-Type that follows, which the stricter reading
			// would lose.
			out.addDefect(Defect{
				Code:       DefectMalformedHeader,
				Part:       -1,
				Detail:     "header line without a colon skipped",
				CorpusCase: "hdr-005",
			})
			continue
		}

		key, ok := repairHeaderName(line[:colon])
		if !ok {
			out.addDefect(Defect{
				Code:       DefectMalformedHeader,
				Part:       -1,
				Detail:     "unusable header name, line discarded",
				CorpusCase: "hdr-010",
			})
			continue
		}

		flush()
		currentKey = canonicalHeaderKey(key)
		currentVal.WriteString(strings.TrimSpace(line[colon+1:]))
	}
	flush()

	h.populate(out)
	return h
}

// salvageDeclaredCharset pulls a charset out of a salvaged Content-Type.
func salvageDeclaredCharset(h CanonHeaders) string {
	ct := h.Get("Content-Type")
	if ct == "" {
		return ""
	}
	if _, params, err := parseMediaType(ct); err == nil {
		return params["charset"]
	}
	return salvageCharsetParam(ct)
}

// isLegibleText reports whether a byte slice is text a human could read.
//
// The threshold question is what separates a useful salvage from garbage in the
// search index. The test is deliberately generous — this runs only after both
// real parsers have already failed, so the alternative to a marginal salvage is
// showing the user nothing at all.
func isLegibleText(b []byte) bool {
	if len(bytes.TrimSpace(b)) == 0 {
		return false
	}
	if !utf8.Valid(b) {
		return false
	}

	var printable, total int
	for _, r := range string(b) {
		if r == utf8.RuneError {
			return false
		}
		total++
		if unicode.IsPrint(r) || unicode.IsSpace(r) {
			printable++
		}
	}
	if total == 0 {
		return false
	}
	// Real text is overwhelmingly printable. Binary misdeclared as text fails
	// this comfortably, while legitimately odd text (control characters in a
	// calendar attachment, say) still passes.
	const minPrintableRatio = 0.85
	return float64(printable)/float64(total) >= minPrintableRatio
}

// utf8Valid is a thin alias so enmime.go can ask the question without importing
// unicode/utf8 for a single call.
func utf8Valid(b []byte) bool { return utf8.Valid(b) }
