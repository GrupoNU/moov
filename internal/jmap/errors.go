package jmap

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// This file is the single home of the JMAP error taxonomy (L2 §4: "mapa único
// RFC 8620 §3.6 — nunca strings ad hoc"). Every error name that can appear on
// the wire is a constant here, registered in one map; nothing anywhere else in
// the codebase writes a JMAP error type as a string literal.

// ErrorCode is a method-level JMAP error type (RFC 8620 §3.6.2), the value of
// the "type" property in an "error" method response.
type ErrorCode string

// The method-level error types of RFC 8620 §3.6.2, "which may be returned for
// any method call where appropriate".
const (
	// CodeServerUnavailable: "Some internal server resource was temporarily
	// unavailable."
	CodeServerUnavailable ErrorCode = "serverUnavailable"
	// CodeServerFail: "An unexpected or unknown error occurred during the
	// processing of the call."
	CodeServerFail ErrorCode = "serverFail"
	// CodeServerPartialFail: "Some, but not all, expected changes described by
	// the method occurred." Its use is strongly discouraged by the RFC.
	CodeServerPartialFail ErrorCode = "serverPartialFail"
	// CodeUnknownMethod: "The server does not recognize this method name"
	// (spelling normalized to US English from the RFC's British original).
	CodeUnknownMethod ErrorCode = "unknownMethod"
	// CodeInvalidArguments: "One of the arguments is of the wrong type or is
	// otherwise invalid, or a required argument is missing."
	CodeInvalidArguments ErrorCode = "invalidArguments"
	// CodeInvalidResultReference: "The method used a result reference for one
	// of its arguments (see Section 3.7), but this failed to resolve."
	CodeInvalidResultReference ErrorCode = "invalidResultReference"
	// CodeForbidden: "executing the method would violate an Access Control
	// List (ACL) or other permissions policy."
	CodeForbidden ErrorCode = "forbidden"
	// CodeAccountNotFound: "The accountId does not correspond to a valid
	// account."
	CodeAccountNotFound ErrorCode = "accountNotFound"
	// CodeAccountNotSupportedByMethod: "The accountId given corresponds to a
	// valid account, but the account does not support this method or data
	// type."
	CodeAccountNotSupportedByMethod ErrorCode = "accountNotSupportedByMethod"
	// CodeAccountReadOnly: "This method modifies state, but the account is
	// read-only."
	CodeAccountReadOnly ErrorCode = "accountReadOnly"
)

// Method-specific error types from the standard methods of RFC 8620 §5,
// declared here so J2/J3 use these constants instead of new strings.
const (
	// CodeRequestTooLarge (§5.1 /get, §5.3 /set): the number of ids/objects
	// exceeds maxObjectsInGet / maxObjectsInSet.
	CodeRequestTooLarge ErrorCode = "requestTooLarge"
	// CodeCannotCalculateChanges (§5.2 /changes, §5.6 /queryChanges): the
	// server cannot calculate changes from the given state. Moov returns it
	// for every Foo/queryChanges in phase 1 (L2 §2.3 — a legitimate answer).
	CodeCannotCalculateChanges ErrorCode = "cannotCalculateChanges"
	// CodeStateMismatch (§5.3 /set): ifInState did not match the current
	// state.
	CodeStateMismatch ErrorCode = "stateMismatch"
	// CodeAnchorNotFound (§5.5 /query): the anchor id was not found in the
	// results.
	CodeAnchorNotFound ErrorCode = "anchorNotFound"
	// CodeUnsupportedFilter (§5.5 /query): the filter is syntactically valid
	// but the server cannot process it.
	CodeUnsupportedFilter ErrorCode = "unsupportedFilter"
	// CodeUnsupportedSort (§5.5 /query): the sort is syntactically valid but
	// the server cannot process it.
	CodeUnsupportedSort ErrorCode = "unsupportedSort"
	// CodeTooManyChanges (§5.6 /queryChanges): more changes than maxChanges.
	CodeTooManyChanges ErrorCode = "tooManyChanges"
	// CodeFromAccountNotFound (§5.4 /copy, §6.3 blob copy): the fromAccountId
	// does not correspond to a valid account.
	CodeFromAccountNotFound ErrorCode = "fromAccountNotFound"
	// CodeFromAccountNotSupportedByMethod (§5.4 /copy): the fromAccountId
	// does not support the data type.
	CodeFromAccountNotSupportedByMethod ErrorCode = "fromAccountNotSupportedByMethod"
)

// errorCodes is THE registry of every error type this server may emit. A code
// missing from this map cannot be constructed through NewMethodError, which is
// what mechanically enforces "no ad-hoc strings": handlers can only fail with
// vocabulary the RFC defines.
var errorCodes = map[ErrorCode]struct{}{
	CodeServerUnavailable:               {},
	CodeServerFail:                      {},
	CodeServerPartialFail:               {},
	CodeUnknownMethod:                   {},
	CodeInvalidArguments:                {},
	CodeInvalidResultReference:          {},
	CodeForbidden:                       {},
	CodeAccountNotFound:                 {},
	CodeAccountNotSupportedByMethod:     {},
	CodeAccountReadOnly:                 {},
	CodeRequestTooLarge:                 {},
	CodeCannotCalculateChanges:          {},
	CodeStateMismatch:                   {},
	CodeAnchorNotFound:                  {},
	CodeUnsupportedFilter:               {},
	CodeUnsupportedSort:                 {},
	CodeTooManyChanges:                  {},
	CodeFromAccountNotFound:             {},
	CodeFromAccountNotSupportedByMethod: {},
}

