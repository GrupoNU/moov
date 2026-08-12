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

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/store"
)

// The store-backed implementation of the reader contracts.
//
// This file is the ONLY place in the JMAP surface that knows the store and the
// blob layer exist. Everything above it — the handlers, the rendering, the
// tests — works against the interfaces in contracts.go.

// Adapter implements MailboxReader, EmailReader, ThreadReader, BlobReader and
// StateReader over the real store.
type Adapter struct {
	store *store.Store
	blobs *blob.Store
}

// NewAdapter builds the store-backed readers.
func NewAdapter(st *store.Store, blobs *blob.Store) (*Adapter, error) {
	if st == nil {
		return nil, errors.New("mail: a store is required")
	}
	if blobs == nil {
		return nil, errors.New("mail: a blob store is required")
	}
	return &Adapter{store: st, blobs: blobs}, nil
}

// NewDeps builds the full dependency set for RegisterGetMethods over a real
// store.
func NewDeps(st *store.Store, blobs *blob.Store, limits jmap.Limits) (*Deps, error) {
	a, err := NewAdapter(st, blobs)
	if err != nil {
		return nil, err
	}
	return &Deps{
		Mailboxes: a,
		Emails:    a,
		Threads:   a,
		Blobs:     a,
		State:     a,
		// J3's readers are the same adapter: Search goes through the store's
		// typed repertoire and Changes through the sync_log/message_state feed.
		Search:  a,
		Changes: a,
		Limits:  limits,
	}, nil
}

// ---------------------------------------------------------------------------
// MailboxReader
// ---------------------------------------------------------------------------

// Mailboxes returns every mailbox of the account with its four counts.
func (a *Adapter) Mailboxes(ctx context.Context, accountID int64) ([]MailboxRow, error) {
	rows, err := a.store.ListMailboxes(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return a.mailboxRows(ctx, accountID, rows)
}

// MailboxesByID returns the requested mailboxes, silently dropping ids that do
// not exist or belong to another account.
//
// It filters the full list rather than issuing a query per id: an account has
// tens of mailboxes, so one indexed read plus a filter beats N round trips,
// and it makes the account scoping impossible to get wrong — the list is
// already scoped, so a foreign id simply never matches.
func (a *Adapter) MailboxesByID(ctx context.Context, accountID int64, ids []int64) ([]MailboxRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}

	all, err := a.store.ListMailboxes(ctx, accountID)
	if err != nil {
		return nil, err
	}
	keep := make([]store.Mailbox, 0, len(ids))
	for _, m := range all {
		if want[m.ID] {
			keep = append(keep, m)
		}
	}
	return a.mailboxRows(ctx, accountID, keep)
}

// mailboxRows converts store mailboxes to MailboxRows, resolving parents,
// sort order and counts.
func (a *Adapter) mailboxRows(ctx context.Context, accountID int64, boxes []store.Mailbox) ([]MailboxRow, error) {
	if len(boxes) == 0 {
		return nil, nil
	}

	// Parent resolution needs the WHOLE tree, not just the requested subset:
	// the parent of a requested mailbox may not itself have been requested.
	all, err := a.store.ListMailboxes(ctx, accountID)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]int64, len(all))
	for _, m := range all {
		byName[m.Name] = m.ID
	}

	out := make([]MailboxRow, 0, len(boxes))
	for _, m := range boxes {
		total, unread, err := a.store.CountMailboxMessages(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		totalThreads, unreadThreads, err := a.threadCounts(ctx, m.ID)
		if err != nil {
			return nil, err
		}

		out = append(out, MailboxRow{
			ID:           m.ID,
			Name:         leafName(m.Name, m.Delimiter),
			ParentID:     parentID(m.Name, m.Delimiter, byName),
			Role:         string(m.Role),
			SortOrder:    sortOrderFor(m.Role),
			IsSubscribed: m.Subscribed,

			TotalEmails:  unsigned(total),
			UnreadEmails: unsigned(unread),

			TotalThreads:  totalThreads,
			UnreadThreads: unreadThreads,
		})
	}
	return out, nil
}

