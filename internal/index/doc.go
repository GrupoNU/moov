// Package index defines the Indexer interface that search is written against,
// and the reindexing machinery behind it.
//
// # Contract
//
// docs/specs/L2-sync-engine.md §2.1 and §2.3. The interface exists so the
// search backend is a decision that can be revisited without touching callers:
//
//   - Today: PostgreSQL tsvector + GIN, validated in spike S3 against a
//     synthetic 5M-message corpus. It meets the Gmail-class bar of the project
//     (search under 100 ms perceived) with the configuration mandated in
//     internal/store — btree_gin composite index, plan_cache_mode =
//     force_custom_plan, STATISTICS 4000 on tsv.
//   - Phase 2, if and only if measurement demands it: Meilisearch behind the
//     same interface. S3 is the evidence that this is a phase-2 question and
//     not an MVP dependency.
//
// # Responsibilities
//
//   - Turn a ParsedMessage into indexable text, weighted by field: subject and
//     addresses above body (L2 §4.2, TextForFTS).
//   - Rebuild the index for an account or a mailbox, incrementally and
//     resumably, so a parser version bump or a stemming configuration change
//     does not require a resync from Dovecot. The blobs are local; reindexing
//     is a local operation.
//   - Keep the query repertoire closed. Search always filters by account_id and
//     always has a LIMIT; counts are capped; ranking runs on the separate pool
//     with a statement_timeout (S3 H5). These are product decisions encoded
//     here, not caller options.
//
// # Open question for E3
//
// Stemming and recall quality (S3 H10): whether to run a dual es/en text search
// configuration. To be evaluated during E3 and decided before the MVP; the
// decision belongs in an ADR because it changes stored tsvectors.
//
// Implementation lands with epic E3 and is exercised throughout E5/E6.
package index
