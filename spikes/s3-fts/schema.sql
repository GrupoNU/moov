-- Moov Mail — Spike S3: PostgreSQL tsvector+GIN FTS benchmark
-- Schema mirrors the sync-store shape from ADR-001.
--
-- Load order matters: this file creates the table WITHOUT the GIN index.
-- The GIN index is built after the bulk COPY (see indexes.sql), which mirrors
-- the sync engine's initial-sync strategy (bulk load, then index).

CREATE EXTENSION IF NOT EXISTS unaccent;
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;

-- unaccent() is STABLE (it depends on the search_path resolution of the
-- dictionary), so it cannot be used directly in a generated column.
-- Pinning the dictionary by name and wrapping it makes it IMMUTABLE.
CREATE OR REPLACE FUNCTION immutable_unaccent(text)
RETURNS text
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
STRICT
AS $$ SELECT public.unaccent('public.unaccent'::regdictionary, $1) $$;

DROP TABLE IF EXISTS messages;

CREATE TABLE messages (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  account_id   int      NOT NULL,
  mailbox_id   int      NOT NULL,
  uid          bigint   NOT NULL,
  date         timestamptz NOT NULL,
  flags        int      NOT NULL DEFAULT 0,   -- bitmask; bit 0 = \Seen
  from_addr    text     NOT NULL,
  to_addrs     text     NOT NULL,
  subject      text     NOT NULL,
  body_text    text     NOT NULL,
  tsv tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', immutable_unaccent(coalesce(subject,''))), 'A') ||
    setweight(to_tsvector('simple', immutable_unaccent(coalesce(from_addr,'') || ' ' || coalesce(to_addrs,''))), 'B') ||
    setweight(to_tsvector('simple', immutable_unaccent(coalesce(body_text,''))), 'C')
  ) STORED
);

-- Note on the text search configuration:
--   'simple' + unaccent means NO stemming. For mixed Spanish/English LATAM
--   mailboxes this is the realistic choice: a single stemmer would mangle the
--   other language, and mail bodies routinely mix both plus product names,
--   invoice codes and URLs which stem badly. Trade-off: "factura"/"facturas"
--   are distinct lexemes, so recall depends on the client issuing prefix
--   queries (shape #5) for morphological variants.
