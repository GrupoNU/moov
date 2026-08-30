-- Moov Mail — migration 0009: the threads table, snoozes and mutes
-- (L3 epic E4, arbitration GC-10).
--
-- Sources of truth: docs/specs/L3-gmail-class-plan.md §3 GC-10 and §4 E4,
-- docs/research/06-gmail-canon.md §2.2 (the Snooze and Mute rows) and §2.3
-- (schedule send), RFC 8621 §3 (Thread) and §5.2 (/changes).
--
-- ===========================================================================
-- 1. WHY A `threads` TABLE NOW, WHEN 0004 DELIBERATELY DECLINED ONE
-- ===========================================================================
--
-- Migration 0004 refused to build this table, and named exactly the condition
-- under which it should be built:
--
--     "What a threads table WOULD buy is a place to hang a thread's own state
--      if one ever appears — a snooze, a mute, a per-thread label. When that
--      day comes the table is added and thread_id becomes a foreign key to
--      it."
--
-- That day is this migration: mute is per-thread state by definition (Gmail's
-- canon §2.2 mutes a CONVERSATION, not a message), and it must survive a cache
-- rebuild, which `messages.thread_id` cannot — thread_id is the id of the
-- oldest member, and a rebuild re-inserts every message with new ids, so every
-- thread_id in the system changes. A mute keyed on thread_id would silently
-- unmute every muted conversation the first time an operator rebuilt the cache.
--
-- ===========================================================================
-- 2. THE DURABLE KEY — what makes a thread identity survive a rebuild
-- ===========================================================================
--
-- The key is the thread's ROOT MESSAGE-ID: the Message-ID of the oldest member
-- that the JWZ pass in internal/store/threads.go would resolve the chain to.
-- It is derived from message headers only, so it is a property of the MAIL
-- rather than of our storage, and re-deriving it from Dovecot yields the same
-- string every time.
--
-- Concretely (internal/store/threadkey.go is the one implementation):
--
--   a. the oldest member's own Message-ID, when it has one;
--   b. otherwise the FIRST entry of that member's References chain (which
--      names an ancestor we never received — a real and common case for a
--      thread that starts mid-conversation);
--   c. otherwise a deterministic digest of the normalized subject plus the
--      account, for the mailers that emit neither. Marked with a distinct
--      prefix so an operator can tell the three apart.
--
-- Why the ROOT rather than a fingerprint of the whole graph: the graph GROWS.
-- A fingerprint over every member's Message-ID would change on every reply,
-- which is the one thing an identity may not do — a mute keyed on it would
-- evaporate the moment the muted conversation received the reply the mute
-- exists to suppress. The root is the only part of the graph that is stable
-- under growth, and it is stable in the same direction thread_id is: JWZ
-- merges always keep the OLDER thread, so the root only ever moves backwards
-- in time, and it moves at all only when a genuine ancestor arrives late.
--
-- The key is per-account (accounts do not share threads: threads.go's whole
-- graph resolution is scoped to one account_id), so the natural key is
-- (account_id, root_message_id).
--
-- ===========================================================================
-- 3. TOMBSTONES, AND WHAT THEY DO AND DO NOT FIX FOR Thread/changes
-- ===========================================================================
--
-- A merge destroys a thread: two conversations become one, and RFC 8621 §3
-- plus ADR-001 §2 say the loser must be reported destroyed. Before this table
-- there was nowhere to record that event, which is one of the two reasons
-- Thread/changes declines (internal/jmap/mail/changes.go).
--
-- `merged_into` plus `destroyed_at` are that record: a merged thread keeps its
-- row, gains a tombstone, and points at its winner. Its id therefore remains
-- resolvable — a client holding the dead id can be told what replaced it —
-- and /changes has a row to put in `destroyed`.
--
-- What it does NOT fix is stated honestly here and in changes.go: created-vs-
-- updated is now exact (created_at is a column), destroyed-by-merge is now
-- exact (the tombstone), but destroyed-by-last-member-tombstoned still is not,
-- because deleting the last message of a thread does not write to this table.
-- That case is handled by keeping the thread row alive (a thread whose members
-- are all tombstoned is reported as UPDATED, not destroyed) — which is a
-- conforming answer that costs a client one Thread/get returning notFound,
-- rather than a wrong answer that costs it a corrupted cache.
--
-- ===========================================================================
-- 4. WHY THE TABLE IS NOT A FOREIGN KEY TARGET OF messages.thread_id
-- ===========================================================================
--
-- 0004 anticipated "thread_id becomes a foreign key to it". It does not,
-- deliberately. thread_id is assigned inside InsertMessages by the row's own
-- id default, BEFORE AssignThreads has had a chance to create or find the
-- thread row (threads.go documents at length why the two are separate
-- transactions and why the intermediate state must be valid). A foreign key
-- would make that valid intermediate state a constraint violation, turning a
-- crash-safe two-step into an all-or-nothing one. The link is maintained by
-- AssignThreads and repaired by ReindexThreads; the invariant is tested, not
-- enforced by the database, and that choice is recorded here so nobody
-- "fixes" it later.

