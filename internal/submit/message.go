package submit

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

// Transmission preparation: what happens to a draft's raw bytes between the
// blob store and MAIL FROM.
//
// The transmitted bytes are a pure, deterministic function of (draft blob,
// intent row): PrepareTransmission(raw, messageID, createdAt) yields the same
// bytes on every call, which is load-bearing twice over —
//
//   - the \Sent copy MUST be the transmitted bytes (rule 5), so the post-send
//     APPEND re-derives them instead of holding them in memory across a
//     possible crash;
//   - the Message-ID dedupe net only catches a replay if a re-preparation
//     after a crash produces the SAME Message-ID, which is why the id is
//     generated once at enqueue and stored on the intent row, never here.
//
// # What is changed, and what is not
//
//   - Bcc is REMOVED from the transmitted headers (RFC 5322 §3.6.3: "the
//     'Bcc:' line is removed even though all of the recipients (including
//     those specified in the 'Bcc:' field) are sent a copy of the message").
//     The blind recipients ride only in the envelope's RCPT TO. The DRAFT
//     blob keeps its Bcc — a draft's own author may see it — and so does the
//     \Sent copy, deliberately NOT: the Sent copy is the transmitted bytes,
//     and re-adding Bcc there would mean two divergent byte streams to
//     reason about. The author's Bcc survives in the store's addresses
//     column, which is where Email/get reads it.
//   - Message-ID and Date are ADDED when absent (RFC 5322 §3.6 SHOULDs both;
//     Moov's own drafts always carry them, so this fires only for drafts
//     other IMAP clients wrote). The Date used is the intent's creation time
//     — a fixed input, see above.
//   - Line endings are normalized to CRLF (RFC 5321 §2.3.8). Moov-assembled
//     drafts are CRLF already; a foreign draft with bare LF would otherwise
//     confuse dot-stuffing on strict servers.
//
// Nothing else is touched: the bytes the user's client assembled are the
// bytes that travel.

// PrepareTransmission derives the transmitted bytes from a draft's raw bytes.
func PrepareTransmission(raw []byte, messageID string, createdAt time.Time) []byte {
	out := normalizeCRLF(raw)
	out = stripHeader(out, "Bcc")
	header, _ := splitHeaderBody(out)
	var add []byte
	if headerValue(header, "Message-ID") == "" && messageID != "" {
		add = append(add, []byte("Message-ID: <"+messageID+">\r\n")...)
	}
	if headerValue(header, "Date") == "" {
		add = append(add, []byte("Date: "+createdAt.UTC().Format(rfc5322Date)+"\r\n")...)
	}
	if len(add) > 0 {
		out = append(add, out...)
	}
	return out
}

// rfc5322Date is the RFC 5322 §3.3 date-time layout. time.RFC1123Z matches it
// for a numeric zone; +0000 spells UTC per the RFC's preference for the
// numeric form.
const rfc5322Date = "Mon, 02 Jan 2006 15:04:05 +0000"

// HeaderValue returns the first value of a top-level header, unfolded and
// trimmed. Empty when absent. It operates on a full message or on a header
// block alike.
func HeaderValue(raw []byte, name string) string {
	header, _ := splitHeaderBody(normalizeCRLF(raw))
	return headerValue(header, name)
}

// MessageIDOf extracts the RFC 5322 Message-ID with angle brackets stripped,
// or "" when the message has none.
func MessageIDOf(raw []byte) string {
	v := HeaderValue(raw, "Message-ID")
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "<")
	v = strings.TrimSuffix(v, ">")
	return strings.TrimSpace(v)
}

// ---------------------------------------------------------------------------
// internals
// ---------------------------------------------------------------------------

// splitHeaderBody splits at the first blank line. The body half includes the
// separator's trailing bytes' remainder (i.e. everything after CRLFCRLF); a
// message without one is all header, per RFC 5322 §3.5's optional body.
func splitHeaderBody(raw []byte) (header, body []byte) {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i+2], raw[i+4:]
	}
	return raw, nil
}

