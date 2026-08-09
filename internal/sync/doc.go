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
// # Initial sync (E5) — usable fast, complete later
//
//  1. LIST + STATUS: the mailbox tree with its SPECIAL-USE roles.
//  2. INBOX, last 30 days: FETCH headers and bodies, parse, insert normally —
//     the index already exists and costs ~0.25 ms/msg (S3 H8). The PWA is
//     usable when this phase ends.
//  3. Historical backfill by checkpoints (per mailbox, descending UID ranges),
//     interruptible and resumable — a kill -9 at any point must resume, which
//     is an explicit acceptance criterion.
//  4. Bulk installation migration (the 89-account case): a separate path using
//     COPY with the GIN indexes built at the end. S3 H6 measured 2,063 rows/s,
//     CPU-bound in to_tsvector, so parse workers are budgeted per core, not per
//     account.
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
