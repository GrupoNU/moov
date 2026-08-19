package jmaphttp

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/config"
)

// EventSource endpoint tests (W4a, RFC 8620 §7.3).
//
// They exercise the endpoint over a real httptest.Server rather than
// httptest.NewRecorder, because a recorder buffers: the whole point of this
// route is that bytes reach the client BEFORE the response ends, and only a
// real connection can show that.

// ---------------------------------------------------------------------------
// doubles
// ---------------------------------------------------------------------------

// fakeNotifier is a hand-driven StateNotifier: the test decides exactly when
// a notification is delivered.
type fakeNotifier struct {
	mu     sync.Mutex
	subs   map[int64][]chan Notification
	closed bool
}

func newFakeNotifier() *fakeNotifier {
	return &fakeNotifier{subs: map[int64][]chan Notification{}}
}

func (n *fakeNotifier) StateEvents(accountID int64) (<-chan Notification, func()) {
	ch := make(chan Notification, 1)
	n.mu.Lock()
	n.subs[accountID] = append(n.subs[accountID], ch)
	n.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			n.mu.Lock()
			defer n.mu.Unlock()
			for i, c := range n.subs[accountID] {
				if c == ch {
					n.subs[accountID] = append(n.subs[accountID][:i], n.subs[accountID][i+1:]...)
					break
				}
			}
			if !n.closed {
				close(ch)
			}
		})
	}
}

func (n *fakeNotifier) Subscribers(accountID int64) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.subs[accountID])
}

// notify wakes every subscriber of an account.
func (n *fakeNotifier) notify(accountID int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, ch := range n.subs[accountID] {
		select {
		case ch <- Notification{}:
		default:
		}
	}
}

// waitForSubscriber blocks until the account has at least one subscriber, so a
// test never notifies into the void before the handler subscribed.
func (n *fakeNotifier) waitForSubscriber(t *testing.T, accountID int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n.Subscribers(accountID) > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no EventSource subscriber appeared")
}

// fakeStates is a StateSource whose values the test controls.
type fakeStates struct {
	mu                       sync.Mutex
	mailbox, email, threadSt string
	err                      error
}

func newFakeStates() *fakeStates {
	return &fakeStates{mailbox: "mb-1", email: "em-1", threadSt: "th-1"}
}

func (f *fakeStates) set(mailbox, email, thread string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mailbox, f.email, f.threadSt = mailbox, email, thread
}

func (f *fakeStates) MailboxState(context.Context, int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mailbox, f.err
}

func (f *fakeStates) EmailState(context.Context, int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.email, f.err
}

func (f *fakeStates) ThreadState(context.Context, int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.threadSt, f.err
}

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

type sseHarness struct {
	srv      *httptest.Server
	notifier *fakeNotifier
	states   *fakeStates
}

func newSSEHarness(t *testing.T, mutate func(*Config)) *sseHarness {
	t.Helper()
	n := newFakeNotifier()
	st := newFakeStates()

	s, _, _, _ := newTestServer(t, func(c *Config) {
		c.Notifier = n
		c.State = st
		if mutate != nil {
			mutate(c)
		}
	})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	return &sseHarness{srv: srv, notifier: n, states: st}
}

// openStream is open() for the tests that do not inspect the response object.
//
// It exists because bodyclose flags any call yielding an *http.Response whose
// body it cannot prove is closed, and it cannot follow the t.Cleanup this
// harness registers. Not returning the response is a cleaner answer than a
// nolint on every call site.
func (h *sseHarness) openStream(t *testing.T, query string, headers map[string]string) (*bufio.Reader, context.CancelFunc) {
	t.Helper()
	_, r, cancel := h.open(t, query, headers) //nolint:bodyclose // the body is closed by open's t.Cleanup
	return r, cancel
}

