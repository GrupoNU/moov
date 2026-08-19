package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// The incremental half of the sync engine (L2 §2.5).
//
// # What "incremental" has to get right
//
// A delta is three separate facts arriving through two different mechanisms,
// and each has a different failure mode if handled carelessly:
//
//   - EXPUNGED messages arrive as VANISHED (EARLIER) — either during the
//     QRESYNC SELECT (the reconnection path) or alongside a CHANGEDSINCE fetch
//     (the live path). Missing one leaves a message visible in Moov that no
//     longer exists in Dovecot: a user clicks a ghost.
//   - CHANGED FLAGS arrive as FETCH responses above the stored modseq. They
//     must land in message_state and nowhere else — that is arbitration A5, and
//     writing them through the message row would rewrite a ~2.2 KB tsv into two
//     GIN indexes for a single toggled bit (S3 H9).
//   - NEW MESSAGES are UIDs at or above the stored UIDNEXT. They are the only
//     part of a delta that needs the full fetch/parse/store pipeline, so they
//     reuse E5's stages verbatim rather than growing a second, subtly different
//     write path.
//
// # Why the modseq cursor is advanced last
//
// The stored HIGHESTMODSEQ is the resume point: everything at or below it is
// believed to be applied. It is therefore written only after the delta it
// describes is committed, exactly like E5's checkpoints. A cursor advanced
// first would, on a crash in between, permanently skip the changes it claimed —
// and unlike a missed backfill window, nothing would ever revisit them. This is
// the single ordering rule the whole incremental path depends on.

// IncrementalResult reports what one incremental pass over one mailbox did.
type IncrementalResult struct {
	Mailbox string

	// New is how many previously unseen messages were fetched and stored.
	New int

	// FlagsUpdated is how many message_state rows had their flags or keywords
	// changed (A5: no message row was touched).
	FlagsUpdated int

	// Vanished is how many messages were tombstoned because the server
	// reported them expunged.
	Vanished int

	// Resynced reports that UIDVALIDITY had changed and the mailbox was
	// invalidated and re-synced from scratch instead of being deltaed.
	Resynced bool

	// FromModSeq and ToModSeq are the cursor's movement across the pass.
	FromModSeq imap.ModSeq
	ToModSeq   imap.ModSeq

	Elapsed time.Duration
}

// Changed reports whether the pass found anything at all, which is what decides
// whether it is worth logging at info level.
func (r IncrementalResult) Changed() bool {
	return r.New > 0 || r.FlagsUpdated > 0 || r.Vanished > 0 || r.Resynced
}

