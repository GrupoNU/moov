package parser

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for the TNEF/winmail.dat decoder (plan L3 decision D-6).
//
// The corpus cases in testdata/mime-corpus/10-tnef cover the end-to-end path
// through Parse; what follows tests the decoder directly, where a hostile stream
// can be constructed byte by byte without wrapping it in a message first. The
// bounds tests are the important ones: they are the difference between a
// decoder and a memory-exhaustion vector.

// --------------------------------------------------------------------------
// Stream construction helpers, mirroring the corpus generator
// --------------------------------------------------------------------------

func tnefHeader() []byte {
	b := make([]byte, 6)
	binary.LittleEndian.PutUint32(b[:4], tnefSignature)
	binary.LittleEndian.PutUint16(b[4:], 1)
	return b
}

// tnefAttr builds one attribute: level, id, length, payload, checksum.
func tnefAttr(level byte, id uint32, payload []byte) []byte {
	b := make([]byte, 0, 9+len(payload)+2)
	b = append(b, level)
	b = binary.LittleEndian.AppendUint32(b, id)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(payload)))
	b = append(b, payload...)
	var sum uint16
	for _, c := range payload {
		sum += uint16(c)
	}
	return binary.LittleEndian.AppendUint16(b, sum)
}

// tnefRendData is the 14-byte AttachRenddata that opens an attachment.
func tnefRendData() []byte { return make([]byte, 14) }

// tnefOneAttachment builds a complete single-attachment stream.
func tnefOneAttachment(name string, data []byte) []byte {
	s := tnefHeader()
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachRendData, tnefRendData())...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachTitle, append([]byte(name), 0))...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachData, data)...)
	return s
}

// tnefLongNameProps builds a MAPI blob carrying PR_ATTACH_LONG_FILENAME.
func tnefLongNameProps(name string) []byte {
	u := make([]byte, 0, len(name)*2+2)
	for _, r := range name {
		u = binary.LittleEndian.AppendUint16(u, uint16(r))
	}
	u = binary.LittleEndian.AppendUint16(u, 0)

	b := make([]byte, 0, 16+len(u))
	b = binary.LittleEndian.AppendUint32(b, 1) // property count
	b = binary.LittleEndian.AppendUint32(b, uint32(mapiTagAttachLongFilename)<<16|mapiTypeUnicode)
	b = binary.LittleEndian.AppendUint32(b, 1) // value count
	b = binary.LittleEndian.AppendUint32(b, uint32(len(u)))
	return append(b, u...)
}

const (
	testMaxAttachment = 25 << 20
	testMaxTotal      = 25 << 20
)

// --------------------------------------------------------------------------
// Detection
// --------------------------------------------------------------------------

func TestIsTNEFPart(t *testing.T) {
	cases := []struct {
		name      string
		part      Part
		want      bool
		rationale string
	}{
		{
			name: "ms-tnef media type",
			part: Part{MediaType: "application/ms-tnef"},
			want: true,
		},
		{
			name: "vendor-prefixed media type",
			part: Part{MediaType: "application/vnd.ms-tnef"},
			want: true,
		},
		{
			name:      "media type in mixed case",
			part:      Part{MediaType: "Application/MS-TNEF"},
			want:      true,
			rationale: "media types are case-insensitive; a sender using title case is not opting out",
		},
		{
			name:      "filename alone, type relabeled by a gateway",
			part:      Part{MediaType: "application/octet-stream", Filename: "winmail.dat"},
			want:      true,
			rationale: "gateways routinely rewrite the type and leave the name; either signal alone must suffice",
		},
		{
			name: "filename in mixed case",
			part: Part{MediaType: "application/octet-stream", Filename: "WinMail.DAT"},
			want: true,
		},
		{
			name:      "traversal-prefixed filename",
			part:      Part{MediaType: "application/octet-stream", Filename: `..\..\winmail.dat`},
			want:      true,
			rationale: "classification is on the base name, so a traversal attempt cannot dodge detection",
		},
		{
			name: "ordinary attachment",
			part: Part{MediaType: "application/pdf", Filename: "invoice.pdf"},
			want: false,
		},
		{
			name:      "name merely containing winmail.dat",
			part:      Part{MediaType: "application/octet-stream", Filename: "not-winmail.dat.zip"},
			want:      false,
			rationale: "a substring match would drag unrelated archives into the decoder",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTNEFPart(tc.part); got != tc.want {
				t.Errorf("isTNEFPart = %v, want %v\nrationale: %s", got, tc.want, tc.rationale)
			}
		})
	}
}

// --------------------------------------------------------------------------
// Extraction
// --------------------------------------------------------------------------

