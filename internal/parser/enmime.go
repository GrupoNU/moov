package parser

import (
	"bytes"
	"errors"
	"strings"

	"github.com/jhillyerd/enmime/v2"
)

// The fallback cascade layer: jhillyerd/enmime.
//
// S4 §3 measured why it is here: 9 of 110 corpus cases hard-fail in go-message
// and parse in enmime, in two clusters that are both ordinary rather than exotic.
// go-message delegates header parsing to net/textproto, which rejects the ENTIRE
// header block on a single malformed line — so one bad header costs the whole
// message, and an mbox "From " envelope line leaking into a message (a routine
// artifact of mailbox handling, corpus real-world-009) takes it down. enmime
// also tolerates unterminated multiparts, where go-message returns a bare EOF.
//
// The cascade is bidirectional, which is the part worth remembering: corpus
// cs-013 declares charset twice and enmime rejects the message outright via Go's
// mime.ParseMediaType, while go-message parses it fine. A one-directional
// "enmime is more lenient" story would still lose that message.

// parseEnmimeChecked runs the fallback layer. The middle return value mirrors
// parseGoMessage's shape (whether the tree is known incomplete); enmime builds
// the whole tree eagerly and reports its recovery through env.Errors rather than
// by stopping early, so it is always false today. It is kept in the signature so
// the two layers stay symmetric and the cascade in Parse reads the same way for
// both.
func parseEnmimeChecked(raw []byte, limits Limits) (*ParsedMessage, bool, error) {
	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return nil, false, err
	}
	if env == nil || env.Root == nil {
		return nil, false, errors.New("enmime: no root part")
	}

	out := &ParsedMessage{
		Status:  StatusOK,
		Parser:  ParserEnmime,
		RawSize: len(raw),
	}

	w := &enmimeWalker{limits: limits, out: out}
	if err := w.walk(env.Root, -1, 0, 0); err != nil {
		// Returned with the error so the cascade can name the cap that fired;
		// see the equivalent comment in parseGoMessage.
		return out, false, err
	}
	if len(out.Parts) == 0 {
		return nil, false, errors.New("enmime: no parts extracted")
	}

	out.Headers = canonHeadersFromEnmime(env, out)

	// enmime reports its own defects on the envelope. They are recorded rather
	// than acted on: they describe recovery it already performed.
	for _, e := range env.Errors {
		out.addDefect(Defect{
			Code:   enmimeDefectCode(e),
			Part:   -1,
			Detail: e.Error(),
		})
		if e.Severe {
			out.downgrade(StatusPartial)
		}
	}

	return out, false, nil
}

// enmimeDefectCode maps an enmime error onto our typed vocabulary, so that
// metrics aggregate across the two libraries rather than splitting by which one
// happened to win the cascade.
func enmimeDefectCode(e *enmime.Error) DefectCode {
	name := strings.ToLower(e.Name)
	switch {
	case strings.Contains(name, "charset"):
		return DefectCharsetUnknown
	case strings.Contains(name, "encoding"):
		return DefectUnknownEncoding
	case strings.Contains(name, "header"):
		return DefectMalformedHeader
	case strings.Contains(name, "boundary"):
		return DefectBodyReadError
	default:
		return DefectMalformedHeader
	}
}

// enmimeWalker mirrors treeWalker over enmime's already-built tree.
type enmimeWalker struct {
	limits Limits
	out    *ParsedMessage
}

func (w *enmimeWalker) walk(p *enmime.Part, parent, depth, rfc822Depth int) error {
	if p == nil {
		return nil
	}
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

	part := w.newPart(p, parent, depth)
	idx := part.Index
	w.out.Parts = append(w.out.Parts, part)

	content := p.Content
	if int64(len(content)) > w.limits.MaxPartSize {
		content = content[:w.limits.MaxPartSize]
		w.out.Parts[idx].PartiallyDecoded = true
		w.out.addDefect(Defect{
			Code:       DefectSizeCapExceeded,
			Part:       idx,
			Detail:     "part content truncated at the per-part cap",
			CorpusCase: "convention C4",
		})
		w.out.downgrade(StatusPartial)
	}

	if w.out.Parts[idx].IsRFC822 && len(content) > 0 {
		if err := w.descendRFC822(idx, content, depth, rfc822Depth); err != nil {
			return err
		}
	} else if p.FirstChild == nil {
		w.finishLeaf(idx, content)
	} else {
		w.out.Parts[idx].IsMultipart = true
	}

	for child := p.FirstChild; child != nil; child = child.NextSibling {
		if err := w.walk(child, idx, depth+1, rfc822Depth); err != nil {
			return err
		}
	}
	return nil
}

