package mail

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/parser"
)

// Email/get — RFC 8621 §4.6 over the §4 Email object.

// emailGetRequest extends the standard /get arguments with the body-fetching
// arguments of §4.6.
type emailGetRequest struct {
	getRequest
	BodyProperties      *[]string `json:"bodyProperties"`
	FetchTextBodyValues bool      `json:"fetchTextBodyValues"`
	FetchHTMLBodyValues bool      `json:"fetchHTMLBodyValues"`
	FetchAllBodyValues  bool      `json:"fetchAllBodyValues"`
	MaxBodyValueBytes   uint64    `json:"maxBodyValueBytes"`
}

// defaultEmailProperties is the §4.6 default property list, verbatim and in
// the RFC's order: "If omitted or null, defaults to [ "id", "blobId",
// "threadId", "mailboxIds", "keywords", "size", "receivedAt", "messageId",
// "inReplyTo", "references", "sender", "from", "to", "cc", "bcc", "replyTo",
// "subject", "sentAt", "hasAttachment", "preview", "bodyValues", "textBody",
// "htmlBody", "attachments" ]".
//
// Note what is NOT in it: bodyStructure. A client that wants the full tree
// asks for it, because it is the largest property an Email has and most
// clients render from textBody/htmlBody instead.
var defaultEmailProperties = []string{
	"id", "blobId", "threadId", "mailboxIds", "keywords", "size", "receivedAt",
	"messageId", "inReplyTo", "references", "sender", "from", "to", "cc", "bcc",
	"replyTo", "subject", "sentAt", "hasAttachment", "preview",
	"bodyValues", "textBody", "htmlBody", "attachments",
}

// emailProperties is every property this server implements — the §4 Email
// object's own properties plus the §4.1.4 body lists.
var emailProperties = func() map[string]bool {
	m := map[string]bool{
		// §4.1: metadata.
		"id": true, "blobId": true, "threadId": true, "mailboxIds": true,
		"keywords": true, "size": true, "receivedAt": true,
		// §4.1.3: header fields parsed into convenience properties, plus the
		// raw `headers` list they are derived from.
		"headers":   true,
		"messageId": true, "inReplyTo": true, "references": true,
		"sender": true, "from": true, "to": true, "cc": true, "bcc": true,
		"replyTo": true, "subject": true, "sentAt": true,
		// §4.1.4: body parts.
		"bodyStructure": true, "bodyValues": true, "textBody": true,
		"htmlBody": true, "attachments": true, "hasAttachment": true,
		"preview": true,
	}
	return m
}()

