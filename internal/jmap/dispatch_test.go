package jmap

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func testEngine(t *testing.T, extra func(*Registry)) *Engine {
	t.Helper()
	reg := NewRegistry()
	RegisterCore(reg)
	if extra != nil {
		extra(reg)
	}
	return NewEngine(reg, DefaultLimits(), []string{CapCore, CapMail}, nil)
}

func process(t *testing.T, e *Engine, body string) *Response {
	t.Helper()
	resp, rerr := e.Process(context.Background(), []byte(body), "state-0")
	if rerr != nil {
		t.Fatalf("request-level error: %v", rerr)
	}
	return resp
}

func errType(t *testing.T, inv Invocation) string {
	t.Helper()
	if inv.Name != "error" {
		t.Fatalf("response %q is not an error", inv.Name)
	}
	var args struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(inv.Args, &args); err != nil {
		t.Fatal(err)
	}
	return args.Type
}

func TestCoreEchoRoundTripsExactBytes(t *testing.T) {
	e := testEngine(t, nil)
	args := `{"hello":true,"nested":{"n":1e2},"list":[1,2,3]}`
	resp := process(t, e, `{
		"using": ["urn:ietf:params:jmap:core"],
		"methodCalls": [["Core/echo", `+args+`, "c1"]]
	}`)
	if len(resp.MethodResponses) != 1 {
		t.Fatalf("responses: %d", len(resp.MethodResponses))
	}
	got := resp.MethodResponses[0]
	if got.Name != "Core/echo" || got.CallID != "c1" {
		t.Fatalf("got %q/%q", got.Name, got.CallID)
	}
	if string(got.Args) != args {
		t.Fatalf("echo not byte-identical:\n in: %s\nout: %s", args, got.Args)
	}
	if resp.SessionState != "state-0" {
		t.Fatalf("sessionState = %q", resp.SessionState)
	}
}

func TestUnknownMethod(t *testing.T) {
	e := testEngine(t, nil)
	resp := process(t, e, `{
		"using": ["urn:ietf:params:jmap:core"],
		"methodCalls": [["No/suchMethod", {}, "c1"]]
	}`)
	if got := errType(t, resp.MethodResponses[0]); got != "unknownMethod" {
		t.Fatalf("got %s", got)
	}
	if resp.MethodResponses[0].CallID != "c1" {
		t.Fatal("call id not echoed on the error")
	}
}

func TestCapabilityNotInUsingIsUnknownMethod(t *testing.T) {
	// RFC 8620 §1.8/§2: the server MUST behave as though it does not
	// implement anything the request did not opt into -> unknownMethod.
	e := testEngine(t, func(r *Registry) {
		r.Register("Mailbox/get", CapMail, func(context.Context, json.RawMessage) (any, *MethodError) {
			return map[string]any{}, nil
		})
	})
	resp := process(t, e, `{
		"using": ["urn:ietf:params:jmap:core"],
		"methodCalls": [["Mailbox/get", {}, "c1"]]
	}`)
	if got := errType(t, resp.MethodResponses[0]); got != "unknownMethod" {
		t.Fatalf("got %s, want unknownMethod", got)
	}

	// With the capability opted in, the same call succeeds.
	resp = process(t, e, `{
		"using": ["urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"],
		"methodCalls": [["Mailbox/get", {}, "c1"]]
	}`)
	if resp.MethodResponses[0].Name != "Mailbox/get" {
		t.Fatalf("got %s", resp.MethodResponses[0].Name)
	}
}

func TestCoreEchoRequiresCoreCapability(t *testing.T) {
	// Strict reading: Core/echo belongs to urn:ietf:params:jmap:core, and an
	// empty using list opted into nothing.
	e := testEngine(t, nil)
	resp := process(t, e, `{
		"using": [],
		"methodCalls": [["Core/echo", {}, "c1"]]
	}`)
	if got := errType(t, resp.MethodResponses[0]); got != "unknownMethod" {
		t.Fatalf("got %s", got)
	}
}

func TestUnknownCapabilityRejectsRequest(t *testing.T) {
	e := testEngine(t, nil)
	_, rerr := e.Process(context.Background(), []byte(`{
		"using": ["urn:ietf:params:jmap:core", "https://example.com/apis/foobar"],
		"methodCalls": []
	}`), "s")
	if rerr == nil || rerr.Type != ProblemUnknownCapability {
		t.Fatalf("got %v, want unknownCapability", rerr)
	}
}

