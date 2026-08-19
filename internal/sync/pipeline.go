package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/parser"
	"github.com/GrupoNU/moov/internal/store"
)

// phaseStats accumulates what one phase did.
type phaseStats struct {
	stored  int
	skipped int
	failed  int
	elapsed time.Duration
}

func (p *phaseStats) add(other phaseStats) {
	p.stored += other.stored
	p.skipped += other.skipped
	p.failed += other.failed
}

// rawMessage is a message after the fetch stage: bytes already durable in the
// blob store, still unparsed.
//
// The bytes are read out of the IMAP literal and into the blob store
// immediately, before anything else touches them, for two reasons. The first is
// the contract: imap.Message.Body is valid only until the iterator advances, so
// deferring the read is a use-after-free. The second is the L2 §2.4 rule that
// the raw blob is persisted FIRST and parsing is a retryable derivation — a
// crash after this point loses no mail, only work.
type rawMessage struct {
	uid          imap.UID
	modSeq       imap.ModSeq
	flags        store.Flags
	keywords     []string
	internalDate time.Time
	hash         blob.Hash
	size         int64

	// raw is the message bytes, kept in memory for the parse stage. Bounded by
	// the pipeline's queue depth, not by the mailbox size.
	raw []byte
}

// parsedMessage is a rawMessage after the parse stage.
type parsedMessage struct {
	raw    rawMessage
	parsed parser.ParsedMessage
}

// fetchAndStore runs the whole pipeline for one set of UIDs in one mailbox, and
// returns what it committed.
//
// The mailbox must already be selected on c. uids must be non-empty and are
// fetched in the order given.
func (s *Syncer) fetchAndStore(
	ctx context.Context,
	c imap.Client,
	account store.Account,
	mb syncMailbox,
	uidValidity uint32,
	uids []imap.UID,
	phase Phase,
) (phaseStats, error) {
	var stats phaseStats
	if len(uids) == 0 {
		return stats, nil
	}

	// Filter out what is already stored BEFORE fetching: on a resumed run most
	// of a window is usually already present, and re-downloading it would make
	// resume cost the same as a first run. This is also the idempotency
	// guarantee — InsertMessages has no ON CONFLICT clause, so a duplicate UID
	// would hit the unique index on (mailbox_id, uidvalidity, uid) and abort
	// the whole batch.
	wanted, alreadyStored, err := s.filterStored(ctx, mb.row.ID, uidValidity, uids)
	if err != nil {
		return stats, err
	}
	stats.skipped += alreadyStored
	if len(wanted) == 0 {
		return stats, nil
	}

	// The three stages run concurrently with bounded queues between them. The
	// queue depth is one batch: enough that the parse pool always has work
	// while the fetch is on the wire, small enough that memory is bounded by
	// batch size times message size rather than by the mailbox.
	rawCh := make(chan rawMessage, s.opts.BatchSize)
	parsedCh := make(chan parsedMessage, s.opts.BatchSize)

	// A pipeline-local context so that a failure in any stage tears the others
	// down instead of leaving them blocked on a channel nobody will read.
	pipeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg        sync.WaitGroup
		fetchErr  error
		writeErr  error
		writeStat phaseStats

		parseMu  sync.Mutex
		parseErr error
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(rawCh)
		fetchErr = s.fetchStage(pipeCtx, c, wanted, rawCh)
		if fetchErr != nil {
			cancel()
		}
	}()

	// The parse pool is CPU-bound (S3 H6) and sized from GOMAXPROCS, never per
	// account: on a bulk migration the accounts share these workers rather than
	// each spawning its own pool and thrashing the scheduler.
	var parseWG sync.WaitGroup
	for range s.opts.ParseWorkers {
		parseWG.Add(1)
		go func() {
			defer parseWG.Done()
			if err := s.parseStage(pipeCtx, rawCh, parsedCh); err != nil {
				parseMu.Lock()
				if parseErr == nil {
					parseErr = err
				}
				parseMu.Unlock()
				cancel()
			}
		}()
	}
	go func() {
		parseWG.Wait()
		close(parsedCh)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		writeStat, writeErr = s.writeStage(pipeCtx, account, mb, uidValidity, parsedCh, phase)
		if writeErr != nil {
			cancel()
		}
	}()

	wg.Wait()
	stats.add(writeStat)

	// Error precedence: report the CAUSE, not the consequence. When a stage
	// fails it cancels the pipeline context, so the other stages also end with
	// context.Canceled — returning that instead of the original would replace a
	// diagnosable failure with a meaningless one. So a non-cancellation error
	// from any stage wins, and cancellation is only reported when nothing else
	// went wrong.
	parseMu.Lock()
	pErr := parseErr
	parseMu.Unlock()

	for _, err := range []error{fetchErr, pErr, writeErr} {
		if err != nil && !errors.Is(err, context.Canceled) {
			return stats, err
		}
	}
	for _, err := range []error{fetchErr, pErr, writeErr} {
		if err != nil {
			return stats, err
		}
	}
	return stats, ctx.Err()
}

