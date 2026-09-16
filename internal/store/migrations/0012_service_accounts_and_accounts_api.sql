-- Moov Mail — migration 0012: service accounts and the per-domain accounts API
-- (epic M1 of docs/briefs/2026-09-14-corppass-event-mailboxes.md; the wire
-- contract is docs/specs/L2-accounts-api-contract.md).
--
-- ===========================================================================
-- 1. WHAT IS STORED HERE, AND WHAT DELIBERATELY IS NOT
-- ===========================================================================
--
-- Dovecot stays the source of truth for MAIL (ADR-001; Moov is a rebuildable
-- cache). This migration stores facts about MOOV'S OWN BEHAVIOUR towards an
-- account — "sending is locked", "this account is suspended by an API
-- caller", "the purge started at T" — and mirrors of Mailcow settings Moov
-- itself wrote (the name, the quota, the limits). None of it describes mailbox
-- content, and every column on `accounts` has a default so the rows that
-- predate this migration are valid without a backfill: an account provisioned
-- by `moovctl account add` is simply "active, not read-only, not suspended".
--
-- ===========================================================================
-- 2. THE ACCOUNT FACTS (contract §2.3/§2.4, deviation D3)
-- ===========================================================================
--
-- `read_only` and `suspended` are SEPARATE booleans rather than one enum,
-- because the contract's state machine round-trips a suspended read-only
-- account back to `readonly` on resume: the headline `state` is DERIVED with
-- precedence deleting > suspended > readonly > active, and the facts are what
-- the derivation reads. `deleting_since` doubles as the deleting flag.
--
-- The engine's own `state` column keeps its meaning: a suspended or deleting
-- account is set to 'disabled' there, which is what already stops the sync
-- supervisor and makes the authenticator refuse the next request — no second
-- gate had to be taught about the new facts.
--
-- `mailcow_app_password_id` is the id of the app password Moov minted for the
-- account. Until now provisioning REPORTED it and forgot it; the read-only
-- transition needs it to delete the old credential after re-issuing one
-- without SMTP (contract §2.4, F0 answer P3). NULL for accounts provisioned
-- with a hand-made app password (`moovctl account add -app-password`), which
-- Moov never could revoke.
--
-- ===========================================================================
-- 3. SERVICE ACCOUNTS (contract §2.1)
-- ===========================================================================
--
-- The key is `msa1_` + 43 base64url characters of 32 random bytes and is
-- stored as its SHA-256. A 256-bit random secret needs no slow hash: the hash
-- exists so a database dump does not hand out live keys, not to survive a
-- guessing attack that cannot happen at 2^256. The lookup is by the hash
-- (unique index), which is what makes authentication one indexed read.
--
-- One domain per key is the security boundary of the whole API (contract §4):
-- Mailcow does not scope its own key by domain, so Moov checks the address's
-- domain against this column BEFORE any Mailcow call.
--
-- ===========================================================================
-- 4. AUDIT AND EXPORTS
-- ===========================================================================
--
-- `account_audit` is append-only: one row per write, with the actor, action,
-- address, result, request id and the caller's optional reason (§2.4). The
-- address is stored as TEXT, not as a foreign key, because the row must
-- outlive the account it describes — "recreated after deletion" is exactly
-- the audited event the contract names.
--
-- `account_exports` keeps the account id as ON DELETE SET NULL for the same
-- reason: a purged export must keep answering 410 (§2.6) even after the
-- account itself is gone, and a cascade would turn that into the 404 the
-- contract reserves for "never existed / not yours".
--
-- COST: every statement here is a metadata-only change (ADD COLUMN with a
-- constant default is catalog-only on PostgreSQL 11+; CREATE TABLE of empty
-- tables). No backfill touches existing rows. Measured shape: sub-second on
-- the pilot's populated store.

-- +goose Up
-- +goose StatementBegin

ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS display_name              text        NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS read_only                 boolean     NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS read_only_since           timestamptz,
    ADD COLUMN IF NOT EXISTS suspended                 boolean     NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS suspended_at              timestamptz,
    ADD COLUMN IF NOT EXISTS deleting_since            timestamptz,
    ADD COLUMN IF NOT EXISTS last_access_at            timestamptz,
    -- Mirrors of what Moov wrote to Mailcow, and the Moov-enforced limits.
    -- 0 / NULL means "not managed through the accounts API": the resource
    -- then reports the installation defaults.
    ADD COLUMN IF NOT EXISTS quota_mb                  integer     NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS send_per_day              integer,
    ADD COLUMN IF NOT EXISTS recipients_per_message    integer,
    ADD COLUMN IF NOT EXISTS attachment_mb             integer,
    ADD COLUMN IF NOT EXISTS mailcow_app_password_id   bigint;

