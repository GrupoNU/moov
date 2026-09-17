package sync

import (
	"context"
	"encoding/json"
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

	// Repaired is how many of those a repair pass then actually fixed —
	// VERIFIED by re-deriving the same comparison that found them, not
	// inferred from a pass having returned no error.
	//
	// The distinction is the whole point of the 2026-09-17 fix. The old field
	// counted "the repair function did not fail", which for a message lost
	// BELOW the cursor is true on every sweep forever while nothing changes.
	// An operator reading `repaired=1` every two minutes was reading a number
	// that meant nothing.
	Repaired int

	// Unrepaired is how many divergences survived the repair attempt. It is
	// the honest half of the pair: Repaired+Unrepaired == Diverged for every
	// divergence the sweep tried to fix.
	Unrepaired int

	// Escalated is how many mailboxes were escalated from the cheap
	// incremental pass to a full backfill walk, which is the only repair that
	// can close a gap below the cursor.
	Escalated int

	// Stuck names the mailboxes whose divergence persisted after the attempt,
	// with the reason it persisted. It is what makes a stuck mailbox visible
	// to a test and to the metric seam, rather than a number with no subject.
	Stuck []Divergence

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

	// The per-sweep escalation budget (part of keeping this safe BY
	// CONSTRUCTION on a real account rather than by hoping).
	//
	// The backoff below is per-MAILBOX, which bounds how often one broken
	// folder is walked but says nothing about how many folders may be walked
	// at once. The account that produced this incident has 24 mailboxes and
	// 26,869 messages; a first sweep after a deploy that found several of them
	// diverged would, unbudgeted, walk several 20k-message folders back to
	// back in one pass — turning a fix for a silent no-op into a thundering
	// herd against the Dovecot this engine exists not to disturb (ADR §4).
	//
	// One walk per sweep. Nothing is skipped permanently: a mailbox that did
	// not get the budget this time is still DETECTED, still counted as
	// unrepaired, still logged at WARN by name, and gets the budget on the next
	// sweep — which at the current 15-minute interval means the worst case for
	// an account with every folder broken is one folder repaired per quarter
	// hour, in a situation where the alternative was zero forever.
	budget := 1

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
			// A mailbox that agrees with the server has recovered, whatever
			// the reason — the condition cleared on its own, a deploy fixed
			// the parser, an operator intervened. Its escalation bound must go
			// with it: the backoff exists to stop a HOPELESS mailbox from
			// being walked forever, not to punish one that had a bad
			// afternoon. Left standing, a folder that failed once in August
			// would still be serving out a 24-hour backoff in September while
			// real mail went missing.
			//
			// It is cleared here, on the healthy path, rather than only in
			// repairSucceeded, because recovery usually arrives as a sweep
			// that finds NOTHING — which never reaches a repair at all.
			if err := w.clearEscalation(ctx, account.ID, row.ID); err != nil {
				return res, err
			}
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
		outcome, rerr := w.repairMailbox(ctx, syncer, account, mb, log, &budget)
		if rerr != nil {
			return res, rerr
		}
		if outcome.escalated {
			res.Escalated++
		}
		if outcome.repaired {
			res.Repaired++
			continue
		}

		res.Unrepaired++
		res.Stuck = append(res.Stuck, Divergence{Mailbox: info.Name, Reason: outcome.reason})

		// The line that the old code could not produce, and whose absence is
		// what let this run for five weeks. It says the repair was attempted
		// and did NOT work, names the mailbox, and carries the reason that is
		// STILL true — so the next question ("what is different about this
		// mailbox?") has somewhere to start.
		log.Warn("reconciler could not repair a divergence; it persists",
			"account_id", account.ID, "mailbox", info.Name,
			"divergence", outcome.reason, "escalated", outcome.escalated,
			"attempts", outcome.attempts, "next_escalation_after", outcome.backoff)

		w.emit(WatchObservation{
			AccountID: account.ID,
			Kind:      ObsStuckDivergence,
			Mailbox:   info.Name,
		})
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
		// The structural divergences — a folder on one side only — are what
		// the sweep above exists to fix, and discovery genuinely fixes them:
		// it creates the missing rows and backfills them. So they, and only
		// they, count as repaired here. Per-mailbox divergences keep whatever
		// verdict their own verification gave them, because sweepAll runs the
		// SAME incremental pass that already failed to help them — crediting
		// it with their repair is the exact mistake this commit removes.
		res.Repaired += res.structuralCount()
	}

	res.Elapsed = time.Since(started)

	if res.Diverged > 0 {
		log.Warn("reconciler sweep found divergences",
			"account_id", account.ID, "checked", res.Checked,
			"diverged", res.Diverged, "repaired", res.Repaired,
			"unrepaired", res.Unrepaired, "escalated", res.Escalated,
			"elapsed", res.Elapsed.Round(time.Millisecond))
	} else {
		log.Debug("reconciler sweep found no divergence",
			"account_id", account.ID, "checked", res.Checked,
			"elapsed", res.Elapsed.Round(time.Millisecond))
	}
	return res, nil
}

