package jmaphttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
)

const testOrigin = "https://webmail.example"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestServer builds a full Server over the auth fakes.
func newTestServer(t *testing.T, mutateCfg func(*Config)) (*Server, *fakeValidator, *fakeDirectory, *fakeClock) {
	t.Helper()
	v := &fakeValidator{valid: map[string]string{"user@example.com": testPassword}}
	d := &fakeDirectory{}
	d.put(testAccount())
	clock := newFakeClock()
	auth, err := newTestAuth(v, d, clock, func(c *AuthConfig) { c.Logger = discardLogger() })
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		AllowedOrigins: []string{testOrigin},
		Logger:         discardLogger(),
	}
	if mutateCfg != nil {
		mutateCfg(&cfg)
	}
	s, err := New(cfg, auth)
	if err != nil {
		t.Fatal(err)
	}
	return s, v, d, clock
}

// doReq runs one request through the complete middleware stack.
func doReq(s *Server, method, path, body string, authed bool, header map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if authed {
		r.SetBasicAuth("user@example.com", testPassword)
	}
	if body != "" && r.Header.Get("Content-Type") == "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, val := range header {
		r.Header.Set(k, val)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func apiBody(calls string) string {
	return `{"using":["urn:ietf:params:jmap:core","urn:ietf:params:jmap:mail"],"methodCalls":[` + calls + `]}`
}

// ---------------------------------------------------------------------------
// Declared == applied: THE limits AC of this epic.
// ---------------------------------------------------------------------------

// TestEveryDeclaredLimitIsApplied iterates every field of jmap.Limits by
// reflection and requires an enforcement proof for each. Adding a limit to
// the struct without adding its enforcement (and its proof here) fails this
// test — that is the point.
func TestEveryDeclaredLimitIsApplied(t *testing.T) {
	proofs := map[string]func(*testing.T){
		"MaxSizeRequest":        proveMaxSizeRequest,
		"MaxCallsInRequest":     proveMaxCallsInRequest,
		"MaxConcurrentRequests": proveMaxConcurrentRequests,
		"MaxObjectsInGet":       proveMaxObjectsInGet,
		"MaxObjectsInSet":       proveMaxObjectsInSet,
		"MaxSizeUpload":         proveUploadLimitsVacuous,
		"MaxConcurrentUpload":   proveUploadLimitsVacuous,
	}

	typ := reflect.TypeOf(jmap.Limits{})
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		proof, ok := proofs[name]
		if !ok {
			t.Errorf("limit %s is declared but has no enforcement proof: declared==applied is violated", name)
			continue
		}
		t.Run(name, proof)
	}
	for name := range proofs {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("proof %s covers a limit that no longer exists", name)
		}
	}
}

func assertLimitProblem(t *testing.T, w *httptest.ResponseRecorder, status int, limit string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, status, w.Body)
	}
	var p map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("problem body: %v", err)
	}
	if p["type"] != string(jmap.ProblemLimit) {
		t.Fatalf("problem type = %v", p["type"])
	}
	if p["limit"] != limit {
		t.Fatalf("problem limit = %v, want %s", p["limit"], limit)
	}
}

func proveMaxSizeRequest(t *testing.T) {
	s, _, _, _ := newTestServer(t, func(c *Config) {
		c.Limits = jmap.DefaultLimits()
		c.Limits.MaxSizeRequest = 120
	})
	// Under the limit: accepted (the empty methodCalls request is ~85 bytes).
	w := doReq(s, http.MethodPost, PathAPI, apiBody(""), true, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("under-limit request: %d (%s)", w.Code, w.Body)
	}
	// Over the limit: the §3.6.1 limit problem naming maxSizeRequest.
	big := apiBody(`["Core/echo",{"pad":"` + strings.Repeat("x", 200) + `"},"c1"]`)
	w = doReq(s, http.MethodPost, PathAPI, big, true, nil)
	assertLimitProblem(t, w, http.StatusRequestEntityTooLarge, "maxSizeRequest")
}

