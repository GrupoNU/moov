package submit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// The outbox state machine, driven through an in-memory queue that mimics the
// store's send-intent semantics exactly (states, CAS cancel, claim rules).
// These tests prove the three rules of doc.go at the state-machine level; the
// PG-gated suite (outbox_pg_test.go) proves the two properties that live in
// PostgreSQL itself — FOR UPDATE SKIP LOCKED and the recovery scan — against
// the real thing.
//
// Crash simulation: a crash cannot be injected mid-function, so each boundary
// of the matrix is reproduced as the PERSISTED STATE a crash at that boundary
// leaves behind (that is the point of persisting every transition: the state
// IS the recovery input), plus the side-effect counters a previous life left
// in the fakes (messages delivered, Sent copies made). Recovery then runs for
// real, and the assertions count deliveries end to end.

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

type fakeQueue struct {
	mu   sync.Mutex
	rows map[int64]*store.SendIntent
	next int64
	now  func() time.Time

	failMarkAccepted int // fail the next N MarkSendIntentAccepted calls
	acceptedCalls    int
}

func newFakeQueue() *fakeQueue {
	return &fakeQueue{rows: map[int64]*store.SendIntent{}, now: time.Now}
}

func (q *fakeQueue) enqueue(accountID, emailID int64, msgID string, notBefore time.Time) *store.SendIntent {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.next++
	payload, _ := json.Marshal(IntentEnvelope{
		IdentityID: "primary",
		MailFrom:   "moov-test@example.test",
		RcptTo:     []string{"dest@example.test"},
	})
	in := &store.SendIntent{
		ID: q.next, AccountID: accountID, EmailID: emailID,
		MessageRFCID: msgID, Payload: payload,
		State: store.IntentQueued, NotBefore: notBefore,
		CreatedAt: q.now(), UpdatedAt: q.now(),
	}
	q.rows[in.ID] = in
	return in
}

func (q *fakeQueue) get(id int64) store.SendIntent {
	q.mu.Lock()
	defer q.mu.Unlock()
	return *q.rows[id]
}

// cancel mimics store.CancelSendIntent's compare-and-set.
func (q *fakeQueue) cancel(id int64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	in := q.rows[id]
	if in == nil || in.State != store.IntentQueued || in.AcceptedAt != nil {
		return false
	}
	now := q.now()
	in.State, in.CanceledAt, in.UpdatedAt = store.IntentCanceled, &now, now
	return true
}

func (q *fakeQueue) DueSendAccounts(context.Context) ([]int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	seen := map[int64]bool{}
	var out []int64
	for _, in := range q.rows {
		if in.State == store.IntentQueued && !in.NotBefore.After(q.now()) && !seen[in.AccountID] {
			seen[in.AccountID] = true
			out = append(out, in.AccountID)
		}
	}
	return out, nil
}

func (q *fakeQueue) ClaimDueSendIntents(_ context.Context, accountID int64, limit int) ([]store.SendIntent, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []store.SendIntent
	for _, in := range q.rows {
		if len(out) >= limit {
			break
		}
		if in.AccountID == accountID && in.State == store.IntentQueued && !in.NotBefore.After(q.now()) {
			in.State = store.IntentInFlight
			in.Attempts++
			in.UpdatedAt = q.now()
			out = append(out, *in)
		}
	}
	return out, nil
}

func (q *fakeQueue) RecoverInFlightSendIntents(context.Context) ([]store.SendIntent, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []store.SendIntent
	for _, in := range q.rows {
		if in.State == store.IntentInFlight {
			in.Attempts++
			in.UpdatedAt = q.now()
			out = append(out, *in)
		}
	}
	return out, nil
}

