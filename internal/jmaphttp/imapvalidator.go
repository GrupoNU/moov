package jmaphttp

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/GrupoNU/moov/internal/imap"
)

// IMAPLoginValidator validates credentials with a real IMAP LOGIN against
// the configured Dovecot (arbitration J-A1: Dovecot is the source of truth
// for auth, ADR §4). One connection per validation: connect, STARTTLS,
// LOGIN, LOGOUT — nothing else touches the mailbox.
type IMAPLoginValidator struct {
	// Host and Port locate Dovecot; inside the deployment Host is the
	// Mailcow container alias ("dovecot") on the shared network.
	Host string
	Port int

	// TLSServerName is the name the certificate is verified against, which
	// legitimately differs from Host (S1 H2: dial the alias, verify the
	// public mail hostname).
	TLSServerName string

	// InsecureSkipVerify disables certificate verification.
	// DEVELOPMENT/TEST ONLY — see the extensive warning on
	// imap.Config.InsecureSkipVerify; internal/imap logs loudly whenever it
	// is honored. It exists here solely so the env-gated integration test can
	// reach a Dovecot whose certificate name cannot be matched from the test
	// network. cmd/moovd never sets it: there is no environment variable for
	// it on purpose.
	InsecureSkipVerify bool

	// Logger for connection-level events. nil means slog.Default().
	Logger *slog.Logger
}

var _ CredentialValidator = (*IMAPLoginValidator)(nil)

// Validate implements CredentialValidator.
func (v *IMAPLoginValidator) Validate(ctx context.Context, username, password string) (bool, error) {
	logger := v.Logger
	if logger == nil {
		logger = slog.Default()
	}

	client := imap.New(logger)
	err := client.Connect(ctx, imap.Config{
		Host:               v.Host,
		Port:               v.Port,
		Username:           username,
		Password:           password,
		TLSServerName:      v.TLSServerName,
		InsecureSkipVerify: v.InsecureSkipVerify,
		ClientName:         "moov-jmap-auth",
	})
	if err == nil {
		// Credentials are valid; the connection served its purpose.
		_ = client.Close()
		return true, nil
	}

	// Connect performs dial → STARTTLS → LOGIN → capability probe → ENABLE.
	// Everything from the capability probe onward happens AFTER a successful
	// LOGIN, so those failures still mean "the credentials are valid" — a
	// capability shortfall is the sync engine's problem, not an auth verdict.
	var missing *imap.MissingCapabilityError
	if errors.As(err, &missing) {
		return true, nil
	}

	if isLoginRejection(err) {
		return false, nil
	}
	// Dial, TLS or protocol trouble: the authority is unavailable, which
	// must surface as 503, never as 401 (a client told 401 during an outage
	// may discard its stored password).
	return false, err
}

// isLoginRejection reports whether err is Dovecot refusing the credentials,
// as opposed to the connection failing around the LOGIN.
//
// HONEST LIMITATION: internal/imap does not (yet) export a typed
// authentication error — its redaction step (conn.go redactErr) deliberately
// flattens the server response to "<TYPE> <CODE>" text to keep credentials
// out of logs. So this classification reads the error STRING produced by
// code we own: Connect wraps a LOGIN failure as "login failed for …: <err>",
// and a server rejection's redacted form carries the IMAP status "NO" or
// "BAD". An I/O error during LOGIN produces the same "login failed" prefix
// but no status token, and correctly classifies as unavailable. The right
// fix is a typed imap.ErrAuthRejected in internal/imap — out of this epic's
// scope (J1 may not touch other packages); flagged in the epic report.
func isLoginRejection(err error) bool {
	msg := err.Error()
	if !strings.Contains(msg, "login failed") {
		return false
	}
	return strings.Contains(msg, ": NO") || strings.Contains(msg, ": BAD")
}
