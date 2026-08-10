package parser

import (
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// Parse turns raw RFC 5322 bytes into Moov's canonical message form.
//
// It is the contract of L2 §4.2 and the entry point of the cascade settled by
// spike S4: go-message, then enmime, then salvage, then failure. It NEVER
// returns an error and never panics on any input, because the engine's operating
// rule is that a message which fails to parse must not break a folder's sync —
// so every failure mode has to be expressible in the returned value.
//
// The raw blob is persisted by the sync layer BEFORE this function is called.
// Parsing is a retryable derivation of bytes that are already safe on disk: a
// parser version bump re-derives, it never re-downloads. This package therefore
// owns no storage and reads nothing but its argument.
//
// A zero Limits is valid and means DefaultLimits.
func Parse(raw io.Reader, limits Limits) ParsedMessage {
	limits = limits.withDefaults()

	data, readDefects, tooLarge := readCapped(raw, limits.MaxTotalSize)

	if tooLarge {
		// Refusing a message larger than the cap is a bounded refusal, which
		// convention C4 settles as correct behavior. Note the bytes were never
		// fully read into memory: the cap is enforced on the reader.
		return failedMessage(ParserNone, len(data), append(readDefects, Defect{
			Code:       DefectSizeCapExceeded,
			Part:       -1,
			Detail:     "raw message exceeds the configured total size cap",
			CorpusCase: "convention C4 (S4 §5)",
		}))
	}

	if len(data) == 0 {
		// Corpus structural-002, the one genuinely hopeless case in the corpus.
		// The requirement is to fail CLEANLY rather than return a
		// partially-initialized struct that downstream code mistakes for a real
		// message.
		return failedMessage(ParserNone, 0, append(readDefects, Defect{
			Code:       DefectEmptyInput,
			Part:       -1,
			Detail:     "empty input: nothing to parse",
			CorpusCase: "structural-002",
		}))
	}

	// Line-ending pre-normalization, before anything else sees the bytes (S4 H9).
	// Narrowly scoped: CR present and LF absent, so a bare CR inside an ordinary
	// body (corpus le-010) is untouched.
	var preDefects []Defect
	preDefects = append(preDefects, readDefects...)
	if normalized, changed := normalizeLineEndings(data); changed {
		data = normalized
		preDefects = append(preDefects, Defect{
			Code:       DefectLineEndingNormalized,
			Part:       -1,
			Detail:     "bare CR line endings normalized to CRLF before parsing",
			CorpusCase: "le-002 (S4 H9)",
		})
	}

	// Structural pre-scan, before ANY library sees the bytes.
	//
	// A deep AND unterminated multipart nest makes enmime's cost grow roughly 4x
	// per two levels (see prescan.go for the measurements): 1.4 KB of input can
	// occupy a parse worker for 18 seconds, and a little deeper for hours. The
	// engine's MaxDepth cap cannot stop it, because that cap is enforced by the
	// walkers and the walkers run only after the library has built its tree. The
	// bound has to be applied to the input, and a linear scan is the one thing
	// that cannot itself become the vector.
	//
	// Refusing here is the same bounded refusal convention C4 already blesses.
	if declared := prescanDepth(data); declared > limits.MaxUnterminatedDepth &&
		isUnterminatedNest(data, declared) {
		return failedMessage(ParserNone, len(data), append(preDefects, Defect{
			Code: DefectDepthCapExceeded,
			Part: -1,
			Detail: "unterminated multipart nest declaring " + itoa(declared) +
				" boundaries exceeds the depth cap; refused before parsing to " +
				"avoid superlinear work in the MIME libraries",
			CorpusCase: "found by FuzzParse; see prescan.go",
		}))
	}

	// Layer 1: go-message. Streaming, conservative, and trustworthy when it
	// succeeds.
	primary, truncated, primaryErr := parseGoMessage(data, limits)
	if primaryErr == nil && primary != nil && isContentless(primary) {
		// The parse "succeeded" and produced nothing a user could read: a
		// multipart whose boundary never matched, so it has no children and its
		// body is unreachable. The manifest names this the failure mode for
		// corpus structural-004 — "returning a multipart with zero children and
		// an unreachable body is the failure mode: the user would see a blank
		// message" — so it must not be accepted as a clean parse. Treating it as
		// a refusal sends it down the cascade to salvage, which recovers the body.
		primaryErr = errors.New("go-message: parsed to no readable content")
	}
	if primaryErr == nil && primary != nil && !truncated {
		primary.prepend(preDefects)
		finish(primary, data)
		return *primary
	}

	// go-message returned a tree it already knows is incomplete: a multipart
	// iteration ended on an error rather than on EOF, so siblings were never
	// reached. It does not report this as a failure, which is what makes it
	// dangerous — the message looks parsed and is quietly missing content
	// (corpus le-003, cs-015, structural-015).
	//
	// enmime recovers all three, so it gets a turn and the RICHER tree wins.
	// Comparing rather than blindly preferring the fallback matters, because the
	// cascade is bidirectional (S4 §3): enmime is not a superset of go-message,
	// and on a message where it does worse the primary result must survive.
	if primaryErr == nil && primary != nil && truncated {
		primary.prepend(preDefects)
		finish(primary, data)

		if fallback, _, err := parseEnmimeChecked(data, limits); err == nil && fallback != nil {
			fallback.prepend(preDefects)
			fallback.addDefect(Defect{
				Code:       DefectPrimaryParserFailed,
				Part:       -1,
				Detail:     "go-message returned a truncated multipart; enmime recovered a fuller tree",
				CorpusCase: "le-003/cs-015/structural-015 (S4 §3)",
			})
			fallback.downgrade(StatusPartial)
			finish(fallback, data)
			if len(fallback.LeafParts()) > len(primary.LeafParts()) {
				return *fallback
			}
		}
		return *primary
	}
	if errors.Is(primaryErr, errCapExceeded) {
		// A cap fired. Trying the other library on the same oversized message
		// would just spend the resources the cap exists to protect.
		return failedMessage(ParserGoMessage, len(data),
			append(preDefects, capDefects(primary)...))
	}

	cascadeDefects := append(preDefects, Defect{
		Code:       DefectPrimaryParserFailed,
		Part:       -1,
		Detail:     "go-message: " + errText(primaryErr),
		CorpusCase: "S4 §3",
	})

	// Layer 2: enmime. Rescues the 9 header-strictness and boundary cases that
	// go-message rejects.
	fallback, _, fallbackErr := parseEnmimeChecked(data, limits)
	if fallbackErr == nil && fallback != nil {
		fallback.prepend(cascadeDefects)
		// The message needed a second parser to be read at all, so something in
		// it is malformed even though the result is complete.
		fallback.downgrade(StatusPartial)
		finish(fallback, data)
		return *fallback
	}
	if errors.Is(fallbackErr, errCapExceeded) {
		return failedMessage(ParserEnmime, len(data),
			append(cascadeDefects, capDefects(fallback)...))
	}

	cascadeDefects = append(cascadeDefects, Defect{
		Code:       DefectFallbackParserFailed,
		Part:       -1,
		Detail:     "enmime: " + errText(fallbackErr),
		CorpusCase: "S4 §2",
	})

	// Layer 3: salvage. Both libraries refused, but the body may still be
	// legible — and for two of the three corpus cases that reach here, the
	// manifest requires that it be recovered rather than shown as blank.
	if salvaged, ok := parseSalvage(data, limits, cascadeDefects); ok {
		finish(salvaged, data)
		return *salvaged
	}

	// Layer 4: nothing was recoverable. The raw blob (already persisted) is all
	// that survives, which is exactly what parse_status='failed' means.
	return failedMessage(ParserNone, len(data), cascadeDefects)
}

// ParseBytes is Parse for callers that already hold the message in memory, which
// the sync engine does after fetching a blob. It avoids a pointless copy.
func ParseBytes(raw []byte, limits Limits) ParsedMessage {
	return Parse(bytesReader(raw), limits)
}

// readCapped reads at most maxSize bytes, reporting whether the input exceeded
// the cap.
//
// The reader is limited to maxSize+1 so that "exactly at the cap" and "over the
// cap" are distinguishable without ever allocating the oversized remainder. A
// hostile 10 GB message costs maxSize+1 bytes of memory, not 10 GB.
func readCapped(r io.Reader, maxSize int64) ([]byte, []Defect, bool) {
	if r == nil {
		return nil, nil, false
	}
	data, err := io.ReadAll(io.LimitReader(r, maxSize+1))

	var defects []Defect
	if err != nil {
		// Partial reads are real data and are kept (S4 §4.2). A truncated
		// download still yields a readable message far more often than not.
		defects = append(defects, Defect{
			Code:       DefectBodyReadError,
			Part:       -1,
			Detail:     "reading raw message: " + err.Error(),
			CorpusCase: "S4 §4.2",
		})
	}
	if int64(len(data)) > maxSize {
		return data[:maxSize], defects, true
	}
	return data, defects, false
}

// failedMessage builds the StatusFailed result.
//
// Headers are deliberately left EMPTY. S4 §2 observed enmime concatenating an
// orphan header line into the following From header while failing on corpus
// hdr-009 — it damaged the header block on its way out. Emitting partial headers
// from a hard-failed parse would propagate exactly that kind of corruption into
// the store, so this package refuses to do it. The raw blob remains the system of
// record and a reparse can always be attempted later.
func failedMessage(parser ParserName, rawSize int, defects []Defect) ParsedMessage {
	return ParsedMessage{
		Status:  StatusFailed,
		Parser:  parser,
		Headers: CanonHeaders{All: map[string][]string{}},
		Defects: defects,
		RawSize: rawSize,
	}
}

// capDefects extracts the cap defects recorded on a partially-built message, so
// that a failure caused by a cap says WHICH cap fired.
func capDefects(m *ParsedMessage) []Defect {
	if m == nil {
		return nil
	}
	var out []Defect
	for _, d := range m.Defects {
		switch d.Code {
		case DefectDepthCapExceeded, DefectPartCapExceeded, DefectSizeCapExceeded:
			out = append(out, d)
		default:
		}
	}
	return out
}

// errText renders an error for a defect detail, tolerating nil.
func errText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

// finish assembles the derived fields once the tree is built.
func finish(m *ParsedMessage, raw []byte) {
	m.truncatedHeaders = headersEndMidLine(raw)

	resolveInlineReferences(m)
	assembleFTS(m)
}

// isContentless reports whether a parse produced a tree with no readable
// content at all — every part a container, or every leaf empty.
//
// This is distinct from a message that legitimately has an empty body (corpus
// structural-001, structural-003): those have a LEAF part, and an empty leaf is
// a real answer. What this catches is a container with nothing under it, which
// means the structure was declared but never materialized and the body bytes are
// stranded where no consumer will look for them.
func isContentless(m *ParsedMessage) bool {
	if len(m.Parts) == 0 {
		return true
	}
	for _, p := range m.Parts {
		if p.IsMultipart {
			continue
		}
		// Any leaf at all, empty or not, counts as content the tree can hold.
		return false
	}
	return true
}

// headersEndMidLine reports whether the input stops inside a header line, with
// no body and no line terminator — the shape of a message cut off in transit.
//
// The distinction this draws is exact, and the corpus contains both sides of it:
//
//   - structural-001 ends with a COMPLETE, CRLF-terminated header line and no
//     blank line. Its notes are explicit that "end-of-input is a valid terminator
//     for a header block" and that the resulting empty body "is correct and is
//     NOT data loss" — so it has one leaf part.
//   - hdr-007 ends mid-token, in the middle of "Content-Type: text/pla", with no
//     terminator at all. The real message continues past that byte, so the
//     message has no body rather than an empty one — zero leaf parts.
//
// The signal separating them is therefore whether the final line was terminated,
// not whether a blank line was ever found.
func headersEndMidLine(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	if raw[len(raw)-1] == '\n' || raw[len(raw)-1] == '\r' {
		return false
	}
	// The last line is unterminated. It is a truncated HEADER only if no
	// header/body separator preceded it — otherwise this is an ordinary body
	// without a trailing newline (corpus le-009).
	_, body := splitHeaderBody(raw)
	return body == nil && looksLikeHeaderBlock(raw)
}

// resolveInlineReferences demotes parts that a sibling body actually renders.
//
// Corpus structural-007 is the case: an image/png labeled
// Content-Disposition: attachment, but carrying a Content-ID that a sibling
// text/html references as cid:logo@example.com. The manifest's policy is that
// the cid: reference WINS — the image is rendered inline and must not also be
// listed as a downloadable attachment, or the user sees a phantom attachment for
// every logo in every newsletter.
//
// The manifest calls this a presentation-layer rule and accepts a parser that
// reports it by literal disposition (convention C2). It is implemented here
// anyway because the evidence needed to decide it — the Content-IDs and the
// sibling bodies that reference them — is entirely contained in the message, so
// the parse layer can answer correctly rather than deferring an answer it
// already has. Nothing is lost by doing so: the part, its bytes and its literal
// Disposition all remain on the Part for a consumer that wants to disagree.
func resolveInlineReferences(m *ParsedMessage) {
	// Collect the Content-IDs referenced by cid: URLs in any text body.
	var referenced map[string]bool
	for i := range m.Parts {
		p := &m.Parts[i]
		if !p.IsText() || len(p.Content) == 0 {
			continue
		}
		for _, id := range findCIDReferences(string(p.Content)) {
			if referenced == nil {
				referenced = make(map[string]bool)
			}
			referenced[id] = true
		}
	}
	if referenced == nil {
		return
	}

	for i := range m.Parts {
		p := &m.Parts[i]
		if p.IsAttachment && p.ContentID != "" && referenced[strings.ToLower(p.ContentID)] {
			p.IsAttachment = false
		}
	}
}

// findCIDReferences extracts the identifiers of cid: URLs in a body.
//
// Matching uses ASCII-only case folding on the ORIGINAL bytes. Searching a
// strings.ToLower copy and indexing the original with the result is a slice
// bounds panic waiting to happen, because Unicode lowercasing can change a
// string's byte length (U+212A KELVIN SIGN is three bytes and lowercases to a
// one-byte "k"). Fuzzing found that exact bug elsewhere in this package.
func findCIDReferences(body string) []string {
	const marker = "cid:"
	var out []string
	b := []byte(body)
	for i := 0; i < len(b); {
		j := indexFoldASCII(b[i:], []byte(marker))
		if j < 0 {
			return out
		}
		start := i + j + len(marker)
		end := start
		for end < len(b) && !isCIDTerminator(b[end]) {
			end++
		}
		if end > start {
			out = append(out, strings.ToLower(string(b[start:end])))
		}
		if end == start {
			end = start + 1 // always make progress
		}
		i = end
	}
	return out
}

// isCIDTerminator reports whether c ends a cid: URL. A Content-ID is an
// addr-spec, so the terminators are the characters that cannot appear in one.
func isCIDTerminator(c byte) bool {
	switch c {
	case '"', '\'', '>', '<', ' ', '\t', '\r', '\n', ')', ']', '}':
		return true
	}
	return false
}

// assembleFTS fills the three weighted text fields.
//
// They are separate rather than one blob because internal/store applies distinct
// PostgreSQL tsvector weights to subject, addresses and body (L2 §2.3). Handing
// the store a single pre-joined string would force it to re-split text this
// package had already separated, guessing at boundaries it cannot recover. See
// the ParsedMessage doc comment for the contract note.
func assembleFTS(m *ParsedMessage) {
	m.SubjectText = toStorableText(strings.TrimSpace(m.Headers.Subject))
	m.AddressText = toStorableText(assembleAddressText(m.Headers))
	m.BodyText = toStorableText(assembleBodyText(m.Parts))
}

// toStorableText enforces the guarantee the store depends on: everything this
// package emits for indexing is valid UTF-8 and free of NUL.
//
// Both properties are hard requirements of a PostgreSQL text column, not
// stylistic preferences — a NUL is rejected outright and invalid UTF-8 fails the
// server's encoding check, either of which would abort the transaction that
// stores the message. The per-part and per-header paths already handle their own
// bytes; this is the final net, so that no future change to the assembly can
// reintroduce the problem silently.
func toStorableText(s string) string {
	if clean, stripped := sanitizeText(s); stripped {
		s = clean
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return s
}

// assembleAddressText flattens the address headers a user would search by.
//
// Bcc is deliberately excluded: it is not part of the message as the recipient
// received it, and indexing it would leak the blind-copy list into search
// results for anyone the message is later shared with.
func assembleAddressText(h CanonHeaders) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, list := range [][]Address{h.From, h.Sender, h.ReplyTo, h.To, h.Cc} {
		for _, a := range list {
			for _, token := range []string{a.Name, a.Address} {
				token = strings.TrimSpace(token)
				if token == "" || seen[token] {
					continue
				}
				seen[token] = true
				if b.Len() > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(token)
			}
		}
	}
	return b.String()
}

// assembleBodyText concatenates the text of every indexable part.
//
// Two decisions worth stating. First, text found inside a descended
// message/rfc822 IS included: users expect to find text in forwarded mail, and
// S4 §4.4 identified this as a product requirement the corpus uncovered.
//
// Second, for a multipart/alternative the text/plain sibling is preferred over
// the text/html one, so the index holds one clean copy of the content rather
// than the same words twice with markup around them. When only HTML exists, its
// tags are stripped. Corpus structural-014 puts the alternatives in inverted
// order specifically to catch a parser that assumes plain text comes first.
func assembleBodyText(parts []Part) string {
	if len(parts) == 0 {
		return ""
	}

	skip := make([]bool, len(parts))
	for i := range parts {
		if parts[i].MediaType != "multipart/alternative" {
			continue
		}
		// Among the direct children of this alternative, prefer plain text.
		var hasPlain bool
		for j := range parts {
			if parts[j].Parent == i && parts[j].MediaType == "text/plain" {
				hasPlain = true
				break
			}
		}
		if !hasPlain {
			continue
		}
		for j := range parts {
			if parts[j].Parent == i && parts[j].MediaType == "text/html" {
				skip[j] = true
			}
		}
	}

	var b strings.Builder
	for i := range parts {
		p := &parts[i]
		if skip[i] || p.IsMultipart || len(p.Content) == 0 || !p.IsText() {
			continue
		}
		// An attachment's text is content the user can search for; a text/plain
		// attachment is still text. Only binary attachments are excluded, which
		// IsText already handles.
		text := string(p.Content)
		if p.MediaType == "text/html" {
			text = stripHTMLTags(text)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	return b.String()
}

// stripHTMLTags removes markup so HTML bodies index as words rather than as
// angle brackets and attribute names.
//
// This is a text-extraction routine for the search index and it is NOT
// sanitization. HTML sanitization for DISPLAY is explicitly out of scope for
// this phase (L2 §1) and belongs to the layer that renders the message: see
// SanitizeHook in hook.go for where that plugs in.
// Case-insensitive tag matching is done with ASCII folding over the ORIGINAL
// bytes, for the same reason as findCIDReferences: a strings.ToLower copy can
// differ in byte length from its input, so positions are not interchangeable
// between the two.
func stripHTMLTags(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	src := []byte(s)
	inTag := false
	var skipUntil []byte

	for i := 0; i < len(src); i++ {
		if skipUntil != nil {
			if hasPrefixFoldASCII(src[i:], skipUntil) {
				i += len(skipUntil) - 1
				skipUntil = nil
				inTag = false
			}
			continue
		}
		switch src[i] {
		case '<':
			// script and style hold code, not prose; indexing them is noise.
			switch {
			case hasPrefixFoldASCII(src[i:], []byte("<script")):
				skipUntil = []byte("</script>")
				continue
			case hasPrefixFoldASCII(src[i:], []byte("<style")):
				skipUntil = []byte("</style>")
				continue
			}
			inTag = true
			// A tag boundary is a word boundary: "a<br>b" must not index as "ab".
			b.WriteByte(' ')
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteByte(src[i])
			}
		}
	}

	return strings.Join(strings.Fields(unescapeHTMLEntities(b.String())), " ")
}

// unescapeHTMLEntities expands the handful of entities that matter for indexing.
// A full entity table would be a dependency (golang.org/x/net/html) for a
// marginal gain in search recall, which is not a trade this package needs to make.
func unescapeHTMLEntities(s string) string {
	if !strings.ContainsRune(s, '&') {
		return s
	}
	return strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&apos;", "'",
		"&nbsp;", " ",
		"&#39;", "'",
		"&#34;", `"`,
	).Replace(s)
}

// addDefect appends a defect to the message.
func (m *ParsedMessage) addDefect(d Defect) {
	m.Defects = append(m.Defects, d)
}

// prepend puts earlier-stage defects in front, so Defects reads in the order
// things actually happened.
func (m *ParsedMessage) prepend(defects []Defect) {
	if len(defects) == 0 {
		return
	}
	m.Defects = append(append([]Defect{}, defects...), m.Defects...)
}

// downgrade worsens the status, never improves it.
func (m *ParsedMessage) downgrade(s ParseStatus) {
	if s.worseThan(m.Status) {
		m.Status = s
	}
}
