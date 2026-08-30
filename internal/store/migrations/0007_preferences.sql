-- Moov Mail — migration 0007: per-account preferences (L3 epic E0).
--
-- Everything a user can configure about their own mail experience lives here:
-- the undo-send window, whether images load, conversation view, density,
-- theme, keyboard shortcuts. Until now the PWA kept those in localStorage,
-- which is not a preference store — it is a per-browser cache. A user who logs
-- in from a second machine gets factory defaults, a user who clears site data
-- loses their settings, and the SERVER cannot read any of it, which matters
-- because at least one preference (undoSendSeconds) governs behavior the
-- server executes on its own after the browser is gone (internal/submit's
-- outbox releases the mail seconds later, from a daemon the tab never talks to
-- again).
--
-- # Why one JSONB column and not a column per preference
--
-- The obvious alternative is a wide table: `undo_send_seconds int`,
-- `images_policy text`, one column per setting with CHECK constraints. It is
-- rejected on the shape of the change stream rather than on taste.
--
-- The preference set is the fastest-moving schema in the product: E5 adds the
-- whole settings surface, E4 adds snooze defaults, E9 adds offline and
-- notification choices, and every one of those is a migration whose only
-- content is "one more column, nullable, with a default". Fourteen preferences
-- today is a conservative floor. Meanwhile NOTHING queries a preference: they
-- are read as a whole object, by exactly one account, on session open — there
-- is no `WHERE density = 'compact'` anywhere in the product and no reason for
-- one to appear. Columns buy indexability and per-field type enforcement; this
-- table needs neither, and pays for both in migration churn.
--
-- What the JSONB blob does NOT buy is permission to store anything. The typed
-- schema lives in Go (internal/store/prefs.go), is validated on every write,
-- and REJECTS unknown keys — so the blob's contents are as constrained as a
-- column set would make them, with the constraint expressed where the error
-- message can cite the JMAP property that was wrong. The database enforces the
-- two things it is genuinely better at: the object is an object (not an array
-- or a scalar), and the version is present.
--
-- # schema_version, and why it is a column rather than only a JSON key
--
-- `prefs` carries its own `"v"` key — the value the Go migration chain reads.
-- The column duplicates it, deliberately, because the two answer different
-- questions. The JSON key is what the READER migrates from; the column is what
-- an OPERATOR can query without parsing JSON ("how many accounts are still on
-- v1?"), and what a future data migration can index and batch on. A trigger is
-- not used to keep them in sync: the single writer (PutPrefs) writes both from
-- the same validated struct, and a test pins that they agree.
--
-- # The state cursor
--
-- Identity (migration 0006) established this server's cursor grammar:
-- "<max(updated_at) nanos>-<row count>" (internal/jmap/mail/adapter.go
-- stateFor). Preferences reproduce it exactly — updated_at per row, count of
-- rows — so Prefs/get's state string is built by the same helper and means the
-- same thing to a client.
--
-- The count term looks degenerate here, since an account has at most ONE prefs
-- row and cannot destroy it. It is kept anyway, and that is not cargo cult:
-- the count is what makes the state move when the row FIRST APPEARS. Before
-- any write, an account has no row and its state is "0-0"; after the first
-- PutPrefs it is "<nanos>-1". A watermark-only cursor would render both as a
-- timestamp and a client holding the pre-write state could not tell the
-- account's preferences had been created. The same grammar therefore stays
-- honest for the same reason it does for identities.

-- +goose Up
-- +goose StatementBegin

-- IF NOT EXISTS for the reason migration 0006 documents at length: the Go test
-- suite runs packages in parallel against ONE database and several of them
-- call store.Migrate at startup, so two runners can reach this file at the
-- same version. Idempotent DDL costs nothing in production (migrations run
-- once, at moovd startup) and removes a flaky failure with nothing to teach.
CREATE TABLE IF NOT EXISTS account_prefs (
    -- The account IS the key: one preference document per mailbox, no
    -- surrogate id. A JMAP client never names a prefs id — the object is a
    -- singleton (RFC 8621 §8 gives VacationResponse exactly this shape,
    -- "id: singleton"), so an id column would be a value nothing ever reads.
    account_id     bigint      PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,

    -- The preference document. Validated and versioned in Go; see
    -- internal/store/prefs.go for the typed schema and the migration chain.
    --
    -- DEFAULT '{}' is the empty document rather than the default preferences.
    -- That distinction is load-bearing: defaults are a PRODUCT decision that
    -- changes between releases (D-3 flipped keyboard shortcuts ON against
    -- Gmail's off-default, and a later arbitration could flip another), and
    -- freezing today's defaults into rows would make every existing account
    -- immune to the change while new accounts got it. Storing only what the
    -- user actually chose, and filling defaults on READ, means a default that
    -- moves moves for everyone who never expressed an opinion — which is what
    -- "default" means.
    prefs          jsonb       NOT NULL DEFAULT '{}',

    -- The schema version of `prefs`, mirrored out of its "v" key. See the
    -- header for why both exist.
    schema_version int         NOT NULL,

    created_at     timestamptz NOT NULL DEFAULT now(),
    -- The state cursor's watermark (see the header).
    updated_at     timestamptz NOT NULL DEFAULT now(),

    -- The two invariants worth enforcing in the database, because a violation
    -- of either would mean the Go layer is broken and the blob is no longer
    -- interpretable:
    --
    --   1. the document is a JSON OBJECT. jsonb accepts `[1,2]`, `"x"` and
    --      `null` as perfectly valid values; every one of them would make
    --      migratePrefs' unmarshal fail on read, turning a bad write into a
    --      read-time error for a user who did nothing wrong.
    --   2. the version is a version. Zero and negatives are not versions this
    --      chain can start from; a row carrying one could never be migrated
    --      forward.
    CONSTRAINT account_prefs_is_object CHECK (jsonb_typeof(prefs) = 'object'),
    CONSTRAINT account_prefs_version_positive CHECK (schema_version > 0)
);

COMMENT ON COLUMN account_prefs.prefs IS
    'Typed preference document, schema and migration chain in internal/store/prefs.go. Unknown keys are rejected at the JMAP layer; this column never holds a key the current schema does not define.';

COMMENT ON COLUMN account_prefs.schema_version IS
    'Mirror of the prefs document''s "v" key, for operator queries and future batch migrations. PutPrefs writes both from one struct.';

-- The state cursor's watermark scan. It is a one-row lookup by primary key in
-- practice, so this index is not about the per-account read; it exists so a
-- future fleet-wide "which accounts changed since" (an admin surface, or a
-- backfill that must resume) is a range scan rather than a sequential one, at
-- the cost of one small index on a table with one row per mailbox.
CREATE INDEX IF NOT EXISTS account_prefs_updated ON account_prefs (updated_at);

-- No backfill.
--
-- Migration 0006 backfilled identities because an account WITHOUT an identity
-- cannot send mail — the absence was a broken feature. The absence of a prefs
-- row is not broken: it is the accurate statement "this user has expressed no
-- preferences", which the read path renders as the full default object. Every
-- existing account therefore gets correct behavior with no rows written, and
-- the first row appears when someone actually changes a setting.

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS account_prefs;

-- +goose StatementEnd
