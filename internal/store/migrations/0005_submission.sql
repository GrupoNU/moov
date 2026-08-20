-- Moov Mail — migration 0005: the transactional outbox (W3, L2-jmap-write W-A3).
--
-- Source of truth: ADR-001 §4 ("Outbox transaccional: SMTP → APPEND a \Sent;
-- nunca reintentar SMTP tras 250; dedupe por Message-ID"), L2-jmap-write §2
-- (W-A3: undo window in intents.not_before) and RFC 8621 §7 (EmailSubmission).
--
-- The intents table (0002) already IS the outbox — the JMAP layer enqueues, the
-- executor claims with FOR UPDATE SKIP LOCKED. What a 'send' intent additionally
-- needs is a PERSISTED execution phase, because the send rules are stated in
-- terms of what has already durably happened:
--
--   * accepted_at is THE sacred column. It is written the instant the SMTP
--     server's 250 answer to DATA is read, BEFORE any subsequent action
--     (ADR §4: "nunca reintentar SMTP tras 250" — the rule is only enforceable
--     if the 250 survives a crash). A 'send' intent with accepted_at set is
--     NEVER transmitted again, only its post-send steps retry.
--   * appended_at records the post-send step (the \Sent copy), so a crash
--     after it never re-appends and a crash before it retries only the copy.
--   * canceled_at is the undo (RFC 8621 §7.1 undoStatus "canceled"): a row can
--     only move queued -> canceled while accepted_at is NULL, enforced by the
--     store's single-statement compare-and-set, which is what makes the
--     cancel-vs-executor race have exactly one winner.
--   * destroyed_at is the EmailSubmission record's tombstone, kept for the same
--     reason message_state keeps deleted_at: EmailSubmission/changes must
--     report the destroy until every client caught up.
--
-- Phase columns rather than payload JSON: the crash-recovery decisions branch
-- on these values, and a decision that guards against double-sending real mail
-- must read a typed, indexed column — not a JSON field a payload rewrite could
-- drop.

-- +goose Up
-- +goose StatementBegin

-- The state CHECK gains 'canceled'. The constraint carries the name PostgreSQL
-- auto-assigned to the inline column constraint of 0002.
ALTER TABLE intents DROP CONSTRAINT intents_state_check;
ALTER TABLE intents ADD CONSTRAINT intents_state_check
    CHECK (state IN ('queued', 'in_flight', 'done', 'failed', 'canceled'));

-- The submission columns. All NULL for the non-send kinds and for every
-- pre-existing row, which is exactly the truthful reading: nothing has
-- happened to them.
ALTER TABLE intents
    -- The draft being sent (messages.id). A plain bigint, deliberately without
    -- a foreign key: the EmailSubmission object is a historical record
    -- (RFC 8621 §7.5 destroy "MUST NOT change the behaviour"), and destroying
    -- the Email must not cascade into or be blocked by the submission log.
    ADD COLUMN email_id       bigint,
    -- The RFC 5322 Message-ID of the transmitted message, angle brackets
    -- stripped — the dedupe key of ADR §4. Generated at enqueue time when the
    -- draft lacks one, so re-preparing the bytes after a crash reuses the SAME
    -- id, which is what makes the dedupe net actually catch a replay.
    ADD COLUMN message_rfc_id text,
    -- The moment the SMTP 250 to DATA was read, plus the server's own reply
    -- line (surfaced as RFC 8621 §7.1 deliveryStatus.smtpReply).
    ADD COLUMN accepted_at    timestamptz,
    ADD COLUMN accepted_reply text,
    -- Post-send phase: the \Sent copy (or its Message-ID dedupe skip).
    ADD COLUMN appended_at    timestamptz,
    -- Undo (W-A3) and the record tombstone.
    ADD COLUMN canceled_at    timestamptz,
    ADD COLUMN destroyed_at   timestamptz;

-- EmailSubmission/get and /changes read an account's send intents by id and by
-- recency. Partial on kind: the flag/move kinds never serve JMAP objects.
CREATE INDEX intents_send_account ON intents (account_id, id)
    WHERE kind = 'send';
CREATE INDEX intents_send_changed ON intents (account_id, updated_at, id)
    WHERE kind = 'send';

-- The outbox executor's global due-scan: "which accounts have runnable sends".
-- Partial on the runnable states so it holds the work queue, not its history.
CREATE INDEX intents_send_due ON intents (not_before, account_id)
    WHERE kind = 'send' AND state IN ('queued', 'in_flight');

-- The Message-ID dedupe probe: "does this account's Sent mailbox already hold
-- a message with this RFC 5322 id". messages.message_id had no index — nothing
-- read it by equality before this epic.
CREATE INDEX messages_acct_message_id ON messages (account_id, message_id)
    WHERE message_id <> '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS messages_acct_message_id;
DROP INDEX IF EXISTS intents_send_due;
DROP INDEX IF EXISTS intents_send_changed;
DROP INDEX IF EXISTS intents_send_account;

ALTER TABLE intents
    DROP COLUMN IF EXISTS destroyed_at,
    DROP COLUMN IF EXISTS canceled_at,
    DROP COLUMN IF EXISTS appended_at,
    DROP COLUMN IF EXISTS accepted_reply,
    DROP COLUMN IF EXISTS accepted_at,
    DROP COLUMN IF EXISTS message_rfc_id,
    DROP COLUMN IF EXISTS email_id;

-- Restoring the narrower CHECK requires no 'canceled' rows to remain.
UPDATE intents SET state = 'failed' WHERE state = 'canceled';
ALTER TABLE intents DROP CONSTRAINT intents_state_check;
ALTER TABLE intents ADD CONSTRAINT intents_state_check
    CHECK (state IN ('queued', 'in_flight', 'done', 'failed'));

-- +goose StatementEnd
