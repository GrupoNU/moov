package sync

import (
	"encoding/json"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GrupoNU/moov/internal/parser"
	"github.com/GrupoNU/moov/internal/store"
)

// previewLength is how much body text the message list shows. Long enough for
// a useful second line, short enough that a batch of 100 rows stays small.
const previewLength = 200

// parserVersion identifies the cascade that produced the stored derivation.
//
// It exists so a parser bump can re-derive exactly the affected rows without
// re-downloading anything (L2 §2.4: the blob is durable, parsing is a retryable
// derivation). Bump it when the parser's output changes meaningfully.
const parserVersion = 1

// newMessage maps a parsed message onto the store's row pair.
func (s *Syncer) newMessage(accountID, mailboxID int64, uidValidity uint32, pm *parsedMessage) store.NewMessage {
	p := pm.parsed
	h := p.Headers

	date := s.messageDate(h.Date, pm.raw.internalDate)

	msg := store.Message{
		AccountID: accountID,
		RawSHA256: pm.raw.hash.Bytes(),
		RawSize:   pm.raw.size,

		MessageID:     sanitizeText(h.MessageID),
		InReplyTo:     firstOrEmpty(h.InReplyTo),
		ReferencesIDs: sanitizeAll(h.References),

		Subject:  sanitizeText(h.Subject),
		FromAddr: sanitizeText(addressText(h.From)),
		ToAddrs:  sanitizeText(addressText(h.To)),
		CcAddrs:  sanitizeText(addressText(h.Cc)),

		Date: date,

		HasAttachments: len(p.Attachments()) > 0,
		Preview:        preview(p.BodyText),
		BodyText:       sanitizeText(p.BodyText),

		ParseStatus:   storeParseStatus(p.Status),
		Parser:        string(p.Parser),
		ParserVersion: parserVersion,
	}

	if !pm.raw.internalDate.IsZero() {
		internal := pm.raw.internalDate
		msg.InternalDate = &internal
	}

	// The JSON columns are built here rather than in the store because their
	// shape is this package's contract with the JMAP layer, not the database's.
	msg.Addresses = encodeAddresses(h)
	msg.MIMEStructure = encodeStructure(p.Parts)
	msg.Defects = encodeDefects(p.Defects)

	state := store.MessageState{
		AccountID:   accountID,
		MailboxID:   mailboxID,
		UID:         int64(pm.raw.uid),
		UIDValidity: int64(uidValidity),
		Flags:       pm.raw.flags,
		Keywords:    sanitizeAll(pm.raw.keywords),
		ModSeqSeen:  modSeqToDB(pm.raw.modSeq),
	}

	return store.NewMessage{Message: msg, State: state}
}

// threadCandidate extracts the headers threading reads from a store row.
//
// It is built from the already-mapped store.Message rather than from the parser
// output, so the sanitization the row went through (sanitizeText: valid UTF-8,
// no NUL) applies to the threading inputs too. A Message-ID carrying a NUL byte
// would otherwise be compared against, and stored in, a text column that
// rejects it.
//
// In-Reply-To is appended to References rather than kept apart: store.AssignThreads
// treats the whole set as unordered ancestors and takes the OLDEST match, so the
// distinction between "the direct parent" and "an ancestor" carries no
// information for the algorithm. Keeping them separate would only invite a
// caller to trust References' order, which real mailers do not preserve.
func threadCandidate(m *store.Message) store.ThreadCandidate {
	c := store.ThreadCandidate{
		MessageID: m.MessageID,
		Subject:   m.Subject,
	}
	if m.InReplyTo != "" {
		c.References = make([]string, 0, len(m.ReferencesIDs)+1)
		c.References = append(c.References, m.ReferencesIDs...)
		c.References = append(c.References, m.InReplyTo)
	} else if len(m.ReferencesIDs) > 0 {
		c.References = m.ReferencesIDs
	}
	return c
}

// messageDate decides what goes in the date column.
//
// The Date header wins when it parses, because it is what the sender meant and
// what every other client shows. INTERNALDATE is the fallback for the header
// being absent, unparseable, or absurd — and "absurd" is not pedantry: mail
// with a Date in 1970 or in 2087 is routine, and a single such message sorts to
// the top or the bottom of every list forever. The column is NOT NULL, so there
// has to be an answer either way; the last resort is now(), which at least puts
// the message where the user just received it.
func (s *Syncer) messageDate(header string, internal time.Time) time.Time {
	if header != "" {
		if t, err := mail.ParseDate(header); err == nil && plausibleDate(t) {
			return t
		}
	}
	if !internal.IsZero() {
		return internal
	}
	return s.opts.Clock()
}

// plausibleDate rejects timestamps outside the range in which email exists.
//
// The lower bound predates SMTP; the upper is far enough ahead that a genuinely
// scheduled message is unaffected and near enough that a typo'd or forged year
// is caught.
func plausibleDate(t time.Time) bool {
	year := t.UTC().Year()
	return year >= 1971 && year <= time.Now().UTC().Year()+2
}

