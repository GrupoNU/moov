-- Moov Mail — Spike S3: indexes, built AFTER the bulk COPY.
-- Each statement is timed individually by load.sh via \timing.

-- The plain GIN on tsv alone. MEASURED VERDICT: this is NOT sufficient.
-- Because account_id is not in the index, every search must build a bitmap of
-- the CORPUS-WIDE matches for the term and only then intersect with the
-- account — 194 ms of pure index scan for a term with 173k global hits, no
-- matter how few belong to the user. Kept here because it is what the obvious
-- schema produces, and RESULTS.md compares against it.
CREATE INDEX messages_tsv_gin ON messages USING gin (tsv);

CREATE INDEX messages_acct_date ON messages (account_id, date DESC);
CREATE INDEX messages_acct_mbox_date ON messages (account_id, mailbox_id, date DESC);

-- THE INDEX THAT MATTERS (requires btree_gin).
-- Putting account_id INSIDE the GIN index lets a single bitmap index scan
-- answer "this term, in this account", so the work is proportional to the
-- user's matches rather than the whole corpus. Measured effect on the rare-term
-- shape at 1M messages: 10,636 ms -> 1.6 ms.
CREATE EXTENSION IF NOT EXISTS btree_gin;
CREATE INDEX messages_acct_tsv_gin ON messages USING gin (account_id, tsv);

-- The default statistics target (100) badly misestimates tsvector selectivity:
-- the planner believed a term with 10 actual hits in the account had 4,951,
-- which made it prefer walking the date index and filtering 999,990 rows.
-- At 4000 the estimate lands close enough that it picks the composite GIN.
-- Cost: ANALYZE on this table takes ~7 min at target 4000 vs seconds at 100.
ALTER TABLE messages ALTER COLUMN tsv SET STATISTICS 4000;
