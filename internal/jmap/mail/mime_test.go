package mail

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/parser"
)

// The assembler's goldens and its strongest property: ROUND-TRIP through the
// repo's own parser. Whatever mime.go emits is exactly what internal/parser
// will later re-derive bodyValues from (Email/get on the draft) and what
// internal/sync stores at reflection time — so "the parser reads back what
// the assembler meant" is not a nicety, it is the draft feature working.

// testAssembler returns an assembler whose clock, entropy and boundaries are
// fixed, so its output is byte-stable.
func testAssembler(t *testing.T) *assembler {
	t.Helper()
	asm, err := newAssembler(
		func() time.Time { return time.Date(2026, 8, 15, 10, 30, 0, 0, time.UTC) },
		func(b []byte) error {
			for i := range b {
				b[i] = 0xAB
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("newAssembler: %v", err)
	}
	return asm
}

func mustAssemble(t *testing.T, h emailHeaders, root *mimePart) assembledEmail {
	t.Helper()
	out, err := testAssembler(t).assemble(h, root, "moov-test@atmosfera.cloud")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	return out
}

// reparse runs the assembled bytes through the production parser cascade.
func reparse(t *testing.T, raw []byte) parser.ParsedMessage {
	t.Helper()
	p := parser.Parse(bytes.NewReader(raw), parser.Limits{})
	if p.Status == parser.StatusFailed {
		t.Fatalf("the parser failed on assembled output — the assembler emits mail the product cannot read:\n%s", raw)
	}
	return p
}

func TestAssembleTextOnlyGolden(t *testing.T) {
	got := mustAssemble(t,
		emailHeaders{
			to:      []EmailAddress{{Name: "Bob", Email: "bob@example.com"}},
			subject: "hola",
		},
		&mimePart{mediaType: "text/plain", text: "hola\n"},
	)

	want := "Date: Sat, 15 Aug 2026 10:30:00 +0000\r\n" +
		"To: \"Bob\" <bob@example.com>\r\n" +
		"Subject: hola\r\n" +
		"Message-ID: <abababababababababababababababab.moov@atmosfera.cloud>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"hola\r\n"
	if string(got.raw) != want {
		t.Errorf("golden mismatch:\n got %q\nwant %q", got.raw, want)
	}
	if got.messageID != "abababababababababababababababab.moov@atmosfera.cloud" {
		t.Errorf("messageID = %q", got.messageID)
	}
}

func TestAssembleAlternativeWithAttachmentRoundTrips(t *testing.T) {
	pdf := []byte{0x25, 0x50, 0x44, 0x46, 0x00, 0xFF, 0x01} // binary, base64 territory
	got := mustAssemble(t,
		emailHeaders{
			from:    []EmailAddress{{Email: "moov-test@atmosfera.cloud"}},
			to:      []EmailAddress{{Email: "dest@example.test"}},
			subject: "informe",
		},
		&mimePart{mediaType: "multipart/mixed", subParts: []*mimePart{
			{mediaType: "multipart/alternative", subParts: []*mimePart{
				{mediaType: "text/plain", text: "versión en texto\n"},
				{mediaType: "text/html", text: "<p>versión en <b>html</b></p>\n"},
			}},
			{mediaType: "application/pdf", disposition: "attachment", filename: "informe.pdf", content: pdf},
		}},
	)

	p := reparse(t, got.raw)
	if p.Headers.Subject != "informe" {
		t.Errorf("subject round-trip = %q", p.Headers.Subject)
	}
	if !strings.Contains(p.BodyText, "versión en texto") {
		t.Errorf("text body lost in round-trip: %q", p.BodyText)
	}

	var types []string
	var attachment *parser.Part
	for i := range p.Parts {
		types = append(types, p.Parts[i].MediaType)
		if p.Parts[i].MediaType == "application/pdf" {
			attachment = &p.Parts[i]
		}
	}
	joined := strings.Join(types, " ")
	for _, want := range []string{"multipart/mixed", "multipart/alternative", "text/plain", "text/html", "application/pdf"} {
		if !strings.Contains(joined, want) {
			t.Errorf("round-tripped structure lacks %s (got %s)", want, joined)
		}
	}
	if attachment == nil || !attachment.IsAttachment || attachment.Filename != "informe.pdf" {
		t.Errorf("attachment round-trip = %+v", attachment)
	}
}

func TestAssembleEncodedWordsRoundTrip(t *testing.T) {
	// Non-ASCII in exactly the places clients put it: the subject and a
	// display name. RFC 2047 encoded-words on the wire, the original strings
	// after the round-trip.
	subject := "Peñarol — señales y contraseñas: ¡éxito!"
	got := mustAssemble(t,
		emailHeaders{
			from:    []EmailAddress{{Name: "Ana Muñoz", Email: "moov-test@atmosfera.cloud"}},
			to:      []EmailAddress{{Email: "dest@example.test"}},
			subject: subject,
		},
		&mimePart{mediaType: "text/plain", text: "cuerpo con acentos: áéíóú\n"},
	)

	header := string(got.raw[:bytes.Index(got.raw, []byte("\r\n\r\n"))])
	if strings.ContainsAny(header, "ñáéíóú¡—") {
		t.Errorf("raw non-ASCII leaked into the header block (RFC 2047 requires encoded-words):\n%s", header)
	}
	if !strings.Contains(header, "=?utf-8?") {
		t.Errorf("no encoded-word in the header block:\n%s", header)
	}

	p := reparse(t, got.raw)
	if p.Headers.Subject != subject {
		t.Errorf("subject round-trip:\n got %q\nwant %q", p.Headers.Subject, subject)
	}
	if len(p.Headers.From) != 1 || p.Headers.From[0].Name != "Ana Muñoz" {
		t.Errorf("display-name round-trip = %+v", p.Headers.From)
	}
	if !strings.Contains(p.BodyText, "áéíóú") {
		t.Errorf("body round-trip lost the accents: %q", p.BodyText)
	}
}

func TestAssembleInlineCidBuildsRelated(t *testing.T) {
	got := mustAssemble(t,
		emailHeaders{to: []EmailAddress{{Email: "dest@example.test"}}},
		&mimePart{mediaType: "multipart/related", subParts: []*mimePart{
			{mediaType: "text/html", text: `<img src="cid:logo1">`},
			{mediaType: "image/png", disposition: "inline", cid: "logo1", filename: "logo.png",
				content: []byte{0x89, 0x50, 0x4E, 0x47}},
		}},
	)

	raw := string(got.raw)
	if !strings.Contains(raw, "Content-ID: <logo1>") {
		t.Errorf("inline part lost its Content-ID:\n%s", raw)
	}
	if !strings.Contains(raw, "Content-Disposition: inline") {
		t.Errorf("inline part lost its disposition:\n%s", raw)
	}
	p := reparse(t, got.raw)
	found := false
	for _, part := range p.Parts {
		if part.MediaType == "image/png" && part.ContentID != "" && part.Disposition == "inline" {
			found = true
		}
	}
	if !found {
		t.Errorf("round-tripped parts lack the inline cid image: %+v", p.Parts)
	}
}

func TestAssembleHeaderLinesStayWithinLimits(t *testing.T) {
	// RFC 5322 §2.1.1: 998 hard, 78 SHOULD. A 40-recipient To and a long
	// subject must fold; the hard limit is asserted absolutely, the soft one
	// with the slack real formatters need (one over-long unbreakable token —
	// an address, an encoded-word — may exceed 78 but never 998).
	to := make([]EmailAddress, 40)
	for i := range to {
		to[i] = EmailAddress{Name: "Destinatario Número Cuarenta", Email: strings.Repeat("x", 12) + "@example.test"}
	}
	got := mustAssemble(t,
		emailHeaders{
			to:      to,
			subject: strings.Repeat("palabras y más palabras con acentós ", 8),
		},
		&mimePart{mediaType: "text/plain", text: "x\n"},
	)

	header := got.raw[:bytes.Index(got.raw, []byte("\r\n\r\n"))]
	over78 := 0
	for _, line := range bytes.Split(header, []byte("\r\n")) {
		if len(line) > 998 {
			t.Fatalf("header line exceeds the RFC 5322 hard limit (%d bytes): %q", len(line), line)
		}
		if len(line) > 78 {
			over78++
		}
	}
	// The fold points exist between every address and every encoded word, so
	// nothing here is unbreakable: the soft limit must actually hold.
	if over78 > 0 {
		t.Errorf("%d header lines exceed the 78-octet SHOULD despite available fold points", over78)
	}

	p := reparse(t, got.raw)
	if len(p.Headers.To) != 40 {
		t.Errorf("folded To round-tripped to %d addresses, want 40", len(p.Headers.To))
	}
}

func TestAssembleDotLeadingLinesSurviveTheWholePath(t *testing.T) {
	// The body a user actually types can start lines with dots; the assembler
	// must produce bytes whose parse — and later SMTP dot-stuffing
	// (internal/submit) — round-trip them. Quoted-printable happens to make
	// this trivially safe; this test pins that it STAYS safe.
	body := ".\n..\n.leading\nnormal\n"
	got := mustAssemble(t,
		emailHeaders{to: []EmailAddress{{Email: "d@example.test"}}},
		&mimePart{mediaType: "text/plain", text: body},
	)
	p := reparse(t, got.raw)
	for _, want := range []string{"..", ".leading", "normal"} {
		if !strings.Contains(p.BodyText, want) {
			t.Errorf("body lost %q through assemble+parse: %q", want, p.BodyText)
		}
	}
}

func TestAssembleThreadingHeaders(t *testing.T) {
	got := mustAssemble(t,
		emailHeaders{
			to:         []EmailAddress{{Email: "d@example.test"}},
			inReplyTo:  []string{"parent@example.test"},
			references: []string{"root@example.test", "<parent@example.test>"},
		},
		&mimePart{mediaType: "text/plain", text: "re\n"},
	)
	p := reparse(t, got.raw)
	if len(p.Headers.InReplyTo) != 1 || p.Headers.InReplyTo[0] != "parent@example.test" {
		t.Errorf("In-Reply-To round-trip = %v", p.Headers.InReplyTo)
	}
	// The formatter must normalize bracketed and bare ids alike.
	if len(p.Headers.References) != 2 || p.Headers.References[1] != "parent@example.test" {
		t.Errorf("References round-trip = %v", p.Headers.References)
	}
}

func TestAssembleClientMessageIDWins(t *testing.T) {
	got := mustAssemble(t,
		emailHeaders{
			to:        []EmailAddress{{Email: "d@example.test"}},
			messageID: "client-chosen@example.test",
		},
		&mimePart{mediaType: "text/plain", text: "x\n"},
	)
	if got.messageID != "client-chosen@example.test" {
		t.Errorf("messageID = %q, want the client's", got.messageID)
	}
	p := reparse(t, got.raw)
	if p.Headers.MessageID != "client-chosen@example.test" {
		t.Errorf("Message-ID round-trip = %q", p.Headers.MessageID)
	}
}

func TestAssembleIsDeterministic(t *testing.T) {
	h := emailHeaders{to: []EmailAddress{{Email: "d@example.test"}}, subject: "det"}
	part := func() *mimePart {
		return &mimePart{mediaType: "multipart/alternative", subParts: []*mimePart{
			{mediaType: "text/plain", text: "a\n"},
			{mediaType: "text/html", text: "<p>a</p>\n"},
		}}
	}
	a := mustAssemble(t, h, part())
	b := mustAssemble(t, h, part())
	if !bytes.Equal(a.raw, b.raw) {
		t.Error("two assemblies with fixed seams differ — the goldens cannot hold")
	}
}
