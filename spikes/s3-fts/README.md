# Spike S3 — PostgreSQL `tsvector`+GIN full-text search at 5M messages

Decides whether PostgreSQL FTS meets Moov's search bar (**p95 ≤ 100 ms warm-cache**,
Gmail/Fastmail-class) at 5M messages, or whether Meilisearch has to enter the MVP.

**Outcome: it does — Meilisearch stays out of the MVP.** 8 of 10 query shapes land at
3-24 ms p95 on a 1M-message account, but only after three non-obvious configuration changes,
each worth one to three orders of magnitude. Relevance ranking and exact result counts are
the two shapes that cannot be made to fit, for reasons no index solves.

**Read [`RESULTS.md`](RESULTS.md) for the measurements and the verdict.** The three required
settings are in `indexes.sql` (composite `gin(account_id, tsv)` + `STATISTICS 4000`) and in
the `-custom-plan` flag of the bench driver (`plan_cache_mode=force_custom_plan`).

Raw measurement output is in [`logs/`](logs/), including the before/after runs that isolate
each configuration change (`warm-genericplan.md` → `warm-plaingin.md` → `warm.md`).

## What this spike builds

| Path | What |
|---|---|
| `schema.sql` | The `messages` table, mirroring the ADR-001 sync-store shape, with a **generated** `tsvector` column (weights A/B/C over subject / addresses / body) |
| `indexes.sql` | GIN on `tsv` + the two btree indexes, built **after** the bulk load |
| `gen/` | Deterministic corpus generator (Go), emits PostgreSQL COPY text on stdout |
| `bench/` | Benchmark driver (Go + pgx): correctness, latency, EXPLAIN, inserts, concurrency |
| `load.sh` | End-to-end load: schema → streaming COPY → index build → ANALYZE, with disk-safety checks |
| `run-bench.sh` | Runs the driver inside the container's network namespace (the DB publishes no ports) |
| `postgresql.conf` | The exact PG tuning used, so the numbers are reproducible |
| `logs/` | Raw output of every benchmark run, including the pre-tuning baselines |

## Corpus design

5,000,000 messages, seeded RNG (`-seed 20260808` ⇒ byte-identical output every run).

- **Accounts** — 89, mirroring the real Crash stress case. Account 1 = 1,000,000 messages
  (Gmail-class power user), account 2 = 500,000, account 3 = 250,000, accounts 4-89 share
  the remaining 3.25M log-normally (median account ≈ 27k). 10-15 mailboxes each, INBOX-heavy.
- **Dates** — spread over 10 years, denser toward the present (`u^2.2` inverse draw).
- **Text** — mixed Spanish/English (60/40), Zipfian draw (s≈1.05) over two embedded
  wordlists of common business/mail vocabulary, plus proper nouns, invoice numbers
  (`INV-2019-3312`), order codes, URLs and phone numbers. Subject 3-10 words; body
  log-normal, median ≈120 words, p99 ≈2000.
- **Correspondents** — pool of 50,000 synthetic addresses drawn Zipfian (s=0.9), so a few
  contacts dominate traffic the way they do in real mailboxes.
- **Flags** — ~30% unread (bit 0 clear), ~8% flagged, ~4% answered.

### Planted needles (correctness gate)

Latency numbers are meaningless if the queries return the wrong rows, so the corpus plants
exact-count needles that the `needles` mode asserts **before** anything is timed:

| Needle | Expected count |
|---|---|
| token `zanzibarita` | 37 corpus-wide (10 in account 1, 8 in account 2, 5 in account 3, rest scattered) |
| token `INV-2024-0857` | exactly 1 (the random invoice generator deliberately never emits year 2024) |
| phrase `quetzal ferroviario nocturno` | exactly 5 |

## Text search configuration

`'simple'` + `unaccent`, **no stemming**. For mixed Spanish/English LATAM mailboxes a single
stemmer would mangle the other language, and mail bodies are full of product names, invoice
codes and URLs that stem badly. Trade-off: `factura` and `facturas` are distinct lexemes, so
morphological recall depends on the client issuing prefix queries (shape #5).

`unaccent()` is only STABLE, so it cannot appear in a generated column directly; `schema.sql`
defines an IMMUTABLE `immutable_unaccent()` wrapper that pins the dictionary by name.

## Reproducing

Prerequisites: Docker, ~30 GB free disk, a box with ≥8 GB RAM to spare.

```bash
# 1. Postgres 17 with the spike's tuning
docker run -d --name moov-s3-pg --cpus=6 --memory=12g --shm-size=2g \
  -e POSTGRES_PASSWORD=s3bench_throwaway \
  -v "$PWD/pgdata:/var/lib/postgresql/data" \
  -v "$PWD:/work" \
  -v "$PWD/postgresql.conf:/etc/postgresql/postgresql.conf:ro" \
  postgres:17-bookworm -c config_file=/etc/postgresql/postgresql.conf
docker exec moov-s3-pg psql -U postgres -c 'CREATE DATABASE moov_s3;'

# 2. Build the tools (no Go toolchain needed on the host)
docker run --rm -v "$PWD:/src" -w /src/gen   -e GOFLAGS=-mod=mod golang:1.24-bookworm go build -o /src/scripts/gen .
docker run --rm -v "$PWD:/src" -w /src/bench -e GOFLAGS=-mod=mod golang:1.24-bookworm go build -o /src/scripts/bench .

# 3. Load 5M messages + build indexes (tens of minutes — run detached)
nohup bash ./load.sh > logs/load.log 2>&1 &

# 4. Correctness gate, then the measurements
./run-bench.sh needles       # MUST pass before trusting any latency
./run-bench.sh sizes
./run-bench.sh warm   -runs 50 -warmups 5
./run-bench.sh explain
./run-bench.sh inserts
./run-bench.sh concurrency -clients 8 -duration 60s
./run-bench.sh concurrency -clients 8 -duration 60s -mix interactive
./run-bench.sh mitigation -runs 30

# cold-cache pass — drop BOTH PostgreSQL's buffers and the OS page cache
docker restart moov-s3-pg && sleep 15 && sync && echo 3 > /proc/sys/vm/drop_caches
./run-bench.sh cold
```

`run-bench.sh` runs the driver inside the Postgres container's network namespace, because
the container deliberately publishes no ports — the database is reachable only from inside
the box, and the password is a throwaway.

To reproduce the *pre-tuning* baselines (i.e. to see the failures the tuning fixes), pass
`-custom-plan=false` and/or drop `messages_acct_tsv_gin` before running `warm`.

## Cleaning up

The corpus is ~23 GB. On a shared or production box, remove it when finished:

```bash
docker rm -f moov-s3-pg && rm -rf ./pgdata
```

## Query shapes

Ten shapes in `bench/queries.go`, each scoped `WHERE account_id = $1` and run against both
the 1M-message account and the median (~27k) account. Shape #1 (common word +
`ORDER BY date DESC LIMIT 50`) is the decisive one: GIN cannot provide ordering, so the
planner must either bitmap-scan every match and sort, or walk the date index and filter.
Shapes #101-103 are mitigations, measured only because #1 needs them.