// Repair, verification and the escalation bound (the 2026-09-17 defect).
//
// # The defect
//
// Account diego@gruponu.com had 24,147 messages in INBOX on Dovecot and 24,146
// in Moov. Exactly one UID was missing — 23840, from five weeks earlier — and
// it was absent from message_state entirely: some one-off failure had dropped
// it. The reconciler found the count mismatch on every sweep, ran an
// incremental pass, and reported `repaired=1`. Overnight it did this 906 times
// with zero errors and repaired nothing.
//
// Two separate bugs, and the second is the dangerous one:
//
//  1. incrementalMailbox CANNOT fix that class of divergence. It SELECTs with
//     QRESYNC from the STORED CURSOR and applies the delta above it; a UID far
//     below the cursor is, by construction, not in any delta it will ever be
//     shown. The repair was structurally incapable of working.
//  2. The result counted a repair whenever the pass returned NO ERROR. It never
//     looked at the store afterwards. A count of "repaired" that does not
//     verify the repair is worse than no count, because it is precisely the
//     number an operator trusts when deciding whether to investigate.
//
// # The fix, in two halves
//
// VERIFY: after the attempt, re-derive the same comparison that detected the
// divergence — a fresh STATUS of that one mailbox against the freshly reloaded
// row, plus the stored-row count. Only a divergence that is actually GONE is
// counted as repaired. This costs one STATUS and one count query, and only for
// a mailbox that already diverged, so a healthy sweep pays nothing.
//
// ESCALATE: a divergence that survives an incremental pass is proof the cursor
// is ahead of the gap. backfillMailbox is the repair that can close it, because
// it walks the mailbox from its top UID down to 1 rather than following the
// cursor. So verification failure escalates to a backfill — once, and then
// under a bound.
//
// # Why the escalation must be bounded, and how
//
// Backfill is the expensive hammer: on the 24k-message INBOX that produced this
// incident it is a full descending walk of the mailbox. And it is not
// guaranteed to work either — a message the parser refuses, or a UID Dovecot
// lists in STATUS but will not FETCH, diverges for a reason no amount of
// walking fixes. Unbounded, the fix would replace "a useless pass every two
// minutes" with "a full mailbox walk every two minutes", which is strictly
// worse: the same false hope, and far more load on the Dovecot the whole engine
// is built not to disturb (ADR §4).
//
// The bound chosen is EXPONENTIAL BACKOFF ON A PERSISTED PER-MAILBOX COUNTER,
// with a hard ceiling:
//
//   - The counter and the last-attempt time live in their own sync_log row,
//     scoped by mailbox ID so a rename cannot orphan it. Persisting matters:
//     the failure this bounds outlives process restarts — the production gap
//     was five weeks old — and an in-memory counter would reset to zero on
//     every deploy and re-authorize the hammer.
//   - The delay is escalationBase << (failures-1), capped at escalationMax. So
//     the FIRST failure escalates immediately (the common case is a real gap
//     that a single backfill closes, and making a user wait an hour for it
//     would be absurd), the second waits 15 minutes, then 30, then an hour, and
//     thereafter a day.
//   - The counter is cleared the moment verification succeeds, so a mailbox
//     that recovers pays nothing on its next incident.
//   - It advances only when a walk actually RAN. A sweep that skipped the walk
//     because the backoff had not elapsed must not push the backoff further
//     out, or a mailbox checked every two minutes would reach the daily ceiling
//     within the hour without one walk having been attempted.
//
// A SECOND bound sits above it and is independent: at most one backfill walk
// per sweep, across all of an account's mailboxes. The backoff bounds how often
// one folder is walked; the per-sweep budget bounds how many folders may be
// walked at once, which on a 24-mailbox account is the difference between a
// repair and a thundering herd. See the budget declared in Reconcile.
//
// A ceiling of one day rather than "stop forever" is deliberate: a mailbox that
// is genuinely unrepairable should keep saying so in the WARN and in the
// metric, and a condition that clears on its own — a transient Dovecot refusal,
// a parser fixed by a deploy — should heal without an operator having to reset
// anything by hand. Once a day is cheap enough to be invisible and frequent
// enough to self-heal.
//
// # Why escalation cannot lose data
//
// backfillMailbox re-fetches UIDs and stores them through the same idempotent
// path the initial sync uses: a UID already present is skipped, never
// rewritten, so a redundant walk over a 27k-message account costs fetches and
// changes nothing a user can see. Nothing is deleted and nothing is reset.
// UIDVALIDITY is checked by loadMailboxCheckpoint before any watermark is
// trusted, so a recreated mailbox cannot have a stale watermark applied to it.
// The one piece of state the walk must move is the checkpoint Complete flag,
// which backfillMailbox short-circuits on — and an escalation interrupted after
// clearing it leaves the mailbox marked in-progress, which is honest and which
// the next sweep resumes.

