package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

// The MIME assembler: RFC 8621 §4.6 creation objects in, RFC 5322/2045 bytes
// out. It is what Email/set create appends to Drafts, and therefore — via the
// outbox — what eventually travels over SMTP, so its output is held to the
// strictest producer profile rather than to what parsers tolerate:
//
//   - CRLF line endings throughout (RFC 5322 §2.1).
//   - Header lines folded at 78 characters where the syntax permits folding
//     (§2.1.1's SHOULD), never inside a quoted-string or an encoded-word.
//   - Non-ASCII header text as RFC 2047 encoded-words (Q for phrases and
//     subjects via mime.QEncoding, which also splits words at the 75-octet
//     cap); non-ASCII display names through net/mail's own quoting/encoding.
//   - text/* bodies in quoted-printable, UTF-8 (RFC 2045 §6.7): readable on
//     the wire, 7bit-safe on servers without 8BITMIME.
//   - Binary parts in base64 wrapped at 76 (RFC 2045 §6.8).
//   - A generated Message-ID (§3.6.4) and Date (§3.6.1) when the client set
//     none — every message this server ever transmits is identifiable, which
//     the outbox's dedupe net depends on.
//
// # Determinism
//
// Everything non-deterministic is a seam on the assembler (clock, entropy,
// boundary counter), injected in tests so the goldens are byte-exact. The
// round-trip property is tested against internal/parser itself: assembled
// bytes must parse back to the same bodies and structure — the strongest
// check available, since that parser is what will serve the draft's
// bodyValues right back to the client.

// assembledEmail is the assembler's output.
type assembledEmail struct {
	raw []byte
	// messageID is the RFC 5322 Message-ID (no angle brackets) the message
	// carries — generated, or the client's own.
	messageID string
}

// assembler renders one creation object. The zero value is not usable; build
// with newAssembler.
type assembler struct {
	now      func() time.Time
	random   func([]byte) error
	boundary int
	// boundarySeed makes boundaries unique across messages while the counter
	// makes them unique within one.
	boundarySeed string
}

func newAssembler(now func() time.Time, random func([]byte) error) (*assembler, error) {
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = func(b []byte) error {
			_, err := rand.Read(b)
			return err
		}
	}
	var seed [8]byte
	if err := random(seed[:]); err != nil {
		return nil, fmt.Errorf("mail: seeding MIME boundaries: %w", err)
	}
	return &assembler{now: now, random: random, boundarySeed: fmt.Sprintf("%x", seed)}, nil
}

// mimePart is one node of the tree to render: either a container (subParts)
// or a leaf (text or content).
type mimePart struct {
	mediaType   string // lowercased type/subtype
	disposition string // "", "attachment", "inline"
	filename    string
	cid         string // Content-ID without angle brackets

	// text is a leaf's decoded text (text/* parts from bodyValues).
	text string
	// content is a leaf's binary content (blob-backed parts).
	content []byte

	subParts []*mimePart
}

func (p *mimePart) isMultipart() bool { return strings.HasPrefix(p.mediaType, "multipart/") }
func (p *mimePart) isText() bool      { return strings.HasPrefix(p.mediaType, "text/") }

// emailHeaders is the header half of a creation object, already validated.
type emailHeaders struct {
	from    []EmailAddress
	sender  []EmailAddress
	replyTo []EmailAddress
	to      []EmailAddress
	cc      []EmailAddress
	bcc     []EmailAddress

	subject string

	// sentAt is the Date header value; zero means "now".
	sentAt time.Time

	// messageID is the client-supplied Message-ID (no brackets); empty means
	// "generate one".
	messageID  string
	inReplyTo  []string
	references []string

	// extra carries the permitted header:Name properties, in insertion order.
	extra []extraHeader
}

type extraHeader struct {
	name  string
	value string
}

