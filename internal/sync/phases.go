package sync

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// runRecent is phase A: the inbox's recent window (L2 §2.5 step 2).
//
// Finishing this phase is what makes the account usable, so it is the phase the
// "<60 s for a 10k mailbox" acceptance criterion measures. It is deliberately
// narrow — one mailbox, one bounded window — because everything else can wait
// and this cannot.
func (s *Syncer) runRecent(ctx context.Context, account store.Account, boxes []syncMailbox, log *slog.Logger) (phaseStats, error) {
	var stats phaseStats
	started := s.opts.Clock()

	var inbox *syncMailbox
	for i := range boxes {
		if boxes[i].isInbox() {
			inbox = &boxes[i]
			break
		}
	}
	if inbox == nil {
		// No INBOX is pathological but not fatal: the backfill covers every
		// mailbox anyway, so the account still syncs, just without the fast
		// path. Failing here would refuse to sync an account over a folder
		// naming quirk.
		log.Warn("no INBOX found; skipping the recent phase")
		return stats, s.saveAccountPhase(ctx, account.ID, PhaseRecent)
	}

	cutoff := s.opts.Clock().Add(-s.opts.RecentWindow)

	err := s.conns.withConn(ctx, func(c imap.Client) error {
		sel, err := s.selectMailbox(ctx, c, account, inbox)
		if err != nil {
			return err
		}
		if sel.NumMessages == 0 {
			return nil
		}

		cp, err := s.loadMailboxCheckpoint(ctx, account.ID, inbox.row.ID, sel.UIDValidity)
		if err != nil {
			return err
		}
		if cp.RecentDone {
			log.Debug("recent phase already done", "mailbox", inbox.info.Name)
			return nil
		}

		uids, err := s.recentUIDs(ctx, c, sel, cutoff)
		if err != nil {
			return err
		}
		if len(uids) == 0 {
			log.Info("no messages in the recent window", "mailbox", inbox.info.Name,
				"since", cutoff.Format(time.RFC3339))
		}

		// Oldest first inside the window, so that if the phase is interrupted
		// the messages already stored are a contiguous run rather than a
		// scatter, and the backfill's descending watermark still describes it.
		st, err := s.fetchAndStore(ctx, c, account, *inbox, sel.UIDValidity, uids, PhaseRecent)
		stats.add(st)
		if err != nil {
			return err
		}

		// Checkpoint AFTER the data is committed. Both records are updated: the
		// mailbox row is what an operator and the JMAP layer read, the
		// checkpoint is what a resumed run reads.
		cp.RecentDone = true
		if len(uids) > 0 {
			// imap.UID and the checkpoint field are both uint32, so the
			// lowest UID of the window carries over exactly.
			cp.UIDLow = uint32(uids[0])
		}
		if err := s.saveMailboxCheckpoint(ctx, account.ID, inbox.row.ID, cp); err != nil {
			return err
		}
		low := int64(cp.UIDLow)
		if err := s.store.SetBackfillProgress(ctx, inbox.row.ID, store.BackfillRecentDone, &low); err != nil {
			return fmt.Errorf("recording recent progress: %w", err)
		}
		return nil
	})

	stats.elapsed = s.opts.Clock().Sub(started)
	if err != nil {
		return stats, err
	}

	log.Info("recent window synced",
		"mailbox", inbox.info.Name,
		"stored", stats.stored,
		"skipped", stats.skipped,
		"parse_failed", stats.failed,
		"elapsed", stats.elapsed.Round(time.Millisecond),
	)
	return stats, s.saveAccountPhase(ctx, account.ID, PhaseRecent)
}

// recentUIDs finds the UIDs of the messages newer than cutoff, walking
// backwards from the newest.
//
// # Why not SEARCH SINCE
//
// The imap.Client contract (L2 §4.1) exposes no SEARCH, so the window is
// derived client-side: fetch INTERNALDATE for a descending band of UIDs and
// stop at the first one older than the cutoff. That is not a workaround, it is
// the cheaper option — a headers-only fetch of a few hundred UIDs costs one
// round trip, while SEARCH SINCE makes the server walk the whole mailbox's
// index. It also reuses the same descending-window machinery phase B needs, so
// there is one scan primitive instead of two.
//
// The scan relies on UID order correlating with arrival order, which IMAP
// guarantees: UIDs are assigned in ascending order as messages arrive
// (RFC 3501 §2.3.1.1). A message whose Date header is old but which was
// delivered recently still has a high UID and is correctly included — the
// INTERNALDATE, not the header, is what this compares.
func (s *Syncer) recentUIDs(ctx context.Context, c imap.Client, sel imap.SelectResult, cutoff time.Time) ([]imap.UID, error) {
	top := highestUID(sel)
	if top == 0 {
		return nil, nil
	}

	var found []imap.UID
	for high := top; high >= 1; {
		low := imap.UID(1)
		if high > windowSize(s.opts.FetchWindow) {
			low = high - windowSize(s.opts.FetchWindow) + 1
		}

		band, reachedCutoff, err := s.probeWindow(ctx, c, low, high, cutoff)
		if err != nil {
			return nil, err
		}
		// The probe returns ascending; prepending keeps the overall result
		// ascending as the scan walks down.
		found = append(band, found...)

		if reachedCutoff || low == 1 {
			break
		}
		high = low - 1
	}
	return found, nil
}

