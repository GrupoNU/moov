-- Moov Mail — migration 0006: editable identities (RFC 8621 §6).
--
-- W3 served ONE identity per account, computed on the fly from the caller's
-- address: `{id:"primary", name: <email>, email: <email>, signatures: ""}`.
-- That was truthful while nothing was editable, and Identity/set answered a
-- flat `forbidden`. A real pilot user then tried to save a signature, which is
-- precisely what §6 exists for — so the identity stops being a derived value
-- and becomes stored state.
--
-- # Why a table and not columns on `accounts`
--
-- §6 is explicit that an account may hold MORE than one identity: "Multiple
-- identities with the same email address MAY exist, to allow for different
-- settings the user wants to pick between (for example, with different
-- names/signatures)." Phase 1 refuses to CREATE additional identities (see
-- identity.go: an unverified sender address is a spoofing vector, and §9.6
-- makes rejecting it a MUST when the user lacks permission), but the storage
-- must not have to be re-shaped when alias verification lands. A child table
-- keyed by account is the shape §6 already describes; five columns bolted onto
-- `accounts` would have to be migrated away the moment a second row is legal.
--
-- The second reason is the state cursor. Identity/changes (§6.2) is a standard
-- /changes method, and this server's cursor grammar everywhere else is
-- "<max(updated_at) nanos>-<row count>" (adapter.go stateFor). A per-row
-- updated_at plus a row count reproduces that grammar exactly, including the
-- property that a DESTROY moves the state (count falls) even when no surviving
-- row was touched. Columns on `accounts` could not express a destroy at all.
--
-- # is_default
--
-- Exactly one row per account carries is_default, enforced by a partial unique
-- index. It marks the identity that IS the mailbox: its `email` is the
-- account's own address, it cannot be destroyed (§6 mayDelete: "Servers may
-- wish to set this to false for the user's username or other default address"),
-- and EmailSubmission falls back to it. The flag is a column rather than an
-- inference from `email = accounts.email` because §6 permits several rows to
-- share one address, so the address cannot identify the default.
--
-- # The backfill
--
-- Every existing account gets its derived identity materialized as a row, in
-- the same statement that creates the table. The pilot is running with four
-- real accounts whose clients hold `identityId: "primary"` — which was a
-- CONSTANT, not an id this server had ever issued from a sequence. Those
-- clients must keep working across this deploy, so the wire id must keep
-- resolving. See identity.go's id scheme: the default identity always renders
-- as "primary" regardless of its row id, so a stored EmailSubmission that
-- references it still resolves after the migration.

-- +goose Up
-- +goose StatementBegin

-- IF NOT EXISTS throughout this migration, which earlier ones do not use.
--
-- The reason is concurrency, and it is a real condition rather than a
-- defensive tic: goose serializes on its own version table, but the Go test
-- suite runs PACKAGES in parallel against ONE shared database, and several of
-- them call store.Migrate on startup. Two packages migrating at once both see
-- version 5, both run this file, and the loser hits
-- `relation "identities" already exists`. That race exists for every migration
-- here; 0006 is simply the first one added after enough packages migrated
-- concurrently for it to surface (internal/sync's watcher tests against
-- internal/store's).
--
-- Making the DDL idempotent is the narrow fix: it costs nothing in production,
-- where migrations run once at moovd startup, and it removes a flaky failure
-- that has nothing to teach. The backfill below is guarded separately, by the
-- NOT EXISTS predicate in its SELECT.
CREATE TABLE IF NOT EXISTS identities (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id    bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    -- The default identity — the account's own mailbox address. See above.
    is_default    boolean     NOT NULL DEFAULT false,

    -- §6 "email: String (immutable) — The 'From' email address the client MUST
    -- use when creating a new Email from this Identity." Immutable is enforced
    -- in the handler (an update naming it answers invalidProperties), not by a
    -- trigger: the refusal has to carry a §6 citation back to the client.
    email         text        NOT NULL,

    -- §6 "name: String (default: "")" — the From display name.
    name          text        NOT NULL DEFAULT '',

    -- §6 "replyTo: EmailAddress[]|null (default: null)" and
    -- §6 "bcc: EmailAddress[]|null (default: null)".
    --
    -- jsonb NULL is the RFC's null, distinct from an empty array: null means
    -- "this identity configures no Reply-To", `[]` would claim a configured
    -- empty list. The handler preserves that distinction on the way out.
    reply_to      jsonb,
    bcc           jsonb,

    -- §6 textSignature / htmlSignature, both "String (default: "")".
    --
    -- html_signature is stored SANITIZED, never raw (identity.go documents the
    -- threat model: this is content Moov transmits, so the sanitizer runs on
    -- the way IN and the database never holds a script the send path could
    -- pick up).
    text_signature text       NOT NULL DEFAULT '',
    html_signature text       NOT NULL DEFAULT '',

    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Identity/get and /changes read an account's identities in id order.
CREATE INDEX IF NOT EXISTS identities_account ON identities (account_id, id);

-- The state cursor's watermark scan: max(updated_at) per account.
CREATE INDEX IF NOT EXISTS identities_account_updated ON identities (account_id, updated_at);

-- Exactly one default per account.
CREATE UNIQUE INDEX IF NOT EXISTS identities_one_default ON identities (account_id)
    WHERE is_default;

COMMENT ON COLUMN identities.html_signature IS
    'Sanitized HTML snippet (internal/jmap/mail/signature.go). Never raw client input: this content is transmitted in outgoing mail.';

-- Backfill: materialize the identity every existing account was already being
-- served. name defaults to the address, which is exactly what handleIdentityGet
-- returned before this migration, so no client sees a value change.
--
-- The NOT EXISTS guard is the backfill's half of the concurrency note above:
-- with CREATE TABLE IF NOT EXISTS, a racing second runner reaches this INSERT
-- against a table the winner already populated, and an unguarded INSERT would
-- give every account a SECOND default identity — which the partial unique
-- index would then reject, failing the migration. Skipping accounts that
-- already have one makes the whole file replay-safe.
INSERT INTO identities (account_id, is_default, email, name)
SELECT a.id, true, a.email, a.email
  FROM accounts a
 WHERE NOT EXISTS (
       SELECT 1 FROM identities i WHERE i.account_id = a.id AND i.is_default);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS identities;

-- +goose StatementEnd