func proveMaxCallsInRequest(t *testing.T) {
	s, _, _, _ := newTestServer(t, func(c *Config) {
		c.Limits = jmap.DefaultLimits()
		c.Limits.MaxCallsInRequest = 1
	})
	w := doReq(s, http.MethodPost, PathAPI, apiBody(`["Core/echo",{},"c1"]`), true, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("at-limit request: %d", w.Code)
	}
	w = doReq(s, http.MethodPost, PathAPI,
		apiBody(`["Core/echo",{},"c1"],["Core/echo",{},"c2"]`), true, nil)
	assertLimitProblem(t, w, http.StatusBadRequest, "maxCallsInRequest")
}

func proveMaxConcurrentRequests(t *testing.T) {
	s, _, _, _ := newTestServer(t, func(c *Config) {
		c.Limits = jmap.DefaultLimits()
		c.Limits.MaxConcurrentRequests = 1
	})

	entered := make(chan struct{})
	release := make(chan struct{})
	s.Registry().Register("Test/block", jmap.CapCore,
		func(ctx context.Context, _ json.RawMessage) (any, *jmap.MethodError) {
			close(entered)
			<-release
			return map[string]any{}, nil
		})

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- doReq(s, http.MethodPost, PathAPI, apiBody(`["Test/block",{},"c1"]`), true, nil)
	}()
	<-entered // the one allowed slot is now occupied

	w := doReq(s, http.MethodPost, PathAPI, apiBody(`["Core/echo",{},"c1"]`), true, nil)
	assertLimitProblem(t, w, http.StatusTooManyRequests, "maxConcurrentRequests")
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}

	close(release)
	if first := <-done; first.Code != http.StatusOK {
		t.Fatalf("blocked request finished with %d", first.Code)
	}
	// The slot is free again.
	w = doReq(s, http.MethodPost, PathAPI, apiBody(`["Core/echo",{},"c1"]`), true, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("post-release request: %d", w.Code)
	}
}

// proveMaxObjectsInGet is the REAL HTTP proof, registered by J2: a genuine
// /get method call over the wire, with more ids than the session advertises,
// answered with requestTooLarge.
//
// Until J2 this proof could only assert that CheckObjectsInGet enforced the
// advertised number, because no /get method existed to call. Now Mailbox/get
// does, so the limit is proved end to end — declared in the session, applied
// to an actual request — which is what the declared==applied AC always meant.
func proveMaxObjectsInGet(t *testing.T) {
	const limit = 3
	s, _, _, _ := newTestServer(t, func(c *Config) {
		c.Limits = jmap.DefaultLimits()
		c.Limits.MaxObjectsInGet = limit
	})
	registerTestMailMethods(t, s)

	ids := func(n int) string {
		parts := make([]string, 0, n)
		for i := 1; i <= n; i++ {
			parts = append(parts, `"`+mail.EncodeMailboxID(int64(i))+`"`)
		}
		return "[" + strings.Join(parts, ",") + "]"
	}

	// At the limit: a normal response, not an error.
	w := doReq(s, http.MethodPost, PathAPI, apiBody(
		`["Mailbox/get",{"accountId":"a7","ids":`+ids(limit)+`},"c1"]`), true, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("at-limit request: %d (%s)", w.Code, w.Body)
	}
	if name := methodResponseName(t, w, 0); name != "Mailbox/get" {
		t.Fatalf("at-limit response name = %q, want Mailbox/get", name)
	}

	// One over: the §5.1 requestTooLarge method error.
	w = doReq(s, http.MethodPost, PathAPI, apiBody(
		`["Mailbox/get",{"accountId":"a7","ids":`+ids(limit+1)+`},"c1"]`), true, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("over-limit request: HTTP %d, want 200 with a method error", w.Code)
	}
	if name := methodResponseName(t, w, 0); name != "error" {
		t.Fatalf("over-limit response name = %q, want error", name)
	}
	if typ := methodErrorType(t, w, 0); typ != string(jmap.CodeRequestTooLarge) {
		t.Fatalf("over-limit error type = %q, want requestTooLarge", typ)
	}

	// And the number enforced is the number advertised.
	if sessionLimit(t, s, "maxObjectsInGet") != int64(limit) {
		t.Fatal("session advertises a different maxObjectsInGet than is enforced")
	}
}