// stripHeader removes every occurrence of a top-level header, folded
// continuation lines included (RFC 5322 §2.2.3: a line starting with WSP
// continues the previous header field).
func stripHeader(raw []byte, name string) []byte {
	header, body := splitHeaderBody(raw)

	var out bytes.Buffer
	out.Grow(len(raw))
	skipping := false
	for _, line := range splitLines(header) {
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if !skipping {
				out.Write(line)
			}
			continue
		}
		skipping = isHeaderNamed(line, name)
		if !skipping {
			out.Write(line)
		}
	}
	if body != nil {
		out.WriteString("\r\n")
		out.Write(body)
	}
	return out.Bytes()
}

// headerValue finds the first field with the given name in a header block and
// returns its unfolded, trimmed value.
func headerValue(header []byte, name string) string {
	lines := splitLines(header)
	for i, line := range lines {
		if !isHeaderNamed(line, name) {
			continue
		}
		_, v, _ := bytes.Cut(line, []byte(":"))
		value := strings.TrimRight(string(v), "\r\n")
		for j := i + 1; j < len(lines); j++ {
			next := lines[j]
			if len(next) == 0 || (next[0] != ' ' && next[0] != '\t') {
				break
			}
			value += " " + strings.TrimSpace(strings.TrimRight(string(next), "\r\n"))
		}
		return strings.TrimSpace(value)
	}
	return ""
}

// isHeaderNamed reports whether a header line's field name equals name,
// case-insensitively (RFC 5322 §1.2.2).
func isHeaderNamed(line []byte, name string) bool {
	rest, ok := cutFold(line, name)
	if !ok {
		return false
	}
	// The colon may be preceded by nothing per RFC 5322's strict grammar, but
	// obsolete syntax (§4.5.8) permits WSP before it and real mail contains
	// it; a stripper that misses "Bcc :" would leak blind recipients.
	rest = bytes.TrimLeft(rest, " \t")
	return len(rest) > 0 && rest[0] == ':'
}

// cutFold is bytes.CutPrefix under ASCII case folding.
func cutFold(b []byte, prefix string) ([]byte, bool) {
	if len(b) < len(prefix) {
		return nil, false
	}
	if !strings.EqualFold(string(b[:len(prefix)]), prefix) {
		return nil, false
	}
	return b[len(prefix):], true
}

// splitLines splits a header block into lines, each keeping its terminator.
func splitLines(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			out = append(out, b)
			break
		}
		out = append(out, b[:i+1])
		b = b[i+1:]
	}
	return out
}

// normalizeCRLF rewrites bare LF (and bare CR) line endings to CRLF. Input
// that is already CRLF returns unchanged without allocating.
func normalizeCRLF(raw []byte) []byte {
	clean := true
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '\n':
			if i == 0 || raw[i-1] != '\r' {
				clean = false
			}
		case '\r':
			if i+1 >= len(raw) || raw[i+1] != '\n' {
				clean = false
			}
		}
		if !clean {
			break
		}
	}
	if clean {
		return raw
	}

	var out bytes.Buffer
	out.Grow(len(raw) + len(raw)/16)
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch c {
		case '\r':
			if i+1 < len(raw) && raw[i+1] == '\n' {
				out.WriteString("\r\n")
				i++
			} else {
				out.WriteString("\r\n")
			}
		case '\n':
			out.WriteString("\r\n")
		default:
			out.WriteByte(c)
		}
	}
	return out.Bytes()
}

// NewMessageID mints an RFC 5322 §3.6.4 Message-ID (without angle brackets)
// in the account's domain, from the given entropy source.
func NewMessageID(random func([]byte) error, accountEmail string) (string, error) {
	var buf [16]byte
	if err := random(buf[:]); err != nil {
		return "", fmt.Errorf("submit: minting a Message-ID: %w", err)
	}
	domain := "moov.invalid"
	if _, d, ok := strings.Cut(accountEmail, "@"); ok && d != "" {
		domain = d
	}
	return fmt.Sprintf("%x.moov@%s", buf, domain), nil
}
