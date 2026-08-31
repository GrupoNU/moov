package parser

import (
	"encoding/binary"
	"errors"
	"mime"
	"path"
	"strings"
	"unicode/utf16"
)

// TNEF (Transport Neutral Encapsulation Format) attachment extraction — plan
// L3 §D-6, signed YES and assigned to this cascade.
//
// # Why this exists
//
// Outlook, when it sends "Rich Text Format" mail to a non-Exchange recipient,
// packs the formatted body AND every real attachment into a single opaque
// application/ms-tnef part named winmail.dat. A client that does not decode it
// shows the user one useless file where an invoice PDF and three photos should
// be. Gmail does not extract TNEF; our audience is Mailcow, which is
// Outlook-heavy, so D-6 puts us ahead of the benchmark rather than at parity —
// the reasoning recorded in the plan's decision table.
//
// # Scope, deliberately narrow
//
// ATTACHMENTS ONLY. This decoder extracts attached files (their bytes and their
// names) and nothing else:
//
//   - No MAPI/OLE property parsing. The attMAPIProps blob is a second,
//     differently-shaped TLV format nested inside this one, and every byte of it
//     is attacker-controlled. Its value here would be marginal metadata.
//   - No compressed-RTF decoding. winmail.dat's body is an LZ-compressed RTF
//     stream (MS-OXRTFCP); decompressing it would be a decompression bomb
//     surface for a body the message almost always ALSO carries as a plain
//     text/html sibling. The body is dropped honestly rather than half-decoded.
//
// The attachments are the value; the rest is where the hazards live.
//
// # Hand-rolled rather than vendored, and why
//
// github.com/teamwork/tnef (MIT) was evaluated. It was rejected because the
// format's attachment subset is roughly ten attribute IDs over a flat
// length-prefixed stream — the code below — while the library reads
// attacker-controlled 32-bit lengths and allocates on them directly, which is
// the precise shape of the superlinear-allocation defect fuzzing already found
// in enmime during E4. Vendoring it would mean auditing and patching it to the
// bounds discipline this file states outright, for a format simple enough not to
// need the dependency. The module vendors everything, so a dependency is also a
// permanent supply-chain surface in a public AGPL repo.
//
// # Never fails the message
//
// Every function here degrades rather than errors upward. When a stream is
// truncated, lies about a length, or is not TNEF at all, the winmail.dat part
// simply stays what it already was: an opaque attachment the user can still
// download and open in Outlook. That is the cascade's degrade-never-block rule
// (L2 §2.4), and it is why extraction runs AFTER a successful parse rather than
// inside either library's walk.

// tnefSignature is the magic that opens every TNEF stream, stored
// little-endian (MS-OXTNEF §2.1.1: TNEF_SIGNATURE = 0x223E9F78).
const tnefSignature = 0x223E9F78

// TNEF level markers (MS-OXTNEF §2.1.3.1). Attributes at message level describe
// the message; attributes at attachment level belong to the attachment most
// recently opened by attAttachRendData.
const (
	tnefLevelMessage    = 0x01
	tnefLevelAttachment = 0x02
)

// The attribute identifiers this decoder acts on. The full set is far larger;
// everything not named here is skipped by length, which is why an unknown or
// future attribute costs nothing.
const (
	// attAttachRendData opens a new attachment. It is the delimiter that makes
	// the flat stream parseable as a list: every attachment-level attribute
	// after it belongs to that attachment, until the next one.
	attAttachRendData = 0x9002

	// attAttachData carries the attachment's bytes.
	attAttachData = 0x800F

	// attAttachTitle is the 8.3 short filename, NUL-terminated ASCII.
	attAttachTitle = 0x8010

	// attAttachment is the attachment's MAPI property blob. Not parsed (see
	// scope above), but it is where attAttachLongFilename lives in real Outlook
	// output, so its long-filename property is extracted by a narrow, bounded
	// scan rather than a full MAPI parse.
	attAttachment = 0x9005
)

