package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Email/set create — RFC 8621 §4.6 over the assembler in mime.go. The product
// shape is the draft (mailboxIds = {drafts}, keywords = {$draft, $seen}), the
// prerequisite of composing at all; the machinery is general enough that a
// create into any single mailbox works, because §4.6 does not scope creation
// to Drafts and neither should the server.
//
// # What a creation object may carry
//
// The §4.6 rule is exact: "The server MUST reject an invalid combination or
// an attempt to set a server-set property with an invalidProperties SetError."
// This implementation accepts:
//
//   - mailboxIds (required; exactly ONE — the same IMAP-folder constraint
//     Email/set update enforces, same error text) and keywords,
//   - the sender/recipient address properties, subject, sentAt,
//   - messageId, inReplyTo, references (client-threaded replies),
//   - bodyValues + textBody/htmlBody (at most one part each — the shape every
//     composing client produces; a multi-segment body list is refused loudly
//     rather than concatenated wrongly) + attachments,
//   - bodyStructure as the explicit alternative to the convenience trio,
//   - header:{Name} / header:{Name}:asText for headers this server does not
//     model (User-Agent, X-*), refused for the ones it does — a client that
//     sets both "subject" and "header:Subject" is asking for a conflict §4.6
//     tells the server to reject.
//
// receivedAt is REFUSED: §4.1.1 types it server-set on the wire path this
// server implements ("immutable; server-set ... the time at which the server
// received it" — the import use-case that sets it is not this epic), and the
// reflection stores the APPEND's own INTERNALDATE, which is the truth.

// EmailCreator is the executor seam for creates: assembled bytes in, the
// reflected message out. The sync engine's write executor implements it
// (ApplyAppend); write_adapter.go is the only file that knows.
type EmailCreator interface {
	CreateMessage(ctx context.Context, accountID, mailboxID int64, raw []byte, flags []string) (CreatedEmail, error)
}

// CreatedEmail is the reflected result of a create.
type CreatedEmail struct {
	ID       int64
	ThreadID int64
	// BlobID is the raw bytes' sha256 hex — the same content address every
	// other blobId in this server is.
	BlobID string
	Size   uint64
}

// ErrCreateUnavailable maps to the refusal Email/set create answered before
// W3: the executor seam is not wired on this deployment.
var ErrCreateUnavailable = errors.New("mail: message creation is not wired on this server")

// maxAttachmentsBytes is the maxSizeAttachmentsPerEmail this server
// advertises (session.go mailAccountCapability: 25 MB, mirroring the Mailcow
// message ceiling the submission path inherits). Declared == applied: this
// constant is asserted against the advertised value by a test.
const maxAttachmentsBytes = 25_000_000

// applyEmailCreate creates one Email. On success it returns the §5.3 created
// object (the server-set properties) and the new wire id.
func (d *Deps) applyEmailCreate(ctx context.Context, accountID int64, accountEmail string, raw json.RawMessage) (map[string]any, string, *setError) {
	if d.Creator == nil {
		return nil, "", &setError{
			Type:        setErrServerUnavailable,
			Description: "Email/set create is not available on this deployment",
		}
	}

	spec, serr := interpretEmailCreate(ctx, raw)
	if serr != nil {
		return nil, "", serr
	}

	// The keyword ceiling (A6/V1), against the target mailbox — the same
	// check, via the same budget, as an update's (email_set.go).
	if serr := d.checkKeywordCeilingNames(ctx, accountID, spec.mailboxID, spec.keywords); serr != nil {
		return nil, "", serr
	}

	root, serr := d.materializeBody(ctx, accountID, spec)
	if serr != nil {
		return nil, "", serr
	}

	asm, err := newAssembler(nil, nil)
	if err != nil {
		return nil, "", &setError{Type: setErrServerFail, Description: "assembling the message failed"}
	}
	assembled, err := asm.assemble(spec.headers, root, accountEmail)
	if err != nil {
		return nil, "", &setError{Type: setErrServerFail, Description: "assembling the message failed"}
	}
	if int64(len(assembled.raw)) > maxAttachmentsBytes*2 {
		// A defense-in-depth ceiling on the whole assembled message; the
		// per-attachment budget below is the advertised limit.
		return nil, "", &setError{Type: setErrTooLarge,
			Description: "the assembled message exceeds this server's size ceiling"}
	}

	created, err := d.Creator.CreateMessage(ctx, accountID, spec.mailboxID, assembled.raw, spec.keywords)
	if err != nil {
		return nil, "", createSetError(err)
	}

	wire := EncodeEmailID(created.ID)
	out := map[string]any{
		// §5.3: the created map carries "any properties of the created
		// objects that were not sent by the client. This includes all
		// server-set properties".
		"id":       wire,
		"blobId":   created.BlobID,
		"threadId": EncodeThreadID(created.ThreadID),
		"size":     created.Size,
	}
	if spec.headers.messageID == "" {
		// The server minted the Message-ID; the client learns it here rather
		// than by refetching.
		out["messageId"] = []string{assembled.messageID}
	}
	return out, wire, nil
}