// handleEmailGet implements Email/get.
func (d *Deps) handleEmailGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	var req emailGetRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("arguments did not parse: %v", err)
	}
	// The common checks (caller, accountId, maxObjectsInGet) run against the
	// embedded standard arguments, so there is one implementation of them.
	base, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	req.getRequest = *base

	// §4.6: Email/get does not support ids:null. The RFC's own words in §5.1
	// license the refusal ("if the server does not support the null value, it
	// MUST return a requestTooLarge error"), and for Email it is the only
	// sane answer: "every message in the account" is unbounded by definition.
	// Email/query (J3) is how a client enumerates.
	if req.IDs == nil {
		return nil, jmap.NewMethodError(jmap.CodeRequestTooLarge).
			WithDescription("Email/get requires an explicit ids list; " +
				"use Email/query to enumerate messages")
	}

	if bad := unknownProperties(req.Properties, emailProperties); len(bad) > 0 {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("unknown Email properties: %v", bad)
	}
	if bad := unknownProperties(req.BodyProperties, bodyPartProperties); len(bad) > 0 {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("unknown bodyProperties: %v", bad)
	}

	props, selective := propertySet(req.Properties)
	if !selective {
		props = make(map[string]bool, len(defaultEmailProperties))
		for _, p := range defaultEmailProperties {
			props[p] = true
		}
	}
	bodyProps := defaultBodyProperties
	if req.BodyProperties != nil {
		bodyProps = *req.BodyProperties
	}

	state, err := d.State.EmailState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading email state", err)
	}
	resp := newGetResponse(req.AccountID, state)

	ids, wire, unknown := decodeIDList(*req.IDs, DecodeEmailID)
	resp.NotFound = append(resp.NotFound, unknown...)

	rows, err := d.Emails.EmailsByID(ctx, caller.AccountID, ids)
	if err != nil {
		return nil, serverFail("reading emails", err)
	}
	found := make(map[int64]bool, len(rows))
	for _, r := range rows {
		found[r.ID] = true
	}
	for _, id := range ids {
		if !found[id] {
			resp.NotFound = append(resp.NotFound, wire[id])
		}
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })

	// Whether any body content is needed at all decides if the raw blob is
	// opened. The overwhelmingly common Email/get — a message list refresh —
	// asks for metadata only and must not pay for a blob read per message.
	needBodies := (req.FetchTextBodyValues || req.FetchHTMLBodyValues || req.FetchAllBodyValues) &&
		props["bodyValues"]

	// `headers` (§4.1.3) is served from the same re-parse: the store keeps a
	// fixed field set, not the raw header block, so the blob is the only place
	// the full list in original order exists. It is NOT in the §4.6 default
	// property list, so this cost is only ever paid by a client that asked for
	// it by name — which is exactly the message-open request, one message at a
	// time, and the same request that already fetches bodyValues.
	needHeaders := props["headers"]

	for i := range rows {
		obj, merr := d.renderEmail(ctx, caller.AccountID, rows[i], props, bodyProps, &req, needBodies, needHeaders)
		if merr != nil {
			return nil, merr
		}
		resp.List = append(resp.List, obj)
	}
	return resp, nil
}