func TestDecodeTNEFSingleAttachment(t *testing.T) {
	payload := []byte("%PDF-1.4 synthetic\n")
	atts, err := decodeTNEF(tnefOneAttachment("REPORT.PDF", payload), testMaxAttachment, testMaxTotal)
	if err != nil {
		t.Fatalf("decodeTNEF: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("got %d attachments, want 1", len(atts))
	}
	if atts[0].Filename != "REPORT.PDF" {
		t.Errorf("filename = %q, want REPORT.PDF", atts[0].Filename)
	}
	if string(atts[0].Data) != string(payload) {
		t.Errorf("data = %q, want %q", atts[0].Data, payload)
	}
}

func TestDecodeTNEFBinaryClean(t *testing.T) {
	// Every byte value, including NUL: an attachment is bytes, not text, and a
	// decoder that treats attAttachData as a C string truncates at the first
	// NUL, which for any real binary file means the first few bytes.
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}
	atts, err := decodeTNEF(tnefOneAttachment("DATA.BIN", payload), testMaxAttachment, testMaxTotal)
	if err != nil {
		t.Fatalf("decodeTNEF: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("got %d attachments, want 1", len(atts))
	}
	if len(atts[0].Data) != 256 {
		t.Fatalf("data length = %d, want 256 (binary payload was truncated)", len(atts[0].Data))
	}
	for i, b := range atts[0].Data {
		if b != byte(i) {
			t.Fatalf("data[%d] = %d, want %d", i, b, i)
		}
	}
}

func TestDecodeTNEFDoesNotAliasInput(t *testing.T) {
	// The decoder must COPY attachment bytes out of the stream. Aliasing would
	// keep the entire winmail.dat payload reachable behind one small attachment
	// for the lifetime of the message, and would let a later mutation of the
	// source buffer silently rewrite stored content.
	stream := tnefOneAttachment("A.TXT", []byte("original"))
	atts, err := decodeTNEF(stream, testMaxAttachment, testMaxTotal)
	if err != nil || len(atts) != 1 {
		t.Fatalf("decodeTNEF: %v, %d attachments", err, len(atts))
	}
	for i := range stream {
		stream[i] = 'X'
	}
	if string(atts[0].Data) != "original" {
		t.Errorf("attachment data aliased the input buffer: got %q", atts[0].Data)
	}
}

func TestDecodeTNEFTwoAttachments(t *testing.T) {
	s := tnefHeader()
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachRendData, tnefRendData())...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachTitle, append([]byte("ONE.TXT"), 0))...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachData, []byte("first"))...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachRendData, tnefRendData())...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachTitle, append([]byte("TWO.TXT"), 0))...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachData, []byte("second"))...)

	atts, err := decodeTNEF(s, testMaxAttachment, testMaxTotal)
	if err != nil {
		t.Fatalf("decodeTNEF: %v", err)
	}
	if len(atts) != 2 {
		t.Fatalf("got %d attachments, want 2 — attAttachRendData must delimit the list", len(atts))
	}
	if atts[0].Filename != "ONE.TXT" || string(atts[0].Data) != "first" {
		t.Errorf("attachment 0 = %q/%q", atts[0].Filename, atts[0].Data)
	}
	if atts[1].Filename != "TWO.TXT" || string(atts[1].Data) != "second" {
		t.Errorf("attachment 1 = %q/%q", atts[1].Filename, atts[1].Data)
	}
}

func TestDecodeTNEFLongFilenameWins(t *testing.T) {
	const long = "Quarterly Financial Report 2026.xlsx"

	// Asserted in BOTH orders because the stream does not guarantee one: the
	// MAPI blob may arrive before or after the data attribute, and a decoder
	// that resolves the name eagerly at attAttachData gets one of these wrong.
	for _, propsFirst := range []bool{true, false} {
		name := "props-after-data"
		if propsFirst {
			name = "props-before-data"
		}
		t.Run(name, func(t *testing.T) {
			s := tnefHeader()
			s = append(s, tnefAttr(tnefLevelAttachment, attAttachRendData, tnefRendData())...)
			s = append(s, tnefAttr(tnefLevelAttachment, attAttachTitle, append([]byte("QUARTE~1.XLS"), 0))...)
			if propsFirst {
				s = append(s, tnefAttr(tnefLevelAttachment, attAttachment, tnefLongNameProps(long))...)
				s = append(s, tnefAttr(tnefLevelAttachment, attAttachData, []byte("xlsx bytes"))...)
			} else {
				s = append(s, tnefAttr(tnefLevelAttachment, attAttachData, []byte("xlsx bytes"))...)
				s = append(s, tnefAttr(tnefLevelAttachment, attAttachment, tnefLongNameProps(long))...)
			}

			atts, err := decodeTNEF(s, testMaxAttachment, testMaxTotal)
			if err != nil {
				t.Fatalf("decodeTNEF: %v", err)
			}
			if len(atts) != 1 {
				t.Fatalf("got %d attachments, want 1", len(atts))
			}
			if atts[0].Filename != long {
				t.Errorf("filename = %q, want the long name %q (the 8.3 title must lose)",
					atts[0].Filename, long)
			}
		})
	}
}

