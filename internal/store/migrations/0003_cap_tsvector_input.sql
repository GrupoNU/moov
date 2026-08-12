-- Moov Mail — migration 0003: bound the tsvector input at the source.
--
-- # The production failure this fixes
--
-- Pilot account 2 (24 mailboxes), 2026-08-12:
--
--   ERROR: string is too long for tsvector (2062784 bytes, max 1048575 bytes)
--   (SQLSTATE 54000)
--   inserting 100 messages in "INBOX": inserting message 28 of 100
--
-- One real message carried 2 MB of extracted body text. PostgreSQL's tsvector
-- has a hard limit of 1 MiB (MAXSTRLEN = 2^20-1 bytes of lexeme data), and
-- migration 0002 fed the generated `tsv` column an UNBOUNDED `body_text`. The
-- row was therefore unstorable, and because a backfill batch is 100 messages in
-- one transaction, the poison message failed the other 99 with it. The
-- supervisor treats every error as transient, so it retried the same doomed
-- batch every 5 minutes indefinitely: one message blocking a whole folder's
-- backfill, which is exactly the failure class rule R4 (L2 §2.4) exists to
-- prevent.
--
-- This migration is the first of the two layers of the fix. It removes the
-- possibility of the error. The second layer — a batch that degrades to
-- per-message insertion rather than failing as a unit — lives in
-- internal/sync/pipeline.go and protects against any FUTURE data-dependent
-- insert error, of which this was only the first.
--
-- ===========================================================================
-- THE ARITHMETIC (measured, PostgreSQL 17.4, 2026-08-12)
-- ===========================================================================
--
-- The 1 MiB limit applies to the RESULTING tsvector — lexemes plus position
-- lists — not to the input text. So the cap cannot be read off the limit
-- directly; it needs the worst-case expansion ratio, which is a property of the
-- text's token shape rather than its size:
--
--   * REPEATED tokens are nearly free. One lexeme carries a position list, and
--     positions are cheap: 40,000 repetitions of one 4-character word produce a
--     528-byte tsvector — 0.003 bytes per input byte.
--   * UNIQUE tokens are the expensive case: every one is a new lexeme with its
--     own per-entry overhead.
--   * SHORT unique tokens are the worst case of all, because the fixed
--     per-lexeme overhead is amortized over fewer content bytes.
--
-- Measured expansion, 256 KiB of GUARANTEED-DISTINCT tokens, by token width
-- (the distinctness matters: a generator whose token space wraps stops being
-- adversarial and understates the ratio by ~20%, which is a trap this
-- measurement fell into once before being corrected):
--
--     token width | tsvector bytes | ratio
--     ------------+----------------+-------
--               1 |        741,730 | 2.829   <-- PEAK
--               2 |        741,366 | 2.828
--               3 |        728,168 | 2.778
--               4 |        616,196 | 2.351
--               6 |        508,752 | 1.941
--               8 |        447,906 | 1.709
--              12 |        379,980 | 1.450
--              16 |        341,588 | 1.303
--
-- THE WORST CASE IS ~2.83 BYTES OF TSVECTOR PER INPUT BYTE, and with the
-- weighted-band concatenation it reaches ~3.0. Note it exceeds 1.0 by a wide
-- margin: a tsvector can be nearly three times the size of the text it indexes,
-- which is why the obvious "cap the body at 1 MiB" would NOT have been safe —
-- it would have failed on exactly this input shape.
--
-- Three-band worst case (subject + addresses + body all filled with the peak
-- token shape simultaneously), by candidate body cap:
--
--     body cap | resulting tsvector | % of 1,048,575 | safety margin
--     ---------+--------------------+----------------+---------------
--      128 KiB |            408,974 |          39.0% | 2.56x
--      160 KiB |            507,278 |          48.4% | 2.07x
--      192 KiB |            598,792 |          57.1% | 1.75x   <-- CHOSEN
--      224 KiB |            677,438 |          64.6% | 1.55x
--      256 KiB |            756,076 |          72.1% | 1.39x
--      320 KiB |            910,312 |          86.8% | 1.15x   (too thin)
--      384 KiB |          1,053,260 |         100.4% | OVER THE LIMIT
--
-- CHOSEN CAPS, and why each is the number it is:
--
--   subject    2 KiB  (2,048 bytes)   RFC 5322 recommends 78 characters per
--                                     header line and 998 as the hard limit; a
--                                     2 KiB subject is already pathological, so
--                                     the cap costs nothing real.
--   addresses  8 KiB  (8,192 bytes)   Applied PER FIELD (from, to, cc), so the
--                                     B band is bounded at 3x8 KiB + 2 separator
--                                     bytes. A large mailing list distribution
--                                     can be genuinely long; 8 KiB holds roughly
--                                     200 addresses, past which searching by
--                                     recipient has no meaning anyway.
--   body     192 KiB  (196,608 bytes) The number the arithmetic decides. At the
--                                     worst-case ratio this yields 599 KB —
--                                     57.1% of the limit, a 1.75x safety factor
--                                     against an input specifically constructed
--                                     to be as bad as possible.
--
-- Why 192 KiB and not more: 384 KiB EXCEEDS the limit outright on the peak
-- shape, and 320 KiB leaves only 1.15x — close enough that a token distribution
-- slightly worse than any measured here, or a future PostgreSQL that widens the
-- per-lexeme overhead, would put us back in production incident. 1.75x is the
-- margin that makes this fix durable rather than merely sufficient for the one
-- message that caused it. Why not less: S3 §3.1 measured mean `body_text` at
-- 1,086 bytes, so 192 KiB is ~180x the average message; the pilot's entire
-- corpus contains exactly one message anywhere near it.
--
-- WHAT IS AND IS NOT TRUNCATED: only the SEARCH VECTOR is bounded. The
-- `body_text` column still stores the message's full extracted text, and the
-- raw blob is untouched. A reader of a 2 MB message sees all of it; what the
-- reader loses is the ability to full-text search past the first 320 KiB of it.
-- That is the right trade — the alternative on offer was not storing the
-- message at all.
--
-- ===========================================================================
-- OPERATIONAL NOTE FOR LARGE INSTALLATIONS
-- ===========================================================================
--
-- ALTERing a STORED generated column REWRITES THE ENTIRE TABLE and rebuilds
-- every index on it, under an ACCESS EXCLUSIVE lock. At pilot scale (tens of
-- thousands of messages) this is seconds and is fine to run on start. At the
-- 5M-message scale S3 benchmarked it is NOT: budget the ~40 minutes of GIN
-- rebuild S3 §5.2 measured, plus the table rewrite, and run it in a window
-- where search being unavailable is acceptable. There is no online variant —
-- PostgreSQL has no ALTER … SET EXPRESSION that avoids the rewrite.
--
-- Dropping and re-adding the column would be no cheaper and would additionally
-- lose the STATISTICS 4000 setting (S3 §5.3), so the ALTER is both the simpler
-- and the safer form. It is written below as DROP EXPRESSION + SET EXPRESSION
-- in one statement list precisely so the column, its type, its position and its
-- statistics target all survive.

