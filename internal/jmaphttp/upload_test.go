package jmaphttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// The W3 HTTP surface: the real §6.1 upload endpoint, the submission
// capability's session truth, and the EmailSubmission push type.

// fakeUploader records uploads and scripts outcomes.
type fakeUploader struct {
	mu    sync.Mutex
	blobs [][]byte
	gate  chan struct{} // when non-nil, uploads block until closed
	err   error
}

func (u *fakeUploader) UploadBlob(ctx context.Context, _ int64, r io.Reader) (string, int64, error) {
	u.mu.Lock()
	gate := u.gate
	u.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return "", 0, ctx.Err()
		}
	}
	// The read happens INSIDE the handler's MaxBytesReader wrap, exactly as
	// the real blob.Put streams — which is what lets the 413 path fire.
	b, err := io.ReadAll(r)
	if err != nil {
		return "", 0, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.err != nil {
		return "", 0, u.err
	}
	u.blobs = append(u.blobs, b)
	return strings.Repeat("e", 64), int64(len(b)), nil
}

func uploadServer(t *testing.T, mutate func(*Config)) (*Server, *fakeUploader) {
	t.Helper()
	up := &fakeUploader{}
	s, _, _, _ := newTestServer(t, func(c *Config) {
		c.Uploader = up
		if mutate != nil {
			mutate(c)
		}
	})
	return s, up
}

func uploadPath() string { return "/jmap/upload/" + jmap.EncodeAccountID(testAccount().ID) }

func TestUploadStoresAndAnswersTheSection61Object(t *testing.T) {
	s, up := uploadServer(t, nil)

	w := doReq(s, http.MethodPost, uploadPath(), "%PDF-bytes", true,
		map[string]string{"Content-Type": "application/pdf"})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", w.Code, w.Body)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	// §6.1's four members, exactly.
	if resp["accountId"] != jmap.EncodeAccountID(testAccount().ID) {
		t.Errorf("accountId = %v", resp["accountId"])
	}
	if resp["blobId"] != strings.Repeat("e", 64) {
		t.Errorf("blobId = %v", resp["blobId"])
	}
	if resp["type"] != "application/pdf" {
		t.Errorf("type = %v, want the request Content-Type echoed", resp["type"])
	}
	if resp["size"] != float64(len("%PDF-bytes")) {
		t.Errorf("size = %v", resp["size"])
	}
	if len(up.blobs) != 1 || string(up.blobs[0]) != "%PDF-bytes" {
		t.Errorf("stored blobs = %q", up.blobs)
	}

	// A missing Content-Type degrades to octet-stream rather than guessing.
	w = doReq(s, http.MethodPost, uploadPath(), "raw", true,
		map[string]string{"Content-Type": ""})
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["type"] != "application/octet-stream" {
		t.Errorf("defaulted type = %v", resp["type"])
	}
}

func TestUploadRefusalsAndScoping(t *testing.T) {
	s, up := uploadServer(t, nil)

	// Unauthenticated: the route is inside requireAuth like every other.
	if w := doReq(s, http.MethodPost, uploadPath(), "x", false, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated upload = %d, want 401", w.Code)
	}
	// A foreign accountId is the same 404 as everywhere — never a 403 oracle.
	if w := doReq(s, http.MethodPost, "/jmap/upload/"+jmap.EncodeAccountID(999), "x", true, nil); w.Code != http.StatusNotFound {
		t.Errorf("foreign account upload = %d, want 404", w.Code)
	}
	if len(up.blobs) != 0 {
		t.Error("a refused upload still stored bytes")
	}

	// No uploader wired: the honest 501 phase 1 answered.
	bare, _, _, _ := newTestServer(t, nil)
	if w := doReq(bare, http.MethodPost, uploadPath(), "x", true, nil); w.Code != http.StatusNotImplemented {
		t.Errorf("upload without an uploader = %d, want 501", w.Code)
	}
}