// mapiAttachLongFilename is the MAPI property tag PR_ATTACH_LONG_FILENAME
// (0x3707) with type PT_UNICODE (0x001F) or PT_STRING8 (0x001E). Long filename
// wins over attAttachTitle when both are present, per the spec for this task:
// the short name is a lossy 8.3 truncation ("INVOIC~1.PDF") and showing it to a
// user when the real name is available would be a self-inflicted regression.
const (
	mapiTagAttachLongFilename = 0x3707
	mapiTypeUnicode           = 0x001F
	mapiTypeString8           = 0x001E
)

// TNEF bounds. Every one of these is a hard refusal, not a truncation: a stream
// that trips a cap is a stream we decline to trust, and declining leaves the
// original winmail.dat intact for the user.
//
// The per-attachment and total caps are expressed against Limits.MaxPartSize so
// that TNEF can never become a way to exceed the caps the rest of the parser
// already enforces. An operator who tightens MaxPartSize tightens this too,
// without knowing this file exists.
const (
	// maxTNEFAttributes bounds the walk itself. A TNEF stream from any real
	// mailer holds a few dozen attributes; thousands means a generator, and the
	// walk must terminate on a bound rather than on the input running out.
	maxTNEFAttributes = 4096

	// maxTNEFAttachments bounds how many attachments one winmail.dat may yield.
	// Well above any real message, low enough that a crafted stream cannot
	// multiply one message into a part explosion. The parser's own MaxParts is
	// also re-checked as extraction proceeds, so this is the inner of two bounds.
	maxTNEFAttachments = 256

	// maxTNEFFilename bounds a decoded filename before sanitization.
	maxTNEFFilename = 255

	// maxTNEFPropScan bounds the MAPI blob bytes scanned for the long filename.
	// The scan is a bounded look for one tag, NOT a parse of the property
	// stream; past this point the short title is used instead.
	maxTNEFPropScan = 1 << 20 // 1 MB
)

// tnefAttachment is one file recovered from a TNEF stream.
type tnefAttachment struct {
	// Filename is the best name found: the long filename when the MAPI blob
	// carried one, otherwise the 8.3 title, otherwise "" for the caller to
	// synthesize.
	Filename string

	// Data is the attachment's decoded bytes.
	Data []byte
}

// errNotTNEF reports that the bytes are not a TNEF stream at all — the common,
// uninteresting case of a part mislabeled application/ms-tnef, or a winmail.dat
// that some gateway already replaced with a placeholder.
var errNotTNEF = errors.New("parser: not a TNEF stream")

