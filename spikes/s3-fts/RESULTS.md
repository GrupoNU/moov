# Spike S3 — Results: PostgreSQL `tsvector`+GIN at 5M messages

**Date:** 2026-08-08/09 · **Question:** does PostgreSQL FTS hold the Gmail-class search bar
(**p95 ≤ 100 ms warm-cache**) at 5M messages, or must Meilisearch enter the MVP?

**Answer:** **Yes — with two non-obvious configuration requirements and one product
concession.** Eight of ten query shapes land between 1.5 ms and 24 ms p95. The two that fail
(relevance ranking, exact result count) fail for a reason no index can fix, and both have
acceptable product-level answers. See the [verdict](#verdict).

---

## 1. Environment

| | |
|---|---|
| Host | Contabo VPS `mail` (production Grupo NU mail server; Mailcow + Postal running alongside) |
| CPU | 8 vCPU (`nproc`) |
| RAM | 23 GB total, ~19 GB free at start |
| Disk | SSD — `lsblk -d -o name,rota` → `sda ROTA=0`. 774 GB volume, 140 GB free at start |
| Docker | 29.4.1 |
| PostgreSQL | **17.10** (Debian 17.10-1.pgdg12+1), image `postgres:17-bookworm` |
| Container limits | `--cpus=6 --memory=12g --shm-size=2g`, **no published ports** |
| Data dir | bind mount `/root/moov-s3/pgdata` |

PostgreSQL configuration (verbatim, from `postgresql.conf` in this directory):

```
shared_buffers = 4GB          effective_cache_size = 12GB
maintenance_work_mem = 2GB    work_mem = 64MB
max_parallel_workers_per_gather = 4
max_wal_size = 8GB            checkpoint_completion_target = 0.9
random_page_cost = 1.1        effective_io_concurrency = 200
shared_preload_libraries = 'pg_stat_statements'   track_io_timing = on
```

**Caveat on the environment:** this is a live production mail server. Baseline load was
~0.15 and Mailcow/Postal traffic is light, but the numbers below are not from an isolated
machine. Treat them as a realistic floor rather than a laboratory best case.

## 2. Corpus

5,000,000 messages, deterministic (`-seed 20260808`). Generator: `gen/` (Go).

| Property | Value |
|---|---|
| Accounts | 89 (mirrors the real Crash stress case) |
| Account 1 | 1,000,000 messages (Gmail-class power user) |
| Account 2 / 3 | 500,000 / 250,000 |
| Accounts 4-89 | remaining 3.25M, log-normal; **median account = 26,640 msgs** (account 29) |
| Mailboxes | 10-15 per account, INBOX ~55% / Sent ~18% |
| Dates | 10-year span, denser toward present |
| Language | ~60% Spanish / 40% English, Zipfian (s≈1.05) |
| Bodies | log-normal, median ≈120 words, p99 ≈2000 |
| Contacts | 50,000-address pool, Zipfian (s=0.9) |
| Flags | ~30% unread, ~8% flagged, ~4% answered |

Both accounts under test are reported in every table: **account 1 (1M msgs)** and
**account 29 (26,640 msgs — the median)**.

### 2.1 Correctness gate — planted needles

Run **before** any timing (`bench -mode needles`). Latency on wrong results is worthless.

| Check | Expected | Actual | Result |
|---|---:|---:|---|
| rare token `zanzibarita` (corpus-wide) | 37 | 37 | PASS |
| unique invoice `INV-2024-0857` (corpus-wide) | 1 | 1 | PASS |
| phrase `quetzal ferroviario nocturno` (corpus-wide) | 5 | 5 | PASS |
| `zanzibarita` in account 1 | 10 | 10 | PASS |
| `zanzibarita` in account 2 | 8 | 8 | PASS |
| total corpus size | 5,000,000 | 5,000,000 | PASS |

**All needle checks PASS.** Exact-count, prefix, and phrase semantics all behave correctly
through the `'simple'` + `unaccent` configuration.

## 3. Bulk load and index build

Bulk-load-then-index, which is the sync engine's initial-sync strategy.

| Step | Time | Notes |
|---|---:|---|
| `COPY` 5,000,000 rows (with generated `tsv`) | **2,423 s (40m 23s)** | **2,063 rows/s.** CPU-bound on `to_tsvector`, not I/O |
| `CREATE INDEX … USING gin (tsv)` | **2,227 s (37m 07s)** | single-threaded; `maintenance_work_mem=2GB` |
| `CREATE INDEX (account_id, date DESC)` | 24.8 s | |
| `CREATE INDEX (account_id, mailbox_id, date DESC)` | 6.2 s | |
| `CREATE INDEX … USING gin (account_id, tsv)` (btree_gin) | **~40 min** | the index that actually matters — see §5.2 |
| `VACUUM (ANALYZE)` at default stats target | 88 s | |
| `ANALYZE` at `STATISTICS 4000` on `tsv` | **429 s (7m 09s)** | required — see §5.3 |

**Initial sync of a 1M-message mailbox is dominated by tsvector generation, not IMAP.**
At 2,063 rows/s a full 5M-message estate takes ~40 min of pure COPY plus ~80 min of index
build. This is a one-time cost per deployment, but it must be planned for (and it argues
for building the GIN indexes *after* the first sync completes, not before).

### 3.1 Sizes

| Object | Size |
|---|---:|
| heap only | 5,379 MB |
| TOAST (bodies + tsv) | 12 GB |
| table total (heap+toast, no indexes) | 18 GB |
| GIN on `tsv` | 2,507 MB |
| **GIN on `(account_id, tsv)`** | **2,545 MB** |
| btree `(account_id, date)` | 150 MB |
| btree `(account_id, mailbox_id, date)` | 150 MB |
| **TOTAL with all indexes** | **23 GB** |

Sampled column sizes (`TABLESAMPLE SYSTEM (0.2)`):

- mean `pg_column_size(tsv)` = **2,192 bytes** → **~11.0 GB over 5M rows**
- mean `pg_column_size(body_text)` = **1,086 bytes** → ~5.4 GB over 5M rows

**The `tsv` column is twice the size of the compressed body text it derives from.** It, not
the message bodies, is what fills TOAST. Budget **~4.6 KB/message all-in** (23 GB / 5M) for
the search store. Adding the composite GIN costs another 2.5 GB — worth it (§5.2), but note
that keeping *both* GIN indexes, as this benchmark did, is wasteful; production should keep
only the composite one.

## 4. Query latency

`bench -mode warm -runs 50 -warmups 5`. Wall-clock from the Go driver (pgx), including row
drain — what the API layer actually pays, not just `Execution Time`. Percentiles are
nearest-rank. **Configuration: composite GIN + `plan_cache_mode=force_custom_plan` +
`STATISTICS 4000`** (all three are required; §5 explains why).

### 4.1 Warm cache — the headline table

| # | Shape | Acct 1 (1M) p50 / **p95** / p99 | Acct 29 (26.6k) p50 / **p95** / p99 | Bar |
|---:|---|---:|---:|:--:|
| 1 | common word + date DESC | 5.5 / **9.3** / 9.8 | 5.7 / **9.0** / 21.4 | PASS |
| 2 | rare word + date DESC | 1.5 / **3.3** / 6.9 | 1.5 / **3.7** / 4.8 | PASS |
| 3 | two-word AND + date DESC | 12.9 / **16.4** / 30.9 | 6.1 / **9.7** / 23.8 | PASS |
| 4 | phrase + date DESC | 1.3 / **3.1** / 5.6 | 1.3 / **4.0** / 6.8 | PASS |
| 5 | prefix `word:*` (search-as-you-type) | 2.8 / **5.0** / 5.5 | 2.1 / **4.8** / 5.0 | PASS |
| 6 | common word + mailbox + last 90d | 4.9 / **6.8** / 7.8 | 5.6 / **7.2** / 19.9 | PASS |
| 7 | common word + unread only | 18.2 / **23.6** / 37.0 | 11.2 / **14.9** / 27.1 | PASS |
| 8 | from-address (weight B) | 10.5 / **14.7** / 30.4 | 10.7 / **14.2** / 18.2 | PASS |
| 9 | two-word AND + `ts_rank_cd` | 778.8 / **892.0** / 1199.8 | 22.3 / **27.6** / 29.7 | **FAIL (1M)** |
| 10 | exact `count(*)` | 383.3 / **451.9** / 484.8 | 10.2 / **14.1** / 22.8 | **FAIL (1M)** |

**Shapes 1-8 pass on both account sizes with 4x-30x headroom.** The shape the brief
predicted would be the killer — #1, common word + `ORDER BY date DESC LIMIT 50` — is the
*fastest* interactive shape at 9.3 ms p95, because the planner walks
`(account_id, date DESC)` and stops after 50 matches (`Rows Removed by Filter: 328`).

The two failures are #9 and #10, and only on the 1M-message account. §6 explains why they
are fundamental and what to do about them.

### 4.2 Cold cache

`bench -mode cold`, n=10 per cell, after `docker restart` **and** `echo 3 > drop_caches`
(both PostgreSQL's buffers and the OS page cache were emptied). With n=10 the p95 column is
literally the single slowest run, i.e. the first-touch I/O.

| # | Shape | Acct 1 p50 / p95(=max) | Acct 29 p50 / p95(=max) |
|---:|---|---:|---:|
| 1 | common word + date DESC | 4.9 / 396.1 | 5.2 / 774.8 |
| 2 | rare word + date DESC | 1.3 / 16.3 | 1.3 / 2.6 |
| 3 | two-word AND | 11.2 / 651.7 | 6.5 / 125.9 |
| 4 | phrase | 1.4 / 26.1 | 2.1 / 5.5 |
| 5 | prefix | 3.4 / 13.7 | 2.1 / 4.1 |
| 6 | common + mailbox + 90d | 5.0 / 21.3 | 3.9 / 113.0 |
| 7 | common + unread | 14.0 / 323.6 | 7.4 / 113.9 |
| 8 | from-address | 6.4 / 10.5 | 9.7 / 290.5 |
| 9 | `ts_rank_cd` | 678.7 / 32503.7 | 20.4 / 819.8 |
| 10 | exact count | 322.1 / 502.9 | 7.8 / 9.3 |

**Only the first query after a restart pays.** p50 is already at warm levels by run 2-3 —
the working set that matters (index upper levels + hot heap pages) faults in almost
immediately. Worst first-touch for an interactive shape is 775 ms. This is acceptable for a
cold start but argues for a warm-up query on service start.

### 4.3 Concurrency

`bench -mode concurrency -clients 8 -duration 60s`, 8 clients on **distinct accounts**,
round-robining the shape mix.

| Mix | Throughput | p50 | **p95** | p99 | max |
|---|---:|---:|---:|---:|---:|
| All 10 shapes | 304 qps | 7.3 ms | **82.2 ms** | 234.7 ms | **68,192 ms** |
| Interactive only (excl. #9, #10) | **607 qps** | 6.4 ms | **44.4 ms** | 106.8 ms | 678 ms |

Both mixes hold p95 under the bar, but the contrast is the finding: **removing the two
unbounded shapes doubles throughput and cuts the worst case from 68 seconds to 0.7 seconds.**
Under concurrency, #9/#10 do not merely miss their own budget — they monopolise shared
buffers and CPU and drag everything else's tail with them. A single power user hitting
"sort by relevance" can degrade search for every other user on the instance.

Single-client p95 for the interactive shapes was 3-24 ms; under 8-way load it rises to
44.4 ms aggregate. That is a ~2-4x degradation, still comfortably inside budget.

### 4.4 Incremental insert cost

The sync engine writes continuously, so insert cost against a *live* GIN index matters as
much as query cost. Batches of 100 rows, 30 batches, with a search issued immediately after
each batch to catch pending-list flush spikes.

| `fastupdate` | Insert p50 / p95 / max (100 rows) | Post-batch search p50 / p95 / max |
|---|---:|---:|
| **on** (default) | **25.2 / 47.9 / 77.8 ms** | 2.4 / 6.2 / 6.4 ms |
| off | 48.4 / 68.0 / 121.8 ms | 2.2 / 6.1 / 14.2 ms |

- **`fastupdate=on` (the default) is the right choice.** It halves insert cost (0.25 ms vs
  0.48 ms per message) and — contrary to the concern that motivated the test — produced
  **no search-latency spikes**. Post-batch search stayed at 2-6 ms in both modes.
- The feared pending-list flush penalty did not materialise at this write rate. It would
  become visible at much higher sustained insert rates or with a larger
  `gin_pending_list_limit`; at mail-sync rates (a few messages per second per account) it is
  a non-issue.
- Sustained ingest headroom: ~4,000 messages/s per connection at 0.25 ms/message.

### 4.5 Flag-update cost (the sync engine's most frequent write)

Marking messages read/flagged is by far the most common write a mail sync engine performs,
and it is *not* free here: PostgreSQL has no partial row update, so changing one `int`
rewrites the whole row — **including the ~2.2 KB generated `tsv`** — and re-inserts it into
both GIN indexes.

| Operation | Time | Per message |
|---|---:|---:|
| single-row flag update (warm, by `id`) | 2.3-2.8 ms | ~2.5 ms |
| 100-row flag update, ids known in advance | 54-65 ms | **~0.58 ms** |
| search issued immediately after | 48 ms | — |

**~0.58 ms per flag change in batches — 2.3x the cost of an inserting a new message.** The
sync engine should batch flag updates (a 100-row batch is 23x cheaper per message than 100
single-row statements) and should expect flag churn, not new mail, to dominate its write
load on an established mailbox.

> **Measurement caveat worth recording:** a first attempt measured 1.5-41 s for the same
> 100-row update. That was the *test query's* fault, not the update's — `WHERE id IN
> (SELECT id … ORDER BY id LIMIT 100)` made the planner scan the primary key and discard
> 2,524,794 rows before finding 100 belonging to the account. The `UPDATE` itself was ~1 ms
> of that. It is a good illustration of §5.3's lesson: on this table, a plan that looks
> reasonable can be three orders of magnitude off, and only `EXPLAIN (ANALYZE)` reveals it.

## 5. Three configuration requirements (each worth 1-2 orders of magnitude)

These are the real deliverable of this spike. The naive schema from the brief — plain
`gin(tsv)`, default statistics, a normal database driver — **fails the bar on 8 of 10
shapes.** The same queries pass on 8 of 10 after three changes.

### 5.1 The generic-plan trap (up to 145x)

The first warm run measured shape #3 at **618 ms p50** while `EXPLAIN ANALYZE` of the same
query showed **12.9 ms**. The gap is prepared statements: pgx (like most drivers) prepares
statements, and after five executions PostgreSQL switches from a *custom* plan (re-planned
per call, parameter values visible) to a *generic* one (planned once, values invisible).

With the tsquery invisible, the planner cannot estimate its selectivity and picks a
`BitmapAnd` that materialises **every row of the account**:

```
Bitmap Heap Scan on messages (actual time=884.836..1847.408 rows=34814)
  ->  BitmapAnd (actual time=858.841..858.844)
        ->  Bitmap Index Scan on messages_tsv_gin   (actual time=254.995 rows=173195)
        ->  Bitmap Index Scan on messages_acct_mbox_date (actual time=590.675 rows=1000000)
Execution Time: 1867.979 ms
```

Setting `plan_cache_mode = force_custom_plan` on the connection restores the index scan:
**1,868 ms → 19.4 ms.**

> **For the sync engine:** set `plan_cache_mode = force_custom_plan` on every connection that
> serves search (a pgx `RuntimeParams` entry, or `ALTER ROLE … SET`). Re-planning costs
> ~0.3-6 ms per query, which the numbers above already include. Without it, search latency
> silently degrades by two orders of magnitude *after the fifth query of a session* — which
> is exactly the kind of bug that never shows up in development.

### 5.2 `account_id` must be inside the GIN index (up to 6,600x)

A plain `gin(tsv)` index knows nothing about accounts, so answering "this term, for this
user" requires building a bitmap of the **corpus-wide** matches first:

```
->  Bitmap Index Scan on messages_tsv_gin (actual time=194.829..194.830 rows=173195)
      Index Cond: (tsv @@ '''factura'' & ''vencimiento''')
```

194 ms of index scan to find 895 rows that belong to account 29. **The cost scales with the
size of the whole installation, not the size of the user's mailbox** — the one property a
multi-tenant mail store cannot afford.

`CREATE EXTENSION btree_gin; CREATE INDEX … USING gin (account_id, tsv)` fixes it:

```
->  Bitmap Index Scan on messages_acct_tsv_gin (actual time=2.660..2.661 rows=895)
      Index Cond: ((account_id = 29) AND (tsv @@ ...))
```

Measured effects at 1M messages: shape #3 on the median account **171 ms → 50 ms**; shape #2
(rare term) **10,636 ms → 1.6 ms**. Cost: 2,545 MB and ~40 min to build.

### 5.3 `STATISTICS 4000` on the `tsv` column

Even with the composite index available, the planner initially refused to use it for the
rare-term shape: it estimated **4,951** matching rows where only **10** existed, concluded
that walking `(account_id, date DESC)` would fill `LIMIT 50` quickly, and instead scanned
the entire account:

```
Limit (actual time=157.085..13084.841 rows=10)
  ->  Index Scan using messages_acct_date on messages
        Filter: (tsv @@ '''zanzibarita''')
        Rows Removed by Filter: 999990
Execution Time: 13084.975 ms
```

This is the LIMIT+date-sort pathology in its pure form: the plan is optimal *if* the
estimate is right, and catastrophic when it is not. Raising the statistics target on the
tsvector column (`ALTER TABLE messages ALTER COLUMN tsv SET STATISTICS 4000`) improved the
estimate to 105 rows — close enough that the planner chose the composite GIN unprompted:
**10,636 ms → 1.6 ms** with no query rewrite and no planner hints.

Cost: `ANALYZE` on this table rises from 88 s to 429 s. Autovacuum's analyze on a large
`messages` table will be correspondingly slower; budget for it.

## 6. The two shapes that still fail — and why no index will save them

Shapes #9 (`ts_rank_cd` relevance) and #10 (exact `count(*)`) fail on the 1M-message account
at 892 ms and 452 ms p95. They fail for the **same fundamental reason**, and it is not an
indexing problem:

> Every other shape is `ORDER BY date DESC LIMIT 50`, so the engine can stop after finding 50
> matches. Relevance ranking must score **every** match before it knows which 50 are best,
> and an exact count must visit **every** match to count it. For `factura vencimiento` in
> account 1 that is **34,814 rows**. No index removes that work; the query has to ask for
> less.

The unbounded plan, in full (`ts_rank_cd`, account 1, cold):

```
->  Bitmap Index Scan on messages_tsv_gin (actual time=204.454..204.455 rows=173195)
->  Bitmap Index Scan on messages_acct_date (actual time=512.534..512.535 rows=1000000)
Execution Time: 33660.310 ms
```

### 6.1 Mitigations measured

`bench -mode mitigation -runs 30`. Bound the candidate set by recency, then rank/count
within it — which is approximately what Gmail does (relevance over a recent window; "many"
instead of an exact total).

| # | Mitigation | Acct 1 p50 / **p95** | Acct 29 p50 / **p95** | Bar |
|---:|---|---:|---:|:--:|
| 101 | rank over top-**500**-by-date | 160.2 / **265.4** | 25.3 / **44.6** | FAIL (1M) |
| 102 | rank over top-**200**-by-date | 63.6 / **134.0** | 22.5 / **37.5** | marginal |
| 103 | capped count `LIMIT 1000` ("999+") | 130.8 / **190.7** | 8.5 / **24.2** | FAIL (1M) |
| 104 | capped count `LIMIT 200` ("199+") | 19.1 / **98.3** | 6.2 / **19.7** | **PASS** |

- **Exact count is solved** by capping: "199+" costs 98 ms p95 at 1M messages versus 452 ms
  for the true total — and Gmail itself shows "1-50 of many" rather than an exact figure.
- **Relevance ranking is bounded but not free.** Ranking the 200 most recent matches costs
  134 ms p95 at 1M messages — 6.7x better than unbounded, still 34% over budget. Ranking a
  90-day window instead of a fixed row count would do better (shape #6 shows a 90-day filter
  costs 6.8 ms), at the cost of never surfacing an old-but-relevant message.

## 7. EXPLAIN plans for the interesting cases

Full plans are in `logs/explain.md` (tuned) and `logs/explain-plaingin.md` (before the
composite index). The four that carry the argument are quoted inline above:

- §5.1 — generic plan `BitmapAnd` materialising 1M rows (1,868 ms)
- §5.2 — plain GIN scanning 173,195 corpus-wide matches for 895 account rows (194 ms)
- §5.3 — `Rows Removed by Filter: 999990` from the misestimate (13,085 ms)
- §6 — `ts_rank_cd` scoring all 34,814 matches (33,660 ms cold)

The winning plan for the interactive shapes, for reference (shape #2, account 1):

```
Limit  (cost=138.92..139.04 rows=50 width=55) (actual time=0.390..0.392 rows=10 loops=1)
  Buffers: shared hit=43
  ->  Sort  (actual time=0.389..0.390 rows=10 loops=1)
        Sort Key: date DESC
        Sort Method: quicksort  Memory: 26kB
        ->  Bitmap Heap Scan on messages  (actual time=0.367..0.382 rows=10 loops=1)
              Recheck Cond: ((account_id = 1) AND (tsv @@ '''zanzibarita'''::tsquery))
              Heap Blocks: exact=10
              ->  Bitmap Index Scan on messages_acct_tsv_gin (actual time=0.357..0.357 rows=10)
                    Index Cond: ((account_id = 1) AND (tsv @@ '''zanzibarita'''::tsquery))
```

Note the estimate is still 105 rows against 10 actual — the statistics target did not make
the estimate *accurate*, only close enough (from 4,951) that the composite GIN won on cost.
The plan reads **43 buffers** where the pre-tuning plan read **3,238,837**.

---

# Verdict

## Does `tsvector`+GIN meet p95 ≤ 100 ms warm-cache at 5M messages?

**Yes for the interactive search product; no for two auxiliary features, both of which have
acceptable product answers.**

**8 of 10 shapes pass with large margins** (3.1-23.6 ms p95, i.e. 4x-30x under budget) on
*both* a 1M-message power-user account and a median 26.6k account. This includes every shape
the user actually feels: keyword search, phrase search, search-as-you-type prefix, folder and
date filters, unread filters, and from-address search. Under 8-way concurrency the
interactive mix sustains **607 qps at 44.4 ms p95**.

**Meilisearch is NOT required for the MVP.** It stays a fase-2 option.

## Which shapes fail, by how much, and is it tunable?

| Shape | p95 @ 1M | Over budget | Tunable? |
|---|---:|---:|---|
| #9 relevance ranking (`ts_rank_cd`) | 892 ms | 8.9x | **Partly.** Bounded to 134 ms (1.3x over) by ranking the 200 most recent matches. Not reducible to <100 ms without narrowing further |
| #10 exact result count | 452 ms | 4.5x | **Yes.** A capped "199+" count is 98 ms — passes |

Both failures are **fundamental to the operation, not to PostgreSQL**: ranking and counting
have no `LIMIT` shortcut and must touch every match. Meilisearch would be faster at ranking
(it is built for it), but adopting a second datastore to fix one non-default sort order is a
poor trade against the operational cost — especially since the mitigation is a product
decision Gmail itself already makes.

## Required for the MVP (all three, non-negotiable)

1. **`CREATE EXTENSION btree_gin` and index `gin (account_id, tsv)`** — not `gin (tsv)`.
   Without `account_id` in the index, query cost scales with the whole installation instead
   of the user's mailbox (up to 6,600x on rare terms). Keep only the composite index; the
   plain one is redundant (2.5 GB saved).
2. **`plan_cache_mode = force_custom_plan`** on every search connection. Without it, latency
   degrades ~145x after the fifth query of a prepared-statement session.
3. **`ALTER TABLE messages ALTER COLUMN tsv SET STATISTICS 4000`** and ANALYZE. Without it
   the planner misestimates tsvector selectivity by ~500x and picks a plan that filters a
   million rows. Accept the ~7-minute ANALYZE.

## Product decisions this forces

- **Result counts must be capped** ("199+" / "many"), not exact. Costs 98 ms instead of 452 ms.
- **Relevance sort must be bounded** to a recent window (200 rows or ~90 days). At 134 ms p95
  it is the slowest thing the product will offer; consider making date-sort the default (it
  is 9 ms) and relevance an explicit opt-in.
- **Rank/count must be isolated from the interactive path.** Under load they take the worst
  case from 678 ms to 68 s and halve throughput. Give them a separate connection pool with a
  `statement_timeout`, so one user's relevance query cannot degrade everyone's search.

## Engineering notes for the sync engine

- **Initial sync is CPU-bound on `to_tsvector`, not IMAP**: 2,063 rows/s. A 1M-message
  mailbox needs ~8 min of COPY plus its share of a ~40 min GIN build. Build GIN indexes
  *after* the first bulk sync, never before.
- **Storage budget: ~4.6 KB/message all-in** (23 GB for 5M with all indexes). The `tsv`
  column alone is ~11 GB — **twice the size of the compressed bodies**. If storage becomes a
  constraint, dropping weight-C body indexing is the biggest single lever (at the cost of
  body search).
- **Incremental writes are cheap and safe**: 0.25 ms/message with the default
  `fastupdate=on`, with no observed search-latency spikes from pending-list flushes. Do not
  set `fastupdate=off` — it doubles insert cost for no measured benefit.
- **Warm up on service start.** Cold first-touch costs up to 775 ms for an interactive shape;
  p50 returns to warm levels by the second or third query.

## Open risks / what this spike did NOT test

- **`'simple'` config means no stemming.** `factura` ≠ `facturas`. Recall depends on the
  client issuing prefix queries. A Spanish/English dual-config or a custom dictionary was not
  evaluated and should be, since it affects perceived search quality more than latency does.
- **Single-node, single-instance.** No replication, no connection-pooler (PgBouncer in
  transaction mode interacts with `plan_cache_mode` and prepared statements — verify before
  adopting it).
- **Synthetic text.** Real mail has quoted reply chains, signatures, base64 fragments and
  HTML-to-text noise, all of which inflate `tsv` size and change token distributions. The
  11 GB tsv figure is likely an underestimate.
- **Concurrency tested at 8 clients on one box.** Not a load-test to saturation, and the host
  was simultaneously running production Mailcow and Postal.
- **No sustained churn / bloat measurement.** Flag-update *latency* was measured (§4.5), but
  not what thousands of them do to GIN index bloat and autovacuum load over days. Since every
  flag change rewrites a 2.2 KB tsv and re-inserts it into two GIN indexes, bloat is a real
  risk and deserves a soak test before production.
- **A `tsv`-less update path was not explored.** If flag churn proves expensive at scale, the
  standard fix is to split volatile columns (`flags`) into a side table so marking a message
  read never touches the tsvector. That restructuring is worth evaluating in the sync-engine
  design, and this spike's numbers (§4.5) are the baseline to beat.