// open starts an authenticated EventSource request and returns the response
// plus a reader over its body.
func (h *sseHarness) open(t *testing.T, query string, headers map[string]string) (*http.Response, *bufio.Reader, context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.URL+PathEventSource+query, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.SetBasicAuth("user@example.com", testPassword)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the cleanup below; bodyclose cannot see through a helper
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	body := resp.Body
	t.Cleanup(func() { _ = body.Close() })
	return resp, bufio.NewReader(body), cancel
}

// sseEvent is one parsed server-sent event.
type sseEvent struct {
	id    string
	name  string
	data  string
	rawID bool // whether an "id:" line was present at all
}

// readEvent reads one complete event (up to the blank line) with a deadline.
func readEvent(t *testing.T, r *bufio.Reader) sseEvent {
	t.Helper()

	type result struct {
		ev  sseEvent
		err error
	}
	done := make(chan result, 1)

	go func() {
		var ev sseEvent
		var data []string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				done <- result{err: err}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				ev.data = strings.Join(data, "\n")
				done <- result{ev: ev}
				return
			case strings.HasPrefix(line, "id: "):
				ev.id = strings.TrimPrefix(line, "id: ")
				ev.rawID = true
			case strings.HasPrefix(line, "event: "):
				ev.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = append(data, strings.TrimPrefix(line, "data: "))
			}
		}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("reading an SSE event: %v", res.err)
		}
		return res.ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an SSE event")
		return sseEvent{}
	}
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestEventSourceRequiresAuth(t *testing.T) {
	t.Parallel()

	h := newSSEHarness(t, nil)

	// No credentials: the push stream is mail data, and it is gated by the
	// same J-A1 auth as every other route.
	resp, err := http.Get(h.srv.URL + PathEventSource)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// TestEventSourceUnwiredIs501 keeps the honest degradation: a server with no
// push plumbing says so rather than serving an empty stream forever.
func TestEventSourceUnwiredIs501(t *testing.T) {
	t.Parallel()

	s, _, _, _ := newTestServer(t, nil) // no Notifier, no State
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+PathEventSource, nil)
	req.SetBasicAuth("user@example.com", testPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", resp.StatusCode)
	}
}

// TestEventSourceFramingAndImmediateState covers §7.3's media type, the
// immediate state event on connection, and §7.1's payload shape.
func TestEventSourceFramingAndImmediateState(t *testing.T) {
	t.Parallel()

	h := newSSEHarness(t, nil)
	resp, r, cancel := h.open(t, "?types=*&closeafter=no&ping=0", nil) //nolint:bodyclose // the harness closes the body in t.Cleanup
	defer cancel()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// §7.3: a "text/event-stream" resource.
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	// A buffering proxy is the documented failure mode §7.3's closeafter
	// exists to work around; not provoking it is better than relying on it.
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Error("X-Accel-Buffering: no is missing; a proxy may buffer the stream")
	}

	ev := readEvent(t, r)
	if ev.name != "state" {
		t.Errorf("event name = %q, want %q (§7.3: an event called \"state\")", ev.name, "state")
	}
	if !ev.rawID || ev.id == "" {
		t.Error("§7.3 SHOULDs an event id encoding the server state; none was sent")
	}

	var change struct {
		Type    string                       `json:"@type"`
		Changed map[string]map[string]string `json:"changed"`
	}
	if err := json.Unmarshal([]byte(ev.data), &change); err != nil {
		t.Fatalf("event data is not JSON: %v (data=%q)", err, ev.data)
	}
	// §7.1: "@type: ... This MUST be the string "StateChange"."
	if change.Type != "StateChange" {
		t.Errorf("@type = %q, want \"StateChange\"", change.Type)
	}
	// §7.1: changed maps an ACCOUNT ID to a TypeState object.
	if len(change.Changed) != 1 {
		t.Fatalf("changed has %d accounts, want exactly the caller's one", len(change.Changed))
	}
	for _, ts := range change.Changed {
		if ts["Mailbox"] != "mb-1" || ts["Email"] != "em-1" || ts["Thread"] != "th-1" {
			t.Errorf("TypeState = %+v, want the reader's current states", ts)
		}
	}
}