// renderEmail builds one Email object.
func (d *Deps) renderEmail(
	ctx context.Context,
	accountID int64,
	row EmailRow,
	props map[string]bool,
	bodyProps []string,
	req *emailGetRequest,
	needBodies bool,
	needHeaders bool,
) (map[string]any, *jmap.MethodError) {
	// §5.1: id is always returned.
	out := map[string]any{"id": EncodeEmailID(row.ID)}

	// The raw message is opened AT MOST ONCE per Email, however many properties
	// need it. headers and bodyValues both come from the same re-parse, so the
	// result is memoized here and shared: opening the blob twice for one object
	// would double the cost L2 §5 risk 2 already flagged as the price of not
	// storing decoded bodies.
	var (
		parsed     *parser.ParsedMessage
		parseTried bool
	)
	rawParse := func() *parser.ParsedMessage {
		if parseTried {
			return parsed
		}
		parseTried = true
		rc, err := d.Emails.RawMessage(ctx, accountID, row.ID)
		if err != nil {
			// ErrNotFound is the torn state the GC grace period exists to
			// prevent (metadata row present, blob gone). Either way the honest
			// answer is "no content available", not a failed batch: the user
			// still gets the headers the store itself holds.
			return nil
		}
		defer func() { _ = rc.Close() }()
		p := parser.Parse(rc, d.ParserLimits)
		parsed = &p
		return parsed
	}

	// ---- §4.1.1 metadata --------------------------------------------------
	if props["blobId"] {
		out["blobId"] = row.BlobID
	}
	if props["threadId"] {
		out["threadId"] = row.ThreadID
	}
	if props["mailboxIds"] {
		// §4.1.1: "The set of Mailbox ids this Email belongs to." A set, so an
		// object with true values.
		ids := make(map[string]bool, len(row.MailboxIDs))
		for _, id := range row.MailboxIDs {
			ids[EncodeMailboxID(id)] = true
		}
		out["mailboxIds"] = ids
	}
	if props["keywords"] {
		out["keywords"] = keywordSet(row.Keywords)
	}
	if props["size"] {
		// §4.1.1: "The size, in octets, of the raw data for the message".
		out["size"] = row.Size
	}
	if props["receivedAt"] {
		// §4.1.1: "The date the Email was received... This is the 'internal
		// date' in IMAP." UTCDate, so RFC 3339 in UTC with a Z.
		out["receivedAt"] = utcDate(row.ReceivedAt)
	}

	// ---- §4.1.3 header convenience properties ----------------------------
	if needHeaders {
		// §4.1.3: "headers: EmailHeader[] — This is a list of all header fields
		// in the message, in the same order they appear in the message."
		out["headers"] = rawHeaderList(rawParse())
	}
	if props["messageId"] {
		out["messageId"] = nilIfEmptyStrings(row.MessageID)
	}
	if props["inReplyTo"] {
		out["inReplyTo"] = nilIfEmptyStrings(row.InReplyTo)
	}
	if props["references"] {
		out["references"] = nilIfEmptyStrings(row.Reference)
	}
	for _, field := range []string{"sender", "from", "to", "cc", "bcc", "replyTo"} {
		if props[field] {
			out[field] = addressList(row.Addresses[field])
		}
	}
	if props["subject"] {
		// §4.1.3 types subject as String|null; an absent Subject header is
		// null, not "". The store cannot distinguish an absent header from an
		// empty one (the column is NOT NULL), so an empty subject is reported
		// as null — the reading a client renders as "(no subject)" either way.
		out["subject"] = nilIfEmpty(row.Subject)
	}
	if props["sentAt"] {
		// §4.1.3: "The date from the Date header" — a Date, and null when the
		// header was absent or unparseable.
		if row.HasSentAt {
			out["sentAt"] = dateWithOffset(row.SentAt)
		} else {
			out["sentAt"] = nil
		}
	}

	// ---- §4.1.4 body ------------------------------------------------------
	needsStructure := props["bodyStructure"] || props["textBody"] ||
		props["htmlBody"] || props["attachments"] || props["bodyValues"]

	if props["hasAttachment"] {
		out["hasAttachment"] = row.HasAttachment
	}
	if props["preview"] {
		out["preview"] = row.Preview
	}

	if !needsStructure {
		return out, nil
	}

	// A message the MIME cascade could not parse has no trustworthy structure
	// (S4: enmime damaged the header block while failing, so partial output
	// from a hard failure is not to be believed). It is served as a minimal
	// Email — metadata and an empty body — with its blobId intact so the user
	// can still download the raw message and read it elsewhere. That is the
	// honest representation L2 §2.4 asks for.
	if row.ParseFailed || len(row.Structure) == 0 {
		if props["bodyStructure"] {
			out["bodyStructure"] = emptyBodyStructure()
		}
		if props["textBody"] {
			out["textBody"] = []any{}
		}
		if props["htmlBody"] {
			out["htmlBody"] = []any{}
		}
		if props["attachments"] {
			out["attachments"] = []any{}
		}
		if props["bodyValues"] {
			out["bodyValues"] = map[string]any{}
		}
		return out, nil
	}

	root := bodyPartTree(row.Structure)
	textBody, htmlBody, attachments := bodyStructureLists(root)

	if props["bodyStructure"] {
		out["bodyStructure"] = renderBodyPart(root, bodyProps, true)
	}
	if props["textBody"] {
		out["textBody"] = renderBodyPartList(textBody, bodyProps)
	}
	if props["htmlBody"] {
		out["htmlBody"] = renderBodyPartList(htmlBody, bodyProps)
	}
	if props["attachments"] {
		out["attachments"] = renderBodyPartList(attachments, bodyProps)
	}

	if !props["bodyValues"] {
		return out, nil
	}
	if !needBodies {
		// §4.6: the fetch*BodyValues arguments default to false, and with none
		// of them set "no body values are returned" — an empty object, not an
		// absent property, since bodyValues was requested.
		out["bodyValues"] = map[string]any{}
		return out, nil
	}

	values, merr := d.bodyValuesFor(rawParse(), root, textBody, htmlBody, req)
	if merr != nil {
		return nil, merr
	}
	out["bodyValues"] = values
	return out, nil
}

