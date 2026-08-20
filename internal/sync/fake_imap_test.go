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

	// highestModSeq is the mailbox's CONDSTORE counter (E6). It only ever
	// increases, which is the property the incremental path depends on.
	highestModSeq imap.ModSeq

	// vanished is the expunge history QRESYNC replays to a reconnecting client
	// (E6).
	vanished []vanishedRecord

	// subscribed models the SUBSCRIBE state (W2). Mailboxes seeded by
	// addMailbox are subscribed, matching what a real account looks like.
	subscribed bool
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

	// ---- E6 ----------------------------------------------------------------

	// watchers are the live watches (E6).
	watchers []*fakeWatch

	// watchErr, when set, makes Watch fail — the connection failure the
	// breaker counts.
	watchErr error

	// silentNotify suppresses event delivery while still applying mutations,
	// which is how a test creates a divergence behind the watcher's back.
	silentNotify bool

	// connectErr, when set, makes the connector fail, so a test can drive the
	// backoff and the breaker without a real socket.
	connectErr error

	// ---- W1 ----------------------------------------------------------------

	// storeErr / moveErr / expungeErr, when set, fail the corresponding write
	// command — the injection the Dovecot-first ordering tests use to prove
	// the store is untouched when IMAP fails.
	storeErr   error
	moveErr    error
	expungeErr error

	// noCopyUID suppresses the COPYUID mapping a MOVE returns, modeling a
	// server without UIDPLUS so the degraded reflection path is testable.
	noCopyUID bool

	// ---- W3 ----------------------------------------------------------------

	// appendErr, when set, fails Append — the Dovecot-first ordering injection
	// for Email/set create and the outbox's \Sent copy.
	appendErr error

	// noAppendUID suppresses the [APPENDUID] response, modeling a server
	// without UIDPLUS so the refusal-to-reflect path is testable.
	noAppendUID bool

	// ---- W2 ----------------------------------------------------------------

	// The folder-command failure injections, one per command, so a test can
	// prove the store is untouched when Dovecot refuses (the W-A1 ordering
	// claim, restated for folders).
	createErr    error
	renameErr    error
	deleteErr    error
	subscribeErr error
	statusErr    error

	// uidValiditySeq hands out a fresh UIDVALIDITY per CREATE.
	uidValiditySeq uint32
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
//
// A brand-new mailbox starts at HIGHESTMODSEQ 1, matching Dovecot: an empty
// folder still has a modseq, which is what lets the incremental path own it
// after the initial sync instead of treating it as never-synced. seedMailbox
// raises it past the seeded messages.
func (s *fakeServer) addMailbox(name string, role imap.MailboxRole, uidValidity uint32) *fakeMailbox {
	s.mu.Lock()
	defer s.mu.Unlock()
	mb := &fakeMailbox{name: name, role: role, uidValidity: uidValidity, highestModSeq: 1, subscribed: true}
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
		// UIDPLUS: the fake answers [APPENDUID]/COPYUID like the real Dovecot,
		// and W3's append path refuses to run without the capability.
		imap.CapUIDPlus: struct{}{},
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
			Subscribed:    m.subscribed,
			NoSelect:      m.noSelect,
			HasStatus:     true,
			NumMessages:   uint32(len(m.messages)),
			UIDNext:       m.uidNext(),
			UIDValidity:   m.uidValidity,
			HighestModSeq: m.highestModSeq,
		})
	}
	return out, nil
}