// probeWindow fetches INTERNALDATE for one descending band and returns the UIDs
// at or after cutoff, plus whether an older message was seen (which means the
// scan can stop).
func (s *Syncer) probeWindow(ctx context.Context, c imap.Client, low, high imap.UID, cutoff time.Time) ([]imap.UID, bool, error) {
	uids := uidRange(low, high)
	if len(uids) == 0 {
		return nil, false, nil
	}

	// Headers are not requested: only the INTERNALDATE decides membership, and
	// fetching header blocks for a band that is mostly outside the window would
	// download data the phase then discards.
	it, err := c.FetchMessages(ctx, uids, imap.FetchSpec{InternalDate: true})
	if err != nil {
		return nil, false, fmt.Errorf("probing uids %d:%d: %w", low, high, err)
	}
	defer func() { _ = it.Close() }()

	var (
		inWindow []imap.UID
		sawOlder bool
	)
	for {
		msg, err := it.Next()
		if err != nil {
			return nil, false, fmt.Errorf("probing uids %d:%d: %w", low, high, err)
		}
		if msg == nil {
			break
		}
		switch {
		case msg.InternalDate.IsZero():
			// A server that did not report one: include the message rather than
			// silently dropping it from the usable phase. A false positive
			// costs one fetch; a false negative hides mail.
			inWindow = append(inWindow, msg.UID)
		case msg.InternalDate.Before(cutoff):
			sawOlder = true
		default:
			inWindow = append(inWindow, msg.UID)
		}
	}
	if err := it.Close(); err != nil {
		return nil, false, fmt.Errorf("probing uids %d:%d: %w", low, high, err)
	}
	return inWindow, sawOlder, nil
}

// runBackfill is phase B: every mailbox, including the inbox's history, in
// descending UID windows with a checkpoint per window (L2 §2.5 step 3).
func (s *Syncer) runBackfill(ctx context.Context, account store.Account, boxes []syncMailbox, log *slog.Logger) (phaseStats, error) {
	var stats phaseStats

	if err := s.saveAccountPhase(ctx, account.ID, PhaseBackfill); err != nil {
		return stats, err
	}

	for i := range boxes {
		mb := boxes[i]
		st, err := s.backfillMailbox(ctx, account, mb, log)
		stats.add(st)
		if err != nil {
			return stats, fmt.Errorf("backfilling %q: %w", mb.info.Name, err)
		}
	}
	return stats, nil
}

// backfillMailbox walks one mailbox from its newest UID down to 1.
//
// Every window is a unit of work whose completion is recorded only after its
// data is committed, which is what makes a kill -9 at any point cost at most
// one window of repeated work and never a message.
func (s *Syncer) backfillMailbox(ctx context.Context, account store.Account, mb syncMailbox, log *slog.Logger) (phaseStats, error) {
	var stats phaseStats

	err := s.conns.withConn(ctx, func(c imap.Client) error {
		sel, err := s.selectMailbox(ctx, c, account, &mb)
		if err != nil {
			return err
		}

		cp, err := s.loadMailboxCheckpoint(ctx, account.ID, mb.row.ID, sel.UIDValidity)
		if err != nil {
			return err
		}
		if cp.Complete {
			log.Debug("backfill already complete", "mailbox", mb.info.Name)
			return nil
		}
		if sel.NumMessages == 0 {
			return s.markComplete(ctx, account.ID, mb, cp)
		}

		// Resume below the watermark: everything from UIDLow upwards is
		// already stored, so the next window ends just under it. A zero
		// watermark means nothing is stored and the walk starts at the top.
		high := highestUID(sel)
		if cp.UIDLow > 0 && imap.UID(cp.UIDLow)-1 < high {
			high = imap.UID(cp.UIDLow) - 1
		}
		if err := s.store.SetBackfillProgress(ctx, mb.row.ID, store.BackfillInProgress, nil); err != nil {
			return fmt.Errorf("marking backfill in progress: %w", err)
		}

		for high >= 1 {
			if err := ctx.Err(); err != nil {
				return err
			}

			low := imap.UID(1)
			if high > windowSize(s.opts.FetchWindow) {
				low = high - windowSize(s.opts.FetchWindow) + 1
			}

			st, err := s.fetchAndStore(ctx, c, account, mb, sel.UIDValidity, uidRange(low, high), PhaseBackfill)
			stats.add(st)
			if err != nil {
				return err
			}

			// Committed, so the watermark may move. Not before.
			cp.UIDLow = uint32(low)
			if err := s.saveMailboxCheckpoint(ctx, account.ID, mb.row.ID, cp); err != nil {
				return err
			}
			windowLow := int64(low)
			if err := s.store.SetBackfillProgress(ctx, mb.row.ID, store.BackfillInProgress, &windowLow); err != nil {
				return fmt.Errorf("recording backfill window: %w", err)
			}

			if low == 1 {
				break
			}
			high = low - 1
		}

		return s.markComplete(ctx, account.ID, mb, cp)
	})
	if err != nil {
		return stats, err
	}

	log.Info("mailbox backfilled",
		"mailbox", mb.info.Name,
		"stored", stats.stored,
		"skipped", stats.skipped,
		"parse_failed", stats.failed,
	)
	return stats, nil
}

