package jmap

import (
	"encoding/json"
	"reflect"
	"testing"
)

// prior builds a methodResponses slice from (name, argsJSON, callID) triples.
func priorResponses(t *testing.T, triples ...[3]string) []Invocation {
	t.Helper()
	out := make([]Invocation, 0, len(triples))
	for _, tr := range triples {
		out = append(out, Invocation{Name: tr[0], Args: json.RawMessage(tr[1]), CallID: tr[2]})
	}
	return out
}

func resolveInto(t *testing.T, args string, prior []Invocation) map[string]any {
	t.Helper()
	raw, merr := ResolveBackReferences(json.RawMessage(args), prior)
	if merr != nil {
		t.Fatalf("resolution failed: %v", merr)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("resolved args are not an object: %v", err)
	}
	return out
}

func wantResolutionError(t *testing.T, args string, prior []Invocation, code ErrorCode) *MethodError {
	t.Helper()
	_, merr := ResolveBackReferences(json.RawMessage(args), prior)
	if merr == nil {
		t.Fatalf("resolution of %s unexpectedly succeeded", args)
	}
	if merr.Code != code {
		t.Fatalf("got %s (%s), want %s", merr.Code, merr.Description, code)
	}
	return merr
}

func TestBackrefUntouchedWithoutReferences(t *testing.T) {
	in := `{"accountId":"a1","ids":["x"],"n":1e3}`
	out, merr := ResolveBackReferences(json.RawMessage(in), nil)
	if merr != nil {
		t.Fatal(merr)
	}
	if string(out) != in {
		t.Fatalf("args without references must pass through byte-identical:\n in: %s\nout: %s", in, out)
	}
}

