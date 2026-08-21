package parser

import (
	"fmt"
	"strings"
)

// ParseStatus is the outcome of a parse, and it is the value the sync engine
// stores in messages.parse_status (L2 §2.3).
//
// The three values are deliberately coarse. The engine's operating rule is that
// a message which fails to parse must never break a folder's sync, so the
// question this type answers is only "how much of this message can be trusted",
// not "what exactly went wrong" — that is what Defects are for.
type ParseStatus string

const (
	// StatusOK means everything meaningful was extracted with no data loss and
	// no guessing. Note that this includes plenty of MALFORMED input: recovering
	// content from broken mail is correct behavior, not leniency (corpus
	// manifest, `expect` definitions).
	StatusOK ParseStatus = "ok"

	// StatusPartial means usable content was recovered, but something was
	// necessarily lost, guessed, or resolved from a genuine ambiguity: a
	// truncated message, an undetectable charset lie, a body that decoded only
	// up to the point of an error, a structure with two defensible readings.
	StatusPartial ParseStatus = "partial"

	// StatusFailed means no meaningful structure could be extracted. The engine
	// keeps the raw blob (which it persisted before calling this package) and
	// moves on. The rate of this status is a metric with an alert (risk R4).
	StatusFailed ParseStatus = "failed"
)

// String implements fmt.Stringer.
func (s ParseStatus) String() string { return string(s) }

// worseThan reports whether s is a strictly worse outcome than other, using the
// ordering ok < partial < failed. Status is only ever downgraded during a parse,
// never upgraded, so this is the single place that ordering is expressed.
func (s ParseStatus) worseThan(other ParseStatus) bool {
	return s.rank() > other.rank()
}

func (s ParseStatus) rank() int {
	switch s {
	case StatusOK:
		return 0
	case StatusPartial:
		return 1
	case StatusFailed:
		return 2
	default:
		return 0
	}
}

// ParserName identifies which layer of the cascade produced a ParsedMessage.
// Recording it is what makes the cascade auditable in production: a shift in the
// distribution of these values across a mailbox is the early warning that a
// library upgrade changed behavior.
type ParserName string

const (
	// ParserGoMessage is the primary, streaming layer (emersion/go-message).
	ParserGoMessage ParserName = "go-message"

	// ParserEnmime is the recovery layer (jhillyerd/enmime), which rescues
	// messages go-message rejects — and, in one corpus case, is rescued BY
	// go-message in turn (S4 §3: the cascade is bidirectional).
	ParserEnmime ParserName = "enmime"

	// ParserSalvage is the floor: structure was unrecoverable, but legible text
	// was extracted from the raw bytes as a single part (S4 H3).
	ParserSalvage ParserName = "salvage"

	// ParserNone means nothing was recovered at all.
	ParserNone ParserName = "none"
)

// String implements fmt.Stringer.
func (p ParserName) String() string { return string(p) }

// DefectCode is a typed classification of something wrong with a message.
//
// Typed rather than free-text because these are counted and alerted on: a defect
// code is a metric label. The human-readable specifics go in Defect.Detail.
type DefectCode string

