package jmap

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInvocationRoundTrip(t *testing.T) {
	in := []byte(`["Core/echo",{"hello":true,"n":1e2},"c1"]`)
	var inv Invocation
	if err := json.Unmarshal(in, &inv); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if inv.Name != "Core/echo" || inv.CallID != "c1" {
		t.Fatalf("parsed %q / %q", inv.Name, inv.CallID)
	}
	// Args must be the raw bytes, preserved exactly (1e2 stays 1e2).
	if string(inv.Args) != `{"hello":true,"n":1e2}` {
		t.Fatalf("args not preserved: %s", inv.Args)
	}
	out, err := json.Marshal(inv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != string(in) {
		t.Fatalf("round trip changed bytes:\n in: %s\nout: %s", in, out)
	}
}

func TestInvocationUnmarshalRejectsBadShapes(t *testing.T) {
	cases := map[string]string{
		"not an array":       `{"a":1}`,
		"two elements":       `["m",{}]`,
		"four elements":      `["m",{},"c","extra"]`,
		"non-string name":    `[42,{},"c"]`,
		"non-object args":    `["m",[1,2],"c"]`,
		"null args":          `["m",null,"c"]`,
		"string args":        `["m","{}","c"]`,
		"non-string call id": `["m",{},7]`,
	}
	for name, in := range cases {
		var inv Invocation
		if err := json.Unmarshal([]byte(in), &inv); err == nil {
			t.Errorf("%s: %s was accepted", name, in)
		}
	}
}

func TestParseRequestValid(t *testing.T) {
	body := []byte(`{
		"using": ["urn:ietf:params:jmap:core"],
		"methodCalls": [["Core/echo", {"x": 1}, "c0"]],
		"createdIds": {}
	}`)
	req, rerr := ParseRequest(body)
	if rerr != nil {
		t.Fatalf("unexpected error: %v", rerr)
	}
	if len(req.Using) != 1 || req.Using[0] != CapCore {
		t.Fatalf("using = %v", req.Using)
	}
	if len(req.MethodCalls) != 1 || req.MethodCalls[0].Name != "Core/echo" {
		t.Fatalf("methodCalls = %+v", req.MethodCalls)
	}
	// createdIds given as {} must be recorded as present (non-nil).
	if req.CreatedIDs == nil {
		t.Fatal("createdIds {} was parsed as absent")
	}
}

func TestParseRequestErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ProblemType
	}{
		{"invalid JSON", `{`, ProblemNotJSON},
		{"trailing data", `{"using":[],"methodCalls":[]} 42`, ProblemNotJSON},
		{"top-level array", `[]`, ProblemNotJSON}, // decodes into struct? no — mismatched type
		{"missing using", `{"methodCalls":[]}`, ProblemNotRequest},
		{"null using", `{"using":null,"methodCalls":[]}`, ProblemNotRequest},
		{"missing methodCalls", `{"using":[]}`, ProblemNotRequest},
		{"using not strings", `{"using":[1],"methodCalls":[]}`, ProblemNotJSON},
		{"bad invocation tuple", `{"using":[],"methodCalls":[["m",{}]]}`, ProblemNotRequest},
		{"invocation args not object", `{"using":[],"methodCalls":[["m",7,"c"]]}`, ProblemNotRequest},
	}
	for _, tc := range cases {
		_, rerr := ParseRequest([]byte(tc.body))
		if rerr == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		// "using not strings" and "top-level array" are type mismatches Go's
		// decoder reports during decode; both notJSON and notRequest are
		// defensible there. Assert only that the request is rejected with one
		// of the two 400-class problems, but pin the primary cases exactly.
		switch tc.name {
		case "using not strings", "top-level array":
			if rerr.Type != ProblemNotJSON && rerr.Type != ProblemNotRequest {
				t.Errorf("%s: got %s", tc.name, rerr.Type)
			}
		default:
			if rerr.Type != tc.want {
				t.Errorf("%s: got %s, want %s", tc.name, rerr.Type, tc.want)
			}
		}
		if rerr.Status != 400 {
			t.Errorf("%s: status %d, want 400", tc.name, rerr.Status)
		}
	}
}

func TestParseRequestRejectsInvalidUTF8(t *testing.T) {
	body := []byte(`{"using":[],"methodCalls":[],"x":"ab` + "\xff" + `"}`)
	_, rerr := ParseRequest(body)
	if rerr == nil || rerr.Type != ProblemNotJSON {
		t.Fatalf("invalid UTF-8 accepted or misclassified: %v", rerr)
	}
}

func TestResponseMarshalCreatedIDs(t *testing.T) {
	// Absent in request -> absent in response.
	out, err := json.Marshal(Response{SessionState: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "createdIds") {
		t.Fatalf("createdIds present when request had none: %s", out)
	}
	if !strings.Contains(string(out), `"methodResponses":[]`) {
		t.Fatalf("methodResponses must be [] never null: %s", out)
	}

	// Given as empty map -> MUST come back (RFC 8620 §3.4), even though empty.
	out, err = json.Marshal(Response{CreatedIDs: map[string]string{}, SessionState: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"createdIds":{}`) {
		t.Fatalf("empty createdIds dropped: %s", out)
	}
}
