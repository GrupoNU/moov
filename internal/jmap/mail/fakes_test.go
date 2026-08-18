package mail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Test doubles for the reader contracts. They hold data in maps and answer
// exactly what the interfaces promise — in particular, absence rather than an
// error for an unknown or foreign id, which is the behavior the handlers turn
// into notFound.

type fakeReaders struct {
	// mailboxes and emails are keyed by account id, so a test can prove
	// account scoping by giving two accounts overlapping object ids.
	mailboxes map[int64][]MailboxRow
	emails    map[int64][]EmailRow
	threads   map[int64][]ThreadRow
	raw       map[int64][]byte // message id -> raw bytes
	blobs     map[string][]byte

	// J3: the search corpus, in the order the repertoire would return it
	// (newest first), and the change feed, oldest first.
	hits         []searchHit
	searchWindow int
	changes      []ChangeRow
	newestChange time.Time

	mailboxCountChanges []int64
	mailboxRowChanges   []int64

	state string
	err   error // when set, every read fails with it

	// W1: the recorded write calls, and the error every write fails with
	// when set. A successful write advances the state string, so a test can
	// prove oldState/newState bracket the call's own effects.
	flagCalls    []fakeFlagsCall
	moveCalls    []fakeMoveCall
	destroyCalls []int64
	writeErr     error

	// W2: the folder mutations, and the keyword budget per mailbox.
	//
	// keywordsInUse is what the ceiling check reads; a nil entry means the
	// mailbox has no keywords, which is the common case and needs no setup.
	// keywordLimit overrides the ceiling so a boundary test does not have to
	// seed 26 keywords by hand — but the DEFAULT is the real 26, so a test
	// that forgets to set it exercises production behavior.
	createMailboxCalls  []fakeCreateMailboxCall
	renameMailboxCalls  []fakeRenameMailboxCall
	destroyMailboxCalls []int64
	mailboxWriteErr     error
	nextMailboxID       int64
	keywordsInUse       map[int64][]string
	keywordLimit        int
	budgetErr           error
}

// fakeCreateMailboxCall is one recorded CreateMailbox invocation.
type fakeCreateMailboxCall struct {
	accountID int64
	name      string
	subscribe bool
}

// fakeRenameMailboxCall is one recorded RenameMailbox invocation.
type fakeRenameMailboxCall struct {
	accountID int64
	mailboxID int64
	newName   string
}

// fakeFlagsCall is one recorded SetFlags invocation.
type fakeFlagsCall struct {
	accountID int64
	messageID int64
	change    FlagsChange
}

// fakeMoveCall is one recorded Move invocation.
type fakeMoveCall struct {
	accountID int64
	messageID int64
	mailboxID int64
}

func newFakeReaders() *fakeReaders {
	return &fakeReaders{
		mailboxes: map[int64][]MailboxRow{},
		emails:    map[int64][]EmailRow{},
		threads:   map[int64][]ThreadRow{},
		raw:       map[int64][]byte{},
		blobs:     map[string][]byte{},
		state:     "state-1",
	}
}

func (f *fakeReaders) Mailboxes(_ context.Context, accountID int64) ([]MailboxRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]MailboxRow(nil), f.mailboxes[accountID]...), nil
}