// filterStored splits a UID set into the ones that still need fetching and a
// count of the ones already present.
func (s *Syncer) filterStored(ctx context.Context, mailboxID int64, uidValidity uint32, uids []imap.UID) ([]imap.UID, int, error) {
	ids := make([]int64, len(uids))
	for i, u := range uids {
		ids[i] = int64(u)
	}
	existing, err := s.store.ExistingUIDs(ctx, mailboxID, int64(uidValidity), ids)
	if err != nil {
		return nil, 0, fmt.Errorf("checking stored uids: %w", err)
	}
	if len(existing) == 0 {
		return uids, 0, nil
	}

	wanted := make([]imap.UID, 0, len(uids)-len(existing))
	for _, u := range uids {
		if !existing[int64(u)] {
			wanted = append(wanted, u)
		}
	}
	return wanted, len(existing), nil
}

// fetchStage streams the UIDs off the connection, puts every body in the blob
// store, and hands the result to the parse pool.
//
// # What is serial here and what is not
//
// Reading the literal MUST happen in this loop: the body reader dies when the
// iterator advances (imap.Message doc), so deferring the read would return
// whatever bytes the connection had moved on to.
//
// Storing the blob must NOT. blob.Put fsyncs the file and its shard directory,
// which measured 14.25 ms per message — 78% of the pipeline's cost and, done
// here, serialized behind the single connection reader. The Put is therefore
// done by the parse workers, which are already a pool: the fsyncs then overlap
// each other instead of queueing, and the connection reader goes back to doing
// only what genuinely cannot be parallelized.
//
// The L2 §2.4 ordering — raw blob durable BEFORE the parse is trusted — is
// preserved exactly: the worker Puts before it parses, and nothing is inserted
// until both are done.
func (s *Syncer) fetchStage(ctx context.Context, c imap.Client, uids []imap.UID, out chan<- rawMessage) error {
	spec := imap.FetchSpec{
		Body:         true,
		Flags:        true,
		InternalDate: true,
		Size:         true,
	}

	it, err := c.FetchMessages(ctx, uids, spec)
	if err != nil {
		return fmt.Errorf("fetching %d messages: %w", len(uids), err)
	}
	defer func() { _ = it.Close() }()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		msg, err := it.Next()
		if err != nil {
			return fmt.Errorf("reading fetched message: %w", err)
		}
		if msg == nil {
			break
		}
		if msg.Body == nil {
			// A UID that vanished between the listing and the fetch: the server
			// answers with no body rather than an error. Skipping it is
			// correct — the message is gone, and E6's expunge handling owns
			// the tombstone.
			continue
		}

		// Read the literal now, while it is still valid. The bytes are handed
		// to a parse worker, which stores and parses them.
		buf, err := readBody(msg.Body)
		if err != nil {
			return fmt.Errorf("reading body of uid %d: %w", msg.UID, err)
		}

		raw := rawMessage{
			uid:          msg.UID,
			modSeq:       msg.ModSeq,
			flags:        storeFlags(msg.Flags),
			keywords:     msg.Keywords,
			internalDate: msg.InternalDate,
			raw:          buf,
		}

		select {
		case out <- raw:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return it.Close()
}

// parseStage stores each message's raw bytes and then runs the MIME cascade.
//
// The store-then-parse order is the L2 §2.4 rule: the raw blob is the system of
// record and must be durable before any derivation of it is trusted, so that a
// parser bump re-derives from bytes already held rather than re-downloading.
//
// The parse itself never fails — parser.Parse cannot return an error by
// contract, and a message it cannot read becomes a row with
// parse_status='failed' (R4) rather than an interruption. The blob write can
// fail, and that one IS fatal to the run: bytes that did not reach disk must
// never be recorded as stored.
func (s *Syncer) parseStage(ctx context.Context, in <-chan rawMessage, out chan<- parsedMessage) error {
	for {
		select {
		case raw, ok := <-in:
			if !ok {
				return nil
			}

			hash, size, err := s.blobs.Put(ctx, bytes.NewReader(raw.raw))
			if err != nil {
				return fmt.Errorf("storing blob for uid %d: %w", raw.uid, err)
			}
			raw.hash, raw.size = hash, size

			pm := parsedMessage{
				raw:    raw,
				parsed: parser.Parse(bytes.NewReader(raw.raw), s.opts.Limits),
			}
			// The raw bytes are released here: they are durable in the blob
			// store and the store row only needs the hash. Holding them until
			// the write stage would triple the pipeline's memory for no gain.
			pm.raw.raw = nil

			select {
			case out <- pm:
			case <-ctx.Done():
				return ctx.Err()
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// writeStage accumulates parsed messages into batches and commits them.
//
// The checkpoint is NOT written here — the caller writes it after this returns,
// so a checkpoint can never claim more than what a committed transaction holds.
func (s *Syncer) writeStage(
	ctx context.Context,
	account store.Account,
	mb syncMailbox,
	uidValidity uint32,
	in <-chan parsedMessage,
	phase Phase,
) (phaseStats, error) {
	var stats phaseStats
	batch := make([]parsedMessage, 0, s.opts.BatchSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, failed, err := s.commitBatch(ctx, account, mb, uidValidity, batch)
		if err != nil {
			return err
		}
		stats.stored += n
		stats.failed += failed
		batch = batch[:0]

		if s.opts.OnProgress != nil {
			s.opts.OnProgress(Progress{
				AccountID: account.ID,
				Phase:     phase,
				Mailbox:   mb.info.Name,
				Stored:    stats.stored,
				Failed:    stats.failed,
				Skipped:   stats.skipped,
			})
		}
		return nil
	}

	for {
		select {
		case pm, ok := <-in:
			if !ok {
				return stats, flush()
			}
			batch = append(batch, pm)
			if len(batch) >= s.opts.BatchSize {
				if err := flush(); err != nil {
					return stats, err
				}
			}
		case <-ctx.Done():
			return stats, ctx.Err()
		}
	}
}

// commitBatch writes one batch of messages and their blob references in a
// single transaction, and returns how many were stored and how many of those
// failed to parse.
//
// # Why the blob reference is added in the same transaction
//
// blob.Put records the bytes with refcount 0, protected only by the GC grace
// period. The reference that keeps them alive must commit atomically with the
// message row that needs them (blob.AddRef's doc), or a crash between the two
// leaves either a message pointing at collectable bytes or bytes nobody will
// ever collect. InsertMessages runs its own transaction, so the references are
// added in a second one immediately after — the window between them is bounded
// by a single round trip and is far inside the GC grace period, which is the
// property that makes the split safe.
// # Why the batch is sorted by blob hash
//
// messages.raw_sha256 is a foreign key to blobs(sha256), so every INSERT takes
// a share lock on the referenced blob row. Blobs are content-addressed and
// therefore shared between accounts holding the same message, which means two
// concurrent batches routinely reference an overlapping set of rows — and in
// arrival order they take those locks in different sequences and deadlock
// (SQLSTATE 40P01). The bulk-migration test found this within seconds of
// running several accounts at once.
//
// Sorting by hash gives every transaction in this package one global lock
// order, at both the INSERT and the AddRef step, which makes the cycle
// impossible instead of merely unlikely.
func (s *Syncer) commitBatch(
	ctx context.Context,
	account store.Account,
	mb syncMailbox,
	uidValidity uint32,
	batch []parsedMessage,
) (stored, failed int, err error) {
	// Sorted in place: the caller's slice is a scratch buffer it clears after
	// this returns, and the order within a batch carries no meaning — every
	// message names its own UID.
	sort.Slice(batch, func(i, j int) bool {
		return bytes.Compare(batch[i].raw.hash[:], batch[j].raw.hash[:]) < 0
	})

	rows := make([]store.NewMessage, 0, len(batch))
	for i := range batch {
		rows = append(rows, s.newMessage(account.ID, mb.row.ID, uidValidity, &batch[i]))
		if batch[i].parsed.Status == parser.StatusFailed {
			failed++
		}
	}

	ids, err := s.insertWithRetry(ctx, rows, mb)
	if err != nil {
		// The batch failed as a unit. If the cause is a per-row DATA error, one
		// message is poisoning the other ninety-nine and retrying the same batch
		// forever cannot help — degrade to inserting them one at a time so the
		// innocent ones land and only the offender is quarantined.
		if !isDataError(err) {
			return 0, 0, err
		}
		degraded, derr := s.insertDegraded(ctx, account, mb, rows, batch)
		if derr != nil {
			return 0, 0, derr
		}
		ids = degraded
		// `failed` is NOT incremented here. It counts messages whose PARSE
		// failed, and it was already computed above from parsed.Status. A
		// quarantined message may or may not be among them, so adding the
		// quarantine count would double-count the overlap. The quarantined rows
		// carry parse_status='failed' in the database, which is what the
		// re-parse sweep and the E8 failure-rate alert actually read.
	}

	if err := s.addBlobRefs(ctx, account.ID, ids, batch); err != nil {
		return 0, 0, err
	}
	if err := s.assignThreads(ctx, account.ID, ids, rows); err != nil {
		return 0, 0, err
	}
	return countInserted(ids), failed, nil
}

// assignThreads groups the batch's messages into conversations (migration 0004).
//
// # Why a failure here does not fail the batch
//
// The messages are already committed and each is already its own thread — the
// JWZ base case, written by InsertMessages itself. Threading turns those
// singletons into conversations; it does not make the mail readable, searchable
// or safe. Failing the batch on a threading error would therefore roll back
// nothing (the insert is a separate committed transaction) while making the
// pipeline retry a fetch it has already completed, which is the one outcome
// worse than a temporarily split thread.
//
// So the error is logged and swallowed, and the state it leaves behind —
// messages threaded as singletons — is exactly what store.ReindexThreads exists
// to converge, idempotently and online. This mirrors the reasoning in
// insertDegraded: a per-message defect must cost that message, never the run.
//
// The exception is context cancellation, which is propagated: a shutdown must
// stop the pipeline rather than be logged as a threading problem.
func (s *Syncer) assignThreads(ctx context.Context, accountID int64, ids []int64, rows []store.NewMessage) error {
	candidates := make([]store.ThreadCandidate, len(rows))
	for i := range rows {
		candidates[i] = threadCandidate(&rows[i].Message)
	}

	assignments, err := s.store.AssignThreads(ctx, accountID, ids, candidates)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.opts.Logger.Warn("threading failed for a batch; messages remain single-message threads",
			"account_id", accountID, "batch_size", len(rows), "error", err)
		return nil
	}

	// A merge is worth a log line: it is the rare case (an ancestor arriving
	// after its descendants), it changes existing messages' threadId — which
	// ADR-001 §2 arbitrated as Thread destroyed+created on the wire — and it is
	// the thing to look at first if a client reports a thread that split or
	// jumped.
	for _, a := range assignments {
		if len(a.MergedFrom) == 0 {
			continue
		}
		s.opts.Logger.Info("threads merged by a late ancestor",
			"account_id", accountID,
			"message_id", a.MessageID,
			"thread_id", a.ThreadID,
			"merged_from", a.MergedFrom,
			"moved_messages", a.MovedMessages)
	}
	return nil
}

// insertDegraded re-inserts a failed batch one message at a time, quarantining
// the ones that still fail.
//
// # Why this exists (production incident, 2026-08-12)
//
// A real message carried 2 MB of extracted body text, which overflowed
// PostgreSQL's 1 MiB tsvector limit (SQLSTATE 54000). The insert is a batch of
// 100 in ONE transaction, so the single bad row aborted the other 99. The
// supervisor treats every error as transient and retried the identical doomed
// batch every 5 minutes, indefinitely: one message blocked an entire folder's
// backfill, and the account never finished syncing.
//
// Migration 0003 removed that particular cause by capping the tsvector's input.
// This function removes the FAILURE CLASS, which is the more valuable half:
// whatever data-dependent insert error the wild produces next — a constraint
// this schema grows later, an encoding case the parser does not yet normalize —
// costs one message instead of one mailbox.
//
// # What "quarantined" means
//
// A message that still fails alone is re-inserted with parse_status='failed'
// and its derived text fields emptied, which is R4's own remedy (L2 §2.4)
// applied to a store error rather than a parse error. The raw blob is already
// durable and untouched, so the message is not lost: it can be re-derived by a
// later parser bump or a schema fix, and until then it occupies its UID so the
// sync does not keep re-fetching it. If even the stripped row fails, the
// message is skipped with a WARN naming the mailbox and UID — the sync
// continues, which is the whole point.
func (s *Syncer) insertDegraded(
	ctx context.Context,
	account store.Account,
	mb syncMailbox,
	rows []store.NewMessage,
	batch []parsedMessage,
) ([]int64, error) {
	s.opts.Logger.Warn("batch insert failed with a data error; degrading to per-message inserts",
		"account_id", account.ID, "mailbox", mb.info.Name, "batch_size", len(rows))

	ids := make([]int64, len(rows))

	for i := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		got, err := s.store.InsertMessages(ctx, rows[i:i+1])
		if err == nil {
			ids[i] = got[0]
			continue
		}
		// A non-data error here is a real failure — a lost connection, a
		// deadlock that outlived its retries — and must propagate rather than
		// be recorded as a bad message.
		if !isDataError(err) {
			return nil, fmt.Errorf("inserting message uid %d in %q: %w",
				batch[i].raw.uid, mb.info.Name, err)
		}

		s.opts.Logger.Warn("message rejected by the store; storing it as failed",
			"account_id", account.ID,
			"mailbox", mb.info.Name,
			"uid", uint32(batch[i].raw.uid),
			"raw_size", batch[i].raw.size,
			"body_text_bytes", len(rows[i].Message.BodyText),
			"error", err)

		stripped := quarantineRow(rows[i])
		got, err = s.store.InsertMessages(ctx, []store.NewMessage{stripped})
		if err != nil {
			// Nothing more this package can do for this message. The blob is
			// durable, so the bytes survive for a later re-derivation; the run
			// carries on, which is the property that matters.
			s.opts.Logger.Warn("message could not be stored even as failed; skipping it",
				"account_id", account.ID,
				"mailbox", mb.info.Name,
				"uid", uint32(batch[i].raw.uid),
				"error", err)
			continue
		}
		ids[i] = got[0]
	}

	return ids, nil
}

// quarantineRow strips a message down to what is certain to be storable.
//
// Everything the parser derived is dropped — it is exactly what the store
// refused — while the identity the sync engine needs (account, blob hash, UID,
// flags, date) is kept. parse_status='failed' is what makes the row visible to
// the re-parse sweep (migration 0002's `messages_reparse` index) and to the
// failure-rate alert E8 watches, so a quarantined message is reported rather
// than silently absorbed.
func quarantineRow(row store.NewMessage) store.NewMessage {
	m := &row.Message
	m.Subject = ""
	m.BodyText = ""
	m.Preview = ""
	m.FromAddr = ""
	m.ToAddrs = ""
	m.CcAddrs = ""
	m.Addresses = nil
	m.MIMEStructure = nil
	m.ReferencesIDs = nil
	m.InReplyTo = ""
	m.ParseStatus = store.ParseFailed
	// The defect vocabulary is the parser's; this is a STORE rejection, so it is
	// recorded under its own name rather than borrowed from a parse outcome.
	m.Defects = []byte(`[{"kind":"store-rejected"}]`)
	return row
}

// isDataError reports whether err is a PostgreSQL error caused by the CONTENT
// of a row rather than by the state of the system.
//
// The distinction is the whole point of the degraded path: a data error is
// PERMANENT for that row — retrying it, alone or in a batch, produces the same
// error forever — while a connection loss, a deadlock or a shutdown is
// transient and must keep the existing retry semantics.
//
// The classes accepted here are the ones whose SQLSTATE says "this value is
// wrong", by the class prefixes PostgreSQL defines (Appendix A):
//
//	22xxx  data exception          — 54000 lives in 54 but see below
//	23xxx  integrity constraint violation
//	54xxx  program limit exceeded  — 54000 is the tsvector overflow itself
//
// 23505 (unique_violation) is deliberately EXCLUDED: InsertMessages resolves a
// duplicate UID through its ON CONFLICT clause, so a 23505 arriving here means
// something this package does not understand, and swallowing it as a bad
// message would hide it.
func isDataError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code == "23505" {
		return false
	}
	switch {
	case strings.HasPrefix(pgErr.Code, "22"), // data exception
		strings.HasPrefix(pgErr.Code, "23"), // integrity constraint violation
		strings.HasPrefix(pgErr.Code, "54"): // program limit exceeded
		return true
	default:
		return false
	}
}

// countInserted counts the rows InsertMessages actually created.
//
// A zero id means the UID was already stored and the row was skipped by the ON
// CONFLICT clause (InsertMessages' doc). Counting it as stored would report
// progress that did not happen — and on a resumed run, where the pre-filter and
// the conflict clause overlap, would inflate every figure the acceptance
// criteria are measured in.
func countInserted(ids []int64) int {
	n := 0
	for _, id := range ids {
		if id != 0 {
			n++
		}
	}
	return n
}

// insertWithRetry inserts a batch, retrying a deadlock.
//
// The sort above removes the cycles this package can create among its own
// transactions. The retry covers the ones it cannot see: the blob GC and any
// other writer taking the same rows by a different route. It is safe because a
// deadlock abort rolls the whole transaction back — the retry repeats work that
// was never committed.
func (s *Syncer) insertWithRetry(ctx context.Context, rows []store.NewMessage, mb syncMailbox) ([]int64, error) {
	var lastErr error
	for attempt := range blobRefRetries {
		ids, err := s.store.InsertMessages(ctx, rows)
		if err == nil {
			return ids, nil
		}
		lastErr = err
		if !isDeadlock(err) || ctx.Err() != nil {
			break
		}
		s.opts.Logger.Debug("message insert deadlocked; retrying",
			"attempt", attempt+1, "mailbox", mb.info.Name)
	}
	return nil, fmt.Errorf("inserting %d messages in %q: %w", len(rows), mb.info.Name, lastErr)
}

// addBlobRefs links every stored message to the blob holding its raw bytes, in
// one transaction.
//
// # Why the references are sorted by hash
//
// blob.AddRef takes a row lock on the blob (SELECT … FOR UPDATE) before
// touching blob_refs, which is what makes its refcount correct under
// concurrency. Blobs are content-addressed and therefore SHARED: two accounts
// holding the same message — a mailing list, a company-wide announcement, the
// same attachment — reference one row.
//
// So two batches committing at the same time can want the same set of blob
// rows. If each locks them in its own arrival order, batch A holds blob X and
// waits for Y while batch B holds Y and waits for X, and PostgreSQL breaks the
// cycle by aborting one with SQLSTATE 40P01. A bulk migration of an
// installation whose accounts share mail hits this constantly — it was found by
// exactly that test, not by reasoning.
//
// Sorting by hash gives every transaction one global lock order, which makes
// the cycle impossible rather than merely unlikely. The sort is over a batch of
// ~100 fixed-size keys, so it costs nothing measurable.
func (s *Syncer) addBlobRefs(ctx context.Context, accountID int64, ids []int64, batch []parsedMessage) error {
	refs := make([]blob.Ref, 0, len(ids))
	for i, id := range ids {
		// A zero id is a message whose UID was already stored, so the insert
		// was skipped (store.InsertMessages' doc). There is no row to reference
		// and the existing one already holds its own reference.
		if id == 0 {
			continue
		}
		refs = append(refs, blob.Ref{
			Hash:      batch[i].raw.hash,
			AccountID: accountID,
			Kind:      blob.OwnerMessage,
			OwnerID:   id,
		})
	}
	if len(refs) == 0 {
		return nil
	}

	// blob.AddRefs sorts by hash internally and takes each blob's lock once for
	// the whole batch, which is what gives every transaction in this package one
	// global lock order.
	//
	// The retry stays as a backstop for the cycles this package cannot see: the
	// blob GC, or a future reparse job, taking the same locks by a different
	// route. It is safe by construction — a deadlock abort rolls the whole
	// transaction back, so a retry repeats work that was never committed, and
	// AddRefs is idempotent per (blob, owner) anyway.
	var err error
	for attempt := range blobRefRetries {
		err = s.store.InTx(ctx, func(tx pgx.Tx) error {
			return blob.AddRefs(ctx, tx, refs)
		})
		if err == nil {
			return nil
		}
		if !isDeadlock(err) || ctx.Err() != nil {
			break
		}
		s.opts.Logger.Debug("blob reference transaction deadlocked; retrying",
			"attempt", attempt+1, "account_id", accountID)
	}
	return fmt.Errorf("adding blob references: %w", err)
}

// blobRefRetries bounds how often a deadlocked reference transaction is
// retried. Three is enough for a transient lock cycle and small enough that a
// systematic one still surfaces as an error rather than as a hang.
const blobRefRetries = 3

// isDeadlock reports whether err is PostgreSQL's deadlock_detected (40P01).
func isDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}

// readBody reads a message body with a bounded initial allocation.
//
// io.ReadAll grows its buffer by reallocation; for mail, the size is known
// often enough that seeding the buffer is worth it. When it is not known, the
// default growth is fine — mail is small by the standards of a heap.
func readBody(r io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// storeFlags converts the IMAP package's normalized flag names to the store's
// bitmask.
//
// Unknown system flags are dropped rather than guessed at: the bitmask is a
// closed set by design (store.Flags), and user keywords — which is what an
// unrecognized name actually is — travel in the keywords column instead.
func storeFlags(flags []string) store.Flags {
	var out store.Flags
	for _, f := range flags {
		switch strings.ToLower(strings.TrimPrefix(f, `\`)) {
		case "seen":
			out |= store.FlagSeen
		case "answered":
			out |= store.FlagAnswered
		case "flagged":
			out |= store.FlagFlagged
		case "deleted":
			out |= store.FlagDeleted
		case "draft":
			out |= store.FlagDraft
		case "recent":
			out |= store.FlagRecent
		}
	}
	return out
}