func TestDecodeTNEFSkipsMessageLevel(t *testing.T) {
	const attSubject = 0x18004
	s := tnefHeader()
	s = append(s, tnefAttr(tnefLevelMessage, attSubject, append([]byte("a subject"), 0))...)
	// A message-level attribute reusing an ATTACHMENT attribute id: dispatching
	// on the id without checking the level would attribute this to a file.
	s = append(s, tnefAttr(tnefLevelMessage, attAttachData, []byte("NOT an attachment"))...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachRendData, tnefRendData())...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachTitle, append([]byte("REAL.TXT"), 0))...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachData, []byte("the real one"))...)

	atts, err := decodeTNEF(s, testMaxAttachment, testMaxTotal)
	if err != nil {
		t.Fatalf("decodeTNEF: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("got %d attachments, want 1 — message-level attributes must be skipped", len(atts))
	}
	if string(atts[0].Data) != "the real one" {
		t.Errorf("data = %q; a message-level attribute was misread as attachment content", atts[0].Data)
	}
}

func TestDecodeTNEFZeroAttachments(t *testing.T) {
	const attSubject = 0x18004
	s := append(tnefHeader(), tnefAttr(tnefLevelMessage, attSubject, append([]byte("x"), 0))...)

	atts, err := decodeTNEF(s, testMaxAttachment, testMaxTotal)
	if err != nil {
		t.Fatalf("a valid container with no files is not an error, got: %v", err)
	}
	if len(atts) != 0 {
		t.Fatalf("got %d attachments, want 0", len(atts))
	}
}

func TestDecodeTNEFDropsPlaceholderWithoutData(t *testing.T) {
	// An attachment record with a name but no attAttachData is a rendering
	// placeholder. Emitting it would put a 0-byte entry in the user's
	// attachment list for a file that does not exist.
	s := tnefHeader()
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachRendData, tnefRendData())...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachTitle, append([]byte("GHOST.TXT"), 0))...)

	atts, err := decodeTNEF(s, testMaxAttachment, testMaxTotal)
	if err != nil {
		t.Fatalf("decodeTNEF: %v", err)
	}
	if len(atts) != 0 {
		t.Fatalf("got %d attachments, want 0 (a dataless record is not a file)", len(atts))
	}
}

// --------------------------------------------------------------------------
// Rejection and bounds — the tests that matter most
// --------------------------------------------------------------------------

func TestDecodeTNEFRejectsNonTNEF(t *testing.T) {
	cases := map[string][]byte{
		"empty":               {},
		"too short":           {0x78, 0x9f},
		"wrong signature":     {0xef, 0xbe, 0xad, 0xde, 0x01, 0x00},
		"plain text":          []byte("this is not a TNEF stream at all"),
		"signature truncated": {0x78, 0x9f, 0x3e},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			atts, err := decodeTNEF(data, testMaxAttachment, testMaxTotal)
			if err == nil {
				t.Fatal("want an error for non-TNEF input")
			}
			// errNotTNEF specifically: the caller keeps this SILENT, because a
			// mislabeled part is the sender's problem and recording a defect
			// would make the metric measure the internet rather than this
			// decoder.
			if !strings.Contains(err.Error(), "not a TNEF stream") {
				t.Errorf("want errNotTNEF, got %v", err)
			}
			if len(atts) != 0 {
				t.Errorf("got %d attachments from non-TNEF input", len(atts))
			}
		})
	}
}

func TestDecodeTNEFRejectsLengthOverflow(t *testing.T) {
	// The vector: a 32-bit length far past the end of the buffer. A decoder
	// that allocates on it, or that adds it to the offset in a wrapping width,
	// is a memory-exhaustion bug reachable by any sender.
	for _, length := range []uint32{0xFFFFFFF0, 0xFFFFFFFF, 0x7FFFFFFF, 1 << 24} {
		s := tnefHeader()
		s = append(s, tnefAttr(tnefLevelAttachment, attAttachRendData, tnefRendData())...)
		s = append(s, tnefLevelAttachment)
		s = binary.LittleEndian.AppendUint32(s, attAttachData)
		s = binary.LittleEndian.AppendUint32(s, length)
		s = append(s, []byte("tiny")...)

		atts, err := decodeTNEF(s, testMaxAttachment, testMaxTotal)
		if err == nil {
			t.Errorf("length %#x: want a refusal, got none", length)
		}
		if len(atts) != 0 {
			t.Errorf("length %#x: got %d attachments from an impossible length", length, len(atts))
		}
	}
}