// markComplete records that a mailbox's history is fully local.
func (s *Syncer) markComplete(ctx context.Context, accountID int64, mb syncMailbox, cp mailboxCheckpoint) error {
	cp.Complete = true
	cp.UIDLow = 1
	if err := s.saveMailboxCheckpoint(ctx, accountID, mb.row.ID, cp); err != nil {
		return err
	}
	low := int64(1)
	if err := s.store.SetBackfillProgress(ctx, mb.row.ID, store.BackfillComplete, &low); err != nil {
		return fmt.Errorf("marking backfill complete: %w", err)
	}
	return nil
}

// selectMailbox selects a mailbox and reconciles a UIDVALIDITY change.
//
// # The UIDVALIDITY rule (L2 §2.5)
//
// A changed UIDVALIDITY means the server's UIDs no longer refer to what Moov
// stored: the same numbers now name different messages. Every local row for
// this mailbox is therefore void, and continuing would attach new mail to old
// identities — the one failure mode in this engine that produces visibly wrong
// mail rather than merely missing mail.
//
// So the mailbox is invalidated and restarted from scratch. It is not as
// expensive as it sounds: InvalidateMailbox drops the rows but leaves the
// blobs, so the resync recovers content by sha256 without re-downloading
// anything already held (S2 H8).
func (s *Syncer) selectMailbox(ctx context.Context, c imap.Client, account store.Account, mb *syncMailbox) (imap.SelectResult, error) {
	var known uint32
	if mb.row.UIDValidity != nil && *mb.row.UIDValidity > 0 {
		known = uidValidityFromDB(*mb.row.UIDValidity)
	}

	// modSeq 0: this is the initial sync, so there is no delta to replay. The
	// incremental path (E6) is what passes a real modseq here.
	sel, err := c.SelectQResync(ctx, mb.info.Name, known, 0)
	if err != nil {
		return imap.SelectResult{}, fmt.Errorf("selecting %q: %w", mb.info.Name, err)
	}

	changed := known != 0 && sel.UIDValidity != known
	if changed {
		s.opts.Logger.Warn("uidvalidity changed; invalidating mailbox",
			"account_id", account.ID, "mailbox", mb.info.Name,
			"was", known, "now", sel.UIDValidity)

		if err := s.store.InvalidateMailbox(ctx, mb.row.ID); err != nil {
			return imap.SelectResult{}, fmt.Errorf("invalidating %q: %w", mb.info.Name, err)
		}
		// The checkpoint is reset too: loadMailboxCheckpoint would discard it
		// on the UIDVALIDITY mismatch anyway, but leaving a stale record behind
		// makes an operator's reading of sync_log wrong.
		if err := s.saveMailboxCheckpoint(ctx, account.ID, mb.row.ID, mailboxCheckpoint{
			UIDValidity: sel.UIDValidity,
		}); err != nil {
			return imap.SelectResult{}, err
		}

		fresh, err := s.store.GetMailbox(ctx, mb.row.ID)
		if err != nil {
			return imap.SelectResult{}, fmt.Errorf("reloading %q: %w", mb.info.Name, err)
		}
		mb.row = fresh
	}

	if err := s.store.SetMailboxSyncState(ctx, mb.row.ID,
		uidValidityToDB(sel.UIDValidity), uidNextToDB(sel.UIDNext),
		modSeqToDB(sel.HighestModSeq)); err != nil {
		return imap.SelectResult{}, fmt.Errorf("recording sync state for %q: %w", mb.info.Name, err)
	}

	return sel, nil
}

// highestUID is the highest UID that can exist in a selected mailbox.
//
// UIDNEXT is the UID the next message will get, so the highest existing one is
// one below it. When the server did not report UIDNEXT, the message count is
// the floor — every mailbox has at least as many UIDs as messages, and a scan
// that starts too low simply finds nothing in its first windows.
func highestUID(sel imap.SelectResult) imap.UID {
	if sel.UIDNext > 1 {
		return sel.UIDNext - 1
	}
	return imap.UID(sel.NumMessages)
}

// uidRange materializes an inclusive UID range in ascending order.
//
// A dense range is correct even when most of its numbers name no message: IMAP
// simply returns nothing for a UID that does not exist, and the alternative —
// asking the server which UIDs exist first — is an extra round trip per window
// to save bytes the server does not send anyway.
func uidRange(low, high imap.UID) []imap.UID {
	if high < low {
		return nil
	}
	out := make([]imap.UID, 0, high-low+1)
	for u := low; ; u++ {
		out = append(out, u)
		if u == high {
			break
		}
	}
	return out
}