// createSetError maps an EmailCreator failure onto §5.3 vocabulary.
func createSetError(err error) *setError {
	switch {
	case errors.Is(err, ErrNotFound):
		// The target mailbox vanished between validation and the append.
		return &setError{Type: setErrNotFound}
	case errors.Is(err, ErrCreateUnavailable):
		return &setError{Type: setErrServerUnavailable,
			Description: "message creation is not available on this deployment"}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &setError{Type: setErrServerUnavailable,
			Description: "creating the message: request canceled or timed out; retry"}
	default:
		return &setError{Type: setErrServerFail, Description: "creating the message failed"}
	}
}

// setErrTooLarge is the §5.3 standard SetError for a record exceeding a
// server limit ("tooLarge: The record ... is too big").
const setErrTooLarge = "tooLarge"

// ---------------------------------------------------------------------------
// interpretation
// ---------------------------------------------------------------------------

// emailCreate is a creation object after interpretation, before blob loads.
type emailCreate struct {
	mailboxID int64
	keywords  []string // writer vocabulary, via imapNameForKeyword

	headers emailHeaders

	bodyValues  map[string]string
	textBody    *createPartRef
	htmlBody    *createPartRef
	attachments []createPartRef

	structure *createStructureNode
}

// createPartRef is one §4.1.4 EmailBodyPart reference in a creation object.
type createPartRef struct {
	partID      string
	blobID      string
	mediaType   string
	name        string
	disposition string
	cid         string
}

// createStructureNode is one bodyStructure node.
type createStructureNode struct {
	createPartRef
	subParts []*createStructureNode
}

// The properties this server models as typed Email properties; their
// header:Name spellings are refused to keep one source of truth per header.
var modeledHeaders = map[string]bool{
	"from": true, "to": true, "cc": true, "bcc": true, "sender": true,
	"reply-to": true, "subject": true, "date": true, "message-id": true,
	"in-reply-to": true, "references": true, "mime-version": true,
	"content-type": true, "content-transfer-encoding": true,
}

