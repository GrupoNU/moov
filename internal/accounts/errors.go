package accounts

import (
	"errors"
	"fmt"
)

// The typed failures this package returns. The HTTP layer maps each onto one
// status of contract §2.2 and never invents a status of its own — so a new
// failure mode that forgets to pick one of these surfaces as a 500 rather
// than as a guessed 4xx.
var (
	// ErrNotFound is EVERYTHING the caller is not entitled to see: the
	// feature is off, the key is unknown, revoked or under-scoped, the
	// address belongs to another domain, or the account does not exist. One
	// error, because the wire has one answer for all of them (§2.1's
	// no-oracle rule); the distinction survives only in the log.
	ErrNotFound = errors.New("accounts: not found")

	// ErrDeleting is the 409: the operation is not allowed while a purge is
	// running. In this version it is the only state that refuses anything.
	ErrDeleting = errors.New("accounts: the account is being deleted")

	// ErrUpstreamRefused is a 502: Mailcow answered and refused. The wrapped
	// text is Moov's summary — never the raw upstream body, never a
	// credential.
	ErrUpstreamRefused = errors.New("accounts: upstream refused")

	// ErrUpstreamUnavailable is a 503: Mailcow (or Dovecot, for the
	// validation login) did not answer. Nothing changed; the caller retries.
	ErrUpstreamUnavailable = errors.New("accounts: upstream unavailable")

	// ErrReadOnlyIsOneWay refuses a resume-to-active on a read-only account:
	// the retention phase is one-way in this version (§2.4).
	ErrReadOnlyIsOneWay = errors.New("accounts: the read-only phase is one-way in this version")
)

// FieldError is the 400 of §2.2: the FIRST offending field and why. Field is
// the JSON name, dotted when nested ("limits.sendPerDay"), and empty when the
// body itself is not a JSON object.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string {
	if e.Field == "" {
		return "accounts: invalid body: " + e.Reason
	}
	return fmt.Sprintf("accounts: invalid field %s: %s", e.Field, e.Reason)
}

// fieldErr is the constructor every validation site uses.
func fieldErr(field, format string, args ...any) *FieldError {
	return &FieldError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// upstreamRefused wraps a Mailcow refusal with Moov's own summary. The
// summary is composed by the caller from what it was doing; the upstream
// error is joined for the LOG only, and the HTTP layer renders only the
// summary.
func upstreamRefused(summary string, cause error) error {
	return fmt.Errorf("%w: %s: %w", ErrUpstreamRefused, summary, cause)
}

// upstreamUnavailable wraps a Mailcow outage.
func upstreamUnavailable(summary string, cause error) error {
	return fmt.Errorf("%w: %s: %w", ErrUpstreamUnavailable, summary, cause)
}
