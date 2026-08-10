package parser

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"strings"

	"github.com/emersion/go-message"
)

// The primary cascade layer: emersion/go-message.
//
// It is first because it streams rather than building the whole tree in memory
// (S4 §5 measured 3 MB against enmime's 50 MB on a 1000-part message), and
// because it is the more conservative of the two — when it succeeds, it is
// trustworthy. When it does not, enmime gets its turn.
//
// go-message's own charset handling is deliberately NOT used. Registering our
// cascade globally via message.CharsetReader would be a process-wide mutation
// from a library package, and it would hide the guessing that S4 §4.3 says must
// be visible. Every part's bytes come out raw and go through decodeCharset here,
// where the charset_guessed flag can be recorded per part.

// parseGoMessage runs the primary layer. It returns a result, whether the tree
// it produced is known to be incomplete, and an error; a non-nil error means this
// layer declined and the cascade should move on.
func parseGoMessage(raw []byte, limits Limits) (*ParsedMessage, bool, error) {
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		// message.Read reports unknown charsets as a non-fatal error alongside a
		// usable entity. Anything else is a genuine refusal.
		if entity == nil || !message.IsUnknownCharset(err) {
			return nil, false, err
		}
	}

	out := &ParsedMessage{
		Status:  StatusOK,
		Parser:  ParserGoMessage,
		RawSize: len(raw),
	}

	w := &treeWalker{limits: limits, out: out}
	if err := w.walk(entity, -1, 0, 0); err != nil {
		// The partially-built message is returned alongside the error so the
		// cascade can read WHICH cap fired off its defect list. Its content is
		// not used — a capped parse is a failure, and failedMessage builds the
		// result — but "the message was refused" and "the message was refused
		// because it was 500 levels deep" are very different things to an
		// operator reading the parse_status=failed metric.
		return out, false, err
	}
	if len(out.Parts) == 0 {
		return nil, false, errors.New("go-message: no parts extracted")
	}

	out.Headers = canonHeadersFromFields(entity.Header.Fields(), out)
	return out, w.truncatedMultipart, nil
}

// treeWalker carries the state a recursive MIME walk needs: the caps, the
// message being built, and the rfc822 descent budget.
// The rfc822 descent depth is a parameter of walk rather than a field here: it
// increases only along the path being walked and must unwind on the way back
// out, which a parameter does for free and a field would get wrong for siblings.
type treeWalker struct {
	limits Limits
	out    *ParsedMessage

	// truncatedMultipart records that a multipart iteration ended on an error
	// rather than on EOF, so some siblings were never reached.
	//
	// This is the signal the cascade needs to escalate. go-message does not fail
	// the message in this situation — it returns the parts it managed to read and
	// stops — which looks like a successful parse from the outside. On corpus
	// cases le-003, cs-015 and structural-015 that means 1 part where the
	// manifest expects 2, 4 and 3, and the missing text is simply absent from
	// the search index with nothing to indicate it. Silent partial structure is
	// precisely the "silent wrong data" class S4 §4 ranks above hard failures.
	truncatedMultipart bool
}

// errCapExceeded aborts a walk when a cap fires. It is a sentinel rather than a
// defect because exceeding a cap fails the whole message (L2 §2.4), and the
// cascade must not then try the same oversized message with the other library.
var errCapExceeded = errors.New("parser: resource cap exceeded")