func TestMaxCallsInRequestEnforcedFromDeclaredValue(t *testing.T) {
	reg := NewRegistry()
	RegisterCore(reg)
	limits := DefaultLimits()
	limits.MaxCallsInRequest = 2
	e := NewEngine(reg, limits, []string{CapCore}, nil)

	calls := `["Core/echo",{},"c1"],["Core/echo",{},"c2"]`
	// At the limit: fine.
	if _, rerr := e.Process(context.Background(),
		[]byte(`{"using":["urn:ietf:params:jmap:core"],"methodCalls":[`+calls+`]}`), "s"); rerr != nil {
		t.Fatalf("at-limit request rejected: %v", rerr)
	}
	// One over: the §3.6.1 limit problem naming maxCallsInRequest.
	_, rerr := e.Process(context.Background(),
		[]byte(`{"using":["urn:ietf:params:jmap:core"],"methodCalls":[`+calls+`,["Core/echo",{},"c3"]]}`), "s")
	if rerr == nil || rerr.Type != ProblemLimit || rerr.Limit != "maxCallsInRequest" {
		t.Fatalf("got %+v", rerr)
	}
}

func TestSequentialProcessingContinuesAfterError(t *testing.T) {
	e := testEngine(t, nil)
	resp := process(t, e, `{
		"using": ["urn:ietf:params:jmap:core"],
		"methodCalls": [
			["Core/echo", {"i":1}, "c1"],
			["No/such", {}, "c2"],
			["Core/echo", {"i":3}, "c3"]
		]
	}`)
	if len(resp.MethodResponses) != 3 {
		t.Fatalf("got %d responses", len(resp.MethodResponses))
	}
	if resp.MethodResponses[0].Name != "Core/echo" ||
		resp.MethodResponses[1].Name != "error" ||
		resp.MethodResponses[2].Name != "Core/echo" {
		t.Fatalf("order/continuation broken: %v", resp.MethodResponses)
	}
}

func TestPanickingHandlerBecomesServerFail(t *testing.T) {
	e := testEngine(t, func(r *Registry) {
		r.Register("Boom/now", CapCore, func(context.Context, json.RawMessage) (any, *MethodError) {
			panic("kaboom")
		})
	})
	resp := process(t, e, `{
		"using": ["urn:ietf:params:jmap:core"],
		"methodCalls": [
			["Boom/now", {}, "c1"],
			["Core/echo", {"alive":true}, "c2"]
		]
	}`)
	if got := errType(t, resp.MethodResponses[0]); got != "serverFail" {
		t.Fatalf("got %s", got)
	}
	// §3.6.2: further method calls processed as normal.
	if resp.MethodResponses[1].Name != "Core/echo" {
		t.Fatal("processing did not continue after the panic")
	}
	// The panic payload must not leak to the wire.
	if strings.Contains(string(resp.MethodResponses[0].Args), "kaboom") {
		t.Fatalf("panic detail leaked: %s", resp.MethodResponses[0].Args)
	}
}

func TestNonObjectHandlerResultIsServerFail(t *testing.T) {
	e := testEngine(t, func(r *Registry) {
		r.Register("Bad/result", CapCore, func(context.Context, json.RawMessage) (any, *MethodError) {
			return []string{"not", "an", "object"}, nil
		})
	})
	resp := process(t, e, `{
		"using": ["urn:ietf:params:jmap:core"],
		"methodCalls": [["Bad/result", {}, "c1"]]
	}`)
	if got := errType(t, resp.MethodResponses[0]); got != "serverFail" {
		t.Fatalf("got %s", got)
	}
}

func TestBackReferenceThroughDispatch(t *testing.T) {
	e := testEngine(t, nil)
	resp := process(t, e, `{
		"using": ["urn:ietf:params:jmap:core"],
		"methodCalls": [
			["Core/echo", {"ids":["a","b"]}, "c1"],
			["Core/echo", {"#got":{"resultOf":"c1","name":"Core/echo","path":"/ids/*"}}, "c2"]
		]
	}`)
	var args map[string]any
	if err := json.Unmarshal(resp.MethodResponses[1].Args, &args); err != nil {
		t.Fatal(err)
	}
	got, ok := args["got"].([]any)
	if !ok || len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("resolved echo = %v", args)
	}
}

func TestCreatedIDsPassThrough(t *testing.T) {
	e := testEngine(t, nil)
	resp := process(t, e, `{
		"using": ["urn:ietf:params:jmap:core"],
		"methodCalls": [],
		"createdIds": {"k1": "real1"}
	}`)
	if resp.CreatedIDs == nil || resp.CreatedIDs["k1"] != "real1" {
		t.Fatalf("createdIds not round-tripped: %v", resp.CreatedIDs)
	}

	resp = process(t, e, `{
		"using": ["urn:ietf:params:jmap:core"],
		"methodCalls": []
	}`)
	if resp.CreatedIDs != nil {
		t.Fatal("createdIds present although the request had none")
	}
}

func TestRegisterPanicsOnMisuse(t *testing.T) {
	reg := NewRegistry()
	RegisterCore(reg)
	for name, fn := range map[string]func(){
		"duplicate": func() { RegisterCore(reg) },
		"empty name": func() {
			reg.Register("", CapCore, func(context.Context, json.RawMessage) (any, *MethodError) { return nil, nil })
		},
		"nil handler": func() { reg.Register("X/y", CapCore, nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			fn()
		}()
	}
}