// proveMaxSizeUpload is the REAL enforcement proof that replaced the phase-1
// vacuous one: a body over the declared limit answers the §3.6.1 limit
// problem naming maxSizeUpload, with 413, and stores nothing.
func proveMaxSizeUpload(t *testing.T) {
	s, up := uploadServer(t, func(c *Config) {
		c.Limits = jmap.DefaultLimits()
		c.Limits.MaxSizeUpload = 64
	})

	w := doReq(s, http.MethodPost, uploadPath(), strings.Repeat("x", 65), true, nil)
	assertLimitProblem(t, w, http.StatusRequestEntityTooLarge, "maxSizeUpload")
	if len(up.blobs) != 0 {
		t.Error("an oversized upload was stored")
	}

	// At the limit exactly: accepted (declared == applied, not off by one).
	if w := doReq(s, http.MethodPost, uploadPath(), strings.Repeat("x", 64), true, nil); w.Code != http.StatusCreated {
		t.Errorf("upload at the exact limit = %d, want 201", w.Code)
	}
}

// proveMaxConcurrentUpload holds max uploads open behind a gate and requires
// the next to bounce with 429 naming maxConcurrentUpload.
func proveMaxConcurrentUpload(t *testing.T) {
	gate := make(chan struct{})
	s, up := uploadServer(t, func(c *Config) {
		c.Limits = jmap.DefaultLimits()
		c.Limits.MaxConcurrentUpload = 2
	})
	up.gate = gate

	release := make(chan *httptest.ResponseRecorder, 2)
	started := make(chan struct{}, 2)
	for range 2 {
		go func() {
			r := httptest.NewRequest(http.MethodPost, uploadPath(), strings.NewReader("held"))
			r.SetBasicAuth("user@example.com", testPassword)
			w := httptest.NewRecorder()
			started <- struct{}{}
			s.Handler().ServeHTTP(w, r)
			release <- w
		}()
	}
	<-started
	<-started
	// Wait until BOTH held uploads actually occupy their slots before issuing
	// the third request — issuing it earlier could win a free slot and then
	// block on the test's own gate, deadlocking the test rather than proving
	// the limit. White-box read of the gate, same package.
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.uploadGate.mu.Lock()
		n := s.uploadGate.inFlight["user@example.com"]
		s.uploadGate.mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("held uploads never occupied their slots (in flight: %d)", n)
		}
		time.Sleep(time.Millisecond)
	}
	third := doReq(s, http.MethodPost, uploadPath(), "one too many", true, nil)
	assertLimitProblem(t, third, http.StatusTooManyRequests, "maxConcurrentUpload")

	close(gate)
	if w := <-release; w.Code != http.StatusCreated {
		t.Errorf("held upload = %d, want 201 after release", w.Code)
	}
	<-release
}

// ---------------------------------------------------------------------------
// the submission capability's session truth
// ---------------------------------------------------------------------------

// mustObject is the checked map assertion the session walk uses.
func mustObject(t *testing.T, v any, what string) map[string]any {
	t.Helper()
	out, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want an object", what, v)
	}
	return out
}

func TestSessionAdvertisesSubmissionExactlyWhenMounted(t *testing.T) {
	// Mounted: capability, accountCapabilities and primaryAccounts all say so.
	s, _, _, _ := newTestServer(t, func(c *Config) { c.Submission = true })
	obj := fetchSession(t, s)

	caps := mustObject(t, obj["capabilities"], "capabilities")
	if _, ok := caps[jmap.CapSubmission]; !ok {
		t.Error("session capabilities lack urn:ietf:params:jmap:submission")
	}
	prim := mustObject(t, obj["primaryAccounts"], "primaryAccounts")
	if prim[jmap.CapSubmission] != jmap.EncodeAccountID(testAccount().ID) {
		t.Errorf("primaryAccounts[submission] = %v", prim[jmap.CapSubmission])
	}
	acct := mustObject(t, mustObject(t, obj["accounts"], "accounts")[jmap.EncodeAccountID(testAccount().ID)], "account")
	subCap, ok := acct["accountCapabilities"].(map[string]any)[jmap.CapSubmission].(map[string]any)
	if !ok {
		t.Fatal("account lacks the submission accountCapability")
	}
	// §1.3.2 truth: no client-schedulable delayed send (the W-A3 undo window
	// is a server-side grace, not FUTURERELEASE), no extensions passed
	// through.
	if subCap["maxDelayedSend"] != float64(0) {
		t.Errorf("maxDelayedSend = %v, want 0", subCap["maxDelayedSend"])
	}
	if ext, ok := subCap["submissionExtensions"].(map[string]any); !ok || len(ext) != 0 {
		t.Errorf("submissionExtensions = %v, want {}", subCap["submissionExtensions"])
	}

	// Not mounted: not a word of it — advertised == registered.
	s2, _, _, _ := newTestServer(t, nil)
	obj2 := fetchSession(t, s2)
	if _, ok := mustObject(t, obj2["capabilities"], "capabilities")[jmap.CapSubmission]; ok {
		t.Error("submission advertised on a server that never mounted it")
	}
}