// walk descends one entity, appending it and its children to the flattened part
// list. parent is the index of the enclosing part, or -1 for the root.
func (w *treeWalker) walk(e *message.Entity, parent, depth, rfc822Depth int) error {
	if depth > w.limits.MaxDepth {
		w.out.addDefect(Defect{
			Code:       DefectDepthCapExceeded,
			Part:       -1,
			Detail:     "MIME nesting deeper than the configured cap",
			CorpusCase: "nest-003/nest-004 (S4 §5, convention C4)",
		})
		return errCapExceeded
	}
	if len(w.out.Parts) >= w.limits.MaxParts {
		w.out.addDefect(Defect{
			Code:       DefectPartCapExceeded,
			Part:       -1,
			Detail:     "more parts than the configured cap",
			CorpusCase: "nest-007 (S4 §5, convention C4)",
		})
		return errCapExceeded
	}

	part := w.newPart(e, parent, depth)
	idx := part.Index
	w.out.Parts = append(w.out.Parts, part)

	if mr := e.MultipartReader(); mr != nil {
		w.out.Parts[idx].IsMultipart = true
		for {
			child, err := mr.NextPart()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				// A truncated or malformed multipart. Everything read so far is
				// kept — that is the whole point of the cascade's floor — and the
				// message is downgraded rather than lost.
				w.out.addDefect(Defect{
					Code:       DefectBodyReadError,
					Part:       idx,
					Detail:     "multipart read: " + err.Error(),
					CorpusCase: "bnd-001/bnd-003/bnd-005",
				})
				w.out.downgrade(StatusPartial)
				w.truncatedMultipart = true
				break
			}
			if err := w.walk(child, idx, depth+1, rfc822Depth); err != nil {
				return err
			}
		}
		return nil
	}

	// A leaf. Read its decoded content, keeping partial reads.
	content, partial, defects := readPartBody(e, idx, w.limits.MaxPartSize)
	for _, d := range defects {
		w.out.addDefect(d)
	}
	if partial {
		w.out.downgrade(StatusPartial)
	}

	if w.out.Parts[idx].IsRFC822 {
		return w.descendRFC822(idx, content, depth, rfc822Depth)
	}

	w.finishLeaf(idx, content, partial)
	return nil
}

// descendRFC822 re-parses an embedded message so its text reaches the index.
//
// S4 §4.4: neither library descends into message/rfc822 — both report the
// embedded message as one opaque leaf. That is defensible parser behavior, but
// it is load-bearing for the product, because users expect to find text inside
// forwarded mail. The engine has to do this itself, which is what this does.
//
// The descent budget is separate from MaxDepth, as corpus nest-006 explicitly
// licenses: forwarded chains are realistic, and eager descent multiplies work at
// every level. Running out of budget leaves the wrapper opaque and marks the
// message partial — the bytes are still there, only the interior is unindexed.
func (w *treeWalker) descendRFC822(idx int, content []byte, depth, rfc822Depth int) error {
	// The wrapper keeps its own bytes regardless of whether we descend.
	w.out.Parts[idx].Content = content
	w.out.Parts[idx].Size = len(content)

	if rfc822Depth >= w.limits.MaxRFC822Depth {
		w.out.addDefect(Defect{
			Code:       DefectRFC822DepthCapped,
			Part:       idx,
			Detail:     "embedded message left opaque at the rfc822 descent cap",
			CorpusCase: "nest-006 (S4 §4.4)",
		})
		w.out.downgrade(StatusPartial)
		return nil
	}
	if len(content) == 0 {
		return nil
	}

	inner, err := message.Read(bytes.NewReader(content))
	if err != nil && (inner == nil || !message.IsUnknownCharset(err)) {
		// The embedded message is not a message after all (corpus
		// structural-016). Keep the wrapper as an opaque leaf; nothing is lost.
		w.out.addDefect(Defect{
			Code:       DefectMalformedHeader,
			Part:       idx,
			Detail:     "message/rfc822 part is not a parseable message: " + err.Error(),
			CorpusCase: "structural-016",
		})
		return nil
	}
	return w.walk(inner, idx, depth+1, rfc822Depth+1)
}

// finishLeaf decodes a leaf's text and records it.
func (w *treeWalker) finishLeaf(idx int, content []byte, partial bool) {
	p := &w.out.Parts[idx]

	if p.IsText() {
		if utf16, ok := utf16BOMDecode(content); ok {
			content = utf16
			p.Charset = "utf-16"
			p.CharsetGuessed = true
		} else {
			res := decodeCharset(content, p.Charset, idx)
			content = res.Text
			p.Charset = res.Charset
			p.CharsetGuessed = res.Guessed
			for _, d := range res.Defects {
				w.out.addDefect(d)
				if d.Code == DefectCharsetGuessed || d.Code == DefectPartialDecode ||
					d.Code == DefectCharsetUnknown {
					w.out.downgrade(StatusPartial)
				}
			}
		}
		content = stripBOM(content)
		if clean, stripped := sanitizeBytes(content); stripped {
			content = clean
			w.out.addDefect(Defect{
				Code:       DefectNULStripped,
				Part:       idx,
				Detail:     "NUL bytes removed from text body",
				CorpusCase: "le-008",
			})
			w.out.downgrade(StatusPartial)
		}
	}

	p.Content = content
	p.Size = len(content)
	p.PartiallyDecoded = partial
}

