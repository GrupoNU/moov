package sync

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
)

// A fake imap.Client with a deterministic corpus.
//
// It exists because the properties E5 must guarantee — resume after a kill -9,
// no duplicates, no loss, a failed parse that does not stop the run — are all
// properties of what happens BETWEEN a fetch and a commit. Against a real
// server those windows are timing-dependent and therefore untestable; against
// this fake they are exact, because the corpus is known and the failure can be
// injected at a chosen message.
//
// It models the parts of IMAP this epic depends on and no more: mailboxes with
// UIDs, UIDVALIDITY, flags and INTERNALDATE. It deliberately does NOT model
// QRESYNC deltas or NOTIFY, which are E6's and have their own harness.

// fakeMessage is one message in a fake mailbox.
type fakeMessage struct {
	uid          imap.UID
	raw          []byte
	flags        []string
	keywords     []string
	internalDate time.Time
	modSeq       imap.ModSeq
}

// fakeMailbox is one folder of the fake server.
type fakeMailbox struct {
	name        string
	role        imap.MailboxRole
	uidValidity uint32
	messages    []fakeMessage // ascending by uid

	// noSelect models an intermediate hierarchy node: it appears in LIST and
	// belongs in the folder tree, but SELECT on it is a protocol error.
	noSelect bool
}

func (m *fakeMailbox) uidNext() imap.UID {
	if len(m.messages) == 0 {
		return 1
	}
	return m.messages[len(m.messages)-1].uid + 1
}

func (m *fakeMailbox) find(uid imap.UID) *fakeMessage {
	for i := range m.messages {
		if m.messages[i].uid == uid {
			return &m.messages[i]
		}
	}
	return nil
}

// fakeServer is the shared state behind however many fakeClients a test uses,
// mirroring the real topology where several connections address one mailbox
// store.
type fakeServer struct {
	mu        sync.Mutex
	mailboxes []*fakeMailbox

	// fetchCount counts BODIES actually handed out, which is how a test proves
	// that a resumed run did not re-download what it already had. Metadata-only
	// probes (the recent phase's INTERNALDATE scan) are not counted: they
	// transfer no message content and would make the figure meaningless.
	fetchCount int

	// failAfterFetches, when > 0, makes the fetch iterator return an error
	// after that many bodies. This is the kill -9 simulator: it interrupts a
	// run mid-window, at a point the test chooses.
	failAfterFetches int

	// listErr, when set, makes ListMailboxes fail.
	listErr error
}

func newFakeServer() *fakeServer { return &fakeServer{} }

func (s *fakeServer) mailbox(name string) *fakeMailbox {
	for _, m := range s.mailboxes {
		if m.name == name {
			return m
		}
	}
	return nil
}

// addMailbox registers a folder.
func (s *fakeServer) addMailbox(name string, role imap.MailboxRole, uidValidity uint32) *fakeMailbox {
	s.mu.Lock()
	defer s.mu.Unlock()
	mb := &fakeMailbox{name: name, role: role, uidValidity: uidValidity}
	s.mailboxes = append(s.mailboxes, mb)
	return mb
}

// client returns a client bound to this server.
func (s *fakeServer) client() imap.Client { return &fakeClient{srv: s} }

// clients returns n clients, as a connection pool would hold.
func (s *fakeServer) clients(n int) []imap.Client {
	out := make([]imap.Client, 0, n)
	for range n {
		out = append(out, s.client())
	}
	return out
}

// totalMessages is the corpus size, which the assertions compare against.
func (s *fakeServer) totalMessages() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.mailboxes {
		n += len(m.messages)
	}
	return n
}

// fakeClient is one connection.
type fakeClient struct {
	srv      *fakeServer
	selected *fakeMailbox
	closed   bool
}

func (c *fakeClient) Connect(_ context.Context, _ imap.Config) error { return nil }

func (c *fakeClient) Capabilities() imap.Capabilities {
	return imap.Capabilities{
		imap.CapIMAP4rev1: struct{}{},
		imap.CapCondStore: struct{}{},
		imap.CapQResync:   struct{}{},
		imap.CapIdle:      struct{}{},
	}
}

