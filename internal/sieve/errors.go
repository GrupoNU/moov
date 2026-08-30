package sieve

import (
	"errors"
	"fmt"
)

// Sentinel errors. They are the conditions callers branch on; everything else
// arrives as *ServerError (a NO the caller did not specifically expect) or a
// wrapped transport error (the circuit-breaker-relevant category: the caller
// can errors.As for net.Error, and a ServerError is by definition NOT one —
// the server answered, so the connection is healthy).
var (
	// ErrNotConnected is returned by every method before a successful Connect
	// or after Close.
	ErrNotConnected = errors.New("sieve: not connected")

	// ErrNoSTARTTLS means the server did not advertise STARTTLS. This package
	// refuses to authenticate over cleartext (see doc.go), so the connection
	// is unusable.
	ErrNoSTARTTLS = errors.New("sieve: server does not offer STARTTLS; refusing to authenticate over cleartext")

	// ErrAuthFailed means the server refused AUTHENTICATE. The server's text
	// is deliberately not carried: a BAD/NO response that echoed part of the
	// exchange would put credential material in a log line (the same redaction
	// rule as internal/imap's redactErr).
	ErrAuthFailed = errors.New("sieve: authentication failed")

	// ErrScriptNotFound maps the NONEXISTENT response code (RFC 5804 §1.3):
	// GETSCRIPT, SETACTIVE, DELETESCRIPT or RENAMESCRIPT named a script the
	// server does not have.
	ErrScriptNotFound = errors.New("sieve: no such script")

	// ErrScriptActive maps the ACTIVE response code: DELETESCRIPT on the
	// active script (RFC 5804 §2.10 forbids it; deactivate first).
	ErrScriptActive = errors.New("sieve: the script is active")

	// ErrScriptExists maps the ALREADYEXISTS response code: RENAMESCRIPT to a
	// name that is taken (RFC 5804 §2.11).
	ErrScriptExists = errors.New("sieve: a script with that name already exists")
)

// QuotaError maps the QUOTA family of response codes (RFC 5804 §1.3): the
// server refused to store the script for a resource reason. Code preserves
// the exact variant ("QUOTA", "QUOTA/MAXSIZE", "QUOTA/MAXSCRIPTS") because
// the JMAP layer maps MAXSIZE to tooLarge and the others to overQuota
// (RFC 9661 §2.4 defines them as distinct SetError types).
type QuotaError struct {
	Code    string
	Message string
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("sieve: server quota refused the script (%s): %s", e.Code, e.Message)
}

// ScriptError means the server rejected the script content itself: PUTSCRIPT
// or CHECKSCRIPT answered NO because the script violates the Sieve grammar or
// requires an extension the server does not support (RFC 5804 §2.6, §2.12).
// Message is the server's human-readable diagnostic, which includes line
// numbers — it is the text RFC 9661's invalidSieve SetError carries to the
// client, so it is preserved verbatim.
type ScriptError struct {
	Message string
}

func (e *ScriptError) Error() string {
	return "sieve: the server rejected the script: " + e.Message
}

// ServerError is any other NO or BYE: the server answered and refused, for a
// reason no sentinel names. Code may be empty (many NOs carry only text).
// A TRYLATER code marks a transient condition (RFC 5804 §1.3) — Transient
// reports it, so a caller with retry logic can branch without matching
// strings.
type ServerError struct {
	Code    string
	Message string
}

func (e *ServerError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("sieve: server refused (%s): %s", e.Code, e.Message)
	}
	return "sieve: server refused: " + e.Message
}

// Transient reports whether the refusal is worth retrying later.
func (e *ServerError) Transient() bool { return e.Code == "TRYLATER" }