// newPart builds a Part from an entity's headers, without its content.
func (w *treeWalker) newPart(e *message.Entity, parent, depth int) Part {
	p := Part{
		Index:     len(w.out.Parts),
		Parent:    parent,
		Depth:     depth,
		MediaType: "text/plain",
		Params:    map[string]string{},
		Headers:   map[string][]string{},
	}

	fields := e.Header.Fields()
	for fields.Next() {
		key := canonicalHeaderKey(fields.Key())
		if key == "" {
			continue
		}
		value, _ := fields.Text()
		if value == "" {
			value = fields.Value()
		}
		p.Headers[key] = append(p.Headers[key], value)
	}

	if ct := e.Header.Get("Content-Type"); ct != "" {
		mt, params, err := parseMediaType(ct)
		switch {
		case err == nil && !isDispatchableMediaType(mt):
			// A syntactically valid but truncated media type, which is what a
			// message cut off mid-header leaves behind ("text/pla" in corpus
			// hdr-007). Its notes are explicit that such a value "is not a valid
			// media type and must not be used for dispatch, so the part should
			// fall back to text/plain".
			w.out.addDefect(Defect{
				Code:       DefectMalformedHeader,
				Part:       p.Index,
				Detail:     "truncated or unregistered media type " + mt + "; using text/plain",
				CorpusCase: "hdr-007",
			})
			w.out.downgrade(StatusPartial)
			p.Params = params
		case err == nil:
			p.MediaType = mt
			p.Params = params
		default:
			// A Content-Type we cannot parse (corpus cs-013 duplicate parameter,
			// hdr-006 two disagreeing headers). Default to text/plain per RFC
			// 2045 rather than dropping the part.
			w.out.addDefect(Defect{
				Code:       DefectMalformedHeader,
				Part:       p.Index,
				Detail:     "Content-Type: " + err.Error(),
				CorpusCase: "cs-013/hdr-006",
			})
			// Salvage the media type textually, since the parameters are what
			// usually break, not the type itself.
			if mt := salvageMediaType(ct); mt != "" {
				p.MediaType = mt
			}
			if cs := salvageCharsetParam(ct); cs != "" {
				p.Params["charset"] = cs
			}
		}
	}

	p.Charset = p.Params["charset"]
	p.Encoding = strings.ToLower(strings.TrimSpace(e.Header.Get("Content-Transfer-Encoding")))
	p.ContentID = stripAngleBrackets(e.Header.Get("Content-ID"))
	p.IsRFC822 = p.MediaType == "message/rfc822"

	if cd := e.Header.Get("Content-Disposition"); cd != "" {
		disp, params, err := parseMediaType(cd)
		if err == nil {
			p.Disposition = disp
			if fn := params["filename"]; fn != "" {
				p.Filename = fn
			}
		} else if disp := salvageMediaType(cd); disp != "" {
			p.Disposition = disp
		}
	}
	if p.Filename == "" {
		p.Filename = p.Params["name"]
	}
	if p.Filename != "" {
		if decoded, defects := decodeHeaderValue(p.Filename, p.Index); decoded != "" {
			p.Filename = decoded
			for _, d := range defects {
				w.out.addDefect(d)
			}
		}
	}

	p.IsAttachment = isAttachmentPart(p)
	return p
}

// isDispatchableMediaType reports whether a media type is one this engine will
// act on, as opposed to a fragment left by a truncated header.
//
// The check is on the TOP-LEVEL type only, which is the part RFC 2045 fixes to a
// closed set (plus the "x-" extension space). Subtypes are open-ended and must
// not be validated against a list — an unrecognized subtype is ordinary, whereas
// an unrecognized top-level type means the header did not survive intact.
func isDispatchableMediaType(mt string) bool {
	slash := strings.IndexByte(mt, '/')
	if slash <= 0 || slash == len(mt)-1 {
		return false
	}
	switch mt[:slash] {
	case "text", "image", "audio", "video", "application", "multipart",
		"message", "model", "font", "example":
		return true
	}
	return strings.HasPrefix(mt, "x-")
}