func (c *fakeClient) ListMailboxes(_ context.Context) ([]imap.MailboxInfo, error) {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()

	if c.srv.listErr != nil {
		return nil, c.srv.listErr
	}

	out := make([]imap.MailboxInfo, 0, len(c.srv.mailboxes))
	for _, m := range c.srv.mailboxes {
		out = append(out, imap.MailboxInfo{
			Name:          m.name,
			Delimiter:     "/",
			Role:          m.role,
			Subscribed:    true,
			NoSelect:      m.noSelect,
			HasStatus:     true,
			NumMessages:   uint32(len(m.messages)),
			UIDNext:       m.uidNext(),
			UIDValidity:   m.uidValidity,
			HighestModSeq: 1,
		})
	}
	return out, nil
}

func (c *fakeClient) SelectQResync(_ context.Context, mailbox string, uidValidity uint32, _ imap.ModSeq) (imap.SelectResult, error) {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()

	mb := c.srv.mailbox(mailbox)
	if mb == nil {
		return imap.SelectResult{}, fmt.Errorf("fake: no such mailbox %q", mailbox)
	}
	if mb.noSelect {
		// A real server rejects this, which is exactly why discovery must
		// filter such mailboxes out rather than trying and recovering.
		return imap.SelectResult{}, fmt.Errorf("fake: mailbox %q is \\Noselect", mailbox)
	}
	c.selected = mb

	return imap.SelectResult{
		UIDValidity:        mb.uidValidity,
		UIDValidityChanged: uidValidity != 0 && uidValidity != mb.uidValidity,
		HighestModSeq:      1,
		UIDNext:            mb.uidNext(),
		NumMessages:        uint32(len(mb.messages)),
	}, nil
}

func (c *fakeClient) FetchChanges(_ context.Context, _ imap.ModSeq) (imap.ChangeIter, error) {
	return nil, fmt.Errorf("fake: FetchChanges is E6's, not E5's")
}

func (c *fakeClient) FetchMessages(_ context.Context, uids []imap.UID, spec imap.FetchSpec) (imap.MessageIter, error) {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()

	if c.selected == nil {
		return nil, imap.ErrNoMailboxSelected
	}

	// Materialize the matching messages up front, the way a real server
	// streams whatever exists and silently ignores UIDs that do not.
	var out []fakeMessage
	for _, u := range uids {
		if m := c.selected.find(u); m != nil {
			out = append(out, *m)
		}
	}
	return &fakeIter{srv: c.srv, msgs: out, spec: spec}, nil
}

func (c *fakeClient) Watch(_ context.Context, _ imap.WatchSpec) (<-chan imap.Event, error) {
	return nil, imap.ErrWatchNotSupported
}

func (c *fakeClient) StoreFlags(_ context.Context, _ []imap.UID, _ imap.FlagDelta, _ imap.ModSeq) (imap.StoreResult, error) {
	return imap.StoreResult{}, fmt.Errorf("fake: StoreFlags is not part of E5")
}

func (c *fakeClient) Metadata() imap.MetadataOps { return nil }

func (c *fakeClient) Close() error {
	c.closed = true
	return nil
}

// fakeIter streams the selected messages, honoring the injected failure point.
type fakeIter struct {
	srv    *fakeServer
	msgs   []fakeMessage
	spec   imap.FetchSpec
	i      int
	closed bool
	body   *fakeBody
}