// SelectQResync selects a mailbox, replaying the QRESYNC delta when the caller
// supplies a cursor.
//
// The VANISHED (EARLIER) replay is the part that matters for E6: a client that
// reconnects with an old modseq is told which UIDs disappeared while it was
// away, and getting that wrong is how a reconnected engine keeps showing mail
// that no longer exists.
func (c *fakeClient) SelectQResync(_ context.Context, mailbox string, uidValidity uint32, modSeq imap.ModSeq) (imap.SelectResult, error) {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()

	if c.closed {
		// A dead connection fails its next command, which is what the write
		// executor's self-healing SELECT probe depends on (W1).
		return imap.SelectResult{}, imap.ErrNotConnected
	}
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

	res := imap.SelectResult{
		UIDValidity:        mb.uidValidity,
		UIDValidityChanged: uidValidity != 0 && uidValidity != mb.uidValidity,
		HighestModSeq:      mb.highestModSeq,
		UIDNext:            mb.uidNext(),
		NumMessages:        uint32(len(mb.messages)),
	}

	// QRESYNC only replays when the caller's UIDVALIDITY still matches: after a
	// change, the old UIDs name nothing and replaying them would be worse than
	// useless.
	if modSeq > 0 && uidValidity == mb.uidValidity {
		res.VanishedUIDs = mb.vanishedSince(modSeq)
	}
	return res, nil
}

// FetchChanges is the live-connection incremental path: everything above the
// cursor, plus the vanished trail.
func (c *fakeClient) FetchChanges(_ context.Context, since imap.ModSeq) (imap.ChangeIter, error) {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()

	if c.selected == nil {
		return nil, imap.ErrNoMailboxSelected
	}

	changed := c.selected.changedSince(since)
	var vanished []imap.UID
	if since > 0 {
		vanished = c.selected.vanishedSince(since)
	}

	// Flags and modseq only: a CHANGEDSINCE pass reports what changed, and the
	// bodies of genuinely new messages are fetched separately by the engine.
	spec := imap.FetchSpec{Flags: true, InternalDate: true, Size: true, ChangedSince: since}
	return &fakeChangeIter{
		fakeIter: fakeIter{srv: c.srv, msgs: changed, spec: spec},
		vanished: vanished,
	}, nil
}

// fakeChangeIter is a fakeIter that also reports the vanished set.
type fakeChangeIter struct {
	fakeIter
	vanished []imap.UID
}

func (it *fakeChangeIter) Vanished() []imap.UID { return it.vanished }

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

func (c *fakeClient) Watch(ctx context.Context, spec imap.WatchSpec) (<-chan imap.Event, error) {
	return c.watch(ctx, spec)
}

// StoreFlags applies a flag delta server-side. Since W1 it models the whole
// contract the write executor depends on: the three RFC 3501 §6.4.6
// operations, the RFC 7162 UNCHANGEDSINCE refusal (a rejected message is
// named in Rejected, never an error — the silent-write hazard of S2 H6), and
// Dovecot's behavior of bumping a modseq only when the flags actually
// changed, which is what the executor's no-op and echo reasoning rest on.
func (c *fakeClient) StoreFlags(_ context.Context, uids []imap.UID, delta imap.FlagDelta, unchangedSince imap.ModSeq) (imap.StoreResult, error) {
	c.srv.mu.Lock()

	if err := c.srv.storeErr; err != nil {
		c.srv.mu.Unlock()
		return imap.StoreResult{}, err
	}
	if c.selected == nil {
		c.srv.mu.Unlock()
		return imap.StoreResult{}, imap.ErrNoMailboxSelected
	}

	var out imap.StoreResult
	changed := false
	for _, u := range uids {
		msg := c.selected.find(u)
		if msg == nil {
			continue
		}
		if unchangedSince > 0 && msg.modSeq > unchangedSince {
			out.Rejected = append(out.Rejected, u)
			continue
		}
		if c.selected.applyDelta(msg, delta) {
			out.Updated = append(out.Updated, u)
			changed = true
		}
	}
	out.HighestModSeq = c.selected.highestModSeq
	mailbox := c.selected.name
	status := c.selected.statusFor()
	c.srv.mu.Unlock()

	if changed {
		// The echo: a real Dovecot notifies the account's watcher about its
		// own write, and W1's convergence claim is tested against exactly
		// this.
		c.srv.notify(mailbox, status)
	}
	return out, nil
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
	// The mailbox counter has to end above every message's, or a delta asking
	// for "changes since HIGHESTMODSEQ" would replay the whole seeded mailbox.
	if m := imap.ModSeq(n); m > mb.highestModSeq {
		mb.highestModSeq = m
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