// isAttachmentPart decides attachment-ness at the PARSE layer only.
//
// Corpus convention C2 is explicit that this is not the presentation decision: a
// Content-ID referenced by a cid: URL in sibling HTML should be RENDERED inline
// rather than listed, but that rule needs the sibling parts and belongs to the
// engine, not here. What this answers is the narrower question: would a parser
// reasonably report this part as an attachment from the message bytes alone.
func isAttachmentPart(p Part) bool {
	if p.IsMultipart {
		return false
	}
	// RFC 1847 protocol parts are machinery, not user content, and must never be
	// offered as a download — corpus real-world-002 is explicit that the
	// application/pkcs7-signature of a multipart/signed is not an attachment
	// (while still being retained byte-for-byte for later re-verification, which
	// it is: the part and its Content survive in Parts either way).
	if isProtocolPart(p.MediaType) {
		return false
	}
	switch p.Disposition {
	case "attachment":
		return true
	case "inline":
		// An inline part with a filename is still a file the user can save, but
		// an inline text part is body content.
		return p.Filename != "" && !p.IsText()
	}
	// No disposition at all: a filename is the remaining signal, and non-text
	// media with a name is an attachment in every mail client.
	if p.Filename != "" && !p.IsText() {
		return true
	}
	return false
}

// isProtocolPart reports whether a media type is cryptographic or reporting
// machinery that belongs to the message format rather than to the user.
//
// These parts must be kept in the store (a signature has to survive for later
// re-verification, and a delivery-status report is what makes a bounce
// actionable) but must never be listed as downloadable attachments.
func isProtocolPart(mediaType string) bool {
	switch mediaType {
	case "application/pkcs7-signature", "application/x-pkcs7-signature",
		"application/pgp-signature", "application/pgp-encrypted",
		"message/delivery-status", "message/disposition-notification",
		"message/feedback-report", "text/rfc822-headers":
		return true
	}
	return false
}

// parseMediaType wraps mime.ParseMediaType, lowercasing the type and the
// parameter keys so that every comparison in this package is case-correct.
func parseMediaType(v string) (string, map[string]string, error) {
	mt, params, err := mime.ParseMediaType(v)
	if err != nil {
		return "", nil, err
	}
	out := make(map[string]string, len(params))
	for k, val := range params {
		out[strings.ToLower(k)] = val
	}
	return strings.ToLower(strings.TrimSpace(mt)), out, nil
}

// salvageMediaType extracts the "type/subtype" from a Content-Type value whose
// PARAMETERS are malformed, which is the common failure (corpus cs-013 declares
// charset twice, and Go's mime rejects the whole header for it).
func salvageMediaType(v string) string {
	if i := strings.IndexByte(v, ';'); i >= 0 {
		v = v[:i]
	}
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" || !strings.Contains(v, "/") {
		return ""
	}
	for _, r := range v {
		if r <= ' ' || r == '"' {
			return ""
		}
	}
	return v
}

// salvageCharsetParam pulls the first charset parameter out of a Content-Type
// that mime.ParseMediaType refused, so a duplicate-parameter header still yields
// a usable charset instead of falling all the way to detection.
func salvageCharsetParam(v string) string {
	// The index MUST be found with ASCII-only case folding, not strings.ToLower.
	// ToLower is Unicode-aware and can change the BYTE LENGTH of a string — the
	// Kelvin sign U+212A lowercases to ASCII "k", three bytes becoming one — so an
	// offset into the lowercased copy does not necessarily address the same
	// position in the original. Indexing v with it panics with a slice bounds
	// error on any header carrying such a rune before the charset parameter.
	//
	// Found by fuzzing a mutated corpus header. It is worth stating plainly
	// because the bug is invisible in review and reachable by any sender: a panic
	// here kills the sync worker, which is the failure mode the whole package
	// exists to avoid.
	i := indexFoldASCII([]byte(v), []byte("charset="))
	if i < 0 {
		return ""
	}
	rest := v[i+len("charset="):]
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return ""
	}
	if rest[0] == '"' {
		if end := strings.IndexByte(rest[1:], '"'); end >= 0 {
			return rest[1 : 1+end]
		}
		return strings.Trim(rest, `"`)
	}
	if end := strings.IndexAny(rest, "; \t"); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// readPartBody reads a leaf's transfer-decoded bytes.
