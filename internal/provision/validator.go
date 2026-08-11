package provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/GrupoNU/moov/internal/imap"
)

// imapValidator is the production IMAPValidator: it opens a real connection to
// Dovecot, authenticates, and closes it.
type imapValidator struct {
	log *slog.Logger
	// dial is indirected so the adapter itself can be tested without a server.
	dial func(logger *slog.Logger) imap.Client
}

// NewIMAPValidator returns an IMAPValidator backed by internal/imap.
//
// The validation is a full STARTTLS + LOGIN + capability probe, which is more
// than strictly needed to check a password — and deliberately so. A password
// that authenticates against a Dovecot missing CONDSTORE has not actually
// validated anything useful: the account would be provisioned and then fail at
// its first sync, at which point the user is gone and the failure is an
// operator's problem. Failing here, while the user is still present, is the
// cheaper place to discover it.
func NewIMAPValidator(logger *slog.Logger) IMAPValidator {
	if logger == nil {
		logger = slog.Default()
	}
	return &imapValidator{log: logger, dial: imap.New}
}

// Validate implements IMAPValidator.
func (v *imapValidator) Validate(ctx context.Context, cfg imap.Config) error {
	client := v.dial(v.log)

	if err := client.Connect(ctx, cfg); err != nil {
		// A missing capability is a server problem, not a credential one, and
		// must not be reported to a user as "wrong password".
		if errors.Is(err, imap.ErrMissingCapability) {
			return fmt.Errorf("the mail server lacks an extension Moov requires: %w", err)
		}
		if isAuthFailure(err) {
			// The underlying error is dropped rather than wrapped here: it is
			// the one case where the server's text is of no diagnostic value
			// (it says "authentication failed") and every extra byte is a
			// chance to echo something that came from the credential.
			return ErrInvalidCredentials
		}
		return fmt.Errorf("connecting to the mail server: %w", err)
	}

	// The connection served its only purpose. A failure to close cleanly is
	// logged rather than returned: the credential IS valid, which is what was
	// being established, and failing the provisioning over an untidy LOGOUT
	// would be the wrong trade.
	if err := client.Close(); err != nil {
		v.log.Warn("closing the validation connection", "error", err)
	}
	return nil
}

// isAuthFailure decides whether a Connect error was the server rejecting the
// credential, as opposed to a network or TLS problem.
//
// It is a string match, which is unpleasant, and the reason is that go-imap
// surfaces a NO response as an untyped error carrying the server's text. The
// alternative — treating every Connect failure as a bad password — would tell
// a user their password is wrong when the real problem is that Dovecot is
// down, which is worse than an imperfect classification. The match is on the
// IMAP response code (RFC 5530 AUTHENTICATIONFAILED) first, which Dovecot does
// send, and falls back to the phrasing.
func isAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"authenticationfailed", // RFC 5530 response code — the reliable one
		"authorizationfailed",
		"authentication failed",
		"invalid credentials",
		"login failed",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