// registerTestMailMethods wires the J2 get-family onto a test server with
// empty readers, so the limit and dispatch paths can be exercised over HTTP
// without a database.
func registerTestMailMethods(t *testing.T, s *Server) {
	t.Helper()
	readers := &emptyMailReaders{}
	mail.RegisterGetMethods(s.Registry(), &mail.Deps{
		Mailboxes: readers,
		Emails:    readers,
		Threads:   readers,
		Blobs:     readers,
		State:     readers,
		Limits:    s.cfg.Limits,
	})
}

// methodResponseName returns the name of the nth method response.
func methodResponseName(t *testing.T, w *httptest.ResponseRecorder, n int) string {
	t.Helper()
	var body struct {
		MethodResponses []json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v (%s)", err, w.Body)
	}
	if n >= len(body.MethodResponses) {
		t.Fatalf("no method response %d in %s", n, w.Body)
	}
	var tuple []json.RawMessage
	if err := json.Unmarshal(body.MethodResponses[n], &tuple); err != nil {
		t.Fatalf("decoding invocation: %v", err)
	}
	var name string
	if err := json.Unmarshal(tuple[0], &name); err != nil {
		t.Fatalf("decoding name: %v", err)
	}
	return name
}

// methodErrorType returns the "type" of the nth method response, which must be
// an error.
func methodErrorType(t *testing.T, w *httptest.ResponseRecorder, n int) string {
	t.Helper()
	var body struct {
		MethodResponses []json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	var tuple []json.RawMessage
	if err := json.Unmarshal(body.MethodResponses[n], &tuple); err != nil {
		t.Fatalf("decoding invocation: %v", err)
	}
	var args struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(tuple[1], &args); err != nil {
		t.Fatalf("decoding error args: %v", err)
	}
	return args.Type
}

func proveMaxObjectsInSet(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	limits := s.cfg.Limits
	merr := limits.CheckObjectsInSet(limits.MaxObjectsInSet + 1)
	if merr == nil || merr.Code != jmap.CodeRequestTooLarge {
		t.Fatalf("over-limit: %v, want requestTooLarge", merr)
	}
	if sessionLimit(t, s, "maxObjectsInSet") != int64(limits.MaxObjectsInSet) {
		t.Fatal("session advertises a different maxObjectsInSet than is enforced")
	}
}

// proveUploadLimitsVacuous: phase 1 accepts NO uploads at all (501), so no
// upload can exceed maxSizeUpload or maxConcurrentUpload — the advertised
// values are vacuously true. The upload epic replaces this proof with real
// body/concurrency checks.
func proveUploadLimitsVacuous(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	w := doReq(s, http.MethodPost, "/jmap/upload/a7", `{"x":1}`, true, nil)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("upload = %d, want 501 (if uploads now exist, these limits need real enforcement proofs)", w.Code)
	}
}

// sessionLimit fetches one advertised core-capability limit over HTTP.
func sessionLimit(t *testing.T, s *Server, name string) int64 {
	t.Helper()
	obj := fetchSession(t, s)
	caps, ok := obj["capabilities"].(map[string]any)
	if !ok {
		t.Fatal("session has no capabilities object")
	}
	core, ok := caps[jmap.CapCore].(map[string]any)
	if !ok {
		t.Fatal("session has no core capability")
	}
	v, ok := core[name].(float64)
	if !ok {
		t.Fatalf("core capability has no numeric %s", name)
	}
	return int64(v)
}

// asObject / asString are checked JSON accessors: a session shape violation
// fails with the offending key named instead of panicking mid-test.
func asObject(t *testing.T, v any, what string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want a JSON object", what, v)
	}
	return m
}

func asString(t *testing.T, v any, what string) string {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%s is %T, want a string", what, v)
	}
	return s
}

func fetchSession(t *testing.T, s *Server) map[string]any {
	t.Helper()
	w := doReq(s, http.MethodGet, PathWellKnown, "", true, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("session fetch: %d (%s)", w.Code, w.Body)
	}
	var obj map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatalf("session JSON: %v", err)
	}
	return obj
}