func (f *fakeReaders) MailboxesByID(_ context.Context, accountID int64, ids []int64) ([]MailboxRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []MailboxRow
	for _, m := range f.mailboxes[accountID] {
		if want[m.ID] {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeReaders) EmailsByID(_ context.Context, accountID int64, ids []int64) ([]EmailRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []EmailRow
	for _, e := range f.emails[accountID] {
		if want[e.ID] {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeReaders) RawMessage(_ context.Context, accountID, messageID int64) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	// Scoped like the real adapter: the message must belong to the account.
	owned := false
	for _, e := range f.emails[accountID] {
		if e.ID == messageID {
			owned = true
			break
		}
	}
	raw, ok := f.raw[messageID]
	if !owned || !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (f *fakeReaders) ThreadsByID(_ context.Context, accountID int64, ids []string) ([]ThreadRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []ThreadRow
	for _, t := range f.threads[accountID] {
		if want[t.ID] {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeReaders) OpenBlob(_ context.Context, accountID int64, blobID string) (io.ReadCloser, int64, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	// Ownership: the account must hold a message whose blobId matches.
	for _, e := range f.emails[accountID] {
		if e.BlobID == blobID {
			b, ok := f.blobs[blobID]
			if !ok {
				return nil, 0, ErrNotFound
			}
			return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
		}
	}
	return nil, 0, ErrNotFound
}

func (f *fakeReaders) MailboxState(context.Context, int64) (string, error) { return f.state, f.err }
func (f *fakeReaders) EmailState(context.Context, int64) (string, error)   { return f.state, f.err }
func (f *fakeReaders) ThreadState(context.Context, int64) (string, error)  { return f.state, f.err }

// SearchEmails answers a translated query out of the seeded corpus.
//
// It applies the SAME truncation the store does — the window bound — because
// that bound is what Email/query's total and anchor reasoning depend on. A
// fake that returned everything would make those paths untestable.
func (f *fakeReaders) SearchEmails(_ context.Context, _ int64, _ searchFilter, s sortSpec) ([]int64, error) {
	if f.err != nil {
		return nil, f.err
	}
	hits := append([]searchHit(nil), f.hits...)
	window := f.searchWindow
	if window <= 0 {
		window = DefaultSearchWindow
	}
	if len(hits) > window {
		hits = hits[:window]
	}
	if s.byRelevance {
		out := make([]int64, 0, len(hits))
		for _, h := range hits {
			out = append(out, h.id)
		}
		return out, nil
	}
	return sortIDsStable(hits, s.ascending, s.keyword != "", s.keywordFirst), nil
}

// ChangedSince replays the seeded feed from a cursor, honoring the limit the
// handler uses to detect a further page.
func (f *fakeReaders) ChangedSince(_ context.Context, _ int64, since time.Time, limit int) ([]ChangeRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []ChangeRow
	for _, c := range f.changes {
		if !c.UpdatedAt.After(since) {
			continue
		}
		out = append(out, c)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// NewestChangeAt reports the account watermark. When a test does not set one,
// it is derived from the seeded feed, so the common case needs no setup.
func (f *fakeReaders) NewestChangeAt(context.Context, int64) (time.Time, error) {
	if f.err != nil {
		return time.Time{}, f.err
	}
	if !f.newestChange.IsZero() {
		return f.newestChange, nil
	}
	var newest time.Time
	for _, c := range f.changes {
		if c.UpdatedAt.After(newest) {
			newest = c.UpdatedAt
		}
	}
	return newest, nil
}

func (f *fakeReaders) MailboxesTouchedSince(_ context.Context, _ int64, _ time.Time, _ int) ([]int64, []int64, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.mailboxCountChanges, f.mailboxRowChanges, nil
}

// ---- EmailWriter (W1) ------------------------------------------------------

// advanceState marks that a write landed, so the /set state strings move the
// way the real StateReader's watermark does.
func (f *fakeReaders) advanceState() {
	f.state = f.state + "'"
}

func (f *fakeReaders) SetFlags(_ context.Context, accountID, messageID int64, change FlagsChange) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.flagCalls = append(f.flagCalls, fakeFlagsCall{accountID: accountID, messageID: messageID, change: change})
	f.advanceState()
	return nil
}

func (f *fakeReaders) Move(_ context.Context, accountID, messageID, mailboxID int64) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	// Scoped like the real writer: an unknown or foreign mailbox is absent.
	owned := false
	for _, m := range f.mailboxes[accountID] {
		if m.ID == mailboxID {
			owned = true
			break
		}
	}
	if !owned {
		return ErrNotFound
	}
	f.moveCalls = append(f.moveCalls, fakeMoveCall{accountID: accountID, messageID: messageID, mailboxID: mailboxID})
	f.advanceState()
	return nil
}

func (f *fakeReaders) Destroy(_ context.Context, accountID, messageID int64) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	owned := false
	for _, e := range f.emails[accountID] {
		if e.ID == messageID {
			owned = true
			break
		}
	}
	if !owned {
		return ErrNotFound
	}
	f.destroyCalls = append(f.destroyCalls, messageID)
	f.advanceState()
	return nil
}

// KeywordBudget implements EmailWriter (W2). The default limit is the real
// ceiling, so a test that says nothing exercises production behavior.
func (f *fakeReaders) KeywordBudget(_ context.Context, _, mailboxID int64) (KeywordBudget, error) {
	if f.budgetErr != nil {
		return KeywordBudget{}, f.budgetErr
	}
	limit := f.keywordLimit
	if limit == 0 {
		limit = maxDurableKeywords
	}
	return KeywordBudget{InUse: append([]string(nil), f.keywordsInUse[mailboxID]...), Limit: limit}, nil
}

// ---- MailboxWriter (W2) ----------------------------------------------------

// CreateMailbox implements MailboxWriter, mutating the fake tree so a second
// operation in the same /set sees the folder the first one made.
func (f *fakeReaders) CreateMailbox(_ context.Context, accountID int64, name string, subscribe bool) (int64, error) {
	if f.mailboxWriteErr != nil {
		return 0, f.mailboxWriteErr
	}
	f.createMailboxCalls = append(f.createMailboxCalls,
		fakeCreateMailboxCall{accountID: accountID, name: name, subscribe: subscribe})

	f.nextMailboxID++
	id := 9000 + f.nextMailboxID

	// The fake tree stores LEAF names and parent ids, like the real reader, so
	// the created row has to be decomposed from the full path the handler
	// composed. That is not busywork: it is what proves the handler composed a
	// path the tree can round-trip.
	leaf, parentID := f.splitPath(accountID, name)
	f.mailboxes[accountID] = append(f.mailboxes[accountID], MailboxRow{
		ID: id, Name: leaf, ParentID: parentID, IsSubscribed: subscribe, SortOrder: 100,
	})
	f.advanceState()
	return id, nil
}

// RenameMailbox implements MailboxWriter, keeping the row's ID — the stability
// property W2 must guarantee — and re-pathing the children with it.
func (f *fakeReaders) RenameMailbox(_ context.Context, accountID, mailboxID int64, newName string) error {
	if f.mailboxWriteErr != nil {
		return f.mailboxWriteErr
	}
	f.renameMailboxCalls = append(f.renameMailboxCalls,
		fakeRenameMailboxCall{accountID: accountID, mailboxID: mailboxID, newName: newName})

	leaf, parentID := f.splitPath(accountID, newName)
	rows := f.mailboxes[accountID]
	for i := range rows {
		if rows[i].ID == mailboxID {
			// Same ID, new name and parent. Children follow automatically here
			// because the fake tree stores parent ids rather than paths, which
			// is exactly how the real store's id survives too.
			rows[i].Name, rows[i].ParentID = leaf, parentID
			f.advanceState()
			return nil
		}
	}
	return ErrNotFound
}

// DestroyMailbox implements MailboxWriter.
func (f *fakeReaders) DestroyMailbox(_ context.Context, accountID, mailboxID int64) error {
	if f.mailboxWriteErr != nil {
		return f.mailboxWriteErr
	}
	rows := f.mailboxes[accountID]
	for i := range rows {
		if rows[i].ID == mailboxID {
			f.mailboxes[accountID] = append(rows[:i], rows[i+1:]...)
			f.destroyMailboxCalls = append(f.destroyMailboxCalls, mailboxID)
			f.advanceState()
			return nil
		}
	}
	return ErrNotFound
}

// splitPath decomposes a full IMAP path into (leaf, parentID) against the
// current fake tree — the inverse of what the handler composed.
func (f *fakeReaders) splitPath(accountID int64, path string) (leaf string, parentID int64) {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return path, 0
	}
	leaf = path[i+1:]
	parentPath := path[:i]
	for _, m := range f.mailboxes[accountID] {
		if f.pathOf(accountID, m.ID) == parentPath {
			return leaf, m.ID
		}
	}
	return leaf, 0
}

// pathOf rebuilds a row's full path in the fake tree.
func (f *fakeReaders) pathOf(accountID, id int64) string {
	byID := map[int64]MailboxRow{}
	for _, m := range f.mailboxes[accountID] {
		byID[m.ID] = m
	}
	var segments []string
	for cur, hops := id, 0; cur != 0 && hops <= len(byID); hops++ {
		row, ok := byID[cur]
		if !ok {
			break
		}
		segments = append([]string{row.Name}, segments...)
		cur = row.ParentID
	}
	return strings.Join(segments, "/")
}

// deps builds a Deps over the fakes, with the real default limits so the
// maxObjectsInGet path is exercised with production values.
func (f *fakeReaders) deps() *Deps {
	return &Deps{
		Mailboxes: f, Emails: f, Threads: f, Blobs: f, State: f,
		Search: f, Changes: f, SearchWindow: f.searchWindow,
		Writer: f, Mailboxer: f,
		Limits: jmap.DefaultLimits(),
	}
}

// contextType is the context interface the handler signatures take. It is
// aliased so the queryChanges table test can hold both handlers in one map
// without repeating the full signature.
type contextType = context.Context

// testAccountID is the account every handler test authenticates as, and
// otherAccountID is the one whose data must never be visible.
const (
	testAccountID  int64 = 7
	otherAccountID int64 = 8
)

// callerCtx returns a context carrying the authenticated caller, as the HTTP
// layer would.
func callerCtx() context.Context {
	return jmap.WithCaller(context.Background(), jmap.Caller{
		AccountID: testAccountID,
		Email:     "user@example.com",
	})
}

// testAccountJMAPID is the wire form of testAccountID.
func testAccountJMAPID() string { return jmap.EncodeAccountID(testAccountID) }

// sampleMailbox builds a mailbox row for tests.
func sampleMailbox(id int64, name, role string, total, unread uint64) MailboxRow {
	return MailboxRow{
		ID: id, Name: name, Role: role,
		SortOrder:    10,
		IsSubscribed: true,
		TotalEmails:  total, UnreadEmails: unread,
		TotalThreads: total, UnreadThreads: unread,
	}
}

// sampleEmail builds an email row with a simple text/plain structure.
func sampleEmail(id int64, subject string) EmailRow {
	return EmailRow{
		ID:         id,
		ThreadID:   EncodeThreadID(id),
		BlobID:     "aa" + repeatHex(62),
		Size:       1024,
		MailboxIDs: []int64{1},
		Keywords:   []string{KeywordSeen},
		ReceivedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
		SentAt:     time.Date(2026, 8, 1, 9, 59, 0, 0, time.UTC),
		HasSentAt:  true,
		Subject:    subject,
		MessageID:  []string{"msg-" + subject + "@example.com"},
		Addresses: map[string][]EmailAddress{
			"from": {{Name: "Alice", Email: "alice@example.com"}},
			"to":   {{Name: "Bob", Email: "bob@example.com"}},
		},
		Preview: "hello",
		Structure: []StructurePart{
			{Index: 0, Parent: -1, MediaType: "text/plain", Charset: "utf-8", Size: 5},
		},
	}
}

func repeatHex(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'b'
	}
	return string(b)
}

// errSecret stands in for an internal error whose text must never reach the
// client (a constraint violation, a DSN, a table name).
var errSecret = errors.New("pq: duplicate key value violates constraint messages_pkey")

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// contextNoCaller is a context carrying no authenticated caller, used to prove
// handlers refuse rather than guess an account.
type contextNoCaller struct{ context.Context }

func (contextNoCaller) Deadline() (time.Time, bool) { return time.Time{}, false }
func (contextNoCaller) Done() <-chan struct{}       { return nil }
func (contextNoCaller) Err() error                  { return nil }
func (contextNoCaller) Value(any) any               { return nil }
