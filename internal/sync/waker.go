package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// The snooze waker (L3 epic E4): the loop that brings snoozed mail back.
//
// # Why a poller and not a timer per snooze
//
// A timer per pending snooze would be a goroutine (or a heap entry) per row,
// rebuilt on every restart, and wrong the moment the process is down at the
// moment a wake is due. A poller has none of those properties to get right:
// the wake time is a COLUMN, the query "what is due" is an indexed range scan
// (snoozes_due, migration 0009), and a daemon that was down for an hour wakes
// everything it missed on its first pass. This is the same reasoning the
// outbox executor states for its own poll, and the two loops are deliberately
// shaped alike.
//
// # The interval, and why a minute is honest
//
// The undo window's resolution is seconds, so the outbox polls every second.
// A snooze's resolution is the user's: Gmail's presets are hours and days
// (canon §2.2; §5 records that the exact preset times are unsourced). A minute
// of latency on a "tomorrow morning" reminder is invisible, and polling every
// second for it would be 86,400 pointless queries a day per deployment.
//
// The consequence is stated rather than hidden: a snooze set for 30 seconds
// from now fires up to a minute late. The API accepts any RFC 3339 instant
// (the JMAP surface does not restrict it to presets), so that case is
// reachable, and "up to one minute late" is the documented guarantee.

// DefaultWakeInterval is how often the waker polls for due snoozes.
const DefaultWakeInterval = time.Minute

// wakeBatch bounds one pass, so a backlog (a daemon that was down for a week)
// is drained over several passes instead of in one unbounded burst of IMAP
// commands.
const wakeBatch = 50

// maxWakeAttempts is how many times a transient failure is retried before the
// snooze is marked failed.
//
// Failing LOUDLY rather than retrying forever is the point: a snooze that can
// never be delivered leaves the message visible in the Snoozed folder with a
// row that says why, which a user can act on. A silent infinite retry would
// leave the same message in the same folder with the system claiming it is
// still going to handle it.
const maxWakeAttempts = 5

// wakeRetryDelay is how long a transient failure waits.
const wakeRetryDelay = 5 * time.Minute

// SnoozeObserver counts woken snoozes (E8-lite). Same seam shape as
// submit.Observer and for the same reason: this package must not import the
// metrics exporter to be able to count a wake. nil is valid.
type SnoozeObserver interface {
	SnoozeWoken()
}

// WakerOptions configures a Waker.
type WakerOptions struct {
	// Interval is the poll period. Default DefaultWakeInterval.
	Interval time.Duration

	// Logger receives structured diagnostics. Default slog.Default().
	Logger *slog.Logger

	// Observer, when set, is told once per successful wake.
	Observer SnoozeObserver
}

// Waker returns snoozed messages to their origin folders when their time
// comes.
type Waker struct {
	store *store.Store
	exec  *WriteExecutor
	opts  WakerOptions
	log   *slog.Logger
}