// assemble renders the message.
func (a *assembler) assemble(h emailHeaders, root *mimePart, accountEmail string) (assembledEmail, error) {
	var out assembledEmail

	messageID := h.messageID
	if messageID == "" {
		id, err := a.newMessageID(accountEmail)
		if err != nil {
			return out, err
		}
		messageID = id
	}

	date := h.sentAt
	if date.IsZero() {
		date = a.now()
	}

	var b bytes.Buffer
	writeHeader(&b, "Date", date.UTC().Format(rfc5322DateLayout))
	if len(h.from) > 0 {
		writeHeader(&b, "From", formatAddressList(h.from))
	}
	if len(h.sender) > 0 {
		writeHeader(&b, "Sender", formatAddressList(h.sender))
	}
	if len(h.replyTo) > 0 {
		writeHeader(&b, "Reply-To", formatAddressList(h.replyTo))
	}
	if len(h.to) > 0 {
		writeHeader(&b, "To", formatAddressList(h.to))
	}
	if len(h.cc) > 0 {
		writeHeader(&b, "Cc", formatAddressList(h.cc))
	}
	if len(h.bcc) > 0 {
		// The DRAFT keeps its Bcc — the author may read their own draft back
		// (RFC 8621 §4.1.2.3 serves bcc to the mailbox owner). The outbox
		// strips it from the transmitted bytes (submit.PrepareTransmission,
		// RFC 5322 §3.6.3); it never leaves the account's own mailbox.
		writeHeader(&b, "Bcc", formatAddressList(h.bcc))
	}
	if h.subject != "" {
		writeHeader(&b, "Subject", encodeUnstructured(h.subject))
	}
	writeHeader(&b, "Message-ID", "<"+messageID+">")
	if len(h.inReplyTo) > 0 {
		writeHeader(&b, "In-Reply-To", formatMessageIDList(h.inReplyTo))
	}
	if len(h.references) > 0 {
		writeHeader(&b, "References", formatMessageIDList(h.references))
	}
	for _, x := range h.extra {
		writeHeader(&b, x.name, encodeUnstructured(x.value))
	}
	writeHeader(&b, "MIME-Version", "1.0")

	if err := a.writePart(&b, root, true); err != nil {
		return out, err
	}

	out.raw = b.Bytes()
	out.messageID = messageID
	return out, nil
}

// writePart renders one part: its content headers, a blank line, its body.
// topLevel parts share the message's header block (no separate blank line
// before their Content-* headers).
func (a *assembler) writePart(b *bytes.Buffer, p *mimePart, topLevel bool) error {
	switch {
	case p.isMultipart():
		boundary := a.nextBoundary()
		writeHeader(b, "Content-Type",
			mime.FormatMediaType(p.mediaType, map[string]string{"boundary": boundary}))
		b.WriteString("\r\n")
		for _, sub := range p.subParts {
			b.WriteString("--" + boundary + "\r\n")
			if err := a.writePart(b, sub, false); err != nil {
				return err
			}
		}
		b.WriteString("--" + boundary + "--\r\n")
		return nil

	case p.isText():
		params := map[string]string{"charset": "utf-8"}
		if p.filename != "" {
			// RFC 2183 puts the filename on Content-Disposition; the name
			// parameter here is the legacy spelling some clients still read.
			params["name"] = p.filename
		}
		writeHeader(b, "Content-Type", mime.FormatMediaType(p.mediaType, params))
		writeHeader(b, "Content-Transfer-Encoding", "quoted-printable")
		a.writeDispositionAndCID(b, p)
		b.WriteString("\r\n")
		return writeQuotedPrintable(b, p.text)

	default:
		params := map[string]string{}
		if p.filename != "" {
			params["name"] = p.filename
		}
		writeHeader(b, "Content-Type", mime.FormatMediaType(p.mediaType, params))
		writeHeader(b, "Content-Transfer-Encoding", "base64")
		a.writeDispositionAndCID(b, p)
		b.WriteString("\r\n")
		writeBase64(b, p.content)
		return nil
	}
}

func (a *assembler) writeDispositionAndCID(b *bytes.Buffer, p *mimePart) {
	if p.disposition != "" {
		params := map[string]string{}
		if p.filename != "" {
			// mime.FormatMediaType emits RFC 2231 extended syntax for a
			// non-ASCII filename, which is the standard's own answer.
			params["filename"] = p.filename
		}
		writeHeader(b, "Content-Disposition", mime.FormatMediaType(p.disposition, params))
	}
	if p.cid != "" {
		writeHeader(b, "Content-ID", "<"+p.cid+">")
	}
}

// nextBoundary mints a boundary unique within the message and across
// messages. The charset is RFC 2046 §5.1.1 bchars; "=_" cannot appear in
// quoted-printable output, the classic guard against boundary collisions.
func (a *assembler) nextBoundary() string {
	a.boundary++
	return fmt.Sprintf("=_moov_%s_%03d", a.boundarySeed, a.boundary)
}