-- +goose Up

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- threads — one row per conversation, with the durable key.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS threads (
    id                bigserial PRIMARY KEY,
    account_id        bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    -- The durable natural key (see §2 of the header). Never null: the
    -- derivation always yields something, falling back to a subject digest.
    root_message_id   text        NOT NULL,

    -- The CURRENT thread_id of this conversation — the messages.thread_id its
    -- members carry. It moves when a merge assigns a smaller id, which is why
    -- it is a plain column and not the primary key: the durable key is what
    -- identity means here, and this is a cache of where the messages point.
    thread_id         bigint      NOT NULL,

    -- The merge tombstone (§3). merged_into names the SURVIVING thread row.
    merged_into       bigint      REFERENCES threads (id) ON DELETE SET NULL,
    destroyed_at      timestamptz,

    created_at        timestamptz NOT NULL DEFAULT now(),
    -- The /changes watermark, in the same grammar every other type uses.
    updated_at        timestamptz NOT NULL DEFAULT now(),

    -- A tombstone must name its survivor and vice versa: a half-recorded merge
    -- is worse than none, because a client would be told the thread died with
    -- no way to find where it went.
    CONSTRAINT threads_tombstone_complete CHECK (
        (merged_into IS NULL AND destroyed_at IS NULL)
        OR (merged_into IS NOT NULL AND destroyed_at IS NOT NULL)
    ),
    CONSTRAINT threads_no_self_merge CHECK (merged_into IS NULL OR merged_into <> id)
);

-- The durable key is unique per account: that is what makes it a KEY rather
-- than a hint, and it is the constraint the insert path's ON CONFLICT uses to
-- make thread creation idempotent under concurrent sync workers.
CREATE UNIQUE INDEX IF NOT EXISTS threads_account_root
    ON threads (account_id, root_message_id);

-- The live lookup: "which thread row owns this messages.thread_id". Partial on
-- the tombstone, because a merged row's thread_id is stale by definition and
-- including it would make the lookup ambiguous.
CREATE UNIQUE INDEX IF NOT EXISTS threads_account_thread_live
    ON threads (account_id, thread_id)
    WHERE destroyed_at IS NULL;

-- The /changes feed: rows of one account after a cursor, in watermark order.
CREATE INDEX IF NOT EXISTS threads_account_updated
    ON threads (account_id, updated_at, id);

COMMENT ON TABLE threads IS
    'One row per conversation, keyed durably by the root Message-ID so the identity survives a cache rebuild (L3 E4, GC-10). messages.thread_id is the volatile id; root_message_id is the identity.';
COMMENT ON COLUMN threads.root_message_id IS
    'Durable natural key: the oldest member''s Message-ID, else its first Reference, else a subject digest (prefixed to distinguish). Derived from headers only — re-derivable from Dovecot.';
COMMENT ON COLUMN threads.merged_into IS
    'Set when this thread was absorbed by another: the surviving threads.id. With destroyed_at, this is the merge tombstone Thread/changes needs (RFC 8621 §3).';