// isTNEFPart reports whether a part should be sent down the TNEF path.
//
// Both signals from the spec are honored, and either alone is enough, because
// real mail gets one or the other wrong constantly: gateways relabel the
// content type to application/octet-stream while keeping the name, and some
// mailers emit application/ms-tnef with no filename at all.
func isTNEFPart(p Part) bool {
	switch strings.ToLower(strings.TrimSpace(p.MediaType)) {
	case "application/ms-tnef", "application/vnd.ms-tnef":
		return true
	}
	// The filename check is on the BASE name, so a traversal attempt
	// ("../../winmail.dat") is recognized as TNEF rather than slipping past the
	// check because of its prefix. Sanitization of the name happens later and
	// separately; this is only classification.
	name := strings.ToLower(strings.TrimSpace(p.Filename))
	if name == "" {
		return false
	}
	return path.Base(strings.ReplaceAll(name, `\`, "/")) == "winmail.dat"
}

// decodeTNEF walks a TNEF stream and returns the attachments it carries.
//
// It returns errNotTNEF when the signature does not match, and a descriptive
// error when the stream is TNEF but unusable. In BOTH cases the caller keeps the
// original part; the distinction exists only so the defect detail can say which
// happened, which is the difference between "this sender's Outlook is
// misconfigured" and "this stream was truncated in transit".
//
// The returned attachments may be non-empty alongside a nil error only. A
// partial walk that hits a malformed attribute keeps what it already decoded and
// stops there: half an invoice recovered beats none, and the original
// winmail.dat remains available either way.
func decodeTNEF(data []byte, maxAttachment, maxTotal int64) ([]tnefAttachment, error) {
	// Signature (4) + key (2) is the minimum before any attribute can begin.
	const headerLen = 6
	if len(data) < headerLen {
		return nil, errNotTNEF
	}
	if binary.LittleEndian.Uint32(data[:4]) != tnefSignature {
		return nil, errNotTNEF
	}

	var (
		out       []tnefAttachment
		cur       *tnefAttachment
		total     int64
		attrCount int
	)

	// pending holds the MAPI blob of the attachment currently open, so the long
	// filename can be resolved once the attachment is complete regardless of
	// whether attAttachment arrived before or after attAttachData.
	var pendingProps []byte

	flush := func() {
		if cur == nil {
			return
		}
		if name := longFilenameFromProps(pendingProps); name != "" {
			cur.Filename = name
		}
		// An attachment with no data is a rendering placeholder, not a file.
		// Emitting it would put a 0-byte entry in the user's attachment list for
		// something that was never there.
		if len(cur.Data) > 0 {
			out = append(out, *cur)
		}
		cur, pendingProps = nil, nil
	}

	pos := headerLen
	for pos < len(data) {
		if attrCount >= maxTNEFAttributes {
			flush()
			return out, errors.New("TNEF attribute count cap exceeded")
		}
		attrCount++

		// Attribute header: level(1) + id(4) + length(4). The checksum(2) that
		// follows the payload is deliberately NOT verified — a wrong checksum in
		// a stream that otherwise decodes cleanly is a reason to distrust a
		// sender's mailer, not a reason to withhold the user's invoice, and
		// several real gateways rewrite payloads without fixing it.
		const attrHeaderLen = 9
		if pos+attrHeaderLen > len(data) {
			// Trailing bytes too short to be an attribute: an ordinary truncated
			// stream. Keep whatever was decoded.
			flush()
			return out, nil
		}

		level := data[pos]
		id := binary.LittleEndian.Uint32(data[pos+1 : pos+5])
		length := binary.LittleEndian.Uint32(data[pos+5 : pos+9])
		pos += attrHeaderLen

		// THE bounds check. A 32-bit length is fully attacker-controlled, and
		// this comparison is done in int64 against the bytes that actually
		// REMAIN — never by allocating length bytes and hoping, and never in a
		// width where the addition could wrap. This is the check whose absence
		// makes every naive TNEF decoder a memory-exhaustion vector.
		remaining := int64(len(data) - pos)
		if int64(length) > remaining {
			flush()
			return out, errors.New("TNEF attribute length exceeds remaining input")
		}

		payload := data[pos : pos+int(length)]
		// Advance past payload AND its 2-byte checksum, clamped so a stream that
		// ends exactly at the payload does not push pos past the end.
		pos += int(length) + 2
		if pos > len(data) {
			pos = len(data)
		}

		if level != tnefLevelAttachment && level != tnefLevelMessage {
			// An unknown level means the stream is not shaped the way this
			// decoder understands. Stopping is safer than guessing: the
			// alternative is walking garbage at attacker-chosen offsets.
			flush()
			return out, errors.New("TNEF stream has an unknown level marker")
		}
		// Message-level attributes describe the message (subject, dates, the
		// compressed RTF body) and are out of scope; skipping them by length is
		// the entire handling they need.
		if level != tnefLevelAttachment {
			continue
		}

		switch id {
		case attAttachRendData:
			// A new attachment begins; whatever was open is complete.
			flush()
			if len(out) >= maxTNEFAttachments {
				return out, errors.New("TNEF attachment count cap exceeded")
			}
			cur = &tnefAttachment{}

		case attAttachTitle:
			if cur == nil {
				continue
			}
			if name := decodeTNEFString(payload); name != "" && cur.Filename == "" {
				cur.Filename = name
			}

		case attAttachment:
			if cur == nil {
				continue
			}
			// Retained only for the bounded long-filename scan below. The blob
			// itself is never parsed as MAPI properties.
			if len(payload) <= maxTNEFPropScan {
				pendingProps = payload
			}

		case attAttachData:
			if cur == nil {
				continue
			}
			// Per-attachment cap mirrors the parser's own MaxPartSize, so a TNEF
			// attachment can never be larger than a MIME part would be allowed
			// to be.
			if int64(len(payload)) > maxAttachment {
				// This attachment is refused; the walk continues, because the
				// other attachments in the same winmail.dat are still fine.
				cur.Data = nil
				continue
			}
			if total+int64(len(payload)) > maxTotal {
				flush()
				return out, errors.New("TNEF total extracted size cap exceeded")
			}
			total += int64(len(payload))
			// Copied rather than aliased: the caller retains these bytes as part
			// content for the lifetime of the message, and keeping a slice of the
			// whole winmail.dat payload alive would pin the entire stream in
			// memory behind one small attachment.
			cur.Data = append([]byte(nil), payload...)
		}
	}

	flush()
	return out, nil
}

// decodeTNEFString decodes a TNEF string attribute: NUL-terminated, and in
// practice either 8-bit or UTF-16LE depending on the sending mailer.
//
// The heuristic is the one real decoders use — an odd length, or an absence of
// interleaved NUL bytes, means 8-bit — and it errs toward 8-bit because
// misreading Latin-1 as UTF-16 produces CJK mojibake, while the reverse
// produces recognizably NUL-spaced text that the sanitizer then cleans.
func decodeTNEFString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) > maxTNEFFilename*2 {
		b = b[:maxTNEFFilename*2]
	}

	if len(b) >= 4 && len(b)%2 == 0 && looksUTF16LE(b) {
		u := make([]uint16, 0, len(b)/2)
		for i := 0; i+1 < len(b); i += 2 {
			c := binary.LittleEndian.Uint16(b[i : i+2])
			if c == 0 {
				break
			}
			u = append(u, c)
		}
		return trimTNEFString(string(utf16.Decode(u)))
	}

	if i := indexByteN(b, 0); i >= 0 {
		b = b[:i]
	}
	// 8-bit TNEF strings are effectively windows-1252 in the wild. Reusing the
	// package's charset cascade keeps one decoder rather than two.
	res := decodeCharset(b, "windows-1252", -1)
	return trimTNEFString(string(res.Text))
}

// looksUTF16LE reports whether a buffer has the NUL-in-odd-position pattern of
// UTF-16LE-encoded ASCII, which is what a Windows filename looks like.
func looksUTF16LE(b []byte) bool {
	if len(b) < 4 || len(b)%2 != 0 {
		return false
	}
	var odd, checked int
	for i := 1; i < len(b) && checked < 16; i += 2 {
		checked++
		if b[i] == 0 {
			odd++
		}
	}
	// Every second byte being NUL across the sampled prefix is the signature.
	return checked > 0 && odd == checked
}

// trimTNEFString bounds and cleans a decoded string.
func trimTNEFString(s string) string {
	s = strings.TrimRight(s, "\x00")
	if clean, stripped := sanitizeText(s); stripped {
		s = clean
	}
	s = strings.TrimSpace(s)
	if len(s) > maxTNEFFilename {
		s = s[:maxTNEFFilename]
		// Never leave a truncated multi-byte rune behind: the result goes into a
		// PostgreSQL text column.
		for len(s) > 0 && !isValidUTF8([]byte(s)) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// longFilenameFromProps looks for PR_ATTACH_LONG_FILENAME in a MAPI property
// blob, WITHOUT parsing the blob as a property stream.
//
// The distinction is the whole point. A real MAPI parser must interpret every
// property's type to know its length, so a lie about one property's type
// desynchronizes it from the stream and every subsequent read is at an
// attacker-chosen offset. This instead scans for one 4-byte tag pattern at
// aligned positions and reads a length-prefixed string after it, validating that
// length against the remaining bytes exactly as the outer walk does. A wrong
// guess costs a wrong filename, never a wrong read.
func longFilenameFromProps(props []byte) string {
	if len(props) < 12 || len(props) > maxTNEFPropScan {
		return ""
	}

	for _, typ := range []uint32{mapiTypeUnicode, mapiTypeString8} {
		// A MAPI property tag is (id << 16) | type, stored little-endian.
		want := uint32(mapiTagAttachLongFilename)<<16 | typ

		for i := 0; i+12 <= len(props); i += 4 {
			if binary.LittleEndian.Uint32(props[i:i+4]) != want {
				continue
			}
			// Layout after the tag for a variable-length property: a count of
			// values (4 bytes), then for each value a byte length (4 bytes)
			// followed by the bytes themselves.
			count := binary.LittleEndian.Uint32(props[i+4 : i+8])
			if count != 1 {
				// Multi-valued or zero-valued: not the single filename shape
				// this scan understands.
				continue
			}
			n := binary.LittleEndian.Uint32(props[i+8 : i+12])
			start := i + 12
			// Same int64 bounds discipline as the outer walk.
			if int64(n) > int64(len(props)-start) || n == 0 {
				continue
			}
			if n > maxTNEFFilename*2 {
				continue
			}
			if name := decodeTNEFString(props[start : start+int(n)]); name != "" {
				return name
			}
		}
	}
	return ""
}

// sanitizeTNEFFilename makes an extracted name safe to present.
//
// Unlike Part.Filename from a MIME header — which the parser deliberately leaves
// raw so that a traversal attempt survives as evidence (corpus structural-013) —
// a TNEF filename is SYNTHESIZED by this package into a part that never existed
// in the MIME tree. There is no original to preserve, so the safe form is the
// only form, and producing a name containing a separator would be inventing the
// hazard rather than reporting one.
func sanitizeTNEFFilename(name string, index int) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = path.Base(name)
	name = strings.TrimSpace(strings.Trim(name, "."))
	// Control characters have no business in a filename and are how a name gets
	// a newline into a Content-Disposition header downstream.
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "attachment-" + itoa(index)
	}
	return name
}

// tnefMediaType guesses a media type from the extracted filename.
//
// Extension-based, via the standard library's table, because the alternative —
// content sniffing — would have this package deciding that a file IS an
// executable or IS HTML, which is a security judgment belonging to the layer
// that serves the bytes (internal/jmaphttp already sniffs and sets nosniff on
// what it serves). An unknown extension yields application/octet-stream, which
// is both the honest answer and the safe one.
func tnefMediaType(filename string) string {
	ext := path.Ext(strings.ToLower(filename))
	if ext == "" {
		return "application/octet-stream"
	}
	mt := mime.TypeByExtension(ext)
	if mt == "" {
		return "application/octet-stream"
	}
	// TypeByExtension returns parameters ("text/html; charset=utf-8"); the Part
	// contract wants the bare type/subtype.
	if base, _, err := mime.ParseMediaType(mt); err == nil {
		return strings.ToLower(base)
	}
	if i := strings.IndexByte(mt, ';'); i > 0 {
		return strings.ToLower(strings.TrimSpace(mt[:i]))
	}
	return strings.ToLower(mt)
}

// extractTNEFParts is the cascade's TNEF stage, run after a parse succeeds.
//
// It appends one synthesized Part per recovered attachment, parented to the
// winmail.dat part that carried them, and leaves that part in place as an
// attachment of its own. Keeping the original is cheap honesty: a client that
// wants the Outlook original still has it, and if this decoder ever gets an
// attachment wrong the user is not left without the bytes to recover it
// manually.
//
// Running here rather than inside the walkers is what makes it work for BOTH
// cascade layers at once: go-message and enmime produce the same flattened Part
// slice, so one implementation serves both, and neither library's tree-building
// is touched. It also means the synthesized parts land in the same Part.Index
// space that internal/jmap/mail's per-part blobIds address, so they become
// downloadable through the existing path with no change there — the blob server
// re-parses the raw message and gets the identical indices by construction.
func extractTNEFParts(m *ParsedMessage, limits Limits) {
	if m == nil || m.Status == StatusFailed || len(m.Parts) == 0 {
		return
	}

	// Snapshot the count first: parts appended by this loop are never themselves
	// candidates, which is what guarantees termination.
	original := len(m.Parts)

	for i := 0; i < original; i++ {
		if len(m.Parts) >= limits.MaxParts {
			return
		}
		p := m.Parts[i]
		if p.IsMultipart || p.IsRFC822 || len(p.Content) == 0 || !isTNEFPart(p) {
			continue
		}

		attachments, err := decodeTNEF(p.Content, limits.MaxPartSize, limits.MaxPartSize)
		if err != nil && len(attachments) == 0 {
			// Extraction failed. The part stays exactly what it was — an opaque
			// winmail.dat attachment — which is the baseline every other mail
			// client gives the user, so nothing is lost relative to not having
			// tried. Not-TNEF is silent: a mislabeled part is the sender's
			// business, not a defect in this message's parse.
			if !errors.Is(err, errNotTNEF) {
				m.addDefect(Defect{
					Code:       DefectTNEFUndecodable,
					Part:       i,
					Detail:     "winmail.dat left opaque: " + err.Error(),
					CorpusCase: "real-world-001 (D-6)",
				})
				m.downgrade(StatusPartial)
			}
			continue
		}
		if len(attachments) == 0 {
			// A well-formed TNEF stream that simply carries no files (Outlook
			// emits these for formatting alone). Nothing to add and nothing
			// wrong: not a defect.
			continue
		}

		for _, att := range attachments {
			if len(m.Parts) >= limits.MaxParts {
				m.addDefect(Defect{
					Code:       DefectPartCapExceeded,
					Part:       i,
					Detail:     "TNEF extraction stopped at the part cap",
					CorpusCase: "D-6",
				})
				m.downgrade(StatusPartial)
				return
			}

			idx := len(m.Parts)
			name := sanitizeTNEFFilename(att.Filename, idx)
			m.Parts = append(m.Parts, Part{
				Index:     idx,
				Parent:    i,
				Depth:     p.Depth + 1,
				MediaType: tnefMediaType(name),
				Params:    map[string]string{"name": name},
				Headers: map[string][]string{
					"Content-Type":        {tnefMediaType(name) + `; name="` + name + `"`},
					"Content-Disposition": {`attachment; filename="` + name + `"`},
				},
				Disposition:  "attachment",
				Filename:     name,
				IsAttachment: true,
				Content:      att.Data,
				Size:         len(att.Data),
			})
		}

		m.addDefect(Defect{
			Code:       DefectTNEFExtracted,
			Part:       i,
			Detail:     "extracted " + itoa(len(attachments)) + " attachment(s) from winmail.dat",
			CorpusCase: "real-world-001 (D-6)",
		})
		if err != nil {
			// Some attachments were recovered before the stream went bad. The
			// user gets those, and the message is marked partial because the
			// rest are genuinely lost.
			m.addDefect(Defect{
				Code:       DefectTNEFUndecodable,
				Part:       i,
				Detail:     "TNEF stream ended early: " + err.Error(),
				CorpusCase: "real-world-001 (D-6)",
			})
			m.downgrade(StatusPartial)
		}
	}
}

// indexByteN is bytes.IndexByte without importing bytes into this file's
// dependency surface for one call.
func indexByteN(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