func TestDecodeTNEFKeepsWhatDecodedBeforeTruncation(t *testing.T) {
	s := tnefOneAttachment("GOOD.TXT", []byte("complete"))
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachRendData, tnefRendData())...)
	s = append(s, tnefLevelAttachment)
	s = binary.LittleEndian.AppendUint32(s, attAttachData)
	s = binary.LittleEndian.AppendUint32(s, 4096)
	s = append(s, []byte("only a few")...)

	atts, err := decodeTNEF(s, testMaxAttachment, testMaxTotal)
	if err == nil {
		t.Fatal("want an error reporting the truncation")
	}
	if len(atts) != 1 {
		t.Fatalf("got %d attachments, want 1 — what decoded before the fault must be kept", len(atts))
	}
	if string(atts[0].Data) != "complete" {
		t.Errorf("recovered data = %q, want %q", atts[0].Data, "complete")
	}
}

func TestDecodeTNEFPerAttachmentCap(t *testing.T) {
	// A single attachment over the cap is refused; the walk continues, because
	// the other files in the same container are still fine.
	s := tnefHeader()
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachRendData, tnefRendData())...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachTitle, append([]byte("BIG.BIN"), 0))...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachData, make([]byte, 4096))...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachRendData, tnefRendData())...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachTitle, append([]byte("SMALL.TXT"), 0))...)
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachData, []byte("fits"))...)

	atts, err := decodeTNEF(s, 1024, testMaxTotal)
	if err != nil {
		t.Fatalf("decodeTNEF: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("got %d attachments, want 1 (the oversized one dropped, the small one kept)", len(atts))
	}
	if atts[0].Filename != "SMALL.TXT" {
		t.Errorf("kept %q, want SMALL.TXT", atts[0].Filename)
	}
}

func TestDecodeTNEFTotalCap(t *testing.T) {
	s := tnefHeader()
	for i := 0; i < 8; i++ {
		s = append(s, tnefAttr(tnefLevelAttachment, attAttachRendData, tnefRendData())...)
		s = append(s, tnefAttr(tnefLevelAttachment, attAttachTitle, append([]byte("F.BIN"), 0))...)
		s = append(s, tnefAttr(tnefLevelAttachment, attAttachData, make([]byte, 512))...)
	}

	atts, err := decodeTNEF(s, testMaxAttachment, 1024)
	if err == nil {
		t.Fatal("want a refusal once the total cap is exceeded")
	}
	var total int
	for _, a := range atts {
		total += len(a.Data)
	}
	if total > 1024 {
		t.Errorf("extracted %d bytes total, over the 1024 cap", total)
	}
}

func TestDecodeTNEFAttributeCountCap(t *testing.T) {
	// A stream of many tiny attributes must terminate on the count bound rather
	// than on the input running out.
	s := tnefHeader()
	for i := 0; i < maxTNEFAttributes+100; i++ {
		s = append(s, tnefAttr(tnefLevelMessage, 0x18004, []byte{0})...)
	}
	atts, err := decodeTNEF(s, testMaxAttachment, testMaxTotal)
	if err == nil {
		t.Fatal("want a refusal at the attribute cap")
	}
	if !strings.Contains(err.Error(), "attribute count cap") {
		t.Errorf("want the attribute cap error, got %v", err)
	}
	if len(atts) != 0 {
		t.Errorf("got %d attachments", len(atts))
	}
}

func TestDecodeTNEFUnknownLevelStops(t *testing.T) {
	s := tnefHeader()
	s = append(s, tnefAttr(0x7f, attAttachData, []byte("garbage"))...)
	if _, err := decodeTNEF(s, testMaxAttachment, testMaxTotal); err == nil {
		t.Fatal("want a refusal on an unknown level marker")
	}
}

// --------------------------------------------------------------------------
// Filename handling
// --------------------------------------------------------------------------

func TestSanitizeTNEFFilename(t *testing.T) {
	cases := []struct {
		in, want, why string
	}{
		{"invoice.pdf", "invoice.pdf", "an ordinary name passes through"},
		{`..\..\..\windows\system32\evil.exe`, "evil.exe",
			"a synthesized part has no original to preserve, so the safe form is the only form"},
		{"../../etc/passwd", "passwd", "POSIX traversal is stripped to the base name"},
		{"a/b/c.txt", "c.txt", "any separator is stripped"},
		{"bad\nname.txt", "badname.txt",
			"a newline in a filename is how a header injection reaches a Content-Disposition"},
		{"with\x00nul.txt", "withnul.txt", "NUL cannot reach a PostgreSQL text column"},
		{"", "attachment-7", "an empty name gets a synthesized one rather than a blank entry"},
		{"...", "attachment-7", "a name of only dots resolves to nothing usable"},
		{"..", "attachment-7", "the parent directory is not a filename"},
		{"   spaced.txt   ", "spaced.txt", "surrounding whitespace is noise"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := sanitizeTNEFFilename(tc.in, 7); got != tc.want {
				t.Errorf("sanitizeTNEFFilename(%q) = %q, want %q\nwhy: %s",
					tc.in, got, tc.want, tc.why)
			}
		})
	}
}

