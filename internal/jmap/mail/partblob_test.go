package mail

import (
	"errors"
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/parser"
)

// Tests for the per-part blob ids (partblob.go) — the closure of web/README.md
// gaps 5 and 9: every leaf body part now advertises a downloadable blobId.

// multipartFixture is a small multipart/mixed message: a text body and a
// binary attachment. CRLF line endings, as on the wire.
const multipartFixture = "From: alice@example.com\r\n" +
	"To: bob@example.com\r\n" +
	"Subject: attached\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n" +
	"\r\n" +
	"--BOUND\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"see attachment\r\n" +
	"--BOUND\r\n" +
	"Content-Type: application/octet-stream\r\n" +
	"Content-Disposition: attachment; filename=\"data.bin\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"aGVsbG8gYmluYXJ5\r\n" + // "hello binary"
	"--BOUND--\r\n"

func TestPartBlobIDGrammar(t *testing.T) {
	hash := strings.Repeat("ab", 32) // 64 hex chars

	for _, tc := range []struct {
		id    string
		hash  string
		index int
		ok    bool
	}{
		{hash + "-0", hash, 0, true},
		{hash + "-2", hash, 2, true},
		{hash + "-137", hash, 137, true},
		// A plain message hash is NOT the part form.
		{hash, "", 0, false},
		// Non-canonical decimals alias one part under many ids; refused.
		{hash + "-00", "", 0, false},
		{hash + "-07", "", 0, false},
		{hash + "--1", "", 0, false},
		{hash + "-", "", 0, false},
		{hash + "-x", "", 0, false},
		{hash + "-1000001", "", 0, false},
		// A malformed or truncated hash half.
		{strings.Repeat("g", 64) + "-1", "", 0, false},
		{strings.Repeat("AB", 32) + "-1", "", 0, false}, // uppercase is not canonical sha256 hex
		{"abc-1", "", 0, false},
		{"", "", 0, false},
	} {
		gotHash, gotIndex, gotOK := parsePartBlobID(tc.id)
		if gotOK != tc.ok || gotHash != tc.hash || (tc.ok && gotIndex != tc.index) {
			t.Errorf("parsePartBlobID(%q) = (%q, %d, %v), want (%q, %d, %v)",
				tc.id, gotHash, gotIndex, gotOK, tc.hash, tc.index, tc.ok)
		}
	}
}

func TestPartBlobIDRoundTrip(t *testing.T) {
	hash := strings.Repeat("0f", 32)
	for _, index := range []int{0, 1, 42, maxPartIndex} {
		id := partBlobIDFor(hash, index)
		gotHash, gotIndex, ok := parsePartBlobID(id)
		if !ok || gotHash != hash || gotIndex != index {
			t.Errorf("round trip of index %d failed: %q -> (%q, %d, %v)",
				index, id, gotHash, gotIndex, ok)
		}
	}
}

func TestPartBlobIDNullForContainersAndRFC822(t *testing.T) {
	hash := strings.Repeat("aa", 32)

	if got := partBlobID(hash, StructurePart{Index: 1, MediaType: "text/plain"}); got != partBlobIDFor(hash, 1) {
		t.Errorf("leaf part blobId = %v", got)
	}
	if got := partBlobID(hash, StructurePart{Index: 0, MediaType: "multipart/mixed", IsMultipart: true}); got != nil {
		t.Errorf("multipart container blobId = %v, want nil", got)
	}
	if got := partBlobID(hash, StructurePart{Index: 2, MediaType: "message/rfc822", IsRFC822: true}); got != nil {
		t.Errorf("rfc822 part blobId = %v, want nil (its raw bytes are not retained)", got)
	}
	if got := partBlobID("", StructurePart{Index: 1, MediaType: "text/plain"}); got != nil {
		t.Errorf("blobId with no message blob = %v, want nil", got)
	}
}