// TestEventSourcePushesOnNotification is the core behavior: a change in the
// sync engine reaches an open stream, carrying the NEW state strings.
func TestEventSourcePushesOnNotification(t *testing.T) {
	t.Parallel()

	h := newSSEHarness(t, nil)
	r, cancel := h.openStream(t, "?types=*&closeafter=no&ping=0", nil)
	defer cancel()

	first := readEvent(t, r)
	h.notifier.waitForSubscriber(t, 7)

	// The store moves on, then the engine notifies.
	h.states.set("mb-2", "em-2", "th-2")
	h.notifier.notify(7)

	second := readEvent(t, r)
	if second.name != "state" {
		t.Fatalf("event name = %q, want state", second.name)
	}
	if second.id == first.id {
		t.Error("the event id did not change although the state did")
	}

	var change struct {
		Changed map[string]map[string]string `json:"changed"`
	}
	if err := json.Unmarshal([]byte(second.data), &change); err != nil {
		t.Fatal(err)
	}
	for _, ts := range change.Changed {
		// The payload must be read at SEND time from the state reader — this
		// is what guarantees a client's follow-up /changes call matches.
		if ts["Email"] != "em-2" {
			t.Errorf("pushed Email state = %q, want the current em-2", ts["Email"])
		}
	}
}

// TestEventSourceTypesFilter covers §7.3: "The server MUST only push changes
// for the types in this list."
func TestEventSourceTypesFilter(t *testing.T) {
	t.Parallel()

	h := newSSEHarness(t, nil)
	r, cancel := h.openStream(t, "?types=Email&closeafter=no&ping=0", nil)
	defer cancel()

	ev := readEvent(t, r)
	var change struct {
		Changed map[string]map[string]string `json:"changed"`
	}
	if err := json.Unmarshal([]byte(ev.data), &change); err != nil {
		t.Fatal(err)
	}
	for _, ts := range change.Changed {
		if _, ok := ts["Email"]; !ok {
			t.Error("Email was requested but not pushed")
		}
		if _, ok := ts["Mailbox"]; ok {
			t.Error("Mailbox was pushed although only Email was requested")
		}
		if _, ok := ts["Thread"]; ok {
			t.Error("Thread was pushed although only Email was requested")
		}
	}
}

// TestEventSourceCloseAfterState covers §7.3: "state": the server MUST end
// the HTTP response after pushing a state event.
func TestEventSourceCloseAfterState(t *testing.T) {
	t.Parallel()

	h := newSSEHarness(t, nil)
	r, cancel := h.openStream(t, "?types=*&closeafter=state&ping=0", nil)
	defer cancel()

	if ev := readEvent(t, r); ev.name != "state" {
		t.Fatalf("event name = %q, want state", ev.name)
	}

	// The body must end right after that event.
	done := make(chan error, 1)
	go func() {
		_, err := r.ReadString('\n')
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the response continued after a closeafter=state event")
		}
	case <-time.After(5 * time.Second):
		t.Error("the response did not end after a closeafter=state event")
	}
}