const (
	// escalationBase is the delay before the SECOND backfill escalation of the
	// same mailbox. The first one is immediate.
	escalationBase = 15 * time.Minute

	// escalationMax is the ceiling on the backoff: a mailbox that has resisted
	// repair many times is retried once a day.
	escalationMax = 24 * time.Hour
)

// repairOutcome is what one repair attempt achieved, verified.
type repairOutcome struct {
	// repaired is true only when the divergence is GONE, re-derived from the
	// server and the store after the attempt.
	repaired bool

	// reason is the divergence that remains, when it remains.
	reason string

	// escalated records that this attempt included a full backfill walk.
	escalated bool

	// attempts is how many consecutive failed repairs this mailbox has now had,
	// and backoff how long before the next escalation is allowed. Both are for
	// the log line: they are the difference between "this just broke" and "this
	// has been broken for a week".
	attempts int
	backoff  time.Duration
}

// repairMailbox attempts to repair one diverged mailbox and VERIFIES the result.
//
// It returns an error only for a genuine failure to talk to the server or the
// store. A repair that simply did not work is not an error — it is an outcome,
// and reporting it as one rather than swallowing it is the entire point.
func (w *PushWatcher) repairMailbox(
	ctx context.Context,
	syncer *Syncer,
	account store.Account,
	mb syncMailbox,
	log *slog.Logger,
	budget *int,
) (repairOutcome, error) {
	var out repairOutcome

	// The cheap repair first: it is what fixes the ordinary missed event, which
	// is the overwhelmingly common case and the reason the sweep exists at all.
	var passErr error
	perr := syncer.conns.withConn(ctx, func(c imap.Client) error {
		_, passErr = syncer.incrementalMailbox(ctx, c, account, mb, log)
		return passErr
	})
	switch {
	case perr == nil:
		// Fall through to verification: a pass that returned nil has proved
		// nothing whatsoever about the store.
	case isNeedsInitialSync(perr):
		// No cursor at all: the incremental path never applied. This is the one
		// case where a backfill is the FIRST repair rather than an escalation,
		// and it has always been handled here.
		if _, berr := syncer.backfillMailbox(ctx, account, mb, log); berr != nil {
			return out, fmt.Errorf("reconciler: backfilling %q: %w", mb.info.Name, berr)
		}
		// It spends the budget: it is the same expensive walk, and a sweep that
		// found ten never-synced mailboxes would otherwise walk all ten.
		*budget--
		out.escalated = true
	default:
		return out, fmt.Errorf("reconciler: repairing %q: %w", mb.info.Name, perr)
	}

	reason, still, err := w.stillDiverged(ctx, syncer, account, mb)
	if err != nil {
		return out, err
	}
	if !still {
		o, serr := w.repairSucceeded(ctx, account, mb)
		o.escalated = out.escalated
		return o, serr
	}

	// It survived. If a backfill already ran above there is nothing cheaper
	// left to try, so record the failure and report it honestly.
	if out.escalated {
		o, ferr := w.repairFailed(ctx, account, mb, reason, true)
		o.escalated = true
		return o, ferr
	}

	if *budget <= 0 {
		// Out of budget for this sweep. The divergence is still reported and
		// still counted; it simply waits for the next sweep to be walked. The
		// escalation bound is deliberately NOT advanced, because no walk ran.
		log.Debug("reconciler: the sweep's escalation budget is spent; deferring the walk",
			"mailbox", mb.info.Name)
		return w.repairFailed(ctx, account, mb, reason, false)
	}

	allowed, failures, wait, err := w.escalationAllowed(ctx, account, mb)
	if err != nil {
		return out, err
	}
	if !allowed {
		log.Debug("reconciler: the backfill escalation is backed off",
			"mailbox", mb.info.Name, "failures", failures, "wait", wait)
		o, ferr := w.repairFailed(ctx, account, mb, reason, false)
		o.backoff = wait
		return o, ferr
	}

	// The escalation: walk the mailbox rather than follow the cursor.
	log.Warn("reconciler: a divergence survived an incremental pass; escalating to a backfill walk",
		"account_id", account.ID, "mailbox", mb.info.Name,
		"divergence", reason, "previous_failures", failures)

	*budget--
	if err := w.backfillFromTop(ctx, syncer, account, mb, log); err != nil {
		return out, err
	}

	reason, still, err = w.stillDiverged(ctx, syncer, account, mb)
	if err != nil {
		return out, err
	}
	if !still {
		o, serr := w.repairSucceeded(ctx, account, mb)
		o.escalated = true
		return o, serr
	}
	o, ferr := w.repairFailed(ctx, account, mb, reason, true)
	o.escalated = true
	return o, ferr
}

