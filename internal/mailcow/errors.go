package mailcow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The F0 error contract (docs/briefs/2026-09-15-vpsmail-nota-dominio-y-mailcow-errores.md
// §3), verified against Mailcow 2026-07a. In one sentence: practically every
// failure arrives inside an HTTP 200, in one of TWO families, and the client
// must read the body to know what happened.
//
//   - type "error"  — transport/authentication, a single object:
//     {"type":"error","msg":"authentication failed"} or
//     {"type":"error","msg":"api access denied for ip 203.0.113.9"}.
//     The two are DIFFERENT problems with different fixes (a wrong key vs a
//     missing allow-list entry), so they map to two sentinels.
//   - type "danger" — an operation failure, inside the result array:
//     [{"type":"danger",...,"msg":["object_exists","x@example.test"]}].
//     `msg` is a string OR an array; it is normalized into an APIError.
//
// Further rules the note states, each pinned by a test in client_test.go:
// the whole result array is inspected (a partial failure is a failure); an
// empty body is a failure (it is what an unauthenticated request gets); and
// `{}` on a GET means "does not exist" ONLY once the key has been validated,
// because the same `{}` is what a silently failed authentication looks like
// — see Client.ValidateKey.

// ErrIPDenied is returned when the key is valid but the source address Moov
// presented is not in the key's allow-list. Mailcow's message names the IP it
// saw, which is the whole diagnostic; it is kept in the error text.
var ErrIPDenied = errors.New("mailcow: API key is valid but this source address is not in its allow-list")

// ErrKeyNotValidated is returned by a GET that answered `{}` before the key
// was validated: that body is what an absent object AND a silently rejected
// key both produce, and treating it as "not found" would let an idempotent
// create mint a duplicate mailbox on a misconfigured key.
var ErrKeyNotValidated = errors.New("mailcow: the API key has not been validated, so an empty object cannot be read as not-found")

// APIError is an operation failure Mailcow reported inside HTTP 200 — the
// "danger" family. Code is the first element of msg (the localization key
// Mailcow uses: "object_exists", "access_denied", "mailbox_quota_exceeded",
// "password_complexity", "username_invalid", ...); Args are the remaining
// elements as strings.
//
// It matches ErrAPI through errors.Is, so callers that only need "the API
// refused" keep working, while callers that must branch — the idempotent
// create treats object_exists as success — read Code.
type APIError struct {
	Type string
	Code string
	Args []string
}

func (e *APIError) Error() string {
	if len(e.Args) == 0 {
		return fmt.Sprintf("%v: type=%q msg=%s", ErrAPI, e.Type, e.Code)
	}
	return fmt.Sprintf("%v: type=%q msg=%s(%s)", ErrAPI, e.Type, e.Code, strings.Join(e.Args, ", "))
}

// Is makes errors.Is(err, ErrAPI) true for every APIError.
func (e *APIError) Is(target error) bool { return target == ErrAPI }

// IsAPICode reports whether err is an APIError carrying code.
func IsAPICode(err error, code string) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == code
}

// The Mailcow msg codes this client's callers branch on. They are the
// localization keys json_api.php emits; the ones below were observed verbatim
// in F0 and are named here so a caller compares against a constant.
const (
	CodeObjectExists         = "object_exists"
	CodeAccessDenied         = "access_denied"
	CodeQuotaExceeded        = "mailbox_quota_exceeded"
	CodePasswordComplexity   = "password_complexity"
	CodePasswordMismatch     = "password_mismatch"
	CodeUsernameInvalid      = "username_invalid"
	msgAuthenticationFailed  = "authentication failed"
	msgAPIAccessDeniedForIP  = "api access denied"
	statusVersionPath        = "/get/status/version"
	failureSnippetLimitBytes = 200
)

// apiResult is Mailcow's result envelope. Both msg and type vary in shape
// between endpoints, so both are decoded permissively.
type apiResult struct {
	Type string          `json:"type"`
	Msg  json.RawMessage `json:"msg"`
}