func TestEngineAcceptsSubmissionCapabilityOnlyWhenMounted(t *testing.T) {
	// The "using" gate moves with the session advertisement (one list, used
	// twice): a request opting into submission on an unmounted server is the
	// §3.6.1 unknownCapability problem.
	body := `{"using":["urn:ietf:params:jmap:submission"],"methodCalls":[["Core/echo",{},"0"]]}`

	s, _, _, _ := newTestServer(t, nil)
	if w := doReq(s, http.MethodPost, PathAPI, body, true, nil); w.Code != http.StatusBadRequest {
		t.Errorf("unmounted submission in using = %d, want 400 unknownCapability", w.Code)
	}

	s2, _, _, _ := newTestServer(t, func(c *Config) { c.Submission = true })
	if w := doReq(s2, http.MethodPost, PathAPI, body, true, nil); w.Code != http.StatusOK {
		t.Errorf("mounted submission in using = %d, want 200 (body: %s)", w.Code, w.Body)
	}
}

// ---------------------------------------------------------------------------
// the EmailSubmission push type (W4a + W3)
// ---------------------------------------------------------------------------

// submissionStateReaders extends the empty readers with the optional
// SubmissionStateSource — the shape mail.Adapter has since W3.
type submissionStateReaders struct{ emptyMailReaders }

func (submissionStateReaders) EmailSubmissionState(context.Context, int64) (string, error) {
	return "sub-9", nil
}

func TestEventSourcePushesEmailSubmissionState(t *testing.T) {
	notifier := &manualNotifier{ch: make(chan Notification, 1)}
	s, _, _, _ := newTestServer(t, func(c *Config) {
		c.Notifier = notifier
		c.State = submissionStateReaders{}
	})

	r := httptest.NewRequest(http.MethodGet, "/jmap/eventsource?types=Email,EmailSubmission&closeafter=state&ping=0", nil)
	r.SetBasicAuth("user@example.com", testPassword)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	body := w.Body.String()
	if !strings.Contains(body, `"EmailSubmission":"sub-9"`) {
		t.Errorf("push payload lacks the EmailSubmission state:\n%s", body)
	}
	if !strings.Contains(body, `"Email":"s1"`) {
		t.Errorf("push payload lacks the Email state:\n%s", body)
	}
	// The event id encodes the whole snapshot, submission included, in the
	// fixed order.
	if !strings.Contains(body, "id: Email:s1;EmailSubmission:sub-9") {
		t.Errorf("event id does not encode the EmailSubmission state:\n%s", body)
	}

	// A state source WITHOUT the optional interface (pre-W3 shape) keeps
	// working and simply never pushes the type. A stream filtered to a type
	// the server never pushes stays OPEN and silent (§7.3: "MUST only push
	// changes for the types in this list"), so the request carries its own
	// deadline and the assertion reads what was written by then: nothing.
	s2, _, _, _ := newTestServer(t, func(c *Config) {
		c.Notifier = &manualNotifier{ch: make(chan Notification, 1)}
		c.State = emptyMailReaders{}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	r2 := httptest.NewRequest(http.MethodGet, "/jmap/eventsource?types=EmailSubmission&closeafter=state&ping=0", nil).WithContext(ctx)
	r2.SetBasicAuth("user@example.com", testPassword)
	w2 := httptest.NewRecorder()
	s2.Handler().ServeHTTP(w2, r2)
	if strings.Contains(w2.Body.String(), "EmailSubmission") {
		t.Errorf("a source without submission state pushed one:\n%s", w2.Body)
	}
}

// manualNotifier is a StateNotifier a test drives by hand.
type manualNotifier struct {
	ch chan Notification
}

func (n *manualNotifier) StateEvents(int64) (<-chan Notification, func()) {
	return n.ch, func() {}
}
func (n *manualNotifier) Subscribers(int64) int { return 0 }