// newMessageID mints an RFC 5322 §3.6.4 msg-id in the account's domain.
func (a *assembler) newMessageID(accountEmail string) (string, error) {
	var buf [16]byte
	if err := a.random(buf[:]); err != nil {
		return "", fmt.Errorf("mail: minting a Message-ID: %w", err)
	}
	domain := "moov.invalid"
	if _, d, ok := strings.Cut(accountEmail, "@"); ok && d != "" {
		domain = d
	}
	return fmt.Sprintf("%x.moov@%s", buf, domain), nil
}

// rfc5322DateLayout is the §3.3 date-time, numeric zone.
const rfc5322DateLayout = "Mon, 02 Jan 2006 15:04:05 +0000"

// ---------------------------------------------------------------------------
// header rendering
// ---------------------------------------------------------------------------

// writeHeader writes one header field, folded at 78 where fold points exist.
//
// Fold points are the "\r\n " sequences the VALUE already carries (the
// address and msg-id formatters place them between elements) plus plain
// spaces in unstructured values. Folding only at spaces can never split a
// quoted-string produced by net/mail (its output quotes whole phrases without
// internal line breaks) nor an encoded-word (which contains no spaces by
// construction, RFC 2047 §2).
func writeHeader(b *bytes.Buffer, name, value string) {
	const limit = 78
	line := name + ": "

	// Pre-folded values (formatters insert "\r\n " between elements) pass
	// through; each segment is then space-folded independently.
	for i, segment := range strings.Split(value, "\r\n ") {
		if i > 0 {
			b.WriteString(line)
			b.WriteString("\r\n")
			line = " "
		}
		for _, word := range strings.Split(segment, " ") {
			if word == "" {
				continue
			}
			switch {
			case line == name+": ":
				if len(line)+len(word) <= limit {
					line += word
				} else {
					// Fold between the colon and the first token — §2.2.3
					// permits FWS anywhere WSP may appear, and this is the
					// only way a name-plus-long-first-token line honors the
					// 78 SHOULD.
					b.WriteString(name + ":")
					b.WriteString("\r\n")
					line = " " + word
				}
			case line == " ":
				line += word
			case len(line)+1+len(word) <= limit:
				line += " " + word
			default:
				b.WriteString(line)
				b.WriteString("\r\n")
				line = " " + word
			}
		}
	}
	b.WriteString(line)
	b.WriteString("\r\n")
}

// formatAddressList renders addresses per RFC 5322 §3.4, with a fold point
// after each comma.
func formatAddressList(addrs []EmailAddress) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		parts = append(parts, (&mail.Address{Name: a.Name, Address: a.Email}).String())
	}
	return strings.Join(parts, ",\r\n ")
}

// formatMessageIDList renders msg-id lists (In-Reply-To, References) with a
// fold point between ids — §3.6.4 permits CFWS there and real chains get
// long.
func formatMessageIDList(ids []string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		id = strings.TrimPrefix(id, "<")
		id = strings.TrimSuffix(id, ">")
		if id != "" {
			parts = append(parts, "<"+id+">")
		}
	}
	return strings.Join(parts, "\r\n ")
}

// encodeUnstructured RFC 2047-encodes an unstructured header value when it
// needs it, word by word: mime.QEncoding encodes only what is non-ASCII and
// splits over-long words at the 75-octet cap itself.
func encodeUnstructured(s string) string {
	return mime.QEncoding.Encode("utf-8", s)
}

// ---------------------------------------------------------------------------
// body encodings
// ---------------------------------------------------------------------------

// writeQuotedPrintable encodes text per RFC 2045 §6.7. The encoder normalizes
// nothing about line endings, so the text's own "\n" are first turned into
// CRLF — quoted-printable transports hard line breaks as real CRLF.
func writeQuotedPrintable(b *bytes.Buffer, text string) error {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\n", "\r\n")
	w := quotedprintable.NewWriter(b)
	if _, err := io.WriteString(w, text); err != nil {
		return fmt.Errorf("mail: quoted-printable body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: quoted-printable body: %w", err)
	}
	// The encoder does not terminate the final line; a part's body must end
	// with CRLF before the next boundary (RFC 2046 §5.1.1 counts that CRLF as
	// the boundary's own).
	if !bytes.HasSuffix(b.Bytes(), []byte("\r\n")) {
		b.WriteString("\r\n")
	}
	return nil
}

// writeBase64 encodes content per RFC 2045 §6.8, wrapped at 76.
func writeBase64(b *bytes.Buffer, content []byte) {
	enc := base64.StdEncoding.EncodeToString(content)
	for len(enc) > 0 {
		n := min(76, len(enc))
		b.WriteString(enc[:n])
		b.WriteString("\r\n")
		enc = enc[n:]
	}
}