// incrementalMailbox runs one incremental pass over one mailbox on the given
// connection.
//
// It is the reconnection path: it SELECTs with QRESYNC, so the server replays
// the delta since the stored modseq. The live path (FetchChanges on an
// already-selected mailbox) is a strict subset and is what liveDelta below
// does; both converge on applyDelta so there is one place where a delta becomes
// database rows.
func (s *Syncer) incrementalMailbox(
	ctx context.Context,
	c imap.Client,
	account store.Account,
	mb syncMailbox,
	log *slog.Logger,
) (IncrementalResult, error) {
	// Real time rather than Options.Clock: this measures how long the pass
	// took, and Options.Clock exists to pin the recent window's DATE, which
	// tests routinely freeze. A frozen clock would report every pass as
	// instantaneous.
	started := time.Now()
	res := IncrementalResult{Mailbox: mb.info.Name}

	known, storedModSeq := mailboxCursor(mb.row)
	res.FromModSeq = storedModSeq

	// A mailbox Moov has never selected has no delta to ask for: it needs the
	// initial sync, not an incremental pass. Saying so explicitly beats issuing
	// a QRESYNC SELECT with a zero cursor, which the server answers with the
	// entire mailbox as if everything had just changed.
	if known == 0 || storedModSeq == 0 {
		log.Debug("mailbox has no sync cursor; initial sync owns it", "mailbox", mb.info.Name)
		return res, errMailboxNeedsInitialSync
	}

	sel, err := c.SelectQResync(ctx, mb.info.Name, known, storedModSeq)
	if err != nil {
		// A stale session view (the mailbox was deleted and recreated by another
		// client while this connection held it open) is not recoverable on this
		// connection: Dovecot keeps refusing the mailbox for the rest of the
		// session, and neither UNSELECT nor a plain SELECT clears it
		// (imap.ErrMailboxStale). Propagating it unchanged is deliberate — the
		// watcher's reconnection loop is the thing that fixes it, by discarding
		// these connections and dialing new ones, after which the ordinary
		// UIDVALIDITY branch below handles the actual resync.
		return res, fmt.Errorf("selecting %q with qresync: %w", mb.info.Name, err)
	}

	// UIDVALIDITY changed: every local UID for this mailbox names a different
	// message now, so there is no delta to apply — only a rebuild. The blobs
	// survive, so this is far cheaper than it sounds (S2 H8).
	if sel.UIDValidity != known {
		log.Warn("uidvalidity changed during incremental sync; invalidating and resyncing",
			"mailbox", mb.info.Name, "was", known, "now", sel.UIDValidity)
		if err := s.resyncMailbox(ctx, account, mb, log); err != nil {
			return res, err
		}
		res.Resynced = true
		res.Elapsed = time.Since(started)
		return res, nil
	}

	delta := mailboxDelta{
		vanished:   sel.VanishedUIDs,
		highestMod: sel.HighestModSeq,
		uidNext:    sel.UIDNext,
	}

	// The QRESYNC SELECT reports WHAT vanished but replays changed flags as
	// unilateral FETCHes that this client surfaces through FetchChanges. Asking
	// for the changes explicitly is one extra round trip and removes any
	// dependence on unilateral data arriving before the SELECT's tagged OK.
	changed, err := s.collectChanges(ctx, c, storedModSeq)
	if err != nil {
		return res, fmt.Errorf("fetching changes in %q: %w", mb.info.Name, err)
	}
	delta.changed = changed.changed
	delta.vanished = append(delta.vanished, changed.vanished...)

	applied, err := s.applyDelta(ctx, c, account, mb, sel.UIDValidity, delta, log)
	if err != nil {
		return res, err
	}

	res.New, res.FlagsUpdated, res.Vanished = applied.stored, applied.flagsUpdated, applied.vanished
	res.ToModSeq = sel.HighestModSeq
	res.Elapsed = time.Since(started)

	// The cursor moves only now, with everything above committed.
	if err := s.store.SetMailboxSyncState(ctx, mb.row.ID,
		uidValidityToDB(sel.UIDValidity), uidNextToDB(sel.UIDNext),
		modSeqToDB(sel.HighestModSeq)); err != nil {
		return res, fmt.Errorf("advancing sync cursor for %q: %w", mb.info.Name, err)
	}

	if res.Changed() {
		log.Info("incremental pass applied a delta",
			"mailbox", mb.info.Name,
			"new", res.New, "flags_updated", res.FlagsUpdated, "vanished", res.Vanished,
			"modseq", fmt.Sprintf("%d->%d", res.FromModSeq, res.ToModSeq),
			"elapsed", res.Elapsed.Round(time.Millisecond))

		// W4a push hook. It sits AFTER the cursor advance, and only under
		// Changed(), for two reasons: everything the pushed state string will
		// describe is committed by now (a client that reacts by calling
		// /changes cannot outrun its own notification), and a pass that found
		// nothing must not wake every connected browser — the state string it
		// would carry is the one the client already holds.
		s.opts.Broker.Notify(account.ID)
	}
	return res, nil
}

// errMailboxNeedsInitialSync means the mailbox has no usable cursor, so the
// incremental path cannot describe its state and E5's machinery must run first.
var errMailboxNeedsInitialSync = errors.New("sync: mailbox has no sync cursor; initial sync required")

// mailboxDelta is everything one pass learned about a mailbox before any of it
// is written.
//
// Collecting it before applying it keeps the two concerns separable: the wire
// protocol decides what a delta IS, and applyDelta decides what a delta MEANS
// for the store. It also means the live path and the reconnect path — which
// gather the same facts by different commands — share one implementation of the
// half that can corrupt data.
type mailboxDelta struct {
	// changed are the messages whose flags or keywords moved, as the server
	// reported them.
	changed []changedMessage

	// vanished are UIDs the server reported expunged.
	vanished []imap.UID

	// highestMod is the mailbox's modseq after the delta.
	highestMod imap.ModSeq

	// uidNext is the mailbox's UIDNEXT after the delta.
	uidNext imap.UID
}