// TestBackrefRFCExample is the worked example of RFC 8620 §3.7 verbatim:
// Foo/changes feeding /created into Foo/get's ids.
func TestBackrefRFCExample(t *testing.T) {
	prior := priorResponses(t, [3]string{"Foo/changes", `{
		"accountId": "A1",
		"oldState": "abcdef",
		"newState": "123456",
		"hasMoreChanges": false,
		"created": [ "f1", "f4" ],
		"updated": [],
		"destroyed": []
	}`, "t0"})

	got := resolveInto(t, `{
		"accountId": "A1",
		"#ids": { "resultOf": "t0", "name": "Foo/changes", "path": "/created" }
	}`, prior)

	want := map[string]any{"accountId": "A1", "ids": []any{"f1", "f4"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBackrefStarFlattening(t *testing.T) {
	// The §3.7 mail example shape: /ids/* over a list of objects each
	// carrying an array — results must flatten one level.
	prior := priorResponses(t, [3]string{"Thread/get", `{
		"list": [
			{ "id": "T1", "emailIds": [ "E1", "E2" ] },
			{ "id": "T2", "emailIds": [ "E3" ] },
			{ "id": "T3", "emailIds": [] }
		]
	}`, "t1"})

	got := resolveInto(t, `{
		"#ids": { "resultOf": "t1", "name": "Thread/get", "path": "/list/*/emailIds" }
	}`, prior)

	want := []any{"E1", "E2", "E3"}
	if !reflect.DeepEqual(got["ids"], want) {
		t.Fatalf("got %v, want %v", got["ids"], want)
	}
}

func TestBackrefNestedStarChains(t *testing.T) {
	prior := priorResponses(t, [3]string{"X/q", `{
		"groups": [
			{ "members": [ {"tags": ["a","b"]}, {"tags": []} ] },
			{ "members": [ {"tags": ["c"]} ] }
		]
	}`, "q1"})

	got := resolveInto(t, `{
		"#tags": { "resultOf": "q1", "name": "X/q", "path": "/groups/*/members/*/tags" }
	}`, prior)

	// Two levels of * with flattening at each level.
	want := []any{"a", "b", "c"}
	if !reflect.DeepEqual(got["tags"], want) {
		t.Fatalf("got %v, want %v", got["tags"], want)
	}
}

func TestBackrefMultiStepChain(t *testing.T) {
	// c0 -> c1 -> c2: each call references the previous one's output.
	prior := priorResponses(t,
		[3]string{"A/one", `{"ids":["x1","x2"]}`, "c0"},
		[3]string{"A/two", `{"list":[{"ref":"y1"},{"ref":"y2"}]}`, "c1"},
	)
	got := resolveInto(t, `{
		"#first": { "resultOf": "c0", "name": "A/one", "path": "/ids/0" },
		"#refs":  { "resultOf": "c1", "name": "A/two", "path": "/list/*/ref" }
	}`, prior)
	if got["first"] != "x1" {
		t.Fatalf("first = %v", got["first"])
	}
	if !reflect.DeepEqual(got["refs"], []any{"y1", "y2"}) {
		t.Fatalf("refs = %v", got["refs"])
	}
}

func TestBackrefMixedLiteralAndReference(t *testing.T) {
	prior := priorResponses(t, [3]string{"A/one", `{"ids":["x"]}`, "c0"})
	got := resolveInto(t, `{
		"accountId": "A1",
		"literal": 42,
		"#ids": { "resultOf": "c0", "name": "A/one", "path": "/ids" }
	}`, prior)
	if got["accountId"] != "A1" || got["literal"] != float64(42) {
		t.Fatalf("literal arguments disturbed: %v", got)
	}
	if !reflect.DeepEqual(got["ids"], []any{"x"}) {
		t.Fatalf("ids = %v", got["ids"])
	}
}

func TestBackrefForwardReferenceFails(t *testing.T) {
	// Referencing a call id that has not been processed yet: at resolution
	// time it is simply absent from methodResponses -> invalidResultReference
	// (§3.7 step 1: "previously processed method calls").
	_ = wantResolutionError(t, `{"#ids":{"resultOf":"later","name":"A/one","path":"/ids"}}`,
		nil, CodeInvalidResultReference)
}

func TestBackrefNameMismatchFails(t *testing.T) {
	prior := priorResponses(t, [3]string{"A/one", `{"ids":[]}`, "c0"})
	_ = wantResolutionError(t, `{"#ids":{"resultOf":"c0","name":"A/two","path":"/ids"}}`,
		prior, CodeInvalidResultReference)
}

func TestBackrefErrorResponseNeverResolves(t *testing.T) {
	// A failed call's response is named "error" (§3.6.2); a reference to it
	// fails the §3.7 step-2 name check.
	prior := priorResponses(t, [3]string{"error", `{"type":"serverFail"}`, "c0"})
	_ = wantResolutionError(t, `{"#ids":{"resultOf":"c0","name":"A/one","path":"/ids"}}`,
		prior, CodeInvalidResultReference)
}

func TestBackrefUnresolvablePaths(t *testing.T) {
	prior := priorResponses(t, [3]string{"A/one", `{"ids":["x1","x2"],"meta":{"n":1}}`, "c0"})
	for name, path := range map[string]string{
		"missing member":        "/nope",
		"index out of bounds":   "/ids/2",
		"negative index":        "/ids/-1",
		"leading zero index":    "/ids/01",
		"past-the-end dash":     "/ids/-",
		"descend into scalar":   "/meta/n/deeper",
		"star over non-array":   "/meta/*",
		"no leading slash":      "ids",
		"index into object":     "/0",
		"member on array":       "/ids/x",
		"trailing empty member": "/meta/",
	} {
		merr := wantResolutionError(t,
			`{"#v":{"resultOf":"c0","name":"A/one","path":"`+path+`"}}`,
			prior, CodeInvalidResultReference)
		if merr.Description == "" {
			t.Errorf("%s: no description on the error", name)
		}
	}
}

func TestBackrefEmptyPointerSelectsWholeArguments(t *testing.T) {
	// RFC 6901 §5: "" references the whole document (here: the arguments
	// object of the referenced response).
	prior := priorResponses(t, [3]string{"A/one", `{"ids":["x"]}`, "c0"})
	got := resolveInto(t, `{"#all":{"resultOf":"c0","name":"A/one","path":""}}`, prior)
	want := map[string]any{"ids": []any{"x"}}
	if !reflect.DeepEqual(got["all"], want) {
		t.Fatalf("got %v", got["all"])
	}
}

func TestBackrefEscapedTokens(t *testing.T) {
	// RFC 6901 §4 escaping: ~1 -> "/" then ~0 -> "~"; "~01" must decode to
	// "~1", never "/".
	prior := priorResponses(t, [3]string{"A/one",
		`{"a/b":1,"m~n":2,"~1":3}`, "c0"})
	got := resolveInto(t, `{
		"#x": {"resultOf":"c0","name":"A/one","path":"/a~1b"},
		"#y": {"resultOf":"c0","name":"A/one","path":"/m~0n"},
		"#z": {"resultOf":"c0","name":"A/one","path":"/~01"}
	}`, prior)
	if got["x"] != float64(1) || got["y"] != float64(2) || got["z"] != float64(3) {
		t.Fatalf("escaping broken: %v", got)
	}
}

func TestBackrefDuplicateNormalAndReferencedForm(t *testing.T) {
	prior := priorResponses(t, [3]string{"A/one", `{"ids":["x"]}`, "c0"})
	// §3.7: "foo" and "#foo" together -> invalidArguments, not
	// invalidResultReference.
	_ = wantResolutionError(t,
		`{"ids":[],"#ids":{"resultOf":"c0","name":"A/one","path":"/ids"}}`,
		prior, CodeInvalidArguments)
}

func TestBackrefMalformedReferenceObject(t *testing.T) {
	prior := priorResponses(t, [3]string{"A/one", `{"ids":["x"]}`, "c0"})
	// A "#" argument whose value is not a ResultReference object can never
	// resolve -> invalidResultReference (see ResolveBackReferences doc).
	_ = wantResolutionError(t, `{"#ids": 42}`, prior, CodeInvalidResultReference)
	_ = wantResolutionError(t, `{"#ids": [1,2]}`, prior, CodeInvalidResultReference)
}

func TestBackrefFirstMatchingResponseWins(t *testing.T) {
	// A single call id may own several responses (§3.2); §3.7 step 1 says
	// "find the FIRST response" with the id.
	prior := priorResponses(t,
		[3]string{"A/one", `{"v":"first"}`, "c0"},
		[3]string{"A/one", `{"v":"second"}`, "c0"},
	)
	got := resolveInto(t, `{"#v":{"resultOf":"c0","name":"A/one","path":"/v"}}`, prior)
	if got["v"] != "first" {
		t.Fatalf("got %v, want the first response's value", got["v"])
	}
}
