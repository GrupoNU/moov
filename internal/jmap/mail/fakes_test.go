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

	state string
	err   error // when set, every read fails with it
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

// deps builds a Deps over the fakes, with the real default limits so the
// maxObjectsInGet path is exercised with production values.
func (f *fakeReaders) deps() *Deps {
	return &Deps{
		Mailboxes: f, Emails: f, Threads: f, Blobs: f, State: f,
		Limits: jmap.DefaultLimits(),
	}
}

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