// ---------------------------------------------------------------------------
// Session object.
// ---------------------------------------------------------------------------

func TestSessionObjectShape(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)

	w := doReq(s, http.MethodGet, PathWellKnown, "", true, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q (RFC 8620 §2 recommends disabling caching)", cc)
	}

	obj := fetchSession(t, s)

	caps := asObject(t, obj["capabilities"], "capabilities")
	if _, ok := caps[jmap.CapCore]; !ok {
		t.Fatal("core capability missing (RFC 8620 §2: MUST be included)")
	}
	if mail, ok := caps[jmap.CapMail].(map[string]any); !ok || len(mail) != 0 {
		t.Fatalf("mail session capability must be an empty object (RFC 8621 §1.3.1), got %v", caps[jmap.CapMail])
	}

	accounts := asObject(t, obj["accounts"], "accounts")
	if len(accounts) != 1 {
		t.Fatalf("session must contain exactly the caller's account, got %d", len(accounts))
	}
	acct := asObject(t, accounts["a7"], "accounts.a7")
	if acct["name"] != "user@example.com" || acct["isPersonal"] != true || acct["isReadOnly"] != true {
		t.Fatalf("account object = %v", acct)
	}
	acctCaps := asObject(t, acct["accountCapabilities"], "accountCapabilities")
	mailCap, ok := acctCaps[jmap.CapMail].(map[string]any)
	if !ok {
		t.Fatal("account lacks the mail accountCapability")
	}
	for _, k := range []string{"maxMailboxesPerEmail", "maxMailboxDepth", "maxSizeMailboxName",
		"maxSizeAttachmentsPerEmail", "emailQuerySortOptions", "mayCreateTopLevelMailbox"} {
		if _, present := mailCap[k]; !present {
			t.Errorf("mail accountCapability lacks %s (RFC 8621 §1.3.1: MUST contain it)", k)
		}
	}
	if mailCap["mayCreateTopLevelMailbox"] != false {
		t.Fatal("a read-only server must not offer mailbox creation")
	}

	// The advertised sort options must be exactly the ones Email/query
	// implements (J1's declared == applied rule, extended to comparators by
	// J3). The handler-side half of this pact is asserted in
	// internal/jmap/mail: TestAdvertisedSortOptionsAreExactlyTheImplementedOnes.
	sorts, ok := mailCap["emailQuerySortOptions"].([]any)
	if !ok {
		t.Fatalf("emailQuerySortOptions = %v, want an array", mailCap["emailQuerySortOptions"])
	}
	advertised := make(map[string]bool, len(sorts))
	for _, s := range sorts {
		name, ok := s.(string)
		if !ok {
			t.Fatalf("emailQuerySortOptions contains %T, want strings", s)
		}
		advertised[name] = true
	}
	// RFC 8621 §4.4.2 makes receivedAt the one sort a server MUST support.
	if !advertised[mail.SortReceivedAt] {
		t.Errorf("emailQuerySortOptions = %v, want it to advertise %q (RFC 8621 §4.4.2 MUST)",
			sorts, mail.SortReceivedAt)
	}
	if !advertised[mail.SortRelevance] {
		t.Errorf("emailQuerySortOptions = %v, want it to advertise %q", sorts, mail.SortRelevance)
	}
	// Nothing may be advertised that Email/query would reject. The §4.4.2
	// SHOULD list is the trap: those names look supportable but need indexes
	// the store does not have.
	for _, unsupported := range []string{"size", "from", "to", "subject", "sentAt", "hasKeyword"} {
		if advertised[unsupported] {
			t.Errorf("emailQuerySortOptions advertises %q, which Email/query refuses with unsupportedSort",
				unsupported)
		}
	}

	// collationAlgorithms stays empty while both sorts compare non-strings
	// (RFC 8620 §5.5: "When the property being compared is not a string, the
	// 'collation' property is ignored"). Advertising one would claim a string
	// ordering this server never performs.
	coreCap := asObject(t, caps[jmap.CapCore], "core capability")
	collations, ok := coreCap["collationAlgorithms"].([]any)
	if !ok {
		t.Fatalf("collationAlgorithms = %v, want an array", coreCap["collationAlgorithms"])
	}
	if len(collations) != 0 {
		t.Errorf("collationAlgorithms = %v, want empty: Email/query refuses every explicit collation",
			collations)
	}

	primary := asObject(t, obj["primaryAccounts"], "primaryAccounts")
	if primary[jmap.CapMail] != "a7" {
		t.Fatalf("primaryAccounts = %v", primary)
	}
	if _, hasCore := primary[jmap.CapCore]; hasCore {
		t.Fatal(`primaryAccounts contains core ("SHOULD NOT be present", RFC 8620 §2)`)
	}

	if obj["username"] != "user@example.com" {
		t.Fatalf("username = %v", obj["username"])
	}
	if u := asString(t, obj["apiUrl"], "apiUrl"); !strings.HasSuffix(u, PathAPI) || !strings.HasPrefix(u, "http") {
		t.Fatalf("apiUrl = %q", u)
	}
	for tmplVar, urlKey := range map[string]string{
		"{accountId}": "downloadUrl", "{blobId}": "downloadUrl", "{name}": "downloadUrl", "{type}": "downloadUrl",
	} {
		if !strings.Contains(asString(t, obj[urlKey], urlKey), tmplVar) {
			t.Errorf("%s lacks the required %s variable (RFC 8620 §2)", urlKey, tmplVar)
		}
	}
	if !strings.Contains(asString(t, obj["uploadUrl"], "uploadUrl"), "{accountId}") {
		t.Error("uploadUrl lacks {accountId}")
	}
	es := asString(t, obj["eventSourceUrl"], "eventSourceUrl")
	for _, v := range []string{"{types}", "{closeafter}", "{ping}"} {
		if !strings.Contains(es, v) {
			t.Errorf("eventSourceUrl lacks %s", v)
		}
	}
	if st, _ := obj["state"].(string); st == "" {
		t.Fatal("session state is empty")
	}
}