// stillDiverged re-derives the divergence check against fresh server and store
// state.
//
// It deliberately re-READS the mailbox row rather than reusing the one the
// sweep started with: the repair pass has just advanced that row cursor, and
// comparing a fresh STATUS against a stale row would report a divergence the
// repair had in fact fixed — turning the honest counter into a different lie.
func (w *PushWatcher) stillDiverged(
	ctx context.Context,
	syncer *Syncer,
	account store.Account,
	mb syncMailbox,
) (string, bool, error) {
	var info imap.MailboxInfo
	if err := syncer.conns.withConn(ctx, func(c imap.Client) error {
		var err error
		info, err = c.StatusMailbox(ctx, mb.info.Name)
		return err
	}); err != nil {
		return "", false, fmt.Errorf("reconciler: re-checking %q: %w", mb.info.Name, err)
	}
	if !info.HasStatus {
		// Nothing to compare, exactly as in the detecting pass. Reporting the
		// divergence as persisting here would make every sweep against a server
		// without STATUS counters escalate to a backfill.
		return "", false, nil
	}

	row, err := w.store.GetMailboxByName(ctx, account.ID, mb.info.Name)
	if err != nil {
		return "", false, fmt.Errorf("reconciler: re-reading %q: %w", mb.info.Name, err)
	}

	if reason, diverged := compareMailboxState(row, info); diverged {
		return reason, true, nil
	}

	// The data check, which is the one that catches this incident shape: the
	// bookkeeping can agree perfectly while a row is simply absent.
	if row.BackfillState == store.BackfillComplete {
		total, _, cerr := w.store.CountMailboxMessages(ctx, row.ID)
		if cerr != nil {
			return "", false, fmt.Errorf("reconciler: re-counting %q: %w", mb.info.Name, cerr)
		}
		if uint32(total) != info.NumMessages { //nolint:gosec // a mailbox count fits a uint32 by IMAP's own protocol
			return fmt.Sprintf("messages stored=%d server=%d", total, info.NumMessages), true, nil
		}
	}
	return "", false, nil
}