// TestEventSourcePingCadence covers the ping contract of §7.3 end to end: an
// event named "ping", data {"interval": N} in SECONDS, no id ("This MUST NOT
// set a new event id"), and a timer measured from the previous event.
//
// The RFC forbids a client-facing minimum above 30s, so a conforming request
// cannot ask for a fast ping. The floor is lowered for this test only (see
// pingFloor) so the REAL timer path runs instead of being asserted by
// inspection; TestSSEPingBoundsAreConforming guards the exported constants.
func TestEventSourcePingCadence(t *testing.T) {
	// Not parallel: it mutates the package-level ping floor.
	old := pingFloor
	pingFloor = 20 * time.Millisecond
	t.Cleanup(func() { pingFloor = old })

	h := newSSEHarness(t, nil)
	r, cancel := h.openStream(t, "?types=*&closeafter=no&ping=1", nil)
	defer cancel()

	if ev := readEvent(t, r); ev.name != "state" {
		t.Fatalf("first event = %q, want state", ev.name)
	}

	ping := readEvent(t, r)
	if ping.name != "ping" {
		t.Fatalf("second event = %q, want ping", ping.name)
	}
	// "This MUST NOT set a new event id."
	if ping.rawID {
		t.Error("the ping event carried an id, which §7.3 forbids")
	}

	var payload struct {
		Interval *int `json:"interval"`
	}
	if err := json.Unmarshal([]byte(ping.data), &payload); err != nil {
		t.Fatalf("ping data is not JSON: %v (data=%q)", err, ping.data)
	}
	if payload.Interval == nil {
		t.Fatal(`the ping data MUST contain an "interval" property (§7.3)`)
	}
	// The interval reported is the one the server actually uses after
	// clamping, in seconds.
	if want := int(clampPing(time.Second) / time.Second); *payload.Interval != want {
		t.Errorf("ping interval = %d, want the clamped %d", *payload.Interval, want)
	}

	// Pings keep coming while the stream is idle.
	if second := readEvent(t, r); second.name != "ping" {
		t.Errorf("third event = %q, want another ping", second.name)
	}
}