func TestTNEFMediaType(t *testing.T) {
	cases := map[string]string{
		"report.pdf":  "application/pdf",
		"notes.txt":   "text/plain",
		"data.bin":    "application/octet-stream",
		"noextension": "application/octet-stream",
		"weird.zzzzz": "application/octet-stream",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got := tnefMediaType(name)
			if got != want {
				t.Errorf("tnefMediaType(%q) = %q, want %q", name, got, want)
			}
			// Whatever the answer, it must be a bare type/subtype: the Part
			// contract has no room for parameters here.
			if strings.ContainsAny(got, "; ") {
				t.Errorf("media type %q carries parameters", got)
			}
		})
	}
}

func TestDecodeTNEFStringUTF16(t *testing.T) {
	// A UTF-16LE filename, as Outlook writes it.
	var b []byte
	for _, r := range "Résumé.docx" {
		b = binary.LittleEndian.AppendUint16(b, uint16(r))
	}
	b = binary.LittleEndian.AppendUint16(b, 0)

	if got := decodeTNEFString(b); got != "Résumé.docx" {
		t.Errorf("decodeTNEFString = %q, want %q", got, "Résumé.docx")
	}
}

func TestDecodeTNEFString8Bit(t *testing.T) {
	if got := decodeTNEFString(append([]byte("PLAIN.TXT"), 0)); got != "PLAIN.TXT" {
		t.Errorf("decodeTNEFString = %q, want PLAIN.TXT", got)
	}
}

// --------------------------------------------------------------------------
// End to end, through Parse
// --------------------------------------------------------------------------

func TestParseExtractsTNEFAttachments(t *testing.T) {
	raw := buildTNEFMessage(t, tnefOneAttachment("REPORT.PDF", []byte("%PDF-1.4 synthetic\n")))

	msg := ParseBytes(raw, DefaultLimits())
	if msg.Status == StatusFailed {
		t.Fatalf("parse failed: %v", msg.Defects)
	}

	var extracted, original *Part
	for i := range msg.Parts {
		switch msg.Parts[i].Filename {
		case "REPORT.PDF":
			extracted = &msg.Parts[i]
		case "winmail.dat":
			original = &msg.Parts[i]
		}
	}

	if extracted == nil {
		t.Fatalf("REPORT.PDF was not extracted; parts: %v", partSummary(msg))
	}
	if !extracted.IsAttachment {
		t.Error("the extracted part is not marked as an attachment")
	}
	if extracted.MediaType != "application/pdf" {
		t.Errorf("extracted media type = %q, want application/pdf", extracted.MediaType)
	}
	if string(extracted.Content) != "%PDF-1.4 synthetic\n" {
		t.Errorf("extracted content = %q", extracted.Content)
	}
	if extracted.Size != len(extracted.Content) {
		t.Errorf("Size %d != len(Content) %d", extracted.Size, len(extracted.Content))
	}

	// The original must survive: it is the only artifact that lets a user
	// recover the file by hand if this decoder ever gets one wrong.
	if original == nil {
		t.Fatal("the winmail.dat original was consumed; it must remain available")
	}
	if !original.IsAttachment {
		t.Error("the winmail.dat original stopped being an attachment")
	}
	if extracted.Parent != original.Index {
		t.Errorf("extracted part parent = %d, want the winmail.dat index %d",
			extracted.Parent, original.Index)
	}

	if !msg.hasDefect(DefectTNEFExtracted) {
		t.Error("no tnef_extracted defect recorded; the extraction is not auditable")
	}
}

func TestParseTNEFStructuralInvariants(t *testing.T) {
	// The invariants every consumer relies on must survive part synthesis:
	// Index == position, Parent strictly backwards, Size == len(Content). A
	// violation would corrupt the store's MIME structure document and the JMAP
	// layer's per-part blobIds, which are derived from these indices.
	streams := map[string][]byte{
		"one":       tnefOneAttachment("A.TXT", []byte("a")),
		"binary":    tnefOneAttachment("B.BIN", []byte{0, 1, 2, 3, 0xff}),
		"truncated": append(tnefOneAttachment("C.TXT", []byte("c")), 0x02, 0x0f, 0x80),
	}
	for name, stream := range streams {
		t.Run(name, func(t *testing.T) {
			msg := ParseBytes(buildTNEFMessage(t, stream), DefaultLimits())
			for i, p := range msg.Parts {
				if p.Index != i {
					t.Errorf("part %d has Index %d", i, p.Index)
				}
				if p.Parent >= i || p.Parent < -1 {
					t.Errorf("part %d has invalid parent %d", i, p.Parent)
				}
				if p.Size != len(p.Content) {
					t.Errorf("part %d: Size %d != len(Content) %d", i, p.Size, len(p.Content))
				}
			}
		})
	}
}