// backfillFromTop runs a full descending walk of one mailbox even though its
// checkpoint claims the backfill is complete.
//
// backfillMailbox returns immediately when cp.Complete is set, which is right
// for every ordinary caller and wrong for exactly this one: "complete" is the
// claim verification has just disproved. The flag is cleared in the persisted
// record before the walk and set again by the walk itself (markComplete runs at
// the end), so an escalation interrupted in between leaves the mailbox marked
// in-progress — honest, and resumable by the next sweep.
func (w *PushWatcher) backfillFromTop(
	ctx context.Context,
	syncer *Syncer,
	account store.Account,
	mb syncMailbox,
	log *slog.Logger,
) error {
	// The row is re-read rather than taken from mb: the incremental pass that
	// just ran may have resynced this mailbox under a new UIDVALIDITY, and
	// loading the checkpoint against the old one would hand back a watermark
	// that names UIDs which no longer exist.
	row, err := w.store.GetMailboxByName(ctx, account.ID, mb.info.Name)
	if err != nil {
		return fmt.Errorf("reconciler: re-reading %q: %w", mb.info.Name, err)
	}

	cp, err := syncer.loadMailboxCheckpoint(ctx, account.ID, row.ID,
		uidValidityFromDB(row.UIDValidityOrZero()))
	if err != nil {
		return fmt.Errorf("reconciler: reading the checkpoint of %q: %w", mb.info.Name, err)
	}
	cp.Complete = false
	cp.UIDLow = 0
	if err := syncer.saveMailboxCheckpoint(ctx, account.ID, row.ID, cp); err != nil {
		return fmt.Errorf("reconciler: rewinding the checkpoint of %q: %w", mb.info.Name, err)
	}

	// The walk uses the FRESH row too, so its SELECT and its checkpoint writes
	// agree with what was just loaded.
	mb.row = row
	if _, err := syncer.backfillMailbox(ctx, account, mb, log); err != nil {
		return fmt.Errorf("reconciler: backfilling %q: %w", mb.info.Name, err)
	}
	return nil
}

// escalationState is the persisted bound on backfill escalations for one
// mailbox.
type escalationState struct {
	Version int `json:"version"`

	// Failures is how many consecutive verified-failed repairs this mailbox has
	// had. Zero after any success.
	Failures int `json:"failures"`

	// LastAttempt is when the most recent backfill escalation ran.
	LastAttempt time.Time `json:"last_attempt"`
}

// escalationScope names the sync_log row holding one mailbox escalation bound.
//
// It is SEPARATE from mailboxScope because that row payload is the backfill
// watermark, owned by a different code path that rewrites it on every window —
// sharing one row would make the bound a casualty of ordinary progress, and
// backfillFromTop rewrites that very record on the way in.
func escalationScope(mailboxID int64) string {
	return fmt.Sprintf("reconcile:%d", mailboxID)
}

// escalationAllowed reports whether this mailbox may be escalated to a backfill
// now, how many failures it has, and how long until it may be if not.
func (w *PushWatcher) escalationAllowed(
	ctx context.Context,
	account store.Account,
	mb syncMailbox,
) (bool, int, time.Duration, error) {
	st, err := w.loadEscalation(ctx, account.ID, mb.row.ID)
	if err != nil {
		return false, 0, 0, err
	}
	if st.Failures <= 0 || st.LastAttempt.IsZero() {
		return true, st.Failures, 0, nil
	}

	delay := escalationMax
	if st.Failures < 32 {
		if d := escalationBase << (st.Failures - 1); d > 0 && d < escalationMax {
			delay = d
		}
	}

	// Real time rather than Options.Clock: this is a rate limit on load against
	// a real server, and a test that pins the clock to a fixed instant must not
	// be able to turn it into "never" or into "always".
	elapsed := time.Since(st.LastAttempt)
	if elapsed >= delay {
		return true, st.Failures, 0, nil
	}
	return false, st.Failures, delay - elapsed, nil
}

// repairSucceeded clears the escalation bound and reports the success.
func (w *PushWatcher) repairSucceeded(
	ctx context.Context,
	account store.Account,
	mb syncMailbox,
) (repairOutcome, error) {
	if err := w.clearEscalation(ctx, account.ID, mb.row.ID); err != nil {
		return repairOutcome{}, err
	}
	return repairOutcome{repaired: true}, nil
}

// repairFailed records the failure against the bound and reports it.
//
// The counter advances only when an escalation actually RAN; see the escalation
// commentary above for why a skipped walk must not push the backoff out.
func (w *PushWatcher) repairFailed(
	ctx context.Context,
	account store.Account,
	mb syncMailbox,
	reason string,
	escalated bool,
) (repairOutcome, error) {
	st, err := w.loadEscalation(ctx, account.ID, mb.row.ID)
	if err != nil {
		return repairOutcome{}, err
	}
	if escalated {
		st.Failures++
		st.LastAttempt = time.Now()
		if err := w.saveEscalation(ctx, account.ID, mb.row.ID, st); err != nil {
			return repairOutcome{}, err
		}
	}
	return repairOutcome{reason: reason, attempts: st.Failures}, nil
}

