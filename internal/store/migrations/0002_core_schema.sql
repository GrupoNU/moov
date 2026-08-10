-- Moov Mail — migration 0002: the core schema.
--
-- Source of truth: docs/specs/L2-sync-engine.md §2.3 (data model + arbitration
-- A5) and spike S3 (spikes/s3-fts/schema.sql + indexes.sql), whose measured
-- baseline this migration adopts.
--
-- THE ONE STRUCTURAL DEPARTURE FROM S3's SCHEMA (arbitration A5): S3 kept
-- `flags` on the `messages` row. It also measured (S3 §4.5) that changing that
-- int rewrites the entire row — including the ~2.2 KB generated `tsv` — and
-- re-inserts it into the GIN indexes, at ~0.58 ms per flag change in batches.
-- Since flag churn dominates writes on an established mailbox, this migration
-- splits the volatile columns into `message_state`, exactly as S3's own "open
-- risks" section recommended. `messages` is immutable after parse; every flag
-- update and every move touches only the narrow row.
--
-- +goose Up
-- +goose StatementBegin

-- ---------------------------------------------------------------------------
-- immutable_unaccent — verbatim from spikes/s3-fts/schema.sql.
--
-- unaccent() is STABLE, not IMMUTABLE, because the dictionary it uses is
-- resolved through search_path at call time. A GENERATED column may only call
-- IMMUTABLE functions. Pinning the dictionary by name ('public.unaccent'
-- ::regdictionary) removes the search_path dependency, which is what makes the
-- wrapper honestly immutable rather than merely labelled so.
--
-- The pinned schema is why migration 0001 creates the extension in public.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION immutable_unaccent(text)
RETURNS text
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
STRICT
AS $$ SELECT public.unaccent('public.unaccent'::regdictionary, $1) $$;

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- accounts
--
-- The IMAP credential columns are OPAQUE ENCRYPTED BYTES here. Encryption is
-- E7's job (AES-256-GCM, master key outside the database); this migration only
-- guarantees the store never has a column shaped to hold a plaintext password.
-- The type is bytea for exactly that reason: a ciphertext blob does not fit in
-- a text column by accident, and nothing in this package can be tempted to
-- read it as a string.
-- ---------------------------------------------------------------------------
CREATE TABLE accounts (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email               text        NOT NULL,

    -- IMAP endpoint. Per-account rather than global: one deployment may serve
    -- more than one Mailcow, and the ServerName used to verify the certificate
    -- is a per-account fact (S1 H2 — the internal cert carries the public
    -- hostname).
    imap_host           text        NOT NULL,
    imap_port           int         NOT NULL DEFAULT 143,
    imap_server_name    text        NOT NULL DEFAULT '',

    -- Opaque ciphertext (E7). NULL until provisioning completes.
    imap_username       text        NOT NULL DEFAULT '',
    imap_app_password   bytea,
    credential_state    text        NOT NULL DEFAULT 'pending'
        CHECK (credential_state IN ('pending', 'active', 'invalid', 'revoked')),

    -- Lifecycle of the account within the engine, as opposed to the state of
    -- its credentials.
    state               text        NOT NULL DEFAULT 'active'
        CHECK (state IN ('active', 'paused', 'disabled')),

    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT accounts_email_key UNIQUE (email)
);

COMMENT ON COLUMN accounts.imap_app_password IS
    'AES-256-GCM ciphertext of the Mailcow app password (E7). Never plaintext.';

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- mailboxes (L2 §2.3)
--
-- uidvalidity + highestmodseq are the QRESYNC resume point (S2 H1). Both are
-- NULL until the mailbox is first selected, which is what distinguishes "never
-- synced" from "synced, empty".
--
-- The backfill checkpoint (L2 §2.5 step 3) lives here rather than in sync_log
-- because it is per mailbox and must be read in the same query that decides
-- what to sync next. It is a UID watermark walked DOWNWARD: the initial sync
-- takes the recent window first, then backfills history in descending UID
-- ranges, so `backfill_uid_low` is "everything above this UID is present".
-- ---------------------------------------------------------------------------
CREATE TABLE mailboxes (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id          bigint      NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,

    -- The IMAP name as the server spells it, plus the hierarchy delimiter the
    -- server reported. Storing the delimiter avoids guessing '/' vs '.' when
    -- splitting a path for display.
    name                text        NOT NULL,
    delimiter           text        NOT NULL DEFAULT '/',

    -- SPECIAL-USE role, normalized without the leading backslash:
    -- inbox|archive|drafts|sent|junk|trash|all|flagged. NULL for a plain folder.
    role                text
        CHECK (role IS NULL OR role IN
              ('inbox', 'archive', 'drafts', 'sent', 'junk', 'trash', 'all', 'flagged')),

    subscribed          boolean     NOT NULL DEFAULT true,
    -- \Noselect and friends: a container that holds no messages.
    selectable          boolean     NOT NULL DEFAULT true,

    -- QRESYNC resume point.
    uidvalidity         bigint,
    uidnext             bigint,
    highestmodseq       bigint,

    -- Backfill state machine (L2 §2.5).
    backfill_state      text        NOT NULL DEFAULT 'pending'
        CHECK (backfill_state IN ('pending', 'recent_done', 'in_progress', 'complete')),
    -- Lowest UID synced so far; everything from here up is present locally.
    backfill_uid_low    bigint,
    backfill_updated_at timestamptz,

    last_synced_at      timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT mailboxes_account_name_key UNIQUE (account_id, name)
);

