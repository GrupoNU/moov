// Package sync orchestrates the mirroring of Dovecot into Moov's store.
//
// It is the only package that composes imap, parser, store, blob and index. Its
// job is scheduling and correctness under failure; the protocol details belong
// to internal/imap and the persistence details to internal/store.
//
// # Contract
//
// docs/specs/L2-sync-engine.md §2.5. The invariant above everything else:
// Dovecot is the source of truth and Moov is a reconstructible cache. When the
// two disagree, Dovecot wins and the local state is rebuilt.
//
// # Initial sync (E5) — usable fast, complete later — IMPLEMENTED
//
//  1. LIST + STATUS: the mailbox tree with its SPECIAL-USE roles (discover.go).
//  2. INBOX, last 30 days: FETCH bodies, parse, insert in batches — the PWA is
//     usable when this phase ends (phases.go, runRecent).
//  3. Historical backfill by checkpoints (per mailbox, descending UID ranges),
//     interruptible and resumable — a kill -9 at any point resumes with no loss
//     and no duplicates (phases.go, checkpoint.go).
//  4. Bulk installation migration (the 89-account case): migration.go, running
//     accounts concurrently against ONE core-sized parse pool. S3 H6 measured
//     2,063 rows/s CPU-bound in to_tsvector, so parse workers are budgeted per
//     core, not per account.
//
// # The pipeline
//
// Fetch, parse and write are three stages with bounded queues between them,
// because their costs sit on different resources — network, CPU and the
// database — and running them in one loop makes a backfill as slow as their
// sum. The queue bound is what keeps a 500k-message mailbox inside a fixed
// memory budget instead of reading the mailbox into a slice.
//
// Two placements in that pipeline were measured rather than assumed:
//
//   - blob.Put runs in the PARSE POOL, not the fetch loop. Its two fsyncs cost
//     14.25 ms per message, and serialized behind the single connection reader
//     they were 78% of the pipeline's time — 96 msg/s. Moved into the pool,
//     where they overlap, the same corpus runs at 231 msg/s.
//   - blob references are added in HASH ORDER. Blobs are shared between
//     accounts by content address, so concurrent batches want the same row
//     locks; arrival order deadlocked (SQLSTATE 40P01) as soon as a bulk
//     migration ran several accounts at once.
//
// # Idempotency, which every restart depends on
//
// Blobs are content-addressed, so a re-Put is free. Message rows are keyed by
// (mailbox, uidvalidity, uid) and the UIDs already stored are filtered out
// before a fetch is issued. And a checkpoint is written only AFTER the batch it
// describes is committed, so a crash repeats work and never skips it.
//
// # Incremental (E6)
//
// SELECT (QRESYNC (uidvalidity modseq)) on reconnect yields VANISHED (EARLIER)
// plus the changes to fetch (S2 H1); on a live connection, UID FETCH
// (CHANGEDSINCE … VANISHED). A changed UIDVALIDITY invalidates the mailbox and
// forces a resync — cheap, because content is recovered by sha256 without
// re-downloading blobs already held.
//
// # Watcher (E6)
//
// One connection per active account: NOTIFY SET STATUS (PERSONAL …) with the
// patched encoder — only the STATUS variant makes flag changes visible in
// unselected mailboxes (S2 H4/H5) — plus a maintenance IDLE loop (S2 H9).
// Events are notification-only (S2 H7): they enqueue a batched FETCH per
// mailbox. NOTIFICATIONOVERFLOW escalates to a full account resync. The watcher
// of an account with no active session stops after N minutes and falls back to
// scheduled reconciliation.
//
// # Defensive reconciler (E6)
//
// A periodic pass (default 6 h) comparing STATUS of every mailbox against local
// state, to catch any lost event. This is not paranoia: Dovecot has a history of
// NOTIFY regressions, and an injected divergence must be found and repaired by
// this pass as an acceptance criterion.
//
// # Writes towards IMAP (phase 1b)
//
// Every conditional write is verified by read-back while [MODIFIED] is not
// exposed (S2 H6). Flag updates are batched (S3 H9: 23x cheaper). The outbox is
// transactional per ADR §4.
//
// # Resilience
//
// A bounded connection pool per account (watcher + N workers, default 2),
// backoff with jitter, and a per-account circuit breaker to stay clear of
// fail2ban (ADR §4). Reconnection distinguishes ECONNRESET, which the post
// beta.8 error wrapping makes possible (S2).
//
// Implementation lands in epics E5 and E6.
package sync