-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- snoozes — the pending-wake record for a snoozed message (canon §2.2).
-- ---------------------------------------------------------------------------
--
-- GC-10 makes Dovecot the source of truth for the snooze ITSELF: a snoozed
-- message is MOVED to a dedicated `Snoozed` folder, so every IMAP client sees
-- it leave the inbox, and a cache rebuild rediscovers the snoozed set by
-- listing that folder. This table holds only what IMAP has no vocabulary for:
-- WHEN to wake it and WHERE it came from.
--
-- That is not Postgres-only state in GC-10's sense, and the distinction is
-- worth being precise about because the arbitration will be audited on it.
-- The user-visible FACT ("this mail is not in my inbox") lives in Dovecot. The
-- SCHEDULE ("bring it back on Tuesday") is Moov's own scheduling intent, has
-- no IMAP representation whatsoever, and its loss degrades to "the message
-- stays in Snoozed until the user moves it back" — visible, recoverable, and
-- not a lie about where mail is.
--
-- The key is the durable MESSAGE-ID, not a store id, for the same reason mute
-- uses the durable thread key: a rebuild renumbers every messages.id.
CREATE TABLE IF NOT EXISTS snoozes (
    id               bigserial PRIMARY KEY,
    account_id       bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    -- The durable identity of the snoozed message: its RFC 5322 Message-ID,
    -- without angle brackets, exactly as messages.message_id stores it.
    message_rfc_id   text        NOT NULL,

    -- When to bring it back. Any instant; the presets are the UI's business
    -- (canon §5 records that Gmail's own preset times are unsourced).
    wake_at          timestamptz NOT NULL,

    -- Where it came from, so the wake returns it to the right folder. It is
    -- the mailbox NAME rather than an id, because the id is store-local and
    -- this row must survive a rebuild; the name is Dovecot's own vocabulary.
    -- Empty means INBOX, which is the only origin Gmail's UI can produce.
    origin_mailbox   text        NOT NULL DEFAULT '',

    -- The wake's outcome, so a failure is visible rather than a silent
    -- disappearance: 'pending' until the waker runs, then 'woken' or 'failed'.
    state            text        NOT NULL DEFAULT 'pending',
    attempts         int         NOT NULL DEFAULT 0,
    last_error       text,

    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT snoozes_state_valid CHECK (state IN ('pending', 'woken', 'failed'))
);

-- One pending snooze per message. A second snooze of the same message REPLACES
-- the wake time (the ON CONFLICT of the upsert), which is what "snooze it
-- again for longer" means and what Gmail does.
CREATE UNIQUE INDEX IF NOT EXISTS snoozes_account_message_pending
    ON snoozes (account_id, message_rfc_id)
    WHERE state = 'pending';

-- The waker's poll: due rows, oldest first. Partial so the index holds only
-- work, not history.
CREATE INDEX IF NOT EXISTS snoozes_due
    ON snoozes (wake_at, id)
    WHERE state = 'pending';

COMMENT ON TABLE snoozes IS
    'Pending wake times for snoozed messages (L3 E4, GC-10). The snooze itself lives in Dovecot as a MOVE to the Snoozed folder; this table holds only the schedule, which IMAP cannot express.';
-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- mutes — muted conversations (canon §2.2).
-- ---------------------------------------------------------------------------
--
-- Mute is the one piece of E4 state that has no IMAP representation at all,
-- and GC-10 accepts that explicitly: "Mute es estado Moov-side pero con clave
-- durable". What makes it acceptable is the pair of properties the arbitration
-- demanded:
--
--   1. the KEY is durable (threads.id, whose identity is the root Message-ID),
--      so a cache rebuild keeps every mute rather than silently dropping it;
--   2. the EFFECT is executed in Dovecot — the engine archives the reply, so
--      every other IMAP client sees the same mailbox contents we do. Nothing
--      about where the mail LIVES is Postgres-only.
--
-- The alternative — a keyword like $muted on every member — was considered and
-- rejected: it would consume one of the 26 durable Maildir keywords the whole
-- label model is rationed against (GC-5), and it would have to be applied to
-- every future member of the thread by the same engine hook that archives
-- them, so it would buy visibility in other clients at the cost of the scarcest
-- resource in the system. Recorded here rather than re-litigated later.
CREATE TABLE IF NOT EXISTS mutes (
    thread_id   bigint      PRIMARY KEY REFERENCES threads (id) ON DELETE CASCADE,
    account_id  bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- The engine's question on every inbound message is "is this thread muted",
-- and its list question is "which of my threads are muted" — both served by
-- the account scope.
CREATE INDEX IF NOT EXISTS mutes_account ON mutes (account_id, thread_id);

COMMENT ON TABLE mutes IS
    'Muted conversations (L3 E4, GC-10). Keyed on threads.id, whose identity is the durable root Message-ID; the EFFECT (replies skip the inbox) is executed by the engine as an archive in Dovecot.';
-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- The backfill: one thread row per existing conversation.
-- ---------------------------------------------------------------------------
--
-- Unlike 0007, which correctly wrote nothing, this migration MUST backfill:
-- an account whose threads have no rows cannot be muted, and Thread/changes
-- would report every pre-existing conversation as created the first time a
-- reply touched it. The absence here is a broken feature, which is 0006's
-- criterion for backfilling.
--
-- The root Message-ID is derived in SQL for the backfill and in Go for the
-- runtime, and the two agree on the cases SQL can express: the oldest member's
-- own message_id (case a), else its first reference (case b). Case c — the
-- subject digest — is deliberately NOT reproduced in SQL. A thread whose
-- oldest member has neither a Message-ID nor a References chain gets no row
-- here, and the runtime creates it on the next touch with the Go
-- normalization, exactly as 0004 left thread_subject_keys empty for the same
-- reason ("a SQL approximation of it would seed keys the runtime never
-- produces").
--
-- Cost: one aggregate pass over messages, with DISTINCT ON served by the
-- (account_id, thread_id, date) index 0004 created. On the pilot's 26,869
-- messages this is the same shape 0004's own backfill ran in under 30 s
-- (deploy/README.md records that number honestly), and this one writes far
-- fewer rows: one per thread rather than one per message.
INSERT INTO threads (account_id, root_message_id, thread_id, created_at, updated_at)
SELECT account_id, root_message_id, thread_id, now(), now()
  FROM (
      SELECT DISTINCT ON (m.account_id, m.thread_id)
             m.account_id,
             m.thread_id,
             CASE
                 WHEN m.message_id IS NOT NULL AND m.message_id <> ''
                     THEN 'mid:' || m.message_id
                 WHEN m.references_ids IS NOT NULL AND array_length(m.references_ids, 1) > 0
                      AND m.references_ids[1] <> ''
                     THEN 'ref:' || m.references_ids[1]
                 ELSE NULL
             END AS root_message_id
        FROM messages m
       WHERE m.thread_id IS NOT NULL
       ORDER BY m.account_id, m.thread_id, m.date, m.id
  ) roots
 WHERE root_message_id IS NOT NULL
ON CONFLICT (account_id, root_message_id) DO NOTHING;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS mutes;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS snoozes;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS threads;
-- +goose StatementEnd
