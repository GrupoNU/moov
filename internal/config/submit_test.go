package config

import (
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/jmap/mail"
)

// The outbox configuration (W3): defaults, the undo-window clamp, and the
// cross-package pin that keeps the restated constants honest.

func TestLoadSubmitDefaults(t *testing.T) {
	t.Setenv("MOOV_SMTP_HOST", "")
	t.Setenv("MOOV_SMTP_PORT", "")
	t.Setenv("MOOV_SMTP_SERVER_NAME", "")
	t.Setenv("MOOV_IMAP_SERVER_NAME", "")
	t.Setenv("MOOV_UNDO_WINDOW_SECONDS", "")

	s, err := loadSubmit()
	if err != nil {
		t.Fatalf("loadSubmit: %v", err)
	}
	if s.SMTPHost != "postfix" || s.SMTPPort != 587 {
		t.Errorf("SMTP defaults = %s:%d, want postfix:587 (ADR §4)", s.SMTPHost, s.SMTPPort)
	}
	if s.UndoWindow != 10*time.Second {
		t.Errorf("undo window default = %v, want 10s (W-A3)", s.UndoWindow)
	}
}

func TestLoadSubmitClampsTheUndoWindow(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"1":   5 * time.Second,  // under the floor
		"5":   5 * time.Second,  // the floor itself
		"17":  17 * time.Second, // in range
		"30":  30 * time.Second, // the ceiling itself
		"600": 30 * time.Second, // over the ceiling
	} {
		t.Setenv("MOOV_UNDO_WINDOW_SECONDS", in)
		s, err := loadSubmit()
		if err != nil {
			t.Fatalf("loadSubmit(%s): %v", in, err)
		}
		if s.UndoWindow != want {
			t.Errorf("UndoWindow(%s) = %v, want %v", in, s.UndoWindow, want)
		}
	}

	t.Setenv("MOOV_UNDO_WINDOW_SECONDS", "not-a-number")
	if _, err := loadSubmit(); err == nil {
		t.Error("an unparseable window loaded silently")
	}
}

func TestLoadSubmitServerNameFallsBackToIMAP(t *testing.T) {
	t.Setenv("MOOV_SMTP_SERVER_NAME", "")
	t.Setenv("MOOV_IMAP_SERVER_NAME", "mail.example.test")
	s, err := loadSubmit()
	if err != nil {
		t.Fatal(err)
	}
	if s.SMTPServerName != "mail.example.test" {
		t.Errorf("server name = %q, want the IMAP fallback (S1 H2, one certificate)", s.SMTPServerName)
	}

	t.Setenv("MOOV_SMTP_SERVER_NAME", "smtp.example.test")
	s, _ = loadSubmit()
	if s.SMTPServerName != "smtp.example.test" {
		t.Errorf("explicit server name = %q", s.SMTPServerName)
	}
}

func TestLoadSubmitPortValidation(t *testing.T) {
	for _, bad := range []string{"0", "65536", "-1", "puerto"} {
		t.Setenv("MOOV_SMTP_PORT", bad)
		if _, err := loadSubmit(); err == nil {
			t.Errorf("port %q loaded silently", bad)
		}
	}
}

// TestUndoWindowConstantsMatchMail pins the restated constants to
// internal/jmap/mail's — the same arrangement DefaultMaxSSEPerAccount has
// with jmaphttp. Config cannot import mail in production code (the dependency
// runs the other way), but the TEST can, and it is what keeps the two from
// drifting.
func TestUndoWindowConstantsMatchMail(t *testing.T) {
	if got := time.Duration(DefaultUndoWindowSeconds) * time.Second; got != mail.DefaultUndoWindow {
		t.Errorf("default: config %v vs mail %v", got, mail.DefaultUndoWindow)
	}
	if got := time.Duration(MinUndoWindowSeconds) * time.Second; got != mail.MinUndoWindow {
		t.Errorf("min: config %v vs mail %v", got, mail.MinUndoWindow)
	}
	if got := time.Duration(MaxUndoWindowSeconds) * time.Second; got != mail.MaxUndoWindow {
		t.Errorf("max: config %v vs mail %v", got, mail.MaxUndoWindow)
	}
}