const (
	// DefectPrimaryParserFailed records that go-message rejected the message and
	// the cascade moved on. Not user-visible harm by itself — it is the evidence
	// that the fallback layer is earning its keep.
	DefectPrimaryParserFailed DefectCode = "primary_parser_failed"

	// DefectFallbackParserFailed records that enmime also rejected the message.
	DefectFallbackParserFailed DefectCode = "fallback_parser_failed"

	// DefectSalvaged records that structure was unrecoverable and the body was
	// recovered as a single flat text part (S4 H3).
	DefectSalvaged DefectCode = "salvaged"

	// DefectPartialDecode records that a part's body decoder failed partway and
	// the bytes decoded up to that point were KEPT. Discarding them is the
	// io.ReadAll trap that S4 §4.2 calls the highest-value lesson of the spike.
	DefectPartialDecode DefectCode = "partial_decode"

	// DefectCharsetUnknown records a charset label that no decoder recognized,
	// so the cascade fell through to detection or to the windows-1252 floor.
	DefectCharsetUnknown DefectCode = "charset_unknown"

	// DefectCharsetGuessed records that the part's charset was not taken from the
	// message's own declaration but detected or defaulted. "No parse error" is
	// never a guarantee of correct text (S4 §4.3).
	DefectCharsetGuessed DefectCode = "charset_guessed"

	// DefectRFC2047Residual records that a decoded header still contained an
	// encoded-word pattern after the standard decoder ran.
	DefectRFC2047Residual DefectCode = "rfc2047_residual"

	// DefectRFC2047Retried records that the residual encoded-word was decoded by
	// the raw-base64 retry pass — the mitigation for S4 §4.1, where both
	// libraries leak raw MIME markup into subject lines with no error at all.
	DefectRFC2047Retried DefectCode = "rfc2047_retried"

	// DefectDepthCapExceeded records that the MIME tree was deeper than
	// Limits.MaxDepth. A bounded refusal is correct behavior (corpus convention
	// C4), not a parser weakness.
	DefectDepthCapExceeded DefectCode = "depth_cap_exceeded"

	// DefectPartCapExceeded records that the message held more parts than
	// Limits.MaxParts.
	DefectPartCapExceeded DefectCode = "part_cap_exceeded"

	// DefectSizeCapExceeded records that the raw message exceeded
	// Limits.MaxTotalSize.
	DefectSizeCapExceeded DefectCode = "size_cap_exceeded"

	// DefectRFC822DepthCapped records that a message/rfc822 part was left opaque
	// because descending further would have exceeded the rfc822 descent budget.
	// Its bytes are retained; only its interior is unindexed.
	DefectRFC822DepthCapped DefectCode = "rfc822_depth_capped"

	// DefectLineEndingNormalized records that the raw input used bare CR line
	// endings and was normalized to CRLF before the cascade ran (S4 H9). Narrowly
	// scoped by construction: it fires only when the buffer has CR and no LF at
	// all, so it can never corrupt a bare-CR-in-body message.
	DefectLineEndingNormalized DefectCode = "line_ending_normalized"

	// DefectNULStripped records that NUL bytes were removed from a header value
	// or body text. PostgreSQL cannot store NUL in a text column, so this is a
	// mandatory sanitization, not a preference (corpus case le-007).
	DefectNULStripped DefectCode = "nul_stripped"

	// DefectEmptyInput records that there was nothing to parse at all.
	DefectEmptyInput DefectCode = "empty_input"

	// DefectMalformedHeader records a header line that could not be parsed as
	// one, and what was done about it.
	DefectMalformedHeader DefectCode = "malformed_header"

	// DefectUnknownEncoding records a Content-Transfer-Encoding no decoder
	// implements, so the body was taken as-is rather than dropped.
	DefectUnknownEncoding DefectCode = "unknown_encoding"

	// DefectBodyReadError records a body read that failed for a reason other than
	// a decoding fault — a truncated stream, most often.
	DefectBodyReadError DefectCode = "body_read_error"
)

// Defect is one observation about a message, traceable to its cause.
type Defect struct {
	// Code classifies the defect. This is the field to aggregate on.
	Code DefectCode

	// Part is the index into ParsedMessage.Parts this defect belongs to, or -1
	// when it concerns the message as a whole.
	Part int

	// Detail is human-readable specifics: the offending charset name, the
	// decoder's error string, the cap that was hit. Never parsed by anything.
	Detail string

	// CorpusCase names the corpus case that motivated this defect path, where one
	// exists (for example "ew-004" or "S4 H5"). It is documentation carried in
	// the code, so that a defect appearing in production leads a maintainer
	// straight to the test that describes it.
	CorpusCase string
}