// rawHeaderList renders the §4.1.3 `headers` property: every header field in
// the order it appeared, as EmailHeader objects.
//
// A message whose blob is gone or whose MIME cascade failed yields an empty
// list rather than null. §4.1.3 types the property as EmailHeader[] — not
// nullable — so an empty list is the only conformant way to say "this server
// has no headers to give", and it is the same reading the body-part `headers`
// already takes for parts whose raw headers were never persisted.
func rawHeaderList(p *parser.ParsedMessage) []any {
	out := []any{}
	if p == nil {
		return out
	}
	for _, h := range p.Headers.Ordered {
		// §4.1.3: "name: String — The header field name as defined in RFC 5322,
		// with the same capitalization that it has in the message."
		// "value: String — The header field value as defined in RFC 5322."
		out = append(out, map[string]any{"name": h.Name, "value": h.Value})
	}
	return out
}

// bodyValuesFor re-parses the raw message and builds the bodyValues map.
//
// The re-parse is the cost L2 §5 risk 2 accepted for phase 1: bodies are not
// stored as text (internal/sync's encodeStructure keeps content out of the
// database on purpose), so the only source of a part's decoded content is the
// raw blob plus the parser. The mitigation named there — a cache keyed by
// (blobId, parser-version) — is a J-later decision to be taken with numbers
// from real Bulwark usage, not speculatively here.
func (d *Deps) bodyValuesFor(
	parsed *parser.ParsedMessage,
	root *bodyPartNode,
	textBody, htmlBody []*bodyPartNode,
	req *emailGetRequest,
) (map[string]bodyValue, *jmap.MethodError) {
	// §4.6 selects which parts get values:
	//   fetchTextBodyValues — "the text/* parts of textBody"
	//   fetchHTMLBodyValues — "the text/* parts of htmlBody"
	//   fetchAllBodyValues  — "the text/* parts of bodyStructure"
	// Only text/* parts ever get a value: a JPEG has no string form.
	want := make(map[int]bool)
	add := func(nodes []*bodyPartNode) {
		for _, n := range nodes {
			if strings.HasPrefix(n.mediaType(), "text/") {
				want[n.part.Index] = true
			}
		}
	}
	if req.FetchTextBodyValues {
		add(textBody)
	}
	if req.FetchHTMLBodyValues {
		add(htmlBody)
	}
	if req.FetchAllBodyValues {
		add(collectAll(root))
	}
	if len(want) == 0 {
		return map[string]bodyValue{}, nil
	}

	// A nil parse means the raw message could not be opened — the torn state
	// (metadata row present, blob gone) the GC grace period exists to prevent.
	// Serve the message without bodies rather than failing the whole batch: the
	// user sees the headers of a message whose content is gone, which is the
	// truth.
	if parsed == nil {
		return map[string]bodyValue{}, nil
	}

	out := make(map[string]bodyValue, len(want))
	for _, p := range parsed.Parts {
		if !want[p.Index] {
			continue
		}
		// The parser has already transcoded text/* content to UTF-8 and
		// recorded whether it had to guess the charset or stopped decoding
		// early — which is exactly §4.1.4's isEncodingProblem ("malformed
		// sections were found while decoding the charset, or the charset was
		// unknown, or the content-transfer-encoding was unknown").
		problem := p.CharsetGuessed || p.PartiallyDecoded
		out[partID(StructurePart{Index: p.Index})] =
			newBodyValue(string(p.Content), req.MaxBodyValueBytes, problem)
	}
	return out, nil
}

// renderBodyPartList renders a list of parts, always as an array.
func renderBodyPartList(nodes []*bodyPartNode, bodyProps []string) []any {
	out := make([]any, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, renderBodyPart(n, bodyProps, false))
	}
	return out
}