func (q *fakeQueue) MarkSendIntentAccepted(_ context.Context, id int64, reply string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.acceptedCalls++
	if q.failMarkAccepted > 0 {
		q.failMarkAccepted--
		return errors.New("injected: store unavailable at the worst instant")
	}
	in := q.rows[id]
	if in == nil {
		return store.ErrNotFound
	}
	if in.AcceptedAt == nil {
		now := q.now()
		in.AcceptedAt, in.AcceptedReply, in.UpdatedAt = &now, reply, now
	}
	return nil
}

func (q *fakeQueue) MarkSendIntentAppended(_ context.Context, id int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	in := q.rows[id]
	if in == nil {
		return store.ErrNotFound
	}
	if in.AppendedAt == nil {
		now := q.now()
		in.AppendedAt, in.UpdatedAt = &now, now
	}
	return nil
}

func (q *fakeQueue) CompleteIntent(_ context.Context, id int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	in := q.rows[id]
	if in == nil {
		return store.ErrNotFound
	}
	in.State, in.UpdatedAt = store.IntentDone, q.now()
	return nil
}

func (q *fakeQueue) FailIntent(_ context.Context, id int64, message string, retryAt *time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	in := q.rows[id]
	if in == nil {
		return store.ErrNotFound
	}
	in.LastError, in.UpdatedAt = message, q.now()
	if retryAt != nil {
		in.State, in.NotBefore = store.IntentQueued, *retryAt
	} else {
		in.State = store.IntentFailed
	}
	return nil
}

func (q *fakeQueue) GetAccount(_ context.Context, id int64) (store.Account, error) {
	return store.Account{ID: id, Email: "moov-test@example.test", IMAPUsername: "moov-test@example.test"}, nil
}

// fakeTransport scripts SMTP outcomes and counts deliveries per Message-ID —
// the end-to-end evidence every matrix assertion reads.
type fakeTransport struct {
	mu        sync.Mutex
	delivered map[string]int // Message-ID -> times the wire saw it
	// script is consumed per call: "ok", "transient", "permanent",
	// "ok-no-callback" (delivers but never invokes onAccepted — the
	// crash-between-250-and-persist simulator). Empty script means "ok".
	script []string
	calls  int
	// capture, when set, receives the next transmitted bytes.
	capture *[]byte
}

func newFakeTransport() *fakeTransport { return &fakeTransport{delivered: map[string]int{}} }

func (t *fakeTransport) Send(_ context.Context, _ store.Account, _ Envelope, msg io.Reader, onAccepted func(string) error) (Result, error) {
	t.mu.Lock()
	action := "ok"
	if t.calls < len(t.script) {
		action = t.script[t.calls]
	}
	t.calls++
	t.mu.Unlock()

	switch action {
	case "transient":
		return Result{}, &Error{Phase: PhaseConnect, Err: errors.New("injected: connection refused")}
	case "permanent":
		return Result{}, &Error{Phase: PhaseMail, Code: 550, Msg: "injected: rejected"}
	}

	raw, _ := io.ReadAll(msg)
	msgID := MessageIDOf(raw)
	t.mu.Lock()
	t.delivered[msgID]++
	if t.capture != nil {
		*t.capture = append([]byte(nil), raw...)
		t.capture = nil
	}
	t.mu.Unlock()

	reply := "250 2.0.0 Ok: queued as TEST"
	if action == "ok-no-callback" {
		// The 250 was read and then the process died before the persist: the
		// caller never learns. The wire, however, saw the message — that is
		// the whole point of counting here.
		return Result{Reply: reply}, &Error{Phase: PhaseDataClose, Err: errors.New("injected: crash after 250, before persist")}
	}
	if onAccepted != nil {
		if err := onAccepted(reply); err != nil {
			return Result{Reply: reply}, &AcceptedUnrecordedError{Reply: reply, Err: err}
		}
	}
	return Result{Reply: reply}, nil
}

func (t *fakeTransport) deliveries(msgID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.delivered[msgID]
}

// fakeSent models the \Sent mailbox: a Message-ID set plus scripted failures.
type fakeSent struct {
	mu        sync.Mutex
	contains  map[string]bool
	appendErr error
	appends   int
}