func TestParseTNEFRespectsPartCap(t *testing.T) {
	// A container with many attachments must not be able to push the message
	// past the parser's own MaxParts. TNEF is not an exemption from the caps.
	s := tnefHeader()
	for i := 0; i < 50; i++ {
		s = append(s, tnefAttr(tnefLevelAttachment, attAttachRendData, tnefRendData())...)
		s = append(s, tnefAttr(tnefLevelAttachment, attAttachTitle, append([]byte("F.TXT"), 0))...)
		s = append(s, tnefAttr(tnefLevelAttachment, attAttachData, []byte("x"))...)
	}

	limits := DefaultLimits()
	limits.MaxParts = 8
	msg := ParseBytes(buildTNEFMessage(t, s), limits)
	if len(msg.Parts) > limits.MaxParts {
		t.Errorf("%d parts exceeds MaxParts %d — TNEF bypassed the cap",
			len(msg.Parts), limits.MaxParts)
	}
}

func TestParseUndecodableTNEFStaysOpaque(t *testing.T) {
	// The degrade-never-block rule: a broken container costs the user nothing
	// relative to a client that never tried to unpack it.
	s := tnefHeader()
	s = append(s, tnefAttr(tnefLevelAttachment, attAttachRendData, tnefRendData())...)
	s = append(s, tnefLevelAttachment)
	s = binary.LittleEndian.AppendUint32(s, attAttachData)
	s = binary.LittleEndian.AppendUint32(s, 0xFFFFFFF0)
	s = append(s, []byte("tiny")...)

	msg := ParseBytes(buildTNEFMessage(t, s), DefaultLimits())
	if msg.Status == StatusFailed {
		t.Fatal("a broken TNEF container must never fail the whole message")
	}
	if len(msg.Attachments()) != 1 {
		t.Errorf("got %d attachments, want just the opaque winmail.dat", len(msg.Attachments()))
	}
	if !msg.hasDefect(DefectTNEFUndecodable) {
		t.Error("no tnef_undecodable defect recorded")
	}
	// The body must still be readable: the failure is confined to one part.
	if !strings.Contains(msg.BodyText, "Rich Text Format") {
		t.Errorf("body text lost: %q", msg.BodyText)
	}
}

func TestParseNonTNEFPartIsSilent(t *testing.T) {
	// A part labeled winmail.dat that is not TNEF must record NO defect: it is
	// the sending gateway's doing, not damage to this message, and a defect
	// here would make the metric measure the internet instead of this decoder.
	msg := ParseBytes(buildTNEFMessage(t, []byte("this was never a TNEF stream")), DefaultLimits())
	if msg.hasDefect(DefectTNEFUndecodable) {
		t.Error("a not-TNEF part recorded a defect; detection failures must be silent")
	}
	if msg.hasDefect(DefectTNEFExtracted) {
		t.Error("a not-TNEF part reported an extraction")
	}
	if msg.Status != StatusOK {
		t.Errorf("status = %s, want ok — nothing is wrong with this message", msg.Status)
	}
}

func TestParseTNEFTextAttachmentReachesIndex(t *testing.T) {
	// A text file inside a winmail.dat must reach the FTS text, which is the
	// product reason extraction runs before assembleFTS: a user searching for a
	// word that exists only inside the Outlook container must find the message.
	const secret = "quarterlyreconciliation"
	stream := tnefOneAttachment("NOTES.TXT", []byte("contains "+secret+" inside\n"))

	msg := ParseBytes(buildTNEFMessage(t, stream), DefaultLimits())
	if !strings.Contains(msg.BodyText, secret) {
		t.Errorf("text extracted from winmail.dat did not reach BodyText: %q", msg.BodyText)
	}
}

