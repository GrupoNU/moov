package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/GrupoNU/moov/internal/imap"
)

// decodeInto unmarshals a JSON fixture into v. It exists so the fakes can be
// built from the SHAPE Mailcow really sends — string-typed booleans and all —
// rather than from Go literals that quietly assume a friendlier schema.
func decodeInto(v any, jsonText string) error {
	if err := json.Unmarshal([]byte(jsonText), v); err != nil {
		return fmt.Errorf("decoding fixture: %w", err)
	}
	return nil
}

// stubIMAPClient is an imap.Client whose Connect and Close are scripted.
//
// It embeds the interface so only the two methods under test need
// implementing; any other call is a nil-pointer panic, which is the correct
// outcome for a method the validator has no business calling.
type stubIMAPClient struct {
	imap.Client
	connectErr error
	closeErr   error
	connects   int
	closes     int
}

func (s *stubIMAPClient) Connect(_ context.Context, _ imap.Config) error {
	s.connects++
	return s.connectErr
}

func (s *stubIMAPClient) Close() error {
	s.closes++
	return s.closeErr
}

func newTestValidator(stub *stubIMAPClient) *imapValidator {
	return &imapValidator{
		log:  slog.New(slog.DiscardHandler),
		dial: func(*slog.Logger) imap.Client { return stub },
	}
}

func TestValidatorSuccessClosesTheConnection(t *testing.T) {
	// The validation connection has one job and must not be left open: a
	// provisioning flow that leaks an IMAP connection per attempt is a
	// resource leak against a server we do not control.
	stub := &stubIMAPClient{}
	v := newTestValidator(stub)

	if err := v.Validate(context.Background(), imap.Config{
		Host: "dovecot", Username: "user@example.com", Password: "pw",
	}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if stub.connects != 1 {
		t.Errorf("connected %d times, want 1", stub.connects)
	}
	if stub.closes != 1 {
		t.Errorf("closed %d times, want 1", stub.closes)
	}
}

func TestValidatorClassifiesAuthFailures(t *testing.T) {
	// Telling a user "wrong password" when the server is down is a bad bug;
	// so is telling them "server error" when they mistyped. These are the
	// strings Dovecot and go-imap really produce.
	authFailures := []string{
		"imap: login failed for user@example.com: AUTHENTICATIONFAILED Authentication failed.",
		"imap: login failed for user@example.com: Authentication failed.",
		"AUTHORIZATIONFAILED",
		"invalid credentials",
	}
	for _, msg := range authFailures {
		t.Run(msg, func(t *testing.T) {
			v := newTestValidator(&stubIMAPClient{connectErr: errors.New(msg)})
			err := v.Validate(context.Background(), imap.Config{Host: "d", Username: "u", Password: "p"})
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("got %v, want ErrInvalidCredentials", err)
			}
		})
	}

	notAuthFailures := []string{
		"imap: dialing dovecot:143: connection refused",
		"imap: STARTTLS to dovecot:143: x509: certificate signed by unknown authority",
		"context deadline exceeded",
	}
	for _, msg := range notAuthFailures {
		t.Run(msg, func(t *testing.T) {
			v := newTestValidator(&stubIMAPClient{connectErr: errors.New(msg)})
			err := v.Validate(context.Background(), imap.Config{Host: "d", Username: "u", Password: "p"})
			if err == nil {
				t.Fatal("expected an error")
			}
			if errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("a server-side failure was reported as a bad password: %v", err)
			}
		})
	}
}

func TestValidatorReportsMissingCapabilityDistinctly(t *testing.T) {
	// A Dovecot without CONDSTORE is a deployment problem. Reporting it as a
	// bad password would send an operator hunting in the wrong place.
	stub := &stubIMAPClient{
		connectErr: &imap.MissingCapabilityError{Missing: []string{"condstore"}},
	}
	v := newTestValidator(stub)

	err := v.Validate(context.Background(), imap.Config{Host: "d", Username: "u", Password: "p"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("a missing capability was reported as a bad password: %v", err)
	}
	if !errors.Is(err, imap.ErrMissingCapability) {
		t.Fatalf("got %v, want ErrMissingCapability", err)
	}
}

func TestValidatorToleratesACloseFailure(t *testing.T) {
	// The credential is valid; that is what was being established. An untidy
	// LOGOUT must not fail the provisioning.
	stub := &stubIMAPClient{closeErr: errors.New("connection reset by peer")}
	v := newTestValidator(stub)

	if err := v.Validate(context.Background(), imap.Config{
		Host: "dovecot", Username: "u", Password: "p",
	}); err != nil {
		t.Fatalf("a close failure failed the validation: %v", err)
	}
}

func TestValidatorErrorsNeverEchoThePassword(t *testing.T) {
	// go-imap's login error carries the server's text. If a server ever echoed
	// the credential, this is the boundary that must not pass it on.
	const pw = "SECRET-USER-PASSWORD-9f8e7d"
	stub := &stubIMAPClient{
		connectErr: fmt.Errorf("imap: login failed: NO [AUTHENTICATIONFAILED] rejected %s", pw),
	}
	v := newTestValidator(stub)

	err := v.Validate(context.Background(), imap.Config{Host: "d", Username: "u", Password: pw})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); contains(got, pw) {
		t.Fatalf("the validation error echoes the password: %s", got)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}

func TestNewIMAPValidatorIsUsable(t *testing.T) {
	// The constructor must produce a working validator with a nil logger.
	if v := NewIMAPValidator(nil); v == nil {
		t.Fatal("NewIMAPValidator(nil) returned nil")
	}
}