// NewWaker builds the waker.
func NewWaker(st *store.Store, exec *WriteExecutor, opts WakerOptions) (*Waker, error) {
	if st == nil {
		return nil, errors.New("sync: a store is required")
	}
	if exec == nil {
		return nil, errors.New("sync: a write executor is required")
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultWakeInterval
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Waker{store: st, exec: exec, opts: opts,
		log: opts.Logger.With("component", "snooze-waker")}, nil
}

// Run polls until ctx ends.
//
// The first pass runs IMMEDIATELY rather than after one interval: a daemon
// that just restarted may have missed wakes while it was down, and making the
// user wait an extra minute for mail that was already late is the wrong
// default.
func (w *Waker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.opts.Interval)
	defer ticker.Stop()

	for {
		if err := w.RunOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A failed pass is logged and the loop continues: the rows are
			// still pending and the next pass retries them. Returning would
			// stop waking mail for the lifetime of the process because of one
			// transient database error.
			w.log.Warn("a snooze wake pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// RunOnce performs one pass and reports how many messages were woken.
//
// Exported so a test can drive the loop deterministically instead of waiting
// for a ticker, and so an operator command could force a pass.
func (w *Waker) RunOnce(ctx context.Context) error {
	due, err := w.store.ClaimDueSnoozes(ctx, time.Now(), wakeBatch)
	if err != nil {
		return fmt.Errorf("claiming due snoozes: %w", err)
	}
	for i := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.wakeOne(ctx, due[i])
	}
	return nil
}

// wakeOne performs one wake, recording the outcome.
//
// Errors are recorded on the row and never returned: one snooze that cannot be
// woken (its message was deleted, its folder is gone, Dovecot refused) must not
// stop the rest of the batch. This is the same per-item isolation the sync
// pipeline's insertDegraded applies, for the same reason.
func (w *Waker) wakeOne(ctx context.Context, sn store.Snooze) {
	messageID, err := w.resolveSnoozedMessage(ctx, sn)
	if err != nil {
		// The message is not findable in Snoozed. That is not a transient
		// failure: the user moved it out by hand, or deleted it, and the
		// snooze is simply obsolete. Recording it as woken (rather than failed)
		// is the truthful outcome — there is nothing left to wake — and it
		// stops the row from being retried forever.
		if errors.Is(err, ErrNotSnoozed) || errors.Is(err, ErrWriteNotFound) {
			w.log.Info("a snoozed message is no longer in the Snoozed folder; retiring its wake",
				"account_id", sn.AccountID, "message_id", sn.MessageRFCID)
			if merr := w.store.MarkSnoozeWoken(ctx, sn.ID); merr != nil {
				w.log.Warn("retiring an obsolete snooze failed", "snooze_id", sn.ID, "error", merr)
			}
			return
		}
		w.fail(ctx, sn, err)
		return
	}

	if _, err := w.exec.wakeMessage(ctx, sn.AccountID, messageID, sn.OriginMailbox); err != nil {
		w.fail(ctx, sn, err)
		return
	}
	if err := w.store.MarkSnoozeWoken(ctx, sn.ID); err != nil {
		// The message IS back in the inbox; only the bookkeeping failed. The
		// next pass will try to wake it again, find it gone from Snoozed, and
		// retire the row through the branch above — so the duplicate-wake path
		// is closed by the same idempotency the design already needed.
		w.log.Warn("a message was woken but its snooze row could not be closed",
			"snooze_id", sn.ID, "error", err)
		return
	}
	w.log.Info("a snoozed message was returned to the top of its folder",
		"account_id", sn.AccountID, "message_id", sn.MessageRFCID, "origin", sn.OriginMailbox)
	if w.opts.Observer != nil {
		w.opts.Observer.SnoozeWoken()
	}
}

// resolveSnoozedMessage finds the store id of a snoozed message from its
// durable Message-ID.
//
// This indirection is the whole point of keying the table on the Message-ID: a
// cache rebuild renumbers every messages.id, and the pending snooze written
// before the rebuild still finds its message afterwards.
func (w *Waker) resolveSnoozedMessage(ctx context.Context, sn store.Snooze) (int64, error) {
	mb, err := w.store.GetMailboxByName(ctx, sn.AccountID, SnoozeMailboxName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, ErrNotSnoozed
		}
		return 0, fmt.Errorf("resolving the Snoozed mailbox: %w", err)
	}
	id, err := w.store.MessageIDInMailbox(ctx, mb.ID, sn.MessageRFCID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, ErrNotSnoozed
		}
		return 0, fmt.Errorf("resolving the snoozed message: %w", err)
	}
	return id, nil
}

// fail records a wake failure, retrying transiently until the attempt cap.
func (w *Waker) fail(ctx context.Context, sn store.Snooze, cause error) {
	permanent := sn.Attempts+1 >= maxWakeAttempts
	var retryAt *time.Time
	if !permanent {
		t := time.Now().Add(wakeRetryDelay)
		retryAt = &t
	}
	if err := w.store.FailSnooze(ctx, sn.ID, cause.Error(), retryAt); err != nil {
		w.log.Warn("recording a snooze failure failed", "snooze_id", sn.ID, "error", err)
	}
	level := slog.LevelWarn
	if permanent {
		level = slog.LevelError
	}
	w.log.Log(ctx, level, "waking a snoozed message failed",
		"account_id", sn.AccountID, "message_id", sn.MessageRFCID,
		"attempts", sn.Attempts+1, "permanent", permanent, "error", cause)
}