// interpretEmailCreate validates the creation object.
func interpretEmailCreate(ctx context.Context, raw json.RawMessage) (*emailCreate, *setError) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, &setError{Type: setErrInvalidProperties,
			Description: "a create must be an Email object (RFC 8621 §4.6)"}
	}

	spec := &emailCreate{bodyValues: map[string]string{}}
	var badProps []string
	bad := func(p string) { badProps = append(badProps, p) }

	for key, val := range obj {
		switch key {
		case "mailboxIds":
			ids, serr := parseCreateMailboxIDs(ctx, val)
			if serr != nil {
				return nil, serr
			}
			if len(ids) != 1 {
				return nil, &setError{Type: setErrInvalidProperties, Properties: []string{"mailboxIds"},
					Description: "a message is created into exactly one mailbox (IMAP folder semantics, ADR-001 mapping); " +
						"labels are keywords, not extra mailboxes"}
			}
			spec.mailboxID = ids[0]

		case "keywords":
			set, serr := parseKeywordSet(val)
			if serr != nil {
				return nil, serr
			}
			spec.keywords = set

		case "from":
			spec.headers.from = decodeCreateAddresses(val, key, bad)
		case "sender":
			spec.headers.sender = decodeCreateAddresses(val, key, bad)
		case "replyTo":
			spec.headers.replyTo = decodeCreateAddresses(val, key, bad)
		case "to":
			spec.headers.to = decodeCreateAddresses(val, key, bad)
		case "cc":
			spec.headers.cc = decodeCreateAddresses(val, key, bad)
		case "bcc":
			spec.headers.bcc = decodeCreateAddresses(val, key, bad)

		case "subject":
			if err := json.Unmarshal(val, &spec.headers.subject); err != nil {
				bad(key)
			}

		case "sentAt":
			var s string
			if err := json.Unmarshal(val, &s); err != nil {
				bad(key)
				continue
			}
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				bad(key)
				continue
			}
			spec.headers.sentAt = t

		case "messageId":
			ids, ok := decodeStringList(val)
			if !ok || len(ids) > 1 {
				// §4.1.1 types it String[]|null; RFC 5322 §3.6.4 permits one
				// msg-id in Message-ID, so a multi-valued set cannot be
				// rendered honestly.
				bad(key)
				continue
			}
			if len(ids) == 1 {
				spec.headers.messageID = strings.Trim(strings.TrimSpace(ids[0]), "<>")
			}

		case "inReplyTo":
			ids, ok := decodeStringList(val)
			if !ok {
				bad(key)
				continue
			}
			spec.headers.inReplyTo = ids

		case "references":
			ids, ok := decodeStringList(val)
			if !ok {
				bad(key)
				continue
			}
			spec.headers.references = ids

		case "bodyValues":
			var values map[string]struct {
				Value string `json:"value"`
			}
			if err := json.Unmarshal(val, &values); err != nil || values == nil {
				bad(key)
				continue
			}
			for id, v := range values {
				spec.bodyValues[id] = v.Value
			}

		case "textBody":
			spec.textBody = decodeSingleBodyList(val, key, "text/plain", bad)
		case "htmlBody":
			spec.htmlBody = decodeSingleBodyList(val, key, "text/html", bad)

		case "attachments":
			var parts []json.RawMessage
			if err := json.Unmarshal(val, &parts); err != nil {
				bad(key)
				continue
			}
			for i, p := range parts {
				ref, ok := decodePartRef(p)
				if !ok || ref.blobID == "" {
					bad(fmt.Sprintf("attachments/%d", i))
					continue
				}
				spec.attachments = append(spec.attachments, ref)
			}

		case "bodyStructure":
			node, ok := decodeStructureNode(val, 0)
			if !ok {
				return nil, &setError{Type: setErrInvalidProperties, Properties: []string{"bodyStructure"},
					Description: "bodyStructure must be a valid EmailBodyPart tree of bounded depth (RFC 8621 §4.1.4)"}
			}
			spec.structure = node

		case "id", "blobId", "threadId", "size", "hasAttachment", "preview", "receivedAt":
			// All server-set (§4.1.1); receivedAt's refusal is argued in the
			// file header.
			bad(key)

		default:
			name, ok := extraHeaderName(key)
			if !ok {
				bad(key)
				continue
			}
			var v string
			if err := json.Unmarshal(val, &v); err != nil {
				bad(key)
				continue
			}
			spec.headers.extra = append(spec.headers.extra, extraHeader{name: name, value: v})
		}
	}

	if len(badProps) > 0 {
		sort.Strings(badProps)
		return nil, &setError{Type: setErrInvalidProperties, Properties: badProps,
			Description: "the creation object carries properties this server cannot honor (RFC 8621 §4.6)"}
	}
	if spec.mailboxID == 0 {
		return nil, &setError{Type: setErrInvalidProperties, Properties: []string{"mailboxIds"},
			Description: "mailboxIds is required on create (RFC 8621 §4.6)"}
	}
	if spec.structure != nil && (spec.textBody != nil || spec.htmlBody != nil || len(spec.attachments) > 0) {
		// §4.6: "If a bodyStructure property is given, the textBody, htmlBody
		// and attachments properties MUST be omitted" (the two describe the
		// same bytes and cannot both win).
		return nil, &setError{Type: setErrInvalidProperties,
			Properties:  []string{"bodyStructure"},
			Description: "bodyStructure excludes textBody/htmlBody/attachments (RFC 8621 §4.6)"}
	}
	// Deterministic header order for the extra headers regardless of JSON map
	// iteration.
	sort.SliceStable(spec.headers.extra, func(i, j int) bool {
		return spec.headers.extra[i].name < spec.headers.extra[j].name
	})
	return spec, nil
}

