-- Moov Mail — migration 0010: SieveScript id mapping + verified forwarding
-- addresses (L3 epic E6).
--
-- # What is deliberately NOT here: the rules
--
-- The filter rules, the vacation response and the forward-all recipe live in
-- the managed Sieve script ON DOVECOT (internal/sieve's metadata header) —
-- Dovecot is the source of truth, and a Moov store rebuild loses no filters.
-- These two tables hold only what genuinely cannot live in the script:
--
-- # sieve_scripts — the id ledger
--
-- RFC 9661 §2.1 makes a SieveScript's id "immutable; server-set" while its
-- NAME is mutable, and ManageSieve (RFC 5804) knows scripts only by name.
-- Something has to remember that "the script now named X is the object a
-- client knows as id 7", or every RENAMESCRIPT would break every client's
-- held ids. That mapping is A5-style lateral state: reconstructible cache
-- (rebuild = re-list the scripts and mint fresh ids; clients refetch on the
-- changed state string), never authoritative for content.
--
-- # forwarding_addresses — the verification ledger
--
-- GC-4's security design: mail may only be redirected to addresses that
-- PROVED consent (token mail, pending -> accepted). "Accepted" is a security
-- fact about a past verification event; encoding it inside a user-editable
-- script would let the script assert its own permission. So it lives here,
-- and the script generator refuses any redirect target without an accepted
-- row.
--
-- IF NOT EXISTS / replay-safety: same concurrency rationale as 0006 (parallel
-- test packages migrating one shared database).

-- +goose Up
-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS sieve_scripts (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id  bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    -- The ManageSieve script name this id currently maps to. Renames update
    -- this column; the id never changes (RFC 9661 §2.1).
    name        text        NOT NULL,

    -- The sha256 (hex) of the script content as last seen, so the blob
    -- reference (blob_refs, owner 'pin') can be moved when content changes.
    -- Empty until first read.
    content_sha text        NOT NULL DEFAULT '',

    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- One id per (account, name): the reconciler upserts on this.
CREATE UNIQUE INDEX IF NOT EXISTS sieve_scripts_account_name
    ON sieve_scripts (account_id, name);

-- The state cursor's watermark scan.
CREATE INDEX IF NOT EXISTS sieve_scripts_account_updated
    ON sieve_scripts (account_id, updated_at);

CREATE TABLE IF NOT EXISTS forwarding_addresses (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id  bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    -- The destination address, stored lowercased (matching is
    -- case-insensitive everywhere: enforcement, generation, display).
    email       text        NOT NULL,

    -- 'pending' until the verification token comes back, then 'accepted'.
    -- There is no 'rejected': an unconsumed token simply expires and the
    -- row can be destroyed and re-created to resend.
    state       text        NOT NULL DEFAULT 'pending'
                CHECK (state IN ('pending', 'accepted')),

    -- When the outstanding verification token stops being honored. NULL for
    -- accepted rows (nothing outstanding).
    token_expires_at timestamptz,

    verified_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- One row per (account, address).
CREATE UNIQUE INDEX IF NOT EXISTS forwarding_addresses_account_email
    ON forwarding_addresses (account_id, email);

-- The state cursor's watermark scan.
CREATE INDEX IF NOT EXISTS forwarding_addresses_account_updated
    ON forwarding_addresses (account_id, updated_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS forwarding_addresses;
DROP TABLE IF EXISTS sieve_scripts;

-- +goose StatementEnd