// changedMessage is one FETCH response of a CHANGEDSINCE pass: identity, flags,
// and the modseq that made it appear in the delta.
type changedMessage struct {
	uid      imap.UID
	modSeq   imap.ModSeq
	flags    store.Flags
	keywords []string
}

// collectChanges drains a FetchChanges iterator into a delta's two halves.
func (s *Syncer) collectChanges(ctx context.Context, c imap.Client, since imap.ModSeq) (mailboxDelta, error) {
	var out mailboxDelta

	it, err := c.FetchChanges(ctx, since)
	if err != nil {
		return out, err
	}
	defer func() { _ = it.Close() }()

	for {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		msg, err := it.Next()
		if err != nil {
			return out, err
		}
		if msg == nil {
			break
		}
		out.changed = append(out.changed, changedMessage{
			uid:      msg.UID,
			modSeq:   msg.ModSeq,
			flags:    storeFlags(msg.Flags),
			keywords: msg.Keywords,
		})
	}

	// Close before reading Vanished: the server may interleave VANISHED with
	// the FETCH responses, so the set is only complete once the command is done
	// (imap.ChangeIter's contract).
	if err := it.Close(); err != nil {
		return out, err
	}
	out.vanished = it.Vanished()
	return out, nil
}

// appliedDelta counts what applyDelta committed.
type appliedDelta struct {
	stored       int
	flagsUpdated int
	vanished     int
}

// applyDelta writes a collected delta to the store.
//
// # The order of the three operations
//
// Tombstones first, then flags, then new messages. It is not arbitrary:
//
//   - Tombstoning first means a message that was expunged AND had its flags
//     changed in the same window ends up deleted rather than resurrected with
//     fresh flags. The server's last word wins, and expunge is final.
//   - New messages last means the pipeline's own idempotency filter sees a
//     store that already reflects the rest of the delta, so a UID that was both
//     "new" and "vanished" is not fetched only to be immediately tombstoned.
func (s *Syncer) applyDelta(
	ctx context.Context,
	c imap.Client,
	account store.Account,
	mb syncMailbox,
	uidValidity uint32,
	delta mailboxDelta,
	log *slog.Logger,
) (appliedDelta, error) {
	var out appliedDelta

	vanished, err := s.applyVanished(ctx, mb, uidValidity, delta.vanished)
	if err != nil {
		return out, err
	}
	out.vanished = vanished

	updated, err := s.applyFlagChanges(ctx, mb, uidValidity, delta.changed, log)
	if err != nil {
		return out, err
	}
	out.flagsUpdated = updated

	stored, err := s.fetchNewMessages(ctx, c, account, mb, uidValidity, delta, log)
	if err != nil {
		return out, err
	}
	out.stored = stored

	return out, nil
}

// applyVanished tombstones the UIDs the server reported expunged.
//
// MarkDeleted rather than DELETE: JMAP Email/changes must keep reporting a
// destroyed message until every client has caught up, so the row survives as a
// tombstone (store.MarkDeleted's doc).
func (s *Syncer) applyVanished(ctx context.Context, mb syncMailbox, uidValidity uint32, vanished []imap.UID) (int, error) {
	uids := dedupeUIDs(vanished)
	if len(uids) == 0 {
		return 0, nil
	}

	ids := make([]int64, 0, len(uids))
	for _, u := range uids {
		ids = append(ids, int64(u))
	}
	if err := s.store.MarkDeleted(ctx, mb.row.ID, int64(uidValidity), ids); err != nil {
		return 0, fmt.Errorf("tombstoning %d messages in %q: %w", len(ids), mb.info.Name, err)
	}
	return len(ids), nil
}