// String implements fmt.Stringer, for test failure messages and logs.
func (d Defect) String() string {
	var b strings.Builder
	b.WriteString(string(d.Code))
	if d.Part >= 0 {
		fmt.Fprintf(&b, "[part %d]", d.Part)
	}
	if d.Detail != "" {
		b.WriteString(": ")
		b.WriteString(d.Detail)
	}
	if d.CorpusCase != "" {
		fmt.Fprintf(&b, " (%s)", d.CorpusCase)
	}
	return b.String()
}

// Address is one parsed mail address. Both fields are already RFC 2047 decoded.
type Address struct {
	// Name is the display name, empty when the address had none.
	Name string
	// Address is the addr-spec ("user@example.com"). It may be empty if the
	// header was malformed enough that only a display name survived.
	Address string
}

// String renders the address in a form suitable for the full-text index.
func (a Address) String() string {
	switch {
	case a.Name != "" && a.Address != "":
		return a.Name + " <" + a.Address + ">"
	case a.Address != "":
		return a.Address
	default:
		return a.Name
	}
}

// CanonHeaders is the canonical, decoded header set of a message.
//
// "Canonical" means two things: header names are in textproto canonical form
// ("Message-Id" -> "Message-ID" is NOT applied; Go's canonical form is used
// consistently), and values have been RFC 2047 decoded, NUL-stripped, and
// unfolded. Raw header bytes are not preserved here — the raw blob is the
// system of record for those, and it always exists.
type CanonHeaders struct {
	// Subject, decoded. Empty when the message had none.
	Subject string

	// From, Sender, ReplyTo, To, Cc, Bcc hold parsed addresses. A message with a
	// malformed address header yields what could be recovered plus a defect,
	// never an error.
	From    []Address
	Sender  []Address
	ReplyTo []Address
	To      []Address
	Cc      []Address
	Bcc     []Address

	// Date is the Date header exactly as it appeared, after unfolding. It is
	// deliberately NOT parsed into a time.Time here: date interpretation (and the
	// fallback to the IMAP INTERNALDATE when the header is absent or nonsense) is
	// the sync layer's decision, and it needs the raw string to make it.
	Date string

	// MessageID, InReplyTo and References carry threading identity, with the
	// angle brackets stripped. Threading itself is not this package's job.
	MessageID  string
	InReplyTo  []string
	References []string

	// All holds every header, canonicalized and decoded, including the ones
	// promoted to typed fields above. Multi-valued headers (Received, and any
	// header a message repeats) keep every occurrence in order.
	All map[string][]string

	// Ordered holds the same headers as All, but as a flat list in the order
	// they appeared in the message, which a map cannot express. RFC 8621
	// §4.1.3 defines the JMAP `headers` property as "a list of all header
	// fields ... in the same order they appear in the message", so a consumer
	// that must reproduce the header block reads this rather than All.
	Ordered []Header

	// CharsetGuessed is true when any header value's charset was detected or
	// defaulted rather than taken from an honest declaration (S4 H6).
	CharsetGuessed bool

	// RFC2047Retried is true when at least one header needed the raw-base64
	// retry pass to decode (S4 H4 / corpus ew-004).
	RFC2047Retried bool
}

// Header is one header field occurrence, kept in message order.
//
// Name preserves the capitalization the message used (RFC 8621 §4.1.3 asks for
// exactly that), while CanonHeaders.All is keyed by the canonical form.
type Header struct {
	Name  string
	Value string
}

// Get returns the first value of a header, or "" when absent. The name is
// canonicalized, so Get("subject") and Get("Subject") are the same lookup.
func (h CanonHeaders) Get(name string) string {
	vs := h.Values(name)
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}

// Values returns every value of a header in the order they appeared, or nil.
func (h CanonHeaders) Values(name string) []string {
	if h.All == nil {
		return nil
	}
	return h.All[canonicalHeaderKey(name)]
}