func TestExtractPartContent(t *testing.T) {
	limits := parser.DefaultLimits()

	// Find the real indices the parser assigns, so the test asserts against
	// the SAME index space Email/get advertises.
	parsed := parser.Parse(strings.NewReader(multipartFixture), limits)
	var textIdx, binIdx, rootIdx int
	textIdx, binIdx, rootIdx = -1, -1, -1
	for _, p := range parsed.Parts {
		switch {
		case p.IsMultipart:
			rootIdx = p.Index
		case p.MediaType == "text/plain":
			textIdx = p.Index
		case p.MediaType == "application/octet-stream":
			binIdx = p.Index
		}
	}
	if textIdx < 0 || binIdx < 0 || rootIdx < 0 {
		t.Fatalf("fixture did not parse into the expected parts: %+v", parsed.Parts)
	}

	// The attachment: base64 undone, exact octets — §4.1.4's "raw octets of
	// the contents of the part... after decoding any known
	// Content-Transfer-Encoding".
	content, err := extractPartContent(strings.NewReader(multipartFixture), binIdx, limits)
	if err != nil {
		t.Fatalf("attachment part: %v", err)
	}
	if string(content) != "hello binary" {
		t.Errorf("attachment content = %q", content)
	}

	// The text part serves its decoded content too.
	content, err = extractPartContent(strings.NewReader(multipartFixture), textIdx, limits)
	if err != nil {
		t.Fatalf("text part: %v", err)
	}
	if !strings.Contains(string(content), "see attachment") {
		t.Errorf("text content = %q", content)
	}

	// The container advertises no blobId, so asking for it is a fabricated
	// id: ErrNotFound, indistinguishable from any other nothing.
	if _, err := extractPartContent(strings.NewReader(multipartFixture), rootIdx, limits); !errors.Is(err, ErrNotFound) {
		t.Errorf("container part error = %v, want ErrNotFound", err)
	}

	// An index the message does not have.
	if _, err := extractPartContent(strings.NewReader(multipartFixture), 99, limits); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing part error = %v, want ErrNotFound", err)
	}
}

// TestEmailGetAdvertisesDownloadablePartBlobIDs pins the contract between
// what Email/get renders and what download parses: every leaf part's blobId
// round-trips through parsePartBlobID onto the message's own blob, and its
// content is extractable at that index — i.e. the advertised id is served,
// never a 404 button.
func TestEmailGetAdvertisesDownloadablePartBlobIDs(t *testing.T) {
	limits := parser.DefaultLimits()
	parsed := parser.Parse(strings.NewReader(multipartFixture), limits)

	// Build the stored structure the sync engine would persist, from the same
	// parse the download path will redo.
	structure := make([]StructurePart, 0, len(parsed.Parts))
	for _, p := range parsed.Parts {
		structure = append(structure, StructurePart{
			Index: p.Index, Parent: p.Parent, Depth: p.Depth,
			MediaType: p.MediaType, Disposition: p.Disposition,
			Filename: p.Filename, Size: p.Size,
			IsAttachment: p.IsAttachment, IsMultipart: p.IsMultipart, IsRFC822: p.IsRFC822,
		})
	}

	f := newFakeReaders()
	row := sampleEmail(1, "attached")
	row.Structure = structure
	f.emails[testAccountID] = []EmailRow{row}
	f.raw[1] = []byte(multipartFixture)
	d := f.deps()

	got := callGet(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeEmailID(1)+`"],`+
			`"properties":["id","blobId","bodyStructure","attachments"]}`)

	e := firstObject(t, got, 0)
	bs := object(t, e, "bodyStructure")
	if bs["blobId"] != nil {
		t.Errorf("the multipart root's blobId = %v, want null (§4.1.4)", bs["blobId"])
	}

	subs, ok := bs["subParts"].([]any)
	if !ok || len(subs) != 2 {
		t.Fatalf("subParts = %#v, want the two leaves", bs["subParts"])
	}
	for i, sub := range subs {
		part, ok := sub.(map[string]any)
		if !ok {
			t.Fatalf("subParts[%d] is %T", i, sub)
		}
		blobID, ok := part["blobId"].(string)
		if !ok || blobID == "" {
			t.Fatalf("leaf %d blobId = %#v, want a composite id", i, part["blobId"])
		}
		hash, index, ok := parsePartBlobID(blobID)
		if !ok {
			t.Fatalf("advertised blobId %q does not parse", blobID)
		}
		if hash != row.BlobID {
			t.Errorf("blobId %q names hash %q, want the message blob %q", blobID, hash, row.BlobID)
		}
		content, err := extractPartContent(strings.NewReader(multipartFixture), index, parser.DefaultLimits())
		if err != nil {
			t.Errorf("advertised blobId %q is not servable: %v", blobID, err)
		}
		if len(content) == 0 {
			t.Errorf("advertised blobId %q served no content", blobID)
		}
	}

	// The attachments list carries the same servable id.
	atts, ok := e["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("attachments = %#v, want exactly the binary part", e["attachments"])
	}
	att, _ := atts[0].(map[string]any)
	if blobID, _ := att["blobId"].(string); blobID == "" {
		t.Error("the attachment's blobId is null; forwarding and per-part download stay broken")
	}
}

// Guard: an empty part reader still terminates and answers ErrNotFound
// rather than panicking or hanging (the parser's cascade floor).
func TestExtractPartContentOnGarbage(t *testing.T) {
	for _, raw := range []string{"", "not mime at all", "\x00\x01\x02"} {
		if _, err := extractPartContent(strings.NewReader(raw), 5, parser.DefaultLimits()); err == nil {
			// Index 5 exists in nothing this small; whatever the parser made
			// of the bytes, the answer must be a refusal, not content.
			t.Errorf("garbage %q served part 5", raw)
		}
	}
}
