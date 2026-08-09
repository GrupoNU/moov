// Package store is Moov's PostgreSQL persistence layer: schema, migrations and
// the complete repertoire of queries the rest of the engine may run.
//
// # Contract
//
// docs/specs/L2-sync-engine.md §2.3 and §4.3. The store exposes methods, never
// SQL: the JMAP layer reads through this package and may not emit queries of
// its own. That is what keeps the performance envelope validated in spike S3
// from being silently escaped by a caller.
//
// # Schema (L2 §2.3, arbitration A5)
//
//	messages       Immutable after parse: identity, headers, dates, MIME
//	               structure, tsv, blob references, parse_status. Indexed
//	               gin(account_id, tsv) via btree_gin (S3 H2).
//	message_state  The narrow, hot row: (message_id PK, account_id, mailbox_id,
//	               uid, flags, keywords, modseq_seen, updated_at). Every flag
//	               update and every move writes HERE, so the ~2.2 KB tsv is
//	               never rewritten (S3 H9 measured that a flag change otherwise
//	               rewrites the whole row into two GIN indexes).
//	mailboxes      uidvalidity, local highestmodseq, SPECIAL-USE roles, backfill
//	               phase state.
//	sync_log       Per account: checkpoints, errors, breaker state. Also the
//	               source of Email/changes for JMAP.
//	intents        Queued writes (flag/move/send) the sync engine executes
//	               against IMAP; the JMAP layer only enqueues.
//
// Identity (S2 H8, no OBJECTID available): message_id is an internal surrogate;
// content identity is the sha256 of the raw blob, which dedupes and survives
// moves; IMAP identity is (mailbox_id, uidvalidity, uid) in message_state. A
// move is an UPDATE of message_state — the content is never touched.
//
// # PostgreSQL configuration (S3 H2-H4, non-negotiable)
//
//   - Extensions btree_gin and unaccent. Applied by migration 0001.
//   - plan_cache_mode = force_custom_plan on the database, so the generic plan
//     never wins over a selective FTS plan (S3 H3). Applied by migration 0001;
//     re-verified by a test.
//   - ALTER TABLE messages ALTER COLUMN tsv SET STATISTICS 4000 — belongs to
//     the migration that creates the column, i.e. E3, not E1.
//   - fastupdate stays on (the default) for the GIN indexes.
//   - Search queries run on a separate pool carrying statement_timeout, and
//     rank/count work is bounded there (S3 H5).
//
// Migrations are pressly/goose with the SQL embedded in the binary
// (internal/store/migrations), so a deploy cannot drift from its schema.
//
// # Open risks this package must watch
//
//   - GIN bloat under sustained churn: soak test before production (A5 removes
//     flags from the tsv write path, which is the main mitigation).
//   - PgBouncer interacts badly with force_custom_plan — verify before any
//     pooler is introduced (S3 H2/H3).
//   - Stemming quality: a dual es/en configuration is to be evaluated during E3
//     (S3 H10).
//
// Implementation lands in epic E3.
package store