//
// This is the S4 §4.2 mitigation, and it is the highest-value single lesson of
// the spike. go-message's decoder returns PARTIAL BYTES alongside its error on a
// lying Content-Transfer-Encoding (corpus cte-001, cte-003). io.ReadAll returns
// (data, err) with data populated up to the failure point, and the idiomatic
//
//	if err != nil { return nil, err }
//
// throws away recoverable content. On cte-003 that is the difference between
// "payload with" and nothing at all. The spike's own harness got this wrong the
// first time and misreported it as parser data loss.
//
// Returns the bytes, whether the read was cut short, and any defects.
func readPartBody(e *message.Entity, idx int, maxSize int64) ([]byte, bool, []Defect) {
	var defects []Defect

	reader := e.Body
	if maxSize > 0 {
		// +1 so that hitting exactly maxSize is distinguishable from exceeding it.
		reader = io.LimitReader(reader, maxSize+1)
	}

	var buf bytes.Buffer
	_, err := io.Copy(&buf, reader)
	content := buf.Bytes()
	partial := false

	if err != nil {
		partial = true
		code := DefectBodyReadError
		corpus := ""
		if isEncodingError(err) {
			code = DefectPartialDecode
			corpus = "cte-001/cte-003 (S4 §4.2)"
		}
		defects = append(defects, Defect{
			Code:       code,
			Part:       idx,
			Detail:     err.Error() + " (kept " + itoa(len(content)) + " decoded bytes)",
			CorpusCase: corpus,
		})
	}

	if maxSize > 0 && int64(len(content)) > maxSize {
		content = content[:maxSize]
		partial = true
		defects = append(defects, Defect{
			Code:       DefectSizeCapExceeded,
			Part:       idx,
			Detail:     "part content truncated at the per-part cap",
			CorpusCase: "convention C4",
		})
	}

	return content, partial, defects
}

// isEncodingError reports whether err came from transfer-decoding rather than
// from the underlying stream, so the defect can be classified correctly.
func isEncodingError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, marker := range []string{
		"base64", "illegal", "quotedprintable", "quoted-printable",
		"unexpected EOF", "invalid", "unhandled encoding", "corrupt",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// itoa is strconv.Itoa without the import, kept local because it appears only in
// defect detail strings.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// canonHeadersFromFields builds the canonical header set from a go-message
// header iterator.
func canonHeadersFromFields(fields message.HeaderFields, out *ParsedMessage) CanonHeaders {
	h := CanonHeaders{All: map[string][]string{}}

	for fields.Next() {
		rawKey := fields.Key()
		key, ok := repairHeaderName(rawKey)
		if !ok {
			// Try the canonicalizer anyway: a name with a NUL is repairable by
			// stripping (corpus le-007), while a nameless line is not.
			if clean, stripped := sanitizeText(rawKey); stripped {
				if key, ok = repairHeaderName(clean); !ok {
					continue
				}
				out.addDefect(Defect{
					Code:       DefectNULStripped,
					Part:       -1,
					Detail:     "NUL removed from header name " + rawKey,
					CorpusCase: "le-007",
				})
			} else {
				out.addDefect(Defect{
					Code:       DefectMalformedHeader,
					Part:       -1,
					Detail:     "unusable header name, line discarded",
					CorpusCase: "hdr-010",
				})
				continue
			}
		}
		canonKey := canonicalHeaderKey(key)
		if canonKey == "" {
			continue
		}

		decoded, defects := decodeHeaderValue(fields.Value(), -1)
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
		h.All[canonKey] = append(h.All[canonKey], decoded)
	}

	h.populate(out)
	return h
}

// populate fills the typed fields from the All map.
func (h *CanonHeaders) populate(out *ParsedMessage) {
	h.Subject = joinEncodedWordFolds(h.Get("Subject"))
	// The fold-join can expose an encoded-word pair that only decodes once
	// glued (corpus ew-001, ew-002: a multi-byte character split across two
	// words). Re-run the decoder over the joined string.
	if encodedWordRe.MatchString(h.Subject) {
		decoded, defects := decodeHeaderValue(h.Subject, -1)
		h.Subject = decoded
		for _, d := range defects {
			out.addDefect(d)
			if d.Code == DefectRFC2047Retried {
				h.RFC2047Retried = true
			}
		}
	}

	h.From = parseAddressList(h.Get("From"), out)
	h.Sender = parseAddressList(h.Get("Sender"), out)
	h.ReplyTo = parseAddressList(h.Get("Reply-To"), out)
	h.To = parseAddressList(h.Get("To"), out)
	h.Cc = parseAddressList(h.Get("Cc"), out)
	h.Bcc = parseAddressList(h.Get("Bcc"), out)

	h.Date = h.Get("Date")
	h.MessageID = stripAngleBrackets(h.Get("Message-Id"))
	h.InReplyTo = splitMessageIDs(h.Get("In-Reply-To"))

	// References can legitimately repeat across several header lines in mangled
	// mail; concatenating keeps the whole chain.
	h.References = splitMessageIDs(strings.Join(h.Values("References"), " "))
}

// bytesReader is a tiny helper so the salvage layer can share this file's
// header machinery without importing bytes at each call site.
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