// addressText renders an address list the way the FTS band expects it, which is
// the same form the parser's AddressText uses.
func addressText(addrs []parser.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if s := a.String(); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

// jsonAddress is the structured form the JMAP layer reads out of the
// `addresses` column.
type jsonAddress struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// encodeAddresses builds the addresses JSON document.
//
// Bcc is included here and excluded from the FTS text (parser contract §4.2):
// the JMAP layer legitimately shows a draft's own Bcc to its author, while
// making a blind copy discoverable through search would defeat the point of it
// being blind.
func encodeAddresses(h parser.CanonHeaders) []byte {
	doc := map[string][]jsonAddress{}
	for key, list := range map[string][]parser.Address{
		"from":    h.From,
		"sender":  h.Sender,
		"replyTo": h.ReplyTo,
		"to":      h.To,
		"cc":      h.Cc,
		"bcc":     h.Bcc,
	} {
		if len(list) == 0 {
			continue
		}
		out := make([]jsonAddress, 0, len(list))
		for _, a := range list {
			out = append(out, jsonAddress{
				Name:  sanitizeText(a.Name),
				Email: sanitizeText(a.Address),
			})
		}
		doc[key] = out
	}

	b, err := json.Marshal(doc)
	if err != nil {
		// Unreachable: the document is plain strings and slices. An empty
		// object is a valid value for the column, so a hypothetical failure
		// degrades one row's metadata instead of failing a whole batch.
		return []byte("{}")
	}
	return b
}

// jsonPart is one node of the stored MIME structure.
type jsonPart struct {
	Index        int    `json:"index"`
	Parent       int    `json:"parent"`
	Depth        int    `json:"depth"`
	MediaType    string `json:"mediaType"`
	Charset      string `json:"charset,omitempty"`
	Encoding     string `json:"encoding,omitempty"`
	Disposition  string `json:"disposition,omitempty"`
	Filename     string `json:"filename,omitempty"`
	ContentID    string `json:"contentId,omitempty"`
	Size         int    `json:"size"`
	IsAttachment bool   `json:"isAttachment,omitempty"`
	IsMultipart  bool   `json:"isMultipart,omitempty"`
	IsRFC822     bool   `json:"isRfc822,omitempty"`
	Partial      bool   `json:"partiallyDecoded,omitempty"`
}

// encodeStructure stores the flattened part tree WITHOUT part content.
//
// The content deliberately does not go in the database: a part's bytes are
// recoverable from the raw blob, which is the system of record, and storing
// them again would put megabytes of attachment into a jsonb column that the
// message list reads on every query.
func encodeStructure(parts []parser.Part) []byte {
	out := make([]jsonPart, 0, len(parts))
	for _, p := range parts {
		out = append(out, jsonPart{
			Index:        p.Index,
			Parent:       p.Parent,
			Depth:        p.Depth,
			MediaType:    p.MediaType,
			Charset:      sanitizeText(p.Charset),
			Encoding:     sanitizeText(p.Encoding),
			Disposition:  sanitizeText(p.Disposition),
			Filename:     sanitizeText(p.Filename),
			ContentID:    sanitizeText(p.ContentID),
			Size:         p.Size,
			IsAttachment: p.IsAttachment,
			IsMultipart:  p.IsMultipart,
			IsRFC822:     p.IsRFC822,
			Partial:      p.PartiallyDecoded,
		})
	}

	b, err := json.Marshal(map[string]any{"parts": out})
	if err != nil {
		return []byte("{}")
	}
	return b
}

// jsonDefect is one stored parse defect (S4 vocabulary).
type jsonDefect struct {
	Code   string `json:"code"`
	Part   int    `json:"part"`
	Detail string `json:"detail,omitempty"`
	Case   string `json:"corpusCase,omitempty"`
}

func encodeDefects(defects []parser.Defect) []byte {
	if len(defects) == 0 {
		return []byte("[]")
	}
	out := make([]jsonDefect, 0, len(defects))
	for _, d := range defects {
		out = append(out, jsonDefect{
			Code:   string(d.Code),
			Part:   d.Part,
			Detail: sanitizeText(d.Detail),
			Case:   d.CorpusCase,
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return []byte("[]")
	}
	return b
}

// storeParseStatus maps the parser's outcome onto the store's, written out
// rather than cast for the same reason as storeRole.
func storeParseStatus(p parser.ParseStatus) store.ParseStatus {
	switch p {
	case parser.StatusOK:
		return store.ParseOK
	case parser.StatusPartial:
		return store.ParsePartial
	case parser.StatusFailed:
		return store.ParseFailed
	default:
		return store.ParseOK
	}
}

// preview truncates body text for the message list, on a rune boundary.
func preview(body string) string {
	s := strings.Join(strings.Fields(body), " ")
	s = sanitizeText(s)
	if len(s) <= previewLength {
		return s
	}
	// Cut at previewLength BYTES, then back off to the last valid rune start,
	// so a multi-byte character is never split into invalid UTF-8 — which a
	// text column would reject outright.
	cut := s[:previewLength]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// sanitizeText makes a string safe for a PostgreSQL text column.
//
// The parser already guarantees valid UTF-8 without NUL (its §4.2 contract), so
// this is defense in depth at the boundary where a violation would become a
// failed transaction for a whole batch of a hundred messages rather than one
// bad row. The cost is a scan of strings that are already clean; the
// alternative is one malformed message aborting a batch.
func sanitizeText(s string) string {
	if s == "" {
		return ""
	}
	needsWork := !utf8.ValidString(s) || strings.IndexByte(s, 0) >= 0
	if !needsWork {
		return s
	}
	s = strings.ReplaceAll(s, "\x00", "")
	return strings.ToValidUTF8(s, "�")
}

func sanitizeAll(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, sanitizeText(s))
	}
	return out
}

func firstOrEmpty(in []string) string {
	if len(in) == 0 {
		return ""
	}
	return sanitizeText(in[0])
}