// applyFlagChanges writes changed flags to message_state, and only there.
//
// # This is the A5 path
//
// Every UPDATE here goes through store.UpdateFlags, which touches
// message_state exclusively. The alternative — carrying flags on the messages
// row — measured ~0.58 ms per message because a single changed bit rewrote the
// whole row into two GIN indexes (S3 H9), and flag churn is the dominant write
// pattern of an established mailbox. There is a store test asserting the tsv is
// untouched across a flag update; this is the caller that must keep it true.
//
// # Why unknown UIDs are not an error
//
// A CHANGEDSINCE pass reports every message above the cursor, including ones
// Moov has never stored: a message that arrived and had its flags set before
// this pass ran is both "new" and "changed". Those are handed to the new-message
// path instead, which fetches their bodies properly. Treating them as an error
// here would make an ordinary interleaving fatal.
func (s *Syncer) applyFlagChanges(
	ctx context.Context,
	mb syncMailbox,
	uidValidity uint32,
	changed []changedMessage,
	log *slog.Logger,
) (int, error) {
	if len(changed) == 0 {
		return 0, nil
	}

	uids := make([]int64, 0, len(changed))
	for _, ch := range changed {
		uids = append(uids, int64(ch.uid))
	}

	states, err := s.store.MessageStatesByUID(ctx, mb.row.ID, int64(uidValidity), uids)
	if err != nil {
		return 0, fmt.Errorf("resolving changed uids in %q: %w", mb.info.Name, err)
	}
	if len(states) == 0 {
		return 0, nil
	}

	updates := make([]store.FlagUpdate, 0, len(changed))
	for _, ch := range changed {
		st, ok := states[int64(ch.uid)]
		if !ok {
			// Not stored yet: the new-message path owns it.
			continue
		}
		keywords := sanitizeAll(ch.keywords)
		if st.Flags == ch.flags && sameKeywords(st.Keywords, keywords) {
			// No observable change. Skipping it keeps updated_at — which is the
			// cursor JMAP Email/changes pages through — from moving for a
			// message that did not actually change, which would otherwise make
			// every client re-fetch it.
			continue
		}
		updates = append(updates, store.FlagUpdate{
			MessageID:  st.MessageID,
			Flags:      ch.flags,
			Keywords:   keywords,
			ModSeqSeen: modSeqToDB(ch.modSeq),
		})
	}
	if len(updates) == 0 {
		return 0, nil
	}

	if err := s.store.UpdateFlags(ctx, updates); err != nil {
		return 0, fmt.Errorf("updating flags in %q: %w", mb.info.Name, err)
	}
	log.Debug("flag updates applied", "mailbox", mb.info.Name, "count", len(updates))
	return len(updates), nil
}

// fetchNewMessages fetches and stores the UIDs a delta implies are new.
//
// The candidate set is the UID range from the mailbox's last known UIDNEXT up
// to the one the server reports now, plus any UID the change list named that is
// not stored. The second half matters: a message that arrived and was flagged
// between two passes appears in the CHANGEDSINCE result, and taking only the
// UIDNEXT range would store it without ever noticing that it changed.
//
// fetchAndStore filters what is already present before issuing a fetch, so the
// candidate set may be generous without costing bandwidth.
func (s *Syncer) fetchNewMessages(
	ctx context.Context,
	c imap.Client,
	account store.Account,
	mb syncMailbox,
	uidValidity uint32,
	delta mailboxDelta,
	log *slog.Logger,
) (int, error) {
	candidates := newUIDCandidates(mb.row, delta)
	if len(candidates) == 0 {
		return 0, nil
	}

	// The mailbox is already selected on c by the caller, which is what
	// fetchAndStore requires.
	stats, err := s.fetchAndStore(ctx, c, account, mb, uidValidity, candidates, PhaseIncremental)
	if err != nil {
		return stats.stored, fmt.Errorf("fetching new messages in %q: %w", mb.info.Name, err)
	}
	if stats.failed > 0 {
		log.Warn("incremental pass stored messages that failed to parse (R4)",
			"mailbox", mb.info.Name, "failed", stats.failed)
	}
	return stats.stored, nil
}