// TestEventSourcePingZeroDisables covers §7.3: "If the value is '0', the
// server MUST NOT send ping events."
func TestEventSourcePingZeroDisables(t *testing.T) {
	old := pingFloor
	pingFloor = 20 * time.Millisecond
	t.Cleanup(func() { pingFloor = old })

	h := newSSEHarness(t, nil)
	r, cancel := h.openStream(t, "?types=*&closeafter=no&ping=0", nil)
	defer cancel()

	if ev := readEvent(t, r); ev.name != "state" {
		t.Fatalf("first event = %q, want state", ev.name)
	}

	got := make(chan string, 1)
	go func() {
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "event: ping") {
				got <- line
				return
			}
		}
	}()
	select {
	case line := <-got:
		t.Errorf("a ping arrived on a ping=0 stream: %q", line)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestEventSourcePingResetByStateEvent covers the exact wording of §7.3: the
// server sends a ping "whenever this time elapses since the previous event
// was sent" — so a state event restarts the timer rather than a free-running
// ticker firing regardless.
func TestEventSourcePingResetByStateEvent(t *testing.T) {
	old := pingFloor
	pingFloor = 150 * time.Millisecond
	t.Cleanup(func() { pingFloor = old })

	h := newSSEHarness(t, nil)
	r, cancel := h.openStream(t, "?types=*&closeafter=no&ping=1", nil)
	defer cancel()

	if ev := readEvent(t, r); ev.name != "state" {
		t.Fatalf("first event = %q, want state", ev.name)
	}
	h.notifier.waitForSubscriber(t, 7)

	// Keep the stream busy for longer than one ping interval, notifying more
	// often than the interval. Every event must be a state event: the timer is
	// reset each time, so it never elapses.
	deadline := time.Now().Add(450 * time.Millisecond)
	for time.Now().Before(deadline) {
		h.states.set("mb-x", "em-"+time.Now().Format("150405.000000000"), "th-x")
		h.notifier.notify(7)
		ev := readEvent(t, r)
		if ev.name == "ping" {
			t.Fatal("a ping fired while the stream was busier than the ping interval; " +
				"the timer is free-running instead of being reset by each event")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestEventSourceConnectionCap covers W-A4's per-account limit.
func TestEventSourceConnectionCap(t *testing.T) {
	t.Parallel()

	h := newSSEHarness(t, func(c *Config) { c.MaxSSEPerAccount = 2 })

	for i := range 2 {
		r, cancel := h.openStream(t, "?types=*&closeafter=no&ping=0", nil)
		defer cancel()
		if ev := readEvent(t, r); ev.name != "state" {
			t.Fatalf("stream %d did not start: %q", i, ev.name)
		}
	}
	h.notifier.waitForSubscriber(t, 7)

	// The third is refused, and told when to come back.
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+PathEventSource, nil)
	req.SetBasicAuth("user@example.com", testPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 for a capped account", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("a 429 without Retry-After leaves the client guessing")
	}
}

// TestEventSourceCapReleasedOnDisconnect proves the cap counts LIVE streams:
// a client that goes away frees its slot, or a reconnect loop would lock an
// account out of push permanently.
func TestEventSourceCapReleasedOnDisconnect(t *testing.T) {
	t.Parallel()

	h := newSSEHarness(t, func(c *Config) { c.MaxSSEPerAccount = 1 })

	r, cancel := h.openStream(t, "?types=*&closeafter=no&ping=0", nil)
	if ev := readEvent(t, r); ev.name != "state" {
		t.Fatalf("stream did not start: %q", ev.name)
	}
	h.notifier.waitForSubscriber(t, 7)

	cancel() // the client disconnects

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.notifier.Subscribers(7) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("the subscription was not released when the client disconnected")
}

// TestEventSourceShutdownClosesStream covers W-A4's clean shutdown: when the
// broker closes, an open stream ends by itself.
func TestEventSourceShutdownClosesStream(t *testing.T) {
	t.Parallel()

	h := newSSEHarness(t, nil)
	r, cancel := h.openStream(t, "?types=*&closeafter=no&ping=0", nil)
	defer cancel()

	if ev := readEvent(t, r); ev.name != "state" {
		t.Fatalf("stream did not start: %q", ev.name)
	}
	h.notifier.waitForSubscriber(t, 7)

	// Simulate the broker shutting down: every subscription's channel closes.
	h.notifier.mu.Lock()
	h.notifier.closed = true
	for _, chans := range h.notifier.subs {
		for _, ch := range chans {
			close(ch)
		}
	}
	h.notifier.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := r.ReadString('\n')
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the stream continued after the broker closed")
		}
	case <-time.After(5 * time.Second):
		t.Error("the stream did not end when the broker closed")
	}
}

// TestEventSourceLastEventIDResume covers §7.3's resume contract as this
// server implements it (see eventsource.go): a reconnect with a known id
// immediately receives a fresh state event.
func TestEventSourceLastEventIDResume(t *testing.T) {
	t.Parallel()

	h := newSSEHarness(t, nil)

	r1, cancel1 := h.openStream(t, "?types=*&closeafter=no&ping=0", nil)
	first := readEvent(t, r1)
	cancel1()

	// The world moved on while the client was away.
	h.states.set("mb-9", "em-9", "th-9")

	r2, cancel2 := h.openStream(t, "?types=*&closeafter=no&ping=0",
		map[string]string{"Last-Event-ID": first.id})
	defer cancel2()

	resumed := readEvent(t, r2)
	if resumed.name != "state" {
		t.Fatalf("resumed stream sent %q, want an immediate state event", resumed.name)
	}
	if resumed.id == first.id {
		t.Error("the resumed event carries the stale id; it must encode current state")
	}
	var change struct {
		Changed map[string]map[string]string `json:"changed"`
	}
	if err := json.Unmarshal([]byte(resumed.data), &change); err != nil {
		t.Fatal(err)
	}
	for _, ts := range change.Changed {
		if ts["Email"] != "em-9" {
			t.Errorf("resumed Email state = %q, want the current em-9", ts["Email"])
		}
	}
}

func TestEventSourceRejectsBadQuery(t *testing.T) {
	t.Parallel()

	h := newSSEHarness(t, nil)

	for _, q := range []string{"?closeafter=maybe", "?ping=-1", "?ping=abc"} {
		req, _ := http.NewRequest(http.MethodGet, h.srv.URL+PathEventSource+q, nil)
		req.SetBasicAuth("user@example.com", testPassword)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, resp.StatusCode)
		}
	}
}