func TestSessionStateConsistentWithAPIResponses(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	obj := fetchSession(t, s)

	w := doReq(s, http.MethodPost, PathAPI, apiBody(`["Core/echo",{},"c1"]`), true, nil)
	var resp struct {
		SessionState string `json:"sessionState"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.SessionState != obj["state"] {
		t.Fatalf("API sessionState %q != session state %q (RFC 8620 §3.4: it is the same string)",
			resp.SessionState, obj["state"])
	}
}

// ---------------------------------------------------------------------------
// Routes and stubs.
// ---------------------------------------------------------------------------

func TestAPIEndToEnd(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	w := doReq(s, http.MethodPost, PathAPI, apiBody(`["Core/echo",{"hi":1},"c1"]`), true, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if !strings.Contains(w.Body.String(), `["Core/echo",{"hi":1},"c1"]`) {
		t.Fatalf("echo missing from response: %s", w.Body)
	}
}

func TestAPIRejectsWrongContentType(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	w := doReq(s, http.MethodPost, PathAPI, apiBody(""), true,
		map[string]string{"Content-Type": "text/plain"})
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", w.Code)
	}
	if !strings.Contains(w.Body.String(), string(jmap.ProblemNotJSON)) {
		t.Fatalf("expected the notJSON problem: %s", w.Body)
	}
	// With parameters the type is still acceptable.
	w = doReq(s, http.MethodPost, PathAPI, apiBody(""), true,
		map[string]string{"Content-Type": "application/json; charset=utf-8"})
	if w.Code != http.StatusOK {
		t.Fatalf("charset parameter rejected: %d", w.Code)
	}
}

func TestEveryRouteRequiresAuth(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, PathWellKnown},
		{http.MethodPost, PathAPI},
		{http.MethodGet, "/jmap/download/a7/blob1/x.txt"},
		{http.MethodPost, "/jmap/upload/a7"},
		{http.MethodGet, PathEventSource},
	} {
		w := doReq(s, rt.method, rt.path, "", false, nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated = %d, want 401", rt.method, rt.path, w.Code)
		}
		if w.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s %s: 401 without a Basic challenge", rt.method, rt.path)
		}
	}
}

func TestStubbedRoutes(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)

	w := doReq(s, http.MethodPost, "/jmap/upload/a7", "x", true, nil)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("upload = %d, want 501", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != problemContentType {
		t.Fatalf("upload problem Content-Type = %q", ct)
	}

	w = doReq(s, http.MethodGet, PathEventSource, "", true, nil)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("eventsource = %d, want 501", w.Code)
	}

	// Download on a server with no blob reader wired: every case is a 404, and
	// since J2 they are BYTE-IDENTICAL. The stub used to answer the caller's
	// own account with a message naming the epic, which was already the right
	// authorization shape but a weaker version of it; now a missing blob, a
	// foreign accountId and a malformed one are literally indistinguishable,
	// so there is no existence oracle left to reason about.
	own := doReq(s, http.MethodGet, "/jmap/download/a7/blob1/x.txt", "", true, nil)
	if own.Code != http.StatusNotFound {
		t.Fatalf("own-account download = %d (%s)", own.Code, own.Body)
	}
	for _, path := range []string{"/jmap/download/a999/blob1/x.txt", "/jmap/download/zzz/blob1/x.txt"} {
		w = doReq(s, http.MethodGet, path, "", true, nil)
		if w.Code != own.Code {
			t.Fatalf("%s = %d, want the same %d as the caller's own account", path, w.Code, own.Code)
		}
		if w.Body.String() != own.Body.String() {
			t.Fatalf("%s answers %q but the caller's own account answers %q: the two are distinguishable",
				path, w.Body.String(), own.Body.String())
		}
	}
}

func TestMethodNotAllowed(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	w := doReq(s, http.MethodGet, PathAPI, "", true, nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on the API endpoint = %d, want 405", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Panic recovery.
// ---------------------------------------------------------------------------

func TestDispatchPanicIsServerFailAndServerSurvives(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	s.Registry().Register("Test/panic", jmap.CapCore,
		func(context.Context, json.RawMessage) (any, *jmap.MethodError) {
			panic("method exploded")
		})

	w := doReq(s, http.MethodPost, PathAPI, apiBody(`["Test/panic",{},"c1"]`), true, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (a method panic is a §3.6.2 method error, not an HTTP error)", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"serverFail"`) {
		t.Fatalf("expected serverFail: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "exploded") {
		t.Fatalf("panic detail leaked to the wire: %s", w.Body)
	}

	// The daemon lives on.
	w = doReq(s, http.MethodPost, PathAPI, apiBody(`["Core/echo",{},"c2"]`), true, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("server did not survive the panic: %d", w.Code)
	}
}

func TestTransportPanicRecoversTo500Problem(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	h := s.logMiddleware(s.recoverMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("transport-level bug")
	})))

	for i := range 2 { // twice: the recovery must not be single-use
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("round %d: status = %d, want 500", i, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != problemContentType {
			t.Fatalf("round %d: Content-Type = %q", i, ct)
		}
		if strings.Contains(w.Body.String(), "transport-level bug") {
			t.Fatal("panic detail leaked")
		}
	}
}

// ---------------------------------------------------------------------------
// Auth cache behavior at the HTTP layer.
// ---------------------------------------------------------------------------

func TestCredentialCachePerUserCap(t *testing.T) {
	clock := newFakeClock()
	cache, err := newCredentialCache(DefaultAuthCacheTTL, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	for i := range maxCachedCredentialsPerUser + 3 {
		cache.put("u", fmt.Sprintf("pw%d", i))
	}
	cache.mu.Lock()
	n := len(cache.entries["u"])
	cache.mu.Unlock()
	if n > maxCachedCredentialsPerUser {
		t.Fatalf("bucket grew to %d entries, cap is %d", n, maxCachedCredentialsPerUser)
	}
	// The newest credential must still be cached.
	if !cache.check("u", fmt.Sprintf("pw%d", maxCachedCredentialsPerUser+2)) {
		t.Fatal("latest credential evicted instead of the oldest")
	}
}