func newFakeSent() *fakeSent { return &fakeSent{contains: map[string]bool{}} }

func (s *fakeSent) AppendToSent(_ context.Context, _ int64, _ []byte, messageID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendErr != nil {
		return false, s.appendErr
	}
	if s.contains[messageID] {
		return true, nil
	}
	s.contains[messageID] = true
	s.appends++
	return false, nil
}

func (s *fakeSent) SentContainsMessageID(_ context.Context, _ int64, messageID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contains[messageID], nil
}

// fakeRaws serves draft bytes.
type fakeRaws struct{ raw map[int64][]byte }

func (r *fakeRaws) RawMessage(_ context.Context, _, emailID int64) (io.ReadCloser, error) {
	raw, ok := r.raw[emailID]
	if !ok {
		return nil, errors.New("no such draft")
	}
	return io.NopCloser(strings.NewReader(string(raw))), nil
}

type countingNotifier struct {
	mu sync.Mutex
	n  int
}

func (c *countingNotifier) Notify(int64) { c.mu.Lock(); c.n++; c.mu.Unlock() }
func (c *countingNotifier) count() int   { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

type outboxEnv struct {
	queue     *fakeQueue
	transport *fakeTransport
	sent      *fakeSent
	raws      *fakeRaws
	notifier  *countingNotifier
	outbox    *Outbox
}

const testMsgID = "w3-test@example.test"

func newOutboxEnv(t *testing.T) *outboxEnv {
	t.Helper()
	env := &outboxEnv{
		queue:     newFakeQueue(),
		transport: newFakeTransport(),
		sent:      newFakeSent(),
		raws:      &fakeRaws{raw: map[int64][]byte{}},
		notifier:  &countingNotifier{},
	}
	env.raws.raw[7] = []byte("Message-ID: <" + testMsgID + ">\r\n" +
		"Date: Sat, 15 Aug 2026 10:00:00 +0000\r\n" +
		"From: moov-test@example.test\r\nTo: dest@example.test\r\n" +
		"Bcc: hidden@example.test\r\nSubject: w3\r\n\r\nhello\r\n")

	ob, err := NewOutbox(env.queue, env.transport, env.sent, env.raws, Options{
		Logger:   slog.New(slog.DiscardHandler),
		Notifier: env.notifier,
	})
	if err != nil {
		t.Fatalf("NewOutbox: %v", err)
	}
	ob.persistRetryBase = time.Millisecond // keep the six-attempt ladder instant
	env.outbox = ob
	return env
}

// due enqueues one intent whose window has already passed.
func (e *outboxEnv) due() *store.SendIntent {
	return e.queue.enqueue(1, 7, testMsgID, time.Now().Add(-time.Second))
}

func (e *outboxEnv) tick() { e.outbox.runOnce(context.Background()) }

// ---------------------------------------------------------------------------
// the happy path and the undo
// ---------------------------------------------------------------------------

func TestOutboxHappyPath(t *testing.T) {
	env := newOutboxEnv(t)
	in := env.due()
	env.tick()

	row := env.queue.get(in.ID)
	if row.State != store.IntentDone {
		t.Fatalf("state = %s, want done (row: %+v)", row.State, row)
	}
	if row.AcceptedAt == nil || row.AppendedAt == nil {
		t.Error("phase timestamps missing after a complete run")
	}
	if !strings.HasPrefix(row.AcceptedReply, "250") {
		t.Errorf("accepted reply = %q, want the 250 line", row.AcceptedReply)
	}
	if n := env.transport.deliveries(testMsgID); n != 1 {
		t.Errorf("delivered %d times, want exactly 1", n)
	}
	if !env.sent.contains[testMsgID] {
		t.Error("no Sent copy was made")
	}
	if env.notifier.count() == 0 {
		t.Error("the broker was never notified; SSE clients would not see the state move")
	}
}

func TestOutboxTransmitsTheBccStrippedBytes(t *testing.T) {
	env := newOutboxEnv(t)
	env.due()

	var wire []byte
	env.transport.scriptCapture(&wire)
	env.tick()

	if strings.Contains(string(wire), "hidden@example.test") {
		t.Error("the transmitted bytes carry the Bcc header — a blind-copy leak (RFC 5322 §3.6.3)")
	}
	if !strings.Contains(string(wire), "To: dest@example.test") {
		t.Error("the transmitted bytes lost a header that must travel")
	}
}

func TestOutboxUndoWindowHoldsAndCancelLeavesNoTrace(t *testing.T) {
	env := newOutboxEnv(t)
	// Inside the window: not_before is in the future.
	in := env.queue.enqueue(1, 7, testMsgID, time.Now().Add(time.Hour))

	env.tick()
	if n := env.transport.calls; n != 0 {
		t.Fatalf("the executor touched an intent inside its undo window (%d transport calls)", n)
	}

	// The cancel CAS wins while queued ∧ ¬accepted (W-A3).
	if !env.queue.cancel(in.ID) {
		t.Fatal("cancel lost against nobody")
	}
	// Even once the window passes, a canceled row is never claimed.
	env.queue.mu.Lock()
	env.queue.rows[in.ID].NotBefore = time.Now().Add(-time.Minute)
	env.queue.mu.Unlock()
	env.tick()

	row := env.queue.get(in.ID)
	if row.State != store.IntentCanceled {
		t.Errorf("state = %s, want canceled", row.State)
	}
	if n := env.transport.calls; n != 0 {
		t.Errorf("a canceled submission reached the transport (%d calls) — undo left a trace", n)
	}
	if env.sent.appends != 0 {
		t.Error("a canceled submission produced a Sent copy")
	}
}

func TestOutboxCancelLosesAfterClaim(t *testing.T) {
	env := newOutboxEnv(t)
	in := env.due()
	env.tick() // claimed and completed

	if env.queue.cancel(in.ID) {
		t.Error("cancel succeeded against a claimed/completed submission; the CAS must refuse (cannotUnsend)")
	}
}

// ---------------------------------------------------------------------------
// rule 2: never retry after 250
// ---------------------------------------------------------------------------

func TestOutboxNeverRetransmitsAfter250(t *testing.T) {
	// THE test of ADR §4's hardest rule: the 250 is persisted, then the
	// \Sent APPEND fails — repeatedly. However many recovery passes run, the
	// wire sees the message exactly once; only the append retries.
	env := newOutboxEnv(t)
	in := env.due()
	env.sent.appendErr = errors.New("injected: dovecot away")

	env.tick()
	row := env.queue.get(in.ID)
	if row.AcceptedAt == nil {
		t.Fatal("acceptance was not persisted")
	}
	if row.State != store.IntentQueued {
		t.Fatalf("state = %s, want queued (post-send retry)", row.State)
	}

	// Three more passes with the append still failing, then one that heals.
	for range 3 {
		env.queue.mu.Lock()
		env.queue.rows[in.ID].NotBefore = time.Now().Add(-time.Second)
		env.queue.mu.Unlock()
		env.tick()
	}
	env.sent.appendErr = nil
	env.queue.mu.Lock()
	env.queue.rows[in.ID].NotBefore = time.Now().Add(-time.Second)
	env.queue.mu.Unlock()
	env.tick()

	if n := env.transport.deliveries(testMsgID); n != 1 {
		t.Errorf("delivered %d times across append retries, want exactly 1 — rule 2 is broken", n)
	}
	row = env.queue.get(in.ID)
	if row.State != store.IntentDone || row.AppendedAt == nil {
		t.Errorf("post-send retry did not converge: state=%s appended=%v", row.State, row.AppendedAt)
	}
}

func TestOutboxPermanentFailureIsFinal(t *testing.T) {
	env := newOutboxEnv(t)
	in := env.due()
	env.transport.script = []string{"permanent"}

	env.tick()
	row := env.queue.get(in.ID)
	if row.State != store.IntentFailed {
		t.Fatalf("state = %s, want failed", row.State)
	}
	if !strings.Contains(row.LastError, "550") {
		t.Errorf("the visible error lost the refusal: %q", row.LastError)
	}

	// Nothing ever touches it again.
	env.tick()
	if env.transport.calls != 1 {
		t.Errorf("a permanently failed submission was retried (%d calls)", env.transport.calls)
	}
}

func TestOutboxTransientRetriesWithBackoffThenSucceeds(t *testing.T) {
	env := newOutboxEnv(t)
	in := env.due()
	env.transport.script = []string{"transient", "transient", "ok"}

	for range 3 {
		env.queue.mu.Lock()
		env.queue.rows[in.ID].NotBefore = time.Now().Add(-time.Second) // collapse the backoff
		env.queue.rows[in.ID].State = store.IntentQueued
		env.queue.mu.Unlock()
		env.tick()
	}

	row := env.queue.get(in.ID)
	if row.State != store.IntentDone {
		t.Fatalf("state = %s, want done after transient retries", row.State)
	}
	if n := env.transport.deliveries(testMsgID); n != 1 {
		t.Errorf("delivered %d times, want 1 (transients never delivered anything)", n)
	}
}

func TestOutboxTransientGivesUpAtTheAttemptCap(t *testing.T) {
	env := newOutboxEnv(t)
	in := env.due()
	env.queue.mu.Lock()
	env.queue.rows[in.ID].Attempts = 7 // one below the default cap of 8; the claim makes it 8
	env.queue.mu.Unlock()
	env.transport.script = []string{"transient"}

	env.tick()
	if row := env.queue.get(in.ID); row.State != store.IntentFailed {
		t.Errorf("state = %s, want failed at the attempt cap", row.State)
	}
}

// ---------------------------------------------------------------------------
// the crash matrix — one test per phase boundary (doc.go)
// ---------------------------------------------------------------------------

func TestCrashMatrix(t *testing.T) {
	type matrix struct {
		name string
		// arrange puts the row and the fakes in the state a crash at this
		// boundary leaves behind.
		arrange func(env *outboxEnv, in *store.SendIntent)
		// wantDeliveries counts total wire deliveries AFTER recovery.
		wantDeliveries int
		wantState      store.IntentState
	}
	for _, tc := range []matrix{
		{
			// Boundary 1: crash after enqueue, before any claim. Recovery has
			// nothing in flight; the ordinary poll delivers once.
			name:           "before-claim",
			arrange:        func(env *outboxEnv, in *store.SendIntent) {},
			wantDeliveries: 1,
			wantState:      store.IntentDone,
		},
		{
			// Boundary 2: crash after the claim, before SMTP. The row is
			// in_flight, nothing was sent; recovery probes Sent (miss) and
			// delivers exactly once.
			name: "after-claim-before-smtp",
			arrange: func(env *outboxEnv, in *store.SendIntent) {
				env.queue.mu.Lock()
				env.queue.rows[in.ID].State = store.IntentInFlight
				env.queue.rows[in.ID].Attempts = 1
				env.queue.mu.Unlock()
			},
			wantDeliveries: 1,
			wantState:      store.IntentDone,
		},
		{
			// Boundary 3: crash mid-SMTP before the 250 (the server never
			// accepted). Identical recovery input to boundary 2 — the wire
			// saw a half transaction the server discarded — so: once.
			name: "mid-smtp-before-250",
			arrange: func(env *outboxEnv, in *store.SendIntent) {
				env.queue.mu.Lock()
				env.queue.rows[in.ID].State = store.IntentInFlight
				env.queue.rows[in.ID].Attempts = 1
				env.queue.mu.Unlock()
			},
			wantDeliveries: 1,
			wantState:      store.IntentDone,
		},
		{
			// Boundary 4a — THE RESIDUAL WINDOW, asserted as documented, not
			// hidden: crash after the 250, before the persist. The row looks
			// unsent, the Sent probe misses (no copy was made yet), and
			// recovery re-sends: 2 deliveries. doc.go documents exactly this
			// as the design's honest residue; this test pins that the residue
			// is CONFINED to this boundary (every other case asserts 1).
			name: "after-250-before-persist--documented-residual",
			arrange: func(env *outboxEnv, in *store.SendIntent) {
				env.queue.mu.Lock()
				env.queue.rows[in.ID].State = store.IntentInFlight
				env.queue.rows[in.ID].Attempts = 1
				env.queue.mu.Unlock()
				env.transport.mu.Lock()
				env.transport.delivered[testMsgID] = 1 // the previous life's send
				env.transport.mu.Unlock()
			},
			wantDeliveries: 2,
			wantState:      store.IntentDone,
		},
		{
			// Boundary 4b — the second net (ADR §4) catching the same crash:
			// the copy IS in Sent (a server-side auto-save, or a fast
			// onSuccess move). Recovery finds the Message-ID and never
			// re-sends.
			name: "after-250-before-persist--sent-probe-catches",
			arrange: func(env *outboxEnv, in *store.SendIntent) {
				env.queue.mu.Lock()
				env.queue.rows[in.ID].State = store.IntentInFlight
				env.queue.rows[in.ID].Attempts = 1
				env.queue.mu.Unlock()
				env.transport.mu.Lock()
				env.transport.delivered[testMsgID] = 1
				env.transport.mu.Unlock()
				env.sent.mu.Lock()
				env.sent.contains[testMsgID] = true
				env.sent.mu.Unlock()
			},
			wantDeliveries: 1,
			wantState:      store.IntentDone,
		},
		{
			// Boundary 5: crash after the persist, before the Sent copy.
			// accepted_at is set — recovery completes the post-send steps and
			// NEVER touches the transport (rule 1's whole purpose).
			name: "after-persist-before-append",
			arrange: func(env *outboxEnv, in *store.SendIntent) {
				now := time.Now()
				env.queue.mu.Lock()
				env.queue.rows[in.ID].State = store.IntentInFlight
				env.queue.rows[in.ID].Attempts = 1
				env.queue.rows[in.ID].AcceptedAt = &now
				env.queue.rows[in.ID].AcceptedReply = "250 2.0.0 Ok"
				env.queue.mu.Unlock()
				env.transport.mu.Lock()
				env.transport.delivered[testMsgID] = 1
				env.transport.mu.Unlock()
			},
			wantDeliveries: 1,
			wantState:      store.IntentDone,
		},
		{
			// Boundary 6: crash after the Sent copy, before completion.
			// Recovery completes; the dedupe makes the append a no-op, the
			// transport is never touched.
			name: "after-append-before-complete",
			arrange: func(env *outboxEnv, in *store.SendIntent) {
				now := time.Now()
				env.queue.mu.Lock()
				env.queue.rows[in.ID].State = store.IntentInFlight
				env.queue.rows[in.ID].Attempts = 1
				env.queue.rows[in.ID].AcceptedAt = &now
				env.queue.rows[in.ID].AppendedAt = &now
				env.queue.mu.Unlock()
				env.transport.mu.Lock()
				env.transport.delivered[testMsgID] = 1
				env.transport.mu.Unlock()
				env.sent.mu.Lock()
				env.sent.contains[testMsgID] = true
				env.sent.mu.Unlock()
			},
			wantDeliveries: 1,
			wantState:      store.IntentDone,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newOutboxEnv(t)
			in := env.due()
			tc.arrange(env, in)

			// The restart: recovery first (as Run does), then the poll loop.
			env.outbox.recover(context.Background())
			env.tick()

			if n := env.transport.deliveries(testMsgID); n != tc.wantDeliveries {
				t.Errorf("delivered %d times, want %d", n, tc.wantDeliveries)
			}
			if row := env.queue.get(in.ID); row.State != tc.wantState {
				t.Errorf("state = %s, want %s", row.State, tc.wantState)
			}
			if env.sent.appends > 1 {
				t.Errorf("the Sent mailbox took %d appends; the dedupe allows at most one", env.sent.appends)
			}
		})
	}
}

