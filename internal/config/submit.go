package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// SubmitConfig is the outbox's configuration (W3, L2-jmap-write §4: "Config
// nueva: MOOV_UNDO_WINDOW_SECONDS ... SMTP host/port").
type SubmitConfig struct {
	// SMTPHost and SMTPPort name the submission server (MOOV_SMTP_HOST /
	// MOOV_SMTP_PORT). Defaults "postfix":587 — the Mailcow Postfix container
	// on the shared Docker network, submission port (ADR §4).
	SMTPHost string
	SMTPPort int

	// SMTPServerName is the certificate name for the STARTTLS handshake
	// (MOOV_SMTP_SERVER_NAME, falling back to MOOV_IMAP_SERVER_NAME): the
	// same alias-vs-certificate split as IMAP (S1 H2) — Moov dials the
	// container alias, the certificate carries the public mail hostname.
	SMTPServerName string

	// UndoWindow is the undo-send window (MOOV_UNDO_WINDOW_SECONDS, W-A3):
	// a submission is released this long after creation and is cancelable
	// meanwhile. Default 10 s, clamped to [5 s, 30 s] — the clamp rather than
	// an error because an out-of-range window is a preference to bound, not a
	// deployment to refuse; the effective value is logged either way.
	UndoWindow time.Duration
}

// The undo window contract. These mirror internal/jmap/mail's constants
// (DefaultUndoWindow et al.); config does not import that package — the
// dependency runs the other way — so the values are restated here and pinned
// together by a test, the same arrangement DefaultMaxSSEPerAccount has.
const (
	DefaultUndoWindowSeconds = 10
	MinUndoWindowSeconds     = 5
	MaxUndoWindowSeconds     = 30
)

// DefaultSMTPHost and DefaultSMTPPort are the Mailcow submission defaults.
const (
	DefaultSMTPHost = "postfix"
	DefaultSMTPPort = 587
)

// loadSubmit reads the outbox settings.
func loadSubmit() (SubmitConfig, error) {
	s := SubmitConfig{
		SMTPHost:       envOr("MOOV_SMTP_HOST", DefaultSMTPHost),
		SMTPPort:       DefaultSMTPPort,
		SMTPServerName: envOr("MOOV_SMTP_SERVER_NAME", os.Getenv("MOOV_IMAP_SERVER_NAME")),
		UndoWindow:     DefaultUndoWindowSeconds * time.Second,
	}

	if v := os.Getenv("MOOV_SMTP_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return SubmitConfig{}, fmt.Errorf("MOOV_SMTP_PORT: %w", err)
		}
		if n < 1 || n > 65535 {
			return SubmitConfig{}, fmt.Errorf("MOOV_SMTP_PORT: %d out of range", n)
		}
		s.SMTPPort = n
	}

	if v := os.Getenv("MOOV_UNDO_WINDOW_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return SubmitConfig{}, fmt.Errorf("MOOV_UNDO_WINDOW_SECONDS: %w", err)
		}
		switch {
		case n < MinUndoWindowSeconds:
			n = MinUndoWindowSeconds
		case n > MaxUndoWindowSeconds:
			n = MaxUndoWindowSeconds
		}
		s.UndoWindow = time.Duration(n) * time.Second
	}

	return s, nil
}

// String renders the outbox settings for logging. Nothing here is a secret —
// the SMTP credentials are per-account app passwords that never pass through
// configuration.
func (s SubmitConfig) String() string {
	return fmt.Sprintf(
		"smtp_host=%s smtp_port=%d smtp_server_name=%s undo_window=%s",
		s.SMTPHost, s.SMTPPort, orUnset(s.SMTPServerName), s.UndoWindow,
	)
}
