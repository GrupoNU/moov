package jmap

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Back-reference resolution (RFC 8620 §3.7): an argument whose name is
// prefixed with "#" takes its value from the result of a previous method call
// in the same request, selected by a ResultReference and a JSON Pointer.

// ResultReference is the value of a "#"-prefixed argument (RFC 8620 §3.7).
type ResultReference struct {
	// ResultOf is the method call id of a previous method call in the current
	// request.
	ResultOf string `json:"resultOf"`

	// Name is the required name of a response to that method call.
	Name string `json:"name"`

	// Path is a JSON Pointer (RFC 6901) into the arguments of the selected
	// response, extended with "*" to map through an array (§3.7).
	Path string `json:"path"`
}

// ResolveBackReferences returns args with every "#"-prefixed argument replaced
// by its resolved value, per the algorithm of RFC 8620 §3.7.
//
// prior is the methodResponses accumulated so far, in processing order —
// including "error" responses, which can never satisfy a reference because
// their response name is "error", not the referenced method's name (§3.6.2 +
// §3.7 step 2).
//
// Errors follow the RFC exactly:
//
//   - "If any result reference fails to resolve, the whole method MUST be
//     rejected with an 'invalidResultReference' error." That covers a missing
//     resultOf, a response name mismatch, an unevaluable path, and a value
//     that is not a well-formed ResultReference object — the last one is a
//     reference that cannot possibly resolve, and §3.7 provides no more
//     specific error for it.
//   - "If an arguments object contains the same argument name in normal and
//     referenced form (e.g., 'foo' and '#foo'), the method MUST return an
//     'invalidArguments' error."
//
// When args contains no "#" argument it is returned unchanged, byte for byte
// — this is what lets Core/echo round-trip its input exactly.
func ResolveBackReferences(args json.RawMessage, prior []Invocation) (json.RawMessage, *MethodError) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil {
		// The Invocation parser already guaranteed an object; an unparseable
		// one here is a malformed argument set.
		return nil, NewMethodError(CodeInvalidArguments).
			WithDescription("arguments are not a valid JSON object: %v", err)
	}

	refNames := make([]string, 0, 1)
	for name := range fields {
		if strings.HasPrefix(name, "#") {
			refNames = append(refNames, name)
		}
	}
	if len(refNames) == 0 {
		return args, nil
	}

	for _, refName := range refNames {
		plain := strings.TrimPrefix(refName, "#")
		if _, dup := fields[plain]; dup {
			return nil, NewMethodError(CodeInvalidArguments).
				WithDescription("argument %q is present in both normal and referenced form", plain)
		}

		var ref ResultReference
		if err := json.Unmarshal(fields[refName], &ref); err != nil {
			return nil, NewMethodError(CodeInvalidResultReference).
				WithDescription("argument %q is not a ResultReference object: %v", refName, err)
		}

		value, merr := resolveReference(ref, prior)
		if merr != nil {
			return nil, merr
		}

		raw, err := json.Marshal(value)
		if err != nil {
			return nil, NewMethodError(CodeServerFail).
				WithDescription("marshaling resolved reference %q: %v", refName, err)
		}
		delete(fields, refName)
		fields[plain] = raw
	}

	out, err := json.Marshal(fields)
	if err != nil {
		return nil, NewMethodError(CodeServerFail).
			WithDescription("rebuilding arguments after reference resolution: %v", err)
	}
	return out, nil
}