// MethodError is a method-level error, rendered on the wire as
// ["error", {"type": ..., ...}, callId] (RFC 8620 §3.6.2).
//
// It implements error so handler code can flow it through normal Go error
// paths, but the dispatcher treats it as data, not as something to log and
// swallow: per §3.6.2 it is inserted at the current point in methodResponses
// and processing continues with the next method call.
type MethodError struct {
	// Code is the "type" property.
	Code ErrorCode

	// Description is the optional "description" property. §3.6.2 defines it
	// for serverFail and invalidArguments and permits other properties
	// generally ("Other properties may be present with further information").
	// It is a non-localized debugging aid, never shown to end users.
	Description string

	// Properties are additional wire properties for error types that define
	// them (e.g. a future tooManyChanges response). "type" and "description"
	// must not be duplicated here.
	Properties map[string]any
}

// NewMethodError returns a MethodError for a code in the registry.
//
// An unregistered code is a programming bug, but a mail server must not panic
// over a bug in one handler: the error degrades to serverFail — the RFC's
// catch-all ("An unexpected or unknown error occurred") — with the original
// code preserved in the description for the log trail.
func NewMethodError(code ErrorCode) *MethodError {
	if _, ok := errorCodes[code]; !ok {
		return &MethodError{
			Code:        CodeServerFail,
			Description: fmt.Sprintf("bug: unregistered JMAP error code %q", code),
		}
	}
	return &MethodError{Code: code}
}

// WithDescription returns a copy carrying a description.
func (e *MethodError) WithDescription(format string, args ...any) *MethodError {
	out := *e
	out.Description = fmt.Sprintf(format, args...)
	return &out
}

// Error implements the error interface.
func (e *MethodError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("jmap: %s: %s", e.Code, e.Description)
	}
	return fmt.Sprintf("jmap: %s", e.Code)
}

// Invocation renders the error as the "error" method response of §3.6.2,
// carrying the failed call's id.
func (e *MethodError) Invocation(callID string) Invocation {
	args := map[string]any{"type": string(e.Code)}
	if e.Description != "" {
		args["description"] = e.Description
	}
	for k, v := range e.Properties {
		args[k] = v
	}
	raw, err := json.Marshal(args)
	if err != nil {
		// Unreachable with the types above; kept total rather than trusted.
		raw = []byte(`{"type":"serverFail"}`)
	}
	return Invocation{Name: "error", Args: raw, CallID: callID}
}

// ProblemType is a request-level error problem type (RFC 8620 §3.6.1): the
// whole HTTP request is rejected with an HTTP error status and an RFC 7807
// problem-details body.
type ProblemType string

// The request-level problem types of RFC 8620 §3.6.1.
const (
	// ProblemUnknownCapability: "The client included a capability in the
	// 'using' property of the request that the server does not support."
	ProblemUnknownCapability ProblemType = "urn:ietf:params:jmap:error:unknownCapability"
	// ProblemNotJSON: "The content type of the request was not
	// 'application/json' or the request did not parse as I-JSON."
	ProblemNotJSON ProblemType = "urn:ietf:params:jmap:error:notJSON"
	// ProblemNotRequest: "The request parsed as JSON but did not match the
	// type signature of the Request object."
	ProblemNotRequest ProblemType = "urn:ietf:params:jmap:error:notRequest"
	// ProblemLimit: "The request was not processed as it would have exceeded
	// one of the request limits defined on the capability object." A "limit"
	// property MUST name the limit being applied (§3.6.1).
	ProblemLimit ProblemType = "urn:ietf:params:jmap:error:limit"
)

// problemStatus is the default HTTP status for each problem type.
//
// §3.6.1 prescribes "an appropriate HTTP error response code" and its examples
// use 400. 400 is the default here; the constructors below override it where
// plain HTTP semantics define a more precise status (413 for an oversized
// body, 429 for concurrency exhaustion, 415 for a wrong media type) — the
// strictest reading of "appropriate", since RFC 7807's "status" member is
// advisory and the problem "type" is what JMAP clients dispatch on.
var problemStatus = map[ProblemType]int{
	ProblemUnknownCapability: http.StatusBadRequest,
	ProblemNotJSON:           http.StatusBadRequest,
	ProblemNotRequest:        http.StatusBadRequest,
	ProblemLimit:             http.StatusBadRequest,
}

// RequestError is a request-level JMAP error: an RFC 7807 problem-details
// object (RFC 8620 §3.6.1) plus the HTTP status to send it with.
type RequestError struct {
	// Type is the problem type URN.
	Type ProblemType

	// Status is the HTTP status code, also serialized as the problem
	// object's "status" member.
	Status int

	// Detail is the human-readable "detail" member.
	Detail string

	// Limit names the exceeded limit; required for ProblemLimit (§3.6.1),
	// empty otherwise.
	Limit string
}

// NewRequestError builds a request-level error with the default status for
// its type.
func NewRequestError(t ProblemType, detail string) *RequestError {
	status, ok := problemStatus[t]
	if !ok {
		status = http.StatusBadRequest
	}
	return &RequestError{Type: t, Status: status, Detail: detail}
}

// NewLimitError builds the §3.6.1 limit problem, naming the exceeded limit.
// status lets the transport pick the precise HTTP semantics (400, 413, 429).
func NewLimitError(status int, limit, detail string) *RequestError {
	return &RequestError{Type: ProblemLimit, Status: status, Detail: detail, Limit: limit}
}

// Error implements the error interface.
func (e *RequestError) Error() string {
	return fmt.Sprintf("jmap: request-level error %s: %s", e.Type, e.Detail)
}

// MarshalJSON renders the RFC 7807 problem-details body.
func (e *RequestError) MarshalJSON() ([]byte, error) {
	body := map[string]any{
		"type":   string(e.Type),
		"status": e.Status,
		"detail": e.Detail,
	}
	if e.Limit != "" {
		body["limit"] = e.Limit
	}
	return json.Marshal(body)
}
