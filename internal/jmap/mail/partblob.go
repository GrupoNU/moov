package mail

import (
	"io"
	"strconv"
	"strings"

	"github.com/GrupoNU/moov/internal/parser"
)

// Per-part blob ids (closes web/README.md gaps 5 and 9).
//
// # The id grammar
//
// A whole message's blobId is the sha256 hex of its raw bytes (L2 §4: "blobId
// = sha256 hex del blob"). A PART's blobId is that hash plus the part's index:
//
//	<64 lowercase hex>-<decimal index>
//
// The two forms cannot collide: a hash is exactly 64 hex characters and the
// part form is strictly longer with a '-' at position 64. The index is the
// same store-assigned part index partID renders, so the id a client reads off
// an EmailBodyPart round-trips through download unchanged.
//
// # What downloading a part id serves
//
// There is no stored per-part blob. Adapter.OpenBlob recognizes the composite
// form, checks the ACCOUNT owns the underlying message blob (the same
// blob_refs rule as a whole-message download — a part grants nothing the
// message did not), re-parses the raw message and serves the part's decoded
// content. The cost is one parse per part download, which is exactly the cost
// Email/get bodyValues already accepted for phase 1 (L2 §5 risk 2), governed
// by the same parser limits.
//
// §4.1.4 defines a part's blob as its octets "after decoding any known
// Content-Transfer-Encoding", which is precisely parser.Part.Content for
// binary parts (attachments, images — the cid: and download consumers this
// exists for). For text/* parts the parser additionally transcodes to UTF-8,
// so the served bytes may differ from the wire's charset encoding; that is a
// deliberate divergence, matches what bodyValues serves for the same part,
// and is strictly more useful to a client than undecodable legacy bytes.

// maxPartIndex bounds the accepted index. The parser's MaxParts cap keeps
// real documents far below this; the bound exists so a crafted id cannot make
// the grammar accept absurd values.
const maxPartIndex = 100000

// partBlobIDFor renders the composite id for one part of a message blob.
func partBlobIDFor(messageBlobID string, index int) string {
	return messageBlobID + "-" + strconv.Itoa(index)
}

// parsePartBlobID recognizes the composite form. ok is false for anything
// else — including a plain message hash, which the caller serves as before.
//
// The index must be in canonical decimal (no leading zeros, no sign): the id
// is compared against what partBlobIDFor produced, and accepting aliases
// ("007") would make one part addressable under many ids for no benefit.
func parsePartBlobID(blobID string) (messageBlobID string, index int, ok bool) {
	const hashLen = 64
	if len(blobID) < hashLen+2 || blobID[hashLen] != '-' {
		return "", 0, false
	}
	hash := blobID[:hashLen]
	for i := 0; i < hashLen; i++ {
		c := hash[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", 0, false
		}
	}
	digits := blobID[hashLen+1:]
	if digits != "0" && strings.HasPrefix(digits, "0") {
		return "", 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 0 || n > maxPartIndex {
		return "", 0, false
	}
	return hash, n, true
}

// extractPartContent parses a raw message and returns the decoded content of
// the part at the given index, or ErrNotFound when the index names no
// servable leaf part.
//
// The index space is the parser's own (Part.Index), which is also what the
// store persisted at sync time and what partID/partBlobID advertised — the
// same parser package produces both, so the advertised index and the
// re-parsed index agree by construction (the identical shared-fate argument
// bodyValues relies on).
func extractPartContent(raw io.Reader, index int, limits parser.Limits) ([]byte, error) {
	parsed := parser.Parse(raw, limits)
	for _, p := range parsed.Parts {
		if p.Index != index {
			continue
		}
		if p.IsMultipart || p.IsRFC822 {
			// Containers advertise no blobId (partBlobID), so a request for
			// one is a fabricated id: not found, indistinguishable from any
			// other id that names nothing.
			return nil, ErrNotFound
		}
		if p.Content == nil {
			// A legitimately empty leaf serves zero bytes.
			return []byte{}, nil
		}
		return p.Content, nil
	}
	return nil, ErrNotFound
}