// renderBodyPart renders one EmailBodyPart (§4.1.4).
//
// withSubParts is true only for bodyStructure, where the nesting is the point.
// The flat lists (textBody/htmlBody/attachments) are defined by §4.1.4 as
// lists of leaf parts, so rendering subParts inside them would duplicate the
// tree into every list.
func renderBodyPart(n *bodyPartNode, bodyProps []string, withSubParts bool) map[string]any {
	if n == nil {
		return nil
	}
	// §4.1.4: "partId ... is null if, and only if, the part is a container for
	// other parts" — a multipart has no content of its own to fetch.
	out := map[string]any{}
	want := make(map[string]bool, len(bodyProps))
	for _, p := range bodyProps {
		want[p] = true
	}

	if want["partId"] {
		if n.isMultipart() {
			out["partId"] = nil
		} else {
			out["partId"] = partID(n.part)
		}
	}
	if want["blobId"] {
		out["blobId"] = partBlobID()
	}
	if want["size"] {
		// §4.1.4: "The size, in octets, of the raw data after content transfer
		// decoding". A container has no raw data of its own.
		if n.isMultipart() {
			out["size"] = 0
		} else {
			out["size"] = n.part.Size
		}
	}
	if want["name"] {
		out["name"] = nilIfEmpty(n.part.Filename)
	}
	if want["type"] {
		// §4.1.4: "The value of the Content-Type header field of the part, if
		// present; otherwise, the implicit type as per the MIME standard
		// (text/plain or message/rfc822 if inside a multipart/digest)."
		out["type"] = n.mediaType()
	}
	if want["charset"] {
		// §4.1.4: "The value of the charset parameter of the Content-Type
		// header field, if present, or null if the header field is present but
		// not of type text/*."
		if strings.HasPrefix(n.mediaType(), "text/") {
			out["charset"] = nilIfEmpty(n.part.Charset)
		} else {
			out["charset"] = nil
		}
	}
	if want["disposition"] {
		out["disposition"] = nilIfEmpty(n.part.Disposition)
	}
	if want["cid"] {
		out["cid"] = nilIfEmpty(n.part.ContentID)
	}
	if want["language"] {
		// Content-Language is not persisted by the store's part document. Null
		// is the RFC's value for absent, and inventing one would be worse than
		// admitting it is unknown. Recorded as a store gap in the J2 report.
		out["language"] = nil
	}
	if want["location"] {
		// Same as language: Content-Location is not persisted.
		out["location"] = nil
	}
	if want["headers"] {
		// The per-part raw headers are not persisted either (encodeStructure
		// stores a fixed field set). An empty list is the honest answer for
		// "this server has no headers to give for this part".
		out["headers"] = []any{}
	}
	if withSubParts || want["subParts"] {
		// §4.1.4: "subParts: ... A list of the sub-parts... or null if not of
		// type multipart/*."
		if n.isMultipart() {
			subs := make([]any, 0, len(n.children))
			for _, c := range n.children {
				subs = append(subs, renderBodyPart(c, bodyProps, withSubParts))
			}
			out["subParts"] = subs
		} else {
			out["subParts"] = nil
		}
	}
	return out
}

// emptyBodyStructure is the bodyStructure of a message with no usable
// structure: a single empty text/plain part, which is the minimum shape
// §4.1.4 permits while remaining a valid EmailBodyPart.
func emptyBodyStructure() map[string]any {
	return map[string]any{
		"partId": nil, "blobId": nil, "size": 0, "name": nil,
		"type": "text/plain", "charset": nil, "disposition": nil,
		"cid": nil, "language": nil, "location": nil, "subParts": nil,
	}
}

// addressList renders an EmailAddress[] property (§4.1.2), null when the
// header was absent.
func addressList(addrs []EmailAddress) any {
	if len(addrs) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(addrs))
	for _, a := range addrs {
		// §4.1.2: name is String|null; email is String.
		out = append(out, map[string]any{
			"name":  nilIfEmpty(a.Name),
			"email": a.Email,
		})
	}
	return out
}

// utcDate renders a UTCDate (RFC 8620 §1.4): RFC 3339 in UTC, "Z" suffix.
func utcDate(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// dateWithOffset renders a Date (RFC 8620 §1.4), which — unlike UTCDate —
// keeps the sender's UTC offset. sentAt is a Date precisely so a client can
// show "sent at 9am their time".
func dateWithOffset(t time.Time) string {
	return t.Format(time.RFC3339)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nilIfEmptyStrings renders a String[]|null property: null when absent, which
// is what §4.1.3 specifies for messageId/inReplyTo/references.
func nilIfEmptyStrings(v []string) any {
	if len(v) == 0 {
		return nil
	}
	return v
}