// Part is one leaf or container node of the MIME tree, flattened.
//
// The tree is flattened rather than nested because every consumer of this
// package — the store, the FTS assembly, the future JMAP bodyStructure — walks
// it linearly, and Depth plus Parent carry everything a nested form would.
type Part struct {
	// Index is this part's position in ParsedMessage.Parts. Self-referential by
	// design, so a Part passed around alone still identifies itself.
	Index int

	// Parent is the Index of the enclosing part, or -1 for the root.
	Parent int

	// Depth is 0 for the root part and increases with each level of nesting.
	// Descending into a message/rfc822 increases it like any other level.
	Depth int

	// MediaType is the lowercased "type/subtype", defaulting to "text/plain" per
	// RFC 2045 when absent or unparseable.
	MediaType string

	// Params holds the Content-Type parameters, lowercased keys.
	Params map[string]string

	// Charset is the charset actually used to decode this part, which is not
	// necessarily the one declared: see CharsetGuessed.
	Charset string

	// CharsetGuessed is true when Charset was detected by heuristic or defaulted
	// to windows-1252 rather than taken from the part's own declaration.
	CharsetGuessed bool

	// Encoding is the Content-Transfer-Encoding as declared, lowercased.
	Encoding string

	// Disposition is the Content-Disposition type, lowercased ("inline",
	// "attachment"), or "" when absent.
	Disposition string

	// Filename is the decoded filename from Content-Disposition or Content-Type,
	// with RFC 2231 continuations joined. It is NOT sanitized for filesystem
	// safety — that is the storage layer's job, and doing it here would destroy
	// the evidence that a message tried a traversal (corpus structural-013).
	Filename string

	// ContentID is the Content-ID with angle brackets stripped, for cid: URL
	// resolution by the presentation layer.
	ContentID string

	// IsAttachment reports whether this part is user-facing attachment content,
	// decided at the PARSE layer only (corpus convention C2). The presentation
	// rule — that a cid:-referenced inline image is rendered, not listed — is
	// deliberately not applied here.
	IsAttachment bool

	// IsMultipart reports whether this part is a container. Container parts carry
	// no content of their own.
	IsMultipart bool

	// IsRFC822 reports whether this part is an embedded message. When the parser
	// descended into it, its children follow it in Parts.
	IsRFC822 bool

	// Content is the DECODED body of a leaf part: transfer-decoding undone, and
	// for text/* media types transcoded to UTF-8. Nil for container parts.
	//
	// On a decode error this holds everything that decoded before the failure —
	// never nil-because-of-error. That is the S4 §4.2 mitigation.
	Content []byte

	// PartiallyDecoded is true when Content is a prefix of the intended content
	// because decoding failed partway.
	PartiallyDecoded bool

	// Size is len(Content) for leaves, 0 for containers.
	Size int

	// Headers are this part's own MIME headers, decoded.
	Headers map[string][]string
}

// IsText reports whether this part carries text this engine will index.
func (p Part) IsText() bool {
	return strings.HasPrefix(p.MediaType, "text/")
}

// ParsedMessage is the output contract of this package (L2 §4.2).
//
// # Divergence from L2 §4.2, deliberate and flagged for the director
//
// The spec writes a single `TextForFTS string`. This implementation exposes
// SubjectText, AddressText and BodyText as three separate fields instead, plus a
// TextForFTS() method that joins them for any consumer wanting the flat form.
//
// The reason is that the store weights them differently: L2 §2.3 builds tsv with
// PostgreSQL weight classes (subject and addresses rank above body). A single
// pre-joined string would force internal/store to re-split text this package had
// already separated, guessing at boundaries it cannot recover — the weighting
// would be reconstructed from a string rather than carried. Three fields make the
// weighting a contract instead of a heuristic, at no cost to any consumer that
// wants the flat form.
type ParsedMessage struct {
	// Status is the outcome. Consumers must check it before trusting anything
	// else — with one important exception spelled out below.
	Status ParseStatus

	// Parser names the cascade layer that produced this result.
	Parser ParserName

	// Headers are the canonical decoded headers.
	//
	// When Status is StatusFailed, Headers is EMPTY by construction, never
	// partially filled. S4 §2 observed enmime damaging the header block while
	// failing on corpus case hdr-009 — it concatenated an orphan line into the
	// following From header — so partial headers from a hard-failed parse are not
	// trustworthy and this package refuses to emit them.
	Headers CanonHeaders

	// Parts is the flattened MIME tree, root first, in document order.
	Parts []Part

	// SubjectText is the subject as indexed. See the type doc for why this is
	// separate from BodyText.
	SubjectText string

	// AddressText is the flattened From/To/Cc/Reply-To display names and
	// addresses, as indexed.
	AddressText string

	// BodyText is the concatenated text of every text/* part, including parts
	// found inside descended message/rfc822 attachments (S4 H7 — users expect to
	// find text inside forwarded mail).
	BodyText string

	// Defects is everything observed, in the order observed.
	Defects []Defect

	// RawSize is the size in bytes of the raw input, after line-ending
	// normalization if any was applied.
	RawSize int

	// truncatedHeaders records that the input ended inside its header block,
	// with no header/body separator anywhere — the shape a connection drop or a
	// disk-full write during delivery produces (corpus hdr-007). Such a message
	// has no body at all, as distinct from an empty body, and LeafParts uses this
	// to avoid reporting a content part the message does not have.
	//
	// Unexported: it is an internal detail of part counting, not something a
	// consumer should branch on. The user-visible signal is Status == partial
	// plus the malformed_header defect.
	truncatedHeaders bool
}

