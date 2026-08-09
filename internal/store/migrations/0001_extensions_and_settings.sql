-- Moov Mail — migration 0001: extensions and database-level settings.
--
-- Everything here comes from spike S3 (docs/spikes/S3-benchmark-fts.md) and is
-- recorded as non-negotiable in docs/specs/L2-sync-engine.md §2.3. This
-- migration deliberately creates no tables: it establishes the ground the
-- schema needs, so that E3's schema migration can assume it.
--
-- +goose Up
-- +goose StatementBegin

-- btree_gin lets a single GIN index cover (account_id, tsv). S3 H2: without it
-- the planner either scans a global tsv index and filters by account, or scans
-- by account and re-ranks — both an order of magnitude worse than the composite
-- index at 5M messages. This is the single most important index decision in the
-- store.
CREATE EXTENSION IF NOT EXISTS btree_gin;

-- unaccent normalizes diacritics for search. Mail in this installed base is
-- overwhelmingly Spanish and Portuguese; "accion" must find "acción" and vice
-- versa, which no stemmer does on its own.
CREATE EXTENSION IF NOT EXISTS unaccent;

-- +goose StatementEnd

-- +goose StatementBegin

-- plan_cache_mode = force_custom_plan, S3 H3.
--
-- WHY: full-text search queries are extremely selectivity-dependent. After five
-- executions PostgreSQL will adopt a generic plan built without knowledge of
-- the actual search term, and S3 measured that generic plan choosing a sequ-
-- ential scan on selective queries. Forcing a custom plan costs a few hundred
-- microseconds of planning and saves seconds of execution.
--
-- WHERE IT IS SET, and why here rather than per connection: this is an
-- ALTER DATABASE, so every connection to the Moov database inherits it,
-- including psql sessions, migrations, and any future service. Setting it per
-- connection in the search pool alone would leave the setting silently absent
-- wherever someone forgot to apply it — exactly the class of drift that makes a
-- benchmark stop reflecting production. The narrower per-pool alternative was
-- rejected for that reason.
--
-- CAVEAT recorded as risk 2 in L2 §5: a transaction-pooling PgBouncer in front
-- of this database does NOT reliably carry database-level settings to the
-- server connection. Before any pooler is introduced, verify with
--     SHOW plan_cache_mode;
-- through the pooler, and if it does not report force_custom_plan, set it on
-- the pooler's server connection instead. The test in internal/store verifies
-- the setting through whatever connection string it is given, which makes that
-- regression visible.
--
-- current_database() is used rather than a literal so the migration works
-- against dev, CI and production databases whatever they are named.
DO $$
BEGIN
    EXECUTE format(
        'ALTER DATABASE %I SET plan_cache_mode = %L',
        current_database(), 'force_custom_plan'
    );
END
$$;

-- +goose StatementEnd

-- NOTE for E3 (do not implement here):
--   ALTER TABLE messages ALTER COLUMN tsv SET STATISTICS 4000;
-- The third mandatory S3 setting belongs to the migration that CREATES the tsv
-- column, because a STATISTICS target on a non-existent column is not a thing
-- that can be expressed. E3 owns it, and E3's test extends
-- migrations_test.go's assertions to cover it.

-- +goose Down
-- +goose StatementBegin

DO $$
BEGIN
    EXECUTE format('ALTER DATABASE %I RESET plan_cache_mode', current_database());
END
$$;

-- +goose StatementEnd

-- +goose StatementBegin

-- The extensions are intentionally NOT dropped on the way down. Dropping
-- btree_gin cascades into every index that depends on it, which turns a routine
-- rollback into data loss. Removing them is a deliberate manual operation.

SELECT 1;

-- +goose StatementEnd