func (it *fakeIter) Next() (*imap.Message, error) {
	if it.closed {
		return nil, imap.ErrIteratorClosed
	}
	// The previous body dies when the iterator advances, exactly as the real
	// one does — so a consumer that defers reading it fails here rather than in
	// production.
	if it.body != nil {
		it.body.dead = true
		it.body = nil
	}
	if it.i >= len(it.msgs) {
		return nil, nil
	}

	// Only a body fetch counts as "downloading a message"; the metadata probe
	// that decides the recent window does not.
	if it.spec.Body {
		it.srv.mu.Lock()
		it.srv.fetchCount++
		count := it.srv.fetchCount
		limit := it.srv.failAfterFetches
		it.srv.mu.Unlock()

		if limit > 0 && count > limit {
			return nil, errInjectedFetchFailure
		}
	}

	m := it.msgs[it.i]
	it.i++

	msg := &imap.Message{
		UID:    m.uid,
		SeqNum: uint32(it.i),
		ModSeq: m.modSeq,
	}
	if it.spec.Flags {
		msg.Flags = m.flags
		msg.Keywords = m.keywords
	}
	if it.spec.InternalDate {
		msg.InternalDate = m.internalDate
	}
	if it.spec.Size {
		msg.Size = int64(len(m.raw))
	}
	if it.spec.Body {
		it.body = &fakeBody{r: bytes.NewReader(m.raw)}
		msg.Body = it.body
	} else if it.spec.Headers {
		header, _, _ := bytes.Cut(m.raw, []byte("\r\n\r\n"))
		msg.Header = header
	}
	return msg, nil
}

func (it *fakeIter) Close() error {
	it.closed = true
	if it.body != nil {
		it.body.dead = true
		it.body = nil
	}
	return nil
}

// fakeBody enforces the real body's lifetime rule.
type fakeBody struct {
	r    io.Reader
	dead bool
}

func (b *fakeBody) Read(p []byte) (int, error) {
	if b.dead {
		return 0, imap.ErrIteratorClosed
	}
	return b.r.Read(p)
}

// errInjectedFetchFailure is the simulated crash.
var errInjectedFetchFailure = fmt.Errorf("fake: injected fetch failure")

// ---------------------------------------------------------------------------
// corpus construction
// ---------------------------------------------------------------------------

// buildMessage renders a deterministic RFC 5322 message.
//
// Deterministic in every byte: the same index always produces the same bytes,
// so a hash-addressed blob store sees the same content on a re-run and the
// idempotency assertions are exact rather than probabilistic.
func buildMessage(idx int, subject string, date time.Time, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "Message-ID: <msg-%d@fake.test>\r\n", idx)
	fmt.Fprintf(&b, "Date: %s\r\n", date.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "From: Sender %d <sender%d@fake.test>\r\n", idx, idx)
	fmt.Fprintf(&b, "To: Moov Test <moov-test@fake.test>\r\n")
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

// unparseableMessage is bytes the cascade cannot make sense of.
//
// It is what proves R4: a message that fails to parse is still STORED (its blob
// is durable and its UID is occupied) and does not stop the run. Empty input is
// the one case the parser documents as hopeless (corpus structural-002), so it
// is the honest way to produce parse_status='failed' without depending on a
// quirk that a parser improvement might fix.
func unparseableMessage() []byte { return []byte{} }

// seedMailbox fills a fake mailbox with n messages ending at the given time,
// one per hour going back.
func seedMailbox(mb *fakeMailbox, n int, newest time.Time, subjectPrefix string) {
	for i := range n {
		uid := imap.UID(i + 1)
		// UID order matches arrival order, as IMAP guarantees: the oldest
		// message has the lowest UID.
		date := newest.Add(-time.Duration(n-1-i) * time.Hour)
		mb.messages = append(mb.messages, fakeMessage{
			uid:          uid,
			raw:          buildMessage(i, fmt.Sprintf("%s %d", subjectPrefix, i), date, fmt.Sprintf("Body of message %d in %s.", i, mb.name)),
			flags:        flagsForIndex(i),
			internalDate: date,
			modSeq:       imap.ModSeq(i + 1),
		})
	}
}

// flagsForIndex gives a deterministic, varied flag distribution so the flag
// mapping is exercised rather than assumed.
func flagsForIndex(i int) []string {
	switch i % 4 {
	case 0:
		return []string{"seen"}
	case 1:
		return nil
	case 2:
		return []string{"seen", "answered"}
	default:
		return []string{"flagged"}
	}
}
