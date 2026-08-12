package jmap

import (
	"encoding/json"
	"testing"
)

func TestEveryRegisteredCodeSerializes(t *testing.T) {
	for code := range errorCodes {
		inv := NewMethodError(code).Invocation("c1")
		if inv.Name != "error" {
			t.Fatalf("%s: response name %q, want error", code, inv.Name)
		}
		if inv.CallID != "c1" {
			t.Fatalf("%s: call id not echoed", code)
		}
		var args map[string]any
		if err := json.Unmarshal(inv.Args, &args); err != nil {
			t.Fatalf("%s: args: %v", code, err)
		}
		if args["type"] != string(code) {
			t.Fatalf("%s: wire type %v", code, args["type"])
		}
	}
}

func TestUnregisteredCodeDegradesToServerFail(t *testing.T) {
	e := NewMethodError(ErrorCode("madeUpError"))
	if e.Code != CodeServerFail {
		t.Fatalf("got %s, want serverFail", e.Code)
	}
	if e.Description == "" {
		t.Fatal("the degraded error must say which code was unregistered")
	}
}

func TestMethodErrorDescriptionAndProperties(t *testing.T) {
	e := NewMethodError(CodeInvalidArguments).WithDescription("bad %s", "ids")
	e.Properties = map[string]any{"extra": 1}
	inv := e.Invocation("c9")
	var args map[string]any
	if err := json.Unmarshal(inv.Args, &args); err != nil {
		t.Fatal(err)
	}
	if args["description"] != "bad ids" {
		t.Fatalf("description = %v", args["description"])
	}
	if args["extra"] != float64(1) {
		t.Fatalf("extra property lost: %v", args)
	}
}

func TestRequestErrorMarshal(t *testing.T) {
	e := NewLimitError(413, "maxSizeRequest", "too big")
	out, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	if body["type"] != string(ProblemLimit) {
		t.Fatalf("type = %v", body["type"])
	}
	if body["limit"] != "maxSizeRequest" {
		t.Fatalf("limit = %v (RFC 8620 §3.6.1 requires it)", body["limit"])
	}
	if body["status"] != float64(413) {
		t.Fatalf("status = %v", body["status"])
	}

	// Non-limit problems must not carry a limit member.
	out, err = json.Marshal(NewRequestError(ProblemNotJSON, "nope"))
	if err != nil {
		t.Fatal(err)
	}
	var body2 map[string]any
	if err := json.Unmarshal(out, &body2); err != nil {
		t.Fatal(err)
	}
	if _, has := body2["limit"]; has {
		t.Fatalf("notJSON problem has a limit member: %s", out)
	}
}
