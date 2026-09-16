-- Moov Mail — migration 0013: delegated sessions and the replay cache behind
-- them (epic M2, docs/specs/L2-accounts-api-contract.md §3).
--
-- Delegated sign-in turns an issuer-signed JWT into an opaque Moov session
-- token. Two facts have to outlive the process for the contract to hold:
--
--   1. THE SESSION. The token the browser holds is `mds1_` + 32 random bytes;
--      the server keeps only its SHA-256 (§3.4 "stored hashed server-side"), so
--      a read of this table yields nothing a caller could present. A row is
--      the session's whole life: when it was issued, when it expires, the
--      absolute lifetime it may be renewed up to, and when it was revoked.
--      Keeping it in the store rather than in process memory is what makes
--      "the user is still signed in after moovd restarts" true, and what lets
--      an issuer-initiated revoke (§3.6) or an accounts-API suspend (§2.4)
--      name every live session of one mailbox.
--
--   2. THE `jti` REPLAY CACHE. §3.2: "Moov remembers every accepted jti until
--      its exp: a second presentation is refused". A process-local cache would
--      re-admit a captured token across a restart inside its five-minute
--      window; the table makes the refusal durable. Rows are tiny and
--      short-lived (exp is at most 5 min out), and the writer sweeps expired
--      ones opportunistically, so the table never grows past a few minutes of
--      traffic.
--
-- The session's account is a real foreign key with ON DELETE CASCADE: deleting
-- an account (accounts API, moovctl) takes its sessions with it, so a session
-- can never outlive the mailbox it grants.
--
-- +goose Up
-- +goose StatementBegin

CREATE TABLE delegated_sessions (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- SHA-256 of the presented token. Unique by construction (256 random bits
    -- behind it); the constraint exists so a hash collision — impossible in
    -- practice — would fail loudly instead of merging two sessions.
    token_hash          bytea NOT NULL UNIQUE,
    account_id          bigint NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    -- The `iss` the session was created through. §3.6 revokes by (sub, iss):
    -- a sign-out at one portal must not end sessions another issuer created.
    issuer              text NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    -- Sliding expiry: 12 h from issue, reset by renewal. Renewal shortens the
    -- OLD row's expiry to a 60 s grace instead of revoking it, so requests in
    -- flight with the previous token still complete (§3.5).
    expires_at          timestamptz NOT NULL,
    -- The hard stop: no renewal past it, whatever expires_at says (§3.4).
    absolute_expires_at timestamptz NOT NULL,
    revoked_at          timestamptz,
    -- Throttled to one write per minute per session by the server; it is a
    -- diagnostic ("is anyone still using this?"), not an audit trail.
    last_seen_at        timestamptz,
    CONSTRAINT delegated_sessions_expiry_order
        CHECK (expires_at <= absolute_expires_at + interval '60 seconds')
);

COMMENT ON TABLE delegated_sessions IS
    'Delegated sign-in sessions (M2). token_hash is SHA-256 of the opaque '
    'mds1_ token; the token itself is never stored. Revocation and expiry are '
    'checked on every request — see internal/jmaphttp/delegated.go.';

-- The per-account lookups: "every live session of this mailbox" for the
-- issuer revoke and the accounts-API suspend, and the active-sessions gauge.
CREATE INDEX delegated_sessions_account_live
    ON delegated_sessions (account_id, issuer)
    WHERE revoked_at IS NULL;

-- Accepted JWT ids, kept until the token's own exp (§3.2). The primary key IS
-- the replay check: a second INSERT of the same (issuer, jti) conflicts.
CREATE TABLE delegated_jti (
    issuer     text NOT NULL,
    jti        text NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (issuer, jti)
);

COMMENT ON TABLE delegated_jti IS
    'Replay cache for delegated sign-in tokens: every accepted jti until its '
    'exp. Swept opportunistically by the writer; never more than a few minutes '
    'of rows.';

CREATE INDEX delegated_jti_expires ON delegated_jti (expires_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS delegated_jti;
DROP TABLE IF EXISTS delegated_sessions;

-- +goose StatementEnd