// leafName returns the mailbox's own name without its parent path.
//
// RFC 8621 §2 is explicit: "name: String — User-visible name for the Mailbox...
// This MUST NOT be the full path". IMAP reports full paths ("INBOX/Work/2026"),
// so the leaf is what JMAP wants and the hierarchy is expressed by parentId.
func leafName(full, delimiter string) string {
	if delimiter == "" {
		return full
	}
	if i := strings.LastIndex(full, delimiter); i >= 0 && i+len(delimiter) <= len(full) {
		if leaf := full[i+len(delimiter):]; leaf != "" {
			return leaf
		}
	}
	return full
}

// parentID resolves a mailbox's parent through its IMAP path.
//
// The store has no parent_id column — the hierarchy lives in the name, which
// is how IMAP expresses it — so the parent is the mailbox whose name is this
// one's path prefix. A path whose parent is not itself a mailbox (a gap in the
// hierarchy, which IMAP permits) yields 0, i.e. top level, because JMAP has no
// way to name a parent that does not exist.
func parentID(full, delimiter string, byName map[string]int64) int64 {
	if delimiter == "" {
		return 0
	}
	i := strings.LastIndex(full, delimiter)
	if i <= 0 {
		return 0
	}
	return byName[full[:i]]
}

// sortOrderFor gives the special-use folders the order every mail client shows
// them in.
//
// RFC 8621 §2 defines sortOrder as "Defines the sort order of Mailboxes when
// presented in the client's UI... The sortOrder fields should be treated as a
// hint" and says that when they are equal the client sorts by name. The
// convention below (Inbox first, Trash and Junk last) is what Gmail, Fastmail
// and every client the vara Gmail-class points at do; ordinary folders share
// one value so they fall back to alphabetical.
func sortOrderFor(role store.MailboxRole) uint64 {
	switch role {
	case store.RoleInbox:
		return 10
	case store.RoleDrafts:
		return 20
	case store.RoleSent:
		return 30
	case store.RoleArchive:
		return 40
	case store.RoleFlagged:
		return 50
	case store.RoleAll:
		return 60
	case store.RoleJunk:
		return 70
	case store.RoleTrash:
		return 80
	case store.RoleNone:
		return 100
	default:
		return 100
	}
}

// threadCounts computes totalThreads and unreadThreads for a mailbox.
//
// RFC 8621 §2 defines them as the number of distinct THREADS with at least one
// email in the mailbox, and — for unreadThreads — the number of threads with
// at least one unread email in the mailbox.
//
// With no thread column in the store (see thread.go), the honest computation
// would be to thread the whole mailbox on every Mailbox/get, which is exactly
// the unbounded work L2 §4.3 forbids. So the counts are derived from the
// message counts instead, which is correct whenever a thread has one message
// in the folder — the overwhelmingly common case — and over-counts otherwise.
//
// This is a documented, bounded inaccuracy rather than a silent one: it is in
// the J2 report as the second half of the threading store gap, and it becomes
// exact for free the moment a thread_id column exists, because this function
// is then one COUNT(DISTINCT thread_id).
func (a *Adapter) threadCounts(ctx context.Context, mailboxID int64) (total, unread uint64, err error) {
	t, u, err := a.store.CountMailboxMessages(ctx, mailboxID)
	if err != nil {
		return 0, 0, err
	}
	return unsigned(t), unsigned(u), nil
}