-- +goose Up
-- +goose StatementBegin

-- One ALTER TABLE, so the table is rewritten ONCE rather than once per clause.
ALTER TABLE messages
    ALTER COLUMN tsv SET EXPRESSION AS (
        setweight(to_tsvector('simple', immutable_unaccent(left(coalesce(subject, ''), 2048))), 'A') ||
        setweight(to_tsvector('simple', immutable_unaccent(
            left(coalesce(from_addr, ''), 8192) || ' ' ||
            left(coalesce(to_addrs, ''), 8192) || ' ' ||
            left(coalesce(cc_addrs, ''), 8192)
        )), 'B') ||
        setweight(to_tsvector('simple', immutable_unaccent(left(coalesce(body_text, ''), 196608))), 'C')
    );

-- +goose StatementEnd

-- +goose StatementBegin
-- The rewrite does not carry the statistics target forward on every PostgreSQL
-- version, and it costs nothing to restate. WHY 4000 is migration 0002's
-- comment and S3 §5.3: at the default of 100 the planner misestimates tsvector
-- selectivity by ~500x and filters a million rows (13,085 ms for a 1.6 ms
-- query).
ALTER TABLE messages ALTER COLUMN tsv SET STATISTICS 4000;

-- +goose StatementEnd

-- +goose StatementBegin
-- The rewrite invalidates the planner's statistics for the whole table. Without
-- this, the first searches after the migration run against stale numbers — the
-- exact condition S3 §5.3 measured at 13 seconds.
ANALYZE messages;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Restores migration 0002's unbounded expression VERBATIM. Note that rolling
-- back re-introduces the production bug: any message whose body exceeds the
-- tsvector limit becomes unstorable again, and the batch containing it fails.
-- The rollback exists for completeness of the migration pair, not because it is
-- ever a good idea.
ALTER TABLE messages
    ALTER COLUMN tsv SET EXPRESSION AS (
        setweight(to_tsvector('simple', immutable_unaccent(coalesce(subject, ''))), 'A') ||
        setweight(to_tsvector('simple', immutable_unaccent(
            coalesce(from_addr, '') || ' ' || coalesce(to_addrs, '') || ' ' || coalesce(cc_addrs, '')
        )), 'B') ||
        setweight(to_tsvector('simple', immutable_unaccent(coalesce(body_text, ''))), 'C')
    );

ALTER TABLE messages ALTER COLUMN tsv SET STATISTICS 4000;

-- +goose StatementEnd