// normalizeMsg turns Mailcow's string-or-array msg into a code and its
// arguments. A string is the code alone; an array's first element is the code
// and the rest are rendered as strings (numbers as digits, nested values as
// their JSON), so a caller can read "mailbox_quota_exceeded" and "2048" from
// ["mailbox_quota_exceeded", 2048] without caring about the shape.
func normalizeMsg(raw json.RawMessage) (code string, args []string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s, nil
		}
		return string(raw), nil
	}
	if raw[0] == '[' {
		var parts []json.RawMessage
		if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
			return snippet(raw), nil
		}
		code = rawToString(parts[0])
		for _, p := range parts[1:] {
			args = append(args, rawToString(p))
		}
		return code, args
	}
	return snippet(raw), nil
}

func rawToString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	case 'n':
		if bytes.Equal(raw, []byte("null")) {
			return ""
		}
	default:
		var n float64
		if err := json.Unmarshal(raw, &n); err == nil {
			return strconv.FormatFloat(n, 'f', -1, 64)
		}
	}
	return string(raw)
}

// classifyErrorEnvelope maps a type=error object onto the two authentication
// sentinels. The ip-denied text is matched by prefix because it carries the
// IP Mailcow saw, which varies and is exactly what the operator needs to read.
func classifyErrorEnvelope(msg string) error {
	lower := strings.ToLower(msg)
	if strings.Contains(lower, msgAPIAccessDeniedForIP) {
		return fmt.Errorf("%w: %s", ErrIPDenied, snippet([]byte(msg)))
	}
	if strings.Contains(lower, msgAuthenticationFailed) {
		return fmt.Errorf("%w: %s", ErrUnauthorized, snippet([]byte(msg)))
	}
	return fmt.Errorf("%w: %s", ErrUnauthorized, snippet([]byte(msg)))
}

// errorEnvelope inspects a body for the single-object "error" family and
// returns the classified error, or nil when the body is not one. It is applied
// to EVERY response — reads included — because a GET's error object would
// otherwise decode into an empty struct and read as "not found".
func errorEnvelope(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var one apiResult
	if err := json.Unmarshal(trimmed, &one); err != nil {
		// A body this function cannot decode is simply not the error family
		// it is looking for — the caller goes on to parse it as a normal
		// result and produces its own, better error if that fails too.
		// Returning the decode failure here would turn every unexpected shape
		// into "Mailcow refused", which is the wrong diagnosis.
		return nil //nolint:nilerr // "not this family" is the answer, not an error.
	}
	if one.Type != "error" {
		return nil
	}
	code, _ := normalizeMsg(one.Msg)
	return classifyErrorEnvelope(code)
}

// checkAPIResult interprets a mutation response under the F0 contract.
//
// Mailcow answers HTTP 200 for failures, so this — not the status code — is
// what decides whether a write happened. The response is sometimes an object
// and sometimes an array of them; both are handled, and EVERY element is
// inspected: one call can produce several results (a mailbox create returns a
// rate-limit result and the create itself) and a failure anywhere is a
// failure.
//
// wantMsg is corroboration only. Mailcow's msg strings are localization keys
// that have been renamed across releases; refusing a success that used a new
// key would break Moov on a harmless upgrade. type=success is the contract.
func checkAPIResult(body []byte, wantMsg string) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return fmt.Errorf("%w: empty response body (what an unauthenticated request gets)", ErrUnexpectedResponse)
	}
	var results []apiResult
	switch trimmed[0] {
	case '[':
		if err := json.Unmarshal(trimmed, &results); err != nil {
			return fmt.Errorf("%w: decoding result: %w (%s)", ErrUnexpectedResponse, err, snippet(body))
		}
	case '{':
		var one apiResult
		if err := json.Unmarshal(trimmed, &one); err != nil {
			return fmt.Errorf("%w: decoding result: %w (%s)", ErrUnexpectedResponse, err, snippet(body))
		}
		results = []apiResult{one}
	default:
		return fmt.Errorf("%w: not a JSON result (%s)", ErrUnexpectedResponse, snippet(body))
	}
	if len(results) == 0 {
		return fmt.Errorf("%w: response carried no result (%s)", ErrUnexpectedResponse, snippet(body))
	}
	_ = wantMsg
	for _, r := range results {
		code, args := normalizeMsg(r.Msg)
		switch r.Type {
		case "success":
			continue
		case "error":
			return classifyErrorEnvelope(code)
		default:
			// "danger" and anything else Mailcow may add: an operation failure.
			return &APIError{Type: r.Type, Code: code, Args: args}
		}
	}
	return nil
}