// extraHeaderName validates a header:{Name}[:asText] property and returns the
// header field name. Modeled headers and non-text forms are refused.
func extraHeaderName(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, "header:")
	if !ok || rest == "" {
		return "", false
	}
	name := rest
	if base, form, hasForm := strings.Cut(rest, ":"); hasForm {
		if form != "asText" && form != "asRaw" {
			return "", false
		}
		name = base
	}
	if name == "" || modeledHeaders[strings.ToLower(name)] {
		return "", false
	}
	// A header field name is printable ASCII except colon (RFC 5322 §2.2).
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c <= ' ' || c > '~' || c == ':' {
			return "", false
		}
	}
	return name, true
}

// parseCreateMailboxIDs is parseMailboxIDSet plus §5.3 creation-id
// references, so "create the folder and file the draft into it" works in one
// request.
func parseCreateMailboxIDs(ctx context.Context, raw json.RawMessage) ([]int64, *setError) {
	var set map[string]json.RawMessage
	if err := json.Unmarshal(raw, &set); err != nil || set == nil {
		return nil, &setError{Type: setErrInvalidProperties, Properties: []string{"mailboxIds"},
			Description: "mailboxIds must be an object of mailboxId: true entries (RFC 8621 §4.1.1)"}
	}
	created := jmap.CreationIDsFromContext(ctx)
	out := make([]int64, 0, len(set))
	var bad []string
	for k, v := range set {
		add, _, valid := boolPatchValue(v)
		wire, resolved := created.Resolve(k)
		if !valid || !add || !resolved {
			bad = append(bad, "mailboxIds/"+k)
			continue
		}
		id, err := DecodeMailboxID(wire)
		if err != nil {
			bad = append(bad, "mailboxIds/"+k)
			continue
		}
		out = append(out, id)
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, &setError{Type: setErrInvalidProperties, Properties: bad,
			Description: "every mailboxIds value must be true and every key a mailbox id this server issued " +
				"(or a #creation-id from this request, RFC 8620 §5.3)"}
	}
	return out, nil
}

func decodeCreateAddresses(raw json.RawMessage, key string, bad func(string)) []EmailAddress {
	var addrs []EmailAddress
	if err := json.Unmarshal(raw, &addrs); err != nil {
		bad(key)
		return nil
	}
	for i, a := range addrs {
		if strings.TrimSpace(a.Email) == "" {
			bad(fmt.Sprintf("%s/%d", key, i))
		}
	}
	return addrs
}

func decodeStringList(raw json.RawMessage) ([]string, bool) {
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, true
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

// decodeSingleBodyList decodes textBody/htmlBody: a list of AT MOST one part
// (see the file header), typed as wantType when a type is given.
func decodeSingleBodyList(raw json.RawMessage, key, wantType string, bad func(string)) *createPartRef {
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		bad(key)
		return nil
	}
	if len(parts) == 0 {
		return nil
	}
	if len(parts) > 1 {
		bad(key)
		return nil
	}
	ref, ok := decodePartRef(parts[0])
	if !ok || ref.partID == "" {
		bad(key + "/0")
		return nil
	}
	if ref.mediaType != "" && ref.mediaType != wantType {
		bad(key + "/0")
		return nil
	}
	ref.mediaType = wantType
	return &ref
}