-- A role is unique per account where it is set: there is exactly one Sent.
-- Partial, because NULL roles are the common case and many of them coexist.
CREATE UNIQUE INDEX mailboxes_account_role_key
    ON mailboxes (account_id, role) WHERE role IS NOT NULL;

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- blobs — content-addressed raw message bytes (L2 §2.3, §2.4).
--
-- The row is the reference count; the bytes live on the filesystem under
-- internal/blob. Two facts make this table the authority rather than a cache:
--
--   * `refcount` is mutated only through the transactional helpers in
--     internal/blob, so a message insert and its AddRef commit together.
--   * `zero_ref_since` is set when refcount reaches 0 and cleared when it rises
--     again. GC collects only blobs whose zero_ref_since is older than a grace
--     period, which is what makes the mark-and-sweep safe against a writer that
--     has just written the bytes but not yet committed its reference.
--
-- sha256 is stored as bytea (32 bytes) rather than hex text: half the size, and
-- it cannot hold a mis-cased or truncated hex string.
-- ---------------------------------------------------------------------------
CREATE TABLE blobs (
    sha256          bytea       PRIMARY KEY CHECK (octet_length(sha256) = 32),
    size            bigint      NOT NULL CHECK (size >= 0),
    refcount        bigint      NOT NULL DEFAULT 0 CHECK (refcount >= 0),
    -- NULL while referenced; the instant the count last hit zero otherwise.
    zero_ref_since  timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- The GC's only scan: unreferenced blobs that have been unreferenced long
-- enough. Partial, so it holds only collection candidates and stays tiny.
CREATE INDEX blobs_gc_candidates
    ON blobs (zero_ref_since) WHERE refcount = 0;

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- messages — IMMUTABLE after parse (A5).
--
-- Identity (L2 §2.3, S2 H8 — Dovecot offers no OBJECTID here):
--   * `id`         internal surrogate, the JMAP Email id;
--   * `raw_sha256` content identity — dedupes and survives moves;
--   * IMAP identity `(mailbox_id, uidvalidity, uid)` lives in message_state.
--
-- Nothing volatile belongs in this table. If a column would change because a
-- user clicked something, it goes to message_state instead: every write here
-- costs a rewrite of the tsv into the GIN index (S3 §4.5).
-- ---------------------------------------------------------------------------
CREATE TABLE messages (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id      bigint      NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,

    -- Raw bytes, content-addressed. RESTRICT rather than CASCADE: dropping a
    -- blob that a message still points at is a bug, and the database should
    -- say so rather than silently leave a dangling reference.
    raw_sha256      bytea       NOT NULL REFERENCES blobs(sha256) ON DELETE RESTRICT,
    raw_size        bigint      NOT NULL CHECK (raw_size >= 0),

    -- ---- Canonical headers, decoded (parser contract, L2 §4.2) -------------
    -- RFC 5322 Message-ID with the angle brackets stripped. Not unique: the
    -- same message legitimately appears in several mailboxes, and forgeries
    -- and duplicates are routine in real mail.
    message_id      text,
    in_reply_to     text,
    -- Parsed References chain, oldest first. Threading (JWZ) reads this.
    references_ids  text[]      NOT NULL DEFAULT '{}',

    subject         text        NOT NULL DEFAULT '',
    -- Display form of the addresses, which is what the FTS weights index.
    -- The structured form lives in `addresses` for the JMAP layer.
    from_addr       text        NOT NULL DEFAULT '',
    to_addrs        text        NOT NULL DEFAULT '',
    cc_addrs        text        NOT NULL DEFAULT '',
    -- {"from":[{"name":…,"email":…}],"to":[…],"cc":[…],"bcc":[…],"replyTo":[…]}
    addresses       jsonb       NOT NULL DEFAULT '{}'::jsonb,

    -- The Date header, falling back to INTERNALDATE when it is absent or
    -- unparseable. NOT NULL because every sort and every index depends on it.
    date            timestamptz NOT NULL,
    -- Kept separately: JMAP exposes receivedAt, and a message with a forged or
    -- wildly skewed Date is common enough that the distinction matters.
    internal_date   timestamptz,

    -- ---- MIME structure summary (L2 §2.3, parser contract) ----------------
    -- The flattened part tree: content types, sizes, dispositions, filenames,
    -- charset/encoding flags, blob refs for the parts worth storing apart.
    -- JSONB rather than a parts table: it is read whole, written once, and
    -- never joined against. A table would buy nothing and cost a join on the
    -- hot Email/get path.
    mime_structure  jsonb       NOT NULL DEFAULT '{}'::jsonb,
    has_attachments boolean     NOT NULL DEFAULT false,
    -- Preview line for the message list, already truncated by the parser.
    preview         text        NOT NULL DEFAULT '',

    -- ---- Parse outcome (L2 §2.4, S4) --------------------------------------
    parse_status    text        NOT NULL DEFAULT 'ok'
        CHECK (parse_status IN ('ok', 'partial', 'failed')),
    -- Which arm of the cascade produced this: go-message | enmime | salvage.
    parser          text        NOT NULL DEFAULT '',
    -- Parser version, so a bump can re-derive exactly the affected rows
    -- (the blob is durable; parsing is a retryable derivation).
    parser_version  int         NOT NULL DEFAULT 0,
    -- Structured defects from the corpus vocabulary (S4).
    defects         jsonb       NOT NULL DEFAULT '[]'::jsonb,

    created_at      timestamptz NOT NULL DEFAULT now(),

    -- The extracted plain text the FTS indexes: the parser's TextForFTS
    -- (L2 §4.2). Declared before `tsv` because a GENERATED expression may only
    -- reference columns already defined.
    body_text       text        NOT NULL DEFAULT '',

    -- ---- Full-text search vector ------------------------------------------
    -- Weight scheme verbatim from spikes/s3-fts/schema.sql, with cc added to
    -- the B (address) band, which is where a reader would expect to find it.
    --
    -- 'simple' + unaccent means NO stemming, and that is deliberate: these are
    -- mixed Spanish/English mailboxes, and a single stemmer mangles the other
    -- language along with product names, invoice codes and URLs. The cost is
    -- that "factura" and "facturas" are distinct lexemes, so morphological
    -- recall depends on the client issuing a prefix query — which is what
    -- SearchPrefix exists for. S3 §"open risks" flags a dual es/en
    -- configuration as worth evaluating; that evaluation is recorded in this
    -- package's doc.go and is not settled here.
    tsv tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('simple', immutable_unaccent(coalesce(subject, ''))), 'A') ||
        setweight(to_tsvector('simple', immutable_unaccent(
            coalesce(from_addr, '') || ' ' || coalesce(to_addrs, '') || ' ' || coalesce(cc_addrs, '')
        )), 'B') ||
        setweight(to_tsvector('simple', immutable_unaccent(coalesce(body_text, ''))), 'C')
    ) STORED
);

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- The three mandatory S3 settings, part 3 of 3.
--
-- Parts 1 and 2 (btree_gin/unaccent extensions, plan_cache_mode) are in
-- migration 0001. This is the one 0001 explicitly deferred to E3 in its
-- closing note, because a STATISTICS target cannot be set on a column that
-- does not exist yet.
--
-- WHY 4000: at the default target of 100 the planner misestimated tsvector
-- selectivity by ~500x (4,951 estimated rows against 10 actual), concluded it
-- could satisfy LIMIT 50 by walking the date index, and filtered 999,990 rows
-- — 13,085 ms for a query that takes 1.6 ms on the composite GIN. At 4000 the
-- estimate lands close enough (105) that the planner chooses correctly with no
-- hint and no query rewrite. Cost: ANALYZE on a 5M-row table rises from 88 s
-- to 429 s. (S3 §5.3.)
--
-- migrate_test.go asserts this is in place; it is the marker migration 0001
-- left for E3.
-- ---------------------------------------------------------------------------
ALTER TABLE messages ALTER COLUMN tsv SET STATISTICS 4000;

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- Indexes on messages.
--
-- THE index (S3 §5.2): account_id INSIDE the GIN index, via btree_gin. Without
-- it a search builds a bitmap of the CORPUS-WIDE matches for the term and only
-- then intersects with the account, so cost scales with the size of the whole
-- installation rather than the user's mailbox — measured at up to 6,600x on a
-- rare term (10,636 ms -> 1.6 ms).
--
-- The plain gin(tsv) that S3 also built is deliberately NOT created here: S3's
-- verdict is that keeping both is wasteful (2.5 GB) and the composite one
-- serves every shape.
-- ---------------------------------------------------------------------------
CREATE INDEX messages_acct_tsv_gin ON messages USING gin (account_id, tsv);