COMMENT ON COLUMN accounts.read_only IS
    'Sending locked by the accounts API (contract §2.4): the app password was re-issued without SMTP and EmailSubmission/set refuses creates.';
COMMENT ON COLUMN accounts.suspended IS
    'Suspended by the accounts API. The engine state is disabled while this is true; resume restores active.';
COMMENT ON COLUMN accounts.deleting_since IS
    'Set by DELETE /admin/accounts; the row disappears when the background purge finishes.';
COMMENT ON COLUMN accounts.mailcow_app_password_id IS
    'Mailcow row id of the app password Moov minted, so a later re-issue can delete it. NULL when Moov did not mint it.';

-- +goose StatementEnd

-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS service_accounts (
    id            text        PRIMARY KEY,
    -- SHA-256 of the secret; the secret itself is shown once and never stored.
    key_hash      bytea       NOT NULL,
    domain        text        NOT NULL,
    scopes        text[]      NOT NULL,
    name          text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    revoked_at    timestamptz,
    last_used_at  timestamptz,
    CONSTRAINT service_accounts_key_hash_key UNIQUE (key_hash),
    CONSTRAINT service_accounts_domain_lower CHECK (domain = lower(domain))
);

COMMENT ON TABLE service_accounts IS
    'API keys of the per-domain accounts API (contract §2.1). One domain per key; scopes accounts:read / accounts:write.';

-- +goose StatementEnd

-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS account_audit (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at           timestamptz NOT NULL DEFAULT now(),
    actor_id     text        NOT NULL,
    actor_name   text        NOT NULL DEFAULT '',
    action       text        NOT NULL,
    address      text        NOT NULL,
    result       text        NOT NULL,
    request_id   text        NOT NULL DEFAULT '',
    reason       text        NOT NULL DEFAULT '',
    note         text        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS account_audit_address_at ON account_audit (address, at DESC);

COMMENT ON TABLE account_audit IS
    'Append-only: one row per accounts-API write (contract §2.4). The address is text so the row outlives the account.';

-- +goose StatementEnd

-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS account_exports (
    id              text        PRIMARY KEY,
    account_id      bigint      REFERENCES accounts(id) ON DELETE SET NULL,
    address         text        NOT NULL,
    status          text        NOT NULL
        CHECK (status IN ('pending', 'running', 'ready', 'failed', 'expired')),
    requested_at    timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    completed_at    timestamptz,
    messages_done   integer     NOT NULL DEFAULT 0,
    messages_total  integer     NOT NULL DEFAULT 0,
    -- Filled when ready.
    path            text        NOT NULL DEFAULT '',
    bytes           bigint      NOT NULL DEFAULT 0,
    sha256          text        NOT NULL DEFAULT '',
    messages        integer     NOT NULL DEFAULT 0,
    mailboxes       integer     NOT NULL DEFAULT 0,
    -- Filled when failed. Human-readable, no internals (contract: Export.error).
    error           text        NOT NULL DEFAULT '',
    -- Set when the zip was removed by the retention sweep; the row stays so
    -- the download route can answer 410 rather than 404.
    purged_at       timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS account_exports_address_requested
    ON account_exports (address, requested_at DESC);

CREATE INDEX IF NOT EXISTS account_exports_pending
    ON account_exports (requested_at) WHERE status = 'pending';

COMMENT ON TABLE account_exports IS
    'Background EML exports of the accounts API (contract §2.6). account_id is SET NULL on purge so a purged export still answers 410.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS account_exports;
DROP TABLE IF EXISTS account_audit;
DROP TABLE IF EXISTS service_accounts;

ALTER TABLE accounts
    DROP COLUMN IF EXISTS display_name,
    DROP COLUMN IF EXISTS read_only,
    DROP COLUMN IF EXISTS read_only_since,
    DROP COLUMN IF EXISTS suspended,
    DROP COLUMN IF EXISTS suspended_at,
    DROP COLUMN IF EXISTS deleting_since,
    DROP COLUMN IF EXISTS last_access_at,
    DROP COLUMN IF EXISTS quota_mb,
    DROP COLUMN IF EXISTS send_per_day,
    DROP COLUMN IF EXISTS recipients_per_message,
    DROP COLUMN IF EXISTS attachment_mb,
    DROP COLUMN IF EXISTS mailcow_app_password_id;

-- +goose StatementEnd
