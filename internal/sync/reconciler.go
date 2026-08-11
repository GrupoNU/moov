package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// The defensive reconciler (L2 §2.5).
//
// # Why a push engine still polls
//
// Because push is not a guarantee, and this engine's correctness claim cannot
// rest on one. Three things can lose an event, none of them hypothetical:
//
//  1. The watcher's event channel drops notifications when the consumer is
//     behind — a deliberate trade in internal/imap, because blocking the
//     decoder goroutine would wedge the connection, and the comment there says
//     in as many words that the reconciler is what catches the difference.
//  2. Dovecot has a history of NOTIFY regressions (the S2 research), and the
//     patched encoder is new code of ours besides.
//  3. A watcher that is down between two events hears neither, and the sweep at
//     reconnection covers only the accounts that actually reconnected.
//
// So a periodic STATUS comparison of every mailbox against local state is the
// backstop that makes a lost event cost latency rather than correctness. It is
// cheap: LIST-STATUS returns every folder's counters in one round trip, so the
// sweep costs one command and a handful of local reads, and only a mailbox that
// actually diverges costs a fetch.
//
// # What counts as a divergence
//
// Three server counters are compared against what Moov stored at its last pass:
// UIDNEXT (a message arrived), MESSAGES (a message arrived or was expunged),
// and HIGHESTMODSEQ (anything at all changed, including a pure flag toggle that
// moves neither of the other two — S2 T4). Any of the three moving means an
// event was missed, and every one found is logged as such: a divergence is not
// routine maintenance, it is evidence that push failed, and the rate of it is a
// number an operator should be able to watch (E8).

// ReconcileResult reports one sweep.
type ReconcileResult struct {
	AccountID int64

	// Checked is how many mailboxes the sweep compared.
	Checked int

	// Diverged is how many differed from local state — i.e. how many events
	// were missed.
	Diverged int

	// Repaired is how many of those an incremental pass then fixed.
	Repaired int

	// Divergences describes what was found, for the log and for tests.
	Divergences []Divergence

	Elapsed time.Duration
}

// Divergence is one mailbox whose server state did not match Moov's.
type Divergence struct {
	Mailbox string

	// Reason names which counters moved, e.g. "uidnext 41->45, highestmodseq
	// 900->912". It is a string because its only consumers are a log line and a
	// test assertion, and giving each counter a typed pair would be three more
	// types for no reader's benefit.
	Reason string
}

// runReconciler runs the periodic sweep until ctx ends.
//
// The first sweep is deliberately NOT immediate: runOnce already sweeps every
// mailbox at connection time, so an immediate pass here would repeat that work
// on every reconnect — and a flapping connection would turn the defensive sweep
// into a hot loop against the server it is trying not to overload.
func (w *PushWatcher) runReconciler(
	ctx context.Context,
	syncer *Syncer,
	account store.Account,
	log *slog.Logger,
) error {
	ticker := time.NewTicker(w.opts.ReconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			res, err := w.Reconcile(ctx, syncer, account, log)
			if err != nil {
				return err
			}
			if res.Diverged > 0 {
				w.emit(WatchObservation{
					AccountID: account.ID,
					Kind:      ObsReconciled,
				})
			}
		}
	}
}

