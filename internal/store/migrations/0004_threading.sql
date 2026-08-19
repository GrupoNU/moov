-- Moov Mail — migration 0004: real threading.
--
-- Source of truth: L2-sync-engine.md §2.3 ("Threading propio por References /
-- In-Reply-To + fallback por subject normalizado; algoritmo JWZ simplificado"),
-- RFC 8621 §3 and §4.1.1, and ADR-001 §2 ("fusión de threads emite
-- destroyed+created").
--
-- Until this migration the JMAP layer DERIVED threads per request from the
-- References chain (internal/jmap/mail/thread.go). That derivation could not do
-- subject joining, could not group a thread whose root is not stored locally,
-- and made Mailbox.totalThreads / unreadThreads approximate. This migration
-- gives threads a column, an identity, and an index — which is what turns all
-- three into exact, indexed reads.
--
-- ===========================================================================
-- THE DESIGN, AND THE ONE CONSTRAINT THAT DICTATES IT
-- ===========================================================================
--
-- RFC 8621 §4.1.1 defines the Email property:
--
--     threadId: "Id" (immutable; server-set) The id of the Thread to which
--     this Email belongs.
--
-- and §3 states the consequence for out-of-order arrival:
--
--     "Since the 'threadId' of an Email is immutable, if the server wishes to
--      merge the Threads, it MUST handle this by deleting and reinserting
--      (with a new Email id) the Emails that change 'threadId'."
--
-- Moov cannot reinsert an Email with a new id: the Email id IS messages.id,
-- which anchors message_state's IMAP identity (mailbox, uidvalidity, uid) and
-- every blob reference. Destroying and recreating a message row on a THREADING
-- event would make an IMAP-sourced identity churn for a reason that has nothing
-- to do with IMAP, and would re-download nothing while invalidating every
-- client's cache of a message that did not change.
--
-- ADR-001 §2 already arbitrated this, before the column existed: "fusión de
-- threads emite destroyed+created (el frontend lo tolera)" — the DESTROYED and
-- CREATED are Thread events, not Email events. This migration is built to make
-- that arbitration cheap and, above all, RARE:
--
--   THE WINNER OF A MERGE IS ALWAYS THE OLDEST THREAD.
--
-- thread_id holds the messages.id of the thread's oldest member, so thread
-- identity is a MINIMUM over a set that only ever grows. A merge therefore
-- moves the younger thread's members onto the older id and never the reverse:
--
--   * the overwhelmingly common case — a reply arriving after its parent —
--     assigns the NEW message to the EXISTING thread id. No existing message's
--     threadId changes at all. This is the case the RFC's immutability rule is
--     really about, and it is honoured exactly.
--   * the rare case — a parent arriving after its children, the third message
--     that joins two orphan halves — moves the YOUNGER members onto the older
--     id. Their threadId changes, which the RFC would rather we handled by
--     reinsertion; the ADR chose the honest alternative instead. Those messages
--     have message_state.updated_at bumped in the same transaction, so
--     Email/changes reports them as updated and a client refetches the new
--     threadId; Thread/changes sees the loser id vanish and the winner grow.
--   * because the winner is a MINIMUM, thread ids never oscillate. A thread's
--     id can only ever move to a SMALLER (older) message id, at most once per
--     ancestor discovered, and it converges. An arbitrary winner choice — the
--     larger thread, say — could ping-pong two threads forever as messages
--     arrive.
--
-- ===========================================================================
-- WHY thread_id LIVES ON `messages` AND NOT ON `message_state`
-- ===========================================================================
--
-- Arbitration A5 (migration 0002) split the volatile columns out of `messages`
-- because writing that row rewrites the ~2.2 KB generated tsv into two GIN
-- indexes, at ~0.58 ms per row. thread_id is written on the insert path (free —
-- the row is being written anyway) and, on a merge, for the losing members.
--
-- It still belongs on `messages`, for two reasons:
--
--   1. Threading is a property of the message's CONTENT — its Message-ID,
--      References and Subject headers, all of which live here and none of which
--      change. A5's rule is "volatile because a user clicked something"; a
--      merge is not a user action, and a message's thread is stable for its
--      whole life except for the rare ancestor discovery.
--   2. The counts query (COUNT(DISTINCT thread_id) per mailbox) joins
--      message_state to messages anyway, and the thread reads are all
--      account-scoped scans of `messages`.
--
-- The cost is real and is accepted knowingly: a merge rewrites the tsv of the
-- losing members. THAT is why the merge is bounded (see thread_merge_max_members
-- in internal/store/threads.go) and why the winner rule minimizes how often it
-- happens at all.
--
-- ===========================================================================
-- BACKFILL COST MODEL (read this before deploying to a large installation)
-- ===========================================================================
--
-- The backfill below is a SINGLE-PASS, SET-BASED assignment: every message gets
-- thread_id = its own id, then two UPDATE … FROM statements join messages to
-- their stored parents. It is bounded by the size of the `messages` table and
-- runs inside the migration's transaction, under a lock.
--
--   pilot        ~27,000 messages  -> well under a second
--   1M messages                    -> tens of seconds, and the UPDATE rewrites
--                                     every row (tsv + 2 GIN indexes)
--   5M messages (the S3 scale)     -> DO NOT run this on start. Budget the
--                                     table rewrite plus the ~40 min GIN
--                                     rebuild S3 §5.2 measured, in a window
--                                     where search may be unavailable.
--
-- The backfill deliberately resolves only ONE level of ancestry (parent by
-- Message-ID, then subject-fallback among the remainder), not the transitive
-- closure. A full JWZ pass over an existing corpus is the job of a reindex
-- command, not of a migration: it is unbounded work with no natural batch
-- boundary. `moovctl reindex-threads` (internal/store.ReindexThreads) does the
-- transitive convergence incrementally, in bounded batches, ONLINE — and it is
-- idempotent, so it is also the recovery path if this backfill is skipped on a
-- large install. See internal/store/threads.go.
--
-- STATISTICS and the S3 settings are NOT touched by this migration.

-- +goose Up
-- +goose StatementBegin

-- ---------------------------------------------------------------------------
-- thread_id — the thread's identity, which is the messages.id of its oldest
-- member.
--
-- NO separate `threads` table, and that is a considered decision rather than an
-- omission. A thread has exactly two properties in RFC 8621 §3 — an id and the
-- ordered list of its Emails — and BOTH are derivable from this column with an
-- index scan. A threads table would hold a surrogate key and nothing else: no
-- thread-level state exists in this system (no per-thread flags, no per-thread
-- labels; A6 puts labels on messages), so the table would have exactly one
-- non-key column's worth of information, duplicated from MIN(id).
--
-- What a threads table WOULD buy is a place to hang a thread's own state if one
-- ever appears — a snooze, a mute, a per-thread label. When that day comes the
-- table is added and thread_id becomes a foreign key to it; nothing in this
-- schema forecloses that, because thread_id is already a stable identity.
-- Adding it NOW would mean maintaining a second row per thread on the insert
-- path, plus deleting it on merge, for no reader. It is not built.
--
-- Nullable ONLY during this migration: it is set to NOT NULL at the end, so the
-- invariant "every message has a thread" is enforced by the database rather
-- than by the care of every writer.
-- ---------------------------------------------------------------------------
ALTER TABLE messages ADD COLUMN thread_id bigint;

COMMENT ON COLUMN messages.thread_id IS
    'Thread identity: the messages.id of the thread''s OLDEST member. A merge '
    'always keeps the smaller id, so thread identity is monotone and converges '
    '(RFC 8621 §3, ADR-001 §2).';

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- Backfill, step 1: every message is its own thread.
--
-- This is the JWZ base case (a message with no stored ancestor is a root) and
-- it also guarantees the column is total before the NOT NULL below, whatever
-- the joins that follow do or fail to match.
-- ---------------------------------------------------------------------------
UPDATE messages SET thread_id = id WHERE thread_id IS NULL;

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- Backfill, step 2: join replies to the OLDEST stored ancestor named anywhere
-- in their References / In-Reply-To chain.
--
-- RFC 5322 §3.6.4 orders References oldest-first, but a chain can be truncated
-- or reordered by intermediate mailers, so this takes the minimum stored id
-- among ALL named ancestors rather than trusting position. The minimum is what
-- makes the result independent of chain order, which is the same property the
-- runtime insert path relies on.
--
-- Only ancestors that are OLDER (a smaller id) are accepted, so this can never
-- point a thread at a younger message and the winner rule holds for backfilled
-- data exactly as it does for new arrivals.
-- ---------------------------------------------------------------------------
WITH refs AS (
    SELECT m.id AS child_id,
           unnest(
               CASE WHEN m.in_reply_to IS NULL OR m.in_reply_to = ''
                    THEN m.references_ids
                    ELSE m.references_ids || ARRAY[m.in_reply_to]
               END
           ) AS ref,
           m.account_id
      FROM messages m
), parents AS (
    SELECT r.child_id, min(p.id) AS parent_id
      FROM refs r
      JOIN messages p
        ON p.account_id = r.account_id
       AND p.message_id = r.ref
     WHERE p.id < r.child_id
     GROUP BY r.child_id
)
UPDATE messages m
   SET thread_id = p.parent_id
  FROM parents p
 WHERE m.id = p.child_id;

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- Backfill, step 3: flatten one level of indirection.
--
-- Step 2 points each reply at its oldest stored ancestor, but that ancestor may
-- itself have been pointed elsewhere — A <- B <- C where C names only B leaves
-- C on B while B moved to A. Repeating the assignment until it converges is a
-- fixpoint loop, and a fixpoint loop is exactly the unbounded work a migration
-- must not contain.
--
-- Three passes are run instead, which resolves every chain up to 8 hops deep
-- (each pass squares the reachable depth: 2 -> 4 -> 8). Deeper chains that
-- References did not already flatten are left partially split and are converged
-- by `moovctl reindex-threads`, which is idempotent and online. That is the
-- honest trade: a bounded migration plus a documented online completion, rather
-- than an unbounded migration that might not finish.
--
-- In practice References carries the FULL ancestry (RFC 5322 §3.6.4), so step 2
-- already lands almost every message directly on its true root and these passes
-- change very few rows.
-- ---------------------------------------------------------------------------
UPDATE messages m
   SET thread_id = t.thread_id
  FROM messages t
 WHERE m.thread_id = t.id
   AND t.thread_id <> t.id
   AND t.thread_id < m.thread_id;

UPDATE messages m
   SET thread_id = t.thread_id
  FROM messages t
 WHERE m.thread_id = t.id
   AND t.thread_id <> t.id
   AND t.thread_id < m.thread_id;

UPDATE messages m
   SET thread_id = t.thread_id
  FROM messages t
 WHERE m.thread_id = t.id
   AND t.thread_id <> t.id
   AND t.thread_id < m.thread_id;

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- The column is now total. Enforce it.
--
-- SET NOT NULL requires a full table scan to verify; it is done here, once,
-- while the table is already hot from the backfill.
-- ---------------------------------------------------------------------------
ALTER TABLE messages ALTER COLUMN thread_id SET NOT NULL;

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- Indexes.
--
-- (thread_id, account_id, date) serves BOTH thread reads:
--   * Thread/get — "every message of thread T in account A" — is a range scan
--     of one contiguous key prefix;
--   * the exact mailbox counts, COUNT(DISTINCT thread_id), which joins
--     message_state to messages and reads only this index for the messages side.
--
-- `date` is the third key column so Thread/get's mandated ordering (RFC 8621
-- §3: "sorted by the receivedAt date of the Email, oldest first") comes off the
-- index in order rather than through a sort node. It costs 8 bytes per row and
-- removes the only sort on the thread read path.
--
-- ===========================================================================
-- WHY thread_id LEADS, AND NOT account_id — A REGRESSION THIS ALREADY CAUSED
-- ===========================================================================
--
-- Every other index in this schema leads with account_id, and this one
-- deliberately does not. The first draft did — (account_id, thread_id, date) —
-- and it BROKE SEARCH. The store's own canary caught it
-- (TestCompositeGINIndexIsUsableForSearch, S3 §5.2):
--
--     Limit
--       ->  Sort  Sort Key: date DESC
--             ->  Index Scan using messages_acct_thread on messages m
--                   Index Cond: (account_id = '83'::bigint)
--                   Filter: (tsv @@ '''zanzibarita'''::tsquery)
--
-- An index LEADING with account_id is a general-purpose "every message of this
-- account" index, so the planner can use it for ANY account-scoped query and
-- then filter — including full-text search, where "filter" means evaluating the
-- tsv predicate against every message the account owns. That is exactly the
-- failure mode S3 §5.2 measured at up to 6,600x (10,636 ms -> 1.6 ms), and
-- exactly what the composite gin(account_id, tsv) exists to prevent. Adding a
-- fourth account-leading btree handed the planner a fresh way to make that same
-- mistake.
--
-- Putting thread_id first does not fully remove the temptation — PostgreSQL can
-- still scan the WHOLE index and use account_id as a non-leading Index Cond,
-- which the same canary then caught a second time at a higher cost (571 vs 5.9)
-- — so the index is ALSO made partial. `WHERE thread_id IS NOT NULL` is true for
-- every row (the column is NOT NULL), so the index contains exactly the same
-- entries it otherwise would; what changes is the planner's bookkeeping. A
-- partial index is only considered for a query whose predicate PostgreSQL can
-- prove implies the index predicate, and a full-text search that mentions
-- neither thread_id nor a NOT NULL test on it cannot. The thread reads all
-- mention thread_id explicitly, so they still match.
--
-- The net effect: this index is reachable ONLY through a query that names a
-- thread, which is precisely the set of queries it was built for.
--
-- The rule this encodes, for whoever adds the next index: an index the planner
-- can reach from an account_id predicate alone competes with the composite GIN
-- on every search query. Lead with the selective column, and constrain the
-- index so an account-only query cannot reach it.
-- ---------------------------------------------------------------------------
CREATE INDEX messages_acct_thread ON messages (thread_id, account_id, date)
    WHERE thread_id IS NOT NULL;

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- The subject-fallback index (JWZ step 5, RFC 8621 §3's second condition:
-- "After stripping automatically added prefixes such as 'Fwd:', 'Re:',
-- '[List-Tag]', etc., and ignoring white space, the subjects are the same").
--
-- The normalized subject is computed in GO, not in SQL, and stored in
-- `thread_subject_keys`. That split is deliberate:
--
--   * the normalization rules are a moving target (list tags, locale-specific
--     reply prefixes — "Re:", "RE:", "Antw:", "R:", "Sv:", "Vs:") and belong in
--     testable code with a unit-test table, not in a PL/pgSQL function that
--     only a migration can change;
--   * a GENERATED column would make every rule change a full table rewrite,
--     which migration 0003 documents as a ~40-minute operation at S3 scale;
--   * a plain lookup table lets the runtime insert path resolve "is there an
--     existing thread with this subject" in ONE indexed read, which is what
--     keeps the insert path O(1)-ish per message.
--
-- The row is (account_id, subject_key) -> thread_id, holding the OLDEST thread
-- that has ever carried that normalized subject. It is a HINT, not an
-- authority: threading correctness comes from the Message-ID graph, and this
-- table only supplies the join when the graph is silent.
--
-- WHY THE SUBJECT FALLBACK IS DELIBERATELY NARROW (see threads.go): joining on
-- subject alone would put every "Re: hello" in a mailbox into one thread, which
-- is worse than not threading. internal/store/threads.go therefore applies it
-- only to a message that HAS a reply marker (its subject carried a stripped
-- "Re:"/"Fwd:" prefix, or it has an In-Reply-To/References header that named
-- nothing stored) and never to a bare original subject, and requires a
-- non-trivial subject. The rule is stated once, in Go, with tests.
-- ---------------------------------------------------------------------------
CREATE TABLE thread_subject_keys (
    account_id  bigint      NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    -- The normalized subject (prefixes stripped, whitespace collapsed, folded).
    -- Bounded: a subject long enough to overflow a btree index entry cannot be
    -- a useful thread key, and the truncation happens in Go where it is tested.
    subject_key text        NOT NULL,
    -- The oldest thread carrying this subject. Moves DOWN only, never up, by
    -- the same monotonicity rule as thread_id itself.
    thread_id   bigint      NOT NULL,
    -- The newest message seen under this key. The subject fallback is refused
    -- when the gap is too large: a "Re: invoice" six months after the last one
    -- is a new conversation, not a reply (threadSubjectWindow in threads.go).
    last_seen   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (account_id, subject_key)
);

COMMENT ON TABLE thread_subject_keys IS
    'JWZ step 5 / RFC 8621 §3 condition 2: normalized-subject -> oldest thread. '
    'A hint for messages the Message-ID graph cannot place, never an authority.';

-- +goose StatementEnd

-- +goose StatementBegin
-- ---------------------------------------------------------------------------
-- Backfill of the subject-key table.
--
-- The migration CANNOT populate this correctly: the normalization is Go code
-- (see above), and a SQL approximation of it would seed the table with keys the
-- runtime would never produce — worse than an empty table, because a wrong key
-- silently mis-joins threads forever.
--
-- The table is therefore left EMPTY, and that is the correct state: an empty
-- hint table makes the subject fallback a no-op, so threading falls back to the
-- Message-ID graph — exactly what migration steps 2 and 3 above computed.
-- `moovctl reindex-threads` (store.ReindexThreads) populates it with the real
-- normalization.
--
-- This comment exists so a reader does not wonder whether the backfill was
-- forgotten. There is deliberately no statement here.
-- ---------------------------------------------------------------------------

-- ---------------------------------------------------------------------------
-- thread_id's initial value: the row's own id.
--
-- The column is NOT NULL and its correct initial value is the identity the
-- INSERT is generating. That cannot be expressed as a DEFAULT — a DEFAULT is
-- evaluated before the identity exists — and it cannot be expressed as a CTE
-- either: every statement inside a WITH clause sees the same snapshot, so an
-- `INSERT … RETURNING id` feeding an `UPDATE … FROM` matches ZERO rows. That
-- was tried first and it silently inserted nothing.
--
-- A BEFORE INSERT trigger is the one form that works, and it has the property
-- that matters more than elegance: it cannot be forgotten by a writer. The
-- batched path (InsertMessages), the COPY path (CopyMessages) and anything
-- added later all get the invariant without restating it, so "every message is
-- its own thread until it is grouped" is guaranteed by the database rather than
-- by the discipline of every caller.
--
-- pg_get_serial_sequence resolves the identity sequence by name, so the trigger
-- does not hard-code a sequence name that a table rewrite could change.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION moov_thread_id_default()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    -- The id is already assigned at BEFORE INSERT time for a GENERATED ALWAYS
    -- AS IDENTITY column, so NEW.id is the real value the row will carry.
    IF NEW.thread_id IS NULL OR NEW.thread_id = 0 THEN
        NEW.thread_id := NEW.id;
    END IF;
    RETURN NEW;
END;
$$;

-- +goose StatementEnd

-- +goose StatementBegin

CREATE TRIGGER messages_thread_id_default
    BEFORE INSERT ON messages
    FOR EACH ROW
    EXECUTE FUNCTION moov_thread_id_default();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TRIGGER IF EXISTS messages_thread_id_default ON messages;
DROP FUNCTION IF EXISTS moov_thread_id_default();
DROP TABLE IF EXISTS thread_subject_keys;
DROP INDEX IF EXISTS messages_acct_thread;
ALTER TABLE messages DROP COLUMN IF EXISTS thread_id;

-- +goose StatementEnd