// TestEventSourceCORS keeps the J1 guarantee on the new route: a browser
// EventSource on an allowed origin must see the headers.
func TestEventSourceCORS(t *testing.T) {
	t.Parallel()

	h := newSSEHarness(t, nil)
	resp, _, cancel := h.open(t, "?types=*&closeafter=state&ping=0", //nolint:bodyclose // the body is closed by open's t.Cleanup
		map[string]string{"Origin": testOrigin})
	defer cancel()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != testOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, testOrigin)
	}
}

// ---------------------------------------------------------------------------
// unit tests for the query parsing and framing
// ---------------------------------------------------------------------------

func TestParseEventSourceQueryDefaults(t *testing.T) {
	t.Parallel()

	// An unexpanded URI Template (a client that substituted nothing) must not
	// be an error: the session advertises a level-1 template.
	opts, perr := parseEventSourceQuery(map[string][]string{
		"types":      {"{types}"},
		"closeafter": {"{closeafter}"},
		"ping":       {"{ping}"},
	})
	if perr != "" {
		t.Fatalf("unexpanded template rejected: %s", perr)
	}
	if opts.types != nil {
		t.Error("an unexpanded types template must mean everything")
	}
	if opts.closeAfterState {
		t.Error("an unexpanded closeafter template must mean persistent")
	}
	if opts.pingInterval != DefaultPingInterval {
		t.Errorf("ping = %v, want the default %v", opts.pingInterval, DefaultPingInterval)
	}
}

func TestParseEventSourceQueryPing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want time.Duration
	}{
		// §7.3: "If the value is '0', the server MUST NOT send ping events."
		{"0", 0},
		// "servers MUST NOT have a minimum allowed value higher than 30":
		// anything below is clamped UP to exactly 30.
		{"1", 30 * time.Second},
		{"30", 30 * time.Second},
		{"120", 120 * time.Second},
		// "...or a maximum allowed value less than 300": above is clamped
		// DOWN to exactly 300.
		{"300", 300 * time.Second},
		{"9999", 300 * time.Second},
	}
	for _, tc := range cases {
		opts, perr := parseEventSourceQuery(map[string][]string{"ping": {tc.in}})
		if perr != "" {
			t.Errorf("ping=%s rejected: %s", tc.in, perr)
			continue
		}
		if opts.pingInterval != tc.want {
			t.Errorf("ping=%s → %v, want %v", tc.in, opts.pingInterval, tc.want)
		}
	}
}

func TestParseEventSourceQueryTypes(t *testing.T) {
	t.Parallel()

	opts, perr := parseEventSourceQuery(map[string][]string{"types": {"Email,Mailbox"}})
	if perr != "" {
		t.Fatal(perr)
	}
	if !opts.types.wants("Email") || !opts.types.wants("Mailbox") {
		t.Error("the requested types must be wanted")
	}
	if opts.types.wants("Thread") {
		t.Error("an unrequested type must not be pushed (§7.3)")
	}

	// "*" means everything.
	star, perr := parseEventSourceQuery(map[string][]string{"types": {"*"}})
	if perr != "" {
		t.Fatal(perr)
	}
	if !star.types.wants("Thread") {
		t.Error(`types="*" must push every type`)
	}
}