// Reconcile compares every mailbox's server STATUS against local state and runs
// an incremental pass over any that diverge.
//
// It is exported because it is worth being able to trigger on demand — an
// operator investigating a stale account, and the E6 acceptance test that
// injects a divergence behind the watcher's back and requires this to find and
// repair it.
func (w *PushWatcher) Reconcile(
	ctx context.Context,
	syncer *Syncer,
	account store.Account,
	log *slog.Logger,
) (ReconcileResult, error) {
	// Real time, not Options.Clock: this is an elapsed-duration measurement,
	// and Options.Clock is often pinned to a fixed instant by a test.
	started := time.Now()
	res := ReconcileResult{AccountID: account.ID}

	// LIST-STATUS: every folder's counters in one round trip (S2 T2a). This is
	// the whole reason the sweep is cheap enough to run on a schedule.
	var infos []imap.MailboxInfo
	if err := syncer.conns.withConn(ctx, func(c imap.Client) error {
		var err error
		infos, err = c.ListMailboxes(ctx)
		return err
	}); err != nil {
		return res, fmt.Errorf("reconciler: listing mailboxes: %w", err)
	}

	stored, err := w.store.ListMailboxes(ctx, account.ID)
	if err != nil {
		return res, fmt.Errorf("reconciler: reading stored mailboxes: %w", err)
	}
	byName := make(map[string]store.Mailbox, len(stored))
	for _, m := range stored {
		byName[m.Name] = m
	}

	for _, info := range infos {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if info.NoSelect {
			continue
		}

		row, known := byName[info.Name]
		if !known {
			// A folder created since the last discovery. It is a divergence by
			// definition — the tree itself is stale — and the sweep below fixes
			// it by re-discovering and backfilling.
			res.Checked++
			res.Diverged++
			res.Divergences = append(res.Divergences, Divergence{
				Mailbox: info.Name,
				Reason:  "mailbox is not stored locally",
			})
			continue
		}
		res.Checked++

		if !info.HasStatus {
			// Without LIST-STATUS there is nothing to compare. Skipping is
			// correct rather than defaulting to "diverged": a server that does
			// not report counters would otherwise make every mailbox look
			// broken on every sweep, and the watcher already covers it.
			continue
		}

		reason, diverged := compareMailboxState(row, info)

		// The counters above compare what the server says now against what Moov
		// RECORDED at its last pass. That catches a missed event, but not a
		// pass that recorded a cursor without storing the messages behind it —
		// which is exactly what an interrupted write, or a bug in this engine,
		// would leave behind. Comparing the server's MESSAGES against the rows
		// actually present is the check that looks at the data rather than at
		// the bookkeeping about the data.
		//
		// It is done only when the cheap comparison found nothing, because it
		// costs a query per mailbox while the three counters are already in
		// hand.
		if !diverged && row.BackfillState == store.BackfillComplete {
			total, _, cerr := w.store.CountMailboxMessages(ctx, row.ID)
			if cerr != nil {
				return res, fmt.Errorf("reconciler: counting %q: %w", info.Name, cerr)
			}
			if uint32(total) != info.NumMessages { //nolint:gosec // a mailbox count fits a uint32 by IMAP's own protocol
				reason = fmt.Sprintf("messages stored=%d server=%d", total, info.NumMessages)
				diverged = true
			}
		}

		if !diverged {
			continue
		}
		res.Diverged++
		res.Divergences = append(res.Divergences, Divergence{Mailbox: info.Name, Reason: reason})

		// A divergence means push missed something. That is worth a warning,
		// not a debug line: it is the signal that the primary mechanism is not
		// doing its job, and its rate is what tells an operator whether NOTIFY
		// is healthy (E8).
		log.Warn("reconciler found a divergence; an event was missed",
			"account_id", account.ID, "mailbox", info.Name, "divergence", reason)

		mb := syncMailbox{row: row, info: info}
		var passErr error
		perr := syncer.conns.withConn(ctx, func(c imap.Client) error {
			_, passErr = syncer.incrementalMailbox(ctx, c, account, mb, log)
			return passErr
		})
		switch {
		case perr == nil:
			res.Repaired++
		case isNeedsInitialSync(perr):
			if _, berr := syncer.backfillMailbox(ctx, account, mb, log); berr != nil {
				return res, fmt.Errorf("reconciler: backfilling %q: %w", info.Name, berr)
			}
			res.Repaired++
		default:
			return res, fmt.Errorf("reconciler: repairing %q: %w", info.Name, perr)
		}
	}

	// A mailbox that vanished from the server: it was deleted by another
	// client. The tree is stale in the other direction, and the discovery in
	// the sweep below does not remove rows, so it is reported rather than
	// silently ignored.
	serverNames := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		serverNames[info.Name] = struct{}{}
	}
	for _, row := range stored {
		if _, ok := serverNames[row.Name]; !ok {
			res.Diverged++
			res.Divergences = append(res.Divergences, Divergence{
				Mailbox: row.Name,
				Reason:  "mailbox no longer exists on the server",
			})
			log.Warn("reconciler: a stored mailbox is gone from the server",
				"account_id", account.ID, "mailbox", row.Name)
		}
	}

	// New or missing folders need the discovery pass, which also backfills what
	// it finds. Running it only when something structural changed keeps the
	// ordinary sweep to one LIST-STATUS.
	if res.needsDiscovery() {
		if err := w.sweepAll(ctx, syncer, account, log, "reconciler-tree-change"); err != nil {
			return res, err
		}
		res.Repaired = res.Diverged
	}

	res.Elapsed = time.Since(started)

	if res.Diverged > 0 {
		log.Warn("reconciler sweep repaired divergences",
			"account_id", account.ID, "checked", res.Checked,
			"diverged", res.Diverged, "repaired", res.Repaired,
			"elapsed", res.Elapsed.Round(time.Millisecond))
	} else {
		log.Debug("reconciler sweep found no divergence",
			"account_id", account.ID, "checked", res.Checked,
			"elapsed", res.Elapsed.Round(time.Millisecond))
	}
	return res, nil
}

// needsDiscovery reports whether the sweep found a structural change — a
// mailbox that exists on one side only — which a per-mailbox pass cannot fix.
func (r ReconcileResult) needsDiscovery() bool {
	for _, d := range r.Divergences {
		if strings.HasPrefix(d.Reason, "mailbox is not stored") ||
			strings.HasPrefix(d.Reason, "mailbox no longer exists") {
			return true
		}
	}
	return false
}

// compareMailboxState reports whether the server's counters differ from what
// Moov stored, and in what way.
//
// # Why the comparison is "differs", not "is greater"
//
// A counter that moved BACKWARDS is not impossible and must not be ignored: a
// mailbox recreated with the same name resets UIDNEXT and MESSAGES, and that is
// precisely the UIDVALIDITY case that must trigger a resync. Treating only
// forward movement as divergence would make the one situation that corrupts
// data the one situation the sweep skips.
func compareMailboxState(row store.Mailbox, info imap.MailboxInfo) (string, bool) {
	var reasons []string

	if row.UIDValidity != nil && uidValidityFromDB(*row.UIDValidity) != info.UIDValidity {
		reasons = append(reasons, fmt.Sprintf("uidvalidity %d->%d",
			uidValidityFromDB(*row.UIDValidity), info.UIDValidity))
	}
	if row.UIDNext != nil && uidFromDB(*row.UIDNext) != info.UIDNext {
		reasons = append(reasons, fmt.Sprintf("uidnext %d->%d",
			uidFromDB(*row.UIDNext), info.UIDNext))
	}
	if row.HighestModSeq != nil && modSeqFromDB(*row.HighestModSeq) != info.HighestModSeq {
		reasons = append(reasons, fmt.Sprintf("highestmodseq %d->%d",
			modSeqFromDB(*row.HighestModSeq), info.HighestModSeq))
	}

	if len(reasons) > 0 {
		return strings.Join(reasons, ", "), true
	}
	return "", false
}

// isNeedsInitialSync reports whether an error means the mailbox has no cursor.
func isNeedsInitialSync(err error) bool {
	return errors.Is(err, errMailboxNeedsInitialSync)
}
