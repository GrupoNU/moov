package jmap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Invocation is the JMAP method call / response tuple (RFC 8620 §3.2):
// a JSON array of exactly three elements — a method (or response) name, an
// arguments object, and an arbitrary client-chosen method call id that the
// server echoes back.
type Invocation struct {
	// Name is the method name in a request, or the response name in a
	// response. RFC 8620 §3.4: "Unless otherwise specified, if the method call
	// completed successfully, its response name is the same as the method name
	// in the request"; an error response's name is "error" (§3.6.2).
	Name string

	// Args is the arguments object, kept as raw JSON. Raw on purpose: it lets
	// Core/echo return byte-identical arguments, spares every method a double
	// decode, and is what back-reference resolution rewrites when a "#"
	// argument is present.
	Args json.RawMessage

	// CallID is the method call id. Every response initiated by this call
	// carries the same id (§3.2).
	CallID string
}

// MarshalJSON renders the invocation as the three-element JSON array of
// RFC 8620 §3.2.
func (inv Invocation) MarshalJSON() ([]byte, error) {
	args := inv.Args
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	return json.Marshal([]any{inv.Name, args, inv.CallID})
}

// UnmarshalJSON parses the three-element array form. It is strict about the
// tuple shape — anything else fails the Request object's type signature and
// must surface as a notRequest error (RFC 8620 §3.6.1).
func (inv *Invocation) UnmarshalJSON(data []byte) error {
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return fmt.Errorf("invocation is not a JSON array: %w", err)
	}
	if len(parts) != 3 {
		return fmt.Errorf("invocation must have exactly 3 elements, got %d", len(parts))
	}
	if err := json.Unmarshal(parts[0], &inv.Name); err != nil {
		return fmt.Errorf("invocation name is not a string: %w", err)
	}
	// The second element's type signature is "String[*]" — an object
	// (RFC 8620 §3.2). Reject anything else here rather than letting a method
	// handler fail on it later with a less precise error.
	if !isJSONObject(parts[1]) {
		return errors.New("invocation arguments must be a JSON object")
	}
	inv.Args = parts[1]
	if err := json.Unmarshal(parts[2], &inv.CallID); err != nil {
		return fmt.Errorf("invocation call id is not a string: %w", err)
	}
	return nil
}

// isJSONObject reports whether raw's first significant byte opens an object.
func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// Request is the JMAP Request object (RFC 8620 §3.3).
type Request struct {
	// Using is the set of capabilities the client opts into. Methods belonging
	// to a capability not present here are treated as not implemented — see
	// Engine.Process for the RFC citation.
	Using []string

	// MethodCalls are processed sequentially, in order (§3.3).
	MethodCalls []Invocation

	// CreatedIDs is the optional creation-id map ("createdIds" on the wire).
	// nil means the property was absent; a non-nil (possibly empty) map means
	// it was given, and per §3.4 the Response must then include it too.
	CreatedIDs map[string]string
}

// ParseRequest decodes and validates a Request body.
//
// It distinguishes the two request-level failure classes of RFC 8620 §3.6.1:
// notJSON when the body is not valid (I-)JSON, and notRequest when it is JSON
// but does not match the Request object's type signature.
//
// Strictness decisions, each per the RFC:
//
//   - The body must be valid UTF-8: I-JSON (RFC 7493 §2.1) requires it, and
//     §3.6.1 makes "did not parse as I-JSON" a notJSON error.
//   - Exactly one top-level JSON value is accepted: §3.1 says the request
//     consists of "a single JSON-encoded Request object". Trailing data is
//     notJSON.
//   - "using" and "methodCalls" are required with their exact types. A missing
//     or null property fails the type signature ("String[]" / "Invocation[]"
//     admit neither absence nor null, §3.3) and is notRequest.
//   - Duplicate object keys are NOT rejected (Go's decoder keeps the last
//     value). I-JSON forbids them, so a fully strict parser would return
//     notJSON; detecting them requires a token-level rescan of every request
//     body, which is not worth the cost for a fault no real client emits.
//     Documented as the one place this parser is more lenient than I-JSON.
func ParseRequest(body []byte) (*Request, *RequestError) {
	if !utf8.Valid(body) {
		return nil, NewRequestError(ProblemNotJSON, "request body is not valid UTF-8")
	}

	var shape struct {
		Using       *[]string          `json:"using"`
		MethodCalls *[]json.RawMessage `json:"methodCalls"`
		CreatedIDs  map[string]string  `json:"createdIds"`
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&shape); err != nil {
		return nil, NewRequestError(ProblemNotJSON, "request body did not parse as JSON: "+err.Error())
	}
	// A second decode must hit EOF; anything else is trailing data.
	if dec.More() {
		return nil, NewRequestError(ProblemNotJSON, "request body contains more than one JSON value")
	}

	if shape.Using == nil {
		return nil, NewRequestError(ProblemNotRequest, `the "using" property is missing or null`)
	}
	if shape.MethodCalls == nil {
		return nil, NewRequestError(ProblemNotRequest, `the "methodCalls" property is missing or null`)
	}

	req := &Request{
		Using:      *shape.Using,
		CreatedIDs: shape.CreatedIDs,
	}
	req.MethodCalls = make([]Invocation, 0, len(*shape.MethodCalls))
	for i, raw := range *shape.MethodCalls {
		var inv Invocation
		if err := json.Unmarshal(raw, &inv); err != nil {
			return nil, NewRequestError(ProblemNotRequest,
				fmt.Sprintf("methodCalls[%d]: %v", i, err))
		}
		req.MethodCalls = append(req.MethodCalls, inv)
	}
	return req, nil
}

// Response is the JMAP Response object (RFC 8620 §3.4).
type Response struct {
	// MethodResponses holds the outputs in the order the methods were
	// processed (§3.4).
	MethodResponses []Invocation

	// CreatedIDs is returned if and only if the request included a createdIds
	// property (§3.4: "optional; only returned if given in the request").
	// Phase 1 has no /set methods, so it is a pass-through of the request's
	// map; proxies rely on the round trip (§5.8).
	CreatedIDs map[string]string

	// SessionState is the current value of the Session object's "state"
	// string (§3.4), so clients can detect a stale session without refetching.
	SessionState string
}

// MarshalJSON renders the Response object.
//
// It is hand-rolled for two wire-level guarantees encoding/json's struct tags
// cannot give: methodResponses is always a JSON array (never null), and
// createdIds appears exactly when the request supplied it — including as an
// EMPTY object, which `omitempty` would silently drop, violating §3.4's
// "MUST include all creation ids passed in the original createdIds parameter".
func (r Response) MarshalJSON() ([]byte, error) {
	responses := r.MethodResponses
	if responses == nil {
		responses = []Invocation{}
	}
	if r.CreatedIDs == nil {
		return json.Marshal(struct {
			MethodResponses []Invocation `json:"methodResponses"`
			SessionState    string       `json:"sessionState"`
		}{responses, r.SessionState})
	}
	return json.Marshal(struct {
		MethodResponses []Invocation      `json:"methodResponses"`
		CreatedIDs      map[string]string `json:"createdIds"`
		SessionState    string            `json:"sessionState"`
	}{responses, r.CreatedIDs, r.SessionState})
}