// descendRFC822 re-parses an embedded message, exactly as the primary layer
// does, and for the same reason (S4 §4.4): neither library descends on its own,
// and forwarded text has to reach the index.
func (w *enmimeWalker) descendRFC822(idx int, content []byte, depth, rfc822Depth int) error {
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

	inner, err := enmime.ReadEnvelope(bytes.NewReader(content))
	if err != nil || inner == nil || inner.Root == nil {
		// Deliberately NOT propagated. A message/rfc822 part whose payload is not
		// actually a message (corpus structural-016) is a defect of that part, not
		// a failure of the enclosing message: the wrapper keeps its bytes and the
		// rest of the tree parses normally. Returning the error here would abort
		// the whole walk and lose a message over one bad attachment — the exact
		// outcome the cascade exists to prevent.
		detail := "message/rfc822 part is not a parseable message"
		if err != nil {
			detail += ": " + err.Error()
		}
		w.out.addDefect(Defect{
			Code:       DefectMalformedHeader,
			Part:       idx,
			Detail:     detail,
			CorpusCase: "structural-016",
		})
		w.out.downgrade(StatusPartial)
		return nil //nolint:nilerr // see above: a bad embedded message is a defect, not a failure
	}
	return w.walk(inner.Root, idx, depth+1, rfc822Depth+1)
}

func (w *enmimeWalker) finishLeaf(idx int, content []byte) {
	p := &w.out.Parts[idx]

	if p.IsText() {
		// enmime has already transcoded text to UTF-8 using its own charset
		// table. Re-running the cascade over valid UTF-8 would be a no-op, so we
		// only intervene when the result is NOT valid UTF-8 — which is the
		// signal that its conversion did not happen or did not work.
		if utf16, ok := utf16BOMDecode(content); ok {
			content = utf16
			p.Charset = "utf-16"
			p.CharsetGuessed = true
		} else if !isValidUTF8(content) {
			res := decodeCharset(content, p.Charset, idx)
			content = res.Text
			p.Charset = res.Charset
			p.CharsetGuessed = res.Guessed
			for _, d := range res.Defects {
				w.out.addDefect(d)
			}
			w.out.downgrade(StatusPartial)
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
}

func (w *enmimeWalker) newPart(p *enmime.Part, parent, depth int) Part {
	out := Part{
		Index:       len(w.out.Parts),
		Parent:      parent,
		Depth:       depth,
		MediaType:   strings.ToLower(strings.TrimSpace(p.ContentType)),
		Params:      map[string]string{},
		Charset:     strings.ToLower(strings.TrimSpace(p.Charset)),
		Disposition: strings.ToLower(strings.TrimSpace(p.Disposition)),
		Filename:    p.FileName,
		ContentID:   stripAngleBrackets(p.ContentID),
		Headers:     map[string][]string{},
	}
	if out.MediaType == "" {
		out.MediaType = "text/plain"
	}
	out.IsRFC822 = out.MediaType == "message/rfc822"

	if p.Header != nil {
		for key, values := range p.Header {
			canonKey := canonicalHeaderKey(key)
			if canonKey == "" {
				continue
			}
			for _, v := range values {
				decoded, _ := decodeHeaderValue(v, out.Index)
				out.Headers[canonKey] = append(out.Headers[canonKey], decoded)
			}
		}
		if ct := p.Header.Get("Content-Type"); ct != "" {
			if _, params, err := parseMediaType(ct); err == nil {
				out.Params = params
			} else if cs := salvageCharsetParam(ct); cs != "" {
				out.Params["charset"] = cs
			}
		}
		out.Encoding = strings.ToLower(strings.TrimSpace(
			p.Header.Get("Content-Transfer-Encoding")))
	}
	if out.Charset != "" {
		out.Params["charset"] = out.Charset
	} else {
		out.Charset = out.Params["charset"]
	}

	out.IsAttachment = isAttachmentPart(out)
	return out
}

// canonHeadersFromEnmime builds canonical headers from an enmime envelope.
func canonHeadersFromEnmime(env *enmime.Envelope, out *ParsedMessage) CanonHeaders {
	h := CanonHeaders{All: map[string][]string{}}

	if env.Root != nil && env.Root.Header != nil {
		for key, values := range env.Root.Header {
			rawKey, ok := repairHeaderName(key)
			if !ok {
				if clean, stripped := sanitizeText(key); stripped {
					if rawKey, ok = repairHeaderName(clean); !ok {
						continue
					}
					out.addDefect(Defect{
						Code:       DefectNULStripped,
						Part:       -1,
						Detail:     "NUL removed from header name",
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
			canonKey := canonicalHeaderKey(rawKey)
			if canonKey == "" {
				continue
			}
			for _, v := range values {
				decoded, defects := decodeHeaderValue(v, -1)
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
		}
	}

	h.populate(out)
	return h
}

// isValidUTF8 reports whether b is well-formed UTF-8.
func isValidUTF8(b []byte) bool {
	return utf8Valid(b)
}