// buildTNEFMessage wraps a raw TNEF stream in the multipart/mixed message shape
// Outlook produces, base64-encoding the container as a real message does.
func buildTNEFMessage(t *testing.T, stream []byte) []byte {
	t.Helper()

	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var enc strings.Builder
	for i := 0; i < len(stream); i += 3 {
		var chunk [3]byte
		n := copy(chunk[:], stream[i:])
		enc.WriteByte(alphabet[chunk[0]>>2])
		enc.WriteByte(alphabet[(chunk[0]&0x03)<<4|chunk[1]>>4])
		if n > 1 {
			enc.WriteByte(alphabet[(chunk[1]&0x0f)<<2|chunk[2]>>6])
		} else {
			enc.WriteByte('=')
		}
		if n > 2 {
			enc.WriteByte(alphabet[chunk[2]&0x3f])
		} else {
			enc.WriteByte('=')
		}
	}

	// Wrap at 76 characters, as a real mailer does.
	b64 := enc.String()
	var wrapped strings.Builder
	for i := 0; i < len(b64); i += 76 {
		end := i + 76
		if end > len(b64) {
			end = len(b64)
		}
		if i > 0 {
			wrapped.WriteString("\r\n")
		}
		wrapped.WriteString(b64[i:end])
	}

	return []byte("From: Outlook User <outlook@example.com>\r\n" +
		"To: Moov Tester <moov-test@example.org>\r\n" +
		"Subject: TNEF test\r\n" +
		"MIME-Version: 1.0\r\n" +
		`Content-Type: multipart/mixed; boundary="=_t_="` + "\r\n\r\n" +
		"--=_t_=\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n\r\n" +
		"This message was sent with Rich Text Format.\r\n" +
		"--=_t_=\r\n" +
		`Content-Type: application/ms-tnef; name="winmail.dat"` + "\r\n" +
		`Content-Disposition: attachment; filename="winmail.dat"` + "\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		wrapped.String() + "\r\n" +
		"--=_t_=--\r\n")
}

// partSummary renders the part tree for a failure message.
func partSummary(m ParsedMessage) string {
	var b strings.Builder
	for _, p := range m.Parts {
		b.WriteString("\n  [" + itoa(p.Index) + "] parent=" + itoa(p.Parent) +
			" " + p.MediaType + " file=" + p.Filename)
	}
	return b.String()
}

// --------------------------------------------------------------------------
// Fuzzing — the E4 discipline, applied to the format's own decoder
// --------------------------------------------------------------------------