func TestOutboxAcceptedUnrecordedRetriesThePersistInPlace(t *testing.T) {
	// The store hiccups at the worst instant: the callback's persist fails,
	// the in-place retry then lands. One delivery, acceptance recorded,
	// everything completes — and the transport was never touched twice.
	env := newOutboxEnv(t)
	in := env.due()
	env.queue.failMarkAccepted = 2 // the callback's attempt + the first in-place retry

	env.tick()

	row := env.queue.get(in.ID)
	if row.AcceptedAt == nil {
		t.Fatal("the acceptance was never recorded despite the in-place retry")
	}
	if row.State != store.IntentDone {
		t.Errorf("state = %s, want done", row.State)
	}
	if n := env.transport.deliveries(testMsgID); n != 1 {
		t.Errorf("delivered %d times, want 1", n)
	}
}

func TestOutboxAcceptedUnrecordedNeverRequeuesTheSend(t *testing.T) {
	// The store stays down past every in-place retry. The row must remain
	// in_flight — NOT re-queued — because re-queueing an unrecorded
	// acceptance schedules a double send (outbox.go's CRITICAL branch).
	env := newOutboxEnv(t)
	in := env.due()
	env.queue.failMarkAccepted = 1 + 6 // callback + all six in-place retries

	env.tick()

	row := env.queue.get(in.ID)
	if row.State != store.IntentInFlight {
		t.Fatalf("state = %s, want in_flight (left for recovery, never re-queued)", row.State)
	}
	if row.AcceptedAt != nil {
		t.Error("acceptance recorded despite every persist failing — the fake is wrong")
	}
	if n := env.transport.deliveries(testMsgID); n != 1 {
		t.Errorf("delivered %d times, want 1", n)
	}
}

