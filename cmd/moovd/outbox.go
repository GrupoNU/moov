package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/config"
	"github.com/GrupoNU/moov/internal/metrics"
	"github.com/GrupoNU/moov/internal/store"
	"github.com/GrupoNU/moov/internal/submit"
)

// The outbox's wiring (W3): the SMTP transport with its credentials, the
// executor's lifecycle, and the upload-pin sweep.
//
// This file is to SMTP what sync.go's accountDialer is to IMAP: the ONE place
// in the daemon where an account's stored ciphertext becomes a plaintext
// credential, here an AUTH PLAIN password for postfix:587 (the app password's
// smtp scope, provisioned by E7 alongside imap).

// smtpTransport implements submit.Transport over submit.Send plus the
// credential keyring.
type smtpTransport struct {
	cfg    config.SubmitConfig
	dialer *accountDialer
	logger *slog.Logger
}

// Send implements submit.Transport. The onAccepted callback is passed through
// untouched: submit.Send invokes it between the 250 and QUIT, which is the
// rule-1 ordering the outbox depends on (internal/submit doc.go).
func (t *smtpTransport) Send(ctx context.Context, account store.Account, env submit.Envelope, msg io.Reader, onAccepted func(reply string) error) (submit.Result, error) {
	password, err := t.dialer.password(account)
	if err != nil {
		return submit.Result{}, fmt.Errorf("account %d: %w", account.ID, err)
	}
	serverName := t.cfg.SMTPServerName
	if serverName == "" {
		serverName = account.IMAPServerName
	}
	return submit.Send(ctx, submit.Config{
		Host:          t.cfg.SMTPHost,
		Port:          t.cfg.SMTPPort,
		TLSServerName: serverName,
		Username:      account.IMAPUsername,
		Password:      password,
	}, env, msg, onAccepted)
}

// submissionMetrics adapts the metric set to the two observer seams the
// submission path exposes: internal/submit's Observer (sent, failed) and
// internal/jmap/mail's SubmissionObserver (canceled). Neither package imports
// internal/metrics; this is the one place the three vocabularies meet, and the
// constants are asserted equal by a test rather than assumed.
type submissionMetrics struct{ m *metrics.Metrics }

// SubmissionFinished implements submit.Observer.
func (s submissionMetrics) SubmissionFinished(result string) {
	if s.m == nil {
		return
	}
	s.m.IncSubmission(result)
}

// SubmissionCanceled implements mail.SubmissionObserver.
func (s submissionMetrics) SubmissionCanceled() {
	if s.m == nil {
		return
	}
	s.m.IncSubmission(metrics.SubmissionCanceled)
}

// outboxComponent owns the running executor's lifecycle.
type outboxComponent struct {
	cancel context.CancelFunc
	done   chan struct{}
	log    *slog.Logger
}

// startOutbox builds and starts the outbox executor in its own goroutine.
//
// It runs on a context of its own (derived from Background, canceled by
// shutdown) rather than on the daemon's signal context, because its lifetime
// is the JMAP component's: it starts when submission is mounted and stops in
// shutdown order, after the HTTP server drained — a /set that just enqueued
// must not watch its executor die first.
func startOutbox(
	cfg config.Config,
	st *store.Store,
	transport submit.Transport,
	sent submit.SentMailbox,
	raws submit.RawSource,
	notifier submit.Notifier,
	blobs *blob.Store,
	observer submit.Observer,
	logger *slog.Logger,
) (*outboxComponent, error) {
	outbox, err := submit.NewOutbox(st, transport, sent, raws, submit.Options{
		Logger:   logger,
		Notifier: notifier,
		Observer: observer,
	})
	if err != nil {
		return nil, fmt.Errorf("building the outbox: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	comp := &outboxComponent{cancel: cancel, done: make(chan struct{}), log: logger}

	go func() {
		defer close(comp.done)
		if err := outbox.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("outbox stopped", "error", err)
		}
	}()

	// The upload-pin sweep (RFC 8620 §6.1 retention; blob/pins.go). Hourly,
	// with a 24 h retention: an upload the user never attached survives a full
	// working day, far past the RFC's one-hour floor, and then the ordinary GC
	// takes over.
	go func() {
		const (
			sweepEvery   = time.Hour
			pinRetention = 24 * time.Hour
		)
		ticker := time.NewTicker(sweepEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepCtx, cancel := context.WithTimeout(ctx, time.Minute)
				if n, err := blobs.ExpirePins(sweepCtx, pinRetention, 1000); err != nil {
					logger.Warn("upload pin sweep failed", "error", err)
				} else if n > 0 {
					logger.Info("expired upload pins", "count", n)
				}
				cancel()
			}
		}
	}()

	logger.Info("outbox running",
		"smtp_host", cfg.Submit.SMTPHost, "smtp_port", cfg.Submit.SMTPPort,
		"undo_window", cfg.Submit.UndoWindow)
	return comp, nil
}

// shutdown stops the executor within ctx's deadline.
func (c *outboxComponent) shutdown(ctx context.Context) {
	if c == nil {
		return
	}
	c.cancel()
	select {
	case <-c.done:
	case <-ctx.Done():
		c.log.Warn("outbox did not stop within the shutdown grace period")
	}
}