// resolveReference runs the three-step resolution of §3.7.
func resolveReference(ref ResultReference, prior []Invocation) (any, *MethodError) {
	// Step 1: "Find the first response with a method call id identical to the
	// 'resultOf' property ... in the 'methodResponses' array from previously
	// processed method calls in the same request. If none, evaluation fails."
	// First, not last: a method call may emit several responses under one id.
	var found *Invocation
	for i := range prior {
		if prior[i].CallID == ref.ResultOf {
			found = &prior[i]
			break
		}
	}
	if found == nil {
		return nil, NewMethodError(CodeInvalidResultReference).
			WithDescription("no previous method response has call id %q", ref.ResultOf)
	}

	// Step 2: "If the response name is not identical to the 'name' property
	// of the ResultReference, evaluation fails."
	if found.Name != ref.Name {
		return nil, NewMethodError(CodeInvalidResultReference).
			WithDescription("response for call id %q is %q, not %q", ref.ResultOf, found.Name, ref.Name)
	}

	// Step 3: apply the path to the response's arguments object.
	var doc any
	if err := json.Unmarshal(found.Args, &doc); err != nil {
		return nil, NewMethodError(CodeServerFail).
			WithDescription("decoding response arguments for call id %q: %v", ref.ResultOf, err)
	}
	value, err := evalPointer(doc, ref.Path)
	if err != nil {
		return nil, NewMethodError(CodeInvalidResultReference).
			WithDescription("path %q: %v", ref.Path, err)
	}
	return value, nil
}

// evalPointer evaluates a JSON Pointer (RFC 6901) against doc, with the
// RFC 8620 §3.7 extension: when the current value is an array, the token "*"
// applies the remaining tokens to every element and returns the results in
// order in a new array; a per-element result that is itself an array
// contributes its elements rather than the array (one level of flattening).
func evalPointer(doc any, pointer string) (any, error) {
	if pointer == "" {
		// RFC 6901 §5: the empty string references the whole document.
		return doc, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("pointer must be empty or start with %q", "/")
	}
	tokens := strings.Split(pointer[1:], "/")
	for i, t := range tokens {
		tokens[i] = unescapeToken(t)
	}
	return evalTokens(doc, tokens)
}

// unescapeToken applies RFC 6901 §4: "~1" to "/" first, then "~0" to "~" —
// this order is what keeps "~01" decoding to "~1" rather than "/".
func unescapeToken(t string) string {
	t = strings.ReplaceAll(t, "~1", "/")
	return strings.ReplaceAll(t, "~0", "~")
}

func evalTokens(doc any, tokens []string) (any, error) {
	if len(tokens) == 0 {
		return doc, nil
	}
	token, rest := tokens[0], tokens[1:]

	switch node := doc.(type) {
	case map[string]any:
		child, ok := node[token]
		if !ok {
			return nil, fmt.Errorf("object has no member %q", token)
		}
		return evalTokens(child, rest)

	case []any:
		if token == "*" {
			out := make([]any, 0, len(node))
			for _, item := range node {
				value, err := evalTokens(item, rest)
				if err != nil {
					return nil, err
				}
				// §3.7: flatten one level when the per-item result is itself
				// an array.
				if arr, isArr := value.([]any); isArr {
					out = append(out, arr...)
				} else {
					out = append(out, value)
				}
			}
			return out, nil
		}
		idx, err := arrayIndex(token, len(node))
		if err != nil {
			return nil, err
		}
		return evalTokens(node[idx], rest)

	default:
		return nil, fmt.Errorf("cannot descend into non-container value with token %q", token)
	}
}

// arrayIndex parses an RFC 6901 array index: decimal digits without a leading
// zero (except "0" itself). "-" — the past-the-end marker — never references
// an existing value and always fails here.
func arrayIndex(token string, length int) (int, error) {
	if token == "-" {
		return 0, fmt.Errorf("index %q references past the end of the array", token)
	}
	if token == "" || (len(token) > 1 && token[0] == '0') {
		return 0, fmt.Errorf("invalid array index %q", token)
	}
	idx, err := strconv.Atoi(token)
	if err != nil || idx < 0 {
		return 0, fmt.Errorf("invalid array index %q", token)
	}
	if idx >= length {
		return 0, fmt.Errorf("array index %d out of bounds (length %d)", idx, length)
	}
	return idx, nil
}