// TextForFTS returns the flat concatenation of the three weighted text fields,
// for consumers that want the shape L2 §4.2 originally described. The store
// should prefer the individual fields so it can apply its tsv weights.
func (m ParsedMessage) TextForFTS() string {
	parts := make([]string, 0, 3)
	for _, s := range []string{m.SubjectText, m.AddressText, m.BodyText} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

// Attachments returns the parts this message presents as attachments, at the
// parse layer (corpus convention C2).
func (m ParsedMessage) Attachments() []Part {
	var out []Part
	for _, p := range m.Parts {
		if p.IsAttachment {
			out = append(out, p)
		}
	}
	return out
}

// LeafParts returns the parts with no children, which is what the corpus
// manifest's `parts` key counts (convention C1).
//
// C1 is specific: a message/rfc822 counts as ONE leaf, and the parts of the
// message it contains are NOT added to the enclosing total. Since this parser
// descends into embedded messages for indexing (S4 H7), a descended rfc822 part
// has children in Parts — so C1 requires it to be counted as a leaf anyway, and
// its interior excluded. That is exactly what this does.
func (m ParsedMessage) LeafParts() []Part {
	hasChild := make([]bool, len(m.Parts))
	insideRFC822 := make([]bool, len(m.Parts))
	for _, p := range m.Parts {
		if p.Parent >= 0 && p.Parent < len(hasChild) {
			hasChild[p.Parent] = true
			// Anything under a message/rfc822 is interior to it for counting.
			if m.Parts[p.Parent].IsRFC822 || insideRFC822[p.Parent] {
				insideRFC822[p.Index] = true
			}
		}
	}
	var out []Part
	for _, p := range m.Parts {
		if insideRFC822[p.Index] {
			continue
		}
		if !p.IsRFC822 && hasChild[p.Index] {
			continue
		}
		// A message truncated inside its header block has a header set but no
		// body at all — not an empty body, no body (corpus hdr-007, which ends
		// mid-token at byte 177 with no blank line). The parser still creates a
		// root entity to hang the headers on, but counting that as a content
		// part would claim the message has a body it does not have, so a
		// contentless root with no siblings is not a leaf.
		//
		// The distinction from structural-003 (a blank-line-only body, which
		// DOES have an empty body and expects 1 part) is the presence of the
		// header/body separator, which is exactly what RawSize vs the header
		// block records.
		if p.Index == 0 && p.Parent == -1 && len(m.Parts) == 1 &&
			len(p.Content) == 0 && m.truncatedHeaders {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Defect codes present, for tests and for metric aggregation.
func (m ParsedMessage) hasDefect(code DefectCode) bool {
	for _, d := range m.Defects {
		if d.Code == code {
			return true
		}
	}
	return false
}