// unsigned converts a count from the database into the UnsignedInt every JMAP
// count property is typed as (RFC 8620 §1.3).
//
// A negative value cannot come from a COUNT(*) or a size column, but the
// conversion is written as a clamp rather than a cast because an unchecked
// int64 -> uint64 cast turns a hypothetical -1 into 18446744073709551615 — a
// number that would reach a client as a mailbox holding eighteen quintillion
// messages. Clamping keeps the failure mode boring.
func unsigned(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// ---------------------------------------------------------------------------
// EmailReader
// ---------------------------------------------------------------------------

// EmailsByID reads message metadata, scoped to the account.
//
// The store exposes GetMessage/GetMessageState per id rather than a batch
// read, so this loops. That is a store gap worth naming (a MessagesByIDs batch
// would turn N round trips into one, and Email/get legitimately asks for up to
// maxObjectsInGet=500 ids), and it is in the J2 report. The loop is correct
// meanwhile, and every read re-checks the account.
func (a *Adapter) EmailsByID(ctx context.Context, accountID int64, ids []int64) ([]EmailRow, error) {
	out := make([]EmailRow, 0, len(ids))
	for _, id := range ids {
		row, err := a.emailRow(ctx, accountID, id)
		switch {
		case errors.Is(err, ErrNotFound):
			continue
		case err != nil:
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

// emailRow assembles one EmailRow, or ErrNotFound.
func (a *Adapter) emailRow(ctx context.Context, accountID, id int64) (EmailRow, error) {
	msg, err := a.store.GetMessage(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return EmailRow{}, ErrNotFound
		}
		return EmailRow{}, err
	}
	// THE authorization check. A message of another account is reported as
	// not found, never as forbidden (contracts.go).
	if msg.AccountID != accountID {
		return EmailRow{}, ErrNotFound
	}

	st, err := a.store.GetMessageState(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// A message row with no state is a torn write the store's own
			// insert path prevents; treat it as absent rather than serving a
			// message that is in no mailbox.
			return EmailRow{}, ErrNotFound
		}
		return EmailRow{}, err
	}
	// A tombstoned message is gone as far as a /get is concerned. It survives
	// only so Email/changes (J3) can report it as destroyed.
	if st.DeletedAt != nil {
		return EmailRow{}, ErrNotFound
	}

	threadID, err := a.threadOf(ctx, accountID, msg)
	if err != nil {
		return EmailRow{}, err
	}

	row := EmailRow{
		ID:       msg.ID,
		ThreadID: threadID,
		BlobID:   blobIDOf(msg.RawSHA256),
		Size:     unsigned(msg.RawSize),

		MailboxIDs: []int64{st.MailboxID},
		Keywords:   jmapKeywords(uint64(st.Flags), st.Keywords),

		// RFC 8621 §4.1.1: receivedAt is "the internal date in IMAP". The
		// store keeps INTERNALDATE when the server gave one and falls back to
		// the Date header otherwise (internal/sync messageDate), so the
		// fallback here mirrors that.
		ReceivedAt: msg.Date,

		Subject:       msg.Subject,
		HasAttachment: msg.HasAttachments,
		Preview:       msg.Preview,
		ParseFailed:   msg.ParseStatus == store.ParseFailed,
	}
	if msg.InternalDate != nil {
		row.ReceivedAt = *msg.InternalDate
	}
	// sentAt is the Date header. The store's date column already holds it when
	// it parsed, with INTERNALDATE as the fallback — so the two coincide when
	// the header was missing, which is the correct reading of "null when the
	// header is absent" that the store cannot express separately.
	if !msg.Date.IsZero() {
		row.SentAt = msg.Date
		row.HasSentAt = true
	}

	if msg.MessageID != "" {
		row.MessageID = []string{msg.MessageID}
	}
	if msg.InReplyTo != "" {
		row.InReplyTo = []string{msg.InReplyTo}
	}
	row.Reference = msg.ReferencesIDs

	row.Addresses = decodeAddresses(msg.Addresses)
	row.Structure = decodeStructure(msg.MIMEStructure)
	return row, nil
}

// blobIDOf renders a raw sha256 as the blobId clients download by. L2 §4:
// "blobId = sha256 hex del blob (ya content-addressed, gratis)".
func blobIDOf(sha []byte) string {
	h, err := blob.HashFromBytes(sha)
	if err != nil {
		return ""
	}
	return h.String()
}

// decodeAddresses parses the store's addresses JSON column into the JMAP
// address shape.
//
// A malformed document yields no addresses rather than an error: the column is
// derived data, and one unreadable row must not fail a batch of 200 messages.
func decodeAddresses(raw []byte) map[string][]EmailAddress {
	out := map[string][]EmailAddress{}
	if len(raw) == 0 {
		return out
	}
	var doc map[string][]struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return out
	}
	for field, list := range doc {
		addrs := make([]EmailAddress, 0, len(list))
		for _, a := range list {
			addrs = append(addrs, EmailAddress{Name: a.Name, Email: a.Email})
		}
		out[field] = addrs
	}
	return out
}

// decodeStructure parses the store's mime_structure column.
func decodeStructure(raw []byte) []StructurePart {
	if len(raw) == 0 {
		return nil
	}
	var doc struct {
		Parts []StructurePart `json:"parts"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	return doc.Parts
}

// RawMessage opens a message's raw bytes, after checking the account owns it.
func (a *Adapter) RawMessage(ctx context.Context, accountID, messageID int64) (io.ReadCloser, error) {
	msg, err := a.store.GetMessage(ctx, messageID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if msg.AccountID != accountID {
		return nil, ErrNotFound
	}
	h, err := blob.HashFromBytes(msg.RawSHA256)
	if err != nil {
		return nil, ErrNotFound
	}
	rc, err := a.blobs.Open(h)
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return rc, nil
}

// ---------------------------------------------------------------------------
// ThreadReader
// ---------------------------------------------------------------------------

// ThreadsByID resolves threads by their root message id.
//
// See thread.go for why threading is derived here rather than read. The
// derivation walks the account's messages that share the thread's root, which
// is bounded by the References index the schema already provides.
func (a *Adapter) ThreadsByID(ctx context.Context, accountID int64, ids []string) ([]ThreadRow, error) {
	out := make([]ThreadRow, 0, len(ids))
	for _, wire := range ids {
		rootID, err := DecodeThreadID(wire)
		if err != nil {
			continue
		}
		members, err := a.threadMembers(ctx, accountID, rootID)
		switch {
		case errors.Is(err, ErrNotFound):
			continue
		case err != nil:
			return nil, err
		}
		out = append(out, ThreadRow{ID: wire, EmailIDs: members})
	}
	return out, nil
}

// threadMembers returns the messages of the thread rooted at rootID, oldest
// first, or ErrNotFound when the root is not a message of this account.
func (a *Adapter) threadMembers(ctx context.Context, accountID, rootID int64) ([]int64, error) {
	root, err := a.store.GetMessage(ctx, rootID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if root.AccountID != accountID {
		return nil, ErrNotFound
	}
	// The root of a thread must itself be a root: a message whose own thread
	// resolves elsewhere is not a thread id this server ever issued.
	rootOfRoot, err := a.rootMessageID(ctx, accountID, root)
	if err != nil {
		return nil, err
	}
	if rootOfRoot != rootID {
		return nil, ErrNotFound
	}

	type member struct {
		id   int64
		when time.Time
	}
	members := []member{{id: root.ID, when: receivedAtOf(root)}}

	// Everything that references the root's Message-ID is in the thread. The
	// query is bounded by the account and served by messages_acct_msgid.
	if root.MessageID != "" {
		descendants, err := a.messagesReferencing(ctx, accountID, root.MessageID)
		if err != nil {
			return nil, err
		}
		for _, m := range descendants {
			if m.ID == root.ID {
				continue
			}
			members = append(members, member{id: m.ID, when: receivedAtOf(m)})
		}
	}

	// §3: "sorted by the receivedAt date of the Email, oldest first". Ties
	// break on id so the order is total and therefore reproducible.
	sort.Slice(members, func(i, j int) bool {
		if members[i].when.Equal(members[j].when) {
			return members[i].id < members[j].id
		}
		return members[i].when.Before(members[j].when)
	})

	out := make([]int64, 0, len(members))
	for _, m := range members {
		out = append(out, m.id)
	}
	return out, nil
}

// threadOf returns the JMAP thread id of a message.
func (a *Adapter) threadOf(ctx context.Context, accountID int64, msg store.Message) (string, error) {
	rootID, err := a.rootMessageID(ctx, accountID, msg)
	if err != nil {
		return "", err
	}
	return EncodeThreadID(rootID), nil
}

// rootMessageID finds the oldest stored ancestor of a message.
//
// The References header lists the thread's ancestry oldest-first (RFC 5322
// §3.6.4), so the first entry this account actually stores is the thread root.
// Falling back to In-Reply-To covers the mailers that send only that, and a
// message with neither is its own root.
//
// It resolves ONE level rather than walking transitively: References already
// carries the full chain, so the first stored entry IS the root, and a walk
// would cost a query per hop for the same answer. The exception is a chain
// whose earlier entries are not stored locally, which yields the oldest one
// that is — the documented weakness in thread.go.
func (a *Adapter) rootMessageID(ctx context.Context, accountID int64, msg store.Message) (int64, error) {
	candidates := msg.ReferencesIDs
	if len(candidates) == 0 && msg.InReplyTo != "" {
		candidates = []string{msg.InReplyTo}
	}
	for _, ref := range candidates {
		if ref == "" || ref == msg.MessageID {
			continue
		}
		ancestor, err := a.messageByMessageID(ctx, accountID, ref)
		switch {
		case errors.Is(err, ErrNotFound):
			continue
		case err != nil:
			return 0, err
		}
		return ancestor, nil
	}
	return msg.ID, nil
}

func receivedAtOf(m store.Message) time.Time {
	if m.InternalDate != nil {
		return *m.InternalDate
	}
	return m.Date
}

// ---------------------------------------------------------------------------
// BlobReader
// ---------------------------------------------------------------------------

// OpenBlob serves a blob download, enforcing that the account references it.
//
// The ownership check is a blob_refs lookup rather than a message lookup, so a
// blob referenced by anything the account owns (a message today, a draft or a
// detached part later) is downloadable, and one it does not reference is
// indistinguishable from one that does not exist.
func (a *Adapter) OpenBlob(ctx context.Context, accountID int64, blobID string) (io.ReadCloser, int64, error) {
	h, err := blob.ParseHash(blobID)
	if err != nil {
		// A malformed blobId cannot name anything; it is not found, not a
		// bad request, because answering differently would tell a prober that
		// well-formed ids are the interesting ones.
		return nil, 0, ErrNotFound
	}

	owned, err := a.accountReferencesBlob(ctx, accountID, h)
	if err != nil {
		return nil, 0, err
	}
	if !owned {
		return nil, 0, ErrNotFound
	}

	size, err := a.blobs.Size(ctx, h)
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	rc, err := a.blobs.Open(h)
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	return rc, size, nil
}

// ---------------------------------------------------------------------------
// StateReader
// ---------------------------------------------------------------------------

// The /get "state" strings.
//
// RFC 8620 §5.1 wants "a string representing the state on the server for all
// the data of this type in the account", and §5.2 makes it the cursor for
// /changes. J3 owns /changes and will define the cursor's real grammar; what
// J2 must not do is emit a state that LOOKS like a cursor but is not
// monotonic, because a client would then pass it to Email/changes and get a
// wrong answer rather than an honest cannotCalculateChanges.
//
// So the state is derived from the account's own change watermark — the
// maximum message_state.updated_at, which is exactly what ChangedSince pages
// through — rendered as nanoseconds. It moves forward on every change and
// never backwards, which is the property §5.2 requires of a cursor, and it is
// already the cursor the store's own /changes read takes.

// EmailState returns the Email type's state string for an account.
func (a *Adapter) EmailState(ctx context.Context, accountID int64) (string, error) {
	return a.dataState(ctx, accountID)
}

// MailboxState is the Mailbox type's state.
//
// It shares the Email watermark rather than tracking mailbox rows separately,
// because a Mailbox object's counts change when its MESSAGES change — an
// unread count moving is a Mailbox/changes event — so a state derived only
// from the mailboxes table would fail to advance when totalEmails did.
func (a *Adapter) MailboxState(ctx context.Context, accountID int64) (string, error) {
	return a.dataState(ctx, accountID)
}

// ThreadState is the Thread type's state, on the same watermark for the same
// reason: a thread changes when a message joins it.
func (a *Adapter) ThreadState(ctx context.Context, accountID int64) (string, error) {
	return a.dataState(ctx, accountID)
}

// stateFor renders a watermark as a state string.
func stateFor(watermark time.Time, count int64) string {
	if watermark.IsZero() {
		// An account with no messages still needs a stable, non-empty state.
		return fmt.Sprintf("0-%d", count)
	}
	return fmt.Sprintf("%d-%d", watermark.UTC().UnixNano(), count)
}