// loadEscalation reads a mailbox escalation bound, treating anything unreadable
// as "no failures yet".
//
// Unreadable means only that this process cannot use the record, and the safe
// response is to allow one walk: a redundant backfill is idempotent and costs
// fetches, while refusing to escalate over a decoding quirk would leave a real
// gap unrepaired forever — the exact failure this whole change exists to end.
func (w *PushWatcher) loadEscalation(ctx context.Context, accountID, mailboxID int64) (escalationState, error) {
	cp, err := w.store.GetCheckpoint(ctx, accountID, escalationScope(mailboxID))
	if err != nil {
		return escalationState{}, fmt.Errorf("reconciler: reading the escalation bound: %w", err)
	}
	var st escalationState
	if uerr := json.Unmarshal(cp.Checkpoint, &st); uerr != nil || st.Version != checkpointVersion {
		// Swallowing the decode error is the documented behavior above, not an
		// oversight: an unusable record must mean "allow one walk", never
		// "refuse to repair this mailbox".
		return escalationState{}, nil //nolint:nilerr // an unreadable bound means no bound; see the doc comment
	}
	return st, nil
}

// clearEscalation forgets a mailbox escalation bound.
//
// # Why this is free for a healthy mailbox
//
// It is called for EVERY mailbox that agrees with the server, on every sweep of
// every account — which is nearly all of them, nearly all the time. The sweep's
// whole affordability claim is "one LIST-STATUS and a handful of local reads"
// (TestReconcilerIsQuietWhenNothingDiverged pins it), and a bound that cost one
// checkpoint SELECT per mailbox per sweep would quietly spend that budget on
// bookkeeping about a failure that has never happened.
//
// So the mailboxes that actually carry a bound are tracked in memory, and a
// mailbox absent from that set is dismissed without touching the database. The
// set is authoritative in the only direction that matters: saveEscalation is
// the sole writer of these rows, and it records into the set, so a bound this
// process wrote is a bound this process knows about.
//
// Losing the set on restart is harmless, and deliberately so. A stale row then
// survives until its mailbox next diverges — at which point escalationAllowed
// reads it from the database as it always does, applies the backoff it
// describes, and the first success clears it for real. The cost of the
// forgetting is one deferred walk on one mailbox after a deploy; the cost of
// remembering perfectly would be a query per mailbox per sweep forever.
func (w *PushWatcher) clearEscalation(ctx context.Context, accountID, mailboxID int64) error {
	if !w.hasEscalation(mailboxID) {
		return nil
	}
	return w.saveEscalation(ctx, accountID, mailboxID, escalationState{})
}

// hasEscalation reports whether this process has written a bound for a mailbox
// and not yet cleared it.
func (w *PushWatcher) hasEscalation(mailboxID int64) bool {
	w.escMu.Lock()
	defer w.escMu.Unlock()
	_, ok := w.escalated[mailboxID]
	return ok
}

// saveEscalation records a mailbox escalation bound.
func (w *PushWatcher) saveEscalation(ctx context.Context, accountID, mailboxID int64, st escalationState) error {
	st.Version = checkpointVersion
	payload, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("reconciler: encoding the escalation bound: %w", err)
	}
	if err := w.store.SaveCheckpoint(ctx, accountID, escalationScope(mailboxID), payload); err != nil {
		return fmt.Errorf("reconciler: saving the escalation bound: %w", err)
	}

	// Record what was just written, so clearEscalation can dismiss a healthy
	// mailbox without a query. Only a row with an actual failure in it counts:
	// writing the zero value IS the clear.
	w.escMu.Lock()
	if st.Failures > 0 {
		if w.escalated == nil {
			w.escalated = map[int64]struct{}{}
		}
		w.escalated[mailboxID] = struct{}{}
	} else {
		delete(w.escalated, mailboxID)
	}
	w.escMu.Unlock()
	return nil
}

// structuralCount is how many divergences the discovery sweep is the fix for.
func (r ReconcileResult) structuralCount() int {
	var n int
	for _, d := range r.Divergences {
		if isStructuralReason(d.Reason) {
			n++
		}
	}
	return n
}

// isStructuralReason reports whether a divergence is a mailbox that exists on
// one side only.
func isStructuralReason(reason string) bool {
	return strings.HasPrefix(reason, "mailbox is not stored") ||
		strings.HasPrefix(reason, "mailbox no longer exists")
}

// needsDiscovery reports whether the sweep found a structural change — a
// mailbox that exists on one side only — which a per-mailbox pass cannot fix.
func (r ReconcileResult) needsDiscovery() bool {
	for _, d := range r.Divergences {
		if isStructuralReason(d.Reason) {
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