func TestOutboxSameProcessNeverDoubleExecutes(t *testing.T) {
	// The in-process guard: a recovery pass and a poll racing over one id
	// execute it once. (The cross-process guard is SKIP LOCKED, proven in the
	// PG suite.)
	env := newOutboxEnv(t)
	in := env.due()
	env.queue.mu.Lock()
	env.queue.rows[in.ID].State = store.IntentInFlight
	env.queue.mu.Unlock()

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env.outbox.recover(context.Background())
		}()
	}
	wg.Wait()

	if n := env.transport.deliveries(testMsgID); n != 1 {
		t.Errorf("delivered %d times under concurrent recovery, want 1", n)
	}
}

// scriptCapture makes the transport record the next transmitted bytes.
func (t *fakeTransport) scriptCapture(dst *[]byte) {
	t.mu.Lock()
	t.capture = dst
	t.mu.Unlock()
}

func TestBackoffIsBoundedAndGrows(t *testing.T) {
	ob, err := NewOutbox(newFakeQueue(), newFakeTransport(), newFakeSent(), &fakeRaws{}, Options{
		Logger:    slog.New(slog.DiscardHandler),
		RetryBase: time.Second,
		RetryMax:  time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	prevMax := time.Duration(0)
	for attempts := 1; attempts <= 12; attempts++ {
		d := ob.backoff(attempts)
		if d < time.Second/2 || d > time.Minute+time.Minute/5 {
			t.Fatalf("attempt %d: backoff %s escaped its [base/2, max*1.2] envelope", attempts, d)
		}
		if attempts <= 6 && d > prevMax {
			prevMax = d
		}
	}
	if prevMax < 2*time.Second {
		t.Errorf("backoff never grew past %s; the exponential is not exponential", prevMax)
	}
}