-- Shape #1 and the unfiltered mailbox listing walk this and stop at LIMIT.
CREATE INDEX messages_acct_date ON messages (account_id, date DESC);

-- Content identity: dedupe lookups, and the resync path that avoids
-- re-downloading a blob already held after a UIDVALIDITY change.
CREATE INDEX messages_acct_sha ON messages (account_id, raw_sha256);

-- Threading (JWZ) resolves parents by Message-ID within an account.
CREATE INDEX messages_acct_msgid ON messages (account_id, message_id)
    WHERE message_id IS NOT NULL;

-- Re-parse sweeps after a parser bump, and the parse_status='failed' rate that
-- E8 alerts on (L2 §2.4, rule R4). Partial: 'ok' is the overwhelming majority
-- and is never the target of these queries.
CREATE INDEX messages_reparse ON messages (account_id, parser_version)
    WHERE parse_status <> 'ok';

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- message_state — the narrow hot row (A5).
--
-- One row per message. `message_id` is both the primary key and the foreign
-- key: the state of a message is a property of that message, so a 1:1 with a
-- shared key is the honest shape, and it makes the join to `messages` a
-- primary-key lookup.
--
-- A move is an UPDATE of mailbox_id/uid here. The content is never touched,
-- which is precisely the property that makes moves cheap and lets a message
-- survive a UIDVALIDITY reset without re-download.
--
-- `flags` is a bitmask of the system flags (see internal/store: FlagSeen etc.)
-- because they are a fixed, small, closed set and a bitmask filter costs
-- nothing. `keywords` is a text[] because user keywords are open-ended — and
-- because A6 maps labels onto IMAP keywords, so this column is where labels
-- physically live.
-- ---------------------------------------------------------------------------
CREATE TABLE message_state (
    message_id      bigint      PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    account_id      bigint      NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    mailbox_id      bigint      NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,

    -- IMAP identity. uidvalidity is carried alongside the uid so a stale row
    -- is detectable without joining mailboxes.
    uid             bigint      NOT NULL,
    uidvalidity     bigint      NOT NULL,

    flags           bigint      NOT NULL DEFAULT 0,
    keywords        text[]      NOT NULL DEFAULT '{}',

    -- The MODSEQ at which this state was last observed from the server. The
    -- CONDSTORE resume point, per message.
    modseq_seen     bigint      NOT NULL DEFAULT 0,

    -- Tombstone for JMAP Email/changes: an expunged message must remain
    -- reportable as "destroyed" until every client has caught up, so the row
    -- is marked rather than deleted.
    deleted_at      timestamptz,

    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- The sync engine's lookup: "what do I have for this mailbox and UID range".
-- UNIQUE because (mailbox, uidvalidity, uid) IS the IMAP identity — a
-- duplicate here would mean the same server message stored twice.
CREATE UNIQUE INDEX message_state_mbox_uid_key
    ON message_state (mailbox_id, uidvalidity, uid);

-- Email/changes feeds: everything that moved for an account since a cursor.
CREATE INDEX message_state_acct_updated ON message_state (account_id, updated_at);

-- The unread counters and the unread-only search shape (#7).
CREATE INDEX message_state_unread ON message_state (account_id, mailbox_id)
    WHERE (flags & 1) = 0 AND deleted_at IS NULL;

-- Keyword/label membership (A6): "every message carrying $MoovL7".
CREATE INDEX message_state_keywords ON message_state USING gin (keywords);

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- blob_refs — who holds a reference to which blob.
--
-- The refcount on `blobs` could in principle be derived by counting these
-- rows. It is kept denormalized anyway because the GC's question ("which blobs
-- are unreferenced and have been for a while") must be answerable by an index
-- scan of collection candidates, not by an aggregate over every reference in
-- the installation. The two are kept consistent inside the same transaction by
-- internal/blob's helpers, and a test asserts they agree.
--
-- `owner_kind` allows references from things that are not messages — a draft
-- being composed, a detached attachment — without a schema change per kind.
-- ---------------------------------------------------------------------------
CREATE TABLE blob_refs (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    sha256      bytea       NOT NULL REFERENCES blobs(sha256) ON DELETE RESTRICT,
    account_id  bigint      NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    owner_kind  text        NOT NULL CHECK (owner_kind IN ('message', 'part', 'draft', 'pin')),
    -- messages.id for 'message'; an internal id for the other kinds. NULL for
    -- a 'pin', which is a deliberate hold with no owner (used while a blob is
    -- written but its message row does not exist yet).
    owner_id    bigint,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- One reference per (blob, owner). The uniqueness is what makes AddRef
-- idempotent: a retried sync of the same message cannot inflate the count.
CREATE UNIQUE INDEX blob_refs_unique
    ON blob_refs (sha256, owner_kind, owner_id) WHERE owner_id IS NOT NULL;

CREATE INDEX blob_refs_sha ON blob_refs (sha256);
CREATE INDEX blob_refs_owner ON blob_refs (owner_kind, owner_id);

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- sync_log — per-account checkpoints, errors and breaker state (L2 §2.3).
--
-- One row per account per scope, where scope is a mailbox id as text or a
-- reserved name ('account' for account-wide state). A log in the sense of "the
-- current state of the sync", not an append-only history: the history that
-- matters for JMAP Email/changes is message_state.updated_at, and an
-- append-only table here would grow without bound for no reader.
-- ---------------------------------------------------------------------------
CREATE TABLE sync_log (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id      bigint      NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    scope           text        NOT NULL,

    -- Opaque to the store: the sync engine decides what a checkpoint means for
    -- a given scope (a UID watermark, a MODSEQ, a phase marker).
    checkpoint      jsonb       NOT NULL DEFAULT '{}'::jsonb,

    -- Monotonic per account. This is the cursor JMAP Email/changes hands out
    -- as its state string, so it must never go backwards.
    state_counter   bigint      NOT NULL DEFAULT 0,

    last_success_at timestamptz,
    last_error      text,
    last_error_at   timestamptz,
    consecutive_errors int      NOT NULL DEFAULT 0,

    -- Circuit breaker (ADR §4, anti fail2ban).
    breaker_state   text        NOT NULL DEFAULT 'closed'
        CHECK (breaker_state IN ('closed', 'open', 'half_open')),
    breaker_until   timestamptz,

    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT sync_log_account_scope_key UNIQUE (account_id, scope)
);

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- intents — client writes queued for the sync engine (L2 §4.3).
--
-- The JMAP layer never talks to IMAP. It enqueues an intent; the sync engine
-- executes it against Dovecot and reports back. This table is therefore the
-- API-to-engine contract, and the outbox of the ADR §4 transactional outbox
-- pattern.
--
-- Deliberately minimal for now: E3 owns the table, but the semantics of each
-- `kind` belong to the epic that executes it (flags/moves in E6, send in a
-- later phase).
-- ---------------------------------------------------------------------------
CREATE TABLE intents (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id   bigint      NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    kind         text        NOT NULL CHECK (kind IN ('flag', 'move', 'send')),
    payload      jsonb       NOT NULL DEFAULT '{}'::jsonb,

    state        text        NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'in_flight', 'done', 'failed')),
    attempts     int         NOT NULL DEFAULT 0,
    last_error   text,

    -- Lets a retry back off without blocking the queue head, and lets "undo
    -- send" hold a message for a few seconds before it is really sent.
    not_before   timestamptz NOT NULL DEFAULT now(),

    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- The claim query: the next runnable intent for an account, oldest first.
-- Partial, so the index holds only the work queue and not its history.
CREATE INDEX intents_runnable ON intents (account_id, not_before, id)
    WHERE state IN ('queued', 'in_flight');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Reverse dependency order. The tables are dropped; the extensions and the
-- database-level settings from 0001 are 0001's to reverse.
DROP TABLE IF EXISTS intents;
DROP TABLE IF EXISTS sync_log;
DROP TABLE IF EXISTS blob_refs;
DROP TABLE IF EXISTS message_state;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS blobs;
DROP TABLE IF EXISTS mailboxes;
DROP TABLE IF EXISTS accounts;
DROP FUNCTION IF EXISTS immutable_unaccent(text);

-- +goose StatementEnd