// newUIDCandidates builds the UID set that might contain new messages.
func newUIDCandidates(row store.Mailbox, delta mailboxDelta) []imap.UID {
	seen := map[imap.UID]struct{}{}
	var out []imap.UID

	add := func(u imap.UID) {
		if u == 0 {
			return
		}
		if _, dup := seen[u]; dup {
			return
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}

	// The arrival range: from the UIDNEXT Moov last recorded up to the one the
	// server reports now. A stored UIDNEXT of 0 means the mailbox was never
	// selected, which incrementalMailbox has already refused.
	if row.UIDNext != nil && *row.UIDNext > 0 && delta.uidNext > 0 {
		from := uidFromDB(*row.UIDNext)
		for u := from; u > 0 && u < delta.uidNext; u++ {
			add(u)
		}
	}

	// Anything the change list named. Already-stored UIDs are filtered out
	// downstream, so naming them here is free.
	for _, ch := range delta.changed {
		add(ch.uid)
	}

	// A vanished UID must never be re-fetched: the server just said it is gone,
	// and asking for it would return nothing while the tombstone is already
	// written.
	if len(delta.vanished) > 0 {
		gone := make(map[imap.UID]struct{}, len(delta.vanished))
		for _, u := range delta.vanished {
			gone[u] = struct{}{}
		}
		filtered := out[:0]
		for _, u := range out {
			if _, dead := gone[u]; !dead {
				filtered = append(filtered, u)
			}
		}
		out = filtered
	}

	return out
}

// resyncMailbox invalidates a mailbox whose UIDVALIDITY changed and re-runs
// E5's initial sync over it.
//
// It reuses E5's machinery rather than reimplementing a fresh pull: the phases
// already know how to walk a mailbox in resumable windows, and a second
// implementation of "fetch everything" is a second place for the idempotency
// rules to be subtly wrong.
func (s *Syncer) resyncMailbox(ctx context.Context, account store.Account, mb syncMailbox, log *slog.Logger) error {
	if err := s.store.InvalidateMailbox(ctx, mb.row.ID); err != nil {
		return fmt.Errorf("invalidating %q: %w", mb.info.Name, err)
	}
	// The checkpoint has to go with it: a watermark that survives describes UIDs
	// that no longer exist, and the backfill would skip everything below it.
	if err := s.saveMailboxCheckpoint(ctx, account.ID, mb.row.ID, mailboxCheckpoint{}); err != nil {
		return err
	}

	fresh, err := s.store.GetMailbox(ctx, mb.row.ID)
	if err != nil {
		return fmt.Errorf("reloading %q: %w", mb.info.Name, err)
	}
	mb.row = fresh

	// backfillMailbox acquires its own connection from the pool, so the caller's
	// connection is not reused here — which also means the caller's selected
	// mailbox is left alone.
	if _, err := s.backfillMailbox(ctx, account, mb, log); err != nil {
		return fmt.Errorf("resyncing %q: %w", mb.info.Name, err)
	}
	return nil
}

// mailboxCursor reads a mailbox row's QRESYNC resume point.
func mailboxCursor(row store.Mailbox) (uidValidity uint32, modSeq imap.ModSeq) {
	if row.UIDValidity != nil && *row.UIDValidity > 0 {
		uidValidity = uidValidityFromDB(*row.UIDValidity)
	}
	if row.HighestModSeq != nil && *row.HighestModSeq > 0 {
		modSeq = modSeqFromDB(*row.HighestModSeq)
	}
	return uidValidity, modSeq
}

// sameKeywords compares two keyword sets as SETS.
//
// Order is not meaningful — the server may report keywords in any order, and
// the store returns them in whatever order PostgreSQL held them — so comparing
// slices positionally would report a change on every pass for a message whose
// keywords merely got shuffled, and each false positive is an unnecessary write
// plus a spurious entry in every client's change feed.
func sameKeywords(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	counts := make(map[string]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

// dedupeUIDs removes duplicates while preserving order.
//
// VANISHED can reach this package by two routes at once (internal/imap's
// armVanishedCollector), and a duplicate would inflate the tombstone count
// reported to an operator without changing what was written.
func dedupeUIDs(uids []imap.UID) []imap.UID {
	if len(uids) <= 1 {
		return uids
	}
	seen := make(map[imap.UID]struct{}, len(uids))
	out := make([]imap.UID, 0, len(uids))
	for _, u := range uids {
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}