func decodePartRef(raw json.RawMessage) (createPartRef, bool) {
	var p struct {
		PartID      *string `json:"partId"`
		BlobID      *string `json:"blobId"`
		Type        string  `json:"type"`
		Name        *string `json:"name"`
		Disposition *string `json:"disposition"`
		Cid         *string `json:"cid"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return createPartRef{}, false
	}
	out := createPartRef{mediaType: strings.ToLower(strings.TrimSpace(p.Type))}
	if p.PartID != nil {
		out.partID = *p.PartID
	}
	if p.BlobID != nil {
		out.blobID = *p.BlobID
	}
	if p.Name != nil {
		out.name = *p.Name
	}
	if p.Disposition != nil {
		out.disposition = strings.ToLower(*p.Disposition)
	}
	if p.Cid != nil {
		out.cid = strings.Trim(*p.Cid, "<>")
	}
	// §4.1.4: partId and blobId are mutually exclusive.
	if out.partID != "" && out.blobID != "" {
		return createPartRef{}, false
	}
	return out, true
}

// The bodyStructure bounds: enough for any real composition, small enough
// that a hostile tree cannot make the assembler do meaningful work.
const (
	maxCreateStructureDepth = 10
	maxCreateStructureParts = 50
)

func decodeStructureNode(raw json.RawMessage, depth int) (*createStructureNode, bool) {
	if depth > maxCreateStructureDepth {
		return nil, false
	}
	ref, ok := decodePartRef(raw)
	if !ok {
		return nil, false
	}
	node := &createStructureNode{createPartRef: ref}

	var sub struct {
		SubParts []json.RawMessage `json:"subParts"`
	}
	if err := json.Unmarshal(raw, &sub); err != nil {
		return nil, false
	}
	if len(sub.SubParts) > maxCreateStructureParts {
		return nil, false
	}
	for _, s := range sub.SubParts {
		child, ok := decodeStructureNode(s, depth+1)
		if !ok {
			return nil, false
		}
		node.subParts = append(node.subParts, child)
	}

	isMultipart := strings.HasPrefix(node.mediaType, "multipart/")
	if isMultipart != (len(node.subParts) > 0) {
		// A multipart with no children or a leaf with children is not a tree
		// the assembler can render truthfully.
		return nil, false
	}
	return node, true
}

// ---------------------------------------------------------------------------
// materialization: refs -> parts, blobs loaded, budgets enforced
// ---------------------------------------------------------------------------

// materializeBody turns the creation spec into the tree mime.go renders.
func (d *Deps) materializeBody(ctx context.Context, accountID int64, spec *emailCreate) (*mimePart, *setError) {
	budget := int64(maxAttachmentsBytes)

	if spec.structure != nil {
		return d.materializeNode(ctx, accountID, spec, spec.structure, &budget)
	}

	var text, html *mimePart
	if spec.textBody != nil {
		p, serr := d.materializeLeaf(ctx, accountID, spec, spec.textBody.partID, "", spec.textBody, &budget, "textBody/0")
		if serr != nil {
			return nil, serr
		}
		text = p
	}
	if spec.htmlBody != nil {
		p, serr := d.materializeLeaf(ctx, accountID, spec, spec.htmlBody.partID, "", spec.htmlBody, &budget, "htmlBody/0")
		if serr != nil {
			return nil, serr
		}
		html = p
	}

	var inline, attached []*mimePart
	for i := range spec.attachments {
		ref := &spec.attachments[i]
		p, serr := d.materializeLeaf(ctx, accountID, spec, "", ref.blobID, ref, &budget, fmt.Sprintf("attachments/%d", i))
		if serr != nil {
			return nil, serr
		}
		// An inline, cid-addressed part referenced from the HTML belongs in a
		// multipart/related with it (RFC 2387); everything else rides
		// multipart/mixed.
		if html != nil && p.disposition == "inline" && p.cid != "" {
			inline = append(inline, p)
		} else {
			attached = append(attached, p)
		}
	}

	if html != nil && len(inline) > 0 {
		related := &mimePart{mediaType: "multipart/related", subParts: append([]*mimePart{html}, inline...)}
		html = related
	}

	var body *mimePart
	switch {
	case text != nil && html != nil:
		body = &mimePart{mediaType: "multipart/alternative", subParts: []*mimePart{text, html}}
	case html != nil:
		body = html
	case text != nil:
		body = text
	default:
		// §4.6 permits a bodyless create (an empty draft is a real thing a
		// client saves); an empty text part is its honest rendering.
		body = &mimePart{mediaType: "text/plain"}
	}

	if len(attached) > 0 {
		return &mimePart{mediaType: "multipart/mixed", subParts: append([]*mimePart{body}, attached...)}, nil
	}
	return body, nil
}

// materializeNode renders one bodyStructure node.
func (d *Deps) materializeNode(ctx context.Context, accountID int64, spec *emailCreate, node *createStructureNode, budget *int64) (*mimePart, *setError) {
	if node.isMultipartNode() {
		out := &mimePart{mediaType: node.mediaType}
		for _, sub := range node.subParts {
			p, serr := d.materializeNode(ctx, accountID, spec, sub, budget)
			if serr != nil {
				return nil, serr
			}
			out.subParts = append(out.subParts, p)
		}
		return out, nil
	}
	return d.materializeLeaf(ctx, accountID, spec, node.partID, node.blobID, &node.createPartRef, budget, "bodyStructure")
}

func (n *createStructureNode) isMultipartNode() bool {
	return strings.HasPrefix(n.mediaType, "multipart/")
}

// materializeLeaf builds one leaf part from a partId (bodyValues text) or a
// blobId (uploaded bytes), charging blob bytes against the attachment budget.
func (d *Deps) materializeLeaf(ctx context.Context, accountID int64, spec *emailCreate, partID, blobID string, ref *createPartRef, budget *int64, prop string) (*mimePart, *setError) {
	p := &mimePart{
		mediaType:   ref.mediaType,
		disposition: ref.disposition,
		filename:    ref.name,
		cid:         ref.cid,
	}
	if p.mediaType == "" {
		p.mediaType = "application/octet-stream"
	}

	switch {
	case partID != "":
		value, ok := spec.bodyValues[partID]
		if !ok {
			return nil, &setError{Type: setErrInvalidProperties, Properties: []string{prop},
				Description: fmt.Sprintf("partId %q names no entry in bodyValues (RFC 8621 §4.6)", partID)}
		}
		if !strings.HasPrefix(p.mediaType, "text/") {
			return nil, &setError{Type: setErrInvalidProperties, Properties: []string{prop},
				Description: "a bodyValues-backed part must be a text/* type (RFC 8621 §4.1.4)"}
		}
		p.text = value
		return p, nil

	case blobID != "":
		if d.Blobs == nil {
			return nil, &setError{Type: setErrServerFail, Description: "no blob store is wired for attachments"}
		}
		// OpenBlob enforces account scoping: a blobId this account holds no
		// reference to — uploaded by someone else, or never uploaded — is
		// indistinguishable from one that does not exist (the no-oracle rule).
		rc, size, err := d.Blobs.OpenBlob(ctx, accountID, blobID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				// §4.6 names this exact condition: blobNotFound, with the
				// offending ids.
				return nil, &setError{Type: setErrBlobNotFound, Properties: []string{prop},
					Description: fmt.Sprintf("blobId %q is not available to this account", blobID)}
			}
			return nil, &setError{Type: setErrServerFail, Description: "reading an attachment blob failed"}
		}
		defer func() { _ = rc.Close() }()

		if size > *budget {
			return nil, &setError{Type: setErrTooLarge, Properties: []string{prop},
				Description: fmt.Sprintf("attachments exceed maxSizeAttachmentsPerEmail (%d bytes)", maxAttachmentsBytes)}
		}
		content, err := io.ReadAll(io.LimitReader(rc, *budget+1))
		if err != nil {
			return nil, &setError{Type: setErrServerFail, Description: "reading an attachment blob failed"}
		}
		if int64(len(content)) > *budget {
			return nil, &setError{Type: setErrTooLarge, Properties: []string{prop},
				Description: fmt.Sprintf("attachments exceed maxSizeAttachmentsPerEmail (%d bytes)", maxAttachmentsBytes)}
		}
		*budget -= int64(len(content))

		if strings.HasPrefix(p.mediaType, "text/") {
			p.text = string(content)
		} else {
			p.content = content
		}
		if p.disposition == "" {
			p.disposition = "attachment"
		}
		return p, nil

	default:
		return nil, &setError{Type: setErrInvalidProperties, Properties: []string{prop},
			Description: "a body part needs a partId or a blobId (RFC 8621 §4.1.4)"}
	}
}

// setErrBlobNotFound is RFC 8621 §4.6's blobNotFound SetError type.
const setErrBlobNotFound = "blobNotFound"