// FuzzTNEF drives decodeTNEF directly with arbitrary bytes.
//
// FuzzParse already reaches this code through the corpus seeds, but only behind
// base64 decoding and a MIME walk, so the fuzzer spends nearly all of its budget
// producing inputs that never reach the TNEF path at all. This target hands the
// mutator the container bytes directly, which is where the length arithmetic
// lives.
//
// The bar, from E4: no panic, no hang, no unbounded allocation. The last is the
// one this format invites — every attribute carries an attacker-controlled
// 32-bit length — and it is asserted here as a property (nothing extracted may
// exceed the caps) rather than left to a memory limit to catch.
//
// Run beyond the seeds with:
//
//	go test ./internal/parser -run FuzzTNEF -fuzz FuzzTNEF -fuzztime 60s
func FuzzTNEF(f *testing.F) {
	// Seed from the corpus TNEF cases, which are real message files; the base64
	// container inside them is what the mutator will learn from.
	dir := filepath.Join(corpusDir(), "10-tnef")
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if data, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil { //nolint:gosec // fixed test-data path
				f.Add(data)
			}
		}
	}

	// Raw container seeds: these reach the attribute walk on the first
	// execution rather than after the fuzzer rediscovers the signature.
	f.Add(tnefOneAttachment("A.TXT", []byte("hello")))
	f.Add(tnefOneAttachment("B.BIN", []byte{0, 1, 2, 3, 0xff, 0xfe}))
	f.Add(tnefHeader())
	f.Add([]byte{})
	f.Add([]byte("not tnef"))

	// The known hazard shape, seeded explicitly and kept in version control
	// rather than left to the fuzzing cache: an attribute length far past the
	// end of the input.
	overflow := tnefHeader()
	overflow = append(overflow, tnefLevelAttachment)
	overflow = binary.LittleEndian.AppendUint32(overflow, attAttachData)
	overflow = binary.LittleEndian.AppendUint32(overflow, 0xFFFFFFFF)
	overflow = append(overflow, []byte("x")...)
	f.Add(overflow)

	// A MAPI blob that lies, which is what the bounded long-filename scan has
	// to survive without desynchronizing.
	props := tnefHeader()
	props = append(props, tnefAttr(tnefLevelAttachment, attAttachRendData, tnefRendData())...)
	props = append(props, tnefAttr(tnefLevelAttachment, attAttachment,
		binary.LittleEndian.AppendUint32(
			binary.LittleEndian.AppendUint32(
				binary.LittleEndian.AppendUint32(nil, 1),
				uint32(mapiTagAttachLongFilename)<<16|mapiTypeUnicode),
			0xFFFFFFFF))...)
	props = append(props, tnefAttr(tnefLevelAttachment, attAttachData, []byte("d"))...)
	f.Add(props)

	f.Fuzz(func(t *testing.T, data []byte) {
		// Caps tight enough that a violation is unmistakable and the fuzzer
		// spends its time on the walk rather than on allocation.
		const (
			maxAttachment = 1 << 16
			maxTotal      = 1 << 17
		)

		atts, err := decodeTNEF(data, maxAttachment, maxTotal)

		// A non-nil error may still come with recovered attachments (the
		// keep-what-decoded rule), so both are always checked.
		if len(atts) > maxTNEFAttachments {
			t.Fatalf("%d attachments exceeds the cap of %d", len(atts), maxTNEFAttachments)
		}

		var total int
		for i, a := range atts {
			if len(a.Data) > maxAttachment {
				t.Fatalf("attachment %d is %d bytes, over the per-attachment cap",
					i, len(a.Data))
			}
			total += len(a.Data)

			// A filename must be storable: the synthesized Part carries it into
			// a PostgreSQL text column, which rejects NUL outright.
			if strings.IndexByte(a.Filename, 0) >= 0 {
				t.Fatalf("attachment %d filename contains NUL: %q", i, a.Filename)
			}
			if len(a.Filename) > maxTNEFFilename {
				t.Fatalf("attachment %d filename is %d bytes, over the cap",
					i, len(a.Filename))
			}

			// And it must survive sanitization into something a filesystem and
			// a header can both hold.
			clean := sanitizeTNEFFilename(a.Filename, i)
			if clean == "" {
				t.Fatalf("attachment %d sanitized to an empty filename", i)
			}
			if strings.ContainsAny(clean, `/\`) || strings.Contains(clean, "..") {
				t.Fatalf("attachment %d sanitized to a traversal-capable name: %q", i, clean)
			}
			for _, r := range clean {
				if r < 0x20 || r == 0x7f {
					t.Fatalf("attachment %d sanitized name holds a control character: %q",
						i, clean)
				}
			}
		}
		if total > maxTotal {
			t.Fatalf("extracted %d bytes total, over the cap of %d", total, maxTotal)
		}

		// Non-TNEF input must never yield attachments: the signature check is
		// the gate, and a bypass would mean arbitrary bytes reaching the walk.
		if err != nil && strings.Contains(err.Error(), "not a TNEF stream") && len(atts) > 0 {
			t.Fatalf("errNotTNEF returned alongside %d attachments", len(atts))
		}

		// Determinism: a reparse after a version bump must not silently change
		// what a user sees.
		again, err2 := decodeTNEF(data, maxAttachment, maxTotal)
		if len(again) != len(atts) || (err == nil) != (err2 == nil) {
			t.Fatal("decodeTNEF is not deterministic for identical input")
		}
	})
}

// FuzzTNEFMessage drives the whole pipeline with a TNEF container embedded in a
// real message, which is the path production actually takes: base64 decode, MIME
// walk, then extraction and part synthesis.
//
// It exists separately from FuzzTNEF because the invariants it can assert are
// different and stronger — the synthesized parts must satisfy every structural
// rule the rest of the package guarantees, and a violation there corrupts the
// store's MIME document rather than merely losing an attachment.
func FuzzTNEFMessage(f *testing.F) {
	f.Add(tnefOneAttachment("A.TXT", []byte("hello")))
	f.Add(tnefHeader())
	f.Add([]byte("not tnef at all"))

	f.Fuzz(func(t *testing.T, stream []byte) {
		const maxStream = 32 << 10
		if len(stream) > maxStream {
			t.Skip("stream larger than the fuzzing budget")
		}

		raw := buildTNEFMessage(t, stream)
		limits := Limits{
			MaxDepth: 20, MaxParts: 50, MaxTotalSize: 1 << 20,
			MaxRFC822Depth: 4, MaxPartSize: 1 << 18,
		}
		msg := ParseBytes(raw, limits)

		if len(msg.Parts) > limits.MaxParts {
			t.Fatalf("%d parts exceeds MaxParts %d", len(msg.Parts), limits.MaxParts)
		}
		for i, p := range msg.Parts {
			if p.Index != i {
				t.Fatalf("part %d has Index %d", i, p.Index)
			}
			if p.Parent >= i || p.Parent < -1 {
				t.Fatalf("part %d has invalid parent %d", i, p.Parent)
			}
			if p.Size != len(p.Content) {
				t.Fatalf("part %d: Size %d != len(Content) %d", i, p.Size, len(p.Content))
			}
			if int64(len(p.Content)) > limits.MaxPartSize {
				t.Fatalf("part %d exceeds MaxPartSize", i)
			}
			if strings.IndexByte(p.Filename, 0) >= 0 {
				t.Fatalf("part %d filename contains NUL", i)
			}
		}

		// The message body is a plain text/plain sibling and must survive
		// whatever the container does: a broken winmail.dat must never take the
		// readable part of the message down with it.
		if msg.Status == StatusFailed {
			t.Fatal("a TNEF container failed the whole message")
		}
		if !strings.Contains(msg.BodyText, "Rich Text Format") {
			t.Fatalf("the plain-text body was lost: %q", msg.BodyText)
		}
	})
}