func TestWriteSSEFraming(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	if err := writeSSE(rec, rec, "state", "abc", `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	want := "id: abc\nevent: state\ndata: {\"a\":1}\n\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("framing =\n%q\nwant\n%q", got, want)
	}

	// A ping carries no id (§7.3: "This MUST NOT set a new event id").
	rec2 := httptest.NewRecorder()
	if err := writeSSE(rec2, rec2, "ping", "", `{"interval":30}`); err != nil {
		t.Fatal(err)
	}
	if got := rec2.Body.String(); strings.Contains(got, "id:") {
		t.Errorf("a ping frame carries an id: %q", got)
	}
	if got := rec2.Body.String(); got != "event: ping\ndata: {\"interval\":30}\n\n" {
		t.Errorf("ping framing = %q", got)
	}
}

// TestWriteSSEMultilineData proves a payload with newlines cannot truncate an
// event at the reader.
func TestWriteSSEMultilineData(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	if err := writeSSE(rec, rec, "state", "", "line1\nline2"); err != nil {
		t.Fatal(err)
	}
	want := "event: state\ndata: line1\ndata: line2\n\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("multiline framing = %q, want %q", got, want)
	}
}

func TestStateEventIDIsStable(t *testing.T) {
	t.Parallel()

	states := map[string]string{"Email": "e1", "Mailbox": "m1", "Thread": "t1"}
	first := stateEventID(states)
	// Map iteration order is random; the id must not be.
	for range 50 {
		if got := stateEventID(states); got != first {
			t.Fatalf("stateEventID is not deterministic: %q vs %q", got, first)
		}
	}
	if !strings.Contains(first, "e1") || !strings.Contains(first, "m1") {
		t.Errorf("the id must encode the whole snapshot (§7.3); got %q", first)
	}

	// A different snapshot must produce a different id, or a client could not
	// tell that anything changed.
	other := stateEventID(map[string]string{"Email": "e2", "Mailbox": "m1", "Thread": "t1"})
	if other == first {
		t.Error("a changed state produced an unchanged event id")
	}
}

func TestStateEventIDOmitsFilteredTypes(t *testing.T) {
	t.Parallel()

	id := stateEventID(map[string]string{"Email": "e1"})
	if strings.Contains(id, "Mailbox") || strings.Contains(id, "Thread") {
		t.Errorf("the id names a type that was not pushed: %q", id)
	}
	if !strings.Contains(id, "Email:e1") {
		t.Errorf("id = %q, want it to encode Email:e1", id)
	}
}

// TestSSEDefaultCapMatchesConfig pins the two restatements of the W-A4
// default together. internal/config cannot import this package (the
// dependency runs the other way), so the constant is written twice; this is
// the test that stops them drifting.
func TestSSEDefaultCapMatchesConfig(t *testing.T) {
	t.Parallel()

	if DefaultMaxSSEPerAccount != config.DefaultMaxSSEPerAccount {
		t.Errorf("jmaphttp default %d != config default %d",
			DefaultMaxSSEPerAccount, config.DefaultMaxSSEPerAccount)
	}
}

// TestSSEPingBoundsAreConforming guards the two numbers RFC 8620 §7.3 states
// as hard interoperability limits: "servers MUST NOT have a minimum allowed
// value higher than 30 or a maximum allowed value less than 300."
func TestSSEPingBoundsAreConforming(t *testing.T) {
	t.Parallel()

	if MinPingInterval > 30*time.Second {
		t.Errorf("MinPingInterval = %v, which RFC 8620 §7.3 forbids (max 30s)", MinPingInterval)
	}
	if MaxPingInterval < 300*time.Second {
		t.Errorf("MaxPingInterval = %v, which RFC 8620 §7.3 forbids (min 300s)", MaxPingInterval)
	}
}

// TestWriteTimeoutAllowsLongStreams pins the invariant that makes push
// possible at all.
//
// http.Server's WriteTimeout is absolute — set when the request is read and
// never extended by a successful write — so any non-zero value silently kills
// every EventSource stream at exactly that interval. A short unit test would
// never catch it (the streams here live milliseconds); production would lose
// push after WriteTimeout and it would look like a client bug. See the
// constant's comment for what bounds the connection instead.
func TestWriteTimeoutAllowsLongStreams(t *testing.T) {
	t.Parallel()

	if WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v; a non-zero write deadline caps every "+
			"EventSource stream at that interval (RFC 8620 §7.3 requires a "+
			"long-running response)", WriteTimeout)
	}
	// The request side must still be bounded — that is where the
	// slowloris-style risk actually lives.
	if ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must stay bounded")
	}
	if IdleTimeout <= 0 {
		t.Error("IdleTimeout must stay bounded")
	}
}
